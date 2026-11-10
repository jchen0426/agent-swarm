//go:build e2e

package harness

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	dockercontainer "github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
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
// test-clean, and returns once it accepts connections. With loopback its
// port is published on 127.0.0.1 alone (Options.Loopback); otherwise as the
// engine publishes it by default.
func startPostgres(ctx context.Context, image, networkName string, loopback bool) (*testcontainers.DockerContainer, error) {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	opts := []testcontainers.ContainerCustomizer{
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
	}
	if loopback {
		// A binding of the exposed port is kept as it is given, its host
		// port, left empty, allocated by the engine.
		opts = append(opts, testcontainers.WithHostConfigModifier(func(hc *dockercontainer.HostConfig) {
			hc.PortBindings = dockernetwork.PortMap{
				dockernetwork.MustParsePort(postgresPort): {{HostIP: netip.MustParseAddr("127.0.0.1")}},
			}
		}))
	}
	return testcontainers.Run(ctx, image, opts...)
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
// when psql does. Its deadline, a minute, is its own, not the test's
// context's, so that a test's cleanup, which runs once that context has ended,
// can put back a database the test was given (R17).
func (e *Env) Psql(t *testing.T, sql string) string {
	t.Helper()
	if e.db == nil {
		t.Fatal("harness: there is no database: Options.NoDatabase is set")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
	defer cancel()
	out, err := e.db.psql(ctx, sql)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return out
}

// Gone reports whether a started turn is over (Contract C9): its process
// gone, under the process runner, or under Docker its container not running
// or removed, as docker inspect shows it. The harness checks containers with
// the docker command line, as the depguard rule e2e forbids the engine's
// client. An engine it cannot ask fails the test, so Gone runs on the test's
// goroutine.
func Gone(t *testing.T, s TurnStarted) bool {
	t.Helper()
	switch {
	case s.Container != "":
		running, found := inspectContainer(t, s.Container, "{{.State.Running}}")
		return !found || strings.TrimSpace(running) == "false"
	case s.PID > 0:
		return errors.Is(syscall.Kill(s.PID, 0), syscall.ESRCH)
	}
	t.Fatalf("harness: turn %s was logged as started with neither a process nor a container", s.Turn)
	return false
}

// ContainerConfig returns docker inspect's output of a container, its
// configuration and its state among it, or "" once it is removed (Contract
// C9). An engine it cannot ask fails the test, so it runs on the test's
// goroutine.
func ContainerConfig(t *testing.T, id string) string {
	t.Helper()
	config, _ := inspectContainer(t, id, "")
	return config
}

// inspectContainer runs docker container inspect of the container id, with
// format when it is not "", and returns what it printed, and false for a
// container the engine does not have, as once it is removed. Any other
// failure fails the test. Its deadline is its own, as Psql's is.
func inspectContainer(t *testing.T, id, format string) (string, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
	defer cancel()
	args := []string{"container", "inspect"}
	if format != "" {
		args = append(args, "--format", format)
	}
	cmd := exec.CommandContext(ctx, "docker", append(args, "--", id)...) //nolint:gosec // G204: docker's own arguments, and a container serve logged.
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if strings.Contains(errOut.String(), "No such container") {
			return "", false
		}
		t.Fatalf("harness: docker container inspect %s: %v\n%s", id, err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), true
}
