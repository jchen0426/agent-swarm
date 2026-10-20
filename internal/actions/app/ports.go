// Package app holds the Actions context's use cases, the MCP gateway's read
// path (spec Section 6), and declares the ports they need.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/actions/output"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Errors that ports return. Callers match them with errors.Is.
var (
	// ErrNoSession is returned by Sessions for a token that names no live
	// session: unknown, expired, or of a run that stopped.
	ErrNoSession = errors.New("app: no such session")
	// ErrConnection marks a call that could not reach the server: the
	// connection could not be made or broke, or the server answered 5xx.
	ErrConnection = errors.New("app: connection failure")
	// ErrTooLarge marks an answer larger than the gateway reads.
	ErrTooLarge = errors.New("app: the answer is too large")
	// ErrNeedsInput marks a server that asked for more input instead of
	// answering, which the gateway never gives.
	ErrNeedsInput = errors.New("app: the server asked for input")
)

// ServerError is an error the server answered a call with. Message is the
// server's own text, at most 4 KiB; any data it sent is dropped. Code is the
// error's JSON-RPC code, zero when the failure had none.
type ServerError struct {
	Message string
	Code    int64
}

func (e *ServerError) Error() string { return "the server answered: " + e.Message }

// invalidParams is JSON-RPC's code for invalid parameters: a server that
// answers it refused the agent's arguments (Decision 20).
const invalidParams = -32602

// Session is an agent's session, as the coordinator runs it (Plan 7).
type Session struct {
	Run   board.RunID
	Agent string
	// Commit is the configuration revision the item locked.
	Commit string
	// Round is the run's current round, and Deadline its end.
	Round    int
	Deadline time.Time
	// Class is limits.Primary for a primary run of either arm, and
	// limits.Shadow for a comparison or probation run.
	Class limits.Class
	// Done is closed when the run stops or the session ends, which ends
	// the session's calls at once.
	Done <-chan struct{}
	// DiagnosisOnly is set when a consistency check of the item's trigger
	// failed: write tools are hidden from all of its runs (spec Section 3.6).
	DiagnosisOnly bool
	// Item holds the item's trusted routing values, which write tools' scope
	// bindings see as item (spec Section 3.3).
	Item map[string]string
	// Epoch is the epoch of the holding whose coordinator issued the
	// session: every write the gateway makes for the session carries it, so
	// that the stores refuse it once that holding is over.
	Epoch int64
}

// Sessions finds the session a gateway token authenticates. Tokens are issued
// by the coordinator, scoped to one run and agent, and expire when the run
// stops (spec Section 2).
type Sessions interface {
	Session(ctx context.Context, token string) (Session, error)
}

// Revisions returns a locked configuration revision by full commit id.
type Revisions interface {
	At(ctx context.Context, commit string) (*config.Revision, error)
}

// Server is a registered server, as the gateway reaches it.
type Server struct {
	Name          string
	Endpoint      string
	CredentialRef string
}

// Conn names an upstream connection. Each agent of each run has its own to
// each server, so that no session state a server keeps crosses runs or
// agents. Platform reads use an empty run and agent.
type Conn struct {
	Run    board.RunID
	Agent  string
	Server Server
}

// Offer is one tool a server lists: its definition as the client decodes it,
// or why it has none.
type Offer struct {
	Def config.ToolDefinition
	Err error
}

// Upstream reaches registered MCP servers. A request the holding refuses,
// whose epoch the context carries, is not sent: its listing or call fails
// wrapping ErrUnavailable, and nothing else does.
type Upstream interface {
	// Offered lists the tools the server offers, by name, on a session of
	// its own, so that what one connection was told cannot hide a change.
	Offered(ctx context.Context, s *Server) (map[string]Offer, error)
	// Call calls a tool on the connection.
	Call(ctx context.Context, c *Conn, tool string, args json.RawMessage) (*output.Result, error)
}

// Status is how a call ended.
type Status string

// Statuses of a call.
const (
	StatusOK        Status = "ok"
	StatusReadError Status = "read_error"
	StatusRefused   Status = "refused"
	// StatusRecorded ends a write call that was recorded as a proposal for a
	// person; nothing was sent (spec Section 6.3). It is not a read, so
	// evidence cannot cite it.
	StatusRecorded Status = "recorded"
)

// CallRecord is what the audit store keeps of a call before it is forwarded.
type CallRecord struct {
	ID string
	// Run and Agent are empty for a platform read outside a run, and Agent
	// is empty for every platform read.
	Run        board.RunID
	Agent      string
	Server     string
	Tool       string
	SchemaHash string
	// Args are the call's arguments as they are sent, redacted, or null if
	// they hold an escape that may hide what a redaction rule matches.
	Args  json.RawMessage
	Round int
	// At is when the gateway made the record, by its clock. The audit store
	// keeps its own time instead, the database's (Plan 7d-1, Decision 1).
	At time.Time
	// Cause names the write call a platform read was made for, if any.
	Cause string
}

// Failure is the side a tool call failed on (Decision 20).
type Failure string

// The sides a call fails on. A call that did not fail, or that its round's
// end or its run's stop cut short, has none. A round's coordinator counts
// the failures of the server, the platform and the tool, which the round
// cannot trust, and never the agent's own.
const (
	// FailureServer: the server could not be reached or used: a connection
	// failure, a timeout, a 5xx, no capacity, a degraded server, an answer it
	// cannot read, or a look at its definitions that failed.
	FailureServer Failure = "server"
	// FailurePlatform: the platform failed the call: the board could not be
	// reached, or the session checked again, or the proposal recorded.
	FailurePlatform Failure = "platform"
	// FailureTool: the tool answered with an error of its own (isError),
	// which is how servers report a backend's outage and an agent's bad
	// arguments alike (R1).
	FailureTool Failure = "tool"
	// FailureAgent: the agent's call was at fault: its arguments, an output
	// too large or partial, its calls in flight at their cap, or what its
	// write's policy or scope refused.
	FailureAgent Failure = "agent"
)

// Outcome is what the audit store keeps of a call when it ends.
type Outcome struct {
	ID     string
	Status Status
	// Failure is the side the call failed on, or empty.
	Failure Failure
	// Output is the canonical, redacted output of a call that succeeded, and
	// the redacted error or the reason otherwise.
	Output string
	// At is when the call ended, by the gateway's clock; the audit store
	// keeps the database's time instead.
	At time.Time
}

// Audit is the audit store's record of tool calls (spec Sections 6.2 and 9.4).
type Audit interface {
	// Begin records a call. The record is committed when Begin returns, so
	// that it exists before the call reaches the server.
	Begin(ctx context.Context, rec *CallRecord) error
	// End records how a call ended.
	End(ctx context.Context, o *Outcome) error
}

// Reason says why a tool is hidden.
type Reason string

// Reasons a tool is hidden.
const (
	// ReasonChanged: the server's definition no longer hashes to the pin, or
	// cannot be read.
	ReasonChanged Reason = "changed"
	// ReasonHiddenCharacters: the definition holds characters a reviewer
	// could miss.
	ReasonHiddenCharacters Reason = "hidden_characters"
	// ReasonSchema: the gateway cannot check the tool's arguments: its input
	// schema does not compile or asks for argument headers, or a bound names
	// an argument the schema lacks.
	ReasonSchema Reason = "schema"
)

// Hide hides one pinned tool definition of one registration until the
// registration is reviewed again (spec Section 3.3): any change to the
// registration's file, which review approves, gives it another hash.
type Hide struct {
	Endpoint     string
	Server       string
	Tool         string
	PinnedHash   string
	Registration string
	Reason       Reason
	// Observed is the hash of the definition the server offered, if any.
	Observed string
	// At is when the tool was hidden: by the gateway's clock when it hides
	// it, and as Hides kept it, the database's time, when it reads it.
	At time.Time
}

// Hides keeps hidden tools across restarts. A hide is also the notice that
// the registration's owner is notified of (Plan 9).
type Hides interface {
	// Hide records a hide. Recording one again changes nothing.
	Hide(ctx context.Context, h *Hide) error
	// Hidden returns every recorded hide.
	Hidden(ctx context.Context) ([]Hide, error)
}

// Conditions evaluates the conditions of tool policy: a read's partialWhen
// on its output, and a write tool's scope bindings.
type Conditions interface {
	PartialWhen(ctx context.Context, expr string, result any) (bool, error)
	// Scope returns the value an argument must equal, from the cited
	// hypothesis's key and the item's trusted routing values.
	Scope(ctx context.Context, expr string, hypothesis, item map[string]string) (string, error)
}

// Board is a run's blackboard as the gateway uses it: through the board
// tools, and to check and record write calls (spec Sections 4.2, 5.2 and
// 5.3). The Work context's application implements it. Posts carry the round
// the agent acted in; one that arrives after that round ended is refused.
type Board interface {
	// Context returns the run's round, snapshot, wrap-up hypothesis and
	// settings.
	Context(ctx context.Context, run board.RunID) (board.Context, error)
	// View returns what an agent sees of the board in the round.
	View(ctx context.Context, run board.RunID, agent string, since int64) (board.View, error)
	PostEvidence(ctx context.Context, run board.RunID, round int, author board.Author, in *board.EvidenceInput) (board.Posted, error)
	PostHypothesis(ctx context.Context, run board.RunID, round int, author board.Author, in *board.HypothesisInput) (board.Posted, error)
	Retract(ctx context.Context, run board.RunID, round int, author board.Author, evidence board.EntryID) (board.Posted, error)
	PostTask(ctx context.Context, run board.RunID, round int, author board.Author, in *board.TaskInput) (board.Posted, error)
	ClaimTask(ctx context.Context, run board.RunID, round int, author board.Author, task board.EntryID) (board.Posted, error)
	CompleteTask(ctx context.Context, run board.RunID, round int, author board.Author, in *board.CompletionInput) (board.Posted, error)
	EndTurn(ctx context.Context, run board.RunID, round int, author board.Author) (board.Posted, error)
	// Proposable checks a write call against the board before any read is
	// made for it, and commits nothing.
	Proposable(ctx context.Context, run board.RunID, round int, p *board.Proposal) (board.Proposable, error)
	// Propose checks a proposal again and commits it.
	Propose(ctx context.Context, run board.RunID, round int, p *board.Proposal) (board.Posted, error)
	// Notice commits a platform notice in the run's current round.
	Notice(ctx context.Context, run board.RunID, author board.Author, n *board.Notice) (board.Posted, error)
}

// IDs issues call ids, such as ids.UUIDv7: unique, and not guessable from
// another call's.
type IDs interface {
	NewID() string
}
