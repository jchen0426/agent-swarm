package memfs_test

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
)

func TestTheFileSystemKeepsTheStandardContract(t *testing.T) {
	f, err := memfs.New(map[string][]byte{
		"yanks.yaml":                        []byte("kind: YankList\n"),
		"agents/a.yaml":                     []byte("kind: Agent\n"),
		"skills/s/1.0.0/SKILL.md":           []byte("# S\n"),
		"skills/s/1.0.0/reference/notes.md": nil,
	})
	require.NoError(t, err)

	require.NoError(t, fstest.TestFS(f, "yanks.yaml", "agents/a.yaml", "skills/s/1.0.0/SKILL.md", "skills/s/1.0.0/reference/notes.md"))
}

func TestASnapshotNeverChanges(t *testing.T) {
	data := []byte("kind: Agent\n")
	f, err := memfs.New(map[string][]byte{"agents/a.yaml": data})
	require.NoError(t, err)

	data[0] = 'K'
	got, err := fs.ReadFile(f, "agents/a.yaml")
	require.NoError(t, err)
	got[1] = 'I'

	again, err := fs.ReadFile(f, "agents/a.yaml")
	require.NoError(t, err)
	assert.Equal(t, "kind: Agent\n", string(again))
}

func TestNewRefusesBadPaths(t *testing.T) {
	for _, files := range []map[string][]byte{
		{"/abs.yaml": nil},
		{"a/../b.yaml": nil},
		{".": nil},
		{"a": nil, "a/b.yaml": nil},
	} {
		_, err := memfs.New(files)
		require.Error(t, err, "%v", files)
	}
}

func TestMissingPathsAreNotFound(t *testing.T) {
	f, err := memfs.New(map[string][]byte{"agents/a.yaml": nil})
	require.NoError(t, err)

	_, err = f.Open("agents/b.yaml")
	require.ErrorIs(t, err, fs.ErrNotExist)
	_, err = fs.ReadDir(f, "servers")
	require.ErrorIs(t, err, fs.ErrNotExist)
	_, err = fs.Stat(f, "../agents")
	require.ErrorIs(t, err, fs.ErrNotExist)
}
