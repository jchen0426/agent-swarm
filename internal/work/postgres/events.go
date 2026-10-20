// Package postgres stores the Work context's data in Postgres. It implements
// the ports that the Work application declares.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// EventStore keeps board events in the board_events table. It implements
// app.EventStore.
type EventStore struct {
	pool   *pgxpool.Pool
	tenant string
	guard  Guard
}

// NewEventStore returns an event store for one tenant's rows. A deployment
// serves one organization, but its records still carry a tenant id (spec
// Section 1.4).
func NewEventStore(pool *pgxpool.Pool, tenant string) *EventStore {
	return &EventStore{pool: pool, tenant: tenant}
}

const selectEvents = `
SELECT seq, id, round, author_kind, author_name, committed_at, body
FROM board_events
WHERE tenant_id = $1 AND run_id = $2
ORDER BY seq`

const insertEvent = `
INSERT INTO board_events
    (tenant_id, run_id, seq, id, round, kind, author_kind, author_name, committed_at, body)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, greatest(clock_timestamp(), $9::timestamptz), $10)
RETURNING committed_at`

// Guarded returns the store, writing only while guard allows it: each
// append checks it, for the holding whose epoch the append's context
// carries, after taking the run's lock, before it reads or writes.
func (s *EventStore) Guarded(guard Guard) *EventStore {
	g := *s
	g.guard = guard
	return &g
}

// Load returns a run's events in commit order.
func (s *EventStore) Load(ctx context.Context, run board.RunID) ([]board.Event, error) {
	return loadEvents(ctx, s.pool, s.tenant, run)
}

// Append commits the entries that decide returns, after the run's last event.
// A transaction-scoped advisory lock serializes appends per run, so decide
// always sees the latest history. Commit times come from the database clock
// and never go backwards within a run.
//
// The transaction runs at read committed whatever the database's default is:
// under repeatable read, its snapshot would predate its wait for the lock and
// miss the entries of the writer it waited for.
func (s *EventStore) Append(ctx context.Context, run board.RunID, decide func([]board.Event) ([]board.Draft, error)) (_ []board.Event, err error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin append: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", s.tenant+"/"+string(run)); err != nil {
		return nil, fmt.Errorf("lock run %s: %w", run, err)
	}
	if s.guard != nil {
		if err := s.guard.Check(ctx, tx); err != nil {
			return nil, err
		}
	}
	history, err := loadEvents(ctx, tx, s.tenant, run)
	if err != nil {
		return nil, err
	}
	drafts, err := decide(history)
	if err != nil {
		return nil, err
	}
	var lastSeq int64
	var lastAt time.Time
	if n := len(history); n > 0 {
		lastSeq, lastAt = history[n-1].Seq, history[n-1].At
	}
	committed := make([]board.Event, 0, len(drafts))
	for i := range drafts {
		d := &drafts[i]
		if d.Run != run {
			return nil, fmt.Errorf("entry %s belongs to run %s, not %s", d.ID, d.Run, run)
		}
		e := board.Event{Seq: lastSeq + 1, ID: d.ID, Run: run, Round: d.Round, Author: d.Author, Body: d.Body}
		err = tx.QueryRow(ctx, insertEvent,
			s.tenant, string(run), e.Seq, string(e.ID), e.Round, string(e.Body.Kind()),
			string(e.Author.Kind), e.Author.Name, lastAt, e.Body,
		).Scan(&e.At)
		if err != nil {
			return nil, fmt.Errorf("insert entry %s: %w", d.ID, err)
		}
		lastSeq, lastAt = e.Seq, e.At
		committed = append(committed, e)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit append: %w", err)
	}
	return committed, nil
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func loadEvents(ctx context.Context, q querier, tenant string, run board.RunID) ([]board.Event, error) {
	rows, err := q.Query(ctx, selectEvents, tenant, string(run))
	if err != nil {
		return nil, fmt.Errorf("query board events of run %s: %w", run, err)
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (board.Event, error) {
		e := board.Event{Run: run}
		var id, authorKind string
		if err := row.Scan(&e.Seq, &id, &e.Round, &authorKind, &e.Author.Name, &e.At, &e.Body); err != nil {
			return board.Event{}, err
		}
		e.ID, e.Author.Kind = board.EntryID(id), board.AuthorKind(authorKind)
		return e, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read board events of run %s: %w", run, err)
	}
	return events, nil
}
