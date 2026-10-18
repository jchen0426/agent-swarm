package config

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/jchen0426/agent-swarm/internal/registry/selector"
)

// SessionTokenReplacement is what the LLM gateway puts in place of a
// session's token, wherever a request holds it. No rule that applies always
// may change it.
const SessionTokenReplacement = "[redacted:session-token]" //nolint:gosec // G101: what replaces a token, not one

// toolCallIDs are 10,000 ids in the model vendor's form for a tool call,
// which a request carries back with each call and its result: "toolu_01"
// and 22 letters and digits, drawn from a fixed seed. A sample of random ids
// can miss a run of one character class, such as eight digits, that a
// pattern would match in a real id, so five more follow, one for each class
// (digits; lowercase hex; uppercase hex; lowercase letters; uppercase
// letters), each made of that class alone.
var toolCallIDs = sync.OnceValue(func() []string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	rng := rand.New(rand.NewPCG(6, 2)) //nolint:gosec // G404: a fixed sample, not a secret
	ids := make([]string, 10_000)
	for i := range ids {
		b := []byte("toolu_01")
		for range 22 {
			b = append(b, alphabet[rng.IntN(len(alphabet))])
		}
		ids[i] = string(b)
	}
	classes := []string{
		"0123456789",
		"0123456789abcdef",
		"0123456789ABCDEF",
		"abcdefghijklmnopqrstuvwxyz",
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	}
	for _, class := range classes {
		b := []byte("toolu_01")
		for i := range 22 {
			b = append(b, class[i%len(class)])
		}
		ids = append(ids, string(b))
	}
	return ids
})

// idHits remembers, for each pattern, a tool-call id it matches, or "".
var idHits sync.Map

// matchesToolCallID returns a tool-call id the pattern matches, or "".
func matchesToolCallID(re *regexp.Regexp) string {
	if hit, ok := idHits.Load(re.String()); ok {
		return hit.(string)
	}
	hit := ""
	if i := slices.IndexFunc(toolCallIDs(), re.MatchString); i >= 0 {
		hit = toolCallIDs()[i]
	}
	idHits.Store(re.String(), hit)
	return hit
}

// checkRedactionRules compiles every rule. Patterns are RE2, so matching
// takes linear time, and a pattern that matches the empty string is refused,
// because it would match everywhere. A pattern that holds a control character
// is refused too: a double-quoted YAML string writes an escape such as \b as
// one, and the rule would match nothing.
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
			case strings.ContainsFunc(p, unicode.IsControl):
				ps.add(r.file, field, "holds a control character, as YAML writes \\b and other escapes in a double-quoted string: write the pattern in single quotes")
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
		if r.Spec.Always && len(r.Spec.Paths) > 0 {
			ps.add(r.file, "spec.paths", "a rule that applies always holds patterns only; paths are written for one tool's output")
		}
		if strings.ContainsAny(r.Replacement(), `"'\`) {
			ps.add(r.file, "spec.replacement", "may hold no quote and no backslash: redacted again, it would be read as a member or an escape")
		}
	}
	ix.checkReplacements(ps)
	ix.checkNamesSurvive(ps)
	ix.checkAlwaysRules(ps)
}

// checkReplacements refuses a pattern of a rule that applies always when it
// changes a replacement: any rule's, or the LLM gateway's for a session's
// token. These rules redact again what was redacted once (the LLM gateway
// redacts a tool's output the MCP gateway redacted, before the model reads
// it), and that must change nothing: a model that quotes a tool's output
// quotes it as it was stored. A pattern that turns a replacement into itself
// changes nothing. A replacement is checked on its own, not with the text
// around it.
func (ix *index) checkReplacements(ps *problems) {
	type replacement struct{ text, of string }
	replacements := []replacement{{SessionTokenReplacement, "the LLM gateway's session tokens"}}
	for _, name := range sortedKeys(ix.redactions) {
		replacements = append(replacements, replacement{ix.redactions[name].Replacement(), "rule " + name})
	}
	for _, r := range ix.alwaysRules() {
		for i, p := range r.Spec.Patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				continue
			}
			for _, rp := range replacements {
				if re.ReplaceAllLiteralString(rp.text, r.Replacement()) != rp.text {
					ps.add(r.file, fmt.Sprintf("spec.patterns[%d]", i),
						"changes the replacement %q, of %s: what was redacted once would change when redacted again", rp.text, rp.of)
				}
			}
		}
	}
}

// checkNamesSurvive refuses a pattern of a rule that applies always when it
// matches what the LLM gateway forwards as written: a tool as agents name
// it, a server's or a board tool, or a tool call's id in the vendor's form.
// The gateway refuses a request whose id or name holds what a rule hides,
// so such a pattern would refuse the turns that call a tool.
func (ix *index) checkNamesSurvive(ps *problems) {
	names := slices.Clone(BoardTools)
	for _, server := range sortedKeys(ix.servers) {
		for _, tool := range sortedKeys(ix.servers[server].Spec.Tools) {
			names = append(names, ToolRef{Server: server, Tool: tool}.AgentName())
		}
	}
	for _, r := range ix.alwaysRules() {
		for i, p := range r.Spec.Patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				continue
			}
			field := fmt.Sprintf("spec.patterns[%d]", i)
			if j := slices.IndexFunc(names, re.MatchString); j >= 0 {
				ps.add(r.file, field, "matches the tool name %q, which the LLM gateway forwards as written: every turn that offers the tool would be refused", names[j])
			} else if id := matchesToolCallID(re); id != "" {
				ps.add(r.file, field, "matches %q, a tool call's id in the vendor's form, which the LLM gateway forwards as written: turns that call tools would be refused; anchor the pattern, for example on word boundaries (\\b, in single quotes), and keep _ out of what it matches", id)
			}
		}
	}
}

// checkAlwaysRules requires a rule that applies always, with patterns, once
// any server is registered: everything that reaches an agent, a store or the
// model passes the deployment's redaction rules (spec Section 9.4), also the
// output of a tool that names none.
func (ix *index) checkAlwaysRules(ps *problems) {
	if len(ix.servers) == 0 || len(ix.alwaysRules()) > 0 {
		return
	}
	first := ix.servers[sortedKeys(ix.servers)[0]]
	ps.add(first.file, "spec", "no redaction rule applies always; mark a rule of secret patterns always: true (spec Section 9.4)")
}

// alwaysRules returns the rules that apply always and have patterns, by name.
func (ix *index) alwaysRules() []*RedactionRule {
	var out []*RedactionRule
	for _, name := range sortedKeys(ix.redactions) {
		if r := ix.redactions[name]; r.Spec.Always && len(r.compiled.patterns) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// AlwaysRedactionRules returns the redaction rules that apply to everything
// the gateway stores or shows, sorted by name.
func (r *Revision) AlwaysRedactionRules() []*RedactionRule { return copyEach(r.ix.alwaysRules()) }

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
