package app_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// recorded is what RecordKillSwitchOn answered, handed back from the
// goroutine that called it.
type recorded struct {
	out app.KillSwitchOutcome
	err error
}

// recordOn has the drain's on recorded on a goroutine of its own, as the
// admin API's request records it, under epoch, the draining holding's, after
// row 0, and hands back its answer.
func recordOn(t *testing.T, h *harness, epoch int64) <-chan recorded {
	t.Helper()
	answer := make(chan recorded, 1)
	c, act := h.c, killing(0)
	go func() {
		out, err := c.RecordKillSwitchOn(t.Context(), epoch, act)
		answer <- recorded{out: out, err: err}
	}()
	return answer
}

// stopsAllNoted and endsAllNoted are an instance's turns and live sessions,
// which note, in one order with what the test notes beside them, each time
// every turn is stopped and every session ended at once.
type stopsAllNoted struct {
	app.Turns
	order *trace
}

func (s stopsAllNoted) StopAll() {
	s.order.note("stop all")
	s.Turns.StopAll()
}

type endsAllNoted struct {
	app.Sessions
	order *trace
}

func (s endsAllNoted) EndAll() {
	s.order.note("end all")
	s.Sessions.EndAll()
}

// requestKey keys a value a request's context carries, which the write of
// the drain's on keeps.
type requestKey struct{}

// Every act answers ErrNotHeld once the coordinator's context ended, while it
// is still held, as between a drain's first moment, which ends the holding's
// context, and its fence (Decisions 12 and 13): with a limit of 1, one item
// investigates, a second waits queued, and a third is closed. A close, a
// reopen, a promotion, and the kill switch's on and off, and each of their
// previews, answer ErrNotHeld before they read or write anything: no item is
// loaded, the switch is neither read nor written, and nothing is recorded or
// posted. Fails first: the acts load and write under an ended context, and
// answer its error; the kill switch's preview answers the read's error, and
// its on and off try their write.
func TestAnActOnACoordinatorWhoseContextEndedAnswersNotHeld(t *testing.T) {
	h := newHarness(t, limit(1))
	end := endingHolding(t, h)
	running := h.open("checkout fails")
	waiting := openLater(h, "payments fail")
	closed := openLater(h, "orders fail")
	_, err := h.c.Close(t.Context(), closed, closing("close-1"))
	require.NoError(t, err)
	require.Equal(t, []item.ID{waiting}, h.c.Lifecycle().Waiting, "the second waits queued")
	end()
	var loads atomic.Int32
	h.records.loads(func([]item.Record) error {
		loads.Add(1)
		return nil
	})
	t.Cleanup(func() { h.records.loads(nil) })
	outcome := func(_ app.Outcome, err error) error { return err }
	switched := func(_ app.KillSwitchOutcome, err error) error { return err }

	for _, c := range []struct {
		name string
		act  func() error
	}{
		{"a close", func() error { return outcome(h.c.Close(t.Context(), running, closing("close-2"))) }},
		{"a close's preview", func() error { return outcome(h.c.Close(t.Context(), running, previewing())) }},
		{"a reopen", func() error { return outcome(h.c.Reopen(t.Context(), closed, reopening("reopen-1"))) }},
		{"a reopen's preview", func() error { return outcome(h.c.Reopen(t.Context(), closed, previewing())) }},
		{"a promotion", func() error { return outcome(h.c.Promote(t.Context(), waiting, promoting("promote-1"))) }},
		{"a promotion's preview", func() error { return outcome(h.c.Promote(t.Context(), waiting, previewing())) }},
		{"the kill switch's on", func() error { return switched(h.c.SetKillSwitch(t.Context(), true, killing(0))) }},
		{"the kill switch's off", func() error { return switched(h.c.SetKillSwitch(t.Context(), false, turningOff(0))) }},
		{"the kill switch's preview", func() error { return switched(h.c.SetKillSwitch(t.Context(), true, previewing())) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			before, reads, writes := h.carried(), h.controls.readsMade(), len(h.controls.writes.noted())
			loads.Store(0)

			err := c.act()

			require.ErrorIs(t, err, app.ErrNotHeld)
			assert.Zero(t, loads.Load(), "it loads no item")
			assert.Equal(t, reads, h.controls.readsMade(), "it reads no switch")
			assert.Len(t, h.controls.writes.noted(), writes, "it writes no row")
			assert.Equal(t, before, h.carried(), "it records and posts nothing")
		})
	}
	assert.Zero(t, h.controls.rows(t), "the switch has no row")
	assert.False(t, h.c.Lifecycle().KillSwitchUnsettled, "no write's outcome is unknown")
}

// An act whose context ends as it records answers ErrNotHeld, and does not
// load its item again (Decision 13): what landed is found by its key on the
// instance that leads next. A close of an item in round 1 makes its append as
// a drain's first moment ends the holding's context, so the append fails for
// it. The close answers ErrNotHeld, and loads nothing after its append: it
// looks for no key, as it would for a lost answer, and nothing landed. Fails
// first: the close answers the append's error, once it loaded the item again
// to look for its key.
func TestAnActWhoseContextEndsAsItRecordsAnswersNotHeldWithNoSecondLoad(t *testing.T) {
	h := newHarness(t)
	end := endingHolding(t, h)
	id := h.open("checkout fails")
	var ended atomic.Bool
	var loads atomic.Int32
	h.records.loads(func([]item.Record) error {
		if ended.Load() {
			loads.Add(1)
		}
		return nil
	})
	h.records.set(func(int) error {
		end()
		ended.Store(true)
		return nil
	})

	_, err := h.c.Close(t.Context(), id, closing("close-1"))

	h.records.set(nil)
	h.records.loads(nil)
	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Zero(t, loads.Load(), "no load after the append")
	assert.Equal(t, item.StatusInvestigating, h.state(id).Status(), "nothing landed")
}

// A kill switch on at a drain's start is recorded, and deferred to the next
// leader (Decisions 5 and 12): two items in round 1, and the holding's context
// ended, as a drain's first moment ends it, the coordinator still held, its
// fence still to come. RecordKillSwitchOn fences it first, as the fence would:
// every turn stops, every session ends, and every run is dropped, its timers
// cancelled, so that no step starts a turn once the row commits. It then
// writes the row, as row 1, under the epoch given, the draining holding's,
// and answers on, at row 1, changed and deferred, with no run stopped and
// nothing owed: the fenced coordinator holds no account, and acts on no run.
// The coordinator that takes over next reads the switch, and halts both runs
// killed, by the on-row 1, before either opens a round; no turn starts.
// Fails first: a build failure, as the coordinator has no RecordKillSwitchOn.
func TestAKillSwitchOnAtTheDrainsStartIsDeferred(t *testing.T) {
	h := newHarness(t)
	endAlls := &atomic.Int32{}
	end := endingHolding(t, h, func(p *app.Ports) { p.Sessions = endingAll{Sessions: p.Sessions, n: endAlls} })
	a, b := h.open("checkout fails"), h.open("payments fail")
	require.Len(t, h.turns.started, 4, "their turns run")
	require.NotZero(t, h.clock.pending(), "their rounds' timers are armed")
	stopAlls, ends := h.turns.all, endAlls.Load()
	end()

	out, err := h.c.RecordKillSwitchOn(t.Context(), h.epoch, killing(0))

	require.NoError(t, err)
	assert.Equal(t, app.KillSwitchOutcome{On: true, Seq: 1, Changed: true, Deferred: true}, out)
	assert.Equal(t, stopAlls+1, h.turns.all, "every turn stops")
	assert.Equal(t, ends+1, endAlls.Load(), "every session ends")
	assert.Zero(t, h.clock.pending(), "every run is dropped, its timers cancelled")
	assert.Equal(t, app.Lifecycle{}, h.c.Lifecycle(), "the coordinator is fenced")
	assert.Equal(t, []int64{h.epoch}, h.controls.writes.noted(), "the row is written under the epoch given")
	assert.Equal(t, int64(1), h.controls.rows(t))
	for _, id := range []item.ID{a, b} {
		assert.Nil(t, h.state(id).Run.Stopped, "%s's run is the next leader's to halt", id)
	}

	h.crash()
	require.NoError(t, h.boot())

	for _, id := range []item.ID{a, b} {
		requireKilled(t, h, id)
		assert.Equal(t, 1, countOf(h, id, item.KindRoundOpened), "%s opens no round", id)
	}
	assert.Empty(t, h.turns.started, "no turn starts")
}

// The drain's on is written under a context of its own (Decision 5): the
// request's values without its cancellation, the epoch given, the draining
// holding's, and a deadline of Options.WriteTimeout, the database's
// transaction timeout. The request's context ended, as when its client left,
// and so did the holding's, so the controls fake sees the write's context
// live, carrying the request's values and the epoch given, with a deadline
// WriteTimeout from the call:
//   - the coordinator still held, its holding's context ended, as at a
//     drain's start;
//   - the coordinator fenced under an earlier holding, as when a drain began
//     before the takeover under its holding held: the epoch given is the
//     draining holding's, not the coordinator's last.
//
// Fails first: a build failure.
func TestTheDrainsOnIsWrittenThoughTheHoldingsContextEnded(t *testing.T) {
	for _, c := range []struct {
		name string
		// drain readies the coordinator for the drain's on, and returns the
		// draining holding's epoch.
		drain func(t *testing.T, h *harness) int64
	}{
		{"the coordinator held, its holding's context ended", func(t *testing.T, h *harness) int64 {
			endingHolding(t, h)()
			return h.epoch
		}},
		{"the coordinator fenced under an earlier holding", func(_ *testing.T, h *harness) int64 {
			h.c.Fence()
			h.controls.lease.Store(h.epoch + 1)
			return h.epoch + 1
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.open("checkout fails")
			epoch := c.drain(t, h)
			var live bool
			var carried int64
			var value any
			var deadline time.Time
			h.controls.onWrite(func(ctx context.Context) {
				live, value = ctx.Err() == nil, ctx.Value(requestKey{})
				carried, _ = holding.EpochFrom(ctx)
				deadline, _ = ctx.Deadline()
			})
			request, leave := context.WithCancel(context.WithValue(t.Context(), requestKey{}, "the request's"))
			leave()

			sent := time.Now()
			out, err := h.c.RecordKillSwitchOn(request, epoch, killing(0))
			answered := time.Now()

			require.NoError(t, err)
			assert.True(t, out.Deferred)
			assert.True(t, live, "the write's context is live")
			assert.Equal(t, "the request's", value, "it carries the request's values")
			assert.Equal(t, epoch, carried, "and the epoch given")
			assert.False(t, deadline.Before(sent.Add(h.opts.WriteTimeout)), "its deadline is WriteTimeout from the call")
			assert.False(t, deadline.After(answered.Add(h.opts.WriteTimeout)), "its deadline is WriteTimeout from the call")
			assert.Equal(t, int64(1), h.controls.rows(t), "the row is written")
		})
	}
}

// The drain's on is refused once its holding ended (Decision 5): the store's
// guard still checks the epoch against the lease, in the write's own
// transaction. The drain fenced the coordinator, and the lease is another
// holding's, so the store refuses the write, wrapping ErrLeaseLost: the on
// answers ErrNotHeld, which the admin API answers not_held. No row is written,
// the switch is not read again, and nothing is tried again. Fails first: a
// build failure.
func TestTheDrainsOnIsRefusedOnceItsHoldingEnded(t *testing.T) {
	h := newHarness(t)
	h.open("checkout fails")
	h.c.Fence()
	h.controls.lease.Store(h.epoch + 1)
	reads := h.controls.readsMade()

	out, err := h.c.RecordKillSwitchOn(t.Context(), h.epoch, killing(0))

	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Equal(t, app.KillSwitchOutcome{}, out)
	assert.Equal(t, []int64{h.epoch}, h.controls.writes.noted(), "the write was tried under the epoch given")
	assert.Zero(t, h.controls.rows(t), "no row")
	assert.Equal(t, reads, h.controls.readsMade(), "no second read")
	assert.Zero(t, h.clock.pending(), "nothing is tried again")
}

// A drain's on whose write fails answers the store's error, which the admin
// API answers 503, unavailable, with no second read (Decision 5): the store
// commits the on as row 1, then its answer is lost. The fenced coordinator
// keeps no view to settle, and marks none unsettled (R37): the next leader's
// takeover reads the table, and the client reads the switch on the next
// leader before it sends the on again. Fails first: a build failure.
func TestADrainsOnWhoseAnswerIsLostAnswersUnavailableWithNoSecondRead(t *testing.T) {
	h := newHarness(t)
	h.c.Fence()
	h.controls.loseAnswers(true)
	reads := h.controls.readsMade()

	_, err := h.c.RecordKillSwitchOn(t.Context(), h.epoch, killing(0))

	h.controls.loseAnswers(false)
	requireUnavailable(t, err)
	assert.Equal(t, int64(1), h.controls.rows(t), "the row committed")
	assert.Equal(t, reads, h.controls.readsMade(), "no second read")
	assert.Equal(t, app.Lifecycle{}, h.c.Lifecycle(), "the fenced coordinator marks nothing")
	assert.Zero(t, h.clock.pending(), "nothing is tried again")
}

// The drain's fence never waits on the drain's on (Decision 5; R24):
// RecordKillSwitchOn lets the coordinator's lock go before it writes, so that
// the drain's fence, which waits for that lock under the leader's own, never
// waits on the write. The holding's context ended, as at a drain's start, and
// the store holds the on's write: Fence returns within 50 ms, while the write
// is still held. Once the store lets the write go, the on answers deferred.
// Fails first: a build failure.
func TestTheDrainsFenceNeverWaitsOnTheDrainsOn(t *testing.T) {
	h := newHarness(t)
	end := endingHolding(t, h)
	h.open("checkout fails")
	end()
	gate := make(chan struct{})
	letGo := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(letGo)
	writing := make(chan struct{}, 1)
	h.controls.onWrite(func(context.Context) {
		writing <- struct{}{}
		<-gate
	})
	answer := recordOn(t, h, h.epoch)
	select {
	case <-writing:
	case got := <-answer:
		t.Fatalf("the on answered without writing: %+v, %v", got.out, got.err)
	}

	fenced := make(chan struct{})
	go func() {
		h.c.Fence()
		close(fenced)
	}()
	select {
	case <-fenced:
	case <-time.After(50 * time.Millisecond):
		t.Error("the drain's fence waited on the on's write")
	}

	letGo()
	got := <-answer
	<-fenced
	require.NoError(t, got.err)
	assert.Equal(t, app.KillSwitchOutcome{On: true, Seq: 1, Changed: true, Deferred: true}, got.out)
}

// A deferred on answers without Owed, and logs none (Decision 5; Contract
// C3): an item's round 1 runs when the drain fences the coordinator, which
// then holds no account. The on recorded during the drain answers with Owed
// nil, and "kill switch set" is logged at Warn, on, at row 1, its latest
// on-row 1, by the admin, under the epoch given, for the cause act, deferred,
// with no owed, and neither stopped nor unstaffed, as it acts on no run.
// Fails first: a build failure.
func TestADeferredOnAnswersWithoutOwed(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	h.open("checkout fails")
	h.c.Fence()

	out, err := h.c.RecordKillSwitchOn(t.Context(), h.epoch, killing(0))

	require.NoError(t, err)
	assert.Nil(t, out.Owed)
	assert.True(t, out.Deferred)
	lines := logged(t, logs.String(), "kill switch set")
	require.Len(t, lines, 1)
	assert.Equal(t, map[string]any{
		"level": "WARN", "on": true, "seq": 1.0, "onSeq": 1.0, "by": "admin", "epoch": float64(h.epoch), "cause": "act", "deferred": true,
	}, pick(lines[0], "level", "on", "seq", "onSeq", "by", "epoch", "cause", "deferred"))
	for _, key := range []string{"owed", "stopped", "unstaffed"} {
		assert.NotContains(t, lines[0], key, "no %s", key)
	}
}

// A kill switch turned on during a drain halts the next leader's runs before
// any round (Decisions 5 and 12): an instance leads, with two items in round
// 1, and drains: its coordinator is fenced, and its leader keeps the holding.
// The on, sent as the admin API sends it, finds SetKillSwitch answering
// ErrNotHeld while the leader holds the lease and drains, so it is recorded
// through RecordKillSwitchOn under the leader's epoch, deferred. The leader
// stops, releasing the lease, and the next instance takes over: its takeover
// reads the switch, and halts both runs killed, by the on-row 1, before
// either opens a round; no turn starts. Fails first: a build failure.
func TestAKillSwitchTurnedOnDuringADrainHaltsTheNextLeadersRunsBeforeAnyRound(t *testing.T) {
	h, _, leader := unled(t)
	require.NoError(t, leader.Lead(t.Context()))
	a, b := h.open("checkout fails"), h.open("payments fail")
	leader.Drain()

	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.ErrorIs(t, err, app.ErrNotHeld, "the drain fenced the coordinator")
	epoch, held, draining := leader.Status()
	require.True(t, held, "the leader holds the lease")
	require.True(t, draining, "and drains")
	out, err := h.c.RecordKillSwitchOn(t.Context(), epoch, killing(0))
	require.NoError(t, err)
	require.True(t, out.Deferred)
	leader.Stop(t.Context())

	require.NoError(t, h.boot())

	for _, id := range []item.ID{a, b} {
		requireKilled(t, h, id)
		assert.Equal(t, 1, countOf(h, id, item.KindRoundOpened), "%s opens no round", id)
	}
	assert.Empty(t, h.turns.started, "no turn starts")
}

// A kill switch on during a drain waits for the fence (Decision 5): a step
// that holds the coordinator's lock, an opening whose turn's start waits on
// the runner, is under way when the drain ends the holding's context, and the
// on comes then. RecordKillSwitchOn waits for that step: no write while it
// holds the lock. Once the step goes on and lets the lock go, the on fences
// the coordinator, and its write comes after StopAll, EndAll and the drop of
// every run, whose timers are all cancelled by then. Fails first: a build
// failure.
func TestAKillSwitchOnDuringADrainWaitsForTheFence(t *testing.T) {
	h := newHarness(t)
	order := &trace{}
	end := endingHolding(t, h, func(p *app.Ports) {
		p.Turns = stopsAllNoted{Turns: p.Turns, order: order}
		p.Sessions = endsAllNoted{Sessions: p.Sessions, order: order}
	})
	var pending atomic.Int32
	h.controls.onWrite(func(context.Context) {
		order.note("write")
		pending.Store(int32(h.clock.pending()))
	})
	started, letGo := blockStarts(h)
	opened := make(chan error, 1)
	go func() { opened <- opens(h) }()
	<-started
	end()

	answer := recordOn(t, h, h.epoch)

	require.Never(t, func() bool { return len(h.controls.writes.noted()) > 0 }, 50*time.Millisecond, time.Millisecond,
		"no write while a step holds the coordinator's lock")
	order.note("the step goes on")
	letGo()
	got := <-answer
	<-opened
	require.NoError(t, got.err)
	assert.Equal(t, app.KillSwitchOutcome{On: true, Seq: 1, Changed: true, Deferred: true}, got.out)
	seen := order.seen()
	at := slices.Index(seen, "the step goes on")
	require.GreaterOrEqual(t, at, 0)
	assert.Equal(t, []string{"the step goes on", "stop all", "end all", "write"}, seen[at:], "the fence's StopAll and EndAll, then the write")
	assert.Zero(t, pending.Load(), "every run was dropped, its timers cancelled, before the write")
}

// RecordKillSwitchOn records only an on during a drain, which it checks as
// the on is checked (Decisions 5, 9 and 13), and records nothing otherwise:
//   - a coordinator held with a live context is no draining one: the on
//     answers ErrNotHeld (R24), and the admin API's next try sends it to
//     SetKillSwitch; the coordinator stays held;
//   - a preview records nothing, and the fenced coordinator holds no account
//     to preview against: it answers ErrNotHeld, which the admin API answers
//     as the drain's, as every act during a drain;
//   - an on without a reason is refused, reason_required.
//
// None writes a row. Fails first: a build failure.
func TestARecordedOnRefusesWhatItCannotRecord(t *testing.T) {
	for _, c := range []struct {
		name string
		// fenced has the drain fence the coordinator first.
		fenced bool
		act    *app.Act
		want   func(t *testing.T, err error)
	}{
		{"a coordinator held with a live context", false, killing(0), func(t *testing.T, err error) { require.ErrorIs(t, err, app.ErrNotHeld) }},
		{"a preview", true, &app.Act{Actor: administrator, Preview: true}, func(t *testing.T, err error) { require.ErrorIs(t, err, app.ErrNotHeld) }},
		{"an on without a reason", true, &app.Act{Actor: administrator}, func(t *testing.T, err error) {
			assert.Equal(t, app.CodeReasonRequired, refusal(err))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.open("checkout fails")
			if c.fenced {
				h.c.Fence()
			}
			lifecycle := h.c.Lifecycle()

			out, err := h.c.RecordKillSwitchOn(t.Context(), h.epoch, c.act)

			c.want(t, err)
			assert.Equal(t, app.KillSwitchOutcome{}, out)
			assert.Empty(t, h.controls.writes.noted(), "no write")
			assert.Zero(t, h.controls.rows(t), "no row")
			assert.Equal(t, lifecycle, h.c.Lifecycle(), "the coordinator stays as it was")
		})
	}
}

// A coordinator needs a write timeout above zero, the database's transaction
// timeout, which bounds the drain's on (Decision 5; Contract C16).
func TestACoordinatorNeedsAWriteTimeoutAboveZero(t *testing.T) {
	h := newHarness(t)
	for _, timeout := range []time.Duration{0, -time.Second} {
		opts := h.opts
		opts.WriteTimeout = timeout

		_, err := app.NewCoordinator(h.ports(), &opts)

		require.Error(t, err, "a write timeout of %v", timeout)
	}
}

// An on whose halt finds the lease gone answers ErrNotHeld (Decision 13;
// Decision 5): two items in round 1; the on commits as row 1, and its steps
// halt the first item's run, but the second item's halt finds the lease gone,
// which fences the coordinator and tells its leader. The act answers
// ErrNotHeld, not a success whose lists the fence emptied: the row is the
// table's, and the next leader's takeover halts the second run killed, by the
// on-row 1, as its own line says. The fenced coordinator's "kill switch set"
// line carries neither stopped, unstaffed nor owed, which its emptied account
// no longer gives (Contract C3). Fails first: the on answers success, with
// nothing owed, and logs owed empty.
func TestAnOnWhoseHaltFindsTheLeaseGoneAnswersNotHeld(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	a, b := h.open("checkout fails"), h.open("payments fail")
	require.Less(t, string(a), string(b), "the on's steps reach the first item first")
	var lost []int64
	h.c.OnLost(func(epoch int64) { lost = append(lost, epoch) })
	h.records.refuses(func(id item.ID, drafts []item.Draft) error {
		if id == b && haltOf(drafts) {
			return fmt.Errorf("record: %w", app.ErrLeaseLost)
		}
		return nil
	})

	out, err := h.c.SetKillSwitch(t.Context(), true, killing(0))

	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Equal(t, app.KillSwitchOutcome{}, out, "no answer of a fenced act")
	assert.Equal(t, int64(1), h.controls.rows(t), "the row committed")
	assert.Equal(t, app.Lifecycle{}, h.c.Lifecycle(), "the coordinator is fenced")
	requireKilled(t, h, a)
	assert.Nil(t, h.state(b).Run.Stopped, "its halt found the lease gone")
	h.clock.Advance(0)
	assert.Equal(t, []int64{h.epoch}, lost, "its leader is told")
	lines := logged(t, logs.String(), "kill switch set")
	require.Len(t, lines, 1)
	assert.Equal(t, map[string]any{"on": true, "seq": 1.0, "cause": "act", "deferred": false}, pick(lines[0], "on", "seq", "cause", "deferred"))
	for _, key := range []string{"owed", "stopped", "unstaffed"} {
		assert.NotContains(t, lines[0], key, "no %s", key)
	}

	h.records.refuses(nil)
	h.crash()
	require.NoError(t, h.boot())

	requireKilled(t, h, b)
	assert.Equal(t, 1, countOf(h, a, item.KindRunStopped), "the first run is halted once")
	takeover := logged(t, logs.String(), "kill switch set")
	require.Len(t, takeover, 2)
	assert.Equal(t, map[string]any{"cause": "takeover", "stopped": 1.0, "owed": nil}, pick(takeover[1], "cause", "stopped", "owed"))
}

// A refresh of the kill switch once the holding's context ended reads
// nothing, and logs no failure (Decisions 5 and 13): a row written out of
// band, as row 1, is newer than the coordinator's view, so the leader's
// renewal armed the refresh; but a drain ends the holding's context before
// its fence, and the refresh meets the ended context:
//   - before it reads: it reads no switch;
//   - as it reads, the coordinator's lock already held: its read fails for
//     the ended context.
//
// Either way it logs no "a step of a work item failed", and leaves the view
// as it was: the next leader reads the switch at its takeover. Fails first:
// the refresh reads under the ended context, and logs the read's failure with
// no item.
func TestARefreshOnceTheHoldingsContextEndedReadsNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		// ends ends the holding's context, end, in the harness's instance.
		ends  func(h *harness, end func())
		reads int
	}{
		{"before it reads", func(_ *harness, end func()) { end() }, 0},
		{"as it reads", func(h *harness, end func()) { h.controls.onRead(end) }, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			logs := &syncBuffer{}
			h := newHarness(t, logTo(logs))
			end := endingHolding(t, h)
			writeRows(t, h, true)
			reads := h.controls.readsMade()
			c.ends(h, end)

			h.c.RefreshKillSwitch(t.Context())

			assert.Equal(t, reads+c.reads, h.controls.readsMade())
			assert.Empty(t, logged(t, logs.String(), "a step of a work item failed"), "no failure is logged")
			assert.Zero(t, h.c.KillSwitchSeen(), "the view stays as it was")
		})
	}
}
