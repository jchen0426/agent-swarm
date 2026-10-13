// Package runner plays one agent's turn as a plain loop on the Messages API
// (ADR-006, candidate D). The tool list is read once, the context is the
// turn's prompt plus its own exchanges, and budgets bound every turn.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/anthropics/anthropic-sdk-go"
)

// Tool is one tool an agent may call.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Toolbox gives an agent its tools. A tool that fails returns its error text
// with isError set, for the model to read; err is for failures the turn cannot
// go on after.
type Toolbox interface {
	Tools(ctx context.Context) ([]Tool, error)
	Call(ctx context.Context, name string, input json.RawMessage) (output string, isError bool, err error)
}

// Config bounds one agent's turn.
type Config struct {
	Model     string
	MaxTokens int64 // per model response
	MaxSteps  int   // model calls per turn
	// TokenBudget caps the turn's cost-weighted tokens (Usage.Weighted). No
	// call starts once the turn has used it up, so the last call can pass it.
	// Zero means no cap.
	TokenBudget int64
	// PromptLimit caps one request's input tokens. The turn ends instead of
	// sending a request estimated to pass it. Zero means no cap.
	PromptLimit int64
	System      string    // the agent's skill
	EndTool     string    // the tool that ends the turn; default "end_turn"
	Log         io.Writer // when set, receives every model response as a JSON line
}

// Outcome says why a turn ended.
type Outcome string

// The ways a turn ends.
const (
	EndedByTool        Outcome = "end_turn"     // the agent called the end tool
	EndedByModel       Outcome = "no_tool"      // the model answered without calling a tool
	EndedBySteps       Outcome = "max_steps"    // MaxSteps model calls were made
	EndedByDeadline    Outcome = "deadline"     // the context ended: the round's time limit
	EndedByBudget      Outcome = "token_budget" // the turn used up its tokens
	EndedByPromptLimit Outcome = "prompt_limit" // the next request would pass PromptLimit
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
// Section 5.6).
func (u Usage) Weighted() int64 {
	return (100*u.Input + 125*u.CacheWrite + 10*u.CacheRead + 500*u.Output) / 100
}

// Result reports one turn.
type Result struct {
	Outcome Outcome `json:"outcome"`
	Steps   int     `json:"steps"` // model calls
	Calls   int     `json:"calls"` // tool calls
	Usage   Usage   `json:"usage"`
}

// bytesPerToken under-estimates the bytes per token of tool output, so that
// the prompt estimate errs high.
const bytesPerToken = 3

// Run plays one turn: it alternates model calls and tool calls until the agent
// calls the end tool, the model stops calling tools, or a budget or ctx ends
// the turn.
func Run(ctx context.Context, client *anthropic.Client, toolbox Toolbox, config *Config, prompt string) (Result, error) {
	cfg := *config
	if cfg.EndTool == "" {
		cfg.EndTool = "end_turn"
	}
	tools, err := toolParams(ctx, toolbox)
	if err != nil {
		return Result{}, err
	}
	var res Result
	messages := []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))}
	var next int64 // the estimated input tokens of the next request
	for {
		if out := stop(ctx, &cfg, &res, next); out != "" {
			res.Outcome = out
			return res, nil
		}
		res.Steps++
		msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     cfg.Model,
			MaxTokens: cfg.MaxTokens,
			System:    []anthropic.TextBlockParam{{Text: cfg.System, CacheControl: anthropic.NewCacheControlEphemeralParam()}},
			Messages:  withBreakpoint(messages),
			Tools:     tools,
		})
		if err != nil {
			if ctx.Err() != nil {
				res.Outcome = EndedByDeadline
				return res, nil
			}
			return res, fmt.Errorf("model call: %w", err)
		}
		if err := record(&cfg, &res, msg); err != nil {
			return res, err
		}
		messages = append(messages, msg.ToParam())
		results, ended, added, err := callTools(ctx, toolbox, &cfg, &res, msg)
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			res.Outcome = EndedByDeadline
			return res, nil
		case err != nil:
			return res, err
		case ended:
			res.Outcome = EndedByTool
			return res, nil
		case len(results) == 0:
			res.Outcome = EndedByModel
			return res, nil
		}
		messages = append(messages, anthropic.NewUserMessage(results...))
		next = msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens + msg.Usage.OutputTokens + added
	}
}

// stop returns the outcome that ends the turn before its next model call, or
// "" to go on.
func stop(ctx context.Context, cfg *Config, res *Result, next int64) Outcome {
	switch {
	case ctx.Err() != nil:
		return EndedByDeadline
	case res.Steps >= cfg.MaxSteps:
		return EndedBySteps
	case cfg.TokenBudget > 0 && res.Usage.Weighted() >= cfg.TokenBudget:
		return EndedByBudget
	case cfg.PromptLimit > 0 && next > cfg.PromptLimit:
		return EndedByPromptLimit
	}
	return ""
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

// callTools runs the tool calls of one response, and returns their results,
// whether the end tool was called, and the estimated tokens of the results.
func callTools(ctx context.Context, toolbox Toolbox, cfg *Config, res *Result, msg *anthropic.Message) (results []anthropic.ContentBlockParamUnion, ended bool, added int64, err error) {
	for i := range msg.Content {
		use, ok := msg.Content[i].AsAny().(anthropic.ToolUseBlock)
		if !ok {
			continue
		}
		res.Calls++
		out, isErr, callErr := toolbox.Call(ctx, use.Name, use.Input)
		if callErr != nil {
			return nil, false, 0, fmt.Errorf("tool %s: %w", use.Name, callErr)
		}
		if err := ctx.Err(); err != nil {
			return nil, false, 0, err
		}
		results = append(results, anthropic.NewToolResultBlock(use.ID, out, isErr))
		added += int64(len(out))/bytesPerToken + 1
		ended = ended || use.Name == cfg.EndTool
	}
	return results, ended, added, nil
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

// toolParams lists the toolbox's tools as Messages API tool definitions.
func toolParams(ctx context.Context, toolbox Toolbox) ([]anthropic.ToolUnionParam, error) {
	tools, err := toolbox.Tools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		var schema anthropic.ToolInputSchemaParam
		if err := json.Unmarshal(t.InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("tool %s: input schema: %w", t.Name, err)
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name: t.Name, Description: anthropic.String(t.Description), InputSchema: schema,
		}})
	}
	return out, nil
}
