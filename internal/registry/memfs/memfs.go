// Package memfs is a read-only file system held in memory. A snapshot of a
// configuration repository is one: it never changes once taken, so a revision
// built from it stays what was checked.
package memfs

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"time"
)

// FS holds regular files. Directories exist where files are.
type FS struct {
	files map[string][]byte
	dirs  map[string][]fs.DirEntry
}

// New returns a file system holding copies of files, keyed by slash-separated
// paths that fs.ValidPath accepts.
func New(files map[string][]byte) (*FS, error) {
	f := &FS{files: make(map[string][]byte, len(files)), dirs: map[string][]fs.DirEntry{}}
	children := map[string]map[string]bool{".": {}}
	for name, data := range files {
		if !fs.ValidPath(name) || name == "." {
			return nil, fmt.Errorf("memfs: invalid path %q", name)
		}
		f.files[name] = bytes.Clone(data)
		for p := name; p != "."; p = path.Dir(p) {
			dir := path.Dir(p)
			if children[dir] == nil {
				children[dir] = map[string]bool{}
			}
			children[dir][path.Base(p)] = true
		}
	}
	for dir, names := range children {
		if _, isFile := f.files[dir]; isFile {
			return nil, fmt.Errorf("memfs: %q is both a file and a directory", dir)
		}
		entries := make([]fs.DirEntry, 0, len(names))
		for _, name := range slices.Sorted(maps.Keys(names)) {
			entries = append(entries, f.info(path.Join(dir, name)))
		}
		f.dirs[dir] = entries
	}
	return f, nil
}

func (f *FS) info(name string) *info {
	if data, ok := f.files[name]; ok {
		return &info{name: path.Base(name), size: int64(len(data))}
	}
	return &info{name: path.Base(name), dir: true}
}

// Open opens a file or a directory.
func (f *FS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if data, ok := f.files[name]; ok {
		return &file{info: f.info(name), r: bytes.NewReader(data)}, nil
	}
	if entries, ok := f.dirs[name]; ok {
		return &dir{info: f.info(name), entries: entries}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// ReadFile returns a copy of a file's content.
func (f *FS) ReadFile(name string) ([]byte, error) {
	data, ok := f.files[name]
	if !ok || !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrNotExist}
	}
	return bytes.Clone(data), nil
}

// ReadDir lists a directory, sorted by name.
func (f *FS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, ok := f.dirs[name]
	if !ok || !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	return slices.Clone(entries), nil
}

// Stat describes a file or a directory.
func (f *FS) Stat(name string) (fs.FileInfo, error) {
	_, isFile := f.files[name]
	_, isDir := f.dirs[name]
	if !fs.ValidPath(name) || (!isFile && !isDir) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return f.info(name), nil
}

// info describes a file or a directory; it is both fs.FileInfo and fs.DirEntry.
type info struct {
	name string
	size int64
	dir  bool
}

func (i *info) Name() string               { return i.name }
func (i *info) Size() int64                { return i.size }
func (i *info) ModTime() time.Time         { return time.Time{} }
func (i *info) IsDir() bool                { return i.dir }
func (i *info) Sys() any                   { return nil }
func (i *info) Info() (fs.FileInfo, error) { return i, nil }
func (i *info) Type() fs.FileMode          { return i.Mode().Type() }

func (i *info) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}

type file struct {
	info *info
	r    *bytes.Reader
}

func (f *file) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *file) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *file) Close() error               { return nil }

type dir struct {
	info    *info
	entries []fs.DirEntry
	offset  int
}

func (d *dir) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *dir) Close() error               { return nil }

func (d *dir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.name, Err: fs.ErrInvalid}
}

// ReadDir returns the next n entries, or all remaining ones when n <= 0.
func (d *dir) ReadDir(n int) ([]fs.DirEntry, error) {
	rest := d.entries[d.offset:]
	if n <= 0 {
		d.offset = len(d.entries)
		return slices.Clone(rest), nil
	}
	if len(rest) == 0 {
		return nil, io.EOF
	}
	n = min(n, len(rest))
	d.offset += n
	return slices.Clone(rest[:n]), nil
}
