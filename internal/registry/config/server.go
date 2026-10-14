package config

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/registry/selector"
	"github.com/jchen0426/agent-swarm/internal/registry/tmpl"
)

// Formats of tool policy.
var (
	// argName is a tool argument's name, a fact's name or a key field's name.
	argName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	// digest is a SHA-256 digest, as images and tool schema hashes are pinned.
	digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// secretRef points into the platform's secret store.
	secretRef = regexp.MustCompile(`^secret://[A-Za-z0-9._/-]+$`)
)

// checkServers checks every server registration and its tool policy (spec
// Section 3.3).
func (ix *index) checkServers(cc ConditionChecker, ps *problems) {
	for _, name := range sortedKeys(ix.servers) {
		s := ix.servers[name]
		f := s.file
		switch u, err := url.Parse(s.Spec.Endpoint); {
		case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
			ps.add(f, "spec.endpoint", "must be an http or https URL")
		case u.User != nil:
			ps.add(f, "spec.endpoint", "must not hold credentials; the server's credential goes in credentialRef")
		}
		if !digest.MatchString(s.Spec.Image) {
			ps.add(f, "spec.image", "must pin the image by digest, as sha256:<64 hex digits>, got %q", s.Spec.Image)
		}
		if ref := s.Spec.CredentialRef; ref != "" && !secretRef.MatchString(ref) {
			ps.add(f, "spec.credentialRef", "must be secret://<path>; the value is not shown, in case it is the secret itself")
		}
		if len(s.Spec.Tools) == 0 {
			ps.add(f, "spec.tools", "lists no tools; unlisted tools are denied")
		}
		for _, tool := range sortedKeys(s.Spec.Tools) {
			ix.checkTool(s, tool, s.Spec.Tools[tool], cc, ps)
		}
	}
}

func (ix *index) checkTool(s *McpServer, tool string, t *ToolPolicy, cc ConditionChecker, ps *problems) {
	f, field := s.file, "spec.tools."+tool
	if !toolName.MatchString(tool) {
		ps.add(f, field, "is not a valid tool name")
	}
	if t == nil {
		ps.add(f, field, "has no policy")
		return
	}
	if !digest.MatchString(t.SchemaHash) {
		ps.add(f, field+".schemaHash", "must be sha256:<64 hex digits>, as agentswarm registry hash-tools prints it, got %q", t.SchemaHash)
	}
	switch t.Audience {
	case "", AudienceAgents, AudiencePlatform:
	default:
		ps.add(f, field+".audience", "must be %q or %q, got %q", AudienceAgents, AudiencePlatform, t.Audience)
	}
	ix.checkRedactNames(f, field+".redact", t.Redact, ps)
	switch t.Effect {
	case EffectRead:
		ix.checkReadPolicy(f, field, t, cc, ps)
	case EffectWrite:
		ix.checkWritePolicy(s, field, t, cc, ps)
	default:
		ps.add(f, field+".effect", "must be %q or %q, got %q", EffectRead, EffectWrite, t.Effect)
	}
}

func (ix *index) checkReadPolicy(f, field string, t *ToolPolicy, cc ConditionChecker, ps *problems) {
	for _, w := range []struct {
		set  bool
		name string
	}{
		{len(t.Target) > 0, "target"}, {len(t.Scope) > 0, "scope"}, {len(t.Candidates) > 0, "candidates"},
		{t.Summary != "", "summary"}, {len(t.Facts) > 0, "facts"},
	} {
		if w.set {
			ps.add(f, field+"."+w.name, "applies to write tools only")
		}
	}
	if t.PartialWhen != "" {
		if err := cc.Check(EnvPartialWhen, t.PartialWhen); err != nil {
			ps.add(f, field+".partialWhen", "%v", err)
		}
	}
	for _, arg := range sortedKeys(t.Bounds) {
		if !argName.MatchString(arg) {
			ps.add(f, field+".bounds."+arg, "is not a valid argument name")
		}
		if err := checkBound(t.Bounds[arg].Max); err != nil {
			ps.add(f, field+".bounds."+arg+".max", "%v", err)
		}
	}
}

// checkBound checks a cap: a positive duration, or a positive number. NaN and
// infinity are refused: no comparison with NaN holds, so it would never refuse
// a call, and infinity caps nothing.
func checkBound(limit string) error {
	if d, err := time.ParseDuration(limit); err == nil {
		if d <= 0 {
			return fmt.Errorf("must be positive, got %q", limit)
		}
		return nil
	}
	n, err := strconv.ParseFloat(limit, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return fmt.Errorf("must be a positive number or duration, got %q", limit)
	}
	return nil
}

// checkWritePolicy applies the loader rules of spec Section 3.3: a write tool
// needs scope and summary, every target argument needs candidates, and reads
// for candidates and facts take only arguments that have candidates.
func (ix *index) checkWritePolicy(s *McpServer, field string, t *ToolPolicy, cc ConditionChecker, ps *problems) {
	f, home := s.file, s.Metadata.Name
	checkReadFieldsUnset(f, field, t, ps)
	if len(t.Scope) == 0 {
		ps.add(f, field+".scope", "is required for a write tool")
	}
	for _, arg := range sortedKeys(t.Scope) {
		if !argName.MatchString(arg) {
			ps.add(f, field+".scope."+arg, "is not a valid argument name")
		}
		if err := cc.Check(EnvScope, t.Scope[arg]); err != nil {
			ps.add(f, field+".scope."+arg, "%v", err)
		}
	}
	validated := func(hole string) string {
		if arg, ok := strings.CutPrefix(hole, "args."); ok && t.Candidates[arg] != nil {
			return ""
		}
		return fmt.Sprintf("uses {%s}; read arguments take only arguments that have candidates", hole)
	}
	for _, arg := range sortedKeys(t.Candidates) {
		if !argName.MatchString(arg) {
			ps.add(f, field+".candidates."+arg, "is not a valid argument name")
		}
		ix.checkRead(f, field+".candidates."+arg, home, t.Candidates[arg], validated, ps)
	}
	if cycle := candidateCycle(t); cycle != "" {
		ps.add(f, field+".candidates", "depend on each other: %s", cycle)
	}
	for i, arg := range t.Target {
		if t.Candidates[arg] == nil {
			ps.add(f, fmt.Sprintf("%s.target[%d]", field, i), "names argument %q, which has no candidates", arg)
		}
	}
	for _, fact := range sortedKeys(t.Facts) {
		if !argName.MatchString(fact) {
			ps.add(f, field+".facts."+fact, "is not a valid fact name")
		}
		ix.checkRead(f, field+".facts."+fact, home, t.Facts[fact], validated, ps)
	}
	ix.checkSummary(f, field, t, ps)
}

// checkReadFieldsUnset refuses the fields that only read tools have.
func checkReadFieldsUnset(f, field string, t *ToolPolicy, ps *problems) {
	if t.Audience == AudiencePlatform {
		ps.add(f, field+".audience", "only read tools serve the platform")
	}
	if t.PartialWhen != "" {
		ps.add(f, field+".partialWhen", "applies to read tools only")
	}
	if len(t.Bounds) > 0 {
		ps.add(f, field+".bounds", "applies to read tools only")
	}
}

// checkSummary checks that a write tool's summary shows only arguments that
// have candidates, and facts.
func (ix *index) checkSummary(f, field string, t *ToolPolicy, ps *problems) {
	if t.Summary == "" {
		ps.add(f, field+".summary", "is required for a write tool")
		return
	}
	tpl, err := tmpl.Parse(t.Summary)
	if err != nil {
		ps.add(f, field+".summary", "%v", err)
		return
	}
	for _, hole := range tpl.Names() {
		arg, isArg := strings.CutPrefix(hole, "args.")
		fact, isFact := strings.CutPrefix(hole, "facts.")
		if (isArg && t.Candidates[arg] != nil) || (isFact && t.Facts[fact] != nil) {
			continue
		}
		ps.add(f, field+".summary", "uses {%s}; a summary shows only arguments that have candidates, and facts", hole)
	}
}

// candidateCycle returns a cycle among candidate reads that use each other's
// arguments, such as "a -> b -> a", or "" when there is none.
func candidateCycle(t *ToolPolicy) string {
	deps := map[string][]string{}
	for _, arg := range sortedKeys(t.Candidates) {
		r := t.Candidates[arg]
		if r == nil {
			continue
		}
		for _, name := range sortedKeys(r.Args) {
			tpl, err := tmpl.Parse(r.Args[name])
			if err != nil {
				continue
			}
			for _, hole := range tpl.Names() {
				if dep, ok := strings.CutPrefix(hole, "args."); ok && t.Candidates[dep] != nil {
					deps[arg] = append(deps[arg], dep)
				}
			}
		}
	}
	state := map[string]int{} // 1 visiting, 2 done
	var path []string
	var visit func(string) string
	visit = func(arg string) string {
		state[arg] = 1
		path = append(path, arg)
		for _, dep := range deps[arg] {
			switch state[dep] {
			case 1:
				i := indexOf(path, dep)
				return strings.Join(append(append([]string(nil), path[i:]...), dep), " -> ")
			case 0:
				if c := visit(dep); c != "" {
					return c
				}
			}
		}
		path = path[:len(path)-1]
		state[arg] = 2
		return ""
	}
	for _, arg := range sortedKeys(deps) {
		if state[arg] == 0 {
			if c := visit(arg); c != "" {
				return c
			}
		}
	}
	return ""
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

// checkRead checks a platform read. A bare tool name is a tool of the server
// named home; outside a server, home is empty and the tool needs its server.
// hole returns why a template hole in the read's arguments is not allowed,
// or "".
func (ix *index) checkRead(file, field, home string, r *PlatformRead, hole func(string) string, ps *problems) {
	if r == nil {
		ps.add(file, field, "is empty")
		return
	}
	if ref, err := resolveRef(r.Tool, home); err != nil {
		ps.add(file, field+".tool", "%v", err)
	} else if msg := ix.readTool(ref); msg != "" {
		ps.add(file, field+".tool", "%s", msg)
	}
	if _, err := selector.Parse(r.Field); err != nil {
		ps.add(file, field+".field", "%v", err)
	}
	for _, arg := range sortedKeys(r.Args) {
		if !argName.MatchString(arg) {
			ps.add(file, field+".args."+arg, "is not a valid argument name")
		}
		tpl, err := tmpl.Parse(r.Args[arg])
		if err != nil {
			ps.add(file, field+".args."+arg, "%v", err)
			continue
		}
		for _, h := range tpl.Names() {
			if msg := hole(h); msg != "" {
				ps.add(file, field+".args."+arg, "%s", msg)
			}
		}
	}
}

// resolveRef resolves a read's tool name against the server it is written in.
func resolveRef(tool, home string) (ToolRef, error) {
	if home != "" && !strings.Contains(tool, ".") {
		if !toolName.MatchString(tool) {
			return ToolRef{}, fmt.Errorf("must be a tool name or server.tool, got %q", tool)
		}
		return ToolRef{Server: home, Tool: tool}, nil
	}
	return ParseToolRef(tool)
}

// readTool returns why ref is not a listed read tool, or "".
func (ix *index) readTool(ref ToolRef) string {
	s := ix.servers[ref.Server]
	if s == nil {
		return fmt.Sprintf("names no registered server %q", ref.Server)
	}
	t := s.Spec.Tools[ref.Tool]
	switch {
	case t == nil:
		return fmt.Sprintf("names tool %q, which server %q does not list", ref.Tool, ref.Server)
	case t.Effect != EffectRead:
		return fmt.Sprintf("names %s, which is not a read tool", ref)
	}
	return ""
}
