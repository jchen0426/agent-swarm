package check

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/runner"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
)

// Turn plays one agent's turn. runner.Run on a model client is the real one.
type Turn func(ctx context.Context, tools runner.Toolbox, cfg *runner.Config, prompt string) (runner.Result, error)

// Options configure a check.
type Options struct {
	Trials int
	// Turn bounds each agent's turn: Model, MaxTokens, MaxSteps and
	// PromptLimit. The check sets System, TokenBudget and Log.
	Turn runner.Config
	// RunBudget is a run's cost-weighted token budget, split evenly among its
	// agents. Zero means no cap.
	RunBudget int64
	// RoundLimit is the run's one round: every agent's turn ends by then.
	RoundLimit time.Duration
	// EntryLimit caps each agent's board entries in the round.
	EntryLimit int
	// MaxOutput caps the bytes of one read's output that an agent sees.
	MaxOutput int
	// Concurrency is how many runs go at once.
	Concurrency int
	// Out is the directory of the records and the report. A check rerun into
	// it keeps the finished runs and runs the rest again.
	Out string
}

func (o *Options) check(agents []Agent) error {
	switch {
	case len(agents) == 0:
		return errors.New("no agents")
	case o.Trials < 1, o.EntryLimit < 1, o.Concurrency < 1, o.Turn.MaxSteps < 1, o.Turn.MaxTokens < 1:
		return errors.New("trials, entry limit, concurrency, steps and tokens per response must be at least 1")
	case o.RoundLimit <= 0:
		return errors.New("the round limit must be positive")
	case o.RunBudget < 0, o.MaxOutput < 0, o.Turn.PromptLimit < 0:
		return errors.New("budgets and limits cannot be negative")
	case o.Turn.Model == "":
		return errors.New("no model")
	case o.Out == "":
		return errors.New("no output directory")
	}
	return nil
}

type checker struct {
	set    *Set
	agents []Agent
	turn   Turn
	opts   *Options
	out    *os.Root // every record is read and written through it
}

type job struct {
	cs    *Case
	trial int
}

// Run checks the agents on every case of the set, Trials times, and writes
// the records and the report to opts.Out. Each run is one round in which the
// agents work at once and on their own. A canceled check returns ctx's error
// and records no unfinished run.
func Run(ctx context.Context, set *Set, agents []Agent, turn Turn, opts *Options) (*Report, error) {
	if err := opts.check(agents); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.Out, 0o750); err != nil {
		return nil, err
	}
	out, err := os.OpenRoot(opts.Out)
	if err != nil {
		return nil, err
	}
	defer func() { _ = out.Close() }()
	c := &checker{set: set, agents: agents, turn: turn, opts: opts, out: out}
	if err := c.claimOut(); err != nil {
		return nil, err
	}
	var jobs []job
	for trial := 1; trial <= opts.Trials; trial++ {
		for i := range set.Cases {
			jobs = append(jobs, job{cs: &set.Cases[i], trial: trial})
		}
	}
	runs, err := c.runAll(ctx, jobs)
	if err != nil {
		return nil, err
	}
	report := summarize(opts.Turn.Model, opts.Trials, runs)
	return report, c.writeReport(report)
}

// runAll plays the jobs, Concurrency at a time, and returns their reports in
// the jobs' order. Runs that an earlier check finished are not played again.
func (c *checker) runAll(ctx context.Context, jobs []job) ([]RunReport, error) {
	runs := make([]RunReport, len(jobs))
	var (
		mu       sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
	}
	slots := make(chan struct{}, c.opts.Concurrency)
	for i, j := range jobs {
		prior, done, err := c.finished(j)
		if err != nil {
			fail(err)
			break
		}
		if done {
			runs[i] = prior
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-slots }()
			rep, err := c.runCase(ctx, j)
			if err != nil {
				fail(err)
				return
			}
			runs[i] = rep
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return runs, firstErr
}

// runDir is a run's directory in the output directory.
func runDir(j job) string {
	return filepath.Join("runs", j.cs.ID, fmt.Sprintf("t%d", j.trial))
}

// finished returns a run recorded without errors by an earlier check into the
// same directory. Any other record of the run is moved aside, not deleted.
func (c *checker) finished(j job) (RunReport, bool, error) {
	dir := runDir(j)
	raw, err := c.out.ReadFile(filepath.Join(dir, "run.json"))
	switch {
	case err == nil:
		var rep RunReport
		if err := json.Unmarshal(raw, &rep); err != nil {
			return RunReport{}, false, fmt.Errorf("%s: %w", dir, err)
		}
		if len(rep.Errors) == 0 {
			return rep, true, nil
		}
	case !errors.Is(err, fs.ErrNotExist):
		return RunReport{}, false, err
	}
	if _, err := c.out.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return RunReport{}, false, nil
	}
	aside := dir + ".failed-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	return RunReport{}, false, c.out.Rename(dir, aside)
}

// runCase plays one run: every agent's turn at once, within the round limit,
// then the assessment, the scores and the records.
func (c *checker) runCase(ctx context.Context, j job) (RunReport, error) {
	dir := runDir(j)
	if err := c.out.MkdirAll(dir, 0o750); err != nil {
		return RunReport{}, err
	}
	run := board.RunID(fmt.Sprintf("%s-t%d", j.cs.ID, j.trial))
	bc := board.Context{Run: run, Round: 1, Settings: board.Settings{Schema: c.set.Schema(j.cs), Convergence: board.DefaultConvergence}}
	store, calls, runs := memory.NewStore(time.Now), memory.NewCalls(), memory.NewRuns()
	runs.Add(&bc)
	boards := app.NewBoards(store, calls, runs, randomIDs{})

	start := time.Now()
	round, cancel := context.WithTimeout(ctx, c.opts.RoundLimit)
	turns := make([]TurnReport, len(c.agents))
	var wg sync.WaitGroup
	for i := range c.agents {
		wg.Go(func() { turns[i] = c.playTurn(round, dir, &c.agents[i], j.cs, run, boards, calls) })
	}
	wg.Wait()
	cancel()
	if err := ctx.Err(); err != nil {
		return RunReport{}, err
	}

	rep, events, err := c.score(ctx, j, boards, store, run)
	if err != nil {
		return RunReport{}, err
	}
	rep.Seconds = time.Since(start).Seconds()
	rep.Turns = turns
	for i := range turns {
		rep.WeightedTokens += turns[i].WeightedTokens
		if turns[i].Error != "" {
			rep.Errors = append(rep.Errors, turns[i].Agent+": "+turns[i].Error)
		}
	}
	all := calls.All()
	rep.Violations = Recheck(events, all)
	err = c.writeRun(dir, &bc, events, all, &rep)
	return rep, err
}

// score assesses the board as the independent run does and scores its
// conclusion against the case.
func (c *checker) score(ctx context.Context, j job, boards *app.Boards, store *memory.Store, run board.RunID) (RunReport, []board.Event, error) {
	a, err := boards.Assess(ctx, run, time.Time{})
	if err != nil {
		return RunReport{}, nil, fmt.Errorf("assess %s: %w", run, err)
	}
	events, err := store.Load(ctx, run)
	if err != nil {
		return RunReport{}, nil, err
	}
	rep := RunReport{
		Case: j.cs.ID, Trial: j.trial, Run: run, Change: j.cs.Change,
		Baseline: j.cs.Baseline, BaselineHit: j.cs.Hit(j.cs.Baseline), Hypotheses: len(a.Hypotheses),
	}
	for i := range a.Hypotheses {
		if h := &a.Hypotheses[i]; h.ID == a.Conclusion {
			rep.Conclusion, rep.Converged = h.Key, h.Converged
		}
	}
	rep.Hit = j.cs.Hit(rep.Conclusion)
	rep.Raters = j.cs.RatersWouldJudge(rep.Conclusion)
	return rep, events, nil
}

// playTurn plays one agent's turn and reports it. Failures to reach a server
// or the model end the turn and are reported, not returned.
func (c *checker) playTurn(ctx context.Context, dir string, a *Agent, cs *Case, run board.RunID, boards *app.Boards, calls *memory.Calls) (rep TurnReport) {
	rep.Agent = a.Name
	start := time.Now()
	defer func() { rep.Seconds = time.Since(start).Seconds() }()
	tb, err := openToolbox(ctx, c, cs, run, a.Name, &rep, boards, calls)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	defer tb.close()
	transcript, err := c.out.Create(filepath.Join(dir, a.Name+".jsonl"))
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	defer func() { _ = transcript.Close() }()
	cfg := c.opts.Turn
	cfg.System = systemPrompt(c.opts.EntryLimit, a.Skill)
	cfg.TokenBudget = c.opts.RunBudget / int64(len(c.agents))
	cfg.Log = transcript
	res, err := c.turn(ctx, tb, &cfg, taskPrompt(c.set, &cs.Task, tb.instructions))
	rep.Outcome, rep.Steps, rep.ToolCalls, rep.Usage = res.Outcome, res.Steps, res.Calls, res.Usage
	rep.WeightedTokens = res.Usage.Weighted()
	if err != nil {
		rep.Error = err.Error()
	}
	return rep
}

// callRecord is a read call as the records keep it.
type callRecord struct {
	ID     string `json:"id"`
	Agent  string `json:"agent"`
	Server string `json:"server"`
	Tool   string `json:"tool"`
	Failed bool   `json:"failed"`
	Output string `json:"output"`
}

// writeRun records a run: its board in the conclude command's form, its read
// calls, and its report, written last because it marks the run finished.
func (c *checker) writeRun(dir string, bc *board.Context, events []board.Event, calls []board.ReadCall, rep *RunReport) error {
	boardFile := struct {
		Settings board.Settings `json:"settings"`
		Events   []board.Event  `json:"events"`
	}{bc.Settings, events}
	if err := c.writeJSON(filepath.Join(dir, "board.json"), boardFile); err != nil {
		return err
	}
	records := make([]callRecord, 0, len(calls))
	for i := range calls {
		r := &calls[i]
		records = append(records, callRecord{ID: r.ID, Agent: r.Agent, Server: r.Server, Tool: r.Tool, Failed: r.Failed, Output: string(r.Output)})
	}
	if err := c.writeJSON(filepath.Join(dir, "calls.json"), records); err != nil {
		return err
	}
	return c.writeJSON(filepath.Join(dir, "run.json"), rep)
}

// writeReport writes the report and, for each trial, its conclusions as
// answers: case ids to keys.
func (c *checker) writeReport(r *Report) error {
	for _, t := range r.Trials {
		answers := map[string]any{}
		for i := range r.Runs {
			if run := &r.Runs[i]; run.Trial == t.Trial && run.Conclusion != nil {
				answers[run.Case] = run.Conclusion
			}
		}
		if err := c.writeJSON(fmt.Sprintf("answers-t%d.json", t.Trial), answers); err != nil {
			return err
		}
	}
	return c.writeJSON("report.json", r)
}

func (c *checker) writeJSON(name string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return c.out.WriteFile(name, append(raw, '\n'), 0o600)
}

// inputs are what a check's results depend on. A check rerun into a
// directory must have the inputs of the check that made it.
type inputs struct {
	Set         *Set          `json:"set"`
	Agents      []agentDigest `json:"agents"`
	Model       string        `json:"model"`
	MaxTokens   int64         `json:"maxTokens"`
	MaxSteps    int           `json:"maxSteps"`
	PromptLimit int64         `json:"promptLimit"`
	Trials      int           `json:"trials"`
	RunBudget   int64         `json:"runBudget"`
	RoundLimit  string        `json:"roundLimit"`
	EntryLimit  int           `json:"entryLimit"`
	MaxOutput   int           `json:"maxOutput"`
}

// agentDigest names an agent and the SHA-256 of its skill.
type agentDigest struct {
	Name  string `json:"name"`
	Skill string `json:"skill"`
}

// claimOut makes the output directory this check's, or checks that it holds
// a check with the same inputs.
func (c *checker) claimOut() error {
	in := inputs{
		Set: c.set, Model: c.opts.Turn.Model, MaxTokens: c.opts.Turn.MaxTokens, MaxSteps: c.opts.Turn.MaxSteps,
		PromptLimit: c.opts.Turn.PromptLimit, Trials: c.opts.Trials, RunBudget: c.opts.RunBudget,
		RoundLimit: c.opts.RoundLimit.String(), EntryLimit: c.opts.EntryLimit, MaxOutput: c.opts.MaxOutput,
	}
	for _, a := range c.agents {
		sum := sha256.Sum256([]byte(a.Skill))
		in.Agents = append(in.Agents, agentDigest{Name: a.Name, Skill: hex.EncodeToString(sum[:])})
	}
	want, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	want = append(want, '\n')
	have, err := c.out.ReadFile("inputs.json")
	switch {
	case err == nil && bytes.Equal(have, want):
		return nil
	case err == nil:
		return fmt.Errorf("%s holds a check with other inputs; use another directory", c.opts.Out)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	entries, err := fs.ReadDir(c.out.FS(), ".")
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s is not empty and holds no check", c.opts.Out)
	}
	return c.out.WriteFile("inputs.json", want, 0o600)
}
