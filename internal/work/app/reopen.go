package app

import (
	"context"
	"fmt"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// reopenKind is a person's reopen, as its request's digest names the act.
const reopenKind = "reopen"

// Reopen reopens a closed item for a person (spec Section 4.1; Decision 7),
// within Options.ReopenWindow of its latest close, as the database's time
// (Clock.Now) judges it, and answers what it did, or, previewed, what it would
// do. It takes one append, item_reopened, with who reopened the item, why, the
// act's request, which keeps the key's hash, never the key (Decision 13), and
// the awaiting_close the item returns to (see reopenedTo). Once the append
// commits, the item advances, and its step arms its auto-close, which counts
// from the reopen, or, while the kill switch is on, arms none, as auto-close
// pauses (Decision 8): the timer armed before, if any, is cancelled, and one
// that fires all the same finds itself stale. Its consequences give when the
// item closes by itself, unless its delay could not be read, or, while the
// kill switch is on, that its auto-close is paused. A reopen acts on no run,
// which stopped, and posts nothing. An item that is not closed changes
// nothing, and nothing is recorded (R10); one closed longer ago than the
// window is refused with a *Refused, CodeReopenWindowPassed. Every answer
// gives the items investigating, as the limit counts them, and the limit (see
// counted), which a reopen leaves as they are.
//
// Before it decides, Reopen looks for the act's key among the item's records
// (see findKey): a reopen it finds is a repeat, which records nothing, answers
// changed, as the reopen did (R14), and advances the item, whose step decides
// from its state now (see actFound); a key that named another act of the item
// is refused with ErrKeyReused. A reopen whose append fails is checked as
// carryOn says: one that landed, its answer lost, answers as a repeat, its item
// advanced, and one that did not changed nothing. A preview takes no key and
// skips the lookup: it runs the same checks on the same state, under the same
// lock, and answers the reopen's consequences, as a reopen now would have
// them, with Changed false, recording nothing and acting on nothing (Decision
// 13). The reason's text is checked and redacted before it is recorded (see
// checkReason).
//
// Reopen takes the coordinator's lock, and reads and writes under the
// coordinator's own context, which carries its holding's epoch, whatever ctx
// says. It answers ErrNotHeld when the coordinator is not held, or its context
// ended, as at a drain's start, before it reads anything, and when it no
// longer holds once the reopen is done (Decision 13; see heldThrough);
// ErrInvalid for an act of another form (see checkAct and checkReason);
// ErrSkipped for an item held by a later build; ErrNoItem for an id with no
// records; ErrKeyReused for a key that named another act of the item; a
// *Refused for an item closed longer ago than the window; and a failed read's
// error, or a failed append's.
func (c *Coordinator) Reopen(_ context.Context, id item.ID, a *Act) (answer Outcome, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.holds() {
		return Outcome{}, ErrNotHeld
	}
	defer func() { answer, err = heldThrough(c, answer, err) }()
	if err := checkAct(a); err != nil {
		return Outcome{}, err
	}
	s, err := c.target(c.ctx, id)
	if err != nil {
		return Outcome{}, err
	}
	reason, err := c.checkReason(s, a.Reason)
	if err != nil {
		return Outcome{}, err
	}
	req := requestOf(reopenKind, id, a)
	if !a.Preview {
		switch found, err := c.findKey(s, req); {
		case err != nil:
			return Outcome{}, err
		case found:
			c.actFound(s)
			return c.counted(&Outcome{Changed: true}), nil
		}
	}
	if s.Status() != item.StatusClosed {
		return c.counted(&Outcome{}), nil
	}
	if c.p.Clock.Now().After(s.ClosedAt.Add(c.opts.ReopenWindow)) {
		return Outcome{}, &Refused{Code: CodeReopenWindowPassed}
	}
	to, err := c.reopenedTo(s)
	if err != nil {
		return Outcome{}, err
	}
	if a.Preview {
		out := reopenOutcome(s, c.p.Clock.Now(), to.AutoClose, &c.killSwitch)
		out.Changed = false
		// A reopen leaves the items investigating as they are: the item
		// awaits its close.
		return c.counted(&out), nil
	}
	out, err := c.reopenNow(s, &item.Reopened{By: a.Actor.Subject, Reason: reason, Request: req, AwaitingClose: to})
	if err != nil {
		return Outcome{}, err
	}
	return c.counted(&out), nil
}

// reopenedTo is the awaiting_close that a reopen returns s, a closed item, to
// (Decision 7): its run's reason, the run's latest stop as ItemReason gives
// it, closed included; or, for an item with no run, not_staffed, for the
// cause R12 gives (see unstaffedCause). Its delay is the one autoCloseDelay
// reads, or zero when it cannot read one, as while no revision is current for
// an item that never started, so that no reopen fails for want of a revision:
// the timer reads the delay once it is armed, and tries again until it can
// (Decision 8). A load that fails is returned.
func (c *Coordinator) reopenedTo(s *item.State) (item.AwaitingClose, error) {
	to := item.AwaitingClose{Reason: item.StopNotStaffed}
	if s.Run != nil {
		// A closed item's run stopped: no close is recorded while it runs.
		to.Reason = item.ItemReason(s.Run.Stopped.Reason)
	} else {
		cause, err := c.unstaffedCause(s.ID)
		if err != nil {
			return item.AwaitingClose{}, err
		}
		to.Cause = cause
	}
	// A delay that cannot be read is recorded as zero: the step's arming reads
	// it again, and logs and tries again what fails.
	to.AutoClose, _ = c.autoCloseDelay(c.ctx, s)
	return to, nil
}

// unstaffedCause is the cause for which a reopen returns an item with no run
// to not_staffed (R12): the cause of the item's latest item_awaiting_close,
// which comes before its close, since a closed item takes no record but a
// reopen; or closed when it has none, as when it was closed while queued, or
// started without a run. A promotion since that record leaves its cause: an
// item not staffed at the daily cap, then promoted, then closed while queued,
// is reopened not staffed for the cause daily_cap. It reads the item's
// records, as findKey does, not the item's fold, which forgets the
// awaiting_close a promotion lifted. A load that fails is returned.
func (c *Coordinator) unstaffedCause(id item.ID) (item.Cause, error) {
	records, err := c.p.Records.Load(c.ctx, id)
	if err != nil {
		return "", fmt.Errorf("load item %s: %w", id, err)
	}
	for i := len(records) - 1; i >= 0; i-- {
		if ac := records[i].Body.AwaitingClose; ac != nil {
			return ac.Cause, nil
		}
	}
	return item.CauseClosed, nil
}

// reopenNow records reopened, the reopen of s, a closed item, then advances
// the item, whose step arms its auto-close, and answers what the reopen did
// (see reopenOutcome). An append that fails is checked as carryOn says: a
// reopen that landed, its answer lost, answers as a repeat (R14).
func (c *Coordinator) reopenNow(s *item.State, reopened *item.Reopened) (Outcome, error) {
	after, err := c.record(c.ctx, s, rec{body: item.Body{Reopened: reopened}})
	if err != nil {
		if _, _, err = c.carryOn(c.ctx, s.ID, reopened.Request, err); err != nil {
			return Outcome{}, err
		}
		return Outcome{Changed: true}, nil
	}
	c.advance(c.ctx, s.ID)
	return reopenOutcome(after, after.AwaitingSince, reopened.AwaitingClose.AutoClose, &c.killSwitch), nil
}

// reopenOutcome is what a reopen of s at at does (Contract C4), ks being the
// coordinator's view of the kill switch: the item awaits its close from at,
// and closes by itself delay after the latest of at, its trigger's resolution
// and the kill switch's latest off (R19), as armAutoClose arms it; or, while
// the switch is on, its auto-close is paused, and gives no time (Decision 8).
// A delay of zero, recorded so because it could not be read, gives no time
// either (Decision 8).
func reopenOutcome(s *item.State, at time.Time, delay time.Duration, ks *KillSwitch) Outcome {
	out := Outcome{Changed: true}
	switch {
	case ks.On:
		out.Consequences.AutoClosePaused = true
	case delay > 0:
		reopened := *s
		reopened.AwaitingSince = at
		out.Consequences.ClosesAt = reopened.AutoCloseAt(delay, ks.OffAt)
	}
	return out
}
