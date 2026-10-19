//go:build integration

package postgres_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var (
	_ app.Records = (*postgres.Records)(nil)
	_ app.Runs    = (*postgres.Runs)(nil)
)

// opened is an item's first record.
func opened(id item.ID, rec string) item.Draft {
	return item.Draft{ID: rec, Item: id, Incident: "inc-" + item.IncidentID(id), Body: item.Body{V: item.Version, Opened: &item.Opened{
		Type: "incident", Title: "checkout fails", Source: "person", Arm: item.ArmVisible, How: item.NoWindow,
	}}}
}

// started is the record that starts an item.
func started(id item.ID, rec string) item.Draft {
	return item.Draft{ID: rec, Item: id, Incident: "inc-" + item.IncidentID(id), Body: item.Body{V: item.Version, Started: &item.Started{
		Commit: "c1", Digest: "d1", Images: map[string]string{"thinloop": "sha256:aa"},
	}}}
}

// runCreated is the record that creates run for an item.
func runCreated(id item.ID, rec string, run board.RunID) item.Draft {
	return item.Draft{ID: rec, Item: id, Incident: "inc-" + item.IncidentID(id), Run: run, Body: item.Body{V: item.Version, RunCreated: &item.RunCreated{
		Class: item.ClassPrimary, Mode: item.ModeVisible, Settings: board.Settings{Convergence: board.DefaultConvergence, Limits: board.Limits{EntriesPerRound: 3}},
		Budget: item.Budget{WallClock: 15 * time.Minute, Tokens: 2_000_000, Rounds: 6}, RoundLimit: 150 * time.Second, Agents: []string{"release-investigator"},
	}}}
}

func appendDrafts(t *testing.T, store *postgres.Records, id item.ID, drafts ...item.Draft) []item.Record {
	t.Helper()
	committed, err := store.Append(t.Context(), id, func([]item.Record) ([]item.Draft, error) { return drafts, nil })
	require.NoError(t, err)
	return committed
}

// An item's records come back as they were committed, numbered from 1 in
// commit order, with the database's time of their commit.
func TestRecordsRoundTrip(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-a")

	committed := appendDrafts(t, store, "item-1", opened("item-1", "r1"), started("item-1", "r2"))
	more := appendDrafts(t, store, "item-1", runCreated("item-1", "r3", "run-1"))
	loaded, err := store.Load(t.Context(), "item-1")

	require.NoError(t, err)
	require.Len(t, loaded, 3)
	assert.Equal(t, []int64{1, 2, 3}, []int64{loaded[0].Seq, loaded[1].Seq, loaded[2].Seq})
	assert.Equal(t, committed[1].Body, loaded[1].Body)
	assert.Equal(t, more[0].Body, loaded[2].Body)
	assert.Equal(t, item.IncidentID("inc-item-1"), loaded[2].Incident)
	assert.Equal(t, board.RunID("run-1"), loaded[2].Run)
	assert.True(t, committed[0].At.Equal(loaded[0].At))
	assert.False(t, loaded[2].At.Before(loaded[1].At))
	s, err := item.Fold(loaded)
	require.NoError(t, err)
	assert.Equal(t, item.StatusInvestigating, s.Status())
}

// A decision that fails commits nothing.
func TestAppendingCommitsNothingWhenDecideFails(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-a")
	refused := errors.New("refused by the rules")

	_, err := store.Append(t.Context(), "item-2", func([]item.Record) ([]item.Draft, error) { return nil, refused })

	require.ErrorIs(t, err, refused)
	loaded, err := store.Load(t.Context(), "item-2")
	require.NoError(t, err)
	assert.Empty(t, loaded)
}

// Appends to one item are serialized: each sees every record committed
// before it.
func TestAppendsToOneItemAreSerialized(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-a")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			_, err := store.Append(t.Context(), "item-3", func(history []item.Record) ([]item.Draft, error) {
				d := opened("item-3", fmt.Sprintf("c-%d", i))
				if len(history) > 0 {
					d.Body = item.Body{V: item.Version, AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}}
				}
				return []item.Draft{d}, nil
			})
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	loaded, err := store.Load(t.Context(), "item-3")
	require.NoError(t, err)
	require.Len(t, loaded, 20)
	assert.NotNil(t, loaded[0].Body.Opened, "only the first saw no history")
	for i := range loaded {
		assert.Equal(t, int64(i+1), loaded[i].Seq)
	}
}

// The coordinator drives the items that do not await their close.
func TestActiveItemsAreThoseNotAwaitingClose(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-active")
	appendDrafts(t, store, "item-a", opened("item-a", "a1"))
	appendDrafts(t, store, "item-b", opened("item-b", "b1"), started("item-b", "b2"))
	closing := opened("item-c", "c2")
	closing.Body = item.Body{V: item.Version, AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}}
	appendDrafts(t, store, "item-c", opened("item-c", "c1"), closing)

	active, err := store.Active(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []item.ID{"item-a", "item-b"}, active)
}

// Active lists the opened items not awaiting their close: not an empty item, nor one of a later version.
func TestActiveItemsAreOpenedAndNotAwaitingClose(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-active-opened")
	appendDrafts(t, store, "item-a", opened("item-a", "a1"))
	appendDrafts(t, store, "item-empty")
	appendDrafts(t, store, "item-later", item.Draft{ID: "l1", Item: "item-later", Incident: "inc-item-later", Body: item.Body{V: item.Version + 1}})

	active, err := store.Active(t.Context())
	require.NoError(t, err)
	loaded, err := store.Load(t.Context(), "item-empty")
	require.NoError(t, err)

	assert.Equal(t, []item.ID{"item-a"}, active)
	assert.Empty(t, loaded)
}

// A run's board context comes from its creation record: its settings, and
// human entries for the primary run that people see.
func TestARunsBoardContextComesFromItsCreation(t *testing.T) {
	pool := pgtest.Pool(t)
	store := postgres.NewRecords(pool, "records-runs")
	appendDrafts(t, store, "item-r", opened("item-r", "r1"), started("item-r", "r2"), runCreated("item-r", "r3", "run-r"))
	runs := postgres.NewRuns(pool, "records-runs")

	bc, err := runs.BoardContext(t.Context(), "run-r")
	require.NoError(t, err)
	_, unknown := runs.BoardContext(t.Context(), "run-404")

	assert.Equal(t, board.Context{Run: "run-r", HumanEntries: true, Settings: board.Settings{
		Convergence: board.DefaultConvergence, Limits: board.Limits{EntriesPerRound: 3},
	}}, bc)
	require.Error(t, unknown)
}

// Work records are audit records: append-only, and chained under their
// incident, so that an anchor names the incidents whose records it covers.
func TestWorkRecordsAreAppendOnlyAndChainedUnderTheirIncident(t *testing.T) {
	pool := pgtest.Pool(t)
	store := postgres.NewRecords(pool, "records-chain")
	appendDrafts(t, store, "item-x", opened("item-x", "x1"))

	for _, stmt := range []string{
		`UPDATE work_records SET kind = 'x' WHERE tenant_id = 'records-chain'`,
		`DELETE FROM work_records WHERE tenant_id = 'records-chain'`,
	} {
		_, err := pool.Exec(t.Context(), stmt)
		require.Error(t, err, stmt)
	}
	assert.Equal(t, []string{`work_records ["item-x", 1] incident:inc-item-x`}, chained(t, pool, "records-chain"))
}

// chained lists a tenant's chain as source, key and scope.
func chained(t *testing.T, pool *pgxpool.Pool, tenant string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT source || ' ' || key || ' ' || run_id FROM audit_chain WHERE tenant_id = $1 ORDER BY seq`, tenant)
	require.NoError(t, err)
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}
