package db

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// embedded returns the migrations this build embeds.
func embedded(t *testing.T) []migration {
	t.Helper()
	ms, err := readMigrations(Migrations())
	require.NoError(t, err)
	require.NotEmpty(t, ms)
	return ms
}

// auditedWith returns the tables a trigger names in the embedded migrations
// and in the scripts given.
func auditedWith(t *testing.T, scripts ...string) map[string]bool {
	t.Helper()
	all := slices.Clone(scripts)
	for _, m := range embedded(t) {
		all = append(all, m.script)
	}
	audited, err := auditedTables(all...)
	require.NoError(t, err)
	return audited
}

// A migration splits at its semicolons, but not at those in quotes, in
// dollar-quoted bodies or in comments.
func TestAMigrationSplitsAtSemicolonsOutsideQuotesAndComments(t *testing.T) {
	for script, want := range map[string][]string{
		"SELECT ';'; SELECT 2":                                  {"SELECT ';'", "SELECT 2"},
		"SELECT 'it''s; here';SELECT 2;":                        {"SELECT 'it''s; here'", "SELECT 2"},
		`SELECT E'a\'; b', e'\\'; SELECT 2`:                     {`SELECT E'a\'; b', e'\\'`, "SELECT 2"},
		`SELECT "a;""b" FROM t; SELECT 2`:                       {`SELECT "a;""b" FROM t`, "SELECT 2"},
		"SELECT 1 -- a comment; not a statement\n; SELECT 2":    {"SELECT 1", "SELECT 2"},
		"/* a /* nested; */ comment; */ SELECT 1; SELECT 2":     {"SELECT 1", "SELECT 2"},
		"DO $x$ BEGIN PERFORM 1; $$; END $x$; SELECT 2":         {"DO $x$ BEGIN PERFORM 1; $$; END $x$", "SELECT 2"},
		"SELECT $$a;b$$, $1, a$b FROM t; ; SELECT 2 -- the end": {"SELECT $$a;b$$, $1, a$b FROM t", "SELECT 2"},
	} {
		stmts, err := split(script)
		require.NoError(t, err, script)
		got := make([]string, 0, len(stmts))
		for _, s := range stmts {
			got = append(got, s.text)
		}
		assert.Equal(t, want, got, script)
	}

	stmts, err := split("-- a comment\n\nCREATE TABLE a (id text);\nCREATE TABLE b (id text);")
	require.NoError(t, err)
	require.Len(t, stmts, 2)
	assert.Equal(t, []int{3, 4}, []int{stmts[0].line, stmts[1].line}, "each statement's line")

	for _, unclosed := range []string{"SELECT 'a", `SELECT "a`, "SELECT 1 /* a", "DO $x$ BEGIN END $y$", `SELECT E'a\'`} {
		_, err := split(unclosed)
		assert.Error(t, err, unclosed)
	}
}

func TestEveryMigrationClassifiesAndThoseOnAnOlderReaderAreOnTheAllowlist(t *testing.T) {
	audited := auditedWith(t)
	assert.Subset(t, slices.Collect(maps.Keys(audited)), []string{
		"board_events", "config_revisions", "config_objects", "tool_calls", "tool_call_outcomes", "tool_hides",
		"audit_chain", "model_calls", "model_call_contents", "model_call_outcomes", "work_records",
	}, "every table a trigger chains or keeps append-only")
	assert.NotContains(t, audited, "leases", "no trigger names the leases")

	for _, m := range embedded(t) {
		stmts, err := split(m.script)
		require.NoError(t, err, m.name)
		require.NotEmpty(t, stmts, m.name)
		for i := range stmts {
			_, err := classify(&stmts[i])
			require.NoError(t, err, "%s, statement %d", m.name, i+1)
		}
		if m.minReader < m.level {
			assert.NoError(t, allowlisted(m.script, audited), "%s declares an older reader", m.name)
		}
	}
}

// allowlistFiles returns the scripts of a directory of testdata/allowlist, by
// file name.
func allowlistFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "allowlist", dir, "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	files := map[string]string{}
	for _, p := range paths {
		data, err := os.ReadFile(p) //nolint:gosec // G304: the test's own data.
		require.NoError(t, err)
		files[filepath.Base(p)] = string(data)
	}
	return files
}

// firstLine returns a script's first line that is neither blank nor a
// comment, without the semicolon that may end it.
func firstLine(t *testing.T, script string) string {
	t.Helper()
	for line := range strings.Lines(script) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "--") {
			return strings.TrimSuffix(line, ";")
		}
	}
	t.Fatalf("the script has no statement:\n%s", script)
	return ""
}

func TestTheAllowlistTakesWhatOlderBuildsCanRead(t *testing.T) {
	for name, script := range allowlistFiles(t, "allowed") {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, allowlisted(script, auditedWith(t, script)))
		})
	}
}

// refusedFor holds, for some of the refused files, a part of the reason the
// allowlist gives, so that none of them is refused only as a statement of no
// kind it knows. Each part is looked for in the reason alone, not in the
// statement the error then quotes.
var refusedFor = map[string]string{
	"null_default_not_null.sql":      "NOT NULL without a default other than NULL",
	"null_cast_default_not_null.sql": "NOT NULL without a default other than NULL",
	"quoted_serial_column.sql":       "is a serial, which fills every row",
	"inherits_table.sql":             "tables it INHERITS, whose triggers",
	"partition_table.sql":            "a PARTITION OF another table, whose reads",
	"create_table_as.sql":            "made AS a query, which copies rows",
	"unicode_serial_column.sql":      "Unicode escapes",
	"unicode_trigger.sql":            "a table the allowlist cannot read",
}

// refusal returns why the allowlist refuses a migration among the embedded
// ones: the scan for audited tables refuses a trigger on a table it cannot
// read, and allowlisted a statement off the allowlist.
func refusal(t *testing.T, script string) error {
	t.Helper()
	all := []string{script}
	for _, m := range embedded(t) {
		all = append(all, m.script)
	}
	audited, err := auditedTables(all...)
	if err != nil {
		return err
	}
	return allowlisted(script, audited)
}

func TestTheAllowlistRefusesWhatOlderBuildsCannotRead(t *testing.T) {
	files := allowlistFiles(t, "refused")
	for name := range refusedFor {
		require.Contains(t, files, name)
	}
	for name, script := range files {
		t.Run(name, func(t *testing.T) {
			named := firstLine(t, script)

			err := refusal(t, script)

			require.Error(t, err)
			reason, quoted := strings.CutSuffix(err.Error(), ": "+named)
			require.True(t, quoted, "the error ends with the statement: %v", err)
			assert.Contains(t, reason, "statement 1,")
			if why, ok := refusedFor[name]; ok {
				assert.Contains(t, reason, why)
			}

			// Behind a statement on the allowlist, it is still the one named.
			err = refusal(t, "CREATE INDEX leases_by_holder ON leases (holder);\n"+script)

			require.Error(t, err)
			reason, quoted = strings.CutSuffix(err.Error(), ": "+named)
			require.True(t, quoted, "the error ends with the statement: %v", err)
			assert.Contains(t, reason, "statement 2,")
		})
	}
}
