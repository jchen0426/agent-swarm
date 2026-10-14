package config_test

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

const skillYAML = "skills/release-diagnosis/skill.yaml"

func TestEveryDeclaredVersionHasItsDirectory(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"skills/release-diagnosis/1.2.0/SKILL.md":           nil,
		"skills/release-diagnosis/1.2.0/reference/flags.md": nil,
	})

	ps := refused(t, repo)

	assertProblem(t, ps, skillYAML, "spec.versions[1]", "has no directory skills/release-diagnosis/1.2.0")
}

func TestVersionsAndTheirSourcesAreChecked(t *testing.T) {
	repo := edit(t, exampleRepo(), skillYAML, "    - version: 1.2.0\n      source: {repository: https://git.example.com/runbooks, commit: 9b8e7d6, path: release}\n",
		"    - version: 1.1.0\n      source: {repository: '', commit: main, path: ../release}\n")

	ps := refused(t, repo)

	assertProblem(t, ps, skillYAML, "spec.versions[1].version", `repeats version "1.1.0"`)
	assertProblem(t, ps, skillYAML, "spec.versions[1].source.repository", "where people edit the skill")
	assertProblem(t, ps, skillYAML, "spec.versions[1].source.commit", `got "main"`)
	assertProblem(t, ps, skillYAML, "spec.versions[1].source.path", "relative path")
}

func TestASkillWithASecretIsRefusedWithoutRepeatingIt(t *testing.T) {
	secret := "AKIAIOSFODNN7EXAMPLE"
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"skills/release-diagnosis/1.2.0/reference/creds.md": file("Use key " + secret + " to read the bucket.\n"),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "skills/release-diagnosis/1.2.0/reference/creds.md", "", `matches redaction rule "default-secrets"`)
	assert.NotContains(t, ps.Error(), secret)
	for _, p := range ps {
		assert.NotContains(t, p.Message, secret)
	}
}

func TestAYankWithdrawsOneReleasedVersion(t *testing.T) {
	repo := edit(t, exampleRepo(), "yanks.yaml", "    reason: Its rollback advice ignored flag changes.\n",
		"    reason: Its rollback advice ignored flag changes.\n"+
			"  - {skill: release-diagnosis@1.0.0, by: alice@example.com, at: 2026-10-14T10:00:00Z, reason: typo}\n"+
			"  - {skill: release-diagnosis@1.1.0, by: admins, at: 2026-10-14T10:00:00Z, reason: ''}\n"+
			"  - {skill: release-diagnosis, by: bob@example.com, reason: no time}\n")

	ps := refused(t, repo)

	assertProblem(t, ps, "yanks.yaml", "yanks[1].skill", "no released version release-diagnosis@1.0.0")
	assertProblem(t, ps, "yanks.yaml", "yanks[2].skill", "yanks release-diagnosis@1.1.0 twice")
	assertProblem(t, ps, "yanks.yaml", "yanks[2].by", "a person's email address")
	assertProblem(t, ps, "yanks.yaml", "yanks[2].reason", "is required")
	assertProblem(t, ps, "yanks.yaml", "yanks[3].skill", "must be skill@version")
	assertProblem(t, ps, "yanks.yaml", "yanks[3].at", "RFC 3339")
}

func TestTheYankListWithdrawsVersions(t *testing.T) {
	rev := build(t, exampleRepo())

	assert.True(t, rev.Yanked(config.SkillRef{Skill: "release-diagnosis", Version: "1.1.0"}))
	assert.False(t, rev.Yanked(config.SkillRef{Skill: "release-diagnosis", Version: "1.2.0"}))
}

func TestSkillsCannotLoadWhenNoRuleCouldScanThem(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"redaction-rules/default-secrets.yaml":              nil,
		"skills/release-diagnosis/1.2.0/reference/creds.md": file("-----BEGIN RSA PRIVATE KEY-----\nAKIAIOSFODNN7EXAMPLE\n"),
	})
	repo = edit(t, repo, rm, "      redact: [default-secrets]\n", "")
	repo = edit(t, repo, alerts, "  redact: [default-secrets]\n", "")

	assertProblem(t, refused(t, repo), skillYAML, "spec.versions", "no redaction rule has patterns")

	pathsOnly := with(repo, map[string]*fstest.MapFile{
		"redaction-rules/env.yaml": file("kind: RedactionRule\nmetadata: {name: env, owner: security@example.com}\nspec: {paths: ['$..env[*].value']}\n"),
	})
	assertProblem(t, refused(t, pathsOnly), skillYAML, "spec.versions", "no redaction rule has patterns")
}
