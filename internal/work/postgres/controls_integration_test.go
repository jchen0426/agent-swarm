//go:build integration

package postgres_test

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/audit/chain"
	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/control"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var _ app.Controls = (*postgres.Controls)(nil)

// incident is the reason the tests' requests give.
var incident = board.Reason{Code: "incident", Text: "checkout is down"}

// tenantOf returns a tenant of the test's own: its name and a random suffix,
// in the anchors' form, lower case, every other character a dash, at most 63
// characters, as chain.ValidTenant takes. The package's tests share one
// Postgres, and the kill switch numbers its rows per tenant, so each test,
// and each run of it under -count, numbers its own.
func tenantOf(t *testing.T) string {
	t.Helper()
	suffix := strconv.FormatUint(rand.Uint64(), 36)
	name := strings.Map(func(r rune) rune {
		if 'a' <= r && r <= 'z' || '0' <= r && r <= '9' {
			return r
		}
		return '-'
	}, strings.ToLower(t.Name()))
	tenant := name[:min(len(name), 62-len(suffix))] + "-" + suffix
	require.True(t, chain.ValidTenant(tenant), tenant)
	return tenant
}

// appendRow turns the kill switch on or off, naming after as the latest row,
// and requires that the request appended a row.
func appendRow(t *testing.T, controls *postgres.Controls, on bool, after int64) control.KillSwitch {
	t.Helper()
	state, changed, err := controls.SetKillSwitch(t.Context(), on, "admin", incident, after)
	require.NoError(t, err)
	require.True(t, changed)
	return state
}

// latest returns the number of the kill switch's latest row.
func latest(t *testing.T, controls *postgres.Controls) int64 {
	t.Helper()
	seq, err := controls.LatestKillSwitch(t.Context())
	require.NoError(t, err)
	return seq
}

// rowTimes returns the times of a tenant's kill switch rows, by their number,
// as the table holds them.
func rowTimes(t *testing.T, pool *pgxpool.Pool, tenant string) map[int64]time.Time {
	t.Helper()
	rows, err := pool.Query(t.Context(), "SELECT seq, recorded_at FROM kill_switch WHERE tenant_id = $1", tenant)
	require.NoError(t, err)
	at := map[int64]time.Time{}
	var (
		seq      int64
		recorded time.Time
	)
	_, err = pgx.ForEachRow(rows, []any{&seq, &recorded}, func() error {
		at[seq] = recorded.UTC()
		return nil
	})
	require.NoError(t, err)
	return at
}

// No row means off (Decision 5): on a tenant whose switch was never set, the
// state is off, with no row, no on-row and zero times, and the latest row is
// numbered 0.
func TestTheKillSwitchIsOffUntilFirstSet(t *testing.T) {
	controls := postgres.NewControls(pgtest.Pool(t), tenantOf(t))

	state, err := controls.KillSwitch(t.Context())

	require.NoError(t, err)
	assert.Equal(t, control.KillSwitch{}, state)
	assert.Zero(t, latest(t, controls))
}

// The kill switch is written under the holding (Decision 5): a guarded store
// writes under the lease's current epoch; under an earlier epoch, its write is
// refused with the holding's ErrLeaseLost, which the coordinator's matches
// too, and writes no row; and a write whose context carries no epoch is a bug,
// refused with ErrNoEpoch, which never fences.
func TestTheKillSwitchIsWrittenOnlyUnderTheHolding(t *testing.T) {
	pool := pgtest.Pool(t)
	tenant := tenantOf(t)
	lease := postgres.NewLease(pool, tenant, "platform", "a", time.Minute, takeWait)
	first := take(t, lease)
	require.NoError(t, lease.Release(t.Context()))
	second := take(t, lease)
	controls := postgres.NewControls(pool, tenant).Guarded(lease)

	_, _, earlier := controls.SetKillSwitch(under(t, first), true, "admin", incident, 0)
	_, _, noEpoch := controls.SetKillSwitch(t.Context(), true, "admin", incident, 0)

	require.ErrorIs(t, earlier, holding.ErrLeaseLost)
	require.ErrorIs(t, earlier, app.ErrLeaseLost)
	require.ErrorIs(t, noEpoch, holding.ErrNoEpoch)
	require.NotErrorIs(t, noEpoch, holding.ErrLeaseLost)
	assert.Zero(t, latest(t, controls), "no row was written")
	state, changed, err := controls.SetKillSwitch(under(t, second), true, "admin", incident, 0)
	require.NoError(t, err, "a write under the current epoch lands")
	assert.True(t, changed)
	assert.Equal(t, int64(1), state.Seq)
}

// Requests are serialized by the tenant's lock of the switch (Decision 5): of
// ten ons sent at once, each naming row 0, one appends row 1, and each of the
// others reads the switch on, its state held already, with no error.
func TestKillSwitchRequestsAreSerialized(t *testing.T) {
	controls := postgres.NewControls(pgtest.Pool(t), tenantOf(t))
	var (
		wg       sync.WaitGroup
		appended atomic.Int32
	)
	for range 10 {
		wg.Go(func() {
			state, changed, err := controls.SetKillSwitch(t.Context(), true, "admin", incident, 0)
			if assert.NoError(t, err) && assert.Equal(t, int64(1), state.Seq) && changed {
				appended.Add(1)
			}
		})
	}
	wg.Wait()

	assert.Equal(t, int32(1), appended.Load())
	assert.Equal(t, int64(1), latest(t, controls))
}

// The kill switch's rows are audit records (Contract C17): each row is chained
// as it is written, with the empty scope, as the next link of the tenant's
// chain; an UPDATE, a DELETE and a TRUNCATE of the table are refused.
func TestTheKillSwitchsRowsAreAppendOnlyAndChained(t *testing.T) {
	pool := pgtest.Pool(t)
	tenant := tenantOf(t)
	controls := postgres.NewControls(pool, tenant)
	appendRow(t, controls, true, 0)
	appendRow(t, controls, false, 1)

	for _, refused := range []struct {
		sql  string
		args []any
	}{
		{"UPDATE kill_switch SET actor = 'someone' WHERE tenant_id = $1", []any{tenant}},
		{"DELETE FROM kill_switch WHERE tenant_id = $1", []any{tenant}},
		{"TRUNCATE kill_switch", nil},
	} {
		_, err := pool.Exec(t.Context(), refused.sql, refused.args...)
		require.ErrorContains(t, err, "kill_switch is append-only", refused.sql)
	}

	type link struct {
		Seq                int64
		Source, Key, Scope string
	}
	rows, err := pool.Query(t.Context(), "SELECT seq, source, key, run_id FROM audit_chain WHERE tenant_id = $1 ORDER BY seq", tenant)
	require.NoError(t, err)
	links, err := pgx.CollectRows(rows, pgx.RowToStructByPos[link])
	require.NoError(t, err)
	assert.Equal(t, []link{{1, "kill_switch", "[1]", ""}, {2, "kill_switch", "[2]", ""}}, links)
	assert.Equal(t, int64(2), latest(t, controls), "the rows stand")
}

// A request names the latest row its sender saw (R8): with rows 1, on, and 2,
// off, an on naming row 1 is stale. It fails with ErrStale, which the
// coordinator's matches too, writes nothing, and comes with the state of row
// 2, which the coordinator's view follows; an on naming row 2 writes row 3.
func TestAKillSwitchRowNamingAnOlderRowIsStale(t *testing.T) {
	controls := postgres.NewControls(pgtest.Pool(t), tenantOf(t))
	appendRow(t, controls, true, 0)
	row2 := appendRow(t, controls, false, 1)

	state, changed, err := controls.SetKillSwitch(t.Context(), true, "admin", incident, 1)

	require.ErrorIs(t, err, control.ErrStale)
	require.ErrorIs(t, err, app.ErrStale)
	assert.False(t, changed)
	assert.Equal(t, row2, state, "the state of row 2")
	assert.Equal(t, int64(2), latest(t, controls), "no row was written")
	row3 := appendRow(t, controls, true, 2)
	assert.Equal(t, int64(3), row3.Seq)
	assert.True(t, row3.On)
}

// Every answer carries the switch's state after it (Decision 5): a write that
// appends, one whose state held already, and a stale one each return the
// state the rows give after them: the latest row's number, writer, reason and
// time, the latest on-row's number and the latest off-row's time. A read of
// the switch gives the same.
func TestEveryKillSwitchAnswerCarriesTheStateAfterIt(t *testing.T) {
	pool := pgtest.Pool(t)
	tenant := tenantOf(t)
	controls := postgres.NewControls(pool, tenant)
	ctx := t.Context()
	resolved := board.Reason{Code: "resolved"}

	first, _, err := controls.SetKillSwitch(ctx, true, "alice", incident, 0)
	require.NoError(t, err)
	second, _, err := controls.SetKillSwitch(ctx, false, "admin", resolved, 1)
	require.NoError(t, err)
	third, _, err := controls.SetKillSwitch(ctx, true, "bob", incident, 2)
	require.NoError(t, err)
	held, changed, err := controls.SetKillSwitch(ctx, true, "carol", incident, 0)
	require.NoError(t, err)
	require.False(t, changed)
	stale, changed, err := controls.SetKillSwitch(ctx, false, "admin", resolved, 2)
	require.ErrorIs(t, err, control.ErrStale)
	require.False(t, changed)
	read, err := controls.KillSwitch(ctx)
	require.NoError(t, err)

	at := rowTimes(t, pool, tenant)
	require.Len(t, at, 3)
	assert.Equal(t, control.KillSwitch{On: true, Seq: 1, OnSeq: 1, By: "alice", Reason: incident, Since: at[1]}, first, "the on that appended row 1")
	assert.Equal(t, control.KillSwitch{Seq: 2, OnSeq: 1, OffAt: at[2], By: "admin", Reason: resolved, Since: at[2]}, second, "the off that appended row 2")
	row3 := control.KillSwitch{On: true, Seq: 3, OnSeq: 3, OffAt: at[2], By: "bob", Reason: incident, Since: at[3]}
	assert.Equal(t, row3, third, "the on that appended row 3")
	assert.Equal(t, row3, held, "an on whose state held already")
	assert.Equal(t, row3, stale, "a stale off")
	assert.Equal(t, row3, read, "a read of the switch")
}

// setCase is a request to the kill switch, after the rows written before it,
// and its answer: whether it appended a row, whether it was stale, and the
// state after it. The memory store's test repeats these cases (R22).
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

// A request appends a row only when it changes the state and names the latest
// row (Decision 5), and its answer carries the state, whatever it did: changed
// and no error when it appended; unchanged and no error when its state held
// already, whatever its after, and nothing appended; unchanged, with ErrStale,
// when it would change the state but its after is not the latest row's number.
func TestSetKillSwitchReportsChangedAndStaleWithTheState(t *testing.T) {
	for _, c := range setCases {
		t.Run(c.name, func(t *testing.T) {
			controls := postgres.NewControls(pgtest.Pool(t), tenantOf(t))
			for i, on := range c.rows {
				appendRow(t, controls, on, int64(i))
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
}
