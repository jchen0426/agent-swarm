package item_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// Rulings follow spec Section 4.2's list on a closed item (Decision 9, R30):
// reject, un-reject and dismiss are open while the item is not closed, and
// shut once it is, so that a person reopens it first; any other kind, a late
// one, is open while the item is not closed and until its close plus the
// window, and shut after. A reopened item takes reject again.
func TestRulingsOpenFollowsTheSpecsListOnAClosedItem(t *testing.T) {
	const window = 72 * time.Hour
	adoption := board.RulingKind("adoption")
	early := []board.RulingKind{board.RulingReject, board.RulingUnreject, board.RulingDismiss}
	l := newOpened(t)
	for _, kind := range append(early, adoption) {
		assert.True(t, l.s.RulingsOpen(kind, t0.Add(time.Hour), window), "%s on an item not closed", kind)
	}

	l.now = t0.Add(time.Hour)
	l.write("", item.Body{Closed: closedBy()})
	closedAt := l.s.ClosedAt
	for _, kind := range early {
		assert.False(t, l.s.RulingsOpen(kind, closedAt, window), "%s on a closed item", kind)
	}
	assert.True(t, l.s.RulingsOpen(adoption, closedAt, window), "a late kind at the close")
	assert.True(t, l.s.RulingsOpen(adoption, closedAt.Add(window), window), "a late kind until the window's end")
	assert.False(t, l.s.RulingsOpen(adoption, closedAt.Add(window+time.Second), window), "a late kind after the window")

	l.now = closedAt.Add(time.Hour)
	l.write("", item.Body{Reopened: reopened(notStaffed(item.CauseClosed))})
	assert.True(t, l.s.RulingsOpen(board.RulingReject, l.now, window), "reject on a reopened item")
}

// Auto-close counts from the item's latest entry into awaiting_close (R19):
// an item that awaited its close at T1, was promoted, and awaits it again at
// T2 closes by itself the delay after T2; after the kill switch's latest
// off, when that comes later, the delay after it.
func TestAutoCloseCountsFromTheLatestEntryIntoAwaitingClose(t *testing.T) {
	const delay = 30 * time.Minute
	t1, t2 := t0.Add(time.Hour), t0.Add(2*time.Hour)
	l := newOpened(t)
	l.now = t1
	l.write("", item.Body{AwaitingClose: notStaffed(item.CauseDailyCap)})
	l.now = t1.Add(10 * time.Minute)
	l.write("", item.Body{Promoted: promoted()})
	l.now = t2
	l.write("", item.Body{AwaitingClose: notStaffed(item.CauseType)})

	assert.Equal(t, t2.Add(delay), l.s.AutoCloseAt(delay, time.Time{}))
	assert.Equal(t, t2.Add(delay), l.s.AutoCloseAt(delay, t1), "an off before the entry")
	off := t2.Add(5 * time.Minute)
	assert.Equal(t, off.Add(delay), l.s.AutoCloseAt(delay, off), "an off after the entry")
}

// An item a person opened has no trigger, which reads as resolved when the
// item opened (Decision 8, R19), so it closes by itself the delay after its
// entry into awaiting_close, which follows its opening.
func TestAnItemOpenedByAPersonClosesByItselfFromItsEntryIntoAwaitingClose(t *testing.T) {
	const delay = 30 * time.Minute
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, quiet())
	stop := l.s.Run.Stop("")
	l.write("run-1", item.Body{RunStopped: &stop})
	l.now = l.now.Add(time.Minute)
	l.write("", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopQuiet, AutoClose: delay}})

	assert.Equal(t, l.s.OpenedAt, l.s.ResolvedAt())
	assert.Equal(t, t0, l.s.ResolvedAt())
	assert.Equal(t, t0.Add(2*time.Minute), l.s.AwaitingSince)
	assert.Equal(t, l.s.AwaitingSince.Add(delay), l.s.AutoCloseAt(delay, time.Time{}))
}
