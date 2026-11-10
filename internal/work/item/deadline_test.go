package item_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// waits are a round end's waits in these tests, each of its own number of
// seconds, so that a time that missed one, or counted one twice, reads
// another time; w is the formula's W, their sum.
var waits = item.Waits{Grace: 5 * time.Second, Settle: 30 * time.Second, Poll: time.Second, Retry: 7 * time.Second}

const w = 43 * time.Second

// latest is the time of the life's latest record.
func (l *life) latest() time.Time {
	return l.records[len(l.records)-1].At
}

// The latest time a run stops is computed forward from its records (design
// D5): T = D + W + max(0, B - U) + n*W + k*(L + W), where D is the open
// round's deadline, or the latest record's time between rounds; B the wall
// clock with every extension and U the counted wall clock, an open normal
// round's to D; n the normal rounds the budget still opens; k the wrap-up
// rounds the run may still have; and L the round's limit. created's budget
// is 3 rounds of at most 2 minutes, and 10 minutes of wall clock (the plan's
// table).
func TestStopByIsComputedForwardFromTheRecords(t *testing.T) {
	for _, tc := range []struct {
		name string
		// life drives the row's run and returns it, with the waits it is read
		// under and the time the formula gives.
		life func(t *testing.T) (l *life, w item.Waits, want time.Time)
	}{
		{"an open normal round: D its deadline, U to D, n less one, k 2", func(t *testing.T) (*life, item.Waits, time.Time) {
			l := newLife(t, created())
			o := l.open()
			return l, waits, o.Deadline.Add(w + (10*time.Minute - 2*time.Minute) + 2*w + 2*(2*time.Minute+w))
		}},
		{"an open wrap-up round: U without it, k 1", func(t *testing.T) (*life, item.Waits, time.Time) {
			l := newLife(t, created())
			l.open()
			l.end(time.Minute, converging(hyp))
			o := l.open()
			return l, waits, o.Deadline.Add(w + (10*time.Minute - time.Minute) + 2*w + (2*time.Minute + w))
		}},
		{"between rounds: D the latest record's time", func(t *testing.T) (*life, item.Waits, time.Time) {
			l := newLife(t, created())
			l.open()
			l.end(time.Minute, busy())
			return l, waits, l.latest().Add(w + (10*time.Minute - time.Minute) + 2*w + 2*(2*time.Minute+w))
		}},
		{"a resumed run: the extension in B, n from it", func(t *testing.T) (*life, item.Waits, time.Time) {
			rc := created()
			rc.Budget.Rounds = 1
			l := newLife(t, rc)
			l.open()
			l.end(time.Minute, busy())
			l.stopAndAwait()
			l.extend(oneRound, 0)
			return l, waits, l.latest().Add(w + (10*time.Minute + 5*time.Minute - time.Minute) + w + 2*(2*time.Minute+w))
		}},
		{"a wrap-up ended by an outage: k = min(2 - 1, 4 - 2)", func(t *testing.T) (*life, item.Waits, time.Time) {
			l := newLife(t, created())
			l.open()
			l.end(time.Minute, converging(hyp))
			l.open()
			outage := converging(hyp)
			outage.EndedBy = item.EndedByOutage
			l.end(2*time.Minute, outage)
			o := l.open()
			return l, waits, o.Deadline.Add(w + (10*time.Minute - time.Minute) + 2*w + (2*time.Minute + w))
		}},
		{"wrap-up rounds that outages used up: k = min(2 - 1, 4 - 4) (the plan's)", func(t *testing.T) (*life, item.Waits, time.Time) {
			l := newLife(t, created())
			l.open()
			l.end(time.Minute, converging(hyp))
			for range 3 {
				l.open()
				outage := converging(hyp)
				outage.EndedBy = item.EndedByOutage
				l.end(2*time.Minute, outage)
			}
			o := l.open()
			return l, waits, o.Deadline.Add(w + (10*time.Minute - time.Minute) + 2*w)
		}},
		{"no budget left: max 0 of a wall clock used past it, n 0", func(t *testing.T) (*life, item.Waits, time.Time) {
			rc := created()
			rc.Budget.Rounds = 2
			l := newLife(t, rc)
			l.open()
			l.end(6*time.Minute, busy())
			l.open()
			l.end(6*time.Minute, busy())
			return l, waits, l.latest().Add(w + 2*(2*time.Minute+w))
		}},
		{"W holds Poll", func(t *testing.T) (*life, item.Waits, time.Time) {
			l := newLife(t, created())
			o := l.open()
			poll := time.Second
			return l, item.Waits{Poll: poll}, o.Deadline.Add(poll + (10*time.Minute - 2*time.Minute) + 2*poll + 2*(2*time.Minute+poll))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, w, want := tc.life(t)
			assert.Equal(t, want, l.s.Run.StopBy(l.latest(), w))
		})
	}
}

// Confirming is the open wrap-up round's hypothesis, and ConfirmingUntil its
// deadline plus Grace, Settle and Poll (R146): a normal round, an ended
// round and a stopped run have neither.
func TestConfirmingIsTheOpenWrapUpRoundsAlone(t *testing.T) {
	l := newLife(t, created())
	none := func(why string) {
		t.Helper()
		assert.Empty(t, l.s.Run.Confirming(), why)
		until, ok := l.s.Run.ConfirmingUntil(waits)
		assert.False(t, ok, why)
		assert.Zero(t, until, why)
	}

	l.open()
	none("a normal round")
	l.end(time.Minute, converging(hyp))
	none("an ended round")

	o := l.open()
	assert.Equal(t, hyp, l.s.Run.Confirming())
	until, ok := l.s.Run.ConfirmingUntil(waits)
	assert.True(t, ok)
	assert.Equal(t, o.Deadline.Add(waits.Grace+waits.Settle+waits.Poll), until, "no retry wait")

	l.end(time.Minute, converging(hyp))
	none("the wrap-up round ended")
	l.stopAndAwait()
	none("the run stopped")
}
