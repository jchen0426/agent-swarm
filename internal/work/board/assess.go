package board

import (
	"cmp"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// HypothesisView is one hypothesis's standing on the board.
type HypothesisView struct {
	ID        EntryID   `json:"id"`
	Key       key.Key   `json:"key"`
	Statement string    `json:"statement"`
	Author    Author    `json:"author"`
	Aliases   []EntryID `json:"aliases"`
	// Supporters and Refuters are the contributors whose latest stance supports
	// or refutes the hypothesis.
	Supporters []Author `json:"supporters"`
	Refuters   []Author `json:"refuters"`
	// Sources are the distinct sources of the supporters' live supporting evidence.
	Sources []Source `json:"sources"`
	// StandingRefutations are the refuting entries that block convergence.
	StandingRefutations []EntryID `json:"standingRefutations"`
	Rejected            bool      `json:"rejected"`
	Converged           bool      `json:"converged"`
	// Score is the number of supporters minus the number of refuters.
	Score int `json:"score"`
}

// Assessment ranks a board's hypotheses and names the run's conclusion.
type Assessment struct {
	Hypotheses []HypothesisView `json:"hypotheses"`
	// Conclusion is empty when the run has no conclusion.
	Conclusion EntryID `json:"conclusion,omitempty"`
}

// AsOf returns the events committed at or before t, so that a board can be
// assessed as of a moment (spec Section 4.3). Events must be in commit order.
func AsOf(events []Event, t time.Time) []Event {
	n := sort.Search(len(events), func(i int) bool { return events[i].At.After(t) })
	return events[:n]
}

// ReplayAsOf checks every event, as Replay does, and then returns the board as
// it stood at t. Checking the whole log first means that an entry out of
// commit order is found rather than cut away unseen.
func ReplayAsOf(settings Settings, events []Event, t time.Time) (*State, error) {
	if _, err := Replay(settings, events); err != nil {
		return nil, err
	}
	return Replay(settings, AsOf(events, t))
}

// Assess ranks the hypotheses by score, then by the number of distinct
// supporting sources, then by age, and picks the run's conclusion (spec
// Section 4.3).
func (s *State) Assess() Assessment {
	views := make([]HypothesisView, 0)
	for i := range s.events {
		e := &s.events[i]
		if e.Body.Hypothesis != nil && s.canonical[e.ID] == e.ID {
			views = append(views, s.view(e))
		}
	}
	sort.SliceStable(views, func(i, j int) bool { return outranks(&views[i], &views[j]) })
	return Assessment{Hypotheses: views, Conclusion: conclusion(views)}
}

// outranks orders hypotheses by score, then by distinct supporting sources.
func outranks(a, b *HypothesisView) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return len(a.Sources) > len(b.Sources)
}

// conclusion picks the conclusion from ranked hypotheses, as spec Section 4.3
// orders it: the top converged one; if none converged, the top one that no
// human rejected. A tie between the top two candidates means no conclusion,
// and so does a top candidate that nobody supports.
func conclusion(ranked []HypothesisView) EntryID {
	candidates := filterViews(ranked, func(v *HypothesisView) bool { return v.Converged })
	if len(candidates) == 0 {
		candidates = filterViews(ranked, func(v *HypothesisView) bool { return !v.Rejected })
	}
	switch {
	case len(candidates) == 0:
		return ""
	case len(candidates) > 1 && !outranks(candidates[0], candidates[1]):
		return ""
	case len(candidates[0].Supporters) == 0:
		return ""
	default:
		return candidates[0].ID
	}
}

func filterViews(views []HypothesisView, keep func(*HypothesisView) bool) []*HypothesisView {
	var out []*HypothesisView
	for i := range views {
		if keep(&views[i]) {
			out = append(out, &views[i])
		}
	}
	return out
}

func (s *State) view(e *Event) HypothesisView {
	count := s.countStances(e.ID, nil)
	v := HypothesisView{
		ID:                  e.ID,
		Key:                 e.Body.Hypothesis.Key,
		Statement:           e.Body.Hypothesis.Statement,
		Author:              e.Author,
		Aliases:             s.aliases(e.ID),
		Supporters:          count.supporters,
		Refuters:            count.refuters,
		Sources:             count.sources,
		StandingRefutations: make([]EntryID, 0, len(count.standing)),
		Rejected:            count.rejected,
		Score:               len(count.supporters) - len(count.refuters),
	}
	for i := range count.standing {
		v.StandingRefutations = append(v.StandingRefutations, count.standing[i].entry)
	}
	conv := s.settings.Convergence
	v.Converged = len(v.Supporters) >= conv.MinContributors &&
		len(v.Sources) >= conv.MinSources &&
		len(v.StandingRefutations) == 0 &&
		!v.Rejected
	return v
}

func (s *State) aliases(h EntryID) []EntryID {
	out := make([]EntryID, 0)
	for i := range s.events {
		id := s.events[i].ID
		if s.events[i].Body.Hypothesis != nil && id != h && s.canonical[id] == h {
			out = append(out, id)
		}
	}
	return out
}

// stanceRecord is one contributor's stance on a hypothesis, as one entry states it.
type stanceRecord struct {
	contributor Author
	stance      Stance
	seq         int64
	entry       EntryID
	sources     []Source
}

// stanceCount is a hypothesis's stances, counted under the board rules.
type stanceCount struct {
	supporters []Author
	refuters   []Author
	sources    []Source
	standing   []stanceRecord
	rejected   bool
}

// countStances counts the stances on canonical hypothesis h. Only each
// contributor's latest live stance counts. extra, when set, is a stance not yet
// posted, used to see what posting it would change.
func (s *State) countStances(h EntryID, extra *stanceRecord) stanceCount {
	records := s.records(h)
	if extra != nil {
		records = append(records, *extra)
	}
	latest := make(map[Author]stanceRecord, len(records))
	for _, r := range records {
		latest[r.contributor] = r
	}
	sources := make(map[Source]bool)
	for _, r := range records {
		if r.stance == Supports && latest[r.contributor].stance == Supports {
			for _, src := range r.sources {
				sources[src] = true
			}
		}
	}
	count := stanceCount{supporters: make([]Author, 0), refuters: make([]Author, 0), rejected: s.rejected[h]}
	for c := range latest {
		if latest[c].stance == Supports {
			count.supporters = append(count.supporters, c)
		} else {
			count.refuters = append(count.refuters, c)
		}
	}
	slices.SortFunc(count.supporters, compareAuthors)
	slices.SortFunc(count.refuters, compareAuthors)
	count.sources = slices.Sorted(maps.Keys(sources))
	for _, c := range count.refuters {
		r := latest[c]
		if s.stands(&r, len(count.supporters), len(count.refuters)) {
			count.standing = append(count.standing, r)
		}
	}
	return count
}

// stands reports whether a refuting record blocks convergence. A dismissed
// refutation never does. Otherwise a human's refutation stands, and an agent's
// stands unless supporters outnumber refuters by the refutation margin.
func (s *State) stands(r *stanceRecord, supporters, refuters int) bool {
	if s.dismissed[r.entry] {
		return false
	}
	if r.contributor.Kind == AuthorHuman {
		return true
	}
	return supporters < refuters+s.settings.Convergence.RefutationMargin
}

// records returns the stance records on canonical hypothesis h in commit
// order. Evidence with a stance gives one record. A hypothesis gives its
// author a supporting record, backed by the cited evidence the author wrote.
// Retracted evidence backs nothing.
func (s *State) records(h EntryID) []stanceRecord {
	var out []stanceRecord
	for i := range s.events {
		e := &s.events[i]
		switch {
		case e.Body.Evidence != nil:
			if r, ok := s.evidenceRecord(e, h); ok {
				out = append(out, r)
			}
		case e.Body.Hypothesis != nil:
			if r, ok := s.authorRecord(e, h); ok {
				out = append(out, r)
			}
		}
	}
	return out
}

func (s *State) evidenceRecord(e *Event, h EntryID) (stanceRecord, bool) {
	ev := e.Body.Evidence
	if ev.Hypothesis == "" || s.canonical[ev.Hypothesis] != h {
		return stanceRecord{}, false
	}
	if _, gone := s.retracted[e.ID]; gone {
		return stanceRecord{}, false
	}
	return stanceRecord{
		contributor: e.Author,
		stance:      ev.Stance,
		seq:         e.Seq,
		entry:       e.ID,
		sources:     []Source{ev.Provenance.Source},
	}, true
}

func (s *State) authorRecord(e *Event, h EntryID) (stanceRecord, bool) {
	if s.canonical[e.ID] != h {
		return stanceRecord{}, false
	}
	var sources []Source
	for _, id := range e.Body.Hypothesis.Evidence {
		ev, ok := s.event(id)
		if !ok || ev.Author != e.Author {
			continue
		}
		if _, gone := s.retracted[id]; gone {
			continue
		}
		sources = append(sources, ev.Body.Evidence.Provenance.Source)
	}
	if len(sources) == 0 {
		return stanceRecord{}, false
	}
	return stanceRecord{contributor: e.Author, stance: Supports, seq: e.Seq, entry: e.ID, sources: sources}, true
}

func compareAuthors(a, b Author) int {
	return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
}
