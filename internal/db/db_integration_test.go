//go:build integration

package db_test

import (
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// The migration that chains the audited tables, run while writers insert
// into them, leaves no row unchained: it locks them against writes from the
// rows it chains until its triggers are in place.
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
	_, err = pool.Exec(t.Context(), "CREATE TABLE schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())")
	require.NoError(t, err)
	for _, f := range files[:len(files)-1] {
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
