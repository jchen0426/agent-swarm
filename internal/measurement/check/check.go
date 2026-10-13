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
	"slices"
	"strings"
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
	// ReadTimeout bounds one read. A read that does not answer in time ends
	// the turn as an infrastructure failure.
	ReadTimeout time.Duration
	// EntryLimit caps each agent's board entries in the round.
	EntryLimit int
	// MaxOutput caps the bytes of one read's output that an agent sees.
	MaxOutput int
	// Concurrency is how many runs go at once.
	Concurrency int
	// Out is the directory of the records and the report. A check rerun into
	// it keeps the finished runs and runs the rest again.
	Out string
	// Build names the code that runs the check: its version control revision,
	// with "+modified" when the tree had uncommitted changes. Only a check
	// made by a clean build can be resumed, so that its runs share one code.
	Build string
}

// MaxPromptLimit is the largest prompt limit: runner.Usage.Weighted prices
// requests under 100,000 input tokens.
const MaxPromptLimit = 100000

func (o *Options) check(agents []Agent) error {
	switch {
	case len(agents) == 0:
		return errors.New("no agents")
	case o.Trials < 1, o.EntryLimit < 1, o.Concurrency < 1, o.Turn.MaxSteps < 1, o.Turn.MaxTokens < 1:
		return errors.New("trials, entry limit, concurrency, steps and tokens per response must be at least 1")
	case o.RoundLimit <= 0, o.ReadTimeout <= 0:
		return errors.New("the round limit and the read timeout must be positive")
	case o.Turn.PromptLimit < 1 || o.Turn.PromptLimit > MaxPromptLimit:
		return fmt.Errorf("the prompt limit must be from 1 to %d tokens, for the token weights to hold", MaxPromptLimit)
	case o.RunBudget < 0, o.MaxOutput < 0:
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
	unlock, err := c.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := c.claimOut(); err != nil {
		return nil, err
	}
	if err := c.setAsideReport(); err != nil {
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
	if err := sameServers(runs); err != nil {
		return nil, err
	}
	report := summarize(opts.Turn.Model, opts.Trials, runs)
	return report, c.writeReport(report)
}

// lock makes the output directory this process's until unlock.
func (c *checker) lock() (unlock func(), err error) {
	f, err := c.out.OpenFile("lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("another check is using %s; if none is, remove %s", c.opts.Out, filepath.Join(c.opts.Out, "lock"))
	}
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	return func() { _ = c.out.Remove("lock") }, nil
}

// setAsideReport moves the report and answers of an earlier check aside, so
// that a check that stops leaves no report that looks like its own.
func (c *checker) setAsideReport() error {
	names, err := fs.Glob(c.out.FS(), "answers-t*.json")
	if err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	for _, name := range append(names, "report.json") {
		err := c.out.Rename(name, name+".before-"+stamp)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// sameServers checks that every run of a case found the same read servers,
// so that a resumed check never joins runs that read different data.
func sameServers(runs []RunReport) error {
	first := map[string]*RunReport{}
	for i := range runs {
		r := &runs[i]
		if len(r.Servers) == 0 {
			continue
		}
		f, ok := first[r.Case]
		if !ok {
			first[r.Case] = r
			continue
		}
		if !slices.Equal(f.Servers, r.Servers) {
			return fmt.Errorf("case %s's runs read different servers: %v in trial %d, %v in trial %d", r.Case, f.Servers, f.Trial, r.Servers, r.Trial)
		}
	}
	return nil
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
// same directory. Any other record of the run, such as one with errors or one
// that cannot be read, is moved aside, not deleted.
func (c *checker) finished(j job) (RunReport, bool, error) {
	dir := runDir(j)
	raw, err := c.out.ReadFile(filepath.Join(dir, "run.json"))
	switch {
	case err == nil:
		var rep RunReport
		if json.Unmarshal(raw, &rep) == nil && len(rep.Errors) == 0 {
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
	all := calls.All()
	rep.Violations = Recheck(events, all)
	for i := range turns {
		t := &turns[i]
		rep.WeightedTokens += t.WeightedTokens
		if t.Error != "" {
			rep.Errors = append(rep.Errors, t.Agent+": "+t.Error)
		}
		if t.Budget > 0 && t.WeightedTokens > t.Budget {
			rep.Violations = append(rep.Violations, fmt.Sprintf("turn %s used %d cost-weighted tokens, over its share of %d", t.Agent, t.WeightedTokens, t.Budget))
		}
		if rep.Servers == nil && len(t.Servers) > 0 {
			rep.Servers = t.Servers
		}
	}
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
		h := &a.Hypotheses[i]
		if h.ID == a.Conclusion {
			rep.Conclusion, rep.Converged = h.Key, h.Converged
		}
		if h.Converged && !j.cs.Hit(h.Key) {
			rep.ConvergedMiss = true
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
	cfg.TokenBudget = c.share()
	rep.Budget = cfg.TokenBudget
	cfg.Log = transcript
	res, err := c.turn(ctx, tb, &cfg, taskPrompt(c.set, &cs.Task, tb.instructions))
	rep.Outcome, rep.Steps, rep.ToolCalls, rep.Usage = res.Outcome, res.Steps, res.Calls, res.Usage
	rep.WeightedTokens = res.Usage.Weighted()
	if err != nil {
		rep.Error = err.Error()
	}
	return rep
}

// share is each agent's part of the run's budget: an even split, and at least
// one token, so that a small budget never reads as none.
func (c *checker) share() int64 {
	if c.opts.RunBudget == 0 {
		return 0
	}
	return max(c.opts.RunBudget/int64(len(c.agents)), 1)
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

// writeJSON writes a file whole or not at all: to a temporary name first,
// then renamed into place.
func (c *checker) writeJSON(name string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := c.out.WriteFile(name+".tmp", append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return c.out.Rename(name+".tmp", name)
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
	ReadTimeout string        `json:"readTimeout"`
	EntryLimit  int           `json:"entryLimit"`
	MaxOutput   int           `json:"maxOutput"`
	Build       string        `json:"build"`
}

// cleanBuild reports whether a build names a revision without uncommitted
// changes.
func cleanBuild(build string) bool {
	return build != "" && build != "unknown" && !strings.HasSuffix(build, "+modified")
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
		RoundLimit: c.opts.RoundLimit.String(), ReadTimeout: c.opts.ReadTimeout.String(), EntryLimit: c.opts.EntryLimit, MaxOutput: c.opts.MaxOutput,
		Build: c.opts.Build,
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
	case err == nil && bytes.Equal(have, want) && !cleanBuild(c.opts.Build):
		return fmt.Errorf("cannot resume the check in %s: build %q does not pin its code; use another directory", c.opts.Out, c.opts.Build)
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
	if slices.ContainsFunc(entries, func(e fs.DirEntry) bool { return e.Name() != "lock" }) {
		return fmt.Errorf("%s is not empty and holds no check", c.opts.Out)
	}
	return c.out.WriteFile("inputs.json", want, 0o600)
}
