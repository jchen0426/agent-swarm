//go:build integration && linux

package docker

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// TestASandboxedTurnReadsItsSpecAndItsExitGivesItsClass: on a live engine,
// with the tests' probe seam (R15) running sh -c 'head -c 1 >/dev/null; exit
// 10' in the probe image in place of a runtime, Open saves the image to the
// store; a turn launches in a sandbox of the runner's settings and is
// reported Launched; it reads the first byte of its spec and exits 10, so it
// ends ended; and its container is gone once its end is reported.
func TestASandboxedTurnReadsItsSpecAndItsExitGivesItsClass(t *testing.T) {
	needsTestDocker(t)
	c := newClient(t)
	o := itOptions(t, newTenant(t, c))
	o.probe = []string{"sh", "-c", "head -c 1 >/dev/null; exit 10"}
	probe := probeID(t, c)
	o.Images = map[string]string{"thinloop": probe}
	lr := openRunner(t, c, o)
	turn := liveTurn(probe)

	lr.start(t, turn)

	lr.end(t, turn)
	launches, exits := lr.reported()
	assert.Equal(t, []app.Launch{{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID}}, launches)
	assert.Equal(t, []app.Exit{{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID, Class: item.TurnEnded}}, exits)
	assert.True(t, lr.r.store.has(probe), "Open did not save the image to the store")
	lr.awaitGone(t, turn.ID)
}
