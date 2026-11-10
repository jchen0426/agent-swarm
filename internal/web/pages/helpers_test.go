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
	"strconv"
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
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/key"
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

// views are the items list and the kill switch's table the test sets: the
// admin API's views as the pages 9a-1 shipped outside the board read them,
// with no item's view, which no page reads (R162).
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
}

// boards are the board's reads the test sets, as the board watch answers
// them (app.BoardWatch): each item's read, by id, an id with none answering
// app.ErrNoItem; err, when set, fails every read; hang, when set, makes a
// read wait for its context's end, as a read whose probe or build outlasts
// its wait does, and fail with the context's error. Each failure answers
// tick, the facts of the tick the read had in hand, beside its error, as the
// watch does (Task 5's review, decision 6); zero, no tick. reads counts the
// reads.
type boards struct {
	mu    sync.Mutex
	items map[item.ID]app.BoardRead
	err   error
	hang  bool
	tick  app.BoardFacts
	reads int
}

func (b *boards) Board(ctx context.Context, id item.ID) (app.BoardRead, error) {
	b.mu.Lock()
	b.reads++
	read, ok := b.items[id]
	err, hang, failed := b.err, b.hang, app.BoardRead{Facts: b.tick}
	b.mu.Unlock()
	switch {
	case hang:
		<-ctx.Done()
		return failed, fmt.Errorf("wait for the projection of item %s's board: %w", id, ctx.Err())
	case err != nil:
		return failed, err
	case !ok:
		return failed, app.ErrNoItem
	}
	return read, nil
}

// inTick sets the facts of the tick a failed read had in hand, zero for
// none.
func (b *boards) inTick(f *app.BoardFacts) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tick = *f
}

// set sets item id's read.
func (b *boards) set(id item.ID, read *app.BoardRead) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.items == nil {
		b.items = map[item.ID]app.BoardRead{}
	}
	b.items[id] = *read
}

// fail sets the error every read fails with, nil for none, and whether a
// read hangs until its context ends.
func (b *boards) fail(err error, hang bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.err, b.hang = err, hang
}

// calls are the gateway's read calls the test sets, as the audit store
// answers them (app.ReadCalls): each call by its id, an id with none
// answering app.ErrCallNotFound; err, when set, fails every read. asked
// holds the ids read, in order.
type calls struct {
	mu    sync.Mutex
	byID  map[string]board.ReadCall
	err   error
	asked []string
}

func (c *calls) ReadCall(_ context.Context, id string) (board.ReadCall, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked = append(c.asked, id)
	if c.err != nil {
		return board.ReadCall{}, c.err
	}
	call, ok := c.byID[id]
	if !ok {
		return board.ReadCall{}, app.ErrCallNotFound
	}
	return call, nil
}

// set sets a read call.
func (c *calls) set(call *board.ReadCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byID == nil {
		c.byID = map[string]board.ReadCall{}
	}
	c.byID[call.ID] = *call
}

// fail sets the error every read fails with, nil for none.
func (c *calls) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

// read are the ids read.
func (c *calls) read() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.asked...)
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

// holder is the lease's holder the test sets; reads counts its reads.
type holder struct {
	mu      sync.Mutex
	name    string
	expires time.Time
	err     error
	// hang makes a read wait for its context's end, as a pool with no
	// connection free would; gate, when set, holds a read until it closes or
	// the read's context ends; and during, when set, runs inside each read,
	// as a slow read's time passing.
	hang   bool
	gate   chan struct{}
	during func()
	reads  int
}

// read is how many times the holder was read.
func (h *holder) read() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reads
}

func (h *holder) Holder(ctx context.Context) (string, time.Time, error) {
	h.mu.Lock()
	hang, gate, during := h.hang, h.gate, h.during
	h.reads++
	h.mu.Unlock()
	if hang {
		<-ctx.Done()
		return "", time.Time{}, ctx.Err()
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return "", time.Time{}, ctx.Err()
		}
	}
	if during != nil {
		during()
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
	boards *boards
	calls  *calls
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
		coord: newCoordinator(), views: &views{}, boards: &boards{}, calls: &calls{}, leader: &leader{held: true, epoch: 1},
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
		Leader: w.leader, Coordinator: w.coord, Views: w.views, Boards: w.boards, ReadCalls: w.calls,
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

// form is the act form's values posted with the form key k and its MAC for
// subject.
func (w *world) form(subject string, kind app.ActKind, id item.ID, k, csrf string) url.Values {
	return url.Values{
		"k": {k}, "mac": {w.keys.FormMAC(subject, string(kind), string(id), k)}, auth.TokenField: {csrf},
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

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
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

// newClockWorld is a world whose pages, and the windows they keep on the
// monotonic clock, read a clock the test moves, from now.
func newClockWorld(t *testing.T) *world {
	t.Helper()
	c := &clock{now: time.Now()}
	t.Cleanup(pages.SetMonotonic(c.Now))
	return build(t, httptest.NewUnstartedServer(nil), func(w *world, _ *deploy.Web) { w.clock = c })
}

// boardRead is a read of item id's board in state, as the board watch
// answers it (app.BoardRead, R162): a type incident item opened for the
// principal sso:carol with the responder sso:erin, titled "Checkout fails",
// whose latest record is of seq 3, last updated now; its facts read now on
// the leader, the kill switch's table read, the switch never set. change sets
// the rest.
func boardRead(id item.ID, state string, change func(r *app.BoardRead)) *app.BoardRead {
	now := time.Now()
	r := &app.BoardRead{
		Board: &app.BoardView{
			Version: app.BoardVersion{Record: app.Mark{ID: "rec-3", Seq: 3}},
			View:    app.View{ID: id, State: state, Type: "incident"},
			Opened:  item.Opened{Type: "incident", Title: "Checkout fails", Principal: "sso:carol", Responders: []string{"sso:erin"}},
			Header:  app.BoardHeader{Title: "Checkout fails", Updated: now},
		},
		Facts: app.BoardFacts{KillSwitchRead: true, Held: true, Now: now},
	}
	if change != nil {
		change(r)
	}
	return r
}

// running gives r's item its run, run-1, whose latest entry is of seq 7, its
// budget 5 rounds, 30 min of wall clock and 50000 tokens, none used yet.
func running(r *app.BoardRead) {
	r.Board.Version.Run, r.Board.Version.Board = "run-1", app.Mark{ID: "ev-7", Seq: 7}
	r.Board.View.Budget = &app.BudgetView{Budget: item.Budget{Rounds: 5, WallClock: 30 * time.Minute, Tokens: 50000}}
}

// assessed is boardRead of an item with its run, whose assessment is a, as
// the projection gives it: each hypothesis a card with its status word (no
// wrap-up round open), ranked, those rejected or against which a refutation
// stands ruled out apart; the conclusion's card in the header; and its round
// 1, whose report holds a, as every round's report does the board's current
// assessment (board.State.Report).
func assessed(id item.ID, state string, a board.Assessment, change func(r *app.BoardRead)) *app.BoardRead {
	return boardRead(id, state, func(r *app.BoardRead) {
		running(r)
		bv := r.Board
		bv.View.Rounds = []app.RoundView{{Round: 1, Report: board.Report{Assessment: a}}}
		for i := range a.Hypotheses {
			c := app.HypothesisCard{HypothesisView: a.Hypotheses[i], Status: board.StatusOf(&a, i, "")}
			if c.Rejected || len(c.StandingRefutations) > 0 {
				bv.RuledOut = append(bv.RuledOut, c)
			} else {
				bv.Hypotheses = append(bv.Hypotheses, c)
			}
			if c.ID == a.Conclusion {
				bv.Header.Conclusion = &c
			}
		}
		if change != nil {
			change(r)
		}
	})
}

// richRead is the read of item id's board with every part its header,
// summary, strip and hypotheses show: an investigating item in round 3,
// round 2 ended, of run outputRun, each round's report its assessment; its title, budget, alert of two
// instances, flap origin and notices; a strip of two of the incident's other
// items, one disagreeing, and one more; the conclusion, ranked, leading, with
// a person's and an agent's supporting quotes, an agent's refuting one, and
// two suggestions of one target; and a ruled-out hypothesis. s ends every
// stored value the regions show, so that a test sees each through its
// markup; "" for none.
func richRead(id item.ID, s string) *app.BoardRead {
	now := time.Now()
	agent, other := board.Author{Kind: board.AuthorAgent, Name: "agent-a"}, board.Author{Kind: board.AuthorAgent, Name: "agent-b"}
	person := board.Author{Kind: board.AuthorHuman, Name: "sso:bob" + s}
	evidence := func(seq int64, by board.Author, ev *board.Evidence) board.Event {
		return board.Event{Seq: seq, ID: board.EntryID("ev-" + strconv.FormatInt(seq, 10)), Run: outputRun, Round: 2, Author: by, At: now, Body: board.Body{Evidence: ev}}
	}
	proposal := func(seq int64, p *board.Proposal) board.Event {
		return board.Event{Seq: seq, ID: board.EntryID("ev-" + strconv.FormatInt(seq, 10)), Run: outputRun, Round: 2, Author: board.Gateway, At: now, Body: board.Body{Proposal: p}}
	}
	lead := card("e1", "checkout"+s, "A bad release of checkout"+s, 2, board.StatusLeading, func(c *app.HypothesisCard) {
		c.Quotes = board.Quotes{
			Supports: []board.Event{
				evidence(8, person, &board.Evidence{Claim: "I saw the release go out" + s, Provenance: board.Provenance{Kind: board.FromHuman, Source: board.HumanObservation},
					Hypothesis: "e1", Stance: board.Supports}),
				evidence(9, agent, &board.Evidence{Claim: "the logs say so", Provenance: board.Provenance{Kind: board.FromReadCall, CallID: "call-1",
					Quote: "disk full on db-1" + s, Source: board.Source("logs/search" + s)}, Hypothesis: "e1", Stance: board.Supports}),
			},
			MoreSupports: 3,
			Refutes: []board.Event{
				evidence(7, other, &board.Evidence{Claim: "no deploy", Provenance: board.Provenance{Kind: board.FromReadCall, CallID: "call-3",
					Quote: "no deploy since Monday" + s, Source: board.Source("deploys/list" + s)}, Hypothesis: "e1", Stance: board.Refutes}),
			},
		}
		c.Suggestions = []app.SuggestionGroup{{
			Target: []board.TargetValue{{Arg: "service" + s, Value: "checkout" + s}, {Arg: "region", Value: "eu-1"}},
			Suggestions: []app.Suggestion{
				{
					Event: proposal(20, &board.Proposal{
						Proposer: "agent-a" + s, Tool: "deploy.rollback", Hypothesis: "e1",
						Summary: []board.SummaryPart{{Text: "Roll back "}, {Text: "checkout" + s, Value: true}, {Text: " to v41"}},
						Facts:   map[string]board.Fact{"version" + s: {Values: []string{`"v42` + s + `"`, `"v41"`}, More: 2}, "owner": {Unavailable: "no_time"}},
					}),
					Status: board.StatusLeading, Superseded: true, Service: app.ServiceOutside,
					Support: map[board.Source]int{"metrics/query": 1, board.Source("logs/search" + s): 2},
				},
				{
					Event: proposal(21, &board.Proposal{
						Proposer: "agent-b", Tool: "deploy.restart", Hypothesis: "e1",
						Summary: []board.SummaryPart{{Text: "Restart "}, {Text: "checkout", Value: true}},
					}),
					Status: board.StatusLeading, Service: app.ServiceUncompared,
				},
			},
		}}
	})
	out := card("e2", "payments"+s, "The payments gateway"+s, 0, board.StatusRejected, func(c *app.HypothesisCard) { c.Rejected = true })
	return boardRead(id, "investigating", func(r *app.BoardRead) {
		running(r)
		bv := r.Board
		bv.Version.Run = outputRun
		bv.View.Type = "incident" + s
		bv.Opened.Title = "Checkout fails" + s
		bv.View.Budget.Used = item.Budget{Rounds: 2, WallClock: 4 * time.Minute, Tokens: 1200}
		bv.Hypotheses, bv.RuledOut = []app.HypothesisCard{lead}, []app.HypothesisCard{out}
		ranked := board.Report{Assessment: board.Assessment{Conclusion: "e1", Hypotheses: []board.HypothesisView{lead.HypothesisView, out.HypothesisView}}}
		bv.View.Rounds = []app.RoundView{{Round: 1, Report: ranked}, {Round: 2, Report: ranked}, {Round: 3, Report: ranked}}
		bv.Header = app.BoardHeader{
			Title: "Checkout fails" + s, Conclusion: &bv.Hypotheses[0], Suggestions: 2,
			Alert: []item.TriggerInstance{
				{Source: "alerting" + s, ID: "inst-1" + s, Status: item.Firing, At: now.Add(-40 * time.Minute)},
				{Source: "alerting" + s, ID: "inst-2", Status: item.Resolved, At: now.Add(-5 * time.Minute)},
			},
			Firing: true, Updated: now, StopBy: now.Add(20 * time.Minute), FlapOf: item.ID("p1" + s), Notices: 2, Round: 3, Ended: 2,
		}
		bv.Cited = map[string][]string{"call-1": {"disk full on db-1" + s}, "call-3": {"no deploy since Monday" + s}}
		r.Strip = app.Strip{Items: []app.StripItem{
			{ID: item.ID("s1" + s), Type: "incident", State: item.StatusInvestigating},
			{ID: "s2", Type: "incident", State: item.StatusAwaitingClose, Conclusion: key.Key{"service": "payments" + s}, Converged: true, Disagrees: true},
		}, More: 1}
	})
}

// card is a hypothesis's card, the hypothesis of id with the key
// service=<service>, its statement, its score, two agents supporting it, a
// person refuting it and one source, with status; change sets the rest.
func card(id, service, statement string, score int, status board.StatusWord, change func(c *app.HypothesisCard)) app.HypothesisCard {
	c := app.HypothesisCard{HypothesisView: hyp(id, service, statement, score, nil), Status: status}
	if change != nil {
		change(&c)
	}
	return c
}

// node is the element of body whose id is id; nil when there is none.
func node(t *testing.T, body []byte, id string) *html.Node {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && attr(n, "id") == id {
			return n
		}
	}
	return nil
}

// regionWords are the words the element of body whose id is id shows (see
// shownWords); it must exist.
func regionWords(t *testing.T, body []byte, id string) string {
	t.Helper()
	n := node(t, body, id)
	require.NotNil(t, n, "no element %q", id)
	return shownWords(n)
}

// inline are the elements that sit in a line of text: the words around them
// join theirs with no space.
var inline = map[string]bool{"bdi": true, "q": true, "span": true, "a": true, "strong": true, "em": true}

// shownWords are the words n shows, as a reader reads them: its text nodes
// as they are, each element that is not inline apart from the words around
// it, an element the hidden attribute hides left out, and spaces folded.
func shownWords(n *html.Node) string {
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch {
		case n.Type == html.TextNode:
			b.WriteString(n.Data)
		case n.Type == html.ElementNode && hasAttr(n, "hidden"):
		case n.Type == html.ElementNode && !inline[n.Data]:
			b.WriteByte(' ')
			for c := range n.ChildNodes() {
				walk(c)
			}
			b.WriteByte(' ')
		default:
			for c := range n.ChildNodes() {
				walk(c)
			}
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// hasAttr reports whether n has the attribute name.
func hasAttr(n *html.Node, name string) bool {
	for _, a := range n.Attr {
		if a.Key == name {
			return true
		}
	}
	return false
}

// quotedData are the texts of the quoted data under n: each <q class="data">
// whose one child is a <bdi> holding text alone.
func quotedData(t *testing.T, n *html.Node) []string {
	t.Helper()
	var out []string
	for q := range n.Descendants() {
		if q.Type != html.ElementNode || q.Data != "q" || attr(q, "class") != "data" {
			continue
		}
		c := q.FirstChild
		require.True(t, c != nil && c.NextSibling == nil && c.Type == html.ElementNode && c.Data == "bdi", "quoted data is one <bdi>")
		out = append(out, nodeText(c))
	}
	return out
}

// regionNames are the board page's regions' ids, in the page's order, as
// live.js replaces them (Task 8): the page's own list.
var regionNames = pages.RegionNames

// versionsOf are the data-v of each region of body by its id, and the
// board's data-etag under "etag".
func versionsOf(t *testing.T, body []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, id := range regionNames {
		n := node(t, body, id)
		require.NotNil(t, n, "region %q", id)
		out[id] = attr(n, "data-v")
	}
	b := node(t, body, "board")
	require.NotNil(t, b)
	out["etag"] = attr(b, "data-etag")
	return out
}

func (v *views) listCount() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.lists
}

// feedAt is when a feed fixture's first line was written, each line after
// it a second later.
var feedAt = time.Date(2026, 11, 11, 14, 20, 0, 0, time.UTC)

// feedFixture is a feed the test builds line by line, as the projection
// merges it (app.FeedLine), oldest first, each line a second after the one
// before: item id's records, and its run outputRun's entries, whose seqs
// each store counts apart.
type feedFixture struct {
	id      item.ID
	lines   []app.FeedLine
	rec, ev int64
}

// record adds the line of the item's next record, its body b, and returns
// its anchor, rec-<seq>.
func (f *feedFixture) record(b *item.Body) string {
	f.rec++
	at := feedAt.Add(time.Duration(len(f.lines)) * time.Second)
	body := *b
	body.V = item.Version
	f.lines = append(f.lines, app.FeedLine{At: at, Repeats: 1, Record: &item.Record{
		Seq: f.rec, ID: "r" + strconv.FormatInt(f.rec, 10), Item: f.id, Incident: item.IncidentID("inc-" + f.id), At: at, Body: body,
	}})
	return "rec-" + strconv.FormatInt(f.rec, 10)
}

// entry adds the line of the run's next entry, by author, its body b, and
// returns its anchor, ev-<seq>.
func (f *feedFixture) entry(by board.Author, b *board.Body) string {
	f.ev++
	at := feedAt.Add(time.Duration(len(f.lines)) * time.Second)
	f.lines = append(f.lines, app.FeedLine{At: at, Repeats: 1, Event: &board.Event{
		Seq: f.ev, ID: board.EntryID("e" + strconv.FormatInt(f.ev, 10)), Run: outputRun, Round: 1, Author: by, At: at, Body: *b,
	}})
	return "ev-" + strconv.FormatInt(f.ev, 10)
}

// last is the line added last, for the test to give what its words need
// (app.FeedLine); it is valid until the next line is added.
func (f *feedFixture) last() *app.FeedLine { return &f.lines[len(f.lines)-1] }

// read is the read of the item's board, investigating in its run
// outputRun, opened for the principal sso:carol (see boardRead), whose feed
// is f's lines.
func (f *feedFixture) read() *app.BoardRead {
	return boardRead(f.id, "investigating", func(r *app.BoardRead) {
		running(r)
		r.Board.Version.Run = outputRun
		r.Board.Feed = f.lines
	})
}

// feedLineOf is the feed's line of anchor on body: the words it shows, its
// time left out (see shownWords), and its node; it must exist, an item of
// the feed's list.
func feedLineOf(t *testing.T, body []byte, anchor string) (string, *html.Node) {
	t.Helper()
	n := node(t, body, anchor)
	require.NotNil(t, n, "no line %q", anchor)
	require.Equal(t, "li", n.Data, anchor)
	require.NotNil(t, n.Parent)
	require.Equal(t, "ol", n.Parent.Data, anchor)
	words := shownWords(n)
	for c := range n.ChildNodes() {
		if c.Type == html.ElementNode && c.Data == "time" {
			words = strings.TrimSpace(strings.TrimPrefix(words, nodeText(c)))
		}
	}
	return words, n
}

// feedAnchors are the anchors of the feed's lines on body, in its order.
func feedAnchors(t *testing.T, body []byte) []string {
	t.Helper()
	list := node(t, body, "feed-list")
	if list == nil {
		return nil
	}
	var out []string
	for n := range list.Descendants() {
		if n.Type == html.ElementNode && n.Data == "li" {
			out = append(out, attr(n, "id"))
		}
	}
	return out
}

// partsOf are the parts of the line n shows, in its order (R171): each link,
// "link:<href>"; and each stored value by how it shows (see kindOf), its
// kind, ":" and its text.
func partsOf(n *html.Node) []string {
	var out []string
	for d := range n.Descendants() {
		switch {
		case d.Type != html.ElementNode:
		case d.Data == "a":
			out = append(out, "link:"+attr(d, "href"))
		case d.Data == "bdi":
			out = append(out, kindOf(d)+":"+nodeText(d))
		}
	}
	return out
}

// kindOf is how the stored value of bdi shows: "data", quoted data, its
// <bdi> the one child of a <q class="data">; "model", model-written text, its
// <bdi> in a <span class="model"> after the words "model-written: "; "person",
// a person's words, its <bdi> after the words "a person's words: "; or "name",
// a name, its <bdi> alone.
func kindOf(bdi *html.Node) string {
	p, before := bdi.Parent, bdi.PrevSibling
	switch {
	case p != nil && p.Data == "q" && attr(p, "class") == "data" && p.FirstChild == bdi && bdi.NextSibling == nil:
		return "data"
	case p != nil && p.Data == "span" && attr(p, "class") == "model" && p.PrevSibling != nil &&
		p.PrevSibling.Type == html.TextNode && strings.HasSuffix(p.PrevSibling.Data, "model-written: "):
		return "model"
	case before != nil && before.Type == html.TextNode && strings.HasSuffix(before.Data, "a person's words: "):
		return "person"
	}
	return "name"
}
