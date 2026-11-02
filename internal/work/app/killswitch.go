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
// the latest on left not staffed, with its state now, and whether that list
// could not be read in full; and what the kill switch's rule still acts on
// (see owed), nil when deferred. A preview gives the switch's state and
// latest row as its table holds them, and the consequences the request would
// have.
type KillSwitchOutcome struct {
	On              bool
	Seq             int64
	Changed         bool
	Deferred        bool
	Stopped         []RunStop
	Unstaffed       []item.ID
	StayedUnstaffed []UnstaffedItem
	// StayedUnstaffedUnread says that the list of the items the latest on
	// left not staffed could not be read in full, so StayedUnstaffed may be
	// incomplete: the list itself could not be read, or an item of it could
	// not, which it gives with no state. The off happened all the same.
	StayedUnstaffedUnread bool
	Owed                  []item.ID
	Preview               bool
	Consequences          Consequences
}

// UnstaffedItem is an item the latest on left not staffed, and its state now
// (Decision 5): empty when the item could not be read, as the answer's
// StayedUnstaffedUnread then says.
type UnstaffedItem struct {
	Item  item.ID
	State item.Status
}

// The causes of a change of the coordinator's view, as "kill switch set" logs
// them (Contract C3): the coordinator's own row; a store's answer to a
// request that appended nothing, or that was stale; a read after a write
// whose outcome is unknown, or its retry; the leader's refresh, after a
// renewal found a newer row; and a takeover's read.
const (
	causeAct      = "act"
	causeAnswer   = "answer"
	causeReread   = "reread"
	causeRenewal  = "renewal"
	causeTakeover = "takeover"
)

// KillSwitchSeen is the latest kill switch row the coordinator's view holds,
// 0 before the first row and while fenced (Decision 5). It reads an atomic
// that every change of the view stores, and takes no lock, so that the
// leader's renewal check never waits for the coordinator.
func (c *Coordinator) KillSwitchSeen() int64 {
	return c.seen.Load()
}

// RefreshKillSwitch reads the kill switch again, under the lock, and has the
// view follow it, which acts on its change, logged with the cause renewal
// (see applyKillSwitch): the leader calls it once a renewal read a row newer
// than the view (Decision 5; see Leader.checkKillSwitch). It reads the table
// rather than take the number the renewal read, so that a renewal that read
// late never moves the view back. A read that succeeds settles a view whose
// write's outcome was unknown (R37). It acts under the coordinator's own
// context, which carries its holding's epoch, whatever ctx says, and does
// nothing while fenced. A read that fails is logged as a step that failed,
// and changes nothing: the next renewal reads the row again.
func (c *Coordinator) RefreshKillSwitch(_ context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.held {
		return
	}
	state, err := c.p.Controls.KillSwitch(c.ctx)
	if err != nil {
		c.failed(c.ctx, "", fmt.Errorf("read the kill switch after a renewal found a newer row: %w", err))
		return
	}
	c.applyKillSwitch(c.ctx, &state, causeRenewal)
}

// setView sets the coordinator's view of the kill switch to v, and stores its
// latest row for KillSwitchSeen: every change of the view goes through it.
func (c *Coordinator) setView(v *KillSwitch) {
	c.killSwitch = *v
	c.seen.Store(v.Seq)
}

// SetKillSwitch turns the kill switch on, or off, for a person (spec Section
// 8.3; Decision 5), and answers what it did, or, previewed, what it would do.
// It takes the coordinator's lock, and, once the request reached it still
// live, writes the switch's row under the coordinator's own context, which
// carries its holding's epoch, whatever ctx says: a client that leaves once
// its request reached the lock cancels no row, no halt and no record, and a
// request that ended before writes nothing, and answers ctx's error. The
// store's answer, changed or not, and stale too, sets the coordinator's view,
// which acts on its change (see applyKillSwitch): an on stops every turn and
// ends every session at once, drops every run, and advances every item, whose
// step halts each run that owes the kill and leaves not staffed each item with
// no run, queued or started. An off arms every auto-close timer again, each
// counting from the off, and restarts nothing: a person promotes the items
// without a run. It never waits for what the on still owes, whose halts and
// records of not staffed are carried out later, the switch off or not. The
// answer gives the switch's state and latest row, whether a row was appended,
// the runs stopped, the items left not staffed, at an off every item the
// latest on left not staffed, with its state now, and what the rule still acts
// on. A write that fails may have landed, its answer lost: the view is then
// unsettled until a read settles it, and the act answers by what that read
// finds (see unanswered).
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
// anything, when the store's guard refused the write, and when the
// coordinator's context ended as it wrote the row or read the switch again;
// ErrInvalid for a request of another form; ErrStale when a row newer than
// After stands and the state asked does not hold; and, for a write whose
// outcome is still unknown, or that did not land, the store's error, which
// the admin API answers 503.
func (c *Coordinator) SetKillSwitch(ctx context.Context, on bool, a *Act) (KillSwitchOutcome, error) {
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
	if err := ctx.Err(); err != nil {
		return KillSwitchOutcome{}, fmt.Errorf("the request ended before the kill switch's row was written: %w", err)
	}
	state, changed, err := c.p.Controls.SetKillSwitch(c.ctx, on, a.Actor.Subject, reason, a.After)
	switch {
	case errors.Is(err, ErrStale):
		// The store's answer carries the switch's state, which the view
		// follows.
		c.applyKillSwitch(c.ctx, &state, causeAnswer)
		return KillSwitchOutcome{}, err
	case err != nil:
		return c.unanswered(on, a.After, err)
	}
	cause := causeAnswer
	if changed {
		cause = causeAct
	}
	out := c.applyKillSwitch(c.ctx, &state, cause)
	out.Changed = changed
	return out, nil
}

// unanswered answers a request to turn the kill switch on, or off, whose
// write failed with err, an error other than ErrStale (Decision 5, step 1).
// The failure is logged as a step that failed. The guard's refusal, which
// wraps ErrLeaseLost, wrote nothing: as failed handles it, it fences the
// coordinator and tells its leader, and the act answers ErrNotHeld. Any other
// error leaves the write's outcome unknown, as its row may have landed with
// its answer lost: the view is marked unsettled, so that no work starts until
// a read settles it (R37; see unsettle). An error that came as the
// coordinator's context ended answers ErrNotHeld, with no second read: the
// fence that follows, as a drain's, clears the mark. Otherwise the
// coordinator reads the switch again, under its lock, and applies what it
// finds, which settles the view; the act answers by what it found, against
// the state asked, on, and after, the latest row its sender saw:
//   - the state asked, by a row after after: changed, as a row written since
//     the sender's view, this request's or another's to the same effect, set
//     it;
//   - the state asked, with no row after after: unchanged, as it held;
//   - not the state asked, with no row after after: err, which the admin API
//     answers 503, as the row did not land and nothing changed, so that the
//     request may be sent again as it was;
//   - not the state asked, with a row after after: ErrStale, as a newer row
//     stands.
//
// A read that fails too arms its retry (see readAgain), and the act answers
// err, which the admin API answers 503: the view stays unsettled until a read
// succeeds, or a later request's answer carries the switch's state. A read
// that failed as the coordinator's context ended arms none, and the act
// answers ErrNotHeld, the mark left for the fence that follows to clear.
func (c *Coordinator) unanswered(on bool, after int64, err error) (KillSwitchOutcome, error) {
	ctx := c.ctx
	err = fmt.Errorf("set the kill switch: %w", err)
	c.failed(ctx, "", err)
	if errors.Is(err, ErrLeaseLost) {
		return KillSwitchOutcome{}, ErrNotHeld
	}
	c.unsettle(ctx)
	if ctx.Err() != nil {
		return KillSwitchOutcome{}, ErrNotHeld
	}
	state, read := c.readAgain(ctx)
	switch {
	case !read && ctx.Err() != nil:
		return KillSwitchOutcome{}, ErrNotHeld
	case !read:
		return KillSwitchOutcome{}, err
	}
	out := c.applyKillSwitch(ctx, &state, causeReread)
	newer := state.Seq > after
	switch {
	case state.On == on:
		out.Changed = newer
		return out, nil
	case newer:
		return KillSwitchOutcome{}, fmt.Errorf("the kill switch's latest row is %d, not %d: %w", state.Seq, after, ErrStale)
	}
	return KillSwitchOutcome{}, err
}

// unsettle marks the view of the kill switch unsettled (R37): a write's
// outcome is unknown, so the switch may be on while the view is off. While
// the mark lasts, and the view is off, the view counts as on for every start
// of work, and nothing is recorded on that account: begin starts no turn, but
// arms its retry; the step waits where it would start an item, create a run,
// or open or hold a round; the walk admits no item; a promotion answers
// ErrUnsettled; and auto-close records no close. The rules that stop work go
// on, and so do close, reopen, previews, reads and the kill switch. It
// publishes the snapshot, which shows the mark, and logs "kill switch
// unsettled", with the view's state and latest row (Contract C3). Only a read
// of the switch that succeeds, or a store's answer that carries its state,
// clears the mark (see applyKillSwitch); the fence clears it with the view.
func (c *Coordinator) unsettle(ctx context.Context) {
	c.unsettled = true
	if c.held {
		c.publish()
	}
	c.log.WarnContext(ctx, "kill switch unsettled", "on", c.killSwitch.On, "seq", c.killSwitch.Seq, "epoch", c.term.Epoch)
}

// settleView clears the view's mark, if it is set, now that state, which a
// read of the kill switch or a store's answer gave, settles it (R37): the
// pending read is cancelled, and "kill switch settled" is logged, with the
// state and latest row state gives (Contract C3). It reports whether the mark
// was set.
func (c *Coordinator) settleView(ctx context.Context, state *KillSwitch) bool {
	if !c.unsettled {
		return false
	}
	c.unsettled = false
	c.cancelReread()
	c.log.InfoContext(ctx, "kill switch settled", "on", state.On, "seq", state.Seq, "epoch", c.term.Epoch)
	return true
}

// readAgain reads the kill switch after a write whose outcome is unknown,
// under ctx, the coordinator's own context, and reports whether it read it. A
// read that fails is logged as a step that failed, and arms the next read
// after the retry wait (see reread), while the coordinator holds and its
// holding lasts: each read that fails arms the next, until one succeeds.
func (c *Coordinator) readAgain(ctx context.Context) (KillSwitch, bool) {
	state, err := c.p.Controls.KillSwitch(ctx)
	if err != nil {
		c.failed(ctx, "", fmt.Errorf("read the kill switch after a write whose outcome is unknown: %w", err))
		if c.held && ctx.Err() == nil {
			c.armReread()
		}
		return KillSwitch{}, false
	}
	return state, true
}

// rereadTimer is the timer of the kill switch's next read after a write whose
// outcome is unknown: its cancel, and, by its address, whether it is still
// the pending read when it fires (see reread).
type rereadTimer struct{ cancel func() }

// armReread arms the kill switch's next read after the retry wait, in place
// of the one armed before: one is pending at most.
func (c *Coordinator) armReread() {
	c.cancelReread()
	t := &rereadTimer{}
	t.cancel = c.p.Timers.At(c.p.Clock.Now().Add(c.opts.Retry), func() { c.reread(t) })
	c.rereading = t
}

// cancelReread cancels the kill switch's pending read, if any. A cancel is no
// port event (R33).
func (c *Coordinator) cancelReread() {
	if c.rereading != nil {
		c.rereading.cancel()
		c.rereading = nil
	}
}

// reread is t, the timer of the kill switch's read after a write whose
// outcome is unknown, or after such a read that failed (see readAgain). It
// fires under the lock, and does nothing unless the coordinator is held and t
// is still the pending read: one cancelled, as once an answer settled the
// view, or armed again since, is stale. A read that succeeds sets the view,
// which settles it, logged with the cause reread (see applyKillSwitch).
func (c *Coordinator) reread(t *rereadTimer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.held || c.rereading != t {
		return
	}
	c.rereading = nil
	if state, read := c.readAgain(c.ctx); read {
		c.applyKillSwitch(c.ctx, &state, causeReread)
	}
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
// as a store's answer or a read of the switch gave it, under the lock, and
// acts on the view's change by Decision 5's two rules, whatever caused it, as
// cause names it (Contract C3). State settles the view first: a mark of a
// write whose outcome is unknown is cleared (R37; see settleView). A raise of
// the latest on-row runs the on's steps 2 to 5 (see kill), whatever else
// changed. Then the view's state decides the rest. Off, when the view was on
// before or its latest on-row was raised, as by an off whose answer reveals
// an on never applied, runs the off's steps: every auto-close timer is armed
// again, while the coordinator holds, each counting from the off (Decision 8;
// see armAutoCloses), and every item the latest on left not staffed is
// listed, with its state now, or the list is marked unread when it cannot be
// read in full (see stayedUnstaffed). Nothing restarts, and nothing waits for
// what is owed. A mark it cleared lets the work that waited go on, whether or
// not the view changed: every item of the account is advanced, in the order
// of their items, and the queue is walked (see advanceEvery), as the on's
// steps do already when they ran. A view that changed, or a mark cleared,
// publishes the snapshot; a view that changed logs "kill switch set": an on,
// its step 6. It returns what it did: the switch's state and latest row, the
// runs it stopped, the items it left not staffed, at an off the items that
// stayed so, and what the kill switch's rule still acts on (see owed). A
// takeover's read does not come here: the fence before it did the on's steps
// 2 to 4, and its own advance acts on the view it read (see takeOverItems).
func (c *Coordinator) applyKillSwitch(ctx context.Context, state *KillSwitch, cause string) KillSwitchOutcome {
	view, before := viewOf(state), c.killSwitch
	settled := c.settleView(ctx, state)
	c.setView(&view)
	out := KillSwitchOutcome{On: state.On, Seq: state.Seq}
	raised := view.OnSeq > before.OnSeq
	if raised {
		out.Stopped, out.Unstaffed = c.kill(ctx)
	}
	turnedOff := !view.On && (before.On || raised)
	if turnedOff {
		c.armAutoCloses(ctx)
		out.StayedUnstaffed, out.StayedUnstaffedUnread = c.stayedUnstaffed(ctx, view.OnSeq)
	}
	if settled && !raised {
		c.advanceEvery(ctx)
	}
	out.Owed = c.owed()
	changed := !sameView(&view, &before)
	if (changed || settled) && c.held {
		c.publish()
	}
	if changed {
		c.logKillSwitch(ctx, state, cause, raised, turnedOff, &out)
	}
	return out
}

// stayedUnstaffed lists, in the order of their items, every item the on-row
// onSeq left not staffed (Decision 5; R4), as the off answers them: those
// whose item_awaiting_close names that on-row, as the records store finds
// them (Records.UnstaffedBy). It reads the records, not the fold: an item
// reopened since returns to an awaiting_close of the reopen's own, which
// names no on-row, and is listed all the same. Each comes with its state now,
// from its records: still awaiting its close, or closed by a person since,
// as auto-close paused while the switch was on. onSeq is the latest on-row of
// the view the off acted on, which its caller passes, not the coordinator's
// view now: a fence, as by a write of the on's steps that found the lease
// gone, clears that view, but not the on-row the off acted on, so the list is
// read by it, and in full, even once the coordinator is fenced. A read that
// fails does not fail the off, which has happened: it is logged, at Error,
// and unread reports that the list could not be read in full. With the list
// unread, no item is given; with an item's load failed, the item is given
// with no state.
func (c *Coordinator) stayedUnstaffed(ctx context.Context, onSeq int64) (items []UnstaffedItem, unread bool) {
	ids, err := c.p.Records.UnstaffedBy(ctx, onSeq)
	if err != nil {
		c.log.ErrorContext(ctx, "the items the kill switch left unstaffed could not be read", "onSeq", onSeq, "err", err)
		return nil, true
	}
	slices.Sort(ids)
	for _, id := range ids {
		u := UnstaffedItem{Item: id}
		if s, err := c.load(ctx, id); err != nil {
			c.failed(ctx, id, fmt.Errorf("read the state of an item the kill switch left unstaffed: %w", err))
			unread = true
		} else {
			u.State = s.Status()
		}
		items = append(items, u)
	}
	return items, unread
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
// staffed each item with no run, queued or started (see step). It returns
// what that did of the kill (see carryOutKill).
func (c *Coordinator) kill(ctx context.Context) (stopped []RunStop, unstaffed []item.ID) {
	c.p.Turns.StopAll()
	c.p.Sessions.EndAll()
	for _, run := range slices.Sorted(maps.Keys(c.runs)) {
		c.drop(run)
	}
	c.cancelAutoCloses()
	return c.carryOutKill(func() { c.advanceEvery(ctx) })
}

// carryOutKill runs advance, which advances the items of the account, whose
// steps halt each run that owes the kill of the view's latest on-row, and
// leave not staffed each item with no run that owes it (see step). Of what
// owed that kill before it (see owing), it returns, in the order of their
// items, the runs whose stop is recorded, each at its latest round, for the
// reason its halt recorded, and the items left not staffed.
func (c *Coordinator) carryOutKill(advance func()) (stopped []RunStop, unstaffed []item.ID) {
	stops, owing := c.owing(c.killSwitch.OnSeq)
	advance()
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

// takeOverItems carries on the items a takeover read (Decision 12, the
// takeover's steps 5 and 6): it advances each item of ids, its advances
// walking no queue, then walks the queue once, under the view of the kill
// switch the takeover read, state. The steps act on that view as on any
// other: a run that owes the kill of its latest on-row is halted, and an item
// with no run that owes it is left not staffed, before that item's next round
// or start; no turn starts while the view is on, nor of a run that owes the
// kill (see begin); and an item awaiting its close arms its auto-close only
// while the view is off, counting from the off (see armAutoClose). The fence
// at the takeover's start stopped every turn, ended every session, dropped
// every run and cancelled every auto-close timer, the on's steps 2 to 4, so
// the takeover runs neither the on's steps nor the off's (see
// applyKillSwitch): it advances no item twice, and does not list the items an
// on left not staffed, which an off answers, not a takeover. The view's
// change is logged, "kill switch set" with the cause takeover, when it acts:
// the view is on, the advance stopped a run or left an item not staffed for
// an on-row, or something is still owed (Contract C3). A takeover that finds
// an off, with nothing owed, logs nothing.
func (c *Coordinator) takeOverItems(ctx context.Context, ids []item.ID, state *KillSwitch) {
	out := KillSwitchOutcome{On: state.On, Seq: state.Seq}
	out.Stopped, out.Unstaffed = c.carryOutKill(func() {
		c.advanceAll(ctx, ids)
		c.walk(ctx)
	})
	out.Owed = c.owed()
	if state.On || len(out.Stopped)+len(out.Unstaffed)+len(out.Owed) > 0 {
		c.logKillSwitch(ctx, state, causeTakeover, state.OnSeq > 0, false, &out)
	}
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
// that stayed so, and whether their list could not be read in full, so that a
// list it could not read never reads as one with no item; then what the rule
// still acts on. It carries no person's text.
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
		attrs = append(attrs, "stayedUnstaffed", stayed, "stayedUnstaffedUnread", out.StayedUnstaffedUnread)
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
