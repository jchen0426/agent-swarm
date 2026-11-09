package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/deploy"
	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/runner/docker"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// servedDeployment writes a deployment whose database is url, its secrets
// and the directory of its turns' output, change edits the deployment's text
// first, and returns the deployment's path and the path of the addresses file
// it names.
func servedDeployment(t *testing.T, url string, change func(text string) string) (deployment, addresses string) {
	t.Helper()
	dir := t.TempDir()
	secrets, output := filepath.Join(dir, "secrets"), filepath.Join(dir, "turns")
	for name, value := range map[string]string{"database/url": url, "admin/token": "admin-token", "vendor/key": "vendor-key"} {
		path := filepath.Join(secrets, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(value+"\n"), 0o600))
	}
	require.NoError(t, os.Mkdir(output, 0o700))
	addresses = filepath.Join(dir, "addresses.json")
	text := fmt.Sprintf(`tenant: example
holder: instance-a
secrets: %q
database: secret://database/url
configuration: {repository: %q, ref: refs/heads/main}
listeners: {mcp: "127.0.0.1:0", llm: "127.0.0.1:0", admin: "127.0.0.1:0", addresses: %q}
admin: {token: secret://admin/token}
vendor: {baseURL: "http://127.0.0.1:1", key: secret://vendor/key, apiVersion: "2023-06-01"}
prices:
  reference: 3000000
  dailyCap: 20000000
  cards:
    claude-sonnet-5-5: {threshold: 0, base: {input: 1000000, output: 5000000, cacheWrite5m: 1250000, cacheWrite1h: 2000000, cacheRead: 100000}}
runner:
  kind: process
  unsafeNoSandbox: true
  output: %q
  images: {thinloop: "sha256:%s"}
`, secrets, filepath.Join(dir, "config"), addresses, output, strings.Repeat("0", 64))
	if change != nil {
		text = change(text)
	}
	deployment = filepath.Join(dir, "deployment.yaml")
	require.NoError(t, os.WriteFile(deployment, []byte(text), 0o600))
	return deployment, addresses
}

// Serve's pools take their bounds from the deployment: every transaction, and
// every transaction left idle, from its database timing, and each connect
// from the lease's renewal period, which is below a take's bound: a connect
// to a database whose address drops its packets frees its slot of the pool
// before the take that waits for it gives up (Decision 4).
func TestServesPoolsTakeTheirBoundsFromTheDeployment(t *testing.T) {
	d := &deploy.Deployment{Timing: deploy.Timing{
		Lease:    deploy.LeaseTiming{TTL: 20 * time.Second, RenewEvery: 4 * time.Second, Margin: 6 * time.Second, TakeEvery: 3 * time.Second, TakeWait: 2 * time.Second},
		Database: deploy.DatabaseTiming{TransactionTimeout: 7 * time.Second, IdleTimeout: 8 * time.Second},
	}}

	o := poolOptions(d)

	assert.Equal(t, db.PoolOptions{TransactionTimeout: 7 * time.Second, IdleTimeout: 8 * time.Second, ConnectTimeout: 4 * time.Second}, o)
}

// leaky is a database URL that does not parse, whose password holds a colon:
// pgx's redaction of such a URL leaves the part of the password before the
// colon in its error.
const leaky = "postgres://u:k8f3q:z9w2v@%zz/db"

// Serve never logs the database's password: of a URL that does not parse it
// says only that, naming the deployment's key, never pgx's words about it.
func TestServeNeverPrintsTheDatabasePassword(t *testing.T) {
	deployment, addresses := servedDeployment(t, leaky, nil)

	_, errOut, code := runCommand("serve", "--deployment", deployment)

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, `"msg":"serve refused to start"`)
	assert.Contains(t, errOut, "database: the database URL does not parse")
	for _, part := range []string{"k8f3q", "z9w2v"} {
		assert.NotContains(t, errOut, part, "no part of the password")
	}
	assert.NoFileExists(t, addresses, "no listener was bound")
}

// asDocker edits a deployment's text, servedDeployment's, into one of the
// Docker runner: without the agents' listeners, which the sandboxes reach
// through runner.docker.sockets, and with block, a runner.docker block, in
// place of the process runner's keys.
func asDocker(t *testing.T, text, block string) string {
	t.Helper()
	for _, edit := range []struct{ old, replacement string }{
		{old: `mcp: "127.0.0.1:0", llm: "127.0.0.1:0", `},
		{old: "  kind: process\n  unsafeNoSandbox: true\n", replacement: "  kind: docker\n  docker: " + block + "\n"},
	} {
		require.Contains(t, text, edit.old)
		text = strings.Replace(text, edit.old, edit.replacement, 1)
	}
	return text
}

// Serve runs the Docker runner on Linux alone (Plan 7d-2, Decision 1).
// Elsewhere it refuses a deployment of the Docker runner, naming runner.kind
// and Linux, before it opens its database or binds a listener: the
// database's URL names no server, so a serve that opened its database first
// would refuse it on another key. On Linux, serve starts with that runner
// (TestServeStartsWithTheDockerRunner).
func TestServeRefusesTheDockerRunnerOffLinuxBeforeItOpensItsDatabase(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("serve runs the Docker runner on Linux: TestServeStartsWithTheDockerRunner")
	}
	store := t.TempDir()
	deployment, addresses := servedDeployment(t, "postgres://u:p@127.0.0.1:1/db", func(text string) string {
		return asDocker(t, text, fmt.Sprintf("{sockets: /run/agentswarm/a, store: %q}", store))
	})
	_, err := deploy.Load(deployment)
	require.NoError(t, err, "a deployment Check takes")

	_, errOut, code := runCommand("serve", "--deployment", deployment)

	assert.Equal(t, exitError, code)
	refusal := logLine(t, errOut, "serve refused to start")
	assert.Contains(t, refusal["err"], "runner.kind: ", "the first refusal is the system's")
	assert.Contains(t, refusal["err"], "Linux")
	assert.NoFileExists(t, addresses, "no listener was bound")
}

// As it loads a deployment of the Docker runner, serve checks that it can
// write in the turns' output directory and in the store (Plan 7d-2,
// Decision 8), and refuses, naming its key, the one it cannot.
func TestServeLoadsTheDockerRunnerOnlyWithItsOutputAndStoreWritable(t *testing.T) {
	for _, tc := range []struct {
		key string
		// missing is the directory the row removes: the turns' output
		// directory, in the deployment's directory dir, or the store.
		missing func(dir, store string) string
	}{
		{key: "runner.output", missing: func(dir, _ string) string { return filepath.Join(dir, "turns") }},
		{key: "runner.docker.store", missing: func(_, store string) string { return store }},
	} {
		t.Run(tc.key, func(t *testing.T) {
			store := t.TempDir()
			deployment, _ := servedDeployment(t, "postgres://u:p@127.0.0.1:1/db", func(text string) string {
				return asDocker(t, text, fmt.Sprintf("{sockets: /run/agentswarm/a, store: %q}", store))
			})
			_, secrets, err := load(deployment)
			require.NoError(t, err, "a Docker deployment whose output and store serve can write in")
			require.NoError(t, secrets.Close())
			require.NoError(t, os.Remove(tc.missing(filepath.Dir(deployment), store)))

			_, _, err = load(deployment)

			require.Error(t, err)
			assert.True(t, strings.HasPrefix(err.Error(), tc.key+": "), "the refusal names %s: %v", tc.key, err)
		})
	}
}

// logLine returns the last line of a JSON log whose msg is msg, and fails
// the test when it has none.
func logLine(t *testing.T, log, msg string) map[string]any {
	t.Helper()
	var found map[string]any
	for l := range strings.SplitSeq(log, "\n") {
		var line map[string]any
		if json.Unmarshal([]byte(l), &line) == nil && line["msg"] == msg {
			found = line
		}
	}
	require.NotNil(t, found, "no line %q in the log:\n%s", msg, log)
	return found
}

// Serve runs the Docker runner on Linux alone, the containers' kernel: a
// sandbox reaches serve through a socket in a bind mount, which carries a
// connection on that kernel only (Plan 7d-2, Decision 1). checkSystem
// refuses the Docker runner on another system, naming runner.kind and
// Linux, and takes it on Linux, and the process runner on any system.
func TestServeRefusesTheDockerRunnerOnASystemOtherThanLinux(t *testing.T) {
	err := checkSystem("darwin", deploy.KindDocker)

	require.ErrorContains(t, err, "runner.kind")
	require.ErrorContains(t, err, "Linux")
	require.NoError(t, checkSystem("linux", deploy.KindDocker))
	require.NoError(t, checkSystem("darwin", deploy.KindProcess))
}

// getenvWithMCPGODEBUG is a getenv whose only variable is MCPGODEBUG, set to
// value.
func getenvWithMCPGODEBUG(value string) func(string) string {
	return func(key string) string {
		if key == "MCPGODEBUG" {
			return value
		}
		return ""
	}
}

// Serve refuses to start while the environment gives MCPGODEBUG a value
// (Plan 7b-1, Decision 15): the MCP SDK reads it once, at its init, and its
// flags change its behaviour for the whole process, both gateways' included.
// The refusal names the variable and never repeats its value, so it is the
// same for any value; an empty value is taken, as the SDK ignores it. Through
// serve, on a deployment whose database names no server, the refusal comes
// before the database is opened and any listener is bound, and with the
// variable empty the refusal is the database's.
func TestServeRefusesToStartWhileMCPGODEBUGIsSet(t *testing.T) {
	var messages []string
	for _, value := range []string{"x", "jsonescaping=1"} {
		err := checkEnvironment(getenvWithMCPGODEBUG(value))

		require.ErrorContains(t, err, "MCPGODEBUG", value)
		messages = append(messages, err.Error())
	}
	assert.Equal(t, messages[0], messages[1], "the message is the same for any value, so it never repeats one")
	assert.NotContains(t, messages[1], "jsonescaping")
	require.NoError(t, checkEnvironment(getenvWithMCPGODEBUG("")), "an unset or empty variable is taken")

	t.Run("set", func(t *testing.T) {
		deployment, addresses := servedDeployment(t, "postgres://u:p@127.0.0.1:1/db", nil)
		t.Setenv("MCPGODEBUG", "x")

		_, errOut, code := runCommand("serve", "--deployment", deployment)

		assert.Equal(t, exitError, code)
		assert.Contains(t, logLine(t, errOut, "serve refused to start")["err"], "MCPGODEBUG")
		assert.NoFileExists(t, addresses, "no listener was bound")
	})
	t.Run("empty", func(t *testing.T) {
		deployment, addresses := servedDeployment(t, "postgres://u:p@127.0.0.1:1/db", nil)
		t.Setenv("MCPGODEBUG", "")

		_, errOut, code := runCommand("serve", "--deployment", deployment)

		assert.Equal(t, exitError, code)
		refusal := logLine(t, errOut, "serve refused to start")
		assert.Contains(t, refusal["err"], "connect to postgres", "the refusal is the database's")
		assert.NotContains(t, refusal["err"], "MCPGODEBUG")
		assert.NoFileExists(t, addresses, "no listener was bound")
	})
}

// bodies is an LLM gateway that keeps the size of each request's body it is
// given.
type bodies struct{ sizes []int }

func (b *bodies) Messages(_ context.Context, in *llmapp.Inbound) *llmapp.Reply {
	b.sizes = append(b.sizes, len(in.Body))
	return &llmapp.Reply{Status: http.StatusOK}
}

// Every listener bounds how long a request's header and body take to read
// (Decision 1), and the agents' two allow HTTP/1 alone, under either runner
// (Plan 7d-2, Decision 2): Go's HTTP/2 server has no per-request header
// timeout. The LLM listener reads no more of a body than the LLM gateway
// takes, from the one setting both have.
func TestServesHTTPServersBoundTheirReads(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	gw := &bodies{}

	s := newServers(nil, gw, http.NotFoundHandler(), nil, log)

	for name, srv := range map[string]*http.Server{"mcp": s.mcp, "llm": s.llm, "admin": s.admin} {
		assert.Equal(t, 10*time.Second, srv.ReadHeaderTimeout, name)
		assert.Equal(t, 30*time.Second, srv.ReadTimeout, name)
	}
	for name, srv := range map[string]*http.Server{"mcp": s.mcp, "llm": s.llm} {
		require.NotNil(t, srv.Protocols, "%s: the server sets its protocols", name)
		assert.True(t, srv.Protocols.HTTP1(), name)
		assert.False(t, srv.Protocols.HTTP2(), name)
		assert.False(t, srv.Protocols.UnencryptedHTTP2(), name)
	}
	opts := llmOptions(&deploy.Deployment{}, log)
	require.Positive(t, opts.MaxBodyBytes)
	llm := httptest.NewServer(s.llm.Handler)
	defer llm.Close()
	resp, err := http.Post(llm.URL+"/v1/messages", "application/json", bytes.NewReader(make([]byte, opts.MaxBodyBytes+100)))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, []int{opts.MaxBodyBytes + 1}, gw.sizes, "the listener reads the gateway's request size and one byte more, which the gateway refuses")
}

// The inbound server, made only with an inbound handler, has exactly the
// admin API's bounds (R113): a request's header within 10 s, the whole of it
// within 30 s. Without a handler there is no inbound server.
func TestTheInboundServerIsBoundedInTime(t *testing.T) {
	log := slog.New(slog.DiscardHandler)

	s := newServers(nil, nil, http.NotFoundHandler(), http.NotFoundHandler(), log)

	require.NotNil(t, s.inbound)
	assert.Equal(t, 10*time.Second, s.inbound.ReadHeaderTimeout)
	assert.Equal(t, 30*time.Second, s.inbound.ReadTimeout)
	assert.Equal(t, s.admin.ReadHeaderTimeout, s.inbound.ReadHeaderTimeout)
	assert.Equal(t, s.admin.ReadTimeout, s.inbound.ReadTimeout)
	assert.NotNil(t, s.inbound.ErrorLog)
	assert.Nil(t, newServers(nil, nil, http.NotFoundHandler(), nil, log).inbound)
}

// The inbound listener is bound only with listeners.inbound, under either
// runner, and the addresses file names it inbound (Contract C2); without the
// key there is no listener and no inbound key.
func TestTheInboundListenerIsBoundOnlyWithItsKey(t *testing.T) {
	d := processDeployment(t, false)
	d.Listeners.Inbound = "127.0.0.1:0"
	lns, err := listen(d, nil)
	require.NoError(t, err)
	t.Cleanup(lns.close)

	require.NotNil(t, lns.inbound, "the inbound listener is bound")
	assert.Equal(t, lns.inbound.Addr().String(), writtenAddresses(t, d, &lns)["inbound"])

	without := processDeployment(t, false)
	plain, err := listen(without, nil)
	require.NoError(t, err)
	t.Cleanup(plain.close)
	assert.Nil(t, plain.inbound)
	assert.NotContains(t, writtenAddresses(t, without, &plain), "inbound")

	off := processDeployment(t, false)
	off.Listeners.Inbound = "0.0.0.0:0"
	_, err = listen(off, nil)
	require.Error(t, err, "an inbound listener off loopback is refused, as the deployment's check refuses it first")
	assert.Contains(t, err.Error(), "listeners.inbound")
}

// steps notes when each step of a drain came, by the bubble's clock, and the
// order the steps came in.
type steps struct {
	mu    sync.Mutex
	start time.Time
	at    map[string]time.Duration
	order []string
}

func newSteps() *steps { return &steps{start: time.Now(), at: map[string]time.Duration{}} }

func (s *steps) note(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.at[step] = time.Since(s.start)
	s.order = append(s.order, step)
}

// before reports whether step a came before step b, both of which came.
func (s *steps) before(a, b string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, j := slices.Index(s.order, a), slices.Index(s.order, b)
	return i >= 0 && j >= 0 && i < j
}

// drainingLeader is the leader as a drain acts on it.
type drainingLeader struct {
	steps *steps
	done  chan struct{}
}

func (l *drainingLeader) Drain() { l.steps.note("leader drains") }

func (l *drainingLeader) Stop(context.Context) {
	l.steps.note("lease released")
	close(l.done)
}

func (l *drainingLeader) Done() <-chan struct{} { return l.done }

// drainingGateway is a gateway whose drain takes its time, unless the
// drain's context ends first.
type drainingGateway struct {
	name  string
	steps *steps
	takes time.Duration
}

func (g drainingGateway) Drain(ctx context.Context) error {
	select {
	case <-time.After(g.takes):
		g.steps.note(g.name + " drained")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// The upstream sessions close beside the LLM gateway's drain, both under the
// holding, after the MCP gateway's drain: the lease's release waits for the
// slower of the two, never for both in turn, and never past the drain bound,
// which Decision 6's failover wait counts once. Sessions that close within
// their bound close before the release, while the holding holds; a close
// that outlasts it goes on alone, after the release.
func TestTheUpstreamSessionsCloseBesideTheLLMGatewaysDrain(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		closes, llm, bound, want time.Duration
		closedFirst              bool
	}{
		{name: "both end within the drain bound", closes: 3 * time.Second, llm: 3 * time.Second, bound: 30 * time.Second, want: 4 * time.Second, closedFirst: true},
		{name: "the sessions' close is the slower", closes: 4 * time.Second, llm: 2 * time.Second, bound: 30 * time.Second, want: 5 * time.Second, closedFirst: true},
		{name: "both outlast the drain bound", closes: time.Minute, llm: time.Minute, bound: 2 * time.Second, want: 3 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				st := newSteps()
				// closed closes once the sessions' close has returned, which
				// the bubble waits for: a close that outlasts the drain goes
				// on alone.
				closed := make(chan struct{})
				d := &drainer{
					leader: &drainingLeader{steps: st, done: make(chan struct{})},
					mcp:    drainingGateway{name: "mcp", steps: st, takes: time.Second},
					llm:    drainingGateway{name: "llm", steps: st, takes: tc.llm},
					closeUpstream: func() {
						defer close(closed)
						time.Sleep(tc.closes)
						st.note("sessions closed")
					},
					bound: tc.bound, busy: 20 * time.Second, log: slog.New(slog.DiscardHandler),
				}

				d.drain(t.Context())
				<-closed

				assert.Equal(t, time.Duration(0), st.at["leader drains"], "the leader drains first")
				assert.Equal(t, time.Second, st.at["mcp drained"], "then the MCP gateway")
				assert.Equal(t, tc.want, st.at["lease released"], "the release waits for the slower of the LLM gateway and the sessions, within the drain bound")
				if tc.closedFirst {
					assert.True(t, st.before("sessions closed", "lease released"), "the sessions closed before the release: %v", st.order)
				} else {
					assert.True(t, st.before("lease released", "sessions closed"), "a close that outlasts its bound goes on after the release: %v", st.order)
				}
			})
		})
	}
}

// stoppingLeader is a leader whose stop takes its time: Done closes takes
// after Stop, as it does once a take in flight that the drain gives up has
// ended. stopped is when Stop came, by the bubble's clock.
type stoppingLeader struct {
	takes   time.Duration
	stopped time.Time
	done    chan struct{}
}

func (l *stoppingLeader) Drain() {}

func (l *stoppingLeader) Stop(context.Context) {
	l.stopped = time.Now()
	time.AfterFunc(l.takes, func() { close(l.done) })
}

func (l *stoppingLeader) Done() <-chan struct{} { return l.done }

// The drain waits for the leader's stop up to the leader's busy bound
// (Decision 14), not the lease's time: a stop that ends within it, as a take
// in flight that the drain gives up does, is waited for; one that outlasts it
// is not, and the drain says so, at Warn, and goes on.
func TestTheDrainWaitsForTheLeaderUpToItsBusyBound(t *testing.T) {
	const notStopped = "the leader did not stop within its busy bound"
	for _, tc := range []struct {
		name          string
		takes, waited time.Duration
		warned        bool
	}{
		{name: "a stop within the bound", takes: 19 * time.Second, waited: 19 * time.Second},
		{name: "a stop past the bound", takes: 21 * time.Second, waited: 20 * time.Second, warned: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				st := newSteps()
				var out bytes.Buffer
				leader := &stoppingLeader{takes: tc.takes, done: make(chan struct{})}
				d := &drainer{
					leader: leader, mcp: drainingGateway{name: "mcp", steps: st}, llm: drainingGateway{name: "llm", steps: st},
					closeUpstream: func() {}, bound: 30 * time.Second, busy: 20 * time.Second, log: slog.New(slog.NewJSONHandler(&out, nil)),
				}

				d.drain(t.Context())
				waited := time.Since(leader.stopped)
				// The stop goes on alone, which the bubble waits for.
				<-leader.Done()

				assert.Equal(t, tc.waited, waited, "the drain's wait for the leader's stop")
				if tc.warned {
					assert.Equal(t, "WARN", logLine(t, out.String(), notStopped)["level"])
				} else {
					assert.NotContains(t, out.String(), notStopped)
				}
			})
		})
	}
}

// Serve's drain waits for the leader up to the leader's busy bound, not the
// lease's time (Decision 14): the drainer serve makes takes leaderBusy, which
// the defaults set above the lease's time, 20 s against 15 s, and the drain
// bound.
func TestServesDrainerTakesTheLeadersBusyBound(t *testing.T) {
	d := loadedDefaults(t)
	require.NotEqual(t, d.Timing.Lease.TTL, leaderBusy(d), "a deployment whose busy bound is not its lease's time")
	s := &server{d: d, log: slog.New(slog.DiscardHandler)}

	dr := s.drainer()

	assert.Equal(t, leaderBusy(d), dr.busy, "the leader's busy bound, not the lease's time")
	assert.Equal(t, d.Timing.Drain, dr.bound)
}

// loadedDefaults is the deployment of servedDeployment's file, which sets no
// timing, as Load gives it: its timings are the defaults.
func loadedDefaults(t *testing.T) *deploy.Deployment {
	t.Helper()
	deployment, _ := servedDeployment(t, "postgres://u:p@127.0.0.1:1/db", nil)
	d, err := deploy.Load(deployment)
	require.NoError(t, err)
	return d
}

// The leader's busy bound, the most a drain waits for the leader's stop, is
// takeWait + 3 × renewEvery (Decision 14): the path of a take in flight that
// the drain gives up, the take's sample of the database's clock, the take, and
// the release of what it took. The path of a take that succeeded, then
// sampled the clock and checked the schema, and then met the stop, 25 s with
// the defaults, is above it.
func TestTheLeadersBusyBoundCoversASampleATakeAndAGivenUpTakesRelease(t *testing.T) {
	defaults := loadedDefaults(t)
	for _, tc := range []struct {
		name   string
		change func(l *deploy.LeaseTiming)
		want   time.Duration
	}{
		{name: "the defaults", change: func(*deploy.LeaseTiming) {}, want: 20 * time.Second},
		{
			name: "a take's wait of 2 s and a renewal period of 1 s", want: 5 * time.Second,
			change: func(l *deploy.LeaseTiming) { l.TakeWait, l.RenewEvery = 2*time.Second, time.Second },
		},
	} {
		d := *defaults
		tc.change(&d.Timing.Lease)

		assert.Equal(t, tc.want, leaderBusy(&d), tc.name)
	}
	l := defaults.Timing.Lease
	succeeded := l.RenewEvery + (l.TakeWait + l.RenewEvery) + l.RenewEvery + l.RenewEvery
	assert.Equal(t, 25*time.Second, succeeded, "a sample, a take that succeeded, a sample and a schema check")
	assert.Greater(t, succeeded, leaderBusy(defaults), "that path is above the bound")
}

// The worst drain (R22, as Task 13 amends it) is the greater of the leader's
// path, 3 × mcp.recordTimeout + drain + 2 × renewEvery, and the standby's, the
// leader's busy bound; plus the close's eight bounds of stopWait (five
// servers, the registry's refresh and two pools; R114), KillAll's 1 s
// and the Docker runner's Close's 1 s. Each row moves one term from the
// defaults'.
func TestTheWorstDrainCountsEachTerm(t *testing.T) {
	defaults := loadedDefaults(t)
	const worst = time.Minute + 37*time.Second
	for _, tc := range []struct {
		name   string
		change func(tm *deploy.Timing)
		want   time.Duration
	}{
		{name: "the defaults give 1m37s", change: func(*deploy.Timing) {}, want: worst},
		{name: "mcp.recordTimeout 6 s adds 3 s", change: func(tm *deploy.Timing) { tm.MCP.RecordTimeout = 6 * time.Second }, want: worst + 3*time.Second},
		{name: "drain 40 s adds 10 s", change: func(tm *deploy.Timing) { tm.Drain = 40 * time.Second }, want: worst + 10*time.Second},
		{name: "renewEvery 4 s takes 2 s away", change: func(tm *deploy.Timing) { tm.Lease.RenewEvery = 4 * time.Second }, want: worst - 2*time.Second},
		{
			name: "a takeWait of 60 s, whose standby path then passes the leader's", change: func(tm *deploy.Timing) { tm.Lease.TakeWait = 60 * time.Second },
			// takeWait + 3 × renewEvery; then the close's 40 s (eight
			// stopWait), KillAll's 1 s and the runner's Close's 1 s.
			want: 60*time.Second + 15*time.Second + 42*time.Second,
		},
	} {
		d := *defaults
		tc.change(&d.Timing)

		assert.Equal(t, tc.want, worstDrain(&d), tc.name)
	}
}

// Under the Docker runner, the addresses file names the agents' listeners by
// their sockets, unix:<path>, and the admin API's by its host and port
// (Contract C2).
func TestTheAddressesNameTheSocketsUnderTheDockerRunner(t *testing.T) {
	cert, err := docker.NewCertificate(time.Now())
	require.NoError(t, err)
	dir := socketDir(t)
	var l listeners
	t.Cleanup(l.close)
	for _, socket := range []struct {
		ln   *net.Listener
		name string
	}{{&l.mcp, "mcp.sock"}, {&l.llm, "llm.sock"}} {
		ln, err := net.Listen("unix", filepath.Join(dir, socket.name))
		require.NoError(t, err)
		*socket.ln = sandboxListener(ln, &cert.TLS)
	}
	l.admin, err = net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	a := l.addresses()

	assert.Equal(t, "unix:"+filepath.Join(dir, "mcp.sock"), a.MCP)
	assert.Equal(t, "unix:"+filepath.Join(dir, "llm.sock"), a.LLM)
	assert.Equal(t, l.admin.Addr().String(), a.Admin)
	assert.Equal(t, os.Getpid(), a.PID)
}

// exitSteps notes, in order, what serve's exit does: the runner's calls, and
// each line serve logs, with its code when it has one.
type exitSteps struct {
	mu    sync.Mutex
	steps []string
}

func (e *exitSteps) note(step string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.steps = append(e.steps, step)
}

func (e *exitSteps) list() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.steps)
}

// exitingRunner is a runner, and the Docker runner's closer, as serve's exit
// acts on them: it notes its stops, its KillAll and its Close, which fails
// with closeErr when it is set.
type exitingRunner struct {
	steps    *exitSteps
	closeErr error
}

func (r exitingRunner) Start(context.Context, *app.Turn) (bool, error) { return false, nil }
func (r exitingRunner) Stop(board.RunID, string)                       { r.steps.note("Stop") }
func (r exitingRunner) StopAll()                                       { r.steps.note("StopAll") }
func (r exitingRunner) KillAll(context.Context)                        { r.steps.note("KillAll") }

func (r exitingRunner) Close() error {
	r.steps.note("Close")
	return r.closeErr
}

// notingHandler is a log handler that notes each line logged, as log <msg>,
// with code=<code> when it has a code.
type notingHandler struct{ steps *exitSteps }

func (h notingHandler) Enabled(context.Context, slog.Level) bool { return true }

//nolint:gocritic // hugeParam: slog.Handler takes a record by value.
func (h notingHandler) Handle(_ context.Context, r slog.Record) error {
	step := "log " + r.Message
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "code" {
			step += " code=" + a.Value.String()
		}
		return true
	})
	h.steps.note(step)
	return nil
}

func (h notingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h notingHandler) WithGroup(string) slog.Handler      { return h }

// Every exit of serve passes through exit (R19), which kills every turn
// first, then closes the Docker runner when there is one, the server's
// closer, never before KillAll, whose removals need the engine's client; then
// logs serve stopped with the code, and ends serve with it. A Close that
// fails is logged before it. Under the process runner, which has no closer,
// it kills and logs. A drain's close leaves the runner alone: it neither
// kills nor closes it.
func TestServeKillsEveryTurnBeforeItExits(t *testing.T) {
	steps := &exitSteps{}
	r := exitingRunner{steps: steps}
	s := &server{log: slog.New(notingHandler{steps}), runner: r, closer: r}

	err := s.exit(t.Context(), exitError)

	assert.Equal(t, []string{"KillAll", "Close", "log serve stopped code=1"}, steps.list(), "under the Docker runner")
	var code served
	require.ErrorAs(t, err, &code)
	assert.Equal(t, exitError, code.code)

	steps = &exitSteps{}
	r = exitingRunner{steps: steps, closeErr: errors.New("the engine's client did not close")}
	s = &server{log: slog.New(notingHandler{steps}), runner: r, closer: r}

	err = s.exit(t.Context(), exitError)

	require.Error(t, err)
	assert.Equal(t, []string{"KillAll", "Close", "log the Docker runner did not close", "log serve stopped code=1"}, steps.list(),
		"a Close that fails")

	steps = &exitSteps{}
	s = &server{log: slog.New(notingHandler{steps}), runner: exitingRunner{steps: steps}}

	err = s.exit(t.Context(), exitOK)

	require.NoError(t, err)
	assert.Equal(t, []string{"KillAll", "log serve stopped code=0"}, steps.list(), "under the process runner")

	steps = &exitSteps{}
	r = exitingRunner{steps: steps}
	s = &server{log: slog.New(notingHandler{steps}), runner: r, closer: r}

	s.close(t.Context())

	assert.Empty(t, steps.list(), "a drain's close")
}

// silentEngine is an engine that never answers, and counts its closes. Open
// asks it nothing but its version: its other calls are never made.
type silentEngine struct {
	docker.Engine
	closes atomic.Int32
}

func (e *silentEngine) Version(context.Context) (docker.Info, error) {
	return docker.Info{}, fmt.Errorf("no connection: %w", docker.ErrUnreachable)
}

func (e *silentEngine) Close() error {
	e.closes.Add(1)
	return nil
}

// A Docker runner that refuses to open leaves its engine to its caller
// (Contract C14): serve, which dialed the engine, closes it, once.
func TestServeClosesTheEngineWhenTheDockerRunnerRefusesToOpen(t *testing.T) {
	cert, err := docker.NewCertificate(time.Now())
	require.NoError(t, err)
	engine := &silentEngine{}
	p := docker.Ports{
		Engine:   engine,
		Holding:  app.NewHolding(15*time.Second, 5*time.Second, nil),
		Skills:   func(context.Context, string, string) ([]string, error) { return nil, nil },
		Ended:    func(context.Context, board.RunID, int, string) (bool, error) { return false, nil },
		Exited:   func(app.Exit) {},
		Launched: func(app.Launch) {},
	}
	o := docker.Options{
		Tenant: "example", Holder: "instance-a", Output: t.TempDir(), OutputCap: 1 << 20, MaxTokens: 4096,
		Sockets: "/run/agentswarm/a", Store: t.TempDir(), Group: 1000,
		Memory: 256 << 20, CPUs: 1, Pids: 64, Files: 256, Tmp: 16 << 20, Cert: cert.PEM,
	}

	r, err := openRunner(t.Context(), &p, &o)

	assert.Nil(t, r)
	require.ErrorContains(t, err, "runner.docker.endpoint", "the engine did not answer")
	assert.Equal(t, int32(1), engine.closes.Load(), "serve closed the engine once")
}

// An unset runner.docker.group is serve's own group, and that is root's, 0,
// when serve runs with gid 0, as root or a container's default user does:
// the sockets and every sandbox would share it (Plan 7d-2, Decision 10).
// Serve refuses it naming runner.docker.group (Decision 8), opens no runner,
// and calls no engine: the stand-in at the deployment's endpoint takes no
// connection.
func TestServeRefusesAnUnsetGroupThatResolvesToRoots(t *testing.T) {
	endpoint := filepath.Join(socketDir(t), "docker.sock")
	engine, err := net.Listen("unix", endpoint)
	require.NoError(t, err)
	t.Cleanup(func() { _ = engine.Close() })
	var calls atomic.Int32
	go func() {
		for {
			conn, err := engine.Accept()
			if err != nil {
				return
			}
			calls.Add(1)
			_ = conn.Close()
		}
	}()
	s := &server{d: &deploy.Deployment{
		Tenant: "example", Holder: "instance-a",
		Runner: deploy.Runner{
			Kind: deploy.KindDocker, Output: t.TempDir(), OutputCap: 1 << 20, MaxTokens: 4096,
			Docker: &deploy.Docker{
				Endpoint: "unix://" + endpoint, Sockets: "/run/agentswarm/a", Store: t.TempDir(), Group: -1,
				Memory: 256 << 20, CPUs: 1, Pids: 64, Files: 256, Tmp: 16 << 20,
			},
		},
	}, hold: app.NewHolding(15*time.Second, 5*time.Second, nil), log: slog.New(slog.DiscardHandler)}

	cert, err := s.openDocker(t.Context(), &later{}, 0)

	require.ErrorContains(t, err, "runner.docker.group: unset, so serve's own group, 0, which is root's")
	assert.Nil(t, cert)
	assert.Nil(t, s.runner, "serve opened no runner")
	assert.Nil(t, s.closer)
	assert.Zero(t, calls.Load(), "serve called the engine")
}
