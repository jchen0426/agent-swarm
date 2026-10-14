package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/actions/memory"
	"github.com/jchen0426/agent-swarm/internal/actions/output"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
)

// The flags server's tools, as the server defines them.
var (
	getFlag = config.ToolDefinition{
		Name: "get_flag", Description: "Read a flag's state.",
		InputSchema: []byte(`{"type":"object","properties":{"flag":{"type":"string"},"window":{"type":"string"}},"required":["flag"]}`),
	}
	listFlags = config.ToolDefinition{Name: "list_flags", Description: "List the flags.", InputSchema: []byte(`{"type":"object"}`)}
	setFlag   = config.ToolDefinition{
		Name: "set_flag", Description: "Set a flag.",
		InputSchema: []byte(`{"type":"object","properties":{"flag":{"type":"string"},"on":{"type":"boolean"}}}`),
	}
	owners = config.ToolDefinition{
		Name: "owners", Description: "Who owns a flag.",
		InputSchema: []byte(`{"type":"object","properties":{"flag":{"type":"string"},"limit":{"type":"integer"}}}`),
	}
)

func hash(t *testing.T, def config.ToolDefinition) string { //nolint:gocritic // tests pass definitions by value for brevity
	t.Helper()
	h, err := config.ToolSchemaHash(&def)
	require.NoError(t, err)
	return h
}

// revision builds the configuration the tests run under: one server whose
// tools are pinned to the given definitions, and one agent. note goes into a
// comment of the registration, so that a revision can review it again.
func revision(t *testing.T, cc *celeval.Conditions, commit string, pins map[string]config.ToolDefinition, note string) *config.Revision {
	t.Helper()
	files, err := memfs.New(map[string][]byte{
		"redaction-rules/private-keys.yaml": []byte(`kind: RedactionRule
metadata: {name: private-keys, owner: security@example.com}
spec:
  always: true
  patterns: ['-----BEGIN [A-Z ]*PRIVATE KEY-----']
`),
		"redaction-rules/default-secrets.yaml": []byte(`kind: RedactionRule
metadata: {name: default-secrets, owner: security@example.com}
spec:
  patterns: ['AKIA[0-9A-Z]{16}']
  paths: ['$..password']
`),
		"servers/flags.yaml": []byte(fmt.Sprintf(`# %s
kind: McpServer
metadata: {name: flags, owner: platform-team}
spec:
  endpoint: https://flags.internal/mcp
  image: "sha256:%s"
  credentialRef: secret://mcp/flags
  tools:
    get_flag:
      schemaHash: %q
      effect: read
      redact: [default-secrets]
      partialWhen: "!(result.truncated == false)"
      bounds: {window: {max: 6h}}
    list_flags: {schemaHash: %q, effect: read}
    set_flag:
      schemaHash: %q
      effect: write
      target: [flag]
      scope: {flag: hypothesis.flag}
      candidates:
        flag: {tool: list_flags, field: "$[*].name"}
      summary: Set {args.flag}
    owners: {schemaHash: %q, effect: read, audience: platform}
`, note, strings.Repeat("a", 64), hash(t, pins["get_flag"]), hash(t, pins["list_flags"]), hash(t, pins["set_flag"]), hash(t, pins["owners"]))),
		"agents/flag-investigator.yaml": []byte(`kind: Agent
metadata: {name: flag-investigator, owner: alice@example.com}
spec:
  runtime: thinloop
  model: claude-haiku-5-5
  tools: [flags.get_flag, flags.list_flags, flags.set_flag]
`),
	})
	require.NoError(t, err)
	rev, err := config.Build(files, commit, cc)
	require.NoError(t, err)
	return rev
}

func defaultPins() map[string]config.ToolDefinition {
	return map[string]config.ToolDefinition{"get_flag": getFlag, "list_flags": listFlags, "set_flag": setFlag, "owners": owners}
}

// revisions holds revisions by commit.
type revisions struct {
	mu   sync.Mutex
	revs map[string]*config.Revision
}

func (r *revisions) At(_ context.Context, commit string) (*config.Revision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rev, ok := r.revs[commit]
	if !ok {
		return nil, errors.New("no such commit")
	}
	return rev, nil
}

func (r *revisions) add(rev *config.Revision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revs[rev.Commit()] = rev
}

// upstream is a programmable stand-in for the servers.
type upstream struct {
	mu       sync.Mutex
	defs     []config.ToolDefinition
	broken   map[string]error
	listErr  error         // the error of every listing, when set
	gate     chan struct{} // when set, a listing waits until it closes
	replies  map[string]*output.Result
	errs     []error // the errors of the next calls, in order
	calls    []call
	listings int
	onCall   func(call)
}

type call struct {
	conn app.Conn
	tool string
	args string
}

func (u *upstream) Offered(ctx context.Context, _ *app.Server) (map[string]app.Offer, error) {
	u.mu.Lock()
	u.listings++
	gate, err := u.gate, u.listErr
	out := map[string]app.Offer{}
	for _, d := range u.defs {
		out[d.Name] = app.Offer{Def: d, Err: u.broken[d.Name]}
	}
	u.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (u *upstream) listed() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.listings
}

func (u *upstream) Call(ctx context.Context, c *app.Conn, tool string, args json.RawMessage) (*output.Result, error) {
	u.mu.Lock()
	if len(u.errs) > 0 {
		err := u.errs[0]
		u.errs = u.errs[1:]
		u.mu.Unlock()
		return nil, err
	}
	cl := call{conn: *c, tool: tool, args: string(args)}
	u.calls = append(u.calls, cl)
	onCall, res := u.onCall, u.replies[tool]
	u.mu.Unlock()
	if onCall != nil {
		onCall(cl)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if res == nil {
		res = &output.Result{Blocks: []output.Block{{Kind: output.Text, Text: "on"}}}
	}
	return res, nil
}

func (u *upstream) sent() []call {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]call(nil), u.calls...)
}

func (u *upstream) set(f func(u *upstream)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	f(u)
}

type ids struct {
	mu sync.Mutex
	n  int
}

func (i *ids) NewID() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return fmt.Sprintf("call-%d", i.n)
}

// clock starts now, because deadlines are real time; tests move it on.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// failingAudit fails to record calls, or only their outcomes.
type failingAudit struct {
	*memory.Audit
	begin bool
}

func (f failingAudit) Begin(ctx context.Context, rec *app.CallRecord) error {
	if f.begin {
		return errors.New("database down")
	}
	return f.Audit.Begin(ctx, rec)
}

func (f failingAudit) End(context.Context, *app.Outcome) error { return errors.New("database down") }

// hangingAudit and hangingHides never finish a write before its context
// ends, as a database that hangs.
type hangingAudit struct{ *memory.Audit }

func (hangingAudit) Begin(ctx context.Context, _ *app.CallRecord) error {
	<-ctx.Done()
	return ctx.Err()
}

func (hangingAudit) End(ctx context.Context, _ *app.Outcome) error {
	<-ctx.Done()
	return ctx.Err()
}

type hangingHides struct{ *memory.Hides }

func (hangingHides) Hide(ctx context.Context, _ *app.Hide) error {
	<-ctx.Done()
	return ctx.Err()
}

// env is a gateway with fakes around it.
type env struct {
	gw       *app.Gateway
	cc       *celeval.Conditions
	rev      *config.Revision
	revs     *revisions
	up       *upstream
	audit    *memory.Audit
	hides    *memory.Hides
	sessions *memory.Sessions
	clock    *clock
	session  app.Session
	opts     app.Options
}

const token = "tok"

func newEnv(t *testing.T, change ...func(*app.Ports, *app.Options)) *env {
	t.Helper()
	cc, err := celeval.New()
	require.NoError(t, err)
	rev := revision(t, cc, "c0ffee0c0ffee0c0ffee0c0ffee0c0ffee0c0ff", defaultPins(), "reviewed")
	e := &env{
		cc: cc, rev: rev, revs: &revisions{revs: map[string]*config.Revision{rev.Commit(): rev}},
		audit: memory.NewAudit(), hides: &memory.Hides{}, sessions: memory.NewSessions(), clock: &clock{t: time.Now()},
		up: &upstream{defs: []config.ToolDefinition{getFlag, listFlags, setFlag, owners}, broken: map[string]error{}, replies: map[string]*output.Result{
			"get_flag": {Blocks: []output.Block{{Kind: output.Text, Text: `{"flag":"x","on":true,"truncated":false}`}}},
		}},
	}
	e.session = app.Session{
		Run: "run-1", Agent: "flag-investigator", Commit: rev.Commit(), Round: 1,
		Deadline: time.Now().Add(150 * time.Second), Class: limits.Primary,
	}
	e.sessions.Put(token, &e.session)
	p := &app.Ports{
		Sessions: e.sessions, Revisions: e.revs, Upstream: e.up, Audit: e.audit, Hides: e.hides,
		Conditions: cc, IDs: &ids{}, Limits: limits.New(limits.Defaults, nil, e.clock.now), Now: e.clock.now,
	}
	e.opts = app.Defaults
	e.opts.Backoff = time.Millisecond
	for _, f := range change {
		f(p, &e.opts)
	}
	e.gw, err = app.New(t.Context(), p, e.opts)
	require.NoError(t, err)
	return e
}

// nextRound moves the session to its next round.
func (e *env) nextRound() {
	e.session.Round++
	e.session.Deadline = time.Now().Add(150 * time.Second)
	e.sessions.Put(token, &e.session)
}

// later moves the clock past the time a look at the server is fresh.
func (e *env) later() { e.clock.advance(e.opts.VerifyEvery + time.Second) }

func (e *env) call(t *testing.T, name, args string) *app.Response {
	t.Helper()
	r, err := e.gw.Call(t.Context(), token, name, json.RawMessage(args))
	require.NoError(t, err)
	return r
}

// sameBytes asserts that got is exactly want, byte for byte: what was checked
// is what is sent.
func sameBytes(t *testing.T, want, got string) {
	t.Helper()
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
