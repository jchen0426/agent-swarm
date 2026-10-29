package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/internal/outcome"
	"github.com/jchen0426/agent-swarm/internal/runner"
	"github.com/jchen0426/agent-swarm/internal/runner/mcpclient"
	"github.com/jchen0426/agent-swarm/internal/runner/spec"
)

// turnSteps is the runtime's step limit, the mechanism check's default
// (R14).
const turnSteps = 50

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
can neither name nor change; no proxy and no redirect is followed. A gateway
whose entry names a socket is dialed there, whatever its URL's host, and the
spec's cert, when it has one, is the only certificate trusted, at TLS 1.3. The
turn lists its tools once, nudges a model that answers without calling a tool
once, ends only when its end_turn call succeeds, and stops at the spec's
deadline.

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
  15  unavailable: a gateway could not serve the turn, or gave no answer;
      or SIGTERM, which only the engine or an operator sends

Any other code is a crash, but a runner reads an end by SIGHUP, SIGINT, SIGKILL or SIGTERM it did not send as unavailable (Contract C7).`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			// SIGTERM ends the turn at once (onTerminate), and is caught only
			// while the turn runs; SIGINT cancels it, as it does any command
			// (run).
			signals, stop := make(chan os.Signal, 1), make(chan struct{})
			signal.Notify(signals, syscall.SIGTERM)
			defer func() {
				signal.Stop(signals)
				close(stop)
			}()
			go onTerminate(signals, stop, c.ErrOrStderr(), os.Exit)
			return runTurn(c.Context(), c.InOrStdin(), c.OutOrStdout())
		},
	}
}

// onTerminate writes "agentswarm: SIGTERM: the turn ends unavailable" on
// stderr and calls exit(outcome.ExitUnavailable) at the first SIGTERM on
// signals; it returns when stop closes. Only the engine or an operator sends
// SIGTERM, as when the engine stops, so the turn ends at once, printing no
// result: the toolbox may wait seconds on its way out (Plan 7d-2, R3).
func onTerminate(signals <-chan os.Signal, stop <-chan struct{}, stderr io.Writer, exit func(int)) {
	select {
	case <-signals:
		_, _ = io.WriteString(stderr, "agentswarm: SIGTERM: the turn ends unavailable\n")
		exit(outcome.ExitUnavailable)
	case <-stop:
	}
}

// runTurn plays the turn that the spec on stdin describes, and returns its
// exit code in a turnExit, or a failure when the spec cannot be taken.
func runTurn(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	s, err := spec.Read(stdin)
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
	ctx, stop := context.WithDeadline(ctx, s.Deadline)
	defer stop()
	rt := newTurnRuntime(s, time.Now())
	res, err := runner.Run(ctx, &rt.model, rt.toolbox, &runner.Config{
		Model: s.Model, MaxTokens: s.MaxTokens, MaxSteps: turnSteps, System: s.System, Nudge: true,
	}, s.Prompt)
	// The result is for a person; the exit code is the runner's, so a result
	// that cannot be written changes nothing.
	_ = json.NewEncoder(stdout).Encode(res)
	return turnExit{code: runner.ExitCode(res, err), err: err}
}

// errRedirect refuses a redirect, which would carry a session's token to
// another server.
var errRedirect = errors.New("the gateway answered with a redirect, which the runtime does not follow")

// turnRuntime is what a turn reaches its gateways with: each gateway's
// client, on a transport of its own, since each gateway has its own socket
// (R2); the model client, on the LLM gateway's; and the toolbox, on the MCP
// gateway's.
type turnRuntime struct {
	mcp, llm gatewayClient
	model    anthropic.Client
	toolbox  *mcpclient.Toolbox
}

// gatewayClient is how the runtime reaches a gateway: its transport, and a
// client on it that follows no redirect.
type gatewayClient struct {
	transport *http.Transport
	client    *http.Client
}

// newTurnRuntime builds a turn's clients from its spec alone (Decision 13):
// a transport per gateway (gatewayTransport), which trusts the spec's cert
// alone when it has one; no redirect; and a model client that reads nothing
// of the environment, retries nothing, and whose request timeout runs to the
// deadline, which also ends the turn's context, so that no answer is cut
// short of the round.
func newTurnRuntime(s *spec.Spec, now time.Time) *turnRuntime {
	var roots *x509.CertPool
	if s.Cert != "" {
		// The spec's check found a certificate in it; a pool left empty
		// would trust nothing.
		roots = x509.NewCertPool()
		roots.AppendCertsFromPEM([]byte(s.Cert))
	}
	mcp, llm := newGatewayClient(s.MCP, roots), newGatewayClient(s.LLM, roots)
	model := anthropic.NewClient(
		option.WithoutEnvironmentDefaults(),
		option.WithHTTPClient(llm.client),
		option.WithBaseURL(s.LLM.URL),
		option.WithAPIKey(string(s.LLM.Token)),
		option.WithMaxRetries(0),
		option.WithRequestTimeout(max(s.Deadline.Sub(now), time.Second)),
	)
	return &turnRuntime{
		mcp: mcp, llm: llm, model: model,
		toolbox: mcpclient.New(s.MCP.URL, string(s.MCP.Token), mcp.client),
	}
}

// newGatewayClient returns a gateway's client, on its own transport, which
// follows no redirect.
func newGatewayClient(g spec.Gateway, roots *x509.CertPool) gatewayClient {
	transport := gatewayTransport(g, roots)
	return gatewayClient{transport: transport, client: &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect },
	}}
}

// gatewayTransport is a gateway's transport: no proxy; HTTP/1.1 alone; its
// socket dialed for every connection when it has one; with roots, TLS 1.3
// at least and roots alone trusted.
//
// A proxy the environment names would see the gateway's token. HTTP/1.1 is
// the one protocol on both sides of the sandbox's hop, which the gateways'
// header timeout needs (Plan 7d-2, Decision 2). Its TLS configuration is its
// own, which offers HTTP/1.1 alone over ALPN, with roots or without: the
// clone's offers h2 too, and a server that took h2 would get HTTP/1.1. The
// socket is dialed whatever the URL's host, which no sandbox resolves; the
// URL's host is still the name the gateway's certificate is checked against.
func gatewayTransport(g spec.Gateway, roots *x509.CertPool) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.Protocols = new(http.Protocols)
	t.Protocols.SetHTTP1(true)
	t.TLSClientConfig = &tls.Config{NextProtos: []string{"http/1.1"}}
	if g.Socket != "" {
		socket := g.Socket
		var d net.Dialer
		t.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "unix", socket)
		}
	}
	if roots != nil {
		t.TLSClientConfig.RootCAs = roots
		t.TLSClientConfig.MinVersion = tls.VersionTLS13
	}
	return t
}
