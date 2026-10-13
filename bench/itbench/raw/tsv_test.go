package raw_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/bench/itbench/raw"
)

func TestReadTSVHandlesQuotingAndEmbeddedNewlines(t *testing.T) {
	in := "Timestamp\tBody\tServiceName\n" +
		"2025-12-15 17:24:14.846190080\t\"{\"\"a\"\": 1}\"\tcart\n" +
		"2025-12-15 17:24:15\t\"line one\n\tline two\"\tad\n"

	var got [][]string
	err := raw.ReadTSV(strings.NewReader(in), func(r raw.Row) error {
		got = append(got, []string{r.Get("Timestamp"), r.Get("Body"), r.Get("ServiceName"), r.Get("NoSuchColumn")})
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, [][]string{
		{"2025-12-15 17:24:14.846190080", `{"a": 1}`, "cart", ""},
		{"2025-12-15 17:24:15", "line one\n\tline two", "ad", ""},
	}, got)
}

func TestReadTSVReportsAShortRow(t *testing.T) {
	err := raw.ReadTSV(strings.NewReader("a\tb\n1\t2\n3\n"), func(raw.Row) error { return nil })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 3")
}

func TestReadTSVStopsOnTheCallbacksError(t *testing.T) {
	stop := errors.New("stop")
	rows := 0

	err := raw.ReadTSV(strings.NewReader("a\n1\n2\n"), func(raw.Row) error {
		rows++
		return stop
	})

	require.ErrorIs(t, err, stop)
	assert.Equal(t, 1, rows)
}

func TestParseTimeReadsTheExportFormats(t *testing.T) {
	want := time.Date(2025, 12, 15, 17, 24, 14, 846190080, time.UTC)
	for _, s := range []string{"2025-12-15 17:24:14.846190080", "2025-12-15T17:24:14.846190080Z"} {
		got, err := raw.ParseTime(s)
		require.NoError(t, err, s)
		assert.True(t, want.Equal(got), "%s: got %v", s, got)
	}
	_, err := raw.ParseTime("yesterday")
	assert.Error(t, err)
}
