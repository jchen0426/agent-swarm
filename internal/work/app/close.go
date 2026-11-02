package app

import (
	"context"
	"fmt"
	"maps"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// closeKind is a person's close, as its request's digest names the act.
const closeKind = "close"

// Close closes an item for a person, from any state (spec Section 4.1;
// Decision 6), and answers what it did. A queued item, one awaiting its
// close, and one investigating with no run running, as before its run's
// creation or once its run's stop is recorded though its finish did not end,
// take one append: item_closed, with who closed it, why, and the act's
// request, which keeps the key's hash, never the key (Decision 13). An item
// whose run has not stopped takes one append of the records that halt the
// run, closed (see haltRecords), and item_closed: its round ends stopped,
// with the turn classes of a snapshot Close takes first, without dropping the
// run, since until the commit the close may not have happened. Once the
// append commits, Close drops the run (see drop), so that no turn of it
// starts again, whatever the advance that follows does; the advance runs the
// closed step (see closedStep). A closed item changes nothing, and nothing is
// recorded (R10).
//
// Close takes the coordinator's lock, and reads, writes and halts under the
// coordinator's own context, which carries its holding's epoch, whatever ctx
// says: a client that leaves cancels no halt (Decision 3). It answers
// ErrNotHeld when the coordinator is not held, before it reads anything;
// ErrInvalid for an act whose key or reason is of another form, or a
// preview, which it does not take; ErrSkipped for an item held by a later
// build; ErrNoItem for an id with no records; and a failed append's error.
func (c *Coordinator) Close(_ context.Context, id item.ID, a *Act) (Outcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.held {
		return Outcome{}, ErrNotHeld
	}
	if err := checkAct(a); err != nil {
		return Outcome{}, err
	}
	if a.Preview {
		return Outcome{}, fmt.Errorf("%w: a close takes no preview", ErrInvalid)
	}
	s, err := c.target(c.ctx, id)
	if err != nil || s.Status() == item.StatusClosed {
		return Outcome{}, err
	}
	out := Outcome{Changed: true, Consequences: Consequences{Closes: []item.ID{id}}}
	var recs []rec
	halts := s.Running()
	if halts {
		c.takeSnapshot(s.Run.ID)
		stop := RunStop{Item: id, Run: s.Run.ID}
		if l := s.Run.Latest(); l != nil {
			stop.Round = l.Opened.Round
		}
		for _, b := range c.haltRecords(c.ctx, s, item.StopClosed, 0) {
			recs = append(recs, rec{run: s.Run.ID, body: b})
			if b.RunStopped != nil {
				// A round that decided its stop stops the run as it decided.
				stop.Reason = b.RunStopped.Reason
			}
		}
		out.Consequences.Stops = []RunStop{stop}
	}
	req := requestOf(closeKind, id, a)
	var reason *board.Reason
	if a.Reason != nil {
		r := *a.Reason
		reason = &r
	}
	recs = append(recs, rec{body: item.Body{Closed: &item.Closed{By: a.Actor.Subject, Reason: reason, Request: &req}}})
	if _, err := c.record(c.ctx, s, recs...); err != nil {
		c.failed(c.ctx, id, err)
		return Outcome{}, err
	}
	if halts {
		c.drop(s.Run.ID)
		// The halt that read the run's snapshot committed.
		delete(c.snapshots, s.Run.ID)
	}
	c.advance(c.ctx, id)
	return out, nil
}

// haltSnapshot is what a halt reads of a run's round from the coordinator's
// memory (Decision 3): the round the coordinator held, and its turn classes
// as the round's deadline would have counted them when it was taken.
type haltSnapshot struct {
	round   int
	classes map[string]item.TurnClass
}

// takeSnapshot keeps in c.snapshots the snapshot of a run the coordinator
// holds: its round, and the turns that ended as they ended, those still
// running interrupted, or unstarted when they started too late to count, and
// those still starting unstarted, as the deadline counts them. A run the
// coordinator holds nothing of keeps the snapshot it has, if any, as a drop
// left it for a halt that has not committed.
func (c *Coordinator) takeSnapshot(run board.RunID) {
	r := c.runs[run]
	if r == nil {
		return
	}
	classes := make(map[string]item.TurnClass, len(r.exits)+len(r.turns)+len(r.starting))
	maps.Copy(classes, r.exits)
	for agent := range r.turns {
		classes[agent] = item.TurnInterrupted
		if r.startedLate[agent] {
			classes[agent] = item.TurnUnstarted
		}
	}
	for agent := range r.starting {
		classes[agent] = item.TurnUnstarted
	}
	c.snapshots[run] = haltSnapshot{round: r.round, classes: classes}
}

// haltRecords returns the records that stop s's run now, for reason, killed
// or closed, and, killed, killedBy, the kill switch's on-row (Decision 3; see
// item.Run.Halt): a decided stop, as its round decided it; or a round open
// ended stopped, then the run's stop; or the run's stop alone. The round's
// turn classes are its snapshot's, when the run has one of that round, or
// else the board's: ended for the agents whose turn end it holds, which Halt
// reads, with every agent it gives no class, as interrupted. The conclusion
// and the turn ends are the board's, and the spend the run's (R11). No read
// stops a halt: a board that cannot be read gives no conclusion and no turn
// end, and a spend that cannot be read gives the run's last recorded spend,
// its latest ended round's; each such read is logged as a step that failed.
func (c *Coordinator) haltRecords(ctx context.Context, s *item.State, reason item.StopReason, killedBy int64) []item.Body {
	run := s.Run
	round := 0
	if l := run.Latest(); l != nil {
		round = l.Opened.Round
	}
	e := &item.Ending{EndedAt: c.p.Clock.Now(), KilledBy: killedBy}
	report, err := c.p.Boards.Report(ctx, run.ID, round)
	if err != nil {
		c.failed(ctx, s.ID, fmt.Errorf("read the board of run %s, which halts: %w", run.ID, err))
	} else {
		e.Report, e.TurnEnds = report, report.Facts.Ended
	}
	switch snap, ok := c.snapshots[run.ID]; {
	case ok && snap.round == round:
		e.Turns = snap.classes
	case err == nil:
		e.Turns = map[string]item.TurnClass{}
		for _, agent := range report.Facts.Ended {
			e.Turns[agent] = item.TurnEnded
		}
	}
	if e.Spent, err = c.p.Spend.Spent(ctx, string(run.ID)); err != nil {
		c.failed(ctx, s.ID, fmt.Errorf("read the spend of run %s, which halts: %w", run.ID, err))
		e.Spent = lastSpent(run)
	}
	return run.Halt(reason, e)
}

// lastSpent is a run's last recorded spend: its latest ended round's.
func lastSpent(run *item.Run) int64 {
	for i := len(run.Rounds) - 1; i >= 0; i-- {
		if e := run.Rounds[i].Ended; e != nil {
			return e.Spent
		}
	}
	return 0
}

// drop drops a run's state in memory (Decision 3), as the fence does every
// run's: it keeps the run's snapshot (see takeSnapshot), cancels the run's
// timers, startAgain's, the deadline's and settle's among them, and deletes
// its entry; then it ends the run's sessions, stops the turns this instance
// holds of its latest round, and closes its upstream sessions. With the entry
// gone, Launched, Exited, startAgain, deadline and settle find no run: a
// launch that the stop cancelled reports to nothing, and nothing arms its
// retry. A run with no entry takes no snapshot and stops no turn, as this
// instance runs none of it: whatever removed the entry, the fence among them,
// stopped its turns. The rest is done all the same. Each part repeats safely,
// and none waits.
func (c *Coordinator) drop(run board.RunID) {
	var agents []string
	if r := c.runs[run]; r != nil {
		c.takeSnapshot(run)
		agents = r.agents()
		c.cancel(r)
		delete(c.runs, run)
	}
	c.p.Sessions.EndRun(run)
	for _, agent := range agents {
		c.p.Turns.Stop(run, agent)
	}
	c.p.UpstreamSessions.CloseRun(run)
}

// closedStep acts on a closed item (Decision 6). When the item has a run,
// whose stop is recorded, it drops the run (see drop), whose snapshot no
// halt reads, then posts the run's stop entry, which a board that holds it
// takes as a repeat, once and best effort: a post that fails is logged, and
// the item waits with no retry, so that an item whose board refuses its stop
// entry keeps a board without one, and is closed all the same. A post that
// meets a later version is returned, so that advance skips the item.
func (c *Coordinator) closedStep(ctx context.Context, s *item.State) error {
	run := s.Run
	if run == nil {
		return nil
	}
	c.drop(run.ID)
	delete(c.snapshots, run.ID)
	_, err := c.p.Boards.Stop(ctx, run.ID, stopInput(run.Stopped))
	switch {
	case err == nil:
	case ofLaterVersion(err):
		return err
	default:
		c.failed(ctx, s.ID, fmt.Errorf("post the stop entry of the closed item's run %s: %w", run.ID, err))
	}
	return nil
}
