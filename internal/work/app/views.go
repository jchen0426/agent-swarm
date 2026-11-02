package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	// Closed is a closed item's close; nil for any other item.
	Closed *ClosedView
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
}

// Views reads items as the admin API shows them, from the stores: the work
// records and the boards, and, of the lifecycle, the coordinator's snapshot
// and the kill switch's table. It takes no lock of the coordinator's, and
// writes nothing.
type Views struct {
	records Records
	boards  *Boards
	p       ViewPorts
}

// NewViews returns the views of the items records keeps, whose runs' boards
// boards reads, and whose lifecycle p gives.
func NewViews(records Records, boards *Boards, p *ViewPorts) *Views {
	return &Views{records: records, boards: boards, p: *p}
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
	return view, nil
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
	if delay, _, err := delayOf(ctx, v.p.Revisions, s); err == nil {
		view.ClosesAt = s.AutoCloseAt(delay, ks.OffAt)
	}
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
