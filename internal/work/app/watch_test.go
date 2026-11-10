package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
)

// watchBuildWait is the watch's bound on a shared build in these tests, the
// database's transaction timeout's default.
const watchBuildWait = 10 * time.Second

// errDown is a probe's failure, as when the database cannot be reached.
var errDown = errors.New("the database is down")

// watchWorld is a board watch over stores in memory, with no coordinator
// (the plan's): the records and the boards, the memory probe over them, and
// the kill switch's table, each counted, and the views and the watch over
// them, on time.Now, which a synctest bubble makes its own.
type watchWorld struct {
	t        *testing.T
	logs     *syncBuffer
	records  *memory.Records
	events   *memory.Store
	loads    *countedLoads
	probe    *countedProbe
	switches *countedSwitch
	siblings *countedSiblings
	entries  *countedEntries
	held     atomic.Bool
	views    *app.Views
	watch    *app.BoardWatch
}

// newWatchWorld makes a watch world, its views' ports as change edits them:
// a leader whose queue is empty, the inbound store's titles, and the boards'
// entries and the incidents' siblings, each counted.
func newWatchWorld(t *testing.T, change ...func(p *app.ViewPorts)) *watchWorld {
	t.Helper()
	w := &watchWorld{t: t, logs: &syncBuffer{}, records: memory.NewRecords(time.Now), events: memory.NewStore(time.Now)}
	w.loads = &countedLoads{Records: w.records, made: map[item.ID]int{}}
	w.probe = &countedProbe{Probe: memory.NewProbe(w.records, w.events)}
	w.switches = &countedSwitch{Controls: memory.NewControls(time.Now)}
	w.siblings = &countedSiblings{list: w.records}
	w.entries = &countedEntries{EntryReader: w.events}
	w.held.Store(true)
	p := app.ViewPorts{
		Lifecycle: func() app.Lifecycle { return app.Lifecycle{} }, Controls: w.switches, ReopenWindow: time.Hour, ClosedRulings: time.Hour,
		Closed: w.records, Incidents: w.records, Titles: w.records, Entries: w.entries, Siblings: w.siblings,
	}
	for _, f := range change {
		f(&p)
	}
	w.views = app.NewViews(w.loads, app.NewBoards(w.events, memory.NewCalls(), w.records, &seqIDs{}), &p)
	w.watch = app.NewBoardWatch(w.views, &app.WatchPorts{
		Probe: w.probe, Controls: w.switches, Lifecycle: p.Lifecycle, Held: w.held.Load, Now: time.Now, BuildWait: watchBuildWait,
		Log: slog.New(slog.NewJSONHandler(w.logs, nil)),
	})
	return w
}

// panicked returns the watch's log lines of a flight that panicked.
func (w *watchWorld) panicked() []map[string]any {
	w.t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(w.logs.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		require.NoError(w.t, json.Unmarshal([]byte(line), &entry), line)
		if entry["msg"] == "a flight of the board watch panicked" {
			out = append(out, entry)
		}
	}
	return out
}

// panickedFlights returns the flight and the item of each log line of a
// flight that panicked, in order.
func (w *watchWorld) panickedFlights() []string {
	w.t.Helper()
	lines := w.panicked()
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, fmt.Sprintf("%v %v", line["flight"], line["item"]))
	}
	return out
}

// panicTag is what a panic's value holds in these tests, as a stored text
// might: the watch's log never holds it.
const panicTag = "a stored text the log never holds"

// requirePanicLogged requires one log line of a panic of the flight named,
// at Error, with its stack and not its value.
func (w *watchWorld) requirePanicLogged(flight string) {
	w.t.Helper()
	lines := w.panicked()
	require.Len(w.t, lines, 1, "the panic is logged once")
	assert.Equal(w.t, "ERROR", lines[0]["level"])
	assert.Equal(w.t, flight, lines[0]["flight"])
	assert.Contains(w.t, lines[0]["stack"], "goroutine", "with its stack")
	assert.NotContains(w.t, w.logs.String(), panicTag, "never its value")
}

// incidentOpening is an item's first record's body: an incident a person
// opened.
var incidentOpening = item.Body{Opened: &item.Opened{Type: "incident", Title: "checkout fails", Source: "person", Arm: item.ArmVisible, How: item.NoWindow}}

// add appends a record of b to item id, in incident, of run, numbered after
// the item's history, and returns it.
//
//nolint:gocritic // hugeParam: a test builds each body as a value, and add sets its copy's version.
func (w *watchWorld) add(id item.ID, incident item.IncidentID, run board.RunID, b item.Body) item.Record {
	w.t.Helper()
	b.V = item.Version
	committed, err := w.records.Append(w.t.Context(), id, func(history []item.Record) ([]item.Draft, error) {
		return []item.Draft{{ID: fmt.Sprintf("%s-%d", id, len(history)+1), Item: id, Incident: incident, Run: run, Body: b}}, nil
	})
	require.NoError(w.t, err)
	return committed[0]
}

// open opens item id in incident, as a person does.
func (w *watchWorld) open(id item.ID, incident item.IncidentID) {
	w.t.Helper()
	w.add(id, incident, "", incidentOpening)
}

// move unlinks item id from incident from into incident to: a record more,
// and so a version of its board more.
func (w *watchWorld) move(id item.ID, from, to item.IncidentID) item.Record {
	w.t.Helper()
	return w.add(id, to, "", item.Body{Unlinked: &item.Unlinked{Left: from, By: "admin", Reason: &board.Reason{Code: "not_related"}}})
}

// read reads item id's board through the watch, under the test's context,
// and requires it answered.
func (w *watchWorld) read(id item.ID) app.BoardRead {
	w.t.Helper()
	r, err := w.watch.Board(w.t.Context(), id)
	require.NoError(w.t, err, id)
	return r
}

// countedLoads is the records as the views read them, each item's loads
// counted; fail, when set, fails a load with the error it returns for the
// item; hold, when set, holds each load until it closes, and slow, when set,
// each item's load as long as it says, either until the load's context ends.
type countedLoads struct {
	app.Records
	mu     sync.Mutex
	made   map[item.ID]int
	fail   func(id item.ID) error
	hold   chan struct{}
	holds  map[item.ID]chan struct{}
	slow   func(id item.ID) time.Duration
	panics func(id item.ID) bool
}

func (l *countedLoads) Load(ctx context.Context, id item.ID) ([]item.Record, error) {
	l.mu.Lock()
	l.made[id]++
	fail, hold, slow, panics := l.fail, l.hold, l.slow, l.panics
	if held := l.holds[id]; held != nil {
		hold = held
	}
	l.mu.Unlock()
	if panics != nil && panics(id) {
		panic(errors.New(panicTag))
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if slow != nil {
		select {
		case <-time.After(slow(id)):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail != nil {
		if err := fail(id); err != nil {
			return nil, err
		}
	}
	return l.Records.Load(ctx, id)
}

// of counts the loads of item id.
func (l *countedLoads) of(id item.ID) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.made[id]
}

// set edits the fake under its lock.
func (l *countedLoads) set(f func(l *countedLoads)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f(l)
}

// countedProbe is the memory probe, each call's items noted, sorted. It
// reads the versions first, as the probe's statement takes its snapshot as
// it starts; then hold, when set, holds the call until it closes, and delay,
// when set, waits as long as it says, either until the call's context ends;
// then fail, when set, fails it. Each call's context's error as it answered
// is noted.
type countedProbe struct {
	app.Probe
	mu     sync.Mutex
	calls  [][]item.ID
	fail   error
	hold   chan struct{}
	delay  func() time.Duration
	ended  []error
	panics bool
	// edit, when set, edits each call's versions before they are answered.
	edit func(versions map[item.ID]app.BoardVersion)
}

func (p *countedProbe) Versions(ctx context.Context, items []item.ID) (map[item.ID]app.BoardVersion, error) {
	p.mu.Lock()
	p.calls = append(p.calls, slices.Sorted(slices.Values(items)))
	fail, hold, delay, panics, edit := p.fail, p.hold, p.delay, p.panics, p.edit
	p.mu.Unlock()
	if panics {
		panic(errors.New(panicTag))
	}
	versions, err := p.Probe.Versions(ctx, items)
	if edit != nil && err == nil {
		edit(versions)
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
		}
	}
	if delay != nil {
		select {
		case <-time.After(delay()):
		case <-ctx.Done():
		}
	}
	p.mu.Lock()
	p.ended = append(p.ended, ctx.Err())
	p.mu.Unlock()
	switch {
	case fail != nil:
		return nil, fail
	case ctx.Err() != nil:
		return nil, ctx.Err()
	}
	return versions, err
}

// endings returns each call's context's error as it answered, in order.
func (p *countedProbe) endings() []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.ended)
}

// made returns the items of each call made, in order.
func (p *countedProbe) made() [][]item.ID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

// set edits the fake under its lock.
func (p *countedProbe) set(f func(p *countedProbe)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

// countedSwitch is the kill switch's table, its reads counted; fail, when
// set, fails each read.
type countedSwitch struct {
	*memory.Controls
	mu    sync.Mutex
	reads int
	fail  bool
}

func (s *countedSwitch) KillSwitch(ctx context.Context) (app.KillSwitch, error) {
	s.mu.Lock()
	s.reads++
	fail := s.fail
	s.mu.Unlock()
	if fail {
		return app.KillSwitch{}, errBlinked
	}
	return s.Controls.KillSwitch(ctx)
}

// made counts the reads.
func (s *countedSwitch) made() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// failing has each read from now on fail, or read again.
func (s *countedSwitch) failing(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = fail
}

// turn writes the kill switch's next row, on or off.
func (s *countedSwitch) turn(t *testing.T, on bool) {
	t.Helper()
	ks, err := s.Controls.KillSwitch(t.Context())
	require.NoError(t, err)
	_, changed, err := s.SetKillSwitch(t.Context(), on, "admin", board.Reason{Code: "incident"}, ks.Seq)
	require.NoError(t, err)
	require.True(t, changed)
}

// One probe a second serves every read (design D2; R137): after a warm-up
// that reads five items, twenty reads of them at once, past the second, make
// one probe, of the five; and once an item is not read for 10 s, the tick
// probes it no more. Fails first: a build failure.
func TestOneProbeServesEveryReadOfASecond(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		items := []item.ID{"item-a", "item-b", "item-c", "item-d", "item-e"}
		for _, id := range items {
			w.open(id, item.IncidentID("inc-"+id))
		}
		for _, id := range items {
			w.read(id)
		}
		time.Sleep(1500 * time.Millisecond)
		warm := len(w.probe.made())

		var wg sync.WaitGroup
		for i := range 20 {
			wg.Go(func() {
				_, err := w.watch.Board(t.Context(), items[i%len(items)])
				assert.NoError(t, err)
			})
		}
		wg.Wait()

		made := w.probe.made()
		require.Len(t, made, warm+1, "one probe served the twenty reads")
		assert.Equal(t, items, made[warm], "of every item read in the last 10 s")

		for range 6 {
			time.Sleep(2 * time.Second)
			for _, id := range items[:4] {
				w.read(id)
			}
		}
		made = w.probe.made()
		assert.Equal(t, items[:4], made[len(made)-1], "an item unread for 10 s leaves the tick")
	})
}

// A read of an item the latest tick did not cover probes that item alone,
// once, and merges it in; the next tick probes it with the others (re-review
// N7). Fails first: a build failure.
func TestAnItemTheTickDidNotCoverIsProbedOnceAndJoinsTheNextTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		w.open("item-x", "inc-x")
		w.read("item-a")

		w.read("item-x")
		w.read("item-x")
		w.read("item-a")

		assert.Equal(t, [][]item.ID{{"item-a"}, {"item-x"}}, w.probe.made(), "the item alone, once; the tick serves the rest")
		time.Sleep(1500 * time.Millisecond)
		w.read("item-a")
		made := w.probe.made()
		assert.Equal(t, []item.ID{"item-a", "item-x"}, made[len(made)-1], "it joins the next tick")
	})
}

// A read the fresh tick covers answers from it at once, though a probe of
// another item alone runs (review 1's m1; R137): that probe changes nothing
// of this item's version. (The plan's.)
func TestACoveredReadWaitsForNoProbeOfAnotherItem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		w.open("item-x", "inc-x")
		w.read("item-a")
		hold := make(chan struct{})
		w.probe.set(func(p *countedProbe) { p.hold = hold })
		var wg sync.WaitGroup
		defer func() {
			close(hold)
			wg.Wait()
		}()
		wg.Go(func() {
			_, err := w.watch.Board(t.Context(), "item-x")
			assert.NoError(t, err)
		})
		synctest.Wait()
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()
		began := time.Now()

		r, err := w.watch.Board(ctx, "item-a")

		require.NoError(t, err, "the fresh tick covers item-a")
		assert.Zero(t, time.Since(began), "at once")
		assert.Equal(t, int64(1), r.Board.Version.Record.Seq)
	})
}

// An id with no record is answered ErrNoItem from the probe that asked it,
// and from its tick while that is fresh, and never joins a later tick
// (review 2's M7): only an item a probe found is noted as read. (The plan's.)
func TestAnIDWithNoRecordNeverJoinsTheTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		w.read("item-a")
		for range 2 {
			_, err := w.watch.Board(t.Context(), "item-gone")
			require.ErrorIs(t, err, app.ErrNoItem)
		}
		assert.Equal(t, [][]item.ID{{"item-a"}, {"item-gone"}}, w.probe.made(), "probed once, then answered from its tick")
		time.Sleep(1500 * time.Millisecond)

		w.read("item-a")

		made := w.probe.made()
		assert.Equal(t, []item.ID{"item-a"}, made[len(made)-1], "never joins a later tick")
	})
}

// A probe runs apart from the reader that started it (plan review M2): that
// reader's context, ended while the probe runs, fails that reader alone; the
// probe goes on under its own bound, and the other readers get its result.
// Fails first: a build failure.
func TestAProbeOutlivesTheReaderThatStartedIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		hold := make(chan struct{})
		w.probe.set(func(p *countedProbe) { p.hold = hold })
		ctx, cancel := context.WithCancel(t.Context())
		var first error
		var wg sync.WaitGroup
		wg.Go(func() { _, first = w.watch.Board(ctx, "item-a") })
		synctest.Wait()
		reads := make([]app.BoardRead, 2)
		errs := make([]error, 2)
		for i := range reads {
			wg.Go(func() { reads[i], errs[i] = w.watch.Board(t.Context(), "item-a") })
		}
		synctest.Wait()

		cancel()
		synctest.Wait()
		close(hold)
		wg.Wait()

		require.ErrorIs(t, first, context.Canceled, "the reader that gave up")
		for i := range reads {
			require.NoError(t, errs[i])
			assert.Equal(t, int64(1), reads[i].Board.Version.Record.Seq)
		}
		assert.Len(t, w.probe.made(), 1, "one probe served all three")
		assert.Equal(t, []error{nil}, w.probe.ended, "the probe's context outlived the reader's")
	})
}

// A failed probe leaves the last good tick in use for 5 s (plan review M2):
// reads answer from it, its version, though a record landed since, until it
// is 5 s old; then they fail with the probe's error, which the pages answer
// 503, until a probe succeeds. With no good tick ever, a read fails at once.
// A failed probe is not tried again within its second, so that a failing
// database sees one probe a second too (the plan's). Fails first: a build
// failure.
func TestAFailedProbeServesTheLastGoodTickForFiveSeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		w.probe.set(func(p *countedProbe) { p.fail = errDown })
		_, err := w.watch.Board(t.Context(), "item-a")
		require.ErrorIs(t, err, errDown, "no good tick ever: at once")
		_, err = w.watch.Board(t.Context(), "item-a")
		require.ErrorIs(t, err, errDown)
		assert.Len(t, w.probe.made(), 1, "not tried again within its second")

		time.Sleep(1500 * time.Millisecond)
		w.probe.set(func(p *countedProbe) { p.fail = nil })
		good := time.Now()
		first := w.read("item-a").Board.Version
		w.move("item-a", "inc-a", "inc-b")
		w.probe.set(func(p *countedProbe) { p.fail = errDown })
		for _, at := range []time.Duration{1500 * time.Millisecond, 3 * time.Second, 4500 * time.Millisecond} {
			time.Sleep(time.Until(good.Add(at)))
			r, err := w.watch.Board(t.Context(), "item-a")
			require.NoError(t, err, "%v after the good tick", at)
			assert.Equal(t, first, r.Board.Version, "the last good tick's version, %v after it", at)
		}
		probes := len(w.probe.made())
		for _, at := range []time.Duration{5500 * time.Millisecond, 7 * time.Second} {
			time.Sleep(time.Until(good.Add(at)))
			_, err := w.watch.Board(t.Context(), "item-a")
			require.ErrorIs(t, err, errDown, "%v after the good tick", at)
		}
		assert.Greater(t, len(w.probe.made()), probes, "each second probes again")

		w.probe.set(func(p *countedProbe) { p.fail = nil })
		time.Sleep(1500 * time.Millisecond)
		assert.Equal(t, int64(2), w.read("item-a").Board.Version.Record.Seq, "a probe that succeeds serves again")
	})
}

// A projection is built once per version (design D2; R140): ten reads at once
// of a new version load its records once. The projections kept are 64, the
// least recently read evicted first (review 2's M3; review 1's m4), so that a
// polled board keeps its version while versions never read again go: of the
// 64 kept, the first is read again, and one more kept evicts the second,
// which a read builds again, while the first stays kept. Fails first: a
// build failure.
func TestAProjectionIsBuiltOncePerVersion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		var wg sync.WaitGroup
		for range 10 {
			wg.Go(func() {
				_, err := w.watch.Board(t.Context(), "item-a")
				assert.NoError(t, err)
			})
		}
		wg.Wait()
		assert.Equal(t, 1, w.loads.of("item-a"), "ten reads, one build")

		e := newWatchWorld(t)
		ids := make([]item.ID, 65)
		for i := range ids {
			ids[i] = item.ID(fmt.Sprintf("item-%02d", i))
			e.open(ids[i], item.IncidentID("inc-"+ids[i]))
		}
		for _, id := range ids[:64] {
			e.read(id)
		}
		e.read(ids[0])
		assert.Equal(t, 1, e.loads.of(ids[0]), "64 are kept")
		e.read(ids[64])
		e.read(ids[1])
		assert.Equal(t, 2, e.loads.of(ids[1]), "the least recently read is evicted first, and built again")
		e.read(ids[0])
		assert.Equal(t, 1, e.loads.of(ids[0]), "the first, read again since, stays kept")
	})
}

// A version once answered is never followed by an older one (Review Focus
// 3): readers that read an item's board while its records grow, through a
// probe whose answers take varying times, each get a version at least as new
// as every version answered before their read began.
func TestAWatchNeverAnswersAnOlderVersionThanItGaveBefore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-0")
		var turn atomic.Int64
		delays := []time.Duration{700 * time.Millisecond, 100 * time.Millisecond, 400 * time.Millisecond, 0}
		w.probe.set(func(p *countedProbe) {
			p.delay = func() time.Duration { return delays[turn.Add(1)%int64(len(delays))] }
		})
		type answer struct {
			began, ended int64
			seq          int64
			err          error
		}
		var (
			order   atomic.Int64
			mu      sync.Mutex
			answers []answer
			wg      sync.WaitGroup
		)
		wg.Go(func() {
			for i := range 30 {
				time.Sleep(300 * time.Millisecond)
				w.move("item-a", item.IncidentID(fmt.Sprintf("inc-%d", i)), item.IncidentID(fmt.Sprintf("inc-%d", i+1)))
			}
		})
		for r := range 5 {
			wg.Go(func() {
				for range 20 {
					time.Sleep(time.Duration(200+100*r) * time.Millisecond)
					began := order.Add(1)
					got, err := w.watch.Board(t.Context(), "item-a")
					a := answer{began: began, ended: order.Add(1), err: err}
					if err == nil {
						a.seq = got.Board.Version.Record.Seq
					}
					mu.Lock()
					answers = append(answers, a)
					mu.Unlock()
				}
			})
		}
		wg.Wait()

		require.Len(t, answers, 100)
		for _, a := range answers {
			require.NoError(t, a.err)
		}
		for _, a := range answers {
			for _, b := range answers {
				if a.ended < b.began {
					require.GreaterOrEqual(t, b.seq, a.seq, "a read that began after one answered seq %d answered %d", a.seq, b.seq)
				}
			}
		}
		assert.Greater(t, answers[len(answers)-1].seq, int64(1), "the records grew under the reads")
	})
}

// A build that fails for good, as on records that are invalid, is cached as
// its error for its version (R140): the item's records load once while the
// version stands. A store's failure is not cached: each read builds again,
// until one succeeds. Fails first: a build failure.
func TestALastingBuildErrorIsCachedForItsVersion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.add("item-bad", "inc-bad", "", item.Body{Started: &item.Started{Commit: "c1"}})
		for range 3 {
			_, err := w.watch.Board(t.Context(), "item-bad")
			require.ErrorIs(t, err, item.ErrInvalid)
		}
		assert.Equal(t, 1, w.loads.of("item-bad"), "a lasting error, cached for its version")

		w.open("item-blip", "inc-blip")
		blips := 2
		w.loads.set(func(l *countedLoads) {
			l.fail = func(id item.ID) error {
				if id != "item-blip" || blips == 0 {
					return nil
				}
				blips--
				return errBlinked
			}
		})
		for range 2 {
			_, err := w.watch.Board(t.Context(), "item-blip")
			require.ErrorIs(t, err, errBlinked)
		}
		w.read("item-blip")
		w.read("item-blip")
		assert.Equal(t, 3, w.loads.of("item-blip"), "two failures not cached, then one build kept")
	})
}

// After a build fails ErrMoved, its item leaves the tick (review 2's M4;
// R140): the read answers the failure once, and the next read, within the
// tick's second, probes the item alone and answers the version the store
// holds. Here the probe answers a version as it stood before a restore, of
// another record id at the record's seq. (The plan's.)
func TestAMovedVersionIsProbedAgainAtTheNextRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		w.probe.set(func(p *countedProbe) {
			p.edit = func(versions map[item.ID]app.BoardVersion) {
				v := versions["item-a"]
				v.Record.ID = "before-the-restore"
				versions["item-a"] = v
			}
		})
		_, err := w.watch.Board(t.Context(), "item-a")
		require.ErrorIs(t, err, app.ErrMoved)
		w.probe.set(func(p *countedProbe) { p.edit = nil })

		r, err := w.watch.Board(t.Context(), "item-a")

		require.NoError(t, err, "the next read probes the item again")
		assert.Len(t, w.probe.made(), 2, "once")
		assert.Equal(t, app.Mark{ID: "item-a-1", Seq: 1}, r.Board.Version.Record)
	})
}

// A read that gives up on its wait ends with its context's error, while its
// build, apart from it under the build's own bound, goes on and lands in the
// cache: the next read is served from it (R141). Fails first: a build
// failure.
func TestAReadPastItsWaitEndsWhileItsBuildGoesOnAndIsCached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		hold := make(chan struct{})
		w.loads.set(func(l *countedLoads) { l.hold = hold })
		short, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()

		_, err := w.watch.Board(short, "item-a")

		require.ErrorIs(t, err, context.DeadlineExceeded, "the read past its wait")
		close(hold)
		synctest.Wait()
		w.read("item-a")
		assert.Equal(t, 1, w.loads.of("item-a"), "the build landed, and served the next read")
	})
}

// The tick reads the kill switch's table once (R139), whatever the number of
// reads it serves, and the probe of an item alone reads it not at all; a
// table that cannot be read leaves the board answered, and says so. Fails
// first: a build failure.
func TestTheTickReadsTheKillSwitchOnceATick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		for _, id := range []item.ID{"item-a", "item-b", "item-c"} {
			w.open(id, item.IncidentID("inc-"+id))
		}
		for range 4 {
			for _, id := range []item.ID{"item-a", "item-b", "item-c"} {
				w.read(id)
			}
		}
		assert.Equal(t, 1, w.switches.made(), "one read for the tick; none for the items probed alone")

		time.Sleep(1500 * time.Millisecond)
		w.switches.turn(t, true)
		r := w.read("item-a")
		w.read("item-b")
		assert.Equal(t, 2, w.switches.made(), "one more for the next tick")
		assert.True(t, r.Facts.KillSwitchRead)
		assert.True(t, r.Facts.KillSwitch.On, "the tick's read")

		time.Sleep(1500 * time.Millisecond)
		w.switches.failing(true)
		r = w.read("item-a")
		assert.False(t, r.Facts.KillSwitchRead, "the tick could not read the table")
		assert.Equal(t, int64(1), r.Board.Version.Record.Seq, "the board is answered")
	})
}

// A projection's build that panics fails as a corrupt one does (review 2's
// I2): every read waiting for it fails, wrapping item.ErrInvalid, as the
// pages answer a corrupt item; its flight is cleared; it is kept for its
// version, so that the same version is not built again each poll, while a
// new version builds again; and the watch logs the panic once, with its
// stack, never its value. (The plan's.)
func TestAProjectionsBuildThatPanicsFailsAsACorruptOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		w.loads.set(func(l *countedLoads) { l.panics = func(id item.ID) bool { return id == "item-a" } })
		errs := make([]error, 5)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Go(func() { _, errs[i] = w.watch.Board(t.Context(), "item-a") })
		}
		wg.Wait()

		for i := range errs {
			require.ErrorIs(t, errs[i], item.ErrInvalid, "every waiter is told")
		}
		assert.Zero(t, w.watch.FlightsForTest(), "its flight is cleared")
		_, err := w.watch.Board(t.Context(), "item-a")
		require.ErrorIs(t, err, item.ErrInvalid)
		assert.Equal(t, 1, w.loads.of("item-a"), "kept for its version")
		w.requirePanicLogged("projection")
		w.loads.set(func(l *countedLoads) { l.panics = nil })
		w.move("item-a", "inc-a", "inc-b")
		time.Sleep(1500 * time.Millisecond)
		assert.Equal(t, int64(2), w.read("item-a").Board.Version.Record.Seq, "a new version builds again")
	})
}

// A probe that panics is a failed probe (review 2's I2): every read waiting
// for it fails, with no good tick to answer from; its flight is cleared; the
// watch logs the panic once, with its stack, never its value; and a probe a
// second later serves again. (The plan's.)
func TestAProbeThatPanicsIsAFailedProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		w.probe.set(func(p *countedProbe) { p.panics = true })
		errs := make([]error, 3)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Go(func() { _, errs[i] = w.watch.Board(t.Context(), "item-a") })
		}
		wg.Wait()

		for i := range errs {
			require.Error(t, errs[i], "every waiter is told")
			require.NotErrorIs(t, errs[i], item.ErrInvalid, "a probe's failure is the database's, answered 503")
			require.NotErrorIs(t, errs[i], app.ErrNoItem, "never a board that is not there")
		}
		assert.Zero(t, w.watch.FlightsForTest(), "its flight is cleared")
		w.requirePanicLogged("probe")
		w.probe.set(func(p *countedProbe) { p.panics = false })
		time.Sleep(1500 * time.Millisecond)
		assert.Equal(t, int64(1), w.read("item-a").Board.Version.Record.Seq, "the next probe serves")
	})
}

// A projection's build that a read waits for is built though the tick has
// left its version (re-review 2's N4): item-a's read with no deadline, joined
// to the build that a read past its wait started, is answered the version it
// read, once item-z's build frees the slot, though a tick read the item's
// next version meanwhile. (The plan's.)
func TestABuildAReadWaitsForIsBuiltThoughTheTickLeftItsVersion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		w.open("item-z", "inc-z")
		w.open("item-q", "inc-q")
		hold := make(chan struct{})
		w.loads.set(func(l *countedLoads) { l.holds = map[item.ID]chan struct{}{"item-z": hold} })
		var wg sync.WaitGroup
		wg.Go(func() { _, _ = w.watch.Board(t.Context(), "item-z") })
		synctest.Wait()
		short, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		wg.Go(func() { _, _ = w.watch.Board(short, "item-a") })
		synctest.Wait()
		var waited app.BoardRead
		var err error
		wg.Go(func() { waited, err = w.watch.Board(t.Context(), "item-a") })
		synctest.Wait()
		time.Sleep(1500 * time.Millisecond)
		w.move("item-a", "inc-a", "inc-b")
		wg.Go(func() { _, _ = w.watch.Board(t.Context(), "item-q") })
		synctest.Wait()
		made := w.probe.made()
		require.Equal(t, []item.ID{"item-a", "item-q", "item-z"}, made[len(made)-1], "a tick read item-a's next version")

		close(hold)
		wg.Wait()

		require.NoError(t, err, "a read waits for the build")
		assert.Equal(t, int64(1), waited.Board.Version.Record.Seq, "the version it read")
	})
}

// A projection's build of the tick's version is built though no read waits
// for it (re-review 2's N4; R141): item-a's read, past its wait while item-z's
// build holds the slot, leaves a build that gets the slot later, builds and
// is kept, and the next read is answered from it at once. (The plan's.)
func TestABuildOfTheTicksVersionIsBuiltThoughNoReadWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		w.open("item-z", "inc-z")
		w.slowLoads(map[item.ID]time.Duration{"item-a": time.Second})
		hold := make(chan struct{})
		w.loads.set(func(l *countedLoads) { l.holds = map[item.ID]chan struct{}{"item-z": hold} })
		var wg sync.WaitGroup
		wg.Go(func() { _, _ = w.watch.Board(t.Context(), "item-z") })
		synctest.Wait()
		short, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		_, err := w.watch.Board(short, "item-a")
		require.ErrorIs(t, err, context.DeadlineExceeded)
		close(hold)
		wg.Wait()
		time.Sleep(2 * time.Second)
		began := time.Now()

		r := w.read("item-a")

		assert.Zero(t, time.Since(began), "answered from the build kept")
		assert.Equal(t, int64(1), r.Board.Version.Record.Seq)
		assert.Equal(t, 1, w.loads.of("item-a"), "built once, by the build the read left")
	})
}

// A flight's panic is logged at most once a minute of its kind and item
// (re-review 2's N5; R176): a probe that panics at each read, a second apart,
// logs its first panic and the one a minute later; the projections of two
// items that panic then each log their own, once in their minute, and a
// strip's of one of them its own. (The plan's.)
func TestAFlightsPanicIsLoggedAtMostOnceAMinuteOfItsKindAndItem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		w.open("item-b", "inc-b")
		w.probe.set(func(p *countedProbe) { p.panics = true })
		for range 61 {
			_, err := w.watch.Board(t.Context(), "item-a")
			require.Error(t, err)
			time.Sleep(time.Second)
		}
		require.Len(t, w.panickedFlights(), 2, "the first, and the one a minute later")
		w.probe.set(func(p *countedProbe) { p.panics = false })
		w.loads.set(func(l *countedLoads) { l.panics = func(item.ID) bool { return true } })
		for _, id := range []item.ID{"item-a", "item-b"} {
			_, err := w.watch.Board(t.Context(), id)
			require.ErrorIs(t, err, item.ErrInvalid, id)
		}
		w.move("item-a", "inc-a", "inc-c")
		time.Sleep(1500 * time.Millisecond)
		_, err := w.watch.Board(t.Context(), "item-a")
		require.ErrorIs(t, err, item.ErrInvalid, "its new version's build panics again")
		w.loads.set(func(l *countedLoads) { l.panics = nil })
		w.siblings.set(func(s *countedSiblings) { s.panics = true })
		w.move("item-a", "inc-c", "inc-d")
		time.Sleep(1500 * time.Millisecond)
		require.True(t, w.read("item-a").Strip.Unread, "its strip's build panics")

		assert.Equal(t, []string{"probe ", "probe ", "projection item-a", "projection item-b", "strip item-a"}, w.panickedFlights())
		assert.NotContains(t, w.logs.String(), panicTag)
	})
}

// One build of each kind runs at a time on the instance (review 2's M6, as
// re-review 1's N1 corrects it): while a projection's build is held, a probe
// of another item answers, and that item's projection build starts only once
// the first ends; while a strip's build of one incident is held, a strip
// build of another incident starts only once it ends, and that incident's
// board's projection is built meanwhile. (The plan's.)
func TestOneBuildOfEachKindRunsAtATimeOnTheInstance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		w.open("item-b", "inc-b")
		hold := make(chan struct{})
		w.loads.set(func(l *countedLoads) { l.holds = map[item.ID]chan struct{}{"item-a": hold} })
		var wg sync.WaitGroup
		for _, id := range []item.ID{"item-a", "item-b"} {
			wg.Go(func() {
				_, err := w.watch.Board(t.Context(), id)
				assert.NoError(t, err, id)
			})
			synctest.Wait()
		}

		assert.Equal(t, [][]item.ID{{"item-a"}, {"item-b"}}, w.probe.made())
		assert.Len(t, w.probe.endings(), 2, "the probe of item-b answers while item-a's build is held")
		assert.Zero(t, w.loads.of("item-b"), "item-b's build waits for item-a's")
		close(hold)
		wg.Wait()
		assert.Equal(t, 1, w.loads.of("item-b"), "and starts once it ends")

		v := newWatchWorld(t)
		v.open("item-x", "inc-1")
		v.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		v.open("item-y", "inc-2")
		v.conclude("item-u", "inc-2", "incident", checkoutRelease, true)
		listing := make(chan struct{})
		v.siblings.set(func(s *countedSiblings) { s.holdOf = map[item.IncidentID]chan struct{}{"inc-1": listing} })
		for _, id := range []item.ID{"item-x", "item-y"} {
			wg.Go(func() {
				_, err := v.watch.Board(t.Context(), id)
				assert.NoError(t, err, id)
			})
			synctest.Wait()
		}
		assert.Equal(t, 1, v.loads.of("item-y"), "item-y's projection is built while item-x's strip build is held")
		assert.Zero(t, v.siblings.listed("item-y"), "item-y's strip build waits for item-x's")
		close(listing)
		wg.Wait()
		assert.Equal(t, 1, v.siblings.listed("item-y"), "and starts once it ends")
	})
}

// A projection's build never waits for a strip's (re-review 1's N1): while
// item-y's strip build holds its listing, a read of item-x, whose projection
// is not kept, answers its board within its wait, its strip unread behind
// item-y's build of their incident. (The plan's.)
func TestAProjectionBuildNeverWaitsForAStripBuild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.open("item-y", "inc-1")
		hold := make(chan struct{})
		var wg sync.WaitGroup
		defer func() {
			close(hold)
			wg.Wait()
		}()
		w.siblings.set(func(s *countedSiblings) { s.hold = hold })
		wg.Go(func() { _, _ = w.watch.Board(t.Context(), "item-y") })
		synctest.Wait()
		ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
		defer cancel()

		r, err := w.watch.Board(ctx, "item-x")

		require.NoError(t, err, "its projection is built though a strip's build holds its listing")
		require.NotNil(t, r.Board)
		assert.Equal(t, 1, w.loads.of("item-x"))
		assert.Equal(t, app.Strip{Unread: true}, r.Strip, "its strip queues behind item-y's")
	})
}

// A strip's build never waits for a projection's (re-review 1's N1): while
// item-z's projection build is held, item-x's strip, past its life, is built
// again at once and kept. (The plan's.)
func TestAStripBuildNeverWaitsForAProjectionBuild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		w.open("item-z", "inc-z")
		w.read("item-x")
		for range 5 {
			time.Sleep(5 * time.Second)
			w.read("item-x")
		}
		time.Sleep(5 * time.Second)
		hold := make(chan struct{})
		w.loads.set(func(l *countedLoads) { l.holds = map[item.ID]chan struct{}{"item-z": hold} })
		var wg sync.WaitGroup
		defer func() {
			close(hold)
			wg.Wait()
		}()
		wg.Go(func() { _, _ = w.watch.Board(t.Context(), "item-z") })
		synctest.Wait()
		w.conclude("item-t", "inc-1", "incident", checkoutRelease, true)

		_, err := w.polled("item-x")
		require.NoError(t, err)
		synctest.Wait()
		r, err := w.polled("item-x")

		require.NoError(t, err)
		assert.Equal(t, []item.ID{"item-t", "item-s"}, stripIDs(r.Strip.Items), "rebuilt while item-z's projection build is held")
	})
}

// A projection's build that no read waits for, whose version the tick has
// left, ends without building once it gets its slot (re-review 2's N4): an
// item whose builds take 1.5 s, and whose version moves every 1.1 s, read
// under 300 ms each time, builds fewer than its eight versions, and another
// item's first read, polled, answers. (The plan's.)
func TestABuildNoReadWaitsForOfAVersionTheTickLeftEndsUnbuilt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-a", "inc-a")
		w.open("item-c", "inc-c")
		w.slowLoads(map[item.ID]time.Duration{"item-a": 1500 * time.Millisecond})
		var wg sync.WaitGroup
		from := item.IncidentID("inc-a")
		for i := range 8 {
			to := item.IncidentID(fmt.Sprintf("inc-a%d", i))
			w.move("item-a", from, to)
			from = to
			wg.Go(func() {
				ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
				defer cancel()
				_, _ = w.watch.Board(ctx, "item-a")
			})
			time.Sleep(1100 * time.Millisecond)
		}

		r, err := w.polled("item-c")

		require.NoError(t, err, "its build waits behind no build of a version left")
		require.NotNil(t, r.Board)
		assert.Less(t, w.loads.of("item-a"), 8, "a version left, for no read, is not built")
		wg.Wait()
	})
}

// The probe answers while a build of each kind is held (re-review 1's N1):
// the web pool's two connections serve one build of each kind at most, and
// the probe waits behind at most one query of each, never a whole build.
// (The plan's; a guard of review 2's M6.)
func TestTheProbeAnswersWhileABuildOfEachKindIsHeld(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.open("item-y", "inc-1")
		w.open("item-z", "inc-z")
		w.open("item-q", "inc-q")
		listing, loading := make(chan struct{}), make(chan struct{})
		w.siblings.set(func(s *countedSiblings) { s.holdOf = map[item.IncidentID]chan struct{}{"inc-1": listing} })
		var wg sync.WaitGroup
		defer func() {
			close(listing)
			close(loading)
			wg.Wait()
		}()
		wg.Go(func() { _, _ = w.watch.Board(t.Context(), "item-x") })
		synctest.Wait()
		w.loads.set(func(l *countedLoads) { l.holds = map[item.ID]chan struct{}{"item-z": loading} })
		wg.Go(func() { _, _ = w.watch.Board(t.Context(), "item-z") })
		synctest.Wait()
		_, most := w.siblings.counts()
		require.Equal(t, 1, most, "item-x's strip build holds its listing")
		require.Equal(t, 1, w.loads.of("item-z"), "item-z's projection build holds its load")
		probes := len(w.probe.endings())
		wg.Go(func() { _, _ = w.watch.Board(t.Context(), "item-q") })
		synctest.Wait()

		assert.Len(t, w.probe.endings(), probes+1, "the probe of item-q answers")
	})
}

// The watch's windows run on the process's monotonic clock (review 2's M2):
// with a Now that stands still, as the database clock's estimate holds its
// reading after a correction back, the tick still ages, and a read a second
// later probes again and answers the record landed since; Now gives the
// facts' time alone. (The plan's.)
func TestTheWatchsWindowsRunOnTheMonotonicClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		frozen := time.Now()
		w.watch = app.NewBoardWatch(w.views, &app.WatchPorts{
			Probe: w.probe, Controls: w.switches, Lifecycle: func() app.Lifecycle { return app.Lifecycle{} }, Held: w.held.Load,
			Now: func() time.Time { return frozen }, BuildWait: watchBuildWait,
		})
		w.open("item-a", "inc-a")
		w.read("item-a")
		w.move("item-a", "inc-a", "inc-b")
		time.Sleep(1500 * time.Millisecond)

		again := w.read("item-a")

		assert.Len(t, w.probe.made(), 2, "probed again a second later")
		assert.Equal(t, int64(2), again.Board.Version.Record.Seq)
		assert.Equal(t, frozen, again.Facts.Now, "Now gives the facts' time alone")
	})
}

// The watch's flights end with its context, which serve ends before it
// closes the pools (review 2's M10): a build held at shutdown ends at once,
// its reader told and its flight cleared, rather than at the build's bound,
// which would hold a connection past the pool's close. (The plan's.)
func TestABuildHeldAtShutdownEndsWithTheWatchsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		watching, stop := context.WithCancel(t.Context())
		defer stop()
		w.watch = app.NewBoardWatch(w.views, &app.WatchPorts{
			Probe: w.probe, Controls: w.switches, Lifecycle: func() app.Lifecycle { return app.Lifecycle{} }, Held: w.held.Load,
			Now: time.Now, BuildWait: watchBuildWait, Context: watching,
		})
		w.open("item-a", "inc-a")
		hold := make(chan struct{})
		defer close(hold)
		w.loads.set(func(l *countedLoads) { l.hold = hold })
		var err error
		var wg sync.WaitGroup
		wg.Go(func() { _, err = w.watch.Board(t.Context(), "item-a") })
		synctest.Wait()
		began := time.Now()

		stop()
		wg.Wait()

		require.ErrorIs(t, err, context.Canceled, "the build ended with the watch's context")
		assert.Zero(t, time.Since(began), "at once, not at the build's bound")
		assert.Zero(t, w.watch.FlightsForTest(), "its flight is cleared")
	})
}

// watchClock is a watch's clock over the harness, moved by hand, apart from
// the harness's own, whose moves fire the coordinator's timers.
type watchClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *watchClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *watchClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// harnessWatch is a board watch over the harness's stores (the plan's):
// views over records, the harness's records' or a test's in front of them,
// with viewPorts' ports, the waits of h's options, the inbound store's titles
// and the harness's entries and siblings; the memory probe over the
// harness's records and boards; the coordinator's snapshot; an instance that
// holds the lease as held says; and the clock now gives, the facts' and the
// windows'; with the watch's ports as change edits them.
func harnessWatch(h *harness, records app.Records, held *atomic.Bool, now func() time.Time, change ...func(p *app.WatchPorts)) *app.BoardWatch {
	p := viewPorts(h)
	p.Waits, p.Titles, p.Entries, p.Siblings = waitsOf(h), h.records, h.store.Store, h.records.Records
	wp := app.WatchPorts{
		Probe: memory.NewProbe(h.records.Records, h.store.Store), Controls: killSwitchReads{h.controls},
		Lifecycle: h.c.Lifecycle, Held: held.Load, Now: now, Monotonic: now, BuildWait: watchBuildWait,
	}
	for _, f := range change {
		f(&wp)
	}
	return app.NewBoardWatch(app.NewViews(records, h.boards, &p), &wp)
}

// watched reads item id's board through the watch, and requires it answered.
func watched(t *testing.T, watch *app.BoardWatch, id item.ID) app.BoardRead {
	t.Helper()
	r, err := watch.Board(t.Context(), id)
	require.NoError(t, err, id)
	return r
}

// The facts of a read are those of now alone (R169): a queued item's place
// in the queue on a leader, none on a standby; an item awaiting its close,
// at a time while the switch is off, its auto-close paused while it is on,
// unknown while the tick could not read the table, or the item's delay's
// revision cannot be read; and the read's time. Not staffed, closed and
// staying open are the projection's, whatever the facts. Fails first: a
// build failure.
func TestTheFactsOfAReadAreThoseOfNowAlone(t *testing.T) {
	h := newHarness(t, limit(1))
	awaiting := awaitingItsClose(h, "checkout fails")
	closed := closedAwaiting(h, "search fails")
	reopened := h.open("orders fail")
	_, err := h.c.Close(t.Context(), reopened, closing("close-2"))
	require.NoError(t, err)
	restore := loseCommit(h, commit)
	_, err = h.c.Reopen(t.Context(), reopened, reopening("reopen-1"))
	require.NoError(t, err)
	restore()
	require.Zero(t, h.state(reopened).Closing.AutoClose, "its reopen recorded no delay, as it could read none")
	staysOpen := ingest(h, trigger("f1", "i", "d", item.Firing, h.clock.Now())).Item
	h.round()
	require.Equal(t, item.StatusAwaitingClose, h.state(staysOpen).Status())
	h.open("payments fail")
	h.open("ledger fails")
	queued := h.open("billing fails")
	require.Equal(t, []item.ID{queued}, h.c.Lifecycle().Waiting[1:], "the second of the queue")
	var held atomic.Bool
	held.Store(true)
	clock := &watchClock{now: h.clock.Now()}
	watch := harnessWatch(h, h.records, &held, clock.Now)

	leader := watched(t, watch, queued)
	held.Store(false)
	standby := watched(t, watch, queued)
	held.Store(true)

	assert.True(t, leader.Facts.Held)
	assert.Equal(t, 2, leader.Facts.QueuePosition, "its place, while this instance holds the lease")
	assert.False(t, standby.Facts.Held)
	assert.Zero(t, standby.Facts.QueuePosition, "none on a standby")
	assert.Equal(t, clock.Now(), leader.Facts.Now, "the read's time")
	at := watched(t, watch, awaiting)
	assert.Equal(t, h.state(awaiting).AwaitingSince.Add(closesAfter), at.Facts.ClosesAt, "its delay after its entry into awaiting_close")
	assert.False(t, at.Facts.AutoClosePaused || at.Facts.AutoCloseUnread || at.Facts.ClosesAtUnread)
	writeRows(t, h, true)
	clock.Advance(1500 * time.Millisecond)
	paused := watched(t, watch, awaiting)
	assert.True(t, paused.Facts.AutoClosePaused, "the tick's table holds the switch on")
	assert.Zero(t, paused.Facts.ClosesAt)
	h.controls.failReads(true)
	clock.Advance(1500 * time.Millisecond)
	unread := watched(t, watch, awaiting)
	h.controls.failReads(false)
	assert.True(t, unread.Facts.AutoCloseUnread, "the tick could not read the table")
	assert.False(t, unread.Facts.AutoClosePaused)
	assert.Zero(t, unread.Facts.ClosesAt)
	writeRows(t, h, false)
	clock.Advance(1500 * time.Millisecond)
	restore = loseCommit(h, commit)
	unknown := watched(t, watch, reopened)
	restore()
	assert.True(t, unknown.Facts.ClosesAtUnread, "its delay's revision could not be read")
	assert.Zero(t, unknown.Facts.ClosesAt)
	assert.False(t, unknown.Facts.AutoClosePaused || unknown.Facts.AutoCloseUnread)

	_, err = h.c.SetKillSwitch(t.Context(), true, killing(h.controls.rows(t)))
	require.NoError(t, err)
	require.Equal(t, item.StatusAwaitingClose, h.state(queued).Status(), "the on left it not staffed")
	for _, on := range []bool{false, true} {
		writeRows(t, h, on)
		clock.Advance(1500 * time.Millisecond)
		for _, leads := range []bool{true, false} {
			held.Store(leads)
			assert.True(t, watched(t, watch, staysOpen).Board.Header.StaysOpen, "the switch on %v, leading %v", on, leads)
			require.NotNil(t, watched(t, watch, closed).Board.View.Closed, "the switch on %v, leading %v", on, leads)
			assert.Equal(t, item.CauseKillSwitch, watched(t, watch, queued).Board.View.NotStaffed, "the switch on %v, leading %v", on, leads)
		}
	}
}

// The facts say whether this instance holds the lease as the lease answers,
// whatever the watch's other ports (re-review 1's N2): a watch with no
// snapshot of the coordinator's reads Held on a leader, and a queued item's
// place, which the snapshot alone gives, is none. (The plan's.)
func TestTheFactsHoldTheLeasesAnswerWithNoSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.watch = app.NewBoardWatch(w.views, &app.WatchPorts{
			Probe: w.probe, Controls: w.switches, Held: w.held.Load, Now: time.Now, BuildWait: watchBuildWait,
		})
		w.open("item-q", "inc-q")
		w.conclude("item-i", "inc-i", "incident", checkoutRelease, true)

		queued, investigating := w.read("item-q"), w.read("item-i")

		require.Equal(t, item.StatusQueued, item.Status(queued.Board.View.State))
		assert.True(t, queued.Facts.Held, "the lease's answer")
		assert.Zero(t, queued.Facts.QueuePosition, "no snapshot, no place")
		assert.True(t, investigating.Facts.Held)
		w.held.Store(false)
		time.Sleep(1500 * time.Millisecond)
		assert.False(t, w.read("item-i").Facts.Held, "a standby's")
	})
}

// A projection whose service marks needed the revision its item locked, and
// could not read it, reads "not compared" and is served from the cache while
// the revision stays unread; once the revision answers, the next read builds
// it again, once, and its marks compare (R175; Task 2's review I1). A read
// whose wait ends while that rebuild runs answers the projection kept
// (review 1's m2). A projection whose marks compare nothing for a reason of
// their own, an item with no routing service, is never built again (the
// plan's).
func TestAProjectionThatMissedItsRevisionIsBuiltAgainOnceTheRevisionAnswers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		routing map[string]string
		before  app.ServiceMark
		after   app.ServiceMark
		builds  int
	}{
		{"a routing service: built again", map[string]string{"service": "checkout"}, app.ServiceUncompared, app.ServiceOutside, 2},
		{"no routing service: never built again", map[string]string{"region": "eu-1"}, app.ServiceUncompared, app.ServiceUncompared, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			deploysRevision(h)
			id, err := h.c.Open(t.Context(), &app.OpenInput{Actor: administrator, Type: "incident", Title: "checkout fails", Source: "person", Routing: tc.routing})
			require.NoError(t, err)
			run := h.state(id).Run.ID
			hyp := postHypothesis(h, run, release, checkoutRelease, postEvidence(h, run, release, "logs/search", "checkout v42 deployed at 10:02", "", "").ID)
			propose(h, run, release, hyp, "deploys.rollback", on("payments"), map[string]string{"service": "payments"})
			restore := loseCommit(h, deploysCommit)
			loads := &countedLoads{Records: h.records, made: map[item.ID]int{}}
			var held atomic.Bool
			watch := harnessWatch(h, loads, &held, h.clock.Now)
			mark := func() app.ServiceMark {
				t.Helper()
				card := cardOf(t, watched(t, watch, id).Board, hyp)
				require.Len(t, card.Suggestions, 1)
				return card.Suggestions[0].Suggestions[0].Service
			}

			assert.Equal(t, tc.before, mark())
			assert.Equal(t, tc.before, mark(), "served from the cache while the revision stays unread")
			assert.Equal(t, 1, loads.of(id), "one build while the revision is unread")
			restore()
			hold := make(chan struct{})
			loads.set(func(l *countedLoads) { l.hold = hold })
			short, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			kept, err := watch.Board(short, id)
			cancel()
			close(hold)
			loads.set(func(l *countedLoads) { l.hold = nil })
			require.NoError(t, err, "a read whose wait ends while the rebuild runs")
			assert.Equal(t, tc.before, cardOf(t, kept.Board, hyp).Suggestions[0].Suggestions[0].Service, "answers the projection kept")
			assert.Equal(t, tc.after, mark(), "the next read, once the revision answers")
			assert.Equal(t, tc.after, mark())
			assert.Equal(t, tc.builds, loads.of(id))
		})
	}
}

// A rebuild of R175's that fails for good, by a panic or on records that are
// invalid, leaves the projection kept in use, its marks not compared, and
// builds it no more for its version (re-review 2's N3): the read that
// rebuilds and each read after it answer it, the rebuild's failure logged,
// and the failure of a later version's rebuild within the minute not logged
// again (re-review 2's N5). (The plan's.)
func TestARevisionsRebuildThatFailsForGoodLeavesTheProjectionKept(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fail   func(l *countedLoads)
		logged string
	}{
		{"a panic", func(l *countedLoads) { l.panics = func(item.ID) bool { return true } }, "a flight of the board watch panicked"},
		{"records invalid", func(l *countedLoads) {
			l.fail = func(item.ID) error { return fmt.Errorf("a record of the build: %w", item.ErrInvalid) }
		}, "a rebuild of a board's projection failed for good; the projection kept is served"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			deploysRevision(h)
			id, err := h.c.Open(t.Context(), &app.OpenInput{Actor: administrator, Type: "incident", Title: "checkout fails", Source: "person", Routing: map[string]string{"service": "checkout"}})
			require.NoError(t, err)
			run := h.state(id).Run.ID
			hyp := postHypothesis(h, run, release, checkoutRelease, postEvidence(h, run, release, "logs/search", "checkout v42 deployed at 10:02", "", "").ID)
			propose(h, run, release, hyp, "deploys.rollback", on("payments"), map[string]string{"service": "payments"})
			restore := loseCommit(h, deploysCommit)
			loads := &countedLoads{Records: h.records, made: map[item.ID]int{}}
			var held atomic.Bool
			logs := &syncBuffer{}
			clock := &watchClock{now: h.clock.Now()}
			watch := harnessWatch(h, loads, &held, clock.Now, func(p *app.WatchPorts) { p.Log = slog.New(slog.NewJSONHandler(logs, nil)) })
			mark := func() app.ServiceMark {
				t.Helper()
				card := cardOf(t, watched(t, watch, id).Board, hyp)
				require.Len(t, card.Suggestions, 1)
				return card.Suggestions[0].Suggestions[0].Service
			}
			require.Equal(t, app.ServiceUncompared, mark())
			restore()
			loads.set(tc.fail)

			rebuilding := mark()
			loads.set(func(l *countedLoads) { l.panics, l.fail = nil, nil })

			assert.Equal(t, app.ServiceUncompared, rebuilding, "the read that rebuilds answers the projection kept")
			assert.Equal(t, app.ServiceUncompared, mark(), "and so does the next")
			assert.Equal(t, app.ServiceUncompared, mark())
			assert.Equal(t, 2, loads.of(id), "no build more for its version")
			assert.Equal(t, 1, strings.Count(logs.String(), tc.logged), "the failure is logged")
			restore = loseCommit(h, deploysCommit)
			postEvidence(h, run, release, "logs/search", "checkout v43 deployed at 10:04", "", "")
			clock.Advance(1500 * time.Millisecond)
			require.Equal(t, app.ServiceUncompared, mark(), "a version more, its revision missed")
			restore()
			loads.set(tc.fail)
			require.Equal(t, app.ServiceUncompared, mark(), "its rebuild fails for good again")
			loads.set(func(l *countedLoads) { l.panics, l.fail = nil, nil })
			assert.Equal(t, 4, loads.of(id))
			assert.Equal(t, 1, strings.Count(logs.String(), tc.logged), "within the minute, not logged again")
			assert.NotContains(t, logs.String(), panicTag)
		})
	}
}
