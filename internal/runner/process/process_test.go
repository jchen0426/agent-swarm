package process_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/ids"
	"github.com/jchen0426/agent-swarm/internal/outcome"
	"github.com/jchen0426/agent-swarm/internal/runner/process"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/tokens"
)

// TestMain plays a fake turn when the runner runs this test binary as its
// runtime, `<binary> turn`, and a fake turn's process of its own when the fake
// starts one, `<binary> linger`; otherwise it runs the tests. The runner gives
// a turn no environment of the test's, so the arguments tell the modes apart.
//
// The fakes end with syscall.Exit, which runs none of the runtime's exit
// hooks: under go test -cover, os.Exit would write a warning that no coverage
// directory is set on the fake's standard error, which is its output file.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 {
		switch os.Args[1] {
		case "turn":
			syscall.Exit(fakeTurn())
		case "linger":
			linger()
		}
	}
	os.Exit(m.Run())
}

// script is what a fake turn does, as its spec's prompt says it, in JSON.
type script struct {
	// Record names the file the fake writes what it saw to (seen), once it
	// has done all the rest but its sleep.
	Record string `json:"record,omitempty"`
	// Deaf ignores SIGTERM, SIGHUP and SIGINT, and the end of standard input.
	// A fake that is not deaf ends at its input's end, as the runtime does:
	// it marks the end beside its record, and exits 14.
	Deaf bool `json:"deaf,omitempty"`
	// Child starts a process of the fake's own, which ignores its parent's
	// end. With Detach, it runs in a session of its own, which takes it out
	// of the turn's process group.
	Child  bool `json:"child,omitempty"`
	Detach bool `json:"detach,omitempty"`
	// Stderr is written on standard error, and then Print bytes on standard
	// output.
	Stderr string `json:"stderr,omitempty"`
	Print  int    `json:"print,omitempty"`
	// Signal kills the fake with SIGKILL, which it sends itself.
	Signal bool `json:"signal,omitempty"`
	// Sleep is how long the fake runs on before it exits.
	Sleep time.Duration `json:"sleep,omitempty"`
	// Exit is the fake's exit code.
	Exit int `json:"exit"`
}

// seen is what a fake turn saw.
type seen struct {
	Spec json.RawMessage `json:"spec"`
	Args []string        `json:"args"`
	Env  []string        `json:"env"`
	Dir  string          `json:"dir"`
	PID  int             `json:"pid"`
	// Child is the process id of the fake's own process, when it started one.
	Child int `json:"child,omitempty"`
}

// fakeTurn plays a turn as the runtime takes one: it reads its spec, one JSON
// object, from standard input, which it then watches for its end, and does
// what the spec's prompt says. A spec it cannot read exits 1, as the
// runtime's does.
func fakeTurn() int {
	var raw json.RawMessage
	if err := json.NewDecoder(os.Stdin).Decode(&raw); err != nil {
		return fail(fmt.Errorf("read the turn's spec: %w", err))
	}
	var spec struct {
		Prompt string `json:"prompt"`
	}
	var s script
	if err := json.Unmarshal(raw, &spec); err != nil {
		return fail(err)
	}
	if err := json.Unmarshal([]byte(spec.Prompt), &s); err != nil {
		return fail(fmt.Errorf("read the fake's script: %w", err))
	}
	if s.Deaf {
		signal.Ignore(syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	} else {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			if s.Record != "" {
				_ = os.WriteFile(s.Record+".eof", nil, 0o600)
			}
			syscall.Exit(outcome.ExitInterrupted)
		}()
	}
	saw := seen{Spec: raw, Args: os.Args, Env: os.Environ(), PID: os.Getpid()}
	saw.Dir, _ = os.Getwd()
	if s.Child {
		self, err := os.Executable()
		if err != nil {
			return fail(err)
		}
		child := exec.Command(self, "linger")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: s.Detach}
		if err := child.Start(); err != nil {
			return fail(err)
		}
		saw.Child = child.Process.Pid
	}
	_, _ = os.Stderr.WriteString(s.Stderr)
	_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), s.Print))
	if s.Record != "" {
		if err := record(s.Record, &saw); err != nil {
			return fail(err)
		}
	}
	if s.Signal {
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}
	time.Sleep(s.Sleep)
	return s.Exit
}

// fail ends a fake turn that cannot play its script, as the runtime ends one
// that crashed.
func fail(err error) int {
	_, _ = fmt.Fprintf(os.Stderr, "agentswarm: %v\n", err)
	return 1
}

// record writes what a fake turn saw, whole: a reader never finds half of it.
func record(name string, saw *seen) error {
	b, err := json.Marshal(saw)
	if err != nil {
		return err
	}
	if err := os.WriteFile(name+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(name+".tmp", name)
}

// linger is a fake turn's process of its own: it ignores its parent's end,
// and the signals that may come with it, and sleeps a minute, unless a kill
// ends it first.
func linger() {
	signal.Ignore(syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	time.Sleep(time.Minute)
	syscall.Exit(0)
}

// epoch is the holding the tests' turns start under.
const epoch = 7

// The skills the tests' agents have, as Skills reads them.
var skills = []string{"You are alpha.", "Read the board before you post."}

// env is a runner under test, with the ports it reads and reports through.
type env struct {
	r    *process.Runner
	out  string // the turns' output directory
	logs *syncBuffer
	held *holdingFake
	// hold, when set, sees each record the runner logs before it is logged.
	hold atomic.Pointer[func(slog.Record)]

	mu      sync.Mutex
	reports []report
}

// report is a turn's end, as the runner reported it, and when.
type report struct {
	app.Exit
	at time.Time
}

// newEnv returns a runner of the fake turn, with ports that keep the ends
// reported, as edit changes them. At the test's end, every turn is stopped,
// and its end reported, before the test's directories go.
func newEnv(t *testing.T, edit ...func(*process.Ports, *process.Options)) *env {
	t.Helper()
	e := &env{out: t.TempDir(), logs: &syncBuffer{}, held: &holdingFake{}}
	e.held.valid.Store(true)
	p, o := e.ports(), options(t, e.out)
	for _, f := range edit {
		f(&p, &o)
	}
	r, err := process.New(p, o)
	require.NoError(t, err)
	e.r = r
	t.Cleanup(func() {
		r.StopAll()
		assert.Eventually(t, func() bool {
			return len(e.reported()) >= strings.Count(e.logs.String(), `"msg":"turn started"`)
		}, 10*time.Second, 5*time.Millisecond, "a turn's end was never reported")
	})
	return e
}

// ports are the runner's ports in a test: a holding that holds, skills read
// at once, a board that holds no turn's end, and an Exited that keeps the
// ends reported.
func (e *env) ports() process.Ports {
	return process.Ports{
		Holding: e.held,
		Skills:  func(context.Context, string, string) ([]string, error) { return slices.Clone(skills), nil },
		Ended:   func(context.Context, board.RunID, int, string) (bool, error) { return false, nil },
		Exited: func(x app.Exit) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.reports = append(e.reports, report{Exit: x, at: time.Now()})
		},
		Log: slog.New(hooked{Handler: slog.NewJSONHandler(e.logs, nil), hook: &e.hold}),
	}
}

// options run the fake turn, this test binary, with Section 15's values.
func options(t *testing.T, out string) process.Options {
	t.Helper()
	return process.Options{
		Binary: binary(t), Output: out, OutputCap: 1 << 20, Slack: 2 * time.Second,
		MCP: "http://127.0.0.1:53001/", LLM: "http://127.0.0.1:53002", MaxTokens: 4096,
	}
}

// binary is this test binary, which plays the fake turn.
func binary(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	return self
}

// ctx is a context of the coordinator's: it carries the holding's epoch.
func (e *env) ctx(t *testing.T) context.Context {
	return holding.WithEpoch(t.Context(), epoch)
}

// reported returns the turns' ends reported so far, in order.
func (e *env) reported() []report {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.reports)
}

// await waits until the runner has reported n turns' ends, and returns them.
func (e *env) await(t *testing.T, n int) []report {
	t.Helper()
	require.Eventually(t, func() bool { return len(e.reported()) >= n }, 10*time.Second, 5*time.Millisecond,
		"the runner did not report %d turns' ends", n)
	return e.reported()
}

// newTurn returns agent alpha's turn in round 1 of run-1, which plays s, its
// round's deadline at deadline.
func newTurn(t *testing.T, s *script, deadline time.Time) *app.Turn {
	t.Helper()
	prompt, err := json.Marshal(s)
	require.NoError(t, err)
	return &app.Turn{
		ID: ids.UUIDv7{}.NewID(), Run: "run-1", Agent: "alpha", Round: 1, Deadline: deadline,
		MCP: tokens.New(tokens.MCP), LLM: tokens.New(tokens.LLM), Commit: "4b825dc642cb6eb9a060e54bf8d69288fbee4904",
		Image: "sha256:" + strings.Repeat("0", 64), Model: "claude-sonnet-5-5", Skills: []string{"triage"}, Prompt: string(prompt),
	}
}

// awaitSeen waits until a fake turn has recorded what it saw, and returns it.
func awaitSeen(t *testing.T, name string) seen {
	t.Helper()
	require.Eventually(t, func() bool { return exists(name) }, 10*time.Second, 5*time.Millisecond, "the turn did not record what it saw")
	b, err := os.ReadFile(name)
	require.NoError(t, err)
	var s seen
	require.NoError(t, json.Unmarshal(b, &s))
	return s
}

// exists reports whether a file is there.
func exists(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}

// output returns a turn's output file.
func output(t *testing.T, dir, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, id+".out"))
	require.NoError(t, err)
	return string(b)
}

// names returns the names of a directory's files.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// gone reports whether no process has pid any more, or, for a negative pid,
// no process is left in the process group -pid.
func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
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

func (h *holdingFake) epochs() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.asked)
}

// hooked hands each record to its hook, when one is set, before it logs it.
type hooked struct {
	slog.Handler
	hook *atomic.Pointer[func(slog.Record)]
}

//nolint:gocritic // hugeParam: slog.Handler takes a record by value.
func (h hooked) Handle(ctx context.Context, r slog.Record) error {
	if f := h.hook.Load(); f != nil {
		(*f)(r)
	}
	return h.Handler.Handle(ctx, r)
}

func (h hooked) WithAttrs(attrs []slog.Attr) slog.Handler {
	return hooked{Handler: h.Handler.WithAttrs(attrs), hook: h.hook}
}

func (h hooked) WithGroup(name string) slog.Handler {
	return hooked{Handler: h.Handler.WithGroup(name), hook: h.hook}
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

// Before a turn starts, the runner asks the holding whether the epoch of the
// coordinator's context still holds (Plan 7d-1, Decision 4). A refusal fails
// the launch, which the coordinator tries again and counts as unstarted:
// Start fails, without a lost lease, since a refusal never fences; nothing is
// launched, no output file is made and no end is reported; and the refusal is
// logged. A context without an epoch is a bug, refused and logged as one.
func TestStartChecksTheHoldingFirst(t *testing.T) {
	for _, c := range []struct {
		name  string
		ctx   func(context.Context) context.Context
		valid bool
		msg   string
		level string
		attrs func(*app.Turn) map[string]any
		asked []int64
	}{
		{
			name: "the holding refuses", ctx: func(ctx context.Context) context.Context { return holding.WithEpoch(ctx, epoch) },
			msg: "holding refused", level: "WARN", attrs: func(*app.Turn) map[string]any { return map[string]any{"epoch": float64(epoch), "request": "turn"} },
			asked: []int64{epoch},
		},
		{
			name: "the context carries no epoch", ctx: func(ctx context.Context) context.Context { return ctx }, valid: true,
			msg: "turn without an epoch", level: "ERROR", attrs: func(turn *app.Turn) map[string]any { return map[string]any{"turn": turn.ID} },
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.held.valid.Store(c.valid)
			seenAt := filepath.Join(t.TempDir(), "seen.json")
			turn := newTurn(t, &script{Record: seenAt, Sleep: 5 * time.Second, Exit: outcome.ExitEnded}, time.Now().Add(time.Minute))

			err := e.r.Start(c.ctx(t.Context()), turn)

			require.Error(t, err)
			require.NotErrorIs(t, err, holding.ErrLeaseLost, "a refusal never fences")
			lines := logged(t, e.logs, c.msg)
			require.Len(t, lines, 1)
			assert.Equal(t, c.level, lines[0]["level"])
			for k, v := range c.attrs(turn) {
				assert.Equal(t, v, lines[0][k], k)
			}
			assert.Equal(t, c.asked, e.held.epochs())
			assert.Never(t, func() bool { return exists(seenAt) }, 500*time.Millisecond, 10*time.Millisecond, "nothing is launched")
			assert.Empty(t, logged(t, e.logs, "turn started"))
			assert.Empty(t, names(t, e.out), "no output file is made")
			assert.Empty(t, e.reported(), "no end is reported")
		})
	}
}

// Start returns once the turn's process runs, without waiting for the turn:
// a turn that runs for 5 s has its Start back within 200 ms. The runner logs
// "turn started" with the turn's id and its process's id (Contract C3).
func TestStartReturnsBeforeTheTurnRuns(t *testing.T) {
	e := newEnv(t)
	seenAt := filepath.Join(t.TempDir(), "seen.json")
	turn := newTurn(t, &script{Record: seenAt, Sleep: 5 * time.Second, Exit: outcome.ExitEnded}, time.Now().Add(time.Minute))

	began := time.Now()
	require.NoError(t, e.r.Start(e.ctx(t), turn))
	took := time.Since(began)

	assert.Less(t, took, 200*time.Millisecond)
	s := awaitSeen(t, seenAt)
	assert.Empty(t, e.reported(), "the turn runs")
	started := logged(t, e.logs, "turn started")
	require.Len(t, started, 1)
	assert.Equal(t, "INFO", started[0]["level"])
	assert.Equal(t, turn.ID, started[0]["turn"])
	pid, ok := started[0]["pid"].(float64)
	require.True(t, ok, "turn started names no process")
	assert.Equal(t, s.PID, int(pid))
}

// A turn whose process cannot be started, its binary missing, is a launch
// that failed: Start returns the error, so that the coordinator tries the
// turn again, and counts it unstarted. No end is ever reported for it, and
// the output file and the directories made for it go. So is a turn whose id
// cannot name its output file in the output directory.
func TestALaunchThatFailsReturnsItsError(t *testing.T) {
	for _, c := range []struct {
		name    string
		missing bool   // the turn's binary is missing
		id      string // the turn's id, when it is not a new one
		err     error  // what the error wraps, when it says
	}{
		{name: "its binary is missing", missing: true, err: fs.ErrNotExist},
		{name: "its id names a file outside the output directory", id: "../escape"},
	} {
		t.Run(c.name, func(t *testing.T) {
			missing := filepath.Join(t.TempDir(), "agentswarm")
			// The runner makes a turn's directories under TMPDIR.
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			e := newEnv(t, func(_ *process.Ports, o *process.Options) {
				if c.missing {
					o.Binary = missing
				}
			})
			turn := newTurn(t, &script{Exit: outcome.ExitEnded}, time.Now().Add(time.Minute))
			if c.id != "" {
				turn.ID = c.id
			}

			err := e.r.Start(e.ctx(t), turn)

			require.Error(t, err)
			if c.err != nil {
				require.ErrorIs(t, err, c.err)
			}
			assert.Never(t, func() bool { return len(e.reported()) > 0 }, 300*time.Millisecond, 10*time.Millisecond, "an end was reported")
			assert.Empty(t, logged(t, e.logs, "turn started"))
			assert.Empty(t, names(t, e.out))
			assert.Empty(t, names(t, tmp), "the turn's directories outlived its launch")
			assert.NoFileExists(t, filepath.Join(e.out, "..", "escape.out"))
		})
	}
}

// An output directory the runner cannot write, as with a wrong permission
// or a full disk, fails the turn's launch (Review Focus 4): Start returns
// the error, so that the turn is tried again and counts as unstarted, and no
// end is reported for it, so that no agent can be removed for it.
func TestAnOutputFileThatCannotBeCreatedFailsTheLaunch(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	e := newEnv(t)
	require.NoError(t, os.Chmod(e.out, 0o500))
	t.Cleanup(func() { _ = os.Chmod(e.out, 0o700) })
	seenAt := filepath.Join(t.TempDir(), "seen.json")

	err := e.r.Start(e.ctx(t), newTurn(t, &script{Record: seenAt, Exit: outcome.ExitEnded}, time.Now().Add(time.Minute)))

	require.ErrorIs(t, err, fs.ErrPermission)
	assert.Never(t, func() bool { return exists(seenAt) || len(e.reported()) > 0 }, 300*time.Millisecond, 10*time.Millisecond,
		"the turn was launched, or an end reported")
	assert.Empty(t, logged(t, e.logs, "turn started"))
}

// The runner writes the turn's spec (Contract C6) on its process's standard
// input: the turn's names, the gateways' URLs with the session's tokens, the
// model, the agent's skills at the item's commit, each its own entry, the
// prompt, the round's deadline plus the runtime's slack, in RFC 3339 with
// nanoseconds, and the deployment's maxTokens. The skills are read after
// Start returns, on a context of the holding that Start's cancellation does
// not end, bounded by the round's deadline. The tokens are nowhere else: not
// in the process's arguments or environment, its output file, or the
// runner's log.
func TestTheSpecReachesTheTurnOnItsStandardInput(t *testing.T) {
	startCtx, cancel := context.WithCancel(holding.WithEpoch(t.Context(), epoch))
	type read struct {
		commit, agent string
		epoch         int64
		deadline      time.Time
		live          bool
	}
	reads := make(chan read, 1)
	e := newEnv(t, func(p *process.Ports, _ *process.Options) {
		p.Skills = func(ctx context.Context, commit, agent string) ([]string, error) {
			select {
			case <-startCtx.Done():
			case <-time.After(5 * time.Second):
			}
			got, _ := holding.EpochFrom(ctx)
			deadline, _ := ctx.Deadline()
			reads <- read{commit: commit, agent: agent, epoch: got, deadline: deadline, live: ctx.Err() == nil}
			return slices.Clone(skills), nil
		}
	})
	seenAt := filepath.Join(t.TempDir(), "seen.json")
	deadline := time.Now().Add(time.Minute)
	turn := newTurn(t, &script{Record: seenAt, Stderr: "agentswarm: the turn's own words\n", Exit: outcome.ExitEnded}, deadline)
	turn.Agent, turn.Round = "beta", 3

	require.NoError(t, e.r.Start(startCtx, turn))
	cancel()

	s := awaitSeen(t, seenAt)
	want, err := json.Marshal(map[string]any{
		"turn": turn.ID, "run": "run-1", "agent": "beta", "round": 3,
		"mcp":   map[string]any{"url": "http://127.0.0.1:53001/", "token": turn.MCP.Reveal()},
		"llm":   map[string]any{"url": "http://127.0.0.1:53002", "token": turn.LLM.Reveal()},
		"model": "claude-sonnet-5-5", "system": skills, "prompt": turn.Prompt,
		"deadline": deadline.Add(2 * time.Second).Format(time.RFC3339Nano), "maxTokens": 4096,
	})
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(s.Spec))
	select {
	case r := <-reads:
		assert.Equal(t, [2]string{turn.Commit, "beta"}, [2]string{r.commit, r.agent})
		assert.Equal(t, int64(epoch), r.epoch, "the skills are read for the holding")
		assert.WithinDuration(t, deadline, r.deadline, 0, "the skills' read is bounded by the round's deadline")
		assert.True(t, r.live, "Start's cancellation ended the skills' read")
	default:
		t.Error("the skills were not read")
	}
	assert.Equal(t, []string{binary(t), "turn"}, s.Args)
	assert.Equal(t, item.TurnEnded, e.await(t, 1)[0].Class)
	out := output(t, e.out, turn.ID)
	assert.Equal(t, "agentswarm: the turn's own words\n", out)
	for _, token := range []string{turn.MCP.Reveal(), turn.LLM.Reveal()} {
		for where, text := range map[string]string{
			"the arguments": strings.Join(s.Args, " "), "the environment": strings.Join(s.Env, " "),
			"the output file": out, "the runner's log": e.logs.String(),
		} {
			assert.NotContains(t, text, token, where)
		}
	}
}

// A turn whose spec the runner cannot deliver is the platform's failure, not
// the turn's: its process is ended at once, and it is reported unavailable,
// never crashed, so that no agent is removed for it (Decision 12). The skills
// may not be read, as when the item's commit is missing; the spec may be
// larger than the runtime reads; or the pipe may fail, as when the process
// closes its standard input unread. The failure is logged.
func TestASpecThatCannotBeDeliveredEndsTheTurnUnavailable(t *testing.T) {
	for _, c := range []struct {
		name   string
		skills []string
		err    error
		binary string // the turn's binary, when it is not the fake turn
		reason string // in the log's line
	}{
		{name: "the skills cannot be read", err: errors.New("the commit is missing"), reason: "the commit is missing"},
		{name: "the spec is larger than the runtime reads", skills: []string{strings.Repeat("s", 1<<20)}, reason: "1 MiB"},
		{
			name: "the pipe fails", skills: []string{strings.Repeat("s", 512<<10)}, reason: "broken pipe",
			binary: "#!/bin/sh\n# It closes its standard input unread, and sleeps.\nexec 0<&-\nexec sleep 30\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, func(p *process.Ports, o *process.Options) {
				p.Skills = func(context.Context, string, string) ([]string, error) { return c.skills, c.err }
				if c.binary != "" {
					o.Binary = filepath.Join(t.TempDir(), "turn.sh")
					require.NoError(t, os.WriteFile(o.Binary, []byte(c.binary), 0o700))
				}
			})
			turn := newTurn(t, &script{Sleep: 30 * time.Second, Exit: outcome.ExitEnded}, time.Now().Add(time.Minute))
			require.NoError(t, e.r.Start(e.ctx(t), turn))
			started := logged(t, e.logs, "turn started")
			require.Len(t, started, 1)
			pid, ok := started[0]["pid"].(float64)
			require.True(t, ok, "turn started names no process")

			x := e.await(t, 1)[0]

			assert.Equal(t, item.TurnUnavailable, x.Class)
			assert.Eventually(t, func() bool { return gone(-int(pid)) }, 5*time.Second, 5*time.Millisecond, "the turn's process group is left")
			lines := logged(t, e.logs, "a turn's spec could not be delivered")
			require.Len(t, lines, 1)
			assert.Equal(t, turn.ID, lines[0]["turn"])
			assert.Contains(t, lines[0]["err"], c.reason)
		})
	}
}

// A spec the runner could not deliver is logged whatever the turn's class
// turns out to be: skills whose read lasts until the round's deadline leave
// the turn interrupted at its deadline, and the log says why.
func TestASpecNotDeliveredIsLoggedWhateverTheTurnsClass(t *testing.T) {
	e := newEnv(t, func(p *process.Ports, _ *process.Options) {
		p.Skills = func(ctx context.Context, _, _ string) ([]string, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})
	turn := newTurn(t, &script{Exit: outcome.ExitEnded}, time.Now().Add(500*time.Millisecond))
	require.NoError(t, e.r.Start(e.ctx(t), turn))

	assert.Equal(t, item.TurnInterrupted, e.await(t, 1)[0].Class)

	lines := logged(t, e.logs, "a turn's spec could not be delivered")
	require.Len(t, lines, 1)
	assert.Equal(t, "WARN", lines[0]["level"])
	assert.Equal(t, turn.ID, lines[0]["turn"])
	assert.Contains(t, lines[0]["err"], "read the skills of alpha")
}

// A Start repeated with a turn's id starts nothing: the turn runs once, and
// its end is reported once. A Start after launches that failed is no repeat:
// it starts the turn.
func TestARepeatedStartStartsNothing(t *testing.T) {
	e := newEnv(t)
	turn := newTurn(t, &script{Sleep: 200 * time.Millisecond, Exit: outcome.ExitEnded}, time.Now().Add(time.Minute))
	e.held.valid.Store(false)
	require.Error(t, e.r.Start(e.ctx(t), turn), "the holding refuses")
	e.held.valid.Store(true)
	away := e.out + ".away"
	require.NoError(t, os.Rename(e.out, away))
	require.Error(t, e.r.Start(e.ctx(t), turn), "the output directory is gone")
	require.NoError(t, os.Rename(away, e.out))

	require.NoError(t, e.r.Start(e.ctx(t), turn), "the Start after launches that failed")
	require.NoError(t, e.r.Start(e.ctx(t), turn), "a Start repeated while the turn runs")
	assert.Equal(t, item.TurnEnded, e.await(t, 1)[0].Class)
	require.NoError(t, e.r.Start(e.ctx(t), turn), "a Start repeated after the turn's end")

	assert.Never(t, func() bool { return len(e.reported()) > 1 }, 500*time.Millisecond, 10*time.Millisecond, "the turn ran again")
	assert.Len(t, logged(t, e.logs, "turn started"), 1)
}

// Before its deadline, a turn's exit code gives its class, by Decision 13's
// table (Contract C7); any other code, and a signal the runner did not send,
// is a crash.
func TestExitCodesGiveTheClassBeforeTheDeadline(t *testing.T) {
	for _, c := range []struct {
		name string
		s    script
		want item.TurnClass
	}{
		{name: "10: ended", s: script{Exit: outcome.ExitEnded}, want: item.TurnEnded},
		{name: "11: finished", s: script{Exit: outcome.ExitFinished}, want: item.TurnFinished},
		{name: "12: refused", s: script{Exit: outcome.ExitRefused}, want: item.TurnRefused},
		{name: "13: vendor", s: script{Exit: outcome.ExitVendor}, want: item.TurnVendor},
		{name: "14: interrupted", s: script{Exit: outcome.ExitInterrupted}, want: item.TurnInterrupted},
		{name: "15: unavailable", s: script{Exit: outcome.ExitUnavailable}, want: item.TurnUnavailable},
		{name: "3: crashed", s: script{Exit: 3}, want: item.TurnCrashed},
		{name: "1, a spec refused or a crash: crashed", s: script{Exit: 1}, want: item.TurnCrashed},
		{name: "0: crashed", s: script{Exit: 0}, want: item.TurnCrashed},
		{name: "killed by a signal: crashed", s: script{Signal: true, Sleep: 5 * time.Second}, want: item.TurnCrashed},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			turn := newTurn(t, &c.s, time.Now().Add(time.Minute))
			require.NoError(t, e.r.Start(e.ctx(t), turn))

			x := e.await(t, 1)[0]

			assert.Equal(t, app.Exit{Run: turn.Run, Agent: turn.Agent, Round: turn.Round, Turn: turn.ID, Class: c.want}, x.Exit)
		})
	}
}

// A turn whose process ends at or after its deadline was interrupted,
// whatever its exit code would have said: the runner kills it at its
// deadline, and reports it once its deadline passed. A turn started after
// its deadline is killed at once.
func TestAProcessEndingAtOrAfterItsDeadlineIsInterrupted(t *testing.T) {
	for _, c := range []struct {
		name  string
		s     script
		after time.Duration // the deadline, from the turn's start
	}{
		{name: "a turn that would end by end_turn after it", s: script{Sleep: 5 * time.Second, Exit: outcome.ExitEnded}, after: 500 * time.Millisecond},
		{name: "a turn that would crash after it", s: script{Sleep: 5 * time.Second, Exit: 3}, after: 500 * time.Millisecond},
		{name: "a turn deaf to its signals and its input's end", s: script{Deaf: true, Sleep: 5 * time.Second, Exit: outcome.ExitEnded}, after: 500 * time.Millisecond},
		{name: "a turn started after its deadline", s: script{Sleep: 5 * time.Second, Exit: outcome.ExitEnded}, after: -time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			began := time.Now()
			deadline := began.Add(c.after)
			require.NoError(t, e.r.Start(e.ctx(t), newTurn(t, &c.s, deadline)))

			x := e.await(t, 1)[0]

			assert.Equal(t, item.TurnInterrupted, x.Class)
			assert.False(t, x.at.Before(deadline), "reported before its deadline")
			assert.Less(t, x.at.Sub(began), 3*time.Second, "killed at its deadline, not at the end of its sleep")
		})
	}
}

// A turn whose own end the board holds ended, whatever its process says
// (Decision 12): when the process ends, the runner asks the board, on a
// context of its own with a bound, unless the turn ended by end_turn. A board
// that cannot answer leaves the exit code's class, and is logged.
func TestATurnEndOnTheBoardMeansEnded(t *testing.T) {
	for _, c := range []struct {
		name  string
		s     script
		after time.Duration // the deadline, from the turn's start
		ended bool          // the board's answer
		err   error
		want  item.TurnClass
		asks  bool
	}{
		{name: "a crash whose end the board holds", s: script{Exit: 3}, after: time.Minute, ended: true, want: item.TurnEnded, asks: true},
		{
			name: "a turn killed at its deadline whose end the board holds", s: script{Sleep: 5 * time.Second, Exit: 3},
			after: 500 * time.Millisecond, ended: true, want: item.TurnEnded, asks: true,
		},
		{name: "a crash whose end the board does not hold", s: script{Exit: 3}, after: time.Minute, want: item.TurnCrashed, asks: true},
		{
			name: "a crash whose end the board cannot tell", s: script{Exit: 3}, after: time.Minute,
			err: errors.New("the board is down"), want: item.TurnCrashed, asks: true,
		},
		{name: "a turn ended by end_turn", s: script{Exit: outcome.ExitEnded}, after: time.Minute, want: item.TurnEnded},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			type asked struct {
				run     board.RunID
				round   int
				agent   string
				bounded bool
			}
			var mu sync.Mutex
			var got []asked
			e := newEnv(t, func(p *process.Ports, _ *process.Options) {
				p.Ended = func(ctx context.Context, run board.RunID, round int, agent string) (bool, error) {
					_, bounded := ctx.Deadline()
					mu.Lock()
					defer mu.Unlock()
					got = append(got, asked{run: run, round: round, agent: agent, bounded: bounded})
					return c.ended, c.err
				}
			})
			turn := newTurn(t, &c.s, time.Now().Add(c.after))
			turn.Round = 4
			require.NoError(t, e.r.Start(e.ctx(t), turn))

			x := e.await(t, 1)[0]

			assert.Equal(t, c.want, x.Class)
			var want []asked
			if c.asks {
				want = []asked{{run: turn.Run, round: 4, agent: turn.Agent, bounded: true}}
			}
			mu.Lock()
			assert.Equal(t, want, got)
			mu.Unlock()
			unread := logged(t, e.logs, "the board could not tell whether a turn ended")
			if c.err == nil {
				assert.Empty(t, unread)
				return
			}
			require.Len(t, unread, 1)
			assert.Equal(t, turn.ID, unread[0]["turn"])
			assert.Equal(t, "the board is down", unread[0]["err"])
		})
	}
}

// A turn's end is reported once, and never before its Start returns: the
// coordinator calls Start under the lock that Exited takes (app.Turns). A
// turn that ends while its Start is still logging it is reported only after
// Start returns, and stopping it after its end reports nothing more. A later
// turn of the same agent in the same run, with an id of its own, does not
// confuse an earlier turn's report: each carries its own turn, round and
// class, once, however their reports interleave.
func TestExitedIsReportedOnceAndNeverBeforeStartReturns(t *testing.T) {
	gate := make(chan struct{})
	e := newEnv(t, func(p *process.Ports, _ *process.Options) {
		p.Ended = func(ctx context.Context, _ board.RunID, round int, _ string) (bool, error) {
			if round == 2 {
				select {
				case <-gate:
				case <-ctx.Done():
				}
			}
			return false, nil
		}
	})
	var released atomic.Pointer[time.Time]
	hold := func(r slog.Record) {
		if r.Message != "turn started" {
			return
		}
		var pid int
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "pid" {
				pid = int(a.Value.Int64())
			}
			return true
		})
		// The turn ends while its Start still logs it: its process is
		// reaped, and the runner has had the time to report it, if it would.
		for end := time.Now().Add(5 * time.Second); !gone(pid) && time.Now().Before(end); {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		now := time.Now()
		released.Store(&now)
	}
	e.hold.Store(&hold)
	first := newTurn(t, &script{Exit: outcome.ExitEnded}, time.Now().Add(time.Minute))

	require.NoError(t, e.r.Start(e.ctx(t), first))
	e.hold.Store(nil)

	x := e.await(t, 1)[0]
	assert.Equal(t, app.Exit{Run: first.Run, Agent: first.Agent, Round: 1, Turn: first.ID, Class: item.TurnEnded}, x.Exit)
	require.NotNil(t, released.Load(), "the turn ended while its Start logged it")
	assert.True(t, x.at.After(*released.Load()), "the turn's end was reported before its Start returned")
	e.r.Stop(first.Run, first.Agent)
	e.r.StopAll()
	assert.Never(t, func() bool { return len(e.reported()) > 1 }, 300*time.Millisecond, 10*time.Millisecond, "the end was reported again")

	// The turn of round 2 is stopped, and its report held at the board while
	// the turn of round 3 starts and ends.
	seenAt := filepath.Join(t.TempDir(), "seen.json")
	second := newTurn(t, &script{Record: seenAt, Sleep: 30 * time.Second, Exit: outcome.ExitEnded}, time.Now().Add(time.Minute))
	second.Round = 2
	require.NoError(t, e.r.Start(e.ctx(t), second))
	awaitSeen(t, seenAt)
	e.r.Stop(second.Run, second.Agent)
	third := newTurn(t, &script{Exit: outcome.ExitFinished}, time.Now().Add(time.Minute))
	third.Round = 3
	require.NoError(t, e.r.Start(e.ctx(t), third))
	assert.Equal(t, app.Exit{Run: third.Run, Agent: third.Agent, Round: 3, Turn: third.ID, Class: item.TurnFinished}, e.await(t, 2)[1].Exit)
	close(gate)
	assert.Equal(t, app.Exit{Run: second.Run, Agent: second.Agent, Round: 2, Turn: second.ID, Class: item.TurnInterrupted}, e.await(t, 3)[2].Exit)
	assert.Never(t, func() bool { return len(e.reported()) > 3 }, 300*time.Millisecond, 10*time.Millisecond, "an end was reported again")
}

// At a turn's deadline, the runner kills the turn's process group, by
// itself: a turn deaf to SIGTERM and to its input's end, which would run on
// long past its deadline, is gone at its deadline, as is the process it
// started; and it is reported interrupted, once its deadline passed.
func TestTheDeadlineKillsTheProcessGroup(t *testing.T) {
	e := newEnv(t)
	seenAt := filepath.Join(t.TempDir(), "seen.json")
	deadline := time.Now().Add(2 * time.Second)
	require.NoError(t, e.r.Start(e.ctx(t), newTurn(t, &script{Record: seenAt, Deaf: true, Child: true, Sleep: 30 * time.Second}, deadline)))
	s := awaitSeen(t, seenAt)
	require.NotZero(t, s.Child)
	require.False(t, gone(-s.PID), "the turn's process group runs before its deadline")

	x := e.await(t, 1)[0]

	assert.Equal(t, item.TurnInterrupted, x.Class)
	assert.False(t, x.at.Before(deadline), "reported before its deadline")
	assert.WithinDuration(t, deadline, x.at, 2*time.Second, "killed at its deadline")
	assert.Eventually(t, func() bool { return gone(-s.PID) }, 5*time.Second, 5*time.Millisecond, "the turn's process group is left")
	assert.True(t, gone(s.Child), "the process the turn started is left")
}

// A turn that started a process of its own, which ignores its parent's end,
// takes it with it when it is killed (Review Focus 3): at its deadline, by
// Stop, and by StopAll, each reported interrupted. A turn that ends on its
// own leaves nothing running either: the runner kills what is left in its
// process group.
func TestAKilledTurnTakesItsChildrenWithIt(t *testing.T) {
	for _, c := range []struct {
		name  string
		sleep time.Duration // how long the turn runs before it ends on its own
		after time.Duration // the deadline, from the turn's start
		kill  func(*process.Runner, *app.Turn)
		want  item.TurnClass
	}{
		{name: "at its deadline", sleep: 30 * time.Second, after: 2 * time.Second, kill: func(*process.Runner, *app.Turn) {}, want: item.TurnInterrupted},
		{
			name: "by Stop", sleep: 30 * time.Second, after: time.Minute,
			kill: func(r *process.Runner, turn *app.Turn) { r.Stop(turn.Run, turn.Agent) }, want: item.TurnInterrupted,
		},
		{name: "by StopAll", sleep: 30 * time.Second, after: time.Minute, kill: func(r *process.Runner, _ *app.Turn) { r.StopAll() }, want: item.TurnInterrupted},
		// It runs on for 2 s after its record, so that its own process is
		// seen running before it ends, even on a busy host.
		{name: "when it ends on its own", sleep: 2 * time.Second, after: time.Minute, kill: func(*process.Runner, *app.Turn) {}, want: item.TurnEnded},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			seenAt := filepath.Join(t.TempDir(), "seen.json")
			turn := newTurn(t, &script{Record: seenAt, Deaf: true, Child: true, Sleep: c.sleep, Exit: outcome.ExitEnded}, time.Now().Add(c.after))
			require.NoError(t, e.r.Start(e.ctx(t), turn))
			s := awaitSeen(t, seenAt)
			require.NotZero(t, s.Child)
			require.False(t, gone(s.Child), "the turn's own process runs")

			c.kill(e.r, turn)

			assert.Equal(t, c.want, e.await(t, 1)[0].Class)
			assert.Eventually(t, func() bool { return gone(s.Child) && gone(s.PID) }, 5*time.Second, 5*time.Millisecond,
				"the turn's own process outlived it")
		})
	}
}

// A process that left the turn's process group, as one that made a session of
// its own, is out of the runner's reach, as it is not of a sandbox's (Plan
// 7d-2), and may hold the turn's output open: the runner reads no more of it
// a second after the turn's process ended, and reports the turn's end.
func TestATurnsEndIsReportedThoughAProcessThatLeftItsGroupHoldsItsOutput(t *testing.T) {
	e := newEnv(t)
	seenAt := filepath.Join(t.TempDir(), "seen.json")
	turn := newTurn(t, &script{Record: seenAt, Child: true, Detach: true, Exit: outcome.ExitEnded}, time.Now().Add(time.Minute))
	require.NoError(t, e.r.Start(e.ctx(t), turn))
	s := awaitSeen(t, seenAt)
	require.NotZero(t, s.Child)
	t.Cleanup(func() { _ = syscall.Kill(s.Child, syscall.SIGKILL) })

	x := e.await(t, 1)[0]

	assert.Equal(t, item.TurnEnded, x.Class)
	assert.False(t, gone(s.Child), "the process left the turn's group, out of the runner's reach")
}

// Stop and StopAll send SIGKILL to the turns' process groups and return,
// without waiting for a turn to end: the coordinator calls them under the
// lock that Exited takes. Each returns within 50 ms while turns run, and
// after StopAll no turn's process group is left. A stopped turn is reported
// interrupted.
func TestStopAndStopAllNeverWait(t *testing.T) {
	var coordinator sync.Mutex
	e := newEnv(t, func(p *process.Ports, _ *process.Options) {
		exited := p.Exited
		p.Exited = func(x app.Exit) {
			coordinator.Lock()
			defer coordinator.Unlock()
			exited(x)
		}
	})
	var pids []int
	for _, agent := range []string{"alpha", "beta", "gamma"} {
		seenAt := filepath.Join(t.TempDir(), agent+".json")
		turn := newTurn(t, &script{Record: seenAt, Deaf: true, Sleep: 30 * time.Second}, time.Now().Add(time.Minute))
		turn.Agent = agent
		require.NoError(t, e.r.Start(e.ctx(t), turn))
		pids = append(pids, awaitSeen(t, seenAt).PID)
	}

	coordinator.Lock()
	stopped := returnsWithin(50*time.Millisecond, func() { e.r.Stop("run-1", "alpha") })
	all := returnsWithin(50*time.Millisecond, e.r.StopAll)
	coordinator.Unlock()

	assert.True(t, stopped, "Stop waited")
	assert.True(t, all, "StopAll waited")
	for _, pid := range pids {
		assert.Eventually(t, func() bool { return gone(-pid) }, 5*time.Second, 5*time.Millisecond, "the process group %d is left", pid)
	}
	for _, x := range e.await(t, 3) {
		assert.Equal(t, item.TurnInterrupted, x.Class, x.Agent)
	}
}

// A turn's standard output and standard error go to its output file, named by
// the turn's id, which keeps the first OutputCap bytes: the runner drains the
// rest and drops it, so that a chatty turn neither blocks nor fails for it. A
// turn that prints 3 MiB and ends by end_turn leaves a 1 MiB file, and is
// ended.
func TestATurnsOutputIsCappedAndTheTurnNeverFailsForIt(t *testing.T) {
	e := newEnv(t)
	line := "agentswarm: a line on standard error\n"
	turn := newTurn(t, &script{Stderr: line, Print: 3 << 20, Exit: outcome.ExitEnded}, time.Now().Add(time.Minute))
	require.NoError(t, e.r.Start(e.ctx(t), turn))

	assert.Equal(t, item.TurnEnded, e.await(t, 1)[0].Class)

	info, err := os.Stat(filepath.Join(e.out, turn.ID+".out"))
	require.NoError(t, err)
	assert.Equal(t, int64(1<<20), info.Size())
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "only the runner's user reads a turn's output")
	out := output(t, e.out, turn.ID)
	assert.True(t, strings.HasPrefix(out, line), "standard error shares the file")
	assert.Empty(t, strings.Trim(out[len(line):], "x"), "the file keeps the turn's first bytes")
}

// A turn's process gets an environment of its own: PATH, and a home and a
// temporary directory made for the turn, in a working directory made for it,
// and nothing else of the runner's environment, a proxy's or another's
// (Decision 12). Those directories are removed once the process ends; its
// output file stays.
func TestTheTurnsEnvironmentIsMinimal(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	t.Setenv("AGENTSWARM_PROBE", "the runner's own")
	e := newEnv(t)
	seenAt := filepath.Join(t.TempDir(), "seen.json")
	turn := newTurn(t, &script{Record: seenAt, Sleep: 30 * time.Second}, time.Now().Add(time.Minute))
	require.NoError(t, e.r.Start(e.ctx(t), turn))
	s := awaitSeen(t, seenAt)

	env := map[string]string{}
	for _, kv := range s.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	assert.Equal(t, []string{"HOME", "PATH", "TMPDIR"}, slices.Sorted(maps.Keys(env)), "nothing else of the runner's environment")
	assert.Equal(t, os.Getenv("PATH"), env["PATH"])
	cwd, err := os.Getwd()
	require.NoError(t, err)
	assert.NotEqual(t, os.Getenv("HOME"), env["HOME"])
	assert.NotEqual(t, filepath.Clean(os.TempDir()), filepath.Clean(env["TMPDIR"]))
	assert.NotEqual(t, cwd, s.Dir)
	made := []string{env["HOME"], env["TMPDIR"], s.Dir}
	for _, dir := range made {
		assert.True(t, filepath.IsAbs(dir), dir)
		assert.DirExists(t, dir)
	}

	e.r.Stop(turn.Run, turn.Agent)
	e.await(t, 1)

	for _, dir := range made {
		assert.NoDirExists(t, dir, "removed once the turn's process ended")
	}
	assert.FileExists(t, filepath.Join(e.out, turn.ID+".out"), "the output file stays")
}

// A turn's standard input stays open for the turn's life: the runtime ends
// its turn at its input's end, as when the runner dies, so the runner never
// closes it while the turn runs (Decision 12). The fake, which exits 14 at
// its input's end as the runtime does, sees none until it is stopped.
func TestATurnsStandardInputStaysOpenForItsLife(t *testing.T) {
	e := newEnv(t)
	seenAt := filepath.Join(t.TempDir(), "seen.json")
	turn := newTurn(t, &script{Record: seenAt, Sleep: 30 * time.Second, Exit: outcome.ExitEnded}, time.Now().Add(time.Minute))
	require.NoError(t, e.r.Start(e.ctx(t), turn))
	s := awaitSeen(t, seenAt)

	assert.Never(t, func() bool { return exists(seenAt+".eof") || len(e.reported()) > 0 }, time.Second, 10*time.Millisecond,
		"the turn's standard input ended while it ran")
	e.r.Stop(turn.Run, turn.Agent)

	assert.Equal(t, item.TurnInterrupted, e.await(t, 1)[0].Class)
	assert.NoFileExists(t, seenAt+".eof")
	assert.True(t, gone(s.PID))
}

// The runner needs every port but its log, and options it can run turns
// with: an output directory, a cap above zero, a slack not below zero, the
// gateways' HTTP URLs, and maxTokens of at least 1.
func TestNewNeedsItsPortsAndOptions(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*process.Ports, *process.Options)
	}{
		{name: "no holding", edit: func(p *process.Ports, _ *process.Options) { p.Holding = nil }},
		{name: "no skills", edit: func(p *process.Ports, _ *process.Options) { p.Skills = nil }},
		{name: "no board", edit: func(p *process.Ports, _ *process.Options) { p.Ended = nil }},
		{name: "no Exited", edit: func(p *process.Ports, _ *process.Options) { p.Exited = nil }},
		{name: "no output directory", edit: func(_ *process.Ports, o *process.Options) { o.Output = "" }},
		{name: "no output cap", edit: func(_ *process.Ports, o *process.Options) { o.OutputCap = 0 }},
		{name: "a slack below zero", edit: func(_ *process.Ports, o *process.Options) { o.Slack = -time.Second }},
		{name: "no MCP gateway", edit: func(_ *process.Ports, o *process.Options) { o.MCP = "" }},
		{name: "an LLM gateway that is not HTTP", edit: func(_ *process.Ports, o *process.Options) { o.LLM = "unix:///run/llm.sock" }},
		{name: "no maxTokens", edit: func(_ *process.Ports, o *process.Options) { o.MaxTokens = 0 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := &env{logs: &syncBuffer{}, held: &holdingFake{}}
			p, o := e.ports(), options(t, t.TempDir())
			c.edit(&p, &o)

			_, err := process.New(p, o)

			require.Error(t, err)
		})
	}
	e := &env{logs: &syncBuffer{}, held: &holdingFake{}}
	p, o := e.ports(), options(t, t.TempDir())
	p.Log, o.Binary = nil, ""
	_, err := process.New(p, o)
	require.NoError(t, err, "without a log, and with serve's own executable")
}
