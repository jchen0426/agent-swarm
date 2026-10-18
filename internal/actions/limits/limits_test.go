package limits_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/limits"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newServer(t *testing.T) (*limits.Server, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)}
	return limits.New(limits.Defaults, nil, c.now).Server("metrics"), c
}

// take acquires n slots of class c and fails the test if any has to wait.
func take(t *testing.T, s *limits.Server, c limits.Class, n int) []func() {
	t.Helper()
	var releases []func()
	for range n {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		release, err := s.Acquire(ctx, c)
		cancel()
		require.NoError(t, err)
		releases = append(releases, release)
	}
	return releases
}

// waits reports whether a call of class c finds no free slot.
func waits(t *testing.T, s *limits.Server, c limits.Class) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	release, err := s.Acquire(ctx, c)
	if err == nil {
		release()
		return false
	}
	require.ErrorIs(t, err, context.DeadlineExceeded)
	return true
}

func TestAgentsLeaveTheReservedSlotsToPlatformReads(t *testing.T) {
	s, _ := newServer(t)

	take(t, s, limits.Primary, 8)

	assert.True(t, waits(t, s, limits.Primary), "two of ten slots are reserved for platform reads")
	take(t, s, limits.Platform, 2)
	assert.True(t, waits(t, s, limits.Platform), "every slot is in use")
}

func TestComparisonAndProbationRunsHoldAtMostHalfTheSlots(t *testing.T) {
	s, _ := newServer(t)

	take(t, s, limits.Shadow, 5)

	assert.True(t, waits(t, s, limits.Shadow))
	take(t, s, limits.Primary, 3)
	assert.True(t, waits(t, s, limits.Primary), "the agents' share is full")
}

func TestAReleasedSlotGoesToAWaitingCall(t *testing.T) {
	s, _ := newServer(t)
	releases := take(t, s, limits.Primary, 8)
	got := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := s.Acquire(ctx, limits.Primary)
		got <- err
	}()

	releases[0]()
	releases[0]() // a second release frees nothing more

	require.NoError(t, <-got)
	assert.True(t, waits(t, s, limits.Primary))
}

func TestConsecutiveConnectionFailuresDegradeAServerForTheCoolDown(t *testing.T) {
	s, c := newServer(t)

	s.Failed()
	s.Failed()
	s.Failed()

	assert.True(t, s.Degraded())
	_, err := s.Acquire(t.Context(), limits.Primary)
	require.ErrorIs(t, err, limits.ErrDegraded, "a primary run's call fails fast")
	assert.True(t, waits(t, s, limits.Shadow), "a comparison or probation run pauses")
	take(t, s, limits.Platform, 1)
	c.advance(60 * time.Second)
	assert.False(t, s.Degraded())
	take(t, s, limits.Primary, 1)
	take(t, s, limits.Shadow, 1)
}

func TestACallThatReachesTheServerEndsARunOfFailures(t *testing.T) {
	s, _ := newServer(t)

	s.Failed()
	s.Failed()
	s.Succeeded()
	s.Failed()

	assert.False(t, s.Degraded())
}

func TestALoadTrialMaySetAServersSlots(t *testing.T) {
	c := &clock{}
	l := limits.New(limits.Defaults, map[string]int{"metrics": 2, "logs": 1}, c.now)

	s := l.Server("metrics")

	take(t, s, limits.Primary, 1)
	assert.True(t, waits(t, s, limits.Primary), "one of two slots is reserved")
	take(t, s, limits.Platform, 1)
	assert.Same(t, s, l.Server("metrics"))
	take(t, l.Server("logs"), limits.Primary, 1)
	assert.True(t, waits(t, l.Server("logs"), limits.Primary), "a server has at least two slots, so agents keep one")
}

func TestAPausedComparisonRunGoesOnWhenTheCoolDownEnds(t *testing.T) {
	s := limits.New(limits.Settings{Slots: 10, Failures: 1, CoolDown: 50 * time.Millisecond}, nil, time.Now).Server("metrics")
	s.Failed()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	release, err := s.Acquire(ctx, limits.Shadow)

	require.NoError(t, err)
	release()
}

// A server that serves agents only, as the model vendor does, reserves no
// slots for platform reads; comparison and probation runs still hold at
// most half.
func TestAServerOfAgentsOnlyReservesNothing(t *testing.T) {
	settings := limits.Defaults
	settings.AgentsOnly = true
	s := limits.New(settings, nil, time.Now).Server("model vendor")

	take(t, s, limits.Shadow, 5)
	assert.True(t, waits(t, s, limits.Shadow))
	take(t, s, limits.Primary, 5)
	assert.True(t, waits(t, s, limits.Primary))
}
