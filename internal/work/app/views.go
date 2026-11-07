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
}

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
	// ClosesAt is as View.ClosesAt, and ClosesAtUnread as View.ClosesAtUnread.
	ClosesAt         time.Time
	ClosesAtUnread   bool
	HeldByLaterBuild bool
	Incident         item.IncidentID
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
	// Held says whether this instance holds the lease (m5): when it does not,
	// the coordinator's snapshot of the queue is empty, and the list's rows
	// say their position is not known. Nil is taken as not holding.
	Held func() bool
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
	logged map[item.ID]struct{}
}

// NewViews returns the views of the items records keeps, whose runs' boards
// boards reads, and whose lifecycle p gives.
func NewViews(records Records, boards *Boards, p *ViewPorts) *Views {
	log := p.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Views{records: records, boards: boards, p: *p, log: log, logged: map[item.ID]struct{}{}}
}

// KillSwitch reads the kill switch's state from its table, as the status
// shows it, a standby's included (Decision 14).
func (v *Views) KillSwitch(ctx context.Context) (KillSwitch, error) {
	return v.p.Controls.KillSwitch(ctx)
}

// Item folds an item's records and reads its board's report for each round,
// and gives its place in the lifecycle (see lifecycle). An item with a record
// or board event of a later version is a View with HeldByLaterBuild alone. An
// id with no records fails with ErrNoItem; an item whose records or board
// this build reads but cannot fold fails with what the fold or the replay
// found.
func (v *Views) Item(ctx context.Context, id item.ID) (View, error) {
	records, err := v.records.Load(ctx, id)
	if err != nil {
		return View{}, fmt.Errorf("load item %s: %w", id, err)
	}
	if len(records) == 0 {
		return View{}, ErrNoItem
	}
	s, err := item.Fold(records)
	switch {
	case ofLaterVersion(err):
		return View{ID: id, HeldByLaterBuild: true}, nil
	case err != nil:
		return View{}, fmt.Errorf("fold item %s: %w", id, err)
	}
	view := View{ID: id, State: string(s.Status()), Incident: s.Incident, Type: s.Opened.Type, OpenedAt: s.OpenedAt}
	v.lifecycle(ctx, s, &view)
	if s.Run == nil {
		// A close keeps the awaiting_close it ended: only an item that awaits
		// its close says why it does.
		if s.Status() == item.StatusAwaitingClose {
			view.Stop = string(s.Closing.Reason)
		}
		return view, nil
	}
	if s.Run.Stopped != nil {
		view.Stop = string(s.Run.Stopped.Reason)
	}
	// The board is read once, so that every round's report comes from the
	// same reading of it.
	_, b, err := v.boards.state(ctx, s.Run.ID)
	switch {
	case ofLaterVersion(err):
		return View{ID: id, HeldByLaterBuild: true}, nil
	case err != nil:
		return View{}, fmt.Errorf("read the board of item %s: %w", id, err)
	}
	for i := range s.Run.Rounds {
		view.Rounds = append(view.Rounds, roundView(&s.Run.Rounds[i], b))
	}
	if e, stopped := b.Stopped(); stopped && e.Body.Stop != nil {
		stop := *e.Body.Stop
		stop.Hypotheses, stop.OpenTasks, stop.Proposals = slices.Clone(stop.Hypotheses), slices.Clone(stop.OpenTasks), slices.Clone(stop.Proposals)
		view.StopEntry = &stop
	}
	return view, nil
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
	held := v.p.Held != nil && v.p.Held()
	var waiting []item.ID
	if held {
		waiting = v.p.Lifecycle().Waiting
	}
	var table switchRead
	rows := make([]Row, 0, len(ids))
	for _, id := range ids {
		records, err := v.records.Load(ctx, id)
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
		case err != nil:
			return nil, fmt.Errorf("fold item %s: %w", id, err)
		}
		row := Row{
			ID: id, Title: s.Opened.Title, Service: s.Opened.Routing["service"], Principal: s.Opened.Principal,
			Responders: slices.Clone(s.Opened.Responders), State: string(s.Status()), PositionKnown: held, Incident: s.Incident,
		}
		switch s.Status() {
		case item.StatusQueued:
			row.QueuePosition = slices.Index(waiting, id) + 1
		case item.StatusAwaitingClose:
			if table.read(ctx, v) {
				row.ClosesAt, row.ClosesAtUnread = v.closesAt(ctx, s, &table.ks)
			} else {
				row.ClosesAtUnread = true
			}
		case item.StatusClosed:
			row.ClosedAt, row.ReopenableUntil = s.ClosedAt, s.ClosedAt.Add(v.p.ReopenWindow)
		}
		rows = append(rows, row)
	}
	return rows, nil
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

// lifecycle gives view s's place in the lifecycle, by its state, never by
// its Closing, which a close keeps (Contract C4): a waiting item's place in
// the queue, as the coordinator's snapshot holds it (R17); an item's that
// awaits its close, why it is not staffed, if it is not (R18), and whether
// its auto-close is paused, or when it closes by itself (see autoClose); a
// closed item's close, with until when it may be reopened and take late
// rulings.
func (v *Views) lifecycle(ctx context.Context, s *item.State, view *View) {
	switch s.Status() {
	case item.StatusQueued:
		view.QueuePosition = slices.Index(v.p.Lifecycle().Waiting, s.ID) + 1
	case item.StatusAwaitingClose:
		view.NotStaffed = notStaffed(s.Closing)
		v.autoClose(ctx, s, view)
	case item.StatusClosed:
		view.Closed = &ClosedView{
			At: s.ClosedAt, By: s.Closed.By, Auto: s.Closed.Auto,
			ReopenableUntil: s.ClosedAt.Add(v.p.ReopenWindow), LateRulingsUntil: s.ClosedAt.Add(v.p.ClosedRulings),
		}
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
	switch {
	case err != nil:
		view.AutoCloseUnread = true
		return
	case ks.On:
		view.AutoClosePaused = true
		return
	}
	view.ClosesAt, view.ClosesAtUnread = v.closesAt(ctx, s, &ks)
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
		v.mu.Lock()
		_, again := v.logged[s.ID]
		v.logged[s.ID] = struct{}{}
		v.mu.Unlock()
		if !again {
			v.log.WarnContext(ctx, "an item's auto-close delay could not be read: its view gives no closing time", "item", s.ID, "err", err)
		}
		return time.Time{}, true
	}
	return s.AutoCloseAt(delay, ks.OffAt), false
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
