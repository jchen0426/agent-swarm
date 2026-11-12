//go:build e2e

package harness

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A database the harness was given has no proxy in front of it, and a harness
// without a toxiproxy or without a database has nothing to cut.
func TestOnlyADatabaseBehindAToxiproxyCanBeCut(t *testing.T) {
	err := (&Env{db: &database{external: true}}).cutError()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be cut")
	assert.Contains(t, err.Error(), databaseEnv, "the message names the variable that gave the database")

	require.ErrorContains(t, (&Env{}).cutError(), "no database")
	require.ErrorContains(t, (&Env{db: &database{}}).cutError(), "Options.Toxiproxy")
	require.NoError(t, (&Env{db: &database{}, tox: &toxiproxy{}}).cutError())
}

func TestAGivenDatabaseIsReadFromItsURL(t *testing.T) {
	d, err := externalDatabase("postgres://alice:s3cret@db.example:5432/orders?sslmode=disable", "pg-1")
	require.NoError(t, err)
	assert.True(t, d.external)
	assert.Equal(t, "alice", d.user)
	assert.Equal(t, "s3cret", d.password)
	assert.Equal(t, "orders", d.name)
	assert.Equal(t, "pg-1", d.container)
	assert.Equal(t, "postgres://alice:s3cret@db.example:5432/orders?sslmode=disable", d.url)

	d, err = externalDatabase("postgres://db.example:5432", "pg-1")
	require.NoError(t, err)
	assert.Equal(t, "postgres", d.user, "the user a container's psql connects as when the URL names none")
	assert.Equal(t, "postgres", d.name, "and the database is named for the user")
}

func TestAGivenDatabaseIsNeverQuotedInAnError(t *testing.T) {
	_, err := externalDatabase("postgres://alice:topsecret@%zz", "pg-1")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "topsecret")
	assert.NotContains(t, err.Error(), "alice")
	assert.Contains(t, err.Error(), databaseEnv)
}

// The commands the harness runs on its own account find the database through
// the libpq environment variables, whatever PG variables the test process has.
func TestTheLibpqEnvironmentNamesTheDatabase(t *testing.T) {
	d, err := externalDatabase("postgres://alice:s3cret@db.example:6543/orders?sslmode=disable", "pg-1")
	require.NoError(t, err)

	env, err := d.libpqEnv([]string{"HOME=/home/alice", "PGHOST=elsewhere", "PGSERVICE=prod"})

	require.NoError(t, err)
	assert.Equal(t, []string{"HOME=/home/alice", "PGHOST=db.example", "PGPORT=6543", "PGUSER=alice", "PGPASSWORD=s3cret",
		"PGDATABASE=orders", "PGSSLMODE=disable"}, env)

	d, err = externalDatabase("postgres://bob@db.example/orders", "pg-1")
	require.NoError(t, err)

	env, err = d.libpqEnv(nil)

	require.NoError(t, err)
	assert.Equal(t, []string{"PGHOST=db.example", "PGPORT=5432", "PGUSER=bob", "PGPASSWORD=", "PGDATABASE=orders"}, env,
		"libpq's default port, and its default TLS mode")
}

// Migrate runs the migrate command of the binary AGENTSWARM_MIGRATE_BIN names,
// when it is set, so that a later build migrates the schema (R17).
func TestMigrateRunsTheBinaryTheEnvironmentNames(t *testing.T) {
	later := filepath.Join(t.TempDir(), "agentswarm-later")
	writeFile(t, later, "#!/bin/sh\necho \"$0 $* $PGHOST:$PGPORT/$PGDATABASE\"\n")
	require.NoError(t, os.Chmod(later, 0o700))
	t.Setenv(migrateBinaryEnv, later)
	env := &Env{t: t, dir: t.TempDir(), db: &database{url: "postgres://u:p@db.example:6543/n?sslmode=disable", user: "u", password: "p", name: "n"}}

	out := env.Migrate(t)

	assert.Equal(t, later+" migrate db.example:6543/n\n", out)
}

func TestPsqlInAGivenDatabaseNeedsItsContainer(t *testing.T) {
	d, err := externalDatabase("postgres://alice:s3cret@db.example:5432/orders", "")
	require.NoError(t, err)

	_, err = d.psql(t.Context(), "select 1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), databaseContainerEnv)
}

// fakeTB stands in for the test a harness reports to, so that a test of the
// harness can look at what it reports. A method it does not define panics, as
// it should: the code under test calls no other.
type fakeTB struct {
	testing.TB

	mu       sync.Mutex
	errors   []string
	fatals   []string
	cleanups []func()
}

func (f *fakeTB) Helper()                  {}
func (f *fakeTB) Logf(string, ...any)      {}
func (f *fakeTB) Context() context.Context { return context.Background() }

func (f *fakeTB) Errorf(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors = append(f.errors, fmt.Sprintf(format, args...))
}

// Fatalf ends the goroutine it is called in, as a test's Fatalf does.
func (f *fakeTB) Fatalf(format string, args ...any) {
	f.mu.Lock()
	f.fatals = append(f.fatals, fmt.Sprintf(format, args...))
	f.mu.Unlock()
	runtime.Goexit()
}

func (f *fakeTB) Fatal(args ...any) { f.Fatalf("%s", fmt.Sprint(args...)) }

func (f *fakeTB) Cleanup(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanups = append(f.cleanups, fn)
}

// end runs the cleanups, the last first, as the end of a test does.
func (f *fakeTB) end() {
	f.mu.Lock()
	cleanups := f.cleanups
	f.cleanups = nil
	f.mu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

// run calls fn in a goroutine of its own, where a Fatalf ends it, and waits.
func (f *fakeTB) run(fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	<-done
}

func (f *fakeTB) reported() (errors, fatals []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.errors), slices.Clone(f.fatals)
}

// post sends a POST with its headers, a body and no keep-alive.
func post(t *testing.T, target, body string, header map[string]string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(body))
	require.NoError(t, err)
	for name, value := range header {
		req.Header.Set(name, value)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	return err
}

// A script is code of a test's, and a bug in it must fail the test: a client
// that sees only a dropped connection would pass many a scenario that expects
// one for another reason.
func TestAVendorScriptThatPanicsFailsItsTest(t *testing.T) {
	test := &fakeTB{}
	v := newVendor(test)
	v.Script(func(*VendorRequest) VendorReply { panic("a script with a bug") })

	err := post(t, v.URL()+"/v1/messages", `{}`, map[string]string{"X-Api-Key": v.Key("a")})

	require.ErrorIs(t, err, io.EOF, "the client sees the connection dropped")
	errors, _ := test.reported()
	require.Len(t, errors, 1, "and the test fails")
	assert.Contains(t, errors[0], "a script with a bug")
	assert.Contains(t, errors[0], "goroutine", "with the stack")
	assert.Contains(t, errors[0], "TestAVendorScriptThatPanicsFailsItsTest", "that shows the script")
	test.end()
}

func TestAToolScriptThatPanicsFailsItsTest(t *testing.T) {
	test := &fakeTB{}
	srv := newMCPServer(test, &ServerSpec{Name: "gateway", Plain: true, Tools: []ToolSpec{{Name: "query"}}}, nil)
	srv.Script("query", func(*ToolCall) ToolReply { panic("a script with a bug") })

	err := post(t, srv.URL(), `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query","arguments":{}}}`,
		map[string]string{"Authorization": "Bearer " + srv.Credential("a")})

	require.ErrorIs(t, err, io.EOF, "the client sees the connection dropped")
	errors, _ := test.reported()
	require.Len(t, errors, 1, "and the test fails")
	assert.Contains(t, errors[0], "a script with a bug")
	assert.Contains(t, errors[0], "goroutine")
	assert.Contains(t, errors[0], "TestAToolScriptThatPanicsFailsItsTest")

	// The panic left the script's lock free: the next call is answered.
	srv.Script("query", func(*ToolCall) ToolReply { return Result("fine") })
	require.NoError(t, post(t, srv.URL(), `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"query","arguments":{}}}`,
		map[string]string{"Authorization": "Bearer " + srv.Credential("a")}))
	test.end()
}

// An answer that cannot be written, a tool's input that JSON cannot hold, is a
// bug of the test's, and the client's 500 must not pass for the vendor's failure.
func TestAVendorAnswerThatCannotBeWrittenFailsItsTest(t *testing.T) {
	test := &fakeTB{}
	v := newVendor(test)
	v.Script(func(*VendorRequest) VendorReply { return ToolUse("read_board", make(chan int)) })

	err := post(t, v.URL()+"/v1/messages", `{}`, map[string]string{"X-Api-Key": v.Key("a")})

	require.NoError(t, err, "the client has an answer: a 500")
	errors, _ := test.reported()
	require.Len(t, errors, 1, "and the test fails")
	assert.Contains(t, errors[0], "could not write a scripted answer")
	test.end()
}

// A test that has ended takes no report: a late one would panic in a goroutine
// of the server's.
func TestNothingIsReportedOnceTheTestHasEnded(t *testing.T) {
	test := &fakeTB{}
	e := newEndpoint(test, http.NotFoundHandler(), nil)

	e.report("while the test runs: %d", 1)
	test.end()
	e.report("after it ended")

	errors, _ := test.reported()
	assert.Equal(t, []string{"while the test runs: 1"}, errors)
}

// A method that is given a test reports to it, not to the test that made the
// Env: a scenario runs in subtests, and Fatal on the parent from the subtest's
// goroutine does not fail the subtest.
func TestADeploymentFailsTheTestItIsGivenNotTheOneThatMadeTheEnv(t *testing.T) {
	made, given := &fakeTB{}, &fakeTB{}
	proxies := map[string]*proxy{}
	for i := range maxInstances {
		proxies["instance-"+strconv.Itoa(i)] = &proxy{}
	}
	env := &Env{
		t: made, dir: t.TempDir(), vendor: &Vendor{keys: map[string]string{}, byName: map[string]string{}},
		servers: map[string]*MCPServer{}, instances: map[string]*instance{},
		db:   &database{url: "postgres://u:p@localhost:5432/n", user: "u", password: "p", name: "n"},
		tox:  &toxiproxy{proxies: proxies},
		repo: &Repo{dir: t.TempDir(), models: []string{defaultModel}},
	}

	given.run(func() { env.deployment(given, "late", &DeploymentSpec{}) })

	_, fatals := given.reported()
	require.Len(t, fatals, 1, "the test the deployment was given fails")
	assert.Contains(t, fatals[0], "at most")
	_, madeFatals := made.reported()
	assert.Empty(t, madeFatals, "the test that made the Env does not")

	// DatabaseURL takes no test, so it fails the one that made the Env.
	made.run(func() { env.DatabaseURL("late") })
	_, madeFatals = made.reported()
	assert.Len(t, madeFatals, 1)
}

// unsetenv unsets a variable for the test, and puts it back when the test
// ends: make test-docker sets the harness's variables for the whole run.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	require.NoError(t, os.Unsetenv(key))
}

// setenv sets a variable for the test, or unsets it when value is empty.
func setenv(t *testing.T, key, value string) {
	t.Helper()
	if value == "" {
		unsetenv(t, key)
		return
	}
	t.Setenv(key, value)
}

// A runner the harness does not know fails New, naming the option or the
// variable it came from, rather than run the process runner and report a
// Docker gate green; so does a tenant that is not one (Contract C9).
func TestNewFailsOnARunnerOrATenantItDoesNotKnow(t *testing.T) {
	for _, tc := range []struct {
		name           string
		option, runner string // Options.Runner, and RunnerEnv's value
		tenant         string // TenantEnv's value
		names          string // what the failure names
	}{
		{name: "an Options.Runner it does not know", option: "podman", runner: "docker", names: "Options.Runner"},
		{name: "a runner variable it does not know", runner: "dockre", names: RunnerEnv},
		{name: "a tenant with a capital", tenant: "Harness", names: TenantEnv},
		{name: "a tenant that begins with a dash", tenant: "-e2e", names: TenantEnv},
		{name: "a tenant with a quote", tenant: "e2e'x", names: TenantEnv},
		{name: "a tenant of 64 characters", tenant: strings.Repeat("a", 64), names: TenantEnv},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setenv(t, RunnerEnv, tc.runner)
			setenv(t, TenantEnv, tc.tenant)
			test := &fakeTB{}

			test.run(func() { settings(test, &Options{Runner: tc.option}) })

			_, fatals := test.reported()
			require.Len(t, fatals, 1, "New fails")
			assert.Contains(t, fatals[0], tc.names)
		})
	}
}

// Options.Runner, when set, chooses the runner, and RunnerEnv when it is not;
// the process runner when neither is. The tenant is TenantEnv's, harness
// when it is unset, and may be as long as 63 characters (Contract C9).
func TestTheRunnerIsTheOptionsOrTheVariablesAndTheTenantTheVariables(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		option, runner, tenant string
		wantRunner, wantTenant string
	}{
		{name: "neither", wantRunner: "process", wantTenant: "harness"},
		{name: "the variables", runner: "docker", tenant: "e2e-20261030120000-0a1b2c3d", wantRunner: "docker", wantTenant: "e2e-20261030120000-0a1b2c3d"},
		{name: "the option over the variable", option: "process", runner: "docker", wantRunner: "process", wantTenant: "harness"},
		{name: "the longest tenant", option: "docker", tenant: strings.Repeat("a", 63), wantRunner: "docker", wantTenant: strings.Repeat("a", 63)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setenv(t, RunnerEnv, tc.runner)
			setenv(t, TenantEnv, tc.tenant)
			test := &fakeTB{}
			var runner, tenant string

			test.run(func() { runner, tenant = settings(test, &Options{Runner: tc.option}) })

			_, fatals := test.reported()
			require.Empty(t, fatals)
			assert.Equal(t, tc.wantRunner, runner)
			assert.Equal(t, tc.wantTenant, tenant)
		})
	}
}

// image is a runtime image's id as the targets pass one in.
const image = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

// envOf is an Env of the runner, as New makes one, with a database and a
// configuration, for a deployment that reports to test.
func envOf(t *testing.T, test testing.TB, runner string) *Env {
	t.Helper()
	return &Env{
		t: test, dir: t.TempDir(), runner: runner, tenant: defaultTenant, vendor: newVendor(t),
		servers: map[string]*MCPServer{}, instances: map[string]*instance{},
		db:   &database{url: "postgres://u:p@localhost:5432/n", user: "u", password: "p", name: "n"},
		repo: &Repo{dir: t.TempDir(), models: []string{defaultModel}},
	}
}

// Under the Docker runner a deployment needs the image its turns run, which
// the targets pass in, and runs no executable of the process runner's: a
// deployment without its image, or with a Binary, fails the test it was given
// (Contract C9). The stubborn image is needed only with Stubborn.
func TestADockerDeploymentFailsWithoutItsImageOrWithABinary(t *testing.T) {
	for _, tc := range []struct {
		name            string
		image, stubborn string // ImageEnv's and StubbornEnv's values
		spec            DeploymentSpec
		names           string // what the failure names; "" for none
	}{
		{name: "the runtime image unset", stubborn: image, names: ImageEnv},
		{name: "the stubborn image unset, with Stubborn", image: image, spec: DeploymentSpec{Stubborn: true}, names: StubbornEnv},
		{name: "a Binary", image: image, stubborn: image, spec: DeploymentSpec{Binary: "/bin/sleep"}, names: "Binary"},
		// Passes before the harness checks anything, and keeps the check of
		// the stubborn image to the deployments that run it.
		{name: "the stubborn image unset, without Stubborn", image: image},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setenv(t, ImageEnv, tc.image)
			setenv(t, StubbornEnv, tc.stubborn)
			test := &fakeTB{}
			env := envOf(t, test, dockerRunner)

			test.run(func() { env.deployment(test, "a", &tc.spec) })

			_, fatals := test.reported()
			deployment := filepath.Join(env.dir, "instances", "a", "deployment.yaml")
			if tc.names == "" {
				require.Empty(t, fatals)
				assert.FileExists(t, deployment)
				return
			}
			require.Len(t, fatals, 1, "the deployment fails the test")
			assert.Contains(t, fatals[0], tc.names)
			assert.NoFileExists(t, deployment, "and writes no file")
		})
	}
}

// Under the process runner, Stubborn names the harness's script as
// runner.binary, so a scenario that sets Binary too fails.
func TestAStubbornProcessDeploymentWithABinaryFails(t *testing.T) {
	test := &fakeTB{}
	env := envOf(t, test, processRunner)

	test.run(func() { env.deployment(test, "a", &DeploymentSpec{Stubborn: true, Binary: "/bin/sleep"}) })

	_, fatals := test.reported()
	require.Len(t, fatals, 1)
	assert.Contains(t, fatals[0], "Stubborn")
	assert.Contains(t, fatals[0], "Binary")
}

// The harness derives an inbound opening's item id as serve does (R129):
// code under test/e2e may not import app.ItemID, so this vector, computed
// once from app.ItemID("harness", "inbound:alerting:fp-a:firing:2026-11-09T10:00:00Z"),
// pins the derivation; it changes only if serve's does.
func TestTheHarnessDerivesAnInboundItemsIDAsServeDoes(t *testing.T) {
	assert.Equal(t, "e4734332-94e3-8f71-9138-59490249cf42", InboundItemID("harness", "alerting", "fp-a:firing:2026-11-09T10:00:00Z"))
	assert.NotEqual(t, InboundItemID("harness", "alerting", "fp-a:firing:2026-11-09T10:00:00Z"),
		InboundItemID("other", "alerting", "fp-a:firing:2026-11-09T10:00:00Z"), "the tenant is part of the id")
}

// An instance has exited once its parent has, as the parent exits with
// serve; Exited says so without waiting, so that the end continues no pid
// that serve no longer holds.
func TestAnInstanceHasExitedOnceItsParentHas(t *testing.T) {
	i := &Instance{parent: &Parent{exited: make(chan struct{})}}
	assert.False(t, i.Exited(), "serve runs while its parent does")
	close(i.parent.exited)
	assert.True(t, i.Exited(), "and has exited once its parent has")
}
