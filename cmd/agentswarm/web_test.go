package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/web"
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
	s.servers.web = web.NewServer(web.Ports{Leader: webLeader{}, Schema: compatibleSchema{}}, web.Options{PublicURL: d.Web.PublicURL, Log: log})
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
