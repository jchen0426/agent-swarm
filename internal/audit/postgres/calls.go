// Package postgres keeps the audit store's records in Postgres: tool calls
// and the tools the MCP gateway hid, model calls, and the audit chain over
// them. It implements the MCP gateway's Audit and Hides ports, the Work
// context's ReadCalls, which the board's provenance check reads, the LLM
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

// Calls records tool calls in tool_calls and tool_call_outcomes.
type Calls struct {
	pool   *pgxpool.Pool
	tenant string
}

// NewCalls returns the record of one tenant's tool calls.
func NewCalls(pool *pgxpool.Pool, tenant string) *Calls {
	return &Calls{pool: pool, tenant: tenant}
}

// Begin records a call. The insert commits before Begin returns.
func (s *Calls) Begin(ctx context.Context, rec *app.CallRecord) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO tool_calls (tenant_id, call_id, run_id, agent, server, tool, schema_hash, arguments, round, started_at, cause)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		s.tenant, rec.ID, string(rec.Run), text(rec.Agent), text(rec.Server), text(rec.Tool), rec.SchemaHash,
		text(string(rec.Args)), rec.Round, rec.At, rec.Cause)
	if err != nil {
		return fmt.Errorf("record call %s: %w", rec.ID, err)
	}
	return nil
}

// End records how a call ended. Recording the same outcome again changes
// nothing, so a write that timed out after it committed can be repeated; a
// different outcome is refused.
func (s *Calls) End(ctx context.Context, o *app.Outcome) error {
	output := text(o.Output)
	tag, err := s.pool.Exec(ctx, `
INSERT INTO tool_call_outcomes (tenant_id, call_id, status, output, ended_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id, call_id) DO NOTHING`, s.tenant, o.ID, string(o.Status), output, o.At)
	if err != nil {
		return fmt.Errorf("record outcome of call %s: %w", o.ID, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var status, recorded string
	if err := s.pool.QueryRow(ctx, `SELECT status, output FROM tool_call_outcomes WHERE tenant_id = $1 AND call_id = $2`,
		s.tenant, o.ID).Scan(&status, &recorded); err != nil {
		return fmt.Errorf("read outcome of call %s: %w", o.ID, err)
	}
	if status != string(o.Status) || recorded != output {
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

// Hides records the tools the gateway hid, in tool_hides.
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
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT DO NOTHING`, s.tenant, h.Endpoint, h.Tool, h.PinnedHash, h.Registration, h.Server, string(h.Reason), h.Observed, h.At)
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
