package board_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

func TestConvergenceNeedsTwoContributorsAndTwoSources(t *testing.T) {
	t.Run("one agent with two sources", func(t *testing.T) {
		f := newFixture(t)
		h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
		f.evidence(release, "metrics", "query", "error rate 12%", h, board.Supports)

		v := f.view(h)
		assert.Len(t, v.Sources, 2)
		assert.False(t, v.Converged)
	})
	t.Run("two agents with one source", func(t *testing.T) {
		f := newFixture(t)
		h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "logs", "search", "NPE", "", ""))
		f.evidence(runtime, "logs", "search", "NPE again", h, board.Supports)

		v := f.view(h)
		assert.Len(t, v.Supporters, 2)
		assert.False(t, v.Converged)
	})
	t.Run("two agents with two sources", func(t *testing.T) {
		f := newFixture(t)
		h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
		f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)

		a := f.state.Assess()
		assert.True(t, f.view(h).Converged)
		assert.Equal(t, h, a.Conclusion)
	})
}

func TestOnlyTheLatestStanceCounts(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	f.evidence(runtime, "logs", "search", "the NPE predates v42", h, board.Refutes)

	v := f.view(h)
	assert.Equal(t, []board.Author{release}, v.Supporters)
	assert.Equal(t, []board.Author{runtime}, v.Refuters)
	assert.Equal(t, 0, v.Score)
}

func TestRetractedEvidenceStopsCounting(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	wrong := f.evidence(runtime, "logs", "search", "no NPE after the rollback", h, board.Refutes)
	require.Equal(t, []board.Author{runtime}, f.view(h).Refuters)

	f.retract(runtime, wrong)

	v := f.view(h)
	assert.Equal(t, []board.Author{release, runtime}, v.Supporters, "the earlier support counts again")
	assert.True(t, v.Converged)
}

func TestHypothesisAuthorSupportNeedsLiveEvidence(t *testing.T) {
	f := newFixture(t)
	e := f.evidence(release, "cd", "get_history", "v42", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e)
	require.Equal(t, []board.Author{release}, f.view(h).Supporters)

	f.retract(release, e)

	assert.Empty(t, f.view(h).Supporters)
}

func TestAgentRefutationStandsUntilOutnumbered(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	ref := f.evidence(deps, "metrics", "query", "errors started before 10:02", h, board.Refutes)

	v := f.view(h)
	assert.Equal(t, []board.EntryID{ref}, v.StandingRefutations)
	assert.False(t, v.Converged)

	f.evidence(board.Author{Kind: board.AuthorAgent, Name: "network-investigator"}, "metrics", "query", "no packet loss", h, board.Supports)

	v = f.view(h)
	assert.Empty(t, v.StandingRefutations, "3 supporters against 1 refuter reaches the margin of 2")
	assert.True(t, v.Converged)
}

func TestHumanRefutationStandsUntilDismissed(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	f.evidence(deps, "metrics", "query", "error rate 12%", h, board.Supports)
	f.evidence(board.Author{Kind: board.AuthorAgent, Name: "network-investigator"}, "metrics", "query", "no packet loss", h, board.Supports)
	ref := f.humanEvidence(alice, h, board.Refutes, nil)

	assert.Equal(t, []board.EntryID{ref}, f.view(h).StandingRefutations, "no margin outweighs a human")

	f.rule(bob, &board.RulingInput{Kind: board.RulingDismiss, Refutation: ref, Reason: someReason})

	assert.True(t, f.view(h).Converged)
}

func TestHumanSupportThatLiftsARefutationIsADismissal(t *testing.T) {
	f := newFixture(t)
	first := f.evidence(release, "cd", "get_history", "v42", "", "")
	h := f.hypothesis(release, checkoutDeploy(), first)
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	ref := f.evidence(deps, "metrics", "query", "errors started before 10:02", h, board.Refutes)

	_, err := f.postEvidence(alice, &board.EvidenceInput{Claim: "rollback fixed it", Hypothesis: h, Stance: board.Supports})
	require.ErrorIs(t, err, board.ErrReasonRequired)

	support := f.humanEvidence(alice, h, board.Supports, someReason)
	e, _ := f.state.Event(support)
	assert.Equal(t, []board.EntryID{ref}, e.Body.Evidence.Dismisses)
	assert.True(t, f.view(h).Converged)

	f.retract(release, first)

	v := f.view(h)
	assert.Equal(t, []board.Author{runtime, alice}, v.Supporters, "agents sort before humans")
	assert.Empty(t, v.StandingRefutations, "the dismissal outlives the margin that caused it")
	assert.True(t, v.Converged)
}

func TestRejectedHypothesisCannotConverge(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	require.True(t, f.view(h).Converged)

	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})

	a := f.state.Assess()
	assert.False(t, f.view(h).Converged)
	assert.Empty(t, a.Conclusion, "a rejected hypothesis is no conclusion")

	f.rule(alice, &board.RulingInput{Kind: board.RulingUnreject, Hypothesis: h})

	assert.True(t, f.view(h).Converged)
}

func TestRejectingAnAliasRejectsTheMergedHypothesis(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	alias := f.hypothesis(runtime, checkoutDeploy(), f.evidence(runtime, "logs", "search", "NPE", "", ""))

	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: alias, Reason: someReason})

	assert.True(t, f.view(h).Rejected)
}

func TestConclusionWithoutConvergence(t *testing.T) {
	t.Run("the leading hypothesis", func(t *testing.T) {
		f := newFixture(t)
		lead := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
		f.evidence(runtime, "cd", "get_history", "v42 again", lead, board.Supports)
		f.hypothesis(deps, key.Key{"service": "payments", "failureMode": "resource-exhaustion"}, f.evidence(deps, "metrics", "query", "pool 98%", "", ""))

		a := f.state.Assess()
		assert.False(t, a.Hypotheses[0].Converged)
		assert.Equal(t, lead, a.Hypotheses[0].ID)
		assert.Equal(t, lead, a.Conclusion)
	})
	t.Run("a tie means no conclusion", func(t *testing.T) {
		f := newFixture(t)
		f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
		f.hypothesis(deps, key.Key{"service": "payments", "failureMode": "resource-exhaustion"}, f.evidence(deps, "metrics", "query", "pool 98%", "", ""))

		assert.Empty(t, f.state.Assess().Conclusion)
	})
	t.Run("no supporter means no conclusion", func(t *testing.T) {
		f := newFixture(t)
		e := f.evidence(release, "cd", "get_history", "v42", "", "")
		h := f.hypothesis(release, checkoutDeploy(), e)
		f.retract(release, e)
		f.evidence(runtime, "logs", "search", "no NPE", h, board.Refutes)

		a := f.state.Assess()
		assert.Equal(t, -1, a.Hypotheses[0].Score)
		assert.Empty(t, a.Conclusion)
	})
}

func TestAssessAsOfAMoment(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	cutoff := f.now
	f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)

	early, err := board.Replay(incidentSettings(), board.AsOf(f.events, cutoff))
	require.NoError(t, err)
	late, err := board.Replay(incidentSettings(), f.events)
	require.NoError(t, err)

	assert.False(t, early.Assess().Hypotheses[0].Converged)
	assert.Equal(t, h, early.Assess().Conclusion, "the leading hypothesis before convergence")
	assert.True(t, late.Assess().Hypotheses[0].Converged)
}

func (f *fixture) humanEvidence(human board.Author, h board.EntryID, stance board.Stance, reason *board.Reason) board.EntryID {
	f.t.Helper()
	d, err := f.postEvidence(human, &board.EvidenceInput{Claim: "I checked", Hypothesis: h, Stance: stance, Reason: reason})
	require.NoError(f.t, err)
	return f.commit(&d).ID
}

// view returns the assessment's view of hypothesis h.
func (f *fixture) view(h board.EntryID) board.HypothesisView {
	f.t.Helper()
	views := f.state.Assess().Hypotheses
	for i := range views {
		if views[i].ID == h {
			return views[i]
		}
	}
	f.t.Fatalf("hypothesis %s is not on the board", h)
	return board.HypothesisView{}
}

func TestAliasesCountForTheMergedHypothesis(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	alias := f.hypothesis(runtime, checkoutDeploy(), f.evidence(runtime, "logs", "search", "NPE", "", ""))
	f.evidence(deps, "metrics", "query", "error rate 12%", alias, board.Supports)

	v := f.view(h)
	assert.Equal(t, []board.EntryID{alias}, v.Aliases)
	assert.Equal(t, []board.Author{deps, release, runtime}, v.Supporters, "the alias's author and evidence citing the alias both count")
	require.Len(t, f.state.Assess().Hypotheses, 1, "an alias is not ranked on its own")
}

func TestANewAliasOfARejectedHypothesisStaysRejected(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})
	f.hypothesis(runtime, checkoutDeploy(), f.evidence(runtime, "logs", "search", "NPE", "", ""))

	v := f.view(h)
	assert.True(t, v.Rejected)
	assert.False(t, v.Converged, "posting a rejected claim again does not escape the rejection")
	assert.Empty(t, f.state.Assess().Conclusion)
}

func TestAsOfIncludesAnEntryCommittedAtTheCutoff(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	support := f.evidence(runtime, "logs", "search", "NPE", h, board.Supports)
	e, ok := f.state.Event(support)
	require.True(t, ok)

	assert.Len(t, board.AsOf(f.events, e.At), len(f.events))
	assert.Len(t, board.AsOf(f.events, e.At.In(time.FixedZone("CEST", 2*60*60))), len(f.events), "a zone changes the notation, not the moment")
	assert.Len(t, board.AsOf(f.events, e.At.Add(-time.Nanosecond)), len(f.events)-1)
}

func TestReplayAsOfChecksTheWholeLog(t *testing.T) {
	f := newFixture(t)
	f.evidence(release, "cd", "get_history", "v42", "", "")
	f.evidence(runtime, "logs", "search", "NPE", "", "")
	f.evidence(deps, "metrics", "query", "pool 98%", "", "")
	first := f.events[0].At
	skewed := append([]board.Event(nil), f.events...)
	skewed[2].At = first.Add(-time.Minute) // committed last, dated before the first entry

	// Cutting first would keep nothing and never see the bad order.
	require.Empty(t, board.AsOf(skewed, first.Add(-30*time.Second)))
	_, err := board.ReplayAsOf(incidentSettings(), skewed, first.Add(-30*time.Second))
	require.ErrorIs(t, err, board.ErrInvalid)

	s, err := board.ReplayAsOf(incidentSettings(), f.events, first)
	require.NoError(t, err)
	_, ok := s.Event(f.events[1].ID)
	assert.False(t, ok, "an entry after the cutoff is checked, not counted")
}

func TestConclusionFollowsTheSpecsRanking(t *testing.T) {
	network := board.Author{Kind: board.AuthorAgent, Name: "network-investigator"}
	payments := key.Key{"service": "payments", "failureMode": "resource-exhaustion"}

	t.Run("an unsupported leader means no conclusion", func(t *testing.T) {
		f := newFixture(t)
		gone := f.evidence(release, "cd", "get_history", "v42", "", "")
		lead := f.hypothesis(release, checkoutDeploy(), gone)
		f.retract(release, gone)
		weak := f.hypothesis(deps, payments, f.evidence(deps, "metrics", "query", "pool 98%", "", ""))
		f.evidence(runtime, "logs", "search", "pool fine", weak, board.Refutes)
		f.evidence(network, "metrics", "query", "no saturation", weak, board.Refutes)

		a := f.state.Assess()
		require.Equal(t, lead, a.Hypotheses[0].ID, "score 0 ranks above score -1")
		assert.Empty(t, a.Conclusion, "the highest-scoring hypothesis has no supporter, so the run concludes nothing")
	})
	t.Run("a supported leader concludes even with a negative score", func(t *testing.T) {
		f := newFixture(t)
		h := f.hypothesis(deps, payments, f.evidence(deps, "metrics", "query", "pool 98%", "", ""))
		f.evidence(runtime, "logs", "search", "pool fine", h, board.Refutes)
		f.evidence(network, "metrics", "query", "no saturation", h, board.Refutes)

		a := f.state.Assess()
		assert.Equal(t, -1, a.Hypotheses[0].Score)
		assert.Equal(t, h, a.Conclusion)
	})
}
