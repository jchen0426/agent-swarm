package app

import (
	"context"

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
	c.killSwitch = viewOf(&v)
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
