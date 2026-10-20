//go:build e2e

// Package harness is the fault-injection harness of the end-to-end tests
// (Plan 7d-1, Decision 17). It drives the built agentswarm binary from outside
// and imports no platform code.
//
// An Env holds what a scenario runs against: a Postgres, and with
// Options.Toxiproxy a toxiproxy that puts one proxy in front of it for each
// serve instance, both in labelled containers; a test CA; a scripted model
// vendor; and scripted MCP servers. Config writes a configuration repository,
// Deployment a deployment file for an instance, and StartParent runs a process
// under a parent that ends it when the test process ends.
package harness

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

const (
	// BinaryEnv names the agentswarm binary the harness runs. make test-e2e
	// sets it to the binary it built; the tests' TestMain builds one and sets
	// it otherwise.
	BinaryEnv = "AGENTSWARM_BIN"
	// ParentEnv makes a copy of the test binary run as the parent of a
	// process (R20): TestMain calls ParentMain when it is set.
	ParentEnv = "AGENTSWARM_E2E_PARENT"

	// migrateBinaryEnv names a binary whose migrate command Migrate runs
	// instead of the binary under test's, so that a later build can migrate
	// the schema whose rows an earlier build's scenarios left (R17).
	migrateBinaryEnv = "AGENTSWARM_MIGRATE_BIN"
	// tenantID is the tenant of every deployment the harness writes, whose
	// audit chain AuditVerify verifies.
	tenantID = "harness"
	// exitFindings is the code agentswarm audit verify exits with when it
	// ran to its end and found something.
	exitFindings = 3
	// commandTimeout bounds a command the harness runs on its own account.
	commandTimeout = 2 * time.Minute
)

// instanceName is what an instance's name may be: it names a directory and a
// proxy, and is the holder the lease records.
var instanceName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// Options says what New starts.
type Options struct {
	// Postgres is the Postgres image; "" means postgres:17-alpine.
	Postgres string
	// Toxiproxy starts a toxiproxy, with a proxy in front of Postgres for each
	// instance that asks for its database URL.
	Toxiproxy bool
	// NoDatabase starts neither Postgres nor a toxiproxy, for the tests that
	// use the scripted vendor and servers alone, such as those of the
	// runtime: a Postgres takes seconds to start. Psql, DatabaseURL, Cut,
	// Restore and Deployment then fail the test.
	NoDatabase bool
	// Servers are the scripted MCP servers.
	Servers []ServerSpec
}

// Env is a harness: what a scenario runs against.
type Env struct {
	t   testing.TB
	dir string

	caFile string
	vendor *Vendor
	// servers are the scripted MCP servers, by name; order is the order of
	// Options.Servers.
	servers map[string]*MCPServer
	order   []string

	// db is nil under Options.NoDatabase.
	db       *database
	postgres *testcontainers.DockerContainer
	tox      *toxiproxy

	mu        sync.Mutex
	repo      *Repo
	configs   int
	instances map[string]*instance
}

// New starts a harness: the test CA, the scripted vendor and the scripted MCP
// servers, and, unless Options.NoDatabase is set, Postgres and, with
// Options.Toxiproxy, a toxiproxy, on one network, labelled
// agentswarm.project=agent-swarm (Postgres also agentswarm.data) for make
// test-clean. When AGENTSWARM_E2E_DATABASE holds a database URL, it starts
// neither Postgres nor a toxiproxy and uses that database (R17); psql then
// runs in the container AGENTSWARM_E2E_DATABASE_CONTAINER names. t.Cleanup
// stops all of it. The methods that take no test of their own, such as
// DatabaseURL and Server, fail the test passed to New, and so are called from
// the goroutine that runs it.
func New(t *testing.T, opts Options) *Env {
	t.Helper()
	e := &Env{t: t, dir: t.TempDir(), servers: map[string]*MCPServer{}, instances: map[string]*instance{}}
	ca, err := newTestCA()
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	e.caFile = filepath.Join(e.dir, "ca.pem")
	if err := os.WriteFile(e.caFile, ca.pem, 0o600); err != nil {
		t.Fatalf("harness: write the CA certificate: %v", err)
	}
	e.vendor = newVendor(t)
	for i := range opts.Servers {
		name := opts.Servers[i].Name
		if !serverName.MatchString(name) {
			t.Fatalf("harness: %q is not a server name: a lowercase DNS label", name)
		}
		if e.servers[name] != nil {
			t.Fatalf("harness: server %q is listed twice", name)
		}
		e.servers[name] = newMCPServer(t, &opts.Servers[i], ca)
		e.order = append(e.order, name)
	}
	if !opts.NoDatabase {
		e.startDatabase(t, &opts)
	}
	return e
}

// startDatabase starts the containers, or takes the database it was given.
func (e *Env) startDatabase(t testing.TB, opts *Options) {
	t.Helper()
	if raw := os.Getenv(databaseEnv); raw != "" {
		d, err := externalDatabase(raw, os.Getenv(databaseContainerEnv))
		if err != nil {
			t.Fatalf("harness: %v", err)
		}
		e.db = d
		return
	}
	nw := startNetwork(t)
	ctx := t.Context()
	var (
		wg     sync.WaitGroup
		pg     *testcontainers.DockerContainer
		pgErr  error
		tox    *toxiproxy
		toxErr error
	)
	wg.Go(func() { pg, pgErr = startPostgres(ctx, cmp.Or(opts.Postgres, defaultPostgresImage), nw.Name) })
	if opts.Toxiproxy {
		wg.Go(func() { tox, toxErr = startToxiproxy(ctx, nw.Name) })
	}
	wg.Wait()
	// Whatever started is removed, whether or not everything did.
	terminateOnCleanup(t, pg)
	if tox != nil {
		terminateOnCleanup(t, tox.container)
	}
	if pgErr != nil {
		t.Fatalf("harness: start Postgres: %v", pgErr)
	}
	if toxErr != nil {
		t.Fatalf("harness: start the toxiproxy: %v", toxErr)
	}
	addr, err := pg.PortEndpoint(ctx, postgresPort, "")
	if err != nil {
		t.Fatalf("harness: find Postgres' port: %v", err)
	}
	e.postgres, e.tox = pg, tox
	e.db = &database{
		url:       postgresURL(addr, postgresUser, postgresPassword, postgresName),
		container: pg.GetContainerID(),
		user:      postgresUser, password: postgresPassword, name: postgresName,
	}
}

// PostgresID returns the id of the Postgres container, or "" when the harness
// started none.
func (e *Env) PostgresID() string {
	if e.postgres == nil {
		return ""
	}
	return e.postgres.GetContainerID()
}

// ToxiproxyID returns the id of the toxiproxy container, or "" when the
// harness started none.
func (e *Env) ToxiproxyID() string {
	if e.tox == nil || e.tox.container == nil {
		return ""
	}
	return e.tox.container.GetContainerID()
}

// DatabaseURL returns the URL of the database for an instance: through the
// instance's proxy when there is a toxiproxy, which Cut and Restore act on,
// and the database itself otherwise. The instance "" always reaches the
// database itself, for what the harness does on its own account. A database
// the harness was given is the same for every instance. It fails the test
// passed to New; Deployment, which takes a test of its own, fails that one.
func (e *Env) DatabaseURL(instance string) string { return e.databaseURL(e.t, instance) }

// databaseURL is DatabaseURL, failing the test it is given.
func (e *Env) databaseURL(t testing.TB, instance string) string {
	t.Helper()
	if e.db == nil {
		t.Fatal("harness: there is no database: Options.NoDatabase is set")
	}
	if e.tox == nil || instance == "" {
		return e.db.url
	}
	p, err := e.tox.proxyFor(t.Context(), instance)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return postgresURL(p.addr, e.db.user, e.db.password, e.db.name)
}

// Vendor returns the scripted model vendor.
func (e *Env) Vendor() *Vendor { return e.vendor }

// Server returns the scripted MCP server of the name. A name it does not know
// fails the test passed to New.
func (e *Env) Server(name string) *MCPServer {
	s := e.servers[name]
	if s == nil {
		e.t.Fatalf("harness: there is no scripted server %q", name)
	}
	return s
}

// CA returns the path of the PEM file of the test CA's certificate, which
// signs the scripted servers' certificates.
func (e *Env) CA() string { return e.caFile }

// Migrate runs agentswarm migrate on the harness's database, which the libpq
// environment variables name, and returns what it printed: a line for each
// migration it applied, then the schema's level. It runs the binary
// AGENTSWARM_MIGRATE_BIN names when that is set (R17), and the binary under
// test otherwise. The test fails when migrate does.
func (e *Env) Migrate(t *testing.T) string {
	t.Helper()
	bin := os.Getenv(migrateBinaryEnv)
	if bin == "" {
		bin = binary(t)
	}
	out, errOut, code := e.runOnDatabase(t, bin, "migrate")
	if code != 0 {
		t.Fatalf("harness: agentswarm migrate exited %d:\n%s", code, errOut)
	}
	return out
}

// AuditVerify anchors the audit chain of the harness's tenant in a file
// store, a new directory, with agentswarm audit anchor, then verifies it with
// agentswarm audit verify, and returns the kinds of the findings verify
// reports, in its order. A file store cannot keep what it holds write-once,
// so a chain that verifies gives store_unlocked and no other finding (R22).
// The test fails when either command does not run to its end.
func (e *Env) AuditVerify(t *testing.T) []string {
	t.Helper()
	dir, err := os.MkdirTemp(e.dir, "anchors-")
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	bin := binary(t)
	flags := []string{"--tenant", tenantID, "--anchors", "file://" + dir}
	if _, errOut, code := e.runOnDatabase(t, bin, append([]string{"audit", "anchor"}, flags...)...); code != 0 {
		t.Fatalf("harness: agentswarm audit anchor exited %d:\n%s", code, errOut)
	}
	out, errOut, code := e.runOnDatabase(t, bin, append([]string{"audit", "verify"}, flags...)...)
	if code != 0 && code != exitFindings {
		t.Fatalf("harness: agentswarm audit verify exited %d:\n%s", code, errOut)
	}
	var report struct {
		Findings []struct {
			Kind string `json:"kind"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("harness: read what audit verify printed: %v\n%s", err, out)
	}
	kinds := make([]string, 0, len(report.Findings))
	for _, f := range report.Findings {
		kinds = append(kinds, f.Kind)
	}
	return kinds
}

// runOnDatabase runs a binary with the libpq environment variables naming the
// harness's database, and returns its output and exit code. The test fails
// when the binary does not start, or does not end within commandTimeout.
func (e *Env) runOnDatabase(t *testing.T, bin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	if e.db == nil {
		t.Fatal("harness: there is no database: Options.NoDatabase is set")
	}
	env, err := e.db.libpqEnv(os.Environ())
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // G204: the binary under test, with the harness's arguments.
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || ctx.Err() != nil) {
		t.Fatalf("harness: run %s %s: %v\n%s", bin, strings.Join(args, " "), err, errOut.String())
	}
	return out.String(), errOut.String(), cmd.ProcessState.ExitCode()
}

// binary returns the agentswarm binary the harness runs.
func binary(t testing.TB) string {
	t.Helper()
	path := os.Getenv(BinaryEnv)
	if path == "" {
		t.Fatalf("harness: %s must name the agentswarm binary", BinaryEnv)
	}
	return path
}

// randomHex returns n random bytes in hexadecimal.
func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// instance is what the harness keeps of a serve instance: the files of its
// deployment.
type instance struct {
	name       string
	dir        string
	deployment string
	secrets    string
	output     string
	addresses  string
	adminToken string
}

// checkInstance fails the test for a name that cannot name an instance.
func checkInstance(t testing.TB, name string) {
	t.Helper()
	if !instanceName.MatchString(name) {
		t.Fatalf("harness: %q is not an instance name: lowercase letters, digits and dashes", name)
	}
}
