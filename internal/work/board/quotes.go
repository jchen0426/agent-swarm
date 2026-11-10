package board

import (
	"cmp"
	"slices"
)

// Quotes are a hypothesis's quotes on each side (design D4), in the rule's
// order, and the candidates not picked.
type Quotes struct {
	Supports, Refutes         []Event
	MoreSupports, MoreRefutes int
}

// Quotes returns hypothesis h's quotes, at most perSide on each side, by spec
// Section 10.2's fixed rule (design D4). The candidates are the evidence
// entries behind the records the assessment counts, so that a quote never
// shows evidence the score ignores: on the supporting side, those behind
// every supporting record of a contributor whose latest stance supports; on
// the refuting side, those behind each refuting contributor's latest record.
// Aliases' records count, as they do for the score. An alias's quotes are its
// hypothesis's; an id of no hypothesis has none.
func (s *State) Quotes(h EntryID, perSide int) Quotes {
	t := tallyOf(s.records(s.canonical[h]))
	var supports, refutes []EntryID
	for _, r := range t.supporting {
		supports = append(supports, r.backing...)
	}
	for _, r := range t.latest {
		if r.stance == Refutes {
			refutes = append(refutes, r.backing...)
		}
	}
	var q Quotes
	q.Supports, q.MoreSupports = s.pick(supports, perSide)
	q.Refutes, q.MoreRefutes = s.pick(refutes, perSide)
	return q
}

// quoteClasses are the quote rule's classes in its order, by the author kind
// of the evidence entry: the platform's first, then a person's, then an
// agent's (design D4).
var quoteClasses = []AuthorKind{AuthorPlatform, AuthorHuman, AuthorAgent}

// pick is the quote rule's walk over candidate evidence entries: the
// distinct ones, ordered by class and each class newest first by board seq,
// taking the first entry of each source not taken yet until perSide are
// taken. more is the distinct candidates left, those skipped for a source
// taken included.
func (s *State) pick(candidates []EntryID, perSide int) (picked []Event, more int) {
	seen := make(map[EntryID]bool, len(candidates))
	entries := make([]*Event, 0, len(candidates))
	for _, id := range candidates {
		if e, ok := s.event(id); ok && !seen[id] {
			seen[id] = true
			entries = append(entries, e)
		}
	}
	slices.SortFunc(entries, func(a, b *Event) int {
		return cmp.Or(
			cmp.Compare(slices.Index(quoteClasses, a.Author.Kind), slices.Index(quoteClasses, b.Author.Kind)),
			cmp.Compare(b.Seq, a.Seq),
		)
	})
	taken := make(map[Source]bool)
	for _, e := range entries {
		if src := e.Body.Evidence.Provenance.Source; len(picked) < perSide && !taken[src] {
			taken[src] = true
			picked = append(picked, *e)
		}
	}
	return picked, len(entries) - len(picked)
}

// Proposals are the proposals citing h or one of its aliases, in commit
// order. An alias's are its hypothesis's; an id of no hypothesis has none.
func (s *State) Proposals(h EntryID) []Event {
	canon, ok := s.canonical[h]
	if !ok {
		return nil
	}
	var out []Event
	for i := range s.events {
		if p := s.events[i].Body.Proposal; p != nil && s.canonical[p.Hypothesis] == canon {
			out = append(out, s.events[i])
		}
	}
	return out
}

// SupportBy counts, by source, the distinct entries behind a's live
// supporting records on h while a's latest stance on h supports; empty
// otherwise (design D6). An entry behind two records counts once, under its
// own evidence's source, as the quotes read it. It reads the records the
// assessment counts, as the quotes do, so a proposer's support is shown as
// the score counts it. An alias's is its hypothesis's.
func (s *State) SupportBy(h EntryID, a Author) map[Source]int {
	counts := make(map[Source]int)
	seen := make(map[EntryID]bool)
	for _, r := range tallyOf(s.records(s.canonical[h])).supporting {
		if r.contributor != a {
			continue
		}
		for _, id := range r.backing {
			if e, ok := s.event(id); ok && !seen[id] {
				seen[id] = true
				counts[e.Body.Evidence.Provenance.Source]++
			}
		}
	}
	return counts
}

// Events is a copy of the board's events in commit order, for the feed:
// changing the slice changes nothing of the board's. As with Event, the
// entries share their bodies with the board's, which no caller changes.
func (s *State) Events() []Event {
	return slices.Clone(s.events)
}
