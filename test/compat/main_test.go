package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/deploy"
)

const (
	// container is the Postgres container the fake docker starts, and port
	// the port it publishes on loopback.
	container = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	port      = "55001"
	// stamp is testNow in UTC, as the names of the run's files carry it.
	stamp = "20261021T040000Z"
	// databaseURL is the database the scenarios are given.
	databaseURL = "postgres://agentswarm:agentswarm@127.0.0.1:" + port + "/agentswarm?sslmode=disable"
)

// testNow is the moment the program thinks it is.
var testNow = time.Date(2026, 10, 21, 15, 0, 0, 0, time.FixedZone("AEDT", 11*60*60))

// items are the items the old scenarios left, as the database lists them.
var items = []string{
	"0193a2b4-0000-8000-8000-000000000001",
	"0193a2b4-0000-8000-8000-000000000002",
	"0193a2b4-0000-8000-8000-000000000003",
}

// tagListing is the listing of the release tags in HEAD's history, HEAD's
// own left out, the latest version first.
var tagListing = command{Args: []string{
	"git", "-c", "versionsort.suffix=-", "tag", "--list", "v*", "--merged", "HEAD", "--no-contains", "HEAD", "--sort=-version:refname",
}}

// psql is a query the program runs with psql in the database's container.
func psql(query string) command {
	return command{Args: []string{
		"docker", "exec", "--env", "PGPASSWORD=agentswarm", container,
		"psql", "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-h", "127.0.0.1", "-U", "agentswarm", "-d", "agentswarm",
		"-c", query,
	}}
}

// schemaQuery reads the schema this build migrated: each applied migration's
// name and min_reader.
var schemaQuery = psql("SELECT name, min_reader FROM schema_migrations ORDER BY name")

// libpq is the environment through which this build's commands reach the
// database.
var libpq = []string{
	"PGHOST=127.0.0.1", "PGPORT=" + port, "PGUSER=agentswarm", "PGPASSWORD=agentswarm", "PGDATABASE=agentswarm", "PGSSLMODE=disable",
}

// exitCode is a command's exit status, as the real runner reports it.
type exitCode int

func (c exitCode) Error() string { return fmt.Sprintf("exit status %d", int(c)) }
func (c exitCode) ExitCode() int { return int(c) }

// fakeShell stands in for the commands the program runs: git, go, the
// docker CLI and this build's binary. It answers each as they would, and
// records them.
type fakeShell struct {
	// tags is what the listing of release tags prints.
	tags string
	// notReady is how many times Postgres answers that it does not accept
	// connections yet.
	notReady int
	// passed are the tests go test reports passed; nil means the three
	// scenarios.
	passed []string
	// findings are the kinds audit verify reports; nil means store_unlocked
	// alone.
	findings []string
	// listed are the items the database lists; nil means items.
	listed []string
	// oldMigrations are the migration files the previous release's tree
	// holds, which git worktree add checks out; nil means levels 1 to 9.
	oldMigrations []string
	// schema are the rows of schema_migrations once this build migrated, as
	// psql prints them, name|min_reader; nil means levels 1 to 9, each read
	// from its own level on.
	schema []string
	// records is what inspect prints of an item; an item it does not hold
	// prints a record of this build's version.
	records map[string]string
	// unclean is what git status prints of the worktree.
	unclean string
	// failing makes a command fail, by its name (see name), after it wrote
	// what it writes.
	failing map[string]error
	calls   []command
}

// name names a command as the fake answers it.
func name(c command) string {
	a := c.Args
	switch filepath.Base(a[0]) {
	case "git":
		switch a[1] {
		case "-c":
			return "git " + a[3]
		case "worktree":
			return "git worktree " + a[2]
		}
		return "git " + a[1]
	case "docker":
		switch {
		case a[1] != "exec":
			return "docker " + a[1]
		case slices.Contains(a, "pg_isready"):
			return "pg_isready"
		case slices.Contains(a, "psql"):
			return "psql " + strings.Fields(strings.SplitN(a[len(a)-1], " FROM ", 2)[1])[0]
		}
		return "docker exec"
	case "go":
		return "go " + a[1]
	}
	if a[1] == "audit" {
		return "agentswarm audit " + a[2]
	}
	return "agentswarm " + a[1]
}

func (f *fakeShell) run(_ context.Context, out io.Writer, c command) error {
	f.calls = append(f.calls, command{Dir: c.Dir, Env: slices.Clone(c.Env), Args: slices.Clone(c.Args)})
	n := name(c)
	var printed string
	switch n {
	case "git tag":
		printed = f.tags
	case "git worktree add":
		if err := f.checkOut(c.Args[4]); err != nil {
			return err
		}
	case "git status":
		printed = f.unclean
	case "docker run":
		printed = container + "\n"
	case "docker port":
		printed = "127.0.0.1:" + port + "\n"
	case "pg_isready":
		if f.notReady > 0 {
			f.notReady--
			return exitCode(2)
		}
		printed = "127.0.0.1:5432 - accepting connections\n"
	case "go test":
		passed := f.passed
		if passed == nil {
			passed = scenarios
		}
		for _, s := range passed {
			printed += "=== RUN   " + s + "\n--- PASS: " + s + " (12.34s)\n"
		}
		printed += "PASS\nok  \tgithub.com/jchen0426/agent-swarm/test/e2e\t99.9s\n"
	case "agentswarm audit anchor":
		printed = `{"key": "harness/0000000000000042", "seq": 42, "runs": 3}` + "\n"
	case "agentswarm audit verify":
		findings := f.findings
		if findings == nil {
			findings = []string{"store_unlocked"}
		}
		var report strings.Builder
		for i, kind := range findings {
			if i > 0 {
				report.WriteString(", ")
			}
			fmt.Fprintf(&report, `{"kind": %q, "detail": "..."}`, kind)
		}
		if _, err := fmt.Fprintf(out, `{"findings": [%s]}`+"\n", report.String()); err != nil {
			return err
		}
		if err := f.failing[n]; err != nil {
			return err
		}
		if len(findings) > 0 {
			return exitCode(3)
		}
		return nil
	case "psql schema_migrations":
		rows := f.schema
		if rows == nil {
			rows = readers(migrations(1, 9))
		}
		for _, row := range rows {
			printed += row + "\n"
		}
	case "psql work_records":
		listed := f.listed
		if listed == nil {
			listed = items
		}
		for _, item := range listed {
			printed += item + "\n"
		}
	case "agentswarm inspect":
		item := c.Args[len(c.Args)-1]
		var ok bool
		if printed, ok = f.records[item]; !ok {
			printed = `{"seq":1,"kind":"item_opened","v":1,"at":"2026-10-21T04:00:01Z","body":{"v":1}}` + "\n"
		}
	case "docker inspect":
		printed = `[{"Id":"` + container + `"}]`
	case "docker cp":
		printed = "tar of " + c.Args[2]
	}
	if _, err := io.WriteString(out, printed); err != nil {
		return err
	}
	return f.failing[n]
}

// migrations returns the file names of the migrations of levels from to to.
func migrations(from, to int) []string {
	var names []string
	for level := from; level <= to; level++ {
		names = append(names, fmt.Sprintf("%04d_migration.sql", level))
	}
	return names
}

// readers returns the rows of schema_migrations of the migrations, each read
// from its own level on, as the migrations of Plan 7d-1 are.
func readers(names []string) []string {
	rows := make([]string, 0, len(names))
	for _, name := range names {
		level, _, _ := strings.Cut(name, "_")
		rows = append(rows, name+"|"+strings.TrimLeft(level, "0"))
	}
	return rows
}

// checkOut makes the worktree git worktree add would, as far as the program
// reads it: the previous release's migrations.
func (f *fakeShell) checkOut(tree string) error {
	names := f.oldMigrations
	if names == nil {
		names = migrations(1, 9)
	}
	dir := filepath.Join(tree, "internal", "db", "migrations")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("-- a migration\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// names returns the names of the commands the fake ran, in order.
func (f *fakeShell) names() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, name(c))
	}
	return out
}

// thisBuild returns the path of a file that stands for this build's binary.
func thisBuild(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "agentswarm")
	require.NoError(t, os.WriteFile(bin, []byte("this build"), 0o600))
	return bin
}

// compat runs the program against the fake at testNow.
func compat(t *testing.T, sh *fakeShell, flags ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(t.Context(), flags, &out, &errOut, sh.run, func() time.Time { return testNow })
	return out.String(), errOut.String(), code
}

// paths are where a run at testNow against the tag keeps its files.
type paths struct {
	dir, tree, old, anchors, deployment string
}

func pathsOf(work, tag string) paths {
	dir := filepath.Join(work, "agentswarm-compat-"+tag+"-"+stamp)
	return paths{
		dir:        dir,
		tree:       filepath.Join(dir, "tree"),
		old:        filepath.Join(dir, "agentswarm-"+tag),
		anchors:    "file://" + filepath.Join(dir, "anchors"),
		deployment: filepath.Join(dir, "deployment.yaml"),
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// cleanUp is what the program runs once it is done or has failed: the
// container is stopped, backed up and removed, and the worktree, once git
// says it holds nothing git does not, is removed.
var cleanUp = []string{"docker stop", "docker inspect", "docker cp", "docker rm", "git status", "git worktree remove"}

func TestCompatSaysSoAndPassesWithoutATag(t *testing.T) {
	sh := &fakeShell{}

	stdout, errOut, code := compat(t, sh)

	require.Equal(t, 0, code, errOut)
	assert.Equal(t, "no release tagged; nothing to check\n", stdout)
	assert.Equal(t, []command{tagListing}, sh.calls, "nothing runs but the listing of tags: no worktree, no build, no container")
}

func TestCompatRunsThePreviousScenariosAgainstASchemaThisBuildMigrated(t *testing.T) {
	bin := thisBuild(t)
	work, backups := t.TempDir(), t.TempDir()
	sh := &fakeShell{tags: "v0.1.0\nv0.1.0-rc.1\nv0.0.9\n"}

	stdout, errOut, code := compat(t, sh, "-bin", bin, "-work", work, "-backups", backups)

	require.Equal(t, 0, code, errOut)
	p := pathsOf(work, "v0.1.0")
	inspect := func(item string) command {
		return command{Args: []string{bin, "inspect", "--deployment", p.deployment, "item", item}}
	}
	assert.Equal(t, []command{
		tagListing,
		{Args: []string{"git", "worktree", "add", "--detach", p.tree, "v0.1.0"}},
		{Dir: p.tree, Args: []string{"go", "build", "-o", p.old, "./cmd/agentswarm"}},
		{Args: []string{
			"docker", "run", "--detach",
			"--label", "agentswarm.project=agent-swarm", "--label", "agentswarm.data=/var/lib/postgresql/data",
			"--env", "POSTGRES_USER=agentswarm", "--env", "POSTGRES_PASSWORD=agentswarm", "--env", "POSTGRES_DB=agentswarm",
			"--publish", "127.0.0.1::5432", "postgres:17-alpine",
		}},
		{Args: []string{"docker", "port", container, "5432/tcp"}},
		{Args: []string{"docker", "exec", container, "pg_isready", "-h", "127.0.0.1", "-U", "agentswarm", "-d", "agentswarm"}},
		{Env: libpq, Args: []string{bin, "migrate"}},
		schemaQuery,
		{
			Dir: p.tree,
			Env: []string{
				"AGENTSWARM_BIN=" + p.old, "AGENTSWARM_MIGRATE_BIN=" + bin,
				"AGENTSWARM_E2E_DATABASE=" + databaseURL, "AGENTSWARM_E2E_DATABASE_CONTAINER=" + container,
			},
			Args: []string{
				"go", "test", "-count=1", "-timeout", "30m", "-tags", "e2e", "-v", "-run",
				"^(TestAConvergedRunStopsConvergedAfterItsWrapUpRound|TestARunWhoseAgentsPostNothingStopsQuietAfterOneRound|TestAStandbyTakesOverFromAKilledLeader)$",
				"./test/e2e/",
			},
		},
		{Env: libpq, Args: []string{bin, "audit", "anchor", "--tenant", "harness", "--anchors", p.anchors}},
		{Env: libpq, Args: []string{bin, "audit", "verify", "--tenant", "harness", "--anchors", p.anchors}},
		psql("SELECT DISTINCT item_id FROM work_records WHERE tenant_id = 'harness' ORDER BY 1"),
		inspect(items[0]), inspect(items[1]), inspect(items[2]),
		{Args: []string{"docker", "stop", container}},
		{Args: []string{"docker", "inspect", container}},
		{Args: []string{"docker", "cp", container + ":/var/lib/postgresql/data", "-"}},
		{Args: []string{"docker", "rm", "-f", "-v", container}},
		{Dir: p.tree, Args: []string{"git", "status", "--porcelain", "--ignored"}},
		{Args: []string{"git", "worktree", "remove", p.tree}},
	}, sh.calls, "the previous release's tree and binary, a labelled Postgres this build migrates, the schema's readers, the old scenarios "+
		"on it, this build's verification and inspection of what they left, then the container and the worktree removed after a backup")
	assert.JSONEq(t, `[{"Id":"`+container+`"}]`, read(t, filepath.Join(backups, container+"-"+stamp+".inspect.json")))
	assert.Equal(t, "tar of "+container+":/var/lib/postgresql/data", read(t, filepath.Join(backups, container+"-"+stamp+"-data.tar")))
	assert.Contains(t, stdout, "--- PASS: TestAStandbyTakesOverFromAKilledLeader", "the scenarios' output is shown")
	assert.Contains(t, stdout, "v0.1.0's scenarios 1, 2 and 8 passed")
	assert.Contains(t, stdout, "inspected the 3 items")
	assert.Equal(t, databaseURL+"\n", read(t, filepath.Join(p.dir, "secrets", "database", "url")), "inspect's deployment reaches the database")
	assert.DirExists(t, filepath.Join(p.dir, "anchors"), "the file store audit anchor writes into")
}

// A release whose migrations the previous release cannot read makes no promise
// that the two run on one schema: Decision 18's deploy order applies, and
// there is nothing to check. The program says so and passes, and removes what
// it made, as ever.
func TestCompatPassesWithoutTheScenariosWhenThePreviousReleaseCannotReadTheSchema(t *testing.T) {
	backups := t.TempDir()
	sh := &fakeShell{tags: "v0.1.0\n", schema: append(readers(migrations(1, 9)),
		"0010_migration.sql|9", "0011_migration.sql|11", "0012_migration.sql|10")}

	stdout, errOut, code := compat(t, sh, "-bin", thisBuild(t), "-work", t.TempDir(), "-backups", backups)

	require.Equal(t, 0, code, errOut)
	assert.Contains(t, stdout, "v0.1.0 cannot read the schema this build migrated (its level 9 is below the schema's min_reader 11); "+
		"deploy in Decision 18's order: stop the older instances, migrate, then start the new build; nothing to check\n")
	assert.Equal(t, append([]string{
		"git tag", "git worktree add", "go build", "docker run", "docker port", "pg_isready", "agentswarm migrate", "psql schema_migrations",
	}, cleanUp...), sh.names(), "no scenario, verification or inspection runs; the container and the worktree go as ever")
	assert.FileExists(t, filepath.Join(backups, container+"-"+stamp+"-data.tar"))
	assert.Empty(t, errOut)
}

// A later schema that the previous release can still read, as a migration on
// the allowlist that declares an older reader leaves it, is what the check
// is for: the scenarios run.
func TestCompatRunsTheScenariosOnALaterSchemaThePreviousReleaseCanRead(t *testing.T) {
	sh := &fakeShell{tags: "v0.1.0\n", schema: append(readers(migrations(1, 9)), "0010_migration.sql|9", "0011_migration.sql|9")}

	stdout, errOut, code := compat(t, sh, "-bin", thisBuild(t), "-work", t.TempDir(), "-backups", t.TempDir())

	require.Equal(t, 0, code, errOut)
	assert.Contains(t, sh.names(), "go test")
	assert.Contains(t, stdout, "v0.1.0's scenarios 1, 2 and 8 passed")
}

// The previous release's level is its tree's last migration's: a tree whose
// migrations cannot be read leaves the check undecided, which fails it.
func TestCompatFailsWhenItCannotTellThePreviousReleasesLevel(t *testing.T) {
	for name, files := range map[string][]string{
		"no migration":            {},
		"a name that is no level": {"0001_migration.sql", "latest.sql"},
	} {
		t.Run(name, func(t *testing.T) {
			sh := &fakeShell{tags: "v0.1.0\n", oldMigrations: files}

			_, errOut, code := compat(t, sh, "-bin", thisBuild(t), "-work", t.TempDir(), "-backups", t.TempDir())

			assert.Equal(t, 1, code)
			assert.Contains(t, errOut, "v0.1.0's level")
			assert.NotContains(t, sh.names(), "go test")
			assert.Equal(t, cleanUp, sh.names()[len(sh.names())-len(cleanUp):])
		})
	}
}

func TestCompatTakesThePreviousReleaseItIsGiven(t *testing.T) {
	work := t.TempDir()
	sh := &fakeShell{tags: "v0.2.0\n"}

	_, errOut, code := compat(t, sh, "-bin", thisBuild(t), "-work", work, "-backups", t.TempDir(), "-previous", "v0.1.0")

	require.Equal(t, 0, code, errOut)
	assert.Equal(t, "git worktree add", name(sh.calls[0]), "no listing of tags")
	assert.Equal(t, pathsOf(work, "v0.1.0").tree, sh.calls[0].Args[4])
	assert.Equal(t, "v0.1.0", sh.calls[0].Args[5])
}

// Postgres restarts once after it initialized its data; the program waits
// until it accepts connections on its address before this build migrates it.
func TestCompatWaitsForPostgresToAcceptConnections(t *testing.T) {
	sh := &fakeShell{tags: "v0.1.0\n", notReady: 2}

	_, errOut, code := compat(t, sh, "-bin", thisBuild(t), "-work", t.TempDir(), "-backups", t.TempDir())

	require.Equal(t, 0, code, errOut)
	got := sh.names()
	i := slices.Index(got, "pg_isready")
	require.GreaterOrEqual(t, i, 0)
	assert.Equal(t, []string{"pg_isready", "pg_isready", "pg_isready", "agentswarm migrate"}, got[i:i+4])
}

// A failure stops the check where it is, and the program still removes what
// it made, after the container's backup.
func TestCompatFailsWhenTheScenariosFailAndStillCleansUp(t *testing.T) {
	backups := t.TempDir()
	sh := &fakeShell{tags: "v0.1.0\n", failing: map[string]error{"go test": exitCode(1)}}

	stdout, errOut, code := compat(t, sh, "-bin", thisBuild(t), "-work", t.TempDir(), "-backups", backups)

	assert.Equal(t, 1, code)
	assert.Equal(t, append([]string{
		"git tag", "git worktree add", "go build", "docker run", "docker port", "pg_isready", "agentswarm migrate", "psql schema_migrations", "go test",
	}, cleanUp...), sh.names())
	assert.Contains(t, errOut, "v0.1.0's scenarios failed")
	assert.NotContains(t, stdout, "passed")
	assert.FileExists(t, filepath.Join(backups, container+"-"+stamp+"-data.tar"), "the database the scenarios left is kept")
}

// go test passes when -run matches no test, as when the old tree named a
// scenario otherwise: the check passes only when each scenario passed.
func TestCompatFailsWhenAScenarioDidNotRun(t *testing.T) {
	sh := &fakeShell{tags: "v0.1.0\n", passed: scenarios[:2]}

	_, errOut, code := compat(t, sh, "-bin", thisBuild(t), "-work", t.TempDir(), "-backups", t.TempDir())

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "TestAStandbyTakesOverFromAKilledLeader")
	assert.NotContains(t, sh.names(), "agentswarm audit anchor")
	assert.Equal(t, cleanUp, sh.names()[len(sh.names())-len(cleanUp):])
}

// R22: a file store cannot lock its objects, so the chain verifies when
// store_unlocked is the only finding.
func TestCompatFailsOnAFindingBesidesStoreUnlocked(t *testing.T) {
	for name, findings := range map[string][]string{
		"a row changed":        {"store_unlocked", "row_changed"},
		"no store_unlocked":    {},
		"another finding only": {"record_unlinked"},
	} {
		t.Run(name, func(t *testing.T) {
			sh := &fakeShell{tags: "v0.1.0\n", findings: findings}

			_, errOut, code := compat(t, sh, "-bin", thisBuild(t), "-work", t.TempDir(), "-backups", t.TempDir())

			assert.Equal(t, 1, code)
			assert.Contains(t, errOut, "the chain does not verify")
			assert.NotContains(t, sh.names(), "agentswarm inspect")
		})
	}
}

func TestCompatFailsWhenVerificationCannotRun(t *testing.T) {
	sh := &fakeShell{tags: "v0.1.0\n", failing: map[string]error{"agentswarm audit verify": exitCode(1)}}

	_, errOut, code := compat(t, sh, "-bin", thisBuild(t), "-work", t.TempDir(), "-backups", t.TempDir())

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "audit verify")
	assert.NotContains(t, sh.names(), "agentswarm inspect")
}

// The roll-forward check: this build reads every record the old build left.
func TestCompatFailsOnARecordThisBuildCannotRead(t *testing.T) {
	sh := &fakeShell{tags: "v0.1.0\n", records: map[string]string{
		items[1]: `{"seq":1,"kind":"item_opened","v":1,"at":"2026-10-21T04:00:01Z","body":{"v":1}}` + "\n" +
			`{"seq":2,"kind":"round_opened","v":2,"at":"2026-10-21T04:00:02Z","raw":"{\"v\":2}"}` + "\n",
	}}

	_, errOut, code := compat(t, sh, "-bin", thisBuild(t), "-work", t.TempDir(), "-backups", t.TempDir())

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, items[1])
	assert.Contains(t, errOut, "round_opened")
	assert.Equal(t, cleanUp, sh.names()[len(sh.names())-len(cleanUp):])
}

// Scenarios that left nothing in the harness's tenant, as when the old
// harness wrote another, leave nothing to check: the check does not pass.
func TestCompatFailsWhenTheScenariosLeftNoItem(t *testing.T) {
	sh := &fakeShell{tags: "v0.1.0\n", listed: []string{}}

	_, errOut, code := compat(t, sh, "-bin", thisBuild(t), "-work", t.TempDir(), "-backups", t.TempDir())

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "left no item")
	assert.NotContains(t, sh.names(), "agentswarm inspect")
}

// R16: nothing is removed whose backup failed.
func TestCompatLeavesAContainerWhoseBackupFailed(t *testing.T) {
	backups := t.TempDir()
	sh := &fakeShell{tags: "v0.1.0\n", failing: map[string]error{"docker cp": errors.New("no space left on device")}}

	stdout, errOut, code := compat(t, sh, "-bin", thisBuild(t), "-work", t.TempDir(), "-backups", backups)

	assert.Equal(t, 1, code)
	assert.NotContains(t, sh.names(), "docker rm")
	assert.Contains(t, errOut, container)
	assert.Contains(t, errOut, "no space left on device")
	assert.Contains(t, stdout, "v0.1.0's scenarios 1, 2 and 8 passed", "the check passed; the cleanup did not")
	assert.NoFileExists(t, filepath.Join(backups, container+"-"+stamp+"-data.tar"), "a copy that failed never looks like a backup")
}

// git worktree remove takes ignored files with it, so a worktree that holds
// anything git does not stays.
func TestCompatLeavesAWorktreeThatHoldsWhatGitDoesNot(t *testing.T) {
	work := t.TempDir()
	sh := &fakeShell{tags: "v0.1.0\n", unclean: "!! test/e2e/scenario.log\n"}

	_, errOut, code := compat(t, sh, "-bin", thisBuild(t), "-work", work, "-backups", t.TempDir())

	assert.Equal(t, 1, code)
	assert.NotContains(t, sh.names(), "git worktree remove")
	assert.Contains(t, errOut, pathsOf(work, "v0.1.0").tree)
	assert.Contains(t, errOut, "scenario.log")
}

func TestCompatNeedsThisBuildsBinaryOnceThereIsATag(t *testing.T) {
	sh := &fakeShell{tags: "v0.1.0\n"}

	_, errOut, code := compat(t, sh, "-bin", filepath.Join(t.TempDir(), "agentswarm"), "-work", t.TempDir())

	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "make build")
	assert.Equal(t, []string{"git tag"}, sh.names())
}

func TestCompatRefusesAnArgument(t *testing.T) {
	sh := &fakeShell{}

	_, errOut, code := compat(t, sh, "v0.1.0")

	assert.Equal(t, 2, code)
	assert.Contains(t, errOut, "-previous")
	assert.Empty(t, sh.calls)
}

// inspect reads a deployment file, which the program writes for this build:
// one this build's own loader refuses would fail every check.
func TestTheDeploymentForInspectIsOneThisBuildLoads(t *testing.T) {
	dir := t.TempDir()

	path, err := writeDeployment(dir, databaseURL)
	require.NoError(t, err)

	d, err := deploy.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "harness", d.Tenant, "the harness's tenant, whose items the scenarios left")
	assert.Equal(t, filepath.Join(dir, "secrets"), d.Secrets)
	assert.Equal(t, "secret://database/url", d.Database)
	assert.Equal(t, databaseURL+"\n", read(t, filepath.Join(dir, "secrets", "database", "url")))
}
