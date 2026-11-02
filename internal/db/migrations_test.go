package db

import (
	"slices"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A migration's level is its file name's number, and its min_reader the one
// its first line declares (Contract C8), or else its level.
func TestAMigrationsLevelAndReaderComeFromItsNameAndFirstLine(t *testing.T) {
	for script, want := range map[string]int{
		"CREATE TABLE t (id text);":                                3,
		"-- agentswarm:min_reader 1\nCREATE TABLE t (id text);":    1,
		"-- agentswarm:min_reader  3\r\nCREATE TABLE t (id text);": 3,
		"-- A comment.\n-- agentswarm:min_reader 1\nSELECT 1;":     3,
		"--agentswarm:min_reader 1\nSELECT 1;":                     3,
	} {
		ms, err := readMigrations(fstest.MapFS{"0003_t.sql": {Data: []byte(script)}})
		require.NoError(t, err, script)
		require.Len(t, ms, 1)
		assert.Equal(t, migration{name: "0003_t.sql", level: 3, minReader: want, script: script}, ms[0], script)
	}

	for script, why := range map[string]string{
		"-- agentswarm:min_reader 4\nSELECT 1;":   "above its own level 3",
		"-- agentswarm:min_reader 0\nSELECT 1;":   "below 1",
		"-- agentswarm:min_reader\nSELECT 1;":     "is not",
		"-- agentswarm:min_reader two\nSELECT 1;": "is not",
		"-- agentswarm:min_reader 1 2\nSELECT 1;": "is not",
		"-- agentswarm:min_reader -1\nSELECT 1;":  "is not",
		"-- agentswarm:min_readers 1\nSELECT 1;":  "is not",
	} {
		_, err := readMigrations(fstest.MapFS{"0003_t.sql": {Data: []byte(script)}})
		require.ErrorContains(t, err, why, script)
		require.ErrorContains(t, err, "0003_t.sql", script)
	}

	for _, names := range [][]string{{"t.sql"}, {"0000_t.sql"}, {"1a_t.sql"}, {"0001_a.sql", "001_b.sql"}} {
		fsys := fstest.MapFS{}
		for _, n := range names {
			fsys[n] = &fstest.MapFile{Data: []byte("SELECT 1;")}
		}
		_, err := readMigrations(fsys)
		require.Error(t, err, "%v", names)
	}

	ms, err := readMigrations(fstest.MapFS{
		"0010_c.sql": {Data: []byte("SELECT 3;")}, "0002_b.sql": {Data: []byte("SELECT 2;")},
		"001_a.sql": {Data: []byte("SELECT 1;")}, "README.md": {Data: []byte("not a migration")},
	})
	require.NoError(t, err)
	levels := make([]int, 0, len(ms))
	for _, m := range ms {
		levels = append(levels, m.level)
	}
	assert.Equal(t, []int{1, 2, 10}, levels, "in the order of their levels, whatever their names' widths")
}

// Migration 0010, the kill switch's table, adds a table with triggers, which
// no build before it can read: it declares no older reader, so its min_reader
// is its own level, 10, which raises the schema's (Plan 7b-1, Decision 2). It
// is this build's last migration.
func TestMigration10ReadersAreOfItsOwnLevel(t *testing.T) {
	ms := embedded(t)
	i := slices.IndexFunc(ms, func(m migration) bool { return m.name == "0010_kill_switch.sql" })
	require.GreaterOrEqual(t, i, 0, "the build embeds 0010_kill_switch.sql")

	assert.Equal(t, 10, ms[i].level)
	assert.Equal(t, 10, ms[i].minReader)
	assert.NotContains(t, ms[i].script, declaration, "it declares no reader")
	assert.Equal(t, 10, Level(), "the build's level")
}
