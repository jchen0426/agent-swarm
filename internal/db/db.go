// Package db opens the platform's Postgres database and applies its schema
// migrations.
//
// A migration's level is its number, the digits before the first underscore
// of its file name (NNNN_name.sql). Each migration may declare the oldest
// build that can read the schema it leaves, its min_reader (Contract C8);
// schema_migrations records it beside the migration's name. A build's level
// is its last migration's, and Schema.State tells whether a build runs on a
// schema (Plan 7d-1, Decision 18).
package db

import (
	"cmp"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationLock is the advisory lock that serializes concurrent migration runs.
const migrationLock = 7342601

// DefaultLockTimeout is how long a migration run waits for each lock unless
// told otherwise.
const DefaultLockTimeout = 5 * time.Second

// readCommitted is the isolation of a migration's transaction, whatever the
// database's default: the audit chain is written only there.
var readCommitted = pgx.TxOptions{IsoLevel: pgx.ReadCommitted}

// Open connects to Postgres and checks the connection. An empty url takes
// the libpq environment variables, such as PGHOST and PGUSER. Every
// connection runs at read committed whatever the database's default, since
// the audit chain is written only there, and ends a transaction left idle
// for a minute, which would otherwise hold the chain's lock.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["default_transaction_isolation"] = "read committed"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "60000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// MigrateOptions are a migration run's.
type MigrateOptions struct {
	// LockTimeout bounds each wait for a lock; zero means [5 s].
	LockTimeout time.Duration
	// Force applies a migration older builds cannot read while the
	// platform's lease is held.
	Force bool
}

// MigrateResult says what a migration run did.
type MigrateResult struct {
	Applied []string // the migrations applied, in order
	Schema  Schema   // the schema afterwards
}

// Migrations returns the migrations this build embeds, at the root of the
// file system.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // "migrations" is a valid path, so this never happens.
	}
	return sub
}

// Migrate applies the embedded migrations with the default options.
// Running it again changes nothing.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := MigrateFS(ctx, pool, Migrations(), MigrateOptions{})
	return err
}

// MigrateFS applies the migrations at the root of fsys that the database
// lacks, in the order of their levels, each in a transaction of its own that
// also records it and its min_reader. It reads and checks every file before
// it applies any, and refuses a migration numbered at or below the schema's
// level, which would apply out of order.
//
// One run at a time holds the migration lock, and each wait for a lock, that
// one's included, lasts opts.LockTimeout at most. A migration whose
// min_reader is above the schema's level when the run began is one that the
// build of a running instance cannot read: it is refused while an instance
// holds a lease, unless opts.Force is set.
//
// On an error, the result still names the migrations applied before it.
func MigrateFS(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, opts MigrateOptions) (MigrateResult, error) {
	all, err := readMigrations(fsys)
	if err != nil {
		return MigrateResult{}, err
	}
	if opts.LockTimeout < 0 {
		return MigrateResult{}, fmt.Errorf("a lock timeout of %v is below zero", opts.LockTimeout)
	}
	pc, err := pool.Acquire(ctx)
	if err != nil {
		return MigrateResult{}, fmt.Errorf("connect to migrate: %w", err)
	}
	// The run has its connection to itself, and closes it: the server then
	// lets the migration lock go, however the run ended.
	conn := pc.Hijack()
	defer func() { _ = conn.Close(ctx) }()
	if err := lockMigrations(ctx, conn, cmp.Or(opts.LockTimeout, DefaultLockTimeout)); err != nil {
		return MigrateResult{}, err
	}
	if err := prepare(ctx, conn); err != nil {
		return MigrateResult{}, err
	}
	applied, before, err := readApplied(ctx, conn)
	if err != nil {
		return MigrateResult{}, err
	}
	var pending []migration
	for _, m := range all {
		switch {
		case applied[m.name]:
		case m.level <= before.Level:
			return MigrateResult{}, fmt.Errorf("migration %s is at level %d, but the schema is at level %d (%s): a migration is numbered above every one applied",
				m.name, m.level, before.Level, before.Last)
		default:
			pending = append(pending, m)
		}
	}
	var res MigrateResult
	for i := range pending {
		if err := apply(ctx, conn, &pending[i], before.Level, opts.Force); err != nil {
			return res, err
		}
		res.Applied = append(res.Applied, pending[i].name)
	}
	if _, res.Schema, err = readApplied(ctx, conn); err != nil {
		return res, err
	}
	return res, nil
}

// lockMigrations bounds each wait of the session for a lock by timeout, and
// takes the migration lock, which the session holds until it ends.
func lockMigrations(ctx context.Context, conn *pgx.Conn, timeout time.Duration) error {
	// Below a millisecond, the setting would be 0: no timeout at all.
	ms := strconv.FormatInt(max(timeout.Milliseconds(), 1), 10) + "ms"
	if _, err := conn.Exec(ctx, "SELECT set_config('lock_timeout', $1, false)", ms); err != nil {
		return fmt.Errorf("set the lock timeout: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLock); err != nil {
		return fmt.Errorf("lock migrations, which another run may hold: %w", err)
	}
	return nil
}

// createMigrations makes schema_migrations: the name of each migration
// applied, and its min_reader.
const createMigrations = `CREATE TABLE IF NOT EXISTS schema_migrations (
	name       text        PRIMARY KEY,
	applied_at timestamptz NOT NULL DEFAULT now(),
	min_reader integer     NOT NULL CHECK (min_reader > 0)
)`

// addReaders gives the schema_migrations of a build before min_reader the
// column, each migration at its own level: those builds read no declaration.
const addReaders = `ALTER TABLE schema_migrations ADD COLUMN min_reader integer CHECK (min_reader > 0);
UPDATE schema_migrations SET min_reader = split_part(name, '_', 1)::integer;
ALTER TABLE schema_migrations ALTER COLUMN min_reader SET NOT NULL`

// prepare makes schema_migrations, or gives the one an older build made its
// readers. It alters the table only then, so that a run takes no lock that
// holds up an instance's check of the schema.
func prepare(ctx context.Context, conn *pgx.Conn) error {
	return pgx.BeginTxFunc(ctx, conn, readCommitted, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, createMigrations); err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}
		var has bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_attribute
WHERE attrelid = 'schema_migrations'::regclass AND attname = 'min_reader' AND NOT attisdropped)`).Scan(&has)
		if err != nil {
			return fmt.Errorf("read schema_migrations: %w", err)
		}
		if has {
			return nil
		}
		if _, err := tx.Exec(ctx, addReaders); err != nil {
			return fmt.Errorf("give schema_migrations its readers: %w", err)
		}
		return nil
	})
}

// querier runs a query: a pool or a connection.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// readApplied reads, in one query, the migrations the database has applied
// and the schema they leave.
func readApplied(ctx context.Context, q querier) (map[string]bool, Schema, error) {
	rows, err := q.Query(ctx, "SELECT name, min_reader FROM schema_migrations")
	if err != nil {
		return nil, Schema{}, fmt.Errorf("read schema_migrations: %w", err)
	}
	applied := map[string]bool{}
	var (
		s         Schema
		name      string
		minReader int
	)
	_, err = pgx.ForEachRow(rows, []any{&name, &minReader}, func() error {
		level, err := levelOf(name)
		if err != nil {
			return err
		}
		applied[name] = true
		if level > s.Level || level == s.Level && name > s.Last {
			s.Level, s.Last = level, name
		}
		s.MinReader = max(s.MinReader, minReader)
		return nil
	})
	if err != nil {
		return nil, Schema{}, fmt.Errorf("read schema_migrations: %w", err)
	}
	return applied, s, nil
}

// apply applies one migration and records it, in a transaction of its own.
// A migration whose min_reader is above level, the schema's when the run
// began, is one that older builds cannot read: guard holds it to the lease.
func apply(ctx context.Context, conn *pgx.Conn, m *migration, level int, force bool) error {
	return pgx.BeginTxFunc(ctx, conn, readCommitted, func(tx pgx.Tx) error {
		if m.minReader > level {
			if err := guard(ctx, tx, m, level, force); err != nil {
				return err
			}
		}
		// Without arguments, pgx sends the script over the simple protocol,
		// which allows several statements in one call.
		if _, err := tx.Exec(ctx, m.script); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (name, min_reader) VALUES ($1, $2)", m.name, m.minReader); err != nil {
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}
		return nil
	})
}

// guard refuses a migration older builds cannot read while an instance holds
// a lease, and so may run such a build, unless force is set. The schema's
// level when the run began stands for the build a holder runs: a build runs
// on no schema behind it. The guard first locks the leases against takes and
// renewals until the migration's transaction ends, so that no holding begins
// under it. A database without leases has no holder.
func guard(ctx context.Context, tx pgx.Tx, m *migration, level int, force bool) error {
	var leases bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass('leases') IS NOT NULL").Scan(&leases); err != nil {
		return fmt.Errorf("migration %s: look for leases: %w", m.name, err)
	}
	if !leases {
		return nil
	}
	if _, err := tx.Exec(ctx, "LOCK TABLE leases IN SHARE MODE"); err != nil {
		return fmt.Errorf("migration %s: lock the leases: %w", m.name, err)
	}
	rows, err := tx.Query(ctx, `SELECT holder, tenant_id, name, expires_at FROM leases
WHERE expires_at > clock_timestamp() ORDER BY tenant_id, name`)
	if err != nil {
		return fmt.Errorf("migration %s: read the leases: %w", m.name, err)
	}
	var (
		holders              []string
		holder, tenant, name string
		expires              time.Time
	)
	_, err = pgx.ForEachRow(rows, []any{&holder, &tenant, &name, &expires}, func() error {
		holders = append(holders, fmt.Sprintf("%s holds the lease %s/%s until %s", holder, tenant, name, expires.UTC().Format(time.RFC3339)))
		return nil
	})
	if err != nil {
		return fmt.Errorf("migration %s: read the leases: %w", m.name, err)
	}
	if len(holders) == 0 || force {
		return nil
	}
	return fmt.Errorf("migration %s needs readers at level %d, above the schema's level %d, and %s: stop the older instances, then migrate; or migrate under them with --force",
		m.name, m.minReader, level, strings.Join(holders, ", and "))
}

// migration is one migration file.
type migration struct {
	name      string // its file name, which schema_migrations records
	level     int    // its number
	minReader int    // the oldest build that can read the schema it leaves
	script    string
}

// readMigrations reads the migrations at the root of fsys, in the order of
// their levels, and checks each one's name and declaration.
func readMigrations(fsys fs.FS) ([]migration, error) {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	ms := make([]migration, 0, len(names))
	for _, name := range names {
		level, err := levelOf(name)
		if err != nil {
			return nil, err
		}
		script, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		minReader, err := declaredReader(string(script), level)
		if err != nil {
			return nil, fmt.Errorf("migration %s: %w", name, err)
		}
		ms = append(ms, migration{name: name, level: level, minReader: minReader, script: string(script)})
	}
	slices.SortFunc(ms, func(a, b migration) int { return cmp.Compare(a.level, b.level) })
	for i := 1; i < len(ms); i++ {
		if ms[i].level == ms[i-1].level {
			return nil, fmt.Errorf("migrations %s and %s are both at level %d", ms[i-1].name, ms[i].name, ms[i].level)
		}
	}
	return ms, nil
}

// levelOf returns the level a migration's file name gives it: the number
// before its first underscore.
func levelOf(name string) (int, error) {
	digits, _, ok := strings.Cut(name, "_")
	level, isNumber := number(digits)
	if !ok || !isNumber || level < 1 || !strings.HasSuffix(name, ".sql") {
		return 0, fmt.Errorf("%q is not a migration's name: NNNN_name.sql, where NNNN is its level, from 1", name)
	}
	return level, nil
}

// declaration begins the first line of a migration that builds older than
// itself can read (Contract C8): -- agentswarm:min_reader N.
const declaration = "-- agentswarm:min_reader"

// declaredReader returns a migration's min_reader: the one its first line
// declares, from 1 to its own level, or else its own level.
func declaredReader(script string, level int) (int, error) {
	first, _, _ := strings.Cut(script, "\n")
	first = strings.TrimSpace(first)
	rest, ok := strings.CutPrefix(first, declaration)
	if !ok {
		return level, nil
	}
	fields := strings.Fields(rest)
	n, isNumber := 0, false
	if len(fields) == 1 && rest != strings.TrimLeft(rest, " \t") {
		n, isNumber = number(fields[0])
	}
	switch {
	case !isNumber:
		return 0, fmt.Errorf("its first line, %q, is not %q and a number", first, declaration)
	case n < 1:
		return 0, fmt.Errorf("it declares min_reader %d, below 1", n)
	case n > level:
		return 0, fmt.Errorf("it declares min_reader %d, above its own level %d", n, level)
	}
	return n, nil
}

// number reads a decimal number written in digits alone.
func number(s string) (int, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}
