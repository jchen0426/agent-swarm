package app

import (
	"encoding/json"
	"math/rand/v2"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/redact"
	"github.com/jchen0426/agent-swarm/internal/llm/prompt"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// defaultSecrets is the example configuration's rule that applies always
// (examples/config/redaction-rules/default-secrets.yaml).
var defaultSecrets = redact.Rule{Replacement: "[redacted:default-secrets]", Patterns: []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/-]{16,}=*`),
	regexp.MustCompile(`(?i)\b(password|passwd|secret|api[_-]?key)\s*[:=]\s*[^\s,;]{8,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----(?:[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----|[A-Za-z0-9+/=:,\s\\-]*)`),
	regexp.MustCompile(`(?i)\b[a-z]+://[^\s:/@]+:[^\s@/]+@`),
}}

// pieces are what drawn texts are made of: keywords, the separators of
// members, quotes, backslashes and line breaks, values, a rule's replacement
// and HTML characters, as agents copy them from logs and configuration.
var pieces = []string{"password", "secret", "api_key", "user", `"`, `": "`, `":"`, ": ", "=", " ", ", ", "hunter2hunter2",
	"abcd1234efgh", "x", `\`, "\n", "{", "}", `\"`, "level=info", "Bearer ", "AKIA", "0123456789ABCDEF", "<", "&",
	"[redacted:default-secrets]", "app.yaml"}

// draw returns a text of one to eight pieces.
func draw(rng *rand.Rand) string {
	var b strings.Builder
	for range 1 + rng.IntN(8) {
		b.WriteString(pieces[rng.IntN(len(pieces))])
	}
	return b.String()
}

// throughLLMGateway sends text to the LLM gateway's checks as a tool's
// result, and returns the text the gateway forwards, or its refusal.
func throughLLMGateway(t *testing.T, red *redact.Redactor, text string) (string, error) {
	t.Helper()
	quoted, err := json.Marshal(text)
	require.NoError(t, err)
	body := `{"model": "m", "max_tokens": 10, "messages": [{"role": "user", "content": "Investigate."},
 {"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_1", "name": "read_board", "input": {}}]},
 {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": [{"type": "text", "text": ` + string(quoted) + `}]}]}]}`
	req, err := prompt.Check(map[string][]string{}, []byte(body), &prompt.Policy{Model: "m", MaxTokens: 10, Version: "2023-06-01"})
	require.NoError(t, err)
	if err := req.Redact(red); err != nil {
		return "", err
	}
	var doc struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(req.Encode(), &doc))
	var results []struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(doc.Messages[2].Content, &results))
	return results[0].Content[0].Text, nil
}

// A snapshot of accepted posts passes the LLM gateway, and the model reads it
// unchanged. The board keeps an agent's text redacted at its own level, and
// read_board writes the snapshot as JSON, which the LLM gateway reads value
// by value, as the MCP gateway reads JSON output. Read as text, the quotes of
// a post such as grep shows password": "hunter2hunter2", escaped in the
// snapshot, close a key, and every agent's turn after read_board would be
// refused. Posts are drawn from a fixed seed.
func TestASnapshotOfAcceptedPostsPassesTheLLMGateway(t *testing.T) {
	red := redact.New(defaultSecrets)
	rng := rand.New(rand.NewPCG(5, 6))
	agent := board.Author{Kind: board.AuthorAgent, Name: "flag-investigator"}
	posts := []string{`grep shows password": "hunter2hunter2" in app.yaml`}
	for range 1000 {
		posts = append(posts, draw(rng))
	}
	snapshots := 0
	for i, post := range posts {
		claim, err := agentText(red, post)
		if err != nil {
			continue
		}
		quote := draw(rng)
		if agentQuote(red, nil, quote) != nil {
			quote = ""
		}
		statement, err := agentText(red, posts[(i+1)%len(posts)])
		if err != nil {
			statement = "the rollout broke checkout"
		}
		view := board.View{
			Round: 2, Snapshot: 2,
			Assessment: board.Assessment{Hypotheses: []board.HypothesisView{{ID: "e-2", Statement: statement, Author: agent}}},
			Entries: []board.Event{
				{Seq: 1, ID: "e-1", Round: 1, Author: agent, Body: board.Body{Evidence: &board.Evidence{
					Claim: claim, Provenance: board.Provenance{Kind: board.FromReadCall, CallID: "c-1", Quote: quote, Source: "logs/search"},
				}}},
				{Seq: 2, ID: "e-2", Round: 1, Author: agent, Body: board.Body{Hypothesis: &board.Hypothesis{Statement: statement, Evidence: []board.EntryID{"e-1"}}}},
			},
		}
		snapshot, _, err := renderView(&view)
		require.NoError(t, err)

		forwarded, err := throughLLMGateway(t, red, snapshot)

		require.NoError(t, err, "the LLM gateway refuses a snapshot of accepted posts: %s", snapshot)
		assert.JSONEq(t, snapshot, forwarded)
		snapshots++
	}
	t.Logf("%d snapshots of accepted posts passed", snapshots)
}
