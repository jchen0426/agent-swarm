// Package process runs agents' turns as host processes, for development and
// tests only (Plan 7d-1, Decision 12). Each turn is the runtime, agentswarm
// turn, run from serve's own executable or the deployment's (R5): one process
// per turn, in a process group of its own, with an environment of PATH and a
// home and a temporary directory made for the turn, in a working directory
// made for it. Its spec (Contract C6) comes on its standard input, which stays
// open for the turn's life, so that the turn ends when the runner's process
// does. Its standard output and standard error go to one file,
// <Output>/<turn id>.out. A turn's process runs with serve's own rights, so
// serve allows the runner only with unsafeNoSandbox; Plan 7d-2 runs turns in
// sandboxes instead.
package process

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/outcome"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

const (
	// maxSpec caps a turn's spec: the runtime reads no more (Contract C6).
	maxSpec = 1 << 20
	// endedWait bounds the board's answer to whether a turn ended, which the
	// runner asks once the turn's process has ended.
	endedWait = 5 * time.Second
	// drainWait bounds the wait for a turn's output once its process group
	// is killed: a process that left the group may hold the pipe open.
	drainWait = time.Second
	// defaultPath is the turns' PATH when the runner's own has none.
	defaultPath = "/usr/bin:/bin"
	// shown is what a session's token prints as.
	shown = "[session token]"
)

// classes gives the class of each exit code of agentswarm turn (Decision 13;
// Contract C7). Any other code is a crash.
var classes = map[int]item.TurnClass{
	outcome.ExitEnded:       item.TurnEnded,
	outcome.ExitFinished:    item.TurnFinished,
	outcome.ExitRefused:     item.TurnRefused,
	outcome.ExitVendor:      item.TurnVendor,
	outcome.ExitInterrupted: item.TurnInterrupted,
	outcome.ExitUnavailable: item.TurnUnavailable,
}

// errUnheld fails the start of a turn the holding refused: the holding the
// start acts for is over. It is a launch that failed, and never fences.
var errUnheld = errors.New("process: the holding of the platform's lease does not hold, so the turn does not start")

// Options set how the runner runs turns.
type Options struct {
	Binary    string        // the executable run as `<Binary> turn`; "" means os.Executable()
	Output    string        // the directory of the turns' output files
	OutputCap int64         // [1 MiB]
	Slack     time.Duration // [2 s], added to a turn's deadline in its spec
	MCP, LLM  string        // the gateways' URLs, as a turn reaches them
	MaxTokens int64         // the deployment's runner.maxTokens [4,096]
}

// Ports are what the runner reads, and reports turns' ends through.
type Ports struct {
	Holding holding.Holding
	// Skills reads an agent's skills at a commit, off the coordinator's lock.
	// It must return once ctx ends: the runner waits for it, without a bound
	// of its own, before it reports the turn's end.
	Skills func(ctx context.Context, commit, agent string) ([]string, error)
	// Ended reports whether the board holds the agent's own turn end in the round.
	Ended func(ctx context.Context, run board.RunID, round int, agent string) (bool, error)
	// Exited is the coordinator's Exited, bound once the coordinator exists.
	Exited func(app.Exit)
	Log    *slog.Logger
}

// Runner runs agents' turns as host processes: it is the coordinator's
// app.Turns, for development and tests.
type Runner struct {
	p      Ports
	o      Options
	log    *slog.Logger
	binary string // the runtime's executable, as an absolute path
	path   string // the turns' PATH

	mu sync.Mutex
	// launched holds the id of each turn the runner launched: a Start
	// repeated with one starts nothing.
	launched map[string]bool
	// live holds the turns whose process has not been reaped, by id.
	live map[string]*proc
}

var _ app.Turns = (*Runner)(nil)

// proc is a turn's process, from its launch to the report of its end.
type proc struct {
	turn app.Turn
	cmd  *exec.Cmd
	pid  int
	// dir holds the turn's home, temporary and working directories.
	dir string
	// stdin is the runner's end of the turn's standard input, which stays
	// open for the turn's life; output is the runner's end of the turn's
	// output, which drain copies to the turn's file.
	stdin, output *os.File
	// cancel ends the spec's delivery.
	cancel context.CancelFunc
	// timer kills the turn at its deadline.
	timer *time.Timer
	// started closes once Start returns: the turn's end is reported after.
	started chan struct{}
	// delivered closes once the spec's delivery is over; specErr says why
	// it failed, if it did.
	delivered chan struct{}
	specErr   error
	// drained closes once the turn's output is drained.
	drained chan struct{}

	// stopped says the runner killed the turn: at its deadline, by Stop or
	// by StopAll. exited says its process was reaped: its group is no
	// longer its to kill. Runner.mu guards both.
	stopped, exited bool
}

// New returns a runner. It needs every port but Log.
//
//nolint:gocritic // hugeParam: the contract passes the options by value.
func New(p Ports, o Options) (*Runner, error) {
	if p.Holding == nil || p.Skills == nil || p.Ended == nil || p.Exited == nil {
		return nil, errors.New("process: the runner needs the holding, the skills, the board's turn ends and Exited")
	}
	if err := o.check(); err != nil {
		return nil, err
	}
	binary := o.Binary
	if binary == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("process: find serve's own executable: %w", err)
		}
		binary = self
	}
	// A turn runs in a working directory of its own, against which a
	// relative path would be read.
	binary, err := filepath.Abs(binary)
	if err != nil {
		return nil, fmt.Errorf("process: find the runtime's executable: %w", err)
	}
	log := p.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	path := os.Getenv("PATH")
	if path == "" {
		path = defaultPath
	}
	return &Runner{p: p, o: o, log: log, binary: binary, path: path, launched: map[string]bool{}, live: map[string]*proc{}}, nil
}

// check refuses options the runner cannot run turns with.
func (o *Options) check() error {
	switch {
	case o.Output == "":
		return errors.New("process: the runner needs a directory for the turns' output")
	case o.OutputCap < 1:
		return errors.New("process: the turns' output cap must be above zero")
	case o.Slack < 0:
		return errors.New("process: the runtime's slack cannot be below zero")
	case o.MaxTokens < 1:
		return errors.New("process: maxTokens must be at least 1")
	case !isHTTP(o.MCP):
		return fmt.Errorf("process: the MCP gateway's URL %q is not an HTTP URL", o.MCP)
	case !isHTTP(o.LLM):
		return fmt.Errorf("process: the LLM gateway's URL %q is not an HTTP URL", o.LLM)
	}
	return nil
}

// isHTTP reports whether s is an HTTP URL with a host, as the runtime takes
// a gateway's.
func isHTTP(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// Start launches a turn's process and returns, without waiting for the turn
// (app.Turns). It first asks Holding.Valid for the epoch ctx carries
// (Decision 4). A launch that fails, the holding's refusal included, returns
// its error: the coordinator tries the turn again, and it counts as
// unstarted. A Start repeated with the id of a turn launched starts nothing.
// Once the process runs, a goroutine of the turn's reads its skills and
// writes its spec, off the coordinator's lock, and on a context that ctx's
// cancellation does not end.
func (r *Runner) Start(ctx context.Context, t *app.Turn) error {
	if err := r.holds(ctx, t); err != nil {
		return err
	}
	if t.ID == "" || filepath.Base(t.ID) != t.ID {
		return fmt.Errorf("process: a turn's id names its output file, which %q cannot", t.ID)
	}
	r.mu.Lock()
	if r.launched[t.ID] {
		r.mu.Unlock()
		return nil
	}
	p, err := r.launch(ctx, t)
	if err == nil {
		r.launched[t.ID] = true
		r.live[t.ID] = p
	}
	r.mu.Unlock()
	if err != nil {
		return err
	}
	r.log.InfoContext(ctx, "turn started", "turn", t.ID, "pid", p.pid)
	close(p.started)
	return nil
}

// holds asks the holding, before a turn starts, whether the holding whose
// epoch ctx carries still holds (Decision 4). A refusal is logged, and never
// fences. A context without an epoch is a bug, refused and logged as one.
func (r *Runner) holds(ctx context.Context, t *app.Turn) error {
	epoch, err := holding.EpochFrom(ctx)
	if err != nil {
		r.log.ErrorContext(ctx, "turn without an epoch", "turn", t.ID)
		return err
	}
	if !r.p.Holding.Valid(epoch) {
		r.log.WarnContext(ctx, "holding refused", "epoch", epoch, "request", "turn")
		return errUnheld
	}
	return nil
}

// launch makes the turn's output file and directories, and starts its
// process, in a process group of its own, with the pipes of its standard
// input and its output held by the runner; then the goroutines that deliver
// its spec, drain its output and reap it, and the timer that kills it at its
// deadline. What it made goes again if it fails. The caller holds r.mu.
func (r *Runner) launch(ctx context.Context, t *app.Turn) (p *proc, err error) {
	var undo []func()
	defer func() {
		if err != nil {
			for _, f := range slices.Backward(undo) {
				f()
			}
		}
	}()
	name := filepath.Join(r.o.Output, t.ID+".out")
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G304: the output directory's file of a turn, named by its id.
	if err != nil {
		return nil, fmt.Errorf("process: create the turn's output file: %w", err)
	}
	undo = append(undo, func() { _, _ = file.Close(), os.Remove(name) })
	dir, err := makeDirs()
	if err != nil {
		return nil, err
	}
	undo = append(undo, func() { _ = os.RemoveAll(dir) })
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("process: make the turn's standard input: %w", err)
	}
	undo = append(undo, func() { _, _ = inR.Close(), inW.Close() })
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("process: make the turn's output pipe: %w", err)
	}
	undo = append(undo, func() { _, _ = outR.Close(), outW.Close() })
	cmd := exec.Command(r.binary, "turn") //nolint:gosec // G204: the runtime, serve's own executable or the deployment's (R5).
	cmd.Dir = filepath.Join(dir, "work")
	cmd.Env = []string{"PATH=" + r.path, "HOME=" + filepath.Join(dir, "home"), "TMPDIR=" + filepath.Join(dir, "tmp")}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, outW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("process: start the turn's process: %w", err)
	}
	// The process holds its own ends of the pipes now.
	_, _ = inR.Close(), outW.Close()

	// The turn's goroutines carry ctx's values, its epoch among them, but
	// not its cancellation: the turn outlives its Start.
	base := context.WithoutCancel(ctx)
	deliverCtx, cancel := context.WithDeadline(base, t.Deadline)
	p = &proc{
		turn: *t, cmd: cmd, pid: cmd.Process.Pid, dir: dir, stdin: inW, output: outR, cancel: cancel,
		started: make(chan struct{}), delivered: make(chan struct{}), drained: make(chan struct{}),
	}
	p.timer = time.AfterFunc(time.Until(t.Deadline), func() { r.kill(p) })
	go r.deliver(deliverCtx, p)
	go r.drain(base, p, file)
	go r.reap(base, p)
	return p, nil
}

// makeDirs makes a turn's directories, its home, its temporary directory and
// its working directory, under one made for the turn, which it returns.
func makeDirs() (string, error) {
	dir, err := os.MkdirTemp("", "agentswarm-turn-")
	if err != nil {
		return "", fmt.Errorf("process: make the turn's directories: %w", err)
	}
	for _, sub := range []string{"home", "tmp", "work"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("process: make the turn's directories: %w", err)
		}
	}
	return dir, nil
}

// Stop ends at once the turns the agent runs in the run: SIGKILL to each
// turn's process group. It never waits: each turn's end is reported once its
// process has ended.
func (r *Runner) Stop(run board.RunID, agent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.live {
		if p.turn.Run == run && p.turn.Agent == agent {
			r.stop(p)
		}
	}
}

// StopAll ends at once every turn the runner runs, as Stop does. Serve's
// exit at once calls it.
func (r *Runner) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.live {
		r.stop(p)
	}
}

// kill ends a turn at its deadline.
func (r *Runner) kill(p *proc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stop(p)
}

// stop ends a turn's spec's delivery, and sends SIGKILL to its process
// group, which takes the processes the turn started with it. It never
// waits. The caller holds r.mu.
func (r *Runner) stop(p *proc) {
	p.cancel()
	if p.exited {
		return
	}
	p.stopped = true
	_ = syscall.Kill(-p.pid, syscall.SIGKILL)
}

// deliver reads the turn's skills at its item's commit, and writes the
// turn's spec (Contract C6) on its standard input, which stays open after. A
// spec it cannot deliver ends the turn at once: its process group is killed,
// and the turn is reported unavailable (Decision 12), unless its deadline, a
// stop or the board says otherwise (see class).
func (r *Runner) deliver(ctx context.Context, p *proc) {
	defer close(p.delivered)
	if p.specErr = r.writeSpec(ctx, p); p.specErr == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !p.exited {
		_ = syscall.Kill(-p.pid, syscall.SIGKILL)
	}
}

// writeSpec writes the turn's spec on its standard input, whole. The write
// ends with ctx: at the turn's deadline, when the turn is stopped, or when
// its process ends.
func (r *Runner) writeSpec(ctx context.Context, p *proc) error {
	t := &p.turn
	skills, err := r.p.Skills(ctx, t.Commit, t.Agent)
	if err != nil {
		return fmt.Errorf("read the skills of %s at %s: %w", t.Agent, t.Commit, err)
	}
	body, err := json.Marshal(r.specOf(t, skills))
	if err != nil {
		return fmt.Errorf("encode the turn's spec: %w", err)
	}
	if len(body) > maxSpec {
		return fmt.Errorf("the turn's spec is %d bytes, more than the runtime reads, 1 MiB", len(body))
	}
	stop := context.AfterFunc(ctx, func() { _ = p.stdin.SetWriteDeadline(time.Now()) })
	defer stop()
	if _, err := p.stdin.Write(body); err != nil {
		return fmt.Errorf("write the turn's spec: %w", err)
	}
	return nil
}

// spec is a turn's spec (Contract C6). Nothing in it is written anywhere
// else.
type spec struct {
	Turn  string  `json:"turn"`
	Run   string  `json:"run"`
	Agent string  `json:"agent"`
	Round int     `json:"round"`
	MCP   gateway `json:"mcp"`
	LLM   gateway `json:"llm"`
	Model string  `json:"model"`
	// System holds the agent's skills, each sent as its own system block.
	System []string `json:"system"`
	Prompt string   `json:"prompt"`
	// Deadline is the round's deadline, in the process's clock, plus the
	// runtime's slack.
	Deadline  time.Time `json:"deadline"`
	MaxTokens int64     `json:"maxTokens"`
}

// gateway is where a turn reaches a gateway, and its session's token there.
type gateway struct {
	URL   string `json:"url"`
	Token token  `json:"token"`
}

// token is a session's token, which only the spec's JSON holds: it prints
// as a placeholder, so that no error or log can hold it.
type token string

func (token) String() string { return shown }

func (token) GoString() string { return shown }

// LogValue logs the token as its placeholder.
func (token) LogValue() slog.Value { return slog.StringValue(shown) }

// specOf returns a turn's spec, with its agent's skills. Its deadline is the
// round's plus the runtime's slack: the runtime's own deadline is a backstop,
// which the runner's kill at the round's deadline comes before.
func (r *Runner) specOf(t *app.Turn, skills []string) *spec {
	if skills == nil {
		skills = []string{}
	}
	return &spec{
		Turn: t.ID, Run: string(t.Run), Agent: t.Agent, Round: t.Round,
		MCP:   gateway{URL: r.o.MCP, Token: token(t.MCP.Reveal())},
		LLM:   gateway{URL: r.o.LLM, Token: token(t.LLM.Reveal())},
		Model: t.Model, System: skills, Prompt: t.Prompt,
		Deadline: t.Deadline.Add(r.o.Slack), MaxTokens: r.o.MaxTokens,
	}
}

// drain copies the turn's output into its file, up to the cap, and drops
// the rest, so that the turn neither blocks on its output nor fails for it.
func (r *Runner) drain(ctx context.Context, p *proc, file *os.File) {
	defer close(p.drained)
	w := &capped{file: file, left: r.o.OutputCap}
	_, _ = io.Copy(w, p.output)
	_ = p.output.Close()
	if err := errors.Join(w.err, file.Close()); err != nil {
		r.log.WarnContext(ctx, "a turn's output could not be written", "turn", p.turn.ID, "err", err)
	}
}

// capped writes to a file up to left bytes, and drops what follows, as it
// drops all that follows a write that failed: it never fails its writer.
type capped struct {
	file *os.File
	left int64
	err  error
}

func (c *capped) Write(b []byte) (int, error) {
	if c.err == nil && c.left > 0 {
		n := min(int64(len(b)), c.left)
		_, c.err = c.file.Write(b[:n])
		c.left -= n
	}
	return len(b), nil
}

// reap waits for the turn's process to end, then ends what it left: the
// processes still in its group, its spec's delivery, its output's drain and
// its directories. It then reports the turn's end, once, after its Start
// returned.
func (r *Runner) reap(ctx context.Context, p *proc) {
	_ = p.cmd.Wait() // the process's state says how it ended
	at := time.Now()
	r.mu.Lock()
	p.exited = true
	delete(r.live, p.turn.ID)
	// The processes the turn started go with it. A group outlives its leader
	// while a process is in it, so its id names no other group yet. With none
	// left, the kill finds no group: the id could name another only once the
	// system gave it out again, and process ids are given out cyclically, so
	// not within the moments since wait4 returned. The same holds for a kill
	// of stop or deliver that lands between wait4 and this lock.
	_ = syscall.Kill(-p.pid, syscall.SIGKILL)
	stopped := p.stopped
	r.mu.Unlock()
	p.timer.Stop()
	p.cancel()
	_ = p.stdin.Close()
	<-p.delivered
	select {
	case <-p.drained:
	case <-time.After(drainWait):
		// A process that left the turn's group holds its output's pipe:
		// the runner reads no more of it.
		_ = p.output.Close()
		<-p.drained
	}
	if err := os.RemoveAll(p.dir); err != nil {
		r.log.WarnContext(ctx, "a turn's directories could not be removed", "turn", p.turn.ID, "err", err)
	}
	class := r.class(ctx, p, at, stopped)
	<-p.started
	r.log.InfoContext(ctx, "turn exited", "turn", p.turn.ID, "pid", p.pid, "status", p.cmd.ProcessState.String(), "class", class)
	r.p.Exited(app.Exit{Run: p.turn.Run, Agent: p.turn.Agent, Round: p.turn.Round, Turn: p.turn.ID, Class: class})
}

// class says how the turn ended (Decision 12), which is advisory: a turn
// whose own end the board holds ended, whatever its process says; any other
// ended as its process says (byProcess). The board is asked on a context of
// the runner's, with a bound; when it cannot answer, the process's class
// stands.
func (r *Runner) class(ctx context.Context, p *proc, at time.Time, stopped bool) item.TurnClass {
	class := r.byProcess(ctx, p, at, stopped)
	if class == item.TurnEnded {
		return class
	}
	ctx, cancel := context.WithTimeout(ctx, endedWait)
	defer cancel()
	ended, err := r.p.Ended(ctx, p.turn.Run, p.turn.Round, p.turn.Agent)
	switch {
	case err != nil:
		r.log.WarnContext(ctx, "the board could not tell whether a turn ended", "turn", p.turn.ID, "err", err)
	case ended:
		return item.TurnEnded
	}
	return class
}

// byProcess says how the turn's process ended: at or after its deadline, or
// killed by the runner, it was interrupted; with its spec not delivered, it
// is unavailable, whatever its exit code says; any other ended as its exit
// code says (Contract C7), any code outside the table, or a signal the runner
// did not send, being a crash. A spec not delivered is logged whatever the
// class, so that a turn interrupted at its deadline shows why.
func (r *Runner) byProcess(ctx context.Context, p *proc, at time.Time, stopped bool) item.TurnClass {
	if p.specErr != nil {
		r.log.WarnContext(ctx, "a turn's spec could not be delivered", "turn", p.turn.ID, "err", p.specErr)
	}
	state := p.cmd.ProcessState
	killed := false
	if state != nil {
		status, ok := state.Sys().(syscall.WaitStatus)
		killed = ok && status.Signaled() && status.Signal() == syscall.SIGKILL
	}
	switch {
	case !at.Before(p.turn.Deadline), stopped && killed:
		return item.TurnInterrupted
	case p.specErr != nil:
		return item.TurnUnavailable
	}
	if class, ok := classes[state.ExitCode()]; ok {
		return class
	}
	return item.TurnCrashed
}
