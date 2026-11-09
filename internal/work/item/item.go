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
	// ErrNotDue marks an automatic close that a records store would stamp
	// before its due: the store refuses it, and writes nothing (R41).
	ErrNotDue = errors.New("item: an automatic close before its due")
)

// Version is the version of the record bodies this code writes and reads.
const Version = 1

// ID identifies a work item, and IncidentID its incident.
type (
	ID         string
	IncidentID string
)

// TriggerStatus is the status a trigger event reports of its instance.
type TriggerStatus string

// Trigger statuses.
const (
	Firing   TriggerStatus = "firing"
	Resolved TriggerStatus = "resolved"
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
	KindPromoted      Kind = "item_promoted"
	KindClosed        Kind = "item_closed"
	KindReopened      Kind = "item_reopened"
	KindExtended      Kind = "run_extended"
	KindLinked        Kind = "item_linked"
	KindUnlinked      Kind = "item_unlinked"
	KindTriggered     Kind = "item_triggered"
)

// Record is one committed work record. Seq numbers the item's records from 1
// in commit order: it counts within an item, so it cannot order the records
// of different items. At is the database time of the commit.
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
	Promoted      *Promoted      `json:"promoted,omitempty"`
	Closed        *Closed        `json:"closed,omitempty"`
	Reopened      *Reopened      `json:"reopened,omitempty"`
	Extended      *Extended      `json:"extended,omitempty"`
	Linked        *Linked        `json:"linked,omitempty"`
	Unlinked      *Unlinked      `json:"unlinked,omitempty"`
	Triggered     *Triggered     `json:"triggered,omitempty"`
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
		{b.Promoted != nil, KindPromoted}, {b.Closed != nil, KindClosed}, {b.Reopened != nil, KindReopened},
		{b.Extended != nil, KindExtended}, {b.Linked != nil, KindLinked}, {b.Unlinked != nil, KindUnlinked},
		{b.Triggered != nil, KindTriggered},
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
	// request from another (Decision 14). It is not Started.Digest, the
	// digest of the revision the item locked, though both are "digest" in
	// JSON. Bodies of version 1 hold it, as no release was tagged before it
	// came (R12).
	Digest string `json:"digest,omitempty"`
	// KillSwitchSeen is the latest kill switch row the coordinator knew when
	// it recorded the opening, 0 before the first (Decision 5).
	KillSwitchSeen int64 `json:"killSwitchSeen,omitempty"`
	// By is the subject of who opened the item (R78), empty in a body of an
	// earlier build, which folds as before.
	By string `json:"by,omitempty"`
	// Trigger is an inbound opening's trigger, nil for a person's opening.
	// The opening's applied firing item_triggered follows it as the item's
	// second record (design D5, R126).
	Trigger *Trigger `json:"trigger,omitempty"`
}

// Trigger is an inbound opening's trigger (design D5): its source, and the
// dedup key and correlation key of the event that opened the item.
type Trigger struct {
	Source         string `json:"source"`
	DedupKey       string `json:"dedupKey"`
	CorrelationKey string `json:"correlationKey,omitempty"`
}

// Triggered is an inbound event appended to an item (design D5): trigger
// evidence, applied or late.
type Triggered struct {
	Source         string        `json:"source"`
	EventID        string        `json:"eventId"`
	InstanceID     string        `json:"instanceId"`
	DedupKey       string        `json:"dedupKey"`
	CorrelationKey string        `json:"correlationKey,omitempty"`
	Status         TriggerStatus `json:"status"`
	// OccurredAt is when the event says its status began, clamped to the
	// instance's clock (design D3).
	OccurredAt time.Time `json:"occurredAt"`
	// Applied says the event was later than its instance's last status, and
	// so changed it; a late event is evidence alone (design D7 step 3).
	Applied bool `json:"applied"`
	// Resume is "", "resumed" or "declined:<code>" (design D7): what came of
	// the firing resume an applied firing tried while its item awaited its
	// close.
	Resume string `json:"resume,omitempty"`
}

// Resumes of a firing (design D7). A declined resume's Resume is
// ResumeDeclined followed by its code.
const (
	ResumeResumed  = "resumed"
	ResumeDeclined = "declined:"
)

// Started locks the configuration an item runs under (spec Section 3.4): the
// revision, and the runtime images its agents start from.
type Started struct {
	Commit string `json:"commit"`
	// Digest is the digest of the revision the item locked (config.Lock). It
	// is not Opened.Digest, the digest of the request that opened the item,
	// though both are "digest" in JSON.
	Digest string            `json:"digest"`
	Images map[string]string `json:"images"`
	// KillSwitchSeen is the latest kill switch row the coordinator knew when
	// it recorded the start, 0 before the first (Decision 5).
	KillSwitchSeen int64 `json:"killSwitchSeen,omitempty"`
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
	// KillSwitchSeen is the latest kill switch row the coordinator knew when
	// it created the run, 0 before the first: a later on-row is a kill the
	// run owes (Decision 5).
	KillSwitchSeen int64 `json:"killSwitchSeen,omitempty"`
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
	// EndedByStopped: the run stopped, killed or closed, while the round was
	// open (Decision 3; see Run.Halt). The round is never quiet, and decides
	// the run's stop.
	EndedByStopped EndedBy = "stopped"
)

// TurnClass says how an agent's turn ended.
type TurnClass string

// Turn classes. Whether a round is quiet, and whether an agent took part in
// it, rest on the turn ends the board recorded, not on the classes, which may
// disagree with them (Decision 9).
const (
	// TurnEnded: the agent's end_turn call succeeded (Decision 13).
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
	// TurnUnavailable: the platform could not serve the turn: a gateway
	// answered it unavailable, or not at all, or the turn's spec could not
	// be delivered (Decisions 12 and 13). It is not a crash, and never counts
	// toward an agent's removal; a round whose every turn ended unavailable
	// or at the model vendor waits for its time limit.
	TurnUnavailable TurnClass = "unavailable"
	// TurnCrashed: the sandbox or the runtime failed. Only crashes count
	// toward an agent's removal (spec Section 8.2).
	TurnCrashed TurnClass = "crashed"
	// TurnUnstarted: the turn did not start in time to count. The
	// coordinator could not start it before the round's deadline, as when
	// the runner could not be reached, or started it with less than a third
	// of the round's span left and it still ran at the deadline, or was
	// reported interrupted; so does a turn reported interrupted whose start
	// the coordinator never learned of. Such a turn leaves the agent's count
	// of crashes as it was.
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
	// StopKilled: the kill switch stopped the run (Decision 5).
	StopKilled StopReason = "killed"
	// StopClosed: a person closed the item while its run ran (Decision 6).
	StopClosed StopReason = "closed"
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

// RoundEnded records how a round ended and what follows it, with the
// evidence its decision read (Decision 9). The coordinator writes it before
// any effect of its decision. Bodies of version 1 hold the evidence, as no
// release was tagged before it came (R12).
type RoundEnded struct {
	Round   int     `json:"round"`
	EndedBy EndedBy `json:"endedBy"`
	// EndedAt is when the round ended, in database time: the last turn's
	// end, or the deadline.
	EndedAt time.Time `json:"endedAt"`
	// Turns holds how each invited agent's turn ended.
	Turns map[string]TurnClass `json:"turns"`
	// TurnEnds lists, in the round's order, the agents of the round whose own
	// turn end the board recorded in it: their end_turn calls that succeeded.
	// The classes in Turns may disagree with them, and both are kept.
	TurnEnds []string `json:"turnEnds"`
	// Spent and DaySpent are the run's spend and the day's, in
	// cost-weighted tokens, as read once the round's model calls settled or
	// the wait for them ended; Settled says whether no model call of the
	// round was still in flight then.
	Spent    int64 `json:"spent"`
	Settled  bool  `json:"settled"`
	DaySpent int64 `json:"daySpent"`
	// FailedCalls counts the model calls noticed in the round as refused,
	// failed or of unknown outcome (Ending.FailedCalls). FailedToolCalls
	// counts the round's failed tool calls, as the MCP gateway's records
	// counted them (Decision 20).
	FailedCalls     int `json:"failedCalls"`
	FailedToolCalls int `json:"failedToolCalls"`
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
	Reason    StopReason    `json:"reason"`
	Rounds    int           `json:"rounds"`
	WallClock time.Duration `json:"wallClock"`
	// Spent and Settled are the last round's (RoundEnded): Settled says
	// whether that round's model calls settled. A call of an earlier round
	// may still be in flight, counted at its estimate (Decision 9).
	Spent      int64         `json:"spent"`
	Settled    bool          `json:"settled"`
	Conclusion board.EntryID `json:"conclusion,omitempty"`
	// Killed is, for a run the kill switch stopped, the number of the row
	// that turned it on.
	Killed int64 `json:"killed,omitempty"`
}

// AwaitingClose records that an item waits for its close, with the item's
// reason (spec Section 4.1): by a person, or by itself after its delay
// (Decision 8).
type AwaitingClose struct {
	Reason StopReason `json:"reason"`
	// AutoClose is the item's auto-close delay as its work item type set it
	// when the record was written, or zero when the writer could not read it,
	// as in a record written before 7b-1 (Decision 8).
	AutoClose time.Duration `json:"autoClose,omitempty"`
	// Cause says why an item with no run is not staffed.
	Cause Cause `json:"cause,omitempty"`
	// UnstaffedBy is, for an item the kill switch left not staffed, the
	// number of the row that turned it on.
	UnstaffedBy int64 `json:"unstaffedBy,omitempty"`
}

// Cause says why an item with no run is not staffed (Decision 4).
type Cause string

// Causes of an item not staffed without a run.
const (
	// CauseKillSwitch: the kill switch was on, or the item owed its latest
	// on-row (Decision 5).
	CauseKillSwitch Cause = "kill_switch"
	// CauseDailyCap: the day's spend reached the daily cap.
	CauseDailyCap Cause = "daily_cap"
	// CauseRouting: the item's routing failed its check under the revision its
	// start was about to lock.
	CauseRouting Cause = "routing"
	// CauseType: that revision cannot run the item's type.
	CauseType Cause = "type"
	// CauseClosed: a reopen returned an item with no run to awaiting its
	// close, and no cause stood before its close, as when it was closed while
	// queued (R12).
	CauseClosed Cause = "closed"
)

// Request is the request of a person's act on an item (Decision 13): the
// hash of its key and the digest of what it asked, each "sha256:<hex>". A
// record keeps the key's hash, never the key.
type Request struct {
	Key    string `json:"key"`
	Digest string `json:"digest"`
}

// Promoted promotes an item with no run (Decision 4): it starts the item,
// whatever the queue, or has the item that started get its run, and lifts
// the item's awaiting_close.
type Promoted struct {
	// By is who promoted the item: the person, or admin through the admin
	// API.
	By string `json:"by"`
	// KillSwitchSeen is the latest kill switch row the coordinator knew when
	// it recorded the promotion, 0 before the first (Decision 5).
	KillSwitchSeen int64 `json:"killSwitchSeen,omitempty"`
	// Reason is the person's reason, if given, its text redacted (Decision
	// 9; R13).
	Reason  *board.Reason `json:"reason,omitempty"`
	Request Request       `json:"request"`
}

// Closed closes an item (spec Section 4.1): a person's close, from any state
// (Decision 6), or its close by itself while it awaits its close (Decision
// 8).
type Closed struct {
	// By is who closed the item, empty for its close by itself.
	By string `json:"by,omitempty"`
	// Auto says the item closed by itself, and Due is when that close fell
	// due, beside the record's own time (R7).
	Auto bool      `json:"auto,omitempty"`
	Due  time.Time `json:"due,omitzero"`
	// Reason is the person's reason, if given, its text redacted (Decision
	// 9).
	Reason *board.Reason `json:"reason,omitempty"`
	// Request is a person's close's request, nil for a close by itself.
	Request *Request `json:"request,omitempty"`
}

// Reopened reopens a closed item (Decision 7), which awaits its close again
// as AwaitingClose says: its reason, its cause, and its auto-close delay.
type Reopened struct {
	// By is who reopened the item: the person, or admin through the admin
	// API.
	By string `json:"by"`
	// Reason is the person's reason, if given, its text redacted (Decision
	// 9).
	Reason        *board.Reason `json:"reason,omitempty"`
	Request       Request       `json:"request"`
	AwaitingClose AwaitingClose `json:"awaitingClose"`
}

// Amount is an extension as a person asks it (Decision 17): a dimension left
// out is nil, and one named as zero is a pointer to zero (R70), so that
// ExtensionAmount gives a dimension left out its share of the rounds asked.
type Amount struct {
	Rounds    *int
	WallClock *time.Duration
	Tokens    *int64
}

// ExtendCause says what extended a run: a person, "", or a trigger's firing.
type ExtendCause string

// Causes of an extension.
const (
	// ExtendFiring: a firing resume (design D7), a platform act that resumes
	// a stopped run with its remaining budget, an amount of zero.
	ExtendFiring ExtendCause = "firing"
)

// Extended extends a run's budget by an amount (Decision 17). On a run that
// runs, the budget rises; on a stopped run whose item awaits its close, it is
// a resume, told by the run's state alone, never by these fields (Decision
// 16): the run's stop moves to its history, and the item investigates again.
type Extended struct {
	// Amount is the extension as ExtensionAmount resolved it, zero for a
	// firing resume.
	Amount Budget `json:"amount"`
	// Cause is "" for a person's extension and ExtendFiring for a firing
	// resume (design D5).
	Cause ExtendCause `json:"cause,omitempty"`
	// KillSwitchSeen is, for a resume, the latest kill switch row the
	// coordinator knew when it recorded it, 0 when no row was ever written:
	// a later on-row is a kill the resumed run owes. An extension of a run
	// that runs records none (the plan review's I3).
	KillSwitchSeen int64 `json:"killSwitchSeen,omitempty"`
	// By is the subject of who extended the run (R101).
	By string `json:"by"`
	// Reason is the person's reason, if given, its text redacted (Decision
	// 9).
	Reason  *board.Reason `json:"reason,omitempty"`
	Request Request       `json:"request"`
}

// Linked moves an item into the incident another item, With, has now
// (Decision 18): the record's column holds the incident it joins, so that it
// is chained under its destination, and Left the one it left. The other
// item's records do not change.
type Linked struct {
	Left IncidentID `json:"left"`
	With ID         `json:"with"`
	// By is the subject of who linked the item (R101).
	By string `json:"by"`
	// Reason is the person's preset reason, its text, if any, redacted
	// (Decision 9).
	Reason  *board.Reason `json:"reason"`
	Request Request       `json:"request"`
}

// Unlinked gives an item a new incident of its own (Decision 18; R61): the
// record's column holds the new incident, and Left the one it left.
type Unlinked struct {
	Left IncidentID `json:"left"`
	// By is the subject of who unlinked the item (R101).
	By string `json:"by"`
	// Reason is the person's preset reason, its text, if any, redacted
	// (Decision 9).
	Reason  *board.Reason `json:"reason"`
	Request Request       `json:"request"`
}

// ItemReason is an item's stop reason given its primary run's: a run cut at
// the daily cap, or killed, leaves the item not staffed, and a run closed
// leaves it closed (spec Section 4.1).
func ItemReason(run StopReason) StopReason {
	if run == StopCut || run == StopKilled {
		return StopNotStaffed
	}
	return run
}
