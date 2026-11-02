package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// promoting is a promotion through the admin API, by its admin, under key,
// with no reason.
func promoting(key string) *app.Act {
	return &app.Act{Actor: app.Actor{Subject: "admin", Admin: true}, Key: key}
}

// promotedKinds are the records of a queued item that a promotion started:
// its opening, its promotion, its start, its run's creation and its first
// round's opening.
var promotedKinds = []item.Kind{item.KindOpened, item.KindPromoted, item.KindStarted, item.KindRunCreated, item.KindRoundOpened}

// refusal returns the code of err when it is a *Refused, and "" otherwise.
func refusal(err error) string {
	var refused *app.Refused
	if errors.As(err, &refused) {
		return refused.Code
	}
	return ""
}

// notStaffedAtTheCap opens an incident while the day's spend is at the daily
// cap, which leaves it awaiting its close not staffed, for the cause
// daily_cap, without a run; then the day's spend falls below the cap again, as
// after midnight.
func notStaffedAtTheCap(h *harness) item.ID {
	h.t.Helper()
	h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })
	id := h.open("checkout fails")
	require.Equal(h.t, &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseDailyCap, AutoClose: closesAfter}, h.state(id).Closing)
	h.spend.set(func(s *spend) { s.day = 0 })
	return id
}

// withoutItsRun opens an incident whose run's creation keeps failing, as the
// revision it locked cannot be read: the item investigates with no run.
// restore lets the revision be read again.
func withoutItsRun(h *harness) (id item.ID, restore func()) {
	h.t.Helper()
	restore = loseCommit(h, commit)
	id = h.open("checkout fails")
	require.Equal(h.t, []item.Kind{item.KindOpened, item.KindStarted}, h.kinds(id), "its run's creation failed")
	return id, restore
}

// admittedUnstarted has, under a limit of 1, an incident investigate and a
// second wait queued, whose start the records store refuses. Once the first
// stops, the walk admits the second, whose start fails: it is queued,
// admitted and counted, its start due.
func admittedUnstarted(h *harness) item.ID {
	h.t.Helper()
	a := h.open("checkout fails")
	q := openLater(h, "payments fail")
	h.records.refuses(func(id item.ID, drafts []item.Draft) error {
		if id == q && startOf(drafts) {
			return errStartRefused
		}
		return nil
	})
	stopQuiet(h, a)
	require.Equal(h.t, []item.Kind{item.KindOpened}, h.kinds(q), "its start failed")
	require.Equal(h.t, app.Lifecycle{Limit: 1, Investigating: 1}, h.c.Lifecycle(), "admitted, and counted")
	return q
}

// promotionAnswerLost is the records store whose appends of a promotion
// commit, then fail with errAnswerLost, as when the database's answer is lost
// on its way back. Every other append answers.
type promotionAnswerLost struct{ *failing }

func (l promotionAnswerLost) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	committed, err := l.failing.Append(ctx, id, decide)
	if err != nil {
		return nil, err
	}
	for i := range committed {
		if committed[i].Body.Promoted != nil {
			return nil, errAnswerLost
		}
	}
	return committed, nil
}

// promotionsLoseTheirAnswers has the harness's instance take over again, under
// the same epoch, with a promotionAnswerLost of the harness's records store as
// its records store.
func promotionsLoseTheirAnswers(t *testing.T, h *harness) {
	t.Helper()
	ports := h.ports()
	ports.Records = promotionAnswerLost{h.records}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
}

// A promotion starts a queued item whatever the queue, even past the limit by
// one (spec Section 5.1; Decision 4): with a limit of 1, A investigating and B
// queued, B's promotion records item_promoted, by the admin, with its request,
// which keeps the key's hash, then B's start, its run and its first round. Two
// items investigate, and the promotion answers B among the starts. The
// switch was never set, so its item_started knew no row of it:
// TestAPromotionIsRefusedWhileTheKillSwitchIsOn asserts a start that knew one.
func TestAPromotionStartsAQueuedItemOverTheLimitByOne(t *testing.T) {
	h := newHarness(t, limit(1))
	h.open("checkout fails")
	b := openLater(h, "payments fail")
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(b), "B waits")

	out, err := h.c.Promote(t.Context(), b, promoting("promote-1"))

	require.NoError(t, err)
	assert.Equal(t, promotedKinds, h.kinds(b))
	p := h.state(b).Promotion
	require.NotNil(t, p)
	assert.Equal(t, "admin", p.By)
	assert.Nil(t, p.Reason)
	assert.Equal(t, keyHash("promote-1"), p.Request.Key)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, p.Request.Digest)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Starts: []item.ID{b}, Investigating: 2, Limit: 1}}, out)
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 2}, h.c.Lifecycle())
}

// Past the limit and one, a promotion is refused (Decision 4): with a limit of
// 1, A investigating, B promoted and started, and C queued, C's promotion
// answers Refused, limit_reached, and records nothing.
func TestAPromotionPastTheLimitAndOneIsRefused(t *testing.T) {
	h := newHarness(t, limit(1))
	h.open("checkout fails")
	b := openLater(h, "payments fail")
	c := openLater(h, "orders fail")
	_, err := h.c.Promote(t.Context(), b, promoting("promote-1"))
	require.NoError(t, err)
	require.Equal(t, app.Lifecycle{Limit: 1, Investigating: 2, Waiting: []item.ID{c}}, h.c.Lifecycle())
	before := h.carried()

	_, err = h.c.Promote(t.Context(), c, promoting("promote-1"))

	assert.Equal(t, app.CodeLimitReached, refusal(err), "%v", err)
	assert.Equal(t, before, h.carried(), "nothing is recorded")
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(c))
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 2, Waiting: []item.ID{c}}, h.c.Lifecycle())
}

// A promoted item counts toward the limit until it starts (Decision 4): with a
// limit of 1, A investigating, and B and C queued, B's promotion records
// item_promoted, but its start fails, as the records store refuses B's
// item_started each time. B counts all the same: two items investigate, and
// the promotion names no start; C's promotion is refused, limit_reached. When
// A stops, the walk admits no one: B holds the slot, and C waits.
func TestAPromotedItemCountsTowardTheLimit(t *testing.T) {
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

	out, err := h.c.Promote(t.Context(), b, promoting("promote-1"))

	require.NoError(t, err)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindPromoted}, h.kinds(b), "its start failed")
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Investigating: 2, Limit: 1}}, out, "B counts, and has not started")
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 2, Waiting: []item.ID{c}}, h.c.Lifecycle())
	_, err = h.c.Promote(t.Context(), c, promoting("promote-1"))
	assert.Equal(t, app.CodeLimitReached, refusal(err), "%v", err)
	stopQuiet(h, a)
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(c), "the walk admits no one")
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1, Waiting: []item.ID{c}}, h.c.Lifecycle(), "B holds the slot")
}

// A promotion of a queued item the walk admitted changes nothing, its start
// due and counted (Decisions 4 and 13): with a limit of 1, Q admitted, its
// start failed, and R promoted and started, two items investigate, past the
// limit. Q's promotion answers changed false, records nothing, and is not
// refused for the limit and one: the check of its effect comes first (R23).
func TestPromotingAnAdmittedItemChangesNothing(t *testing.T) {
	h := newHarness(t, limit(1))
	q := admittedUnstarted(h)
	r := openLater(h, "orders fail")
	_, err := h.c.Promote(t.Context(), r, promoting("promote-1"))
	require.NoError(t, err)
	require.Equal(t, promotedKinds, h.kinds(r))
	require.Equal(t, app.Lifecycle{Limit: 1, Investigating: 2}, h.c.Lifecycle(), "past the limit")
	before := h.carried()

	out, err := h.c.Promote(t.Context(), q, promoting("promote-1"))

	require.NoError(t, err, "not refused for the limit")
	assert.Equal(t, app.Outcome{Consequences: app.Consequences{Investigating: 2, Limit: 1}}, out)
	assert.Equal(t, before, h.carried(), "nothing is recorded")
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(q))
}

// A promotion starts an item that never ran (Decision 4): an item opened at the
// daily cap awaits its close not staffed, without a run; once the day's spend
// is below the cap again, its promotion lifts its awaiting_close, and it
// starts: its run is created and its first round opens, its agents' turns
// starting.
func TestPromotingAnItemThatNeverRanStartsIt(t *testing.T) {
	h := newHarness(t)
	id := notStaffedAtTheCap(h)

	out, err := h.c.Promote(t.Context(), id, promoting("promote-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Starts: []item.ID{id}, Investigating: 1, Limit: 3}}, out)
	assert.Equal(t, []item.Kind{
		item.KindOpened, item.KindAwaitingClose, item.KindPromoted, item.KindStarted, item.KindRunCreated, item.KindRoundOpened,
	}, h.kinds(id))
	s := h.state(id)
	assert.Nil(t, s.Closing, "its awaiting_close is lifted")
	assert.Equal(t, item.StatusInvestigating, s.Status())
	assert.Len(t, h.turns.started, 2, "its round's turns start")
}

// A promotion of an item that started without a run has its step create its
// run (Decision 4): an item whose run's creation failed, then left awaiting
// its close not staffed, for the cause kill_switch, by a record the test
// appends as Task 12's kill switch writes it, is promoted once its revision
// reads again. Its promotion records item_promoted, which lifts its
// awaiting_close, and its step creates its run, whose first round opens. It
// started already, so the promotion names no start.
func TestPromotingAStartedItemWithoutARunCreatesItsRun(t *testing.T) {
	h := newHarness(t)
	id, restore := withoutItsRun(h)
	appendRaw(t, h, id, item.Body{V: item.Version, AwaitingClose: &item.AwaitingClose{
		Reason: item.StopNotStaffed, AutoClose: closesAfter, Cause: item.CauseKillSwitch, UnstaffedBy: 1,
	}})
	require.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
	restore()

	out, err := h.c.Promote(t.Context(), id, promoting("promote-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Investigating: 1, Limit: 3}}, out)
	assert.Equal(t, []item.Kind{
		item.KindOpened, item.KindStarted, item.KindAwaitingClose, item.KindPromoted, item.KindRunCreated, item.KindRoundOpened,
	}, h.kinds(id))
	assert.Equal(t, item.StatusInvestigating, h.state(id).Status())
}

// A promotion of an item that investigates without a run changes nothing
// (Decisions 4 and 13): its step creates its run. Here the run's creation
// keeps failing, as the revision the item locked cannot be read; the
// promotion answers changed false, and records nothing.
func TestPromotingAnInvestigatingItemWithoutARunChangesNothing(t *testing.T) {
	h := newHarness(t)
	id, _ := withoutItsRun(h)
	h.clock.Advance(h.opts.Retry)
	require.Equal(t, []item.Kind{item.KindOpened, item.KindStarted}, h.kinds(id), "its run's creation keeps failing")
	before := h.carried()

	out, err := h.c.Promote(t.Context(), id, promoting("promote-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Consequences: app.Consequences{Investigating: 1, Limit: 3}}, out)
	assert.Equal(t, before, h.carried(), "nothing is recorded")
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindStarted}, h.kinds(id))
}

// An admitted item counts toward the limit and one, as a promoted item does
// (Decision 4; the plan's Review Focus 5): with a limit of 3, X1, X2 and X3
// investigating, and the queue [Q1, Q3, Q4], X3 stops. The walk admits Q1,
// whose start fails once, as the records store refuses Q1's first
// item_started. Q3's promotion starts it; Q4's is refused, limit_reached; and
// Q1's retry starts it: four items investigate, the limit and one, never
// five. Under a walk whose admission does not count, the walk would admit Q3
// too, Q4's promotion would start it, and five would investigate.
func TestAnAdmittedItemCountsTowardTheLimitAndOne(t *testing.T) {
	h := newHarness(t)
	x1, x2, x3 := h.open("checkout fails"), h.open("payments fail"), h.open("orders fail")
	q1, q3, q4 := openLater(h, "search fails"), openLater(h, "billing fails"), openLater(h, "login fails")
	require.Equal(t, app.Lifecycle{Limit: 3, Investigating: 3, Waiting: []item.ID{q1, q3, q4}}, h.c.Lifecycle())
	refused := false
	h.records.refuses(func(id item.ID, drafts []item.Draft) error {
		if id == q1 && startOf(drafts) && !refused {
			refused = true
			return errStartRefused
		}
		return nil
	})
	stopQuiet(h, x3)
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(q1), "the walk admitted Q1, whose start failed")
	assert.Equal(t, app.Lifecycle{Limit: 3, Investigating: 3, Waiting: []item.ID{q3, q4}}, h.c.Lifecycle(), "Q1 counts")

	_, err := h.c.Promote(t.Context(), q3, promoting("promote-1"))
	require.NoError(t, err)
	assert.Equal(t, promotedKinds, h.kinds(q3), "Q3's promotion starts it")
	_, err = h.c.Promote(t.Context(), q4, promoting("promote-1"))
	assert.Equal(t, app.CodeLimitReached, refusal(err), "%v", err)
	h.clock.Advance(h.opts.Retry)

	assert.Equal(t, startedKinds, h.kinds(q1), "Q1's retry starts it")
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(q4), "Q4 waits")
	for _, id := range []item.ID{x1, x2, q1, q3} {
		assert.Equal(t, item.StatusInvestigating, h.state(id).Status(), "%s investigates", id)
	}
	assert.Equal(t, app.Lifecycle{Limit: 3, Investigating: 4, Waiting: []item.ID{q4}}, h.c.Lifecycle(), "four, the limit and one, never five")
}

// A promotion whose append committed, though the database's answer was lost,
// is found by its key (Decision 13): Promote loads the item, finds its
// item_promoted, and answers changed, as a repeat (R14); the item, advanced,
// starts.
func TestAPromotionWhoseCommitAnswerWasLostStartsTheItem(t *testing.T) {
	h := newHarness(t, limit(1))
	promotionsLoseTheirAnswers(t, h)
	h.open("checkout fails")
	b := openLater(h, "payments fail")

	out, err := h.c.Promote(t.Context(), b, promoting("promote-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Investigating: 2, Limit: 1}}, out, "as a repeat")
	assert.Equal(t, promotedKinds, h.kinds(b), "its item_promoted landed, and it starts")
}

// An act whose effect holds already changes nothing (Decision 13): a close of
// a closed item; a reopen of an item awaiting its close; a promotion of an
// item that has a run, here one awaiting its close; of an item that
// investigates with no run; and of a queued item the walk admitted, whose
// start failed. Each answers changed false and records nothing, so that its
// key is kept nowhere (R10): a later act under the same key, with another
// digest, is taken, not refused as a key reused.
func TestAnActWhoseEffectHoldsChangesNothing(t *testing.T) {
	type act func(h *harness, id item.ID, key string) (app.Outcome, error)
	closeIt := func(h *harness, id item.ID, key string) (app.Outcome, error) {
		return h.c.Close(h.t.Context(), id, closing(key))
	}
	reopenIt := func(h *harness, id item.ID, key string) (app.Outcome, error) {
		return h.c.Reopen(h.t.Context(), id, reopening(key))
	}
	promoteIt := func(h *harness, id item.ID, key string) (app.Outcome, error) {
		return h.c.Promote(h.t.Context(), id, promoting(key))
	}
	for _, c := range []struct {
		name       string
		limit      int
		item       func(h *harness) item.ID
		act, later act
	}{
		{"a close of a closed item", 3, func(h *harness) item.ID { return closedAwaiting(h, "checkout fails") }, closeIt, reopenIt},
		{"a reopen of an item awaiting its close", 3, func(h *harness) item.ID { return awaitingItsClose(h, "checkout fails") }, reopenIt, closeIt},
		{"a promotion of an item that has a run", 3, func(h *harness) item.ID { return awaitingItsClose(h, "checkout fails") }, promoteIt, closeIt},
		{"a promotion of an investigating item with no run", 3, func(h *harness) item.ID {
			id, _ := withoutItsRun(h)
			return id
		}, promoteIt, closeIt},
		{"a promotion of a queued item admitted", 1, admittedUnstarted, promoteIt, closeIt},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, limit(c.limit))
			id := c.item(h)
			kinds, before := h.kinds(id), h.carried()

			out, err := c.act(h, id, "act-1")

			require.NoError(t, err)
			assert.False(t, out.Changed)
			assert.Equal(t, kinds, h.kinds(id), "nothing is recorded")
			assert.Equal(t, before, h.carried())
			later, err := c.later(h, id, "act-1")
			require.NoError(t, err, "its key is kept nowhere")
			assert.True(t, later.Changed)
		})
	}
}

// A promotion of a closed item is refused, closed: reopen it first; and one
// once the day's spend reached the daily cap is refused, daily_cap_reached
// (Decision 4). Each comes in R23's order: with a limit of 1, A
// investigating, B promoted and started, a closed item, and C queued, the day's
// spend reaches the cap. The closed item's promotion answers closed, though
// the day's spend is at the cap; C's answers daily_cap_reached, though the
// items investigating are at the limit and one. Neither records anything.
// (The plan's.)
func TestAPromotionIsRefusedOnAClosedItemAndAtTheDailyCap(t *testing.T) {
	h := newHarness(t, limit(1))
	h.open("checkout fails")
	b := openLater(h, "payments fail")
	closed := openLater(h, "orders fail")
	c := openLater(h, "search fails")
	_, err := h.c.Close(t.Context(), closed, closing("close-1"))
	require.NoError(t, err)
	_, err = h.c.Promote(t.Context(), b, promoting("promote-1"))
	require.NoError(t, err)
	require.Equal(t, app.Lifecycle{Limit: 1, Investigating: 2, Waiting: []item.ID{c}}, h.c.Lifecycle(), "the limit and one")
	h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })
	before := h.carried()

	_, onClosed := h.c.Promote(t.Context(), closed, promoting("promote-1"))
	_, atTheCap := h.c.Promote(t.Context(), c, promoting("promote-1"))

	assert.Equal(t, app.CodeClosed, refusal(onClosed), "%v", onClosed)
	assert.Equal(t, app.CodeDailyCapReached, refusal(atTheCap), "%v", atTheCap)
	assert.Equal(t, before, h.carried(), "nothing is recorded")
	assert.Equal(t, item.StatusClosed, h.state(closed).Status())
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(c))
}

// A promoted item whose start failed once starts on its own retry, with no
// other trigger, and so holds no slot for ever (Decision 4; Task 9's review):
// with a limit of 1, A investigating, and B and C queued, B's promotion
// records item_promoted, but B's start fails, as the day's spend cannot be
// read at that read. B counts, its start due, and the promotion names no
// start; on its retry, B starts, and C still waits. A step that started
// admitted items alone would leave B promoted, counted and unstarted for
// ever.
func TestAPromotedItemWhoseStartFailedOnceStartsOnItsRetry(t *testing.T) {
	h := newHarness(t, limit(1))
	h.open("checkout fails")
	b := openLater(h, "payments fail")
	c := openLater(h, "orders fail")
	// The promotion reads the day's spend first, and B's start next.
	h.spend.dayFails(func(n int) error {
		if n == 2 {
			return errors.New("the day's spend cannot be read")
		}
		return nil
	})
	out, err := h.c.Promote(t.Context(), b, promoting("promote-1"))
	require.NoError(t, err)
	require.Equal(t, []item.Kind{item.KindOpened, item.KindPromoted}, h.kinds(b), "its start failed")
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Investigating: 2, Limit: 1}}, out, "no start yet")
	require.Equal(t, app.Lifecycle{Limit: 1, Investigating: 2, Waiting: []item.ID{c}}, h.c.Lifecycle(), "B counts")

	h.clock.Advance(h.opts.Retry)

	assert.Equal(t, promotedKinds, h.kinds(b), "B starts on its retry")
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(c))
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 2, Waiting: []item.ID{c}}, h.c.Lifecycle())
}

// A promotion's preview takes no key, runs the promotion's checks on the same
// state, under the same lock, and answers its consequences with changed
// false, recording nothing and starting nothing (Decision 13): with a limit of
// 1, A investigating, and B and C queued, B's preview answers B among the
// starts, and two items investigating; B still waits. The promotion that
// follows answers what the preview gave. C's preview then is refused,
// limit_reached, as its promotion would be.
func TestAPromotionsPreviewRecordsNothing(t *testing.T) {
	h := newHarness(t, limit(1))
	h.open("checkout fails")
	b := openLater(h, "payments fail")
	c := openLater(h, "orders fail")
	before := h.carried()

	preview, err := h.c.Promote(t.Context(), b, previewing())

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Consequences: app.Consequences{Starts: []item.ID{b}, Investigating: 2, Limit: 1}}, preview)
	assert.Equal(t, before, h.carried(), "nothing is recorded, and no turn starts")
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1, Waiting: []item.ID{b, c}}, h.c.Lifecycle(), "B still waits")
	out, err := h.c.Promote(t.Context(), b, promoting("promote-1"))
	require.NoError(t, err)
	assert.True(t, out.Changed)
	assert.Equal(t, preview.Consequences, out.Consequences, "the preview gave what the promotion did")
	_, err = h.c.Promote(t.Context(), c, previewing())
	assert.Equal(t, app.CodeLimitReached, refusal(err), "%v", err)
}

// A promotion repeated with its key answers as the promotion did, and records
// nothing more (Decisions 4 and 13; R14): the stops and starts it made are in
// its records. Under its key with another reason's code it is another act of
// the item, refused with ErrKeyReused (R42).
func TestAPromotionRepeatedWithItsKeyAnswersAsItDid(t *testing.T) {
	h := newHarness(t, limit(1))
	h.open("checkout fails")
	b := openLater(h, "payments fail")
	first, err := h.c.Promote(t.Context(), b, promoting("promote-1"))
	require.NoError(t, err)
	promoted, before := h.kinds(b), h.carried()
	another := promoting("promote-1")
	another.Reason = &board.Reason{Code: "urgent"}

	again, err := h.c.Promote(t.Context(), b, promoting("promote-1"))
	_, reused := h.c.Promote(t.Context(), b, another)

	require.NoError(t, err)
	assert.True(t, first.Changed)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Investigating: 2, Limit: 1}}, again, "changed, as the promotion did")
	require.ErrorIs(t, reused, app.ErrKeyReused)
	assert.Equal(t, promoted, h.kinds(b), "no record more")
	assert.Equal(t, before, h.carried(), "nothing is written, and no turn starts again")
}

// A promotion's reason is redacted with the rules that apply always before it
// is recorded (Decision 9; R13): a promotion whose reason's text holds what
// the api-keys rule matches records the rule's replacement in its place, and
// its record holds the key nowhere.
func TestAPromotionsReasonIsRedactedBeforeItIsRecorded(t *testing.T) {
	h := newHarness(t, limit(1))
	h.open("checkout fails")
	b := openLater(h, "payments fail")
	a := promoting("promote-1")
	a.Reason = &board.Reason{Code: "urgent", Text: "the key sk-abcdefgh leaked here"}

	_, err := h.c.Promote(t.Context(), b, a)

	require.NoError(t, err)
	records, err := h.records.Load(t.Context(), b)
	require.NoError(t, err)
	require.Len(t, records, len(promotedKinds))
	promoted := records[1]
	require.NotNil(t, promoted.Body.Promoted)
	assert.Equal(t, &board.Reason{Code: "urgent", Text: "the key [redacted:api-keys] leaked here"}, promoted.Body.Promoted.Reason)
	raw, err := json.Marshal(promoted)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "sk-abcdefgh")
}

// A promotion is allowed as often as the item has no run (R6): an item not
// staffed at the daily cap is promoted while the current revision's rules
// refuse its routing, so its start leaves it not staffed again, for the cause
// routing, and the promotion answers it among the items not staffed. Once the
// configuration is fixed, the item is promoted again, and starts.
func TestAPromotionIsAllowedAsOftenAsTheItemHasNoRun(t *testing.T) {
	h := newHarness(t)
	id := notStaffedAtTheCap(h)
	fixed := h.revs.rev
	h.revs.set(h.revision(strings.Repeat("b", 40), func(files map[string][]byte) {
		files["redaction-rules/services.yaml"] = []byte(`kind: RedactionRule
metadata: {name: services, owner: security@example.com}
spec:
  always: true
  patterns: ['^checkout$']
`)
	}))

	first, err := h.c.Promote(t.Context(), id, promoting("promote-1"))
	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Unstaffed: []item.ID{id}, Limit: 3}}, first, "not staffed again")
	assert.Equal(t, &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseRouting, AutoClose: closesAfter}, h.state(id).Closing)
	h.revs.set(fixed)

	again, err := h.c.Promote(t.Context(), id, promoting("promote-2"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Starts: []item.ID{id}, Investigating: 1, Limit: 3}}, again)
	assert.Equal(t, []item.Kind{
		item.KindOpened, item.KindAwaitingClose, item.KindPromoted, item.KindAwaitingClose,
		item.KindPromoted, item.KindStarted, item.KindRunCreated, item.KindRoundOpened,
	}, h.kinds(id))
}
