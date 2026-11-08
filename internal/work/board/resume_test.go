package board_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

func resumeInput() *board.ResumeInput {
	return &board.ResumeInput{Cause: "extension", Rounds: 2, WallClock: 5 * time.Minute, Tokens: 500}
}

// startRound opens round n the way the coordinator does.
func (f *fixture) startRound(n int) board.Event {
	f.t.Helper()
	e := f.must(f.state.StartRound(&f.ctx, f.nextID("round"), board.Coordinator, &board.RoundStart{Round: n}))
	f.round(n)
	return e
}

func (f *fixture) stop() board.Event {
	f.t.Helper()
	return f.must(f.state.Stop(&f.ctx, f.nextID("stop"), board.Coordinator, stopInput()))
}

func (f *fixture) resume() board.Event {
	f.t.Helper()
	return f.must(f.state.Resume(&f.ctx, f.nextID("resume"), board.Coordinator, resumeInput()))
}

// A resume entry from the coordinator lifts the stop: the board takes
// evidence, the next round entry and a turn end again.
func TestAResumeEntryLiftsTheStop(t *testing.T) {
	f := newFixture(t)
	f.startRound(1)
	f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	f.stop()
	_, stopped := f.state.Stopped()
	require.True(t, stopped)

	r := f.resume()

	assert.Equal(t, &board.Resume{Cause: "extension", Rounds: 2, WallClock: 5 * time.Minute, Tokens: 500}, r.Body.Resume)
	assert.Equal(t, board.KindResume, r.Body.Kind())
	_, stopped = f.state.Stopped()
	assert.False(t, stopped)
	rep := f.state.Report(1)
	assert.False(t, rep.Stopped)
	f.startRound(2)
	f.evidence(runtime, "logs", "search", "NPE in v42", "", "")
	f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), runtime))
}

// A board resumes only after a stop, and only the coordinator resumes it.
func TestABoardResumesOnlyAfterAStop(t *testing.T) {
	f := newFixture(t)
	f.startRound(1)

	_, err := f.state.Resume(&f.ctx, f.nextID("resume"), board.Coordinator, resumeInput())
	require.ErrorIs(t, err, board.ErrInvalid, "a board never stopped")

	f.stop()
	_, err = f.state.Resume(&f.ctx, f.nextID("resume"), release, resumeInput())
	require.ErrorIs(t, err, board.ErrNotAllowed, "an agent")
	_, err = f.state.Resume(&f.ctx, f.nextID("resume"), alice, resumeInput())
	require.ErrorIs(t, err, board.ErrNotAllowed, "a person")
	_, stopped := f.state.Stopped()
	assert.True(t, stopped)

	bad := resumeInput()
	bad.Cause = "Free text"
	_, err = f.state.Resume(&f.ctx, f.nextID("resume"), board.Coordinator, bad)
	require.ErrorIs(t, err, board.ErrInvalid, "a cause this build does not know")
	bad = resumeInput()
	bad.Rounds = -1
	_, err = f.state.Resume(&f.ctx, f.nextID("resume"), board.Coordinator, bad)
	require.ErrorIs(t, err, board.ErrInvalid, "a negative amount")
}

// A second resume with no stop between repeats the first and posts nothing.
func TestAResumeRepeatedReturnsItsEntry(t *testing.T) {
	f := newFixture(t)
	f.startRound(1)
	f.stop()
	first := f.resume()
	n := len(f.events)

	again := f.must(f.state.Resume(&f.ctx, f.nextID("resume"), board.Coordinator, resumeInput()))

	assert.Equal(t, first.ID, again.ID)
	assert.Len(t, f.events, n)
}

// After a resume, Stop posts a new stop entry, not a repeat of the first.
func TestAResumedBoardTakesASecondStopEntry(t *testing.T) {
	f := newFixture(t)
	f.startRound(1)
	first := f.stop()
	f.resume()

	second := f.stop()

	assert.NotEqual(t, first.ID, second.ID)
	got, stopped := f.state.Stopped()
	require.True(t, stopped)
	assert.Equal(t, second.ID, got.ID)
	_, ok := f.state.Event(first.ID)
	assert.True(t, ok, "the first stop entry stays")

	// A new resume is taken for the second stop, not a repeat of the first.
	third := f.resume()
	assert.Len(t, f.events, 5)
	assert.Equal(t, board.KindResume, third.Body.Kind())
}

// Rounds after a resume follow the last: a resume after round 3 takes round 4.
func TestRoundsAfterAResumeFollowTheLast(t *testing.T) {
	f := newFixture(t)
	f.startRound(1)
	f.startRound(2)
	f.startRound(3)
	f.stop()
	f.resume()

	n := len(f.events)
	// Round 3 is the latest the board holds: posting it again repeats it
	// (R68), and nothing is written. Round 5 skips one and is refused.
	again, err := f.posted(f.state.StartRound(&f.ctx, f.nextID("round"), board.Coordinator, &board.RoundStart{Round: 3}))
	require.NoError(t, err)
	assert.Equal(t, 3, again.Body.Round.Round)
	assert.Len(t, f.events, n)
	_, err = f.state.StartRound(&f.ctx, f.nextID("round"), board.Coordinator, &board.RoundStart{Round: 5})
	require.ErrorIs(t, err, board.ErrInvalid)
	_, err = f.state.StartRound(&f.ctx, f.nextID("round"), board.Coordinator, &board.RoundStart{Round: 2})
	require.ErrorIs(t, err, board.ErrInvalid)
	e := f.startRound(4)
	assert.Equal(t, 4, e.Body.Round.Round)
}

// A proposal after a resume is taken: the resume sets the stop aside (D29).
func TestAProposalAfterAResumeIsTaken(t *testing.T) {
	f := newFixture(t)
	f.startRound(1)
	e1 := f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	h := f.hypothesis(release, checkoutDeploy(), e1)
	f.stop()
	_, err := f.propose(proposal(release, h, `{"service":"checkout"}`))
	require.ErrorIs(t, err, board.ErrStopped)

	f.resume()
	f.startRound(2)
	p, err := f.propose(proposal(release, h, `{"service":"checkout"}`))

	require.NoError(t, err)
	assert.Equal(t, board.KindProposal, p.Body.Kind())
}

// An agent sees the resume entry in its snapshot, with its numbers and no
// text.
func TestAnAgentSeesTheResumeInItsSnapshot(t *testing.T) {
	f := newFixture(t)
	f.startRound(1)
	f.stop()
	r := f.resume()
	f.startRound(2)

	v, err := f.state.View(&f.ctx, runtime.Name, 0)
	require.NoError(t, err)

	var seen *board.Event
	for i := range v.Entries {
		if v.Entries[i].ID == r.ID {
			seen = &v.Entries[i]
		}
	}
	require.NotNil(t, seen, "the resume entry is in the snapshot")
	assert.Equal(t, &board.Resume{Cause: "extension", Rounds: 2, WallClock: 5 * time.Minute, Tokens: 500}, seen.Body.Resume)
}

// A replay of a stop, a resume and a second stop gives a stopped board.
func TestAReplayFoldsAResume(t *testing.T) {
	f := newFixture(t)
	f.startRound(1)
	f.stop()
	f.resume()
	f.startRound(2)
	second := f.stop()

	s, err := board.Replay(f.ctx.Settings, f.events)

	require.NoError(t, err)
	got, stopped := s.Stopped()
	require.True(t, stopped)
	assert.Equal(t, second.ID, got.ID)
	_, err = board.Replay(f.ctx.Settings, f.events[:2])
	require.NoError(t, err)
}
