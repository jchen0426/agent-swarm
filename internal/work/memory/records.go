package memory

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// Records is an in-memory app.Records, and the app.Runs its run records
// make. One lock serializes appends, and record times never go backwards
// within an item, as in the Postgres store.
type Records struct {
	mu      sync.Mutex
	now     func() time.Time
	records map[item.ID][]item.Record
}

// NewRecords returns an empty store whose record times come from now.
func NewRecords(now func() time.Time) *Records {
	return &Records{now: now, records: map[item.ID][]item.Record{}}
}

// Load returns an item's records in commit order.
func (s *Records) Load(_ context.Context, id item.ID) ([]item.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.records[id]), nil
}

// Active returns the items that do not await their close, by id.
func (s *Records) Active(context.Context) ([]item.ID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []item.ID
	for id, records := range s.records {
		if !slices.ContainsFunc(records, func(r item.Record) bool { return r.Body.AwaitingClose != nil }) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

// Append commits the records that decide returns, after the item's last.
func (s *Records) Append(_ context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	history := s.records[id]
	drafts, err := decide(slices.Clone(history))
	if err != nil {
		return nil, err
	}
	var last time.Time
	if len(history) > 0 {
		last = history[len(history)-1].At
	}
	committed := make([]item.Record, 0, len(drafts))
	for i := range drafts {
		d := &drafts[i]
		if d.Item != id {
			return nil, fmt.Errorf("record %s belongs to item %s, not %s", d.ID, d.Item, id)
		}
		at := s.now().UTC()
		if !at.After(last) {
			at = last.Add(time.Microsecond)
		}
		last = at
		committed = append(committed, item.Record{
			Seq: int64(len(history) + len(committed) + 1), ID: d.ID, Item: id, Incident: d.Incident, Run: d.Run, At: at, Body: d.Body,
		})
	}
	s.records[id] = append(history, committed...)
	return committed, nil
}

// BoardContext returns a run's board context from its creation record.
func (s *Records) BoardContext(_ context.Context, run board.RunID) (board.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, records := range s.records {
		for i := range records {
			if rc := records[i].Body.RunCreated; rc != nil && records[i].Run == run {
				return board.Context{Run: run, HumanEntries: rc.Class == item.ClassPrimary && rc.Mode == item.ModeVisible, Settings: rc.Settings}, nil
			}
		}
	}
	return board.Context{}, fmt.Errorf("no run %s", run)
}
