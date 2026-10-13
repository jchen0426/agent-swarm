//go:build spike

// Package criteria runs each candidate runtime of ADR-006 in a sandbox that
// can reach only the two gateway stand-ins, and checks it against the seven
// criteria. Run it from the module root with:
//
//	go test -tags spike -count=1 -v ./criteria
package criteria_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/jchen0426/agent-swarm/spike/runtime/llmgw"
	"github.com/jchen0426/agent-swarm/spike/runtime/mcpgw"
)

const goImage = "agentswarm-spike-go:latest"

// images maps each image the suite builds to its Dockerfile.
var images = map[string]string{goImage: "sandbox/go.Dockerfile"}

// workdir is where a runtime starts. Its name is a canary too, so that a
// runtime that tells the model its working directory is caught.
const workdir = "/canary-workdir"

func TestMain(m *testing.M) {
	for tag, dockerfile := range images {
		if err := build(tag, dockerfile); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func build(tag, dockerfile string) error {
	cmd := exec.Command("docker", "build", "-q", "-t", tag, "-f", dockerfile, ".")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker build %s: %w\n%s", tag, err, out)
	}
	return nil
}

// candidate is one runtime under test.
type candidate struct {
	name   string
	image  string
	prefix string // how the runtime names gateway tools in its model requests
	cmd    func(prompt string, limit time.Duration) []string
	env    map[string]string
	// exact marks a runtime that claims its requests hold only what the
	// platform gave it: the skill, the prompt and the turn's own exchanges.
	exact bool
}

var candidates = []candidate{
	{
		name:  "thinloop",
		image: goImage,
		exact: true,
		cmd: func(prompt string, limit time.Duration) []string {
			return []string{"thinloop", "-system-file", "/in/system.md", "-prompt-file", "/in/" + prompt, "-round", limit.String()}
		},
	},
}

// contentCanaries are planted in host and workspace files, settings, git and
// the environment. None of them may reach a model.
var contentCanaries = []string{
	"CANARY-PROJECT-MEMORY", "CANARY-USER-MEMORY", "CANARY-AUTO-MEMORY", "CANARY-SETTINGS-VALUE",
	"CANARY-FILE-NAME", "CANARY-FILE-CONTENT", "CANARY-ENV-VALUE",
}

// metadataCanaries name the sandbox's working directory and host. A runtime
// that reports them tells the model about its environment; that is recorded
// as a finding, not a failure.
var metadataCanaries = []string{workdir, "canary-host"}

func forEach(t *testing.T, test func(t *testing.T, c candidate)) {
	t.Helper()
	for _, c := range candidates {
		t.Run(c.name, func(t *testing.T) { test(t, c) })
	}
}

func use(name string) llmgw.Turn { return llmgw.Turn{ToolUse: &llmgw.ToolUse{Name: name}} }

// sandbox is one internal network holding the two gateway stand-ins.
type sandbox struct {
	t        *testing.T
	ctx      context.Context
	net      string // the sandbox network's name
	out      string // gateway logs
	work     string // the canary working directory
	home     string // the canary home directory
	scenario string
	mcp      *testcontainers.DockerContainer
}

func newSandbox(t *testing.T, script []llmgw.Turn, mcpArgs ...string) *sandbox {
	t.Helper()
	s := emptySandbox(t)
	raw, err := json.Marshal(script)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(s.out, "script.json"), raw, 0o600))
	s.gateway("llmgw", []string{"llmgw", "-script", "/out/script.json", "-log", "/out/llm.jsonl"})
	s.startMCP(mcpArgs...)
	return s
}

// emptySandbox makes the internal network and the canary directories, with no gateways yet.
func emptySandbox(t *testing.T) *sandbox {
	t.Helper()
	ctx := context.Background()
	s := &sandbox{t: t, ctx: ctx, net: newIsolatedNetwork(t), out: t.TempDir()}
	var err error
	s.scenario, err = filepath.Abs("../sandbox/scenario")
	require.NoError(t, err)
	s.work, s.home = copyCanary(t)
	return s
}

// newIsolatedNetwork creates the sandbox network: internal, and with no address
// for the bridge on the host, so it has no gateway. An internal network alone
// still lets containers reach the host through the gateway address.
// testcontainers v0.44 drops network driver options, so the Docker CLI creates it.
func newIsolatedNetwork(t *testing.T) string {
	t.Helper()
	name := "agentswarm-spike-" + strings.ToLower(rand.Text())
	out, err := exec.Command("docker", "network", "create", "--internal",
		"-o", "com.docker.network.bridge.gateway_mode_ipv4=isolated",
		"--label", "agentswarm-spike=sandbox", name).CombinedOutput()
	require.NoError(t, err, string(out))
	t.Cleanup(func() {
		if out, err := exec.Command("docker", "network", "rm", name).CombinedOutput(); err != nil {
			t.Logf("remove network %s: %v: %s", name, err, out)
		}
	})
	return name
}

// copyCanary copies the canary tree for one sandbox.
func copyCanary(t *testing.T) (work, home string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.CopyFS(root, os.DirFS("../sandbox/canary")))
	return filepath.Join(root, "work"), filepath.Join(root, "home")
}

// gateway starts a gateway stand-in on the sandbox network under alias.
func (s *sandbox) gateway(alias string, cmd []string, extra ...testcontainers.ContainerCustomizer) *testcontainers.DockerContainer {
	s.t.Helper()
	opts := append([]testcontainers.ContainerCustomizer{
		testcontainers.WithCmd(cmd...),
		network.WithNetworkName([]string{alias}, s.net),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, s.out+":/out", s.scenario+":/scenario:ro")
		}),
		testcontainers.WithWaitStrategy(wait.ForLog("listening on")),
	}, extra...)
	ctr, err := start(s.t, goImage, opts...)
	require.NoError(s.t, err)
	return ctr
}

// start runs a container and schedules its removal before looking at the error:
// testcontainers returns the container even when its start or wait fails.
func start(t *testing.T, image string, opts ...testcontainers.ContainerCustomizer) (*testcontainers.DockerContainer, error) {
	t.Helper()
	ctr, err := testcontainers.Run(context.Background(), image, opts...)
	testcontainers.CleanupContainer(t, ctr)
	return ctr, err
}

// startMCP (re)starts the MCP gateway stand-in; each restart begins a round.
func (s *sandbox) startMCP(args ...string) {
	s.t.Helper()
	if s.mcp != nil {
		require.NoError(s.t, testcontainers.TerminateContainer(s.mcp))
	}
	s.mcp = s.gateway("mcpgw", append([]string{"mcpgw", "-outputs", "/scenario/outputs.json", "-log", "/out/mcp.jsonl"}, args...))
}

type run struct {
	exit int
	took time.Duration
	logs string
}

// runRound plays one turn of candidate c in the sandbox and waits for it to exit.
func (s *sandbox) runRound(c candidate, prompt string, limit time.Duration) run {
	s.t.Helper()
	env := map[string]string{"AGENT_SESSION_TOKEN": llmgw.DummyKey, "HOME": "/home/agent", "CANARY_ENV": "CANARY-ENV-VALUE"}
	for k, v := range c.env {
		env[k] = v
	}
	return s.runContainer(c.image, c.cmd(prompt, limit), env, limit)
}

func (s *sandbox) runContainer(image string, cmd []string, env map[string]string, limit time.Duration,
	extra ...testcontainers.ContainerCustomizer,
) run {
	s.t.Helper()
	began := time.Now()
	opts := append([]testcontainers.ContainerCustomizer{
		testcontainers.WithCmd(cmd...),
		testcontainers.WithEnv(env),
		network.WithNetworkName(nil, s.net),
		testcontainers.WithConfigModifier(func(cfg *container.Config) {
			cfg.Hostname = "canary-host"
			cfg.WorkingDir = workdir
		}),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, s.work+":"+workdir, s.home+":/home/agent", s.scenario+":/in:ro")
		}),
		testcontainers.WithWaitStrategy(wait.ForExit().WithExitTimeout(limit + time.Minute)),
	}, extra...)
	ctr, err := start(s.t, image, opts...)
	require.NoError(s.t, err)
	took := time.Since(began)
	state, err := ctr.State(s.ctx)
	require.NoError(s.t, err)
	logs, err := ctr.Logs(s.ctx)
	require.NoError(s.t, err)
	text, err := io.ReadAll(logs)
	require.NoError(s.t, err)
	s.t.Logf("%s: exit %d after %s\n%s", strings.Join(cmd, " "), state.ExitCode, took.Round(time.Millisecond), text)
	return run{exit: state.ExitCode, took: took, logs: string(text)}
}

func (s *sandbox) records() []llmgw.Record {
	return readJSONL[llmgw.Record](s.t, filepath.Join(s.out, "llm.jsonl"))
}

func (s *sandbox) calls() []mcpgw.Call {
	return readJSONL[mcpgw.Call](s.t, filepath.Join(s.out, "mcp.jsonl"))
}

func readJSONL[T any](t *testing.T, path string) []T {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var out []T
	for line := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var v T
		require.NoError(t, json.Unmarshal(line, &v))
		out = append(out, v)
	}
	return out
}

func agentLoop(records []llmgw.Record) []llmgw.Record {
	var out []llmgw.Record
	for _, rec := range records {
		if rec.AgentLoop {
			out = append(out, rec)
		}
	}
	return out
}

func toolsOf(t *testing.T, body json.RawMessage) []string {
	t.Helper()
	var req struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(body, &req))
	names := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func named(c candidate, tools ...string) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, c.prefix+tool)
	}
	slices.Sort(out)
	return out
}

func toolNames(calls []mcpgw.Call) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Tool)
	}
	return out
}

// markers lists the canaries that host configuration set off. Canary hooks and
// servers report to the LLM stand-in at /canary/<name>, so the runtime under
// test can neither fake nor erase them.
func (s *sandbox) markers() []string {
	var names []string
	for _, rec := range s.records() {
		if name, ok := strings.CutPrefix(rec.Path, "/canary/"); ok && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// known lists the texts the platform puts into requests: the skill, the
// prompts, the canned tool outputs and the stand-ins' fixed replies.
func (s *sandbox) known(t *testing.T) []string {
	t.Helper()
	var texts []string
	for _, name := range []string{"system.md", "prompt-round1.md", "prompt-round2.md"} {
		raw, err := os.ReadFile(filepath.Join(s.scenario, name))
		require.NoError(t, err)
		texts = append(texts, strings.TrimSpace(string(raw)))
	}
	raw, err := os.ReadFile(filepath.Join(s.scenario, "outputs.json"))
	require.NoError(t, err)
	var outputs map[string]string
	require.NoError(t, json.Unmarshal(raw, &outputs))
	for _, out := range outputs {
		texts = append(texts, out)
	}
	return append(texts, "recorded", "no data", "script exhausted")
}

// unexplained returns the text in a request that no known text accounts for:
// whatever the runtime added on its own.
func unexplained(t *testing.T, body json.RawMessage, known []string) []string {
	t.Helper()
	var req struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(body, &req))
	texts := textsOf(req.System)
	for _, m := range req.Messages {
		texts = append(texts, textsOf(m.Content)...)
	}
	var out []string
	for _, text := range texts {
		for _, k := range known {
			text = strings.ReplaceAll(text, k, "")
		}
		if text = strings.TrimSpace(text); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// textsOf collects the text of a system field or of a message's content: a
// plain string, or text blocks and tool results. Tool calls and thinking are
// the model's own output.
func textsOf(raw json.RawMessage) []string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return []string{str}
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			out = append(out, b.Text)
		case "tool_result":
			out = append(out, textsOf(b.Content)...)
		}
	}
	return out
}

// The sandbox reaches the two gateways and nothing else (criterion 4): no
// outside host by name, by IPv4 or by IPv6, and no outside name resolves, so
// DNS cannot carry data out either.
func TestOnlyTheGatewaysAreReachable(t *testing.T) {
	s := newSandbox(t, nil, "-tools", "end_turn")

	r := s.runContainer(goImage, []string{"sh", "-c",
		"wget -q -T 5 -O- http://llmgw:8080/healthz && wget -q -T 5 -O- http://mcpgw:8090/healthz" +
			" && ! wget -q -T 5 -O- https://example.com && ! wget -q -T 5 -O- http://1.1.1.1" +
			" && ! wget -q -T 5 -O- 'http://[2606:4700:4700::1111]' && ! nslookup example.com"},
		nil, time.Minute)

	assert.Equal(t, 0, r.exit)
}

// The sandbox cannot reach the host (spec Section 9.3: egress only to the two
// gateways): not through its network's gateway address, the default bridge's
// gateway, the names Docker gives the host, or Docker's host-gateway mapping.
// A listener in this test process stands for any service on the host. Being
// refused counts as reaching the host: the packet got there.
func TestTheSandboxCannotReachTheHost(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	srv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "host-listener") }),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	tcp, ok := ln.Addr().(*net.TCPAddr)
	require.True(t, ok)
	s := newSandbox(t, nil, "-tools", "end_turn")
	targets := append([]string{"host.docker.internal", "gateway.docker.internal", "host-gateway-probe"},
		gateways(t, s.net, "bridge")...)

	probe := fmt.Sprintf(`for addr in %s; do
  out=$(wget -q -T 3 -O- "http://$addr:%d/" 2>&1)
  case "$out" in *host-listener*|*"Connection refused"*) echo "REACHABLE $addr: $out";; esac
done`, strings.Join(targets, " "), tcp.Port)
	r := s.runContainer(goImage, []string{"sh", "-c", probe}, nil, time.Minute,
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.ExtraHosts = append(hc.ExtraHosts, "host-gateway-probe:host-gateway")
		}))

	assert.Equal(t, 0, r.exit)
	assert.NotContains(t, r.logs, "REACHABLE", "probed %v", targets)
}

// gateways returns the gateway addresses of the named Docker networks.
func gateways(t *testing.T, networks ...string) []string {
	t.Helper()
	var out []string
	for _, name := range networks {
		raw, err := exec.Command("docker", "network", "inspect", name,
			"--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}").Output()
		require.NoError(t, err)
		out = append(out, strings.Fields(string(raw))...)
	}
	return out
}

// Criterion 1: a turn ends when the agent calls end_turn. Criterion 4: the
// runtime does its work with egress denied.
func TestCriterion1EndTurnEndsTheTurn(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		s := newSandbox(t, []llmgw.Turn{use("get_alert"), use("post_hypothesis"), use("end_turn"), use("get_alert")},
			"-tools", "get_alert,post_hypothesis,end_turn")

		r := s.runRound(c, "prompt-round1.md", time.Minute)

		assert.Equal(t, 0, r.exit, "criterion 4: the turn completes with egress denied")
		assert.Equal(t, []string{"get_alert", "post_hypothesis", "end_turn"}, toolNames(s.calls()))
		assert.Len(t, agentLoop(s.records()), 3, "criterion 1: no model call after end_turn")
	})
}

// Criterion 1: the round's time limit ends the turn. Each read takes 2 s and
// the script never runs out, so a runtime that ignores the limit keeps calling.
// Counted from the turn's first model request, every model request and tool
// call must start within the limit plus a second of grace, and the runtime must
// still be working when the limit passes.
func TestCriterion1TheTimeLimitEndsTheTurn(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		script := make([]llmgw.Turn, 30)
		for i := range script {
			script[i] = use("slow_read")
		}
		s := newSandbox(t, script, "-tools", "slow_read,end_turn", "-slow", "2s")
		const limit, grace = 5 * time.Second, time.Second

		r := s.runRound(c, "prompt-round1.md", limit)

		assert.Equal(t, 0, r.exit)
		loop, calls := agentLoop(s.records()), s.calls()
		require.NotEmpty(t, loop)
		require.GreaterOrEqual(t, len(calls), 2, "the runtime was still working when the limit passed")
		deadline := loop[0].Time.Add(limit + grace)
		for _, rec := range loop {
			assert.False(t, rec.Time.After(deadline), "model request %d started %s after the turn's first", rec.Seq, rec.Time.Sub(loop[0].Time))
		}
		for _, call := range calls {
			assert.False(t, call.Time.After(deadline), "tool call started %s after the turn's first request", call.Time.Sub(loop[0].Time))
		}
		t.Logf("%d model requests and %d tool calls; the last call started %s after the first request; the process ran %s",
			len(loop), len(calls), calls[len(calls)-1].Time.Sub(loop[0].Time).Round(time.Millisecond), r.took.Round(time.Millisecond))
	})
}

// Criteria 2 and 3: the gateway is the only MCP endpoint, and the model is
// offered no tool that reaches outside the sandbox.
func TestCriteria2And3OnlyGatewayTools(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		s := newSandbox(t, []llmgw.Turn{use("get_alert"), use("end_turn")}, "-tools", "get_alert,end_turn")

		s.runRound(c, "prompt-round1.md", time.Minute)

		loop := agentLoop(s.records())
		require.NotEmpty(t, loop)
		for _, rec := range loop {
			assert.Equal(t, named(c, "end_turn", "get_alert"), toolsOf(t, rec.Body), "request %d", rec.Seq)
		}
		assert.Empty(t, s.markers(), "no MCP server or hook from host configuration ran")
	})
}

// Criterion 5: no host or workspace content reaches the model.
func TestCriterion5NoHostContent(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		s := newSandbox(t, []llmgw.Turn{use("get_alert"), use("end_turn")}, "-tools", "get_alert,end_turn")

		s.runRound(c, "prompt-round1.md", time.Minute)

		records := s.records()
		require.NotEmpty(t, records)
		for _, rec := range records {
			if rec.Path == "/v1/messages" {
				require.NotEmpty(t, rec.Body, "request %d has no readable body, so the search below would prove nothing", rec.Seq)
			}
			for _, canary := range contentCanaries {
				assert.NotContains(t, string(rec.Body), canary, "request %d %s", rec.Seq, rec.Path)
			}
			for _, canary := range metadataCanaries {
				if strings.Contains(string(rec.Body), canary) {
					t.Logf("finding: request %d tells the model %q", rec.Seq, canary)
				}
			}
		}
		// Scripted tool inputs come from the script, so this guards against a
		// runtime rewriting tool arguments; the live test checks the model's own.
		for _, call := range s.calls() {
			for _, canary := range contentCanaries {
				assert.NotContains(t, string(call.Args), canary, "tool call %s", call.Tool)
			}
		}
		// Beyond the canaries: every text in a model request must come from the
		// platform. Text that does not is a finding; for a runtime that claims
		// exact requests, it is a failure.
		known := s.known(t)
		var extra []string
		for _, rec := range agentLoop(records) {
			for _, text := range unexplained(t, rec.Body, known) {
				if !slices.Contains(extra, text) {
					extra = append(extra, text)
				}
			}
		}
		if c.exact {
			assert.Empty(t, extra, "text the platform did not provide")
		}
		for _, text := range extra {
			t.Logf("finding: request text the platform did not provide: %.300q", text)
		}
		var first struct {
			MaxTokens int             `json:"max_tokens"`
			Thinking  json.RawMessage `json:"thinking"`
			Metadata  json.RawMessage `json:"metadata"`
		}
		require.NoError(t, json.Unmarshal(agentLoop(records)[0].Body, &first))
		t.Logf("request settings: max_tokens %d, thinking %s, metadata %s", first.MaxTokens, first.Thinking, first.Metadata)
	})
}

// Criterion 6: the context stays bounded between rounds.
func TestCriterion6NothingCarriesOverBetweenRounds(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		s := newSandbox(t, []llmgw.Turn{use("get_alert"), use("end_turn"), use("query_logs"), use("end_turn")},
			"-tools", "get_alert,query_logs,end_turn")
		s.runRound(c, "prompt-round1.md", time.Minute)
		first := len(s.records())

		s.startMCP("-round", "2", "-tools", "get_alert,query_logs,end_turn")
		s.runRound(c, "prompt-round2.md", time.Minute)

		records := s.records()
		require.Greater(t, len(records), first)
		for _, rec := range records[first:] {
			assert.NotContains(t, string(rec.Body), "ROUND-ONE-ONLY", "request %d", rec.Seq)
			assert.NotContains(t, string(rec.Body), "Round 1.", "request %d", rec.Seq)
		}
		t.Logf("first request of each round: %d and %d bytes", len(records[0].Body), len(records[first].Body))
	})
}

// Criterion 7: tool lists change only at round boundaries.
func TestCriterion7ToolListsChangeOnlyBetweenRounds(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		// probe_extra appears when the slow first call starts, so a runtime that
		// listens for tools/list_changed has two seconds to pick it up.
		s := newSandbox(t, []llmgw.Turn{use("slow_read"), use("get_alert"), use("end_turn"), use("query_logs"), use("end_turn")},
			"-tools", "slow_read,get_alert,end_turn", "-add-after-first-call", "probe_extra", "-slow", "2s")
		s.runRound(c, "prompt-round1.md", time.Minute)
		first := len(s.records())

		s.startMCP("-round", "2", "-tools", "query_logs,end_turn")
		s.runRound(c, "prompt-round2.md", time.Minute)

		records := s.records()
		round1 := agentLoop(records[:first])
		require.Len(t, round1, 3)
		for _, rec := range round1 {
			assert.Equal(t, named(c, "end_turn", "get_alert", "slow_read"), toolsOf(t, rec.Body), "round 1 keeps its tools, request %d", rec.Seq)
		}
		round2 := agentLoop(records[first:])
		require.NotEmpty(t, round2)
		for _, rec := range round2 {
			assert.Equal(t, named(c, "end_turn", "query_logs"), toolsOf(t, rec.Body), "round 2 gets its tools, request %d", rec.Seq)
		}
	})
}

// The sandbox holds no model key: the gateway sees only the dummy.
func TestTheSandboxHoldsNoModelKey(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		s := newSandbox(t, []llmgw.Turn{use("end_turn")}, "-tools", "end_turn")

		s.runRound(c, "prompt-round1.md", time.Minute)

		records := s.records()
		require.NotEmpty(t, records)
		dummy := 0
		for _, rec := range records {
			assert.NotEqual(t, "other", rec.KeyHeader, "request %d %s carries a key that is not the dummy", rec.Seq, rec.Path)
			if rec.KeyHeader == "dummy" {
				dummy++
			}
		}
		assert.Positive(t, dummy, "model requests carry the dummy session token")
	})
}

// A container whose start fails is still removed. testcontainers returns the
// container together with the error, and with the Ryuk reaper disabled nothing
// else would remove it.
func TestAFailedStartLeavesNothingBehind(t *testing.T) {
	label := "leak-probe-" + strings.ToLower(rand.Text())
	t.Run("start fails", func(t *testing.T) {
		_, err := start(t, goImage,
			testcontainers.WithCmd("sleep", "60"),
			testcontainers.WithLabels(map[string]string{"agentswarm-spike-probe": label}),
			testcontainers.WithWaitStrategy(wait.ForLog("never printed").WithStartupTimeout(2*time.Second)))
		require.Error(t, err)
	})

	out, err := exec.Command("docker", "ps", "-aq", "--filter", "label=agentswarm-spike-probe="+label).Output()
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(string(out)), "the failed container was left behind")
}
