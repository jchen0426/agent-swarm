package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	llmapp "github.com/jchen0426/agent-swarm/internal/llm/app"
	workapp "github.com/jchen0426/agent-swarm/internal/work/app"
)

// ModelCalls records model calls in model_calls, their requests and
// completions in model_call_contents, and how they ended in
// model_call_outcomes, and counts what they spent. It implements the LLM
// gateway's Calls port.
type ModelCalls struct {
	pool   *pgxpool.Pool
	tenant string
}

// NewModelCalls returns the record of one tenant's model calls.
func NewModelCalls(pool *pgxpool.Pool, tenant string) *ModelCalls {
	return &ModelCalls{pool: pool, tenant: tenant}
}

// Begin records a call and its request, in one transaction that commits
// before Begin returns.
func (s *ModelCalls) Begin(ctx context.Context, rec *llmapp.CallRecord) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
INSERT INTO model_calls (tenant_id, call_id, run_id, agent, round, class, model, request_bytes, request_digest, estimate, started_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			s.tenant, rec.ID, text(rec.Run), text(rec.Agent), rec.Round, string(rec.Class), text(rec.Model),
			rec.RequestBytes, rec.RequestDigest, rec.Estimate, rec.At); err != nil {
			return err
		}
		return s.content(ctx, tx, rec.ID, "request", rec.Request)
	})
	if err != nil {
		return fmt.Errorf("record model call %s: %w", rec.ID, err)
	}
	return nil
}

// End records how a call ended, and its completion, in one transaction.
// Recording the same outcome again changes nothing, so a write that timed
// out after it committed can be repeated; a different outcome is refused.
func (s *ModelCalls) End(ctx context.Context, o *llmapp.Outcome) error {
	reason := text(o.Reason)
	var recorded bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
INSERT INTO model_call_outcomes (tenant_id, call_id, status, reason, input_tokens, cache_write_5m_tokens,
    cache_write_1h_tokens, cache_read_tokens, output_tokens, spend, weighted, attempts, details, ended_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (tenant_id, call_id) DO NOTHING`,
			s.tenant, o.ID, string(o.Status), reason, o.Usage.Input, o.Usage.CacheWrite5m, o.Usage.CacheWrite1h,
			o.Usage.CacheRead, o.Usage.Output, o.Spend, o.Weighted, o.Attempts, text(o.Details), o.At)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		recorded = true
		return s.content(ctx, tx, o.ID, "completion", o.Completion)
	})
	if err != nil {
		return fmt.Errorf("record outcome of model call %s: %w", o.ID, err)
	}
	if recorded {
		return nil
	}
	return s.same(ctx, o, reason)
}

// same reports whether a call's recorded outcome is o.
func (s *ModelCalls) same(ctx context.Context, o *llmapp.Outcome, reason string) error {
	var status, recReason string
	var weighted int64
	var completion *string
	err := s.pool.QueryRow(ctx, `
SELECT o.status, o.reason, o.weighted, c.content
FROM model_call_outcomes o
LEFT JOIN model_call_contents c ON c.tenant_id = o.tenant_id AND c.call_id = o.call_id AND c.part = 'completion'
WHERE o.tenant_id = $1 AND o.call_id = $2`, s.tenant, o.ID).Scan(&status, &recReason, &weighted, &completion)
	if err != nil {
		return fmt.Errorf("read outcome of model call %s: %w", o.ID, err)
	}
	stored := ""
	if completion != nil {
		stored = *completion
	}
	if status != string(o.Status) || recReason != reason || weighted != o.Weighted || stored != text(o.Completion) {
		return fmt.Errorf("%w: %s", ErrConflict, o.ID)
	}
	return nil
}

// content records a part of a call, unless it is empty.
func (s *ModelCalls) content(ctx context.Context, tx pgx.Tx, id, part, content string) error {
	if content == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO model_call_contents (tenant_id, call_id, part, content) VALUES ($1, $2, $3, $4)`,
		s.tenant, id, part, text(content))
	return err
}

// Spent returns the cost-weighted tokens of a run's calls: what each cost
// once its outcome is recorded, and its estimate until then.
func (s *ModelCalls) Spent(ctx context.Context, run string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
SELECT coalesce(sum(coalesce(o.weighted, c.estimate)), 0)::bigint
FROM model_calls c LEFT JOIN model_call_outcomes o USING (tenant_id, call_id)
WHERE c.tenant_id = $1 AND c.run_id = $2`, s.tenant, run).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count the spend of run %s: %w", run, err)
	}
	return n, nil
}

// SpentSince returns the cost-weighted tokens of the tenant's calls that
// began at or after since, counted as Spent counts them.
func (s *ModelCalls) SpentSince(ctx context.Context, since time.Time) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
SELECT coalesce(sum(coalesce(o.weighted, c.estimate)), 0)::bigint
FROM model_calls c LEFT JOIN model_call_outcomes o USING (tenant_id, call_id)
WHERE c.tenant_id = $1 AND c.started_at >= $2`, s.tenant, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count the spend since %s: %w", since.Format(time.RFC3339), err)
	}
	return n, nil
}

// InFlight counts a run's calls that have no outcome yet.
func (s *ModelCalls) InFlight(ctx context.Context, run string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM model_calls c LEFT JOIN model_call_outcomes o USING (tenant_id, call_id)
WHERE c.tenant_id = $1 AND c.run_id = $2 AND o.call_id IS NULL`, s.tenant, run).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count the calls of run %s in flight: %w", run, err)
	}
	return n, nil
}

// Failed returns a run's calls whose outcome is refused, error or unknown,
// in the order they began, as the coordinator notices them on the board.
func (s *ModelCalls) Failed(ctx context.Context, run string) ([]workapp.ModelCall, error) {
	rows, err := s.pool.Query(ctx, `
SELECT c.call_id, c.agent, o.status FROM model_calls c JOIN model_call_outcomes o USING (tenant_id, call_id)
WHERE c.tenant_id = $1 AND c.run_id = $2 AND o.status <> 'ok'
ORDER BY c.started_at, c.call_id`, s.tenant, run)
	if err != nil {
		return nil, fmt.Errorf("read the failed calls of run %s: %w", run, err)
	}
	calls, err := pgx.CollectRows(rows, pgx.RowToStructByPos[workapp.ModelCall])
	if err != nil {
		return nil, fmt.Errorf("read the failed calls of run %s: %w", run, err)
	}
	return calls, nil
}
