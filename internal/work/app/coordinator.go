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
	// ErrSkipped marks a request about an item this holding skips: a later
	// build wrote records or board events of it that this build cannot read,
	// and only such a build acts on it (Decision 19).
	ErrSkipped = errors.New("app: the item is held by a later build")
)

// Ports are what the coordinator acts through.
type Ports struct {
	Records   Records
	Boards    *Boards
	Spend     Spend
	Revisions Revisions
	Sessions  Sessions
	Turns     Turns
	Clock     Clock
	Timers    Timers
	IDs       IDs
}

// Options are the coordinator's settings (spec Section 15).
type Options struct {
	// DailyCap is the deployment's daily token cap, in cost-weighted
	// tokens.
	DailyCap int64
	// Grace is how long a round's end waits for the round's write calls,
	// which commit apart from the call's time: the gateways' record
	// timeout [5 s].
	Grace time.Duration
	// SettleWait is how long a round's end waits for the run's model calls
	// in flight [30 s], and SettlePoll how often it looks again [1 s].
	SettleWait time.Duration
	SettlePoll time.Duration
	// Retry is how long a step that failed waits before it is tried again
	// [5 s].
	Retry time.Duration
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
	// skipped holds, sorted, the items this holding skips (see skip). It
	// changes under the lock, and Skipped reads it without, so that it
	// never waits for a takeover.
	skipped atomic.Pointer[[]item.ID]
}

// running is what the coordinator holds in memory of a run's open round:
// the sessions it issued, the turns that run, by agent, with their ids, the
// turns the runner could not start yet, how the others ended, whether this
// instance took the round up too late, which turns it started too late, and
// the round's timers.
type running struct {
	item     item.ID
	round    int
	opened   time.Time
	deadline time.Time
	// late says this instance took the round up with less than a third of its
	// span left (see hold): however the round ends, it ends unseen.
	late bool
	// startedLate holds the agents whose turns started with less than a
	// third of the round's span left, in a round taken up in time (see
	// begin): one still running at the deadline, or reported interrupted,
	// counts as unstarted.
	startedLate map[string]bool
	tokens      map[string]Tokens
	turns       map[string]string
	starting    map[string]*Turn
	exits       map[string]item.TurnClass
	ended       *ended
	timers      []func()
}

// ended is how and when a round ended, until its end is recorded.
type ended struct {
	by item.EndedBy
	at time.Time
	// unseen says this instance did not see the round's turns: it did not
	// act on the round, or took it up too late (item.Ending).
	unseen bool
}

// maxSteps bounds the steps one advance takes, so that a mistake in the
// records cannot loop forever.
const maxSteps = 32

// lateShare is the part of a round's span that must be left when this
// instance takes the round up, or starts a turn in it, for the round or the
// turn to count: a third.
const lateShare = 3

// NewCoordinator returns a coordinator over p.
func NewCoordinator(p *Ports, opts *Options) (*Coordinator, error) {
	if p.missing() {
		return nil, errors.New("app: the coordinator needs every port")
	}
	if opts.DailyCap <= 0 || opts.Grace <= 0 || opts.SettleWait < 0 || opts.SettlePoll <= 0 || opts.Retry <= 0 {
		return nil, errors.New("app: the coordinator needs a daily cap, a grace, a settling poll and a retry above zero")
	}
	c := &Coordinator{p: *p, opts: *opts, log: opts.Log, ctx: context.Background(), runs: map[board.RunID]*running{}, retries: map[item.ID]func(){}}
	// A coordinator acts once it takes over.
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	return c, nil
}

// missing reports whether a port is unset.
func (p *Ports) missing() bool {
	return p.Records == nil || p.Boards == nil || p.Spend == nil || p.Revisions == nil || p.Sessions == nil ||
		p.Turns == nil || p.Clock == nil || p.Timers == nil || p.IDs == nil
}

// OpenInput is a work item to open.
type OpenInput struct {
	Type  string
	Title string
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

// Open opens a work item and starts it at once (Plan 7b queues items). An
// item opened once the day's spend reached the daily cap is not staffed. Its
// title is redacted with the rules that apply always before it is recorded.
// Routing is recorded as given, so routing that is out of bounds, or that
// those rules would change, is refused (see checkRouting).
// Once the item's opening is recorded, Open returns its id: what the item's
// start does next is tried again until it succeeds. The opening acts for the
// current term: its writes carry the term's epoch.
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
	rev := c.p.Revisions.Current()
	if rev == nil {
		return "", errors.New("app: the registry has no configuration yet")
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
	day, err := c.p.Spend.SpentToday(ctx)
	if err != nil {
		return "", fmt.Errorf("read the day's spend: %w", err)
	}
	s := &item.State{ID: item.ID(c.p.IDs.NewID()), Incident: item.IncidentID(c.p.IDs.NewID())}
	bodies := []item.Body{{Opened: &item.Opened{
		Type: in.Type, Title: title(rev, in.Title), Source: in.Source, Routing: maps.Clone(in.Routing),
		Principal: in.Principal, Responders: slices.Clone(in.Responders), Arm: item.ArmVisible, How: item.NoWindow,
	}}}
	if day >= c.opts.DailyCap {
		bodies = append(bodies, item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}})
	}
	if err := c.record(ctx, s, "", bodies...); err != nil {
		c.failed(ctx, s.ID, err)
		return "", err
	}
	// The item's start is the coordinator's from here on, not the request's.
	c.advance(c.ctx, s.ID)
	return s.ID, nil
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

// TakeOver has the coordinator act under term, the lease's new holding
// (spec Section 8.2), until it is fenced: it stops the turns and ends the
// sessions of any earlier holding, and carries on every item that does not
// await its close, from its records. Before it acts on any item, it reads
// every such item's records, and its run's board. It skips each item whose
// records or board events are of a later version than this build reads
// (item.ErrVersion, board.ErrVersion): the item is logged once and left
// alone, and the other items are carried on (Decision 19; see skip). It
// refuses the lease, and holds nothing, when a load fails: a record or event
// of a later version may be among those it could not load, and an item's next
// step may write a record before it reads the board, so its caller gives the
// lease up and takes it again to read them. Invalid records and boards do not
// refuse it, those that fold into an invalid state and those whose body does
// not decode, which a load reports as item.ErrInvalid or board.ErrInvalid:
// that item's steps fail and are tried again, and the other items go on. A
// round in progress gets fresh sessions and turns for the agents that have
// not ended theirs, and ends unseen when it is taken up with less than a
// third of its span left (see hold); a round whose deadline passed before
// this instance acted on it ends as missed says; a decision recorded is
// carried out. Steps that timers run later use ctx, which the holding ends.
// Every write of the takeover, and of those steps, carries term's epoch. A
// takeover whose holding ended before it began, or that finds the lease gone,
// acts on no item after it, and says so.
func (c *Coordinator) TakeOver(ctx context.Context, term Term) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fence()
	ctx = holding.WithEpoch(ctx, term.Epoch)
	ids, err := c.p.Records.Active(ctx)
	if err != nil {
		return fmt.Errorf("take over: %w", err)
	}
	skips := map[item.ID]error{}
	for _, id := range ids {
		switch err := c.readable(ctx, id); {
		case ofLaterVersion(err):
			skips[id] = err
		case err != nil && !corrupt(err):
			return fmt.Errorf("take over: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("take over: %w", err)
	}
	c.held, c.term, c.ctx = true, term, ctx
	for _, id := range ids {
		if err := skips[id]; err != nil {
			c.skip(ctx, id, err)
		}
	}
	for _, id := range ids {
		if !c.held || ctx.Err() != nil {
			break
		}
		c.advance(ctx, id)
	}
	if !c.held {
		return fmt.Errorf("take over: %w", ErrLeaseLost)
	}
	return ctx.Err()
}

// readable reads an item's records, and its run's board, as a takeover does
// before it acts on any item.
func (c *Coordinator) readable(ctx context.Context, id item.ID) error {
	s, err := c.load(ctx, id)
	if err != nil || s.Run == nil {
		return err
	}
	return c.p.Boards.Readable(ctx, s.Run.ID)
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
// item and holding, and drops what the coordinator holds of the item's run:
// its sessions, which end, its agents' turns, which stop, and its timers,
// settle's own retry among them. Only this holding's sessions and turns can
// be live: its takeover ended every earlier holding's. No retry of the item
// is pending when it is skipped (advance cancels its own first), and none is
// armed again: advance passes the item by until the next takeover reads it
// again.
func (c *Coordinator) skip(ctx context.Context, id item.ID, err error) {
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
// must: its turns stop, its sessions end and its timers are cancelled. It
// acts again only once it takes over, and skips no item until then.
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
	clear(c.runs)
	clear(c.retries)
	c.skipped.Store(nil)
}

// Exited reports how an agent's turn ended. An end reported twice, late, for
// a round that is over, of another turn, or as a class the runner does not
// report changes nothing. A turn reported interrupted counts as the deadline
// counts it when this instance started it with less than a third of the
// round's span left, or never knew it started: as unstarted. The runner stops
// a turn at the deadline too, and its report may come before this instance's
// deadline does. Any other end counts as reported. A round ends once every
// turn ended, unless each ended at the model vendor: such a round ends at its
// deadline, so that a vendor's outage cannot spend the run's rounds in
// seconds.
func (c *Coordinator) Exited(ctx context.Context, e Exit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.held {
		return
	}
	r := c.runs[e.Run]
	if r == nil || r.round != e.Round || r.ended != nil {
		return
	}
	starting := false
	if id, ok := r.turns[e.Agent]; !ok || id != e.Turn {
		// A turn whose Start failed may have started after all, if the
		// runner's answer was what was lost, and end before the retry: its
		// end counts, by the id the retry would use.
		if held := r.starting[e.Agent]; held == nil || held.ID != e.Turn {
			return
		}
		starting = true
	}
	if !reported(e.Class) {
		c.log.WarnContext(ctx, "a turn's end of a class the runner does not report is ignored", "run", e.Run, "agent", e.Agent, "class", e.Class)
		return
	}
	class := e.Class
	if class == item.TurnInterrupted && (starting || r.startedLate[e.Agent]) {
		class = item.TurnUnstarted
	}
	delete(r.turns, e.Agent)
	delete(r.starting, e.Agent)
	r.exits[e.Agent] = class
	c.whenAllEnded(e.Run, r)
}

// reported reports whether the runner reports a turn's end as class.
func reported(class item.TurnClass) bool {
	switch class {
	case item.TurnEnded, item.TurnFinished, item.TurnInterrupted, item.TurnRefused, item.TurnVendor, item.TurnCrashed:
		return true
	}
	return false
}

func (c *Coordinator) whenAllEnded(run board.RunID, r *running) {
	if len(r.turns) > 0 || len(r.starting) > 0 {
		return
	}
	for _, class := range r.exits {
		if class != item.TurnVendor {
			c.end(run, r, item.EndedByTurns, c.p.Clock.Now())
			return
		}
	}
}

// deadline ends a round at its deadline: the turns still running are
// stopped, and count as interrupted, but those started with less than a
// third of the round's span left count, as those the runner never started
// do, as unstarted.
func (c *Coordinator) deadline(run board.RunID, round int, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[run]
	if !c.held || r == nil || r.round != round || r.ended != nil {
		return
	}
	for agent := range r.turns {
		c.p.Turns.Stop(run, agent)
		r.exits[agent] = item.TurnInterrupted
		if r.startedLate[agent] {
			r.exits[agent] = item.TurnUnstarted
		}
	}
	for agent := range r.starting {
		c.p.Turns.Stop(run, agent)
		r.exits[agent] = item.TurnUnstarted
	}
	clear(r.turns)
	clear(r.starting)
	c.end(run, r, item.EndedByDeadline, at)
}

// end marks a round ended, and lets its write calls have their grace before
// its end is decided. A round this instance took up too late ends unseen,
// however it ends.
func (c *Coordinator) end(run board.RunID, r *running, by item.EndedBy, at time.Time) {
	r.ended = &ended{by: by, at: at, unseen: r.late}
	round := r.round
	c.arm(r, c.p.Clock.Now().Add(c.opts.Grace), func() { c.settle(run, round) })
}

// settle waits for the run's model calls in flight, up to the settling
// wait, then decides the round's end. An end that meets a later version
// skips the item, and settle arms no retry of its own (see skip).
func (c *Coordinator) settle(run board.RunID, round int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[run]
	if !c.held || r == nil || r.round != round || r.ended == nil {
		return
	}
	again := func(after time.Duration) { c.arm(r, c.p.Clock.Now().Add(after), func() { c.settle(run, round) }) }
	n, err := c.p.Spend.InFlight(c.ctx, string(run))
	if err == nil {
		if n > 0 && c.p.Clock.Now().Before(r.ended.at.Add(c.opts.Grace+c.opts.SettleWait)) {
			again(c.opts.SettlePoll)
			return
		}
		if err = c.endRound(c.ctx, run, r, n == 0); err == nil {
			c.advance(c.ctx, r.item)
			return
		}
	}
	if ofLaterVersion(err) {
		c.skip(c.ctx, r.item, err)
		return
	}
	c.failed(c.ctx, r.item, err)
	if c.held {
		again(c.opts.Retry)
	}
}

// endRound records how a round ended and what follows it. Before it
// decides, it notices on the board each model call of the run that was
// refused, failed or whose outcome is unknown, once per call. A round whose
// end is recorded already is left as it is.
func (c *Coordinator) endRound(ctx context.Context, run board.RunID, r *running, settled bool) error {
	s, err := c.load(ctx, r.item)
	if err != nil {
		return err
	}
	if l := s.Run.Latest(); l == nil || l.Ended != nil || l.Opened.Round != r.round {
		return nil
	}
	spent, err := c.p.Spend.Spent(ctx, string(run))
	if err != nil {
		return fmt.Errorf("read the run's spend: %w", err)
	}
	day, err := c.p.Spend.SpentToday(ctx)
	if err != nil {
		return fmt.Errorf("read the day's spend: %w", err)
	}
	failed, err := c.p.Spend.Failed(ctx, string(run))
	if err != nil {
		return fmt.Errorf("read the run's failed model calls: %w", err)
	}
	report, err := c.p.Boards.Report(ctx, run, r.round)
	if err != nil {
		return err
	}
	// The calls noticed in the round are those the report holds, which an
	// earlier try of this end may have posted, and those noticed now.
	noticed := report.Facts.FailedCalls
	for _, call := range failed {
		if report.Noticed(call.ID) {
			continue
		}
		n := &board.Notice{Code: noticeCode(call.Status), Agent: call.Agent, CallID: call.ID}
		if _, err := c.p.Boards.Notice(ctx, run, board.Coordinator, n); err != nil {
			return err
		}
		noticed++
	}
	re := s.Run.End(&item.Ending{
		EndedBy: r.ended.by, EndedAt: r.ended.at, Turns: maps.Clone(r.exits), Spent: spent, Settled: settled,
		DaySpent: day, DailyCap: c.opts.DailyCap, FailedCalls: noticed, Report: report, Unseen: r.ended.unseen,
	})
	if err := c.record(ctx, s, run, item.Body{RoundEnded: &re}); err != nil {
		return err
	}
	c.cancel(r)
	return nil
}

// noticeCode is the notice of a model call's outcome.
func noticeCode(status string) board.NoticeCode {
	switch status {
	case "refused":
		return board.NoticeModelRefused
	case "error":
		return board.NoticeModelFailed
	}
	return board.NoticeModelUnknown
}

// advance takes an item through every step its records allow now, until it
// waits for a round's end or awaits its close. A step that fails is tried
// again after the retry wait, but one that meets a later version skips the
// item, which advance passes by from then on (see skip). The lifecycle's
// rules refuse to record after a record of a later version, so that no step
// writes past one.
func (c *Coordinator) advance(ctx context.Context, id item.ID) {
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
				c.retries[id] = c.p.Timers.At(c.p.Clock.Now().Add(c.opts.Retry), func() {
					c.mu.Lock()
					defer c.mu.Unlock()
					if c.held {
						c.advance(c.ctx, id)
					}
				})
			}
		}
		return
	}
	c.failed(ctx, id, errors.New("app: the item took more steps at once than an item can"))
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

// step takes an item's next step, and reports whether it must wait: for a
// round's end, or for the item's close.
func (c *Coordinator) step(ctx context.Context, s *item.State) (wait bool, err error) {
	switch {
	case s.Closing != nil:
		c.forget(s)
		return true, nil
	case s.Started == nil:
		return false, c.start(ctx, s)
	case s.Run == nil:
		return false, c.create(ctx, s)
	case s.Run.Stopped != nil:
		return false, c.close(ctx, s)
	}
	l := s.Run.Latest()
	switch {
	case l == nil && len(s.Run.Created.Agents) == 0:
		return false, c.unstaffed(ctx, s)
	case l == nil || l.Ended != nil && l.Ended.Decision.Next != item.NextStop:
		return false, c.open(ctx, s)
	case l.Ended == nil:
		if r := c.runs[s.Run.ID]; (r == nil || r.round != l.Opened.Round) && !c.p.Clock.Now().Before(l.Opened.Deadline) {
			return false, c.missed(ctx, s)
		}
		return true, c.hold(ctx, s)
	}
	a, err := c.p.Boards.Assess(ctx, s.Run.ID, time.Time{})
	if err != nil {
		return false, err
	}
	stop := s.Run.Stop(a.Conclusion)
	return false, c.record(ctx, s, s.Run.ID, item.Body{RunStopped: &stop})
}

// start locks the configuration the item runs under; an item whose type
// the current revision cannot run is not staffed.
func (c *Coordinator) start(ctx context.Context, s *item.State) error {
	rev := c.p.Revisions.Current()
	if rev == nil {
		return errors.New("app: the registry has no configuration yet")
	}
	if runnable(rev, s.Opened.Type) != nil {
		return c.record(ctx, s, "", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}})
	}
	lock, err := c.p.Revisions.Lock(rev)
	if err != nil {
		return fmt.Errorf("lock revision %s: %w", rev.Commit(), err)
	}
	return c.record(ctx, s, "", item.Body{Started: &item.Started{Commit: lock.Commit, Digest: lock.Digest, Images: lock.RuntimeImages}})
}

// create creates the item's run, the primary run that people see, with the
// settings and budget of its type, inviting the agents subscribed to it.
func (c *Coordinator) create(ctx context.Context, s *item.State) error {
	rev, err := c.p.Revisions.At(ctx, s.Started.Commit)
	if err != nil {
		return fmt.Errorf("read revision %s: %w", s.Started.Commit, err)
	}
	ws := rev.WorkItemType(s.Opened.Type).Settings()
	return c.record(ctx, s, board.RunID(c.p.IDs.NewID()), item.Body{RunCreated: &item.RunCreated{
		Class: item.ClassPrimary, Mode: item.ModeVisible, Settings: boardSettings(&ws),
		Budget:     item.Budget{WallClock: ws.Budget.WallClock, Tokens: ws.Budget.Tokens, Rounds: ws.Budget.Rounds},
		RoundLimit: ws.RoundLimit, Agents: invited(rev, s.Opened.Type, ws.MaxAgents),
	}})
}

// unstaffed stops a run with no agent to invite, and says so on its board.
func (c *Coordinator) unstaffed(ctx context.Context, s *item.State) error {
	if _, err := c.p.Boards.Notice(ctx, s.Run.ID, board.Coordinator, &board.Notice{Code: board.NoticeNoAgents}); err != nil {
		return err
	}
	stop := s.Run.Stop("")
	return c.record(ctx, s, s.Run.ID, item.Body{RunStopped: &stop})
}

// open opens the run's next round as its last decided, or its first after
// the evidence the item opened with.
func (c *Coordinator) open(ctx context.Context, s *item.State) error {
	if s.Run.Latest() == nil {
		rev, err := c.p.Revisions.At(ctx, s.Started.Commit)
		if err != nil {
			return fmt.Errorf("read revision %s: %w", s.Started.Commit, err)
		}
		in := &board.PlatformEvidenceInput{Claim: triggerClaim(rev, s.Opened), Ref: "item:" + string(s.ID), Source: board.Source(s.Opened.Source)}
		if _, err := c.p.Boards.PostPlatformEvidence(ctx, s.Run.ID, in); err != nil {
			return err
		}
	}
	o := s.Run.Open(c.p.Clock.Now())
	return c.record(ctx, s, s.Run.ID, item.Body{RoundOpened: &o})
}

// hold acts on the run's open round, once per round on this instance: it
// opens the round on the board, then gives the agents' sessions new tokens
// and puts them in it, ends those of agents that left, starts the turns of
// the agents that have not ended theirs, and arms the round's deadline. A
// round it takes up with less than a third of its span left is held as
// usual, so that its agents use the time that is left, but it ends unseen
// (item.Ending): turns that the deadline interrupts at once cannot tell a
// round no agent had time for from one that found nothing.
func (c *Coordinator) hold(ctx context.Context, s *item.State) error {
	run := s.Run
	o := &run.Latest().Opened
	r := c.runs[run.ID]
	if r == nil {
		r = &running{item: s.ID, tokens: map[string]Tokens{}}
		c.runs[run.ID] = r
	}
	if r.round == o.Round {
		return nil
	}
	if _, err := c.p.Boards.StartRound(ctx, run.ID, &board.RoundStart{Round: o.Round, WrapUp: o.WrapUp}); err != nil {
		return err
	}
	report, err := c.p.Boards.Report(ctx, run.ID, o.Round)
	if err != nil {
		return err
	}
	rev, err := c.p.Revisions.At(ctx, s.Started.Commit)
	if err != nil {
		return fmt.Errorf("read revision %s: %w", s.Started.Commit, err)
	}
	for agent := range r.tokens {
		if !slices.Contains(o.Agents, agent) {
			c.p.Sessions.End(run.ID, agent)
			delete(r.tokens, agent)
		}
	}
	for _, agent := range o.Agents {
		// Each round's sessions have tokens of their own: a turn of an
		// earlier round still running cannot act in this one. The
		// gateways' writes for them act for this term.
		r.tokens[agent] = c.p.Sessions.Issue(&Session{Run: run.ID, Agent: agent, Commit: s.Started.Commit, Item: s.Opened.Routing, Epoch: c.term.Epoch})
		if err := c.p.Sessions.Move(run.ID, agent, o.Round, c.p.Clock.Local(o.Deadline), o.Budget); err != nil {
			return err
		}
	}
	c.cancel(r)
	r.round, r.opened, r.deadline, r.ended = o.Round, o.OpenedAt, o.Deadline, nil
	r.late = r.tooLate(c.p.Clock.Now())
	r.turns, r.starting, r.exits = map[string]string{}, map[string]*Turn{}, map[string]item.TurnClass{}
	r.startedLate = map[string]bool{}
	for _, agent := range o.Agents {
		if slices.Contains(report.Facts.Ended, agent) {
			r.exits[agent] = item.TurnEnded
			continue
		}
		c.begin(ctx, run.ID, r, c.turn(s, rev, o, agent, r.tokens[agent]))
	}
	round, deadline := o.Round, o.Deadline
	c.arm(r, deadline, func() { c.deadline(run.ID, round, deadline) })
	c.whenAllEnded(run.ID, r)
	return nil
}

// tooLate reports whether the round has less than a third of its span left
// at now, the span from its opening to its deadline.
func (r *running) tooLate(now time.Time) bool {
	return r.deadline.Sub(now) < r.deadline.Sub(r.opened)/lateShare
}

// agents returns, sorted, the agents of the run that this instance issued
// sessions to, started turns for, or tries to start turns for.
func (r *running) agents() []string {
	agents := slices.Collect(maps.Keys(r.tokens))
	agents = slices.AppendSeq(agents, maps.Keys(r.turns))
	agents = slices.AppendSeq(agents, maps.Keys(r.starting))
	slices.Sort(agents)
	return slices.Compact(agents)
}

// begin starts a turn. A turn the runner could not start is tried again
// after the retry wait, with the same id, while its round lasts. A turn
// started with less than a third of the round's span left, as when the
// runner is back late, counts as unstarted if it still runs at the deadline
// (see deadline), or if the runner reports it interrupted (see Exited): it
// had no time to find anything. A round taken up too late is unseen already,
// and its turns count as they did.
func (c *Coordinator) begin(ctx context.Context, run board.RunID, r *running, t *Turn) {
	if err := c.p.Turns.Start(ctx, t); err != nil {
		c.failed(ctx, r.item, fmt.Errorf("start the turn of %s: %w", t.Agent, err))
		r.starting[t.Agent] = t
		round, agent := r.round, t.Agent
		c.arm(r, c.p.Clock.Now().Add(c.opts.Retry), func() { c.startAgain(run, round, agent) })
		return
	}
	delete(r.starting, t.Agent)
	r.turns[t.Agent] = t.ID
	if !r.late && r.tooLate(c.p.Clock.Now()) {
		r.startedLate[t.Agent] = true
	}
}

// startAgain tries again to start a turn the runner could not start, while
// its round lasts.
func (c *Coordinator) startAgain(run board.RunID, round int, agent string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.runs[run]
	if r == nil || r.round != round || r.ended != nil || r.starting[agent] == nil || !c.p.Clock.Now().Before(r.deadline) {
		return
	}
	c.begin(c.ctx, run, r, r.starting[agent])
}

// missed ends a round whose deadline passed before this instance acted on
// it, once its board entry is posted, as its holder may have died before it
// was. Such a round is unseen: never quiet, and, as a wrap-up round, it
// concludes nothing (item.Ending). A round whose deadline passed between the last holding's expiry and
// this holding's start, while no instance held the lease, ends by an
// outage: none of its turns counts, and it counts toward no budget. Any
// other ends by its deadline: the turns that ended on the board ended; the
// others were interrupted by the last holding's deadline, or never started,
// when this holding could not act on the round before its deadline.
func (c *Coordinator) missed(ctx context.Context, s *item.State) error {
	run, o := s.Run.ID, &s.Run.Latest().Opened
	if _, err := c.p.Boards.StartRound(ctx, run, &board.RoundStart{Round: o.Round, WrapUp: o.WrapUp}); err != nil {
		return err
	}
	n, err := c.p.Spend.InFlight(ctx, string(run))
	if err != nil {
		return fmt.Errorf("read the run's model calls in flight: %w", err)
	}
	r := &running{
		item: s.ID, round: o.Round, deadline: o.Deadline, tokens: map[string]Tokens{}, turns: map[string]string{}, starting: map[string]*Turn{},
		exits: map[string]item.TurnClass{}, ended: &ended{by: item.EndedByDeadline, at: o.Deadline, unseen: true},
	}
	if o.Deadline.After(c.term.Previous) && !o.Deadline.After(c.term.At) {
		r.ended.by = item.EndedByOutage
	} else {
		report, err := c.p.Boards.Report(ctx, run, o.Round)
		if err != nil {
			return err
		}
		rest := item.TurnInterrupted
		if o.Deadline.After(c.term.At) {
			rest = item.TurnUnstarted
		}
		for _, agent := range o.Agents {
			r.exits[agent] = rest
			if slices.Contains(report.Facts.Ended, agent) {
				r.exits[agent] = item.TurnEnded
			}
		}
	}
	old := c.runs[run]
	if old != nil {
		c.cancel(old)
		r.tokens = old.tokens
	}
	c.runs[run] = r
	if err := c.endRound(ctx, run, r, n == 0); err != nil {
		// The round is left as this instance found it, so that the retry
		// ends it again.
		if old != nil {
			c.runs[run] = old
		} else {
			delete(c.runs, run)
		}
		return err
	}
	return nil
}

// close acts on a run that stopped: its sessions and turns end, then the
// stop entry, with the conclusion its record holds, ends its board, which
// may take tries, and the item awaits its close, with the run's reason as
// the item's.
func (c *Coordinator) close(ctx context.Context, s *item.State) error {
	run := s.Run
	st := run.Stopped
	c.p.Sessions.EndRun(run.ID)
	if l := run.Latest(); l != nil {
		for _, agent := range l.Opened.Agents {
			c.p.Turns.Stop(run.ID, agent)
		}
	}
	in := &board.StopInput{Reason: string(st.Reason), Conclusion: st.Conclusion, Rounds: st.Rounds, WallClock: st.WallClock, Tokens: st.Spent}
	if _, err := c.p.Boards.Stop(ctx, run.ID, in); err != nil {
		return err
	}
	return c.record(ctx, s, "", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.ItemReason(st.Reason)}})
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

// turn is an agent's turn in a round.
func (c *Coordinator) turn(s *item.State, rev *config.Revision, o *item.RoundOpened, agent string, t Tokens) *Turn {
	turn := &Turn{
		ID: c.p.IDs.NewID(), Run: s.Run.ID, Agent: agent, Round: o.Round, WrapUp: o.WrapUp, Deadline: c.p.Clock.Local(o.Deadline),
		MCP: t.MCP, LLM: t.LLM,
	}
	if a := rev.Agent(agent); a != nil {
		turn.Image, turn.Model, turn.Skills = s.Started.Images[a.Spec.Runtime], a.Spec.Model, slices.Clone(a.Spec.Skills)
	}
	turn.Prompt = prompt(turn, roundsLeft(s.Run))
	return turn
}

// roundsLeft is how many normal rounds the run's budget allows after its open
// round: rounds that ended by an outage count toward no budget, so a round's
// number does not say it.
func roundsLeft(run *item.Run) int {
	used := 0
	for i := len(run.Rounds) - 1; i >= 0; i-- {
		if e := run.Rounds[i].Ended; e != nil {
			used = e.Rounds
			break
		}
	}
	return max(run.Created.Budget.Rounds-used-1, 0)
}

// prompt is a turn's opening message. It holds the platform's words and ids
// only: what agents and people wrote reaches the model through read_board,
// which marks it as data.
func prompt(t *Turn, left int) string {
	if t.WrapUp != "" {
		return fmt.Sprintf("Round %d is the wrap-up round of this investigation: hypothesis %s converged. Read the board with read_board. "+
			"Post evidence only if it refutes that hypothesis, then call end_turn.", t.Round, t.WrapUp)
	}
	next := fmt.Sprintf("at most %d more rounds follow it", left)
	switch left {
	case 0:
		next = "it is the last"
	case 1:
		next = "at most one more round follows it"
	}
	return fmt.Sprintf("Round %d of this investigation: %s. Read the board with read_board, investigate with your tools, "+
		"post what you find, and call end_turn when you are done.", t.Round, next)
}

// load folds an item's records.
func (c *Coordinator) load(ctx context.Context, id item.ID) (*item.State, error) {
	records, err := c.p.Records.Load(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load item %s: %w", id, err)
	}
	return item.Fold(records)
}

// record appends records of the item, each checked first against the
// item's records as committed: the lifecycle's rules refuse a record out of
// order, as a decision made twice would be.
func (c *Coordinator) record(ctx context.Context, s *item.State, run board.RunID, bodies ...item.Body) error {
	_, err := c.p.Records.Append(ctx, s.ID, func(history []item.Record) ([]item.Draft, error) {
		check, err := item.Fold(history)
		if err != nil {
			return nil, err
		}
		drafts := make([]item.Draft, 0, len(bodies))
		for i := range bodies {
			b := bodies[i]
			b.V = item.Version
			d := item.Draft{ID: c.p.IDs.NewID(), Item: s.ID, Incident: s.Incident, Run: run, Body: b}
			r := item.Record{Seq: int64(len(history) + i + 1), ID: d.ID, Item: d.Item, Incident: d.Incident, Run: run, Body: b}
			if err := check.Apply(&r); err != nil {
				return nil, err
			}
			drafts = append(drafts, d)
		}
		return drafts, nil
	})
	if err != nil {
		return fmt.Errorf("record item %s: %w", s.ID, err)
	}
	return nil
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
// title whose escapes may hide what a rule matches is replaced whole.
func title(rev *config.Revision, raw string) string {
	red := always(rev)
	if red.Escaped(raw) {
		return "[a title the redaction rules could not read]"
	}
	return cut(red.Text(strings.TrimSpace(raw)), maxTitleRunes)
}

// checkRouting refuses routing that cannot be recorded as given and quoted on
// the board. Write tools' scope bindings read the values, so they are recorded
// as given, and a value that redaction would change is no value (spec Section
// 3.6). It refuses more than maxRoutingKeys keys, a key that is empty or of
// more than maxRoutingKeyRunes characters, and a value of more than
// maxRoutingValueRunes. It tests each key and each value alone, and both as
// the member key=value, which the opening's claim on the board holds, against
// the rules that apply always, and refuses routing that those rules would
// change in any of the three, or whose escapes may hide what they remove: a
// rule for password=... changes the member of the routing
// {"password": "..."}, though neither its key nor its value alone. The error
// never quotes routing: it may be a secret.
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
