//go:build integration

package postgres_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/postgres"
	"github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	workapp "github.com/jchen0426/agent-swarm/internal/work/app"
)

var _ workapp.Spend = (*postgres.ModelCalls)(nil)

const modelRequest = `{"max_tokens":1,"messages":[{"content":"hi","role":"user"}],"model":"claude-haiku-5-5"}`

// modelRecord is a model call of run that began at start, with an
// estimate of 5 weighted tokens.
func modelRecord(id, run string, start time.Time) *app.CallRecord {
	return &app.CallRecord{ID: id, Run: run, Agent: "flag-investigator", Round: 1, Class: cost.Primary,
		Model: "claude-haiku-5-5", Request: modelRequest, RequestBytes: int64(len(modelRequest)), RequestDigest: "d1", Estimate: 5, At: start}
}

func modelOutcome(id string, weighted int64, end time.Time) *app.Outcome {
	return &app.Outcome{ID: id, Status: app.StatusOK, Completion: `{"content":[{"text":"ok","type":"text"}]}`,
		Usage: cost.Usage{Input: weighted}, Spend: 100_000 * weighted, Weighted: weighted, Attempts: 1, Details: `{"id":"msg_1"}`, At: end}
}

// modelCall records a model call of run that began at start, spent
// weighted tokens and ended two minutes later.
func modelCall(t *testing.T, pool *pgxpool.Pool, tenant, id, run string, weighted int64, start time.Time) {
	t.Helper()
	calls := postgres.NewModelCalls(pool, tenant)
	require.NoError(t, calls.Begin(t.Context(), modelRecord(id, run, start)))
	require.NoError(t, calls.End(t.Context(), modelOutcome(id, weighted, start.Add(2*time.Minute))))
}

// A model call is recorded with its request before it is forwarded, then
// its outcome with its completion, the contents apart from the records. The
// same outcome recorded again changes nothing; another one is refused.
func TestAModelCallIsRecordedThenItsOutcome(t *testing.T) {
	pool := pgtest.Pool(t)
	calls := postgres.NewModelCalls(pool, "models-record")
	require.NoError(t, calls.Begin(t.Context(), modelRecord("m-1", "run-1", at)))

	require.NoError(t, calls.End(t.Context(), modelOutcome("m-1", 120, at)))
	require.NoError(t, calls.End(t.Context(), modelOutcome("m-1", 120, at)), "the same outcome again")
	changed := modelOutcome("m-1", 120, at)
	changed.Completion = `{"content":[]}`
	err := calls.End(t.Context(), changed)

	require.ErrorIs(t, err, postgres.ErrConflict)
	contents := map[string]string{}
	rows, err := pool.Query(t.Context(), `SELECT part, content FROM model_call_contents WHERE tenant_id = 'models-record' AND call_id = 'm-1'`)
	require.NoError(t, err)
	for rows.Next() {
		var part, content string
		require.NoError(t, rows.Scan(&part, &content))
		contents[part] = content
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, map[string]string{"request": modelRequest, "completion": `{"content":[{"text":"ok","type":"text"}]}`}, contents)
	var weighted, spend int64
	var details string
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT weighted, spend, details FROM model_call_outcomes WHERE tenant_id = 'models-record'`).Scan(&weighted, &spend, &details))
	assert.Equal(t, int64(120), weighted)
	assert.Equal(t, int64(12_000_000), spend)
	assert.JSONEq(t, `{"id":"msg_1"}`, details)
}

// A run's spend is its own calls': what each cost once its outcome is
// recorded, and its estimate until then. The day's is the tenant's calls'
// that began since the day began, counted the same way.
func TestSpendIsCountedPerRunAndSinceATime(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "models-spend"
	calls := postgres.NewModelCalls(pool, tenant)
	day := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	modelCall(t, pool, tenant, "s-1", "run-a", 100, day.Add(-time.Minute))
	modelCall(t, pool, tenant, "s-2", "run-a", 200, day.Add(time.Hour))
	modelCall(t, pool, tenant, "s-3", "run-b", 400, day.Add(2*time.Hour))
	modelCall(t, pool, "models-other", "s-4", "run-a", 800, day.Add(time.Hour))
	inFlight := modelRecord("s-5", "run-a", day.Add(3*time.Hour))
	inFlight.Estimate = 1600
	require.NoError(t, calls.Begin(t.Context(), inFlight))

	runA, err := calls.Spent(t.Context(), "run-a")
	require.NoError(t, err)
	today, err := calls.SpentSince(t.Context(), day)
	require.NoError(t, err)
	none, err := calls.Spent(t.Context(), "run-z")
	require.NoError(t, err)

	assert.Equal(t, int64(100+200+1600), runA)
	assert.Equal(t, int64(200+400+1600), today, "the call that began yesterday counts yesterday")
	assert.Equal(t, int64(0), none)
}

// Model call records never change: only the retention job deletes them.
func TestModelCallRecordsNeverChange(t *testing.T) {
	pool := pgtest.Pool(t)
	modelCall(t, pool, "models-guard", "g-1", "run-1", 10, at)

	for _, stmt := range []string{
		`UPDATE model_calls SET request_digest = '' WHERE tenant_id = 'models-guard'`,
		`DELETE FROM model_calls WHERE tenant_id = 'models-guard'`,
		`UPDATE model_call_contents SET content = '' WHERE tenant_id = 'models-guard'`,
		`DELETE FROM model_call_contents WHERE tenant_id = 'models-guard'`,
		`UPDATE model_call_outcomes SET weighted = 0 WHERE tenant_id = 'models-guard'`,
		`DELETE FROM model_call_outcomes WHERE tenant_id = 'models-guard'`,
		`TRUNCATE model_call_contents`,
	} {
		_, err := pool.Exec(t.Context(), stmt)

		require.ErrorContains(t, err, "is append-only", stmt)
	}
}

// Model calls are chained as they are recorded, their contents and
// outcomes under their call's run, and verification renders each row to
// its record's digest.
func TestModelCallsAreChained(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "models-chain"
	modelCall(t, pool, tenant, "c-1", "run-m", 42, at)
	calls := postgres.NewModelCalls(pool, tenant)
	refused := modelRecord("c-2", "run-m", at)
	refused.Model, refused.Request, refused.RequestDigest, refused.Estimate = "", "", "", 0
	require.NoError(t, calls.Begin(t.Context(), refused))
	require.NoError(t, calls.End(t.Context(), &app.Outcome{ID: "c-2", Status: app.StatusRefused, Reason: "the field \"metadata\" is not allowed", At: at}))
	c := newChain(pool, tenant)

	links := readChain(t, c)

	var got []string
	for i := range links {
		got = append(got, fmt.Sprintf("%s %s %s", links[i].Source, links[i].Run, links[i].Key))
	}
	assert.Equal(t, []string{`model_calls run-m ["c-1"]`, `model_call_contents run-m ["c-1", "request"]`,
		`model_call_outcomes run-m ["c-1"]`, `model_call_contents run-m ["c-1", "completion"]`,
		`model_calls run-m ["c-2"]`, `model_call_outcomes run-m ["c-2"]`}, got)
	assert.Equal(t, 6, checkRows(t, c))
}

// The coordinator reads how many of a run's calls have no outcome yet, and
// the calls it notices on the run's board: those refused, failed or whose
// outcome is unknown, in the order they began.
func TestARunsCallsInFlightAndFailedAreRead(t *testing.T) {
	pool := pgtest.Pool(t)
	calls := postgres.NewModelCalls(pool, "models-failed")
	ended := func(id, run string, status app.Status, start time.Time) {
		require.NoError(t, calls.Begin(t.Context(), modelRecord(id, run, start)))
		o := modelOutcome(id, 1, start.Add(time.Minute))
		o.Status = status
		require.NoError(t, calls.End(t.Context(), o))
	}
	ended("m-1", "run-1", app.StatusOK, at)
	ended("m-2", "run-1", app.StatusRefused, at.Add(time.Second))
	require.NoError(t, calls.Begin(t.Context(), modelRecord("m-3", "run-1", at.Add(2*time.Second))))
	ended("m-4", "run-1", app.StatusError, at.Add(3*time.Second))
	ended("m-5", "run-1", app.StatusUnknown, at.Add(4*time.Second))
	ended("m-6", "run-2", app.StatusRefused, at.Add(5*time.Second))

	inFlight, err := calls.InFlight(t.Context(), "run-1")
	require.NoError(t, err)
	failed, err := calls.Failed(t.Context(), "run-1")
	require.NoError(t, err)

	assert.Equal(t, 1, inFlight)
	assert.Equal(t, []workapp.ModelCall{
		{ID: "m-2", Agent: "flag-investigator", Status: "refused"},
		{ID: "m-4", Agent: "flag-investigator", Status: "error"},
		{ID: "m-5", Agent: "flag-investigator", Status: "unknown"},
	}, failed)
}
