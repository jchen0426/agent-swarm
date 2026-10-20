package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	actionsmemory "github.com/jchen0426/agent-swarm/internal/actions/memory"
	"github.com/jchen0426/agent-swarm/internal/actions/output"
	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
	"github.com/jchen0426/agent-swarm/internal/work/sessions"
)

const commit = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"

// holder is the harness's instance's name, as the lease records it.
const holder = "instance-a"

// image is the runtime image the deployment pins for thinloop.
var image = "sha256:" + strings.Repeat("a", 64)

// search is the logs server's tool, and logLines what it answers.
var search = config.ToolDefinition{
	Name: "search", Description: "Search the logs.",
	InputSchema: []byte(`{"type":"object","properties":{"q":{"type":"string"}}}`),
}

const logLines = `{"lines":["checkout v42 deployed at 10:02","errors rose at 10:03"]}`

// configuration is a revision with an incident type of three rounds of at
// most two minutes, ten minutes of wall clock and 90,000 tokens, where two
// agents that read one source converge; two agents subscribed to it, who
// read the logs, and a third that is not; a change type no agent subscribes
// to; and a rule that applies always.
func configuration(t *testing.T) map[string][]byte {
	t.Helper()
	searchHash, err := config.ToolSchemaHash(&search)
	require.NoError(t, err)
	return map[string][]byte{
		"work-item-types/incident.yaml": []byte(`kind: WorkItemType
metadata: {name: incident, owner: sre-leads}
spec:
  hypothesisKey:
    - {name: service, values: [checkout, payments]}
    - {name: failureMode, values: [bad-release, config-change]}
  budget: {wallClock: 10m, tokens: 90000, rounds: 3}
  round: {timeLimit: 2m}
  convergence: {minContributors: 2, minSources: 1, refutationMargin: 2}
`),
		"work-item-types/change.yaml": []byte(`kind: WorkItemType
metadata: {name: change, owner: sre-leads}
spec:
  hypothesisKey:
    - {name: service, values: [checkout]}
`),
		"work-item-types/catalogued.yaml": []byte(`kind: WorkItemType
metadata: {name: catalogued, owner: sre-leads}
spec:
  hypothesisKey:
    - {name: service, values: {tool: catalog.list_services, field: "$[*].name"}}
`),
		"servers/catalog.yaml": []byte(`kind: McpServer
metadata: {name: catalog, owner: platform}
spec:
  endpoint: https://catalog.internal/mcp
  image: "sha256:` + strings.Repeat("c", 64) + `"
  tools:
    list_services: {effect: read, schemaHash: "sha256:` + strings.Repeat("b", 64) + `"}
`),
		"servers/logs.yaml": []byte(`kind: McpServer
metadata: {name: logs, owner: platform}
spec:
  endpoint: https://logs.internal/mcp
  image: "sha256:` + strings.Repeat("d", 64) + `"
  tools:
    search: {effect: read, schemaHash: "` + searchHash + `"}
`),
		"agents/release-investigator.yaml":  agent("release-investigator", "incident"),
		"agents/runtime-investigator.yaml":  agent("runtime-investigator", "incident"),
		"agents/capacity-investigator.yaml": agent("capacity-investigator", ""),
		"redaction-rules/api-keys.yaml": []byte(`kind: RedactionRule
metadata: {name: api-keys, owner: security@example.com}
spec:
  always: true
  patterns: ['sk-[a-z0-9]{8}']
`),
	}
}

// agent is an agent of the platform's runtime and model that reads the
// logs, subscribed to one type, or to none.
func agent(name, subscribed string) []byte {
	return fmt.Appendf(nil, `kind: Agent
metadata: {name: %s, owner: alice@example.com}
spec:
  runtime: thinloop
  model: claude-haiku-5-5
  tools: [logs.search]
  subscribe: {workItemTypes: [%s]}
`, name, subscribed)
}

// revisions is the registry: the revisions it holds by commit, and the
// current one, which those of then replace one by one, a call of Current
// after another, as when the registry refreshes while an item opens and
// starts.
type revisions struct {
	mu   sync.Mutex
	rev  *config.Revision
	then []*config.Revision
	all  map[string]*config.Revision
}

func (r *revisions) Current() *config.Revision {
	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.rev
	if len(r.then) > 0 {
		r.rev, r.then = r.then[0], r.then[1:]
	}
	return current
}

func (r *revisions) At(_ context.Context, c string) (*config.Revision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rev := r.all[c]; rev != nil {
		return rev, nil
	}
	return nil, fmt.Errorf("no revision %s", c)
}

// set makes rev the current revision; next makes revs the current ones, one
// by one, from Current's next call on.
func (r *revisions) set(rev *config.Revision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rev = rev
}

func (r *revisions) next(revs ...*config.Revision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.then = revs
}

func (r *revisions) Lock(rev *config.Revision) (config.Lock, error) {
	return rev.Lock(map[string]string{"thinloop": image})
}

// clock is the Clock and the Timers, moved by hand: Advance fires the
// timers that come due on the way, in order, outside its lock. It starts at
// the real time, which the MCP gateway's call deadlines are measured in. Once
// run, it follows the real time instead, and is not moved by hand.
type clock struct {
	mu     sync.Mutex
	now    time.Time
	runs   bool
	timers []*timer
	n      int
}

type timer struct {
	at        time.Time
	n         int
	fn        func()
	cancelled bool
}

func newClock() *clock { return &clock{now: time.Now().UTC().Truncate(time.Second)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runs {
		return time.Now().UTC().Round(0)
	}
	return c.now
}

// run makes the clock follow the real time from here on, with no monotonic
// reading, as the database's time has none.
func (c *clock) run() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runs = true
}

func (c *clock) Local(t time.Time) time.Time { return t }

func (c *clock) At(t time.Time, fn func()) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	tm := &timer{at: t, n: c.n, fn: fn}
	c.timers = append(c.timers, tm)
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		tm.cancelled = true
	}
}

// Advance moves the clock d on, firing each timer as its time comes.
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.SliceStable(c.timers, func(i, j int) bool {
			return c.timers[i].at.Before(c.timers[j].at) || c.timers[i].at.Equal(c.timers[j].at) && c.timers[i].n < c.timers[j].n
		})
		var due *timer
		for len(c.timers) > 0 && (c.timers[0].cancelled || !c.timers[0].at.After(end)) {
			tm := c.timers[0]
			c.timers = c.timers[1:]
			if !tm.cancelled {
				due = tm
				break
			}
		}
		if due == nil {
			// A timer's function may have moved the clock on past end.
			if c.now.Before(end) {
				c.now = end
			}
			c.mu.Unlock()
			return
		}
		if due.at.After(c.now) {
			c.now = due.at
		}
		c.mu.Unlock()
		due.fn()
	}
}

// goTimers runs each timer's function on a goroutine of its own, as a real
// clock's timers do, at the clock's times; wait waits until none runs and
// none is due.
type goTimers struct {
	c  *clock
	wg sync.WaitGroup
}

func (g *goTimers) At(t time.Time, fn func()) func() {
	return g.c.At(t, func() {
		g.wg.Go(fn)
	})
}

func (g *goTimers) wait() {
	for {
		g.wg.Wait()
		if !g.c.due() {
			return
		}
		g.c.Advance(0)
	}
}

// due reports whether a timer armed and not cancelled is due.
func (c *clock) due() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, tm := range c.timers {
		if !tm.cancelled && !tm.at.After(c.now) {
			return true
		}
	}
	return false
}

// pending counts the timers armed and not cancelled.
func (c *clock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, tm := range c.timers {
		if !tm.cancelled {
			n++
		}
	}
	return n
}

// spend is the LLM gateway's records, as the round's ends read them: the
// model calls in flight are counted by round, as model_calls records each
// call's round.
type spend struct {
	mu       sync.Mutex
	spent    map[string]int64
	day      int64
	inFlight map[roundOf]int
	failed   map[string][]app.ModelCall
}

// roundOf names a round of a run, as the gateways' records do.
type roundOf struct {
	run   string
	round int
}

func newSpend() *spend {
	return &spend{spent: map[string]int64{}, inFlight: map[roundOf]int{}, failed: map[string][]app.ModelCall{}}
}

func (s *spend) Spent(_ context.Context, run string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spent[run], nil
}

func (s *spend) SpentToday(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.day, nil
}

func (s *spend) InFlight(_ context.Context, run string, round int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight[roundOf{run: run, round: round}], nil
}

func (s *spend) Failed(_ context.Context, run string) ([]app.ModelCall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.failed[run]), nil
}

func (s *spend) set(f func(s *spend)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// script is what an agent does in its turn, through the MCP gateway; it
// returns how the turn ended, or "" for a turn that never ends by itself. A
// script ends a turn ended through endTurn, so that the board holds the
// agent's turn end, as the runtime ends a turn ended only once its end_turn
// call succeeded (Decision 13); play checks that it does.
type script func(h *harness, t *app.Turn) item.TurnClass

// endTurn ends the turn at once: the agent's end_turn is posted on the board.
// As the runtime does, it tries end_turn once more when the gateway answers
// it unavailable, as it answers a board call whose board it could not reach
// (Plan 7d-1, Decisions 13 and 20).
func endTurn(h *harness, t *app.Turn) item.TurnClass {
	r, err := h.gw.Call(h.t.Context(), t.MCP.Reveal(), "end_turn", json.RawMessage(`{}`))
	if errors.Is(err, actionsapp.ErrUnavailable) {
		r, err = h.gw.Call(h.t.Context(), t.MCP.Reveal(), "end_turn", json.RawMessage(`{}`))
	}
	require.NoError(h.t, err)
	require.Equal(h.t, actionsapp.StatusOK, r.Status, r.Text)
	return item.TurnEnded
}

// endTurnUnreported posts the agent's end_turn on the board, but its turn's
// end never reaches the coordinator, as when the runner's report of it is
// lost: the deadline counts the turn as one still running.
func endTurnUnreported(h *harness, t *app.Turn) item.TurnClass {
	endTurn(h, t)
	return ""
}

// ending ends the turn as class says, without end_turn: a class other than
// ended.
func ending(class item.TurnClass) script {
	return func(*harness, *app.Turn) item.TurnClass { return class }
}

// hypothesis reads the logs, posts what they show as evidence, and posts
// that checkout's bad release is the cause, citing it, then ends the turn:
// two agents that do so make the hypothesis converge.
func hypothesis(h *harness, t *app.Turn) item.TurnClass {
	read := h.call(t, "logs__search", `{"q":"checkout"}`)
	require.Equal(h.t, actionsapp.StatusOK, read.Status, read.Text)
	ev := h.call(t, "post_evidence", fmt.Sprintf(`{"claim":"v42 went out a minute before the errors","call_id":%q,"quote":"checkout v42 deployed at 10:02"}`, read.CallID))
	require.Equal(h.t, actionsapp.StatusOK, ev.Status, ev.Text)
	r := h.call(t, "post_hypothesis", fmt.Sprintf(`{"key":{"service":"checkout","failureMode":"bad-release"},"statement":"v42 broke checkout","evidence_ids":[%q]}`,
		strings.TrimPrefix(ev.Text, "posted evidence ")))
	require.Equal(h.t, actionsapp.StatusOK, r.Status, r.Text)
	return endTurn(h, t)
}

// turns is the Turns port, scripted: a turn started waits until the test
// plays it. fail, when set, says why the runner cannot start a turn.
type turns struct {
	mu      sync.Mutex
	queued  []app.Turn
	started []app.Turn
	stopped []string
	all     int
	fail    func(t *app.Turn) error
}

func (s *turns) Start(_ context.Context, t *app.Turn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		if err := s.fail(t); err != nil {
			return err
		}
	}
	s.queued = append(s.queued, *t)
	s.started = append(s.started, *t)
	return nil
}

func (s *turns) Stop(run board.RunID, agent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued = slices.DeleteFunc(s.queued, func(t app.Turn) bool { return t.Run == run && t.Agent == agent })
	s.stopped = append(s.stopped, fmt.Sprintf("%s/%s", run, agent))
}

func (s *turns) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued = nil
	s.all++
}

func (s *turns) take() []app.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.queued
	s.queued = nil
	return out
}

// harness is a coordinator over stores in memory, the clock, and agents
// that act through the MCP gateway with the tokens their turns were given.
// The stores, the clock, the spend and the registry of configurations are
// the database's and the world's; the coordinator, the live sessions, the
// gateway and the turns are one instance's, which a crash loses.
type harness struct {
	t       *testing.T
	clock   *clock
	records *failing
	store   *failingStore
	boards  *app.Boards
	audit   *actionsmemory.Audit
	spend   *spend
	tools   *toolCalls
	revs    *revisions
	cc      *celeval.Conditions
	ids     *ids
	sweeper *sweeper
	opts    app.Options
	scripts map[string]script
	epoch   int64
	// expired is when the last instance's holding of the lease expired:
	// when it crashed; previous is that instance's name, as the next take
	// reads it, "" for none.
	expired  time.Time
	previous string
	// timers are the instances' timers: the clock's, unless a test gives
	// others.
	timers app.Timers

	c        *app.Coordinator
	registry *sessions.Registry
	gw       *actionsapp.Gateway
	turns    *turns
}

// epochs notes the epoch of the holding each write acts for, as its context
// carries it, or 0 for none.
type epochs struct {
	mu   sync.Mutex
	seen []int64
}

func (e *epochs) note(ctx context.Context) {
	epoch, _ := holding.EpochFrom(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = append(e.seen, epoch)
}

// noted returns the epochs noted, in the order of the writes.
func (e *epochs) noted() []int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.seen)
}

// failing is the records store, which can fail an append: fail returns the
// error for the nth append, counting from 1, or nil. As a database does, it
// refuses an append whose context ended. It notes the epoch of each append,
// as the guarded store checks it.
type failing struct {
	*memory.Records
	writes epochs
	mu     sync.Mutex
	n      int
	fail   func(n int) error
	// hold, when set, makes an append wait until it is closed or its
	// context ends, as a store that hangs; waiting counts those that wait.
	hold    chan struct{}
	waiting atomic.Int32
}

func (f *failing) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	f.writes.note(ctx)
	f.mu.Lock()
	f.n++
	n, fail, hold := f.n, f.fail, f.hold
	f.mu.Unlock()
	if fail != nil {
		if err := fail(n); err != nil {
			return nil, err
		}
	}
	if hold != nil {
		f.waiting.Add(1)
		select {
		case <-hold:
		case <-ctx.Done():
		}
		f.waiting.Add(-1)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.Records.Append(ctx, id, decide)
}

// hang makes appends wait, until the returned function lets them go.
func (f *failing) hang() (letGo func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hold := make(chan struct{})
	f.hold = hold
	return sync.OnceFunc(func() {
		f.mu.Lock()
		f.hold = nil
		f.mu.Unlock()
		close(hold)
	})
}

func (f *failing) set(fail func(n int) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

// failingStore is the boards' store, which can fail an append as failing
// does, and notes the epoch of each append as failing does.
type failingStore struct {
	*memory.Store
	writes epochs
	mu     sync.Mutex
	n      int
	fail   func(n int) error
}

func (f *failingStore) Append(ctx context.Context, run board.RunID, decide func([]board.Event) ([]board.Draft, error)) ([]board.Event, error) {
	f.writes.note(ctx)
	f.mu.Lock()
	f.n++
	n, fail := f.n, f.fail
	f.mu.Unlock()
	if fail != nil {
		if err := fail(n); err != nil {
			return nil, err
		}
	}
	return f.Store.Append(ctx, run, decide)
}

func (f *failingStore) set(fail func(n int) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

// ids issues readable sequential ids for one harness.
type ids struct {
	mu sync.Mutex
	n  int
}

func (i *ids) NewID() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return fmt.Sprintf("id-%d", i.n)
}

// logs is the logs server: it offers its search tool, which answers
// logLines.
type logs struct{}

func (logs) Offered(_ context.Context, s *actionsapp.Server) (map[string]actionsapp.Offer, error) {
	if s.Name != "logs" {
		return nil, errors.New("no such server")
	}
	return map[string]actionsapp.Offer{"search": {Def: search}}, nil
}

func (logs) Call(context.Context, *actionsapp.Conn, string, json.RawMessage) (*output.Result, error) {
	return &output.Result{Blocks: []output.Block{{Kind: output.Text, Text: logLines}}}, nil
}

// holds is the holding the gateway asks before a request to a server: the
// harness's lease always holds, since its coordinator never runs past it.
type holds struct{}

func (holds) Valid(int64) bool { return true }

func newHarness(t *testing.T, change ...func(*app.Options)) *harness {
	t.Helper()
	cc, err := celeval.New()
	require.NoError(t, err)
	fsys, err := memfs.New(configuration(t))
	require.NoError(t, err)
	rev, err := config.Build(fsys, commit, cc)
	require.NoError(t, err)
	h := &harness{t: t, clock: newClock(), spend: newSpend(), revs: &revisions{rev: rev, all: map[string]*config.Revision{commit: rev}}, cc: cc, ids: &ids{}, sweeper: &sweeper{}, scripts: map[string]script{}}
	h.timers = h.clock
	h.tools = newToolCalls(h.clock.Now)
	h.records = &failing{Records: memory.NewRecords(h.clock.Now)}
	h.store = &failingStore{Store: memory.NewStore(h.clock.Now)}
	h.audit = actionsmemory.NewAudit()
	h.boards = app.NewBoards(h.store, h.audit, h.records, h.ids)
	h.opts = app.Options{Holder: holder, DailyCap: 1_000_000, Grace: 5 * time.Second, SettleWait: 30 * time.Second, SettlePoll: time.Second, Retry: 5 * time.Second}
	for _, f := range change {
		f(&h.opts)
	}
	// Every write of the coordinator, and of the gateway for the sessions
	// it issued, acts for a holding: the guarded stores refuse one that
	// carries no epoch.
	t.Cleanup(func() {
		for _, epoch := range append(h.records.writes.noted(), h.store.writes.noted()...) {
			if epoch == 0 {
				t.Error("a write carried no epoch")
				return
			}
		}
	})
	require.NoError(t, h.boot())
	return h
}

// revision builds a revision of the harness's configuration, as change
// edits its files, under commit, and keeps it in the registry.
func (h *harness) revision(commit string, change func(files map[string][]byte)) *config.Revision {
	h.t.Helper()
	files := configuration(h.t)
	change(files)
	fsys, err := memfs.New(files)
	require.NoError(h.t, err)
	rev, err := config.Build(fsys, commit, h.cc)
	require.NoError(h.t, err)
	h.revs.mu.Lock()
	defer h.revs.mu.Unlock()
	h.revs.all[commit] = rev
	return rev
}

// boot starts an instance: a coordinator, its sessions, its MCP gateway and
// its turns, which takes the lease over.
func (h *harness) boot() error {
	h.t.Helper()
	h.registry, h.turns = sessions.New(), &turns{}
	var err error
	h.gw, err = actionsapp.New(h.t.Context(), &actionsapp.Ports{
		Sessions: h.registry.MCP(), Revisions: h.revs, Upstream: logs{}, Audit: h.audit, Hides: &actionsmemory.Hides{},
		Conditions: h.cc, IDs: h.ids, Limits: limits.New(limits.Defaults, nil, h.clock.Now), Now: h.clock.Now, Board: h.boards,
		Holding: holds{},
	}, &actionsapp.Defaults)
	require.NoError(h.t, err)
	h.c, err = app.NewCoordinator(h.ports(), &h.opts)
	require.NoError(h.t, err)
	h.epoch++
	return h.c.TakeOver(h.t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.expired, PreviousHolder: h.previous})
}

func (h *harness) ports() *app.Ports {
	return &app.Ports{
		Records: h.records, Boards: h.boards, Spend: h.spend, ToolCalls: h.tools, Sweeper: h.sweeper, Revisions: h.revs, Sessions: h.registry,
		Turns: h.turns, Clock: h.clock, Timers: h.timers, IDs: h.ids,
	}
}

// crash loses the instance: its timers, its sessions and its turns die with
// it.
func (h *harness) crash() {
	h.c.Fence()
	h.expired = h.clock.Now()
}

// open opens an incident, as a person does.
func (h *harness) open(title string) item.ID {
	h.t.Helper()
	id, err := h.c.Open(h.t.Context(), &app.OpenInput{Type: "incident", Title: title, Source: "person", Routing: map[string]string{"service": "checkout"}})
	require.NoError(h.t, err)
	return id
}

// play plays every turn started, each by its agent's script (end_turn by
// default), and reports how each ended; it returns the turns played. Each turn
// it reports ended has the agent's turn end on the board, as Decision 9 reads
// it.
func (h *harness) play() []app.Turn {
	h.t.Helper()
	played := h.turns.take()
	for i := range played {
		t := &played[i]
		s := h.scripts[t.Agent]
		if s == nil {
			s = endTurn
		}
		class := s(h, t)
		if class == item.TurnEnded {
			report, err := h.boards.Report(h.t.Context(), t.Run, t.Round)
			require.NoError(h.t, err)
			require.Contains(h.t, report.Facts.Ended, t.Agent, "a turn reported ended has no end_turn on the board")
		}
		if class != "" {
			h.c.Exited(h.t.Context(), app.Exit{Run: t.Run, Agent: t.Agent, Round: t.Round, Turn: t.ID, Class: class})
		}
	}
	return played
}

// round plays the turns started, then lets the round's end be decided.
func (h *harness) round() []app.Turn {
	h.t.Helper()
	played := h.play()
	h.clock.Advance(h.opts.Grace)
	return played
}

// call makes a tool call with a turn's MCP token.
func (h *harness) call(t *app.Turn, name, args string) *actionsapp.Response {
	h.t.Helper()
	r, err := h.gw.Call(h.t.Context(), t.MCP.Reveal(), name, json.RawMessage(args))
	require.NoError(h.t, err)
	return r
}

// state folds an item's records.
func (h *harness) state(id item.ID) *item.State {
	h.t.Helper()
	records, err := h.records.Load(h.t.Context(), id)
	require.NoError(h.t, err)
	s, err := item.Fold(records)
	require.NoError(h.t, err)
	return s
}

// kinds lists an item's records by kind.
func (h *harness) kinds(id item.ID) []item.Kind {
	h.t.Helper()
	records, err := h.records.Load(h.t.Context(), id)
	require.NoError(h.t, err)
	out := make([]item.Kind, 0, len(records))
	for i := range records {
		out = append(out, records[i].Body.Kind())
	}
	return out
}

// events returns a run's board.
func (h *harness) events(run board.RunID) []board.Event {
	h.t.Helper()
	events, err := h.store.Load(h.t.Context(), run)
	require.NoError(h.t, err)
	return events
}
