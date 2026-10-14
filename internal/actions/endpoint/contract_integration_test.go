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
)

func TestMain(m *testing.M) {
	os.Exit(pgtest.Main(m))
}

// Spec Section 12.2, against the audit store the platform runs on: when a
// request reaches the server, its record is committed in Postgres.
func TestTheCommittedAuditRecordExistsWhenTheServerReceivesTheCall(t *testing.T) {
	calls := postgres.NewCalls(pgtest.Pool(t), "contract")
	s := newStack(t, calls, calls)
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
