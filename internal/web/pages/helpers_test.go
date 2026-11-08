package pages_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
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

// The OIDC client's secret, which the fake IdP checks, and its reference.
const (
	clientSecretRef = "secret://web/client"
	clientSecret    = "the-client-secret"
)

// secrets is a secret store holding the session key and the OIDC client's
// secret.
type secrets struct{ value string }

func (s *secrets) Secret(_ context.Context, ref string) (string, error) {
	switch {
	case ref == clientSecretRef:
		return clientSecret, nil
	case ref != keyRef || s.value == "":
		return "", errors.New("no such secret")
	}
	return s.value, nil
}

// clock is a clock the test moves, which the authenticator and the pages
// read.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
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

	// The openings: the inputs Open received, the items they opened by id,
	// and the errors queued for them.
	openCalls []app.OpenInput
	openedBy  map[item.ID]string
	openEr    []error

	// The kill switch: the calls it received; the errors queued for its
	// previews, its commits and its records during a drain, each nil for
	// none; the preview's answer; the answers of a commit and of a record
	// with no error queued; and a hook run at each commit, as another
	// writer of the table would.
	ksCalls    []ksCall
	ksPreviewE []error
	ksCommitE  []error
	ksRecordE  []error
	ksPreview  app.KillSwitchOutcome
	ksAnswer   app.KillSwitchOutcome
	ksRecorded app.KillSwitchOutcome
	ksOnCommit func()
	// ksBlock, when set, holds each preview until it is closed.
	ksBlock chan struct{}
}

// ksCall is a call of the kill switch: SetKillSwitch's, or, record,
// RecordKillSwitchOn's with its epoch.
type ksCall struct {
	on, preview, record bool
	epoch, after        int64
	actor               app.Actor
	reason              *string
}

func newCoordinator() *coordinator {
	return &coordinator{opened: map[item.ID]*item.Opened{}, keys: map[string]bool{}, openedBy: map[item.ID]string{}}
}

// Open decides as the coordinator does, first the actor by app.May and the
// principal by checkOpener's rule (R78), then answers the error queued;
// it opens one item per id, and answers a repeat the same id (R85).
func (c *coordinator) Open(_ context.Context, in *app.OpenInput) (item.ID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.openCalls = append(c.openCalls, *in)
	if _, err := app.May(in.Actor, app.ActOpen, nil); err != nil {
		return "", err
	}
	if in.Principal != in.Actor.Subject && !in.Actor.Admin {
		return "", fmt.Errorf("%w: a responder opens items as their own principal", app.ErrInvalid)
	}
	if err := pop(&c.openEr); err != nil {
		return "", err
	}
	if in.ID == "" {
		return "", errors.New("no id")
	}
	if _, ok := c.openedBy[in.ID]; !ok {
		c.openedBy[in.ID] = in.Principal
	}
	return in.ID, nil
}

// openings are the inputs Open received.
func (c *coordinator) openings() []app.OpenInput {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]app.OpenInput(nil), c.openCalls...)
}

// openedItems counts the items opened.
func (c *coordinator) openedItems() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.openedBy)
}

// queueOpen queues errors for Open.
func (c *coordinator) queueOpen(errs ...error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.openEr = append(c.openEr, errs...)
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

// SetKillSwitch answers the error queued for the call, as the coordinator
// refuses an unheld call before it checks it; then refuses as checkSwitch
// does, by app.May (R79) and the on's reason; then answers the preview's
// outcome, or the commit's.
func (c *coordinator) SetKillSwitch(_ context.Context, on bool, a *app.Act) (app.KillSwitchOutcome, error) {
	c.mu.Lock()
	c.switches++
	c.ksCalls = append(c.ksCalls, newKsCall(on, a, false, 0))
	queue := &c.ksCommitE
	if a.Preview {
		queue = &c.ksPreviewE
	}
	hook, block := c.ksOnCommit, c.ksBlock
	err := pop(queue)
	c.mu.Unlock()
	if a.Preview && block != nil {
		<-block
	}
	if !a.Preview && hook != nil {
		hook()
	}
	if err != nil {
		return app.KillSwitchOutcome{}, err
	}
	if err := checkSwitch(on, a); err != nil {
		return app.KillSwitchOutcome{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if a.Preview {
		return c.ksPreview, nil
	}
	return c.ksAnswer, nil
}

// RecordKillSwitchOn checks as checkSwitch does, first, as the coordinator
// does (R79), then answers the error queued, or the recorded outcome.
func (c *coordinator) RecordKillSwitchOn(_ context.Context, epoch int64, a *app.Act) (app.KillSwitchOutcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.switches++
	c.ksCalls = append(c.ksCalls, newKsCall(true, a, true, epoch))
	if err := checkSwitch(true, a); err != nil {
		return app.KillSwitchOutcome{}, err
	}
	if err := pop(&c.ksRecordE); err != nil {
		return app.KillSwitchOutcome{}, err
	}
	return c.ksRecorded, nil
}

func newKsCall(on bool, a *app.Act, record bool, epoch int64) ksCall {
	call := ksCall{on: on, preview: a.Preview, record: record, epoch: epoch, after: a.After, actor: a.Actor}
	if a.Reason != nil {
		r := a.Reason.Code + ":" + a.Reason.Text
		call.reason = &r
	}
	return call
}

// checkSwitch refuses as the coordinator's checkSwitch does: the role by
// app.May, then an on's reason, but for a preview.
func checkSwitch(on bool, a *app.Act) error {
	kind := app.ActSwitchOff
	if on {
		kind = app.ActSwitchOn
	}
	if _, err := app.May(a.Actor, kind, nil); err != nil {
		return err
	}
	if on && a.Reason == nil && !a.Preview {
		return &app.Refused{Code: app.CodeReasonRequired}
	}
	return nil
}

// pop takes the first error queued, nil for none.
func pop(queue *[]error) error {
	if len(*queue) == 0 {
		return nil
	}
	err := (*queue)[0]
	*queue = (*queue)[1:]
	return err
}

// switchCommits are the kill switch's calls that were not previews.
func (c *coordinator) switchCommits() []ksCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []ksCall
	for _, k := range c.ksCalls {
		if !k.preview {
			out = append(out, k)
		}
	}
	return out
}

// switchPreviews counts the kill switch's previews.
func (c *coordinator) switchPreviews() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, k := range c.ksCalls {
		if k.preview {
			n++
		}
	}
	return n
}

// queueSwitch queues errors for the kill switch's previews, commits or
// records.
func (c *coordinator) queueSwitch(queue *[]error, errs ...error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	*queue = append(*queue, errs...)
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
	// unstaffed are the items each on-row left not staffed; unstaffedErr
	// fails their read.
	lists        int
	unstaffed    map[int64][]item.ID
	unstaffedErr error
	// items are the item views by id; itemErr fails their read.
	items   map[item.ID]app.View
	itemErr error
}

func (v *views) Item(_ context.Context, id item.ID) (app.View, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.itemErr != nil {
		return app.View{}, v.itemErr
	}
	view, ok := v.items[id]
	if !ok {
		return app.View{}, app.ErrNoItem
	}
	return view, nil
}

// setItem sets an item's view.
func (v *views) setItem(view *app.View) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.items == nil {
		v.items = map[item.ID]app.View{}
	}
	v.items[view.ID] = *view
}

// revisions give a revision of two work item types.
type revisions struct {
	mu  sync.Mutex
	rev *config.Revision
}

func (r *revisions) Current() *config.Revision {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rev
}

// set sets the current revision, nil for none.
func (r *revisions) set(rev *config.Revision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rev = rev
}

// revision is a revision of the work item types incident and change.
func revision(t *testing.T) *config.Revision {
	t.Helper()
	rev, err := config.Build(fstest.MapFS{
		"work-item-types/incident.yaml": {Data: []byte("kind: WorkItemType\nmetadata: {name: incident, owner: sre-leads}\nspec:\n  hypothesisKey:\n    - {name: service, values: [checkout]}\n")},
		"work-item-types/change.yaml":   {Data: []byte("kind: WorkItemType\nmetadata: {name: change, owner: sre-leads}\nspec:\n  hypothesisKey:\n    - {name: service, values: [checkout]}\n")},
	}, "c1", nil)
	require.NoError(t, err)
	return rev
}

func (v *views) UnstaffedBy(_ context.Context, seq int64) ([]item.ID, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]item.ID(nil), v.unstaffed[seq]...), v.unstaffedErr
}

// setSwitch sets the kill switch's table.
func (v *views) setSwitch(ks *app.KillSwitch) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.ks = *ks
}

func (v *views) List(context.Context) ([]app.Row, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.lists++
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

// logLines is a log's output, safe for the server's goroutines.
type logLines struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logLines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
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
	// logs are the pages' log, at Info and above.
	logs *logLines
	revs *revisions
	// zone is the zone the pages show times in; nil is UTC.
	zone *time.Location
	// ca, idp and clock are set by newIDPWorld alone.
	ca    *webtest.CA
	idp   *webtest.IDP
	clock *clock
}

func newWorld(t *testing.T) *world {
	t.Helper()
	return build(t, httptest.NewUnstartedServer(nil), nil)
}

// newIDPWorld is a world whose server's certificate a test CA issues, and
// whose authenticator signs in at a fake IdP under that CA, on a clock the
// test moves.
func newIDPWorld(t *testing.T) *world {
	t.Helper()
	ca := webtest.NewCA(t)
	srv := ca.UnstartedServer(t, nil)
	public := "https://" + srv.Listener.Addr().String()
	idp := webtest.NewIDP(t, ca, webtest.Client{ID: clientID, Secret: clientSecret, RedirectURL: public + "/auth/callback"})
	c := &clock{now: time.Now().Truncate(time.Second)}
	idp.SetClock(c.Now)
	w := build(t, srv, func(w *world, d *deploy.Web) {
		w.ca, w.idp, w.clock = ca, idp, c
		d.OIDC.Issuer = idp.URL
		d.OIDC.CABundle = ca.File(t)
	})
	return w
}

// build is a world served by srv, its deployment changed by change.
func build(t *testing.T, srv *httptest.Server, change func(w *world, d *deploy.Web)) *world {
	t.Helper()
	w := &world{
		srv: srv, public: "https://" + srv.Listener.Addr().String(),
		coord: newCoordinator(), views: &views{}, leader: &leader{held: true, epoch: 1},
		holder: &holder{name: "instance-a", expires: time.Now().Add(time.Minute)},
		logs:   &logLines{},
		revs:   &revisions{rev: revision(t)},
	}
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	secret := base64.StdEncoding.EncodeToString(b)
	w.keys, err = auth.NewKeys(secret, issuer, clientID)
	require.NoError(t, err)
	log := slog.New(slog.NewJSONHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	d := deploy.Web{
		PublicURL: w.public,
		OIDC: deploy.OIDC{
			Issuer: issuer, ClientID: clientID, ClientSecretRef: clientSecretRef,
			SubjectClaim: "sub", GroupsClaim: "groups",
		},
		Groups:        deploy.Groups{Viewers: "viewers", Responders: "responders", Admins: "admins"},
		SessionKeyRef: keyRef, SessionLength: 8 * time.Hour, FreshWindow: 5 * time.Minute, TimeZone: "UTC",
	}
	if change != nil {
		change(w, &d)
	}
	now := time.Now
	if w.clock != nil {
		now = w.clock.Now
	}
	if w.idp != nil {
		// The keys are bound to the issuer, which is the fake IdP's.
		w.keys, err = auth.NewKeys(secret, d.OIDC.Issuer, clientID)
		require.NoError(t, err)
	}
	a, err := auth.NewAuthenticator(d, &secrets{value: secret}, now, log)
	require.NoError(t, err)
	p := &web.Ports{
		Leader: w.leader, Coordinator: w.coord, Views: w.views,
		Switching:   switching.Ports{Coordinator: w.coord, Leader: w.leader, Log: log},
		LeaseHolder: w.holder, Revisions: w.revs, Tenant: "acme", Clock: now, Log: log,
	}
	server := web.NewServer(p, web.Options{PublicURL: w.public, Log: log, Pages: pages.New(p, a, w.zone)})
	srv.Config.Handler = server.Handler
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return w
}

// browser is a browser signed in as subject with roles, fresh.
func (w *world) browser(t *testing.T, subject string, roles ...auth.Role) *webtest.Browser {
	t.Helper()
	b := webtest.NewBrowser(t, w.client())
	b.SetCookie(w.public, webtest.SignedIn(t, w.keys, roles, subject))
	return b
}

// client is a client that trusts the server's certificate.
func (w *world) client() *http.Client {
	if w.ca != nil {
		return w.ca.Client()
	}
	return w.srv.Client()
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
	client := w.client()
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

// newZoneWorld is a world whose pages show times in loc.
func newZoneWorld(t *testing.T, loc *time.Location) *world {
	t.Helper()
	return build(t, httptest.NewUnstartedServer(nil), func(w *world, _ *deploy.Web) { w.zone = loc })
}

func (v *views) listCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.lists
}
