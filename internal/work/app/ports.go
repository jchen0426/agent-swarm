package app

import (
	"context"
	"errors"
	"time"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/control"
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
	// with item.ErrVersion, and the coordinator skips the item (Decision 19).
	Load(ctx context.Context, id item.ID) ([]item.Record, error)
	// Active returns the items the coordinator drives: those that are not
	// closed, which have an opening and no close that a reopen did not
	// follow (Decision 12). An item awaiting its close is among them: it needs
	// its auto-close timer.
	Active(ctx context.Context) ([]item.ID, error)
	// Append serializes writers per item. decide sees every committed
	// record of the item and returns the records to commit; an error from
	// decide commits nothing and is returned as is. Append refuses an
	// automatic close that it would stamp before its due, on its own rule for
	// an item's times, with item.ErrNotDue, and commits nothing (R41): the
	// coordinator's clock is an estimate of the store's.
	Append(ctx context.Context, id item.ID, decide func(history []item.Record) ([]item.Draft, error)) ([]item.Record, error)
	// UnstaffedBy returns the items whose item_awaiting_close names the kill
	// switch's row seq, the on-row that left them not staffed, by id: those
	// that the off lists (Decision 5).
	UnstaffedBy(ctx context.Context, seq int64) ([]item.ID, error)
}

// ClosedLister lists the items closed lately. Only the views take it, so that
// the Records the coordinator takes stays as it is (R92); both stores
// implement it.
type ClosedLister interface {
	// ClosedSince returns the items whose latest item_closed committed within
	// window of the store's time, by the database's clock in Postgres, and
	// which no later item_reopened followed, by id.
	ClosedSince(ctx context.Context, window time.Duration) ([]item.ID, error)
}

// Incidents reads the items of an incident (Decision 18; R102): a narrow port
// beside Records, which the coordinator reads for an unlink's "alone" and a
// link's items, and the views for an item's incident. Both stores implement
// it.
type Incidents interface {
	// IncidentItems returns the items whose latest record is in incident,
	// each once, sorted by id; none for an incident with no item.
	IncidentItems(ctx context.Context, incident item.IncidentID) ([]item.ID, error)
}

// KillSwitch is the kill switch's state (Decision 5): control.KillSwitch,
// which the stores return without importing this package.
type KillSwitch = control.KillSwitch

// ErrStale marks a request to the kill switch whose After is not the latest
// row's number, while the state it asks does not hold: control.ErrStale,
// which the stores return with the state.
var ErrStale = control.ErrStale

// Controls is the coordinator's port to the platform-wide controls. Its
// SetKillSwitch returns the switch's state after it, and whether it appended
// a row, whatever it did: with ErrStale, the state is returned too. It writes
// under the holding whose epoch ctx carries, and its guard's refusal wraps
// ErrLeaseLost (Decision 5).
type Controls interface {
	// KillSwitch reads the kill switch's state: off until it is first set.
	KillSwitch(ctx context.Context) (KillSwitch, error)
	// SetKillSwitch turns the kill switch on or off for by, for reason, its
	// text redacted, as a request whose sender saw after as the latest row.
	// It appends no row when the state asked holds already, whatever after
	// says, and fails with ErrStale when after is not the latest row's
	// number.
	SetKillSwitch(ctx context.Context, on bool, by string, reason board.Reason, after int64) (state KillSwitch, changed bool, err error)
}

// KillSwitchRows is the leader's port for its renewal check (Decision 5): the
// number of the kill switch's latest row, which the leader compares with its
// coordinator's view after each good renewal. Serve reads it on the lease's
// pool, as the renewals.
type KillSwitchRows interface {
	// LatestKillSwitch returns the number of the kill switch's latest row, 0
	// before the first.
	LatestKillSwitch(ctx context.Context) (int64, error)
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
	// InFlight counts a round's model calls without an outcome: those of the
	// run that the LLM gateway recorded in the round (model_calls.round). A
	// call of an earlier round still in flight holds its round's spend, but
	// not a later round's end (Decision 9).
	InFlight(ctx context.Context, run string, round int) (int, error)
	// Failed returns a run's calls whose outcome is refused, error or
	// unknown, in the order they began.
	Failed(ctx context.Context, run string) ([]ModelCall, error)
}

// ToolCalls is what the MCP gateway's records say of a round's tool calls.
type ToolCalls interface {
	// Failed counts the round's failed tool calls that Decision 20 counts.
	Failed(ctx context.Context, run string, round int) (int, error)
}

// Sweeper gives the model calls an instance left without an outcome one
// (Decision 7). It is the LLM gateway's, which prices such a call as it
// prices a lost one (R10).
type Sweeper interface {
	// Sweep gives each call without an outcome that began before before, in
	// the database's time, an outcome of unknown, for reason, and returns how
	// many it gave one. Its writes act for the holding whose epoch ctx
	// carries: one that finds the lease gone stops it, with an error that
	// wraps ErrLeaseLost. A call whose outcome cannot be recorded otherwise
	// is logged and left, and the others are swept.
	Sweep(ctx context.Context, before time.Time, reason string) (int, error)
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
	// PreviousHolder is the holder of the holding before this one, as the
	// take read it; empty for the lease's first holding.
	PreviousHolder string
}

// ErrSchema marks a schema this build does not run on: the schema is behind
// the build, or its oldest reader is above it (Decision 18).
var ErrSchema = errors.New("app: this build does not run on the schema")

// ClockSampler samples the database's clock (clock.DB): the leader samples
// it around each take and renewal, so that the coordinator's clock is fresh
// when it takes over.
type ClockSampler interface {
	Sample(ctx context.Context) error
}

// Schema checks that this build runs on the database's schema. An error
// that wraps ErrSchema stops the leader for good; any other is a failed
// read, tried again.
type Schema interface {
	Check(ctx context.Context) error
}

// Clock tells the coordinator the time as the database tells it: records
// and deadlines are in database time, so that an instance that takes over
// reads them as the instance that wrote them did. The leader's Clock is the
// process's monotonic clock instead, by which it watches its holding.
type Clock interface {
	Now() time.Time
	// Local returns a time of the database's in the process's clock, in
	// which the gateways compare deadlines.
	Local(t time.Time) time.Time
}

// Timers runs a function at a time of the Clock's. The coordinator arms its
// timers from the records, so that an instance that takes over arms them
// again. It arms and cancels them under its lock, which fn takes, as the
// leader does under its own. Each fn runs on a goroutine of its own,
// concurrently with the others and with the caller, and never before At
// returns. cancel never waits for a fn that runs: a fn whose time came may
// run after cancel, so it checks, under the lock it takes, that it is still
// wanted.
type Timers interface {
	// At runs fn at t, or as soon as it can when t has passed, and returns
	// a function that cancels it.
	At(t time.Time, fn func()) (cancel func())
}

// Turn is an agent's turn in a round, as the runner starts it (ADR-006):
// every turn starts from a fresh context, the round's prompt, the agent's
// skills and the board, read through read_board.
type Turn struct {
	// ID names the turn: the runner reports its launch and its end with it,
	// and a Start repeated with it, while the runner has the turn, starts
	// nothing.
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
	// Commit is the configuration's commit the item locked when it started,
	// at which the runner reads the agent's skills (Decision 12).
	Commit string
	// Image is the agent's runtime image, by digest, as the item locked it.
	Image  string
	Model  string
	Skills []string
	Prompt string
}

// Turns runs agents' turns: the runner, as host processes for development
// and tests (Plan 7d-1, Decision 12), or in Docker sandboxes (Plan 7d-2,
// Decision 5). The coordinator calls Turns under its lock. A runner reports a
// turn's end once, with its ID, through Coordinator.Exited, and how a launch
// it left pending ended once, through Coordinator.Launched. Both take the
// coordinator's lock, so a runner calls them holding none of its own locks:
// the coordinator calls Stop under its lock, and the reverse order would
// deadlock it, and the leader, which fences under its own. No method calls
// them before it returns, or waits for a turn to end, or for an engine. A
// runner forgets a turn once it has reported the turn's end: the coordinator
// never starts again a turn whose end it learned.
type Turns interface {
	// Start starts a turn, or registers it for a launch after Start returns.
	// It fails when it cannot: Holding.Valid refused the epoch ctx carries
	// (an error wrapping ErrUnheld), or the turn's output file, process or
	// sandbox could not be made. A turn whose Start failed is tried again
	// with the same ID while its round lasts, and counts as unstarted.
	// pending says the runner launches the turn and reports the launch
	// through Launched; a turn not pending runs. A Start repeated with the
	// ID of a turn the runner has starts nothing. A turn whose spec cannot
	// be delivered once it runs ends unavailable. Neither ends crashed.
	Start(ctx context.Context, t *Turn) (pending bool, err error)
	// Stop ends a turn at once: its launch is cancelled, its process or
	// container killed. It never waits.
	Stop(run board.RunID, agent string)
	// StopAll ends every turn the instance runs, as Stop does.
	StopAll()
}

// UpstreamSessions closes a run's upstream MCP sessions when it stops, in the
// background, never waiting (Decision 3): the MCP gateway's pool of sessions
// with registered servers.
type UpstreamSessions interface {
	// CloseRun lets go of every session kept for run, of every agent and
	// server, and closes each in the background, while its holding holds. The
	// coordinator calls it under its lock: it never waits.
	CloseRun(run board.RunID)
}

// ErrUnheld marks a turn's start or launch that the holding refused: the
// holding it acts for is over. It is a launch that failed, posts no notice,
// and never fences.
var ErrUnheld = errors.New("app: the holding refused the turn's start")

// LaunchReason says why a launch failed (Decision 7).
type LaunchReason string

const (
	ReasonEngine    LaunchReason = "engine"    // the engine did not answer, or the launch outlasted its bound
	ReasonImage     LaunchReason = "image"     // the image is missing and not in the store, breaks the contract, or reads no spec the runner writes
	ReasonLaunch    LaunchReason = "launch"    // any other failure
	ReasonHolding   LaunchReason = "holding"   // the holding refused: no notice
	ReasonCancelled LaunchReason = "cancelled" // a stop, a fence or a drain: no notice
)

// Launch is how a launch that Start left pending ended. Err is nil when the
// turn runs: its end then comes through Exited, never before Launched.
// Otherwise the launch failed, as a failed Start does, for Reason.
type Launch struct {
	Run    board.RunID
	Agent  string
	Round  int
	Turn   string
	Reason LaunchReason
	Err    error
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
