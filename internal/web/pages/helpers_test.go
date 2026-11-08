package pages_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/web"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/web/pages"
	"github.com/jchen0426/agent-swarm/internal/web/webtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The deployment the tests serve.
const (
	issuer   = "https://idp.example.com"
	clientID = "agentswarm"
	keyRef   = "secret://web/session"
)

// secrets is a secret store holding the session key alone.
type secrets struct{ value string }

func (s *secrets) Secret(_ context.Context, ref string) (string, error) {
	if ref != keyRef || s.value == "" {
		return "", errors.New("no such secret")
	}
	return s.value, nil
}

// act is one call of an item act the coordinator received.
type act struct {
	kind    app.ActKind
	id      item.ID
	key     string
	reason  *string
	preview bool
}

// coordinator is a coordinator of items whose openings the test sets. It
// decides who may act through app.May, as the coordinator does, records a
// key once, as the coordinator's key lookup does (a repeat changes nothing
// and answers changed), and answers the errors the test queues, first those
// of previews, then those of commits.
type coordinator struct {
	mu        sync.Mutex
	opened    map[item.ID]*item.Opened
	calls     []act
	recorded  []act
	keys      map[string]bool
	previewEr []error
	commitEr  []error
	switches  int
	consequen app.Consequences
}

func newCoordinator() *coordinator {
	return &coordinator{opened: map[item.ID]*item.Opened{}, keys: map[string]bool{}}
}

func (c *coordinator) Promote(_ context.Context, id item.ID, a *app.Act) (app.Outcome, error) {
	return c.act(app.ActPromote, id, a)
}

func (c *coordinator) Close(_ context.Context, id item.ID, a *app.Act) (app.Outcome, error) {
	return c.act(app.ActClose, id, a)
}

func (c *coordinator) Reopen(_ context.Context, id item.ID, a *app.Act) (app.Outcome, error) {
	return c.act(app.ActReopen, id, a)
}

func (c *coordinator) act(kind app.ActKind, id item.ID, a *app.Act) (app.Outcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	call := act{kind: kind, id: id, key: a.Key, preview: a.Preview}
	if a.Reason != nil {
		r := a.Reason.Code + ":" + a.Reason.Text
		call.reason = &r
	}
	c.calls = append(c.calls, call)
	queue := &c.commitEr
	if a.Preview {
		queue = &c.previewEr
	}
	if len(*queue) > 0 {
		err := (*queue)[0]
		*queue = (*queue)[1:]
		if err != nil {
			return app.Outcome{}, err
		}
	}
	o, ok := c.opened[id]
	if !ok {
		return app.Outcome{}, app.ErrNoItem
	}
	if _, err := app.May(a.Actor, kind, o); err != nil {
		return app.Outcome{}, err
	}
	if a.Preview {
		return app.Outcome{Consequences: c.consequen}, nil
	}
	if c.keys[a.Key] {
		return app.Outcome{Changed: true}, nil
	}
	c.keys[a.Key] = true
	c.recorded = append(c.recorded, call)
	return app.Outcome{Changed: true, Consequences: c.consequen}, nil
}

func (c *coordinator) SetKillSwitch(context.Context, bool, *app.Act) (app.KillSwitchOutcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.switches++
	return app.KillSwitchOutcome{}, nil
}

func (c *coordinator) RecordKillSwitchOn(context.Context, int64, *app.Act) (app.KillSwitchOutcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.switches++
	return app.KillSwitchOutcome{}, nil
}

// queue queues errors for previews, or for commits.
func (c *coordinator) queue(preview bool, errs ...error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if preview {
		c.previewEr = append(c.previewEr, errs...)
		return
	}
	c.commitEr = append(c.commitEr, errs...)
}

func (c *coordinator) open(id item.ID, principal string, responders ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opened[id] = &item.Opened{Principal: principal, Responders: responders}
}

// commits are the calls that were not previews.
func (c *coordinator) commits() []act {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []act
	for _, a := range c.calls {
		if !a.preview {
			out = append(out, a)
		}
	}
	return out
}

func (c *coordinator) previews() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, a := range c.calls {
		if a.preview {
			n++
		}
	}
	return n
}

func (c *coordinator) records() []act {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]act(nil), c.recorded...)
}

func (c *coordinator) switched() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.switches
}

// views are the items list and the kill switch's table the test sets.
type views struct {
	mu      sync.Mutex
	rows    []app.Row
	listErr error
	ks      app.KillSwitch
	ksErr   error
	ksReads int
}

func (v *views) List(context.Context) ([]app.Row, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]app.Row(nil), v.rows...), v.listErr
}

func (v *views) KillSwitch(context.Context) (app.KillSwitch, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.ksReads++
	return v.ks, v.ksErr
}

func (v *views) reads() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.ksReads
}

// leader is the leader's status the test sets.
type leader struct {
	mu             sync.Mutex
	held, draining bool
	epoch          int64
}

func (l *leader) Status() (epoch int64, held, draining bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.epoch, l.held, l.draining
}

func (l *leader) set(held, draining bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held, l.draining = held, draining
}

// holder is the lease's holder the test sets.
type holder struct {
	mu      sync.Mutex
	name    string
	expires time.Time
	err     error
	// hang makes a read wait for its context's end, as a pool with no
	// connection free would.
	hang bool
}

func (h *holder) Holder(ctx context.Context) (string, time.Time, error) {
	h.mu.Lock()
	hang := h.hang
	h.mu.Unlock()
	if hang {
		<-ctx.Done()
		return "", time.Time{}, ctx.Err()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.name, h.expires, h.err
}

// world serves the pages over TLS through the real server, its public URL
// the server's own, with fakes behind the ports.
type world struct {
	srv    *httptest.Server
	public string
	keys   auth.Keys
	coord  *coordinator
	views  *views
	leader *leader
	holder *holder
}

func newWorld(t *testing.T) *world {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	w := &world{
		srv: srv, public: "https://" + srv.Listener.Addr().String(),
		coord: newCoordinator(), views: &views{}, leader: &leader{held: true, epoch: 1},
		holder: &holder{name: "instance-a", expires: time.Now().Add(time.Minute)},
	}
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	secret := base64.StdEncoding.EncodeToString(b)
	w.keys, err = auth.NewKeys(secret, issuer, clientID)
	require.NoError(t, err)
	log := slog.New(slog.DiscardHandler)
	a, err := auth.NewAuthenticator(deploy.Web{
		PublicURL: w.public,
		OIDC: deploy.OIDC{
			Issuer: issuer, ClientID: clientID, ClientSecretRef: "secret://web/client",
			SubjectClaim: "sub", GroupsClaim: "groups",
		},
		Groups:        deploy.Groups{Viewers: "viewers", Responders: "responders", Admins: "admins"},
		SessionKeyRef: keyRef, SessionLength: 8 * time.Hour, FreshWindow: 5 * time.Minute, TimeZone: "UTC",
	}, &secrets{value: secret}, nil, log)
	require.NoError(t, err)
	p := &web.Ports{
		Leader: w.leader, Coordinator: w.coord, Views: w.views,
		Switching:   switching.Ports{Coordinator: w.coord, Leader: w.leader, Log: log},
		LeaseHolder: w.holder, Tenant: "acme", Clock: time.Now, Log: log,
	}
	server := web.NewServer(p, web.Options{PublicURL: w.public, Log: log, Pages: pages.New(p, a, time.UTC)})
	srv.Config.Handler = server.Handler
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return w
}

// browser is a browser signed in as subject with roles, fresh.
func (w *world) browser(t *testing.T, subject string, roles ...auth.Role) *webtest.Browser {
	t.Helper()
	b := webtest.NewBrowser(t, w.srv.Client())
	b.SetCookie(w.public, webtest.SignedIn(t, w.keys, roles, subject))
	return b
}

// session is a session of subject with roles, authenticated authAgo ago
// and expiring in expiresIn, sealed as the session cookie.
func (w *world) session(t *testing.T, subject string, authAgo, expiresIn time.Duration, roles ...auth.Role) *http.Cookie {
	t.Helper()
	now := time.Now().Truncate(time.Second)
	s := auth.Session{
		Issuer: issuer, ClientID: clientID, Subject: subject, Roles: roles,
		AuthTime: now.Add(-authAgo), IssuedAt: now.Add(-authAgo), Expires: now.Add(expiresIn),
	}
	sealed, err := s.Seal(w.keys)
	require.NoError(t, err)
	return auth.SessionCookie(sealed, s.Expires)
}

// token is the CSRF token of the session c holds.
func (w *world) token(t *testing.T, c *http.Cookie) string {
	t.Helper()
	s, err := auth.Open(w.keys, c.Value)
	require.NoError(t, err)
	return w.keys.Token(&s)
}

// post posts form to path with cookie (nil for none), from the public
// origin, following no redirect.
func (w *world) post(t *testing.T, path string, cookie *http.Cookie, form url.Values) (resp *http.Response, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, w.public+path, strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", w.public)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return w.do(t, req)
}

// get gets path with cookie (nil for none), following no redirect.
func (w *world) get(t *testing.T, path string, cookie *http.Cookie) (resp *http.Response, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, w.public+path, http.NoBody)
	require.NoError(t, err)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return w.do(t, req)
}

func (w *world) do(t *testing.T, req *http.Request) (resp *http.Response, body string) {
	t.Helper()
	client := w.srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var b bytes.Buffer
	_, err = b.ReadFrom(resp.Body)
	require.NoError(t, err)
	return resp, b.String()
}

// form is the act form's values posted with key and its MAC for subject.
func (w *world) form(subject string, kind app.ActKind, id item.ID, key, csrf string) url.Values {
	return url.Values{
		"k": {key}, "mac": {w.keys.FormMAC(subject, string(kind), string(id), key)}, auth.TokenField: {csrf},
	}
}

// field is the value of the input named name in the form posted to action
// on body; empty when there is none.
func field(t *testing.T, body []byte, action, name string) string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.Data != "form" || attr(n, "action") != action {
			continue
		}
		for in := range n.Descendants() {
			if in.Type == html.ElementNode && in.Data == "input" && attr(in, "name") == name {
				return attr(in, "value")
			}
		}
	}
	return ""
}

// textarea is the text of the textarea named name in the form posted to
// action on body.
func textarea(t *testing.T, body []byte, action, name string) string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.Data != "form" || attr(n, "action") != action {
			continue
		}
		for in := range n.Descendants() {
			if in.Type == html.ElementNode && in.Data == "textarea" && attr(in, "name") == name {
				var b strings.Builder
				for c := range in.ChildNodes() {
					b.WriteString(c.Data)
				}
				return b.String()
			}
		}
	}
	return ""
}

// buttons are the values of the buttons named name in body.
func buttons(t *testing.T, body []byte, name string) []string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	var out []string
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "button" && attr(n, "name") == name {
			out = append(out, attr(n, "value"))
		}
	}
	return out
}

// links are the hrefs of body's links.
func links(t *testing.T, body []byte) []string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	var out []string
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "a" {
			out = append(out, attr(n, "href"))
		}
	}
	return out
}

// text is body's text, its tags dropped and its spaces folded.
func text(t *testing.T, body []byte) string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	var b strings.Builder
	for n := range doc.Descendants() {
		if n.Type == html.TextNode && (n.Parent == nil || (n.Parent.Data != "style" && n.Parent.Data != "title")) {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
