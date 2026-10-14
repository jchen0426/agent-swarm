package dirsource_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/dirsource"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, text := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(text), 0o600))
	}
}

func TestReadTakesASnapshotOfTheDirectory(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"agents/a.yaml":           "kind: Agent\n",
		"skills/s/1.0.0/SKILL.md": "# S\n",
		".git/config":             "[core]\n",
		".github/workflows/x.yml": "on: push\n",
		"skills/s/1.0.0/.notes":   "kept: only top-level hidden entries are skipped\n",
	})

	snap, err := dirsource.New(dir).Read(t.Context(), "")

	require.NoError(t, err)
	assert.Empty(t, snap.Commit)
	data, err := fs.ReadFile(snap.Files, "agents/a.yaml")
	require.NoError(t, err)
	assert.Equal(t, "kind: Agent\n", string(data))
	_, err = fs.Stat(snap.Files, ".git/config")
	require.ErrorIs(t, err, fs.ErrNotExist)
	_, err = fs.Stat(snap.Files, "skills/s/1.0.0/.notes")
	require.NoError(t, err)

	write(t, dir, map[string]string{"agents/a.yaml": "kind: Changed\n"})
	data, err = fs.ReadFile(snap.Files, "agents/a.yaml")
	require.NoError(t, err)
	assert.Equal(t, "kind: Agent\n", string(data), "a snapshot does not follow later edits")
}

func TestReadRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"agents/a.yaml": "kind: Agent\n"})
	require.NoError(t, os.Symlink("a.yaml", filepath.Join(dir, "agents", "b.yaml")))

	_, err := dirsource.New(dir).Read(t.Context(), "")

	require.ErrorContains(t, err, "agents/b.yaml is not a regular file")
}

func TestReadRefusesALinkThatLeavesTheDirectory(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	write(t, outside, map[string]string{"secret.yaml": "token: x\n"})
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agents"), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret.yaml"), filepath.Join(dir, "agents", "x.yaml")))

	_, err := dirsource.New(dir).Read(t.Context(), "")

	require.Error(t, err)
}

func TestADirectoryHasNoRef(t *testing.T) {
	_, err := dirsource.New(t.TempDir()).Read(t.Context(), "main")

	require.ErrorContains(t, err, `no ref "main"`)
}

func TestAMissingDirectoryIsAnError(t *testing.T) {
	_, err := dirsource.New(filepath.Join(t.TempDir(), "nope")).Read(t.Context(), "")

	require.Error(t, err)
}
