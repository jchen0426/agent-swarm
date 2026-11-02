package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

var (
	// ErrInvalid marks a request the coordinator refuses, such as an item
	// of a type it cannot run.
	ErrInvalid = errors.New("app: invalid request")
	// ErrNotHeld marks a request to a coordinator whose instance does not
	// hold the lease.
	ErrNotHeld = errors.New("app: this instance does not hold the lease")
	// ErrSkipped marks a request about an item held by a later build, which
	// wrote records or board events of it that this build cannot read: only
	// such a build acts on it (Decision 19). This holding skips such an item,
	// unless it is closed (see Skipped).
	ErrSkipped = errors.New("app: the item is held by a later build")
	// ErrKeyReused marks an opening whose key opened an item for another
	// request (Decision 14), and an act on an item whose key named another
	// act of the item (Decision 13): a key names one act of its item, and the
	// same key on another item is that item's own act (R42).
	ErrKeyReused = errors.New("app: the key was used for another request")
	// ErrNoRevision marks an opening, or an item's start, while the registry
	// has no current revision, as before it loads its first (R19).
	ErrNoRevision = errors.New("app: the registry has no configuration yet")
	// ErrUnsettled refuses a promotion while a kill switch write's outcome is
	// unknown and the view is off: the switch may be on (R37). The admin API
	// answers it 503, unavailable.
	ErrUnsettled = errors.New("app: the kill switch's state is not known yet")
)

// Ports are what the coordinator acts through. A round's end reads its
// failed tool calls through ToolCalls: without them it could not tell a quiet
// round (Decision 9). Every stop of a run closes its upstream MCP sessions
// through UpstreamSessions (Decision 3). The kill switch is read and set
// through Controls (Decision 5).
type Ports struct {
	Records          Records
	Boards           *Boards
	Spend            Spend
	ToolCalls        ToolCalls
	Sweeper          Sweeper
	Revisions        Revisions
	Sessions         Sessions
	Turns            Turns
	UpstreamSessions UpstreamSessions
	Controls         Controls
	Clock            Clock
	Timers           Timers
	IDs              IDs
}

// Options are the coordinator's settings (spec Section 15).
type Options struct {
	// Holder is this instance's name, as the lease records it: the
	// deployment's holder. A takeover whose take read it as the last
	// holder's says so in the reason it sweeps with (Decision 7).
	Holder string
	// DailyCap is the deployment's daily token cap, in cost-weighted
	// tokens.
	DailyCap int64
	// Grace is how long a round's end waits for the round's write calls,
	// which commit apart from the call's time, before it reads anything of
	// the round: the gateways' record timeout [5 s].
	Grace time.Duration
	// SettleWait is how long a round's end waits for the round's model calls
	// in flight [30 s], and SettlePoll how often it looks again [1 s].
	SettleWait time.Duration
	SettlePoll time.Duration
	// Retry is how long a step that failed waits before it is tried again
	// [5 s].
	Retry time.Duration
	// ReopenWindow is how long after its latest close a person may reopen an
	// item [24 h], judged in the database's time (Decision 7).
	ReopenWindow time.Duration
	// Limit is how many items investigate at once [3], at least 1: the
	// deployment's lifecycle.investigating. Items opened past it wait in the
	// queue (Decision 4).
	Limit int
	// WriteTimeout bounds the write of a kill switch's on recorded during a
	// drain, which the drain's end of the holding's context does not end
	// (Decision 5; see RecordKillSwitchOn): the database's transaction
	// timeout [10 s].
	WriteTimeout time.Duration
	// Log receives the steps that failed; nil logs nothing.
	Log *slog.Logger
}

// Coordinator drives work items through their lifecycle (spec Sections 4.1
// and 5): it opens and starts items, opens and ends rounds, and stops runs.
// It writes each decision as a record before it acts on it, and every
// action can be repeated, so that it resumes from the records after a crash.
// One lock serializes its decisions. It acts only while its instance holds
// the lease: from TakeOver until it is fenced. Every write it makes, and
// every write the gateways make for the sessions it issues, carries the
// epoch of the term it acts under, which the guarded stores check.
type Coordinator struct {
	p    Ports
	opts Options
	log  *slog.Logger

	mu   sync.Mutex
	held bool
	// term is the holding the coordinator acts under, from TakeOver on.
	term Term
	// ctx ends with the holding, and carries its epoch: the steps that
	// timers run use it.
	ctx     context.Context
	lost    func(epoch int64)
	runs    map[board.RunID]*running
	retries map[item.ID]func()
	// autoCloses holds, by item, the auto-close timer of each item awaiting
	// its close: one per item at most (see armAutoClose).
	autoCloses map[item.ID]*autoCloseTimer
	// snapshots holds, by run, what a halt reads of a run's round from memory,
	// which a drop or a close keeps until the halt that reads it commits; the
	// fence clears them (R45; see takeSnapshot).
	snapshots map[board.RunID]haltSnapshot
	// skipped holds, sorted, the items this holding skips (see skip). It
	// changes under the lock, and Skipped reads it without, so that it
	// never waits for a takeover.
	skipped atomic.Pointer[[]item.ID]
	// items is the coordinator's account of the items it drives, by item:
	// each item that is not closed, as its latest load or record left it,
	// with its admission mark (Decision 4; see queue.go). The fence empties
	// it, and every takeover rebuilds it.
	items map[item.ID]entry
	// walking says a walk, or a takeover, is under way: the advances it makes
	// walk no queue of their own, and a takeover walks once at its end (see
	// walk).
	walking bool
	// lifecycle holds the account's snapshot, which changes under the lock,
	// and Lifecycle reads without, as Skipped does; nil while fenced.
	lifecycle atomic.Pointer[Lifecycle]
	// killSwitch is the coordinator's view of the kill switch (Decision 5):
	// whether it is on, its latest row, its latest on-row and its latest
	// off-row's time, as the store's latest answer or read gave them (see
	// applyKillSwitch). Each takeover reads it from the table, and the fence
	// clears it. seen holds its latest row, which every change of the view
	// stores (see setView), and KillSwitchSeen reads without the lock.
	killSwitch KillSwitch
	seen       atomic.Int64
	// unsettled marks the view unsettled: a write of the kill switch's whose
	// outcome is unknown came after the view's latest read or answer, so the
	// switch may be on while the view is off, and no work starts until a read
	// settles it (R37; see unsettle). rereading is the pending read that
	// settles it, armed after a read that failed, or nil (see reread). The
	// fence clears both with the view.
	unsettled bool
	rereading *rereadTimer
	// advancing counts the advances under way (see advance), and walkArmed
	// is the walk of the queue armed on the timers and not yet run, or nil
	// (see walkSoon). The fence cancels it.
	advancing int
	walkArmed *walkTimer
}

// maxSteps bounds the steps one advance takes, so that a mistake in the
// records cannot loop forever.
const maxSteps = 32

// NewCoordinator returns a coordinator over p.
func NewCoordinator(p *Ports, opts *Options) (*Coordinator, error) {
	if p.missing() {
		return nil, errors.New("app: the coordinator needs every port")
	}
	if opts.DailyCap <= 0 || opts.Grace <= 0 || opts.SettleWait < 0 || opts.SettlePoll <= 0 || opts.Retry <= 0 || opts.ReopenWindow <= 0 || opts.WriteTimeout <= 0 {
		return nil, errors.New("app: the coordinator needs a daily cap, a grace, a settling poll, a retry, a reopen window and a write timeout above zero")
	}
	if opts.Limit < 1 {
		return nil, errors.New("app: the coordinator needs a limit of at least one item investigating at once")
	}
	if opts.Holder == "" {
		return nil, errors.New("app: the coordinator needs its instance's name, as the lease records it")
	}
	c := &Coordinator{
		p: *p, opts: *opts, log: opts.Log, ctx: context.Background(), runs: map[board.RunID]*running{}, retries: map[item.ID]func(){},
		autoCloses: map[item.ID]*autoCloseTimer{}, snapshots: map[board.RunID]haltSnapshot{}, items: map[item.ID]entry{},
	}
	// A coordinator acts once it takes over.
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	return c, nil
}

// missing reports whether a port is unset.
func (p *Ports) missing() bool {
	return p.Records == nil || p.Boards == nil || p.Spend == nil || p.ToolCalls == nil || p.Sweeper == nil || p.Revisions == nil ||
		p.Sessions == nil || p.Turns == nil || p.UpstreamSessions == nil || p.Controls == nil || p.Clock == nil || p.Timers == nil || p.IDs == nil
}

// OpenInput is a work item to open.
type OpenInput struct {
	// ID names the item, as the admin API derives it from the request's key
	// (Decision 14), or is empty, for a new name. A name opens one item at
	// most (see Open).
	ID item.ID
	// Digest is the request's digest, which the item's opening records: an
	// opening repeated under the item's name tells the same request from
	// another by it.
	Digest string
	Type   string
	Title  string
	// Source says who opens the item: "person", or an inbound source.
	Source string
	// Routing holds trusted values only, which write tools' scope bindings
	// see as the item's (spec Section 3.3). Open records it as given, and
	// refuses routing it could not record so (see checkRouting).
	Routing    map[string]string
	Principal  string
	Responders []string
}

// source is who opens an item: a lowercase word, as inbound sources are
// named.
var source = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// maxTitleRunes caps an item's title.
const maxTitleRunes = 200

// The bounds of an item's routing: its keys, and the characters of a key and
// of a value.
const (
	maxRoutingKeys       = 16
	maxRoutingKeyRunes   = 64
	maxRoutingValueRunes = 256
)

// Open opens a work item, which starts at once while the items investigating
// are fewer than the limit, and waits in the queue otherwise: the walk that
// ends the opening's advance admits it in its turn (Decision 4). While a kill
// switch write's outcome is unknown, the view off, an item records its
// item_opened alone and waits queued, as no item starts (R37). An item
// opened while the kill switch is on is not staffed, for the cause
// kill_switch, left so by its latest on-row (Decision 5), and one opened once
// the day's spend reached the daily cap, for the cause daily_cap, each with
// its type's auto-close delay (Decision 8; see unstaffedAtOpening). The
// opening records the latest kill switch row the coordinator knew. Its
// title is redacted with the rules that apply always before it is recorded,
// or replaced (see title). Routing is recorded as given, so routing that is
// out of bounds, that holds a character a reader could miss, or that those
// rules would change, is refused (see checkRouting). While the registry has
// no current revision, no item opens: Open fails with ErrNoRevision.
// Once the item's opening is recorded, Open returns its id: what the item's
// start does next is tried again until it succeeds. The opening acts for the
// current term: its writes carry the term's epoch. An opening whose append
// failed is looked up by its item's id, as it may have landed (Decision 11;
// see unrecorded).
// An opening that names its item opens it once at most (Decision 14; see
// repeated): repeated with the same request, it returns the item, whatever
// the registry holds now; another request under the name is refused with
// ErrKeyReused, and an opening of an item held by a later build with
// ErrSkipped. An opening that finds its item opened only as it records, or
// whose own append landed with its answer lost, gets the same answers (see
// unrecorded).
func (c *Coordinator) Open(ctx context.Context, in *OpenInput) (item.ID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.held {
		return "", ErrNotHeld
	}
	// The opening ends with its request, or with the holding: a fence waits
	// for no store an opening waits on.
	ctx, cancel := context.WithCancel(holding.WithEpoch(ctx, c.term.Epoch))
	defer cancel()
	defer context.AfterFunc(c.ctx, cancel)()
	if in.ID != "" {
		opened, err := c.repeated(ctx, in)
		if err != nil {
			return "", err
		}
		if opened {
			return in.ID, nil
		}
	}
	rev := c.p.Revisions.Current()
	if rev == nil {
		return "", ErrNoRevision
	}
	if err := runnable(rev, in.Type); err != nil {
		return "", err
	}
	if !source.MatchString(in.Source) {
		return "", fmt.Errorf("%w: an item's source is a lowercase word", ErrInvalid)
	}
	if err := checkRouting(rev, in.Routing); err != nil {
		return "", err
	}
	unstaffed, err := c.unstaffedAtOpening(ctx, rev, in.Type)
	if err != nil {
		return "", err
	}
	id := in.ID
	if id == "" {
		id = item.ID(c.p.IDs.NewID())
	}
	s := &item.State{ID: id, Incident: item.IncidentID(c.p.IDs.NewID())}
	recs := []rec{{body: item.Body{Opened: &item.Opened{
		Type: in.Type, Title: title(rev, in.Title), Source: in.Source, Routing: maps.Clone(in.Routing),
		Principal: in.Principal, Responders: slices.Clone(in.Responders), Arm: item.ArmVisible, How: item.NoWindow, Digest: in.Digest,
		KillSwitchSeen: c.killSwitch.Seq,
	}}}}
	if unstaffed != nil {
		recs = append(recs, rec{body: item.Body{AwaitingClose: unstaffed}})
	}
	if _, err := c.record(ctx, s, recs...); err != nil {
		return c.unrecorded(ctx, in, s.ID, err)
	}
	// The item's start is the coordinator's from here on, not the request's.
	c.advance(c.ctx, s.ID)
	return s.ID, nil
}

// unstaffedAtOpening is the item_awaiting_close an opening records beside its
// item_opened when the item opens not staffed (spec Section 5.1, step 6), or
// nil, in Decision 4's order: while the kill switch is on, not staffed for
// the cause kill_switch, left so by its latest on-row (Decision 5); else,
// once the day's spend reached the daily cap, for the cause daily_cap. Each
// holds its type's auto-close delay in rev (Decision 8). A read of the day's
// spend that fails is returned.
func (c *Coordinator) unstaffedAtOpening(ctx context.Context, rev *config.Revision, typ string) (*item.AwaitingClose, error) {
	if c.killSwitch.On {
		return &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseKillSwitch, UnstaffedBy: c.killSwitch.OnSeq, AutoClose: typeDelay(rev, typ)}, nil
	}
	day, err := c.p.Spend.SpentToday(ctx)
	switch {
	case err != nil:
		return nil, fmt.Errorf("read the day's spend: %w", err)
	case day >= c.opts.DailyCap:
		return &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseDailyCap, AutoClose: typeDelay(rev, typ)}, nil
	}
	return nil, nil
}

// unrecorded answers an opening whose append failed (Decision 11). A failed
// append may have landed: the database's answer may be lost on its way back,
// or the request may end while its commit is in flight, leaving the commit to
// land after it. The lifecycle's rules also refuse the record of an opening by
// key when its item opened meanwhile: the retry of a request that ended while
// its opening's commit was in flight may read the item before that commit
// lands, then wait for it on the item's lock as it records. So, while this
// instance holds the lease, the item is looked up by its id (see landed). An
// opening that landed is answered: by key, as a repeated one (see repeated),
// the same request getting the item, which starts if it never did; without a
// key, with the id it made, and the item advances. Otherwise the append's
// failure is logged as a failed step and returned. When the look-up's load
// fails too, its failure is logged, and the item's retry is armed (see
// retry): it loads the item again and advances it, and an item whose opening
// never landed waits (see step).
func (c *Coordinator) unrecorded(ctx context.Context, in *OpenInput, id item.ID, err error) (item.ID, error) {
	if !c.held || errors.Is(err, ErrLeaseLost) {
		c.failed(ctx, id, err)
		return "", err
	}
	opened, again := c.landed(ctx, in, id)
	switch {
	case opened:
		return id, nil
	case errors.Is(again, ErrKeyReused), errors.Is(again, ErrSkipped):
		return "", again
	}
	c.failed(ctx, id, err)
	if again != nil {
		c.failed(ctx, id, again)
		c.retry(id)
	}
	return "", err
}

// landed looks up, by its id, an item whose opening's append failed, and
// reports whether the opening landed. An opening by key is answered as a
// repeated one (see repeated): another request's opening under the key is
// refused with ErrKeyReused, and an item held by a later build with
// ErrSkipped. An opening without a key finds the opening it made, the only
// one its new id can hold, and advances the item. Any other error is the
// load's.
func (c *Coordinator) landed(ctx context.Context, in *OpenInput, id item.ID) (bool, error) {
	if in.ID != "" {
		return c.repeated(ctx, in)
	}
	s, err := c.load(ctx, id)
	if err != nil || s.Opened == nil {
		return false, err
	}
	c.advance(c.ctx, id)
	return true, nil
}

// repeated answers an opening that names an item whose opening is recorded
// already, and reports whether it is (Decision 14). The same request, as the
// recorded digest tells, gets the item, and a queued item is advanced as far
// as the queue allows, and no further: the answer to its first opening may
// have been lost once its record landed, under this holding or an earlier
// one, but a retry never jumps the queue (Decision 4). Another request under
// the item's name is refused with ErrKeyReused. An item held by a later build
// is refused with ErrSkipped: one this holding skips, and one whose records
// hold one this build cannot read, as an item closed at the takeover may,
// which the takeover did not read. A refusal leaves the item as it is. It
// reads the item's records, not its board: a closed item, whose board holds
// an event of a later version, is returned as it is, though the admin API's
// view reports it held by a later build. Nothing is written to such an item.
func (c *Coordinator) repeated(ctx context.Context, in *OpenInput) (bool, error) {
	if c.isSkipped(in.ID) {
		return false, ErrSkipped
	}
	s, err := c.load(ctx, in.ID)
	switch {
	case ofLaterVersion(err):
		return false, fmt.Errorf("%w: %w", ErrSkipped, err)
	case err != nil:
		return false, err
	case s.Opened == nil:
		return false, nil
	case s.Opened.Digest != in.Digest:
		return false, ErrKeyReused
	}
	if s.Status() == item.StatusQueued {
		c.advance(c.ctx, s.ID)
	}
	return true, nil
}

// OnLost sets what the coordinator calls once it fenced itself because a
// write found the lease gone, with the epoch of the holding it acted under,
// so that its leader takes the lease again. It is called through a timer,
// never under the coordinator's lock: it may come once the leader holds the
// lease again, under another epoch.
func (c *Coordinator) OnLost(f func(epoch int64)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lost = f
}

// TakeOver has the coordinator act under term, the lease's new holding (spec
// Section 8.2), until it is fenced: it stops the turns and ends the sessions of
// any earlier holding, and carries on every item that is not closed, from its
// records (Decision 12): an item awaiting its close gets its auto-close timer,
// unless the kill switch is on, which fires after the takeover, never inside
// it (see armAutoClose). It first has the model calls that the holdings before
// term left without an outcome swept (Decision 7; see sweep), so that a
// round's end it carries on sees their outcomes. Then, before it reads any
// item, it reads the kill switch into its view (Decision 12): a read that
// fails refuses the lease, and the coordinator holds nothing, so that the view
// never defaults to off. Each item's advance acts on that view, as every step
// does (see takeOverItems): a run that owes the kill of its latest on-row is
// halted, and an item with no run that owes it is left not staffed, before
// that item's next round or start, whatever the switch says now. Before it
// acts on any item, it reads every such item's records, and its run's board.
// It skips each item whose records or board events are of a later version
// than this build reads (item.ErrVersion, board.ErrVersion): the item is
// logged once and left alone, and the other items are carried on
// (Decision 19; see skip). It refuses the lease, and holds nothing, when a load
// fails: a record or event of a later version may be among those it could not
// load, and an item's next step may write a record before it reads the board,
// so its caller gives the lease up and takes it again to read them. Invalid
// records and boards do not refuse it, those that fold into an invalid state
// and those whose body does not decode, which a load reports as item.ErrInvalid
// or board.ErrInvalid: that item's steps fail and are tried again, and the
// other items go on. From what it read, it rebuilds its account of the items,
// without admission marks: the items it skips, and those whose records do not
// fold, are not in it (Decision 4). It advances every item, then walks the
// queue once, which admits again. A round in progress gets fresh sessions and
// turns for the agents that have not ended theirs, and ends unseen when it is
// taken up with less than a third of its span left (see hold); a round whose
// deadline passed before this instance acted on it ends as missed says; a
// decision recorded is carried out. Steps that timers run later use ctx,
// which the holding ends.
// Every write of the takeover, the sweep's included, and of those steps,
// carries term's epoch. A takeover whose holding ended before it began, or that
// finds the lease gone, acts on no item after it, and says so.
func (c *Coordinator) TakeOver(ctx context.Context, term Term) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fence()
	ctx = holding.WithEpoch(ctx, term.Epoch)
	if err := c.sweep(ctx, term); err != nil {
		return fmt.Errorf("take over: %w", err)
	}
	state, err := c.p.Controls.KillSwitch(ctx)
	if err != nil {
		return fmt.Errorf("take over: read the kill switch: %w", err)
	}
	ids, err := c.p.Records.Active(ctx)
	if err != nil {
		return fmt.Errorf("take over: %w", err)
	}
	states, skips, err := c.read(ctx, ids)
	if err != nil {
		return fmt.Errorf("take over: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("take over: %w", err)
	}
	c.held, c.term, c.ctx = true, term, ctx
	view := viewOf(&state)
	c.setView(&view)
	for _, id := range ids {
		if err := skips[id]; err != nil {
			c.skip(ctx, id, err)
		}
	}
	c.rebuild(states)
	c.takeOverItems(ctx, ids, &state)
	if !c.held {
		return fmt.Errorf("take over: %w", ErrLeaseLost)
	}
	return ctx.Err()
}

// advanceAll advances each item of ids, as a takeover does, while the
// coordinator holds and its holding lasts. Its advances walk no queue of
// their own: the takeover walks once, after it has advanced every item
// (Decision 12).
func (c *Coordinator) advanceAll(ctx context.Context, ids []item.ID) {
	c.walking = true
	defer func() { c.walking = false }()
	for _, id := range ids {
		if !c.held || ctx.Err() != nil {
			return
		}
		c.advance(ctx, id)
	}
}

// advanceEvery advances every item of the account, in the order of their
// items (R33), its advances walking no queue of their own, then walks the
// queue once, as the kill switch's on does, and a read that settles the view
// (R37).
func (c *Coordinator) advanceEvery(ctx context.Context) {
	c.advanceAll(ctx, slices.Sorted(maps.Keys(c.items)))
	c.walk(ctx)
}

// sweep has the Sweeper give an outcome to each model call without one that
// began before term's holding did: the instance that made it stopped before
// its outcome was recorded (Decision 7). The reason names who stopped: the
// instance the take read as the last holder, or this instance, whose earlier
// holding ended. A sweep is logged with how many calls it gave an outcome,
// "unknown", none included. The sweep is best effort, and a failure of it is
// logged, but one that finds the lease gone stops the takeover, as does the
// end of the holding, which the leader logs.
func (c *Coordinator) sweep(ctx context.Context, term Term) error {
	n, err := c.p.Sweeper.Sweep(ctx, term.At, sweepReason(term.PreviousHolder, c.opts.Holder))
	switch {
	case errors.Is(err, ErrLeaseLost) || ctx.Err() != nil:
		return err
	case err != nil:
		c.log.ErrorContext(ctx, "the model calls left without an outcome could not be swept", "err", err)
	default:
		c.log.InfoContext(ctx, "the model calls left without an outcome were swept", "count", n, "epoch", term.Epoch)
	}
	return nil
}

// sweepReason is the reason a sweep gives the calls that the holding of
// previous left, as this instance, holder, takes over. The lease's first
// holding follows no holder.
func sweepReason(previous, holder string) string {
	switch previous {
	case "":
		return "an earlier holding ended before the call's outcome was recorded"
	case holder:
		return "this instance's earlier holding ended before the call's outcome was recorded"
	}
	return "the instance " + previous + " stopped before the call's outcome was recorded"
}

// read reads every item of ids, as a takeover does before it acts on any
// (see readable). It returns the states of the items whose records fold and
// that it does not skip, whatever their boards hold, and, by item, the items
// held by a later build, with what it met there. A read that fails otherwise,
// but for invalid records or an invalid board, fails it.
func (c *Coordinator) read(ctx context.Context, ids []item.ID) ([]*item.State, map[item.ID]error, error) {
	var states []*item.State
	skips := map[item.ID]error{}
	for _, id := range ids {
		s, err := c.readable(ctx, id)
		switch {
		case ofLaterVersion(err):
			skips[id] = err
		case err != nil && !corrupt(err):
			return nil, nil, err
		case s != nil:
			states = append(states, s)
		}
	}
	return states, skips, nil
}

// readable reads an item's records, and its run's board, as a takeover does
// before it acts on any item, and returns the item's state once its records
// fold, whatever its board holds.
func (c *Coordinator) readable(ctx context.Context, id item.ID) (*item.State, error) {
	s, err := c.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.Run == nil {
		return s, nil
	}
	return s, c.p.Boards.Readable(ctx, s.Run.ID)
}

// ofLaterVersion reports whether err says that an item holds a record, or its
// run's board an event, of a later version than this build reads.
func ofLaterVersion(err error) bool {
	return errors.Is(err, item.ErrVersion) || errors.Is(err, board.ErrVersion)
}

// corrupt reports whether err says that an item's records or board are
// invalid: they fold into an invalid state, or a body does not decode.
func corrupt(err error) bool {
	return errors.Is(err, item.ErrInvalid) || errors.Is(err, board.ErrInvalid)
}

// Skipped returns, sorted, the items this holding skips (Decision 19): those
// whose records or board events a later build wrote. It takes no lock, so
// that it never waits for a takeover. A fenced coordinator holds nothing and
// skips nothing.
func (c *Coordinator) Skipped() []item.ID {
	if p := c.skipped.Load(); p != nil {
		return slices.Clone(*p)
	}
	return nil
}

// isSkipped reports whether this holding skips the item.
func (c *Coordinator) isSkipped(id item.ID) bool {
	p := c.skipped.Load()
	return p != nil && slices.Contains(*p, id)
}

// skip leaves an item whose records or board events a later build wrote to a
// build that can read them (Decision 19). It logs "item skipped", once per
// item and holding, and drops what the coordinator holds of the item: its
// auto-close timer, which is cancelled, and of its run, its sessions, which
// end, its agents' turns, which stop, and its timers, settle's own retry among
// them. Only this holding's sessions and turns can be live: its takeover ended
// every earlier holding's. No retry of the item is pending when it is skipped
// (advance cancels its own first), and none is armed again: advance passes
// the item by until the next takeover reads it again. The item leaves the
// account of the items (Decision 4), so that it holds up no other.
func (c *Coordinator) skip(ctx context.Context, id item.ID, err error) {
	c.cancelAutoClose(id)
	for run, r := range c.runs {
		if r.item != id {
			continue
		}
		c.p.Sessions.EndRun(run)
		for _, agent := range r.agents() {
			c.p.Turns.Stop(run, agent)
		}
		c.cancel(r)
		delete(c.runs, run)
	}
	c.leave(id)
	if c.isSkipped(id) {
		return
	}
	skipped := []item.ID{id}
	if p := c.skipped.Load(); p != nil {
		skipped = append(slices.Clone(*p), id)
	}
	slices.Sort(skipped)
	c.skipped.Store(&skipped)
	c.log.WarnContext(ctx, "item skipped", "item", id, "reason", err.Error())
}

// Fence stops the coordinator acting, as an instance that lost the lease
// must: its turns stop, its sessions end and its timers are cancelled, the
// auto-close timers among them, which the instance that leads next arms
// again (Decision 8). It acts again only once it takes over, and skips no
// item until then; a halt after the takeover classes its turns from the
// board, as the fence keeps no snapshot (R45). Its account of the items is
// emptied, and its snapshot with it (Decision 12): the instance that leads
// next rebuilds both from the records. Its view of the kill switch is
// cleared, off, until a takeover reads the switch again, and with it the
// view's mark of a write whose outcome is unknown, whose pending read is
// cancelled (R37).
func (c *Coordinator) Fence() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fence()
}

func (c *Coordinator) fence() {
	c.held = false
	c.p.Turns.StopAll()
	c.p.Sessions.EndAll()
	for _, r := range c.runs {
		c.cancel(r)
	}
	for _, cancel := range c.retries {
		cancel()
	}
	c.cancelAutoCloses()
	c.cancelReread()
	c.cancelWalkSoon()
	clear(c.runs)
	clear(c.retries)
	clear(c.snapshots)
	clear(c.items)
	c.setView(&KillSwitch{})
	c.unsettled = false
	c.lifecycle.Store(nil)
	c.skipped.Store(nil)
}

// advance takes an item through every step its records allow now (see
// steps), then walks the queue (Decision 4): every change of the queue, or of
// the items investigating, passes through an advance, or through a walk armed
// on the timers when a load outside one changes them (see load). The advances
// of a walk, or of a takeover, walk no queue of their own (see walk).
func (c *Coordinator) advance(ctx context.Context, id item.ID) {
	c.advancing++
	defer func() { c.advancing-- }()
	c.steps(ctx, id)
	c.walk(ctx)
}

// walkSoon arms a walk of the queue at once on the timers, which runs once
// the lock is free, under the coordinator's own context, while it holds (see
// walk): one is armed at most, and a walk that runs first cancels it. A load
// outside an advance or a walk, as a preview's or a refused act's, may find
// an item that no longer counts among the items investigating, its record's
// answer lost, which frees a slot that no advance of the act's own fills; the
// act records nothing for it, and a preview acts on nothing, so the walk
// follows the act.
func (c *Coordinator) walkSoon() {
	if c.walkArmed != nil {
		return
	}
	t := &walkTimer{}
	t.cancel = c.p.Timers.At(c.p.Clock.Now(), func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.walkArmed == t && c.holds() {
			c.walk(c.ctx)
		}
	})
	c.walkArmed = t
}

// walkTimer is the timer of a walk walkSoon armed: its cancel, and, by its
// address, whether it is still the walk armed when it fires.
type walkTimer struct{ cancel func() }

// cancelWalkSoon cancels the walk walkSoon armed, if any: a walk that runs
// takes its place. A cancel is no port event (R33).
func (c *Coordinator) cancelWalkSoon() {
	if c.walkArmed != nil {
		c.walkArmed.cancel()
		c.walkArmed = nil
	}
}

// steps takes an item through every step its records allow now, until it
// waits for a round's end, for its admission or promotion, or for its close.
// A step that fails is tried again after the retry wait, but one that meets a
// later version skips the item, which advance passes by from then on (see
// skip). The lifecycle's rules refuse to record after a record of a later
// version, so that no step writes past one.
func (c *Coordinator) steps(ctx context.Context, id item.ID) {
	if cancel := c.retries[id]; cancel != nil {
		cancel()
		delete(c.retries, id)
	}
	if c.isSkipped(id) {
		return
	}
	for range maxSteps {
		s, err := c.load(ctx, id)
		if err == nil {
			var done bool
			if done, err = c.step(ctx, s); err == nil && !done {
				continue
			}
		}
		switch {
		case err == nil:
		case ofLaterVersion(err):
			c.skip(ctx, id, err)
		default:
			c.failed(ctx, id, err)
			if c.held {
				c.retry(id)
			}
		}
		return
	}
	c.failed(ctx, id, errors.New("app: the item took more steps at once than an item can"))
}

// retry arms the item's retry, which advances it after the retry wait, while
// this instance holds the lease, in place of any retry of it armed before:
// an item has one retry at most, which advance cancels first.
func (c *Coordinator) retry(id item.ID) {
	if cancel := c.retries[id]; cancel != nil {
		cancel()
	}
	c.retries[id] = c.p.Timers.At(c.p.Clock.Now().Add(c.opts.Retry), func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.held {
			c.advance(c.ctx, id)
		}
	})
}

// failed logs a step that failed. A write that found the lease gone fences
// the coordinator at once, and tells its leader. A write refused for want of
// an epoch is a bug, not a lost lease: it is logged as such, never fences,
// and its step is tried again as any other.
func (c *Coordinator) failed(ctx context.Context, id item.ID, err error) {
	if errors.Is(err, holding.ErrNoEpoch) {
		c.log.ErrorContext(ctx, "write without an epoch", "item", id, "err", err)
		return
	}
	c.log.ErrorContext(ctx, "a step of a work item failed", "item", id, "err", err)
	if errors.Is(err, ErrLeaseLost) && c.held {
		c.fence()
		if c.lost != nil {
			lost, epoch := c.lost, c.term.Epoch
			c.p.Timers.At(c.p.Clock.Now(), func() { lost(epoch) })
		}
	}
}

// step takes an item's next step, in Decision 12's order, and reports whether
// it must wait: for a round's end, for its admission or promotion, or for the
// item's close. An item with no opening, as one whose opening's append failed
// without landing, has no step: it waits, and nothing is written to it
// (Decision 11). A closed item takes the closed step and waits (Decision 6),
// told by its state, not by how it awaited its close: a close keeps that, and
// an item closed while queued, or started without a run, never awaited it. An
// item awaiting its close forgets its run, and arms its auto-close (Decision
// 12; see armAutoClose). An item with no run takes the step its kill switch
// rule, its start or its run's creation gives (see runless). A run that
// stopped is finished (see finish); one that has not takes the step its
// rounds give (see runStep).
func (c *Coordinator) step(ctx context.Context, s *item.State) (wait bool, err error) {
	switch {
	case s.Opened == nil:
		return true, nil
	case s.Status() == item.StatusClosed:
		return true, c.closedStep(ctx, s)
	case s.Closing != nil:
		c.forget(s)
		c.armAutoClose(ctx, s)
		return true, nil
	case s.Run == nil:
		return c.runless(ctx, s)
	case s.Run.Stopped != nil:
		return false, c.finish(ctx, s)
	}
	return c.runStep(ctx, s)
}

// runless takes the step of s, an item with no run (Decision 12, rule 4).
// While the kill switch is on, or when the item owes the view's latest
// on-row, it is left not staffed (see leaveUnstaffed), a started item with no
// run created first, whatever its admission or promotion. While a kill switch
// write's outcome is unknown, the view off, it waits, and nothing is recorded
// on that account: the switch may be on (R37), and the read that settles the
// view advances it again. Otherwise a queued item starts once the walk
// admitted it or a person promoted it, on any advance, its own retry
// included, and waits otherwise (Decision 4): a promoted item's start ignores
// the queue. An item that started gets its run.
func (c *Coordinator) runless(ctx context.Context, s *item.State) (wait bool, err error) {
	switch {
	case c.killSwitch.On || s.Owes(c.killSwitch.OnSeq):
		return false, c.leaveUnstaffed(ctx, s)
	case c.unsettled:
		return true, nil
	case s.Started == nil && !c.startDue(s):
		return true, nil
	case s.Started == nil:
		return false, c.start(ctx, s)
	}
	return false, c.create(ctx, s)
}

// runStep takes the step of s, an item whose run has not stopped (Decision
// 12, rules 6 to 9): a run with no agent to invite stops not staffed; a round
// past its deadline that this instance did not hold ends as missed says; a
// run that owes the kill of the view's latest on-row is dropped, then halted
// killed (see killRun), before it opens a round or starts a turn, whatever
// the switch says now; a run whose last round decided its stop stops so;
// otherwise the run opens its next round, or holds its open one. While a kill
// switch write's outcome is unknown, the view off, it waits where it would
// open or hold a round, so that no session is issued or moved, and nothing is
// recorded on that account (R37): the rules that stop work go on.
func (c *Coordinator) runStep(ctx context.Context, s *item.State) (wait bool, err error) {
	l := s.Run.Latest()
	switch {
	case l == nil && len(s.Run.Created.Agents) == 0:
		return false, c.unstaffed(ctx, s)
	case l != nil && l.Ended == nil && c.missedRound(s.Run.ID, l):
		return false, c.missed(ctx, s)
	case s.Owes(c.killSwitch.OnSeq):
		return false, c.killRun(ctx, s)
	case l != nil && l.Ended != nil && l.Ended.Decision.Next == item.NextStop:
		return false, c.conclude(ctx, s)
	case c.unsettled:
		return true, nil
	case l == nil || l.Ended != nil:
		return false, c.open(ctx, s)
	}
	return true, c.hold(ctx, s)
}

// startDue reports whether the start of s, a queued item, is due, which its
// step then makes (Decision 4): the walk admitted it, or a person promoted it.
// Either mark counts the item among the items investigating until it starts.
func (c *Coordinator) startDue(s *item.State) bool {
	return c.admitted(s.ID) || s.Promotion != nil
}

// missedRound reports whether the run's open round, l, passed its deadline
// while this instance did not hold it: missed ends such a round. A round this
// instance held and dropped, keeping its snapshot (see drop), is no missed
// round: one dropped for its halt is ended stopped by the halt, with the
// classes the snapshot keeps (Decision 3); one dropped by a close whose
// outcome was unknown, which did not land, is held again by the step, and
// ended by its deadline at once, unseen, if that passed (see closeNow).
func (c *Coordinator) missedRound(run board.RunID, l *item.Round) bool {
	if snap, ok := c.snapshots[run]; ok && snap.round == l.Opened.Round {
		return false
	}
	r := c.runs[run]
	return (r == nil || r.round != l.Opened.Round) && !c.p.Clock.Now().Before(l.Opened.Deadline)
}

// start locks the configuration the item runs under, the current revision,
// and records the latest kill switch row the coordinator knew, unless the
// item is not staffed, for the cause of the first of these checks that
// fails, in Decision 4's order, after the kill switch's, which the step makes
// before any start (see runless):
//   - daily_cap: the day's spend reached the daily cap, as at an opening (spec
//     Section 5.1). When a slot frees at the cap, the walk leaves every queued
//     item it reaches so, and the whole queue drains;
//   - routing: the item's routing fails its check under that revision (see
//     checkRouting), as when a rule that applies always came since the item
//     opened (Decision 11);
//   - type: that revision cannot run the item's type.
//
// The record of an item not staffed holds its type's auto-close delay in that
// revision, or zero when the revision holds no such type (Decision 8). A start
// whose read of the day's spend fails, or that finds no revision current,
// fails, and is tried again.
func (c *Coordinator) start(ctx context.Context, s *item.State) error {
	rev := c.p.Revisions.Current()
	if rev == nil {
		return ErrNoRevision
	}
	day, err := c.p.Spend.SpentToday(ctx)
	if err != nil {
		return fmt.Errorf("read the day's spend: %w", err)
	}
	var cause item.Cause
	switch {
	case day >= c.opts.DailyCap:
		cause = item.CauseDailyCap
	case checkRouting(rev, s.Opened.Routing) != nil:
		cause = item.CauseRouting
	case runnable(rev, s.Opened.Type) != nil:
		cause = item.CauseType
	}
	if cause != "" {
		_, err := c.record(ctx, s, rec{body: item.Body{AwaitingClose: &item.AwaitingClose{
			Reason: item.StopNotStaffed, Cause: cause, AutoClose: typeDelay(rev, s.Opened.Type),
		}}})
		return err
	}
	lock, err := c.p.Revisions.Lock(rev)
	if err != nil {
		return fmt.Errorf("lock revision %s: %w", rev.Commit(), err)
	}
	_, err = c.record(ctx, s, rec{body: item.Body{Started: &item.Started{
		Commit: lock.Commit, Digest: lock.Digest, Images: lock.RuntimeImages, KillSwitchSeen: c.killSwitch.Seq,
	}}})
	return err
}

// create creates the item's run, the primary run that people see, with the
// settings and budget of its type, inviting the agents subscribed to it. The
// run's creation records the latest kill switch row the coordinator knew: a
// later on-row is a kill the run owes (Decision 5).
func (c *Coordinator) create(ctx context.Context, s *item.State) error {
	rev, err := c.p.Revisions.At(ctx, s.Started.Commit)
	if err != nil {
		return fmt.Errorf("read revision %s: %w", s.Started.Commit, err)
	}
	ws := rev.WorkItemType(s.Opened.Type).Settings()
	_, err = c.record(ctx, s, rec{run: board.RunID(c.p.IDs.NewID()), body: item.Body{RunCreated: &item.RunCreated{
		Class: item.ClassPrimary, Mode: item.ModeVisible, Settings: boardSettings(&ws),
		Budget:     item.Budget{WallClock: ws.Budget.WallClock, Tokens: ws.Budget.Tokens, Rounds: ws.Budget.Rounds},
		RoundLimit: ws.RoundLimit, Agents: invited(rev, s.Opened.Type, ws.MaxAgents), KillSwitchSeen: c.killSwitch.Seq,
	}}})
	return err
}

// unstaffed stops a run with no agent to invite, and says so on its board.
func (c *Coordinator) unstaffed(ctx context.Context, s *item.State) error {
	if _, err := c.p.Boards.Notice(ctx, s.Run.ID, board.Coordinator, &board.Notice{Code: board.NoticeNoAgents}); err != nil {
		return err
	}
	stop := s.Run.Stop("")
	_, err := c.record(ctx, s, rec{run: s.Run.ID, body: item.Body{RunStopped: &stop}})
	return err
}

// conclude stops a run as its last round decided, with the conclusion its
// board's assessment gives.
func (c *Coordinator) conclude(ctx context.Context, s *item.State) error {
	a, err := c.p.Boards.Assess(ctx, s.Run.ID, time.Time{})
	if err != nil {
		return err
	}
	stop := s.Run.Stop(a.Conclusion)
	_, err = c.record(ctx, s, rec{run: s.Run.ID, body: item.Body{RunStopped: &stop}})
	return err
}

// finish acts on a run that stopped (Decision 3), each effect repeatable: its
// sessions, its turns and its upstream sessions end; a run the kill switch
// stopped gets the board's kill_switch notice (see noticeKilled); then the
// stop entry, with the conclusion its record holds, ends its board, which may
// take tries; and the item awaits its close, with the run's reason as the
// item's (ItemReason, which reads killed as not_staffed), its type's
// auto-close delay in the revision it locked, or zero when that revision
// cannot be read (Decision 8; see writerDelay), and, for a run the kill
// switch stopped, the on-row that stopped it. The item stays active until
// that record, so a crash before it is carried on.
func (c *Coordinator) finish(ctx context.Context, s *item.State) error {
	run := s.Run
	st := run.Stopped
	c.p.Sessions.EndRun(run.ID)
	if l := run.Latest(); l != nil {
		for _, agent := range l.Opened.Agents {
			c.p.Turns.Stop(run.ID, agent)
		}
	}
	c.p.UpstreamSessions.CloseRun(run.ID)
	killed := st.Reason == item.StopKilled
	if killed {
		if err := c.noticeKilled(ctx, run.ID); err != nil {
			return err
		}
	}
	if _, err := c.p.Boards.Stop(ctx, run.ID, stopInput(st)); err != nil {
		return err
	}
	closing := &item.AwaitingClose{Reason: item.ItemReason(st.Reason), AutoClose: c.writerDelay(ctx, s)}
	if killed {
		closing.UnstaffedBy = st.Killed
	}
	_, err := c.record(ctx, s, rec{body: item.Body{AwaitingClose: closing}})
	return err
}

// writerDelay is the auto-close delay of s's type as the writer of its
// item_awaiting_close can read it (Decision 8): in the revision s locked, or,
// for an item that never started, in the current one; zero when it can read
// neither, or the revision holds no such type.
func (c *Coordinator) writerDelay(ctx context.Context, s *item.State) time.Duration {
	if s.Started != nil {
		rev, err := c.p.Revisions.At(ctx, s.Started.Commit)
		if err != nil {
			return 0
		}
		return typeDelay(rev, s.Opened.Type)
	}
	if rev := c.p.Revisions.Current(); rev != nil {
		return typeDelay(rev, s.Opened.Type)
	}
	return 0
}

// stopInput is the stop entry of a run that stopped as st records.
func stopInput(st *item.RunStopped) *board.StopInput {
	return &board.StopInput{Reason: string(st.Reason), Conclusion: st.Conclusion, Rounds: st.Rounds, WallClock: st.WallClock, Tokens: st.Spent}
}

// forget drops what the coordinator holds of an item that awaits its close.
func (c *Coordinator) forget(s *item.State) {
	if s.Run == nil {
		return
	}
	if r := c.runs[s.Run.ID]; r != nil {
		c.cancel(r)
		delete(c.runs, s.Run.ID)
	}
}

// load folds an item's records, and refreshes the account of the items with
// what it finds (Decision 4): an item whose records do not fold, or that a
// later build holds, leaves it (see unreadable). A load outside an advance or
// a walk that finds the item no longer counting among the items
// investigating, as when a record of it landed with its answer lost, has the
// queue walked at once (see walkSoon).
func (c *Coordinator) load(ctx context.Context, id item.ID) (*item.State, error) {
	records, err := c.p.Records.Load(ctx, id)
	if err != nil {
		err = fmt.Errorf("load item %s: %w", id, err)
		c.unreadable(id, err)
		return nil, err
	}
	s, err := item.Fold(records)
	if err != nil {
		c.unreadable(id, err)
		return nil, err
	}
	counted := c.items[id].counts()
	c.refresh(id, s)
	if counted && !c.items[id].counts() && c.advancing == 0 && !c.walking {
		c.walkSoon()
	}
	return s, nil
}

// rec is a record to append: its body, and the run it is about, "" for a
// record of the item.
type rec struct {
	run  board.RunID
	body item.Body
}

// record appends recs in one append, each checked first against the fold of
// the item's records as committed and of the records before it in the append:
// the lifecycle's rules refuse a record out of order, as a decision made twice
// would be. Each record takes its incident from that fold, or, an opening, the
// incident s gives, so that a state s that is stale cannot give it another
// (Decision 2). record returns the item's state after the commit, folded from
// the history and the committed records, with their times, which the account
// of the items follows (Decision 4).
func (c *Coordinator) record(ctx context.Context, s *item.State, recs ...rec) (*item.State, error) {
	var history []item.Record
	committed, err := c.p.Records.Append(ctx, s.ID, func(h []item.Record) ([]item.Draft, error) {
		history = h
		check, err := item.Fold(h)
		if err != nil {
			return nil, err
		}
		drafts := make([]item.Draft, 0, len(recs))
		for i := range recs {
			b := recs[i].body
			b.V = item.Version
			incident := check.Incident
			if b.Opened != nil {
				incident = s.Incident
			}
			d := item.Draft{ID: c.p.IDs.NewID(), Item: s.ID, Incident: incident, Run: recs[i].run, Body: b}
			r := item.Record{Seq: int64(len(h) + i + 1), ID: d.ID, Item: d.Item, Incident: d.Incident, Run: d.Run, Body: b}
			if err := check.Apply(&r); err != nil {
				return nil, err
			}
			drafts = append(drafts, d)
		}
		return drafts, nil
	})
	if err != nil {
		return nil, fmt.Errorf("record item %s: %w", s.ID, err)
	}
	after, err := item.Fold(slices.Concat(history, committed))
	if err != nil {
		return nil, fmt.Errorf("fold item %s after its commit: %w", s.ID, err)
	}
	c.refresh(s.ID, after)
	return after, nil
}

func (c *Coordinator) arm(r *running, at time.Time, fn func()) {
	r.timers = append(r.timers, c.p.Timers.At(at, fn))
}

func (c *Coordinator) cancel(r *running) {
	for _, cancel := range r.timers {
		cancel()
	}
	r.timers = nil
}

// runnable refuses a type the coordinator cannot run: one that does not
// exist, or whose key reads its values from a tool when an item opens,
// which Plan 7c builds.
func runnable(rev *config.Revision, typ string) error {
	wt := rev.WorkItemType(typ)
	if wt == nil {
		return fmt.Errorf("%w: the configuration has no such work item type", ErrInvalid)
	}
	for _, f := range wt.Settings().Key {
		if f.Values.Read != nil {
			return fmt.Errorf("%w: the type's key reads its values when an item opens, which the coordinator does not do yet", ErrInvalid)
		}
	}
	return nil
}

// boardSettings are a run's board settings, from its type's.
func boardSettings(ws *config.WorkSettings) board.Settings {
	schema := key.Schema{Fields: make([]key.Field, 0, len(ws.Key))}
	for _, f := range ws.Key {
		schema.Fields = append(schema.Fields, key.Field{Name: f.Name, Values: slices.Clone(f.Values.List)})
	}
	return board.Settings{
		Schema: schema,
		Convergence: board.Convergence{
			MinContributors: ws.Convergence.MinContributors, MinSources: ws.Convergence.MinSources, RefutationMargin: ws.Convergence.RefutationMargin,
		},
		Limits: board.Limits{EntriesPerRound: ws.Limits.EntriesPerRound, OpenClaims: ws.Limits.OpenClaims, LeaseRounds: ws.Limits.LeaseRounds},
	}
}

// invited returns the agents an item of type typ invites: those subscribed
// to the type, in the revision's order, but those that pin a yanked skill
// version, up to max.
func invited(rev *config.Revision, typ string, maxAgents int) []string {
	out := []string{}
	for _, a := range rev.Agents() {
		if len(out) == maxAgents {
			break
		}
		if slices.Contains(a.Spec.Subscribe.WorkItemTypes, typ) && len(rev.YankedPins(a.Metadata.Name)) == 0 {
			out = append(out, a.Metadata.Name)
		}
	}
	return out
}

// always returns the redaction rules that apply always: the session tokens'
// rule, then the configuration's.
func always(rev *config.Revision) *redact.Redactor {
	rules := []redact.Rule{redact.SessionTokens()}
	for _, r := range rev.AlwaysRedactionRules() {
		rules = append(rules, redact.FromConfig(r))
	}
	return redact.New(rules...)
}

// title redacts an item's title as it is recorded, cut to maxTitleRunes. A
// title whose escapes may hide what a rule matches is replaced whole, and so
// is one that, once trimmed, holds a character a reader could miss
// (config.Unseen): replaced, not refused, so that no opening fails for its
// title (Decision 11). A title whose only such characters lead or trail it,
// as a trailing line feed, is trimmed, not replaced (R44).
func title(rev *config.Revision, raw string) string {
	red := always(rev)
	if red.Escaped(raw) {
		return "[a title the redaction rules could not read]"
	}
	trimmed := strings.TrimSpace(raw)
	if strings.ContainsFunc(trimmed, config.Unseen) {
		return "[a title with characters a reader could miss]"
	}
	return cut(red.Text(trimmed), maxTitleRunes)
}

// checkRouting refuses routing that cannot be recorded as given and quoted on
// the board. Write tools' scope bindings read the values, so they are recorded
// as given, and a value that redaction would change is no value (spec Section
// 3.6). It refuses more than maxRoutingKeys keys, a key that is empty or of
// more than maxRoutingKeyRunes characters, and a value of more than
// maxRoutingValueRunes. It refuses a key or a value that holds a character a
// reader could miss (config.Unseen; Decision 11): a value such as
// checkout\nservice=payments would show on the opening's claim as a second
// line that reads as another routing member. It tests each key and each
// value alone, and both as the member key=value, which the opening's claim
// on the board holds, against the rules that apply always, and refuses
// routing that those rules would change in any of the three, or whose
// escapes may hide what they remove: a rule for password=... changes the
// member of the routing {"password": "..."}, though neither its key nor its
// value alone. The error never quotes routing: it may be a secret.
func checkRouting(rev *config.Revision, routing map[string]string) error {
	if len(routing) > maxRoutingKeys {
		return fmt.Errorf("%w: an item's routing holds at most %d keys", ErrInvalid, maxRoutingKeys)
	}
	red := always(rev)
	for _, k := range slices.Sorted(maps.Keys(routing)) {
		v := routing[k]
		member := k + "=" + v
		switch {
		case k == "" || utf8.RuneCountInString(k) > maxRoutingKeyRunes:
			return fmt.Errorf("%w: a routing key holds 1 to %d characters", ErrInvalid, maxRoutingKeyRunes)
		case utf8.RuneCountInString(v) > maxRoutingValueRunes:
			return fmt.Errorf("%w: a routing value holds at most %d characters", ErrInvalid, maxRoutingValueRunes)
		case strings.ContainsFunc(k, config.Unseen) || strings.ContainsFunc(v, config.Unseen):
			return fmt.Errorf("%w: a routing key or value holds a character a reader could miss, such as a tab, a line feed or a zero-width space", ErrInvalid)
		case red.Text(k) != k || red.Text(v) != v || red.Text(member) != member,
			red.Escaped(k) || red.Escaped(v) || red.Escaped(member):
			return fmt.Errorf("%w: a routing key or value holds text the redaction rules remove, or an escape that may hide it", ErrInvalid)
		}
	}
	return nil
}

// triggerClaim is the evidence an item opens with, composed by rule: who
// opened it, its title and its routing values, redacted. The board holds it,
// trimmed, as a member of its snapshot, which the LLM gateway checks as
// written and as claim=<claim>; a claim whose escapes may hide what a rule
// removes would make the gateway refuse every model call that reads the
// snapshot, so it is replaced whole. Open refuses routing that could make
// one: this is for records it did not check.
func triggerClaim(rev *config.Revision, o *item.Opened) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Opened by %s: %s", o.Source, o.Title)
	for _, k := range slices.Sorted(maps.Keys(o.Routing)) {
		fmt.Fprintf(&b, "; %s=%s", k, o.Routing[k])
	}
	red := always(rev)
	claim := cut(strings.TrimSpace(red.Text(b.String())), board.MaxClaimRunes)
	if red.Escaped(claim) || red.Escaped("claim="+claim) {
		return "Opened by " + o.Source + ": [an opening the redaction rules could not read]"
	}
	return claim
}

// cut cuts s to at most n characters, the last of them a mark that it was
// cut.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
