// Package item holds a work item's lifecycle as the coordinator keeps it: the
// records it writes before it acts, the state they fold into, and the rules
// that decide how a round ends and a run stops (spec Sections 4.1, 5.2 and
// 5.6).
package item

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Errors returned by the record rules. Callers match them with errors.Is.
var (
	// ErrInvalid marks a record that breaks the lifecycle's order, or whose
	// body does not decode.
	ErrInvalid = errors.New("item: invalid record")
	// ErrVersion marks a record of a version this code does not read: an
	// older binary never acts on records it cannot read.
	ErrVersion = errors.New("item: unknown record version")
)

// Version is the version of the record bodies this code writes and reads.
const Version = 1

// ID identifies a work item, and IncidentID its incident.
type (
	ID         string
	IncidentID string
)

// Kind is a record's type.
type Kind string

// Record kinds.
const (
	KindOpened        Kind = "item_opened"
	KindStarted       Kind = "item_started"
	KindRunCreated    Kind = "run_created"
	KindRoundOpened   Kind = "round_opened"
	KindRoundEnded    Kind = "round_ended"
	KindRunStopped    Kind = "run_stopped"
	KindAwaitingClose Kind = "item_awaiting_close"
)

// Record is one committed work record. Seq orders a tenant's records, and At
// is the database time of the commit.
type Record struct {
	Seq      int64
	ID       string
	Item     ID
	Incident IncidentID
	// Run is the run the record is about, or empty.
	Run  board.RunID
	At   time.Time
	Body Body
}

// Draft is a record that passed the rules, not committed yet. The store
// assigns its Seq and At.
type Draft struct {
	ID       string
	Item     ID
	Incident IncidentID
	Run      board.RunID
	Body     Body
}

// Body holds a record's content: its version and exactly one kind.
type Body struct {
	V             int            `json:"v"`
	Opened        *Opened        `json:"opened,omitempty"`
	Started       *Started       `json:"started,omitempty"`
	RunCreated    *RunCreated    `json:"runCreated,omitempty"`
	RoundOpened   *RoundOpened   `json:"roundOpened,omitempty"`
	RoundEnded    *RoundEnded    `json:"roundEnded,omitempty"`
	RunStopped    *RunStopped    `json:"runStopped,omitempty"`
	AwaitingClose *AwaitingClose `json:"awaitingClose,omitempty"`
}

// UnmarshalJSON decodes a body of this version whole, and of another only
// its version, so that the fold refuses it with ErrVersion: a later version
// may write a body this one cannot decode. A body whose version is missing,
// null or below 1 does not decode: versions start at 1, so it is corrupt,
// not a later version.
func (b *Body) UnmarshalJSON(raw []byte) error {
	var v struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	if v.V < 1 {
		return fmt.Errorf("the body holds version %d, and versions start at 1", v.V)
	}
	if v.V != Version {
		*b = Body{V: v.V}
		return nil
	}
	type body Body
	var out body
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	*b = Body(out)
	return nil
}

// Kind returns the kind of record the body holds, or "" when it holds none
// or more than one.
func (b *Body) Kind() Kind {
	var kind Kind
	n := 0
	for _, f := range []struct {
		set  bool
		kind Kind
	}{
		{b.Opened != nil, KindOpened}, {b.Started != nil, KindStarted}, {b.RunCreated != nil, KindRunCreated},
		{b.RoundOpened != nil, KindRoundOpened}, {b.RoundEnded != nil, KindRoundEnded},
		{b.RunStopped != nil, KindRunStopped}, {b.AwaitingClose != nil, KindAwaitingClose},
	} {
		if f.set {
			kind, n = f.kind, n+1
		}
	}
	if n != 1 {
		return ""
	}
	return kind
}

// Arms and how an item was assigned one (spec Section 13.1). Until the
// measurement window exists (Plan 8), every item is visible, with no window.
const (
	ArmVisible = "visible"
	NoWindow   = "no_window"
)

// Opened opens a work item (spec Section 4.1). Its title is redacted before
// it is recorded, and Routing holds trusted values only.
type Opened struct {
	// Type is the item's work item type.
	Type  string `json:"type"`
	Title string `json:"title"`
	// Source says who opened the item: "person", or an inbound source.
	Source     string            `json:"source"`
	Routing    map[string]string `json:"routing,omitempty"`
	Principal  string            `json:"principal,omitempty"`
	Responders []string          `json:"responders,omitempty"`
	Arm        string            `json:"arm"`
	How        string            `json:"how"`
	// Digest is the digest of the request that opened the item by its key,
	// by which an opening repeated under the item's id is told the same
	// request from another (Decision 14). Bodies of version 1 hold it, as no
	// release was tagged before it came (R12).
	Digest string `json:"digest,omitempty"`
}

// Started locks the configuration an item runs under (spec Section 3.4): the
// revision, and the runtime images its agents start from.
type Started struct {
	Commit string            `json:"commit"`
	Digest string            `json:"digest"`
	Images map[string]string `json:"images"`
}

// Classes and modes of runs. Until Plan 8 an item has one run, the primary,
// which people see.
const (
	ClassPrimary = "primary"
	ModeVisible  = "visible"
)

// Budget is a run's budget (spec Section 5.6).
type Budget struct {
	WallClock time.Duration `json:"wallClock"`
	// Tokens counts cost-weighted tokens, as the LLM gateway prices them.
	Tokens int64 `json:"tokens"`
	Rounds int   `json:"rounds"`
}

// RunCreated creates a run with its settings, budget and invited agents.
type RunCreated struct {
	Class      string         `json:"class"`
	Mode       string         `json:"mode"`
	Settings   board.Settings `json:"settings"`
	Budget     Budget         `json:"budget"`
	RoundLimit time.Duration  `json:"roundLimit"`
	// Agents are the agents invited, in the revision's order.
	Agents []string `json:"agents"`
}

// RoundOpened opens a round. The coordinator writes it before the round's
// board entry, and repeats what follows it after a crash.
type RoundOpened struct {
	Round int `json:"round"`
	// WrapUp names the converged hypothesis in a wrap-up round.
	WrapUp board.EntryID `json:"wrapUp,omitempty"`
	// OpenedAt and Deadline are in database time.
	OpenedAt time.Time `json:"openedAt"`
	Deadline time.Time `json:"deadline"`
	// Budget is the run's token budget as the LLM gateway holds its sessions
	// to it in the round: the run's, or in a wrap-up round its allowance.
	Budget int64 `json:"budget"`
	// Agents are the agents the round invites.
	Agents []string `json:"agents"`
}

// EndedBy says how a round ended.
type EndedBy string

// How rounds end.
const (
	// EndedByTurns: every turn ended.
	EndedByTurns EndedBy = "turns"
	// EndedByDeadline: the round's deadline passed.
	EndedByDeadline EndedBy = "deadline"
	// EndedByOutage: the deadline passed while no instance held the lease.
	// No turn is counted, and the round counts toward no budget.
	EndedByOutage EndedBy = "outage"
)

// TurnClass says how an agent's turn ended.
type TurnClass string

// Turn classes.
const (
	// TurnEnded: the agent called end_turn.
	TurnEnded TurnClass = "ended"
	// TurnFinished: the runtime ended without end_turn: no tool called, a
	// step or token limit, a reply cut short.
	TurnFinished TurnClass = "finished"
	// TurnInterrupted: the deadline or a stop ended the turn.
	TurnInterrupted TurnClass = "interrupted"
	// TurnRefused: the LLM gateway refused a model call (4xx).
	TurnRefused TurnClass = "refused"
	// TurnVendor: the model vendor failed after the gateway's retries (5xx).
	TurnVendor TurnClass = "vendor"
	// TurnCrashed: the sandbox or the runtime failed. Only crashes count
	// toward an agent's removal (spec Section 8.2).
	TurnCrashed TurnClass = "crashed"
	// TurnUnstarted: the turn did not start in time to count. The
	// coordinator could not start it before the round's deadline, as when
	// the runner could not be reached, or started it with less than a third
	// of the round's span left and it still ran at the deadline, or was
	// reported interrupted; so does a turn reported interrupted whose start
	// the coordinator never learned of. Such a turn is not quiet, takes no
	// part in its round, and leaves the agent's count of crashes as it was.
	TurnUnstarted TurnClass = "unstarted"
)

// StopReason says why a run stopped (spec Section 4.1).
type StopReason string

// Stop reasons.
const (
	StopConverged       StopReason = "converged"
	StopQuiet           StopReason = "quiet"
	StopBudgetExhausted StopReason = "budget_exhausted"
	StopTriggerResolved StopReason = "trigger_resolved"
	// StopCut: the day's spend reached the daily cap.
	StopCut StopReason = "cut"
	// StopNotStaffed: no agent to invite, or none left.
	StopNotStaffed StopReason = "not_staffed"
)

// Next says what follows a round.
type Next string

// What follows a round.
const (
	NextRound  Next = "round"
	NextWrapUp Next = "wrap_up"
	NextStop   Next = "stop"
)

// Decision is what the coordinator decided when a round ended.
type Decision struct {
	Next   Next          `json:"next"`
	WrapUp board.EntryID `json:"wrapUp,omitempty"`
	Stop   StopReason    `json:"stop,omitempty"`
}

// RoundEnded records how a round ended and what follows it. The coordinator
// writes it before any effect of its decision.
type RoundEnded struct {
	Round   int     `json:"round"`
	EndedBy EndedBy `json:"endedBy"`
	// EndedAt is when the round ended, in database time: the last turn's
	// end, or the deadline.
	EndedAt time.Time `json:"endedAt"`
	// Turns holds how each invited agent's turn ended.
	Turns map[string]TurnClass `json:"turns"`
	// Spent and DaySpent are the run's spend and the day's, in
	// cost-weighted tokens, as read once the round's calls settled or the
	// wait for them ended; Settled says whether no call of the run was
	// still in flight.
	Spent    int64 `json:"spent"`
	Settled  bool  `json:"settled"`
	DaySpent int64 `json:"daySpent"`
	// Rounds and WallClock are the budget used so far, this round included:
	// normal rounds that did not end by an outage.
	Rounds    int           `json:"rounds"`
	WallClock time.Duration `json:"wallClock"`
	// Converged lists the hypotheses converged when the round ended, by
	// score; Entries counts the round's entries (board.RoundFacts).
	Converged []board.EntryID `json:"converged,omitempty"`
	Entries   int             `json:"entries"`
	Quiet     bool            `json:"quiet"`
	// Removed lists the agents removed after this round: two consecutive
	// crashed turns (spec Section 8.2).
	Removed []string `json:"removed,omitempty"`
	// Unseen says the instance that ended the round did not see its turns
	// (Ending.Unseen).
	Unseen   bool     `json:"unseen,omitempty"`
	Decision Decision `json:"decision"`
}

// RunStopped stops a run, with the budget it used and its conclusion.
type RunStopped struct {
	Reason     StopReason    `json:"reason"`
	Rounds     int           `json:"rounds"`
	WallClock  time.Duration `json:"wallClock"`
	Spent      int64         `json:"spent"`
	Settled    bool          `json:"settled"`
	Conclusion board.EntryID `json:"conclusion,omitempty"`
}

// AwaitingClose records that an item waits for a person to close it, with
// the item's reason (spec Section 4.1).
type AwaitingClose struct {
	Reason StopReason `json:"reason"`
}

// ItemReason is an item's stop reason given its primary run's: a run cut at
// the daily cap leaves the item not staffed (spec Section 4.1).
func ItemReason(run StopReason) StopReason {
	if run == StopCut {
		return StopNotStaffed
	}
	return run
}
