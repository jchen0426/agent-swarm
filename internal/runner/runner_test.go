package runner_test

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/jchen0426/agent-swarm/internal/outcome"
	"github.com/jchen0426/agent-swarm/internal/runner"
)

// reply is one scripted model response: tool calls, or text when calls is
// empty; or, when status, drop or raw is set, an answer that is not one.
type reply struct {
	calls              []string
	text               string // the text of an answer without a tool call; "done" when empty
	empty              bool   // an answer with no content at all
	input, output      int64
	cacheRead, created int64
	delay              time.Duration
	stop               string // the stop reason, when not the usual one
	status             int    // a failure with this status, marked with marker when it is set
	marker             string
	drop               bool   // no answer at all: the connection closes
	broken             bool   // the answer breaks off: its connection closes after its start
	raw                string // the body as it is, with status 200
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
	w.Header().Set("Content-Type", "application/json")
	switch {
	case rep.drop:
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	case rep.broken:
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id": "msg_1", "type": "message", "content": [`)
		_ = http.NewResponseController(w).Flush()
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	case rep.status != 0:
		if rep.marker != "" {
			w.Header().Set(outcome.Header, rep.marker)
		}
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"scripted failure"}}`)
		return
	case rep.raw != "":
		_, _ = io.WriteString(w, rep.raw)
		return
	}
	_ = json.NewEncoder(w).Encode(answer(n, &rep))
}

// answer is a scripted reply's Messages API body.
func answer(n int, rep *reply) map[string]any {
	text := rep.text
	if text == "" {
		text = "done"
	}
	content := []map[string]any{{"type": "text", "text": text}}
	stop := "end_turn"
	if len(rep.calls) > 0 {
		content, stop = nil, "tool_use"
		for i, name := range rep.calls {
			content = append(content, map[string]any{"type": "tool_use", "id": fmt.Sprintf("tu_%d_%d", n, i), "name": name, "input": map[string]any{}})
		}
	}
	if rep.empty {
		content = []map[string]any{}
	}
	if rep.stop != "" {
		stop = rep.stop
	}
	return map[string]any{
		"id": fmt.Sprintf("msg_%d", n), "type": "message", "role": "assistant", "model": "test-model",
		"content": content, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": rep.input, "output_tokens": rep.output,
			"cache_read_input_tokens": rep.cacheRead, "cache_creation_input_tokens": rep.created},
	}
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

// answered is one answer of a fake tool: its blocks, or a failure.
type answered struct {
	blocks  []string
	isError bool
	err     error
}

// toolbox is a fake Toolbox. A tool with answers in queued gives them in
// turn, and its last one again after that; any other gives its output in
// outputs as one block, and end_turn "ended".
type toolbox struct {
	mu      sync.Mutex
	outputs map[string]string
	errors  map[string]bool
	queued  map[string][]answered
	fail    error // returned by every call when set
	panics  bool  // every call panics when set
	// slow holds a tool's answer back this long, whatever its context.
	slow map[string]time.Duration
	// stuck makes the listing wait until its context ends.
	stuck  bool
	listed int
	called []string
}

func (t *toolbox) Tools(ctx context.Context) ([]runner.Tool, error) {
	if t.stuck {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.listed++
	tools := []runner.Tool{{Name: "end_turn", Description: "End your turn.", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	names := map[string]bool{}
	for name := range t.outputs {
		names[name] = true
	}
	for name := range t.queued {
		names[name] = name != "end_turn"
	}
	for name, add := range names {
		if add {
			tools = append(tools, runner.Tool{Name: name, Description: "A read.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)})
		}
	}
	return tools, nil
}

func (t *toolbox) Call(_ context.Context, name string, _ json.RawMessage) (blocks []string, isError bool, err error) {
	time.Sleep(t.slow[name])
	t.mu.Lock()
	defer t.mu.Unlock()
	t.called = append(t.called, name)
	if t.panics {
		panic("the toolbox broke")
	}
	if t.fail != nil {
		return nil, false, t.fail
	}
	if q := t.queued[name]; len(q) > 0 {
		a := q[0]
		if len(q) > 1 {
			t.queued[name] = q[1:]
		}
		return a.blocks, a.isError, a.err
	}
	if name == "end_turn" {
		return []string{"ended"}, false, nil
	}
	return []string{t.outputs[name]}, t.errors[name], nil
}

func (t *toolbox) calls() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.called...)
}

func newClient(t *testing.T, api *fakeAPI) *anthropic.Client {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	c := anthropic.NewClient(option.WithoutEnvironmentDefaults(), option.WithBaseURL(srv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0))
	return &c
}

func config() *runner.Config {
	return &runner.Config{Model: "test-model", MaxTokens: 1024, MaxSteps: 10, System: []string{"SKILL"}}
}

// nudging is config with the nudge on, as agentswarm turn runs.
func nudging() *runner.Config {
	cfg := config()
	cfg.Nudge = true
	return cfg
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
	for stop, outcome := range map[string]runner.Outcome{
		"max_tokens": runner.EndedByMaxTokens, "model_context_window_exceeded": runner.EndedByContextWindow, "refusal": runner.EndedByRefusal,
	} {
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

// With the nudge on, a model that answers without calling a tool is sent
// the platform's one message, after its answer, and goes on (Decision 13).
func TestAnAnswerWithoutAToolIsNudgedOnce(t *testing.T) {
	api := &fakeAPI{script: []reply{{text: "The cause is the flag."}, {calls: []string{"end_turn"}}}}
	tools := &toolbox{}

	res, err := runner.Run(context.Background(), newClient(t, api), tools, nudging(), "the alert")

	require.NoError(t, err)
	require.Equal(t, 2, api.count())
	messages := api.request(1)["messages"].([]any)
	require.Len(t, messages, 3, "the prompt, the answer without a tool, and the nudge")
	assert.Equal(t, "assistant", messages[1].(map[string]any)["role"])
	nudge := messages[2].(map[string]any)
	assert.Equal(t, "user", nudge["role"])
	content := nudge["content"].([]any)
	require.Len(t, content, 1)
	assert.Equal(t, "text", content[0].(map[string]any)["type"])
	assert.Equal(t, runner.Nudge, content[0].(map[string]any)["text"], "the platform's message, exactly")
	assert.Equal(t, runner.EndedByTool, res.Outcome)
	assert.True(t, res.Nudged)
	assert.Equal(t, outcome.ExitEnded, runner.ExitCode(res, err))
	assert.Equal(t, []string{"end_turn"}, tools.calls())
}

// The nudge is sent once per turn: a second answer without a tool, right
// after it or after more tool calls, ends the turn finished.
func TestASecondAnswerWithoutAToolFinishesTheTurn(t *testing.T) {
	for name, script := range map[string][]reply{
		"right after the nudge": {{text: "Nothing found."}, {text: "Still nothing."}, {calls: []string{"end_turn"}}},
		"after more tool calls": {{text: "Nothing found."}, {calls: []string{"get_changes"}}, {text: "Still nothing."}, {calls: []string{"end_turn"}}},
	} {
		t.Run(name, func(t *testing.T) {
			api := &fakeAPI{script: script}

			res, err := runner.Run(context.Background(), newClient(t, api), &toolbox{outputs: map[string]string{"get_changes": "x"}}, nudging(), "the alert")

			require.NoError(t, err)
			assert.Equal(t, runner.EndedByModel, res.Outcome)
			assert.True(t, res.Nudged)
			assert.Equal(t, outcome.ExitFinished, runner.ExitCode(res, err))
			assert.Equal(t, len(script)-1, api.count(), "no request after the second answer without a tool")
		})
	}
}

// Without the nudge, as the mechanism check runs unless it is given
// --nudge, the first answer without a tool ends the turn, as in Plan 3.
func TestWithoutTheNudgeAnAnswerWithoutAToolEndsTheTurn(t *testing.T) {
	api := &fakeAPI{script: []reply{{text: "The cause is the flag."}, {calls: []string{"end_turn"}}}}

	res, err := runner.Run(context.Background(), newClient(t, api), &toolbox{}, config(), "the alert")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedByModel, res.Outcome)
	assert.False(t, res.Nudged)
	assert.Equal(t, 1, api.count())
	assert.Equal(t, outcome.ExitFinished, runner.ExitCode(res, err))
}

// An answer with no content cannot be sent back, as the Messages API takes
// no empty message: the nudge follows the request's last message instead.
func TestAnEmptyAnswerIsNudgedWithoutBeingSentBack(t *testing.T) {
	api := &fakeAPI{script: []reply{{empty: true}, {calls: []string{"end_turn"}}}}

	res, err := runner.Run(context.Background(), newClient(t, api), &toolbox{}, nudging(), "the alert")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedByTool, res.Outcome)
	require.Equal(t, 2, api.count(), "the empty answer was nudged")
	messages := api.request(1)["messages"].([]any)
	require.Len(t, messages, 2, "the prompt and the nudge; no empty answer")
	for i, m := range messages {
		assert.Equal(t, "user", m.(map[string]any)["role"], i)
		assert.NotEmpty(t, m.(map[string]any)["content"], i)
	}
	assert.Equal(t, runner.Nudge, lastBlock(t, api.request(1))["text"])
}

// The Messages API takes no empty text block, so the answer a request sends
// back leaves its empty text blocks out, and its thinking blocks as they are;
// an answer with nothing else is not sent back.
func TestEmptyTextBlocksOfAnAnswerAreNotSentBack(t *testing.T) {
	answer := func(content, stop string) reply {
		return reply{raw: `{"id":"msg_1","type":"message","role":"assistant","model":"test-model","content":` + content +
			`,"stop_reason":"` + stop + `","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`}
	}
	for _, c := range []struct {
		name   string
		first  reply
		echoed []map[string]any // the answer as the next request sends it back; nil when it is not sent back
	}{
		{name: "an answer of empty text", first: answer(`[{"type":"text","text":""}]`, "end_turn")},
		{name: "an empty text beside a tool call", first: answer(`[{"type":"text","text":""},{"type":"tool_use","id":"tu_1","name":"get_changes","input":{}}]`, "tool_use"),
			echoed: []map[string]any{{"type": "tool_use", "id": "tu_1", "name": "get_changes", "input": map[string]any{}}}},
		{name: "a thinking block and an empty text", first: answer(`[{"type":"thinking","thinking":"","signature":"EqQB"},{"type":"text","text":""}]`, "end_turn"),
			echoed: []map[string]any{{"type": "thinking", "thinking": "", "signature": "EqQB"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeAPI{script: []reply{c.first, {calls: []string{"end_turn"}}}}

			res, err := runner.Run(context.Background(), newClient(t, api), &toolbox{outputs: map[string]string{"get_changes": "x"}}, nudging(), "the alert")

			require.NoError(t, err)
			assert.Equal(t, runner.EndedByTool, res.Outcome)
			require.Equal(t, 2, api.count())
			messages := api.request(1)["messages"].([]any)
			if c.echoed == nil {
				require.Len(t, messages, 2, "the prompt and the nudge; no answer sent back")
				assert.Equal(t, runner.Nudge, lastBlock(t, api.request(1))["text"])
				return
			}
			require.Len(t, messages, 3)
			sent := messages[1].(map[string]any)
			assert.Equal(t, "assistant", sent["role"])
			content := sent["content"].([]any)
			require.Len(t, content, len(c.echoed))
			for i, want := range c.echoed {
				assert.Equal(t, want, content[i], "block %d", i)
			}
		})
	}
}

// The turn ends ended only when the end_turn call succeeds (Decision 9's
// turn end): a call the round's end refused leaves the turn interrupted, and
// one answered as a tool's error goes back to the model.
func TestTheTurnEndsOnlyWhenEndTurnSucceeds(t *testing.T) {
	t.Run("the session gone", func(t *testing.T) {
		api := &fakeAPI{script: []reply{{calls: []string{"end_turn"}}}}
		tools := &toolbox{queued: map[string][]answered{"end_turn": {{err: fmt.Errorf("call end_turn: %w: the round has ended: your turn is over", runner.ErrSessionGone)}}}}

		res, err := runner.Run(context.Background(), newClient(t, api), tools, nudging(), "the alert")

		require.ErrorIs(t, err, runner.ErrSessionGone)
		assert.NotEqual(t, runner.EndedByTool, res.Outcome)
		assert.Equal(t, outcome.ExitInterrupted, runner.ExitCode(res, err))
		assert.Equal(t, []string{"end_turn"}, tools.calls(), "a session gone is not tried again")
	})
	t.Run("a tool's error", func(t *testing.T) {
		api := &fakeAPI{script: []reply{{calls: []string{"end_turn"}}, {calls: []string{"end_turn"}}}}
		tools := &toolbox{queued: map[string][]answered{"end_turn": {
			{blocks: []string{"source: board", "refused: post your hypothesis first"}, isError: true},
			{blocks: []string{"source: board", "your turn has ended"}},
		}}}

		res, err := runner.Run(context.Background(), newClient(t, api), tools, nudging(), "the alert")

		require.NoError(t, err)
		require.Equal(t, 2, api.count(), "the refused end_turn went back to the model")
		last := lastBlock(t, api.request(1))
		assert.Equal(t, "tool_result", last["type"])
		assert.Equal(t, true, last["is_error"])
		assert.Equal(t, runner.EndedByTool, res.Outcome)
		assert.Equal(t, outcome.ExitEnded, runner.ExitCode(res, err))
	})
}

// Once the end_turn call succeeded the turn is over: no call of the same
// answer after it is made.
func TestNoCallFollowsASuccessfulEndTurn(t *testing.T) {
	api := &fakeAPI{script: []reply{{calls: []string{"end_turn", "get_changes"}}}}
	tools := &toolbox{outputs: map[string]string{"get_changes": "x"}}

	res, err := runner.Run(context.Background(), newClient(t, api), tools, nudging(), "the alert")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedByTool, res.Outcome)
	assert.Equal(t, []string{"end_turn"}, tools.calls())
}

// No tool call starts once the turn's time is up, even when the call before
// it ran past the deadline (ADR-006, criterion 1).
func TestNoToolCallStartsAfterTheDeadline(t *testing.T) {
	api := &fakeAPI{script: []reply{{calls: []string{"slow_read", "get_changes"}}}}
	tools := &toolbox{outputs: map[string]string{"slow_read": "late", "get_changes": "x"}, slow: map[string]time.Duration{"slow_read": 300 * time.Millisecond}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	res, err := runner.Run(ctx, newClient(t, api), tools, nudging(), "the alert")

	require.NoError(t, err)
	assert.Equal(t, runner.EndedByDeadline, res.Outcome)
	assert.Equal(t, []string{"slow_read"}, tools.calls())
	assert.Equal(t, 1, api.count())
}

// An end_turn answered unavailable is tried once more (Decision 13).
func TestEndTurnIsTriedOnceMoreWhenUnavailable(t *testing.T) {
	unavailable := answered{err: fmt.Errorf("call end_turn: %w: the gateway cannot serve this call now; try again", runner.ErrUnavailable)}
	t.Run("then it succeeds", func(t *testing.T) {
		api := &fakeAPI{script: []reply{{calls: []string{"end_turn"}}}}
		tools := &toolbox{queued: map[string][]answered{"end_turn": {unavailable, {blocks: []string{"source: board", "your turn has ended"}}}}}

		res, err := runner.Run(context.Background(), newClient(t, api), tools, nudging(), "the alert")

		require.NoError(t, err)
		assert.Equal(t, runner.EndedByTool, res.Outcome)
		assert.Equal(t, outcome.ExitEnded, runner.ExitCode(res, err))
		assert.Equal(t, []string{"end_turn", "end_turn"}, tools.calls())
		assert.Equal(t, 1, api.count(), "the second try asks the model nothing")
	})
	t.Run("unavailable twice", func(t *testing.T) {
		api := &fakeAPI{script: []reply{{calls: []string{"end_turn"}}}}
		tools := &toolbox{queued: map[string][]answered{"end_turn": {unavailable, unavailable}}}

		res, err := runner.Run(context.Background(), newClient(t, api), tools, nudging(), "the alert")

		require.ErrorIs(t, err, runner.ErrUnavailable)
		assert.Equal(t, outcome.ExitUnavailable, runner.ExitCode(res, err))
		assert.Equal(t, []string{"end_turn", "end_turn"}, tools.calls(), "tried once more, and no more")
	})
	t.Run("any other tool is not tried again", func(t *testing.T) {
		api := &fakeAPI{script: []reply{{calls: []string{"get_changes"}}, {calls: []string{"end_turn"}}}}
		tools := &toolbox{queued: map[string][]answered{"get_changes": {unavailable, {blocks: []string{"x"}}}}}

		res, err := runner.Run(context.Background(), newClient(t, api), tools, nudging(), "the alert")

		require.ErrorIs(t, err, runner.ErrUnavailable)
		assert.Equal(t, outcome.ExitUnavailable, runner.ExitCode(res, err))
		assert.Equal(t, []string{"get_changes"}, tools.calls())
	})
}

// crashed is the code ExitCode gives a turn that crashed: any code outside
// outcome's table is one, and ExitCode gives 1.
const crashed = 1

// Each line of Decision 13's exit table, each cause on its own row, played
// as a turn of the runtime. The MCP endpoint's answers come from the toolbox
// already given their class (the toolbox's own tests map the endpoint's).
func TestEachOutcomeHasItsExitCode(t *testing.T) {
	failing := func(tool string, err error) *toolbox {
		return &toolbox{queued: map[string][]answered{tool: {{err: err}}}}
	}
	text := func(content, stop string) string {
		return `{"id":"msg_1","type":"message","role":"assistant","model":"test-model","content":` + content +
			`,"stop_reason":"` + stop + `","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`
	}
	for _, c := range []struct {
		name   string
		script []reply
		tools  *toolbox
		config func(*runner.Config)
		within time.Duration // the turn's own deadline, when it has one
		want   int
	}{
		{name: "ended: the end_turn call succeeded", script: []reply{{calls: []string{"end_turn"}}}, want: outcome.ExitEnded},

		{name: "finished: a second answer without a tool, after the nudge", script: []reply{{}, {}}, want: outcome.ExitFinished},
		{name: "finished: the step limit", script: []reply{{calls: []string{"get_changes"}}, {calls: []string{"get_changes"}}},
			config: func(c *runner.Config) { c.MaxSteps = 2 }, want: outcome.ExitFinished},
		{name: "finished: the token budget", config: func(c *runner.Config) { c.TokenBudget = 1000 }, want: outcome.ExitFinished},
		{name: "finished: the prompt limit", script: []reply{{calls: []string{"query_logs"}, input: 80000}},
			tools:  &toolbox{outputs: map[string]string{"query_logs": strings.Repeat("log line\n", 4000)}},
			config: func(c *runner.Config) { c.PromptLimit = 90000 }, want: outcome.ExitFinished},
		{name: "finished: max_tokens", script: []reply{{calls: []string{"get_changes"}, stop: "max_tokens"}}, want: outcome.ExitFinished},
		{name: "finished: max_tokens, its partial tool call never judged", script: []reply{{raw: text(`[{"type":"tool_use","id":"tu_1","name":"get_changes","input":"x"}]`, "max_tokens")}},
			want: outcome.ExitFinished},
		{name: "finished: the model's context window", script: []reply{{calls: []string{"get_changes"}, stop: "model_context_window_exceeded"}}, want: outcome.ExitFinished},
		{name: "finished: the model's context window, its partial tool call never judged",
			script: []reply{{raw: text(`[{"type":"tool_use","id":"tu_1","name":"get_changes","input":"x"}]`, "model_context_window_exceeded")}}, want: outcome.ExitFinished},

		{name: "refused: the LLM gateway's refused", script: []reply{{status: http.StatusForbidden, marker: outcome.Refused}}, want: outcome.ExitRefused},
		{name: "refused: the MCP endpoint's refused", script: []reply{{calls: []string{"get_changes"}}},
			tools: failing("get_changes", fmt.Errorf("call get_changes: %w", runner.ErrRefused)), want: outcome.ExitRefused},
		{name: "refused: the model's own refusal", script: []reply{{stop: "refusal"}}, want: outcome.ExitRefused},

		{name: "vendor: the LLM gateway's vendor", script: []reply{{status: http.StatusBadGateway, marker: outcome.Vendor}}, want: outcome.ExitVendor},

		{name: "interrupted: the LLM gateway's cut", script: []reply{{status: http.StatusGatewayTimeout, marker: outcome.Cut}}, want: outcome.ExitInterrupted},
		{name: "interrupted: the MCP session gone", script: []reply{{calls: []string{"get_changes"}}},
			tools: failing("get_changes", fmt.Errorf("call get_changes: %w: the session has ended", runner.ErrSessionGone)), want: outcome.ExitInterrupted},
		{name: "interrupted: the runtime's own deadline", script: []reply{{delay: 5 * time.Second}}, within: 200 * time.Millisecond, want: outcome.ExitInterrupted},
		{name: "interrupted: the runtime's own deadline, while it lists its tools", tools: &toolbox{stuck: true}, within: 200 * time.Millisecond, want: outcome.ExitInterrupted},
		{name: "interrupted: an end_turn refused because the round ended", script: []reply{{calls: []string{"end_turn"}}},
			tools: failing("end_turn", fmt.Errorf("call end_turn: %w: the round has ended: your turn is over", runner.ErrSessionGone)), want: outcome.ExitInterrupted},

		{name: "unavailable: the LLM gateway's unavailable", script: []reply{{status: http.StatusServiceUnavailable, marker: outcome.Unavailable}}, want: outcome.ExitUnavailable},
		{name: "unavailable: an unmarked reply", script: []reply{{status: http.StatusInternalServerError}}, want: outcome.ExitUnavailable},
		{name: "unavailable: the MCP endpoint's unavailable", script: []reply{{calls: []string{"get_changes"}}},
			tools: failing("get_changes", fmt.Errorf("call get_changes: %w: the gateway cannot serve this call now; try again", runner.ErrUnavailable)), want: outcome.ExitUnavailable},
		{name: "unavailable: no HTTP answer at all", script: []reply{{drop: true}}, want: outcome.ExitUnavailable},
		{name: "unavailable: no HTTP answer at all, an answer that broke off", script: []reply{{broken: true}}, want: outcome.ExitUnavailable},

		{name: "crashed: malformed content, not JSON", script: []reply{{raw: `{"id": "msg_1", "type": "mess`}}, want: crashed},
		{name: "crashed: malformed content, not a list of blocks", script: []reply{{raw: text(`"done"`, "end_turn")}}, want: crashed},
		{name: "crashed: malformed content, a tool call without an input object", script: []reply{{raw: text(`[{"type":"tool_use","id":"tu_1","name":"get_changes","input":"x"}]`, "tool_use")}}, want: crashed},
		{name: "crashed: an unknown block type", script: []reply{{raw: text(`[{"type":"web_search_tool_result","tool_use_id":"x","content":[]}]`, "end_turn")}}, want: crashed},
		{name: "crashed: an unknown stop reason", script: []reply{{stop: "pause_turn"}}, want: crashed},
		{name: "crashed: any other error", script: []reply{{calls: []string{"get_changes"}}},
			tools: failing("get_changes", errors.New("the toolbox lost its way")), want: crashed},
	} {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeAPI{script: c.script}
			tools := c.tools
			if tools == nil {
				tools = &toolbox{outputs: map[string]string{"get_changes": "x"}}
			}
			cfg := nudging()
			if c.config != nil {
				c.config(cfg)
			}
			ctx := context.Background()
			if c.within > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, c.within)
				defer cancel()
			}

			res, err := runner.Run(ctx, newClient(t, api), tools, cfg, "the alert")

			assert.Equal(t, c.want, runner.ExitCode(res, err), "outcome %q, error %v", res.Outcome, err)
		})
	}
	t.Run("crashed: a panic", func(t *testing.T) {
		api := &fakeAPI{script: []reply{{calls: []string{"get_changes"}}}}

		// The runtime does not turn a panic into a code of the table: the
		// process ends with Go's own, outside it.
		assert.Panics(t, func() {
			_, _ = runner.Run(context.Background(), newClient(t, api), &toolbox{panics: true}, nudging(), "the alert")
		})
	})
}

// A gateway's reply that is not a success and carries no marker is the
// gateway's failure, never the runtime's crash (Decision 13).
func TestAnUnmarkedFailureReadsAsUnavailable(t *testing.T) {
	api := &fakeAPI{script: []reply{{status: http.StatusInternalServerError}}}

	res, err := runner.Run(context.Background(), newClient(t, api), &toolbox{}, nudging(), "the alert")

	require.ErrorIs(t, err, runner.ErrUnavailable)
	assert.Equal(t, outcome.ExitUnavailable, runner.ExitCode(res, err))
}

// Each text block of a tool's result reaches the model as its own block:
// the gateway's head, which names the call, apart from the output an agent
// quotes.
func TestEachTextBlockOfAToolResultReachesTheModelOnItsOwn(t *testing.T) {
	api := &fakeAPI{script: []reply{{calls: []string{"search"}}, {calls: []string{"end_turn"}}}}
	blocks := []string{"call_id: c1\nsource: logs/search", "10:02 checkout error: FLAG_X invalid\n10:03 retry"}
	tools := &toolbox{queued: map[string][]answered{"search": {{blocks: blocks}}}}

	_, err := runner.Run(context.Background(), newClient(t, api), tools, nudging(), "the alert")

	require.NoError(t, err)
	result := lastBlock(t, api.request(1))
	require.Equal(t, "tool_result", result["type"])
	content := result["content"].([]any)
	require.Len(t, content, 2)
	for i, want := range blocks {
		block := content[i].(map[string]any)
		assert.Equal(t, "text", block["type"])
		assert.Equal(t, want, block["text"])
	}
}

// The Messages API takes no empty text block, so an empty one of a tool's
// result is left out, and a result with no text has no content.
func TestAToolResultsEmptyTextBlocksAreLeftOut(t *testing.T) {
	api := &fakeAPI{script: []reply{{calls: []string{"search", "count"}}, {calls: []string{"end_turn"}}}}
	tools := &toolbox{queued: map[string][]answered{
		"search": {{blocks: []string{"call_id: c1", "", "a line"}}},
		"count":  {{blocks: []string{""}}},
	}}

	_, err := runner.Run(context.Background(), newClient(t, api), tools, nudging(), "the alert")

	require.NoError(t, err)
	messages := api.request(1)["messages"].([]any)
	results := messages[len(messages)-1].(map[string]any)["content"].([]any)
	require.Len(t, results, 2)
	search := results[0].(map[string]any)["content"].([]any)
	require.Len(t, search, 2)
	assert.Equal(t, "call_id: c1", search[0].(map[string]any)["text"])
	assert.Equal(t, "a line", search[1].(map[string]any)["text"])
	assert.NotContains(t, results[1].(map[string]any), "content")
}

// Each of the agent's skills is its own system block, in order; the last
// carries the cache's breakpoint.
func TestEachSkillIsItsOwnSystemBlock(t *testing.T) {
	api := &fakeAPI{script: []reply{{calls: []string{"end_turn"}}}}
	cfg := nudging()
	cfg.System = []string{"You are alpha.", "Read the logs before the metrics."}

	_, err := runner.Run(context.Background(), newClient(t, api), &toolbox{}, cfg, "the alert")

	require.NoError(t, err)
	system := api.request(0)["system"].([]any)
	require.Len(t, system, 2)
	for i, want := range cfg.System {
		block := system[i].(map[string]any)
		assert.Equal(t, "text", block["type"])
		assert.Equal(t, want, block["text"])
	}
	assert.Nil(t, system[0].(map[string]any)["cache_control"])
	assert.NotNil(t, system[1].(map[string]any)["cache_control"])
}

func lastBlock(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	messages := req["messages"].([]any)
	content := messages[len(messages)-1].(map[string]any)["content"].([]any)
	return content[len(content)-1].(map[string]any)
}
