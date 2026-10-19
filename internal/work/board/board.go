// Package board implements a run's blackboard: typed, append-only entries with
// provenance, and the rules that turn them into stances, convergence and a
// conclusion (spec Sections 4.2 and 4.3).
package board

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// Errors returned by the board rules. Callers match them with errors.Is.
var (
	// ErrInvalid marks an entry that breaks a board rule.
	ErrInvalid = errors.New("board: invalid entry")
	// ErrNotFound marks a reference to an entry that is not on this board.
	ErrNotFound = errors.New("board: no such entry")
	// ErrNotAllowed marks an author who may not post this entry.
	ErrNotAllowed = errors.New("board: not allowed")
	// ErrProvenance marks evidence whose provenance does not check out.
	ErrProvenance = errors.New("board: provenance check failed")
	// ErrReasonRequired marks a human action that needs a preset reason.
	ErrReasonRequired = errors.New("board: a reason is required")
	// ErrLimit marks an agent's post beyond the round's entry limit, or a
	// claim beyond the open claims an agent may hold.
	ErrLimit = errors.New("board: limit reached")
	// ErrTurnEnded marks an agent's post after it ended its turn in the round.
	ErrTurnEnded = errors.New("board: the turn has ended")
	// ErrWrapUp marks an agent's post that the wrap-up round does not allow.
	ErrWrapUp = errors.New("board: not allowed in the wrap-up round")
	// ErrHeld marks a claim on a task that another agent holds.
	ErrHeld = errors.New("board: the task is held")
	// ErrRoundEnded marks an agent's post that arrives after the round it was
	// made in ended.
	ErrRoundEnded = errors.New("board: the round ended")
	// ErrStopped marks an entry other than a ruling on the board of a run
	// that stopped.
	ErrStopped = errors.New("board: the run has stopped")
)

// Size limits that keep entries readable and bound flooding. The spec sets
// none; these are the implementation's choices.
const (
	MaxClaimRunes     = 2000
	MaxQuoteRunes     = 2000
	MaxStatementRunes = 1000
	MaxReasonRunes    = 500
	MaxCitations      = 20
	MaxTitleRunes     = 200
	MaxTags           = 8
	MaxSummaryRunes   = 1000
	// MaxFactValues caps the values a proposal keeps of one fact, and
	// MaxValueRunes each value, written as JSON.
	MaxFactValues = 5
	MaxValueRunes = 200
)

// RunID identifies a run. Each run has its own board.
type RunID string

// EntryID identifies one board entry.
type EntryID string

// AuthorKind says who wrote an entry.
type AuthorKind string

// Author kinds.
const (
	AuthorAgent    AuthorKind = "agent"
	AuthorHuman    AuthorKind = "human"
	AuthorPlatform AuthorKind = "platform"
)

// Author is an entry's writer: an agent's name, a human's sign-in subject, or
// the platform.
type Author struct {
	Kind AuthorKind `json:"kind"`
	Name string     `json:"name"`
}

// IsContributor reports whether the author can hold a stance. Agents and
// humans can; the platform cannot.
func (a Author) IsContributor() bool {
	return a.Kind == AuthorAgent || a.Kind == AuthorHuman
}

// Kind is an entry's type.
type Kind string

// Entry kinds.
const (
	KindEvidence   Kind = "evidence"
	KindHypothesis Kind = "hypothesis"
	KindRetraction Kind = "retraction"
	KindRuling     Kind = "ruling"
	KindTask       Kind = "task"
	KindClaim      Kind = "claim"
	KindCompletion Kind = "completion"
	KindTurnEnd    Kind = "turn_end"
	KindProposal   Kind = "proposal"
	KindNotice     Kind = "notice"
	KindRound      Kind = "round"
	KindStop       Kind = "stop"
)

// Stance is a contributor's position on a hypothesis.
type Stance string

// Stances.
const (
	Supports Stance = "supports"
	Refutes  Stance = "refutes"
)

// Source is where evidence comes from: a (server, tool) pair, human
// observation, or an inbound trigger source.
type Source string

// HumanObservation is the one source of all human evidence.
const HumanObservation Source = "human-observation"

// ToolSource names a (server, tool) pair as a source.
func ToolSource(server, tool string) Source {
	return Source(server + "/" + tool)
}

// ProvenanceKind says what backs an evidence entry.
type ProvenanceKind string

// Provenance kinds (spec Section 4.2).
const (
	FromReadCall ProvenanceKind = "read_call"
	FromHuman    ProvenanceKind = "human"
	FromPlatform ProvenanceKind = "platform"
)

// Provenance records what backs an evidence entry, and its source.
type Provenance struct {
	Kind   ProvenanceKind `json:"kind"`
	CallID string         `json:"callId,omitempty"`
	Quote  string         `json:"quote,omitempty"`
	// Ref names the trigger event or platform read behind a platform record.
	Ref    string `json:"ref,omitempty"`
	Source Source `json:"source"`
}

// Reason is a preset reason code with optional free text.
type Reason struct {
	Code string `json:"code"`
	Text string `json:"text,omitempty"`
}

// Evidence is a claim with provenance and, optionally, a stance on a hypothesis.
type Evidence struct {
	Claim      string     `json:"claim"`
	Provenance Provenance `json:"provenance"`
	Hypothesis EntryID    `json:"hypothesis,omitempty"`
	Stance     Stance     `json:"stance,omitempty"`
	// Dismisses lists the agent refutations that this human support lifted.
	// The support counts as their dismissal (spec Section 4.2).
	Dismisses []EntryID `json:"dismisses,omitempty"`
	Reason    *Reason   `json:"reason,omitempty"`
}

// Hypothesis is a keyed claim, grounded in evidence.
type Hypothesis struct {
	Key       key.Key   `json:"key"`
	Statement string    `json:"statement"`
	Evidence  []EntryID `json:"evidence"`
	// AliasOf names the earlier hypothesis with an equal key that this one
	// merged into. Empty for a hypothesis that merged into nothing.
	AliasOf EntryID `json:"aliasOf,omitempty"`
}

// Retraction withdraws the author's own earlier evidence.
type Retraction struct {
	Evidence EntryID `json:"evidence"`
}

// RulingKind is a human ruling that affects convergence.
type RulingKind string

// Ruling kinds. The other rulings of spec Section 4.2 act on work items and
// arrive with the coordinator.
const (
	RulingReject   RulingKind = "reject"
	RulingUnreject RulingKind = "unreject"
	RulingDismiss  RulingKind = "dismiss_refutation"
)

// Ruling is a human decision about a hypothesis or a refutation.
type Ruling struct {
	Kind       RulingKind `json:"kind"`
	Hypothesis EntryID    `json:"hypothesis,omitempty"`
	Refutation EntryID    `json:"refutation,omitempty"`
	Reason     *Reason    `json:"reason,omitempty"`
}

// Task is work for any agent to take: a title and tags (spec Section 4.2).
type Task struct {
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
}

// Claim takes a task, so that one agent at a time works on it.
type Claim struct {
	Task EntryID `json:"task"`
}

// Completion ends a task the author holds, citing the evidence that answers it.
type Completion struct {
	Task     EntryID   `json:"task"`
	Evidence []EntryID `json:"evidence"`
}

// TurnEnd records that an agent ended its turn in the entry's round.
type TurnEnd struct{}

// RoundStart opens a round (spec Section 5.2). The coordinator writes it
// when it freezes the round's snapshot, which holds every entry before it.
// WrapUp names the converged hypothesis when the round is the wrap-up round
// (spec Section 5.6).
type RoundStart struct {
	Round  int     `json:"round"`
	WrapUp EntryID `json:"wrapUp,omitempty"`
}

// Stop ends a run's board (spec Section 5.6): the coordinator's summary of
// how the run ended, in codes, numbers and ids, never text an agent wrote.
// After it the board takes rulings only.
type Stop struct {
	// Reason is the run's stop reason, as a code.
	Reason     string  `json:"reason"`
	Conclusion EntryID `json:"conclusion,omitempty"`
	// Hypotheses are the board's hypotheses by score, OpenTasks the tasks
	// no one completed and Proposals the proposals made, each at most
	// MaxStopListed; Omitted counts the ids left out.
	Hypotheses []EntryID `json:"hypotheses"`
	OpenTasks  []EntryID `json:"openTasks"`
	Proposals  []EntryID `json:"proposals"`
	Omitted    int       `json:"omitted,omitempty"`
	// Rounds, WallClock and Tokens are the budget the run used.
	Rounds    int           `json:"rounds"`
	WallClock time.Duration `json:"wallClock"`
	Tokens    int64         `json:"tokens"`
}

// MaxStopListed caps each list of ids a stop entry holds.
const MaxStopListed = 50

// StopInput is what the coordinator says of a run it stops; the board adds
// what it holds.
type StopInput struct {
	Reason string
	// Conclusion is the hypothesis the coordinator recorded as the run's
	// conclusion when it decided to stop; empty when it recorded none.
	Conclusion EntryID
	Rounds     int
	WallClock  time.Duration
	Tokens     int64
}

// PlatformEvidenceInput is evidence the platform posts: a claim it composed
// by rule, the record behind it and its source (spec Section 4.2).
type PlatformEvidenceInput struct {
	Claim  string
	Ref    string
	Source Source
}

// Proposal is a write call the gateway recorded for a person and never sent
// (spec Sections 4.4, 5.3 and 6.3). The gateway writes it; Proposer names the
// agent that made the call. Values that came from servers are stored as JSON
// text, so that storage never rewrites them.
type Proposal struct {
	Proposer string `json:"proposer"`
	// Tool is the write tool, as server.tool, and SchemaHash its pinned hash.
	Tool       string `json:"tool"`
	SchemaHash string `json:"schemaHash"`
	// ArgsHash is the SHA-256 of the call's arguments without _hypothesis,
	// redacted, as sha256:<hex>. The write call's audit record keeps them.
	ArgsHash string `json:"argsHash"`
	// Hypothesis is the hypothesis the call cites, as cited.
	Hypothesis EntryID `json:"hypothesis"`
	// Target holds each target argument and the candidate it matched, in the
	// order tool policy lists them.
	Target []TargetValue `json:"target"`
	// Scope holds each scoped argument and the value its binding gave.
	Scope map[string]string `json:"scope,omitempty"`
	// Summary is the tool's summary, rendered by rules: the policy's text,
	// and the values filled in, which screens show as data.
	Summary []SummaryPart `json:"summary"`
	// Facts holds each fact the tool's policy names.
	Facts map[string]Fact `json:"facts,omitempty"`
	// Candidates holds each candidate argument's read: its call id.
	Candidates map[string]string `json:"candidates,omitempty"`
	// CallID is the write call's id in the audit store.
	CallID string `json:"callId"`
}

// SummaryPart is a piece of a proposal's summary: text from the tool's
// policy, or a value filled in, which a screen shows as data, never as the
// platform's words.
type SummaryPart struct {
	Text  string `json:"text"`
	Value bool   `json:"value,omitempty"`
}

// SummaryText joins a summary's parts into plain text.
func SummaryText(parts []SummaryPart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// TargetValue is one target argument and its value.
type TargetValue struct {
	Arg   string `json:"arg"`
	Value string `json:"value"`
}

// Fact is a value the platform read for a proposal: the first values its
// field selected, each as JSON text, and how many more there were; or why it
// is unavailable.
type Fact struct {
	Values      []string `json:"values,omitempty"`
	More        int      `json:"more,omitempty"`
	CallID      string   `json:"callId,omitempty"`
	Unavailable string   `json:"unavailable,omitempty"`
}

// NoticeCode says what a notice reports.
type NoticeCode string

// Notice codes.
const (
	// NoticeRefusedCall reports a call the gateway refused.
	NoticeRefusedCall NoticeCode = "refused_call"
	// NoticeFailedRead reports a read that ended in a read error.
	NoticeFailedRead NoticeCode = "failed_read"
	// NoticeModelRefused, NoticeModelFailed and NoticeModelUnknown report a
	// model call the LLM gateway refused, one that failed, and one whose
	// outcome is unknown.
	NoticeModelRefused NoticeCode = "model_refused"
	NoticeModelFailed  NoticeCode = "model_failed"
	NoticeModelUnknown NoticeCode = "model_unknown"
	// NoticeNoAgents reports a run left with no agent to invite.
	NoticeNoAgents NoticeCode = "no_agents"
)

// modelNotice reports whether a notice reports a model call, which it
// names: each call has its own notice.
func modelNotice(c NoticeCode) bool {
	return c == NoticeModelRefused || c == NoticeModelFailed || c == NoticeModelUnknown
}

// Notice is the platform's report of a failure on the board (spec Sections
// 4.2 and 8.1). It holds names, ids and codes only, never text an agent or a
// server wrote: people read it as a platform fact. A no_agents notice names
// nothing.
type Notice struct {
	Code  NoticeCode `json:"code"`
	Agent string     `json:"agent,omitempty"`
	// Tool is the name the agent called, when it is a tool of the agent's
	// list; empty otherwise.
	Tool string `json:"tool,omitempty"`
	// CallID names the call's record in the audit store, if it has one.
	CallID string `json:"callId,omitempty"`
	// Rule names the board rule that refused a board tool's call.
	Rule string `json:"rule,omitempty"`
}

// Body holds an entry's content. Exactly one field is set.
type Body struct {
	Evidence   *Evidence   `json:"evidence,omitempty"`
	Hypothesis *Hypothesis `json:"hypothesis,omitempty"`
	Retraction *Retraction `json:"retraction,omitempty"`
	Ruling     *Ruling     `json:"ruling,omitempty"`
	Task       *Task       `json:"task,omitempty"`
	Claim      *Claim      `json:"claim,omitempty"`
	Completion *Completion `json:"completion,omitempty"`
	TurnEnd    *TurnEnd    `json:"turnEnd,omitempty"`
	Proposal   *Proposal   `json:"proposal,omitempty"`
	Notice     *Notice     `json:"notice,omitempty"`
	Round      *RoundStart `json:"round,omitempty"`
	Stop       *Stop       `json:"stop,omitempty"`
}

// Kind returns the kind of entry the body holds, or "" when it holds none or
// more than one.
func (b *Body) Kind() Kind {
	var kind Kind
	n := 0
	for _, f := range []struct {
		set  bool
		kind Kind
	}{
		{b.Evidence != nil, KindEvidence}, {b.Hypothesis != nil, KindHypothesis},
		{b.Retraction != nil, KindRetraction}, {b.Ruling != nil, KindRuling},
		{b.Task != nil, KindTask}, {b.Claim != nil, KindClaim},
		{b.Completion != nil, KindCompletion}, {b.TurnEnd != nil, KindTurnEnd},
		{b.Proposal != nil, KindProposal}, {b.Notice != nil, KindNotice},
		{b.Round != nil, KindRound}, {b.Stop != nil, KindStop},
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

// Event is one committed board entry. Seq orders a run's entries, and At is
// the database time of the commit; both only grow within a run.
type Event struct {
	Seq    int64     `json:"seq"`
	ID     EntryID   `json:"id"`
	Run    RunID     `json:"run"`
	Round  int       `json:"round"`
	Author Author    `json:"author"`
	At     time.Time `json:"at"`
	Body   Body      `json:"body"`
}

// Draft is an entry that passed the rules but is not committed yet. The store
// assigns its Seq and At.
type Draft struct {
	ID     EntryID
	Run    RunID
	Round  int
	Author Author
	Body   Body
}

// Convergence holds a work item type's convergence settings (spec Section 3.5).
type Convergence struct {
	MinContributors  int `json:"minContributors"`
	MinSources       int `json:"minSources"`
	RefutationMargin int `json:"refutationMargin"`
}

// DefaultConvergence is the spec's default: 2 contributors, 2 sources and a
// refutation margin of 2 (spec Section 15).
var DefaultConvergence = Convergence{MinContributors: 2, MinSources: 2, RefutationMargin: 2}

// check refuses settings under which a hypothesis could converge without any
// support: convergence needs at least one contributor and one source, and the
// refutation margin cannot be negative.
func (c Convergence) check() error {
	if c.MinContributors < 1 || c.MinSources < 1 || c.RefutationMargin < 0 {
		return fmt.Errorf("%w: convergence settings need at least 1 contributor, 1 source and a margin of 0, got %d, %d and %d",
			ErrInvalid, c.MinContributors, c.MinSources, c.RefutationMargin)
	}
	return nil
}

// Limits are a run's board limits (spec Section 4.2). A zero field takes the
// spec's default.
type Limits struct {
	// EntriesPerRound caps the evidence, hypotheses, tasks and proposals an
	// agent posts in one round.
	EntriesPerRound int `json:"entriesPerRound,omitempty"`
	// OpenClaims caps the tasks an agent holds at once.
	OpenClaims int `json:"openClaims,omitempty"`
	// LeaseRounds is how many rounds a claim holds, the round of the claim
	// included.
	LeaseRounds int `json:"leaseRounds,omitempty"`
}

// DefaultLimits are the spec's defaults: 5 entries per round, 2 open claims,
// and a lease of 2 rounds (spec Section 15).
var DefaultLimits = Limits{EntriesPerRound: 5, OpenClaims: 2, LeaseRounds: 2}

// orDefault fills each unset limit with its default.
func (l Limits) orDefault() Limits {
	if l.EntriesPerRound == 0 {
		l.EntriesPerRound = DefaultLimits.EntriesPerRound
	}
	if l.OpenClaims == 0 {
		l.OpenClaims = DefaultLimits.OpenClaims
	}
	if l.LeaseRounds == 0 {
		l.LeaseRounds = DefaultLimits.LeaseRounds
	}
	return l
}

// check refuses negative limits.
func (l Limits) check() error {
	if l.EntriesPerRound < 0 || l.OpenClaims < 0 || l.LeaseRounds < 0 {
		return fmt.Errorf("%w: board limits cannot be negative, got %d, %d and %d",
			ErrInvalid, l.EntriesPerRound, l.OpenClaims, l.LeaseRounds)
	}
	return nil
}

// Settings are a run's board settings, locked from its work item type.
type Settings struct {
	Schema      key.Schema  `json:"schema"`
	Convergence Convergence `json:"convergence"`
	Limits      Limits      `json:"limits,omitzero"`
}

// Context says where and when an entry is posted. Round, Snapshot and WrapUp
// come from the board's latest round entry; a board without round entries,
// as one without a coordinator, takes them from the run.
type Context struct {
	Run   RunID
	Round int
	// HumanEntries is true only for the visible arm's primary run. Shadow-mode
	// runs receive no human entries (spec Section 7).
	HumanEntries bool
	Settings     Settings
	// Snapshot is the sequence number of the last entry in the round's
	// snapshot, which the coordinator freezes when the round starts; agents
	// see other authors' entries up to it (spec Section 5.2).
	Snapshot int64
	// WrapUp names the converged hypothesis during the run's wrap-up round,
	// and is empty in every other round (spec Section 5.6).
	WrapUp EntryID
}

// ReadCall is a gateway read call as the audit store keeps it.
type ReadCall struct {
	ID string
	// Run and Agent identify who made the call. Agent is empty for platform reads.
	Run    RunID
	Agent  string
	Server string
	Tool   string
	// Failed is true when the call ended in a read error, which cannot be cited.
	Failed bool
	// Args are the call's stored, redacted arguments, as JSON.
	Args []byte
	// Output is the stored, redacted output.
	Output []byte
}

// EvidenceInput is what an author submits to post evidence.
type EvidenceInput struct {
	Claim string
	// CallID and Quote are an agent's provenance: one of its own read calls in
	// this run, and text that appears verbatim in that call's output.
	CallID string
	Quote  string
	// Hypothesis and Stance are optional, and go together.
	Hypothesis EntryID
	Stance     Stance
	// Reason is needed when a human's support would lift a standing refutation.
	Reason *Reason
}

// HypothesisInput is what an agent submits to post a hypothesis.
type HypothesisInput struct {
	Key       key.Key
	Statement string
	Evidence  []EntryID
}

// TaskInput is what an agent submits to post a task.
type TaskInput struct {
	Title string
	Tags  []string
}

// CompletionInput is what an agent submits to complete a task it holds.
type CompletionInput struct {
	Task     EntryID
	Evidence []EntryID
}

// RulingInput is what a human submits to rule on a hypothesis or a refutation.
type RulingInput struct {
	Kind       RulingKind
	Hypothesis EntryID
	Refutation EntryID
	Reason     *Reason
}
