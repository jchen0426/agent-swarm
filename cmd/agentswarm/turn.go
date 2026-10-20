package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/internal/runner"
	"github.com/jchen0426/agent-swarm/internal/runner/mcpclient"
)

// maxSpec caps a turn's spec (Contract C6).
const maxSpec = 1 << 20

// turnSteps is the runtime's step limit, the mechanism check's default
// (R14).
const turnSteps = 50

// turnSpec is a turn's spec (Contract C6): one JSON object, which the runner
// writes on the runtime's standard input. Nothing in it is written anywhere
// else.
type turnSpec struct {
	Turn  string      `json:"turn"`
	Run   string      `json:"run"`
	Agent string      `json:"agent"`
	Round int         `json:"round"`
	MCP   gatewaySpec `json:"mcp"`
	LLM   gatewaySpec `json:"llm"`
	Model string      `json:"model"`
	// System holds the agent's skills, each sent as its own system block.
	System []string `json:"system"`
	Prompt string   `json:"prompt"`
	// Deadline is the round's deadline, in the process's clock, plus the
	// runtime's slack.
	Deadline time.Time `json:"deadline"`
	// MaxTokens is the deployment's runner.maxTokens (R14).
	MaxTokens int64 `json:"maxTokens"`
}

// gatewaySpec is where a gateway is, and the session's token for it.
type gatewaySpec struct {
	URL   string `json:"url"`
	Token token  `json:"token"`
}

// token is a session's token. The runtime sends it only in a request's
// header, and it prints as a placeholder, so that no error or log can hold
// it.
type token string

func (token) String() string { return "[token]" }

func (token) GoString() string { return "[token]" }

// turnExit carries the exit code of a turn, its class (Decision 13), out of
// agentswarm turn, an ended turn's included; err is the failure that ended
// it, if one did.
type turnExit struct {
	code int
	err  error
}

func (e turnExit) Error() string {
	if e.err == nil {
		return fmt.Sprintf("the turn exits %d", e.code)
	}
	return e.err.Error()
}

func newTurnCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "turn",
		Short: "Play one agent's turn: the runtime that serve's runner starts",
		Long: `Play one agent's turn, as the runtime that serve's runner starts for it
(Plan 7d-1, Decision 13). It reads the turn's spec, one JSON object of at most
1 MiB, from standard input, then keeps reading it: when standard input ends,
the turn ends at once.

The model is reached through the LLM gateway and the tools through the MCP
gateway, at the spec's URLs and with its session tokens, which the environment
can neither name nor change; no proxy and no redirect is followed. The turn
lists its tools once, nudges a model that answers without calling a tool once,
ends only when its end_turn call succeeds, and stops at the spec's deadline.

It prints the turn's result as JSON and exits with the code of how the turn
ended:

  10  ended: its end_turn call succeeded
  11  finished: a second answer without a tool, after the nudge; the step
      limit (50); an answer cut at the spec's maxTokens, or at the model's
      context window
  12  refused: by a gateway, or by the model itself
  13  vendor: the model vendor failed
  14  interrupted: the deadline, a gateway's cut, the session gone, or the end
      of standard input
  15  unavailable: a gateway could not serve the turn, or gave no answer

Any other code is a crash.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runTurn(c.Context(), c.InOrStdin(), c.OutOrStdout())
		},
	}
}

// runTurn plays the turn that the spec on stdin describes, and returns its
// exit code in a turnExit, or a failure when the spec cannot be taken.
func runTurn(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	spec, err := readSpec(stdin)
	if err != nil {
		return failure{err}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// The runner holds standard input open for the turn's life: its end
		// is the runner's, or its parent's, and ends the turn (Decision 12).
		_, _ = io.Copy(io.Discard, stdin)
		cancel()
	}()
	ctx, stop := context.WithDeadline(ctx, spec.Deadline)
	defer stop()
	rt := newTurnRuntime(spec, time.Now())
	res, err := runner.Run(ctx, &rt.model, rt.toolbox, &runner.Config{
		Model: spec.Model, MaxTokens: spec.MaxTokens, MaxSteps: turnSteps, System: spec.System, Nudge: true,
	}, spec.Prompt)
	// The result is for a person; the exit code is the runner's, so a result
	// that cannot be written changes nothing.
	_ = json.NewEncoder(stdout).Encode(res)
	return turnExit{code: runner.ExitCode(res, err), err: err}
}

// readSpec reads a turn's spec: one JSON object of at most maxSpec bytes,
// after which it reads no more of r. A field it does not know is left alone,
// and one it needs must be there.
func readSpec(r io.Reader) (*turnSpec, error) {
	limited := &io.LimitedReader{R: r, N: maxSpec + 1}
	dec := json.NewDecoder(limited)
	var spec turnSpec
	err := dec.Decode(&spec)
	switch {
	case err != nil && limited.N == 0, err == nil && dec.InputOffset() > maxSpec:
		return nil, errors.New("the turn's spec is larger than 1 MiB")
	case err != nil:
		return nil, fmt.Errorf("read the turn's spec: %w", err)
	}
	return &spec, spec.check()
}

// check refuses a spec without a field the runtime needs.
func (s *turnSpec) check() error {
	for _, g := range []struct {
		name string
		spec gatewaySpec
	}{{"the MCP gateway", s.MCP}, {"the LLM gateway", s.LLM}} {
		if u, err := url.Parse(g.spec.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("the turn's spec has no URL for %s", g.name)
		}
		if g.spec.Token == "" {
			return fmt.Errorf("the turn's spec has no token for %s", g.name)
		}
	}
	switch {
	case s.Model == "":
		return errors.New("the turn's spec names no model")
	case s.Prompt == "":
		return errors.New("the turn's spec has no prompt")
	case s.Deadline.IsZero():
		return errors.New("the turn's spec has no deadline")
	case s.MaxTokens < 1:
		return errors.New("the turn's spec has no maxTokens")
	}
	return nil
}

// errRedirect refuses a redirect, which would carry a session's token to
// another server.
var errRedirect = errors.New("the gateway answered with a redirect, which the runtime does not follow")

// turnRuntime is what a turn reaches its gateways with: one transport for
// both, the model client and the toolbox.
type turnRuntime struct {
	transport *http.Transport
	client    *http.Client
	model     anthropic.Client
	toolbox   *mcpclient.Toolbox
}

// newTurnRuntime builds a turn's clients from its spec alone (Decision 13):
// no proxy the environment names, no redirect, and a model client that reads
// nothing of the environment, retries nothing, and whose request timeout
// runs to the deadline, which also ends the turn's context, so that no answer
// is cut short of the round.
func newTurnRuntime(spec *turnSpec, now time.Time) *turnRuntime {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect },
	}
	model := anthropic.NewClient(
		option.WithoutEnvironmentDefaults(),
		option.WithHTTPClient(client),
		option.WithBaseURL(spec.LLM.URL),
		option.WithAPIKey(string(spec.LLM.Token)),
		option.WithMaxRetries(0),
		option.WithRequestTimeout(max(spec.Deadline.Sub(now), time.Second)),
	)
	return &turnRuntime{
		transport: transport, client: client, model: model,
		toolbox: mcpclient.New(spec.MCP.URL, string(spec.MCP.Token), client),
	}
}
