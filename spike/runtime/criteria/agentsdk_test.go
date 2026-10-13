//go:build spike

package criteria_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/spike/runtime/llmgw"
)

const sdkImage = "agentswarm-spike-agentsdk:latest"

func init() {
	images[sdkImage] = "sandbox/agentsdk.Dockerfile"
	candidates = append(candidates, sdkCandidate("agentsdk", "full"), sdkCandidate("agentsdk-min", "minimal"))
}

// sdkCandidate runs candidate B at one hardening level of run_round.py: full,
// minimal (setting_sources=[] and strict_mcp_config only) or none.
func sdkCandidate(name, hardening string) candidate {
	return candidate{
		name:   name,
		image:  sdkImage,
		prefix: "mcp__gw__",
		cmd: func(prompt string, limit time.Duration) []string {
			return []string{"python3", "/app/run_round.py", "--system-file", "/in/system.md",
				"--prompt-file", "/in/" + prompt, "--round-seconds", fmt.Sprint(limit.Seconds()), "--hardening", hardening}
		},
		env: map[string]string{"ANTHROPIC_BASE_URL": "http://llmgw:8080", "ANTHROPIC_API_KEY": llmgw.DummyKey},
	}
}

// A positive control for criteria 2 and 5: without hardening, the canaries that
// a prompt or a start-up path can reach must each show up, or the hardened runs
// would prove nothing. The run still keeps tools=[], the gateway as the only MCP
// server, dontAsk, the end_turn hook and the agent's own system prompt. The
// other canaries have no known path into a prompt here; they are only logged.
func TestCanariesCatchTheUnhardenedSDK(t *testing.T) {
	s := newSandbox(t, []llmgw.Turn{use("get_alert"), use("end_turn")}, "-tools", "get_alert,end_turn")

	s.runRound(sdkCandidate("agentsdk-unhardened", "none"), "prompt-round1.md", time.Minute)

	var leaked []string
	for _, rec := range s.records() {
		for _, canary := range contentCanaries {
			if strings.Contains(string(rec.Body), canary) && !slices.Contains(leaked, canary) {
				leaked = append(leaked, canary)
			}
		}
	}
	started := s.markers()
	t.Logf("without hardening: leaked %v; host configuration started %v", leaked, started)
	for _, canary := range []string{"CANARY-PROJECT-MEMORY", "CANARY-USER-MEMORY"} {
		assert.Contains(t, leaked, canary)
	}
	for _, marker := range []string{"project-mcp-started", "settings-hook-ran", "user-mcp-started"} {
		assert.Contains(t, started, marker)
	}
}
