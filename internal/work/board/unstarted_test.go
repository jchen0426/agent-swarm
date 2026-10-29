package board_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// unstarted is the notice that agent's turn could not start, for reason.
func unstarted(agent, reason string) *board.Notice {
	return &board.Notice{Code: board.NoticeTurnUnstarted, Agent: agent, Reason: reason}
}

// notify posts a notice by author through the board's rules, and returns
// its entry, or the entry it repeats.
func (f *fixture) notify(author board.Author, n *board.Notice) (board.Event, error) {
	f.t.Helper()
	return f.posted(f.state.Notify(&f.ctx, f.nextID("n"), author, n))
}

// written returns body as a store holds it after the fixture's entries,
// written by author in the fixture's round and checked by none of this
// build's rules, as a later build may have written it.
func (f *fixture) written(author board.Author, body *board.Body) board.Event {
	return board.Event{
		Seq: int64(len(f.events) + 1), ID: f.nextID("written"), Run: f.ctx.Run, Round: f.ctx.Round,
		Author: author, At: f.now.Add(time.Second), Body: *body,
	}
}

// noticeBody is a notice's body, of this build's version.
func noticeBody(n *board.Notice) *board.Body {
	return &board.Body{V: board.Version, Notice: n}
}

// A turn_unstarted notice names the agent whose turn could not start, and
// why: the engine did not answer, the image is missing or breaks the
// contract, or the launch failed otherwise (Decision 7, R6). Posting refuses
// one without an agent, one without a reason, and one of a reason this build
// does not give. Each post is on a board of its own: a repeat is found before
// the rules, so a second notice of alpha's in the round would return the
// first unchecked.
func TestATurnUnstartedNoticeNamesItsAgentAndAReason(t *testing.T) {
	for _, reason := range []string{"engine", "image", "launch"} {
		f := newFixture(t)

		e, err := f.notify(board.Coordinator, unstarted("alpha", reason))

		require.NoError(t, err, reason)
		assert.Equal(t, unstarted("alpha", reason), e.Body.Notice, reason)
	}
	for name, n := range map[string]*board.Notice{
		"without an agent": unstarted("", "engine"),
		"without a reason": unstarted("alpha", ""),
		"of reason quota":  unstarted("alpha", "quota"),
	} {
		f := newFixture(t)

		_, err := f.notify(board.Coordinator, n)

		require.ErrorIs(t, err, board.ErrInvalid, name)
	}
}

// A posted turn_unstarted notice holds its code, its agent and its reason,
// and nothing else (C11): posting refuses one that also names a tool, a call
// or a rule. Each refusal is on a board of its own, since a call is no part
// of a repeated notice's key: after alpha's first notice, one that names a
// call would return the first unchecked.
func TestATurnUnstartedNoticeNamesNothingElse(t *testing.T) {
	f := newFixture(t)
	e, err := f.notify(board.Coordinator, unstarted("alpha", "launch"))
	require.NoError(t, err)
	raw, err := json.Marshal(e.Body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"v": 1, "notice": {"code": "turn_unstarted", "agent": "alpha", "reason": "launch"}}`, string(raw))

	for name, n := range map[string]*board.Notice{
		"a tool": {Code: board.NoticeTurnUnstarted, Agent: "alpha", Reason: "launch", Tool: "flags__set_flag"},
		"a call": {Code: board.NoticeTurnUnstarted, Agent: "alpha", Reason: "launch", CallID: "call-1"},
		"a rule": {Code: board.NoticeTurnUnstarted, Agent: "alpha", Reason: "launch", Rule: "limit"},
	} {
		g := newFixture(t)

		_, err := g.notify(board.Coordinator, n)

		require.ErrorIs(t, err, board.ErrInvalid, name)
	}
}

// Only a turn_unstarted notice gives a reason: posting refuses a notice of
// any other code that gives one, even a reason a turn_unstarted notice gives.
func TestOnlyATurnUnstartedNoticeCarriesAReason(t *testing.T) {
	for _, tt := range []struct {
		name   string
		author board.Author
		notice *board.Notice
	}{
		{"refused_call", board.Gateway, &board.Notice{Code: board.NoticeRefusedCall, Agent: "alpha", Tool: "flags__set_flag", Reason: "engine"}},
		{"failed_read", board.Gateway, &board.Notice{Code: board.NoticeFailedRead, Agent: "alpha", Tool: "logs__search", Reason: "engine"}},
		{"model_failed", board.Coordinator, &board.Notice{Code: board.NoticeModelFailed, Agent: "alpha", CallID: "m-1", Reason: "engine"}},
		{"no_agents", board.Coordinator, &board.Notice{Code: board.NoticeNoAgents, Reason: "engine"}},
	} {
		f := newFixture(t)

		_, err := f.notify(tt.author, tt.notice)

		require.ErrorIs(t, err, board.ErrInvalid, tt.name)
	}
}

// Posting never takes a notice of a code this build does not know, not even
// one a replay takes as a later build's: this build posts only the codes it
// knows (Decision 7). A guard: posting refused such a notice before replay
// took any, and must go on refusing it.
func TestPostingNeverTakesAnUnknownCode(t *testing.T) {
	f := newFixture(t)

	_, err := f.notify(board.Coordinator, &board.Notice{Code: "disk_full", Agent: "alpha"})

	require.ErrorIs(t, err, board.ErrInvalid)
	assert.Empty(t, f.events)
}

// A replay takes a well-formed notice of a code this build does not know, as
// a later build may have posted it (Decision 7): the platform's, its code and
// its reason of a stop reason's format, its other fields ignored. It feeds no
// decision: the round's report is the one the board gives without it.
func TestReplayAcceptsAWellFormedNoticeOfAnUnknownCode(t *testing.T) {
	f := newFixture(t)
	f.evidence(release, "cd", "get_history", "v42 deployed", "", "")
	f.must(f.state.Notify(&f.ctx, f.nextID("n"), board.Coordinator, &board.Notice{Code: board.NoticeModelFailed, Agent: runtime.Name, CallID: "m-1"}))
	f.must(f.state.EndTurn(&f.ctx, f.nextID("end"), release))
	body, err := board.DecodeBody([]byte(`{"notice": {"code": "disk_full", "reason": "quota", "limit": 3}}`))
	require.NoError(t, err)
	later := f.written(board.Coordinator, &body)
	without, err := board.Replay(f.ctx.Settings, f.events)
	require.NoError(t, err)
	require.Equal(t, board.RoundFacts{Ended: []string{release.Name}, Entries: 1, FailedCalls: 1}, without.Report(1).Facts,
		"a notice counted as an entry or a failed call would show")

	with, err := board.Replay(f.ctx.Settings, append(f.events, later))

	require.NoError(t, err)
	e, ok := with.Event(later.ID)
	require.True(t, ok)
	assert.Equal(t, &board.Notice{Code: "disk_full", Reason: "quota"}, e.Body.Notice)
	assert.Equal(t, without.Report(1), with.Report(1))
}

// A replay refuses a notice of a code this build does not know unless it is
// well formed: an agent's is refused, and so is one whose code or reason is
// not of a stop reason's format. A guard: the replay refused each before it
// took any notice of an unknown code, and must go on refusing them, so that
// its leniency stays within its bounds.
func TestReplayRefusesANoticeOfAnUnknownCodeThatIsNotWellFormed(t *testing.T) {
	alpha := board.Author{Kind: board.AuthorAgent, Name: "alpha"}
	for _, tt := range []struct {
		name   string
		author board.Author
		notice *board.Notice
		want   error
	}{
		{"an agent's notice", alpha, &board.Notice{Code: "disk_full", Agent: "alpha"}, board.ErrNotAllowed},
		{"a code with a capital", board.Coordinator, &board.Notice{Code: "Disk"}, board.ErrInvalid},
		{"a code with a hyphen", board.Coordinator, &board.Notice{Code: "disk-full"}, board.ErrInvalid},
		{"a code that starts with a digit", board.Coordinator, &board.Notice{Code: "1disk"}, board.ErrInvalid},
		{"a code of 33 letters", board.Coordinator, &board.Notice{Code: board.NoticeCode(strings.Repeat("d", 33))}, board.ErrInvalid},
		{"a reason with a capital", board.Coordinator, &board.Notice{Code: "disk_full", Reason: "Quota"}, board.ErrInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)

			_, err := board.Replay(f.ctx.Settings, []board.Event{f.written(tt.author, noticeBody(tt.notice))})

			require.ErrorIs(t, err, tt.want)
		})
	}
}

// A replay takes a notice of a code this build knows whose reason this build
// does not know, as a later build may give one, when the notice is well
// formed: its reason of a stop reason's format, and its code's other rules
// kept. Posting refuses it.
func TestReplayAcceptsAKnownCodeWithAReasonThisBuildDoesNotKnow(t *testing.T) {
	for name, n := range map[string]*board.Notice{
		"turn_unstarted of reason quota": unstarted("alpha", "quota"),
		"refused_call, which gives none": {Code: board.NoticeRefusedCall, Agent: "alpha", Tool: "flags__set_flag", Reason: "quota"},
	} {
		f := newFixture(t)

		_, err := board.Replay(f.ctx.Settings, []board.Event{f.written(board.Coordinator, noticeBody(n))})
		require.NoError(t, err, name)
		_, err = f.notify(board.Coordinator, n)
		require.ErrorIs(t, err, board.ErrInvalid, name)
	}
	for name, n := range map[string]*board.Notice{
		"a reason with a capital": unstarted("alpha", "Quota"),
		"without an agent":        unstarted("", "quota"),
		"without a reason":        unstarted("alpha", ""),
	} {
		f := newFixture(t)

		_, err := board.Replay(f.ctx.Settings, []board.Event{f.written(board.Coordinator, noticeBody(n))})

		require.ErrorIs(t, err, board.ErrInvalid, name)
	}
}

// One turn_unstarted notice per agent and round is enough, whatever its
// reason, which is not part of a repeated notice's key: a launch that fails
// again in the round, for another reason, adds nothing (Decision 7).
func TestATurnUnstartedNoticeIsPostedOncePerAgentPerRoundWhateverItsReason(t *testing.T) {
	f := newFixture(t)

	first, err := f.notify(board.Coordinator, unstarted("alpha", "engine"))
	require.NoError(t, err)
	again, err := f.notify(board.Coordinator, unstarted("alpha", "image"))
	require.NoError(t, err)
	f.round(2)
	next, err := f.notify(board.Coordinator, unstarted("alpha", "image"))
	require.NoError(t, err)
	beta, err := f.notify(board.Coordinator, unstarted("beta", "engine"))
	require.NoError(t, err)

	assert.Equal(t, first.ID, again.ID, "round 1's second notice repeats the first")
	assert.Equal(t, "engine", again.Body.Notice.Reason, "with the first's reason")
	assert.NotEqual(t, first.ID, next.ID, "round 2 has its own")
	assert.NotEqual(t, next.ID, beta.ID, "beta has its own")
	assert.Len(t, f.events, 3)
}

// A replay refuses an event of a later version with ErrVersion before any
// rule, its leniency toward notices included (Decision 10): scenario 11(a)'s
// body, as the stores decode it, and a body that holds a well-formed notice
// of a code this build does not know. A guard: the replay refused both so
// before it took any notice of an unknown code, and must go on doing so.
func TestALaterVersionsNoticeStillFailsWithErrVersion(t *testing.T) {
	stored, err := board.DecodeBody([]byte(`{"v": 2, "notice": {"shape": "of a later build"}}`))
	require.NoError(t, err)
	for name, body := range map[string]*board.Body{
		"scenario 11(a)'s body":                   &stored,
		"a well-formed notice of an unknown code": {V: board.Version + 1, Notice: &board.Notice{Code: "disk_full"}},
	} {
		f := newFixture(t)
		f.task(release, "check the pool")

		_, err := board.Replay(f.ctx.Settings, append(f.events, f.written(board.Coordinator, body)))

		require.ErrorIs(t, err, board.ErrVersion, name)
		require.NotErrorIs(t, err, board.ErrInvalid, name)
	}
}
