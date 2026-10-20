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

var (
	url         string
	containerID string
)

// Main starts one Postgres container, runs the package's tests and removes
// the container. Use it as: func TestMain(m *testing.M) { os.Exit(pgtest.Main(m)) }.
//
// The container carries labels for make test-clean, which removes a container
// that a killed test run left behind: agentswarm.project marks it as this
// project's, and agentswarm.data names the path whose files test-clean saves
// first.
func Main(m *testing.M) int {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("agentswarm"),
		postgres.WithUsername("agentswarm"),
		postgres.WithPassword("agentswarm"),
		testcontainers.WithLabels(map[string]string{
			"agentswarm.project": "agent-swarm",
			"agentswarm.data":    "/var/lib/postgresql/data",
		}),
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
	containerID = ctr.GetContainerID()
	url, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		slog.ErrorContext(ctx, "read postgres connection string", "error", err)
		return 1
	}
	return m.Run()
}

// ContainerID returns the id of the package's container, for tests of the
// container itself.
func ContainerID() string { return containerID }

// Pool returns a connection pool to the package's container, with every
// migration applied. The pool closes when the test ends.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return PoolWithParams(t, nil)
}

// URL returns the connection string of the package's container, with every
// migration applied, for code that opens its own pool.
func URL(t *testing.T) string {
	t.Helper()
	Pool(t)
	return url
}

// PoolWithParams is Pool with extra run-time parameters on every connection,
// such as a different default_transaction_isolation.
func PoolWithParams(t *testing.T, params map[string]string) *pgxpool.Pool {
	t.Helper()
	require.NotEmpty(t, url, "call pgtest.Main from TestMain first")
	cfg, err := pgxpool.ParseConfig(url)
	require.NoError(t, err)
	for name, value := range params {
		cfg.ConnConfig.RuntimeParams[name] = value
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, db.Migrate(t.Context(), pool))
	return pool
}
