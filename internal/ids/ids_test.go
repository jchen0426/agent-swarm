package ids_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/ids"
)

func TestUUIDv7IDsAreVersion7AndOrdered(t *testing.T) {
	var gen ids.UUIDv7
	prev := gen.NewID()
	for range 1000 {
		next := gen.NewID()
		u, err := uuid.Parse(next)
		require.NoError(t, err)
		assert.Equal(t, uuid.Version(7), u.Version())
		assert.Greater(t, next, prev)
		prev = next
	}
}
