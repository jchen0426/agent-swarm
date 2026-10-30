//go:build live

package endpoint_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/runner"
)

const (
	// liveVendor is the model vendor's API.
	liveVendor = "https://api.anthropic.com"
	// livePrompt makes a turn of two calls, a tool call and then the end tool,
	// and asks for reasoning before the first. The reasoning it asks for is
	// what makes the model think: it thinks only when a task needs it, and the
	// check needs a thinking block to send back.
	livePrompt = `Three services call each other: web calls cart, cart calls pay. Errors at web rose at 10:02, at cart at 10:01, at pay never. Before any tool call, work out which service most likely broke first and why. Then call metrics__query once with q set to that service, then call end_turn.`
	// liveTurns is how many turns the check plays at most to cover the echo
	// of a thinking block: a turn may make a single call, and the model does
	// not always think.
	liveTurns = 3
)

// liveKey is the vendor's key. It is never printed: the one place it can
// come back is the text of an error, which withoutKey cleans.
type liveKey string

// Secret returns the key, whatever reference asks for it.
func (k liveKey) Secret(context.Context, string) (string, error) { return string(k), nil }

// withoutKey returns s with apiKey, if it holds it, left out.
func withoutKey(s, apiKey string) string {
	return strings.ReplaceAll(s, apiKey, "[the vendor's key]")
}

// liveTurnResult is what a turn covered.
type liveTurnResult struct {
	calls, echoed int
	outcome       runner.Outcome
}

// missing says what the turn left uncovered of the echo of a thinking block,
// or "" when it covered it. The echo takes two calls, so that an answer goes
// back through the gateway, and a thinking block in that answer.
func (r liveTurnResult) missing() string {
	switch {
	case r.calls < 2:
		return fmt.Sprintf("the turn made %d call(s) and ended by %s, so no answer went back through the gateway", r.calls, r.outcome)
	case r.echoed == 0:
		return fmt.Sprintf("the model gave no thinking block in the turn's %d calls (it ended by %s)", r.calls, r.outcome)
	}
	return ""
}

// thinkingBlocks counts the thinking blocks, redacted ones too, that a
// recorded request sends back.
func thinkingBlocks(t *testing.T, request string) int {
	t.Helper()
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal([]byte(request), &req), "the recorded request of a forwarded call is not JSON")
	n := 0
	for i := range req.Messages {
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(req.Messages[i].Content, &blocks) != nil {
			continue // a string, which holds no block
		}
		for _, b := range blocks {
			if b.Type == "thinking" || b.Type == "redacted_thinking" {
				n++
			}
		}
	}
	return n
}

// liveEnv puts a new gateway between the SDK's client and the real vendor,
// whose key, apiKey, it reads as the deployment's secret: modelName is the
// agent's model, timeout bounds a call to the vendor, and round is how long
// the session's round lasts.
func liveEnv(t *testing.T, apiKey, modelName string, timeout, round time.Duration) *env {
	t.Helper()
	e := new(env)
	e.connect(t, &wiring{vendorURL: liveVendor, secrets: liveKey(apiKey), model: modelName, timeout: timeout, round: round})
	return e
}

// playLiveTurn plays one turn of the platform's runtime through a new
// gateway to the real vendor, and returns what it covered. It fails the test
// at once if the gateway did not pass a request of the runtime's, which is the
// regression the live check exists for, if a call has no outcome, or if the
// runtime fails. A turn of a single call is no failure of the gateway: it
// covers no echo, and its caller plays another.
func playLiveTurn(t *testing.T, apiKey, modelName string) liveTurnResult {
	t.Helper()
	e := liveEnv(t, apiKey, modelName, 2*time.Minute, 5*time.Minute)

	res, err := runner.Run(t.Context(), &e.client, toolbox{}, &runner.Config{
		Model: modelName, MaxTokens: 1024, MaxSteps: 5, System: []string{"You investigate incidents."},
	}, livePrompt)

	records := e.calls.Records()
	failed := false
	for i := range records {
		_, o, _, ended := e.calls.Record(records[i].ID)
		switch {
		case !ended:
			t.Errorf("call %d of the runtime's turn has no outcome recorded", i+1)
			failed = true
		case o.Status != app.StatusOK:
			t.Errorf("the gateway did not pass call %d of the runtime's turn: status %q, reason: %s", i+1, o.Status, withoutKey(o.Reason, apiKey))
			failed = true
		}
	}
	if err != nil {
		t.Errorf("the runtime's turn failed: %s", withoutKey(err.Error(), apiKey))
		failed = true
	}
	if failed {
		t.FailNow()
	}
	// The second call's request sends the first answer back, and each later
	// one sends back all the answers before it.
	echoed := 0
	for i := 1; i < len(records); i++ {
		echoed = max(echoed, thinkingBlocks(t, records[i].Request))
	}
	return liveTurnResult{calls: len(records), echoed: echoed, outcome: res.Outcome}
}

// writeLiveCheck records that the live check passed, for the tripwire
// (live_check_test.go) to read.
func writeLiveCheck(t *testing.T, modelName string) {
	t.Helper()
	data, err := json.MarshalIndent(liveCheck{SDK: sdkVersion(t), Model: modelName, Checked: time.Now().UTC().Format(time.DateOnly)}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(liveCheckFile), 0o755))
	require.NoError(t, os.WriteFile(liveCheckFile, append(data, '\n'), 0o644))
}

// TestLiveTurnThroughTheGateway plays a turn of the platform's runtime
// through the gateway against the real model vendor, as agents will: every
// request the runtime sends must pass the allowlist, the echo of an answer's
// thinking block included. The allowlist admits what the runtime sends today
// (Decision 2), so this fails when a newer SDK or model sends more, before an
// agent's turn ends at its second call. It needs AGENTSWARM_LIVE_KEY, the
// vendor's key, and the network, and costs a few cents a run, so it runs only
// with the live tag (make test-live). AGENTSWARM_LIVE_MODEL names another
// model than the fixtures'. A turn that covers no echo of a thinking block, for
// it made a single call or the model gave no thinking block, is played again,
// up to three turns. On success it records the SDK it passed with.
func TestLiveTurnThroughTheGateway(t *testing.T) {
	apiKey := os.Getenv("AGENTSWARM_LIVE_KEY")
	if apiKey == "" {
		t.Skip("set AGENTSWARM_LIVE_KEY to the model vendor's key to run this against the vendor")
	}
	modelName := cmp.Or(os.Getenv("AGENTSWARM_LIVE_MODEL"), model)

	var missed []string
	for turn := 1; turn <= liveTurns; turn++ {
		covered := playLiveTurn(t, apiKey, modelName)
		why := covered.missing()
		if why == "" {
			writeLiveCheck(t, modelName)
			t.Logf("the live check passed with %s on %s: %d calls through the gateway, %d thinking blocks echoed, the turn ended by %s; recorded in %s",
				sdkVersion(t), modelName, covered.calls, covered.echoed, covered.outcome, liveCheckFile)
			return
		}
		t.Logf("turn %d did not cover the echo of a thinking block: %s", turn, why)
		missed = append(missed, fmt.Sprintf("turn %d: %s", turn, why))
	}
	t.Fatalf("%d turns did not cover the echo of a thinking block: %s", liveTurns, strings.Join(missed, "; "))
}

const (
	// liveMaxTokens is a turn's max_tokens, runner.maxTokens's default
	// [4,096] (R14).
	liveMaxTokens = 4096
	// echoPrompt asks for reasoning before the answer, which makes the model
	// think, as livePrompt does, and for an answer in one sentence without a
	// tool: an answer the runtime nudges, holding a thinking block and text.
	echoPrompt = `Three services call each other: web calls cart, cart calls pay. Errors at web rose at 10:02, at cart at 10:01, at pay never. Before you answer, work out which service most likely broke first and why. Then answer in one sentence, and call no tool.`
	// rateCalls is how many calls measure a model's output rate.
	rateCalls = 3
	// ratePrompt asks for an answer far longer than liveMaxTokens, so that
	// each call writes until max_tokens cuts it.
	ratePrompt = `Write every whole number from one to three thousand in words, in order, one per line: one, two, three, and so on. Write nothing else: no heading, no numerals, no commentary, and skip or shorten no number.`
	// roundLimit is a round's time limit, by default [150 s] (spec Section
	// 15).
	roundLimit = 150 * time.Second
	// callTimeout is the LLM gateway's call timeout, the deployment's
	// timing.llm.vendorTimeout, by default [10 min].
	callTimeout = 10 * time.Minute
)

// liveCall is a call through the gateway: the answer, or the client's
// error; how long the call took, as the client saw it; and the outcome the
// gateway recorded of it, if it recorded one.
type liveCall struct {
	answer   *anthropic.Message
	err      error
	took     time.Duration
	outcome  app.Outcome
	recorded bool
}

// call sends params through the gateway, as the runtime's client does.
func (e *env) call(ctx context.Context, params *anthropic.MessageNewParams) *liveCall {
	before := len(e.calls.Records())
	start := time.Now()
	answer, err := e.client.Messages.New(ctx, *params)
	c := &liveCall{answer: answer, err: err, took: time.Since(start)}
	if records := e.calls.Records(); len(records) > before {
		_, c.outcome, _, c.recorded = e.calls.Record(records[before].ID)
	}
	return c
}

// taken reports whether the vendor answered the call with 200 and the client
// read the answer.
func (c *liveCall) taken() bool {
	return c.err == nil && c.recorded && c.outcome.Status == app.StatusOK
}

// said says how the call ended, without the vendor's key: that the vendor
// answered 200, or what refused or failed the request, as the gateway
// recorded it. The client's error, which repeats a reason the gateway
// recorded, is added only where the record does not explain the call: it has
// no outcome, as when the gateway cut the request first, or its outcome is a
// success.
func (c *liveCall) said(apiKey string) string {
	var s string
	switch {
	case !c.recorded:
		s = "the gateway recorded no outcome of the call"
	case c.outcome.Status == app.StatusOK:
		s = "the vendor answered 200"
	case c.outcome.Status == app.StatusRefused:
		s = "the gateway refused the request, which never reached the vendor: " + c.outcome.Reason
	default:
		s = c.outcome.Reason
	}
	if c.err != nil && (!c.recorded || c.outcome.Status == app.StatusOK) {
		s += "; the client's error: " + c.err.Error()
	}
	return withoutKey(s, apiKey)
}

// liveTools returns the toolbox's tools as the runtime offers them.
func liveTools(t *testing.T) []anthropic.ToolUnionParam {
	t.Helper()
	tools, err := toolbox{}.Tools(t.Context())
	require.NoError(t, err)
	params := make([]anthropic.ToolUnionParam, 0, len(tools))
	for i := range tools {
		var schema anthropic.ToolInputSchemaParam
		require.NoError(t, json.Unmarshal(tools[i].InputSchema, &schema))
		params = append(params, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name: tools[i].Name, Description: anthropic.String(tools[i].Description), InputSchema: schema,
		}})
	}
	return params
}

// asTheRuntime is a request as the runtime sends one: the turn's max_tokens,
// a skill as the system prompt, with the cache's breakpoint, the toolbox's
// tools, and messages, the last of which lastText makes.
func asTheRuntime(modelName string, tools []anthropic.ToolUnionParam, messages ...anthropic.MessageParam) *anthropic.MessageNewParams {
	return &anthropic.MessageNewParams{
		Model: modelName, MaxTokens: liveMaxTokens, Messages: messages, Tools: tools,
		System: []anthropic.TextBlockParam{{Text: "You investigate incidents.", CacheControl: anthropic.NewCacheControlEphemeralParam()}},
	}
}

// lastText is a user's text as the last message of a request the runtime
// sends, with the cache's breakpoint, which the runtime puts on the last
// block of every request.
func lastText(text string) anthropic.MessageParam {
	return anthropic.NewUserMessage(anthropic.ContentBlockParamUnion{OfText: &anthropic.TextBlockParam{
		Text: text, CacheControl: anthropic.NewCacheControlEphemeralParam(),
	}})
}

// sentBack returns the answer as the runtime's echo sends it back once each
// of its text blocks holds text: without its empty text blocks, which the
// echo leaves out, and with its thinking blocks as they came.
func sentBack(msg *anthropic.Message, text string) anthropic.MessageParam {
	answer := msg.ToParam()
	for i := range answer.Content {
		if b := answer.Content[i].OfText; b != nil {
			b.Text = text
		}
	}
	answer.Content = slices.DeleteFunc(answer.Content, func(b anthropic.ContentBlockParamUnion) bool {
		return b.OfText != nil && b.OfText.Text == ""
	})
	return answer
}

// kinds lists the kinds of an answer's blocks, in order.
func kinds(msg *anthropic.Message) string {
	names := make([]string, 0, len(msg.Content))
	for i := range msg.Content {
		names = append(names, msg.Content[i].Type)
	}
	return cmp.Or(strings.Join(names, ", "), "no block")
}

// notNudged says why an answer cannot test the echoes, or "" when it can: it
// must be one the runtime nudges, which ended by end_turn and calls no tool,
// and hold a thinking block and text.
func notNudged(msg *anthropic.Message) string {
	var thinking, text, calls int
	for i := range msg.Content {
		switch b := msg.Content[i].AsAny().(type) {
		case anthropic.ThinkingBlock, anthropic.RedactedThinkingBlock:
			thinking++
		case anthropic.TextBlock:
			if strings.TrimSpace(b.Text) != "" {
				text++
			}
		case anthropic.ToolUseBlock:
			calls++
		}
	}
	var why []string
	if msg.StopReason != anthropic.StopReasonEndTurn {
		why = append(why, fmt.Sprintf("it ended by %s, not end_turn, so the runtime would not nudge it", msg.StopReason))
	}
	if calls > 0 {
		why = append(why, "it calls a tool, so the runtime would send back the tool's result, not the nudge")
	}
	if thinking == 0 {
		why = append(why, "it holds no thinking block")
	}
	if text == 0 {
		why = append(why, "it holds no text")
	}
	return strings.Join(why, "; ")
}

// TestLiveEchoesOfAThinkingAnswerWithoutText checks, against the real model
// vendor, the two echoes of a nudged answer that Plan 7d-1 left unverified.
// The runtime sends an answer without a tool call back before its nudge, as
// its echo sends every answer: without its empty text blocks, so that an
// answer whose text is empty goes back as its thinking block alone, and with
// any other text as it came, whitespace too. A prompt that asks for reasoning
// first, and for an answer in one sentence without a tool, gives an answer
// holding a thinking block and text; two requests then send that answer back
// through the gateway with the nudge after it, as the runtime sends it: once
// with its text emptied, and once with its text replaced by a space. The test
// logs, for each, whether the vendor answered 200, and fails for each it did
// not, naming it. A first answer that cannot test the echoes, without a
// thinking block or text, or one the runtime would not nudge, fails the test
// before anything is sent back. It needs AGENTSWARM_LIVE_KEY, and plays the
// model AGENTSWARM_LIVE_MODEL names, by default the fixtures', as the live
// check does.
func TestLiveEchoesOfAThinkingAnswerWithoutText(t *testing.T) {
	apiKey := os.Getenv("AGENTSWARM_LIVE_KEY")
	if apiKey == "" {
		t.Skip("set AGENTSWARM_LIVE_KEY to the model vendor's key to run this against the vendor")
	}
	modelName := cmp.Or(os.Getenv("AGENTSWARM_LIVE_MODEL"), model)
	e := liveEnv(t, apiKey, modelName, 2*time.Minute, 5*time.Minute)
	tools := liveTools(t)

	first := e.call(t.Context(), asTheRuntime(modelName, tools, lastText(echoPrompt)))
	if !first.taken() {
		t.Fatalf("the first request, for an answer to send back, got none: %s", first.said(apiKey))
	}
	t.Logf("the first answer of %s holds %s, and ended by %s", modelName, kinds(first.answer), first.answer.StopReason)
	if why := notNudged(first.answer); why != "" {
		t.Fatalf("the first answer cannot test the echoes, so neither was sent: %s", why)
	}

	prompt := anthropic.NewUserMessage(anthropic.NewTextBlock(echoPrompt))
	for _, echo := range []struct{ name, text string }{
		{"its text emptied, which the runtime sends as its thinking block alone", ""},
		{"its text replaced by a space", " "},
	} {
		c := e.call(t.Context(), asTheRuntime(modelName, tools, prompt, sentBack(first.answer, echo.text), lastText(runner.Nudge)))
		if c.taken() {
			t.Logf("the answer sent back with %s, the nudge after it: %s", echo.name, c.said(apiKey))
		} else {
			t.Errorf("the answer sent back with %s, the nudge after it, was not taken: %s", echo.name, c.said(apiKey))
		}
	}
}

// rateModels returns the models AGENTSWARM_LIVE_RATE_MODELS names, separated
// by commas, each trimmed, or the fixtures' model when it is unset or empty.
// The name is kept apart from AGENTSWARM_LIVE_MODEL, the one model the live
// check and the echoes play. An empty entry fails the test, naming the
// variable.
func rateModels(t *testing.T, apiKey string) []string {
	t.Helper()
	list := os.Getenv("AGENTSWARM_LIVE_RATE_MODELS")
	if list == "" {
		return []string{model}
	}
	names := strings.Split(list, ",")
	for i := range names {
		names[i] = strings.TrimSpace(names[i])
		if names[i] == "" {
			t.Fatalf("AGENTSWARM_LIVE_RATE_MODELS has an empty entry, entry %d of %q: name each model, separated by commas",
				i+1, withoutKey(list, apiKey))
		}
	}
	return names
}

// measureRate makes rateCalls calls of modelName through a new gateway, each
// for an answer longer than liveMaxTokens, and logs each call's output tokens
// a second. It returns the lowest rate of the calls that max_tokens cut, and
// that call's number, which is 0 when none was cut. A call that gave no
// answer fails the test, as does an answer that ended before max_tokens cut
// it, whose rate the failure gives and the verdict does not judge.
func measureRate(t *testing.T, apiKey, modelName string) (slowest float64, which int) {
	t.Helper()
	// The session's round outlasts the calls, so that the call timeout alone
	// bounds each, as the gateway's VendorTimeout and the client's own.
	e := liveEnv(t, apiKey, modelName, callTimeout, time.Hour)
	for n := 1; n <= rateCalls; n++ {
		c := e.call(t.Context(), &anthropic.MessageNewParams{
			Model: modelName, MaxTokens: liveMaxTokens,
			Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(ratePrompt))},
		})
		if !c.taken() {
			t.Errorf("%s, call %d of %d, gave no answer after %s: %s", modelName, n, rateCalls, c.took.Round(time.Millisecond), c.said(apiKey))
			continue
		}
		out := c.answer.Usage.OutputTokens
		rate := float64(out) / c.took.Seconds()
		line := fmt.Sprintf("%s, call %d of %d: %d output tokens in %s, %.1f a second; it ended by %s, after %d attempt(s) at the vendor",
			modelName, n, rateCalls, out, c.took.Round(time.Millisecond), rate, c.answer.StopReason, c.outcome.Attempts)
		if c.answer.StopReason != anthropic.StopReasonMaxTokens {
			t.Errorf("%s; max_tokens, %d, did not cut it, so the prompt did not hold the model to max_tokens, and the call's rate is not judged",
				line, liveMaxTokens)
			continue
		}
		t.Log(line)
		if which == 0 || rate < slowest {
			slowest, which = rate, n
		}
	}
	return slowest, which
}

// TestLiveOutputRateFitsARound checks, against the real model vendor, that
// each model the agents use writes a turn's max_tokens, liveMaxTokens, within
// a round's time limit and within the gateway's call timeout (Plan 6b's
// constraint; Plan 7d-1, R14): an answer written slower is cut by the round's
// end. For each model of AGENTSWARM_LIVE_RATE_MODELS (rateModels), it makes
// three calls through the gateway with max_tokens 4,096 and a prompt that asks
// for the numbers from one to three thousand in words, one a line, and logs
// each call's output tokens a second. It fails for a model whose slowest call,
// at its rate, writes 4,096 tokens past the round's 150 s or the call timeout's
// 10 minutes, and for a model with no call max_tokens cut, whose rate is not
// judged. It needs AGENTSWARM_LIVE_KEY, as the live check does.
func TestLiveOutputRateFitsARound(t *testing.T) {
	apiKey := os.Getenv("AGENTSWARM_LIVE_KEY")
	if apiKey == "" {
		t.Skip("set AGENTSWARM_LIVE_KEY to the model vendor's key to run this against the vendor")
	}
	for _, modelName := range rateModels(t, apiKey) {
		slowest, which := measureRate(t, apiKey, modelName)
		if which == 0 {
			t.Errorf("%s: no call wrote max_tokens, %d, so its output rate is not judged", modelName, liveMaxTokens)
			continue
		}
		need := time.Duration(liveMaxTokens / slowest * float64(time.Second))
		fits := true
		for _, bound := range []struct {
			name  string
			limit time.Duration
		}{{"the round's time limit", roundLimit}, {"the call timeout", callTimeout}} {
			if need > bound.limit {
				t.Errorf("%s writes too slowly: its slowest call, call %d, at %.1f output tokens a second, writes %d tokens in %s, past %s, %s",
					modelName, which, slowest, liveMaxTokens, need.Round(time.Millisecond), bound.name, bound.limit)
				fits = false
			}
		}
		if fits {
			t.Logf("%s: its slowest call, call %d, at %.1f output tokens a second, writes %d tokens in %s, within the round's time limit, %s, and the call timeout, %s",
				modelName, which, slowest, liveMaxTokens, need.Round(time.Millisecond), roundLimit, callTimeout)
		}
	}
}
