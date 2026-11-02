package app_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// startedKinds are the records of an item that started: its opening, its
// start, its run's creation and its first round's opening.
var startedKinds = []item.Kind{item.KindOpened, item.KindStarted, item.KindRunCreated, item.KindRoundOpened}

// limit has the harness's coordinator let n items investigate at once.
func limit(n int) func(*app.Options) {
	return func(o *app.Options) { o.Limit = n }
}

// openLater opens an incident a second after the clock's time, so that its
// opening's time ranks it after every item opened before it (Decision 4).
func openLater(h *harness, title string) item.ID {
	h.t.Helper()
	h.clock.Advance(time.Second)
	return h.open(title)
}

// queued opens an incident by key under each id, a second apart, so that
// the queue orders them as given, by their openings' times, whatever their
// ids' order (Decision 4), and returns the ids.
func queued(h *harness, ids ...item.ID) []item.ID {
	h.t.Helper()
	for _, id := range ids {
		h.clock.Advance(time.Second)
		_, err := h.c.Open(h.t.Context(), byKey(id, "sha256:"+string(id), "checkout fails at "+string(id)))
		require.NoError(h.t, err)
	}
	return ids
}

// stopQuiet has an item's round end quiet: its agents end their turns, the
// turns of other items waiting, and once the round's write calls had their
// grace, its run stops quiet and the item awaits its close.
func stopQuiet(h *harness, id item.ID) {
	h.t.Helper()
	s := h.state(id)
	require.NotNil(h.t, s.Run, "%s investigates", id)
	run := s.Run.ID
	h.turns.mu.Lock()
	var mine, others []app.Turn
	for i := range h.turns.queued {
		if h.turns.queued[i].Run == run {
			mine = append(mine, h.turns.queued[i])
		} else {
			others = append(others, h.turns.queued[i])
		}
	}
	h.turns.queued = mine
	h.turns.mu.Unlock()
	h.play()
	h.turns.mu.Lock()
	h.turns.queued = append(others, h.turns.queued...)
	h.turns.mu.Unlock()
	h.clock.Advance(h.opts.Grace)
	stopped := h.state(id).Run.Stopped
	require.NotNil(h.t, stopped, "%s's run stops", id)
	require.Equal(h.t, item.StopQuiet, stopped.Reason)
}

// startsInOrder stops first, then each item of queue but the last once it
// started, each quiet: as each stops, the next item of queue starts, and
// those after it wait, in their order.
func startsInOrder(h *harness, first item.ID, queue []item.ID) {
	h.t.Helper()
	stopping := first
	for i, next := range queue {
		stopQuiet(h, stopping)
		assert.Equal(h.t, startedKinds, h.kinds(next), "%s starts once %s stops", next, stopping)
		var waiting []item.ID
		waiting = append(waiting, queue[i+1:]...)
		assert.Equal(h.t, waiting, h.c.Lifecycle().Waiting, "once %s started", next)
		stopping = next
	}
}

// stopReason is why the item's run stopped, or empty while the item has no
// run, or its run goes on.
func stopReason(h *harness, id item.ID) item.StopReason {
	h.t.Helper()
	if s := h.state(id); s.Run != nil && s.Run.Stopped != nil {
		return s.Run.Stopped.Reason
	}
	return ""
}

// startOf reports whether drafts hold an item's start.
func startOf(drafts []item.Draft) bool {
	for i := range drafts {
		if drafts[i].Body.Started != nil {
			return true
		}
	}
	return false
}

// errStartRefused is what a records store answers that refuses an item's
// start.
var errStartRefused = errors.New("the records store refuses the start")

// An item opened while the items investigating are at the limit waits queued
// (spec Section 5.1; Decision 4): with a limit of 1, the first item opened
// starts, and the second records its opening alone. The coordinator's
// snapshot gives the limit, the one item investigating, and the second
// waiting.
func TestAnItemOverTheLimitWaitsQueued(t *testing.T) {
	h := newHarness(t, limit(1))
	first := h.open("checkout fails")

	second := h.open("payments fail")

	assert.Equal(t, startedKinds, h.kinds(first))
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(second))
	assert.Equal(t, item.StatusQueued, h.state(second).Status())
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1, Waiting: []item.ID{second}}, h.c.Lifecycle())
}

// An opening while a slot is free starts its item in its own call (Decision
// 4): the walk that ends the opening's advance admits it. With one item
// investigating of 3, the opening records the item's start, its run and its
// first round before it returns.
func TestAnOpeningStartsAtOnceWhileASlotIsFree(t *testing.T) {
	h := newHarness(t)
	h.open("checkout fails")

	id := h.open("payments fail")

	assert.Equal(t, startedKinds, h.kinds(id))
}

// Queued items start in the order they opened (Decision 4): with a limit of
// 1, A investigating, and B, C and D queued, in that order of opening, each
// item that stops lets the next start: B, then C, then D. Their ids run the
// other way, so that the order is their openings', not their ids'.
func TestQueuedItemsStartInTheOrderTheyOpened(t *testing.T) {
	h := newHarness(t, limit(1))
	a := h.open("checkout fails")
	queue := queued(h, "item-3", "item-2", "item-1")
	require.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1, Waiting: queue}, h.c.Lifecycle())

	startsInOrder(h, a, queue)
}

// A queued item starts when an item stops investigating (Decision 4): with a
// limit of 1, A's round ends quiet, and the advance that settled it, which
// stops A's run, walks the queue: B starts in it.
func TestAQueuedItemStartsWhenAnItemStopsInvestigating(t *testing.T) {
	h := newHarness(t, limit(1))
	a := h.open("checkout fails")
	b := h.open("payments fail")
	h.play()
	h.clock.Advance(h.opts.Grace - time.Second)
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(b), "B waits while A's round settles")

	h.clock.Advance(time.Second)

	assert.Equal(t, item.StopQuiet, stopReason(h, a))
	assert.Equal(t, startedKinds, h.kinds(b), "B starts in the advance that settled A's round")
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1}, h.c.Lifecycle())
}

// A queued item starts when an investigating item is closed (Decision 6): with
// a limit of 1, A, in round 1, is closed, and the walk that ends the close's
// advance admits B, which starts before the close answers. The close answers
// the items investigating after it, B among them, and the limit.
func TestAQueuedItemStartsWhenAnInvestigatingItemIsClosed(t *testing.T) {
	h := newHarness(t, limit(1))
	a := h.open("checkout fails")
	b := h.open("payments fail")
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(b))

	out, err := h.c.Close(t.Context(), a, closing("close-1"))

	require.NoError(t, err)
	assert.True(t, out.Changed)
	assert.Equal(t, 1, out.Consequences.Investigating, "B, as the close answers")
	assert.Equal(t, 1, out.Consequences.Limit)
	assert.Equal(t, item.StatusClosed, h.state(a).Status())
	assert.Equal(t, startedKinds, h.kinds(b))
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1}, h.c.Lifecycle())
}

// Closing an item frees its slot (Decision 6): with a limit of 1, B, admitted
// once A stopped, keeps failing to start, as the records store refuses its
// item_started, and holds the slot while C waits; closing B frees it, and C
// starts.
func TestClosingFreesASlotForTheQueue(t *testing.T) {
	h := newHarness(t, limit(1))
	a := h.open("checkout fails")
	b := openLater(h, "payments fail")
	c := openLater(h, "orders fail")
	h.records.refuses(func(id item.ID, drafts []item.Draft) error {
		if id == b && startOf(drafts) {
			return errStartRefused
		}
		return nil
	})
	stopQuiet(h, a)
	h.clock.Advance(3 * h.opts.Retry)
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(b), "B's start keeps failing")
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(c))
	require.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1, Waiting: []item.ID{c}}, h.c.Lifecycle(), "B holds the slot")

	_, err := h.c.Close(t.Context(), b, closing("close-1"))

	require.NoError(t, err)
	assert.Equal(t, item.StatusClosed, h.state(b).Status())
	assert.Equal(t, startedKinds, h.kinds(c))
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1}, h.c.Lifecycle())
}

// A queue's head that its start leaves not staffed lets the next item start
// (Decision 4): with a limit of 1, A investigating, and B and C queued, the
// current revision drops B's type. When A stops, the walk admits B, which
// records not_staffed, for the cause type, and loses its mark; the walk then
// admits C, which starts.
func TestAQueueHeadLeftNotStaffedLetsTheNextItemStart(t *testing.T) {
	h := newHarness(t, limit(1))
	a := h.open("checkout fails")
	b, err := h.c.Open(t.Context(), &app.OpenInput{Type: "change", Title: "checkout v43 goes out", Source: "person"})
	require.NoError(t, err)
	c := openLater(h, "payments fail")
	require.Equal(t, []item.ID{b, c}, h.c.Lifecycle().Waiting)
	h.revs.set(h.revision(strings.Repeat("b", 40), func(files map[string][]byte) {
		delete(files, "work-item-types/change.yaml")
	}))

	stopQuiet(h, a)

	assert.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(b))
	assert.Equal(t, &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseType}, h.state(b).Closing)
	assert.Equal(t, startedKinds, h.kinds(c))
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1}, h.c.Lifecycle())
}

// A queued item closed by a close whose answer was lost, and which no one
// tries again, lets the next item start (Decision 4; review 2's trace): with
// a limit of 1, A investigating, and B and C queued, B's close commits, but
// its answer is lost, and the load by which the close looks for its record
// fails too, as the database blinks, so the coordinator still counts B
// queued. When A stops, before B's retry, the walk admits B, whose load finds
// it closed: B loses its mark, and the walk admits C, which starts.
func TestAQueuedItemClosedByALostAnswerNotRetriedStillLetsTheNextStart(t *testing.T) {
	h := newHarness(t, limit(1))
	closesLoseTheirAnswers(t, h, true)
	a := h.open("checkout fails")
	b := openLater(h, "payments fail")
	c := openLater(h, "orders fail")
	h.play()
	h.clock.Advance(time.Second)
	_, err := h.c.Close(t.Context(), b, closing("close-1"))
	require.ErrorIs(t, err, errAnswerLost)
	require.Equal(t, item.StatusClosed, h.state(b).Status(), "though its close landed")
	require.Equal(t, []item.ID{b, c}, h.c.Lifecycle().Waiting, "B still waits, as far as the coordinator knows")

	// A's round settles a second before B's retry would fire.
	h.clock.Advance(h.opts.Grace - time.Second)

	assert.Equal(t, item.StopQuiet, stopReason(h, a))
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindClosed}, h.kinds(b), "B never starts")
	assert.Equal(t, startedKinds, h.kinds(c))
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1}, h.c.Lifecycle())
}

// A queued item closed by an act whose answer was lost leaves the queue at
// once (Decisions 4 and 13): its close's append commits, and its answer is
// lost; the close looks for its record and finds it, and the load that found
// it refreshes the coordinator's account, so the item leaves Waiting before
// the close answers.
func TestAQueuedItemClosedByAnActWhoseAnswerWasLostLeavesTheQueue(t *testing.T) {
	h := newHarness(t, limit(1))
	closesLoseTheirAnswers(t, h, false)
	h.open("checkout fails")
	b := openLater(h, "payments fail")
	c := openLater(h, "orders fail")
	require.Equal(t, []item.ID{b, c}, h.c.Lifecycle().Waiting)

	out, err := h.c.Close(t.Context(), b, closing("close-1"))

	require.NoError(t, err)
	assert.True(t, out.Changed, "as a repeat")
	assert.Equal(t, item.StatusClosed, h.state(b).Status())
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1, Waiting: []item.ID{c}}, h.c.Lifecycle())
}

// The walk advances each queued item it reaches once at most (Decision 4).
// Here A investigates, and B, C and D wait behind it under a limit of 1, when
// the instance stops; the next takes over with a limit of 4, and the records
// store refuses every item's start. The takeover's walk admits B, C and D,
// and tries each start once: each item keeps its mark and its count, and none
// is advanced again. The takeover reads every item and advances it before
// its walk, a queued item waiting then, so each queued item is loaded three
// times in all, the walk's once.
func TestTheWalkAdvancesEachQueuedItemAtMostOnce(t *testing.T) {
	h := newHarness(t, limit(1))
	h.open("checkout fails")
	queue := []item.ID{openLater(h, "payments fail"), openLater(h, "orders fail"), openLater(h, "search fails")}
	h.crash()
	loads, starts := map[item.ID]int{}, map[item.ID]int{}
	h.records.loads(func(records []item.Record) error {
		if len(records) > 0 {
			loads[records[0].Item]++
		}
		return nil
	})
	h.records.refuses(func(id item.ID, drafts []item.Draft) error {
		if !startOf(drafts) {
			return nil
		}
		starts[id]++
		return errStartRefused
	})
	h.opts.Limit = 4

	require.NoError(t, h.boot())

	for _, id := range queue {
		assert.Equal(t, 1, starts[id], "the walk tried %s's start once", id)
		assert.Equal(t, 3, loads[id], "%s: read and advanced by the takeover, then once by its walk", id)
	}
	assert.Equal(t, app.Lifecycle{Limit: 4, Investigating: 4}, h.c.Lifecycle(), "each admitted, and counted")
}

// A queued item whose turn comes at the daily cap is not staffed, as an
// opening at the cap is (spec Section 5.1; Decision 4): with a limit of 1, A
// investigating, and B and C queued, the day's spend reaches the cap. A's
// round ends, and its run is cut; the walk admits B, which records
// not_staffed, for the cause daily_cap, with its type's delay, then C, which
// records the same: the whole queue drains.
func TestAQueuedItemReachingItsTurnAtTheDailyCapIsNotStaffed(t *testing.T) {
	h := newHarness(t, limit(1))
	a := h.open("checkout fails")
	b := openLater(h, "payments fail")
	c := openLater(h, "orders fail")
	h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })

	h.round()

	assert.Equal(t, item.StopCut, stopReason(h, a))
	for _, id := range []item.ID{b, c} {
		assert.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(id))
		assert.Equal(t, &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseDailyCap, AutoClose: closesAfter}, h.state(id).Closing)
	}
	assert.Equal(t, app.Lifecycle{Limit: 1}, h.c.Lifecycle(), "the whole queue drained")
}

// A start checks, in Decision 4's order, the daily cap, then the item's
// routing, then its type, and is not staffed for the cause of the first check
// that fails (the controller's ruling for Task 9). Here an item that an
// earlier build opened, whose routing holds a tab, which openings refuse
// since Decision 11, is of the type change, which the current revision drops.
// The next instance takes it over, and its walk admits it: its start records
// not_staffed for the cause routing, not type; and, once the day's spend is at
// the cap, for the cause daily_cap. (The plan's.)
func TestAStartChecksTheDailyCapThenRoutingThenType(t *testing.T) {
	for _, c := range []struct {
		name  string
		atCap bool
		cause item.Cause
	}{
		{"its routing and its type fail", false, item.CauseRouting},
		{"the cap, its routing and its type fail", true, item.CauseDailyCap},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			id := item.ID("opened-earlier")
			_, err := h.records.Records.Append(t.Context(), id, func([]item.Record) ([]item.Draft, error) {
				return []item.Draft{{ID: h.ids.NewID(), Item: id, Incident: "incident-1", Body: item.Body{V: item.Version, Opened: &item.Opened{
					Type: "change", Title: "checkout v43 goes out", Source: "person", Routing: map[string]string{"service": "checkout\tpayments"},
					Arm: item.ArmVisible, How: item.NoWindow,
				}}}}, nil
			})
			require.NoError(t, err)
			h.revs.set(h.revision(strings.Repeat("b", 40), func(files map[string][]byte) {
				delete(files, "work-item-types/change.yaml")
			}))
			if c.atCap {
				h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })
			}
			h.crash()

			require.NoError(t, h.boot())

			assert.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(id))
			assert.Equal(t, &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: c.cause}, h.state(id).Closing)
		})
	}
}

// A retried opening of a queued item never jumps the queue (Decision 4): with
// a limit of 1, A investigating, and B queued by its key, B's opening repeated
// under its key, as a person retries it, returns B, which stays queued, with
// no item_started.
func TestARetriedOpeningOfAQueuedItemDoesNotJumpTheQueue(t *testing.T) {
	h := newHarness(t, limit(1))
	h.open("checkout fails")
	in := byKey("item-by-key", "sha256:request", "payments fail")
	_, err := h.c.Open(t.Context(), in)
	require.NoError(t, err)
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(in.ID))

	id, err := h.c.Open(t.Context(), in)

	require.NoError(t, err)
	assert.Equal(t, in.ID, id)
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(id), "it stays queued")
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1, Waiting: []item.ID{id}}, h.c.Lifecycle())
}

// A queued item whose start fails keeps its mark and its place (Decision 4):
// with a limit of 1, A investigating, and B and C queued, A stops, and the
// walk admits B, whose start fails once, as the day's spend cannot be read at
// that read. B keeps its count, holding the slot, and C waits; B starts on its
// retry, and C still waits.
func TestAQueuedItemWhoseStartFailedKeepsItsPlace(t *testing.T) {
	h := newHarness(t, limit(1))
	a := h.open("checkout fails")
	b := openLater(h, "payments fail")
	c := openLater(h, "orders fail")
	// A's round's end reads the day's spend first, and B's start next.
	h.spend.dayFails(func(n int) error {
		if n == 2 {
			return errors.New("the day's spend cannot be read")
		}
		return nil
	})

	stopQuiet(h, a)

	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(b), "B's start failed")
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1, Waiting: []item.ID{c}}, h.c.Lifecycle(), "B keeps its mark and its count, and C waits")
	h.clock.Advance(h.opts.Retry)
	assert.Equal(t, startedKinds, h.kinds(b), "B starts on its retry")
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(c))
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1, Waiting: []item.ID{c}}, h.c.Lifecycle())
}

// An item whose records do not fold holds up no other (spec Section 8.2;
// Decision 4): with a limit of 1, A investigating, and B and C queued, a
// record out of the lifecycle's order lands on B, the queue's head. When A
// stops, the walk admits B, whose load fails, as its records do not fold: B
// leaves the account, and the walk admits C, which starts. B's steps fail,
// and are tried again.
func TestAQueuedItemWhoseRecordsDoNotFoldHoldsUpNoOther(t *testing.T) {
	steps := &stepErrors{}
	h := newHarness(t, limit(1), func(o *app.Options) { o.Log = slog.New(steps) })
	a := h.open("checkout fails")
	b := openLater(h, "payments fail")
	c := openLater(h, "orders fail")
	appendRaw(t, h, b, item.Body{V: item.Version, RoundOpened: &item.RoundOpened{Round: 1}})

	stopQuiet(h, a)

	assert.Equal(t, startedKinds, h.kinds(c), "C starts")
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1}, h.c.Lifecycle(), "B is not in the account")
	failed := steps.kept()
	require.NotEmpty(t, failed)
	require.ErrorIs(t, failed[len(failed)-1], item.ErrInvalid, "B's step failed")
	h.clock.Advance(h.opts.Retry)
	again := steps.kept()
	require.Len(t, again, len(failed)+1, "B's step is tried again")
	require.ErrorIs(t, again[len(again)-1], item.ErrInvalid)
}

// A lowered limit starts nothing until the items investigating are below it
// (Decision 4): three items investigate under a limit of 3, and D waits. The
// instance stops, and the next takes over with a limit of 1: D waits while
// two of the three stop, and starts once the third stops.
func TestALoweredLimitStartsNothingUntilBelowIt(t *testing.T) {
	h := newHarness(t)
	investigating := []item.ID{h.open("checkout fails"), h.open("payments fail"), h.open("orders fail")}
	d := openLater(h, "search fails")
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(d))
	h.crash()
	h.opts.Limit = 1
	require.NoError(t, h.boot())
	require.Equal(t, app.Lifecycle{Limit: 1, Investigating: 3, Waiting: []item.ID{d}}, h.c.Lifecycle())

	for i, id := range investigating[:2] {
		stopQuiet(h, id)
		assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(d), "D waits")
		assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 2 - i, Waiting: []item.ID{d}}, h.c.Lifecycle())
	}
	stopQuiet(h, investigating[2])
	assert.Equal(t, startedKinds, h.kinds(d), "D starts once none investigates")
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1}, h.c.Lifecycle())
}

// An admitted item whose start failed once starts on its own retry, with no
// other trigger (Decision 4; review 3, I1): with a limit of 1, X
// investigating, and the queue [Q1, Q2], X stops, and the walk admits Q1,
// whose start fails, as the day's spend cannot be read. Nothing but Q1's
// retry is armed to advance an item, and it starts Q1, though no walk admits
// Q1 again; Q2 waits. A walk that kept no admission past its own advance of
// Q1 would free Q1's slot once its start failed, and start Q2 in its place.
func TestAnAdmittedItemWhoseStartFailedOnceStartsOnItsRetry(t *testing.T) {
	h := newHarness(t, limit(1))
	x := h.open("checkout fails")
	q1 := openLater(h, "payments fail")
	q2 := openLater(h, "orders fail")
	// X's round's end reads the day's spend first, and Q1's start next.
	h.spend.dayFails(func(n int) error {
		if n == 2 {
			return errors.New("the day's spend cannot be read")
		}
		return nil
	})
	stopQuiet(h, x)
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(q1), "its start failed")
	retry := h.clock.Now().Add(h.opts.Retry)
	require.Equal(t, []time.Time{retry, h.state(x).AwaitingSince.Add(closesAfter)}, h.clock.armed(), "Q1's retry, and X's auto-close")

	h.clock.Advance(h.opts.Retry)

	assert.Equal(t, startedKinds, h.kinds(q1))
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(q2))
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1, Waiting: []item.ID{q2}}, h.c.Lifecycle())
}

// A coordinator needs a limit of at least one item investigating at once
// (Decision 4; Contract C16). (The plan's.)
func TestACoordinatorNeedsALimitOfAtLeastOne(t *testing.T) {
	h := newHarness(t)
	for _, n := range []int{0, -1} {
		opts := h.opts
		opts.Limit = n

		_, err := app.NewCoordinator(h.ports(), &opts)

		require.Error(t, err, "a limit of %d", n)
	}
}
