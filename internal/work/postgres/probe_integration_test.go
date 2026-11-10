//go:build integration

package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var _ app.Probe = (*postgres.Probe)(nil)

// The probe gives, in one statement, each item's latest record, the run its
// latest run_created names, and that run's latest entry (design D2; R138):
// an item queued has no run, an item whose run has no entry no entry, and an
// id with no record, as another tenant's item, is absent. A probe of three
// items and an unknown id takes the pool's connection once. Fails first: a
// build failure.
func TestTheProbeGivesEachItemsLatestRecordAndItsRunsLatestEntry(t *testing.T) {
	pool := pgtest.Pool(t)
	tenant := tenantOf(t)
	records, events := postgres.NewRecords(pool, tenant), postgres.NewEventStore(pool, tenant)
	appendDrafts(t, records, "item-q", opened("item-q", "q1"))
	appendDrafts(t, records, "item-r", opened("item-r", "r1"), started("item-r", "r2"), runCreated("item-r", "r3", "run-r"))
	appendDrafts(t, records, "item-b", opened("item-b", "b1"), started("item-b", "b2"), runCreated("item-b", "b3", "run-b"))
	appendDrafts(t, records, "item-b", inIncident("item-b", "b4", "inc-other", linkedFrom("inc-item-b")))
	_, err := events.Append(t.Context(), "run-b", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{evidenceDraft("run-b", board.EntryID(tenant+"-pb-1"), "v42"), evidenceDraft("run-b", board.EntryID(tenant+"-pb-2"), "v43")}, nil
	})
	require.NoError(t, err)
	appendDrafts(t, postgres.NewRecords(pool, tenant+"-other"), "item-z", opened("item-z", "z1"))
	probe := postgres.NewProbe(pool, tenant)
	before := pool.Stat().AcquireCount()

	got, err := probe.Versions(t.Context(), []item.ID{"item-q", "item-r", "item-b", "item-z"})

	require.NoError(t, err)
	assert.Equal(t, before+1, pool.Stat().AcquireCount(), "one statement")
	assert.Equal(t, map[item.ID]app.BoardVersion{
		"item-q": {Record: app.Mark{ID: "q1", Seq: 1}},
		"item-r": {Record: app.Mark{ID: "r3", Seq: 3}, Run: "run-r"},
		"item-b": {Record: app.Mark{ID: "b4", Seq: 4}, Run: "run-b", Board: app.Mark{ID: tenant + "-pb-2", Seq: 2}},
	}, got)
	none, err := probe.Versions(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}
