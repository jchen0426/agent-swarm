// Package dirsource reads configuration from a directory, such as a checkout
// that a pull request check validates before any commit exists. It
// implements app.Source.
package dirsource

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/jchen0426/agent-swarm/internal/registry/app"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
)

// Source reads one directory.
type Source struct {
	dir string
}

// New returns a source that reads dir.
func New(dir string) *Source { return &Source{dir: dir} }

// Read takes a snapshot of the directory. A directory has no commit, so ref
// must be empty, and the snapshot's commit is empty. Top-level hidden
// entries, such as .git, are skipped; links and other special files are
// refused, so the snapshot holds only what the directory itself holds.
func (s *Source) Read(ctx context.Context, ref string) (app.Snapshot, error) {
	if ref != "" {
		return app.Snapshot{}, fmt.Errorf("dirsource: a directory has no ref %q", ref)
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return app.Snapshot{}, fmt.Errorf("dirsource: %w", err)
	}
	defer func() { _ = root.Close() }()
	files, err := readAll(ctx, root.FS())
	if err != nil {
		return app.Snapshot{}, fmt.Errorf("dirsource: read %s: %w", s.dir, err)
	}
	f, err := memfs.New(files)
	if err != nil {
		return app.Snapshot{}, errors.Join(errors.New("dirsource"), err)
	}
	return app.Snapshot{Files: f}, nil
}

// readAll reads every regular file of fsys within the snapshot limits.
func readAll(ctx context.Context, fsys fs.FS) (map[string][]byte, error) {
	files := map[string][]byte{}
	var total int64
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case ctx.Err() != nil:
			return ctx.Err()
		case p != "." && !strings.Contains(p, "/") && strings.HasPrefix(p, "."):
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a regular file; configuration holds no links", p)
		case len(files) == app.MaxSnapshotFiles:
			return fmt.Errorf("the directory holds more than %d files", app.MaxSnapshotFiles)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if total += int64(len(data)); total > app.MaxSnapshotBytes {
			return fmt.Errorf("the directory holds more than %d bytes", app.MaxSnapshotBytes)
		}
		files[p] = data
		return nil
	})
	return files, err
}
