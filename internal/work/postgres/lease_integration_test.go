//go:build integration

package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/clock"
	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var (
	_ app.Lease     = (*postgres.Lease)(nil)
	_ clock.Sampler = (*postgres.Lease)(nil)
)

// takeWait is how long the tests' takes wait for the writes in flight: long
// enough that none of them gives up but where a test says so.
const takeWait = 5 * time.Second

func take(t *testing.T, l *postgres.Lease) app.Term {
	t.Helper()
	term, took, err := l.Take(t.Context())
	require.NoError(t, err)
	require.True(t, took)
	return term
}

// under returns the test's context, carrying the term's epoch: a write under
// it acts for that holding.
func under(t *testing.T, term app.Term) context.Context {
	t.Helper()
	return holding.WithEpoch(t.Context(), term.Epoch)
}

// One instance holds the lease at a time. Releasing it lets another take
// it at once, with a new epoch.
func TestTheLeaseIsHeldByOneInstanceAtATime(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-one", "platform", "a", time.Minute, takeWait)
	b := postgres.NewLease(pool, "lease-one", "platform", "b", time.Minute, takeWait)

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
	a := postgres.NewLease(pool, "lease-expired", "platform", "a", 300*time.Millisecond, takeWait)
	b := postgres.NewLease(pool, "lease-expired", "platform", "b", time.Minute, takeWait)
	first := under(t, take(t, a))
	time.Sleep(400 * time.Millisecond)

	_, err := postgres.NewRecords(pool, "lease-expired").Guarded(a).Append(first, "item-1", drafting(opened("item-1", "r0")))
	require.ErrorIs(t, err, app.ErrLeaseLost, "an expired holding writes nothing, even before a takeover")
	term := take(t, b)

	assert.Equal(t, int64(2), term.Epoch)
	require.ErrorIs(t, a.Renew(t.Context()), app.ErrLeaseLost)
	_, err = postgres.NewRecords(pool, "lease-expired").Guarded(a).Append(first, "item-1", drafting(opened("item-1", "r1")))
	require.ErrorIs(t, err, app.ErrLeaseLost)
	_, err = postgres.NewEventStore(pool, "lease-expired").Guarded(a).Append(first, "run-1", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{evidenceDraft("run-1", "le-1", "v42")}, nil
	})
	require.ErrorIs(t, err, app.ErrLeaseLost)
	_, err = postgres.NewRecords(pool, "lease-expired").Guarded(b).Append(under(t, term), "item-1", drafting(opened("item-1", "r2")))
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
	a := postgres.NewLease(pool, "lease-previous", "platform", "a", 300*time.Millisecond, takeWait)
	b := postgres.NewLease(pool, "lease-previous", "platform", "b", time.Minute, takeWait)
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

// A take reads the name of the instance whose holding came before it, for
// the reason a sweep gives the model calls that holding left (R11): after
// a's holding expires, b's take reads a; after b releases, a's reads b; after
// a's own holding expires, its next take reads a. The lease's first holding
// reads none.
func TestATakeReadsThePreviousHoldersName(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-previous-holder", "platform", "a", 300*time.Millisecond, takeWait)
	b := postgres.NewLease(pool, "lease-previous-holder", "platform", "b", time.Minute, takeWait)
	first := take(t, a)
	time.Sleep(400 * time.Millisecond)

	second := take(t, b)
	require.NoError(t, b.Release(t.Context()))
	third := take(t, a)
	time.Sleep(400 * time.Millisecond)
	fourth := take(t, a)

	assert.Empty(t, first.PreviousHolder, "the lease's first holding")
	assert.Equal(t, "a", second.PreviousHolder, "a's holding expired")
	assert.Equal(t, "b", third.PreviousHolder, "b released its holding")
	assert.Equal(t, "a", fourth.PreviousHolder, "a's own holding expired")
}

// Releasing an expired holding changes nothing: the next holding's Previous is its expiry.
func TestReleasingAnExpiredHoldingLeavesItsEnd(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-released-expired", "platform", "a", 300*time.Millisecond, takeWait)
	b := postgres.NewLease(pool, "lease-released-expired", "platform", "b", time.Minute, takeWait)
	first := take(t, a)
	time.Sleep(800 * time.Millisecond)

	require.NoError(t, a.Release(t.Context()), "releasing an expired holding is no error")
	second := take(t, b)

	assert.WithinDuration(t, first.At.Add(300*time.Millisecond), second.Previous, 100*time.Millisecond, "the expiry, not the release")
}

// A holding taken over is not ended by its earlier holder's release, as when
// that instance's leader gives the lease up after a takeover that found it
// gone: the release names the epoch it held, and the new holding goes on.
func TestAReleaseOfAnEarlierHoldingLeavesTheNewOne(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-stale-release", "platform", "a", 300*time.Millisecond, takeWait)
	b := postgres.NewLease(pool, "lease-stale-release", "platform", "b", time.Minute, takeWait)
	take(t, a)
	time.Sleep(400 * time.Millisecond)
	take(t, b)

	require.NoError(t, a.Release(t.Context()), "a gives up the holding it had")

	require.NoError(t, b.Renew(t.Context()), "b still holds the lease")
	_, took, err := a.Take(t.Context())
	require.NoError(t, err)
	assert.False(t, took, "and no one can take it")
}

// A holding that expired is not renewed, though no other instance took the
// lease over: it ended when it expired, as the next takeover reads it, and its
// holder neither writes nor acts until it takes the lease again.
func TestAnExpiredHoldingIsNotRenewed(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-renew-expired", "platform", "a", 300*time.Millisecond, takeWait)
	first := take(t, a)
	time.Sleep(400 * time.Millisecond)

	require.ErrorIs(t, a.Renew(t.Context()), app.ErrLeaseLost)
	_, err := postgres.NewRecords(pool, "lease-renew-expired").Guarded(a).Append(under(t, first), "item-1", drafting(opened("item-1", "r1")))
	require.ErrorIs(t, err, app.ErrLeaseLost, "nor does it write")
	second := take(t, a)
	assert.Equal(t, int64(2), second.Epoch, "a new holding")
	assert.WithinDuration(t, first.At.Add(300*time.Millisecond), second.Previous, 100*time.Millisecond, "the first ended when it expired")
}

// heldAnswer is a query tracer that, once armed, holds the answer of the first
// query begun after that until letGo is closed: the database ran the query,
// and its caller has not seen the answer yet. inside is closed once that
// answer is held.
type heldAnswer struct {
	mu     sync.Mutex
	armed  bool
	inside chan struct{}
	letGo  chan struct{}
}

type heldQuery struct{}

func (h *heldAnswer) arm() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.armed = true
}

func (h *heldAnswer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.armed {
		return ctx
	}
	h.armed = false
	return context.WithValue(ctx, heldQuery{}, true)
}

func (h *heldAnswer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if ctx.Value(heldQuery{}) != nil {
		close(h.inside)
		<-h.letGo
	}
}

// A renewal whose answer comes back after its instance took the lease again,
// as when its leader, fenced while the renewal was in flight, takes the lease
// once the holding expired, leaves the new holding alone: the renewal found
// its own holding expired, and forgets that holding only, so the new one is
// renewed and writes.
func TestALateRenewalOfAnExpiredHoldingLeavesTheNextOne(t *testing.T) {
	held := &heldAnswer{inside: make(chan struct{}), letGo: make(chan struct{})}
	cfg, err := pgxpool.ParseConfig(pgtest.URL(t))
	require.NoError(t, err)
	cfg.ConnConfig.Tracer = held
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	// The held answer is let go before the pool closes, whatever happens.
	letGo := sync.OnceFunc(func() { close(held.letGo) })
	t.Cleanup(letGo)
	a := postgres.NewLease(pool, "lease-late-renewal", "platform", "a", 300*time.Millisecond, takeWait)
	take(t, a)
	time.Sleep(400 * time.Millisecond)
	held.arm()
	renewed := make(chan error, 1)
	go func() { renewed <- a.Renew(context.Background()) }()
	<-held.inside
	second := take(t, a)

	letGo()

	require.ErrorIs(t, <-renewed, app.ErrLeaseLost, "the late renewal found its holding expired")
	assert.Equal(t, int64(2), second.Epoch)
	require.NoError(t, a.Renew(t.Context()), "the next holding is renewed")
	_, err = postgres.NewRecords(pool, "lease-late-renewal").Guarded(a).Append(under(t, second), "item-1", drafting(opened("item-1", "r1")))
	require.NoError(t, err, "and writes")
}

// A takeover waits for the writes of the last holding still in flight, and
// fences the rest: a write that checked the lease before the takeover
// commits first, and none after it does.
func TestATakeoverWaitsForWritesInFlight(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-flight", "platform", "a", 300*time.Millisecond, takeWait)
	b := postgres.NewLease(pool, "lease-flight", "platform", "b", time.Minute, takeWait)
	first := take(t, a)
	inside, release := make(chan struct{}), make(chan struct{})
	appended := make(chan error, 1)
	go func() {
		ctx := holding.WithEpoch(context.Background(), first.Epoch)
		_, err := postgres.NewRecords(pool, "lease-flight").Guarded(a).Append(ctx, "item-1", func([]item.Record) ([]item.Draft, error) {
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
	_, err := postgres.NewRecords(pool, "lease-flight").Guarded(a).Append(under(t, first), "item-1", drafting(opened("item-1", "r2")))
	require.ErrorIs(t, err, app.ErrLeaseLost)
}

// Renewing the lease waits for no write: writes take the lease's row in
// key share, which a renewal does not wait for.
func TestRenewalWaitsForNoWrite(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-renew", "platform", "a", time.Minute, takeWait)
	term := take(t, a)
	inside, release := make(chan struct{}), make(chan struct{})
	appended := make(chan error, 1)
	go func() {
		ctx := holding.WithEpoch(context.Background(), term.Epoch)
		_, err := postgres.NewEventStore(pool, "lease-renew").Guarded(a).Append(ctx, "run-1", func([]board.Event) ([]board.Draft, error) {
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

// The lease samples the database's time, for the coordinator's clock: the
// sample lies between the database's times read before it and after it.
func TestTheLeasesSampleIsTheDatabasesTime(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-sample", "platform", "a", time.Minute, takeWait)
	var before, after time.Time
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&before))

	at, err := a.Sample(t.Context())
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&after))

	require.NoError(t, err)
	assert.False(t, at.Before(before), "not before the time read before it")
	assert.False(t, at.After(after), "nor after the time read after it")
}

// A store that writes under the lease writes nothing before it is taken.
func TestAGuardedStoreWritesNothingBeforeTheLeaseIsTaken(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-none", "platform", "a", time.Minute, takeWait)

	_, err := postgres.NewRecords(pool, "lease-none").Guarded(a).Append(holding.WithEpoch(t.Context(), 1), "item-1", drafting(opened("item-1", "r1")))

	require.ErrorIs(t, err, app.ErrLeaseLost)
	assertNoRecords(t, pool, "lease-none")
}

// A write acts for the holding its context names, whichever holding its
// instance has now: after a release and a second take, a write whose context
// carries the first epoch is refused, and nothing is written. The refusal is
// the holding's ErrLeaseLost, which the coordinator's matches too.
func TestAGuardedWriteUnderAnEarlierEpochIsRefused(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-earlier", "platform", "a", time.Minute, takeWait)
	first := take(t, a)
	require.NoError(t, a.Release(t.Context()))
	second := take(t, a)

	_, recorded := postgres.NewRecords(pool, "lease-earlier").Guarded(a).Append(under(t, first), "item-1", drafting(opened("item-1", "r1")))
	_, posted := postgres.NewEventStore(pool, "lease-earlier").Guarded(a).Append(under(t, first), "run-1", func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{evidenceDraft("run-1", "ea-1", "v42")}, nil
	})

	require.ErrorIs(t, recorded, holding.ErrLeaseLost)
	require.ErrorIs(t, recorded, app.ErrLeaseLost, "the coordinator's sentinel is the holding's")
	require.ErrorIs(t, posted, holding.ErrLeaseLost)
	assertNoRecords(t, pool, "lease-earlier")
	assertNoEvents(t, pool, "lease-earlier")
	_, err := postgres.NewRecords(pool, "lease-earlier").Guarded(a).Append(under(t, second), "item-1", drafting(opened("item-1", "r2")))
	require.NoError(t, err, "a write under the current epoch lands")
}

// A write whose context carries no epoch is a bug, not a lost lease: though
// the lease is held, it is refused with ErrNoEpoch, which never fences, and
// nothing is written.
func TestAGuardedWriteWithoutAnEpochIsABugNotALostLease(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-no-epoch", "platform", "a", time.Minute, takeWait)
	take(t, a)

	_, err := postgres.NewRecords(pool, "lease-no-epoch").Guarded(a).Append(t.Context(), "item-1", drafting(opened("item-1", "r1")))

	require.ErrorIs(t, err, holding.ErrNoEpoch)
	require.NotErrorIs(t, err, holding.ErrLeaseLost)
	assertNoRecords(t, pool, "lease-no-epoch")
}

// A take waits for the writes of the last holding in flight for its wait at
// most: past it, it has not taken the lease, and says so with no error, as
// when another instance holds it (R9). Its leader takes again later.
func TestATakeBehindAWriterGivesUpAfterItsWait(t *testing.T) {
	pool := pgtest.Pool(t)
	a := postgres.NewLease(pool, "lease-take-wait", "platform", "a", 300*time.Millisecond, takeWait)
	b := postgres.NewLease(pool, "lease-take-wait", "platform", "b", time.Minute, 300*time.Millisecond)
	first := take(t, a)
	writer, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = writer.Rollback(context.Background()) }()
	require.NoError(t, a.Check(under(t, first), writer), "the writer holds the lease's row in key share")
	time.Sleep(400 * time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	start := time.Now()
	_, took, err := b.Take(ctx)
	waited := time.Since(start)

	require.NoError(t, err, "a take that waited past its wait is no error")
	assert.False(t, took)
	assert.Less(t, waited, time.Second)
}

// queuedEvent is a board event a writer inserts by hand: its insert takes the
// tenant's audit chain lock, until the writer's transaction ends.
const queuedEvent = `INSERT INTO board_events (tenant_id, run_id, seq, id, round, kind, author_kind, author_name, committed_at, body)
VALUES ($1, $2, 1, $3, 1, 'task', 'agent', 'release-investigator', clock_timestamp(), '{"task": {"title": "check the pool"}}')`

// A takeover comes within its bound behind writers queued on the audit
// chain's lock (review 2's probe of the chain's lock): serve's pools end every
// transaction past its timeout, whether it idles or waits. Writer 1 passes the
// guard, inserts a board event, which takes the chain's lock, and idles in its
// transaction; writers 2 and 3 pass the guard and wait for the chain's lock,
// so they are not idle. With the idle timeout alone, each would hold the
// lease's row a timeout after the one before it, and the takeover would come
// after about three timeouts.
func TestATakeoverComesWithinItsBoundBehindWritersQueuedOnTheChain(t *testing.T) {
	const tenant = "lease-chain-queue"
	bound := db.PoolOptions{TransactionTimeout: time.Second, IdleTimeout: time.Second, ConnectTimeout: time.Second}
	pool, err := db.OpenWith(t.Context(), pgtest.URL(t), bound)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	other, err := db.OpenWith(t.Context(), pgtest.URL(t), bound)
	require.NoError(t, err)
	t.Cleanup(other.Close)
	observer := pgtest.Pool(t)
	a := postgres.NewLease(pool, tenant, "platform", "a", time.Second, 300*time.Millisecond)
	b := postgres.NewLease(other, tenant, "platform", "b", time.Second, 300*time.Millisecond)
	ctx := holding.WithEpoch(context.Background(), take(t, a).Epoch)

	start := time.Now()
	first, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = first.Rollback(context.Background()) }()
	require.NoError(t, a.Check(ctx, first))
	_, err = first.Exec(ctx, queuedEvent, tenant, "run-1", "run-1-1")
	require.NoError(t, err)
	type writer struct{ check, commit error }
	queued, commit := make(chan writer, 2), make(chan struct{})
	for _, run := range []string{"run-2", "run-3"} {
		go func() {
			var w writer
			tx, err := pool.Begin(ctx)
			if err != nil {
				w.check, w.commit = err, err
				queued <- w
				return
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if w.check = a.Check(ctx, tx); w.check == nil {
				_, _ = tx.Exec(ctx, queuedEvent, tenant, run, run+"-1")
			}
			<-commit
			w.commit = tx.Commit(context.Background())
			queued <- w
		}()
	}
	require.Eventually(t, func() bool {
		var n int
		err := observer.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity
WHERE wait_event_type = 'Lock' AND wait_event = 'advisory' AND query LIKE 'INSERT INTO board_events%'`).Scan(&n)
		return err == nil && n == 2
	}, 500*time.Millisecond, 5*time.Millisecond, "writers 2 and 3 wait for the chain's lock")

	took := false
	for !took && time.Since(start) < 5*time.Second {
		_, took, err = b.Take(t.Context())
		require.NoError(t, err)
		if !took {
			time.Sleep(200 * time.Millisecond)
		}
	}
	elapsed := time.Since(start)

	require.True(t, took, "the other instance took the lease")
	assert.Less(t, elapsed, 2*time.Second, "the takeover came within its bound, from writer 1's start")
	require.Error(t, first.Commit(context.Background()), "writer 1's commit fails")
	close(commit)
	for range 2 {
		w := <-queued
		require.NoError(t, w.check, "the writer passed the guard")
		require.Error(t, w.commit, "the writer's commit fails")
	}
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

func assertNoEvents(t *testing.T, pool *pgxpool.Pool, tenant string) {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT count(*) FROM board_events WHERE tenant_id = $1`, tenant).Scan(&n))
	assert.Zero(t, n)
}
