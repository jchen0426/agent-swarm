//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	neturl "net/url"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
	"github.com/jchen0426/agent-swarm/internal/audit/dirstore"
	"github.com/jchen0426/agent-swarm/internal/audit/postgres"
	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/testsupport/pgtest"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	workpostgres "github.com/jchen0426/agent-swarm/internal/work/postgres"
)

var investigator = board.Author{Kind: board.AuthorAgent, Name: "flag-investigator"}

// post appends one board entry to run, through the board's own store.
func post(t *testing.T, pool *pgxpool.Pool, tenant string, run board.RunID, id board.EntryID, body *board.Body) {
	t.Helper()
	_, err := workpostgres.NewEventStore(pool, tenant).Append(t.Context(), run, func([]board.Event) ([]board.Draft, error) {
		return []board.Draft{{ID: id, Run: run, Round: 1, Author: investigator, Body: *body}}, nil
	})
	require.NoError(t, err)
}

func task(title string) *board.Body { return &board.Body{Task: &board.Task{Title: title}} }

// call records a call of run and its outcome, through the audit store.
func call(t *testing.T, pool *pgxpool.Pool, tenant, id string, run board.RunID, args, output string) {
	t.Helper()
	calls := postgres.NewCalls(pool, tenant)
	rec := record(id)
	rec.Run, rec.Args = run, json.RawMessage(args)
	require.NoError(t, calls.Begin(t.Context(), rec))
	require.NoError(t, calls.End(t.Context(), &actionsapp.Outcome{ID: id, Status: actionsapp.StatusOK, Output: output, At: at}))
}

// hide records a hidden tool.
func hide(t *testing.T, pool *pgxpool.Pool, tenant, tool string) {
	t.Helper()
	require.NoError(t, postgres.NewHides(pool, tenant).Hide(t.Context(), &actionsapp.Hide{
		Endpoint: "https://flags.internal/mcp", Tool: tool, PinnedHash: "sha256:pin", Registration: "sha256:reg",
		Server: "flags", Reason: actionsapp.ReasonChanged, Observed: "sha256:other", At: at,
	}))
}

// read calls fn with one snapshot of the tenant's chain.
func read(t *testing.T, c *postgres.Chain, fn func(app.View)) {
	t.Helper()
	require.NoError(t, c.Read(t.Context(), func(v app.View) error { fn(v); return nil }))
}

// readChain returns the tenant's records, and checks that each links to the
// one before as verification recomputes it.
func readChain(t *testing.T, c *postgres.Chain) []chain.Link {
	t.Helper()
	var links []chain.Link
	read(t, c, func(v app.View) {
		require.NoError(t, v.Links(t.Context(), func(l *chain.Link) error { links = append(links, *l); return nil }))
	})
	var prev chain.Sum
	for i := range links {
		l := &links[i]
		require.Equal(t, int64(i+1), l.Seq, "the chain has no gap")
		require.Equal(t, prev, l.Prev)
		require.Equal(t, chain.LinkHash(prev, l), l.Hash, "the database's hash is the one verification recomputes")
		prev = l.Hash
	}
	return links
}

// checkRows checks that every record's row renders to its digest, and that
// no row is unchained; it returns how many rows there are.
func checkRows(t *testing.T, c *postgres.Chain) int {
	t.Helper()
	n := 0
	read(t, c, func(v app.View) {
		require.NoError(t, v.Rows(t.Context(), func(r *app.Row) error {
			n++
			require.True(t, r.Known, r.Source)
			require.NotNil(t, r.Rendered, r.Source)
			assert.Equal(t, r.Digest, chain.Digest(string(r.Rendered)), "%s %s", r.Source, r.Key)
			return nil
		}))
		unchained, err := v.Unchained(t.Context())
		require.NoError(t, err)
		assert.Empty(t, unchained)
	})
	return n
}

func newChain(pool *pgxpool.Pool, tenant string) *postgres.Chain {
	return postgres.NewChain(pool, tenant, "public")
}

// Every row of every audited table is chained as it is inserted, in order,
// and verification renders each row to the digest its record holds.
func TestEveryAuditedRowIsChained(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-every"
	post(t, pool, tenant, "run-a", "every-1", task("check the pool"))
	post(t, pool, tenant, "run-a", "every-2", task("check the cache"))
	call(t, pool, tenant, "every-c1", "run-b", `{"flag":"x"}`, `{"on":true}`)
	hide(t, pool, tenant, "get_flag")
	c := newChain(pool, tenant)

	links := readChain(t, c)

	var got []string
	for i := range links {
		got = append(got, links[i].Source+" "+links[i].Run)
	}
	assert.Equal(t, []string{"board_events run-a", "board_events run-a", "tool_calls run-b", "tool_call_outcomes run-b", "tool_hides "}, got)
	assert.Equal(t, `["run-a", 2]`, links[1].Key)
	assert.Equal(t, 5, checkRows(t, c))
	read(t, c, func(v app.View) {
		head, err := v.Head(t.Context())
		require.NoError(t, err)
		assert.Equal(t, links[4].Hash, head.Hash)
		runs, err := v.Runs(t.Context(), 0, 5)
		require.NoError(t, err)
		assert.Equal(t, []string{"run-a", "run-b"}, runs)
		runs, err = v.Runs(t.Context(), 3, 5)
		require.NoError(t, err)
		assert.Equal(t, []string{"run-b"}, runs, "an outcome is its call's run")
		counts, err := v.Counts(t.Context())
		require.NoError(t, err)
		assert.Equal(t, map[string]int64{"board_events": 2, "tool_calls": 1, "tool_call_outcomes": 1, "tool_hides": 1,
			"config_revisions": 0, "config_objects": 0}, counts)
	})
}

// An empty chain has no head.
func TestAnEmptyChainHasNoHead(t *testing.T) {
	c := newChain(pgtest.Pool(t), "chain-empty")

	read(t, c, func(v app.View) {
		head, err := v.Head(t.Context())
		require.NoError(t, err)
		assert.Nil(t, head)
	})
	assert.Empty(t, readChain(t, c))
}

// Writers that race, some of them rolled back, leave one contiguous chain:
// records commit in the order of their seq, and a rollback leaves no gap.
func TestRacingWritersLeaveOneContiguousChain(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-race"
	var wg sync.WaitGroup
	for i := range 15 {
		wg.Go(func() { call(t, pool, tenant, fmt.Sprintf("race-c%d", i), "run-c", `{"flag":"x"}`, "ok") })
		wg.Go(func() {
			post(t, pool, tenant, board.RunID(fmt.Sprintf("run-%d", i%3)), board.EntryID(fmt.Sprintf("race-%d", i)), task("t"))
		})
		wg.Go(func() {
			err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
				_, err := tx.Exec(context.Background(), `INSERT INTO tool_calls (tenant_id, call_id, run_id, agent, server, tool, schema_hash, arguments, round, started_at)
VALUES ($1, $2, 'run-c', 'a', 's', 't', 'h', '{}', 1, now())`, tenant, fmt.Sprintf("race-rolled-back-%d", i))
				if err != nil {
					return err
				}
				return errors.New("roll back")
			})
			assert.EqualError(t, err, "roll back")
		})
	}
	wg.Wait()
	c := newChain(pool, tenant)

	links := readChain(t, c)
	assert.Len(t, links, 45)
	assert.Equal(t, 45, checkRows(t, c))
	for i := 1; i < len(links); i++ {
		assert.False(t, links[i].ChainedAt.Before(links[i-1].ChainedAt), "a record is chained no earlier than the one before")
	}
}

// The chain is written only at read committed: at any other level, a
// transaction's snapshot could predate the record it waited for, so the
// insert fails rather than forking the chain.
func TestAuditedRowsAreWrittenOnlyAtReadCommitted(t *testing.T) {
	const tenant = "chain-levels"
	for _, level := range []string{"repeatable read", "serializable"} {
		pool := pgtest.PoolWithParams(t, map[string]string{"default_transaction_isolation": level})

		err := postgres.NewCalls(pool, tenant).Begin(t.Context(), record("levels-"+level))

		require.ErrorContains(t, err, "read committed", level)
	}
}

// A row's digest is the same whatever the inserting session's settings and
// the reading connection's encoding, and whatever the text holds: quotes,
// backslashes, control characters, characters outside ASCII, and JSON
// written inside a string.
func TestTheDigestIgnoresSessionSettingsAndEncodings(t *testing.T) {
	odd := pgtest.PoolWithParams(t, map[string]string{"TimeZone": "Pacific/Chatham", "DateStyle": "SQL, DMY", "IntervalStyle": "sql_standard"})
	const tenant = "chain-odd"
	text := "q\"uote \\back\nline\ttab \x01ctl é ü {\"a\":[1,2.50]}"
	call(t, odd, tenant, "odd-c1", "run-odd", fmt.Sprintf(`{"text":%q}`, text), text+" 漢字")
	post(t, odd, tenant, "run-odd", "odd-1", &board.Body{Task: &board.Task{Title: text, Tags: []string{"a\"b", "漢"}}})

	for _, params := range []map[string]string{nil, {"client_encoding": "LATIN1"}} {
		c := newChain(pgtest.PoolWithParams(t, params), tenant)

		readChain(t, c)
		assert.Equal(t, 3, checkRows(t, c), params)
	}
}

// tamper runs statements with every trigger off, as an operator with the
// database's superuser may.
func tamper(t *testing.T, pool *pgxpool.Pool, statements ...string) {
	t.Helper()
	require.NoError(t, pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), "SET LOCAL session_replication_role = replica"); err != nil {
			return err
		}
		for _, s := range statements {
			if _, err := tx.Exec(t.Context(), s); err != nil {
				return err
			}
		}
		return nil
	}))
}

// auditor returns the auditor of tenant, with its anchors in a directory,
// and a clock tests move.
func auditor(t *testing.T, pool *pgxpool.Pool, tenant string, now *time.Time) *app.Auditor {
	t.Helper()
	store, err := dirstore.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	a, err := app.New(newChain(pool, tenant), store, tenant, func() time.Time { return *now })
	require.NoError(t, err)
	return a
}

// findings returns what a verification found, as kind and table, but for
// the directory store being unlocked.
func findings(t *testing.T, a *app.Auditor) []string {
	t.Helper()
	all, err := a.Verify(t.Context(), time.Hour)
	require.NoError(t, err)
	var out []string
	for i := range all {
		if all[i].Kind != chain.StoreUnlocked {
			out = append(out, string(all[i].Kind)+" "+all[i].Source)
		}
	}
	return out
}

// What an operator does to the database behind the platform's back shows:
// a row changed, a row deleted, a row inserted without its record.
func TestVerificationFindsWhatWasDoneBehindThePlatformsBack(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-tamper"
	post(t, pool, tenant, "run-t", "tamper-1", task("check the pool"))
	call(t, pool, tenant, "tamper-c1", "run-t", `{"flag":"x"}`, `{"on":true}`)
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	require.Empty(t, findings(t, a))

	tamper(t, pool,
		`UPDATE board_events SET body = '{"task":{"title":"check nothing"}}' WHERE tenant_id = 'chain-tamper' AND id = 'tamper-1'`,
		`DELETE FROM tool_call_outcomes WHERE tenant_id = 'chain-tamper' AND call_id = 'tamper-c1'`,
		`INSERT INTO tool_hides (tenant_id, endpoint, tool, pinned_hash, registration, server, reason, observed_hash, hidden_at)
VALUES ('chain-tamper', 'https://flags.internal/mcp', 'set_flag', 'p', 'r', 'flags', 'changed', 'o', now())`)

	assert.ElementsMatch(t, []string{"changed_row board_events", "missing_row tool_call_outcomes", "unchained_row tool_hides"}, findings(t, a))
}

// Records are never deleted, by the retention job either; a chain whose
// first records an operator deleted with the triggers off shows as cut.
func TestTheChainsStartCannotBeDeleted(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-prefix"
	post(t, pool, tenant, "run-p", "prefix-1", task("first"))
	post(t, pool, tenant, "run-p", "prefix-2", task("second"))
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)

	err = pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), "SET LOCAL agentswarm.retention = 'on'"); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), "DELETE FROM audit_chain WHERE tenant_id = 'chain-prefix' AND seq = 1")
		return err
	})
	require.ErrorContains(t, err, "append-only")
	tamper(t, pool,
		"DELETE FROM audit_chain WHERE tenant_id = 'chain-prefix' AND seq = 1",
		"DELETE FROM board_events WHERE tenant_id = 'chain-prefix' AND id = 'prefix-1'")

	assert.Equal(t, []string{"truncated "}, findings(t, a))
}

// Rows written while the chain trigger was off, and deleted since, still
// show: the anchor taken meanwhile counted them, and saw the trigger off.
func TestRowsWrittenUnchainedShowEvenOnceDeleted(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-unchained"
	hide(t, pool, tenant, "get_flag")
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := pool.Exec(t.Context(), "ALTER TABLE tool_hides DISABLE TRIGGER tool_hides_chain")
	require.NoError(t, err)
	enable := func() {
		_, err := pool.Exec(context.Background(), "ALTER TABLE tool_hides ENABLE TRIGGER tool_hides_chain")
		require.NoError(t, err)
	}
	t.Cleanup(enable)
	hide(t, pool, tenant, "set_flag")
	_, err = a.Anchor(t.Context())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"table tool_hides", "table tool_hides", "unchained_row tool_hides", "unchained_rows tool_hides"}, findings(t, a),
		"the table now, and as the anchor saw it")
	enable()

	tamper(t, pool, "DELETE FROM tool_hides WHERE tenant_id = 'chain-unchained' AND tool = 'set_flag'")

	assert.ElementsMatch(t, []string{"unchained_rows tool_hides", "table tool_hides"}, findings(t, a))
}

// A table that row security filters, or that is not a plain table, shows:
// verification could be shown other rows than raters see.
func TestTablesVerificationCannotTrustShow(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-tables"
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), "ALTER TABLE tool_hides ENABLE ROW LEVEL SECURITY")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "ALTER TABLE tool_hides DISABLE ROW LEVEL SECURITY")
		require.NoError(t, err)
	})

	assert.Equal(t, []string{"table tool_hides"}, findings(t, a))
}

// database opens a pool to a database of the package's container, by name.
func database(t *testing.T, name string) *pgxpool.Pool {
	t.Helper()
	u, err := neturl.Parse(pgtest.URL(t))
	require.NoError(t, err)
	u.Path = "/" + name
	pool, err := db.Open(t.Context(), u.String())
	require.NoError(t, err)
	return pool
}

// After a restore from a backup, the anchor taken next names what the chain
// no longer holds: its range, and the runs whose records went.
func TestARestoreFromABackupShowsWhatWasLost(t *testing.T) {
	admin := pgtest.Pool(t)
	ctx := t.Context()
	_, err := admin.Exec(ctx, "CREATE DATABASE restore_live")
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, name := range []string{"restore_live", "restore_backup"} {
			_, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			assert.NoError(t, err)
		}
	})
	live := database(t, "restore_live")
	require.NoError(t, db.Migrate(ctx, live))
	store, err := dirstore.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	const tenant = "chain-restore"
	now := time.Now().Add(-3 * time.Hour)
	anchor := func(pool *pgxpool.Pool) {
		t.Helper()
		a, err := app.New(newChain(pool, tenant), store, tenant, func() time.Time { return now })
		require.NoError(t, err)
		_, err = a.Anchor(ctx)
		require.NoError(t, err)
		now = now.Add(time.Hour)
	}
	post(t, live, tenant, "run-early", "restore-1", task("before the backup"))
	anchor(live)
	live.Close()
	_, err = admin.Exec(ctx, "CREATE DATABASE restore_backup TEMPLATE restore_live")
	require.NoError(t, err)
	live = database(t, "restore_live")
	call(t, live, tenant, "restore-c1", "run-late", `{"flag":"x"}`, "ok")
	post(t, live, tenant, "run-later", "restore-2", task("after the backup"))
	anchor(live)
	live.Close()

	for _, sql := range []string{"DROP DATABASE restore_live", "CREATE DATABASE restore_live TEMPLATE restore_backup"} {
		_, err := admin.Exec(ctx, sql)
		require.NoError(t, err)
	}
	live = database(t, "restore_live")
	defer live.Close()
	for i := range 4 {
		post(t, live, tenant, "run-new", board.EntryID(fmt.Sprintf("restore-new-%d", i)), task("after the restore"))
	}
	anchor(live)
	a, err := app.New(newChain(live, tenant), store, tenant, func() time.Time { return now })
	require.NoError(t, err)

	got, err := a.Verify(ctx, time.Hour)

	require.NoError(t, err)
	var kinds []string
	for i := range got {
		if got[i].Kind == chain.StoreUnlocked {
			continue
		}
		kinds = append(kinds, string(got[i].Kind))
		if got[i].Kind == chain.Lost {
			assert.Equal(t, []int64{4, 4}, []int64{got[i].Seq, max(got[i].Through, got[i].Seq)})
			assert.Equal(t, []string{"run-late", "run-later"}, got[i].Runs)
		}
	}
	assert.Equal(t, []string{"lost"}, kinds)
}

// Another trigger or a rule that acts on an audited table's inserts, which
// could change or drop a row before it is chained, shows.
func TestATriggerOrARuleOnInsertsShows(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-altered"
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), `
CREATE FUNCTION rewrite_body() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.body := '{}'; RETURN NEW; END; $$;
CREATE TRIGGER rewrite_body BEFORE INSERT ON board_events FOR EACH ROW EXECUTE FUNCTION rewrite_body();
CREATE RULE drop_hides AS ON INSERT TO tool_hides DO INSTEAD NOTHING;`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `
DROP RULE drop_hides ON tool_hides;
DROP TRIGGER rewrite_body ON board_events;
DROP FUNCTION rewrite_body();`)
		require.NoError(t, err)
	})

	assert.ElementsMatch(t, []string{"table board_events", "table tool_hides"}, findings(t, a))
}

// A table of an audited table's name in another schema, which the
// platform's search path could reach first and so take its writes
// unchained, shows.
func TestATableOfTheSameNameInAnotherSchemaShows(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-shadow"
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := a.Anchor(t.Context())
	require.NoError(t, err)
	_, err = pool.Exec(t.Context(), "CREATE SCHEMA shadow; CREATE TABLE shadow.tool_hides (LIKE public.tool_hides)")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "DROP SCHEMA shadow CASCADE")
		require.NoError(t, err)
	})

	assert.Equal(t, []string{"table tool_hides"}, findings(t, a))
}

// A record of a source no audited table has, as an operator can append by
// hand, shows as a record this build cannot render; it never makes an
// anchor unreadable, so later anchors still count.
func TestARogueRecordLeavesAnchorsReadable(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-rogue"
	post(t, pool, tenant, "run-r", "rogue-1", task("first"))
	_, err := pool.Exec(t.Context(), "SELECT audit_chain_append('chain-rogue', 'X', '[\"x\"]', '', 1::smallint, 'rogue')")
	require.NoError(t, err)
	now := time.Now()
	a := auditor(t, pool, tenant, &now)

	an, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"board_events": 1, "tool_calls": 0, "tool_call_outcomes": 0, "tool_hides": 0, "config_revisions": 0, "config_objects": 0}, an.Records)
	assert.Equal(t, []string{"unknown_form X"}, findings(t, a))
}

// A trigger that takes the chain trigger's name, but fires before the row
// is inserted and rewrites it, shows: the chain trigger is known by its
// kind and its function, never by its name alone.
func TestAChainTriggerImpostorShows(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-impostor"
	now := time.Now()
	a := auditor(t, pool, tenant, &now)
	_, err := pool.Exec(t.Context(), `
DROP TRIGGER board_events_chain ON board_events;
CREATE FUNCTION impostor() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.body := '{"task":{"title":"forged"}}';
    PERFORM audit_chain_append(NEW.tenant_id, 'board_events', audit_key(NEW), NEW.run_id, 1::smallint, audit_form1(NEW));
    RETURN NEW;
END;
$$;
CREATE TRIGGER board_events_chain BEFORE INSERT ON board_events FOR EACH ROW EXECUTE FUNCTION impostor();`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `
DROP TRIGGER board_events_chain ON board_events;
DROP FUNCTION impostor();
CREATE TRIGGER board_events_chain AFTER INSERT ON board_events FOR EACH ROW EXECUTE FUNCTION audit_chain_board_events();`)
		require.NoError(t, err)
	})
	post(t, pool, tenant, "run-i", "impostor-1", task("check the pool"))
	_, err = a.Anchor(t.Context())
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"table board_events", "table board_events"}, findings(t, a), "now, and as the anchor saw it")
}

// One record appended by hand, with a run too long to name, never stops
// anchoring: the anchor counts the run rather than naming it, and
// verification shows that, beside the record's missing row.
func TestARunTooLongToNameLeavesAnchoringRunning(t *testing.T) {
	pool := pgtest.Pool(t)
	const tenant = "chain-longrun"
	post(t, pool, tenant, "run-l", "longrun-1", task("first"))
	_, err := pool.Exec(t.Context(), "SELECT audit_chain_append('chain-longrun', 'board_events', '[\"x\"]', repeat('r', 5000000), 1::smallint, 'x')")
	require.NoError(t, err)
	now := time.Now()
	a := auditor(t, pool, tenant, &now)

	an, err := a.Anchor(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []string{"run-l"}, an.Runs)
	assert.Equal(t, int64(1), an.Omitted)
	assert.ElementsMatch(t, []string{"missing_row board_events", "runs_omitted "}, findings(t, a))
}
