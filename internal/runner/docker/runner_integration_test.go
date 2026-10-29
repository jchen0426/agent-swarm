//go:build integration && linux

package docker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/tokens"
)

// TestASandboxedTurnReadsItsSpecAndItsExitGivesItsClass: on a live engine,
// with the tests' probe seam (R15) running sh -c 'head -c 1 >/dev/null; exit
// 10' in the probe image in place of a runtime, Open saves the image to the
// store; a turn launches in a sandbox of the runner's settings and is
// reported Launched; it reads the first byte of its spec and exits 10, so it
// ends ended; and its container is gone once its end is reported.
func TestASandboxedTurnReadsItsSpecAndItsExitGivesItsClass(t *testing.T) {
	c := newClient(t)
	o := itOptions(t, newTenant(t, c))
	o.probe = []string{"sh", "-c", "head -c 1 >/dev/null; exit 10"}
	probe := probeID(t, c)
	o.Images = map[string]string{"thinloop": probe}
	engine, err := Dial(engineEndpoint)
	require.NoError(t, err)
	var mu sync.Mutex
	var launches []app.Launch
	var exits []app.Exit
	p := itPorts(t, engine)
	p.Exited = func(x app.Exit) {
		mu.Lock()
		defer mu.Unlock()
		exits = append(exits, x)
	}
	p.Launched = func(l app.Launch) {
		mu.Lock()
		defer mu.Unlock()
		launches = append(launches, l)
	}
	r, err := Open(t.Context(), p, o)
	if err != nil {
		_ = engine.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() {
		// t.Context has ended by the time a cleanup runs.
		r.KillAll(context.Background())
		_ = r.Close()
	})
	turn := testTurn(probe)
	turn.ID = "it-" + hex8()
	turn.MCP, turn.LLM = tokens.New(tokens.MCP), tokens.New(tokens.LLM)

	pending, err := r.Start(holding.WithEpoch(t.Context(), epoch), turn)

	require.NoError(t, err)
	require.True(t, pending)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(exits) > 0
	}, itWait, 50*time.Millisecond, "the turn's end was not reported")
	mu.Lock()
	assert.Equal(t, []app.Launch{{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID}}, launches)
	assert.Equal(t, []app.Exit{{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID, Class: item.TurnEnded}}, exits)
	mu.Unlock()
	assert.True(t, r.store.has(probe), "Open did not save the image to the store")
	assert.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := engine.Inspect(ctx, containerName(turn.ID))
		return errors.Is(err, ErrNoContainer)
	}, itWait, 50*time.Millisecond, "the turn's container is not gone")
}
