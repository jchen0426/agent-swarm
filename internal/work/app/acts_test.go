package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

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
	assert.Equal(t, app.Outcome{Changed: true}, again, "changed, as the close did")
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
// database blinked: Close answers the append's error, and the run's turns go
// on for now. After the retry wait the coordinator loads the item again,
// finds its item_closed, drops the run, which stops its turns and closes its
// upstream sessions, and posts its stop entry.
func TestAnActWhoseLoadAfterAFailedAppendFailsIsTriedAgain(t *testing.T) {
	h := newHarness(t)
	closesLoseTheirAnswers(t, h, true)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.ErrorIs(t, err, errAnswerLost, "the append's error")
	require.Equal(t, item.StatusClosed, h.state(id).Status(), "though its item_closed landed")
	require.Empty(t, h.turns.stopped, "its turns go on for now")

	h.clock.Advance(h.opts.Retry)

	assert.Contains(t, h.turns.stopped, string(run)+"/"+release.Name)
	assert.Contains(t, h.turns.stopped, string(run)+"/"+runtime.Name)
	assert.Contains(t, h.upstreams.closedRuns(), run)
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
	assert.Equal(t, app.Outcome{Changed: true}, out, "as a repeat")
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
// error. The person retries under the same key before the retry wait: the
// key is found, the run is dropped, and its turns stop; nothing more is
// recorded, and the item's pending retry is spent.
func TestARetriedCloseAfterALostAnswerStopsTheRunsTurns(t *testing.T) {
	h := newHarness(t)
	closesLoseTheirAnswers(t, h, true)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.ErrorIs(t, err, errAnswerLost)
	require.Empty(t, h.turns.stopped)
	closed := h.kinds(id)

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true}, out)
	assert.Contains(t, h.turns.stopped, string(run)+"/"+release.Name)
	assert.Contains(t, h.turns.stopped, string(run)+"/"+runtime.Name)
	assert.Equal(t, closed, h.kinds(id), "no record more")
	assert.Zero(t, h.clock.pending(), "the advance took the pending retry's place")
}

// A close found by its key drops the run before it advances the item, so that
// an advance that fails cannot leave a turn to start (Decision 13). Here
// beta's start failed, and its retry waits, when a close's item_closed lands
// with its answer lost and the load after it failing, so the person got the
// error; the person retries under the key, which is found, and the advance
// after it fails at its load. Once the clock passes the retry wait, beta's
// start is not tried again.
func TestAFoundCloseWhoseAdvanceFailsStartsNoTurn(t *testing.T) {
	h := newHarness(t)
	closesLoseTheirAnswers(t, h, true)
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
	_, err := h.c.Close(t.Context(), id, closing("close-1"))
	require.ErrorIs(t, err, errAnswerLost)
	// The retry's reads of the item, its target's and its key's lookup, are
	// answered; every load after them fails.
	loads := 0
	h.records.loads(func([]item.Record) error {
		loads++
		if loads > 2 {
			return errBlinked
		}
		return nil
	})

	out, err := h.c.Close(t.Context(), id, closing("close-1"))

	require.NoError(t, err)
	assert.True(t, out.Changed)
	require.Empty(t, stopEntries(h, run), "the advance after the found key failed at its load")
	h.clock.Advance(h.opts.Retry)
	assert.Equal(t, 1, starts, "beta's start is not tried again")
	assert.Contains(t, h.turns.stopped, string(run)+"/"+runtime.Name)
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
		Stops: []app.RunStop{{Item: id, Run: run, Round: 1, Reason: item.StopClosed}}, Closes: []item.ID{id},
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
	assert.Equal(t, app.Outcome{Changed: true}, again)
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
