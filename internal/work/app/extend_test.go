package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// extending is an extension through the admin API, by its admin, under key,
// with no reason.
func extending(key string) *app.Act {
	return &app.Act{Actor: administrator, Key: key}
}

// tokens is an extension of n tokens alone.
func tokens(n int64) item.Amount { return item.Amount{Tokens: &n} }

// rounds is an extension of n rounds, its other dimensions their share.
func rounds(n int) item.Amount { return item.Amount{Rounds: &n} }

// wallClock is an extension of d of wall clock alone.
func wallClock(d time.Duration) item.Amount { return item.Amount{WallClock: &d} }

// busy has the release investigator post a task in each turn, so that no
// round is quiet and the run goes on to its budget.
func busy(h *harness) {
	h.scripts[release.Name] = func(h *harness, t *app.Turn) item.TurnClass {
		r := h.call(t, "post_task", `{"title":"check round `+strings.Repeat("i", t.Round)+`"}`)
		require.Equal(h.t, actionsapp.StatusOK, r.Status, r.Text)
		return endTurn(h, t)
	}
}

// millionTokens makes the harness's current revision one whose incident type
// has a budget of 1,000,000 tokens, so that an extension of 500,000 is within
// it; the items that start from now on lock it.
func millionTokens(h *harness) {
	h.revs.set(h.revision(strings.Repeat("f", 40), func(files map[string][]byte) {
		files["work-item-types/incident.yaml"] = []byte(strings.Replace(string(files["work-item-types/incident.yaml"]), "tokens: 90000", "tokens: 1000000", 1))
	}))
}

// inRound opens a busy incident and plays its rounds until round n is open,
// and returns it; the moves made so far are forgotten.
func inRound(h *harness, n int) item.ID {
	h.t.Helper()
	busy(h)
	id := h.open("checkout fails")
	for range n - 1 {
		h.round()
	}
	l := h.state(id).Run.Latest()
	require.Equal(h.t, n, l.Opened.Round)
	require.Nil(h.t, l.Ended, "round %d is open", n)
	h.moves.take()
	return id
}

// usedBy is the budget a run used, as its latest ended round says.
func usedBy(r *item.Run) item.Budget {
	for i := len(r.Rounds) - 1; i >= 0; i-- {
		if e := r.Rounds[i].Ended; e != nil {
			return item.Budget{Rounds: e.Rounds, WallClock: e.WallClock, Tokens: e.Spent}
		}
	}
	return item.Budget{}
}

// raisedTo is the moves that put each agent of the open round of id's run in
// it with budget, its deadline unchanged.
func raisedTo(h *harness, id item.ID, budget int64) []move {
	h.t.Helper()
	s := h.state(id)
	o := s.Run.Latest().Opened
	out := make([]move, 0, len(o.Agents))
	for _, agent := range o.Agents {
		out = append(out, move{run: s.Run.ID, agent: agent, round: o.Round, deadline: o.Deadline, budget: budget})
	}
	return out
}

// An extension of 500,000 tokens while round 2 is open raises its sessions'
// budget at once (Decision 17; R5, R54): run_extended, by the admin, and each
// agent of round 2 is moved with the round's budget plus 500,000, its
// deadline unchanged. The answer is changed, with the budget before, after
// and used, and no resume. Fails first: a build failure.
func TestAnExtensionWhileARoundIsOpenRaisesItsSessionsTokenBudget(t *testing.T) {
	h := newHarness(t)
	millionTokens(h)
	id := inRound(h, 2)
	before := h.state(id)
	budget := before.Run.Latest().Opened.Budget

	out, err := h.c.Extend(t.Context(), id, tokens(500_000), extending("extend-1"))

	require.NoError(t, err)
	after := h.state(id)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Budget: &app.BudgetChange{
		Before: before.Run.Budget(), After: after.Run.Budget(), Used: usedBy(after.Run),
	}}}, out)
	assert.Equal(t, before.Run.Budget().Tokens+500_000, after.Run.Budget().Tokens)
	require.Equal(t, item.KindExtended, lastKind(h, id))
	e := lastRecord(h, id).Body.Extended
	assert.Equal(t, item.Budget{Tokens: 500_000}, e.Amount)
	assert.Equal(t, administrator.Subject, e.By)
	assert.Zero(t, e.KillSwitchSeen, "a running run's extension records no kill switch row")
	assert.ElementsMatch(t, raisedTo(h, id, budget+500_000), h.moves.take())
	assert.Equal(t, budget+500_000, after.Run.SessionBudget(2))
}

// An extension above the run's creation's budget in any dimension is refused
// ErrInvalid (Decision 17): rounds, wall clock or tokens above the type's,
// its preview too. Nothing is recorded, and no session moves.
func TestAnExtensionLargerThanTheTypesBudgetIsRefused(t *testing.T) {
	h := newHarness(t)
	id := inRound(h, 2)
	kinds := h.kinds(id)
	for name, asked := range map[string]item.Amount{
		"rounds":     rounds(4),
		"wall clock": wallClock(10*time.Minute + time.Second),
		"tokens":     tokens(90_001),
	} {
		for _, key := range []string{"extend-" + strings.ReplaceAll(name, " ", "-"), ""} {
			_, err := h.c.Extend(t.Context(), id, asked, as(administrator, key))

			require.ErrorIs(t, err, app.ErrInvalid, "%s (key %q)", name, key)
		}
	}
	assert.Equal(t, kinds, h.kinds(id), "nothing is recorded")
	assert.Empty(t, h.moves.take(), "no session moves")
}

// Every extension is refused while the kill switch is on, kill_switch_on, of
// a running run and of a stopped one alike, previews included, recording
// nothing; while the view is unsettled, each answers ErrUnsettled (Decision
// 17; R52).
func TestEveryExtensionIsRefusedWhileTheKillSwitchIsOn(t *testing.T) {
	h := newHarness(t, limit(2))
	stopped := awaitingItsClose(h, "payments fail")
	running := inRound(h, 2)
	before := map[item.ID][]item.Kind{running: h.kinds(running), stopped: h.kinds(stopped)}
	view := h.c.Lifecycle().KillSwitch
	h.c.SetKillSwitchViewForTest(app.KillSwitch{On: true, Seq: 1, OnSeq: 1})
	for name, id := range map[string]item.ID{"a running run": running, "a stopped run": stopped} {
		for _, key := range []string{"extend-on", ""} {
			_, err := h.c.Extend(t.Context(), id, tokens(1_000), as(administrator, key))

			assert.Equal(t, app.CodeKillSwitchOn, refusal(err), "%s (key %q): %v", name, key, err)
		}
		assert.Equal(t, before[id], h.kinds(id), "%s: nothing is recorded", name)
	}
	h.c.SetKillSwitchViewForTest(view)

	unknownOn(t, h, false)

	for name, id := range map[string]item.ID{"a running run": running, "a stopped run": stopped} {
		_, err := h.c.Extend(t.Context(), id, tokens(1_000), extending("extend-unsettled"))

		require.ErrorIs(t, err, app.ErrUnsettled, name)
		assert.Equal(t, before[id], h.kinds(id), "%s: nothing is recorded", name)
	}
	assert.Empty(t, h.moves.take(), "no session moves")
}

// An extension retried under its key extends once (Decision 17: by its key
// alone): the retry answers as the extension did, and one run_extended is
// recorded; the same key with another amount, of tokens or of wall clock,
// is refused ErrKeyReused (R98, I1).
func TestARetriedExtensionWithItsKeyExtendsOnce(t *testing.T) {
	h := newHarness(t)
	id := inRound(h, 2)

	first, err := h.c.Extend(t.Context(), id, tokens(1_000), extending("extend-1"))
	require.NoError(t, err)
	again, err := h.c.Extend(t.Context(), id, tokens(1_000), extending("extend-1"))

	require.NoError(t, err)
	assert.Equal(t, first, again, "as the extension answered")
	assert.Equal(t, 1, countOf(h, id, item.KindExtended), "one run_extended")
	for name, asked := range map[string]item.Amount{
		"another number of tokens":  tokens(2_000),
		"a wall clock named":        {Tokens: tokens(1_000).Tokens, WallClock: wallClock(time.Minute).WallClock},
		"rounds in place of tokens": rounds(1),
	} {
		_, err := h.c.Extend(t.Context(), id, asked, extending("extend-1"))

		require.ErrorIs(t, err, app.ErrKeyReused, name)
	}
	assert.Equal(t, 1, countOf(h, id, item.KindExtended), "still one")
}

// extensionAnswerLost is the records store whose appends of a run_extended
// commit, then fail with errAnswerLost, as when the database's answer is lost
// on its way back. Every other append answers.
type extensionAnswerLost struct{ *failing }

func (l extensionAnswerLost) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	committed, err := l.failing.Append(ctx, id, decide)
	if err != nil {
		return nil, err
	}
	for i := range committed {
		if committed[i].Body.Extended != nil {
			return nil, errAnswerLost
		}
	}
	return committed, nil
}

// extensionsLoseTheirAnswers has the harness's instance take over again,
// under the same epoch, with an extensionAnswerLost of the harness's records
// store as its records store, and the lookup after the next extension's lost
// answer failing once; the moves made so far are forgotten.
func extensionsLoseTheirAnswers(t *testing.T, h *harness, id item.ID) (blinked *bool) {
	t.Helper()
	ports := h.ports()
	ports.Records = extensionAnswerLost{h.records}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
	h.moves.take()
	blinked = new(bool)
	h.records.loads(func(records []item.Record) error {
		if !*blinked && len(records) > 0 && records[0].Item == id && records[len(records)-1].Body.Kind() == item.KindExtended {
			*blinked = true
			return errBlinked
		}
		return nil
	})
	return blinked
}

// An extension whose run_extended landed with its answer lost, and whose
// lookup failed too, answers the append's error and moves nothing; after the
// retry wait, the item's advance finds the record, and raises the open
// round's sessions (R54; Review Focus 1). Fails first: nothing raises them
// after a found record.
func TestAnExtensionFoundAfterALostAnswerMovesTheOpenRoundsSessions(t *testing.T) {
	h := newHarness(t)
	millionTokens(h)
	id := inRound(h, 2)
	budget := h.state(id).Run.Latest().Opened.Budget
	blinked := extensionsLoseTheirAnswers(t, h, id)

	_, err := h.c.Extend(t.Context(), id, tokens(500_000), extending("extend-1"))

	require.ErrorIs(t, err, errAnswerLost, "the outcome is not known yet")
	require.True(t, *blinked, "the lookup failed")
	require.Equal(t, item.KindExtended, lastKind(h, id), "it landed")
	assert.Empty(t, h.moves.take(), "nothing moves on an unknown outcome")

	h.clock.Advance(h.opts.Retry)

	assert.ElementsMatch(t, raisedTo(h, id, budget+500_000), h.moves.take(), "the retry raises them")
}

// An extension that commits before its round's end is what that end decides
// by (Review Focus 2): a run at its last round, an extension of 1 round, and
// the round's end decides another round, not budget_exhausted, which opens.
func TestAnExtensionBeforeARoundsEndIsReadByItsDecision(t *testing.T) {
	h := newHarness(t)
	id := inRound(h, 3)

	_, err := h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))
	require.NoError(t, err)
	h.round()

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 4, "a fourth round opened")
	assert.Equal(t, item.NextRound, s.Run.Rounds[2].Ended.Decision.Next)
	assert.Nil(t, s.Run.Stopped)
}

// An extension's preview shows the budget before, after and used, and no
// resume, recording nothing and moving nothing (R57).
func TestAnExtensionsPreviewShowsTheBudgetBeforeAndAfter(t *testing.T) {
	h := newHarness(t)
	millionTokens(h)
	id := inRound(h, 2)
	s := h.state(id)
	kinds := h.kinds(id)
	after := s.Run.Budget()
	after.Tokens += 500_000

	out, err := h.c.Extend(t.Context(), id, tokens(500_000), as(administrator, ""))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Consequences: app.Consequences{Budget: &app.BudgetChange{
		Before: s.Run.Budget(), After: after, Used: usedBy(s.Run),
	}}}, out)
	assert.False(t, out.Consequences.Resumes)
	assert.Equal(t, kinds, h.kinds(id), "nothing is recorded")
	assert.Empty(t, h.moves.take(), "nothing moves")
}

// An extension of an item with no run is refused not_started, whatever the
// amount (R62): queued, investigating before its run's creation, or awaiting
// its close not staffed; of a closed item, closed. Nothing is recorded.
func TestAnExtensionOfAnItemWithNoRunIsRefused(t *testing.T) {
	for _, c := range []struct {
		name string
		item func(h *harness) item.ID
		code string
	}{
		{"queued", func(h *harness) item.ID {
			h.open("checkout fails")
			return openLater(h, "payments fail")
		}, app.CodeNotStarted},
		{"investigating before its run's creation", func(h *harness) item.ID {
			id, _ := withoutItsRun(h)
			return id
		}, app.CodeNotStarted},
		{"awaiting its close not staffed", notStaffedAtTheCap, app.CodeNotStarted},
		{"closed", func(h *harness) item.ID { return closedAwaiting(h, "checkout fails") }, app.CodeClosed},
	} {
		h := newHarness(t, limit(1))
		id := c.item(h)
		kinds := h.kinds(id)
		for name, asked := range map[string]item.Amount{"a round": rounds(1), "too many rounds": rounds(99)} {
			for _, key := range []string{"extend-1", ""} {
				_, err := h.c.Extend(t.Context(), id, asked, as(administrator, key))

				assert.Equal(t, c.code, refusal(err), "%s, %s (key %q): %v", c.name, name, key, err)
			}
		}
		assert.Equal(t, kinds, h.kinds(id), "%s: nothing is recorded", c.name)
	}
}

// An extension while a round's decided stop is not recorded yet answers
// ErrStopping and records nothing (R69; the plan review's I5): the run's last
// round ends deciding budget_exhausted, and conclude fails at Boards.Assess;
// once the board reads again, the run stops as decided. Fails first: the
// extension records and answers changed, and the run stops anyway.
func TestAnExtensionWhileADecidedStopIsUnrecordedAnswersStopping(t *testing.T) {
	h := newHarness(t)
	id := inRound(h, 3)
	decided := false
	h.records.refuses(func(_ item.ID, drafts []item.Draft) error {
		for i := range drafts {
			if e := drafts[i].Body.RoundEnded; e != nil && e.Decision.Next == item.NextStop {
				decided = true
			}
		}
		return nil
	})
	h.store.loads(func([]board.Event) error {
		if decided {
			return errBlinked
		}
		return nil
	})
	h.round()
	s := h.state(id)
	require.True(t, s.Run.Concludes(), "the stop is decided, not recorded")
	kinds := h.kinds(id)

	for _, key := range []string{"extend-1", ""} {
		_, err := h.c.Extend(t.Context(), id, rounds(1), as(administrator, key))

		require.ErrorIs(t, err, app.ErrStopping, "key %q", key)
	}
	assert.Equal(t, kinds, h.kinds(id), "nothing is recorded")

	decided = false
	h.clock.Advance(h.opts.Retry)

	s = h.state(id)
	require.NotNil(t, s.Run.Stopped)
	assert.Equal(t, item.StopBudgetExhausted, s.Run.Stopped.Reason, "the run stops as decided")
}

// No session moves while the view of the kill switch is unsettled (R37,
// R54; the plan review's M4): an extension lands with its answer lost and its
// lookup failing; the view is then unsettled, and the extension, sent again
// under its key, is found, and answers changed; no session moves, its item's
// retry included, until a read settles the view, and then each agent's moves
// once.
func TestNoSessionMovesWhileTheViewIsUnsettled(t *testing.T) {
	h := newHarness(t)
	millionTokens(h)
	id := inRound(h, 2)
	budget := h.state(id).Run.Latest().Opened.Budget
	extensionsLoseTheirAnswers(t, h, id)
	_, err := h.c.Extend(t.Context(), id, tokens(500_000), extending("extend-1"))
	require.ErrorIs(t, err, errAnswerLost)
	unknownOn(t, h, false)

	out, err := h.c.Extend(t.Context(), id, tokens(500_000), extending("extend-1"))

	require.NoError(t, err)
	assert.True(t, out.Changed, "found by its key")
	h.clock.Advance(h.opts.Retry)
	assert.Empty(t, h.moves.take(), "nothing moves while the view is unsettled")

	h.controls.failReads(false)
	h.clock.Advance(h.opts.Retry)

	assert.ElementsMatch(t, raisedTo(h, id, budget+500_000), h.moves.take(), "once settled, each agent's session moves once")
	h.clock.Advance(h.opts.Retry)
	assert.Empty(t, h.moves.take(), "and no more")
}
