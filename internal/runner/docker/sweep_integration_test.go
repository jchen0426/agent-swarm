//go:build integration && linux

package docker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTheStartSweepRemovesAContainerOfThePreviousProcess (Decision 5): on a
// live engine, a stopped container of the sandbox's settings, labelled with
// the test's tenant and holder and with agentswarm.project=agent-swarm, as
// the holder's previous process leaves one, is gone once Open returns. The
// tests' probe seam (R15) runs true in the probe image, so that the
// container stops at once.
func TestTheStartSweepRemovesAContainerOfThePreviousProcess(t *testing.T) {
	c := newClient(t)
	e := newEngine(t)
	o := itOptions(t, newTenant(t, c))
	o.probe = []string{"true"}
	turn := testTurn(probeID(t, c))
	turn.ID = "it-" + hex8()
	ctx, cancel := context.WithTimeout(t.Context(), itWait)
	defer cancel()
	name, cfg, host := o.sandbox(turn)
	require.Equal(t, "agent-swarm", cfg.Labels["agentswarm.project"])
	id, err := e.Create(ctx, name, cfg, host)
	require.NoError(t, err)
	require.NoError(t, e.Start(ctx, id))
	require.NoError(t, e.Wait(ctx, id))
	previous, err := e.Inspect(ctx, id)
	require.NoError(t, err)
	require.False(t, previous.Running, "the previous process's container did not stop")
	require.Equal(t, o.Holder, previous.Labels[labelHolder])

	openRunner(t, c, o)

	_, err = e.Inspect(ctx, id)
	require.ErrorIs(t, err, ErrNoContainer, "the previous process's container is still on the engine")
}
