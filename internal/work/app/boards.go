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

// PostEvidence checks and commits an evidence entry.
func (b *Boards) PostEvidence(ctx context.Context, run board.RunID, author board.Author, in *board.EvidenceInput) (board.Event, error) {
	bc, err := b.runs.BoardContext(ctx, run)
	if err != nil {
		return board.Event{}, fmt.Errorf("load run %s: %w", run, err)
	}
	var call *board.ReadCall
	if author.Kind == board.AuthorAgent && in.CallID != "" {
		found, err := b.calls.ReadCall(ctx, in.CallID)
		switch {
		case errors.Is(err, ErrCallNotFound):
			// The board rules refuse evidence that cites no known call.
		case err != nil:
			return board.Event{}, fmt.Errorf("look up read call %s: %w", in.CallID, err)
		default:
			call = &found
		}
	}
	id := board.EntryID(b.ids.NewID())
	return b.appendOne(ctx, &bc, func(s *board.State) (board.Draft, error) {
		return s.NewEvidence(&bc, id, author, in, call)
	})
}

// PostHypothesis checks and commits a hypothesis, merging it into an earlier
// hypothesis with an equal key.
func (b *Boards) PostHypothesis(ctx context.Context, run board.RunID, author board.Author, in *board.HypothesisInput) (board.Event, error) {
	bc, err := b.runs.BoardContext(ctx, run)
	if err != nil {
		return board.Event{}, fmt.Errorf("load run %s: %w", run, err)
	}
	id := board.EntryID(b.ids.NewID())
	return b.appendOne(ctx, &bc, func(s *board.State) (board.Draft, error) {
		return s.NewHypothesis(&bc, id, author, in)
	})
}

// Retract retracts an agent's own evidence. Retracting evidence that is
// already retracted returns the earlier retraction and commits nothing, so a
// repeated call does no harm (spec Section 5.2).
func (b *Boards) Retract(ctx context.Context, run board.RunID, author board.Author, evidence board.EntryID) (board.Event, error) {
	bc, err := b.runs.BoardContext(ctx, run)
	if err != nil {
		return board.Event{}, fmt.Errorf("load run %s: %w", run, err)
	}
	id := board.EntryID(b.ids.NewID())
	var earlier board.Event
	committed, err := b.store.Append(ctx, run, func(history []board.Event) ([]board.Draft, error) {
		s, err := board.Replay(bc.Settings, history)
		if err != nil {
			return nil, err
		}
		if rid, done := s.RetractionOf(evidence); done {
			if e, _ := s.Event(rid); e.Author == author {
				earlier = e
				return nil, nil
			}
		}
		d, err := s.NewRetraction(&bc, id, author, evidence)
		if err != nil {
			return nil, err
		}
		return []board.Draft{d}, nil
	})
	if err != nil {
		return board.Event{}, err
	}
	if len(committed) == 0 {
		return earlier, nil
	}
	return committed[0], nil
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
