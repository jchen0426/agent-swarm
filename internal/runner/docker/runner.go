package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/runner/output"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
)

const (
	// openWait bounds each engine call Open makes: the ping, each image's
	// inspect, save, load and tag, and the start sweep's list, inspects,
	// kills and removals (Contract C14).
	openWait = 30 * time.Second
	// killAllWait is how long KillAll waits for the engine's answers, fixed
	// in code (Decision 5).
	killAllWait = time.Second
)

// The deployment's keys that Open's refusals name (Decision 8).
const (
	keyEndpoint = "runner.docker.endpoint"
	keyStore    = "runner.docker.store"
	keyHolder   = "holder"
	keyImages   = "runner.images."
)

// Ports are what the runner reads, and reports turns' launches and ends
// through (Contract C14).
type Ports struct {
	// Engine is the engine, Dial's in serve. Once Open returns the runner,
	// the runner owns it, and Close closes it; Open's refusal leaves it to
	// its caller.
	Engine  Engine
	Holding holding.Holding
	// Skills reads an agent's skills at a commit, off the coordinator's lock.
	// It must return once ctx ends.
	Skills func(ctx context.Context, commit, agent string) ([]string, error)
	// Ended reports whether the board holds the agent's own turn end in the
	// round.
	Ended func(ctx context.Context, run board.RunID, round int, agent string) (bool, error)
	// Exited and Launched are the coordinator's, bound once the coordinator
	// exists. Each takes the coordinator's lock, so the runner calls them
	// holding none of its own.
	Exited   func(app.Exit)
	Launched func(app.Launch)
	Log      *slog.Logger
}

// check refuses ports the runner cannot run with: it needs every port but
// Log.
func (p *Ports) check() error {
	if p.Engine == nil || p.Holding == nil || p.Skills == nil || p.Ended == nil || p.Exited == nil || p.Launched == nil {
		return errors.New("docker: the runner needs the engine, the holding, the skills, the board's turn ends, Exited and Launched")
	}
	return nil
}

// Runner runs agents' turns in Docker sandboxes, a container per turn: it is
// the coordinator's app.Turns (Decision 5). Its Start calls no engine: it
// registers the turn and queues its launch, which runs off the coordinator's
// lock and reports through Launched. Its stops never wait for the engine.
// Every minute, from Open until Close, it sweeps the containers its turns
// left, and checks its images (startJob).
type Runner struct {
	p     Ports
	o     Options
	log   *slog.Logger
	e     *watched
	arch  string // the engine's architecture, which an image must be of
	store store
	// slots holds a token for each launch that takes its steps.
	slots chan struct{}
	// close stops the minute's job, releases the holder's lock and closes
	// the engine, once.
	close func() error

	mu sync.Mutex
	// turns holds each turn the runner has, by its id, from its Start until
	// the runner forgets it: once its launch failed, or its end was reported.
	turns map[string]*turn
	// missing lists, sorted, the images of Options.Images that the minute's
	// last check found missing (checkImages).
	missing []string
}

var _ app.Turns = (*Runner)(nil)

// Open checks what the runner runs on, and returns it (Contract C14), in
// this order: the engine answers, and gives its architecture; the store is
// a directory serve can write; the holder's lock, which lives in the store,
// is free, and Open takes it; and each image of Options.Images, by its
// name's order, is on the engine or in the store, meets the contract, and is
// in the store, saved there if it was not. Then, before any turn, the start
// sweep (sweep) kills if they run, and removes, the containers of its own
// holder, its previous process's, since the runner tracks nothing yet, and
// another holder's that are due; and the minute's job starts (startJob).
// Each refusal names its deployment key, and leaves the holder's lock free;
// an engine call that runs past openWait, or finds the engine unreachable,
// refuses the start naming runner.docker.endpoint, and so does a start
// sweep whose listing fails. The ports and the options are checked first,
// with no engine call. A refusal leaves Ports.Engine to its caller, who
// closes it; once Open returns the runner, the runner owns it, and Close
// closes it.
//
//nolint:gocritic // hugeParam: the contract passes the ports and the options by value.
func Open(ctx context.Context, p Ports, o Options) (*Runner, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	if err := o.check(); err != nil {
		return nil, err
	}
	log := p.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	r := &Runner{
		p: p, o: o, log: log, e: &watched{e: p.Engine}, store: store{dir: o.Store},
		slots: make(chan struct{}, launchSlots), turns: map[string]*turn{},
	}
	vctx, cancel := context.WithTimeout(ctx, openWait)
	info, err := r.e.Version(vctx)
	cancel()
	if err != nil {
		return nil, refusal(keyEndpoint, fmt.Errorf("docker: the engine does not answer: %w", err))
	}
	r.arch = info.Arch
	if err := writable(o.Store); err != nil {
		return nil, refusal(keyStore, err)
	}
	unlock, err := lockHolder(o.Store, o.Tenant, o.Holder)
	if err != nil {
		return nil, refusal(keyHolder, err)
	}
	for _, name := range slices.Sorted(maps.Keys(o.Images)) {
		if err := r.openImage(ctx, name, o.Images[name]); err != nil {
			unlock()
			return nil, err
		}
	}
	if err := r.sweep(ctx, true, openWait); err != nil {
		unlock()
		return nil, refusal(keyEndpoint, err)
	}
	stop := r.startJob(ctx)
	r.close = sync.OnceValue(func() error {
		stop()
		unlock()
		return r.e.Close()
	})
	return r, nil
}

// refusal is Open's refusal of err, which names key, the deployment's key
// to change.
func refusal(key string, err error) error { return fmt.Errorf("%s: %w", key, err) }

// keyOf names the key of a refusal err makes: runner.docker.endpoint for an
// engine that did not answer, otherwise key.
func keyOf(err error, key string) string {
	if errors.Is(err, ErrUnreachable) {
		return keyEndpoint
	}
	return key
}

// writable checks that dir is a directory in which serve can make a file:
// it makes one, and removes it.
func writable(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("docker: the store: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("docker: the store %s is not a directory", dir)
	}
	f, err := os.CreateTemp(dir, ".agentswarm-*")
	if err != nil {
		return fmt.Errorf("docker: the store %s cannot be written: %w", dir, err)
	}
	return errors.Join(f.Close(), os.Remove(f.Name())) //nolint:gosec // G703: the file writable just made, in the deployment's store.
}

// openImage readies the image id of runner.images.<name> at Open (Decision
// 6): it finds it on the engine, or reloads it from the store when the
// engine lost it; checks it against the contract, through Options.specVersion
// (never checkImage itself); and saves it to the store unless the store has
// it. A refusal names runner.images.<name>, but one the engine's silence
// makes, runner.docker.endpoint, and a save the store cannot keep,
// runner.docker.store. Its engine calls run within openWait.
func (r *Runner) openImage(ctx context.Context, name, id string) error {
	key := keyImages + name
	if _, _, err := r.store.file(id); err != nil {
		return refusal(key, err)
	}
	fctx, cancel := context.WithTimeout(ctx, openWait)
	img, err := r.find(fctx, id)
	cancel()
	if err != nil {
		return refusal(keyOf(err, key), err)
	}
	if _, err := r.o.specVersion(&img, r.arch); err != nil {
		return refusal(key, err)
	}
	if r.store.has(id) {
		return nil
	}
	sctx, cancel := context.WithTimeout(ctx, openWait)
	defer cancel()
	if err := r.store.save(sctx, r.e, id); err != nil {
		return refusal(keyOf(err, keyStore), err)
	}
	return nil
}

// Start registers a turn for a launch after Start returns, and leaves it
// pending (app.Turns; Decision 5). It runs under the coordinator's lock, so
// it calls no engine (Review Focus 2). It asks Holding.Valid for the epoch
// ctx carries; checks the turn's id, which names its output file and its
// container; answers a Start repeated with the id of a turn the runner has
// as the first Start now stands, (true, nil) until the turn's Launched was
// reported and (false, nil) after, and starts nothing (R25); creates the
// turn's output file, so that an output directory the runner cannot write
// fails the Start, as under the process runner; then registers the turn,
// arms its deadline's timer, and queues its launch (launch), on a context
// derived from ctx, which a fence or a drain cancels.
func (r *Runner) Start(ctx context.Context, t *app.Turn) (bool, error) {
	began := time.Now()
	if err := r.holds(ctx, t); err != nil {
		return false, err
	}
	if err := checkID(t.ID); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if tr, ok := r.turns[t.ID]; ok {
		return !tr.reported, nil
	}
	file, err := output.Create(r.o.Output, t.ID, r.o.OutputCap)
	if err != nil {
		return false, fmt.Errorf("docker: create the turn's output file: %w", err)
	}
	steps, cancel := context.WithCancel(ctx)
	tr := &turn{t: *t, began: began, file: file, base: context.WithoutCancel(ctx), cancel: cancel, launched: make(chan struct{})}
	tr.timer = time.AfterFunc(time.Until(t.Deadline), func() { r.atDeadline(tr) })
	r.turns[t.ID] = tr
	go r.launch(steps, tr)
	return true, nil
}

// errUnheld fails a turn's start, or its launch, that the holding refused:
// the holding it acts for is over. It is a launch that failed, which posts
// no notice and never fences: it wraps app.ErrUnheld, never
// holding.ErrLeaseLost.
var errUnheld = fmt.Errorf("docker: the holding of the platform's lease does not hold, so the turn does not start: %w", app.ErrUnheld)

// holds asks the holding whether the holding whose epoch ctx carries still
// holds: at Start, and again before the turn's container starts (Decision
// 5, step 5), as every sandbox's start needs. A refusal is logged, and never
// fences. A context without an epoch is a bug, refused and logged as one.
func (r *Runner) holds(ctx context.Context, t *app.Turn) error {
	epoch, err := holding.EpochFrom(ctx)
	if err != nil {
		r.log.ErrorContext(ctx, "turn without an epoch", "turn", t.ID)
		return err
	}
	if !r.p.Holding.Valid(epoch) {
		r.log.WarnContext(ctx, "holding refused", "epoch", epoch, "request", "turn", "turn", t.ID)
		return errUnheld
	}
	return nil
}

// turnIDForm is what a turn's id may hold to name its container,
// agentswarm-turn-<id>: the engine takes a container's name of
// [a-zA-Z0-9][a-zA-Z0-9_.-]+, and the prefix begins it.
var turnIDForm = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// checkID refuses a turn's id that cannot name its output file
// (output.CheckID) or its container.
func checkID(id string) error {
	if err := output.CheckID(id); err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	if !turnIDForm.MatchString(id) {
		return fmt.Errorf("docker: a turn's id names its container, which %q cannot", id)
	}
	return nil
}

// Stop ends at once the turns the agent runs in the run, as stop says. It
// never waits: each turn's launch, or its end, is reported once it ends.
func (r *Runner) Stop(run board.RunID, agent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tr := range r.turns {
		if tr.t.Run == run && tr.t.Agent == agent {
			r.stop(tr)
		}
	}
}

// StopAll ends at once every turn the runner has, as Stop does.
func (r *Runner) StopAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tr := range r.turns {
		r.stop(tr)
	}
}

// atDeadline stops a turn at its deadline, unless the runner forgot it.
func (r *Runner) atDeadline(tr *turn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turns[tr.t.ID] == tr {
		r.stop(tr)
	}
}

// stop ends a turn at once (Decision 5), as halt does, and sends a running
// turn's container SIGKILL, from a goroutine, on every stop, so that a kill
// that failed goes again. It never waits for the engine. The caller holds
// r.mu.
func (r *Runner) stop(tr *turn) {
	if tr.halt() {
		go r.kill(tr.base, tr.container)
	}
}

// KillAll is serve's last word to the engine, on each of its exits (Decision
// 5): it halts every turn the runner has, its launch cancelled, and
// force-removes, by name and in parallel, the container of each, launching,
// started or running; the engine orders a removal after a start in flight.
// It waits up to killAllWait for the engine's answers, so that the removals
// are sent before serve exits, whatever ctx's state: ctx gives them its
// values alone. A turn it halts ends as its launch, or its end, says.
func (r *Runner) KillAll(ctx context.Context) {
	r.mu.Lock()
	names := make([]string, 0, len(r.turns))
	for _, tr := range r.turns {
		tr.halt()
		names = append(names, containerName(tr.t.ID))
	}
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), killAllWait)
	defer cancel()
	var wg sync.WaitGroup
	for _, name := range names {
		wg.Go(func() { _ = r.e.Remove(ctx, name) })
	}
	wg.Wait()
}

// Close stops the minute's job, waiting at most closeWait for its pass in
// flight, and then releases the holder's lock and closes the engine's
// client, once (Contract C14). It kills nothing: serve calls it after
// KillAll, never before, since KillAll's removals need the client.
func (r *Runner) Close() error { return r.close() }

// forget forgets a turn: the runner no longer has it, its deadline's timer
// is stopped, and its launch's context released.
func (r *Runner) forget(tr *turn) {
	tr.timer.Stop()
	tr.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.turns[tr.t.ID] == tr {
		delete(r.turns, tr.t.ID)
	}
}
