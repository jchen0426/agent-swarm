//go:build live

package endpoint_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/llm/app"
	"github.com/jchen0426/agent-swarm/internal/llm/cost"
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
	// liveRound is the session's round in the echo and rate tests. It
	// outlasts their calls, which liveContext ends first, so that the call
	// timeout alone bounds each call.
	liveRound = time.Hour
	// liveMargin is how long before go test's timeout liveContext ends the
	// echo and rate tests' calls.
	liveMargin = time.Minute
	// echoTries is how many times an echo's request is sent while it fails
	// transiently, and transientPause is the wait before each new try. Two
	// waits outlast the minute of cool-down that connect's vendor limits
	// start once three calls in a row have failed, during which the gateway
	// fails a call at once.
	echoTries      = 3
	transientPause = 40 * time.Second
)

// liveContext returns the test's context, ended liveMargin before go test's
// timeout when there is one. The echo and rate tests make every call under
// it, and start no call or try once it has ended. So a vendor that hangs
// ends them with their own messages, never with the timeout's panic, and
// the live tests end within make test-live's timeout: the live check's
// turns end with their rounds, five minutes each.
func liveContext(t *testing.T) context.Context {
	t.Helper()
	deadline, ok := t.Deadline()
	if !ok {
		return t.Context()
	}
	ctx, cancel := context.WithDeadline(t.Context(), deadline.Add(-liveMargin))
	t.Cleanup(cancel)
	return ctx
}

// liveCall is a call through the gateway: the answer, or the client's
// error; how long the call took, as the client saw it; the id of the
// gateway's record of the call, and the outcome recorded, if any; and
// whether the test's context had ended when the call returned.
type liveCall struct {
	answer   *anthropic.Message
	err      error
	took     time.Duration
	id       string
	outcome  app.Outcome
	recorded bool
	late     bool
}

// call sends params through the gateway, once, as the runtime's client does:
// a call the vendor hangs on ends at the call timeout, and is not sent again.
func (e *env) call(ctx context.Context, params *anthropic.MessageNewParams) *liveCall {
	before := len(e.calls.Records())
	start := time.Now()
	answer, err := e.client.Messages.New(ctx, *params, option.WithMaxRetries(0))
	c := &liveCall{answer: answer, err: err, took: time.Since(start), late: ctx.Err() != nil}
	if records := e.calls.Records(); len(records) > before {
		c.id = records[before].ID
		_, c.outcome, _, c.recorded = e.calls.Record(c.id)
	}
	return c
}

// taken reports whether the vendor answered the call with 200 and the client
// read the answer.
func (c *liveCall) taken() bool {
	return c.err == nil && c.recorded && c.outcome.Status == app.StatusOK
}

// transient reports whether the call failed in a way another try may get
// past: a reply of 408, 429 or 5xx, the vendor's, passed on once the
// gateway's own attempts failed, or the gateway's, as when it fails a call at
// once while it holds the vendor degraded. An answer the gateway lost, as when
// the vendor did not answer within the call timeout, is not: another try could
// take as long. Nor is a call the test's context ended.
func (c *liveCall) transient() bool {
	var reply *anthropic.Error
	if c.taken() || c.late || !errors.As(c.err, &reply) || (c.recorded && c.outcome.Status == app.StatusUnknown) {
		return false
	}
	return reply.StatusCode == http.StatusRequestTimeout || reply.StatusCode == http.StatusTooManyRequests ||
		reply.StatusCode >= http.StatusInternalServerError
}

// said says how the call ended, without the vendor's key: that the vendor
// answered 200, or what refused or failed the request, as the gateway
// recorded it, with the gateway's attempts at the vendor. The client's error,
// which repeats a reason the gateway recorded, is added only where the record
// does not explain the call: it has no outcome, as when the gateway cut the
// request first, or its outcome is a success.
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
	if c.recorded && c.outcome.Attempts > 0 {
		s += fmt.Sprintf(", after %d attempt(s) at the vendor", c.outcome.Attempts)
	}
	if c.err != nil && (!c.recorded || c.outcome.Status == app.StatusOK) {
		s += "; the client's error: " + c.err.Error()
	}
	if c.late && !c.taken() {
		s += fmt.Sprintf("; the test's time ran out, %s before go test's timeout", liveMargin)
	}
	return withoutKey(s, apiKey)
}

// callPatiently sends params as call does, up to echoTries times while the
// call fails transiently, waiting transientPause before each new try, so that
// a transient failure is not read as the call's verdict; a refusal, a 4xx, is
// its verdict. It returns the last call, and those that failed transiently
// before it.
func (e *env) callPatiently(ctx context.Context, params *anthropic.MessageNewParams) (last *liveCall, failed []*liveCall) {
	for {
		last = e.call(ctx, params)
		if len(failed)+1 == echoTries || !last.transient() || !pauseFor(ctx, transientPause) {
			return last, failed
		}
		failed = append(failed, last)
	}
}

// pauseFor waits d, and reports whether it did before ctx ended.
func pauseFor(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// sentBlocks reads, in the gateway's record of a call, the answer its request
// sent back, the request's second message, as the vendor got it: its thinking
// blocks, redacted ones too, and the texts of its text blocks. reached is
// false for a call the gateway did not admit, whose record holds no request
// that reached the vendor.
func (e *env) sentBlocks(id string) (thinking int, texts []string, reached bool, err error) {
	rec, _, began, _ := e.calls.Record(id)
	if !began || rec.Estimate == 0 {
		return 0, nil, false, nil
	}
	var req struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal([]byte(rec.Request), &req) != nil || len(req.Messages) != 3 {
		return 0, nil, true, errors.New("its record holds no request of three messages, the prompt, the answer sent back and the nudge")
	}
	for _, b := range req.Messages[1].Content {
		switch b.Type {
		case "thinking", "redacted_thinking":
			thinking++
		case "text":
			texts = append(texts, b.Text)
		}
	}
	return thinking, texts, true, nil
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

// sentBack returns the answer as the runtime's echo sends it back once change
// has changed the text of each of its text blocks: without its empty text
// blocks, which the echo leaves out, and with its thinking blocks as they
// came. It returns the texts it sends back too.
func sentBack(msg *anthropic.Message, change func(string) string) (answer anthropic.MessageParam, texts []string) {
	answer = msg.ToParam()
	for i := range answer.Content {
		if b := answer.Content[i].OfText; b != nil {
			b.Text = change(b.Text)
		}
	}
	answer.Content = slices.DeleteFunc(answer.Content, func(b anthropic.ContentBlockParamUnion) bool {
		return b.OfText != nil && b.OfText.Text == ""
	})
	for i := range answer.Content {
		if b := answer.Content[i].OfText; b != nil {
			texts = append(texts, b.Text)
		}
	}
	return answer, texts
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

// liveSpend holds the gateways a live test made, whose calls' cost the test
// logs when it ends.
type liveSpend struct{ envs []*env }

// spendLog returns a liveSpend that logs its calls' cost when t ends.
func spendLog(t *testing.T) *liveSpend {
	t.Helper()
	s := new(liveSpend)
	t.Cleanup(func() { s.log(t) })
	return s
}

// add counts e's calls in the spend, and returns e.
func (s *liveSpend) add(e *env) *env {
	s.envs = append(s.envs, e)
	return e
}

// log logs the usage and the spend the gateways recorded for the test's
// calls, summed. The spend is in pico-USD (10^-12 USD), at the price card
// connect gives the gateway whatever the model: Haiku 5.5's, for prompts
// under 100K tokens. A call whose answer was lost counts at its worst case,
// as the gateway counts it.
func (s *liveSpend) log(t *testing.T) {
	var u cost.Usage
	var spend int64
	calls, open := 0, 0
	for _, e := range s.envs {
		records := e.calls.Records()
		for i := range records {
			calls++
			_, o, _, ended := e.calls.Record(records[i].ID)
			if !ended {
				open++
				continue
			}
			u.Input += o.Usage.Input
			u.CacheWrite5m += o.Usage.CacheWrite5m
			u.CacheWrite1h += o.Usage.CacheWrite1h
			u.CacheRead += o.Usage.CacheRead
			u.Output += o.Usage.Output
			spend += o.Spend
		}
	}
	t.Logf("the test's %d calls through the gateway, %d of them without an outcome yet, used %d input tokens, besides %d written to the cache and %d read from it, and %d output tokens; they cost %d pico-USD, $%.6f, at the test's price card for every model, Haiku 5.5's for prompts under 100K tokens: $0.10 per million input tokens and $0.50 per million output tokens; a call whose answer was lost counts at its worst case",
		calls, open, u.Input, u.CacheWrite5m+u.CacheWrite1h, u.CacheRead, u.Output, spend, float64(spend)/1e12)
}

// echoTest is a run of the echo test: its test, the vendor's key, the model
// it plays, the runtime's tools, and the spend of the gateways it makes.
type echoTest struct {
	t         *testing.T
	apiKey    string
	modelName string
	tools     []anthropic.ToolUnionParam
	spend     *liveSpend
}

// firstAnswer asks for an answer that can test the echoes up to liveTurns
// times, each through a new gateway, as the live check plays its turns: the
// model thinks only when it chooses to. It returns the gateway whose session
// holds the answer's thinking block, since a gateway takes back only the
// thinking blocks it gave, and the answer's call. A request that fails
// transiently is a miss, and the next try waits transientPause; one that
// fails otherwise fails the test at once. When no try gives such an answer,
// the test fails, naming every miss.
func (x *echoTest) firstAnswer(ctx context.Context) (*env, *liveCall) {
	t := x.t
	t.Helper()
	var missed []string
	for try := 1; try <= liveTurns; try++ {
		if ctx.Err() != nil {
			missed = append(missed, fmt.Sprintf("try %d: the test's time ran out, %s before go test's timeout", try, liveMargin))
			break
		}
		e := x.spend.add(liveEnv(t, x.apiKey, x.modelName, callTimeout, liveRound))
		c := e.call(ctx, asTheRuntime(x.modelName, x.tools, lastText(echoPrompt)))
		switch {
		case c.taken():
			t.Logf("first answer, try %d of %d: %s's answer holds %s, and ended by %s, after %d attempt(s) at the vendor",
				try, liveTurns, x.modelName, kinds(c.answer), c.answer.StopReason, c.outcome.Attempts)
			why := notNudged(c.answer)
			if why == "" {
				return e, c
			}
			t.Logf("first answer, try %d of %d, cannot test the echoes: %s", try, liveTurns, why)
			missed = append(missed, fmt.Sprintf("try %d: %s", try, why))
		case c.transient():
			why := "the request failed transiently: " + c.said(x.apiKey)
			t.Logf("first answer, try %d of %d: %s", try, liveTurns, why)
			missed = append(missed, fmt.Sprintf("try %d: %s", try, why))
			if try < liveTurns {
				pauseFor(ctx, transientPause)
			}
		default:
			t.Fatalf("the first request, for an answer to send back, got none on try %d of %d: %s", try, liveTurns, c.said(x.apiKey))
		}
	}
	t.Fatalf("no first answer could test the echoes, so none was sent back: %s", strings.Join(missed, "; "))
	return nil, nil
}

// sendBack sends first's answer back through e three times, as the runtime's
// echo sends it, with the nudge after it: with its text as it came, a control
// that tells a refusal of the changed text from one of what every nudge echo
// carries; with its text emptied, which the runtime sends as its thinking
// block alone; and with its text replaced by a space. Each request is tried
// again while it fails transiently (callPatiently), and each such failure is
// logged. For each echo, it checks in the gateway's record that the vendor
// got what the echo sends back (checkSent), and logs that the vendor answered
// 200, with what it answered, or fails the test, naming the echo.
func (x *echoTest) sendBack(ctx context.Context, e *env, first *liveCall) {
	t := x.t
	t.Helper()
	prompt := anthropic.NewUserMessage(anthropic.NewTextBlock(echoPrompt))
	for _, echo := range []struct {
		name   string
		change func(string) string
	}{
		{"its text as it came, the control", func(text string) string { return text }},
		{"its text emptied, which the runtime sends as its thinking block alone", func(string) string { return "" }},
		{"its text replaced by a space", func(string) string { return " " }},
	} {
		answer, texts := sentBack(first.answer, echo.change)
		c, failed := e.callPatiently(ctx, asTheRuntime(x.modelName, x.tools, prompt, answer, lastText(runner.Nudge)))
		for i, f := range failed {
			t.Logf("the answer sent back with %s, the nudge after it, failed transiently on try %d of %d, and is sent again %s later: %s",
				echo.name, i+1, echoTries, transientPause, f.said(x.apiKey))
		}
		x.checkSent(e, c, echo.name, texts)
		if c.taken() {
			t.Logf("the answer sent back with %s, the nudge after it: %s; its answer holds %s, and ended by %s",
				echo.name, c.said(x.apiKey), kinds(c.answer), c.answer.StopReason)
		} else {
			t.Errorf("the answer sent back with %s, the nudge after it, was not taken on try %d of %d: %s",
				echo.name, len(failed)+1, echoTries, c.said(x.apiKey))
		}
	}
}

// checkSent fails the test when an echo's request reached the vendor without
// what the echo sends back, a thinking block and the texts want, as the
// gateway's record of the call holds it: its verdict would not test that
// echo.
func (x *echoTest) checkSent(e *env, c *liveCall, name string, want []string) {
	x.t.Helper()
	thinking, texts, reached, err := e.sentBlocks(c.id)
	if !reached {
		return // the vendor got nothing, as the call's verdict says
	}
	if err != nil {
		x.t.Errorf("the answer sent back with %s cannot be checked against the gateway's record: %s", name, err)
		return
	}
	if thinking == 0 || !slices.Equal(texts, want) {
		// The texts are the model's own, so the message passes through
		// withoutKey, as every text that comes from the vendor does.
		x.t.Error(withoutKey(fmt.Sprintf("the answer sent back with %s reached the vendor with %d thinking block(s) and the texts %q, not a thinking block and the texts %q, so its verdict does not test that echo",
			name, thinking, texts, want), x.apiKey))
	}
}

// TestLiveEchoesOfAThinkingAnswerWithoutText checks, against the real model
// vendor, the two echoes of a nudged answer that Plan 7d-1 left unverified.
// The runtime sends an answer without a tool call back before its nudge, as
// its echo sends every answer: without its empty text blocks, so that an
// answer whose text is empty goes back as its thinking block alone, and with
// any other text as it came, whitespace too. A prompt that asks for reasoning
// first, and for an answer in one sentence without a tool, gives an answer
// holding a thinking block and text, asked for up to three times
// (firstAnswer). Through the gateway that gave it, requests then send that
// answer back with the nudge after it, as the runtime sends it: with its text
// as it came, a control; with its text emptied; and with its text replaced by
// a space (sendBack). The test logs, for each, whether the vendor answered
// 200, and fails for each it did not, naming it. A run that cannot test the
// echoes fails. When it ends, it logs what its calls cost. It needs
// AGENTSWARM_LIVE_KEY, and plays the model AGENTSWARM_LIVE_MODEL names, by
// default the fixtures', as the live check does.
func TestLiveEchoesOfAThinkingAnswerWithoutText(t *testing.T) {
	apiKey := os.Getenv("AGENTSWARM_LIVE_KEY")
	if apiKey == "" {
		t.Skip("set AGENTSWARM_LIVE_KEY to the model vendor's key to run this against the vendor")
	}
	x := &echoTest{t: t, apiKey: apiKey, modelName: cmp.Or(os.Getenv("AGENTSWARM_LIVE_MODEL"), model), tools: liveTools(t), spend: spendLog(t)}
	ctx := liveContext(t)
	e, first := x.firstAnswer(ctx)
	x.sendBack(ctx, e, first)
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

// rateCall is a call of the rate test the vendor answered with output: its
// number, its output tokens, how long it took, how it ended, and the
// gateway's attempts at the vendor.
type rateCall struct {
	n, attempts int
	out         int64
	took        time.Duration
	stop        anthropic.StopReason
}

// rate is the call's output tokens a second.
func (r *rateCall) rate() float64 { return float64(r.out) / r.took.Seconds() }

// need is how long the call would take to write liveMaxTokens tokens at its
// rate: its own time when max_tokens cut it. For a call that ended early it
// is more than the true time, since the time to its first token is spread
// over fewer tokens.
func (r *rateCall) need() time.Duration { return time.Duration(int64(r.took) * liveMaxTokens / r.out) }

// measureRate makes rateCalls calls of modelName through e, each for an
// answer longer than liveMaxTokens, and logs each. It returns the calls the
// vendor answered with output, to be judged at their own rates, those that
// ended early included. A call that got no answer is logged, and fails the
// test when it took past the round's time limit, as its verdict would.
func (e *env) measureRate(ctx context.Context, t *testing.T, apiKey, modelName string) []rateCall {
	t.Helper()
	var judged []rateCall
	for n := 1; n <= rateCalls; n++ {
		if ctx.Err() != nil {
			t.Errorf("%s: the test's time ran out, %s before go test's timeout, before call %d of %d", modelName, liveMargin, n, rateCalls)
			break
		}
		c := e.call(ctx, &anthropic.MessageNewParams{
			Model: modelName, MaxTokens: liveMaxTokens,
			Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(ratePrompt))},
		})
		head, took := fmt.Sprintf("%s, call %d of %d", modelName, n, rateCalls), c.took.Round(time.Millisecond)
		switch {
		case !c.taken() && c.took > roundLimit:
			t.Errorf("%s got no answer in %s, past the round's time limit, %s: %s", head, took, roundLimit, c.said(apiKey))
		case !c.taken():
			t.Logf("%s got no answer in %s, so it is not judged: %s", head, took, c.said(apiKey))
		case c.answer.Usage.OutputTokens < 1:
			t.Logf("%s was answered with no output token, so it is not judged", head)
		default:
			r := rateCall{n: n, attempts: c.outcome.Attempts, out: c.answer.Usage.OutputTokens, took: c.took, stop: c.answer.StopReason}
			ended := "max_tokens cut it"
			if r.stop != anthropic.StopReasonMaxTokens {
				ended = fmt.Sprintf("it ended early, by %s, and is judged at its own rate, which can only overstate its time to %d tokens", r.stop, liveMaxTokens)
			}
			t.Logf("%s: %d output tokens in %s, %.1f a second, after %d attempt(s) at the vendor; %s", head, r.out, took, r.rate(), r.attempts, ended)
			judged = append(judged, r)
		}
	}
	return judged
}

// judgeRate judges a model by the slowest of its calls, each at its own rate,
// those that ended early included: the test fails when that call writes
// liveMaxTokens tokens past the round's time limit or past the call timeout,
// and when no call was answered with output.
func judgeRate(t *testing.T, modelName string, calls []rateCall) {
	t.Helper()
	if len(calls) == 0 {
		t.Errorf("%s: no call was answered with output, so its output rate is not judged", modelName)
		return
	}
	slowest := &calls[0]
	for i := range calls {
		if calls[i].need() > slowest.need() {
			slowest = &calls[i]
		}
	}
	need := slowest.need()
	which := fmt.Sprintf("its slowest call, call %d, after %d attempt(s) at the vendor, at %.1f output tokens a second", slowest.n, slowest.attempts, slowest.rate())
	if slowest.stop != anthropic.StopReasonMaxTokens {
		which += fmt.Sprintf(", which ended early, by %s", slowest.stop)
	}
	fits := true
	for _, bound := range []struct {
		name  string
		limit time.Duration
	}{{"the round's time limit", roundLimit}, {"the call timeout", callTimeout}} {
		if need > bound.limit {
			t.Errorf("%s writes too slowly: %s, writes %d tokens in %s, past %s, %s",
				modelName, which, liveMaxTokens, need.Round(time.Millisecond), bound.name, bound.limit)
			fits = false
		}
	}
	if fits {
		t.Logf("%s: %s, writes %d tokens in %s, within the round's time limit, %s, and the call timeout, %s",
			modelName, which, liveMaxTokens, need.Round(time.Millisecond), roundLimit, callTimeout)
	}
}

// TestLiveOutputRateFitsARound checks, against the real model vendor, that
// each model the agents use writes a turn's max_tokens, liveMaxTokens, within
// a round's time limit and within the gateway's call timeout (Plan 6b's
// constraint; Plan 7d-1, R14): an answer written slower is cut by the round's
// end. For each model of AGENTSWARM_LIVE_RATE_MODELS (rateModels), it makes
// three calls through the gateway with max_tokens 4,096 and a prompt that asks
// for the numbers from one to three thousand in words, one a line, and logs
// each call's output tokens a second (measureRate). It judges each model by
// its slowest call, at that call's own rate (judgeRate): it fails for a model
// whose slowest call writes 4,096 tokens past the round's 150 s or the call
// timeout's 10 minutes, and for a model with no call answered. When it ends,
// it logs what its calls cost. It needs AGENTSWARM_LIVE_KEY, as the live check
// does.
func TestLiveOutputRateFitsARound(t *testing.T) {
	apiKey := os.Getenv("AGENTSWARM_LIVE_KEY")
	if apiKey == "" {
		t.Skip("set AGENTSWARM_LIVE_KEY to the model vendor's key to run this against the vendor")
	}
	ctx := liveContext(t)
	spend := spendLog(t)
	for _, modelName := range rateModels(t, apiKey) {
		e := spend.add(liveEnv(t, apiKey, modelName, callTimeout, liveRound))
		judgeRate(t, modelName, e.measureRate(ctx, t, apiKey, modelName))
	}
}
