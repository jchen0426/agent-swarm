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
	candidates = append(candidates, agentsdk)
}

var agentsdk = candidate{
	name:   "agentsdk",
	image:  sdkImage,
	prefix: "mcp__gw__",
	cmd: func(prompt string, limit time.Duration) []string {
		return []string{"python3", "/app/run_round.py", "--system-file", "/in/system.md",
			"--prompt-file", "/in/" + prompt, "--round-seconds", fmt.Sprint(limit.Seconds())}
	},
	env: map[string]string{"ANTHROPIC_BASE_URL": "http://llmgw:8080", "ANTHROPIC_API_KEY": llmgw.DummyKey},
}

// A positive control for criteria 2 and 5: with the SDK's defaults, the canaries
// must show up, or the hardened runs above would prove nothing.
func TestCanariesCatchTheSDKDefaults(t *testing.T) {
	s := newSandbox(t, []llmgw.Turn{use("get_alert"), use("end_turn")}, "-tools", "get_alert,end_turn")
	defaults := agentsdk
	defaults.cmd = func(prompt string, limit time.Duration) []string {
		return append(agentsdk.cmd(prompt, limit), "--defaults")
	}

	s.runRound(defaults, "prompt-round1.md", time.Minute)

	var leaked []string
	for _, rec := range s.records() {
		for _, canary := range contentCanaries {
			if strings.Contains(string(rec.Body), canary) && !slices.Contains(leaked, canary) {
				leaked = append(leaked, canary)
			}
		}
	}
	started := markers(t, s.marks)
	t.Logf("with the SDK's defaults: leaked %v; host configuration started %v", leaked, started)
	assert.True(t, len(leaked) > 0 || len(started) > 0, "the canaries detected nothing")
}
