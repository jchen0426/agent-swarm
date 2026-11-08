//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"
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
	assert.False(t, recordedAt(t, a, second, "item_started").Before(recordedAt(t, a, first, "item_awaiting_close")),
		"the second item starts once the first awaits its close, not before")
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

// callSessions returns, sorted, the sessions over which an instance's calls of
// a server's tools went: each the connection of a run, an agent and the
// server, which the MCP gateway pools, so one for each run and agent that
// called the server.
func callSessions(t *testing.T, server *harness.MCPServer, instance string) []string {
	t.Helper()
	var out []string
	requests := server.Requests(instance)
	for i := range requests {
		if r := &requests[i]; r.RPC == "tools/call" {
			require.NotEmpty(t, r.Session, "a call of %s goes over a session", r.Tool)
			if !slices.Contains(out, r.Session) {
				out = append(out, r.Session)
			}
		}
	}
	slices.Sort(out)
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

// vendorAfter lists the requests the vendor received after at, whichever
// instance sent them, each as a failure shows it.
func vendorAfter(env *harness.Env, at time.Time) []string {
	var out []string
	requests := env.Vendor().All()
	for i := range requests {
		if r := &requests[i]; r.At.After(at) {
			out = append(out, fmt.Sprintf("the vendor: %s %s from %q, agent %q, at %s",
				r.Method, r.Path, r.Instance, r.Agent, r.At.UTC().Format(time.RFC3339Nano)))
		}
	}
	return out
}

// quietAfter asserts that an act which stopped runs, sent at sent and
// answered at answered, left the vendor and a server quiet: after the answer,
// neither received any request, whichever instance sent it, but for the
// DELETEs by which instance closed sessions, the sessions the stopped runs
// pooled at the server; and each of those sessions was closed by exactly one
// DELETE, sent no sooner than the act was, so that the act closed it. The
// server's requests are read whole but for the harness's own listing of its
// tools (harness.PinInstance), as scenario 9 reads them. what names the act
// as a failure says it. It reads what has come so far, so its caller calls it
// once anything late has had time to come. The stopped runs must have pooled
// sessions, so that it never passes on none.
func quietAfter(t *testing.T, env *harness.Env, server *harness.MCPServer, instance string, sent, answered time.Time, sessions []string, what string) {
	t.Helper()
	require.NotEmpty(t, sessions, "the runs %s stopped pooled sessions at the server, for the act to close", what)
	late := vendorAfter(env, answered)
	requests := server.All()
	for i := range requests {
		r := &requests[i]
		closing := r.Instance == instance && r.Method == http.MethodDelete && slices.Contains(sessions, r.Session)
		if r.Instance != harness.PinInstance && r.At.After(answered) && !closing {
			late = append(late, fmt.Sprintf("the MCP server: %s %s %s from %q at %s",
				r.Method, r.Path, r.RPC, r.Instance, r.At.UTC().Format(time.RFC3339Nano)))
		}
	}
	assert.Empty(t, late, "nothing but the pooled sessions' DELETEs after %s's answer, at %s", what, answered.UTC().Format(time.RFC3339Nano))
	closes := deletes(server, instance)
	for _, session := range sessions {
		if assert.Len(t, closes[session], 1, "one DELETE for pooled session %s", session) {
			assert.False(t, closes[session][0].Before(sent), "%s, sent at %s, closed pooled session %s",
				what, sent.UTC().Format(time.RFC3339Nano), session)
		}
	}
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
	pooled := callSessions(t, server, "a")
	require.Len(t, pooled, 2, "a pooled session for each agent, over which alpha calls search and beta deploys")

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
		return !slices.ContainsFunc(pooled, func(session string) bool { return len(closes[session]) == 0 })
	}), "the MCP gateway closes each session the run pooled: %v", deletes(server, "a"))

	code, again := a.Admin().Close(t, id, "close")
	assert.Equal(t, http.StatusOK, code, again.Error)
	assert.True(t, again.Changed, "the close repeated with its key answers changed, as the close did (R14)")
	assert.Equal(t, closed.Item, again.Item, "and the item as the close left it")
	investigating, limit := counted(&closed.Consequences)
	assert.Equal(t, []int{0, 3}, []int{investigating, limit}, "the close's answer gives the items investigating, none once it is done, and serve's limit")
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
	quietAfter(t, env, server, "a", sent, answered, pooled, "the close")
}

// kinds returns the kinds of an item's records, in order, as inspect prints
// them (C5).
func kinds(t *testing.T, a *harness.Instance, item string) []string {
	t.Helper()
	var out []string
	for _, line := range a.Inspect(t, "item", item) {
		out = append(out, fmt.Sprint(line["kind"]))
	}
	return out
}

// stopsAndResumes returns a run's board's stop and resume entries, in order,
// as inspect prints them (C5): "stop <reason>" for a stop entry, "resume" for
// a resume entry.
func stopsAndResumes(t *testing.T, a *harness.Instance, run string) []string {
	t.Helper()
	var out []string
	for _, event := range a.Inspect(t, "board", run) {
		raw, err := json.Marshal(event["body"])
		require.NoError(t, err)
		var body struct {
			Stop *struct {
				Reason string `json:"reason"`
			} `json:"stop"`
			Resume json.RawMessage `json:"resume"`
		}
		require.NoError(t, json.Unmarshal(raw, &body))
		switch {
		case body.Stop != nil:
			out = append(out, "stop "+body.Stop.Reason)
		case body.Resume != nil:
			out = append(out, "resume")
		}
	}
	return out
}

// Scenario 24: an extension of a stopped run resumes it on its board. A run
// of one round stops budget_exhausted; its view gives its budget and the
// extension the deployment offers. An extension of one round resumes it: the
// board takes a resume entry, round 2 runs, and the run stops again, its board
// with a second stop entry. The extension repeated with its key answers as it
// did and extends once. An extension of wall clock alone, the run's rounds
// spent, is refused would_stop_at_once; and with two items investigating, the
// limit and one, the run's resume is refused limit_reached.
func TestAnExtensionResumesAStoppedRunOnItsBoard(t *testing.T) {
	d := quick
	d.Lifecycle = harness.Lifecycle{Investigating: 1, Extension: &harness.Amount{Rounds: 1, WallClock: 2 * time.Minute}}
	env, a := serving(t, team(1), d)
	// Every turn ends by its agent's first request, so that each round ends
	// at once, until the test holds the vendor's answers, so that the items it
	// opens then investigate until the test ends.
	var hold atomic.Bool
	env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply {
		if hold.Load() {
			return harness.Hang()
		}
		return harness.EndTurn()
	})

	v := investigate(t, a, "extended")
	id := v.ID
	assert.Equal(t, "budget_exhausted", v.Stop, "the run of one round uses its budget")
	requireRounds(t, &v, 1)
	if assert.NotNil(t, v.Budget, "the view gives the run's budget") {
		assert.Equal(t, harness.AmountView{Rounds: 1, WallClock: "5m0s", Tokens: 2_000_000}, v.Budget.AmountView, "the type's budget")
		assert.Equal(t, 1, v.Budget.Used.Rounds, "the run used its round")
	}
	assert.Equal(t, &harness.AmountView{Rounds: 1, WallClock: "2m0s"}, v.ExtensionOffered, "the view offers the deployment's lifecycle.extension")
	run := runOf(t, env, id)

	one := 1
	code, preview := a.Admin().Extend(t, id, "", harness.ExtendBody{Rounds: &one, Preview: true})
	require.Equal(t, http.StatusOK, code, preview.Error)
	assert.True(t, preview.Preview)
	assert.True(t, preview.Consequences.Resumes, "the preview says the extension resumes the run")
	investigating, limit := counted(&preview.Consequences)
	assert.Equal(t, []int{1, 1}, []int{investigating, limit}, "the preview counts the run's item among the items investigating")
	assert.Equal(t, "awaiting_close", a.Admin().WaitItem(t, id, stopped, runWait).State, "the preview changed nothing")

	before := harness.AmountView{Rounds: 1, WallClock: "5m0s", Tokens: 2_000_000}
	after := harness.AmountView{Rounds: 2, WallClock: "10m0s", Tokens: 4_000_000}
	code, extended := a.Admin().Extend(t, id, "extend", harness.ExtendBody{Rounds: &one, Reason: &harness.Reason{Code: "more_time"}})
	require.Equal(t, http.StatusOK, code, extended.Error)
	assert.True(t, extended.Changed)
	assert.True(t, extended.Consequences.Resumes, "the extension resumes the run")
	if assert.NotNil(t, extended.Consequences.Budget) {
		assert.Equal(t, before, extended.Consequences.Budget.Before, "one round's share of each dimension the extension leaves out")
		assert.Equal(t, after, extended.Consequences.Budget.After)
	}
	investigating, limit = counted(&extended.Consequences)
	assert.Equal(t, []int{1, 1}, []int{investigating, limit})
	if assert.NotNil(t, extended.Item, "the answer shows the item once the extension is done") {
		// Round 2 may end, and the item await its close again, before the
		// answer reads the item's view.
		switch extended.Item.State {
		case "investigating":
		case "awaiting_close":
			assert.Len(t, extended.Item.Rounds, 2, "an item awaiting its close in the answer ran round 2 already")
		default:
			assert.Fail(t, "the resume sends the item back to investigating", "state %q", extended.Item.State)
		}
	}

	code, again := a.Admin().Extend(t, id, "extend", harness.ExtendBody{Rounds: &one, Reason: &harness.Reason{Code: "more_time"}})
	require.Equal(t, http.StatusOK, code, again.Error)
	assert.True(t, again.Changed, "the extension repeated with its key answers changed, as it did")
	assert.True(t, again.Consequences.Resumes, "and that it resumed the run")
	if assert.NotNil(t, again.Consequences.Budget) {
		assert.Equal(t, before, again.Consequences.Budget.Before, "and the budget before it")
		assert.Equal(t, after, again.Consequences.Budget.After, "and after it")
	}

	v = a.Admin().WaitItem(t, id, func(v *harness.ItemView) bool { return stopped(v) && len(v.Rounds) == 2 }, runWait)
	assert.Equal(t, "budget_exhausted", v.Stop, "round 2 uses the budget extended")
	if assert.NotNil(t, v.Budget) {
		assert.Equal(t, after, v.Budget.AmountView, "the extension, repeated, extends once")
		assert.Equal(t, 2, v.Budget.Used.Rounds)
	}
	records := kinds(t, a, id)
	assert.Equal(t, 1, countOf(records, "run_extended"), "one run_extended record: %v", records)
	assert.Equal(t, 2, countOf(records, "item_awaiting_close"), "the item awaits its close twice: %v", records)
	assert.Equal(t, []string{"stop budget_exhausted", "resume", "stop budget_exhausted"}, stopsAndResumes(t, a, run),
		"the board's stop, its resume entry, and the second stop entry")

	code, refused := a.Admin().Extend(t, id, "clock", harness.ExtendBody{WallClock: "1m"})
	assert.Equal(t, http.StatusConflict, code, refused.Error)
	assert.Equal(t, "would_stop_at_once", refused.Code, "wall clock alone, the run's rounds spent, would not open a round")

	hold.Store(true)
	held := a.Admin().OpenItem(t, "held", incident, openWait)
	a.Admin().WaitItem(t, held, investigatingNow, runWait)
	promoted := a.Admin().OpenItem(t, "promoted", incident, openWait)
	waiting(t, a, promoted, 1)
	code, started := a.Admin().Promote(t, promoted, "promote")
	require.Equal(t, http.StatusOK, code, started.Error)
	investigating, limit = counted(&started.Consequences)
	require.Equal(t, []int{2, 1}, []int{investigating, limit}, "two items investigate, the limit and one")

	code, refused = a.Admin().Extend(t, id, "over", harness.ExtendBody{Rounds: &one})
	assert.Equal(t, http.StatusConflict, code, refused.Error)
	assert.Equal(t, "limit_reached", refused.Code, "a resume past the limit and one is refused")
	code, v = a.Admin().Item(t, id)
	require.Equal(t, http.StatusOK, code, v.Error)
	assert.Equal(t, "awaiting_close", v.State, "the refused resume changed nothing")
	requireRounds(t, &v, 2)
}

// Scenario 25: a link moves an item into another's incident, and an unlink
// gives it one of its own. B, awaiting its close, linked to A shares A's
// incident, though it records nothing after its link, and both views list
// both items; A records nothing. B unlinked has a new incident, of its own,
// and A's lists A alone. A link to an unknown item answers 404, no_such_item.
// The chain verifies.
func TestLinkedItemsShareAnIncidentAndUnlinkApart(t *testing.T) {
	env, a := serving(t, team(1), quick)
	env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply { return harness.EndTurn() })
	first := investigate(t, a, "first")
	second := investigate(t, a, "second")
	require.NotEqual(t, first.Incident, second.Incident, "each item opens its own incident")
	assert.Equal(t, []string{second.ID}, second.IncidentItems, "an item opened alone is its incident's only item")
	both := slices.Sorted(slices.Values([]string{first.ID, second.ID}))
	firstRecords := kinds(t, a, first.ID)

	code, linked := a.Admin().Link(t, second.ID, "link", first.ID, &harness.Reason{Code: "same_incident"})
	require.Equal(t, http.StatusOK, code, linked.Error)
	assert.True(t, linked.Changed)
	assert.Equal(t, &harness.IncidentChangeView{From: second.Incident, To: first.Incident, Items: both}, linked.Consequences.Incident,
		"the link leaves B's incident for A's, which holds both items")
	if assert.NotNil(t, linked.Item, "the answer shows the item once the link is done") {
		assert.Equal(t, first.Incident, linked.Item.Incident)
		assert.Equal(t, both, linked.Item.IncidentItems)
	}
	code, v := a.Admin().Item(t, second.ID)
	require.Equal(t, http.StatusOK, code, v.Error)
	assert.Equal(t, "awaiting_close", v.State, "the link changes nothing of B's run")
	assert.Equal(t, first.Incident, v.Incident, "B shares A's incident")
	assert.Equal(t, both, v.IncidentItems, "B's view lists both items")
	code, v = a.Admin().Item(t, first.ID)
	require.Equal(t, http.StatusOK, code, v.Error)
	assert.Equal(t, first.Incident, v.Incident)
	assert.Equal(t, both, v.IncidentItems, "A's view lists both items")
	records := kinds(t, a, second.ID)
	assert.Equal(t, "item_linked", records[len(records)-1], "B records nothing after its link, and is its new incident's all the same: %v", records)
	assert.Equal(t, firstRecords, kinds(t, a, first.ID), "A records nothing")

	code, unlinked := a.Admin().Unlink(t, second.ID, "unlink", &harness.Reason{Code: "separate_incident"})
	require.Equal(t, http.StatusOK, code, unlinked.Error)
	assert.True(t, unlinked.Changed)
	if assert.NotNil(t, unlinked.Consequences.Incident) {
		c := unlinked.Consequences.Incident
		assert.Equal(t, first.Incident, c.From, "the unlink leaves A's incident")
		assert.NotContains(t, []string{"", first.Incident, second.Incident}, c.To, "for a new incident, not B's first")
		assert.Equal(t, []string{second.ID}, c.Items, "which holds B alone")
		code, v = a.Admin().Item(t, second.ID)
		require.Equal(t, http.StatusOK, code, v.Error)
		assert.Equal(t, c.To, v.Incident, "B has an incident of its own")
		assert.Equal(t, []string{second.ID}, v.IncidentItems)
	}
	code, v = a.Admin().Item(t, first.ID)
	require.Equal(t, http.StatusOK, code, v.Error)
	assert.Equal(t, []string{first.ID}, v.IncidentItems, "A's incident holds A alone again")

	code, refused := a.Admin().Link(t, second.ID, "unknown", "no-such-item", &harness.Reason{Code: "same_incident"})
	assert.Equal(t, http.StatusNotFound, code, refused.Error)
	assert.Equal(t, "no_such_item", refused.Code, "a link to an unknown item")

	assert.Equal(t, []string{"store_unlocked"}, env.AuditVerify(t), "the chain verifies")
}
