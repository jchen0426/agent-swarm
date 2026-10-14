// Package postgres stores the Registry context's records in Postgres. It
// implements the ports that the Registry application declares.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// ErrConflict is returned when a commit was recorded with other content.
var ErrConflict = errors.New("postgres: the commit was recorded with another digest")

// Revisions records loaded configuration revisions in the config_revisions
// and config_objects tables. It implements app.Revisions.
type Revisions struct {
	pool   *pgxpool.Pool
	tenant string
}

// NewRevisions returns the record of one tenant's revisions.
func NewRevisions(pool *pgxpool.Pool, tenant string) *Revisions {
	return &Revisions{pool: pool, tenant: tenant}
}

// Record records a revision and its objects. Recording a commit again changes
// nothing. A commit recorded with another digest is refused, because one
// commit has one content.
func (s *Revisions) Record(ctx context.Context, rev *config.Revision) (err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin record: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()
	tag, err := tx.Exec(ctx, `
INSERT INTO config_revisions (tenant_id, commit_id, digest) VALUES ($1, $2, $3)
ON CONFLICT (tenant_id, commit_id) DO NOTHING`, s.tenant, rev.Commit(), rev.Digest())
	if err != nil {
		return fmt.Errorf("record revision %s: %w", rev.Commit(), err)
	}
	if tag.RowsAffected() == 0 {
		var recorded string
		if err = tx.QueryRow(ctx, `SELECT digest FROM config_revisions WHERE tenant_id = $1 AND commit_id = $2`,
			s.tenant, rev.Commit()).Scan(&recorded); err != nil {
			return fmt.Errorf("read revision %s: %w", rev.Commit(), err)
		}
		if recorded != rev.Digest() {
			err = fmt.Errorf("%w: %s has %s, not %s", ErrConflict, rev.Commit(), recorded, rev.Digest())
			return err
		}
		return tx.Commit(ctx)
	}
	rows := make([][]any, 0, len(rev.Objects()))
	for _, o := range rev.Objects() {
		rows = append(rows, []any{s.tenant, rev.Commit(), string(o.Kind), o.Name, o.Owner, o.File, o.Hash, o.Version})
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"config_objects"},
		[]string{"tenant_id", "commit_id", "kind", "name", "owner", "file", "hash", "version"},
		pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("record objects of %s: %w", rev.Commit(), err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit record of %s: %w", rev.Commit(), err)
	}
	return nil
}

// Objects returns the recorded objects of a commit, sorted by kind and name,
// or nothing when the commit was never recorded.
func (s *Revisions) Objects(ctx context.Context, commit string) ([]config.Object, error) {
	rows, err := s.pool.Query(ctx, `
SELECT kind, name, owner, file, hash, version FROM config_objects
WHERE tenant_id = $1 AND commit_id = $2
ORDER BY kind, name`, s.tenant, commit)
	if err != nil {
		return nil, fmt.Errorf("read objects of %s: %w", commit, err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (config.Object, error) {
		var o config.Object
		var kind string
		err := row.Scan(&kind, &o.Name, &o.Owner, &o.File, &o.Hash, &o.Version)
		o.Kind = config.Kind(kind)
		return o, err
	})
}
