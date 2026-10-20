// Package upstream reaches registered MCP servers for the gateway. It
// implements app.Upstream: a call goes over the connection of its run, agent
// and server, and a listing for verification over a session of its own.
//
// Every HTTP exchange is watched: a request that could not be sent or got a
// 5xx answer is a connection failure; an answer longer than the cap is cut
// and refused; a 401 or 403 makes the credential be read again once. A
// credential is read from the secret store when it is used, kept for a
// minute, and sent only to the registered endpoint, never over plain HTTP.
package upstream

import (
	"cmp"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/output"
	"github.com/jchen0426/agent-swarm/internal/registry/mcptools"
)

// Secrets resolves a secret reference when it is used.
type Secrets interface {
	Secret(ctx context.Context, ref string) (string, error)
}

// Options bound the pool.
type Options struct {
	// MaxResponseBytes caps each HTTP answer a server sends.
	MaxResponseBytes int64
	// IdleTimeout closes a connection unused for that long.
	IdleTimeout time.Duration
	// CredentialTTL is how long a credential read from the store is kept.
	CredentialTTL time.Duration
	// Transport makes the HTTP requests; nil means http.DefaultTransport.
	Transport http.RoundTripper
	// RootCAs are the roots a server's certificate is checked against, in
	// place of the system roots; nil means the system roots alone. A
	// deployment that trusts a CA of its own besides the system roots makes
	// the pool with mcptools.SystemRootsWith. They apply to the transport the
	// pool makes for itself, so they have no effect when Transport is set.
	RootCAs *x509.CertPool
}

// Defaults are the pool's limits: answers of at most 8 MiB, connections
// closed after 10 idle minutes, and credentials kept for a minute.
var Defaults = Options{MaxResponseBytes: 8 << 20, IdleTimeout: 10 * time.Minute, CredentialTTL: time.Minute}

// maxServerMessage caps the text of an error a server answers with, as the
// pool keeps it. The gateway redacts it and then cuts it shorter, so the cap
// here only bounds memory.
const maxServerMessage = 64 << 10

// localCodes are the JSON-RPC error codes the SDK gives errors of its own
// making: the client or the server closing, and a request the transport
// could not deliver. No server answered them.
var localCodes = map[int64]bool{-32003: true, -32004: true, -32005: true}

// Pool keeps one client session per connection.
type Pool struct {
	secrets Secrets
	opts    Options
	now     func() time.Time

	mu    sync.Mutex
	conns map[app.Conn]*conn
	creds map[string]credential
	// closing counts the sessions being closed in the background.
	closing sync.WaitGroup
	// own is the transport made for RootCAs, whose idle connections Close
	// closes.
	own *http.Transport
}

type conn struct {
	session  *mcp.ClientSession
	lastUsed time.Time
}

type credential struct {
	value string
	read  time.Time
}

// New returns an empty pool.
func New(secrets Secrets, opts Options, now func() time.Time) *Pool {
	p := &Pool{secrets: secrets, opts: opts, now: now, conns: map[app.Conn]*conn{}, creds: map[string]credential{}}
	if opts.Transport == nil && opts.RootCAs != nil {
		p.own = mcptools.TrustingTransport(opts.RootCAs)
		p.opts.Transport = p.own
	}
	return p
}

// Close closes every connection, and waits until the sessions being closed
// are.
func (p *Pool) Close() {
	p.mu.Lock()
	var closing []*mcp.ClientSession
	for k, c := range p.conns {
		closing = append(closing, c.session)
		delete(p.conns, k)
	}
	p.mu.Unlock()
	for _, s := range closing {
		p.release(s)
	}
	p.closing.Wait()
	if p.own != nil {
		p.own.CloseIdleConnections()
	}
}

// release closes a session in the background. Closing one sends the server a
// request, which a slow server may hold for seconds, and no call or look
// waits for that. The request reads the credential under p.mu, so release is
// never called with p.mu held.
func (p *Pool) release(s *mcp.ClientSession) {
	p.closing.Go(func() { _ = s.Close() })
}

// Offered lists the server's tools on a session of its own, closed after.
func (p *Pool) Offered(ctx context.Context, s *app.Server) (map[string]app.Offer, error) {
	obs := &observation{}
	ctx = context.WithValue(ctx, observed{}, obs)
	session, err := p.connect(ctx, s)
	if err != nil {
		return nil, p.classify(ctx, obs, err)
	}
	defer p.release(session)
	listed, err := mcptools.Offered(ctx, session)
	if err != nil {
		return nil, p.classify(ctx, obs, err)
	}
	out := make(map[string]app.Offer, len(listed))
	for name := range listed {
		out[name] = app.Offer{Def: listed[name].Def, Err: listed[name].Err}
	}
	return out, nil
}

// Call calls a tool on the connection. A server that refuses the credential
// gets one more try with the credential read again.
func (p *Pool) Call(ctx context.Context, c *app.Conn, tool string, args json.RawMessage) (*output.Result, error) {
	for attempt := 0; ; attempt++ {
		obs := &observation{}
		callCtx := context.WithValue(ctx, observed{}, obs)
		cn, err := p.get(callCtx, c)
		if err != nil {
			return nil, p.classify(callCtx, obs, err)
		}
		res, err := cn.session.CallTool(callCtx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if obs.unauthorized() && attempt == 0 {
			p.forget(c)
			continue
		}
		if err != nil {
			var wire *jsonrpc.Error
			if errors.As(err, &wire) && !localCodes[wire.Code] && !obs.any() {
				return nil, &app.ServerError{Message: truncate(wire.Message, maxServerMessage)}
			}
			p.drop(c, cn)
			return nil, p.classify(callCtx, obs, err)
		}
		if res.NeedsInput() {
			return nil, app.ErrNeedsInput
		}
		return result(res), nil
	}
}

// classify turns a failed exchange into the gateway's errors.
func (p *Pool) classify(ctx context.Context, obs *observation, err error) error {
	switch {
	case obs.tooLarge():
		return app.ErrTooLarge
	case obs.failed():
		return fmt.Errorf("%w: %w", app.ErrConnection, err)
	case ctx.Err() != nil:
		return ctx.Err()
	case obs.unauthorized():
		return &app.ServerError{Message: "the server refused the gateway's credential"}
	}
	return err
}

// get returns the connection's session, connecting when there is none. It
// closes connections that have been idle too long.
func (p *Pool) get(ctx context.Context, c *app.Conn) (*conn, error) {
	now := p.now()
	p.mu.Lock()
	var idle []*mcp.ClientSession
	for k, cn := range p.conns {
		if k != *c && now.Sub(cn.lastUsed) > p.opts.IdleTimeout {
			idle = append(idle, cn.session)
			delete(p.conns, k)
		}
	}
	cn := p.conns[*c]
	if cn != nil {
		cn.lastUsed = now
	}
	p.mu.Unlock()
	for _, s := range idle {
		p.release(s)
	}
	if cn != nil {
		return cn, nil
	}
	session, err := p.connect(ctx, &c.Server)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if old := p.conns[*c]; old != nil {
		p.mu.Unlock()
		p.release(session)
		return old, nil
	}
	cn = &conn{session: session, lastUsed: now}
	p.conns[*c] = cn
	p.mu.Unlock()
	return cn, nil
}

// drop closes a connection whose session broke, so the next call connects
// again.
func (p *Pool) drop(c *app.Conn, cn *conn) {
	p.mu.Lock()
	if p.conns[*c] == cn {
		delete(p.conns, *c)
	}
	p.mu.Unlock()
	p.release(cn.session)
}

// forget drops the connection and its server's credential, which is read
// again when next used.
func (p *Pool) forget(c *app.Conn) {
	p.mu.Lock()
	cn := p.conns[*c]
	delete(p.conns, *c)
	delete(p.creds, c.Server.CredentialRef)
	p.mu.Unlock()
	if cn != nil {
		p.release(cn.session)
	}
}

func (p *Pool) connect(ctx context.Context, s *app.Server) (*mcp.ClientSession, error) {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("upstream: %s has no https endpoint; a credential is never sent in plain text", s.Name)
	}
	t := &transport{
		base: cmp.Or[http.RoundTripper](p.opts.Transport, http.DefaultTransport), origin: u.Scheme + "://" + u.Host,
		max: p.opts.MaxResponseBytes,
		authorization: func(ctx context.Context) (string, error) {
			if s.CredentialRef == "" {
				return "", nil
			}
			v, err := p.credential(ctx, s.CredentialRef)
			return "Bearer " + v, err
		},
	}
	session, err := mcptools.Client().Connect(ctx, mcptools.Transport(s.Endpoint, mcptools.NoRedirects(t)), nil)
	if err != nil {
		return nil, fmt.Errorf("upstream: connect to %s: %w", s.Name, err)
	}
	return session, nil
}

// credential returns a server's credential, read from the store at most a
// minute ago.
func (p *Pool) credential(ctx context.Context, ref string) (string, error) {
	p.mu.Lock()
	c, ok := p.creds[ref]
	p.mu.Unlock()
	if ok && p.now().Sub(c.read) < p.opts.CredentialTTL {
		return c.value, nil
	}
	v, err := p.secrets.Secret(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("upstream: read credential %s: %w", ref, err)
	}
	p.mu.Lock()
	p.creds[ref] = credential{value: v, read: p.now()}
	p.mu.Unlock()
	return v, nil
}

// observed keys a call's observation in its context.
type observed struct{}

// observation records what the HTTP exchanges of one call met.
type observation struct {
	mu                       sync.Mutex
	failure, large, rejected bool
}

func (o *observation) set(f func(o *observation)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	f(o)
}

func (o *observation) failed() bool       { o.mu.Lock(); defer o.mu.Unlock(); return o.failure }
func (o *observation) tooLarge() bool     { o.mu.Lock(); defer o.mu.Unlock(); return o.large }
func (o *observation) unauthorized() bool { o.mu.Lock(); defer o.mu.Unlock(); return o.rejected }
func (o *observation) any() bool          { return o.failed() || o.tooLarge() || o.unauthorized() }

// transport sends a server's requests: the credential only to the
// registered origin, every answer capped, and what happened noted in the
// call's observation.
type transport struct {
	base          http.RoundTripper
	origin        string
	max           int64
	authorization func(context.Context) (string, error)
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	obs, _ := r.Context().Value(observed{}).(*observation)
	if obs == nil {
		obs = &observation{}
	}
	if r.URL.Scheme+"://"+r.URL.Host != t.origin {
		return nil, errors.New("upstream: a request left the registered endpoint")
	}
	auth, err := t.authorization(r.Context())
	if err != nil {
		return nil, err
	}
	if auth != "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", auth)
	}
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		if r.Context().Err() == nil {
			obs.set(func(o *observation) { o.failure = true })
		}
		return nil, err
	}
	switch {
	case resp.StatusCode >= 500:
		obs.set(func(o *observation) { o.failure = true })
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		obs.set(func(o *observation) { o.rejected = true })
	}
	resp.Body = &capped{body: resp.Body, left: t.max, obs: obs}
	return resp, nil
}

// errTooLarge ends an answer longer than the cap.
var errTooLarge = errors.New("upstream: the answer is larger than the gateway reads")

// capped reads at most left bytes of body, then fails.
type capped struct {
	body io.ReadCloser
	left int64
	obs  *observation
}

func (c *capped) Read(b []byte) (int, error) {
	if c.left <= 0 {
		c.obs.set(func(o *observation) { o.large = true })
		return 0, errTooLarge
	}
	if int64(len(b)) > c.left {
		b = b[:c.left]
	}
	n, err := c.body.Read(b)
	c.left -= int64(n)
	return n, err
}

func (c *capped) Close() error { return c.body.Close() }

// result turns an SDK result into the gateway's. Links and the URIs and
// metadata of content are dropped: nobody reviewed them, and the gateway
// cannot fetch what they point to.
func result(res *mcp.CallToolResult) *output.Result {
	out := &output.Result{Structured: res.StructuredContent, IsError: res.IsError}
	for _, c := range res.Content {
		var b output.Block
		switch c := c.(type) {
		case *mcp.TextContent:
			b = output.Block{Kind: output.Text, Text: c.Text}
		case *mcp.ImageContent:
			b = output.Block{Kind: output.Image}
		case *mcp.AudioContent:
			b = output.Block{Kind: output.Audio}
		case *mcp.ResourceLink:
			b = output.Block{Kind: output.Link}
		case *mcp.EmbeddedResource:
			b = output.Block{Kind: output.Resource, Binary: true}
			if r := c.Resource; r != nil && r.Blob == nil {
				b.Text, b.Binary = r.Text, false
			}
		default:
			b = output.Block{Kind: "unknown"}
		}
		out.Blocks = append(out.Blocks, b)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
