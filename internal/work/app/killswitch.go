package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// KillSwitchOutcome is what a request to the kill switch did, or, previewed,
// would do (Contract C4; Decision 5): the switch's state and its latest row
// after it; whether it appended a row; whether it was recorded during a drain,
// for the next leader to act on; the runs it stopped, each at its latest
// round, and the items with no run it left not staffed; at an off, each item
// the latest on left not staffed, with its state now; and what the kill
// switch's rule still acts on (see owed), nil when deferred. A preview gives
// the switch's state and latest row as its table holds them, and the
// consequences the request would have.
type KillSwitchOutcome struct {
	On              bool
	Seq             int64
	Changed         bool
	Deferred        bool
	Stopped         []RunStop
	Unstaffed       []item.ID
	StayedUnstaffed []UnstaffedItem
	Owed            []item.ID
	Preview         bool
	Consequences    Consequences
}

// UnstaffedItem is an item the latest on left not staffed, and its state now
// (Decision 5).
type UnstaffedItem struct {
	Item  item.ID
	State item.Status
}

// The causes of a change of the coordinator's view, as "kill switch set" logs
// them (Contract C3): the coordinator's own row, and a store's answer to a
// request that appended nothing.
const (
	causeAct    = "act"
	causeAnswer = "answer"
)

// SetKillSwitch turns the kill switch on, or off, for a person (spec Section
// 8.3; Decision 5), and answers what it did, or, previewed, what it would do.
// It takes the coordinator's lock, and writes the switch's row under the
// coordinator's own context, which carries its holding's epoch, whatever ctx
// says: a client that leaves once its request reached the lock cancels no
// row, no halt and no record. The store's answer, changed or not, sets the
// coordinator's view, which acts on its change (see applyKillSwitch): an on
// stops every turn and ends every session at once, drops every run, and
// advances every item, whose step halts each run that owes the kill and
// leaves not staffed each item with no run, queued or started. An off arms
// every auto-close timer again, each counting from the off, and restarts
// nothing: a person promotes the items without a run. It never waits for what
// the on still owes, whose halts and records of not staffed are carried out
// later, the switch off or not. The answer gives the switch's state and latest
// row, whether a row was appended, the runs stopped, the items left not
// staffed, at an off every item the latest on left not staffed, with its state
// now, and what the rule still acts on.
//
// A request names who acts, a subject, which the row keeps, and After, the
// latest row its sender saw, 0 before the first (R8). Anyone who may act turns
// the switch on, but only an admin turns it off: an off, or its preview, by an
// actor who is no admin is refused with a *Refused, CodeAdminOnly, before
// anything is read or written. An on needs a reason, and one without is
// refused with a *Refused, CodeReasonRequired; an off takes one if given
// (Decision 9). The reason's text is checked and redacted as a person's text
// with the rules of the registry's current revision: with none current, a
// text is refused with ErrInvalid, and a code alone is taken (see
// checkSwitch).
//
// A preview records nothing, acts on nothing, and leaves the view as it was
// (see previewKillSwitch). It needs no reason, as it takes no key: it writes
// no row.
//
// It answers ErrNotHeld when the coordinator is not held, before it reads
// anything; ErrInvalid for a request of another form; and a store's error as
// the store gave it.
func (c *Coordinator) SetKillSwitch(_ context.Context, on bool, a *Act) (KillSwitchOutcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.held {
		return KillSwitchOutcome{}, ErrNotHeld
	}
	reason, err := c.checkSwitch(on, a)
	if err != nil {
		return KillSwitchOutcome{}, err
	}
	if a.Preview {
		return c.previewKillSwitch(on)
	}
	state, changed, err := c.p.Controls.SetKillSwitch(c.ctx, on, a.Actor.Subject, reason, a.After)
	if err != nil {
		return KillSwitchOutcome{}, err
	}
	cause := causeAnswer
	if changed {
		cause = causeAct
	}
	out := c.applyKillSwitch(c.ctx, &state, cause)
	out.Changed = changed
	return out, nil
}

// checkSwitch checks a request to the kill switch (Decisions 5 and 9): who
// acts, a subject; After, 0 or above; an off's actor, an admin, as only an
// admin turns the switch off, a preview included; and its reason, which an on
// needs, but for a preview. It returns the reason the row keeps: its code,
// and its text checked and redacted as a person's text (see checkReason),
// with the rules of the current revision, since the kill switch is no item's;
// both empty for none. An error never quotes the reason.
func (c *Coordinator) checkSwitch(on bool, a *Act) (board.Reason, error) {
	switch {
	case a == nil:
		return board.Reason{}, fmt.Errorf("%w: no act", ErrInvalid)
	case a.Actor.Subject == "":
		return board.Reason{}, fmt.Errorf("%w: an act names who acts", ErrInvalid)
	case a.After < 0:
		return board.Reason{}, fmt.Errorf("%w: after is the kill switch's latest row its sender saw, 0 before the first", ErrInvalid)
	case !on && !a.Actor.Admin:
		return board.Reason{}, &Refused{Code: CodeAdminOnly}
	case on && a.Reason == nil && !a.Preview:
		return board.Reason{}, &Refused{Code: CodeReasonRequired}
	}
	r, err := c.checkReason(&item.State{}, a.Reason)
	if err != nil || r == nil {
		return board.Reason{}, err
	}
	return *r, nil
}

// previewKillSwitch answers what a request to turn the kill switch on, or off,
// would do (Decision 13), recording nothing, acting on nothing, and leaving
// the view as it was. It reads the switch from its table, not from the view,
// and answers the table's state and latest row, which the act then names as
// After: a view one renewal behind would give a row the act finds stale. An
// on's consequences are the runs that would stop and the items that would not
// be staffed (see wouldKill); an off's, none in this build.
func (c *Coordinator) previewKillSwitch(on bool) (KillSwitchOutcome, error) {
	state, err := c.p.Controls.KillSwitch(c.ctx)
	if err != nil {
		return KillSwitchOutcome{}, fmt.Errorf("read the kill switch: %w", err)
	}
	out := KillSwitchOutcome{On: state.On, Seq: state.Seq, Preview: true}
	if on {
		out.Consequences.Stops, out.Consequences.Unstaffed = c.wouldKill(&state)
	}
	return out, nil
}

// wouldKill is what turning the kill switch on would stop, its table holding
// state: the on applies the next row while the switch is off, and its latest
// on-row while it is on, and acts only when that raises the view's latest
// on-row (see applyKillSwitch). Then the runs that owe that on-row would stop,
// and the items with no run that owe it would not be staffed (see owing).
func (c *Coordinator) wouldKill(state *KillSwitch) ([]RunStop, []item.ID) {
	onSeq := state.OnSeq
	if !state.On {
		onSeq = state.Seq + 1
	}
	if onSeq <= c.killSwitch.OnSeq {
		return nil, nil
	}
	return c.owing(onSeq)
}

// owing lists, in the order of their items, what in the account owes the
// kill of the on-row onSeq (Decision 5): the runs that would stop, each at its
// latest round, 0 before its first, for the reason its halt records (see
// runStop), and the items with no run, queued or started, that would be left
// not staffed.
func (c *Coordinator) owing(onSeq int64) (stops []RunStop, unstaffed []item.ID) {
	for _, id := range slices.Sorted(maps.Keys(c.items)) {
		s := c.items[id].state
		switch {
		case !s.Owes(onSeq):
		case s.Running():
			stops = append(stops, runStop(s, item.StopKilled))
		default:
			unstaffed = append(unstaffed, id)
		}
	}
	return stops, unstaffed
}

// applyKillSwitch has the coordinator's view follow state, the switch's state
// as the store gave it, under the lock, and acts on the view's change by
// Decision 5's two rules, whatever caused it, as cause names it (Contract
// C3). A raise of the latest on-row runs the on's steps 2 to 5 (see kill),
// whatever else changed. Then the view's state decides the rest. Off, when
// the view was on before or its latest on-row was raised, as by an off whose
// answer reveals an on never applied, runs the off's steps, while the
// coordinator holds: every auto-close timer is armed again, each counting from
// the off (Decision 8; see armAutoCloses), and every item the latest on left
// not staffed is listed, with its state now (see stayedUnstaffed). Nothing
// restarts, and nothing waits for what is owed. A view that changed publishes
// the snapshot and logs "kill switch set": an on, its step 6. It returns what
// it did: the switch's state and latest row, the runs it stopped, the items
// it left not staffed, at an off the items that stayed so, and what the kill
// switch's rule still acts on (see owed).
func (c *Coordinator) applyKillSwitch(ctx context.Context, state *KillSwitch, cause string) KillSwitchOutcome {
	view, before := viewOf(state), c.killSwitch
	c.killSwitch = view
	out := KillSwitchOutcome{On: state.On, Seq: state.Seq}
	raised := view.OnSeq > before.OnSeq
	if raised {
		out.Stopped, out.Unstaffed = c.kill(ctx)
	}
	turnedOff := !view.On && (before.On || raised)
	if turnedOff && c.held {
		c.armAutoCloses(ctx)
		out.StayedUnstaffed = c.stayedUnstaffed(ctx)
	}
	out.Owed = c.owed()
	if !sameView(&view, &before) {
		if c.held {
			c.publish()
		}
		c.logKillSwitch(ctx, state, cause, raised, turnedOff, &out)
	}
	return out
}

// stayedUnstaffed lists, in the order of their items, every item the view's
// latest on-row left not staffed (Decision 5; R4), as the off answers them:
// those whose item_awaiting_close names that on-row, as the records store
// finds them (Records.UnstaffedBy). It reads the records, not the fold: an
// item reopened since returns to an awaiting_close of the reopen's own, which
// names no on-row, and is listed all the same. Each comes with its state now,
// from its records: still awaiting its close, or closed by a person since,
// as auto-close paused while the switch was on. A read that fails is logged:
// the off has happened, and its answer lists what could be read, an item whose
// load failed with no state.
func (c *Coordinator) stayedUnstaffed(ctx context.Context) []UnstaffedItem {
	onSeq := c.killSwitch.OnSeq
	ids, err := c.p.Records.UnstaffedBy(ctx, onSeq)
	if err != nil {
		c.log.ErrorContext(ctx, "the items the kill switch left unstaffed could not be read", "onSeq", onSeq, "err", err)
		return nil
	}
	slices.Sort(ids)
	var out []UnstaffedItem
	for _, id := range ids {
		u := UnstaffedItem{Item: id}
		if s, err := c.load(ctx, id); err != nil {
			c.failed(ctx, id, fmt.Errorf("read the state of an item the kill switch left unstaffed: %w", err))
		} else {
			u.State = s.Status()
		}
		out = append(out, u)
	}
	return out
}

// kill runs the on's steps 2 to 5 (Decision 5), under ctx, the coordinator's
// own context, for the view's latest on-row. Every turn stops and every
// session ends at once. Every run is dropped, its snapshot kept for its halt
// (see drop), while the coordinator still holds: a launch the stop cancelled
// then reports to no run, and nothing arms its retry. Every auto-close timer
// is cancelled, and, the view on, the advances that follow arm none (see
// armAutoClose). Then each item of the account is advanced, in order, its
// advances walking no queue, and the queue is walked once: the step halts
// each run that owes the kill, with its snapshot's classes, and leaves not
// staffed each item with no run, queued or started (see step). It returns, in
// the order of their items, the runs whose stop is recorded, each at its
// latest round, for the reason its halt recorded, and the items it left not
// staffed.
func (c *Coordinator) kill(ctx context.Context) (stopped []RunStop, unstaffed []item.ID) {
	c.p.Turns.StopAll()
	c.p.Sessions.EndAll()
	for _, run := range slices.Sorted(maps.Keys(c.runs)) {
		c.drop(run)
	}
	c.cancelAutoCloses()
	stops, owing := c.owing(c.killSwitch.OnSeq)
	c.advanceAll(ctx, slices.Sorted(maps.Keys(c.items)))
	c.walk(ctx)
	for i := range stops {
		if s := c.items[stops[i].Item].state; s != nil && s.Run != nil && s.Run.Stopped != nil {
			stopped = append(stopped, stopOf(s))
		}
	}
	for _, id := range owing {
		if e, ok := c.items[id]; ok && e.status == item.StatusAwaitingClose {
			unstaffed = append(unstaffed, id)
		}
	}
	return stopped, unstaffed
}

// stopOf is the stop of s's run, which stopped, as its record holds it: at
// its latest round, 0 before its first, for the reason it stopped for.
func stopOf(s *item.State) RunStop {
	stop := RunStop{Item: s.ID, Run: s.Run.ID, Reason: s.Run.Stopped.Reason}
	if l := s.Run.Latest(); l != nil {
		stop.Round = l.Opened.Round
	}
	return stop
}

// owed lists, sorted, what the kill switch's rule still acts on (Decision 5):
// the items whose run has not stopped and owes the kill of the view's latest
// on-row; the items whose killed run's finish has not ended; and the items
// queued, or started with no run, that owe that on-row. Items awaiting their
// close, or closed, are not in it, though their opening may have known an
// older row.
func (c *Coordinator) owed() []item.ID {
	var out []item.ID
	for id, e := range c.items {
		if e.state.Owes(c.killSwitch.OnSeq) || finishing(e.state) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// finishing reports whether s's run stopped killed while its finish has not
// ended: the item does not await its close yet.
func finishing(s *item.State) bool {
	return s.Status() == item.StatusInvestigating && s.Run != nil && s.Run.Stopped != nil && s.Run.Stopped.Reason == item.StopKilled
}

// logKillSwitch logs "kill switch set" (Contract C3): the switch's state, its
// latest row and latest on-row, who wrote the latest row, the holding's epoch,
// the cause, and, once the on's steps ran, the runs they stopped, by number,
// and the items they left not staffed; once the off's steps ran, the items
// that stayed so; then what the rule still acts on. It carries no person's
// text.
func (c *Coordinator) logKillSwitch(ctx context.Context, state *KillSwitch, cause string, raised, turnedOff bool, out *KillSwitchOutcome) {
	attrs := []any{"on", state.On, "seq", state.Seq, "onSeq", state.OnSeq, "by", state.By, "epoch", c.term.Epoch, "cause", cause, "deferred", out.Deferred}
	if raised {
		attrs = append(attrs, "stopped", len(out.Stopped), "unstaffed", out.Unstaffed)
	}
	if turnedOff {
		stayed := make([]item.ID, 0, len(out.StayedUnstaffed))
		for _, u := range out.StayedUnstaffed {
			stayed = append(stayed, u.Item)
		}
		attrs = append(attrs, "stayedUnstaffed", stayed)
	}
	attrs = append(attrs, "owed", out.Owed)
	c.log.WarnContext(ctx, "kill switch set", attrs...)
}

// viewOf is the coordinator's view of the kill switch as state gives it
// (Decision 5): its state, its latest row, its latest on-row and its latest
// off-row's time.
func viewOf(state *KillSwitch) KillSwitch {
	return KillSwitch{On: state.On, Seq: state.Seq, OnSeq: state.OnSeq, OffAt: state.OffAt}
}

// sameView reports whether the views a and b agree.
func sameView(a, b *KillSwitch) bool {
	return a.On == b.On && a.Seq == b.Seq && a.OnSeq == b.OnSeq && a.OffAt.Equal(b.OffAt)
}

// killRun halts s's run, which owes the kill of the view's latest on-row
// (Decision 3; Decision 12, rule 8). It drops the run first (see drop), since
// the kill is owed whatever its record does: the run's turns stop, its
// sessions end, and no turn of it starts again. Then it records, in one
// append, the records that stop the run killed, by that on-row (see
// haltRecords), with the turn classes of the snapshot the drop kept, if any;
// once they commit, the snapshot is forgotten, and finish follows. A halt
// that fails is tried again, as every step is, its snapshot kept for the
// retry (R45).
func (c *Coordinator) killRun(ctx context.Context, s *item.State) error {
	run := s.Run.ID
	c.drop(run)
	bodies := c.haltRecords(ctx, s, item.StopKilled, c.killSwitch.OnSeq)
	recs := make([]rec, 0, len(bodies))
	for _, b := range bodies {
		recs = append(recs, rec{run: run, body: b})
	}
	if _, err := c.record(ctx, s, recs...); err != nil {
		return err
	}
	delete(c.snapshots, run)
	return nil
}

// leaveUnstaffed leaves s, an item with no run, queued or started, not
// staffed for the kill switch (Decision 5; Decision 12, rule 4): its
// item_awaiting_close not_staffed, for the cause kill_switch, left so by the
// view's latest on-row, with its type's auto-close delay as its writer can
// read it (see writerDelay).
func (c *Coordinator) leaveUnstaffed(ctx context.Context, s *item.State) error {
	_, err := c.record(ctx, s, rec{body: item.Body{AwaitingClose: &item.AwaitingClose{
		Reason: item.StopNotStaffed, Cause: item.CauseKillSwitch, UnstaffedBy: c.killSwitch.OnSeq, AutoClose: c.writerDelay(ctx, s),
	}}})
	return err
}

// noticeKilled posts on a run's board the kill_switch notice, which says that
// the kill switch stopped the run (Decision 5), once per round, as the board
// takes it. A board that holds its stop entry takes no notice: its
// board.ErrStopped counts as done (carry 5), so that a post after the stop
// entry is not tried again for ever.
func (c *Coordinator) noticeKilled(ctx context.Context, run board.RunID) error {
	_, err := c.p.Boards.Notice(ctx, run, board.Coordinator, &board.Notice{Code: board.NoticeKillSwitch})
	if errors.Is(err, board.ErrStopped) {
		return nil
	}
	return err
}
