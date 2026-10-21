// Command compat is make test-compat, the N−1 test of Plan 7d-1's Decision
// 18. It runs the previous release's harness scenarios 1, 2 and 8, from a
// worktree at the release's tag and with the binary built there, against a
// schema this build migrated. Then this build verifies the audit chain they
// left and inspects each item they left: the roll-forward check. The check
// passes when every scenario passes, the chain's only finding is the file
// store's store_unlocked (R22), and this build reads every record of every
// item. Until a release is tagged, it says so and passes.
//
// The check is of a promise: that the previous release can read the schema
// this build leaves, as only migrations on the allowlist may declare. Once
// this build migrated the database, the program compares the schema's
// min_reader with the previous release's level, its tree's last migration's
// number. When the previous release cannot read the schema, this build makes
// no such promise, and Decision 18's deploy order applies instead: the
// program says so, naming both levels, and passes without the scenarios.
//
// The previous release is the latest tag v* in HEAD's history, HEAD's own
// tags left out, or the tag or commit -previous names. The scenarios share
// one database: a Postgres in a container the program starts, labelled for
// make test-clean, which this build migrates first. The harness of the old
// tree then uses it instead of starting its own (AGENTSWARM_E2E_DATABASE),
// runs psql in its container (AGENTSWARM_E2E_DATABASE_CONTAINER), and
// migrates it with this build (AGENTSWARM_MIGRATE_BIN), as R17 has it.
//
// Done or failed, the program removes what it made. The container goes once
// its docker inspect output and its data are saved under the backups
// directory, in files named as make test-clean names them (R16); one whose
// backup fails stays. The worktree goes once git says it holds nothing git
// does not, since git worktree remove takes ignored files with it. The run's
// other files, the old binary among them, stay in the run's directory.
//
// Usage: go run ./test/compat [-bin bin/agentswarm] [-previous TAG] [-work DIR] [-backups .test-clean]
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Exit codes.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

// noRelease is what the program says, and passes with, while no release is
// tagged.
const noRelease = "no release tagged; nothing to check"

// The database the scenarios share: a Postgres in a container labelled as
// this project's, whose data make test-clean keeps (Decision 16).
const (
	postgresImage    = "postgres:17-alpine"
	postgresData     = "/var/lib/postgresql/data"
	postgresPort     = "5432/tcp"
	databaseUser     = "agentswarm"
	databasePassword = "agentswarm"
	databaseName     = "agentswarm"
	projectLabel     = "agentswarm.project=agent-swarm"
	dataLabel        = "agentswarm.data=" + postgresData
)

// harnessTenant is the tenant of every deployment the harness writes: the
// chain this build verifies, and the items it inspects, are this tenant's.
const harnessTenant = "harness"

// exitFindings is the code agentswarm audit verify exits with when it ran to
// its end and found something.
const exitFindings = 3

// scenarios are the harness scenarios 1, 2 and 8 of Decision 17, by the
// names of their tests in the previous release's tree: convergence, quiet,
// and a standby's takeover from a killed leader.
var scenarios = []string{
	"TestAConvergedRunStopsConvergedAfterItsWrapUpRound",
	"TestARunWhoseAgentsPostNothingStopsQuietAfterOneRound",
	"TestAStandbyTakesOverFromAKilledLeader",
}

// The run's waits.
const (
	// readyWait bounds the wait for Postgres to accept connections, which it
	// does once it has initialized its data and restarted; readyPoll is how
	// often the program asks.
	readyWait = 2 * time.Minute
	readyPoll = 250 * time.Millisecond
	// scenariosTimeout is go test's timeout for the three scenarios. On one
	// database, each scenario's serve first waits for the lease the one
	// before it left unreleased to expire.
	scenariosTimeout = "30m"
	// cleanupWait bounds the removal of what the run made, which a run that
	// was stopped still does.
	cleanupWait = 2 * time.Minute
)

// stampLayout is how the names of the run's files carry its start, in UTC,
// as make test-clean's do.
const stampLayout = "20060102T150405Z"

var (
	// containerID is a container's id as docker prints it. Nothing else
	// reaches a command that removes a container.
	containerID = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
	// itemID is what an item's id may be, so that none reads as a flag.
	itemID = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z_-]{0,127}$`)
	// unsafe is what a tag may hold that a file's name may not.
	unsafe = regexp.MustCompile(`[^0-9A-Za-z._-]`)
)

// command is a command the program runs: its arguments, the directory it
// runs in ("" for the program's own), and the variables it adds to the
// program's environment.
type command struct {
	Dir  string
	Env  []string
	Args []string
}

// runner runs a command, writes what it prints to out, and returns an error
// when the command fails. An error with an ExitCode method carries the
// command's exit code.
type runner func(ctx context.Context, out io.Writer, c command) error

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, shell, time.Now)
	stop()
	os.Exit(code)
}

// run finds the previous release, checks it as the package's doc says, and
// returns the exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer, sh runner, now func() time.Time) int {
	flags := flag.NewFlagSet("compat", flag.ContinueOnError)
	flags.SetOutput(stderr)
	bin := flags.String("bin", "bin/agentswarm", "this build's agentswarm binary, which make build writes")
	previous := flags.String("previous", "", "the previous release's tag, or a commit; by default the latest tag v* in HEAD's history, HEAD's own left out")
	work := flags.String("work", os.TempDir(), "the directory in which the run makes its own: the worktree, the old binary and the files the run writes")
	backups := flags.String("backups", ".test-clean", "the directory that receives the container's backup")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if flags.NArg() > 0 {
		_, _ = fmt.Fprintln(stderr, "compat: it takes flags only; name the previous release with -previous")
		flags.Usage()
		return exitUsage
	}

	tag := *previous
	if tag == "" {
		var err error
		if tag, err = previousRelease(ctx, sh); err != nil {
			_, _ = fmt.Fprintf(stderr, "compat: %v\n", err)
			return exitFailed
		}
		if tag == "" {
			_, _ = fmt.Fprintln(stdout, noRelease)
			return exitOK
		}
	}

	c := &check{sh: sh, stdout: stdout, stderr: stderr, tag: tag, stamp: now().UTC().Format(stampLayout)}
	var err error
	if c.bin, err = filepath.Abs(*bin); err == nil {
		c.backups, err = filepath.Abs(*backups)
	}
	if err == nil {
		c.dir, err = filepath.Abs(filepath.Join(*work, "agentswarm-compat-"+unsafe.ReplaceAllString(tag, "-")+"-"+c.stamp))
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "compat: %v\n", err)
		return exitFailed
	}
	if info, err := os.Stat(c.bin); err != nil || !info.Mode().IsRegular() {
		_, _ = fmt.Fprintf(stderr, "compat: this build's binary %s is missing: run make build, or name it with -bin\n", c.bin)
		return exitFailed
	}
	c.tree = filepath.Join(c.dir, "tree")
	c.old = filepath.Join(c.dir, "agentswarm-"+unsafe.ReplaceAllString(tag, "-"))
	return c.run(ctx)
}

// previousRelease returns the latest tag v* in HEAD's history, HEAD's own
// left out, by version, a pre-release before its release; "" when there is
// none.
func previousRelease(ctx context.Context, sh runner) (string, error) {
	var out bytes.Buffer
	err := sh(ctx, &out, command{Args: []string{
		"git", "-c", "versionsort.suffix=-", "tag", "--list", "v*", "--merged", "HEAD", "--no-contains", "HEAD", "--sort=-version:refname",
	}})
	if err != nil {
		return "", fmt.Errorf("list the release tags: %w", err)
	}
	for line := range strings.Lines(out.String()) {
		if tag := strings.TrimSpace(line); tag != "" {
			return tag, nil
		}
	}
	return "", nil
}

// check is one run of the check against the previous release.
type check struct {
	sh             runner
	stdout, stderr io.Writer
	// tag is the previous release, bin this build's binary, and old the
	// binary built from the release's tree.
	tag, bin, old string
	// stamp is the run's start in UTC; backups is where the container's
	// backup goes.
	stamp, backups string
	// dir is the run's directory, and tree the worktree in it.
	dir, tree string
	// container is the Postgres container, once docker started it; host and
	// port are where it listens.
	container, host, port string
}

// say tells the person what happened; complain tells what went wrong.
func (c *check) say(format string, args ...any) {
	_, _ = fmt.Fprintf(c.stdout, "compat: %s\n", fmt.Sprintf(format, args...))
}

func (c *check) complain(format string, args ...any) {
	_, _ = fmt.Fprintf(c.stderr, "compat: %s\n", fmt.Sprintf(format, args...))
}

// run checks the previous release, removes what it made, and returns the
// exit code: a failure of either is exitFailed.
func (c *check) run(ctx context.Context) (code int) {
	if err := os.MkdirAll(filepath.Dir(c.dir), 0o750); err != nil {
		c.complain("%v", err)
		return exitFailed
	}
	if err := os.Mkdir(c.dir, 0o750); err != nil {
		c.complain("%v", err)
		return exitFailed
	}
	defer c.say("the run's files stay in %s", c.dir)

	if err := c.sh(ctx, io.Discard, command{Args: []string{"git", "worktree", "add", "--detach", c.tree, c.tag}}); err != nil {
		c.complain("check out %s: %v", c.tag, err)
		return exitFailed
	}
	defer func() {
		if !c.removeWorktree(ctx) {
			code = exitFailed
		}
	}()
	defer func() {
		if c.container != "" && !c.removeContainer(ctx) {
			code = exitFailed
		}
	}()
	if err := c.check(ctx); err != nil {
		c.complain("%v", err)
		return exitFailed
	}
	return exitOK
}

// check builds the previous release, runs its scenarios on a database this
// build migrated, and has this build verify and inspect what they left.
func (c *check) check(ctx context.Context) error {
	if err := c.sh(ctx, io.Discard, command{Dir: c.tree, Args: []string{"go", "build", "-o", c.old, "./cmd/agentswarm"}}); err != nil {
		return fmt.Errorf("build %s: %w", c.tag, err)
	}
	if err := c.startPostgres(ctx); err != nil {
		return err
	}
	if err := c.sh(ctx, c.stdout, command{Env: c.libpq(), Args: []string{c.bin, "migrate"}}); err != nil {
		return fmt.Errorf("this build's migrate: %w", err)
	}
	level, err := c.previousLevel()
	if err != nil {
		return err
	}
	minReader, err := c.schemaReader(ctx)
	if err != nil {
		return err
	}
	if minReader > level {
		_, _ = fmt.Fprintln(c.stdout, unreadable(c.tag, level, minReader))
		return nil
	}
	if err := c.runScenarios(ctx); err != nil {
		return err
	}
	if err := c.verify(ctx); err != nil {
		return err
	}
	n, err := c.inspect(ctx)
	if err != nil {
		return err
	}
	c.say("%s's scenarios 1, 2 and 8 passed against a schema this build migrated, and this build verified the chain and inspected the %d items they left",
		c.tag, n)
	return nil
}

// unreadable is what the program says, and passes with, when the previous
// release cannot read the schema this build migrated: this build then makes
// no promise that the two run on one schema, and Decision 18's deploy order
// applies instead.
func unreadable(tag string, level, minReader int) string {
	return fmt.Sprintf("%s cannot read the schema this build migrated (its level %d is below the schema's min_reader %d); "+
		"deploy in Decision 18's order: stop the older instances, migrate, then start the new build; nothing to check", tag, level, minReader)
}

// previousLevel returns the previous release's level, as internal/db reads a
// build's: its tree's last migration's number.
func (c *check) previousLevel() (int, error) {
	names, err := filepath.Glob(filepath.Join(c.tree, "internal", "db", "migrations", "*.sql"))
	if err != nil {
		return 0, fmt.Errorf("%s's level: %w", c.tag, err)
	}
	if len(names) == 0 {
		return 0, fmt.Errorf("%s's level: its tree holds no migration in internal/db/migrations", c.tag)
	}
	level := 0
	for _, name := range names {
		n, err := levelOf(filepath.Base(name))
		if err != nil {
			return 0, fmt.Errorf("%s's level: %w", c.tag, err)
		}
		level = max(level, n)
	}
	return level, nil
}

// schemaReader returns the min_reader of the schema this build migrated, as
// internal/db reads it: the largest of its applied migrations', which
// schema_migrations holds beside each one's name.
func (c *check) schemaReader(ctx context.Context) (int, error) {
	var out bytes.Buffer
	if err := c.sh(ctx, &out, c.psql("SELECT name, min_reader FROM schema_migrations ORDER BY name")); err != nil {
		return 0, fmt.Errorf("read the schema this build migrated: %w", err)
	}
	minReader := 0
	for line := range strings.Lines(out.String()) {
		row := strings.TrimSpace(line)
		if row == "" {
			continue
		}
		name, reader, ok := strings.Cut(row, "|")
		n, err := strconv.Atoi(reader)
		if !ok || err != nil || n < 1 {
			return 0, fmt.Errorf("read the schema this build migrated: %q is no migration's name and min_reader", row)
		}
		if _, err := levelOf(name); err != nil {
			return 0, fmt.Errorf("read the schema this build migrated: %w", err)
		}
		minReader = max(minReader, n)
	}
	if minReader == 0 {
		return 0, errors.New("read the schema this build migrated: schema_migrations holds no migration")
	}
	return minReader, nil
}

// levelOf returns the level a migration's file name gives it, as internal/db
// reads it: the number before its first underscore, from 1, of a name
// NNNN_name.sql.
func levelOf(name string) (int, error) {
	digits, _, ok := strings.Cut(name, "_")
	level, err := strconv.Atoi(digits)
	if !ok || err != nil || strings.Trim(digits, "0123456789") != "" || level < 1 || !strings.HasSuffix(name, ".sql") {
		return 0, fmt.Errorf("%q is not a migration's name: NNNN_name.sql, where NNNN is its level, from 1", name)
	}
	return level, nil
}

// startPostgres starts the labelled Postgres, and returns once it accepts
// connections on its address.
func (c *check) startPostgres(ctx context.Context) error {
	var out bytes.Buffer
	err := c.sh(ctx, &out, command{Args: []string{
		"docker", "run", "--detach", "--label", projectLabel, "--label", dataLabel,
		"--env", "POSTGRES_USER=" + databaseUser, "--env", "POSTGRES_PASSWORD=" + databasePassword, "--env", "POSTGRES_DB=" + databaseName,
		"--publish", "127.0.0.1::5432", postgresImage,
	}})
	id := strings.TrimSpace(out.String())
	if containerID.MatchString(id) {
		c.container = id
	}
	if err != nil {
		return fmt.Errorf("start Postgres: %w", err)
	}
	if c.container == "" {
		return fmt.Errorf("start Postgres: docker printed %q, not a container's id", id)
	}

	out.Reset()
	if err := c.sh(ctx, &out, command{Args: []string{"docker", "port", c.container, postgresPort}}); err != nil {
		return fmt.Errorf("find Postgres' port: %w", err)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(out.String()), "\n")
	if c.host, c.port, err = net.SplitHostPort(strings.TrimSpace(first)); err != nil {
		return fmt.Errorf("find Postgres' port: docker printed %q: %w", out.String(), err)
	}

	ready := command{Args: []string{"docker", "exec", c.container, "pg_isready", "-h", "127.0.0.1", "-U", databaseUser, "-d", databaseName}}
	deadline := time.Now().Add(readyWait)
	for {
		err := c.sh(ctx, io.Discard, ready)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the database did not accept connections within %v: %w", readyWait, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyPoll):
		}
	}
}

// libpq returns the environment through which this build's commands reach
// the database.
func (c *check) libpq() []string {
	return []string{
		"PGHOST=" + c.host, "PGPORT=" + c.port, "PGUSER=" + databaseUser, "PGPASSWORD=" + databasePassword,
		"PGDATABASE=" + databaseName, "PGSSLMODE=disable",
	}
}

// databaseURL returns the URL of the database.
func (c *check) databaseURL() string {
	u := url.URL{
		Scheme: "postgres", User: url.UserPassword(databaseUser, databasePassword), Host: net.JoinHostPort(c.host, c.port),
		Path: "/" + databaseName, RawQuery: "sslmode=disable",
	}
	return u.String()
}

// runScenarios runs the old tree's scenarios with its own binary, on the
// database this build migrated, and shows what they print. go test passes
// when -run matches no test, so each scenario must be seen to pass.
func (c *check) runScenarios(ctx context.Context) error {
	var out bytes.Buffer
	err := c.sh(ctx, io.MultiWriter(c.stdout, &out), command{
		Dir: c.tree,
		Env: []string{
			"AGENTSWARM_BIN=" + c.old, "AGENTSWARM_MIGRATE_BIN=" + c.bin,
			"AGENTSWARM_E2E_DATABASE=" + c.databaseURL(), "AGENTSWARM_E2E_DATABASE_CONTAINER=" + c.container,
		},
		Args: []string{
			"go", "test", "-count=1", "-timeout", scenariosTimeout, "-tags", "e2e", "-v",
			"-run", "^(" + strings.Join(scenarios, "|") + ")$", "./test/e2e/",
		},
	})
	if err != nil {
		return fmt.Errorf("%s's scenarios failed: %w", c.tag, err)
	}
	for _, s := range scenarios {
		if !passed(out.String(), s) {
			return fmt.Errorf("%s's scenario %s did not pass: go test did not run it, as when the tree names no test so", c.tag, s)
		}
	}
	return nil
}

// passed reports whether go test's verbose output says that the test passed.
func passed(output, test string) bool {
	for line := range strings.Lines(output) {
		if strings.HasPrefix(line, "--- PASS: "+test+" ") {
			return true
		}
	}
	return false
}

// verify anchors the harness tenant's chain into a file store with this
// build's audit anchor, then verifies it with its audit verify. A file store
// cannot lock its objects, so the chain verifies when store_unlocked is the
// only finding (R22).
func (c *check) verify(ctx context.Context) error {
	dir := filepath.Join(c.dir, "anchors")
	if err := os.Mkdir(dir, 0o750); err != nil {
		return err
	}
	anchors := (&url.URL{Scheme: "file", Path: dir}).String()
	flags := []string{"--tenant", harnessTenant, "--anchors", anchors}
	if err := c.sh(ctx, io.Discard, command{Env: c.libpq(), Args: append([]string{c.bin, "audit", "anchor"}, flags...)}); err != nil {
		return fmt.Errorf("this build's audit anchor: %w", err)
	}
	var out bytes.Buffer
	err := c.sh(ctx, &out, command{Env: c.libpq(), Args: append([]string{c.bin, "audit", "verify"}, flags...)})
	var exit interface{ ExitCode() int }
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != exitFindings) {
		return fmt.Errorf("this build's audit verify could not run: %w", err)
	}
	var report struct {
		Findings []struct {
			Kind string `json:"kind"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		return fmt.Errorf("read what this build's audit verify printed: %w\n%s", err, out.String())
	}
	kinds := make([]string, 0, len(report.Findings))
	for _, f := range report.Findings {
		kinds = append(kinds, f.Kind)
	}
	if !slices.Equal(kinds, []string{"store_unlocked"}) {
		return fmt.Errorf("the chain does not verify: this build's audit verify found %q, where store_unlocked alone means it does (R22):\n%s",
			kinds, out.String())
	}
	return nil
}

// inspect has this build's inspect print each item the scenarios left, and
// returns how many there are. It fails when they left none, or when an item
// holds a record of a version this build cannot read, which inspect prints
// raw.
func (c *check) inspect(ctx context.Context) (int, error) {
	items, err := c.items(ctx)
	if err != nil {
		return 0, err
	}
	deployment, err := writeDeployment(c.dir, c.databaseURL())
	if err != nil {
		return 0, fmt.Errorf("write a deployment for inspect: %w", err)
	}
	for _, item := range items {
		var out bytes.Buffer
		if err := c.sh(ctx, &out, command{Args: []string{c.bin, "inspect", "--deployment", deployment, "item", item}}); err != nil {
			return 0, fmt.Errorf("this build's inspect of item %s: %w", item, err)
		}
		records := 0
		for line := range strings.Lines(out.String()) {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var r struct {
				Seq  int64           `json:"seq"`
				Kind string          `json:"kind"`
				V    int             `json:"v"`
				Raw  json.RawMessage `json:"raw"`
			}
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				return 0, fmt.Errorf("item %s: inspect printed a line that is no record: %w: %s", item, err, line)
			}
			if len(r.Raw) > 0 {
				return 0, fmt.Errorf("item %s holds a record this build cannot read: record %d, %s, of version %d", item, r.Seq, r.Kind, r.V)
			}
			records++
		}
		if records == 0 {
			return 0, fmt.Errorf("item %s: inspect printed no record of it", item)
		}
	}
	return len(items), nil
}

// items lists the items of the harness's tenant, through psql in the
// database's container.
func (c *check) items(ctx context.Context) ([]string, error) {
	var out bytes.Buffer
	err := c.sh(ctx, &out, c.psql("SELECT DISTINCT item_id FROM work_records WHERE tenant_id = '"+harnessTenant+"' ORDER BY 1"))
	if err != nil {
		return nil, fmt.Errorf("list the items the scenarios left: %w", err)
	}
	var items []string
	for line := range strings.Lines(out.String()) {
		item := strings.TrimSpace(line)
		if item == "" {
			continue
		}
		if !itemID.MatchString(item) {
			return nil, fmt.Errorf("list the items the scenarios left: %q is no item's id", item)
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("the scenarios left no item in the tenant %s, so there is nothing to inspect", harnessTenant)
	}
	return items, nil
}

// psql is a query run with psql in the database's container: what it prints
// is its rows, unaligned and without a header, columns divided by |.
func (c *check) psql(query string) command {
	return command{Args: []string{
		"docker", "exec", "--env", "PGPASSWORD=" + databasePassword, c.container,
		"psql", "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-h", "127.0.0.1", "-U", databaseUser, "-d", databaseName,
		"-c", query,
	}}
}

// writeDeployment writes in dir a deployment file of the harness's tenant on
// the database at url, with its secrets directory, and returns the file's
// path. This build's inspect reads the tenant and the database alone, but
// loads the whole file (Contract C1), so the rest is what C1 asks for, and
// names nothing serve could run. It is written as JSON, which YAML reads.
func writeDeployment(dir, databaseURL string) (string, error) {
	secrets := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(filepath.Join(secrets, "database"), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(secrets, "database", "url"), []byte(databaseURL+"\n"), 0o600); err != nil {
		return "", err
	}
	type object = map[string]any
	d := object{
		"tenant":        harnessTenant,
		"holder":        "compat",
		"secrets":       secrets,
		"database":      "secret://database/url",
		"configuration": object{"repository": dir, "ref": "refs/heads/main"},
		"listeners": object{
			"mcp": "127.0.0.1:0", "llm": "127.0.0.1:0", "admin": "127.0.0.1:0", "addresses": filepath.Join(dir, "addresses.json"),
		},
		"admin":  object{"token": "secret://admin/token"},
		"vendor": object{"baseURL": "http://127.0.0.1:1", "key": "secret://vendor/key", "apiVersion": "2023-06-01"},
		"prices": object{
			"reference": 3_000_000, "dailyCap": 20_000_000,
			"cards": object{"none": object{"threshold": 0, "base": object{
				"input": 1_000_000, "output": 5_000_000, "cacheWrite5m": 1_250_000, "cacheWrite1h": 2_000_000, "cacheRead": 100_000,
			}}},
		},
		"runner": object{
			"kind": "process", "unsafeNoSandbox": true, "output": filepath.Join(dir, "turns"),
			"images": object{"thinloop": "sha256:" + strings.Repeat("0", 64)},
		},
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "deployment.yaml")
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// removeContainer stops the container, so that its data is whole, saves its
// docker inspect output and its data under the backups directory, and only
// then removes it. It reports whether the container is gone.
func (c *check) removeContainer(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupWait)
	defer cancel()
	if err := c.sh(ctx, io.Discard, command{Args: []string{"docker", "stop", c.container}}); err != nil {
		c.complain("stop the container %s: %v; backing it up as it is", c.container, err)
	}
	stem := filepath.Join(c.backups, c.container+"-"+c.stamp)
	if err := backUp(ctx, c.sh, stem, c.container); err != nil {
		c.complain("the container %s stays, as its backup failed: %v; make test-clean removes it after an hour, after a backup of its own",
			c.container, err)
		return false
	}
	if err := c.sh(ctx, io.Discard, command{Args: []string{"docker", "rm", "-f", "-v", c.container}}); err != nil {
		c.complain("the container %s is backed up as %s* but was not removed: %v", c.container, stem, err)
		return false
	}
	c.say("removed the container %s, backed up as %s*", c.container, stem)
	return true
}

// removeWorktree removes the worktree once git says it holds nothing git
// does not: git worktree remove refuses a worktree with changes or untracked
// files, but takes ignored ones with it. It reports whether the worktree is
// gone.
func (c *check) removeWorktree(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupWait)
	defer cancel()
	var status bytes.Buffer
	if err := c.sh(ctx, &status, command{Dir: c.tree, Args: []string{"git", "status", "--porcelain", "--ignored"}}); err != nil {
		c.complain("the worktree %s stays, as git could not say what it holds: %v", c.tree, err)
		return false
	}
	if held := strings.TrimSpace(status.String()); held != "" {
		c.complain("the worktree %s stays, as it holds what git does not:\n%s\nRemove it with git worktree remove --force once nothing in it is wanted.",
			c.tree, held)
		return false
	}
	if err := c.sh(ctx, io.Discard, command{Args: []string{"git", "worktree", "remove", c.tree}}); err != nil {
		c.complain("the worktree %s stays: %v", c.tree, err)
		return false
	}
	c.say("removed the worktree %s", c.tree)
	return true
}

// backUp saves what the container held, in files whose names begin with
// stem: its docker inspect output, then the files at its data path.
func backUp(ctx context.Context, sh runner, stem, container string) error {
	if err := os.MkdirAll(filepath.Dir(stem), 0o750); err != nil {
		return err
	}
	err := save(stem+".inspect.json", func(w io.Writer) error {
		return sh(ctx, w, command{Args: []string{"docker", "inspect", container}})
	})
	if err != nil {
		return fmt.Errorf("save its inspect output: %w", err)
	}
	err = save(stem+"-data.tar", func(w io.Writer) error {
		return sh(ctx, w, command{Args: []string{"docker", "cp", container + ":" + postgresData, "-"}})
	})
	if err != nil {
		return fmt.Errorf("save its data at %s: %w", postgresData, err)
	}
	return nil
}

// save writes what write produces to path, and never replaces a file already
// there, as a backup is never replaced. The file takes its name only once it
// is whole, so that a copy that failed never looks like a backup; the part it
// wrote stays beside it, as path.partial.
func save(path string, write func(io.Writer) error) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists, and a backup is never replaced", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	part := path + ".partial"
	f, err := os.OpenFile(filepath.Clean(part), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	err = write(f)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(part, path)
}

// stderrLimit is how much of a command's standard error its error keeps: the
// end, where a failure is told.
const stderrLimit = 16 << 10

// tail keeps the last stderrLimit bytes written to it.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if over := len(t.b) - stderrLimit; over > 0 {
		t.b = t.b[over:]
	}
	return len(p), nil
}

// shell runs a command in its directory, with the program's environment but
// its libpq variables, which would name another database, and with the
// command's own. What it prints goes to out; what it says on its standard
// error comes back in its error. It runs in a process group of its own, which
// a stopped run kills whole: go test's test binary with go test, whose death
// ends the scenarios' serves (R20).
func shell(ctx context.Context, out io.Writer, c command) error {
	cmd := exec.CommandContext(ctx, c.Args[0], c.Args[1:]...) //nolint:gosec // G204: this program's own commands, on paths it made.
	cmd.Dir = c.Dir
	cmd.Env = append(withoutLibpq(os.Environ()), c.Env...)
	var stderr tail
	cmd.Stdout, cmd.Stderr = out, &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		} else if err != nil {
			return err
		}
		return nil
	}
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Run(); err != nil {
		name := filepath.Base(c.Args[0])
		if len(c.Args) > 1 {
			name += " " + c.Args[1]
		}
		if msg := strings.TrimSpace(string(stderr.b)); msg != "" {
			return fmt.Errorf("%s: %w:\n%s", name, err, msg)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// withoutLibpq returns environ without its libpq variables (PG...).
func withoutLibpq(environ []string) []string {
	return slices.DeleteFunc(slices.Clone(environ), func(kv string) bool { return strings.HasPrefix(kv, "PG") })
}
