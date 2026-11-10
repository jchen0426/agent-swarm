package app_test

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
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
// calls counted, with the most that ran at once and each call's limit; fail,
// when set, fails each call, and hold, when set, holds each until it closes
// or the call's context ends.
type countedSiblings struct {
	list        app.Siblings
	mu          sync.Mutex
	calls, busy int
	most        int
	limits      []int
	fail        error
	hold        chan struct{}
}

func (s *countedSiblings) Siblings(ctx context.Context, incident item.IncidentID, except item.ID, limit int) ([]item.ID, int, error) {
	s.mu.Lock()
	s.calls++
	s.limits = append(s.limits, limit)
	s.busy++
	s.most = max(s.most, s.busy)
	fail, hold := s.fail, s.hold
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy--
		s.mu.Unlock()
	}()
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
				reads[i], err = w.watch.Board(t.Context(), id)
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
// and is kept so for 5 s, with no second listing, then built again (R172); a
// good strip is kept 30 s; and a strip unread for 30 s is dropped. Fails
// first: a build failure.
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
		assert.Equal(t, []item.ID{"item-s"}, stripIDs(w.read("item-x").Strip.Items), "built again past 5 s")
		time.Sleep(29 * time.Second)
		w.read("item-x")
		calls, _ = w.siblings.counts()
		assert.Equal(t, 2, calls, "a good strip is kept 30 s")
		time.Sleep(2 * time.Second)
		w.read("item-x")
		calls, _ = w.siblings.counts()
		assert.Equal(t, 3, calls, "and built again past them")

		w.open("item-y", "inc-2")
		time.Sleep(31 * time.Second)
		w.read("item-y")
		assert.Equal(t, []item.ID{"item-y"}, w.watch.StripsForTest(), "item-x's strip, unread for 30 s, is dropped")
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
		w.loads.set(func(l *countedLoads) {
			l.slow = func(id item.ID) time.Duration {
				if id == "item-slow" {
					return 3 * time.Second
				}
				return 0
			}
		})
		began := time.Now()

		r := w.read("item-x")

		assert.Equal(t, app.Strip{Unread: true}, r.Strip, "a strip past its bound")
		assert.Equal(t, 2*time.Second, time.Since(began), "the build's bound")
		assert.Equal(t, int64(1), r.Board.Version.Record.Seq, "the rest of the board is read")
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
