package clock

import "time"

// Timers is app.Timers on a clock. A timer waits the time from the clock's
// Now, when it is armed, until its time, on the process's own timers: a
// correction of the clock afterwards leaves it as it is. Each function runs on
// a goroutine of its own, concurrently with the others and with the caller,
// and never before At returns. Cancelling never waits for a function that
// runs: a function whose time came may run after its timer was cancelled, so
// it checks, under the lock it takes, that it is still wanted.
type Timers struct {
	clock interface{ Now() time.Time }
}

// NewTimers returns timers on c, the coordinator's clock or the leader's.
func NewTimers(c interface{ Now() time.Time }) *Timers {
	return &Timers{clock: c}
}

// At runs fn at the clock's time at, or as soon as it can once at has passed,
// and returns a function that cancels it.
func (t *Timers) At(at time.Time, fn func()) (cancel func()) {
	gate := make(chan struct{})
	timer := time.AfterFunc(at.Sub(t.clock.Now()), func() {
		<-gate
		fn()
	})
	close(gate)
	return func() { timer.Stop() }
}
