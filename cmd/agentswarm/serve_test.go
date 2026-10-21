package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
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

// bodies is an LLM gateway that keeps the size of each request's body it is
// given.
type bodies struct{ sizes []int }

func (b *bodies) Messages(_ context.Context, in *llmapp.Inbound) *llmapp.Reply {
	b.sizes = append(b.sizes, len(in.Body))
	return &llmapp.Reply{Status: http.StatusOK}
}

// Every listener bounds how long a request's header and body take to read
// (Decision 1), and the LLM listener reads no more of a body than the LLM
// gateway takes, from the one setting both have.
func TestServesHTTPServersBoundTheirReads(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	gw := &bodies{}

	s := newServers(nil, gw, http.NotFoundHandler(), log)

	for name, srv := range map[string]*http.Server{"mcp": s.mcp, "llm": s.llm, "admin": s.admin} {
		assert.Equal(t, 10*time.Second, srv.ReadHeaderTimeout, name)
		assert.Equal(t, 30*time.Second, srv.ReadTimeout, name)
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
					bound: tc.bound, ttl: 15 * time.Second, log: slog.New(slog.DiscardHandler),
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
