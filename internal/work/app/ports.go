package app

import (
	"context"
	"errors"
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

// ErrLeaseLost marks a write refused, or a renewal failed, because the
// instance no longer holds the lease: it expired, or another instance took
// it over.
var ErrLeaseLost = errors.New("app: the lease is lost")

// Lease is the platform's lease: one instance holds it at a time, and only
// the holder acts (spec Section 8.2; ADR-004). Every write of the holder
// checks it in its transaction, so that a holder that lost it writes
// nothing more.
type Lease interface {
	// Take takes the lease when no one holds it, and reports whether it
	// did. A new holding has a new epoch, and taking it waits for the
	// writes of the last holding still in flight.
	Take(ctx context.Context) (Term, bool, error)
	// Renew extends the holding. It fails with ErrLeaseLost once the
	// holding expired or was taken over.
	Renew(ctx context.Context) error
	// Release gives the lease up, so that another instance may take it at
	// once.
	Release(ctx context.Context) error
	// TTL is how long a holding lasts from its last renewal.
	TTL() time.Duration
}

// Term is a holding of the lease: its epoch, and the database's time when
// it was taken.
type Term struct {
	Epoch int64
	At    time.Time
	// Previous is when the holding before it expired, or was released, in
	// the database's time; zero for the lease's first holding.
	Previous time.Time
}
