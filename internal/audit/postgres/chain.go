package postgres

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
)

// form is how verification renders a row of one source in one form: its key
// and its columns, as migration 0005 writes them. Verification sends these
// expressions itself, so that it never relies on a function stored in the
// database it checks, and takes the text as UTF-8 bytes, so that the
// connection's encoding never changes what it hashes.
type form struct {
	key, row string
}

// source is an audited table with the forms this build renders.
type source struct {
	table string
	forms map[int16]form
}

// sources are the audited tables, in a fixed order.
var sources = []source{
	{"board_events", map[int16]form{1: {
		key: `jsonb_build_array(t.run_id, t.seq)::text`,
		row: `jsonb_build_array(t.tenant_id, t.run_id, t.seq, t.id, t.round, t.kind, t.author_kind, t.author_name,
			(extract(epoch FROM t.committed_at) * 1000000)::bigint, t.body)::text`,
	}}},
	{"tool_calls", map[int16]form{1: {
		key: `jsonb_build_array(t.call_id)::text`,
		row: `jsonb_build_array(t.tenant_id, t.call_id, t.run_id, t.agent, t.server, t.tool, t.schema_hash, t.arguments, t.round,
			(extract(epoch FROM t.started_at) * 1000000)::bigint, t.cause)::text`,
	}}},
	{"tool_call_outcomes", map[int16]form{1: {
		key: `jsonb_build_array(t.call_id)::text`,
		row: `jsonb_build_array(t.tenant_id, t.call_id, t.status, t.output, (extract(epoch FROM t.ended_at) * 1000000)::bigint)::text`,
	}}},
	{"tool_hides", map[int16]form{1: {
		key: `jsonb_build_array(t.endpoint, t.tool, t.pinned_hash, t.registration)::text`,
		row: `jsonb_build_array(t.tenant_id, t.endpoint, t.tool, t.pinned_hash, t.registration, t.server, t.reason, t.observed_hash,
			(extract(epoch FROM t.hidden_at) * 1000000)::bigint)::text`,
	}}},
	{"config_revisions", map[int16]form{1: {
		key: `jsonb_build_array(t.commit_id)::text`,
		row: `jsonb_build_array(t.tenant_id, t.commit_id, t.digest, (extract(epoch FROM t.loaded_at) * 1000000)::bigint)::text`,
	}}},
	{"config_objects", map[int16]form{1: {
		key: `jsonb_build_array(t.commit_id, t.kind, t.name)::text`,
		row: `jsonb_build_array(t.tenant_id, t.commit_id, t.kind, t.name, t.owner, t.file, t.hash, t.version)::text`,
	}}},
}

// Chain reads one tenant's audit chain and the rows it covers, in the
// schema that holds the platform's tables. It implements the Audit
// application's Chain port.
type Chain struct {
	pool   *pgxpool.Pool
	tenant string
	schema string
}

// NewChain returns the reader of one tenant's chain in schema.
func NewChain(pool *pgxpool.Pool, tenant, schema string) *Chain {
	return &Chain{pool: pool, tenant: tenant, schema: schema}
}

// Read calls fn with a view of one snapshot: a read-only transaction at
// repeatable read, whose search path holds only the system catalog, so
// that every name it reads is the one it means, and whose text arrives as
// UTF-8 whatever the connection asked for. Its joins hash rather than loop,
// so a whole chain is read in time linear in its size, also before the
// tables have statistics.
func (c *Chain) Read(ctx context.Context, fn func(app.View) error) error {
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin a snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, set := range []string{"SET LOCAL search_path = pg_catalog", "SET LOCAL client_encoding = 'UTF8'", "SET LOCAL enable_nestloop = off"} {
		if _, err := tx.Exec(ctx, set); err != nil {
			return fmt.Errorf("set up the snapshot: %w", err)
		}
	}
	return fn(&view{tx: tx, tenant: c.tenant, schema: c.schema})
}

type view struct {
	tx     pgx.Tx
	tenant string
	schema string
}

// table returns a table's name, qualified with the schema and quoted.
func (v *view) table(name string) string { return pgx.Identifier{v.schema, name}.Sanitize() }

func (v *view) selectLinks() string {
	return "SELECT seq, source, key, run_id, form, digest, prev, hash, chained_at FROM " + v.table("audit_chain") + " WHERE tenant_id = $1"
}

func (v *view) Head(ctx context.Context) (*chain.Link, error) {
	rows, err := v.tx.Query(ctx, v.selectLinks()+" ORDER BY seq DESC LIMIT 1", v.tenant)
	if err != nil {
		return nil, fmt.Errorf("read the chain's head: %w", err)
	}
	links, err := pgx.CollectRows(rows, scanLink)
	if err != nil {
		return nil, fmt.Errorf("read the chain's head: %w", err)
	}
	if len(links) == 0 {
		return nil, nil
	}
	return &links[0], nil
}

func (v *view) HashAt(ctx context.Context, seq int64) (chain.Sum, bool, error) {
	var hash []byte
	err := v.tx.QueryRow(ctx, "SELECT hash FROM "+v.table("audit_chain")+" WHERE tenant_id = $1 AND seq = $2", v.tenant, seq).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return chain.Sum{}, false, nil
	}
	if err != nil {
		return chain.Sum{}, false, fmt.Errorf("read the record at seq %d: %w", seq, err)
	}
	var s chain.Sum
	if len(hash) != len(s) {
		return chain.Sum{}, false, fmt.Errorf("the record at seq %d holds a hash of the wrong length", seq)
	}
	copy(s[:], hash)
	return s, true, nil
}

func (v *view) Runs(ctx context.Context, after, upTo int64) ([]string, error) {
	rows, err := v.tx.Query(ctx, "SELECT DISTINCT run_id FROM "+v.table("audit_chain")+
		" WHERE tenant_id = $1 AND seq > $2 AND seq <= $3 AND run_id <> ''", v.tenant, after, upTo)
	if err != nil {
		return nil, fmt.Errorf("read the chain's runs: %w", err)
	}
	runs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read the chain's runs: %w", err)
	}
	slices.Sort(runs)
	return runs, nil
}

func (v *view) Counts(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for _, s := range sources {
		var n int64
		if err := v.tx.QueryRow(ctx, "SELECT count(*) FROM "+v.table(s.table)+" WHERE tenant_id = $1", v.tenant).Scan(&n); err != nil {
			return nil, fmt.Errorf("count the rows of %s: %w", s.table, err)
		}
		out[s.table] = n
	}
	return out, nil
}

// Records counts the chain's records of each audited table, and of no
// other source: a record of a source this build does not know is
// verification's to report, and must not make an anchor unreadable.
func (v *view) Records(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	names := make([]string, 0, len(sources))
	for _, s := range sources {
		out[s.table] = 0
		names = append(names, s.table)
	}
	rows, err := v.tx.Query(ctx, "SELECT source, count(*) FROM "+v.table("audit_chain")+
		" WHERE tenant_id = $1 AND source = ANY($2) GROUP BY source", v.tenant, names)
	if err != nil {
		return nil, fmt.Errorf("count the chain's records: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var source string
		var n int64
		if err := rows.Scan(&source, &n); err != nil {
			return nil, fmt.Errorf("count the chain's records: %w", err)
		}
		out[source] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count the chain's records: %w", err)
	}
	return out, nil
}

func (v *view) Links(ctx context.Context, fn func(*chain.Link) error) error {
	rows, err := v.tx.Query(ctx, v.selectLinks()+" ORDER BY seq", v.tenant)
	if err != nil {
		return fmt.Errorf("read the chain: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return fmt.Errorf("read the chain: %w", err)
		}
		if err := fn(&l); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the chain: %w", err)
	}
	return nil
}

func scanLink(row pgx.CollectableRow) (chain.Link, error) {
	var l chain.Link
	var digest, prev, hash []byte
	var at time.Time
	if err := row.Scan(&l.Seq, &l.Source, &l.Key, &l.Run, &l.Form, &digest, &prev, &hash, &at); err != nil {
		return chain.Link{}, err
	}
	if len(digest) != len(l.Digest) || len(prev) != len(l.Prev) || len(hash) != len(l.Hash) {
		return chain.Link{}, fmt.Errorf("record %d holds a hash of the wrong length", l.Seq)
	}
	copy(l.Digest[:], digest)
	copy(l.Prev[:], prev)
	copy(l.Hash[:], hash)
	l.ChainedAt = at.UTC()
	return l, nil
}

// Rows renders each record's row as its form says, source by source; a
// record of a form or source this build does not know comes last.
func (v *view) Rows(ctx context.Context, fn func(*app.Row) error) error {
	var known strings.Builder
	for _, s := range sources {
		for _, n := range slices.Sorted(maps.Keys(s.forms)) {
			f := s.forms[n]
			fmt.Fprintf(&known, " AND NOT (c.source = '%s' AND c.form = %d)", s.table, n)
			query := fmt.Sprintf(`
SELECT c.seq, c.source, c.key, c.form, c.digest, t.rendered
FROM %s c
LEFT JOIN (SELECT %s AS key, convert_to(%s, 'UTF8') AS rendered FROM %s t WHERE t.tenant_id = $1) t ON t.key = c.key
WHERE c.tenant_id = $1 AND c.source = '%s' AND c.form = %d
ORDER BY c.seq`, v.table("audit_chain"), f.key, f.row, v.table(s.table), s.table, n)
			if err := v.rows(ctx, query, true, fn); err != nil {
				return err
			}
		}
	}
	return v.rows(ctx, "SELECT c.seq, c.source, c.key, c.form, c.digest, NULL::bytea FROM "+v.table("audit_chain")+
		" c WHERE c.tenant_id = $1"+known.String()+" ORDER BY c.seq", false, fn)
}

func (v *view) rows(ctx context.Context, query string, known bool, fn func(*app.Row) error) error {
	rows, err := v.tx.Query(ctx, query, v.tenant)
	if err != nil {
		return fmt.Errorf("read the chained rows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		r := app.Row{Known: known}
		var digest []byte
		if err := rows.Scan(&r.Seq, &r.Source, &r.Key, &r.Form, &digest, &r.Rendered); err != nil {
			return fmt.Errorf("read the chained rows: %w", err)
		}
		copy(r.Digest[:], digest)
		if err := fn(&r); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the chained rows: %w", err)
	}
	return nil
}

func (v *view) Unchained(ctx context.Context) ([]app.Row, error) {
	var out []app.Row
	for _, s := range sources {
		key := s.forms[1].key
		query := fmt.Sprintf(`
SELECT %s FROM %s t
WHERE t.tenant_id = $1 AND NOT EXISTS (
    SELECT 1 FROM %s c WHERE c.tenant_id = $1 AND c.source = '%s' AND c.key = %s)`, key, v.table(s.table), v.table("audit_chain"), s.table, key)
		rows, err := v.tx.Query(ctx, query, v.tenant)
		if err != nil {
			return nil, fmt.Errorf("read the unchained rows of %s: %w", s.table, err)
		}
		keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, fmt.Errorf("read the unchained rows of %s: %w", s.table, err)
		}
		for _, k := range keys {
			out = append(out, app.Row{Source: s.table, Key: k})
		}
	}
	return out, nil
}

// Tables reads, from the system catalog, how each audited table and the
// chain itself are held: a plain table, without row security, with no
// trigger but its chain trigger and no rule acting on its inserts, and, for
// an audited table, with its chain trigger enabled. The chain trigger is
// known by its name, its kind, after each inserted row, and its function,
// never by its name alone. A relation of the same name in another schema,
// which the platform's search path could reach before its own, counts
// against its table as a filter.
// chainTrigger is true of the trigger g that migration 0005 made for the
// table c: named <table>_chain, firing after each inserted row (tgtype 5:
// row and insert), and calling audit_chain_<table>() in the schema $1.
const chainTrigger = `g.tgname = c.relname || '_chain' AND g.tgtype = 5
             AND g.tgfoid = to_regprocedure(format('%I.%I()', $1::text, 'audit_chain_' || c.relname))`

func (v *view) Tables(ctx context.Context) ([]app.Table, error) {
	names := []string{"audit_chain"}
	for _, s := range sources {
		names = append(names, s.table)
	}
	rows, err := v.tx.Query(ctx, `
SELECT c.relname, c.relkind = 'r',
       c.relrowsecurity OR c.relforcerowsecurity OR EXISTS (
           SELECT 1 FROM pg_class o JOIN pg_namespace m ON m.oid = o.relnamespace
           WHERE o.relname = c.relname AND m.nspname <> $1 AND m.nspname NOT IN ('pg_catalog', 'information_schema')),
       c.relname = 'audit_chain' OR EXISTS (
           SELECT 1 FROM pg_trigger g
           WHERE g.tgrelid = c.oid AND g.tgenabled IN ('O', 'A') AND `+chainTrigger+`),
       EXISTS (
           SELECT 1 FROM pg_trigger g
           WHERE g.tgrelid = c.oid AND NOT g.tgisinternal AND g.tgenabled <> 'D'
             AND (g.tgtype::int & 4) <> 0 AND NOT (`+chainTrigger+`))
       OR EXISTS (SELECT 1 FROM pg_rewrite w WHERE w.ev_class = c.oid)
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relname = ANY($2)`, v.schema, names)
	if err != nil {
		return nil, fmt.Errorf("read the audited tables: %w", err)
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (app.Table, error) {
		var t app.Table
		err := row.Scan(&t.Name, &t.Plain, &t.Filtered, &t.Chains, &t.Altered)
		return t, err
	})
	if err != nil {
		return nil, fmt.Errorf("read the audited tables: %w", err)
	}
	out := make([]app.Table, 0, len(names))
	for _, name := range names {
		i := slices.IndexFunc(found, func(t app.Table) bool { return t.Name == name })
		if i < 0 {
			out = append(out, app.Table{Name: name})
			continue
		}
		out = append(out, found[i])
	}
	return out, nil
}
