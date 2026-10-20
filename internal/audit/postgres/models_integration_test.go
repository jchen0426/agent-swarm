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
	"github.com/jchen0426/agent-swarm/internal/holding"
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
// admitted today, counted the same way.
func TestSpendIsCountedPerRunAndForTheDay(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "models-spend"
	calls := postgres.NewModelCalls(pool, tenant)
	modelCall(t, pool, tenant, "s-2", "run-a", 200, at)
	modelCall(t, pool, tenant, "s-3", "run-b", 400, at)
	modelCall(t, pool, "models-other", "s-4", "run-a", 800, at)
	inFlight := modelRecord("s-5", "run-a", at)
	inFlight.Estimate = 1600
	require.NoError(t, calls.Begin(t.Context(), inFlight))

	runA, err := calls.Spent(t.Context(), "run-a")
	require.NoError(t, err)
	today, err := calls.SpentToday(t.Context())
	require.NoError(t, err)
	none, err := calls.Spent(t.Context(), "run-z")
	require.NoError(t, err)

	assert.Equal(t, int64(200+1600), runA)
	assert.Equal(t, int64(200+400+1600), today, "the tenant's calls alone")
	assert.Equal(t, int64(0), none)
}

// The day's spend counts from the database's UTC midnight (Decision 8): of two
// calls one second either side of it, only the later counts.
func TestSpentTodayCountsFromTheDatabasesUTCMidnight(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "models-midnight"
	for _, c := range []struct {
		id       string
		estimate int64
		offset   time.Duration
	}{{"before-midnight", 100, -time.Second}, {"after-midnight", 7, time.Second}} {
		_, err := pool.Exec(t.Context(), `
INSERT INTO model_calls (tenant_id, call_id, run_id, agent, round, class, model, request_bytes, request_digest, estimate, started_at)
VALUES ($1, $2, 'run-1', 'flag-investigator', 1, 'primary', 'claude-haiku-5-5', 0, '', $3, date_trunc('day', clock_timestamp(), 'UTC') + $4::interval)`,
			tenant, c.id, c.estimate, c.offset)
		require.NoError(t, err, c.id)
	}

	today, err := postgres.NewModelCalls(pool, tenant).SpentToday(t.Context())

	require.NoError(t, err)
	assert.Equal(t, int64(7), today)
}

// Model calls, their contents and their outcomes are written only under the
// holding the write acts for: one under an epoch whose holding is over is
// refused, wrapping holding.ErrLeaseLost, and leaves no row, of the call, its
// request, its outcome or its completion; one under the current epoch lands.
func TestModelCallsTheirContentsAndOutcomesAreWrittenOnlyUnderTheHolding(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "models-guarded"
	lease, stale, current := heldLease(t, pool, tenant)
	calls := postgres.NewModelCalls(pool, tenant).Guarded(lease)

	require.ErrorIs(t, calls.Begin(stale, modelRecord("g-1", "run-1", at)), holding.ErrLeaseLost)
	assert.Zero(t, rows(t, pool, "model_calls", tenant), "no call")
	assert.Zero(t, rows(t, pool, "model_call_contents", tenant), "nor its request")
	require.NoError(t, calls.Begin(current, modelRecord("g-1", "run-1", at)))
	require.ErrorIs(t, calls.End(stale, modelOutcome("g-1", 7, at)), holding.ErrLeaseLost)
	assert.Zero(t, rows(t, pool, "model_call_outcomes", tenant), "no outcome")
	assert.Equal(t, 1, rows(t, pool, "model_call_contents", tenant), "its request alone, no completion")
	require.NoError(t, calls.End(current, modelOutcome("g-1", 7, at)))

	assert.Equal(t, 1, rows(t, pool, "model_calls", tenant))
	assert.Equal(t, 1, rows(t, pool, "model_call_outcomes", tenant))
	assert.Equal(t, 2, rows(t, pool, "model_call_contents", tenant), "its request and its completion")
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

// The calls a sweep gives an outcome are the tenant's without one that began
// before the given time, in the database's time, in the order they began.
// Each comes with what the estimate of a lost call reads: its model, its
// stored estimate, its request's size, and the max_tokens of its stored
// request, or 0 where the record holds none it can read: a refused request's
// record, cut, or no request at all.
func TestUnsettledCallsAreThoseWithoutAnOutcomeThatBeganBefore(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "models-unsettled"
	calls := postgres.NewModelCalls(pool, tenant)
	const forwarded = `{"max_tokens":2048,"messages":[{"content":"hi","role":"user"}],"model":"claude-haiku-5-5"}`
	left := modelRecord("left", "run-1", at)
	left.Request, left.RequestBytes, left.Estimate = forwarded, 3000, 900
	require.NoError(t, calls.Begin(t.Context(), left))
	modelCall(t, pool, tenant, "ended", "run-1", 7, at)
	cut := modelRecord("cut", "run-2", at)
	cut.Request, cut.RequestBytes, cut.Estimate = `{"max_tokens":1024,"messages":[{"content":"a long refused requ…`, 70_000, 0
	require.NoError(t, calls.Begin(t.Context(), cut))
	unread := modelRecord("an-unread-one", "run-2", at)
	unread.Model, unread.Request, unread.RequestDigest, unread.RequestBytes, unread.Estimate = "", "", "", 12, 0
	require.NoError(t, calls.Begin(t.Context(), unread))
	require.NoError(t, postgres.NewModelCalls(pool, "models-unsettled-other").Begin(t.Context(), modelRecord("another-tenants", "run-1", at)))
	before := clockTimestamp(t, pool)
	require.NoError(t, calls.Begin(t.Context(), modelRecord("later", "run-1", at)))

	unsettled, err := calls.Unsettled(t.Context(), before)

	require.NoError(t, err)
	assert.Equal(t, []app.Unsettled{
		{ID: "left", Model: "claude-haiku-5-5", Estimate: 900, RequestBytes: 3000, MaxTokens: 2048},
		{ID: "cut", Model: "claude-haiku-5-5", Estimate: 0, RequestBytes: 70_000, MaxTokens: 0},
		{ID: "an-unread-one", Model: "", Estimate: 0, RequestBytes: 12, MaxTokens: 0},
	}, unsettled, "not the call that ended, another tenant's, or the one that began later")
}

// The coordinator reads how many of a round's calls have no outcome yet, and
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

	inFlight, err := calls.InFlight(t.Context(), "run-1", 1)
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

// A round's calls in flight are its own (Decision 9): the model calls of the
// run recorded in the round, without an outcome. A call of an earlier round
// still in flight, the round's call that ended, another run's call and
// another tenant's do not count.
func TestInFlightCountsTheRoundsCallsOnly(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "models-in-flight"
	calls := postgres.NewModelCalls(pool, tenant)
	begin := func(calls *postgres.ModelCalls, id, run string, round int) {
		rec := modelRecord(id, run, at)
		rec.Round = round
		require.NoError(t, calls.Begin(t.Context(), rec), id)
	}
	begin(calls, "hung-in-round-1", "run-1", 1)
	begin(calls, "open-in-round-2", "run-1", 2)
	begin(calls, "also-open-in-round-2", "run-1", 2)
	begin(calls, "ended-in-round-2", "run-1", 2)
	require.NoError(t, calls.End(t.Context(), modelOutcome("ended-in-round-2", 3, at)))
	begin(calls, "another-runs", "run-2", 2)
	begin(postgres.NewModelCalls(pool, "models-in-flight-other"), "another-tenants", "run-1", 2)

	counts := map[int]int{}
	for _, round := range []int{1, 2, 3} {
		n, err := calls.InFlight(t.Context(), "run-1", round)
		require.NoError(t, err, round)
		counts[round] = n
	}

	assert.Equal(t, map[int]int{1: 1, 2: 2, 3: 0}, counts)
}

// The stores wrap the sentinels the gateways share (R18), so that a gateway
// can tell why a write was refused without importing the stores: an outcome
// of a model call that already has another, as when a sweep settled it, is
// refused wrapping llmapp's ErrConflict as well as the store's own; a guarded
// write under an epoch whose holding is over is refused wrapping
// holding.ErrLeaseLost.
func TestTheStoresWrapTheSharedSentinels(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "models-sentinels"
	lease, stale, current := heldLease(t, pool, tenant)
	calls := postgres.NewModelCalls(pool, tenant).Guarded(lease)
	require.NoError(t, calls.Begin(current, modelRecord("s-1", "run-1", at)))
	swept := modelOutcome("s-1", 7, at)
	swept.Status, swept.Reason, swept.Completion = app.StatusUnknown, "an earlier holding ended before the call's outcome was recorded", ""
	require.NoError(t, calls.End(current, swept))

	conflict := calls.End(current, modelOutcome("s-1", 7, at))
	lost := calls.End(stale, modelOutcome("s-1", 7, at))

	require.ErrorIs(t, conflict, app.ErrConflict)
	require.ErrorIs(t, conflict, postgres.ErrConflict, "the store's own sentinel still matches")
	require.ErrorIs(t, lost, holding.ErrLeaseLost)
}
