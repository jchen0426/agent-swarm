//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var (
	_ app.Inbound       = (*postgres.Records)(nil)
	_ app.InboundTitles = (*postgres.Records)(nil)
	_ app.Records       = (*postgres.Records)(nil)
)

// The inbound titles are read by their key, the tenant's source and event
// id (migration 0012's primary key): a row put, and one that committed with
// an item's records; another source's row of the same id, another tenant's
// row, and an id with no row are absent.
func TestTheInboundTitlesAreReadByTheirKey(t *testing.T) {
	pool := pgtest.Pool(t)
	s := postgres.NewRecords(pool, tenantOf(t))
	put(t, s, &app.InboundEvent{Source: "alerting", EventID: "e1", Outcome: app.InboundRefused, Title: "checkout fails"})
	appendInboundRow(t, s, &app.InboundEvent{
		Source: "alerting", EventID: "e2", InstanceID: "i", Status: item.Firing, OccurredAt: epoch0, Item: "x", Applied: true,
		Outcome: app.InboundAppended, Title: "disk full",
	})
	put(t, s, &app.InboundEvent{Source: "paging", EventID: "e1", Outcome: app.InboundRefused, Title: "paging's e1"})
	put(t, s, &app.InboundEvent{Source: "paging", EventID: "e3", Outcome: app.InboundRefused, Title: "paging's e3"})
	put(t, postgres.NewRecords(pool, tenantOf(t)), &app.InboundEvent{Source: "alerting", EventID: "e4", Outcome: app.InboundRefused, Title: "another tenant's"})

	titles, err := s.Titles(t.Context(), "alerting", []string{"e1", "e2", "e3", "e4", "e9"})

	require.NoError(t, err)
	assert.Equal(t, map[string]string{"e1": "checkout fails", "e2": "disk full"}, titles)
	none, err := s.Titles(t.Context(), "alerting", nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

var epoch0 = time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)

func put(t *testing.T, s *postgres.Records, e *app.InboundEvent) {
	t.Helper()
	require.NoError(t, s.Put(t.Context(), e))
}

// appendInboundRow commits a row of e.Item, with no record, as AppendInbound does.
func appendInboundRow(t *testing.T, s *postgres.Records, e *app.InboundEvent) {
	t.Helper()
	_, err := s.AppendInbound(t.Context(), e.Item, func([]item.Record) ([]item.Draft, *app.InboundEvent, error) {
		return nil, e, nil
	})
	require.NoError(t, err)
}

// An inbound row is append-only: an UPDATE, a DELETE and a TRUNCATE are
// refused, and the retention job's DELETE under its setting succeeds.
func TestTheInboundEventsAreAppendOnly(t *testing.T) {
	pool := pgtest.Pool(t)
	tenant := tenantOf(t)
	put(t, postgres.NewRecords(pool, tenant), &app.InboundEvent{Source: "alerting", EventID: "e1", Outcome: app.InboundRefused, Reason: "r"})

	for _, refused := range []struct {
		sql  string
		args []any
	}{
		{"UPDATE inbound_events SET reason = 'x' WHERE tenant_id = $1", []any{tenant}},
		{"DELETE FROM inbound_events WHERE tenant_id = $1", []any{tenant}},
		{"TRUNCATE inbound_events", nil},
	} {
		_, err := pool.Exec(t.Context(), refused.sql, refused.args...)
		require.ErrorContains(t, err, "inbound_events is append-only", refused.sql)
	}

	tx, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(t.Context()) }()
	_, err = tx.Exec(t.Context(), "SET LOCAL agentswarm.retention = 'on'")
	require.NoError(t, err)
	tag, err := tx.Exec(t.Context(), "DELETE FROM inbound_events WHERE tenant_id = $1", tenant)
	require.NoError(t, err)
	assert.Equal(t, int64(1), tag.RowsAffected())
}

// An event is seen by its source and id; a second Put of the same key fails
// with ErrSeen and the first row stands.
func TestAnInboundEventIsSeenOnceBySourceAndID(t *testing.T) {
	pool := pgtest.Pool(t)
	tenant := tenantOf(t)
	s := postgres.NewRecords(pool, tenant)
	put(t, s, &app.InboundEvent{Source: "alerting", EventID: "e1", Outcome: app.InboundRefused, Reason: "first"})

	seen, err := s.Seen(t.Context(), "alerting", "e1")
	require.NoError(t, err)
	assert.True(t, seen)
	seen, err = s.Seen(t.Context(), "paging", "e1")
	require.NoError(t, err)
	assert.False(t, seen)

	err = s.Put(t.Context(), &app.InboundEvent{Source: "alerting", EventID: "e1", Outcome: app.InboundUnroutable, Reason: "second"})
	require.ErrorIs(t, err, app.ErrSeen)
	var reason string
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT reason FROM inbound_events WHERE tenant_id = $1 AND source = 'alerting' AND event_id = 'e1'", tenant).Scan(&reason))
	assert.Equal(t, "first", reason)
}

// An instance's last status is its latest applied row, resolved over firing at
// equal time; an unknown instance has none.
func TestAnInstancesLastStatusIsItsLatestAppliedRow(t *testing.T) {
	s := postgres.NewRecords(pgtest.Pool(t), tenantOf(t))
	t0, t1, t2, t3 := epoch0, epoch0.Add(time.Minute), epoch0.Add(2*time.Minute), epoch0.Add(3*time.Minute)
	appendInboundRow(t, s, &app.InboundEvent{Source: "a", EventID: "1", InstanceID: "i", Status: item.Firing, OccurredAt: t1, Item: "x", Applied: true, Outcome: app.InboundAppended})
	put(t, s, &app.InboundEvent{Source: "a", EventID: "2", InstanceID: "i", Status: item.Firing, OccurredAt: t0, Outcome: app.InboundLate})
	put(t, s, &app.InboundEvent{Source: "a", EventID: "3", InstanceID: "i", Status: item.Resolved, OccurredAt: t2, Applied: true, Outcome: app.InboundResolvedUnmatched})
	put(t, s, &app.InboundEvent{Source: "a", EventID: "4", InstanceID: "i", Outcome: app.InboundRefused})

	status, at, ok, err := s.LastStatus(t.Context(), "a", "i")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, item.Resolved, status)
	assert.True(t, t2.Equal(at))

	appendInboundRow(t, s, &app.InboundEvent{Source: "a", EventID: "5", InstanceID: "j", Status: item.Firing, OccurredAt: t3, Item: "y", Applied: true, Outcome: app.InboundAppended})
	appendInboundRow(t, s, &app.InboundEvent{Source: "a", EventID: "6", InstanceID: "j", Status: item.Resolved, OccurredAt: t3, Item: "y", Applied: true, Outcome: app.InboundAppended})
	status, at, ok, err = s.LastStatus(t.Context(), "a", "j")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, item.Resolved, status)
	assert.True(t, t3.Equal(at))

	_, _, ok, err = s.LastStatus(t.Context(), "a", "nobody")
	require.NoError(t, err)
	assert.False(t, ok)
}

// The last item a key took is the item of its latest row that has one.
func TestTheLastItemAKeyTookIsTheLatestRowWithAnItem(t *testing.T) {
	s := postgres.NewRecords(pgtest.Pool(t), tenantOf(t))
	appendInboundRow(t, s, &app.InboundEvent{Source: "a", EventID: "1", DedupKey: "k", Status: item.Firing, OccurredAt: epoch0, Item: "A", Applied: true, Outcome: app.InboundOpened})
	appendInboundRow(t, s, &app.InboundEvent{Source: "a", EventID: "2", DedupKey: "k", Status: item.Firing, OccurredAt: epoch0, Item: "B", Applied: true, Outcome: app.InboundAppended})
	put(t, s, &app.InboundEvent{Source: "a", EventID: "3", DedupKey: "k", Outcome: app.InboundUnroutable})

	id, ok, err := s.LastItem(t.Context(), "a", "k")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, item.ID("B"), id)
	_, ok, err = s.LastItem(t.Context(), "a", "unknown")
	require.NoError(t, err)
	assert.False(t, ok)
}

func recordCount(t *testing.T, s *postgres.Records, id item.ID) int {
	t.Helper()
	records, err := s.Load(t.Context(), id)
	require.NoError(t, err)
	return len(records)
}

// AppendInbound commits the records and the row together, or neither.
func TestAppendInboundCommitsTheRecordsAndTheRowTogether(t *testing.T) {
	s := postgres.NewRecords(pgtest.Pool(t), tenantOf(t))
	row := func(eventID string) *app.InboundEvent {
		return &app.InboundEvent{Source: "a", EventID: eventID, DedupKey: "k", Status: item.Firing, OccurredAt: epoch0, Applied: true, Outcome: app.InboundOpened}
	}

	committed, err := s.AppendInbound(t.Context(), "x", func([]item.Record) ([]item.Draft, *app.InboundEvent, error) {
		return []item.Draft{opened("x", "r1"), started("x", "r2")}, row("e1"), nil
	})
	require.NoError(t, err)
	assert.Len(t, committed, 2)
	id, ok, err := s.LastItem(t.Context(), "a", "k")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, item.ID("x"), id, "the row's item is the item")

	_, err = s.AppendInbound(t.Context(), "y", func([]item.Record) ([]item.Draft, *app.InboundEvent, error) {
		return []item.Draft{opened("y", "r3")}, row("e2"), assert.AnError
	})
	require.ErrorIs(t, err, assert.AnError)
	assert.Zero(t, recordCount(t, s, "y"))
	seen, err := s.Seen(t.Context(), "a", "e2")
	require.NoError(t, err)
	assert.False(t, seen, "a decide that fails commits no row")

	_, err = s.AppendInbound(t.Context(), "z", func([]item.Record) ([]item.Draft, *app.InboundEvent, error) {
		return []item.Draft{opened("z", "r4")}, row("e1"), nil
	})
	require.ErrorIs(t, err, app.ErrSeen)
	assert.Zero(t, recordCount(t, s, "z"), "a held key commits no record")
}

// Every insert is guarded by the holding: under an earlier epoch, Put and
// AppendInbound fail with ErrLeaseLost and write nothing; without an epoch,
// with ErrNoEpoch.
func TestEveryInboundInsertIsGuardedByTheHolding(t *testing.T) {
	pool := pgtest.Pool(t)
	tenant := tenantOf(t)
	lease := postgres.NewLease(pool, tenant, "platform", "a", time.Minute, takeWait)
	first := take(t, lease)
	require.NoError(t, lease.Release(t.Context()))
	second := take(t, lease)
	s := postgres.NewRecords(pool, tenant).Guarded(lease)
	row := &app.InboundEvent{Source: "a", EventID: "e1", DedupKey: "k", Status: item.Firing, OccurredAt: epoch0, Applied: true, Outcome: app.InboundOpened}
	decide := func([]item.Record) ([]item.Draft, *app.InboundEvent, error) {
		return []item.Draft{opened("x", "r1")}, row, nil
	}

	earlierPut := s.Put(under(t, first), &app.InboundEvent{Source: "a", EventID: "p1", Outcome: app.InboundRefused})
	_, earlierAppend := s.AppendInbound(under(t, first), "x", decide)
	noEpochPut := s.Put(t.Context(), &app.InboundEvent{Source: "a", EventID: "p1", Outcome: app.InboundRefused})
	_, noEpochAppend := s.AppendInbound(t.Context(), "x", decide)

	require.ErrorIs(t, earlierPut, holding.ErrLeaseLost)
	require.ErrorIs(t, earlierAppend, holding.ErrLeaseLost)
	require.ErrorIs(t, noEpochPut, holding.ErrNoEpoch)
	require.ErrorIs(t, noEpochAppend, holding.ErrNoEpoch)
	require.NotErrorIs(t, noEpochPut, holding.ErrLeaseLost)
	var n int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM inbound_events WHERE tenant_id = $1", tenant).Scan(&n))
	assert.Zero(t, n, "nothing was written")
	assert.Zero(t, recordCount(t, s, "x"))

	require.NoError(t, s.Put(under(t, second), &app.InboundEvent{Source: "a", EventID: "p1", Outcome: app.InboundRefused}), "a write under the current epoch lands")
}

// A stored row keeps every column: empty strings as ”, no status as ”, no
// time as NULL, no item as NULL.
func TestAStoredInboundEventKeepsEveryColumn(t *testing.T) {
	pool := pgtest.Pool(t)
	tenant := tenantOf(t)
	s := postgres.NewRecords(pool, tenant)
	_, err := s.AppendInbound(t.Context(), "x", func([]item.Record) ([]item.Draft, *app.InboundEvent, error) {
		return []item.Draft{opened("x", "r1")}, &app.InboundEvent{
			Source: "a", EventID: "full", InstanceID: "i", DedupKey: "d", CorrelationKey: "c", Status: item.Firing, OccurredAt: epoch0,
			Applied: true, Outcome: app.InboundOpened, Reason: "why", Title: "a title", Payload: `{"k":1}`,
		}, nil
	})
	require.NoError(t, err)
	put(t, s, &app.InboundEvent{Source: "a", EventID: "bare", Outcome: app.InboundUnroutable, Reason: "no_route"})

	type row struct {
		Instance, Status, Dedup, Correlation, Outcome, Reason, Title, Payload string
		OccurredAt                                                            *time.Time
		Item                                                                  *string
		Applied                                                               bool
		ReceivedAt                                                            time.Time
	}
	read := func(id string) row {
		rows, err := pool.Query(t.Context(), `SELECT instance_id, status, dedup_key, correlation_key, outcome, reason, title, payload, occurred_at, item_id, applied, received_at
			FROM inbound_events WHERE tenant_id = $1 AND source = 'a' AND event_id = $2`, tenant, id)
		require.NoError(t, err)
		r, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[row])
		require.NoError(t, err)
		return r
	}

	full := read("full")
	require.NotNil(t, full.Item)
	require.NotNil(t, full.OccurredAt)
	assert.Equal(t, "x", *full.Item)
	assert.True(t, epoch0.Equal(*full.OccurredAt))
	assert.False(t, full.ReceivedAt.IsZero())
	full.Item, full.OccurredAt, full.ReceivedAt = nil, nil, time.Time{}
	assert.Equal(t, row{Instance: "i", Status: "firing", Dedup: "d", Correlation: "c", Outcome: "opened", Reason: "why", Title: "a title", Payload: `{"k":1}`, Applied: true}, full)

	bare := read("bare")
	assert.Nil(t, bare.Item)
	assert.Nil(t, bare.OccurredAt)
	bare.ReceivedAt = time.Time{}
	assert.Equal(t, row{Outcome: "unroutable", Reason: "no_route"}, bare)
}

// An applied row is ordered by its status and time: the database refuses one
// without either, and a Put of a row that names an item is refused before it
// reaches the database. Nothing is written.
func TestAnAppliedRowNeedsItsStatusAndTimeAndAPutNamesNoItem(t *testing.T) {
	pool := pgtest.Pool(t)
	tenant := tenantOf(t)
	s := postgres.NewRecords(pool, tenant)
	for name, e := range map[string]*app.InboundEvent{
		"no status": {Source: "a", EventID: "e1", OccurredAt: epoch0, Applied: true, Outcome: app.InboundResolvedUnmatched},
		"no time":   {Source: "a", EventID: "e2", Status: item.Resolved, Applied: true, Outcome: app.InboundResolvedUnmatched},
	} {
		require.ErrorContains(t, s.Put(t.Context(), e), "inbound_events_applied_ordered", name)
	}
	_, err := s.AppendInbound(t.Context(), "x", func([]item.Record) ([]item.Draft, *app.InboundEvent, error) {
		return []item.Draft{opened("x", "r1")}, &app.InboundEvent{Source: "a", EventID: "e3", Item: "x", Applied: true, Outcome: app.InboundAppended}, nil
	})
	require.ErrorContains(t, err, "inbound_events_applied_ordered", "an appended row without its status")
	assert.Zero(t, recordCount(t, s, "x"), "its records roll back with it")

	err = s.Put(t.Context(), &app.InboundEvent{Source: "a", EventID: "e4", Status: item.Firing, OccurredAt: epoch0, Item: "x", Applied: true, Outcome: app.InboundAppended})
	require.ErrorIs(t, err, app.ErrRowOfAnItem)

	var n int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM inbound_events WHERE tenant_id = $1", tenant).Scan(&n))
	assert.Zero(t, n, "nothing was written")
}
