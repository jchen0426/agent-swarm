package files_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/secrets/files"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
}

func open(t *testing.T, dir string) *files.Store {
	t.Helper()
	s, err := files.Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestASecretIsReadWhenItIsUsed(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "mcp", "catalog"), "first\n")
	s := open(t, dir)

	got, err := s.Secret(t.Context(), "secret://mcp/catalog")
	require.NoError(t, err)
	assert.Equal(t, "first", got)

	write(t, filepath.Join(dir, "mcp", "catalog"), "rotated")
	got, err = s.Secret(t.Context(), "secret://mcp/catalog")
	require.NoError(t, err)
	assert.Equal(t, "rotated", got)
}

func TestAReferenceNeverLeavesTheDirectory(t *testing.T) {
	outside := t.TempDir()
	write(t, filepath.Join(outside, "token"), "not yours")
	dir := t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(outside, "token"), filepath.Join(dir, "link")))
	s := open(t, dir)

	for _, ref := range []string{
		"secret://../token", "secret:///etc/passwd", "secret://link", "secret://", "file://token", "mcp/catalog",
	} {
		_, err := s.Secret(t.Context(), ref)
		require.Error(t, err, ref)
		assert.NotContains(t, err.Error(), "not yours")
	}
}

func TestAMissingOrEmptySecretIsAnError(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "empty"), "\n")
	s := open(t, dir)

	_, err := s.Secret(t.Context(), "secret://missing")
	require.ErrorIs(t, err, files.ErrNotFound)
	_, err = s.Secret(t.Context(), "secret://empty")
	require.ErrorContains(t, err, "is empty")
}
