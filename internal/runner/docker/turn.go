package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"syscall"
	"time"

	"github.com/jchen0426/agent-swarm/internal/outcome"
	"github.com/jchen0426/agent-swarm/internal/runner/output"
	"github.com/jchen0426/agent-swarm/internal/runner/spec"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

const (
	// endWait bounds each engine call of a running turn: a kill, an
	// inspect of its end, its container's removal.
	endWait = 10 * time.Second
	// endedWait bounds the board's answer to whether a turn ended, which the
	// runner asks once the turn's container has ended.
	endedWait = 5 * time.Second
	// drainWait bounds the wait for a turn's output once its container has
	// ended, after which the runner closes the attachment.
	drainWait = time.Second
	// retryWait is the first pause before a failed wait's inspect, or wait,
	// is tried again, and retryWaitMax the longest.
	retryWait    = 100 * time.Millisecond
	retryWaitMax = 2 * time.Second
	// exitKilled is a container's exit code once SIGKILL ended its process:
	// 128 and the signal's number.
	exitKilled = 128 + int(syscall.SIGKILL)
)

// state is where a registered turn stands (Decision 5).
type state int

const (
	// launching: queued for a slot, or in steps 1 to 5 of its launch.
	launching state = iota
	// started: step 6, its container's start, is in flight.
	started
	// running: from step 7 on, whether or not its Launched was reported.
	running
)

// turn is a turn the runner has, from its Start until the runner forgets
// it. Its state, its timer, its launch's cancel and its attachment live here
// under the runner's lock; the runner calls no engine holding it.
type turn struct {
	t     app.Turn
	began time.Time // when its Start was called
	file  *output.File
	// base carries Start's context's values, its epoch among them, without
	// its cancellation: the turn outlives its Start, and a fence or a drain
	// does not cut its goroutines.
	base context.Context
	// cancel ends the launch's wait for a slot and its steps 1 to 5.
	cancel context.CancelFunc
	// timer stops the turn at its deadline.
	timer *time.Timer
	// launched closes once the turn's Launched has returned: the report of
	// its end waits for it.
	launched chan struct{}

	// Runner.mu guards the fields from here to the next comment.
	state state
	// stopped says a stop came: the deadline, Stop, StopAll or KillAll.
	// stoppedAt is when the first did.
	stopped   bool
	stoppedAt time.Time
	// reported says the turn's Launched was reported (R25).
	reported bool
	// ended says the container's end is known: no stop kills it any more.
	ended bool
	// container is the container's id, once created, and att its
	// attachment, once attached.
	container string
	att       Attachment
	// stopDelivery ends the delivery of the turn's spec, once it runs.
	stopDelivery context.CancelFunc

	// delivered closes once the spec's delivery is over, specErr saying why
	// it failed, if it did; drained, once the turn's output is copied.
	delivered, drained chan struct{}
	specErr            error
}

// run moves a started turn to running (Decision 5, step 7), and returns the
// context its spec's delivery runs on, which ends at the turn's deadline, at
// a stop, or at the turn's end. The caller holds Runner.mu.
func (tr *turn) run() context.Context {
	ctx, cancel := context.WithDeadline(tr.base, tr.t.Deadline)
	tr.state, tr.stopDelivery = running, cancel
	tr.delivered, tr.drained = make(chan struct{}), make(chan struct{})
	return ctx
}

// halt ends what a stop ends of a turn but its container (Decision 5): a
// launching turn's launch is cancelled; a started turn is marked stopped,
// which its launch's step 7 reads, to kill it; a running turn's spec's
// delivery is ended. It reports whether the turn's container runs, for its
// caller to kill. The caller holds Runner.mu.
func (tr *turn) halt() (runs bool) {
	if !tr.stopped {
		tr.stopped, tr.stoppedAt = true, time.Now()
	}
	switch tr.state {
	case launching:
		tr.cancel()
	case running:
		tr.stopDelivery()
		return !tr.ended
	}
	return false
}

// kill sends a container SIGKILL, by its id or its name, on a context of
// its own bounded by endWait. A kill that fails changes nothing: the
// container ends at its runtime's own deadline, at a later stop's kill, or
// at the kill by its name that its wait sends once the turn's deadline plus
// the slack has come and the container still runs (waitEnd).
func (r *Runner) kill(ctx context.Context, idOrName string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), endWait)
	defer cancel()
	_ = r.e.Kill(ctx, idOrName)
}

// deliver reads the turn's skills at its item's commit, and writes the
// turn's spec (Contract C6) of version on its container's standard input,
// which stays open after, for the turn's life. The delivery ends with ctx:
// at the turn's deadline, at a stop, or at the turn's end. A spec it cannot
// deliver ends the turn: its container is killed, and the turn is
// unavailable (Decision 5), unless the board, its deadline or a stop says
// otherwise (byContainer).
func (r *Runner) deliver(ctx context.Context, tr *turn, version int) {
	defer close(tr.delivered)
	if tr.specErr = r.writeSpec(ctx, tr, version); tr.specErr == nil {
		return
	}
	r.mu.Lock()
	ended := tr.ended
	r.mu.Unlock()
	if !ended {
		r.kill(ctx, tr.container)
	}
}

// writeSpec writes the turn's spec, whole, on its container's attachment.
func (r *Runner) writeSpec(ctx context.Context, tr *turn, version int) error {
	t := &tr.t
	skills, err := r.p.Skills(ctx, t.Commit, t.Agent)
	if err != nil {
		return fmt.Errorf("read the skills of %s at %s: %w", t.Agent, t.Commit, err)
	}
	body, err := json.Marshal(r.specOf(t, version, skills))
	if err != nil {
		return fmt.Errorf("encode the turn's spec: %w", err)
	}
	if len(body) > spec.MaxSize {
		return fmt.Errorf("the turn's spec is %d bytes, more than the runtime reads, 1 MiB", len(body))
	}
	if _, err := tr.att.Write(body); err != nil {
		return fmt.Errorf("write the turn's spec: %w", err)
	}
	return nil
}

// specOf returns a turn's spec (Contract C6) of version, the newest that the
// image reads and the runner writes (Decision 9): each gateway's URL, with
// the session's token and the gateway's socket in the sandbox, and the
// certificate the runtime trusts alone (Decision 2); the agent's skills; and
// the round's deadline plus the runtime's slack, a backstop that the
// runner's kill at the round's deadline comes before.
func (r *Runner) specOf(t *app.Turn, version int, skills []string) *spec.Spec {
	if skills == nil {
		skills = []string{}
	}
	return &spec.Spec{
		V:    version,
		Turn: t.ID, Run: string(t.Run), Agent: t.Agent, Round: t.Round,
		MCP:   spec.Gateway{URL: MCPURL, Token: spec.Token(t.MCP.Reveal()), Socket: path.Join(SocketsDir, MCPSocket)},
		LLM:   spec.Gateway{URL: LLMURL, Token: spec.Token(t.LLM.Reveal()), Socket: path.Join(SocketsDir, LLMSocket)},
		Cert:  r.o.Cert,
		Model: t.Model, System: skills, Prompt: t.Prompt,
		Deadline: t.Deadline.Add(r.o.Slack), MaxTokens: r.o.MaxTokens,
	}
}

// copyOutput copies the container's output, its standard output and
// standard error, into the turn's file, up to its cap, and drops the rest
// (output.File), so that the turn neither blocks on its output nor fails for
// it.
func (r *Runner) copyOutput(tr *turn) {
	defer close(tr.drained)
	_, _ = io.Copy(tr.file, tr.att.Output())
	if err := tr.file.Close(); err != nil {
		r.log.WarnContext(tr.base, "a turn's output could not be written", "turn", tr.t.ID, "err", err)
	}
}

// await waits for the turn's container to end (waitEnd), then ends what the
// turn left: its spec's delivery, the copy of its output, which it waits for
// up to drainWait, and its attachment. It reports the turn's end once,
// after its Launched, and logs it turn exited (Contract C3; R23); forgets the
// turn; and then removes its container, by its id, with its volumes, when
// the engine showed its end (Decision 5). A removal that fails is left to
// the sweep.
func (r *Runner) await(tr *turn) {
	ctx := tr.base
	c, known := r.waitEnd(ctx, tr)
	r.mu.Lock()
	tr.ended = true
	stopped := tr.stopped
	r.mu.Unlock()
	tr.stopDelivery()
	select {
	case <-tr.drained:
	case <-time.After(drainWait):
	}
	_ = tr.att.Close()
	<-tr.drained
	<-tr.delivered
	class := r.class(ctx, tr, &c, known, stopped)
	<-tr.launched
	exit := -1
	if known {
		exit = c.ExitCode
	}
	r.log.InfoContext(ctx, "turn exited", "turn", tr.t.ID, "container", tr.container, "class", class, "exit", exit, "oom", c.OOMKilled)
	r.p.Exited(app.Exit{Run: tr.t.Run, Agent: tr.t.Agent, Round: tr.t.Round, Turn: tr.t.ID, Class: class})
	r.forget(tr)
	if known {
		r.remove(ctx, tr.container, endWait)
	}
}

// waitEnd waits for the turn's container to end, up to the turn's deadline
// plus the runtime's slack, and returns its state, and whether the engine
// showed it (Decision 5). Once the wait returns, as the container ended,
// the wait failed, as when the engine restarts, or its bound came, the
// container is inspected: one that has ended gives its state; one that still
// runs, as under the engine's live-restore, or after a deadline's kill the
// engine never got, is waited for again up to the same limit, with a backoff
// between waits the engine refuses at once, then killed by its name, and
// inspected once more; one that is gone, or that the engine does not show by
// then, gives none, and is left to the sweep. So no container the runner
// tracks outlives that limit for want of a kill, and a silent engine holds a
// turn past it by one inspect's bound at most.
func (r *Runner) waitEnd(ctx context.Context, tr *turn) (Container, bool) {
	id, limit := tr.container, tr.t.Deadline.Add(r.o.Slack)
	first, cancel := context.WithDeadline(ctx, limit)
	_ = r.e.Wait(first, id)
	cancel()
	killed, backoff := false, retryWait
	for {
		c, err := r.inspectEnd(ctx, id, limit)
		switch {
		case err == nil && !c.Running:
			return c, true
		case err != nil, killed:
			return Container{}, false
		}
		until := limit
		if !time.Now().Before(limit) {
			r.kill(ctx, containerName(tr.t.ID))
			killed, until = true, time.Now().Add(endWait)
		}
		wctx, cancel := context.WithDeadline(ctx, until)
		err = r.e.Wait(wctx, id)
		cancel()
		if left := time.Until(until); err != nil && left > 0 {
			time.Sleep(min(backoff, left))
			backoff = min(2*backoff, retryWaitMax)
		}
	}
}

// inspectEnd inspects a container for its end. While the engine does not
// answer, it tries again, with a backoff, until limit, and then gives up
// (Decision 5); it always tries once. Each try is bounded by endWait.
func (r *Runner) inspectEnd(ctx context.Context, id string, limit time.Time) (Container, error) {
	backoff := retryWait
	for {
		ictx, cancel := context.WithTimeout(ctx, endWait)
		c, err := r.e.Inspect(ictx, id)
		cancel()
		left := time.Until(limit)
		if !errors.Is(err, ErrUnreachable) || left <= 0 {
			return c, err
		}
		time.Sleep(min(backoff, left))
		backoff = min(2*backoff, retryWaitMax)
	}
}

// class says how a turn ended (Decision 5; Contract C7), which is advisory:
// a turn whose own end the board holds ended, whatever its container says;
// any other ended as its container says (byContainer). The board is asked on
// a context bounded by endedWait; when it cannot answer, the container's
// class stands. A spec not delivered is logged whatever the class, so that a
// turn interrupted at its deadline shows why.
func (r *Runner) class(ctx context.Context, tr *turn, c *Container, known, stopped bool) item.TurnClass {
	if tr.specErr != nil {
		r.log.WarnContext(ctx, "a turn's spec could not be delivered", "turn", tr.t.ID, "err", tr.specErr)
	}
	class := byContainer(&tr.t, c, known, stopped, tr.specErr != nil)
	if class == item.TurnEnded {
		return class
	}
	bctx, cancel := context.WithTimeout(ctx, endedWait)
	defer cancel()
	ended, err := r.p.Ended(bctx, tr.t.Run, tr.t.Round, tr.t.Agent)
	switch {
	case err != nil:
		r.log.WarnContext(ctx, "the board could not tell whether a turn ended", "turn", tr.t.ID, "err", err)
	case ended:
		return item.TurnEnded
	}
	return class
}

// byContainer says how a turn's container ended (Contract C7), judged by its
// FinishedAt, not by when the wait returned, first rule first: at or after
// the turn's deadline, or by the runner's own kill, it was interrupted; with
// its spec not delivered, it is unavailable; ended by the engine's OOM
// killer, it crashed; with an exit code of the table, it ended as the code
// says; ended by one of outcome.OutsideSignals, which the runner did not
// send, as the engine's stop or restart ends it, it is unavailable (Review
// Focus 1); anything else, a runtime's own SIGSEGV or SIGABRT included,
// crashed. A container whose end the engine did not show, gone or not
// answered for, is unavailable.
func byContainer(t *app.Turn, c *Container, known, stopped, undelivered bool) item.TurnClass {
	switch {
	case !known:
		return item.TurnUnavailable
	case !c.Finished.Before(t.Deadline), stopped && c.ExitCode == exitKilled:
		return item.TurnInterrupted
	case undelivered:
		return item.TurnUnavailable
	case c.OOMKilled:
		return item.TurnCrashed
	}
	if class, ok := output.Class(c.ExitCode); ok {
		return class
	}
	if c.ExitCode > 128 && slices.Contains(outcome.OutsideSignals, syscall.Signal(c.ExitCode-128)) {
		return item.TurnUnavailable
	}
	return item.TurnCrashed
}
