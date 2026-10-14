package config

import (
	"io/fs"
	"maps"
	"path"
	"slices"
)

// Revision is one configuration revision that keeps every rule. It never
// changes once built: every work item that locked it shares it, so its
// accessors return copies.
type Revision struct {
	commit string
	digest string
	ix     *index
}

// Build reads the configuration in fsys, the files of the given commit, and
// checks every rule of spec Sections 3.1 to 3.7. It returns the revision, or
// Problems listing every reason the revision is refused.
func Build(fsys fs.FS, commit string, cc ConditionChecker) (*Revision, error) {
	ps := &problems{}
	ix := read(fsys, ps)
	ix.check(cc, ps)
	if list := ps.sorted(); list != nil {
		return nil, list
	}
	return &Revision{commit: commit, digest: ix.versions(), ix: ix}, nil
}

// check runs the rules. Each adds the problems it finds; later rules use what
// earlier ones resolved.
func (ix *index) check(cc ConditionChecker, ps *problems) {
	ix.checkRedactionRules(ps)
	ix.checkServers(cc, ps)
	ix.checkIdentitySources(ps)
	ix.checkSkills(ps)
	ix.scanSkills(ps)
	ix.checkYanks(ps)
	ix.checkWorkItemTypes(ps)
	ix.checkTriggerMappings(cc, ps)
	ix.checkAgents(ps)
}

// Commit returns the commit the revision was read from.
func (r *Revision) Commit() string { return r.commit }

// Digest returns the SHA-256 of every configuration file and skill bundle in
// the revision, as sha256:<hex>. Equal content gives an equal digest, however
// it was read.
func (r *Revision) Digest() string { return r.digest }

// Agents returns every agent in configuration order: by name.
func (r *Revision) Agents() []*Agent { return copyEach(sortedValues(r.ix.agents)) }

// Agent returns the named agent, or nil.
func (r *Revision) Agent(name string) *Agent { return copyOf(r.ix.agents[name]) }

// Server returns the named server registration, or nil.
func (r *Revision) Server(name string) *McpServer { return copyOf(r.ix.servers[name]) }

// Tool returns the policy of a listed tool, or nil.
func (r *Revision) Tool(ref ToolRef) *ToolPolicy {
	s := r.ix.servers[ref.Server]
	if s == nil {
		return nil
	}
	return copyOf(s.Spec.Tools[ref.Tool])
}

// Skill returns the named skill, or nil.
func (r *Revision) Skill(name string) *Skill { return copyOf(r.ix.skills[name]) }

// SkillVersion returns a released skill version, or nil.
func (r *Revision) SkillVersion(ref SkillRef) *SkillVersion { return copyOf(r.ix.skillVersion(ref)) }

// SkillBundle returns the files of a released skill version.
func (r *Revision) SkillBundle(ref SkillRef) (fs.FS, error) {
	if r.ix.skillVersion(ref) == nil {
		return nil, fs.ErrNotExist
	}
	return fs.Sub(r.ix.fsys, path.Join(skillsDir, ref.Skill, ref.Version))
}

// WorkItemType returns the named work item type, or nil.
func (r *Revision) WorkItemType(name string) *WorkItemType { return copyOf(r.ix.workTypes[name]) }

// TriggerMapping returns the mapping of an inbound source, or nil.
func (r *Revision) TriggerMapping(source string) *TriggerMapping {
	for _, m := range sortedValues(r.ix.mappings) {
		if m.Spec.Source == source {
			return copyOf(m)
		}
	}
	return nil
}

// RedactionRule returns the named redaction rule, or nil.
func (r *Revision) RedactionRule(name string) *RedactionRule { return copyOf(r.ix.redactions[name]) }

func sortedKeys[V any](m map[string]V) []string { return slices.Sorted(maps.Keys(m)) }

func sortedValues[V any](m map[string]V) []V {
	out := make([]V, 0, len(m))
	for _, k := range sortedKeys(m) {
		out = append(out, m[k])
	}
	return out
}
