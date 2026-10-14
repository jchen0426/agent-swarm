// Package files resolves secret references from a directory of files, as a
// secret store mounted into the platform's container provides them:
// secret://mcp/catalog is the file mcp/catalog under the directory. Each
// reference is read when it is used, so a rotated secret takes effect without
// a restart (spec Section 9.3).
package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// ErrNotFound is returned for a reference that names no secret.
var ErrNotFound = errors.New("files: no such secret")

// Store reads secrets under one directory. Names never resolve outside it,
// through .. or through a link.
type Store struct {
	root *os.Root
}

// Open returns a store of the secrets under dir.
func Open(dir string) (*Store, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("files: open %s: %w", dir, err)
	}
	return &Store{root: root}, nil
}

// Close releases the directory.
func (s *Store) Close() error { return s.root.Close() }

// Secret returns the secret that ref names, without a trailing newline. The
// error never holds the secret.
func (s *Store) Secret(_ context.Context, ref string) (string, error) {
	name, ok := strings.CutPrefix(ref, "secret://")
	if !ok || !fs.ValidPath(name) || name == "." {
		return "", fmt.Errorf("files: %q is not a secret reference", ref)
	}
	data, err := s.root.ReadFile(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrNotFound, ref)
		}
		return "", fmt.Errorf("files: read %s: %w", ref, err)
	}
	data = bytes.TrimRight(data, "\r\n")
	if len(data) == 0 {
		return "", fmt.Errorf("files: secret %s is empty", ref)
	}
	return string(data), nil
}
