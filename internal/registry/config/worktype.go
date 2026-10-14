package config

import (
	"fmt"
	"regexp"
	"slices"
	"time"
)

// Formats of work item types, shared with trigger mappings and agents.
var (
	// token is a word without white space: a milestone, a task tag, a group.
	token = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	// runtimeName names an agent runtime, which the deployment maps to an image.
	runtimeName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)
)

// otherValue is the value every key field accepts besides its closed set
// (spec Section 4.3), so a closed list does not repeat it.
const otherValue = "other"

// maxAgentsPerRun caps a run's team. The spec's default is 4.
const maxAgentsPerRun = 16

// The defaults of spec Section 15.
var defaultSettings = WorkSettings{
	Budget:         Budget{WallClock: 15 * time.Minute, Tokens: 2_000_000, Rounds: 6},
	RoundLimit:     150 * time.Second,
	MaxAgents:      4,
	Limits:         Limits{EntriesPerRound: 5, OpenClaims: 2, LeaseRounds: 2},
	Convergence:    Convergence{MinContributors: 2, MinSources: 2, RefutationMargin: 2},
	AutoClose:      30 * time.Minute,
	InvisibleShare: 0.5,
}

// checkWorkItemTypes checks every work item type and resolves its settings.
func (ix *index) checkWorkItemTypes(ps *problems) {
	for _, name := range sortedKeys(ix.workTypes) {
		w := ix.workTypes[name]
		w.settings = ix.checkWorkItemType(w, ps)
	}
}

func (ix *index) checkWorkItemType(w *WorkItemType, ps *problems) WorkSettings {
	f, sp := w.file, &w.Spec
	s := defaultSettings
	s.Key = sp.HypothesisKey
	ix.checkKey(f, sp.HypothesisKey, ps)
	s.Budget = resolveBudget(f, "spec.budget", sp.Budget, s.Budget, ps)
	s.RoundLimit = resolveRound(f, "spec.round", sp.Round, s.RoundLimit, s.Budget, ps)
	s.MaxAgents = atLeast(f, "spec.team.maxAgents", sp.Team.MaxAgents, s.MaxAgents, 1, ps)
	if s.MaxAgents > maxAgentsPerRun {
		ps.add(f, "spec.team.maxAgents", "must be at most %d", maxAgentsPerRun)
	}
	s.Limits = Limits{
		EntriesPerRound: atLeast(f, "spec.limits.entriesPerRound", sp.Limits.EntriesPerRound, s.Limits.EntriesPerRound, 1, ps),
		OpenClaims:      atLeast(f, "spec.limits.openClaims", sp.Limits.OpenClaims, s.Limits.OpenClaims, 1, ps),
		LeaseRounds:     atLeast(f, "spec.limits.leaseRounds", sp.Limits.LeaseRounds, s.Limits.LeaseRounds, 1, ps),
	}
	s.Convergence = Convergence{
		MinContributors:  atLeast(f, "spec.convergence.minContributors", sp.Convergence.MinContributors, s.Convergence.MinContributors, 1, ps),
		MinSources:       atLeast(f, "spec.convergence.minSources", sp.Convergence.MinSources, s.Convergence.MinSources, 1, ps),
		RefutationMargin: atLeast(f, "spec.convergence.refutationMargin", sp.Convergence.RefutationMargin, s.Convergence.RefutationMargin, 0, ps),
	}
	s.Milestones = sp.Milestones
	checkTokens(f, "spec.milestones", sp.Milestones, ps)
	s.AutoClose = positive(f, "spec.autoClose.after", sp.AutoClose.After, s.AutoClose, ps)
	if share := sp.Measurement.InvisibleShare; share != nil {
		if *share < 0 || *share > 1 {
			ps.add(f, "spec.measurement.invisibleShare", "must be between 0 and 1, got %v", *share)
		}
		s.InvisibleShare = *share
	}
	s.ShadowRuns = ix.checkShadowRuns(f, sp.ShadowRuns, &s, ps)
	return s
}

// checkKey checks the hypothesis key's fields and their closed value sets.
// Values read when an item opens take fixed arguments.
func (ix *index) checkKey(f string, fields []KeyField, ps *problems) {
	seen := map[string]bool{}
	for i, kf := range fields {
		field := fmt.Sprintf("spec.hypothesisKey[%d]", i)
		switch {
		case !argName.MatchString(kf.Name):
			ps.add(f, field+".name", "must be a field name such as failureMode, got %q", kf.Name)
		case seen[kf.Name]:
			ps.add(f, field+".name", "repeats field %q", kf.Name)
		}
		seen[kf.Name] = true
		switch {
		case kf.Values.Read != nil:
			ix.checkRead(f, field+".values", "", kf.Values.Read, func(hole string) string {
				return fmt.Sprintf("uses {%s}; key values are read with fixed arguments", hole)
			}, ps)
		case kf.Values.List == nil:
			ps.add(f, field+".values", "is required: a list, or a read")
		default:
			checkValueList(f, field+".values", kf.Values.List, ps)
		}
	}
}

func checkValueList(f, field string, values []string, ps *problems) {
	seen := map[string]bool{}
	for i, v := range values {
		switch {
		case v == "":
			ps.add(f, fmt.Sprintf("%s[%d]", field, i), "is empty")
		case v == otherValue:
			ps.add(f, fmt.Sprintf("%s[%d]", field, i), "every field accepts %q already", otherValue)
		case seen[v]:
			ps.add(f, fmt.Sprintf("%s[%d]", field, i), "repeats %q", v)
		}
		seen[v] = true
	}
}

func (ix *index) checkShadowRuns(f string, runs []ShadowRun, base *WorkSettings, ps *problems) []ShadowSettings {
	var out []ShadowSettings
	seen := map[string]bool{}
	for i, r := range runs {
		field := fmt.Sprintf("spec.shadowRuns[%d]", i)
		switch {
		case !objectName.MatchString(r.Name):
			ps.add(f, field+".name", "must be a lowercase DNS label, got %q", r.Name)
		case seen[r.Name]:
			ps.add(f, field+".name", "repeats shadow run %q", r.Name)
		}
		seen[r.Name] = true
		if len(r.Arms) == 0 {
			ps.add(f, field+".arms", "names no arm")
		}
		for j, arm := range r.Arms {
			if arm != "visible" && arm != "invisible" || slices.Index(r.Arms, arm) != j {
				ps.add(f, fmt.Sprintf("%s.arms[%d]", field, j), "must be visible or invisible, each once, got %q", arm)
			}
		}
		budget := resolveBudget(f, field+".override.budget", r.Override.Budget, base.Budget, ps)
		for j, agent := range r.Override.Agents {
			if ix.agents[agent] == nil {
				ps.add(f, fmt.Sprintf("%s.override.agents[%d]", field, j), "names no agent %q", agent)
			}
		}
		if rt := r.Override.Runtime; rt != "" && !runtimeName.MatchString(rt) {
			ps.add(f, field+".override.runtime", "must be a runtime name, got %q", rt)
		}
		out = append(out, ShadowSettings{
			Name: r.Name, Arms: r.Arms, Budget: budget,
			RoundLimit: resolveRound(f, field+".override.round", r.Override.Round, base.RoundLimit, budget, ps),
			Agents:     r.Override.Agents, Runtime: r.Override.Runtime,
		})
	}
	return out
}

func resolveBudget(f, field string, b BudgetSpec, base Budget, ps *problems) Budget {
	out := Budget{
		WallClock: positive(f, field+".wallClock", b.WallClock, base.WallClock, ps),
		Rounds:    atLeast(f, field+".rounds", b.Rounds, base.Rounds, 1, ps),
		Tokens:    base.Tokens,
	}
	if b.Tokens != nil {
		if *b.Tokens < 1 {
			ps.add(f, field+".tokens", "must be at least 1, got %d", *b.Tokens)
		}
		out.Tokens = *b.Tokens
	}
	return out
}

// resolveRound resolves a round time limit, which must fit in the budget's
// wall clock.
func resolveRound(f, field string, r RoundSpec, base time.Duration, budget Budget, ps *problems) time.Duration {
	limit := positive(f, field+".timeLimit", r.TimeLimit, base, ps)
	if limit > budget.WallClock {
		ps.add(f, field+".timeLimit", "is %v, longer than the wall-clock budget of %v", limit, budget.WallClock)
	}
	return limit
}

func atLeast(f, field string, v *int, def, least int, ps *problems) int {
	if v == nil {
		return def
	}
	if *v < least {
		ps.add(f, field, "must be at least %d, got %d", least, *v)
	}
	return *v
}

func positive(f, field string, v *time.Duration, def time.Duration, ps *problems) time.Duration {
	if v == nil {
		return def
	}
	if *v <= 0 {
		ps.add(f, field, "must be positive, got %v", *v)
	}
	return *v
}

// checkTokens checks a list of words, such as milestones or task tags.
func checkTokens(f, field string, list []string, ps *problems) {
	for i, v := range list {
		if !token.MatchString(v) || slices.Index(list, v) != i {
			ps.add(f, fmt.Sprintf("%s[%d]", field, i), "must be a lowercase word, each once, got %q", v)
		}
	}
}
