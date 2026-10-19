package board

import "sort"

// RoundFacts is what a round produced, as the board tells it.
type RoundFacts struct {
	// Ended holds the agents that ended their turn in the round, sorted.
	Ended []string
	// Entries counts the round's entries of agents and people, turn ends
	// aside, and the proposals the gateway recorded for agents. Notices,
	// round entries, platform evidence and the stop entry are the
	// platform's own, and do not count: a round without entries is quiet as
	// far as the board can tell (spec Section 5.6).
	Entries int
}

// Report is what the coordinator reads of a run's board when one of its
// rounds ends: the board's assessment, what the round produced, the model
// calls the board holds a notice of, and whether the run stopped.
type Report struct {
	Assessment Assessment
	Facts      RoundFacts
	Stopped    bool
	noticed    map[string]bool
}

// Noticed reports whether the board holds a notice of the model call with
// the given id, in any round.
func (r *Report) Noticed(callID string) bool { return r.noticed[callID] }

// Report returns the board's report for round.
func (s *State) Report(round int) Report {
	r := Report{Assessment: s.Assess(), Facts: RoundFacts{Ended: []string{}}, Stopped: s.stop >= 0, noticed: map[string]bool{}}
	for i := range s.events {
		e := &s.events[i]
		if n := e.Body.Notice; n != nil && modelNotice(n.Code) {
			r.noticed[n.CallID] = true
		}
		if e.Round != round {
			continue
		}
		switch {
		case e.Body.TurnEnd != nil:
			r.Facts.Ended = append(r.Facts.Ended, e.Author.Name)
		case e.Author.IsContributor(), e.Body.Proposal != nil:
			r.Facts.Entries++
		}
	}
	sort.Strings(r.Facts.Ended)
	return r
}

// Stopped returns the board's stop entry, if the run stopped.
func (s *State) Stopped() (Event, bool) {
	if s.stop < 0 {
		return Event{}, false
	}
	return s.events[s.stop], true
}
