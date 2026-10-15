package board

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// Gateway is the author of the entries the MCP gateway writes: proposals and
// its notices (spec Section 4.2).
var Gateway = Author{Kind: AuthorPlatform, Name: "gateway"}

// MaxNotices caps the notices one agent's calls leave in one round.
const MaxNotices = 10

// Posting is a post that passed the board's rules: the entry to commit or,
// when the post repeats one of its author's entries, that entry, and nothing
// to commit. Blackboard tools are idempotent, so a call repeated after a lost
// response does no harm (spec Section 5.2).
type Posting struct {
	// Draft is the entry to commit, or nil when the post repeats one.
	Draft *Draft
	// Earlier is the entry the post repeats, or nil.
	Earlier *Event
}

func repeated(e *Event) Posting { return Posting{Earlier: e} }

func drafted(d *Draft) Posting { return Posting{Draft: d} }

// Posted is what a post led to once committed: the entry it committed, or
// the earlier entry it repeats, with nothing committed.
type Posted struct {
	Event  Event
	Repeat bool
}

// The round's rules, which the board tools keep (spec Sections 4.2, 5.2 and
// 5.6), apply to agents' posts only, and only when they are posted: a board
// replays under any settings. A repeat is found before any of them, so it
// always returns the earlier entry. A post is a repeat only when posting it
// again would change nothing: one that would change its author's stance is
// a new entry.

// PostEvidence checks evidence as the board tools post it. It repeats the
// author's live evidence with the same claim, call, quote, hypothesis and
// stance, while that stance is still the author's latest on the hypothesis.
func (s *State) PostEvidence(c *Context, id EntryID, author Author, in *EvidenceInput, call *ReadCall) (Posting, error) {
	claim, quote := strings.TrimSpace(in.Claim), NormalizeSpace(in.Quote)
	if e := s.earlier(author, func(e *Event) bool {
		ev := e.Body.Evidence
		_, gone := s.retracted[e.ID]
		return ev != nil && !gone && ev.Claim == claim && ev.Provenance.CallID == in.CallID &&
			ev.Provenance.Quote == quote && ev.Hypothesis == in.Hypothesis && ev.Stance == in.Stance &&
			(ev.Hypothesis == "" || s.stanceOf(author, ev.Hypothesis) == ev.Stance)
	}); e != nil {
		return repeated(e), nil
	}
	if err := s.admit(c, author, KindEvidence, in.Hypothesis, in.Stance); err != nil {
		return Posting{}, err
	}
	d, err := s.NewEvidence(c, id, author, in, call)
	if err != nil {
		return Posting{}, err
	}
	return drafted(&d), nil
}

// PostHypothesis checks a hypothesis as the board tools post it. It repeats
// the author's hypothesis with the same key, statement and evidence, unless
// the author has since refuted it: posting it again then supports it anew.
func (s *State) PostHypothesis(c *Context, id EntryID, author Author, in *HypothesisInput) (Posting, error) {
	statement, evidence := strings.TrimSpace(in.Statement), uniqueIDs(in.Evidence)
	if e := s.earlier(author, func(e *Event) bool {
		h := e.Body.Hypothesis
		return h != nil && h.Key.Equal(in.Key) && h.Statement == statement && slices.Equal(h.Evidence, evidence) &&
			s.stanceOf(author, e.ID) != Refutes
	}); e != nil {
		return repeated(e), nil
	}
	if err := s.admit(c, author, KindHypothesis, "", ""); err != nil {
		return Posting{}, err
	}
	d, err := s.NewHypothesis(c, id, author, in)
	if err != nil {
		return Posting{}, err
	}
	return drafted(&d), nil
}

// Retract checks a retraction as the board tools post it. Retracting
// evidence the author already retracted repeats that retraction.
func (s *State) Retract(c *Context, id EntryID, author Author, evidence EntryID) (Posting, error) {
	if rid, done := s.retracted[evidence]; done {
		if e, _ := s.Event(rid); e.Author == author {
			return repeated(&e), nil
		}
	}
	if err := s.admit(c, author, KindRetraction, "", ""); err != nil {
		return Posting{}, err
	}
	d, err := s.NewRetraction(c, id, author, evidence)
	if err != nil {
		return Posting{}, err
	}
	return drafted(&d), nil
}

// PostTask checks a task. It repeats the author's open task with the same
// title and tags; once that task is completed, the same task can be posted
// again (spec Section 5.2).
func (s *State) PostTask(c *Context, id EntryID, author Author, in *TaskInput) (Posting, error) {
	t := &Task{Title: strings.TrimSpace(in.Title), Tags: slices.Clone(in.Tags)}
	if t.Tags == nil {
		t.Tags = []string{}
	}
	if e := s.earlier(author, func(e *Event) bool {
		old := e.Body.Task
		_, done := s.completed[e.ID]
		return old != nil && !done && old.Title == t.Title && slices.Equal(old.Tags, t.Tags)
	}); e != nil {
		return repeated(e), nil
	}
	if err := s.admit(c, author, KindTask, "", ""); err != nil {
		return Posting{}, err
	}
	d, err := s.prepare(c, id, author, &Body{Task: t})
	if err != nil {
		return Posting{}, err
	}
	return drafted(&d), nil
}

// Claim checks a claim on a task (spec Section 4.2). A task has one holder at
// a time: the agent of its latest claim, from the round of the claim through
// the lease's last round, until the task is completed. An agent holds at most
// the open claims the limits allow, and one whose lease just lapsed leaves the
// task to the others for a round. Claiming a task the author holds repeats
// its claim.
func (s *State) Claim(c *Context, id EntryID, author Author, task EntryID) (Posting, error) {
	if _, err := s.task(task); err != nil {
		return Posting{}, err
	}
	if _, done := s.completed[task]; done {
		return Posting{}, fmt.Errorf("%w: task %s is completed", ErrInvalid, task)
	}
	lease := c.Settings.Limits.orDefault().LeaseRounds
	holder, claim, held := s.holder(task, c.Round, lease)
	if held && holder == author {
		return repeated(claim), nil
	}
	if err := s.admit(c, author, KindClaim, "", ""); err != nil {
		return Posting{}, err
	}
	if held {
		return Posting{}, fmt.Errorf("%w: another agent holds task %s through round %d", ErrHeld, task, claim.Round+lease-1)
	}
	if last, ok := s.Event(s.lastClaim[task]); ok && last.Author == author && c.Round == last.Round+lease {
		return Posting{}, fmt.Errorf("%w: you held task %s through round %d; it is open to the other agents for this round", ErrHeld, task, c.Round-1)
	}
	if n := s.openClaims(author, c.Round, lease); n >= c.Settings.Limits.orDefault().OpenClaims {
		return Posting{}, fmt.Errorf("%w: you hold %d tasks, the most an agent may; complete one first", ErrLimit, n)
	}
	d, err := s.prepare(c, id, author, &Body{Claim: &Claim{Task: task}})
	if err != nil {
		return Posting{}, err
	}
	return drafted(&d), nil
}

// Complete checks a completion: the author holds the task now, and cites the
// live evidence that answers it. Completing a task the author completed
// repeats its completion.
func (s *State) Complete(c *Context, id EntryID, author Author, in *CompletionInput) (Posting, error) {
	if _, err := s.task(in.Task); err != nil {
		return Posting{}, err
	}
	if cid, done := s.completed[in.Task]; done {
		e, _ := s.Event(cid)
		if e.Author == author {
			return repeated(&e), nil
		}
		return Posting{}, fmt.Errorf("%w: task %s is already completed", ErrInvalid, in.Task)
	}
	if err := s.admit(c, author, KindCompletion, "", ""); err != nil {
		return Posting{}, err
	}
	if holder, _, held := s.holder(in.Task, c.Round, c.Settings.Limits.orDefault().LeaseRounds); !held || holder != author {
		return Posting{}, fmt.Errorf("%w: you do not hold task %s; claim it first", ErrNotAllowed, in.Task)
	}
	d, err := s.prepare(c, id, author, &Body{Completion: &Completion{Task: in.Task, Evidence: uniqueIDs(in.Evidence)}})
	if err != nil {
		return Posting{}, err
	}
	return drafted(&d), nil
}

// EndTurn records that the author ended its turn in the round. Ending it again
// repeats the first.
func (s *State) EndTurn(c *Context, id EntryID, author Author) (Posting, error) {
	if e := s.earlier(author, func(e *Event) bool { return e.Body.TurnEnd != nil && e.Round == c.Round }); e != nil {
		return repeated(e), nil
	}
	d, err := s.prepare(c, id, author, &Body{TurnEnd: &TurnEnd{}})
	if err != nil {
		return Posting{}, err
	}
	return drafted(&d), nil
}

// Coordinator is the author of round entries.
var Coordinator = Author{Kind: AuthorPlatform, Name: "coordinator"}

// StartRound checks the start of a round, which the coordinator writes when
// it freezes the round's snapshot. Starting the board's current round again,
// with the same wrap-up hypothesis, repeats its entry.
func (s *State) StartRound(c *Context, id EntryID, author Author, in *RoundStart) (Posting, error) {
	if s.round >= 0 && *s.events[s.round].Body.Round == *in {
		e := s.events[s.round]
		return repeated(&e), nil
	}
	opened := *c
	opened.Round = in.Round
	r := *in
	d, err := s.prepare(&opened, id, author, &Body{Round: &r})
	if err != nil {
		return Posting{}, err
	}
	return drafted(&d), nil
}

// Opened applies the board's latest round entry to a context: its round,
// its snapshot, which holds every entry before the round entry, and its
// wrap-up hypothesis. A board without round entries leaves the context as it
// is.
func (s *State) Opened(c *Context) {
	if s.round < 0 {
		return
	}
	e := &s.events[s.round]
	c.Round, c.Snapshot, c.WrapUp = e.Body.Round.Round, e.Seq-1, e.Body.Round.WrapUp
}

// Proposable is what the board says of a write call before the gateway reads
// its candidates and facts: the key of the hypothesis it cites, which its
// scope binds to, or the proposal it repeats.
type Proposable struct {
	Key     key.Key
	Earlier *Event
}

// Proposable checks a write call against the board before any read is made
// for it (spec Section 5.3): the cited hypothesis exists and is not rejected,
// and the proposer itself supports it with live evidence, which a proposer
// must (spec Section 9.2). The round's rules apply to the proposer as to a
// post. A call that repeats the proposer's proposal with the same tool,
// arguments and hypothesis returns that proposal.
func (s *State) Proposable(c *Context, p *Proposal) (Proposable, error) {
	if e := s.earlierProposal(p); e != nil {
		return Proposable{Earlier: e}, nil
	}
	h, err := s.proposable(c, p)
	if err != nil {
		return Proposable{}, err
	}
	e, _ := s.event(h)
	return Proposable{Key: maps.Clone(e.Body.Hypothesis.Key)}, nil
}

// Propose checks a proposal again, once its reads are done, and returns it to
// commit as the gateway's entry, or the proposal it repeats.
func (s *State) Propose(c *Context, id EntryID, p *Proposal) (Posting, error) {
	if e := s.earlierProposal(p); e != nil {
		return repeated(e), nil
	}
	if _, err := s.proposable(c, p); err != nil {
		return Posting{}, err
	}
	cp := *p
	cp.Target, cp.Summary = slices.Clone(p.Target), slices.Clone(p.Summary)
	cp.Scope, cp.Facts, cp.Candidates = maps.Clone(p.Scope), maps.Clone(p.Facts), maps.Clone(p.Candidates)
	d, err := s.prepare(c, id, Gateway, &Body{Proposal: &cp})
	if err != nil {
		return Posting{}, err
	}
	return drafted(&d), nil
}

func (s *State) proposable(c *Context, p *Proposal) (EntryID, error) {
	proposer := Author{Kind: AuthorAgent, Name: p.Proposer}
	if p.Proposer == "" {
		return "", fmt.Errorf("%w: a proposal names its proposer", ErrInvalid)
	}
	if err := s.admit(c, proposer, KindProposal, p.Hypothesis, ""); err != nil {
		return "", err
	}
	h, err := s.canonicalOf(p.Hypothesis)
	if err != nil {
		return "", err
	}
	// The refusals name the hypothesis as the proposer cited it: the one it
	// merged into may be another agent's entry of this round.
	if s.rejected[h] {
		return "", fmt.Errorf("%w: hypothesis %s is rejected", ErrNotAllowed, p.Hypothesis)
	}
	if s.stanceOf(proposer, h) != Supports {
		return "", fmt.Errorf("%w: you support hypothesis %s with no live evidence of your own, so you cannot propose an action for it", ErrNotAllowed, p.Hypothesis)
	}
	return h, nil
}

func (s *State) earlierProposal(p *Proposal) *Event {
	return s.earlier(Gateway, func(e *Event) bool {
		old := e.Body.Proposal
		return old != nil && old.Proposer == p.Proposer && old.Tool == p.Tool && old.SchemaHash == p.SchemaHash &&
			old.ArgsHash == p.ArgsHash && old.Hypothesis == p.Hypothesis
	})
}

// Notify checks a platform notice. One notice per round, agent, tool, code
// and rule is enough, and an agent's calls leave at most MaxNotices in a
// round: a later one repeats the latest, so calls that loop cannot flood the
// board.
func (s *State) Notify(c *Context, id EntryID, author Author, n *Notice) (Posting, error) {
	var latest *Event
	count := 0
	for i := range s.events {
		e := &s.events[i]
		old := e.Body.Notice
		if old == nil || e.Round != c.Round || old.Agent != n.Agent {
			continue
		}
		if old.Code == n.Code && old.Tool == n.Tool && old.Rule == n.Rule {
			found := *e
			return repeated(&found), nil
		}
		latest, count = e, count+1
	}
	if count >= MaxNotices {
		found := *latest
		return repeated(&found), nil
	}
	cp := *n
	d, err := s.prepare(c, id, author, &Body{Notice: &cp})
	if err != nil {
		return Posting{}, err
	}
	return drafted(&d), nil
}

// admit applies the round's rules to an agent's post: nothing after it ended
// its turn in the round, only what the wrap-up round allows, and no more
// entries than the round's limit.
func (s *State) admit(c *Context, author Author, kind Kind, hypothesis EntryID, stance Stance) error {
	if author.Kind != AuthorAgent {
		return nil
	}
	if s.earlier(author, func(e *Event) bool { return e.Body.TurnEnd != nil && e.Round == c.Round }) != nil {
		return fmt.Errorf("%w: you ended your turn in round %d", ErrTurnEnded, c.Round)
	}
	if c.WrapUp != "" {
		if err := s.wrapUp(c.WrapUp, kind, hypothesis, stance); err != nil {
			return err
		}
	}
	if counted(kind) {
		limit := c.Settings.Limits.orDefault().EntriesPerRound
		if n := s.posted(author, c.Round); n >= limit {
			return fmt.Errorf("%w: you posted %d entries in round %d, the most a round allows", ErrLimit, n, c.Round)
		}
	}
	return nil
}

// wrapUp allows, in the wrap-up round, only evidence that refutes the
// converged hypothesis, retractions, and proposals that cite it; aliases count
// as the hypothesis they merged into (spec Section 5.6).
func (s *State) wrapUp(converged EntryID, kind Kind, cited EntryID, stance Stance) error {
	target, _ := s.canonicalOf(converged)
	h, err := s.canonicalOf(cited)
	addresses := err == nil && target != "" && h == target
	switch kind {
	case KindRetraction:
		return nil
	case KindEvidence:
		if addresses && stance == Refutes {
			return nil
		}
		return fmt.Errorf("%w: evidence in this round must refute hypothesis %s", ErrWrapUp, converged)
	case KindProposal:
		if addresses {
			return nil
		}
		return fmt.Errorf("%w: an action in this round must cite hypothesis %s", ErrWrapUp, converged)
	}
	return fmt.Errorf("%w: this round allows only evidence that refutes hypothesis %s, retractions, and actions that cite it", ErrWrapUp, converged)
}

// counted reports whether an entry of kind counts toward the entry limit:
// evidence, hypotheses, tasks and proposals do. Retractions, claims,
// completions and turn ends act on entries that were counted.
func counted(kind Kind) bool {
	return kind == KindEvidence || kind == KindHypothesis || kind == KindTask || kind == KindProposal
}

// posted counts the agent's entries in round that count toward the limit:
// its posts, and the proposals the gateway recorded for it.
func (s *State) posted(agent Author, round int) int {
	n := 0
	for i := range s.events {
		e := &s.events[i]
		if e.Round != round {
			continue
		}
		mine := e.Author == agent && counted(e.Body.Kind())
		proposed := e.Body.Proposal != nil && agent.Kind == AuthorAgent && e.Body.Proposal.Proposer == agent.Name
		if mine || proposed {
			n++
		}
	}
	return n
}

// stanceOf returns a contributor's latest live stance on a hypothesis, or ""
// when it has none.
func (s *State) stanceOf(contributor Author, hypothesis EntryID) Stance {
	h, err := s.canonicalOf(hypothesis)
	if err != nil {
		return ""
	}
	var latest Stance
	for _, r := range s.records(h) {
		if r.contributor == contributor {
			latest = r.stance
		}
	}
	return latest
}

// holder returns who holds a task in round, and the claim: the agent of the
// task's latest claim, through round claim+lease-1, unless the task is
// completed.
func (s *State) holder(task EntryID, round, lease int) (Author, *Event, bool) {
	if _, done := s.completed[task]; done {
		return Author{}, nil, false
	}
	id, ok := s.lastClaim[task]
	if !ok {
		return Author{}, nil, false
	}
	e, _ := s.Event(id)
	if round > e.Round+lease-1 {
		return Author{}, nil, false
	}
	return e.Author, &e, true
}

// openClaims counts the tasks the author holds in round.
func (s *State) openClaims(author Author, round, lease int) int {
	n := 0
	for task := range s.lastClaim {
		if holder, _, held := s.holder(task, round, lease); held && holder == author {
			n++
		}
	}
	return n
}

// earlier returns a copy of the author's first entry for which match holds.
// Humans' entries never repeat: they post through the human interface.
func (s *State) earlier(author Author, match func(*Event) bool) *Event {
	if author.Kind == AuthorHuman {
		return nil
	}
	for i := range s.events {
		if e := s.events[i]; e.Author == author && match(&e) {
			return &e
		}
	}
	return nil
}
