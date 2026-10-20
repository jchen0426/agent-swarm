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
	"testing/iotest"
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
	"github.com/jchen0426/agent-swarm/internal/outcome"
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
	gw       *app.Gateway
	lease    *lease
	calls    *memory.Calls
	sessions *memory.Sessions
	session  app.Session
	served   atomic.Int32
	url      string
	client   anthropic.Client
}

// lease is the platform's lease as the gateway sees it: its holding holds,
// whatever its epoch, until it ends.
type lease struct{ ended atomic.Bool }

func (l *lease) Valid(int64) bool { return !l.ended.Load() }

func (l *lease) end() { l.ended.Store(true) }

// wiring is what the gateway is connected to: the vendor, scripted or real,
// and the secrets its key is read from; the model the agent uses; how long a
// call to the vendor may last (the options' VendorTimeout); and how long the
// session's round lasts. change, when set, changes the gateway's ports, its
// options and the session before the gateway is made.
type wiring struct {
	vendorURL      string
	secrets        llmanthropic.Secrets
	model          string
	timeout, round time.Duration
	change         func(*app.Ports, *app.Options, *app.Session)
}

// newEnv puts the gateway between the SDK's client and a scripted vendor:
// client → endpoint → gateway → the vendor adapter → the vendor.
func newEnv(t *testing.T, replies ...string) *env {
	t.Helper()
	v := &vendor{replies: replies}
	e := serve(t, v, nil)
	e.vendor = v
	return e
}

// serve puts the gateway, as change wires it, between the SDK's client and a
// vendor that h answers for.
func serve(t *testing.T, h http.Handler, change func(*app.Ports, *app.Options, *app.Session)) *env {
	t.Helper()
	vendorSrv := httptest.NewServer(h)
	t.Cleanup(vendorSrv.Close)
	e := &env{}
	e.connect(t, &wiring{vendorURL: vendorSrv.URL, secrets: key{}, model: model, timeout: 10 * time.Second, round: time.Minute, change: change})
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
	e.lease = &lease{}
	ports := app.Ports{Sessions: e.sessions, Revisions: &revisions{rev}, Vendor: v, Calls: e.calls, IDs: &ids{}, Now: time.Now, Holding: e.lease}
	e.session = app.Session{Run: "run-1", Agent: "flag-investigator", Commit: commit, Round: 1, Class: cost.Primary,
		Budget: 2_000_000, Deadline: time.Now().Add(w.round), Epoch: 1}
	if w.change != nil {
		w.change(&ports, &opts, &e.session)
	}
	e.gw, err = app.New(&ports, &opts)
	require.NoError(t, err)
	e.sessions.Put(token, &e.session)
	handler := endpoint.New(e.gw, opts.MaxBodyBytes)
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

// The SDK does not repeat a request the gateway refused, even one it would
// repeat but for the answer's x-should-retry: false. A 403 it never repeats,
// so a refusal of the budget cannot show that; a 429 it does, unless told
// not to. A session whose round has no refusals left is shut out, and its
// next request is answered 429.
func TestTheSDKDoesNotRepeatARefusal(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{}`))
	e.session.Budget = 0
	e.sessions.Put(token, &e.session)
	ask := func() *anthropic.Error {
		_, err := e.client.Messages.New(t.Context(), anthropic.MessageNewParams{
			Model: model, MaxTokens: 1024,
			Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
		})
		var apiErr *anthropic.Error
		require.ErrorAs(t, err, &apiErr)
		return apiErr
	}
	// The refusals a round records: connect's MaxRefusals.
	for range 10 {
		require.Equal(t, http.StatusForbidden, ask().StatusCode)
	}
	served := e.served.Load()

	apiErr := ask()

	assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
	assert.Equal(t, served+1, e.served.Load(), "the SDK asked once")
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

func (toolbox) Call(_ context.Context, name string, _ json.RawMessage) (blocks []string, isError bool, err error) {
	switch name {
	case "metrics__query":
		return []string{"call_id: c-1\nsource: metrics/query", "errors rose at 10:02; token sk-aaaaaaaa"}, false, nil
	case "end_turn":
		return []string{"source: board", "your turn has ended"}, false, nil
	}
	return nil, false, errors.New("no such tool")
}

// The platform's runtime plays a whole turn through the gateway, against
// answers in the shape the platform's model gives them: every request it
// sends, the answer it echoes back with its thinking block and its tool
// call's caller included, is within the allowlist, and what a tool
// returned reaches the vendor redacted.
func TestARunnerTurnPassesTheAllowlist(t *testing.T) {
	e := newEnv(t, message(1, "metrics__query", `{"q": "errors"}`), message(2, "end_turn", `{}`))

	res, err := runner.Run(t.Context(), &e.client, toolbox{}, &runner.Config{
		Model: model, MaxTokens: 1024, MaxSteps: 5, System: []string{"You investigate incidents.", "Read the metrics first."},
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

// answered is the vendor's answer without a tool call, in the shape the
// platform's model answers in.
func answered(n int, text string) string {
	return fmt.Sprintf(`{"id": "msg_01%024d", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
 "content": [{"type": "thinking", "thinking": "", "signature": "%s%d"}, {"type": "text", "text": %q}],
 "stop_reason": "end_turn", "stop_sequence": null, "container": null, "diagnostics": null, "stop_details": null,
 "usage": {"input_tokens": 4, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 5228, "output_tokens": 40}}`, n, signature, n, text)
}

// The runtime's nudge needs no change to the allowlist (Decision 13): a turn
// whose model answers without a tool, is nudged, and then calls end_turn,
// passes the gateway with every request.
func TestANudgedRunnerTurnPassesTheAllowlist(t *testing.T) {
	e := newEnv(t, answered(1, "The cause is the flag."), message(2, "end_turn", `{}`))

	res, err := runner.Run(t.Context(), &e.client, toolbox{}, &runner.Config{
		Model: model, MaxTokens: 1024, MaxSteps: 5, System: []string{"You investigate incidents."}, Nudge: true,
	}, "Find why checkout fails.")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedByTool, res.Outcome)
	assert.True(t, res.Nudged)
	require.Len(t, e.vendor.bodies, 2)
	assert.Contains(t, e.vendor.bodies[1], runner.Nudge)
	for _, rec := range e.calls.Records() {
		_, o, _, _ := e.calls.Record(rec.ID)
		assert.Equal(t, app.StatusOK, o.Status, o.Reason)
	}
}

// ask is a request as the runtime sends one, which the gateway forwards.
const ask = `{"model": "claude-haiku-5-5", "max_tokens": 1024, "messages": [{"role": "user", "content": "Find why checkout fails."}]}`

// send posts body to path with the session token tok, as the runtime's
// client does, and returns the gateway's answer, read in full.
func (e *env) send(t *testing.T, path, tok, body string) *http.Response {
	t.Helper()
	resp, err := e.post(t.Context(), path, tok, body)
	require.NoError(t, err)
	return resp
}

// post is send for a goroutine of its own, which cannot stop the test.
func (e *env) post(ctx context.Context, path, tok, body string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", tok)
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, err = io.Copy(io.Discard, resp.Body)
	return resp, err
}

// ask sends ask with the session's token.
func (e *env) ask(t *testing.T) *http.Response {
	t.Helper()
	return e.send(t, "/v1/messages", token, ask)
}

// answering is a vendor that answers every request with status and body.
func answering(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// overloaded is the vendor's answer when it is busy.
const overloaded = `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`

// held is a vendor that holds every request until release is closed, then
// answers it with message 1, and tells got of each request it holds.
func held(got chan<- struct{}, release <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		got <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, message(1, "metrics__query", `{}`))
	})
}

// noKey is a secret store that cannot be read.
type noKey struct{}

func (noKey) Secret(context.Context, string) (string, error) {
	return "", errors.New("the secret store is down")
}

// downSessions cannot read any session.
type downSessions struct{}

func (downSessions) Session(context.Context, string) (app.Session, error) {
	return app.Session{}, errors.New("the session store is down")
}

// downRevisions cannot read any revision.
type downRevisions struct{}

func (downRevisions) At(context.Context, string) (*config.Revision, error) {
	return nil, errors.New("the registry is down")
}

// marked is a reply site of the gateway's: the status it answers with, the
// marker it carries, and a request that meets it.
type marked struct {
	name   string
	status int
	marker string
	meet   func(t *testing.T) *http.Response
}

// Every reply that is not a success carries X-Agentswarm-Outcome, which
// tells the runtime whose the failure was (Plan 7d-1, Decision 13): one row
// per reply site of the decision's table.
func TestEveryNonSuccessReplyCarriesItsMarker(t *testing.T) {
	sites := []marked{
		// The gateway's own refusals.
		{"the allowlist", 400, outcome.Refused, func(t *testing.T) *http.Response {
			e := newEnv(t, message(1, "metrics__query", `{}`))
			return e.send(t, "/v1/messages", token, strings.Replace(ask, `"max_tokens": 1024,`, `"max_tokens": 1024, "metadata": {},`, 1))
		}},
		{"the budget", 403, outcome.Refused, func(t *testing.T) *http.Response {
			return serve(t, answering(200, message(1, "metrics__query", `{}`)), func(_ *app.Ports, _ *app.Options, s *app.Session) { s.Budget = 0 }).ask(t)
		}},
		{"an escape that may hide what a rule matches", 400, outcome.Refused, func(t *testing.T) *http.Response {
			e := newEnv(t, message(1, "metrics__query", `{}`))
			return e.send(t, "/v1/messages", token, strings.Replace(ask, "Find why checkout fails.", `The key is s\\u006b-aaaaaaaa.`, 1))
		}},
		{"the refusals' cap", 429, outcome.Refused, func(t *testing.T) *http.Response {
			e := serve(t, answering(200, message(1, "metrics__query", `{}`)), func(_ *app.Ports, o *app.Options, _ *app.Session) { o.MaxRefusals = 1 })
			require.Equal(t, 404, e.send(t, "/v1/models", token, ask).StatusCode)
			return e.ask(t)
		}},
		{"the refusals' cap, once the session is shut out", 429, outcome.Refused, func(t *testing.T) *http.Response {
			e := serve(t, answering(200, message(1, "metrics__query", `{}`)), func(_ *app.Ports, o *app.Options, _ *app.Session) { o.MaxRefusals = 1 })
			require.Equal(t, 404, e.send(t, "/v1/models", token, ask).StatusCode)
			require.Equal(t, 429, e.ask(t).StatusCode, "the shut-out is recorded")
			return e.ask(t)
		}},
		{"the daily cap", 403, outcome.Refused, func(t *testing.T) *http.Response {
			e := serve(t, answering(200, message(1, "metrics__query", `{}`)), func(_ *app.Ports, o *app.Options, _ *app.Session) { o.Limits.DailyCap = 1000 })
			require.NoError(t, e.calls.Begin(t.Context(), &app.CallRecord{ID: "elsewhere", Run: "run-other", Class: cost.Primary, Estimate: 1000}))
			return e.ask(t)
		}},
		{"a body over the cap", 413, outcome.Refused, func(t *testing.T) *http.Response {
			return newEnv(t, message(1, "metrics__query", `{}`)).send(t, "/v1/messages", token, strings.Repeat(" ", 600<<10))
		}},
		// The model vendor's failures.
		{"a vendor failure passed through after its retries", 529, outcome.Vendor, func(t *testing.T) *http.Response {
			return serve(t, answering(529, overloaded), nil).ask(t)
		}},
		{"the vendor marked degraded, failing fast", 503, outcome.Vendor, func(t *testing.T) *http.Response {
			e := serve(t, answering(529, overloaded), func(_ *app.Ports, o *app.Options, _ *app.Session) {
				o.Vendor, o.Backoff = limits.Settings{Slots: 10, Failures: 1, CoolDown: time.Minute}, nil
			})
			require.Equal(t, 529, e.ask(t).StatusCode, "one failure degrades the vendor")
			return e.ask(t)
		}},
		{"an answer withheld", 502, outcome.Vendor, func(t *testing.T) *http.Response {
			withheld := strings.Replace(message(1, "metrics__query", `{}`), `"content": [`, `"content": [{"type": "text", "text": "Saw s\\u006b-zzzzzzzz."}, `, 1)
			return serve(t, answering(200, withheld), nil).ask(t)
		}},
		{"an answer lost", 502, outcome.Vendor, func(t *testing.T) *http.Response {
			return serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
					_ = conn.Close()
				}
			}), nil).ask(t)
		}},
		{"an answer unreadable", 502, outcome.Vendor, func(t *testing.T) *http.Response {
			return serve(t, answering(200, `{"content": [], "stop_reason": "end_turn"}`), nil).ask(t)
		}},
		{"the vendor not reached", 502, outcome.Vendor, func(t *testing.T) *http.Response {
			gone := httptest.NewServer(http.NotFoundHandler())
			gone.Close()
			e := &env{}
			e.connect(t, &wiring{vendorURL: gone.URL, secrets: key{}, model: model, timeout: 10 * time.Second, round: time.Minute})
			return e.ask(t)
		}},
		// Cut: the round's end, the session's end, a dead token.
		{"the round's deadline", 403, outcome.Cut, func(t *testing.T) *http.Response {
			return serve(t, answering(200, message(1, "metrics__query", `{}`)), func(_ *app.Ports, _ *app.Options, s *app.Session) {
				s.Deadline = time.Now().Add(-time.Second)
			}).ask(t)
		}},
		{"the gateway's cut at the round's end", 504, outcome.Cut, func(t *testing.T) *http.Response {
			got, release := make(chan struct{}, 1), make(chan struct{})
			e := serve(t, held(got, release), func(_ *app.Ports, _ *app.Options, s *app.Session) {
				s.Deadline = time.Now().Add(300 * time.Millisecond)
			})
			t.Cleanup(func() { close(release) }) // before the vendor's server closes
			return e.ask(t)
		}},
		{"the session's end", 502, outcome.Cut, func(t *testing.T) *http.Response {
			got, release, done := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
			e := serve(t, held(got, release), func(_ *app.Ports, _ *app.Options, s *app.Session) { s.Done = done })
			t.Cleanup(func() { close(release) }) // before the vendor's server closes
			go func() {
				<-got
				close(done)
			}()
			return e.ask(t)
		}},
		{"a token with no live session", 401, outcome.Cut, func(t *testing.T) *http.Response {
			return newEnv(t, message(1, "metrics__query", `{}`)).send(t, "/v1/messages", "stolen", ask)
		}},
		// The gateway's own failures.
		{"the gateway stopping", 503, outcome.Unavailable, func(t *testing.T) *http.Response {
			e := newEnv(t, message(1, "metrics__query", `{}`))
			require.NoError(t, e.gw.Drain(t.Context()))
			return e.ask(t)
		}},
		{"the session unreadable", 503, outcome.Unavailable, func(t *testing.T) *http.Response {
			return serve(t, answering(200, message(1, "metrics__query", `{}`)), func(p *app.Ports, _ *app.Options, _ *app.Session) { p.Sessions = downSessions{} }).ask(t)
		}},
		{"the configuration unreadable", 503, outcome.Unavailable, func(t *testing.T) *http.Response {
			return serve(t, answering(200, message(1, "metrics__query", `{}`)), func(p *app.Ports, _ *app.Options, _ *app.Session) { p.Revisions = downRevisions{} }).ask(t)
		}},
		{"the spend unreadable", 503, outcome.Unavailable, func(t *testing.T) *http.Response {
			e := newEnv(t, message(1, "metrics__query", `{}`))
			e.calls.SetFail(errors.New("the store is down"))
			return e.ask(t)
		}},
		{"a call not recorded", 503, outcome.Unavailable, func(t *testing.T) *http.Response {
			e := newEnv(t, message(1, "metrics__query", `{}`))
			e.calls.SetFailCommitted(errors.New("the commit's answer was lost"))
			return e.ask(t)
		}},
		{"an answer not recorded", 503, outcome.Unavailable, func(t *testing.T) *http.Response {
			e := newEnv(t, message(1, "metrics__query", `{}`))
			e.calls.SetFailEnd(errors.New("the store is down"))
			t.Cleanup(func() { e.calls.SetFailEnd(nil) })
			return e.ask(t)
		}},
		{"a refusal not recorded", 503, outcome.Unavailable, func(t *testing.T) *http.Response {
			e := newEnv(t, message(1, "metrics__query", `{}`))
			e.calls.SetFail(errors.New("the store is down"))
			return e.send(t, "/v1/models", token, ask)
		}},
		{"a wrong path", 404, outcome.Unavailable, func(t *testing.T) *http.Response {
			return newEnv(t, message(1, "metrics__query", `{}`)).send(t, "/v1/models", token, ask)
		}},
		{"one call at a time", 429, outcome.Unavailable, func(t *testing.T) *http.Response {
			got, release := make(chan struct{}, 1), make(chan struct{})
			e := serve(t, held(got, release), nil)
			first := make(chan struct{})
			go func() {
				defer close(first)
				_, _ = e.post(context.Background(), "/v1/messages", token, ask)
			}()
			<-got
			second := e.ask(t)
			close(release)
			<-first
			return second
		}},
		{"an agent missing from the configuration", 403, outcome.Unavailable, func(t *testing.T) *http.Response {
			return serve(t, answering(200, message(1, "metrics__query", `{}`)), func(_ *app.Ports, _ *app.Options, s *app.Session) { s.Agent = "nobody" }).ask(t)
		}},
		{"a model with no price", 403, outcome.Unavailable, func(t *testing.T) *http.Response {
			return serve(t, answering(200, message(1, "metrics__query", `{}`)), func(_ *app.Ports, o *app.Options, _ *app.Session) { o.Cards = map[string]cost.Card{} }).ask(t)
		}},
		{"the vendor's key unreadable", 503, outcome.Unavailable, func(t *testing.T) *http.Response {
			vendorSrv := httptest.NewServer(answering(200, message(1, "metrics__query", `{}`)))
			t.Cleanup(vendorSrv.Close)
			e := &env{}
			e.connect(t, &wiring{vendorURL: vendorSrv.URL, secrets: noKey{}, model: model, timeout: 10 * time.Second, round: time.Minute})
			return e.ask(t)
		}},
		{"a holding no longer valid", 503, outcome.Unavailable, func(t *testing.T) *http.Response {
			e := newEnv(t, message(1, "metrics__query", `{}`))
			e.lease.end()
			return e.ask(t)
		}},
		{"a request that could not be read", 400, outcome.Unavailable, func(t *testing.T) *http.Response {
			e := newEnv(t, message(1, "metrics__query", `{}`))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", iotest.ErrReader(errors.New("the client went")))
			req.Header.Set("X-Api-Key", token)
			w := httptest.NewRecorder()
			endpoint.New(e.gw, 512<<10).ServeHTTP(w, req)
			return w.Result()
		}},
	}
	for _, s := range sites {
		t.Run(s.name, func(t *testing.T) {
			resp := s.meet(t)

			assert.Equal(t, s.status, resp.StatusCode)
			assert.Equal(t, s.marker, resp.Header.Get(outcome.Header))
		})
	}
}

// A reply that decline rewrites carries the marker of the reply it sends,
// not of the one it replaced: a refusal, or the round's end, whose record
// cannot be written is answered as the gateway's own failure.
func TestADeclinedReplyKeepsItsMarker(t *testing.T) {
	for name, change := range map[string]func(*app.Ports, *app.Options, *app.Session){
		"a refusal of the budget": func(_ *app.Ports, _ *app.Options, s *app.Session) { s.Budget = 0 },
		"the round's end":         func(_ *app.Ports, _ *app.Options, s *app.Session) { s.Deadline = time.Now().Add(-time.Second) },
	} {
		e := serve(t, answering(200, message(1, "metrics__query", `{}`)), change)
		e.calls.SetFailEnd(errors.New("the store is down"))

		resp := e.ask(t)

		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode, name)
		assert.Equal(t, outcome.Unavailable, resp.Header.Get(outcome.Header), name)
	}
}
