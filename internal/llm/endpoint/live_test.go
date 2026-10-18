//go:build live

package endpoint_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/runner"
)

const (
	// liveVendor is the model vendor's API.
	liveVendor = "https://api.anthropic.com"
	// livePrompt makes a turn of two calls, a tool call and then the end tool,
	// and asks for reasoning before the first. The reasoning it asks for is
	// what makes the model think: it thinks only when a task needs it, and the
	// check needs a thinking block to send back.
	livePrompt = `Three services call each other: web calls cart, cart calls pay. Errors at web rose at 10:02, at cart at 10:01, at pay never. Before any tool call, work out which service most likely broke first and why. Then call metrics__query once with q set to that service, then call end_turn.`
	// liveTurns is how many turns the check plays at most to cover the echo
	// of a thinking block: a turn may make a single call, and the model does
	// not always think.
	liveTurns = 3
)

// liveKey is the vendor's key. It is never printed: the one place it can
// come back is the text of an error, which withoutKey cleans.
type liveKey string

// Secret returns the key, whatever reference asks for it.
func (k liveKey) Secret(context.Context, string) (string, error) { return string(k), nil }

// withoutKey returns s with apiKey, if it holds it, left out.
func withoutKey(s, apiKey string) string {
	return strings.ReplaceAll(s, apiKey, "[the vendor's key]")
}

// liveTurnResult is what a turn covered.
type liveTurnResult struct {
	calls, echoed int
	outcome       runner.Outcome
}

// missing says what the turn left uncovered of the echo of a thinking block,
// or "" when it covered it. The echo takes two calls, so that an answer goes
// back through the gateway, and a thinking block in that answer.
func (r liveTurnResult) missing() string {
	switch {
	case r.calls < 2:
		return fmt.Sprintf("the turn made %d call(s) and ended by %s, so no answer went back through the gateway", r.calls, r.outcome)
	case r.echoed == 0:
		return fmt.Sprintf("the model gave no thinking block in the turn's %d calls (it ended by %s)", r.calls, r.outcome)
	}
	return ""
}

// thinkingBlocks counts the thinking blocks, redacted ones too, that a
// recorded request sends back.
func thinkingBlocks(t *testing.T, request string) int {
	t.Helper()
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(request), &req), "the recorded request of a forwarded call is not JSON")
	n := 0
	for i := range req.Messages {
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(req.Messages[i].Content, &blocks) != nil {
			continue // a string, which holds no block
		}
		for _, b := range blocks {
			if b.Type == "thinking" || b.Type == "redacted_thinking" {
				n++
			}
		}
	}
	return n
}

// playLiveTurn plays one turn of the platform's runtime through a new
// gateway to the real vendor, and returns what it covered. It fails the test
// at once if the gateway did not pass a request of the runtime's, which is the
// regression the live check exists for, if a call has no outcome, or if the
// runtime fails. A turn of a single call is no failure of the gateway: it
// covers no echo, and its caller plays another.
func playLiveTurn(t *testing.T, apiKey, modelName string) liveTurnResult {
	t.Helper()
	e := new(env)
	e.connect(t, &wiring{vendorURL: liveVendor, secrets: liveKey(apiKey), model: modelName, timeout: 2 * time.Minute, round: 5 * time.Minute})

	res, err := runner.Run(t.Context(), &e.client, toolbox{}, &runner.Config{
		Model: modelName, MaxTokens: 1024, MaxSteps: 5, System: "You investigate incidents.",
	}, livePrompt)

	records := e.calls.Records()
	failed := false
	for i := range records {
		_, o, _, ended := e.calls.Record(records[i].ID)
		switch {
		case !ended:
			t.Errorf("call %d of the runtime's turn has no outcome recorded", i+1)
			failed = true
		case o.Status != app.StatusOK:
			t.Errorf("the gateway did not pass call %d of the runtime's turn: status %q, reason: %s", i+1, o.Status, withoutKey(o.Reason, apiKey))
			failed = true
		}
	}
	if err != nil {
		t.Errorf("the runtime's turn failed: %s", withoutKey(err.Error(), apiKey))
		failed = true
	}
	if failed {
		t.FailNow()
	}
	// The second call's request sends the first answer back, and each later
	// one sends back all the answers before it.
	echoed := 0
	for i := 1; i < len(records); i++ {
		echoed = max(echoed, thinkingBlocks(t, records[i].Request))
	}
	return liveTurnResult{calls: len(records), echoed: echoed, outcome: res.Outcome}
}

// writeLiveCheck records that the live check passed, for the tripwire
// (live_check_test.go) to read.
func writeLiveCheck(t *testing.T, modelName string) {
	t.Helper()
	data, err := json.MarshalIndent(liveCheck{SDK: sdkVersion(t), Model: modelName, Checked: time.Now().UTC().Format(time.DateOnly)}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(liveCheckFile), 0o755))
	require.NoError(t, os.WriteFile(liveCheckFile, append(data, '\n'), 0o644))
}

// TestLiveTurnThroughTheGateway plays a turn of the platform's runtime
// through the gateway against the real model vendor, as agents will: every
// request the runtime sends must pass the allowlist, the echo of an answer's
// thinking block included. The allowlist admits what the runtime sends today
// (Decision 2), so this fails when a newer SDK or model sends more, before an
// agent's turn ends at its second call. It needs AGENTSWARM_LIVE_KEY, the
// vendor's key, and the network, and costs a few cents a run, so it runs only
// with the live tag (make test-live). AGENTSWARM_LIVE_MODEL names another
// model than the fixtures'. A turn that covers no echo of a thinking block, for
// it made a single call or the model gave no thinking block, is played again,
// up to three turns. On success it records the SDK it passed with.
func TestLiveTurnThroughTheGateway(t *testing.T) {
	apiKey := os.Getenv("AGENTSWARM_LIVE_KEY")
	if apiKey == "" {
		t.Skip("set AGENTSWARM_LIVE_KEY to the model vendor's key to run this against the vendor")
	}
	modelName := cmp.Or(os.Getenv("AGENTSWARM_LIVE_MODEL"), model)

	var missed []string
	for turn := 1; turn <= liveTurns; turn++ {
		covered := playLiveTurn(t, apiKey, modelName)
		why := covered.missing()
		if why == "" {
			writeLiveCheck(t, modelName)
			t.Logf("the live check passed with %s on %s: %d calls through the gateway, %d thinking blocks echoed, the turn ended by %s; recorded in %s",
				sdkVersion(t), modelName, covered.calls, covered.echoed, covered.outcome, liveCheckFile)
			return
		}
		t.Logf("turn %d did not cover the echo of a thinking block: %s", turn, why)
		missed = append(missed, fmt.Sprintf("turn %d: %s", turn, why))
	}
	t.Fatalf("%d turns did not cover the echo of a thinking block: %s", liveTurns, strings.Join(missed, "; "))
}
