// Package app holds the LLM context's use case, the LLM gateway: the only
// route from an agent's sandbox to the model vendor (spec Sections 2, 5.6,
// 7, 9.3 and 13.6; ADR-007). It declares the ports it needs.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/jchen0426/agent-swarm/internal/llm/cost"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
)

// Errors that ports return. Callers match them with errors.Is.
var (
	// ErrNoSession is returned by Sessions for a token that names no live
	// session: unknown, expired, or of a run that stopped.
	ErrNoSession = errors.New("app: no such session")
	// ErrNotSent marks an attempt whose request never reached the vendor:
	// another attempt may succeed, and costs nothing twice.
	ErrNotSent = errors.New("app: the request was not sent")
	// ErrLost marks an attempt whose request reached the vendor and whose
	// answer did not come back: the vendor may have charged for it, so it is
	// never tried again, and its cost is counted as its worst case.
	ErrLost = errors.New("app: the answer was lost")
	// ErrConflict marks an outcome refused because the call already has
	// another, as when a sweep settled it (R18). ModelCalls.End wraps it
	// beside postgres.ErrConflict, which it returns today.
	ErrConflict = errors.New("llmapp: the call already has another outcome")
	// ErrNoKey marks an attempt not made because the deployment's key for
	// the vendor could not be read: the platform's own failure, not the
	// vendor's, which is not tried again and costs nothing.
	ErrNoKey = errors.New("app: the vendor's key could not be read")
)

// Session is an agent's session, as the coordinator runs it (Plan 7).
type Session struct {
	Run   string
	Agent string
	// Commit is the configuration revision the item locked.
	Commit string
	// Round is the run's current round, and Deadline its end: no call
	// starts after it, and no attempt is tried again that could not end
	// before it.
	Round    int
	Deadline time.Time
	// Class says whether the run is cut first as spend nears the daily cap.
	Class cost.Class
	// Budget is the run's token budget, in cost-weighted tokens, as the
	// coordinator sets it now.
	Budget int64
	// Done is closed when the run stops or the session ends: the session's
	// requests are answered at once, and no attempt is tried again.
	Done <-chan struct{}
	// Epoch is the epoch of the holding whose coordinator issued the
	// session: every write the gateway makes for the session carries it, so
	// that the stores refuse it once that holding is over.
	Epoch int64
}

// Sessions finds the session an LLM gateway token authenticates. Tokens
// are issued by the coordinator, one per agent session, scoped to its run
// and agent, and expire when the run stops (spec Section 2).
type Sessions interface {
	Session(ctx context.Context, token string) (Session, error)
}

// Revisions returns a locked configuration revision by full commit id.
type Revisions interface {
	At(ctx context.Context, commit string) (*config.Revision, error)
}

// Answer is the vendor's answer to one attempt.
type Answer struct {
	Status int
	Body   []byte
	// RequestID is the vendor's id of the request, if it gave one.
	RequestID string
	// RetryAfter is how long the vendor asked the next attempt to wait.
	RetryAfter time.Duration
}

// Vendor sends a checked request to the vendor's Messages API, with the
// deployment's key and nothing of the client's. An attempt that fails
// before or after the request reached the vendor is ErrNotSent or ErrLost,
// and one whose key cannot be read is ErrNoKey.
type Vendor interface {
	Send(ctx context.Context, body []byte) (*Answer, error)
}

// Status is how a model call ended.
type Status string

// Statuses of a model call.
const (
	StatusOK      Status = "ok"
	StatusRefused Status = "refused"
	// StatusError is a call that failed, for which the vendor charged
	// nothing, or a call whose answer the gateway withheld because its text
	// hides what a redaction rule matches behind an escape, which no record
	// may hold: that one counts the usage the vendor reported.
	StatusError Status = "error"
	// StatusUnknown is a call the vendor may have charged for without an
	// answer the gateway could read: its cost is counted as its worst case.
	StatusUnknown Status = "unknown"
)

// CallRecord is what the audit store keeps of a model call before it is
// forwarded.
type CallRecord struct {
	ID    string
	Run   string
	Agent string
	Round int
	Class cost.Class
	// Model is the model the request named, "" when it could not be read.
	Model string
	// Request is the request as it was forwarded, redacted, its thinking
	// blocks too, which go to the vendor as the gateway gave them; a call
	// recorded and then not made keeps it so as well. For a request not
	// forwarded, it is at most the first 4 KiB of its JSON with every
	// string, key and number redacted, or "" when it could not be read.
	Request string
	// RequestBytes is the size of the request as it was sent or received.
	// RequestDigest is the SHA-256, in hex, of the request as Request
	// records it, in full: what was forwarded, redacted, or the redacted
	// JSON of a request not forwarded; "" when there is none. It never
	// gives back what the record hides.
	RequestBytes  int64
	RequestDigest string
	// Estimate is the call's worst case in cost-weighted tokens, which its
	// run's spend and the day's count until its outcome is recorded; 0 for
	// a request that is not forwarded.
	Estimate int64
	// At is when the call was admitted, by the gateway's clock. The record
	// keeps its own time instead, the database's, which says the day the
	// call counts in (Plan 7d-1, Decisions 1 and 8).
	At time.Time
}

// Outcome is what the audit store keeps of a model call when it ends.
type Outcome struct {
	ID     string
	Status Status
	// Reason says why a call was refused or failed, redacted.
	Reason string
	// Completion is the answer's record, redacted, for a call that
	// succeeded.
	Completion string
	Usage      cost.Usage
	// Spend is the call's cost in pico-USD, and Weighted the same in
	// cost-weighted tokens.
	Spend, Weighted int64
	// Attempts counts the attempts made to reach the vendor.
	Attempts int
	// Details is how the vendor served the answer, as JSON, "" when there
	// is no answer.
	Details string
	// At is when the call ended, by the gateway's clock; the record keeps
	// its own time instead.
	At time.Time
}

// Calls is the audit store's record of model calls (spec Sections 9.3 and
// 9.4), which the budgets are counted from. A call counts what its outcome
// says it cost once that is recorded, and its estimate until then: while it
// is in flight, and for good if its outcome never is.
type Calls interface {
	// Begin records a call and its request. The record is committed when
	// Begin returns, so that it exists before the call reaches the vendor.
	Begin(ctx context.Context, rec *CallRecord) error
	// End records how a call ended, with its completion. Recording the
	// same outcome again changes nothing; another is refused, wrapping
	// ErrConflict.
	End(ctx context.Context, o *Outcome) error
	// Spent returns the cost-weighted tokens of a run's calls.
	Spent(ctx context.Context, run string) (int64, error)
	// SpentToday returns the cost-weighted tokens of the tenant's calls
	// admitted since the database's UTC midnight, the day the daily cap
	// counts (Decision 8). A call is admitted when its record is committed.
	SpentToday(ctx context.Context) (int64, error)
	// Unsettled returns the calls without an outcome that began before
	// before, by the record's clock, the database's, in the order they
	// began: a sweep gives them one (Plan 7d-1, Decision 7).
	Unsettled(ctx context.Context, before time.Time) ([]Unsettled, error)
}

// Unsettled is a model call without an outcome, with what Plan 6b's
// estimate of a lost call reads.
type Unsettled struct {
	ID    string
	Model string
	// Estimate is the stored worst case, in cost-weighted tokens: 0 for a
	// request that was not forwarded.
	Estimate int64
	// RequestBytes is the stored request's size.
	RequestBytes int64
	// MaxTokens is read from the call's stored request
	// (prompt.RecordedMaxTokens): 0 where the record holds none it can read.
	MaxTokens int64
}
