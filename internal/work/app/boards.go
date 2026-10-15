// Package app holds the Work context's use cases and declares the ports they need.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// EventStore keeps each run's board events.
type EventStore interface {
	// Load returns a run's events in commit order.
	Load(ctx context.Context, run board.RunID) ([]board.Event, error)
	// Append serializes writers per run. decide sees every committed event of
	// the run and returns the entries to commit; an error from decide commits
	// nothing and is returned as is.
	Append(ctx context.Context, run board.RunID, decide func(history []board.Event) ([]board.Draft, error)) ([]board.Event, error)
}

// ReadCalls looks up gateway read calls in the audit store.
type ReadCalls interface {
	// ReadCall returns the call with the given id, or ErrCallNotFound.
	ReadCall(ctx context.Context, id string) (board.ReadCall, error)
}

// ErrCallNotFound is returned by ReadCalls when no read call has the id.
var ErrCallNotFound = errors.New("app: read call not found")

// Runs tells the board rules what they need to know about a run.
type Runs interface {
	BoardContext(ctx context.Context, run board.RunID) (board.Context, error)
}

// IDs issues new entry identifiers.
type IDs interface {
	NewID() string
}

// Boards posts entries to run boards and assesses them.
type Boards struct {
	store EventStore
	calls ReadCalls
	runs  Runs
	ids   IDs
}

// NewBoards returns the board use cases over the given ports.
func NewBoards(store EventStore, calls ReadCalls, runs Runs, ids IDs) *Boards {
	return &Boards{store: store, calls: calls, runs: runs, ids: ids}
}

// The use cases that post for agents take the round the agent acted in,
// which counts from 1. A post that arrives after that round ended is
// refused, never counted in the next round; humans and the platform pass 0,
// and post in the current round.

// PostEvidence checks and commits an evidence entry.
func (b *Boards) PostEvidence(ctx context.Context, run board.RunID, round int, author board.Author, in *board.EvidenceInput) (board.Posted, error) {
	var call *board.ReadCall
	if author.Kind == board.AuthorAgent && in.CallID != "" {
		found, err := b.calls.ReadCall(ctx, in.CallID)
		switch {
		case errors.Is(err, ErrCallNotFound):
			// The board rules refuse evidence that cites no known call.
		case err != nil:
			return board.Posted{}, fmt.Errorf("look up read call %s: %w", in.CallID, err)
		default:
			call = &found
		}
	}
	return b.post(ctx, run, round, func(s *board.State, bc *board.Context, id board.EntryID) (board.Posting, error) {
		return s.PostEvidence(bc, id, author, in, call)
	})
}

// PostHypothesis checks and commits a hypothesis, merging it into an earlier
// hypothesis with an equal key.
func (b *Boards) PostHypothesis(ctx context.Context, run board.RunID, round int, author board.Author, in *board.HypothesisInput) (board.Posted, error) {
	return b.post(ctx, run, round, func(s *board.State, bc *board.Context, id board.EntryID) (board.Posting, error) {
		return s.PostHypothesis(bc, id, author, in)
	})
}

// Retract retracts an agent's own evidence. Retracting evidence that is
// already retracted returns the earlier retraction.
func (b *Boards) Retract(ctx context.Context, run board.RunID, round int, author board.Author, evidence board.EntryID) (board.Posted, error) {
	return b.post(ctx, run, round, func(s *board.State, bc *board.Context, id board.EntryID) (board.Posting, error) {
		return s.Retract(bc, id, author, evidence)
	})
}

// PostTask checks and commits a task.
func (b *Boards) PostTask(ctx context.Context, run board.RunID, round int, author board.Author, in *board.TaskInput) (board.Posted, error) {
	return b.post(ctx, run, round, func(s *board.State, bc *board.Context, id board.EntryID) (board.Posting, error) {
		return s.PostTask(bc, id, author, in)
	})
}

// ClaimTask claims a task for its author. Claims are atomic: appends to a
// run's board are serialized, and each sees every earlier entry.
func (b *Boards) ClaimTask(ctx context.Context, run board.RunID, round int, author board.Author, task board.EntryID) (board.Posted, error) {
	return b.post(ctx, run, round, func(s *board.State, bc *board.Context, id board.EntryID) (board.Posting, error) {
		return s.Claim(bc, id, author, task)
	})
}

// CompleteTask completes a task its author holds.
func (b *Boards) CompleteTask(ctx context.Context, run board.RunID, round int, author board.Author, in *board.CompletionInput) (board.Posted, error) {
	return b.post(ctx, run, round, func(s *board.State, bc *board.Context, id board.EntryID) (board.Posting, error) {
		return s.Complete(bc, id, author, in)
	})
}

// EndTurn records that an agent ended its turn in the round it acted in.
func (b *Boards) EndTurn(ctx context.Context, run board.RunID, round int, author board.Author) (board.Posted, error) {
	return b.post(ctx, run, round, func(s *board.State, bc *board.Context, id board.EntryID) (board.Posting, error) {
		return s.EndTurn(bc, id, author)
	})
}

// Proposable checks a write call against the run's board before any read is
// made for it, and commits nothing.
func (b *Boards) Proposable(ctx context.Context, run board.RunID, round int, p *board.Proposal) (board.Proposable, error) {
	bc, s, err := b.state(ctx, run)
	if err != nil {
		return board.Proposable{}, err
	}
	if err := sameRound(round, &bc); err != nil {
		return board.Proposable{}, err
	}
	return s.Proposable(&bc, p)
}

// Propose checks a proposal again and commits it as the gateway's entry.
func (b *Boards) Propose(ctx context.Context, run board.RunID, round int, p *board.Proposal) (board.Posted, error) {
	return b.post(ctx, run, round, func(s *board.State, bc *board.Context, id board.EntryID) (board.Posting, error) {
		return s.Propose(bc, id, p)
	})
}

// Notice commits a platform notice in the run's current round, once per
// agent, tool, code and rule, and at most board.MaxNotices per agent.
func (b *Boards) Notice(ctx context.Context, run board.RunID, author board.Author, n *board.Notice) (board.Posted, error) {
	return b.post(ctx, run, 0, func(s *board.State, bc *board.Context, id board.EntryID) (board.Posting, error) {
		return s.Notify(bc, id, author, n)
	})
}

// StartRound opens a round on a run's board, as the coordinator does when it
// freezes the round's snapshot (spec Section 5.2). From then on the board's
// round entries decide a post's round, under the run's lock.
func (b *Boards) StartRound(ctx context.Context, run board.RunID, in *board.RoundStart) (board.Posted, error) {
	return b.post(ctx, run, 0, func(s *board.State, bc *board.Context, id board.EntryID) (board.Posting, error) {
		return s.StartRound(bc, id, board.Coordinator, in)
	})
}

// View returns what an agent sees of its run's board in the current round.
func (b *Boards) View(ctx context.Context, run board.RunID, agent string, since int64) (board.View, error) {
	bc, s, err := b.state(ctx, run)
	if err != nil {
		return board.View{}, err
	}
	return s.View(&bc, agent, since)
}

// Context returns a run's board context: its settings, and its round, its
// snapshot and its wrap-up hypothesis, as the board's latest round entry
// says.
func (b *Boards) Context(ctx context.Context, run board.RunID) (board.Context, error) {
	bc, _, err := b.state(ctx, run)
	return bc, err
}

// run returns what the run says of its board: its settings and, for a board
// without round entries, its round.
func (b *Boards) run(ctx context.Context, run board.RunID) (board.Context, error) {
	bc, err := b.runs.BoardContext(ctx, run)
	if err != nil {
		return board.Context{}, fmt.Errorf("load run %s: %w", run, err)
	}
	return bc, nil
}

// state returns a run's board context and its board, as committed now.
func (b *Boards) state(ctx context.Context, run board.RunID) (board.Context, *board.State, error) {
	bc, err := b.run(ctx, run)
	if err != nil {
		return board.Context{}, nil, err
	}
	events, err := b.store.Load(ctx, run)
	if err != nil {
		return board.Context{}, nil, fmt.Errorf("load board of run %s: %w", run, err)
	}
	s, err := board.Replay(bc.Settings, events)
	if err != nil {
		return board.Context{}, nil, err
	}
	s.Opened(&bc)
	return bc, s, nil
}

// post checks a post against the run's whole board and commits its entry, or
// returns the entry it repeats. The run is read before the append, never
// inside its lock, which would hold a connection while it waits for another.
// The post's round is checked inside the lock, against the board's latest
// round entry: the coordinator opens a round by appending one, so a post
// made in a round that has ended is refused, never counted after it.
func (b *Boards) post(ctx context.Context, run board.RunID, round int, check func(*board.State, *board.Context, board.EntryID) (board.Posting, error)) (board.Posted, error) {
	bc, err := b.run(ctx, run)
	if err != nil {
		return board.Posted{}, err
	}
	id := board.EntryID(b.ids.NewID())
	var earlier *board.Event
	committed, err := b.store.Append(ctx, run, func(history []board.Event) ([]board.Draft, error) {
		earlier = nil
		s, err := board.Replay(bc.Settings, history)
		if err != nil {
			return nil, err
		}
		now := bc
		s.Opened(&now)
		if err := sameRound(round, &now); err != nil {
			return nil, err
		}
		p, err := check(s, &now, id)
		switch {
		case err != nil:
			return nil, err
		case p.Earlier != nil:
			earlier = p.Earlier
			return nil, nil
		}
		return []board.Draft{*p.Draft}, nil
	})
	if err != nil {
		return board.Posted{}, err
	}
	if earlier != nil {
		return board.Posted{Event: *earlier, Repeat: true}, nil
	}
	return board.Posted{Event: committed[0]}, nil
}

// sameRound refuses a post made in a round that has ended.
func sameRound(round int, bc *board.Context) error {
	if round != 0 && round != bc.Round {
		return fmt.Errorf("%w: it was made in round %d, and the run is in round %d", board.ErrRoundEnded, round, bc.Round)
	}
	return nil
}

// Rule checks and commits a human's ruling on a hypothesis or a refutation.
func (b *Boards) Rule(ctx context.Context, run board.RunID, author board.Author, in *board.RulingInput) (board.Event, error) {
	bc, err := b.runs.BoardContext(ctx, run)
	if err != nil {
		return board.Event{}, fmt.Errorf("load run %s: %w", run, err)
	}
	id := board.EntryID(b.ids.NewID())
	return b.appendOne(ctx, &bc, func(s *board.State) (board.Draft, error) {
		return s.NewRuling(&bc, id, author, in)
	})
}

// Assess returns a run's ranked hypotheses and conclusion as of at. A zero at
// assesses every committed entry.
func (b *Boards) Assess(ctx context.Context, run board.RunID, at time.Time) (board.Assessment, error) {
	bc, err := b.runs.BoardContext(ctx, run)
	if err != nil {
		return board.Assessment{}, fmt.Errorf("load run %s: %w", run, err)
	}
	events, err := b.store.Load(ctx, run)
	if err != nil {
		return board.Assessment{}, fmt.Errorf("load board of run %s: %w", run, err)
	}
	replay := board.Replay
	if !at.IsZero() {
		replay = func(settings board.Settings, events []board.Event) (*board.State, error) {
			return board.ReplayAsOf(settings, events, at)
		}
	}
	s, err := replay(bc.Settings, events)
	if err != nil {
		return board.Assessment{}, err
	}
	return s.Assess(), nil
}

func (b *Boards) appendOne(ctx context.Context, bc *board.Context, decide func(*board.State) (board.Draft, error)) (board.Event, error) {
	committed, err := b.store.Append(ctx, bc.Run, func(history []board.Event) ([]board.Draft, error) {
		s, err := board.Replay(bc.Settings, history)
		if err != nil {
			return nil, err
		}
		d, err := decide(s)
		if err != nil {
			return nil, err
		}
		return []board.Draft{d}, nil
	})
	if err != nil {
		return board.Event{}, err
	}
	return committed[0], nil
}
