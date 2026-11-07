package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/jchen0426/agent-swarm/internal/holding"
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
// nothing while fenced, or once that context ended, as a drain's first moment
// ends it before the drain's fence (Decision 13). A read that fails is logged
// as a step that failed, and changes nothing: the next renewal reads the row
// again. One that fails as the context ends is no failure to log: the next
// leader reads the switch at its takeover.
func (c *Coordinator) RefreshKillSwitch(_ context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.holds() {
		return
	}
	state, err := c.p.Controls.KillSwitch(c.ctx)
	switch {
	case err == nil:
		c.applyKillSwitch(c.ctx, &state, causeRenewal)
	case c.ctx.Err() == nil:
		c.failed(c.ctx, "", fmt.Errorf("read the kill switch after a renewal found a newer row: %w", err))
	}
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
// It answers ErrNotHeld when the coordinator is not held, or its context
// ended, as at a drain's start, before it reads anything; when the store's
// guard refused the write; and when the coordinator no longer holds once the
// request is done, fenced as the view's steps went on, as by a halt that found
// the lease gone, or its context ended as it wrote the row or read the switch
// again (Decision 13; see heldThrough): the row may be the table's then, and
// the next leader's takeover applies it. During a drain, the on is recorded
// through RecordKillSwitchOn instead. It answers ErrInvalid for a request of
// another form; ErrStale when a row newer than After stands and the state
// asked does not hold; and, for a write whose outcome is still unknown, or
// that did not land, the store's error, which the admin API answers 503.
func (c *Coordinator) SetKillSwitch(ctx context.Context, on bool, a *Act) (answer KillSwitchOutcome, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.holds() {
		return KillSwitchOutcome{}, ErrNotHeld
	}
	defer func() { answer, err = heldThrough(c, answer, err) }()
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

// RecordKillSwitchOn records the kill switch's on during a drain, and acts on
// no run (Decision 5): the admin API calls it once SetKillSwitch answered
// ErrNotHeld while the leader holds the lease and drains, with epoch, the
// draining holding's. It takes the coordinator's lock only to fence it if the
// drain has not yet: a coordinator held whose context ended, as a drain's
// first moment ends it, is fenced at once, as the drain's fence would fence it,
// every turn stopped, every session ended and every run dropped, so that no
// step starts a turn once the row commits; one held with a live context does
// not drain, and the on answers ErrNotHeld (R24), which the admin API's next
// try sends to SetKillSwitch. It then lets the lock go, so that the drain's
// fence, which waits for that lock under the leader's own, never waits on the
// write, and writes the row under a context of its own: ctx's values without
// its cancellation, epoch, and a deadline of Options.WriteTimeout. Neither the
// drain's end of the holding's context nor a client that leaves ends the
// write; the timeout bounds it, and the store's guard checks the epoch against
// the lease in the write's own transaction.
//
// It checks the request as the on is checked (see checkSwitch), which reads
// nothing the coordinator's lock guards, before it takes the lock. It answers
// the switch's state and latest row, changed as the store says, and deferred,
// with no run stopped and nothing owed: the fenced coordinator holds no
// account, and the next leader's takeover reads the table and halts every run
// that owes the kill before its next round (Decision 12). It sets no view and
// no mark (R37), and logs "kill switch set", deferred, with nothing owed
// (Contract C3). A preview records nothing, and the fenced coordinator has no
// account to preview against: it answers ErrNotHeld, which the admin API
// answers as the drain's, as it answers every act during a drain. The guard's
// refusal, as once the draining holding ended, answers ErrNotHeld. Any other
// failed write answers its error, with no second read: ErrStale, as the store
// gives it, when a newer row stands; and any other, a lost answer included,
// an error the admin API answers 503, as the client then reads the switch on
// the next leader before it sends the on again.
func (c *Coordinator) RecordKillSwitchOn(ctx context.Context, epoch int64, a *Act) (KillSwitchOutcome, error) {
	reason, err := c.checkSwitch(true, a)
	if err != nil {
		return KillSwitchOutcome{}, err
	}
	if a.Preview || !c.fencedForTheDrain() {
		return KillSwitchOutcome{}, ErrNotHeld
	}
	writing, cancel := context.WithTimeout(holding.WithEpoch(context.WithoutCancel(ctx), epoch), c.opts.WriteTimeout)
	defer cancel()
	state, changed, err := c.p.Controls.SetKillSwitch(writing, true, a.Actor.Subject, reason, a.After)
	switch {
	case errors.Is(err, ErrLeaseLost):
		return KillSwitchOutcome{}, ErrNotHeld
	case err != nil:
		return KillSwitchOutcome{}, fmt.Errorf("record the kill switch's on during the drain: %w", err)
	}
	cause := causeAnswer
	if changed {
		cause = causeAct
	}
	out := KillSwitchOutcome{On: state.On, Seq: state.Seq, Changed: changed, Deferred: true}
	c.logKillSwitch(ctx, &state, epoch, cause, false, false, false, &out)
	return out, nil
}

// fencedForTheDrain makes sure, under the coordinator's lock, that the
// coordinator is fenced for the drain's on (Decision 5), and reports whether
// it is: one held whose context ended is fenced now, as the drain's fence
// would fence it (see fence); one not held is fenced already; one held with a
// live context does not drain (R24).
func (c *Coordinator) fencedForTheDrain() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case !c.held:
	case c.ctx.Err() == nil:
		return false
	default:
		c.fence()
	}
	return true
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
// fires under the lock, and does nothing unless the coordinator holds, its
// holding's context live, and t is still the pending read: one cancelled, as
// once an answer settled the view, or armed again since, is stale, and one
// that fires between a drain's first moment and its fence reads nothing, as
// the next leader reads the switch at its takeover. A read that succeeds sets
// the view, which settles it, logged with the cause reread (see
// applyKillSwitch).
func (c *Coordinator) reread(t *rereadTimer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.holds() || c.rereading != t {
		return
	}
	c.rereading = nil
	if state, read := c.readAgain(c.ctx); read {
		c.applyKillSwitch(c.ctx, &state, causeReread)
	}
}

// checkSwitch checks a request to the kill switch (Decisions 5 and 9): who
// acts, a subject; After, 0 or above; its actor's role, as May decides it
// (R79): an on's, a responder or an admin, and an off's, an admin, as only an
// admin turns the switch off, a preview included, and RecordKillSwitchOn's on
// during a drain too; and its reason, which an on needs, but for a preview.
// It returns the reason the row keeps: its code, and its text checked and
// redacted as a person's text (see checkReason), with the rules of the
// current revision, since the kill switch is no item's; both empty for none. An error never quotes the reason.
func (c *Coordinator) checkSwitch(on bool, a *Act) (board.Reason, error) {
	switch {
	case a == nil:
		return board.Reason{}, fmt.Errorf("%w: no act", ErrInvalid)
	case a.Actor.Subject == "":
		return board.Reason{}, fmt.Errorf("%w: an act names who acts", ErrInvalid)
	case a.After < 0:
		return board.Reason{}, fmt.Errorf("%w: after is the kill switch's latest row its sender saw, 0 before the first", ErrInvalid)
	}
	kind := ActSwitchOff
	if on {
		kind = ActSwitchOn
	}
	if _, err := May(a.Actor, kind, nil); err != nil {
		return board.Reason{}, err
	}
	if on && a.Reason == nil && !a.Preview {
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
		if out.Consequences.Stops, out.Consequences.Unstaffed, err = c.wouldKill(c.ctx, &state); err != nil {
			return KillSwitchOutcome{}, err
		}
	}
	return out, nil
}

// wouldKill is what turning the kill switch on would stop, its table holding
// state: the on applies the next row while the switch is off, and its latest
// on-row while it is on, and acts only when that raises the view's latest
// on-row (see applyKillSwitch). Then the runs that owe that on-row would stop,
// and the items with no run that owe it would not be staffed (see owing).
//
// A run whose open round missed its deadline, which this instance does not
// hold, the on's advance ends as missed before it halts the run, by Decision
// 12's order (see runStep), and the run stops for that round's decision when
// it decides a stop, not killed. So the preview reads that end as missed
// would, recording nothing (see missedEnd and ending), and leaves out a run
// whose missed round decides its stop, so that it names what the answer will
// (the scoped re-review's N1). A read that fails fails the preview.
func (c *Coordinator) wouldKill(ctx context.Context, state *KillSwitch) ([]RunStop, []item.ID, error) {
	onSeq := state.OnSeq
	if !state.On {
		onSeq = state.Seq + 1
	}
	if onSeq <= c.killSwitch.OnSeq {
		return nil, nil, nil
	}
	stops, unstaffed := c.owing(onSeq)
	kept := stops[:0]
	for _, stop := range stops {
		s := c.items[stop.Item].state
		if l := s.Run.Latest(); l != nil && l.Ended == nil && c.missedRound(s.Run.ID, l) {
			decided, err := c.missedDecidesStop(ctx, s)
			if err != nil {
				return nil, nil, err
			}
			if decided {
				continue
			}
		}
		kept = append(kept, stop)
	}
	return kept, unstaffed, nil
}

// missedDecidesStop reports whether the open round of s's run, which missed
// its deadline, ends deciding the run's stop, as missed would end it, read
// and not recorded.
func (c *Coordinator) missedDecidesStop(ctx context.Context, s *item.State) (bool, error) {
	r, settled, err := c.missedEnd(ctx, s)
	if err != nil {
		return false, err
	}
	e, _, err := c.ending(ctx, s.Run.ID, r, settled)
	if err != nil {
		return false, err
	}
	return s.Run.End(e).Decision.Next == item.NextStop, nil
}

// owing lists, in the order of their items, what in the account owes the
// kill of the on-row onSeq (Decision 5): the runs that would stop killed, each
// at its latest round, 0 before its first, and the items with no run, queued
// or started, that would be left not staffed. A run whose latest round decided
// its stop, which its halt then records for that reason (see runStop), is
// stopped but not killed: it is in neither list, so that a preview names
// what the answer will. A run whose open round missed its deadline is listed
// as killed here, from the account alone: the preview reads its missed
// round's end further (see wouldKill), and carryOutKill tells its stop by
// its state once the advance is done.
func (c *Coordinator) owing(onSeq int64) (stops []RunStop, unstaffed []item.ID) {
	for _, id := range slices.Sorted(maps.Keys(c.items)) {
		s := c.items[id].state
		switch {
		case !s.Owes(onSeq):
		case s.Running():
			if stop := runStop(s, item.StopKilled); stop.Reason == item.StopKilled {
				stops = append(stops, stop)
			}
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
// see armAutoCloses), and, last, every item the latest on left not staffed is
// listed, with its state now, or the list is marked unread when it cannot be
// read in full (see stayedUnstaffed). Nothing restarts, and nothing waits for
// what is owed. A mark it cleared lets the work that waited go on, whether or
// not the view changed: before the off's list is read, every item of the
// account is advanced, in the order of their items, and the queue is walked
// (see advanceEvery), as the on's steps do already when they ran. What that
// advance carried out of a kill still owed, the runs it halted for the view's
// latest on-row and the items it left not staffed (see carryOutKill), is
// returned as the on's steps return it while the view is on, so that an on's
// answer never leaves out a run that ran when its request came, which it
// neither stopped nor owes. While the view is off, the off's list, read after
// that advance, gives those items with their states now, and the answer names
// none of them among its stops or the items it left unstaffed (Contract C3;
// Review Focus 4): that list is what the operator promotes from. A view that
// changed, or a mark cleared, publishes the snapshot; a view that changed
// logs "kill switch set": an on, its step 6. It returns what it did: the
// switch's state and latest row, the runs it stopped, the items it left not
// staffed, at an off the items that stayed so, and what the kill switch's
// rule still acts on (see owed). A takeover's read does not come here: the
// fence before it did the on's steps 2 to 4, and its own advance acts on the
// view it read (see takeOverItems).
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
	}
	if settled && !raised {
		stopped, unstaffed := c.carryOutKill(func() { c.advanceEvery(ctx) })
		if view.On {
			out.Stopped, out.Unstaffed = stopped, unstaffed
		}
	}
	if turnedOff {
		out.StayedUnstaffed, out.StayedUnstaffedUnread = c.stayedUnstaffed(ctx, view.OnSeq)
	}
	out.Owed = c.owed()
	changed := !sameView(&view, &before)
	if (changed || settled) && c.held {
		c.publish()
	}
	if changed {
		c.logKillSwitch(ctx, state, c.term.Epoch, cause, raised, turnedOff, c.held, &out)
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
// leave not staffed each item with no run that owes it (see step). Of the
// items that owed that kill before it (see owing), it returns, in the order
// of their items, the runs whose recorded stop is killed, each at its latest
// round (a run whose round had decided another stop is not among them), and
// the items with no run left not staffed, each told by its state once advance
// is done, as its loads found it: an item the account held with no run may
// have one whose creation landed with its answer lost, which its advance
// found and halted.
func (c *Coordinator) carryOutKill(advance func()) (stopped []RunStop, unstaffed []item.ID) {
	stops, owing := c.owing(c.killSwitch.OnSeq)
	for i := range stops {
		owing = append(owing, stops[i].Item)
	}
	slices.Sort(owing)
	advance()
	for _, id := range owing {
		e, ok := c.items[id]
		switch {
		case !ok:
		case e.state.Run != nil && e.state.Run.Stopped != nil:
			if stop := stopOf(e.state); stop.Reason == item.StopKilled {
				stopped = append(stopped, stop)
			}
		case e.state.Run == nil && e.status == item.StatusAwaitingClose:
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
		c.logKillSwitch(ctx, state, c.term.Epoch, causeTakeover, state.OnSeq > 0, false, c.held, &out)
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
// latest row and latest on-row, who wrote the latest row, epoch, the epoch of
// the holding it acted for, the cause, and whether the on was deferred; once
// the on's steps ran, the runs they stopped, by number, and the items they
// left not staffed; once the off's steps ran, the items that stayed so, and
// whether their list could not be read in full, so that a list it could not
// read never reads as one with no item; then what the rule still acts on. A
// coordinator that holds no account, accounted false, logs neither what the
// on's steps did nor what is still owed, which its account no longer gives:
// a deferred on's, recorded once the drain fenced it, and an act's or a
// takeover's once a fence emptied the account as the steps went on. The off's
// list, read from the records by its on-row, is logged all the same. It reads
// nothing the coordinator's lock guards, so that RecordKillSwitchOn calls it
// without the lock, and it carries no person's text.
func (c *Coordinator) logKillSwitch(ctx context.Context, state *KillSwitch, epoch int64, cause string, raised, turnedOff, accounted bool, out *KillSwitchOutcome) {
	attrs := []any{"on", state.On, "seq", state.Seq, "onSeq", state.OnSeq, "by", state.By, "epoch", epoch, "cause", cause, "deferred", out.Deferred}
	if raised && accounted {
		attrs = append(attrs, "stopped", len(out.Stopped), "unstaffed", out.Unstaffed)
	}
	if turnedOff {
		stayed := make([]item.ID, 0, len(out.StayedUnstaffed))
		for _, u := range out.StayedUnstaffed {
			stayed = append(stayed, u.Item)
		}
		attrs = append(attrs, "stayedUnstaffed", stayed, "stayedUnstaffedUnread", out.StayedUnstaffedUnread)
	}
	if accounted {
		attrs = append(attrs, "owed", out.Owed)
	}
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
