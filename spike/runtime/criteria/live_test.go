//go:build spike && live

package criteria_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
)

// TestLiveDiagnosis runs each candidate three times against the real model,
// through the LLM gateway stand-in in forward mode. The key stays in the
// gateway's container, outside the sandbox. Run it with:
//
//	ANTHROPIC_API_KEY=... go test -tags 'spike live' -count=1 -v -run TestLiveDiagnosis ./criteria
func TestLiveDiagnosis(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("set ANTHROPIC_API_KEY to run against the real model")
	}
	forEach(t, func(t *testing.T, c candidate) {
		hits := 0
		for run := 1; run <= 3; run++ {
			s := emptySandbox(t)
			s.gateway("llmgw", []string{"llmgw", "-upstream", "https://api.anthropic.com", "-log", "/out/llm.jsonl"},
				network.WithBridgeNetwork(), testcontainers.WithEnv(map[string]string{"ANTHROPIC_API_KEY": key}))
			s.startMCP("-tools", "get_alert,get_recent_changes,query_logs,post_hypothesis,end_turn")

			r := s.runRound(c, "prompt-round1.md", 3*time.Minute)

			require.Equal(t, 0, r.exit)
			var keys []string
			ended := false
			for _, call := range s.calls() {
				switch call.Tool {
				case "post_hypothesis":
					var args struct {
						Key string `json:"key"`
					}
					_ = json.Unmarshal(call.Args, &args)
					keys = append(keys, args.Key)
				case "end_turn":
					ended = true
				}
			}
			assert.True(t, ended, "run %d: the agent ends its turn", run)
			for _, rec := range s.records() {
				assert.NotEqual(t, "other", rec.KeyHeader, "run %d: request %d left the sandbox with a real key", run, rec.Seq)
			}
			if len(keys) == 1 && keys[0] == "bad-release/cart" {
				hits++
			}
			t.Logf("%s run %d: hypotheses %v, %d model calls, %s", c.name, run, keys, len(agentLoop(s.records())), r.took.Round(time.Second))
		}
		t.Logf("%s: %d of 3 runs reached bad-release/cart", c.name, hits)
	})
}
