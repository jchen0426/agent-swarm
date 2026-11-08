package app_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
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

// resumeEntries returns the resume entries of a run's board, in order.
func resumeEntries(h *harness, run board.RunID) []*board.Resume {
	h.t.Helper()
	var out []*board.Resume
	events := h.events(run)
	for i := range events {
		if r := events[i].Body.Resume; r != nil {
			out = append(out, r)
		}
	}
	return out
}

// roundsOnBoard returns the rounds a run's board's round entries open, in
// order.
func roundsOnBoard(h *harness, run board.RunID) []int {
	h.t.Helper()
	var out []int
	events := h.events(run)
	for i := range events {
		if r := events[i].Body.Round; r != nil {
			out = append(out, r.Round)
		}
	}
	return out
}

// entryAt returns the index of the first entry of a run's board that is
// holds true of, or -1.
func entryAt(h *harness, run board.RunID, is func(*board.Body) bool) int {
	h.t.Helper()
	events := h.events(run)
	for i := range events {
		if is(&events[i].Body) {
			return i
		}
	}
	return -1
}

// resumeOf reports whether drafts hold a board's resume entry.
func resumeOf(drafts []board.Draft) bool {
	for i := range drafts {
		if drafts[i].Body.Resume != nil {
			return true
		}
	}
	return false
}

// refusingResumes has the boards' store refuse every resume entry from now
// on, and returns how many it refused so far.
func refusingResumes(h *harness) (refused *atomic.Int32) {
	refused = new(atomic.Int32)
	h.store.refuses(func(_ board.RunID, drafts []board.Draft) error {
		if resumeOf(drafts) {
			refused.Add(1)
			return errRefused
		}
		return nil
	})
	return refused
}

// exhaustedRun opens a busy incident and plays its three rounds, the last of
// which decides budget_exhausted: its run stops so, and the item awaits its
// close.
func exhaustedRun(h *harness, title string) item.ID {
	h.t.Helper()
	busy(h)
	id := h.open(title)
	for range 3 {
		h.round()
	}
	s := h.state(id)
	require.NotNil(h.t, s.Run.Stopped)
	require.Equal(h.t, item.StopBudgetExhausted, s.Run.Stopped.Reason)
	require.Equal(h.t, item.StatusAwaitingClose, s.Status())
	return id
}

// requireResumedInto fails unless the run of id was resumed and its round n
// is open, recorded and on its board.
func requireResumedInto(t *testing.T, h *harness, id item.ID, n int) {
	t.Helper()
	s := h.state(id)
	require.Nil(t, s.Run.Stopped, "the run runs")
	require.Equal(t, item.StatusInvestigating, s.Status())
	require.Len(t, s.Run.Rounds, n, "round %d opened", n)
	require.Nil(t, s.Run.Rounds[n-1].Ended, "round %d is open", n)
	entries := roundsOnBoard(h, s.Run.ID)
	require.NotEmpty(t, entries)
	assert.Equal(t, n, entries[len(entries)-1], "round %d is on the board", n)
}

// noAgentKilled has a change, which no agent subscribes to, get its run,
// whose no_agents notice the board refuses, so that the run runs with no
// agent; then an on kills it, and an off follows. The item awaits its close.
func noAgentKilled(h *harness) item.ID {
	h.t.Helper()
	h.store.refuses(func(_ board.RunID, drafts []board.Draft) error {
		for i := range drafts {
			if n := drafts[i].Body.Notice; n != nil && n.Code == board.NoticeNoAgents {
				return errRefused
			}
		}
		return nil
	})
	id, err := h.c.Open(h.t.Context(), &app.OpenInput{Actor: administrator, Type: "change", Title: "a change", Source: "person"})
	require.NoError(h.t, err)
	_, err = h.c.SetKillSwitch(h.t.Context(), true, killing(0))
	require.NoError(h.t, err)
	_, err = h.c.SetKillSwitch(h.t.Context(), false, turningOff(1))
	require.NoError(h.t, err)
	s := h.state(id)
	require.NotNil(h.t, s.Run.Stopped)
	require.Equal(h.t, item.StopKilled, s.Run.Stopped.Reason)
	require.Equal(h.t, item.StatusAwaitingClose, s.Status())
	return id
}

// An extension of a stopped run that would not open a round is refused, in
// R60's order (Decision 16), its preview alike: a closed item, closed; one
// with no run, not_started; the kill switch on, kill_switch_on; the day's
// spend at the cap, daily_cap_reached; a run with no agent left,
// no_agents_left; a budget that the run's usage would still exhaust,
// would_stop_at_once (R56: wall clock alone, on a run whose rounds are
// spent); and past the limit and one, limit_reached. Each records nothing,
// and posts nothing. Fails first: the stopped case answers ErrInvalid.
func TestAnExtensionThatWouldNotOpenARoundIsRefused(t *testing.T) {
	for _, c := range []struct {
		code  string
		opts  []func(*app.Options)
		item  func(h *harness) item.ID
		asked item.Amount
	}{
		{code: app.CodeClosed, item: func(h *harness) item.ID { return closedAwaiting(h, "checkout fails") }},
		{code: app.CodeNotStarted, item: notStaffedAtTheCap},
		{code: app.CodeKillSwitchOn, item: func(h *harness) item.ID {
			id := awaitingItsClose(h, "checkout fails")
			h.c.SetKillSwitchViewForTest(app.KillSwitch{On: true, Seq: 1, OnSeq: 1})
			return id
		}},
		{code: app.CodeDailyCapReached, item: func(h *harness) item.ID {
			id := awaitingItsClose(h, "checkout fails")
			h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })
			return id
		}},
		{code: app.CodeNoAgentsLeft, item: noAgentKilled},
		{code: app.CodeWouldStopAtOnce, asked: wallClock(time.Minute), item: func(h *harness) item.ID { return exhaustedRun(h, "checkout fails") }},
		{code: app.CodeLimitReached, opts: []func(*app.Options){limit(1)}, item: func(h *harness) item.ID {
			id := awaitingItsClose(h, "checkout fails")
			openLater(h, "payments fail")
			q := openLater(h, "orders fail")
			_, err := h.c.Promote(h.t.Context(), q, promoting("promote-1"))
			require.NoError(h.t, err)
			require.Equal(h.t, 2, h.c.Lifecycle().Investigating, "the limit and one")
			return id
		}},
	} {
		h := newHarness(t, c.opts...)
		id := c.item(h)
		asked := c.asked
		if asked == (item.Amount{}) {
			asked = rounds(1)
		}
		kinds, carried := h.kinds(id), h.carried()
		for _, key := range []string{"extend-1", ""} {
			_, err := h.c.Extend(t.Context(), id, asked, as(administrator, key))

			assert.Equal(t, c.code, refusal(err), "%s (key %q): %v", c.code, key, err)
		}
		assert.Equal(t, kinds, h.kinds(id), "%s: nothing is recorded", c.code)
		assert.Equal(t, carried, h.carried(), "%s: nothing is recorded or posted", c.code)
	}
}

// A resume takes the items investigating to the limit and one at most
// (Decision 16, the controller's call): with a limit of 1, A awaiting its
// close and two items investigating, A's resume is refused limit_reached;
// once one of them stops, A resumes, and opens its round 2.
func TestAResumePastTheLimitAndOneIsRefused(t *testing.T) {
	h := newHarness(t, limit(1))
	a := awaitingItsClose(h, "checkout fails")
	openLater(h, "payments fail")
	c := openLater(h, "orders fail")
	_, err := h.c.Promote(t.Context(), c, promoting("promote-1"))
	require.NoError(t, err)
	require.Equal(t, app.Lifecycle{Limit: 1, Investigating: 2}, h.c.Lifecycle())
	kinds := h.kinds(a)

	_, err = h.c.Extend(t.Context(), a, rounds(1), extending("extend-1"))

	assert.Equal(t, app.CodeLimitReached, refusal(err), "%v", err)
	assert.Equal(t, kinds, h.kinds(a), "nothing is recorded")

	stopQuiet(h, c)
	require.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1}, h.c.Lifecycle())

	out, err := h.c.Extend(t.Context(), a, rounds(1), extending("extend-2"))

	require.NoError(t, err)
	assert.True(t, out.Changed)
	assert.True(t, out.Consequences.Resumes)
	assert.Equal(t, 2, out.Consequences.Investigating)
	assert.Equal(t, 1, out.Consequences.Limit)
	requireResumedInto(t, h, a, 2)
}

// A resume of a run that is stopping answers ErrStopping and records nothing
// (Contract C16; R69): a run whose stop is recorded and whose finish failed
// at the board, its stop entry refused; and a run whose round decided a stop
// not yet recorded, its conclude failing at the board.
func TestAResumeOfARunThatIsStoppingAnswersTryAgain(t *testing.T) {
	t.Run("its stop recorded, its finish failing", func(t *testing.T) {
		h := newHarness(t)
		h.store.refuses(func(_ board.RunID, drafts []board.Draft) error {
			if stopEntryOf(drafts) {
				return errRefused
			}
			return nil
		})
		id := h.open("checkout fails")
		h.round()
		s := h.state(id)
		require.NotNil(t, s.Run.Stopped, "its stop is recorded")
		require.Equal(t, item.StatusInvestigating, s.Status(), "its finish has not ended")
		kinds := h.kinds(id)

		for _, key := range []string{"extend-1", ""} {
			_, err := h.c.Extend(t.Context(), id, rounds(1), as(administrator, key))

			require.ErrorIs(t, err, app.ErrStopping, "key %q", key)
		}
		assert.Equal(t, kinds, h.kinds(id), "nothing is recorded")
	})
	t.Run("its round's decided stop not yet recorded", func(t *testing.T) {
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
		require.True(t, h.state(id).Run.Concludes())
		kinds := h.kinds(id)

		_, err := h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))

		require.ErrorIs(t, err, app.ErrStopping)
		assert.Equal(t, kinds, h.kinds(id), "nothing is recorded")
	})
}

// A resumed run whose board refuses its resume entry leaves the item
// investigating with no round, its step tried again (Decision 16); a close
// then closes it, its run stopped closed, and the run is dropped: its step is
// not tried again.
func TestAResumedRunWhoseBoardRefusesItsResumeCanStillBeClosed(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	refused := refusingResumes(h)

	_, err := h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))

	require.NoError(t, err)
	s := h.state(id)
	require.Equal(t, item.StatusInvestigating, s.Status())
	require.Len(t, s.Run.Rounds, 1, "no round opened")
	tried := refused.Load()
	require.Positive(t, tried)
	h.clock.Advance(h.opts.Retry)
	require.Greater(t, refused.Load(), tried, "its step is tried again")

	_, err = h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	s = h.state(id)
	assert.Equal(t, item.StatusClosed, s.Status())
	require.NotNil(t, s.Run.Stopped)
	assert.Equal(t, item.StopClosed, s.Run.Stopped.Reason)
	assert.Len(t, s.Run.Rounds, 1, "no round opened")
	tried = refused.Load()
	h.clock.Advance(3 * h.opts.Retry)
	assert.Equal(t, tried, refused.Load(), "the run is dropped: nothing is tried again")
}

// An extension resumes a run that the kill switch killed, once the switch is
// off (Decision 16): the on kills the run in round 1; the off; an extension
// resumes it, recording the view's latest row, 2: the board's resume entry,
// then round 2, its turns started; and the run owes no kill.
func TestAnExtensionResumesAKilledRunOnceTheKillSwitchIsOff(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	require.Equal(t, item.StopKilled, h.state(id).Run.Stopped.Reason)
	_, err = h.c.SetKillSwitch(t.Context(), false, turningOff(1))
	require.NoError(t, err)
	h.turns.take()

	out, err := h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))

	require.NoError(t, err)
	assert.True(t, out.Consequences.Resumes)
	requireResumedInto(t, h, id, 2)
	s := h.state(id)
	require.Len(t, s.Run.Extensions, 1)
	assert.Equal(t, int64(2), s.Run.Extensions[0].Extended.KillSwitchSeen, "the view's latest row")
	assert.False(t, s.Owes(1), "no owed kill")
	require.Len(t, resumeEntries(h, run), 1)
	assert.Less(t, entryAt(h, run, func(b *board.Body) bool { return b.Resume != nil }),
		entryAt(h, run, func(b *board.Body) bool { return b.Round != nil && b.Round.Round == 2 }), "the resume entry, then round 2")
	started := h.turns.take()
	require.Len(t, started, 2, "round 2's turns started")
	for _, turn := range started {
		assert.Equal(t, 2, turn.Round)
	}
}

// A close retried late under its key, after a reopen and a resume, answers
// as a repeat and leaves the run going (Plan 7b-1, Decisions 6 and 13): a
// close under K halts the run in round 1; a reopen; an extension resumes
// the run into round 2; the close retried under K records nothing, stops no
// turn, and round 2 goes on.
func TestALateCloseRetryAfterAReopenAndAResumeLeavesTheRunGoing(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	_, err := h.c.Close(t.Context(), id, closing("close-k"))
	require.NoError(t, err)
	_, err = h.c.Reopen(t.Context(), id, reopening("reopen-1"))
	require.NoError(t, err)
	h.turns.take()
	_, err = h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))
	require.NoError(t, err)
	requireResumedInto(t, h, id, 2)
	kinds, stopped := h.kinds(id), len(h.turns.stopped)

	out, err := h.c.Close(t.Context(), id, closing("close-k"))

	require.NoError(t, err)
	assert.True(t, out.Changed, "as a repeat")
	assert.Equal(t, kinds, h.kinds(id), "nothing is recorded")
	assert.Len(t, h.turns.stopped, stopped, "no turn is stopped")
	requireResumedInto(t, h, id, 2)
	played := h.play()
	require.Len(t, played, 2, "round 2's turns go on")
	for _, turn := range played {
		assert.Equal(t, 2, turn.Round)
	}
}

// A resume whose outcome is unknown counts its item toward the limit (R53;
// Review Focus 1): with a limit of 1, A awaiting its close with a stopped
// run, B investigating, and Q waiting, A's resume lands with its answer lost
// and the lookup after it fails. Until A's retry, the snapshot counts A, a
// promotion of Q is refused limit_reached, and the walk admits nothing; on
// its retry A opens its round 2, and two items investigate, never three.
// Fails first: the account holds A as awaiting its close, and Q's promotion
// lands, three investigating.
func TestAResumeWhoseOutcomeIsUnknownCountsTowardTheLimit(t *testing.T) {
	h := newHarness(t, limit(1))
	a := awaitingItsClose(h, "checkout fails")
	b := openLater(h, "payments fail")
	q := openLater(h, "orders fail")
	require.Equal(t, app.Lifecycle{Limit: 1, Investigating: 1, Waiting: []item.ID{q}}, h.c.Lifecycle())
	blinked := extensionsLoseTheirAnswers(t, h, a)

	_, err := h.c.Extend(t.Context(), a, rounds(1), extending("extend-1"))

	require.ErrorIs(t, err, errAnswerLost, "the outcome is not known yet")
	require.True(t, *blinked, "the lookup failed")
	require.Equal(t, item.KindExtended, lastKind(h, a), "it landed")
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 2, Waiting: []item.ID{q}}, h.c.Lifecycle(), "A counts")
	before := h.carried()

	_, err = h.c.Promote(t.Context(), q, promoting("promote-q"))

	assert.Equal(t, app.CodeLimitReached, refusal(err), "%v", err)
	assert.Equal(t, before, h.carried(), "nothing is recorded")
	h.clock.Advance(h.opts.Retry)
	requireResumedInto(t, h, a, 2)
	investigating := 0
	for _, id := range []item.ID{a, b, q} {
		if h.state(id).Status() == item.StatusInvestigating {
			investigating++
		}
	}
	assert.Equal(t, 2, investigating, "the limit and one, never three")
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(q), "Q waits")
	assert.Equal(t, app.Lifecycle{Limit: 1, Investigating: 2, Waiting: []item.ID{q}}, h.c.Lifecycle())
}

// An on after a resume halts the run before its round opens (Review Focus
// 5; the re-review's N5): a resume commits, and its resume post fails; an
// on-row comes; the step halts the run killed, with no round_opened after the
// resume. The records' latest stop is killed, while the board's stop entry is
// still the first one, repeated, with its old reason, quiet.
func TestAnOnAfterAResumeHaltsTheRunBeforeItsRoundOpens(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	run := h.state(id).Run.ID
	refusingResumes(h)
	_, err := h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))
	require.NoError(t, err)
	require.Len(t, h.state(id).Run.Rounds, 1, "no round opened")

	_, err = h.c.SetKillSwitch(t.Context(), true, killing(0))

	require.NoError(t, err)
	s := h.state(id)
	require.NotNil(t, s.Run.Stopped)
	assert.Equal(t, item.StopKilled, s.Run.Stopped.Reason, "the records' latest stop")
	assert.Equal(t, int64(1), s.Run.Stopped.Killed)
	assert.Len(t, s.Run.Rounds, 1, "no round opened")
	kinds := h.kinds(id)
	assert.NotContains(t, kinds[slices.Index(kinds, item.KindExtended):], item.KindRoundOpened, "no round_opened after the resume")
	assert.Equal(t, item.StatusAwaitingClose, s.Status())
	stops := stopEntries(h, run)
	require.Len(t, stops, 1, "the first stop entry, repeated")
	assert.Equal(t, string(item.StopQuiet), stops[0].Reason, "with its old reason")
}

// A resume of a run whose board never took its stop opens its next round (the
// re-review's N1; R73): a close of a running run whose closed step's stop
// post fails; a reopen; an extension: the resume lands, open posts no resume
// entry, and round 2 opens on the board. Fails first: the board refuses the
// resume as never stopped, and the step fails on every retry.
func TestAResumeOfARunWhoseBoardNeverTookItsStopOpensItsNextRound(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.store.refuses(func(_ board.RunID, drafts []board.Draft) error {
		if stopEntryOf(drafts) {
			return errRefused
		}
		return nil
	})
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)
	h.store.refuses(nil)
	require.Empty(t, stopEntries(h, run), "the board never took its stop")
	_, err = h.c.Reopen(t.Context(), id, reopening("reopen-1"))
	require.NoError(t, err)

	_, err = h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))

	require.NoError(t, err)
	assert.Empty(t, resumeEntries(h, run), "no resume entry")
	requireResumedInto(t, h, id, 2)
	assert.Equal(t, []int{1, 2}, roundsOnBoard(h, run))
}

// A resumed run whose round decided budget_exhausted opens a round rather than
// concluding again (the plan review's C1; R67, R74): its next step opens
// round 4, with no second run_stopped and no conclude. Fails first: runStep
// takes its conclude case.
func TestAResumedRunOfADecidedStopOpensARoundRatherThanConcluding(t *testing.T) {
	h := newHarness(t)
	id := exhaustedRun(h, "checkout fails")

	_, err := h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))

	require.NoError(t, err)
	requireResumedInto(t, h, id, 4)
	assert.Equal(t, 1, countOf(h, id, item.KindRunStopped), "no second run_stopped")
	assert.Len(t, stopEntries(h, h.state(id).Run.ID), 1, "no second stop entry")
}

// A resume of a run killed before its first round opens round 1 (the plan
// review's C1; the re-review's N3): its platform evidence is refused, so that
// round 1 never opens; an on kills the run; the off; a resume: the board's
// resume entry comes first, then the trigger's platform evidence, then
// round 1.
func TestAResumeOfARunKilledBeforeItsFirstRoundOpensRoundOne(t *testing.T) {
	h := newHarness(t)
	h.store.refuses(func(_ board.RunID, drafts []board.Draft) error {
		for i := range drafts {
			if drafts[i].Body.Evidence != nil {
				return errRefused
			}
		}
		return nil
	})
	id := h.open("checkout fails")
	s := h.state(id)
	require.NotNil(t, s.Run)
	require.Empty(t, s.Run.Rounds, "no round opened")
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	require.Equal(t, item.StopKilled, h.state(id).Run.Stopped.Reason)
	h.store.refuses(nil)
	_, err = h.c.SetKillSwitch(t.Context(), false, turningOff(1))
	require.NoError(t, err)

	_, err = h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))

	require.NoError(t, err)
	requireResumedInto(t, h, id, 1)
	run := s.Run.ID
	resume := entryAt(h, run, func(b *board.Body) bool { return b.Resume != nil })
	evidence := entryAt(h, run, func(b *board.Body) bool { return b.Evidence != nil })
	round := entryAt(h, run, func(b *board.Body) bool { return b.Round != nil })
	require.NotEqual(t, -1, resume)
	assert.Less(t, resume, evidence, "the resume entry first")
	assert.Less(t, evidence, round, "then the evidence, then round 1")
}

// A resume where no kill switch row was ever written resumes (the plan
// review's I3): the table empty, the view's Seq 0, the resume lands, its
// run_extended with no killSwitchSeen field, and round 2 opens.
func TestAResumeWithNoKillSwitchRowEverWrittenResumes(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	require.Zero(t, h.controls.rows(t), "no row was ever written")

	_, err := h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))

	require.NoError(t, err)
	requireResumedInto(t, h, id, 2)
	records, err := h.records.Load(t.Context(), id)
	require.NoError(t, err)
	var e *item.Extended
	for i := range records {
		if records[i].Body.Extended != nil {
			e = records[i].Body.Extended
		}
	}
	require.NotNil(t, e)
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "killSwitchSeen")
}

// A resume after a halt of a round its board never started opens its next
// round (the plan review's I4; R68): round 2's round_opened is recorded, its
// StartRound failing; a close halts the run; a reopen and a resume: open
// posts the resume entry, StartRound(2), then round 3 opens and its
// StartRound(3) is taken. Fails first: the board refuses round 3 for ever.
func TestAResumeAfterAHaltOfARoundTheBoardNeverStartedOpensItsNextRound(t *testing.T) {
	h := newHarness(t)
	busy(h)
	h.store.refuses(func(_ board.RunID, drafts []board.Draft) error {
		for i := range drafts {
			if r := drafts[i].Body.Round; r != nil && r.Round == 2 {
				return errRefused
			}
		}
		return nil
	})
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.round()
	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2, "round 2 is recorded")
	require.Equal(t, []int{1}, roundsOnBoard(h, run), "the board never started it")
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)
	h.store.refuses(nil)
	_, err = h.c.Reopen(t.Context(), id, reopening("reopen-1"))
	require.NoError(t, err)

	_, err = h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))

	require.NoError(t, err)
	requireResumedInto(t, h, id, 3)
	assert.Equal(t, []int{1, 2, 3}, roundsOnBoard(h, run))
	assert.Less(t, entryAt(h, run, func(b *board.Body) bool { return b.Resume != nil }),
		entryAt(h, run, func(b *board.Body) bool { return b.Round != nil && b.Round.Round == 2 }), "the resume entry, then round 2's")
}

// A resumed run's second stop is finished, and the item awaits its close
// again (Decision 16): the resumed round decides budget_exhausted; finish
// posts a second stop entry, records a second item_awaiting_close, and arms
// its auto-close.
func TestAResumedRunsSecondStopIsFinishedAndAwaitsItsClose(t *testing.T) {
	h := newHarness(t)
	id := exhaustedRun(h, "checkout fails")
	run := h.state(id).Run.ID
	_, err := h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))
	require.NoError(t, err)
	requireResumedInto(t, h, id, 4)

	h.round()

	s := h.state(id)
	require.NotNil(t, s.Run.Stopped)
	assert.Equal(t, item.StopBudgetExhausted, s.Run.Stopped.Reason)
	assert.Equal(t, item.StatusAwaitingClose, s.Status())
	assert.Equal(t, 2, countOf(h, id, item.KindAwaitingClose), "a second item_awaiting_close")
	stops := stopEntries(h, run)
	require.Len(t, stops, 2, "a second stop entry")
	assert.Equal(t, string(item.StopBudgetExhausted), stops[1].Reason)
	assert.Contains(t, h.clock.armed(), s.AwaitingSince.Add(closesAfter), "its auto-close is armed")
}

// An auto-close timer armed before a resume closes nothing (Plan 7b-1,
// Decision 8: a stale timer records nothing): an item's auto-close timer is
// armed, at a minute; then a resume; the timer fires, and records nothing.
func TestAnAutoCloseArmedBeforeAResumeClosesNothing(t *testing.T) {
	h := newHarness(t)
	h.revs.set(h.revision(strings.Repeat("a", 40), func(files map[string][]byte) {
		files["work-item-types/incident.yaml"] = append(files["work-item-types/incident.yaml"], "  autoClose: {after: 1m}\n"...)
	}))
	id := awaitingItsClose(h, "checkout fails")
	due := h.state(id).AwaitingSince.Add(time.Minute)
	require.Contains(t, h.clock.armed(), due, "its auto-close is armed")
	_, err := h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))
	require.NoError(t, err)
	requireResumedInto(t, h, id, 2)
	kinds := h.kinds(id)

	advanceTo(h, due)

	assert.Equal(t, kinds, h.kinds(id), "nothing is recorded")
	assert.Equal(t, item.StatusInvestigating, h.state(id).Status())
}

// An on after a resume whose outcome is unknown names the run it kills (the
// adversarial review's I1): A awaits its close with a stopped run, and its
// resume lands with its answer lost, the lookup after it failing too, so
// the account holds A as stopped, though unsure. The on's advance finds the
// resume and halts the run killed; its answer names the run among its stops.
// A preview before the on refreshes A first, and names the run too. Fails
// first: the account's state of A owes no kill, so neither names it.
func TestAnOnAfterAResumeWhoseOutcomeIsUnknownNamesTheRunItKills(t *testing.T) {
	for _, previewed := range []bool{false, true} {
		h := newHarness(t)
		a := awaitingItsClose(h, "checkout fails")
		run := h.state(a).Run.ID
		blinked := extensionsLoseTheirAnswers(t, h, a)
		_, err := h.c.Extend(t.Context(), a, rounds(1), extending("extend-1"))
		require.ErrorIs(t, err, errAnswerLost)
		require.True(t, *blinked, "the lookup failed")
		stops := []app.RunStop{{Item: a, Run: run, Round: 1, Reason: item.StopKilled}}

		if previewed {
			preview, err := h.c.SetKillSwitch(t.Context(), true, previewing())

			require.NoError(t, err)
			assert.Equal(t, stops, preview.Consequences.Stops, "the preview names it")
		}
		out, err := h.c.SetKillSwitch(t.Context(), true, killing(0))

		require.NoError(t, err)
		assert.Equal(t, stops, out.Stopped, "the answer names it (previewed %v)", previewed)
		s := h.state(a)
		require.NotNil(t, s.Run.Stopped)
		assert.Equal(t, item.StopKilled, s.Run.Stopped.Reason)
	}
}

// An unsure item that the on does not stop is named nowhere: A, killed by an
// earlier on and resumed after the off with its answer lost and its lookup
// failing, did not land its resume; a second on finds A's run stopped by the
// first, and its answer does not name it (the adversarial review's I1).
func TestAnOnDoesNotNameAnUnsureRunItDidNotStop(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	_, err = h.c.SetKillSwitch(t.Context(), false, turningOff(1))
	require.NoError(t, err)
	refused, blinked := false, false
	h.records.refuses(func(id item.ID, drafts []item.Draft) error {
		for i := range drafts {
			if id == a && drafts[i].Body.Extended != nil {
				refused = true
				return errRefused
			}
		}
		return nil
	})
	h.records.loads(func(records []item.Record) error {
		if refused && !blinked && len(records) > 0 && records[0].Item == a {
			blinked = true
			return errBlinked
		}
		return nil
	})
	_, err = h.c.Extend(t.Context(), a, rounds(1), extending("extend-1"))
	require.ErrorIs(t, err, errRefused)
	require.True(t, blinked)
	require.Equal(t, 1, h.c.Lifecycle().Investigating, "A counts, unsure")
	h.records.refuses(nil)

	out, err := h.c.SetKillSwitch(t.Context(), true, killing(2))

	require.NoError(t, err)
	assert.Empty(t, out.Stopped, "its stop was the first on's")
	assert.Empty(t, out.Unstaffed)
}

// A resume whose stop spent past its latest round's end is judged by the
// larger spend (the adversarial review's M1; R56): round 1 ends with 95% of
// the tokens spent; round 2 never opens; a close halts the run between
// rounds, its stop recording 102%; a reopen; a resume adding 2% is refused
// would_stop_at_once, and one adding 5% would resume. Fails first: the
// latest round's spend alone was read, and the 2% resumed.
func TestAResumeIsJudgedByItsStopsSpendWhenLarger(t *testing.T) {
	h := newHarness(t)
	busy(h)
	id := h.open("checkout fails")
	run := string(h.state(id).Run.ID)
	h.spend.set(func(s *spend) { s.spent[run] = 85_500 })
	h.records.refuses(func(_ item.ID, drafts []item.Draft) error {
		for i := range drafts {
			if o := drafts[i].Body.RoundOpened; o != nil && o.Round == 2 {
				return errRefused
			}
		}
		return nil
	})
	h.round()
	s := h.state(id)
	require.Len(t, s.Run.Rounds, 1)
	require.Equal(t, int64(85_500), s.Run.Rounds[0].Ended.Spent)
	h.spend.set(func(s *spend) { s.spent[run] = 91_800 })
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)
	h.records.refuses(nil)
	require.Equal(t, int64(91_800), h.state(id).Run.Stopped.Spent)
	_, err = h.c.Reopen(t.Context(), id, reopening("reopen-1"))
	require.NoError(t, err)
	kinds := h.kinds(id)

	_, err = h.c.Extend(t.Context(), id, tokens(1_800), extending("extend-1"))

	assert.Equal(t, app.CodeWouldStopAtOnce, refusal(err), "%v", err)
	assert.Equal(t, kinds, h.kinds(id), "nothing is recorded")
	out, err := h.c.Extend(t.Context(), id, tokens(4_500), as(administrator, ""))
	require.NoError(t, err)
	assert.True(t, out.Consequences.Resumes, "5% more would resume")
}

// A resume that landed with its answer lost, found by the lookup, whose
// re-read for its answer fails, still answers the items investigating and the
// limit (the adversarial review's M2; R57).
func TestAResumeFoundWhoseReReadFailsAnswersTheItemsInvestigating(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	ports := h.ports()
	ports.Records = extensionAnswerLost{h.records}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
	// Once round 2's turns start, the advance is done: the next load, the
	// answer's re-read, fails once.
	blink := false
	h.turns.mu.Lock()
	h.turns.fail = func(*app.Turn) error { blink = true; return nil }
	h.turns.mu.Unlock()
	blinked := false
	h.records.loads(func(records []item.Record) error {
		if blink && !blinked && len(records) > 0 && records[0].Item == id {
			blinked = true
			return errBlinked
		}
		return nil
	})

	out, err := h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))

	require.NoError(t, err)
	require.True(t, blinked, "the re-read failed")
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Resumes: true, Investigating: 1, Limit: 3}}, out)
	requireResumedInto(t, h, id, 2)
}
