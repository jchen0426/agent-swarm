//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// The scenarios of Plan 7b-1 of an item's lifecycle (Decisions 4, 6, 7 and
// 8): items past the limit wait in the queue and start in their order, and a
// promotion starts one past the limit by one, and no further; a close stops
// an item's run from outside it, a reopen returns the item to awaiting_close,
// and the item closes by itself once its delay has passed. They read what a
// person reads: the admin API's answers and views (C4), the records and the
// boards (C5), and what the vendor and the MCP server received. No request
// after an act's answer is judged by what the vendor and the MCP server
// received, never by serve's log: under Docker the runner logs turn started
// after its check for a stop, outside its lock, so a launch that passed that
// check just before the act may log just after the answer, though the act
// killed it. The helpers the scenarios share are in helpers_test.go.

// autoClose is the delay after which an item of these scenarios closes by
// itself once it awaits its close: its work item type's autoClose.after.
const autoClose = 2 * time.Second

// quickRetry is quick's deployment, with the coordinator's retry wait cut to
// half a second. An auto-close timer that fires before its deadline on
// serve's clock, as one does when a sample of the database's clock corrects
// that clock back while the timer waits, arms itself again a retry wait
// later (R41), and so does one whose close the records refuse as not yet
// due: at serve's default, 5 s, such an item would close by itself seconds
// past its delay, which is 2 s here.
var quickRetry = func() harness.DeploymentSpec {
	d := quick
	d.Timing.Coordinator.Retry = 500 * time.Millisecond
	return d
}()

// waiting asserts that an item waits in the queue, at a position, from 1.
func waiting(t *testing.T, a *harness.Instance, id string, position int) {
	t.Helper()
	code, v := a.Admin().Item(t, id)
	require.Equal(t, http.StatusOK, code, v.Error)
	assert.Equal(t, "queued", v.State, "item %s waits", id)
	if assert.NotNil(t, v.Queue, "item %s has a place in the queue", id) {
		assert.Equal(t, position, v.Queue.Position, "item %s's place in the queue", id)
	}
}

// investigatingNow says that an item investigates.
func investigatingNow(v *harness.ItemView) bool { return v.State == "investigating" }

// counted is an act's consequences' count of the items investigating and its
// limit, -1 for either the answer left out.
func counted(c *harness.Consequences) (investigating, limit int) {
	investigating, limit = -1, -1
	if c.Investigating != nil {
		investigating = *c.Investigating
	}
	if c.Limit != nil {
		limit = *c.Limit
	}
	return investigating, limit
}

// Scenario 19: past the limit of items investigating at once, here one, an
// item waits in the queue, in the order the items opened, and starts once a
// slot frees, in that order. A promotion starts a waiting item at once, past
// the limit by one, and no item past it by two.
func TestItemsPastTheLimitWaitAndStartInOrder(t *testing.T) {
	d := quick
	d.Lifecycle = harness.Lifecycle{Investigating: 1}
	env, a := serving(t, team(0), d)
	// The first item's turns end at once, each by its agent's first request,
	// and every later request is held until the test ends, so that every
	// item that starts after the first investigates until then. The first
	// item's answers wait until the test has seen the second and third items
	// wait, so that the first cannot stop before they open.
	release := make(chan struct{})
	ctx, answered := t.Context(), map[string]bool{}
	env.Vendor().Script(func(r *harness.VendorRequest) harness.VendorReply {
		if answered[r.Agent] {
			return harness.Hang()
		}
		answered[r.Agent] = true
		select {
		case <-release:
		case <-ctx.Done():
		}
		return harness.EndTurn()
	})

	first := a.Admin().OpenItem(t, "first", incident, openWait)
	calling(t, env, "a", 2)
	second := a.Admin().OpenItem(t, "second", incident, openWait)
	third := a.Admin().OpenItem(t, "third", incident, openWait)
	waiting(t, a, second, 1)
	waiting(t, a, third, 2)
	close(release)

	v := a.Admin().WaitItem(t, first, stopped, runWait)
	assert.Equal(t, "quiet", v.Stop, "the first item's run stops quiet")
	a.Admin().WaitItem(t, second, investigatingNow, runWait)
	waiting(t, a, third, 1)

	code, promoted := a.Admin().Promote(t, third, "promote-third")
	require.Equal(t, http.StatusOK, code, promoted.Error)
	assert.True(t, promoted.Changed)
	assert.Equal(t, []string{third}, promoted.Consequences.Starts, "the promotion starts the third item at once")
	investigating, limit := counted(&promoted.Consequences)
	assert.Equal(t, 2, investigating, "two items investigate, past the limit by one")
	assert.Equal(t, 1, limit)
	if assert.NotNil(t, promoted.Item, "the answer shows the item once the promotion is done") {
		assert.Equal(t, "investigating", promoted.Item.State)
	}
	code, status := a.Admin().Status(t)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, harness.QueueStatus{Limit: 1, Investigating: 2}, status.Queue, "the status gives two items investigating")

	fourth := a.Admin().OpenItem(t, "fourth", incident, openWait)
	waiting(t, a, fourth, 1)
	code, refused := a.Admin().Promote(t, fourth, "promote-fourth")
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "limit_reached", refused.Code, "a promotion past the limit by two is refused")
	waiting(t, a, fourth, 1)
	code, status = a.Admin().Status(t)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, harness.QueueStatus{Limit: 1, Investigating: 2, Queued: 1}, status.Queue, "the refused promotion changed nothing")
}

// readThenWait is a model whose agents each read the logs once, with a tool
// of their own, and then wait on an answer the vendor holds until the test
// ends: when the test acts, each turn's call has ended, the session it went
// over is pooled, and no call is in flight to send anything after the act.
func readThenWait(r *harness.VendorRequest) harness.VendorReply {
	if lastText(r) == "" {
		return harness.ToolUse(reads[r.Agent], readsWith[r.Agent])
	}
	return harness.Hang()
}

// heldAfterARead waits until the vendor holds n requests of an instance's
// that follow a turn's read (see readThenWait): n turns have each read once,
// and wait on the vendor.
func heldAfterARead(t *testing.T, env *harness.Env, instance string, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		requests, held := env.Vendor().Requests(instance), 0
		for i := range requests {
			if lastText(&requests[i]) != "" {
				held++
			}
		}
		return held >= n
	}, runWait, 50*time.Millisecond, "%d turns of %s's read once, then wait on the vendor", n, instance)
}

// pooledSessions returns the sessions an instance's calls of a server's tools
// went over, by the tool: each the connection of the run, the agent and the
// server that made the call, which the MCP gateway pools.
func pooledSessions(t *testing.T, server *harness.MCPServer, instance string) map[string]string {
	t.Helper()
	out := map[string]string{}
	requests := server.Requests(instance)
	for i := range requests {
		if r := &requests[i]; r.RPC == "tools/call" {
			require.NotEmpty(t, r.Session, "a call of %s goes over a session", r.Tool)
			out[r.Tool] = r.Session
		}
	}
	return out
}

// deletes returns, by the session each closes, when an instance's DELETEs
// reached a server, in order.
func deletes(server *harness.MCPServer, instance string) map[string][]time.Time {
	out := map[string][]time.Time{}
	requests := server.Requests(instance)
	for i := range requests {
		if r := &requests[i]; r.Method == http.MethodDelete {
			out[r.Session] = append(out[r.Session], r.At)
		}
	}
	return out
}

// posted returns what a run's board holds, as inspect prints it (C5): the
// reasons of its stop entries and the codes of its notices, each in order.
func posted(t *testing.T, a *harness.Instance, run string) (stops, notices []string) {
	t.Helper()
	for _, event := range a.Inspect(t, "board", run) {
		raw, err := json.Marshal(event["body"])
		require.NoError(t, err)
		var body struct {
			Stop *struct {
				Reason string `json:"reason"`
			} `json:"stop"`
			Notice *struct {
				Code string `json:"code"`
			} `json:"notice"`
		}
		require.NoError(t, json.Unmarshal(raw, &body))
		switch {
		case body.Stop != nil:
			stops = append(stops, body.Stop.Reason)
		case body.Notice != nil:
			notices = append(notices, body.Notice.Code)
		}
	}
	return stops, notices
}

// recordedAt returns when an item's last record of a kind was recorded, by
// the database's clock, as inspect prints it (C5).
func recordedAt(t *testing.T, a *harness.Instance, item, kind string) time.Time {
	t.Helper()
	var at time.Time
	for _, line := range a.Inspect(t, "item", item) {
		if line["kind"] == kind {
			parsed, err := time.Parse(time.RFC3339Nano, fmt.Sprint(line["at"]))
			require.NoError(t, err, "a record's time: %v", line)
			at = parsed
		}
	}
	require.False(t, at.IsZero(), "item %s has a record of kind %s", item, kind)
	return at
}

// Scenario 20: a close stops an item's run from outside it, at once: the run
// stops closed, its turns end, the MCP gateway closes each session the run
// pooled at the MCP server, and neither the vendor nor the MCP server hears
// of the run again; its board's stop entry says closed. The close repeated
// with its key answers as the close did. A reopen returns the item to
// awaiting_close, and the item closes by itself its delay after the reopen.
func TestAClosedItemStopsItsRunAndReopensAndClosesByItself(t *testing.T) {
	cfg := team(0)
	cfg.WorkType.AutoClose = autoClose
	env, a := serving(t, cfg, quickRetry)
	reading(env)
	env.Vendor().Script(readThenWait)
	id := a.Admin().OpenItem(t, "closed", incident, openWait)
	heldAfterARead(t, env, "a", 2)
	waitForTurns(t, a, 2)
	run := runOf(t, env, id)
	server := env.Server("logs")
	pooled := pooledSessions(t, server, "a")
	require.Len(t, pooled, 2, "a call of each agent's tool, alpha's search and beta's deploys")
	require.NotEqual(t, pooled["search"], pooled["deploys"], "a pooled session for each agent")

	sent := time.Now()
	code, closed := a.Admin().Close(t, id, "close")
	answered := time.Now()

	require.Equal(t, http.StatusOK, code, closed.Error)
	assert.LessOrEqual(t, answered.Sub(sent), time.Second, "the close stops the run within a second")
	assert.True(t, closed.Changed)
	assert.Equal(t, []harness.StoppedRun{{Item: id, Run: run, Round: 1, Reason: "closed"}}, closed.Consequences.Stops)
	assert.Equal(t, []string{id}, closed.Consequences.Closes)
	require.NotNil(t, closed.Item, "the answer shows the item once the close is done")
	assert.Equal(t, "closed", closed.Item.State)
	assert.Equal(t, "closed", closed.Item.Stop, "the run stopped closed")
	allGone(t, a.TurnsStarted(), "every turn the run started is gone")
	assert.True(t, within(10*time.Second, func() bool {
		closes := deletes(server, "a")
		return len(closes[pooled["search"]]) > 0 && len(closes[pooled["deploys"]]) > 0
	}), "the MCP gateway closes each session the run pooled: %v", deletes(server, "a"))

	code, again := a.Admin().Close(t, id, "close")
	assert.Equal(t, http.StatusOK, code, again.Error)
	assert.True(t, again.Changed, "the close repeated with its key answers changed, as the close did (R14)")
	assert.Equal(t, closed.Item, again.Item, "and the item as the close left it")
	investigating, limit := counted(&closed.Consequences)
	againInvestigating, againLimit := counted(&again.Consequences)
	assert.Equal(t, []int{investigating, limit}, []int{againInvestigating, againLimit}, "and the items investigating and the limit")

	code, reopened := a.Admin().Reopen(t, id, "reopen")
	require.Equal(t, http.StatusOK, code, reopened.Error)
	assert.True(t, reopened.Changed)
	require.NotNil(t, reopened.Item, "the answer shows the item once the reopen is done")
	assert.Equal(t, "awaiting_close", reopened.Item.State, "the reopen returns the item to awaiting_close")
	assert.Equal(t, "closed", reopened.Item.Stop, "its run's latest stop")
	reopenedAt := recordedAt(t, a, id, "item_reopened")
	assert.WithinDuration(t, reopenedAt.Add(autoClose), reopened.Item.ClosesAt, 0, "its auto-close counts from the reopen")
	v := a.Admin().WaitItem(t, id, func(v *harness.ItemView) bool { return v.State == "closed" }, autoClose+10*time.Second)
	require.NotNil(t, v.Closed)
	assert.True(t, v.Closed.Auto, "the item closed by itself")
	assert.Empty(t, v.Closed.By, "by no one")
	assert.False(t, v.Closed.At.Before(reopened.Item.ClosesAt), "not before its deadline, %v; closed at %v", reopened.Item.ClosesAt, v.Closed.At)
	assert.LessOrEqual(t, v.Closed.At.Sub(reopenedAt), autoClose+time.Second, "about %v after the reopen", autoClose)

	// What the run's board holds, and what the vendor and the MCP server
	// received after the close's answer, read once the rest of the scenario
	// has given anything late time to come.
	stops, notices := posted(t, a, run)
	assert.Equal(t, []string{"closed"}, stops, "the board's stop entry says closed")
	assert.NotContains(t, notices, "turn_unstarted", "no turn_unstarted notice is posted")
	var late []string
	requests := env.Vendor().Requests("a")
	for i := range requests {
		if r := &requests[i]; r.At.After(answered) {
			late = append(late, fmt.Sprintf("the vendor: %s %s from %s at %s", r.Method, r.Path, r.Agent, r.At.Format(time.RFC3339Nano)))
		}
	}
	calls := server.Requests("a")
	for i := range calls {
		r := &calls[i]
		pooledClose := r.Method == http.MethodDelete && (r.Session == pooled["search"] || r.Session == pooled["deploys"])
		if r.At.After(answered) && !pooledClose {
			late = append(late, fmt.Sprintf("the MCP server: %s %s %s at %s", r.Method, r.Path, r.RPC, r.At.Format(time.RFC3339Nano)))
		}
	}
	assert.Empty(t, late, "nothing but the pooled sessions' DELETEs after the close's answer, at %s", answered.Format(time.RFC3339Nano))
	closes := deletes(server, "a")
	for agent, tool := range map[string]string{"alpha": "search", "beta": "deploys"} {
		if assert.Len(t, closes[pooled[tool]], 1, "one DELETE for %s's pooled session", agent) {
			assert.False(t, closes[pooled[tool]][0].Before(sent), "the close, sent at %s, closed %s's pooled session",
				sent.Format(time.RFC3339Nano), agent)
		}
	}
}
