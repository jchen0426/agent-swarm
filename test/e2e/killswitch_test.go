//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// The scenarios of Plan 7b-1 of the kill switch (Decisions 5 and 12). Its on
// stops every run, killed, and staffs no item, an item opened while it is on
// included, and pauses every auto-close, until its off, which names the items
// it left not staffed. A row the coordinator did not write stops every run all
// the same, at a takeover or at the leader's next renewal. A draining serve
// refuses every act on an item but the on, which it records for the next
// leader, whose takeover halts every run before any opens a round. The
// scenarios read what a person reads, as the lifecycle's do, whose helpers
// they share (lifecycle_test.go): no request after an answer is judged by what
// the vendor and the MCP server received, never by serve's log.

// stopping is the reason the scenarios give the kill switch's on, which needs
// one: a code alone.
var stopping = &harness.Reason{Code: "runaway_agents"}

// hanging is a model that never answers: every turn waits on its first call
// until the test ends.
func hanging(*harness.VendorRequest) harness.VendorReply { return harness.Hang() }

// runningTwo opens two items on instance a, and returns their ids once the
// vendor has received a call of each of their four turns: both runs are in
// their first round, and every turn waits on the vendor.
func runningTwo(t *testing.T, env *harness.Env, a *harness.Instance) []string {
	t.Helper()
	ids := []string{a.Admin().OpenItem(t, "first", incident, openWait), a.Admin().OpenItem(t, "second", incident, openWait)}
	calling(t, env, "a", 4)
	return ids
}

// callSessions returns, sorted, the sessions over which an instance's calls of
// a server's tools went: one for each run and agent that called the server,
// which the MCP gateway pools.
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

// count returns how many of codes are code.
func count(codes []string, code string) int {
	n := 0
	for _, c := range codes {
		if c == code {
			n++
		}
	}
	return n
}

// paused reports whether an item's view says that its auto-close is paused.
func paused(v *harness.ItemView) bool { return v.AutoClosePaused != nil && *v.AutoClosePaused }

// killedRun waits until an instance's admin API shows an item awaiting its
// close once its run stopped, and asserts that the run stopped killed and
// that the item is not staffed for the kill switch (R18).
func killedRun(t *testing.T, a *harness.Instance, id string) {
	t.Helper()
	v := a.Admin().WaitItem(t, id, stopped, runWait)
	assert.Equal(t, "killed", v.Stop, "item %s's run stopped killed", id)
	assert.Equal(t, "kill_switch", v.NotStaffed, "item %s is not staffed for the kill switch", id)
}

// notStaffed asserts that an item with no run awaits its close, not staffed
// for the kill switch, and that its auto-close is paused, as while the switch
// is on.
func notStaffed(t *testing.T, a *harness.Instance, id string) {
	t.Helper()
	code, v := a.Admin().Item(t, id)
	require.Equal(t, http.StatusOK, code, v.Error)
	assert.Equal(t, "awaiting_close", v.State, "item %s awaits its close", id)
	assert.Equal(t, "not_staffed", v.Stop, "item %s is not staffed", id)
	assert.Equal(t, "kill_switch", v.NotStaffed, "item %s is not staffed for the kill switch", id)
	assert.Empty(t, v.Rounds, "item %s never ran", id)
	assert.True(t, paused(&v), "item %s's auto-close is paused", id)
}

// insertOn appends an on-row to the kill switch's table by hand, with psql in
// the test's own database: a stand-in for a row whose writer crashed, or lost
// its answer, which no coordinator applied. As the store's rows are, it is the
// tenant's next row, recorded at the database's clock_timestamp(), so that
// the chain's trigger chains it; it is the admin's, with a reason's code
// alone.
func insertOn(t *testing.T, env *harness.Env) {
	t.Helper()
	env.Psql(t, `INSERT INTO kill_switch (tenant_id, seq, id, state, actor, reason_code, reason_text, recorded_at)
SELECT '`+env.Tenant()+`', coalesce(max(seq), 0) + 1, gen_random_uuid()::text, 'on', 'admin', '`+stopping.Code+`', '', clock_timestamp()
FROM kill_switch WHERE tenant_id = '`+env.Tenant()+`'`)
}

// byCause matches a "kill switch set" line of a cause: what changed the
// coordinator's view of the switch (Contract C3).
func byCause(cause string) func(map[string]any) bool {
	return func(l map[string]any) bool { return l["cause"] == cause }
}

// tookAt returns when a take of the lease was sent, by the wall clock of its
// process, which is the test's, as its "lease taken" line names it.
func tookAt(t *testing.T, taken map[string]any) time.Time {
	t.Helper()
	wall, err := time.Parse(time.RFC3339Nano, fmt.Sprint(taken["wall"]))
	require.NoError(t, err, "a take names its wall time: %v", taken)
	return wall
}

// Scenario 21: the kill switch's on stops every run, killed, and staffs no
// item. With two items investigating at once at most, two investigate, each
// of whose turns has completed a call of a tool and waits on an answer the
// vendor holds, and a third waits in the queue. The on stops both runs,
// killed, with one kill_switch notice on each board, and leaves the waiting
// item not staffed; after its answer the vendor receives no request, and the
// MCP server nothing but a DELETE for each session the runs pooled. An item
// opened while the switch is on is not staffed either. No item closes by
// itself while the switch holds, past their delay of 2 s. An off that names
// the row before the on is refused as stale, and one that names the on
// answers the four items it left not staffed. A promotion then staffs the
// item that waited, which never ran, and the other three close by themselves
// about 2 s after the off.
func TestTheKillSwitchStopsEveryRunAndStaffsNothingUntilOff(t *testing.T) {
	cfg := team(0)
	cfg.WorkType.AutoClose = autoClose
	d := quickRetry
	d.Lifecycle = harness.Lifecycle{Investigating: 2}
	env, a := serving(t, cfg, d)
	reading(env)
	env.Vendor().Script(readThenWait)
	first := a.Admin().OpenItem(t, "first", incident, openWait)
	second := a.Admin().OpenItem(t, "second", incident, openWait)
	heldAfterARead(t, env, "a", 4)
	turns := waitForTurns(t, a, 4)
	queued := a.Admin().OpenItem(t, "queued", incident, openWait)
	waiting(t, a, queued, 1)
	runs := map[string]string{first: runOf(t, env, first), second: runOf(t, env, second)}
	server := env.Server("logs")
	pooled := callSessions(t, server, "a")
	require.Len(t, pooled, 4, "a pooled session for each agent of each run")
	// The client reads the switch before it acts, and names the row it read
	// (R8).
	code, before := a.Admin().KillSwitch(t)
	require.Equal(t, http.StatusOK, code, before.Error)
	require.False(t, before.On, "the kill switch is off")

	sent := time.Now()
	code, on := a.Admin().SetKillSwitch(t, true, before.Seq, stopping)
	answered := time.Now()

	require.Equal(t, http.StatusOK, code, on.Error)
	assert.True(t, on.On)
	assert.Equal(t, before.Seq+1, on.Seq, "the on is the row after the one its sender read")
	assert.True(t, on.Changed)
	assert.False(t, on.Deferred)
	assert.ElementsMatch(t, []harness.StoppedRun{{Item: first, Run: runs[first], Round: 1}, {Item: second, Run: runs[second], Round: 1}},
		on.Stopped, "the on stops both runs, each in its first round")
	assert.Equal(t, []string{queued}, on.Unstaffed, "the on leaves the waiting item not staffed")
	assert.Empty(t, on.StayedUnstaffed)
	assert.NotNil(t, on.Owed, "an on's answer names what it still owes")
	assert.Empty(t, on.Owed, "the on owes nothing once it is answered")
	allGone(t, turns, "every turn the runs started is gone")
	for _, id := range []string{first, second} {
		killedRun(t, a, id)
		stops, notices := posted(t, a, runs[id])
		assert.Equal(t, []string{"killed"}, stops, "item %s's board's stop entry says killed", id)
		assert.Equal(t, 1, count(notices, "kill_switch"), "item %s's board holds one kill_switch notice: %v", id, notices)
	}
	notStaffed(t, a, queued)
	opened := a.Admin().OpenItem(t, "opened-while-on", incident, openWait)
	notStaffed(t, a, opened)

	// The switch holds past every item's delay, counted from its entry into
	// awaiting_close, of which the opening's is the last.
	unstaffed := []string{first, second, queued, opened}
	closedOne := func() bool {
		for _, id := range unstaffed {
			if code, v := a.Admin().Item(t, id); code == http.StatusOK && v.State == "closed" {
				return true
			}
		}
		return false
	}
	assert.False(t, within(autoClose+2*time.Second, closedOne), "no item closes by itself while the kill switch is on")
	for _, id := range unstaffed {
		code, v := a.Admin().Item(t, id)
		require.Equal(t, http.StatusOK, code, v.Error)
		assert.Equal(t, "awaiting_close", v.State, "item %s awaits its close", id)
		assert.True(t, paused(&v), "item %s's auto-close is paused", id)
		assert.Zero(t, v.ClosesAt, "item %s's view gives no time to close by itself", id)
	}
	// What the vendor and the MCP server received after the on's answer, read
	// once the hold has given anything late time to come, and before the
	// promotion staffs an item again.
	late := vendorAfter(env, answered)
	calls := server.Requests("a")
	for i := range calls {
		r := &calls[i]
		pooledClose := r.Method == http.MethodDelete && slices.Contains(pooled, r.Session)
		if r.At.After(answered) && !pooledClose {
			late = append(late, fmt.Sprintf("the MCP server: %s %s %s at %s", r.Method, r.Path, r.RPC, r.At.UTC().Format(time.RFC3339Nano)))
		}
	}
	assert.Empty(t, late, "nothing but the pooled sessions' DELETEs after the on's answer, at %s", answered.UTC().Format(time.RFC3339Nano))
	closes := deletes(server, "a")
	for _, session := range pooled {
		if assert.Len(t, closes[session], 1, "one DELETE for pooled session %s", session) {
			assert.False(t, closes[session][0].Before(sent), "the on, sent at %s, closed pooled session %s",
				sent.UTC().Format(time.RFC3339Nano), session)
		}
	}

	code, stale := a.Admin().SetKillSwitch(t, false, before.Seq, nil)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "stale", stale.Code, "an off that names the row before the on is refused")
	code, still := a.Admin().KillSwitch(t)
	require.Equal(t, http.StatusOK, code, still.Error)
	assert.True(t, still.On, "the refused off changed nothing")
	assert.Equal(t, on.Seq, still.Seq, "the refused off appended no row")

	code, off := a.Admin().SetKillSwitch(t, false, on.Seq, nil)
	// The waiting item awaits its close as well, so its auto-close counts from
	// the off too: it is promoted at once, well before its delay has passed.
	promoting, promoted := a.Admin().Promote(t, queued, "promote-queued")

	require.Equal(t, http.StatusOK, code, off.Error)
	assert.False(t, off.On)
	assert.Equal(t, on.Seq+1, off.Seq)
	assert.True(t, off.Changed)
	assert.ElementsMatch(t, []harness.UnstaffedItem{
		{Item: first, State: "awaiting_close"}, {Item: second, State: "awaiting_close"},
		{Item: queued, State: "awaiting_close"}, {Item: opened, State: "awaiting_close"},
	}, off.StayedUnstaffed, "the off answers the four items the on left not staffed")
	assert.False(t, off.StayedUnstaffedUnread)
	assert.Empty(t, off.Stopped)
	assert.Empty(t, off.Unstaffed)
	assert.NotNil(t, off.Owed, "an off's answer names what is still owed")
	assert.Empty(t, off.Owed, "nothing is owed")
	require.Equal(t, http.StatusOK, promoting, promoted.Error)
	assert.True(t, promoted.Changed)
	assert.Equal(t, []string{queued}, promoted.Consequences.Starts, "the promotion starts the item that waited")
	if assert.NotNil(t, promoted.Item, "the answer shows the item once the promotion is done") {
		assert.Equal(t, "investigating", promoted.Item.State)
	}
	code, switched := a.Admin().KillSwitch(t)
	require.Equal(t, http.StatusOK, code, switched.Error)
	require.False(t, switched.On)
	require.Equal(t, off.Seq, switched.Seq, "the latest row is the off")
	offAt := switched.Since
	for _, id := range []string{first, second, opened} {
		v := a.Admin().WaitItem(t, id, func(v *harness.ItemView) bool { return v.State == "closed" }, autoClose+10*time.Second)
		require.NotNil(t, v.Closed)
		assert.True(t, v.Closed.Auto, "item %s closed by itself", id)
		assert.Empty(t, v.Closed.By, "item %s closed by no one", id)
		assert.False(t, v.Closed.At.Before(offAt.Add(autoClose)), "item %s closed no sooner than its delay after the off, at %v; it closed at %v",
			id, offAt, v.Closed.At)
		assert.LessOrEqual(t, v.Closed.At.Sub(offAt), autoClose+time.Second, "item %s closed about %v after the off", id, autoClose)
	}
	v := a.Admin().WaitItem(t, queued, func(v *harness.ItemView) bool { return len(v.Rounds) > 0 }, runWait)
	assert.Equal(t, "investigating", v.State, "the promotion staffed the item that waited, which does not close by itself")
}

// Scenario 22: an on-row the coordinator did not write, which psql inserts
// as a stand-in for a row whose writer crashed or lost its answer, stops every
// run, killed, all the same, and the chain's trigger chains it. (a) Inserted
// once the leader was killed with its runs in a round, while no instance
// leads, it is applied by the standby's takeover, which opens no round. (b)
// Inserted while the leader leads, it is applied by the leader's renewal
// check, within two renewal periods: the read that follows the renewal's
// round trip, its sample of the database's clock and its check of the schema
// finds the row newer than the coordinator's view, whose refresh stops the
// runs. The vendor receives no request after the takeover, or after the
// refresh, and in both the chain verifies.
func TestAKillSwitchRowTheCoordinatorDidNotWriteStopsEveryRun(t *testing.T) {
	t.Run("inserted while no instance leads", func(t *testing.T) {
		env, _ := configured(t, []harness.ServerSpec{logs}, team(0))
		a := leading(t, env, leased)
		b := standingBy(t, env, leased)
		env.Vendor().Script(hanging)
		ids := runningTwo(t, env, a)

		a.Signal(t, syscall.SIGKILL)
		require.Equal(t, 128+int(syscall.SIGKILL), a.Wait(t, 10*time.Second), "a was killed")
		insertOn(t, env)
		// The row is in by now: a take sent later was sent while no instance
		// led.
		landed := time.Now()

		taken := b.Log().WaitFor(t, "lease taken", nil, leadWait)
		took := tookAt(t, taken)
		require.True(t, took.After(landed), "b took the lease at %s, after the row was in, at %s: no instance led when it was inserted",
			took.UTC().Format(time.RFC3339Nano), landed.UTC().Format(time.RFC3339Nano))
		for _, id := range ids {
			killedRun(t, b, id)
			assert.Equal(t, []int{1}, roundsOpened(t, b, id), "b opens no round of item %s", id)
		}
		assert.Empty(t, vendorAfter(env, took), "the vendor receives no request after b's take, at %s", took.UTC().Format(time.RFC3339Nano))
		assert.Equal(t, []string{"store_unlocked"}, env.AuditVerify(t), "the chain verifies")
	})

	t.Run("inserted while the leader leads", func(t *testing.T) {
		env, a := serving(t, team(0), leased)
		env.Vendor().Script(hanging)
		ids := runningTwo(t, env, a)

		// The bound counts from before psql runs, so that psql's own time
		// counts against it, as scenario 11(c)'s does.
		inserting := time.Now()
		insertOn(t, env)

		set := a.Log().WaitFor(t, "kill switch set", byCause("renewal"), leadWait)
		refreshed := loggedAt(t, set)
		assert.LessOrEqual(t, refreshed.Sub(inserting), 2*lease.RenewEvery+processes,
			"the leader's renewal check has its coordinator stop the runs within two renewal periods of the insert")
		for _, id := range ids {
			killedRun(t, a, id)
		}
		assert.Empty(t, vendorAfter(env, refreshed), "the vendor receives no request begun after the runs stopped, at %s",
			refreshed.UTC().Format(time.RFC3339Nano))
		assert.Equal(t, []string{"store_unlocked"}, env.AuditVerify(t), "the chain verifies")
	})
}

// Scenario 23: a drain, held open by the vendor's delayed answers to the
// turns' calls, refuses every act on an item as draining, and answers the
// reads. The kill switch's on is recorded under the draining holding, deferred
// for the next leader, and its off is refused as draining. The standby's
// takeover halts every run, each of which owes the on, before any of them
// opens a round, and the vendor receives no request after its take.
func TestADrainingServeRefusesLifecycleActsButTheKillSwitchsOn(t *testing.T) {
	d := leased
	d.Timing.Drain = 2 * drainHeld
	env, _ := configured(t, []harness.ServerSpec{logs}, team(0))
	a := leading(t, env, d)
	b := standingBy(t, env, d)
	env.Vendor().Script(byInstance(func(*harness.VendorRequest) harness.VendorReply { return harness.EndTurn().After(drainHeld) }))
	ids := runningTwo(t, env, a)
	a.Signal(t, syscall.SIGTERM)
	a.Log().WaitFor(t, "draining", nil, 10*time.Second)

	for _, act := range []string{"promote", "close", "reopen"} {
		code, refused := a.Admin().Act(t, ids[0], act, act+"-while-draining", harness.ActBody{})
		assert.Equal(t, http.StatusConflict, code, "the %s", act)
		assert.Equal(t, "draining", refused.Code, "the %s is refused as draining", act)
	}
	code, v := a.Admin().Item(t, ids[0])
	require.Equal(t, http.StatusOK, code, v.Error)
	assert.Equal(t, "investigating", v.State, "the item's view answers")
	code, before := a.Admin().KillSwitch(t)
	require.Equal(t, http.StatusOK, code, before.Error)
	require.False(t, before.On, "the kill switch is off")

	code, on := a.Admin().SetKillSwitch(t, true, before.Seq, stopping)

	require.Equal(t, http.StatusOK, code, on.Error)
	assert.True(t, on.On)
	assert.Equal(t, before.Seq+1, on.Seq)
	assert.True(t, on.Changed)
	assert.True(t, on.Deferred, "the on is recorded for the next leader")
	assert.Empty(t, on.Stopped, "the on stops no run itself")
	assert.Empty(t, on.Unstaffed)
	assert.Nil(t, on.Owed, "a deferred on's answer names nothing owed")
	code, status := a.Admin().Status(t)
	require.Equal(t, http.StatusOK, code)
	assert.True(t, status.Draining, "the status answers that serve drains")
	assert.Equal(t, harness.KillSwitchStatus{On: true, Seq: on.Seq}, status.KillSwitch, "and reads the on from the table")
	code, off := a.Admin().SetKillSwitch(t, false, on.Seq, nil)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "draining", off.Code, "the off is refused as draining")

	taken := b.Log().WaitFor(t, "lease taken", nil, leadWait)
	require.Equal(t, 0, a.Wait(t, leadWait), "a drains and exits 0")
	for _, id := range ids {
		killedRun(t, b, id)
		assert.Equal(t, []int{1}, roundsOpened(t, b, id), "b opens no round of item %s", id)
	}
	took := tookAt(t, taken)
	assert.Empty(t, vendorAfter(env, took), "the vendor receives no request after b's take, at %s", took.UTC().Format(time.RFC3339Nano))
}
