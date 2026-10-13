package thinloop_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/spike/runtime/llmgw"
	"github.com/jchen0426/agent-swarm/spike/runtime/mcpgw"
	"github.com/jchen0426/agent-swarm/spike/runtime/thinloop"
)

type rig struct {
	llm   *llmgw.Gateway
	gw    *mcpgw.Gateway
	model *anthropic.Client
	tools *mcp.ClientSession
}

func newRig(t *testing.T, script []llmgw.Turn, opts mcpgw.Options, tools ...string) *rig {
	t.Helper()
	r := &rig{llm: llmgw.New(&llmgw.Options{Script: script}), gw: mcpgw.New(opts)}
	llmSrv := httptest.NewServer(r.llm)
	t.Cleanup(llmSrv.Close)
	gwSrv := httptest.NewServer(r.gw)
	t.Cleanup(gwSrv.Close)
	r.gw.SetRound(1, tools...)
	model := anthropic.NewClient(option.WithoutEnvironmentDefaults(), option.WithBaseURL(llmSrv.URL),
		option.WithAPIKey(llmgw.DummyKey), option.WithMaxRetries(0))
	r.model = &model
	r.tools = connect(t, gwSrv.URL)
	return r
}

func connect(t *testing.T, url string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "thinloop-test", Version: "0"}, nil)
	cs, err := c.Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: url + "/mcp", DisableStandaloneSSE: true}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func use(name string) llmgw.Turn { return llmgw.Turn{ToolUse: &llmgw.ToolUse{Name: name}} }

var cfg = thinloop.Config{Model: "spike-model", MaxTokens: 512, MaxSteps: 10, System: "SKILL-TEXT"}

func requestTools(t *testing.T, body json.RawMessage) []string {
	t.Helper()
	var req struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(body, &req))
	var names []string
	for _, tool := range req.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func TestTheTurnEndsWhenTheAgentCallsTheEndTool(t *testing.T) {
	r := newRig(t, []llmgw.Turn{use("get_alert"), use("post_hypothesis"), use("end_turn")},
		mcpgw.Options{}, "get_alert", "post_hypothesis", "end_turn")

	res, err := thinloop.Run(context.Background(), r.model, r.tools, cfg, "SNAPSHOT")

	require.NoError(t, err)
	assert.Equal(t, thinloop.EndedByTool, res.Outcome)
	assert.Equal(t, 3, res.Steps)
	var called []string
	for _, c := range r.gw.Calls() {
		called = append(called, c.Tool)
	}
	assert.Equal(t, []string{"get_alert", "post_hypothesis", "end_turn"}, called)
	assert.Len(t, r.llm.Records(), 3, "no model call after end_turn")
}

func TestTheToolListIsReadOnceARound(t *testing.T) {
	r := newRig(t, []llmgw.Turn{use("get_alert"), use("get_alert"), use("end_turn")},
		mcpgw.Options{AddAfterFirstCall: "probe_extra"}, "get_alert", "end_turn")

	_, err := thinloop.Run(context.Background(), r.model, r.tools, cfg, "SNAPSHOT")

	require.NoError(t, err)
	for _, rec := range r.llm.Records() {
		assert.Equal(t, []string{"end_turn", "get_alert"}, requestTools(t, rec.Body))
	}
}

func TestRequestsHoldOnlyTheSkillThePromptAndTheTurn(t *testing.T) {
	r := newRig(t, []llmgw.Turn{use("end_turn")}, mcpgw.Options{}, "end_turn")

	_, err := thinloop.Run(context.Background(), r.model, r.tools, cfg, "SNAPSHOT")

	require.NoError(t, err)
	records := r.llm.Records()
	require.Len(t, records, 1)
	var body struct {
		System   []map[string]any `json:"system"`
		Messages []map[string]any `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(records[0].Body, &body))
	require.Len(t, body.System, 1)
	assert.Equal(t, "SKILL-TEXT", body.System[0]["text"])
	require.Len(t, body.Messages, 1)
	assert.Contains(t, string(records[0].Body), `"text":"SNAPSHOT"`)
	assert.Equal(t, "dummy", records[0].KeyHeader)
}

func TestNothingCarriesOverBetweenRounds(t *testing.T) {
	r := newRig(t, []llmgw.Turn{use("get_alert"), use("end_turn"), use("end_turn")},
		mcpgw.Options{Outputs: map[string]string{"get_alert": "ROUND-ONE-ONLY"}}, "get_alert", "end_turn")
	_, err := thinloop.Run(context.Background(), r.model, r.tools, cfg, "SNAPSHOT 1")
	require.NoError(t, err)
	seen := len(r.llm.Records())

	_, err = thinloop.Run(context.Background(), r.model, r.tools, cfg, "SNAPSHOT 2")

	require.NoError(t, err)
	for _, rec := range r.llm.Records()[seen:] {
		assert.NotContains(t, string(rec.Body), "ROUND-ONE-ONLY")
		assert.NotContains(t, string(rec.Body), "SNAPSHOT 1")
	}
}

func TestTheDeadlineEndsTheTurn(t *testing.T) {
	r := newRig(t, []llmgw.Turn{use("slow_read")},
		mcpgw.Options{Delays: map[string]time.Duration{"slow_read": 5 * time.Second}}, "slow_read", "end_turn")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := thinloop.Run(ctx, r.model, r.tools, cfg, "SNAPSHOT")

	require.NoError(t, err)
	assert.Equal(t, thinloop.EndedByDeadline, res.Outcome)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestMaxStepsEndsTheTurn(t *testing.T) {
	r := newRig(t, []llmgw.Turn{use("get_alert"), use("get_alert"), use("get_alert")},
		mcpgw.Options{}, "get_alert", "end_turn")
	limited := cfg
	limited.MaxSteps = 2

	res, err := thinloop.Run(context.Background(), r.model, r.tools, limited, "SNAPSHOT")

	require.NoError(t, err)
	assert.Equal(t, thinloop.EndedBySteps, res.Outcome)
	assert.Equal(t, 2, res.Steps)
}

func TestAModelThatStopsCallingToolsEndsTheTurn(t *testing.T) {
	r := newRig(t, []llmgw.Turn{{Text: "I am done"}}, mcpgw.Options{}, "get_alert", "end_turn")

	res, err := thinloop.Run(context.Background(), r.model, r.tools, cfg, "SNAPSHOT")

	require.NoError(t, err)
	assert.Equal(t, thinloop.EndedByModel, res.Outcome)
	assert.True(t, strings.HasPrefix(string(res.Outcome), "no_tool"))
}

func TestToolSchemasReachTheModelUnchanged(t *testing.T) {
	r := newRig(t, []llmgw.Turn{use("end_turn")}, mcpgw.Options{}, "end_turn")

	_, err := thinloop.Run(context.Background(), r.model, r.tools, cfg, "SNAPSHOT")

	require.NoError(t, err)
	var body struct {
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(r.llm.Records()[0].Body, &body))
	require.Len(t, body.Tools, 1)
	assert.Equal(t, "End your turn for this round.", body.Tools[0].Description)
	assert.JSONEq(t, `{"type":"object","additionalProperties":true}`, string(body.Tools[0].InputSchema))
}
