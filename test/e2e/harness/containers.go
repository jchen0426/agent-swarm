//go:build e2e

package harness

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// databaseEnv names a database that the harness uses instead of starting
	// its own, so that a later build can read the rows an earlier one left
	// (R17). databaseContainerEnv names the container that database runs in,
	// which Psql needs.
	databaseEnv          = "AGENTSWARM_E2E_DATABASE"
	databaseContainerEnv = "AGENTSWARM_E2E_DATABASE_CONTAINER"

	defaultPostgresImage = "postgres:17-alpine"
	postgresUser         = "agentswarm"
	postgresPassword     = "agentswarm"
	postgresName         = "agentswarm"
	postgresAlias        = "postgres"
	postgresPort         = "5432/tcp"
	postgresData         = "/var/lib/postgresql/data"

	// projectLabel marks every container the harness starts as this
	// project's, for make test-clean. dataLabel names the path of the data a
	// container holds that is worth keeping (Decision 16).
	projectLabel = "agentswarm.project"
	projectName  = "agent-swarm"
	dataLabel    = "agentswarm.data"

	// startTimeout bounds the start of the harness's containers, an image
	// pull included.
	startTimeout = 5 * time.Minute
	// stopTimeout bounds the removal of a container or a network.
	stopTimeout = time.Minute
)

// database is the database the harness's instances use.
type database struct {
	// url reaches the database from the test process.
	url string
	// container is the container psql runs in.
	container string
	user      string
	password  string
	name      string
	// external is set when the database was given, not started.
	external bool
}

// postgresURL returns the URL of the database at an address.
func postgresURL(addr, user, password, name string) string {
	u := url.URL{
		Scheme: "postgres", User: url.UserPassword(user, password), Host: addr, Path: "/" + name,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

// externalDatabase describes the database a deployment of the harness was
// given. Its errors never quote the URL, which holds a password.
func externalDatabase(rawURL, container string) (*database, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, errors.New(databaseEnv + " is not a database URL")
	}
	d := &database{url: rawURL, container: container, external: true, user: u.User.Username(), name: strings.TrimPrefix(u.Path, "/")}
	d.password, _ = u.User.Password()
	if d.user == "" {
		d.user = "postgres"
	}
	if d.name == "" {
		d.name = d.user
	}
	return d, nil
}

// libpqEnv returns the environment of a process that reaches the database
// through the libpq environment variables: environ without its own PG
// variables, and the database's. Its errors never quote the URL.
func (d *database) libpqEnv(environ []string) ([]string, error) {
	u, err := url.Parse(d.url)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("the database URL names no host")
	}
	env := make([]string, 0, len(environ)+6)
	for _, kv := range environ {
		if !strings.HasPrefix(kv, "PG") {
			env = append(env, kv)
		}
	}
	env = append(env, "PGHOST="+u.Hostname(), "PGPORT="+cmp.Or(u.Port(), "5432"),
		"PGUSER="+d.user, "PGPASSWORD="+d.password, "PGDATABASE="+d.name)
	if mode := u.Query().Get("sslmode"); mode != "" {
		env = append(env, "PGSSLMODE="+mode)
	}
	return env, nil
}

// startNetwork starts the network the harness's containers share, and removes
// it when the test ends, after the containers.
func startNetwork(t testing.TB) *testcontainers.DockerNetwork {
	t.Helper()
	nw, err := network.New(t.Context(), network.WithLabels(map[string]string{projectLabel: projectName}))
	if err != nil {
		t.Fatalf("harness: create the network (Docker must run): %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		if err := nw.Remove(ctx); err != nil {
			t.Logf("harness: remove the network %s: %v", nw.Name, err)
		}
	})
	return nw
}

// startPostgres starts a Postgres on the network, labelled for make
// test-clean, and returns once it accepts connections.
func startPostgres(ctx context.Context, image, networkName string) (*testcontainers.DockerContainer, error) {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	return testcontainers.Run(ctx, image,
		testcontainers.WithEnv(map[string]string{
			"POSTGRES_USER": postgresUser, "POSTGRES_PASSWORD": postgresPassword, "POSTGRES_DB": postgresName,
		}),
		testcontainers.WithExposedPorts(postgresPort),
		testcontainers.WithLabels(map[string]string{projectLabel: projectName, dataLabel: postgresData}),
		network.WithNetworkName([]string{postgresAlias}, networkName),
		// The image restarts the server once after it initialized the data.
		testcontainers.WithAdditionalWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			wait.ForListeningPort(postgresPort),
		),
	)
}

// terminateOnCleanup removes the container when the test ends.
func terminateOnCleanup(t testing.TB, ctr testcontainers.Container) {
	t.Helper()
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(ctr, testcontainers.StopContext(context.Background())); err != nil {
			t.Logf("harness: remove a container: %v", err)
		}
	})
}

// psql runs the SQL in the database's container, through docker exec, and
// returns what psql prints: rows unaligned, without a header or a count.
func (d *database) psql(ctx context.Context, sql string) (string, error) {
	if d.container == "" {
		return "", fmt.Errorf("a given database needs %s to name the container psql runs in", databaseContainerEnv)
	}
	args := []string{"exec", "-i"}
	if d.password != "" {
		args = append(args, "-e", "PGPASSWORD="+d.password)
	}
	args = append(args, d.container, "psql", "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1",
		"-h", "127.0.0.1", "-U", d.user, "-d", d.name, "-f", "-")
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // G204: docker's own arguments, and the container the test names.
	cmd.Stdin = strings.NewReader(sql)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("psql: %w\n%s", err, strings.TrimSpace(errOut.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Psql runs the SQL with psql in the database's container, through docker
// exec, and returns what psql prints: the rows of every statement, unaligned
// and without a header, one to a line, columns divided by |. The test fails
// when psql does.
func (e *Env) Psql(t *testing.T, sql string) string {
	t.Helper()
	if e.db == nil {
		t.Fatal("harness: there is no database: Options.NoDatabase is set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	out, err := e.db.psql(ctx, sql)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return out
}
