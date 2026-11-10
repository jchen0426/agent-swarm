package memory

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The inbound event store's rows are kept in insert order beside the records,
// under the same lock, so that a row and the records it commits with appear
// together. Records implements app.Inbound.

func (s *Records) heldLocked(source, eventID string) bool {
	return slices.ContainsFunc(s.inbound, func(e app.InboundEvent) bool { return e.Source == source && e.EventID == eventID })
}

// Seen tells whether the store holds a row of (source, eventID).
func (s *Records) Seen(_ context.Context, source, eventID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.heldLocked(source, eventID), nil
}

// Put writes a row that touches no item, and refuses one that names an item:
// such a row commits with its records (see AppendInbound). A row whose key is
// held fails with app.ErrSeen.
func (s *Records) Put(_ context.Context, e *app.InboundEvent) error {
	if e.Item != "" {
		return fmt.Errorf("put the inbound event of source %s: %w", e.Source, app.ErrRowOfAnItem)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heldLocked(e.Source, e.EventID) {
		return fmt.Errorf("%w: %s", app.ErrSeen, e.Source)
	}
	row := *e
	row.ReceivedAt = s.now().UTC()
	s.inbound = append(s.inbound, row)
	return nil
}

// LastStatus is the instance's latest applied row: by occurred_at, and at
// equal time resolved over firing.
func (s *Records) LastStatus(_ context.Context, source, instanceID string) (item.TriggerStatus, time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *app.InboundEvent
	for i := range s.inbound {
		e := &s.inbound[i]
		if e.Source != source || e.InstanceID != instanceID || !e.Applied {
			continue
		}
		if best == nil || e.OccurredAt.After(best.OccurredAt) ||
			e.OccurredAt.Equal(best.OccurredAt) && e.Status == item.Resolved && best.Status != item.Resolved {
			best = e
		}
	}
	if best == nil {
		return "", time.Time{}, false, nil
	}
	return best.Status, best.OccurredAt, true, nil
}

// LastItem is the item of the latest row of the dedup key that names an item.
func (s *Records) LastItem(_ context.Context, source, dedupKey string) (item.ID, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.inbound) - 1; i >= 0; i-- {
		if e := &s.inbound[i]; e.Source == source && e.DedupKey == dedupKey && e.Item != "" {
			return e.Item, true, nil
		}
	}
	return "", false, nil
}

// Titles is the title of each event id of source that the store keeps, as
// the Postgres store reads them by their key: an id it holds no row of is
// absent.
func (s *Records) Titles(_ context.Context, source string, eventIDs []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(eventIDs))
	for i := range s.inbound {
		if e := &s.inbound[i]; e.Source == source && slices.Contains(eventIDs, e.EventID) {
			out[e.EventID] = e.Title
		}
	}
	return out, nil
}
