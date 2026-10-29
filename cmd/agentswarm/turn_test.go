package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/outcome"
	"github.com/jchen0426/agent-swarm/internal/runner"
	"github.com/jchen0426/agent-swarm/internal/runner/spec"
)

// modelMode is how the Messages API's stand-in answers.
type modelMode int

const (
	modelEnds  modelMode = iota // the model calls end_turn
	modelHangs                  // every request is held until the client gives up
	modelReads                  // the model calls a read, again and again
	modelFails                  // every request fails, marked unavailable
)

// gateways stand in for the two a turn reaches: a Messages API whose model
// answers as mode says; and an MCP endpoint, without sessions as the
// platform's is, that lists a read and end_turn and answers them. Each keeps
// what it was sent.
type gateways struct {
	llm, mcp           *httptest.Server
	llmToken, mcpToken string
	mode               modelMode
	// sockets holds the Unix socket each stand-in serves on, by gateway,
	// "mcp" or "llm", when they serve on sockets.
	sockets map[string]string

	mu      sync.Mutex
	urls    []string      // each model request's URL, its path and query
	headers []http.Header // each model request's
	keys    []string      // the X-Api-Key of each model request
	bodies  [][]byte      // each model request's body
	bearers []string      // the Authorization of each MCP request
	protos  []int         // the ProtoMajor of each request, to either stand-in
}

// newGateways returns the stand-ins, each on a loopback port of its own, as
// the process runner's turns reach the gateways.
func newGateways(t *testing.T, mode modelMode) *gateways {
	t.Helper()
	g := &gateways{llmToken: "aswl_" + rand.Text(), mcpToken: "aswm_" + rand.Text(), mode: mode}
	g.llm = httptest.NewServer(http.HandlerFunc(g.answer))
	t.Cleanup(g.llm.Close)
	g.mcp = httptest.NewServer(g.endpoint())
	t.Cleanup(g.mcp.Close)
	return g
}

// newSocketGateways returns the stand-ins on Unix sockets in a short
// temporary directory, as a sandbox reaches the gateways (C6, version 2):
// over TLS by mcpTLS and llmTLS, or plain HTTP where one is nil. Their spec
// names the sockets, and URLs whose hosts do not resolve.
func newSocketGateways(t *testing.T, mode modelMode, mcpTLS, llmTLS *tls.Config) *gateways {
	t.Helper()
	g := &gateways{llmToken: "aswl_" + rand.Text(), mcpToken: "aswm_" + rand.Text(), mode: mode}
	dir := socketDir(t)
	g.sockets = map[string]string{"mcp": filepath.Join(dir, "mcp.sock"), "llm": filepath.Join(dir, "llm.sock")}
	g.llm = serveOn(t, g.sockets["llm"], http.HandlerFunc(g.answer), llmTLS)
	g.mcp = serveOn(t, g.sockets["mcp"], g.endpoint(), mcpTLS)
	return g
}

// socketDir returns a temporary directory whose sockets' paths fit the 104
// bytes macOS allows a socket's path, which t.TempDir's, named for the test,
// can pass.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "as-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// serveOn serves h on a new Unix socket at path: over TLS by config, or
// plain HTTP without one. A handshake that fails is for the test to see, not
// for its log.
func serveOn(t *testing.T, path string, h http.Handler, config *tls.Config) *httptest.Server {
	t.Helper()
	l, err := net.Listen("unix", path)
	require.NoError(t, err)
	s := httptest.NewUnstartedServer(h)
	require.NoError(t, s.Listener.Close())
	s.Listener = l
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	if config == nil {
		s.Start()
	} else {
		s.TLS = config
		s.StartTLS()
	}
	t.Cleanup(s.Close)
	return s
}

// gatewayNames are the names the gateways' certificate holds (Plan 7d-2,
// Decision 2), and the hosts of their URLs under the Docker runner.
var gatewayNames = []string{"mcp.agentswarm.internal", "llm.agentswarm.internal"}

// selfSigned returns a self-signed ECDSA P-256 certificate for names, as
// serve makes the gateways' (Plan 7d-2, Decision 2), and its PEM.
func selfSigned(t *testing.T, names ...string) (cert tls.Certificate, pemText string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		Subject:   pkix.Name{CommonName: names[0]},
		DNSNames:  names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// endpoint is the MCP endpoint's stand-in, which keeps each request's bearer.
func (g *gateways) endpoint() http.Handler {
	s := mcp.NewServer(&mcp.Implementation{Name: "agentswarm-gateway", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "end_turn", Description: "End your turn.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "source: board"}, &mcp.TextContent{Text: "your turn has ended"}}}, nil
		})
	s.AddTool(&mcp.Tool{Name: "logs__search", Description: "Search the logs.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "call_id: c-1"}, &mcp.TextContent{Text: "no errors"}}}, nil
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.bearers = append(g.bearers, r.Header.Get("Authorization"))
		g.protos = append(g.protos, r.ProtoMajor)
		g.mu.Unlock()
		h.ServeHTTP(w, r)
	})
}

func (g *gateways) answer(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.urls = append(g.urls, r.URL.String())
	g.headers = append(g.headers, r.Header.Clone())
	g.keys = append(g.keys, r.Header.Get("X-Api-Key"))
	g.bodies = append(g.bodies, body)
	g.protos = append(g.protos, r.ProtoMajor)
	n := len(g.bodies)
	g.mu.Unlock()
	tool := "end_turn"
	switch g.mode {
	case modelHangs:
		<-r.Context().Done()
		return
	case modelFails:
		w.Header().Set(outcome.Header, outcome.Unavailable)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"the gateway cannot serve this call now"}}`)
		return
	case modelReads:
		tool = "logs__search"
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"id":"msg_%d","type":"message","role":"assistant","model":"claude-sonnet-5-5",`+
		`"content":[{"type":"tool_use","id":"tu_%d","name":%q,"input":{}}],"stop_reason":"tool_use","stop_sequence":null,`+
		`"usage":{"input_tokens":10,"output_tokens":5}}`, n, n, tool)
}

func (g *gateways) asked() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.bodies)
}

// reached returns how many requests reached each stand-in's handler: the
// MCP endpoint's and the Messages API's.
func (g *gateways) reached() (tools, models int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.bearers), len(g.bodies)
}

// spec is a turn's spec (C6) for the gateways, as the runner writes it: of
// Plan 7d-1's form; or, when the stand-ins serve on sockets, of version 2,
// each gateway's entry naming its socket and a URL whose host does not
// resolve, as under the Docker runner. The spec has no cert: a test that
// serves TLS adds the one it means.
func (g *gateways) spec(deadline time.Time) map[string]any {
	s := map[string]any{
		"turn": "0193a5c8-0000-7000-8000-000000000001", "run": "0193a5c8-0000-7000-8000-000000000002", "agent": "alpha", "round": 2,
		"mcp":   map[string]string{"url": g.mcp.URL, "token": g.mcpToken},
		"llm":   map[string]string{"url": g.llm.URL, "token": g.llmToken},
		"model": "claude-sonnet-5-5", "system": []string{"You are alpha.", "Read the logs first."},
		"prompt": "Round 2: find the cause.", "deadline": deadline.Format(time.RFC3339Nano), "maxTokens": 4096,
	}
	if g.sockets != nil {
		s["v"] = 2
		s["mcp"] = map[string]string{"url": socketURL(g.mcp, "mcp.agentswarm.internal/"), "token": g.mcpToken, "socket": g.sockets["mcp"]}
		s["llm"] = map[string]string{"url": socketURL(g.llm, "llm.agentswarm.internal"), "token": g.llmToken, "socket": g.sockets["llm"]}
	}
	return s
}

// socketURL is the URL of a stand-in on a socket, at host: https when it
// serves TLS.
func socketURL(s *httptest.Server, host string) string {
	if s.TLS != nil {
		return "https://" + host
	}
	return "http://" + host
}

// sized returns the gateways' spec, encoded, with its skill grown so that it
// is n bytes long.
func (g *gateways) sized(t *testing.T, n int) []byte {
	t.Helper()
	s := g.spec(time.Now().Add(time.Minute))
	s["system"] = []string{""}
	short := len(encode(t, s))
	s["system"] = []string{strings.Repeat("x", n-short)}
	raw := encode(t, s)
	require.Len(t, raw, n)
	return raw
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

// playing is agentswarm turn running in the test's process.
type playing struct {
	input          *io.PipeWriter
	done           chan struct{}
	stdout, stderr bytes.Buffer
	code           int
}

// startTurn runs agentswarm turn with raw, its spec, on its standard input,
// which stays open until the turn exits or the test closes it, as the runner
// holds it open for the turn's life.
func startTurn(t *testing.T, raw []byte) *playing {
	t.Helper()
	r, w := io.Pipe()
	p := &playing{input: w, done: make(chan struct{})}
	go func() { _, _ = w.Write(raw) }()
	go func() {
		defer close(p.done)
		p.code = run([]string{"turn"}, r, &p.stdout, &p.stderr)
	}()
	t.Cleanup(func() {
		_ = w.Close()
		<-p.done
	})
	return p
}

// wait returns the turn's exit code, failing the test if it does not exit
// within timeout.
func (p *playing) wait(t *testing.T, timeout time.Duration) int {
	t.Helper()
	select {
	case <-p.done:
		return p.code
	case <-time.After(timeout):
		t.Fatalf("the turn did not exit within %v", timeout)
		return -1
	}
}

// The runtime takes everything it needs from the spec on its standard input:
// the gateways' URLs and tokens, the model, the skills, the prompt and
// max_tokens. Nothing of the SDK's environment reaches a request: not its
// model URL, its token or its headers (ADR-006, criteria 2 and 5). A spec it
// cannot take is refused before any request.
func TestTurnTakesItsSpecFromStandardInput(t *testing.T) {
	var trapped atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		trapped.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(trap.Close)
	// Without a key in the environment, the SDK's defaults would read its
	// token and its headers.
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_BASE_URL", trap.URL)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "trap-token")
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Canary: from-the-environment")
	t.Run("a spec", func(t *testing.T) {
		g := newGateways(t, modelEnds)

		p := startTurn(t, encode(t, g.spec(time.Now().Add(time.Minute))))

		require.Equal(t, outcome.ExitEnded, p.wait(t, 10*time.Second), p.stderr.String())
		var res runner.Result
		require.NoError(t, json.Unmarshal(p.stdout.Bytes(), &res), p.stdout.String())
		assert.Equal(t, runner.EndedByTool, res.Outcome)
		g.mu.Lock()
		defer g.mu.Unlock()
		require.Len(t, g.bodies, 1)
		assert.Equal(t, []string{g.llmToken}, g.keys, "the spec's LLM token, as the key")
		assert.Empty(t, g.headers[0].Get("Authorization"), "no token of the environment's")
		assert.Empty(t, g.headers[0].Get("X-Canary"), "no header of the environment's")
		// The LLM token travels in X-Api-Key and nowhere else: not in the URL,
		// the body or another header (resolution 1; Global Constraints,
		// secrets).
		assert.NotContains(t, g.urls[0], g.llmToken)
		assert.NotContains(t, string(g.bodies[0]), g.llmToken)
		for name, values := range g.headers[0] {
			if name != "X-Api-Key" {
				assert.NotContains(t, strings.Join(values, " "), g.llmToken, "header %s", name)
			}
		}
		var req struct {
			Model     string `json:"model"`
			MaxTokens int64  `json:"max_tokens"`
			System    []struct {
				Text string `json:"text"`
			} `json:"system"`
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.Unmarshal(g.bodies[0], &req))
		assert.Equal(t, "claude-sonnet-5-5", req.Model)
		assert.Equal(t, int64(4096), req.MaxTokens)
		require.Len(t, req.System, 2, "each skill its own system block")
		assert.Equal(t, "You are alpha.", req.System[0].Text)
		assert.Equal(t, "Read the logs first.", req.System[1].Text)
		require.Len(t, req.Messages, 1)
		assert.Equal(t, "Round 2: find the cause.", req.Messages[0].Content[0].Text)
		require.NotEmpty(t, g.bearers)
		for _, b := range g.bearers {
			assert.Equal(t, "Bearer "+g.mcpToken, b)
		}
		assert.Zero(t, trapped.Load(), "the environment's model URL and key reach nothing")
	})
	t.Run("a spec of 1 MiB, then a newline", func(t *testing.T) {
		g := newGateways(t, modelEnds)

		p := startTurn(t, append(g.sized(t, spec.MaxSize), '\n'))

		assert.Equal(t, outcome.ExitEnded, p.wait(t, 10*time.Second), p.stderr.String())
	})
	for _, c := range []struct {
		name string
		spec func(t *testing.T, g *gateways) []byte
		// ends closes standard input after the spec, which ends a spec that
		// is cut off.
		ends bool
		says string
	}{
		{name: "a spec over 1 MiB", says: "larger than 1 MiB", spec: func(t *testing.T, g *gateways) []byte {
			s := g.spec(time.Now().Add(time.Minute))
			s["system"] = []string{strings.Repeat("x", 1<<20)}
			return encode(t, s)
		}},
		{name: "a spec of 1 MiB and one byte", says: "larger than 1 MiB", spec: func(t *testing.T, g *gateways) []byte { return g.sized(t, spec.MaxSize+1) }},
		{name: "a spec cut off", says: "read the turn's spec", ends: true, spec: func(*testing.T, *gateways) []byte { return []byte(`{"turn": `) }},
		{name: "a spec that is not JSON", says: "read the turn's spec", spec: func(*testing.T, *gateways) []byte { return []byte(`turn`) }},
		{name: "a spec without the MCP gateway's token", says: "no token for the MCP gateway", spec: func(t *testing.T, g *gateways) []byte {
			s := g.spec(time.Now().Add(time.Minute))
			s["mcp"] = map[string]string{"url": g.mcp.URL}
			return encode(t, s)
		}},
		{name: "a spec without a deadline", says: "no deadline", spec: func(t *testing.T, g *gateways) []byte {
			s := g.spec(time.Now().Add(time.Minute))
			delete(s, "deadline")
			return encode(t, s)
		}},
		{name: "a spec of a version the runtime does not read", says: "version 3", spec: func(t *testing.T, g *gateways) []byte {
			s := g.spec(time.Now().Add(time.Minute))
			s["v"] = 3
			return encode(t, s)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newGateways(t, modelEnds)
			raw := c.spec(t, g)

			p := startTurn(t, raw)
			if c.ends {
				require.NoError(t, p.input.Close())
			}

			code := p.wait(t, 10*time.Second)
			assert.Equal(t, exitError, code, "a crash: no class of the table")
			assert.Contains(t, p.stderr.String(), c.says)
			assert.Zero(t, g.asked(), "no request")
			assert.Empty(t, g.bearers, "no request")
			assert.NotContains(t, p.stderr.String(), g.llmToken)
			assert.NotContains(t, p.stderr.String(), g.mcpToken)
		})
	}
}

// holdOnRead makes the MCP endpoint's stand-in one that issues sessions, as
// the scripted servers do, whose read hangs until its caller gives up, and
// which holds every DELETE and cancel notice until the test ends: a client
// that waited for either would wait out its own bound. It returns a channel
// that closes when a read begins.
func (g *gateways) holdOnRead(t *testing.T) <-chan struct{} {
	t.Helper()
	reading, over := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s := mcp.NewServer(&mcp.Implementation{Name: "agentswarm-gateway", Version: "1"}, nil)
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "server/discover" {
				// As the platform's endpoint answers: the client falls back to
				// initialize, and to a session.
				return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "no such method"}
			}
			return next(ctx, method, req)
		}
	})
	s.AddTool(&mcp.Tool{Name: "logs__search", Description: "Search the logs.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			once.Do(func() { close(reading) })
			<-ctx.Done()
			return nil, ctx.Err()
		})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if r.Method == http.MethodDelete || bytes.Contains(body, []byte(`"notifications/cancelled"`)) {
			select {
			case <-over:
			case <-r.Context().Done():
			}
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		close(over)
		srv.Close()
	})
	g.mcp = srv
	return reading
}

// The runner holds the runtime's standard input open for the turn's life:
// its end, as when the runner or its parent dies, ends the turn at once,
// interrupted (Decision 12), whatever call is under way: nothing the turn
// sends on its way out is waited for.
func TestTurnExitsWhenItsStandardInputCloses(t *testing.T) {
	for _, c := range []struct {
		name string
		// stand returns the gateways' stand-ins, and whether the call that
		// the turn's input ends during has begun.
		stand func(t *testing.T) (*gateways, func() bool)
	}{
		{name: "during a model call", stand: func(t *testing.T) (*gateways, func() bool) {
			g := newGateways(t, modelHangs)
			return g, func() bool { return g.asked() > 0 }
		}},
		{name: "during a tool call", stand: func(t *testing.T) (*gateways, func() bool) {
			g := newGateways(t, modelReads)
			reading := g.holdOnRead(t)
			return g, func() bool {
				select {
				case <-reading:
					return true
				default:
					return false
				}
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, begun := c.stand(t)
			p := startTurn(t, encode(t, g.spec(time.Now().Add(time.Minute))))
			require.Eventually(t, begun, 10*time.Second, 10*time.Millisecond, "the call begins")

			closed := time.Now()
			require.NoError(t, p.input.Close())
			code := p.wait(t, 15*time.Second)

			assert.Equal(t, outcome.ExitInterrupted, code, p.stderr.String())
			assert.Less(t, time.Since(closed), time.Second)
		})
	}
}

// validSpec is a spec the runtime takes, for tests of how it builds its
// clients.
func validSpec(deadline time.Time) *spec.Spec {
	return &spec.Spec{
		V:   spec.V2,
		MCP: spec.Gateway{URL: "http://127.0.0.1:53001/", Token: "aswm_test"}, LLM: spec.Gateway{URL: "http://127.0.0.1:53002", Token: "aswl_test"},
		Model: "claude-sonnet-5-5", System: []string{"You are alpha."}, Prompt: "Find the cause.", Deadline: deadline, MaxTokens: 4096,
	}
}

// The runtime reaches each gateway directly, on a transport of the
// gateway's own (R2): a proxy the environment names would see the gateways'
// tokens (Decision 12), and HTTP/1.1 is the one protocol it speaks (Plan
// 7d-2, Decision 2), over TLS too, where its own TLS configuration offers no
// h2, with a cert in the spec or without one.
func TestTheRuntimesTransportsIgnoreProxies(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")

	rt := newTurnRuntime(validSpec(time.Now().Add(time.Minute)), time.Now())

	for name, gw := range map[string]gatewayClient{"mcp": rt.mcp, "llm": rt.llm} {
		assert.Nil(t, gw.transport.Proxy, name)
		require.NotNil(t, gw.transport.Protocols, name)
		assert.True(t, gw.transport.Protocols.HTTP1(), name)
		assert.False(t, gw.transport.Protocols.HTTP2(), name)
		assert.False(t, gw.transport.Protocols.UnencryptedHTTP2(), name)
		require.NotNil(t, gw.transport.TLSClientConfig, name)
		assert.NotContains(t, gw.transport.TLSClientConfig.NextProtos, "h2", name)
		assert.Same(t, gw.transport, gw.client.Transport, "%s: the client uses its gateway's transport", name)
	}
	assert.NotSame(t, rt.mcp.transport, rt.llm.transport, "a transport per gateway")
}

// Without a cert in its spec, as in every spec of version 1, a gateway's
// transport speaks HTTP/1.1 to an https URL too (Decision 9): a server that
// offers HTTP/2 as well answers it in HTTP/1.1. A transport that offered h2
// over ALPN, and then wrote HTTP/1.1 on the connection, would get no answer.
func TestTheRuntimeSpeaksHTTP11OverTLSWithoutACert(t *testing.T) {
	var mu sync.Mutex
	var protos []int
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		protos = append(protos, r.ProtoMajor)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	srv.EnableHTTP2 = true
	srv.TLS = &tls.Config{NextProtos: []string{"h2", "http/1.1"}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	// A client that asks for HTTP/2 gets it: the server offers it.
	conn, err := tls.Dial("tcp", srv.Listener.Addr().String(), &tls.Config{RootCAs: roots, NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS12})
	require.NoError(t, err)
	assert.Equal(t, "h2", conn.ConnectionState().NegotiatedProtocol)
	require.NoError(t, conn.Close())
	transport := gatewayTransport(spec.Gateway{URL: srv.URL, Token: "aswl_test"}, nil)
	// The test's one seam: it trusts the server's certificate, and leaves
	// ALPN alone.
	transport.TLSClientConfig.RootCAs = roots
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
	require.NoError(t, err)

	resp, err := (&http.Client{Transport: transport}).Do(req)

	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, 1, resp.ProtoMajor)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []int{1}, protos, "the server saw one request, in HTTP/1.1")
}

// The model client's request timeout runs to the spec's deadline, which
// ends the turn's context too, so no answer is cut short of the round: a
// round longer than the SDK's own default of ten minutes keeps its length.
func TestTheClientsTimeoutRunsToTheDeadline(t *testing.T) {
	now := time.Now()
	s := validSpec(now.Add(30 * time.Minute))

	rt := newTurnRuntime(s, now)

	timeout, err := anthropic.CalculateNonStreamingTimeout(int(s.MaxTokens), s.Model, rt.model.Options)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, timeout, s.Deadline.Sub(now))
}

// A gateway's redirect is not followed: it would carry a session's token to
// another server (Global Constraints, secrets). It is an answer that is not
// a success, so the turn ends unavailable.
func TestTheRuntimeFollowsNoRedirect(t *testing.T) {
	var trapped atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		trapped.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(trap.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, trap.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	for _, which := range []string{"llm", "mcp"} {
		t.Run(which, func(t *testing.T) {
			g := newGateways(t, modelEnds)
			s := g.spec(time.Now().Add(time.Minute))
			s[which] = map[string]string{"url": redirect.URL, "token": map[string]string{"llm": g.llmToken, "mcp": g.mcpToken}[which]}

			p := startTurn(t, encode(t, s))

			assert.Equal(t, outcome.ExitUnavailable, p.wait(t, 10*time.Second), p.stderr.String())
			assert.Zero(t, trapped.Load(), "nothing reached the redirect's target")
		})
	}
}

// The runtime tries a model call once: the LLM gateway retries the vendor
// itself (Plan 6b), and its answer that is not a success ends the turn.
func TestTheRuntimeTriesAModelCallOnce(t *testing.T) {
	g := newGateways(t, modelFails)

	p := startTurn(t, encode(t, g.spec(time.Now().Add(time.Minute))))

	assert.Equal(t, outcome.ExitUnavailable, p.wait(t, 10*time.Second), p.stderr.String())
	assert.Equal(t, 1, g.asked())
}

// The runtime's step limit is 50 model calls (R14).
func TestTheRuntimeStopsAtItsStepLimit(t *testing.T) {
	g := newGateways(t, modelReads)

	p := startTurn(t, encode(t, g.spec(time.Now().Add(time.Minute))))

	assert.Equal(t, outcome.ExitFinished, p.wait(t, 30*time.Second), p.stderr.String())
	assert.Equal(t, 50, g.asked())
	var res runner.Result
	require.NoError(t, json.Unmarshal(p.stdout.Bytes(), &res), p.stdout.String())
	assert.Equal(t, runner.EndedBySteps, res.Outcome)
}

// A gateway's socket, when the spec names one, is dialed for every
// connection to that gateway, whatever its URL's host: under the Docker
// runner the sandbox has no network, and the URLs' hosts resolve nowhere
// (C6, version 2).
func TestTheRuntimeDialsAGatewaysSocketWhateverItsURLsHost(t *testing.T) {
	g := newSocketGateways(t, modelEnds, nil, nil)

	p := startTurn(t, encode(t, g.spec(time.Now().Add(time.Minute))))

	assert.Equal(t, outcome.ExitEnded, p.wait(t, 10*time.Second), p.stderr.String())
	tools, models := g.reached()
	assert.Positive(t, tools, "the MCP gateway's socket was reached")
	assert.Positive(t, models, "the LLM gateway's socket was reached")
}

// With a cert in its spec, the runtime trusts that certificate alone for the
// gateways' https URLs, and checks each URL's host against it (Plan 7d-2,
// Decision 2): another certificate for the same names, or the right one for
// another name, is refused before any request, and the turn ends
// unavailable.
func TestTheRuntimeTrustsTheSpecsCertificateAlone(t *testing.T) {
	right, rightPEM := selfSigned(t, gatewayNames...)
	_, otherPEM := selfSigned(t, gatewayNames...)
	elsewhere, elsewherePEM := selfSigned(t, "elsewhere.agentswarm.internal")
	for _, c := range []struct {
		name   string
		served tls.Certificate // the stand-ins'
		cert   string          // the spec's
		want   int
	}{
		{name: "the certificate the stand-ins serve", served: right, cert: rightPEM, want: outcome.ExitEnded},
		{name: "another self-signed certificate for the same names", served: right, cert: otherPEM, want: outcome.ExitUnavailable},
		{name: "the certificate the stand-ins serve, for another name", served: elsewhere, cert: elsewherePEM, want: outcome.ExitUnavailable},
	} {
		t.Run(c.name, func(t *testing.T) {
			config := &tls.Config{Certificates: []tls.Certificate{c.served}}
			g := newSocketGateways(t, modelEnds, config, config)
			s := g.spec(time.Now().Add(time.Minute))
			s["cert"] = c.cert

			p := startTurn(t, encode(t, s))

			assert.Equal(t, c.want, p.wait(t, 10*time.Second), p.stderr.String())
			tools, models := g.reached()
			if c.want == outcome.ExitEnded {
				assert.Positive(t, tools)
				assert.Positive(t, models)
				return
			}
			assert.Zero(t, tools, "the MCP gateway's stand-in saw no request")
			assert.Zero(t, models, "the LLM gateway's stand-in saw no request")
		})
	}
}

// With a cert in its spec, the runtime speaks TLS 1.3 at least: a gateway
// that offers TLS 1.2 at most is refused before any request, and the turn
// ends unavailable. The same gateway at TLS 1.3 ends the turn ended, which
// shows that the refusal is the version's.
func TestTheRuntimeRequiresTLS13(t *testing.T) {
	served, certPEM := selfSigned(t, gatewayNames...)
	for _, c := range []struct {
		name  string
		which string // the stand-in whose highest version is limit
		limit uint16
		want  int
	}{
		{name: "the MCP gateway at TLS 1.2", which: "mcp", limit: tls.VersionTLS12, want: outcome.ExitUnavailable},
		{name: "the MCP gateway at TLS 1.3", which: "mcp", limit: tls.VersionTLS13, want: outcome.ExitEnded},
		{name: "the LLM gateway at TLS 1.2", which: "llm", limit: tls.VersionTLS12, want: outcome.ExitUnavailable},
		{name: "the LLM gateway at TLS 1.3", which: "llm", limit: tls.VersionTLS13, want: outcome.ExitEnded},
	} {
		t.Run(c.name, func(t *testing.T) {
			configs := map[string]*tls.Config{
				"mcp": {Certificates: []tls.Certificate{served}},
				"llm": {Certificates: []tls.Certificate{served}},
			}
			configs[c.which].MaxVersion = c.limit
			g := newSocketGateways(t, modelEnds, configs["mcp"], configs["llm"])
			s := g.spec(time.Now().Add(time.Minute))
			s["cert"] = certPEM

			p := startTurn(t, encode(t, s))

			assert.Equal(t, c.want, p.wait(t, 10*time.Second), p.stderr.String())
			tools, models := g.reached()
			seen := map[string]int{"mcp": tools, "llm": models}[c.which]
			if c.want == outcome.ExitEnded {
				assert.Positive(t, seen, "the stand-in served the turn")
				return
			}
			assert.Zero(t, seen, "the stand-in's handler saw no request")
		})
	}
}

// The runtime speaks HTTP/1.1 alone, even to a gateway that offers HTTP/2
// (Plan 7d-2, Decision 2): spec Section 8.2's bound on a request's header
// holds for HTTP/1.1 alone.
func TestTheRuntimeSpeaksHTTP11ToAServerThatOffersH2(t *testing.T) {
	served, certPEM := selfSigned(t, gatewayNames...)
	config := &tls.Config{Certificates: []tls.Certificate{served}, NextProtos: []string{"h2", "http/1.1"}}
	g := newSocketGateways(t, modelEnds, config, config)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM([]byte(certPEM)))
	for _, which := range []string{"mcp", "llm"} {
		// A client that asks for HTTP/2 gets it: the stand-ins offer it.
		conn, err := tls.Dial("unix", g.sockets[which], &tls.Config{
			RootCAs: roots, ServerName: which + ".agentswarm.internal", NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS13,
		})
		require.NoError(t, err, which)
		assert.Equal(t, "h2", conn.ConnectionState().NegotiatedProtocol, which)
		require.NoError(t, conn.Close())
	}
	s := g.spec(time.Now().Add(time.Minute))
	s["cert"] = certPEM

	p := startTurn(t, encode(t, s))

	assert.Equal(t, outcome.ExitEnded, p.wait(t, 10*time.Second), p.stderr.String())
	g.mu.Lock()
	defer g.mu.Unlock()
	require.NotEmpty(t, g.protos, "the stand-ins saw requests")
	for i, proto := range g.protos {
		assert.Equal(t, 1, proto, "request %d", i)
	}
}

// At SIGTERM, which only the engine or an operator sends, the turn ends at
// once, unavailable (R3): one line on standard error, and exit 15. Once the
// turn is over, its stop returns with no exit.
func TestOnTerminateExitsUnavailable(t *testing.T) {
	for _, c := range []struct {
		name    string
		sigterm bool // a SIGTERM comes; otherwise, the turn stops
		exits   []int
		stderr  string
	}{
		{name: "at SIGTERM", sigterm: true, exits: []int{outcome.ExitUnavailable}, stderr: "agentswarm: SIGTERM: the turn ends unavailable\n"},
		{name: "when the turn stops"},
	} {
		t.Run(c.name, func(t *testing.T) {
			signals, stop := make(chan os.Signal, 1), make(chan struct{})
			if c.sigterm {
				signals <- syscall.SIGTERM
				defer close(stop)
			} else {
				close(stop)
			}
			var stderr bytes.Buffer
			var exits []int
			returned := make(chan struct{})

			go func() {
				defer close(returned)
				onTerminate(signals, stop, &stderr, func(code int) { exits = append(exits, code) })
			}()

			select {
			case <-returned:
			case <-time.After(5 * time.Second):
				t.Fatal("onTerminate did not return")
			}
			assert.Equal(t, c.exits, exits)
			assert.Equal(t, c.stderr, stderr.String())
		})
	}
}

// The runtime's help says how a turn's end by a signal reads: SIGINT, which
// cancels the turn, ends it interrupted, 14; SIGTERM, which only the engine
// or an operator sends, ends it unavailable, 15; and a runner reads an end by
// a signal it did not send as unavailable (Contract C7).
func TestTheRuntimesHelpSaysHowSignalsRead(t *testing.T) {
	out, errOut, code := runCommand("turn", "--help")

	require.Equal(t, exitOK, code, errOut)
	assert.Contains(t, out, "\n  14  interrupted: the deadline, a gateway's cut, the session gone, the end\n      of standard input, or SIGINT\n")
	assert.Contains(t, out, "\n      or SIGTERM, which only the engine or an operator sends\n")
	assert.Contains(t, out, "\nAny other code is a crash, but a runner reads an end by SIGHUP, SIGINT, SIGKILL or SIGTERM it did not send as unavailable (Contract C7).\n")
}
