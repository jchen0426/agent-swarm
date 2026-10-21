package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/deploy"
	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
)

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

// steps notes when each step of a drain came, by the bubble's clock.
type steps struct {
	mu    sync.Mutex
	start time.Time
	at    map[string]time.Duration
}

func newSteps() *steps { return &steps{start: time.Now(), at: map[string]time.Duration{}} }

func (s *steps) note(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.at[step] = time.Since(s.start)
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
// which Decision 6's failover wait counts once.
func TestTheUpstreamSessionsCloseBesideTheLLMGatewaysDrain(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		closes, llm, bound, want time.Duration
	}{
		{name: "both end within the drain bound", closes: 3 * time.Second, llm: 3 * time.Second, bound: 30 * time.Second, want: 4 * time.Second},
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
			})
		})
	}
}
