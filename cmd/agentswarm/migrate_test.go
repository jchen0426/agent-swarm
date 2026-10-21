package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearLibpqEnv blanks, until the test ends, every PG variable the test
// process has, so that only what the test sets names a database. pgx reads
// libpq's variables, none of which is set otherwise, and ignores a blank one;
// a service the developer's environment names would otherwise take over the
// connection, and could be a database that accepts the login.
func clearLibpqEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "PG") {
			t.Setenv(name, "")
		}
	}
}

// The database URL holds a password, which migrate never prints: not when the
// URL does not parse, its password holding a colon included, of which pgx's
// own words would leave a part, nor when the database cannot be reached,
// whether a secret or libpq's environment gives it, nor when the URL is given
// where its reference belongs. Of a URL that does not parse it says only
// that, naming the flag that gave it.
func TestMigrateNeverPrintsTheDatabasePassword(t *testing.T) {
	clearLibpqEnv(t)
	for url, why := range map[string]string{
		"postgres://u:topsecret@%zz/db":                           "--database: the database URL does not parse",
		"postgres://u:top:secret@%zz/db":                          "--database: the database URL does not parse",
		"postgres://u:topsecret@127.0.0.1:1/db?connect_timeout=2": "ping postgres",
	} {
		secrets := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(secrets, "database"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(secrets, "database", "url"), []byte(url+"\n"), 0o600))

		out, errOut, code := runCommand("migrate", "--secrets", secrets, "--database", "secret://database/url")

		assert.Equal(t, exitError, code, why)
		assert.Contains(t, errOut, why)
		for _, part := range []string{"topsecret", "top:", ":secret"} {
			assert.NotContains(t, out+errOut, part, "%s: %s", url, why)
		}
	}

	out, errOut, code := runCommand("migrate", "--secrets", t.TempDir(), "--database", "postgres://u:topsecret@127.0.0.1:1/db")

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "not a secret reference")
	assert.NotContains(t, out+errOut, "topsecret")

	for name, value := range map[string]string{
		"PGHOST": "127.0.0.1", "PGPORT": "1", "PGUSER": "u", "PGPASSWORD": "topsecret", "PGDATABASE": "db", "PGCONNECT_TIMEOUT": "2",
	} {
		t.Setenv(name, value)
	}

	out, errOut, code = runCommand("migrate")

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "ping postgres")
	assert.NotContains(t, out+errOut, "topsecret")
}
