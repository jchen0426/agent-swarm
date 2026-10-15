package board_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

func ids(events []board.Event) []board.EntryID {
	out := []board.EntryID{}
	for i := range events {
		out = append(out, events[i].ID)
	}
	return out
}

// An agent sees the snapshot frozen when its round started, and its own
// entries since; other authors' entries of the round appear in the next
// round's snapshot (spec Section 5.2).
func TestAViewShowsTheSnapshotAndTheAgentsOwnEntries(t *testing.T) {
	f := newFixture(t)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.round(2)
	mine := f.evidence(runtime, "logs", "search", "NPE in v42", h, board.Supports)
	theirs := f.evidence(deps, "deps", "versions", "lib 2.1", h, board.Refutes)
	f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), runtime))

	v, err := f.state.View(&f.ctx, runtime.Name, 0)
	require.NoError(t, err)

	assert.Equal(t, []board.EntryID{e1, h}, ids(v.Entries))
	assert.Equal(t, []board.EntryID{mine}, ids(v.Own), "own entries after the snapshot, without the turn end")
	require.Len(t, v.Assessment.Hypotheses, 1)
	assert.Empty(t, v.Assessment.Hypotheses[0].Refuters, "the snapshot's assessment, without this round's refutation")
	assert.NotContains(t, ids(v.Own), theirs)

	f.round(3)
	v, err = f.state.View(&f.ctx, runtime.Name, 2)
	require.NoError(t, err)
	assert.Equal(t, []board.EntryID{mine, theirs}, ids(v.Entries), "the next round's snapshot, after the cursor")
	assert.Empty(t, v.Own)
}

// A hypothesis that merged into another agent's hypothesis of the same round
// does not name it: the merge would show that entry a round early.
func TestAMergeIntoAnEntryNotYetSeenIsNotNamed(t *testing.T) {
	f := newFixture(t)
	f.round(2)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	theirs := f.hypothesis(release, checkoutDeploy(), e1)
	e2 := f.evidence(runtime, "logs", "search", "NPE in v42", "", "")
	mine := f.hypothesis(runtime, checkoutDeploy(), e2)

	v, err := f.state.View(&f.ctx, runtime.Name, 0)
	require.NoError(t, err)

	require.Len(t, v.Own, 2)
	assert.Equal(t, mine, v.Own[1].ID)
	assert.Empty(t, v.Own[1].Body.Hypothesis.AliasOf)
	stored, _ := f.state.Event(mine)
	assert.Equal(t, theirs, stored.Body.Hypothesis.AliasOf, "the board itself keeps the merge")

	f.round(3)
	v, err = f.state.View(&f.ctx, runtime.Name, 0)
	require.NoError(t, err)
	assert.Equal(t, theirs, v.Entries[3].Body.Hypothesis.AliasOf, "once in the snapshot, the merge shows")
}

// A view shows the snapshot's tasks with who holds each in the round.
func TestAViewShowsWhoHoldsEachTask(t *testing.T) {
	f := newFixture(t)
	held, open := f.task(deps, "check the pool"), f.task(deps, "check the cache")
	_, err := f.claim(release, held)
	require.NoError(t, err)
	f.round(2)

	v, err := f.state.View(&f.ctx, runtime.Name, 0)
	require.NoError(t, err)

	require.Len(t, v.Tasks, 2)
	assert.Equal(t, board.TaskView{ID: held, Title: "check the pool", Tags: []string{}, Author: deps, Holder: release.Name, Until: 2}, v.Tasks[0])
	assert.Equal(t, open, v.Tasks[1].ID)
	assert.Empty(t, v.Tasks[1].Holder)
}

// An agent's view shows its own notices and the proposals recorded for it,
// and no other agent's notices.
func TestAViewShowsTheAgentsOwnNoticesAndProposals(t *testing.T) {
	f := newFixture(t)
	f.must(f.state.Notify(&f.ctx, f.nextID("note"), board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: release.Name}))
	theirs := f.must(f.state.Notify(&f.ctx, f.nextID("note"), board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: runtime.Name}))
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.round(2)
	p, err := f.propose(proposal(release, h, `{}`))
	require.NoError(t, err)

	v, err := f.state.View(&f.ctx, release.Name, 0)

	require.NoError(t, err)
	assert.NotContains(t, ids(v.Entries), theirs.ID)
	assert.Len(t, v.Entries, 3)
	assert.Equal(t, []board.EntryID{p.ID}, ids(v.Own))
}
