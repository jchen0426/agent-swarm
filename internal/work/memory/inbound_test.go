package memory_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
)

var _ app.Inbound = (*memory.Records)(nil)

func putRow(t *testing.T, s *memory.Records, e *app.InboundEvent) {
	t.Helper()
	require.NoError(t, s.Put(t.Context(), e))
}

// The memory store answers as the Postgres one does, case by case. A row is
// seen by its source and event id; a second of the same key is refused and
// leaves the first unchanged.
func TestTheMemoryInboundStoreAnswersAsThePostgresOne(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	clock := t0
	now := func() time.Time { clock = clock.Add(time.Second); return clock }

	t.Run("an event is seen once by source and id", func(t *testing.T) {
		s := memory.NewRecords(now)
		putRow(t, s, &app.InboundEvent{Source: "alerting", EventID: "e1", Outcome: app.InboundRefused, Reason: "first"})

		seen, err := s.Seen(t.Context(), "alerting", "e1")
		require.NoError(t, err)
		assert.True(t, seen)
		seen, err = s.Seen(t.Context(), "paging", "e1")
		require.NoError(t, err)
		assert.False(t, seen)
		err = s.Put(t.Context(), &app.InboundEvent{Source: "alerting", EventID: "e1", Outcome: app.InboundRefused, Reason: "second"})
		require.ErrorIs(t, err, app.ErrSeen)
	})

	t.Run("an instance's last status is its latest applied row", func(t *testing.T) {
		s := memory.NewRecords(now)
		t1, t2, t3 := t0.Add(time.Minute), t0.Add(2*time.Minute), t0.Add(3*time.Minute)
		putRow(t, s, &app.InboundEvent{Source: "a", EventID: "1", InstanceID: "i", Status: item.Firing, OccurredAt: t1, Item: "x", Applied: true, Outcome: app.InboundAppended})
		putRow(t, s, &app.InboundEvent{Source: "a", EventID: "2", InstanceID: "i", Status: item.Firing, OccurredAt: t0, Outcome: app.InboundLate})
		putRow(t, s, &app.InboundEvent{Source: "a", EventID: "3", InstanceID: "i", Status: item.Resolved, OccurredAt: t2, Applied: true, Outcome: app.InboundResolvedUnmatched})
		putRow(t, s, &app.InboundEvent{Source: "a", EventID: "4", InstanceID: "i", Outcome: app.InboundRefused})

		status, at, ok, err := s.LastStatus(t.Context(), "a", "i")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, item.Resolved, status)
		assert.True(t, t2.Equal(at))

		putRow(t, s, &app.InboundEvent{Source: "a", EventID: "5", InstanceID: "j", Status: item.Firing, OccurredAt: t3, Item: "y", Applied: true, Outcome: app.InboundAppended})
		putRow(t, s, &app.InboundEvent{Source: "a", EventID: "6", InstanceID: "j", Status: item.Resolved, OccurredAt: t3, Item: "y", Applied: true, Outcome: app.InboundAppended})
		status, at, ok, err = s.LastStatus(t.Context(), "a", "j")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, item.Resolved, status, "at equal time resolved wins")
		assert.True(t, t3.Equal(at))

		_, _, ok, err = s.LastStatus(t.Context(), "a", "nobody")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("the last item a key took is the latest row with an item", func(t *testing.T) {
		s := memory.NewRecords(now)
		putRow(t, s, &app.InboundEvent{Source: "a", EventID: "1", DedupKey: "k", Item: "A", Applied: true, Outcome: app.InboundOpened})
		putRow(t, s, &app.InboundEvent{Source: "a", EventID: "2", DedupKey: "k", Item: "B", Applied: true, Outcome: app.InboundAppended})
		putRow(t, s, &app.InboundEvent{Source: "a", EventID: "3", DedupKey: "k", Outcome: app.InboundUnroutable})

		id, ok, err := s.LastItem(t.Context(), "a", "k")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, item.ID("B"), id)
		_, ok, err = s.LastItem(t.Context(), "a", "unknown")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("a row commits with its records, or nothing does", func(t *testing.T) {
		s := memory.NewRecords(now)
		drafts := []item.Draft{
			{ID: "r1", Item: "x", Incident: "inc", Body: body(item.Body{Opened: &item.Opened{Type: "incident"}})},
			{ID: "r2", Item: "x", Incident: "inc", Body: body(item.Body{Started: &item.Started{}})},
		}
		committed, err := s.AppendInbound(t.Context(), "x", func([]item.Record) ([]item.Draft, *app.InboundEvent, error) {
			return drafts, &app.InboundEvent{Source: "a", EventID: "e1", DedupKey: "k", Applied: true, Outcome: app.InboundOpened}, nil
		})
		require.NoError(t, err)
		assert.Len(t, committed, 2)
		id, ok, err := s.LastItem(t.Context(), "a", "k")
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, item.ID("x"), id, "the row's item is the item appended to")

		_, err = s.AppendInbound(t.Context(), "y", func([]item.Record) ([]item.Draft, *app.InboundEvent, error) {
			return []item.Draft{{ID: "r3", Item: "y", Incident: "inc", Body: body(item.Body{Opened: &item.Opened{Type: "incident"}})}}, nil, assert.AnError
		})
		require.ErrorIs(t, err, assert.AnError)
		loaded, err := s.Load(t.Context(), "y")
		require.NoError(t, err)
		assert.Empty(t, loaded, "a decide that fails commits nothing")

		_, err = s.AppendInbound(t.Context(), "z", func([]item.Record) ([]item.Draft, *app.InboundEvent, error) {
			return []item.Draft{{ID: "r4", Item: "z", Incident: "inc", Body: body(item.Body{Opened: &item.Opened{Type: "incident"}})}},
				&app.InboundEvent{Source: "a", EventID: "e1", Outcome: app.InboundRefused}, nil
		})
		require.ErrorIs(t, err, app.ErrSeen)
		loaded, err = s.Load(t.Context(), "z")
		require.NoError(t, err)
		assert.Empty(t, loaded, "a held key commits no record")
	})

	t.Run("a stored row keeps every column and the store's time", func(t *testing.T) {
		s := memory.NewRecords(now)
		full := app.InboundEvent{
			Source: "a", EventID: "e1", InstanceID: "i", DedupKey: "d", CorrelationKey: "c", Status: item.Firing, OccurredAt: t0,
			Item: "x", Applied: true, Outcome: app.InboundAppended, Reason: "r", Title: "t", Payload: "{}",
		}
		putRow(t, s, &full)
		rows := s.InboundRows()
		require.Len(t, rows, 1)
		got := rows[0]
		assert.False(t, got.ReceivedAt.IsZero())
		got.ReceivedAt = time.Time{}
		assert.Equal(t, full, got)
	})
}
