package holding_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/holding"
)

// A context carries the epoch it was given, through the contexts derived
// from it, a detached one included, until another epoch is given.
func TestAContextCarriesItsEpoch(t *testing.T) {
	ctx := holding.WithEpoch(t.Context(), 3)
	derived, cancel := context.WithCancel(ctx)
	defer cancel()

	for name, c := range map[string]context.Context{
		"the context given it": ctx,
		"a derived context":    derived,
		"a detached context":   context.WithoutCancel(derived),
	} {
		epoch, err := holding.EpochFrom(c)

		require.NoError(t, err, name)
		assert.Equal(t, int64(3), epoch, name)
	}
	later, err := holding.EpochFrom(holding.WithEpoch(ctx, 4))
	require.NoError(t, err)
	assert.Equal(t, int64(4), later, "a later epoch replaces it")
}

// A context never given an epoch has none: a write under it is a bug.
func TestAContextWithoutAnEpochHasNone(t *testing.T) {
	epoch, err := holding.EpochFrom(t.Context())

	require.ErrorIs(t, err, holding.ErrNoEpoch)
	assert.Zero(t, epoch)
	assert.NotErrorIs(t, err, holding.ErrLeaseLost, "no epoch is a bug, not a lost lease")
}

// Epochs count from 1: a context given one below 1 has none.
func TestAnEpochBelowOneIsNone(t *testing.T) {
	for _, given := range []int64{0, -1} {
		epoch, err := holding.EpochFrom(holding.WithEpoch(t.Context(), given))

		require.ErrorIs(t, err, holding.ErrNoEpoch, given)
		assert.Zero(t, epoch, given)
	}
}
