package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"

	"github.com/jchen0426/agent-swarm/internal/outcome"
)

// fakeEngine is the runner's engine in its unit tests (Contract C14). It
// keeps images and containers, and runs a started container's program, as
// the engine runs its process. It records every call, with its argument,
// and can hold the calls of a method until the test releases them: a held
// call returns once its context ends, wrapping ErrUnreachable and the
// context's error, as the port's calls do, and so does a call made on a
// context that has ended. Its hook, when set, gives a call an error of the
// test's.
type fakeEngine struct {
	arch string

	mu         sync.Mutex
	trail      []call
	images     map[string]Image          // by id
	tags       map[string]string         // a tag's image id
	known      map[string]Image          // every image the engine held, which a load gives back
	containers map[string]*fakeContainer // by id
	names      map[string]string         // a container's id, by its name
	gates      map[string]chan struct{}  // the calls of a method wait until its gate closes
	frozen     chan struct{}             // every call waits, unrecorded, until it closes
	hook       func(method, arg string) error
	programs   map[string]program // by the turn's id, which its container's label holds
	program    program            // what a container of no program of its own runs
	made       int                // containers made
}

// call is an engine call, as the fake recorded it: its method, the id, name
// or reference it names, and whether its context was live when it was made.
// A mark of the test's, such as Exited, is a call with no argument.
type call struct {
	method, arg string
	live        bool
}

// String is the call as the tests compare it: "Remove agentswarm-turn-1".
func (c call) String() string { return strings.TrimSpace(c.method + " " + c.arg) }

// program is what a fake container runs once started, as a turn's runtime
// does: it reads its spec, one JSON object, from its input, and ends with 1
// when it cannot; then it writes line, and print bytes, on its output; then it ends
// with exit, and oom, after sleep, or at until when that is set; or, with
// forever, it runs until it is killed, or until its input ends, which ends it
// with 14, as the runtime ends at its input's end.
type program struct {
	line    string
	print   int
	sleep   time.Duration
	until   time.Time
	exit    int
	oom     bool
	forever bool
}

// fakeContainer is a container of the fake's: its configuration, its
// state, and its standard input and output, each a pipe.
type fakeContainer struct {
	id, name string
	cfg      *container.Config
	host     *container.HostConfig
	created  time.Time
	program  program

	started, running bool
	exit             int
	oom              bool
	finished         time.Time
	// done closes once the container has ended.
	done chan struct{}
	// input holds every byte written on the container's attachment.
	input bytes.Buffer

	inR  *io.PipeReader
	inW  *io.PipeWriter
	outR *io.PipeReader
	outW *io.PipeWriter
}

// newFakeEngine returns an engine of the arm64 architecture, which holds
// images, and whose containers run until they are killed.
func newFakeEngine(images ...*Image) *fakeEngine {
	fe := &fakeEngine{
		arch: "arm64", images: map[string]Image{}, tags: map[string]string{}, known: map[string]Image{},
		containers: map[string]*fakeContainer{}, names: map[string]string{}, gates: map[string]chan struct{}{},
		programs: map[string]program{}, program: program{forever: true},
	}
	for _, img := range images {
		fe.addImage(img)
	}
	return fe
}

// held is the error of a call whose context ended before the engine
// answered it.
func held(ctx context.Context, method, arg string) error {
	return fmt.Errorf("%w: %s %s: %w", ErrUnreachable, method, arg, ctx.Err())
}

// down is the error of a call the engine did not answer: no connection.
func down(method, arg string) error {
	return fmt.Errorf("%w: %s %s: connection refused", ErrUnreachable, method, arg)
}

// enter takes a call: it waits while the fake is frozen, records the call,
// waits while its method is held, and gives the call the hook's error, if
// any. A call whose context ends while it waits, or ended before it was
// made, fails as the port says.
func (fe *fakeEngine) enter(ctx context.Context, method, arg string) error {
	fe.mu.Lock()
	frozen := fe.frozen
	fe.mu.Unlock()
	if frozen != nil {
		select {
		case <-frozen:
		case <-ctx.Done():
			return held(ctx, method, arg)
		}
	}
	fe.mu.Lock()
	fe.trail = append(fe.trail, call{method: method, arg: arg, live: ctx.Err() == nil})
	gate, hook := fe.gates[method], fe.hook
	fe.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return held(ctx, method, arg)
		}
	}
	if ctx.Err() != nil {
		return held(ctx, method, arg)
	}
	if hook != nil {
		return hook(method, arg)
	}
	return nil
}

// block holds every later call of method until release, which it returns. A
// gate closes only while it is registered: a release after releaseAll, or a
// second release, does nothing; a block of a method already held opens the
// gate it replaces, so that no call waits on a gate no release reaches.
func (fe *fakeEngine) block(method string) (release func()) {
	gate := make(chan struct{})
	fe.mu.Lock()
	defer fe.mu.Unlock()
	if old, ok := fe.gates[method]; ok {
		close(old)
	}
	fe.gates[method] = gate
	return func() {
		fe.mu.Lock()
		defer fe.mu.Unlock()
		if fe.gates[method] == gate {
			delete(fe.gates, method)
			close(gate)
		}
	}
}

// freeze holds every later call, before it is recorded, until thaw, which it
// returns. As block's gate, the freeze's closes only while it is
// registered.
func (fe *fakeEngine) freeze() (thaw func()) {
	frozen := make(chan struct{})
	fe.mu.Lock()
	defer fe.mu.Unlock()
	if fe.frozen != nil {
		close(fe.frozen)
	}
	fe.frozen = frozen
	return func() {
		fe.mu.Lock()
		defer fe.mu.Unlock()
		if fe.frozen == frozen {
			fe.frozen = nil
			close(frozen)
		}
	}
}

// setHook gives each later call hook's error, when it gives one.
func (fe *fakeEngine) setHook(hook func(method, arg string) error) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	fe.hook = hook
}

// releaseAll releases every call held, thaws the fake and removes its hook.
func (fe *fakeEngine) releaseAll() {
	fe.mu.Lock()
	gates, frozen := slices.Collect(maps.Values(fe.gates)), fe.frozen
	fe.gates, fe.frozen, fe.hook = map[string]chan struct{}{}, nil, nil
	fe.mu.Unlock()
	for _, gate := range gates {
		close(gate)
	}
	if frozen != nil {
		close(frozen)
	}
}

// mark records a mark of the test's, such as Exited, in the trail.
func (fe *fakeEngine) mark(what string) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	fe.trail = append(fe.trail, call{method: what, live: true})
}

// forgetCalls clears the trail.
func (fe *fakeEngine) forgetCalls() {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	fe.trail = nil
}

// calls returns the trail.
func (fe *fakeEngine) calls() []call {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return slices.Clone(fe.trail)
}

// said returns the trail as the tests compare it.
func (fe *fakeEngine) said() []string {
	var out []string
	for _, c := range fe.calls() {
		out = append(out, c.String())
	}
	return out
}

// saidOf returns the trail's calls whose argument is arg, as the tests
// compare them: those of one container, by its id, with no mark of the
// test's, whose order against a turn's calls no test fixes.
func (fe *fakeEngine) saidOf(arg string) []string {
	var out []string
	for _, c := range fe.calls() {
		if c.arg == arg {
			out = append(out, c.String())
		}
	}
	return out
}

// callsOf returns the calls of method.
func (fe *fakeEngine) callsOf(method string) []call {
	var out []call
	for _, c := range fe.calls() {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

// play sets what the container of the turn id runs.
func (fe *fakeEngine) play(id string, p program) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	fe.programs[id] = p
}

// addImage puts an image on the engine.
func (fe *fakeEngine) addImage(img *Image) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	fe.images[img.ID] = *img
	fe.known[img.ID] = *img
}

// lose takes an image off the engine, with its tags, as a prune does.
func (fe *fakeEngine) lose(id string) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	delete(fe.images, id)
	maps.DeleteFunc(fe.tags, func(_, v string) bool { return v == id })
}

// tagged returns the image id a tag names, if any.
func (fe *fakeEngine) tagged(ref string) string {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return fe.tags[ref]
}

// byTurn returns the container of the turn id, if the engine has it.
func (fe *fakeEngine) byTurn(id string) *fakeContainer {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	if cid, ok := fe.names[containerName(id)]; ok {
		return fe.containers[cid]
	}
	return nil
}

// left is a container that no launch of the test made, as a previous
// process of the holder, or another holder, leaves one (leave).
type left struct {
	labels  map[string]string
	running bool
	// created is when it was created, and finished when it ended: zero for
	// one that runs, or never ran.
	created, finished time.Time
}

// leave puts l's container on the engine, unrecorded, and returns its id:
// running, stopped with exit code 0 once finished, or never started. It is
// named left-<n>, a name no sandbox has, so that a call by its name shows.
func (fe *fakeEngine) leave(l left) string {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	fe.made++
	c := &fakeContainer{
		id: fmt.Sprintf("%064x", fe.made), name: fmt.Sprintf("left-%d", fe.made),
		cfg: &container.Config{Labels: l.labels}, host: &container.HostConfig{},
		created: l.created, started: l.running || !l.finished.IsZero(), running: l.running, finished: l.finished,
		done: make(chan struct{}),
	}
	c.inR, c.inW = io.Pipe()
	c.outR, c.outW = io.Pipe()
	if c.started && !c.running {
		close(c.done)
	}
	fe.containers[c.id] = c
	fe.names[c.name] = c.id
	return c.id
}

// holds reports whether the engine holds the container id.
func (fe *fakeEngine) holds(id string) bool {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	_, ok := fe.containers[id]
	return ok
}

// received returns what the container's attachment received.
func (fe *fakeEngine) received(c *fakeContainer) string {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return c.input.String()
}

// state returns the container's state: whether it runs, and how it ended.
func (fe *fakeEngine) state(c *fakeContainer) (running bool, exit int, finished time.Time) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return c.running, c.exit, c.finished
}

// find returns the container that idOrName names.
func (fe *fakeEngine) find(method, idOrName string) (*fakeContainer, error) {
	fe.mu.Lock()
	defer fe.mu.Unlock()
	id := idOrName
	if named, ok := fe.names[idOrName]; ok {
		id = named
	}
	c, ok := fe.containers[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s %s", ErrNoContainer, method, idOrName)
	}
	return c, nil
}

// drop takes a container off the engine at once, as an operator's docker rm
// -f does.
func (fe *fakeEngine) drop(idOrName string) {
	if c, err := fe.find("drop", idOrName); err == nil {
		fe.end(c, 128+9, false)
		fe.mu.Lock()
		delete(fe.containers, c.id)
		delete(fe.names, c.name)
		fe.mu.Unlock()
		fe.closeStreams(c)
	}
}

// end ends a running container with code, once: it notes how and when, and
// closes its streams, so that its output reaches its end and its input
// takes no more.
func (fe *fakeEngine) end(c *fakeContainer, code int, oom bool) {
	fe.mu.Lock()
	if !c.running {
		fe.mu.Unlock()
		return
	}
	c.running, c.exit, c.oom, c.finished = false, code, oom, time.Now()
	fe.mu.Unlock()
	close(c.done)
	fe.closeStreams(c)
}

// closeStreams closes the container's own ends of its streams.
func (fe *fakeEngine) closeStreams(c *fakeContainer) {
	_ = c.outW.Close()
	_ = c.inR.CloseWithError(io.ErrClosedPipe)
}

// run runs the container's program (program).
func (fe *fakeEngine) run(c *fakeContainer) {
	p := c.program
	dec := json.NewDecoder(c.inR)
	var spec json.RawMessage
	if err := dec.Decode(&spec); err != nil {
		fe.end(c, 1, false)
		return
	}
	if p.line != "" {
		_, _ = io.WriteString(c.outW, p.line)
	}
	if p.print > 0 {
		_, _ = c.outW.Write(bytes.Repeat([]byte("x"), p.print))
	}
	if p.forever {
		eof := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, io.MultiReader(dec.Buffered(), c.inR))
			close(eof)
		}()
		select {
		case <-eof:
			fe.end(c, outcome.ExitInterrupted, false)
		case <-c.done:
		}
		return
	}
	wait := p.sleep
	if !p.until.IsZero() {
		wait = time.Until(p.until)
	}
	select {
	case <-time.After(wait):
		fe.end(c, p.exit, p.oom)
	case <-c.done:
	}
}

var _ Engine = (*fakeEngine)(nil)

func (fe *fakeEngine) Version(ctx context.Context) (Info, error) {
	if err := fe.enter(ctx, "Version", ""); err != nil {
		return Info{}, err
	}
	return Info{OS: "linux", Arch: fe.arch}, nil
}

func (fe *fakeEngine) Image(ctx context.Context, ref string) (Image, error) {
	if err := fe.enter(ctx, "Image", ref); err != nil {
		return Image{}, err
	}
	fe.mu.Lock()
	defer fe.mu.Unlock()
	id := ref
	if tagged, ok := fe.tags[ref]; ok {
		id = tagged
	}
	img, ok := fe.images[id]
	if !ok {
		return Image{}, fmt.Errorf("%w: Image %s", ErrNoImage, ref)
	}
	return img, nil
}

// Save writes the fake's archive of an image: one line that names it.
func (fe *fakeEngine) Save(ctx context.Context, id string, w io.Writer) error {
	if err := fe.enter(ctx, "Save", id); err != nil {
		return err
	}
	fe.mu.Lock()
	_, ok := fe.images[id]
	fe.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: Save %s", ErrNoImage, id)
	}
	_, err := io.WriteString(w, "fake image "+id+"\n")
	return err
}

// Load gives back the image an archive of Save's names.
func (fe *fakeEngine) Load(ctx context.Context, r io.Reader) error {
	if err := fe.enter(ctx, "Load", ""); err != nil {
		return err
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	id, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "fake image ")
	fe.mu.Lock()
	defer fe.mu.Unlock()
	img, known := fe.known[id]
	if !ok || !known {
		return errors.New("docker: load an image: the archive holds no image")
	}
	fe.images[id] = img
	return nil
}

func (fe *fakeEngine) Tag(ctx context.Context, id, ref string) error {
	if err := fe.enter(ctx, "Tag", id+" "+ref); err != nil {
		return err
	}
	fe.mu.Lock()
	defer fe.mu.Unlock()
	if _, ok := fe.images[id]; !ok {
		return fmt.Errorf("%w: Tag %s", ErrNoImage, id)
	}
	fe.tags[ref] = id
	return nil
}

// Create makes a container that has not run, of an image the engine holds,
// under a name no other container holds.
func (fe *fakeEngine) Create(ctx context.Context, name string, cfg *container.Config, host *container.HostConfig) (string, error) {
	if err := fe.enter(ctx, "Create", name); err != nil {
		return "", err
	}
	fe.mu.Lock()
	defer fe.mu.Unlock()
	if _, ok := fe.names[name]; ok {
		return "", fmt.Errorf("docker: create the container %s: the name is in use", name)
	}
	if _, ok := fe.images[cfg.Image]; !ok {
		return "", fmt.Errorf("%w: Create %s", ErrNoImage, cfg.Image)
	}
	fe.made++
	id := fmt.Sprintf("%064x", fe.made)
	p, ok := fe.programs[cfg.Labels[labelTurn]]
	if !ok {
		p = fe.program
	}
	c := &fakeContainer{id: id, name: name, cfg: cfg, host: host, created: time.Now(), program: p, done: make(chan struct{})}
	c.inR, c.inW = io.Pipe()
	c.outR, c.outW = io.Pipe()
	fe.containers[id] = c
	fe.names[name] = id
	return id, nil
}

func (fe *fakeEngine) Attach(ctx context.Context, id string) (Attachment, error) {
	if err := fe.enter(ctx, "Attach", id); err != nil {
		return nil, err
	}
	c, err := fe.find("Attach", id)
	if err != nil {
		return nil, err
	}
	return &fakeAttachment{fe: fe, c: c}, nil
}

func (fe *fakeEngine) Start(ctx context.Context, id string) error {
	if err := fe.enter(ctx, "Start", id); err != nil {
		return err
	}
	c, err := fe.find("Start", id)
	if err != nil {
		return err
	}
	fe.mu.Lock()
	defer fe.mu.Unlock()
	if c.started {
		return fmt.Errorf("docker: start the container %s: it has started", id)
	}
	c.started, c.running = true, true
	go fe.run(c)
	return nil
}

// Wait returns once the container is not running: at once for one that
// never started.
func (fe *fakeEngine) Wait(ctx context.Context, id string) error {
	if err := fe.enter(ctx, "Wait", id); err != nil {
		return err
	}
	c, err := fe.find("Wait", id)
	if err != nil {
		return err
	}
	fe.mu.Lock()
	started := c.started
	fe.mu.Unlock()
	if !started {
		return nil
	}
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return held(ctx, "Wait", id)
	}
}

func (fe *fakeEngine) Inspect(ctx context.Context, idOrName string) (Container, error) {
	if err := fe.enter(ctx, "Inspect", idOrName); err != nil {
		return Container{}, err
	}
	c, err := fe.find("Inspect", idOrName)
	if err != nil {
		return Container{}, err
	}
	fe.mu.Lock()
	defer fe.mu.Unlock()
	return Container{
		ID: c.id, Name: c.name, Labels: c.cfg.Labels, Running: c.running,
		Created: c.created, Finished: c.finished, ExitCode: c.exit, OOMKilled: c.oom,
	}, nil
}

// Kill ends a running container with 137, as SIGKILL does; a kill of one
// that does not run succeeds (Contract C14, amended).
func (fe *fakeEngine) Kill(ctx context.Context, idOrName string) error {
	if err := fe.enter(ctx, "Kill", idOrName); err != nil {
		return err
	}
	c, err := fe.find("Kill", idOrName)
	if err != nil {
		return err
	}
	fe.end(c, 128+9, false)
	return nil
}

// Remove removes a container, running or not: a running one is killed.
func (fe *fakeEngine) Remove(ctx context.Context, idOrName string) error {
	if err := fe.enter(ctx, "Remove", idOrName); err != nil {
		return err
	}
	if _, err := fe.find("Remove", idOrName); err != nil {
		return err
	}
	fe.drop(idOrName)
	return nil
}

func (fe *fakeEngine) List(ctx context.Context, label string) ([]Listed, error) {
	if err := fe.enter(ctx, "List", label); err != nil {
		return nil, err
	}
	key, value, _ := strings.Cut(label, "=")
	fe.mu.Lock()
	defer fe.mu.Unlock()
	var listed []Listed
	for _, c := range fe.containers {
		if v, ok := c.cfg.Labels[key]; ok && v == value {
			listed = append(listed, Listed{ID: c.id, Labels: c.cfg.Labels})
		}
	}
	return listed, nil
}

func (fe *fakeEngine) Close() error {
	fe.mark("Close")
	return nil
}

// fakeAttachment is a fake container's attachment: what it writes goes to
// the container's input, which keeps a copy, and its output is the
// container's.
type fakeAttachment struct {
	fe *fakeEngine
	c  *fakeContainer
}

func (a *fakeAttachment) Write(p []byte) (int, error) {
	a.fe.mu.Lock()
	a.c.input.Write(p)
	a.fe.mu.Unlock()
	return a.c.inW.Write(p)
}

// Close ends the attachment: the container's input ends, and its output
// reads no more.
func (a *fakeAttachment) Close() error {
	_ = a.c.inW.Close()
	return a.c.outR.Close()
}

func (a *fakeAttachment) Output() io.Reader { return a.c.outR }
