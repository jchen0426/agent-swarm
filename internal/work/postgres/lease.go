package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// Guard checks, inside a write transaction, that the writer may write.
type Guard interface {
	Check(ctx context.Context, tx pgx.Tx) error
}

// Lease is a lease in the leases table, as one instance holds it. It
// implements app.Lease, and is the Guard of the stores that write under it.
type Lease struct {
	pool   *pgxpool.Pool
	tenant string
	name   string
	holder string
	ttl    time.Duration
	// epoch is the epoch of the instance's holding, or 0 when it holds
	// none.
	epoch atomic.Int64
}

// NewLease returns lease name of a tenant, as holder takes it for ttl at a
// time.
func NewLease(pool *pgxpool.Pool, tenant, name, holder string, ttl time.Duration) *Lease {
	return &Lease{pool: pool, tenant: tenant, name: name, holder: holder, ttl: ttl}
}

// Take takes the lease when it was never taken or its holding expired, and
// reads when that holding expired. A takeover waits for the writes of the
// last holding in flight: reading it for update, as changing the epoch
// does, takes the row in a mode their key share holds off.
func (l *Lease) Take(ctx context.Context) (_ app.Term, _ bool, err error) {
	tx, err := l.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return app.Term{}, false, fmt.Errorf("begin taking the lease: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var term app.Term
	err = tx.QueryRow(ctx, `
INSERT INTO leases (tenant_id, name, epoch, holder, expires_at) VALUES ($1, $2, 1, $3, clock_timestamp() + $4::interval)
ON CONFLICT DO NOTHING RETURNING epoch, clock_timestamp()`, l.tenant, l.name, l.holder, l.ttl).Scan(&term.Epoch, &term.At)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
SELECT expires_at FROM leases WHERE tenant_id = $1 AND name = $2 AND expires_at <= clock_timestamp() FOR UPDATE`,
			l.tenant, l.name).Scan(&term.Previous)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.Term{}, false, nil
		}
		if err == nil {
			err = tx.QueryRow(ctx, `
UPDATE leases SET epoch = epoch + 1, holder = $3, expires_at = clock_timestamp() + $4::interval
WHERE tenant_id = $1 AND name = $2
RETURNING epoch, clock_timestamp()`, l.tenant, l.name, l.holder, l.ttl).Scan(&term.Epoch, &term.At)
		}
	}
	if err != nil {
		return app.Term{}, false, fmt.Errorf("take the lease: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Term{}, false, fmt.Errorf("commit taking the lease: %w", err)
	}
	l.epoch.Store(term.Epoch)
	return term, true, nil
}

// Renew extends the holding by the lease's ttl from now, unless it expired
// or another instance took it over. It changes the expiry only, so it waits
// for no write.
func (l *Lease) Renew(ctx context.Context) error {
	epoch := l.epoch.Load()
	tag, err := l.pool.Exec(ctx, `
UPDATE leases SET expires_at = clock_timestamp() + $4::interval
WHERE tenant_id = $1 AND name = $2 AND epoch = $3 AND expires_at > clock_timestamp()`, l.tenant, l.name, epoch, l.ttl)
	if err != nil {
		return fmt.Errorf("renew the lease: %w", err)
	}
	if epoch == 0 || tag.RowsAffected() == 0 {
		l.epoch.CompareAndSwap(epoch, 0)
		return fmt.Errorf("renew the lease: %w", app.ErrLeaseLost)
	}
	return nil
}

// TTL is how long a holding lasts from its last renewal.
func (l *Lease) TTL() time.Duration { return l.ttl }

// Release gives the holding up: it expires at once. A holding that already
// expired is left alone, so the next holding reads the moment it expired.
func (l *Lease) Release(ctx context.Context) error {
	epoch := l.epoch.Swap(0)
	if epoch == 0 {
		return nil
	}
	_, err := l.pool.Exec(ctx, `UPDATE leases SET expires_at = clock_timestamp() WHERE tenant_id = $1 AND name = $2 AND epoch = $3 AND expires_at > clock_timestamp()`,
		l.tenant, l.name, epoch)
	if err != nil {
		return fmt.Errorf("release the lease: %w", err)
	}
	return nil
}

// Check checks, in a write transaction, that the instance's holding still
// holds the lease, and takes the lease's row in key share until the
// transaction ends, so that no takeover commits before the write does.
func (l *Lease) Check(ctx context.Context, tx pgx.Tx) error {
	epoch := l.epoch.Load()
	if epoch == 0 {
		return fmt.Errorf("check the lease: %w", app.ErrLeaseLost)
	}
	var one int
	err := tx.QueryRow(ctx, `
SELECT 1 FROM leases WHERE tenant_id = $1 AND name = $2 AND epoch = $3 AND expires_at > clock_timestamp() FOR KEY SHARE`,
		l.tenant, l.name, epoch).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check the lease: %w", app.ErrLeaseLost)
	}
	if err != nil {
		return fmt.Errorf("check the lease: %w", err)
	}
	return nil
}
