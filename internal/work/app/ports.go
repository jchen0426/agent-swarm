package app

import (
	"context"
	"time"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/tokens"
)

// Records keeps work items' records.
type Records interface {
	// Load returns an item's records in commit order. A store that cannot
	// decode a record's body reports it wrapped with item.ErrInvalid, which
	// tells a corrupt row from a read that failed: a body of this version that
	// does not decode, or whose version is missing, null or below 1. A record
	// of another version loads as its version alone, which the fold refuses
	// with item.ErrVersion.
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
	// SpentToday returns the spend of the calls admitted since the
	// database's UTC midnight (Decision 8).
	SpentToday(ctx context.Context) (int64, error)
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
// it over. It is holding.ErrLeaseLost, which every guard's refusal wraps, so
// that the gateways test for it without importing the coordinator.
var ErrLeaseLost = holding.ErrLeaseLost

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

// Clock tells the coordinator the time as the database tells it: records
// and deadlines are in database time, so that an instance that takes over
// reads them as the instance that wrote them did.
type Clock interface {
	Now() time.Time
	// Local returns a time of the database's in the process's clock, in
	// which the gateways compare deadlines.
	Local(t time.Time) time.Time
}

// Timers runs a function at a time of the Clock's. The coordinator arms its
// timers from the records, so that an instance that takes over arms them
// again. It arms and cancels them under its lock, which fn takes: At never
// runs fn before it returns, and cancel never waits for a fn that runs.
type Timers interface {
	// At runs fn at t, or as soon as it can when t has passed, and returns
	// a function that cancels it.
	At(t time.Time, fn func()) (cancel func())
}

// Turn is an agent's turn in a round, as the runner starts it (ADR-006):
// every turn starts from a fresh context, the round's prompt, the agent's
// skills and the board, read through read_board.
type Turn struct {
	// ID names the turn: the runner reports its end with it, and a Start
	// repeated with it starts the turn once at most.
	ID    string
	Run   board.RunID
	Agent string
	Round int
	// WrapUp names the converged hypothesis in a wrap-up round.
	WrapUp board.EntryID
	// Deadline is the round's end, in the process's clock.
	Deadline time.Time
	// MCP and LLM are the session's tokens at the two gateways.
	MCP, LLM tokens.Secret
	// Image is the agent's runtime image, by digest, as the item locked it.
	Image  string
	Model  string
	Skills []string
	Prompt string
}

// Turns runs agents' turns: the runner, in sandboxes (Plan 7d). A turn's
// end is reported once, with its ID, through Coordinator.Exited. The
// coordinator calls Turns under its lock, which Exited takes: no method calls
// Exited before it returns, or waits for a turn to end.
type Turns interface {
	// Start starts a turn, and returns once the runner has it, without
	// waiting for its sandbox. It fails only when the runner cannot be
	// reached: a turn whose sandbox fails to start ends crashed.
	Start(ctx context.Context, t *Turn) error
	// Stop ends a turn at once.
	Stop(run board.RunID, agent string)
	// StopAll ends every turn the instance runs.
	StopAll()
}

// Exit is how an agent's turn in a round ended.
type Exit struct {
	Run   board.RunID
	Agent string
	Round int
	// Turn is the ID of the turn that ended.
	Turn  string
	Class item.TurnClass
}

// Revisions are the configuration's revisions, as the registry keeps them.
type Revisions interface {
	// Current returns the revision new items start from.
	Current() *config.Revision
	// At returns the revision of a commit, as an item locked it.
	At(ctx context.Context, commit string) (*config.Revision, error)
	// Lock locks a revision with the deployment's runtime images.
	Lock(rev *config.Revision) (config.Lock, error)
}
