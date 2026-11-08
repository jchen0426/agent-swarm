package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
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
	s.servers = newServers(nil, nil, http.NotFoundHandler(), log)
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
// holder (R90), the revisions, the tenant, the clock and the log; and the
// pages behind the authenticator built from the web block and the secret
// store: with no session key a page answers 503 while /healthz answers 200
// (R86; Task 5's m1), and with one, a page sends a person to sign in.
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
	coord, leader, views, lease, revs := fakeWebCoordinator{}, webLeader{}, webViews{}, webLease{}, webRevisions{}
	p := webPorts(d, coord, leader, compatibleSchema{}, views, lease, revs, log)
	assert.Equal(t, coord, p.Coordinator)
	assert.Equal(t, views, p.Views)
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
}
