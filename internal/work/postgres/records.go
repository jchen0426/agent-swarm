package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/work/app"
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
// append checks it, for the holding whose epoch the append's context
// carries, after taking the item's lock, before it reads or writes.
func (s *Records) Guarded(guard Guard) *Records {
	g := *s
	g.guard = guard
	return &g
}

// Load returns an item's records in commit order. A record whose body does
// not decode is invalid, as a record the fold refuses is: the error wraps
// item.ErrInvalid and the decode error, which tells an item with a corrupt
// row from a read that failed. Such is a body of this version that does not
// decode, and one whose version is missing, null or below 1. A body of
// another version decodes to its version alone, which the fold refuses with
// item.ErrVersion.
func (s *Records) Load(ctx context.Context, id item.ID) ([]item.Record, error) {
	return loadRecords(ctx, s.pool, s.tenant, id)
}

// Active returns the items that are not closed, by id: each item's opening,
// read by kind, without an item_closed that no item_reopened of a later seq
// follows. Every read is by kind, on the index work_records_by_kind.
func (s *Records) Active(ctx context.Context) ([]item.ID, error) {
	rows, err := s.pool.Query(ctx, `
SELECT o.item_id FROM work_records o
WHERE o.tenant_id = $1 AND o.kind = $2 AND NOT EXISTS (
    SELECT 1 FROM work_records c WHERE c.tenant_id = o.tenant_id AND c.kind = $3 AND c.item_id = o.item_id AND NOT EXISTS (
        SELECT 1 FROM work_records r WHERE r.tenant_id = c.tenant_id AND r.kind = $4 AND r.item_id = c.item_id AND r.seq > c.seq))
ORDER BY o.item_id`, s.tenant, string(item.KindOpened), string(item.KindClosed), string(item.KindReopened))
	if err != nil {
		return nil, fmt.Errorf("query active items: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[item.ID])
	if err != nil {
		return nil, fmt.Errorf("read active items: %w", err)
	}
	return ids, nil
}

// ClosedSince returns the items whose latest item_closed committed within
// window of the database's time and which no item_reopened of a later seq
// followed, by id. A close that a later close follows is not the latest, so
// the item is found by its latest alone. It reads by kind, on the index
// work_records_by_kind.
func (s *Records) ClosedSince(ctx context.Context, window time.Duration) ([]item.ID, error) {
	rows, err := s.pool.Query(ctx, `
SELECT c.item_id FROM work_records c
WHERE c.tenant_id = $1 AND c.kind = $2 AND c.recorded_at > clock_timestamp() - $4::float8 * interval '1 second'
  AND NOT EXISTS (
    SELECT 1 FROM work_records r WHERE r.tenant_id = c.tenant_id AND r.item_id = c.item_id AND r.kind IN ($2, $3) AND r.seq > c.seq)
ORDER BY c.item_id`, s.tenant, string(item.KindClosed), string(item.KindReopened), window.Seconds())
	if err != nil {
		return nil, fmt.Errorf("query the items closed within %s: %w", window, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[item.ID])
	if err != nil {
		return nil, fmt.Errorf("read the items closed within %s: %w", window, err)
	}
	return ids, nil
}

// IncidentItems returns the items whose latest record is in incident, by id
// (Decision 18): an item's incident is its latest record's, so of an item's
// records in incident only its latest record of all keeps it, once. It reads
// by incident, on migration 0011's index work_records_by_incident, and each
// item's latest seq on the table's key.
func (s *Records) IncidentItems(ctx context.Context, incident item.IncidentID) ([]item.ID, error) {
	rows, err := s.pool.Query(ctx, `
SELECT r.item_id FROM work_records r
WHERE r.tenant_id = $1 AND r.incident_id = $2
  AND r.seq = (SELECT max(m.seq) FROM work_records m WHERE m.tenant_id = r.tenant_id AND m.item_id = r.item_id)
ORDER BY r.item_id`, s.tenant, string(incident))
	if err != nil {
		return nil, fmt.Errorf("query the items of incident %s: %w", incident, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[item.ID])
	if err != nil {
		return nil, fmt.Errorf("read the items of incident %s: %w", incident, err)
	}
	return ids, nil
}

// selectSiblings lists an incident's other items, newest opened first (R172):
// by incident, on migration 0011's index work_records_by_incident, each item
// whose latest seq, on the table's key, is a record in the incident, ordered
// by its first record's time, its item_opened's (the fold's first rule), read
// on the key at seq 1; and how many such items there are, the window's count
// before the limit. One statement, which sees one snapshot.
const selectSiblings = `
SELECT r.item_id, count(*) OVER ()
FROM work_records r
JOIN work_records o ON o.tenant_id = r.tenant_id AND o.item_id = r.item_id AND o.seq = 1
WHERE r.tenant_id = $1 AND r.incident_id = $2 AND r.item_id <> $3
  AND r.seq = (SELECT max(m.seq) FROM work_records m WHERE m.tenant_id = r.tenant_id AND m.item_id = r.item_id)
ORDER BY o.recorded_at DESC, r.item_id
LIMIT $4`

// Siblings lists the items whose latest record is in incident, but except,
// newest opened first, at equal times by id, at most limit, and how many such
// items there are (design D7; R172), in one statement: an item linked in from
// another incident among them, as IncidentItems reads them. With none, the
// list is empty and the count 0.
func (s *Records) Siblings(ctx context.Context, incident item.IncidentID, except item.ID, limit int) ([]item.ID, int, error) {
	rows, err := s.pool.Query(ctx, selectSiblings, s.tenant, string(incident), string(except), limit)
	if err != nil {
		return nil, 0, fmt.Errorf("query the other items of incident %s: %w", incident, err)
	}
	ids := []item.ID{}
	var (
		id    string
		count int64
	)
	_, err = pgx.ForEachRow(rows, []any{&id, &count}, func() error {
		ids = append(ids, item.ID(id))
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("read the other items of incident %s: %w", incident, err)
	}
	return ids, int(count), nil
}

// UnstaffedBy returns the items whose item_awaiting_close names the kill
// switch's row seq as the on-row that left them not staffed
// (awaitingClose.unstaffedBy), by id. It reads by kind, on the index
// work_records_by_kind.
func (s *Records) UnstaffedBy(ctx context.Context, seq int64) ([]item.ID, error) {
	rows, err := s.pool.Query(ctx, `
SELECT DISTINCT item_id FROM work_records
WHERE tenant_id = $1 AND kind = $2 AND body -> 'awaitingClose' -> 'unstaffedBy' = to_jsonb($3::bigint)
ORDER BY item_id`, s.tenant, string(item.KindAwaitingClose), seq)
	if err != nil {
		return nil, fmt.Errorf("query the items the kill switch's row %d left unstaffed: %w", seq, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[item.ID])
	if err != nil {
		return nil, fmt.Errorf("read the items the kill switch's row %d left unstaffed: %w", seq, err)
	}
	return ids, nil
}

// Append commits the records that decide returns, after the item's last.
// A transaction-scoped advisory lock serializes appends per item, so decide
// always sees the latest history, and record times come from the database
// clock, never going back within an item. The transaction runs at read
// committed, as the board's appends do. An automatic close whose record's
// time, as the insert stamps it, is before its due is refused with
// item.ErrNotDue, and the transaction rolls back, writing nothing (R41).
func (s *Records) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	return s.appendRows(ctx, id, func(h []item.Record) ([]item.Draft, *app.InboundEvent, error) {
		drafts, err := decide(h)
		return drafts, nil, err
	})
}

// AppendInbound is Append whose decide also returns an inbound event's row.
// The row is inserted after the drafts, in the same transaction, under the
// same guard, with its item set to id. A row whose key is held fails with
// app.ErrSeen, and the transaction writes nothing (R109).
func (s *Records) AppendInbound(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, *app.InboundEvent, error)) ([]item.Record, error) {
	return s.appendRows(ctx, id, decide)
}

func (s *Records) appendRows(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, *app.InboundEvent, error)) (_ []item.Record, err error) {
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
	drafts, row, err := decide(history)
	if err != nil {
		return nil, err
	}
	committed, err := s.insertDrafts(ctx, tx, id, history, drafts)
	if err != nil {
		return nil, err
	}
	if row != nil {
		stored := *row
		stored.Item = id
		if err := s.insertInbound(ctx, tx, &stored); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit append: %w", err)
	}
	return committed, nil
}

// insertDrafts inserts drafts after history's last record, in tx.
func (s *Records) insertDrafts(ctx context.Context, tx pgx.Tx, id item.ID, history []item.Record, drafts []item.Draft) ([]item.Record, error) {
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
		err := tx.QueryRow(ctx, insertRecord, s.tenant, string(id), r.Seq, r.ID, string(r.Incident), string(r.Run),
			string(r.Body.Kind()), r.Body, lastAt).Scan(&r.At)
		if err != nil {
			return nil, fmt.Errorf("insert record %s: %w", d.ID, err)
		}
		if err := due(&r); err != nil {
			return nil, err
		}
		lastSeq, lastAt = r.Seq, r.At
		committed = append(committed, r)
	}
	return committed, nil
}

// due refuses, with item.ErrNotDue, a record of an automatic close that its
// insert stamped before its due (R41).
func due(r *item.Record) error {
	if c := r.Body.Closed; c != nil && c.Auto && r.At.Before(c.Due) {
		return fmt.Errorf("record %s is stamped %s, before its due %s: %w", r.ID, r.At, c.Due, item.ErrNotDue)
	}
	return nil
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
