package app_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
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
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// countedSiblings is the records' listing of an incident's other items, its
// calls counted, with the most that ran at once, each call's limit and the
// item it excepted; fail, when set, fails each call; hold, when set, holds
// each until it closes or the call's context ends, and holdOf the calls of
// the incidents it names so; stuck holds the calls of the incidents it names
// until it closes, whatever their contexts, as a store whose read overruns
// its context does.
type countedSiblings struct {
	list        app.Siblings
	mu          sync.Mutex
	calls, busy int
	most        int
	limits      []int
	excepts     []item.ID
	fail        error
	hold        chan struct{}
	holdOf      map[item.IncidentID]chan struct{}
	stuck       map[item.IncidentID]chan struct{}
	panics      bool
}

func (s *countedSiblings) Siblings(ctx context.Context, incident item.IncidentID, except item.ID, limit int) ([]item.ID, int, error) {
	s.mu.Lock()
	s.calls++
	s.limits = append(s.limits, limit)
	s.excepts = append(s.excepts, except)
	s.busy++
	s.most = max(s.most, s.busy)
	fail, hold, panics, stuck := s.fail, s.hold, s.panics, s.stuck[incident]
	if held := s.holdOf[incident]; held != nil {
		hold = held
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy--
		s.mu.Unlock()
	}()
	if panics {
		panic(errors.New(panicTag))
	}
	if stuck != nil {
		<-stuck
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}
	if fail != nil {
		return nil, 0, fail
	}
	return s.list.Siblings(ctx, incident, except, limit)
}

// listed counts the calls that excepted item id: those of id's own strip.
func (s *countedSiblings) listed(id item.ID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, except := range s.excepts {
		if except == id {
			n++
		}
	}
	return n
}

// listings returns the item each call excepted, in order: the boards whose
// strips were listed.
func (s *countedSiblings) listings() []item.ID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.excepts)
}

// counts returns the calls made, and the most that ran at once.
func (s *countedSiblings) counts() (calls, most int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, s.most
}

// set edits the fake under its lock.
func (s *countedSiblings) set(f func(s *countedSiblings)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// countedEntries is the boards' entries read by their ids, each read noted;
// fail, when set, fails the reads of the entries it names.
type countedEntries struct {
	app.EntryReader
	mu    sync.Mutex
	reads []board.EntryID
	fail  map[board.EntryID]error
}

func (e *countedEntries) Entry(ctx context.Context, id board.EntryID) (board.Event, bool, error) {
	e.mu.Lock()
	e.reads = append(e.reads, id)
	err := e.fail[id]
	e.mu.Unlock()
	if err != nil {
		return board.Event{}, false, err
	}
	return e.EntryReader.Entry(ctx, id)
}

// made returns the entries read, in order.
func (e *countedEntries) made() []board.EntryID {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.reads)
}

// conclude writes item id's records, in incident, of work item type typ: its
// opening, its start, its run's creation, and its rounds up to its run's
// stop, concluding on a hypothesis of k. When converged, a round converged on
// it and its wrap-up round followed, and the run stopped converged on it;
// otherwise the run stopped quiet after a round, with it as its conclusion.
// The run's board holds the hypothesis and the evidence it cites. It returns
// the hypothesis's id.
func (w *watchWorld) conclude(id item.ID, incident item.IncidentID, typ string, k key.Key, converged bool) board.EntryID {
	w.t.Helper()
	run, hyp, ev := board.RunID("run-"+string(id)), board.EntryID("hyp-"+string(id)), board.EntryID("ev-"+string(id))
	opened := *incidentOpening.Opened
	opened.Type = typ
	w.add(id, incident, "", item.Body{Opened: &opened})
	w.add(id, incident, "", item.Body{Started: &item.Started{Commit: "c1", Digest: "d1"}})
	fields := make([]key.Field, 0, len(k))
	for _, name := range slices.Sorted(maps.Keys(k)) {
		fields = append(fields, key.Field{Name: name, Values: []string{k[name]}})
	}
	agents := []string{release.Name}
	w.add(id, incident, run, item.Body{RunCreated: &item.RunCreated{
		Class: item.ClassPrimary, Mode: item.ModeVisible,
		Settings: board.Settings{Schema: key.Schema{Fields: fields}, Convergence: board.DefaultConvergence},
		Budget:   item.Budget{WallClock: 15 * time.Minute, Tokens: 1_000_000, Rounds: 6}, RoundLimit: 2 * time.Minute, Agents: agents,
	}})
	now := time.Now()
	w.add(id, incident, run, item.Body{RoundOpened: &item.RoundOpened{Round: 1, OpenedAt: now, Deadline: now.Add(2 * time.Minute), Agents: agents}})
	if converged {
		w.add(id, incident, run, item.Body{RoundEnded: &item.RoundEnded{
			Round: 1, EndedBy: item.EndedByTurns, EndedAt: now, Converged: []board.EntryID{hyp}, Decision: item.Decision{Next: item.NextWrapUp, WrapUp: hyp},
		}})
		w.add(id, incident, run, item.Body{RoundOpened: &item.RoundOpened{Round: 2, WrapUp: hyp, OpenedAt: now, Deadline: now.Add(2 * time.Minute), Agents: agents}})
		w.add(id, incident, run, item.Body{RoundEnded: &item.RoundEnded{
			Round: 2, EndedBy: item.EndedByTurns, EndedAt: now, Converged: []board.EntryID{hyp}, Decision: item.Decision{Next: item.NextStop, Stop: item.StopConverged},
		}})
		w.add(id, incident, run, item.Body{RunStopped: &item.RunStopped{Reason: item.StopConverged, Rounds: 2, Conclusion: hyp}})
	} else {
		w.add(id, incident, run, item.Body{RoundEnded: &item.RoundEnded{
			Round: 1, EndedBy: item.EndedByTurns, EndedAt: now, Quiet: true, Decision: item.Decision{Next: item.NextStop, Stop: item.StopQuiet},
		}})
		w.add(id, incident, run, item.Body{RunStopped: &item.RunStopped{Reason: item.StopQuiet, Rounds: 1, Conclusion: hyp}})
	}
	_, err := w.events.Append(w.t.Context(), run, func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{
			{ID: ev, Run: run, Round: 1, Author: release, Body: board.Body{V: board.Version, Evidence: &board.Evidence{
				Claim: "v42 went out a minute before the errors", Provenance: board.Provenance{Kind: board.FromReadCall, CallID: "call-" + string(id), Quote: "v42", Source: "logs/search"},
			}}},
			{ID: hyp, Run: run, Round: 1, Author: release, Body: board.Body{V: board.Version, Hypothesis: &board.Hypothesis{
				Key: k, Statement: "it is " + k.String(), Evidence: []board.EntryID{ev},
			}}},
		}, nil
	})
	require.NoError(w.t, err)
	return hyp
}

// pagesWait is the wait the pages read a board under, Task 4's boardWait
// (R141): these tests read the strip as the pages do.
const pagesWait = 2 * time.Second

// polled reads item id's board as the pages read it, under pagesWait.
func (w *watchWorld) polled(id item.ID) (app.BoardRead, error) {
	ctx, cancel := context.WithTimeout(w.t.Context(), pagesWait)
	defer cancel()
	return w.watch.Board(ctx, id)
}

// slowLoads has each load of the items named take as long as it says.
func (w *watchWorld) slowLoads(took map[item.ID]time.Duration) {
	w.loads.set(func(l *countedLoads) { l.slow = func(id item.ID) time.Duration { return took[id] } })
}

// byID indexes a strip's items by their ids.
func byID(items []app.StripItem) map[item.ID]app.StripItem {
	out := make(map[item.ID]app.StripItem, len(items))
	for _, it := range items {
		out[it.ID] = it
	}
	return out
}

// stripIDs lists a strip's items' ids, in its order.
func stripIDs(items []app.StripItem) []item.ID {
	out := make([]item.ID, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// sibling is the id of the nth sibling.
func sibling(n int) item.ID { return item.ID(fmt.Sprintf("item-s%02d", n)) }

// The strip folds the items it shows alone (plan review I6): of 25 other
// items, a second apart, one listing gives the ten opened last, newest first,
// whose records alone are loaded, and whose conclusions alone are read; the
// 15 others are "and 15 more". Fails first: a build failure.
func TestTheStripFoldsOnlyTheItemsItShows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		for n := range 25 {
			time.Sleep(time.Second)
			w.conclude(sibling(n), "inc-1", "incident", checkoutRelease, true)
		}

		r := w.read("item-x")

		calls, _ := w.siblings.counts()
		assert.Equal(t, 1, calls, "one listing")
		assert.Equal(t, []int{10}, w.siblings.limits, "of at most the ten shown")
		want := make([]item.ID, 0, 10)
		for n := 24; n >= 15; n-- {
			want = append(want, sibling(n))
		}
		assert.Equal(t, want, stripIDs(r.Strip.Items), "the ten opened last, newest first")
		assert.Equal(t, 15, r.Strip.More)
		assert.False(t, r.Strip.Unread)
		loads := 0
		for n := range 25 {
			loads += w.loads.of(sibling(n))
			if n < 15 {
				assert.Zero(t, w.loads.of(sibling(n)), "%s is not shown, and not loaded", sibling(n))
			}
		}
		assert.Equal(t, 10, loads, "the ten shown, each loaded once")
		assert.LessOrEqual(t, len(w.entries.made()), 10, "at most the ten shown's conclusions")
		for _, it := range r.Strip.Items {
			assert.Equal(t, app.StripItem{ID: it.ID, Type: "incident", State: item.StatusInvestigating, Conclusion: checkoutRelease, Converged: true}, it)
		}
	})
}

// One strip build runs per incident at a time (R172): twenty reads at once
// of a board, while its strip's listing waits, make one build, and the reads
// of the incident's other boards wait for it, each building after it, one at
// a time. Fails first: a build failure.
func TestOneStripBuildRunsPerIncidentAtATime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		for _, id := range []item.ID{"item-x", "item-y", "item-z"} {
			w.open(id, "inc-1")
		}
		w.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		hold := make(chan struct{})
		w.siblings.set(func(s *countedSiblings) { s.hold = hold })
		var wg sync.WaitGroup
		reads := make([]app.BoardRead, 22)
		for i := range reads {
			id := item.ID("item-x")
			switch i {
			case 20:
				id = "item-y"
			case 21:
				id = "item-z"
			}
			wg.Go(func() {
				var err error
				reads[i], err = w.polled(id)
				assert.NoError(t, err)
			})
		}
		synctest.Wait()
		calls, most := w.siblings.counts()
		assert.Equal(t, 1, calls, "one build, its listing held, while every read waits")
		assert.Equal(t, 1, most)

		close(hold)
		wg.Wait()

		calls, most = w.siblings.counts()
		assert.Equal(t, 3, calls, "the twenty reads of one board made one build, and each other board one")
		assert.Equal(t, 1, most, "one at a time")
		others := map[item.ID][]item.ID{
			"item-x": {"item-s", "item-y", "item-z"}, "item-y": {"item-s", "item-x", "item-z"}, "item-z": {"item-s", "item-x", "item-y"},
		}
		for i := range reads {
			if !assert.NotNil(t, reads[i].Board, i) {
				continue
			}
			id := reads[i].Board.View.ID
			assert.Equal(t, others[id], stripIDs(reads[i].Strip.Items), "%s's strip is of its incident's other items", id)
		}
	})
}

// A strip whose listing fails reads unread, the rest of the board as it is,
// and is kept so for 5 s, with no second listing; past them, a read answers
// it as kept and starts its rebuild, whose strip the next read gets (R172;
// review 1's I1); a good strip is kept 30 s; and a strip unread for 30 s is
// dropped. Fails first: a build failure.
func TestAFailedStripIsCachedForFiveSecondsAndAnUnreadOneDropped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		w.siblings.set(func(s *countedSiblings) { s.fail = errBlinked })

		r := w.read("item-x")

		assert.Equal(t, app.Strip{Unread: true}, r.Strip, "the incident's items could not be listed")
		assert.Equal(t, int64(1), r.Board.Version.Record.Seq, "the rest of the board is read")
		time.Sleep(4 * time.Second)
		assert.True(t, w.read("item-x").Strip.Unread)
		calls, _ := w.siblings.counts()
		assert.Equal(t, 1, calls, "no second listing within 5 s")
		w.siblings.set(func(s *countedSiblings) { s.fail = nil })
		time.Sleep(1500 * time.Millisecond)
		assert.True(t, w.read("item-x").Strip.Unread, "answered as kept, past 5 s, while it is built again")
		synctest.Wait()
		assert.Equal(t, []item.ID{"item-s"}, stripIDs(w.read("item-x").Strip.Items), "built again past 5 s")
		time.Sleep(29 * time.Second)
		w.read("item-x")
		synctest.Wait()
		calls, _ = w.siblings.counts()
		assert.Equal(t, 2, calls, "a good strip is kept 30 s")
		time.Sleep(2 * time.Second)
		w.read("item-x")
		synctest.Wait()
		calls, _ = w.siblings.counts()
		assert.Equal(t, 3, calls, "and built again past them")

		w.open("item-y", "inc-2")
		time.Sleep(31 * time.Second)
		w.read("item-y")
		assert.Equal(t, []item.ID{"item-y"}, w.watch.StripsForTest(), "item-x's strip, unread for 30 s, is dropped")
	})
}

// A strip whose builds fail in a row is kept unread 5 s, then 10, then 20,
// and then 30 s at most, each failure in a row doubling the last's, however
// many fail; a good build resets it, so that the next failure is kept 5 s
// again (re-review 2's N2). (The plan's.)
func TestAStripThatFailsInARowIsKeptLongerEachTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		w.siblings.set(func(s *countedSiblings) { s.fail = errBlinked })
		require.True(t, w.read("item-x").Strip.Unread, "the first failure")
		listings := func() int {
			t.Helper()
			w.read("item-x")
			synctest.Wait()
			calls, _ := w.siblings.counts()
			return calls
		}
		keptFor := func(kept time.Duration, then string) {
			t.Helper()
			calls, _ := w.siblings.counts()
			time.Sleep(kept - time.Second)
			assert.Equal(t, calls, listings(), "kept %v", kept)
			time.Sleep(time.Second)
			assert.Equal(t, calls+1, listings(), "built again past %v, %s", kept, then)
		}

		for _, kept := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second} {
			keptFor(kept, "and failed")
		}
		for range 40 {
			keptFor(30*time.Second, "and failed, many times in a row")
		}
		w.siblings.set(func(s *countedSiblings) { s.fail = nil })
		keptFor(30*time.Second, "and read")
		require.Equal(t, []item.ID{"item-s"}, stripIDs(w.read("item-x").Strip.Items))
		w.siblings.set(func(s *countedSiblings) { s.fail = errBlinked })
		keptFor(30*time.Second, "and failed")
		keptFor(5*time.Second, "the good build having reset it")
	})
}

// A strip's build that gets no strip slot within its bound reads nothing
// (re-review 2's N2), while a store's listing of another incident overruns
// its bound and holds the slot: a good strip kept stays as kept, answered at
// once, though its rebuild found no slot; and a board with no strip kept is
// answered its strip unread, with nothing kept. (The plan's.)
func TestAStripBuildThatGetsNoSlotReadsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		w.open("item-y", "inc-2")
		w.open("item-z", "inc-z")
		w.read("item-x")
		for range 5 {
			time.Sleep(5 * time.Second)
			w.read("item-x")
		}
		time.Sleep(5 * time.Second)
		stuck := make(chan struct{})
		w.siblings.set(func(s *countedSiblings) { s.stuck = map[item.IncidentID]chan struct{}{"inc-z": stuck} })
		var wg sync.WaitGroup
		defer func() {
			close(stuck)
			wg.Wait()
		}()
		wg.Go(func() { _, _ = w.polled("item-z") })
		synctest.Wait()
		r, err := w.polled("item-x")
		require.NoError(t, err)
		require.Equal(t, []item.ID{"item-s"}, stripIDs(r.Strip.Items), "past its life, answered at once while its rebuild waits")
		time.Sleep(2500 * time.Millisecond)

		r, err = w.polled("item-x")
		y := w.read("item-y")

		require.NoError(t, err)
		assert.Equal(t, []item.ID{"item-s"}, stripIDs(r.Strip.Items), "its rebuild found no slot: the strip stays as kept")
		assert.False(t, r.Strip.Unread, "nothing failed but the wait for the slot")
		assert.Equal(t, app.Strip{Unread: true}, y.Strip, "no strip kept, and its build found no slot")
		assert.NotContains(t, w.watch.StripsForTest(), item.ID("item-y"), "nothing kept of it")
	})
}

// A strip whose build passes its bound reads unread, the rest of the board as
// it is (R172): a sibling whose records take 3 s to load holds the build past
// its 2 s, and the strip is not given with that sibling's entry missing.
// (The plan's.)
func TestAStripPastItsBoundReadsUnread(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		w.conclude("item-slow", "inc-1", "incident", checkoutRelease, true)
		w.slowLoads(map[item.ID]time.Duration{"item-slow": 3 * time.Second})
		began := time.Now()

		r, err := w.polled("item-x")

		require.NoError(t, err)
		assert.Equal(t, app.Strip{Unread: true}, r.Strip, "a strip past its bound")
		assert.Equal(t, 2*time.Second, time.Since(began), "the build's bound")
		assert.Equal(t, int64(1), r.Board.Version.Record.Seq, "the rest of the board is read")
	})
}

// A board whose strip's build outlasts the read is answered, its strip
// unread, with no error (review 1's I1, review 2's I1, sequence A; design
// D7): the build goes on and is kept, and the next read gets it. (The
// plan's.)
func TestAReadWhoseStripOutlastsItIsAnsweredItsStripUnread(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		w.slowLoads(map[item.ID]time.Duration{"item-x": 500 * time.Millisecond, "item-s": 1800 * time.Millisecond})

		r, err := w.polled("item-x")

		require.NoError(t, err, "the strip never fails the read")
		require.NotNil(t, r.Board)
		assert.Equal(t, app.Strip{Unread: true}, r.Strip, "its build has not landed")
		time.Sleep(time.Second)
		again, err := w.polled("item-x")
		require.NoError(t, err)
		assert.Equal(t, []item.ID{"item-s"}, stripIDs(again.Strip.Items), "the build went on, and was kept")
		calls, _ := w.siblings.counts()
		assert.Equal(t, 1, calls, "one build")
	})
}

// The boards of one incident read at once each answer (review 2's I1,
// sequence B; R172): their strips build one at a time, each 0.8 s, and the
// third board's read, past its wait before its strip's build lands, is
// answered with its strip unread. (The plan's.)
func TestTheBoardsOfAnIncidentReadAtOnceEachAnswer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		ids := []item.ID{"item-x", "item-y", "item-z"}
		for _, id := range ids {
			w.open(id, "inc-1")
		}
		w.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		w.slowLoads(map[item.ID]time.Duration{"item-s": 800 * time.Millisecond})
		reads, errs := make([]app.BoardRead, len(ids)), make([]error, len(ids))
		var wg sync.WaitGroup
		for i, id := range ids {
			wg.Go(func() { reads[i], errs[i] = w.polled(id) })
		}
		wg.Wait()

		unread := 0
		for i, id := range ids {
			require.NoError(t, errs[i], id)
			require.NotNil(t, reads[i].Board, id)
			if reads[i].Strip.Unread {
				unread++
			}
		}
		assert.Equal(t, 1, unread, "the board whose strip builds third, past its wait")
		time.Sleep(time.Second)
	})
}

// A strip that expires as the database blips, the probe failing and the
// listing hanging, is answered as kept, the board from the last good tick
// (R137), while its rebuild runs; once the rebuild fails, the strip reads
// unread (review 2's I1, sequence C). (The plan's.)
func TestAStripThatExpiresInABlipIsAnsweredAsKept(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		for range 30 {
			_, err := w.polled("item-x")
			require.NoError(t, err)
			time.Sleep(time.Second)
		}
		hold := make(chan struct{})
		defer close(hold)
		w.probe.set(func(p *countedProbe) { p.fail = errDown })
		w.siblings.set(func(s *countedSiblings) { s.hold = hold })
		began := time.Now()

		r, err := w.polled("item-x")

		require.NoError(t, err, "the last good tick, under 5 s old, serves the board")
		assert.Zero(t, time.Since(began), "at once")
		assert.Equal(t, []item.ID{"item-s"}, stripIDs(r.Strip.Items), "the strip kept, past its 30 s")
		time.Sleep(3 * time.Second)
		r, err = w.polled("item-x")
		require.NoError(t, err)
		assert.Equal(t, app.Strip{Unread: true}, r.Strip, "its rebuild failed")
	})
}

// A polled board whose strip expires is answered the strip kept, at once,
// while its rebuild runs, and the rebuild's strip once it landed (review 1's
// I1; plan review I6: the last good strip is served meanwhile). (The plan's.)
func TestAPolledBoardIsAnsweredItsKeptStripWhileItsRebuildRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		for range 6 {
			_, err := w.polled("item-x")
			require.NoError(t, err)
			time.Sleep(5 * time.Second)
		}
		time.Sleep(time.Second)
		hold := make(chan struct{})
		w.siblings.set(func(s *countedSiblings) { s.hold = hold })
		w.conclude("item-t", "inc-1", "incident", checkoutRelease, true)
		began := time.Now()

		r, err := w.polled("item-x")

		require.NoError(t, err)
		assert.Zero(t, time.Since(began), "the poll waits for no strip")
		assert.Equal(t, []item.ID{"item-s"}, stripIDs(r.Strip.Items), "the strip kept, past its 30 s")
		synctest.Wait()
		calls, _ := w.siblings.counts()
		assert.Equal(t, 2, calls, "its rebuild runs")
		close(hold)
		synctest.Wait()
		r, err = w.polled("item-x")
		require.NoError(t, err)
		assert.Equal(t, []item.ID{"item-t", "item-s"}, stripIDs(r.Strip.Items), "the rebuild's strip")
	})
}

// The strips found past their life while another board's build of the
// incident runs are noted, and rebuilt as that build settles, the first noted
// first (re-review 2's N1): item-y's and item-z's strips, 30 s old, are read
// while item-x's strip build runs, and are rebuilt as soon as it ends, so that
// their next reads show the sibling opened since, rather than starting their
// rebuilds themselves, the polls' phases aligned with item-x's builds. (The
// plan's.)
func TestStripsPastTheirLifeWhileAnotherBuildRunsAreRebuiltAsItSettles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		noted := []item.ID{"item-y", "item-z"}
		for _, id := range append([]item.ID{"item-x"}, noted...) {
			w.open(id, "inc-1")
		}
		w.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		for _, id := range noted {
			w.read(id)
		}
		time.Sleep(25 * time.Second)
		for _, id := range noted {
			w.read(id)
		}
		time.Sleep(4 * time.Second)
		w.conclude("item-t", "inc-1", "incident", checkoutRelease, true)
		time.Sleep(time.Second)
		hold := make(chan struct{})
		w.loads.set(func(l *countedLoads) { l.holds = map[item.ID]chan struct{}{"item-y": hold} })
		var wg sync.WaitGroup
		wg.Go(func() { _, _ = w.polled("item-x") })
		synctest.Wait()
		require.Equal(t, 1, w.siblings.listed("item-x"), "item-x's strip build runs, its load of item-y held")
		for _, id := range noted {
			r, err := w.polled(id)
			require.NoError(t, err)
			require.NotContains(t, stripIDs(r.Strip.Items), item.ID("item-t"), "%s's, past its life, answered as kept", id)
		}
		time.Sleep(time.Second)
		close(hold)
		wg.Wait()
		synctest.Wait()

		for _, id := range noted {
			r, err := w.polled(id)
			require.NoError(t, err)
			assert.Contains(t, stripIDs(r.Strip.Items), item.ID("item-t"), "%s's, rebuilt as item-x's build settled", id)
		}
		listings := w.siblings.listings()
		assert.Equal(t, []item.ID{"item-x", "item-y", "item-z"}, listings[len(listings)-3:], "the first noted first")
	})
}

// A strip noted while another build ran, and dropped, unread for 30 s, by the
// time that build settles, is not built again (re-review 2's N1): item-x's
// strip build, its listing overrunning its bound by half a minute, settles
// once item-y's strip is gone. (The plan's.)
func TestANotedStripDroppedBeforeItsTurnIsNotBuiltAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.open("item-y", "inc-1")
		w.read("item-y")
		time.Sleep(25 * time.Second)
		w.read("item-y")
		time.Sleep(5 * time.Second)
		var wg sync.WaitGroup
		defer wg.Wait()
		stuck := make(chan struct{})
		unstick := sync.OnceFunc(func() { close(stuck) })
		defer unstick()
		w.siblings.set(func(s *countedSiblings) { s.stuck = map[item.IncidentID]chan struct{}{"inc-1": stuck} })
		wg.Go(func() { _, _ = w.polled("item-x") })
		synctest.Wait()
		require.Equal(t, []item.ID{"item-x"}, stripIDs(w.read("item-y").Strip.Items), "past its life while item-x's build runs: answered as kept, and noted")
		time.Sleep(31 * time.Second)

		unstick()
		wg.Wait()
		synctest.Wait()

		assert.Equal(t, []item.ID{"item-y", "item-x"}, w.siblings.listings(), "item-y's strip, dropped, is not built again")
	})
}

// A strip's build that panics is a failed strip (review 2's I2): every read
// waiting for it is answered its board, the strip unread; its flight is
// cleared; the watch logs the panic once, with its stack, never its value;
// and, kept 5 s as a failed strip, it is built again past them. (The
// plan's.)
func TestAStripsBuildThatPanicsIsAFailedStrip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		w.siblings.set(func(s *countedSiblings) { s.panics = true })
		reads, errs := make([]app.BoardRead, 3), make([]error, 3)
		var wg sync.WaitGroup
		for i := range reads {
			wg.Go(func() { reads[i], errs[i] = w.polled("item-x") })
		}
		wg.Wait()

		for i := range reads {
			require.NoError(t, errs[i], "every waiter is told")
			require.NotNil(t, reads[i].Board)
			assert.Equal(t, app.Strip{Unread: true}, reads[i].Strip)
		}
		assert.Zero(t, w.watch.FlightsForTest(), "its flight is cleared")
		w.requirePanicLogged("strip")
		w.siblings.set(func(s *countedSiblings) { s.panics = false })
		time.Sleep(4 * time.Second)
		assert.True(t, w.read("item-x").Strip.Unread, "kept 5 s")
		calls, _ := w.siblings.counts()
		assert.Equal(t, 1, calls)
		time.Sleep(1500 * time.Millisecond)
		w.read("item-x")
		synctest.Wait()
		assert.Equal(t, []item.ID{"item-s"}, stripIDs(w.read("item-x").Strip.Items), "built again past 5 s")
	})
}

// A strip that holds a sibling unreadable for a transient reason, a store's
// failure to load its records, is kept 5 s, as a failed strip, and then
// built again (review 2's M5); one whose sibling is unreadable for good, its
// records invalid, is kept 30 s. (The plan's.)
func TestAStripWithASiblingUnreadForNowIsKeptFiveSeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.conclude("item-s", "inc-1", "incident", checkoutRelease, true)
		var blips atomic.Int32
		blips.Store(1)
		w.loads.set(func(l *countedLoads) {
			l.fail = func(id item.ID) error {
				if id == "item-s" && blips.Add(-1) >= 0 {
					return errBlinked
				}
				return nil
			}
		})
		assert.True(t, byID(w.read("item-x").Strip.Items)["item-s"].Unreadable, "its load failed")
		time.Sleep(5500 * time.Millisecond)
		w.read("item-x")
		synctest.Wait()
		calls, _ := w.siblings.counts()
		assert.Equal(t, 2, calls, "built again past 5 s")
		assert.False(t, byID(w.read("item-x").Strip.Items)["item-s"].Unreadable, "and read")

		c := newWatchWorld(t)
		c.open("item-x", "inc-1")
		c.open("item-corrupt", "inc-1")
		c.add("item-corrupt", "inc-1", "", incidentOpening)
		assert.True(t, byID(c.read("item-x").Strip.Items)["item-corrupt"].Unreadable)
		time.Sleep(5500 * time.Millisecond)
		c.read("item-x")
		synctest.Wait()
		calls, _ = c.siblings.counts()
		assert.Equal(t, 1, calls, "unreadable for good: kept 30 s")
	})
}

// A hidden sibling shows its state alone (design D7, D13; Review Focus 5):
// nothing of its run is read, no conclusion's entry among it; it never
// disagrees, and it makes no other disagree. Fails first: a build failure.
func TestAHiddenSiblingShowsItsStateAloneAndNothingOfItsRunIsRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t, func(p *app.ViewPorts) { p.Visible = func(v *app.View) bool { return v.ID != "item-hidden" } })
		w.conclude("item-x", "inc-1", "incident", checkoutRelease, true)
		time.Sleep(time.Second)
		hidden := w.conclude("item-hidden", "inc-1", "incident", checkoutConfig, true)
		time.Sleep(time.Second)
		w.conclude("item-v", "inc-1", "incident", checkoutRelease, true)

		items := byID(w.read("item-x").Strip.Items)

		assert.Equal(t, app.StripItem{ID: "item-hidden", State: item.StatusInvestigating, Hidden: true}, items["item-hidden"], "its state alone")
		assert.NotContains(t, w.entries.made(), hidden, "nothing of its run is read")
		assert.Equal(t, app.StripItem{ID: "item-v", Type: "incident", State: item.StatusInvestigating, Conclusion: checkoutRelease, Converged: true},
			items["item-v"], "it agrees with this item, and the hidden one makes it disagree with nothing")
	})
}

// Disagreement is judged among the items the strip shows and this item
// (R148): a shown sibling disagrees when its converged conclusion's key
// differs from another's of the same type, this item's own among them; a key
// of another type, a conclusion not converged, and an item past the ten shown
// count for nothing. Fails first: a build failure.
func TestDisagreementIsJudgedAmongTheShownItemsAndThisOne(t *testing.T) {
	type sib struct {
		typ       string
		k         key.Key
		converged bool
	}
	for _, tc := range []struct {
		name string
		// own is this item's conclusion, nil for an item with none.
		own      *sib
		siblings []sib
		// disagree are the siblings, by their place, that disagree.
		disagree []int
	}{
		{name: "two of one type with different keys", siblings: []sib{{"incident", checkoutRelease, true}, {"incident", checkoutConfig, true}}, disagree: []int{0, 1}},
		{name: "two of one type with one key", siblings: []sib{{"incident", checkoutRelease, true}, {"incident", checkoutRelease, true}}},
		{name: "another type's never", siblings: []sib{{"incident", checkoutRelease, true}, {"change", checkoutConfig, true}}},
		{name: "this item's own conclusion counts", own: &sib{"incident", checkoutRelease, true}, siblings: []sib{{"incident", paymentsConfig, true}}, disagree: []int{0}},
		{name: "this item's own conclusion agrees", own: &sib{"incident", checkoutRelease, true}, siblings: []sib{{"incident", checkoutRelease, true}}},
		{name: "a conclusion not converged counts for nothing", own: &sib{"incident", checkoutRelease, true}, siblings: []sib{{"incident", checkoutConfig, false}}},
		{name: "this item's own not converged counts for nothing", own: &sib{"incident", checkoutRelease, false}, siblings: []sib{{"incident", checkoutConfig, true}}},
		{
			name: "an item past the ten counts for nothing",
			siblings: []sib{
				{"incident", checkoutConfig, true}, {"incident", checkoutRelease, true}, {"incident", checkoutRelease, true}, {"incident", checkoutRelease, true},
				{"incident", checkoutRelease, true}, {"incident", checkoutRelease, true}, {"incident", checkoutRelease, true}, {"incident", checkoutRelease, true},
				{"incident", checkoutRelease, true}, {"incident", checkoutRelease, true}, {"incident", checkoutRelease, true},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := newWatchWorld(t)
				if tc.own != nil {
					w.conclude("item-x", "inc-1", tc.own.typ, tc.own.k, tc.own.converged)
				} else {
					w.open("item-x", "inc-1")
				}
				for n, s := range tc.siblings {
					time.Sleep(time.Second)
					w.conclude(sibling(n), "inc-1", s.typ, s.k, s.converged)
				}

				items := byID(w.read("item-x").Strip.Items)

				var disagree []int
				for n := range tc.siblings {
					if it, shown := items[sibling(n)]; shown && it.Disagrees {
						disagree = append(disagree, n)
					}
				}
				assert.Equal(t, tc.disagree, disagree)
			})
		})
	}
}

// A sibling that cannot be read is its own entry, its id alone, and the
// strip and the board are read (design D7): records that do not fold, a
// record of a later build, and a conclusion whose entry cannot be read. Fails
// first: a build failure.
func TestASiblingThatCannotBeReadIsItsOwnEntry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWatchWorld(t)
		w.open("item-x", "inc-1")
		w.open("item-corrupt", "inc-1")
		w.add("item-corrupt", "inc-1", "", incidentOpening)
		_, err := w.records.Append(t.Context(), "item-later", func([]item.Record) ([]item.Draft, error) {
			return []item.Draft{{ID: "item-later-1", Item: "item-later", Incident: "inc-1", Body: item.Body{V: item.Version + 1}}}, nil
		})
		require.NoError(t, err)
		lost := w.conclude("item-lost", "inc-1", "incident", checkoutRelease, true)
		w.entries.fail = map[board.EntryID]error{lost: errBlinked}
		w.conclude("item-good", "inc-1", "incident", checkoutRelease, true)

		r := w.read("item-x")

		assert.False(t, r.Strip.Unread, "the incident's items were listed")
		items := byID(r.Strip.Items)
		for _, id := range []item.ID{"item-corrupt", "item-later", "item-lost"} {
			assert.Equal(t, app.StripItem{ID: id, Unreadable: true}, items[id], id)
		}
		assert.Equal(t, app.StripItem{ID: "item-good", Type: "incident", State: item.StatusInvestigating, Conclusion: checkoutRelease, Converged: true}, items["item-good"])
	})
}
