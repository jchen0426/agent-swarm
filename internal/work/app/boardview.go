package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// Mark is a row of a store, a record of an item or an entry of a run's
// board: its id and its seq.
type Mark struct {
	ID  string
	Seq int64
}

// BoardVersion is what a projection is of (design D2): the item's latest
// record, and its run's latest board entry; never sent to a page.
type BoardVersion struct {
	Record Mark
	Run    board.RunID // "" before the item's run_created
	Board  Mark        // zero with no run or no entry
}

// ErrMoved marks a version whose record or entry at its seq is not its own,
// or whose run is not the one the item's records name up to it, as after a
// database restore (R140): the projection is answered 503, and the next
// probe reads the version again.
var ErrMoved = errors.New("app: the board moved under its version")

// Visible is the predicate of whether an item's board is shown, true until
// Plan 8a (R144): ViewPorts.Visible defaults to it.
func Visible(*View) bool { return true }

// quotesPerSide is how many quotes a hypothesis's card shows on each side,
// the spec's deployment default of 2 (spec Section 15), a constant until a
// deployment key is asked for.
const quotesPerSide = 2

// BoardView is an item's board as its pages show it (design D3): what the
// records and the board determine, and nothing of the instance, the kill
// switch, a revision, the incident's other items or the viewer. Shared: no
// caller changes it.
type BoardView struct {
	Version BoardVersion
	// View is Item's view, with what the records determine (R169): the state,
	// the stop, NotStaffed with its cause, Closed with its windows, the stop
	// entry, the budget. Its QueuePosition, AutoClosePaused, AutoCloseUnread,
	// ClosesAt, ClosesAtUnread and IncidentItems stay zero (BoardFacts, Strip).
	View       View
	Opened     item.Opened // principal, responders, title, routing, by, source, trigger
	Header     BoardHeader
	Hypotheses []HypothesisCard // ranked, those ruled out apart
	RuledOut   []HypothesisCard // rejected, or a refutation stands
	Feed       []FeedLine
	// Cited holds each read call that evidence on the board cites, with the
	// quotes citing it (design D10).
	Cited map[string][]string
	state *item.State // for the facts of a read (Task 3)
}

// BoardHeader is a board's header (design D3, D5): what the item's records
// and its run's board say of it.
type BoardHeader struct {
	Title           string
	Conclusion      *HypothesisCard // the assessment's conclusion; nil with none
	Suggestions     int
	Alert           []item.TriggerInstance
	Firing          bool
	StaysOpen       bool      // awaiting its close while its alert fires (row 8; R169)
	Updated         time.Time // the later of the latest record's time and the latest entry's
	StopBy          time.Time // zero but while investigating with a run
	ConfirmingUntil time.Time // zero but while a wrap-up round is open
	FlapOf          item.ID   // the closed item a flap's opening linked to
	Notices         int       // the latest round's notices, each counted
	Round, Ended    int       // the latest round, and the latest ended round (R177)
}

// HypothesisCard is one of the board's hypotheses as its pages show it
// (design D3, D4, D6).
type HypothesisCard struct {
	board.HypothesisView
	Status      board.StatusWord // confirming included
	Quotes      board.Quotes     // perSide 2, the spec's [2]
	Suggestions []SuggestionGroup
}

// Board is the projection of the item's records and its run's events up to
// at's seqs (R140); a zero at reads everything committed, and the view's
// Version says what it read. ErrNoItem as Item; a later version gives
// View.HeldByLaterBuild alone; ErrMoved when the record or entry at at's seq
// is not at's.
//
// The records kept are those of seq up to at.Record.Seq, the last of them
// at.Record's, and the run they name must be at.Run; the board's events kept
// are those of seq up to at.Board.Seq, the last of them at.Board's, none for
// a zero mark. Board builds a hidden item's projection as any other's
// (R144). It reads the inbound store's titles (see titled), and the locked
// revision once, for the service marks, when the board holds a proposal.
func (v *Views) Board(ctx context.Context, id item.ID, at BoardVersion) (*BoardView, error) {
	records, err := v.records.Load(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load item %s: %w", id, err)
	}
	if len(records) == 0 {
		return nil, ErrNoItem
	}
	whole := at == (BoardVersion{})
	if !whole {
		if records, err = recordsAt(records, at.Record); err != nil {
			return nil, err
		}
	}
	s, err := item.Fold(records)
	switch {
	case ofLaterVersion(err):
		return &BoardView{View: View{ID: id, HeldByLaterBuild: true}}, nil
	case err != nil:
		return nil, fmt.Errorf("fold item %s: %w", id, err)
	}
	var mark *Mark
	if !whole {
		if run := runOf(s); run != at.Run {
			return nil, fmt.Errorf("%w: item %s's run up to seq %d is %q, not %q", ErrMoved, id, at.Record.Seq, run, at.Run)
		}
		mark = &at.Board
	}
	b, err := v.boardOf(ctx, s, mark)
	switch {
	case ofLaterVersion(err):
		return &BoardView{View: View{ID: id, HeldByLaterBuild: true}}, nil
	case err != nil:
		return nil, fmt.Errorf("read the board of item %s: %w", id, err)
	}
	return v.project(ctx, records, s, b)
}

// recordsAt is records, in commit order, cut at the seq of at (R140): those
// committed after it are left out, and the last one kept must be at's, or
// the records moved under it (ErrMoved).
func recordsAt(records []item.Record, at Mark) ([]item.Record, error) {
	kept := records[:sort.Search(len(records), func(i int) bool { return records[i].Seq > at.Seq })]
	if n := len(kept); n == 0 || kept[n-1].ID != at.ID || kept[n-1].Seq != at.Seq {
		return nil, fmt.Errorf("%w: the item's record of seq %d is not %q", ErrMoved, at.Seq, at.ID)
	}
	return kept, nil
}

// runOf is the id of s's run, "" before its run_created.
func runOf(s *item.State) board.RunID {
	if s.Run == nil {
		return ""
	}
	return s.Run.ID
}

// project is the board view of s, the fold of records, and b, its run's
// board as cut, nil with no run: the version they are, the view they
// determine (see projected), the item's opening, the header, the cards, the
// feed and the read calls cited.
func (v *Views) project(ctx context.Context, records []item.Record, s *item.State, b *board.State) (*BoardView, error) {
	last := &records[len(records)-1]
	bv := &BoardView{
		Version: BoardVersion{Record: Mark{ID: last.ID, Seq: last.Seq}},
		View:    v.projected(s, b), Opened: *s.Opened, Header: v.header(records, s), state: s,
	}
	var events []board.Event
	var a *board.Assessment
	if b != nil {
		events, a = b.Events(), new(b.Assess())
		bv.Version.Run = s.Run.ID
		if n := len(events); n > 0 {
			e := &events[n-1]
			bv.Version.Board = Mark{ID: string(e.ID), Seq: e.Seq}
			if e.At.After(bv.Header.Updated) {
				bv.Header.Updated = e.At
			}
		}
		v.hypotheses(ctx, s, b, a, events, bv)
		bv.Header.Notices = noticesIn(events, bv.Header.Round)
		bv.Cited = cited(events)
	}
	feed, err := v.feed(ctx, records, s, b, a, events)
	if err != nil {
		return nil, err
	}
	bv.Feed = feed
	return bv, nil
}

// header is what the item's records give the board's header (design D3, D5;
// R169): its title; its alert, each instance with its last status, and
// whether it fires; whether it stays open, awaiting its close while its
// alert fires; its latest record's time, which the board's latest entry may
// make later (see project); the closed item a flap's opening linked it to;
// and, of its run, its latest round and its latest ended round, the latest
// time it stops while it investigates (Run.StopBy), and when the decision of
// its open wrap-up round lands (Run.ConfirmingUntil).
func (v *Views) header(records []item.Record, s *item.State) BoardHeader {
	latest := records[len(records)-1].At
	h := BoardHeader{Title: s.Opened.Title, Alert: s.Instances(), Firing: s.TriggerFiring(), Updated: latest, FlapOf: flapOf(records)}
	h.StaysOpen = s.Status() == item.StatusAwaitingClose && h.Firing
	r := s.Run
	if r == nil {
		return h
	}
	if s.Status() == item.StatusInvestigating && s.Running() {
		h.StopBy = r.StopBy(latest, v.p.Waits)
	}
	h.ConfirmingUntil, _ = r.ConfirmingUntil(v.p.Waits)
	if l := r.Latest(); l != nil {
		h.Round = l.Opened.Round
	}
	for i := len(r.Rounds) - 1; i >= 0 && h.Ended == 0; i-- {
		if r.Rounds[i].Ended != nil {
			h.Ended = r.Rounds[i].Opened.Round
		}
	}
	return h
}

// flapOf is the closed item a flap's opening linked the item to (design D8):
// the With of the item_linked by a source that is the opening's third record
// (R126); "" for any other item.
func flapOf(records []item.Record) item.ID {
	if len(records) < 3 {
		return ""
	}
	if l := records[2].Body.Linked; l != nil && strings.HasPrefix(l.By, SourceSubjectPrefix) {
		return l.With
	}
	return ""
}

// hypotheses gives bv the board's hypotheses, as b's assessment a ranks
// them, as cards: each with its status now, confirming while its wrap-up
// round is open (Run.Confirming); its quotes; and its suggestions (see
// suggesting); those ruled out, rejected or against which a refutation
// stands, apart. The header's conclusion is the card of a's, and the header
// counts every suggestion, as spec 10.2 asks.
func (v *Views) hypotheses(ctx context.Context, s *item.State, b *board.State, a *board.Assessment, events []board.Event, bv *BoardView) {
	sg := v.suggesting(ctx, s, events)
	wrapUp := s.Run.Confirming()
	for i := range a.Hypotheses {
		h := &a.Hypotheses[i]
		card := HypothesisCard{HypothesisView: *h, Status: board.StatusOf(a, i, wrapUp), Quotes: b.Quotes(h.ID, quotesPerSide)}
		card.Suggestions = sg.groups(b, &card)
		for j := range card.Suggestions {
			bv.Header.Suggestions += len(card.Suggestions[j].Suggestions)
		}
		if h.Rejected || len(h.StandingRefutations) > 0 {
			bv.RuledOut = append(bv.RuledOut, card)
		} else {
			bv.Hypotheses = append(bv.Hypotheses, card)
		}
	}
	for _, cards := range [][]HypothesisCard{bv.Hypotheses, bv.RuledOut} {
		for i := range cards {
			if a.Conclusion != "" && cards[i].ID == a.Conclusion {
				bv.Header.Conclusion = &cards[i]
			}
		}
	}
}

// noticesIn counts the notices of round among events, each counted.
func noticesIn(events []board.Event, round int) int {
	n := 0
	for i := range events {
		if events[i].Body.Notice != nil && events[i].Round == round {
			n++
		}
	}
	return n
}

// cited is each read call that evidence among events cites, with the quotes
// citing it, each once, in commit order (design D10); nil when none does.
func cited(events []board.Event) map[string][]string {
	var out map[string][]string
	for i := range events {
		ev := events[i].Body.Evidence
		if ev == nil || ev.Provenance.CallID == "" {
			continue
		}
		if out == nil {
			out = map[string][]string{}
		}
		call, quote := ev.Provenance.CallID, ev.Provenance.Quote
		if !slices.Contains(out[call], quote) {
			out[call] = append(out[call], quote)
		}
	}
	return out
}
