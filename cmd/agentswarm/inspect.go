package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/deploy"
	"github.com/jchen0426/agent-swarm/internal/secrets/files"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// What inspect prints.
const (
	inspectItem  = "item"
	inspectBoard = "board"
	inspectCalls = "calls"
)

func newInspectCommand() *cobra.Command {
	var deployment string
	cmd := &cobra.Command{
		Use:   "inspect --deployment FILE (item ID | board RUN | calls RUN)",
		Short: "Print an item's records, a run's board or a run's calls, as the database holds them",
		Long: `Print, as JSON lines, what the database of a deployment holds (Plan 7d-1,
Contract C5):

  item ID    each of the item's work records:
             {"seq", "kind", "v", "at", "body"}
  board RUN  each event of the run's board, in the same form
  calls RUN  each of the run's model calls:
             {"call": "model", "id", "agent", "round", "model", "started", "status", "reason"}
             then each of its tool calls:
             {"call": "tool", "id", "agent", "round", "server", "tool", "name", "started",
              "status", "failure", "reason"}

A body prints as it is stored, redacted already. A record or an event of a later
version than this build reads prints its body's text as "raw" in place of
"body". A call without an outcome has no status yet.`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 2 || (args[0] != inspectItem && args[0] != inspectBoard && args[0] != inspectCalls) {
				return errors.New("inspect takes item ID, board RUN or calls RUN")
			}
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			if err := inspect(c.Context(), deployment, args[0], args[1], c.OutOrStdout()); err != nil {
				return failure{err}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&deployment, "deployment", "", "the deployment file, whose database and tenant inspect reads")
	_ = cmd.MarkFlagRequired("deployment")
	return cmd
}

// inspect prints what the deployment's database holds of an item, a run's
// board or a run's calls (C5), one JSON line each.
func inspect(ctx context.Context, path, what, id string, out io.Writer) error {
	d, err := deploy.Load(path)
	if err != nil {
		return err
	}
	secrets, err := files.Open(d.Secrets)
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	defer func() { _ = secrets.Close() }()
	url, err := secrets.Secret(ctx, d.Database)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	pool, err := db.Open(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	emit := func(v any) error {
		if err := enc.Encode(v); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		return nil
	}
	switch what {
	case inspectItem:
		return printStored(ctx, pool, `SELECT seq, kind, body::text, recorded_at FROM work_records
WHERE tenant_id = $1 AND item_id = $2 ORDER BY seq`, d.Tenant, id, 0, item.Version, emit)
	case inspectBoard:
		// An event's body without a version is of version 1 (Decision 10).
		return printStored(ctx, pool, `SELECT seq, kind, body::text, committed_at FROM board_events
WHERE tenant_id = $1 AND run_id = $2 ORDER BY seq`, d.Tenant, id, 1, board.Version, emit)
	}
	return printCalls(ctx, pool, d.Tenant, id, emit)
}

// stored is a work record or a board event as inspect prints it: its body as
// stored, or, of a version later than this build reads, its body's text as
// raw.
type stored struct {
	Seq  int64           `json:"seq"`
	Kind string          `json:"kind"`
	V    int             `json:"v"`
	At   string          `json:"at"`
	Body json.RawMessage `json:"body,omitempty"`
	Raw  string          `json:"raw,omitempty"`
}

// printStored prints each row a query of records or events returns. A body
// whose version is above build's is printed raw; absent is the version of a
// body that has none.
func printStored(ctx context.Context, pool *pgxpool.Pool, query, tenant, id string, absent, build int, emit func(any) error) error {
	rows, err := pool.Query(ctx, query, tenant, id)
	if err != nil {
		return fmt.Errorf("read %s: %w", id, err)
	}
	var (
		s    stored
		body string
		at   time.Time
	)
	_, err = pgx.ForEachRow(rows, []any{&s.Seq, &s.Kind, &body, &at}, func() error {
		line := stored{Seq: s.Seq, Kind: s.Kind, V: versionOf([]byte(body), absent), At: timestamp(at)}
		if line.V > build {
			line.Raw = body
		} else {
			line.Body = json.RawMessage(body)
		}
		return emit(line)
	})
	if err != nil {
		return fmt.Errorf("read %s: %w", id, err)
	}
	return nil
}

// versionOf reads a body's version: absent when it holds none, and 0 when
// its version is no whole number, which no build writes.
func versionOf(body []byte, absent int) int {
	var head struct {
		V json.RawMessage `json:"v"`
	}
	if json.Unmarshal(body, &head) != nil {
		return 0
	}
	if head.V == nil {
		return absent
	}
	var v int
	if json.Unmarshal(head.V, &v) != nil {
		return 0
	}
	return v
}

// timestamp prints a time of the database's in UTC, to the nanosecond.
func timestamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// modelCall and toolCall are a run's calls as inspect prints them.
type modelCall struct {
	Call    string `json:"call"`
	ID      string `json:"id"`
	Agent   string `json:"agent"`
	Round   int    `json:"round"`
	Model   string `json:"model"`
	Started string `json:"started"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
}

type toolCall struct {
	Call   string `json:"call"`
	ID     string `json:"id"`
	Agent  string `json:"agent"`
	Round  int    `json:"round"`
	Server string `json:"server"`
	Tool   string `json:"tool"`
	// Name is server/tool, or the tool of a call with no server: a board
	// tool's.
	Name    string `json:"name"`
	Started string `json:"started"`
	Status  string `json:"status"`
	Failure string `json:"failure"`
	// Reason is a failed call's outcome, what it was told, as its output
	// holds it; empty for a call that succeeded, whose output is the data it
	// read, and for one without an outcome.
	Reason string `json:"reason"`
}

// printCalls prints a run's model calls, then its tool calls, each in the
// order they began.
func printCalls(ctx context.Context, pool *pgxpool.Pool, tenant, run string, emit func(any) error) error {
	rows, err := pool.Query(ctx, `
SELECT c.call_id, c.agent, c.round, c.model, c.started_at, coalesce(o.status, ''), coalesce(o.reason, '')
FROM model_calls c LEFT JOIN model_call_outcomes o USING (tenant_id, call_id)
WHERE c.tenant_id = $1 AND c.run_id = $2 ORDER BY c.started_at, c.call_id`, tenant, run)
	if err != nil {
		return fmt.Errorf("read the model calls of run %s: %w", run, err)
	}
	m := modelCall{Call: "model"}
	var at time.Time
	_, err = pgx.ForEachRow(rows, []any{&m.ID, &m.Agent, &m.Round, &m.Model, &at, &m.Status, &m.Reason}, func() error {
		m.Started = timestamp(at)
		return emit(m)
	})
	if err != nil {
		return fmt.Errorf("read the model calls of run %s: %w", run, err)
	}
	rows, err = pool.Query(ctx, `
SELECT c.call_id, c.agent, c.round, c.server, c.tool, c.started_at, coalesce(o.status, ''), coalesce(o.failure, ''), coalesce(o.output, '')
FROM tool_calls c LEFT JOIN tool_call_outcomes o USING (tenant_id, call_id)
WHERE c.tenant_id = $1 AND c.run_id = $2 ORDER BY c.started_at, c.call_id`, tenant, run)
	if err != nil {
		return fmt.Errorf("read the tool calls of run %s: %w", run, err)
	}
	tc := toolCall{Call: "tool"}
	var output string
	_, err = pgx.ForEachRow(rows, []any{&tc.ID, &tc.Agent, &tc.Round, &tc.Server, &tc.Tool, &at, &tc.Status, &tc.Failure, &output}, func() error {
		tc.Name, tc.Started, tc.Reason = tc.Tool, timestamp(at), ""
		if tc.Server != "" {
			tc.Name = tc.Server + "/" + tc.Tool
		}
		if tc.Status != "" && tc.Status != "ok" && tc.Status != "recorded" {
			tc.Reason = output
		}
		return emit(tc)
	})
	if err != nil {
		return fmt.Errorf("read the tool calls of run %s: %w", run, err)
	}
	return nil
}
