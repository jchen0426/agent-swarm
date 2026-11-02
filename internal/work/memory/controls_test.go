package memory_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/control"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/memory"
)

var _ app.Controls = (*memory.Controls)(nil)

// incident is the reason the tests' requests give.
var incident = board.Reason{Code: "incident", Text: "checkout is down"}

// setCase is a request to the kill switch, after the rows written before it,
// and its answer: whether it appended a row, whether it was stale, and the
// state after it. setCase, shape and setCases repeat, case for case, those of
// the Postgres store's TestSetKillSwitchReportsChangedAndStaleWithTheState
// (internal/work/postgres).
type setCase struct {
	name    string
	rows    []bool // the rows written before, on or off, each naming the latest
	on      bool
	after   int64
	changed bool
	stale   bool
	want    shape
}

// shape is what a case checks of a state: whether it is on, the latest row's
// number, the latest on-row's, and whether an off-row stands.
type shape struct {
	On         bool
	Seq, OnSeq int64
	OffRow     bool
}

func shapeOf(s *control.KillSwitch) shape {
	return shape{On: s.On, Seq: s.Seq, OnSeq: s.OnSeq, OffRow: !s.OffAt.IsZero()}
}

var setCases = []setCase{
	{name: "on from off, after the latest", on: true, after: 0, changed: true, want: shape{On: true, Seq: 1, OnSeq: 1}},
	{name: "on from an off-row, after the latest", rows: []bool{true, false}, on: true, after: 2, changed: true,
		want: shape{On: true, Seq: 3, OnSeq: 3, OffRow: true}},
	{name: "on while on, after an older row", rows: []bool{true, false, true}, on: true, after: 1,
		want: shape{On: true, Seq: 3, OnSeq: 3, OffRow: true}},
	{name: "on while on, after the latest", rows: []bool{true}, on: true, after: 1, want: shape{On: true, Seq: 1, OnSeq: 1}},
	{name: "on while on, after a row to come", rows: []bool{true}, on: true, after: 5, want: shape{On: true, Seq: 1, OnSeq: 1}},
	{name: "off from on, after an older row", rows: []bool{true, false, true}, on: false, after: 2, stale: true,
		want: shape{On: true, Seq: 3, OnSeq: 3, OffRow: true}},
	{name: "off from on, after the latest", rows: []bool{true}, on: false, after: 1, changed: true, want: shape{Seq: 2, OnSeq: 1, OffRow: true}},
	{name: "on from off, after an older row", rows: []bool{true, false}, on: true, after: 1, stale: true,
		want: shape{Seq: 2, OnSeq: 1, OffRow: true}},
	{name: "off while off, before any row", on: false, after: 3},
	{name: "off while off, after an older row", rows: []bool{true, false}, on: false, after: 0, want: shape{Seq: 2, OnSeq: 1, OffRow: true}},
}

// latest returns the number of the kill switch's latest row.
func latest(t *testing.T, controls *memory.Controls) int64 {
	t.Helper()
	seq, err := controls.LatestKillSwitch(t.Context())
	require.NoError(t, err)
	return seq
}

// The memory twin of the kill switch's store, and memory.Records'
// UnstaffedBy, which the coordinator's tests run on, answer as the Postgres
// stores do (R22): every case of TestTheKillSwitchIsOffUntilFirstSet, of
// TestSetKillSwitchReportsChangedAndStaleWithTheState and of
// TestTheItemsAnOnLeftUnstaffedAreFoundByItsRow (internal/work/postgres),
// which it repeats. Each answer carries the state after it, as the Postgres
// store's do, with its rows' times from the store's clock.
func TestTheMemoryStoresAnswerAsThePostgresOnesDo(t *testing.T) {
	t.Run("the kill switch is off until first set", func(t *testing.T) {
		controls := memory.NewControls(time.Now)

		state, err := controls.KillSwitch(t.Context())

		require.NoError(t, err)
		assert.Equal(t, control.KillSwitch{}, state)
		assert.Zero(t, latest(t, controls))
	})

	for _, c := range setCases {
		t.Run(c.name, func(t *testing.T) {
			controls := memory.NewControls(time.Now)
			for i, on := range c.rows {
				_, changed, err := controls.SetKillSwitch(t.Context(), on, "admin", incident, int64(i))
				require.NoError(t, err)
				require.True(t, changed)
			}

			state, changed, err := controls.SetKillSwitch(t.Context(), c.on, "admin", incident, c.after)

			if c.stale {
				require.ErrorIs(t, err, control.ErrStale)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, c.changed, changed)
			assert.Equal(t, c.want, shapeOf(&state))
			assert.Equal(t, c.want.Seq, latest(t, controls), "the rows stand as the state says")
		})
	}

	t.Run("each answer carries the state after it, its times from the clock", func(t *testing.T) {
		at := func(minute int) time.Time { return time.Date(2026, 11, 2, 9, minute, 0, 0, time.UTC) }
		clock := at(0)
		controls := memory.NewControls(func() time.Time { return clock })
		ctx := t.Context()
		resolved := board.Reason{Code: "resolved"}

		first, _, err := controls.SetKillSwitch(ctx, true, "alice", incident, 0)
		require.NoError(t, err)
		clock = at(5)
		second, _, err := controls.SetKillSwitch(ctx, false, "admin", resolved, 1)
		require.NoError(t, err)
		clock = at(10)
		third, _, err := controls.SetKillSwitch(ctx, true, "bob", incident, 2)
		require.NoError(t, err)
		clock = at(15)
		held, changed, err := controls.SetKillSwitch(ctx, true, "carol", incident, 0)
		require.NoError(t, err)
		require.False(t, changed)
		stale, changed, err := controls.SetKillSwitch(ctx, false, "admin", resolved, 2)
		require.ErrorIs(t, err, control.ErrStale)
		require.False(t, changed)
		read, err := controls.KillSwitch(ctx)
		require.NoError(t, err)

		assert.Equal(t, control.KillSwitch{On: true, Seq: 1, OnSeq: 1, By: "alice", Reason: incident, Since: at(0)}, first, "the on that appended row 1")
		assert.Equal(t, control.KillSwitch{Seq: 2, OnSeq: 1, OffAt: at(5), By: "admin", Reason: resolved, Since: at(5)}, second, "the off that appended row 2")
		row3 := control.KillSwitch{On: true, Seq: 3, OnSeq: 3, OffAt: at(5), By: "bob", Reason: incident, Since: at(10)}
		assert.Equal(t, row3, third, "the on that appended row 3")
		assert.Equal(t, row3, held, "an on whose state held already")
		assert.Equal(t, row3, stale, "a stale off")
		assert.Equal(t, row3, read, "a read of the switch")
	})

	t.Run("the items an on left unstaffed are found by its row", func(t *testing.T) {
		records := memory.NewRecords(time.Now)
		for _, u := range []struct {
			id item.ID
			by int64
		}{{"item-c", 4}, {"item-b", 2}, {"item-a", 4}} {
			require.NoError(t, appendTo(t, records, u.id, opening, item.Body{AwaitingClose: &item.AwaitingClose{
				Reason: item.StopNotStaffed, Cause: item.CauseKillSwitch, UnstaffedBy: u.by,
			}}))
		}

		found, err := records.UnstaffedBy(t.Context(), 4)

		require.NoError(t, err)
		assert.Equal(t, []item.ID{"item-a", "item-c"}, found)
	})
}
