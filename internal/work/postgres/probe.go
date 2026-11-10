package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// Probe reads the versions of items' boards, which the web's board watch
// probes once a tick (Plan 9a-2, design D2). It implements app.Probe.
type Probe struct {
	pool   *pgxpool.Pool
	tenant string
}

// NewProbe returns the probe of one tenant's items.
func NewProbe(pool *pgxpool.Pool, tenant string) *Probe {
	return &Probe{pool: pool, tenant: tenant}
}

// selectVersions reads, for each item asked, its latest record on the
// table's key (tenant_id, item_id, seq), its latest run_created's run on the
// index work_records_by_kind, and that run's latest entry on board_events'
// key (tenant_id, run_id, seq), in one statement, which sees one snapshot
// (R138). An item with no record gives no row.
const selectVersions = `
SELECT i.id, r.seq, r.id, coalesce(c.run_id, ''), coalesce(e.seq, 0), coalesce(e.id, '')
FROM unnest($2::text[]) AS i(id)
CROSS JOIN LATERAL (SELECT seq, id FROM work_records
                    WHERE tenant_id = $1 AND item_id = i.id ORDER BY seq DESC LIMIT 1) r
LEFT JOIN LATERAL (SELECT run_id FROM work_records
                   WHERE tenant_id = $1 AND item_id = i.id AND kind = 'run_created' ORDER BY seq DESC LIMIT 1) c ON true
LEFT JOIN LATERAL (SELECT seq, id FROM board_events
                   WHERE tenant_id = $1 AND run_id = c.run_id ORDER BY seq DESC LIMIT 1) e ON true`

// Versions gives each item's latest record, the run its latest run_created
// names, "" before one, and that run's latest entry, zero before one, in one
// statement; an item with no record is absent. It asks nothing for no item.
func (p *Probe) Versions(ctx context.Context, items []item.ID) (map[item.ID]app.BoardVersion, error) {
	out := make(map[item.ID]app.BoardVersion, len(items))
	if len(items) == 0 {
		return out, nil
	}
	ids := make([]string, len(items))
	for i, id := range items {
		ids[i] = string(id)
	}
	rows, err := p.pool.Query(ctx, selectVersions, p.tenant, ids)
	if err != nil {
		return nil, fmt.Errorf("probe the boards of %d items: %w", len(items), err)
	}
	var (
		id, recordID, run, entryID string
		recordSeq, entrySeq        int64
	)
	_, err = pgx.ForEachRow(rows, []any{&id, &recordSeq, &recordID, &run, &entrySeq, &entryID}, func() error {
		out[item.ID(id)] = app.BoardVersion{
			Record: app.Mark{ID: recordID, Seq: recordSeq}, Run: board.RunID(run), Board: app.Mark{ID: entryID, Seq: entrySeq},
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the probe of the boards of %d items: %w", len(items), err)
	}
	return out, nil
}
