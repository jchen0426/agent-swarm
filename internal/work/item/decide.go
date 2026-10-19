package item

import (
	"slices"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

const (
	// MaxWrapUps caps a run's wrap-up rounds, those lost to an outage or a
	// takeover aside: a convergence revoked twice gets no third (spec
	// Section 5.6).
	MaxWrapUps = 2
	// MaxWrapUpRounds caps the wrap-up rounds a run opens in all, those lost
	// to an outage or a takeover included: each raises the sessions' token
	// allowance, and a platform that kept failing in wrap-up rounds would
	// otherwise hold a run in them (spec Section 5.6).
	MaxWrapUpRounds = 2 * MaxWrapUps
	// MaxCrashes is how many crashed turns in a row remove an agent from a
	// run (spec Section 8.2).
	MaxCrashes = 2
	// roundShare is the part of a round's limit that must be left of the
	// wall clock for a normal round to open: a third.
	roundShare = 3
)

// Ending is what the coordinator knows when a round ends.
type Ending struct {
	EndedBy EndedBy
	// EndedAt is when the round ended, in database time.
	EndedAt time.Time
	// Turns holds how each agent's turn ended, as the runner reported it; a
	// turn it never reported counts as interrupted.
	Turns map[string]TurnClass
	// Spent, Settled and DaySpent are as RoundEnded holds them, and
	// DailyCap is the deployment's.
	Spent    int64
	Settled  bool
	DaySpent int64
	DailyCap int64
	// FailedCalls counts the model calls noticed on the board in the round as
	// refused, failed or of unknown outcome: those the round's report holds,
	// and those noticed as the round ends.
	FailedCalls int
	// Report is the run's board as the round ended.
	Report board.Report
	// TriggerResolved says the item's trigger resolved (Plan 7c).
	TriggerResolved bool
	// Unseen says the instance ending the round did not see its turns: it did
	// not act on the round, as a takeover finds a round past its deadline, or
	// acted on it too late, with less than a third of its span left. The turns
	// are what the board and the deadline say, which cannot tell a round no
	// agent ran from one that found nothing.
	Unseen bool
}

// tookPart reports whether an agent took part in a round, as the coordinator
// saw it: one of its turns took part. A round that ended by an outage, and one
// the instance that ended it did not see, show none.
func (re *RoundEnded) tookPart() bool {
	if re.EndedBy == EndedByOutage || re.Unseen {
		return false
	}
	for _, c := range re.Turns {
		if c.tookPart() {
			return true
		}
	}
	return false
}

// tookPart reports whether a turn that ended as c took part in its round: it
// ended, finished or was interrupted. A turn refused, failed at the model
// vendor, crashed or not started in time could not act, and took no part.
func (c TurnClass) tookPart() bool {
	return c == TurnEnded || c == TurnFinished || c == TurnInterrupted
}

// End decides how the run's open round ends: the record the coordinator
// writes before any of its effects. In this order, the run stops when the
// round was the wrap-up round of a hypothesis still converged, in which an
// agent took part (converged), when the day's spend reached the cap (cut),
// when the trigger resolved, or when no agent is left (not staffed).
// Otherwise a converged hypothesis gets a wrap-up round, whatever the
// budget, while the run has had fewer than MaxWrapUps, those lost to an
// outage or a takeover aside, and has opened fewer than MaxWrapUpRounds in
// all; then the run stops when its budget is used, or when the round was
// quiet; else a normal round follows.
func (r *Run) End(e *Ending) RoundEnded {
	i := len(r.Rounds) - 1
	o := &r.Rounds[i].Opened
	re := RoundEnded{
		Round: o.Round, EndedBy: e.EndedBy, EndedAt: e.EndedAt, Turns: map[string]TurnClass{},
		Spent: e.Spent, Settled: e.Settled, DaySpent: e.DaySpent, Entries: e.Report.Facts.Entries, Unseen: e.Unseen,
	}
	re.Rounds, re.WallClock = r.usage()
	counted := e.EndedBy != EndedByOutage
	if counted {
		for _, a := range o.Agents {
			c, ok := e.Turns[a]
			if !ok {
				c = TurnInterrupted
			}
			re.Turns[a] = c
			if c == TurnCrashed && r.crashes(a, i)+1 >= MaxCrashes {
				re.Removed = append(re.Removed, a)
			}
		}
		if o.WrapUp == "" {
			re.Rounds++
			re.WallClock += max(e.EndedAt.Sub(o.OpenedAt), 0)
		}
	}
	for i := range e.Report.Assessment.Hypotheses {
		if h := &e.Report.Assessment.Hypotheses[i]; h.Converged {
			re.Converged = append(re.Converged, h.ID)
		}
	}
	re.Quiet = quiet(o, &re, e)
	re.Decision = r.decide(o, &re, e)
	return re
}

// quiet reports whether a round is quiet: its agents had their chance, and
// found nothing. It did not end by an outage, the instance ending it saw its
// turns, its model calls settled and none was noticed in it as refused,
// failed or of unknown outcome, it left no entry, and every turn took part:
// it ended, finished or was interrupted.
func quiet(o *RoundOpened, re *RoundEnded, e *Ending) bool {
	if e.EndedBy == EndedByOutage || e.Unseen || !e.Settled || e.FailedCalls > 0 || re.Entries > 0 {
		return false
	}
	return !slices.ContainsFunc(o.Agents, func(a string) bool { return !re.Turns[a].tookPart() })
}

func (r *Run) decide(o *RoundOpened, re *RoundEnded, e *Ending) Decision {
	stop := func(reason StopReason) Decision { return Decision{Next: NextStop, Stop: reason} }
	switch {
	case o.WrapUp != "" && re.tookPart() && slices.Contains(re.Converged, o.WrapUp):
		return stop(StopConverged)
	case e.DaySpent >= e.DailyCap:
		return stop(StopCut)
	case e.TriggerResolved:
		return stop(StopTriggerResolved)
	case len(o.Agents) == len(re.Removed):
		return stop(StopNotStaffed)
	case len(re.Converged) > 0 && r.wrapUps(re) < MaxWrapUps && r.wrapUpRounds() < MaxWrapUpRounds:
		return Decision{Next: NextWrapUp, WrapUp: re.Converged[0]}
	case r.exhausted(re):
		return stop(StopBudgetExhausted)
	case re.Quiet:
		return stop(StopQuiet)
	}
	return Decision{Next: NextRound}
}

// exhausted reports whether the run used its budget: its rounds, its wall
// clock, so much of it that less than a third of a round's limit is left,
// or its tokens.
func (r *Run) exhausted(re *RoundEnded) bool {
	b := r.Created.Budget
	left := b.WallClock - re.WallClock
	return re.Rounds >= b.Rounds || left < r.Created.RoundLimit/roundShare || left <= 0 || re.Spent >= b.Tokens
}

// Open returns the run's next round as decided, opening at now, in database
// time. A normal round's deadline is its limit, cut to the wall clock left;
// a wrap-up round, outside the rounds and wall-clock budgets, has its whole
// limit, and its sessions may spend one round's share of the token budget
// above the run's spend when it opens (spec Section 5.6).
func (r *Run) Open(now time.Time) RoundOpened {
	b := r.Created.Budget
	o := RoundOpened{Round: len(r.Rounds) + 1, OpenedAt: now, Budget: b.Tokens, Agents: r.Agents()}
	limit := r.Created.RoundLimit
	if l := r.Latest(); l != nil && l.Ended != nil && l.Ended.Decision.Next == NextWrapUp {
		o.WrapUp = l.Ended.Decision.WrapUp
		o.Budget = max(b.Tokens, l.Ended.Spent+b.Tokens/int64(max(b.Rounds, 1)))
	} else {
		_, used := r.usage()
		limit = min(limit, b.WallClock-used)
	}
	o.Deadline = now.Add(limit)
	return o
}

// Stop returns the run's stop record: the reason its latest round decided,
// with the budget it says the run used, or not staffed before its first
// round. A converged run's conclusion is the hypothesis of its wrap-up
// round, which stayed converged through it; another run's is the conclusion
// given, the board's as the round ended.
func (r *Run) Stop(conclusion board.EntryID) RunStopped {
	st := RunStopped{Reason: StopNotStaffed, Conclusion: conclusion}
	if l := r.Latest(); l != nil && l.Ended != nil {
		e := l.Ended
		st.Reason, st.Rounds, st.WallClock, st.Spent, st.Settled = e.Decision.Stop, e.Rounds, e.WallClock, e.Spent, e.Settled
		if st.Reason == StopConverged {
			st.Conclusion = l.Opened.WrapUp
		}
	}
	return st
}
