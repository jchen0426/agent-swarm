package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sync/atomic"

	"github.com/moby/moby/api/types/container"

	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// What Status.Engine says of the engine (Decision 7).
const (
	engineOK          = "ok"
	engineUnreachable = "unreachable"
)

// Status is what the runner says of what it runs on, which serve's
// /v1/status reports as its runner (Decision 7; Contract C14).
type Status struct {
	// Engine is "ok" or "unreachable", as the runner's record of the engine
	// (watched) holds it: a call the engine answered marks it ok; a call
	// that found no connection, or a broken one, or that ran out its own
	// bound (a launch's 10 s, openWait, KillAll's 1 s, a minute's pass's
	// passWait), marks it unreachable, but a wait whose own deadline came
	// first; a call its context's cancel ended (a stop, a fence, a drain,
	// Close) leaves it as it was (plan review 2, F3).
	Engine string
	// MissingImages lists, sorted, the ids of the images of runner.images
	// that the minute's last check found missing (R7): empty, never nil,
	// while none is.
	MissingImages []string
}

// Status returns the runner's status (Decision 7), from what the runner
// keeps: its record of the engine, and the images its minute's check found
// missing (checkImages). It calls no engine.
func (r *Runner) Status() Status {
	engine := engineOK
	if r.e.unreachable.Load() {
		engine = engineUnreachable
	}
	r.mu.Lock()
	missing := make([]string, len(r.missing))
	copy(missing, r.missing)
	r.mu.Unlock()
	return Status{Engine: engine, MissingImages: missing}
}

// checkImages is the minute's check of the images of Options.Images (R7;
// Decision 6): each id once, in order, by the launch's own check (image),
// bounded by passWait. An image the engine lost comes back from the store,
// tagged; one neither on the engine nor in the store, breaking the
// contract, or whose inspect the engine refused, is missing: it is logged an
// image is missing, and listed in Status until a later check finds it. The
// engine's silence ends the check, leaving the images it did not reach as
// they were, and checkImages returns it.
func (r *Runner) checkImages(ctx context.Context) error {
	r.mu.Lock()
	missing := slices.Clone(r.missing)
	r.mu.Unlock()
	var silent error
	for _, id := range slices.Compact(slices.Sorted(maps.Values(r.o.Images))) {
		ictx, cancel := context.WithTimeout(ctx, passWait)
		_, reason, err := r.image(ictx, id)
		cancel()
		if reason == app.ReasonEngine {
			silent = fmt.Errorf("docker: the image check stopped at the image %s: %w", id, err)
			break
		}
		missing = slices.DeleteFunc(missing, func(m string) bool { return m == id })
		if err != nil {
			r.log.ErrorContext(ctx, "an image is missing", "image", id, "err", err)
			missing = append(missing, id)
		}
	}
	slices.Sort(missing)
	r.mu.Lock()
	r.missing = missing
	r.mu.Unlock()
	return silent
}

// watched is the runner's engine: each call's answer, or its lack of one,
// is the runner's record of the engine, which a launch that a stop ends at
// its turn's deadline reads, and so does Status (Contract C14).
// A call the engine answered, an error it gave included, marks it
// reachable; a call that found no connection, or a broken one, or that ran
// out its own bound, marks it unreachable; a call its context's cancel
// ended, as a stop, a fence or a drain ends it, leaves the record as it was
// (plan review 2, F3), and so does a wait for a container that outlasted
// the wait's deadline (Wait).
type watched struct {
	e           Engine
	unreachable atomic.Bool
}

var _ Engine = (*watched)(nil)

// note keeps what a call's error says of the engine.
func (w *watched) note(err error) {
	switch {
	case !errors.Is(err, ErrUnreachable):
		w.unreachable.Store(false)
	case errors.Is(err, context.Canceled):
	default:
		w.unreachable.Store(true)
	}
}

func (w *watched) Version(ctx context.Context) (Info, error) {
	info, err := w.e.Version(ctx)
	w.note(err)
	return info, err
}

func (w *watched) Image(ctx context.Context, ref string) (Image, error) {
	img, err := w.e.Image(ctx, ref)
	w.note(err)
	return img, err
}

func (w *watched) Save(ctx context.Context, id string, to io.Writer) error {
	err := w.e.Save(ctx, id, to)
	w.note(err)
	return err
}

func (w *watched) Load(ctx context.Context, from io.Reader) error {
	err := w.e.Load(ctx, from)
	w.note(err)
	return err
}

func (w *watched) Tag(ctx context.Context, id, ref string) error {
	err := w.e.Tag(ctx, id, ref)
	w.note(err)
	return err
}

func (w *watched) Create(ctx context.Context, name string, cfg *container.Config, host *container.HostConfig) (string, error) {
	id, err := w.e.Create(ctx, name, cfg, host)
	w.note(err)
	return id, err
}

func (w *watched) Attach(ctx context.Context, id string) (Attachment, error) {
	att, err := w.e.Attach(ctx, id)
	w.note(err)
	return att, err
}

func (w *watched) Start(ctx context.Context, id string) error {
	err := w.e.Start(ctx, id)
	w.note(err)
	return err
}

// Wait notes nothing of a wait whose context's deadline came first: the
// engine answers a wait once the container ends, so a container that runs
// past the wait's deadline says nothing of the engine. A connection that
// broke, or an answer, is noted as for any call.
func (w *watched) Wait(ctx context.Context, id string) error {
	err := w.e.Wait(ctx, id)
	if !errors.Is(err, context.DeadlineExceeded) {
		w.note(err)
	}
	return err
}

func (w *watched) Inspect(ctx context.Context, idOrName string) (Container, error) {
	c, err := w.e.Inspect(ctx, idOrName)
	w.note(err)
	return c, err
}

func (w *watched) Kill(ctx context.Context, idOrName string) error {
	err := w.e.Kill(ctx, idOrName)
	w.note(err)
	return err
}

func (w *watched) Remove(ctx context.Context, idOrName string) error {
	err := w.e.Remove(ctx, idOrName)
	w.note(err)
	return err
}

func (w *watched) List(ctx context.Context, label string) ([]Listed, error) {
	listed, err := w.e.List(ctx, label)
	w.note(err)
	return listed, err
}

// Close closes the engine's client, which tells nothing of the engine.
func (w *watched) Close() error { return w.e.Close() }
