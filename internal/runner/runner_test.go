package runner_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/runner"
)

// reply is one scripted model response: tool calls, or text when calls is empty.
type reply struct {
	calls              []string
	input, output      int64
	cacheRead, created int64
	delay              time.Duration
	stop               string // the stop reason, when not the usual one
}

// fakeAPI serves scripted Messages API responses and records the requests.
type fakeAPI struct {
	mu       sync.Mutex
	script   []reply
	requests []map[string]any
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	n := len(f.requests)
	var rep reply
	if n <= len(f.script) {
		rep = f.script[n-1]
	}
	f.mu.Unlock()
	if rep.delay > 0 {
		select {
		case <-time.After(rep.delay):
		case <-r.Context().Done():
			return
		}
	}
	content := []map[string]any{{"type": "text", "text": "done"}}
	stop := "end_turn"
	if len(rep.calls) > 0 {
		content, stop = nil, "tool_use"
		for i, name := range rep.calls {
			content = append(content, map[string]any{"type": "tool_use", "id": fmt.Sprintf("tu_%d_%d", n, i), "name": name, "input": map[string]any{}})
		}
	}
	if rep.stop != "" {
		stop = rep.stop
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": fmt.Sprintf("msg_%d", n), "type": "message", "role": "assistant", "model": "test-model",
		"content": content, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": rep.input, "output_tokens": rep.output,
			"cache_read_input_tokens": rep.cacheRead, "cache_creation_input_tokens": rep.created},
	})
}

func (f *fakeAPI) request(i int) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[i]
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// toolbox is a fake Toolbox with fixed outputs.
type toolbox struct {
	mu      sync.Mutex
	outputs map[string]string
	errors  map[string]bool
	fail    error // returned by every call when set
	listed  int
	called  []string
}

func (t *toolbox) Tools(context.Context) ([]runner.Tool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.listed++
	tools := []runner.Tool{{Name: "end_turn", Description: "End your turn.", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	for name := range t.outputs {
		tools = append(tools, runner.Tool{Name: name, Description: "A read.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)})
	}
	return tools, nil
}

func (t *toolbox) Call(_ context.Context, name string, _ json.RawMessage) (out string, isError bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.called = append(t.called, name)
	if t.fail != nil {
		return "", false, t.fail
	}
	if name == "end_turn" {
		return "ended", false, nil
	}
	return t.outputs[name], t.errors[name], nil
}

func newClient(t *testing.T, api *fakeAPI) *anthropic.Client {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	c := anthropic.NewClient(option.WithoutEnvironmentDefaults(), option.WithBaseURL(srv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0))
	return &c
}

func config() *runner.Config {
	return &runner.Config{Model: "test-model", MaxTokens: 1024, MaxSteps: 10, System: "SKILL"}
}

func TestATurnEndsWhenTheAgentCallsEndTurn(t *testing.T) {
	api := &fakeAPI{script: []reply{{calls: []string{"get_changes"}, input: 100, output: 10}, {calls: []string{"end_turn"}, input: 150, output: 5, cacheRead: 90}}}
	tools := &toolbox{outputs: map[string]string{"get_changes": "QUOTE_ADDR changed"}}

	res, err := runner.Run(context.Background(), newClient(t, api), tools, config(), "the alert")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedByTool, res.Outcome)
	assert.Equal(t, 2, res.Steps)
	assert.Equal(t, 2, res.Calls)
	assert.Equal(t, runner.Usage{Input: 250, Output: 15, CacheRead: 90}, res.Usage)
	assert.Equal(t, 1, tools.listed, "the tool list is read once per turn")
	assert.Equal(t, []string{"get_changes", "end_turn"}, tools.called)
}

func TestATurnEndsWhenTheModelStopsCallingTools(t *testing.T) {
	api := &fakeAPI{script: []reply{{input: 100, output: 10}}}

	res, err := runner.Run(context.Background(), newClient(t, api), &toolbox{}, config(), "the alert")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedByModel, res.Outcome)
}

func TestATurnStopsAfterMaxSteps(t *testing.T) {
	api := &fakeAPI{}
	for range 5 {
		api.script = append(api.script, reply{calls: []string{"get_changes"}, input: 10, output: 1})
	}
	cfg := config()
	cfg.MaxSteps = 3

	res, err := runner.Run(context.Background(), newClient(t, api), &toolbox{outputs: map[string]string{"get_changes": "x"}}, cfg, "the alert")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedBySteps, res.Outcome)
	assert.Equal(t, 3, api.count())
}

func TestTheDeadlineEndsTheTurn(t *testing.T) {
	api := &fakeAPI{script: []reply{{calls: []string{"get_changes"}, input: 10, output: 1}, {delay: 5 * time.Second}}}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	res, err := runner.Run(ctx, newClient(t, api), &toolbox{outputs: map[string]string{"get_changes": "x"}}, config(), "the alert")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedByDeadline, res.Outcome)
}

func TestTheTokenBudgetCountsCostWeightedTokens(t *testing.T) {
	// Cache writes weigh 1.25, cache reads a tenth and output five times input.
	u := runner.Usage{Input: 100, Output: 20, CacheRead: 5000, CacheWrite: 400}

	assert.Equal(t, int64(100+100+500+500), u.Weighted())
}

func TestNoCallStartsThatCouldPassTheBudget(t *testing.T) {
	// The first call weighs 100 + 5*20 + 5000/10 = 700. The next request reads
	// about 5,121 tokens; at worst all of them are written to the cache, and
	// the reply is 1,024 tokens long: 6,401 + 5,120 more, which would pass
	// 12,000.
	api := &fakeAPI{script: []reply{
		{calls: []string{"get_changes"}, input: 100, output: 20, cacheRead: 5000},
		{calls: []string{"end_turn"}},
	}}
	cfg := config()
	cfg.TokenBudget = 12000

	res, err := runner.Run(context.Background(), newClient(t, api), &toolbox{outputs: map[string]string{"get_changes": "x"}}, cfg, "the alert")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedByBudget, res.Outcome)
	assert.Equal(t, 1, api.count())
	assert.LessOrEqual(t, res.Usage.Weighted(), cfg.TokenBudget)
}

func TestABudgetTooSmallForOneReplyMakesNoCall(t *testing.T) {
	api := &fakeAPI{}
	cfg := config()
	cfg.TokenBudget = 1000 // less than a 1,024-token reply weighs

	res, err := runner.Run(context.Background(), newClient(t, api), &toolbox{}, cfg, "the alert")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedByBudget, res.Outcome)
	assert.Equal(t, 0, api.count())
}

func TestACutOrRefusedReplyEndsTheTurnWithoutItsToolCalls(t *testing.T) {
	for stop, outcome := range map[string]runner.Outcome{"max_tokens": runner.EndedByMaxTokens, "refusal": runner.EndedByRefusal} {
		api := &fakeAPI{script: []reply{{calls: []string{"get_changes"}, input: 10, output: 1, stop: stop}}}
		tools := &toolbox{outputs: map[string]string{"get_changes": "x"}}

		res, err := runner.Run(context.Background(), newClient(t, api), tools, config(), "the alert")

		require.NoError(t, err, stop)
		assert.Equal(t, outcome, res.Outcome, stop)
		assert.Empty(t, tools.called, "the tool call of a %s reply may be partial", stop)
	}
}

func TestOnlyTheTurnsOwnDeadlineEndsItAsADeadline(t *testing.T) {
	api := &fakeAPI{script: []reply{{calls: []string{"get_changes"}, input: 10, output: 1}}}
	tools := &toolbox{fail: fmt.Errorf("read get_changes: %w", context.DeadlineExceeded)}

	_, err := runner.Run(context.Background(), newClient(t, api), tools, config(), "the alert")

	require.Error(t, err, "a read that timed out on its own is a failure, not the end of the round")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestThePromptLimitStopsTheTurnBeforeABiggerRequest(t *testing.T) {
	api := &fakeAPI{script: []reply{{calls: []string{"query_logs"}, input: 80000, output: 100}, {calls: []string{"end_turn"}}}}
	cfg := config()
	cfg.PromptLimit = 90000
	tools := &toolbox{outputs: map[string]string{"query_logs": strings.Repeat("log line\n", 4000)}}

	res, err := runner.Run(context.Background(), newClient(t, api), tools, cfg, "the alert")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedByPromptLimit, res.Outcome)
	assert.Equal(t, 1, api.count(), "the next request would pass the limit, so it is not sent")
}

func TestAToolErrorGoesBackToTheModel(t *testing.T) {
	api := &fakeAPI{script: []reply{{calls: []string{"get_object"}}, {calls: []string{"end_turn"}}}}
	tools := &toolbox{outputs: map[string]string{"get_object": "no Deployment named x"}, errors: map[string]bool{"get_object": true}}

	_, err := runner.Run(context.Background(), newClient(t, api), tools, config(), "the alert")

	require.NoError(t, err)
	last := lastBlock(t, api.request(1))
	assert.Equal(t, "tool_result", last["type"])
	assert.Equal(t, true, last["is_error"])
}

func TestTheSkillAndTheLatestBlockAreCached(t *testing.T) {
	api := &fakeAPI{script: []reply{{calls: []string{"get_changes"}}, {calls: []string{"end_turn"}}}}

	_, err := runner.Run(context.Background(), newClient(t, api), &toolbox{outputs: map[string]string{"get_changes": "x"}}, config(), "the alert")

	require.NoError(t, err)
	second := api.request(1)
	system := second["system"].([]any)[0].(map[string]any)
	assert.Equal(t, "SKILL", system["text"])
	assert.NotNil(t, system["cache_control"])
	assert.NotNil(t, lastBlock(t, second)["cache_control"])
	first := second["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	assert.Nil(t, first["cache_control"], "one moving breakpoint, not one per message")
}

func TestALogRecordsEveryResponse(t *testing.T) {
	api := &fakeAPI{script: []reply{{calls: []string{"end_turn"}, input: 5, output: 1}}}
	var log strings.Builder
	cfg := config()
	cfg.Log = &log

	_, err := runner.Run(context.Background(), newClient(t, api), &toolbox{}, cfg, "the alert")

	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], `"msg_1"`)
}

func lastBlock(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	messages := req["messages"].([]any)
	content := messages[len(messages)-1].(map[string]any)["content"].([]any)
	return content[len(content)-1].(map[string]any)
}
