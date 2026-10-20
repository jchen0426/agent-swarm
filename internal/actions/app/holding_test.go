package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/memory"
	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// guardedBoard is the board as the guard of a holding that is over leaves
// it: an append it is told to refuse fails wrapping holding.ErrLeaseLost. A
// check of a write call ends the holding first, when endsAt is set.
type guardedBoard struct {
	app.Board
	posts, proposals bool
	endsAt           *lease
}

func (b *guardedBoard) PostTask(ctx context.Context, run board.RunID, round int, author board.Author, in *board.TaskInput) (board.Posted, error) {
	if b.posts {
		return board.Posted{}, fmt.Errorf("append to the board of run %s: %w", run, holding.ErrLeaseLost)
	}
	return b.Board.PostTask(ctx, run, round, author, in)
}

func (b *guardedBoard) Proposable(ctx context.Context, run board.RunID, round int, p *board.Proposal) (board.Proposable, error) {
	if b.endsAt != nil {
		b.endsAt.end()
	}
	return b.Board.Proposable(ctx, run, round, p)
}

func (b *guardedBoard) Propose(ctx context.Context, run board.RunID, round int, p *board.Proposal) (board.Posted, error) {
	if b.proposals {
		return board.Posted{}, fmt.Errorf("append to the board of run %s: %w", run, holding.ErrLeaseLost)
	}
	return b.Board.Propose(ctx, run, round, p)
}

// failures counts the calls recorded as failed: refused, or ended in a read
// error.
func (e *env) failures() int {
	n := 0
	for _, id := range e.audit.Calls() {
		if _, out, _, ended := e.audit.Record(id); ended && (out.Status == app.StatusRefused || out.Status == app.StatusReadError) {
			n++
		}
	}
	return n
}

// refusal is a site where the holding stops a call: act makes the call,
// after what the row needs before, and returns its error. requests is how
// many requests reach a server in act, and records how many calls act
// records, all before the holding ended; logged says whether the gateway
// asked the holding, which it logs, or a guard refused a write.
type refusal struct {
	name     string
	change   func(*app.Ports, *app.Options)
	before   func(t *testing.T, e *env)
	act      func(t *testing.T, e *env) error
	requests int
	records  int
	logged   bool
}

// A call the holding stops is answered unavailable and records no failure,
// wherever it is stopped (Plan 7d-1, Decisions 4 and 20): no request reaches
// a server once the holding has ended, a holding's refusal is logged, and a
// write a guard refuses records nothing either.
func TestAHoldingRefusalLeavesNoFailedCall(t *testing.T) {
	conn := fmt.Errorf("%w: connection refused", app.ErrConnection)
	read := func(t *testing.T, e *env) error {
		_, err := e.gw.Call(t.Context(), token, "flags__get_flag", json.RawMessage(`{"flag":"x","window":"1h"}`))
		return err
	}
	var h string
	write := func(t *testing.T, e *env) error {
		_, err := e.gw.Call(t.Context(), token, "flags__set_flag", json.RawMessage(setArgs("x", h)))
		return err
	}
	hypothesis := func(t *testing.T, e *env) {
		e.flags("x")
		h = e.hypothesis(t, "x")
	}
	var guard *guardedBoard
	guarded := func(f func(*guardedBoard)) func(*app.Ports, *app.Options) {
		return func(p *app.Ports, _ *app.Options) {
			guard = &guardedBoard{Board: p.Board}
			f(guard)
			p.Board = guard
		}
	}
	for _, r := range []refusal{
		{name: "a read whose look is due", before: func(t *testing.T, e *env) {
			e.listed(t)
			e.later()
			e.lease.end()
		}, act: read, logged: true},
		{name: "a read whose look is fresh", before: func(t *testing.T, e *env) {
			e.listed(t)
			e.lease.end()
		}, act: read, logged: true},
		{name: "a read tried again after a connection failure", before: func(t *testing.T, e *env) {
			e.listed(t)
			e.up.set(func(u *upstream) { u.errs, u.failed = []error{conn}, e.lease.end })
		}, act: read, requests: 1, records: 1, logged: true},
		{name: "a read the upstream refuses for the holding", before: func(t *testing.T, e *env) {
			e.listed(t)
			e.up.set(func(u *upstream) { u.errs = []error{fmt.Errorf("%w: the holding does not hold", app.ErrUnavailable)} })
		}, act: read, requests: 1, records: 1, logged: true},
		{name: "a write whose look is due", before: func(t *testing.T, e *env) {
			hypothesis(t, e)
			e.later()
			e.lease.end()
		}, act: write, logged: true},
		{name: "a write whose candidates are to be read", change: guarded(func(*guardedBoard) {}), before: func(t *testing.T, e *env) {
			hypothesis(t, e)
			guard.endsAt = e.lease
		}, act: write, records: 1, logged: true},
		{name: "a write whose proposal the guard refuses", change: guarded(func(b *guardedBoard) { b.proposals = true }),
			before: hypothesis, act: write, requests: 1, records: 2},
		{name: "a board post the guard refuses", change: guarded(func(b *guardedBoard) { b.posts = true }), before: func(t *testing.T, e *env) { e.listed(t) }, act: func(t *testing.T, e *env) error {
			_, err := e.gw.Call(t.Context(), token, "post_task", json.RawMessage(`{"title":"check the pool"}`))
			return err
		}, records: 1},
		{name: "a platform read", before: func(_ *testing.T, e *env) { e.lease.end() }, act: func(t *testing.T, e *env) error {
			_, err := e.gw.PlatformRead(e.held(t), app.Origin{Run: "run-1", Commit: e.rev.Commit()}, "flags",
				&config.PlatformRead{Tool: "owners", Field: "$.owners[*].user"}, app.Values{})
			return err
		}, logged: true},
	} {
		t.Run(r.name, func(t *testing.T) {
			logs := &syncBuffer{}
			change := []func(*app.Ports, *app.Options){logging(logs)}
			if r.change != nil {
				change = append(change, r.change)
			}
			e := newEnv(t, change...)
			if r.before != nil {
				r.before(t, e)
			}
			failures, requests, records := e.failures(), e.up.requests(), len(e.audit.Calls())

			err := r.act(t, e)

			require.ErrorIs(t, err, app.ErrUnavailable)
			assert.Equal(t, failures, e.failures(), "no failed call is recorded")
			assert.Equal(t, requests+r.requests, e.up.requests(), "no request reaches a server once the holding ended")
			assert.Len(t, e.audit.Calls(), records+r.records, "no call is recorded once the holding ended")
			refused := logged(t, logs, "holding refused")
			if !r.logged {
				assert.Empty(t, refused)
				return
			}
			require.Len(t, refused, 1)
			assert.InDelta(t, 1, refused[0]["epoch"], 0)
			assert.Equal(t, "mcp", refused[0]["request"])
		})
	}
}

// A listing whose look at a server the holding refuses, at the gateway or at
// the upstream, is answered unavailable: it records no failed tools/list
// call, and no list is kept for the round (Decision 20).
func TestAHoldingRefusalInAListingAnswersUnavailable(t *testing.T) {
	for name, refuse := range map[string]func(e *env){
		"the gateway": func(e *env) { e.lease.end() },
		"the upstream": func(e *env) {
			e.up.set(func(u *upstream) { u.listErr = fmt.Errorf("%w: the holding does not hold", app.ErrUnavailable) })
		},
	} {
		logs := &syncBuffer{}
		e := newEnv(t, logging(logs))
		refuse(e)

		_, err := e.gw.Tools(t.Context(), token)

		require.ErrorIs(t, err, app.ErrUnavailable, name)
		assert.Empty(t, e.audit.Calls(), "%s: no tools/list call is recorded", name)
		assert.Len(t, logged(t, logs, "holding refused"), 1, name)
		e.lease.ended.Store(false)
		e.up.set(func(u *upstream) { u.listErr = nil })
		list, err := e.gw.Tools(t.Context(), token)
		require.NoError(t, err, name)
		assert.Contains(t, names(list), "flags__get_flag", "%s: the next listing makes the round's list", name)
	}
}

// slowAudit holds the first outcome it is given until release is closed,
// and says on started when it has it. Later outcomes pass at once.
type slowAudit struct {
	*memory.Audit
	first            atomic.Bool
	started, release chan struct{}
}

func (a *slowAudit) End(ctx context.Context, o *app.Outcome) error {
	if a.first.CompareAndSwap(false, true) {
		close(a.started)
		<-a.release
	}
	return a.Audit.End(ctx, o)
}

// A drained gateway takes no new call, which it answers unavailable, and
// waits for the writes its calls in flight make apart from them, which land
// within 3 × RecordTimeout (Plan 7d-1, Decision 6).
func TestADrainedGatewayTakesNoNewCallAndFinishesItsWrites(t *testing.T) {
	audit := &slowAudit{Audit: memory.NewAudit(), started: make(chan struct{}), release: make(chan struct{})}
	e := newEnv(t, func(p *app.Ports, o *app.Options) {
		p.Audit = audit
		o.RecordTimeout = 200 * time.Millisecond
	})
	answered := make(chan *app.Response, 1)
	go func() {
		r, _ := e.gw.Call(context.Background(), token, "flags__list_flags", json.RawMessage(`{}`))
		answered <- r
	}()
	<-audit.started
	start := time.Now()
	drained := make(chan error, 1)
	go func() { drained <- e.gw.Drain(t.Context()) }()

	require.Eventually(t, func() bool {
		_, err := e.gw.Tools(t.Context(), token)
		return errors.Is(err, app.ErrUnavailable)
	}, 5*time.Second, time.Millisecond, "a listing is answered unavailable")
	_, err := e.gw.Call(t.Context(), token, "flags__get_flag", json.RawMessage(`{"flag":"x","window":"1h"}`))
	require.ErrorIs(t, err, app.ErrUnavailable, "so is a new call")
	_, err = e.gw.PlatformRead(e.held(t), app.Origin{Run: "run-1", Commit: e.rev.Commit()}, "flags",
		&config.PlatformRead{Tool: "owners", Field: "$.owners[*].user"}, app.Values{})
	require.ErrorIs(t, err, app.ErrUnavailable, "and a platform read")
	select {
	case err := <-drained:
		t.Fatalf("the drain ended (%v) while a write was in flight", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(audit.release)

	require.NoError(t, <-drained)
	assert.Less(t, time.Since(start), 3*200*time.Millisecond)
	r := <-answered
	_, out, _, ended := audit.Record(r.CallID)
	require.True(t, ended, "the write in flight landed")
	assert.Equal(t, app.StatusOK, out.Status)
}

// A drain waits no longer than 3 × RecordTimeout for the calls in flight: a
// call that hangs at its server is left to end on its own.
func TestADrainEndsAtItsBound(t *testing.T) {
	e := newEnv(t, func(_ *app.Ports, o *app.Options) { o.RecordTimeout = 50 * time.Millisecond })
	e.up.set(func(u *upstream) { u.stall = map[string]bool{"list_flags": true} })
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.gw.Call(context.Background(), token, "flags__list_flags", json.RawMessage(`{}`))
	}()
	require.Eventually(t, func() bool { return len(e.up.sent()) == 1 }, 5*time.Second, time.Millisecond)
	start := time.Now()

	err := e.gw.Drain(t.Context())

	require.Error(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 3*50*time.Millisecond)
	assert.Less(t, time.Since(start), time.Second)
	e.sessions.Stop(token)
	<-done
}

// The gateway asks the holding before every request to a server, so it
// cannot be made without one.
func TestTheMCPGatewayCannotBeMadeWithoutAHolding(t *testing.T) {
	var ports app.Ports
	newEnv(t, func(p *app.Ports, _ *app.Options) { ports = *p })
	ports.Holding = nil

	_, err := app.New(t.Context(), &ports, &app.Defaults)

	require.Error(t, err)
}
