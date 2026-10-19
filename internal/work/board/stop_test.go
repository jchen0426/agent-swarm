package board_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// stopInput is how the coordinator stops the fixture's run.
func stopInput() *board.StopInput {
	return &board.StopInput{Reason: "quiet", Rounds: 2, WallClock: 3 * time.Minute, Tokens: 1234}
}

// The stop entry sums up how the run ended in ids and numbers: the
// conclusion the coordinator recorded, the hypotheses by score, the tasks
// left open and the proposals made, with the budget the coordinator says the
// run used.
func TestTheStopEntrySumsUpTheRun(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.evidence(runtime, "logs", "search", "NullPointerException", h, board.Supports)
	open := f.task(release, "check the pool")
	done := f.task(runtime, "check the deploy")
	_, err := f.claim(runtime, done)
	require.NoError(t, err)
	_, err = f.complete(runtime, done, e1)
	require.NoError(t, err)
	p, err := f.propose(proposal(release, h, `{"service":"checkout"}`))
	require.NoError(t, err)

	in := stopInput()
	in.Conclusion = h
	stop := f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, in))

	assert.Equal(t, board.Coordinator, stop.Author)
	assert.Equal(t, f.ctx.Round, stop.Round)
	assert.Equal(t, &board.Stop{
		Reason: "quiet", Conclusion: h, Hypotheses: []board.EntryID{h}, OpenTasks: []board.EntryID{open},
		Proposals: []board.EntryID{p.ID}, Rounds: 2, WallClock: 3 * time.Minute, Tokens: 1234,
	}, stop.Body.Stop)
	got, stopped := f.state.Stopped()
	require.True(t, stopped)
	assert.Equal(t, stop.ID, got.ID)
}

// The stop entry holds the conclusion the coordinator recorded when it
// decided to stop, not the board's as it stands: a ruling between the two
// cannot make the board and the record disagree. A conclusion that names no
// hypothesis of the board is refused.
func TestTheStopEntryHoldsTheRecordedConclusion(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.evidence(runtime, "logs", "search", "NullPointerException", h, board.Supports)
	require.Equal(t, h, f.state.Assess().Conclusion)
	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})
	require.Empty(t, f.state.Assess().Conclusion)

	for name, in := range map[string]*board.StopInput{
		"not a hypothesis": {Reason: "converged", Conclusion: e1},
		"no such entry":    {Reason: "converged", Conclusion: "e-99"},
	} {
		_, err := f.posted(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, in))
		require.ErrorIs(t, err, board.ErrInvalid, name)
	}
	stop := f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, &board.StopInput{Reason: "converged", Conclusion: h}))

	assert.Equal(t, h, stop.Body.Stop.Conclusion)
	_, err := board.Replay(f.ctx.Settings, f.events)
	require.NoError(t, err)
}

// A write call is refused before any read is made for it once the run
// stops, as its proposal would be.
func TestAWriteCallIsNotProposableOnceTheRunStops(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, stopInput()))

	_, err := f.state.Proposable(&f.ctx, proposal(release, h, `{"service":"checkout"}`))

	require.ErrorIs(t, err, board.ErrStopped)
}

// Once the run stops, its board takes rulings only: no agent's post, no
// proposal, no notice, no round, no platform evidence and no human evidence.
// Stopping it again repeats the stop entry.
func TestAStoppedBoardTakesRulingsOnly(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.evidence(runtime, "logs", "search", "NullPointerException", h, board.Supports)
	task := f.task(release, "check the pool")
	stop := f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, stopInput()))
	callID := f.call(deps, "deps", "list", "redis 7.2")

	for name, post := range map[string]func() (board.Posting, error){
		"evidence": func() (board.Posting, error) {
			return f.state.PostEvidence(&f.ctx, f.nextID("ev"), deps, &board.EvidenceInput{Claim: "redis", CallID: callID, Quote: "redis 7.2"}, f.calls[callID])
		},
		"a hypothesis": func() (board.Posting, error) {
			return f.state.PostHypothesis(&f.ctx, f.nextID("hyp"), deps, &board.HypothesisInput{Key: checkoutDeploy(), Statement: "v42", Evidence: []board.EntryID{e1}})
		},
		"a retraction": func() (board.Posting, error) { return f.state.Retract(&f.ctx, f.nextID("ret"), release, e1) },
		"a task": func() (board.Posting, error) {
			return f.state.PostTask(&f.ctx, f.nextID("task"), deps, &board.TaskInput{Title: "t"})
		},
		"a claim":    func() (board.Posting, error) { return f.state.Claim(&f.ctx, f.nextID("claim"), deps, task) },
		"a turn end": func() (board.Posting, error) { return f.state.EndTurn(&f.ctx, f.nextID("end"), deps) },
		"a proposal": func() (board.Posting, error) {
			return f.state.Propose(&f.ctx, f.nextID("prop"), proposal(release, h, `{"service":"checkout"}`))
		},
		"a notice": func() (board.Posting, error) {
			return f.state.Notify(&f.ctx, f.nextID("n"), board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: deps.Name})
		},
		"a round": func() (board.Posting, error) {
			return f.state.StartRound(&f.ctx, f.nextID("round"), board.Coordinator, &board.RoundStart{Round: 1})
		},
		"platform evidence": func() (board.Posting, error) {
			return f.state.PostPlatformEvidence(&f.ctx, f.nextID("pe"), board.Coordinator, &board.PlatformEvidenceInput{Claim: "resolved", Ref: "rec-9", Source: "alertmanager"})
		},
		"human evidence": func() (board.Posting, error) {
			d, err := f.state.NewEvidence(&f.ctx, f.nextID("ev"), alice, &board.EvidenceInput{Claim: "seen"}, nil)
			return board.Posting{Draft: &d}, err
		},
	} {
		_, err := f.posted(post())

		require.ErrorIs(t, err, board.ErrStopped, name)
	}
	f.rule(alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: h, Reason: someReason})
	again := f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, &board.StopInput{Reason: "cut"}))
	assert.Equal(t, stop.ID, again.ID)
	_, err := board.Replay(f.ctx.Settings, f.events)
	require.NoError(t, err, "a ruling after the stop replays")
}

// Only the coordinator stops a run, with a reason written as a code.
func TestOnlyTheCoordinatorStopsARun(t *testing.T) {
	f := newFixture(t)

	_, err := f.posted(f.state.Stop(&f.ctx, f.nextID("stop"), release, stopInput()))
	require.ErrorIs(t, err, board.ErrNotAllowed)
	for _, reason := range []string{"", "Quiet", "the run went quiet", strings.Repeat("q", 33)} {
		_, err = f.posted(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, &board.StopInput{Reason: reason}))
		require.ErrorIs(t, err, board.ErrInvalid, reason)
	}
}

// The platform posts evidence that names the record behind it and its
// source, and posts it once per record: posting it again repeats it.
func TestPlatformEvidenceNamesItsRecordOnce(t *testing.T) {
	f := newFixture(t)
	in := &board.PlatformEvidenceInput{Claim: "Opened by alertmanager: checkout errors above 5%", Ref: "rec-1", Source: "alertmanager"}

	first := f.must(f.state.PostPlatformEvidence(&f.ctx, f.nextID("pe"), board.Coordinator, in))
	again := f.must(f.state.PostPlatformEvidence(&f.ctx, f.nextID("pe"), board.Coordinator, &board.PlatformEvidenceInput{Claim: "other", Ref: "rec-1", Source: "alertmanager"}))

	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, &board.Evidence{Claim: in.Claim, Provenance: board.Provenance{Kind: board.FromPlatform, Ref: "rec-1", Source: "alertmanager"}}, first.Body.Evidence)
	for name, c := range map[string]struct {
		author board.Author
		in     board.PlatformEvidenceInput
		err    error
	}{
		"an agent":     {release, board.PlatformEvidenceInput{Claim: "x", Ref: "rec-2", Source: "s"}, board.ErrNotAllowed},
		"a person":     {alice, board.PlatformEvidenceInput{Claim: "x", Ref: "rec-2", Source: "s"}, board.ErrNotAllowed},
		"no claim":     {board.Coordinator, board.PlatformEvidenceInput{Claim: " ", Ref: "rec-2", Source: "s"}, board.ErrInvalid},
		"a long claim": {board.Coordinator, board.PlatformEvidenceInput{Claim: strings.Repeat("x", board.MaxClaimRunes+1), Ref: "rec-2", Source: "s"}, board.ErrInvalid},
		"no record":    {board.Coordinator, board.PlatformEvidenceInput{Claim: "x", Source: "s"}, board.ErrProvenance},
		"no source":    {board.Coordinator, board.PlatformEvidenceInput{Claim: "x", Ref: "rec-2"}, board.ErrProvenance},
	} {
		_, err := f.posted(f.state.PostPlatformEvidence(&f.ctx, f.nextID("pe"), c.author, &c.in))

		require.ErrorIs(t, err, c.err, name)
	}
}

// A notice of a model call names the call, and each call has its own
// notice; a notice that no agent is left names nothing.
func TestModelNoticesAreOnePerCall(t *testing.T) {
	f := newFixture(t)
	failed := func(call string) *board.Notice {
		return &board.Notice{Code: board.NoticeModelFailed, Agent: release.Name, CallID: call}
	}

	first := f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, failed("m-1")))
	second := f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, failed("m-2")))
	again := f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, failed("m-1")))
	none := f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, &board.Notice{Code: board.NoticeNoAgents}))
	f.round(2)
	later := f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, &board.Notice{Code: board.NoticeModelRefused, Agent: release.Name, CallID: "m-1"}))

	assert.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, first.ID, later.ID, "a call has one notice, in any round")
	assert.Equal(t, board.NoticeNoAgents, none.Body.Notice.Code)
	for name, n := range map[string]*board.Notice{
		"a model notice without its call": {Code: board.NoticeModelRefused, Agent: release.Name},
		"a model notice without an agent": {Code: board.NoticeModelUnknown, CallID: "m-3"},
		"no agents, naming one":           {Code: board.NoticeNoAgents, Agent: release.Name},
		"an unknown code":                 {Code: "model_slow", Agent: release.Name, CallID: "m-3"},
	} {
		_, err := f.posted(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, n))

		require.ErrorIs(t, err, board.ErrInvalid, name)
	}
}

// The gateway's notices of an agent's calls cannot crowd out the notice of
// its model call: the cap of notices in a round counts the gateway's only,
// and a turn ends at its first failed model call (Plan 7d), which bounds the
// model's notices.
func TestAModelCallsNoticeIsNeverCrowdedOut(t *testing.T) {
	f := newFixture(t)
	for i := range board.MaxNotices + 1 {
		f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: release.Name, Tool: fmt.Sprintf("tool_%d", i)}))
	}
	before := len(f.events)

	n := f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, &board.Notice{Code: board.NoticeModelFailed, Agent: release.Name, CallID: "m-1"}))

	assert.Len(t, f.events, before+1)
	assert.Equal(t, "m-1", n.Body.Notice.CallID)
	report := f.state.Report(f.ctx.Round)
	assert.True(t, report.Noticed("m-1"))
}

// A round's report says what the round produced, as the coordinator reads it
// when the round ends: the agents that ended their turn, and how many entries
// count as activity. Notices, turn ends, round entries, platform evidence
// and the stop entry do not; a proposal does, for its proposer. It also
// names the model calls the board holds a notice of, in any round.
func TestARoundsReportSaysWhatTheRoundProduced(t *testing.T) {
	f := newFixture(t)
	f.must(f.state.PostPlatformEvidence(&f.ctx, f.nextID("pe"), board.Coordinator, &board.PlatformEvidenceInput{Claim: "opened", Ref: "rec-1", Source: "person"}))
	f.must(f.state.StartRound(&f.ctx, f.nextID("round"), board.Coordinator, &board.RoundStart{Round: 1}))
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), release))
	f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, &board.Notice{Code: board.NoticeModelFailed, Agent: runtime.Name, CallID: "m-1"}))
	f.round(2)
	f.must(f.state.StartRound(&f.ctx, f.nextID("round"), board.Coordinator, &board.RoundStart{Round: 2}))
	f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), runtime))
	f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), release))
	f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: release.Name}))
	f.round(3)
	f.must(f.state.StartRound(&f.ctx, f.nextID("round"), board.Coordinator, &board.RoundStart{Round: 3}))
	_, err := f.propose(proposal(release, h, `{"service":"checkout"}`))
	require.NoError(t, err)
	f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, stopInput()))

	one, two, three := f.state.Report(1), f.state.Report(2), f.state.Report(3)

	assert.Equal(t, board.RoundFacts{Ended: []string{release.Name}, Entries: 2}, one.Facts)
	assert.Equal(t, board.RoundFacts{Ended: []string{release.Name, runtime.Name}}, two.Facts)
	assert.Equal(t, board.RoundFacts{Ended: []string{}, Entries: 1}, three.Facts)
	assert.Equal(t, h, one.Assessment.Hypotheses[0].ID)
	assert.True(t, three.Noticed("m-1"))
	assert.False(t, three.Noticed("m-2"))
	assert.True(t, three.Stopped)
}
