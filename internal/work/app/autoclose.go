package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// autoCloseTimer is an item's auto-close timer: its cancel, and, by its
// address, whether it is still the item's timer when it fires (see
// autoClose).
type autoCloseTimer struct{ cancel func() }

// typeDelay is the auto-close delay of the work item type typ in rev, or zero
// when rev holds no such type: the delay an item_awaiting_close records, as
// its writer can read it (Decision 8).
func typeDelay(rev *config.Revision, typ string) time.Duration {
	if wt := rev.WorkItemType(typ); wt != nil {
		return wt.Settings().AutoClose
	}
	return 0
}

// autoCloseDelay is s's auto-close delay, by the rule delayOf gives, read
// through the coordinator's revisions. The default of spec Section 15 that a
// revision without the item's type gives is logged (R43).
func (c *Coordinator) autoCloseDelay(ctx context.Context, s *item.State) (time.Duration, error) {
	delay, defaultedIn, err := delayOf(ctx, c.p.Revisions, s)
	if defaultedIn != nil {
		c.log.WarnContext(ctx, "auto-close takes the default delay: the revision holds no type of the item's",
			"item", s.ID, "type", s.Opened.Type, "commit", defaultedIn.Commit(), "delay", config.DefaultAutoClose)
	}
	return delay, err
}

// delayOf is s's auto-close delay (Decision 8), as the coordinator arms its
// timer and the views give its time: the one its latest item_awaiting_close or
// item_reopened recorded, or, when that is zero, as in a record written before
// 7b-1, its work item type's autoClose.after in the revision s locked, or, for
// an item that never started, in the current revision, as revs gives them. A
// revision that holds no type of the item's, as the current one for an item
// that never started whose type it dropped, gives instead the default of spec
// Section 15 (R43): no revision would ever give the item its delay. Then
// defaultedIn is that revision, nil otherwise. It fails when it can read no
// revision: the locked one, or a current one, as before the registry loads
// its first.
func delayOf(ctx context.Context, revs Revisions, s *item.State) (delay time.Duration, defaultedIn *config.Revision, err error) {
	if s.Closing != nil && s.Closing.AutoClose > 0 {
		return s.Closing.AutoClose, nil, nil
	}
	var rev *config.Revision
	if s.Started != nil {
		if rev, err = revs.At(ctx, s.Started.Commit); err != nil {
			return 0, nil, fmt.Errorf("read revision %s: %w", s.Started.Commit, err)
		}
	} else if rev = revs.Current(); rev == nil {
		return 0, nil, ErrNoRevision
	}
	if wt := rev.WorkItemType(s.Opened.Type); wt != nil {
		return wt.Settings().AutoClose, nil, nil
	}
	return config.DefaultAutoClose, rev, nil
}

// armAutoClose arms the auto-close timer of s, an item awaiting its close, at
// its deadline (Decision 8): s.AutoCloseAt its delay (see autoCloseDelay) and
// the kill switch's latest off, as the coordinator's view holds it. It
// cancels the item's timer armed before, so that an item has one at most.
// While the kill switch is on it arms none, and reads no delay: auto-close
// pauses, and the off arms every such timer again, counting from the off (see
// armAutoCloses). A delay it cannot read leaves the item without a timer: the
// read is logged as a step that failed, and the item's retry is armed, whose
// advance tries the arming again after the retry wait.
func (c *Coordinator) armAutoClose(ctx context.Context, s *item.State) {
	c.cancelAutoClose(s.ID)
	if c.killSwitch.On {
		return
	}
	delay, err := c.autoCloseDelay(ctx, s)
	if err != nil {
		c.failed(ctx, s.ID, fmt.Errorf("read the auto-close delay of item %s: %w", s.ID, err))
		if c.held {
			c.retry(s.ID)
		}
		return
	}
	c.armAt(s.ID, s.AutoCloseAt(delay, c.killSwitch.OffAt), delay)
}

// armAutoCloses arms again the auto-close timer of every item of the account
// that awaits its close, in the order of their items (R33), as the kill
// switch's off does (Decision 8): each counts from the off, the view's latest
// off-row's time, unless the item entered awaiting_close after it (see
// armAutoClose). It arms from the account's fold of each item, reading no
// item, so that the off never waits: a timer that a later change of its item
// made stale records nothing when it fires (see autoClose).
func (c *Coordinator) armAutoCloses(ctx context.Context) {
	for _, id := range slices.Sorted(maps.Keys(c.items)) {
		if e := c.items[id]; c.held && e.status == item.StatusAwaitingClose {
			c.armAutoClose(ctx, e.state)
		}
	}
}

// armAt arms the item's auto-close timer at at, under delay, in place of the
// one armed before.
func (c *Coordinator) armAt(id item.ID, at time.Time, delay time.Duration) {
	c.cancelAutoClose(id)
	t := &autoCloseTimer{}
	t.cancel = c.p.Timers.At(at, func() { c.autoClose(id, t, delay) })
	c.autoCloses[id] = t
}

// cancelAutoClose cancels the item's auto-close timer, if it has one.
func (c *Coordinator) cancelAutoClose(id item.ID) {
	if t := c.autoCloses[id]; t != nil {
		t.cancel()
		delete(c.autoCloses, id)
	}
}

// cancelAutoCloses cancels every auto-close timer. A cancel is no port event,
// so the loop needs no order (R33).
func (c *Coordinator) cancelAutoCloses() {
	for _, t := range c.autoCloses {
		t.cancel()
	}
	clear(c.autoCloses)
}

// autoClose is t, the auto-close timer of an item, armed under delay (see
// armAutoClose). It fires under the lock, and does nothing unless the
// coordinator holds, its holding's context live, and t is still the item's
// timer: one cancelled, or armed again since, is stale, and one that fires
// between a drain's first moment and its fence is the next leader's, which
// arms it again. It records nothing while the kill switch is on, and arms
// nothing: auto-close pauses, and the off arms the timer again (Decision 8).
// So too while a kill switch write's outcome is unknown, as the switch may be
// on: the read that settles the view advances every item, which arms the
// timer again (R37). It loads the item, and records item_closed, automatic,
// due at the item's deadline, by no one and for no request (R7), only while
// the item awaits its close and its deadline, computed again from its state
// now, with the delay its records hold, or else delay, and the kill switch's
// latest off, has passed on the coordinator's clock: a promotion or a close
// made the timer stale, and it records nothing. A timer that fires before
// that deadline, as after a correction of the coordinator's clock back since
// its arming, records nothing and arms itself again at the deadline (R41), a
// retry wait from now at least: a clock that holds its reading, as clock.DB
// holds its last one after a correction back, would otherwise have it fire
// again at once, and again, for as long as the clock holds. A load or a
// record that fails is logged as a step that failed, and the timer is armed
// again after the retry wait. So is a record the records store refuses with
// item.ErrNotDue, which it would stamp before its due (R41), but that refusal
// is logged at Info: it is an expected race while the instance's clock runs
// ahead of the database's. One that meets a later version skips the item. An
// automatic close has no effect of its own (Decision 8): the item's run, if
// it has one, was finished before the item awaited its close, and the step
// that armed the timer forgot it.
func (c *Coordinator) autoClose(id item.ID, t *autoCloseTimer, delay time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.holds() || c.autoCloses[id] != t {
		return
	}
	delete(c.autoCloses, id)
	if c.killSwitch.On || c.unsettled {
		return
	}
	s, err := c.load(c.ctx, id)
	if err == nil {
		if s.Status() != item.StatusAwaitingClose {
			return
		}
		if s.Closing.AutoClose > 0 {
			delay = s.Closing.AutoClose
		}
		due := s.AutoCloseAt(delay, c.killSwitch.OffAt)
		if now := c.p.Clock.Now(); now.Before(due) {
			// The coordinator's clock may hold its reading, as clock.DB holds its
			// last one after a correction back: a timer armed at the deadline
			// would wait the span from that reading, fire before the deadline
			// again, and so on, until the clock moves on. It waits the retry
			// wait at least.
			at := now.Add(c.opts.Retry)
			if due.After(at) {
				at = due
			}
			c.armAt(id, at, delay)
			return
		}
		if _, err = c.record(c.ctx, s, rec{body: item.Body{Closed: &item.Closed{Auto: true, Due: due}}}); err == nil {
			return
		}
	}
	switch {
	case ofLaterVersion(err):
		c.skip(c.ctx, id, err)
		return
	case errors.Is(err, item.ErrNotDue):
		// An expected race while the instance's clock runs ahead of the
		// database's (R41), not a step that failed.
		c.log.InfoContext(c.ctx, "an automatic close came before its due on the database's time, and is tried again", "item", id, "err", err)
	default:
		c.failed(c.ctx, id, err)
	}
	if c.held {
		c.armAt(id, c.p.Clock.Now().Add(c.opts.Retry), delay)
	}
}
