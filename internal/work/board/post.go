package board

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
)

var errNoHumanEntries = fmt.Errorf("%w: this run takes no human entries", ErrNotAllowed)

// NewEvidence checks an evidence post and returns the entry to commit.
//
// An agent's evidence cites one of its own read calls in this run, plus a
// quote that appears in the call's stored output; call is that read call as
// the audit store holds it, or nil when the store has no such call. A human's
// evidence is human observation, and only runs that take human entries accept
// it. A human's support that would lift an agent's standing refutation counts
// as its dismissal and needs a reason (spec Section 4.2).
func (s *State) NewEvidence(c *Context, id EntryID, author Author, in *EvidenceInput, call *ReadCall) (Draft, error) {
	ev := &Evidence{
		Claim:      strings.TrimSpace(in.Claim),
		Hypothesis: in.Hypothesis,
		Stance:     in.Stance,
		Reason:     in.Reason,
	}
	switch author.Kind {
	case AuthorAgent:
		p, err := agentProvenance(c, author, in, call)
		if err != nil {
			return Draft{}, err
		}
		ev.Provenance = p
	case AuthorHuman:
		if !c.HumanEntries {
			return Draft{}, errNoHumanEntries
		}
		if in.CallID != "" || in.Quote != "" {
			return Draft{}, fmt.Errorf("%w: a human's evidence cites no read call", ErrInvalid)
		}
		ev.Provenance = Provenance{Kind: FromHuman, Source: HumanObservation}
	default:
		return Draft{}, fmt.Errorf("%w: %s authors do not post evidence this way", ErrNotAllowed, author.Kind)
	}
	d, err := s.prepare(c, id, author, &Body{Evidence: ev})
	if err != nil {
		return Draft{}, err
	}
	if author.Kind == AuthorHuman && ev.Stance == Supports {
		if err := s.liftRefutations(author, ev); err != nil {
			return Draft{}, err
		}
	}
	return d, nil
}

func agentProvenance(c *Context, author Author, in *EvidenceInput, call *ReadCall) (Provenance, error) {
	quote := NormalizeSpace(in.Quote)
	if err := checkText("quote", quote, MaxQuoteRunes); err != nil {
		return Provenance{}, err
	}
	switch {
	case call == nil || in.CallID == "" || call.ID != in.CallID:
		return Provenance{}, fmt.Errorf("%w: no read call %q", ErrProvenance, in.CallID)
	case call.Run != c.Run:
		return Provenance{}, fmt.Errorf("%w: read call %s belongs to another run", ErrProvenance, call.ID)
	case call.Agent != author.Name:
		return Provenance{}, fmt.Errorf("%w: read call %s was not made by %s", ErrProvenance, call.ID, author.Name)
	case call.Failed:
		return Provenance{}, fmt.Errorf("%w: read call %s ended in an error, which cannot be cited", ErrProvenance, call.ID)
	case !QuoteAppears(call.Output, quote):
		return Provenance{}, fmt.Errorf("%w: the quote does not appear in the output of read call %s", ErrProvenance, call.ID)
	case inArguments(call.Args, quote):
		return Provenance{}, fmt.Errorf("%w: the quote is text the call was given, not text it answered", ErrProvenance)
	}
	return Provenance{Kind: FromReadCall, CallID: call.ID, Quote: quote, Source: ToolSource(call.Server, call.Tool)}, nil
}

// liftRefutations records which agent refutations the human's support would
// lift, and requires a reason when there are any.
func (s *State) liftRefutations(author Author, ev *Evidence) error {
	h := s.canonical[ev.Hypothesis]
	support := stanceRecord{contributor: author, stance: Supports, seq: math.MaxInt64, sources: []Source{HumanObservation}}
	before := s.countStances(h, nil).standing
	after := s.countStances(h, &support).standing
	for i := range before {
		r := &before[i]
		stillStands := slices.ContainsFunc(after, func(a stanceRecord) bool { return a.entry == r.entry })
		if r.contributor != author && !stillStands {
			ev.Dismisses = append(ev.Dismisses, r.entry)
		}
	}
	if len(ev.Dismisses) > 0 {
		return checkReason(ev.Reason, true)
	}
	return nil
}

// NewHypothesis checks a hypothesis post and returns the entry to commit. A
// hypothesis whose key equals an earlier mergeable key becomes an alias of it,
// and its author's support counts for the earlier one (spec Section 4.3).
// Citing the same evidence twice counts once.
func (s *State) NewHypothesis(c *Context, id EntryID, author Author, in *HypothesisInput) (Draft, error) {
	h := &Hypothesis{
		Key:       maps.Clone(in.Key),
		Statement: strings.TrimSpace(in.Statement),
		Evidence:  uniqueIDs(in.Evidence),
	}
	h.AliasOf, _ = s.mergeTarget(h.Key)
	return s.prepare(c, id, author, &Body{Hypothesis: h})
}

// NewRetraction checks that an agent retracts its own live evidence and
// returns the entry to commit.
func (s *State) NewRetraction(c *Context, id EntryID, author Author, evidence EntryID) (Draft, error) {
	return s.prepare(c, id, author, &Body{Retraction: &Retraction{Evidence: evidence}})
}

// NewRuling checks a human's ruling and returns the entry to commit. Only runs
// that take human entries accept rulings.
func (s *State) NewRuling(c *Context, id EntryID, author Author, in *RulingInput) (Draft, error) {
	if author.Kind == AuthorHuman && !c.HumanEntries {
		return Draft{}, errNoHumanEntries
	}
	r := &Ruling{Kind: in.Kind, Hypothesis: in.Hypothesis, Refutation: in.Refutation, Reason: in.Reason}
	return s.prepare(c, id, author, &Body{Ruling: r})
}

// prepare checks a new entry against the board and returns its draft, of this
// build's version.
func (s *State) prepare(c *Context, id EntryID, author Author, b *Body) (Draft, error) {
	if c.Run == "" {
		return Draft{}, fmt.Errorf("%w: entry without a run", ErrInvalid)
	}
	if err := s.checkNewID(id); err != nil {
		return Draft{}, err
	}
	if err := s.check(author, b); err != nil {
		return Draft{}, err
	}
	d := Draft{ID: id, Run: c.Run, Round: c.Round, Author: author, Body: *b}
	d.Body.V = Version
	return d, nil
}

func uniqueIDs(ids []EntryID) []EntryID {
	out := make([]EntryID, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
