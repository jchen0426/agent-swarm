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
	// TurnEnds holds the agents whose own turn end the board recorded in the
	// round (board.RoundFacts.Ended): their end_turn calls that succeeded.
	// Quiet and taking part rest on them, not on Turns (Decision 9).
	TurnEnds []string
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
	// FailedToolCalls counts the round's failed tool calls, as the MCP
	// gateway's records counted them once the round's write calls had their
	// grace (Decision 20, R2).
	FailedToolCalls int
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

// seenWithoutFailure reports whether the instance that ended a round saw it
// and the round left none of the failures the gateways' records hold
// (Decision 9), as quiet and taking part both need: it did not end by an
// outage, nor unseen; no model call of it was still in flight once the wait
// for them ended, none was refused, failed or of unknown outcome, and no tool
// call of it failed.
func (re *RoundEnded) seenWithoutFailure() bool {
	return re.EndedBy != EndedByOutage && !re.Unseen && re.Settled && re.FailedCalls == 0 && re.FailedToolCalls == 0
}

// tookPart reports whether an agent took part in a round, as the platform's
// records tell it (Decision 9): the instance saw the round, it left no
// failure, and at least one agent of it ended its own turn on the board. The
// classes do not count: an interrupted turn, which left no turn end, never
// takes part.
func (re *RoundEnded) tookPart() bool {
	return re.seenWithoutFailure() && len(re.TurnEnds) > 0
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
		Round: o.Round, EndedBy: e.EndedBy, EndedAt: e.EndedAt, Turns: map[string]TurnClass{}, TurnEnds: turnEnds(o.Agents, e.TurnEnds),
		Spent: e.Spent, Settled: e.Settled, DaySpent: e.DaySpent, FailedCalls: e.FailedCalls, FailedToolCalls: e.FailedToolCalls,
		Entries: e.Report.Facts.Entries, Unseen: e.Unseen,
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
	re.Quiet = quiet(o, &re)
	re.Decision = r.decide(o, &re, e)
	return re
}

// turnEnds returns, in the round's order, those of the round's agents whose
// own turn end is among ends: a round's agents are those it invited, and no
// one else's turn end counts.
func turnEnds(agents, ends []string) []string {
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		if slices.Contains(ends, a) {
			out = append(out, a)
		}
	}
	return out
}

// quiet reports whether a round is quiet: its agents had their chance, and
// found nothing (Decision 9). The instance saw it, it left no failure and no
// entry of an agent or a person but turn ends, at least one agent was in it,
// and every agent of it ended its own turn on the board.
func quiet(o *RoundOpened, re *RoundEnded) bool {
	if !re.seenWithoutFailure() || re.Entries > 0 || len(o.Agents) == 0 {
		return false
	}
	return !slices.ContainsFunc(o.Agents, func(a string) bool { return !slices.Contains(re.TurnEnds, a) })
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
