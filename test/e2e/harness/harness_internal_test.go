//go:build e2e

package harness

import (
	"testing"

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

func TestPsqlInAGivenDatabaseNeedsItsContainer(t *testing.T) {
	d, err := externalDatabase("postgres://alice:s3cret@db.example:5432/orders", "")
	require.NoError(t, err)

	_, err = d.psql(t.Context(), "select 1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), databaseContainerEnv)
}
