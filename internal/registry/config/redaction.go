package config

import (
	"fmt"
	"regexp"

	"github.com/jchen0426/agent-swarm/internal/registry/selector"
)

// checkRedactionRules compiles every rule. Patterns are RE2, so matching
// takes linear time, and a pattern that matches the empty string is refused,
// because it would match everywhere.
func (ix *index) checkRedactionRules(ps *problems) {
	for _, name := range sortedKeys(ix.redactions) {
		r := ix.redactions[name]
		if len(r.Spec.Patterns) == 0 && len(r.Spec.Paths) == 0 {
			ps.add(r.file, "spec", "needs at least one pattern or path")
		}
		for i, p := range r.Spec.Patterns {
			field := fmt.Sprintf("spec.patterns[%d]", i)
			re, err := regexp.Compile(p)
			switch {
			case err != nil:
				ps.add(r.file, field, "is not a valid regular expression: %v", err)
			case re.MatchString(""):
				ps.add(r.file, field, "matches the empty string")
			default:
				r.compiled.patterns = append(r.compiled.patterns, re)
			}
		}
		for i, p := range r.Spec.Paths {
			sel, err := selector.Parse(p)
			if err != nil {
				ps.add(r.file, fmt.Sprintf("spec.paths[%d]", i), "%v", err)
				continue
			}
			r.compiled.paths = append(r.compiled.paths, sel)
		}
	}
}

// checkRedactNames checks that every named rule exists, once.
func (ix *index) checkRedactNames(file, field string, names []string, ps *problems) {
	seen := map[string]bool{}
	for i, name := range names {
		switch {
		case seen[name]:
			ps.add(file, fmt.Sprintf("%s[%d]", field, i), "names %q twice", name)
		case ix.redactions[name] == nil:
			ps.add(file, fmt.Sprintf("%s[%d]", field, i), "names no redaction rule %q", name)
		}
		seen[name] = true
	}
}

// checkIdentitySources checks that each identity source names a listed read
// tool, and that no tool is registered twice.
func (ix *index) checkIdentitySources(ps *problems) {
	byTool := map[string]string{}
	for _, name := range sortedKeys(ix.identities) {
		s := ix.identities[name]
		ref, err := ParseToolRef(s.Spec.Tool)
		if err != nil {
			ps.add(s.file, "spec.tool", "%v", err)
			continue
		}
		if msg := ix.readTool(ref); msg != "" {
			ps.add(s.file, "spec.tool", "%s", msg)
		}
		if other, dup := byTool[ref.String()]; dup {
			ps.add(s.file, "spec.tool", "is already the identity source %q", other)
		}
		byTool[ref.String()] = name
	}
}

// identitySourceFor returns the identity source registered for a tool, or nil.
func (ix *index) identitySourceFor(ref ToolRef) *IdentitySource {
	for _, name := range sortedKeys(ix.identities) {
		if ix.identities[name].Spec.Tool == ref.String() {
			return ix.identities[name]
		}
	}
	return nil
}

// IdentitySource returns the identity source registered for a tool, or nil.
func (r *Revision) IdentitySource(ref ToolRef) *IdentitySource {
	return copied(r.ix.identitySourceFor(ref))
}
