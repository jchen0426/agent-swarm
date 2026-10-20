package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// ErrNoItem marks a view of an item that has no records.
var ErrNoItem = errors.New("app: no such item")

// View is an item as the admin API shows it (Contract C4), read from the
// stores, off the coordinator's lock.
type View struct {
	ID item.ID
	// State is the item's state: queued, investigating or awaiting_close.
	State string
	// Stop is why the item's run stopped, or, for an item that awaits its
	// close without a run, as one opened at the daily cap does, why it does:
	// empty while the run goes on.
	Stop string
	// HeldByLaterBuild says that a later build wrote a record of the item, or
	// an event of its run's board, that this build cannot read (Decision 19):
	// a view that says so says nothing else.
	HeldByLaterBuild bool
	// Rounds are the run's rounds, in order, the open one among them.
	Rounds []RoundView
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

// Views reads items as the admin API shows them, from the stores: the work
// records and the boards. It takes no lock of the coordinator's, and writes
// nothing.
type Views struct {
	records Records
	boards  *Boards
}

// NewViews returns the views of the items records keeps, whose runs' boards
// boards reads.
func NewViews(records Records, boards *Boards) *Views {
	return &Views{records: records, boards: boards}
}

// Item folds an item's records and reads its board's report for each round.
// An item with a record or board event of a later version is a View with
// HeldByLaterBuild alone. An id with no records fails with ErrNoItem; an
// item whose records or board this build reads but cannot fold fails with
// what the fold or the replay found.
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
	view := View{ID: id, State: string(s.Status())}
	if s.Run == nil {
		if s.Closing != nil {
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

// roundView is a round's view, with the board's report of it.
func roundView(r *item.Round, b *board.State) RoundView {
	rv := RoundView{Round: r.Opened.Round, WrapUp: r.Opened.WrapUp != "", Report: b.Report(r.Opened.Round)}
	if e := r.Ended; e != nil {
		rv.Ended, rv.Quiet, rv.TurnEnds = string(e.EndedBy), e.Quiet, slices.Clone(e.TurnEnds)
		rv.FailedCalls, rv.FailedToolCalls, rv.Settled = e.FailedCalls, e.FailedToolCalls, e.Settled
	}
	return rv
}
