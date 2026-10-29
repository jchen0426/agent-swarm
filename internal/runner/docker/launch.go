package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/app"
)

const (
	// launchSlots is how many launches take their steps at once, fixed in
	// code (Decision 5): review 1 measured twelve at once at 2.57 s.
	launchSlots = 8
	// launchWait bounds a launch from when it gets its slot, fixed in code
	// (Decision 5), and the removal of what a launch that failed made.
	launchWait = 10 * time.Second
)

// launch takes a turn's launch, off the coordinator's lock (Decision 5). It
// waits for a slot until the turn's deadline or a stop, which cancel steps,
// as a fence or a drain does through Start's context. From when it gets
// one, it has launchWait for its steps: 1 to 5 on steps (prepare), and 6 to
// 8 on a context that a fence does not cancel, within what remains (start).
// It reports how the launch ended through Launched, once.
func (r *Runner) launch(steps context.Context, tr *turn) {
	select {
	case r.slots <- struct{}{}:
	case <-steps.Done():
		r.failed(tr, r.stopReason(tr), fmt.Errorf("docker: the turn's launch was stopped while it waited for a slot: %w", context.Cause(steps)))
		return
	}
	release := sync.OnceFunc(func() { <-r.slots })
	defer release()
	bound := time.Now().Add(launchWait)
	ctx, cancel := context.WithDeadline(steps, bound)
	defer cancel()
	version, reason, err := r.prepare(ctx, tr)
	if err != nil {
		if steps.Err() != nil {
			reason = r.stopReason(tr)
		}
		release()
		r.failed(tr, reason, err)
		return
	}
	run, cancelRun := context.WithDeadline(tr.base, bound)
	defer cancelRun()
	r.start(run, tr, version, release)
}

// prepare takes a launch's steps 1 to 5 on ctx (Decision 5): it removes, by
// its name, a container an earlier attempt of the turn left; checks the
// turn's image (image), which gives the spec's version to write; creates the
// container, and attaches to its standard input and output, keeping each in
// the turn, so that a launch that fails removes what it made; and asks the
// holding again, as every sandbox's start needs. A failure says its reason;
// its caller reads one that a stop, a fence or a drain made.
func (r *Runner) prepare(ctx context.Context, tr *turn) (int, app.LaunchReason, error) {
	name, cfg, host := r.o.sandbox(&tr.t)
	if err := r.e.Remove(ctx, name); err != nil && !errors.Is(err, ErrNoContainer) {
		return 0, reasonOf(err, app.ReasonLaunch), fmt.Errorf("docker: remove the container an earlier attempt of the turn left: %w", err)
	}
	version, reason, err := r.image(ctx, tr.t.Image)
	if err != nil {
		return 0, reason, err
	}
	id, err := r.e.Create(ctx, name, cfg, host)
	if err != nil {
		return 0, reasonOf(err, app.ReasonLaunch), err
	}
	r.mu.Lock()
	tr.container = id
	r.mu.Unlock()
	att, err := r.e.Attach(ctx, id)
	if err != nil {
		return 0, reasonOf(err, app.ReasonLaunch), err
	}
	r.mu.Lock()
	tr.att = att
	r.mu.Unlock()
	if err := r.holds(ctx, &tr.t); err != nil {
		return 0, app.ReasonHolding, err
	}
	if ctx.Err() != nil {
		return 0, app.ReasonEngine, fmt.Errorf("docker: the turn's launch ran out its bound: %w", context.Cause(ctx))
	}
	return version, "", nil
}

// start takes a launch's steps 6 to 8 on ctx (Decision 5). It moves the turn
// to started, unless a stop came since step 5, and starts the container.
// Then, under the runner's lock, in one step, it checks whether the turn was
// stopped meanwhile and, if not, moves it to running, so that a stop after
// the check finds it running and kills it the usual way. A turn stopped
// meanwhile has its container killed, waited for and removed, and is
// reported cancelled. Any other is logged turn started; its goroutines
// deliver its spec, of version, copy its output and wait for its end; and it
// is reported Launched, once release has freed its slot.
func (r *Runner) start(ctx context.Context, tr *turn, version int, release func()) {
	r.mu.Lock()
	stopped := tr.stopped
	if !stopped {
		tr.state = started
	}
	r.mu.Unlock()
	if stopped {
		release()
		r.failed(tr, r.stopReason(tr), errors.New("docker: the turn was stopped before its container started"))
		return
	}
	if err := r.e.Start(ctx, tr.container); err != nil {
		release()
		r.failed(tr, r.startReason(tr, err), err)
		return
	}
	startedAt := time.Now()
	var deliver context.Context
	r.mu.Lock()
	if stopped = tr.stopped; !stopped {
		deliver = tr.run()
	}
	r.mu.Unlock()
	if stopped {
		// Their errors change nothing: failed's forced removal follows, and
		// logs its own failure.
		_ = r.e.Kill(ctx, tr.container)
		_ = r.e.Wait(ctx, tr.container)
		release()
		r.failed(tr, app.ReasonCancelled, errors.New("docker: the turn was stopped while its container started"))
		return
	}
	r.log.InfoContext(tr.base, "turn started", "turn", tr.t.ID, "container", tr.container, "launch", startedAt.Sub(tr.began).String())
	go r.deliver(deliver, tr, version)
	go r.copyOutput(tr)
	go r.await(tr)
	release()
	r.p.Launched(app.Launch{Run: tr.t.Run, Agent: tr.t.Agent, Round: tr.t.Round, Turn: tr.t.ID})
	r.mu.Lock()
	tr.reported = true
	r.mu.Unlock()
	close(tr.launched)
}

// image checks a turn's image at its launch (Decision 6), on the engine or
// reloaded from the store when the engine lost it, and returns the spec's
// version the launch writes (Decision 9). An image neither on the engine nor
// in the store, or whose copy does not come back, breaking the contract, or
// reading no version the runner writes, reads image; an engine that does not
// answer reads engine.
func (r *Runner) image(ctx context.Context, id string) (int, app.LaunchReason, error) {
	img, err := r.find(ctx, id)
	switch {
	case errors.Is(err, ErrUnreachable):
		return 0, app.ReasonEngine, err
	case errors.Is(err, ErrNoImage):
		return 0, app.ReasonImage, err
	case err != nil:
		return 0, app.ReasonLaunch, err
	}
	version, err := r.o.specVersion(&img, r.arch)
	if err != nil {
		return 0, app.ReasonImage, err
	}
	return version, "", nil
}

// find returns the image id from the engine or, when the engine lost it, as
// a prune does, from the store, which loads it and tags it as make image
// does (Decision 6; Review Focus 3). An image the store lacks too, or whose
// copy does not come back, fails wrapping ErrNoImage.
func (r *Runner) find(ctx context.Context, id string) (Image, error) {
	img, err := r.e.Image(ctx, id)
	if !errors.Is(err, ErrNoImage) {
		return img, err
	}
	if !r.store.has(id) {
		return Image{}, fmt.Errorf("docker: the image %s is neither on the engine nor in the store: %w", id, err)
	}
	img, reloadErr := r.store.reload(ctx, r.e, id)
	if reloadErr != nil {
		return Image{}, fmt.Errorf("docker: the engine lost the image %s, and its copy in the store did not come back: %w (%w)", id, reloadErr, err)
	}
	r.log.InfoContext(ctx, "an image was reloaded from the store", "image", id)
	return img, nil
}

// reasonOf is the reason of a launch whose engine call failed with err:
// engine when the engine did not answer, or the launch outlasted its bound;
// otherwise otherwise.
func reasonOf(err error, otherwise app.LaunchReason) app.LaunchReason {
	if errors.Is(err, ErrUnreachable) {
		return app.ReasonEngine
	}
	return otherwise
}

// startReason is the reason of a launch whose container's start failed with
// err: a stop's (stopReason), when the turn was stopped while its start was
// in flight, as when KillAll's removal reached the engine before the start
// did; otherwise the start's own (reasonOf).
func (r *Runner) startReason(tr *turn, err error) app.LaunchReason {
	r.mu.Lock()
	stopped := tr.stopped
	r.mu.Unlock()
	if stopped {
		return r.stopReason(tr)
	}
	return reasonOf(err, app.ReasonLaunch)
}

// stopReason is the reason of a launch that a stop, a fence or a drain
// ended: cancelled, which posts no notice. But a launch that a stop ended at
// or after its turn's deadline ends as the deadline ends it: engine while the
// engine's last call found it unreachable, so that its agent's board says
// why, and cancelled otherwise (plan review 2, F1). The runner's own timer
// and the coordinator's deadline stop a turn at the same deadline.
func (r *Runner) stopReason(tr *turn) app.LaunchReason {
	r.mu.Lock()
	stopped, at := tr.stopped, tr.stoppedAt
	r.mu.Unlock()
	if stopped && !at.Before(tr.t.Deadline) && r.e.unreachable.Load() {
		return app.ReasonEngine
	}
	return app.ReasonCancelled
}

// failed ends a launch that failed for reason (Decision 5). It removes what
// the launch made: its attachment; its container, by its id, on a fresh
// context bounded by launchWait, so that a cancelled launch still cleans up;
// and the turn's output file, which nothing wrote. It forgets the turn, and
// then reports Launched with the error and its reason, so that the
// coordinator's retry finds the turn's id free.
func (r *Runner) failed(tr *turn, reason app.LaunchReason, err error) {
	r.mu.Lock()
	id, att := tr.container, tr.att
	r.mu.Unlock()
	if att != nil {
		_ = att.Close()
	}
	if id != "" {
		r.remove(tr.base, id, launchWait)
	}
	_ = tr.file.Close()
	_ = os.Remove(tr.file.Name())
	r.forget(tr)
	r.log.WarnContext(tr.base, "a turn's launch failed", "turn", tr.t.ID, "reason", reason, "err", err)
	r.p.Launched(app.Launch{Run: tr.t.Run, Agent: tr.t.Agent, Round: tr.t.Round, Turn: tr.t.ID, Reason: reason, Err: err})
}

// remove removes a container, with its volumes, by its id, on a context of
// its own bounded by wait. A removal that fails, the engine's conflict over
// a removal already in progress included, is logged, and left to the sweep;
// a container already gone needs none.
func (r *Runner) remove(ctx context.Context, id string, wait time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if err := r.e.Remove(ctx, id); err != nil && !errors.Is(err, ErrNoContainer) {
		r.log.WarnContext(ctx, "a container could not be removed", "container", id, "err", err)
	}
}
