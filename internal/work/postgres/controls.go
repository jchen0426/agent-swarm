package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/ids"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/control"
)

// Controls keeps one tenant's platform-wide controls: the kill switch's rows,
// in the kill_switch table (Decision 5; Contract C17). It implements
// app.Controls.
type Controls struct {
	pool   *pgxpool.Pool
	tenant string
	guard  Guard
}

// NewControls returns the store of one tenant's controls.
func NewControls(pool *pgxpool.Pool, tenant string) *Controls {
	return &Controls{pool: pool, tenant: tenant}
}

// Guarded returns the store, writing only while guard allows it: each
// SetKillSwitch checks it, for the holding whose epoch the request's context
// carries, after taking the tenant's lock of the switch, before it reads or
// writes.
func (s *Controls) Guarded(guard Guard) *Controls {
	g := *s
	g.guard = guard
	return &g
}

// selectKillSwitch reads the kill switch's state in one query: its latest
// row, the latest on-row's number and the latest off-row's time. A tenant
// whose switch was never set has no row.
const selectKillSwitch = `
SELECT l.seq, l.state = 'on', l.actor, l.reason_code, l.reason_text, l.recorded_at,
       coalesce((SELECT max(o.seq) FROM kill_switch o WHERE o.tenant_id = l.tenant_id AND o.state = 'on'), 0),
       (SELECT f.recorded_at FROM kill_switch f WHERE f.tenant_id = l.tenant_id AND f.state = 'off' ORDER BY f.seq DESC LIMIT 1)
FROM kill_switch l
WHERE l.tenant_id = $1
ORDER BY l.seq DESC
LIMIT 1`

const insertKillSwitch = `
INSERT INTO kill_switch (tenant_id, seq, id, state, actor, reason_code, reason_text, recorded_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, clock_timestamp())
RETURNING recorded_at`

// KillSwitch returns the kill switch's state, its times in UTC: off, with no
// row, until it is first set.
func (s *Controls) KillSwitch(ctx context.Context) (control.KillSwitch, error) {
	return readKillSwitch(ctx, s.pool, s.tenant)
}

// SetKillSwitch turns the kill switch on or off for by, for reason, whose
// text the caller redacted, as a request whose sender saw after as the latest
// row (Decision 5). It runs one transaction at read committed, which takes the
// tenant's lock of the switch, so that requests are serialized, checks the
// guard, and reads the state. A request whose state holds already appends
// nothing, whatever after says; one whose after is not the latest row's
// number appends nothing either, and fails with control.ErrStale; any other
// appends the next row, at the database's clock_timestamp(). It returns the
// state after it, and whether it appended a row, whatever it did: the state
// comes with ErrStale too. The guard's refusal, which wraps
// holding.ErrLeaseLost, comes with no state: the request read none.
func (s *Controls) SetKillSwitch(ctx context.Context, on bool, by string, reason board.Reason, after int64) (control.KillSwitch, bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return control.KillSwitch{}, false, fmt.Errorf("begin setting the kill switch: %w", err)
	}
	// Once the transaction commits, its rollback does nothing.
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "kill_switch/"+s.tenant); err != nil {
		return control.KillSwitch{}, false, fmt.Errorf("lock the kill switch: %w", err)
	}
	if s.guard != nil {
		if err := s.guard.Check(ctx, tx); err != nil {
			return control.KillSwitch{}, false, err
		}
	}
	state, err := readKillSwitch(ctx, tx, s.tenant)
	switch {
	case err != nil:
		return control.KillSwitch{}, false, err
	case state.On == on:
		return state, false, nil
	case after != state.Seq:
		return state, false, fmt.Errorf("the kill switch's latest row is %d, not %d: %w", state.Seq, after, control.ErrStale)
	}
	next := control.KillSwitch{On: on, Seq: state.Seq + 1, OnSeq: state.OnSeq, OffAt: state.OffAt, By: by, Reason: reason}
	err = tx.QueryRow(ctx, insertKillSwitch, s.tenant, next.Seq, ids.UUIDv7{}.NewID(), stateName(on), by, reason.Code, reason.Text).
		Scan(&next.Since)
	if err != nil {
		return control.KillSwitch{}, false, fmt.Errorf("insert the kill switch's row %d: %w", next.Seq, err)
	}
	next.Since = next.Since.UTC()
	if on {
		next.OnSeq = next.Seq
	} else {
		next.OffAt = next.Since
	}
	if err := tx.Commit(ctx); err != nil {
		return control.KillSwitch{}, false, fmt.Errorf("commit the kill switch's row %d: %w", next.Seq, err)
	}
	return next, true, nil
}

// LatestKillSwitch returns the number of the kill switch's latest row, 0
// before the first: one read on the table's primary key, which the leader's
// renewal check makes (Decision 5).
func (s *Controls) LatestKillSwitch(ctx context.Context) (int64, error) {
	var seq int64
	if err := s.pool.QueryRow(ctx, "SELECT coalesce(max(seq), 0) FROM kill_switch WHERE tenant_id = $1", s.tenant).Scan(&seq); err != nil {
		return 0, fmt.Errorf("read the kill switch's latest row: %w", err)
	}
	return seq, nil
}

// stateName is a row's state, as the table holds it.
func stateName(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// readKillSwitch reads the kill switch's state through q, a pool or the
// transaction of a request.
func readKillSwitch(ctx context.Context, q querier, tenant string) (control.KillSwitch, error) {
	rows, err := q.Query(ctx, selectKillSwitch, tenant)
	if err != nil {
		return control.KillSwitch{}, fmt.Errorf("read the kill switch: %w", err)
	}
	states, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (control.KillSwitch, error) {
		var (
			s     control.KillSwitch
			offAt *time.Time
		)
		if err := row.Scan(&s.Seq, &s.On, &s.By, &s.Reason.Code, &s.Reason.Text, &s.Since, &s.OnSeq, &offAt); err != nil {
			return control.KillSwitch{}, err
		}
		s.Since = s.Since.UTC()
		if offAt != nil {
			s.OffAt = offAt.UTC()
		}
		return s, nil
	})
	if err != nil {
		return control.KillSwitch{}, fmt.Errorf("read the kill switch: %w", err)
	}
	if len(states) == 0 {
		return control.KillSwitch{}, nil
	}
	return states[0], nil
}
