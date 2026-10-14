package config

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
)

// KindSkillVersion marks a skill version in the records of a revision.
const KindSkillVersion Kind = "SkillVersion"

// Object is one recorded object of a revision: what it is, who owns it, and
// the hashes that pin it (spec Section 3.7, "content hashes recorded").
type Object struct {
	Kind  Kind   `json:"kind"`
	Name  string `json:"name"`
	Owner string `json:"owner,omitempty"`
	File  string `json:"file"`
	Hash  string `json:"hash"`
	// Version is an agent's version, or a skill version's digest.
	Version string `json:"version,omitempty"`
}

// Lock is what a work item locks when it starts and keeps until it closes
// (spec Section 3): the commit, the revision's digest, and the image of every
// runtime the revision uses.
type Lock struct {
	Commit        string            `json:"commit"`
	Digest        string            `json:"digest"`
	RuntimeImages map[string]string `json:"runtimeImages"`
}

// versions computes every agent's version and returns the revision's digest.
func (ix *index) versions() string {
	for _, a := range ix.agents {
		a.version = ix.agentVersion(a)
	}
	var b strings.Builder
	for _, file := range sortedKeys(ix.files) {
		_, _ = fmt.Fprintf(&b, "file %s %s\n", file, ix.files[file])
	}
	for _, name := range sortedKeys(ix.skills) {
		for _, v := range ix.skills[name].Spec.Versions {
			_, _ = fmt.Fprintf(&b, "bundle %s %s\n", path.Join(skillsDir, name, v.Version), v.digest)
		}
	}
	return hashBytes([]byte(b.String()))
}

// agentVersion hashes what an agent's version covers (spec Section 3.1): its
// file, the skill versions it pins and the tools its grants resolve to. A
// newly listed tool that a pattern matches is therefore a new version.
func (ix *index) agentVersion(a *Agent) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "agent %s\n", a.hash)
	pins := slices.SortedFunc(slices.Values(a.skills), func(x, y SkillRef) int { return strings.Compare(x.String(), y.String()) })
	for _, ref := range pins {
		_, _ = fmt.Fprintf(&b, "skill %s %s\n", ref, ix.skillVersion(ref).digest)
	}
	for _, t := range a.tools {
		_, _ = fmt.Fprintf(&b, "tool %s %s %s\n", t.Ref, t.Effect, t.SchemaHash)
	}
	return hashBytes([]byte(b.String()))
}

// Objects returns the revision's objects and skill versions, sorted by kind
// and name, for the record of the revision.
func (r *Revision) Objects() []Object {
	var out []Object
	addAll := func(kind Kind, objs map[string]header) {
		for _, name := range sortedKeys(objs) {
			_, md := objs[name].head()
			o := Object{Kind: kind, Name: name, Owner: md.Owner}
			o.File, o.Hash = originOf(objs[name])
			out = append(out, o)
		}
	}
	addAll(KindAgent, headers(r.ix.agents))
	addAll(KindIdentitySource, headers(r.ix.identities))
	addAll(KindMcpServer, headers(r.ix.servers))
	addAll(KindRedactionRule, headers(r.ix.redactions))
	addAll(KindSkill, headers(r.ix.skills))
	addAll(KindTriggerMapping, headers(r.ix.mappings))
	addAll(KindWorkItemType, headers(r.ix.workTypes))
	for i := range out {
		if out[i].Kind == KindAgent {
			out[i].Version = r.ix.agents[out[i].Name].version
		}
	}
	for _, name := range sortedKeys(r.ix.skills) {
		s := r.ix.skills[name]
		for _, v := range s.Spec.Versions {
			ref := SkillRef{Skill: name, Version: v.Version}
			out = append(out, Object{
				Kind: KindSkillVersion, Name: ref.String(), Owner: s.Metadata.Owner,
				File: path.Join(skillsDir, name, v.Version), Hash: v.digest, Version: v.digest,
			})
		}
	}
	if y := r.ix.yanks; y != nil {
		out = append(out, Object{Kind: KindYankList, Name: "yanks", File: y.file, Hash: y.hash})
	}
	return out
}

func headers[T header](m map[string]T) map[string]header {
	out := make(map[string]header, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// originOf returns an object's file and hash.
func originOf(h header) (file, hash string) {
	type origined interface {
		File() string
		Hash() string
	}
	o := h.(origined)
	return o.File(), o.Hash()
}

// Lock returns the lock of this revision. images maps runtime names to image
// digests, as the deployment pins them; every runtime that an agent or a
// shadow run names needs one.
func (r *Revision) Lock(images map[string]string) (Lock, error) {
	used := map[string]string{}
	need := func(runtime, who string) error {
		img, ok := images[runtime]
		if !ok || !digest.MatchString(img) {
			return fmt.Errorf("config: runtime %q of %s has no image pinned as sha256:<64 hex digits>", runtime, who)
		}
		used[runtime] = img
		return nil
	}
	for _, a := range sortedValues(r.ix.agents) {
		if err := need(a.Spec.Runtime, "agent "+a.Metadata.Name); err != nil {
			return Lock{}, err
		}
	}
	for _, name := range sortedKeys(r.ix.workTypes) {
		for _, run := range r.ix.workTypes[name].settings.ShadowRuns {
			if run.Runtime == "" {
				continue
			}
			if err := need(run.Runtime, "shadow run "+name+"/"+run.Name); err != nil {
				return Lock{}, err
			}
		}
	}
	return Lock{Commit: r.commit, Digest: r.digest, RuntimeImages: maps.Clone(used)}, nil
}
