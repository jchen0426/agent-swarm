package app

import (
	"context"
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
	// stripKept is how long a good strip is kept from its build, how long
	// one unread is kept from its latest read, and how long at most a failed
	// one is kept; failedStripKept how long a strip whose build failed, the
	// first in a row, or that holds an item unreadable for now, is kept (see
	// life).
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

// keptStrip is a strip kept: the strip, whether an item of it is unreadable
// for now (see stripItem), how many of its builds failed in a row, this one's
// among them, when it was built, and when it was last read.
type keptStrip struct {
	s           Strip
	forNow      bool
	failures    int
	built, read time.Time
}

// life is how long from its build the strip is answered before a read builds
// it again (R172): a good one stripKept; one that holds an item unreadable
// for now failedStripKept (review 2's M5); one whose build failed, its
// listing or its bound, failedStripKept, doubled for each failure in a row
// before it, up to stripKept: 5, 10, 20, then 30 s (re-review 2's N2), so
// that an incident whose strip cannot be read is not listed every 5 s.
func (k *keptStrip) life() time.Duration {
	switch {
	case k.s.Unread:
		life := failedStripKept
		for range k.failures - 1 {
			if life >= stripKept {
				break
			}
			life *= 2
		}
		return min(life, stripKept)
	case k.forNow:
		return failedStripKept
	}
	return stripKept
}

// stripFlight is an incident's strip's build that runs, of key. Once done is
// closed, readNothing says whether it read nothing, having got no strip slot
// within its bound (see buildStrip).
type stripFlight struct {
	key         stripKey
	done        chan struct{}
	readNothing bool
}

// strip is the strip of bv's board (design D7; R172), the board of an item a
// later build does not hold, which never fails the read. A strip kept is
// answered at once, judged anew (see judged), and past its life (see life) a
// read starts its rebuild, when no build of its incident runs (plan review
// I6: the last good strip is served meanwhile); when another board's runs, it
// notes it, for its rebuild as that build settles (see note). With no strip
// kept, the read waits under ctx for its build, which it starts when no build
// of the incident runs, one build of an incident's strips at a time, apart
// from ctx's end; once ctx ends first, or its own build read nothing, it is
// answered unread, the build that runs going on for the next read (review 1's
// I1, review 2's I1, re-review 2's N2). Each read drops the strips unread for
// stripKept.
func (w *BoardWatch) strip(ctx context.Context, bv *BoardView) Strip {
	k := stripKey{incident: bv.View.Incident, item: bv.View.ID}
	for {
		w.mu.Lock()
		now := w.mono()
		w.dropUnread(now)
		running := w.stripping[k.incident]
		if kept := w.strips[k]; kept != nil {
			kept.read = now
			if now.Sub(kept.built) >= kept.life() {
				switch {
				case running == nil:
					w.startStrip(ctx, k, now)
				case running.key != k:
					w.note(k)
				}
			}
			s := kept.s
			w.mu.Unlock()
			return w.v.judged(s, bv)
		}
		if running == nil {
			running = w.startStrip(ctx, k, now)
		}
		w.mu.Unlock()
		select {
		case <-running.done:
			if running.key == k && running.readNothing {
				return Strip{Unread: true}
			}
		case <-ctx.Done():
			return Strip{Unread: true}
		}
	}
}

// startStrip starts the build of k's strip, at now, under w.mu, as its
// incident's one build, apart from ctx's end (see detached and buildStrip).
func (w *BoardWatch) startStrip(ctx context.Context, k stripKey, now time.Time) *stripFlight {
	f := &stripFlight{key: k, done: make(chan struct{})}
	w.stripping[k.incident] = f
	flying, end := w.detached(ctx)
	go func() {
		defer end()
		w.buildStrip(flying, f, now)
	}()
	return f
}

// note notes k's strip, under w.mu, found past its life while another
// board's build of its incident runs, after those noted before it, once
// (re-review 2's N1): as each build of the incident settles, the first strip
// noted that is still kept and past its life is built again (see
// settleStrip), so that a read never puts a rebuild off, whatever the phases
// of the incident's boards' polls; else a strip whose reads each found another
// build running would be served past its life without limit.
func (w *BoardWatch) note(k stripKey) {
	if !slices.Contains(w.noted[k.incident], k) {
		w.noted[k.incident] = append(w.noted[k.incident], k)
	}
}

// startNoted starts, under w.mu, at now, the rebuild of the first strip noted
// of incident that is still kept, not dropped, and past its life, and forgets
// it and those noted before it. No read starts it: it runs under the watch's
// context (see background), so that a chain of such rebuilds never nests a
// context in the one before.
func (w *BoardWatch) startNoted(incident item.IncidentID, now time.Time) {
	w.dropUnread(now)
	noted := w.noted[incident]
	for len(noted) > 0 {
		k := noted[0]
		noted = noted[1:]
		if kept := w.strips[k]; kept != nil && now.Sub(kept.built) >= kept.life() {
			w.startStrip(w.background(), k, now)
			break
		}
	}
	if len(noted) == 0 {
		delete(w.noted, incident)
		return
	}
	w.noted[incident] = noted
}

// dropUnread drops the strips unread for stripKept, under w.mu (R172).
func (w *BoardWatch) dropUnread(now time.Time) {
	for k, kept := range w.strips {
		if now.Sub(kept.read) >= stripKept {
			delete(w.strips, k)
		}
	}
}

// buildStrip builds f's strip, started at now, within stripWait, the wait for
// the instance's one strip build at a time among it (see slot and
// Views.stripOf), and keeps it (see settleStrip): a build that gets no slot
// within its bound reads nothing, and keeps nothing (re-review 2's N2). Its
// cleanup is registered first, and a panic, logged (see panicked), is a strip
// that failed: unread, kept as a failed one is (review 2's I2).
func (w *BoardWatch) buildStrip(ctx context.Context, f *stripFlight, now time.Time) {
	kept := &keptStrip{s: Strip{Unread: true}, built: now, read: now}
	defer func() {
		if p := recover(); p != nil {
			w.panicked(ctx, "strip", f.key.item, p)
			kept.s, kept.forNow = Strip{Unread: true}, false
		}
		w.settleStrip(f, kept)
	}()
	building, cancel := context.WithTimeout(ctx, stripWait)
	defer cancel()
	release, err := w.slot(building, w.stripSlot)
	if err != nil {
		f.readNothing = true
		return
	}
	defer release()
	kept.s, kept.forNow = w.v.stripOf(building, f.key.incident, f.key.item)
}

// settleStrip ends f, under w.mu: unless it read nothing, which leaves the
// strip kept before as it was, it keeps kept, f's strip, in place of that
// one, as read at the later of its build and that one's latest read, and, an
// unread one, as that one's failures in a row, none for a good one, and this
// one; its incident's
// build is then done, and the rebuild of a strip noted of its incident
// starts (see startNoted).
func (w *BoardWatch) settleStrip(f *stripFlight, kept *keptStrip) {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer close(f.done)
	delete(w.stripping, f.key.incident)
	defer w.startNoted(f.key.incident, w.mono())
	if f.readNothing {
		return
	}
	old := w.strips[f.key]
	if old != nil && old.read.After(kept.read) {
		kept.read = old.read
	}
	if kept.s.Unread {
		kept.failures = 1
		if old != nil {
			kept.failures += old.failures
		}
	}
	w.strips[f.key] = kept
}

// stripOf builds the strip of incident's items but except (R172), within
// ctx's bound: one listing, newest opened first, of at most stripItems, with
// how many there are past them; and each listed item's entry (see
// stripItem), and whether one of them is unreadable for now. A listing that
// fails, or a build past its bound, is unread.
func (v *Views) stripOf(ctx context.Context, incident item.IncidentID, except item.ID) (s Strip, forNow bool) {
	if v.p.Siblings == nil {
		return Strip{Unread: true}, false
	}
	ids, total, err := v.p.Siblings.Siblings(ctx, incident, except, stripItems)
	if err != nil {
		return Strip{Unread: true}, false
	}
	ids = ids[:min(len(ids), stripItems)]
	s = Strip{Items: make([]StripItem, 0, len(ids)), More: max(total-len(ids), 0)}
	for _, id := range ids {
		it, unreadForNow := v.stripItem(ctx, id)
		s.Items = append(s.Items, it)
		forNow = forNow || unreadForNow
	}
	if ctx.Err() != nil {
		return Strip{Unread: true}, false
	}
	return s, forNow
}

// stripItem is a listed item's entry in the strip, from its records folded:
// its type and state; a hidden one, as Visible says of its view built from
// its fold, its state alone, with nothing read of its run; a visible one
// whose run stopped, whether it stopped converged, and the key of its
// conclusion, read in one read of its entry. One whose records cannot be read
// or folded, a later build's among them, or whose conclusion's entry cannot be
// read, is its id alone, unreadable: for now, when a store failed to load its
// records or read its entry (forNow, review 2's M5); for good, when they are
// invalid or of a later build, or the entry is absent, of a later build, or
// no hypothesis.
func (v *Views) stripItem(ctx context.Context, id item.ID) (it StripItem, forNow bool) {
	unreadable := StripItem{ID: id, Unreadable: true}
	records, err := v.records.Load(ctx, id)
	switch {
	case err != nil:
		return unreadable, !corrupt(err)
	case len(records) == 0:
		return unreadable, false
	}
	s, err := item.Fold(records)
	if err != nil {
		return unreadable, false
	}
	view := folded(s)
	if !v.visible(&view) {
		return StripItem{ID: id, State: s.Status(), Hidden: true}, false
	}
	it = StripItem{ID: id, Type: s.Opened.Type, State: s.Status()}
	if s.Run == nil || s.Run.Stopped == nil {
		return it, false
	}
	st := s.Run.Stopped
	it.Converged = st.Reason == item.StopConverged
	if st.Conclusion == "" {
		return it, false
	}
	if v.p.Entries == nil {
		return unreadable, false
	}
	ev, ok, err := v.p.Entries.Entry(ctx, st.Conclusion)
	switch {
	case err != nil:
		return unreadable, !corrupt(err) && !ofLaterVersion(err)
	case !ok || ev.Body.Hypothesis == nil:
		return unreadable, false
	}
	it.Conclusion = ev.Body.Hypothesis.Key
	return it, false
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
// other item. The header's conclusion is that card when the assessment
// concludes on it.
func (v *Views) ownConclusion(bv *BoardView) (key.Key, bool) {
	s := bv.state
	if s == nil || s.Run == nil || s.Run.Stopped == nil || s.Run.Stopped.Reason != item.StopConverged || !v.visible(&bv.View) {
		return nil, false
	}
	conclusion := s.Run.Stopped.Conclusion
	card := bv.Header.Conclusion
	if card == nil || !card.holds(conclusion) {
		card = bv.card(conclusion)
	}
	if card == nil {
		return nil, false
	}
	return card.Key, true
}
