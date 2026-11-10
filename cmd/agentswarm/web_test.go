package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	registryapp "github.com/jchen0426/agent-swarm/internal/registry/app"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/current"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
	"github.com/jchen0426/agent-swarm/internal/web"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	workpg "github.com/jchen0426/agent-swarm/internal/work/postgres"
)

// webLeader is the leader as the web interface reads it: the draining
// leader, which still holds the lease.
type webLeader struct{}

func (webLeader) Status() (epoch int64, held, draining bool) { return 3, true, true }

// compatibleSchema is a compatible schema.
type compatibleSchema struct{}

func (compatibleSchema) State(context.Context) (compatible bool, state string, err error) {
	return true, "compatible", nil
}

// processDeployment is a deployment of the process runner whose listeners
// bind any free loopback port, with a web block when withWeb is set.
func processDeployment(t *testing.T, withWeb bool) *deploy.Deployment {
	t.Helper()
	d := &deploy.Deployment{
		Listeners: deploy.Listeners{
			MCP: "127.0.0.1:0", LLM: "127.0.0.1:0", Admin: "127.0.0.1:0", Addresses: filepath.Join(t.TempDir(), "addresses.json"),
		},
		Runner: deploy.Runner{Kind: deploy.KindProcess},
		Timing: deploy.Timing{Drain: time.Second},
	}
	if withWeb {
		d.Listeners.Web = "127.0.0.1:0"
		d.Web = &deploy.Web{PublicURL: "https://agents.example.com"}
	}
	return d
}

// writtenAddresses writes the listeners' addresses as serve does, and returns
// the file's keys and values.
func writtenAddresses(t *testing.T, d *deploy.Deployment, lns *listeners) map[string]any {
	t.Helper()
	require.NoError(t, writeAddresses(d.Listeners.Addresses, lns.addresses()))
	raw, err := os.ReadFile(d.Listeners.Addresses)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	return got
}

// leaderz gets the web listener's /leaderz, and returns its status, or the
// error of a listener that no longer serves.
func leaderz(addr string) (int, error) {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + addr + "/leaderz")
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// With a web block, serve binds a fourth listener, on loopback, names it in
// the addresses file as web (Contract C2), and serves it through a drain, as
// the admin API's: the draining leader's /leaderz answers 200 until the
// drain's close, after which the listener is closed.
func TestServeWritesTheWebAddressAndServesThroughADrain(t *testing.T) {
	d := processDeployment(t, true)
	lns, err := listen(d, nil)
	require.NoError(t, err)
	t.Cleanup(lns.close)
	require.NotNil(t, lns.web, "the web listener is bound")
	addr := lns.web.Addr().String()

	assert.Equal(t, addr, writtenAddresses(t, d, &lns)["web"])

	log := slog.New(slog.DiscardHandler)
	s := &server{d: d, log: log, listeners: lns}
	s.servers = newServers(nil, nil, http.NotFoundHandler(), nil, log)
	s.servers.web = web.NewServer(&web.Ports{Leader: webLeader{}, Schema: compatibleSchema{}}, web.Options{PublicURL: d.Web.PublicURL, Log: log})
	s.serve(t.Context())
	var during int
	st := newSteps()
	dr := &drainer{
		leader: &probingLeader{drainingLeader: drainingLeader{steps: st, done: make(chan struct{})}, probe: func() {
			during, err = leaderz(addr)
		}},
		mcp: drainingGateway{name: "mcp", steps: st}, llm: drainingGateway{name: "llm", steps: st},
		closeUpstream: func() {}, bound: time.Second, busy: time.Second, log: log,
	}

	dr.drain(t.Context())
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, during, "the draining leader's /leaderz answers 200")
	after, err := leaderz(addr)
	require.NoError(t, err, "the web listener serves until the drain's close")
	assert.Equal(t, http.StatusOK, after)
	s.close(t.Context())

	_, err = leaderz(addr)
	assert.Error(t, err, "the drain's close closes the web listener")
}

// probingLeader is a draining leader that probes the web listener as it
// releases the lease, at the drain's end.
type probingLeader struct {
	drainingLeader
	probe func()
}

func (l *probingLeader) Stop(ctx context.Context) {
	l.probe()
	l.drainingLeader.Stop(ctx)
}

// Without a web block, serve binds the three listeners it always has, and
// its addresses file is as before: it has no web key.
func TestServeWithoutAWebBlockHasNoWebListener(t *testing.T) {
	d := processDeployment(t, false)
	lns, err := listen(d, nil)
	require.NoError(t, err)
	t.Cleanup(lns.close)

	assert.Nil(t, lns.web)
	got := writtenAddresses(t, d, &lns)
	assert.NotContains(t, got, "web")
	assert.ElementsMatch(t, []string{"pid", "mcp", "llm", "admin"}, keysOf(got))
}

// A web listener that is not on loopback is refused, as the deployment's
// check refuses it first.
func TestServeRefusesAWebListenerOffLoopback(t *testing.T) {
	d := processDeployment(t, true)
	d.Listeners.Web = "0.0.0.0:0"

	_, err := listen(d, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "listeners.web")
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// fakeWebCoordinator is the coordinator as the web interface acts through it:
// its item acts and the kill switch's.
type fakeWebCoordinator struct{}

func (fakeWebCoordinator) Promote(context.Context, item.ID, *app.Act) (app.Outcome, error) {
	return app.Outcome{}, nil
}

func (fakeWebCoordinator) Close(context.Context, item.ID, *app.Act) (app.Outcome, error) {
	return app.Outcome{}, nil
}

func (fakeWebCoordinator) Reopen(context.Context, item.ID, *app.Act) (app.Outcome, error) {
	return app.Outcome{}, nil
}

func (fakeWebCoordinator) Open(context.Context, *app.OpenInput) (item.ID, error) { return "", nil }

func (fakeWebCoordinator) SetKillSwitch(context.Context, bool, *app.Act) (app.KillSwitchOutcome, error) {
	return app.KillSwitchOutcome{}, nil
}

func (fakeWebCoordinator) RecordKillSwitchOn(context.Context, int64, *app.Act) (app.KillSwitchOutcome, error) {
	return app.KillSwitchOutcome{}, nil
}

// webViews are the views as the web interface reads them.
type webViews struct{}

func (webViews) List(context.Context) ([]app.Row, error) { return nil, nil }

func (webViews) Item(context.Context, item.ID) (app.View, error) { return app.View{}, nil }

func (webViews) KillSwitch(context.Context) (app.KillSwitch, error) { return app.KillSwitch{}, nil }

func (webViews) UnstaffedBy(context.Context, int64) ([]item.ID, error) { return nil, nil }

// webLease is the lease's holder.
type webLease struct{}

func (webLease) Holder(context.Context) (string, time.Time, error) {
	return "instance-a", time.Time{}, nil
}

// webRevisions are the revisions.
type webRevisions struct{}

func (webRevisions) Current() *config.Revision { return nil }

// webBoards are the board's reads, the web's watch's.
type webBoards struct{}

func (webBoards) Board(context.Context, item.ID) (app.BoardRead, error) { return app.BoardRead{}, nil }

// webSecrets is a secret store whose session key the test sets.
type webSecrets struct{ key string }

func (s webSecrets) Secret(_ context.Context, ref string) (string, error) {
	if ref != "secret://web/session" || s.key == "" {
		return "", errors.New("no such secret")
	}
	return s.key, nil
}

// Serve wires the web interface's ports (I6): the coordinator, as the item
// acts' and the kill switch's routing's, the views, the leader, the lease's
// holder (R90), the revisions, the tenant, the clock and the log; the
// board's reads, the web's watch (R143); and the pages behind the
// authenticator built from the web block and the secret store: with no
// session key a page answers 503 while /healthz answers 200 (R86; Task 5's
// m1), and with one, a page sends a person to sign in. The web's views read
// the web's stores, with the coordinator's waits of a round's end and the
// web's inbound store's titles (Task 2's review M5), and every other port the
// admin views read.
func TestServeWiresTheWebPorts(t *testing.T) {
	var _ web.LeaseHolder = (*workpg.Lease)(nil)
	d := processDeployment(t, true)
	d.Tenant = "acme"
	d.Web = &deploy.Web{
		PublicURL: "https://agents.example.com",
		OIDC: deploy.OIDC{
			Issuer: "https://idp.example.com", ClientID: "agentswarm", ClientSecretRef: "secret://web/client",
			SubjectClaim: "sub", GroupsClaim: "groups",
		},
		Groups:        deploy.Groups{Viewers: "v", Responders: "r", Admins: "a"},
		SessionKeyRef: "secret://web/session", SessionLength: 8 * time.Hour, FreshWindow: 5 * time.Minute, TimeZone: "Europe/Berlin",
	}
	log := slog.New(slog.DiscardHandler)
	coord, leader, views, lease, revs, boards := fakeWebCoordinator{}, webLeader{}, webViews{}, webLease{}, webRevisions{}, webBoards{}
	p := webPorts(d, coord, leader, compatibleSchema{}, views, lease, revs, boards, log)
	assert.Equal(t, coord, p.Coordinator)
	assert.Equal(t, views, p.Views, "the admin API's views, on the main pool (R143)")
	assert.Equal(t, boards, p.Boards, "the web's watch, on the web's pool (R143)")
	assert.Equal(t, leader, p.Leader)
	assert.Equal(t, compatibleSchema{}, p.Schema)
	assert.Equal(t, coord, p.Switching.Coordinator, "the kill switch's routing acts through the coordinator")
	assert.Equal(t, leader, p.Switching.Leader)
	assert.Same(t, log, p.Switching.Log)
	assert.Equal(t, lease, p.LeaseHolder)
	assert.Equal(t, revs, p.Revisions)
	assert.Equal(t, "acme", p.Tenant)
	assert.NotNil(t, p.Clock)
	assert.Same(t, log, p.Log)

	get := func(srv *http.Server, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody)
		req.Host = "agents.example.com"
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)
		return w
	}
	srv, err := newWebServer(d, &p, webSecrets{}, log)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, get(srv, "/healthz").Code)
	page := get(srv, "/items")
	assert.Equal(t, http.StatusServiceUnavailable, page.Code)
	assert.Contains(t, page.Body.String(), "sign-in is unavailable")

	srv, err = newWebServer(d, &p, webSecrets{key: base64.StdEncoding.EncodeToString(make([]byte, 32))}, log)
	require.NoError(t, err)
	page = get(srv, "/items")
	assert.Equal(t, http.StatusSeeOther, page.Code)
	assert.Equal(t, "/auth/signin?return=%2Fitems", page.Header().Get("Location"))
	assert.Equal(t, http.StatusOK, get(srv, "/static/app.css").Code)

	d.Timing.Coordinator = deploy.CoordinatorTiming{Grace: 5 * time.Second, SettleWait: 30 * time.Second, SettlePoll: 2 * time.Second, Retry: 7 * time.Second}
	ws := newWebStores(nil, d)
	admin := app.ViewPorts{
		Lifecycle: func() app.Lifecycle { return app.Lifecycle{Limit: 3} }, Controls: workpg.NewControls(nil, d.Tenant), ReopenWindow: 24 * time.Hour,
		ClosedRulings: 72 * time.Hour, Closed: workpg.NewRecords(nil, d.Tenant), Incidents: workpg.NewRecords(nil, d.Tenant),
		Extension: item.Budget{Rounds: 1, WallClock: 5 * time.Minute}, Held: func() bool { return true }, Log: log,
	}
	wp := webViewPorts(d, &admin, ws, nil)
	assert.Equal(t, item.Waits{Grace: 5 * time.Second, Settle: 30 * time.Second, Poll: 2 * time.Second, Retry: 7 * time.Second}, wp.Waits,
		"the coordinator's waits, by which the board shows the latest time a run stops")
	require.NotNil(t, wp.Titles, "the alerts' titles")
	assert.Same(t, ws.records, wp.Titles, "read from the web's inbound store")
	assert.Same(t, ws.controls, wp.Controls)
	assert.Same(t, ws.records, wp.Closed)
	assert.Same(t, ws.records, wp.Incidents)
	assert.Same(t, ws.events, wp.Entries)
	assert.Same(t, ws.records, wp.Siblings)
	assert.Nil(t, wp.Visible, "every item visible until Plan 8a (R144)")
	assert.Equal(t, cachedRevisions{}, wp.Revisions, "the registry's cache alone (R175)")
	assert.Equal(t, admin.ReopenWindow, wp.ReopenWindow)
	assert.Equal(t, admin.ClosedRulings, wp.ClosedRulings)
	assert.Equal(t, admin.Extension, wp.Extension)
	assert.Equal(t, app.Lifecycle{Limit: 3}, wp.Lifecycle(), "the coordinator's snapshot")
	assert.True(t, wp.Held())
	assert.Same(t, log, wp.Log)
}

// countedConfiguration is a configuration source of commits, its reads
// counted.
type countedConfiguration struct {
	mu      sync.Mutex
	commits map[string]map[string][]byte
	reads   int
}

func (c *countedConfiguration) Read(_ context.Context, ref string) (registryapp.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	files, ok := c.commits[ref]
	if !ok {
		return registryapp.Snapshot{}, fmt.Errorf("no commit %s", ref)
	}
	fsys, err := memfs.New(files)
	if err != nil {
		return registryapp.Snapshot{}, err
	}
	return registryapp.Snapshot{Commit: ref, Files: fsys}, nil
}

func (c *countedConfiguration) made() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

// keptNothing is the record of revisions, which keeps nothing.
type keptNothing struct{}

func (keptNothing) Record(context.Context, *config.Revision) error { return nil }

// The web's views read revisions from the registry's cache alone (R175): a
// revision the registry holds answers; one it does not, though its source
// holds it, is unread; and neither reads the source. The web locks no
// revision. (The plan's.) Fails first: a build failure.
func TestTheWebsViewsReadRevisionsFromTheCacheAlone(t *testing.T) {
	held, other := strings.Repeat("a", 40), strings.Repeat("b", 40)
	rule := map[string][]byte{"redaction-rules/one.yaml": []byte("kind: RedactionRule\nmetadata: {name: one, owner: security@example.com}\nspec: {patterns: ['secret']}\n")}
	src := &countedConfiguration{commits: map[string]map[string][]byte{held: rule, other: rule}}
	cc, err := celeval.New()
	require.NoError(t, err)
	revs := current.New(registryapp.New(src, cc, keptNothing{}, "refs/heads/main", nil), current.Options{
		Priced: func(string) bool { return true }, CheckSkill: func(*config.Revision, string) error { return nil },
	})
	_, err = revs.At(t.Context(), held)
	require.NoError(t, err)
	reads := src.made()
	d := processDeployment(t, true)
	wp := webViewPorts(d, &app.ViewPorts{}, newWebStores(nil, d), revs)
	require.NotNil(t, wp.Revisions, "the web's views read revisions")

	rev, err := wp.Revisions.At(t.Context(), held)
	require.NoError(t, err)
	_, unread := wp.Revisions.At(t.Context(), other)
	_, locked := wp.Revisions.Lock(rev)

	assert.Equal(t, held, rev.Commit(), "a revision the registry holds")
	require.Error(t, unread, "a miss is unread")
	assert.Equal(t, reads, src.made(), "the source is never read")
	require.Error(t, locked, "the web locks no revision")
}
