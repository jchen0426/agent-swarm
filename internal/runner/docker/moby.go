package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
)

// dialWait bounds a dial of the engine's socket, as the moby client's own
// dialer does.
const dialWait = 10 * time.Second

// Dial returns the moby client's Engine at a unix:// endpoint, negotiating
// the API version on its first call. Dial itself asks the engine nothing:
// it fails only for an endpoint that is not unix://.
func Dial(endpoint string) (Engine, error) {
	path, ok := strings.CutPrefix(endpoint, "unix://")
	if !ok || path == "" {
		return nil, fmt.Errorf("docker: the engine's endpoint %q is not a unix:// socket", endpoint)
	}
	c, err := client.New(client.WithHost(endpoint), client.WithDialContext(dialer(path)))
	if err != nil {
		return nil, fmt.Errorf("docker: a client of the engine at %s: %w", endpoint, err)
	}
	return &moby{c: c}, nil
}

// dialer returns the client's dialer of the engine's socket at path. A dial
// whose context carries an attach's hijack (hijackKey) hands it the
// connection.
func dialer(path string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialWait}
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := d.DialContext(ctx, "unix", path)
		if err != nil {
			return nil, err
		}
		if h, ok := ctx.Value(hijackKey{}).(*hijack); ok {
			h.hold(conn)
		}
		return conn, nil
	}
}

// moby is the Engine of the moby client. Every call but Attach is an
// ordinary request, which the client ends once its context ends; Attach ends
// its hijack itself.
type moby struct {
	c *client.Client
	// negotiated says an attach has settled the client's API version.
	negotiated atomic.Bool
}

var _ Engine = (*moby)(nil)

func (m *moby) Version(ctx context.Context) (Info, error) {
	v, err := m.c.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return Info{}, engineErr(ctx, "ask the engine its version", err, nil)
	}
	return Info{OS: v.Os, Arch: v.Arch}, nil
}

func (m *moby) Image(ctx context.Context, ref string) (Image, error) {
	r, err := m.c.ImageInspect(ctx, ref)
	if err != nil {
		return Image{}, engineErr(ctx, "inspect the image "+ref, err, ErrNoImage)
	}
	return imageOf(&r.InspectResponse), nil
}

// Save writes docker save's archive of the image id, saved by its id, to w.
// A failure of w's is its own, not the engine's.
func (m *moby) Save(ctx context.Context, id string, w io.Writer) error {
	op := "save the image " + id
	archive, err := m.c.ImageSave(ctx, []string{id})
	if err != nil {
		return engineErr(ctx, op, err, ErrNoImage)
	}
	defer func() { _ = archive.Close() }()
	src := &watchedReader{r: archive}
	if _, err := io.Copy(w, src); err != nil {
		if src.failed() != nil {
			return engineErr(ctx, op, err, ErrNoImage)
		}
		return fmt.Errorf("docker: %s: write it: %w", op, err)
	}
	return nil
}

// Load loads the images of docker save's archive r. A failure of r's is its
// own, not the engine's.
func (m *moby) Load(ctx context.Context, r io.Reader) error {
	const op = "load an image"
	src := &watchedReader{r: r}
	out, err := m.c.ImageLoad(ctx, src, client.ImageLoadWithQuiet(true))
	if err == nil {
		err = readMessages(out)
	}
	if err != nil {
		if failed := src.failed(); failed != nil {
			return fmt.Errorf("docker: %s: read it: %w", op, failed)
		}
		return engineErr(ctx, op, err, nil)
	}
	return nil
}

func (m *moby) Tag(ctx context.Context, id, ref string) error {
	if _, err := m.c.ImageTag(ctx, client.ImageTagOptions{Source: id, Target: ref}); err != nil {
		return engineErr(ctx, "tag the image "+id+" as "+ref, err, ErrNoImage)
	}
	return nil
}

// Create creates the container; the one object a creation can find missing
// is its image.
func (m *moby) Create(ctx context.Context, name string, cfg *container.Config, host *container.HostConfig) (string, error) {
	created, err := m.c.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: cfg, HostConfig: host})
	if err != nil {
		return "", engineErr(ctx, "create the container "+name, err, ErrNoImage)
	}
	return created.ID, nil
}

// Attach attaches to the container's standard input, output and error. It
// honours ctx, which the moby client's hijack does not once it has dialed:
// the hijack runs on a goroutine of its own, and when ctx ends first, its
// connection is closed, whether it was dialed already or is dialed later, so
// that the goroutine ends, and an answer that comes after all is not left
// open. The engine's refusal of the hijack, a missing container's included,
// gives no reason the client reads, so no refusal reads ErrNoContainer.
func (m *moby) Attach(ctx context.Context, id string) (Attachment, error) {
	op := "attach to the container " + id
	if err := m.negotiate(ctx); err != nil {
		return nil, engineErr(ctx, op, err, nil)
	}
	h := &hijack{}
	type answer struct {
		attached client.ContainerAttachResult
		err      error
	}
	answered := make(chan answer, 1)
	go func() {
		attached, err := m.c.ContainerAttach(context.WithValue(ctx, hijackKey{}, h), id,
			client.ContainerAttachOptions{Stream: true, Stdin: true, Stdout: true, Stderr: true})
		answered <- answer{attached, err}
	}()
	select {
	case a := <-answered:
		if a.err != nil {
			return nil, engineErr(ctx, op, a.err, nil)
		}
		return newAttachment(&a.attached.HijackedResponse), nil
	case <-ctx.Done():
		h.cut()
		return nil, engineErr(ctx, op, ctx.Err(), nil)
	}
}

// negotiate settles the client's API version before an attach, so that no
// ordinary request runs under the hijack's context, which would hand the
// hijack a connection of the client's pool: the dial the hijack's context
// carries is then the hijack's own.
func (m *moby) negotiate(ctx context.Context) error {
	if m.negotiated.Load() {
		return nil
	}
	if _, err := m.c.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		return err
	}
	m.negotiated.Store(true)
	return nil
}

func (m *moby) Start(ctx context.Context, id string) error {
	if _, err := m.c.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return engineErr(ctx, "start the container "+id, err, ErrNoContainer)
	}
	return nil
}

// Wait returns once the container is not running: at once for one that has
// ended, or never started. An end the engine could not wait for in full, as
// it reports with its answer, fails the wait.
func (m *moby) Wait(ctx context.Context, id string) error {
	waited := m.c.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case res := <-waited.Result:
		if res.Error != nil {
			return fmt.Errorf("docker: wait for the container %s: %s", id, res.Error.Message)
		}
		return nil
	case err := <-waited.Error:
		return engineErr(ctx, "wait for the container "+id, err, ErrNoContainer)
	}
}

func (m *moby) Inspect(ctx context.Context, idOrName string) (Container, error) {
	r, err := m.c.ContainerInspect(ctx, idOrName, client.ContainerInspectOptions{})
	if err != nil {
		return Container{}, engineErr(ctx, "inspect the container "+idOrName, err, ErrNoContainer)
	}
	return containerOf(&r.Container)
}

// Kill sends the container SIGKILL.
func (m *moby) Kill(ctx context.Context, idOrName string) error {
	if _, err := m.c.ContainerKill(ctx, idOrName, client.ContainerKillOptions{Signal: "SIGKILL"}); err != nil {
		return engineErr(ctx, "kill the container "+idOrName, err, ErrNoContainer)
	}
	return nil
}

// Remove removes the container, running or not, with its volumes.
func (m *moby) Remove(ctx context.Context, idOrName string) error {
	opts := client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}
	if _, err := m.c.ContainerRemove(ctx, idOrName, opts); err != nil {
		return engineErr(ctx, "remove the container "+idOrName, err, ErrNoContainer)
	}
	return nil
}

// List lists the containers labelled label, key=value, in every state.
func (m *moby) List(ctx context.Context, label string) ([]Listed, error) {
	opts := client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", label)}
	r, err := m.c.ContainerList(ctx, opts)
	if err != nil {
		return nil, engineErr(ctx, "list the containers labelled "+label, err, nil)
	}
	listed := make([]Listed, 0, len(r.Items))
	for i := range r.Items {
		listed = append(listed, Listed{ID: r.Items[i].ID, Labels: r.Items[i].Labels})
	}
	return listed, nil
}

func (m *moby) Close() error { return m.c.Close() }

// engineErr is the error of the moby client's call op, err, as the port
// gives it. The engine's answer that the object is missing reads gone,
// ErrNoImage or ErrNoContainer (nil for a call of no object); a context that
// ended reads ErrUnreachable, wrapping the context's error; no connection,
// or one that broke, reads ErrUnreachable; any other answer is kept as the
// engine gave it.
func engineErr(ctx context.Context, op string, err, gone error) error {
	switch {
	case gone != nil && cerrdefs.IsNotFound(err):
		return fmt.Errorf("%w: %s", gone, op)
	case ctx.Err() != nil:
		return fmt.Errorf("%w: %s: %w", ErrUnreachable, op, ctx.Err())
	case brokenConn(err):
		return fmt.Errorf("%w: %s: %w", ErrUnreachable, op, err)
	default:
		return fmt.Errorf("docker: %s: %w", op, err)
	}
}

// brokenConn reports whether err says that the engine did not answer: the
// moby client's failure to connect, a failure of the connection's, or an
// answer cut short.
func brokenConn(err error) bool {
	var netErr net.Error
	return client.IsErrConnectionFailed(err) || errors.As(err, &netErr) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

// imageOf is what the contract reads of an image's inspection.
func imageOf(r *image.InspectResponse) Image {
	img := Image{ID: r.ID, OS: r.Os, Arch: r.Architecture}
	if c := r.Config; c != nil {
		img.Labels, img.Env, img.OnBuild = c.Labels, c.Env, c.OnBuild
		img.Volumes = slices.Sorted(maps.Keys(c.Volumes))
		if c.Healthcheck != nil {
			img.Healthcheck = c.Healthcheck.Test
		}
	}
	return img
}

// containerOf is what the runner reads of a container's inspection.
func containerOf(r *container.InspectResponse) (Container, error) {
	c := Container{ID: r.ID, Name: strings.TrimPrefix(r.Name, "/")}
	if r.Config != nil {
		c.Labels = r.Config.Labels
	}
	var err error
	if c.Created, err = engineTime(r.Created); err != nil {
		return Container{}, fmt.Errorf("docker: the container %s's creation time: %w", r.ID, err)
	}
	if s := r.State; s != nil {
		c.Running, c.ExitCode, c.OOMKilled = s.Running, s.ExitCode, s.OOMKilled
		if c.Finished, err = engineTime(s.FinishedAt); err != nil {
			return Container{}, fmt.Errorf("docker: the container %s's end: %w", r.ID, err)
		}
	}
	return c, nil
}

// engineTime reads a time the engine gives, in RFC 3339 with nanoseconds. Its
// zero, which a container that never ran gives, and none at all read as the
// zero time.
func engineTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	switch {
	case err != nil:
		return time.Time{}, err
	case t.IsZero():
		return time.Time{}, nil
	}
	return t, nil
}

// readMessages reads an image call's stream of JSON messages to its end, and
// closes it; it returns the error a message carries.
func readMessages(r io.ReadCloser) error {
	defer func() { _ = r.Close() }()
	dec := json.NewDecoder(r)
	for {
		var msg jsonstream.Message
		if err := dec.Decode(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if msg.Error != nil {
			return msg.Error
		}
	}
}

// watchedReader reads r, and keeps the first failure of r's, io.EOF aside,
// so that the caller of a copy tells its source's failure from its sink's.
// The client may read a request's body on a goroutine of its own.
type watchedReader struct {
	r   io.Reader
	mu  sync.Mutex
	err error
}

func (w *watchedReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		w.mu.Lock()
		if w.err == nil {
			w.err = err
		}
		w.mu.Unlock()
	}
	return n, err
}

// failed returns the first failure of r's, io.EOF aside.
func (w *watchedReader) failed() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// hijackKey is the context key under which Attach hands the dialer its
// hijack.
type hijackKey struct{}

// hijack holds the connection an attach's hijack runs on, so that Attach can
// close it when its context ends first: once it has dialed, the moby client
// writes the request and reads the engine's answer on the bare connection,
// watching no context (hijack.go), and would wait for good on an engine that
// never answers.
type hijack struct {
	mu   sync.Mutex
	conn net.Conn
	done bool
}

// hold keeps the hijack's connection, or closes it at once when the hijack
// was cut already.
func (h *hijack) hold(conn net.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.done {
		_ = conn.Close()
		return
	}
	h.conn = conn
}

// cut closes the connection held, and any the dial hands the hijack later.
func (h *hijack) cut() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.done = true
	if h.conn != nil {
		_ = h.conn.Close()
	}
}

// attachment is a container's attached streams, on its hijacked connection
// (Attachment): Write writes the container's standard input, and a goroutine
// of its own demultiplexes the container's output (stdcopy) into a pipe,
// which Output reads to io.EOF once the engine ends the stream.
type attachment struct {
	conn net.Conn
	out  *io.PipeReader
	// end closes the connection and the pipe, once.
	end func() error
}

// newAttachment returns the attachment of a hijacked connection.
func newAttachment(h *client.HijackedResponse) *attachment {
	r, w := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(w, w, h.Reader)
		_ = w.CloseWithError(err)
	}()
	conn := h.Conn
	return &attachment{conn: conn, out: r, end: sync.OnceValue(func() error {
		err := conn.Close()
		// A copy that waits on a reader no one reads ends too.
		_ = r.Close()
		return err
	})}
}

func (a *attachment) Write(p []byte) (int, error) { return a.conn.Write(p) }

// Close ends the attachment: the engine closes the container's input, and
// Output reads io.ErrClosedPipe from then on.
func (a *attachment) Close() error { return a.end() }

func (a *attachment) Output() io.Reader { return a.out }
