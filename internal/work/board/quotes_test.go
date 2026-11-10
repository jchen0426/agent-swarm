package board_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// perSide is the spec's quotes per side on a hypothesis (spec Section 15).
const perSide = 2

// quoted is a hypothesis's quotes as entry ids, in the rule's order, and the
// counts of the candidates not picked; a side with no quote holds nil.
type quoted struct {
	supports, refutes         []board.EntryID
	moreSupports, moreRefutes int
}

func quotedOf(q board.Quotes) quoted {
	out := quoted{moreSupports: q.MoreSupports, moreRefutes: q.MoreRefutes}
	for i := range q.Supports {
		out.supports = append(out.supports, q.Supports[i].ID)
	}
	for i := range q.Refutes {
		out.refutes = append(out.refutes, q.Refutes[i].ID)
	}
	return out
}

// The quote rule (design D4): the candidates are the entries behind the
// records the assessment counts, aliases included; platform evidence comes
// first, then human, then agent, each newest first; the walk takes one entry
// of each distinct source, up to the spec's 2 a side, and counts the rest.
func TestQuotesFollowTheFixedRule(t *testing.T) {
	for _, tc := range []struct {
		name string
		// build builds the row's board and returns the hypothesis asked and
		// the quotes the rule gives it.
		build func(f *fixture) (board.EntryID, quoted)
	}{
		{"two agents of two sources supporting: both, newest first", func(f *fixture) (board.EntryID, quoted) {
			e1 := f.evidence(release, "cd", "get_history", "v42", "", "")
			h := f.hypothesis(release, checkoutDeploy(), e1)
			e2 := f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
			return h, quoted{supports: []board.EntryID{e2, e1}}
		}},
		{"three supporting entries of two sources, two of one: the newest of each source", func(f *fixture) (board.EntryID, quoted) {
			e1 := f.evidence(release, "cd", "get_history", "v42", "", "")
			h := f.hypothesis(release, checkoutDeploy(), e1)
			f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
			e3 := f.evidence(deps, "logs", "search", "NPE in v42", h, board.Supports)
			return h, quoted{supports: []board.EntryID{e3, e1}, moreSupports: 1}
		}},
		{"a person's support and two agents': the person's first, then the newest agent's", func(f *fixture) (board.EntryID, quoted) {
			e1 := f.evidence(release, "cd", "get_history", "v42", "", "")
			h := f.hypothesis(release, checkoutDeploy(), e1)
			person := f.humanEvidence(alice, h, board.Supports, nil)
			e3 := f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
			return h, quoted{supports: []board.EntryID{person, e3}, moreSupports: 1}
		}},
		{"two persons' support: one quote of human observation, then an agent's (R145)", func(f *fixture) (board.EntryID, quoted) {
			e1 := f.evidence(release, "cd", "get_history", "v42", "", "")
			h := f.hypothesis(release, checkoutDeploy(), e1)
			f.humanEvidence(alice, h, board.Supports, nil)
			bobs := f.humanEvidence(bob, h, board.Supports, nil)
			return h, quoted{supports: []board.EntryID{bobs, e1}, moreSupports: 1}
		}},
		{"the author's own cited evidence, posted before the hypothesis with no stance (review I7)", func(f *fixture) (board.EntryID, quoted) {
			theirs := f.evidence(runtime, "logs", "search", "NPE", "", "")
			e1 := f.evidence(release, "cd", "get_history", "v42", "", "")
			e2 := f.evidence(release, "metrics", "query", "error rate 12%", "", "")
			// The evidence of another author it cites is no support of the author's.
			h := f.hypothesis(release, checkoutDeploy(), e1, e2, theirs)
			return h, quoted{supports: []board.EntryID{e2, e1}}
		}},
		{"a retracted entry is no candidate", func(f *fixture) (board.EntryID, quoted) {
			e1 := f.evidence(release, "cd", "get_history", "v42", "", "")
			e2 := f.evidence(release, "metrics", "query", "error rate 12%", "", "")
			h := f.hypothesis(release, checkoutDeploy(), e1, e2)
			e3 := f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
			f.retract(release, e2)
			f.retract(runtime, e3)
			return h, quoted{supports: []board.EntryID{e1}}
		}},
		{"a contributor whose latest stance refutes: its latest refutation alone", func(f *fixture) (board.EntryID, quoted) {
			e1 := f.evidence(release, "cd", "get_history", "v42", "", "")
			h := f.hypothesis(release, checkoutDeploy(), e1)
			f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
			f.evidence(runtime, "logs", "search", "the NPE predates v42", h, board.Refutes)
			latest := f.evidence(runtime, "metrics", "query", "errors started before 10:02", h, board.Refutes)
			return h, quoted{supports: []board.EntryID{e1}, refutes: []board.EntryID{latest}}
		}},
		{"an alias's evidence counts for its hypothesis, and the alias's id asks for the hypothesis's", func(f *fixture) (board.EntryID, quoted) {
			e1 := f.evidence(release, "cd", "get_history", "v42", "", "")
			h := f.hypothesis(release, checkoutDeploy(), e1)
			e2 := f.evidence(runtime, "logs", "search", "NPE", "", "")
			alias := f.hypothesis(runtime, checkoutDeploy(), e2)
			e3 := f.evidence(deps, "metrics", "query", "error rate 12%", alias, board.Supports)
			assert.Equal(f.t, f.state.Quotes(h, perSide), f.state.Quotes(alias, perSide), "Quotes of the alias's id gives the hypothesis's")
			assert.Equal(f.t, board.Quotes{}, f.state.Quotes(e3, perSide), "an id of no hypothesis has none")
			return h, quoted{supports: []board.EntryID{e3, e2}, moreSupports: 1}
		}},
		{"the platform's evidence a hypothesis cites is never quoted (R145)", func(f *fixture) (board.EntryID, quoted) {
			opening := f.must(f.state.PostPlatformEvidence(&f.ctx, f.nextID("pe"), board.Coordinator,
				&board.PlatformEvidenceInput{Claim: "Opened by alerting: checkout fails", Ref: "item:1", Source: "alerting"})).ID
			e1 := f.evidence(release, "cd", "get_history", "v42", "", "")
			h := f.hypothesis(release, checkoutDeploy(), opening, e1)
			return h, quoted{supports: []board.EntryID{e1}}
		}},
		{"one entry behind two records is one candidate", func(f *fixture) (board.EntryID, quoted) {
			e1 := f.evidence(release, "cd", "get_history", "v42", "", "")
			h := f.hypothesis(release, checkoutDeploy(), e1)
			both := f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
			// An alias of h by the entry's author, citing it: its record is
			// backed by the entry that supports h by stance.
			f.hypothesis(runtime, checkoutDeploy(), both)
			return h, quoted{supports: []board.EntryID{both, e1}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			h, want := tc.build(f)
			assert.Equal(t, want, quotedOf(f.state.Quotes(h, perSide)))
		})
	}
}

// sharesTheWalk asserts that h's quotes, with no bound on a side, are the
// assessment's: one quote per distinct source of h's view, each support by
// one of its supporters and each refutation by one of its refuters (plan
// review M10(a)).
func (f *fixture) sharesTheWalk(h board.EntryID) {
	f.t.Helper()
	q, v := f.state.Quotes(h, math.MaxInt), f.view(h)
	var sources []board.Source
	for i := range q.Supports {
		e := &q.Supports[i]
		sources = append(sources, e.Body.Evidence.Provenance.Source)
		assert.Contains(f.t, v.Supporters, e.Author, "support %s is a supporter's", e.ID)
	}
	assert.ElementsMatch(f.t, v.Sources, sources, "one quote per distinct source the assessment counts")
	for i := range q.Refutes {
		e := &q.Refutes[i]
		assert.Contains(f.t, v.Refuters, e.Author, "refutation %s is a refuter's", e.ID)
	}
}

// The quotes and the score come from one walk, so a quote never shows
// evidence the score ignores: on the boards of assess_test.go's tests,
// rebuilt with the same fixture calls and checked where those tests check
// them (the plan's; plan review M10(a)).
func TestTheQuotesAndTheAssessmentShareOneWalk(t *testing.T) {
	network := board.Author{Kind: board.AuthorAgent, Name: "network-investigator"}
	for _, tc := range []struct {
		name  string
		build func(f *fixture)
	}{
		{"TestConvergenceNeedsTwoContributorsAndTwoSources: one agent with two sources", func(f *fixture) {
			h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
			f.evidence(release, "metrics", "query", "error rate 12%", h, board.Supports)
			f.sharesTheWalk(h)
		}},
		{"TestConvergenceNeedsTwoContributorsAndTwoSources: two agents with one source", func(f *fixture) {
			h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "logs", "search", "NPE", "", ""))
			f.evidence(runtime, "logs", "search", "NPE again", h, board.Supports)
			f.sharesTheWalk(h)
		}},
		{"TestConvergenceNeedsTwoContributorsAndTwoSources: two agents with two sources", func(f *fixture) {
			h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
			f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
			f.sharesTheWalk(h)
		}},
		{"TestOnlyTheLatestStanceCounts", func(f *fixture) {
			h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
			f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
			f.evidence(runtime, "logs", "search", "the NPE predates v42", h, board.Refutes)
			f.sharesTheWalk(h)
		}},
		{"TestRetractedEvidenceStopsCounting", func(f *fixture) {
			h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
			f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
			wrong := f.evidence(runtime, "logs", "search", "no NPE after the rollback", h, board.Refutes)
			f.sharesTheWalk(h)
			f.retract(runtime, wrong)
			f.sharesTheWalk(h)
		}},
		{"TestHypothesisAuthorSupportNeedsLiveEvidence", func(f *fixture) {
			e := f.evidence(release, "cd", "get_history", "v42", "", "")
			h := f.hypothesis(release, checkoutDeploy(), e)
			f.sharesTheWalk(h)
			f.retract(release, e)
			f.sharesTheWalk(h)
		}},
		{"TestAgentRefutationStandsUntilOutnumbered", func(f *fixture) {
			h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
			f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
			f.evidence(deps, "metrics", "query", "errors started before 10:02", h, board.Refutes)
			f.sharesTheWalk(h)
			f.evidence(network, "metrics", "query", "no packet loss", h, board.Supports)
			f.sharesTheWalk(h)
		}},
		{"TestHumanSupportThatLiftsARefutationIsADismissal", func(f *fixture) {
			first := f.evidence(release, "cd", "get_history", "v42", "", "")
			h := f.hypothesis(release, checkoutDeploy(), first)
			f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
			f.evidence(deps, "metrics", "query", "errors started before 10:02", h, board.Refutes)
			_, err := f.postEvidence(alice, &board.EvidenceInput{Claim: "rollback fixed it", Hypothesis: h, Stance: board.Supports})
			require.ErrorIs(f.t, err, board.ErrReasonRequired)
			f.humanEvidence(alice, h, board.Supports, someReason)
			f.sharesTheWalk(h)
			f.retract(release, first)
			f.sharesTheWalk(h)
		}},
		{"TestAliasesCountForTheMergedHypothesis", func(f *fixture) {
			h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
			alias := f.hypothesis(runtime, checkoutDeploy(), f.evidence(runtime, "logs", "search", "NPE", "", ""))
			f.evidence(deps, "metrics", "query", "error rate 12%", alias, board.Supports)
			f.sharesTheWalk(h)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { tc.build(newFixture(t)) })
	}
}

// A hypothesis's proposals are those citing it or one of its aliases, in
// commit order; a proposal citing another hypothesis is not among them.
func TestProposalsAreListedUnderTheirHypothesis(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42 deployed", "", ""))
	alias := f.hypothesis(runtime, checkoutDeploy(), f.evidence(runtime, "logs", "search", "NPE in v42", "", ""))
	other := f.hypothesis(deps, key.Key{"service": "payments", "failureMode": "resource-exhaustion"}, f.evidence(deps, "metrics", "query", "pool 98%", "", ""))

	first, err := f.propose(proposal(release, h, `{"service":"checkout"}`))
	require.NoError(t, err)
	viaAlias, err := f.propose(proposal(runtime, alias, `{"service":"checkout"}`))
	require.NoError(t, err)
	elsewhere, err := f.propose(proposal(deps, other, `{"service":"payments"}`))
	require.NoError(t, err)
	last, err := f.propose(proposal(release, alias, `{"service":"payments"}`))
	require.NoError(t, err)

	assert.Equal(t, []board.EntryID{first.ID, viaAlias.ID, last.ID}, ids(f.state.Proposals(h)), "in commit order, those citing the alias included")
	assert.Equal(t, ids(f.state.Proposals(h)), ids(f.state.Proposals(alias)), "an alias's proposals are its hypothesis's")
	assert.Equal(t, []board.EntryID{elsewhere.ID}, ids(f.state.Proposals(other)))
	assert.Empty(t, f.state.Proposals("hyp-404"), "an id of no hypothesis has none")
}

// SupportBy counts, by source, the distinct entries behind a contributor's
// live supporting records on a hypothesis while its latest stance supports
// it, which is how the proposer's support was obtained (design D6).
func TestSupportByCountsAContributorsCountedSources(t *testing.T) {
	t.Run("by source, while the latest stance supports", func(t *testing.T) {
		f := newFixture(t)
		h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
		f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
		f.evidence(runtime, "logs", "search", "NPE again", h, board.Supports)
		f.evidence(runtime, "logs", "deploys", "v42 at 10:02", h, board.Supports)

		assert.Equal(t, map[board.Source]int{"logs/search": 2, "logs/deploys": 1}, f.state.SupportBy(h, runtime))
		assert.Equal(t, map[board.Source]int{"cd/get_history": 1}, f.state.SupportBy(h, release), "the author's cited evidence")

		f.evidence(runtime, "metrics", "query", "errors started before 10:02", h, board.Refutes)
		assert.Empty(t, f.state.SupportBy(h, runtime), "the latest stance refutes")
		assert.Empty(t, f.state.SupportBy(h, deps), "no stance at all")
	})
	t.Run("an entry behind two records counts once", func(t *testing.T) {
		f := newFixture(t)
		h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
		both := f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
		f.hypothesis(runtime, checkoutDeploy(), both)

		assert.Equal(t, map[board.Source]int{"logs/search": 1}, f.state.SupportBy(h, runtime))
	})
}

// Events is a copy of the board's events in commit order: changing the slice
// it returns changes nothing of the board's.
func TestEventsIsACopy(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	before := f.state.Assess()

	events := f.state.Events()
	require.Equal(t, f.events, events, "the board's events, in commit order")
	events[0].ID = "changed"
	events[2] = board.Event{}

	assert.Equal(t, f.events, f.state.Events())
	assert.Equal(t, before, f.state.Assess())
}
