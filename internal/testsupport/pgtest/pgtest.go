// Package pgtest runs a disposable Postgres container for integration tests.
//
// A test package calls Main from its TestMain, so that its tests share one
// container, and Pool from each test that needs a database. Tests share the
// database, so each test uses its own run ids.
package pgtest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/jchen0426/agent-swarm/internal/db"
)

// Image is the Postgres image the integration tests run against.
const Image = "postgres:17-alpine"

var url string

// Main starts one Postgres container, runs the package's tests and removes
// the container. Use it as: func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }.
func Main(m *testing.M) int {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("agentswarm"),
		postgres.WithUsername("agentswarm"),
		postgres.WithPassword("agentswarm"),
		postgres.BasicWaitStrategies(),
	)
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			slog.ErrorContext(ctx, "remove postgres container", "error", err)
		}
	}()
	if err != nil {
		slog.ErrorContext(ctx, "start postgres container", "error", err)
		return 1
	}
	url, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		slog.ErrorContext(ctx, "read postgres connection string", "error", err)
		return 1
	}
	return m.Run()
}

// Pool returns a connection pool to the package's container, with every
// migration applied. The pool closes when the test ends.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	require.NotEmpty(t, url, "call pgtest.Main from TestMain first")
	pool, err := db.Open(t.Context(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, db.Migrate(t.Context(), pool))
	return pool
}
