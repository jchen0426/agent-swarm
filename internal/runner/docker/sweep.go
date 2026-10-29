package docker

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The minute's job and the sweep (Decision 5; R7), fixed in code.
const (
	// passEvery is the period of the minute's job, whose pass is the sweep,
	// then the image check.
	passEvery = time.Minute
	// passWait bounds each engine call of a minute's pass: the sweep's
	// listing, each inspect, kill and removal, and each image's check. The
	// start sweep's calls are bounded by openWait.
	passWait = 10 * time.Second
	// closeWait is how long Close waits for a pass in flight once it has
	// cancelled it.
	closeWait = time.Second
	// When the sweep takes an untracked container of its own holder: a
	// stopped one, a minute after its end; a never-started one, 10 s after
	// its creation; a running one, a minute after its deadline plus the
	// runtime's slack.
	stoppedDue      = time.Minute
	neverStartedDue = 10 * time.Second
	runningDue      = time.Minute
	// When it takes another holder's: 5 minutes after its deadline; or, with
	// no deadline label it can read, an hour after its creation.
	othersDue     = 5 * time.Minute
	unlabelledDue = time.Hour
)

// startJob starts the minute's job (Decision 5; R7): a pass every minute,
// from Open's return until Close, on a context that carries ctx's values
// and that only stop cancels. stop cancels the job, and waits for a pass in
// flight until it ends or closeWait has passed, whichever comes first: a
// pass whose engine call does not let go at its context's end is waited for
// no longer.
func (r *Runner) startJob(ctx context.Context) (stop func()) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(passEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if ctx.Err() == nil {
					r.pass(ctx)
				}
			}
		}
	}()
	return func() {
		cancel()
		wait := time.NewTimer(closeWait)
		defer wait.Stop()
		select {
		case <-done:
		case <-wait.C:
		}
	}
}

// pass is a minute's pass (Decision 5; R7): the sweep, then the image check
// (checkImages), each engine call bounded by passWait. A sweep cut short, by
// a listing that failed or by the engine's silence, is logged, and so is an
// image check the engine's silence cut short; the engine's silence ends the
// pass. A pass that Close cancels ends there, and logs nothing.
func (r *Runner) pass(ctx context.Context) {
	err := r.sweep(ctx, false, passWait)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		r.log.WarnContext(ctx, "the sweep did not finish", "err", err)
		if errors.Is(err, ErrUnreachable) {
			return
		}
	}
	if err := r.checkImages(ctx); err != nil && ctx.Err() == nil {
		r.log.WarnContext(ctx, "the image check did not finish", "err", err)
	}
}

// sweep lists the containers labelled with the runner's tenant, in every
// state, and takes each untracked one that is due (Decision 5). A container
// whose agentswarm.turn names a turn the runner has is tracked, and never
// touched. The sweep inspects every other one and, once it is due (due),
// kills it if it runs and removes it (take), by its id from the listing,
// never by its name, since a retry reuses a turn's name. atStart says the
// runner tracks nothing yet. Each engine call is bounded by wait. A listing
// that fails, or a call that finds the engine silent, ends the sweep with
// its error; a container's other failures leave it to the next sweep
// (unswept).
func (r *Runner) sweep(ctx context.Context, atStart bool, wait time.Duration) error {
	lctx, cancel := context.WithTimeout(ctx, wait)
	listed, err := r.e.List(lctx, labelTenant+"="+r.o.Tenant)
	cancel()
	if err != nil {
		return fmt.Errorf("docker: the sweep could not list the tenant's containers: %w", err)
	}
	for _, l := range listed {
		if r.tracked(l.Labels[labelTurn]) {
			continue
		}
		if err := r.take(ctx, l.ID, atStart, wait); err != nil {
			return fmt.Errorf("docker: the sweep stopped at the container %s: %w", l.ID, err)
		}
	}
	return nil
}

// tracked reports whether the turn id is one the runner has, whose
// containers the sweep never touches.
func (r *Runner) tracked(turn string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.turns[turn]
	return ok
}

// take inspects the untracked container id and, once it is due, kills it
// if it runs, then removes it, with its volumes, by its id, each call
// bounded by wait, and logs the removal. A kill that fails, but for the
// engine's silence or a container gone, leaves the removal, which is
// forced, to kill it. A failure is logged as the call it was: a container
// could not be inspected, or could not be removed (unswept). take returns
// an error only for the engine's silence.
func (r *Runner) take(ctx context.Context, id string, atStart bool, wait time.Duration) error {
	ictx, cancelInspect := context.WithTimeout(ctx, wait)
	c, err := r.e.Inspect(ictx, id)
	cancelInspect()
	if err != nil {
		return r.unswept(ctx, "a container could not be inspected", id, err)
	}
	if !r.due(&c, atStart, time.Now()) {
		return nil
	}
	if c.Running {
		kctx, cancelKill := context.WithTimeout(ctx, wait)
		err = r.e.Kill(kctx, id)
		cancelKill()
		if errors.Is(err, ErrNoContainer) || errors.Is(err, ErrUnreachable) {
			return r.unswept(ctx, "a container could not be removed", id, err)
		}
	}
	rctx, cancelRemove := context.WithTimeout(ctx, wait)
	err = r.e.Remove(rctx, id)
	cancelRemove()
	if err != nil {
		return r.unswept(ctx, "a container could not be removed", id, err)
	}
	r.log.InfoContext(ctx, "the sweep removed a container", "container", id, "holder", c.Labels[labelHolder])
	return nil
}

// unswept is what a failure of the sweep's call on the container id does: a
// container gone meanwhile needs nothing more; the engine's silence ends the
// sweep, which returns err; any other failure, the engine's conflict over a
// removal already in progress (an ordinary error of the port) included, is
// logged msg, at Warn, which names the call that failed, and leaves the
// container to the next sweep, which takes it once it is due, and never
// refuses Open.
func (r *Runner) unswept(ctx context.Context, msg, id string, err error) error {
	switch {
	case errors.Is(err, ErrNoContainer):
		return nil
	case errors.Is(err, ErrUnreachable):
		return err
	}
	r.log.WarnContext(ctx, msg, "container", id, "err", err)
	return nil
}

// due reports whether the sweep takes the untracked container c at now
// (Decision 5). Another holder's is due once its agentswarm.deadline is over
// 5 minutes past, so that a live turn of another instance, or a clock step
// of a few minutes, is never touched; or, with no deadline label it can
// read, an hour after its creation. Of its own holder's, every one is due at
// start, being its previous process's; later, a stopped one is due over a
// minute after its end; a never-started one (a zero Finished), over 10 s
// after its creation; a running one, over a minute after its deadline plus
// the runtime's slack, or, with no deadline label it can read, an hour after
// its creation, as another holder's.
func (r *Runner) due(c *Container, atStart bool, now time.Time) bool {
	deadline, err := time.Parse(time.RFC3339Nano, c.Labels[labelDeadline])
	labelled, own := err == nil, c.Labels[labelHolder] == r.o.Holder
	switch {
	case !own && labelled:
		return now.Sub(deadline) > othersDue
	case !own:
		return now.Sub(c.Created) > unlabelledDue
	case atStart:
		return true
	case c.Running && labelled:
		return now.Sub(deadline) > r.o.Slack+runningDue
	case c.Running:
		return now.Sub(c.Created) > unlabelledDue
	case c.Finished.IsZero():
		return now.Sub(c.Created) > neverStartedDue
	}
	return now.Sub(c.Finished) > stoppedDue
}
