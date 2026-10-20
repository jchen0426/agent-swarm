package check_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/measurement/check"
	"github.com/jchen0426/agent-swarm/internal/runner"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// fakeServer is an MCP server over streamable HTTP: read-only tools with
// fixed outputs, a read-only tool that fails, and a tool that is not read-only.
type fakeServer struct {
	url     string
	wrote   atomic.Bool
	current atomic.Pointer[mcp.Server]
	outputs map[string]string
}

func newFakeServer(t *testing.T, outputs map[string]string) *fakeServer {
	t.Helper()
	return newVersionedServer(t, "1", outputs)
}

// newVersionedServer is newFakeServer with the given server version, which
// stands for the data it serves. swap serves another version at the same
// address.
func newVersionedServer(t *testing.T, version string, outputs map[string]string) *fakeServer {
	t.Helper()
	f := &fakeServer{}
	f.current.Store(f.build(version, outputs))
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return f.current.Load() }, nil))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	f.outputs = outputs
	return f
}

func (f *fakeServer) swap(version string) { f.current.Store(f.build(version, f.outputs)) }

func (f *fakeServer) build(version string, outputs map[string]string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "observability", Version: version}, &mcp.ServerOptions{Instructions: "Data ends at the alert."})
	schema := json.RawMessage(`{"type":"object","properties":{"service":{"type":"string"}}}`)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	for name, out := range outputs {
		s.AddTool(&mcp.Tool{Name: name, Description: "Read " + name + ".", InputSchema: schema, Annotations: readOnly},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out}}}, nil
			})
	}
	s.AddTool(&mcp.Tool{Name: "broken_read", Description: "Fails.", InputSchema: schema, Annotations: readOnly},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			res := &mcp.CallToolResult{}
			res.SetError(errors.New("unknown service payment"))
			return res, nil
		})
	s.AddTool(&mcp.Tool{Name: "slow_read", Description: "Answers after two seconds.", InputSchema: schema, Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "late"}}}, nil
		})
	s.AddTool(&mcp.Tool{Name: "restart_service", Description: "Restarts a service.", InputSchema: schema},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			f.wrote.Store(true)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "restarted"}}}, nil
		})
	return s
}

var defaultOutputs = map[string]string{
	"get_changes": "10:01 checkout config FLAG_X set to on",
	"query_logs":  "checkout error: FLAG_X has an invalid value",
}

var (
	checkoutConfig = key.Key{check.ServiceField: "checkout", check.FailureModeField: "config-change"}
	cartRelease    = key.Key{check.ServiceField: "cart", check.FailureModeField: "bad-release"}
)

func newCase(id, server string) check.Case {
	return check.Case{
		ID:       id,
		Task:     check.Task{Servers: []string{server}, Alert: "Errors on checkout in " + id, Services: []string{"cart", "checkout"}},
		Change:   true,
		Labels:   []key.Key{checkoutConfig},
		Baseline: cartRelease,
	}
}

func newSet(cases ...check.Case) *check.Set {
	return &check.Set{
		FailureModes: []check.FailureMode{
			{Name: "bad-release", Meaning: "A new version broke the service."},
			{Name: "config-change", Meaning: "A configuration change broke the service."},
		},
		Cases: cases,
	}
}

var twoAgents = []check.Agent{{Name: "changes", Skill: "Skill of changes."}, {Name: "runtime", Skill: "Skill of runtime."}}

func options(t *testing.T) *check.Options {
	t.Helper()
	return &check.Options{
		Trials:      1,
		Turn:        runner.Config{Model: "test-model", MaxTokens: 1024, MaxSteps: 10, PromptLimit: 90000},
		RunBudget:   1000,
		RoundLimit:  10 * time.Second,
		ReadTimeout: 5 * time.Second,
		EntryLimit:  5,
		MaxOutput:   1000,
		Concurrency: 2,
		Out:         t.TempDir(),
		Build:       "0123abc",
	}
}

// player calls one agent's tools in a scripted turn. It reports failures
// with assert, because it runs outside the test's goroutine.
type player struct {
	t      *testing.T
	ctx    context.Context
	tools  runner.Toolbox
	prompt string
}

func (p *player) call(name string, input any) (out string, isError bool) {
	raw, err := json.Marshal(input)
	if err != nil {
		p.t.Errorf("input of %s: %v", name, err)
	}
	blocks, isError, err := p.tools.Call(p.ctx, name, raw)
	if err != nil {
		p.t.Errorf("call %s: %v", name, err)
		return "", isError
	}
	// One block, as Plan 3's runner sent every result, so that a rerun of
	// Plan 3's check sends what it sent (R21).
	if assert.Len(p.t, blocks, 1, "the answer of %s", name) {
		out = blocks[0]
	}
	return out, isError
}

// read calls a read tool and returns its call id.
func (p *player) read(tool string) string {
	out, _ := p.call(tool, map[string]any{})
	first, _, _ := strings.Cut(out, "\n")
	id, _, _ := strings.Cut(strings.TrimPrefix(first, "call_id: "), " ")
	return id
}

func (p *player) evidence(callID, quote string) string {
	out, isError := p.call("post_evidence", map[string]any{"claim": "Seen: " + quote, "call_id": callID, "quote": quote})
	assert.False(p.t, isError, out)
	return strings.TrimPrefix(out, "posted evidence ")
}

func (p *player) hypothesis(k key.Key, evidence ...string) string {
	out, isError := p.call("post_hypothesis", map[string]any{
		"service": k[check.ServiceField], "failure_mode": k[check.FailureModeField], "statement": "The cause.", "evidence": evidence,
	})
	assert.False(p.t, isError, out)
	return out
}

// seen is what a scripted turn was given.
type seen struct {
	cfg    runner.Config
	prompt string
	tools  []string
}

// script is a Turn that plays each agent's scripted turn, told apart by its
// skill, and records what each turn was given.
type script struct {
	t     *testing.T
	plays map[string]func(*player)
	mu    sync.Mutex
	seen  map[string][]seen
}

func newScript(t *testing.T, plays map[string]func(*player)) *script {
	return &script{t: t, plays: plays, seen: map[string][]seen{}}
}

func (s *script) turn(ctx context.Context, tools runner.Toolbox, cfg *runner.Config, prompt string) (runner.Result, error) {
	name := regexp.MustCompile(`Skill of (\S+)\.`).FindStringSubmatch(strings.Join(cfg.System, "\n"))[1]
	list, err := tools.Tools(ctx)
	if err != nil {
		return runner.Result{}, err
	}
	var names []string
	for _, tool := range list {
		names = append(names, tool.Name)
	}
	s.mu.Lock()
	s.seen[name] = append(s.seen[name], seen{cfg: *cfg, prompt: prompt, tools: names})
	s.mu.Unlock()
	if play := s.plays[name]; play != nil {
		play(&player{t: s.t, ctx: ctx, tools: tools, prompt: prompt})
	}
	return runner.Result{Outcome: runner.EndedByTool, Steps: 1, Usage: runner.Usage{Input: 100, Output: 10}}, nil
}

func (s *script) given(agent string) []seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[agent]
}

// agreeing plays two agents that find the same cause through different reads.
var agreeing = map[string]func(*player){
	"changes": func(p *player) {
		e := p.evidence(p.read("get_changes"), "checkout config FLAG_X set to on")
		p.hypothesis(checkoutConfig, e)
	},
	"runtime": func(p *player) {
		e := p.evidence(p.read("query_logs"), "FLAG_X has an invalid value")
		p.hypothesis(checkoutConfig, e)
	},
}

func turnOf(r *check.RunReport, agent string) check.TurnReport {
	i := slices.IndexFunc(r.Turns, func(t check.TurnReport) bool { return t.Agent == agent })
	if i < 0 {
		return check.TurnReport{}
	}
	return r.Turns[i]
}

func TestAgentsThatAgreeFromTwoSourcesConvergeOnAHit(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)

	report, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, newScript(t, agreeing).turn, opts)

	require.NoError(t, err)
	require.Len(t, report.Runs, 1)
	run := report.Runs[0]
	assert.Equal(t, checkoutConfig, run.Conclusion)
	assert.True(t, run.Converged)
	assert.True(t, run.Hit)
	assert.False(t, run.BaselineHit)
	assert.Empty(t, run.Violations)
	assert.Empty(t, run.Errors)
	assert.Equal(t, check.TrialSummary{
		Trial: 1, Cases: 1, Hits: 1, ChangeCases: 1, ChangeHits: 1, WeightedTokens: 2 * 150,
	}, report.Trials[0])
	assert.Equal(t, 2, turnOf(&run, "changes").Entries)
}

func TestTheRecordsReplayToTheSameConclusion(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)

	_, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, newScript(t, agreeing).turn, opts)
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(opts.Out, "runs", "S1", "t1", "board.json"))
	require.NoError(t, err)
	var f struct {
		Settings board.Settings `json:"settings"`
		Events   []board.Event  `json:"events"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&f), "the conclude command reads this shape")
	s, err := board.Replay(f.Settings, f.Events)
	require.NoError(t, err)
	a := s.Assess()
	require.NotEmpty(t, a.Conclusion)
	assert.Equal(t, checkoutConfig, a.Hypotheses[0].Key)

	answers, err := os.ReadFile(filepath.Join(opts.Out, "answers-t1.json"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"S1": {"service": "checkout", "failureMode": "config-change"}}`, string(answers))
	assert.FileExists(t, filepath.Join(opts.Out, "runs", "S1", "t1", "calls.json"))
	assert.FileExists(t, filepath.Join(opts.Out, "report.json"))
}

func TestEvidenceMustQuoteAReadOfTheAgentsOwn(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	var outputs []string
	plays := map[string]func(*player){
		"changes": func(p *player) {
			id := p.read("get_changes")
			for _, in := range []map[string]any{
				{"claim": "Made up.", "call_id": id, "quote": "FLAG_Y was removed"},
				{"claim": "Not mine.", "call_id": "runtime-c1", "quote": "FLAG_X"},
				{"claim": "Failed.", "call_id": p.read("broken_read"), "quote": "payment"},
			} {
				out, isError := p.call("post_evidence", in)
				assert.True(t, isError, out)
				outputs = append(outputs, out)
			}
		},
		"runtime": func(p *player) { p.read("query_logs") },
	}

	report, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, newScript(t, plays).turn, options(t))

	require.NoError(t, err)
	require.Len(t, outputs, 3)
	assert.Contains(t, outputs[0], "does not appear in the output")
	assert.Contains(t, outputs[1], "runtime-c1")
	assert.Contains(t, outputs[2], "ended in an error")
	turn := turnOf(&report.Runs[0], "changes")
	assert.Len(t, turn.Refused, 3)
	assert.Equal(t, 2, turn.Reads)
	assert.Equal(t, 1, turn.FailedReads)
	assert.Equal(t, 0, turn.Entries)
	assert.Nil(t, report.Runs[0].Conclusion)
	assert.Equal(t, 1, report.Trials[0].NoConclusion)
}

func TestOnlyReadOnlyToolsAreOffered(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	var out string
	var isError bool
	sc := newScript(t, map[string]func(*player){
		"changes": func(p *player) { out, isError = p.call("restart_service", map[string]any{"service": "checkout"}) },
	})

	_, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, sc.turn, options(t))

	require.NoError(t, err)
	tools := sc.given("changes")[0].tools
	assert.ElementsMatch(t, []string{"get_changes", "query_logs", "broken_read", "slow_read", "post_evidence", "post_hypothesis", "end_turn"}, tools)
	assert.True(t, isError)
	assert.Contains(t, out, `no tool named "restart_service"`)
	assert.False(t, srv.wrote.Load(), "the call never reached the server")
}

func TestAPostRevealsNothingOfTheOtherAgents(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	var mu sync.Mutex
	var outs []string
	post := func(tool, quote string) func(*player) {
		return func(p *player) {
			out := p.hypothesis(checkoutConfig, p.evidence(p.read(tool), quote))
			mu.Lock()
			outs = append(outs, out)
			mu.Unlock()
		}
	}
	plays := map[string]func(*player){
		"changes": post("get_changes", "FLAG_X set to on"),
		"runtime": post("query_logs", "FLAG_X has an invalid value"),
	}

	_, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, newScript(t, plays).turn, options(t))

	require.NoError(t, err)
	require.Len(t, outs, 2)
	for _, out := range outs {
		assert.Regexp(t, `^posted hypothesis e[0-9a-f]{8}$`, out, "one of the two merged, and neither may learn it")
	}
	assert.NotEqual(t, outs[0], outs[1])
}

func TestTheEntryLimitRefusesExtraEntries(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)
	opts.EntryLimit = 2
	var last string
	plays := map[string]func(*player){
		"changes": func(p *player) {
			id := p.read("get_changes")
			p.evidence(id, "checkout config")
			p.evidence(id, "FLAG_X set to on")
			last, _ = p.call("post_evidence", map[string]any{"claim": "Third.", "call_id": id, "quote": "10:01"})
		},
	}

	report, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, newScript(t, plays).turn, opts)

	require.NoError(t, err)
	assert.Contains(t, last, "2 entries")
	assert.Equal(t, 2, turnOf(&report.Runs[0], "changes").Entries)
}

// A post repeated after a lost answer returns the first and is not counted
// again (spec Section 5.2).
func TestARepeatedPostIsNotCountedAgain(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)
	opts.EntryLimit = 2
	var repeat, second, atLimit string
	plays := map[string]func(*player){
		"changes": func(p *player) {
			id := p.read("get_changes")
			p.evidence(id, "checkout config")
			repeat, _ = p.call("post_evidence", map[string]any{"claim": "Seen: checkout config", "call_id": id, "quote": "checkout config"})
			second, _ = p.call("post_evidence", map[string]any{"claim": "Second.", "call_id": id, "quote": "10:01"})
			atLimit, _ = p.call("post_evidence", map[string]any{"claim": "Seen: checkout config", "call_id": id, "quote": "checkout config"})
		},
	}

	report, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, newScript(t, plays).turn, opts)

	require.NoError(t, err)
	assert.Contains(t, repeat, "posted evidence")
	assert.Contains(t, second, "posted evidence")
	assert.Contains(t, atLimit, "posted evidence", "a repeat at the limit returns the original, as the board does")
	assert.Equal(t, 2, turnOf(&report.Runs[0], "changes").Entries)
}

func TestATieIsNoConclusionAndAMiss(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	plays := map[string]func(*player){
		"changes": func(p *player) { p.hypothesis(checkoutConfig, p.evidence(p.read("get_changes"), "FLAG_X set to on")) },
		"runtime": func(p *player) { p.hypothesis(cartRelease, p.evidence(p.read("query_logs"), "checkout error")) },
	}

	report, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, newScript(t, plays).turn, options(t))

	require.NoError(t, err)
	run := report.Runs[0]
	assert.Nil(t, run.Conclusion)
	assert.False(t, run.Hit)
	assert.Equal(t, 2, run.Hypotheses)
	assert.Equal(t, 1, report.Trials[0].NoConclusion)
}

func TestAConvergedMissIsAFalseConvergence(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	plays := map[string]func(*player){
		"changes": func(p *player) { p.hypothesis(cartRelease, p.evidence(p.read("get_changes"), "FLAG_X set to on")) },
		"runtime": func(p *player) { p.hypothesis(cartRelease, p.evidence(p.read("query_logs"), "checkout error")) },
	}

	report, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, newScript(t, plays).turn, options(t))

	require.NoError(t, err)
	assert.True(t, report.Runs[0].Converged)
	assert.False(t, report.Runs[0].Hit)
	assert.Equal(t, 1, report.Trials[0].FalseConvergence)
}

func TestEachTrialRunsEveryCase(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)
	opts.Trials = 2
	other := newCase("S2", srv.url)
	other.Change = false
	other.Baseline = checkoutConfig
	plays := map[string]func(*player){
		"changes": func(p *player) {
			if strings.Contains(p.prompt, "in S1") {
				agreeing["changes"](p)
			}
		},
		"runtime": func(p *player) {
			if strings.Contains(p.prompt, "in S1") {
				agreeing["runtime"](p)
			}
		},
	}

	report, err := check.Run(context.Background(), newSet(newCase("S1", srv.url), other), twoAgents, newScript(t, plays).turn, opts)

	require.NoError(t, err)
	require.Len(t, report.Runs, 4)
	require.Len(t, report.Trials, 2)
	for i, tr := range report.Trials {
		assert.Equal(t, check.TrialSummary{
			Trial: i + 1, Cases: 2, Hits: 1, BaselineHits: 1, ChangeCases: 1, ChangeHits: 1, NoConclusion: 1, WeightedTokens: 4 * 150,
		}, tr)
	}
	assert.FileExists(t, filepath.Join(opts.Out, "answers-t2.json"))
}

func TestEachAgentGetsItsShareOfTheBudgetAndTheTask(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	sc := newScript(t, nil)

	_, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, sc.turn, options(t))

	require.NoError(t, err)
	given := sc.given("runtime")[0]
	assert.Equal(t, int64(500), given.cfg.TokenBudget, "the run's 1000 split between two agents")
	assert.Equal(t, "test-model", given.cfg.Model)
	require.Len(t, given.cfg.System, 1, "the rules and the skill in one block, as Plan 3's check sent them")
	assert.Contains(t, given.cfg.System[0], "at most 5 entries")
	assert.True(t, strings.HasSuffix(given.cfg.System[0], "Skill of runtime."))
	for _, want := range []string{"Errors on checkout in S1", "cart, checkout", "config-change: A configuration change broke the service.", "Data ends at the alert."} {
		assert.Contains(t, given.prompt, want)
	}
}

func TestAnUnreachableServerIsAnErrorNotAMiss(t *testing.T) {
	report, err := check.Run(context.Background(), newSet(newCase("S1", "http://127.0.0.1:1/mcp")), twoAgents, newScript(t, nil).turn, options(t))

	require.NoError(t, err)
	assert.Equal(t, 1, report.Errors)
	assert.Equal(t, 1, report.Trials[0].Errors)
	assert.NotEmpty(t, report.Runs[0].Errors)
	assert.NotEmpty(t, turnOf(&report.Runs[0], "changes").Error)
}

func TestARerunKeepsFinishedRunsAndRedoesFailedOnes(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)
	set := newSet(newCase("S1", srv.url), newCase("S2", srv.url))
	var failS2 atomic.Bool
	failS2.Store(true)
	var turns atomic.Int32
	sc := newScript(t, agreeing)
	turn := func(ctx context.Context, tools runner.Toolbox, cfg *runner.Config, prompt string) (runner.Result, error) {
		turns.Add(1)
		if failS2.Load() && strings.Contains(prompt, "in S2") {
			return runner.Result{}, errors.New("model call: 529 overloaded")
		}
		return sc.turn(ctx, tools, cfg, prompt)
	}

	first, err := check.Run(context.Background(), set, twoAgents, turn, opts)
	require.NoError(t, err)
	require.Equal(t, 1, first.Errors)

	failS2.Store(false)
	turns.Store(0)
	second, err := check.Run(context.Background(), set, twoAgents, turn, opts)

	require.NoError(t, err)
	assert.Equal(t, int32(2), turns.Load(), "only S2's two agents ran again")
	assert.Equal(t, 0, second.Errors)
	assert.Equal(t, 2, second.Trials[0].Hits)
	backups, err := filepath.Glob(filepath.Join(opts.Out, "runs", "S2", "t1.failed-*"))
	require.NoError(t, err)
	assert.Len(t, backups, 1, "the failed run is kept aside, not deleted")
}

func TestARerunRefusesOtherInputs(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)
	set := newSet(newCase("S1", srv.url))
	_, err := check.Run(context.Background(), set, twoAgents, newScript(t, nil).turn, opts)
	require.NoError(t, err)

	opts.Trials = 2
	_, err = check.Run(context.Background(), set, twoAgents, newScript(t, nil).turn, opts)

	require.ErrorContains(t, err, "other inputs")
}

func TestACanceledCheckRecordsNoRun(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc := newScript(t, map[string]func(*player){"changes": func(*player) { cancel() }})

	_, err := check.Run(ctx, newSet(newCase("S1", srv.url)), twoAgents, sc.turn, opts)

	require.ErrorIs(t, err, context.Canceled)
	assert.NoFileExists(t, filepath.Join(opts.Out, "runs", "S1", "t1", "run.json"))
}

func TestALongReadIsCutAndOnlyWhatTheAgentSawCanBeQuoted(t *testing.T) {
	srv := newFakeServer(t, map[string]string{"query_logs": strings.Repeat("a", 30) + "TAIL"})
	opts := options(t)
	opts.MaxOutput = 20
	var read, posted string
	var refused bool
	plays := map[string]func(*player){
		"changes": func(p *player) {
			read, _ = p.call("query_logs", map[string]any{})
			posted, refused = p.call("post_evidence", map[string]any{"claim": "Tail.", "call_id": "changes-c1", "quote": "TAIL"})
		},
	}

	_, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, newScript(t, plays).turn, opts)

	require.NoError(t, err)
	assert.NotContains(t, read, "TAIL")
	assert.Contains(t, read, "cut at 20 of 34 bytes")
	assert.True(t, refused, posted)
}

func TestScoringLeavesToRatersWhatSection134LeavesToThem(t *testing.T) {
	c := check.Case{Labels: []key.Key{
		{check.ServiceField: "checkout", check.FailureModeField: "config-change"},
		{check.ServiceField: "checkout", check.FailureModeField: "bad-release"},
	}}
	k := func(service, mode string) key.Key {
		return key.Key{check.ServiceField: service, check.FailureModeField: mode}
	}
	tests := map[string]struct {
		conclusion  key.Key
		hit, raters bool
	}{
		"no conclusion":           {nil, false, false},
		"a label":                 {k("checkout", "bad-release"), true, false},
		"other service":           {k("other", "config-change"), false, true},
		"other mode":              {k("checkout", "other"), false, true},
		"another service":         {k("cart", "config-change"), false, true},
		"the service, wrong mode": {k("checkout", "resource-exhaustion"), false, false},
		"dependency failure":      {k("checkout", "dependency-failure"), false, true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.hit, c.Hit(tt.conclusion))
			assert.Equal(t, tt.raters, c.RatersWouldJudge(tt.conclusion))
		})
	}
}

func TestRecheckFindsEvidenceThatTheRecordsDoNotBackUp(t *testing.T) {
	calls := []board.ReadCall{{ID: "a-c1", Run: "S1-t1", Agent: "a", Server: "observability", Tool: "query_logs", Output: []byte("checkout error")}}
	evidence := func(id, callID, quote string, source board.Source) board.Event {
		return board.Event{ID: board.EntryID(id), Run: "S1-t1", Author: board.Author{Kind: board.AuthorAgent, Name: "a"}, Body: board.Body{Evidence: &board.Evidence{
			Claim: "x", Provenance: board.Provenance{Kind: board.FromReadCall, CallID: callID, Quote: quote, Source: source},
		}}}
	}
	events := []board.Event{
		evidence("e1", "a-c1", "checkout error", "observability/query_logs"),
		evidence("e2", "a-c1", "payment error", "observability/query_logs"),
		evidence("e3", "a-c9", "checkout", "observability/query_logs"),
		evidence("e4", "a-c1", "checkout", "observability/get_changes"),
	}

	violations := check.Recheck(events, calls)

	require.Len(t, violations, 3)
	assert.Contains(t, violations[0], "e2")
	assert.Contains(t, violations[1], "e3")
	assert.Contains(t, violations[2], "e4")
}

func TestARefusalOfAnUnknownIDSaysWhichIDsToCite(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	var out string
	plays := map[string]func(*player){
		"changes": func(p *player) {
			id := p.read("get_changes")
			out, _ = p.call("post_hypothesis", map[string]any{
				"service": "checkout", "failure_mode": "config-change", "statement": "The cause.", "evidence": []string{id},
			})
		},
	}

	_, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, newScript(t, plays).turn, options(t))

	require.NoError(t, err)
	assert.Contains(t, out, "no such entry: changes-c1")
	assert.Contains(t, out, "cite the ids that post_evidence and post_hypothesis returned, not call_ids")
}

func TestAReadThatDoesNotAnswerIsAnErrorNotTheEndOfTheRound(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)
	opts.ReadTimeout = 200 * time.Millisecond
	turn := func(ctx context.Context, tools runner.Toolbox, cfg *runner.Config, prompt string) (runner.Result, error) {
		_, _, err := tools.Call(ctx, "slow_read", json.RawMessage(`{}`))
		return runner.Result{Outcome: runner.EndedByTool}, err
	}

	report, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, turn, opts)

	require.NoError(t, err)
	assert.Equal(t, 1, report.Errors)
	assert.Contains(t, turnOf(&report.Runs[0], "changes").Error, "slow_read gave no answer in 200ms")
}

func TestCitingAnotherAgentsReadSaysNoMoreThanCitingNothing(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	read := make(chan struct{})
	var foreign, unknown string
	plays := map[string]func(*player){
		"runtime": func(p *player) {
			p.read("query_logs")
			close(read)
		},
		"changes": func(p *player) {
			<-read
			foreign, _ = p.call("post_evidence", map[string]any{"claim": "x", "call_id": "runtime-c1", "quote": "checkout"})
			unknown, _ = p.call("post_evidence", map[string]any{"claim": "x", "call_id": "changes-c9", "quote": "checkout"})
		},
	}

	_, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, newScript(t, plays).turn, options(t))

	require.NoError(t, err)
	assert.Equal(t, `refused: board: provenance check failed: no read call "runtime-c1"`, foreign)
	assert.Equal(t, `refused: board: provenance check failed: no read call "changes-c9"`, unknown)
}

func TestAnyConvergedMissIsAFalseConvergence(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	both := func(first, second string) func(*player) {
		return func(p *player) {
			p.hypothesis(checkoutConfig, p.evidence(p.read(first), "checkout"))
			p.hypothesis(cartRelease, p.evidence(p.read(second), "checkout"))
		}
	}
	plays := map[string]func(*player){"changes": both("get_changes", "query_logs"), "runtime": both("query_logs", "get_changes")}

	report, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, newScript(t, plays).turn, options(t))

	require.NoError(t, err)
	run := report.Runs[0]
	assert.Nil(t, run.Conclusion, "two converged hypotheses tie")
	assert.True(t, run.ConvergedMiss)
	assert.Equal(t, 1, report.Trials[0].FalseConvergence)
}

func TestATurnOverItsShareOfTheBudgetIsAViolation(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)
	opts.RunBudget = 1 // less than one token per agent still caps each turn
	var shares []int64
	var mu sync.Mutex
	turn := func(_ context.Context, _ runner.Toolbox, cfg *runner.Config, _ string) (runner.Result, error) {
		mu.Lock()
		shares = append(shares, cfg.TokenBudget)
		mu.Unlock()
		return runner.Result{Outcome: runner.EndedByModel, Usage: runner.Usage{Output: 10}}, nil
	}

	report, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, turn, opts)

	require.NoError(t, err)
	assert.Equal(t, []int64{1, 1}, shares)
	assert.Equal(t, 2, report.Violations)
	assert.Contains(t, report.Runs[0].Violations[0], "over its share of 1")
}

func TestThePromptLimitMustKeepTheWeightsTrue(t *testing.T) {
	for _, limit := range []int64{0, 100001} {
		opts := options(t)
		opts.Turn.PromptLimit = limit

		_, err := check.Run(context.Background(), newSet(newCase("S1", "http://127.0.0.1:1/mcp")), twoAgents, newScript(t, nil).turn, opts)

		require.ErrorContains(t, err, "prompt limit", limit)
	}
}

func TestOnlyACleanBuildCanResumeACheck(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)
	opts.Build = "0123abc+modified"
	set := newSet(newCase("S1", srv.url))
	_, err := check.Run(context.Background(), set, twoAgents, newScript(t, nil).turn, opts)
	require.NoError(t, err)

	_, err = check.Run(context.Background(), set, twoAgents, newScript(t, nil).turn, opts)

	require.ErrorContains(t, err, "cannot resume")
}

func TestACaseRunAgainstOtherServersCannotJoinItsEarlierRuns(t *testing.T) {
	srv := newVersionedServer(t, "data-1", defaultOutputs)
	opts := options(t)
	opts.Trials = 2
	set := newSet(newCase("S1", srv.url))
	var runtimeTurns atomic.Int32
	turn := func(_ context.Context, _ runner.Toolbox, cfg *runner.Config, _ string) (runner.Result, error) {
		if strings.Contains(strings.Join(cfg.System, "\n"), "Skill of runtime.") && runtimeTurns.Add(1) == 1 {
			return runner.Result{}, errors.New("model call: 529 overloaded")
		}
		return runner.Result{Outcome: runner.EndedByModel}, nil
	}
	first, err := check.Run(context.Background(), set, twoAgents, turn, opts)
	require.NoError(t, err)
	require.Equal(t, 1, first.Errors, "one trial failed and one finished")

	srv.swap("data-2") // the same address now serves other data: a server restarted on new datasets
	_, err = check.Run(context.Background(), set, twoAgents, turn, opts)

	require.ErrorContains(t, err, "different servers")
}

func TestAnUnreadableRunRecordIsRunAgain(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)
	set := newSet(newCase("S1", srv.url))
	_, err := check.Run(context.Background(), set, twoAgents, newScript(t, agreeing).turn, opts)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(opts.Out, "runs", "S1", "t1", "run.json"), []byte(`{"case": "S1", "tri`), 0o600))

	report, err := check.Run(context.Background(), set, twoAgents, newScript(t, agreeing).turn, opts)

	require.NoError(t, err)
	assert.True(t, report.Runs[0].Hit)
	aside, _ := filepath.Glob(filepath.Join(opts.Out, "runs", "S1", "t1.failed-*"))
	assert.Len(t, aside, 1)
}

func TestAnotherCheckCannotUseTheSameDirectoryAtOnce(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)
	require.NoError(t, os.WriteFile(filepath.Join(opts.Out, "lock"), nil, 0o600))

	_, err := check.Run(context.Background(), newSet(newCase("S1", srv.url)), twoAgents, newScript(t, nil).turn, opts)

	require.ErrorContains(t, err, "another check")
}

func TestARerunThatStopsLeavesNoReportFromBefore(t *testing.T) {
	srv := newFakeServer(t, defaultOutputs)
	opts := options(t)
	set := newSet(newCase("S1", srv.url), newCase("S2", srv.url))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopOnS2 := newScript(t, map[string]func(*player){"changes": func(p *player) {
		if strings.Contains(p.prompt, "in S2") {
			cancel()
		}
	}})
	_, err := check.Run(ctx, set, twoAgents, stopOnS2.turn, opts)
	require.ErrorIs(t, err, context.Canceled)
	_, err = check.Run(context.Background(), set, twoAgents, newScript(t, nil).turn, opts)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(opts.Out, "report.json"))

	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	_, err = check.Run(ctx2, set, twoAgents, newScript(t, nil).turn, opts)

	require.ErrorIs(t, err, context.Canceled)
	assert.NoFileExists(t, filepath.Join(opts.Out, "report.json"))
}
