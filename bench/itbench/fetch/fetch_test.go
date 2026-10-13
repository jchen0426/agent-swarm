package fetch_test

import (
	"context"
	"crypto/sha1" //nolint:gosec // git blob ids are SHA-1; the test computes one.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/fetch"
)

const (
	revision = "d0916b08ba421ce5e672e9ad68aa947d938dfef0"
	prefix   = "snapshots/sre/v0.2"
)

func blobID(content string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content))) //nolint:gosec // see the import.
	return hex.EncodeToString(sum[:])
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// hub stands in for the Hugging Face hub: a tree listing in two pages, and
// file contents.
type hub struct {
	listing  []map[string]any
	contents map[string]string
	gets     atomic.Int32
}

func (h *hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	treePrefix := "/api/datasets/org/bench/tree/" + revision + "/"
	resolvePrefix := "/datasets/org/bench/resolve/" + revision + "/"
	switch {
	case strings.HasPrefix(r.URL.Path, treePrefix):
		page := h.listing[:1]
		if r.URL.Query().Get("cursor") == "2" {
			page = h.listing[1:]
		} else {
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?recursive=true&cursor=2>; rel="next"`, r.Host, r.URL.Path))
		}
		_ = json.NewEncoder(w).Encode(page)
	case strings.HasPrefix(r.URL.Path, resolvePrefix):
		h.gets.Add(1)
		content, ok := h.contents[strings.TrimPrefix(r.URL.Path, resolvePrefix)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(content))
	default:
		http.NotFound(w, r)
	}
}

func newHub(t *testing.T, small, large, largeID string) (*hub, *fetch.Source) {
	t.Helper()
	h := &hub{
		listing: []map[string]any{
			{"type": "file", "path": prefix + "/Scenario-1/ground_truth.yaml", "size": len(small), "oid": blobID(small)},
			{"type": "file", "path": prefix + "/Scenario-1/metrics/pod_x_raw.tsv", "size": len(large), "oid": "x",
				"lfs": map[string]any{"oid": largeID, "size": len(large)}},
		},
		contents: map[string]string{prefix + "/Scenario-1/ground_truth.yaml": small, prefix + "/Scenario-1/metrics/pod_x_raw.tsv": large},
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	src, err := fetch.ParseSource(srv.URL+"/datasets/org/bench", revision)
	require.NoError(t, err)
	return h, src
}

func TestScenarioDownloadsAndChecksEveryFile(t *testing.T) {
	h, src := newHub(t, "fault: x\n", "a\tb\n1\t2\n", sha256Hex("a\tb\n1\t2\n"))
	dest := t.TempDir()

	n, err := src.Scenario(context.Background(), prefix, "Scenario-1", dest)

	require.NoError(t, err)
	assert.Equal(t, 2, n)
	got, err := os.ReadFile(filepath.Join(dest, "Scenario-1", "metrics", "pod_x_raw.tsv"))
	require.NoError(t, err)
	assert.Equal(t, "a\tb\n1\t2\n", string(got))
	assert.Equal(t, int32(2), h.gets.Load())

	_, err = src.Scenario(context.Background(), prefix, "Scenario-1", dest)
	require.NoError(t, err)
	assert.Equal(t, int32(2), h.gets.Load(), "files already present with the right hash are not fetched again")
}

func TestAHashMismatchFailsAndLeavesNoFile(t *testing.T) {
	_, src := newHub(t, "fault: x\n", "a\tb\n", sha256Hex("something else"))
	dest := t.TempDir()

	_, err := src.Scenario(context.Background(), prefix, "Scenario-1", dest)

	require.ErrorContains(t, err, "sha256")
	_, statErr := os.Stat(filepath.Join(dest, "Scenario-1", "metrics", "pod_x_raw.tsv"))
	assert.True(t, os.IsNotExist(statErr))
	leftovers, _ := filepath.Glob(filepath.Join(dest, "Scenario-1", "metrics", "*"))
	assert.Empty(t, leftovers)
}

func TestAListingThatEscapesTheScenarioIsRefused(t *testing.T) {
	h, src := newHub(t, "fault: x\n", "a\n", sha256Hex("a\n"))
	h.listing[1]["path"] = prefix + "/Scenario-1/../../escape.tsv"

	_, err := src.Scenario(context.Background(), prefix, "Scenario-1", t.TempDir())

	require.ErrorContains(t, err, "outside")
}

func TestParseSourceNeedsADatasetURL(t *testing.T) {
	_, err := fetch.ParseSource("https://huggingface.co/models/org/x", revision)
	require.Error(t, err)
	_, err = fetch.ParseSource("https://huggingface.co/datasets/org/bench", "main")
	require.ErrorContains(t, err, "commit")
}
