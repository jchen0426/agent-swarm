package board_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

func TestAgentEvidenceCitesItsOwnReadCall(t *testing.T) {
	f := newFixture(t)
	own := f.call(release, "cd", "get_history", "checkout v42 deployed at 10:02")
	other := f.call(runtime, "logs", "search", "checkout v42 deployed at 10:02")
	failed := f.call(release, "cd", "get_history", "timeout")
	f.calls[failed].Failed = true
	foreign := f.call(release, "cd", "get_history", "checkout v42 deployed at 10:02")
	f.calls[foreign].Run = "run-2"

	tests := []struct {
		name   string
		callID string
		quote  string
		want   error
	}{
		{"unknown call", "call-404", "v42", board.ErrProvenance},
		{"another agent's call", other, "v42", board.ErrProvenance},
		{"a failed call", failed, "timeout", board.ErrProvenance},
		{"another run's call", foreign, "v42", board.ErrProvenance},
		{"quote not in the output", own, "v43", board.ErrProvenance},
		{"empty quote", own, "  ", board.ErrInvalid},
		{"too long a quote", own, strings.Repeat("v", board.MaxQuoteRunes+1), board.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.postEvidence(release, &board.EvidenceInput{Claim: "deployed", CallID: tt.callID, Quote: tt.quote})
			require.ErrorIs(t, err, tt.want)
		})
	}

	d, err := f.postEvidence(release, &board.EvidenceInput{Claim: " deployed ", CallID: own, Quote: "v42  deployed"})
	require.NoError(t, err)
	ev := d.Body.Evidence
	assert.Equal(t, "deployed", ev.Claim)
	assert.Equal(t, board.Provenance{Kind: board.FromReadCall, CallID: own, Quote: "v42 deployed", Source: "cd/get_history"}, ev.Provenance)
}

func TestEvidenceStanceGoesWithAHypothesis(t *testing.T) {
	f := newFixture(t)
	e := f.evidence(release, "cd", "get_history", "v42", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e)
	callID := f.call(runtime, "logs", "search", "NullPointerException")

	tests := []struct {
		name string
		in   board.EvidenceInput
		want error
	}{
		{"stance without hypothesis", board.EvidenceInput{Stance: board.Supports}, board.ErrInvalid},
		{"hypothesis without stance", board.EvidenceInput{Hypothesis: h}, board.ErrInvalid},
		{"unknown hypothesis", board.EvidenceInput{Hypothesis: "hyp-404", Stance: board.Supports}, board.ErrNotFound},
		{"evidence is not a hypothesis", board.EvidenceInput{Hypothesis: e, Stance: board.Supports}, board.ErrInvalid},
		{"unknown stance", board.EvidenceInput{Hypothesis: h, Stance: "maybe"}, board.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			in.Claim, in.CallID, in.Quote = "a claim", callID, "NullPointerException"
			_, err := f.postEvidence(runtime, &in)
			require.ErrorIs(t, err, tt.want)
		})
	}
}

func TestHumanEvidence(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))

	d, err := f.postEvidence(alice, &board.EvidenceInput{Claim: "I rolled it back and errors stopped", Hypothesis: h, Stance: board.Supports})
	require.NoError(t, err)
	assert.Equal(t, board.Provenance{Kind: board.FromHuman, Source: board.HumanObservation}, d.Body.Evidence.Provenance)

	_, err = f.postEvidence(alice, &board.EvidenceInput{Claim: "see this", CallID: "call-1", Quote: "v42"})
	require.ErrorIs(t, err, board.ErrInvalid, "humans cite no read call")

	f.ctx.HumanEntries = false
	_, err = f.postEvidence(alice, &board.EvidenceInput{Claim: "I checked"})
	require.ErrorIs(t, err, board.ErrNotAllowed, "shadow-mode runs take no human entries")
}

func TestHypothesisRules(t *testing.T) {
	f := newFixture(t)
	e := f.evidence(release, "cd", "get_history", "v42", "", "")
	gone := f.evidence(release, "cd", "get_history", "v41", "", "")
	f.retract(release, gone)

	tests := []struct {
		name   string
		author board.Author
		in     board.HypothesisInput
		want   error
	}{
		{"humans post no hypotheses", alice, board.HypothesisInput{Key: checkoutDeploy(), Statement: "s", Evidence: []board.EntryID{e}}, board.ErrNotAllowed},
		{"no evidence", release, board.HypothesisInput{Key: checkoutDeploy(), Statement: "s"}, board.ErrInvalid},
		{"unknown evidence", release, board.HypothesisInput{Key: checkoutDeploy(), Statement: "s", Evidence: []board.EntryID{"ev-404"}}, board.ErrNotFound},
		{"retracted evidence", release, board.HypothesisInput{Key: checkoutDeploy(), Statement: "s", Evidence: []board.EntryID{gone}}, board.ErrInvalid},
		{"empty statement", release, board.HypothesisInput{Key: checkoutDeploy(), Statement: " ", Evidence: []board.EntryID{e}}, board.ErrInvalid},
		{"key outside the schema", release, board.HypothesisInput{Key: key.Key{"service": "search", "failureMode": "bad-deploy"}, Statement: "s", Evidence: []board.EntryID{e}}, key.ErrValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.state.NewHypothesis(&f.ctx, "hyp-x", tt.author, &tt.in)
			require.ErrorIs(t, err, tt.want)
		})
	}

	d, err := f.state.NewHypothesis(&f.ctx, "hyp-x", release, &board.HypothesisInput{Key: checkoutDeploy(), Statement: "bad deploy", Evidence: []board.EntryID{e, e}})
	require.NoError(t, err)
	assert.Equal(t, []board.EntryID{e}, d.Body.Hypothesis.Evidence, "a repeated citation counts once")
}

func TestEqualKeysMerge(t *testing.T) {
	f := newFixture(t)
	first := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	second := f.hypothesis(runtime, checkoutDeploy(), f.evidence(runtime, "logs", "search", "NPE", "", ""))
	unsure := key.Key{"service": "checkout", "failureMode": key.Other}
	f.hypothesis(release, unsure, f.evidence(release, "cd", "get_history", "v40", "", ""))
	notMerged := f.hypothesis(runtime, unsure, f.evidence(runtime, "logs", "search", "warn", "", ""))

	e, ok := f.state.Event(second)
	require.True(t, ok)
	assert.Equal(t, first, e.Body.Hypothesis.AliasOf)
	e, ok = f.state.Event(notMerged)
	require.True(t, ok)
	assert.Empty(t, e.Body.Hypothesis.AliasOf, "keys that contain other never merge")
}

func TestRetraction(t *testing.T) {
	f := newFixture(t)
	e := f.evidence(release, "cd", "get_history", "v42", "", "")

	_, err := f.state.NewRetraction(&f.ctx, "ret-x", runtime, e)
	require.ErrorIs(t, err, board.ErrNotAllowed, "only the author retracts")
	_, err = f.state.NewRetraction(&f.ctx, "ret-x", alice, e)
	require.ErrorIs(t, err, board.ErrNotAllowed, "humans do not retract")

	f.retract(release, e)
	_, ok := f.state.RetractionOf(e)
	assert.True(t, ok)
	_, err = f.state.NewRetraction(&f.ctx, "ret-y", release, e)
	require.ErrorIs(t, err, board.ErrInvalid, "a second retraction is refused; callers treat a repeat as done")
}

func TestRulings(t *testing.T) {
	f := newFixture(t)
	h := f.hypothesis(release, checkoutDeploy(), f.evidence(release, "cd", "get_history", "v42", "", ""))
	ref := f.evidence(runtime, "logs", "search", "no errors after 10:02", h, board.Refutes)
	sup := f.evidence(deps, "metrics", "query", "error rate 12%", h, board.Supports)

	tests := []struct {
		name   string
		author board.Author
		in     board.RulingInput
		want   error
	}{
		{"agents issue no rulings", release, board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason}, board.ErrNotAllowed},
		{"reject needs a reason", alice, board.RulingInput{Kind: board.RulingReject, Hypothesis: h}, board.ErrReasonRequired},
		{"unreject what is not rejected", alice, board.RulingInput{Kind: board.RulingUnreject, Hypothesis: h}, board.ErrInvalid},
		{"dismiss needs a reason", alice, board.RulingInput{Kind: board.RulingDismiss, Refutation: ref}, board.ErrReasonRequired},
		{"dismiss a support", alice, board.RulingInput{Kind: board.RulingDismiss, Refutation: sup, Reason: someReason}, board.ErrInvalid},
		{"unknown ruling", alice, board.RulingInput{Kind: "close", Hypothesis: h}, board.ErrInvalid},
		{"a reason's text needs a code", alice, board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: &board.Reason{Text: "no"}}, board.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.state.NewRuling(&f.ctx, "rul-x", tt.author, &tt.in)
			require.ErrorIs(t, err, tt.want)
		})
	}

	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})
	_, err := f.state.NewRuling(&f.ctx, "rul-y", bob, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})
	require.ErrorIs(t, err, board.ErrInvalid, "already rejected")

	f.ctx.HumanEntries = false
	_, err = f.state.NewRuling(&f.ctx, "rul-z", alice, &board.RulingInput{Kind: board.RulingUnreject, Hypothesis: h})
	require.ErrorIs(t, err, board.ErrNotAllowed, "shadow-mode runs take no rulings")
}

func TestReplayChecksConsistency(t *testing.T) {
	f := newFixture(t)
	e := f.evidence(release, "cd", "get_history", "v42", "", "")
	f.hypothesis(release, checkoutDeploy(), e)
	settings := incidentSettings()

	_, err := board.Replay(settings, f.events)
	require.NoError(t, err)

	swapped := []board.Event{f.events[1], f.events[0]}
	_, err = board.Replay(settings, swapped)
	require.ErrorIs(t, err, board.ErrNotFound, "a hypothesis cannot cite evidence committed after it")

	dup := []board.Event{f.events[0], f.events[0]}
	dup[1].Seq = 2
	_, err = board.Replay(settings, dup)
	require.ErrorIs(t, err, board.ErrInvalid, "duplicate ids")

	backwards := []board.Event{f.events[0], f.events[1]}
	backwards[1].Seq = 1
	_, err = board.Replay(settings, backwards)
	require.ErrorIs(t, err, board.ErrInvalid, "sequence numbers only grow")

	f.hypothesis(runtime, checkoutDeploy(), f.evidence(runtime, "logs", "search", "NPE", "", ""))
	unmerged := append([]board.Event(nil), f.events...)
	last := unmerged[len(unmerged)-1]
	h := *last.Body.Hypothesis
	h.AliasOf = ""
	last.Body.Hypothesis = &h
	unmerged[len(unmerged)-1] = last
	_, err = board.Replay(settings, unmerged)
	require.ErrorIs(t, err, board.ErrInvalid, "an equal mergeable key must merge")
}

func TestClaimLength(t *testing.T) {
	f := newFixture(t)
	callID := f.call(release, "cd", "get_history", "v42")

	_, err := f.postEvidence(release, &board.EvidenceInput{Claim: strings.Repeat("x", board.MaxClaimRunes+1), CallID: callID, Quote: "v42"})
	require.ErrorIs(t, err, board.ErrInvalid)
}
