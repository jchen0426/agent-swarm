package app

import (
	"context"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// Records keeps work items' records.
type Records interface {
	// Load returns an item's records in commit order.
	Load(ctx context.Context, id item.ID) ([]item.Record, error)
	// Active returns the items the coordinator drives: those that do not
	// await their close.
	Active(ctx context.Context) ([]item.ID, error)
	// Append serializes writers per item. decide sees every committed
	// record of the item and returns the records to commit; an error from
	// decide commits nothing and is returned as is.
	Append(ctx context.Context, id item.ID, decide func(history []item.Record) ([]item.Draft, error)) ([]item.Record, error)
}

// Spend is what the LLM gateway's records say of model calls, in
// cost-weighted tokens: a call counts what its outcome says it cost, and
// its worst case until its outcome is recorded.
type Spend interface {
	// Spent returns a run's spend.
	Spent(ctx context.Context, run string) (int64, error)
	// SpentSince returns the spend of the calls admitted at or after since.
	SpentSince(ctx context.Context, since time.Time) (int64, error)
	// InFlight counts a run's calls without an outcome.
	InFlight(ctx context.Context, run string) (int, error)
	// Failed returns a run's calls whose outcome is refused, error or
	// unknown, in the order they began.
	Failed(ctx context.Context, run string) ([]ModelCall, error)
}

// ModelCall is a model call as the coordinator notices it on the board.
type ModelCall struct {
	ID    string
	Agent string
	// Status is the call's outcome: refused, error or unknown.
	Status string
}
