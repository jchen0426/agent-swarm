package app_test

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	goruntime "runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The interleaving test's flags (The interleaving test; R46). The default run
// takes the default schedules from the base seed fixed in code, the same in
// every gate; a run over its budget lowers the counts, never the checks. The
// soak runs for a duration instead, from a base seed of the clock's unless
// -interleave.base gives one, half of it sequential and half concurrent.
var (
	interleaveSeed       = flag.Int64("interleave.seed", 0, "run this one schedule, in the test's mode, logging every event")
	interleaveBase       = flag.Int64("interleave.base", defaultBase, "the seed of the first schedule; each next schedule's is one more")
	interleaveSchedules  = flag.Int("interleave.schedules", defaultSchedules, "how many sequential schedules a run takes")
	interleaveConcurrent = flag.Int("interleave.concurrent", defaultConcurrent, "how many concurrent schedules a run takes")
	interleaveEvents     = flag.Int("interleave.events", defaultEvents, "how many events a schedule takes before it settles")
	interleaveFor        = flag.Duration("interleave.for", 0, "the soak: run schedules for this long, half sequential and half concurrent, from a base seed of the clock's unless -interleave.base gives one")
)

const (
	defaultBase       = 20261102
	defaultSchedules  = 200
	defaultConcurrent = 20
	defaultEvents     = 200
	// tailLines is how much of a failed schedule's log its failure prints.
	tailLines = 80
	// watchdogAfter is how long a sequential schedule may run in real time: a
	// bound of the test's, far above any correct schedule's tens of
	// milliseconds, even under -race on a loaded machine. concurrentBound is a
	// concurrent schedule's, past which it counts as one that deadlocks.
	watchdogAfter   = 60 * time.Second
	concurrentBound = 30 * time.Second
	// maxBatch is how many events a batch of the concurrent mode runs at once,
	// at most.
	maxBatch = 4
)

// The stages after which runSchedules checks a world: after each event, and at
// the end of each settling stage.
const (
	stageEvent   = "event"
	stageSettled = "settled"
	stageQuiet   = "quiet"
)

// seedFlag returns the seed -interleave.seed gives, and whether it gives one.
func seedFlag() (int64, bool) {
	return *interleaveSeed, flagSet("interleave.seed")
}

// flagSet reports whether the command line sets the flag name.
func flagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// driver is what the driver keeps of a schedule beside the world: its length
// and the events it drew; every event it logged; the items it opened, and the
// requests and keys it sent, so that it sends them again, and how many kill
// switch requests it sent; the database clock's steps forward, which its
// bound counts; what the batch it picks reserved; the write of the drain's on
// it holds; the leading coordinator's snapshots around the last event; and
// the world's first failure. known, held and failure are under w.mu, as the
// concurrent mode's events reach them from their goroutines.
type driver struct {
	length, drawn, logged int
	known                 []item.ID
	openings              []opening
	keys                  []actKey
	sent, switches        int
	forward               []dbStep
	batch                 batch
	held                  *heldOn
	published             app.Lifecycle
	before, after         app.Lifecycle
	failure               error
}

// batch is what the events picked for a batch reserved, so that no two of a
// concurrent batch's events clash: the runner's turns they report, and
// whether one passes time, steps the database's clock forward, or lets the
// drain's on write. A sequential batch holds one event.
type batch struct {
	turns     map[*runnerTurn]bool
	time      bool
	dbForward bool
	letGo     bool
}

// event is an event of a schedule: its line in the log, and what runs it.
type event struct {
	desc string
	run  func()
}

// opening is a keyed opening the driver sent: its item and its request's
// digest.
type opening struct {
	id     item.ID
	digest string
}

// actKey is a key the driver sent for an act on an item.
type actKey struct {
	id  item.ID
	act string
	key string
}

// dbStep is a step forward of the database's clock: when, on the monotonic
// clock, and by how much.
type dbStep struct {
	at time.Time
	d  time.Duration
}

// heldOn is the drain's on, recorded through RecordKillSwitchOn on a goroutine
// of its own, as the admin API's request records it, whose write the driver
// holds at the kill switch's store: its instance, its write, and the call's
// answer once it returns.
type heldOn struct {
	in    *instance
	write *heldWrite
	done  chan recorded
}

// The acts on items.
const (
	actClose   = "closes"
	actPromote = "promotes"
	actReopen  = "reopens"
)

// do runs an event: it logs desc, runs fn, lets go a take held mid-act, if
// any, once fn is done, and fires the timers due (see doBatch).
func (w *world) do(desc string, fn func()) {
	w.doBatch([]event{{desc, fn}})
}

// doBatch runs a batch of events: it logs each, runs them, at once, each on a
// goroutine of its own, when there are several, as the concurrent mode's
// batches do, or the one on the driver's goroutine; lets go a take held
// mid-act, if any, once they are done; and fires the timers due until nothing
// runs and none is due (see quiesce). It keeps for the checks the leading
// coordinator's snapshot before the batch, the last one a leader published,
// and after it, empty when no coordinator leads then, a fenced one's
// included; each live instance's snapshot around it; and the records
// committed since the snapshot before it: the batch's, and those of the
// batches after which no leader published one.
func (w *world) doBatch(events []event) {
	w.before = w.published
	views := map[*instance]app.Lifecycle{}
	for _, in := range w.live() {
		views[in] = in.coord.Lifecycle()
	}
	w.mu.Lock()
	if w.after.Limit > 0 {
		w.events = nil
	}
	for _, e := range events {
		w.logged++
		w.entries = append(w.entries, logEntry{event: w.logged, text: e.desc, at: w.mono})
	}
	w.mu.Unlock()
	if len(events) == 1 {
		events[0].run()
	} else {
		for _, e := range events {
			w.wg.Go(e.run)
		}
	}
	w.releaseTake()
	w.quiesce()
	w.after = app.Lifecycle{}
	if in := w.leading(); in != nil {
		if lc := in.coord.Lifecycle(); lc.Limit > 0 {
			w.published, w.after = lc, lc
		}
	}
	var after []instView
	for _, in := range w.live() {
		after = append(after, instView{in: in, before: views[in], after: in.coord.Lifecycle()})
	}
	w.mu.Lock()
	w.views = after
	w.mu.Unlock()
}

// quiesce waits until the batch's goroutines are done, then fires the timers
// due, and again, until nothing runs and no timer is due, or the world failed:
// a guard that failed it, as maxFires' on timers that fire without end, ends
// the event there, with its goroutines done.
func (w *world) quiesce() {
	for {
		w.wg.Wait()
		if w.failed() != nil || !w.fireDue() {
			return
		}
	}
}

// step picks the schedule's next event, or the concurrent mode's next batch
// of one to maxBatch events, among those enabled, by their weights, and runs
// it; it returns false, running none, once the schedule has run all its
// events, or the world failed. A batch ends before an event that runs alone.
func (w *world) step() bool {
	if w.drawn >= w.length || w.failed() != nil {
		return false
	}
	n := 1
	if w.mode == concurrent {
		n += w.rng.IntN(maxBatch)
	}
	w.batch = batch{turns: map[*runnerTurn]bool{}}
	var events []event
	for len(events) < n && w.drawn < w.length {
		solo, desc, run := w.pick(len(events) == 0)
		w.drawn++
		events = append(events, event{desc, run})
		if solo {
			break
		}
	}
	w.doBatch(events)
	return true
}

// eventKind is a kind of event a schedule picks among: its weight, whether
// it can run now, whether it runs alone, outside a concurrent batch, and how
// it picks its parameters, which gives the event's line and what runs it.
type eventKind struct {
	weight int
	ok     func(w *world) bool
	solo   bool
	pick   func(w *world) (string, func())
}

// eventKinds are the events a schedule picks among (The interleaving test):
// acts, the world's faults, the lease's and the instances' lifecycle, time
// passing and clock steps, rows written out of band, the day's spend and the
// registry's current revision, and the runner's reports. A stop, a crash and
// a boot run alone: a process gone, or replaced, takes no part in a batch's
// other events. A batch passes time once at most, steps the database's clock
// forward once at most, and lets the drain's on go once at most, and reports
// each turn once at most.
var eventKinds = []eventKind{
	{weight: 10, ok: (*world).actable, pick: (*world).pickOpen},
	{weight: 3, ok: (*world).anyOpening, pick: (*world).pickOpenAgain},
	{weight: 5, ok: (*world).anyKnown, pick: func(w *world) (string, func()) { return w.pickAct(actClose) }},
	{weight: 4, ok: (*world).anyKnown, pick: func(w *world) (string, func()) { return w.pickAct(actPromote) }},
	{weight: 3, ok: (*world).anyKnown, pick: func(w *world) (string, func()) { return w.pickAct(actReopen) }},
	{weight: 6, ok: (*world).actable, pick: (*world).pickSwitch},
	{weight: 6, ok: func(w *world) bool { return w.heldOn() != nil && !w.batch.letGo }, pick: func(w *world) (string, func()) {
		w.batch.letGo = true
		return "the driver lets the drain's on write", func() { w.letGo() }
	}},
	{weight: 6, ok: func(*world) bool { return true }, pick: (*world).pickFault},
	{weight: 2, ok: func(w *world) bool { return w.leading() != nil }, pick: (*world).pickLeaseLoss},
	{weight: 1, ok: func(w *world) bool { return w.leading() != nil }, pick: (*world).pickDrain},
	{weight: 3, ok: func(w *world) bool { return w.drainingWithoutWrite() != nil }, solo: true, pick: (*world).pickStop},
	{weight: 1, ok: func(w *world) bool { return len(w.crashable()) > 0 }, solo: true, pick: (*world).pickCrash},
	{weight: 4, ok: func(w *world) bool { return len(w.bootable()) > 0 }, solo: true, pick: (*world).pickBoot},
	{weight: 16, ok: func(w *world) bool { return !w.batch.time }, pick: (*world).pickTime},
	{weight: 4, ok: func(*world) bool { return true }, pick: (*world).pickStep},
	{weight: 2, ok: func(*world) bool { return true }, pick: (*world).pickRow},
	{weight: 2, ok: func(*world) bool { return true }, pick: (*world).pickSpend},
	{weight: 2, ok: func(*world) bool { return true }, pick: (*world).pickRevision},
	{weight: 10, ok: func(w *world) bool { return w.anyTurn(launchable) }, pick: (*world).pickLaunch},
	{weight: 10, ok: func(w *world) bool { return w.anyTurn(running) }, pick: (*world).pickEnd},
	{weight: 8, ok: func(w *world) bool { return w.anyTurn(stopped) }, pick: (*world).pickReport},
}

// pick draws an event among the kinds enabled now, by their weights, those
// that run alone only when alone says the batch has no event yet, and
// reports whether it runs alone.
func (w *world) pick(alone bool) (solo bool, desc string, run func()) {
	enabled := make([]*eventKind, 0, len(eventKinds))
	total := 0
	for i := range eventKinds {
		if k := &eventKinds[i]; (alone || !k.solo) && k.ok(w) {
			enabled = append(enabled, k)
			total += k.weight
		}
	}
	r := w.rng.IntN(total)
	for _, k := range enabled {
		if r < k.weight {
			desc, run = k.pick(w)
			return k.solo, desc, run
		}
		r -= k.weight
	}
	panic("the weights add up to their total")
}

// actable reports whether acts are sent now: an instance leads, or drains,
// as people act through the instance that leads; while none does, every act
// would answer not held, and the schedule spends its events elsewhere.
func (w *world) actable() bool    { return w.leading() != nil || w.drainingOne() != nil }
func (w *world) anyOpening() bool { return w.actable() && len(w.openings) > 0 }
func (w *world) anyKnown() bool   { return w.actable() && len(w.knownItems()) > 0 }

// knownItems returns the items the driver opened, or named by their keys.
func (w *world) knownItems() []item.ID {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.known)
}

// heldOn returns the drain's on whose write the driver holds, or nil.
func (w *world) heldOn() *heldOn {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.held
}

// drainingOne returns an instance that drains, or nil.
func (w *world) drainingOne() *instance {
	for _, in := range w.live() {
		if in.draining() {
			return in
		}
	}
	return nil
}

// target returns the instance an act goes to: the one that leads, or else the
// one that drains, almost always, or any live one, which may not hold the
// lease.
func (w *world) target() *instance {
	if w.rng.IntN(16) > 0 {
		if lead := w.leading(); lead != nil {
			return lead
		}
		if drain := w.drainingOne(); drain != nil {
			return drain
		}
	}
	live := w.live()
	return live[w.rng.IntN(len(live))]
}

// know notes an item the driver opened, or named by its key.
func (w *world) know(id item.ID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !slices.Contains(w.known, id) {
		w.known = append(w.known, id)
	}
}

// answer is an act's answer as the log holds it.
func answer(text string, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	return text
}

// openInput is an incident a person opens, of the checkout service.
func openInput() *app.OpenInput {
	return openInputOf("checkout")
}

// openInputOf is an incident a person opens, of service.
func openInputOf(service string) *app.OpenInput {
	return &app.OpenInput{Type: "incident", Title: service + " fails", Source: "person", Routing: map[string]string{"service": service}}
}

// services are the services an item opens for: payments, which a revision's
// rule redacts, so that an item opened before it is current is not staffed
// at its start for its routing.
var services = []string{"checkout", "checkout", "payments"}

// pickOpen opens an item, by key mostly, as the admin API names it from its
// request's Idempotency-Key (Decision 14), or with no key.
func (w *world) pickOpen() (desc string, run func()) {
	in, o := w.target(), openInputOf(services[w.rng.IntN(len(services))])
	if w.rng.IntN(3) == 0 {
		return fmt.Sprintf("%s opens an item of %s with no key", in.name, o.Routing["service"]), func() { w.open(in, o) }
	}
	w.sent++
	o.ID = item.ID(fmt.Sprintf("k%d", w.sent))
	o.Digest = "sha256:open-" + string(o.ID)
	w.openings = append(w.openings, opening{id: o.ID, digest: o.Digest})
	w.know(o.ID)
	return fmt.Sprintf("%s opens %s of %s", in.name, o.ID, o.Routing["service"]), func() { w.open(in, o) }
}

// pickOpenAgain sends a keyed opening again: its retry, with the same
// request, or another request under its key.
func (w *world) pickOpenAgain() (desc string, run func()) {
	in, o := w.target(), openInput()
	op := w.openings[w.rng.IntN(len(w.openings))]
	o.ID, o.Digest = op.id, op.digest
	how := "the same request"
	if w.rng.IntN(3) == 0 {
		o.Digest, how = "sha256:other-"+string(op.id), "another request"
	}
	return fmt.Sprintf("%s opens %s again, %s", in.name, op.id, how), func() { w.open(in, o) }
}

// open sends an opening to in, which a keyed one notes as sent under its key
// (invariant 6).
func (w *world) open(in *instance, o *app.OpenInput) {
	if o.ID != "" {
		w.noteOpening(o.ID, o.Digest)
	}
	id, err := in.coord.Open(context.Background(), o)
	if err == nil {
		w.know(id)
	}
	w.noteResult(answer("opened "+string(id), err))
}

// pickAct picks an act on an item the driver knows: a preview, which takes no
// key; an act under a key sent before, for this act or another, of this item
// or another; or one under a new key.
func (w *world) pickAct(act string) (desc string, run func()) {
	in := w.target()
	known := w.knownItems()
	id := known[w.rng.IntN(len(known))]
	a := &app.Act{Actor: administrator}
	var how string
	switch r := w.rng.IntN(8); {
	case r == 0:
		a.Preview, how = true, "previewed"
	case r == 1 && len(w.keys) > 0:
		k := w.keys[w.rng.IntN(len(w.keys))]
		a.Key, how = k.key, "again under "+k.key
	default:
		w.sent++
		a.Key = fmt.Sprintf("act-%d", w.sent)
		w.keys = append(w.keys, actKey{id: id, act: act, key: a.Key})
		how = "under " + a.Key
	}
	return fmt.Sprintf("%s %s %s, %s", in.name, act, id, how), func() { w.act(in, act, id, a) }
}

// act sends act on item id to in, which an act with a key notes as sent under
// it (invariant 6).
func (w *world) act(in *instance, act string, id item.ID, a *app.Act) {
	ctx := context.Background()
	if !a.Preview {
		w.noteSent(id, act, a.Key)
	}
	var out app.Outcome
	var err error
	switch act {
	case actClose:
		out, err = in.coord.Close(ctx, id, a)
	case actPromote:
		out, err = in.coord.Promote(ctx, id, a)
	default:
		out, err = in.coord.Reopen(ctx, id, a)
	}
	w.noteResult(answer(fmt.Sprintf("changed=%t %+v", out.Changed, out.Consequences), err))
}

// latestRow is the number of the kill switch's latest row, as the driver
// reads the table.
func (w *world) latestRow() int64 {
	seq, err := w.switchStore.LatestKillSwitch(context.Background())
	require.NoError(w.t, err)
	return seq
}

// pickSwitch turns the kill switch on or off, after the latest row, after a
// row that is not, or after what its own preview read; or previews it. Each
// request acts as an admin subject of its own, which names the commit its on
// came after (see observer.cameAt).
func (w *world) pickSwitch() (desc string, run func()) {
	in := w.target()
	on := w.rng.IntN(2) == 0
	what := map[bool]string{true: "on", false: "off"}[on]
	w.switches++
	a := &app.Act{Actor: app.Actor{Subject: fmt.Sprintf("admin-%d", w.switches), Admin: true}}
	if on {
		a.Reason = &board.Reason{Code: "incident"}
	}
	latest := w.latestRow()
	switch w.rng.IntN(5) {
	case 0:
		a.Preview = true
		return fmt.Sprintf("%s previews the kill switch %s", in.name, what), func() { w.setSwitch(in, on, a) }
	case 1:
		a.After = latest - 1
		if latest == 0 {
			a.After = 1
		}
		return fmt.Sprintf("%s turns the kill switch %s after %d, not the latest", in.name, what, a.After), func() { w.setSwitch(in, on, a) }
	case 2:
		return fmt.Sprintf("%s turns the kill switch %s after its preview", in.name, what), func() { w.previewThenSet(in, on, a) }
	}
	a.After = latest
	return fmt.Sprintf("%s turns the kill switch %s after %d", in.name, what, a.After), func() { w.setSwitch(in, on, a) }
}

// setSwitch sends a request to the kill switch. An on's success answer is
// noted for invariant 1. An on that a draining instance answers ErrNotHeld is
// recorded through RecordKillSwitchOn, as the admin API does, under the
// draining holding's epoch, its write held by the driver (see recordOn).
func (w *world) setSwitch(in *instance, on bool, a *app.Act) (app.KillSwitchOutcome, error) {
	out, err := in.coord.SetKillSwitch(context.Background(), on, a)
	w.noteResult(answer(fmt.Sprintf("on=%t seq=%d changed=%t stopped=%d unstaffed=%v owed=%v", out.On, out.Seq, out.Changed, len(out.Stopped), out.Unstaffed, out.Owed), err))
	switch {
	case !on || a.Preview:
	case err == nil && !out.Deferred:
		w.noteAnswer(in, a.Actor.Subject, &out)
	case errors.Is(err, app.ErrNotHeld) && in.draining():
		w.recordOn(in, a)
	}
	return out, err
}

// previewThenSet previews the request, then sends it after the row the
// preview read.
func (w *world) previewThenSet(in *instance, on bool, a *app.Act) {
	p, err := in.coord.SetKillSwitch(context.Background(), on, &app.Act{Actor: a.Actor, Preview: true})
	w.noteResult(answer(fmt.Sprintf("previewed on=%t seq=%d", p.On, p.Seq), err))
	if err != nil {
		return
	}
	a.After = p.Seq
	_, _ = w.setSwitch(in, on, a)
}

// recordOn records the drain's on through RecordKillSwitchOn, on a goroutine
// of its own, under the draining holding's epoch, its write held at the kill
// switch's store: the driver waits until the call blocks there, or returns
// (R35, the first condition). The driver holds one such write at most: an on
// while it holds one, as another of a concurrent batch's, is not recorded.
func (w *world) recordOn(in *instance, a *app.Act) {
	epoch, _, _ := in.leader.Status()
	h := &heldOn{in: in, write: &heldWrite{reached: make(chan struct{}), release: make(chan struct{})}, done: make(chan recorded, 1)}
	w.mu.Lock()
	if w.held != nil {
		w.mu.Unlock()
		return
	}
	w.held, in.holdAt = h, h.write
	w.mu.Unlock()
	w.noteResult(fmt.Sprintf("the drain's on is recorded under epoch %d", epoch))
	go func() {
		out, err := in.coord.RecordKillSwitchOn(context.Background(), epoch, a)
		h.done <- recorded{out: out, err: err}
	}()
	select {
	case <-h.write.reached:
		w.noteResult("its write is held")
	case r := <-h.done:
		w.mu.Lock()
		w.held, in.holdAt = nil, nil
		w.mu.Unlock()
		w.noteRecorded(&r)
	}
}

// letGo lets the held write of the drain's on go, waits until its call
// returns (R35, the first condition), and returns its answer.
func (w *world) letGo() recorded {
	w.mu.Lock()
	h := w.held
	w.held = nil
	w.mu.Unlock()
	close(h.write.release)
	r := <-h.done
	w.noteRecorded(&r)
	return r
}

func (w *world) noteRecorded(r *recorded) {
	w.noteResult(answer(fmt.Sprintf("recorded on=%t seq=%d changed=%t deferred=%t", r.out.On, r.out.Seq, r.out.Changed, r.out.Deferred), r.err))
}

// pickFault arms a fault of a store's, or a renewal's.
func (w *world) pickFault() (desc string, run func()) {
	f := fault(w.rng.IntN(int(faultLeaseExpires)))
	return "fault: " + faultNames[f], func() { w.armFault(f) }
}

// pickLeaseLoss arms the lease's loss in the middle of an act: its expiry, or
// the other instance's take after it, before the next guarded write, or
// after one or two more, so that it lands between two writes of one act too.
func (w *world) pickLeaseLoss() (desc string, run func()) {
	f := faultLeaseExpires
	if w.rng.IntN(2) == 0 {
		f = faultLeaseTaken
	}
	skip := w.rng.IntN(3)
	return fmt.Sprintf("fault: %s, after %d guarded writes", faultNames[f], skip), func() { w.armLeaseLoss(f, skip) }
}

func (w *world) pickDrain() (desc string, run func()) {
	in := w.leading()
	return in.name + " drains", in.leader.Drain
}

// drainingWithoutWrite returns a draining instance whose drain's on the
// driver does not hold, which may stop, or nil.
func (w *world) drainingWithoutWrite() *instance {
	held := w.heldOn()
	for _, in := range w.live() {
		if in.draining() && (held == nil || held.in != in) {
			return in
		}
	}
	return nil
}

func (w *world) pickStop() (desc string, run func()) {
	in := w.drainingWithoutWrite()
	return in.name + " stops", func() {
		in.leader.Stop(context.Background())
		w.discard(in)
	}
}

// crashable returns the live instances whose drain's on the driver does not
// hold.
func (w *world) crashable() []*instance {
	held := w.heldOn()
	var out []*instance
	for _, in := range w.live() {
		if held == nil || held.in != in {
			out = append(out, in)
		}
	}
	return out
}

func (w *world) pickCrash() (desc string, run func()) {
	ins := w.crashable()
	in := ins[w.rng.IntN(len(ins))]
	return in.name + " crashes", func() { w.discard(in) }
}

// bootable returns the slots with no live instance, or whose instance's
// leader stopped for good, as when a drain's holding was lost, but for the
// instance whose drain's on the driver holds: a boot discards the instance
// it replaces, and the held write must not return to a discarded one.
func (w *world) bootable() []int {
	held := w.heldOn()
	var out []int
	for slot := range 2 {
		in := w.inst(slot)
		if in == nil || in.stopped() && (held == nil || held.in != in) {
			out = append(out, slot)
		}
	}
	return out
}

func (w *world) pickBoot() (desc string, run func()) {
	slots := w.bootable()
	slot := slots[w.rng.IntN(len(slots))]
	return fmt.Sprintf("an instance boots on host %c", 'a'+slot), func() { w.reboot(slot) }
}

// reboot boots an instance on the host of slot, in place of one whose leader
// stopped.
func (w *world) reboot(slot int) {
	if old := w.inst(slot); old != nil {
		w.discard(old)
	}
	w.boot(slot)
}

// passings are how long time passes in an event, the short more often.
var passings = []time.Duration{
	time.Second, time.Second, 2 * time.Second, 5 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute,
}

func (w *world) pickTime() (desc string, run func()) {
	w.batch.time = true
	d := passings[w.rng.IntN(len(passings))]
	return "time passes " + d.String(), func() { w.passTime(d) }
}

// The clock steps (R39): the database's clock forward by less than the
// lease's margin in all within a lease's term, the premise of the lease, and
// every other step up to 10 minutes either way, a bound of the test's.
var (
	dbForward = []time.Duration{time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second}
	steps     = []time.Duration{time.Second, 10 * time.Second, time.Minute, 5 * time.Minute, 10 * time.Minute}
)

// forwardRoom is how far the database's clock may step forward now: less than
// the lease's margin, less its steps forward within the last lease's term.
// No other step forward runs in the batch: a batch takes one at most, and its
// time passing, or the lease's loss mid-act, only frees room.
func (w *world) forwardRoom() time.Duration {
	now := w.monoNow()
	room := worldMargin
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.forward {
		if now.Sub(s.at) < worldTTL {
			room -= s.d
		}
	}
	return room
}

// pickStep steps the database's clock, or a host's wall clock, forward or
// back.
func (w *world) pickStep() (desc string, run func()) {
	slot := w.rng.IntN(3) - 1 // -1 names the database's clock
	d := steps[w.rng.IntN(len(steps))]
	if w.rng.IntN(2) == 0 {
		d = -d
	}
	if slot < 0 && d > 0 {
		room := w.forwardRoom()
		allowed := slices.DeleteFunc(slices.Clone(dbForward), func(s time.Duration) bool { return s >= room })
		if len(allowed) == 0 || w.batch.dbForward {
			d = -d
		} else {
			d = allowed[w.rng.IntN(len(allowed))]
			w.batch.dbForward = true
		}
	}
	name := "the database's clock"
	if slot >= 0 {
		name = fmt.Sprintf("host %c's wall clock", 'a'+slot)
	}
	return fmt.Sprintf("%s steps %s", name, d), func() { w.stepClock(slot, d) }
}

// stepClock steps the database's clock, for slot -1, or the wall clock of the
// host of slot, by d: it moves no timer, and no monotonic time.
func (w *world) stepClock(slot int, d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if slot >= 0 {
		w.wallOffset[slot] += d
		return
	}
	w.dbOffset += d
	if d > 0 {
		w.forward = append(w.forward, dbStep{at: w.mono, d: d})
	}
}

// pickRow writes a row of the kill switch out of band, on or off, as a psql
// session, a restore or a writer that crashed would leave it.
func (w *world) pickRow() (desc string, run func()) {
	on := w.rng.IntN(2) == 0
	return fmt.Sprintf("the driver writes a kill switch row out of band, on=%t", on), func() { w.writeRow(on) }
}

// writeRow writes the row after the latest the driver reads, which, in a
// concurrent batch, a coordinator's row may follow first: the request is then
// stale, and writes nothing.
func (w *world) writeRow(on bool) {
	ctx := context.Background()
	latest, err := w.switchStore.LatestKillSwitch(ctx)
	if err != nil {
		w.fail("read the kill switch's latest row: %v", err)
		return
	}
	state, changed, err := w.switchStore.SetKillSwitch(ctx, on, outOfBand, board.Reason{Code: "restore"}, latest)
	switch {
	case errors.Is(err, app.ErrStale):
		w.noteResult(fmt.Sprintf("row %d on=%t stands: the row is not written", state.Seq, state.On))
		return
	case err != nil:
		w.fail("write a kill switch row out of band: %v", err)
		return
	}
	if changed {
		w.row(&state, true)
	}
	w.noteResult(fmt.Sprintf("row %d on=%t changed=%t", state.Seq, state.On, changed))
}

// pickSpend has the day's spend reach the daily cap, or, three times as
// often, a new day begin, whose spend is zero, so that the cap stands a
// quarter of the time, and the queue forms in the rest.
func (w *world) pickSpend() (desc string, run func()) {
	if w.rng.IntN(4) == 0 {
		return "the day's spend reaches the daily cap", func() { w.setCapped(true) }
	}
	return "a new day begins, its spend zero", func() { w.setCapped(false) }
}

// setCapped says whether the day's spend reached the daily cap.
func (w *world) setCapped(capped bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.capped = capped
}

// currents are the revisions the driver makes current, the base one as often
// as the others together, so that it stands half the time: under the one
// whose type reads its key, no item opens.
var currents = []int{revBase, revBase, revRedactsPayments, revReadsItsKey}

// pickRevision makes a revision of the registry's current.
func (w *world) pickRevision() (desc string, run func()) {
	rev := currents[w.rng.IntN(len(currents))]
	return "the registry's current revision becomes " + revNames[rev], func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.current = rev
	}
}

// The runner's reports.

func launchable(rt *runnerTurn) bool { return rt.launching && !rt.stopped }
func running(rt *runnerTurn) bool    { return !rt.launching && !rt.stopped }
func stopped(rt *runnerTurn) bool    { return rt.stopped }

// anyTurn reports whether a live instance's runner has a turn that passes
// keep, and that no event of the batch reports already.
func (w *world) anyTurn(keep func(*runnerTurn) bool) bool {
	_, rts := w.runnerTurns(w.unreserved(keep))
	return len(rts) > 0
}

// pickTurn picks a turn that passes keep, and that no event of the batch
// reports already, and its instance, and reserves the turn for the event.
func (w *world) pickTurn(keep func(*runnerTurn) bool) (*instance, *runnerTurn) {
	ins, rts := w.runnerTurns(w.unreserved(keep))
	i := w.rng.IntN(len(rts))
	w.batch.turns[rts[i]] = true
	return ins[i], rts[i]
}

// unreserved passes the turns that pass keep, and that no event of the batch
// reports already: a runner reports a turn's end once.
func (w *world) unreserved(keep func(*runnerTurn) bool) func(*runnerTurn) bool {
	return func(rt *runnerTurn) bool { return keep(rt) && !w.batch.turns[rt] }
}

// launchReasons are how a launch ends when the driver reports it: it runs,
// most often, or fails for a reason of the engine's, the image's or another.
var launchReasons = []app.LaunchReason{"", "", "", app.ReasonEngine, app.ReasonImage, app.ReasonLaunch}

func (w *world) pickLaunch() (desc string, run func()) {
	in, rt := w.pickTurn(launchable)
	reason := launchReasons[w.rng.IntN(len(launchReasons))]
	return fmt.Sprintf("%s's runner: the launch of %s %s ends: %s", in.name, rt.turn.Run, rt.turn.Agent, cmp.Or(string(reason), "it runs")),
		func() { w.launch(in, rt, reason) }
}

// turnClasses are the classes the runner reports a turn's end as.
var turnClasses = []item.TurnClass{
	item.TurnEnded, item.TurnFinished, item.TurnInterrupted, item.TurnRefused, item.TurnVendor, item.TurnUnavailable, item.TurnCrashed,
}

func (w *world) pickEnd() (desc string, run func()) {
	in, rt := w.pickTurn(running)
	class := turnClasses[w.rng.IntN(len(turnClasses))]
	onBoard := w.rng.IntN(2) == 0
	return fmt.Sprintf("%s's runner: the turn of %s %s ends %s, its turn end on the board: %t", in.name, rt.turn.Run, rt.turn.Agent, class, onBoard),
		func() { w.end(in, rt, class, onBoard) }
}

// stopReportsDrawn are how the runner reports a pending launch a stop ended,
// cancelled most often.
var stopReportsDrawn = []stopReport{reportCancelled, reportCancelled, reportFailed, reportRan}

func (w *world) pickReport() (desc string, run func()) {
	in, rt := w.pickTurn(stopped)
	how := stopReportsDrawn[w.rng.IntN(len(stopReportsDrawn))]
	return fmt.Sprintf("%s's runner reports the stopped turn of %s %s, a pending launch %s", in.name, rt.turn.Run, rt.turn.Agent, stopReports[how]),
		func() { w.reportStop(in, rt, how) }
}

// The settling stages.

// settleKillSwitch is the first settling stage (The interleaving test), one
// event: with faults and steps off, and a leader holding, time passes by the
// retry wait and two renewal periods while the runner reports nothing. One
// holding spans that time, the same leader's under the same epoch from its
// start to its end, so that the coordinator catches up with the table: a
// holding that ends in it, as when a renewal that failed before the faults
// went off lets the leader's watch end it, starts the span again, within the
// settling's bound.
func (w *world) settleKillSwitch() {
	d := worldRetry + 2*worldRenewEvery
	w.do("settle: faults and steps off, and time passes "+d.String()+" under one leader's holding, the runner reporting nothing", func() {
		until := w.monoNow().Add(settleBound)
		for {
			w.lead()
			in := w.leading()
			if w.failed() != nil || in == nil {
				return
			}
			epoch, _, _ := in.leader.Status()
			w.passTime(d)
			if w.failed() != nil {
				return
			}
			if now := w.leading(); now == in {
				if e, _, _ := now.leader.Status(); e == epoch {
					return
				}
			}
			if !w.monoNow().Before(until) {
				w.fail("no leader held the lease for %s in a row within %s", d, settleBound)
				return
			}
		}
	})
}

// settleQuiet is the second: the runner answers every launch and ends every
// turn at once, and time passes, timer by timer, until no coordinator timer is
// armed while a leader holds the lease: a holding that ends meanwhile is
// taken again first, and the takeover carries the items on.
func (w *world) settleQuiet() {
	w.do("settle: the runner answers at once, and time passes until no coordinator timer is armed under a leader's holding", func() {
		until := w.monoNow().Add(settleBound)
		for fired := 0; ; fired++ {
			w.answerAll()
			if w.failed() != nil {
				return
			}
			if !w.coordinatorArmed() {
				if w.leading() != nil {
					return
				}
				w.lead()
				continue
			}
			if fired == maxFires || !w.fireNext(until) {
				w.fail("the schedule does not settle: its coordinators' timers are still armed after %s", settleBound)
				return
			}
		}
	})
}

// lead turns the faults off, lets the drain's on write, and boots an instance
// on each host that has none, or whose leader stopped or drains, which stops;
// then time passes until a leader holds the lease, within the settling's
// bound, or the world failed, as when timers fire without end.
func (w *world) lead() {
	w.disarm()
	if w.heldOn() != nil {
		w.letGo()
	}
	for slot := range 2 {
		switch in := w.inst(slot); {
		case in == nil:
			w.boot(slot)
		case in.draining():
			in.leader.Stop(context.Background())
			w.reboot(slot)
		case in.stopped():
			w.reboot(slot)
		}
	}
	until := w.monoNow().Add(settleBound)
	for w.leading() == nil && w.failed() == nil {
		if !w.monoNow().Before(until) {
			w.fail("no leader holds the lease after %s", settleBound)
			return
		}
		w.passTime(worldTakeEvery)
	}
}

// answerAll has the runners answer every launch, end every turn, ended, with
// its turn end on the board, and report every turn a stop ended, as
// cancelled for a pending launch, at once: in the concurrent mode, each from
// a goroutine of its own.
func (w *world) answerAll() {
	for range 16 {
		ins, rts := w.runnerTurns(func(*runnerTurn) bool { return true })
		if len(rts) == 0 {
			return
		}
		var reports sync.WaitGroup
		for i, rt := range rts {
			answer := func() {
				switch launching, stopped, ok := w.turnState(ins[i], rt); {
				case !ok:
				case stopped:
					w.reportStop(ins[i], rt, reportCancelled)
				case launching:
					w.launch(ins[i], rt, "")
				default:
					w.end(ins[i], rt, item.TurnEnded, true)
				}
			}
			if w.mode == concurrent {
				reports.Go(answer)
			} else {
				answer()
			}
		}
		reports.Wait()
	}
	w.fail("the runner's turns never end")
}

// turnState returns whether rt launches and whether a stop ended it, and
// whether in's runner still has it.
func (w *world) turnState(in *instance, rt *runnerTurn) (launching, stopped, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return rt.launching, rt.stopped, !in.gone && slices.Contains(in.turns, rt)
}

// The runs.

// replayCommand is the command that replays the schedule of seed in test.
func replayCommand(test string, seed int64) string {
	return fmt.Sprintf("go test -race -count=1 -run '^%s$' ./internal/work/app/ -args -interleave.seed=%d", test, seed)
}

// watchdog bounds a schedule's real time. The sequential mode waits at its
// hand-offs without a bound, and the concurrent mode for its goroutines, so a
// schedule that deadlocks would hang until go test's timeout, which names no
// seed: once after has passed, the watchdog prints every goroutine's stack,
// then panics with the seed and the command that replays it, as no goroutine
// but the test's may fail the test, and the test's waits. stop disarms it.
func watchdog(t *testing.T, seed int64, after time.Duration) (stop func()) {
	t.Helper()
	test := t.Name()
	timer := time.AfterFunc(after, func() {
		stacks := make([]byte, 1<<22)
		stacks = stacks[:goruntime.Stack(stacks, true)]
		fmt.Fprintf(os.Stderr, "schedule %d did not finish within %s; every goroutine:\n%s\n", seed, after, stacks)
		panic(fmt.Sprintf("schedule %d did not finish within %s of real time, as one that deadlocks; replay it: %s", seed, after, replayCommand(test, seed)))
	})
	return func() { timer.Stop() }
}

// runSchedules runs the flags' schedules in mode m, calling check after each
// event, or batch, and at the end of each settling stage: the default run's
// count of the mode's schedules from the base seed; or, for the soak, as many
// as half of -interleave.for allows, from a base seed of the clock's unless
// -interleave.base gives one. It logs the base seed. A failure reports the
// schedule's seed, the tail of its log, the invariant it broke, and the
// command that replays it, which -interleave.seed runs, in the test's mode,
// logging every event: in the sequential mode, event for event; in the
// concurrent mode, its choice of events and faults, not their timing, so that
// a concurrent failure is chased by running the seed many times (-count), or
// by writing its order as a sequential schedule (R35).
func runSchedules(t *testing.T, m mode, check func(w *world, stage string) error) {
	t.Helper()
	if seed, ok := seedFlag(); ok {
		w, err := runSchedule(t, seed, m, check)
		t.Logf("schedule %d:\n%s", seed, strings.Join(w.log(), "\n"))
		if err != nil {
			t.Fatalf("schedule %d broke: %v", seed, err)
		}
		return
	}
	base, count := *interleaveBase, *interleaveSchedules
	if m == concurrent {
		count = *interleaveConcurrent
	}
	var until time.Time
	if *interleaveFor > 0 {
		until = time.Now().Add(*interleaveFor / 2)
		if !flagSet("interleave.base") {
			base = time.Now().UnixNano()
		}
	}
	t.Logf("base seed %d; replay one schedule with: %s", base, replayCommand(t.Name(), base))
	ran := 0
	for i := 0; until.IsZero() && i < count || !until.IsZero() && time.Now().Before(until); i++ {
		seed := base + int64(i)
		if w, err := runSchedule(t, seed, m, check); err != nil {
			lines := w.log()
			how := "replay it"
			if m == concurrent {
				how = "run its choice of events again, not its timing, as often as it takes (-count)"
			}
			t.Fatalf("schedule %d broke: %v\nthe tail of its log:\n%s\n%s: %s",
				seed, err, strings.Join(lines[max(0, len(lines)-tailLines):], "\n"), how, replayCommand(t.Name(), seed))
		}
		ran++
	}
	t.Logf("%d schedules from base seed %d kept the invariants", ran, base)
}

// runSchedule runs the schedule of seed to its end, then through both
// settling stages, checking after each event, or batch, and each stage,
// under the watchdog, whose bound is the concurrent mode's in that mode.
func runSchedule(t *testing.T, seed int64, m mode, check func(w *world, stage string) error) (*world, error) {
	t.Helper()
	bound := watchdogAfter
	if m == concurrent {
		bound = concurrentBound
	}
	defer watchdog(t, seed, bound)()
	w := newWorld(t, seed, m)
	w.length = *interleaveEvents
	checked := func(stage string) error {
		if err := w.failed(); err != nil {
			return err
		}
		return check(w, stage)
	}
	for w.step() {
		if err := checked(stageEvent); err != nil {
			return w, err
		}
	}
	if err := w.failed(); err != nil {
		return w, err
	}
	w.settleKillSwitch()
	if err := checked(stageSettled); err != nil {
		return w, err
	}
	w.settleQuiet()
	return w, checked(stageQuiet)
}

// The default run's sequential schedules, with clock steps both ways among
// their events, keep the nine invariants, each where its line says, after
// every event and at the end of each settling stage, and none deadlocks or
// fails to settle; a failure prints its seed, the invariant and the command
// that replays it. It replaces Task 17's run of invariant 5, so that each
// gate runs the default schedules once (R46). With -interleave.for, the soak
// runs it for half that time, from a base seed of the clock's.
func TestRandomSchedulesKeepTheLifecyclesInvariants(t *testing.T) {
	runSchedules(t, sequential, lifecycleInvariants)
}

// The default run's concurrent schedules keep the nine invariants at the end
// of each batch of events and at the end of each settling stage, under -race,
// and none deadlocks: a schedule that has not settled within its real-time
// bound fails, printing its seed and every goroutine's stack. Its seed
// replays its choice of events and faults, not their timing (R35). With
// -interleave.for, the soak runs it for half that time.
func TestConcurrentSchedulesKeepTheLifecyclesInvariants(t *testing.T) {
	runSchedules(t, concurrent, lifecycleInvariants)
}

// replayBase is the first seed TestASeedReplaysItsSchedule replays,
// replaySeeds how many it replays, replayEvents its schedules' length, and
// replayRuns how many times it runs each.
const (
	replayBase   = 7_000
	replaySeeds  = 20
	replayEvents = 50
	replayRuns   = 4
)

// scheduleLog runs the sequential schedule of seed, of n events and its
// settling, and returns its log.
func scheduleLog(t *testing.T, seed int64, n int) []string {
	t.Helper()
	defer watchdog(t, seed, watchdogAfter)()
	w := newWorld(t, seed, sequential)
	w.length = n
	for w.step() {
	}
	w.settleKillSwitch()
	w.settleQuiet()
	if err := w.failed(); err != nil {
		w.noteResult("the schedule failed: " + err.Error())
	}
	return w.log()
}

// A seed replays its schedule in the sequential mode (R35): twenty seeds, each
// run four times on schedules of 50 events and their settling, give equal logs
// of events and port calls, event for event, and two different seeds give
// different logs. Fails first, once the world exists, while a loop of the
// coordinator reaches a port in a map's order (R33), on most runs: a map of a
// round's three turns, as Go iterates maps of eight entries or fewer, comes
// back out of order once in four, so each seed runs four times. The
// coordinator's tests of each such loop, over fourteen agents, fail on every
// run (TestTheDeadlineStopsTheRunningTurnsInTheOrderOfTheirAgents and those
// beside it).
func TestASeedReplaysItsSchedule(t *testing.T) {
	for i := range replaySeeds {
		seed := replayBase + int64(i)
		first := scheduleLog(t, seed, replayEvents)
		for range replayRuns - 1 {
			again := scheduleLog(t, seed, replayEvents)
			at := 0
			for at < min(len(first), len(again)) && first[at] == again[at] {
				at++
			}
			if at < len(first) || at < len(again) {
				from := max(0, at-30)
				t.Fatalf("schedule %d did not replay: its logs part at line %d\nthe first run:\n%s\nanother:\n%s",
					seed, at, strings.Join(first[from:min(len(first), at+5)], "\n"), strings.Join(again[from:min(len(again), at+5)], "\n"))
			}
		}
	}
	assert.NotEqual(t, scheduleLog(t, replayBase, replayEvents), scheduleLog(t, replayBase+1, replayEvents), "two seeds give two schedules")
}

// The fakes' table: each fault on a short schedule written by hand.

// opened opens an incident on in, as a person does, and returns it.
func (w *world) opened(in *instance) item.ID {
	var id item.ID
	var err error
	w.do(in.name+" opens an item", func() { id, err = in.coord.Open(context.Background(), openInput()) })
	require.NoError(w.t, err)
	return id
}

// kinds lists an item's records by kind.
func (w *world) kinds(id item.ID) []item.Kind {
	records := w.records()[id]
	out := make([]item.Kind, 0, len(records))
	for i := range records {
		out = append(out, records[i].Body.Kind())
	}
	return out
}

// runOf returns the item's run.
func (w *world) runOf(id item.ID) board.RunID {
	records := w.records()[id]
	for i := range records {
		if records[i].Body.RunCreated != nil {
			return records[i].Run
		}
	}
	w.t.Fatalf("item %s has no run", id)
	return ""
}

// boardOf returns a run's board.
func (w *world) boardOf(run board.RunID) []board.Event {
	events, err := w.eventStore.Load(context.Background(), run)
	require.NoError(w.t, err)
	return events
}

// evidenceOn counts the evidence entries on a run's board.
func (w *world) evidenceOn(run board.RunID) int {
	n := 0
	events := w.boardOf(run)
	for i := range events {
		if events[i].Body.Evidence != nil {
			n++
		}
	}
	return n
}

// callIndex returns the index, from from on, of the first port call that
// matches c's instance, port and method, and its item when c names one, or -1.
func callIndex(calls []portCall, from int, c *portCall) int {
	for i := from; i < len(calls); i++ {
		if k := &calls[i]; k.Inst == c.Inst && k.Port == c.Port && k.Method == c.Method && (c.Item == "" || k.Item == c.Item) {
			return i
		}
	}
	return -1
}

// held reports whether the instance's leader holds the lease.
func held(in *instance) bool {
	_, h, _ := in.leader.Status()
	return h
}

// The world's fakes inject each fault the driver arms, each on a short
// schedule written by hand, not drawn (The interleaving test). Fails first:
// a build failure, no world.
func TestTheInterleavingFakesInjectEachFault(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		run  func(t *testing.T, w *world, a, b *instance)
	}{
		{"a records append that commits, then loses its answer, leaves its record, and the act carries on as its repeat", func(t *testing.T, w *world, a, _ *instance) {
			id := w.opened(a)
			w.armFault(faultRecordsLost)
			var out app.Outcome
			var err error
			w.do("a1 closes it", func() { out, err = a.coord.Close(ctx, id, closing("close-1")) })
			require.NoError(t, err)
			assert.True(t, out.Changed, "the close answers changed")
			assert.Empty(t, out.Consequences.Closes, "it answers as a repeat, found by its key (Decision 13)")
			assert.Equal(t, 1, kindCount(w.kinds(id), item.KindClosed), "its record is left, once")
		}},
		{"a records append that fails before its commit leaves no record, and the act answers the error", func(t *testing.T, w *world, a, _ *instance) {
			id := w.opened(a)
			w.armFault(faultRecordsFail)
			var err error
			w.do("a1 closes it", func() { _, err = a.coord.Close(ctx, id, closing("close-1")) })
			require.ErrorIs(t, err, errBlinked)
			assert.Zero(t, kindCount(w.kinds(id), item.KindClosed), "no close is recorded")
		}},
		{"a board append that commits, then loses its answer, leaves its event, which the step's retry finds", func(t *testing.T, w *world, a, _ *instance) {
			w.armFault(faultEventsLost)
			id := w.opened(a)
			run := w.runOf(id)
			assert.Equal(t, 1, w.evidenceOn(run), "the opening's evidence is on the board")
			assert.NotContains(t, w.kinds(id), item.KindRoundOpened, "the step failed, and opened no round")
			w.do("time passes the retry wait", func() { w.passTime(worldRetry) })
			assert.Contains(t, w.kinds(id), item.KindRoundOpened, "the retry opens the round")
			assert.Equal(t, 1, w.evidenceOn(run), "the retry's post found the evidence there, and posted it once")
		}},
		{"a board append that fails before its commit leaves no event, and the step answers the error, then posts it again", func(t *testing.T, w *world, a, _ *instance) {
			w.armFault(faultEventsFail)
			id := w.opened(a)
			run := w.runOf(id)
			assert.Zero(t, w.evidenceOn(run), "no evidence is on the board")
			assert.NotContains(t, w.kinds(id), item.KindRoundOpened)
			w.do("time passes the retry wait", func() { w.passTime(worldRetry) })
			assert.Contains(t, w.kinds(id), item.KindRoundOpened)
			assert.Equal(t, 1, w.evidenceOn(run))
		}},
		{"a kill switch write that commits, then loses its answer, leaves its row, and the act reads it again", func(t *testing.T, w *world, a, _ *instance) {
			w.armFault(faultSwitchLost)
			var out app.KillSwitchOutcome
			var err error
			w.do("a1 turns the kill switch on", func() { out, err = w.setSwitch(a, true, killing(0)) })
			require.NoError(t, err)
			assert.True(t, out.On)
			assert.True(t, out.Changed, "the read after the write finds its row")
			assert.Equal(t, int64(1), w.latestRow())
			assert.True(t, a.coord.Lifecycle().KillSwitch.On)
		}},
		{"a kill switch write that fails before its commit leaves no row, and the act answers the error", func(t *testing.T, w *world, a, _ *instance) {
			w.armFault(faultSwitchFail)
			var err error
			w.do("a1 turns the kill switch on", func() { _, err = w.setSwitch(a, true, killing(0)) })
			require.ErrorIs(t, err, errBlinked)
			assert.Zero(t, w.latestRow())
			assert.False(t, a.coord.Lifecycle().KillSwitchUnsettled, "the read after the write settles the view")
		}},
		{"a failed read of the kill switch, after a write whose answer was lost, leaves the view unsettled until the read after the retry wait", func(t *testing.T, w *world, a, _ *instance) {
			w.armFault(faultSwitchLost)
			w.armFault(faultSwitchRead)
			var err error
			w.do("a1 turns the kill switch on", func() { _, err = w.setSwitch(a, true, killing(0)) })
			require.ErrorIs(t, err, errAnswerLost)
			assert.True(t, a.coord.Lifecycle().KillSwitchUnsettled)
			w.do("time passes the retry wait", func() { w.passTime(worldRetry) })
			lc := a.coord.Lifecycle()
			assert.False(t, lc.KillSwitchUnsettled)
			assert.True(t, lc.KillSwitch.On)
		}},
		{"the other instance's take between an act's start and its first guarded write refuses the write, which fences the coordinator, before the takeover acts", func(t *testing.T, w *world, a, b *instance) {
			id := w.opened(a)
			w.armFault(faultLeaseTaken)
			from := len(w.calls())
			var err error
			w.do("a1 closes it", func() { _, err = a.coord.Close(ctx, id, closing("close-1")) })
			require.ErrorIs(t, err, app.ErrNotHeld)
			assert.Zero(t, kindCount(w.kinds(id), item.KindClosed))
			calls := w.calls()
			take := callIndex(calls, from, &portCall{Inst: "b1", Port: "lease", Method: "Take"})
			refused := callIndex(calls, from, &portCall{Inst: "a1", Port: "records", Method: "refused", Item: id})
			told := callIndex(calls, max(refused, 0), &portCall{Inst: "a1", Port: "timers", Method: "At"})
			acts := callIndex(calls, from, &portCall{Inst: "b1", Port: "sweeper", Method: "Sweep"})
			assert.True(t, take >= 0 && take < refused && refused < told && told < acts,
				"b's take lands before a's write, which fences a, whose coordinator tells its leader, before b's takeover acts: %d %d %d %d", take, refused, told, acts)
			assert.False(t, held(a), "a's leader holds no more")
			assert.Equal(t, app.Lifecycle{}, a.coord.Lifecycle(), "a's coordinator is fenced")
			assert.Same(t, b, w.leading())
		}},
		{"the lease's expiry between an act's start and its first guarded write refuses the write, which fences the coordinator", func(t *testing.T, w *world, a, b *instance) {
			id := w.opened(a)
			w.armFault(faultLeaseExpires)
			from := len(w.calls())
			var err error
			w.do("a1 closes it", func() { _, err = a.coord.Close(ctx, id, closing("close-1")) })
			require.ErrorIs(t, err, app.ErrNotHeld)
			assert.Zero(t, kindCount(w.kinds(id), item.KindClosed))
			calls := w.calls()
			refused := callIndex(calls, from, &portCall{Inst: "a1", Port: "records", Method: "refused", Item: id})
			told := callIndex(calls, max(refused, 0), &portCall{Inst: "a1", Port: "timers", Method: "At"})
			take := callIndex(calls, from, &portCall{Inst: "b1", Port: "lease", Method: "Take"})
			assert.True(t, refused >= 0 && refused < told && told < take, "the write is refused, and a's coordinator tells its leader, before b takes: %d %d %d", refused, told, take)
			assert.False(t, held(a), "a's leader holds no more")
			assert.Equal(t, app.Lifecycle{}, a.coord.Lifecycle())
			assert.Same(t, b, w.leading(), "b's take, which was due, takes the lease once it expired")
		}},
		{"a failed renewal lets the leader's watch fence the coordinator at the lease's time less its margin", func(t *testing.T, w *world, a, _ *instance) {
			w.armFault(faultRenewal)
			w.do("time passes to just before the lease's time less its margin", func() { w.passTime(worldTTL - worldMargin - time.Millisecond) })
			assert.True(t, held(a))
			assert.Equal(t, worldLimit, a.coord.Lifecycle().Limit, "a's coordinator acts")
			w.do("time passes to the lease's time less its margin", func() { w.passTime(time.Millisecond) })
			assert.False(t, held(a))
			assert.Equal(t, app.Lifecycle{}, a.coord.Lifecycle(), "a's coordinator is fenced")
			w.do("time passes to just before the lease expires", func() { w.passTime(worldMargin - time.Millisecond) })
			assert.Nil(t, w.leading(), "no one takes the lease before it expires")
		}},
		{"a drain fences the coordinator, an act then answers not held, and a drain's on held at its write lets a fence and another act through", func(t *testing.T, w *world, a, _ *instance) {
			id := w.opened(a)
			w.do("a1 drains", a.leader.Drain)
			var err error
			w.do("a1 closes it", func() { _, err = a.coord.Close(ctx, id, closing("close-1")) })
			require.ErrorIs(t, err, app.ErrNotHeld)
			w.do("a1 turns the kill switch on", func() { _, err = w.setSwitch(a, true, killing(0)) })
			require.ErrorIs(t, err, app.ErrNotHeld)
			require.NotNil(t, w.held, "the drain's on is held at its write")
			w.do("a1 is fenced again", a.coord.Fence)
			w.do("a1 opens an item", func() { _, err = a.coord.Open(ctx, openInput()) })
			require.ErrorIs(t, err, app.ErrNotHeld)
			assert.Zero(t, w.latestRow(), "the held write has written nothing")
			var r recorded
			w.do("the driver lets the drain's on write", func() { r = w.letGo() })
			require.NoError(t, r.err)
			assert.Equal(t, app.KillSwitchOutcome{On: true, Seq: 1, Changed: true, Deferred: true}, r.out)
			epoch, _, _ := a.leader.Status()
			writes := w.writes()
			assert.Equal(t, write{Store: storeSwitch, Epoch: epoch, Lease: epoch}, writes[len(writes)-1], "the row is written under the draining holding")
		}},
		{"a takeover by the second coordinator over the same stores carries the first's items on", func(t *testing.T, w *world, a, b *instance) {
			w.pending = false
			first, second, third := w.opened(a), w.opened(a), w.opened(a)
			require.Equal(t, []item.ID{third}, a.coord.Lifecycle().Waiting)
			w.do("a1 crashes", func() { w.discard(a) })
			w.do("time passes the lease's term", func() { w.passTime(worldTTL) })
			require.Same(t, b, w.leading())
			before, after := w.lifecycles()
			assert.Equal(t, []item.ID{third}, before.Waiting, "a's snapshot, the last a leader published before b took over")
			assert.Equal(t, 2, after.Investigating, "b's, which carries a's items on")
			assert.Equal(t, []item.ID{third}, after.Waiting)
			_, rts := w.runnerTurns(running)
			var runs, open []board.RunID
			for _, rt := range rts {
				runs = append(runs, rt.turn.Run)
			}
			for range worldAgents {
				open = append(open, w.runOf(first), w.runOf(second))
			}
			assert.ElementsMatch(t, open, runs, "b's runner runs the turns of the open rounds")
		}},
		{"time passing fires each timer it passes, in their order, and none it does not reach", func(t *testing.T, w *world, a, _ *instance) {
			var fired []int
			now := w.monoNow()
			for i, d := range []time.Duration{3 * time.Second, time.Second, 2 * time.Second, 10 * time.Second} {
				leaderTimers{a}.At(now.Add(d), func() { fired = append(fired, i) })
			}
			w.do("time passes 5s", func() { w.passTime(5 * time.Second) })
			assert.Equal(t, []int{1, 2, 0}, fired)
		}},
		{"a step of the database's clock fires no timer, shows in the next record's time, and reaches the coordinator's clock at the next sample, which holds its reading after a step back", func(t *testing.T, w *world, a, _ *instance) {
			fired := false
			leaderTimers{a}.At(w.monoNow().Add(time.Second), func() { fired = true })
			before := a.db.Now()
			w.do("the database's clock steps 4s", func() { w.stepClock(-1, 4*time.Second) })
			assert.False(t, fired, "no timer fires")
			assert.Equal(t, before, a.db.Now(), "the coordinator's clock moves at the next sample")
			id := w.opened(a)
			assert.Equal(t, w.dbNow(), w.records()[id][0].At, "the opening is stamped with the database's time")
			w.do("time passes to the next renewal, which samples", func() { w.passTime(worldRenewEvery) })
			assert.True(t, fired)
			assert.Equal(t, w.dbNow(), a.db.Now(), "the sample moved the coordinator's clock")
			fired = false
			leaderTimers{a}.At(w.monoNow().Add(time.Second), func() { fired = true })
			w.do("the database's clock steps -1m", func() { w.stepClock(-1, -time.Minute) })
			assert.False(t, fired, "no timer fires at a step back either")
			ahead := a.db.Now()
			second := w.opened(a)
			assert.Equal(t, w.dbNow(), w.records()[second][0].At, "the next opening is stamped a minute back")
			w.do("time passes to the next renewal, which samples", func() { w.passTime(worldRenewEvery) })
			assert.False(t, a.db.Now().Before(ahead), "the coordinator's clock does not go back")
			assert.True(t, a.db.Now().After(w.dbNow()), "it holds its reading, ahead of the database's")
		}},
		{"a step of a wall clock reaches the coordinator's clock at once, and the holding's check, which a step back does not extend", func(t *testing.T, w *world, a, _ *instance) {
			epoch, _, _ := a.leader.Status()
			before := a.db.Now()
			w.do("host a's wall clock steps 1m", func() { w.stepClock(0, time.Minute) })
			assert.Equal(t, before.Add(time.Minute), a.db.Now(), "the coordinator's clock follows at once")
			assert.False(t, a.hold.Valid(epoch), "the holding's check counts the wall clock's minute")
			w.do("time passes to the next renewal", func() { w.passTime(worldRenewEvery) })
			assert.True(t, a.hold.Valid(epoch), "the renewal counts from now")
			ahead := a.db.Now()
			w.armFault(faultRenewal)
			w.do("host a's wall clock steps -1m", func() { w.stepClock(0, -time.Minute) })
			assert.Equal(t, ahead, a.db.Now(), "the coordinator's clock holds its reading")
			w.do("time passes to just before the holding ends", func() { w.passTime(worldTTL - worldMargin - time.Millisecond) })
			assert.True(t, a.hold.Valid(epoch))
			w.do("time passes to its end", func() { w.passTime(time.Millisecond) })
			assert.False(t, a.hold.Valid(epoch), "the step back extends no holding")
		}},
		{"a kill switch row the driver writes out of band reaches the leading coordinator at its leader's next renewal check", func(t *testing.T, w *world, a, _ *instance) {
			w.do("the driver writes an on-row out of band", func() { w.writeRow(true) })
			assert.Zero(t, a.coord.KillSwitchSeen())
			w.do("time passes to just before the next renewal", func() { w.passTime(worldRenewEvery - time.Millisecond) })
			assert.Zero(t, a.coord.KillSwitchSeen())
			w.do("time passes to the next renewal", func() { w.passTime(time.Millisecond) })
			assert.Equal(t, int64(1), a.coord.KillSwitchSeen())
			assert.True(t, a.coord.Lifecycle().KillSwitch.On)
			assert.Equal(t, []ksRow{{Seq: 1, On: true, At: worldStart, OutOfBand: true, Order: 1}}, w.killSwitchRows())
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, 1, sequential)
			c.run(t, w, w.inst(0), w.inst(1))
			require.NoError(t, w.failed())
		})
	}
	t.Run("a launch that fails, for each reason, and one a stop cancels, reach the coordinator through Launched", func(t *testing.T) {
		for _, c := range []struct {
			reason  app.LaunchReason
			notice  bool
			retried bool
		}{
			{app.ReasonEngine, true, true}, {app.ReasonImage, true, true}, {app.ReasonLaunch, true, true},
			{app.ReasonHolding, false, true}, {app.ReasonCancelled, false, false},
		} {
			t.Run(string(c.reason), func(t *testing.T) {
				w := newWorld(t, 1, sequential)
				w.pending = true
				a := w.inst(0)
				id := w.opened(a)
				run := w.runOf(id)
				ins, rts := w.runnerTurns(launchable)
				require.Len(t, rts, worldAgents)
				rt := rts[0]
				switch c.reason {
				case app.ReasonHolding:
					w.do("host a's wall clock steps 1m", func() { w.stepClock(0, time.Minute) })
					w.do("the launch ends", func() { w.launch(ins[0], rt, "") })
				case app.ReasonCancelled:
					w.do("a1 closes it", func() { _, _ = a.coord.Close(ctx, id, closing("close-1")) })
					w.do("the runner reports the stopped launch", func() { w.reportStop(ins[0], rt, reportCancelled) })
				default:
					w.do("the launch ends", func() { w.launch(ins[0], rt, c.reason) })
				}
				var notices []string
				for _, e := range w.boardOf(run) {
					if n := e.Body.Notice; n != nil && n.Code == board.NoticeTurnUnstarted {
						notices = append(notices, n.Agent+" "+n.Reason)
					}
				}
				if c.notice {
					assert.Equal(t, []string{rt.turn.Agent + " " + string(c.reason)}, notices)
				} else {
					assert.Empty(t, notices)
				}
				from := len(w.calls())
				w.do("time passes the retry wait", func() { w.passTime(worldRetry) })
				again := callIndex(w.calls(), from, &portCall{Inst: "a1", Port: "turns", Method: "Start"}) >= 0
				assert.Equal(t, c.retried, again, "the turn's start is tried again after the retry wait")
			})
		}
	})
	t.Run("each class of a turn's end reaches the coordinator through Exited, with or without its turn end on the board", func(t *testing.T) {
		for _, class := range turnClasses {
			for _, onBoard := range []bool{false, true} {
				if class == item.TurnEnded && !onBoard {
					continue
				}
				t.Run(fmt.Sprintf("%s, on the board: %t", class, onBoard), func(t *testing.T) {
					w := newWorld(t, 1, sequential)
					w.pending = false
					a := w.inst(0)
					id := w.opened(a)
					ins, rts := w.runnerTurns(running)
					require.Len(t, rts, worldAgents)
					w.do("every turn ends", func() {
						for i, rt := range rts {
							w.end(ins[i], rt, class, onBoard)
						}
					})
					w.do("time passes the round's span", func() { w.passTime(2*time.Minute + worldRetry) })
					var ended *item.RoundEnded
					records := w.records()[id]
					for i := range records {
						if records[i].Body.RoundEnded != nil && ended == nil {
							ended = records[i].Body.RoundEnded
						}
					}
					require.NotNil(t, ended, "the round ended")
					for _, rt := range rts {
						assert.Equal(t, class, ended.Turns[rt.turn.Agent])
					}
					if onBoard {
						assert.Len(t, ended.TurnEnds, worldAgents, "every turn end is on the board")
					} else {
						assert.Empty(t, ended.TurnEnds)
					}
				})
			}
		}
	})
}

// Timers that fire without end fail the schedule with maxFires' message, and
// end the event, or the settling stage, they fire in, in either mode: a
// coordinator timer that arms itself again at once fires maxFires times, and
// once more at most, rather than on until the watchdog calls the schedule one
// that deadlocks and ends the test binary. The storm stops by itself at ten
// times maxFires, so that the test ends either way. Fails first: once the
// world failed, the event's quiesce fires the storm on, to its own end.
func TestTimersFiringWithoutEndFailTheScheduleAndEndTheirEvent(t *testing.T) {
	for _, m := range []mode{sequential, concurrent} {
		for _, c := range []struct {
			name string
			// run starts the storm, which start arms, and runs the world on.
			run  func(w *world, start func())
			want string
		}{
			{"in an event", func(w *world, start func()) { w.do("a coordinator timer arms itself again at once", start) }, "timers fired without end"},
			{"in the first settling stage, which waits for a leader", func(w *world, start func()) {
				w.discard(w.inst(0))
				start()
				w.settleKillSwitch()
			}, "timers fired without end"},
			{"in the quiet settling stage", func(w *world, start func()) { start(); w.settleQuiet() }, "the schedule does not settle"},
		} {
			t.Run(fmt.Sprintf("%s, %s", [...]string{"sequential", "concurrent"}[m], c.name), func(t *testing.T) {
				w := newWorld(t, 1, m)
				in := w.inst(0)
				var fires atomic.Int64
				var storm func()
				storm = func() {
					if fires.Add(1) < 10*maxFires {
						w.arm(in, true, 0, storm)
					}
				}
				c.run(w, func() { w.arm(in, true, 0, storm) })
				require.Error(t, w.failed())
				assert.Contains(t, w.failed().Error(), c.want)
				assert.LessOrEqual(t, fires.Load(), int64(maxFires+1), "the guard ended the firing")
			})
		}
	}
}

// kindCount counts kind among kinds.
func kindCount(kinds []item.Kind, kind item.Kind) int {
	n := 0
	for _, k := range kinds {
		if k == kind {
			n++
		}
	}
	return n
}
