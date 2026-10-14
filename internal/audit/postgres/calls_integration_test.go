//go:build integration

package postgres_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/audit/postgres"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	workapp "github.com/jchen0426/agent-swarm/internal/work/app"
)

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

	_, err = calls.ReadCall(t.Context(), "c-unknown")
	require.ErrorIs(t, err, workapp.ErrCallNotFound)
}

func TestARefusedOrFailedCallCannotBeCited(t *testing.T) {
	calls := postgres.NewCalls(pgtest.Pool(t), "t1")
	for id, status := range map[string]app.Status{"c-2": app.StatusRefused, "c-3": app.StatusReadError} {
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
	got[0].At = got[0].At.UTC()
	assert.Equal(t, *h, got[0])
}
