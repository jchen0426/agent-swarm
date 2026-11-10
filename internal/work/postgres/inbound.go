package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// uniqueViolation is the SQLSTATE of a row whose key is held.
const uniqueViolation = "23505"

const insertInbound = `
INSERT INTO inbound_events (tenant_id, source, event_id, received_at, instance_id, status, occurred_at, dedup_key, correlation_key,
                            item_id, applied, outcome, reason, title, payload)
VALUES ($1, $2, $3, clock_timestamp(), $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`

// insertInbound inserts an inbound event's row in tx, the store's own time its
// received_at. A row whose key is held fails with app.ErrSeen.
func (s *Records) insertInbound(ctx context.Context, tx pgx.Tx, e *app.InboundEvent) error {
	var occurred *time.Time
	if !e.OccurredAt.IsZero() {
		occurred = &e.OccurredAt
	}
	var itemID *string
	if e.Item != "" {
		id := string(e.Item)
		itemID = &id
	}
	_, err := tx.Exec(ctx, insertInbound, s.tenant, e.Source, e.EventID, e.InstanceID, string(e.Status), occurred, e.DedupKey,
		e.CorrelationKey, itemID, e.Applied, string(e.Outcome), e.Reason, e.Title, e.Payload)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return fmt.Errorf("%w: source %s", app.ErrSeen, e.Source)
	}
	if err != nil {
		return fmt.Errorf("insert the inbound event of source %s: %w", e.Source, err)
	}
	return nil
}

// Seen tells whether the store holds a row of (source, eventID).
func (s *Records) Seen(ctx context.Context, source, eventID string) (bool, error) {
	var seen bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inbound_events WHERE tenant_id = $1 AND source = $2 AND event_id = $3)`,
		s.tenant, source, eventID).Scan(&seen)
	if err != nil {
		return false, fmt.Errorf("read whether the inbound event of source %s was seen: %w", source, err)
	}
	return seen, nil
}

// Put writes a row that touches no item, in one read committed transaction:
// the guard's check, then the insert. It refuses one that names an item, with
// app.ErrRowOfAnItem, writing nothing: such a row commits with its records
// (see AppendInbound). A row whose key is held fails with app.ErrSeen.
func (s *Records) Put(ctx context.Context, e *app.InboundEvent) (err error) {
	if e.Item != "" {
		return fmt.Errorf("put the inbound event of source %s: %w", e.Source, app.ErrRowOfAnItem)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin put: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if s.guard != nil {
		if err := s.guard.Check(ctx, tx); err != nil {
			return err
		}
	}
	if err := s.insertInbound(ctx, tx, e); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit put: %w", err)
	}
	return nil
}

// LastStatus is the instance's latest applied row: by occurred_at, and at
// equal time resolved over firing.
func (s *Records) LastStatus(ctx context.Context, source, instanceID string) (status item.TriggerStatus, at time.Time, ok bool, err error) {
	var raw string
	err = s.pool.QueryRow(ctx, `
SELECT status, occurred_at FROM inbound_events
WHERE tenant_id = $1 AND source = $2 AND instance_id = $3 AND applied
ORDER BY occurred_at DESC, (status = 'resolved') DESC LIMIT 1`, s.tenant, source, instanceID).Scan(&raw, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("read the last status of an instance of source %s: %w", source, err)
	}
	return item.TriggerStatus(raw), at, true, nil
}

// LastItem is the item of the latest row, by received_at, of the dedup key
// that names an item.
func (s *Records) LastItem(ctx context.Context, source, dedupKey string) (item.ID, bool, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
SELECT item_id FROM inbound_events
WHERE tenant_id = $1 AND source = $2 AND dedup_key = $3 AND item_id IS NOT NULL
ORDER BY received_at DESC LIMIT 1`, s.tenant, source, dedupKey).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read the last item of a dedup key of source %s: %w", source, err)
	}
	return item.ID(id), true, nil
}

// Titles is the title of each event id of source that the store keeps, read
// by the table's primary key (tenant_id, source, event_id): an id it no
// longer keeps, as after the store's retention, is absent.
func (s *Records) Titles(ctx context.Context, source string, eventIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(eventIDs))
	if len(eventIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
SELECT event_id, title FROM inbound_events
WHERE tenant_id = $1 AND source = $2 AND event_id = ANY($3)`, s.tenant, source, eventIDs)
	if err != nil {
		return nil, fmt.Errorf("query the titles of inbound events of source %s: %w", source, err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, fmt.Errorf("read the titles of inbound events of source %s: %w", source, err)
		}
		out[id] = title
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the titles of inbound events of source %s: %w", source, err)
	}
	return out, nil
}
