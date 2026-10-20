package db_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/db"
)

// A pool from OpenWith is never unbounded: Postgres reads a timeout of 0 as
// none at all, so OpenWith refuses either timeout below a millisecond, before
// it connects.
func TestABoundedPoolRefusesATimeoutThatBoundsNothing(t *testing.T) {
	for name, o := range map[string]db.PoolOptions{
		"no transaction timeout":        {IdleTimeout: time.Second},
		"no idle timeout":               {TransactionTimeout: time.Second},
		"a timeout below a millisecond": {TransactionTimeout: time.Second, IdleTimeout: time.Microsecond},
	} {
		_, err := db.OpenWith(t.Context(), "postgres://nobody@127.0.0.1:1/none?connect_timeout=1", o)

		require.ErrorContains(t, err, "a millisecond or more", name)
	}
}
