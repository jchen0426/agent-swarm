package board_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// Authors used across the board tests.
var (
	release = board.Author{Kind: board.AuthorAgent, Name: "release-investigator"}
	runtime = board.Author{Kind: board.AuthorAgent, Name: "runtime-investigator"}
	deps    = board.Author{Kind: board.AuthorAgent, Name: "dependency-investigator"}
	alice   = board.Author{Kind: board.AuthorHuman, Name: "alice@example.com"}
	bob     = board.Author{Kind: board.AuthorHuman, Name: "bob@example.com"}
)

func incidentSettings() board.Settings {
	return board.Settings{
		Schema: key.Schema{Fields: []key.Field{
			{Name: "service", Values: []string{"checkout", "payments"}},
			{Name: "failureMode", Values: []string{"bad-deploy", "config-change", "resource-exhaustion"}},
		}},
		Convergence: board.DefaultConvergence,
	}
}

func checkoutDeploy() key.Key {
	return key.Key{"service": "checkout", "failureMode": "bad-deploy"}
}

// fixture builds a board through the public rules, committing each draft the
// way the event store does: the next sequence number and a later time.
type fixture struct {
	t      *testing.T
	ctx    board.Context
	state  *board.State
	events []board.Event
	calls  map[string]*board.ReadCall
	now    time.Time
	n      int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := board.Context{Run: "run-1", Round: 1, HumanEntries: true, Settings: incidentSettings()}
	state, err := board.Replay(ctx.Settings, nil)
	require.NoError(t, err)
	return &fixture{
		t:     t,
		ctx:   ctx,
		state: state,
		calls: make(map[string]*board.ReadCall),
		now:   time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC),
	}
}

func (f *fixture) nextID(prefix string) board.EntryID {
	f.n++
	return board.EntryID(fmt.Sprintf("%s-%d", prefix, f.n))
}

// call records a successful read call by an agent in the fixture's run.
func (f *fixture) call(agent board.Author, server, tool, output string) string {
	f.n++
	id := fmt.Sprintf("call-%d", f.n)
	f.calls[id] = &board.ReadCall{ID: id, Run: f.ctx.Run, Agent: agent.Name, Server: server, Tool: tool, Output: []byte(output)}
	return id
}

func (f *fixture) commit(d *board.Draft) board.Event {
	f.t.Helper()
	f.now = f.now.Add(time.Second)
	e := board.Event{Seq: int64(len(f.events) + 1), ID: d.ID, Run: d.Run, Round: d.Round, Author: d.Author, At: f.now, Body: d.Body}
	require.NoError(f.t, f.state.Apply(&e))
	f.events = append(f.events, e)
	return e
}

func (f *fixture) postEvidence(author board.Author, in *board.EvidenceInput) (board.Draft, error) {
	return f.state.NewEvidence(&f.ctx, f.nextID("ev"), author, in, f.calls[in.CallID])
}

// evidence posts an agent's evidence that quotes its own read call and
// returns the entry id. A non-empty hypothesis also sets a stance.
func (f *fixture) evidence(agent board.Author, server, tool, quote string, h board.EntryID, stance board.Stance) board.EntryID {
	f.t.Helper()
	callID := f.call(agent, server, tool, "output: "+quote)
	d, err := f.postEvidence(agent, &board.EvidenceInput{Claim: "saw " + quote, CallID: callID, Quote: quote, Hypothesis: h, Stance: stance})
	require.NoError(f.t, err)
	return f.commit(&d).ID
}

func (f *fixture) hypothesis(agent board.Author, k key.Key, evidence ...board.EntryID) board.EntryID {
	f.t.Helper()
	d, err := f.state.NewHypothesis(&f.ctx, f.nextID("hyp"), agent, &board.HypothesisInput{Key: k, Statement: "the cause is " + k.String(), Evidence: evidence})
	require.NoError(f.t, err)
	return f.commit(&d).ID
}

func (f *fixture) retract(agent board.Author, evidence board.EntryID) {
	f.t.Helper()
	d, err := f.state.NewRetraction(&f.ctx, f.nextID("ret"), agent, evidence)
	require.NoError(f.t, err)
	f.commit(&d)
}

func (f *fixture) rule(human board.Author, in *board.RulingInput) {
	f.t.Helper()
	d, err := f.state.NewRuling(&f.ctx, f.nextID("rul"), human, in)
	require.NoError(f.t, err)
	f.commit(&d)
}

var someReason = &board.Reason{Code: "wrong-cause"}

// posted commits a posting's draft and returns the entry, or returns the
// entry the posting repeats.
func (f *fixture) posted(p board.Posting, err error) (board.Event, error) {
	f.t.Helper()
	if err != nil {
		return board.Event{}, err
	}
	if p.Earlier != nil {
		return *p.Earlier, nil
	}
	return f.commit(p.Draft), nil
}

// must is posted for a post that must succeed.
func (f *fixture) must(p board.Posting, err error) board.Event {
	f.t.Helper()
	e, err := f.posted(p, err)
	require.NoError(f.t, err)
	return e
}

// round moves the board to round n, with a snapshot of every entry so far.
func (f *fixture) round(n int) {
	f.ctx.Round = n
	f.ctx.Snapshot = int64(len(f.events))
}

func (f *fixture) task(agent board.Author, title string, tags ...string) board.EntryID {
	f.t.Helper()
	return f.must(f.state.PostTask(&f.ctx, f.nextID("task"), agent, &board.TaskInput{Title: title, Tags: tags})).ID
}

func (f *fixture) claim(agent board.Author, task board.EntryID) (board.Event, error) {
	f.t.Helper()
	return f.posted(f.state.Claim(&f.ctx, f.nextID("claim"), agent, task))
}

func (f *fixture) complete(agent board.Author, task board.EntryID, evidence ...board.EntryID) (board.Event, error) {
	f.t.Helper()
	return f.posted(f.state.Complete(&f.ctx, f.nextID("done"), agent, &board.CompletionInput{Task: task, Evidence: evidence}))
}

// proposal is a write call by agent citing h, with arguments args, as the
// gateway records it.
func proposal(agent board.Author, h board.EntryID, args string) *board.Proposal {
	return &board.Proposal{
		Proposer: agent.Name, Tool: "deploys.rollback", SchemaHash: "sha256:" + strings.Repeat("a", 64),
		ArgsHash: "sha256:" + args, Hypothesis: h, Target: []board.TargetValue{{Arg: "service", Value: "checkout"}},
		Summary: []board.SummaryPart{{Text: "Roll back "}, {Text: "checkout", Value: true}}, CallID: "call-w",
	}
}

func (f *fixture) propose(p *board.Proposal) (board.Event, error) {
	f.t.Helper()
	return f.posted(f.state.Propose(&f.ctx, f.nextID("prop"), p))
}
