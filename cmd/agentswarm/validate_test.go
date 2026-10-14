package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

const exampleConfig = "../../examples/config"

type validated struct {
	Commit   string          `json:"commit"`
	Digest   string          `json:"digest"`
	Objects  []config.Object `json:"objects"`
	Problems config.Problems `json:"problems"`
}

func TestValidateAcceptsTheExampleConfiguration(t *testing.T) {
	stdout, errOut, code := runCommand("registry", "validate", "--dir", exampleConfig)

	require.Equal(t, exitOK, code, errOut)
	var v validated
	require.NoError(t, json.Unmarshal([]byte(stdout), &v))
	assert.Empty(t, v.Commit)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, v.Digest)
	assert.Len(t, v.Objects, 26)
	var skill *config.Object
	for i := range v.Objects {
		if v.Objects[i].Name == "release-diagnosis@1.0.0" {
			skill = &v.Objects[i]
		}
	}
	require.NotNil(t, skill)
	assert.Equal(t, config.KindSkillVersion, skill.Kind)
	assert.Equal(t, "skills/release-diagnosis/1.0.0", skill.File)
	assert.Regexp(t, `^sha256:[0-9a-f]{64}$`, skill.Hash)
}

func TestValidateReadsACommitOfAGitRepository(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	rule := "kind: RedactionRule\nmetadata: {name: default-secrets, owner: security@example.com}\nspec: {patterns: ['AKIA[0-9A-Z]{16}']}\n"
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "redaction-rules"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "redaction-rules", "default-secrets.yaml"), []byte(rule), 0o600))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, wt.AddGlob("."))
	when := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	commit, err := wt.Commit("configuration", &git.CommitOptions{Author: &object.Signature{Name: "R", Email: "r@example.com", When: when}})
	require.NoError(t, err)

	stdout, errOut, code := runCommand("registry", "validate", "--repo", dir, "--ref", "HEAD")

	require.Equal(t, exitOK, code, errOut)
	var v validated
	require.NoError(t, json.Unmarshal([]byte(stdout), &v))
	assert.Equal(t, commit.String(), v.Commit)
	require.Len(t, v.Objects, 1)
	assert.Equal(t, config.KindRedactionRule, v.Objects[0].Kind)
}

func TestValidatePrintsEveryProblemOfARefusedRevision(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "a.yaml"), []byte("kind: Agent\nmetadata: {name: b, owner: team}\nspec: {}\n"), 0o600))

	stdout, errOut, code := runCommand("registry", "validate", "--dir", dir)

	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "the configuration is refused: 2 problems")
	var v validated
	require.NoError(t, json.Unmarshal([]byte(stdout), &v))
	require.Len(t, v.Problems, 2)
	assert.Equal(t, "metadata.name", v.Problems[0].Field)
	assert.Equal(t, "metadata.owner", v.Problems[1].Field)
}

func TestValidateNeedsOneSource(t *testing.T) {
	_, errOut, code := runCommand("registry", "validate")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, "at least one of the flags in the group [dir repo] is required")

	_, errOut, code = runCommand("registry", "validate", "--dir", exampleConfig, "--ref", "main")
	assert.Equal(t, exitUsage, code)
	assert.Contains(t, errOut, "none of the others can be")

	_, errOut, code = runCommand("registry", "validate", "--repo", t.TempDir())
	assert.Equal(t, exitError, code)
	assert.Contains(t, errOut, "gitsource: open")
}
