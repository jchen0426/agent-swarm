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

// Active returns the items that are not closed, by id, as the Postgres store
// lists them: those with an opening record, and no close without a reopen
// after it.
func (s *Records) Active(context.Context) ([]item.ID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []item.ID
	for id, records := range s.records {
		opened, closed := false, false
		for i := range records {
			switch records[i].Body.Kind() {
			case item.KindOpened:
				opened = true
			case item.KindClosed:
				closed = true
			case item.KindReopened:
				closed = false
			}
		}
		if opened && !closed {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

// ClosedSince returns the items whose latest close is within window of the
// store's time, and no reopen followed it, by id, as the Postgres store lists
// them: a close counts when it was recorded after now less window.
func (s *Records) ClosedSince(_ context.Context, window time.Duration) ([]item.ID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	since := s.now().Add(-window)
	var out []item.ID
	for id, records := range s.records {
		for i := len(records) - 1; i >= 0; i-- {
			kind := records[i].Body.Kind()
			if kind == item.KindReopened {
				break
			}
			if kind == item.KindClosed {
				if records[i].At.After(since) {
					out = append(out, id)
				}
				break
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// IncidentItems returns the items whose latest record is in incident, by id,
// as the Postgres store reads them (Decision 18): an item's incident is its
// latest record's. An incident with no item gives an empty list, not nil, as
// the Postgres store's does.
func (s *Records) IncidentItems(_ context.Context, incident item.IncidentID) ([]item.ID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []item.ID{}
	for id, records := range s.records {
		if n := len(records); n > 0 && records[n-1].Incident == incident {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

// UnstaffedBy returns the items whose item_awaiting_close names the kill
// switch's row seq as the on-row that left them not staffed, by id, as the
// Postgres store finds them. A record that names no on-row is found for no
// seq, 0 included: the store's bodies leave the number out.
func (s *Records) UnstaffedBy(_ context.Context, seq int64) ([]item.ID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []item.ID
	for id, records := range s.records {
		for i := range records {
			if a := records[i].Body.AwaitingClose; a != nil && a.UnstaffedBy != 0 && a.UnstaffedBy == seq {
				out = append(out, id)
				break
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// Append commits the records that decide returns, after the item's last. It
// refuses an automatic close that it would stamp before its due, after its
// rule for an item's times, with item.ErrNotDue, and then commits nothing.
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
		if c := d.Body.Closed; c != nil && c.Auto && at.Before(c.Due) {
			return nil, fmt.Errorf("record %s would be stamped %s, before its due %s: %w", d.ID, at, c.Due, item.ErrNotDue)
		}
		last = at
		committed = append(committed, item.Record{
			Seq: int64(len(history) + len(committed) + 1), ID: d.ID, Item: id, Incident: d.Incident, Run: d.Run, At: at, Body: d.Body,
		})
	}
	if len(committed) > 0 {
		s.records[id] = append(history, committed...)
	}
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
