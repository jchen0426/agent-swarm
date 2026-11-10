package app

import (
	"context"
	"slices"
	"strings"

	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// SuggestionGroup is a hypothesis's suggestions of one target (design D6).
type SuggestionGroup struct {
	Target      []board.TargetValue
	Suggestions []Suggestion // in commit order
}

// Suggestion is one of a hypothesis's suggestions, a proposal the gateway
// recorded for a person and never sent (spec 6.3; design D6).
type Suggestion struct {
	Event      board.Event      // the proposal
	Status     board.StatusWord // its hypothesis's, now
	Superseded bool
	Service    ServiceMark
	Support    map[board.Source]int // the proposer's own counted evidence on the hypothesis
}

// ServiceMark says whether a suggestion acts on the item's service (review
// M3): inside it, outside it, or not compared, when neither side can be
// determined, so that the mark never fails silently.
type ServiceMark string

// Service marks.
const (
	ServiceInside     ServiceMark = "inside"
	ServiceOutside    ServiceMark = "outside"
	ServiceUncompared ServiceMark = "uncompared"
)

// serviceBinding is the scope binding the service mark compares, spec 3.3's
// example: the cited hypothesis's service.
const serviceBinding = "hypothesis.service"

// suggesting is what a board's suggestions are read against: the revision
// the item locked, nil when it cannot be read; the item's routing service;
// and the hypotheses converged at some round's end.
type suggesting struct {
	rev       *config.Revision
	service   string
	converged map[board.EntryID]bool
}

// suggesting reads what s's suggestions are read against: the revision the
// item locked, once, through Revisions.At, only when events hold a
// proposal. A mark compares the item's routing service under that revision
// (see mark), so a mark needs it when the item has a routing service: a
// revision a mark needed and could not read, a cache miss among them
// (R175), leaves every suggestion uncompared, and bv names its commit
// (revisionMissed), so that the watch builds the projection again once it
// answers. The hypotheses converged at some round's end are every
// round_ended's Converged of the run, read from the records, with no replay
// (R147).
func (v *Views) suggesting(ctx context.Context, s *item.State, events []board.Event, bv *BoardView) *suggesting {
	sg := &suggesting{service: s.Opened.Routing["service"], converged: map[board.EntryID]bool{}}
	for i := range s.Run.Rounds {
		if e := s.Run.Rounds[i].Ended; e != nil {
			for _, id := range e.Converged {
				sg.converged[id] = true
			}
		}
	}
	proposed := false
	for i := range events {
		proposed = proposed || events[i].Body.Proposal != nil
	}
	if proposed && v.p.Revisions != nil && s.Started != nil {
		rev, err := v.p.Revisions.At(ctx, s.Started.Commit)
		switch {
		case err == nil && rev != nil:
			sg.rev = rev
		case sg.service != "":
			bv.revisionMissed = s.Started.Commit
		}
	}
	return sg
}

// groups are card's suggestions (design D6): the proposals citing its
// hypothesis or an alias of it (board.State.Proposals), grouped by their
// target, as tool policy lists it, each group in commit order and the groups
// by their first proposal's. Each holds its hypothesis's status now; the
// superseded mark (board.Superseded); its service mark (see mark); and the
// proposer's own counted evidence on the hypothesis, by source
// (board.State.SupportBy), which is how its support was obtained.
func (sg *suggesting) groups(b *board.State, card *HypothesisCard) []SuggestionGroup {
	var groups []SuggestionGroup
	proposals := b.Proposals(card.ID)
	for i := range proposals {
		p := proposals[i].Body.Proposal
		g := slices.IndexFunc(groups, func(g SuggestionGroup) bool { return slices.Equal(g.Target, p.Target) })
		if g < 0 {
			groups = append(groups, SuggestionGroup{Target: p.Target})
			g = len(groups) - 1
		}
		groups[g].Suggestions = append(groups[g].Suggestions, Suggestion{
			Event: proposals[i], Status: card.Status, Superseded: board.Superseded(&card.HypothesisView, sg.converged[card.ID]),
			Service: sg.mark(p), Support: b.SupportBy(card.ID, board.Author{Kind: board.AuthorAgent, Name: p.Proposer}),
		})
	}
	return groups
}

// mark is p's service mark (review M3), from the tool policy of the revision
// the item locked: for each scoped argument whose binding, trimmed, is
// hypothesis.service, outside when the value the proposal recorded for it
// differs from the item's routing service, and inside when every such value
// is the service. It is uncompared when no such binding exists, the
// proposal recorded no value for one, the item has no routing service, or the
// revision or the tool cannot be read.
func (sg *suggesting) mark(p *board.Proposal) ServiceMark {
	if sg.rev == nil || sg.service == "" {
		return ServiceUncompared
	}
	ref, err := config.ParseToolRef(p.Tool)
	if err != nil {
		return ServiceUncompared
	}
	policy := sg.rev.Tool(ref)
	if policy == nil {
		return ServiceUncompared
	}
	mark := ServiceUncompared
	for arg, binding := range policy.Scope {
		value, recorded := p.Scope[arg]
		switch {
		case strings.TrimSpace(binding) != serviceBinding || !recorded:
		case value != sg.service:
			return ServiceOutside
		default:
			mark = ServiceInside
		}
	}
	return mark
}
