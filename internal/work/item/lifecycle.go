package item

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Running reports whether the item has a run that has not stopped.
func (s *State) Running() bool {
	return s.Run != nil && s.Run.Stopped == nil
}

// ResolvedAt is when the item's trigger resolved, and false while it fires
// (design D5, R110): the record time of the applied item_triggered after
// which no instance fired. An item that no instance fired, as one a person
// opened, which has no trigger, was resolved when it opened (Decision 8,
// R19).
func (s *State) ResolvedAt() (at time.Time, resolved bool) {
	if !s.trigger.resolved {
		return time.Time{}, false
	}
	return s.trigger.resolvedAt, true
}

// AutoCloseAt is when the item, awaiting its close, closes by itself: delay
// after the latest of its trigger's resolution, its latest entry into
// awaiting_close, and offAt, the kill switch's latest turning off, zero
// before the first (spec Section 4.1; Decision 8, R19). It is false, never,
// exactly while the trigger fires (R110).
func (s *State) AutoCloseAt(delay time.Duration, offAt time.Time) (at time.Time, ok bool) {
	from, ok := s.ResolvedAt()
	if !ok {
		return time.Time{}, false
	}
	for _, t := range []time.Time{s.AwaitingSince, offAt} {
		if t.After(from) {
			from = t
		}
	}
	return from.Add(delay), true
}

// TriggerFiring reports whether any instance's last applied status on the
// item is a firing (design D5).
func (s *State) TriggerFiring() bool {
	for _, a := range s.trigger.last {
		if a.status == Firing {
			return true
		}
	}
	return false
}

// TriggerResolvedSince reports whether the item's trigger is resolved by a
// record later, by sequence, than its run's creation or latest resume (design
// D12, R127): a run started or resumed after the resolution is not cut by it.
// An item with no run, or that no instance fired, as one a person opened, has
// none.
func (s *State) TriggerResolvedSince() bool {
	return s.Run != nil && s.trigger.resolved && s.trigger.resolvedSeq > s.Run.startedSeq
}

// FiringResumeDeclined reports whether the latest firing resume tried since
// the run's latest stop was declined (re-review N1), so that the next applied
// firing of any instance tries again.
func (s *State) FiringResumeDeclined() bool {
	return strings.HasPrefix(s.trigger.resume, ResumeDeclined)
}

// FiringResumeMissed reports whether an applied firing that would have tried
// a firing resume landed since the run's latest stop with none tried, as one
// that came before its item awaited its close (fix round 1, M-1), or since
// its latest round's end and before that stop (the final review's M-1), so
// that the next applied firing of any instance tries it.
func (s *State) FiringResumeMissed() bool {
	return s.trigger.missed
}

// HoldsInstance reports whether the item took an event of source's instance,
// applied or late (design D5).
func (s *State) HoldsInstance(source, instanceID string) bool {
	return s.trigger.instances[keyOf{source, instanceID}]
}

// HoldsDedupKey reports whether the item took an event of source keyed key,
// applied or late (design D5).
func (s *State) HoldsDedupKey(source, key string) bool {
	return s.trigger.dedups[keyOf{source, key}]
}

// HoldsCorrelationKey reports whether the item took an event of source
// correlated by key, applied or late (R111).
func (s *State) HoldsCorrelationKey(source, key string) bool {
	return s.trigger.correlations[keyOf{source, key}]
}

// TriggerInstance is one instance of an item's trigger (design D5): its
// source, its id, its last applied status on the item, and when that status
// began.
type TriggerInstance struct {
	Source, ID string
	Status     TriggerStatus
	At         time.Time
}

// Instances is each instance's last applied status on the item, by source,
// then by id, and nil while none applied: a late event applies nothing, so
// an instance only late events named is not among them.
func (s *State) Instances() []TriggerInstance {
	var out []TriggerInstance
	for k, a := range s.trigger.last {
		out = append(out, TriggerInstance{Source: k.source, ID: k.value, Status: a.status, At: a.at})
	}
	slices.SortFunc(out, func(a, b TriggerInstance) int {
		return cmp.Or(cmp.Compare(a.Source, b.Source), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// Instance is source's instance's last applied status on the item, and when
// it began, and false when no applied event of it is on the item.
func (s *State) Instance(source, instanceID string) (status TriggerStatus, at time.Time, ok bool) {
	a, ok := s.trigger.last[keyOf{source, instanceID}]
	return a.status, a.at, ok
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

// KillSwitchSeen is the latest kill switch row the coordinator knew for the
// item (Decision 5): its run's, the largest of its creation's and its
// resumes' (Run.KillSwitchSeen); with no run, the latest of its opening's,
// its start's and its promotion's, 0 before the first row.
func (s *State) KillSwitchSeen() int64 {
	if s.Run != nil {
		return s.Run.KillSwitchSeen()
	}
	var seen int64
	if s.Opened != nil {
		seen = s.Opened.KillSwitchSeen
	}
	if s.Started != nil {
		seen = max(seen, s.Started.KillSwitchSeen)
	}
	if s.Promotion != nil {
		seen = max(seen, s.Promotion.KillSwitchSeen)
	}
	return seen
}

// Owes reports whether the item owes the kill of the on-row onSeq (Decision
// 5): a run that has not stopped, or an item with no run that is queued or
// started, with onSeq above KillSwitchSeen. An item awaiting its close owes
// nothing, nor does a closed one, nor one with no opening.
func (s *State) Owes(onSeq int64) bool {
	switch {
	case s.Opened == nil || onSeq <= s.KillSwitchSeen():
		return false
	case s.Run != nil:
		return s.Run.Stopped == nil
	}
	status := s.Status()
	return status == StatusQueued || status == StatusInvestigating
}
