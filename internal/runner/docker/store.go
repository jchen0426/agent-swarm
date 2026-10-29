package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// runtimeRepository is the repository of the runtime images' tags: make
// image tags an image agentswarm-runtime:<its id's 64 hex digits>, and so
// does a reload (Contract C12).
const runtimeRepository = "agentswarm-runtime"

// imageIDForm is an image's id, as the engine gives it and an item locks it.
var imageIDForm = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// store keeps a copy of each runtime image a deployment runs, as docker save
// writes it by id, so that an image the engine lost to a prune comes back
// with the id an item locked (Decision 6; Contract C12). A copy is the file
// <dir>/<64 hex digits>.tar (R8). Serve deletes nothing from the store.
type store struct{ dir string }

// file returns the path of id's copy and id's hex digits, or fails for an id
// that is not an image's, which names no file of the store.
func (s store) file(id string) (path, digits string, err error) {
	if !imageIDForm.MatchString(id) {
		return "", "", fmt.Errorf("docker: %q is not an image's id, sha256: and 64 hexadecimal digits", id)
	}
	digits = strings.TrimPrefix(id, "sha256:")
	return filepath.Join(s.dir, digits+".tar"), digits, nil
}

// has reports whether the store holds a copy of id.
func (s store) has(id string) bool {
	path, _, err := s.file(id)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// save writes the engine's copy of id to the store: to a temporary file
// beside its own, .<hex>.tar.<random>, which, once whole and synced, is
// renamed <hex>.tar, so that the store holds a copy whole or not at all. A
// save that fails leaves no file.
func (s store) save(ctx context.Context, e Engine, id string) (err error) {
	path, digits, err := s.file(id)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "."+digits+".tar.*")
	if err != nil {
		return fmt.Errorf("docker: save the image %s to the store: %w", id, err)
	}
	defer func() {
		if err != nil {
			_, _ = tmp.Close(), os.Remove(tmp.Name()) //nolint:gosec // G703: the temporary file the save just made, in the store.
		}
	}()
	if err = e.Save(ctx, id, tmp); err != nil {
		return fmt.Errorf("docker: save the image %s to the store: %w", id, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("docker: save the image %s to the store: %w", id, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("docker: save the image %s to the store: %w", id, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil { //nolint:gosec // G703: the save's temporary file, to the image's copy in the store, named by its id's digits.
		return fmt.Errorf("docker: save the image %s to the store: %w", id, err)
	}
	return nil
}

// reload loads <hex>.tar, checks that the engine then holds id, and tags it
// agentswarm-runtime:<hex>, as make image does, since a loaded image is
// untagged, and a plain prune removes untagged images. It returns the image
// the engine then holds. A copy that gives back another id fails the
// reload, and nothing is tagged.
func (s store) reload(ctx context.Context, e Engine, id string) (Image, error) {
	path, digits, err := s.file(id)
	if err != nil {
		return Image{}, err
	}
	f, err := os.Open(path) //nolint:gosec // G304: the store's copy of an image, named by its id's digits.
	if err != nil {
		return Image{}, fmt.Errorf("docker: reload the image %s from the store: %w", id, err)
	}
	defer func() { _ = f.Close() }()
	if err := e.Load(ctx, f); err != nil {
		return Image{}, fmt.Errorf("docker: reload the image %s from the store: %w", id, err)
	}
	loaded, err := e.Image(ctx, id)
	if err != nil {
		return Image{}, fmt.Errorf("docker: reload the image %s from the store: the engine does not hold it after the load: %w", id, err)
	}
	if loaded.ID != id {
		return Image{}, fmt.Errorf("docker: reload the image %s from the store: the store's copy gave back the image %s", id, loaded.ID)
	}
	if err := e.Tag(ctx, id, runtimeRepository+":"+digits); err != nil {
		return Image{}, fmt.Errorf("docker: tag the image %s, reloaded from the store: %w", id, err)
	}
	return loaded, nil
}
