package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// previewing is a close's preview through the admin API, by its admin, which
// takes no key.
func previewing() *app.Act {
	return &app.Act{Actor: app.Actor{Subject: "admin", Admin: true}, Preview: true}
}

// errAnswerLost is what an append answers whose commit landed, though the
// database's answer was lost on its way back.
var errAnswerLost = errors.New("the database's answer was lost")

// closeAnswerLost is the records store whose appends of a person's close
// commit, then fail with errAnswerLost; with blink, the item's next load
// after such an append fails too, as when the database blinks. Every other
// append answers.
type closeAnswerLost struct {
	*failing
	blink bool

	mu      sync.Mutex
	blinked map[item.ID]bool
}

func (l *closeAnswerLost) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	committed, err := l.failing.Append(ctx, id, decide)
	if err != nil || !closedByAPerson(committed) {
		return committed, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.blinked[id] = l.blink
	return nil, errAnswerLost
}

func (l *closeAnswerLost) Load(ctx context.Context, id item.ID) ([]item.Record, error) {
	l.mu.Lock()
	blink := l.blinked[id]
	delete(l.blinked, id)
	l.mu.Unlock()
	if blink {
		return nil, errBlinked
	}
	return l.failing.Load(ctx, id)
}

// closedByAPerson reports whether records hold a person's close.
func closedByAPerson(records []item.Record) bool {
	for i := range records {
		if c := records[i].Body.Closed; c != nil && c.Request != nil {
			return true
		}
	}
	return false
}

// closesLoseTheirAnswers has the harness's instance take over again, under
// the same epoch, with a closeAnswerLost of the harness's records store as its
// records store.
func closesLoseTheirAnswers(t *testing.T, h *harness, blink bool) {
	t.Helper()
	ports := h.ports()
	ports.Records = &closeAnswerLost{failing: h.records, blink: blink, blinked: map[item.ID]bool{}}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
}

// A key names one act of its item (Decision 13; R42): a close of an item
// under the key its earlier close used, with another reason's code, is
// another act, refused with ErrKeyReused, and records nothing. The same key
// on another item is that item's own act: it closes that item, which records
// its own item_closed under it.
func TestAKeyReusedForAnotherActIsRefused(t *testing.T) {
	h := newHarness(t)
	first, second := h.open("checkout fails"), h.open("payments fail")
	under := func(code string) *app.Act {
		a := closing("close-1")
		a.Reason = &board.Reason{Code: code}
		return a
	}
	_, err := h.c.Close(t.Context(), first, under("duplicate"))
	require.NoError(t, err)
	closed, before := h.kinds(first), h.carried()

	_, reused := h.c.Close(t.Context(), first, under("resolved"))

	require.ErrorIs(t, reused, app.ErrKeyReused)
	assert.Equal(t, closed, h.kinds(first), "nothing is recorded")
	assert.Equal(t, before, h.carried(), "nothing is written")
	out, err := h.c.Close(t.Context(), second, under("duplicate"))
	require.NoError(t, err)
	assert.True(t, out.Changed, "the same key on another item is that item's own act")
	s := h.state(second)
	assert.Equal(t, item.StatusClosed, s.Status())
	assert.Equal(t, item.KindClosed, lastKind(h, second))
	assert.Equal(t, keyHash("close-1"), s.Closed.Request.Key, "its own item_closed, under the key")
}

// An act repeated with its key answers as the act did (Decision 13; R14): a
// close repeated under its key answers changed, as the close did, and records
// nothing more. The run's stop and the item's close are in the first close's
// records.
func TestAnActRepeatedWithItsKeyAnswersAsItDid(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	first, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)
	closed := h.kinds(id)

	again, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.True(t, first.Changed)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Limit: 3}}, again, "changed, as the close did")
	assert.Equal(t, closed, h.kinds(id), "no record more")
}

// A key lookup that finds its act advances the item (Decision 13). Here a
// close's closed step failed to post the run's stop entry, as the board
// refused it, and is not tried again on its own; the board back, the close
// repeated under its key advances the item, whose closed step posts the stop
// entry.
func TestAFoundKeyAdvancesTheItem(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.store.set(func(int) error { return errors.New("the board refuses the stop entry") })
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)
	require.Empty(t, stopEntries(h, run), "the closed step failed at the board")
	h.store.set(nil)

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.True(t, out.Changed)
	stops := stopEntries(h, run)
	require.Len(t, stops, 1, "the stop entry is posted")
	assert.Equal(t, "closed", stops[0].Reason)
}

// An act whose append failed, and whose load after it failed too, is tried
// again (Decision 13). Here a close's append committed, though the database's
// answer was lost, and the load by which Close looked for it failed, as the
// database blinked: Close answers the append's error, and, the close's
// outcome unknown, drops the run before it answers, which stops its turns and
// closes its upstream sessions (see
// TestACloseWhoseOutcomeIsUnknownStartsNoTurnAfterItsAnswer). After the retry
// wait the coordinator loads the item again, finds its item_closed, and its
// closed step posts the run's stop entry.
func TestAnActWhoseLoadAfterAFailedAppendFailsIsTriedAgain(t *testing.T) {
	h := newHarness(t)
	closesLoseTheirAnswers(t, h, true)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.ErrorIs(t, err, errAnswerLost, "the append's error")
	require.Equal(t, item.StatusClosed, h.state(id).Status(), "though its item_closed landed")
	assert.Contains(t, h.turns.stopped, string(run)+"/"+release.Name, "its turns stop")
	assert.Contains(t, h.turns.stopped, string(run)+"/"+runtime.Name, "its turns stop")
	assert.Contains(t, h.upstreams.closedRuns(), run)
	require.Empty(t, stopEntries(h, run), "no stop entry yet")

	h.clock.Advance(h.opts.Retry)

	stops := stopEntries(h, run)
	require.Len(t, stops, 1)
	assert.Equal(t, "closed", stops[0].Reason)
}

// A close whose append fails without landing changed nothing (Decisions 6
// and 13): Close answers the append's error, and the run goes on with its
// state and its timers. Its turns run, alpha's ending through end_turn, its
// session still live, and its deadline ends its round, beta's turn
// interrupted.
func TestACloseWhoseAppendFailsLeavesTheRunGoingOn(t *testing.T) {
	h := newHarness(t)
	h.scripts[runtime.Name] = ending("")
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	kinds := h.kinds(id)
	failed := false
	h.records.set(func(int) error {
		if failed {
			return nil
		}
		failed = true
		return errBlinked
	})

	_, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.ErrorIs(t, err, errBlinked, "the append's error")
	assert.Equal(t, kinds, h.kinds(id), "nothing landed")
	assert.Empty(t, h.turns.stopped, "no turn is stopped")
	assert.NotContains(t, h.upstreams.closedRuns(), run)
	h.play()
	advanceTo(h, h.state(id).Run.Latest().Opened.Deadline)
	h.clock.Advance(h.opts.Grace)
	ended := h.state(id).Run.Rounds[0].Ended
	require.NotNil(t, ended, "its deadline ended its round")
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnInterrupted}, ended.Turns)
}

// A close whose append committed, though the database's answer was lost, is
// found by its key (Decisions 6 and 13): Close loads the item, finds its
// item_closed, and answers changed, as a repeat; it drops the run, and the
// closed step stops its turns, ends its sessions, closes its upstream
// sessions and posts its stop entry.
func TestACloseWhoseCommitAnswerWasLostStopsTheRun(t *testing.T) {
	h := newHarness(t)
	closesLoseTheirAnswers(t, h, false)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	started := slices.Clone(h.turns.started)
	require.Len(t, started, 2)

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Limit: 3}}, out, "as a repeat")
	assert.Equal(t, item.StatusClosed, h.state(id).Status())
	for _, turn := range started {
		assert.Contains(t, h.turns.stopped, string(run)+"/"+turn.Agent)
		_, err := h.registry.MCP().Session(t.Context(), turn.MCP.Reveal())
		require.ErrorIs(t, err, actionsapp.ErrNoSession, "its session ended")
	}
	assert.Contains(t, h.upstreams.closedRuns(), run)
	stops := stopEntries(h, run)
	require.Len(t, stops, 1)
	assert.Equal(t, "closed", stops[0].Reason)
}

// A close retried after a lost answer finds its key (Decision 13). Here the
// first try's item_closed landed, though the database's answer was lost, and
// the load by which Close looked for it failed too, so the person got the
// error, and the run was dropped, the close's outcome unknown. The person
// retries under the same key before the retry wait: the key is found, and the
// close answers changed, as a repeat; nothing more is recorded, no turn is
// stopped again, the found key's advance runs the closed step, which posts
// the stop entry, and the item's pending retry is spent.
func TestARetriedCloseAfterALostAnswerAnswersAsARepeat(t *testing.T) {
	h := newHarness(t)
	closesLoseTheirAnswers(t, h, true)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.ErrorIs(t, err, errAnswerLost)
	require.Len(t, h.turns.stopped, 2, "the run was dropped")
	closed := h.kinds(id)

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Limit: 3}}, out)
	assert.Equal(t, closed, h.kinds(id), "no record more")
	assert.Len(t, h.turns.stopped, 2, "no turn is stopped again")
	assert.Len(t, stopEntries(h, run), 1, "the found key's advance ran the closed step")
	assert.Zero(t, h.clock.pending(), "the advance took the pending retry's place")
}

// A close found by its key drops the run before it advances the item, so that
// an advance that fails cannot leave a turn to start (Decision 13). Here
// beta's start failed, and its retry waits, when a close's item_closed lands
// with its answer lost: Close looks for it, finds it by its key, and answers
// changed, as a repeat, and the advance after the found key fails at its
// load. The close knew its outcome, so the found key's drop is the only one.
// Once the clock passes the retry wait, beta's start is not tried again.
func TestAFoundCloseWhoseAdvanceFailsStartsNoTurn(t *testing.T) {
	h := newHarness(t)
	closesLoseTheirAnswers(t, h, false)
	starts := 0
	h.turns.fail = func(turn *app.Turn) error {
		if turn.Agent != runtime.Name {
			return nil
		}
		starts++
		return errors.New("the runner cannot be reached")
	}
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	require.Equal(t, 1, starts, "beta's start failed, and waits for its retry")
	// The close's reads of the item before its append, its target's and its
	// key's lookup, and those after it, as it looks for its record, are
	// answered; every load after them fails.
	loads := 0
	h.records.loads(func([]item.Record) error {
		loads++
		if loads > 4 {
			return errBlinked
		}
		return nil
	})

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.True(t, out.Changed, "found by its key, as a repeat")
	require.Empty(t, stopEntries(h, run), "the advance after the found key failed at its load")
	h.clock.Advance(h.opts.Retry)
	assert.Equal(t, 1, starts, "beta's start is not tried again")
	assert.Contains(t, h.turns.stopped, string(run)+"/"+runtime.Name)
}

// A close whose outcome is unknown, its item_closed landed with its answer
// lost and the load that looks for it failing too, drops the run before it
// answers, as the kill's halt drops first (Decisions 3, 6 and 13; Review
// Focus 1): the close may have landed, and no turn of a closed item starts
// after the answer. The close comes a second after the item opened, in two
// cases:
//   - beta's start failed, and its retry, which fires before the item's,
//     waits;
//   - each turn's launch is pending, as under Docker, and the runner may
//     complete it before the item's retry.
//
// Close answers the append's error, and at once every turn of the run is
// stopped, a pending launch with it, its sessions end and its upstream
// sessions close; once the clock passes beta's retry, but not the item's, no
// turn has started since the answer. Fails first: only the item's retry
// dropped the run, so beta's retry started its turn, and every session was
// live until then.
func TestACloseWhoseOutcomeIsUnknownStartsNoTurnAfterItsAnswer(t *testing.T) {
	for _, row := range []struct {
		name    string
		pending bool
	}{
		{name: "a turn whose start failed, which its timer tries again"},
		{name: "turns whose launches are pending", pending: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newHarness(t)
			closesLoseTheirAnswers(t, h, true)
			unreachable := !row.pending
			h.turns.fail = func(turn *app.Turn) error {
				if unreachable && turn.Agent == runtime.Name {
					return errors.New("the runner cannot be reached")
				}
				return nil
			}
			h.turns.pending = row.pending
			id := h.open("checkout fails")
			run := h.state(id).Run.ID
			given := slices.Clone(h.turns.started)
			// The close comes a second later, so that beta's retry fires
			// before the item's.
			h.clock.Advance(time.Second)

			_, err := h.c.Close(t.Context(), id, closing("close-1"))

			require.ErrorIs(t, err, errAnswerLost, "the append's error: the close's outcome is not known yet")
			require.Equal(t, item.StatusClosed, h.state(id).Status(), "the close landed")
			unreachable = false
			assert.Contains(t, h.turns.stopped, string(run)+"/"+release.Name)
			assert.Contains(t, h.turns.stopped, string(run)+"/"+runtime.Name, "beta's turn too, waiting for its start's retry or launching")
			for _, turn := range given {
				_, err := h.registry.MCP().Session(t.Context(), turn.MCP.Reveal())
				require.ErrorIs(t, err, actionsapp.ErrNoSession, "%s's session ended", turn.Agent)
			}
			assert.Contains(t, h.upstreams.closedRuns(), run)
			answered := len(h.turns.started)
			h.clock.Advance(4*time.Second + 500*time.Millisecond)
			assert.Len(t, h.turns.started, answered, "no turn of the closed item started after the answer")
		})
	}
}

// closeFailsUnknown has the harness's records store refuse the next append of
// a person's close before it commits, as a database that cannot be reached
// does, and fail the load after it, so that the close cannot learn whether it
// landed, though it did not.
func closeFailsUnknown(h *harness) {
	h.t.Helper()
	refused, blink := false, false
	h.records.refuses(func(_ item.ID, drafts []item.Draft) error {
		for i := range drafts {
			if c := drafts[i].Body.Closed; c != nil && c.Request != nil && !refused {
				refused, blink = true, true
				return errBlinked
			}
		}
		return nil
	})
	h.records.loads(func([]item.Record) error {
		if blink {
			blink = false
			return errBlinked
		}
		return nil
	})
}

// A close whose outcome is unknown drops its run though the close did not
// land (Decisions 3 and 6): its append failed before it committed, and the
// load that looked for it failed too. The item's retry then carries the run
// on, as a takeover carries a run it does not hold: the item still
// investigates, and its step holds the run's round again, with fresh sessions
// and turns, the snapshot the drop kept (R45) keeping the round from reading
// as missed. In two cases:
//   - the round's deadline has not passed: its turns start again, each with a
//     live session, and their ends end the round;
//   - the round's deadline passed during the retry wait: the round ends by its
//     deadline, unseen, and the run goes on to its next round, whose turns
//     start.
//
// Fails first: the close drops nothing, and the retry, finding the round
// held, starts no turn.
func TestARunWhoseCloseDidNotLandIsCarriedOnByItsItemsRetry(t *testing.T) {
	for _, row := range []struct {
		name   string
		passed bool
	}{
		{name: "before the round's deadline"},
		{name: "the round's deadline passed during the retry wait", passed: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newHarness(t)
			id := h.open("checkout fails")
			run := h.state(id).Run.ID
			given := slices.Clone(h.turns.started)
			require.Len(t, given, 2)
			if row.passed {
				advanceTo(h, h.state(id).Run.Latest().Opened.Deadline.Add(-2*time.Second))
			}
			kinds := h.kinds(id)
			closeFailsUnknown(h)

			_, err := h.c.Close(t.Context(), id, closing("close-1"))

			require.ErrorIs(t, err, errBlinked, "the append's error: the close's outcome is not known yet")
			require.Equal(t, kinds, h.kinds(id), "nothing landed")
			assert.Contains(t, h.turns.stopped, string(run)+"/"+release.Name, "the run is dropped")
			assert.Contains(t, h.turns.stopped, string(run)+"/"+runtime.Name, "the run is dropped")
			assert.Contains(t, h.upstreams.closedRuns(), run, "the run is dropped")
			h.clock.Advance(h.opts.Retry)
			if !row.passed {
				again := h.turns.started[len(given):]
				require.Len(t, again, 2, "the retry holds round 1 again: its turns start")
				for _, turn := range again {
					assert.Equal(t, 1, turn.Round)
					_, err := h.registry.MCP().Session(t.Context(), turn.MCP.Reveal())
					require.NoError(t, err, "%s's turn has a session of its own", turn.Agent)
				}
				h.round()
				ended := h.state(id).Run.Rounds[0].Ended
				require.NotNil(t, ended, "the turns end the round")
				assert.Equal(t, item.EndedByTurns, ended.EndedBy)
				assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, ended.Turns)
				return
			}
			h.clock.Advance(h.opts.Grace)
			s := h.state(id)
			require.Len(t, s.Run.Rounds, 2, "round 1 ended, and round 2 opened")
			assert.Equal(t, item.EndedByDeadline, s.Run.Rounds[0].Ended.EndedBy)
			assert.True(t, s.Run.Rounds[0].Ended.Unseen, "the retry took the round up after its deadline")
			assert.Equal(t, 2, h.last(release.Name).Round, "round 2's turns start")
			assert.Equal(t, 2, h.last(runtime.Name).Round, "round 2's turns start")
		})
	}
}

// A preview runs the act's checks on the same state, under the same lock, and
// answers what the act would do, recording nothing and acting on nothing
// (Decision 13): a close's preview of an item in round 1 answers its run's
// stop, at round 1, closed, and the item's close. Nothing is recorded; no
// session ends, no turn stops and no upstream session closes; and the round
// goes on, its turns ending it.
func TestAPreviewRecordsNothingAndActsOnNothing(t *testing.T) {
	h := newHarness(t)
	order := tracing(t, h)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	before, seen := h.carried(), order.seen()

	out, err := h.c.Close(t.Context(), id, previewing())

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Consequences: app.Consequences{
		Stops: []app.RunStop{{Item: id, Run: run, Round: 1, Reason: item.StopClosed}}, Closes: []item.ID{id}, Limit: 3,
	}}, out)
	assert.Equal(t, before, h.carried(), "nothing is recorded")
	assert.Equal(t, seen, order.seen(), "no EndRun, Stop or CloseRun")
	assert.Equal(t, item.StatusInvestigating, h.state(id).Status())
	h.round()
	ended := h.state(id).Run.Rounds[0].Ended
	require.NotNil(t, ended, "the round goes on")
	assert.Equal(t, item.EndedByTurns, ended.EndedBy)
}

// A preview takes no key, and the key lookup skips it (Decision 13): a
// preview without a key is answered, and a close under a key after it closes
// the item, with the consequences the preview gave.
func TestAPreviewTakesNoKeyAndALaterActWithTheKeyActs(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	preview, err := h.c.Close(t.Context(), id, previewing())
	require.NoError(t, err)
	require.False(t, preview.Changed)

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.True(t, out.Changed)
	assert.Equal(t, preview.Consequences, out.Consequences, "the preview gave what the close did")
	assert.Equal(t, item.StatusClosed, h.state(id).Status())
}

// A key lookup reads every record of the item, not only what its fold keeps
// (Decision 13; R42). Here a close under one key is followed by a reopen under
// another, a record the fold keeps no request of, and which leaves the close
// out of the fold: the close repeated under its key is found, answers changed,
// records nothing, and leaves the item awaiting its close, its run not dropped
// again; a close under the reopen's key names the reopen, and is refused with
// ErrKeyReused.
func TestAKeyIsFoundAmongEveryRecordOfItsItem(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.NoError(t, err)
	appendRaw(t, h, id, item.Body{V: item.Version, Reopened: &item.Reopened{
		By: "admin", Request: item.Request{Key: keyHash("reopen-1"), Digest: "sha256:reopen"},
		AwaitingClose: item.AwaitingClose{Reason: item.StopQuiet, AutoClose: closesAfter},
	}})
	reopened, closedRuns := h.kinds(id), len(h.upstreams.closedRuns())

	again, err := h.c.Close(t.Context(), id, closing("close-1"))
	_, reused := h.c.Close(t.Context(), id, closing("reopen-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{Limit: 3}}, again)
	require.ErrorIs(t, reused, app.ErrKeyReused)
	assert.Equal(t, reopened, h.kinds(id), "nothing is recorded")
	assert.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
	assert.Len(t, h.upstreams.closedRuns(), closedRuns, "the run is not dropped again")
}

// An act names who acts (Decision 13): its record keeps the subject, and a
// close that names no one is an automatic close's (Contract C19). An act
// whose actor has no subject, an admin's or not, is refused with ErrInvalid,
// its preview too, and nothing is recorded.
func TestAnActWithoutASubjectIsRefused(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	before := h.carried()

	_, act := h.c.Close(t.Context(), id, &app.Act{Actor: app.Actor{Admin: true}, Key: "close-1"})
	_, preview := h.c.Close(t.Context(), id, &app.Act{Actor: app.Actor{Admin: true}, Preview: true})

	require.ErrorIs(t, act, app.ErrInvalid)
	require.ErrorIs(t, preview, app.ErrInvalid)
	assert.Equal(t, before, h.carried(), "nothing is recorded")
	assert.Equal(t, item.StatusInvestigating, h.state(id).Status())
}

// A person's reason is redacted with the rules that apply always before it is
// recorded, not refused, when a rule changes it without an escape (Decision
// 9; R1): a close whose reason's text holds what the api-keys rule matches
// records the rule's replacement in its place, and its record holds the key
// nowhere.
func TestAPersonsReasonIsRedactedBeforeItIsRecorded(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	a := closing("close-1")
	a.Reason = &board.Reason{Code: "duplicate", Text: "the key sk-abcdefgh leaked here"}

	_, err := h.c.Close(t.Context(), id, a)

	require.NoError(t, err)
	assert.Equal(t, &board.Reason{Code: "duplicate", Text: "the key [redacted:api-keys] leaked here"}, h.state(id).Closed.Reason)
	raw, err := json.Marshal(lastRecord(h, id))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "sk-abcdefgh")
}

// Every act that takes a person's text redacts its reason's text with the
// rules that apply always before it is recorded (Decision 9; R1, R13): a
// close, a promotion, a reopen and an extension (M11), in the item's
// records, and the kill
// switch's on, in its row. Each act's reason holds what the api-keys rule
// matches, and what it recorded holds the rule's replacement in its place,
// and the key nowhere. The records and the row are chained and append-only,
// so a text recorded unredacted would stay. (A guard: each act redacts
// today.)
func TestEveryActRedactsItsReasonBeforeItIsRecorded(t *testing.T) {
	leaked := board.Reason{Code: "duplicate", Text: "the key sk-abcdefgh leaked here"}
	redacted := &board.Reason{Code: "duplicate", Text: "the key [redacted:api-keys] leaked here"}
	giving := func(a *app.Act) *app.Act {
		reason := leaked
		a.Reason = &reason
		return a
	}
	for _, row := range []struct {
		name string
		// act acts with the reason on a harness of its own, and returns the
		// reason it recorded, and the record or row that holds it.
		act func(h *harness) (*board.Reason, any)
	}{
		{name: "a close", act: func(h *harness) (*board.Reason, any) {
			id := h.open("checkout fails")
			_, err := h.c.Close(h.t.Context(), id, giving(closing("close-1")))
			require.NoError(h.t, err)
			r := lastRecordOf(h, id, func(b *item.Body) bool { return b.Closed != nil })
			return r.Body.Closed.Reason, r
		}},
		{name: "a promotion", act: func(h *harness) (*board.Reason, any) {
			id := notStaffedAtTheCap(h)
			_, err := h.c.Promote(h.t.Context(), id, giving(promoting("promote-1")))
			require.NoError(h.t, err)
			r := lastRecordOf(h, id, func(b *item.Body) bool { return b.Promoted != nil })
			return r.Body.Promoted.Reason, r
		}},
		{name: "a reopen", act: func(h *harness) (*board.Reason, any) {
			id := closedAwaiting(h, "checkout fails")
			_, err := h.c.Reopen(h.t.Context(), id, giving(reopening("reopen-1")))
			require.NoError(h.t, err)
			r := lastRecordOf(h, id, func(b *item.Body) bool { return b.Reopened != nil })
			return r.Body.Reopened.Reason, r
		}},
		{name: "an extension", act: func(h *harness) (*board.Reason, any) {
			id := h.open("checkout fails")
			_, err := h.c.Extend(h.t.Context(), id, tokens(1_000), giving(extending("extend-1")))
			require.NoError(h.t, err)
			r := lastRecordOf(h, id, func(b *item.Body) bool { return b.Extended != nil })
			return r.Body.Extended.Reason, r
		}},
		{name: "a link", act: func(h *harness) (*board.Reason, any) {
			a, b := h.open("checkout fails"), h.open("payments fail")
			_, err := h.c.Link(h.t.Context(), b, a, giving(&app.Act{Actor: administrator, Key: "link-1"}))
			require.NoError(h.t, err)
			r := lastRecordOf(h, b, func(b *item.Body) bool { return b.Linked != nil })
			return r.Body.Linked.Reason, r
		}},
		{name: "an unlink", act: func(h *harness) (*board.Reason, any) {
			a, b := h.open("checkout fails"), h.open("payments fail")
			_, err := h.c.Link(h.t.Context(), b, a, linking("link-1"))
			require.NoError(h.t, err)
			_, err = h.c.Unlink(h.t.Context(), b, giving(&app.Act{Actor: administrator, Key: "unlink-1"}))
			require.NoError(h.t, err)
			r := lastRecordOf(h, b, func(b *item.Body) bool { return b.Unlinked != nil })
			return r.Body.Unlinked.Reason, r
		}},
		{name: "the kill switch's on", act: func(h *harness) (*board.Reason, any) {
			_, err := h.c.SetKillSwitch(h.t.Context(), true, giving(&app.Act{Actor: administrator}))
			require.NoError(h.t, err)
			state, err := h.controls.Controls.KillSwitch(h.t.Context())
			require.NoError(h.t, err)
			return &state.Reason, state
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			h := newHarness(t)

			reason, recorded := row.act(h)

			assert.Equal(t, redacted, reason)
			raw, err := json.Marshal(recorded)
			require.NoError(t, err)
			assert.NotContains(t, string(raw), "sk-abcdefgh", "the key is recorded nowhere")
		})
	}
}

// lastRecordOf returns the last record of an item whose body is as is says.
func lastRecordOf(h *harness, id item.ID, is func(*item.Body) bool) item.Record {
	h.t.Helper()
	records, err := h.records.Load(h.t.Context(), id)
	require.NoError(h.t, err)
	for i := len(records) - 1; i >= 0; i-- {
		if is(&records[i].Body) {
			return records[i]
		}
	}
	h.t.Fatalf("no record of item %s is as asked", id)
	return item.Record{}
}

// A person's text on an item that started is checked with the rules of the
// revision it locked, not the current one's (Decision 9): here the current
// revision's rule that applies always is another, and the api-keys rule,
// which the locked revision holds, is gone from it; the close's reason is
// redacted all the same. When the locked revision cannot be read, as when its
// commit is missing from the repository, the current revision's rules check
// the text, which is taken and redacted.
func TestAPersonsTextIsCheckedWithTheRulesOfTheItemsLockedRevision(t *testing.T) {
	h := newHarness(t)
	locked := h.revs.Current()
	first, second := h.open("checkout fails"), h.open("payments fail")
	h.revs.set(h.revision(strings.Repeat("b", 40), func(files map[string][]byte) {
		delete(files, "redaction-rules/api-keys.yaml")
		files["redaction-rules/tokens.yaml"] = []byte(`kind: RedactionRule
metadata: {name: tokens, owner: security@example.com}
spec:
  always: true
  patterns: ['tok-[a-z0-9]{8}']
`)
	}))
	closed := func(id item.ID) *board.Reason {
		a := closing("close-1")
		a.Reason = &board.Reason{Code: "duplicate", Text: "the key sk-abcdefgh leaked here"}
		_, err := h.c.Close(t.Context(), id, a)
		require.NoError(t, err)
		return h.state(id).Closed.Reason
	}
	redacted := &board.Reason{Code: "duplicate", Text: "the key [redacted:api-keys] leaked here"}

	assert.Equal(t, redacted, closed(first), "by the locked revision's rules")
	h.revs.set(locked)
	loseCommit(h, commit)
	assert.Equal(t, redacted, closed(second), "by the current revision's rules")
}

// A reason's text is checked with the rules of the item's locked revision, or
// else of the current one; with neither, free text is refused, and a code
// alone is taken (Decision 9). Here the item never started, as its start
// found no revision, and none is current.
func TestFreeTextWithNoConfigurationIsRefusedAndACodeAloneIsTaken(t *testing.T) {
	h := newHarness(t)
	// The opening reads the revision; its start reads none.
	h.revs.next(nil)
	id := h.open("checkout fails")
	require.Equal(t, item.StatusQueued, h.state(id).Status())
	withText := closing("close-1")
	withText.Reason = &board.Reason{Code: "duplicate", Text: "another item tracks it"}
	before := h.carried()

	_, err := h.c.Close(t.Context(), id, withText)

	require.ErrorIs(t, err, app.ErrInvalid)
	assert.Equal(t, before, h.carried(), "nothing is recorded")
	codeAlone := closing("close-2")
	codeAlone.Reason = &board.Reason{Code: "duplicate"}
	out, err := h.c.Close(t.Context(), id, codeAlone)
	require.NoError(t, err)
	assert.True(t, out.Changed)
	assert.Equal(t, &board.Reason{Code: "duplicate"}, h.state(id).Closed.Reason)
}

// A reason's form is checked (Decision 9): its code is a lowercase letter,
// then at most 31 lowercase letters and underscores, and its text holds at
// most 500 characters. Each reason of another form is refused with
// ErrInvalid, and nothing is recorded. (The plan's.)
func TestAReasonOfAnotherFormIsRefused(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	before := h.carried()
	reasons := []board.Reason{
		{Code: "Bad"},
		{Code: "1x"},
		{Code: strings.Repeat("d", 33)},
		{Code: "duplicate", Text: strings.Repeat("x", 501)},
	}

	for i := range reasons {
		a := closing("close-1")
		a.Reason = &reasons[i]
		_, err := h.c.Close(t.Context(), id, a)
		require.ErrorIs(t, err, app.ErrInvalid, "reason %d", i)
	}

	assert.Equal(t, before, h.carried(), "nothing is recorded")
	assert.Equal(t, item.StatusInvestigating, h.state(id).Status())
}
