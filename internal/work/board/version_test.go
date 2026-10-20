package board_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// A body written before board events carried a version holds none: it reads
// as version 1, whole (Decision 10).
func TestAnEventBodyWithoutAVersionReadsAsVersion1(t *testing.T) {
	var b board.Body

	require.NoError(t, json.Unmarshal([]byte(`{"task": {"title": "check the deploy", "tags": ["deploy"]}}`), &b))

	assert.Equal(t, board.Body{V: 1, Task: &board.Task{Title: "check the deploy", Tags: []string{"deploy"}}}, b)
	e := board.Event{ID: "e-1", Body: b}
	require.NoError(t, e.CheckVersion(), "this build reads it")
}

// A version that is present and below 1 is invalid, as is one that is no
// number: versions start at 1, so such a body is corrupt, not of a later
// version. A body of this version that does not decode is invalid too.
func TestAnEventVersionBelow1IsInvalid(t *testing.T) {
	for name, raw := range map[string]string{
		"version zero":                   `{"v": 0, "task": {"title": "check the deploy"}}`,
		"a negative version":             `{"v": -1}`,
		"a null version":                 `{"v": null, "task": {"title": "check the deploy"}}`,
		"a version written as text":      `{"v": "2"}`,
		"a body of this version, broken": `{"v": 1, "task": "not a task"}`,
	} {
		var b board.Body

		err := json.Unmarshal([]byte(raw), &b)

		require.ErrorIs(t, err, board.ErrInvalid, name)
		assert.NotErrorIs(t, err, board.ErrVersion, name)
	}
}

// A body of a later version decodes to its version alone, whatever else it
// holds, since a later build may write a body this one cannot decode; such an
// event is refused with ErrVersion, which no event of this version is.
func TestAnEventOfALaterVersionDecodesToItsVersionAlone(t *testing.T) {
	var later board.Body

	require.NoError(t, json.Unmarshal([]byte(`{"v": 2, "task": "a shape this build does not know", "verdict": {"by": "a later build"}}`), &later))

	assert.Equal(t, board.Body{V: 2}, later)
	e := board.Event{Seq: 3, ID: "e-3", Body: later}
	err := e.CheckVersion()
	require.ErrorIs(t, err, board.ErrVersion)
	require.NotErrorIs(t, err, board.ErrInvalid)
	assert.Contains(t, err.Error(), "e-3", "the error names the entry")
}

// This build writes version 1: every entry the board's rules make carries it,
// whoever posts it and whatever its kind, and its stored body says so.
func TestThisBuildWritesEventsAtVersion1(t *testing.T) {
	f := newFixture(t)
	f.must(f.state.StartRound(&f.ctx, f.nextID("round"), board.Coordinator, &board.RoundStart{Round: 1}))
	ev := f.evidence(release, "cd", "get_history", "v42 deployed at 10:02", "", "")
	h := f.hypothesis(release, checkoutDeploy(), ev)
	f.evidence(runtime, "logs", "search", "errors rose at 10:03", h, board.Supports)
	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})
	task := f.task(release, "check the pool", "db")
	_, err := f.claim(runtime, task)
	require.NoError(t, err)
	f.must(f.state.Notify(&f.ctx, f.nextID("notice"), board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: release.Name}))
	f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), release))
	f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, &board.StopInput{Reason: "quiet", Rounds: 1}))

	require.Len(t, f.events, 10)
	for _, e := range f.events {
		assert.Equal(t, 1, e.Body.V, "%s entry %s", e.Body.Kind(), e.ID)
		raw, err := json.Marshal(e.Body)
		require.NoError(t, err)
		var stored struct {
			V json.RawMessage `json:"v"`
		}
		require.NoError(t, json.Unmarshal(raw, &stored))
		assert.Equal(t, "1", string(stored.V), "%s entry %s, as stored: %s", e.Body.Kind(), e.ID, raw)
	}
}
