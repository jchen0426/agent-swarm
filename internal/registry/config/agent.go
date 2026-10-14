package config

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// modelID is a model identifier behind the LLM gateway.
var modelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)

// grantPattern is the tool part of a grant: a tool name in which * matches
// any run of characters.
var grantPattern = regexp.MustCompile(`^[A-Za-z0-9_*][A-Za-z0-9_.*-]{0,127}$`)

// checkAgents checks every agent, pins its skills and resolves its grants.
func (ix *index) checkAgents(ps *problems) {
	for _, name := range sortedKeys(ix.agents) {
		a := ix.agents[name]
		f, sp := a.file, &a.Spec
		if !runtimeName.MatchString(sp.Runtime) {
			ps.add(f, "spec.runtime", "must be a runtime name, got %q", sp.Runtime)
		}
		if !modelID.MatchString(sp.Model) {
			ps.add(f, "spec.model", "must be a model id, got %q", sp.Model)
		}
		ix.checkPins(a, ps)
		a.tools = ix.resolveGrants(a, ps)
		for i, wt := range sp.Subscribe.WorkItemTypes {
			if ix.workTypes[wt] == nil || slices.Index(sp.Subscribe.WorkItemTypes, wt) != i {
				ps.add(f, fmt.Sprintf("spec.subscribe.workItemTypes[%d]", i), "must name a work item type, once, got %q", wt)
			}
		}
		checkTokens(f, "spec.subscribe.taskTags", sp.Subscribe.TaskTags, ps)
	}
}

// checkPins checks that each pin names a released version, one per skill.
func (ix *index) checkPins(a *Agent, ps *problems) {
	pinned := map[string]bool{}
	for i, pin := range a.Spec.Skills {
		field := fmt.Sprintf("spec.skills[%d]", i)
		ref, err := ParseSkillRef(pin)
		switch {
		case err != nil:
			ps.add(a.file, field, "%v", err)
			continue
		case pinned[ref.Skill]:
			ps.add(a.file, field, "pins skill %q again; an agent pins one version of a skill", ref.Skill)
		case ix.skillVersion(ref) == nil:
			ps.add(a.file, field, "names no released version %s", ref)
		default:
			a.skills = append(a.skills, ref)
		}
		pinned[ref.Skill] = true
	}
}

// resolveGrants resolves an agent's grants against tool policy (spec Sections
// 3.1 and 3.7). A grant matches server and tool separately, and * matches only
// within a tool name. Patterns may resolve to read tools only, so write tools
// are granted by exact name, and no grant may resolve to a platform tool.
func (ix *index) resolveGrants(a *Agent, ps *problems) []GrantedTool {
	granted := map[ToolRef]GrantedTool{}
	for i, g := range a.Spec.Tools {
		field := fmt.Sprintf("spec.tools[%d]", i)
		server, pattern, ok := strings.Cut(g, ".")
		if !ok || !objectName.MatchString(server) || !grantPattern.MatchString(pattern) {
			ps.add(a.file, field, "must be server.tool or server.pattern, where * matches within a tool name, got %q", g)
			continue
		}
		s := ix.servers[server]
		if s == nil {
			ps.add(a.file, field, "names no registered server %q", server)
			continue
		}
		matched := false
		for _, tool := range sortedKeys(s.Spec.Tools) {
			t := s.Spec.Tools[tool]
			if t == nil || !matchPattern(pattern, tool) {
				continue
			}
			matched = true
			ref := ToolRef{Server: server, Tool: tool}
			switch {
			case t.Audience == AudiencePlatform:
				ps.add(a.file, field, "resolves to %s, a platform tool, which no agent may be granted", ref)
			case t.Effect == EffectWrite && strings.Contains(pattern, "*"):
				ps.add(a.file, field, "resolves to write tool %s; grant write tools by exact name", ref)
			default:
				granted[ref] = GrantedTool{Ref: ref, Effect: t.Effect, SchemaHash: t.SchemaHash}
			}
		}
		if !matched {
			ps.add(a.file, field, "resolves to no tool that server %q lists", server)
		}
	}
	tools := make([]GrantedTool, 0, len(granted))
	for _, t := range granted {
		tools = append(tools, t)
	}
	slices.SortFunc(tools, func(x, y GrantedTool) int {
		return cmp.Or(cmp.Compare(x.Ref.Server, y.Ref.Server), cmp.Compare(x.Ref.Tool, y.Ref.Tool))
	})
	return tools
}

// matchPattern matches a tool name against a pattern in which * matches any
// run of characters, including none.
func matchPattern(pattern, name string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == name
	}
	rest, ok := strings.CutPrefix(name, parts[0])
	if !ok {
		return false
	}
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, p)
		if i < 0 {
			return false
		}
		rest = rest[i+len(p):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}

// YankedPins returns the pins of an agent that the yank list withdraws. Such
// an agent is not invited to items that start under this revision, except
// in the invisible arm during a measurement window (spec Section 5.1).
func (r *Revision) YankedPins(agent string) []SkillRef {
	a := r.ix.agents[agent]
	if a == nil {
		return nil
	}
	var out []SkillRef
	for _, ref := range a.skills {
		if r.ix.yanked(ref) {
			out = append(out, ref)
		}
	}
	return out
}
