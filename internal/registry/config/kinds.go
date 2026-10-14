// Package config is the Registry context's domain: the kinds of the
// configuration repository, the rules a revision must keep to be loaded, and
// the validated revision that work items lock (spec Section 3; ADR-014).
package config

import (
	"fmt"
	"regexp"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/jchen0426/agent-swarm/internal/registry/selector"
)

// Kind names an object's type.
type Kind string

// The kinds of ADR-014, plus redaction rules, which tool policies and trigger
// mappings name, and the yank list.
const (
	KindAgent          Kind = "Agent"
	KindSkill          Kind = "Skill"
	KindMcpServer      Kind = "McpServer"
	KindWorkItemType   Kind = "WorkItemType"
	KindTriggerMapping Kind = "TriggerMapping"
	KindIdentitySource Kind = "IdentitySource"
	KindRedactionRule  Kind = "RedactionRule"
	KindYankList       Kind = "YankList"
)

// Metadata names an object and its owner, who is notified of its changes.
type Metadata struct {
	Name  string `yaml:"name" json:"name"`
	Owner string `yaml:"owner" json:"owner"`
}

// origin records the file an object was read from and the file's content hash.
type origin struct {
	file string
	hash string
}

// File returns the path of the object's file in the repository.
func (o *origin) File() string { return o.file }

// Hash returns the SHA-256 of the object's file, as sha256:<hex>.
func (o *origin) Hash() string { return o.hash }

// Agent is an agent's configuration (spec Section 3.1).
type Agent struct {
	origin   `yaml:"-"`
	Kind     Kind      `yaml:"kind"`
	Metadata Metadata  `yaml:"metadata"`
	Spec     AgentSpec `yaml:"spec"`
	version  string
	tools    []GrantedTool
	skills   []SkillRef
}

// AgentSpec is what an agent runs, knows, may call and works on.
type AgentSpec struct {
	Runtime   string       `yaml:"runtime"`
	Model     string       `yaml:"model"`
	Skills    []string     `yaml:"skills"`
	Tools     []string     `yaml:"tools"`
	Subscribe Subscription `yaml:"subscribe"`
}

// Subscription says which items invite an agent, and which tasks let it join
// a run after Phase 0.
type Subscription struct {
	WorkItemTypes []string `yaml:"workItemTypes"`
	TaskTags      []string `yaml:"taskTags"`
}

// Skill is a skill and its released versions (spec Section 3.2). Each version's
// bundle sits in the repository at skills/<name>/<version>/.
type Skill struct {
	origin   `yaml:"-"`
	Kind     Kind      `yaml:"kind"`
	Metadata Metadata  `yaml:"metadata"`
	Spec     SkillSpec `yaml:"spec"`
}

// SkillSpec lists a skill's versions.
type SkillSpec struct {
	Versions []*SkillVersion `yaml:"versions"`
}

// SkillVersion is one released snapshot of a skill.
type SkillVersion struct {
	Version string      `yaml:"version"`
	Source  SkillSource `yaml:"source"`
	digest  string
	files   []string
}

// Digest returns the SHA-256 of the version's bundle: its file paths and
// contents, as sha256:<hex>.
func (v *SkillVersion) Digest() string { return v.digest }

// Files returns the bundle's file paths, relative to its directory, sorted.
func (v *SkillVersion) Files() []string { return v.files }

// SkillSource records where people edit a skill and which commit a version
// snapshots.
type SkillSource struct {
	Repository string `yaml:"repository"`
	Commit     string `yaml:"commit"`
	Path       string `yaml:"path"`
}

// McpServer is a server registration with its tool policy (spec Section 3.3).
type McpServer struct {
	origin   `yaml:"-"`
	Kind     Kind          `yaml:"kind"`
	Metadata Metadata      `yaml:"metadata"`
	Spec     McpServerSpec `yaml:"spec"`
}

// McpServerSpec is where a server runs and which of its tools are listed.
// Unlisted tools are denied.
type McpServerSpec struct {
	Endpoint      string                 `yaml:"endpoint"`
	Image         string                 `yaml:"image"`
	CredentialRef string                 `yaml:"credentialRef"`
	Tools         map[string]*ToolPolicy `yaml:"tools"`
}

// Effect says whether a tool reads or writes. The platform sets it; servers'
// own hints are never trusted.
type Effect string

// Effects.
const (
	EffectRead  Effect = "read"
	EffectWrite Effect = "write"
)

// Audience says who may call a tool: agents, or only the platform's own reads.
type Audience string

// Audiences.
const (
	AudienceAgents   Audience = "agents"
	AudiencePlatform Audience = "platform"
)

// ToolPolicy is one listed tool's policy.
type ToolPolicy struct {
	SchemaHash  string                   `yaml:"schemaHash"`
	Effect      Effect                   `yaml:"effect"`
	Audience    Audience                 `yaml:"audience"`
	Redact      []string                 `yaml:"redact"`
	PartialWhen string                   `yaml:"partialWhen"`
	Bounds      map[string]Bound         `yaml:"bounds"`
	Target      []string                 `yaml:"target"`
	Scope       map[string]string        `yaml:"scope"`
	Candidates  map[string]*PlatformRead `yaml:"candidates"`
	Summary     string                   `yaml:"summary"`
	Facts       map[string]*PlatformRead `yaml:"facts"`
}

// Bound caps one argument that drives a read's cost. Max is a positive
// number, or a positive duration such as 6h.
type Bound struct {
	Max string `yaml:"max"`
}

// PlatformRead is a read the platform makes under its own identity: for
// candidates, facts, key values, principals and responders. Tool is
// server.tool, or a tool of the same server. Field selects the values from
// the read's output.
type PlatformRead struct {
	Tool  string            `yaml:"tool"`
	Args  map[string]string `yaml:"args"`
	Field string            `yaml:"field"`
}

// WorkItemType is a work item type (spec Section 3.5). Unset settings take
// the defaults of spec Section 15.
type WorkItemType struct {
	origin   `yaml:"-"`
	Kind     Kind             `yaml:"kind"`
	Metadata Metadata         `yaml:"metadata"`
	Spec     WorkItemTypeSpec `yaml:"spec"`
	settings WorkSettings
}

// WorkItemTypeSpec is a work item type's file form. Pointers tell an unset
// setting from a zero one.
type WorkItemTypeSpec struct {
	HypothesisKey []KeyField    `yaml:"hypothesisKey"`
	Budget        BudgetSpec    `yaml:"budget"`
	Round         RoundSpec     `yaml:"round"`
	Team          TeamSpec      `yaml:"team"`
	Limits        LimitsSpec    `yaml:"limits"`
	Convergence   ConvergeSpec  `yaml:"convergence"`
	Milestones    []string      `yaml:"milestones"`
	AutoClose     AutoCloseSpec `yaml:"autoClose"`
	Measurement   MeasureSpec   `yaml:"measurement"`
	ShadowRuns    []ShadowRun   `yaml:"shadowRuns"`
}

// KeyField is one hypothesis key field and its closed value set.
type KeyField struct {
	Name   string    `yaml:"name"`
	Values KeyValues `yaml:"values"`
}

// KeyValues is a closed value set: a list given inline, or a platform read
// that produces it when an item opens (spec Section 4.3).
type KeyValues struct {
	List []string
	Read *PlatformRead
}

// UnmarshalYAML accepts a list or a read. The read's fields are checked as
// strictly as the rest of the file.
func (v *KeyValues) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.SequenceNode:
		list := []string{}
		if err := n.Decode(&list); err != nil {
			return err
		}
		v.List = list
		return nil
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if k := n.Content[i]; k.Value != "tool" && k.Value != "args" && k.Value != "field" {
				return fmt.Errorf("line %d: field %s not found in a read", k.Line, k.Value)
			}
		}
		v.Read = new(PlatformRead)
		return n.Decode(v.Read)
	default:
		return fmt.Errorf("line %d: values must be a list or a read", n.Line)
	}
}

// BudgetSpec is a run's budget: wall clock, cost-weighted tokens and rounds.
type BudgetSpec struct {
	WallClock *time.Duration `yaml:"wallClock"`
	Tokens    *int64         `yaml:"tokens"`
	Rounds    *int           `yaml:"rounds"`
}

// RoundSpec holds a round's time limit.
type RoundSpec struct {
	TimeLimit *time.Duration `yaml:"timeLimit"`
}

// TeamSpec holds the most agents a run invites.
type TeamSpec struct {
	MaxAgents *int `yaml:"maxAgents"`
}

// LimitsSpec holds the board limits of spec Section 4.2.
type LimitsSpec struct {
	EntriesPerRound *int `yaml:"entriesPerRound"`
	OpenClaims      *int `yaml:"openClaims"`
	LeaseRounds     *int `yaml:"leaseRounds"`
}

// ConvergeSpec holds the convergence settings of spec Section 4.3.
type ConvergeSpec struct {
	MinContributors  *int `yaml:"minContributors"`
	MinSources       *int `yaml:"minSources"`
	RefutationMargin *int `yaml:"refutationMargin"`
}

// AutoCloseSpec holds when an item in awaiting_close closes by itself.
type AutoCloseSpec struct {
	After *time.Duration `yaml:"after"`
}

// MeasureSpec holds the share of items in the invisible arm (ADR-011).
type MeasureSpec struct {
	InvisibleShare *float64 `yaml:"invisibleShare"`
}

// ShadowRun is a run that works an item in shadow mode beside its primary
// run (spec Section 7).
type ShadowRun struct {
	Name     string   `yaml:"name"`
	Arms     []string `yaml:"arms"`
	Override Override `yaml:"override"`
}

// Override changes a shadow run's settings from the primary run's: its
// budget, its round time limit, its agents or its runtime.
type Override struct {
	Budget  BudgetSpec `yaml:"budget"`
	Round   RoundSpec  `yaml:"round"`
	Agents  []string   `yaml:"agents"`
	Runtime string     `yaml:"runtime"`
}

// TriggerMapping turns one inbound source's requests into normalized events
// (spec Section 3.6).
type TriggerMapping struct {
	origin   `yaml:"-"`
	Kind     Kind               `yaml:"kind"`
	Metadata Metadata           `yaml:"metadata"`
	Spec     TriggerMappingSpec `yaml:"spec"`
}

// TriggerMappingSpec is a trigger mapping. Templates fill holes from the
// fields of one event, after the split.
type TriggerMappingSpec struct {
	Source            string             `yaml:"source"`
	Authentication    Authentication     `yaml:"authentication"`
	Split             string             `yaml:"split"`
	TrustedFields     []string           `yaml:"trustedFields"`
	EventID           string             `yaml:"eventId"`
	Status            StatusMapping      `yaml:"status"`
	OccurredAt        OccurredAt         `yaml:"occurredAt"`
	Title             string             `yaml:"title"`
	WorkItemType      string             `yaml:"workItemType"`
	DedupKey          string             `yaml:"dedupKey"`
	InstanceID        string             `yaml:"instanceId"`
	Correlate         *Correlation       `yaml:"correlate"`
	Principal         *PlatformRead      `yaml:"principal"`
	Responders        *PlatformRead      `yaml:"responders"`
	FallbackPrincipal *FallbackPrincipal `yaml:"fallbackPrincipal"`
	Redact            []string           `yaml:"redact"`
	Checks            []string           `yaml:"checks"`
}

// Authentication is how a source proves its requests: a body signature with
// a signed timestamp, or a bearer token (spec Section 3.6).
type Authentication struct {
	HMACSecretRef   string         `yaml:"hmacSecretRef"`
	MaxAge          *time.Duration `yaml:"maxAge"`
	BearerSecretRef string         `yaml:"bearerSecretRef"`
}

// StatusMapping maps the source's status values to firing and resolved.
type StatusMapping struct {
	Value    string   `yaml:"value"`
	Firing   []string `yaml:"firing"`
	Resolved []string `yaml:"resolved"`
}

// OccurredAt names the event's time: its start when firing, its end when
// resolved.
type OccurredAt struct {
	Firing   string `yaml:"firing"`
	Resolved string `yaml:"resolved"`
}

// Correlation joins an event to an item opened within a window with the same
// key.
type Correlation struct {
	Key    string         `yaml:"key"`
	Within *time.Duration `yaml:"within"`
}

// FallbackPrincipal names the group whose current on-call person is the
// principal when the principal cannot be resolved (spec Section 5.1).
type FallbackPrincipal struct {
	Group string `yaml:"group"`
}

// IdentitySource registers a read tool whose output confers authority (spec
// Section 3.6).
type IdentitySource struct {
	origin   `yaml:"-"`
	Kind     Kind               `yaml:"kind"`
	Metadata Metadata           `yaml:"metadata"`
	Spec     IdentitySourceSpec `yaml:"spec"`
}

// IdentitySourceSpec names the tool.
type IdentitySourceSpec struct {
	Tool string `yaml:"tool"`
}

// RedactionRule is a named redaction rule (spec Section 9.4): regular
// expressions whose matches are replaced, and JSONPath selectors whose values
// are replaced whole.
type RedactionRule struct {
	origin   `yaml:"-"`
	Kind     Kind              `yaml:"kind"`
	Metadata Metadata          `yaml:"metadata"`
	Spec     RedactionRuleSpec `yaml:"spec"`
	compiled compiledRule
}

// RedactionRuleSpec holds a rule's patterns, selectors and replacement text.
type RedactionRuleSpec struct {
	Patterns    []string `yaml:"patterns"`
	Paths       []string `yaml:"paths"`
	Replacement string   `yaml:"replacement"`
}

// YankList lists the skill versions that are withdrawn (spec Section 3.2).
type YankList struct {
	origin `yaml:"-"`
	Kind   Kind   `yaml:"kind"`
	Yanks  []Yank `yaml:"yanks"`
}

// Yank withdraws one skill version.
type Yank struct {
	Skill  string    `yaml:"skill"`
	By     string    `yaml:"by"`
	At     time.Time `yaml:"at"`
	Reason string    `yaml:"reason"`
}

// GrantedTool is one tool an agent may call, as its grants resolve.
type GrantedTool struct {
	Ref        ToolRef
	Effect     Effect
	SchemaHash string
}

// Tools returns the tools the agent's grants resolve to, sorted.
func (a *Agent) Tools() []GrantedTool { return a.tools }

// Skills returns the skill versions the agent pins, in the order it lists them.
func (a *Agent) Skills() []SkillRef { return a.skills }

// Version returns the agent's version: the hash of its file, the skill
// versions it pins and the tools its grants resolve to (spec Section 3.1).
func (a *Agent) Version() string { return a.version }

// WorkSettings are a work item type's settings, with the defaults of spec
// Section 15 where the file sets none.
type WorkSettings struct {
	Key            []KeyField
	Budget         Budget
	RoundLimit     time.Duration
	MaxAgents      int
	Limits         Limits
	Convergence    Convergence
	Milestones     []string
	AutoClose      time.Duration
	InvisibleShare float64
	ShadowRuns     []ShadowSettings
}

// Budget is a run's budget.
type Budget struct {
	WallClock time.Duration
	Tokens    int64
	Rounds    int
}

// Limits are the board limits of spec Section 4.2.
type Limits struct {
	EntriesPerRound int
	OpenClaims      int
	LeaseRounds     int
}

// Convergence holds the convergence settings of spec Section 4.3.
type Convergence struct {
	MinContributors  int
	MinSources       int
	RefutationMargin int
}

// ShadowSettings are a shadow run's settings after its override.
type ShadowSettings struct {
	Name       string
	Arms       []string
	Budget     Budget
	RoundLimit time.Duration
	// Agents and Runtime are empty when the run uses the primary run's.
	Agents  []string
	Runtime string
}

// Settings returns the type's settings, with defaults applied.
func (w *WorkItemType) Settings() WorkSettings { return w.settings }

// compiledRule is a redaction rule ready to apply.
type compiledRule struct {
	patterns []*regexp.Regexp
	paths    []selector.Selector
}

// Patterns returns the rule's compiled regular expressions.
func (r *RedactionRule) Patterns() []*regexp.Regexp { return r.compiled.patterns }

// Paths returns the rule's compiled selectors.
func (r *RedactionRule) Paths() []selector.Selector { return r.compiled.paths }

// Replacement returns the text that replaces what the rule redacts.
func (r *RedactionRule) Replacement() string {
	if r.Spec.Replacement != "" {
		return r.Spec.Replacement
	}
	return "[redacted:" + r.Metadata.Name + "]"
}
