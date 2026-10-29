package docker

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/tokens"
)

// epoch is the holding the tests' turns start under.
const epoch = 7

// testSkills are the skills the tests' agents have, as Skills reads them.
var testSkills = []string{"You are alpha.", "Read the board before you post."}

// holdingFake answers as valid says, and keeps the epochs it is asked about.
type holdingFake struct {
	valid atomic.Bool
	mu    sync.Mutex
	asked []int64
}

func (h *holdingFake) Valid(epoch int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.asked = append(h.asked, epoch)
	return h.valid.Load()
}

// syncBuffer holds what a log writes, from any goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// logged returns the lines of a JSON log whose message is msg.
func logged(t *testing.T, logs *syncBuffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(logs.String()) {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
		if entry["msg"] == msg {
			out = append(out, entry)
		}
	}
	return out
}

// env is a runner under test, on a fake engine, with the ports it reads and
// reports through.
type env struct {
	r    *Runner
	fe   *fakeEngine
	o    Options
	held *holdingFake
	logs *syncBuffer

	mu       sync.Mutex
	launches []app.Launch
	exits    []app.Exit
	exitsAt  []time.Time
}

// newFixture returns an env before its runner opens: a fake engine that
// holds the good image, and options that run it as runner.images.thinloop.
func newFixture(t *testing.T) *env {
	t.Helper()
	e := &env{fe: newFakeEngine(goodImage()), o: testOptions(t), held: &holdingFake{}, logs: &syncBuffer{}}
	e.held.valid.Store(true)
	e.o.Images = map[string]string{"thinloop": goodImage().ID}
	return e
}

// newEnv returns a runner opened on the fixture's ports and options, as edit
// changes them (open), with the fake's trail cleared of Open's calls.
func newEnv(t *testing.T, edit ...func(*Ports, *Options)) *env {
	t.Helper()
	e := newFixture(t)
	e.open(t, edit...)
	e.fe.forgetCalls()
	return e
}

// open opens the env's runner on its ports and options, as edit changes
// them. At the test's end, every call the fake holds is released, every turn
// stopped and forgotten, and the runner closed.
func (e *env) open(t *testing.T, edit ...func(*Ports, *Options)) {
	t.Helper()
	p, o := e.ports(), e.o
	for _, f := range edit {
		f(&p, &o)
	}
	r, err := Open(t.Context(), p, o)
	require.NoError(t, err)
	e.r, e.o = r, o
	t.Cleanup(func() { e.settle(t) })
}

// ports are the runner's ports in a test: the fake engine, a holding that
// holds, skills read at once, a board that holds no turn's end, and an
// Exited and a Launched that keep what they are told, each marked on the
// fake's trail.
func (e *env) ports() Ports {
	return Ports{
		Engine:  e.fe,
		Holding: e.held,
		Skills:  func(context.Context, string, string) ([]string, error) { return slices.Clone(testSkills), nil },
		Ended:   func(context.Context, board.RunID, int, string) (bool, error) { return false, nil },
		Exited: func(x app.Exit) {
			e.fe.mark("Exited")
			e.mu.Lock()
			defer e.mu.Unlock()
			e.exits = append(e.exits, x)
			e.exitsAt = append(e.exitsAt, time.Now())
		},
		Launched: func(l app.Launch) {
			e.fe.mark("Launched")
			e.mu.Lock()
			defer e.mu.Unlock()
			e.launches = append(e.launches, l)
		},
		Log: slog.New(slog.NewJSONHandler(e.logs, nil)),
	}
}

// settle ends what a test left: it releases every call the fake holds, stops
// every turn, waits until the runner has forgotten each, and closes the
// runner.
func (e *env) settle(t *testing.T) {
	e.fe.releaseAll()
	e.r.StopAll()
	for range 1000 {
		if registered(e.r) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Zero(t, registered(e.r), "a turn was never forgotten")
	assert.NoError(t, e.r.Close())
}

// registered counts the turns the runner has.
func registered(r *Runner) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.turns)
}

// ctx is a context of the coordinator's: it carries the holding's epoch.
func (e *env) ctx(t *testing.T) context.Context {
	return holding.WithEpoch(t.Context(), epoch)
}

// start starts a turn on a context of the coordinator's, and fails the test
// unless Start answers no error, and pending.
func (e *env) start(t *testing.T, turn *app.Turn) {
	t.Helper()
	pending, err := e.r.Start(e.ctx(t), turn)
	require.NoError(t, err)
	require.True(t, pending, "the turn's launch was not left pending")
}

// launched returns the launches reported so far, in order.
func (e *env) launched() []app.Launch {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.launches)
}

// exited returns the turns' ends reported so far, in order.
func (e *env) exited() []app.Exit {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.exits)
}

// exitedAt returns when each turn's end was reported.
func (e *env) exitedAt() []time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.exitsAt)
}

// turns numbers the tests' turns.
var turns atomic.Int64

// newTurn returns a new turn of agent in round 2 of run-1, of the good
// image, its round's deadline a minute away.
func newTurn(agent string) *app.Turn {
	return &app.Turn{
		ID: fmt.Sprintf("turn-%d", turns.Add(1)), Run: "run-1", Agent: agent, Round: 2,
		Deadline: time.Now().Add(time.Minute),
		MCP:      tokens.New(tokens.MCP), LLM: tokens.New(tokens.LLM),
		Commit: "4b825dc642cb6eb9a060e54bf8d69288fbee4904", Image: goodImage().ID,
		Model: "claude-sonnet-5-5", Prompt: "Find the cause.",
	}
}

// launchOf returns the launch of the turn id that the runner reported.
func launchOf(t *testing.T, e *env, id string) app.Launch {
	t.Helper()
	for _, l := range e.launched() {
		if l.Turn == id {
			return l
		}
	}
	require.Failf(t, "no launch was reported", "of the turn %s", id)
	return app.Launch{}
}

// onFirst returns a hook that gives the first call of method err's error,
// and changes no other.
func onFirst(method string, err func(arg string) error) func(string, string) error {
	var done atomic.Bool
	return func(m, arg string) error {
		if m == method && done.CompareAndSwap(false, true) {
			return err(arg)
		}
		return nil
	}
}

// returnsWithin reports whether fn returns within d.
func returnsWithin(d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// TestStartCallsNoEngine (Review Focus 2): Start runs under the
// coordinator's lock, so it calls no engine: with every engine call held,
// it returns (true, nil) within 50 ms, having made the turn's output file,
// and the fake recorded no call before it returned. The launch's calls come
// after, off the lock, its first step first.
func TestStartCallsNoEngine(t *testing.T) {
	e := newEnv(t)
	thaw := e.fe.freeze()
	t.Cleanup(thaw)
	turn := newTurn("alpha")
	type answer struct {
		pending bool
		err     error
	}
	answered := make(chan answer, 1)
	ctx := e.ctx(t)
	go func() {
		pending, err := e.r.Start(ctx, turn)
		answered <- answer{pending, err}
	}()
	var a answer
	select {
	case a = <-answered:
	case <-time.After(50 * time.Millisecond):
		require.FailNow(t, "Start did not return within 50 ms, with every engine call held")
	}

	assert.Empty(t, e.fe.calls(), "the fake recorded a call before Start returned")
	require.NoError(t, a.err)
	assert.True(t, a.pending, "the turn's launch was not left pending")
	assert.FileExists(t, filepath.Join(e.o.Output, turn.ID+".out"))
	thaw()
	require.Eventually(t, func() bool { return len(e.fe.calls()) > 0 }, 10*time.Second, 5*time.Millisecond, "the launch made no call")
	assert.Equal(t, "Remove "+containerName(turn.ID), e.fe.calls()[0].String(), "the launch's first call is its first step")
}

// TestStartChecksTheHoldingFirst: before a turn starts, the runner asks the
// holding whether the epoch of the coordinator's context still holds (Plan
// 7d-1, Decision 4). A refusal fails Start wrapping app.ErrUnheld, which
// never fences, and is logged holding refused, with request turn. A context
// without an epoch is a bug, refused with holding.ErrNoEpoch and logged as
// one. Either way no output file is made, and no engine is called.
func TestStartChecksTheHoldingFirst(t *testing.T) {
	for _, row := range []struct {
		name       string
		ctx        func(context.Context) context.Context
		valid      bool
		err        error
		msg, level string
		attrs      func(turn *app.Turn) map[string]any
	}{
		{
			name: "the holding refuses", ctx: func(ctx context.Context) context.Context { return holding.WithEpoch(ctx, epoch) },
			err: app.ErrUnheld, msg: "holding refused", level: "WARN",
			attrs: func(turn *app.Turn) map[string]any {
				return map[string]any{"epoch": float64(epoch), "request": "turn", "turn": turn.ID}
			},
		},
		{
			name: "the context carries no epoch", ctx: func(ctx context.Context) context.Context { return ctx }, valid: true,
			err: holding.ErrNoEpoch, msg: "turn without an epoch", level: "ERROR",
			attrs: func(turn *app.Turn) map[string]any { return map[string]any{"turn": turn.ID} },
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			e := newEnv(t)
			e.held.valid.Store(row.valid)
			turn := newTurn("alpha")

			pending, err := e.r.Start(row.ctx(t.Context()), turn)

			require.ErrorIs(t, err, row.err)
			require.NotErrorIs(t, err, holding.ErrLeaseLost, "a refusal never fences")
			assert.False(t, pending)
			lines := logged(t, e.logs, row.msg)
			require.Len(t, lines, 1)
			assert.Equal(t, row.level, lines[0]["level"])
			for k, v := range row.attrs(turn) {
				assert.Equal(t, v, lines[0][k], k)
			}
			assert.NoFileExists(t, filepath.Join(e.o.Output, turn.ID+".out"), "an output file was made")
			assert.Never(t, func() bool { return len(e.fe.calls()) > 0 }, 200*time.Millisecond, 10*time.Millisecond, "an engine was called")
			assert.Zero(t, registered(e.r))
		})
	}
}

// TestAnOutputFileThatCannotBeCreatedFailsTheStart: an output directory the
// runner cannot write fails the turn's Start, as under the process runner,
// so that the turn is tried again and counts as unstarted; nothing is
// queued, and no launch reaches the engine.
func TestAnOutputFileThatCannotBeCreatedFailsTheStart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	e := newEnv(t)
	require.NoError(t, os.Chmod(e.o.Output, 0o500))
	t.Cleanup(func() { _ = os.Chmod(e.o.Output, 0o700) })

	pending, err := e.r.Start(e.ctx(t), newTurn("alpha"))

	require.ErrorIs(t, err, fs.ErrPermission)
	assert.False(t, pending)
	assert.Zero(t, registered(e.r), "the turn was queued")
	assert.Never(t, func() bool { return len(e.fe.calls()) > 0 }, 200*time.Millisecond, 10*time.Millisecond, "a launch reached the engine")
}

// TestATurnWhoseIDCannotNameItsContainerIsRefused: a turn's id names its
// output file and its container, agentswarm-turn-<id>, whose name the engine
// takes of [a-zA-Z0-9_.-] alone. An id that cannot name both fails Start, as
// the process runner refuses one that cannot name its output file: no file
// is made, outside the output directory or in it, nothing is queued, and no
// engine is called.
func TestATurnWhoseIDCannotNameItsContainerIsRefused(t *testing.T) {
	for _, id := range []string{"", "../escape", "a/b", "a b", "tür", "a:b"} {
		t.Run(fmt.Sprintf("%q", id), func(t *testing.T) {
			e := newEnv(t)
			turn := newTurn("alpha")
			turn.ID = id

			pending, err := e.r.Start(e.ctx(t), turn)

			require.Error(t, err)
			assert.False(t, pending)
			assert.Empty(t, dirNames(t, e.o.Output), "an output file was made")
			assert.NoFileExists(t, filepath.Join(e.o.Output, "..", "escape.out"))
			assert.Zero(t, registered(e.r), "the turn was queued")
			assert.Never(t, func() bool { return len(e.fe.calls()) > 0 }, 200*time.Millisecond, 10*time.Millisecond, "a launch reached the engine")
		})
	}
}

// TestALaunchTakesItsStepsInOrderAndDeliversTheSpec (Decision 5): a launch
// removes a container an earlier attempt of the turn left, by its name;
// checks the turn's image; creates, attaches to and starts the container,
// by its id; and reports Launched once, with no error. The attachment gets
// Contract C6's spec, of version 2: the gateways' URLs and sockets, the
// certificate, the round's deadline plus the slack, and maxTokens. The runner
// logs turn started with the turn, the container's id, and the launch's
// time, from Start to the container's start, as a Go duration (R21).
func TestALaunchTakesItsStepsInOrderAndDeliversTheSpec(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		e.fe.setHook(func(method, _ string) error {
			if method == "Create" {
				time.Sleep(1500 * time.Millisecond)
			}
			return nil
		})
		turn := newTurn("alpha")
		e.start(t, turn)
		time.Sleep(2 * time.Second)
		synctest.Wait()

		c := e.fe.byTurn(turn.ID)
		require.NotNil(t, c, "no container was made")
		name := containerName(turn.ID)
		steps := []string{"Remove " + name, "Image " + turn.Image, "Create " + name, "Attach " + c.id, "Start " + c.id}
		said := e.fe.said()
		require.GreaterOrEqual(t, len(said), len(steps), "the launch took too few steps: %q", said)
		assert.Equal(t, steps, said[:len(steps)])
		assert.Equal(t, []app.Launch{{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID}}, e.launched())
		want, err := json.Marshal(map[string]any{
			"v":    2,
			"turn": turn.ID, "run": "run-1", "agent": "alpha", "round": 2,
			"mcp":   map[string]any{"url": "https://mcp.agentswarm.internal/", "token": turn.MCP.Reveal(), "socket": "/run/agentswarm/mcp.sock"},
			"llm":   map[string]any{"url": "https://llm.agentswarm.internal", "token": turn.LLM.Reveal(), "socket": "/run/agentswarm/llm.sock"},
			"cert":  e.o.Cert,
			"model": "claude-sonnet-5-5", "system": testSkills, "prompt": turn.Prompt,
			"deadline": turn.Deadline.Add(2 * time.Second).Format(time.RFC3339Nano), "maxTokens": 4096,
		})
		require.NoError(t, err)
		assert.JSONEq(t, string(want), e.fe.received(c))
		started := logged(t, e.logs, "turn started")
		require.Len(t, started, 1)
		assert.Equal(t, "INFO", started[0]["level"])
		assert.Equal(t, turn.ID, started[0]["turn"])
		assert.Equal(t, c.id, started[0]["container"])
		assert.Equal(t, "1.5s", started[0]["launch"], "the launch runs from Start to the container's start")
	})
}

// TestNoTokenAppearsInTheContainersConfiguration (Global Constraints,
// Secrets): neither session token is in the container's configuration, its
// entrypoint, command, environment or labels, nor in the host's, nor in the
// runner's log; the spec on the attachment holds both.
func TestNoTokenAppearsInTheContainersConfiguration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		turn := newTurn("alpha")
		e.start(t, turn)
		synctest.Wait()

		c := e.fe.byTurn(turn.ID)
		require.NotNil(t, c, "no container was made")
		cfg, err := json.Marshal(c.cfg)
		require.NoError(t, err)
		host, err := json.Marshal(c.host)
		require.NoError(t, err)
		for _, token := range []string{turn.MCP.Reveal(), turn.LLM.Reveal()} {
			for where, text := range map[string]string{
				"the entrypoint": strings.Join(c.cfg.Entrypoint, " "), "the command": strings.Join(c.cfg.Cmd, " "),
				"the environment": strings.Join(c.cfg.Env, " "), "the labels": fmt.Sprint(c.cfg.Labels),
				"the configuration": string(cfg), "the host's configuration": string(host), "the runner's log": e.logs.String(),
			} {
				assert.NotContains(t, text, token, where)
			}
			assert.Contains(t, e.fe.received(c), token, "the spec on the attachment holds the token")
		}
	})
}

// TestALaunchChecksTheHoldingBeforeItStartsTheContainer (Decision 5, step
// 5): a holding that ends while the launch attaches stops the launch before
// the container starts. The container is removed by its id, the turn is
// forgotten, and Launched reports the reason holding, with an error that
// wraps app.ErrUnheld.
func TestALaunchChecksTheHoldingBeforeItStartsTheContainer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		release := e.fe.block("Attach")
		turn := newTurn("alpha")
		e.start(t, turn)
		synctest.Wait()
		attached := e.fe.callsOf("Attach")
		require.Len(t, attached, 1, "the launch did not attach")

		e.held.valid.Store(false)
		release()
		synctest.Wait()

		assert.Empty(t, e.fe.callsOf("Start"), "the container started")
		assert.Contains(t, e.fe.said(), "Remove "+attached[0].arg, "the container was not removed by its id")
		l := launchOf(t, e, turn.ID)
		assert.Equal(t, app.ReasonHolding, l.Reason)
		require.ErrorIs(t, l.Err, app.ErrUnheld)
		assert.Zero(t, registered(e.r), "the turn was not forgotten")
	})
}

// TestAFailedLaunchForgetsTheTurnBeforeItReports (Decision 5): a launch that
// fails, here at its attach, removes its container by its id on a fresh
// context, forgets the turn, and only then reports Launched, with the reason
// launch: inside that report a Start with the turn's id is a new Start, which
// answers (true, nil) and launches the turn again, as the coordinator's
// retry needs.
func TestAFailedLaunchForgetsTheTurnBeforeItReports(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		type answer struct {
			pending bool
			err     error
		}
		again := make(chan answer, 1)
		turn := newTurn("alpha")
		var r atomic.Pointer[Runner]
		var once sync.Once
		e := newEnv(t, func(p *Ports, _ *Options) {
			launched := p.Launched
			p.Launched = func(l app.Launch) {
				launched(l)
				once.Do(func() {
					pending, err := r.Load().Start(holding.WithEpoch(context.Background(), epoch), turn)
					again <- answer{pending, err}
				})
			}
		})
		r.Store(e.r)
		e.fe.setHook(onFirst("Attach", func(string) error { return errors.New("the engine refused the attach") }))

		e.start(t, turn)
		synctest.Wait()

		require.NotEmpty(t, e.launched(), "no launch was reported")
		first := e.launched()[0]
		assert.Equal(t, app.ReasonLaunch, first.Reason)
		require.Error(t, first.Err)
		attached := e.fe.callsOf("Attach")
		require.NotEmpty(t, attached)
		removed := slices.IndexFunc(e.fe.calls(), func(c call) bool { return c.String() == "Remove "+attached[0].arg })
		require.GreaterOrEqual(t, removed, 0, "the container was not removed by its id")
		assert.True(t, e.fe.calls()[removed].live, "the removal's context had ended")
		select {
		case a := <-again:
			require.NoError(t, a.err)
			assert.True(t, a.pending)
		default:
			require.FailNow(t, "the report made no Start")
		}
		launches := e.launched()
		require.Len(t, launches, 2, "the Start inside the report found the turn still registered, and launched nothing")
		assert.Equal(t, turn.ID, launches[1].Turn)
		require.NoError(t, launches[1].Err)
		assert.Len(t, e.fe.callsOf("Create"), 2)
	})
}

// TestALaunchThatOutlastsItsBoundOrMeetsNoEngineReadsEngine (Review Focus
// 2): a launch has 10 s from when it gets its slot. One whose engine call
// outlasts that, or that finds the engine unreachable, reports the reason
// engine, at its bound or at once; it frees its slot, and removes the
// container it made, by its id.
func TestALaunchThatOutlastsItsBoundOrMeetsNoEngineReadsEngine(t *testing.T) {
	for _, row := range []struct {
		name string
		// held is the method the engine holds; fails, the one it does not
		// answer.
		held, fails string
		made        bool
		at          time.Duration
	}{
		{name: "Create held past 10 s", held: "Create", at: 10 * time.Second},
		{name: "Attach held past 10 s", held: "Attach", made: true, at: 10 * time.Second},
		{name: "Create meets no engine", fails: "Create"},
	} {
		t.Run(row.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newEnv(t)
				if row.held != "" {
					e.fe.block(row.held)
				}
				if row.fails != "" {
					e.fe.setHook(onFirst(row.fails, func(arg string) error { return down(row.fails, arg) }))
				}
				turn := newTurn("alpha")
				e.start(t, turn)
				if row.at > 0 {
					time.Sleep(row.at - time.Nanosecond)
					synctest.Wait()
					require.Empty(t, e.launched(), "the launch ended before its bound")
					time.Sleep(time.Nanosecond)
				}
				synctest.Wait()

				l := launchOf(t, e, turn.ID)
				assert.Equal(t, app.ReasonEngine, l.Reason)
				require.ErrorIs(t, l.Err, ErrUnreachable)
				assert.Empty(t, e.r.slots, "the launch kept its slot")
				ids := e.fe.callsOf("Create")
				require.NotEmpty(t, ids)
				created := slices.ContainsFunc(e.fe.said(), func(s string) bool { return strings.HasPrefix(s, "Attach ") })
				assert.Equal(t, row.made, created)
				if row.made {
					attach := e.fe.callsOf("Attach")[0].arg
					assert.Contains(t, e.fe.said(), "Remove "+attach, "the container was not removed by its id")
				}
				assert.Zero(t, registered(e.r))
			})
		})
	}
}

// TestAnImageThatIsMissingOrBreaksTheContractReadsImage (Decision 6): a turn
// whose image is neither on the engine nor in the store, or breaks the
// contract, reports the reason image, and no container is created.
func TestAnImageThatIsMissingOrBreaksTheContractReadsImage(t *testing.T) {
	for _, row := range []struct {
		name  string
		image func() *Image // put on the engine; nil for none
	}{
		{name: "no image and no copy in the store"},
		{name: "a label of 1 alone", image: func() *Image {
			img := goodImage()
			img.Labels["io.agentswarm.turn-spec"] = "1"
			return img
		}},
		{name: "a volume", image: func() *Image {
			img := goodImage()
			img.Volumes = []string{"/data"}
			return img
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newEnv(t)
				turn := newTurn("alpha")
				turn.Image = "sha256:" + strings.Repeat("e", 64)
				if row.image != nil {
					img := row.image()
					img.ID = turn.Image
					e.fe.addImage(img)
				}
				e.start(t, turn)
				synctest.Wait()

				l := launchOf(t, e, turn.ID)
				assert.Equal(t, app.ReasonImage, l.Reason)
				require.Error(t, l.Err)
				assert.Empty(t, e.fe.callsOf("Create"), "a container was created")
			})
		})
	}
}

// TestALaunchReloadsAnImageTheEngineLost (Review Focus 3): an image a prune
// took off the engine comes back from the store at the next launch: the
// launch loads the store's copy, finds the image again, tags it
// agentswarm-runtime:<hex>, and only then creates the container; the
// reload is logged.
func TestALaunchReloadsAnImageTheEngineLost(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		id := goodImage().ID
		e.fe.lose(id)
		turn := newTurn("alpha")
		e.start(t, turn)
		synctest.Wait()

		hex := strings.TrimPrefix(id, "sha256:")
		steps := []string{"Image " + id, "Load", "Image " + id, "Tag " + id + " agentswarm-runtime:" + hex, "Create " + containerName(turn.ID)}
		said := e.fe.said()
		require.GreaterOrEqual(t, len(said), 1+len(steps), "the launch took too few steps: %q", said)
		assert.Equal(t, steps, said[1:1+len(steps)])
		assert.Equal(t, id, e.fe.tagged("agentswarm-runtime:"+hex))
		require.NoError(t, launchOf(t, e, turn.ID).Err)
		reloaded := logged(t, e.logs, "an image was reloaded from the store")
		require.Len(t, reloaded, 1)
		assert.Equal(t, id, reloaded[0]["image"])
	})
}

// TestAtMostEightLaunchesRunAtOnce (Decision 5): twelve launches while the
// engine holds every creation make eight creations; once the engine
// answers, all twelve launch.
func TestAtMostEightLaunchesRunAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		release := e.fe.block("Create")
		for i := range 12 {
			e.start(t, newTurn(fmt.Sprintf("agent-%d", i)))
		}
		synctest.Wait()

		assert.Len(t, e.fe.callsOf("Create"), 8)
		release()
		synctest.Wait()
		assert.Len(t, e.fe.callsOf("Create"), 12)
		launches := e.launched()
		assert.Len(t, launches, 12)
		for _, l := range launches {
			assert.NoError(t, l.Err, l.Agent)
		}
	})
}

// TestALaunchQueuedPastItsDeadlineWhileTheEngineDoesNotAnswerReadsEngine
// (Review Focus 2; plan review 2, F1): with every slot held by launches whose
// first call the engine holds, a launch queued for a slot at its turn's
// deadline reports cancelled while the engine's last call answered, and
// engine while that call found the engine unreachable, so that its agent's
// board says why. A call that a stop's cancel ended says nothing of the
// engine (Contract C14). A Stop at the turn's deadline, as the coordinator's
// deadline sends, reads as the deadline does: its rows stop the runner's own
// timer first, so that the Stop alone ends the launch.
func TestALaunchQueuedPastItsDeadlineWhileTheEngineDoesNotAnswerReadsEngine(t *testing.T) {
	for _, row := range []struct {
		name        string
		unreachable bool
		// cancelled has a stop end a held call before the slots fill.
		cancelled bool
		stop      bool
		want      app.LaunchReason
	}{
		{name: "the engine's last call answered", want: app.ReasonCancelled},
		{name: "the engine's last call found it unreachable", unreachable: true, want: app.ReasonEngine},
		{name: "a call a stop cancelled, after one that answered", cancelled: true, want: app.ReasonCancelled},
		{name: "a Stop at the deadline, the engine unreachable", unreachable: true, stop: true, want: app.ReasonEngine},
		{name: "a Stop at the deadline, the engine answering", stop: true, want: app.ReasonCancelled},
	} {
		t.Run(row.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newEnv(t)
				if row.unreachable {
					e.fe.setHook(onFirst("Remove", func(arg string) error { return down("Remove", arg) }))
					first := newTurn("first")
					e.start(t, first)
					synctest.Wait()
					require.Equal(t, app.ReasonEngine, launchOf(t, e, first.ID).Reason, "the engine was not found unreachable")
				}
				e.fe.block("Remove")
				if row.cancelled {
					stopped := newTurn("stopped")
					e.start(t, stopped)
					synctest.Wait()
					e.r.Stop(stopped.Run, stopped.Agent)
					synctest.Wait()
					require.Equal(t, app.ReasonCancelled, launchOf(t, e, stopped.ID).Reason)
				}
				for i := range 8 {
					e.start(t, newTurn(fmt.Sprintf("holder-%d", i)))
				}
				synctest.Wait()
				require.Len(t, e.r.slots, 8, "the launches do not hold every slot")
				queued := newTurn("queued")
				queued.Deadline = time.Now().Add(5 * time.Second)
				e.start(t, queued)
				synctest.Wait()
				if row.stop {
					e.r.mu.Lock()
					e.r.turns[queued.ID].timer.Stop()
					e.r.mu.Unlock()
				}

				time.Sleep(time.Until(queued.Deadline))
				if row.stop {
					synctest.Wait()
					require.False(t, slices.ContainsFunc(e.launched(), func(l app.Launch) bool { return l.Turn == queued.ID }),
						"the launch ended before the Stop")
					e.r.Stop(queued.Run, queued.Agent)
				}
				synctest.Wait()

				l := launchOf(t, e, queued.ID)
				assert.Equal(t, row.want, l.Reason)
				require.Error(t, l.Err)
			})
		})
	}
}

// TestAStopCancelsALaunchingTurn (Decision 5): a Stop while the launch's
// engine call is held ends the launch's context, so that the held call
// returns; the container the launch made, if any, is removed by its id on a
// fresh context; and Launched reports cancelled, which posts no notice. A
// creation held when the stop comes made no container the runner knows of.
func TestAStopCancelsALaunchingTurn(t *testing.T) {
	for _, row := range []struct {
		held string
		made bool
	}{{held: "Create"}, {held: "Attach", made: true}} {
		t.Run(row.held+" held", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newEnv(t)
				e.fe.block(row.held)
				turn := newTurn("alpha")
				e.start(t, turn)
				synctest.Wait()
				require.Len(t, e.fe.callsOf(row.held), 1)

				e.r.Stop(turn.Run, turn.Agent)
				synctest.Wait()

				l := launchOf(t, e, turn.ID)
				assert.Equal(t, app.ReasonCancelled, l.Reason, "the held call did not return with its context")
				require.Error(t, l.Err)
				if row.made {
					id := e.fe.callsOf("Attach")[0].arg
					removed := slices.IndexFunc(e.fe.calls(), func(c call) bool { return c.String() == "Remove "+id })
					require.GreaterOrEqual(t, removed, 0, "the container was not removed by its id")
					assert.True(t, e.fe.calls()[removed].live, "the removal ran on the cancelled launch's context")
				}
				assert.Empty(t, e.fe.callsOf("Start"))
				assert.Zero(t, registered(e.r))
			})
		})
	}
}

// TestAFenceCancelsTheLaunchsStepsButNotItsStart (Decision 5): the context
// Start was given, which a fence or a drain cancels, ends the launch's steps
// before the container's start, as cancelled; once the engine has the start,
// it is seen through, and the turn runs.
func TestAFenceCancelsTheLaunchsStepsButNotItsStart(t *testing.T) {
	for _, row := range []struct {
		held string
		runs bool
	}{{held: "Create"}, {held: "Start", runs: true}} {
		t.Run(row.held+" held", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newEnv(t)
				release := e.fe.block(row.held)
				ctx, fence := context.WithCancel(e.ctx(t))
				defer fence()
				turn := newTurn("alpha")
				pending, err := e.r.Start(ctx, turn)
				require.NoError(t, err)
				require.True(t, pending)
				synctest.Wait()
				require.Len(t, e.fe.callsOf(row.held), 1)

				fence()
				synctest.Wait()
				if !row.runs {
					assert.Equal(t, app.ReasonCancelled, launchOf(t, e, turn.ID).Reason)
					return
				}
				assert.Empty(t, e.launched(), "the fence ended the container's start")
				release()
				synctest.Wait()
				require.NoError(t, launchOf(t, e, turn.ID).Err)
				c := e.fe.byTurn(turn.ID)
				require.NotNil(t, c)
				running, _, _ := e.fe.state(c)
				assert.True(t, running, "the turn does not run")
				assert.Equal(t, 1, registered(e.r))
			})
		})
	}
}

// TestAStartedTurnStoppedBeforeItRunsIsKilledRemovedAndReportedCancelled
// (Decision 5, steps 7 and 8): a Stop while the engine holds the container's
// start marks the turn stopped; once the start returns, the container is
// killed, waited for and removed, by its id, and Launched reports cancelled;
// no end is reported, since the turn never ran.
func TestAStartedTurnStoppedBeforeItRunsIsKilledRemovedAndReportedCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		release := e.fe.block("Start")
		turn := newTurn("alpha")
		e.start(t, turn)
		synctest.Wait()
		starts := e.fe.callsOf("Start")
		require.Len(t, starts, 1)
		id := starts[0].arg

		e.r.Stop(turn.Run, turn.Agent)
		synctest.Wait()
		assert.Empty(t, e.launched(), "the stop ended the container's start")
		release()
		synctest.Wait()

		said := e.fe.said()
		at := slices.Index(said, "Start "+id)
		require.GreaterOrEqual(t, at, 0)
		assert.Equal(t, []string{"Kill " + id, "Wait " + id, "Remove " + id, "Launched"}, said[at+1:])
		assert.Equal(t, app.ReasonCancelled, launchOf(t, e, turn.ID).Reason)
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		assert.Empty(t, e.exited(), "an end was reported for a turn that never ran")
	})
}

// TestStopAndStopAllNeverWait (Decision 5): Stop, StopAll and a turn's
// deadline send SIGKILL from a goroutine and return, never waiting for the
// engine: with the engine holding every kill from before the first deadline,
// each returns within 50 ms, under the coordinator's lock, which Exited
// takes, and every running turn's container gets its kill. A deadline's kill
// that held the runner's lock would hold the Stop and the StopAll after it.
func TestStopAndStopAllNeverWait(t *testing.T) {
	var coordinator sync.Mutex
	e := newEnv(t, func(p *Ports, _ *Options) {
		exited := p.Exited
		p.Exited = func(x app.Exit) {
			coordinator.Lock()
			defer coordinator.Unlock()
			exited(x)
		}
	})
	var all []*app.Turn
	for _, agent := range []string{"alpha", "beta", "gamma"} {
		all = append(all, newTurn(agent))
	}
	soon := newTurn("delta")
	soon.Deadline = time.Now().Add(500 * time.Millisecond)
	all = append(all, soon)
	release := e.fe.block("Kill")
	t.Cleanup(release)
	for _, turn := range all {
		e.start(t, turn)
	}
	require.Eventually(t, func() bool { return len(e.launched()) == 4 }, 10*time.Second, 5*time.Millisecond)
	ids := map[*app.Turn]string{}
	for _, turn := range all {
		c := e.fe.byTurn(turn.ID)
		require.NotNil(t, c, turn.Agent)
		ids[turn] = c.id
	}
	require.Eventually(t, func() bool { return slices.Contains(e.fe.said(), "Kill "+ids[soon]) }, 10*time.Second, 5*time.Millisecond,
		"the deadline sent no kill")

	coordinator.Lock()
	stopped := returnsWithin(50*time.Millisecond, func() { e.r.Stop("run-1", "alpha") })
	stoppedAll := returnsWithin(50*time.Millisecond, e.r.StopAll)
	coordinator.Unlock()

	assert.True(t, stopped, "Stop waited")
	assert.True(t, stoppedAll, "StopAll waited")
	for _, turn := range all {
		assert.Eventually(t, func() bool { return slices.Contains(e.fe.said(), "Kill "+ids[turn]) }, 10*time.Second, 5*time.Millisecond,
			"no kill for %s", turn.Agent)
	}
	release()
	require.Eventually(t, func() bool { return len(e.exited()) == 4 }, 10*time.Second, 5*time.Millisecond)
	for _, x := range e.exited() {
		assert.Equal(t, item.TurnInterrupted, x.Class, x.Agent)
	}
}

// TestTheSpecIsDeliveredBeforeLaunchedReturns (Decision 5): the spec's
// delivery does not wait for Launched, which waits for the coordinator's
// lock: with Launched held, the spec reaches the attachment.
func TestTheSpecIsDeliveredBeforeLaunchedReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		e := newEnv(t, func(p *Ports, _ *Options) {
			launched := p.Launched
			p.Launched = func(l app.Launch) {
				launched(l)
				<-gate
			}
		})
		t.Cleanup(release)
		turn := newTurn("alpha")
		e.start(t, turn)
		synctest.Wait()

		require.Len(t, e.launched(), 1, "Launched was not called")
		c := e.fe.byTurn(turn.ID)
		require.NotNil(t, c)
		var got struct {
			Turn string `json:"turn"`
		}
		require.NoError(t, json.Unmarshal([]byte(e.fe.received(c)), &got), "the attachment holds no spec")
		assert.Equal(t, turn.ID, got.Turn)
	})
}

// TestTheTurnsEndIsReportedOnceAfterLaunched (Contract C10): a container
// that ends while Launched is held has its end reported only once Launched
// returned, and once.
func TestTheTurnsEndIsReportedOnceAfterLaunched(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		e := newEnv(t, func(p *Ports, _ *Options) {
			launched := p.Launched
			p.Launched = func(l app.Launch) {
				launched(l)
				<-gate
			}
		})
		t.Cleanup(release)
		turn := newTurn("alpha")
		e.fe.play(turn.ID, program{exit: 10})
		e.start(t, turn)
		synctest.Wait()
		c := e.fe.byTurn(turn.ID)
		require.NotNil(t, c)
		running, _, _ := e.fe.state(c)
		require.False(t, running, "the container did not end")
		assert.Empty(t, e.exited(), "the end was reported before Launched returned")

		release()
		synctest.Wait()
		time.Sleep(time.Minute)
		synctest.Wait()

		assert.Equal(t, []app.Exit{{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID, Class: item.TurnEnded}}, e.exited())
		said := e.fe.said()
		assert.Less(t, slices.Index(said, "Launched"), slices.Index(said, "Exited"))
	})
}

// TestASpecThatCannotBeDeliveredEndsTheTurnUnavailable (Decision 5): a spec
// the runner cannot deliver, its agent's skills unread, is the platform's
// failure: the container is killed, the turn ends unavailable, never
// crashed, and the failure is logged.
func TestASpecThatCannotBeDeliveredEndsTheTurnUnavailable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, func(p *Ports, _ *Options) {
			p.Skills = func(context.Context, string, string) ([]string, error) {
				return nil, errors.New("the commit is missing")
			}
		})
		turn := newTurn("alpha")
		e.start(t, turn)
		synctest.Wait()

		starts := e.fe.callsOf("Start")
		require.Len(t, starts, 1)
		assert.Contains(t, e.fe.said(), "Kill "+starts[0].arg, "the container was not killed")
		exits := e.exited()
		require.Len(t, exits, 1)
		assert.Equal(t, item.TurnUnavailable, exits[0].Class)
		lines := logged(t, e.logs, "a turn's spec could not be delivered")
		require.Len(t, lines, 1)
		assert.Equal(t, turn.ID, lines[0]["turn"])
		assert.Contains(t, lines[0]["err"], "the commit is missing")
	})
}

// classRow is a turn's end, as a test plays it.
type classRow struct {
	name string
	prog program
	// atDeadline ends the program at the turn's deadline.
	atDeadline bool
	// ended says the board holds the agent's turn end.
	ended bool
	// stop stops the turn once it runs.
	stop bool
	// deafKill makes the engine refuse every kill.
	deafKill bool
	want     item.TurnClass
}

// playClass plays a row's turn, and returns its end's class.
func playClass(t *testing.T, row *classRow) item.TurnClass {
	t.Helper()
	var class item.TurnClass
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t, func(p *Ports, _ *Options) {
			if row.ended {
				p.Ended = func(context.Context, board.RunID, int, string) (bool, error) { return true, nil }
			}
		})
		turn := newTurn("alpha")
		prog := row.prog
		if row.atDeadline {
			prog.until = turn.Deadline
		}
		e.fe.play(turn.ID, prog)
		if row.deafKill {
			e.fe.setHook(func(method, _ string) error {
				if method == "Kill" {
					return errors.New("the engine refused the kill")
				}
				return nil
			})
		}
		e.start(t, turn)
		synctest.Wait()
		if row.stop {
			e.r.Stop(turn.Run, turn.Agent)
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		exits := e.exited()
		require.Len(t, exits, 1)
		class = exits[0].Class
	})
	return class
}

// TestHowATurnEndedIsReadFromItsContainer (Contract C7, by its order): the
// board's turn end means ended, whatever the container says; an end at the
// deadline, by FinishedAt, or by the runner's own kill, interrupted; the
// engine's OOM killer, crashed; an exit code of the table, its class; a
// runtime's own SIGSEGV or SIGABRT, or any other code, crashed.
func TestHowATurnEndedIsReadFromItsContainer(t *testing.T) {
	for _, row := range []classRow{
		{name: "the board's turn end", prog: program{exit: 1}, ended: true, want: item.TurnEnded},
		{name: "Finished at the deadline", prog: program{exit: 10}, atDeadline: true, deafKill: true, want: item.TurnInterrupted},
		{name: "the runner's own kill", prog: program{forever: true}, stop: true, want: item.TurnInterrupted},
		{name: "OOMKilled with 137", prog: program{exit: 137, oom: true}, want: item.TurnCrashed},
		{name: "10", prog: program{exit: 10}, want: item.TurnEnded},
		{name: "11", prog: program{exit: 11}, want: item.TurnFinished},
		{name: "12", prog: program{exit: 12}, want: item.TurnRefused},
		{name: "13", prog: program{exit: 13}, want: item.TurnVendor},
		{name: "14", prog: program{exit: 14}, want: item.TurnInterrupted},
		{name: "15", prog: program{exit: 15}, want: item.TurnUnavailable},
		{name: "139, SIGSEGV", prog: program{exit: 139}, want: item.TurnCrashed},
		{name: "134, SIGABRT", prog: program{exit: 134}, want: item.TurnCrashed},
		{name: "1", prog: program{exit: 1}, want: item.TurnCrashed},
	} {
		t.Run(row.name, func(t *testing.T) {
			assert.Equal(t, row.want, playClass(t, &row))
		})
	}
}

// TestASignalTheRunnerDidNotSendReadsUnavailable (Review Focus 1): a
// container that ended by SIGHUP, SIGINT, SIGKILL or SIGTERM, which the
// runner did not send, and not for its memory, as an engine's stop or
// restart ends it, is unavailable, never crashed, so that no agent is
// removed for the host's maintenance.
func TestASignalTheRunnerDidNotSendReadsUnavailable(t *testing.T) {
	for _, code := range []int{129, 130, 137, 143} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			assert.Equal(t, item.TurnUnavailable, playClass(t, &classRow{prog: program{exit: code}}))
		})
	}
}

// TestAFailedWaitInspectsTheContainerAgain (Decision 5): after a wait that
// failed, the runner inspects the container again. One that still runs, as
// under the engine's live-restore, is waited for again, and its exit gives
// its class; one that is gone is unavailable; one the engine does not show
// until the deadline plus the slack is unavailable, and left to the sweep.
func TestAFailedWaitInspectsTheContainerAgain(t *testing.T) {
	for _, row := range []struct {
		name string
		// hook, given the fake, is the engine's hook.
		hook func(fe *fakeEngine) func(string, string) error
		prog program
		want item.TurnClass
		// late says the end is reported no earlier than the deadline plus
		// the slack, and nothing removed after.
		late bool
	}{
		{
			name: "running, then exited",
			hook: func(*fakeEngine) func(string, string) error {
				return onFirst("Wait", func(arg string) error { return down("Wait", arg) })
			},
			prog: program{sleep: 2 * time.Second, exit: 11}, want: item.TurnFinished,
		},
		{
			name: "gone",
			hook: func(fe *fakeEngine) func(string, string) error {
				return onFirst("Wait", func(arg string) error {
					fe.drop(arg)
					return down("Wait", arg)
				})
			},
			prog: program{forever: true}, want: item.TurnUnavailable,
		},
		{
			name: "unreachable until the deadline plus the slack",
			hook: func(*fakeEngine) func(string, string) error {
				var gone atomic.Bool
				return func(method, arg string) error {
					if method == "Wait" {
						gone.Store(true)
					}
					if gone.Load() {
						return down(method, arg)
					}
					return nil
				}
			},
			prog: program{forever: true}, want: item.TurnUnavailable, late: true,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newEnv(t)
				turn := newTurn("alpha")
				e.fe.play(turn.ID, row.prog)
				e.fe.setHook(row.hook(e.fe))
				e.start(t, turn)
				synctest.Wait()
				id := e.fe.callsOf("Start")[0].arg

				time.Sleep(2 * time.Minute)
				synctest.Wait()

				exits := e.exited()
				require.Len(t, exits, 1)
				assert.Equal(t, row.want, exits[0].Class)
				onContainer := e.fe.saidOf(id)
				wait := slices.Index(onContainer, "Wait "+id)
				require.GreaterOrEqual(t, wait, 0)
				require.Greater(t, len(onContainer), wait+1, "the failed wait was not followed by an inspect")
				assert.Equal(t, "Inspect "+id, onContainer[wait+1], "the failed wait was not followed by an inspect")
				if row.late {
					assert.False(t, e.exitedAt()[0].Before(turn.Deadline.Add(e.o.Slack)), "the end was reported before the deadline plus the slack")
					said := e.fe.said()
					assert.NotContains(t, said[slices.Index(said, "Exited"):], "Remove "+id, "the container was not left to the sweep")
				}
			})
		})
	}
}

// TestAWaitTheEngineRefusesIsTriedAgainAfterAPause (Decision 5): after a
// failed wait, a container the engine shows running is waited for again; a
// wait the engine refuses at once is tried again after a pause that
// doubles, up to 2 s, never at once, so that the runner does not spin
// against the engine until the turn's deadline. Each refusal takes the
// engine 1 ms, so that a runner that spins shows as thousands of waits.
func TestAWaitTheEngineRefusesIsTriedAgainAfterAPause(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		turn := newTurn("alpha")
		e.fe.play(turn.ID, program{sleep: 5 * time.Second, exit: 11})
		e.fe.setHook(func(method, _ string) error {
			if method == "Wait" {
				time.Sleep(time.Millisecond)
				return errors.New("the engine refused the wait")
			}
			return nil
		})
		e.start(t, turn)
		time.Sleep(10 * time.Second)
		synctest.Wait()

		assert.LessOrEqual(t, len(e.fe.callsOf("Wait")), 10, "the runner waited again at once")
		exits := e.exited()
		require.Len(t, exits, 1)
		assert.Equal(t, item.TurnFinished, exits[0].Class)
	})
}

// TestAContainerStillRunningPastItsDeadlineAfterAFailedWaitIsKilledByName
// (Decision 5): after a failed wait, a container that the engine shows
// running past the turn's deadline plus the slack, its kill by id refused,
// is killed by its name at once, and ends interrupted.
func TestAContainerStillRunningPastItsDeadlineAfterAFailedWaitIsKilledByName(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		turn := newTurn("alpha")
		name := containerName(turn.ID)
		var waited atomic.Bool
		e.fe.setHook(func(method, arg string) error {
			switch {
			case method == "Wait" && waited.CompareAndSwap(false, true):
				return down(method, arg)
			case method == "Kill" && arg != name:
				return errors.New("the engine refused the kill")
			}
			return nil
		})
		e.start(t, turn)
		synctest.Wait()

		time.Sleep(time.Until(turn.Deadline.Add(e.o.Slack)) - time.Nanosecond)
		synctest.Wait()
		assert.NotContains(t, e.fe.said(), "Kill "+name, "killed by its name before the deadline plus the slack")
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		assert.Contains(t, e.fe.said(), "Kill "+name, "not killed by its name at the deadline plus the slack")
		time.Sleep(time.Minute)
		synctest.Wait()

		exits := e.exited()
		require.Len(t, exits, 1)
		assert.Equal(t, item.TurnInterrupted, exits[0].Class)
	})
}

// TestAContainerWhoseDeadlineKillIsLostIsKilledByName (Decision 5): a turn's
// first wait ends at its deadline plus the runtime's slack, so that a
// container the deadline's kill did not reach, and that still runs then, is
// killed by its name at once, ends interrupted, and is forgotten, though no
// wait failed: a container the runner tracks is never left to outlive its
// round.
func TestAContainerWhoseDeadlineKillIsLostIsKilledByName(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		turn := newTurn("alpha")
		name := containerName(turn.ID)
		e.fe.setHook(func(method, arg string) error {
			if method == "Kill" && arg != name {
				return errors.New("the engine refused the kill")
			}
			return nil
		})
		e.start(t, turn)
		synctest.Wait()
		id := e.fe.callsOf("Start")[0].arg

		time.Sleep(time.Until(turn.Deadline.Add(e.o.Slack)) - time.Nanosecond)
		synctest.Wait()
		require.Contains(t, e.fe.said(), "Kill "+id, "the deadline sent no kill")
		assert.NotContains(t, e.fe.said(), "Kill "+name, "killed by its name before the deadline plus the slack")
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		assert.Contains(t, e.fe.said(), "Kill "+name, "not killed by its name at the deadline plus the slack")
		time.Sleep(time.Minute)
		synctest.Wait()

		exits := e.exited()
		require.Len(t, exits, 1, "the turn's end was not reported")
		assert.Equal(t, item.TurnInterrupted, exits[0].Class)
		assert.Zero(t, registered(e.r), "the turn was not forgotten")
	})
}

// TestATurnsContainerIsRemovedAfterItsEndIsReported (Decision 5): once a
// turn's end is known, the runner inspects its container, reports the end,
// forgets the turn, and then removes the container, by its id; a removal
// that fails is logged, and changes nothing else: the sweep takes the
// container later.
func TestATurnsContainerIsRemovedAfterItsEndIsReported(t *testing.T) {
	for _, row := range []struct {
		name  string
		fails bool
	}{{name: "removed"}, {name: "a removal that fails", fails: true}} {
		t.Run(row.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newEnv(t)
				turn := newTurn("alpha")
				e.fe.play(turn.ID, program{exit: 10})
				if row.fails {
					e.fe.setHook(func(method, arg string) error {
						if method == "Remove" && arg != containerName(turn.ID) {
							return errors.New("a removal of the container is in progress")
						}
						return nil
					})
				}
				e.start(t, turn)
				synctest.Wait()

				id := e.fe.callsOf("Start")[0].arg
				// Launched's mark races the turn's wait, and is not compared.
				said := slices.DeleteFunc(e.fe.said(), func(s string) bool { return s == "Launched" })
				exited := slices.Index(said, "Exited")
				require.Positive(t, exited, "no end was reported")
				assert.Equal(t, "Inspect "+id, said[exited-1])
				assert.Equal(t, []string{"Remove " + id}, said[exited+1:])
				assert.Equal(t, []app.Exit{{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID, Class: item.TurnEnded}}, e.exited())
				assert.Zero(t, registered(e.r))
				lines := logged(t, e.logs, "a container could not be removed")
				if !row.fails {
					assert.Empty(t, lines)
					return
				}
				require.Len(t, lines, 1)
				assert.Equal(t, "WARN", lines[0]["level"])
				assert.Equal(t, id, lines[0]["container"])
				assert.Contains(t, lines[0]["err"], "in progress")
			})
		})
	}
}

// TestKillAllRemovesEveryTurnsContainerAndWaitsAtMostASecond (Decision 5,
// serve's exits): KillAll cancels every launch and removes, by name and in
// parallel, the container of every turn the runner has, launching, started
// or running; with the engine silent it returns at 1 s, and with the engine
// answering, as soon as every removal is answered. In parallel: each removal
// is made on a live context, where removals one after another would make
// the later ones on the context the first outlasted. A started turn reads
// cancelled, as a stop's does, whether its start then succeeds or fails, as
// when KillAll's removal reaches the engine first.
func TestKillAllRemovesEveryTurnsContainerAndWaitsAtMostASecond(t *testing.T) {
	for _, row := range []struct {
		name   string
		silent bool
		took   time.Duration
	}{{name: "the engine silent", silent: true, took: time.Second}, {name: "the engine answering"}} {
		t.Run(row.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				e := newEnv(t)
				running := newTurn("alpha")
				e.start(t, running)
				synctest.Wait()
				require.Len(t, e.launched(), 1)
				releaseStart := e.fe.block("Start")
				started := newTurn("beta")
				e.start(t, started)
				synctest.Wait()
				e.fe.block("Create")
				launching := newTurn("gamma")
				e.start(t, launching)
				synctest.Wait()
				releaseRemove := func() {}
				if row.silent {
					releaseRemove = e.fe.block("Remove")
				}
				e.fe.forgetCalls()

				began := time.Now()
				e.r.KillAll(t.Context())
				took := time.Since(began)

				releaseRemove()
				assert.Equal(t, row.took, took)
				var removed []string
				for _, c := range e.fe.callsOf("Remove") {
					removed = append(removed, c.arg)
					assert.True(t, c.live, "the removal of %s was made on a context that had ended: the removals did not run in parallel", c.arg)
				}
				assert.ElementsMatch(t, []string{containerName(running.ID), containerName(started.ID), containerName(launching.ID)}, removed)
				synctest.Wait()
				assert.Equal(t, app.ReasonCancelled, launchOf(t, e, launching.ID).Reason, "the launch was not cancelled")
				releaseStart()
				synctest.Wait()
				assert.Equal(t, app.ReasonCancelled, launchOf(t, e, started.ID).Reason, "the start in flight was not cancelled")
			})
		})
	}
}

// TestARepeatedStartStartsNothing (R25): a Start whose id the runner has
// starts nothing, and answers as the first Start now stands: (true, nil)
// while the turn launches, (false, nil) once its Launched was reported. The
// turn is created once.
func TestARepeatedStartStartsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		release := e.fe.block("Create")
		turn := newTurn("alpha")
		e.start(t, turn)
		synctest.Wait()

		pending, err := e.r.Start(e.ctx(t), turn)
		require.NoError(t, err)
		assert.True(t, pending, "a Start while the turn launches")
		release()
		synctest.Wait()
		require.Len(t, e.launched(), 1)
		pending, err = e.r.Start(e.ctx(t), turn)
		require.NoError(t, err)
		assert.False(t, pending, "a Start once the turn's launch was reported")
		synctest.Wait()

		assert.Len(t, e.fe.callsOf("Create"), 1)
		assert.Len(t, e.launched(), 1)
	})
}

// TestATurnIsForgottenOnceItsEndIsReported (Contract C10): a turn is
// forgotten once its end is reported, and not before: a Start with its id
// while the report is in flight starts nothing; after the report, a Start
// with its id launches the turn again. The first report is held on a
// channel, and any later one returns at once, so that a runner that forgot
// the turn early fails the test rather than holding a second report where
// the bubble cannot see it wait.
func TestATurnIsForgottenOnceItsEndIsReported(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		var first atomic.Bool
		e := newEnv(t, func(p *Ports, _ *Options) {
			exited := p.Exited
			p.Exited = func(x app.Exit) {
				exited(x)
				if first.CompareAndSwap(false, true) {
					<-gate
				}
			}
		})
		t.Cleanup(release)
		turn := newTurn("alpha")
		e.fe.play(turn.ID, program{exit: 10})
		e.start(t, turn)
		synctest.Wait()
		require.Len(t, e.exited(), 1, "the turn's end was not reported")

		pending, err := e.r.Start(e.ctx(t), turn)
		require.NoError(t, err)
		assert.False(t, pending, "a Start while the turn's end is reported")
		synctest.Wait()
		assert.Len(t, e.fe.callsOf("Create"), 1, "the turn was launched again before its end was reported")

		release()
		synctest.Wait()
		e.start(t, turn)
		synctest.Wait()
		assert.Len(t, e.fe.callsOf("Create"), 2, "the turn was not launched again")
		assert.Len(t, e.launched(), 2)
	})
}

// TestTheOutputIsCappedAndTheTurnNeverFailsForIt (Plan 7d-1, Decision 12):
// a turn's output file, <runner.output>/<turn id>.out, mode 0600, keeps the
// first OutputCap bytes of its output, and the runner drops the rest, so
// that a turn that prints 3 MiB leaves a 1 MiB file, and ends ended. A line
// first sets the copy's reads off the cap's boundary, which a cap that kept
// the whole read across it would pass.
func TestTheOutputIsCappedAndTheTurnNeverFailsForIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		turn := newTurn("alpha")
		line := "agentswarm: a line on standard error\n"
		e.fe.play(turn.ID, program{line: line, print: 3 << 20, exit: 10})
		e.start(t, turn)
		synctest.Wait()

		exits := e.exited()
		require.Len(t, exits, 1)
		assert.Equal(t, item.TurnEnded, exits[0].Class)
		path := filepath.Join(e.o.Output, turn.ID+".out")
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, int64(1<<20), info.Size())
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "only the runner's user reads a turn's output")
		out, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(string(out), line), "the file keeps the turn's first bytes")
		assert.Empty(t, strings.Trim(string(out[len(line):]), "x"), "the file keeps the turn's first bytes")
	})
}

// TestTurnExitedSaysTheContainerAndHowItEnded (Contract C3; R23): turn
// exited, at Info, names the turn, its container's id, its class, its exit
// code and whether the engine's OOM killer ended it.
func TestTurnExitedSaysTheContainerAndHowItEnded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		turn := newTurn("alpha")
		e.fe.play(turn.ID, program{exit: 12})
		e.start(t, turn)
		synctest.Wait()

		lines := logged(t, e.logs, "turn exited")
		require.Len(t, lines, 1)
		assert.Equal(t, "INFO", lines[0]["level"])
		assert.Equal(t, turn.ID, lines[0]["turn"])
		assert.Equal(t, e.fe.callsOf("Start")[0].arg, lines[0]["container"])
		assert.Equal(t, string(item.TurnRefused), lines[0]["class"])
		assert.Equal(t, float64(12), lines[0]["exit"]) //nolint:testifylint // float-compare: JSON numbers decode as float64, and 12 is exact.
		assert.Equal(t, false, lines[0]["oom"])
	})
}

// TestOpenRefusesWhatTheRunnerCannotRunOn (Contract C14, in Open's order):
// each refusal names its deployment key: an engine that does not answer,
// runner.docker.endpoint; a store serve cannot write, runner.docker.store,
// and not holder, since the holder's lock lives in the store; the holder's
// lock another serve holds, holder; an image neither on the engine nor in
// the store, or one that breaks the contract, runner.images.thinloop; a save
// the engine holds past openWait, runner.docker.endpoint; and the start
// sweep's listing, which meets no engine, or which the engine holds past
// openWait, or refuses, runner.docker.endpoint, since the sweep comes before
// any turn. A refusal leaves the holder's lock free.
func TestOpenRefusesWhatTheRunnerCannotRunOn(t *testing.T) {
	for _, row := range []struct {
		name string
		key  string
		// set prepares the refusal.
		set    func(t *testing.T, e *env)
		asRoot bool // the row runs as root, which writes into any directory
		took   time.Duration
		// locked says the test holds the holder's lock itself.
		locked bool
	}{
		{name: "the engine does not answer", key: "runner.docker.endpoint", asRoot: true, set: func(_ *testing.T, e *env) {
			e.fe.setHook(onFirst("Version", func(string) error { return down("Version", "") }))
		}},
		{name: "a store serve cannot write", key: "runner.docker.store", set: func(t *testing.T, e *env) {
			require.NoError(t, os.Chmod(e.o.Store, 0o500))
			t.Cleanup(func() { _ = os.Chmod(e.o.Store, 0o700) })
		}},
		{name: "the lock another serve holds", key: "holder", asRoot: true, locked: true, set: func(t *testing.T, e *env) {
			unlock, err := lockHolder(e.o.Store, e.o.Tenant, e.o.Holder)
			require.NoError(t, err)
			t.Cleanup(unlock)
		}},
		{name: "an image neither on the engine nor in the store", key: "runner.images.thinloop", asRoot: true, set: func(_ *testing.T, e *env) {
			e.o.Images["thinloop"] = "sha256:" + strings.Repeat("e", 64)
		}},
		{name: "an image that breaks the contract", key: "runner.images.thinloop", asRoot: true, set: func(_ *testing.T, e *env) {
			img := goodImage()
			img.ID, img.Volumes = "sha256:"+strings.Repeat("b", 64), []string{"/data"}
			e.fe.addImage(img)
			e.o.Images["thinloop"] = img.ID
		}},
		{name: "a save held past openWait", key: "runner.docker.endpoint", asRoot: true, took: openWait, set: func(_ *testing.T, e *env) {
			e.fe.block("Save")
		}},
		{name: "the start sweep's listing meets no engine", key: "runner.docker.endpoint", asRoot: true, set: func(_ *testing.T, e *env) {
			e.fe.setHook(onFirst("List", func(arg string) error { return down("List", arg) }))
		}},
		{name: "the start sweep's listing held past openWait", key: "runner.docker.endpoint", asRoot: true, took: openWait, set: func(_ *testing.T, e *env) {
			e.fe.block("List")
		}},
		{name: "the start sweep's listing refused", key: "runner.docker.endpoint", asRoot: true, set: func(_ *testing.T, e *env) {
			e.fe.setHook(onFirst("List", func(string) error { return errors.New("the engine refused the listing") }))
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			if !row.asRoot && os.Geteuid() == 0 {
				t.Skip("root writes into a read-only directory")
			}
			synctest.Test(t, func(t *testing.T) {
				e := newFixture(t)
				row.set(t, e)
				t.Cleanup(e.fe.releaseAll)

				began := time.Now()
				r, err := Open(t.Context(), e.ports(), e.o)
				took := time.Since(began)

				if r != nil {
					t.Cleanup(func() { _ = r.Close() })
				}
				require.Error(t, err)
				assert.True(t, strings.HasPrefix(err.Error(), row.key+": "), "the refusal %q does not name %s", err, row.key)
				assert.Equal(t, row.took, took)
				require.NoError(t, os.Chmod(e.o.Store, 0o700), "a store the test can lock in")
				if !row.locked {
					unlock, err := lockHolder(e.o.Store, e.o.Tenant, e.o.Holder)
					require.NoError(t, err, "the refusal kept the holder's lock")
					unlock()
				}
			})
		})
	}
}

// TestOpenSavesEachImageToTheStore (Decision 6): Open saves each image of
// runner.images that the engine has and the store lacks, once, as
// <hex>.tar; a second Open, after Close, finds it there and saves nothing.
func TestOpenSavesEachImageToTheStore(t *testing.T) {
	e := newFixture(t)
	id := goodImage().ID

	r, err := Open(t.Context(), e.ports(), e.o)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })

	saves := e.fe.callsOf("Save")
	require.Len(t, saves, 1)
	assert.Equal(t, id, saves[0].arg)
	assert.FileExists(t, filepath.Join(e.o.Store, strings.TrimPrefix(id, "sha256:")+".tar"))
	require.NoError(t, r.Close())
	e.fe.forgetCalls()
	again, err := Open(t.Context(), e.ports(), e.o)
	require.NoError(t, err)
	t.Cleanup(func() { _ = again.Close() })
	assert.Empty(t, e.fe.callsOf("Save"))
}

// TestCloseReleasesTheHoldersLockAndClosesTheEngine (Contract C14): Close
// releases the holder's lock, so that another Open of the pair may take it,
// and closes the engine's client, once; it makes no other engine call, and
// kills nothing, even with a turn running: serve's KillAll comes first.
func TestCloseReleasesTheHoldersLockAndClosesTheEngine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e := newEnv(t)
		e.start(t, newTurn("alpha"))
		synctest.Wait()
		require.Len(t, e.launched(), 1)
		e.fe.forgetCalls()

		require.NoError(t, e.r.Close())
		require.NoError(t, e.r.Close(), "a second Close")
		synctest.Wait()

		assert.Equal(t, []string{"Close"}, e.fe.said(), "Close made another engine call, or killed a turn")
		unlock, err := lockHolder(e.o.Store, e.o.Tenant, e.o.Holder)
		require.NoError(t, err, "Close kept the holder's lock")
		unlock()
	})
}

// TestOpenNeedsItsPortsAndOptions: Open needs every port but its log, and
// options that check passes; without them it fails, and calls no engine.
func TestOpenNeedsItsPortsAndOptions(t *testing.T) {
	for _, row := range []struct {
		name string
		edit func(*Ports, *Options)
	}{
		{name: "no engine", edit: func(p *Ports, _ *Options) { p.Engine = nil }},
		{name: "no holding", edit: func(p *Ports, _ *Options) { p.Holding = nil }},
		{name: "no skills", edit: func(p *Ports, _ *Options) { p.Skills = nil }},
		{name: "no board", edit: func(p *Ports, _ *Options) { p.Ended = nil }},
		{name: "no Exited", edit: func(p *Ports, _ *Options) { p.Exited = nil }},
		{name: "no Launched", edit: func(p *Ports, _ *Options) { p.Launched = nil }},
		{name: "options check refuses", edit: func(_ *Ports, o *Options) { o.OutputCap = 0 }},
	} {
		t.Run(row.name, func(t *testing.T) {
			e := newFixture(t)
			p, o := e.ports(), e.o
			row.edit(&p, &o)

			r, err := Open(t.Context(), p, o)

			if r != nil {
				t.Cleanup(func() { _ = r.Close() })
			}
			require.Error(t, err)
			assert.Empty(t, e.fe.calls(), "Open called the engine")
		})
	}
	e := newFixture(t)
	p := e.ports()
	p.Log = nil
	r, err := Open(t.Context(), p, e.o)
	require.NoError(t, err, "without a log")
	t.Cleanup(func() { _ = r.Close() })
	require.NoError(t, r.Close())
}

// TestTheEnginesRecordIsWhatItsLastCallFound (Contract C14; plan review 2,
// F3): the runner's record of the engine, which a launch stopped at its
// deadline reads, is what the last engine call found. A call the engine
// answered, an error it gave included, marks it reachable; one that found no
// connection, or ran out its own bound, marks it unreachable; one that a
// cancel ended, as a stop, a fence or a drain ends it, leaves the record as
// it was, either way; and so does a wait for a container that runs past the
// wait's deadline, since the engine answers a wait once the container ends.
func TestTheEnginesRecordIsWhatItsLastCallFound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fe := newFakeEngine(goodImage())
		w := &watched{e: fe}
		ctx := t.Context()
		cancelledAfter := func(d time.Duration) context.Context {
			c, cancel := context.WithCancel(ctx)
			time.AfterFunc(d, cancel)
			return c
		}

		_, err := w.Version(ctx)
		require.NoError(t, err)
		assert.False(t, w.unreachable.Load(), "an answer")
		fe.setHook(onFirst("Version", func(string) error { return down("Version", "") }))
		_, err = w.Version(ctx)
		require.ErrorIs(t, err, ErrUnreachable)
		assert.True(t, w.unreachable.Load(), "no connection")
		_, err = w.Image(ctx, "sha256:"+strings.Repeat("f", 64))
		require.ErrorIs(t, err, ErrNoImage)
		assert.False(t, w.unreachable.Load(), "an error the engine gave is an answer")

		release := fe.block("Inspect")
		_, err = w.Inspect(cancelledAfter(time.Second), "x")
		require.ErrorIs(t, err, context.Canceled)
		assert.False(t, w.unreachable.Load(), "a cancel changed a record of an engine that answers")
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err = w.Inspect(bounded, "x")
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.True(t, w.unreachable.Load(), "a call that ran out its own bound")
		_, err = w.Inspect(cancelledAfter(time.Second), "x")
		require.ErrorIs(t, err, context.Canceled)
		assert.True(t, w.unreachable.Load(), "a cancel changed a record of an engine that does not answer")
		release()

		id, err := w.Create(ctx, "agentswarm-turn-record", &container.Config{Image: goodImage().ID}, &container.HostConfig{})
		require.NoError(t, err)
		require.NoError(t, w.Start(ctx, id))
		assert.False(t, w.unreachable.Load())
		waited, cancelWait := context.WithTimeout(ctx, 10*time.Second)
		defer cancelWait()
		require.ErrorIs(t, w.Wait(waited, id), context.DeadlineExceeded)
		assert.False(t, w.unreachable.Load(), "a wait for a container that ran past it marked the engine unreachable")
		require.NoError(t, w.Kill(ctx, id))
	})
}

// TestNewCertificateIsAnECDSAP256LeafForBothGatewaysForTenYears (Decision
// 2): the gateways' certificate is a self-signed ECDSA P-256 leaf for
// exactly mcp.agentswarm.internal and llm.agentswarm.internal, valid until
// ten years after now; a client that trusts it alone completes a TLS 1.3
// handshake, over a pipe, with a server that holds it, for each name, and
// verifies it for no other. The server sends no session ticket, which a
// client that has finished its handshake would leave unread on the pipe.
func TestNewCertificateIsAnECDSAP256LeafForBothGatewaysForTenYears(t *testing.T) {
	now := time.Date(2026, 10, 29, 9, 30, 0, 0, time.UTC)

	c, err := NewCertificate(now)

	require.NoError(t, err)
	block, rest := pem.Decode([]byte(c.PEM))
	require.NotNil(t, block, "the PEM holds no block")
	assert.Equal(t, "CERTIFICATE", block.Type)
	assert.Empty(t, rest)
	require.Len(t, c.TLS.Certificate, 1)
	assert.Equal(t, block.Bytes, c.TLS.Certificate[0], "the PEM and the TLS certificate differ")
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	public, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	require.True(t, ok, "the key is not ECDSA")
	assert.Equal(t, elliptic.P256(), public.Curve)
	assert.ElementsMatch(t, []string{"mcp.agentswarm.internal", "llm.agentswarm.internal"}, leaf.DNSNames)
	assert.Empty(t, leaf.IPAddresses)
	assert.Empty(t, leaf.URIs)
	assert.False(t, leaf.IsCA, "a leaf")
	assert.Equal(t, now.AddDate(10, 0, 0), leaf.NotAfter)
	assert.False(t, leaf.NotBefore.After(now))
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM([]byte(c.PEM)))
	for _, name := range []string{"mcp.agentswarm.internal", "llm.agentswarm.internal"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			server, client := net.Pipe()
			t.Cleanup(func() { _, _ = server.Close(), client.Close() })
			served := make(chan error, 1)
			go func() {
				s := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{c.TLS}, MinVersion: tls.VersionTLS13, SessionTicketsDisabled: true})
				served <- s.HandshakeContext(ctx)
			}()
			tc := tls.Client(client, &tls.Config{
				RootCAs: roots, ServerName: name, MinVersion: tls.VersionTLS13,
				Time: func() time.Time { return now.Add(time.Hour) },
			})

			require.NoError(t, tc.HandshakeContext(ctx))
			assert.Equal(t, uint16(tls.VersionTLS13), tc.ConnectionState().Version)
			require.NoError(t, <-served)
		})
	}
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "other.agentswarm.internal", CurrentTime: now.Add(time.Hour)})
	require.Error(t, err, "the certificate verifies for another name")
}
