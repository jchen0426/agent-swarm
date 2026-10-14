package config

import (
	"maps"
	"slices"
)

// A revision is shared by every work item that locked it, so its accessors
// return copies. Each copy is a shallow copy whose unshare method has replaced
// every slice, map and pointer it holds with a copy of its own: nothing a
// caller changes reaches the revision. Compiled patterns and selectors stay
// shared, since they never change.

// sharer is a pointer to a type whose shallow copies can be unshared.
type sharer[T any] interface {
	*T
	unshare()
}

// copyOf returns a copy of p that shares nothing with it, or nil.
func copyOf[T any, P sharer[T]](p P) P {
	if p == nil {
		return nil
	}
	c := P(new(T))
	*c = *p
	c.unshare()
	return c
}

// copyEach returns a copy of s, each element copied by copyOf.
func copyEach[T any, P sharer[T]](s []P) []P {
	if s == nil {
		return nil
	}
	out := make([]P, len(s))
	for i, p := range s {
		out[i] = copyOf(p)
	}
	return out
}

// copyValues returns a copy of m, each value copied by copyOf.
func copyValues[T any, P sharer[T]](m map[string]P) map[string]P {
	if m == nil {
		return nil
	}
	out := make(map[string]P, len(m))
	for k, p := range m {
		out[k] = copyOf(p)
	}
	return out
}

// unshareEach returns a copy of s, each element unshared.
func unshareEach[T any, P sharer[T]](s []T) []T {
	out := slices.Clone(s)
	for i := range out {
		P(&out[i]).unshare()
	}
	return out
}

// copied returns a copy of what p points to, or nil, for values that hold no
// slice, map or pointer.
func copied[T any](p *T) *T {
	if p == nil {
		return nil
	}
	c := *p
	return &c
}

func (a *Agent) unshare() {
	a.Spec.Skills = slices.Clone(a.Spec.Skills)
	a.Spec.Tools = slices.Clone(a.Spec.Tools)
	a.Spec.Subscribe.WorkItemTypes = slices.Clone(a.Spec.Subscribe.WorkItemTypes)
	a.Spec.Subscribe.TaskTags = slices.Clone(a.Spec.Subscribe.TaskTags)
	a.tools = slices.Clone(a.tools)
	a.skills = slices.Clone(a.skills)
}

func (s *Skill) unshare() { s.Spec.Versions = copyEach(s.Spec.Versions) }

func (v *SkillVersion) unshare() { v.files = slices.Clone(v.files) }

func (s *McpServer) unshare() { s.Spec.Tools = copyValues(s.Spec.Tools) }

func (t *ToolPolicy) unshare() {
	t.Redact = slices.Clone(t.Redact)
	t.Bounds = maps.Clone(t.Bounds)
	t.Target = slices.Clone(t.Target)
	t.Scope = maps.Clone(t.Scope)
	t.Candidates = copyValues(t.Candidates)
	t.Facts = copyValues(t.Facts)
}

func (r *PlatformRead) unshare() { r.Args = maps.Clone(r.Args) }

func (w *WorkItemType) unshare() {
	sp := &w.Spec
	sp.HypothesisKey = unshareEach(sp.HypothesisKey)
	sp.Budget.unshare()
	sp.Round.unshare()
	sp.Team.MaxAgents = copied(sp.Team.MaxAgents)
	sp.Limits = LimitsSpec{
		EntriesPerRound: copied(sp.Limits.EntriesPerRound),
		OpenClaims:      copied(sp.Limits.OpenClaims),
		LeaseRounds:     copied(sp.Limits.LeaseRounds),
	}
	sp.Convergence = ConvergeSpec{
		MinContributors:  copied(sp.Convergence.MinContributors),
		MinSources:       copied(sp.Convergence.MinSources),
		RefutationMargin: copied(sp.Convergence.RefutationMargin),
	}
	sp.Milestones = slices.Clone(sp.Milestones)
	sp.AutoClose.After = copied(sp.AutoClose.After)
	sp.Measurement.InvisibleShare = copied(sp.Measurement.InvisibleShare)
	sp.ShadowRuns = unshareEach(sp.ShadowRuns)
	w.settings.Key = unshareEach(w.settings.Key)
	w.settings.Milestones = slices.Clone(w.settings.Milestones)
	w.settings.ShadowRuns = unshareEach(w.settings.ShadowRuns)
}

func (k *KeyField) unshare() {
	k.Values.List = slices.Clone(k.Values.List)
	k.Values.Read = copyOf(k.Values.Read)
}

func (b *BudgetSpec) unshare() {
	b.WallClock, b.Tokens, b.Rounds = copied(b.WallClock), copied(b.Tokens), copied(b.Rounds)
}

func (r *RoundSpec) unshare() { r.TimeLimit = copied(r.TimeLimit) }

func (r *ShadowRun) unshare() {
	r.Arms = slices.Clone(r.Arms)
	r.Override.Budget.unshare()
	r.Override.Round.unshare()
	r.Override.Agents = slices.Clone(r.Override.Agents)
}

func (s *ShadowSettings) unshare() {
	s.Arms = slices.Clone(s.Arms)
	s.Agents = slices.Clone(s.Agents)
}

func (m *TriggerMapping) unshare() {
	sp := &m.Spec
	sp.Authentication.MaxAge = copied(sp.Authentication.MaxAge)
	sp.TrustedFields = slices.Clone(sp.TrustedFields)
	sp.Status.Firing = slices.Clone(sp.Status.Firing)
	sp.Status.Resolved = slices.Clone(sp.Status.Resolved)
	sp.Correlate = copyOf(sp.Correlate)
	sp.Principal = copyOf(sp.Principal)
	sp.Responders = copyOf(sp.Responders)
	sp.FallbackPrincipal = copied(sp.FallbackPrincipal)
	sp.Redact = slices.Clone(sp.Redact)
	sp.Checks = slices.Clone(sp.Checks)
}

func (c *Correlation) unshare() { c.Within = copied(c.Within) }

func (r *RedactionRule) unshare() {
	r.Spec.Patterns = slices.Clone(r.Spec.Patterns)
	r.Spec.Paths = slices.Clone(r.Spec.Paths)
	r.compiled.patterns = slices.Clone(r.compiled.patterns)
	r.compiled.paths = slices.Clone(r.compiled.paths)
}
