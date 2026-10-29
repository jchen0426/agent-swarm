package docker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// storeEngine is an engine for the store's tests: Save runs save, Load reads
// what it is given, Image answers the id it holds, and Tag records each call.
// Any other call panics, on the nil Engine it embeds.
type storeEngine struct {
	Engine
	save   func(w io.Writer) error
	loaded string
	// id is what Image answers.
	id string
	// tags holds "<id> <ref>" of each Tag.
	tags []string
}

func (f *storeEngine) Save(_ context.Context, _ string, w io.Writer) error { return f.save(w) }

func (f *storeEngine) Load(_ context.Context, r io.Reader) error {
	b, err := io.ReadAll(r)
	f.loaded = string(b)
	return err
}

func (f *storeEngine) Image(_ context.Context, _ string) (Image, error) { return Image{ID: f.id}, nil }

func (f *storeEngine) Tag(_ context.Context, id, ref string) error {
	f.tags = append(f.tags, id+" "+ref)
	return nil
}

// dirNames returns the names in dir, sorted.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	found, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(found))
	for _, e := range found {
		names = append(names, e.Name())
	}
	return names
}

// TestTheStoreWritesAnImageWholeOrNotAtAll (Contract C12): a save is written
// to .<hex>.tar.<random> and renamed to <hex>.tar once whole; a save that
// fails leaves neither.
func TestTheStoreWritesAnImageWholeOrNotAtAll(t *testing.T) {
	dir := t.TempDir()
	s := store{dir: dir}
	hex := strings.Repeat("ab", 32)
	id := "sha256:" + hex
	temporary := regexp.MustCompile(`^\.` + hex + `\.tar\..+$`)

	half := &storeEngine{save: func(w io.Writer) error {
		_, _ = io.WriteString(w, "half of a tar")
		return errors.New("the engine went away")
	}}
	require.Error(t, s.save(t.Context(), half, id))
	assert.False(t, s.has(id))
	assert.Empty(t, dirNames(t, dir), "a save that failed leaves no file")

	var during []string
	whole := &storeEngine{save: func(w io.Writer) error {
		during = dirNames(t, dir)
		_, err := io.WriteString(w, "a whole tar")
		return err
	}}
	require.NoError(t, s.save(t.Context(), whole, id))
	require.Len(t, during, 1)
	assert.Regexp(t, temporary, during[0], "the save writes to a temporary file first")
	assert.True(t, s.has(id))
	assert.Equal(t, []string{hex + ".tar"}, dirNames(t, dir), "no temporary file is left")
	got, err := os.ReadFile(filepath.Join(dir, hex+".tar"))
	require.NoError(t, err)
	assert.Equal(t, "a whole tar", string(got))
}

// TestAReloadMustGiveBackTheLockedID (Contract C12): a reload loads the
// store's copy, and tags the image only once the engine holds the very id
// the item locked.
func TestAReloadMustGiveBackTheLockedID(t *testing.T) {
	dir := t.TempDir()
	s := store{dir: dir}
	hex := strings.Repeat("ab", 32)
	id := "sha256:" + hex
	require.NoError(t, os.WriteFile(filepath.Join(dir, hex+".tar"), []byte("a tar"), 0o600))

	other := &storeEngine{id: "sha256:" + strings.Repeat("cd", 32)}
	err := s.reload(t.Context(), other, id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), id)
	assert.Equal(t, "a tar", other.loaded, "the reload loads the store's copy")
	assert.Empty(t, other.tags, "an image of another id is not tagged")

	same := &storeEngine{id: id}
	require.NoError(t, s.reload(t.Context(), same, id))
	assert.Equal(t, []string{id + " agentswarm-runtime:" + hex}, same.tags)
}
