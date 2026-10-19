package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// Records keeps work items' records in the work_records table. It
// implements app.Records.
type Records struct {
	pool   *pgxpool.Pool
	tenant string
	guard  Guard
}

// NewRecords returns the store of one tenant's work records.
func NewRecords(pool *pgxpool.Pool, tenant string) *Records {
	return &Records{pool: pool, tenant: tenant}
}

const selectRecords = `
SELECT seq, id, incident_id, run_id, recorded_at, body
FROM work_records
WHERE tenant_id = $1 AND item_id = $2
ORDER BY seq`

const insertRecord = `
INSERT INTO work_records (tenant_id, item_id, seq, id, incident_id, run_id, kind, body, recorded_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, greatest(clock_timestamp(), $9::timestamptz))
RETURNING recorded_at`

// Guarded returns the store, writing only while guard allows it: each
// append checks it after taking the item's lock, before it reads or writes.
func (s *Records) Guarded(guard Guard) *Records {
	g := *s
	g.guard = guard
	return &g
}

// Load returns an item's records in commit order. A record of this version
// whose body does not decode is invalid, as a record the fold refuses is: the
// error wraps item.ErrInvalid and the decode error, which tells an item with
// a corrupt row from a read that failed. A record of another version decodes
// to its version alone, which the fold refuses with item.ErrVersion.
func (s *Records) Load(ctx context.Context, id item.ID) ([]item.Record, error) {
	return loadRecords(ctx, s.pool, s.tenant, id)
}

// Active returns the items that do not await their close, by id: each
// item's opening, read by kind, without a record that it awaits its close.
func (s *Records) Active(ctx context.Context) ([]item.ID, error) {
	rows, err := s.pool.Query(ctx, `
SELECT o.item_id FROM work_records o
WHERE o.tenant_id = $1 AND o.kind = $2 AND NOT EXISTS (
    SELECT 1 FROM work_records c WHERE c.tenant_id = o.tenant_id AND c.kind = $3 AND c.item_id = o.item_id)
ORDER BY o.item_id`, s.tenant, string(item.KindOpened), string(item.KindAwaitingClose))
	if err != nil {
		return nil, fmt.Errorf("query active items: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[item.ID])
	if err != nil {
		return nil, fmt.Errorf("read active items: %w", err)
	}
	return ids, nil
}

// Append commits the records that decide returns, after the item's last.
// A transaction-scoped advisory lock serializes appends per item, so decide
// always sees the latest history, and record times come from the database
// clock, never going back within an item. The transaction runs at read
// committed, as the board's appends do.
func (s *Records) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) (_ []item.Record, err error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin append: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "work_records/"+s.tenant+"/"+string(id)); err != nil {
		return nil, fmt.Errorf("lock item %s: %w", id, err)
	}
	if s.guard != nil {
		if err := s.guard.Check(ctx, tx); err != nil {
			return nil, err
		}
	}
	history, err := loadRecords(ctx, tx, s.tenant, id)
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
	committed := make([]item.Record, 0, len(drafts))
	for i := range drafts {
		d := &drafts[i]
		if d.Item != id {
			return nil, fmt.Errorf("record %s belongs to item %s, not %s", d.ID, d.Item, id)
		}
		r := item.Record{Seq: lastSeq + 1, ID: d.ID, Item: id, Incident: d.Incident, Run: d.Run, Body: d.Body}
		err = tx.QueryRow(ctx, insertRecord, s.tenant, string(id), r.Seq, r.ID, string(r.Incident), string(r.Run),
			string(r.Body.Kind()), r.Body, lastAt).Scan(&r.At)
		if err != nil {
			return nil, fmt.Errorf("insert record %s: %w", d.ID, err)
		}
		lastSeq, lastAt = r.Seq, r.At
		committed = append(committed, r)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit append: %w", err)
	}
	return committed, nil
}

func loadRecords(ctx context.Context, q querier, tenant string, id item.ID) ([]item.Record, error) {
	rows, err := q.Query(ctx, selectRecords, tenant, string(id))
	if err != nil {
		return nil, fmt.Errorf("query records of item %s: %w", id, err)
	}
	records, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (item.Record, error) {
		r := item.Record{Item: id}
		var incident, run string
		var body []byte
		if err := row.Scan(&r.Seq, &r.ID, &incident, &run, &r.At, &body); err != nil {
			return item.Record{}, err
		}
		r.Incident, r.Run = item.IncidentID(incident), board.RunID(run)
		// The scan leaves the body undecoded, so that a body that does not
		// decode is told from a read that failed.
		if err := json.Unmarshal(body, &r.Body); err != nil {
			return item.Record{}, fmt.Errorf("record %d: %w: %w", r.Seq, item.ErrInvalid, err)
		}
		return r, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read records of item %s: %w", id, err)
	}
	return records, nil
}

// Runs tells the board rules what a run's records say of it. It implements
// the Work application's Runs port.
type Runs struct {
	pool   *pgxpool.Pool
	tenant string
}

// NewRuns returns the runs of one tenant's work records.
func NewRuns(pool *pgxpool.Pool, tenant string) *Runs {
	return &Runs{pool: pool, tenant: tenant}
}

// BoardContext returns a run's board context: the settings its creation
// record holds, and human entries for a primary run people see. Its round
// is the board's to say.
func (r *Runs) BoardContext(ctx context.Context, run board.RunID) (board.Context, error) {
	var raw []byte
	// run_id <> '' lets a generic plan use the partial index on runs.
	err := r.pool.QueryRow(ctx, `SELECT body FROM work_records WHERE tenant_id = $1 AND run_id = $2 AND run_id <> '' AND kind = $3`,
		r.tenant, string(run), string(item.KindRunCreated)).Scan(&raw)
	if err != nil {
		return board.Context{}, fmt.Errorf("read the creation of run %s: %w", run, err)
	}
	var b item.Body
	if err := json.Unmarshal(raw, &b); err != nil || b.RunCreated == nil {
		return board.Context{}, fmt.Errorf("read the creation of run %s: not a run's creation", run)
	}
	rc := b.RunCreated
	return board.Context{Run: run, HumanEntries: rc.Class == item.ClassPrimary && rc.Mode == item.ModeVisible, Settings: rc.Settings}, nil
}
