// Package docker runs agents' turns in Docker sandboxes (Plan 7d-2, Decision
// 5): a container per turn, of the runner's own settings whatever its image
// says (Decision 3; Contract C13), from a runtime image that meets the image
// contract (Decision 6; Contract C12) and that a store keeps, so that a
// prune on the host does not lose it. The runner reaches the engine only
// through its own port, Engine, which Dial's moby client implements and the
// unit tests fake.
package docker

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/moby/moby/api/types/container"
)

// Engine is what the runner asks of the Docker Engine: a port of its own, so
// that its unit tests run against a fake. Every call returns once its ctx
// ends, whether or not the engine has answered, with an error wrapping both
// ErrUnreachable and ctx's error; the runner tells a stop's cancel from the
// engine's silence by why ctx ended (Decision 5), and a cancel leaves the
// runner's record of the engine as it was.
type Engine interface {
	Version(ctx context.Context) (Info, error)
	Image(ctx context.Context, ref string) (Image, error) // ErrNoImage when missing
	Save(ctx context.Context, id string, w io.Writer) error
	Load(ctx context.Context, r io.Reader) error
	Tag(ctx context.Context, id, ref string) error
	Create(ctx context.Context, name string, cfg *container.Config, host *container.HostConfig) (string, error)
	// Attach honours ctx, which the moby client's hijack does not once it
	// has dialed.
	Attach(ctx context.Context, id string) (Attachment, error)
	Start(ctx context.Context, id string) error
	Wait(ctx context.Context, id string) error                       // returns once the container is not running
	Inspect(ctx context.Context, idOrName string) (Container, error) // ErrNoContainer when gone
	Kill(ctx context.Context, idOrName string) error                 // nil when it does not run; ErrNoContainer when gone
	Remove(ctx context.Context, idOrName string) error               // forced, with its volumes; ErrNoContainer when gone
	List(ctx context.Context, label string) ([]Listed, error)        // every state, by label key=value
	Close() error
}

var (
	// ErrNoImage marks an image the engine does not have.
	ErrNoImage = errors.New("docker: no such image")
	// ErrNoContainer marks a container the engine does not have.
	ErrNoContainer = errors.New("docker: no such container")
	// ErrUnreachable wraps a call the engine did not answer: no connection,
	// or one that broke, or a context that ended first.
	ErrUnreachable = errors.New("docker: the engine does not answer")
)

// Info is what the engine says of itself.
type Info struct{ OS, Arch string }

// Image is what the image contract reads of an image (Contract C12).
type Image struct {
	ID                    string // sha256:<64 hex digits>
	OS, Arch              string
	Labels                map[string]string
	Env, Volumes, OnBuild []string
	Healthcheck           []string // its Test; nil when the image has none
}

// Attachment is a container's standard input, open for the turn's life, and
// its standard output and error, demultiplexed into one stream (stdcopy).
type Attachment interface {
	io.WriteCloser // closing it ends the attachment, and so the container's input
	Output() io.Reader
}

// Container is what the runner reads of a container's state.
type Container struct {
	ID, Name          string
	Labels            map[string]string
	Running           bool
	Created, Finished time.Time // Finished is zero for a container that never ran
	ExitCode          int
	OOMKilled         bool
}

// Listed is a container as a listing gives it.
type Listed struct {
	ID     string
	Labels map[string]string
}
