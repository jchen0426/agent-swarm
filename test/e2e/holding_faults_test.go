//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// The scenarios of Decision 17 (Plan 7d-1) of faults of the holding: a leader
// that dies, one that stops past its lease's time, one cut from the database,
// a rollback to a build that cannot read what a later one wrote, and a
// shutdown. Two instances, a and b, serve one database, unless a scenario
// says otherwise, and read it as a person would: serve's log, the admin API,
// inspect, the audit chain, and the requests the vendor and the MCP server
// received. The helpers the scenarios share are in helpers_test.go.

// lease is the lease's timing of these scenarios, shortened so that a
// takeover comes within seconds.
var lease = harness.LeaseTiming{
	TTL: 3 * time.Second, RenewEvery: time.Second, Margin: time.Second, TakeEvery: time.Second, TakeWait: 500 * time.Millisecond,
}

// leased is the deployment of these scenarios: quick's, with the lease's
// timing shortened and both of the database's timeouts at its time less its
// margin, which bounds every transaction a holding runs.
var leased = func() harness.DeploymentSpec {
	d := quick
	d.Timing.Lease = lease
	d.Timing.Database = harness.DatabaseTiming{TransactionTimeout: 2 * time.Second, IdleTimeout: 2 * time.Second}
	return d
}()

// processes is the time a bound allows the processes beyond it: to notice
// what happened, act on it and log it.
const processes = 2 * time.Second

// takeover is Decision 4's bound on a takeover after the holder goes silent:
// the lease's time, a wait for the next take, and one take, which waits at
// most takeWait; plus the time the processes take.
var takeover = lease.TTL + lease.TakeEvery + lease.TakeWait + processes

// standingBy runs instance b's serve on a deployment d, beside a leader,
// until it has started and a revision is current: it stands by, and takes
// the lease over once the leader's holding has expired, or been released.
//
//nolint:gocritic // hugeParam: the harness takes a scenario's specs by value.
func standingBy(t *testing.T, env *harness.Env, d harness.DeploymentSpec) *harness.Instance {
	t.Helper()
	b := env.Serve(t, "b", env.Deployment(t, "b", d))
	b.Log().WaitFor(t, "serve started", nil, leadWait)
	b.Log().WaitFor(t, "revision current", nil, leadWait)
	code, status := b.Admin().Status(t)
	require.Equal(t, http.StatusOK, code)
	require.False(t, status.Leading, "b stands by while a leads")
	return b
}

// byInstance answers the requests of instance a with a's reply, and those of
// any other instance as b does: each ends its turn at once.
func byInstance(a func(*harness.VendorRequest) harness.VendorReply) func(*harness.VendorRequest) harness.VendorReply {
	return func(r *harness.VendorRequest) harness.VendorReply {
		if r.Instance == "a" {
			return a(r)
		}
		return harness.EndTurn()
	}
}

// calling waits until the vendor has received n calls of an instance's.
func calling(t *testing.T, env *harness.Env, instance string, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return len(env.Vendor().Requests(instance)) >= n }, runWait, 50*time.Millisecond,
		"the vendor receives %d calls of %s's", n, instance)
}

// loggedAt returns when serve logged a line, by its process's clock, which is
// the test's.
func loggedAt(t *testing.T, line map[string]any) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, fmt.Sprint(line["time"]))
	require.NoError(t, err, "a log line's time: %v", line)
	return at
}

// epochOf returns the epoch a log line names.
func epochOf(t *testing.T, line map[string]any) float64 {
	t.Helper()
	epoch, ok := line["epoch"].(float64)
	require.True(t, ok, "the line names an epoch: %v", line)
	return epoch
}

// ofEpoch matches a log line of an epoch.
func ofEpoch(epoch float64) func(map[string]any) bool {
	return func(l map[string]any) bool { return l["epoch"] == epoch }
}

// logged returns the lines of a log whose msg is msg, in order.
func logged(lines []map[string]any, msg string) []map[string]any {
	var out []map[string]any
	for _, line := range lines {
		if line["msg"] == msg {
			out = append(out, line)
		}
	}
	return out
}

// firstOf returns the position of the first line of a log whose msg is msg
// and that match accepts, when it is not nil, or -1 when there is none.
func firstOf(lines []map[string]any, msg string, match func(map[string]any) bool) int {
	return slices.IndexFunc(lines, func(l map[string]any) bool { return l["msg"] == msg && (match == nil || match(l)) })
}

// allGone waits until no process of pids is left.
func allGone(t *testing.T, pids []int, why string) {
	t.Helper()
	assert.Eventually(t, func() bool { return !slices.ContainsFunc(pids, func(pid int) bool { return !gone(pid) }) },
		10*time.Second, 50*time.Millisecond, "%s: %v", why, pids)
}

// carriedOn asserts that a run another holding took over reached its stop by
// the turns that holding ran: its last round is quiet, with every agent's
// turn end.
func carriedOn(t *testing.T, v *harness.ItemView) {
	t.Helper()
	require.NotEmpty(t, v.Rounds, "the run had rounds")
	last := v.Rounds[len(v.Rounds)-1]
	assert.Equal(t, "quiet", v.Stop)
	assert.True(t, last.Quiet, "round %d is quiet: %+v", last.Round, last.Evidence)
	assert.ElementsMatch(t, []string{"alpha", "beta"}, last.Evidence.TurnEnds, "round %d: every agent ended its turn", last.Round)
}

// roundsOpened returns the rounds an item's round_opened records open, in the
// order they were recorded.
func roundsOpened(t *testing.T, a *harness.Instance, item string) []int {
	t.Helper()
	var out []int
	for _, line := range a.Inspect(t, "item", item) {
		if line["kind"] != "round_opened" {
			continue
		}
		raw, err := json.Marshal(line["body"])
		require.NoError(t, err)
		var body struct {
			RoundOpened *struct {
				Round int `json:"round"`
			} `json:"roundOpened"`
		}
		require.NoError(t, json.Unmarshal(raw, &body))
		require.NotNil(t, body.RoundOpened, "a round_opened record opens a round: %s", raw)
		out = append(out, body.RoundOpened.Round)
	}
	return out
}

// Scenario 8: the leader dies with model calls in flight. The standby takes
// the lease over within Decision 4's bound and gives the calls the dead
// leader left an outcome, naming it, before its round's end reads them; the
// dead leader's turns end with it; the standby carries the item on, and
// nothing is recorded twice.
func TestAStandbyTakesOverFromAKilledLeader(t *testing.T) {
	env, _ := configured(t, []harness.ServerSpec{logs}, team(0))
	a := leading(t, env, leased)
	b := standingBy(t, env, leased)
	env.Vendor().Script(byInstance(func(*harness.VendorRequest) harness.VendorReply { return harness.Hang() }))
	id := a.Admin().OpenItem(t, "killed", incident, openWait)
	calling(t, env, "a", 2)

	killed := time.Now()
	a.Signal(t, syscall.SIGKILL)

	taken := b.Log().WaitFor(t, "lease taken", nil, leadWait)
	assert.LessOrEqual(t, loggedAt(t, taken).Sub(killed), takeover, "b takes the lease within Decision 4's bound of a's death")
	require.Equal(t, 128+int(syscall.SIGKILL), a.Wait(t, 10*time.Second), "a was killed")
	v := b.Admin().WaitItem(t, id, stopped, runWait)
	requireRounds(t, &v, 2, "round 1, carried on, which the swept calls keep from being quiet, then a quiet round")
	assert.Equal(t, 2, v.Rounds[0].Evidence.FailedModelCalls, "round 1's end read the outcomes of a's calls")
	assert.True(t, v.Rounds[0].Evidence.Settled, "no call of round 1 was left in flight")
	carriedOn(t, &v)

	var swept []callLine
	for _, c := range callsOf(t, b, runOf(t, env, id), "model") {
		require.NotEmpty(t, c.Status, "every model call has an outcome: %+v", c)
		if c.Status != "ok" {
			swept = append(swept, c)
		}
	}
	require.Len(t, swept, 2, "a's calls in flight, and b's answered: %+v", swept)
	for _, c := range swept {
		assert.Equal(t, "unknown", c.Status, "%s's call", c.Agent)
		assert.Equal(t, 1, c.Round, "%s's call", c.Agent)
		assert.Regexp(t, `\binstance a\b`, c.Reason, "the reason names the instance that stopped")
	}
	assert.ElementsMatch(t, []string{"alpha", "beta"}, []string{swept[0].Agent, swept[1].Agent})
	pids := turnPIDs(a)
	require.NotEmpty(t, pids, "a started its turns")
	allGone(t, pids, "every turn a started is gone")
	assert.Equal(t, []string{"store_unlocked"}, env.AuditVerify(t), "the chain verifies")
	opened := roundsOpened(t, b, id)
	assert.Equal(t, []int{1, 2}, opened, "each round is opened once")
}

// Scenario 9: the leader stops, as a process cut off by a partition does, for
// longer than its lease's time. The standby takes the lease over within
// Decision 4's bound. Once the old leader runs again, it finds its holding
// lost and fences itself, and neither the vendor nor the MCP server hears
// from it after the standby's take, though each of its gateways had a request
// to send, one its fence cannot stop: it asks the holding before every
// request.
func TestAPartitionedLeaderSendsNothingAfterTheStandbysTake(t *testing.T) {
	env, _ := configured(t, []harness.ServerSpec{logs}, team(0))
	a := leading(t, env, leased)
	epoch := epochOf(t, a.Log().WaitFor(t, "lease taken", nil, time.Second))
	b := standingBy(t, env, leased)
	pid := a.Addresses().PID
	// A test that fails while a is stopped lets it run again, so that it ends
	// with the test.
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGCONT) })
	// a stops with a request in each gateway's hands that does not end with
	// the turn that asked for it, and that a sends once it runs again unless
	// the holding refuses it. alpha's model calls are answered 529, which the
	// LLM gateway tries again, apart from the turn's request, after a second,
	// then two, then four. beta reads the logs, and each of a's reads is
	// answered 500, after which the MCP gateway drops its session to the
	// server and closes it, apart from the call. The first read that finds
	// alpha's call waiting to be tried again stops a just before it is
	// answered, so that a reads the 500, and closes the session, only once it
	// runs again.
	env.Vendor().Script(byInstance(func(r *harness.VendorRequest) harness.VendorReply {
		if r.Agent == "alpha" {
			return overloaded
		}
		return harness.ToolUse(reads[r.Agent], readsWith[r.Agent])
	}))
	stops := make(chan time.Time, 1)
	stopped := false
	env.Server("logs").Script("deploys", func(c *harness.ToolCall) harness.ToolReply {
		if c.Instance != "a" {
			return harness.Result(deploysOutput)
		}
		if !stopped && waitsAgain(env.Vendor().Requests("a")) {
			stopped = true
			stops <- time.Now()
			if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
				t.Errorf("stop a: %v", err)
			}
		}
		return harness.HTTPFailure(http.StatusInternalServerError)
	})
	a.Admin().OpenItem(t, "partitioned", incident, openWait)

	var paused time.Time
	select {
	case paused = <-stops:
	case <-time.After(runWait):
		t.Fatal("no read of a's found alpha's call waiting to be tried again, so a never stopped")
	}
	taken := b.Log().WaitFor(t, "lease taken", nil, leadWait)
	assert.LessOrEqual(t, loggedAt(t, taken).Sub(paused), takeover, "b takes the lease within Decision 4's bound of a's stop")
	time.Sleep(time.Until(paused.Add(lease.TTL + 2*time.Second)))
	a.Signal(t, syscall.SIGCONT)

	wall, err := time.Parse(time.RFC3339Nano, fmt.Sprint(taken["wall"]))
	require.NoError(t, err, "b's take names its wall time: %v", taken)
	late := func() []string {
		var out []string
		for _, r := range env.Vendor().Requests("a") {
			if r.At.After(wall) {
				out = append(out, r.Method+" "+r.Path+" at "+r.At.Format(time.RFC3339Nano))
			}
		}
		for _, r := range env.Server("logs").Requests("a") {
			if r.At.After(wall) {
				out = append(out, r.Method+" "+r.Path+" "+r.RPC+" at "+r.At.Format(time.RFC3339Nano))
			}
		}
		return out
	}
	// a's process catches up once it runs again: the call its LLM gateway
	// waits to try again, the read its MCP gateway was answered 500, whose
	// session it closes, and its renewal and its watch, which find the
	// holding lost.
	assert.Never(t, func() bool { return len(late()) > 0 }, 3*time.Second, 50*time.Millisecond,
		"the vendor and the MCP server hear from a after b's take")
	a.Log().WaitFor(t, "lease lost", ofEpoch(epoch), leadWait)
	a.Log().WaitFor(t, "fenced", ofEpoch(epoch), leadWait)
	pids := turnPIDs(a)
	require.NotEmpty(t, pids, "a started its turns")
	allGone(t, pids, "a's fence stops its turns")
	assert.Empty(t, late(), "no request of a's reached the vendor or the MCP server after b's take, at %s", wall.Format(time.RFC3339Nano))
}

// failed bounds how long the LLM gateway takes to read a failure the vendor
// answered a call with and begin its wait, of a second at least, to try the
// call again.
const failed = 50 * time.Millisecond

// waitsAgain reports whether alpha's latest model call among the requests is
// over a moment old: the LLM gateway has read the failure it was answered
// with, and waits to try the call again.
func waitsAgain(requests []harness.VendorRequest) bool {
	for i := len(requests) - 1; i >= 0; i-- {
		if requests[i].Agent == "alpha" {
			return time.Since(requests[i].At) > failed
		}
	}
	return false
}

// lastRenewal dates, by the test's clock, the last renewal of the lease that
// reached the database: its expiry less the lease's time, by the time left on
// it, which the database's own clock reads. It may date the renewal early,
// by as long as psql takes to start, never late.
func lastRenewal(t *testing.T, env *harness.Env) time.Time {
	t.Helper()
	asked := time.Now()
	out := env.Psql(t, "SELECT extract(epoch FROM expires_at - clock_timestamp()) FROM leases WHERE tenant_id = 'harness'")
	left, err := strconv.ParseFloat(out, 64)
	require.NoError(t, err, "the time left on the lease: %q", out)
	return asked.Add(time.Duration(left*float64(time.Second)) - lease.TTL)
}

// fencing is the time a leader takes to fence its coordinator and log it once
// its watch fires, together with the time psql takes to start, by which
// lastRenewal may date a renewal early.
const fencing = 500 * time.Millisecond

// Scenario 10: the leader is cut from the database. With no renewal it fences
// itself at the lease's time less its margin after its last good renewal,
// before its holding could expire; once the database is back, it takes the
// lease again, under a later epoch, within Decision 4's bound, and carries its
// item on to its stop.
func TestALeaderCutFromTheDatabaseFencesAndTakesTheLeaseAgain(t *testing.T) {
	env := harness.New(t, harness.Options{Toxiproxy: true, Servers: []harness.ServerSpec{logs}})
	env.Migrate(t)
	env.Config(t, team(0))
	a := leading(t, env, leased)
	first := a.Log().WaitFor(t, "lease taken", nil, time.Second)
	// Every answer ends the agent's turn, 2 s late: the round's calls are in
	// flight when the database is cut, and their answers come while it is.
	env.Vendor().Script(func(*harness.VendorRequest) harness.VendorReply { return harness.EndTurn().After(2 * time.Second) })
	id := a.Admin().OpenItem(t, "cut", incident, openWait)
	calling(t, env, "a", 2)

	cutting := time.Now()
	env.Cut(t, "a")
	// No renewal of a's reaches the database from here on: the lease's row
	// holds the last, which psql reads past the proxy.
	renewed := lastRenewal(t, env)
	fenced := a.Log().WaitFor(t, "fenced", ofEpoch(epochOf(t, first)), leadWait)
	at := loggedAt(t, fenced)
	assert.True(t, at.After(cutting), "a fences once it is cut, not before")
	assert.LessOrEqual(t, at.Sub(renewed), lease.TTL-lease.Margin+fencing,
		"a fences within the lease's time less its margin of its last good renewal")
	env.Restore(t, "a")
	restored := time.Now()

	again := a.Log().WaitFor(t, "lease taken", func(l map[string]any) bool { return epochOf(t, l) > epochOf(t, first) }, leadWait)
	assert.LessOrEqual(t, loggedAt(t, again).Sub(restored), takeover, "a takes the lease again within Decision 4's bound of the database's return")
	v := a.Admin().WaitItem(t, id, stopped, runWait)
	carriedOn(t, &v)
}

// laterRecord appends to an item's records one of version 2, as a later build
// writes one: this build cannot read it.
func laterRecord(t *testing.T, env *harness.Env, item string) {
	t.Helper()
	env.Psql(t, `INSERT INTO work_records (tenant_id, item_id, seq, id, incident_id, run_id, kind, body, recorded_at)
SELECT tenant_id, item_id, max(seq) + 1, 'later-' || item_id, max(incident_id), max(run_id), 'round_ended',
       '{"v": 2, "roundEnded": {"round": 1, "shape": "of a later build"}}', clock_timestamp()
FROM work_records WHERE tenant_id = 'harness' AND item_id = '`+item+`' GROUP BY tenant_id, item_id`)
}

// laterEvent appends to the board of an item's run an event of version 2, as
// a later build writes one: this build cannot read it.
func laterEvent(t *testing.T, env *harness.Env, item string) {
	t.Helper()
	run := runOf(t, env, item)
	env.Psql(t, `INSERT INTO board_events (tenant_id, run_id, seq, id, round, kind, author_kind, author_name, committed_at, body)
SELECT tenant_id, run_id, max(seq) + 1, 'later-' || run_id, max(round), 'notice', 'platform', 'coordinator', clock_timestamp(),
       '{"v": 2, "notice": {"shape": "of a later build"}}'
FROM board_events WHERE tenant_id = 'harness' AND run_id = '`+run+`' GROUP BY tenant_id, run_id`)
}

// Scenario 11(a): a rollback. A build that cannot read what a later build
// wrote of an item, a work record or a board event of version 2, takes the
// lease over: it skips that item, which it shows as held by a later build,
// counts in its status and leaves alone, and carries the other item on.
func TestARollbackSkipsTheItemsALaterBuildWrote(t *testing.T) {
	for _, tc := range []struct {
		name  string
		later func(t *testing.T, env *harness.Env, item string)
	}{
		{name: "a work record of version 2", later: laterRecord},
		{name: "a board event of version 2", later: laterEvent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := configured(t, []harness.ServerSpec{logs}, team(0))
			a := leading(t, env, leased)
			b := standingBy(t, env, leased)
			// a's calls never end, so that neither item writes while the
			// later version is written.
			env.Vendor().Script(byInstance(func(*harness.VendorRequest) harness.VendorReply { return harness.Hang() }))
			skipped := a.Admin().OpenItem(t, "written-later", incident, openWait)
			other := a.Admin().OpenItem(t, "other", incident, openWait)
			calling(t, env, "a", 4)
			tc.later(t, env, skipped)
			run := runOf(t, env, skipped)
			records, events := a.Inspect(t, "item", skipped), a.Inspect(t, "board", run)

			a.Signal(t, syscall.SIGKILL)
			b.Log().WaitFor(t, "lease taken", nil, leadWait)
			v := b.Admin().WaitItem(t, other, stopped, runWait)

			carriedOn(t, &v)
			code, view := b.Admin().Item(t, skipped)
			require.Equal(t, http.StatusOK, code)
			assert.Equal(t, harness.ItemView{ID: skipped, HeldByLaterBuild: true}, view, "the item is shown held by a later build, and nothing else")
			code, status := b.Admin().Status(t)
			require.Equal(t, http.StatusOK, code)
			assert.True(t, status.Leading)
			assert.Equal(t, 1, status.Skipped, "b skips one item")
			skips := logged(b.Log().Lines(), "item skipped")
			require.Len(t, skips, 1, "b logs the skip once")
			assert.Equal(t, skipped, skips[0]["item"])
			assert.Equal(t, records, b.Inspect(t, "item", skipped), "b writes no record of the item")
			assert.Equal(t, events, b.Inspect(t, "board", run), "b writes nothing on its board")
		})
	}
}

// buildLevel returns this build's schema level, from what migrate printed of
// a fresh database: the level it left the schema at, its last line.
func buildLevel(t *testing.T, migrated string) int {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(migrated), "\n")
	m := level.FindStringSubmatch(lines[len(lines)-1])
	require.NotNil(t, m, "migrate prints the schema's level last: %q", migrated)
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return n
}

// Scenario 11(b): a later migration that this build can read, one whose
// min_reader is this build's level, does not keep serve from starting, or
// its leader from leading.
func TestServeStartsOnACompatibleLaterSchema(t *testing.T) {
	env := harness.New(t, harness.Options{Servers: []harness.ServerSpec{logs}})
	build := buildLevel(t, env.Migrate(t))
	env.Config(t, team(0))
	env.Psql(t, fmt.Sprintf("INSERT INTO schema_migrations (name, min_reader) VALUES ('0099_later.sql', %d)", build))

	a := env.Serve(t, "a", env.Deployment(t, "a", leased))

	started := a.Log().WaitFor(t, "serve started", nil, leadWait)
	assert.InDelta(t, build, started["level"], 0, "the later migration's min_reader is the level of the build that starts")
	code, body := a.Admin().Readyz(t)
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "ready", body)
	status := a.Admin().WaitStatus(t, func(s *harness.InstanceStatus) bool { return s.Leading }, leadWait)
	assert.Equal(t, "compatible", status.Schema)
}

// Scenario 11(c): a later migration that this build cannot read, whose
// min_reader is above this build's level. A leader stops at its next
// renewal, without a drain, since it must not write into a schema it cannot
// run on: it fences, releases the lease and exits 1. A new serve refuses to
// start.
func TestAnIncompatibleSchemaStopsTheLeaderWithoutADrain(t *testing.T) {
	env, _ := configured(t, []harness.ServerSpec{logs}, team(0))
	a := leading(t, env, leased)
	epoch := epochOf(t, a.Log().WaitFor(t, "lease taken", nil, time.Second))

	inserted := time.Now()
	env.Psql(t, "INSERT INTO schema_migrations (name, min_reader) VALUES ('0099_later.sql', 99)")

	require.Equal(t, 1, a.Wait(t, leadWait), "a exits 1")
	lines := a.Log().Lines()
	incompatible := firstOf(lines, "schema incompatible", nil)
	fenced := firstOf(lines, "fenced", ofEpoch(epoch))
	released := firstOf(lines, "lease released", ofEpoch(epoch))
	require.GreaterOrEqual(t, incompatible, 0, "a logs that the schema is incompatible")
	require.Greater(t, fenced, incompatible, "a fences once it finds the schema incompatible")
	require.Greater(t, released, fenced, "a releases the lease once it has fenced")
	assert.LessOrEqual(t, loggedAt(t, lines[incompatible]).Sub(inserted), lease.RenewEvery+processes, "a finds it at its next renewal")
	assert.Contains(t, lines[incompatible]["reason"], "min_reader 99", "the reason names the levels")
	assert.Equal(t, -1, firstOf(lines, "draining", nil), "a does not drain")
	assert.GreaterOrEqual(t, firstOf(lines, "serve stopped", func(l map[string]any) bool { return l["code"] == float64(1) }), 0)

	again := env.Serve(t, "a", env.Deployment(t, "a", leased))

	assert.Equal(t, 1, again.Wait(t, leadWait), "a new serve exits 1")
	assert.Zero(t, again.Addresses(), "it bound no listener")
	refusals := logged(again.Log().Lines(), "schema incompatible")
	require.Len(t, refusals, 1, "it logs that the schema is incompatible")
	assert.Contains(t, refusals[0]["reason"], "min_reader 99")
	assert.Empty(t, logged(again.Log().Lines(), "serve started"), "it does not start")
}

// Scenario 13: a shutdown with model calls in flight. Serve drains under the
// lease it holds: the calls' answers come and are recorded under its
// holding, and only then is the lease released; the standby takes it over
// within Decision 6's failover wait after a drain.
func TestShutdownDrainsUnderTheHeldLeaseThenReleases(t *testing.T) {
	const delayed = 3 * time.Second
	d := leased
	d.Timing.Drain = 2 * delayed
	env, _ := configured(t, []harness.ServerSpec{logs}, team(0))
	a := leading(t, env, d)
	epoch := epochOf(t, a.Log().WaitFor(t, "lease taken", nil, time.Second))
	b := standingBy(t, env, d)
	env.Vendor().Script(byInstance(func(*harness.VendorRequest) harness.VendorReply { return harness.EndTurn().After(delayed) }))
	id := a.Admin().OpenItem(t, "shutdown", incident, openWait)
	calling(t, env, "a", 2)

	signaled := time.Now()
	a.Signal(t, syscall.SIGTERM)

	taken := b.Log().WaitFor(t, "lease taken", nil, leadWait)
	assert.LessOrEqual(t, loggedAt(t, taken).Sub(signaled),
		3*d.Timing.MCP.RecordTimeout+d.Timing.Drain+lease.TakeEvery+lease.TakeWait+processes,
		"b takes the lease within Decision 6's failover wait after a drain")
	require.Equal(t, 0, a.Wait(t, leadWait), "a drains and exits 0")
	lines := a.Log().Lines()
	draining := firstOf(lines, "draining", ofEpoch(epoch))
	released := firstOf(lines, "lease released", ofEpoch(epoch))
	require.GreaterOrEqual(t, draining, 0, "a drains")
	assert.Greater(t, released, draining, "a releases the lease once it has drained")
	assert.Greater(t, epochOf(t, taken), epoch)
	v := b.Admin().WaitItem(t, id, stopped, runWait)
	carriedOn(t, &v)

	// Every call has an outcome the vendor's answer gave: a's delayed calls',
	// recorded under a's holding, during its drain, since b's sweep would
	// have given one that a left without an outcome "unknown".
	calls := callsOf(t, b, runOf(t, env, id), "model")
	assert.Len(t, calls, len(env.Vendor().Requests("a"))+len(env.Vendor().Requests("b")), "a call for each request the vendor answered")
	for _, c := range calls {
		assert.Equal(t, "ok", c.Status, "%s's call of round %d: %s", c.Agent, c.Round, c.Reason)
	}
}
