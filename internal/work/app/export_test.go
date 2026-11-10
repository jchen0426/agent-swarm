package app

import (
	"context"
	"slices"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// SetKillSwitchViewForTest sets the coordinator's view of the kill switch to
// v, its state, its latest row, its latest on-row and its latest off-row's
// time, under its lock, and acts on nothing: no public path leaves a run held
// in memory that owes a kill, since every raise of the latest on-row drops
// every run, so a test of what such a run does sets the view itself.
//
//nolint:gocritic // hugeParam: the hook's contract (Task 12) passes the view by value.
func (c *Coordinator) SetKillSwitchViewForTest(v KillSwitch) {
	c.mu.Lock()
	defer c.mu.Unlock()
	view := viewOf(&v)
	c.setView(&view)
}

// AdvanceForTest advances item id under the coordinator's lock, under the
// coordinator's own context, as a timer's retry would, while the coordinator
// is held.
func (c *Coordinator) AdvanceForTest(_ context.Context, id item.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held {
		c.advance(c.ctx, id)
	}
}

// OpenLockedForTest opens in through openLocked, as Ingest does: under the
// coordinator's lock, with the holding checked and ctx bound to its epoch.
func (c *Coordinator) OpenLockedForTest(ctx context.Context, in *OpenInput) (item.ID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.held {
		return "", ErrNotHeld
	}
	ctx, cancel := context.WithCancel(holding.WithEpoch(ctx, c.term.Epoch))
	defer cancel()
	defer context.AfterFunc(c.ctx, cancel)()
	return c.openLocked(ctx, in)
}

// RevisionMissedForTest is the commit whose revision a service mark of the
// build needed and could not read, "" when none did (R175).
func (bv *BoardView) RevisionMissedForTest() string { return bv.revisionMissed }

// StripsForTest returns the items whose strips the watch keeps, sorted.
func (w *BoardWatch) StripsForTest() []item.ID {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]item.ID, 0, len(w.strips))
	for k := range w.strips {
		out = append(out, k.item)
	}
	slices.Sort(out)
	return out
}

// FlightsForTest counts the flights that run: the probe, the projections'
// builds and the strips' builds.
func (w *BoardWatch) FlightsForTest() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(w.building) + len(w.stripping)
	if w.probing != nil {
		n++
	}
	return n
}
