package check

import (
	"fmt"
	"slices"

	"github.com/jchen0426/agent-swarm/internal/runner"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// dependencyFailure is the failure mode whose mismatches spec Section 13.4
// leaves to raters.
const dependencyFailure = "dependency-failure"

// Hit reports whether a conclusion equals one of the case's labels. No
// conclusion is never a hit.
func (c *Case) Hit(k key.Key) bool {
	return k != nil && slices.ContainsFunc(c.Labels, k.Equal)
}

// RatersWouldJudge reports whether spec Section 13.4 would leave a
// conclusion to raters instead of deciding it by key: its key contains other,
// or it misses with dependency-failure on either side or with a service that
// no label names. A check has no raters, so such a conclusion counts as a
// miss, and the report marks it.
func (c *Case) RatersWouldJudge(k key.Key) bool {
	switch {
	case k == nil:
		return false
	case hasOther(k):
		return true
	case c.Hit(k):
		return false
	case k[FailureModeField] == dependencyFailure ||
		slices.ContainsFunc(c.Labels, func(l key.Key) bool { return l[FailureModeField] == dependencyFailure }):
		return true
	}
	return !slices.ContainsFunc(c.Labels, func(l key.Key) bool { return l[ServiceField] == k[ServiceField] })
}

// Server is a read server as a turn found it: its name and version, and the
// SHA-256 of its read-only tools' names, descriptions and schemas.
type Server struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Tools   string `json:"tools"`
}

// TurnReport is one agent's turn in one run.
type TurnReport struct {
	Agent   string         `json:"agent"`
	Servers []Server       `json:"servers,omitempty"`
	Outcome runner.Outcome `json:"outcome,omitempty"`
	// Nudged says the turn sent the runtime's nudge (runner.Result.Nudged),
	// as a check run with --nudge does: a turn that ended no_tool with it set
	// gave a second answer without a tool, after the nudge. Plan 3's records,
	// whose checks never nudged, have no such field.
	Nudged bool `json:"nudged,omitempty"`
	// Error is an infrastructure failure that ended the turn: the model API or
	// a read server.
	Error          string       `json:"error,omitempty"`
	Steps          int          `json:"steps"`
	ToolCalls      int          `json:"toolCalls"`
	Usage          runner.Usage `json:"usage"`
	WeightedTokens int64        `json:"weightedTokens"`
	// Budget is the turn's share of the run's budget; zero means no cap.
	Budget      int64   `json:"budget"`
	Seconds     float64 `json:"seconds"`
	Reads       int     `json:"reads"`
	FailedReads int     `json:"failedReads"`
	Entries     int     `json:"entries"`
	// Refused lists the posts the board refused, with the reason.
	Refused []string `json:"refused,omitempty"`
}

// RunReport is one case in one trial.
type RunReport struct {
	Case       string      `json:"case"`
	Trial      int         `json:"trial"`
	Run        board.RunID `json:"run"`
	Servers    []Server    `json:"servers"`
	Change     bool        `json:"change"`
	Conclusion key.Key     `json:"conclusion"`
	// Converged is true when the conclusion converged. ConvergedMiss is true
	// when any hypothesis that converged misses the labels: a false
	// convergence (spec Section 13.2).
	Converged     bool    `json:"converged"`
	ConvergedMiss bool    `json:"convergedMiss"`
	Hit           bool    `json:"hit"`
	Raters        bool    `json:"raters"`
	Baseline      key.Key `json:"baseline"`
	BaselineHit   bool    `json:"baselineHit"`
	// Hypotheses counts the distinct keys on the board.
	Hypotheses     int          `json:"hypotheses"`
	Seconds        float64      `json:"seconds"`
	WeightedTokens int64        `json:"weightedTokens"`
	Turns          []TurnReport `json:"turns"`
	// Violations are breaches of the board's mechanics found by Recheck.
	Violations []string `json:"violations,omitempty"`
	// Errors are the turns' infrastructure failures. A run with errors is run
	// again.
	Errors []string `json:"errors,omitempty"`
}

// TrialSummary counts one trial's results over the set.
type TrialSummary struct {
	Trial              int `json:"trial"`
	Cases              int `json:"cases"`
	Hits               int `json:"hits"`
	BaselineHits       int `json:"baselineHits"`
	ChangeCases        int `json:"changeCases"`
	ChangeHits         int `json:"changeHits"`
	ChangeBaselineHits int `json:"changeBaselineHits"`
	NoConclusion       int `json:"noConclusion"`
	// FalseConvergence counts runs in which a hypothesis that misses converged.
	FalseConvergence int   `json:"falseConvergence"`
	RaterCases       int   `json:"raterCases"`
	Errors           int   `json:"errors"`
	Violations       int   `json:"violations"`
	WeightedTokens   int64 `json:"weightedTokens"`
}

// Report is a whole check.
type Report struct {
	Model  string         `json:"model"`
	Trials []TrialSummary `json:"trials"`
	Runs   []RunReport    `json:"runs"`
	// Errors counts runs with an infrastructure failure, and Violations the
	// breaches of the board's mechanics over all runs.
	Errors     int `json:"errors"`
	Violations int `json:"violations"`
}

// summarize counts each trial's runs.
func summarize(model string, trials int, runs []RunReport) *Report {
	r := &Report{Model: model, Runs: runs}
	for t := 1; t <= trials; t++ {
		s := TrialSummary{Trial: t}
		for i := range runs {
			run := &runs[i]
			if run.Trial != t {
				continue
			}
			s.Cases++
			s.Hits += btoi(run.Hit)
			s.BaselineHits += btoi(run.BaselineHit)
			if run.Change {
				s.ChangeCases++
				s.ChangeHits += btoi(run.Hit)
				s.ChangeBaselineHits += btoi(run.BaselineHit)
			}
			s.NoConclusion += btoi(run.Conclusion == nil)
			s.FalseConvergence += btoi(run.ConvergedMiss)
			s.RaterCases += btoi(run.Raters)
			s.Errors += btoi(len(run.Errors) > 0)
			s.Violations += len(run.Violations)
			s.WeightedTokens += run.WeightedTokens
		}
		r.Errors += s.Errors
		r.Violations += s.Violations
		r.Trials = append(r.Trials, s)
	}
	return r
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Recheck checks every agent's evidence against the recorded read calls, after
// the run: the call exists, belongs to the evidence's run and author, did not
// fail, holds the quote, and is the evidence's source. The board checked all
// of it when the evidence was posted; this is the check's own record that
// nothing slipped through (C1).
func Recheck(events []board.Event, calls []board.ReadCall) []string {
	byID := make(map[string]*board.ReadCall, len(calls))
	for i := range calls {
		byID[calls[i].ID] = &calls[i]
	}
	var out []string
	for i := range events {
		e := &events[i]
		ev := e.Body.Evidence
		if ev == nil || e.Author.Kind != board.AuthorAgent {
			continue
		}
		p := &ev.Provenance
		call, ok := byID[p.CallID]
		switch {
		case !ok:
			out = append(out, fmt.Sprintf("evidence %s cites read call %q, which is not recorded", e.ID, p.CallID))
		case call.Run != e.Run || call.Agent != e.Author.Name:
			out = append(out, fmt.Sprintf("evidence %s cites read call %s of another run or agent", e.ID, call.ID))
		case call.Failed:
			out = append(out, fmt.Sprintf("evidence %s cites read call %s, which failed", e.ID, call.ID))
		case !board.QuoteAppears(call.Output, p.Quote):
			out = append(out, fmt.Sprintf("evidence %s quotes text that is not in read call %s", e.ID, call.ID))
		case p.Source != board.ToolSource(call.Server, call.Tool):
			out = append(out, fmt.Sprintf("evidence %s names source %s, but read call %s is %s", e.ID, p.Source, call.ID, board.ToolSource(call.Server, call.Tool)))
		}
	}
	return out
}
