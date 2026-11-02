package app_test

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// closesAfter is the auto-close delay of the harness's incident type, which
// sets none: spec Section 15's default.
const closesAfter = 30 * time.Minute

// advanceTo moves the harness's clock on to t, firing each timer as its time
// comes.
func advanceTo(h *harness, t time.Time) {
	h.t.Helper()
	h.clock.Advance(t.Sub(h.clock.Now()))
}

// lastRecord returns an item's last record.
func lastRecord(h *harness, id item.ID) item.Record {
	h.t.Helper()
	records, err := h.records.Load(h.t.Context(), id)
	require.NoError(h.t, err)
	require.NotEmpty(h.t, records)
	return records[len(records)-1]
}

// lastClose returns the close an item's last record holds, the zero close
// when it holds none, and the record's time.
func lastClose(h *harness, id item.ID) (item.Closed, time.Time) {
	h.t.Helper()
	r := lastRecord(h, id)
	if r.Body.Closed == nil {
		return item.Closed{}, r.At
	}
	return *r.Body.Closed, r.At
}

// awaitingItsClose opens an incident and plays its round, which stops its run
// quiet: the item awaits its close.
func awaitingItsClose(h *harness, title string) item.ID {
	h.t.Helper()
	id := h.open(title)
	h.round()
	require.Equal(h.t, item.StatusAwaitingClose, h.state(id).Status())
	return id
}

// An item awaiting its close closes by itself once its delay has passed since
// its latest entry into awaiting_close (spec Section 4.1; Decision 8): its run
// stops quiet at T, and its item_awaiting_close records its type's delay, 30
// minutes; at T plus 30 minutes less a second nothing is recorded; at T plus
// 30 minutes it closes, automatic, due then, by no one and for no request
// (R7).
func TestAnItemAwaitingItsCloseClosesByItselfAfterItsDelay(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	s := h.state(id)
	at, kinds := s.AwaitingSince, h.kinds(id)

	advanceTo(h, at.Add(closesAfter-time.Second))
	before := h.kinds(id)
	h.clock.Advance(time.Second)

	assert.Equal(t, item.StopQuiet, s.Closing.Reason)
	assert.Equal(t, closesAfter, s.Closing.AutoClose, "its item_awaiting_close records the delay")
	assert.Equal(t, kinds, before, "nothing is recorded before the delay has passed")
	closed, recorded := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: at.Add(closesAfter)}, closed, "automatic, due at T plus 30 minutes, by no one, for no request")
	assert.Equal(t, at.Add(closesAfter), recorded)
	assert.Equal(t, item.StatusClosed, h.state(id).Status())
}

// An item's delay is the one its item_awaiting_close recorded (Decision 8), so
// that a change of its type reaches only the items that enter awaiting_close
// after it: here the item awaits its close with 30 minutes recorded, then the
// registry's current revision sets its type's autoClose.after to 10 minutes,
// and a takeover arms its auto-close again, from its records. It closes 30
// minutes after its entry, not 10. The revision the item locked cannot be
// read by then, so that the delay can come from the records alone.
func TestTheDelayIsTheOneRecordedWhenTheItemAwaitedItsClose(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	s := h.state(id)
	require.Equal(t, closesAfter, s.Closing.AutoClose, "30 minutes recorded")
	h.revs.set(h.revision(strings.Repeat("b", 40), func(files map[string][]byte) {
		files["work-item-types/incident.yaml"] = append(files["work-item-types/incident.yaml"], "  autoClose: {after: 10m}\n"...)
	}))
	loseCommit(h, commit)
	h.crash()
	require.NoError(t, h.boot())
	kinds := h.kinds(id)

	advanceTo(h, s.AwaitingSince.Add(10*time.Minute))
	assert.Equal(t, kinds, h.kinds(id), "not at the current revision's 10 minutes")
	advanceTo(h, s.AwaitingSince.Add(closesAfter))

	closed, _ := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: s.AwaitingSince.Add(closesAfter)}, closed, "at the 30 minutes recorded")
}

// loseCommit has the registry fail to read the revision of commit, as when
// the commit is missing from the repository, until restore is called.
func loseCommit(h *harness, commit string) (restore func()) {
	h.revs.mu.Lock()
	defer h.revs.mu.Unlock()
	rev := h.revs.all[commit]
	delete(h.revs.all, commit)
	return func() {
		h.revs.mu.Lock()
		defer h.revs.mu.Unlock()
		h.revs.all[commit] = rev
	}
}

// A delay that cannot be read is tried again after the retry wait (Decision
// 8): here the item's item_awaiting_close records none, as a build before
// 7b-1 wrote it, and the revision the item locked cannot be read, as when its
// commit is missing from the repository. No timer of its auto-close is armed:
// each read that fails is logged as a step that failed, and the item's retry
// is armed, every retry wait; once the revision reads, the retry arms the
// item's timer at its deadline, which closes it.
func TestADelayThatCannotBeReadIsTriedAgain(t *testing.T) {
	steps := &stepErrors{}
	h := newHarness(t, func(o *app.Options) { o.Log = slog.New(steps) })
	id := h.open("checkout fails")
	// The run stops, and the item's finish fails to record its
	// item_awaiting_close, which the test then appends as such a build wrote
	// it.
	h.records.set(func(int) error {
		if lastKind(h, id) == item.KindRunStopped {
			return errBlinked
		}
		return nil
	})
	h.round()
	require.Equal(t, item.KindRunStopped, lastKind(h, id))
	h.records.set(nil)
	appendRaw(t, h, id, item.Body{V: item.Version, AwaitingClose: &item.AwaitingClose{Reason: item.StopQuiet}})
	restore := loseCommit(h, commit)
	failed := len(steps.kept())

	h.clock.Advance(h.opts.Retry)
	assert.Equal(t, []time.Time{h.clock.Now().Add(h.opts.Retry)}, h.clock.armed(), "no timer of its auto-close: its retry alone")
	h.clock.Advance(h.opts.Retry)
	assert.Equal(t, []time.Time{h.clock.Now().Add(h.opts.Retry)}, h.clock.armed(), "tried again after the retry wait")
	assert.Len(t, steps.kept(), failed+2, "each read that failed, logged as a step that failed")
	restore()
	h.clock.Advance(h.opts.Retry)

	due := h.state(id).AwaitingSince.Add(closesAfter)
	assert.Equal(t, []time.Time{due}, h.clock.armed(), "once the revision reads, its timer at its deadline")
	advanceTo(h, due)
	closed, _ := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: due}, closed, "it closes at its deadline")
}

// A takeover carries on an item awaiting its close (Decision 12): Active lists
// it, and the takeover advances it, which arms its auto-close, the fence
// having cancelled the timer the last holding armed.
func TestATakeoverCarriesOnAnItemAwaitingItsClose(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	due := h.state(id).AwaitingSince.Add(closesAfter)
	h.crash()
	require.Empty(t, h.clock.armed(), "the fence cancelled the timers")

	require.NoError(t, h.boot())

	assert.Equal(t, []item.ID{id}, activeItems(h))
	assert.Equal(t, []time.Time{due}, h.clock.armed(), "its auto-close is armed")
}

// An auto-close timer that fires before its deadline on the coordinator's
// clock, as after a correction of that clock back since its arming, records
// nothing and arms itself again at the deadline (R41), which closes the item:
// here the harness fires it while its clock reads a minute before the
// deadline. (The plan's.)
func TestAnAutoCloseTimerThatFiresBeforeItsDeadlineArmsAgain(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	due := h.state(id).AwaitingSince.Add(closesAfter)
	advanceTo(h, due.Add(-time.Minute))
	kinds := h.kinds(id)

	fired := h.clock.fireEarly(time.Minute)

	assert.Equal(t, 1, fired, "the item's timer fired a minute early")
	assert.Equal(t, kinds, h.kinds(id), "nothing is recorded")
	assert.Equal(t, []time.Time{due}, h.clock.armed(), "a timer of the item is armed at the deadline")
	h.clock.Advance(time.Minute)
	closed, recorded := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: due}, closed, "the timer armed again closes the item")
	assert.Equal(t, due, recorded)
}

// An automatic close that the records store refuses as not due, as it would
// stamp it before its due on the database's time, is tried again after the
// retry wait, when it lands, once (R41). The refusal is an expected race
// while the instance's clock runs ahead of the database's: it is logged once,
// at Info, with the item and the refusal, and not as a step that failed, at
// Error. (The plan's.)
func TestAnAutoCloseTheRecordsRefuseAsNotDueIsTriedAgain(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	id := awaitingItsClose(h, "checkout fails")
	due := h.state(id).AwaitingSince.Add(closesAfter)
	appends := len(h.records.writes.noted())
	h.records.set(func(n int) error {
		if n == appends+1 {
			return fmt.Errorf("insert record: %w", item.ErrNotDue)
		}
		return nil
	})
	kinds := h.kinds(id)

	advanceTo(h, due)

	assert.Equal(t, kinds, h.kinds(id), "the store refused the close")
	early := logged(t, logs.String(), "an automatic close came before its due on the database's time, and is tried again")
	require.Len(t, early, 1, "logged once")
	assert.Equal(t, "INFO", early[0]["level"])
	assert.Equal(t, string(id), early[0]["item"])
	assert.Contains(t, early[0]["err"], item.ErrNotDue.Error())
	assert.Empty(t, errorLines(t, logs.String()), "nothing at Error")
	h.clock.Advance(h.opts.Retry - time.Second)
	assert.Equal(t, kinds, h.kinds(id), "not tried again before the retry wait")
	h.clock.Advance(time.Second)
	closedKinds := append(slices.Clone(kinds), item.KindClosed)
	assert.Equal(t, closedKinds, h.kinds(id), "tried again, it lands")
	closed, recorded := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: due}, closed)
	assert.Equal(t, due.Add(h.opts.Retry), recorded)
	h.clock.Advance(time.Hour)
	assert.Equal(t, closedKinds, h.kinds(id), "once")
}

// heldClock is a coordinator's clock that holds a reading while one is set,
// as clock.DB holds its last reading after a correction back until the
// process's time catches up; otherwise it reads the harness's clock.
type heldClock struct {
	c    *clock
	mu   sync.Mutex
	held time.Time
}

func (h *heldClock) Now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.held.IsZero() {
		return h.held
	}
	return h.c.Now()
}

func (*heldClock) Local(t time.Time) time.Time { return t }

// hold has the clock hold t, or, zero, read the harness's clock again.
func (h *heldClock) hold(t time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.held = t
}

// waitingTimers are timers on the harness's clock that wait, as clock.Timers
// does, the span from the coordinator's clock at their arming to their time.
type waitingTimers struct {
	c     *clock
	clock app.Clock
}

func (w waitingTimers) At(t time.Time, fn func()) func() {
	return w.c.At(w.c.Now().Add(t.Sub(w.clock.Now())), fn)
}

// An auto-close timer that fires before its deadline while the coordinator's
// clock holds a reading, as clock.DB holds its last one after a correction
// back until the process's time catches up, arms itself again no sooner than
// the retry wait (R41). Armed again at the deadline, it would wait the span
// from the held reading to the deadline, fire before the deadline again, and
// so on, loading the item each time, for as long as the clock holds. Here the
// clock holds a millisecond before the item's deadline while a second passes:
// the timer fires once in that second; once the clock moves on, it closes the
// item at its deadline. Found by the interleaving test (Task 17), whose
// schedule 20261240 replays it, with two items' timers firing again 2 µs
// apart. Fails first: the timer fires every millisecond.
func TestAnAutoCloseTimerThatFiresWhileTheClockHoldsArmsNoSoonerThanTheRetryWait(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	due := h.state(id).AwaitingSince.Add(closesAfter)
	advanceTo(h, due.Add(-time.Minute))
	held := &heldClock{c: h.clock}
	held.hold(due.Add(-time.Millisecond))
	ports := h.ports()
	ports.Clock, ports.Timers = held, waitingTimers{c: h.clock, clock: held}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
	kinds := h.kinds(id)
	var loads atomic.Int32
	h.records.loads(func(records []item.Record) error {
		if len(records) > 0 && records[0].Item == id {
			loads.Add(1)
		}
		return nil
	})

	h.clock.Advance(time.Second)

	assert.Equal(t, int32(1), loads.Load(), "the timer fired once in the second, and armed itself again no sooner than the retry wait")
	h.records.loads(nil)
	assert.Equal(t, kinds, h.kinds(id), "nothing is recorded before the deadline")
	held.hold(time.Time{})
	advanceTo(h, due.Add(h.opts.Retry))
	closed, recorded := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: due}, closed, "it closes once the clock moves on")
	assert.Equal(t, due, recorded, "at its deadline")
}

// lockOnceFails is the registry, whose first lock of a revision fails, as
// when the deployment's runtime images cannot be read.
type lockOnceFails struct {
	*revisions
	failed atomic.Bool
}

func (l *lockOnceFails) Lock(rev *config.Revision) (config.Lock, error) {
	if l.failed.CompareAndSwap(false, true) {
		return config.Lock{}, errors.New("the runtime images cannot be read")
	}
	return l.revisions.Lock(rev)
}

// overRevisions has the harness's instance take over again, under the same
// epoch, with revs as its registry.
func overRevisions(t *testing.T, h *harness, revs app.Revisions) {
	t.Helper()
	ports := h.ports()
	ports.Revisions = revs
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
}

// defaultDelayTaken is what the coordinator logs when an item's auto-close
// takes the default delay, as the revision it reads holds no type of the
// item's (R43).
const defaultDelayTaken = "auto-close takes the default delay: the revision holds no type of the item's"

// An item that never started, whose type the current revision no longer
// holds, reads its delay from no revision: its auto-close takes the default of
// spec Section 15, 30 minutes, logged once, instead of trying its arming again
// every retry wait (R43). Here the item's first start fails, as the registry
// fails to lock the revision once, and the registry's next current revision
// drops the item's type: the retried start records not_staffed, for the cause
// type, with no delay, as no revision gives one. The item's timer is armed at
// that entry plus 30 minutes, and closes it then. (The plan's.)
func TestAnItemWhoseTypeIsGoneClosesByItselfAfterTheDefaultDelay(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	overRevisions(t, h, &lockOnceFails{revisions: h.revs})
	dropped := h.revision(strings.Repeat("b", 40), func(files map[string][]byte) {
		delete(files, "work-item-types/incident.yaml")
		files["agents/release-investigator.yaml"] = agent(release.Name, "")
		files["agents/runtime-investigator.yaml"] = agent(runtime.Name, "")
	})
	// The opening and the first start read the harness's revision, and the
	// retried start the one that drops the item's type.
	h.revs.next(h.revs.Current(), dropped)
	id := h.open("checkout fails")
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(id), "its first start failed")

	h.clock.Advance(h.opts.Retry)

	s := h.state(id)
	require.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(id))
	assert.Equal(t, item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseType}, *s.Closing, "with no delay, as no revision gives one")
	due := s.AwaitingSince.Add(closesAfter)
	assert.Equal(t, []time.Time{due}, h.clock.armed(), "its timer, at the default delay")
	h.clock.Advance(3 * h.opts.Retry)
	assert.Equal(t, []time.Time{due}, h.clock.armed(), "no arming is tried again every retry wait")
	advanceTo(h, due.Add(-time.Second))
	assert.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
	h.clock.Advance(time.Second)
	closed, _ := lastClose(h, id)
	assert.Equal(t, item.Closed{Auto: true, Due: due}, closed, "it closes after the default delay")
	assert.Len(t, logged(t, logs.String(), defaultDelayTaken), 1, "the fallback, logged once")
}
