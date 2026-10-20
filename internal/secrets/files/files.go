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
	"regexp"
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
// error never holds the secret, nor a value given in place of a reference,
// which may be a secret itself, as a database URL with its password is.
func (s *Store) Secret(_ context.Context, ref string) (string, error) {
	name, ok := strings.CutPrefix(ref, "secret://")
	if !ok {
		return "", notAReference(ref)
	}
	if !fs.ValidPath(name) || name == "." {
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

// urlScheme matches the scheme a URL begins with.
var urlScheme = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.-]{0,15})://`)

// notAReference reports a value given in place of a secret reference. It
// names at most the scheme of a URL, never what follows it.
func notAReference(value string) error {
	if m := urlScheme.FindStringSubmatch(value); m != nil {
		return fmt.Errorf("files: a %s URL is not a secret reference, secret://NAME", m[1])
	}
	return errors.New("files: the value is not a secret reference, secret://NAME")
}
