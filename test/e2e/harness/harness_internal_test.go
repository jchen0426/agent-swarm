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
