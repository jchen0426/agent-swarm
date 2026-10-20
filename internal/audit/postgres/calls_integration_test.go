//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/audit/postgres"
	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	workapp "github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	workpostgres "github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var _ workapp.ToolCalls = (*postgres.Calls)(nil)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

var at = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

func record(id string) *app.CallRecord {
	return &app.CallRecord{
		ID: id, Run: "run-1", Agent: "flag-investigator", Server: "flags", Tool: "get_flag",
		SchemaHash: "sha256:pin", Args: json.RawMessage(`{"flag":"x"}`), Round: 2, At: at,
	}
}

func TestACallIsRecordedThenItsOutcome(t *testing.T) {
	calls := postgres.NewCalls(pgtest.Pool(t), "t1")
	require.NoError(t, calls.Begin(t.Context(), record("c-1")))

	before, err := calls.ReadCall(t.Context(), "c-1")
	require.NoError(t, err)
	assert.True(t, before.Failed, "a call that has not ended cannot be cited")

	require.NoError(t, calls.End(t.Context(), &app.Outcome{ID: "c-1", Status: app.StatusOK, Output: `{"on":true}`, At: at}))
	after, err := calls.ReadCall(t.Context(), "c-1")
	require.NoError(t, err)
	assert.Equal(t, "run-1", string(after.Run))
	assert.Equal(t, "flag-investigator", after.Agent)
	assert.Equal(t, "flags", after.Server)
	assert.Equal(t, "get_flag", after.Tool)
	assert.False(t, after.Failed)
	assert.Equal(t, `{"on":true}`, string(after.Output))
	assert.JSONEq(t, `{"flag":"x"}`, string(after.Args), "the stored arguments, which a quote may not merely repeat")

	_, err = calls.ReadCall(t.Context(), "c-unknown")
	require.ErrorIs(t, err, workapp.ErrCallNotFound)
}

func TestARefusedOrFailedCallCannotBeCited(t *testing.T) {
	calls := postgres.NewCalls(pgtest.Pool(t), "t1")
	for id, status := range map[string]app.Status{"c-2": app.StatusRefused, "c-3": app.StatusReadError, "c-recorded": app.StatusRecorded} {
		require.NoError(t, calls.Begin(t.Context(), record(id)))
		require.NoError(t, calls.End(t.Context(), &app.Outcome{ID: id, Status: status, Output: "why", At: at}))

		c, err := calls.ReadCall(t.Context(), id)

		require.NoError(t, err)
		assert.True(t, c.Failed)
		assert.Empty(t, c.Output)
	}
}

func TestAnOutcomeRecordedAgainIsOneOutcome(t *testing.T) {
	calls := postgres.NewCalls(pgtest.Pool(t), "t1")
	require.NoError(t, calls.Begin(t.Context(), record("c-4")))
	o := &app.Outcome{ID: "c-4", Status: app.StatusOK, Output: "on", At: at}
	require.NoError(t, calls.End(t.Context(), o))

	require.NoError(t, calls.End(t.Context(), o), "a repeated write after a timeout")
	err := calls.End(t.Context(), &app.Outcome{ID: "c-4", Status: app.StatusOK, Output: "off", At: at})
	require.ErrorIs(t, err, postgres.ErrConflict)
	err = calls.End(t.Context(), &app.Outcome{ID: "c-4", Status: app.StatusOK, Failure: app.FailureServer, Output: "on", At: at})
	require.ErrorIs(t, err, postgres.ErrConflict, "another failure is another outcome")
}

func TestTextThatPostgresCannotHoldIsStoredReplaced(t *testing.T) {
	calls := postgres.NewCalls(pgtest.Pool(t), "t1")
	require.NoError(t, calls.Begin(t.Context(), record("c-5")))

	require.NoError(t, calls.End(t.Context(), &app.Outcome{ID: "c-5", Status: app.StatusOK, Output: "a" + string([]byte{0, 0xff}) + "b", At: at}))

	c, err := calls.ReadCall(t.Context(), "c-5")
	require.NoError(t, err)
	assert.Equal(t, "a\uFFFD\uFFFDb", string(c.Output))
}

func TestRecordsNeverChange(t *testing.T) {
	pool := pgtest.Pool(t)
	calls := postgres.NewCalls(pool, "t1")
	require.NoError(t, calls.Begin(t.Context(), record("c-6")))
	require.NoError(t, calls.End(t.Context(), &app.Outcome{ID: "c-6", Status: app.StatusOK, Output: "on", At: at}))

	for _, stmt := range []string{
		"UPDATE tool_calls SET agent = 'someone' WHERE call_id = 'c-6'",
		"DELETE FROM tool_call_outcomes WHERE call_id = 'c-6'",
		"TRUNCATE tool_hides",
	} {
		_, err := pool.Exec(t.Context(), stmt)
		require.ErrorContains(t, err, "append-only", stmt)
	}
}

func TestTenantsAreKeptApart(t *testing.T) {
	pool := pgtest.Pool(t)
	require.NoError(t, postgres.NewCalls(pool, "t1").Begin(t.Context(), record("c-7")))

	_, err := postgres.NewCalls(pool, "t2").ReadCall(t.Context(), "c-7")

	require.ErrorIs(t, err, workapp.ErrCallNotFound)
}

func TestAHideIsKeptOnce(t *testing.T) {
	hides := postgres.NewHides(pgtest.Pool(t), "t-hides")
	h := &app.Hide{
		Endpoint: "https://flags.internal/mcp", Server: "flags", Tool: "get_flag", PinnedHash: "sha256:pin",
		Registration: "sha256:reg", Reason: app.ReasonChanged, Observed: "sha256:other", At: at,
	}

	require.NoError(t, hides.Hide(t.Context(), h))
	require.NoError(t, hides.Hide(t.Context(), h))

	got, err := hides.Hidden(t.Context())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.WithinDuration(t, time.Now(), got[0].At, time.Minute, "the database's time")
	got[0].At = h.At
	assert.Equal(t, *h, got[0])
}

// A call without a server, a board tool's or a name the agent could not
// call, is no read call: evidence cannot cite it.
func TestACallWithoutAServerIsNoReadCall(t *testing.T) {
	calls := postgres.NewCalls(pgtest.Pool(t), "t1")
	rec := record("c-board")
	rec.Server, rec.Tool = "", "post_evidence"
	require.NoError(t, calls.Begin(t.Context(), rec))
	require.NoError(t, calls.End(t.Context(), &app.Outcome{ID: "c-board", Status: app.StatusOK, Output: "posted evidence e-1", At: at}))

	_, err := calls.ReadCall(t.Context(), "c-board")

	require.ErrorIs(t, err, workapp.ErrCallNotFound)
}

// A read made for a write call names it, so that it is never taken for one
// of the platform's own reads.
func TestAReadMadeForAWriteCallNamesIt(t *testing.T) {
	pool := pgtest.Pool(t)
	calls := postgres.NewCalls(pool, "t1")
	rec := record("c-candidates")
	rec.Agent, rec.Cause = "", "c-write"
	require.NoError(t, calls.Begin(t.Context(), rec))

	var cause string
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT cause FROM tool_calls WHERE call_id = 'c-candidates'").Scan(&cause))
	assert.Equal(t, "c-write", cause)
}

// heldLease takes a lease of tenant, releases it and takes it again: it
// returns the lease, a context under the first holding, which is over, and
// one under the second, which holds.
func heldLease(t *testing.T, pool *pgxpool.Pool, tenant string) (lease *workpostgres.Lease, stale, current context.Context) {
	t.Helper()
	lease = workpostgres.NewLease(pool, tenant, "platform", "a", time.Minute, 5*time.Second)
	first, took, err := lease.Take(t.Context())
	require.NoError(t, err)
	require.True(t, took)
	require.NoError(t, lease.Release(t.Context()))
	second, took, err := lease.Take(t.Context())
	require.NoError(t, err)
	require.True(t, took)
	return lease, holding.WithEpoch(t.Context(), first.Epoch), holding.WithEpoch(t.Context(), second.Epoch)
}

// rows counts a tenant's rows of an audit table.
func rows(t *testing.T, pool *pgxpool.Pool, table, tenant string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table+" WHERE tenant_id = $1", tenant).Scan(&n))
	return n
}

// Tool calls and their outcomes are written only under the holding the write
// acts for: one under an epoch whose holding is over is refused, wrapping
// holding.ErrLeaseLost, and leaves no row; one under the current epoch lands.
func TestToolCallsAndTheirOutcomesAreWrittenOnlyUnderTheHolding(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "calls-guarded"
	lease, stale, current := heldLease(t, pool, tenant)
	calls := postgres.NewCalls(pool, tenant).Guarded(lease)
	o := &app.Outcome{ID: "g-1", Status: app.StatusOK, Output: "on", At: at}

	require.ErrorIs(t, calls.Begin(stale, record("g-1")), holding.ErrLeaseLost)
	assert.Zero(t, rows(t, pool, "tool_calls", tenant), "no call")
	require.NoError(t, calls.Begin(current, record("g-1")))
	require.ErrorIs(t, calls.End(stale, o), holding.ErrLeaseLost)
	assert.Zero(t, rows(t, pool, "tool_call_outcomes", tenant), "no outcome")
	require.NoError(t, calls.End(current, o))

	assert.Equal(t, 1, rows(t, pool, "tool_calls", tenant))
	assert.Equal(t, 1, rows(t, pool, "tool_call_outcomes", tenant))
}

// clockTimestamp reads the database's clock.
func clockTimestamp(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT clock_timestamp()").Scan(&now))
	return now
}

// Every audit record is written in the database's time, whatever time its
// writer gives it (Decision 1): a tool call, its outcome, a hide, a model
// call and its outcome.
func TestAuditRecordsAreWrittenInTheDatabasesTime(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "audit-times"
	given := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	before := clockTimestamp(t, pool)
	calls := postgres.NewCalls(pool, tenant)
	rec := record("time-1")
	rec.At = given
	require.NoError(t, calls.Begin(t.Context(), rec))
	require.NoError(t, calls.End(t.Context(), &app.Outcome{ID: "time-1", Status: app.StatusOK, Output: "on", At: given}))
	require.NoError(t, postgres.NewHides(pool, tenant).Hide(t.Context(), &app.Hide{
		Endpoint: "https://flags.internal/mcp", Server: "flags", Tool: "get_flag", PinnedHash: "sha256:pin",
		Registration: "sha256:reg", Reason: app.ReasonChanged, Observed: "sha256:other", At: given,
	}))
	models := postgres.NewModelCalls(pool, tenant)
	require.NoError(t, models.Begin(t.Context(), modelRecord("time-m1", "run-1", given)))
	require.NoError(t, models.End(t.Context(), modelOutcome("time-m1", 1, given)))
	after := clockTimestamp(t, pool)

	for name, query := range map[string]string{
		"a tool call":        "SELECT started_at FROM tool_calls WHERE tenant_id = $1",
		"its outcome":        "SELECT ended_at FROM tool_call_outcomes WHERE tenant_id = $1",
		"a hide":             "SELECT hidden_at FROM tool_hides WHERE tenant_id = $1",
		"a model call":       "SELECT started_at FROM model_calls WHERE tenant_id = $1",
		"a model call's end": "SELECT ended_at FROM model_call_outcomes WHERE tenant_id = $1",
	} {
		var stored time.Time
		require.NoError(t, pool.QueryRow(t.Context(), query, tenant).Scan(&stored), name)

		assert.False(t, stored.Before(before), "%s at %s, before %s", name, stored, before)
		assert.False(t, stored.After(after), "%s at %s, after %s", name, stored, after)
	}
}

// toolCall is a call as the count of failed calls reads it: whose it is, of
// which tool, in which round, and how it ended. One that is open has no
// outcome; one with a cause is a platform read made for a write call; one
// elsewhere is of another run.
type toolCall struct {
	agent, server, tool, cause string
	round                      int
	status                     app.Status
	failure                    app.Failure
	open, elsewhere            bool
}

// A round's failed tool calls are the ones it cannot trust (Decision 20):
// each that failed on the server's side, the platform's or the tool's,
// unless a later call of the same tool by the same agent in the round
// succeeded, and each still without an outcome, which nothing recovers. An
// agent's own failure, a call cut short and a platform read, which has no
// round, never count. Each row is a round of its own.
func TestFailedToolCallsCountWhatTheRoundCannotTrust(t *testing.T) {
	calls := postgres.NewCalls(pgtest.Pool(t), "calls-failed")
	const alpha, beta = "flag-investigator", "flag-reviewer"
	read := func(agent, tool string, status app.Status, failure app.Failure) toolCall {
		return toolCall{agent: agent, server: "flags", tool: tool, round: 1, status: status, failure: failure}
	}
	boardCall := func(tool string, status app.Status, failure app.Failure) toolCall {
		return toolCall{agent: alpha, tool: tool, round: 1, status: status, failure: failure}
	}
	ok, recorded, readError, refused := app.StatusOK, app.StatusRecorded, app.StatusReadError, app.StatusRefused
	for i, c := range []struct {
		name  string
		calls []toolCall
		round int // the round counted
		want  int
	}{
		{"a read's server failure, not recovered", []toolCall{read(alpha, "get_flag", readError, app.FailureServer)}, 1, 1},
		{"a tool failure, then the same agent's successful read of the same tool", []toolCall{
			read(alpha, "get_flag", readError, app.FailureTool), read(alpha, "get_flag", ok, ""),
		}, 1, 0},
		{"a tool failure, then another agent's success", []toolCall{
			read(alpha, "get_flag", readError, app.FailureTool), read(beta, "get_flag", ok, ""),
		}, 1, 1},
		{"a tool failure, then the same agent's success of another tool", []toolCall{
			read(alpha, "get_flag", readError, app.FailureTool), read(alpha, "list_flags", ok, ""),
		}, 1, 1},
		{"a success, then a tool failure of the same tool", []toolCall{
			read(alpha, "get_flag", ok, ""), read(alpha, "get_flag", readError, app.FailureTool),
		}, 1, 1},
		{"a tool failure, then the same failure again", []toolCall{
			read(alpha, "get_flag", readError, app.FailureTool), read(alpha, "get_flag", readError, app.FailureTool),
		}, 1, 2},
		{"a tool failure, then the same agent's success of a tool of the same name on another server", []toolCall{
			read(alpha, "query", readError, app.FailureTool), {agent: alpha, server: "logs", tool: "query", round: 1, status: ok},
		}, 1, 1},
		{"a tool failure, then the same agent's success of the same tool in the next round", []toolCall{
			read(alpha, "get_flag", readError, app.FailureTool), {agent: alpha, server: "flags", tool: "get_flag", round: 2, status: ok},
		}, 1, 1},
		{"a tool failure, then the same agent's success of the same tool in another run", []toolCall{
			read(alpha, "get_flag", readError, app.FailureTool), {agent: alpha, server: "flags", tool: "get_flag", round: 1, status: ok, elsewhere: true},
		}, 1, 1},
		{"an agent failure", []toolCall{read(alpha, "get_flag", refused, app.FailureAgent)}, 1, 0},
		{"a cut call", []toolCall{read(alpha, "get_flag", readError, "")}, 1, 0},
		{"a tool call without an outcome", []toolCall{{agent: alpha, server: "flags", tool: "get_flag", round: 1, open: true}}, 1, 1},
		{"a tool call without an outcome, then the same agent's success", []toolCall{
			{agent: alpha, server: "flags", tool: "get_flag", round: 1, open: true}, read(alpha, "get_flag", ok, ""),
		}, 1, 1},
		{"a failure in an earlier round", []toolCall{read(alpha, "get_flag", readError, app.FailureServer)}, 2, 0},
		{"a listing's failure", []toolCall{{agent: alpha, server: "logs", tool: "tools/list", round: 1, status: readError, failure: app.FailureServer}}, 1, 1},
		{"a write's platform failure", []toolCall{read(alpha, "set_flag", readError, app.FailurePlatform)}, 1, 1},
		{"a failed end_turn, then a successful one by the same agent", []toolCall{
			boardCall("end_turn", readError, app.FailurePlatform), boardCall("end_turn", ok, ""),
		}, 1, 0},
		{"a failed read_board, then a successful end_turn", []toolCall{
			boardCall("read_board", readError, app.FailurePlatform), boardCall("end_turn", ok, ""),
		}, 1, 1},
		{"a write's server failure, then the same write by the same agent recorded", []toolCall{
			read(alpha, "set_flag", refused, app.FailureServer), read(alpha, "set_flag", recorded, ""),
		}, 1, 0},
		{"a platform read's server failure, which has no round: its write's own row counts it", []toolCall{
			{server: "flags", tool: "list_flags", cause: "c-write", status: readError, failure: app.FailureServer},
		}, 1, 0},
		{"a platform read's server failure, recorded in a round, which no agent made", []toolCall{
			{server: "flags", tool: "list_flags", cause: "c-write", round: 1, status: readError, failure: app.FailureServer},
		}, 1, 0},
	} {
		run := fmt.Sprintf("run-failed-%d", i)
		for j, tc := range c.calls {
			id, in := fmt.Sprintf("%s-%d", run, j), board.RunID(run)
			if tc.elsewhere {
				in += "-elsewhere"
			}
			require.NoError(t, calls.Begin(t.Context(), &app.CallRecord{
				ID: id, Run: in, Agent: tc.agent, Server: tc.server, Tool: tc.tool,
				Args: json.RawMessage(`{}`), Round: tc.round, Cause: tc.cause, At: at,
			}), c.name)
			if !tc.open {
				require.NoError(t, calls.End(t.Context(), &app.Outcome{ID: id, Status: tc.status, Failure: tc.failure, Output: "why", At: at}), c.name)
			}
		}

		got, err := calls.Failed(t.Context(), run, c.round)

		require.NoError(t, err, c.name)
		assert.Equal(t, c.want, got, c.name)
	}
}
