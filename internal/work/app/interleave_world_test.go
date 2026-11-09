package app_test

import (
	"cmp"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbclock "github.com/jchen0426/agent-swarm/internal/clock"
	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
)

// mode is how a world runs its schedule (The interleaving test): sequential,
// the default, on the driver's goroutine, whose seed replays its order of port
// events (R35); or concurrent, in batches of events that run at once, each on
// a goroutine, with every timer firing on a goroutine of its own, so that
// -race, and a deadlock's real-time bound, see the leaders' and the
// coordinators' locks under real contention. A concurrent seed replays its
// choice of events and faults, as far as the state each batch starts from
// lets it, not their timing.
//
// The concurrent mode has three limits. -race cannot see a race whose two
// accesses have a port call between them, since every port takes w.mu, whose
// lock and unlock order the two. Invariant 9 compares few pairs of an item
// admitted and one still waiting, as the items still waiting are those that
// waited before the batch: the sequential mode and the soak carry the queue's
// order. And committedRecords reads the table's latest row after the commit,
// not with it, so that a row the driver writes out of band in between can
// only make invariant 8's pause more lenient, never stricter.
type mode int

const (
	sequential mode = iota
	concurrent
)

// The world's settings: the lease's, as the leader's tests hold them; the
// coordinator's, as the harness's, with a limit of two items investigating,
// so that the queue forms soon, and a reopen window of ten minutes, so that a
// schedule meets its end; and the bound a schedule's settling waits for.
const (
	worldTTL        = 15 * time.Second
	worldMargin     = 5 * time.Second
	worldRenewEvery = 5 * time.Second
	worldTakeEvery  = 5 * time.Second
	worldTakeWait   = 5 * time.Second
	worldRetry      = 5 * time.Second
	worldLimit      = 2
	worldReopen     = 10 * time.Minute
	// worldClosedRulings is the window of late rulings and links on a closed
	// item, as long as the reopen window, so that a schedule meets its end.
	worldClosedRulings = 10 * time.Minute
	// worldDailyCap is the deployment's daily cap, which the day's spend
	// reaches when the driver says so.
	worldDailyCap = 1_000_000
	// worldAutoClose is the auto-close delay of the world's work item type, in
	// every revision of the registry's (R46).
	worldAutoClose = time.Minute
	// worldWrite bounds the write of the drain's on in real time, which the
	// fakes never reach: the run tests no bound measured in real time (R35),
	// and a write the driver holds must not time out while it does.
	worldWrite = time.Hour
	// settleBound is how much virtual time a settling stage waits, at most,
	// for a leader, or for the coordinators' timers to end: a bound of the
	// test's.
	settleBound = 2 * time.Hour
	// maxFires bounds the timers one event fires, so that timers that arm
	// one another at once without end fail the schedule rather than hang it.
	maxFires = 20_000
)

// worldStart is when every world's clocks start: a time fixed in code, so that
// the seed decides everything.
var worldStart = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

// worldHolders are the names of the instances' two hosts, as the lease records
// them: the instances a host boots keep its name.
var worldHolders = [2]string{"instance-a", "instance-b"}

// The world's stores, as its log and its writes name them.
const (
	storeRecords = "records"
	storeEvents  = "events"
	storeSwitch  = "switch"
)

// worldAgents is how many agents the world's revision subscribes to the
// incident type, each of which its items invite.
const worldAgents = 3

// The registry's revisions, which the driver makes current in turn: the base
// one; one with a rule that applies always and matches the routing value
// payments, so that an item opened with it before is not staffed at its start
// for its routing; and one whose incident type reads its key's values from a
// tool, which the coordinator cannot run, so that an item opened before is
// not staffed at its start for its type (Decision 4). Each keeps the type's
// auto-close delay.
const (
	revBase = iota
	revRedactsPayments
	revReadsItsKey
	revCount
)

var revNames = [revCount]string{
	revBase: "the base revision", revRedactsPayments: "a revision whose rule redacts payments", revReadsItsKey: "a revision whose type reads its key from a tool",
}

// worldRevisionsBuilt builds the registry's revisions once, as no world
// changes them: three agents subscribed to the incident type, whose items
// close by themselves 1 minute after they await their close (R46). Three, so
// that a map of a round's turns that its coordinator iterates in its order
// rather than sorted reaches a port in another order often enough for
// TestASeedReplaysItsSchedule to see it.
var worldRevisionsBuilt = sync.OnceValues(func() ([revCount]*config.Revision, error) {
	var revs [revCount]*config.Revision
	cc, err := celeval.New()
	if err != nil {
		return revs, err
	}
	searchHash, err := config.ToolSchemaHash(&search)
	if err != nil {
		return revs, err
	}
	incident := `kind: WorkItemType
metadata: {name: incident, owner: sre-leads}
spec:
  hypothesisKey:
    - {name: service, values: [checkout, payments]}
    - {name: failureMode, values: [bad-release, config-change]}
  budget: {wallClock: 10m, tokens: 90000, rounds: 3}
  round: {timeLimit: 2m}
  convergence: {minContributors: 2, minSources: 1, refutationMargin: 2}
  autoClose: {after: 1m}
`
	for i, c := range [revCount]string{commit, strings.Repeat("b", 40), strings.Repeat("c", 40)} {
		files := map[string][]byte{
			"work-item-types/incident.yaml": []byte(incident),
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
			"agents/capacity-investigator.yaml": agent("capacity-investigator", "incident"),
			"redaction-rules/api-keys.yaml": []byte(`kind: RedactionRule
metadata: {name: api-keys, owner: security@example.com}
spec:
  always: true
  patterns: ['sk-[a-z0-9]{8}']
`),
		}
		switch i {
		case revRedactsPayments:
			files["redaction-rules/services.yaml"] = []byte(`kind: RedactionRule
metadata: {name: services, owner: security@example.com}
spec:
  always: true
  patterns: ['^payments$']
`)
		case revReadsItsKey:
			files["work-item-types/incident.yaml"] = []byte(strings.Replace(incident,
				"{name: service, values: [checkout, payments]}", `{name: service, values: {tool: catalog.list_services, field: "$[*].name"}}`, 1))
			files["servers/catalog.yaml"] = []byte(`kind: McpServer
metadata: {name: catalog, owner: platform}
spec:
  endpoint: https://catalog.internal/mcp
  image: "sha256:` + strings.Repeat("c", 64) + `"
  tools:
    list_services: {effect: read, schemaHash: "sha256:` + strings.Repeat("b", 64) + `"}
`)
		}
		fsys, err := memfs.New(files)
		if err != nil {
			return revs, err
		}
		if revs[i], err = config.Build(fsys, c, cc); err != nil {
			return revs, fmt.Errorf("%s: %w", revNames[i], err)
		}
	}
	return revs, nil
})

// world is the interleaving test's world (The interleaving test): two
// instances, a and b, each the real leader over the real coordinator, as serve
// wires them, over one set of fakes that own every timer and port and inject
// the faults the driver arms. Its three kinds of clock are one monotonic
// clock, which only goes forward and owns every timer; the database's clock,
// which stamps every record, event and kill switch row, judges the lease, and
// answers the leaders' samples; and each host's wall clock. The database's
// and the wall clocks are the monotonic clock's time plus an offset of their
// own, which a step moves, and time passing moves them all.
type world struct {
	t    testing.TB
	seed int64
	mode mode
	rng  *rand.Rand
	revs [revCount]*config.Revision

	// wg counts, in the concurrent mode, the goroutines of the batch under
	// way: its events, the timers they fire, and the takes they start.
	wg sync.WaitGroup

	// The stores are the database's: the memory stores, behind the guards
	// the instances' ports put in front of them.
	recordStore *memory.Records
	eventStore  *memory.Store
	switchStore *memory.Controls
	readCalls   *memory.Calls

	// leaseMu is the lease's row lock: a guarded write holds it shared from
	// its check to its commit, as the Postgres guard's FOR KEY SHARE does, and
	// a take, a renewal or a release holds it alone.
	leaseMu sync.RWMutex

	// mu guards the rest: the clocks, the timers, the lease's row, the faults,
	// the instances' slots and their process state, the ids, the log, and
	// what the world notes. No call into a coordinator or a leader is made
	// while it is held.
	mu         sync.Mutex
	mono       time.Time
	dbOffset   time.Duration
	wallOffset [2]time.Duration
	timers     timerQueue
	armedN     int
	lease      leaseRow
	armed      [faultCount]bool
	// pairing is how a write fault spent arms its store's read fault on the
	// instance whose write it failed (R49): half the time, by the schedule's
	// source, or always, or never, as a hand-written schedule needs.
	pairing pairing
	insts   [2]*instance
	boots   [2]int
	ids     int
	// pending says the runner leaves each launch pending, and reports it, as
	// the Docker runner does; otherwise a turn runs once started, as under
	// the process runner. The seed picks it.
	pending bool
	// current is the registry's current revision, of revs; capped says the
	// day's spend reached the daily cap; lossSkip is how many guarded writes
	// for the lease's holding the lease's loss mid-act, once armed, lets pass
	// before it lands, so that it lands between two writes of one act too.
	current  int
	capped   bool
	lossSkip int
	// takeHold is a take of the lease to hold once it commits, and
	// takeHeld one so held, which the event that lost the lease mid-act lets
	// go once it is done (see loseMidAct).
	takeHold, takeHeld *heldTake
	entries            []logEntry
	committed          []write
	rows               []ksRow
	itemIDs            []item.ID
	itemSeen           map[item.ID]bool
	// dirty holds the items whose records changed since the last check of
	// invariant 5, which folds them again; checkedWrites counts the writes it
	// checked.
	dirty         map[item.ID]bool
	checkedWrites int

	driver
	observer
}

// instance is an instance of the platform on one of the world's two hosts,
// as serve wires it: its holding, on the monotonic clock and its host's wall
// clock; its coordinator's clock.DB, over a sampler of the database's clock
// and that wall clock, which its leader samples; the real coordinator and
// leader over the world's fakes; and what the instance keeps in its process,
// which a crash loses: its live sessions and its runner's turns.
type instance struct {
	w       *world
	slot    int
	name    string
	hold    *app.Holding
	db      *dbclock.DB
	lease   *instLease
	coord   *app.Coordinator
	leader  *app.Leader
	gateway *app.Boards

	// Under w.mu.
	live  map[sessionKey]*liveSession
	turns []*runnerTurn
	// reads are the read faults armed on the instance, which its next read
	// of their store spends (see failRead).
	reads [faultCount]bool
	// holdAt is the write of the drain's on that the driver holds at the
	// kill switch's store, once it reaches it.
	holdAt *heldWrite
	gone   bool
}

// portCall is a call an instance's coordinator, leader or gateway made to one
// of the world's ports: the instance, the port and the method, what it names,
// the epoch its context carried, if any, and the monotonic clock's time.
type portCall struct {
	Inst   string
	Port   string
	Method string
	Run    board.RunID
	Item   item.ID
	Agent  string
	Note   string
	Epoch  int64
	At     time.Time
	// pos is the call's position in the world's log (see closedAt).
	pos int
}

// write is a write a guarded store committed: its store, the epoch it acted
// for, and the lease's epoch at its commit.
type write struct {
	Store string
	Epoch int64
	Lease int64
}

// ksRow is a row of the kill switch's table: its number, on or off, its time
// in the database's clock, and whether the driver wrote it out of band.
type ksRow struct {
	Seq       int64
	On        bool
	At        time.Time
	OutOfBand bool
}

// logEntry is a line of the world's log: an event, a port call, or a note,
// such as an act's answer or a timer that fires.
type logEntry struct {
	event int
	text  string
	call  *portCall
	at    time.Time
}

// newWorld returns a world whose schedule seed decides, run in mode m, with
// both instances booted: a takes the lease, and b waits to take it.
func newWorld(t testing.TB, seed int64, m mode) *world {
	t.Helper()
	revs, err := worldRevisionsBuilt()
	require.NoError(t, err)
	w := &world{
		t: t, seed: seed, mode: m, rng: rand.New(rand.NewPCG(uint64(seed), 0x7b1)), revs: revs, mono: worldStart,
		itemSeen: map[item.ID]bool{}, dirty: map[item.ID]bool{}, readCalls: memory.NewCalls(), observer: newObserver(),
	}
	w.recordStore = memory.NewRecords(w.dbNow)
	w.eventStore = memory.NewStore(w.dbNow)
	w.switchStore = memory.NewControls(w.dbNow)
	w.pending = w.rng.IntN(2) == 0
	w.do("boot a and b", func() {
		w.boot(0)
		w.boot(1)
	})
	return w
}

// boot boots an instance on the host of slot, as serve does, and has its
// leader lead, under a context the world never ends: the instance stops only
// by a stop, which the driver sends, or a crash, which discards it.
func (w *world) boot(slot int) *instance {
	w.mu.Lock()
	w.boots[slot]++
	in := &instance{w: w, slot: slot, name: fmt.Sprintf("%c%d", 'a'+slot, w.boots[slot]), live: map[sessionKey]*liveSession{}}
	w.mu.Unlock()
	wall := func() time.Time { return w.wallNow(slot) }
	in.hold = app.NewHolding(worldTTL, worldMargin, wall)
	in.db = dbclock.NewDB(samplerPort{in}, wall)
	in.lease = &instLease{in: in}
	in.gateway = app.NewBoards(eventsPort{in: in, port: "gateway"}, w.readCalls, runsPort{in}, worldIDs{w})
	coord, err := app.NewCoordinator(&app.Ports{
		Records: recordsPort{in}, Boards: app.NewBoards(eventsPort{in: in, port: "events", faulty: true}, w.readCalls, runsPort{in}, worldIDs{w}),
		Spend: spendPort{in}, ToolCalls: toolsPort{in}, Sweeper: sweeperPort{in}, Revisions: worldRevisions{w}, Sessions: sessionsPort{in},
		Turns: runnerPort{in}, UpstreamSessions: upstreamPort{in}, Controls: controlsPort{in}, Clock: in.db, Timers: coordTimers{in}, IDs: worldIDs{w},
		Incidents: incidentsPort{in},
	}, &app.Options{
		Holder: worldHolders[slot], DailyCap: worldDailyCap, Grace: 5 * time.Second, SettleWait: 30 * time.Second, SettlePoll: time.Second,
		Retry: worldRetry, ReopenWindow: worldReopen, ClosedRulings: worldClosedRulings, Limit: worldLimit, WriteTimeout: worldWrite,
	})
	require.NoError(w.t, err)
	leader, err := app.NewLeader(in.lease, coord, monoClock{w}, leaderTimers{in}, &app.LeaderOptions{
		RenewEvery: worldRenewEvery, Margin: worldMargin, TakeEvery: worldTakeEvery, TakeWait: worldTakeWait,
		Holding: in.hold, Sampler: in.db, KillSwitch: rowsPort{in},
	})
	require.NoError(w.t, err)
	in.coord, in.leader = coord, leader
	w.mu.Lock()
	w.insts[slot] = in
	w.mu.Unlock()
	// Lead fails only for a schema this build cannot run on, which the world
	// has none of.
	_ = leader.Lead(context.Background())
	return in
}

// discard discards an instance, as a crash does, or a stop once its leader
// released the lease: its timers, its live sessions and its runner's turns go
// with it, and nothing calls it again. Its holding of the lease, if any,
// lasts until it expires.
func (w *world) discard(in *instance) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, t := range w.timers {
		if t.in == in {
			t.gone = true
		}
	}
	in.gone, in.live, in.turns = true, nil, nil
	if w.insts[in.slot] == in {
		w.insts[in.slot] = nil
	}
}

// inst returns the live instance of slot, or nil.
func (w *world) inst(slot int) *instance {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.insts[slot]
}

// live returns the live instances, a's slot first.
func (w *world) live() []*instance {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []*instance
	for _, in := range w.insts {
		if in != nil {
			out = append(out, in)
		}
	}
	return out
}

// leading returns the instance whose leader holds the lease and does not
// drain, whose coordinator acts, or nil.
func (w *world) leading() *instance {
	for _, in := range w.live() {
		if _, held, draining := in.leader.Status(); held && !draining {
			return in
		}
	}
	return nil
}

// draining reports whether the instance's leader holds the lease and drains.
func (in *instance) draining() bool {
	_, held, draining := in.leader.Status()
	return held && draining
}

// stopped reports whether the instance's leader stopped for good, as when a
// drain's holding was lost.
func (in *instance) stopped() bool {
	select {
	case <-in.leader.Done():
		return true
	default:
		return false
	}
}

// mayTake reports whether the instance's leader takes the lease when it
// tries: it neither holds it nor drains, and has not stopped.
func (in *instance) mayTake() bool {
	_, held, draining := in.leader.Status()
	return !held && !draining && !in.stopped()
}

// The clocks.

// monoNow is the monotonic clock's time.
func (w *world) monoNow() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.mono
}

// dbNow is the database's time.
func (w *world) dbNow() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dbNowLocked()
}

func (w *world) dbNowLocked() time.Time { return w.mono.Add(w.dbOffset) }

// wallNow is the wall clock's time on the host of slot.
func (w *world) wallNow(slot int) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.mono.Add(w.wallOffset[slot])
}

// monoClock is the leader's clock: the monotonic clock, in whose times its
// timers are given.
type monoClock struct{ w *world }

func (c monoClock) Now() time.Time            { return c.w.monoNow() }
func (monoClock) Local(t time.Time) time.Time { return t }

// The timers.

// wtimer is a timer of the world's: when it is due on the monotonic clock, its
// number, which orders the timers due together by their arming, its function,
// the instance that armed it, whether it is its coordinator's, and whether it
// was cancelled, fired or discarded.
type wtimer struct {
	due   time.Time
	n     int
	fn    func()
	in    *instance
	coord bool
	gone  bool
}

// timerQueue orders the world's timers by when they are due, then by their
// arming.
type timerQueue []*wtimer

func (q timerQueue) Len() int { return len(q) }

func (q timerQueue) Less(i, j int) bool {
	if !q[i].due.Equal(q[j].due) {
		return q[i].due.Before(q[j].due)
	}
	return q[i].n < q[j].n
}

func (q timerQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }

func (q *timerQueue) Push(x any) { *q = append(*q, x.(*wtimer)) }

func (q *timerQueue) Pop() any {
	old := *q
	t := old[len(old)-1]
	*q = old[:len(old)-1]
	return t
}

// arm arms a timer of in's, its coordinator's or its leader's, after after on
// the monotonic clock, or at once when after is not above zero, and logs it.
// Its cancel is no port event, and is not logged (R33).
func (w *world) arm(in *instance, coord bool, after time.Duration, fn func()) func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armedN++
	t := &wtimer{due: w.mono.Add(max(after, 0)), n: w.armedN, fn: fn, in: in, coord: coord}
	heap.Push(&w.timers, t)
	port := "leader"
	if coord {
		port = "timers"
	}
	w.addCallLocked(&portCall{Inst: in.name, Port: port, Method: "At", Note: fmt.Sprintf("#%d %s", t.n, offset(t.due))})
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		t.gone = true
	}
}

// coordTimers are a coordinator's timers, as clock.Timers over its clock.DB
// are: a timer waits, in monotonic time, from the coordinator's clock at its
// arming to its time.
type coordTimers struct{ in *instance }

func (p coordTimers) At(t time.Time, fn func()) func() {
	return p.in.w.arm(p.in, true, t.Sub(p.in.db.Now()), fn)
}

// leaderTimers are a leader's timers, on the monotonic clock.
type leaderTimers struct{ in *instance }

func (p leaderTimers) At(t time.Time, fn func()) func() {
	return p.in.w.arm(p.in, false, t.Sub(p.in.w.monoNow()), fn)
}

// fireNext fires the next timer due at until or before, moving the monotonic
// clock to it, and reports whether there was one. In the sequential mode it
// fires on the driver's goroutine, and a take held because the lease was lost
// in the middle of the timer's step is let go once the step is done, as after
// an event's act (see loseMidAct). In the concurrent mode it fires, each on a
// goroutine of its own, every timer due by then, and waits until they are
// done.
func (w *world) fireNext(until time.Time) bool {
	w.mu.Lock()
	for w.timers.Len() > 0 && w.timers[0].gone {
		heap.Pop(&w.timers)
	}
	if w.timers.Len() == 0 || w.timers[0].due.After(until) {
		w.mu.Unlock()
		return false
	}
	if due := w.timers[0].due; due.After(w.mono) {
		w.mono = due
	}
	var fire []*wtimer
	for w.timers.Len() > 0 && !w.timers[0].due.After(w.mono) && (len(fire) == 0 || w.mode == concurrent) {
		t := heap.Pop(&w.timers).(*wtimer)
		if t.gone {
			continue
		}
		t.gone = true
		w.addCallLocked(&portCall{Inst: t.in.name, Port: "timer", Method: "fires", Note: fmt.Sprintf("#%d", t.n)})
		fire = append(fire, t)
	}
	w.mu.Unlock()
	if w.mode == sequential {
		fire[0].fn()
		w.releaseTake()
		return true
	}
	var fired sync.WaitGroup
	for _, t := range fire {
		fired.Go(t.fn)
	}
	fired.Wait()
	return true
}

// passTime has time pass by d: monotonic time advances, and the database's
// and the wall clocks with it, firing each timer it passes, in order. It
// reports whether it fired any.
func (w *world) passTime(d time.Duration) bool {
	until := w.monoNow().Add(d)
	fired := 0
	for ; w.fireNext(until); fired++ {
		if fired == maxFires {
			w.fail("timers fired without end: %d of them in %s", fired, d)
			return true
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.mono.Before(until) {
		w.mono = until
	}
	return fired > 0
}

// fireDue fires the timers due now, those armed by the ones it fires among
// them, as timers due run as soon as they can, and reports whether it fired
// any.
func (w *world) fireDue() bool { return w.passTime(0) }

// coordinatorArmed reports whether a live instance's coordinator has a timer
// armed.
func (w *world) coordinatorArmed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, t := range w.timers {
		if t.coord && !t.gone && !t.in.gone {
			return true
		}
	}
	return false
}

// The lease.

// leaseRow is the lease's row, as the database keeps it: the latest holding's
// epoch and holder, and when it expires in the database's time; 0 and zero
// before the first take.
type leaseRow struct {
	epoch   int64
	holder  string
	expires time.Time
}

// instLease is an instance's handle on the lease, as postgres.Lease is: it
// takes, renews and releases the lease's row for its own holding, whose epoch
// it keeps, judged by the database's clock.
type instLease struct {
	in    *instance
	epoch int64 // under w.mu
}

// heldTake is a take of the lease by in's leader that the driver holds once it
// commits, until the act during which it landed is done: committed closes
// then, release lets it go, and done closes once its take is over.
type heldTake struct {
	in        *instance
	committed chan struct{}
	release   chan struct{}
	done      chan struct{}
}

// Take takes the lease when it was never taken or its holding expired, as
// postgres.Lease.Take does: the term gives when the holding before expired,
// or was released, and its holder.
func (l *instLease) Take(ctx context.Context) (app.Term, bool, error) {
	w := l.in.w
	l.in.note(ctx, &portCall{Port: "lease", Method: "Take"})
	if err := ctx.Err(); err != nil {
		return app.Term{}, false, err
	}
	w.leaseMu.Lock()
	w.mu.Lock()
	now := w.dbNowLocked()
	if w.lease.epoch > 0 && now.Before(w.lease.expires) {
		w.mu.Unlock()
		w.leaseMu.Unlock()
		return app.Term{}, false, nil
	}
	term := app.Term{Epoch: w.lease.epoch + 1, At: now}
	if w.lease.epoch > 0 {
		term.Previous, term.PreviousHolder = w.lease.expires, w.lease.holder
	}
	w.lease = leaseRow{epoch: term.Epoch, holder: worldHolders[l.in.slot], expires: now.Add(worldTTL)}
	l.epoch = term.Epoch
	hold := w.takeHold
	if hold != nil && hold.in == l.in {
		w.takeHold = nil
	} else {
		hold = nil
	}
	w.mu.Unlock()
	w.leaseMu.Unlock()
	if hold != nil {
		close(hold.committed)
		<-hold.release
	}
	return term, true, nil
}

// Renew extends the holding by the lease's term from now, unless it expired
// or another instance took it over, as postgres.Lease.Renew does; a renewal
// the driver failed fails with a store error, and renews nothing.
func (l *instLease) Renew(ctx context.Context) error {
	w := l.in.w
	l.in.note(ctx, &portCall{Port: "lease", Method: "Renew"})
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.spend(faultRenewal) {
		return fmt.Errorf("renew the lease: %w", errBlinked)
	}
	w.leaseMu.Lock()
	defer w.leaseMu.Unlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.dbNowLocked()
	if l.epoch == 0 || w.lease.epoch != l.epoch || !now.Before(w.lease.expires) {
		l.epoch = 0
		return fmt.Errorf("renew the lease: %w", holding.ErrLeaseLost)
	}
	w.lease.expires = now.Add(worldTTL)
	return nil
}

// Release gives the holding up, which expires at once, unless it expired
// already, as postgres.Lease.Release does.
func (l *instLease) Release(ctx context.Context) error {
	w := l.in.w
	l.in.note(ctx, &portCall{Port: "lease", Method: "Release"})
	if err := ctx.Err(); err != nil {
		return err
	}
	w.leaseMu.Lock()
	defer w.leaseMu.Unlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	epoch := l.epoch
	l.epoch = 0
	if now := w.dbNowLocked(); epoch != 0 && w.lease.epoch == epoch && now.Before(w.lease.expires) {
		w.lease.expires = now
	}
	return nil
}

func (*instLease) TTL() time.Duration { return worldTTL }

// guard checks, as postgres.Lease.Check does in a write's transaction, that
// the holding whose epoch ctx carries holds the lease, unexpired on the
// database's clock, and returns that epoch. A refusal is logged as a call of
// in's to the store c names. Its caller holds the lease's row lock shared
// until its write commits. Once the violation table armed faultSkipGuard,
// the next write is not checked, and carries whatever epoch ctx carries, 0
// for none.
func (w *world) guard(ctx context.Context, in *instance, c *portCall) (epoch int64, err error) {
	epoch, err = holding.EpochFrom(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.armed[faultSkipGuard] {
		w.armed[faultSkipGuard] = false
		return epoch, nil
	}
	switch {
	case err != nil:
		err = fmt.Errorf("check the lease: %w", err)
	case epoch != w.lease.epoch || !w.dbNowLocked().Before(w.lease.expires):
		err = fmt.Errorf("check the lease: epoch %d does not hold it: %w", epoch, holding.ErrLeaseLost)
	default:
		return epoch, nil
	}
	c.Inst, c.Method, c.Epoch, c.Note = in.name, "refused", epoch, "the guard"
	w.addCallLocked(c)
	return epoch, err
}

// The faults.

// fault is a fault the driver arms, which the next write or read it applies
// to spends. The faults of the stores' reads, from faultRecordsRead to
// faultSwitchRead, are armed on an instance, and spent by its next read of
// their store (see armRead); every other is the world's, spent by the next
// write it applies to, whichever instance makes it.
type fault int

const (
	faultRecordsLost fault = iota
	faultRecordsFail
	faultEventsLost
	faultEventsFail
	faultSwitchLost
	faultSwitchFail
	faultRenewal
	faultLeaseExpires
	faultLeaseTaken
	faultRecordsRead
	faultEventsRead
	faultSwitchRead
	// faultSkipGuard has the next guarded write skip the lease's check, as no
	// store may: the violation table's, which feeds invariant 5 a write that
	// no guard checked, and never a schedule's.
	faultSkipGuard
	faultCount
)

var faultNames = [faultCount]string{
	faultRecordsLost: "a records append loses its answer", faultRecordsFail: "a records append fails",
	faultEventsLost: "a board append loses its answer", faultEventsFail: "a board append fails",
	faultSwitchLost: "a kill switch write loses its answer", faultSwitchFail: "a kill switch write fails",
	faultRenewal: "a renewal fails", faultLeaseExpires: "the lease expires mid-act", faultLeaseTaken: "the lease is taken mid-act",
	faultRecordsRead: "a records read fails", faultEventsRead: "a board read fails", faultSwitchRead: "a kill switch read fails",
	faultSkipGuard: "the next guarded write skips its guard",
}

// readFaults are the faults of the stores' reads, which are armed on an
// instance.
var readFaults = []fault{faultRecordsRead, faultEventsRead, faultSwitchRead}

// pairing is how a write fault spent arms a read fault (see pairRead).
type pairing int

const (
	pairHalf pairing = iota
	pairAlways
	pairNever
)

// storeFaults are the faults of a store's writes: one that fails before its
// commit, and one that loses its answer after it.
func storeFaults(store string) (fail, lose fault) {
	switch store {
	case storeRecords:
		return faultRecordsFail, faultRecordsLost
	case storeEvents:
		return faultEventsFail, faultEventsLost
	}
	return faultSwitchFail, faultSwitchLost
}

// readFault is the fault of a store's reads.
func readFault(store string) fault {
	switch store {
	case storeRecords:
		return faultRecordsRead
	case storeEvents:
		return faultEventsRead
	}
	return faultSwitchRead
}

// armFault arms fault f, a fault of the world's, which the next write it
// applies to spends. A read fault is armed on an instance (see armRead).
func (w *world) armFault(f fault) {
	if slices.Contains(readFaults, f) {
		panic(fmt.Sprintf("the harness: %s is armed on an instance, by armRead", faultNames[f]))
	}
	w.armLeaseLoss(f, 0)
}

// armRead arms read fault f on in: its next read of f's store fails, before
// the store's lock is taken, with an error that is neither a lost lease nor
// of a later version (see failRead).
func (w *world) armRead(in *instance, f fault) {
	w.mu.Lock()
	defer w.mu.Unlock()
	in.reads[f] = true
}

// pairRead arms the read fault of store on in, whose write to store a write
// fault just failed, lost or failed before its commit, as w.pairing says: by
// default half the time, drawn from the schedule's source, never from the
// clock (R49). It logs the read fault it arms, so that the log shows why the
// read fails.
func (w *world) pairRead(in *instance, store string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch w.pairing {
	case pairNever:
		return
	case pairHalf:
		if w.rng.IntN(2) == 0 {
			return
		}
	}
	f := readFault(store)
	in.reads[f] = true
	w.entries = append(w.entries, logEntry{text: fmt.Sprintf("paired: %s, on %s", faultNames[f], in.name), at: w.mono})
}

// failRead spends the read fault of store armed on in, if any, and reports
// whether it was armed: the read, method, of item id, if it reads one, then
// fails. The failure is logged as a call of in's to store, so that the log,
// which a seed replays, keeps it.
func (in *instance) failRead(store, method string, id item.ID) bool {
	w := in.w
	w.mu.Lock()
	defer w.mu.Unlock()
	f := readFault(store)
	if !in.reads[f] {
		return false
	}
	in.reads[f] = false
	w.addCallLocked(&portCall{Inst: in.name, Port: store, Method: "fails", Item: id, Note: method})
	return true
}

// armLeaseLoss arms fault f, the lease's loss mid-act among them, which
// lets skip guarded writes for the lease's holding pass before it lands: one
// lets the loss land between two writes of one act.
func (w *world) armLeaseLoss(f fault, skip int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armed[f] = true
	if f == faultLeaseExpires || f == faultLeaseTaken {
		w.lossSkip = skip
	}
}

// spend spends fault f, and reports whether it was armed.
func (w *world) spend(f fault) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	armed := w.armed[f]
	w.armed[f] = false
	return armed
}

// disarm disarms every fault, those armed on the instances among them, as
// the settling stages do.
func (w *world) disarm() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armed = [faultCount]bool{}
	for _, in := range w.insts {
		if in != nil {
			in.reads = [faultCount]bool{}
		}
	}
}

// beforeWrite runs what comes before a guarded write of in's coordinator to
// store reaches the guard, as the faults the driver armed have it: the write
// of the drain's on held, when the driver holds it; the lease lost in the
// middle of an act, when the write acts for the lease's holding; and the
// store's faults, of which a failure before the commit fails the write here.
// It reports whether the write's answer is lost once it commits. A store's
// fault spent arms the store's read fault on in, as the pairing has it (see
// pairRead). The gateway's writes are not faulty: the faults are the
// coordinator's.
func (w *world) beforeWrite(ctx context.Context, in *instance, store string, faulty bool) (lost bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !faulty {
		return false, nil
	}
	if store == storeSwitch {
		in.holdAtWrite()
	}
	w.loseMidAct(ctx, in)
	fail, lose := storeFaults(store)
	if w.spend(fail) {
		w.pairRead(in, store)
		return false, fmt.Errorf("write to the %s store: %w", store, errBlinked)
	}
	if w.spend(lose) {
		w.pairRead(in, store)
		return true, nil
	}
	return false, nil
}

// loseMidAct has the lease lost in the middle of an act, when armed, before
// the act's next guarded write, made for the lease's holding, once the writes
// it lets pass first have (see armLeaseLoss) (The interleaving test). Time
// passes on every clock, without a timer firing, as they wait for the act,
// until the holding expires on the database's clock: the instance's holding
// is then no longer valid, as the lease's premise has it. Armed so, the other
// instance's leader then takes the lease. In the sequential mode its take is
// held once it commits, until the act is done: the write is refused, and
// fences the instance, before the takeover acts (R35, the fourth condition).
// In the concurrent mode the take runs on a goroutine of the batch's, and the
// takeover goes on as the losing instance's act does, under real contention.
func (w *world) loseMidAct(ctx context.Context, in *instance) {
	epoch, err := holding.EpochFrom(ctx)
	if err != nil {
		return
	}
	w.mu.Lock()
	taken := w.armed[faultLeaseTaken]
	if epoch != w.lease.epoch || !taken && !w.armed[faultLeaseExpires] {
		w.mu.Unlock()
		return
	}
	if w.lossSkip > 0 {
		w.lossSkip--
		w.mu.Unlock()
		return
	}
	w.armed[faultLeaseTaken], w.armed[faultLeaseExpires] = false, false
	d := max(w.lease.expires.Sub(w.dbNowLocked()), 0)
	w.mono = w.mono.Add(d)
	w.addCallLocked(&portCall{Inst: in.name, Port: "world", Method: "expires", Note: fmt.Sprintf("time passes %s, no timer fires", d)})
	w.mu.Unlock()
	if !taken {
		return
	}
	other := w.inst(1 - in.slot)
	if other == nil || !other.mayTake() {
		return
	}
	if w.mode == concurrent {
		// The caller runs on a goroutine the batch counts, or on a timer's that
		// a quiesce waits for, before it waits for the batch again.
		w.wg.Go(func() { _ = other.leader.Lead(context.Background()) })
		return
	}
	hold := &heldTake{in: other, committed: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	w.mu.Lock()
	w.takeHold = hold
	w.mu.Unlock()
	// The other instance's leader tries a take now, as its take timer would:
	// Lead runs one take.
	go func() {
		defer close(hold.done)
		_ = other.leader.Lead(context.Background())
	}()
	select {
	case <-hold.committed:
		w.mu.Lock()
		w.takeHeld = hold
		w.mu.Unlock()
	case <-hold.done:
		w.mu.Lock()
		w.takeHold = nil
		w.mu.Unlock()
	}
}

// releaseTake lets go the take held mid-act, if any, and waits until the take,
// and the takeover after it, are over.
func (w *world) releaseTake() {
	w.mu.Lock()
	hold := w.takeHeld
	w.takeHeld = nil
	w.mu.Unlock()
	if hold != nil {
		close(hold.release)
		<-hold.done
	}
}

// heldWrite is the write of the drain's on that the driver holds at the kill
// switch's store: reached closes once the write blocks there, and release
// lets it go.
type heldWrite struct {
	reached chan struct{}
	release chan struct{}
}

// holdAtWrite blocks the write of the drain's on that the driver holds, until
// the driver lets it go.
func (in *instance) holdAtWrite() {
	w := in.w
	w.mu.Lock()
	h := in.holdAt
	in.holdAt = nil
	w.mu.Unlock()
	if h == nil {
		return
	}
	close(h.reached)
	<-h.release
}

// The stores' ports.

// recordsPort is an instance's coordinator's records, the memory records
// behind the guard. Each read spends the records' read fault armed on the
// instance (see failRead).
type recordsPort struct{ in *instance }

func (p recordsPort) Load(ctx context.Context, id item.ID) ([]item.Record, error) {
	p.in.note(ctx, &portCall{Port: "records", Method: "Load", Item: id})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.in.failRead(storeRecords, "Load", id) {
		return nil, fmt.Errorf("load item %s's records: %w", id, errBlinked)
	}
	records, err := p.in.w.recordStore.Load(ctx, id)
	if err == nil {
		p.in.w.noteUnheld(id, nil, false)
	}
	return records, err
}

func (p recordsPort) Active(ctx context.Context) ([]item.ID, error) {
	p.in.note(ctx, &portCall{Port: "records", Method: "Active"})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.in.failRead(storeRecords, "Active", "") {
		return nil, fmt.Errorf("list the active items: %w", errBlinked)
	}
	return p.in.w.recordStore.Active(ctx)
}

func (p recordsPort) UnstaffedBy(ctx context.Context, seq int64) ([]item.ID, error) {
	p.in.note(ctx, &portCall{Port: "records", Method: "UnstaffedBy", Note: fmt.Sprint(seq)})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.in.failRead(storeRecords, "UnstaffedBy", "") {
		return nil, fmt.Errorf("list the items on-row %d left unstaffed: %w", seq, errBlinked)
	}
	return p.in.w.recordStore.UnstaffedBy(ctx, seq)
}

// incidentsPort is an instance's coordinator's incidents, the memory records
// behind the guard, as recordsPort: each read spends the records' read fault
// armed on the instance (see failRead), which the two share (R102).
type incidentsPort struct{ in *instance }

func (p incidentsPort) IncidentItems(ctx context.Context, incident item.IncidentID) ([]item.ID, error) {
	p.in.note(ctx, &portCall{Port: "records", Method: "IncidentItems", Note: string(incident)})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.in.failRead(storeRecords, "IncidentItems", "") {
		return nil, fmt.Errorf("list the items of incident %s: %w", incident, errBlinked)
	}
	return p.in.w.recordStore.IncidentItems(ctx, incident)
}

// Append is the guarded append: the guard checks the lease within the
// store's lock, before decide, and the commit follows under the same lock, as
// in one transaction. A store that the violation table made refuse what decide
// returns refuses it there (see observer.refuse). Each commit is noted for the
// checks (see committedRecords), an automatic close with what invariant 8
// judges at it.
func (p recordsPort) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	return p.append(ctx, id, "Append", func(h []item.Record) ([]item.Draft, *app.InboundEvent, error) {
		drafts, err := decide(h)
		return drafts, nil, err
	})
}

// AppendInbound is Append whose decide also returns the event's row, which
// commits with the drafts; its guard, note and faults are Append's.
func (p recordsPort) AppendInbound(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, *app.InboundEvent, error)) ([]item.Record, error) {
	return p.append(ctx, id, "AppendInbound", decide)
}

// The inbound reads pass through unfaulted until the driver injects them.
func (p recordsPort) Seen(ctx context.Context, source, eventID string) (bool, error) {
	return p.in.w.recordStore.Seen(ctx, source, eventID)
}

func (p recordsPort) LastStatus(ctx context.Context, source, instanceID string) (item.TriggerStatus, time.Time, bool, error) {
	return p.in.w.recordStore.LastStatus(ctx, source, instanceID)
}

func (p recordsPort) LastItem(ctx context.Context, source, dedupKey string) (item.ID, bool, error) {
	return p.in.w.recordStore.LastItem(ctx, source, dedupKey)
}

func (p recordsPort) append(ctx context.Context, id item.ID, method string, decide func([]item.Record) ([]item.Draft, *app.InboundEvent, error)) ([]item.Record, error) {
	w := p.in.w
	p.in.note(ctx, &portCall{Port: "records", Method: method, Item: id})
	w.noteTry(ctx, p.in, id)
	lost, err := w.beforeWrite(ctx, p.in, storeRecords, true)
	if err != nil {
		return nil, err
	}
	w.leaseMu.RLock()
	defer w.leaseMu.RUnlock()
	var epoch int64
	var history []item.Record
	committed, err := w.recordStore.AppendInbound(ctx, id, func(h []item.Record) ([]item.Draft, *app.InboundEvent, error) {
		var err error
		if epoch, err = w.guard(ctx, p.in, &portCall{Port: "records", Item: id}); err != nil {
			return nil, nil, err
		}
		history = h
		drafts, row, err := decide(h)
		if err == nil && w.refuse != nil {
			err = w.refuse(id, drafts)
		}
		return drafts, row, err
	})
	if err != nil {
		return nil, err
	}
	if len(committed) > 0 {
		w.wrote(storeRecords, epoch)
		w.mu.Lock()
		for i := range committed {
			w.recordBy[committed[i].ID] = tryHolder{p.in.name, epoch}
		}
		w.mu.Unlock()
		w.committedRecords(p.in, id, history, committed)
	}
	if lost {
		w.noteLost(committed)
		w.noteUnheld(id, committed, true)
		return nil, errAnswerLost
	}
	return committed, nil
}

// runsPort is the runs an instance's boards read, from the records.
type runsPort struct{ in *instance }

func (p runsPort) BoardContext(ctx context.Context, run board.RunID) (board.Context, error) {
	p.in.note(ctx, &portCall{Port: "runs", Method: "BoardContext", Run: run})
	if err := ctx.Err(); err != nil {
		return board.Context{}, err
	}
	return p.in.w.recordStore.BoardContext(ctx, run)
}

// eventsPort is the board events' store of an instance's coordinator, whose
// writes and reads the driver's faults reach, or of its gateway, whose writes
// and reads they do not: the memory store behind the guard.
type eventsPort struct {
	in     *instance
	port   string
	faulty bool
}

func (p eventsPort) Load(ctx context.Context, run board.RunID) ([]board.Event, error) {
	p.in.note(ctx, &portCall{Port: p.port, Method: "Load", Run: run})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.faulty && p.in.failRead(storeEvents, "Load", "") {
		return nil, fmt.Errorf("load the board of run %s: %w", run, errBlinked)
	}
	return p.in.w.eventStore.Load(ctx, run)
}

func (p eventsPort) Append(ctx context.Context, run board.RunID, decide func([]board.Event) ([]board.Draft, error)) ([]board.Event, error) {
	w := p.in.w
	p.in.note(ctx, &portCall{Port: p.port, Method: "Append", Run: run})
	lost, err := w.beforeWrite(ctx, p.in, storeEvents, p.faulty)
	if err != nil {
		return nil, err
	}
	w.leaseMu.RLock()
	defer w.leaseMu.RUnlock()
	var epoch int64
	committed, err := w.eventStore.Append(ctx, run, func(history []board.Event) ([]board.Draft, error) {
		var err error
		if epoch, err = w.guard(ctx, p.in, &portCall{Port: p.port, Run: run}); err != nil {
			return nil, err
		}
		return decide(history)
	})
	if err != nil {
		return nil, err
	}
	if len(committed) > 0 {
		w.wrote(storeEvents, epoch)
	}
	if lost {
		return nil, errAnswerLost
	}
	return committed, nil
}

// controlsPort is an instance's coordinator's kill switch: the memory
// controls behind the guard, which checks the lease in the write's own step.
type controlsPort struct{ in *instance }

func (p controlsPort) KillSwitch(ctx context.Context) (app.KillSwitch, error) {
	w := p.in.w
	p.in.note(ctx, &portCall{Port: "switch", Method: "KillSwitch"})
	if err := ctx.Err(); err != nil {
		return app.KillSwitch{}, err
	}
	if p.in.failRead(storeSwitch, "KillSwitch", "") {
		return app.KillSwitch{}, fmt.Errorf("read the kill switch: %w", errBlinked)
	}
	return w.switchStore.KillSwitch(ctx)
}

func (p controlsPort) SetKillSwitch(ctx context.Context, on bool, by string, reason board.Reason, after int64) (state app.KillSwitch, changed bool, err error) {
	w := p.in.w
	p.in.note(ctx, &portCall{Port: "switch", Method: "SetKillSwitch", Note: fmt.Sprintf("on=%t after=%d", on, after)})
	if on {
		w.came(by)
	}
	lost, err := w.beforeWrite(ctx, p.in, storeSwitch, true)
	if err != nil {
		return app.KillSwitch{}, false, err
	}
	w.leaseMu.RLock()
	defer w.leaseMu.RUnlock()
	epoch, err := w.guard(ctx, p.in, &portCall{Port: "switch"})
	if err != nil {
		return app.KillSwitch{}, false, err
	}
	state, changed, err = w.switchStore.SetKillSwitch(ctx, on, by, reason, after)
	if changed {
		w.wrote(storeSwitch, epoch)
		w.row(&state, false)
	}
	if lost {
		return app.KillSwitch{}, false, errAnswerLost
	}
	return state, changed, err
}

// rowsPort is an instance's leader's read of the kill switch's latest row,
// which the driver's failed reads reach too.
type rowsPort struct{ in *instance }

func (p rowsPort) LatestKillSwitch(ctx context.Context) (int64, error) {
	w := p.in.w
	p.in.note(ctx, &portCall{Port: "rows", Method: "LatestKillSwitch"})
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if p.in.failRead(storeSwitch, "LatestKillSwitch", "") {
		return 0, fmt.Errorf("read the kill switch's latest row: %w", errBlinked)
	}
	return w.switchStore.LatestKillSwitch(ctx)
}

// samplerPort is an instance's sampler of the database's clock, which its
// clock.DB samples, as the lease's pool answers clock_timestamp().
type samplerPort struct{ in *instance }

func (p samplerPort) Sample(ctx context.Context) (time.Time, error) {
	p.in.note(ctx, &portCall{Port: "sampler", Method: "Sample"})
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	return p.in.w.dbNow(), nil
}

// sweeperPort is the LLM gateway's sweep: no model call is ever left without
// an outcome, but its write checks the lease as any guarded write does.
type sweeperPort struct{ in *instance }

func (p sweeperPort) Sweep(ctx context.Context, _ time.Time, _ string) (int, error) {
	w := p.in.w
	p.in.note(ctx, &portCall{Port: "sweeper", Method: "Sweep"})
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	w.leaseMu.RLock()
	defer w.leaseMu.RUnlock()
	_, err := w.guard(ctx, p.in, &portCall{Port: "sweeper"})
	return 0, err
}

// spendPort is the LLM gateway's records of model calls: none spends, none is
// in flight, and none fails; the day's spend is zero, or the daily cap once
// the driver says the day's calls reached it, until it says a new day began.
type spendPort struct{ in *instance }

func (p spendPort) Spent(ctx context.Context, run string) (int64, error) {
	p.in.note(ctx, &portCall{Port: "spend", Method: "Spent", Run: board.RunID(run)})
	return 0, ctx.Err()
}

func (p spendPort) SpentToday(ctx context.Context) (int64, error) {
	w := p.in.w
	p.in.note(ctx, &portCall{Port: "spend", Method: "SpentToday"})
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.capped {
		return worldDailyCap, ctx.Err()
	}
	return 0, ctx.Err()
}

func (p spendPort) InFlight(ctx context.Context, run string, _ int) (int, error) {
	p.in.note(ctx, &portCall{Port: "spend", Method: "InFlight", Run: board.RunID(run)})
	return 0, ctx.Err()
}

func (p spendPort) Failed(ctx context.Context, run string) ([]app.ModelCall, error) {
	p.in.note(ctx, &portCall{Port: "spend", Method: "Failed", Run: board.RunID(run)})
	return nil, ctx.Err()
}

// toolsPort is the MCP gateway's records of tool calls: none fails.
type toolsPort struct{ in *instance }

func (p toolsPort) Failed(ctx context.Context, run string, _ int) (int, error) {
	p.in.note(ctx, &portCall{Port: "tools", Method: "Failed", Run: board.RunID(run)})
	return 0, ctx.Err()
}

// worldRevisions is the registry: its revisions, of which the driver makes
// one current, and which deployments lock with the runtime image.
type worldRevisions struct{ w *world }

func (r worldRevisions) Current() *config.Revision {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	return r.w.revs[r.w.current]
}

func (r worldRevisions) At(_ context.Context, c string) (*config.Revision, error) {
	for _, rev := range r.w.revs {
		if c == rev.Commit() {
			return rev, nil
		}
	}
	return nil, fmt.Errorf("no revision %s", c)
}

func (worldRevisions) Lock(rev *config.Revision) (config.Lock, error) {
	return rev.Lock(map[string]string{"thinloop": image})
}

// worldIDs issues the world's ids, in sequence, so that the seed decides them.
type worldIDs struct{ w *world }

func (i worldIDs) NewID() string {
	i.w.mu.Lock()
	defer i.w.mu.Unlock()
	i.w.ids++
	return fmt.Sprintf("id-%d", i.w.ids)
}

// The process's ports: the sessions, the runner and the upstream pool.

// sessionKey names an agent's session in a run.
type sessionKey struct {
	run   board.RunID
	agent string
}

// liveSession is a live session: the epoch of the holding whose coordinator
// issued it, the round it is in, 0 before its first, and the tokens its
// latest move gave it, which an extension of tokens raises (R54).
type liveSession struct {
	epoch  int64
	round  int
	budget int64
}

// sessionsPort is an instance's live sessions, as the sessions registry
// keeps them. Each issue and each move is noted for invariant 1, under the
// epoch of the holding the session acts for; a move records its budget.
type sessionsPort struct{ in *instance }

func (p sessionsPort) Issue(s *app.Session) app.Tokens {
	w := p.in.w
	c := &portCall{Port: "sessions", Method: "Issue", Run: s.Run, Agent: s.Agent, Epoch: s.Epoch}
	p.in.note(context.Background(), c)
	w.noteWork(p.in, c, "Sessions.Issue", s.Epoch)
	w.mu.Lock()
	defer w.mu.Unlock()
	if !p.in.gone {
		p.in.live[sessionKey{s.Run, s.Agent}] = &liveSession{epoch: s.Epoch}
	}
	return app.Tokens{}
}

func (p sessionsPort) Move(run board.RunID, agent string, round int, deadline time.Time, budget int64) error {
	w := p.in.w
	c := &portCall{Port: "sessions", Method: "Move", Run: run, Agent: agent, Note: fmt.Sprintf("%d budget=%d", round, budget)}
	p.in.note(context.Background(), c)
	if round < 1 || deadline.IsZero() {
		return fmt.Errorf("sessions: a session moves into a round from 1, with a deadline, not round %d", round)
	}
	w.mu.Lock()
	s := p.in.live[sessionKey{run, agent}]
	var epoch int64
	if s != nil {
		epoch = s.epoch
	}
	w.mu.Unlock()
	if s == nil {
		return fmt.Errorf("sessions: %s has no session in run %s", agent, run)
	}
	if w.noteWork(p.in, c, "Sessions.Move", epoch) {
		w.noteMove(p.in, run, agent, round, budget)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	s.round, s.budget = round, budget
	return nil
}

func (p sessionsPort) End(run board.RunID, agent string) {
	w := p.in.w
	p.in.note(context.Background(), &portCall{Port: "sessions", Method: "End", Run: run, Agent: agent})
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(p.in.live, sessionKey{run, agent})
}

func (p sessionsPort) EndRun(run board.RunID) {
	w := p.in.w
	p.in.note(context.Background(), &portCall{Port: "sessions", Method: "EndRun", Run: run})
	w.mu.Lock()
	defer w.mu.Unlock()
	maps.DeleteFunc(p.in.live, func(k sessionKey, _ *liveSession) bool { return k.run == run })
}

func (p sessionsPort) EndAll() {
	w := p.in.w
	p.in.note(context.Background(), &portCall{Port: "sessions", Method: "EndAll"})
	w.mu.Lock()
	defer w.mu.Unlock()
	clear(p.in.live)
}

// runnerTurn is a turn an instance's runner has: the turn, the epoch its
// start acted for, whether its launch is pending, and whether a stop ended
// it, whose report is due.
type runnerTurn struct {
	turn      app.Turn
	epoch     int64
	launching bool
	stopped   bool
}

// runnerPort is an instance's runner, the Turns port: it refuses a start
// whose holding is not valid, as the runners do, and keeps each turn it
// starts, in order, until the driver reports how its launch or the turn
// ended (see launch, end and reportStop). Each start is noted for invariant
// 1, with the holding's validity the runner judged it by.
type runnerPort struct{ in *instance }

func (p runnerPort) Start(ctx context.Context, t *app.Turn) (bool, error) {
	w := p.in.w
	c := &portCall{Port: "turns", Method: "Start", Run: t.Run, Agent: t.Agent, Note: t.ID}
	p.in.note(ctx, c)
	epoch, err := holding.EpochFrom(ctx)
	if err != nil {
		return false, err
	}
	if !w.noteWork(p.in, c, "Turns.Start", epoch) {
		return false, fmt.Errorf("start the turn of %s: %w", t.Agent, app.ErrUnheld)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, rt := range p.in.turns {
		if rt.turn.ID == t.ID {
			return rt.launching, nil
		}
	}
	rt := &runnerTurn{turn: *t, epoch: epoch, launching: w.pending}
	p.in.turns = append(p.in.turns, rt)
	return rt.launching, nil
}

func (p runnerPort) Stop(run board.RunID, agent string) {
	w := p.in.w
	p.in.note(context.Background(), &portCall{Port: "turns", Method: "Stop", Run: run, Agent: agent})
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, rt := range p.in.turns {
		if rt.turn.Run == run && rt.turn.Agent == agent {
			rt.stopped = true
		}
	}
}

func (p runnerPort) StopAll() {
	w := p.in.w
	p.in.note(context.Background(), &portCall{Port: "turns", Method: "StopAll"})
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, rt := range p.in.turns {
		rt.stopped = true
	}
}

// upstreamPort is an instance's pool of upstream MCP sessions, which notes
// each run whose sessions the coordinator closed, and when, among the
// records' commits (invariant 7).
type upstreamPort struct{ in *instance }

func (p upstreamPort) CloseRun(run board.RunID) {
	w := p.in.w
	p.in.note(context.Background(), &portCall{Port: "upstream", Method: "CloseRun", Run: run})
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closedRuns[run] = w.order
}

// The runner's reports, which the driver makes from its goroutine.

// errCancelled is how a launch a stop cancelled fails.
var errCancelled = errors.New("the launch was cancelled")

// forget has the instance's runner forget rt, once it reported its end.
func (in *instance) forget(rt *runnerTurn) {
	w := in.w
	w.mu.Lock()
	defer w.mu.Unlock()
	in.turns = slices.DeleteFunc(in.turns, func(o *runnerTurn) bool { return o == rt })
}

// launch reports how the pending launch of rt ended: it runs, for no reason,
// or it failed for reason; one whose holding is no longer valid fails for the
// holding, as the runner asks the holding before the sandbox starts.
func (w *world) launch(in *instance, rt *runnerTurn, reason app.LaunchReason) {
	l := app.Launch{Run: rt.turn.Run, Agent: rt.turn.Agent, Round: rt.turn.Round, Turn: rt.turn.ID}
	switch {
	case !in.hold.Valid(rt.epoch):
		l.Reason, l.Err = app.ReasonHolding, fmt.Errorf("launch the turn of %s: %w", rt.turn.Agent, app.ErrUnheld)
	case reason != "":
		l.Reason, l.Err = reason, fmt.Errorf("launch the turn of %s: %s", rt.turn.Agent, reason)
	}
	w.mu.Lock()
	if l.Err == nil {
		rt.launching = false
	} else {
		in.turns = slices.DeleteFunc(in.turns, func(o *runnerTurn) bool { return o == rt })
	}
	w.mu.Unlock()
	w.noteResult(fmt.Sprintf("launched %s %s: %s", rt.turn.Run, rt.turn.Agent, cmp.Or(string(l.Reason), "runs")))
	in.coord.Launched(context.Background(), l)
}

// end ends rt, a running turn, as class says, with its agent's turn end on
// the board first when onBoard says so: posted as the gateway posts end_turn,
// under its session's epoch. A turn the runtime ends ended has its turn end
// on the board: one whose post failed ends unavailable (Decision 13).
func (w *world) end(in *instance, rt *runnerTurn, class item.TurnClass, onBoard bool) {
	if (onBoard || class == item.TurnEnded) && !w.endTurn(in, &rt.turn) && class == item.TurnEnded {
		class = item.TurnUnavailable
	}
	in.forget(rt)
	w.noteResult(fmt.Sprintf("exited %s %s: %s", rt.turn.Run, rt.turn.Agent, class))
	in.coord.Exited(context.Background(), app.Exit{Run: rt.turn.Run, Agent: rt.turn.Agent, Round: rt.turn.Round, Turn: rt.turn.ID, Class: class})
}

// stopReport is how the runner reports a pending launch that a stop ended:
// cancelled, as the runners mostly report it; failed, for the engine, a
// failure the launch met before the cancel reached it; or launched, the
// launch done just before the stop came, whose turn the stop then
// interrupted.
type stopReport int

const (
	reportCancelled stopReport = iota
	reportFailed
	reportRan
)

var stopReports = [...]string{reportCancelled: "cancelled", reportFailed: "failed for the engine", reportRan: "launched, then interrupted"}

// reportStop reports how rt, a turn a stop ended, ended: a pending launch as
// how says, or a turn interrupted, or ended when the board holds its turn
// end, as the runners report it.
func (w *world) reportStop(in *instance, rt *runnerTurn, how stopReport) {
	w.mu.Lock()
	launching := rt.launching
	w.mu.Unlock()
	in.forget(rt)
	if launching {
		l := app.Launch{Run: rt.turn.Run, Agent: rt.turn.Agent, Round: rt.turn.Round, Turn: rt.turn.ID}
		switch how {
		case reportCancelled:
			l.Reason, l.Err = app.ReasonCancelled, errCancelled
		case reportFailed:
			l.Reason, l.Err = app.ReasonEngine, fmt.Errorf("launch the turn of %s: %s", rt.turn.Agent, app.ReasonEngine)
		}
		w.noteResult(fmt.Sprintf("launched %s %s: %s", rt.turn.Run, rt.turn.Agent, stopReports[how]))
		in.coord.Launched(context.Background(), l)
		if how == reportRan {
			in.coord.Exited(context.Background(), app.Exit{Run: rt.turn.Run, Agent: rt.turn.Agent, Round: rt.turn.Round, Turn: rt.turn.ID, Class: item.TurnInterrupted})
		}
		return
	}
	class := item.TurnInterrupted
	report, err := in.gateway.Report(context.Background(), rt.turn.Run, rt.turn.Round)
	if err == nil && slices.Contains(report.Facts.Ended, rt.turn.Agent) {
		class = item.TurnEnded
	}
	w.noteResult(fmt.Sprintf("exited %s %s: %s", rt.turn.Run, rt.turn.Agent, class))
	in.coord.Exited(context.Background(), app.Exit{Run: rt.turn.Run, Agent: rt.turn.Agent, Round: rt.turn.Round, Turn: rt.turn.ID, Class: class})
}

// endTurn posts the agent's turn end on the board, as the gateway posts
// end_turn for the agent's live session in the turn's round, under the
// session's epoch, and reports whether it did.
func (w *world) endTurn(in *instance, t *app.Turn) bool {
	w.mu.Lock()
	s := in.live[sessionKey{t.Run, t.Agent}]
	w.mu.Unlock()
	if s == nil || s.round != t.Round {
		return false
	}
	ctx := holding.WithEpoch(context.Background(), s.epoch)
	_, err := in.gateway.EndTurn(ctx, t.Run, t.Round, board.Author{Kind: board.AuthorAgent, Name: t.Agent})
	return err == nil
}

// runnerTurns returns the turns of the live instances' runners that pass
// keep, a's first, each in its runner's order.
func (w *world) runnerTurns(keep func(rt *runnerTurn) bool) (ins []*instance, rts []*runnerTurn) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, in := range w.insts {
		if in == nil {
			continue
		}
		for _, rt := range in.turns {
			if keep(rt) {
				ins, rts = append(ins, in), append(rts, rt)
			}
		}
	}
	return ins, rts
}

// What the world notes.

// note logs a port call of in's, with the epoch ctx carries, if any.
func (in *instance) note(ctx context.Context, c *portCall) {
	if c.Epoch == 0 {
		c.Epoch, _ = holding.EpochFrom(ctx)
	}
	c.Inst = in.name
	in.w.mu.Lock()
	defer in.w.mu.Unlock()
	in.w.addCallLocked(c)
}

// addCallLocked logs a port call at the monotonic clock's time, and its
// position in the log; w.mu is held.
func (w *world) addCallLocked(c *portCall) {
	c.At, c.pos = w.mono, len(w.entries)
	w.entries = append(w.entries, logEntry{call: c, at: w.mono})
}

// noteResult logs what an event's act or report answered, or did.
func (w *world) noteResult(text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, logEntry{text: text, at: w.mono})
}

// wrote notes a write a guarded store committed, for the holding of epoch,
// with the lease's epoch as the lease's row holds it now, at the commit, read
// apart from the guard's check (invariant 5). Its caller holds the lease's row
// lock shared, as the commit did.
func (w *world) wrote(store string, epoch int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.committed = append(w.committed, write{Store: store, Epoch: epoch, Lease: w.lease.epoch})
}

// row notes a row of the kill switch's table, as state gives it.
func (w *world) row(state *app.KillSwitch, outOfBand bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rows = append(w.rows, ksRow{Seq: state.Seq, On: state.On, At: state.Since, OutOfBand: outOfBand})
}

// log returns the world's log, a line for each event, then one for each port
// call it caused and each answer, in order, with its instance and the
// monotonic clock's time from the world's start.
func (w *world) log() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.entries))
	for i := range w.entries {
		out = append(out, w.entries[i].String())
	}
	return out
}

func (e *logEntry) String() string {
	switch {
	case e.event > 0:
		return fmt.Sprintf("#%d %s %s", e.event, offset(e.at), e.text)
	case e.call == nil:
		return "      = " + e.text
	}
	c := e.call
	var b strings.Builder
	fmt.Fprintf(&b, "    %s %s.%s", c.Inst, c.Port, c.Method)
	for _, s := range []string{string(c.Item), string(c.Run), c.Agent, c.Note} {
		if s != "" {
			b.WriteString(" " + s)
		}
	}
	if c.Epoch != 0 {
		fmt.Fprintf(&b, " epoch=%d", c.Epoch)
	}
	b.WriteString(" @" + offset(c.At))
	return b.String()
}

// offset is a time of the monotonic clock's as the time since the world's
// start.
func offset(t time.Time) string { return "+" + t.Sub(worldStart).String() }

// calls returns every port call, in order.
func (w *world) calls() []portCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []portCall
	for i := range w.entries {
		if c := w.entries[i].call; c != nil {
			out = append(out, *c)
		}
	}
	return out
}

// writes returns every write the guarded stores committed, in order.
func (w *world) writes() []write {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.committed)
}

// killSwitchRows returns the kill switch's rows, in order.
func (w *world) killSwitchRows() []ksRow {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.rows)
}

// records returns, by item, the records of every item the stores hold.
func (w *world) records() map[item.ID][]item.Record {
	w.mu.Lock()
	ids := slices.Clone(w.itemIDs)
	w.mu.Unlock()
	out := make(map[item.ID][]item.Record, len(ids))
	for _, id := range ids {
		records, err := w.recordStore.Load(context.Background(), id)
		require.NoError(w.t, err)
		out[id] = records
	}
	return out
}

// lifecycles returns the leading coordinator's snapshot before the last
// event, the last one a leader published, and after it, empty when no
// coordinator leads.
func (w *world) lifecycles() (before, after app.Lifecycle) { return w.before, w.after }

// fail notes the world's first failure, which ends its schedule.
func (w *world) fail(format string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failure == nil {
		w.failure = fmt.Errorf(format, args...)
	}
}

// failed returns the world's first failure, or nil.
func (w *world) failed() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.failure
}
