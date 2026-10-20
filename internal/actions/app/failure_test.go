package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/actions/limits"
	"github.com/jchen0426/agent-swarm/internal/actions/memory"
	"github.com/jchen0426/agent-swarm/internal/actions/output"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// errDown is how a store or a board that cannot be reached fails: with an
// error that breaks no board rule.
var errDown = errors.New("database down")

// unreachableBoard cannot be reached for what it is told to fail: a write
// call's check, its proposal, an end of a turn, or a read of the board. When
// it stalls, it fails only once its caller gives up.
type unreachableBoard struct {
	app.Board
	checks, proposals, posts, views bool
	stall                           bool
}

// down is how the board fails a request.
func (b *unreachableBoard) down(ctx context.Context) error {
	if b.stall {
		<-ctx.Done()
		return ctx.Err()
	}
	return errDown
}

func (b *unreachableBoard) Proposable(ctx context.Context, run board.RunID, round int, p *board.Proposal) (board.Proposable, error) {
	if b.checks {
		return board.Proposable{}, b.down(ctx)
	}
	return b.Board.Proposable(ctx, run, round, p)
}

func (b *unreachableBoard) Propose(ctx context.Context, run board.RunID, round int, p *board.Proposal) (board.Posted, error) {
	if b.proposals {
		return board.Posted{}, b.down(ctx)
	}
	return b.Board.Propose(ctx, run, round, p)
}

func (b *unreachableBoard) EndTurn(ctx context.Context, run board.RunID, round int, author board.Author) (board.Posted, error) {
	if b.posts {
		return board.Posted{}, b.down(ctx)
	}
	return b.Board.EndTurn(ctx, run, round, author)
}

func (b *unreachableBoard) View(ctx context.Context, run board.RunID, agent string, since int64) (board.View, error) {
	if b.views {
		return board.View{}, b.down(ctx)
	}
	return b.Board.View(ctx, run, agent, since)
}

// shortRound ends the session's round in 300 ms.
func (e *env) shortRound() {
	e.session.Deadline = time.Now().Add(300 * time.Millisecond)
	e.sessions.Put(token, &e.session)
}

// unrecordedAudit cannot record a call, and keeps every outcome it is given,
// so that an outcome recorded without its call shows.
type unrecordedAudit struct {
	*memory.Audit
	ended atomic.Int32
}

func (*unrecordedAudit) Begin(context.Context, *app.CallRecord) error { return errDown }

func (a *unrecordedAudit) End(ctx context.Context, o *app.Outcome) error {
	a.ended.Add(1)
	return a.Audit.End(ctx, o)
}

// lastCall returns the id of the call recorded last.
func (e *env) lastCall(t *testing.T) string {
	t.Helper()
	ids := e.audit.Calls()
	require.NotEmpty(t, ids)
	return ids[len(ids)-1]
}

// blocked starts a read of list_flags that holds its slots, the session's
// and the server's, until the returned function ends it.
func (e *env) blocked(t *testing.T) (end func()) {
	t.Helper()
	block, done := make(chan struct{}), make(chan struct{})
	sent := len(e.up.sent())
	e.up.set(func(u *upstream) { u.onCall = func(call) { <-block } })
	go func() {
		defer close(done)
		_, _ = e.gw.Call(context.Background(), token, "flags__list_flags", json.RawMessage(`{}`))
	}()
	require.Eventually(t, func() bool { return len(e.up.sent()) == sent+1 }, 5*time.Second, time.Millisecond)
	return func() {
		close(block)
		<-done
	}
}

// listed lists the session's tools, so that a call's look at its server is
// the call's own.
func (e *env) listed(t *testing.T) {
	t.Helper()
	_, err := e.gw.Tools(t.Context(), token)
	require.NoError(t, err)
}

// setArgs writes the arguments of a set_flag call that cites hypothesis h.
func setArgs(flag, h string) string { return fmt.Sprintf(`{"flag":%q,"_hypothesis":%q}`, flag, h) }

// withLogsCandidates is the configuration with the logs server, whose query
// tool gives set_flag's candidates.
func withLogsCandidates(t *testing.T) map[string][]byte {
	t.Helper()
	files := withLogs(t, revisionFiles(t, defaultPins(), "reviewed"))
	server := string(files["servers/flags.yaml"])
	require.Contains(t, server, "flag: {tool: list_flags,")
	files["servers/flags.yaml"] = []byte(strings.Replace(server, "flag: {tool: list_flags,", "flag: {tool: logs.query,", 1))
	return files
}

// failureSite is one place where a call of the MCP gateway can fail
// (Decision 20): act makes the call there, and returns its id.
type failureSite struct {
	name   string
	change func(*app.Ports, *app.Options)
	act    func(t *testing.T, e *env) string
	want   app.Failure
}

// Every failure site of the MCP gateway records the side its failure was on
// (Decision 20): for reads, writes, board calls, listings and the platform's
// own reads. A call cut short by its round's deadline or its run's stop
// records none. The table has a row for each site of the decision's table
// but a hides reload that fails, which the gateway does not make yet, and
// rows for the other ways the gateway refuses a call or cuts it short.
func TestEveryFailureSiteRecordsItsSide(t *testing.T) {
	conn := fmt.Errorf("%w: connection refused", app.ErrConnection)
	failed := func(errs ...error) func(*upstream) { return func(u *upstream) { u.errs = errs } }
	short := func(_ *app.Ports, o *app.Options) { o.CallTimeout = 200 * time.Millisecond }
	oneAtATime := func(_ *app.Ports, o *app.Options) { o.SessionCalls, o.CallTimeout = 1, 200*time.Millisecond }
	var sessions *failingSessions
	sites := []failureSite{
		// The server's side: a read.
		{"a read whose server cannot be reached", nil, func(t *testing.T, e *env) string {
			e.up.set(failed(conn, conn, conn))
			r := e.call(t, "flags__list_flags", `{}`)
			require.Equal(t, "flags could not be reached", r.Text)
			return r.CallID
		}, app.FailureServer},
		{"a read of a degraded server, which fails fast", nil, func(t *testing.T, e *env) string {
			for range 3 {
				e.up.set(failed(conn, conn, conn))
				e.call(t, "flags__list_flags", `{}`)
			}
			r := e.call(t, "flags__list_flags", `{}`)
			require.Contains(t, r.Text, "degraded")
			return r.CallID
		}, app.FailureServer},
		{"a read whose server gives no answer in the call's time", short, func(t *testing.T, e *env) string {
			e.up.set(func(u *upstream) { u.stall = map[string]bool{"list_flags": true} })
			r := e.call(t, "flags__list_flags", `{}`)
			require.Contains(t, r.Text, "no answer in time")
			return r.CallID
		}, app.FailureServer},
		{"a read answered with an HTTP 5xx", nil, func(t *testing.T, e *env) string {
			answered := fmt.Errorf("%w: the server answered 503", app.ErrConnection)
			e.up.set(failed(answered, answered, answered))
			r := e.call(t, "flags__list_flags", `{}`)
			require.Equal(t, "flags could not be reached", r.Text)
			return r.CallID
		}, app.FailureServer},
		{"a read answered with a JSON-RPC error other than invalid parameters", nil, func(t *testing.T, e *env) string {
			e.up.set(failed(&app.ServerError{Message: "the flag store is down", Code: -32603}))
			r := e.call(t, "flags__list_flags", `{}`)
			require.Equal(t, "flags answered with an error: the flag store is down", r.Text)
			return r.CallID
		}, app.FailureServer},
		{"a read that finds its server's slots full until its time ends", func(p *app.Ports, o *app.Options) {
			p.Limits = limits.New(limits.Settings{Slots: 2, Failures: 3, CoolDown: time.Minute}, nil, p.Now)
			o.CallTimeout = 200 * time.Millisecond
		}, func(t *testing.T, e *env) string {
			end := e.blocked(t)
			r := e.call(t, "flags__get_flag", `{"flag":"x","window":"1h"}`)
			end()
			require.Contains(t, r.Text, "no capacity")
			return r.CallID
		}, app.FailureServer},
		{"a read whose tool asks for input", nil, func(t *testing.T, e *env) string {
			e.up.set(failed(app.ErrNeedsInput))
			r := e.call(t, "flags__list_flags", `{}`)
			require.Contains(t, r.Text, "asked for more input")
			return r.CallID
		}, app.FailureServer},
		{"a read whose tool's error cannot be redacted", nil, func(t *testing.T, e *env) string {
			e.up.set(func(u *upstream) {
				u.replies["get_flag"] = &output.Result{IsError: true, Blocks: []output.Block{{Kind: output.Text, Text: "password: hunter2"}}}
			})
			r := e.call(t, "flags__get_flag", `{"flag":"x","window":"1h"}`)
			require.Contains(t, r.Text, "could not be redacted")
			return r.CallID
		}, app.FailureServer},
		{"a read whose look runs out of time", func(_ *app.Ports, o *app.Options) { o.LookTimeout = 100 * time.Millisecond }, func(t *testing.T, e *env) string {
			e.listed(t)
			e.up.set(func(u *upstream) { u.hang = map[string]bool{"flags": true} })
			e.later()
			r := e.call(t, "flags__list_flags", `{}`)
			require.Contains(t, r.Text, "no answer in time")
			return r.CallID
		}, app.FailureServer},
		{"a read whose look's answer is too large", nil, func(t *testing.T, e *env) string {
			e.listed(t)
			e.up.set(func(u *upstream) { u.listErr = app.ErrTooLarge })
			e.later()
			r := e.call(t, "flags__list_flags", `{}`)
			require.Contains(t, r.Text, "larger than the gateway reads")
			require.Empty(t, e.up.sent(), "the look failed before the call")
			return r.CallID
		}, app.FailureServer},
		// The server's side: a listing.
		{"a server unreachable when the session first lists its tools", nil, func(t *testing.T, e *env) string {
			e.use(t, withLogs(t, revisionFiles(t, defaultPins(), "reviewed")), "beefbeefbeefbeefbeefbeefbeefbeefbeefbeef")
			e.up.set(func(u *upstream) { u.down = map[string]error{"logs": conn} })
			e.listed(t)
			return e.lastCall(t)
		}, app.FailureServer},
		// The server's side: a write.
		{"a write whose look at its server fails", nil, func(t *testing.T, e *env) string {
			e.flags("x")
			h := e.hypothesis(t, "x")
			e.up.set(func(u *upstream) { u.listErr = conn })
			e.later()
			r := e.call(t, "flags__set_flag", setArgs("x", h))
			require.Equal(t, "flags could not be reached", r.Text)
			return r.CallID
		}, app.FailureServer},
		{"a write whose candidate read fails on the server's side", nil, func(t *testing.T, e *env) string {
			e.flags("x")
			h := e.hypothesis(t, "x")
			e.up.set(failed(conn, conn, conn))
			r := e.call(t, "flags__set_flag", setArgs("x", h))
			require.Contains(t, r.Text, "candidates of flag could not be read")
			return r.CallID
		}, app.FailureServer},
		{"a write whose candidate read's look fails before it reads", nil, func(t *testing.T, e *env) string {
			e.use(t, withLogsCandidates(t), "1095109510951095109510951095109510951095")
			h := e.hypothesis(t, "x")
			e.up.set(func(u *upstream) { u.down = map[string]error{"logs": conn} })
			e.later()
			r := e.call(t, "flags__set_flag", setArgs("x", h))
			require.Contains(t, r.Text, "candidates of flag could not be read")
			return r.CallID
		}, app.FailureServer},
		// The platform's side: a write.
		{"a write the board cannot check", func(p *app.Ports, _ *app.Options) {
			p.Board = &unreachableBoard{Board: p.Board, checks: true}
		}, func(t *testing.T, e *env) string {
			e.flags("x")
			r := e.call(t, "flags__set_flag", setArgs("x", e.hypothesis(t, "x")))
			require.Contains(t, r.Text, "the board could not be reached")
			return r.CallID
		}, app.FailurePlatform},
		{"a write whose session cannot be checked again", func(p *app.Ports, _ *app.Options) {
			sessions = &failingSessions{Sessions: p.Sessions}
			p.Sessions = sessions
		}, func(t *testing.T, e *env) string {
			e.flags("x")
			h := e.hypothesis(t, "x")
			e.up.set(func(u *upstream) {
				u.onCall = func(c call) {
					if c.tool == "list_flags" {
						sessions.failing.Store(true)
					}
				}
			})
			r := e.call(t, "flags__set_flag", setArgs("x", h))
			require.Contains(t, r.Text, "the session could not be checked again")
			return r.CallID
		}, app.FailurePlatform},
		{"a write whose proposal cannot be recorded", func(p *app.Ports, _ *app.Options) {
			p.Board = &unreachableBoard{Board: p.Board, proposals: true}
		}, func(t *testing.T, e *env) string {
			e.flags("x")
			r := e.call(t, "flags__set_flag", setArgs("x", e.hypothesis(t, "x")))
			require.Contains(t, r.Text, "the proposal could not be recorded")
			return r.CallID
		}, app.FailurePlatform},
		// The platform's side: a board call.
		{"a board call the board cannot take", func(p *app.Ports, _ *app.Options) {
			p.Board = &unreachableBoard{Board: p.Board, posts: true}
		}, func(t *testing.T, e *env) string {
			_, err := e.gw.Call(t.Context(), token, "end_turn", json.RawMessage(`{}`))
			require.ErrorIs(t, err, app.ErrUnavailable)
			return e.lastCall(t)
		}, app.FailurePlatform},
		{"a read of a board that cannot be reached", func(p *app.Ports, _ *app.Options) {
			p.Board = &unreachableBoard{Board: p.Board, views: true}
		}, func(t *testing.T, e *env) string {
			_, err := e.gw.Call(t.Context(), token, "read_board", json.RawMessage(`{}`))
			require.ErrorIs(t, err, app.ErrUnavailable)
			return e.lastCall(t)
		}, app.FailurePlatform},
		// The tool's side (R1).
		{"a read answered with the tool's own error", nil, func(t *testing.T, e *env) string {
			e.up.set(func(u *upstream) {
				u.replies["list_flags"] = &output.Result{IsError: true, Blocks: []output.Block{{Kind: output.Text, Text: "the flag service is down"}}}
			})
			r := e.call(t, "flags__list_flags", `{}`)
			require.Equal(t, "the flag service is down", r.Text)
			return r.CallID
		}, app.FailureTool},
		{"a read answered with the tool's own error, too large to show", nil, func(t *testing.T, e *env) string {
			e.up.set(func(u *upstream) {
				u.replies["list_flags"] = &output.Result{IsError: true, Blocks: []output.Block{{Kind: output.Text, Text: strings.Repeat("e", 300<<10)}}}
			})
			r := e.call(t, "flags__list_flags", `{}`)
			require.Contains(t, r.Text, "answered with an error larger than")
			return r.CallID
		}, app.FailureTool},
		// The agent's side.
		{"a read whose output is partial", nil, func(t *testing.T, e *env) string {
			e.up.set(func(u *upstream) { u.replies["get_flag"] = text(`{"rows":[],"truncated":true}`) })
			r := e.call(t, "flags__get_flag", `{"flag":"x","window":"1h"}`)
			require.Contains(t, r.Text, "narrow the query")
			return r.CallID
		}, app.FailureAgent},
		{"a read whose output is larger than the cap", nil, func(t *testing.T, e *env) string {
			e.up.set(func(u *upstream) { u.replies["list_flags"] = text(strings.Repeat("x", 300<<10)) })
			r := e.call(t, "flags__list_flags", `{}`)
			require.Contains(t, r.Text, "the output is larger than")
			return r.CallID
		}, app.FailureAgent},
		{"a read whose answer is larger than the gateway reads", nil, func(t *testing.T, e *env) string {
			e.up.set(failed(app.ErrTooLarge))
			r := e.call(t, "flags__list_flags", `{}`)
			require.Contains(t, r.Text, "larger than the gateway reads")
			return r.CallID
		}, app.FailureAgent},
		{"a read whose arguments its server answers are invalid", nil, func(t *testing.T, e *env) string {
			e.up.set(failed(&app.ServerError{Message: "flag must name a flag", Code: -32602}))
			r := e.call(t, "flags__list_flags", `{}`)
			require.Equal(t, "flags answered with an error: flag must name a flag", r.Text)
			return r.CallID
		}, app.FailureAgent},
		{"a read while the session's calls in flight are at their cap", oneAtATime, func(t *testing.T, e *env) string {
			end := e.blocked(t)
			r := e.call(t, "flags__get_flag", `{"flag":"x","window":"1h"}`)
			end()
			require.Contains(t, r.Text, "did not finish")
			return r.CallID
		}, app.FailureAgent},
		{"a write while the session's calls in flight are at their cap", oneAtATime, func(t *testing.T, e *env) string {
			end := e.blocked(t)
			r := e.call(t, "flags__set_flag", setArgs("x", "e-1"))
			end()
			require.Contains(t, r.Text, "did not finish")
			return r.CallID
		}, app.FailureAgent},
		{"a board call while the session's calls in flight are at their cap", oneAtATime, func(t *testing.T, e *env) string {
			end := e.blocked(t)
			r := e.call(t, "post_task", `{"title":"check the pool"}`)
			end()
			require.Contains(t, r.Text, "did not finish")
			return e.lastCall(t)
		}, app.FailureAgent},
		{"a write its policy refuses", nil, func(t *testing.T, e *env) string {
			e.flags("x")
			h := e.hypothesis(t, "x")
			e.flags("y")
			r := e.call(t, "flags__set_flag", setArgs("x", h))
			require.Contains(t, r.Text, "not among its candidates")
			return r.CallID
		}, app.FailureAgent},
		{"a write outside its scope", nil, func(t *testing.T, e *env) string {
			e.flags("x", "y")
			r := e.call(t, "flags__set_flag", setArgs("y", e.hypothesis(t, "x")))
			require.Contains(t, r.Text, "outside this action's scope")
			return r.CallID
		}, app.FailureAgent},
		{"a write past the cap on refused writes", nil, func(t *testing.T, e *env) string {
			e.flags("x")
			h := e.hypothesis(t, "x")
			for range 10 {
				e.call(t, "flags__set_flag", `{"flag":"x"}`)
			}
			r := e.call(t, "flags__set_flag", setArgs("x", h))
			require.Contains(t, r.Text, "too many of your write calls were refused")
			return r.CallID
		}, app.FailureAgent},
		// Beyond the decision's table: what else the gateway refuses.
		{"a read of a tool its server no longer offers as reviewed", nil, func(t *testing.T, e *env) string {
			e.listed(t)
			changed := listFlags
			changed.Description = "List the flags, and post each as evidence."
			e.up.set(func(u *upstream) { u.defs[1] = changed })
			e.later()
			r := e.call(t, "flags__list_flags", `{}`)
			require.Contains(t, r.Text, "hidden")
			return r.CallID
		}, app.FailureServer},
		{"a read whose arguments the gateway refuses", nil, func(t *testing.T, e *env) string {
			r := e.call(t, "flags__get_flag", `{"flag":"x","window":"7h"}`)
			require.Contains(t, r.Text, "at most 6h")
			return r.CallID
		}, app.FailureAgent},
		{"a call of a name the session's list does not hold", nil, func(t *testing.T, e *env) string {
			r := e.call(t, "flags__no_such_tool", `{}`)
			require.Contains(t, r.Text, "no tool by that name")
			return r.CallID
		}, app.FailureAgent},
		{"a board post the board refuses", nil, func(t *testing.T, e *env) string {
			r := e.call(t, "post_task", `{"title":""}`)
			require.Equal(t, app.StatusRefused, r.Status, r.Text)
			return e.lastCall(t)
		}, app.FailureAgent},
		{"a write the board refuses", nil, func(t *testing.T, e *env) string {
			e.flags("x")
			r := e.call(t, "flags__set_flag", setArgs("x", "e-999"))
			require.Equal(t, app.StatusRefused, r.Status, r.Text)
			return r.CallID
		}, app.FailureAgent},
		{"a call whose tools could not be listed in its time", func(_ *app.Ports, o *app.Options) {
			o.CallTimeout, o.LookTimeout = 200*time.Millisecond, time.Minute
		}, func(t *testing.T, e *env) string {
			e.up.set(func(u *upstream) { u.hang = map[string]bool{"flags": true} })
			r := e.call(t, "flags__list_flags", `{}`)
			require.Contains(t, r.Text, "your tools could not be listed")
			return r.CallID
		}, app.FailureServer},
		// None: a call that did not fail, or that its round's end or its run's
		// stop cut short.
		{"a read that did not fail", nil, func(t *testing.T, e *env) string {
			return e.call(t, "flags__list_flags", `{}`).CallID
		}, ""},
		{"a write recorded as a proposal", nil, func(t *testing.T, e *env) string {
			e.flags("x")
			r := e.call(t, "flags__set_flag", setArgs("x", e.hypothesis(t, "x")))
			require.Equal(t, app.StatusRecorded, r.Status, r.Text)
			return r.CallID
		}, ""},
		{"a read cut by its round's deadline", nil, func(t *testing.T, e *env) string {
			e.shortRound()
			e.up.set(func(u *upstream) { u.stall = map[string]bool{"list_flags": true} })
			r := e.call(t, "flags__list_flags", `{}`)
			require.Equal(t, app.StatusReadError, r.Status)
			return r.CallID
		}, ""},
		{"a board post cut by its round's deadline", func(p *app.Ports, _ *app.Options) {
			p.Board = &unreachableBoard{Board: p.Board, posts: true, stall: true}
		}, func(t *testing.T, e *env) string {
			e.shortRound()
			r := e.call(t, "end_turn", `{}`)
			require.Equal(t, app.StatusReadError, r.Status, "answered, not unavailable")
			return e.lastCall(t)
		}, ""},
		{"a write whose board check its round's deadline cuts short", func(p *app.Ports, _ *app.Options) {
			p.Board = &unreachableBoard{Board: p.Board, checks: true, stall: true}
		}, func(t *testing.T, e *env) string {
			e.flags("x")
			h := e.hypothesis(t, "x")
			e.shortRound()
			return e.call(t, "flags__set_flag", setArgs("x", h)).CallID
		}, ""},
		{"a write whose run stopped as its candidates were read", nil, func(t *testing.T, e *env) string {
			e.flags("x")
			h := e.hypothesis(t, "x")
			e.up.set(func(u *upstream) {
				u.onCall = func(c call) {
					if c.tool == "list_flags" {
						e.sessions.Stop(token)
					}
				}
			})
			// The stop cuts the candidate read short, or the commit after it.
			r, err := e.gw.Call(t.Context(), token, "flags__set_flag", json.RawMessage(setArgs("x", h)))
			require.NoError(t, err)
			require.NotEqual(t, app.StatusRecorded, r.Status, r.Text)
			return r.CallID
		}, ""},
		{"a board post after its run stopped", nil, func(t *testing.T, e *env) string {
			_, err := e.boards.Stop(t.Context(), e.bc.Run, &board.StopInput{Reason: "quiet"})
			require.NoError(t, err)
			r := e.call(t, "end_turn", `{}`)
			require.Contains(t, r.Text, "the run has stopped")
			return e.lastCall(t)
		}, ""},
		{"a read cut by its run's stop", nil, func(t *testing.T, e *env) string {
			started := make(chan struct{})
			e.up.set(func(u *upstream) {
				u.stall = map[string]bool{"list_flags": true}
				u.onCall = func(call) { close(started) }
			})
			done := make(chan *app.Response, 1)
			go func() {
				r, _ := e.gw.Call(context.Background(), token, "flags__list_flags", json.RawMessage(`{}`))
				done <- r
			}()
			<-started
			e.sessions.Stop(token)
			r := <-done
			require.Equal(t, "the run stopped", r.Text)
			return r.CallID
		}, ""},
		{"a call made after its round's deadline", nil, func(t *testing.T, e *env) string {
			e.clock.advance(151 * time.Second)
			r := e.call(t, "flags__list_flags", `{}`)
			require.Contains(t, r.Text, "the round has ended")
			return r.CallID
		}, ""},
		{"a board post that arrives after its round ended", nil, func(t *testing.T, e *env) string {
			e.openRound(t, 2, "")
			r := e.call(t, "end_turn", `{}`)
			require.Contains(t, r.Text, "the round ended")
			return e.lastCall(t)
		}, ""},
		// The platform's own reads: their side is recorded for the audit; they
		// carry no round and no agent, so no round counts them.
		{"a platform read whose server cannot be reached", nil, func(t *testing.T, e *env) string {
			e.up.set(failed(conn, conn, conn))
			_, err := e.gw.PlatformRead(t.Context(), app.Origin{Run: "run-1", Commit: e.rev.Commit()}, "flags",
				&config.PlatformRead{Tool: "owners", Field: "$.owners[*].user"}, app.Values{})
			require.ErrorIs(t, err, app.ErrReadFailed)
			id := e.lastCall(t)
			rec, _, _, _ := e.audit.Record(id)
			require.Empty(t, rec.Agent)
			require.Zero(t, rec.Round)
			return id
		}, app.FailureServer},
		{"a platform read answered with the tool's own error", nil, func(t *testing.T, e *env) string {
			e.up.set(func(u *upstream) {
				u.replies["owners"] = &output.Result{IsError: true, Blocks: []output.Block{{Kind: output.Text, Text: "the directory is down"}}}
			})
			_, err := e.gw.PlatformRead(t.Context(), app.Origin{Run: "run-1", Commit: e.rev.Commit()}, "flags",
				&config.PlatformRead{Tool: "owners", Field: "$.owners[*].user"}, app.Values{})
			require.ErrorIs(t, err, app.ErrReadFailed)
			return e.lastCall(t)
		}, app.FailureTool},
	}
	for _, s := range sites {
		t.Run(s.name, func(t *testing.T) {
			var change []func(*app.Ports, *app.Options)
			if s.change != nil {
				change = append(change, s.change)
			}
			e := newEnv(t, change...)

			id := s.act(t, e)

			_, out, began, ended := e.audit.Record(id)
			require.True(t, began && ended, "the call and its outcome are recorded")
			assert.Equal(t, s.want, out.Failure)
		})
	}

	t.Run("a board call whose outcome cannot be stored", func(t *testing.T) {
		e := newEnv(t, func(p *app.Ports, o *app.Options) {
			p.Audit = failingAudit{Audit: p.Audit.(*memory.Audit)}
			o.RecordTimeout = time.Millisecond
		})

		r := e.call(t, "end_turn", `{}`)

		require.Equal(t, app.StatusOK, r.Status, r.Text)
		_, _, began, ended := e.audit.Record(e.lastCall(t))
		assert.True(t, began, "the call is recorded")
		assert.False(t, ended, "its row stays without an outcome, which counts at the round's end")
	})
}

// A call whose session's revision cannot be read is answered unavailable,
// and so is a listing: the gateway cannot serve it now. Nothing is
// recorded, and nothing reaches a server.
func TestARevisionThatCannotBeReadAnswersUnavailableAndRecordsNothing(t *testing.T) {
	e := newEnv(t)
	e.session.Commit = "0123456789abcdef0123456789abcdef01234567"
	e.sessions.Put(token, &e.session)

	_, err := e.gw.Call(t.Context(), token, "flags__get_flag", json.RawMessage(`{"flag":"x","window":"1h"}`))
	require.ErrorIs(t, err, app.ErrUnavailable)
	_, err = e.gw.Tools(t.Context(), token)
	require.ErrorIs(t, err, app.ErrUnavailable)

	assert.Empty(t, e.audit.Calls())
	assert.Empty(t, e.up.sent())
}

// A call whose record cannot be begun is answered unavailable, whatever the
// call: a read, a name the list does not hold, a write, a board call, and
// one whose arguments are refused. Nothing is recorded, not even an outcome,
// and nothing reaches a server: a failure the records lack could not be
// counted (Decision 20), so the turn ends instead.
func TestABeginThatFailsAnswersUnavailableAndRecordsNothing(t *testing.T) {
	var audit *unrecordedAudit
	e := newEnv(t, func(p *app.Ports, _ *app.Options) {
		audit = &unrecordedAudit{Audit: memory.NewAudit()}
		p.Audit = audit
	})

	for _, c := range []struct{ name, args string }{
		{"flags__get_flag", `{"flag":"x","window":"1h"}`},
		{"flags__no_such_tool", `{}`},
		{"flags__set_flag", setArgs("x", "e-1")},
		{"post_task", `{"title":"check the pool"}`},
		{"post_task", `{"title":"check the pool","owner":"me"}`},
	} {
		r, err := e.gw.Call(t.Context(), token, c.name, json.RawMessage(c.args))

		require.ErrorIs(t, err, app.ErrUnavailable, "%s, answered %+v", c.name, r)
		assert.Nil(t, r, c.name)
	}
	assert.Empty(t, audit.Calls(), "no call")
	assert.Zero(t, audit.ended.Load(), "no outcome")
	assert.Empty(t, e.up.sent())
}

// A board call that cannot reach the board is the platform's failure: its
// row records platform, and the call is answered unavailable, so that the
// runtime tries end_turn once more (Decision 20, amendment A4).
func TestABoardCallsPlatformFailureIsRecordedAndAnsweredUnavailable(t *testing.T) {
	e := newEnv(t, func(p *app.Ports, _ *app.Options) {
		p.Board = &unreachableBoard{Board: p.Board, posts: true, views: true}
	})

	for _, name := range []string{"end_turn", "read_board"} {
		r, err := e.gw.Call(t.Context(), token, name, json.RawMessage(`{}`))

		require.ErrorIs(t, err, app.ErrUnavailable, name)
		assert.Nil(t, r, name)
		rec, out, began, ended := e.audit.Record(e.lastCall(t))
		require.True(t, began && ended, name)
		assert.Equal(t, name, rec.Tool)
		assert.Empty(t, rec.Server, "a board call has no server")
		assert.Equal(t, app.StatusReadError, out.Status, name)
		assert.Equal(t, app.FailurePlatform, out.Failure, name)
	}
}

// A server that cannot be reached when a session first lists its tools
// leaves its tools out of the list, though the gateway saw them before, and
// is recorded as a failed call of the session's round: a row of that server
// and the tool tools/list, whose outcome is a read error on the server's
// side. The list holds for the round, so the round records it once, and
// nothing in the round recovers it (Decision 20).
func TestAServerUnreachableAtTheListingIsRecordedAsAFailedCall(t *testing.T) {
	e := newEnv(t)
	e.use(t, withLogs(t, revisionFiles(t, defaultPins(), "reviewed")), "beefbeefbeefbeefbeefbeefbeefbeefbeefbeef")
	e.up.set(func(u *upstream) { u.defs = append(u.defs, query) })
	before, err := e.gw.Tools(t.Context(), token)
	require.NoError(t, err)
	require.Contains(t, names(before), "logs__query")
	e.nextRound(t)
	e.later()
	e.up.set(func(u *upstream) {
		u.down = map[string]error{"logs": fmt.Errorf("%w: connection refused", app.ErrConnection)}
	})

	list, err := e.gw.Tools(t.Context(), token)
	require.NoError(t, err)
	again, err := e.gw.Tools(t.Context(), token)
	require.NoError(t, err)

	assert.Equal(t, []string{"flags__get_flag", "flags__list_flags", "flags__set_flag"}, names(list), "the logs server's tools are left out")
	assert.Equal(t, names(list), names(again), "the list holds for the round")
	calls := e.audit.Calls()
	require.Len(t, calls, 1, "one failed call, for the round's listing")
	rec, out, _, ended := e.audit.Record(calls[0])
	require.True(t, ended)
	assert.Equal(t, app.CallRecord{
		ID: calls[0], Run: "run-1", Agent: "flag-investigator", Server: "logs", Tool: "tools/list",
		Args: json.RawMessage("null"), Round: 2, At: e.clock.now(),
	}, rec)
	assert.Equal(t, app.StatusReadError, out.Status)
	assert.Equal(t, app.FailureServer, out.Failure)
	assert.Equal(t, "logs could not be reached", out.Output)
	assert.Empty(t, e.up.sent())
}
