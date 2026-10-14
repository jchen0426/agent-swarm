package config_test

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

func TestTheExampleRepositoryKeepsEveryRule(t *testing.T) {
	rev := build(t, exampleRepo())

	assert.Equal(t, "c0ffee0", rev.Commit())
	require.Len(t, rev.Agents(), 1)
	assert.Equal(t, "release-investigator", rev.Agents()[0].Metadata.Name)
	assert.NotNil(t, rev.Server("release-manager"))
	assert.NotNil(t, rev.WorkItemType("incident"))
	assert.NotNil(t, rev.TriggerMapping("alerting"))
	assert.Nil(t, rev.TriggerMapping("pager"))
}

func TestAnEmptyRepositoryIsAValidRevision(t *testing.T) {
	rev := build(t, fstest.MapFS{})

	assert.Empty(t, rev.Agents())
}

func TestUnknownFieldsAreRefusedNotIgnored(t *testing.T) {
	repo := edit(t, exampleRepo(), "servers/release-manager.yaml", "partialWhen:", "partialwhen:")

	ps := refused(t, repo)

	assertProblem(t, ps, "servers/release-manager.yaml", "", "field partialwhen not found")
	assertProblem(t, ps, "servers/release-manager.yaml", "", "line 11")
}

func TestRepeatedKeysAndExtraDocumentsAreRefused(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"redaction-rules/twice.yaml": file("kind: RedactionRule\nkind: RedactionRule\n"),
		"redaction-rules/two-docs.yaml": file("kind: RedactionRule\nmetadata: {name: two-docs, owner: a@example.com}\n" +
			"spec: {patterns: [x]}\n---\nkind: RedactionRule\n"),
		"redaction-rules/empty.yaml": file(""),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "redaction-rules/twice.yaml", "", `mapping key "kind" already defined`)
	assertProblem(t, ps, "redaction-rules/two-docs.yaml", "", "exactly one YAML document")
	assertProblem(t, ps, "redaction-rules/empty.yaml", "", "the file is empty")
}

func TestAnObjectsNameAndKindMatchItsFile(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"agents/other-name.yaml": exampleRepo()["agents/release-investigator.yaml"],
		"agents/a-server.yaml":   file("kind: McpServer\nmetadata: {name: a-server, owner: platform-team}\nspec: {}\n"),
		"agents/Upper.yaml":      file("kind: Agent\nmetadata: {name: Upper, owner: a@example.com}\nspec: {}\n"),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "agents/other-name.yaml", "metadata.name", `must match the file name "other-name"`)
	assertProblem(t, ps, "agents/a-server.yaml", "kind", "must be Agent in this directory")
	assertProblem(t, ps, "agents/Upper.yaml", "", "lowercase DNS label")
}

func TestNothingThatLooksLikeConfigurationIsSilentlyIgnored(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"agent.yaml":                 file("kind: Agent\n"),
		"agent/x.yaml":               file("kind: Agent\n"),
		"agents/x.yml":               file("kind: Agent\n"),
		"agents/nested/x.yaml":       file("kind: Agent\n"),
		".github/workflows/ci.yaml":  file("on: push\n"),
		"CODEOWNERS":                 file("* @sre-leads\n"),
		"servers/notes.txt":          file("notes\n"),
		"skills/stray.yaml":          file("kind: Skill\n"),
		"skills/release-diagnosis/9": file("not a version\n"),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "agent.yaml", "", "objects go in their kind's directory")
	assertProblem(t, ps, "agent", "", "is not a configuration directory")
	assertProblem(t, ps, "agents/x.yml", "", "<name>.yaml")
	assertProblem(t, ps, "agents/nested", "", "<name>.yaml")
	assertProblem(t, ps, "servers/notes.txt", "", "<name>.yaml")
	assertProblem(t, ps, "skills/stray.yaml", "", "must be a directory")
	assertProblem(t, ps, "skills/release-diagnosis/9", "", "neither skill.yaml nor the directory of a version")
	for _, p := range ps {
		assert.False(t, strings.HasPrefix(p.File, ".github"), "hidden entries are not configuration: %s", p)
		assert.NotEqual(t, "CODEOWNERS", p.File)
	}
}

func TestAgentsAndSkillsAreOwnedByAPerson(t *testing.T) {
	repo := edit(t, exampleRepo(), "agents/release-investigator.yaml", "owner: alice@example.com", "owner: checkout-team")
	repo = edit(t, repo, "skills/release-diagnosis/skill.yaml", "owner: alice@example.com", "owner: Alice <alice@example.com>")
	repo = edit(t, repo, "servers/metrics.yaml", "owner: platform-team", "owner: platform team")
	repo = edit(t, repo, "work-item-types/incident.yaml", "owner: sre-leads@example.com", "owner: ''")

	ps := refused(t, repo)

	assertProblem(t, ps, "agents/release-investigator.yaml", "metadata.owner", "a person's email address")
	assertProblem(t, ps, "skills/release-diagnosis/skill.yaml", "metadata.owner", "a person's email address")
	assertProblem(t, ps, "servers/metrics.yaml", "metadata.owner", "one word")
	assertProblem(t, ps, "work-item-types/incident.yaml", "metadata.owner", "is required")
}

func TestEveryProblemIsReportedInFileOrder(t *testing.T) {
	repo := edit(t, exampleRepo(), "servers/metrics.yaml", "owner: platform-team", "owner: platform team")
	repo = edit(t, repo, "agents/release-investigator.yaml", "name: release-investigator,", "name: investigator,")

	ps := refused(t, repo)

	require.Len(t, ps, 2, list(ps))
	assert.Equal(t, "agents/release-investigator.yaml", ps[0].File)
	assert.Equal(t, "servers/metrics.yaml", ps[1].File)
	assert.Contains(t, ps.Error(), "2 problems")
}

func TestOversizedFilesAreRefused(t *testing.T) {
	big := "kind: RedactionRule\n# " + strings.Repeat("x", 1<<20) + "\n"
	repo := with(exampleRepo(), map[string]*fstest.MapFile{"redaction-rules/big.yaml": file(big)})

	ps := refused(t, repo)

	assertProblem(t, ps, "redaction-rules/big.yaml", "", "larger than 1048576 bytes")
}

func TestSymlinksAreRefused(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"agents/link.yaml": {Data: []byte("release-investigator.yaml"), Mode: fs.ModeSymlink | 0o777},
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "agents/link.yaml", "", "regular file")
}

func TestABundleHoldsItsEntryFileAndOnlyDeclaredVersions(t *testing.T) {
	repo := with(exampleRepo(), map[string]*fstest.MapFile{
		"skills/release-diagnosis/1.1.0/SKILL.md": nil,
		"skills/release-diagnosis/1.1.0/notes.md": file("No entry file.\n"),
		"skills/release-diagnosis/2.0.0/SKILL.md": file("# Not declared\n"),
	})

	ps := refused(t, repo)

	assertProblem(t, ps, "skills/release-diagnosis/1.1.0", "", "has no SKILL.md")
	assertProblem(t, ps, "skills/release-diagnosis/2.0.0", "", "neither skill.yaml nor the directory of a version")
}

func TestBundlesHaveLimits(t *testing.T) {
	files := map[string]*fstest.MapFile{}
	for i := range 501 {
		files[fmt.Sprintf("skills/release-diagnosis/1.2.0/reference/%03d.md", i)] = file("x\n")
	}
	ps := refused(t, with(exampleRepo(), files))

	assertProblem(t, ps, "skills/release-diagnosis/1.2.0", "", "holds 503 files; a version holds at most 500")

	big := file(strings.Repeat("y", 10<<20))
	ps = refused(t, with(exampleRepo(), map[string]*fstest.MapFile{"skills/release-diagnosis/1.2.0/reference/big.md": big}))

	assertProblem(t, ps, "skills/release-diagnosis/1.2.0", "", "a version holds at most 10485760")
}

func TestABundlesDigestCoversItsPathsAndContents(t *testing.T) {
	ref := config.SkillRef{Skill: "release-diagnosis", Version: "1.2.0"}
	digest := func(repo fstest.MapFS) string {
		t.Helper()
		v := build(t, repo).SkillVersion(ref)
		require.NotNil(t, v)
		return v.Digest()
	}
	base := digest(exampleRepo())

	renamed := with(exampleRepo(), map[string]*fstest.MapFile{
		"skills/release-diagnosis/1.2.0/reference/flags.md": nil,
		"skills/release-diagnosis/1.2.0/reference/flag.md":  exampleRepo()["skills/release-diagnosis/1.2.0/reference/flags.md"],
	})
	changed := edit(t, exampleRepo(), "skills/release-diagnosis/1.2.0/reference/flags.md", "affects", "touches")

	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, base)
	assert.Equal(t, base, digest(exampleRepo()))
	assert.NotEqual(t, base, digest(renamed))
	assert.NotEqual(t, base, digest(changed))
	assert.Equal(t, []string{"SKILL.md", "reference/flags.md"}, build(t, exampleRepo()).SkillVersion(ref).Files())
}

func TestASkillBundleCanBeRead(t *testing.T) {
	rev := build(t, exampleRepo())

	bundle, err := rev.SkillBundle(config.SkillRef{Skill: "release-diagnosis", Version: "1.2.0"})
	require.NoError(t, err)
	data, err := fs.ReadFile(bundle, "reference/flags.md")
	require.NoError(t, err)
	assert.Contains(t, string(data), "keyed to the service")

	_, err = rev.SkillBundle(config.SkillRef{Skill: "release-diagnosis", Version: "9.9.9"})
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestTheYankListIsNamedByItsKind(t *testing.T) {
	repo := edit(t, exampleRepo(), "yanks.yaml", "kind: YankList", "kind: Yanks")

	ps := refused(t, repo)

	assertProblem(t, ps, "yanks.yaml", "kind", "must be YankList")
}
