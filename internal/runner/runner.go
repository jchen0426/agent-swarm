// Package runner plays one agent's turn as a plain loop on the Messages API
// (ADR-006, candidate D). The tool list is read once, the context is the
// turn's prompt plus its own exchanges, and budgets bound every turn. It is
// the loop of the platform's runtime, agentswarm turn (Plan 7d-1, Decision
// 13), and of the mechanism check.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/jchen0426/agent-swarm/internal/outcome"
)

// Tool is one tool an agent may call.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Toolbox gives an agent its tools. A tool that fails returns its error text
// with isError set, for the model to read; err is for failures the turn cannot
// go on after, and a gateway's wraps one of the errors below.
type Toolbox interface {
	Tools(ctx context.Context) ([]Tool, error)
	// Call returns the result's content, each text block on its own.
	Call(ctx context.Context, name string, input json.RawMessage) (blocks []string, isError bool, err error)
}

// The ways a gateway can fail a turn, which the toolbox and the client
// return wrapped (Decision 13).
var (
	ErrSessionGone = errors.New("runner: the session is gone")
	ErrUnavailable = errors.New("runner: the gateway is unavailable")
	ErrRefused     = errors.New("runner: the gateway refused the request")
	ErrVendor      = errors.New("runner: the model vendor failed")
	ErrCut         = errors.New("runner: the gateway cut the call")
)

// Nudge is the platform's message to a model that answered without a tool.
const Nudge = "You answered without calling a tool. If you are done, call end_turn; otherwise, continue."

// ByMarker returns the error a turn ends with when a gateway's reply is not
// a success, by the reply's marker (outcome.Header). A reply marked
// unavailable, one without a marker, and one marked with a value this build
// does not know read as unavailable, never as a crash (Decision 13).
func ByMarker(marker string) error {
	switch marker {
	case outcome.Refused:
		return ErrRefused
	case outcome.Vendor:
		return ErrVendor
	case outcome.Cut:
		return ErrCut
	}
	return ErrUnavailable
}

// exitCrashed is the exit code of a turn that crashed: any code outside
// outcome's table is one.
const exitCrashed = 1

// ExitCode gives a turn's exit code (Decision 13; Contract C7): the class of
// the outcome of a turn that ended, or of the gateway's failure that ended
// it, and 1 for a turn that crashed: an answer the runtime could not read, or
// any other error.
func ExitCode(res Result, err error) int {
	if err != nil {
		switch {
		case errors.Is(err, ErrRefused):
			return outcome.ExitRefused
		case errors.Is(err, ErrVendor):
			return outcome.ExitVendor
		case errors.Is(err, ErrCut), errors.Is(err, ErrSessionGone):
			return outcome.ExitInterrupted
		case errors.Is(err, ErrUnavailable):
			return outcome.ExitUnavailable
		}
		return exitCrashed
	}
	switch res.Outcome {
	case EndedByTool:
		return outcome.ExitEnded
	case EndedByModel, EndedBySteps, EndedByBudget, EndedByPromptLimit, EndedByMaxTokens:
		return outcome.ExitFinished
	case EndedByRefusal:
		return outcome.ExitRefused
	case EndedByDeadline:
		return outcome.ExitInterrupted
	}
	return exitCrashed
}

// Config bounds one agent's turn.
type Config struct {
	Model     string
	MaxTokens int64 // per model response
	MaxSteps  int   // model calls per turn
	// TokenBudget caps the turn's cost-weighted tokens (Usage.Weighted). No
	// call starts that could pass it: one whose request, written to the cache
	// in full, and a reply of MaxTokens would. Zero means no cap.
	TokenBudget int64
	// PromptLimit caps one request's input tokens. The turn ends instead of
	// sending a request estimated to pass it. Zero means no cap.
	PromptLimit int64
	// System holds the agent's skills, each sent as its own system text
	// block, in order.
	System  []string
	EndTool string // the tool that ends the turn; default "end_turn"
	// Nudge sends Nudge, once per turn, to a model that answers without
	// calling a tool, and ends the turn at its second such answer (R21).
	// Without it, as the mechanism check runs unless given --nudge, the
	// first such answer ends the turn, as in Plan 3.
	Nudge bool
	Log   io.Writer // when set, receives every model response as a JSON line
}

// Outcome says why a turn ended.
type Outcome string

// The ways a turn ends.
const (
	EndedByTool        Outcome = "end_turn"     // the agent's call of the end tool succeeded
	EndedByModel       Outcome = "no_tool"      // the model answered without calling a tool; after the nudge, with Config.Nudge
	EndedBySteps       Outcome = "max_steps"    // MaxSteps model calls were made
	EndedByDeadline    Outcome = "deadline"     // the turn's context ended: the round's time limit, or the turn was stopped
	EndedByBudget      Outcome = "token_budget" // the next call could pass the turn's tokens
	EndedByPromptLimit Outcome = "prompt_limit" // the next request would pass PromptLimit
	EndedByMaxTokens   Outcome = "max_tokens"   // a reply was cut at MaxTokens; its tool calls were not run
	EndedByRefusal     Outcome = "refusal"      // the model refused; its tool calls were not run
)

// Usage counts a turn's tokens, as the API reports them.
type Usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

// Total is every token the turn sent or received.
func (u Usage) Total() int64 { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

// Weighted counts the tokens as the vendor prices them relative to uncached
// input: cache writes at 1.25, cache reads at 0.1 and output at 5 (spec
// Section 5.6). The weights hold for requests under 100,000 input tokens.
func (u Usage) Weighted() int64 {
	return (100*u.Input + 125*u.CacheWrite + 10*u.CacheRead + 500*u.Output) / 100
}

// Result reports one turn.
type Result struct {
	Outcome Outcome `json:"outcome"`
	// Nudged says the turn sent Nudge. A turn that ended no_tool with it
	// ended at its second answer without a tool; one that ended no_tool
	// without it, at its first, as every such turn of Plan 3's did.
	Nudged bool  `json:"nudged,omitempty"`
	Steps  int   `json:"steps"` // model calls
	Calls  int   `json:"calls"` // tool calls
	Usage  Usage `json:"usage"`
}

// bytesPerToken under-estimates the bytes per token of new text, so that the
// prompt estimate errs high. worstBytesPerToken errs higher still, for the
// budget, which a call must never pass.
const (
	bytesPerToken      = 3
	worstBytesPerToken = 2
)

// turn is one turn under way: the conversation so far, and the estimated
// input tokens of its next request, as the prompt limit and the budget count
// them.
type turn struct {
	client   *anthropic.Client
	toolbox  Toolbox
	cfg      Config
	tools    []anthropic.ToolUnionParam
	messages []anthropic.MessageParam
	next     int64
	worst    int64
	res      Result
}

// Run plays one turn: it alternates model calls and tool calls until the
// agent's call of the end tool succeeds, the model stops calling tools, a
// limit or ctx ends the turn, or a failure does. A gateway's failure wraps
// one of the errors above; ExitCode gives the turn's exit code.
func Run(ctx context.Context, client *anthropic.Client, toolbox Toolbox, config *Config, prompt string) (Result, error) {
	t := &turn{client: client, toolbox: toolbox, cfg: *config}
	if t.cfg.EndTool == "" {
		t.cfg.EndTool = "end_turn"
	}
	tools, toolBytes, err := toolParams(ctx, toolbox)
	if err != nil {
		return over(ctx, t.res, err)
	}
	t.tools = tools
	t.messages = []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))}
	// The first request is not held to the prompt limit.
	t.worst = int64(textBytes(t.cfg.System)+len(prompt)+toolBytes) / worstBytesPerToken
	for {
		if out := stop(ctx, &t.cfg, &t.res, t.next, t.worst); out != "" {
			t.res.Outcome = out
			return t.res, nil
		}
		if done, err := t.step(ctx); done || err != nil {
			return over(ctx, t.res, err)
		}
	}
}

// over returns how a turn ended. A failure once the turn's context has ended
// is the end of the turn's time, not a failure.
func over(ctx context.Context, res Result, err error) (Result, error) {
	if err != nil && ctx.Err() != nil {
		res.Outcome = EndedByDeadline
		return res, nil
	}
	return res, err
}

// step makes one model call and the tool calls of its answer, and reports
// whether the turn is over, with its outcome set when no error ended it.
func (t *turn) step(ctx context.Context) (done bool, err error) {
	t.res.Steps++
	msg, err := t.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     t.cfg.Model,
		MaxTokens: t.cfg.MaxTokens,
		System:    systemBlocks(t.cfg.System),
		Messages:  withBreakpoint(t.messages),
		Tools:     t.tools,
	}, option.WithMiddleware(whole))
	if err != nil {
		return true, fmt.Errorf("model call: %w", modelFailure(err))
	}
	if err := record(&t.cfg, &t.res, msg); err != nil {
		return true, err
	}
	if out, err := read(msg); err != nil || out != "" {
		t.res.Outcome = out
		return true, err
	}
	results, ended, added, err := callTools(ctx, t.toolbox, &t.cfg, &t.res, msg)
	switch {
	case err != nil:
		return true, err
	case ended:
		t.res.Outcome = EndedByTool
		return true, nil
	case len(results) == 0 && (!t.cfg.Nudge || t.res.Nudged):
		t.res.Outcome = EndedByModel
		return true, nil
	case len(results) == 0:
		t.res.Nudged = true
		results, added = []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(Nudge)}, int64(len(Nudge))
	}
	t.answered(msg, results, added)
	return false, nil
}

// answered adds the model's answer and the turn's reply to it, the tools'
// results or the nudge, to the conversation, and estimates the next request.
// An answer with no content is left out: the Messages API takes no empty
// message, and joins the reply to the user's message before it.
func (t *turn) answered(msg *anthropic.Message, reply []anthropic.ContentBlockParamUnion, added int64) {
	if len(msg.Content) > 0 {
		t.messages = append(t.messages, msg.ToParam())
	}
	t.messages = append(t.messages, anthropic.NewUserMessage(reply...))
	sent := msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens + msg.Usage.OutputTokens
	t.next = sent + added/bytesPerToken + int64(len(reply))
	t.worst = sent + added/worstBytesPerToken + int64(len(reply))
}

// modelFailure gives a model call that failed its class (Decision 13): a
// reply of the gateway's that is not a success, by its marker; no whole
// answer at all, as when the connection was refused or dropped, unavailable.
// Any other failure, an answer that came whole but cannot be read among
// them, is returned as it is: a crash.
func modelFailure(err error) error {
	var reply *anthropic.Error
	var partial *noAnswerError
	switch {
	case errors.As(err, &reply):
		marker := ""
		if reply.Response != nil {
			marker = reply.Response.Header.Get(outcome.Header)
		}
		return fmt.Errorf("%w: %w", ByMarker(marker), err)
	case errors.As(err, &partial):
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return err
}

// noAnswerError marks a model call that got no whole answer: no response, or
// a response whose body broke off.
type noAnswerError struct{ err error }

func (e *noAnswerError) Error() string { return e.err.Error() }

func (e *noAnswerError) Unwrap() error { return e.err }

// whole is a middleware of the model client's that marks the failures of a
// call that got no whole answer, so that they are told apart from an answer
// that came whole and cannot be read.
func whole(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	res, err := next(req)
	if err != nil {
		return res, &noAnswerError{err}
	}
	res.Body = &answerBody{res.Body}
	return res, nil
}

// answerBody is an answer's body, whose read failures it marks.
type answerBody struct{ io.ReadCloser }

func (b *answerBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = &noAnswerError{err}
	}
	return n, err
}

// read returns the outcome of an answer that ends the turn, cut or refused,
// whose tool calls may be partial and are not run, or "" for one the turn
// goes on from. An answer the runtime cannot read is an error, a crash
// (Decision 13): its content not a list of whole blocks of the kinds the
// runtime knows, or its stop reason one it does not know.
func read(msg *anthropic.Message) (Outcome, error) {
	if !msg.JSON.Content.Valid() {
		return "", errors.New("the model's answer has no list of content blocks")
	}
	switch msg.StopReason {
	case anthropic.StopReasonToolUse, anthropic.StopReasonEndTurn, anthropic.StopReasonStopSequence:
	case anthropic.StopReasonMaxTokens:
		return EndedByMaxTokens, nil
	case anthropic.StopReasonRefusal:
		return EndedByRefusal, nil
	default:
		return "", fmt.Errorf("the model's answer has a stop reason the runtime does not know: %q", msg.StopReason)
	}
	for i := range msg.Content {
		if err := readBlock(&msg.Content[i]); err != nil {
			return "", fmt.Errorf("block %d of the model's answer: %w", i, err)
		}
	}
	return "", nil
}

// readBlock checks that the runtime can read a block of an answer.
func readBlock(b *anthropic.ContentBlockUnion) error {
	switch block := b.AsAny().(type) {
	case anthropic.TextBlock:
		if !block.JSON.Text.Valid() {
			return errors.New("a text block without its text")
		}
	case anthropic.ThinkingBlock, anthropic.RedactedThinkingBlock:
	case anthropic.ToolUseBlock:
		if block.ID == "" || block.Name == "" || !isObject(block.Input) {
			return errors.New("a tool call without its id, its name or an input object")
		}
	default:
		return fmt.Errorf("a block of a kind the runtime cannot read: %q", b.Type)
	}
	return nil
}

// isObject reports JSON that is an object.
func isObject(raw json.RawMessage) bool {
	var v map[string]json.RawMessage
	return json.Unmarshal(raw, &v) == nil && v != nil
}

// stop returns the outcome that ends the turn before its next model call, or
// "" to go on. next and worst estimate the next request's input tokens.
func stop(ctx context.Context, cfg *Config, res *Result, next, worst int64) Outcome {
	switch {
	case ctx.Err() != nil:
		return EndedByDeadline
	case res.Steps >= cfg.MaxSteps:
		return EndedBySteps
	case cfg.TokenBudget > 0 && res.Usage.Weighted()+worstCost(cfg, worst) > cfg.TokenBudget:
		return EndedByBudget
	case cfg.PromptLimit > 0 && next > cfg.PromptLimit:
		return EndedByPromptLimit
	}
	return ""
}

// worstCost is the most that a request of input tokens can weigh: every
// input token written to the cache, and a reply of MaxTokens.
func worstCost(cfg *Config, input int64) int64 {
	return (125*input)/100 + 5*cfg.MaxTokens
}

// record adds a response's usage to the turn and logs the response.
func record(cfg *Config, res *Result, msg *anthropic.Message) error {
	res.Usage.Input += msg.Usage.InputTokens
	res.Usage.Output += msg.Usage.OutputTokens
	res.Usage.CacheRead += msg.Usage.CacheReadInputTokens
	res.Usage.CacheWrite += msg.Usage.CacheCreationInputTokens
	if cfg.Log == nil {
		return nil
	}
	if _, err := io.WriteString(cfg.Log, msg.RawJSON()+"\n"); err != nil {
		return fmt.Errorf("log: %w", err)
	}
	return nil
}

// callTools runs the tool calls of one answer, in order, and returns their
// results, whether the end tool's call succeeded, and the bytes of the
// results. No call starts once the turn's context has ended, nor after the
// end tool's call succeeded: the turn is over.
func callTools(ctx context.Context, toolbox Toolbox, cfg *Config, res *Result, msg *anthropic.Message) (results []anthropic.ContentBlockParamUnion, ended bool, added int64, err error) {
	for i := range msg.Content {
		use, ok := msg.Content[i].AsAny().(anthropic.ToolUseBlock)
		if !ok {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, false, 0, err
		}
		res.Calls++
		blocks, isErr, err := call(ctx, toolbox, cfg, &use)
		if err != nil {
			return nil, false, 0, fmt.Errorf("tool %s: %w", use.Name, err)
		}
		result, size := toolResult(use.ID, blocks, isErr)
		results = append(results, result)
		added += size
		if use.Name == cfg.EndTool && !isErr {
			return results, true, added, nil
		}
	}
	return results, false, added, nil
}

// call runs one tool call. The end tool's call that its gateway could not
// serve is tried once more (Decision 13): the turn ends only when that call
// succeeds.
func call(ctx context.Context, toolbox Toolbox, cfg *Config, use *anthropic.ToolUseBlock) (blocks []string, isError bool, err error) {
	blocks, isError, err = toolbox.Call(ctx, use.Name, use.Input)
	if use.Name == cfg.EndTool && errors.Is(err, ErrUnavailable) && ctx.Err() == nil {
		blocks, isError, err = toolbox.Call(ctx, use.Name, use.Input)
	}
	return blocks, isError, err
}

// toolResult is a tool's result for the model, each text block of its own,
// and its bytes. The Messages API takes no empty text block, so an empty one
// is left out, and a result with no text has no content.
func toolResult(id string, blocks []string, isError bool) (block anthropic.ContentBlockParamUnion, size int64) {
	result := anthropic.ToolResultBlockParam{ToolUseID: id, IsError: anthropic.Bool(isError)}
	for _, b := range blocks {
		if b == "" {
			continue
		}
		result.Content = append(result.Content, anthropic.ToolResultBlockParamContentUnion{OfText: &anthropic.TextBlockParam{Text: b}})
		size += int64(len(b))
	}
	return anthropic.ContentBlockParamUnion{OfToolResult: &result}, size
}

// systemBlocks are the agent's skills, each its own text block. The last
// carries the cache's breakpoint, which caches the tools and every skill.
func systemBlocks(skills []string) []anthropic.TextBlockParam {
	var blocks []anthropic.TextBlockParam
	for _, s := range skills {
		blocks = append(blocks, anthropic.TextBlockParam{Text: s})
	}
	if n := len(blocks); n > 0 {
		blocks[n-1].CacheControl = anthropic.NewCacheControlEphemeralParam()
	}
	return blocks
}

// textBytes counts the bytes of texts.
func textBytes(texts []string) int {
	n := 0
	for _, s := range texts {
		n += len(s)
	}
	return n
}

// withBreakpoint returns the messages with a cache breakpoint on the last
// block only, so that each request reads the cache its predecessor wrote.
func withBreakpoint(messages []anthropic.MessageParam) []anthropic.MessageParam {
	out := append([]anthropic.MessageParam(nil), messages...)
	last := &out[len(out)-1]
	content := append([]anthropic.ContentBlockParamUnion(nil), last.Content...)
	block := &content[len(content)-1]
	switch {
	case block.OfText != nil:
		b := *block.OfText
		b.CacheControl = anthropic.NewCacheControlEphemeralParam()
		block.OfText = &b
	case block.OfToolResult != nil:
		b := *block.OfToolResult
		b.CacheControl = anthropic.NewCacheControlEphemeralParam()
		block.OfToolResult = &b
	}
	last.Content = content
	return out
}

// toolParams lists the toolbox's tools as Messages API tool definitions, and
// the bytes they take.
func toolParams(ctx context.Context, toolbox Toolbox) (_ []anthropic.ToolUnionParam, size int, _ error) {
	tools, err := toolbox.Tools(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("list tools: %w", err)
	}
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		var schema anthropic.ToolInputSchemaParam
		if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
			return nil, 0, fmt.Errorf("tool %s: input schema: %w", t.Name, err)
		}
		size += len(t.Name) + len(t.Description) + len(t.InputSchema)
		out = append(out, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name: t.Name, Description: anthropic.String(t.Description), InputSchema: schema,
		}})
	}
	return out, size, nil
}
