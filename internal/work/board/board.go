// Package board implements a run's blackboard: typed, append-only entries with
// provenance, and the rules that turn them into stances, convergence and a
// conclusion (spec Sections 4.2 and 4.3).
package board

import (
	"errors"
	"fmt"
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
)

// Size limits that keep entries readable and bound flooding. The spec sets
// none; these are the implementation's choices.
const (
	MaxClaimRunes     = 2000
	MaxQuoteRunes     = 2000
	MaxStatementRunes = 1000
	MaxReasonRunes    = 500
	MaxCitations      = 20
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

// Body holds an entry's content. Exactly one field is set.
type Body struct {
	Evidence   *Evidence   `json:"evidence,omitempty"`
	Hypothesis *Hypothesis `json:"hypothesis,omitempty"`
	Retraction *Retraction `json:"retraction,omitempty"`
	Ruling     *Ruling     `json:"ruling,omitempty"`
}

// Kind returns the kind of entry the body holds, or "" when it holds none or
// more than one.
func (b *Body) Kind() Kind {
	var kind Kind
	n := 0
	if b.Evidence != nil {
		kind, n = KindEvidence, n+1
	}
	if b.Hypothesis != nil {
		kind, n = KindHypothesis, n+1
	}
	if b.Retraction != nil {
		kind, n = KindRetraction, n+1
	}
	if b.Ruling != nil {
		kind, n = KindRuling, n+1
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

// Settings are a run's board settings, locked from its work item type.
type Settings struct {
	Schema      key.Schema  `json:"schema"`
	Convergence Convergence `json:"convergence"`
}

// Context says where and when an entry is posted.
type Context struct {
	Run   RunID
	Round int
	// HumanEntries is true only for the visible arm's primary run. Shadow-mode
	// runs receive no human entries (spec Section 7).
	HumanEntries bool
	Settings     Settings
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

// RulingInput is what a human submits to rule on a hypothesis or a refutation.
type RulingInput struct {
	Kind       RulingKind
	Hypothesis EntryID
	Refutation EntryID
	Reason     *Reason
}
