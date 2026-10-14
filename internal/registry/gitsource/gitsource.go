// Package gitsource reads configuration revisions from a Git repository: the
// files of one commit, never a working tree, so what loads is exactly what
// was reviewed. It implements app.Source.
package gitsource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/jchen0426/agent-swarm/internal/registry/app"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
)

// fullCommitID is a full SHA-1 commit id.
var fullCommitID = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Source reads one repository. Reads are serialized.
type Source struct {
	mu  sync.Mutex
	dir string
}

// Open checks that dir holds a repository, a clone of the configuration
// repository, and returns a source that reads it.
func Open(dir string) (*Source, error) {
	if _, err := git.PlainOpen(dir); err != nil {
		return nil, fmt.Errorf("gitsource: open %s: %w", dir, err)
	}
	return &Source{dir: dir}, nil
}

// Read returns the files of the commit that ref names: HEAD (also when ref
// is empty), a full reference name such as refs/heads/main or refs/tags/v1,
// or a full commit id. Short names and abbreviated ids are refused, because
// a tag, a branch and an id prefix can share a name, and the wrong one would
// load a commit nobody reviewed. Top-level hidden entries, such as .github,
// are skipped; links and submodules are refused.
//
// Each read opens the repository again. Something else updates it, such as a
// fetch or git's own gc, and an open repository keeps the pack files it found
// first: it would miss new commits and lose packs that gc removed.
func (s *Source) Read(ctx context.Context, ref string) (app.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, err := git.PlainOpen(s.dir)
	if err != nil {
		return app.Snapshot{}, fmt.Errorf("gitsource: open %s: %w", s.dir, err)
	}
	r := reader{repo: repo}
	hash, err := r.resolve(ref)
	if err != nil {
		return app.Snapshot{}, fmt.Errorf("gitsource: resolve %q: %w", ref, err)
	}
	commit, err := repo.CommitObject(hash)
	if err != nil {
		return app.Snapshot{}, fmt.Errorf("gitsource: commit %s: %w", hash, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return app.Snapshot{}, fmt.Errorf("gitsource: tree of %s: %w", hash, err)
	}
	files, err := r.readTree(ctx, tree)
	if err != nil {
		return app.Snapshot{}, fmt.Errorf("gitsource: commit %s: %w", hash, err)
	}
	f, err := memfs.New(files)
	if err != nil {
		return app.Snapshot{}, errors.Join(errors.New("gitsource"), err)
	}
	return app.Snapshot{Commit: hash.String(), Files: f}, nil
}

// reader reads one opened repository.
type reader struct {
	repo *git.Repository
}

// resolve finds the commit that ref names, without guessing.
func (r reader) resolve(ref string) (plumbing.Hash, error) {
	switch {
	case ref == "" || ref == "HEAD":
		head, err := r.repo.Head()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		return r.peel(head.Hash())
	case fullCommitID.MatchString(ref):
		return plumbing.NewHash(ref), nil
	case strings.HasPrefix(ref, "refs/"):
		named, err := r.repo.Reference(plumbing.ReferenceName(ref), true)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		return r.peel(named.Hash())
	}
	return plumbing.ZeroHash, errors.New("name HEAD, a full ref such as refs/heads/main, or a full commit id")
}

// peel follows an annotated tag to the commit it names.
func (r reader) peel(hash plumbing.Hash) (plumbing.Hash, error) {
	tag, err := r.repo.TagObject(hash)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return hash, nil
	}
	if err != nil {
		return plumbing.ZeroHash, err
	}
	commit, err := tag.Commit()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return commit.Hash, nil
}

func (r reader) readTree(ctx context.Context, tree *object.Tree) (map[string][]byte, error) {
	walker := object.NewTreeWalker(tree, true, nil)
	defer walker.Close()
	files := map[string][]byte{}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name, entry, err := walker.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, err
		}
		if top, _, _ := strings.Cut(name, "/"); strings.HasPrefix(top, ".") {
			continue
		}
		switch entry.Mode {
		case filemode.Dir:
			continue
		case filemode.Regular, filemode.Executable, filemode.Deprecated:
		default:
			return nil, fmt.Errorf("%s is a link or a submodule; configuration holds regular files only", name)
		}
		if len(files) == app.MaxSnapshotFiles {
			return nil, fmt.Errorf("the commit holds more than %d files", app.MaxSnapshotFiles)
		}
		data, err := r.readBlob(entry.Hash, &total)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		files[name] = data
	}
}

func (r reader) readBlob(hash plumbing.Hash, total *int64) ([]byte, error) {
	blob, err := r.repo.BlobObject(hash)
	if err != nil {
		return nil, err
	}
	if *total += blob.Size; *total > app.MaxSnapshotBytes {
		return nil, fmt.Errorf("the commit holds more than %d bytes", app.MaxSnapshotBytes)
	}
	content, err := blob.Reader()
	if err != nil {
		return nil, err
	}
	defer func() { _ = content.Close() }()
	return io.ReadAll(content)
}
