package item

import (
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Running reports whether the item has a run that has not stopped.
func (s *State) Running() bool {
	return s.Run != nil && s.Run.Stopped == nil
}

// ResolvedAt is when the item's trigger resolved. An item a person opened
// has no trigger, which reads as resolved when the item opened (Decision 8,
// R19), and every item is so until Plan 7c-1 gives an item with a trigger its
// trigger's resolution.
func (s *State) ResolvedAt() time.Time {
	return s.OpenedAt
}

// AutoCloseAt is when the item, awaiting its close, closes by itself: delay
// after the latest of its trigger's resolution, its latest entry into
// awaiting_close, and offAt, the kill switch's latest turning off, zero
// before the first (spec Section 4.1; Decision 8, R19).
func (s *State) AutoCloseAt(delay time.Duration, offAt time.Time) time.Time {
	from := s.ResolvedAt()
	for _, t := range []time.Time{s.AwaitingSince, offAt} {
		if t.After(from) {
			from = t
		}
	}
	return from.Add(delay)
}

// RulingsOpen reports whether a person may rule kind on the item's board at
// now (spec Section 4.2; Decision 9, R30). Reject, un-reject and dismiss are
// open while the item is not closed: a closed item is reopened first. Any
// other kind, as the adoption, feedback and milestone rulings of Plans 8b and
// 9b, is open while the item is not closed, and until window after its close.
func (s *State) RulingsOpen(kind board.RulingKind, now time.Time, window time.Duration) bool {
	if s.Status() != StatusClosed {
		return true
	}
	switch kind {
	case board.RulingReject, board.RulingUnreject, board.RulingDismiss:
		return false
	}
	return !now.After(s.ClosedAt.Add(window))
}
