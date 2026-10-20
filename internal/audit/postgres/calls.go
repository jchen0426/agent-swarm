// Package postgres keeps the audit store's records in Postgres: tool calls
// and the tools the MCP gateway hid, model calls, and the audit chain over
// them. It implements the MCP gateway's Audit and Hides ports, the Work
// context's ReadCalls, which the board's provenance check reads, and its
// ToolCalls, which the coordinator counts a round's failed calls by, the LLM
// gateway's Calls port, and the Audit application's Chain port.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	workapp "github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// ErrConflict is returned when a call's outcome was recorded with another
// content.
var ErrConflict = errors.New("postgres: the call has another outcome")

// Guard checks, inside a write transaction, that the writer may write: the
// platform's lease, which refuses a write whose holding is over (Plan 7d-1,
// Decision 4).
type Guard interface {
	Check(ctx context.Context, tx pgx.Tx) error
}

// write runs fn in a transaction, at the pool's default isolation, which the
// audit chain refuses to be other than read committed. When guard is set, it
// checks it first, for the holding whose epoch ctx carries: a refusal commits
// nothing and is returned as is.
func write(ctx context.Context, pool *pgxpool.Pool, guard Guard, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if guard != nil {
			if err := guard.Check(ctx, tx); err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

// Calls records tool calls in tool_calls and tool_call_outcomes, each in the
// database's time: the time a call record or an outcome gives is not
// written.
type Calls struct {
	pool   *pgxpool.Pool
	tenant string
	guard  Guard
}

// NewCalls returns the record of one tenant's tool calls.
func NewCalls(pool *pgxpool.Pool, tenant string) *Calls {
	return &Calls{pool: pool, tenant: tenant}
}

// Guarded returns the record, writing only while guard allows it: each
// write checks it first, in the write's transaction, for the holding whose
// epoch the write's context carries.
func (s *Calls) Guarded(guard Guard) *Calls {
	g := *s
	g.guard = guard
	return &g
}

// Begin records a call. The insert commits before Begin returns.
func (s *Calls) Begin(ctx context.Context, rec *app.CallRecord) error {
	err := write(ctx, s.pool, s.guard, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
INSERT INTO tool_calls (tenant_id, call_id, run_id, agent, server, tool, schema_hash, arguments, round, started_at, cause)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, clock_timestamp(), $10)`,
			s.tenant, rec.ID, string(rec.Run), text(rec.Agent), text(rec.Server), text(rec.Tool), rec.SchemaHash,
			text(string(rec.Args)), rec.Round, rec.Cause)
		return err
	})
	if err != nil {
		return fmt.Errorf("record call %s: %w", rec.ID, err)
	}
	return nil
}

// End records how a call ended, and the side its failure was on. Recording
// the same outcome again changes nothing, so a write that timed out after it
// committed can be repeated; a different outcome is refused.
func (s *Calls) End(ctx context.Context, o *app.Outcome) error {
	output := text(o.Output)
	var inserted bool
	err := write(ctx, s.pool, s.guard, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
INSERT INTO tool_call_outcomes (tenant_id, call_id, status, output, failure, ended_at)
VALUES ($1, $2, $3, $4, $5, clock_timestamp())
ON CONFLICT (tenant_id, call_id) DO NOTHING`, s.tenant, o.ID, string(o.Status), output, string(o.Failure))
		inserted = tag.RowsAffected() == 1
		return err
	})
	if err != nil {
		return fmt.Errorf("record outcome of call %s: %w", o.ID, err)
	}
	if inserted {
		return nil
	}
	var status, recorded, failure string
	if err := s.pool.QueryRow(ctx, `SELECT status, output, failure FROM tool_call_outcomes WHERE tenant_id = $1 AND call_id = $2`,
		s.tenant, o.ID).Scan(&status, &recorded, &failure); err != nil {
		return fmt.Errorf("read outcome of call %s: %w", o.ID, err)
	}
	if status != string(o.Status) || recorded != output || failure != string(o.Failure) {
		return fmt.Errorf("%w: %s", ErrConflict, o.ID)
	}
	return nil
}

// ReadCall returns a call as the board's provenance check reads it, with its
// stored arguments. A call that has not ended, was refused or ended in a
// read error has failed: none of them can be cited. A call without a server,
// a board tool's or a name nothing answers to, is no read call.
func (s *Calls) ReadCall(ctx context.Context, id string) (board.ReadCall, error) {
	var c board.ReadCall
	var run, args string
	var status, output *string
	err := s.pool.QueryRow(ctx, `
SELECT c.run_id, c.agent, c.server, c.tool, c.arguments, o.status, o.output
FROM tool_calls c LEFT JOIN tool_call_outcomes o USING (tenant_id, call_id)
WHERE c.tenant_id = $1 AND c.call_id = $2 AND c.server <> ''`, s.tenant, id).Scan(&run, &c.Agent, &c.Server, &c.Tool, &args, &status, &output)
	if errors.Is(err, pgx.ErrNoRows) {
		return board.ReadCall{}, workapp.ErrCallNotFound
	}
	if err != nil {
		return board.ReadCall{}, fmt.Errorf("read call %s: %w", id, err)
	}
	c.ID, c.Run, c.Failed, c.Args = id, board.RunID(run), true, []byte(args)
	if status != nil && *status == string(app.StatusOK) {
		c.Failed, c.Output = false, []byte(*output)
	}
	return c, nil
}

// Failed counts the failed tool calls a round cannot trust (Plan 7d-1,
// Decision 20): each of the round's calls by an agent that failed on the
// server's side, the platform's or the tool's, unless a later call of the
// same tool (the same server, or none, and the same name) by the same agent
// in the round succeeded, ok or recorded; and each of the round's calls
// still without an outcome, as the platform's failure, which nothing
// recovers. The platform's own reads carry no round and no agent, so none
// counts: the write each serves records the side that refused it.
func (s *Calls) Failed(ctx context.Context, run string, round int) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM tool_calls c LEFT JOIN tool_call_outcomes o USING (tenant_id, call_id)
WHERE c.tenant_id = $1 AND c.run_id = $2 AND c.round = $3 AND c.agent <> ''
  AND (o.call_id IS NULL
       OR o.failure = ANY($4) AND NOT EXISTS (
           SELECT 1 FROM tool_calls l JOIN tool_call_outcomes lo USING (tenant_id, call_id)
           WHERE l.tenant_id = c.tenant_id AND l.run_id = c.run_id AND l.round = c.round
             AND l.agent = c.agent AND l.server = c.server AND l.tool = c.tool
             AND l.started_at > c.started_at AND lo.status = ANY($5)))`,
		s.tenant, run, round,
		[]string{string(app.FailureServer), string(app.FailurePlatform), string(app.FailureTool)},
		[]string{string(app.StatusOK), string(app.StatusRecorded)}).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count the failed tool calls of round %d of run %s: %w", round, run, err)
	}
	return n, nil
}

// Hides records the tools the gateway hid, in tool_hides, in the database's
// time. A hide is not guarded: it holds for every holding.
type Hides struct {
	pool   *pgxpool.Pool
	tenant string
}

// NewHides returns the record of one tenant's hidden tools.
func NewHides(pool *pgxpool.Pool, tenant string) *Hides {
	return &Hides{pool: pool, tenant: tenant}
}

// Hide records a hide. Recording one again changes nothing.
func (s *Hides) Hide(ctx context.Context, h *app.Hide) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO tool_hides (tenant_id, endpoint, tool, pinned_hash, registration, server, reason, observed_hash, hidden_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, clock_timestamp())
ON CONFLICT DO NOTHING`, s.tenant, h.Endpoint, h.Tool, h.PinnedHash, h.Registration, h.Server, string(h.Reason), h.Observed)
	if err != nil {
		return fmt.Errorf("record hide of %s at %s: %w", h.Tool, h.Endpoint, err)
	}
	return nil
}

// Hidden returns every recorded hide, oldest first.
func (s *Hides) Hidden(ctx context.Context) ([]app.Hide, error) {
	rows, err := s.pool.Query(ctx, `
SELECT endpoint, tool, pinned_hash, registration, server, reason, observed_hash, hidden_at
FROM tool_hides WHERE tenant_id = $1 ORDER BY hidden_at, endpoint, tool`, s.tenant)
	if err != nil {
		return nil, fmt.Errorf("read hides: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (app.Hide, error) {
		var h app.Hide
		var reason string
		err := row.Scan(&h.Endpoint, &h.Tool, &h.PinnedHash, &h.Registration, &h.Server, &reason, &h.Observed, &h.At)
		h.Reason = app.Reason(reason)
		return h, err
	})
}

// text makes s storable: Postgres text holds neither NUL characters nor
// invalid UTF-8, so each becomes U+FFFD.
func text(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	return strings.ReplaceAll(s, "\x00", "\uFFFD")
}
