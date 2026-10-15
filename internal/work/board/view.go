package board

import "sort"

// View is what an agent sees of its run's board in a round (spec Section
// 5.2): the snapshot the coordinator froze when the round started, and the
// agent's own entries since. Other authors' entries of the round appear only
// in the next round's snapshot.
type View struct {
	Round    int
	Snapshot int64
	WrapUp   EntryID
	// Assessment ranks the snapshot's hypotheses.
	Assessment Assessment
	// Tasks are the snapshot's tasks, with who holds them in this round.
	Tasks []TaskView
	// Entries are the snapshot's entries after the reader's cursor, in commit
	// order, without turn ends.
	Entries []Event
	// Own are the agent's entries after the snapshot, in commit order,
	// without turn ends.
	Own []Event
}

// TaskView is a task as the snapshot shows it in a round.
type TaskView struct {
	ID     EntryID  `json:"id"`
	Title  string   `json:"title"`
	Tags   []string `json:"tags"`
	Author Author   `json:"author"`
	// Holder holds the task through round Until; empty when nobody does.
	Holder string `json:"holder,omitempty"`
	Until  int    `json:"until,omitempty"`
	// Completion is the entry that completed the task, if any.
	Completion EntryID `json:"completion,omitempty"`
}

// View returns what agent sees in round c.Round: the snapshot's entries with
// a sequence number after since, without other agents' notices, its
// assessment and its tasks; and the agent's own entries committed after the
// snapshot, the proposals recorded for it included. An own hypothesis that
// merged into an entry the agent cannot see yet does not name it, so a merge
// never shows another author's entry before the next round.
func (s *State) View(c *Context, agent string, since int64) (View, error) {
	n := sort.Search(len(s.events), func(i int) bool { return s.events[i].Seq > c.Snapshot })
	snap, err := Replay(s.settings, s.events[:n])
	if err != nil {
		return View{}, err
	}
	v := View{
		Round: c.Round, Snapshot: c.Snapshot, WrapUp: c.WrapUp,
		Assessment: snap.Assess(), Tasks: snap.tasks(c), Entries: []Event{}, Own: []Event{},
	}
	for i := range s.events[:n] {
		if e := &s.events[i]; e.Seq > since && shown(e, agent) {
			v.Entries = append(v.Entries, *e)
		}
	}
	me := Author{Kind: AuthorAgent, Name: agent}
	for i := n; i < len(s.events); i++ {
		e := s.events[i]
		proposed := e.Body.Proposal != nil && e.Body.Proposal.Proposer == agent
		if (e.Author != me && !proposed) || e.Body.TurnEnd != nil {
			continue
		}
		if h := e.Body.Hypothesis; h != nil && h.AliasOf != "" {
			if target, _ := s.event(h.AliasOf); target.Seq > c.Snapshot && target.Author != me {
				hidden := *h
				hidden.AliasOf = ""
				e.Body.Hypothesis = &hidden
			}
		}
		v.Own = append(v.Own, e)
	}
	return v, nil
}

// shown reports whether an agent's view shows an entry of the snapshot: every
// entry but turn ends, round entries, which the view's round says, and
// notices about other agents' calls.
func shown(e *Event, agent string) bool {
	if n := e.Body.Notice; n != nil && n.Agent != agent {
		return false
	}
	return e.Body.TurnEnd == nil && e.Body.Round == nil
}

// tasks returns the board's tasks, with who holds each in round c.Round.
func (s *State) tasks(c *Context) []TaskView {
	lease := c.Settings.Limits.orDefault().LeaseRounds
	out := []TaskView{}
	for i := range s.events {
		e := &s.events[i]
		if e.Body.Task == nil {
			continue
		}
		t := TaskView{ID: e.ID, Title: e.Body.Task.Title, Tags: e.Body.Task.Tags, Author: e.Author, Completion: s.completed[e.ID]}
		if holder, claim, held := s.holder(e.ID, c.Round, lease); held {
			t.Holder, t.Until = holder.Name, claim.Round+lease-1
		}
		out = append(out, t)
	}
	return out
}
