package gitsource_test

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/dirsource"
	"github.com/jchen0426/agent-swarm/internal/registry/gitsource"
)

var author = &object.Signature{Name: "Reviewer", Email: "reviewer@example.com", When: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)}

// repo is a Git repository in a temporary directory.
type repo struct {
	t   *testing.T
	dir string
	git *git.Repository
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	return &repo{t: t, dir: dir, git: r}
}

func (r *repo) write(files map[string]string) {
	r.t.Helper()
	for p, text := range files {
		full := filepath.Join(r.dir, filepath.FromSlash(p))
		require.NoError(r.t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(r.t, os.WriteFile(full, []byte(text), 0o600))
	}
}

func (r *repo) commit(msg string) string {
	r.t.Helper()
	wt, err := r.git.Worktree()
	require.NoError(r.t, err)
	require.NoError(r.t, wt.AddGlob("."))
	h, err := wt.Commit(msg, &git.CommitOptions{Author: author, Committer: author, AllowEmptyCommits: true})
	require.NoError(r.t, err)
	return h.String()
}

func source(t *testing.T, r *repo) *gitsource.Source {
	t.Helper()
	s, err := gitsource.Open(r.dir)
	require.NoError(t, err)
	return s
}

func readText(t *testing.T, fsys fs.FS, name string) string {
	t.Helper()
	data, err := fs.ReadFile(fsys, name)
	require.NoError(t, err)
	return string(data)
}

func TestReadReturnsTheFilesOfTheCommitAtARef(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"agents/a.yaml": "v1\n", ".github/workflows/ci.yaml": "on: push\n"})
	first := r.commit("first")
	r.write(map[string]string{"agents/a.yaml": "v2\n"})
	second := r.commit("second")
	src := source(t, r)

	head, err := src.Read(t.Context(), "")
	require.NoError(t, err)
	assert.Equal(t, second, head.Commit)
	assert.Equal(t, "v2\n", readText(t, head.Files, "agents/a.yaml"))
	_, err = fs.Stat(head.Files, ".github/workflows/ci.yaml")
	require.ErrorIs(t, err, fs.ErrNotExist)

	old, err := src.Read(t.Context(), first)
	require.NoError(t, err)
	assert.Equal(t, first, old.Commit)
	assert.Equal(t, "v1\n", readText(t, old.Files, "agents/a.yaml"))

	byBranch, err := src.Read(t.Context(), "refs/heads/master")
	require.NoError(t, err)
	assert.Equal(t, second, byBranch.Commit)

	head, err = src.Read(t.Context(), "HEAD")
	require.NoError(t, err)
	assert.Equal(t, second, head.Commit)
}

func TestReadRefusesNamesThatCouldMeanTwoCommits(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"agents/a.yaml": "reviewed\n"})
	reviewed := r.commit("reviewed")
	r.write(map[string]string{"agents/a.yaml": "not reviewed\n"})
	unreviewed := r.commit("not reviewed")
	_, err := r.git.CreateTag("master", plumbing.NewHash(reviewed), nil)
	require.NoError(t, err)
	src := source(t, r)

	for _, ambiguous := range []string{"master", reviewed[:7], "heads/master"} {
		_, err := src.Read(t.Context(), ambiguous)
		require.ErrorContains(t, err, "full ref", ambiguous)
	}
	branch, err := src.Read(t.Context(), "refs/heads/master")
	require.NoError(t, err)
	assert.Equal(t, unreviewed, branch.Commit)
	tag, err := src.Read(t.Context(), "refs/tags/master")
	require.NoError(t, err)
	assert.Equal(t, reviewed, tag.Commit)
}

func TestAnAnnotatedTagNamesItsCommit(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"agents/a.yaml": "v1\n"})
	commit := r.commit("v1")
	_, err := r.git.CreateTag("v1", plumbing.NewHash(commit), &git.CreateTagOptions{Tagger: author, Message: "Release v1"})
	require.NoError(t, err)

	snap, err := source(t, r).Read(t.Context(), "refs/tags/v1")

	require.NoError(t, err)
	assert.Equal(t, commit, snap.Commit)
	assert.Equal(t, "v1\n", readText(t, snap.Files, "agents/a.yaml"))
}

func TestAFullCommitIDMustNameACommit(t *testing.T) {
	r := newRepo(t)
	r.commit("empty")

	_, err := source(t, r).Read(t.Context(), "1111111111111111111111111111111111111111")

	require.Error(t, err)
}

func TestReadIgnoresTheWorkingTree(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"agents/a.yaml": "reviewed\n"})
	r.commit("reviewed")
	r.write(map[string]string{"agents/a.yaml": "not reviewed\n", "agents/b.yaml": "untracked\n"})

	snap, err := source(t, r).Read(t.Context(), "")

	require.NoError(t, err)
	assert.Equal(t, "reviewed\n", readText(t, snap.Files, "agents/a.yaml"))
	_, err = fs.Stat(snap.Files, "agents/b.yaml")
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestReadRefusesLinks(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{"agents/a.yaml": "kind: Agent\n"})
	require.NoError(t, os.Symlink("a.yaml", filepath.Join(r.dir, "agents", "b.yaml")))
	r.commit("a link")

	_, err := source(t, r).Read(t.Context(), "")

	require.ErrorContains(t, err, "agents/b.yaml is a link or a submodule")
}

func TestReadRefusesSubmodules(t *testing.T) {
	r := newRepo(t)
	r.commit("empty")
	store := r.git.Storer
	tree := &object.Tree{Entries: []object.TreeEntry{
		{Name: "skills", Mode: filemode.Submodule, Hash: plumbing.NewHash("1111111111111111111111111111111111111111")},
	}}
	treeObj := store.NewEncodedObject()
	require.NoError(t, tree.Encode(treeObj))
	treeHash, err := store.SetEncodedObject(treeObj)
	require.NoError(t, err)
	commit := &object.Commit{Author: *author, Committer: *author, Message: "a submodule", TreeHash: treeHash}
	commitObj := store.NewEncodedObject()
	require.NoError(t, commit.Encode(commitObj))
	commitHash, err := store.SetEncodedObject(commitObj)
	require.NoError(t, err)

	_, err = source(t, r).Read(t.Context(), commitHash.String())

	require.ErrorContains(t, err, "skills is a link or a submodule")
}

func TestAnUnknownRefIsAnError(t *testing.T) {
	r := newRepo(t)
	r.commit("empty")

	_, err := source(t, r).Read(t.Context(), "refs/heads/nope")

	require.ErrorContains(t, err, `resolve "refs/heads/nope"`)
}

func TestOpenNeedsARepository(t *testing.T) {
	_, err := gitsource.Open(t.TempDir())

	require.Error(t, err)
}

type anyCondition struct{}

func (anyCondition) Check(config.Environment, string) error { return nil }

func TestEqualContentGivesAnEqualDigestFromEitherSource(t *testing.T) {
	r := newRepo(t)
	r.write(map[string]string{
		"redaction-rules/default-secrets.yaml": "kind: RedactionRule\nmetadata: {name: default-secrets, owner: security@example.com}\nspec: {patterns: ['AKIA[0-9A-Z]{16}']}\n",
		"skills/runbook/skill.yaml": "kind: Skill\nmetadata: {name: runbook, owner: alice@example.com}\n" +
			"spec: {versions: [{version: 1.0.0, source: {repository: https://git.example.com/runbooks, commit: 3f2a9c1}}]}\n",
		"skills/runbook/1.0.0/SKILL.md":     "# Runbook\n",
		"skills/runbook/1.0.0/ref/notes.md": "Notes.\n",
	})
	commit := r.commit("configuration")

	fromGit, err := source(t, r).Read(t.Context(), "")
	require.NoError(t, err)
	fromDir, err := dirsource.New(r.dir).Read(t.Context(), "")
	require.NoError(t, err)
	revGit, err := config.Build(fromGit.Files, fromGit.Commit, anyCondition{})
	require.NoError(t, err)
	revDir, err := config.Build(fromDir.Files, "", anyCondition{})
	require.NoError(t, err)

	assert.Equal(t, commit, revGit.Commit())
	assert.Equal(t, revGit.Digest(), revDir.Digest())
}

func TestReadSeesCommitsFetchedAndPackedOutsideIt(t *testing.T) {
	gitCLI, err := exec.LookPath("git")
	if err != nil {
		t.Skip("needs the git command")
	}
	upstream := newRepo(t)
	upstream.write(map[string]string{"agents/a.yaml": "v1\n"})
	first := upstream.commit("first")
	clone := t.TempDir()
	runGit(t, gitCLI, "clone", "-q", "file://"+upstream.dir, clone) // a pack, as from a remote
	src, err := gitsource.Open(clone)
	require.NoError(t, err)
	_, err = src.Read(t.Context(), "refs/remotes/origin/master")
	require.NoError(t, err)

	files := map[string]string{}
	for i := range 150 { // more objects than git unpacks, so the fetch writes a new pack
		files[fmt.Sprintf("agents/a%03d.yaml", i)] = "v2\n"
	}
	upstream.write(files)
	second := upstream.commit("second")
	runGit(t, gitCLI, "-C", clone, "fetch", "-q")

	fetched, err := src.Read(t.Context(), "refs/remotes/origin/master")
	require.NoError(t, err)
	assert.Equal(t, second, fetched.Commit)

	runGit(t, gitCLI, "-C", clone, "gc", "-q", "--prune=now")
	old, err := src.Read(t.Context(), first)
	require.NoError(t, err)
	assert.Equal(t, first, old.Commit)
}

func runGit(t *testing.T, gitCLI string, args ...string) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), gitCLI, args...).CombinedOutput()
	require.NoError(t, err, string(out))
}
