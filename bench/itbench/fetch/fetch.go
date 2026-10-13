// Package fetch downloads scenario directories from a Hugging Face dataset at
// a pinned commit, and checks every file against the hub's hash.
package fetch

import (
	"context"
	"crypto/sha1" //nolint:gosec // the hub identifies small files by their git blob id, which is SHA-1.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	commit   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)
)

// maxPages bounds a listing, so that a hub that keeps linking cannot loop.
const maxPages = 100

// Source is a dataset on the hub at one commit.
type Source struct {
	base, repo, revision string
	client               *http.Client
}

// ParseSource reads a dataset URL such as
// https://huggingface.co/datasets/org/name. The revision must be a commit, so
// that every run reads the same bytes.
func ParseSource(dataset, revision string) (*Source, error) {
	u, err := url.Parse(dataset)
	if err != nil {
		return nil, err
	}
	repo, ok := strings.CutPrefix(u.Path, "/datasets/")
	if !ok || strings.Count(strings.Trim(repo, "/"), "/") != 1 {
		return nil, fmt.Errorf("%s is not a dataset URL like https://huggingface.co/datasets/org/name", dataset)
	}
	if !commit.MatchString(revision) {
		return nil, fmt.Errorf("revision %q is not a commit id", revision)
	}
	return &Source{base: u.Scheme + "://" + u.Host, repo: strings.Trim(repo, "/"), revision: revision, client: &http.Client{}}, nil
}

// File is one file as the hub lists it. LFS is set for files stored in LFS,
// whose id is their SHA-256; other files' id is their git blob id.
type File struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	OID  string `json:"oid"`
	LFS  *struct {
		OID  string `json:"oid"`
		Size int64  `json:"size"`
	} `json:"lfs"`
}

// list returns every file under dir.
func (s *Source) list(ctx context.Context, dir string) ([]File, error) {
	next := fmt.Sprintf("%s/api/datasets/%s/tree/%s/%s?recursive=true", s.base, s.repo, s.revision, dir)
	var out []File
	for page := 0; next != ""; page++ {
		if page == maxPages {
			return nil, fmt.Errorf("listing %s: more than %d pages", dir, maxPages)
		}
		var files []File
		link, err := s.getJSON(ctx, next, &files)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", dir, err)
		}
		out = append(out, files...)
		next = ""
		if m := nextLink.FindStringSubmatch(link); m != nil {
			next = m[1]
		}
	}
	return out, nil
}

func (s *Source) getJSON(ctx context.Context, target string, v any) (link string, err error) {
	resp, err := s.get(ctx, target)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	return resp.Header.Get("Link"), json.NewDecoder(resp.Body).Decode(v)
}

func (s *Source) get(ctx context.Context, target string) (*http.Response, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	if u.Scheme+"://"+u.Host != s.base {
		return nil, fmt.Errorf("%s is not on %s", target, s.base)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req) //nolint:gosec // G704: the host was checked against the set's source above.
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", target, resp.Status)
	}
	return resp, nil
}

// Scenario downloads the files under prefix/dir into destRoot/dir, skipping
// files already there with the right hash, and returns how many files the
// directory holds.
func (s *Source) Scenario(ctx context.Context, prefix, dir, destRoot string) (int, error) {
	files, err := s.list(ctx, prefix+"/"+dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range files {
		f := &files[i]
		if f.Type != "file" {
			continue
		}
		rel := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(f.Path, prefix+"/")))
		if !strings.HasPrefix(f.Path, prefix+"/") || !filepath.IsLocal(rel) || !strings.HasPrefix(rel, dir+string(filepath.Separator)) {
			return n, fmt.Errorf("%s is outside %s", f.Path, dir)
		}
		if err := s.download(ctx, f, filepath.Join(destRoot, rel)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// download writes the file to dest, unless dest already holds it.
func (s *Source) download(ctx context.Context, f *File, dest string) (err error) {
	if ok, err := matches(f, dest); err != nil || ok {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	resp, err := s.get(ctx, fmt.Sprintf("%s/datasets/%s/resolve/%s/%s", s.base, s.repo, s.revision, f.Path))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	part := dest + ".part"
	out, err := os.Create(filepath.Clean(part))
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(part)
		}
	}()
	_, copyErr := io.Copy(out, resp.Body)
	if err := errors.Join(copyErr, out.Close()); err != nil {
		return err
	}
	if ok, err := matches(f, part); err != nil || !ok {
		return errors.Join(err, fmt.Errorf("%s: size or %s does not match the hub", f.Path, hashName(f)))
	}
	return os.Rename(part, dest)
}

// matches reports whether path holds the file the hub lists.
func matches(f *File, path string) (_ bool, err error) {
	in, err := os.Open(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, in.Close()) }()
	h, want := sha256.New(), ""
	if f.LFS != nil {
		want = f.LFS.OID
	} else {
		h, want = sha1.New(), f.OID //nolint:gosec // see the import.
		_, _ = fmt.Fprintf(h, "blob %d\x00", f.Size)
	}
	n, err := io.Copy(h, in)
	if err != nil {
		return false, err
	}
	return n == f.Size && sumOf(h) == want, nil
}

func sumOf(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }

func hashName(f *File) string {
	if f.LFS != nil {
		return "sha256"
	}
	return "git blob id"
}
