package item

import (
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Waits are a round end's waits (spec 5.6): the write calls' grace, the wait
// for model calls in flight, the settle's poll (rounds.go:291-295, :317-322),
// and one retry wait of a step that failed (Options.Retry).
type Waits struct{ Grace, Settle, Poll, Retry time.Duration }

// StopBy is the latest time the run stops (design D5's T), computed forward
// from its records, never from its start; latest is the item's latest
// record's time. It is an upper bound absent an outage, a takeover or a
// resume, which move it later:
//
//	T = D + W + max(0, B - U) + n*W + k*(L + W)
//
// D is the open round's deadline, at which it ends at the latest, or latest
// between rounds; and the next round, or the stop, follows a round's end at
// once. W is a round end's waits: its grace, then its settle's, whose
// decision lands at the settle's first poll at or after Grace and Settle, so
// up to a poll after them, and one retry wait of a failure. B is the wall
// clock with every extension (Run.Budget), and U the wall clock the counted
// normal rounds used, the open round's to D only when it is a normal round:
// a wrap-up round counts none (decide.go's End), and a normal round's
// deadline is its limit cut to the wall clock left (Run.Open). n is the
// normal rounds the budget still opens, each ending after its W. k is the
// wrap-up rounds the run may still have, each its whole limit L and its W:
// the counted wrap-ups left, an open one counted as no fault ends it, and no
// more than the wrap-up rounds it may still open (MaxWrapUps,
// MaxWrapUpRounds), since wrap-ups come before the budget (decide).
func (r *Run) StopBy(latest time.Time, w Waits) time.Time {
	wait, limit, b := w.Grace+w.Settle+w.Poll+w.Retry, r.Created.RoundLimit, r.Budget()
	rounds, used := r.usage()
	counted, opened := r.wrapUps(&RoundEnded{}), r.wrapUpRounds()
	d := latest
	if l := r.Latest(); l != nil && l.Ended == nil {
		d = l.Opened.Deadline
		if l.Opened.WrapUp == "" {
			rounds, used = rounds+1, used+max(d.Sub(l.Opened.OpenedAt), 0)
		}
	}
	n := max(0, b.Rounds-rounds)
	k := max(0, min(MaxWrapUps-counted, MaxWrapUpRounds-opened))
	return d.Add(wait + max(0, b.WallClock-used) + time.Duration(n)*wait + time.Duration(k)*(limit+wait))
}

// ConfirmingUntil is when the decision of the run's open wrap-up round lands
// at the latest (R146): the round's deadline, plus Grace, Settle and a Poll;
// false, with no time, while no wrap-up round is open (see Confirming).
func (r *Run) ConfirmingUntil(w Waits) (time.Time, bool) {
	if r.Confirming() == "" {
		return time.Time{}, false
	}
	return r.Latest().Opened.Deadline.Add(w.Grace + w.Settle + w.Poll), true
}

// Confirming is the hypothesis of the run's open wrap-up round while the run
// has not stopped, the spec's "in the wrap-up round" (R146), which
// board.StatusOf reads as confirming once it converged; "" otherwise: a
// normal round, a round that ended, and a stopped run name none.
func (r *Run) Confirming() board.EntryID {
	if l := r.Latest(); r.Stopped == nil && l != nil && l.Ended == nil {
		return l.Opened.WrapUp
	}
	return ""
}
