package raw_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

// pythonColumns are the columns that hold Python literals in the raw tables.
var pythonColumns = []string{
	"ResourceAttributes", "ScopeAttributes", "LogAttributes", "SpanAttributes",
	"Events.Timestamp", "Events.Name", "Events.Attributes",
	"Links.TraceId", "Links.SpanId", "Links.TraceState", "Links.Attributes", "tags",
}

// TestEveryRawTableParses reads every table of one downloaded scenario, set by
// ITBENCH_RAW_SCENARIO, and parses every Python-literal column.
func TestEveryRawTableParses(t *testing.T) {
	dir := os.Getenv("ITBENCH_RAW_SCENARIO")
	if dir == "" {
		t.Skip("ITBENCH_RAW_SCENARIO is not set")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.tsv"))
	require.NoError(t, err)
	more, err := filepath.Glob(filepath.Join(dir, "metrics", "*.tsv"))
	require.NoError(t, err)
	files = append(files, more...)
	require.NotEmpty(t, files)

	for _, path := range files {
		f, err := os.Open(path)
		require.NoError(t, err)
		rows := 0
		err = raw.ReadTSV(f, func(r raw.Row) error {
			rows++
			for _, col := range pythonColumns {
				if v := r.Get(col); v != "" {
					if _, err := raw.ParsePython(v); err != nil {
						t.Errorf("%s row %d column %s: %v: %.120s", filepath.Base(path), rows, col, err, v)
					}
				}
			}
			for _, col := range []string{"Timestamp", "timestamp"} {
				if v := r.Get(col); v != "" {
					if _, err := raw.ParseTime(v); err != nil {
						t.Errorf("%s row %d: %v", filepath.Base(path), rows, err)
					}
				}
			}
			return nil
		})
		assert.NoError(t, f.Close())
		require.NoError(t, err, path)
		assert.Positive(t, rows, path)
		t.Logf("%s: %d rows", strings.TrimPrefix(path, dir), rows)
	}
}
