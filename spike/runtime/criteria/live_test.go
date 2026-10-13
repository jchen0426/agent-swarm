//go:build spike && live

package criteria_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
)

// liveModels are the models the forwarding stand-in lets a request name: the
// candidates' model, and the small model a runtime may use for side requests.
const liveModels = "claude-sonnet-5-5,claude-haiku-5-5"

// TestLiveDiagnosis runs each decision candidate three times against the real
// model, through the LLM gateway stand-in in forward mode. The key stays in the
// stand-in's container, outside the sandbox. A run's failure is counted, not
// fatal, so that point 1 of the decision rule, end_turn in at least 2 of 3 runs,
// can be judged. Run it with:
//
//	ANTHROPIC_API_KEY=... go test -tags 'spike live' -count=1 -v -run TestLiveDiagnosis ./criteria
func TestLiveDiagnosis(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("set ANTHROPIC_API_KEY to run against the real model")
	}
	forEach(t, func(t *testing.T, c candidate) {
		if strings.HasSuffix(c.name, "-min") {
			t.Skip("an ablation of agentsdk, not a decision candidate")
		}
		ended, hits := 0, 0
		for run := 1; run <= 3; run++ {
			s := emptySandbox(t)
			s.gateway("llmgw", []string{"llmgw", "-upstream", "https://api.anthropic.com", "-models", liveModels, "-log", "/out/llm.jsonl"},
				network.WithBridgeNetwork(), testcontainers.WithEnv(map[string]string{"ANTHROPIC_API_KEY": key}))
			s.startMCP("-tools", "get_alert,get_recent_changes,query_logs,post_hypothesis,end_turn")

			r := s.runRound(c, "prompt-round1.md", 3*time.Minute)

			var keys []string
			endedRun := false
			for _, call := range s.calls() {
				switch call.Tool {
				case "post_hypothesis":
					var args struct {
						Key string `json:"key"`
					}
					_ = json.Unmarshal(call.Args, &args)
					keys = append(keys, args.Key)
				case "end_turn":
					endedRun = true
				}
				for _, canary := range contentCanaries {
					assert.NotContains(t, string(call.Args), canary, "run %d: tool call %s", run, call.Tool)
				}
			}
			for _, rec := range s.records() {
				assert.NotEqual(t, "other", rec.KeyHeader, "run %d: request %d left the sandbox with a real key", run, rec.Seq)
				for _, canary := range contentCanaries {
					assert.NotContains(t, string(rec.Body), canary, "run %d: request %d", run, rec.Seq)
				}
			}
			if endedRun {
				ended++
			}
			if len(keys) == 1 && keys[0] == "bad-release/cart" {
				hits++
			}
			t.Logf("%s run %d: exit %d, hypotheses %v, ended %v, %d model calls, %s",
				c.name, run, r.exit, keys, endedRun, len(agentLoop(s.records())), r.took.Round(time.Second))
		}
		t.Logf("%s: %d of 3 runs ended with end_turn; %d reached bad-release/cart", c.name, ended, hits)
		assert.GreaterOrEqual(t, ended, 2, "decision rule, point 1: end_turn in at least 2 of 3 runs")
	})
}
