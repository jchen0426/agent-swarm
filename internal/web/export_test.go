package web

import "time"

// SetMonotonic sets the clock of the windows the servers made after it keep,
// the Host refusal's log once a minute, and returns the function that
// restores it.
func SetMonotonic(f func() time.Time) (restore func()) {
	old := monotonic
	monotonic = f
	return func() { monotonic = old }
}
