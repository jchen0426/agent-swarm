//go:build integration

package endpoint_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/postgres"
	"github.com/jchen0426/agent-swarm/internal/testsupport/fakemcp"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	workpostgres "github.com/jchen0426/agent-swarm/internal/work/postgres"
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

// Spec Section 12.2, against the audit store the platform runs on: when a
// request reaches the server, its record is committed in Postgres.
func TestTheCommittedAuditRecordExistsWhenTheServerReceivesTheCall(t *testing.T) {
	pool := pgtest.Pool(t)
	calls := postgres.NewCalls(pool, "contract")
	s := newStack(t, calls, calls, workpostgres.NewEventStore(pool, "contract"))
	var found, finished bool
	s.server.OnCall(func(fakemcp.Call) {
		c, err := calls.ReadCall(context.Background(), "call-1")
		found, finished = err == nil, err == nil && !c.Failed
	})

	res := s.call(t, "flags__get_flag", map[string]any{"flag": "checkout-v2", "window": "1h"})

	require.Equal(t, "call-1", callID(res))
	assert.True(t, found, "the record was committed before the call arrived")
	assert.False(t, finished, "its outcome came after")
	stored, err := calls.ReadCall(t.Context(), "call-1")
	require.NoError(t, err)
	assert.False(t, stored.Failed)
}

// The record-only property against the stores the platform runs on: the
// proposal is committed in Postgres with the board, the write call is
// recorded and cannot be cited, and the server received reads only.
func TestARecordedWriteIsCommittedAndNeverReachesTheServer(t *testing.T) {
	pool := pgtest.Pool(t)
	calls := postgres.NewCalls(pool, "contract-write")
	events := workpostgres.NewEventStore(pool, "contract-write")
	s := newStack(t, calls, calls, events)
	h := s.hypothesis(t)

	res := s.call(t, "flags__set_flag", map[string]any{"flag": "checkout-v2", "on": false, "_hypothesis": h})

	require.False(t, res.IsError, textOf(res))
	committed, err := events.Load(t.Context(), "run-1")
	require.NoError(t, err)
	last := committed[len(committed)-1].Body.Proposal
	require.NotNil(t, last)
	assert.Equal(t, "flags.set_flag", last.Tool)
	call, err := calls.ReadCall(t.Context(), callID(res))
	require.NoError(t, err)
	assert.True(t, call.Failed, "a recorded write call is no read to cite")
	for _, c := range s.server.Calls() {
		assert.Equal(t, "get_flag", c.Tool)
	}
}
