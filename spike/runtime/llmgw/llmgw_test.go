package llmgw_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/spike/runtime/llmgw"
)

func client(t *testing.T, baseURL string) *anthropic.Client {
	t.Helper()
	c := anthropic.NewClient(
		option.WithoutEnvironmentDefaults(),
		option.WithBaseURL(baseURL),
		option.WithAPIKey(llmgw.DummyKey),
		option.WithMaxRetries(0),
	)
	return &c
}

func tool(name string) anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name:        name,
		InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{}},
	}}
}

func params(tools ...string) anthropic.MessageNewParams {
	p := anthropic.MessageNewParams{
		Model:     "spike-model",
		MaxTokens: 256,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("investigate"))},
	}
	for _, name := range tools {
		p.Tools = append(p.Tools, tool(name))
	}
	return p
}

func toolUse(t *testing.T, msg *anthropic.Message) anthropic.ToolUseBlock {
	t.Helper()
	for i := range msg.Content {
		if use, ok := msg.Content[i].AsAny().(anthropic.ToolUseBlock); ok {
			return use
		}
	}
	require.FailNow(t, "no tool_use block in the reply")
	return anthropic.ToolUseBlock{}
}

func TestScriptedRepliesPlayInOrder(t *testing.T) {
	gw := llmgw.New(&llmgw.Options{Script: []llmgw.Turn{
		{ToolUse: &llmgw.ToolUse{Name: "get_alert", Input: json.RawMessage(`{"id":"a1"}`)}},
		{Text: "done", ToolUse: &llmgw.ToolUse{Name: "end_turn"}},
	}})
	srv := httptest.NewServer(gw)
	defer srv.Close()
	c := client(t, srv.URL)

	first, err := c.Messages.New(context.Background(), params("get_alert", "end_turn"))
	require.NoError(t, err)
	assert.Equal(t, anthropic.StopReasonToolUse, first.StopReason)
	use := toolUse(t, first)
	assert.Equal(t, "get_alert", use.Name)
	assert.JSONEq(t, `{"id":"a1"}`, string(use.Input))

	second, err := c.Messages.New(context.Background(), params("get_alert", "end_turn"))
	require.NoError(t, err)
	assert.Equal(t, "end_turn", toolUse(t, second).Name)
}

func TestScriptedToolNamesMatchPrefixedTools(t *testing.T) {
	gw := llmgw.New(&llmgw.Options{Script: []llmgw.Turn{{ToolUse: &llmgw.ToolUse{Name: "end_turn"}}}})
	srv := httptest.NewServer(gw)
	defer srv.Close()

	msg, err := client(t, srv.URL).Messages.New(context.Background(), params("mcp__gw__end_turn"))
	require.NoError(t, err)
	assert.Equal(t, "mcp__gw__end_turn", toolUse(t, msg).Name)
}

func TestRequestsWithoutTheEndToolGetAPlainReply(t *testing.T) {
	gw := llmgw.New(&llmgw.Options{Script: []llmgw.Turn{{ToolUse: &llmgw.ToolUse{Name: "end_turn"}}}})
	srv := httptest.NewServer(gw)
	defer srv.Close()
	c := client(t, srv.URL)

	side, err := c.Messages.New(context.Background(), params())
	require.NoError(t, err)
	assert.Equal(t, anthropic.StopReasonEndTurn, side.StopReason)

	main, err := c.Messages.New(context.Background(), params("end_turn"))
	require.NoError(t, err)
	assert.Equal(t, "end_turn", toolUse(t, main).Name, "a side request must not consume the script")

	records := gw.Records()
	require.Len(t, records, 2)
	assert.False(t, records[0].AgentLoop)
	assert.True(t, records[1].AgentLoop)
}

func TestAnExhaustedScriptEndsTheTurn(t *testing.T) {
	srv := httptest.NewServer(llmgw.New(&llmgw.Options{}))
	defer srv.Close()

	msg, err := client(t, srv.URL).Messages.New(context.Background(), params("end_turn"))
	require.NoError(t, err)
	assert.Equal(t, anthropic.StopReasonEndTurn, msg.StopReason)
}

func TestStreamingRepliesParseWithTheSDK(t *testing.T) {
	gw := llmgw.New(&llmgw.Options{Script: []llmgw.Turn{
		{Text: "checking", ToolUse: &llmgw.ToolUse{Name: "query_logs", Input: json.RawMessage(`{"service":"cart"}`)}},
	}})
	srv := httptest.NewServer(gw)
	defer srv.Close()

	stream := client(t, srv.URL).Messages.NewStreaming(context.Background(), params("query_logs", "end_turn"))
	var msg anthropic.Message
	for stream.Next() {
		require.NoError(t, msg.Accumulate(stream.Current()))
	}
	require.NoError(t, stream.Err())
	assert.Equal(t, anthropic.StopReasonToolUse, msg.StopReason)
	use := toolUse(t, &msg)
	assert.Equal(t, "query_logs", use.Name)
	assert.JSONEq(t, `{"service":"cart"}`, string(use.Input))
}

func TestRecordsClassifyTheKeyHeader(t *testing.T) {
	gw := llmgw.New(&llmgw.Options{})
	srv := httptest.NewServer(gw)
	defer srv.Close()

	for _, key := range []string{llmgw.DummyKey, "sk-real-looking", ""} {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(`{}`))
		require.NoError(t, err)
		if key != "" {
			req.Header.Set("x-api-key", key)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
	}

	records := gw.Records()
	require.Len(t, records, 3)
	assert.Equal(t, "dummy", records[0].KeyHeader)
	assert.Equal(t, "other", records[1].KeyHeader)
	assert.Equal(t, "none", records[2].KeyHeader)
}

func TestUnknownPathsAreRecordedAndRefused(t *testing.T) {
	gw := llmgw.New(&llmgw.Options{})
	srv := httptest.NewServer(gw)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/telemetry")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Len(t, gw.Records(), 1)
	assert.Equal(t, "/v1/telemetry", gw.Records()[0].Path)
}

func TestForwardModeSwapsTheKey(t *testing.T) {
	var seen http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		_, _ = io.WriteString(w, `{"forwarded":true}`)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	gw := llmgw.New(&llmgw.Options{Upstream: target, APIKey: "real-key"})
	srv := httptest.NewServer(gw)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(`{"model":"m"}`))
	require.NoError(t, err)
	req.Header.Set("x-api-key", llmgw.DummyKey)
	req.Header.Set("Authorization", "Bearer "+llmgw.DummyKey)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.JSONEq(t, `{"forwarded":true}`, string(body))
	assert.Equal(t, "real-key", seen.Get("x-api-key"))
	assert.Empty(t, seen.Get("Authorization"))
	require.Len(t, gw.Records(), 1)
	assert.JSONEq(t, `{"model":"m"}`, string(gw.Records()[0].Body))
}

func TestGzipBodiesAreRecordedDecoded(t *testing.T) {
	gw := llmgw.New(&llmgw.Options{})
	srv := httptest.NewServer(gw)
	defer srv.Close()
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	_, err := zw.Write([]byte(`{"model":"m","secret":"CANARY"}`))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", &zipped)
	require.NoError(t, err)
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	require.Len(t, gw.Records(), 1)
	assert.JSONEq(t, `{"model":"m","secret":"CANARY"}`, string(gw.Records()[0].Body))
}

func TestTheWarmUpProbeIsAnswered(t *testing.T) {
	srv := httptest.NewServer(llmgw.New(&llmgw.Options{}))
	defer srv.Close()

	resp, err := http.Head(srv.URL + "/api/hello")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestForwardModeForwardsOnlyTheMessagesAPI(t *testing.T) {
	var forwarded []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = append(forwarded, r.Method+" "+r.URL.Path)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	srv := httptest.NewServer(llmgw.New(&llmgw.Options{Upstream: target, APIKey: "real-key"}))
	defer srv.Close()

	statuses := map[string]int{}
	for _, call := range []struct{ method, path string }{
		{http.MethodPost, "/v1/messages"},
		{http.MethodPost, "/v1/messages/count_tokens"},
		{http.MethodHead, "/api/hello"},
		{http.MethodGet, "/v1/models"},
		{http.MethodPost, "/v1/files"},
		{http.MethodPost, "/v1/messages/batches"},
		{http.MethodGet, "/v1/messages"},
	} {
		req, err := http.NewRequest(call.method, srv.URL+call.path, strings.NewReader(`{}`))
		require.NoError(t, err)
		req.Header.Set("x-api-key", llmgw.DummyKey)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		statuses[call.method+" "+call.path] = resp.StatusCode
	}

	assert.Equal(t, []string{"POST /v1/messages", "POST /v1/messages/count_tokens"}, forwarded,
		"the real key reaches only the Messages API")
	assert.Equal(t, http.StatusOK, statuses["HEAD /api/hello"], "the warm-up probe is answered locally")
	for _, refused := range []string{"GET /v1/models", "POST /v1/files", "POST /v1/messages/batches", "GET /v1/messages"} {
		assert.Equal(t, http.StatusNotFound, statuses[refused], refused)
	}
}
