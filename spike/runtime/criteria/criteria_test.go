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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
}

var candidates = []candidate{
	{
		name:  "thinloop",
		image: goImage,
		cmd: func(prompt string, limit time.Duration) []string {
			return []string{"thinloop", "-system-file", "/in/system.md", "-prompt-file", "/in/" + prompt, "-round", limit.String()}
		},
	},
}

// contentCanaries are planted in host and workspace files, settings, git and
// the environment. None of them may reach a model.
var contentCanaries = []string{
	"CANARY-PROJECT-MEMORY", "CANARY-USER-MEMORY", "CANARY-SETTINGS-VALUE", "CANARY-GIT-COMMIT",
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
	net      *testcontainers.DockerNetwork
	out      string // gateway logs
	marks    string // files that canary hooks and servers create when they run
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
	nw, err := network.New(ctx, network.WithInternal())
	require.NoError(t, err)
	t.Cleanup(func() { _ = nw.Remove(ctx) })
	s := &sandbox{t: t, ctx: ctx, net: nw, out: t.TempDir(), marks: t.TempDir()}
	s.scenario, err = filepath.Abs("../sandbox/scenario")
	require.NoError(t, err)
	s.work, s.home = copyCanary(t)
	return s
}

// copyCanary copies the canary tree and makes its working directory a git
// repository whose only commit message is a canary.
func copyCanary(t *testing.T) (work, home string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.CopyFS(root, os.DirFS("../sandbox/canary")))
	work, home = filepath.Join(root, "work"), filepath.Join(root, "home")
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.name=x", "-c", "user.email=x@x", "commit", "-q", "-m", "CANARY-GIT-COMMIT"},
	} {
		git := exec.Command("git", args...)
		git.Dir = work
		out, err := git.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	return work, home
}

// gateway starts a gateway stand-in on the sandbox network under alias.
func (s *sandbox) gateway(alias string, cmd []string, extra ...testcontainers.ContainerCustomizer) *testcontainers.DockerContainer {
	s.t.Helper()
	opts := append([]testcontainers.ContainerCustomizer{
		testcontainers.WithCmd(cmd...),
		network.WithNetwork([]string{alias}, s.net),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, s.out+":/out", s.scenario+":/scenario:ro")
		}),
		testcontainers.WithWaitStrategy(wait.ForLog("listening on")),
	}, extra...)
	ctr, err := testcontainers.Run(s.ctx, goImage, opts...)
	require.NoError(s.t, err)
	s.t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	return ctr
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

func (s *sandbox) runContainer(image string, cmd []string, env map[string]string, limit time.Duration) run {
	s.t.Helper()
	start := time.Now()
	ctr, err := testcontainers.Run(s.ctx, image,
		testcontainers.WithCmd(cmd...),
		testcontainers.WithEnv(env),
		network.WithNetwork(nil, s.net),
		testcontainers.WithConfigModifier(func(cfg *container.Config) {
			cfg.Hostname = "canary-host"
			cfg.WorkingDir = workdir
		}),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, s.work+":"+workdir, s.home+":/home/agent",
				s.marks+":/canary-out", s.scenario+":/in:ro")
		}),
		testcontainers.WithWaitStrategy(wait.ForExit().WithExitTimeout(limit+time.Minute)),
	)
	require.NoError(s.t, err)
	s.t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	took := time.Since(start)
	state, err := ctr.State(s.ctx)
	require.NoError(s.t, err)
	logs, err := ctr.Logs(s.ctx)
	require.NoError(s.t, err)
	text, err := io.ReadAll(logs)
	require.NoError(s.t, err)
	s.t.Logf("%s: exit %d after %s\n%s", strings.Join(cmd, " "), state.ExitCode, took.Round(time.Millisecond), text)
	return run{exit: state.ExitCode, took: took}
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

func markers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
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

// Criterion 1: the round's time limit ends the turn.
func TestCriterion1TheTimeLimitEndsTheTurn(t *testing.T) {
	forEach(t, func(t *testing.T, c candidate) {
		s := newSandbox(t, []llmgw.Turn{use("slow_read")}, "-tools", "slow_read,end_turn", "-slow", "5m")

		r := s.runRound(c, "prompt-round1.md", 5*time.Second)

		assert.Less(t, r.took, 30*time.Second, "the turn must end near the 5 s limit")
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
		assert.Empty(t, markers(t, s.marks), "no MCP server or hook from host configuration ran")
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
		for _, call := range s.calls() {
			for _, canary := range contentCanaries {
				assert.NotContains(t, string(call.Args), canary, "tool call %s", call.Tool)
			}
		}
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
