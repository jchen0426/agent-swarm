//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	auditapp "github.com/jchen0426/agent-swarm/internal/audit/app"
	auditchain "github.com/jchen0426/agent-swarm/internal/audit/chain"
	auditpg "github.com/jchen0426/agent-swarm/internal/audit/postgres"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var (
	_ app.Records      = (*postgres.Records)(nil)
	_ app.Runs         = (*postgres.Runs)(nil)
	_ app.ClosedLister = (*postgres.Records)(nil)
)

// opened is an item's first record, as an opening by key records it, with
// its request's digest.
func opened(id item.ID, rec string) item.Draft {
	return item.Draft{ID: rec, Item: id, Incident: "inc-" + item.IncidentID(id), Body: item.Body{V: item.Version, Opened: &item.Opened{
		Type: "incident", Title: "checkout fails", Source: "person", Arm: item.ArmVisible, How: item.NoWindow, Digest: "sha256:request",
	}}}
}

// started is the record that starts an item.
func started(id item.ID, rec string) item.Draft {
	return item.Draft{ID: rec, Item: id, Incident: "inc-" + item.IncidentID(id), Body: item.Body{V: item.Version, Started: &item.Started{
		Commit: "c1", Digest: "d1", Images: map[string]string{"thinloop": "sha256:aa"},
	}}}
}

// runCreated is the record that creates run for an item.
func runCreated(id item.ID, rec string, run board.RunID) item.Draft {
	return item.Draft{ID: rec, Item: id, Incident: "inc-" + item.IncidentID(id), Run: run, Body: item.Body{V: item.Version, RunCreated: &item.RunCreated{
		Class: item.ClassPrimary, Mode: item.ModeVisible, Settings: board.Settings{Convergence: board.DefaultConvergence, Limits: board.Limits{EntriesPerRound: 3}},
		Budget: item.Budget{WallClock: 15 * time.Minute, Tokens: 2_000_000, Rounds: 6}, RoundLimit: 150 * time.Second, Agents: []string{"release-investigator"},
	}}}
}

// awaitingClose is the record by which an item with no run awaits its close,
// not staffed.
func awaitingClose(id item.ID, rec string) item.Draft {
	return item.Draft{ID: rec, Item: id, Incident: "inc-" + item.IncidentID(id), Body: item.Body{V: item.Version, AwaitingClose: &item.AwaitingClose{
		Reason: item.StopNotStaffed,
	}}}
}

// closed is the record of an item's close, as c says.
func closed(id item.ID, rec string, c *item.Closed) item.Draft {
	return item.Draft{ID: rec, Item: id, Incident: "inc-" + item.IncidentID(id), Body: item.Body{V: item.Version, Closed: c}}
}

// byAdmin is a person's close through the admin API: it has no due.
var byAdmin = &item.Closed{By: "admin", Request: &item.Request{Key: "sha256:k1", Digest: "sha256:d1"}}

// reopened is the record of an item's reopen, which returns it to awaiting its
// close, not staffed.
func reopened(id item.ID, rec string) item.Draft {
	return item.Draft{ID: rec, Item: id, Incident: "inc-" + item.IncidentID(id), Body: item.Body{V: item.Version, Reopened: &item.Reopened{
		By: "admin", Request: item.Request{Key: "sha256:k2", Digest: "sha256:d2"}, AwaitingClose: item.AwaitingClose{Reason: item.StopNotStaffed},
	}}}
}

// unstaffed is the record by which the kill switch's on-row by leaves an item
// with no run not staffed.
func unstaffed(id item.ID, rec string, by int64) item.Draft {
	return item.Draft{ID: rec, Item: id, Incident: "inc-" + item.IncidentID(id), Body: item.Body{V: item.Version, AwaitingClose: &item.AwaitingClose{
		Reason: item.StopNotStaffed, Cause: item.CauseKillSwitch, UnstaffedBy: by,
	}}}
}

func appendDrafts(t *testing.T, store *postgres.Records, id item.ID, drafts ...item.Draft) []item.Record {
	t.Helper()
	committed, err := store.Append(t.Context(), id, func([]item.Record) ([]item.Draft, error) { return drafts, nil })
	require.NoError(t, err)
	return committed
}

// An item's records come back as they were committed, numbered from 1 in
// commit order, with the database's time of their commit.
func TestRecordsRoundTrip(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-a")

	committed := appendDrafts(t, store, "item-1", opened("item-1", "r1"), started("item-1", "r2"))
	more := appendDrafts(t, store, "item-1", runCreated("item-1", "r3", "run-1"))
	loaded, err := store.Load(t.Context(), "item-1")

	require.NoError(t, err)
	require.Len(t, loaded, 3)
	assert.Equal(t, []int64{1, 2, 3}, []int64{loaded[0].Seq, loaded[1].Seq, loaded[2].Seq})
	assert.Equal(t, committed[0].Body, loaded[0].Body, "the opening, its request's digest with it")
	assert.Equal(t, committed[1].Body, loaded[1].Body)
	assert.Equal(t, more[0].Body, loaded[2].Body)
	assert.Equal(t, item.IncidentID("inc-item-1"), loaded[2].Incident)
	assert.Equal(t, board.RunID("run-1"), loaded[2].Run)
	assert.True(t, committed[0].At.Equal(loaded[0].At))
	assert.False(t, loaded[2].At.Before(loaded[1].At))
	s, err := item.Fold(loaded)
	require.NoError(t, err)
	assert.Equal(t, item.StatusInvestigating, s.Status())
}

// A decision that fails commits nothing.
func TestAppendingCommitsNothingWhenDecideFails(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-a")
	refused := errors.New("refused by the rules")

	_, err := store.Append(t.Context(), "item-2", func([]item.Record) ([]item.Draft, error) { return nil, refused })

	require.ErrorIs(t, err, refused)
	loaded, err := store.Load(t.Context(), "item-2")
	require.NoError(t, err)
	assert.Empty(t, loaded)
}

// Appends to one item are serialized: each sees every record committed
// before it.
func TestAppendsToOneItemAreSerialized(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-a")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			_, err := store.Append(t.Context(), "item-3", func(history []item.Record) ([]item.Draft, error) {
				d := opened("item-3", fmt.Sprintf("c-%d", i))
				if len(history) > 0 {
					d.Body = item.Body{V: item.Version, AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}}
				}
				return []item.Draft{d}, nil
			})
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	loaded, err := store.Load(t.Context(), "item-3")
	require.NoError(t, err)
	require.Len(t, loaded, 20)
	assert.NotNil(t, loaded[0].Body.Opened, "only the first saw no history")
	for i := range loaded {
		assert.Equal(t, int64(i+1), loaded[i].Seq)
	}
}

// The coordinator drives the items that are not closed (Decision 12; R16):
// Active lists, by id, an item opened, one awaiting its close, and one closed
// and reopened; not one closed, nor one closed again after a reopen, whose
// close no reopen of a later seq follows.
func TestActiveItemsAreThoseNotClosed(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-active")
	appendDrafts(t, store, "item-a", opened("item-a", "a1"))
	appendDrafts(t, store, "item-b", opened("item-b", "b1"), awaitingClose("item-b", "b2"))
	appendDrafts(t, store, "item-c", opened("item-c", "c1"), awaitingClose("item-c", "c2"), closed("item-c", "c3", byAdmin))
	appendDrafts(t, store, "item-d", opened("item-d", "d1"), awaitingClose("item-d", "d2"), closed("item-d", "d3", byAdmin), reopened("item-d", "d4"))
	appendDrafts(t, store, "item-e", opened("item-e", "e1"), awaitingClose("item-e", "e2"), closed("item-e", "e3", byAdmin), reopened("item-e", "e4"),
		closed("item-e", "e5", byAdmin))

	active, err := store.Active(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []item.ID{"item-a", "item-b", "item-d"}, active)
}

// Active lists the opened items that are not closed: not an empty item, nor one of a later version.
func TestActiveItemsAreOpenedAndNotClosed(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-active-opened")
	appendDrafts(t, store, "item-a", opened("item-a", "a1"))
	appendDrafts(t, store, "item-empty")
	appendDrafts(t, store, "item-later", item.Draft{ID: "l1", Item: "item-later", Incident: "inc-item-later", Body: item.Body{V: item.Version + 1}})

	active, err := store.Active(t.Context())
	require.NoError(t, err)
	loaded, err := store.Load(t.Context(), "item-empty")
	require.NoError(t, err)

	assert.Equal(t, []item.ID{"item-a"}, active)
	assert.Empty(t, loaded)
}

// The off lists every item the latest on left unstaffed (Decision 5): of three
// items' item_awaiting_close records, two naming the on-row 4 and one the
// on-row 2, UnstaffedBy(4) finds the two, by id.
func TestTheItemsAnOnLeftUnstaffedAreFoundByItsRow(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), tenantOf(t))
	for _, u := range []struct {
		id item.ID
		by int64
	}{{"item-c", 4}, {"item-b", 2}, {"item-a", 4}} {
		appendDrafts(t, store, u.id, opened(u.id, string(u.id)+"-1"), unstaffed(u.id, string(u.id)+"-2", u.by))
	}

	found, err := store.UnstaffedBy(t.Context(), 4)

	require.NoError(t, err)
	assert.Equal(t, []item.ID{"item-a", "item-c"}, found)
}

// The store refuses an automatic close that it would stamp before its due, on
// the database's time, with item.ErrNotDue, in the append's own transaction,
// which writes nothing (R41): one due an hour after the database's clock is
// refused, and the item's records and the audit chain are as they were; one
// whose due has passed is taken; and a person's close, which has no due, is
// taken at any time. (The plan's.)
func TestTheRecordsRefuseAnAutomaticCloseStampedBeforeItsDue(t *testing.T) {
	pool := pgtest.Pool(t)
	store := postgres.NewRecords(pool, "records-due")
	for _, id := range []item.ID{"item-auto", "item-person"} {
		appendDrafts(t, store, id, opened(id, string(id)+"-1"), awaitingClose(id, string(id)+"-2"))
	}
	var now time.Time
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT clock_timestamp()").Scan(&now))
	before, err := store.Load(t.Context(), "item-auto")
	require.NoError(t, err)
	chain := chained(t, pool, "records-due")

	_, early := store.Append(t.Context(), "item-auto", func([]item.Record) ([]item.Draft, error) {
		return []item.Draft{closed("item-auto", "item-auto-3", &item.Closed{Auto: true, Due: now.Add(time.Hour)})}, nil
	})

	require.ErrorIs(t, early, item.ErrNotDue)
	after, err := store.Load(t.Context(), "item-auto")
	require.NoError(t, err)
	assert.Equal(t, before, after, "the item's records are as they were")
	assert.Equal(t, chain, chained(t, pool, "records-due"), "the chain's row rolled back with the record")
	taken := appendDrafts(t, store, "item-auto", closed("item-auto", "item-auto-3", &item.Closed{Auto: true, Due: now.Add(-time.Hour)}))
	assert.Equal(t, int64(3), taken[0].Seq, "a close whose due has passed")
	appendDrafts(t, store, "item-person", closed("item-person", "item-person-3", byAdmin))
}

// insertRaw writes an item's first record by hand, as a store holds a row no
// append wrote: its body is the JSON given.
func insertRaw(t *testing.T, pool *pgxpool.Pool, tenant string, id item.ID, body string) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `INSERT INTO work_records (tenant_id, item_id, seq, id, incident_id, run_id, kind, body, recorded_at)
		VALUES ($1, $2, 1, $3, $4, '', $5, $6::jsonb, now())`, tenant, string(id), "raw-"+string(id), "inc-"+string(id), string(item.KindOpened), body)
	require.NoError(t, err)
}

// A work record of this version whose body does not decode is invalid, as a
// record the fold refuses is: Load reports it wrapped with item.ErrInvalid,
// and keeps the decode error, so that a takeover can tell an item with a
// corrupt row from a read that failed, and carry on past the item.
func TestARecordOfThisVersionThatDoesNotDecodeIsInvalid(t *testing.T) {
	pool := pgtest.Pool(t)
	store := postgres.NewRecords(pool, "records-corrupt")
	insertRaw(t, pool, "records-corrupt", "item-corrupt", `{"v": 1, "opened": "not an opening"}`)

	_, err := store.Load(t.Context(), "item-corrupt")

	require.ErrorIs(t, err, item.ErrInvalid)
	var decode *json.UnmarshalTypeError
	require.ErrorAs(t, err, &decode, "the decode error is kept")
	assert.NotErrorIs(t, err, item.ErrVersion)
}

// A work record whose body holds no version is corrupt, not a later version:
// Load reports it wrapped with item.ErrInvalid, which a takeover lets by, and
// does not return it for the fold to refuse with item.ErrVersion, which makes
// the coordinator skip the item.
func TestARecordWithNoVersionIsInvalid(t *testing.T) {
	pool := pgtest.Pool(t)
	store := postgres.NewRecords(pool, "records-no-version")
	for id, body := range map[item.ID]string{"item-empty": `{}`, "item-null": `{"v": null}`, "item-zero": `{"v": 0}`} {
		insertRaw(t, pool, "records-no-version", id, body)

		_, err := store.Load(t.Context(), id)

		require.ErrorIs(t, err, item.ErrInvalid, body)
		assert.NotErrorIs(t, err, item.ErrVersion, body)
	}
}

// A work record of another version decodes to its version alone, whatever its
// body holds: Load returns it, and the fold refuses it with item.ErrVersion,
// which makes the coordinator skip the item, not with item.ErrInvalid, which a
// takeover lets by.
func TestARecordOfAnotherVersionLoadsAsItsVersionAlone(t *testing.T) {
	pool := pgtest.Pool(t)
	store := postgres.NewRecords(pool, "records-later")
	insertRaw(t, pool, "records-later", "item-later", `{"v": 2, "opened": "a shape this build does not know"}`)

	loaded, err := store.Load(t.Context(), "item-later")

	require.NoError(t, err)
	require.Len(t, loaded, 1)
	assert.Equal(t, item.Body{V: item.Version + 1}, loaded[0].Body)
	_, err = item.Fold(loaded)
	require.ErrorIs(t, err, item.ErrVersion)
	assert.NotErrorIs(t, err, item.ErrInvalid)
}

// A read that fails is not a corrupt row: Load does not report it as invalid,
// so that a takeover that could not read an item's records refuses the lease
// instead of letting the item by.
func TestAFailedReadOfRecordsIsNotInvalid(t *testing.T) {
	store := postgres.NewRecords(pgtest.Pool(t), "records-read")
	appendDrafts(t, store, "item-read", opened("item-read", "rd1"))
	ended, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := store.Load(ended, "item-read")

	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, item.ErrInvalid)
}

// A run's board context comes from its creation record: its settings, and
// human entries for the primary run that people see.
func TestARunsBoardContextComesFromItsCreation(t *testing.T) {
	pool := pgtest.Pool(t)
	store := postgres.NewRecords(pool, "records-runs")
	appendDrafts(t, store, "item-r", opened("item-r", "r1"), started("item-r", "r2"), runCreated("item-r", "r3", "run-r"))
	runs := postgres.NewRuns(pool, "records-runs")

	bc, err := runs.BoardContext(t.Context(), "run-r")
	require.NoError(t, err)
	_, unknown := runs.BoardContext(t.Context(), "run-404")

	assert.Equal(t, board.Context{Run: "run-r", HumanEntries: true, Settings: board.Settings{
		Convergence: board.DefaultConvergence, Limits: board.Limits{EntriesPerRound: 3},
	}}, bc)
	require.Error(t, unknown)
}

// Work records are audit records: append-only, and chained under their
// incident, so that an anchor names the incidents whose records it covers.
func TestWorkRecordsAreAppendOnlyAndChainedUnderTheirIncident(t *testing.T) {
	pool := pgtest.Pool(t)
	store := postgres.NewRecords(pool, "records-chain")
	appendDrafts(t, store, "item-x", opened("item-x", "x1"))

	for _, stmt := range []string{
		`UPDATE work_records SET kind = 'x' WHERE tenant_id = 'records-chain'`,
		`DELETE FROM work_records WHERE tenant_id = 'records-chain'`,
	} {
		_, err := pool.Exec(t.Context(), stmt)
		require.Error(t, err, stmt)
	}
	assert.Equal(t, []string{`work_records ["item-x", 1] incident:inc-item-x`}, chained(t, pool, "records-chain"))
}

// chained lists a tenant's chain as source, key and scope.
func chained(t *testing.T, pool *pgxpool.Pool, tenant string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT source || ' ' || key || ' ' || run_id FROM audit_chain WHERE tenant_id = $1 ORDER BY seq`, tenant)
	require.NoError(t, err)
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

// insertAt writes a record by hand, as recorded age before the database's
// time: the table is append-only, but an insert may choose its recorded_at.
func insertAt(t *testing.T, pool *pgxpool.Pool, tenant string, id item.ID, seq int64, kind item.Kind, body, age string) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `INSERT INTO work_records (tenant_id, item_id, seq, id, incident_id, run_id, kind, body, recorded_at)
		VALUES ($1, $2, $3, $4, $5, '', $6, $7::jsonb, clock_timestamp() - $8::interval)`,
		tenant, string(id), seq, fmt.Sprintf("raw-%s-%d", id, seq), "inc-"+string(id), string(kind), body, age)
	require.NoError(t, err)
}

// ClosedSince lists, by id, the items whose latest close committed within the
// window of the database's time (the method takes no time of the host's; the
// SQL reads clock_timestamp()) and no reopen followed. Records are placed at
// ages before the database's now, so that no test waits: item-old closed 2
// hours ago; item-both closed 2 hours ago, was reopened 90 minutes ago and
// closed again just now, so that its latest close counts; item-new closed just
// now; item-reopened closed just now and was reopened; item-open never closed.
func TestClosedSinceUsesTheDatabasesTime(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "records-closed-since"
	store := postgres.NewRecords(pool, tenant)
	const opening, closing, reopening = `{"v":1,"opened":{"type":"incident"}}`, `{"v":1,"closed":{"by":"admin"}}`, `{"v":1,"reopened":{"by":"admin"}}`
	for _, id := range []item.ID{"item-old", "item-both"} {
		insertAt(t, pool, tenant, id, 1, item.KindOpened, opening, "4 hours")
		insertAt(t, pool, tenant, id, 2, item.KindClosed, closing, "2 hours")
	}
	insertAt(t, pool, tenant, "item-both", 3, item.KindReopened, reopening, "90 minutes")
	appendDrafts(t, store, "item-both", closed("item-both", "both-c2", byAdmin))
	for _, id := range []item.ID{"item-new", "item-reopened", "item-open"} {
		appendDrafts(t, store, id, opened(id, string(id)+"-o"))
	}
	appendDrafts(t, store, "item-new", closed("item-new", "new-c", byAdmin))
	appendDrafts(t, store, "item-reopened", closed("item-reopened", "re-c", byAdmin), reopened("item-reopened", "re-r"))

	inside, err := store.ClosedSince(t.Context(), time.Hour)
	require.NoError(t, err)
	wide, err := store.ClosedSince(t.Context(), 3*time.Hour)
	require.NoError(t, err)

	assert.Equal(t, []item.ID{"item-both", "item-new"}, inside)
	assert.Equal(t, []item.ID{"item-both", "item-new", "item-old"}, wide)
}

var _ app.Incidents = (*postgres.Records)(nil)

// inIncident is a record of an item in incident, as body says.
//
//nolint:gocritic // hugeParam: a test builds each body as a value, and inIncident sets its copy's version.
func inIncident(id item.ID, rec string, incident item.IncidentID, body item.Body) item.Draft {
	body.V = item.Version
	return item.Draft{ID: rec, Item: id, Incident: incident, Body: body}
}

// linkedFrom is a link's body, from the incident left.
func linkedFrom(left item.IncidentID) item.Body {
	return item.Body{Linked: &item.Linked{Left: left, With: "item-a", By: "admin", Reason: &board.Reason{Code: "duplicate"}}}
}

// The items of an incident are those whose latest record is in it, each once,
// sorted (Decision 18), read on migration 0011's index: item-a opened in
// inc-1; item-c opened in inc-2 and linked into inc-1; item-e moved out of
// inc-1 and back; item-b unlinked out of inc-1 into inc-3; item-d recorded in
// inc-2 alone. An incident with no item has none; another tenant's items are
// not read.
func TestTheItemsOfAnIncident(t *testing.T) {
	pool := pgtest.Pool(t)
	tenant := tenantOf(t)
	store := postgres.NewRecords(pool, tenant)
	opening := item.Body{Opened: &item.Opened{Type: "incident", Title: "t", Source: "person", Arm: item.ArmVisible, How: item.NoWindow}}
	unlinked := item.Body{Unlinked: &item.Unlinked{Left: "inc-1", By: "admin", Reason: &board.Reason{Code: "not_related"}}}
	appendDrafts(t, store, "item-c", inIncident("item-c", "c1", "inc-2", opening), inIncident("item-c", "c2", "inc-1", linkedFrom("inc-2")))
	appendDrafts(t, store, "item-a", inIncident("item-a", "a1", "inc-1", opening))
	appendDrafts(t, store, "item-e", inIncident("item-e", "e1", "inc-1", opening), inIncident("item-e", "e2", "inc-2", linkedFrom("inc-1")))
	appendDrafts(t, store, "item-e", inIncident("item-e", "e3", "inc-1", linkedFrom("inc-2")))
	appendDrafts(t, store, "item-b", inIncident("item-b", "b1", "inc-1", opening), inIncident("item-b", "b2", "inc-3", unlinked))
	appendDrafts(t, store, "item-d", inIncident("item-d", "d1", "inc-2", opening), awaitingClose("item-d", "d2"))
	appendDrafts(t, postgres.NewRecords(pool, tenant+"-other"), "item-z", inIncident("item-z", "z1", "inc-1", opening))

	for incident, want := range map[item.IncidentID][]item.ID{
		"inc-1": {"item-a", "item-c", "item-e"}, "inc-2": nil, "inc-item-d": {"item-d"}, "inc-3": {"item-b"}, "inc-none": nil,
	} {
		got, err := store.IncidentItems(t.Context(), incident)

		require.NoError(t, err, incident)
		if want == nil {
			assert.Empty(t, got, incident)
			continue
		}
		assert.Equal(t, want, got, incident)
	}
}

// A link record is chained under its destination (Decision 18): its scope in
// the audit chain is incident:<the incident it joins>, and the chain verifies.
func TestALinkRecordIsChainedUnderItsDestination(t *testing.T) {
	pool := pgtest.Pool(t)
	tenant := tenantOf(t)
	store := postgres.NewRecords(pool, tenant)
	appendDrafts(t, store, "item-b", opened("item-b", "b1"))

	appendDrafts(t, store, "item-b", inIncident("item-b", "b2", "inc-item-a", linkedFrom("inc-item-b")))

	assert.Equal(t, []string{
		`work_records ["item-b", 1] incident:inc-item-b`,
		`work_records ["item-b", 2] incident:inc-item-a`,
	}, chained(t, pool, tenant))
	loaded, err := store.Load(t.Context(), "item-b")
	require.NoError(t, err)
	s, err := item.Fold(loaded)
	require.NoError(t, err)
	assert.Equal(t, item.IncidentID("inc-item-a"), s.Incident)
	verifies(t, pool, tenant)
}

// verifies checks that a tenant's chain verifies as verification recomputes
// it: each link follows the one before with no gap and hashes as it is
// recomputed, every row renders to its record's digest, and no row is
// unchained.
func verifies(t *testing.T, pool *pgxpool.Pool, tenant string) {
	t.Helper()
	require.NoError(t, auditpg.NewChain(pool, tenant, "public").Read(t.Context(), func(v auditapp.View) error {
		var prev auditchain.Sum
		seq := int64(0)
		require.NoError(t, v.Links(t.Context(), func(l *auditchain.Link) error {
			seq++
			require.Equal(t, seq, l.Seq, "the chain has no gap")
			require.Equal(t, prev, l.Prev)
			require.Equal(t, auditchain.LinkHash(prev, l), l.Hash, "the hash is the one verification recomputes")
			prev = l.Hash
			return nil
		}))
		require.Positive(t, seq)
		require.NoError(t, v.Rows(t.Context(), func(r *auditapp.Row) error {
			require.True(t, r.Known, r.Source)
			assert.Equal(t, r.Digest, auditchain.Digest(string(r.Rendered)), "%s %s", r.Source, r.Key)
			return nil
		}))
		unchained, err := v.Unchained(t.Context())
		require.NoError(t, err)
		assert.Empty(t, unchained)
		return nil
	}))
}
