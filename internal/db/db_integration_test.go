//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
)

func TestMigrateTwiceChangesNothing(t *testing.T) {
	pool := pgtest.Pool(t)

	require.NoError(t, db.Migrate(t.Context(), pool))

	var applied int
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM schema_migrations").Scan(&applied))
	assert.Equal(t, 1, applied)
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
