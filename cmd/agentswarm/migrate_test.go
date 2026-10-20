package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The database URL holds a password, which migrate never prints: not when the
// URL does not parse, nor when the database cannot be reached, whether a
// secret or libpq's environment gives it.
func TestMigrateNeverPrintsTheDatabasePassword(t *testing.T) {
	for url, why := range map[string]string{
		"postgres://u:topsecret@%zz/db":                           "cannot parse",
		"postgres://u:topsecret@127.0.0.1:1/db?connect_timeout=2": "ping postgres",
	} {
		secrets := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(secrets, "database"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(secrets, "database", "url"), []byte(url+"\n"), 0o600))

		out, errOut, code := runCommand("migrate", "--secrets", secrets, "--database", "secret://database/url")

		assert.Equal(t, exitError, code, why)
		assert.Contains(t, errOut, why)
		assert.NotContains(t, out+errOut, "topsecret", why)
	}

	for name, value := range map[string]string{
		"PGHOST": "127.0.0.1", "PGPORT": "1", "PGUSER": "u", "PGPASSWORD": "topsecret", "PGDATABASE": "db", "PGCONNECT_TIMEOUT": "2",
	} {
		t.Setenv(name, value)
	}

	out, errOut, code := runCommand("migrate")

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "ping postgres")
	assert.NotContains(t, out+errOut, "topsecret")
}
