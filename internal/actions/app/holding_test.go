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

// slowHides holds the first reload of the hides made while slow is set until
// release is closed, or the reload's own context ends, and says on started
// when it holds it. With fail set, the reload it held then fails, as a store
// that went down.
type slowHides struct {
	*memory.Hides
	slow, fail       atomic.Bool
	started, release chan struct{}
}

func (h *slowHides) Hidden(ctx context.Context) ([]app.Hide, error) {
	if h.slow.CompareAndSwap(true, false) {
		close(h.started)
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if h.fail.Load() {
			return nil, errDown
		}
	}
	return h.Hides.Hidden(ctx)
}

// stop stops the run of the session, as the coordinator does, while a
// reload of the hides holds the session's call, and lets the reload end once
// the stop has reached the call: the reload outlives the stop, since it runs
// apart from the call's context.
func (e *env) stopDuring(t *testing.T, hides *slowHides, answered <-chan struct{}) {
	t.Helper()
	<-hides.started
	e.sessions.Stop(token)
	select {
	case <-answered:
		t.Fatal("the call was answered while its hides were reloaded: the stop cut the reload")
	case <-time.After(50 * time.Millisecond):
	}
	close(hides.release)
}

// A call its run's stop cuts short while its hides are reloaded ends as a
// cut, before its record is begun as after, and whether the reload then
// succeeds or fails: the reload runs apart from the call's context, the
// call's row is begun and ended apart from it too, with no side, and the call
// is answered as one cut short, never as unavailable (Decisions 13 and 20).
// The audit store refuses an ended context, as the database does.
func TestACallCutShortWhileTheHidesReloadFailsOnNoSide(t *testing.T) {
	read := func(t *testing.T, e *env) (string, string) {
		e.listed(t)
		return "flags__list_flags", `{}`
	}
	write := func(t *testing.T, e *env) (string, string) {
		e.flags("x")
		return "flags__set_flag", setArgs("x", e.hypothesis(t, "x"))
	}
	for name, c := range map[string]struct {
		before      func(t *testing.T, e *env) (tool, args string)
		reloadFails bool
		status      app.Status
		text        string
	}{
		"a read":                          {read, false, app.StatusReadError, "the run stopped"},
		"a write":                         {write, false, app.StatusRefused, "the candidates of flag could not be read, so the action cannot be checked"},
		"a read whose reload then fails":  {read, true, app.StatusReadError, "the tools hidden for review could not be read, so the call was not made; try again"},
		"a write whose reload then fails": {write, true, app.StatusReadError, "the tools hidden for review could not be read, so the call was not made; try again"},
	} {
		t.Run(name, func(t *testing.T) {
			hides := &slowHides{Hides: &memory.Hides{}, started: make(chan struct{}), release: make(chan struct{})}
			e := newEnv(t, timed, func(p *app.Ports, _ *app.Options) { p.Hides = hides })
			tool, args := c.before(t, e)
			hides.fail.Store(c.reloadFails)
			hides.slow.Store(true)
			e.later()
			sent := len(e.up.sent())
			answered := make(chan struct{})
			var r *app.Response
			var err error
			go func() {
				defer close(answered)
				r, err = e.gw.Call(context.Background(), token, tool, json.RawMessage(args))
			}()

			e.stopDuring(t, hides, answered)
			<-answered

			require.NoError(t, err, "a cut is never unavailable")
			assert.Equal(t, c.status, r.Status)
			assert.Equal(t, c.text, r.Text)
			_, out, began, ended := e.audit.Record(r.CallID)
			require.True(t, began && ended, "the call is recorded")
			assert.Equal(t, c.status, out.Status)
			assert.Empty(t, out.Failure, "a call cut short fails on no side")
			assert.Len(t, e.up.sent(), sent, "nothing reaches a server once the call is cut")
		})
	}
}

// platformBegins fails to begin the platform's own reads while failing is
// set, as a store that went down.
type platformBegins struct {
	timedAudit
	failing atomic.Bool
}

func (a *platformBegins) Begin(ctx context.Context, rec *app.CallRecord) error {
	if a.failing.Load() && rec.Agent == "" {
		return errDown
	}
	return a.timedAudit.Begin(ctx, rec)
}

// gatedChecks holds the first check of a write call made while gate is set
// until open is closed, and says on waiting when it holds it.
type gatedChecks struct {
	app.Board
	gate          atomic.Bool
	waiting, open chan struct{}
}

func (b *gatedChecks) Proposable(ctx context.Context, run board.RunID, round int, p *board.Proposal) (board.Proposable, error) {
	if b.gate.CompareAndSwap(true, false) {
		close(b.waiting)
		<-b.open
	}
	return b.Board.Proposable(ctx, run, round, p)
}

// A write whose candidate read its run's stop cuts short while that read's
// hides are reloaded is refused as a cut, on no side, never the platform's:
// whether the read's record is begun, or the store refuses it then, or the
// reload then fails.
func TestAWriteWhoseCandidateReadIsCutDuringTheReloadFailsOnNoSide(t *testing.T) {
	for name, c := range map[string]struct{ storeDown, reloadFails bool }{
		"the read's record begun":   {},
		"the read's record refused": {storeDown: true},
		"the read's reload failing": {reloadFails: true},
	} {
		t.Run(name, func(t *testing.T) {
			hides := &slowHides{Hides: &memory.Hides{}, started: make(chan struct{}), release: make(chan struct{})}
			audit := &platformBegins{}
			checks := &gatedChecks{waiting: make(chan struct{}), open: make(chan struct{})}
			e := newEnv(t, func(p *app.Ports, _ *app.Options) {
				audit.timedAudit = timedAudit{p.Audit.(*memory.Audit)}
				checks.Board = p.Board
				p.Audit, p.Hides, p.Board = audit, hides, checks
			})
			e.flags("x")
			h := e.hypothesis(t, "x")
			checks.gate.Store(true)
			answered := make(chan struct{})
			var r *app.Response
			var err error
			go func() {
				defer close(answered)
				r, err = e.gw.Call(context.Background(), token, "flags__set_flag", json.RawMessage(setArgs("x", h)))
			}()
			<-checks.waiting
			// The write's own look is fresh; its candidate read looks again,
			// which makes the hides due: the reload is the candidate read's.
			e.later()
			hides.fail.Store(c.reloadFails)
			hides.slow.Store(true)
			audit.failing.Store(c.storeDown)
			close(checks.open)

			e.stopDuring(t, hides, answered)
			<-answered

			require.NoError(t, err, "a cut is never unavailable")
			assert.Equal(t, app.StatusRefused, r.Status)
			_, out, began, ended := e.audit.Record(r.CallID)
			require.True(t, began && ended, "the write is recorded")
			assert.Empty(t, out.Failure, "a cut is no failure, never the platform's")
			assert.Empty(t, e.proposals(t))
		})
	}
}

// A board call its round's end cut before its record was begun ends as a
// cut: its row is begun and ended apart from its context, on no side, and it
// is answered as a cut, which says its turn is over, never as unavailable.
func TestABoardCallCutBeforeItsRecordEndsAsACut(t *testing.T) {
	e := newEnv(t, timed, func(p *app.Ports, _ *app.Options) { p.Board = timedBoard{p.Board} })
	e.listed(t)
	// The round ends just after the gateway's clock, which the call's own
	// check reads, so the call goes on; on the process's clock, which its
	// context keeps, the round has ended by then.
	e.session.Deadline = e.clock.now().Add(time.Millisecond)
	e.sessions.Put(token, &e.session)
	time.Sleep(2 * time.Millisecond)

	r, err := e.gw.Call(t.Context(), token, "post_task", json.RawMessage(`{"title":"check the pool"}`))

	require.NoError(t, err, "a cut is never unavailable")
	assert.Equal(t, app.StatusReadError, r.Status, r.Text)
	assert.True(t, r.TurnOver)
	_, out, began, ended := e.audit.Record(e.lastCall(t))
	require.True(t, began && ended, "the call is recorded")
	assert.Empty(t, out.Failure)
	for _, ev := range e.events(t) {
		assert.Nil(t, ev.Body.Task, "nothing is posted")
	}
}

// stallingBoard holds the first read of a run's board context made while
// stall is set until the read's context ends, and says on reading when it
// holds it.
type stallingBoard struct {
	app.Board
	stall   atomic.Bool
	reading chan struct{}
}

func (b *stallingBoard) Context(ctx context.Context, run board.RunID) (board.Context, error) {
	if b.stall.CompareAndSwap(true, false) {
		close(b.reading)
		<-ctx.Done()
		return board.Context{}, ctx.Err()
	}
	return b.Board.Context(ctx, run)
}

// A session whose run stops while its board is read for its tools is cut
// short, not unavailable (Decision 13): a listing answers that the session
// is gone, and a call is answered and recorded as cut short, on no side.
func TestASessionStoppedWhileItsBoardIsReadIsCutShort(t *testing.T) {
	var stalling *stallingBoard
	stall := func(p *app.Ports, _ *app.Options) {
		stalling = &stallingBoard{Board: p.Board, reading: make(chan struct{})}
		stalling.stall.Store(true)
		p.Board = stalling
	}
	t.Run("a listing", func(t *testing.T) {
		e := newEnv(t, stall)
		listed := make(chan error, 1)
		go func() {
			_, err := e.gw.Tools(context.Background(), token)
			listed <- err
		}()
		<-stalling.reading

		e.sessions.Stop(token)
		err := <-listed

		require.ErrorIs(t, err, app.ErrNoSession)
		assert.NotErrorIs(t, err, app.ErrUnavailable)
	})
	t.Run("a call", func(t *testing.T) {
		e := newEnv(t, stall)
		answered := make(chan error, 1)
		var r *app.Response
		go func() {
			var err error
			r, err = e.gw.Call(context.Background(), token, "flags__list_flags", json.RawMessage(`{}`))
			answered <- err
		}()
		<-stalling.reading

		e.sessions.Stop(token)
		err := <-answered

		require.NoError(t, err)
		assert.Equal(t, app.StatusReadError, r.Status)
		assert.Equal(t, "the run stopped", r.Text)
		_, out, began, ended := e.audit.Record(r.CallID)
		require.True(t, began && ended, "the call is recorded")
		assert.Empty(t, out.Failure, "cut short, on no side")
	})
}

// A request to a server under a context without an epoch is a bug, not a
// holding's refusal (Decision 4): it is refused, and logged as a bug.
func TestARequestWithoutAnEpochIsLoggedAsABug(t *testing.T) {
	logs := &syncBuffer{}
	e := newEnv(t, logging(logs))

	_, err := e.gw.PlatformRead(t.Context(), app.Origin{Run: "run-1", Commit: e.rev.Commit()}, "flags",
		&config.PlatformRead{Tool: "owners", Field: "$.owners[*].user"}, app.Values{})

	require.ErrorIs(t, err, app.ErrUnavailable)
	assert.Zero(t, e.up.requests())
	assert.Empty(t, logged(t, logs, "holding refused"))
	bug := logged(t, logs, "request without an epoch")
	require.Len(t, bug, 1)
	assert.Equal(t, "ERROR", bug[0]["level"])
	assert.Equal(t, "mcp", bug[0]["request"])
}

// A drain of a gateway with no call in its hands succeeds, whatever its
// context: one whose context has ended too is still drained.
func TestADrainOfAnIdleGatewaySucceedsWhateverItsContext(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	for range 1000 {
		require.NoError(t, e.gw.Drain(ctx))
	}
}
