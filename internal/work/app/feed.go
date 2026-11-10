package app

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// FeedLine is one line of the feed (design D8): a record's or an entry's,
// with what its words need that the entry itself does not hold.
type FeedLine struct {
	At        time.Time
	Record    *item.Record // a record's line; nil for an entry's
	Event     *board.Event // an entry's line; nil for a record's
	Repeats   int          // the notices a notice's line stands for (D9); 1 otherwise
	Key       key.Key      // the key the line names: a stance's, a hypothesis's (an alias's hypothesis's), a ruling's, a proposal's, a wrap-up's, a stop's conclusion's
	Typed     *item.Record // the record a platform evidence's ref names (R150); nil for none
	Title     string       // a trigger event's title from the inbound store (D8)
	Kept      bool         // false once the store keeps no title
	Firing    int          // the instances firing once an applied item_triggered was applied
	Round     *item.Round  // a round divider's round, as the records hold it
	Retracted time.Time    // when an evidence line's entry was retracted; zero while it stands
	Task      *board.Event // a claim's or a completion's task
	Of        board.Author // a dismissed refutation's author, or a retraction's evidence's
}

// The refs of platform evidence (R150): the opening's, item:<id>, and a
// trigger update's, trigger:<event id>.
const (
	refItem    = "item:"
	refTrigger = "trigger:"
)

// feed is the board's feed (design D8): the item's records and its run's
// entries, events, merged by their database times, at equal time records
// first, then each store's own seq (R149's left out; see recordLines and
// entryLines), with the titles of the trigger events it shows (see titled).
// a is b's assessment; b and a are nil, and events empty, for an item with
// no run.
func (v *Views) feed(ctx context.Context, records []item.Record, s *item.State, b *board.State, a *board.Assessment, events []board.Event) ([]FeedLine, error) {
	lines, err := recordLines(records)
	if err != nil {
		return nil, err
	}
	if b != nil {
		lines = append(lines, entryLines(records, s, b, a, events)...)
	}
	slices.SortStableFunc(lines, func(x, y FeedLine) int {
		return cmp.Or(x.At.Compare(y.At), cmp.Compare(x.store(), y.store()), cmp.Compare(x.seq(), y.seq()))
	})
	if err := v.titled(ctx, lines); err != nil {
		return nil, err
	}
	return lines, nil
}

// store orders a record's line before an entry's at equal time.
func (l *FeedLine) store() int {
	if l.Record != nil {
		return 0
	}
	return 1
}

// seq is the seq of the line's record or entry in its store.
func (l *FeedLine) seq() int64 {
	if l.Record != nil {
		return l.Record.Seq
	}
	return l.Event.Seq
}

// recordLines are the feed's lines of the item's records (R149): every
// record but round_opened, round_ended, run_stopped and item_awaiting_close,
// whose effect shows in a round's divider, the board's stop entry and the
// header, and a firing resume's run_extended, whose line is its
// item_triggered's. The line of an applied item_triggered counts the
// instances firing once it applied, as the records folded up to it give
// them.
func recordLines(records []item.Record) ([]FeedLine, error) {
	var lines []FeedLine
	upTo := &item.State{}
	for i := range records {
		r := &records[i]
		if err := upTo.Apply(r); err != nil {
			return nil, fmt.Errorf("fold item %s up to record %d: %w", r.Item, r.Seq, err)
		}
		if !shown(&r.Body) {
			continue
		}
		l := FeedLine{At: r.At, Record: r, Repeats: 1}
		if t := r.Body.Triggered; t != nil && t.Applied {
			for _, in := range upTo.Instances() {
				if in.Status == item.Firing {
					l.Firing++
				}
			}
		}
		lines = append(lines, l)
	}
	return lines, nil
}

// shown reports whether a record has a line of its own (see recordLines).
func shown(b *item.Body) bool {
	switch b.Kind() {
	case item.KindRoundOpened, item.KindRoundEnded, item.KindRunStopped, item.KindAwaitingClose:
		return false
	case item.KindExtended:
		return b.Extended.Cause != item.ExtendFiring
	}
	return true
}

// noticeKey is what makes notices repeats in the feed (design D9): their
// round, code, agent, tool, rule and skill.
type noticeKey struct {
	round                    int
	code                     board.NoticeCode
	agent, tool, rule, skill string
}

// entryLines are the feed's lines of the run's entries (design D8, D9;
// R149): every entry but a turn end, whose effect shows in its round's
// divider, and the resume entry of a firing resume, whose line is its
// item_triggered's. Notices repeated in a round, of one code, agent, tool,
// rule and skill, are one line, at the first, with their count. The keys
// the lines name are those of a, b's assessment.
func entryLines(records []item.Record, s *item.State, b *board.State, a *board.Assessment, events []board.Event) []FeedLine {
	f := &feedFacts{records: records, s: s, b: b, events: events, at: map[board.EntryID]int{}, keys: map[board.EntryID]key.Key{}}
	for i := range events {
		f.at[events[i].ID] = i
	}
	for i := range a.Hypotheses {
		h := &a.Hypotheses[i]
		f.keys[h.ID] = h.Key
		for _, alias := range h.Aliases {
			f.keys[alias] = h.Key
		}
	}
	var lines []FeedLine
	first := map[noticeKey]int{}
	for i := range events {
		e := &events[i]
		switch {
		case e.Body.TurnEnd != nil, e.Body.Resume != nil && e.Body.Resume.Cause == string(item.ExtendFiring):
			continue
		case e.Body.Notice != nil:
			n := e.Body.Notice
			k := noticeKey{round: e.Round, code: n.Code, agent: n.Agent, tool: n.Tool, rule: n.Rule, skill: n.Skill}
			if j, repeat := first[k]; repeat {
				lines[j].Repeats++
				continue
			}
			first[k] = len(lines)
		}
		lines = append(lines, f.line(e))
	}
	return lines
}

// feedFacts are what the entries' lines are read against: the item's
// records and their fold, the run's board and its events, each entry's place
// among them, and each hypothesis's key, an alias's its hypothesis's.
type feedFacts struct {
	records []item.Record
	s       *item.State
	b       *board.State
	events  []board.Event
	at      map[board.EntryID]int
	keys    map[board.EntryID]key.Key
}

// line is e's line, with what its words need that e does not hold (see
// FeedLine).
func (f *feedFacts) line(e *board.Event) FeedLine {
	l := FeedLine{At: e.At, Event: e, Repeats: 1}
	switch body := &e.Body; {
	case body.Evidence != nil:
		f.evidence(&l)
	case body.Hypothesis != nil:
		l.Key = f.keys[e.ID]
	case body.Retraction != nil:
		if ev := f.entry(body.Retraction.Evidence); ev != nil {
			l.Of = ev.Author
		}
	case body.Ruling != nil:
		f.ruling(&l)
	case body.Claim != nil:
		l.Task = f.entry(body.Claim.Task)
	case body.Completion != nil:
		l.Task = f.entry(body.Completion.Task)
	case body.Proposal != nil:
		l.Key = f.keys[body.Proposal.Hypothesis]
	case body.Round != nil:
		l.Key = f.keys[body.Round.WrapUp]
		if r := f.s.Run; body.Round.Round >= 1 && body.Round.Round <= len(r.Rounds) {
			l.Round = &r.Rounds[body.Round.Round-1]
		}
	case body.Stop != nil:
		l.Key = f.keys[body.Stop.Conclusion]
	}
	return l
}

// evidence gives an evidence line the key of its stance, the typed record
// its ref names, when it is platform evidence (see typed), and when its
// entry was retracted.
func (f *feedFacts) evidence(l *FeedLine) {
	ev := l.Event.Body.Evidence
	l.Key = f.keys[ev.Hypothesis]
	l.Typed = f.typed(ev)
	if id, ok := f.b.RetractionOf(l.Event.ID); ok {
		if r := f.entry(id); r != nil {
			l.Retracted = r.At
		}
	}
}

// ruling gives a ruling's line its key: a rejection's or an un-rejection's,
// its hypothesis's; a dismissal's, the dismissed refutation's hypothesis's,
// with the refutation's author.
func (f *feedFacts) ruling(l *FeedLine) {
	r := l.Event.Body.Ruling
	if r.Kind != board.RulingDismiss {
		l.Key = f.keys[r.Hypothesis]
		return
	}
	if ref := f.entry(r.Refutation); ref != nil && ref.Body.Evidence != nil {
		l.Key, l.Of = f.keys[ref.Body.Evidence.Hypothesis], ref.Author
	}
}

// entry is the run's entry of id among the events, or nil.
func (f *feedFacts) entry(id board.EntryID) *board.Event {
	if i, ok := f.at[id]; ok {
		return &f.events[i]
	}
	return nil
}

// typed is the record a platform evidence's ref names (R150): of item:<id>,
// the item's own, its item_opened; of trigger:<event id>, the item_triggered
// of the evidence's source and that event id, since an event id is unique
// per source only. It is nil for any other evidence, and for a ref that names
// no record the item holds.
func (f *feedFacts) typed(ev *board.Evidence) *item.Record {
	p := &ev.Provenance
	if p.Kind != board.FromPlatform {
		return nil
	}
	if id, ok := strings.CutPrefix(p.Ref, refItem); ok {
		if item.ID(id) == f.s.ID {
			return &f.records[0]
		}
		return nil
	}
	eventID, ok := strings.CutPrefix(p.Ref, refTrigger)
	if !ok {
		return nil
	}
	for i := range f.records {
		if t := f.records[i].Body.Triggered; t != nil && t.Source == string(p.Source) && t.EventID == eventID {
			return &f.records[i]
		}
	}
	return nil
}

// titled gives the lines of the trigger events the feed shows their titles
// from the inbound event store (design D8; R151), one call per source over
// every item_triggered among the lines: each event's own line, and the line
// of the platform evidence that names it (Typed). An event whose title the
// store no longer keeps reads not kept. With no store, none is read.
func (v *Views) titled(ctx context.Context, lines []FeedLine) error {
	if v.p.Titles == nil {
		return nil
	}
	ids := map[string][]string{}
	for i := range lines {
		if r := lines[i].Record; r != nil && r.Body.Triggered != nil {
			t := r.Body.Triggered
			ids[t.Source] = append(ids[t.Source], t.EventID)
		}
	}
	titles := map[string]map[string]string{}
	for _, source := range slices.Sorted(maps.Keys(ids)) {
		got, err := v.p.Titles.Titles(ctx, source, ids[source])
		if err != nil {
			return fmt.Errorf("read the titles of source %s: %w", source, err)
		}
		titles[source] = got
	}
	for i := range lines {
		r := lines[i].Record
		if r == nil {
			r = lines[i].Typed
		}
		if r != nil && r.Body.Triggered != nil {
			t := r.Body.Triggered
			lines[i].Title, lines[i].Kept = titles[t.Source][t.EventID]
		}
	}
	return nil
}
