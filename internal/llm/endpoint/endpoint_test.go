package endpoint_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	llmanthropic "github.com/jchen0426/agent-swarm/internal/llm/anthropic"
	"github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/llm/endpoint"
	"github.com/jchen0426/agent-swarm/internal/llm/memory"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
	"github.com/jchen0426/agent-swarm/internal/runner"
)

const (
	model  = "claude-haiku-5-5"
	commit = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	token  = "session-token"
)

type revisions struct{ rev *config.Revision }

func (r *revisions) At(context.Context, string) (*config.Revision, error) { return r.rev, nil }

type key struct{}

func (key) Secret(context.Context, string) (string, error) { return "vendor-key", nil }

type ids struct{ n atomic.Int32 }

func (i *ids) NewID() string { return fmt.Sprintf("m-%d", i.n.Add(1)) }

// vendor is the model vendor's Messages API, scripted: it answers the n-th
// request with the n-th reply, the last repeating, and keeps each
// request's headers and body.
type vendor struct {
	mu      sync.Mutex
	replies []string
	headers []http.Header
	bodies  []string
	// slow holds the first answer back this long.
	slow time.Duration
	// active counts the requests under way, and most the most at once.
	active, most atomic.Int32
}

func (v *vendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	active := v.active.Add(1)
	defer v.active.Add(-1)
	for {
		most := v.most.Load()
		if active <= most || v.most.CompareAndSwap(most, active) {
			break
		}
	}
	b, _ := io.ReadAll(r.Body)
	v.mu.Lock()
	n := len(v.bodies)
	v.headers, v.bodies = append(v.headers, r.Header.Clone()), append(v.bodies, string(b))
	reply, slow := v.replies[min(n, len(v.replies)-1)], v.slow
	v.mu.Unlock()
	if n == 0 {
		time.Sleep(slow)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Request-Id", fmt.Sprintf("req_%d", n+1))
	_, _ = io.WriteString(w, reply)
}

// signature stands in for a thinking block's signature, as long as the
// model's are.
var signature = strings.Repeat("CAQSjQUKEAgS", 73)

// message is the vendor's answer that calls tool with input, in the shape
// the platform's model answers in: a thinking block with no text and a
// signature first, a tool call with its caller, and every field of usage
// the vendor reports.
func message(n int, tool, input string) string {
	return fmt.Sprintf(`{"id": "msg_01%024d", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
 "content": [{"type": "thinking", "thinking": "", "signature": "%s%d"},
  {"type": "tool_use", "id": "toolu_01%022d", "name": "%s", "input": %s, "caller": {"type": "direct"}}],
 "stop_reason": "tool_use", "stop_sequence": null, "container": null, "diagnostics": null, "stop_details": null,
 "usage": {"input_tokens": 4, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 5228,
  "cache_creation": {"ephemeral_5m_input_tokens": 0, "ephemeral_1h_input_tokens": 0}, "output_tokens": 180,
  "output_tokens_details": {"thinking_tokens": 24}, "service_tier": "standard", "inference_geo": "global"}}`, n, signature, n, n, tool, input)
}

type env struct {
	vendor   *vendor
	calls    *memory.Calls
	sessions *memory.Sessions
	session  app.Session
	served   atomic.Int32
	url      string
	client   anthropic.Client
}

// wiring is what the gateway is connected to: the vendor, scripted or real,
// and the secrets its key is read from; the model the agent uses; how long a
// call to the vendor may last (the options' VendorTimeout); and how long the
// session's round lasts.
type wiring struct {
	vendorURL      string
	secrets        llmanthropic.Secrets
	model          string
	timeout, round time.Duration
}

// newEnv puts the gateway between the SDK's client and a scripted vendor:
// client → endpoint → gateway → the vendor adapter → the vendor.
func newEnv(t *testing.T, replies ...string) *env {
	t.Helper()
	e := &env{vendor: &vendor{replies: replies}}
	vendorSrv := httptest.NewServer(e.vendor)
	t.Cleanup(vendorSrv.Close)
	e.connect(t, &wiring{vendorURL: vendorSrv.URL, secrets: key{}, model: model, timeout: 10 * time.Second, round: time.Minute})
	return e
}

// connect puts the gateway between the SDK's client and the vendor w names.
func (e *env) connect(t *testing.T, w *wiring) {
	t.Helper()
	e.calls, e.sessions = memory.NewCalls(), memory.NewSessions()
	v, err := llmanthropic.New(&llmanthropic.Config{BaseURL: w.vendorURL, KeyRef: "secret://llm/vendor", Version: "2023-06-01"}, w.secrets)
	require.NoError(t, err)
	cc, err := celeval.New()
	require.NoError(t, err)
	fsys, err := memfs.New(map[string][]byte{
		"redaction-rules/api-keys.yaml": []byte("kind: RedactionRule\nmetadata: {name: api-keys, owner: security@example.com}\nspec:\n  always: true\n  patterns: ['sk-[a-z0-9]{8}']\n"),
		"agents/flag-investigator.yaml": fmt.Appendf(nil, "kind: Agent\nmetadata: {name: flag-investigator, owner: alice@example.com}\nspec:\n  runtime: thinloop\n  model: %s\n  tools: []\n", w.model),
	})
	require.NoError(t, err)
	rev, err := config.Build(fsys, commit, cc)
	require.NoError(t, err)
	opts := app.Options{MaxBodyBytes: 512 << 10, MaxTokens: 16384, Version: "2023-06-01",
		Limits: cost.Limits{DailyCap: 10_000_000, ShadowCut: 80}, Reference: 100_000,
		Cards:   map[string]cost.Card{w.model: {Base: cost.Rates{Input: 100_000, Output: 500_000, CacheWrite5m: 125_000, CacheWrite1h: 200_000, CacheRead: 10_000}}},
		Vendor:  limits.Settings{Slots: 10, Failures: 3, CoolDown: time.Minute},
		Backoff: []time.Duration{time.Millisecond}, VendorTimeout: w.timeout, RecordTimeout: time.Second, MaxRefusals: 10,
		LastCallWait: 10 * time.Second}
	gw, err := app.New(&app.Ports{Sessions: e.sessions, Revisions: &revisions{rev}, Vendor: v, Calls: e.calls, IDs: &ids{}, Now: time.Now}, &opts)
	require.NoError(t, err)
	e.session = app.Session{Run: "run-1", Agent: "flag-investigator", Commit: commit, Round: 1, Class: cost.Primary,
		Budget: 2_000_000, Deadline: time.Now().Add(w.round)}
	e.sessions.Put(token, &e.session)
	handler := endpoint.New(gw, opts.MaxBodyBytes)
	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.served.Add(1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(gatewaySrv.Close)
	e.url = gatewaySrv.URL
	// The client reads nothing from the developer's ANTHROPIC_ variables or
	// profile: they would add a key, or headers, the gateway refuses.
	e.client = anthropic.NewClient(option.WithoutEnvironmentDefaults(), option.WithBaseURL(gatewaySrv.URL), option.WithAPIKey(token))
}

// The SDK, given the gateway as its base URL and the session's token as its
// key, calls the vendor through it: the vendor gets the deployment's key and
// the gateway's version, and none of the client's headers.
func TestTheSDKCallsTheVendorThroughTheGateway(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{"q": "errors"}`))

	msg, err := e.client.Messages.New(t.Context(), anthropic.MessageNewParams{
		Model: model, MaxTokens: 1024,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("Find why checkout fails."))},
	})

	require.NoError(t, err)
	assert.Equal(t, anthropic.StopReasonToolUse, msg.StopReason)
	require.Len(t, e.vendor.headers, 1)
	h := e.vendor.headers[0]
	assert.Equal(t, "vendor-key", h.Get("X-Api-Key"))
	assert.Equal(t, "2023-06-01", h.Get("Anthropic-Version"))
	for name := range h {
		assert.False(t, strings.HasPrefix(name, "X-Stainless"), name)
	}
	assert.NotContains(t, e.vendor.bodies[0], token)
	records := e.calls.Records()
	require.Len(t, records, 1)
	_, o, _, ended := e.calls.Record(records[0].ID)
	require.True(t, ended)
	assert.Equal(t, app.StatusOK, o.Status)
}

// A client may carry the session's token as a bearer token instead of as
// its key.
func TestTheTokenMayBeABearerToken(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{}`))
	client := anthropic.NewClient(option.WithoutEnvironmentDefaults(), option.WithBaseURL(e.url), option.WithAuthToken(token))

	_, err := client.Messages.New(t.Context(), anthropic.MessageNewParams{
		Model: model, MaxTokens: 1024,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	})

	require.NoError(t, err)
	assert.Equal(t, "vendor-key", e.vendor.headers[0].Get("X-Api-Key"))
	assert.Empty(t, e.vendor.headers[0].Get("Authorization"))
}

// The SDK does not repeat a request the gateway refused.
func TestTheSDKDoesNotRepeatARefusal(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{}`))
	e.session.Budget = 0
	e.sessions.Put(token, &e.session)

	_, err := e.client.Messages.New(t.Context(), anthropic.MessageNewParams{
		Model: model, MaxTokens: 1024,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	})

	var apiErr *anthropic.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusForbidden, apiErr.StatusCode)
	assert.Equal(t, int32(1), e.served.Load())
	assert.Empty(t, e.vendor.bodies)
}

// A request larger than the gateway reads is refused, and recorded at the
// size it declared.
func TestARequestTooLargeIsRecordedAtItsSize(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{}`))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.url+"/v1/messages", strings.NewReader(strings.Repeat(" ", 600<<10)))
	require.NoError(t, err)
	req.Header.Set("X-Api-Key", token)

	resp, err := http.DefaultClient.Do(req)

	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
	records := e.calls.Records()
	require.Len(t, records, 1)
	assert.Equal(t, int64(600<<10), records[0].RequestBytes)
}

// A request with a query string is refused and recorded, whatever the
// query asks for: the SDK's beta methods add one.
func TestAQueryStringIsRefused(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{}`))

	_, err := e.client.Messages.New(t.Context(), anthropic.MessageNewParams{
		Model: model, MaxTokens: 1024,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	}, option.WithQuery("beta", "true"))

	var apiErr *anthropic.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.StatusCode)
	assert.Empty(t, e.vendor.bodies)
	records := e.calls.Records()
	require.Len(t, records, 1)
	_, o, _, ended := e.calls.Record(records[0].ID)
	require.True(t, ended)
	assert.Equal(t, app.StatusRefused, o.Status)
}

// A client whose connection drops while its call is under way, and which
// sends the request again, as the SDK does, waits for the first call to
// end: the vendor never has two of a session's calls at once. The second
// call is made, and paid for, too: the runtime's client is to make no
// attempts of its own (Plan 7).
func TestARequestSentAgainWaitsForTheCallItRepeats(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{}`), message(2, "metrics__query", `{}`))
	e.vendor.mu.Lock()
	e.vendor.slow = time.Second
	e.vendor.mu.Unlock()
	var dropped atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if dropped.CompareAndSwap(false, true) {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
		}
		body, _ := io.ReadAll(r.Body)
		req, err := http.NewRequestWithContext(ctx, r.Method, e.url+r.URL.Path, bytes.NewReader(body))
		if !assert.NoError(t, err) {
			return
		}
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		maps.Copy(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(proxy.Close)
	client := anthropic.NewClient(option.WithoutEnvironmentDefaults(), option.WithBaseURL(proxy.URL), option.WithAPIKey(token))

	_, err := client.Messages.New(t.Context(), anthropic.MessageNewParams{
		Model: model, MaxTokens: 1024,
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
	})

	require.NoError(t, err)
	assert.Equal(t, int32(1), e.vendor.most.Load(), "never two of the session's calls at once")
	assert.Len(t, e.calls.Records(), 2)
}

// toolbox has one tool, which answers with a secret, and the end tool.
type toolbox struct{}

func (toolbox) Tools(context.Context) ([]runner.Tool, error) {
	return []runner.Tool{
		{Name: "metrics__query", Description: "Query metrics.", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)},
		{Name: "end_turn", Description: "End your turn.", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}, nil
}

func (toolbox) Call(_ context.Context, name string, _ json.RawMessage) (output string, isError bool, err error) {
	switch name {
	case "metrics__query":
		return "errors rose at 10:02; token sk-aaaaaaaa", false, nil
	case "end_turn":
		return "your turn has ended", false, nil
	}
	return "", false, errors.New("no such tool")
}

// The platform's runtime plays a whole turn through the gateway, against
// answers in the shape the platform's model gives them: every request it
// sends, the answer it echoes back with its thinking block and its tool
// call's caller included, is within the allowlist, and what a tool
// returned reaches the vendor redacted.
func TestARunnerTurnPassesTheAllowlist(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{"q": "errors"}`), message(2, "end_turn", `{}`))

	res, err := runner.Run(t.Context(), &e.client, toolbox{}, &runner.Config{
		Model: model, MaxTokens: 1024, MaxSteps: 5, System: "You investigate incidents.",
	}, "Find why checkout fails.")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedByTool, res.Outcome)
	require.Len(t, e.vendor.bodies, 2)
	assert.NotContains(t, e.vendor.bodies[1], "sk-aaaaaaaa")
	assert.Contains(t, e.vendor.bodies[1], "[redacted:api-keys]")
	for _, rec := range e.calls.Records() {
		_, o, _, _ := e.calls.Record(rec.ID)
		assert.Equal(t, app.StatusOK, o.Status, o.Reason)
	}
}
