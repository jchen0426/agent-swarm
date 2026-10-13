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
	"time"

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

func TestRecordsCarryTheTimeTheyArrived(t *testing.T) {
	gw := llmgw.New(&llmgw.Options{})
	srv := httptest.NewServer(gw)
	defer srv.Close()

	before := time.Now()
	_, err := client(t, srv.URL).Messages.New(context.Background(), params("end_turn"))
	require.NoError(t, err)
	after := time.Now()

	require.Len(t, gw.Records(), 1)
	got := gw.Records()[0].Time
	assert.False(t, got.Before(before) || got.After(after), "%s is outside [%s, %s]", got, before, after)
}

func TestForwardModeRefusesWhatTheVendorWouldRunOrFetch(t *testing.T) {
	var forwarded []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		forwarded = append(forwarded, string(body))
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	srv := httptest.NewServer(llmgw.New(&llmgw.Options{Upstream: target, APIKey: "real-key", Models: []string{"allowed-model"}}))
	defer srv.Close()

	refused := map[string]string{
		"a server tool":              `{"model":"allowed-model","tools":[{"type":"web_fetch_20250910","name":"web_fetch"}]}`,
		"an MCP connector":           `{"model":"allowed-model","mcp_servers":[{"type":"url","url":"https://example.com/mcp","name":"x"}]}`,
		"an image by URL":            `{"model":"allowed-model","messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}]}`,
		"a document by file id":      `{"model":"allowed-model","messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"f1"}}]}]}`,
		"a URL inside a tool result": `{"model":"allowed-model","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"url","url":"https://example.com/b.png"}}]}]}]}`,
		"another model":              `{"model":"other-model"}`,
	}
	for name, body := range refused {
		resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, name)
	}
	allowed := `{"model":"allowed-model","tools":[{"name":"get_alert","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}]}`
	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(allowed))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, forwarded, 1, "only the allowed request reaches the vendor")
	assert.JSONEq(t, allowed, forwarded[0])
}

func TestForwardModeRetriesOverloadAndRateLimits(t *testing.T) {
	statuses := []int{529, 429, http.StatusOK}
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		status := statuses[min(attempts, len(statuses)-1)]
		attempts++
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"attempt":true}`)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	srv := httptest.NewServer(llmgw.New(&llmgw.Options{
		Upstream: target, APIKey: "real-key", RetryBackoff: []time.Duration{time.Millisecond, time.Millisecond},
	}))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"m"}`))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 3, attempts)
}

func TestForwardModeReturnsOtherErrorsAtOnce(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	srv := httptest.NewServer(llmgw.New(&llmgw.Options{
		Upstream: target, APIKey: "real-key", RetryBackoff: []time.Duration{time.Millisecond},
	}))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"m"}`))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, 1, attempts)
}

// forwardRig starts a forwarding stand-in whose upstream records request bodies.
func forwardRig(t *testing.T) (srv *httptest.Server, bodies *[]string) {
	t.Helper()
	var received []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received = append(received, r.Header.Get("Content-Encoding")+"|"+string(body))
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	srv = httptest.NewServer(llmgw.New(&llmgw.Options{Upstream: target, APIKey: "real-key", Models: []string{"allowed-model"}}))
	t.Cleanup(srv.Close)
	return srv, &received
}

func post(t *testing.T, srv *httptest.Server, body string) int {
	t.Helper()
	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	return resp.StatusCode
}

// The vendor must receive exactly the request the stand-in checked, so a body
// that two JSON parsers could read differently is either refused or forwarded
// in the one reading that was checked.
func TestForwardModeForwardsExactlyWhatItChecked(t *testing.T) {
	srv, bodies := forwardRig(t)

	safeLast := `{"model":"allowed-model","tools":[{"type":"web_fetch_20250910","name":"web_fetch"}],"tools":[{"name":"get_alert","input_schema":{"type":"object"}}]}`
	safeFirst := `{"model":"allowed-model","tools":[{"name":"get_alert","input_schema":{"type":"object"}}],"tools":[{"type":"web_fetch_20250910","name":"web_fetch"}]}`

	assert.Equal(t, http.StatusOK, post(t, srv, safeLast))
	assert.Equal(t, http.StatusForbidden, post(t, srv, safeFirst))
	require.Len(t, *bodies, 1)
	assert.NotContains(t, (*bodies)[0], "web_fetch", "the duplicate the stand-in discarded must not reach the vendor")
}

func TestForwardModeForwardsADecodedBody(t *testing.T) {
	srv, bodies := forwardRig(t)
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	_, err := zw.Write([]byte(`{"model":"allowed-model"}`))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", &zipped)
	require.NoError(t, err)
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, *bodies, 1)
	assert.Equal(t, `|{"model":"allowed-model"}`, (*bodies)[0])
}

func TestForwardModeRefusesHiddenVendorFeatures(t *testing.T) {
	srv, bodies := forwardRig(t)

	for name, body := range map[string]string{
		"a tool type that is not a string": `{"model":"allowed-model","tools":[{"type":["web_fetch_20250910"],"name":"x"}]}`,
		"a source that is not an object":   `{"model":"allowed-model","messages":[{"role":"user","content":[{"type":"image","source":"https://example.com/a.png"}]}]}`,
		"a URL inside a content source":    `{"model":"allowed-model","messages":[{"role":"user","content":[{"type":"document","source":{"type":"content","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}}]}]}`,
		"a URL in the system prompt":       `{"model":"allowed-model","system":[{"type":"text","text":"x","source":{"type":"url","url":"https://example.com/a"}}]}`,
		"a key in another case":            `{"model":"allowed-model","Tools":[{"type":"web_fetch_20250910","name":"x"}]}`,
		"a code execution container":       `{"model":"allowed-model","container":"c1"}`,
		"tools that are not a list":        `{"model":"allowed-model","tools":{"type":"web_fetch_20250910"}}`,
	} {
		assert.Equal(t, http.StatusForbidden, post(t, srv, body), name)
	}
	assert.Empty(t, *bodies)
}

func TestForwardModeRefusesCheckedKeysInAnotherCaseAtAnyDepth(t *testing.T) {
	srv, bodies := forwardRig(t)

	for name, body := range map[string]string{
		"a tool's type":    `{"model":"allowed-model","tools":[{"name":"x","TYPE":"web_fetch_20250910"}]}`,
		"a block's source": `{"model":"allowed-model","messages":[{"role":"user","content":[{"type":"image","Source":{"type":"url","url":"https://example.com/a.png"}}]}]}`,
		"a source's type":  `{"model":"allowed-model","messages":[{"role":"user","content":[{"type":"image","source":{"Type":"url","url":"https://example.com/a.png"}}]}]}`,
	} {
		assert.Equal(t, http.StatusForbidden, post(t, srv, body), name)
	}
	assert.Empty(t, *bodies)
}

func TestForwardModeDeclaresTheJSONItForwards(t *testing.T) {
	var contentType string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	require.NoError(t, err)
	srv := httptest.NewServer(llmgw.New(&llmgw.Options{Upstream: target, APIKey: "real-key"}))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/x-www-form-urlencoded", strings.NewReader(`{"model":"m"}`))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, "application/json", contentType, "the vendor parses the body the way the stand-in did")
}
