package app_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
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

// closing is a close through the admin API, by its admin, under key, with no
// reason.
func closing(key string) *app.Act {
	return &app.Act{Actor: app.Actor{Subject: "admin", Admin: true}, Key: key}
}

// keyHash is how a record keeps an act's key: "sha256:" and the key's
// hexadecimal SHA-256.
func keyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// stopEntries returns the stop entries of a run's board, in order.
func stopEntries(h *harness, run board.RunID) []*board.Stop {
	h.t.Helper()
	var out []*board.Stop
	events := h.events(run)
	for i := range events {
		if st := events[i].Body.Stop; st != nil {
			out = append(out, st)
		}
	}
	return out
}

// An item left queued, as its start found no revision, is closed by a person
// (Decision 6): one append, item_closed, by the admin, with the act's
// request, and the item is closed. It waits as a closed item, though it never
// awaited its close: no step of it is tried again, and once a revision is
// current it never starts, at a takeover either.
func TestClosingAQueuedItemClosesIt(t *testing.T) {
	h := newHarness(t)
	rev := h.revs.rev
	// The opening reads the revision; its start reads none.
	h.revs.next(nil)
	id := h.open("checkout fails")
	require.Equal(t, item.StatusQueued, h.state(id).Status())

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Closes: []item.ID{id}}}, out)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindClosed}, h.kinds(id))
	s := h.state(id)
	assert.Equal(t, item.StatusClosed, s.Status())
	assert.Equal(t, "admin", s.Closed.By)
	require.NotNil(t, s.Closed.Request)
	assert.Equal(t, keyHash("close-1"), s.Closed.Request.Key)
	assert.Zero(t, h.clock.pending(), "no step of the closed item is tried again")
	h.revs.set(rev)
	h.clock.Advance(3 * h.opts.Retry)
	h.crash()
	require.NoError(t, h.boot())
	assert.Zero(t, h.clock.pending(), "nor after a takeover")
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindClosed}, h.kinds(id), "it never starts")
	assert.Empty(t, h.turns.started)
}

// A close of an item whose run is in a round stops the run mid-round
// (Decisions 3 and 6): one append ends the round stopped, with the classes
// the instance knew, alpha's turn ended and beta's interrupted, stops the run
// closed, and closes the item. Then the run's turns stop, its sessions end,
// its upstream sessions close, and its board's stop entry says closed.
func TestClosingAnInvestigatingItemStopsItsRunMidRound(t *testing.T) {
	h := newHarness(t)
	h.scripts[runtime.Name] = ending("")
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	played := h.play()
	appends := 0
	h.records.set(func(int) error {
		appends++
		return nil
	})

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{
		Stops: []app.RunStop{{Item: id, Run: run, Round: 1, Reason: item.StopClosed}}, Closes: []item.ID{id},
	}}, out)
	assert.Equal(t, 1, appends, "the halt's records and the close in one append")
	assert.Equal(t, []item.Kind{
		item.KindOpened, item.KindStarted, item.KindRunCreated, item.KindRoundOpened, item.KindRoundEnded, item.KindRunStopped, item.KindClosed,
	}, h.kinds(id))
	s := h.state(id)
	ended := s.Run.Rounds[0].Ended
	assert.Equal(t, item.EndedByStopped, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnInterrupted}, ended.Turns)
	assert.Equal(t, []string{release.Name}, ended.TurnEnds)
	assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopClosed}, ended.Decision)
	assert.Equal(t, item.StopClosed, s.Run.Stopped.Reason)
	assert.Equal(t, item.StatusClosed, s.Status())
	for _, turn := range played {
		assert.Contains(t, h.turns.stopped, string(run)+"/"+turn.Agent)
		_, err := h.registry.MCP().Session(t.Context(), turn.MCP.Reveal())
		require.ErrorIs(t, err, actionsapp.ErrNoSession)
	}
	assert.Contains(t, h.upstreams.closedRuns(), run)
	stops := stopEntries(h, run)
	require.Len(t, stops, 1)
	assert.Equal(t, "closed", stops[0].Reason)
}

// A close of an item awaiting its close, its run stopped quiet, records
// item_closed alone (Decision 6).
func TestClosingAnItemAwaitingItsCloseClosesIt(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	h.round()
	require.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
	awaiting := h.kinds(id)

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Closes: []item.ID{id}}}, out)
	assert.Equal(t, append(slices.Clone(awaiting), item.KindClosed), h.kinds(id))
	assert.Equal(t, item.StatusClosed, h.state(id).Status())
}

// A close of a closed item changes nothing, and records nothing (Decision
// 13).
func TestClosingAClosedItemChangesNothing(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)
	closed, before := h.kinds(id), h.carried()

	out, err := h.c.Close(t.Context(), id, closing("close-2"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{}, out)
	assert.Equal(t, closed, h.kinds(id))
	assert.Equal(t, before, h.carried(), "no record and no board entry")
}

// No state of an item refuses a close (Decision 6): an item whose run stopped
// quiet, and whose board refuses its stop entry, so that its finish keeps
// failing, is closed all the same. The closed step's post of the stop entry
// fails, is logged once, and is not tried again: no retry of the item is
// armed.
func TestAnItemWhoseStopEntryCannotBePostedCanStillBeClosed(t *testing.T) {
	steps := &stepErrors{}
	h := newHarness(t, func(o *app.Options) { o.Log = slog.New(steps) })
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.play()
	errRefused := errors.New("the board refuses the stop entry")
	h.store.set(func(int) error { return errRefused })
	h.clock.Advance(h.opts.Grace)
	h.clock.Advance(h.opts.Retry)
	require.Equal(t, item.KindRunStopped, lastKind(h, id), "its finish keeps failing")
	before := len(steps.kept())
	require.GreaterOrEqual(t, before, 2)

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Closes: []item.ID{id}}}, out)
	assert.Equal(t, item.KindClosed, lastKind(h, id))
	assert.Equal(t, item.StatusClosed, h.state(id).Status())
	failed := steps.kept()[before:]
	require.Len(t, failed, 1, "the closed step's post failed, logged once")
	require.ErrorIs(t, failed[0], errRefused)
	assert.Zero(t, h.clock.pending(), "no retry of the item is armed")
	h.clock.Advance(3 * h.opts.Retry)
	assert.Len(t, steps.kept(), before+1, "the post is not tried again")
	assert.Empty(t, stopEntries(h, run))
}

// A close of an item whose run's stop is recorded, though its finish did not
// end, as its item_awaiting_close append failed, records item_closed alone,
// and no halt's records (Decision 6); the closed step posts the stop entry,
// which the board, holding it from the finish, takes once.
func TestClosingAnItemWhoseRunStoppedBeforeItsFinishEndedClosesIt(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	failing := true
	h.records.set(func(int) error {
		if failing && lastKind(h, id) == item.KindRunStopped {
			return errBlinked
		}
		return nil
	})
	h.round()
	require.Equal(t, item.KindRunStopped, lastKind(h, id), "its item_awaiting_close append failed")
	failing = false
	stopped := h.kinds(id)

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Closes: []item.ID{id}}}, out)
	assert.Equal(t, append(slices.Clone(stopped), item.KindClosed), h.kinds(id), "item_closed alone")
	stops := stopEntries(h, run)
	require.Len(t, stops, 1)
	assert.Equal(t, "quiet", stops[0].Reason)
}

// A closed step whose post meets an event of a later version on the run's
// board skips the item, as every step that meets one does (Decision 19): the
// item is logged once, and nothing is tried again. (The plan's.)
func TestAClosedStepThatMeetsALaterBoardSkipsTheItem(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	id := h.open("checkout fails")
	h.round()
	require.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
	appendLaterEvent(t, h, h.state(id).Run.ID)

	_, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.Equal(t, item.StatusClosed, h.state(id).Status())
	assert.Equal(t, []item.ID{id}, h.c.Skipped())
	requireSkippedOnce(t, logs, id)
	assert.Zero(t, h.clock.pending(), "nothing is tried again")
}

// tracedSessions, tracedTurns and tracedUpstreams are an instance's live
// sessions, turns and upstream sessions, which note, in one order, each run
// whose sessions end, each turn stopped, and each run whose upstream sessions
// close.
type tracedSessions struct {
	app.Sessions
	order *trace
}

func (s tracedSessions) EndRun(run board.RunID) {
	s.order.note("end " + string(run))
	s.Sessions.EndRun(run)
}

type tracedTurns struct {
	app.Turns
	order *trace
}

func (t tracedTurns) Stop(run board.RunID, agent string) {
	t.order.note("stop " + string(run) + "/" + agent)
	t.Turns.Stop(run, agent)
}

type tracedUpstreams struct {
	app.UpstreamSessions
	order *trace
}

func (u tracedUpstreams) CloseRun(run board.RunID) {
	u.order.note("close " + string(run))
	u.UpstreamSessions.CloseRun(run)
}

// tracing has the harness's instance take over again, under the same epoch,
// with its sessions, its turns and its upstream sessions noting, in one
// order, what they end.
func tracing(t *testing.T, h *harness) *trace {
	t.Helper()
	order := &trace{}
	ports := h.ports()
	ports.Sessions = tracedSessions{Sessions: ports.Sessions, order: order}
	ports.Turns = tracedTurns{Turns: ports.Turns, order: order}
	ports.UpstreamSessions = tracedUpstreams{UpstreamSessions: ports.UpstreamSessions, order: order}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
	return order
}

// Once a close commits, it drops the run's state (Decision 3): the run's
// sessions end, then each of its agents' turns stops, then its upstream
// sessions close; and the coordinator forgets the run, so that the end of a
// turn of it, and the report of a launch the stop cancelled, record nothing
// and arm no retry.
func TestACloseEndsTheRunsSessionsTurnsAndUpstreamSessionsBeforeItForgetsTheRun(t *testing.T) {
	h := newHarness(t)
	order := tracing(t, h)
	h.turns.pending = true
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	alpha, beta := h.last(release.Name), h.last(runtime.Name)
	// Alpha's turn runs; beta's launch is pending.
	h.launch(alpha, "", nil)

	_, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	seen := order.seen()
	require.GreaterOrEqual(t, len(seen), 4)
	assert.Equal(t, []string{
		"end " + string(run), "stop " + string(run) + "/" + release.Name, "stop " + string(run) + "/" + runtime.Name, "close " + string(run),
	}, seen[:4], "the close's drop; the closed step drops the run again, which repeats safely")
	kinds, events, started := h.kinds(id), h.events(run), len(h.turns.started)
	h.c.Exited(t.Context(), app.Exit{Run: run, Agent: release.Name, Round: 1, Turn: alpha.ID, Class: item.TurnEnded})
	h.c.Launched(t.Context(), launchOf(beta, app.ReasonCancelled, errors.New("the stop cancelled the launch")))
	assert.Equal(t, kinds, h.kinds(id), "nothing is recorded")
	assert.Equal(t, events, h.events(run), "nothing is posted")
	assert.Zero(t, h.clock.pending(), "no retry is armed")
	h.clock.Advance(3 * h.opts.Retry)
	assert.Len(t, h.turns.started, started, "no turn starts again")
}

// A close drops the run itself, before it advances the item, so that an
// advance that fails cannot leave a turn to start (Decisions 3 and 13): here
// beta's start failed, and its retry waits, when the close commits; the
// advance after the close fails at its load. Once the clock passes the retry
// wait, beta's start is not tried again. The guard at begin does not cover a
// close, whose run owes no kill.
func TestACloseWhoseAdvanceFailsStartsNoTurn(t *testing.T) {
	h := newHarness(t)
	starts := 0
	h.turns.fail = func(turn *app.Turn) error {
		if turn.Agent != runtime.Name {
			return nil
		}
		starts++
		return errors.New("the runner cannot be reached")
	}
	id := h.open("checkout fails")
	require.Equal(t, 1, starts, "beta's start failed, and waits for its retry")
	failed := false
	h.records.loads(func(records []item.Record) error {
		if !failed && records[len(records)-1].Body.Kind() == item.KindClosed {
			failed = true
			return errBlinked
		}
		return nil
	})

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	require.True(t, out.Changed)
	require.True(t, failed, "the advance after the close failed at its load")
	h.clock.Advance(h.opts.Retry)
	h.clock.Advance(h.opts.Retry)
	assert.Equal(t, 1, starts, "beta's start is not tried again")
	assert.Equal(t, item.StatusClosed, h.state(id).Status())
}

// A close at the instant a round's end decided its stop, which is not
// recorded yet, as the board's assessment failed, keeps the decided reason
// (Decision 3): it records run_stopped quiet, as the round decided, and
// closes the item.
func TestACloseAfterAStopWasDecidedKeepsTheDecidedReasonAndClosesTheItem(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	// The board cannot be read once the round's end is recorded, so the run's
	// stop, which reads its assessment, is not recorded.
	h.store.loads(func([]board.Event) error {
		if lastKind(h, id) == item.KindRoundEnded {
			return errors.New("the board's store is down")
		}
		return nil
	})
	h.round()
	s := h.state(id)
	require.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopQuiet}, s.Run.Rounds[0].Ended.Decision)
	require.Nil(t, s.Run.Stopped, "the decided stop is not recorded")

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{
		Stops: []app.RunStop{{Item: id, Run: run, Round: 1, Reason: item.StopQuiet}}, Closes: []item.ID{id},
	}}, out)
	kinds := h.kinds(id)
	assert.Equal(t, []item.Kind{item.KindRoundEnded, item.KindRunStopped, item.KindClosed}, kinds[len(kinds)-3:])
	s = h.state(id)
	assert.Equal(t, item.StopQuiet, s.Run.Stopped.Reason)
	assert.Equal(t, item.StatusClosed, s.Status())
}

// A board that cannot be read does not stop a halt (Decision 3): the close
// records it with no conclusion, though the board has one, and with the
// classes of the snapshot it took, alpha's turn ended and beta's
// interrupted.
func TestAHaltWhoseBoardCannotBeReadStillRecords(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name], h.scripts[runtime.Name] = hypothesis, ending("")
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.play()
	a, err := h.boards.Assess(t.Context(), run, time.Time{})
	require.NoError(t, err)
	require.NotEmpty(t, a.Conclusion, "the board, read, has a conclusion")
	h.store.loads(func([]board.Event) error { return errors.New("the board's store is down") })

	_, err = h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	s := h.state(id)
	ended := s.Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, item.EndedByStopped, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnInterrupted}, ended.Turns)
	assert.Equal(t, item.StopClosed, s.Run.Stopped.Reason)
	assert.Empty(t, s.Run.Stopped.Conclusion)
	assert.Equal(t, item.StatusClosed, s.Status())
}

// A halt with no snapshot of its run classes the round's agents from the
// board (Decision 3): ended for those whose turn end it holds, interrupted
// for the rest. Here the run is not held in memory, as after a takeover whose
// advance of the item failed at its load.
func TestAHaltWithoutASnapshotClassesItsTurnsFromTheBoard(t *testing.T) {
	h := newHarness(t)
	h.scripts[runtime.Name] = ending("")
	id := h.open("checkout fails")
	h.play()
	h.crash()
	// The takeover reads the item, then fails to load it as it advances it.
	loads := 0
	h.records.loads(func([]item.Record) error {
		loads++
		if loads == 2 {
			return errBlinked
		}
		return nil
	})
	require.NoError(t, h.boot())
	h.records.loads(nil)
	require.Equal(t, 2, loads)
	require.Empty(t, h.turns.started, "the takeover did not hold the round")

	_, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	ended := h.state(id).Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnInterrupted}, ended.Turns)
	assert.Equal(t, []string{release.Name}, ended.TurnEnds)
}

// A halt whose read of the run's spend fails goes on (R11): its records keep
// the run's last recorded spend, its latest ended round's, and the read is
// logged as a step that failed. (The plan's.)
func TestAHaltWhoseSpendCannotBeReadKeepsTheLastRecordedSpend(t *testing.T) {
	steps := &stepErrors{}
	h := newHarness(t, func(o *app.Options) { o.Log = slog.New(steps) })
	h.scripts[release.Name] = postsATask
	id := h.open("checkout fails")
	run := string(h.state(id).Run.ID)
	h.spend.set(func(s *spend) { s.spent[run] = 700 })
	h.round()
	require.Len(t, h.state(id).Run.Rounds, 2, "round 1 ended, its spend 700, and round 2 opened")
	errSpend := errors.New("the model calls cannot be read")
	h.spend.set(func(s *spend) { s.spent[run], s.spentErr = 900, errSpend })

	_, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	s := h.state(id)
	assert.Equal(t, item.EndedByStopped, s.Run.Rounds[1].Ended.EndedBy)
	assert.Equal(t, int64(700), s.Run.Rounds[1].Ended.Spent)
	assert.Equal(t, int64(700), s.Run.Stopped.Spent)
	assert.True(t, slices.ContainsFunc(steps.kept(), func(err error) bool { return errors.Is(err, errSpend) }), "the read is logged")
}

// Every stop closes the run's upstream MCP sessions (Decision 3, carry 19):
// a run stopped quiet, through its finish, and a run closed mid-round.
func TestARunsUpstreamSessionsCloseWhenItStops(t *testing.T) {
	for name, stop := range map[string]func(h *harness, id item.ID){
		"a run stopped quiet, through its finish": func(h *harness, _ item.ID) { h.round() },
		"a run closed mid-round": func(h *harness, id item.ID) {
			_, err := h.c.Close(h.t.Context(), id, closing("close-1"))
			require.NoError(h.t, err)
		},
	} {
		h := newHarness(t)
		id := h.open("checkout fails")
		run := h.state(id).Run.ID
		require.Empty(t, h.upstreams.closedRuns(), name)

		stop(h, id)

		assert.Contains(t, h.upstreams.closedRuns(), run, name)
	}
}

// A close's record keeps its key's hash, "sha256:" and the key's hexadecimal
// SHA-256, and the key nowhere (Decision 13). Its digest is the act's: the
// same close of the same item, its reason's text aside, has the same digest;
// another reason's code, or another item, another.
func TestARecordKeepsTheKeysHashNotTheKey(t *testing.T) {
	const key = "close-7f3a9c-of-checkout"
	closed := func(id item.ID, reason *board.Reason) *item.Record {
		h := newHarness(t)
		_, err := h.c.Open(t.Context(), byKey(id, "sha256:request", "checkout fails"))
		require.NoError(t, err)
		_, err = h.c.Close(t.Context(), id, &app.Act{Actor: app.Actor{Subject: "admin", Admin: true}, Key: key, Reason: reason})
		require.NoError(t, err)
		records, err := h.records.Load(t.Context(), id)
		require.NoError(t, err)
		require.Equal(t, item.KindClosed, records[len(records)-1].Body.Kind())
		return &records[len(records)-1]
	}

	first := closed("item-1", &board.Reason{Code: "duplicate", Text: "the outage another item tracks"})
	otherText := closed("item-1", &board.Reason{Code: "duplicate", Text: "other words"})
	otherCode := closed("item-1", &board.Reason{Code: "resolved", Text: "the outage another item tracks"})
	otherItem := closed("item-2", &board.Reason{Code: "duplicate", Text: "the outage another item tracks"})

	request := first.Body.Closed.Request
	require.NotNil(t, request)
	assert.Equal(t, keyHash(key), request.Key)
	raw, err := json.Marshal(first)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), key)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, request.Digest)
	assert.NotEqual(t, request.Key, request.Digest)
	assert.Equal(t, request.Digest, otherText.Body.Closed.Request.Digest, "the reason's text changes no digest")
	assert.NotEqual(t, request.Digest, otherCode.Body.Closed.Request.Digest, "its code does")
	assert.NotEqual(t, request.Digest, otherItem.Body.Closed.Request.Digest, "and so does the item")
}

// A close answers ErrNotHeld when the coordinator is not held (Decision 13);
// ErrInvalid for an act without a key, one whose key or reason is of another
// form (R20; Decision 9), or a preview, which a close does not take; ErrNoItem
// for an id with no records; and ErrSkipped for an item whose records hold
// one of a later version (Decision 19). None records anything. (The plan's.)
func TestACloseThatCannotActAnswersWhy(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	later := h.open("payments fail")
	appendRaw(t, h, later, item.Body{V: item.Version + 1})
	reasoned := func(r *board.Reason) *app.Act {
		a := closing("close-1")
		a.Reason = r
		return a
	}
	preview := closing("close-1")
	preview.Preview = true
	before := h.carried()
	for name, c := range map[string]struct {
		id   item.ID
		act  *app.Act
		want error
	}{
		"no act":                     {id, nil, app.ErrInvalid},
		"no key":                     {id, closing(""), app.ErrInvalid},
		"a key of 129 characters":    {id, closing(strings.Repeat("k", 129)), app.ErrInvalid},
		"a key with a line feed":     {id, closing("close\n1"), app.ErrInvalid},
		"a key beyond ASCII":         {id, closing("clôture-1"), app.ErrInvalid},
		"a reason's text alone":      {id, reasoned(&board.Reason{Text: "a duplicate"}), app.ErrInvalid},
		"a code of capitals":         {id, reasoned(&board.Reason{Code: "Duplicate"}), app.ErrInvalid},
		"a code of 33 letters":       {id, reasoned(&board.Reason{Code: strings.Repeat("d", 33)}), app.ErrInvalid},
		"a text of 501 characters":   {id, reasoned(&board.Reason{Code: "duplicate", Text: strings.Repeat("x", 501)}), app.ErrInvalid},
		"a preview":                  {id, preview, app.ErrInvalid},
		"an id with no records":      {"no-such-item", closing("close-1"), app.ErrNoItem},
		"an item of a later version": {later, closing("close-1"), app.ErrSkipped},
	} {
		_, err := h.c.Close(t.Context(), c.id, c.act)
		require.ErrorIs(t, err, c.want, name)
	}
	h.c.Fence()
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Equal(t, before, h.carried(), "nothing is recorded")
	assert.Equal(t, item.StatusInvestigating, h.state(id).Status())
}

// A coordinator needs the upstream sessions, as it needs every port: every
// stop closes the run's upstream MCP sessions (Decision 3). (The plan's.)
func TestACoordinatorNeedsTheUpstreamSessions(t *testing.T) {
	h := newHarness(t)
	ports := h.ports()
	ports.UpstreamSessions = nil

	_, err := app.NewCoordinator(ports, &h.opts)

	require.Error(t, err)
}
