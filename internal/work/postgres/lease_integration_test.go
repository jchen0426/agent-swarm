//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var _ app.Lease = (*postgres.Lease)(nil)

func take(t *testing.T, l *postgres.Lease) app.Term {
	t.Helper()
	term, took, err := l.Take(t.Context())
	require.NoError(t, err)
	require.True(t, took)
	return term
}

// One instance holds the lease at a time. Releasing it lets another take
// it at once, with a new epoch.
func TestTheLeaseIsHeldByOneInstanceAtATime(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-one", "platform", "a", time.Minute)
	b := postgres.NewLease(pool, "lease-one", "platform", "b", time.Minute)

	first := take(t, a)
	_, took, err := b.Take(t.Context())
	require.NoError(t, err)
	require.NoError(t, a.Renew(t.Context()))
	require.NoError(t, a.Release(t.Context()))
	second := take(t, b)

	assert.False(t, took, "a holds it")
	assert.Equal(t, int64(1), first.Epoch)
	assert.Equal(t, int64(2), second.Epoch)
	assert.WithinDuration(t, time.Now(), first.At, time.Minute, "the database's time")
	require.ErrorIs(t, a.Renew(t.Context()), app.ErrLeaseLost, "a released it")
}

// A holding that expired is taken over with a new epoch, and the instance
// that held it can neither renew it nor write.
func TestAnExpiredLeaseIsTakenOverAndItsHolderFenced(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-expired", "platform", "a", 300*time.Millisecond)
	b := postgres.NewLease(pool, "lease-expired", "platform", "b", time.Minute)
	take(t, a)
	time.Sleep(400 * time.Millisecond)

	_, err := postgres.NewRecords(pool, "lease-expired").Guarded(a).Append(t.Context(), "item-1", drafting(opened("item-1", "r0")))
	require.ErrorIs(t, err, app.ErrLeaseLost, "an expired holding writes nothing, even before a takeover")
	term := take(t, b)

	assert.Equal(t, int64(2), term.Epoch)
	require.ErrorIs(t, a.Renew(t.Context()), app.ErrLeaseLost)
	_, err = postgres.NewRecords(pool, "lease-expired").Guarded(a).Append(t.Context(), "item-1", drafting(opened("item-1", "r1")))
	require.ErrorIs(t, err, app.ErrLeaseLost)
	_, err = postgres.NewEventStore(pool, "lease-expired").Guarded(a).Append(t.Context(), "run-1", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{evidenceDraft("run-1", "le-1", "v42")}, nil
	})
	require.ErrorIs(t, err, app.ErrLeaseLost)
	_, err = postgres.NewRecords(pool, "lease-expired").Guarded(b).Append(t.Context(), "item-1", drafting(opened("item-1", "r2")))
	require.NoError(t, err)
	records, err := postgres.NewRecords(pool, "lease-expired").Load(t.Context(), "item-1")
	require.NoError(t, err)
	assert.Len(t, records, 1, "only the holder's record")
}

// A holding knows when the one before it expired, in the database's time,
// so that a takeover tells a round whose deadline passed while no instance
// held the lease from one whose holder died after its deadline; the first
// holding has none. The lease says how long a holding lasts.
func TestAHoldingKnowsWhenTheOneBeforeItExpired(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-previous", "platform", "a", 300*time.Millisecond)
	b := postgres.NewLease(pool, "lease-previous", "platform", "b", time.Minute)
	first := take(t, a)
	time.Sleep(400 * time.Millisecond)

	second := take(t, b)
	require.NoError(t, b.Release(t.Context()))
	third := take(t, a)

	assert.True(t, first.Previous.IsZero())
	assert.WithinDuration(t, first.At.Add(300*time.Millisecond), second.Previous, 50*time.Millisecond)
	assert.True(t, second.Previous.Before(second.At))
	assert.True(t, third.Previous.After(second.At), "a release ends the holding at once")
	assert.True(t, third.Previous.Before(third.At))
	assert.Equal(t, time.Minute, b.TTL())
}

// A takeover waits for the writes of the last holding still in flight, and
// fences the rest: a write that checked the lease before the takeover
// commits first, and none after it does.
func TestATakeoverWaitsForWritesInFlight(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-flight", "platform", "a", 300*time.Millisecond)
	b := postgres.NewLease(pool, "lease-flight", "platform", "b", time.Minute)
	take(t, a)
	inside, release := make(chan struct{}), make(chan struct{})
	appended := make(chan error, 1)
	go func() {
		_, err := postgres.NewRecords(pool, "lease-flight").Guarded(a).Append(context.Background(), "item-1", func([]item.Record) ([]item.Draft, error) {
			close(inside)
			<-release
			return []item.Draft{opened("item-1", "r1")}, nil
		})
		appended <- err
	}()
	<-inside
	time.Sleep(400 * time.Millisecond)
	taken := make(chan app.Term, 1)
	go func() {
		term, _, _ := b.Take(context.Background())
		taken <- term
	}()

	require.Never(t, func() bool { return len(taken) > 0 }, 300*time.Millisecond, 10*time.Millisecond, "the takeover waits for the write in flight")
	close(release)
	require.NoError(t, <-appended)
	term := <-taken

	assert.Equal(t, int64(2), term.Epoch)
	_, err := postgres.NewRecords(pool, "lease-flight").Guarded(a).Append(t.Context(), "item-1", drafting(opened("item-1", "r2")))
	require.ErrorIs(t, err, app.ErrLeaseLost)
}

// Renewing the lease waits for no write: writes take the lease's row in
// key share, which a renewal does not wait for.
func TestRenewalWaitsForNoWrite(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-renew", "platform", "a", time.Minute)
	take(t, a)
	inside, release := make(chan struct{}), make(chan struct{})
	appended := make(chan error, 1)
	go func() {
		_, err := postgres.NewEventStore(pool, "lease-renew").Guarded(a).Append(context.Background(), "run-1", func([]board.Event) ([]board.Draft, error) {
			close(inside)
			<-release
			return []board.Draft{evidenceDraft("run-1", "rn-1", "v42")}, nil
		})
		appended <- err
	}()
	<-inside

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err := a.Renew(ctx)
	close(release)

	require.NoError(t, err, "the renewal did not wait for the write")
	require.NoError(t, <-appended)
}

// A store that writes under the lease writes nothing before it is taken.
func TestAGuardedStoreWritesNothingBeforeTheLeaseIsTaken(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-none", "platform", "a", time.Minute)

	_, err := postgres.NewRecords(pool, "lease-none").Guarded(a).Append(t.Context(), "item-1", drafting(opened("item-1", "r1")))

	require.ErrorIs(t, err, app.ErrLeaseLost)
	assertNoRecords(t, pool, "lease-none")
}

func drafting(drafts ...item.Draft) func([]item.Record) ([]item.Draft, error) {
	return func([]item.Record) ([]item.Draft, error) { return drafts, nil }
}

func assertNoRecords(t *testing.T, pool *pgxpool.Pool, tenant string) {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM work_records WHERE tenant_id = $1`, tenant).Scan(&n))
	assert.Zero(t, n)
}
