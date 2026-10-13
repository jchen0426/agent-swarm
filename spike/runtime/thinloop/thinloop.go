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
