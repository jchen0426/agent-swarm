package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// Guard checks, inside a write transaction, that the writer may write.
type Guard interface {
	Check(ctx context.Context, tx pgx.Tx) error
}

// lockNotAvailable is the SQLSTATE of a wait for a lock that outlasted
// lock_timeout.
const lockNotAvailable = "55P03"

// Lease is a lease in the leases table, as one instance holds it. It
// implements app.Lease, and is the Guard of the stores that write under it.
type Lease struct {
	pool   *pgxpool.Pool
	tenant string
	name   string
	holder string
	ttl    time.Duration
	// takeWait bounds a take's wait for the writes of the last holding in
	// flight.
	takeWait time.Duration
	// epoch is the epoch of the instance's holding, or 0 when it holds
	// none: the holding Renew and Release act on.
	epoch atomic.Int64
}

// NewLease returns lease name of a tenant, as holder takes it for ttl at a
// time. A take waits for the writes of the last holding still in flight for
// takeWait at most, in whole milliseconds and at least one.
func NewLease(pool *pgxpool.Pool, tenant, name, holder string, ttl, takeWait time.Duration) *Lease {
	return &Lease{pool: pool, tenant: tenant, name: name, holder: holder, ttl: ttl, takeWait: takeWait}
}

// Take takes the lease when it was never taken or its holding expired, and
// reads when that holding expired. A takeover waits for the writes of the
// last holding in flight: reading it for update, as changing the epoch
// does, takes the row in a mode their key share holds off. It waits the
// take's wait at most, by lock_timeout: a take that waits longer has not
// taken the lease, and says so with no error, as when another instance holds
// it, since only a holding's writers hold its row (R9). The next take tries
// again.
func (l *Lease) Take(ctx context.Context) (_ app.Term, _ bool, err error) {
	tx, err := l.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return app.Term{}, false, fmt.Errorf("begin taking the lease: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	wait := strconv.FormatInt(max(l.takeWait.Milliseconds(), 1), 10) + "ms"
	if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', $1, true)", wait); err != nil {
		return app.Term{}, false, fmt.Errorf("bound the take's wait: %w", err)
	}
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
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable {
		return app.Term{}, false, nil
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
		return fmt.Errorf("renew the lease: %w", holding.ErrLeaseLost)
	}
	return nil
}

// TTL is how long a holding lasts from its last renewal.
func (l *Lease) TTL() time.Duration { return l.ttl }

// Sample reads clock_timestamp() on the lease's pool: the database's time,
// which the coordinator's clock estimates (clock.DB). The statement waits on
// no lock.
func (l *Lease) Sample(ctx context.Context) (time.Time, error) {
	var at time.Time
	if err := l.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("sample the database's clock: %w", err)
	}
	return at, nil
}

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

// Check checks, in a write transaction, that the holding the write acts for,
// the epoch holding.EpochFrom(ctx) gives, still holds the lease, whichever
// holding the instance has now, and takes the lease's row in key share until
// the transaction ends, so that no takeover commits before the write does. It
// fails wrapping holding.ErrLeaseLost when that holding is over; a context
// without an epoch fails wrapping holding.ErrNoEpoch, a bug, and not
// ErrLeaseLost.
func (l *Lease) Check(ctx context.Context, tx pgx.Tx) error {
	epoch, err := holding.EpochFrom(ctx)
	if err != nil {
		return fmt.Errorf("check the lease: %w", err)
	}
	var one int
	err = tx.QueryRow(ctx, `
SELECT 1 FROM leases WHERE tenant_id = $1 AND name = $2 AND epoch = $3 AND expires_at > clock_timestamp() FOR KEY SHARE`,
		l.tenant, l.name, epoch).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check the lease: %w", holding.ErrLeaseLost)
	}
	if err != nil {
		return fmt.Errorf("check the lease: %w", err)
	}
	return nil
}
