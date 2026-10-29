package docker

import (
	"context"
	"errors"
	"io"
	"sync/atomic"

	"github.com/moby/moby/api/types/container"
)

// watched is the runner's engine: each call's answer, or its lack of one,
// is the runner's record of the engine, which a launch that a stop ends at
// its turn's deadline reads, and the runner's status will (Contract C14).
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
