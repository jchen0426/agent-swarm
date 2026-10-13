# Plan 2: Runtime Spike (ADR-006) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

| | |
|---|---|
| Status | Draft for review |
| Date | 2026-10-13 |
| Roadmap | `2026-10-12-implementation-roadmap.md`, Plan 2 |
| Decision | ADR-006: the agent runtime, chosen by spike C |

**Goal:** Choose the agent runtime of ADR-006. Run candidate B, the Claude Agent SDK, and candidate D, a thin Go loop on the Messages API, in a sandbox that reaches only stand-ins for the two gateways. Check both against the seven criteria, measure them on a real model, desk-check candidate E, and write the decision into ADR-006.

**Architecture:** A separate Go module in `spike/runtime/`. Two stand-ins replace the platform's gateways. `llmgw` records every model request, and answers from a script or forwards the request with a key the sandbox never holds. `mcpgw` serves the tools of the current round and records every call. Candidate D is a Go package with a command; candidate B is a short Python program on the SDK. A testcontainers suite runs each candidate on an internal Docker network, with canaries planted in its working and home directories, and checks the criteria from what the stand-ins recorded.

**Tech Stack:** Go 1.26; anthropic-sdk-go v1.80.0; the MCP Go SDK v1.8.0; testify v1.12.0; testcontainers-go v0.44.0; Docker; Python 3.12 with claude-agent-sdk 0.2.165, which bundles Claude Code CLI 2.1.294.

**Spec:** `plan/2026-10-11-agent-swarm-platform-spec.md`, Sections 2 (architecture and invariants), 5.2 (rounds), 5.6 (budgets and stop), 9.3 (sandboxes and gateways) and 14.1 (the spike). ADR-006 holds the seven criteria, ADR-007 the LLM gateway.

**Verified:** Every code block was compiled, tested and linted in a scratch copy, then replayed task by task in an empty tree. Each red step fails for the stated reason, each green step passes, and `golangci-lint run --build-tags 'spike live' ./...` reports 0 issues after every task. Tasks 4 and 5 ran against Docker. Task 6's live test was checked only to skip without a key.

## Results seen while verifying this plan

Verification already ran the scripted part of the spike. Execution reruns it; the results are listed here so the reviewer knows what to expect.

- **Candidate D passed every criterion.** Its requests held only the skill, the prompt and the turn, and it stopped 0.4 s after a 5 s round limit.
- **Candidate B passed criteria 1–6 only in its hardened configuration:** `tools=[]`, `setting_sources=[]`, `strict_mcp_config`, `permission_mode="dontAsk"`, a private `CLAUDE_CONFIG_DIR` and twelve environment switches.
- **With the SDK's defaults**, the control run sent both CLAUDE.md canaries to the model, started the project's and the user's MCP servers, and ran the settings hook.
- **Even hardened, B told the model its working directory**, and it stopped about 6 s after the round limit.
- **B failed criterion 7.** On `tools/list_changed` it refreshed its tools within the turn, and a tool added mid-round appeared in its next two requests. An earlier version of the test passed only because B's next request came before the notification. The test now gives the notification two seconds.
- **Release cadence**, counted on 2026-10-13 over the previous 90 days: 46 releases of claude-agent-sdk, each bundling a new CLI, and 27 of anthropic-sdk-go.

## Global Constraints

- **Location.** All code lives in `spike/runtime/`, in its own Go module `github.com/jchen0426/agent-swarm/spike/runtime` with `go 1.26.0`. Nothing in the main module imports it, and the main module's `go test ./...`, `make` targets and lint never enter it.
- **Throwaway.** The spike's code is evidence for ADR-006, not platform code. Plan 7 builds the production runner with its own tests.
- **Versions.** anthropic-sdk-go v1.80.0; MCP Go SDK v1.8.0; testify v1.12.0; testcontainers-go v0.44.0; claude-agent-sdk 0.2.165; images `golang:1.26-alpine`, `alpine:3.22` and `python:3.12-slim`.
- **No key in the sandbox.** Runtimes get only `sandbox-dummy-key` as their session token. Only the LLM gateway stand-in, outside the sandbox, may hold a real key, and only in Task 6.
- **Model calls.** Tasks 1–5 make none: the LLM stand-in answers from scripts. Task 6 makes at most six short runs on the author's key.
- **Lint.** From `spike/runtime`, `golangci-lint run --build-tags 'spike live' ./...` reports `0 issues.` with the repository's `.golangci.yml`, two directories up.
- **Containers.** Docker must be running. The author's machine disables the Ryuk reaper, so every container and network is removed in `t.Cleanup`.
- **Language.** Code, comments and documents are in English.
- **Commits.** One commit per task, files staged by path, conventional messages, no Co-Authored-By line; push after each task.

## Decision rule

Fixed before execution. Task 7 applies it to `spike/runtime/results/RESULTS.md`.

1. **Out:** a candidate that, in its hardened configuration, fails criterion 1, 2, 3, 4, 5 or 6. Also out, when Task 6's live test runs: a candidate that ends its turn with `end_turn` in fewer than 2 of 3 runs.
2. **Criterion 7 is also a gateway rule.** The MCP gateway (Plan 5) holds each session's tool list until its round ends. A candidate that refreshes within a round stays in, and the gateway rule is recorded as required.
3. **Among the candidates still in,** prefer one that meets the criteria by construction over one that meets them only through configuration, because an upgrade can undo a configuration silently. Then prefer less code for one diagnosis flow, then fewer languages and smaller images.
4. **Provisional:** if the live test cannot run, the decision is recorded as provisional and confirmed at the start of Plan 3.

## Review Focus

The ways the spike could reach a wrong decision through a false pass. Each is pinned by a test in the task that owns the code.

1. **A request body the canary search cannot read**, compressed or not JSON, so the search passes vacuously. (`TestGzipBodiesAreRecordedDecoded` in Task 1; the readable-body check in `TestCriterion5NoHostContent`, Task 4)
2. **Timing that hides a refresh within a round.** A runtime's next request can come before the notification arrives. (The slow first call in `TestCriterion7ToolListsChangeOnlyBetweenRounds`, Task 4)
3. **Canaries that never fire**, which would make every leak test pass. (`TestCanariesCatchTheSDKDefaults`, Task 5)
4. **Host content leaving through tool arguments** rather than prompts. (The tool-argument check in `TestCriterion5NoHostContent`, Task 4)
5. **Egress through DNS or IPv6**, which an IPv4 HTTP probe misses. (`TestOnlyTheGatewaysAreReachable`, Task 4)

## Not in this plan

- **The production runner.** Plan 7 builds it on the runtime this plan chooses. It also handles model errors and retries, which the scripted stand-in never returns.
- **The real gateways.** The MCP gateway is Plan 5 and the LLM gateway Plan 6. The stand-ins only record traffic and answer it.
- **Container hardening beyond the network:** a read-only root filesystem, seccomp, user namespaces and resource limits belong to Plan 10.

## File Structure

| Path | Responsibility |
|---|---|
| `spike/runtime/go.mod` | The spike's own module; the main module never imports it |
| `spike/runtime/llmgw/` | The LLM gateway stand-in: records requests, answers from a script or forwards with its own key |
| `spike/runtime/mcpgw/` | The MCP gateway stand-in: the tools of the current round, canned outputs and a call log |
| `spike/runtime/thinloop/` | Candidate D: one agent's turn as a loop on the Messages API |
| `spike/runtime/cmd/` | The `llmgw`, `mcpgw` and `thinloop` commands that the containers run |
| `spike/runtime/agentsdk/` | Candidate B: one turn on the Claude Agent SDK, hardened or with the SDK's defaults |
| `spike/runtime/sandbox/` | Dockerfiles, canary host content and the scripted scenario |
| `spike/runtime/criteria/` | The sandbox suite: one test per criterion, the canary control and the live test |
| `spike/runtime/tools/cadence.py` | Counts each runtime library's releases in the last 90 days |
| `spike/runtime/results/RESULTS.md` | The measured results that ADR-006 cites |
| `spike/runtime/README.md` | How to rerun the suite when a runtime's version changes |

## Tasks

| Task | Delivers |
|---|---|
| 1 | The spike module and the LLM gateway stand-in |
| 2 | The MCP gateway stand-in |
| 3 | Candidate D, a thin loop |
| 4 | The sandbox and the criteria suite |
| 5 | Candidate B, the Claude Agent SDK |
| 6 | Measurements: live runs, code size, release cadence and candidate E |
| 7 | The decision in ADR-006 |

---

### Task 1: The spike module and the LLM gateway stand-in

**Files:**
- Create: `spike/runtime/go.mod`, `spike/runtime/go.sum`
- Create: `spike/runtime/llmgw/llmgw.go`
- Test: `spike/runtime/llmgw/llmgw_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces, in package `llmgw`:
  - `const DummyKey = "sandbox-dummy-key"`;
  - `type Turn struct { Text string; ToolUse *ToolUse }` and `type ToolUse struct { Name string; Input json.RawMessage }`;
  - `type Record struct { Seq int; Method, Path, KeyHeader string; AgentLoop bool; Body json.RawMessage }`, where `KeyHeader` is `dummy`, `other` or `none`;
  - `type Options struct { Script []Turn; EndTool string; Upstream *url.URL; APIKey string; Log io.Writer }`;
  - `func New(opts *Options) *Gateway`, `(*Gateway).Records() []Record` and `(*Gateway).ServeHTTP`.

- [ ] **Step 1: Create the module**

From the repository root:

```bash
mkdir -p spike/runtime && cd spike/runtime
go mod init github.com/jchen0426/agent-swarm/spike/runtime
go mod edit -go=1.26.0
go get github.com/anthropics/anthropic-sdk-go@v1.80.0 github.com/stretchr/testify/assert@v1.12.0 github.com/stretchr/testify/require@v1.12.0
```

Expected: `go.mod` requires anthropic-sdk-go v1.80.0 and testify v1.12.0. Getting testify's `assert` and `require` packages, not just the module, records the go.sum entries of their own dependencies. Without them the next step fails on a missing go.sum entry instead of on the missing code. Run every later command in `spike/runtime`, unless a step says otherwise.

- [ ] **Step 2: Write the failing tests**

The streaming test parses the stand-in's server-sent events with the real Go SDK, so the stand-in speaks the Messages API as runtimes expect. Create `llmgw/llmgw_test.go`:

```go
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
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./llmgw`
Expected: FAIL with `no non-test Go files in .../spike/runtime/llmgw`.

- [ ] **Step 4: Write the stand-in**

Requests that offer the end tool are the agent loop and consume the script. Others, such as a runtime's background requests, get a plain reply, so they cannot shift the script. Gzip bodies are decoded before they are recorded, so the canary search of Task 4 can read them. Create `llmgw/llmgw.go`:

```go
// Package llmgw stands in for the platform's LLM gateway (ADR-007) in the
// runtime spike. It records every request a runtime sends, and answers either
// from a script or by forwarding to the real API with a key that the sandbox
// never holds.
package llmgw

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
)

// DummyKey is the only credential a sandbox holds. In forward mode the
// gateway replaces it with its own key.
const DummyKey = "sandbox-dummy-key"

// Turn is one scripted assistant reply: optional text, then an optional tool call.
type Turn struct {
	Text    string   `json:"text,omitempty"`
	ToolUse *ToolUse `json:"tool_use,omitempty"`
}

// ToolUse names a tool by its bare name. The gateway matches it against the
// request's tools, so "end_turn" also matches "mcp__gw__end_turn".
type ToolUse struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// Record is one request the gateway received.
type Record struct {
	Seq       int             `json:"seq"`
	Method    string          `json:"method"`
	Path      string          `json:"path"`
	KeyHeader string          `json:"key_header"` // "dummy", "other" or "none"
	AgentLoop bool            `json:"agent_loop"` // the request offered the end tool
	Body      json.RawMessage `json:"body,omitempty"`
}

// Options configure a Gateway.
type Options struct {
	// Script holds the replies to agent-loop requests, in order. Requests that
	// do not offer EndTool get a plain text reply and do not consume it.
	Script []Turn
	// EndTool marks agent-loop requests. The default is "end_turn".
	EndTool string
	// Upstream selects forward mode: requests go to the real API with APIKey.
	Upstream *url.URL
	APIKey   string `json:"-"`
	// Log, when set, receives every record as one JSON line.
	Log io.Writer
}

// Gateway is an http.Handler.
type Gateway struct {
	opts    Options
	proxy   *httputil.ReverseProxy
	mu      sync.Mutex
	records []Record
	next    int
}

// New returns a gateway in scripted mode, or in forward mode when
// opts.Upstream is set.
func New(opts *Options) *Gateway {
	g := &Gateway{opts: *opts}
	if g.opts.EndTool == "" {
		g.opts.EndTool = "end_turn"
	}
	if g.opts.Upstream != nil {
		g.proxy = &httputil.ReverseProxy{
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(g.opts.Upstream)
				r.Out.Header.Del("Authorization")
				r.Out.Header.Set("x-api-key", g.opts.APIKey)
			},
			FlushInterval: -1,
		}
	}
	return g
}

// Records returns a copy of the requests received so far.
func (g *Gateway) Records() []Record {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Record(nil), g.records...)
}

type request struct {
	Model  string `json:"model"`
	Stream bool   `json:"stream"`
	Tools  []struct {
		Name string `json:"name"`
	} `json:"tools"`
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		_, _ = io.WriteString(w, "ok")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body, err := decode(r.Header, raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req request
	_ = json.Unmarshal(body, &req)
	tools := make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		tools = append(tools, t.Name)
	}
	agentLoop := resolve(g.opts.EndTool, tools) != ""
	seq := g.record(r, body, agentLoop)

	if g.proxy != nil {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		g.proxy.ServeHTTP(w, r) //nolint:gosec // G704: the proxy only ever reaches the configured upstream
		return
	}
	switch r.URL.Path {
	case "/v1/messages":
		blocks, stop := g.reply(seq, agentLoop, tools)
		if req.Stream {
			writeStream(w, seq, req.Model, len(body)/4, blocks, stop)
			return
		}
		writeJSON(w, map[string]any{
			"id": fmt.Sprintf("msg_spike_%d", seq), "type": "message", "role": "assistant",
			"model": req.Model, "content": blocks, "stop_reason": stop, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": len(body) / 4, "output_tokens": 10},
		})
	case "/v1/messages/count_tokens":
		writeJSON(w, map[string]int{"input_tokens": len(body) / 4})
	case "/api/hello":
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func (g *Gateway) record(r *http.Request, body []byte, agentLoop bool) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec := Record{
		Seq: len(g.records) + 1, Method: r.Method, Path: r.URL.Path,
		KeyHeader: keyKind(r.Header), AgentLoop: agentLoop,
	}
	if json.Valid(body) {
		rec.Body = body
	}
	g.records = append(g.records, rec)
	if g.opts.Log != nil {
		line, err := json.Marshal(rec)
		if err == nil {
			_, _ = g.opts.Log.Write(append(line, '\n'))
		}
	}
	return rec.Seq
}

// decode undoes a gzip Content-Encoding, so that compressed bodies are
// recorded and searched like any other.
func decode(h http.Header, raw []byte) ([]byte, error) {
	if !strings.EqualFold(h.Get("Content-Encoding"), "gzip") {
		return raw, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("gzip body: %w", err)
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(io.LimitReader(zr, 64<<20))
}

func keyKind(h http.Header) string {
	key := h.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(h.Get("Authorization"), "Bearer ")
	}
	switch key {
	case "":
		return "none"
	case DummyKey:
		return "dummy"
	default:
		return "other"
	}
}

// resolve returns the request's tool that a scripted bare name refers to, or "".
func resolve(name string, tools []string) string {
	for _, t := range tools {
		if t == name || strings.HasSuffix(t, "__"+name) {
			return t
		}
	}
	return ""
}

type block = map[string]any

func (g *Gateway) reply(seq int, agentLoop bool, tools []string) (blocks []block, stop string) {
	if !agentLoop {
		return []block{{"type": "text", "text": "ok"}}, "end_turn"
	}
	g.mu.Lock()
	if g.next >= len(g.opts.Script) {
		g.mu.Unlock()
		return []block{{"type": "text", "text": "script exhausted"}}, "end_turn"
	}
	turn := g.opts.Script[g.next]
	g.next++
	g.mu.Unlock()

	if turn.Text != "" {
		blocks = append(blocks, block{"type": "text", "text": turn.Text})
	}
	if turn.ToolUse == nil {
		return blocks, "end_turn"
	}
	name := resolve(turn.ToolUse.Name, tools)
	if name == "" {
		name = turn.ToolUse.Name
	}
	input := turn.ToolUse.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	blocks = append(blocks, block{
		"type": "tool_use", "id": fmt.Sprintf("toolu_spike_%d", seq), "name": name, "input": input,
	})
	return blocks, "tool_use"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// writeStream sends the reply as Messages API server-sent events.
func writeStream(w http.ResponseWriter, seq int, model string, inputTokens int, blocks []block, stop string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	send := func(event string, data any) {
		payload, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
		if flusher != nil {
			flusher.Flush()
		}
	}
	send("message_start", block{"type": "message_start", "message": block{
		"id": fmt.Sprintf("msg_spike_%d", seq), "type": "message", "role": "assistant", "model": model,
		"content": []block{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": inputTokens, "output_tokens": 1},
	}})
	for i, b := range blocks {
		switch b["type"] {
		case "text":
			send("content_block_start", block{"type": "content_block_start", "index": i, "content_block": block{"type": "text", "text": ""}})
			send("content_block_delta", block{"type": "content_block_delta", "index": i, "delta": block{"type": "text_delta", "text": b["text"]}})
		case "tool_use":
			send("content_block_start", block{"type": "content_block_start", "index": i, "content_block": block{
				"type": "tool_use", "id": b["id"], "name": b["name"], "input": block{},
			}})
			send("content_block_delta", block{"type": "content_block_delta", "index": i, "delta": block{
				"type": "input_json_delta", "partial_json": string(b["input"].(json.RawMessage)),
			}})
		}
		send("content_block_stop", block{"type": "content_block_stop", "index": i})
	}
	send("message_delta", block{"type": "message_delta", "delta": block{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 10}})
	send("message_stop", block{"type": "message_stop"})
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./llmgw`
Expected: `ok  	github.com/jchen0426/agent-swarm/spike/runtime/llmgw`, with ten tests.

- [ ] **Step 6: Lint**

Run: `golangci-lint run --build-tags 'spike live' ./...`
Expected: `0 issues.`

- [ ] **Step 7: Commit**

From the repository root:

```bash
git add spike/runtime/go.mod spike/runtime/go.sum spike/runtime/llmgw/llmgw.go spike/runtime/llmgw/llmgw_test.go
git commit -m "feat(spike): add the LLM gateway stand-in for the runtime spike"
git push
```

---

### Task 2: The MCP gateway stand-in

**Files:**
- Create: `spike/runtime/mcpgw/mcpgw.go`
- Test: `spike/runtime/mcpgw/mcpgw_test.go`
- Modify: `spike/runtime/go.mod`, `spike/runtime/go.sum`

**Interfaces:**
- Consumes: nothing.
- Produces, in package `mcpgw`:
  - `type Call struct { Round int; Tool string; Args json.RawMessage }`;
  - `type Options struct { Outputs map[string]string; Delays map[string]time.Duration; AddAfterFirstCall string; Log io.Writer }`;
  - `func New(opts Options) *Gateway`, `(*Gateway).SetRound(n int, tools ...string)`, `(*Gateway).Calls() []Call`;
  - `(*Gateway).ServeHTTP`, serving MCP at `/mcp`, `POST /admin/round?n=N&tools=a,b`, `GET /admin/calls` and `/healthz`.

- [ ] **Step 1: Add the MCP SDK**

Run: `go get github.com/modelcontextprotocol/go-sdk/mcp@v1.8.0`
Expected: `go.mod` requires `github.com/modelcontextprotocol/go-sdk v1.8.0`. Task 1's `go mod tidy` removes modules that no code uses yet, so the SDK is added here. Without it, the red step fails on a missing go.sum entry instead of on the missing code.

- [ ] **Step 2: Write the failing tests**

Each test server closes through `t.Cleanup`, which runs after the client sessions close. A deferred `Close` would wait forever on the session's open event stream. Create `mcpgw/mcpgw_test.go`:

```go
package mcpgw_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/spike/runtime/mcpgw"
)

func connect(t *testing.T, url string, onChange func()) *mcp.ClientSession {
	t.Helper()
	opts := &mcp.ClientOptions{}
	if onChange != nil {
		opts.ToolListChangedHandler = func(context.Context, *mcp.ToolListChangedRequest) { onChange() }
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, opts)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: url + "/mcp"}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func names(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var out []string
	for _, tool := range res.Tools {
		out = append(out, tool.Name)
	}
	slices.Sort(out)
	return out
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	text, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	return text.Text
}

func TestToolsFollowTheRound(t *testing.T) {
	gw := mcpgw.New(mcpgw.Options{})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)

	gw.SetRound(1, "get_alert", "end_turn")
	assert.Equal(t, []string{"end_turn", "get_alert"}, names(t, connect(t, srv.URL, nil)))

	gw.SetRound(2, "query_logs", "end_turn")
	assert.Equal(t, []string{"end_turn", "query_logs"}, names(t, connect(t, srv.URL, nil)))
}

func TestCallsAreRecordedWithTheirRound(t *testing.T) {
	gw := mcpgw.New(mcpgw.Options{Outputs: map[string]string{"get_alert": "checkout error rate 12%"}})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	gw.SetRound(3, "get_alert")

	out := call(t, connect(t, srv.URL, nil), "get_alert", map[string]any{"id": "a1"})

	assert.Equal(t, "checkout error rate 12%", out)
	calls := gw.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, 3, calls[0].Round)
	assert.Equal(t, "get_alert", calls[0].Tool)
	assert.JSONEq(t, `{"id":"a1"}`, string(calls[0].Args))
}

func TestBoardToolsAnswerRecorded(t *testing.T) {
	gw := mcpgw.New(mcpgw.Options{})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	gw.SetRound(1, "post_hypothesis", "end_turn")
	cs := connect(t, srv.URL, nil)

	assert.Equal(t, "recorded", call(t, cs, "post_hypothesis", map[string]any{"key": "bad-release/cart"}))
	assert.Equal(t, "recorded", call(t, cs, "end_turn", nil))
}

func TestAToolAppearsAfterTheFirstCallOfRoundOne(t *testing.T) {
	gw := mcpgw.New(mcpgw.Options{AddAfterFirstCall: "probe_extra"})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	gw.SetRound(1, "get_alert", "end_turn")
	changed := make(chan struct{}, 1)
	cs := connect(t, srv.URL, func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	assert.NotContains(t, names(t, cs), "probe_extra")

	call(t, cs, "get_alert", nil)

	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "no tools/list_changed notification")
	}
	assert.Contains(t, names(t, cs), "probe_extra")
}

func TestDelaysSlowATool(t *testing.T) {
	gw := mcpgw.New(mcpgw.Options{Delays: map[string]time.Duration{"slow_read": 300 * time.Millisecond}})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)
	gw.SetRound(1, "slow_read")
	cs := connect(t, srv.URL, nil)

	start := time.Now()
	call(t, cs, "slow_read", nil)
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond)
}

func TestTheAdminEndpointSetsTheRound(t *testing.T) {
	gw := mcpgw.New(mcpgw.Options{})
	srv := httptest.NewServer(gw)
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/admin/round?n=2&tools=query_logs,end_turn", "text/plain", nil)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	assert.Equal(t, []string{"end_turn", "query_logs"}, names(t, connect(t, srv.URL, nil)))
}
```

- [ ] **Step 3: Run them to see them fail**

Run: `go test ./mcpgw`
Expected: FAIL with `no non-test Go files in .../spike/runtime/mcpgw`.

- [ ] **Step 4: Write the stand-in**

`AddAfterFirstCall` adds a tool while a round is running. The SDK then sends `tools/list_changed` to every session, which is the event criterion 7 is about. Create `mcpgw/mcpgw.go`:

```go
// Package mcpgw stands in for the platform's MCP gateway (spec Section 6) in
// the runtime spike. It serves canned read tools and the board tools of the
// current round, and records every call.
package mcpgw

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Call is one tool call the gateway received.
type Call struct {
	Round int             `json:"round"`
	Tool  string          `json:"tool"`
	Args  json.RawMessage `json:"args,omitempty"`
}

// Options configure a Gateway.
type Options struct {
	// Outputs maps read tools to their canned output. Board tools answer "recorded".
	Outputs map[string]string
	// Delays makes tools slow, to exercise round time limits.
	Delays map[string]time.Duration
	// AddAfterFirstCall names a tool added right after the first call of round 1,
	// to check that runtimes keep their tool list until the round ends.
	AddAfterFirstCall string
	// Log, when set, receives every call as one JSON line.
	Log io.Writer
}

var boardTools = map[string]bool{"post_evidence": true, "post_hypothesis": true, "end_turn": true}

var descriptions = map[string]string{
	"get_alert":          "Return the alert that opened this item.",
	"get_recent_changes": "List release and configuration changes on a service before a time.",
	"query_logs":         "Search a service's logs in a time range.",
	"slow_read":          "A read that takes a long time.",
	"probe_extra":        "A tool that appears in the middle of a round.",
	"post_evidence":      "Post evidence to the board, with a verbatim quote of a read's output.",
	"post_hypothesis":    "Post a hypothesis to the board: a key and one line of rationale.",
	"end_turn":           "End your turn for this round.",
}

// Gateway is an http.Handler. It serves MCP at /mcp, sets the round with
// POST /admin/round?n=N&tools=a,b, and lists calls at /admin/calls.
type Gateway struct {
	opts   Options
	server *mcp.Server
	mcp    http.Handler
	mu     sync.Mutex
	round  int
	tools  []string
	added  bool
	calls  []Call
}

// New returns a gateway with no tools; SetRound gives it some.
func New(opts Options) *Gateway {
	g := &Gateway{opts: opts}
	g.server = mcp.NewServer(&mcp.Implementation{Name: "spike-mcp-gateway", Version: "0"}, nil)
	g.mcp = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return g.server }, nil)
	return g
}

// SetRound starts round n with exactly the given tools.
func (g *Gateway) SetRound(n int, tools ...string) {
	g.mu.Lock()
	old := g.tools
	g.round, g.tools, g.added = n, tools, false
	g.mu.Unlock()
	g.server.RemoveTools(old...)
	for _, name := range tools {
		g.add(name)
	}
}

// Calls returns a copy of the calls received so far.
func (g *Gateway) Calls() []Call {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Call(nil), g.calls...)
}

func (g *Gateway) add(name string) {
	desc := descriptions[name]
	if desc == "" {
		desc = "Spike tool."
	}
	g.server.AddTool(&mcp.Tool{
		Name:        name,
		Description: desc,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return g.call(ctx, name, req.Params.Arguments)
	})
}

func (g *Gateway) call(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	g.mu.Lock()
	c := Call{Round: g.round, Tool: name, Args: args}
	g.calls = append(g.calls, c)
	addNow := g.round == 1 && !g.added && g.opts.AddAfterFirstCall != ""
	if addNow {
		g.added = true
	}
	g.mu.Unlock()
	if g.opts.Log != nil {
		if line, err := json.Marshal(c); err == nil {
			_, _ = g.opts.Log.Write(append(line, '\n'))
		}
	}
	if addNow {
		g.add(g.opts.AddAfterFirstCall)
	}
	if d := g.opts.Delays[name]; d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out := "recorded"
	if !boardTools[name] {
		out = g.opts.Outputs[name]
		if out == "" {
			out = "no data"
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}}, nil
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		_, _ = io.WriteString(w, "ok")
	case "/admin/round":
		n, err := strconv.Atoi(r.URL.Query().Get("n"))
		if err != nil || r.Method != http.MethodPost {
			http.Error(w, "POST /admin/round?n=N&tools=a,b", http.StatusBadRequest)
			return
		}
		g.SetRound(n, strings.Split(r.URL.Query().Get("tools"), ",")...)
		w.WriteHeader(http.StatusNoContent)
	case "/admin/calls":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(g.Calls())
	default:
		g.mcp.ServeHTTP(w, r)
	}
}
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./mcpgw`
Expected: `ok  	github.com/jchen0426/agent-swarm/spike/runtime/mcpgw`, with six tests. `TestAToolAppearsAfterTheFirstCallOfRoundOne` shows a client receiving `tools/list_changed`.

- [ ] **Step 6: Lint**

Run: `golangci-lint run --build-tags 'spike live' ./...`
Expected: `0 issues.`

- [ ] **Step 7: Commit**

From the repository root:

```bash
git add spike/runtime/go.mod spike/runtime/go.sum spike/runtime/mcpgw/mcpgw.go spike/runtime/mcpgw/mcpgw_test.go
git commit -m "feat(spike): add the MCP gateway stand-in for the runtime spike"
git push
```

---

### Task 3: Candidate D, a thin loop

**Files:**
- Create: `spike/runtime/thinloop/thinloop.go`
- Create: `spike/runtime/cmd/thinloop/main.go`
- Test: `spike/runtime/thinloop/thinloop_test.go`

**Interfaces:**
- Consumes: `llmgw.New`, `llmgw.Options`, `llmgw.Turn`, `llmgw.ToolUse`, `llmgw.Record` and `llmgw.DummyKey` (Task 1); `mcpgw.New`, `mcpgw.Options`, `(*mcpgw.Gateway).SetRound` and `Calls` (Task 2). Tests only.
- Produces, in package `thinloop`:
  - `type Config struct { Model string; MaxTokens int64; MaxSteps int; System, EndTool string }`;
  - `type Outcome string`, with `EndedByTool` (`end_turn`), `EndedByModel` (`no_tool`), `EndedBySteps` (`max_steps`) and `EndedByDeadline` (`deadline`);
  - `type Result struct { Outcome Outcome; Steps int; Tools []string }`;
  - `func Run(ctx context.Context, model *anthropic.Client, gateway *mcp.ClientSession, cfg Config, prompt string) (Result, error)`.
- Produces the `thinloop` command. Its flags are `-llm-url`, `-mcp-url`, `-model`, `-system-file`, `-prompt-file`, `-round` and `-max-steps`. It reads its session token from `AGENT_SESSION_TOKEN` and prints the `Result` as JSON.

- [ ] **Step 1: Write the failing tests**

Create `thinloop/thinloop_test.go`:

```go
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
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./thinloop`
Expected: FAIL with `no non-test Go files in .../spike/runtime/thinloop`.

- [ ] **Step 3: Write the loop**

The loop lists the gateway's tools once, when the turn starts, which is how it meets criterion 7. Its only context is the turn's own prompt and exchanges, which is how it meets criterion 6. Create `thinloop/thinloop.go`:

```go
// Package thinloop is candidate D of ADR-006: one agent's turn in one round,
// as a plain loop on the Messages API. The tool list is read once, when the
// turn starts, and the context is the turn's prompt plus its own exchanges, so
// nothing carries over between rounds.
package thinloop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Config describes one agent's turn.
type Config struct {
	Model     string
	MaxTokens int64
	MaxSteps  int    // model calls per turn
	System    string // the agent's instructions: its skill
	EndTool   string // the board tool that ends the turn; default "end_turn"
}

// Outcome says why a turn ended.
type Outcome string

// The ways a turn ends.
const (
	EndedByTool     Outcome = "end_turn"  // the agent called the end tool
	EndedByModel    Outcome = "no_tool"   // the model answered without calling a tool
	EndedBySteps    Outcome = "max_steps" // MaxSteps model calls were made
	EndedByDeadline Outcome = "deadline"  // the round's time limit passed
)

// Result reports one turn.
type Result struct {
	Outcome Outcome  `json:"outcome"`
	Steps   int      `json:"steps"`
	Tools   []string `json:"tools"` // the tool list the turn used
}

// Run plays one turn. It lists the gateway's tools once, then alternates model
// calls and tool calls until the agent calls the end tool, the model stops
// calling tools, MaxSteps model calls are made or ctx ends.
func Run(ctx context.Context, model *anthropic.Client, gateway *mcp.ClientSession, cfg Config, prompt string) (Result, error) {
	if cfg.EndTool == "" {
		cfg.EndTool = "end_turn"
	}
	listed, err := gateway.ListTools(ctx, nil)
	if err != nil {
		return Result{}, fmt.Errorf("list tools: %w", err)
	}
	var res Result
	tools := make([]anthropic.ToolUnionParam, 0, len(listed.Tools))
	for _, t := range listed.Tools {
		p, err := toolParam(t)
		if err != nil {
			return Result{}, err
		}
		tools = append(tools, p)
		res.Tools = append(res.Tools, t.Name)
	}

	messages := []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))}
	for res.Steps < cfg.MaxSteps {
		res.Steps++
		msg, err := model.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     cfg.Model,
			MaxTokens: cfg.MaxTokens,
			System:    []anthropic.TextBlockParam{{Text: cfg.System}},
			Messages:  messages,
			Tools:     tools,
		})
		if err != nil {
			if ctx.Err() != nil {
				res.Outcome = EndedByDeadline
				return res, nil
			}
			return res, fmt.Errorf("model call: %w", err)
		}
		messages = append(messages, msg.ToParam())

		var results []anthropic.ContentBlockParamUnion
		ended := false
		for i := range msg.Content {
			use, ok := msg.Content[i].AsAny().(anthropic.ToolUseBlock)
			if !ok {
				continue
			}
			out, isErr := callTool(ctx, gateway, &use)
			if ctx.Err() != nil {
				res.Outcome = EndedByDeadline
				return res, nil
			}
			results = append(results, anthropic.NewToolResultBlock(use.ID, out, isErr))
			ended = ended || use.Name == cfg.EndTool
		}
		switch {
		case ended:
			res.Outcome = EndedByTool
			return res, nil
		case len(results) == 0:
			res.Outcome = EndedByModel
			return res, nil
		}
		messages = append(messages, anthropic.NewUserMessage(results...))
	}
	res.Outcome = EndedBySteps
	return res, nil
}

// toolParam turns an MCP tool into a Messages API tool definition.
func toolParam(t *mcp.Tool) (anthropic.ToolUnionParam, error) {
	raw, err := json.Marshal(t.InputSchema)
	if err != nil {
		return anthropic.ToolUnionParam{}, fmt.Errorf("tool %s: %w", t.Name, err)
	}
	var schema anthropic.ToolInputSchemaParam
	if err := json.Unmarshal(raw, &schema); err != nil {
		return anthropic.ToolUnionParam{}, fmt.Errorf("tool %s: %w", t.Name, err)
	}
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name: t.Name, Description: anthropic.String(t.Description), InputSchema: schema,
	}}, nil
}

// callTool calls a tool through the gateway and returns its text output.
func callTool(ctx context.Context, gateway *mcp.ClientSession, use *anthropic.ToolUseBlock) (string, bool) {
	res, err := gateway.CallTool(ctx, &mcp.CallToolParams{Name: use.Name, Arguments: use.Input})
	if err != nil {
		return err.Error(), true
	}
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String(), res.IsError
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `go mod tidy && go test -count=1 ./thinloop`
Expected: `ok  	github.com/jchen0426/agent-swarm/spike/runtime/thinloop`, with eight tests.

- [ ] **Step 5: Write the command**

`option.WithoutEnvironmentDefaults()` stops the Go SDK from reading any credential from the environment. The only credential is the session token that the sandbox passes in. Create `cmd/thinloop/main.go`:

```go
// Command thinloop plays one agent's turn in one round with candidate D, and
// prints the result as JSON. The sandbox gives it only a session token, never
// a model key.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jchen0426/agent-swarm/spike/runtime/thinloop"
)

func main() {
	llmURL := flag.String("llm-url", "http://llmgw:8080", "the LLM gateway")
	mcpURL := flag.String("mcp-url", "http://mcpgw:8090/mcp", "the MCP gateway")
	model := flag.String("model", "claude-sonnet-5-5", "model id")
	systemFile := flag.String("system-file", "/in/system.md", "the agent's skill")
	promptFile := flag.String("prompt-file", "/in/prompt.md", "this round's prompt: the board snapshot")
	round := flag.Duration("round", 2*time.Minute, "the round's time limit")
	maxSteps := flag.Int("max-steps", 30, "model calls per turn")
	flag.Parse()

	cfg := thinloop.Config{Model: *model, MaxTokens: 4096, MaxSteps: *maxSteps}
	if err := run(*llmURL, *mcpURL, *systemFile, *promptFile, *round, cfg); err != nil {
		log.Fatal(err)
	}
}

func run(llmURL, mcpURL, systemFile, promptFile string, round time.Duration, cfg thinloop.Config) error {
	system, err := os.ReadFile(filepath.Clean(systemFile))
	if err != nil {
		return err
	}
	prompt, err := os.ReadFile(filepath.Clean(promptFile))
	if err != nil {
		return err
	}
	cfg.System = string(system)
	ctx, cancel := context.WithTimeout(context.Background(), round)
	defer cancel()

	client := anthropic.NewClient(
		option.WithoutEnvironmentDefaults(),
		option.WithBaseURL(llmURL),
		option.WithAPIKey(os.Getenv("AGENT_SESSION_TOKEN")),
		option.WithMaxRetries(0),
	)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "thinloop", Version: "spike"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: mcpURL, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	res, err := thinloop.Run(ctx, &client, session, cfg, string(prompt))
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(res)
}
```

- [ ] **Step 6: Build and lint**

Run: `go build -o /dev/null ./cmd/thinloop && golangci-lint run --build-tags 'spike live' ./...`
Expected: no build output, then `0 issues.`

- [ ] **Step 7: Commit**

From the repository root:

```bash
git add spike/runtime/go.mod spike/runtime/go.sum spike/runtime/thinloop/thinloop.go spike/runtime/thinloop/thinloop_test.go spike/runtime/cmd/thinloop/main.go
git commit -m "feat(spike): add candidate D, a thin loop on the Messages API"
git push
```

---

### Task 4: The sandbox and the criteria suite

**Files:**
- Create: `spike/runtime/cmd/llmgw/main.go`, `spike/runtime/cmd/mcpgw/main.go`
- Create: `spike/runtime/sandbox/go.Dockerfile`, `spike/runtime/.dockerignore`
- Create: `spike/runtime/sandbox/canary/work/CLAUDE.md`, `.../work/.claude/settings.json`, `.../work/.mcp.json`, `.../work/notes/CANARY-FILE-NAME.txt`, `.../home/.claude/CLAUDE.md` and `.../home/.claude.json`
- Create: `spike/runtime/sandbox/scenario/system.md`, `prompt-round1.md`, `prompt-round2.md` and `outputs.json`
- Test: `spike/runtime/criteria/criteria_test.go`

**Interfaces:**
- Consumes: `llmgw.Record`, `llmgw.Turn`, `llmgw.ToolUse` and `llmgw.DummyKey` (Task 1); `mcpgw.Call` (Task 2); the `thinloop` command (Task 3).
- Produces, in the `criteria_test` package, which Tasks 5 and 6 extend:
  - `images map[string]string`, from image tag to Dockerfile, and `candidates []candidate`;
  - `type candidate struct { name, image, prefix string; cmd func(prompt string, limit time.Duration) []string; env map[string]string }`;
  - `newSandbox`, `emptySandbox`, `(*sandbox).gateway`, `startMCP`, `runRound`, `runContainer`, `records` and `calls`;
  - the helpers `forEach`, `use`, `agentLoop`, `toolsOf`, `named`, `toolNames` and `markers`, and `contentCanaries`, `metadataCanaries` and `workdir`.

- [ ] **Step 1: Add testcontainers**

Run: `go get github.com/testcontainers/testcontainers-go@v0.44.0`
Expected: `go.mod` requires testcontainers-go v0.44.0.

- [ ] **Step 2: Write the suite**

One test per criterion, each run for every registered candidate. The sandbox is an internal Docker network. The stand-ins sit on it under the names `llmgw` and `mcpgw`, and the runtime container runs with the canary working and home directories mounted. Two points in the tests deserve a reviewer's attention:
- Criterion 7's round begins with a slow call, so that a runtime which listens for `tools/list_changed` has two seconds to react before its next request.
- Criterion 5 separates content canaries, which must never reach a model, from metadata canaries, the working directory and host name, which are logged as findings.

Create `criteria/criteria_test.go`:

```go
//go:build spike

// Package criteria runs each candidate runtime of ADR-006 in a sandbox that
// can reach only the two gateway stand-ins, and checks it against the seven
// criteria. Run it from the module root with:
//
//	go test -tags spike -count=1 -v ./criteria
package criteria_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/jchen0426/agent-swarm/spike/runtime/llmgw"
	"github.com/jchen0426/agent-swarm/spike/runtime/mcpgw"
)

const goImage = "agentswarm-spike-go:latest"

// images maps each image the suite builds to its Dockerfile.
var images = map[string]string{goImage: "sandbox/go.Dockerfile"}

// workdir is where a runtime starts. Its name is a canary too, so that a
// runtime that tells the model its working directory is caught.
const workdir = "/canary-workdir"

func TestMain(m *testing.M) {
	for tag, dockerfile := range images {
		if err := build(tag, dockerfile); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func build(tag, dockerfile string) error {
	cmd := exec.Command("docker", "build", "-q", "-t", tag, "-f", dockerfile, ".")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker build %s: %w\n%s", tag, err, out)
	}
	return nil
}

// candidate is one runtime under test.
type candidate struct {
	name   string
	image  string
	prefix string // how the runtime names gateway tools in its model requests
	cmd    func(prompt string, limit time.Duration) []string
	env    map[string]string
}

var candidates = []candidate{
	{
		name:  "thinloop",
		image: goImage,
		cmd: func(prompt string, limit time.Duration) []string {
			return []string{"thinloop", "-system-file", "/in/system.md", "-prompt-file", "/in/" + prompt, "-round", limit.String()}
		},
	},
}

// contentCanaries are planted in host and workspace files, settings, git and
// the environment. None of them may reach a model.
var contentCanaries = []string{
	"CANARY-PROJECT-MEMORY", "CANARY-USER-MEMORY", "CANARY-SETTINGS-VALUE", "CANARY-GIT-COMMIT",
	"CANARY-FILE-NAME", "CANARY-FILE-CONTENT", "CANARY-ENV-VALUE",
}

// metadataCanaries name the sandbox's working directory and host. A runtime
// that reports them tells the model about its environment; that is recorded
// as a finding, not a failure.
var metadataCanaries = []string{workdir, "canary-host"}

func forEach(t *testing.T, test func(t *testing.T, c candidate)) {
	t.Helper()
	for _, c := range candidates {
		t.Run(c.name, func(t *testing.T) { test(t, c) })
	}
}

func use(name string) llmgw.Turn { return llmgw.Turn{ToolUse: &llmgw.ToolUse{Name: name}} }

// sandbox is one internal network holding the two gateway stand-ins.
type sandbox struct {
	t        *testing.T
	ctx      context.Context
	net      *testcontainers.DockerNetwork
	out      string // gateway logs
	marks    string // files that canary hooks and servers create when they run
	work     string // the canary working directory
	home     string // the canary home directory
	scenario string
	mcp      *testcontainers.DockerContainer
}

func newSandbox(t *testing.T, script []llmgw.Turn, mcpArgs ...string) *sandbox {
	t.Helper()
	s := emptySandbox(t)
	raw, err := json.Marshal(script)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(s.out, "script.json"), raw, 0o600))
	s.gateway("llmgw", []string{"llmgw", "-script", "/out/script.json", "-log", "/out/llm.jsonl"})
	s.startMCP(mcpArgs...)
	return s
}

// emptySandbox makes the internal network and the canary directories, with no gateways yet.
func emptySandbox(t *testing.T) *sandbox {
	t.Helper()
	ctx := context.Background()
	nw, err := network.New(ctx, network.WithInternal())
	require.NoError(t, err)
	t.Cleanup(func() { _ = nw.Remove(ctx) })
	s := &sandbox{t: t, ctx: ctx, net: nw, out: t.TempDir(), marks: t.TempDir()}
	s.scenario, err = filepath.Abs("../sandbox/scenario")
	require.NoError(t, err)
	s.work, s.home = copyCanary(t)
	return s
}

// copyCanary copies the canary tree and makes its working directory a git
// repository whose only commit message is a canary.
func copyCanary(t *testing.T) (work, home string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.CopyFS(root, os.DirFS("../sandbox/canary")))
	work, home = filepath.Join(root, "work"), filepath.Join(root, "home")
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.name=x", "-c", "user.email=x@x", "commit", "-q", "-m", "CANARY-GIT-COMMIT"},
	} {
		git := exec.Command("git", args...)
		git.Dir = work
		out, err := git.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	return work, home
}

// gateway starts a gateway stand-in on the sandbox network under alias.
func (s *sandbox) gateway(alias string, cmd []string, extra ...testcontainers.ContainerCustomizer) *testcontainers.DockerContainer {
	s.t.Helper()
	opts := append([]testcontainers.ContainerCustomizer{
		testcontainers.WithCmd(cmd...),
		network.WithNetwork([]string{alias}, s.net),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, s.out+":/out", s.scenario+":/scenario:ro")
		}),
		testcontainers.WithWaitStrategy(wait.ForLog("listening on")),
	}, extra...)
	ctr, err := testcontainers.Run(s.ctx, goImage, opts...)
	require.NoError(s.t, err)
	s.t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	return ctr
}

// startMCP (re)starts the MCP gateway stand-in; each restart begins a round.
func (s *sandbox) startMCP(args ...string) {
	s.t.Helper()
	if s.mcp != nil {
		require.NoError(s.t, testcontainers.TerminateContainer(s.mcp))
	}
	s.mcp = s.gateway("mcpgw", append([]string{"mcpgw", "-outputs", "/scenario/outputs.json", "-log", "/out/mcp.jsonl"}, args...))
}

type run struct {
	exit int
	took time.Duration
}

// runRound plays one turn of candidate c in the sandbox and waits for it to exit.
func (s *sandbox) runRound(c candidate, prompt string, limit time.Duration) run {
	s.t.Helper()
	env := map[string]string{"AGENT_SESSION_TOKEN": llmgw.DummyKey, "HOME": "/home/agent", "CANARY_ENV": "CANARY-ENV-VALUE"}
	for k, v := range c.env {
		env[k] = v
	}
	return s.runContainer(c.image, c.cmd(prompt, limit), env, limit)
}

func (s *sandbox) runContainer(image string, cmd []string, env map[string]string, limit time.Duration) run {
	s.t.Helper()
	start := time.Now()
	ctr, err := testcontainers.Run(s.ctx, image,
		testcontainers.WithCmd(cmd...),
		testcontainers.WithEnv(env),
		network.WithNetwork(nil, s.net),
		testcontainers.WithConfigModifier(func(cfg *container.Config) {
			cfg.Hostname = "canary-host"
			cfg.WorkingDir = workdir
		}),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, s.work+":"+workdir, s.home+":/home/agent",
				s.marks+":/canary-out", s.scenario+":/in:ro")
		}),
		testcontainers.WithWaitStrategy(wait.ForExit().WithExitTimeout(limit+time.Minute)),
	)
	require.NoError(s.t, err)
	s.t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	took := time.Since(start)
	state, err := ctr.State(s.ctx)
	require.NoError(s.t, err)
	logs, err := ctr.Logs(s.ctx)
	require.NoError(s.t, err)
	text, err := io.ReadAll(logs)
	require.NoError(s.t, err)
	s.t.Logf("%s: exit %d after %s\n%s", strings.Join(cmd, " "), state.ExitCode, took.Round(time.Millisecond), text)
	return run{exit: state.ExitCode, took: took}
}

func (s *sandbox) records() []llmgw.Record {
	return readJSONL[llmgw.Record](s.t, filepath.Join(s.out, "llm.jsonl"))
}

func (s *sandbox) calls() []mcpgw.Call {
	return readJSONL[mcpgw.Call](s.t, filepath.Join(s.out, "mcp.jsonl"))
}

func readJSONL[T any](t *testing.T, path string) []T {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var out []T
	for line := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var v T
		require.NoError(t, json.Unmarshal(line, &v))
		out = append(out, v)
	}
	return out
}

func agentLoop(records []llmgw.Record) []llmgw.Record {
	var out []llmgw.Record
	for _, rec := range records {
		if rec.AgentLoop {
			out = append(out, rec)
		}
	}
	return out
}

func toolsOf(t *testing.T, body json.RawMessage) []string {
	t.Helper()
	var req struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(body, &req))
	names := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func named(c candidate, tools ...string) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, c.prefix+tool)
	}
	slices.Sort(out)
	return out
}

func toolNames(calls []mcpgw.Call) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Tool)
	}
	return out
}

func markers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// The sandbox reaches the two gateways and nothing else (criterion 4): no
// outside host by name, by IPv4 or by IPv6, and no outside name resolves, so
// DNS cannot carry data out either.
func TestOnlyTheGatewaysAreReachable(t *testing.T) {
	s := newSandbox(t, nil, "-tools", "end_turn")

	r := s.runContainer(goImage, []string{"sh", "-c",
		"wget -q -T 5 -O- http://llmgw:8080/healthz && wget -q -T 5 -O- http://mcpgw:8090/healthz" +
			" && ! wget -q -T 5 -O- https://example.com && ! wget -q -T 5 -O- http://1.1.1.1" +
			" && ! wget -q -T 5 -O- 'http://[2606:4700:4700::1111]' && ! nslookup example.com"},
		nil, time.Minute)

	assert.Equal(t, 0, r.exit)
}

// Criterion 1: a turn ends when the agent calls end_turn. Criterion 4: the
// runtime does its work with egress denied.
func TestCriterion1EndTurnEndsTheTurn(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		s := newSandbox(t, []llmgw.Turn{use("get_alert"), use("post_hypothesis"), use("end_turn"), use("get_alert")},
			"-tools", "get_alert,post_hypothesis,end_turn")

		r := s.runRound(c, "prompt-round1.md", time.Minute)

		assert.Equal(t, 0, r.exit, "criterion 4: the turn completes with egress denied")
		assert.Equal(t, []string{"get_alert", "post_hypothesis", "end_turn"}, toolNames(s.calls()))
		assert.Len(t, agentLoop(s.records()), 3, "criterion 1: no model call after end_turn")
	})
}

// Criterion 1: the round's time limit ends the turn.
func TestCriterion1TheTimeLimitEndsTheTurn(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		s := newSandbox(t, []llmgw.Turn{use("slow_read")}, "-tools", "slow_read,end_turn", "-slow", "5m")

		r := s.runRound(c, "prompt-round1.md", 5*time.Second)

		assert.Less(t, r.took, 30*time.Second, "the turn must end near the 5 s limit")
	})
}

// Criteria 2 and 3: the gateway is the only MCP endpoint, and the model is
// offered no tool that reaches outside the sandbox.
func TestCriteria2And3OnlyGatewayTools(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		s := newSandbox(t, []llmgw.Turn{use("get_alert"), use("end_turn")}, "-tools", "get_alert,end_turn")

		s.runRound(c, "prompt-round1.md", time.Minute)

		loop := agentLoop(s.records())
		require.NotEmpty(t, loop)
		for _, rec := range loop {
			assert.Equal(t, named(c, "end_turn", "get_alert"), toolsOf(t, rec.Body), "request %d", rec.Seq)
		}
		assert.Empty(t, markers(t, s.marks), "no MCP server or hook from host configuration ran")
	})
}

// Criterion 5: no host or workspace content reaches the model.
func TestCriterion5NoHostContent(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		s := newSandbox(t, []llmgw.Turn{use("get_alert"), use("end_turn")}, "-tools", "get_alert,end_turn")

		s.runRound(c, "prompt-round1.md", time.Minute)

		records := s.records()
		require.NotEmpty(t, records)
		for _, rec := range records {
			if rec.Path == "/v1/messages" {
				require.NotEmpty(t, rec.Body, "request %d has no readable body, so the search below would prove nothing", rec.Seq)
			}
			for _, canary := range contentCanaries {
				assert.NotContains(t, string(rec.Body), canary, "request %d %s", rec.Seq, rec.Path)
			}
			for _, canary := range metadataCanaries {
				if strings.Contains(string(rec.Body), canary) {
					t.Logf("finding: request %d tells the model %q", rec.Seq, canary)
				}
			}
		}
		for _, call := range s.calls() {
			for _, canary := range contentCanaries {
				assert.NotContains(t, string(call.Args), canary, "tool call %s", call.Tool)
			}
		}
	})
}

// Criterion 6: the context stays bounded between rounds.
func TestCriterion6NothingCarriesOverBetweenRounds(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		s := newSandbox(t, []llmgw.Turn{use("get_alert"), use("end_turn"), use("query_logs"), use("end_turn")},
			"-tools", "get_alert,query_logs,end_turn")
		s.runRound(c, "prompt-round1.md", time.Minute)
		first := len(s.records())

		s.startMCP("-round", "2", "-tools", "get_alert,query_logs,end_turn")
		s.runRound(c, "prompt-round2.md", time.Minute)

		records := s.records()
		require.Greater(t, len(records), first)
		for _, rec := range records[first:] {
			assert.NotContains(t, string(rec.Body), "ROUND-ONE-ONLY", "request %d", rec.Seq)
			assert.NotContains(t, string(rec.Body), "Round 1.", "request %d", rec.Seq)
		}
		t.Logf("first request of each round: %d and %d bytes", len(records[0].Body), len(records[first].Body))
	})
}

// Criterion 7: tool lists change only at round boundaries.
func TestCriterion7ToolListsChangeOnlyBetweenRounds(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		// probe_extra appears when the slow first call starts, so a runtime that
		// listens for tools/list_changed has two seconds to pick it up.
		s := newSandbox(t, []llmgw.Turn{use("slow_read"), use("get_alert"), use("end_turn"), use("query_logs"), use("end_turn")},
			"-tools", "slow_read,get_alert,end_turn", "-add-after-first-call", "probe_extra", "-slow", "2s")
		s.runRound(c, "prompt-round1.md", time.Minute)
		first := len(s.records())

		s.startMCP("-round", "2", "-tools", "query_logs,end_turn")
		s.runRound(c, "prompt-round2.md", time.Minute)

		records := s.records()
		round1 := agentLoop(records[:first])
		require.Len(t, round1, 3)
		for _, rec := range round1 {
			assert.Equal(t, named(c, "end_turn", "get_alert", "slow_read"), toolsOf(t, rec.Body), "round 1 keeps its tools, request %d", rec.Seq)
		}
		round2 := agentLoop(records[first:])
		require.NotEmpty(t, round2)
		for _, rec := range round2 {
			assert.Equal(t, named(c, "end_turn", "query_logs"), toolsOf(t, rec.Body), "round 2 gets its tools, request %d", rec.Seq)
		}
	})
}

// The sandbox holds no model key: the gateway sees only the dummy.
func TestTheSandboxHoldsNoModelKey(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		s := newSandbox(t, []llmgw.Turn{use("end_turn")}, "-tools", "end_turn")

		s.runRound(c, "prompt-round1.md", time.Minute)

		records := s.records()
		require.NotEmpty(t, records)
		dummy := 0
		for _, rec := range records {
			assert.NotEqual(t, "other", rec.KeyHeader, "request %d %s carries a key that is not the dummy", rec.Seq, rec.Path)
			if rec.KeyHeader == "dummy" {
				dummy++
			}
		}
		assert.Positive(t, dummy, "model requests carry the dummy session token")
	})
}
```

- [ ] **Step 3: Run the suite to see it fail**

Run: `go mod tidy && go test -tags spike -count=1 ./criteria`
Expected: FAIL from `TestMain` with `docker build agentswarm-spike-go:latest: exit status 1` and `lstat sandbox: no such file or directory`.

- [ ] **Step 4: Write the gateway commands**

Create `cmd/llmgw/main.go`:

```go
// Command llmgw runs the spike's stand-in LLM gateway: scripted by default,
// or forwarding to -upstream with the key in its own ANTHROPIC_API_KEY.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/jchen0426/agent-swarm/spike/runtime/llmgw"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	script := flag.String("script", "", "scripted mode: a JSON file holding an array of turns")
	upstream := flag.String("upstream", "", "forward mode: the real API base URL")
	logPath := flag.String("log", "/out/llm.jsonl", "file that receives one JSON line per request")
	flag.Parse()

	out, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	opts := llmgw.Options{Log: out}
	if *script != "" {
		raw, err := os.ReadFile(*script)
		if err != nil {
			log.Fatal(err)
		}
		if err := json.Unmarshal(raw, &opts.Script); err != nil {
			log.Fatalf("parse %s: %v", *script, err)
		}
	}
	if *upstream != "" {
		if opts.Upstream, err = url.Parse(*upstream); err != nil {
			log.Fatal(err)
		}
		if opts.APIKey = os.Getenv("ANTHROPIC_API_KEY"); opts.APIKey == "" {
			log.Fatal("forward mode needs ANTHROPIC_API_KEY")
		}
	}
	srv := &http.Server{Addr: *addr, Handler: llmgw.New(&opts), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
```

Create `cmd/mcpgw/main.go`:

```go
// Command mcpgw runs the spike's stand-in MCP gateway.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/spike/runtime/mcpgw"
)

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	outputs := flag.String("outputs", "", "a JSON file mapping read tools to their canned output")
	round := flag.Int("round", 1, "the round to start in")
	tools := flag.String("tools", "get_alert,get_recent_changes,query_logs,post_hypothesis,end_turn", "the round's tools")
	addAfter := flag.String("add-after-first-call", "", "a tool to add after the first call of round 1")
	slow := flag.Duration("slow", 30*time.Second, "how long slow_read takes")
	logPath := flag.String("log", "/out/mcp.jsonl", "file that receives one JSON line per call")
	flag.Parse()

	out, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	opts := mcpgw.Options{
		Log:               out,
		AddAfterFirstCall: *addAfter,
		Delays:            map[string]time.Duration{"slow_read": *slow},
	}
	if *outputs != "" {
		raw, err := os.ReadFile(*outputs)
		if err != nil {
			log.Fatal(err)
		}
		if err := json.Unmarshal(raw, &opts.Outputs); err != nil {
			log.Fatalf("parse %s: %v", *outputs, err)
		}
	}
	gw := mcpgw.New(opts)
	gw.SetRound(*round, strings.Split(*tools, ",")...)
	srv := &http.Server{Addr: *addr, Handler: gw, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
```

- [ ] **Step 5: Write the image**

One image holds the two stand-ins and candidate D. Its user is `agent`, not root. Create `sandbox/go.Dockerfile`:

```dockerfile
# The spike's Go binaries: both gateway stand-ins and candidate D.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/ ./cmd/...

FROM alpine:3.22
RUN adduser -D -h /home/agent agent
COPY --from=build /out/ /usr/local/bin/
USER agent
WORKDIR /work
```

Create `.dockerignore`:

```text
sandbox/canary
**/*_test.go
```

- [ ] **Step 6: Write the canary content**

Each canary is a file that a runtime might read on its own:
- project and user memory (`CLAUDE.md`);
- project settings, with an environment value and a hook that leaves a marker;
- project and user MCP configuration, each with a server whose start leaves a marker;
- a file whose name and content are canaries.

The suite also commits the working directory to git with a canary message, sets a canary environment variable, and names the host and the working directory with canaries.

Create `sandbox/canary/work/CLAUDE.md`:

```markdown
# Project notes

CANARY-PROJECT-MEMORY: a runtime that sends this file to a model leaks host content.
```

Create `sandbox/canary/work/.claude/settings.json`:

```json
{
  "env": {"CANARY_SETTINGS_ENV": "CANARY-SETTINGS-VALUE"},
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "touch /canary-out/settings-hook-ran"}]}]
  }
}
```

Create `sandbox/canary/work/.mcp.json`:

```json
{
  "mcpServers": {
    "canary-project": {"command": "sh", "args": ["-c", "touch /canary-out/project-mcp-started; sleep 5"]}
  }
}
```

Create `sandbox/canary/work/notes/CANARY-FILE-NAME.txt`:

```text
CANARY-FILE-CONTENT
```

Create `sandbox/canary/home/.claude/CLAUDE.md`:

```markdown
CANARY-USER-MEMORY: a runtime that sends this file to a model leaks host content.
```

Create `sandbox/canary/home/.claude.json`:

```json
{
  "mcpServers": {
    "canary-user": {"command": "sh", "args": ["-c", "touch /canary-out/user-mcp-started; sleep 5"]}
  }
}
```

- [ ] **Step 7: Write the scripted scenario**

The same files serve the scripted runs and Task 6's live runs. The known cause is a bad release of `cart`. Create `sandbox/scenario/system.md`:

```markdown
You are release-investigator, one of several agents investigating an incident.
Use your tools to find the cause. Then post exactly one hypothesis with
post_hypothesis. Its key has the form <failureMode>/<service>: failureMode is
one of bad-release, config-change, dependency-failure, resource-exhaustion or
other, and service names the service where the cause lies. Then call end_turn.
```

Create `sandbox/scenario/prompt-round1.md`:

```markdown
Round 1. The board is empty. Alert a1 fired on service cart at 10:00.
```

Create `sandbox/scenario/prompt-round2.md`:

```markdown
Round 2. The board holds one hypothesis: bad-release on cart.
```

Create `sandbox/scenario/outputs.json`:

```json
{
  "get_alert": "alert a1: cart error rate 12% since 10:00 (ROUND-ONE-ONLY marker)",
  "get_recent_changes": "cart: release 2026.10.13-2 at 09:58 by deploy-bot; no other changes in the last 24 h",
  "query_logs": "cart 09:59:12 ERROR NullPointerException at CartService.addItem; first seen after release 2026.10.13-2"
}
```

- [ ] **Step 8: Run the suite to see it pass**

Run: `go test -tags spike -count=1 -v ./criteria`
Expected, after about a minute: `--- PASS` for each of these, each with a `thinloop` subtest, and no `finding:` line:
- `TestOnlyTheGatewaysAreReachable`;
- `TestCriterion1EndTurnEndsTheTurn` and `TestCriterion1TheTimeLimitEndsTheTurn`;
- `TestCriteria2And3OnlyGatewayTools`;
- `TestCriterion5NoHostContent`;
- `TestCriterion6NothingCarriesOverBetweenRounds`;
- `TestCriterion7ToolListsChangeOnlyBetweenRounds`;
- `TestTheSandboxHoldsNoModelKey`.

The run ends with `ok  	github.com/jchen0426/agent-swarm/spike/runtime/criteria`.

- [ ] **Step 9: Lint**

Run: `golangci-lint run --build-tags 'spike live' ./...`
Expected: `0 issues.`

- [ ] **Step 10: Commit**

From the repository root:

```bash
git add spike/runtime/go.mod spike/runtime/go.sum spike/runtime/.dockerignore spike/runtime/cmd/llmgw/main.go spike/runtime/cmd/mcpgw/main.go spike/runtime/sandbox/go.Dockerfile spike/runtime/sandbox/canary spike/runtime/sandbox/scenario spike/runtime/criteria/criteria_test.go
git status --short spike/runtime
git commit -m "test(spike): add the sandbox and the criteria suite for candidate D"
git push
```

Before committing, check that `git status` lists all six canary files, including the dot files under `.claude/` and `.mcp.json`.

---

### Task 5: Candidate B, the Claude Agent SDK

**Files:**
- Create: `spike/runtime/agentsdk/requirements.txt`, `spike/runtime/agentsdk/run_round.py`
- Create: `spike/runtime/sandbox/agentsdk.Dockerfile`
- Test: `spike/runtime/criteria/agentsdk_test.go`

**Interfaces:**
- Consumes: Task 4's suite: `images`, `candidates`, `candidate`, `newSandbox`, `runRound`, `records`, `markers`, `use` and `contentCanaries`.
- Produces:
  - the `agentsdk` candidate, which names gateway tools `mcp__gw__<tool>`;
  - `run_round.py`, with the flags `--mcp-url`, `--model`, `--system-file`, `--prompt-file`, `--round-seconds`, `--max-steps` and `--defaults`.

- [ ] **Step 1: Register the candidate and its control**

The control runs B with the SDK's defaults and requires the canaries to catch something. Without it, the hardened runs could pass because the canaries are blind. Create `criteria/agentsdk_test.go`:

```go
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
```

- [ ] **Step 2: Run the suite to see it fail**

Run: `go test -tags spike -count=1 ./criteria`
Expected: FAIL from `TestMain` with `docker build agentswarm-spike-agentsdk:latest: exit status 1` and `open agentsdk.Dockerfile: no such file or directory`.

- [ ] **Step 3: Write the runner**

The hardened configuration follows Anthropic's documentation for the SDK and the CLI, as of 2026-10-13. Each part does one job:
- `tools=[]` removes every built-in tool.
- `allowed_tools` approves only the gateway's tools. It does not restrict the tool set, so `permission_mode="dontAsk"` denies everything else.
- `setting_sources=[]`, `strict_mcp_config` and a private `CLAUDE_CONFIG_DIR` keep host settings, memory and MCP configuration out.
- The environment switches turn off memory, CLAUDE.md, attachments, git instructions, the attribution header, prompt history, the title request, built-in subagents, auto-compaction, claude.ai connectors and nonessential traffic.
- A `PostToolUse` hook on `mcp__gw__end_turn` ends the session once the call has run.

Create `agentsdk/requirements.txt`:

```text
claude-agent-sdk==0.2.165
```

Create `agentsdk/run_round.py`:

```python
"""Candidate B of ADR-006: one agent's turn in one round on the Claude Agent SDK.

The sandbox passes a session token as ANTHROPIC_API_KEY and the LLM gateway as
ANTHROPIC_BASE_URL. Hardened mode switches off everything documented to load
host or workspace content; --defaults keeps the SDK's defaults, as a control
that shows what the hardening prevents.
"""

import argparse
import asyncio
import json
import os
import sys
import tempfile

from claude_agent_sdk import ClaudeAgentOptions, HookMatcher, ResultMessage, query

END_TOOL = "mcp__gw__end_turn"

HARDENING_ENV = {
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    "DISABLE_AUTOUPDATER": "1",
    "CLAUDE_CODE_DISABLE_AUTO_MEMORY": "1",
    "CLAUDE_CODE_DISABLE_CLAUDE_MDS": "1",
    "CLAUDE_CODE_DISABLE_ATTACHMENTS": "1",
    "CLAUDE_CODE_DISABLE_GIT_INSTRUCTIONS": "1",
    "CLAUDE_CODE_ATTRIBUTION_HEADER": "0",
    "CLAUDE_CODE_SKIP_PROMPT_HISTORY": "1",
    "CLAUDE_CODE_DISABLE_TERMINAL_TITLE": "1",
    "CLAUDE_AGENT_SDK_DISABLE_BUILTIN_AGENTS": "1",
    "DISABLE_AUTO_COMPACT": "1",
    "ENABLE_CLAUDEAI_MCP_SERVERS": "false",
}


async def stop_after_end_turn(_input, _tool_use_id, _context):
    """Ends the session as soon as the agent's end_turn call has run."""
    return {"continue_": False, "stopReason": "end_turn"}


def options(args, system):
    stop = [HookMatcher(matcher=END_TOOL, hooks=[stop_after_end_turn])]
    opts = ClaudeAgentOptions(
        system_prompt=system,
        tools=[],
        allowed_tools=["mcp__gw__*"],
        permission_mode="dontAsk",
        mcp_servers={"gw": {"type": "http", "url": args.mcp_url}},
        max_turns=args.max_steps,
        model=args.model,
        cwd=os.getcwd(),
        hooks={"PostToolUse": stop, "PostToolUseFailure": stop},
    )
    if not args.defaults:
        opts.setting_sources = []
        opts.strict_mcp_config = True
        opts.env = dict(HARDENING_ENV, CLAUDE_CONFIG_DIR=tempfile.mkdtemp(prefix="claude-config-"))
    return opts


async def run(args):
    with open(args.system_file, encoding="utf-8") as f:
        system = f.read()
    with open(args.prompt_file, encoding="utf-8") as f:
        prompt = f.read()
    result = {"outcome": "no_result"}
    async for message in query(prompt=prompt, options=options(args, system)):
        if isinstance(message, ResultMessage):
            result = {"outcome": message.subtype, "turns": message.num_turns, "stop_reason": message.stop_reason}
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--mcp-url", default="http://mcpgw:8090/mcp")
    parser.add_argument("--model", default="claude-sonnet-5-5")
    parser.add_argument("--system-file", default="/in/system.md")
    parser.add_argument("--prompt-file", default="/in/prompt.md")
    parser.add_argument("--round-seconds", type=float, default=120)
    parser.add_argument("--max-steps", type=int, default=30)
    parser.add_argument("--defaults", action="store_true", help="keep the SDK's defaults (control run)")
    args = parser.parse_args()
    try:
        result = asyncio.run(asyncio.wait_for(run(args), timeout=args.round_seconds))
    except TimeoutError:
        result = {"outcome": "deadline"}
    print(json.dumps(result))
    sys.stdout.flush()


if __name__ == "__main__":
    main()
```

- [ ] **Step 4: Write the image**

The SDK publishes no musl wheel, so this image uses a glibc base, not Alpine. Create `sandbox/agentsdk.Dockerfile`:

```dockerfile
# Candidate B: the Claude Agent SDK, which bundles the Claude Code CLI. A glibc
# base, because the SDK ships no musl wheel.
FROM python:3.12-slim
RUN useradd -m -d /home/agent agent
COPY agentsdk/requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r /app/requirements.txt
COPY agentsdk/run_round.py /app/run_round.py
USER agent
WORKDIR /work
```

- [ ] **Step 5: Run the suite and keep its log**

Run:

```bash
go test -tags spike -count=1 -v ./criteria 2>&1 | tee "${TMPDIR:-/tmp}/spike-criteria.log"
```

Expected: FAIL, with exactly one failing subtest, `--- FAIL: TestCriterion7ToolListsChangeOnlyBetweenRounds/agentsdk`. Its assertion shows `mcp__gw__probe_extra` in round-1 requests 2 and 3. Also expected:
- `TestCriterion5NoHostContent/agentsdk` passes, but logs `finding: request N tells the model "/canary-workdir"`;
- `TestCanariesCatchTheSDKDefaults` passes, and logs `leaked [CANARY-PROJECT-MEMORY CANARY-USER-MEMORY]; host configuration started [project-mcp-started settings-hook-ran user-mcp-started]`;
- the log line `exit 0 after …` of `TestCriterion1TheTimeLimitEndsTheTurn/agentsdk` shows B stopping about 6 s after its 5 s limit.

These are spike results, not defects. Keep the log outside the repository: it holds local paths. If any other test fails, stop and investigate before Task 6.

- [ ] **Step 6: Lint**

Run: `golangci-lint run --build-tags 'spike live' ./...`
Expected: `0 issues.`

- [ ] **Step 7: Commit**

From the repository root:

```bash
git add spike/runtime/agentsdk/requirements.txt spike/runtime/agentsdk/run_round.py spike/runtime/sandbox/agentsdk.Dockerfile spike/runtime/criteria/agentsdk_test.go
git commit -m "test(spike): add candidate B, the Claude Agent SDK, to the criteria suite"
git push
```

---

### Task 6: Measurements: live runs, code size, release cadence and candidate E

**Files:**
- Test: `spike/runtime/criteria/live_test.go`
- Create: `spike/runtime/tools/cadence.py`
- Create: `spike/runtime/results/RESULTS.md`

**Interfaces:**
- Consumes: Task 4's suite: `forEach`, `emptySandbox`, `(*sandbox).gateway`, `startMCP`, `runRound`, `calls`, `records` and `agentLoop`.
- Produces: `results/RESULTS.md`, which Task 7 and ADR-006 cite.

- [ ] **Step 1: Write the live test**

It runs each candidate three times against the real model, with the LLM stand-in in forward mode. The key lives only in the stand-in's container, which also joins the bridge network to reach the API. The test also checks that no request leaves the sandbox with a real key. Create `criteria/live_test.go`:

```go
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
```

- [ ] **Step 2: Run it without a key**

Run: `env -u ANTHROPIC_API_KEY go test -tags 'spike live' -count=1 -v -run TestLiveDiagnosis ./criteria`
Expected: `--- SKIP: TestLiveDiagnosis`, then `ok`.

- [ ] **Step 3: Run it with the author's key**

This step needs the author. Six short runs of `claude-sonnet-5-5` on a small context cost well under a dollar.

```bash
ANTHROPIC_API_KEY="$AUTHOR_ANTHROPIC_KEY" go test -tags 'spike live' -count=1 -v -run TestLiveDiagnosis ./criteria 2>&1 | tee "${TMPDIR:-/tmp}/spike-live.log"
```

Expected, for each candidate:
- three lines `<candidate> run N: hypotheses [...], M model calls, Ts`;
- a summary line `<candidate>: K of 3 runs reached bad-release/cart`;
- the test passes only if every run ends with `end_turn`, and no request carries a key other than the dummy.

If no key is available, skip this step and write "not run" in RESULTS.md; the decision is then provisional (decision rule, point 4).

- [ ] **Step 4: Measure the code for one diagnosis flow**

Run: `wc -l thinloop/thinloop.go cmd/thinloop/main.go agentsdk/run_round.py`
Expected, as verified: 142, 69 and 92 lines. That is 211 lines of Go for D, and 92 lines of Python for B.

- [ ] **Step 5: Count recent releases**

Create `tools/cadence.py`:

```python
"""Count releases in the last 90 days: PyPI JSON on stdin, or Go proxy .info lines with --go."""
import datetime
import json
import sys

cut = datetime.datetime.now(datetime.timezone.utc) - datetime.timedelta(days=90)
if "--go" in sys.argv:
    times = [json.loads(line)["Time"] for line in sys.stdin if line.strip()]
else:
    releases = json.load(sys.stdin)["releases"]
    times = [files[0]["upload_time_iso_8601"] for files in releases.values() if files]
recent = [t for t in times if datetime.datetime.fromisoformat(t.replace("Z", "+00:00")) > cut]
print(len(recent))
```

Run:

```bash
curl -s https://pypi.org/pypi/claude-agent-sdk/json | python3 -I tools/cadence.py
for v in $(curl -s https://proxy.golang.org/github.com/anthropics/anthropic-sdk-go/@v/list); do curl -s "https://proxy.golang.org/github.com/anthropics/anthropic-sdk-go/@v/$v.info"; echo; done | python3 -I tools/cadence.py --go
```

Expected: two counts. On 2026-10-13 they were 46 for claude-agent-sdk and 27 for anthropic-sdk-go.

- [ ] **Step 6: Desk-check candidate E**

The candidate is HolmesGPT (`https://github.com/HolmesGPT/holmesgpt`, Apache-2.0, a CNCF sandbox project). From its documentation and source, answer one question per criterion:
1. Can one investigation end on a tool call or a deadline, so that it fits one round?
2. Can its tools come only from remote MCP servers that the platform names, with its built-in toolsets off?
3. Can every built-in tool that reaches outside, such as kubectl, shell or HTTP, be switched off?
4. Can it run with egress limited to one model endpoint and one MCP endpoint, including a custom model base URL?
5. Does it load local configuration, runbooks or memory into prompts by default, and can that be switched off?
6. Does it keep context from one investigation to the next?
7. Does it re-read its tool list during an investigation?

Expected: a seven-row table. Each row says met, not met or unknown, with the link that supports it.

- [ ] **Step 7: Write the results**

Create `results/RESULTS.md` with these sections, each filled from the step named:
- **Header table:** the date of this task; the versions (claude-agent-sdk 0.2.165 with CLI 2.1.294, anthropic-sdk-go v1.80.0, MCP Go SDK v1.8.0); and the Docker version from `docker version --format '{{.Server.Version}}'`.
- **Criteria (scripted model):** one row per test of Task 5's run, with PASS or FAIL for `thinloop` and for `agentsdk`.
- **Findings:** the metadata finding of criterion 5; B's stop time after the round limit; and the control's `leaked` and `started` lists, all from the Task 5 log.
- **Live runs:** the summary lines of Step 3, or "not run".
- **Code for one diagnosis flow:** Step 4.
- **Release cadence:** Step 5, with the date counted.
- **Candidate E:** Step 6's table.

- [ ] **Step 8: Lint and commit**

Run: `golangci-lint run --build-tags 'spike live' ./...`
Expected: `0 issues.`

From the repository root:

```bash
git add spike/runtime/criteria/live_test.go spike/runtime/tools/cadence.py spike/runtime/results/RESULTS.md
git commit -m "docs(spike): record the runtime spike's measurements"
git push
```

---

### Task 7: The decision in ADR-006

**Files:**
- Modify: `plan/adr/ADR-006-agent-runtime-sdk-sandbox.md`
- Modify: `plan/adr/README.md`
- Modify: `plan/2026-10-12-implementation-roadmap.md`
- Modify: `README.md`
- Create: `spike/runtime/README.md`

**Interfaces:**
- Consumes: `spike/runtime/results/RESULTS.md` (Task 6).
- Produces: ADR-006 with status Accepted, or Accepted (provisional) when the live test did not run.

- [ ] **Step 1: Apply the decision rule**

Apply the four points of "Decision rule" above to RESULTS.md.

Expected, from the verified results: B meets criteria 1–6 only through configuration, and needs the gateway rule for criterion 7. D meets all seven by construction. Unless the live runs rule D out, the rule selects D.

If the rule selects B instead, Step 2 changes in three places. The Decision section names B with its hardened configuration. The rejected options name D, with the reason the rule gave. The consequences add the gateway rule and a rerun of the suite on every SDK upgrade.

- [ ] **Step 2: Rewrite ADR-006**

Replace the whole file with the text below. Write the day you run this step where it says `2026-10-DD`, and add "(provisional)" to the status if the live test did not run. Write the E line of "Rejected options" from the desk check.

````markdown
# ADR-006 Agent runtime: a thin loop on the Messages API

| | |
|---|---|
| Status | Accepted. Spike C (roadmap Plan 2) decided it. |
| Date | 2026-10-10; revised 2026-10-12; decided 2026-10-DD |
| Related | Spec Sections 2, 5.2, 5.6 and 9.3; roadmap Plan 2; `spike/runtime/results/RESULTS.md`; ADR-001; ADR-007; ADR-013; ADR-015 |

## Context

- Third-party agent runtimes ship built-in tools for shell, files and the web. Used as they come, those tools would bypass the gateways, the budgets and the audit.
- The platform must control how context is built (redaction, data boundaries, round snapshots) and keep it bounded between rounds. In SDK-style runtimes the runtime manages much of this itself.
- Spike C tested the claims about SDK defaults in a sandbox, with canaries in the working directory, the home directory, settings, git and the environment.
- The platform is written in Go (ADR-015). The runtime inside the sandbox may use another language (spec Section 2.2).

## Options

- **A.** Build a full agent framework.
- **B.** The Claude Agent SDK, with the platform controlling the tool surface and egress.
- **C.** Run a coding-agent command-line tool directly.
- **D.** A thin loop on the model API: tool calls and structured output, with a step limit.
- **E.** An off-the-shelf diagnosis tool used as a runtime (ADR-001), where one can meet the criteria below.

## Decision

D: a thin loop on the Messages API, written in Go, one process per agent per round.

Spike C ran B and D in a sandbox that reached only stand-ins for the two gateways. The decision rule of roadmap Plan 2 chose D: it meets all seven criteria by construction. B meets criteria 1–6 only through configuration, and fails criterion 7 on its own.

The criteria still bind any future runtime:

1. **Round-based turns.** A turn ends when the agent calls `end_turn` or the round time limit passes.
2. **Gateway-only MCP.** The platform's MCP gateway is the only MCP endpoint.
3. **No reach outside.** Built-in tools that reach outside the sandbox are disabled.
4. **Egress denied by default.** Only the two gateways are reachable.
5. **No host content.** Host or workspace configuration never enters prompts.
6. **Bounded context.** The session context is kept bounded between rounds.
7. **Tool lists change at round boundaries** only.

## Results

| Criterion | D: thin loop | B: Claude Agent SDK 0.2.165, hardened |
|---|---|---|
| 1. Round-based turns | Met. It ends at `end_turn`, and about 0.4 s after the round limit | Met, with a `PostToolUse` hook on `end_turn`. It stops about 6 s after the round limit |
| 2. Gateway-only MCP | Met by construction | Met only with `strict_mcp_config`, `setting_sources=[]` and a private `CLAUDE_CONFIG_DIR`. With its defaults, the project's and the user's MCP servers started |
| 3. No reach outside | Met: the model sees only the gateway's tools | Met with `tools=[]` |
| 4. Egress denied | Met | Met, with nonessential traffic switched off |
| 5. No host content | Met: requests hold only the skill, the round's prompt and the turn | Met for content only when hardened, with twelve environment switches. With its defaults, CLAUDE.md content reached the model. Hardened, it still sends its working directory |
| 6. Bounded context | Met by construction: a new process each round | Met: no session persistence and a new configuration directory each run |
| 7. Tool lists change at round boundaries | Met: it lists tools once per turn | Not met: it refreshes on `tools/list_changed` within a turn |

Also measured, in `spike/runtime/results/RESULTS.md`:
- **Code for one diagnosis flow:** 211 lines of Go for D; 92 lines of Python for B, plus its hardening.
- **Release cadence over 90 days:** 46 releases of claude-agent-sdk, each bundling a new CLI; 27 of anthropic-sdk-go.
- **Live runs** on the real model, and **the desk check of E**.

Whatever the runtime:

- the sandbox holds no model key; the LLM gateway adds it outside the sandbox (ADR-007);
- only tools registered with the platform are exposed;
- a lost session restarts from the current board snapshot, never from the runtime's own session store (spec Section 5.2);
- the runtime version is pinned in the runtime image, which every work item locks (spec Section 3);
- the MCP gateway holds each session's tool list until its round ends, and sends no `tools/list_changed` within a round (criterion 7, as a gateway rule).

## Rejected options

- **A:** expensive, and not where the platform differs.
- **B:** it meets criteria 2 and 5 only through about fifteen settings, which its frequent releases can change silently. With its defaults it leaks host content into prompts. It fails criterion 7 on its own. It adds Python and a bundled CLI of about 240 MB to a Go platform.
- **C:** it loads project configuration automatically and updates itself, so it is the least controllable.
- **E:** one line from the desk check. E stays an external comparator either way (spec Section 13.3).

## Consequences

- The platform owns the loop. Plan 7's runner builds it test-driven, including what the SDK would have supplied: retries on model errors, prompt caching, and step and token budgets.
- The runner is Go, like the platform (ADR-015), and the runtime image needs neither Python nor Node.
- The spike's sandbox suite, `spike/runtime/criteria`, is the conformance test for the runner. Plan 7 ports it.
- The platform's contract with any runtime stays "MCP tools plus the LLM gateway". The Go runner starts the runtime inside a sandbox (ADR-015).

## Reversibility

Medium. The runtime is swappable behind that contract, and the suite tells whether a candidate meets the criteria.

## Revisit when

- The loop needs a capability that would cost more to build than to adopt, such as managing context within a long round.
- An SDK offers a documented locked-down mode that passes the suite with its defaults.
- A second model vendor is needed (ADR-007).

## History

- **2026-10-10:** proposed: spike C to choose between the Claude Agent SDK and a thin loop.
- **2026-10-12:** rewritten in English. The seven criteria added, and off-the-shelf tools added as candidates (spec Section 16).
- **2026-10-DD:** decided by spike C: D. B failed criterion 7 on its own, and met criteria 2 and 5 only through configuration.
````

- [ ] **Step 3: Update the indexes**

In `plan/adr/README.md`, replace the ADR-006 row with:

```markdown
| [006](ADR-006-agent-runtime-sdk-sandbox.md) | Agent runtime: a thin loop on the Messages API, chosen by a spike | Accepted |
```

In `plan/2026-10-12-implementation-roadmap.md`, replace the first sentence of Section 5 with:

```markdown
Plan 1 was executed on 2026-10-12, and Plan 2 on 2026-10-DD: ADR-006 chose a thin loop on the Messages API.
```

In the same file's header table, change the Plan 2 row's note from "(draft for review)" to "(executed)".

In `README.md`, change the Plan 2 entry's note from "(draft for review)" to "(executed)", and extend the status line to end with:

```markdown
Plan 2 (the runtime spike) chose the agent runtime (ADR-006).
```

- [ ] **Step 4: Write the spike's README**

Create `spike/runtime/README.md`:

```markdown
# Runtime spike (ADR-006)

Evidence for ADR-006, kept so that the criteria can be rerun when a runtime changes. It is not platform code: nothing in the main module imports it.

| Path | What it is |
|---|---|
| `llmgw/`, `mcpgw/` | Stand-ins for the LLM gateway and the MCP gateway |
| `thinloop/`, `cmd/thinloop/` | Candidate D, the chosen runtime |
| `agentsdk/` | Candidate B, the Claude Agent SDK |
| `sandbox/` | Images, canaries and the scripted scenario |
| `criteria/` | One test per criterion, the canary control and the live test |
| `results/RESULTS.md` | What the spike measured |

Run from this directory:

- unit tests: `go test ./...`
- the sandbox suite, with Docker running: `go test -tags spike -count=1 -v ./criteria`
- live runs, with the author's key: `ANTHROPIC_API_KEY=... go test -tags 'spike live' -count=1 -v -run TestLiveDiagnosis ./criteria`
- lint: `golangci-lint run --build-tags 'spike live' ./...`

Rerun the sandbox suite whenever a candidate's version changes (ADR-006, "Revisit when").
```

- [ ] **Step 5: Commit**

From the repository root:

```bash
git add plan/adr/ADR-006-agent-runtime-sdk-sandbox.md plan/adr/README.md plan/2026-10-12-implementation-roadmap.md README.md spike/runtime/README.md
git commit -m "docs(adr): decide the agent runtime from the spike's results"
git push
```

## After the last task

Run the unit tests and the sandbox suite once more on a clean checkout:

```bash
cd spike/runtime
go test ./...
go test -tags spike -count=1 ./criteria
```

The suite still fails `TestCriterion7ToolListsChangeOnlyBetweenRounds/agentsdk`; that is the recorded result. Then ask for the whole-branch review.
