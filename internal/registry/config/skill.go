package config

import (
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// commitID is a full or abbreviated Git commit id.
var commitID = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// maxReasonRunes caps a yank's reason.
const maxReasonRunes = 500

// checkSkills checks every skill's versions and their sources.
func (ix *index) checkSkills(ps *problems) {
	for _, name := range sortedKeys(ix.skills) {
		s := ix.skills[name]
		if len(s.Spec.Versions) == 0 {
			ps.add(s.file, "spec.versions", "lists no versions")
		}
		seen := map[string]bool{}
		for i, v := range s.Spec.Versions {
			field := fmt.Sprintf("spec.versions[%d]", i)
			if v == nil {
				ps.add(s.file, field, "is empty")
				continue
			}
			switch {
			case !versionLabel.MatchString(v.Version):
				ps.add(s.file, field+".version", "must be a version label such as 1.2.0, got %q", v.Version)
			case seen[v.Version]:
				ps.add(s.file, field+".version", "repeats version %q", v.Version)
			case v.digest == "":
				ps.add(s.file, field, "has no directory %s", path.Join(skillsDir, name, v.Version))
			}
			seen[v.Version] = true
			src := v.Source
			if src.Repository == "" || strings.ContainsFunc(src.Repository, isSpace) {
				ps.add(s.file, field+".source.repository", "must name where people edit the skill")
			}
			if !commitID.MatchString(src.Commit) {
				ps.add(s.file, field+".source.commit", "must be the commit the version snapshots, got %q", src.Commit)
			}
			if src.Path != "" && !fs.ValidPath(src.Path) {
				ps.add(s.file, field+".source.path", "must be a relative path within the source, got %q", src.Path)
			}
		}
	}
}

// scanSkills is the blocking scan of spec Section 3.2: a skill version whose
// files match a redaction rule's pattern is refused. The problem names the
// file and the rule, never the text that matched. A revision whose rules hold
// no pattern could scan nothing, so its skills are refused.
func (ix *index) scanSkills(ps *problems) {
	if !ix.anyPattern() {
		for _, name := range sortedKeys(ix.skills) {
			if s := ix.skills[name]; len(s.Spec.Versions) > 0 {
				ps.add(s.file, "spec.versions", "cannot pass the blocking scan of spec Section 3.2: no redaction rule has patterns")
			}
		}
		return
	}
	rules := sortedKeys(ix.redactions)
	for _, name := range sortedKeys(ix.skills) {
		for _, v := range ix.skills[name].Spec.Versions {
			if v == nil {
				continue
			}
			dir := path.Join(skillsDir, name, v.Version)
			for _, f := range v.files {
				data, err := fs.ReadFile(ix.fsys, path.Join(dir, f))
				if err != nil {
					continue // reported when the bundle was read
				}
				for _, rule := range rules {
					if ix.redactions[rule].matches(data) {
						ps.add(path.Join(dir, f), "", "matches redaction rule %q; skills hold no secrets or personal data", rule)
					}
				}
			}
		}
	}
}

// matches reports whether any of the rule's patterns matches data.
func (r *RedactionRule) matches(data []byte) bool {
	for _, re := range r.compiled.patterns {
		if re.Match(data) {
			return true
		}
	}
	return false
}

// checkYanks checks that each yank withdraws one released version, once, and
// records who, when and why.
func (ix *index) checkYanks(ps *problems) {
	if ix.yanks == nil {
		return
	}
	seen := map[string]bool{}
	for i, y := range ix.yanks.Yanks {
		field := fmt.Sprintf("yanks[%d]", i)
		ref, err := ParseSkillRef(y.Skill)
		switch {
		case err != nil:
			ps.add(yanksFile, field+".skill", "%v", err)
		case ix.skillVersion(ref) == nil:
			ps.add(yanksFile, field+".skill", "names no released version %s", ref)
		case seen[ref.String()]:
			ps.add(yanksFile, field+".skill", "yanks %s twice", ref)
		}
		seen[y.Skill] = true
		if err := checkOwner(y.By, true); err != nil {
			ps.add(yanksFile, field+".by", "%v", err)
		}
		if y.At.IsZero() {
			ps.add(yanksFile, field+".at", "is required, as an RFC 3339 time")
		}
		if strings.TrimSpace(y.Reason) == "" || utf8.RuneCountInString(y.Reason) > maxReasonRunes {
			ps.add(yanksFile, field+".reason", "is required, in at most %d characters", maxReasonRunes)
		}
	}
}

// yanked reports whether the yank list withdraws ref.
func (ix *index) yanked(ref SkillRef) bool {
	if ix.yanks == nil {
		return false
	}
	for _, y := range ix.yanks.Yanks {
		if y.Skill == ref.String() {
			return true
		}
	}
	return false
}

// Yanked reports whether the yank list withdraws a skill version.
func (r *Revision) Yanked(ref SkillRef) bool { return r.ix.yanked(ref) }

// anyPattern reports whether any redaction rule has a compiled pattern.
func (ix *index) anyPattern() bool {
	for _, r := range ix.redactions {
		if len(r.compiled.patterns) > 0 {
			return true
		}
	}
	return false
}
