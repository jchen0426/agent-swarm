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

// fireEarly fires, without moving the clock, each timer armed so far that
// comes due within d of the clock's time, in order, outside its lock, and
// returns how many it fired: real timers fire so after a correction of the
// clock back by d since their arming, as each waits, in monotonic time, the
// span from the clock's time at its arming to its own. A timer armed as they
// fire waits for its own time.
func (c *clock) fireEarly(d time.Duration) int {
	c.mu.Lock()
	armed := c.n
	c.mu.Unlock()
	fired := 0
	for {
		c.mu.Lock()
		sort.SliceStable(c.timers, func(i, j int) bool {
			return c.timers[i].at.Before(c.timers[j].at) || c.timers[i].at.Equal(c.timers[j].at) && c.timers[i].n < c.timers[j].n
		})
		var due *timer
		for i, tm := range c.timers {
			if !tm.cancelled && tm.n <= armed && !tm.at.After(c.now.Add(d)) {
				due = tm
				c.timers = slices.Delete(c.timers, i, i+1)
				break
			}
		}
		c.mu.Unlock()
		if due == nil {
			return fired
		}
		fired++
		due.fn()
	}
}

// stepBack steps the clock back by d, as a correction of the database's
// clock does: the records written from then on are stamped d earlier, and no
// timer fires.
func (c *clock) stepBack(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(-d)
}

// armed returns, in order, the times of the timers armed and not cancelled.
func (c *clock) armed() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []time.Time
	for _, tm := range c.timers {
		if !tm.cancelled {
			out = append(out, tm.at)
		}
	}
	slices.SortFunc(out, time.Time.Compare)
	return out
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

// made counts the timers armed so far, cancelled or not.
func (c *clock) made() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// spend is the LLM gateway's records, as the round's ends read them: the
// model calls in flight are counted by round, as model_calls records each
// call's round. spentErr, when set, fails each read of a run's spend;
// dayFail, when set, returns the error for the nth read of the day's spend
// since it was set, counting from 1, or nil (see dayFails).
type spend struct {
	mu       sync.Mutex
	spent    map[string]int64
	spentErr error
	day      int64
	dayFail  func(n int) error
	dayReads int
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
	if s.spentErr != nil {
		return 0, s.spentErr
	}
	return s.spent[run], nil
}

func (s *spend) SpentToday(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dayReads++
	if s.dayFail != nil {
		if err := s.dayFail(s.dayReads); err != nil {
			return 0, err
		}
	}
	return s.day, nil
}

// dayFails has the reads of the day's spend from now on fail as fail says:
// with the error it returns for the nth of them, counting from 1, or not at
// all for nil.
func (s *spend) dayFails(fail func(n int) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dayFail, s.dayReads = fail, 0
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
// plays it. fail, when set, says why the runner cannot start a turn. pending
// has Start leave each launch pending, as the Docker runner does: the turn
// is not played until the test reports its launch (see launch).
type turns struct {
	mu      sync.Mutex
	queued  []app.Turn
	started []app.Turn
	stopped []string
	all     int
	fail    func(t *app.Turn) error
	pending bool
}

func (s *turns) Start(_ context.Context, t *app.Turn) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		if err := s.fail(t); err != nil {
			return false, err
		}
	}
	s.started = append(s.started, *t)
	if s.pending {
		return true, nil
	}
	s.queued = append(s.queued, *t)
	return false, nil
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

// upstreams is the UpstreamSessions port, the MCP gateway's pool as the
// coordinator sees it: it notes each run whose upstream sessions it was asked
// to close, in order.
type upstreams struct {
	mu     sync.Mutex
	closed []board.RunID
}

func (u *upstreams) CloseRun(run board.RunID) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = append(u.closed, run)
}

// closedRuns returns the runs whose upstream sessions the coordinator closed,
// in order, once per call.
func (u *upstreams) closedRuns() []board.RunID {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.closed)
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
	// controls is the kill switch's store, the database's.
	controls *controls
	// moves notes every Sessions.Move an instance makes, across takeovers.
	moves *moves

	c         *app.Coordinator
	registry  *sessions.Registry
	gw        *actionsapp.Gateway
	turns     *turns
	upstreams *upstreams
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
// as the guarded store checks it. load, when set, sees the records each load
// of an item reads, and fails the load with the error it returns. refuse,
// when set, sees the records each append of an item would commit, and refuses
// the append with the error it returns, committing nothing. unstaffed, when
// set, fails each read of the items a kill switch's on-row left not staffed
// with the error it returns for the row.
type failing struct {
	*memory.Records
	writes    epochs
	mu        sync.Mutex
	n         int
	fail      func(n int) error
	load      func(records []item.Record) error
	refuse    func(id item.ID, drafts []item.Draft) error
	unstaffed func(seq int64) error
	// lose, when set, returns the error the nth write answers once it
	// committed, as a database whose answer was lost (see losesAnswers).
	lose func(n int) error
	// rows are the inbound rows committed, in order (see inbound).
	rows []app.InboundEvent
	// hold, when set, makes an append wait until it is closed or its
	// context ends, as a store that hangs; waiting counts those that wait.
	hold    chan struct{}
	waiting atomic.Int32
}

func (f *failing) Load(ctx context.Context, id item.ID) ([]item.Record, error) {
	records, err := f.Records.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	load := f.load
	f.mu.Unlock()
	if load != nil {
		if err := load(records); err != nil {
			return nil, err
		}
	}
	return records, nil
}

func (f *failing) loads(load func(records []item.Record) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load = load
}

// UnstaffedBy returns the items the kill switch's row seq left not staffed,
// as the records store finds them, unless unstaffed fails the read.
func (f *failing) UnstaffedBy(ctx context.Context, seq int64) ([]item.ID, error) {
	f.mu.Lock()
	unstaffed := f.unstaffed
	f.mu.Unlock()
	if unstaffed != nil {
		if err := unstaffed(seq); err != nil {
			return nil, err
		}
	}
	return f.Records.UnstaffedBy(ctx, seq)
}

// unstaffedFails has the reads of the items a kill switch's on-row left not
// staffed from now on fail with the error fail returns for the row, or none
// for nil.
func (f *failing) unstaffedFails(fail func(seq int64) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unstaffed = fail
}

func (f *failing) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	return f.write(ctx, id, func(history []item.Record) ([]item.Draft, *app.InboundEvent, error) {
		drafts, err := decide(history)
		return drafts, nil, err
	})
}

// AppendInbound is Append whose decide also returns the event's row: its
// faults, its hold, its refusals and its lost answers are Append's.
func (f *failing) AppendInbound(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, *app.InboundEvent, error)) ([]item.Record, error) {
	return f.write(ctx, id, decide)
}

// Put writes a row that touches no item: its faults and its lost answers are
// an append's, counted among them.
func (f *failing) Put(ctx context.Context, e *app.InboundEvent) error {
	n, err := f.before(ctx)
	if err != nil {
		return err
	}
	if err := f.Records.Put(ctx, e); err != nil {
		return err
	}
	f.committed(e, "")
	return f.lost(n)
}

// before notes a write's epoch, counts it, and fails it as fail says, or
// holds it as hang does; it returns the write's number.
func (f *failing) before(ctx context.Context) (int, error) {
	f.writes.note(ctx)
	f.mu.Lock()
	f.n++
	n, fail, hold := f.n, f.fail, f.hold
	f.mu.Unlock()
	if fail != nil {
		if err := fail(n); err != nil {
			return n, err
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
	return n, ctx.Err()
}

func (f *failing) write(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, *app.InboundEvent, error)) ([]item.Record, error) {
	n, err := f.before(ctx)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	refuse := f.refuse
	f.mu.Unlock()
	var row *app.InboundEvent
	committed, err := f.Records.AppendInbound(ctx, id, func(history []item.Record) ([]item.Draft, *app.InboundEvent, error) {
		drafts, r, err := decide(history)
		if err != nil {
			return nil, nil, err
		}
		if refuse != nil {
			if err := refuse(id, drafts); err != nil {
				return nil, nil, err
			}
		}
		row = r
		return drafts, r, nil
	})
	if err != nil {
		return nil, err
	}
	if row != nil {
		f.committed(row, id)
	}
	if err := f.lost(n); err != nil {
		return nil, err
	}
	return committed, nil
}

// committed notes a row the store committed, with its item.
func (f *failing) committed(e *app.InboundEvent, id item.ID) {
	row := *e
	row.Item = id
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, row)
}

// lost is the error the nth write answers once it committed, as a database
// whose answer was lost: lose's, or none.
func (f *failing) lost(n int) error {
	f.mu.Lock()
	lose := f.lose
	f.mu.Unlock()
	if lose == nil {
		return nil
	}
	return lose(n)
}

// losesAnswers has the writes from now on answer the error lose returns for
// the nth, counting from 1, once they committed, or none for nil.
func (f *failing) losesAnswers(lose func(n int) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lose = lose
}

// inbound returns the inbound rows the store committed, in order, each with
// its item.
func (f *failing) inbound() []app.InboundEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.rows)
}

// refuses has the appends from now on refuse the records refuse names an
// error for, as it sees them before their commit, or none for nil.
func (f *failing) refuses(refuse func(id item.ID, drafts []item.Draft) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse = refuse
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
// does, and notes the epoch of each append as failing does. load, when set,
// sees the events each load reads, and fails the load with the error it
// returns. refuse, when set, sees the entries each append of a run would
// commit, and refuses the append with the error it returns, committing
// nothing.
type failingStore struct {
	*memory.Store
	writes epochs
	mu     sync.Mutex
	n      int
	fail   func(n int) error
	load   func(events []board.Event) error
	refuse func(run board.RunID, drafts []board.Draft) error
}

func (f *failingStore) Load(ctx context.Context, run board.RunID) ([]board.Event, error) {
	events, err := f.Store.Load(ctx, run)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	load := f.load
	f.mu.Unlock()
	if load != nil {
		if err := load(events); err != nil {
			return nil, err
		}
	}
	return events, nil
}

func (f *failingStore) loads(load func(events []board.Event) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.load = load
}

func (f *failingStore) Append(ctx context.Context, run board.RunID, decide func([]board.Event) ([]board.Draft, error)) ([]board.Event, error) {
	f.writes.note(ctx)
	f.mu.Lock()
	f.n++
	n, fail, refuse := f.n, f.fail, f.refuse
	f.mu.Unlock()
	if fail != nil {
		if err := fail(n); err != nil {
			return nil, err
		}
	}
	if refuse != nil {
		return f.Store.Append(ctx, run, func(history []board.Event) ([]board.Draft, error) {
			drafts, err := decide(history)
			if err != nil {
				return nil, err
			}
			if err := refuse(run, drafts); err != nil {
				return nil, err
			}
			return drafts, nil
		})
	}
	return f.Store.Append(ctx, run, decide)
}

func (f *failingStore) set(fail func(n int) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = fail
}

// refuses has the appends from now on refuse the entries refuse names an
// error for, as it sees them before their commit, or none for nil.
func (f *failingStore) refuses(refuse func(run board.RunID, drafts []board.Draft) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuse = refuse
}

// controls is the kill switch's store, memory.Controls, behind a guard that
// checks the epoch each write's context carries against the lease's, before
// it writes, as the Postgres store's guard checks it in the write's own
// transaction: a write without an epoch fails wrapping holding.ErrNoEpoch,
// and one under an epoch that does not hold the lease wrapping
// holding.ErrLeaseLost, and neither writes a row. It notes the epoch of each
// write, as the guarded stores' fakes do, and, as a database does, refuses a
// write or a read whose context ended. lease is the epoch that holds the
// lease: the harness's instance's, which each boot sets; a test that stores
// another has the guard refuse every write. writing, when set, runs as each
// write begins, with the write's context, before the guard; committed, when
// set, runs once a write appended its row, before the write returns; reading,
// when set, runs as each read begins. The fake fails as a test sets it (see
// loseAnswers, failWrites and failReads), and counts the reads of the switch
// made through it (see readsMade).
type controls struct {
	*memory.Controls
	writes    epochs
	lease     atomic.Int64
	mu        sync.Mutex
	writing   func(ctx context.Context)
	committed func()
	reading   func()
	// answersLost has each write run, then fail with errAnswerLost;
	// writesFail has each write fail with errBlinked before it writes
	// anything; readsFail has each read fail with errBlinked. reads counts
	// the reads.
	answersLost, writesFail, readsFail bool
	reads                              int
}

func (c *controls) SetKillSwitch(ctx context.Context, on bool, by string, reason board.Reason, after int64) (state app.KillSwitch, changed bool, err error) {
	c.writes.note(ctx)
	c.mu.Lock()
	writing := c.writing
	c.mu.Unlock()
	if writing != nil {
		writing(ctx)
	}
	epoch, err := holding.EpochFrom(ctx)
	switch {
	case err != nil:
		return app.KillSwitch{}, false, fmt.Errorf("check the lease: %w", err)
	case epoch != c.lease.Load():
		return app.KillSwitch{}, false, fmt.Errorf("check the lease: epoch %d does not hold it: %w", epoch, holding.ErrLeaseLost)
	case ctx.Err() != nil:
		return app.KillSwitch{}, false, ctx.Err()
	}
	c.mu.Lock()
	committed, lose, fail := c.committed, c.answersLost, c.writesFail
	c.mu.Unlock()
	if fail {
		return app.KillSwitch{}, false, errBlinked
	}
	state, changed, err = c.Controls.SetKillSwitch(ctx, on, by, reason, after)
	if changed && committed != nil {
		committed()
	}
	if lose {
		return app.KillSwitch{}, false, errAnswerLost
	}
	return state, changed, err
}

// KillSwitch reads the kill switch's state, as the coordinator reads it,
// unless the reads fail (see failReads), or its context ended. Each read is
// counted, and runs reading first, if set.
func (c *controls) KillSwitch(ctx context.Context) (app.KillSwitch, error) {
	c.mu.Lock()
	c.reads++
	fail, reading := c.readsFail, c.reading
	c.mu.Unlock()
	if reading != nil {
		reading()
	}
	switch {
	case fail:
		return app.KillSwitch{}, errBlinked
	case ctx.Err() != nil:
		return app.KillSwitch{}, ctx.Err()
	}
	return c.Controls.KillSwitch(ctx)
}

// onWrite has each write from now on run f as it begins, with the write's
// context, or none for nil.
func (c *controls) onWrite(f func(ctx context.Context)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writing = f
}

// onCommit has each write that appends a row from now on run f once its row
// is in, or none for nil.
func (c *controls) onCommit(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.committed = f
}

// onRead has each read of the switch from now on run f as it begins, or
// none for nil.
func (c *controls) onRead(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reading = f
}

// loseAnswers has each write from now on do what it does, its row committed
// if it appends one, then fail with errAnswerLost, as when the database's
// answer is lost on its way back; false has the writes answer again.
func (c *controls) loseAnswers(lose bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.answersLost = lose
}

// failWrites has each write from now on fail with errBlinked before it
// writes anything, as when the database cannot be reached; false has the
// writes write again.
func (c *controls) failWrites(fail bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writesFail = fail
}

// failReads has each read of the switch from now on fail with errBlinked;
// false has the reads read again.
func (c *controls) failReads(fail bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readsFail = fail
}

// readsMade counts the reads of the switch made through the fake, failed
// ones included.
func (c *controls) readsMade() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

// rows returns the number of the kill switch's latest row.
func (c *controls) rows(t *testing.T) int64 {
	t.Helper()
	seq, err := c.LatestKillSwitch(t.Context())
	require.NoError(t, err)
	return seq
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
	h := &harness{t: t, clock: newClock(), spend: newSpend(), revs: &revisions{rev: rev, all: map[string]*config.Revision{commit: rev}}, cc: cc, ids: &ids{}, sweeper: &sweeper{}, scripts: map[string]script{}, moves: &moves{}}
	h.timers = h.clock
	h.tools = newToolCalls(h.clock.Now)
	h.records = &failing{Records: memory.NewRecords(h.clock.Now)}
	h.store = &failingStore{Store: memory.NewStore(h.clock.Now)}
	h.controls = &controls{Controls: memory.NewControls(h.clock.Now)}
	h.audit = actionsmemory.NewAudit()
	h.boards = app.NewBoards(h.store, h.audit, h.records, h.ids)
	h.opts = app.Options{
		Holder: holder, DailyCap: 1_000_000, Grace: 5 * time.Second, SettleWait: 30 * time.Second, SettlePoll: time.Second, Retry: 5 * time.Second,
		ReopenWindow: 24 * time.Hour, ClosedRulings: 72 * time.Hour, Limit: 3, WriteTimeout: 10 * time.Second,
	}
	for _, f := range change {
		f(&h.opts)
	}
	// Every write of the coordinator, and of the gateway for the sessions
	// it issued, acts for a holding: the guarded stores refuse one that
	// carries no epoch.
	t.Cleanup(func() {
		for _, epoch := range slices.Concat(h.records.writes.noted(), h.store.writes.noted(), h.controls.writes.noted()) {
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

// boot starts an instance: a coordinator, its sessions, its MCP gateway, its
// upstream sessions and its turns, which takes the lease over.
func (h *harness) boot() error {
	h.t.Helper()
	h.registry, h.turns, h.upstreams = sessions.New(), &turns{}, &upstreams{}
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
	// The instance's holding is the lease's from its take on.
	h.controls.lease.Store(h.epoch)
	return h.c.TakeOver(h.t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.expired, PreviousHolder: h.previous})
}

func (h *harness) ports() *app.Ports {
	return &app.Ports{
		Records: h.records, Boards: h.boards, Spend: h.spend, ToolCalls: h.tools, Sweeper: h.sweeper, Revisions: h.revs, Sessions: movesNoted{Sessions: h.registry, moves: h.moves},
		Turns: h.turns, UpstreamSessions: h.upstreams, Controls: h.controls, Clock: h.clock, Timers: h.timers, IDs: h.ids,
		Incidents: h.records, Inbound: h.records,
	}
}

// move is a Sessions.Move an instance made: the session's run, agent and
// round, its deadline, and the token budget it was moved with.
type move struct {
	run      board.RunID
	agent    string
	round    int
	deadline time.Time
	budget   int64
}

// moves notes the moves of the instances' sessions, in order.
type moves struct {
	mu   sync.Mutex
	made []move
}

// take returns the moves noted since the last take, and forgets them.
func (m *moves) take() []move {
	m.mu.Lock()
	defer m.mu.Unlock()
	made := m.made
	m.made = nil
	return made
}

// movesNoted is an instance's live sessions, which note each move.
type movesNoted struct {
	app.Sessions
	moves *moves
}

func (s movesNoted) Move(run board.RunID, agent string, round int, deadline time.Time, budget int64) error {
	s.moves.mu.Lock()
	s.moves.made = append(s.moves.made, move{run: run, agent: agent, round: round, deadline: deadline, budget: budget})
	s.moves.mu.Unlock()
	return s.Sessions.Move(run, agent, round, deadline, budget)
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
	id, err := h.c.Open(h.t.Context(), &app.OpenInput{Actor: administrator, Type: "incident", Title: title, Source: "person", Routing: map[string]string{"service": "checkout"}})
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

// launch reports how the launch of a turn that Start left pending ended, as
// the runner does: with no error, the turn runs, and play plays it; with one,
// the launch failed, for reason.
func (h *harness) launch(turn *app.Turn, reason app.LaunchReason, err error) {
	h.t.Helper()
	if err == nil {
		h.turns.mu.Lock()
		h.turns.queued = append(h.turns.queued, *turn)
		h.turns.mu.Unlock()
	}
	h.c.Launched(h.t.Context(), launchOf(turn, reason, err))
}

// launchOf is how the pending launch of a turn ended, as the runner reports
// it.
func launchOf(turn *app.Turn, reason app.LaunchReason, err error) app.Launch {
	return app.Launch{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID, Reason: reason, Err: err}
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
