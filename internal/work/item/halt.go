package item

// Halt returns the records that stop the run now, for reason, killed or
// closed, given what the coordinator knew at the halt (Decision 3).
//
// When the run's latest round decided its stop, which is not recorded yet,
// the run stops as that round decided (Run.Stop), whatever reason asks: the
// round's own decision is what measurement reads. A resumed run's latest
// round decided nothing it still acts on (see decided): before its next
// round it stops for reason (R67).
//
// Otherwise a round still open ends stopped first, deciding to stop for
// reason. It records what the instance knew at the stop and reads nothing
// more: the turn classes e gives, an agent without one interrupted, the turn
// ends e gives, and the run's spend; no failed call, and never settled. It is
// never quiet, converges nothing and removes no agent. A normal round counts
// toward the rounds and the wall clock, to the stop; a wrap-up round counts
// toward neither, but uses up a wrap-up and counts among the wrap-up rounds
// opened, as a wrap-up round no agent took part in does (see wrapUps).
//
// The run then stops for reason, with the latest ended round's rounds and
// wall clock, the stopped one included; the run's spend at the halt
// (e.Spent), unsettled; the board's conclusion at the halt; and, killed, the
// kill switch's on-row (e.KilledBy).
func (r *Run) Halt(reason StopReason, e *Ending) []Body {
	conclusion := e.Report.Assessment.Conclusion
	l := r.Latest()
	if r.decided().Next == NextStop {
		st := r.Stop(conclusion)
		return []Body{{RunStopped: &st}}
	}
	var bodies []Body
	st := RunStopped{Reason: reason, Spent: e.Spent, Conclusion: conclusion}
	st.Rounds, st.WallClock = r.usage()
	if l != nil && l.Ended == nil {
		re := r.stopRound(reason, e)
		bodies = append(bodies, Body{RoundEnded: &re})
		st.Rounds, st.WallClock = re.Rounds, re.WallClock
	}
	if reason == StopKilled {
		st.Killed = e.KilledBy
	}
	return append(bodies, Body{RunStopped: &st})
}

// stopRound is how the run's open round ends when the run stops for reason
// (see Halt).
func (r *Run) stopRound(reason StopReason, e *Ending) RoundEnded {
	o := &r.Latest().Opened
	re := RoundEnded{
		Round: o.Round, EndedBy: EndedByStopped, EndedAt: e.EndedAt, Turns: map[string]TurnClass{}, TurnEnds: turnEnds(o.Agents, e.TurnEnds),
		Spent: e.Spent, DaySpent: e.DaySpent, Entries: e.Report.Facts.Entries, Decision: Decision{Next: NextStop, Stop: reason},
	}
	for _, a := range o.Agents {
		c, ok := e.Turns[a]
		if !ok {
			c = TurnInterrupted
		}
		re.Turns[a] = c
	}
	re.Rounds, re.WallClock = r.usage()
	if o.WrapUp == "" {
		re.Rounds++
		re.WallClock += max(e.EndedAt.Sub(o.OpenedAt), 0)
	}
	return re
}
