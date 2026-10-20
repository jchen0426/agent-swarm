//go:build integration

package main

import (
	"context"
	"fmt"
	"io/fs"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
)

// freshDatabase creates a database of the package's container that no
// migration has touched, drops it when the test ends, and returns its URL.
func freshDatabase(t *testing.T, name string) *neturl.URL {
	t.Helper()
	admin := pgtest.Pool(t)
	_, err := admin.Exec(t.Context(), "CREATE DATABASE "+name)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		assert.NoError(t, err)
	})
	u, err := neturl.Parse(pgtest.URL(t))
	require.NoError(t, err)
	u.Path = "/" + name
	return u
}

// migrateOutput returns what a migrate run that applies every embedded
// migration prints, and the line that ends it.
func migrateOutput(t *testing.T) (all, last string) {
	t.Helper()
	names, err := fs.Glob(db.Migrations(), "*.sql")
	require.NoError(t, err)
	require.NotEmpty(t, names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString("applied " + name + "\n")
	}
	last = fmt.Sprintf("schema at level %d (%s)\n", db.Level(), names[len(names)-1])
	return b.String() + last, last
}

func TestMigratePrintsWhatItAppliedAndTheLevel(t *testing.T) {
	u := freshDatabase(t, "migrate_cli_env")
	password, _ := u.User.Password()
	for name, value := range map[string]string{
		"PGHOST": u.Hostname(), "PGPORT": u.Port(), "PGUSER": u.User.Username(), "PGPASSWORD": password,
		"PGDATABASE": strings.TrimPrefix(u.Path, "/"), "PGSSLMODE": "disable",
	} {
		t.Setenv(name, value)
	}
	all, last := migrateOutput(t)

	out, errOut, code := runCommand("migrate")

	require.Equal(t, exitOK, code, errOut)
	assert.Equal(t, all, out)

	out, errOut, code = runCommand("migrate")

	require.Equal(t, exitOK, code, errOut)
	assert.Equal(t, last, out, "a second run applies nothing")
}

func TestMigrateTakesTheURLFromASecret(t *testing.T) {
	u := freshDatabase(t, "migrate_cli_secret")
	secrets := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(secrets, "database"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(secrets, "database", "url"), []byte(u.String()+"\n"), 0o600))
	// The environment names no database that answers.
	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGPORT", "1")
	all, last := migrateOutput(t)

	out, errOut, code := runCommand("migrate", "--secrets", secrets, "--database", "secret://database/url")

	require.Equal(t, exitOK, code, errOut)
	assert.Equal(t, all, out)

	out, errOut, code = runCommand("migrate", "--secrets", secrets, "--database", "secret://database/url")

	require.Equal(t, exitOK, code, errOut)
	assert.Equal(t, last, out)

	_, errOut, code = runCommand("migrate", "--secrets", secrets)

	assert.Equal(t, exitUsage, code, "a secrets directory without a reference")
	assert.Contains(t, errOut, "database")
}
