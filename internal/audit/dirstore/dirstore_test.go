package dirstore_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
	"github.com/jchen0426/agent-swarm/internal/audit/dirstore"
)

func open(t *testing.T) (s *dirstore.Store, dir string) {
	t.Helper()
	dir = t.TempDir()
	s, err := dirstore.Open(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

// An anchor is written once, as a file under its tenant's directory, and
// listed with every other file there, each read on its own.
func TestAnAnchorIsWrittenOnceAndListed(t *testing.T) {
	s, dir := open(t)
	ctx := t.Context()

	require.NoError(t, s.Put(ctx, "tenant-a/a.json", []byte("one")))
	require.ErrorIs(t, s.Put(ctx, "tenant-a/a.json", []byte("two")), app.ErrExists)
	require.NoError(t, s.Put(ctx, "tenant-b/b.json", []byte("other tenant")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tenant-a", "planted.json"), []byte("planted"), 0o600))

	got, err := s.List(ctx, "tenant-a")
	require.NoError(t, err)
	for i := range got {
		require.NoError(t, s.Read(ctx, &got[i]))
	}

	assert.ElementsMatch(t, []chain.Stored{{Key: "tenant-a/a.json", Body: []byte("one")}, {Key: "tenant-a/planted.json", Body: []byte("planted")}}, got)
}

// A tenant without anchors lists none.
func TestATenantWithoutAnchorsListsNone(t *testing.T) {
	s, _ := open(t)

	got, err := s.List(t.Context(), "tenant-a")

	require.NoError(t, err)
	assert.Empty(t, got)
}

// Names never leave the directory, and a file too large to be an anchor is
// listed cut, so that it never decodes.
func TestNamesStayInTheDirectory(t *testing.T) {
	s, dir := open(t)
	ctx := t.Context()
	for _, bad := range []string{"../a.json", "/a.json", "a.json", "tenant-a/x/a.json", "tenant-a/../../a.json"} {
		require.Error(t, s.Put(ctx, bad, []byte("x")), bad)
	}
	_, err := s.List(ctx, "../tenant-a")
	require.Error(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tenant-a"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tenant-a", "big.json"), make([]byte, chain.MaxAnchorBytes+10), 0o600))

	got, err := s.List(ctx, "tenant-a")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.NoError(t, s.Read(ctx, &got[0]))

	assert.Len(t, got[0].Body, chain.MaxAnchorBytes+1)
}

// A directory is not write-once, and keeps no file under retention.
func TestADirectoryIsNotWriteOnce(t *testing.T) {
	s, _ := open(t)
	require.NoError(t, s.Put(t.Context(), "tenant-a/a.json", []byte("one")))
	o := chain.Stored{Key: "tenant-a/a.json"}

	locked, why, err := s.Locked(t.Context())

	require.NoError(t, err)
	assert.False(t, locked)
	assert.Contains(t, why, "not write-once")
	require.NoError(t, s.Retention(t.Context(), &o))
	assert.True(t, o.LockedUntil.IsZero())
}
