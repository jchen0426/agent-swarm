package app_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/llm/memory"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
)

const (
	model  = "claude-haiku-5-5"
	commit = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	token  = "session-token-123456"
)

var t0 = time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)

// files are a configuration with one agent, on the gateway's model, and a
// rule of secret patterns that applies always.
var files = map[string][]byte{
	"redaction-rules/api-keys.yaml": []byte(`kind: RedactionRule
metadata: {name: api-keys, owner: security@example.com}
spec:
  always: true
  patterns: ['sk-[a-z0-9]{8}']
`),
	"agents/flag-investigator.yaml": []byte(`kind: Agent
metadata: {name: flag-investigator, owner: alice@example.com}
spec:
  runtime: thinloop
  model: claude-haiku-5-5
  tools: []
`),
}

// card prices the model at $0.10 and $0.50 per million input and output
// tokens: with the reference at the input price, a cost-weighted token is
// an uncached input token, a cache write 1.25 of one, a read 0.1 and an
// output token 5.
var card = cost.Card{Base: cost.Rates{Input: 100_000, Output: 500_000, CacheWrite5m: 125_000, CacheWrite1h: 200_000, CacheRead: 10_000}}

type revisions struct{ rev *config.Revision }

func (r *revisions) At(_ context.Context, c string) (*config.Revision, error) {
	if c != commit {
		return nil, fmt.Errorf("no revision %s", c)
	}
	return r.rev, nil
}

// vendor is the model vendor, scripted: each attempt takes the next answer
// or error, and the last answer repeats. It keeps what each attempt sent,
// and whether the call's record existed when it did.
type vendor struct {
	calls *memory.Calls

	mu       sync.Mutex
	answers  []*app.Answer
	errs     []error
	bodies   []string
	recorded []bool
	// block, when set, holds every attempt until it is closed, or until
	// the attempt's context ends.
	block chan struct{}
	sent  atomic.Int32
	// active counts the attempts under way, and most the most at once.
	active, most atomic.Int32
}

func (v *vendor) Send(ctx context.Context, body []byte) (*app.Answer, error) {
	v.mu.Lock()
	n := len(v.bodies)
	v.bodies = append(v.bodies, string(body))
	records := v.calls.Records()
	v.recorded = append(v.recorded, len(records) > 0 && records[len(records)-1].Request == string(body))
	block := v.block
	v.mu.Unlock()
	v.sent.Add(1)
	active := v.active.Add(1)
	defer v.active.Add(-1)
	for {
		most := v.most.Load()
		if active <= most || v.most.CompareAndSwap(most, active) {
			break
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			// As an HTTP client does when its request is cancelled after it
			// was written.
			return nil, fmt.Errorf("%w: %w", app.ErrLost, ctx.Err())
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if n < len(v.errs) && v.errs[n] != nil {
		return nil, v.errs[n]
	}
	if len(v.answers) == 0 {
		return ok(), nil
	}
	return v.answers[min(n, len(v.answers)-1)], nil
}

func (v *vendor) attempts() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.bodies)
}

// thinking is the thinking block of ok's answer.
const thinking = `{"type": "thinking", "thinking": "", "signature": "EqQBCkgIBRAB"}`

// ok is an answer as the model gives one: a thinking block, a text and a
// tool call, and the tokens it used: 1,000 input, 400 written to the
// five-minute cache, 20,000 read from it and 300 out, 5,000 cost-weighted.
func ok() *app.Answer {
	return &app.Answer{Status: 200, RequestID: "req_1", Body: []byte(`{"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
 "content": [` + thinking + `, {"type": "text", "text": "Saw sk-zzzzzzzz in the logs."},
  {"type": "tool_use", "id": "toolu_9", "name": "metrics__query", "input": {"q": "x"}, "caller": {"type": "direct"}}],
 "stop_reason": "tool_use", "stop_sequence": null,
 "usage": {"input_tokens": 1000, "cache_creation_input_tokens": 400, "cache_read_input_tokens": 20000, "output_tokens": 300,
  "cache_creation": {"ephemeral_5m_input_tokens": 400, "ephemeral_1h_input_tokens": 0}, "service_tier": "standard"}}`)}
}

// saying is ok's answer with its text changed to text. The JSON of text is
// the vendor's: an escape in it is written as the vendor writes it.
func saying(text string) *app.Answer {
	a := ok()
	a.Body = []byte(strings.Replace(string(a.Body), "Saw sk-zzzzzzzz in the logs.", text, 1))
	return a
}

// withToolCall is the answer a with the id of its tool call changed to
// callID.
func withToolCall(a *app.Answer, callID string) *app.Answer {
	b := *a
	b.Body = []byte(strings.ReplaceAll(string(a.Body), "toolu_9", callID))
	return &b
}

type ids struct{ n atomic.Int32 }

func (i *ids) NewID() string { return fmt.Sprintf("m-%d", i.n.Add(1)) }

type env struct {
	gw       *app.Gateway
	sessions *memory.Sessions
	calls    *memory.Calls
	vendor   *vendor
	session  app.Session
	opts     app.Options
	ports    app.Ports
}

func options() app.Options {
	return app.Options{
		MaxBodyBytes: 64 << 10, MaxTokens: 16384, Version: "2023-06-01",
		Limits: cost.Limits{DailyCap: 1_000_000, ShadowCut: 80}, Cards: map[string]cost.Card{model: card}, Reference: 100_000,
		Vendor:  limits.Settings{Slots: 10, Failures: 3, CoolDown: time.Minute},
		Backoff: []time.Duration{time.Millisecond, 2 * time.Millisecond}, VendorTimeout: 10 * time.Second,
		RecordTimeout: time.Second, MaxRefusals: 10, LastCallWait: 5 * time.Second,
	}
}

func newEnv(t *testing.T, change ...func(*app.Options)) *env {
	t.Helper()
	cc, err := celeval.New()
	require.NoError(t, err)
	fsys, err := memfs.New(files)
	require.NoError(t, err)
	rev, err := config.Build(fsys, commit, cc)
	require.NoError(t, err)
	e := &env{sessions: memory.NewSessions(), calls: memory.NewCalls(), opts: options()}
	for _, c := range change {
		c(&e.opts)
	}
	e.vendor = &vendor{calls: e.calls}
	e.session = app.Session{Run: "run-1", Agent: "flag-investigator", Commit: commit, Round: 2, Class: cost.Primary,
		Budget: 2_000_000, Deadline: time.Now().Add(time.Minute)}
	e.sessions.Put(token, &e.session)
	e.ports = app.Ports{Sessions: e.sessions, Revisions: &revisions{rev}, Vendor: e.vendor, Calls: e.calls,
		IDs: &ids{}, Now: func() time.Time { return t0 }}
	e.gw, err = app.New(&e.ports, &e.opts)
	require.NoError(t, err)
	return e
}

// restart replaces the gateway with a new one, as a restart does: what it
// kept in memory is gone, the records stay.
func (e *env) restart(t *testing.T) {
	t.Helper()
	gw, err := app.New(&e.ports, &e.opts)
	require.NoError(t, err)
	e.gw = gw
}

// headers are what anthropic-sdk-go sends.
func headers() map[string][]string {
	return map[string][]string{"Anthropic-Version": {"2023-06-01"}, "Content-Type": {"application/json"}, "X-Stainless-Os": {"Linux"}}
}

// request is a request as the runner sends one at a turn's start.
const request = `{"model": "claude-haiku-5-5", "max_tokens": 1024,
 "system": [{"type": "text", "text": "You investigate incidents.", "cache_control": {"type": "ephemeral"}}],
 "tools": [{"name": "metrics__query", "description": "Query metrics.", "input_schema": {"type": "object"}}],
 "messages": [{"role": "user", "content": "Find why checkout fails."}]}`

// next is the request the runner sends after ok's answer: the answer
// echoed back, its thinking block included, and the tool's result.
const next = `{"model": "claude-haiku-5-5", "max_tokens": 1024,
 "system": [{"type": "text", "text": "You investigate incidents.", "cache_control": {"type": "ephemeral"}}],
 "tools": [{"name": "metrics__query", "description": "Query metrics.", "input_schema": {"type": "object"}}],
 "messages": [{"role": "user", "content": "Find why checkout fails."},
  {"role": "assistant", "content": [` + thinking + `, {"type": "text", "text": "Saw sk-zzzzzzzz in the logs."},
   {"type": "tool_use", "id": "toolu_9", "name": "metrics__query", "input": {"q": "x"}, "caller": {"type": "direct"}}]},
  {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_9", "content": "errors rose"}]}]}`

// echoing is the request the runner sends after an answer whose tool call
// has the id callID: the answer echoed back, its thinking block left out, so
// that only the id is in question, and the tool's result.
func echoing(callID string) string {
	return strings.ReplaceAll(strings.Replace(next, thinking+", ", "", 1), "toolu_9", callID)
}

func (e *env) send(t *testing.T, tok, body string) *app.Reply {
	t.Helper()
	return e.sendWith(t.Context(), tok, body)
}

// sendWith sends body with the session token tok, as a request whose
// context is ctx.
func (e *env) sendWith(ctx context.Context, tok, body string) *app.Reply {
	return e.gw.Messages(ctx, &app.Inbound{Method: "POST", Path: "/v1/messages", Token: tok, Header: headers(), Body: []byte(body)})
}

// stray sends a request the gateway refuses before reading it, with the
// session token tok.
func (e *env) stray(t *testing.T, tok string) *app.Reply {
	t.Helper()
	return e.gw.Messages(t.Context(), &app.Inbound{Method: "GET", Path: "/v1/models", Token: tok, Header: headers()})
}

func (e *env) post(t *testing.T, body string) *app.Reply {
	t.Helper()
	return e.send(t, token, body)
}

// last returns the last call's record and outcome.
func (e *env) last(t *testing.T) (app.CallRecord, app.Outcome) {
	t.Helper()
	records := e.calls.Records()
	require.NotEmpty(t, records, "no call was recorded")
	rec, o, _, ended := e.calls.Record(records[len(records)-1].ID)
	require.True(t, ended, "the call has no outcome")
	return rec, o
}
