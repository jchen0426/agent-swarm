//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// The scenarios of Decision 17 (Plan 7d-1) of faults at the model vendor and
// at the MCP servers: a vendor that hangs, answers late, fails, or answers
// what the runtime cannot read; a turn that never ends; the daily cap; a bad
// configuration commit; and servers whose reads fail, or that are down. Each
// reads the item's view (C4), and the records, calls and log lines that say
// why, as a person would. "Not quiet" means that no round's quiet is true and
// that the run did not stop quiet. The helpers the scenarios share are in
// helpers_test.go.

// readOnce is a model whose agents each read the logs once, with a tool of
// their own, and then end their turns, whatever the read answered.
func readOnce(r *harness.VendorRequest) harness.VendorReply {
	if lastText(r) == "" {
		return harness.ToolUse(reads[r.Agent], readsWith[r.Agent])
	}
	return harness.EndTurn()
}

// Scenario 4: a vendor that never answers. A round's calls never settle, and
// the calls of the round after it wait behind them until that round ends too,
// so every round is not quiet, and the run stops when its budget of rounds is
// used.
func TestAHangingVendorLeavesRoundsNotQuietAndTheBudgetStopsTheRun(t *testing.T) {
	d := quick
	d.Timing.Coordinator.SettleWait = 2 * time.Second
	env, a := serving(t, limited(2, 4*time.Second), d)
	env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply { return harness.Hang() })

	v := investigate(t, a, "hanging")

	notQuiet(t, &v)
	assert.Equal(t, "budget_exhausted", v.Stop)
	requireRounds(t, &v, 2)
	for _, r := range v.Rounds {
		assert.True(t, !r.Evidence.Settled || r.Evidence.FailedModelCalls > 0,
			"round %d: its calls did not settle, or failed: %+v", r.Round, r.Evidence)
		assert.Empty(t, r.Evidence.TurnEnds, "round %d: no turn ended", r.Round)
	}
	assert.False(t, v.Rounds[0].Evidence.Settled, "the first round's calls hang past its settling wait")
}

// Scenario 5: a vendor that answers each call after the round's time limit.
// The answers come and are recorded, so the rounds' calls settle with none
// failed, but no turn ends, and no round is quiet.
func TestASlowVendorsLateAnswersLeaveRoundsNotQuiet(t *testing.T) {
	const limit = 3 * time.Second
	env, a := serving(t, limited(2, limit), quick)
	env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply {
		return harness.EndTurn().After(limit + 1500*time.Millisecond)
	})

	v := investigate(t, a, "slow")

	notQuiet(t, &v)
	assert.Equal(t, "budget_exhausted", v.Stop)
	requireRounds(t, &v, 2)
	for _, r := range v.Rounds {
		assert.Equal(t, "deadline", r.Ended, "round %d", r.Round)
		assert.Empty(t, r.Evidence.TurnEnds, "round %d: no turn ended", r.Round)
		assert.True(t, r.Evidence.Settled, "round %d: the late answers came, and were recorded", r.Round)
		assert.Zero(t, r.Evidence.FailedModelCalls, "round %d", r.Round)
	}
}

// vendorAnswered reads the status from the reason of a model call the vendor
// failed.
var vendorAnswered = regexp.MustCompile(`^the vendor answered (\d+): `)

// answeredWith returns the status the vendor answered a model call with, as
// its reason says, or the reason when it says none.
func answeredWith(reason string) string {
	if m := vendorAnswered.FindStringSubmatch(reason); m != nil {
		return m[1]
	}
	return reason
}

// storm is the round limit of a round whose calls the vendor fails: a call
// the LLM gateway tries again (1 s, then 2 s) is answered well before the
// deadline, so that its turn ends vendor, not interrupted.
const storm = 6 * time.Second

// Scenario 6: whatever the vendor fails with, no round is quiet.
func TestVendorFailuresNeverMakeARoundQuiet(t *testing.T) {
	t.Run("(a) 529s and 500s", func(t *testing.T) {
		env, a := serving(t, limited(1, storm), quick)
		n := 0
		env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply {
			n++
			if n%2 == 1 {
				return overloaded
			}
			return apiError
		})

		v := investigate(t, a, "failing")

		notQuiet(t, &v)
		for _, r := range v.Rounds {
			assert.Positive(t, r.Evidence.FailedModelCalls, "round %d", r.Round)
			assert.Empty(t, r.Evidence.TurnEnds, "round %d", r.Round)
		}
	})

	t.Run("(b) one ok answer, then 529s", func(t *testing.T) {
		env, a := serving(t, limited(1, storm), quick)
		reading(env)
		env.Vendor().Script(func(r *harness.VendorRequest) harness.VendorReply {
			if lastText(r) == "" {
				return harness.ToolUse(reads[r.Agent], readsWith[r.Agent])
			}
			return overloaded
		})

		v := investigate(t, a, "failing-later")

		notQuiet(t, &v)
		for _, r := range v.Rounds {
			assert.Positive(t, r.Evidence.FailedModelCalls, "round %d", r.Round)
		}
		statuses := map[string][]string{}
		for _, c := range callsOf(t, a, runOf(t, env, v.ID), "model") {
			statuses[c.Agent] = append(statuses[c.Agent], c.Status)
		}
		assert.Equal(t, map[string][]string{"alpha": {"ok", "error"}, "beta": {"ok", "error"}}, statuses,
			"each turn's first call is answered, and its next fails")
	})

	t.Run("(c) a 401 storm, then a 429 storm", func(t *testing.T) {
		env, a := serving(t, limited(2, storm), quick)
		env.Vendor().Script(func(r *harness.VendorRequest) harness.VendorReply {
			if roundOf(r) == 1 {
				return unauthorized
			}
			return rateLimited
		})

		v := investigate(t, a, "storms")

		notQuiet(t, &v)
		requireRounds(t, &v, 2)
		for _, r := range v.Rounds {
			assert.Equal(t, "deadline", r.Ended, "round %d lasts its whole time limit", r.Round)
			assert.Positive(t, r.Evidence.FailedModelCalls, "round %d", r.Round)
		}
		rounds := roundsEnded(t, a, v.ID)
		require.Len(t, rounds, 2)
		for _, e := range rounds {
			assert.Equal(t, map[string]string{"alpha": "vendor", "beta": "vendor"}, e.Turns, "round %d", e.Round)
		}
		// Each storm met its round, which the script tells by the round's
		// prompt: the calls' outcomes say what the vendor answered.
		answered := map[int]map[string]string{}
		for _, c := range callsOf(t, a, runOf(t, env, v.ID), "model") {
			if answered[c.Round] == nil {
				answered[c.Round] = map[string]string{}
			}
			answered[c.Round][c.Agent] = answeredWith(c.Reason)
		}
		assert.Equal(t, map[int]map[string]string{1: {"alpha": "401", "beta": "401"}, 2: {"alpha": "429", "beta": "429"}}, answered,
			"round 1's calls met the 401 storm, and round 2's the 429 storm")
	})

	t.Run("(d) an answer with an unknown content block", func(t *testing.T) {
		env, a := serving(t, team(0), quick)
		env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply {
			return harness.VendorReply{Blocks: []map[string]any{{"type": "harness_widget", "widget": "a block no runtime reads"}}}
		})

		v := investigate(t, a, "unreadable")

		notQuiet(t, &v)
		assert.Equal(t, "not_staffed", v.Stop, "no agent is left")
		requireRounds(t, &v, 2, "two rounds of crashed turns remove the agents")
		rounds := roundsEnded(t, a, v.ID)
		require.Len(t, rounds, 2)
		for _, e := range rounds {
			assert.Equal(t, map[string]string{"alpha": "crashed", "beta": "crashed"}, e.Turns, "round %d", e.Round)
		}
		assert.Empty(t, rounds[0].Removed, "one crash removes no one")
		assert.ElementsMatch(t, []string{"alpha", "beta"}, rounds[1].Removed, "two crashes in a row remove an agent")
	})

	t.Run("(e) the step limit, and max_tokens", func(t *testing.T) {
		env, a := serving(t, limited(1, 30*time.Second), quick)
		reading(env)
		env.Vendor().Script(func(r *harness.VendorRequest) harness.VendorReply {
			if r.Agent == "alpha" {
				return harness.ToolUse("logs__search", map[string]any{"q": "error"})
			}
			cut := harness.Text("The logs show that deploy 42")
			cut.Stop = "max_tokens"
			return cut
		})

		v := investigate(t, a, "limits")

		notQuiet(t, &v)
		requireRounds(t, &v, 1)
		assert.Empty(t, v.Rounds[0].Evidence.TurnEnds)
		rounds := roundsEnded(t, a, v.ID)
		require.Len(t, rounds, 1)
		assert.Equal(t, map[string]string{"alpha": "finished", "beta": "finished"}, rounds[0].Turns)
		asked := map[string]int{}
		for _, r := range env.Vendor().Requests("a") {
			asked[r.Agent]++
		}
		assert.Equal(t, map[string]int{"alpha": 50, "beta": 1}, asked,
			"alpha's turn calls tools until the step limit of 50 model calls; beta's ends at its answer cut at max_tokens")
	})
}

// Scenario 7: a turn whose runtime ignores its deadline, its standard input
// and SIGTERM, and sleeps for two minutes (R5), is killed at the round's
// deadline: its process lives the round's time limit, and no longer, and the
// round, which no turn ended, is not quiet.
func TestATurnThatNeverEndsIsKilledAtItsDeadline(t *testing.T) {
	const limit = 3 * time.Second
	d := quick
	d.Binary = stubborn(t)
	_, a := serving(t, limited(1, limit), d)

	id := a.Admin().OpenItem(t, "stubborn", incident, openWait)
	pids := waitForTurns(t, a, 2)
	for _, pid := range pids {
		require.False(t, gone(pid), "turn process %d runs", pid)
	}
	v := a.Admin().WaitItem(t, id, stopped, runWait)

	for _, pid := range pids {
		assert.True(t, gone(pid), "turn process %d is gone", pid)
	}
	turns := lives(t, a)
	require.Len(t, turns, 2)
	for turn, l := range turns {
		require.False(t, l.started.IsZero(), turn)
		require.False(t, l.exited.IsZero(), "turn %s exited", turn)
		assert.Equal(t, "interrupted", l.class, turn)
		// A turn starts a moment after its round opens, and its deadline is
		// the round's time limit after that opening.
		life := l.exited.Sub(l.started)
		assert.Greater(t, life, limit-time.Second, "turn %s runs until the round's deadline", turn)
		assert.LessOrEqual(t, life, limit+500*time.Millisecond, "turn %s is killed at the deadline, not later", turn)
	}
	notQuiet(t, &v)
	requireRounds(t, &v, 1)
	assert.Equal(t, "deadline", v.Rounds[0].Ended)
	assert.Empty(t, v.Rounds[0].Evidence.TurnEnds)
}

// Scenario 12: once the day's spend reaches the daily cap, the running run
// stops cut at its round's end, and an item opened then is not staffed.
func TestTheDailyCapStopsRunsAndStaffsNoNewItem(t *testing.T) {
	const dailyCap = 1000
	d := quick
	d.DailyCap = dailyCap
	env, a := serving(t, team(0), d)
	// An answer of 1,000 input tokens at $1 a million and 1,000 output tokens
	// at $5 a million costs $0.006: 2,000 tokens at the reference's $3 a
	// million, twice the cap.
	env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply {
		return harness.EndTurn().WithUsage(harness.Usage(1000, 1000))
	})

	running := investigate(t, a, "running")
	later := a.Admin().OpenItem(t, "after-the-cap", incident, openWait)
	code, v := a.Admin().Item(t, later)

	assert.Equal(t, "cut", running.Stop)
	requireRounds(t, &running, 1)
	rounds := roundsEnded(t, a, running.ID)
	require.Len(t, rounds, 1)
	assert.GreaterOrEqual(t, rounds[0].DaySpent, int64(dailyCap), "the round's end read the day's spend past the cap")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "awaiting_close", v.State)
	assert.Equal(t, "not_staffed", v.Stop, "an item opened once the day's spend reached the cap is not staffed")
	assert.Empty(t, v.Rounds)
}

// withModel returns a change of the configuration that has alpha use model in
// place of from.
func withModel(t *testing.T, from, model string) func(dir string) {
	return func(dir string) {
		path := filepath.Join(dir, "agents", "alpha.yaml")
		text, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(text), `model: "`+from+`"`)
		require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(text), `"`+from+`"`, `"`+model+`"`, 1)), 0o600))
	}
}

// byCommit matches a log line of a commit.
func byCommit(commit string) func(map[string]any) bool {
	return func(l map[string]any) bool { return l["commit"] == commit }
}

// Scenario 15: a commit that fails Decision 1's checks, an agent's model with
// no price card in the deployment, is refused, and the item running under the
// commit it locked goes on to its stop. A restart starts, with no revision
// current: an opening answers 503 until a good commit is made and refreshed,
// and then 201.
func TestABadConfigurationCommitIsRefusedAndRunningItemsGoOn(t *testing.T) {
	const priced, unpriced = "claude-sonnet-5-5", "claude-unpriced-1"
	d := quick
	d.Timing.RegistryRefresh = time.Second
	env, repo := configured(t, []harness.ServerSpec{logs}, team(0))
	deployment := env.Deployment(t, "a", d)
	a := env.Serve(t, "a", deployment)
	a.Log().WaitFor(t, "lease taken", nil, leadWait)
	a.Log().WaitFor(t, "revision current", nil, leadWait)
	// The turns' answers come after the refusal, which the next refresh
	// logs.
	env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply { return harness.EndTurn().After(5 * time.Second) })
	id := a.Admin().OpenItem(t, "running", incident, openWait)
	waitForTurns(t, a, 2)

	bad := repo.Commit(t, withModel(t, priced, unpriced))
	refused := a.Log().WaitFor(t, "revision refused", byCommit(bad), leadWait)
	code, during := a.Admin().Item(t, id)
	v := a.Admin().WaitItem(t, id, stopped, runWait)

	assert.Contains(t, refused["reason"], "agent alpha's model "+unpriced+" has no price card in the deployment")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "investigating", during.State, "the item runs when the commit is refused")
	assert.Equal(t, "quiet", v.Stop, "the running item reaches its stop")

	a.Signal(t, syscall.SIGTERM)
	require.Equal(t, 0, a.Wait(t, time.Minute), "serve drains and exits")
	b := env.Serve(t, "a", deployment)
	b.Admin().WaitStatus(t, func(s *harness.InstanceStatus) bool { return s.Leading }, leadWait)
	b.Log().WaitFor(t, "revision refused", byCommit(bad), leadWait)
	code, opened := b.Admin().Open(t, "after-the-restart", incident)
	assert.Equal(t, http.StatusServiceUnavailable, code, "no revision is current, so no item opens")
	assert.Equal(t, "no configuration is current yet; try again", opened.Error)

	good := repo.Commit(t, withModel(t, unpriced, priced))
	b.Log().WaitFor(t, "revision current", byCommit(good), leadWait)
	b.Admin().OpenItem(t, "after-the-restart", incident, openWait)

	for _, line := range b.Log().Lines() {
		if line["msg"] == "revision current" {
			assert.Equal(t, good, line["commit"], "no revision was current after the restart until the good commit")
		}
	}
}

// failing scripts the logs server to answer every read with reply.
//
//nolint:gocritic // hugeParam: a reply is a value scripts build and return.
func failing(env *harness.Env, reply harness.ToolReply) {
	for _, tool := range []string{"search", "deploys"} {
		env.Server("logs").Script(tool, func(*harness.ToolCall) harness.ToolReply { return reply })
	}
}

// failingAfterFirst scripts the logs server to answer each tool's first read
// as reading does, and every later one with reply.
//
//nolint:gocritic // hugeParam: a reply is a value scripts build and return.
func failingAfterFirst(env *harness.Env, reply harness.ToolReply) {
	for tool, output := range map[string]string{"search": searchOutput, "deploys": deploysOutput} {
		n := 0
		env.Server("logs").Script(tool, func(*harness.ToolCall) harness.ToolReply {
			n++
			if n == 1 {
				return harness.Result(output)
			}
			return reply
		})
	}
}

// lastFailed reports whether the last message of a request holds a tool's
// result that is an error.
func lastFailed(r *harness.VendorRequest) bool {
	if len(r.Messages) < 2 {
		return false
	}
	return slices.ContainsFunc(r.Messages[len(r.Messages)-1].Blocks, func(b harness.VendorBlock) bool { return b.IsError })
}

// releases is a scripted server with a write tool, whose calls the MCP
// gateway records as proposals for a person.
var releases = harness.ServerSpec{Name: "releases", Tools: []harness.ToolSpec{{
	Name: "rollback", Description: "Roll a service back to its last good release.",
	InputSchema: `{"type":"object","properties":{"service":{"type":"string"}}}`,
	Write:       &harness.WriteSpec{Scope: map[string]string{"service": "service"}, Summary: "Roll the service back"},
}}}

// Scenario 17: a tool call that failed on the server's, the platform's or the
// tool's side keeps its round from being quiet, unless the agent's later call
// of the same tool succeeded; one that failed on the agent's side does not.
func TestFailedToolCallsKeepRoundsFromBeingQuiet(t *testing.T) {
	t.Run("(a) every read answers HTTP 500", func(t *testing.T) {
		env, a := serving(t, limited(1, 30*time.Second), quick)
		failing(env, harness.HTTPFailure(http.StatusInternalServerError))
		env.Vendor().Script(readOnce)

		v := investigate(t, a, "server-failing")

		notQuiet(t, &v)
		requireRounds(t, &v, 1)
		assert.Positive(t, v.Rounds[0].Evidence.FailedToolCalls)
		assert.ElementsMatch(t, []string{"alpha", "beta"}, v.Rounds[0].Evidence.TurnEnds, "the agents ended their turns")
	})

	t.Run("(b) a read whose output is over the cap", func(t *testing.T) {
		env, a := serving(t, team(0), quick)
		// About 300 KiB, over the gateway's cap on an output, 256 KiB.
		env.Server("logs").Script("search", func(*harness.ToolCall) harness.ToolReply {
			return harness.Result(strings.Repeat("checkout answers 500. ", 300<<10/22))
		})
		env.Vendor().Script(func(r *harness.VendorRequest) harness.VendorReply {
			if r.Agent == "alpha" && lastText(r) == "" {
				return harness.ToolUse("logs__search", map[string]any{"q": "error"})
			}
			return harness.EndTurn()
		})

		v := investigate(t, a, "too-broad")

		assert.Equal(t, "quiet", v.Stop, "a read the agent must narrow leaves its round quiet")
		requireRounds(t, &v, 1)
		assert.True(t, v.Rounds[0].Quiet)
		assert.Zero(t, v.Rounds[0].Evidence.FailedToolCalls)
		searches := callsNamed(t, a, runOf(t, env, v.ID), "logs/search")
		require.Len(t, searches, 1)
		assert.Equal(t, "agent", searches[0].Failure, "the read failed on the agent's side")
		assert.Contains(t, searches[0].Reason, "narrow the query")
	})

	t.Run("(c) a wrap-up round whose reads fail", func(t *testing.T) {
		env, a := serving(t, team(1), quick)
		failingAfterFirst(env, harness.HTTPFailure(http.StatusInternalServerError))
		env.Vendor().Script(func(r *harness.VendorRequest) harness.VendorReply {
			if slices.Contains(r.Tools, "post_hypothesis") {
				return investigating(r)
			}
			return readOnce(r)
		})

		v := investigate(t, a, "wrap-up-failing")

		notQuiet(t, &v)
		assert.NotEqual(t, "converged", v.Stop, "no wrap-up round concludes")
		var wrapUps int
		for _, r := range v.Rounds {
			if !r.WrapUp {
				continue
			}
			wrapUps++
			assert.Positive(t, r.Evidence.FailedToolCalls, "wrap-up round %d", r.Round)
			assert.ElementsMatch(t, []string{"alpha", "beta"}, r.Evidence.TurnEnds, "wrap-up round %d", r.Round)
		}
		assert.Positive(t, wrapUps, "the converged hypothesis gets its wrap-up rounds")
	})

	t.Run("(d) every read answers isError", func(t *testing.T) {
		env, a := serving(t, limited(1, 30*time.Second), quick)
		failing(env, harness.ErrorResult("the logs backend is down"))
		env.Vendor().Script(readOnce)

		v := investigate(t, a, "backend-down")

		notQuiet(t, &v)
		requireRounds(t, &v, 1)
		assert.Positive(t, v.Rounds[0].Evidence.FailedToolCalls)
		assert.ElementsMatch(t, []string{"alpha", "beta"}, v.Rounds[0].Evidence.TurnEnds, "the agents ended their turns")
	})

	t.Run("(e) a read isError, then the same read with fixed arguments", func(t *testing.T) {
		env, a := serving(t, team(0), quick)
		env.Server("logs").Script("search", func(c *harness.ToolCall) harness.ToolReply {
			var args struct {
				Q string `json:"q"`
			}
			_ = json.Unmarshal(c.Arguments, &args)
			if args.Q == "" {
				return harness.ErrorResult("q is required: name what to search the logs for")
			}
			return harness.Result(searchOutput)
		})
		env.Vendor().Script(func(r *harness.VendorRequest) harness.VendorReply {
			switch {
			case r.Agent != "alpha":
				return harness.EndTurn()
			case lastText(r) == "":
				return harness.ToolUse("logs__search", map[string]any{})
			case lastFailed(r):
				return harness.ToolUse("logs__search", map[string]any{"q": "error"})
			}
			return harness.EndTurn()
		})

		v := investigate(t, a, "fixed-arguments")

		assert.Equal(t, "quiet", v.Stop, "the read that succeeded recovers the one that failed")
		requireRounds(t, &v, 1)
		assert.True(t, v.Rounds[0].Quiet)
		assert.Zero(t, v.Rounds[0].Evidence.FailedToolCalls)
		searches := callsNamed(t, a, runOf(t, env, v.ID), "logs/search")
		require.Len(t, searches, 2)
		assert.Equal(t, "tool", searches[0].Failure, "the first read failed on the tool's side")
		assert.Equal(t, "ok", searches[1].Status)
	})

	t.Run("(f) a write whose server is down", func(t *testing.T) {
		env, _ := configured(t, []harness.ServerSpec{logs, releases}, harness.ConfigSpec{
			// Only alpha lists releases' tools, so that its listing alone
			// looks at the server, before it goes down.
			Agents: []harness.AgentSpec{
				{Name: "alpha", Servers: []string{"logs", "releases"}},
				{Name: "beta", Servers: []string{"logs"}},
			},
			WorkType: harness.WorkTypeSpec{Rounds: 1},
		})
		d := quick
		// A call looks at its server again whenever an earlier look is a
		// millisecond old: the write looks at a server that went down since
		// alpha's tools were listed.
		d.Timing.MCP.VerifyEvery = time.Millisecond
		a := leading(t, env, d)
		server := env.Server("releases")
		env.Vendor().Script(func(r *harness.VendorRequest) harness.VendorReply {
			if r.Agent != "alpha" || lastText(r) != "" {
				return harness.EndTurn()
			}
			server.Down()
			return harness.ToolUse("releases__rollback", map[string]any{"service": "checkout", "_hypothesis": "the-bad-release"})
		})

		v := investigate(t, a, "write-down")

		notQuiet(t, &v)
		requireRounds(t, &v, 1)
		assert.Equal(t, 1, v.Rounds[0].Evidence.FailedToolCalls, "the write")
		assert.ElementsMatch(t, []string{"alpha", "beta"}, v.Rounds[0].Evidence.TurnEnds, "the agents ended their turns")
		writes := callsNamed(t, a, runOf(t, env, v.ID), "releases/rollback")
		require.Len(t, writes, 1)
		assert.Equal(t, "alpha", writes[0].Agent)
		assert.Equal(t, "read_error", writes[0].Status)
		assert.Equal(t, "server", writes[0].Failure, "the write failed on the server's side")
	})
}

// Scenario 18: the MCP server is down when serve starts. The first listing
// leaves its tools out and records a failed call of the round, whose round
// is not quiet.
func TestAServerDownAtStartLeavesItsRoundNotQuiet(t *testing.T) {
	env, _ := configured(t, []harness.ServerSpec{logs}, limited(1, 30*time.Second))
	env.Server("logs").Down()
	a := leading(t, env, quick)
	env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply { return harness.EndTurn() })

	v := investigate(t, a, "server-down")

	notQuiet(t, &v)
	requireRounds(t, &v, 1)
	assert.Positive(t, v.Rounds[0].Evidence.FailedToolCalls)
	listings := callsNamed(t, a, runOf(t, env, v.ID), "logs/tools/list")
	require.NotEmpty(t, listings, "a listing that could not reach the server is a failed call")
	for _, c := range listings {
		assert.Equal(t, "read_error", c.Status)
		assert.Equal(t, "server", c.Failure)
		assert.Equal(t, 1, c.Round)
	}
	requests := env.Vendor().Requests("a")
	require.NotEmpty(t, requests)
	for _, r := range requests {
		assert.NotContains(t, r.Tools, "logs__search", "the server's tools are missing from the listing")
		assert.NotContains(t, r.Tools, "logs__deploys", "the server's tools are missing from the listing")
	}
}
