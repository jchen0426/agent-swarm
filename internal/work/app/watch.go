package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The board watch's values (R159), fixed in code until a deployment asks for
// a key.
const (
	// tickEvery is how long a probe's result serves the reads it covers, and
	// probeWait a probe's bound (R137).
	tickEvery = time.Second
	probeWait = time.Second
	// readWithin is how recently an item was read for a tick to probe it.
	readWithin = 10 * time.Second
	// lastGoodKept is how long, from its own time, the last good tick stays
	// in use once a probe failed.
	lastGoodKept = 5 * time.Second
	// maxBuilt is how many projections an instance keeps.
	maxBuilt = 64
)

// BoardFacts are the facts of now (design D2; R169), computed at each read:
// those that depend on the instance, the kill switch or a revision alone.
type BoardFacts struct {
	KillSwitch                       KillSwitch
	KillSwitchRead                   bool // the tick read the table
	Held                             bool // this instance holds the lease
	QueuePosition                    int  // a queued item's place, while Held
	AutoClosePaused, AutoCloseUnread bool
	ClosesAt                         time.Time
	ClosesAtUnread                   bool
	Now                              time.Time
}

// BoardRead is a read of an item's board (design D2, D3): its projection, the
// facts of now, and its incident's strip.
type BoardRead struct {
	Board *BoardView
	Facts BoardFacts
	Strip Strip
}

// WatchPorts are what the board watch reads besides the views: the probe of
// the boards' versions and the kill switch's table, each read once a tick
// (R137, R139); the coordinator's snapshot and whether this instance holds
// the lease, for a queued item's place; the clock; and a shared build's
// bound.
type WatchPorts struct {
	Probe    Probe
	Controls interface {
		KillSwitch(ctx context.Context) (KillSwitch, error)
	}
	Lifecycle func() Lifecycle
	Held      func() bool
	Now       func() time.Time // the database's time as the instance estimates it
	BuildWait time.Duration    // a shared build's bound: the database's transaction timeout
}

// BoardWatch serves the board's reads (design D2), off the coordinator's
// lock, and writes nothing: each item's version from one probe a second for
// every item read in the last 10 s (R137); the projection of each version,
// kept and built once (R140); each board's incident strip, one build of an
// incident's at a time (R172); and, at each read, the facts of now (R169). No
// goroutine of its own ticks: a probe, a build and a strip's build run apart
// from the read that started them, each within its bound, and end.
type BoardWatch struct {
	v *Views
	p WatchPorts

	mu sync.Mutex
	// tick is the latest good probe's result, nil before the first; failed
	// is the probe that failed since, nil when none did; and probing is the
	// probe that runs, nil when none does: one runs at a time.
	tick    *tick
	failed  *failure
	probing *probe
	// read holds when each item was last read, for the next tick.
	read map[item.ID]time.Time
	// built holds the projections kept, order their keys, oldest inserted
	// first, and building the build that runs of each key.
	built    map[builtKey]built
	order    []builtKey
	building map[builtKey]*flight
	// strips holds the strips kept, and stripping the strip's build that
	// runs of each incident.
	strips    map[stripKey]*keptStrip
	stripping map[item.IncidentID]*stripFlight
}

// NewBoardWatch returns the board watch over v, whose reads it shares, with
// the ports p gives.
func NewBoardWatch(v *Views, p *WatchPorts) *BoardWatch {
	return &BoardWatch{
		v: v, p: *p, read: map[item.ID]time.Time{}, built: map[builtKey]built{}, building: map[builtKey]*flight{},
		strips: map[stripKey]*keptStrip{}, stripping: map[item.IncidentID]*stripFlight{},
	}
}

// Board reads item id's board: its version from the latest tick, or one
// probe (R137); the projection of that version, cached or built once
// (R140); the incident's strip (R172), but for an item a later build holds,
// whose board says that alone; and the facts of now. An id with no record
// fails with ErrNoItem, and a projection's build as Views.Board fails; a read
// whose context ends first fails with its error, while what it waited for
// goes on.
func (w *BoardWatch) Board(ctx context.Context, id item.ID) (BoardRead, error) {
	t, v, ok, err := w.version(ctx, id)
	switch {
	case err != nil:
		return BoardRead{}, err
	case !ok:
		return BoardRead{}, ErrNoItem
	}
	bv, err := w.projection(ctx, id, v)
	if err != nil {
		return BoardRead{}, err
	}
	read := BoardRead{Board: bv, Facts: w.facts(ctx, t, bv)}
	if bv.View.HeldByLaterBuild {
		return read, nil
	}
	if read.Strip, err = w.strip(ctx, bv); err != nil {
		return BoardRead{}, err
	}
	return read, nil
}

// tick is a probe's result: when it was taken; the items it read, an item
// with no record among them, and the version of each item that has one; and
// the kill switch's table as the tick read it, and whether it could.
type tick struct {
	at       time.Time
	asked    map[item.ID]bool
	versions map[item.ID]BoardVersion
	ks       KillSwitch
	ksRead   bool
}

// merged is t with the result of a probe of items alone, which keeps t's time
// and its read of the kill switch.
func (t *tick) merged(items []item.ID, versions map[item.ID]BoardVersion) *tick {
	m := &tick{at: t.at, asked: maps.Clone(t.asked), versions: maps.Clone(t.versions), ks: t.ks, ksRead: t.ksRead}
	for _, id := range items {
		m.asked[id] = true
		if v, ok := versions[id]; ok {
			m.versions[id] = v
		} else {
			delete(m.versions, id)
		}
	}
	return m
}

// probe is a probe that runs: the items it reads, and whether it is a tick's,
// which also reads the kill switch's table and replaces the tick, or an
// item's alone, merged into it. Once done is closed, result is the tick it
// left, or err why it failed.
type probe struct {
	items  []item.ID
	tick   bool
	done   chan struct{}
	result *tick
	err    error
}

// failure is a probe that failed: when it was taken, and why.
type failure struct {
	at  time.Time
	err error
}

// versioned is what a read of an item's version gives: the tick it is read
// from, the version, whether the item has a record, or why it failed.
type versioned struct {
	t   *tick
	v   BoardVersion
	ok  bool
	err error
}

// version returns item id's version, ok false for an item with no record,
// and the tick it is read from (R137): the latest tick's, or that of the
// probe it waits for, under ctx, once that probe has read the item (see
// next). A probe it waited for that failed leaves the last good tick in use
// (see lastGood).
func (w *BoardWatch) version(ctx context.Context, id item.ID) (*tick, BoardVersion, bool, error) {
	for {
		w.mu.Lock()
		got, pr := w.next(ctx, id)
		w.mu.Unlock()
		if pr == nil {
			return got.t, got.v, got.ok, got.err
		}
		select {
		case <-pr.done:
		case <-ctx.Done():
			return nil, BoardVersion{}, false, fmt.Errorf("wait for the probe of item %s's board: %w", id, ctx.Err())
		}
		switch {
		case !slices.Contains(pr.items, id):
			// Another item's probe: the next tick may cover this one.
		case pr.err == nil:
			v, ok := pr.result.versions[id]
			return pr.result, v, ok, nil
		default:
			w.mu.Lock()
			got = w.lastGood(w.p.Now(), id, pr.err)
			w.mu.Unlock()
			return got.t, got.v, got.ok, got.err
		}
	}
}

// next is what a read of item id's version does now, under w.mu, which it
// notes as the item's latest read: wait for the probe that runs, which it
// returns; or answer from the latest tick while it is fresh, less than
// tickEvery old, and covers the item; or, while a probe failed less than
// tickEvery ago, answer from the last good tick, as lastGood does, and probe
// nothing; or start a probe, and return it: of the item alone, to merge into
// a fresh tick, or a tick's, of every item read in the last readWithin.
func (w *BoardWatch) next(ctx context.Context, id item.ID) (versioned, *probe) {
	now := w.p.Now()
	w.read[id] = now
	if w.probing != nil {
		return versioned{}, w.probing
	}
	t := w.tick
	fresh := t != nil && now.Sub(t.at) < tickEvery
	switch {
	case fresh && t.asked[id]:
		v, ok := t.versions[id]
		return versioned{t: t, v: v, ok: ok}, nil
	case w.failed != nil && now.Sub(w.failed.at) < tickEvery:
		return w.lastGood(now, id, w.failed.err), nil
	case fresh:
		return versioned{}, w.start(ctx, []item.ID{id}, false, now)
	}
	return versioned{}, w.start(ctx, w.tickItems(now), true, now)
}

// lastGood is item id's version from the last good tick, which a probe that
// failed with err leaves in use while it is less than lastGoodKept old
// (R137); past that, or for an item it does not cover, the read fails with
// err, which the pages answer 503.
func (w *BoardWatch) lastGood(now time.Time, id item.ID, err error) versioned {
	if t := w.tick; t != nil && now.Sub(t.at) < lastGoodKept && t.asked[id] {
		v, ok := t.versions[id]
		return versioned{t: t, v: v, ok: ok}
	}
	return versioned{err: fmt.Errorf("read the version of item %s's board: %w", id, err)}
}

// tickItems are the items a tick probes, under w.mu: each read in the last
// readWithin, in their order; the others are forgotten.
func (w *BoardWatch) tickItems(now time.Time) []item.ID {
	items := make([]item.ID, 0, len(w.read))
	for id, at := range w.read {
		if now.Sub(at) < readWithin {
			items = append(items, id)
		} else {
			delete(w.read, id)
		}
	}
	slices.Sort(items)
	return items
}

// start starts a probe of items taken at now, a tick's when tick is set,
// under w.mu, as the one probe that runs, apart from ctx (see run).
func (w *BoardWatch) start(ctx context.Context, items []item.ID, tick bool, now time.Time) *probe {
	pr := &probe{items: items, tick: tick, done: make(chan struct{})}
	w.probing = pr
	go w.run(context.WithoutCancel(ctx), pr, now)
	return pr
}

// errNotRead stands for a read of the kill switch's table a probe did not
// make.
var errNotRead = errors.New("app: the kill switch's table was not read")

// run runs pr, taken at at, within probeWait: the probe, and for a tick's
// the kill switch's table, once (R139), a read of which that fails leaves the
// tick without it. Then, under w.mu, a tick's result replaces the tick, an
// item's merges into it, or the failure is noted, and pr is done.
func (w *BoardWatch) run(ctx context.Context, pr *probe, at time.Time) {
	ctx, cancel := context.WithTimeout(ctx, probeWait)
	defer cancel()
	versions, err := w.p.Probe.Versions(ctx, pr.items)
	var ks KillSwitch
	ksErr := errNotRead
	if err == nil && pr.tick {
		ks, ksErr = w.p.Controls.KillSwitch(ctx)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	defer close(pr.done)
	w.probing = nil
	if err != nil {
		pr.err = fmt.Errorf("probe the versions of %d boards: %w", len(pr.items), err)
		w.failed = &failure{at: at, err: pr.err}
		return
	}
	w.failed = nil
	if pr.tick || w.tick == nil {
		asked := make(map[item.ID]bool, len(pr.items))
		for _, id := range pr.items {
			asked[id] = true
		}
		w.tick = &tick{at: at, asked: asked, versions: versions, ks: ks, ksRead: ksErr == nil}
	} else {
		w.tick = w.tick.merged(pr.items, versions)
	}
	pr.result = w.tick
}

// builtKey is what a projection is kept by: its item, and the version the
// probe gave, never the projection's own, which a held view leaves zero.
type builtKey struct {
	id item.ID
	v  BoardVersion
}

// built is a projection kept: its view, or the error its build failed with
// for good.
type built struct {
	bv  *BoardView
	err error
}

// flight is a projection's build that runs: once done is closed, its view or
// its error.
type flight struct {
	done chan struct{}
	bv   *BoardView
	err  error
}

// projection is the projection of item id at version v (R140): the one kept,
// unless it missed the revision its marks needed, which answers now (R175;
// see recovers); else the build of it that runs, or one it starts, waited for
// under ctx (see flight). A rebuild that fails leaves the projection kept in
// use.
func (w *BoardWatch) projection(ctx context.Context, id item.ID, v BoardVersion) (*BoardView, error) {
	k := builtKey{id: id, v: v}
	w.mu.Lock()
	b, kept := w.built[k]
	w.mu.Unlock()
	var stale *BoardView
	if kept {
		if !w.recovers(ctx, b.bv) {
			return b.bv, b.err
		}
		stale = b.bv
	}
	f := w.flight(ctx, &k, stale)
	select {
	case <-f.done:
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for the projection of item %s's board: %w", id, ctx.Err())
	}
	if kept && f.err != nil {
		return b.bv, b.err
	}
	return f.bv, f.err
}

// recovers reports whether bv, a projection kept, named the revision a mark
// of it needed and could not read, and the views' revisions answer it now:
// the web's read the registry's cache alone, so this is no read of a source
// (R175). A projection whose marks compare nothing for a reason of their own
// names none, and is never built again.
func (w *BoardWatch) recovers(ctx context.Context, bv *BoardView) bool {
	if bv == nil || bv.revisionMissed == "" || w.v.p.Revisions == nil {
		return false
	}
	rev, err := w.v.p.Revisions.At(ctx, bv.revisionMissed)
	return err == nil && rev != nil
}

// flight is the build of k that runs; or, as a build done, the projection a
// build kept since the read looked, unless it is stale, the one the read
// found kept and builds again; or else a build it starts, apart from ctx: so
// that a version is built once at a time (see build).
func (w *BoardWatch) flight(ctx context.Context, k *builtKey, stale *BoardView) *flight {
	w.mu.Lock()
	defer w.mu.Unlock()
	if f := w.building[*k]; f != nil {
		return f
	}
	if b, kept := w.built[*k]; kept && (stale == nil || b.bv != stale) {
		f := &flight{done: make(chan struct{}), bv: b.bv, err: b.err}
		close(f.done)
		return f
	}
	f := &flight{done: make(chan struct{})}
	w.building[*k] = f
	key := *k
	go w.build(context.WithoutCancel(ctx), &key, f)
	return f
}

// build builds k's projection within BuildWait (R141), and keeps it, or the
// error it failed with for good: records or a board that are invalid
// (R140). Any other failure, a store's, is transient and not kept.
func (w *BoardWatch) build(ctx context.Context, k *builtKey, f *flight) {
	ctx, cancel := context.WithTimeout(ctx, w.p.BuildWait)
	defer cancel()
	bv, err := w.v.Board(ctx, k.id, k.v)
	w.mu.Lock()
	defer w.mu.Unlock()
	defer close(f.done)
	delete(w.building, *k)
	f.bv, f.err = bv, err
	if err == nil || corrupt(err) {
		w.keep(k, built{bv: bv, err: err})
	}
}

// keep keeps b as k's projection, under w.mu, in place of the one kept
// before; past maxBuilt, the oldest inserted is evicted.
func (w *BoardWatch) keep(k *builtKey, b built) {
	if _, ok := w.built[*k]; !ok {
		w.order = append(w.order, *k)
	}
	w.built[*k] = b
	for len(w.order) > maxBuilt {
		delete(w.built, w.order[0])
		w.order = slices.Delete(w.order, 0, 1)
	}
}

// facts are bv's facts of now (R169), with t, the tick its version is read
// from: the tick's kill switch; whether this instance holds the lease, and,
// while it does, a queued item's place in the coordinator's queue (R17), a
// lease lost before the snapshot leaving none (m6); an item's that awaits
// its close, its auto-close paused while the tick's table holds the switch
// on, unread when the tick could not read it, or when it closes by itself
// (see Views.closesAt); and the time now. A view a later build holds has the
// instance's facts alone.
func (w *BoardWatch) facts(ctx context.Context, t *tick, bv *BoardView) BoardFacts {
	f := BoardFacts{KillSwitch: t.ks, KillSwitchRead: t.ksRead, Now: w.p.Now()}
	f.Held = w.p.Held != nil && w.p.Held()
	s := bv.state
	if s == nil {
		return f
	}
	switch s.Status() {
	case item.StatusQueued:
		if f.Held && w.p.Lifecycle != nil {
			waiting := w.p.Lifecycle().Waiting
			if f.Held = w.p.Held(); f.Held {
				f.QueuePosition = slices.Index(waiting, s.ID) + 1
			}
		}
	case item.StatusAwaitingClose:
		switch {
		case !t.ksRead:
			f.AutoCloseUnread = true
		case t.ks.On:
			f.AutoClosePaused = true
		default:
			f.ClosesAt, f.ClosesAtUnread = w.v.closesAt(ctx, s, &t.ks)
		}
	}
	return f
}
