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
	model := flag.String("model", "claude-haiku-5-5", "model id")
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
