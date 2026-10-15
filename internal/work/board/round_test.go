package board_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// An agent posts at most the round's limit of evidence, hypotheses, tasks and
// proposals. Repeats, retractions, claims, completions and turn ends do not
// count, and the next round starts again from zero.
func TestTheEntryLimitCountsPostsAndProposals(t *testing.T) {
	f := newFixture(t)
	f.ctx.Settings.Limits = board.Limits{EntriesPerRound: 3}
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	task := f.task(release, "check the error rate")

	in := &board.EvidenceInput{Claim: "again", CallID: f.call(release, "cd", "get_history", "v42 rolled out"), Quote: "v42 rolled out"}
	_, err := f.posted(f.state.PostEvidence(&f.ctx, f.nextID("ev"), release, in, f.calls[in.CallID]))
	require.ErrorIs(t, err, board.ErrLimit)
	_, err = f.propose(proposal(release, h, `{"service":"checkout"}`))
	require.ErrorIs(t, err, board.ErrLimit)

	_, err = f.claim(release, task)
	require.NoError(t, err, "a claim is not an entry that counts")
	f.retract(release, e1)
	again := f.must(f.state.PostTask(&f.ctx, f.nextID("task"), release, &board.TaskInput{Title: "check the error rate"}))
	assert.Equal(t, task, again.ID, "a repeat at the limit returns the earlier entry")

	f.round(2)
	_, err = f.posted(f.state.PostEvidence(&f.ctx, f.nextID("ev"), release, in, f.calls[in.CallID]))
	require.NoError(t, err)
}

// A call repeated after its answer was lost returns the original entry and
// commits nothing (spec Section 5.2).
func TestARepeatedPostReturnsTheOriginalEntry(t *testing.T) {
	f := newFixture(t)
	callID := f.call(release, "cd", "get_history", "checkout v42 deployed at 10:02")
	in := &board.EvidenceInput{Claim: "v42 went out", CallID: callID, Quote: "v42 deployed"}
	first := f.must(f.state.PostEvidence(&f.ctx, f.nextID("ev"), release, in, f.calls[callID]))
	repeat := f.must(f.state.PostEvidence(&f.ctx, f.nextID("ev"), release, &board.EvidenceInput{Claim: " v42 went out ", CallID: callID, Quote: "v42  deployed"}, f.calls[callID]))
	assert.Equal(t, first.ID, repeat.ID, "the same claim and quote, written with other spacing")

	h := f.must(f.state.PostHypothesis(&f.ctx, f.nextID("hyp"), release, &board.HypothesisInput{Key: checkoutDeploy(), Statement: "v42 broke checkout", Evidence: []board.EntryID{first.ID}}))
	hAgain := f.must(f.state.PostHypothesis(&f.ctx, f.nextID("hyp"), release, &board.HypothesisInput{Key: checkoutDeploy(), Statement: "v42 broke checkout", Evidence: []board.EntryID{first.ID, first.ID}}))
	assert.Equal(t, h.ID, hAgain.ID)

	other := f.must(f.state.PostHypothesis(&f.ctx, f.nextID("hyp"), runtime, &board.HypothesisInput{Key: checkoutDeploy(), Statement: "v42 broke checkout", Evidence: []board.EntryID{first.ID}}))
	assert.NotEqual(t, h.ID, other.ID, "another author's post is never a repeat")

	end := f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), release))
	assert.Equal(t, end.ID, f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), release)).ID)
	assert.Len(t, f.events, 4, "no repeat committed anything")
}

// Evidence that was retracted, and a task that was completed, are not
// repeated: posting them again posts anew.
func TestRetractedEvidenceAndCompletedTasksArePostedAnew(t *testing.T) {
	f := newFixture(t)
	callID := f.call(release, "cd", "get_history", "checkout v42 deployed")
	in := &board.EvidenceInput{Claim: "v42 went out", CallID: callID, Quote: "v42 deployed"}
	first := f.must(f.state.PostEvidence(&f.ctx, f.nextID("ev"), release, in, f.calls[callID]))
	retraction := f.must(f.state.Retract(&f.ctx, f.nextID("ret"), release, first.ID))
	assert.Equal(t, retraction.ID, f.must(f.state.Retract(&f.ctx, f.nextID("ret"), release, first.ID)).ID)

	second := f.must(f.state.PostEvidence(&f.ctx, f.nextID("ev"), release, in, f.calls[callID]))
	assert.NotEqual(t, first.ID, second.ID)

	task := f.task(release, "check the pool")
	_, err := f.claim(release, task)
	require.NoError(t, err)
	done, err := f.complete(release, task, second.ID)
	require.NoError(t, err)
	doneAgain, err := f.complete(release, task, second.ID)
	require.NoError(t, err)
	assert.Equal(t, done.ID, doneAgain.ID)
	assert.NotEqual(t, task, f.task(release, "check the pool"))
}

// After an agent ends its turn, it posts nothing more in that round; the
// next round it may.
func TestAnAgentPostsNothingAfterItEndsItsTurn(t *testing.T) {
	f := newFixture(t)
	f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), release))

	_, err := f.posted(f.state.PostTask(&f.ctx, f.nextID("task"), release, &board.TaskInput{Title: "late"}))
	require.ErrorIs(t, err, board.ErrTurnEnded)
	f.task(runtime, "another agent's turn goes on")

	f.round(2)
	f.task(release, "a new round")
}

// In the wrap-up round an agent may only refute the converged hypothesis,
// retract its evidence, propose actions that cite it, and end its turn
// (spec Section 5.6). An alias counts as the hypothesis it merged into.
func TestTheWrapUpRoundAllowsOnlyRefutationsRetractionsAndActions(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	e2 := f.evidence(runtime, "logs", "search", "NPE in v42", "", "")
	alias := f.hypothesis(runtime, checkoutDeploy(), e2)
	e3 := f.evidence(deps, "deps", "versions", "lib 2.1", "", "")
	other := f.hypothesis(deps, map[string]string{"service": "payments", "failureMode": "config-change"}, e3)
	task := f.task(deps, "check payments")
	f.round(2)
	f.ctx.WrapUp = h

	evidence := func(agent board.Author, hypothesis board.EntryID, stance board.Stance) error {
		in := &board.EvidenceInput{Claim: "seen", CallID: f.call(agent, "logs", "search", "line"), Quote: "line", Hypothesis: hypothesis, Stance: stance}
		_, err := f.posted(f.state.PostEvidence(&f.ctx, f.nextID("ev"), agent, in, f.calls[in.CallID]))
		return err
	}
	require.ErrorIs(t, evidence(deps, h, board.Supports), board.ErrWrapUp)
	require.ErrorIs(t, evidence(deps, "", ""), board.ErrWrapUp)
	require.ErrorIs(t, evidence(deps, other, board.Refutes), board.ErrWrapUp)
	_, err := f.posted(f.state.PostHypothesis(&f.ctx, f.nextID("hyp"), deps, &board.HypothesisInput{Key: checkoutDeploy(), Statement: "s", Evidence: []board.EntryID{e3}}))
	require.ErrorIs(t, err, board.ErrWrapUp)
	_, err = f.posted(f.state.PostTask(&f.ctx, f.nextID("task"), deps, &board.TaskInput{Title: "t"}))
	require.ErrorIs(t, err, board.ErrWrapUp)
	_, err = f.claim(release, task)
	require.ErrorIs(t, err, board.ErrWrapUp)
	_, err = f.propose(proposal(deps, other, `{}`))
	require.ErrorIs(t, err, board.ErrWrapUp)

	require.NoError(t, evidence(deps, alias, board.Refutes), "refuting through an alias")
	f.retract(release, e1)
	_, err = f.propose(proposal(runtime, alias, `{}`))
	require.NoError(t, err, "an action that cites the converged hypothesis through an alias")
	f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), deps))
}

// A claim holds from its round through the lease's last round; then it
// lapses, and another agent may claim the task. An agent holds at most the
// open claims the limits allow.
func TestAClaimHoldsThroughItsLease(t *testing.T) {
	f := newFixture(t)
	t1, t2, t3 := f.task(deps, "one"), f.task(deps, "two"), f.task(deps, "three")
	first, err := f.claim(release, t1)
	require.NoError(t, err)
	_, err = f.claim(runtime, t1)
	require.ErrorIs(t, err, board.ErrHeld)
	again, err := f.claim(release, t1)
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID, "claiming a task one holds repeats the claim")

	_, err = f.claim(release, t2)
	require.NoError(t, err)
	_, err = f.claim(release, t3)
	require.ErrorIs(t, err, board.ErrLimit)

	f.round(2)
	_, err = f.claim(runtime, t1)
	require.ErrorIs(t, err, board.ErrHeld, "the lease of 2 rounds covers round 2")
	f.round(3)
	_, err = f.claim(runtime, t1)
	require.NoError(t, err, "the lease lapsed after round 2")
	_, err = f.claim(release, t3)
	require.NoError(t, err, "a lapsed claim no longer counts")
}

// A task is completed by its holder, citing live evidence; never by an agent
// that does not hold it, and never twice.
func TestATaskIsCompletedByItsHolderWithLiveEvidence(t *testing.T) {
	f := newFixture(t)
	task := f.task(deps, "check the pool")
	e := f.evidence(release, "db", "pool", "pool at 100%", "", "")
	retracted := f.evidence(release, "db", "pool", "pool at 99%", "", "")
	f.retract(release, retracted)

	_, err := f.complete(release, task, e)
	require.ErrorIs(t, err, board.ErrNotAllowed, "a task one does not hold")
	_, err = f.claim(release, task)
	require.NoError(t, err)
	_, err = f.complete(release, task)
	require.ErrorIs(t, err, board.ErrInvalid, "a completion cites evidence")
	_, err = f.complete(release, task, retracted)
	require.ErrorIs(t, err, board.ErrInvalid)
	_, err = f.complete(release, task, e)
	require.NoError(t, err)

	_, err = f.complete(runtime, task, e)
	require.ErrorIs(t, err, board.ErrInvalid)
	_, err = f.claim(runtime, task)
	require.ErrorIs(t, err, board.ErrInvalid)
}

// A proposer must itself support the hypothesis it cites with live evidence
// (spec Section 9.2); the rule is checked on the hypothesis an alias merged
// into. A rejected hypothesis takes no proposal.
func TestAProposerMustSupportTheHypothesisItCites(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	e2 := f.evidence(runtime, "logs", "search", "NPE in v42", "", "")
	alias := f.hypothesis(runtime, checkoutDeploy(), e2)
	// deps cites release's evidence: that is no support of its own.
	f.hypothesis(deps, checkoutDeploy(), e1)

	_, err := f.propose(proposal(deps, h, `{}`))
	require.ErrorIs(t, err, board.ErrNotAllowed, "support only through another agent's evidence")
	_, err = f.propose(proposal(runtime, h, `{}`))
	require.NoError(t, err, "support through an alias counts for the hypothesis")
	_, err = f.propose(proposal(release, alias, `{}`))
	require.NoError(t, err, "an alias resolves to the hypothesis it merged into")

	f.evidence(runtime, "logs", "search", "NPE was in v41 too", h, board.Refutes)
	_, err = f.propose(proposal(runtime, h, `{"service":"payments"}`))
	require.ErrorIs(t, err, board.ErrNotAllowed, "the latest stance refutes")

	f.retract(release, e1)
	_, err = f.propose(proposal(release, h, `{"service":"payments"}`))
	require.ErrorIs(t, err, board.ErrNotAllowed, "the only supporting evidence was retracted")

	_, err = f.propose(proposal(release, e2, `{}`))
	require.ErrorIs(t, err, board.ErrInvalid, "evidence is not a hypothesis")
}

// A rejected hypothesis takes no proposal until it is un-rejected.
func TestARejectedHypothesisTakesNoProposal(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})

	_, err := f.state.Proposable(&f.ctx, proposal(release, h, `{}`))
	require.ErrorIs(t, err, board.ErrNotAllowed)

	f.rule(alice, &board.RulingInput{Kind: board.RulingUnreject, Hypothesis: h})
	p, err := f.state.Proposable(&f.ctx, proposal(release, h, `{}`))
	require.NoError(t, err)
	assert.Equal(t, checkoutDeploy(), p.Key)
}

// A proposal that repeats the author's proposal with the same tool,
// arguments and hypothesis returns it, before any read is made for it.
func TestARepeatedProposalReturnsTheFirst(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	first, err := f.propose(proposal(release, h, `{"service":"checkout"}`))
	require.NoError(t, err)

	p, err := f.state.Proposable(&f.ctx, proposal(release, h, `{"service":"checkout"}`))
	require.NoError(t, err)
	require.NotNil(t, p.Earlier)
	assert.Equal(t, first.ID, p.Earlier.ID)
	second, err := f.propose(proposal(release, h, `{"service":"payments"}`))
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID)
}

// One notice per round, agent, tool, code and rule: a refused call that
// loops cannot flood the board.
func TestNoticesAreOnePerRoundAgentToolCodeAndRule(t *testing.T) {
	f := newFixture(t)
	platform := board.Author{Kind: board.AuthorPlatform, Name: "gateway"}
	notify := func(n board.Notice) board.EntryID {
		return f.must(f.state.Notify(&f.ctx, f.nextID("note"), platform, &n)).ID
	}
	first := notify(board.Notice{Code: board.NoticeRefusedCall, Agent: "release-investigator", Tool: "flags__set_flag", CallID: "call-1"})
	assert.Equal(t, first, notify(board.Notice{Code: board.NoticeRefusedCall, Agent: "release-investigator", Tool: "flags__set_flag", CallID: "call-2"}))
	assert.NotEqual(t, first, notify(board.Notice{Code: board.NoticeFailedRead, Agent: "release-investigator", Tool: "flags__set_flag"}))
	assert.NotEqual(t, first, notify(board.Notice{Code: board.NoticeRefusedCall, Agent: "release-investigator", Tool: "flags__set_flag", Rule: "limit"}), "another rule")
	assert.NotEqual(t, first, notify(board.Notice{Code: board.NoticeRefusedCall, Agent: "release-investigator", Tool: "post_evidence", Rule: "limit"}))
	f.round(2)
	assert.NotEqual(t, first, notify(board.Notice{Code: board.NoticeRefusedCall, Agent: "release-investigator", Tool: "flags__set_flag"}))

	_, err := f.posted(f.state.Notify(&f.ctx, f.nextID("note"), release, &board.Notice{Code: board.NoticeRefusedCall, Agent: "x"}))
	require.ErrorIs(t, err, board.ErrNotAllowed, "only the platform posts notices")
}

// The round's rules are checked when an entry is posted, never when a board
// is replayed, so a stored board replays under any settings.
func TestABoardReplaysUnderStricterLimits(t *testing.T) {
	f := newFixture(t)
	for _, title := range []string{"one", "two", "three"} {
		f.task(release, title)
	}
	strict := f.ctx.Settings
	strict.Limits = board.Limits{EntriesPerRound: 1}

	_, err := board.Replay(strict, f.events)

	require.NoError(t, err)
}

func TestReplayRefusesNegativeLimits(t *testing.T) {
	settings := incidentSettings()
	settings.Limits.OpenClaims = -1

	_, err := board.Replay(settings, nil)

	require.ErrorIs(t, err, board.ErrInvalid)
}

// A post repeats an earlier entry only when posting it again would change
// nothing: one that would change its author's stance is a new entry.
func TestARepostThatChangesTheAuthorsStanceIsANewEntry(t *testing.T) {
	f := newFixture(t)
	e0 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e0)
	callID := f.call(runtime, "logs", "search", "NPE in v42")
	support := &board.EvidenceInput{Claim: "NPE in v42", CallID: callID, Quote: "NPE in v42", Hypothesis: h, Stance: board.Supports}
	first := f.must(f.state.PostEvidence(&f.ctx, f.nextID("ev"), runtime, support, f.calls[callID]))
	f.evidence(runtime, "logs", "search", "NPE was in v41 too", h, board.Refutes)

	again := f.must(f.state.PostEvidence(&f.ctx, f.nextID("ev"), runtime, support, f.calls[callID]))

	assert.NotEqual(t, first.ID, again.ID)
	assert.Contains(t, f.state.Assess().Hypotheses[0].Supporters, runtime, "the author supports the hypothesis again")
}

// A hypothesis posted again after its author refuted it supports it anew.
func TestAHypothesisRepostedAfterARefutationSupportsItAgain(t *testing.T) {
	f := newFixture(t)
	e0 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	in := &board.HypothesisInput{Key: checkoutDeploy(), Statement: "v42 broke checkout", Evidence: []board.EntryID{e0}}
	first := f.must(f.state.PostHypothesis(&f.ctx, f.nextID("hyp"), release, in))
	f.evidence(release, "cd", "get_history", "v42 was rolled back at 10:05", first.ID, board.Refutes)

	again := f.must(f.state.PostHypothesis(&f.ctx, f.nextID("hyp"), release, in))

	assert.NotEqual(t, first.ID, again.ID)
	assert.Contains(t, f.state.Assess().Hypotheses[0].Supporters, release)
}

// An agent whose claim lapsed leaves the task to the other agents for one
// round, so that it cannot hold a task for the whole run.
func TestAnAgentWhoseClaimLapsedLeavesTheTaskToOthersForARound(t *testing.T) {
	f := newFixture(t)
	task := f.task(deps, "check the pool")
	_, err := f.claim(release, task)
	require.NoError(t, err)

	f.round(3)
	_, err = f.claim(release, task)
	require.ErrorIs(t, err, board.ErrHeld)
	f.round(4)
	_, err = f.claim(release, task)
	require.NoError(t, err, "nobody took it in round 3")
}

// A hypothesis that cites its author's own refutation of the same
// hypothesis does not make its author a supporter.
func TestARefutationCitedByAHypothesisIsNoSupport(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	refutation := f.evidence(runtime, "logs", "search", "no error in v42", h, board.Refutes)
	f.hypothesis(runtime, checkoutDeploy(), refutation)

	v := f.state.Assess().Hypotheses[0]

	assert.Contains(t, v.Refuters, runtime)
	assert.NotContains(t, v.Supporters, runtime)
}

// A quote that lies within the call's own arguments is text the agent gave
// the call, not text the call answered.
func TestAQuoteOfTheCallsOwnArgumentsIsNoEvidence(t *testing.T) {
	f := newFixture(t)
	echo := f.call(release, "logs", "search", `No results for "deploy 42 caused the outage"`)
	f.calls[echo].Args = []byte(`{"query":"deploy 42 caused the outage"}`)
	hit := f.call(release, "logs", "search", "10:02 ERROR deploy 42 failed its health check")
	f.calls[hit].Args = []byte(`{"query":"deploy 42"}`)

	_, err := f.postEvidence(release, &board.EvidenceInput{Claim: "deploy 42 caused it", CallID: echo, Quote: "deploy 42 caused the outage"})
	require.ErrorIs(t, err, board.ErrProvenance)
	_, err = f.postEvidence(release, &board.EvidenceInput{Claim: "deploy 42 failed", CallID: hit, Quote: "deploy 42 failed its health check"})
	require.NoError(t, err, "a quote longer than the argument it contains is the call's answer")
}

// A proposal is the gateway's entry; its proposer is the agent whose call it
// records, whose entries it counts among.
func TestAProposalIsTheGatewaysEntryForItsProposer(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)

	p, err := f.propose(proposal(release, h, `{}`))

	require.NoError(t, err)
	assert.Equal(t, board.Gateway, p.Author)
	assert.Equal(t, release.Name, p.Body.Proposal.Proposer)
	unnamed := proposal(release, h, `{"service":"payments"}`)
	unnamed.Proposer = ""
	_, err = f.propose(unnamed)
	require.ErrorIs(t, err, board.ErrInvalid)
}

// An agent's calls leave at most MaxNotices notices in a round.
func TestAnAgentsCallsLeaveAtMostTenNoticesARound(t *testing.T) {
	f := newFixture(t)
	for i := range board.MaxNotices + 2 {
		f.must(f.state.Notify(&f.ctx, f.nextID("note"), board.Gateway, &board.Notice{
			Code: board.NoticeRefusedCall, Agent: release.Name, Tool: fmt.Sprintf("tool_%d", i),
		}))
	}

	assert.Len(t, f.events, board.MaxNotices)
	f.must(f.state.Notify(&f.ctx, f.nextID("note"), board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: runtime.Name}))
	assert.Len(t, f.events, board.MaxNotices+1, "another agent's notices are counted apart")
}

// A proposal counts among its proposer's entries of the round, although the
// gateway writes it.
func TestAProposalCountsAmongItsProposersEntries(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.round(2)
	f.ctx.Settings.Limits = board.Limits{EntriesPerRound: 1}
	_, err := f.propose(proposal(release, h, `{}`))
	require.NoError(t, err)

	_, err = f.posted(f.state.PostTask(&f.ctx, f.nextID("task"), release, &board.TaskInput{Title: "one more"}))

	require.ErrorIs(t, err, board.ErrLimit)
}

// A refusal names the hypothesis as the proposer cited it, never the one it
// merged into, which may be another agent's entry of this round.
func TestARefusalNamesTheHypothesisAsCited(t *testing.T) {
	f := newFixture(t)
	f.round(2)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	theirs := f.hypothesis(release, checkoutDeploy(), e1)
	e2 := f.evidence(runtime, "logs", "search", "NPE in v42", "", "")
	mine := f.hypothesis(runtime, checkoutDeploy(), e2)
	f.retract(runtime, e2)

	_, err := f.propose(proposal(runtime, mine, `{}`))

	require.ErrorIs(t, err, board.ErrNotAllowed)
	assert.Contains(t, err.Error(), string(mine))
	assert.NotContains(t, err.Error(), string(theirs))
}

// A quote of a key the agent wrote inside an argument is its own text too.
func TestAQuoteOfAKeyInsideTheArgumentsIsNoEvidence(t *testing.T) {
	f := newFixture(t)
	echo := f.call(release, "logs", "search", `{"matched":{"deploy 42 caused the outage":0}}`)
	f.calls[echo].Args = []byte(`{"filter":{"deploy 42 caused the outage":true}}`)

	_, err := f.postEvidence(release, &board.EvidenceInput{Claim: "deploy 42 caused it", CallID: echo, Quote: "deploy 42 caused the outage"})

	require.ErrorIs(t, err, board.ErrProvenance)
}

// A proposal keeps at most MaxFactValues values of a fact, each JSON of at
// most MaxValueRunes characters.
func TestAProposalsFactsAreBounded(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	for name, fact := range map[string]board.Fact{
		"too many": {Values: []string{`1`, `2`, `3`, `4`, `5`, `6`}},
		"too long": {Values: []string{`"` + strings.Repeat("v", board.MaxValueRunes) + `"`}},
		"not json": {Values: []string{`v42`}},
	} {
		p := proposal(release, h, name)
		p.Facts = map[string]board.Fact{"current": fact}
		_, err := f.propose(p)
		require.ErrorIs(t, err, board.ErrInvalid, name)
	}
}

// Rounds open in order, each with an entry the coordinator writes; the
// latest one gives a context its round, its snapshot and its wrap-up
// hypothesis.
func TestRoundsOpenInOrder(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	start := func(author board.Author, in board.RoundStart) (board.Event, error) {
		return f.posted(f.state.StartRound(&f.ctx, f.nextID("round"), author, &in))
	}

	_, err := start(board.Coordinator, board.RoundStart{Round: 2})
	require.ErrorIs(t, err, board.ErrInvalid, "round 1 comes first")
	_, err = start(release, board.RoundStart{Round: 1})
	require.ErrorIs(t, err, board.ErrNotAllowed, "only the coordinator starts rounds")
	first, err := start(board.Coordinator, board.RoundStart{Round: 1})
	require.NoError(t, err)
	again, err := start(board.Coordinator, board.RoundStart{Round: 1})
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID, "starting the current round again repeats its entry")
	_, err = start(board.Coordinator, board.RoundStart{Round: 2, WrapUp: e1})
	require.ErrorIs(t, err, board.ErrInvalid, "a wrap-up round names a hypothesis")
	wrap, err := start(board.Coordinator, board.RoundStart{Round: 2, WrapUp: h})
	require.NoError(t, err)

	c := board.Context{Round: 1}
	f.state.Opened(&c)
	assert.Equal(t, 2, c.Round)
	assert.Equal(t, wrap.Seq-1, c.Snapshot, "the snapshot holds every entry before the round's")
	assert.Equal(t, h, c.WrapUp)
	assert.Equal(t, 2, wrap.Round)
}
