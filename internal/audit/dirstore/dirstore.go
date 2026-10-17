// Package dirstore keeps audit anchors as files in a directory, for
// development and tests. A directory is not write-once: whoever can write it
// can change an anchor, so verification reports the store as unlocked. It
// implements the Audit application's Anchors port.
package dirstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

// Store keeps anchors under one directory. Names never resolve outside it.
type Store struct {
	root *os.Root
}

// Open returns the store of the anchors under dir, which must exist.
func Open(dir string) (*Store, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("dirstore: open %s: %w", dir, err)
	}
	return &Store{root: root}, nil
}

// Close releases the directory.
func (s *Store) Close() error { return s.root.Close() }

// Put writes body as the file key, which must not exist.
func (s *Store) Put(_ context.Context, key string, body []byte) error {
	if !fs.ValidPath(key) || strings.Count(key, "/") != 1 {
		return fmt.Errorf("dirstore: %q is not an anchor's name", key)
	}
	dir, _, _ := strings.Cut(key, "/")
	if err := s.root.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("dirstore: %w", err)
	}
	f, err := s.root.OpenFile(key, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o440)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%w: %s", app.ErrExists, key)
	}
	if err != nil {
		return fmt.Errorf("dirstore: %w", err)
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("dirstore: write %s: %w", key, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("dirstore: write %s: %w", key, err)
	}
	return nil
}

// List returns every file under the tenant's directory, without its body.
func (s *Store) List(_ context.Context, tenant string) ([]chain.Stored, error) {
	if !chain.ValidTenant(tenant) {
		return nil, fmt.Errorf("dirstore: %q is not a tenant id", tenant)
	}
	var out []chain.Stored
	err := fs.WalkDir(s.root.FS(), tenant, func(name string, d fs.DirEntry, err error) error {
		switch {
		case errors.Is(err, fs.ErrNotExist) && name == tenant:
			return fs.SkipAll
		case err != nil:
			return err
		case !d.IsDir():
			out = append(out, chain.Stored{Key: name})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("dirstore: list %s: %w", tenant, err)
	}
	return out, nil
}

// Read reads a file, at most one byte past the largest anchor, so that a
// larger one never decodes.
func (s *Store) Read(_ context.Context, o *chain.Stored) error {
	f, err := s.root.Open(o.Key)
	if err != nil {
		return fmt.Errorf("dirstore: %w", err)
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(io.LimitReader(f, chain.MaxAnchorBytes+1))
	if err != nil {
		return fmt.Errorf("dirstore: read %s: %w", o.Key, err)
	}
	o.Body = body
	return nil
}

// Retention leaves a file without retention: a directory keeps none.
func (*Store) Retention(context.Context, *chain.Stored) error { return nil }

// Locked reports that a directory is not write-once.
func (*Store) Locked(context.Context) (locked bool, why string, err error) {
	return false, "a directory is not write-once: whoever can write it can change an anchor", nil
}
