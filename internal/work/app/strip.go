package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// The strip's values (R159), fixed in code until a deployment asks for a key.
const (
	// stripItems is how many of the incident's other items a strip shows.
	stripItems = 10
	// stripWait bounds a strip's build, the web pool's wait.
	stripWait = 2 * time.Second
	// stripKept is how long a good strip is kept from its build, and how
	// long one unread is kept from its latest read; failedStripKept how long
	// a strip whose listing failed is kept.
	stripKept       = 30 * time.Second
	failedStripKept = 5 * time.Second
)

// Strip is the incident's other items (design D7), as their records give
// them: those a board's strip shows, the items of its item's incident but
// itself.
type Strip struct {
	Items  []StripItem // newest opened first; at most 10
	More   int         // the other items past the ten
	Unread bool        // the incident's items could not be listed (7c-1a's IncidentItemsUnread)
}

// StripItem is one of the incident's other items as the strip shows it,
// built from its records alone, and one read of its conclusion's entry: no
// board of it is replayed (design D2).
type StripItem struct {
	ID         item.ID
	Type       string
	State      item.Status
	Hidden     bool    // Visible says no: its state alone, nothing read of its run
	Conclusion key.Key // a stopped run's conclusion's key; nil with none
	Converged  bool    // its run stopped converged
	Disagrees  bool    // set at each read (R148)
	Unreadable bool    // its records, or its conclusion's entry, could not be read
}

// stripKey is what a strip is kept by: the incident, and the item whose
// board shows it, which its listing leaves out.
type stripKey struct {
	incident item.IncidentID
	item     item.ID
}

// keptStrip is a strip kept: the strip, when it was built, and when it was
// last read.
type keptStrip struct {
	s           Strip
	built, read time.Time
}

// life is how long the strip is kept from its build: a good one stripKept,
// one whose listing failed failedStripKept (R172).
func (k *keptStrip) life() time.Duration {
	if k.s.Unread {
		return failedStripKept
	}
	return stripKept
}

// stripFlight is an incident's strip's build that runs, of key.
type stripFlight struct {
	key  stripKey
	done chan struct{}
}

// strip is the strip of bv's board (design D7; R172), the board of an item a
// later build does not hold: the one kept while it is kept (see life); else
// one built, one build of an incident's strips at a time, apart from ctx,
// waited for under ctx. Each read drops the strips unread for stripKept, and
// judges disagreement anew (see judged).
func (w *BoardWatch) strip(ctx context.Context, bv *BoardView) (Strip, error) {
	k := stripKey{incident: bv.View.Incident, item: bv.View.ID}
	for {
		w.mu.Lock()
		now := w.p.Now()
		w.dropUnread(now)
		if kept := w.strips[k]; kept != nil && now.Sub(kept.built) < kept.life() {
			kept.read = now
			s := kept.s
			w.mu.Unlock()
			return w.v.judged(s, bv), nil
		}
		f := w.stripping[k.incident]
		if f == nil {
			f = &stripFlight{key: k, done: make(chan struct{})}
			w.stripping[k.incident] = f
			go w.buildStrip(context.WithoutCancel(ctx), f, now)
		}
		w.mu.Unlock()
		select {
		case <-f.done:
		case <-ctx.Done():
			return Strip{}, fmt.Errorf("wait for the strip of item %s's board: %w", bv.View.ID, ctx.Err())
		}
	}
}

// dropUnread drops the strips unread for stripKept, under w.mu (R172).
func (w *BoardWatch) dropUnread(now time.Time) {
	for k, kept := range w.strips {
		if now.Sub(kept.read) >= stripKept {
			delete(w.strips, k)
		}
	}
}

// buildStrip builds f's strip, started at now (see Views.stripOf), and keeps
// it, built and read at now, under w.mu, its incident's build done.
func (w *BoardWatch) buildStrip(ctx context.Context, f *stripFlight, now time.Time) {
	s := w.v.stripOf(ctx, f.key.incident, f.key.item)
	w.mu.Lock()
	defer w.mu.Unlock()
	defer close(f.done)
	delete(w.stripping, f.key.incident)
	w.strips[f.key] = &keptStrip{s: s, built: now, read: now}
}

// stripOf builds the strip of incident's items but except (R172), within
// stripWait: one listing, newest opened first, of at most stripItems, with
// how many there are past them; and each listed item's entry (see
// stripItem). A listing that fails, or a build past its bound, is unread.
func (v *Views) stripOf(ctx context.Context, incident item.IncidentID, except item.ID) Strip {
	ctx, cancel := context.WithTimeout(ctx, stripWait)
	defer cancel()
	if v.p.Siblings == nil {
		return Strip{Unread: true}
	}
	ids, total, err := v.p.Siblings.Siblings(ctx, incident, except, stripItems)
	if err != nil {
		return Strip{Unread: true}
	}
	ids = ids[:min(len(ids), stripItems)]
	s := Strip{Items: make([]StripItem, 0, len(ids)), More: max(total-len(ids), 0)}
	for _, id := range ids {
		s.Items = append(s.Items, v.stripItem(ctx, id))
	}
	if ctx.Err() != nil {
		return Strip{Unread: true}
	}
	return s
}

// stripItem is a listed item's entry in the strip, from its records folded:
// its type and state; a hidden one, as Visible says of its view built from
// its fold, its state alone, with nothing read of its run; a visible one
// whose run stopped, whether it stopped converged, and the key of its
// conclusion, read in one read of its entry. One whose records cannot be read
// or folded, a later build's among them, or whose conclusion's entry cannot be
// read, is its id alone, unreadable.
func (v *Views) stripItem(ctx context.Context, id item.ID) StripItem {
	unreadable := StripItem{ID: id, Unreadable: true}
	records, err := v.records.Load(ctx, id)
	if err != nil || len(records) == 0 {
		return unreadable
	}
	s, err := item.Fold(records)
	if err != nil {
		return unreadable
	}
	view := View{ID: s.ID, State: string(s.Status()), Incident: s.Incident, Type: s.Opened.Type, OpenedAt: s.OpenedAt}
	if !v.visible(&view) {
		return StripItem{ID: id, State: s.Status(), Hidden: true}
	}
	it := StripItem{ID: id, Type: s.Opened.Type, State: s.Status()}
	if s.Run == nil || s.Run.Stopped == nil {
		return it
	}
	st := s.Run.Stopped
	it.Converged = st.Reason == item.StopConverged
	if st.Conclusion == "" {
		return it
	}
	if v.p.Entries == nil {
		return unreadable
	}
	ev, ok, err := v.p.Entries.Entry(ctx, st.Conclusion)
	if err != nil || !ok || ev.Body.Hypothesis == nil {
		return unreadable
	}
	it.Conclusion = ev.Body.Hypothesis.Key
	return it
}

// judged is s as bv's board shows it (R148): its items copied, since the
// strip kept is shared, each visible one whose run stopped converged
// disagreeing when its conclusion's key differs from another's of the same
// type, among the items shown and bv's own item, while it is visible. A
// hidden item, an unreadable one, and a conclusion not converged count for
// nothing, and an item past the ten shown is not among them.
func (v *Views) judged(s Strip, bv *BoardView) Strip {
	if len(s.Items) == 0 {
		return s
	}
	items := slices.Clone(s.Items)
	own, judging := v.ownConclusion(bv)
	for i := range items {
		it := &items[i]
		if !judges(it) {
			continue
		}
		it.Disagrees = judging && bv.View.Type == it.Type && !own.Equal(it.Conclusion)
		for j := range items {
			if j != i && judges(&items[j]) && items[j].Type == it.Type && !items[j].Conclusion.Equal(it.Conclusion) {
				it.Disagrees = true
			}
		}
	}
	s.Items = items
	return s
}

// judges reports whether a strip's item counts toward disagreement: a visible
// item whose run stopped converged, its conclusion read.
func judges(it *StripItem) bool {
	return !it.Hidden && !it.Unreadable && it.Converged && it.Conclusion != nil
}

// ownConclusion is the key of the conclusion bv's item's run stopped converged
// on, as bv's cards hold it, while the item is visible (R148); false for any
// other item.
func (v *Views) ownConclusion(bv *BoardView) (key.Key, bool) {
	s := bv.state
	if s == nil || s.Run == nil || s.Run.Stopped == nil || s.Run.Stopped.Reason != item.StopConverged || !v.visible(&bv.View) {
		return nil, false
	}
	conclusion := s.Run.Stopped.Conclusion
	for _, cards := range [][]HypothesisCard{bv.Hypotheses, bv.RuledOut} {
		for i := range cards {
			if cards[i].ID == conclusion || slices.Contains(cards[i].Aliases, conclusion) {
				return cards[i].Key, true
			}
		}
	}
	return nil, false
}
