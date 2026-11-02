package app

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// Lifecycle is the snapshot of the coordinator's lifecycle that readers take
// without a lock (Decision 4): the coordinator's view of the kill switch, and
// whether it is unsettled, the limit, the items investigating as the limit
// counts them, and the queue in its order. A fenced coordinator's is empty.
type Lifecycle struct {
	// KillSwitch is the coordinator's view of the kill switch: whether it is
	// on, its latest row, its latest on-row and its latest off-row's time
	// (Decision 5).
	KillSwitch KillSwitch
	// KillSwitchUnsettled says a kill switch write's outcome is unknown: the
	// switch may be on though the view is off, and no work starts until a read
	// settles the view (R37).
	KillSwitchUnsettled bool
	Limit               int
	// Investigating counts as the limit counts: the items investigating, and
	// the queued items admitted or promoted, which have not started yet.
	Investigating int
	// Waiting holds the queued items neither admitted nor promoted, in the
	// queue's order.
	Waiting []item.ID
}

// Lifecycle returns the coordinator's snapshot. It takes no lock, as Skipped
// does, so that it never waits for a takeover or a walk.
func (c *Coordinator) Lifecycle() Lifecycle {
	p := c.lifecycle.Load()
	if p == nil {
		return Lifecycle{}
	}
	out := *p
	out.Waiting = slices.Clone(p.Waiting)
	return out
}

// entry is what the coordinator's account keeps of an item that is not
// closed (Decision 4): its state, when it opened, the opening record's time,
// which ranks it in the queue, and, while it is queued, whether a person
// promoted it and whether the walk admitted it. An admission is a mark here,
// not a record: the same records give the same admissions, as a takeover's
// walk admits again. It keeps the item's fold too, as the latest load or
// record left it, which the kill switch's rules read without loading the item
// (Decision 5; see owed and owing).
type entry struct {
	status   item.Status
	opened   time.Time
	promoted bool
	admitted bool
	state    *item.State
}

// counts reports whether the item counts among the items investigating, as
// the limit counts them: it investigates, or it is queued, admitted or
// promoted, and its start is due.
func (e entry) counts() bool {
	return e.status == item.StatusInvestigating || e.status == item.StatusQueued && (e.admitted || e.promoted)
}

// waits reports whether the item waits in the queue: queued, neither
// admitted nor promoted.
func (e entry) waits() bool {
	return e.status == item.StatusQueued && !e.admitted && !e.promoted
}

// refresh brings the account's entry of the item id to s, its state now, as
// every load and every record does (Decision 4), and publishes the snapshot
// again. A fenced coordinator keeps no account.
func (c *Coordinator) refresh(id item.ID, s *item.State) {
	if !c.held {
		return
	}
	c.enter(id, s)
	c.publish()
}

// enter sets the account's entry of the item id to s, its state now. A queued
// item keeps its admission mark, which it drops once it starts or leaves the
// queue; an item closed, one with no opening, and one this holding skips
// leave the account.
func (c *Coordinator) enter(id item.ID, s *item.State) {
	status := s.Status()
	if s.Opened == nil || status == item.StatusClosed || c.isSkipped(id) {
		delete(c.items, id)
		return
	}
	queued := status == item.StatusQueued
	c.items[id] = entry{
		status: status, opened: s.OpenedAt, promoted: queued && s.Promotion != nil, admitted: queued && c.items[id].admitted, state: s,
	}
}

// unreadable has an item whose records cannot be read, as err says, leave the
// account (Decision 4): one whose records do not fold, or that a later build
// holds. The walk then never reaches it, so it holds up no other (spec
// Section 8.2), and the first load that succeeds puts it back. An item whose
// load merely failed keeps its entry, which its retry refreshes.
func (c *Coordinator) unreadable(id item.ID, err error) {
	if errors.Is(err, item.ErrInvalid) || ofLaterVersion(err) {
		c.leave(id)
	}
}

// leave has the item id leave the account, and publishes the snapshot again
// if it was in it.
func (c *Coordinator) leave(id item.ID) {
	if _, ok := c.items[id]; ok {
		delete(c.items, id)
		c.publish()
	}
}

// rebuild makes the account anew from the states a takeover read, without
// admission marks (Decision 12), and publishes it: the items it skips, and
// those whose records do not fold, are not among them.
func (c *Coordinator) rebuild(states []*item.State) {
	clear(c.items)
	for _, s := range states {
		c.enter(s.ID, s)
	}
	c.publish()
}

// investigating counts the items investigating as the limit counts them
// (Decision 4): those that investigate, and the queued items admitted or
// promoted, which have not started yet.
func (c *Coordinator) investigating() int {
	n := 0
	for _, e := range c.items {
		if e.counts() {
			n++
		}
	}
	return n
}

// overLimit reports whether one more item investigating would take the
// items investigating past the limit and one (Decision 4): a promotion that
// would is refused.
func (c *Coordinator) overLimit() bool {
	return c.investigating()+1 > c.opts.Limit+1
}

// waiting returns the items waiting in the queue in its order, by their rank:
// the opening record's time, then the item's id (Decision 4; R40), not by when
// the account learned of them.
func (c *Coordinator) waiting() []item.ID {
	var ids []item.ID
	for id, e := range c.items {
		if e.waits() {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b item.ID) int {
		if n := c.items[a].opened.Compare(c.items[b].opened); n != 0 {
			return n
		}
		return cmp.Compare(a, b)
	})
	return ids
}

// walk admits queued items, in their order, while the items investigating
// are fewer than the limit and no kill switch write's outcome is unknown
// (Decision 4; R37): each admitted item is marked, which counts it, and
// advanced once. An item that starts stays counted. One that its start
// leaves not staffed, or that its load finds closed or cannot fold, leaves
// the queue, which frees its count, and the walk goes on. One whose start
// fails keeps its mark, its count and its place, and starts on its own retry,
// or on any later advance; the walk goes on. The advances the walk makes walk
// no queue of their own: they are part of its loop, which ends once the items
// investigating reach the limit, the queue ends, or the coordinator is fenced
// or its holding ends. A walk asked for while a walk or a takeover runs does
// nothing: the takeover walks once, at its end. While a write's outcome is
// unknown the walk admits none, so that admission staffs nothing: the read
// that settles the view walks again.
func (c *Coordinator) walk(ctx context.Context) {
	if c.walking {
		return
	}
	c.walking = true
	defer func() { c.walking = false }()
	advanced := map[item.ID]bool{}
	for c.held && ctx.Err() == nil && !c.unsettled && c.investigating() < c.opts.Limit {
		id, ok := c.next(advanced)
		if !ok {
			return
		}
		advanced[id] = true
		e := c.items[id]
		e.admitted = true
		c.items[id] = e
		c.publish()
		c.advance(ctx, id)
	}
}

// next returns the first item waiting in the queue that this walk has not
// advanced.
func (c *Coordinator) next(advanced map[item.ID]bool) (item.ID, bool) {
	for _, id := range c.waiting() {
		if !advanced[id] {
			return id, true
		}
	}
	return "", false
}

// admitted reports whether the walk admitted the item, which its step then
// starts (Decision 4).
func (c *Coordinator) admitted(id item.ID) bool {
	return c.items[id].admitted
}

// publish publishes the account's snapshot, with the view of the kill switch
// and its mark (see Lifecycle).
func (c *Coordinator) publish() {
	c.lifecycle.Store(&Lifecycle{
		KillSwitch: c.killSwitch, KillSwitchUnsettled: c.unsettled, Limit: c.opts.Limit, Investigating: c.investigating(), Waiting: c.waiting(),
	})
}

// counted gives out, an act's answer, the items investigating, as the limit
// counts them, and the limit, from the account as it stands once the act is
// done: its advance, and the walk that ends it, included (Contract C4; R14).
// It returns the answer.
func (c *Coordinator) counted(out *Outcome) Outcome {
	out.Consequences.Investigating, out.Consequences.Limit = c.investigating(), c.opts.Limit
	return *out
}

// investigatingWithout counts the items investigating, as the limit counts
// them, once the item id has left the account, as a close takes it out, and
// the walk that follows has admitted waiting items into the slots that
// frees, as a close's preview answers it (Decision 13). It counts every such
// admission as one that holds, as a start that fails keeps its mark: one that
// the item's start would leave not staffed, as at the daily cap, is counted
// all the same.
func (c *Coordinator) investigatingWithout(id item.ID) int {
	n, waiting := 0, 0
	for other, e := range c.items {
		switch {
		case other == id:
		case e.counts():
			n++
		case e.waits():
			waiting++
		}
	}
	return n + max(0, min(c.opts.Limit-n, waiting))
}
