package app_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/holding"
	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// An item opens and starts at once: it locks the configuration, creates its
// run with the agents subscribed to its type, posts the evidence it opened
// with, opens round 1 on the board, and starts a turn for each agent, with
// sessions the gateways know from then on.
func TestAnItemStartsItsFirstRoundAtOnce(t *testing.T) {
	h := newHarness(t)

	id := h.open("checkout fails")

	s := h.state(id)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindStarted, item.KindRunCreated, item.KindRoundOpened}, h.kinds(id))
	assert.Equal(t, &item.Started{Commit: commit, Digest: h.revs.rev.Digest(), Images: map[string]string{"thinloop": image}}, s.Started)
	assert.Equal(t, []string{release.Name, runtime.Name}, s.Run.Created.Agents, "the subscribed agents, in the revision's order")
	assert.Equal(t, item.Budget{WallClock: 10 * time.Minute, Tokens: 90_000, Rounds: 3}, s.Run.Created.Budget)
	events := h.events(s.Run.ID)
	require.Len(t, events, 2)
	assert.Equal(t, &board.Evidence{Claim: "Opened by person: checkout fails; service=checkout", Provenance: board.Provenance{
		Kind: board.FromPlatform, Ref: "item:" + string(id), Source: "person",
	}}, events[0].Body.Evidence)
	assert.Equal(t, &board.RoundStart{Round: 1}, events[1].Body.Round)
	started := h.turns.started
	require.Len(t, started, 2)
	assert.NotEqual(t, started[0].ID, started[1].ID, "each turn has its own id")
	opened := s.Run.Latest().Opened
	for i, name := range []string{release.Name, runtime.Name} {
		turn := started[i]
		assert.NotEmpty(t, turn.ID)
		assert.Equal(t, name, turn.Agent)
		assert.Equal(t, 1, turn.Round)
		assert.Equal(t, opened.Deadline, turn.Deadline)
		assert.Equal(t, image, turn.Image)
		assert.Equal(t, "claude-haiku-5-5", turn.Model)
		assert.Contains(t, turn.Prompt, "Round 1 of this investigation: at most 2 more rounds follow it")
		mcp, err := h.registry.MCP().Session(t.Context(), turn.MCP.Reveal())
		require.NoError(t, err)
		assert.Equal(t, actionsapp.Session{Run: s.Run.ID, Agent: name, Commit: commit, Round: 1, Deadline: opened.Deadline,
			Item: map[string]string{"service": "checkout"}, Done: mcp.Done, Epoch: h.epoch}, mcp)
		llm, err := h.registry.LLM().Session(t.Context(), turn.LLM.Reveal())
		require.NoError(t, err)
		assert.Equal(t, int64(90_000), llm.Budget)
	}
}

// A round whose agents all ended their own turns on the board leaves nothing
// on it but their turn ends: once the round's write calls had their grace,
// the run stops quiet, and the round's end records the turn ends it rests on.
// Its board ends with the stop entry, its sessions end, and the item awaits
// its close, with its auto-close armed, the one timer left.
func TestAQuietRoundStopsTheRun(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID

	played := h.play()
	h.clock.Advance(h.opts.Grace - time.Second)
	require.Len(t, h.kinds(id), 4, "the round's end waits for its write calls' grace")
	h.clock.Advance(time.Second)

	s := h.state(id)
	assert.Equal(t, item.StatusAwaitingClose, s.Status())
	assert.Equal(t, item.StopQuiet, s.Closing.Reason)
	assert.Equal(t, item.StopQuiet, s.Run.Stopped.Reason)
	ended := s.Run.Rounds[0].Ended
	assert.Equal(t, item.EndedByTurns, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, ended.Turns)
	assert.Equal(t, []string{release.Name, runtime.Name}, ended.TurnEnds)
	stop, stopped := lastStop(h.events(run))
	require.True(t, stopped)
	assert.Equal(t, "quiet", stop.Reason)
	assert.Equal(t, 1, stop.Rounds)
	_, err := h.registry.MCP().Session(t.Context(), played[0].MCP.Reveal())
	require.ErrorIs(t, err, actionsapp.ErrNoSession)
	_, err = h.registry.LLM().Session(t.Context(), played[1].LLM.Reveal())
	require.ErrorIs(t, err, llmapp.ErrNoSession)
	assert.Equal(t, []time.Time{s.AwaitingSince.Add(closesAfter)}, h.clock.armed(), "one timer is left, the item's auto-close")
}

func lastStop(events []board.Event) (*board.Stop, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if st := events[i].Body.Stop; st != nil {
			return st, true
		}
	}
	return nil, false
}

// Two agents that post the same hypothesis make it converge; the next
// round is its wrap-up round, in which the agents may refute it, and the
// run stops converged with it as the conclusion. The wrap-up round's
// sessions may spend a round's share of the budget above the run's spend.
func TestAConvergedHypothesisGetsItsWrapUpRoundAndTheRunStops(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name], h.scripts[runtime.Name] = hypothesis, hypothesis
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.spend.set(func(s *spend) { s.spent[string(run)] = 89_000 })

	h.round()
	wrapUp := h.turns.take()
	h.turns.queued = append(h.turns.queued, wrapUp...)
	h.scripts[release.Name], h.scripts[runtime.Name] = endTurn, endTurn
	llm, err := h.registry.LLM().Session(t.Context(), wrapUp[0].LLM.Reveal())
	require.NoError(t, err)
	h.round()

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	h1 := s.Run.Rounds[0].Ended.Decision.WrapUp
	assert.Equal(t, item.Decision{Next: item.NextWrapUp, WrapUp: h1}, s.Run.Rounds[0].Ended.Decision)
	assert.Equal(t, h1, wrapUp[0].WrapUp)
	assert.Contains(t, wrapUp[0].Prompt, "wrap-up round")
	assert.Equal(t, int64(89_000+30_000), llm.Budget)
	assert.Equal(t, item.StopConverged, s.Run.Stopped.Reason)
	assert.Equal(t, h1, s.Run.Stopped.Conclusion)
	stop, _ := lastStop(h.events(run))
	assert.Equal(t, h1, stop.Conclusion)
	assert.Equal(t, &board.RoundStart{Round: 2, WrapUp: h1}, roundEntries(h.events(run))[1])
}

// A wrap-up round in which no agent took part, as when the model vendor
// failed every turn, or every turn was refused once the day's spend reached
// the cap, concludes nothing: its hypothesis is still converged, but no agent
// could have refuted it. It uses up a wrap-up, so the next wrap-up round is
// the run's last; with the cap reached, the run stops cut, and its item is
// not staffed.
func TestAWrapUpRoundNoAgentCouldActInConcludesNothing(t *testing.T) {
	for name, c := range map[string]struct {
		class  item.TurnClass
		capped bool
	}{
		"the vendor failed every turn": {class: item.TurnVendor},
		"the cap refused every turn":   {class: item.TurnRefused, capped: true},
	} {
		h := newHarness(t)
		h.scripts[release.Name], h.scripts[runtime.Name] = hypothesis, hypothesis
		id := h.open("checkout fails")
		h.round()
		converged := h.state(id).Run.Rounds[0].Ended.Decision
		require.Equal(t, item.NextWrapUp, converged.Next, name)
		if c.capped {
			h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })
		}
		h.scripts[release.Name], h.scripts[runtime.Name] = ending(c.class), ending(c.class)

		h.play()
		h.clock.Advance(2*time.Minute + h.opts.Grace)

		s := h.state(id)
		require.GreaterOrEqual(t, len(s.Run.Rounds), 2, name)
		wrapUp := s.Run.Rounds[1].Ended
		require.NotNil(t, wrapUp, name)
		assert.Equal(t, map[string]item.TurnClass{release.Name: c.class, runtime.Name: c.class}, wrapUp.Turns, name)
		if !c.capped {
			assert.Equal(t, converged, wrapUp.Decision, "%s: another wrap-up round, the last", name)
			assert.Nil(t, s.Run.Stopped, name)
			continue
		}
		assert.Equal(t, item.Decision{Next: item.NextStop, Stop: item.StopCut}, wrapUp.Decision, name)
		require.NotNil(t, s.Closing, name)
		assert.Equal(t, item.StopNotStaffed, s.Closing.Reason, name)
	}
}

func roundEntries(events []board.Event) []*board.RoundStart {
	var out []*board.RoundStart
	for i := range events {
		if r := events[i].Body.Round; r != nil {
			out = append(out, r)
		}
	}
	return out
}

// The deadline ends a round's turns: those still running are stopped and
// count as interrupted.
func TestTheDeadlineEndsTheTurnsStillRunning(t *testing.T) {
	h := newHarness(t)
	h.scripts[runtime.Name] = ending("")
	id := h.open("checkout fails")
	run := h.state(id).Run.ID

	h.play()
	h.clock.Advance(2 * time.Minute)
	require.Contains(t, h.turns.stopped, string(run)+"/"+runtime.Name, "the deadline stops it, before the round's end is decided")
	require.Nil(t, h.state(id).Run.Rounds[0].Ended)
	h.clock.Advance(h.opts.Grace)

	ended := h.state(id).Run.Rounds[0].Ended
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnInterrupted}, ended.Turns)
}

// A round whose every turn failed at the model vendor ends at its
// deadline, not earlier, and is never quiet: a vendor outage cannot spend
// the run's rounds in seconds.
func TestARoundOfVendorFailuresEndsAtItsDeadline(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name], h.scripts[runtime.Name] = ending(item.TurnVendor), ending(item.TurnVendor)
	id := h.open("checkout fails")

	h.play()
	h.clock.Advance(h.opts.Grace)
	require.Len(t, h.state(id).Run.Rounds, 1, "every turn ended, but the round goes on")
	h.clock.Advance(2*time.Minute - h.opts.Grace)
	h.clock.Advance(h.opts.Grace)

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	ended := s.Run.Rounds[0].Ended
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.False(t, ended.Quiet)
	assert.Equal(t, item.TurnVendor, ended.Turns[release.Name])
}

// A round whose every turn ended unavailable, the platform unable to serve
// it, or unavailable and at the model vendor, ends at its deadline, not
// earlier, as a round of vendor failures does: an outage of the platform
// cannot spend the run's rounds in seconds (Decision 13). The turns count as they
// ended: unavailable is no crash, so the round removes no agent, and it is
// not quiet.
func TestARoundWhoseEveryTurnWasUnavailableWaitsForItsLimit(t *testing.T) {
	for name, classes := range map[string][2]item.TurnClass{
		"every turn unavailable":             {item.TurnUnavailable, item.TurnUnavailable},
		"one unavailable, one at the vendor": {item.TurnUnavailable, item.TurnVendor},
	} {
		h := newHarness(t)
		h.scripts[release.Name], h.scripts[runtime.Name] = ending(classes[0]), ending(classes[1])
		id := h.open("checkout fails")

		h.play()
		h.clock.Advance(h.opts.Grace)
		require.Nil(t, h.state(id).Run.Rounds[0].Ended, "%s: every turn ended, but the round goes on", name)
		h.clock.Advance(2*time.Minute - h.opts.Grace)
		h.clock.Advance(h.opts.Grace)

		s := h.state(id)
		require.Len(t, s.Run.Rounds, 2, name)
		ended := s.Run.Rounds[0].Ended
		assert.Equal(t, item.EndedByDeadline, ended.EndedBy, name)
		assert.Equal(t, map[string]item.TurnClass{release.Name: classes[0], runtime.Name: classes[1]}, ended.Turns, name)
		assert.False(t, ended.Quiet, name)
		assert.Empty(t, ended.Removed, name)
	}
}

// A round's end waits for the round's model calls in flight, up to the
// settling wait, so that their cost counts: it reads the spend again once
// they ended, or once the wait is over, and says whether they settled.
func TestARoundsEndWaitsForTheRunsCallsInFlight(t *testing.T) {
	for name, c := range map[string]struct {
		after   time.Duration
		settled bool
	}{
		"they end":    {after: 3 * time.Second, settled: true},
		"they go on":  {after: time.Hour, settled: false},
		"none at all": {after: 0, settled: true},
	} {
		h := newHarness(t)
		id := h.open("checkout fails")
		run := string(h.state(id).Run.ID)
		if c.after > 0 {
			h.spend.set(func(s *spend) { s.inFlight[roundOf{run, 1}] = 1 })
		}

		h.play()
		h.clock.Advance(h.opts.Grace)
		if c.after > 0 {
			assert.Nil(t, h.state(id).Run.Rounds[0].Ended, name)
			h.spend.set(func(s *spend) { s.spent[run] = 700 })
			if c.after < h.opts.SettleWait {
				h.clock.Advance(c.after - time.Second)
				h.spend.set(func(s *spend) { s.inFlight[roundOf{run, 1}] = 0 })
			}
			h.clock.Advance(h.opts.SettleWait)
		}

		ended := h.state(id).Run.Rounds[0].Ended
		require.NotNil(t, ended, name)
		assert.Equal(t, c.settled, ended.Settled, name)
		if c.after > 0 {
			assert.Equal(t, int64(700), ended.Spent, name)
		}
	}
}

// A model call of an earlier round still in flight does not keep a later
// round unsettled (Decision 9): a round's end waits only for its own round's
// model calls. Here round 1's call hangs, so round 1 ends unsettled once the
// settling wait is over; round 2's end, which round 1's call does not
// concern, waits for nothing and is settled.
func TestACallHungFromAnEarlierRoundDoesNotKeepALaterRoundUnsettled(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name] = postsATask
	id := h.open("checkout fails")
	run := string(h.state(id).Run.ID)
	h.spend.set(func(s *spend) { s.inFlight[roundOf{run, 1}] = 1 })
	h.round()
	h.clock.Advance(h.opts.SettleWait)
	first := h.state(id).Run.Rounds[0].Ended
	require.NotNil(t, first)
	require.False(t, first.Settled, "round 1's own call hung")

	h.round()

	second := h.state(id).Run.Rounds[1].Ended
	require.NotNil(t, second, "round 2's end waited for round 1's call")
	assert.True(t, second.Settled)
}

// Each model call refused, failed or of unknown outcome is noticed on the
// run's board once, at the end of the round in which its outcome is first
// read, naming the call and its agent.
func TestAFailedModelCallIsNoticedOnce(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name] = ending(item.TurnRefused)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.spend.set(func(s *spend) {
		s.failed[string(run)] = []app.ModelCall{{ID: "m-1", Agent: release.Name, Status: "refused"}}
	})

	h.round()
	h.spend.set(func(s *spend) {
		s.failed[string(run)] = append(s.failed[string(run)], app.ModelCall{ID: "m-2", Agent: runtime.Name, Status: "unknown"},
			app.ModelCall{ID: "m-3", Agent: runtime.Name, Status: "error"})
	})
	h.round()

	var notices []string
	for _, e := range h.events(run) {
		if n := e.Body.Notice; n != nil {
			notices = append(notices, string(n.Code)+" "+n.Agent+" "+n.CallID+" "+strings.Repeat("r", e.Round))
		}
	}
	assert.Equal(t, []string{"model_refused release-investigator m-1 r", "model_unknown runtime-investigator m-2 rr", "model_failed runtime-investigator m-3 rr"}, notices)
}

// A round whose model calls were still in flight when the wait for them
// ended, or were noticed in it as refused, failed or of unknown outcome, is
// never quiet, though its agents ended their turns on the board and nothing
// else was posted: the agents did not have their chance. The run goes on.
func TestARoundWhoseModelCallsFailedOrDidNotSettleIsNeverQuiet(t *testing.T) {
	for name, vendor := range map[string]func(s *spend, run string){
		"its calls still in flight": func(s *spend, run string) { s.inFlight[roundOf{run, 1}] = 2 },
		"its calls failed": func(s *spend, run string) {
			s.failed[run] = []app.ModelCall{{ID: "call-1", Agent: release.Name, Status: "error"}, {ID: "call-2", Agent: runtime.Name, Status: "unknown"}}
		},
		"a call refused": func(s *spend, run string) {
			s.failed[run] = []app.ModelCall{{ID: "call-1", Agent: release.Name, Status: "refused"}}
		},
	} {
		h := newHarness(t)
		id := h.open("checkout fails")
		run := h.state(id).Run.ID
		h.play()
		h.spend.set(func(s *spend) { vendor(s, string(run)) })

		h.clock.Advance(h.opts.Grace + h.opts.SettleWait)

		s := h.state(id)
		ended := s.Run.Rounds[0].Ended
		require.NotNil(t, ended, name)
		assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, ended.Turns, name)
		assert.Equal(t, []string{release.Name, runtime.Name}, ended.TurnEnds, name)
		assert.False(t, ended.Quiet, name)
		assert.Nil(t, s.Run.Stopped, "%s: the run goes on", name)
	}
}

// The model calls noticed in a round count when its end is decided again, as
// after a write of its end that failed: the notices posted the first time are
// on the board, and are not posted again.
func TestTheCallsNoticedBeforeARoundsEndFailedCountWhenItIsDecidedAgain(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.spend.set(func(s *spend) {
		s.failed[string(run)] = []app.ModelCall{{ID: "call-1", Agent: release.Name, Status: "error"}}
	})
	h.records.set(func(n int) error {
		if n == 5 {
			return errors.New("the database blinked")
		}
		return nil
	})
	h.round()
	require.Nil(t, h.state(id).Run.Rounds[0].Ended, "the round's end was not recorded")

	h.clock.Advance(h.opts.Retry)

	ended := h.state(id).Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.False(t, ended.Quiet, "the call noticed before the end failed counts")
	notices := 0
	for _, e := range h.events(run) {
		if e.Body.Notice != nil {
			notices++
		}
	}
	assert.Equal(t, 1, notices, "the call is noticed once")
}

// A round's end reads the round's failed tool calls from the MCP gateway's
// records once its write calls had their grace (Decision 9, R2): it asks for
// the round's run and number, and a count above zero leaves the round not
// quiet, though its agents ended their turns on the board and nothing else
// was posted. The round's end records the count. A failed tool call of an
// earlier round does not count in a later one: round 2, alike but for it, is
// quiet.
func TestARoundsEndReadsItsFailedToolCalls(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := string(h.state(id).Run.ID)
	h.tools.set(func(c *toolCalls) { c.failed[roundOf{run, 1}] = 1 })
	h.play()
	first := h.clock.Now()
	h.clock.Advance(h.opts.Grace - time.Second)
	require.Empty(t, h.tools.noted(), "not before the grace")

	h.clock.Advance(time.Second)
	h.play()
	second := h.clock.Now()
	h.clock.Advance(h.opts.Grace)

	assert.Equal(t, []askedFor{{run: run, round: 1, at: first.Add(h.opts.Grace)}, {run: run, round: 2, at: second.Add(h.opts.Grace)}}, h.tools.noted())
	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	ended := s.Run.Rounds[0].Ended
	assert.Equal(t, []string{release.Name, runtime.Name}, ended.TurnEnds)
	assert.Equal(t, 1, ended.FailedToolCalls)
	assert.False(t, ended.Quiet)
	assert.Equal(t, item.Decision{Next: item.NextRound}, ended.Decision)
	assert.Zero(t, s.Run.Rounds[1].Ended.FailedToolCalls)
	assert.True(t, s.Run.Rounds[1].Ended.Quiet)
	assert.Equal(t, item.StopQuiet, s.Run.Stopped.Reason)
}

// A coordinator needs the record of the round's tool calls, as it needs every
// port: a round's end that could not read its failed tool calls could not
// tell a quiet round (Decision 9).
func TestACoordinatorNeedsTheToolCalls(t *testing.T) {
	h := newHarness(t)
	ports := h.ports()
	ports.ToolCalls = nil

	_, err := app.NewCoordinator(ports, &h.opts)

	require.Error(t, err)
}

// An item opened once the day's spend reached the daily cap is not staffed,
// for the cause daily_cap: it awaits its close, with no run and no turn.
func TestAnItemOpenedOverTheDailyCapIsNotStaffed(t *testing.T) {
	h := newHarness(t)
	h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })

	id := h.open("checkout fails")

	s := h.state(id)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(id))
	assert.Equal(t, item.StopNotStaffed, s.Closing.Reason)
	assert.Equal(t, item.CauseDailyCap, s.Closing.Cause)
	assert.Empty(t, h.turns.started)
}

// A run whose round ends once the day's spend reached the cap stops cut,
// and its item is not staffed.
func TestARunIsCutAtTheDailyCap(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name], h.scripts[runtime.Name] = hypothesis, hypothesis
	id := h.open("checkout fails")
	h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })

	h.round()

	s := h.state(id)
	assert.Equal(t, item.StopCut, s.Run.Stopped.Reason)
	assert.Equal(t, item.StopNotStaffed, s.Closing.Reason)
}

// An item whose type no agent subscribes to is not staffed: its run stops
// at once, with a no_agents notice on its board.
func TestAnItemWithNoAgentIsNotStaffed(t *testing.T) {
	h := newHarness(t)

	id, err := h.c.Open(t.Context(), &app.OpenInput{Type: "change", Title: "a change", Source: "person"})
	require.NoError(t, err)

	s := h.state(id)
	assert.Equal(t, item.StopNotStaffed, s.Run.Stopped.Reason)
	assert.Equal(t, item.StopNotStaffed, s.Closing.Reason)
	events := h.events(s.Run.ID)
	require.Len(t, events, 2)
	assert.Equal(t, &board.Notice{Code: board.NoticeNoAgents}, events[0].Body.Notice)
	assert.Equal(t, "not_staffed", events[1].Body.Stop.Reason)
	assert.Empty(t, h.turns.started)
}

// An agent whose turn crashed in two rounds in a row leaves the run: its
// session ends, and the next round invites the others.
func TestAnAgentThatCrashesTwiceLeavesTheRun(t *testing.T) {
	h := newHarness(t)
	h.scripts[runtime.Name] = ending(item.TurnCrashed)
	h.scripts[release.Name] = func(h *harness, t *app.Turn) item.TurnClass {
		r := h.call(t, "post_task", `{"title":"check the deploy"}`)
		require.Equal(h.t, actionsapp.StatusOK, r.Status, r.Text)
		return endTurn(h, t)
	}
	id := h.open("checkout fails")

	h.round()
	second := h.round()
	third := h.turns.take()

	s := h.state(id)
	assert.Equal(t, []string{runtime.Name}, s.Run.Rounds[1].Ended.Removed)
	require.Len(t, third, 1)
	assert.Equal(t, release.Name, third[0].Agent)
	for _, turn := range second {
		if turn.Agent == runtime.Name {
			_, err := h.registry.MCP().Session(t.Context(), turn.MCP.Reveal())
			require.ErrorIs(t, err, actionsapp.ErrNoSession)
		}
	}
}

// manyAgents makes current a revision of the harness's configuration with
// twelve agents more subscribed to the incident type, whose items invite up
// to sixteen: a round of an item opened then has fourteen agents, so that a
// map of them iterates in the order its hash decides, not in the order they
// went in (Go's maps of eight entries or fewer only rotate it). It returns
// the fourteen, sorted.
func manyAgents(h *harness) []string {
	h.t.Helper()
	agents := []string{release.Name, runtime.Name}
	h.revs.set(h.revision(strings.Repeat("e", 40), func(files map[string][]byte) {
		files["work-item-types/incident.yaml"] = append(files["work-item-types/incident.yaml"], "  team: {maxAgents: 16}\n"...)
		for i := range 12 {
			name := fmt.Sprintf("agent-%02d", i+1)
			files["agents/"+name+".yaml"] = agent(name, "incident")
			agents = append(agents, name)
		}
	}))
	slices.Sort(agents)
	return agents
}

// stopsSince returns the turns the runner was told to stop, as run/agent, from
// the nth on.
func stopsSince(h *harness, n int) []string {
	h.turns.mu.Lock()
	defer h.turns.mu.Unlock()
	return slices.Clone(h.turns.stopped[n:])
}

// stopsOf are the stops of run's turns of agents, in their order.
func stopsOf(run board.RunID, agents []string) []string {
	out := make([]string, 0, len(agents))
	for _, a := range agents {
		out = append(out, string(run)+"/"+a)
	}
	return out
}

// The deadline stops the turns still running in the order of their agents
// (R33), so that a seed of the interleaving test replays its order of port
// events: here the round's fourteen turns run at the deadline. Fails first:
// with the loop over the running turns in their map's order.
func TestTheDeadlineStopsTheRunningTurnsInTheOrderOfTheirAgents(t *testing.T) {
	h := newHarness(t)
	agents := manyAgents(h)
	id := h.open("checkout fails")
	s := h.state(id)
	require.ElementsMatch(t, agents, s.Run.Latest().Opened.Agents)
	stopped := len(stopsSince(h, 0))

	advanceTo(h, s.Run.Latest().Opened.Deadline)

	assert.Equal(t, stopsOf(s.Run.ID, agents), stopsSince(h, stopped))
}

// The deadline stops the turns still starting, as those whose launch is
// pending, in the order of their agents (R33): here the round's fourteen
// launches are all pending at the deadline. Fails first: with the loop over
// the starting turns in their map's order.
func TestTheDeadlineStopsTheStartingTurnsInTheOrderOfTheirAgents(t *testing.T) {
	h := newHarness(t)
	h.turns.pending = true
	agents := manyAgents(h)
	id := h.open("checkout fails")
	s := h.state(id)
	require.ElementsMatch(t, agents, s.Run.Latest().Opened.Agents)
	stopped := len(stopsSince(h, 0))

	advanceTo(h, s.Run.Latest().Opened.Deadline)

	assert.Equal(t, stopsOf(s.Run.ID, agents), stopsSince(h, stopped))
}

// sessionEnds is an instance's live sessions, which note each session that
// ends alone, as run/agent, in order.
type sessionEnds struct {
	app.Sessions
	order *trace
}

func (s sessionEnds) End(run board.RunID, agent string) {
	s.order.note(string(run) + "/" + agent)
	s.Sessions.End(run, agent)
}

// A round's hold ends the sessions of the agents that left the run in the
// order of their agents (R33): here every agent but one crashes in two rounds
// in a row and leaves the run, and the third round's hold ends the thirteen
// sessions. Fails first: with the loop over the sessions in their map's
// order.
func TestAHoldEndsTheSessionsOfTheAgentsThatLeftInTheOrderOfTheirAgents(t *testing.T) {
	h := newHarness(t)
	agents := manyAgents(h)
	ends := &trace{}
	ports := h.ports()
	ports.Sessions = sessionEnds{Sessions: ports.Sessions, order: ends}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
	left := slices.DeleteFunc(slices.Clone(agents), func(a string) bool { return a == release.Name })
	for _, a := range left {
		h.scripts[a] = ending(item.TurnCrashed)
	}
	id := h.open("checkout fails")

	h.round()
	h.round()

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 3, "the third round opened")
	require.Equal(t, left, s.Run.Rounds[1].Ended.Removed)
	assert.Equal(t, stopsOf(s.Run.ID, left), ends.seen())
}

// A run stops when its budget is used: here its three rounds.
func TestARunStopsWhenItsRoundsAreUsed(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name] = func(h *harness, t *app.Turn) item.TurnClass {
		r := h.call(t, "post_task", `{"title":"check round `+strings.Repeat("i", t.Round)+`"}`)
		require.Equal(h.t, actionsapp.StatusOK, r.Status, r.Text)
		return endTurn(h, t)
	}
	id := h.open("checkout fails")

	var last []app.Turn
	for range 3 {
		last = h.round()
	}

	s := h.state(id)
	assert.Len(t, s.Run.Rounds, 3)
	assert.Equal(t, item.StopBudgetExhausted, s.Run.Stopped.Reason)
	assert.Contains(t, last[0].Prompt, "Round 3 of this investigation: it is the last")
}

// The title is redacted with the rules that apply always before it is
// recorded or posted.
func TestTheTitleIsRedactedBeforeItIsRecorded(t *testing.T) {
	h := newHarness(t)

	id := h.open("key sk-abcdefgh leaked")

	s := h.state(id)
	assert.Equal(t, "key [redacted:api-keys] leaked", s.Opened.Title)
	assert.NotContains(t, h.events(s.Run.ID)[0].Body.Evidence.Claim, "sk-abcdefgh")
}

// A title whose escapes may hide what a rule that applies always removes is
// recorded as a fixed text instead: the records are append-only, so a title
// kept as written could never be redacted.
func TestATitleThatHidesAMatchBehindAnEscapeIsReplaced(t *testing.T) {
	h := newHarness(t)

	id := h.open("key sk-\\u0061bcdefgh leaked")

	s := h.state(id)
	assert.Equal(t, "[a title the redaction rules could not read]", s.Opened.Title)
	assert.Equal(t, "Opened by person: [a title the redaction rules could not read]; service=checkout", h.events(s.Run.ID)[0].Body.Evidence.Claim)
}

// A title is recorded cut to 200 characters, the last of them a mark that it
// was cut; a title of 200 characters is kept whole, whatever its bytes.
func TestALongTitleIsRecordedCut(t *testing.T) {
	h := newHarness(t)

	long := h.state(h.open(strings.Repeat("é", 250))).Opened.Title
	whole := h.state(h.open(strings.Repeat("é", 200))).Opened.Title

	assert.Equal(t, strings.Repeat("é", 199)+"…", long)
	assert.Equal(t, strings.Repeat("é", 200), whole)
}

// A title that holds a character a reader could miss is recorded as a fixed
// text instead, as one that hides a match behind an escape is (Decision 11):
// replaced, not refused, so that no opening fails for its title. A title is
// checked once it is trimmed, so one whose only such character trails it is
// trimmed, not replaced (R44).
func TestATitleWithACharacterAReaderCouldMissIsReplaced(t *testing.T) {
	h := newHarness(t)
	for raw, want := range map[string]string{
		"checkout\tfails":  "[a title with characters a reader could miss]",
		"a\u200bb":         "[a title with characters a reader could miss]",
		"checkout fails":   "checkout fails",
		"checkout fails\n": "checkout fails",
	} {
		id := h.open(raw)

		assert.Equal(t, want, h.state(id).Opened.Title, "%q", raw)
	}
}

// An item of a type that does not exist, or whose key reads its values
// from a tool when the item opens, which Plan 7c builds, is refused, and
// nothing is recorded.
func TestAnItemOfATypeTheCoordinatorCannotRunIsRefused(t *testing.T) {
	h := newHarness(t)
	for _, typ := range []string{"outage", "catalogued"} {
		_, err := h.c.Open(t.Context(), &app.OpenInput{Type: typ, Title: "t", Source: "person"})

		require.ErrorIs(t, err, app.ErrInvalid, typ)
	}
	active, err := h.records.Active(t.Context())
	require.NoError(t, err)
	assert.Empty(t, active)
}

// openRouted opens an incident with routing, as a person does.
func openRouted(h *harness, routing map[string]string) (item.ID, error) {
	h.t.Helper()
	return h.c.Open(h.t.Context(), &app.OpenInput{Type: "incident", Title: "checkout fails", Source: "person", Routing: routing})
}

// requireNothingRecorded fails unless the harness holds no item that runs and
// has started no turn.
func requireNothingRecorded(h *harness) {
	h.t.Helper()
	active, err := h.records.Active(h.t.Context())
	require.NoError(h.t, err)
	assert.Empty(h.t, active)
	assert.Empty(h.t, h.turns.started)
}

// An item whose routing hides, behind a JSON escape, what a rule that
// applies always removes is refused, and nothing is recorded: the routing
// reaches the board in the opening's claim, and a snapshot that holds such
// text makes the LLM gateway refuse every model call that reads it. The
// escape may be in a value or in a key, and a rule may read each alone, or
// both as the member key=value.
func TestRoutingThatHidesAMatchBehindAnEscapeIsRefused(t *testing.T) {
	h := newHarness(t)
	h.revs.set(h.revision(strings.Repeat("b", 40), func(files map[string][]byte) {
		files["redaction-rules/members.yaml"] = []byte(`kind: RedactionRule
metadata: {name: members, owner: security@example.com}
spec:
  always: true
  patterns: ['^token[0-9]{4}$', 'api-key$', 'password=[a-z0-9]{8}']
`)
	}))
	for name, routing := range map[string]map[string]string{
		"a value":                            {"service": "sk-\\u0061bcdefgh"},
		"a key":                              {"sk-\\u0061bcdefgh": "checkout"},
		"a value that a rule reads whole":    {"service": "tok\\u0065n1234"},
		"a key that a rule reads to its end": {"api\\u002dkey": "checkout"},
		"a key and a value read together":    {"pass\\u0077ord": "s3cr3t42"},
	} {
		_, err := openRouted(h, routing)

		require.ErrorIs(t, err, app.ErrInvalid, name)
	}
	requireNothingRecorded(h)
}

// An item whose routing holds text that a rule that applies always removes is
// refused, and nothing is recorded: routing is recorded as given, since write
// tools' scope bindings compare its values, and a value that redaction would
// change is no value to compare (spec Section 3.6). A rule may read a key or a
// value alone, or both as the member key=value, as a rule for password=...
// reads the routing {"password": "..."}: recorded, its value would stay in
// item_opened, an append-only audit record, while the board's claim showed it
// redacted (spec Section 9.4). Routing that no such rule reads opens.
func TestRoutingThatTheAlwaysRulesWouldChangeIsRefused(t *testing.T) {
	h := newHarness(t)
	h.revs.set(h.revision(strings.Repeat("f", 40), func(files map[string][]byte) {
		files["redaction-rules/passwords.yaml"] = []byte(`kind: RedactionRule
metadata: {name: passwords, owner: security@example.com}
spec:
  always: true
  patterns: ['password=\S+']
`)
	}))
	for name, routing := range map[string]map[string]string{
		"a value":                         {"service": "sk-abcdefgh"},
		"a key":                           {"sk-abcdefgh": "checkout"},
		"a key and a value read together": {"password": "s3cr3t42"},
	} {
		_, err := openRouted(h, routing)

		require.ErrorIs(t, err, app.ErrInvalid, name)
	}
	requireNothingRecorded(h)
	_, err := openRouted(h, map[string]string{"service": "checkout", "password-policy": "strict"})
	require.NoError(t, err, "routing that no rule changes opens")
}

// An item whose routing holds a character a reader could miss, in a key or in
// a value, is refused, and nothing is recorded (Decision 11): the routing
// reaches the opening's claim on the board, where a line feed in a value
// would show a second line that reads as another routing member, and write
// tools' scope bindings. Routing with a space opens.
func TestRoutingWithACharacterAReaderCouldMissIsRefused(t *testing.T) {
	h := newHarness(t)
	for name, routing := range map[string]map[string]string{
		"a line feed in a value":        {"service": "checkout\nservice=payments"},
		"a tab in a key":                {"ser\tvice": "checkout"},
		"a zero-width space in a value": {"service": "check\u200bout"},
	} {
		_, err := openRouted(h, routing)

		require.ErrorIs(t, err, app.ErrInvalid, name)
	}
	requireNothingRecorded(h)
	id, err := openRouted(h, map[string]string{"service": "check out"})
	require.NoError(t, err, "a space is no character a reader could miss")
	assert.Equal(t, map[string]string{"service": "check out"}, h.state(id).Opened.Routing)
}

// An item's routing holds at most 16 keys, each of 1 to 64 characters, with
// values of at most 256 characters: the bounds are inclusive, and count
// characters, not bytes. Routing within them is recorded as given.
func TestRoutingIsBounded(t *testing.T) {
	keys := func(n int) map[string]string {
		routing := map[string]string{}
		for i := range n {
			routing[fmt.Sprintf("key-%02d", i)] = "v"
		}
		return routing
	}
	h := newHarness(t)
	for name, c := range map[string]struct {
		routing map[string]string
		refused bool
	}{
		"17 keys":                            {routing: keys(17), refused: true},
		"16 keys":                            {routing: keys(16)},
		"an empty key":                       {routing: map[string]string{"": "v"}, refused: true},
		"a key of 65 characters":             {routing: map[string]string{strings.Repeat("k", 65): "v"}, refused: true},
		"a key of 64 characters":             {routing: map[string]string{strings.Repeat("k", 64): "v"}},
		"a key of 64 two-byte characters":    {routing: map[string]string{strings.Repeat("é", 64): "v"}},
		"a value of 257 characters":          {routing: map[string]string{"service": strings.Repeat("v", 257)}, refused: true},
		"a value of 256 characters":          {routing: map[string]string{"service": strings.Repeat("v", 256)}},
		"a value of 256 two-byte characters": {routing: map[string]string{"service": strings.Repeat("é", 256)}},
	} {
		id, err := openRouted(h, c.routing)

		if c.refused {
			require.ErrorIs(t, err, app.ErrInvalid, name)
			continue
		}
		require.NoError(t, err, name)
		assert.Equal(t, c.routing, h.state(id).Opened.Routing, name)
	}
}

// An opening whose claim runs past 2,000 characters is posted cut to 2,000,
// the last of them a mark that it was cut: the board refuses a longer claim,
// and the item would never open its first round. Routing within its bounds
// can make a claim that long.
func TestALongOpeningClaimIsPostedCut(t *testing.T) {
	h := newHarness(t)
	routing := map[string]string{}
	var full strings.Builder
	full.WriteString("Opened by person: checkout fails")
	for i := range 16 {
		key, value := fmt.Sprintf("key-%02d", i), strings.Repeat("v", 256)
		routing[key] = value
		fmt.Fprintf(&full, "; %s=%s", key, value)
	}

	id, err := openRouted(h, routing)

	require.NoError(t, err)
	s := h.state(id)
	require.NotNil(t, s.Run)
	events := h.events(s.Run.ID)
	require.NotEmpty(t, events, "the first round opens")
	claim := events[0].Body.Evidence.Claim
	assert.Equal(t, string([]rune(full.String())[:1999])+"…", claim)
	assert.Equal(t, 2000, utf8.RuneCountInString(claim))
}

// A turn's end reported twice, late, for a round that is over, of another
// turn, or as a class the runner does not report changes nothing.
func TestALateOrRepeatedExitChangesNothing(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	turn := h.turns.started[0]
	require.Equal(t, release.Name, turn.Agent)

	for _, e := range []app.Exit{
		{Run: run, Agent: release.Name, Round: 1, Turn: "another-turn", Class: item.TurnEnded},
		{Run: run, Agent: release.Name, Round: 1, Class: item.TurnEnded},
		{Run: run, Agent: release.Name, Round: 1, Turn: turn.ID, Class: "lost"},
		{Run: run, Agent: release.Name, Round: 1, Turn: turn.ID, Class: item.TurnUnstarted},
		{Run: run, Agent: release.Name, Round: 1, Turn: turn.ID, Class: item.TurnCrashed},
		{Run: run, Agent: release.Name, Round: 1, Turn: turn.ID, Class: item.TurnEnded},
		{Run: run, Agent: release.Name, Round: 2, Turn: turn.ID, Class: item.TurnEnded},
		{Run: "run-404", Agent: release.Name, Round: 1, Turn: turn.ID, Class: item.TurnEnded},
		{Run: run, Agent: "stranger", Round: 1, Turn: turn.ID, Class: item.TurnEnded},
	} {
		h.c.Exited(t.Context(), e)
	}
	h.play()
	h.clock.Advance(h.opts.Grace)

	assert.Equal(t, item.TurnCrashed, h.state(id).Run.Rounds[0].Ended.Turns[release.Name])
}

// Each round's sessions have tokens of their own: a turn of an earlier round
// still running cannot act in the next.
func TestEachRoundsSessionsHaveTokensOfTheirOwn(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name] = func(h *harness, t *app.Turn) item.TurnClass {
		r := h.call(t, "post_task", `{"title":"check round `+strings.Repeat("i", t.Round)+`"}`)
		require.Equal(h.t, actionsapp.StatusOK, r.Status, r.Text)
		return endTurn(h, t)
	}
	h.open("checkout fails")

	first := h.round()
	second := h.turns.take()

	require.Len(t, second, 2)
	for i := range second {
		require.Equal(t, first[i].Agent, second[i].Agent)
		assert.NotEqual(t, first[i].MCP.Reveal(), second[i].MCP.Reveal())
		assert.NotEqual(t, first[i].LLM.Reveal(), second[i].LLM.Reveal())
		_, err := h.registry.MCP().Session(t.Context(), first[i].MCP.Reveal())
		require.ErrorIs(t, err, actionsapp.ErrNoSession)
		_, err = h.registry.LLM().Session(t.Context(), first[i].LLM.Reveal())
		require.ErrorIs(t, err, llmapp.ErrNoSession)
		s, err := h.registry.MCP().Session(t.Context(), second[i].MCP.Reveal())
		require.NoError(t, err)
		assert.Equal(t, 2, s.Round)
	}
}

// A stopped run's sessions and turns end before its stop entry is posted,
// which may take tries: no agent acts on a run that stopped.
func TestAStoppedRunsSessionsAndTurnsEndBeforeItsStopEntry(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	played := h.play()
	h.store.set(func(int) error { return errors.New("the board's store is down") })

	h.clock.Advance(h.opts.Grace)

	require.Equal(t, item.KindRunStopped, h.kinds(id)[len(h.kinds(id))-1], "the stop entry is not posted yet")
	for _, turn := range played {
		_, err := h.registry.MCP().Session(t.Context(), turn.MCP.Reveal())
		require.ErrorIs(t, err, actionsapp.ErrNoSession)
		assert.Contains(t, h.turns.stopped, string(run)+"/"+turn.Agent)
	}
	h.store.set(nil)
	h.clock.Advance(h.opts.Retry)
	_, stopped := lastStop(h.events(run))
	assert.True(t, stopped)
	assert.Equal(t, item.StatusAwaitingClose, h.state(id).Status())
}

// A turn the runner could not start is tried again after the retry wait,
// while its round lasts, with the same id, so that a runner that started it
// after all starts it once.
func TestATurnThatCouldNotStartIsTriedAgain(t *testing.T) {
	h := newHarness(t)
	var tried []string
	h.turns.fail = func(t *app.Turn) error {
		if t.Agent != runtime.Name || len(tried) == 2 {
			return nil
		}
		tried = append(tried, t.ID)
		return errors.New("the runner cannot be reached")
	}
	id := h.open("checkout fails")
	require.Len(t, h.turns.started, 1)
	h.clock.Advance(h.opts.Retry)
	require.Len(t, h.turns.started, 1)

	h.clock.Advance(h.opts.Retry)

	require.Len(t, h.turns.started, 2)
	assert.Equal(t, runtime.Name, h.turns.started[1].Agent)
	assert.Equal(t, []string{h.turns.started[1].ID, h.turns.started[1].ID}, tried)
	h.round()
	ended := h.state(id).Run.Rounds[0].Ended
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, ended.Turns)
}

// The end of a turn whose first Start failed is taken when it comes before
// the retry, as the runner may have started the turn after all: the round
// ends by its turns, with the class the runner reported, and the retry does
// not start the turn again. The end of another turn of the agent changes
// nothing.
func TestTheEndOfATurnWhoseStartFailedIsTaken(t *testing.T) {
	h := newHarness(t)
	failed := false
	h.turns.fail = func(turn *app.Turn) error {
		if turn.Agent != runtime.Name || failed {
			return nil
		}
		failed = true
		h.turns.queued, h.turns.started = append(h.turns.queued, *turn), append(h.turns.started, *turn)
		return errors.New("the runner started the turn, but its answer was lost")
	}
	h.scripts[runtime.Name] = ending(item.TurnCrashed)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID

	h.c.Exited(t.Context(), app.Exit{Run: run, Agent: runtime.Name, Round: 1, Turn: "another-turn", Class: item.TurnEnded})
	h.round()

	ended := h.state(id).Run.Rounds[0].Ended
	require.NotNil(t, ended, "the round ends once its turns ended, not at its deadline")
	assert.Equal(t, item.EndedByTurns, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnCrashed}, ended.Turns)
	assert.False(t, ended.Quiet)
	firstRound := slices.DeleteFunc(slices.Clone(h.turns.started), func(turn app.Turn) bool { return turn.Round != 1 })
	assert.Len(t, firstRound, 2, "the retry did not start the turn that ended")
}

// A turn that never started by its round's deadline counts as unstarted:
// it is not quiet, and not a crash, so that a runner out of reach removes
// no agent; the round ends at its deadline.
func TestATurnThatNeverStartsIsUnstarted(t *testing.T) {
	h := newHarness(t)
	h.turns.fail = func(t *app.Turn) error {
		if t.Agent == runtime.Name {
			return errors.New("the runner cannot be reached")
		}
		return nil
	}
	id := h.open("checkout fails")

	for range 2 {
		h.play()
		h.clock.Advance(2 * time.Minute)
		h.clock.Advance(h.opts.Grace)
	}

	s := h.state(id)
	require.Len(t, s.Run.Rounds, 3)
	for _, r := range s.Run.Rounds[:2] {
		assert.Equal(t, item.EndedByDeadline, r.Ended.EndedBy)
		assert.Equal(t, item.TurnUnstarted, r.Ended.Turns[runtime.Name])
		assert.False(t, r.Ended.Quiet)
		assert.Empty(t, r.Ended.Removed)
	}
	assert.Equal(t, []string{release.Name, runtime.Name}, s.Run.Rounds[2].Opened.Agents)
}

// An item's start does not depend on the request that opened it: once its
// opening is recorded, the request may end, as when a person's browser
// closes, and the item still starts at once.
func TestAnItemStartsThoughTheRequestThatOpenedItEnded(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	h.records.set(func(n int) error {
		if n == 2 {
			cancel()
		}
		return nil
	})

	id, err := h.c.Open(ctx, &app.OpenInput{Type: "incident", Title: "checkout fails", Source: "person"})

	require.NoError(t, err)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindStarted, item.KindRunCreated, item.KindRoundOpened}, h.kinds(id))
}

// An item runs under the revision it locks when it starts, though the
// registry moved on between its opening and its start, and moves on again
// before its run is created: its run, its sessions and its turns are the
// locked revision's, and each of its three agents ends its turn on the board
// under it, so that its round is quiet. A revision that cannot run the item's
// type leaves it not staffed, for the cause type.
func TestAnItemRunsUnderTheRevisionItLocksAtStart(t *testing.T) {
	h := newHarness(t)
	moved := h.revision(strings.Repeat("b", 40), func(files map[string][]byte) {
		files["agents/capacity-investigator.yaml"] = agent("capacity-investigator", "incident")
	})
	later := h.revision(strings.Repeat("c", 40), func(files map[string][]byte) {
		delete(files, "agents/capacity-investigator.yaml")
	})
	h.revs.next(moved, later)
	id := h.open("checkout fails")
	started := h.turns.started

	s := h.state(id)
	assert.Equal(t, moved.Commit(), s.Started.Commit)
	assert.Equal(t, []string{"capacity-investigator", release.Name, runtime.Name}, s.Run.Created.Agents, "the locked revision's agents")
	require.Len(t, started, 3)
	mcp, err := h.registry.MCP().Session(t.Context(), started[0].MCP.Reveal())
	require.NoError(t, err)
	assert.Equal(t, moved.Commit(), mcp.Commit)
	h.round()
	assert.Equal(t, []string{"capacity-investigator", release.Name, runtime.Name}, h.state(id).Run.Rounds[0].Ended.TurnEnds,
		"each agent's end_turn was taken under the locked revision")
	assert.Equal(t, item.StopQuiet, h.state(id).Run.Stopped.Reason, "its round ran under the locked revision")

	h.revs.next(h.revision(strings.Repeat("d", 40), func(files map[string][]byte) {
		files["work-item-types/incident.yaml"] = []byte(strings.Replace(string(files["work-item-types/incident.yaml"]),
			"{name: service, values: [checkout, payments]}", `{name: service, values: {tool: catalog.list_services, field: "$[*].name"}}`, 1))
	}))
	unrunnable := h.open("payments fail")
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(unrunnable))
	assert.Equal(t, item.StopNotStaffed, h.state(unrunnable).Closing.Reason)
	assert.Equal(t, item.CauseType, h.state(unrunnable).Closing.Cause)
}

// An item whose routing the revision its start would lock refuses is not
// staffed, for the cause routing, and does not start (Decision 11): the
// opening checked the routing under the revision current then, and here the
// registry moved on to a revision with a rule that applies always and matches
// the routing's value, which the item's sessions and scope bindings would
// then get as given.
func TestAnItemWhoseRoutingTheLockedRevisionRefusesIsNotStaffed(t *testing.T) {
	h := newHarness(t)
	h.revs.next(h.revision(strings.Repeat("b", 40), func(files map[string][]byte) {
		files["redaction-rules/services.yaml"] = []byte(`kind: RedactionRule
metadata: {name: services, owner: security@example.com}
spec:
  always: true
  patterns: ['^checkout$']
`)
	}))

	id := h.open("checkout fails")

	require.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(id), "no item_started")
	s := h.state(id)
	assert.Equal(t, item.StopNotStaffed, s.Closing.Reason)
	assert.Equal(t, item.CauseRouting, s.Closing.Cause)
	assert.Empty(t, h.turns.started)
}

// An agent that pins a skill version the yank list withdraws is not invited
// (spec Section 5.1).
func TestAnAgentThatPinsAYankedSkillIsNotInvited(t *testing.T) {
	h := newHarness(t)
	h.revs.set(h.revision(strings.Repeat("e", 40), func(files map[string][]byte) {
		files["skills/release-diagnosis/skill.yaml"] = []byte(`kind: Skill
metadata: {name: release-diagnosis, owner: alice@example.com}
spec:
  versions:
    - version: 1.1.0
      source: {repository: https://git.example.com/runbooks, commit: 3f2a9c1, path: release}
`)
		files["skills/release-diagnosis/1.1.0/SKILL.md"] = []byte("# Release diagnosis\n\nRead the release history first.\n")
		files["yanks.yaml"] = []byte(`kind: YankList
yanks:
  - skill: release-diagnosis@1.1.0
    by: alice@example.com
    at: 2026-10-14T09:00:00Z
    reason: it reads the wrong history
`)
		files["agents/runtime-investigator.yaml"] = []byte(strings.Replace(string(agent("runtime-investigator", "incident")),
			"  tools:", "  skills: [release-diagnosis@1.1.0]\n  tools:", 1))
	}))

	id := h.open("checkout fails")

	assert.Equal(t, []string{release.Name}, h.state(id).Run.Created.Agents)
}

// pinsDiagnosis edits a revision's files so that release-investigator pins
// the skill version diagnosis@1.0.0, and runtime-investigator diagnosis@1.1.0;
// with yank, the yank list withdraws diagnosis@1.0.0.
func pinsDiagnosis(yank bool) func(files map[string][]byte) {
	return func(files map[string][]byte) {
		files["skills/diagnosis/skill.yaml"] = []byte(`kind: Skill
metadata: {name: diagnosis, owner: alice@example.com}
spec:
  versions:
    - version: 1.0.0
      source: {repository: https://git.example.com/runbooks, commit: 3f2a9c1, path: diagnosis}
    - version: 1.1.0
      source: {repository: https://git.example.com/runbooks, commit: 9b8e7d6, path: diagnosis}
`)
		files["skills/diagnosis/1.0.0/SKILL.md"] = []byte("# Diagnosis\n\nRead the logs first.\n")
		files["skills/diagnosis/1.1.0/SKILL.md"] = []byte("# Diagnosis\n\nRead the logs and the deploys first.\n")
		for name, pin := range map[string]string{release.Name: "diagnosis@1.0.0", runtime.Name: "diagnosis@1.1.0"} {
			files["agents/"+name+".yaml"] = []byte(strings.Replace(string(agent(name, "incident")), "  tools:", "  skills: ["+pin+"]\n  tools:", 1))
		}
		if yank {
			files["yanks.yaml"] = []byte(`kind: YankList
yanks:
  - skill: diagnosis@1.0.0
    by: alice@example.com
    at: 2026-10-14T09:00:00Z
    reason: it reads the wrong logs
`)
		}
	}
}

// yankNotices returns the skill_yanked notices on a run's board.
func yankNotices(events []board.Event) []board.Event {
	var out []board.Event
	for i := range events {
		if n := events[i].Body.Notice; n != nil && n.Code == board.NoticeSkillYanked {
			out = append(out, events[i])
		}
	}
	return out
}

// An item already started keeps its locked skill versions when the registry's
// current revision yanks one, and its board gets a notice (Decision 10): at
// each round's start, each agent of the round that pins, in the locked
// revision, a version the current revision yanks gets a skill_yanked notice,
// once per run, agent and version, whatever the round. Here alpha,
// release-investigator, pins the yanked diagnosis@1.0.0: round 1's start posts
// one notice, and round 2's start none. Beta, runtime-investigator, pins
// diagnosis@1.1.0, which no yank withdraws, and gets none.
func TestARoundOfAnItemWhoseLockedSkillIsYankedGetsANoticeOnce(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name] = postsATask
	locked := h.revision(strings.Repeat("b", 40), pinsDiagnosis(false))
	// The opening reads the harness's revision, the start locks locked, and
	// each round's start reads the revision that yanks diagnosis@1.0.0.
	h.revs.next(locked, h.revision(strings.Repeat("c", 40), pinsDiagnosis(true)))

	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	first := yankNotices(h.events(run))
	h.round()

	require.Equal(t, locked.Commit(), h.state(id).Started.Commit)
	require.Equal(t, []string{release.Name, runtime.Name}, h.state(id).Run.Created.Agents, "both agents were invited under the locked revision")
	require.Len(t, h.state(id).Run.Rounds, 2, "round 2 started")
	require.Len(t, first, 1, "round 1's start posts one notice")
	assert.Equal(t, 1, first[0].Round)
	assert.Equal(t, &board.Notice{Code: board.NoticeSkillYanked, Agent: release.Name, Skill: "diagnosis@1.0.0"}, first[0].Body.Notice)
	assert.Equal(t, first, yankNotices(h.events(run)), "round 2's start posts none")
}

// While no revision is current, as before the registry loads its first, a
// round's start posts no yank notice, and its turns start (Decision 10).
func TestNoYankNoticeWhileNoRevisionIsCurrent(t *testing.T) {
	h := newHarness(t)
	// The opening reads the harness's revision, the start locks one where
	// release-investigator pins diagnosis@1.0.0, and the round's start reads
	// no revision.
	h.revs.next(h.revision(strings.Repeat("b", 40), pinsDiagnosis(false)), nil)

	id := h.open("checkout fails")

	assert.Empty(t, yankNotices(h.events(h.state(id).Run.ID)))
	assert.Len(t, h.turns.started, 2, "the round's turns start")
}

// A yank notice that cannot be posted stalls no round (Decision 10): its
// failure is logged as a step that failed, and the round's turns start and
// its deadline is armed, as when a turn_unstarted notice fails. The board's
// appends of the item's opening are its opening's evidence, its round 1's
// entry, then the notice.
func TestAYankNoticeThatFailsStallsNoRound(t *testing.T) {
	steps := &stepErrors{}
	h := newHarness(t, func(o *app.Options) { o.Log = slog.New(steps) })
	h.revs.next(h.revision(strings.Repeat("b", 40), pinsDiagnosis(false)), h.revision(strings.Repeat("c", 40), pinsDiagnosis(true)))
	h.store.set(func(n int) error {
		if n == 3 {
			return errBlinked
		}
		return nil
	})

	id := h.open("checkout fails")

	failed := steps.kept()
	require.Len(t, failed, 1, "the notice's failure is logged as a step that failed")
	require.ErrorIs(t, failed[0], errBlinked)
	assert.Empty(t, yankNotices(h.events(h.state(id).Run.ID)))
	assert.Len(t, h.turns.started, 2, "the round's turns start")
	assert.Equal(t, 1, h.clock.pending(), "the round's deadline is armed")
}

// A step that meets a later version during a holding ends what the
// coordinator holds of the item's round, and arms nothing (Decision 19): the
// run's sessions end, its agents' turns are stopped, and its timers are
// cancelled, settle's own retry among them, and no retry of the item is
// armed. The item is left alone: nothing is written to it after. Here a step
// meets a record of a later version as settle tries again to record the
// round's end, which could not be recorded at first, and as the next round's
// opening is recorded, which the lifecycle's rules refuse after it; and an
// event of a later version as the round's end reads the board.
func TestAStepThatMeetsALaterVersionEndsItsRoundAndArmsNothing(t *testing.T) {
	later := item.Body{V: item.Version + 1}
	for _, c := range []struct {
		name string
		// meet has a step of the item's run meet a record or an event of a
		// later version, and returns the turns of the round it played.
		meet func(h *harness, id item.ID) []app.Turn
	}{
		{"settle's retry", func(h *harness, id item.ID) []app.Turn {
			h.records.set(func(n int) error {
				if n == 5 {
					return errors.New("the database blinked")
				}
				return nil
			})
			played := h.round()
			require.Len(t, h.kinds(id), 4, "the round's end was not recorded")
			require.Equal(t, 2, h.clock.pending(), "the round's deadline, and settle's retry")
			appendRaw(t, h, id, later)
			h.clock.Advance(h.opts.Retry)
			require.Equal(t, item.Kind(""), lastKind(h, id), "nothing is written after the later record")
			return played
		}},
		{"the next round's opening", func(h *harness, id item.ID) []app.Turn {
			h.scripts[release.Name] = postsATask
			h.records.set(func(n int) error {
				if n == 6 {
					appendRaw(t, h, id, later)
				}
				return nil
			})
			played := h.round()
			require.Equal(t, item.Kind(""), lastKind(h, id), "nothing is written after the later record")
			return played
		}},
		{"the round's end, which reads the board", func(h *harness, id item.ID) []app.Turn {
			played := h.play()
			appendLaterEvent(t, h, h.state(id).Run.ID)
			h.clock.Advance(h.opts.Grace)
			require.Equal(t, item.KindRoundOpened, lastKind(h, id), "the round's end is not recorded")
			return played
		}},
	} {
		logs := &syncBuffer{}
		h := newHarness(t, logTo(logs))
		id := h.open("checkout fails")
		run := h.state(id).Run.ID
		stopped := len(h.turns.stopped)

		played := c.meet(h, id)

		assert.Equal(t, []item.ID{id}, h.c.Skipped(), c.name)
		for _, turn := range played {
			_, err := h.registry.MCP().Session(t.Context(), turn.MCP.Reveal())
			require.ErrorIs(t, err, actionsapp.ErrNoSession, "%s: its sessions end", c.name)
			_, err = h.registry.LLM().Session(t.Context(), turn.LLM.Reveal())
			require.ErrorIs(t, err, llmapp.ErrNoSession, "%s: its sessions end", c.name)
			assert.Contains(t, h.turns.stopped[stopped:], string(run)+"/"+turn.Agent, "%s: its agents' turns are stopped", c.name)
		}
		assert.Zero(t, h.clock.pending(), "%s: its timers are cancelled, and no retry is armed", c.name)
		kinds := h.kinds(id)
		h.clock.Advance(10 * time.Minute)
		assert.Equal(t, kinds, h.kinds(id), "%s: nothing is written to it", c.name)
		requireSkippedOnce(t, logs, id)
	}
}

// lastKind returns the kind of an item's last record.
func lastKind(h *harness, id item.ID) item.Kind {
	h.t.Helper()
	kinds := h.kinds(id)
	return kinds[len(kinds)-1]
}

// byKey is an incident's opening by key, as the admin API makes one of a
// person's request: the item's id, which the request's key derives, and the
// request's digest.
func byKey(id item.ID, digest, title string) *app.OpenInput {
	return &app.OpenInput{ID: id, Digest: digest, Type: "incident", Title: title, Source: "person", Routing: map[string]string{"service": "checkout"}}
}

// activeItems returns the items the harness holds that are not closed.
func activeItems(h *harness) []item.ID {
	h.t.Helper()
	active, err := h.records.Active(h.t.Context())
	require.NoError(h.t, err)
	return active
}

// An opening that names its item by key opens it once (Decision 14): the
// same request opened again, as a person retries it, gets the same item,
// which opened and started once, and no second item. The opening records the
// request's digest.
func TestAnOpeningByKeyReturnsTheSameItemForTheSameRequest(t *testing.T) {
	h := newHarness(t)
	in := byKey("item-by-key", "sha256:request", "checkout fails")

	first, err := h.c.Open(t.Context(), in)
	require.NoError(t, err)
	again, err := h.c.Open(t.Context(), in)

	require.NoError(t, err)
	assert.Equal(t, in.ID, first, "the item is named by its key")
	assert.Equal(t, first, again)
	assert.Equal(t, "sha256:request", h.state(first).Opened.Digest)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindStarted, item.KindRunCreated, item.KindRoundOpened}, h.kinds(first), "it opened and started once")
	assert.Equal(t, []item.ID{first}, activeItems(h), "no second item")
	assert.Len(t, h.turns.started, 2, "its round's turns started once")
}

// An opening by a key that opened an item for another request, as its digest
// tells, is refused, and leaves the item as it was: a key holds for its
// item's whole life (Decision 14). Here the item's start failed, as the
// database blinked, and waits for its retry: the refused opening writes
// nothing, starts nothing, and opens no second item, and the item starts on
// its own retry.
func TestAnOpeningByKeyWithAnotherRequestIsRefused(t *testing.T) {
	h := newHarness(t)
	h.records.set(func(n int) error {
		if n == 2 {
			return errBlinked
		}
		return nil
	})
	id, err := h.c.Open(t.Context(), byKey("item-by-key", "sha256:request", "checkout fails"))
	require.NoError(t, err)
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(id), "its start waits for its retry")
	opened := *h.state(id).Opened

	_, err = h.c.Open(t.Context(), byKey(id, "sha256:another", "payments fail"))

	require.ErrorIs(t, err, app.ErrKeyReused)
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(id), "nothing is written to the item, and it does not start")
	assert.Equal(t, opened, *h.state(id).Opened)
	assert.Equal(t, []item.ID{id}, activeItems(h), "no second item")
	assert.Empty(t, h.turns.started)
	h.clock.Advance(h.opts.Retry)
	assert.Len(t, h.turns.started, 2, "the item starts on its own retry")
}

// stepErrors is a log handler that keeps the errors of the steps that failed,
// as the coordinator logs them, so that a test tells what they wrap.
type stepErrors struct {
	mu   sync.Mutex
	errs []error
}

func (s *stepErrors) Enabled(context.Context, slog.Level) bool { return true }

// Handle keeps the error of a step that failed.
//
//nolint:gocritic // hugeParam: slog.Handler takes a record by value.
func (s *stepErrors) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "a step of a work item failed" {
		return nil
	}
	r.Attrs(func(a slog.Attr) bool {
		if err, ok := a.Value.Any().(error); ok && a.Key == "err" {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.errs = append(s.errs, err)
		}
		return true
	})
	return nil
}

func (s *stepErrors) WithAttrs([]slog.Attr) slog.Handler { return s }

func (s *stepErrors) WithGroup(string) slog.Handler { return s }

// kept returns the errors kept, in the order they were logged.
func (s *stepErrors) kept() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.errs)
}

// While the registry has no current revision, as before it loads its first,
// an opening fails with ErrNoRevision and records nothing (R19). An item
// whose opening was recorded before the revision went away waits: its start
// fails with ErrNoRevision, and is tried again, until a revision is current.
func TestOpeningAndStartingWithNoCurrentRevisionFailWithErrNoRevision(t *testing.T) {
	steps := &stepErrors{}
	h := newHarness(t, func(o *app.Options) { o.Log = slog.New(steps) })
	rev := h.revs.rev
	h.revs.set(nil)

	err := opens(h)

	require.ErrorIs(t, err, app.ErrNoRevision)
	requireNothingRecorded(h)
	h.revs.set(rev)
	// The opening reads the revision; its start, and its retry, read none.
	h.revs.next(nil)
	id := h.open("checkout fails")
	h.clock.Advance(h.opts.Retry)
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(id), "its start waits for a revision")
	failed := steps.kept()
	require.Len(t, failed, 2, "its start failed, and failed again on its retry")
	for _, err := range failed {
		require.ErrorIs(t, err, app.ErrNoRevision)
	}
	h.revs.set(rev)
	h.clock.Advance(h.opts.Retry)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindStarted, item.KindRunCreated, item.KindRoundOpened}, h.kinds(id), "it starts on the retry after")
}

// A repeated opening gets its item while the registry has no current
// revision: it opens no item, so it needs none (Decision 14). An item that
// started is returned as it is; one that never started, as its start found no
// revision, is returned and advanced, and its start fails with ErrNoRevision
// again, until a revision is current.
func TestARepeatedOpeningGetsItsItemWhileNoRevisionIsCurrent(t *testing.T) {
	steps := &stepErrors{}
	h := newHarness(t, func(o *app.Options) { o.Log = slog.New(steps) })
	started := byKey("started-by-key", "sha256:started", "checkout fails")
	_, err := h.c.Open(t.Context(), started)
	require.NoError(t, err)
	// The next opening reads the revision; its start reads none.
	h.revs.next(nil)
	queued := byKey("queued-by-key", "sha256:queued", "payments fail")
	_, err = h.c.Open(t.Context(), queued)
	require.NoError(t, err)
	require.Equal(t, []item.Kind{item.KindOpened}, h.kinds(queued.ID), "its start waits for a revision")
	require.Len(t, steps.kept(), 1)
	h.revs.set(nil)

	startedAgain, startedErr := h.c.Open(t.Context(), started)
	queuedAgain, queuedErr := h.c.Open(t.Context(), queued)

	require.NoError(t, startedErr)
	require.NoError(t, queuedErr)
	assert.Equal(t, started.ID, startedAgain)
	assert.Equal(t, queued.ID, queuedAgain)
	failed := steps.kept()
	require.Len(t, failed, 2, "the repeat advanced the item that never started, and its start failed again")
	require.ErrorIs(t, failed[1], app.ErrNoRevision)
	assert.Equal(t, []item.Kind{item.KindOpened}, h.kinds(queued.ID))
	assert.Len(t, h.turns.started, 2, "the started item's turns, once")
}

// Each turn carries the configuration's commit its item locked when it
// started, at which the runner reads the agent's skills (Decision 12): not
// the registry's current revision, which moved on since, in the item's first
// round and in its next.
func TestATurnCarriesItsItemsLockedCommit(t *testing.T) {
	h := newHarness(t)
	h.scripts[release.Name] = postsATask
	locked := h.revision(strings.Repeat("b", 40), func(map[string][]byte) {})
	h.revs.next(locked, h.revision(strings.Repeat("c", 40), func(map[string][]byte) {}))

	id := h.open("checkout fails")
	h.round()

	require.Equal(t, locked.Commit(), h.state(id).Started.Commit)
	require.Len(t, h.state(id).Run.Rounds, 2, "the item's next round opened")
	require.Len(t, h.turns.started, 4)
	for _, turn := range h.turns.started {
		assert.Equal(t, locked.Commit(), turn.Commit, "round %d's turn of %s", turn.Round, turn.Agent)
	}
}

// errEngine is how a launch fails when the engine does not answer.
var errEngine = errors.New("the engine did not answer")

// last returns the turn the runner was last asked to start for agent.
func (h *harness) last(agent string) *app.Turn {
	h.t.Helper()
	h.turns.mu.Lock()
	defer h.turns.mu.Unlock()
	for i := len(h.turns.started) - 1; i >= 0; i-- {
		if h.turns.started[i].Agent == agent {
			turn := h.turns.started[i]
			return &turn
		}
	}
	h.t.Fatalf("no turn of %s was started", agent)
	return nil
}

// startsOf counts the starts the runner was asked for, and took, of the turn
// whose id is id.
func (h *harness) startsOf(id string) int {
	h.turns.mu.Lock()
	defer h.turns.mu.Unlock()
	n := 0
	for i := range h.turns.started {
		if h.turns.started[i].ID == id {
			n++
		}
	}
	return n
}

// unstartedNotices returns the turn_unstarted notices of a run's board, each
// as its round, its agent and its reason.
func unstartedNotices(h *harness, run board.RunID) []string {
	h.t.Helper()
	var out []string
	events := h.events(run)
	for i := range events {
		if n := events[i].Body.Notice; n != nil && n.Code == board.NoticeTurnUnstarted {
			out = append(out, fmt.Sprintf("%d %s %s", events[i].Round, n.Agent, n.Reason))
		}
	}
	return out
}

// A turn whose launch Start leaves pending runs once the runner reports its
// launch (Decision 4): until then it is not played, and the round waits for
// it; once each launch is reported without an error, the turns run, and their
// ends, as end_turn ends them, end the round by its turns.
func TestALaunchingTurnRunsOnceItsLaunchIsReported(t *testing.T) {
	h := newHarness(t)
	h.turns.pending = true
	id := h.open("checkout fails")
	launching := slices.Clone(h.turns.started)
	require.Len(t, launching, 2)

	require.Empty(t, h.play(), "no turn runs while its launch is pending")
	h.clock.Advance(h.opts.Grace)
	require.Nil(t, h.state(id).Run.Rounds[0].Ended, "the round waits for its launching turns")
	for i := range launching {
		h.launch(&launching[i], "", nil)
	}
	h.round()

	ended := h.state(id).Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, item.EndedByTurns, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, ended.Turns)
}

// A turn whose launch is still pending at its round's deadline counts as
// unstarted, as a turn the runner never started does, and is stopped
// (Decision 4): no Launched came for it. Here the other agent's launch was
// reported, and its turn ended.
func TestALaunchingTurnAtTheDeadlineIsUnstartedAndStopped(t *testing.T) {
	h := newHarness(t)
	h.turns.pending = true
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.launch(h.last(release.Name), "", nil)
	h.play()

	h.clock.Advance(2 * time.Minute)

	assert.Contains(t, h.turns.stopped, string(run)+"/"+runtime.Name, "the deadline stops the launching turn")
	h.clock.Advance(h.opts.Grace)
	ended := h.state(id).Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnUnstarted}, ended.Turns)
}

// A round waits for its launching turns (Decision 4): with one agent's turn
// ended, and the other's launch still pending, the round goes on; it ends by
// its turns once the other's launch is reported and its turn ends.
func TestARoundWaitsForItsLaunchingTurns(t *testing.T) {
	h := newHarness(t)
	h.turns.pending = true
	id := h.open("checkout fails")
	h.launch(h.last(release.Name), "", nil)
	h.play()
	h.clock.Advance(h.opts.Grace)
	require.Nil(t, h.state(id).Run.Rounds[0].Ended, "the round waits for the launching turn")

	h.launch(h.last(runtime.Name), "", nil)
	h.round()

	ended := h.state(id).Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, item.EndedByTurns, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnEnded, runtime.Name: item.TurnEnded}, ended.Turns)
}

// A launch that failed is tried again after the retry wait, with the same id,
// as a Start that failed is (Decision 4): Launched with the engine's failure
// arms the retry, and Start is called again with the turn's id once the wait
// is over, not before.
func TestAFailedLaunchIsTriedAgainWithTheSameID(t *testing.T) {
	h := newHarness(t)
	h.turns.pending = true
	h.open("checkout fails")
	turn := h.last(release.Name)

	h.launch(turn, app.ReasonEngine, errEngine)
	h.clock.Advance(h.opts.Retry - time.Second)
	require.Equal(t, 1, h.startsOf(turn.ID), "not before the retry wait")
	h.clock.Advance(time.Second)

	assert.Equal(t, 2, h.startsOf(turn.ID), "started again, with the same id")
}

// A turn whose launch is reported with less than a third of the round's span
// left started too late (Decision 4), as a turn the runner started late does:
// still running at the deadline, it counts as unstarted. A turn whose launch
// is reported with more than a third left counts as interrupted there. Here
// the runtime agent's launch is reported 90 s into the round's two minutes,
// the release agent's 30 s in.
func TestALaunchReportedWithLessThanAThirdOfTheRoundLeftCountsAsStartedLate(t *testing.T) {
	h := newHarness(t)
	h.turns.pending = true
	id := h.open("checkout fails")
	h.clock.Advance(30 * time.Second)
	h.launch(h.last(release.Name), "", nil)
	h.clock.Advance(time.Minute)
	h.launch(h.last(runtime.Name), "", nil)

	h.clock.Advance(30*time.Second + h.opts.Grace)

	ended := h.state(id).Run.Rounds[0].Ended
	require.NotNil(t, ended)
	assert.Equal(t, item.EndedByDeadline, ended.EndedBy)
	assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnInterrupted, runtime.Name: item.TurnUnstarted}, ended.Turns)
}

// A launch that failed posts a turn_unstarted notice on the run's board,
// naming the agent and why (Decision 7), once per agent and round (Review
// Focus 2): the engine's failure, then, after its retry, a missing image,
// leave one notice, of the engine; after a fence and a takeover, the new
// holding's failed launch in the same round adds none; round 2's failure
// adds one.
func TestAFailedLaunchPostsOneNoticePerAgentPerRound(t *testing.T) {
	h := newHarness(t)
	h.turns.pending = true
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	first := "1 " + release.Name + " engine"

	h.launch(h.last(release.Name), app.ReasonEngine, errEngine)
	h.clock.Advance(h.opts.Retry)
	h.launch(h.last(release.Name), app.ReasonImage, errors.New("the image is missing from the engine and the store"))
	require.Equal(t, []string{first}, unstartedNotices(h, run))
	h.c.Fence()
	h.epoch++
	require.NoError(t, h.c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.launch(h.last(release.Name), app.ReasonEngine, errEngine)
	require.Equal(t, []string{first}, unstartedNotices(h, run), "the takeover's failed launch, in the same round")
	h.clock.Advance(2*time.Minute + h.opts.Grace)
	require.Len(t, h.state(id).Run.Rounds, 2)

	h.launch(h.last(release.Name), app.ReasonEngine, errEngine)

	assert.Equal(t, []string{first, "2 " + release.Name + " engine"}, unstartedNotices(h, run))
}

// A launch the holding refused, or that a stop, a fence or a drain cancelled,
// posts no notice, whatever its error says (Decision 7): a refusal means the
// instance is losing the lease, and its write would be refused. No post is
// tried: the board's store sees no append. Its retry is armed all the same,
// and Start is called again with the same id.
func TestALaunchCancelledOrRefusedByTheHoldingPostsNothing(t *testing.T) {
	for _, reason := range []app.LaunchReason{app.ReasonCancelled, app.ReasonHolding} {
		h := newHarness(t)
		h.turns.pending = true
		id := h.open("checkout fails")
		turn := h.last(release.Name)
		appends := len(h.store.writes.noted())

		h.launch(turn, reason, errors.New("the launch did not happen"))
		h.clock.Advance(h.opts.Retry)

		assert.Len(t, h.store.writes.noted(), appends, "%s: a post was tried", reason)
		assert.Empty(t, unstartedNotices(h, h.state(id).Run.ID), reason)
		assert.Equal(t, 2, h.startsOf(turn.ID), "%s: the retry, with the same id", reason)
	}
}

// A Start that fails posts a turn_unstarted notice, of the reason launch
// (Decision 7), unless its error wraps ErrUnheld: the holding refused the
// turn's start, as the process runner's holding check does, and a refusal's
// write would be refused too. Both are tried again after the retry wait, with
// the same id.
func TestAStartThatFailsPostsANoticeUnlessTheHoldingRefusedIt(t *testing.T) {
	for _, c := range []struct {
		name    string
		err     error
		notices []string
	}{
		{name: "another error", err: errors.New("the turn's output file could not be made"), notices: []string{"1 " + release.Name + " launch"}},
		{name: "the holding's refusal", err: fmt.Errorf("process: the holding does not hold: %w", app.ErrUnheld)},
	} {
		h := newHarness(t)
		var refused string
		h.turns.fail = func(turn *app.Turn) error {
			if turn.Agent != release.Name || refused != "" {
				return nil
			}
			refused = turn.ID
			return c.err
		}
		id := h.open("checkout fails")
		require.NotEmpty(t, refused, c.name)

		assert.Equal(t, c.notices, unstartedNotices(h, h.state(id).Run.ID), c.name)
		h.clock.Advance(h.opts.Retry)
		assert.Equal(t, 1, h.startsOf(refused), "%s: the retry, with the same id", c.name)
	}
}

// A Launched the coordinator no longer waits for changes nothing (C10): one
// that names another turn; one without an error, for a round its deadline
// ended; one for a round the run has left; one for a run that stopped; and
// any once the coordinator is fenced. It writes no record, posts no notice,
// starts no turn and arms no retry.
func TestALaunchedTheCoordinatorNoLongerWaitsForChangesNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		// stale readies the report of how the launch of turn, the release
		// agent's turn of round 1, ended.
		stale func(h *harness, id item.ID, turn *app.Turn) app.Launch
	}{
		{"another turn's id", func(_ *harness, _ item.ID, turn *app.Turn) app.Launch {
			l := launchOf(turn, app.ReasonEngine, errEngine)
			l.Turn = "another-turn"
			return l
		}},
		{"a round its deadline ended, reported without an error", func(h *harness, id item.ID, turn *app.Turn) app.Launch {
			h.clock.Advance(2 * time.Minute)
			require.Nil(h.t, h.state(id).Run.Rounds[0].Ended, "the round's end is not recorded yet")
			return launchOf(turn, "", nil)
		}},
		{"a round the run has left", func(h *harness, id item.ID, turn *app.Turn) app.Launch {
			h.clock.Advance(2*time.Minute + h.opts.Grace)
			require.Len(h.t, h.state(id).Run.Rounds, 2)
			return launchOf(turn, app.ReasonEngine, errEngine)
		}},
		{"a run that stopped", func(h *harness, id item.ID, turn *app.Turn) app.Launch {
			h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })
			h.clock.Advance(2*time.Minute + h.opts.Grace)
			require.Equal(h.t, item.StatusAwaitingClose, h.state(id).Status())
			return launchOf(turn, app.ReasonEngine, errEngine)
		}},
		{"a fenced coordinator", func(h *harness, _ item.ID, turn *app.Turn) app.Launch {
			h.c.Fence()
			return launchOf(turn, app.ReasonEngine, errEngine)
		}},
	} {
		h := newHarness(t)
		h.turns.pending = true
		id := h.open("checkout fails")
		l := c.stale(h, id, h.last(release.Name))
		before, timers := h.carried(), h.clock.pending()

		h.c.Launched(t.Context(), l)

		assert.Equal(t, before, h.carried(), "%s: no record, no notice, no start", c.name)
		assert.Equal(t, timers, h.clock.pending(), "%s: no retry", c.name)
	}
}

// A launch that fails after its round's deadline still posts its notice
// (Decision 7; Review Focus 2): the deadline ends the round, counts the
// launching turn as unstarted and stops it, and keeps its id, so that the
// failure the runner then reports, as a launch queued while the engine does
// not answer reports at its deadline, posts one turn_unstarted notice, naming
// the agent, of the engine; and no Start follows, since the round is over.
// The same report posts nothing once the run's next round opened; nor after
// a fence and a takeover, whose holding never knew the turn, though the run
// is still on its round; nor for a cancelled launch; nor in the window where
// hold has posted the next round's start and failed before its reset, which
// leaves the run on the round while the board is on the next (R5).
func TestALaunchThatFailsAfterItsRoundsDeadlineStillPostsItsNotice(t *testing.T) {
	for _, c := range []struct {
		name   string
		reason app.LaunchReason
		// then readies the coordinator, once the deadline ended round 1, for
		// the report.
		then    func(h *harness, id item.ID, run board.RunID)
		notices []string
	}{
		{name: "the engine's failure", reason: app.ReasonEngine, notices: []string{"1 " + release.Name + " engine"}},
		{name: "once the run's next round opened", reason: app.ReasonEngine, then: func(h *harness, id item.ID, _ board.RunID) {
			h.clock.Advance(h.opts.Grace)
			require.Len(h.t, h.state(id).Run.Rounds, 2)
		}},
		{name: "after a fence and a takeover", reason: app.ReasonEngine, then: func(h *harness, id item.ID, _ board.RunID) {
			// The takeover ends round 1, but its record of round 2's opening,
			// the item's sixth record, fails.
			h.records.set(func(n int) error {
				if n == 6 {
					return errBlinked
				}
				return nil
			})
			h.c.Fence()
			h.epoch++
			require.NoError(h.t, h.c.TakeOver(h.t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
			s := h.state(id)
			require.Len(h.t, s.Run.Rounds, 1, "the run is still on round 1")
			require.NotNil(h.t, s.Run.Rounds[0].Ended)
		}},
		{name: "a cancelled launch", reason: app.ReasonCancelled},
		{name: "in the window of the next round's start", reason: app.ReasonEngine, then: func(h *harness, id item.ID, run board.RunID) {
			// hold posts round 2's start, then its read of the round's report
			// fails.
			h.store.loads(func(events []board.Event) error {
				if len(roundEntries(events)) == 2 {
					return errBlinked
				}
				return nil
			})
			h.clock.Advance(h.opts.Grace)
			events, err := h.store.Store.Load(h.t.Context(), run)
			require.NoError(h.t, err)
			require.Len(h.t, roundEntries(events), 2, "the board is on round 2")
			require.Len(h.t, h.state(id).Run.Rounds, 2)
			require.Len(h.t, h.turns.started, 2, "round 2's turns did not start")
		}},
	} {
		h := newHarness(t)
		h.turns.pending = true
		id := h.open("checkout fails")
		run := h.state(id).Run.ID
		turn := h.last(release.Name)
		h.clock.Advance(2 * time.Minute)
		require.Contains(t, h.turns.stopped, string(run)+"/"+release.Name, "%s: the deadline stops the launching turn", c.name)
		if c.then != nil {
			c.then(h, id, run)
		}
		timers, appends := h.clock.pending(), len(h.store.writes.noted())

		h.launch(turn, c.reason, errEngine)

		h.store.loads(nil)
		assert.Len(t, h.store.writes.noted()[appends:], len(c.notices), "%s: the posts tried", c.name)
		assert.Equal(t, c.notices, unstartedNotices(h, run), c.name)
		assert.Equal(t, timers, h.clock.pending(), "%s: no retry", c.name)
		h.clock.Advance(h.opts.Retry)
		assert.Equal(t, 1, h.startsOf(turn.ID), "%s: no Start follows", c.name)
	}
}

// A round whose deadline passed before this instance acted on it forgets the
// earlier round's launching turns before missed posts its start, as hold does
// (R5): a failed launch of the earlier round, reported late, posts nothing,
// though missed fails after that post, leaving the run on the earlier round
// while the board is on the later one. Here round 1 ends at its deadline with
// both launches pending; every load of the item's records fails from round
// 2's opening until its deadline passed, so that no hold acts on round 2;
// then missed posts round 2's start, and its read of the round's report
// fails.
func TestAMissedRoundForgetsTheEarlierRoundsLaunchesBeforeItsStart(t *testing.T) {
	h := newHarness(t)
	h.turns.pending = true
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	turn := h.last(release.Name)
	h.records.loads(func(records []item.Record) error {
		if len(records) == 0 {
			return nil
		}
		if o := records[len(records)-1].Body.RoundOpened; o != nil && o.Round == 2 {
			return errBlinked
		}
		return nil
	})
	h.clock.Advance(2*time.Minute + h.opts.Grace)
	h.clock.Advance(2 * time.Minute)
	h.records.loads(nil)
	h.store.loads(func(events []board.Event) error {
		if len(roundEntries(events)) == 2 {
			return errBlinked
		}
		return nil
	})
	h.clock.Advance(h.opts.Retry)
	events, err := h.store.Store.Load(t.Context(), run)
	require.NoError(t, err)
	require.Len(t, roundEntries(events), 2, "missed posted round 2's start")
	s := h.state(id)
	require.Len(t, s.Run.Rounds, 2)
	require.Nil(t, s.Run.Rounds[1].Ended, "missed failed")
	require.Len(t, h.turns.started, 2, "no hold started round 2's turns")
	appends := len(h.store.writes.noted())

	h.launch(turn, app.ReasonEngine, errEngine)

	h.store.loads(nil)
	assert.Len(t, h.store.writes.noted(), appends, "a post was tried")
	assert.Empty(t, unstartedNotices(h, run))
}

// A notice that cannot be posted changes nothing else (Decision 7): the retry
// of the turn that could not start is armed all the same. A post that finds
// the lease gone fences the coordinator, as every write of the coordinator
// does, and hold then starts no other agent's turn and arms nothing. Here the
// Start of the round's first agent fails, and the board's store fails the
// notice's append, its third, after the item's evidence and the round's
// start.
func TestANoticeThatCannotBePostedStillArmsTheRetry(t *testing.T) {
	for _, c := range []struct {
		name   string
		err    error
		fenced bool
	}{
		{name: "an error of the store's own", err: errBlinked},
		{name: "the lease found gone", err: fmt.Errorf("append: %w", holding.ErrLeaseLost), fenced: true},
	} {
		h := newHarness(t)
		var refused string
		h.turns.fail = func(turn *app.Turn) error {
			if turn.Agent != release.Name || refused != "" {
				return nil
			}
			refused = turn.ID
			return errors.New("the runner cannot be reached")
		}
		h.store.set(func(n int) error {
			if n == 3 {
				return c.err
			}
			return nil
		})

		h.open("checkout fails")

		require.NotEmpty(t, refused, c.name)
		if c.fenced {
			assert.Empty(t, h.turns.started, "%s: no Start for the second agent", c.name)
			require.ErrorIs(t, opens(h), app.ErrNotHeld, "%s: the coordinator is fenced", c.name)
			assert.Zero(t, h.clock.pending(), "%s: nothing is armed", c.name)
			continue
		}
		require.Len(t, h.turns.started, 1, c.name)
		h.clock.Advance(h.opts.Retry)
		assert.Equal(t, 1, h.startsOf(refused), "%s: the retry, with the same id", c.name)
	}
}

// Launched posts its notice under the coordinator's own context, which
// carries the epoch of the holding it acts under, as every guarded write does
// (Decision 7), whatever context the runner reports with: here an earlier
// holding's, once the coordinator took over under a second.
func TestLaunchedPostsUnderTheHoldingsEpoch(t *testing.T) {
	h := newHarness(t)
	h.turns.pending = true
	h.open("checkout fails")
	h.c.Fence()
	h.epoch++
	require.NoError(t, h.c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	appends := len(h.store.writes.noted())

	h.c.Launched(holding.WithEpoch(t.Context(), h.epoch-1), launchOf(h.last(release.Name), app.ReasonEngine, errEngine))

	assert.Equal(t, []int64{h.epoch}, h.store.writes.noted()[appends:], "the notice's append, under the holding's epoch")
}

// The launch reasons a turn_unstarted notice gives are words the board takes
// (R6): a notice of each, engine, image and launch, posted through the
// board's rules, commits. A word that drifted would have the board refuse
// every such notice, which the coordinator logs and goes on from (Decision
// 7). Each is posted on a board of its own: the board takes one
// turn_unstarted notice per agent and round, whatever its reason.
func TestTheLaunchReasonsANoticeGivesAreTheBoardsWords(t *testing.T) {
	h := newHarness(t)
	for _, reason := range []app.LaunchReason{app.ReasonEngine, app.ReasonImage, app.ReasonLaunch} {
		run := h.state(h.open("checkout fails")).Run.ID

		posted, err := h.boards.Notice(holding.WithEpoch(t.Context(), h.epoch), run, board.Coordinator,
			&board.Notice{Code: board.NoticeTurnUnstarted, Agent: release.Name, Reason: string(reason)})

		require.NoError(t, err, reason)
		assert.False(t, posted.Repeat, reason)
		assert.Equal(t, []string{"1 " + release.Name + " " + string(reason)}, unstartedNotices(h, run), reason)
	}
}
