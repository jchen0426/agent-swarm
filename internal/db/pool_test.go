package db_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/db"
)

// A pool from OpenWith is never unbounded: Postgres reads a timeout of 0 as
// none at all, and pgxpool a connect timeout of 0 as its own 2 minutes, so
// OpenWith refuses any of its timeouts below a millisecond, before it
// connects.
func TestABoundedPoolRefusesATimeoutThatBoundsNothing(t *testing.T) {
	for name, o := range map[string]db.PoolOptions{
		"no transaction timeout":        {IdleTimeout: time.Second, ConnectTimeout: time.Second},
		"no idle timeout":               {TransactionTimeout: time.Second, ConnectTimeout: time.Second},
		"no connect timeout":            {TransactionTimeout: time.Second, IdleTimeout: time.Second},
		"a timeout below a millisecond": {TransactionTimeout: time.Second, IdleTimeout: time.Microsecond, ConnectTimeout: time.Second},
	} {
		_, err := db.OpenWith(t.Context(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1", o)

		require.ErrorContains(t, err, "a millisecond or more", name)
	}
}

// A database URL that does not parse is named so, and nothing more: pgx's
// own words about it redact its password, but leave the part of one before a
// second colon. Every opener says only that, so that no command prints them.
func TestAURLThatDoesNotParseIsNamedWithoutItsPassword(t *testing.T) {
	const leaky = "postgres://u:k8f3q:z9w2v@%zz/db"
	_, open := db.Open(t.Context(), leaky)
	_, bounded := db.OpenWith(t.Context(), leaky, db.PoolOptions{TransactionTimeout: time.Second, IdleTimeout: time.Second, ConnectTimeout: time.Second})
	for name, err := range map[string]error{
		"CheckURL":    db.CheckURL(leaky),
		"CheckServer": db.CheckServer(t.Context(), leaky),
		"Open":        open,
		"OpenWith":    bounded,
	} {
		require.ErrorIs(t, err, db.ErrURL, name)
		for _, part := range []string{"k8f3q", "z9w2v"} {
			require.NotContains(t, err.Error(), part, name)
		}
	}
	require.NoError(t, db.CheckURL("postgres://u:k8f3q:z9w2v@127.0.0.1:5432/db"), "a password may hold a colon")
}
