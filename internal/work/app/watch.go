package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime/debug"
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
	// logEvery is how often at most a flight's failure that no read is told,
	// a panic or a rebuild's, is logged of one kind and item (re-review 2's
	// N5), as R176 limits a board read's failure.
	logEvery = time.Minute
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
	// Board is the projection the watch keeps for the version read, shared
	// by every read of that version: read only. Never sort, append to or
	// edit its slices or maps in place; a page that orders or trims a part
	// copies it first (review 1's m8).
	Board *BoardView
	Facts BoardFacts
	// Strip is the read's own copy of its strip's items.
	Strip Strip
}

// WatchPorts are what the board watch reads besides the views: the probe of
// the boards' versions and the kill switch's table, each read once a tick
// (R137, R139); the coordinator's snapshot and whether this instance holds
// the lease, for a queued item's place; the clocks; a shared build's bound;
// and the log, which receives a flight that panicked (nil discards it).
type WatchPorts struct {
	Probe    Probe
	Controls interface {
		KillSwitch(ctx context.Context) (KillSwitch, error)
	}
	Lifecycle func() Lifecycle
	Held      func() bool
	// Now is the database's time as the instance estimates it, the facts'
	// time alone: it may stand still, as the estimate holds its reading after
	// a correction back.
	Now func() time.Time
	// Monotonic is the clock the watch's windows run on (review 2's M2): the
	// tick's freshness, a failed probe's window, the last good tick's, the
	// items read lately and the strips' lives. nil is the process's monotonic
	// clock (clock.Monotonic, time.Now's reading), which serve leaves; tests
	// move theirs.
	Monotonic func() time.Time
	BuildWait time.Duration // a shared build's bound: the database's transaction timeout
	Log       *slog.Logger
	// Context ends the watch's flights, the probe, the projections' builds
	// and the strips' builds, each of which otherwise runs apart from the
	// read that started it, under that read's values, or, a noted strip's
	// rebuild, which no read starts, under this context's: serve ends it
	// before it closes the pools, so that no flight holds a connection past
	// their close (review 2's M10). nil never ends them.
	Context context.Context
}

// BoardWatch serves the board's reads (design D2), off the coordinator's
// lock, and writes nothing: each item's version from one probe a second for
// every item read in the last 10 s (R137); the projection of each version,
// kept and built once (R140); each board's incident strip, one build of an
// incident's at a time (R172); and, at each read, the facts of now (R169). No
// goroutine of its own ticks: a probe, a build and a strip's build run apart
// from the read that started them, or, a noted strip's rebuild, as the build
// before it settles, each within its bound, and end.
type BoardWatch struct {
	v   *Views
	p   WatchPorts
	log *slog.Logger
	// mono is the clock of the watch's windows (WatchPorts.Monotonic).
	mono func() time.Time

	mu sync.Mutex
	// tick is the latest good probe's result, nil before the first; failed
	// is the probe that failed since, nil when none did; and probing is the
	// probe that runs, nil when none does: one runs at a time.
	tick    *tick
	failed  *failure
	probing *probe
	// read holds when each item was last read, for the next tick.
	read map[item.ID]time.Time
	// built holds the projections kept, order their keys, the least recently
	// read first, and building the build that runs of each key.
	built    map[builtKey]built
	order    []builtKey
	building map[builtKey]*flight
	// strips holds the strips kept, stripping the strip's build that runs of
	// each incident, and noted the strips of each incident that a read found
	// past their life while another build of the incident ran, the first
	// noted first (see note).
	strips    map[stripKey]*keptStrip
	stripping map[item.IncidentID]*stripFlight
	noted     map[item.IncidentID][]stripKey
	// projectionSlot and stripSlot are the instance's one build of each kind
	// at a time, a projection's and a strip's (re-review 1's N1; see slot).
	projectionSlot, stripSlot chan struct{}
	// logged holds when a line of each flight's kind and item was last
	// logged, on the monotonic clock (see due).
	logged map[loggedKey]time.Time
}

// loggedKey is what a flight's log line is limited by: the flight's kind,
// "probe", "projection" or "strip", and its item, "" for the probe, whose
// lines are the instance's.
type loggedKey struct {
	kind string
	id   item.ID
}

// NewBoardWatch returns the board watch over v, whose reads it shares, with
// the ports p gives.
func NewBoardWatch(v *Views, p *WatchPorts) *BoardWatch {
	log := p.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	mono := p.Monotonic
	if mono == nil {
		mono = time.Now
	}
	return &BoardWatch{
		v: v, p: *p, log: log, mono: mono, read: map[item.ID]time.Time{}, built: map[builtKey]built{}, building: map[builtKey]*flight{},
		strips: map[stripKey]*keptStrip{}, stripping: map[item.IncidentID]*stripFlight{}, noted: map[item.IncidentID][]stripKey{},
		projectionSlot: make(chan struct{}, 1), stripSlot: make(chan struct{}, 1), logged: map[loggedKey]time.Time{},
	}
}

// slot waits under ctx, a flight's bounded context, for kind, the
// instance's one build of a kind at a time, and returns its release (review
// 2's M6, as re-review 1's N1 corrects it): projectionSlot for a projection's
// build, R175's rebuilds among them, and stripSlot for a strip's, so that a
// projection's build never waits for a strip's, nor a strip's for a
// projection's. The probe takes neither, and waits behind no whole build: the
// web's read stores hold a connection of its pool's two for one query at a
// time, in no transaction, and the pool lends them in the order asked, so the
// probe waits behind at most one query of each kind. One slow build of a kind
// delays the others of its kind up to its bound.
func (w *BoardWatch) slot(ctx context.Context, kind chan struct{}) (release func(), err error) {
	select {
	case kind <- struct{}{}:
		return func() { <-kind }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for the instance's build: %w", ctx.Err())
	}
}

// detached is a flight's context: the values of ctx, the read's that starts
// it, not its end, which is the watch's context's (WatchPorts.Context); and
// the function that releases it, which the flight calls as it ends.
func (w *BoardWatch) detached(ctx context.Context) (flying context.Context, end func()) {
	d, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if w.p.Context == nil {
		return d, cancel
	}
	stop := context.AfterFunc(w.p.Context, cancel)
	return d, func() {
		stop()
		cancel()
	}
}

// rebuildFailed logs the failure for good of a rebuild of R175's of item id's
// board, on records or a board that are invalid, at Error (re-review 2's
// N3): the projection kept is served in its place, "not compared", so that
// no read is told. The line names the item and the failure's class, corrupt,
// never the error's text (see FailureClass).
func (w *BoardWatch) rebuildFailed(ctx context.Context, id item.ID) {
	if !w.due("projection", id) {
		return
	}
	w.log.ErrorContext(ctx, "a rebuild of a board's projection failed for good; the projection kept is served, its marks not compared",
		"item", id, "class", FailureCorrupt)
}

// background is the context of a flight that no read starts, a noted strip's
// rebuild (see startNoted): the watch's own (WatchPorts.Context), else one
// that never ends.
func (w *BoardWatch) background() context.Context {
	if w.p.Context != nil {
		return w.p.Context
	}
	return context.Background()
}

// panicked logs p, a flight's panic that it recovered, at Error, with the
// stack and the panic's type, never its value, which may hold a stored text:
// the flight is the probe, a projection or a strip, of item id, "" for the
// probe; once a minute at most of the flight's kind and item (see due). The
// same code ran in each request before, where net/http recovers a panic; a
// flight apart from requests recovers its own (review 2's I2), so that a
// panic on one board's records never ends the instance.
func (w *BoardWatch) panicked(ctx context.Context, flight string, id item.ID, p any) {
	if !w.due(flight, id) {
		return
	}
	w.log.ErrorContext(ctx, "a flight of the board watch panicked",
		"flight", flight, "item", id, "type", fmt.Sprintf("%T", p), "stack", string(debug.Stack()))
}

// due reports whether a line of the flight's kind of item id is to be logged
// now, and if so notes it logged (re-review 2's N5): the first always, and
// then once in logEvery at most, on the monotonic clock, so that a panic a
// poll, or a rebuild's failure, does not log a line a second. The lines older
// than logEvery are forgotten.
func (w *BoardWatch) due(kind string, id item.ID) bool {
	now := w.mono()
	w.mu.Lock()
	defer w.mu.Unlock()
	k := loggedKey{kind: kind, id: id}
	if at, ok := w.logged[k]; ok && now.Sub(at) < logEvery {
		return false
	}
	maps.DeleteFunc(w.logged, func(_ loggedKey, at time.Time) bool { return now.Sub(at) >= logEvery })
	w.logged[k] = now
	return true
}

// Board reads item id's board: its version from the latest tick, or one
// probe (R137); the projection of that version, cached or built once
// (R140); the incident's strip (R172), but for an item a later build holds,
// whose board says that alone; and the facts of now. An id with no record
// fails with ErrNoItem, and a projection's build as Views.Board fails; a read
// whose context ends while it waits for its version or its projection fails
// with its error, while what it waited for goes on. The strip never fails a
// read (see strip).
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
	if !bv.View.HeldByLaterBuild {
		read.Strip = w.strip(ctx, bv)
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

// without is t without item id, which keeps t's time and its read of the
// kill switch: the next read of id probes it alone.
func (t *tick) without(id item.ID) *tick {
	m := &tick{at: t.at, asked: maps.Clone(t.asked), versions: maps.Clone(t.versions), ks: t.ks, ksRead: t.ksRead}
	delete(m.asked, id)
	delete(m.versions, id)
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
// (see lastGood). An item found is noted as read, for the next ticks (see
// noteRead).
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
		if !slices.Contains(pr.items, id) {
			// Another item's probe: the next tick may cover this one.
			continue
		}
		w.mu.Lock()
		if pr.err == nil {
			v, ok := pr.result.versions[id]
			got = versioned{t: pr.result, v: v, ok: ok}
		} else {
			got = w.lastGood(w.mono(), id, pr.err)
		}
		w.noteRead(id, w.mono(), &got)
		w.mu.Unlock()
		return got.t, got.v, got.ok, got.err
	}
}

// next is what a read of item id's version does now, under w.mu: answer from
// the latest tick while it is fresh, less than tickEvery old, and covers the
// item, whatever probe runs, since a probe that runs while the tick is fresh
// is another item's alone, and changes nothing of this one's (review 1's m1);
// or wait for the probe that runs, which it returns; or, while a probe failed
// less than tickEvery ago, answer from the last good tick, as lastGood does,
// and probe nothing; or start a probe, and return it: of the item alone, to
// merge into a fresh tick, or a tick's, of every item read in the last
// readWithin and this one.
func (w *BoardWatch) next(ctx context.Context, id item.ID) (versioned, *probe) {
	now := w.mono()
	t := w.tick
	fresh := t != nil && now.Sub(t.at) < tickEvery
	if fresh && t.asked[id] {
		v, ok := t.versions[id]
		got := versioned{t: t, v: v, ok: ok}
		w.noteRead(id, now, &got)
		return got, nil
	}
	if w.probing != nil {
		return versioned{}, w.probing
	}
	switch {
	case w.failed != nil && now.Sub(w.failed.at) < tickEvery:
		got := w.lastGood(now, id, w.failed.err)
		w.noteRead(id, now, &got)
		return got, nil
	case fresh:
		return versioned{}, w.start(ctx, []item.ID{id}, false, now)
	}
	items := w.tickItems(now)
	if i, found := slices.BinarySearch(items, id); !found {
		items = slices.Insert(items, i, id)
	}
	return versioned{}, w.start(ctx, items, true, now)
}

// noteRead notes, under w.mu, that item id was read at now, when got found
// its record: an id with no record is answered ErrNoItem from the tick that
// asked it, and never joins a later tick (review 2's M7).
func (w *BoardWatch) noteRead(id item.ID, now time.Time, got *versioned) {
	if got.err == nil && got.ok {
		w.read[id] = now
	}
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
// under w.mu, as the one probe that runs, apart from ctx's end (see detached
// and run).
func (w *BoardWatch) start(ctx context.Context, items []item.ID, tick bool, now time.Time) *probe {
	pr := &probe{items: items, tick: tick, done: make(chan struct{})}
	w.probing = pr
	flying, end := w.detached(ctx)
	go func() {
		defer end()
		w.run(flying, pr, now)
	}()
	return pr
}

// errNotRead stands for a read of the kill switch's table a probe did not
// make.
var errNotRead = errors.New("app: the kill switch's table was not read")

// probed is what a probe read: the versions, and for a tick's the kill
// switch's table and its read's error; or why the probe failed.
type probed struct {
	versions map[item.ID]BoardVersion
	ks       KillSwitch
	ksErr    error
	err      error
}

// errProbePanicked is a probe that panicked: a failed probe (review 2's I2).
var errProbePanicked = errors.New("app: the probe panicked")

// run runs pr, taken at at, within probeWait: the probe, and for a tick's
// the kill switch's table, once (R139), a read of which that fails leaves the
// tick without it (see settleProbe). Its cleanup is registered first, and a
// panic is a failed probe, logged (see panicked).
func (w *BoardWatch) run(ctx context.Context, pr *probe, at time.Time) {
	got := probed{ksErr: errNotRead}
	defer func() {
		if p := recover(); p != nil {
			w.panicked(ctx, "probe", "", p)
			got.err = errProbePanicked
		}
		w.settleProbe(pr, at, &got)
	}()
	probing, cancel := context.WithTimeout(ctx, probeWait)
	defer cancel()
	got.versions, got.err = w.p.Probe.Versions(probing, pr.items)
	if got.err == nil && pr.tick {
		got.ks, got.ksErr = w.p.Controls.KillSwitch(probing)
	}
}

// settleProbe ends pr, taken at at, as got says, under w.mu: a tick's result
// replaces the tick, an item's merges into it, or the failure is noted; pr is
// then done.
func (w *BoardWatch) settleProbe(pr *probe, at time.Time, got *probed) {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer close(pr.done)
	w.probing = nil
	if got.err != nil {
		pr.err = fmt.Errorf("probe the versions of %d boards: %w", len(pr.items), got.err)
		w.failed = &failure{at: at, err: pr.err}
		return
	}
	w.failed = nil
	if pr.tick || w.tick == nil {
		asked := make(map[item.ID]bool, len(pr.items))
		for _, id := range pr.items {
			asked[id] = true
		}
		w.tick = &tick{at: at, asked: asked, versions: got.versions, ks: got.ks, ksRead: got.ksErr == nil}
	} else {
		w.tick = w.tick.merged(pr.items, got.versions)
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

// flight is a projection's build that runs: the projection kept that it
// builds again, nil for a first build (see recovers); the reads that wait for
// it, counted under w.mu (see unwanted); once done is closed, its view or its
// error.
type flight struct {
	stale   *BoardView
	waiting int
	done    chan struct{}
	bv      *BoardView
	err     error
}

// projection is the projection of item id at version v (R140): the one kept,
// unless it missed the revision its marks needed, which answers now (R175;
// see recovers); else the build of it that runs, or one it starts, waited for
// under ctx (see flight). A rebuild that fails, or that a read's wait ends
// before, leaves the projection kept in use (review 1's m2).
func (w *BoardWatch) projection(ctx context.Context, id item.ID, v BoardVersion) (*BoardView, error) {
	k := builtKey{id: id, v: v}
	w.mu.Lock()
	b, kept := w.built[k]
	if kept {
		w.touch(&k)
	}
	w.mu.Unlock()
	var stale *BoardView
	if kept {
		if !w.recovers(ctx, b.bv) {
			return b.bv, b.err
		}
		stale = b.bv
	}
	f := w.flight(ctx, &k, stale)
	defer w.left(f)
	select {
	case <-f.done:
	case <-ctx.Done():
		if kept {
			return b.bv, b.err
		}
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
// found kept and builds again; or else a build it starts, apart from ctx's
// end (see detached): so that a version is built once at a time (see
// build). The read is counted among the flight's waiting until it leaves
// (see left).
func (w *BoardWatch) flight(ctx context.Context, k *builtKey, stale *BoardView) *flight {
	w.mu.Lock()
	defer w.mu.Unlock()
	if f := w.building[*k]; f != nil {
		f.waiting++
		return f
	}
	if b, kept := w.built[*k]; kept && (stale == nil || b.bv != stale) {
		w.touch(k)
		f := &flight{waiting: 1, done: make(chan struct{}), bv: b.bv, err: b.err}
		close(f.done)
		return f
	}
	f := &flight{stale: stale, waiting: 1, done: make(chan struct{})}
	w.building[*k] = f
	key := *k
	flying, end := w.detached(ctx)
	go func() {
		defer end()
		w.build(flying, &key, f)
	}()
	return f
}

// left counts a read that waited for f as gone, under w.mu.
func (w *BoardWatch) left(f *flight) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f.waiting--
}

// errUnwanted is a build that ended without building: no read waited for it,
// and the tick had left its version (see unwanted).
var errUnwanted = errors.New("app: no read waits for the build of a version the tick has left")

// build builds k's projection within BuildWait (R141) (see settleBuild), once
// it gets its slot, unless it is unwanted then, which ends it unbuilt, as a
// build that failed for now (see unwanted). Its cleanup is registered first,
// and a panic, logged (see panicked), is a build that failed for good, as on
// records that are invalid: it wraps item.ErrInvalid, and is kept for its
// version, so that the same version is not built again each poll (review 2's
// I2). A rebuild's failure for good, which no read is told, is logged too
// (see rebuildFailed).
func (w *BoardWatch) build(ctx context.Context, k *builtKey, f *flight) {
	var (
		bv  *BoardView
		err error
	)
	defer func() {
		if p := recover(); p != nil {
			w.panicked(ctx, "projection", k.id, p)
			bv, err = nil, fmt.Errorf("%w: the build of item %s's board panicked", item.ErrInvalid, k.id)
		} else if f.stale != nil && corrupt(err) {
			w.rebuildFailed(ctx, k.id)
		}
		w.settleBuild(k, f, bv, err)
	}()
	building, cancel := context.WithTimeout(ctx, w.p.BuildWait)
	defer cancel()
	release, err := w.slot(building, w.projectionSlot)
	if err != nil {
		return
	}
	defer release()
	if w.unwanted(k, f) {
		err = fmt.Errorf("build item %s's board: %w", k.id, errUnwanted)
		return
	}
	bv, err = w.v.Board(building, k.id, k.v)
}

// unwanted reports, under w.mu, whether f, the build of k that got its slot,
// is to end unbuilt (re-review 2's N4): no read waits for it, and the latest
// tick holds another version of its item, so that a busy board's builds of
// the versions it left never hold the slot that the other boards' builds
// wait for. Such a build is taken out of those that run at once, so that no
// read joins it as it ends; a read of its version builds it again. A rebuild
// of R175's, of the tick's version, never ends so.
func (w *BoardWatch) unwanted(k *builtKey, f *flight) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if f.waiting > 0 || w.tick == nil {
		return false
	}
	if v, ok := w.tick.versions[k.id]; !ok || v == k.v {
		return false
	}
	if w.building[*k] == f {
		delete(w.building, *k)
	}
	return true
}

// settleBuild ends f, the build of k, under w.mu, with bv or err, and keeps
// it, or the error it failed with for good: records or a board that are
// invalid (R140). A rebuild of R175's that failed for good, a panic's among
// them, keeps instead the projection it built again, its revision no longer
// named as missed, so that it is served "not compared" and not built again
// for its version (re-review 2's N3). Any other failure, a store's or an
// unwanted build's, is transient and not kept: a rebuild's leaves the
// projection kept as it was. A version that moved under its build takes its
// item out of the tick, while the tick still holds that version, so that the
// next read probes the item alone, and R140's 503 is answered once (review
// 2's M4).
func (w *BoardWatch) settleBuild(k *builtKey, f *flight, bv *BoardView, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer close(f.done)
	if w.building[*k] == f {
		delete(w.building, *k)
	}
	if f.stale != nil && corrupt(err) {
		uncompared := *f.stale
		uncompared.revisionMissed = ""
		bv, err = &uncompared, nil
	}
	f.bv, f.err = bv, err
	switch {
	case err == nil || corrupt(err):
		w.keep(k, built{bv: bv, err: err})
	case errors.Is(err, ErrMoved) && w.tick != nil:
		if v, ok := w.tick.versions[k.id]; ok && v == k.v {
			w.tick = w.tick.without(k.id)
		}
	}
}

// keep keeps b as k's projection, under w.mu, in place of the one kept
// before, as the one read last; past maxBuilt, the least recently read is
// evicted (review 2's M3): a polled board keeps its version, and versions
// never read again, an item's dead ones among them, go first.
func (w *BoardWatch) keep(k *builtKey, b built) {
	w.built[*k] = b
	w.touch(k)
	for len(w.order) > maxBuilt {
		delete(w.built, w.order[0])
		w.order = slices.Delete(w.order, 0, 1)
	}
}

// touch moves k last in the order of eviction, under w.mu, as the projection
// read last.
func (w *BoardWatch) touch(k *builtKey) {
	if i := slices.Index(w.order, *k); i >= 0 {
		w.order = slices.Delete(w.order, i, i+1)
	}
	w.order = append(w.order, *k)
}

// facts are bv's facts of now (R169), with t, the tick its version is read
// from: the tick's kill switch; whether this instance holds the lease, as the
// lease answers (re-review 1's N2), and, while it does, a queued item's place
// in the coordinator's queue (R17), from its snapshot, a lease lost before
// the snapshot leaving none, and no longer held (m6), and a watch with no
// snapshot giving none; an item's that awaits its close, its auto-close
// paused while the tick's table holds the switch on, unread when the tick
// could not read it, or when it closes by itself (see Views.closesAt); and
// the time now. A view a later build holds has the instance's facts alone.
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
			var waiting []item.ID
			if waiting, f.Held = waitingWhileHeld(w.p.Held, w.p.Lifecycle); f.Held {
				f.QueuePosition = slices.Index(waiting, s.ID) + 1
			}
		}
	case item.StatusAwaitingClose:
		c := w.v.selfCloseOf(ctx, s, &t.ks, t.ksRead)
		f.AutoClosePaused, f.AutoCloseUnread, f.ClosesAt, f.ClosesAtUnread = c.paused, c.unread, c.at, c.atUnread
	}
	return f
}
