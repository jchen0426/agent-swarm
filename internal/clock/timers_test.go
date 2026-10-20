package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/jchen0426/agent-swarm/internal/clock"
)

// A timer's function never runs before At returns, even when its time has
// passed: a caller that arms timers under the lock their functions take, as
// the coordinator and the leader do, keeps what At returned before any
// function runs. A function run within At, on the caller's goroutine, would
// wait for the caller's lock forever.
func TestAtNeverRunsItsFunctionBeforeItReturns(t *testing.T) {
	timers := clock.NewTimers(clock.Monotonic{})
	var mu sync.Mutex
	var wg sync.WaitGroup
	cancels := make([]func(), 1000)
	early := 0
	armed := make(chan struct{})

	go func() {
		defer close(armed)
		for i := range cancels {
			wg.Add(1)
			mu.Lock()
			cancels[i] = timers.At(time.Now().Add(-time.Second), func() {
				defer wg.Done()
				mu.Lock()
				defer mu.Unlock()
				if cancels[i] == nil {
					early++
				}
			})
			mu.Unlock()
		}
	}()
	select {
	case <-armed:
	case <-time.After(5 * time.Second):
		t.Fatal("At ran a function before it returned")
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Zero(t, early, "functions that ran before At returned")
}

// Cancelling a timer whose function runs does not wait for the function, so
// that a caller may cancel under the lock the function waits for.
func TestCancelNeverWaitsForARunningFunction(t *testing.T) {
	timers := clock.NewTimers(clock.Monotonic{})
	running, letGo := make(chan struct{}), make(chan struct{})
	defer close(letGo)
	cancel := timers.At(time.Now(), func() {
		close(running)
		<-letGo
	})
	<-running

	took := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		cancel()
		took <- time.Since(start)
	}()

	select {
	case d := <-took:
		assert.Less(t, d, 10*time.Millisecond)
	case <-time.After(time.Second):
		t.Fatal("cancel waited for the function")
	}
}

// Each timer's function runs on a goroutine of its own: two due at once, each
// of which waits for the other to start, both finish.
func TestFunctionsRunConcurrently(t *testing.T) {
	timers := clock.NewTimers(clock.Monotonic{})
	first, second := make(chan struct{}), make(chan struct{})
	finished := make(chan struct{}, 2)
	at := time.Now().Add(10 * time.Millisecond)

	timers.At(at, func() {
		close(first)
		<-second
		finished <- struct{}{}
	})
	timers.At(at, func() {
		close(second)
		<-first
		finished <- struct{}{}
	})

	for range 2 {
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("the functions did not run concurrently")
		}
	}
}
