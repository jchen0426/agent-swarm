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

	at := func(offAt time.Time) time.Time {
		t.Helper()
		due, ok := l.s.AutoCloseAt(delay, offAt)
		assert.True(t, ok, "an item a person opened has no trigger to fire")
		return due
	}
	assert.Equal(t, t2.Add(delay), at(time.Time{}))
	assert.Equal(t, t2.Add(delay), at(t1), "an off before the entry")
	off := t2.Add(5 * time.Minute)
	assert.Equal(t, off.Add(delay), at(off), "an off after the entry")
}

// An item owes the kill of an on-row above the latest kill switch row the
// coordinator knew for it (Decision 5): with no run, the latest that its
// opening, its promotion and its start knew, while it is queued or started;
// with a run, the one its run's creation knew, while the run has not stopped.
// An item awaiting its close owes nothing, nor does a closed one, nor one with
// no opening.
func TestAnItemOwesTheKillOfAnOnRowAboveTheLatestRowItKnew(t *testing.T) {
	assert.False(t, (&item.State{}).Owes(1), "an item with no opening")
	l := &life{t: t, s: &item.State{}, now: t0}
	l.write("", item.Body{Opened: &item.Opened{Type: "incident", Title: "checkout fails", Source: "person", Arm: item.ArmVisible, How: item.NoWindow, KillSwitchSeen: 2}})
	owes := func(what string, seen int64) {
		t.Helper()
		assert.Equal(t, seen, l.s.KillSwitchSeen(), what)
		assert.False(t, l.s.Owes(seen), "%s: the row it knew", what)
		assert.True(t, l.s.Owes(seen+1), "%s: a later on-row", what)
	}

	owes("queued, its opening's", 2)
	p := promoted()
	p.KillSwitchSeen = 4
	l.write("", item.Body{Promoted: p})
	owes("queued, its promotion's", 4)
	l.write("", item.Body{Started: &item.Started{Commit: "c1", Digest: "d1", Images: map[string]string{"thinloop": "sha256:aa"}, KillSwitchSeen: 5}})
	owes("started with no run, its start's", 5)
	rc := created()
	rc.KillSwitchSeen = 7
	l.write("run-1", item.Body{RunCreated: rc})
	owes("its run's, while the run runs", 7)
	l.open()
	l.end(time.Minute, quiet())
	stop := l.s.Run.Stop("")
	l.write("run-1", item.Body{RunStopped: &stop})
	assert.False(t, l.s.Owes(8), "its run stopped")
	l.write("", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopQuiet}})
	assert.False(t, l.s.Owes(8), "it awaits its close")
	l.write("", item.Body{Closed: closedBy()})
	assert.False(t, l.s.Owes(8), "it is closed")

	unstaffed := newOpened(t)
	unstaffed.write("", item.Body{AwaitingClose: notStaffed(item.CauseDailyCap)})
	assert.False(t, unstaffed.s.Owes(1), "an item awaiting its close not staffed, with no run")
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

	resolved, ok := l.s.ResolvedAt()
	assert.True(t, ok)
	assert.Equal(t, l.s.OpenedAt, resolved)
	assert.Equal(t, t0, resolved)
	assert.Equal(t, t0.Add(2*time.Minute), l.s.AwaitingSince)
	due, ok := l.s.AutoCloseAt(delay, time.Time{})
	assert.True(t, ok)
	assert.Equal(t, l.s.AwaitingSince.Add(delay), due)
}
