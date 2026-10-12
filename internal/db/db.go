// Package db opens the platform's Postgres database and applies its schema
// migrations.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationLock is the advisory lock that serializes concurrent migration runs.
const migrationLock = 7342601

// Open connects to Postgres and checks the connection.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// Migrate applies, in file-name order and in one transaction, every embedded
// migration that has not been applied yet. Running it again changes nothing.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (err error) {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	slices.Sort(names)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLock); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`
	if _, err = tx.Exec(ctx, createTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	for _, name := range names {
		if err := apply(ctx, tx, name); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

// apply runs one migration file unless it has run before.
func apply(ctx context.Context, tx pgx.Tx, name string) error {
	base := path.Base(name)
	var done bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)", base).Scan(&done); err != nil {
		return fmt.Errorf("check migration %s: %w", base, err)
	}
	if done {
		return nil
	}
	script, err := migrations.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read migration %s: %w", base, err)
	}
	// Without arguments, pgx sends the script over the simple protocol, which
	// allows several statements in one call.
	if _, err := tx.Exec(ctx, string(script)); err != nil {
		return fmt.Errorf("apply migration %s: %w", base, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (name) VALUES ($1)", base); err != nil {
		return fmt.Errorf("record migration %s: %w", base, err)
	}
	return nil
}
