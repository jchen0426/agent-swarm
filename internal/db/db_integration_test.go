//go:build integration

package db_test

import (
	"context"
	"fmt"
	"io/fs"
	neturl "net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
)

func TestMigrateTwiceChangesNothing(t *testing.T) {
	pool := pgtest.Pool(t)

	require.NoError(t, db.Migrate(t.Context(), pool))

	files, err := filepath.Glob("migrations/*.sql")
	require.NoError(t, err)
	var applied int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM schema_migrations").Scan(&applied))
	assert.Len(t, files, applied)
}

func TestBoardEventsAreAppendOnly(t *testing.T) {
	ctx := t.Context()
	pool := pgtest.Pool(t)
	_, err := pool.Exec(ctx, `INSERT INTO board_events
		(tenant_id, run_id, seq, id, round, kind, author_kind, author_name, committed_at, body)
		VALUES ('t', 'append-only', 1, 'append-only-1', 1, 'evidence', 'agent', 'a', now(), '{}')`)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, "UPDATE board_events SET author_name = 'b' WHERE id = 'append-only-1'")
	require.ErrorContains(t, err, "append-only")
	_, err = pool.Exec(ctx, "DELETE FROM board_events WHERE id = 'append-only-1'")
	require.ErrorContains(t, err, "append-only")
	_, err = pool.Exec(ctx, "TRUNCATE board_events")
	require.ErrorContains(t, err, "append-only")

	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL agentswarm.retention = 'on'"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "DELETE FROM board_events WHERE id = 'append-only-1'")
		return err
	})
	require.NoError(t, err, "the retention job may delete")
}

func TestOpenFailsOnABadURL(t *testing.T) {
	_, err := db.Open(context.Background(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1")

	require.Error(t, err)
}

// Every connection Open makes runs at read committed whatever the database's
// default, since the audit chain is written only there, and ends a
// transaction left idle for a minute, which would hold the chain's lock.
func TestOpenReadsCommittedAndEndsIdleTransactions(t *testing.T) {
	url := pgtest.URL(t)
	admin := pgtest.Pool(t)
	_, err := admin.Exec(t.Context(), "CREATE DATABASE open_defaults")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS open_defaults WITH (FORCE)")
		assert.NoError(t, err)
	})
	_, err = admin.Exec(t.Context(), "ALTER DATABASE open_defaults SET default_transaction_isolation = 'repeatable read'")
	require.NoError(t, err)
	u, err := neturl.Parse(url)
	require.NoError(t, err)
	u.Path = "/open_defaults"

	pool, err := db.Open(t.Context(), u.String())
	require.NoError(t, err)
	defer pool.Close()

	var level, idle string
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT current_setting('transaction_isolation'), current_setting('idle_in_transaction_session_timeout')").Scan(&level, &idle))
	assert.Equal(t, "read committed", level)
	assert.Equal(t, "1min", idle)
}

// Serve's pools bound every transaction, and every transaction left idle, to
// at most the lease's time less its margin, so that a writer of a holding
// that is over holds the lease's row, and the audit chain's lock, no longer
// than that (Decision 4). They read committed whatever the database's
// default, as Open's do. They bound the ping of a connection that has idled,
// which runs under the caller's context, to a second, so that a connection to
// a database whose address drops its packets holds no take, renewal or write
// past it; Open's pool, migrate's, sets no such bound.
func TestServesPoolsBoundTheirTransactionsAndIdleTransactions(t *testing.T) {
	admin := pgtest.Pool(t)
	_, err := admin.Exec(t.Context(), "CREATE DATABASE open_bounded")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS open_bounded WITH (FORCE)")
		assert.NoError(t, err)
	})
	_, err = admin.Exec(t.Context(), "ALTER DATABASE open_bounded SET default_transaction_isolation = 'repeatable read'")
	require.NoError(t, err)
	u, err := neturl.Parse(pgtest.URL(t))
	require.NoError(t, err)
	u.Path = "/open_bounded"

	pool, err := db.OpenWith(t.Context(), u.String(), db.PoolOptions{TransactionTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second})
	require.NoError(t, err)
	defer pool.Close()

	var transaction, idle, level string
	require.NoError(t, pool.QueryRow(t.Context(), `SELECT current_setting('transaction_timeout'),
current_setting('idle_in_transaction_session_timeout'), current_setting('transaction_isolation')`).Scan(&transaction, &idle, &level))
	assert.Equal(t, "10s", transaction)
	assert.Equal(t, "10s", idle)
	assert.Equal(t, "read committed", level)
	assert.Equal(t, time.Second, pool.Config().PingTimeout, "an idle connection's ping")
	unbounded, err := db.Open(t.Context(), u.String())
	require.NoError(t, err)
	defer unbounded.Close()
	assert.Zero(t, unbounded.Config().PingTimeout, "Open's pool")
}

// Serve refuses a database server older than Postgres 17, which has no
// transaction_timeout. CheckServer connects setting no timeout, since such a
// server refuses a connection that sets one, as OpenWith's do.
func TestAServerOlderThan17IsRefused(t *testing.T) {
	ctr, err := postgres.Run(t.Context(), "postgres:16-alpine",
		postgres.WithDatabase("agentswarm"),
		postgres.WithUsername("agentswarm"),
		postgres.WithPassword("agentswarm"),
		testcontainers.WithLabels(map[string]string{
			"agentswarm.project": "agent-swarm",
			"agentswarm.data":    "/var/lib/postgresql/data",
		}),
		postgres.BasicWaitStrategies(),
	)
	t.Cleanup(func() { assert.NoError(t, testcontainers.TerminateContainer(ctr)) })
	require.NoError(t, err)
	old, err := ctr.ConnectionString(t.Context(), "sslmode=disable")
	require.NoError(t, err)

	refused := db.CheckServer(t.Context(), old)
	_, bounded := db.OpenWith(t.Context(), old, db.PoolOptions{TransactionTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second})

	require.Error(t, refused)
	assert.Contains(t, refused.Error(), "PostgreSQL 16.", "it names the server's version")
	require.Error(t, bounded, "a server older than 17 refuses a connection that sets transaction_timeout")
	require.NoError(t, db.CheckServer(t.Context(), pgtest.URL(t)), "pgtest's Postgres 17")
}

// CheckServer reads a URL as serve's pools read it: a deployment's URL may
// carry the pools' own settings, such as pool_max_conns, which are no
// connection parameters, and the check before the pools open takes it.
func TestTheServersCheckReadsAURLAsThePoolsDo(t *testing.T) {
	url := pgtest.URL(t) + "&pool_max_conns=4"
	pool, err := db.OpenWith(t.Context(), url, db.PoolOptions{TransactionTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second})
	require.NoError(t, err, "the pools take the URL")
	pool.Close()

	assert.NoError(t, db.CheckServer(t.Context(), url))
}

// Migration 0005 chains the audited tables while writers insert into them,
// and leaves no row unchained: it locks the tables until it commits, by which
// time its triggers are in place. The migrations before 0005 are applied by
// hand, so that db.Migrate runs 0005 and every migration after it, including
// any added later, under the writers.
func TestChainingWhileWritersRunLeavesNoRowUnchained(t *testing.T) {
	admin := pgtest.Pool(t)
	_, err := admin.Exec(t.Context(), "CREATE DATABASE migrate_live")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS migrate_live WITH (FORCE)")
		assert.NoError(t, err)
	})
	u, err := neturl.Parse(pgtest.URL(t))
	require.NoError(t, err)
	u.Path = "/migrate_live"
	pool, err := db.Open(t.Context(), u.String())
	require.NoError(t, err)
	defer pool.Close()
	files, err := filepath.Glob("migrations/*.sql")
	require.NoError(t, err)
	chaining := slices.Index(files, "migrations/0005_audit_chain.sql")
	require.GreaterOrEqual(t, chaining, 0, "migrations/0005_audit_chain.sql is absent")
	_, err = pool.Exec(t.Context(), "CREATE TABLE schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())")
	require.NoError(t, err)
	for _, f := range files[:chaining] {
		sql, err := os.ReadFile(f)
		require.NoError(t, err)
		_, err = pool.Exec(t.Context(), string(sql))
		require.NoError(t, err, f)
		_, err = pool.Exec(t.Context(), "INSERT INTO schema_migrations (name) VALUES ($1)", filepath.Base(f))
		require.NoError(t, err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO tool_calls (tenant_id, call_id, run_id, agent, server, tool, schema_hash, arguments, round, started_at)
SELECT 't', 'before-' || i, 'r', 'a', 's', 't', 'h', '{}', 1, now() FROM generate_series(1, 20000) i`)
	require.NoError(t, err)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := range 4 {
		wg.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_, err := pool.Exec(context.Background(), `INSERT INTO tool_calls (tenant_id, call_id, run_id, agent, server, tool, schema_hash, arguments, round, started_at)
VALUES ('t', $1, 'r', 'a', 's', 't', 'h', '{}', 1, now())`, fmt.Sprintf("during-%d-%d", w, i))
				assert.NoError(t, err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, db.Migrate(t.Context(), pool))
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	var rows, records int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM tool_calls").Scan(&rows))
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_chain WHERE source = 'tool_calls'").Scan(&records))
	assert.Greater(t, rows, 20000)
	assert.Equal(t, rows, records)
}

// freshDatabase creates a database of the package's container that no
// migration has touched, and opens a pool to it. Both go when the test ends.
func freshDatabase(t *testing.T, name string) *pgxpool.Pool {
	t.Helper()
	admin := pgtest.Pool(t)
	_, err := admin.Exec(t.Context(), "CREATE DATABASE "+name)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		assert.NoError(t, err)
	})
	u, err := neturl.Parse(pgtest.URL(t))
	require.NoError(t, err)
	u.Path = "/" + name
	pool, err := db.Open(t.Context(), u.String())
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// levelOf returns the level a migration's file name gives it: its number.
func levelOf(t *testing.T, name string) int {
	t.Helper()
	number, _, ok := strings.Cut(name, "_")
	require.True(t, ok, name)
	level, err := strconv.Atoi(number)
	require.NoError(t, err, name)
	return level
}

// readers returns each applied migration's min_reader, by its name.
func readers(t *testing.T, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	rows, err := pool.Query(t.Context(), "SELECT name, min_reader FROM schema_migrations")
	require.NoError(t, err)
	got := map[string]int{}
	var (
		name   string
		reader int
	)
	_, err = pgx.ForEachRow(rows, []any{&name, &reader}, func() error {
		got[name] = reader
		return nil
	})
	require.NoError(t, err)
	return got
}

// exists reports whether the database has a table of the name.
func exists(t *testing.T, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var ok bool
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT to_regclass($1) IS NOT NULL", table).Scan(&ok))
	return ok
}

// withEmbedded returns the embedded migrations and one more.
func withEmbedded(t *testing.T, name, script string) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{name: {Data: []byte(script)}}
	names, err := fs.Glob(db.Migrations(), "*.sql")
	require.NoError(t, err)
	require.NotEmpty(t, names)
	for _, n := range names {
		data, err := fs.ReadFile(db.Migrations(), n)
		require.NoError(t, err)
		fsys[n] = &fstest.MapFile{Data: data}
	}
	return fsys
}

// hold writes a holding of the platform's lease by holder, which expires
// after in, or expired that long ago when in is negative.
func hold(t *testing.T, pool *pgxpool.Pool, holder string, in time.Duration) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `INSERT INTO leases (tenant_id, name, epoch, holder, expires_at)
VALUES ('t', 'coordinator', 1, $1, clock_timestamp() + $2::interval)`, holder, in)
	require.NoError(t, err)
}

func TestMigrateRecordsEachMigrationsReader(t *testing.T) {
	pool := freshDatabase(t, "migrate_readers")

	require.NoError(t, db.Migrate(t.Context(), pool))

	got := readers(t, pool)
	files, err := filepath.Glob("migrations/*.sql")
	require.NoError(t, err)
	require.Len(t, got, len(files))
	for name, reader := range got {
		assert.Equal(t, levelOf(t, name), reader, "%s is at its own level", name)
	}
	s, err := db.ReadSchema(t.Context(), pool)
	require.NoError(t, err)
	last := filepath.Base(files[len(files)-1])
	assert.Equal(t, db.Schema{Level: db.Level(), Last: last, MinReader: db.Level()}, s)
	assert.Equal(t, levelOf(t, last), db.Level(), "the build's level is its last migration's")
}

// things is a migration that creates a table no trigger names.
const things = "CREATE TABLE things (id text PRIMARY KEY);\n"

func TestAMigrationMayDeclareAnOlderReader(t *testing.T) {
	pool := freshDatabase(t, "migrate_declared")
	fsys := fstest.MapFS{
		"0001_things.sql": {Data: []byte(things)},
		"0002_note.sql":   {Data: []byte("-- agentswarm:min_reader 1\nALTER TABLE things ADD COLUMN note text;\n")},
	}

	res, err := db.MigrateFS(t.Context(), pool, fsys, db.MigrateOptions{})

	require.NoError(t, err)
	assert.Equal(t, []string{"0001_things.sql", "0002_note.sql"}, res.Applied)
	assert.Equal(t, map[string]int{"0001_things.sql": 1, "0002_note.sql": 1}, readers(t, pool))
	want := db.Schema{Level: 2, Last: "0002_note.sql", MinReader: 1}
	assert.Equal(t, want, res.Schema)
	s, err := db.ReadSchema(t.Context(), pool)
	require.NoError(t, err)
	assert.Equal(t, want, s)
}

func TestADeclarationAboveItsOwnLevelIsRefused(t *testing.T) {
	pool := freshDatabase(t, "migrate_above")
	fsys := fstest.MapFS{
		"0001_things.sql": {Data: []byte(things)},
		"0002_note.sql":   {Data: []byte("-- agentswarm:min_reader 3\nALTER TABLE things ADD COLUMN note text;\n")},
	}

	_, err := db.MigrateFS(t.Context(), pool, fsys, db.MigrateOptions{})

	require.ErrorContains(t, err, "0002_note.sql")
	s, err := db.ReadSchema(t.Context(), pool)
	require.NoError(t, err)
	assert.Equal(t, db.Schema{}, s, "nothing is applied")
	assert.False(t, exists(t, pool, "things"), "0001 included")
}

// A database Plan 7a's Migrate made has a schema_migrations table without
// readers; each migration it lists gets its own level.
func TestAnOldMigrationsTableGainsItsReaders(t *testing.T) {
	pool := freshDatabase(t, "migrate_old_table")
	_, err := pool.Exec(t.Context(), `CREATE TABLE schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
INSERT INTO schema_migrations (name) VALUES ('0001_a.sql'), ('0002_b.sql'), ('0003_c.sql')`)
	require.NoError(t, err)
	// Each would fail if it ran again: the table records them as applied.
	failing := &fstest.MapFile{Data: []byte("SELECT 1/0;\n")}
	fsys := fstest.MapFS{"0001_a.sql": failing, "0002_b.sql": failing, "0003_c.sql": failing}

	res, err := db.MigrateFS(t.Context(), pool, fsys, db.MigrateOptions{})

	require.NoError(t, err)
	assert.Empty(t, res.Applied)
	assert.Equal(t, map[string]int{"0001_a.sql": 1, "0002_b.sql": 2, "0003_c.sql": 3}, readers(t, pool))
	assert.Equal(t, db.Schema{Level: 3, Last: "0003_c.sql", MinReader: 3}, res.Schema)
}

// later is a migration at its own level, which no build before it can read.
const later = "CREATE TABLE later (id text PRIMARY KEY);\n"

func TestMigrateRefusesWhatOlderBuildsCannotReadWhileTheLeaseIsHeld(t *testing.T) {
	pool := freshDatabase(t, "migrate_guarded")
	require.NoError(t, db.Migrate(t.Context(), pool))
	hold(t, pool, "instance-a", time.Minute)
	fsys := withEmbedded(t, "0099_later.sql", later)

	_, err := db.MigrateFS(t.Context(), pool, fsys, db.MigrateOptions{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "instance-a")
	assert.Contains(t, err.Error(), "--force")
	assert.NotContains(t, readers(t, pool), "0099_later.sql")
	assert.False(t, exists(t, pool, "later"))

	res, err := db.MigrateFS(t.Context(), pool, fsys, db.MigrateOptions{Force: true})

	require.NoError(t, err)
	assert.Equal(t, []string{"0099_later.sql"}, res.Applied)
	assert.Equal(t, db.Schema{Level: 99, Last: "0099_later.sql", MinReader: 99}, res.Schema)
	assert.True(t, exists(t, pool, "later"))
}

func TestMigrateAppliesWhatOlderBuildsCanReadWhileTheLeaseIsHeld(t *testing.T) {
	pool := freshDatabase(t, "migrate_compatible")
	require.NoError(t, db.Migrate(t.Context(), pool))
	hold(t, pool, "instance-a", time.Minute)
	script := fmt.Sprintf("-- agentswarm:min_reader %d\nALTER TABLE leases ADD COLUMN note text;\n", db.Level())

	res, err := db.MigrateFS(t.Context(), pool, withEmbedded(t, "0099_note.sql", script), db.MigrateOptions{})

	require.NoError(t, err)
	assert.Equal(t, []string{"0099_note.sql"}, res.Applied)
	assert.Equal(t, db.Schema{Level: 99, Last: "0099_note.sql", MinReader: db.Level()}, res.Schema)
	assert.Equal(t, db.Compatible, res.Schema.State(db.Level()), "the holder's build still runs on the schema")
}

func TestMigrateIgnoresAnExpiredLease(t *testing.T) {
	pool := freshDatabase(t, "migrate_expired")
	require.NoError(t, db.Migrate(t.Context(), pool))
	hold(t, pool, "instance-a", -time.Minute)

	res, err := db.MigrateFS(t.Context(), pool, withEmbedded(t, "0099_later.sql", later), db.MigrateOptions{})

	require.NoError(t, err)
	assert.Equal(t, []string{"0099_later.sql"}, res.Applied)
	assert.True(t, exists(t, pool, "later"))
}

func TestMigrateGivesUpOnALockItCannotTake(t *testing.T) {
	pool := freshDatabase(t, "migrate_lock_timeout")
	fsys := fstest.MapFS{"0001_things.sql": {Data: []byte(things)}}
	_, err := db.MigrateFS(t.Context(), pool, fsys, db.MigrateOptions{})
	require.NoError(t, err)
	holder, err := pool.Begin(t.Context())
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(context.Background()) }()
	_, err = holder.Exec(t.Context(), "LOCK TABLE things IN ACCESS EXCLUSIVE MODE")
	require.NoError(t, err)
	fsys["0002_note.sql"] = &fstest.MapFile{Data: []byte("ALTER TABLE things ADD COLUMN note text;\n")}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	start := time.Now()
	_, err = db.MigrateFS(ctx, pool, fsys, db.MigrateOptions{LockTimeout: 200 * time.Millisecond})
	took := time.Since(start)

	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "55P03", pgErr.Code, "lock_not_available")
	assert.Less(t, took, 2*time.Second)
	require.NoError(t, holder.Rollback(t.Context()))
	assert.NotContains(t, readers(t, pool), "0002_note.sql")
}

// The checker tells a build whether it runs on the schema: not on one never
// migrated, which is behind, nor on one a later migration needs newer
// readers for; but on a later one it can read.
func TestTheCheckerTellsWhetherThisBuildRunsOnTheSchema(t *testing.T) {
	pool := freshDatabase(t, "migrate_checker")
	c := db.NewChecker(pool)

	state, err := c.State(t.Context())
	require.NoError(t, err)
	assert.Equal(t, db.Behind, state, "a database never migrated")
	err = c.Check(t.Context())
	require.ErrorIs(t, err, db.ErrIncompatible)
	assert.Contains(t, err.Error(), "behind")

	require.NoError(t, db.Migrate(t.Context(), pool))
	state, err = c.State(t.Context())
	require.NoError(t, err)
	assert.Equal(t, db.Compatible, state)
	require.NoError(t, c.Check(t.Context()))

	_, err = pool.Exec(t.Context(), "INSERT INTO schema_migrations (name, min_reader) VALUES ('0099_later.sql', $1)", db.Level())
	require.NoError(t, err)
	state, err = c.State(t.Context())
	require.NoError(t, err)
	assert.Equal(t, db.Compatible, state, "a later schema this build can read")

	_, err = pool.Exec(t.Context(), "INSERT INTO schema_migrations (name, min_reader) VALUES ('0100_later.sql', 100)")
	require.NoError(t, err)
	state, err = c.State(t.Context())
	require.NoError(t, err)
	assert.Equal(t, db.Incompatible, state)
	err = c.Check(t.Context())
	require.ErrorIs(t, err, db.ErrIncompatible)
	assert.Contains(t, err.Error(), "incompatible")
	assert.Contains(t, err.Error(), "level 100")
	assert.Contains(t, err.Error(), "min_reader 100")
	assert.Contains(t, err.Error(), fmt.Sprintf("level %d", db.Level()), "this build's")
}
