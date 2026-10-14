package app

import (
	"context"
	"sync"
)

// flights caps the calls a session has in flight: in all, and to one server
// (spec Section 6.2 sets limits per server; these keep one session from
// taking a server's whole share).
type flights struct {
	mu    sync.Mutex
	count map[flightKey]int
	freed chan struct{}
}

type flightKey struct {
	token  string
	server string // empty for the session's count across servers
}

func newFlights() *flights {
	return &flights{count: map[flightKey]int{}, freed: make(chan struct{})}
}

// take waits until the session has fewer than perSession calls in flight,
// and fewer than perServer to server, then counts one more. It returns the
// release, or ctx's error.
func (f *flights) take(ctx context.Context, token, server string, perSession, perServer int) (func(), error) {
	all, one := flightKey{token, ""}, flightKey{token, server}
	for {
		f.mu.Lock()
		if f.count[all] < perSession && f.count[one] < perServer {
			f.count[all]++
			f.count[one]++
			f.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { f.release(all, one) }) }, nil
		}
		freed := f.freed
		f.mu.Unlock()
		select {
		case <-freed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (f *flights) release(keys ...flightKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		f.count[k]--
		if f.count[k] <= 0 {
			delete(f.count, k)
		}
	}
	close(f.freed)
	f.freed = make(chan struct{})
}
