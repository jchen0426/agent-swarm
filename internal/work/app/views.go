package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// ErrNoItem marks a view of an item that has no records.
var ErrNoItem = errors.New("app: no such item")

// View is an item as the admin API shows it (Contract C4), read from the
// stores, off the coordinator's lock.
type View struct {
	ID item.ID
	// State is the item's state: queued, investigating, awaiting_close or
	// closed.
	State string
	// Stop is why the item's run stopped, or, for an item that awaits its
	// close without a run, as one opened at the daily cap does, why it does:
	// empty while the run goes on, and for a closed item that never had a run.
	Stop string
	// Stops counts the run's stops so far, from the records' fold: its
	// earlier stops, each lifted by a resume, and its stop now, while it is
	// stopped (item.Run.Stops, Stopped); 0 for an item with no run. Set with
	// Stop, so that both change in the one version that stops the run (Task
	// 5's re-review N1).
	Stops int
	// HeldByLaterBuild says that a later build wrote a record of the item, or
	// an event of its run's board, that this build cannot read (Decision 19):
	// a view that says so says nothing else.
	HeldByLaterBuild bool
	// Rounds are the run's rounds, in order, the open one among them.
	Rounds []RoundView
	// Incident, Type and OpenedAt are the item's incident, its work item type,
	// and when it opened, every item's.
	Incident item.IncidentID
	Type     string
	OpenedAt time.Time
	// QueuePosition is a waiting item's place in the queue, from 1, among the
	// queued items neither admitted nor promoted, in the queue's order (R17);
	// 0 for any other item, as for every item on a standby, whose queue is
	// empty.
	QueuePosition int
	// NotStaffed is why an item that awaits its close not staffed is: its
	// record's cause, or kill_switch for an item whose run the kill switch
	// stopped (R18); empty for any other item.
	NotStaffed item.Cause
	// AutoClosePaused says that an item awaiting its close does not close by
	// itself while the kill switch is on (Decision 8). ClosesAt is when such an
	// item closes by itself, while its auto-close is not paused and its delay
	// can be read; zero otherwise. AutoCloseUnread says that the kill switch's
	// table could not be read, so that neither is known (R28).
	AutoClosePaused bool
	ClosesAt        time.Time
	AutoCloseUnread bool
	// ClosesAtUnread says that the item's auto-close delay could not be read
	// (the revision it needs), so that ClosesAt is zero though the auto-close
	// is not paused (I15).
	ClosesAtUnread bool
	// Closed is a closed item's close; nil for any other item.
	Closed *ClosedView
	// StopEntry is the stop entry of the run's board, read from the same
	// replay as the rounds; nil while the run goes on and for an item with no
	// run.
	StopEntry *board.Stop
	// IncidentItems are the items of the item's incident, itself among them,
	// sorted (Decision 18): those whose latest record is in it, as Incidents
	// reads them. Never nil for a view read from the stores.
	IncidentItems []item.ID
	// Budget is the run's budget and what it used (Decision 17); nil for an
	// item with no run.
	Budget *BudgetView
	// ExtensionOffered is the extension the interface offers for the item's
	// run, ViewPorts.Extension with each dimension clamped to the run's
	// creation's budget, so that the offer is never one Extend refuses as
	// above it (R71); zero for an item with no run. It names rounds and a
	// wall clock, never tokens, which an extension then takes as the rounds'
	// share.
	ExtensionOffered item.Budget
}

// BudgetView is a run's budget as the item's view gives it (Contract C16):
// its creation's plus every extension's (item.Run.Budget), and what the run
// used, as its latest ended round recorded it, zero before its first round
// ends.
type BudgetView struct{ Budget, Used item.Budget }

// Row is an item as the items list shows it: its record's fold alone, no
// board and no round.
type Row struct {
	ID    item.ID
	Title string
	// Service is the item's routing "service", empty when it has none.
	Service    string
	Principal  string
	Responders []string
	State      string
	// QueuePosition is as View.QueuePosition; PositionKnown says that this
	// instance holds the lease, so that its snapshot of the queue is the
	// queue's: a standby's rows say it is not, and their position is 0.
	QueuePosition int
	PositionKnown bool
	// ClosedAt and ReopenableUntil are a closed item's close and until when
	// it may be reopened; zero for any other item.
	ClosedAt        time.Time
	ReopenableUntil time.Time
	// ClosesAt, ClosesAtUnread, AutoClosePaused and AutoCloseUnread are as the
	// View's: a zero ClosesAt with neither flag set is an item that does not
	// await its close.
	ClosesAt         time.Time
	ClosesAtUnread   bool
	AutoClosePaused  bool
	AutoCloseUnread  bool
	HeldByLaterBuild bool
	// Unreadable says that the item's records are invalid (see corrupt): the
	// row holds its id alone, and the item is logged once.
	Unreadable bool
	Incident   item.IncidentID
}

// ClosedView is a closed item's close (Contract C4): when, by whom, empty for
// a close by itself, which Auto says; and until when a person may reopen the
// item, and may make the rulings a closed item takes.
type ClosedView struct {
	At               time.Time
	By               string
	Auto             bool
	ReopenableUntil  time.Time
	LateRulingsUntil time.Time
}

// RoundView is a round as the admin API shows it: how it opened, how it
// ended and the evidence its end read, as RoundEnded records them (Decision
// 9), and the board's report of it. A round still open has ended nothing:
// its report is the board's so far.
type RoundView struct {
	Round  int
	WrapUp bool
	// Ended is how the round ended, as RoundEnded records it: turns, deadline
	// or outage; empty while it is open.
	Ended string
	Quiet bool
	// TurnEnds lists the agents whose own turn end the board recorded in the
	// round.
	TurnEnds []string
	// FailedCalls counts the round's failed model calls, and FailedToolCalls
	// its failed tool calls; Settled says no model call of it was in flight
	// once the wait for them ended.
	FailedCalls     int
	FailedToolCalls int
	Settled         bool
	Report          board.Report
}

// ViewPorts are what the views read besides the work records and the boards
// (Decision 14): the coordinator's snapshot, for the queue; the kill switch's
// table, which they only read, so that a standby shows the switch, and an
// auto-close it pauses, too; the configuration's revisions, for an
// auto-close delay recorded as zero; and the deployment's windows of a closed
// item (Decision 15).
type ViewPorts struct {
	// Lifecycle is the coordinator's snapshot (Coordinator.Lifecycle), a
	// standby's empty.
	Lifecycle func() Lifecycle
	// Controls reads the kill switch from its table: its reads alone, since
	// serve gives the views the table unguarded, which no lease check guards
	// a write to, so that the views have no way to write it.
	Controls interface {
		KillSwitch(ctx context.Context) (KillSwitch, error)
	}
	// Revisions give an item's auto-close delay that its records hold as zero
	// (Decision 8).
	Revisions Revisions
	// ReopenWindow is how long after its latest close a person may reopen an
	// item, and ClosedRulings how long after it a closed item takes the
	// rulings a closed item takes.
	ReopenWindow  time.Duration
	ClosedRulings time.Duration
	// Closed lists the items closed within the reopen window, for the items
	// list (R92).
	Closed ClosedLister
	// Incidents reads an incident's items, for the item's view (R102; D16).
	Incidents Incidents
	// Extension is the extension the interface offers, the deployment's
	// lifecycle.extension [1 round and 5 min] (Decision 17): its rounds and
	// wall clock.
	Extension item.Budget
	// Held says whether this instance holds the lease (m5): when it does not,
	// the coordinator's snapshot of the queue is empty, and the list's rows
	// say their position is not known. Nil is taken as not holding.
	Held func() bool
	// Visible decides whether an item's board is shown (R144); nil is
	// Visible, which shows every item until Plan 8a.
	Visible func(*View) bool
	// Titles reads the titles of the trigger events a board's feed shows
	// from the inbound event store (design D8; R151); nil reads none, as if
	// the store kept none.
	Titles InboundTitles
	// Waits are a round end's waits, the coordinator's, by which a board
	// shows the latest time its run stops (design D5).
	Waits item.Waits
	// Entries reads the boards' entries by their ids, and Siblings lists an
	// incident's other items, for the board watch's incident strip (design
	// D7; R172).
	Entries  EntryReader
	Siblings Siblings
	// Log receives the views' logs; nil discards them.
	Log *slog.Logger
}

// Views reads items as the admin API shows them, from the stores: the work
// records and the boards, and, of the lifecycle, the coordinator's snapshot
// and the kill switch's table. It takes no lock of the coordinator's, and
// writes nothing.
type Views struct {
	records Records
	boards  *Boards
	p       ViewPorts
	log     *slog.Logger
	// logged holds the items whose unread auto-close delay was logged, so
	// that a page that lists them often logs each once.
	mu     sync.Mutex
	logged map[logged]struct{}
}

// logged keys the once-set: an item and the failure logged for it.
type logged struct {
	id     item.ID
	reason string
}

// NewViews returns the views of the items records keeps, whose runs' boards
// boards reads, and whose lifecycle p gives.
func NewViews(records Records, boards *Boards, p *ViewPorts) *Views {
	log := p.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Views{records: records, boards: boards, p: *p, log: log, logged: map[logged]struct{}{}}
}

// visible says whether view's board is shown, as the ports' predicate says,
// Visible when they give none (R144).
func (v *Views) visible(view *View) bool {
	if v.p.Visible == nil {
		return Visible(view)
	}
	return v.p.Visible(view)
}

// KillSwitch reads the kill switch's state from its table, as the status
// shows it, a standby's included (Decision 14).
func (v *Views) KillSwitch(ctx context.Context) (KillSwitch, error) {
	return v.p.Controls.KillSwitch(ctx)
}

// UnstaffedBy returns the items whose item_awaiting_close names the kill
// switch's on-row seq, the items that on-row left not staffed, as the
// records hold them (Records.UnstaffedBy): those an off would list, which
// the web's off shows as its preview (Plan 9a-1, M7). It reads the records,
// not the coordinator, so that any instance shows them.
func (v *Views) UnstaffedBy(ctx context.Context, seq int64) ([]item.ID, error) {
	return v.records.UnstaffedBy(ctx, seq)
}

// Item folds an item's records and reads its board's report for each round
// (see load and projected), and gives its place in the lifecycle now (see
// lifecycle) and the items of its incident. An item with a record or board
// event of a later version is a View with HeldByLaterBuild alone. An id with
// no records fails with ErrNoItem; an item whose records or board this build
// reads but cannot fold fails with what the fold or the replay found.
func (v *Views) Item(ctx context.Context, id item.ID) (View, error) {
	l, held, err := v.load(ctx, id, nil)
	switch {
	case err != nil:
		return View{}, err
	case held:
		return View{ID: id, HeldByLaterBuild: true}, nil
	}
	items, err := v.p.Incidents.IncidentItems(ctx, l.s.Incident)
	if err != nil {
		return View{}, fmt.Errorf("read the items of incident %s of item %s: %w", l.s.Incident, id, err)
	}
	view := v.projected(l.s, l.b)
	v.lifecycle(ctx, l.s, &view)
	view.IncidentItems = append([]item.ID{}, items...)
	return view, nil
}

// loaded is an item as Item and Board read it: its records, as the version
// read keeps them, their fold, and its run's board, nil with no run.
type loaded struct {
	records []item.Record
	s       *item.State
	b       *board.State
}

// load reads an item as Item and Board both read it: its records and their
// fold, then its run's board, read once, so that every round's report comes
// from the same reading of it. With at set, the records and the board are
// those up to at's seqs (R140; see recordsAt and Boards.stateAt), and the run
// the records name must be at's, or the item moved under it (ErrMoved). An
// id with no records fails with ErrNoItem. An item with a record or a board
// event of a later version is held by a later build (Decision 19): held,
// and nothing else. Any other failure is returned as the load, the fold or
// the board's read found it.
func (v *Views) load(ctx context.Context, id item.ID, at *BoardVersion) (l *loaded, held bool, err error) {
	records, err := v.records.Load(ctx, id)
	if err != nil {
		return nil, false, fmt.Errorf("load item %s: %w", id, err)
	}
	if len(records) == 0 {
		return nil, false, ErrNoItem
	}
	var mark *Mark
	if at != nil {
		if records, err = recordsAt(records, at.Record); err != nil {
			return nil, false, err
		}
		mark = &at.Board
	}
	s, err := item.Fold(records)
	switch {
	case ofLaterVersion(err):
		return nil, true, nil
	case err != nil:
		return nil, false, fmt.Errorf("fold item %s: %w", id, err)
	}
	if run := runOf(s); at != nil && run != at.Run {
		return nil, false, fmt.Errorf("%w: item %s's run up to seq %d is %q, not %q", ErrMoved, id, at.Record.Seq, run, at.Run)
	}
	b, err := v.boardOf(ctx, s, mark)
	switch {
	case ofLaterVersion(err):
		return nil, true, nil
	case err != nil:
		return nil, false, fmt.Errorf("read the board of item %s: %w", id, err)
	}
	return &loaded{records: records, s: s, b: b}, false, nil
}

// boardOf reads the board of s's run, as Boards.stateAt cuts it at at: nil
// for an item with no run, which reads nothing.
func (v *Views) boardOf(ctx context.Context, s *item.State, at *Mark) (*board.State, error) {
	if s.Run == nil {
		return nil, nil
	}
	_, b, err := v.boards.stateAt(ctx, s.Run.ID, at)
	return b, err
}

// projected is s's view as its records and its run's board, b, nil for an
// item with no run, determine it (R169), which Item and Board share: its
// state and stop, and its run's stops so far; why it is not staffed, if it
// awaits its close not staffed (R18); a closed item's close, with until when
// it may be reopened and take late rulings; and, of its run, its rounds with
// the board's report of each, its stop entry, its budget and the extension
// the interface offers. It reads nothing: the facts of now are lifecycle's.
func (v *Views) projected(s *item.State, b *board.State) View {
	view := folded(s)
	switch s.Status() {
	case item.StatusAwaitingClose:
		view.NotStaffed = notStaffed(s.Closing)
	case item.StatusClosed:
		view.Closed = &ClosedView{
			At: s.ClosedAt, By: s.Closed.By, Auto: s.Closed.Auto,
			ReopenableUntil: s.ClosedAt.Add(v.p.ReopenWindow), LateRulingsUntil: s.ClosedAt.Add(v.p.ClosedRulings),
		}
	}
	if s.Run == nil {
		// A close keeps the awaiting_close it ended: only an item that awaits
		// its close says why it does.
		if s.Status() == item.StatusAwaitingClose {
			view.Stop = string(s.Closing.Reason)
		}
		return view
	}
	view.Stops = len(s.Run.Stops)
	if s.Run.Stopped != nil {
		view.Stop, view.Stops = string(s.Run.Stopped.Reason), view.Stops+1
	}
	view.Budget = &BudgetView{Budget: s.Run.Budget(), Used: usedBy(s.Run)}
	view.ExtensionOffered = offered(v.p.Extension, s.Run.Created.Budget)
	for i := range s.Run.Rounds {
		view.Rounds = append(view.Rounds, roundView(&s.Run.Rounds[i], b))
	}
	if e, stopped := b.Stopped(); stopped && e.Body.Stop != nil {
		stop := *e.Body.Stop
		stop.Hypotheses, stop.OpenTasks, stop.Proposals = slices.Clone(stop.Hypotheses), slices.Clone(stop.OpenTasks), slices.Clone(stop.Proposals)
		view.StopEntry = &stop
	}
	return view
}

// List folds the records of each item that is not closed and each item closed
// within the reopen window, once each, in the order of their ids, and gives a
// Row of each: the fold alone, with no board read, and the kill switch's table
// read once for the list, and only when an item awaits its close. An item of a
// later version is a Row with HeldByLaterBuild alone. A list that cannot be
// made, or an item that cannot be folded, fails it.
func (v *Views) List(ctx context.Context) ([]Row, error) {
	active, err := v.records.Active(ctx)
	if err != nil {
		return nil, fmt.Errorf("list the active items: %w", err)
	}
	closed, err := v.p.Closed.ClosedSince(ctx, v.p.ReopenWindow)
	if err != nil {
		return nil, fmt.Errorf("list the items closed lately: %w", err)
	}
	ids := slices.Concat(active, closed)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	waiting, held := waitingWhileHeld(v.p.Held, v.p.Lifecycle)
	var table switchRead
	rows := make([]Row, 0, len(ids))
	for _, id := range ids {
		records, err := v.records.Load(ctx, id)
		if corrupt(err) {
			rows = append(rows, v.unreadable(ctx, id))
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load item %s: %w", id, err)
		}
		if len(records) == 0 {
			continue
		}
		s, err := item.Fold(records)
		switch {
		case ofLaterVersion(err):
			rows = append(rows, Row{ID: id, HeldByLaterBuild: true})
			continue
		case corrupt(err):
			rows = append(rows, v.unreadable(ctx, id))
			continue
		case err != nil:
			return nil, fmt.Errorf("fold item %s: %w", id, err)
		}
		row := v.row(ctx, s, held, waiting, &table)
		rows = append(rows, row)
	}
	return rows, nil
}

// row is s's row in the list: held says that this instance holds the lease,
// waiting is the queue, and table the kill switch's table, read once for the
// list.
func (v *Views) row(ctx context.Context, s *item.State, held bool, waiting []item.ID, table *switchRead) Row {
	id := s.ID
	row := Row{
		ID: id, Title: s.Opened.Title, Service: s.Opened.Routing["service"], Principal: s.Opened.Principal,
		Responders: slices.Clone(s.Opened.Responders), State: string(s.Status()), PositionKnown: held, Incident: s.Incident,
	}
	switch s.Status() {
	case item.StatusQueued:
		row.QueuePosition = slices.Index(waiting, id) + 1
	case item.StatusAwaitingClose:
		c := v.selfCloseOf(ctx, s, &table.ks, table.read(ctx, v))
		row.AutoClosePaused, row.AutoCloseUnread, row.ClosesAt, row.ClosesAtUnread = c.paused, c.unread, c.at, c.atUnread
	case item.StatusClosed:
		row.ClosedAt, row.ReopenableUntil = s.ClosedAt, s.ClosedAt.Add(v.p.ReopenWindow)
	}
	return row
}

// unreadable is the row of an item whose records are invalid, which logs the
// item once, not once for each list: the line names the item and the
// failure's class, corrupt, never the error's text (see FailureClass).
func (v *Views) unreadable(ctx context.Context, id item.ID) Row {
	if v.firstTime(id, "invalid") {
		v.log.WarnContext(ctx, "an item's records are invalid: the list shows it unreadable", "item", id, "class", FailureCorrupt)
	}
	return Row{ID: id, Unreadable: true}
}

// The classes of a read's failure, which a log line of the views, the board
// watch and the board's pages names in place of the error (see FailureClass).
const (
	FailureCorrupt  = "corrupt"
	FailureDeadline = "deadline"
	FailureStore    = "store"
)

// FailureClass is the class of err, a read's failure, which a log line names
// in place of its text: on a corrupt row that text can quote a stored value,
// as a board rule's %q of a stored tag, code or kind, a body's raw text, a
// decode error, or a record's commit quoted by a revision's read does
// (Task 4's ruling; review 1's M1). FailureCorrupt is records or a board
// that are invalid, item.ErrInvalid or board.ErrInvalid, a panic the board
// watch recovered in a build among them, which the watch logs itself;
// FailureDeadline, the read's context ended; FailureStore, any other
// failure, whose text is usually the driver's, and is not logged either.
func FailureClass(err error) string {
	switch {
	case corrupt(err):
		return FailureCorrupt
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		return FailureDeadline
	}
	return FailureStore
}

// firstTime reports whether the failure named for has not been logged for the
// item before, and notes it.
func (v *Views) firstTime(id item.ID, reason string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	k := logged{id, reason}
	_, again := v.logged[k]
	v.logged[k] = struct{}{}
	return !again
}

// waitingWhileHeld is the coordinator's queue as this instance's snapshot
// holds it, while held says that this instance holds the lease, and whether
// it does: held is read again after the snapshot, since a lease lost before
// it leaves the snapshot empty, which is no known position (m6). The list and
// the board watch read the queue so; nil ports are no lease held.
func waitingWhileHeld(held func() bool, lifecycle func() Lifecycle) ([]item.ID, bool) {
	if held == nil || lifecycle == nil || !held() {
		return nil, false
	}
	waiting := lifecycle().Waiting
	return waiting, held()
}

// selfClose is how an item awaiting its close closes by itself (Decision 8;
// R28): paused while the kill switch's table holds the switch on; unread
// while the table could not be read; or when, and whether its delay could
// not be read (see closesAt). The item's view, the list's rows and the board
// watch's facts each give it so.
type selfClose struct {
	paused, unread bool
	at             time.Time
	atUnread       bool
}

// selfCloseOf is the selfClose of s, an item awaiting its close, under ks,
// the kill switch's table as it was read, read saying whether it could be.
func (v *Views) selfCloseOf(ctx context.Context, s *item.State, ks *KillSwitch, read bool) selfClose {
	switch {
	case !read:
		return selfClose{unread: true}
	case ks.On:
		return selfClose{paused: true}
	}
	at, unread := v.closesAt(ctx, s, ks)
	return selfClose{at: at, atUnread: unread}
}

// switchRead reads the kill switch's table once, when it is first asked.
type switchRead struct {
	done, ok bool
	ks       KillSwitch
}

// read reads the table through v, on the first call, and reports whether it
// could be read.
func (t *switchRead) read(ctx context.Context, v *Views) bool {
	if !t.done {
		var err error
		t.ks, err = v.p.Controls.KillSwitch(ctx)
		t.done, t.ok = true, err == nil
	}
	return t.ok
}

// lifecycle gives view s's place in the lifecycle now, beside what its
// records determine (see projected), by its state, never by its Closing,
// which a close keeps (Contract C4): a waiting item's place in the queue, as
// the coordinator's snapshot holds it (R17); and an item's that awaits its
// close, whether its auto-close is paused, or when it closes by itself (see
// autoClose).
func (v *Views) lifecycle(ctx context.Context, s *item.State, view *View) {
	switch s.Status() {
	case item.StatusQueued:
		view.QueuePosition = slices.Index(v.p.Lifecycle().Waiting, s.ID) + 1
	case item.StatusAwaitingClose:
		v.autoClose(ctx, s, view)
	}
}

// notStaffed is why an item that awaits its close as closing says is not
// staffed (R18): its record's cause, or kill_switch when the record names the
// on-row that stopped its run; empty when it awaits its close for another
// reason, or with a run cut at the daily cap or stopped not staffed, whose
// stop says why.
func notStaffed(closing *item.AwaitingClose) item.Cause {
	switch {
	case closing.Reason != item.StopNotStaffed:
		return ""
	case closing.Cause != "":
		return closing.Cause
	case closing.UnstaffedBy > 0:
		return item.CauseKillSwitch
	}
	return ""
}

// autoClose gives view whether the auto-close of s, an item awaiting its
// close, is paused, as the kill switch's table holds the switch, on, and,
// while it is not, when the item closes by itself: its delay (see delayOf)
// after the latest of its trigger's resolution, its latest entry into
// awaiting_close, and the switch's latest off, as the coordinator arms its
// timer (Decision 8). A delay that cannot be read gives no time. A table that
// cannot be read gives neither, and says so (R28).
func (v *Views) autoClose(ctx context.Context, s *item.State, view *View) {
	ks, err := v.p.Controls.KillSwitch(ctx)
	c := v.selfCloseOf(ctx, s, &ks, err == nil)
	view.AutoClosePaused, view.AutoCloseUnread, view.ClosesAt, view.ClosesAtUnread = c.paused, c.unread, c.at, c.atUnread
}

// closesAt is when s, an item awaiting its close, closes by itself while the
// kill switch ks is off (see autoClose), and whether its delay could not be
// read, which gives no time. The first unread delay of an item is logged,
// once for the item, not once for each view of it (I15). A switch that is on
// pauses the auto-close: no time, and nothing unread.
func (v *Views) closesAt(ctx context.Context, s *item.State, ks *KillSwitch) (at time.Time, unread bool) {
	if ks.On {
		return time.Time{}, false
	}
	delay, _, err := delayOf(ctx, v.p.Revisions, s)
	if err != nil {
		if v.firstTime(s.ID, "delay") {
			v.log.WarnContext(ctx, "an item's auto-close delay could not be read: its view gives no closing time", "item", s.ID, "class", FailureClass(err))
		}
		return time.Time{}, true
	}
	at, ok := s.AutoCloseAt(delay, ks.OffAt)
	if !ok {
		// The item's trigger fires: it never closes by itself while it does
		// (R110), which gives no time, and nothing unread.
		return time.Time{}, false
	}
	return at, false
}

// folded is the view s's fold alone gives, with nothing of its run's board:
// its id, state, incident, type and opening time. projected builds on it, and
// the strip asks Visible about each sibling's (R144), so that the one
// predicate judges a board and a strip's item by the same fields; Plan 8a's
// Visible reads only these (review 2's M8).
func folded(s *item.State) View {
	return View{ID: s.ID, State: string(s.Status()), Incident: s.Incident, Type: s.Opened.Type, OpenedAt: s.OpenedAt}
}

// offered is the extension the interface offers for a run created with the
// budget created: the deployment's, each of its rounds and wall clock
// clamped to created's (R71).
func offered(extension, created item.Budget) item.Budget {
	return item.Budget{Rounds: min(extension.Rounds, created.Rounds), WallClock: min(extension.WallClock, created.WallClock)}
}

// roundView is a round's view, with the board's report of it.
func roundView(r *item.Round, b *board.State) RoundView {
	rv := RoundView{Round: r.Opened.Round, WrapUp: r.Opened.WrapUp != "", Report: b.Report(r.Opened.Round)}
	if e := r.Ended; e != nil {
		rv.Ended, rv.Quiet, rv.TurnEnds = string(e.EndedBy), e.Quiet, slices.Clone(e.TurnEnds)
		rv.FailedCalls, rv.FailedToolCalls, rv.Settled = e.FailedCalls, e.FailedToolCalls, e.Settled
	}
	return rv
}
