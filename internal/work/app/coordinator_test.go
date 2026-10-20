package app_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
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

// A round whose turns all ended leaves nothing on the board but turn ends:
// once the round's write calls had their grace, the run stops quiet. Its
// board ends with the stop entry, its sessions end, and the item awaits its
// close.
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
	stop, stopped := lastStop(h.events(run))
	require.True(t, stopped)
	assert.Equal(t, "quiet", stop.Reason)
	assert.Equal(t, 1, stop.Rounds)
	_, err := h.registry.MCP().Session(t.Context(), played[0].MCP.Reveal())
	require.ErrorIs(t, err, actionsapp.ErrNoSession)
	_, err = h.registry.LLM().Session(t.Context(), played[1].LLM.Reveal())
	require.ErrorIs(t, err, llmapp.ErrNoSession)
	assert.Zero(t, h.clock.pending(), "no timer is left")
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

// A round's end waits for the run's model calls in flight, up to the
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
			h.spend.set(func(s *spend) { s.inFlight[run] = 1 })
		}

		h.play()
		h.clock.Advance(h.opts.Grace)
		if c.after > 0 {
			assert.Nil(t, h.state(id).Run.Rounds[0].Ended, name)
			h.spend.set(func(s *spend) { s.spent[run] = 700 })
			if c.after < h.opts.SettleWait {
				h.clock.Advance(c.after - time.Second)
				h.spend.set(func(s *spend) { s.inFlight[run] = 0 })
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

// A round whose agents waited on a model vendor that hangs is never quiet,
// though nothing was posted and the deadline interrupted every turn: its
// model calls were still in flight when the wait for them ended, or were
// noticed in the round as refused, failed or of unknown outcome, so the
// agents did not have their chance. The run goes on.
func TestARoundWhoseModelCallsFailedOrDidNotSettleIsNeverQuiet(t *testing.T) {
	for name, vendor := range map[string]func(s *spend, run string){
		"its calls still in flight": func(s *spend, run string) { s.inFlight[run] = 2 },
		"its calls failed": func(s *spend, run string) {
			s.failed[run] = []app.ModelCall{{ID: "call-1", Agent: release.Name, Status: "error"}, {ID: "call-2", Agent: runtime.Name, Status: "unknown"}}
		},
		"a call refused": func(s *spend, run string) {
			s.failed[run] = []app.ModelCall{{ID: "call-1", Agent: release.Name, Status: "refused"}}
		},
	} {
		h := newHarness(t)
		h.scripts[release.Name], h.scripts[runtime.Name] = ending(""), ending("")
		id := h.open("checkout fails")
		run := h.state(id).Run.ID
		h.play()
		h.spend.set(func(s *spend) { vendor(s, string(run)) })

		h.clock.Advance(2*time.Minute + h.opts.Grace + h.opts.SettleWait)

		s := h.state(id)
		ended := s.Run.Rounds[0].Ended
		require.NotNil(t, ended, name)
		assert.Equal(t, map[string]item.TurnClass{release.Name: item.TurnInterrupted, runtime.Name: item.TurnInterrupted}, ended.Turns, name)
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

// An item opened once the day's spend reached the daily cap is not staffed:
// it awaits its close, with no run and no turn.
func TestAnItemOpenedOverTheDailyCapIsNotStaffed(t *testing.T) {
	h := newHarness(t)
	h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })

	id := h.open("checkout fails")

	s := h.state(id)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(id))
	assert.Equal(t, item.StopNotStaffed, s.Closing.Reason)
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
// locked revision's. A revision that cannot run the item's type leaves it
// not staffed.
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
	assert.Equal(t, item.StopQuiet, h.state(id).Run.Stopped.Reason, "its round ran under the locked revision")

	h.revs.next(h.revision(strings.Repeat("d", 40), func(files map[string][]byte) {
		files["work-item-types/incident.yaml"] = []byte(strings.Replace(string(files["work-item-types/incident.yaml"]),
			"{name: service, values: [checkout, payments]}", `{name: service, values: {tool: catalog.list_services, field: "$[*].name"}}`, 1))
	}))
	unrunnable := h.open("payments fail")
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindAwaitingClose}, h.kinds(unrunnable))
	assert.Equal(t, item.StopNotStaffed, h.state(unrunnable).Closing.Reason)
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
