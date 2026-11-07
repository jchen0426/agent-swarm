package app_test

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The ten invariants of the interleaving test (The interleaving test; R36 to
// R42; R48): each is a check of a world at a stage, after each event, or each batch
// of the concurrent mode's, and at the end of each settling stage, where its
// line says. The ports note what invariants 1, 8 and 10 judge, at each call
// that starts work and at each automatic close: the table's latest row, the calling
// instance's view and its holding, as each stood at the call; the checks judge
// the notes after the event. Each check judges times only by comparing two of
// the database's, never against the order the events ran in, so that every
// invariant holds under clock steps both ways (R39).

// outOfBand is who the driver writes the kill switch's rows as, out of band,
// as a psql session, a restore or a writer that crashed leaves them: a row a
// coordinator wrote names the person whose act wrote it.
const outOfBand = "psql"

// observer is what the checks keep of a world, under w.mu: the records'
// commits, numbered in the order they committed, each record's number and its
// append's; the records committed since the snapshot the checks judge against
// (see doBatch); what the ports noted at each call that starts work and at
// each automatic close (invariants 1 and 8); the kill switch's ons the driver
// sent and their success answers, and the commit each one's request came
// after; the commit after which each run's upstream sessions were last closed
// (invariant 7); the digests of the acts and keyed openings in flight, by
// item and key, which a record of the act carries, and what the records
// committed against them (invariant 6); each live instance's snapshot after
// the event; the items whose opening landed with its answer lost, which no
// coordinator has read since, and the commit after which a coordinator read
// each such item first (invariant 9); each run's item, the position in the world's log of each item's
// latest close with no reopen after it, and the calls that started work after
// it (invariant 10); how far the checks read each kind of note; and, for the
// violation table alone, a store's refusal of what an append would commit.
// folded, each item's records and their fold as the checks last read them, is
// the checks' own, on the driver's goroutine.
type observer struct {
	order, appends   int64
	recordOrder      map[string]int64
	recordAppend     map[string]int64
	events           []item.Record
	works            []workNote
	autoClosed       []autoCloseNote
	answers          []onAnswer
	cameAt           map[string]int64
	closedRuns       map[board.RunID]int64
	inFlight         map[sentKey][]string
	openingsInFlight map[item.ID][]string
	keyFaults        []error
	views            []instView
	runItems         map[board.RunID]item.ID
	closedPos        map[item.ID]int
	unheld           map[item.ID]bool
	heldAt           map[item.ID]int64
	closedFaults     []error

	checkedWorks, checkedCloses, checkedAnswers, checkedKeyFaults, checkedClosedFaults int

	refuse func(id item.ID, drafts []item.Draft) error
	folded map[item.ID]*foldedItem
}

func newObserver() observer {
	return observer{
		recordOrder: map[string]int64{}, recordAppend: map[string]int64{}, cameAt: map[string]int64{}, closedRuns: map[board.RunID]int64{},
		inFlight: map[sentKey][]string{}, openingsInFlight: map[item.ID][]string{}, folded: map[item.ID]*foldedItem{},
		runItems: map[board.RunID]item.ID{}, closedPos: map[item.ID]int{}, unheld: map[item.ID]bool{}, heldAt: map[item.ID]int64{},
	}
}

// workNote is what a port noted of a call that starts work (invariant 1): the
// instance that called, the call, its run and agent, the epoch it acted for,
// whether the instance's holding was valid for it, the table's latest row,
// and the latest row the instance's view held.
type workNote struct {
	inst, call string
	run        board.RunID
	agent      string
	epoch      int64
	valid      bool
	latest     app.KillSwitch
	seen       int64
}

// autoCloseNote is what the records' port noted of an automatic close
// (invariant 8): the instance that recorded it, its record, its item's state
// before it, the table's latest row, and the latest row and the off time of
// the instance's view.
type autoCloseNote struct {
	inst   string
	rec    item.Record
	before *item.State
	latest app.KillSwitch
	seen   int64
	offAt  time.Time
}

// onAnswer is a kill switch's on the driver sent and its success answer: the
// instance, the subject it acted as, which names the commit its request came
// after (cameAt), and the answer.
type onAnswer struct {
	inst, subject string
	out           app.KillSwitchOutcome
}

// sentKey names the acts the driver sends to an item under a key, as records
// keep the key: its hash.
type sentKey struct {
	id  item.ID
	key string
}

// instView is a live instance's coordinator's snapshot after an event.
type instView struct {
	in    *instance
	after app.Lifecycle
}

// foldedItem is an item's records as the checks last read them, and their
// fold, or why they could not be read or folded.
type foldedItem struct {
	records []item.Record
	state   *item.State
	err     error
}

// broke is the error of a check whose invariant n broke.
func broke(n int, format string, args ...any) error {
	return fmt.Errorf("invariant %d: "+format, append([]any{n}, args...)...)
}

// What the ports note.

// validFor reports whether the instance's holding is valid for epoch, as the
// runner and the gateways ask it: none is, for no epoch.
func (in *instance) validFor(epoch int64) bool {
	return epoch != 0 && in.hold.Valid(epoch)
}

// noteWork notes a call of in's that starts work, call, logged as c, for its
// run and agent, under epoch (invariant 1): the table's latest row, then in's
// view, then whether in's holding is valid for epoch, which it returns, for
// the runner to judge the call by. It reads the row first and the holding
// last, as the concurrent mode needs: a row another instance writes comes
// after that instance took the lease, by when in's holding is no longer
// valid, as the lease's premise has it; and a row in's own coordinator
// writes, like any move of its view, comes under the lock it calls under. A
// call made while the holding is valid is judged by invariant 10's part (a)
// too (see noWorkForAClosedItem), whose fault the check after the event
// reports. Its caller holds no lock of the world's.
func (w *world) noteWork(in *instance, c *portCall, call string, epoch int64) (valid bool) {
	latest, _ := w.switchStore.KillSwitch(context.Background())
	n := workNote{inst: in.name, call: call, run: c.Run, agent: c.Agent, epoch: epoch, latest: latest, seen: in.coord.KillSwitchSeen()}
	n.valid = in.validFor(epoch)
	var closed error
	if n.valid {
		judged := *c
		judged.Epoch = epoch
		closed = w.noWorkForAClosedItem(&judged)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.works = append(w.works, n)
	if closed != nil {
		w.closedFaults = append(w.closedFaults, closed)
	}
	return n.valid
}

// committedRecords notes the records an append of in's committed for item id,
// after history: their numbers in the order of the records' commits, the
// append's, the item to read again, the item of each run they name, the
// position in the world's log of a close among them, which a reopen among
// them forgets (see closedAt), and, for each automatic close, what
// invariant 8 judges at it, with the table's latest row and the view of in's
// coordinator, which holds its lock as it records, read now. It judges each
// record of a person's act, and each opening by key, against the acts and
// openings in flight under its key (invariant 6; see inFlightFault). in is
// nil for the violation table's own appends. Its caller holds the lease's row
// lock shared, as the commit did, and no lock of the world's.
func (w *world) committedRecords(in *instance, id item.ID, history, committed []item.Record) {
	var closes []autoCloseNote
	for i := range committed {
		if c := committed[i].Body.Closed; c != nil && c.Auto && in != nil {
			n := autoCloseNote{inst: in.name, rec: committed[i], seen: in.coord.KillSwitchSeen(), offAt: in.coord.Lifecycle().KillSwitch.OffAt}
			n.latest, _ = w.switchStore.KillSwitch(context.Background())
			n.before, _ = item.Fold(slices.Concat(history, committed[:i]))
			closes = append(closes, n)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.appends++
	for i := range committed {
		w.order++
		w.recordOrder[committed[i].ID] = w.order
		w.recordAppend[committed[i].ID] = w.appends
		w.events = append(w.events, committed[i])
		if err := w.inFlightFault(&committed[i]); err != nil {
			w.keyFaults = append(w.keyFaults, err)
		}
		if r := &committed[i]; r.Run != "" {
			w.runItems[r.Run] = r.Item
		}
		switch b := &committed[i].Body; {
		case b.Closed != nil:
			w.closedPos[id] = len(w.entries)
		case b.Reopened != nil:
			delete(w.closedPos, id)
		}
	}
	w.dirty[id] = true
	if !w.itemSeen[id] {
		w.itemSeen[id] = true
		w.itemIDs = append(w.itemIDs, id)
	}
	w.autoClosed = append(w.autoClosed, closes...)
}

// inFlightFault judges a committed record against what is in flight under its
// key (invariant 6), under w.mu: the request of a person's act carries the
// digest of an act the driver sends the record's item under that key now, as
// a record of an act lands during the act, and an opening by key the digest
// of an opening the driver sends under it now. In the sequential mode one act
// is in flight; in the concurrent mode, a batch's few. With one record a key
// (see aKeyNamesOneActOfItsItem), the record is the first act that landed
// under its key.
func (w *world) inFlightFault(r *item.Record) error {
	if o := r.Body.Opened; o != nil && o.Digest != "" && !slices.Contains(w.openingsInFlight[r.Item], o.Digest) {
		return broke(6, "item %s opened with the digest %s, not that of an opening in flight under its key: %v", r.Item, o.Digest, w.openingsInFlight[r.Item])
	}
	if req := requestHeld(&r.Body); req != nil {
		if flying := w.inFlight[sentKey{r.Item, req.Key}]; !slices.Contains(flying, req.Digest) {
			return broke(6, "item %s's record %d holds the key %s with the digest %s, not that of an act in flight under the key: %v",
				r.Item, r.Seq, req.Key, req.Digest, flying)
		}
	}
	return nil
}

// noteUnheld notes, of an append of the records of item id, whose answer was
// lost if lost says so, whether a coordinator holds the item since: one whose
// opening landed with its answer lost holds it only once it reads the item,
// which a load that succeeds notes, with lost false and no opening, and the
// commit after which it did (invariant 9).
func (w *world) noteUnheld(id item.ID, committed []item.Record, lost bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !lost {
		if w.unheld[id] {
			delete(w.unheld, id)
			w.heldAt[id] = w.order
		}
		return
	}
	for i := range committed {
		if committed[i].Body.Opened != nil {
			w.unheld[id] = true
		}
	}
}

// came notes that a kill switch's on, sent as subject, came to the kill
// switch's store, after the records committed so far: the runs then running
// are those its success answer names (invariant 1).
func (w *world) came(subject string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cameAt[subject] = w.order
}

// noteAnswer notes the success answer of a kill switch's on, sent as subject
// to in.
func (w *world) noteAnswer(in *instance, subject string, out *app.KillSwitchOutcome) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.answers = append(w.answers, onAnswer{inst: in.name, subject: subject, out: *out})
}

// actKinds are the kinds of the driver's acts as their requests' digests name
// them (Decision 13).
var actKinds = map[string]string{actClose: "close", actPromote: "promote", actReopen: "reopen"}

// actDigest is the digest a record keeps of the driver's act on item id, as
// Decision 13 makes it: the SHA-256 of the canonical JSON of its kind, its
// item and its reason's code, of which the driver's acts give none.
func actDigest(act string, id item.ID) string {
	canonical, err := json.Marshal(map[string]string{"kind": actKinds[act], "item": string(id), "reason": ""})
	if err != nil {
		panic(err) // strings always encode
	}
	return keyHash(string(canonical))
}

// flying notes an act the driver sends to item id under key as in flight,
// until landed, which its caller calls once the act answered: a record of it
// keeps the key's hash and the act's digest (invariant 6).
func (w *world) flying(id item.ID, act, key string) (landed func()) {
	k, d := sentKey{id, keyHash(key)}, actDigest(act, id)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inFlight[k] = append(w.inFlight[k], d)
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.inFlight[k] = removeOne(w.inFlight[k], d)
	}
}

// openingFlying notes an opening the driver sends under its item's key, with
// its request's digest, as in flight, until landed (invariant 6).
func (w *world) openingFlying(id item.ID, digest string) (landed func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.openingsInFlight[id] = append(w.openingsInFlight[id], digest)
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.openingsInFlight[id] = removeOne(w.openingsInFlight[id], digest)
	}
}

// removeOne removes one s from in.
func removeOne(in []string, s string) []string {
	if i := slices.Index(in, s); i >= 0 {
		return slices.Delete(in, i, i+1)
	}
	return in
}

// What the checks read.

// observe reads again, and folds, the records of each item a commit changed
// since the checks last read them.
func (w *world) observe() {
	w.mu.Lock()
	ids := slices.Sorted(maps.Keys(w.dirty))
	clear(w.dirty)
	w.mu.Unlock()
	for _, id := range ids {
		records, err := w.recordStore.Load(context.Background(), id)
		f := &foldedItem{records: records, err: err}
		if err == nil {
			f.state, f.err = item.Fold(records)
		}
		w.folded[id] = f
	}
}

// observed returns the items the checks read, by id.
func (w *world) observed() []item.ID {
	return slices.Sorted(maps.Keys(w.folded))
}

// investigating counts the items investigating as the records hold them and
// the limit counts them: those whose status is investigating, and those
// queued and promoted (Decision 4); and it lists the items queued.
func (w *world) investigating() (n int, queued []item.ID) {
	for _, id := range w.observed() {
		s := w.folded[id].state
		if s == nil {
			continue
		}
		switch s.Status() {
		case item.StatusInvestigating:
			n++
		case item.StatusQueued:
			queued = append(queued, id)
			if s.Promotion != nil {
				n++
			}
		}
	}
	return n, queued
}

// runOrders returns, from an item's records, the numbers of its run's
// creation and of its run's stop in the order of the records' commits, 0 for
// one not recorded.
func (w *world) runOrders(records []item.Record) (created, stopped int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range records {
		switch {
		case records[i].Body.RunCreated != nil:
			created = w.recordOrder[records[i].ID]
		case records[i].Body.RunStopped != nil:
			stopped = w.recordOrder[records[i].ID]
		}
	}
	return created, stopped
}

// The checks.

// lifecycleInvariants checks the ten invariants, in their order, each where
// its line applies, and returns the first that broke.
func lifecycleInvariants(w *world, stage string) error {
	for _, check := range []func(*world, string) error{
		noWorkWhileOn, everyRunAliveAtAnOnIsHalted, noQueuedItemWaitsWhileASlotIsFree, theLimitAndOne, recordsAndEpochs,
		aKeyNamesOneActOfItsItem, aStoppedRunLeavesNothingRunning, autoCloseKeepsItsPauseAndItsDeadline, theQueuesOrder,
		closedItemsRunNothing,
	} {
		if err := check(w, stage); err != nil {
			return err
		}
	}
	return nil
}

// noWorkWhileOn checks invariant 1 (R36, R37). While the kill switch's latest
// row is an on-row, no instance calls Turns.Start, Sessions.Issue or
// Sessions.Move: from the row's commit when a coordinator wrote it, whatever
// its answer, and from the moment the calling instance's view reached it when
// the driver wrote it out of band (see onAt). A call counts only while the
// calling instance's holding is valid for its epoch: one made after its
// holding ended is refused downstream, by the runner and by the gateways.
// After each event, an instance whose view is on has no live session and its
// runner runs no turn, as when the view turned on in the event, by the on's
// steps or a takeover's; and an on's success answer names every run that ran
// when the on came in its Stopped or its Owed. A drain's on, answered
// deferred, names none, as the next leader's takeover acts on it.
func noWorkWhileOn(w *world, _ string) error {
	w.mu.Lock()
	works := slices.Clone(w.works[w.checkedWorks:])
	answers := slices.Clone(w.answers[w.checkedAnswers:])
	w.checkedWorks, w.checkedAnswers = len(w.works), len(w.answers)
	views := slices.Clone(w.views)
	w.mu.Unlock()
	for i := range works {
		if err := works[i].whileOn(); err != nil {
			return err
		}
	}
	for i := range views {
		if err := w.stillWhileOn(&views[i]); err != nil {
			return err
		}
	}
	w.observe()
	for i := range answers {
		if err := w.namesEveryRun(&answers[i]); err != nil {
			return err
		}
	}
	return nil
}

// onAt reports whether a call or a record made while latest was the table's
// latest row, by an instance whose view held seen, comes while the kill
// switch is on, as invariants 1 and 8 count it, and how: the latest row is an
// on a coordinator wrote, by SetKillSwitch or RecordKillSwitchOn, from its
// commit, whatever its answer; or an on the driver wrote out of band, from
// the moment the view reached it, R37's residual.
func onAt(latest *app.KillSwitch, seen int64) (on bool, how string) {
	switch {
	case !latest.On:
		return false, ""
	case latest.By != outOfBand:
		return true, fmt.Sprintf("after on-row %d committed, which a coordinator wrote", latest.Seq)
	case seen >= latest.Seq:
		return true, fmt.Sprintf("once its view reached on-row %d, which the driver wrote out of band", latest.Seq)
	}
	return false, ""
}

// whileOn judges a call that starts work by the table's latest row and the
// caller's view, as they stood at the call.
func (n *workNote) whileOn() error {
	if on, how := onAt(&n.latest, n.seen); n.valid && on {
		return broke(1, "%s called %s for run %s, agent %s, under epoch %d, %s", n.inst, n.call, n.run, n.agent, n.epoch, how)
	}
	return nil
}

// stillWhileOn checks, of an instance whose view is on after the event, that
// it keeps no session live and that its runner runs no turn: the on's steps,
// or the fence before a takeover, stopped every turn and ended every session,
// and nothing starts while the view is on.
func (w *world) stillWhileOn(v *instView) error {
	if !v.after.KillSwitch.On {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if v.in.gone {
		return nil
	}
	var live []string
	for k := range v.in.live {
		live = append(live, string(k.run)+"/"+k.agent)
	}
	if len(live) > 0 {
		slices.Sort(live)
		return broke(1, "%s's view is on, at on-row %d, and it keeps the sessions %v live", v.in.name, v.after.KillSwitch.OnSeq, live)
	}
	for _, rt := range v.in.turns {
		if !rt.stopped {
			return broke(1, "%s's view is on, at on-row %d, and its runner still runs the turn of %s %s", v.in.name, v.after.KillSwitch.OnSeq, rt.turn.Run, rt.turn.Agent)
		}
	}
	return nil
}

// namesEveryRun checks that an on's success answer names, in its Stopped or
// its Owed, every run that ran when the on came: one whose creation committed
// before the on's request came to the kill switch's store, and whose stop, if
// any, committed after and killed: a run whose round had decided another stop
// is halted for that stop, and the answer does not name it. Every on that answers success reached the store, so
// an answer whose request the world never noted there is a fault of the
// harness's, which fails the schedule.
func (w *world) namesEveryRun(a *onAnswer) error {
	w.mu.Lock()
	came, ok := w.cameAt[a.subject]
	w.mu.Unlock()
	if !ok {
		return fmt.Errorf("the harness: %s's on, sent as %s, answered success, but the world noted no request of it at the kill switch's store", a.inst, a.subject)
	}
	for _, id := range w.observed() {
		f := w.folded[id]
		if f.state == nil || f.state.Run == nil {
			continue
		}
		created, stopped := w.runOrders(f.records)
		if created > came || stopped != 0 && stopped <= came {
			continue
		}
		if stopped != 0 && f.state.Run.Stopped.Reason != item.StopKilled {
			continue
		}
		run := f.state.Run.ID
		if !slices.ContainsFunc(a.out.Stopped, func(s app.RunStop) bool { return s.Run == run }) && !slices.Contains(a.out.Owed, id) {
			return broke(1, "%s's on answered row %d naming neither run %s of item %s, which ran when the on came, among its stops %v, nor the item among those owed %v",
				a.inst, a.out.Seq, run, id, a.out.Stopped, a.out.Owed)
		}
	}
	return nil
}

// everyRunAliveAtAnOnIsHalted checks invariant 2 (Decision 5) once the first
// settling stage is over: no item owes the kill switch's latest on-row, as
// the table holds it (State.Owes): every run created before it has stopped,
// and no item with no run that owes it has started.
func everyRunAliveAtAnOnIsHalted(w *world, stage string) error {
	if stage == stageEvent {
		return nil
	}
	table, err := w.switchStore.KillSwitch(context.Background())
	if err != nil || table.OnSeq == 0 {
		return err
	}
	w.observe()
	for _, id := range w.observed() {
		if s := w.folded[id].state; s != nil && s.Owes(table.OnSeq) {
			return broke(2, "item %s, %s, still owes the kill switch's latest on-row %d after the first settling stage", id, s.Status(), table.OnSeq)
		}
	}
	return nil
}

// noQueuedItemWaitsWhileASlotIsFree checks invariant 3 (Decision 4; R37).
// After every event, the leading coordinator's snapshot holds a waiting item
// only while its items investigating are at the limit or above, or while its
// view is unsettled; once quiet, the records show no queued item while fewer
// than the limit investigate.
func noQueuedItemWaitsWhileASlotIsFree(w *world, stage string) error {
	if _, after := w.lifecycles(); len(after.Waiting) > 0 && after.Investigating < after.Limit && !after.KillSwitchUnsettled {
		return broke(3, "the leading coordinator's snapshot holds %v waiting while %d of %d investigate, its view settled",
			after.Waiting, after.Investigating, after.Limit)
	}
	if stage != stageQuiet {
		return nil
	}
	w.observe()
	if n, queued := w.investigating(); len(queued) > 0 && n < worldLimit {
		return broke(3, "once quiet, the records hold %v queued while %d of %d investigate", queued, n, worldLimit)
	}
	return nil
}

// theLimitAndOne checks invariant 4 (Decision 4) after every event: the items
// investigating, counted from the records and as the leading coordinator's
// snapshot counts them, are at most the limit and one.
func theLimitAndOne(w *world, _ string) error {
	if _, after := w.lifecycles(); after.Investigating > after.Limit+1 {
		return broke(4, "the leading coordinator's snapshot counts %d items investigating, above the limit %d and one", after.Investigating, after.Limit)
	}
	w.observe()
	if n, _ := w.investigating(); n > worldLimit+1 {
		return broke(4, "the records hold %d items investigating, above the limit %d and one", n, worldLimit)
	}
	return nil
}

// recordsAndEpochs checks invariant 5 after every event: every item's records
// fold, and every write the guarded stores committed carried an epoch, the
// lease's at its commit. The world reads the lease's epoch at the commit,
// apart from the guard's check (see wrote), so a write that no guard checked,
// or a lease that moved between a write's check and its commit, breaks it.
// It checks the writes committed since its last check.
func recordsAndEpochs(w *world, _ string) error {
	w.observe()
	for _, id := range w.observed() {
		if err := w.folded[id].err; err != nil {
			return broke(5, "the records of item %s cannot be read or do not fold: %v", id, err)
		}
	}
	w.mu.Lock()
	writes := slices.Clone(w.committed[w.checkedWrites:])
	w.checkedWrites = len(w.committed)
	w.mu.Unlock()
	for _, wr := range writes {
		if wr.Epoch == 0 || wr.Epoch != wr.Lease {
			return broke(5, "a write to the %s store carried epoch %d, while the lease's was %d", wr.Store, wr.Epoch, wr.Lease)
		}
	}
	return nil
}

// requestHeld is the request a person's act on an item keeps in its record,
// b: a promotion's, a reopen's, or a person's close's; nil for any other
// record (Contract C19).
func requestHeld(b *item.Body) *item.Request {
	switch {
	case b.Promoted != nil:
		return &b.Promoted.Request
	case b.Reopened != nil:
		return &b.Reopened.Request
	case b.Closed != nil:
		return b.Closed.Request
	}
	return nil
}

// aKeyNamesOneActOfItsItem checks invariant 6 (R42) after every event: no two
// records of an item hold one request's key, and each record of a person's
// act carries, with its key, the digest of an act the driver had in flight to
// that item under that key as the record committed, which the records' port
// judged at the commit (see inFlightFault); an opening by key, the digest of
// an opening in flight under it. One record a key, of an act in flight as it
// landed, is the first act under the key that landed: an act refused before
// its record, or whose append did not land, leaves the key free for the next.
// The same key on two items names two acts, one on each.
func aKeyNamesOneActOfItsItem(w *world, _ string) error {
	w.observe()
	w.mu.Lock()
	faults := slices.Clone(w.keyFaults[w.checkedKeyFaults:])
	w.checkedKeyFaults = len(w.keyFaults)
	w.mu.Unlock()
	if len(faults) > 0 {
		return faults[0]
	}
	for _, id := range w.observed() {
		keys := map[string]int64{}
		records := w.folded[id].records
		for i := range records {
			req := requestHeld(&records[i].Body)
			if req == nil {
				continue
			}
			if seq, ok := keys[req.Key]; ok {
				return broke(6, "item %s holds the key %s on two records, %d and %d", id, req.Key, seq, records[i].Seq)
			}
			keys[req.Key] = records[i].Seq
		}
	}
	return nil
}

// aStoppedRunLeavesNothingRunning checks invariant 7 (Decision 3) once quiet:
// no run that stopped, and no closed item's run, has a live session on an
// instance or a turn its runner still runs, and each such run's upstream
// sessions were closed once its stop committed.
func aStoppedRunLeavesNothingRunning(w *world, stage string) error {
	if stage != stageQuiet {
		return nil
	}
	w.observe()
	for _, id := range w.observed() {
		f := w.folded[id]
		if s := f.state; s != nil && s.Run != nil && (s.Run.Stopped != nil || s.Status() == item.StatusClosed) {
			if err := w.leftNothingRunning(id, s.Run.ID, f.records); err != nil {
				return err
			}
		}
	}
	return nil
}

// leftNothingRunning checks item id's run, which stopped, against the live
// instances and the upstream pools.
func (w *world) leftNothingRunning(id item.ID, run board.RunID, records []item.Record) error {
	_, stopped := w.runOrders(records)
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, in := range w.insts {
		if in == nil {
			continue
		}
		for k := range in.live {
			if k.run == run {
				return broke(7, "item %s's run %s stopped, and %s keeps the session of %s live", id, run, in.name, k.agent)
			}
		}
		for _, rt := range in.turns {
			if rt.turn.Run == run && !rt.stopped {
				return broke(7, "item %s's run %s stopped, and %s's runner still runs the turn of %s", id, run, in.name, rt.turn.Agent)
			}
		}
	}
	if closed := w.closedRuns[run]; closed < stopped {
		return broke(7, "item %s's run %s stopped at commit %d, and its upstream sessions were last closed after commit %d", id, run, stopped, closed)
	}
	return nil
}

// autoCloseKeepsItsPauseAndItsDeadline checks invariant 8 (Decision 8; R19,
// R41) at each automatic close the records' port noted, after every event.
func autoCloseKeepsItsPauseAndItsDeadline(w *world, _ string) error {
	w.mu.Lock()
	notes := slices.Clone(w.autoClosed[w.checkedCloses:])
	w.checkedCloses = len(w.autoClosed)
	w.mu.Unlock()
	for i := range notes {
		if err := notes[i].keeps(); err != nil {
			return err
		}
	}
	return nil
}

// keeps judges an automatic close: none is recorded while the table's latest
// row is an on, a row the driver wrote out of band counting from the moment
// the recording instance's view reached it, as in invariant 1 (see onAt); its
// due is the deadline its item's state before it gives (R19), with the off
// time of the recording instance's view, not the table's latest off-row's,
// which a row written out of band and not yet seen can move; and the time its
// record is stamped with, the database's, is no earlier (R41). An item whose
// state before it does not fold is invariant 5's.
func (n *autoCloseNote) keeps() error {
	if on, how := onAt(&n.latest, n.seen); on {
		return broke(8, "%s recorded item %s's automatic close %s", n.inst, n.rec.Item, how)
	}
	if n.before == nil || n.before.Closing == nil {
		return nil
	}
	delay := n.before.Closing.AutoClose
	if delay == 0 {
		delay = worldAutoClose
	}
	c := n.rec.Body.Closed
	if due := n.before.AutoCloseAt(delay, n.offAt); !c.Due.Equal(due) {
		return broke(8, "%s recorded item %s's automatic close due at %s, not at %s, which its state before it gives with its view's off at %s",
			n.inst, n.rec.Item, c.Due, due, n.offAt)
	}
	if n.rec.At.Before(c.Due) {
		return broke(8, "item %s's automatic close is stamped %s, before its due %s", n.rec.Item, n.rec.At, c.Due)
	}
	return nil
}

// theQueuesOrder checks invariant 9 (Decision 4; R9, R40) after every event:
// every item the walk admitted in it ranks ahead of every item still waiting
// in the leading coordinator's snapshot, an item ranking by its opening
// record's time, then its id. An item the walk admitted is one waiting in the
// snapshot before the event, or opened in it queued, that leaves the waiting
// list in the event by the walk's admission (see admittedByTheWalk): it
// started, or was not staffed at its start for the daily cap, its routing or
// its type, or waits for its start's retry, admitted; not by a promotion, a
// close, or not staffed for the kill switch. A promotion past older waiting
// items is correct, and no admission. Against the order in which the
// openings committed, the rank tolerates only two items whose openings' times
// are out of their commit order, which a step back of the database's clock
// between them makes, R9's residual, and, with their times equal, the order of
// their ids, as R9 gives it. The snapshot before is the last one a leading
// coordinator published, the event spans the events since it, and an event
// after which none leads is not judged. An item absent from the snapshots,
// whose records the coordinator could not read, holds up no other. In the
// concurrent mode an item opened in the batch may have come after an
// admission of the batch's, so the items still waiting are those that waited
// before the batch. Nor does an item the coordinator did not hold when the
// walk admitted another, as its opening landed with its answer lost and the
// lookup after it failed, until a read of it took it in, waiting, in the same
// event (see heldBefore).
func theQueuesOrder(w *world, _ string) error {
	before, after := w.lifecycles()
	if after.Limit == 0 {
		return nil
	}
	w.observe()
	w.mu.Lock()
	inEvent := map[string]bool{}
	var opened []item.ID
	for i := range w.events {
		r := &w.events[i]
		inEvent[r.ID] = true
		if r.Body.Opened != nil {
			opened = append(opened, r.Item)
		}
	}
	w.mu.Unlock()
	waiting, waited := setOf(after.Waiting), setOf(before.Waiting)
	var admitted []item.ID
	for _, id := range slices.Concat(before.Waiting, opened) {
		if !waiting[id] && !slices.Contains(admitted, id) && w.admittedByTheWalk(id, inEvent) {
			admitted = append(admitted, id)
		}
	}
	for _, still := range after.Waiting {
		if w.mode == concurrent && !waited[still] {
			continue
		}
		for _, a := range admitted {
			if !w.ranksAhead(a, still) && w.heldBefore(still, a) {
				return broke(9, "item %s, admitted by the walk, does not rank ahead of item %s, still waiting: their openings' times are %s and %s",
					a, still, w.openedAt(a), w.openedAt(still))
			}
		}
	}
	return nil
}

// admittedByTheWalk reports whether item id, which left the waiting list in
// the event, left it by the walk's admission there, as the record that took
// it out of the queue says, the first after its opening: none, the item
// admitted and its start due; or one the event committed, its start, or its
// not staffed at its start for the daily cap, its routing or its type. A
// promotion, a close, or a not staffed for the kill switch is no admission,
// nor is a not staffed in the opening's own append, as the item never waited.
// Nor is an opening that landed with its answer lost, while no coordinator
// has read the item since: the opening's lookup failed, the coordinator does
// not hold the item, and the item's retry, not the walk, takes it into the
// account (see noteUnheld).
// Nor is a record that committed before the event: the snapshot before it
// held the item waiting all the same, as the coordinator learns of a record
// whose answer was lost only at the item's next load, and that load, not the
// walk, took the item out of the waiting list.
func (w *world) admittedByTheWalk(id item.ID, inEvent map[string]bool) bool {
	f := w.folded[id]
	w.mu.Lock()
	unheld := w.unheld[id]
	w.mu.Unlock()
	switch {
	case f == nil || len(f.records) == 0 || unheld:
		return false
	case len(f.records) == 1:
		return true
	}
	opening, exit := &f.records[0], &f.records[1]
	w.mu.Lock()
	sameAppend := w.recordAppend[opening.ID] == w.recordAppend[exit.ID]
	w.mu.Unlock()
	switch {
	case !inEvent[exit.ID] || sameAppend:
		return false
	case exit.Body.Started != nil:
		return true
	case exit.Body.AwaitingClose != nil:
		return exit.Body.AwaitingClose.Cause != item.CauseKillSwitch
	}
	return false
}

// heldBefore reports whether a coordinator held item still when item a left
// the waiting list: unless still is an item whose opening landed with its
// answer lost, and a's record that took it out of the queue committed before
// a coordinator first read still, which took it into the account.
func (w *world) heldBefore(still, a item.ID) bool {
	f := w.folded[a]
	w.mu.Lock()
	defer w.mu.Unlock()
	at, ok := w.heldAt[still]
	if !ok || f == nil || len(f.records) < 2 {
		return true
	}
	return w.recordOrder[f.records[1].ID] > at
}

// setOf returns the items of ids as a set.
func setOf(ids []item.ID) map[item.ID]bool {
	out := make(map[item.ID]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// openedAt is when item id opened, by its opening record's time.
func (w *world) openedAt(id item.ID) time.Time {
	if f := w.folded[id]; f != nil && len(f.records) > 0 {
		return f.records[0].At
	}
	return time.Time{}
}

// ranksAhead reports whether item a ranks ahead of item b in the queue: by its
// opening record's time, then its id (R40).
func (w *world) ranksAhead(a, b item.ID) bool {
	if ta, tb := w.openedAt(a), w.openedAt(b); !ta.Equal(tb) {
		return ta.Before(tb)
	}
	return a < b
}

// closedItemsRunNothing checks invariant 10, nothing runs for a closed item
// (R48, R72), after every event: from the commit of an item's item_closed
// with no item_reopened after it, (a) no instance called Turns.Start,
// Sessions.Issue or Sessions.Move for the item's run, as the ports judged each
// call as it came (see noWorkForAClosedItem); and (b) at the event's end, no
// live instance keeps a live session of the run, and no runner holds an
// unstopped turn of it, a pending launch completed after the close among them
// (see nothingRunsForAClosedItem). A call or a state counts only while the
// instance's holding is valid for its epoch, as invariant 1 counts it.
// Invariant 7 reads a closed item's run only once quiet, after the item's
// retry dropped it; this one watches the close in time.
func closedItemsRunNothing(w *world, _ string) error {
	w.mu.Lock()
	faults := slices.Clone(w.closedFaults[w.checkedClosedFaults:])
	w.checkedClosedFaults = len(w.closedFaults)
	w.mu.Unlock()
	if len(faults) > 0 {
		return faults[0]
	}
	return w.nothingRunsForAClosedItem()
}

// closedAt returns the position in the world's log of the commit of item
// id's latest close with no reopen after it, and whether it has one: a port
// call at that position or after it came after the close committed.
func (w *world) closedAt(id item.ID) (int, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	pos, ok := w.closedPos[id]
	return pos, ok
}

// noWorkForAClosedItem judges c, a call that starts work, made while the
// calling instance's holding was valid for c's epoch, by invariant 10's part
// (a): it breaks the invariant when c's run is that of an item whose close
// committed before c, with no reopen after it.
func (w *world) noWorkForAClosedItem(c *portCall) error {
	w.mu.Lock()
	id, ok := w.runItems[c.Run]
	w.mu.Unlock()
	if !ok {
		return nil
	}
	if at, closed := w.closedAt(id); !closed || c.pos < at {
		return nil
	}
	return broke(10, "%s called %s.%s for run %s, agent %s, under epoch %d, after item %s's close committed", c.Inst, c.Port, c.Method, c.Run, c.Agent, c.Epoch, id)
}

// nothingRunsForAClosedItem checks invariant 10's part (b) at an event's end:
// no live instance keeps a live session of a closed item's run, and no runner
// holds an unstopped turn of it, launching or running, while the instance's
// holding is valid for the session's or the turn's epoch. The holding is
// judged once w.mu is let go, as the holding reads the host's wall clock.
func (w *world) nothingRunsForAClosedItem() error {
	type kept struct {
		in    *instance
		epoch int64
		err   error
	}
	var all []kept
	w.mu.Lock()
	closedRun := func(run board.RunID) (item.ID, bool) {
		id, ok := w.runItems[run]
		if !ok {
			return "", false
		}
		_, closed := w.closedPos[id]
		return id, closed
	}
	for _, in := range w.insts {
		if in == nil || in.gone {
			continue
		}
		keys := slices.SortedFunc(maps.Keys(in.live), func(a, b sessionKey) int {
			return cmp.Or(cmp.Compare(a.run, b.run), cmp.Compare(a.agent, b.agent))
		})
		for _, k := range keys {
			if id, closed := closedRun(k.run); closed {
				all = append(all, kept{in, in.live[k].epoch, broke(10, "item %s's close committed, and %s keeps the session of %s %s live", id, in.name, k.run, k.agent)})
			}
		}
		for _, rt := range in.turns {
			if id, closed := closedRun(rt.turn.Run); closed && !rt.stopped {
				how := "runs"
				if rt.launching {
					how = "launches"
				}
				all = append(all, kept{in, rt.epoch, broke(10, "item %s's close committed, and %s's runner still %s the turn of %s %s", id, in.name, how, rt.turn.Run, rt.turn.Agent)})
			}
		}
	}
	w.mu.Unlock()
	for _, k := range all {
		if k.in.validFor(k.epoch) {
			return k.err
		}
	}
	return nil
}

// The violation table's fixtures.

// appendDirect appends records of bodies to item id straight to the records
// store, past every port and guard, as the violation table's fixtures do, and
// notes them as the stores' commits are noted, but for any write: no guarded
// store committed them. A round's end or a run's stop is a record of the
// item's run.
func (w *world) appendDirect(id item.ID, bodies ...item.Body) {
	var history []item.Record
	committed, err := w.recordStore.Append(context.Background(), id, func(h []item.Record) ([]item.Draft, error) {
		history = h
		incident := item.IncidentID("incident-" + string(id))
		if len(h) > 0 {
			incident = h[0].Incident
		}
		drafts := make([]item.Draft, 0, len(bodies))
		for _, b := range bodies {
			b.V = item.Version
			d := item.Draft{ID: worldIDs{w}.NewID(), Item: id, Incident: incident, Body: b}
			if b.RoundEnded != nil || b.RunStopped != nil {
				for i := range h {
					if h[i].Body.RunCreated != nil {
						d.Run = h[i].Run
					}
				}
			}
			drafts = append(drafts, d)
		}
		return drafts, nil
	})
	require.NoError(w.t, err)
	w.committedRecords(nil, id, history, committed)
}

// stateOf folds item id's records as the store holds them now.
func (w *world) stateOf(id item.ID) *item.State {
	records, err := w.recordStore.Load(context.Background(), id)
	require.NoError(w.t, err)
	s, err := item.Fold(records)
	require.NoError(w.t, err)
	return s
}

// unstaffedAtTheCap opens an item on in once the day's spend reached the
// daily cap, which leaves it not staffed at its opening, awaiting its close;
// then a new day begins.
func (w *world) unstaffedAtTheCap(in *instance) item.ID {
	w.do("the day's spend reaches the daily cap", func() { w.setCapped(true) })
	id := w.opened(in)
	w.do("a new day begins", func() { w.setCapped(false) })
	require.Equal(w.t, item.StatusAwaitingClose, w.stateOf(id).Status())
	return id
}

// openedBody is an opening record's body, as a person opens an incident.
func openedBody() *item.Opened {
	return &item.Opened{Type: "incident", Title: "checkout fails", Source: "person", Arm: item.ArmVisible, How: item.NoWindow}
}

// startedBody is a start record's body, under the base revision.
func startedBody() *item.Started {
	return &item.Started{Commit: commit, Digest: "sha256:" + commit, Images: map[string]string{"thinloop": image}}
}

// openingDraft decides an opening of item id, as an append's decide does.
func openingDraft(id item.ID) func([]item.Record) ([]item.Draft, error) {
	return func([]item.Record) ([]item.Draft, error) {
		return []item.Draft{{ID: "opening-" + string(id), Item: id, Incident: item.IncidentID("incident-" + string(id)), Body: item.Body{V: item.Version, Opened: openedBody()}}}, nil
	}
}

// autoCloseDraft decides item id's automatic close, due at due, as the
// coordinator's auto-close timer does.
func autoCloseDraft(id item.ID, due time.Time) func([]item.Record) ([]item.Draft, error) {
	return func(h []item.Record) ([]item.Draft, error) {
		return []item.Draft{{ID: "auto-close-" + string(id), Item: id, Incident: h[0].Incident, Body: item.Body{V: item.Version, Closed: &item.Closed{Auto: true, Due: due}}}}, nil
	}
}

// Each invariant's check, fed a world or a state that breaks it, reports it,
// naming the invariant and the reason, so that a row cannot pass on another
// part of its invariant; and each check passes the worlds its invariant
// allows, so that no check is stricter than its invariant (The interleaving
// test). A check that cannot fail would let every run pass for nothing.
// Fails first: a build failure, no checks.
func TestEachInterleavingInvariantCatchesItsViolation(t *testing.T) {
	ctx := context.Background()
	epochOf := func(in *instance) int64 {
		epoch, _, _ := in.leader.Status()
		return epoch
	}
	startTurnOf := func(in *instance, epoch int64, run board.RunID) error {
		_, err := runnerPort{in}.Start(holding.WithEpoch(ctx, epoch), &app.Turn{ID: "turn-1", Run: run, Agent: "release-investigator", Round: 1})
		return err
	}
	startTurn := func(in *instance, epoch int64) error { return startTurnOf(in, epoch, "run-1") }
	for _, c := range []struct {
		name  string
		check func(w *world, stage string) error
		stage string
		// broke is the invariant the row breaks, 0 for none; want is what its
		// error says of why, empty for a row that keeps its check.
		broke int
		want  string
		run   func(t *testing.T, w *world, a, b *instance)
	}{
		{"1: a start the fake runner is told to make after an on-row a coordinator wrote committed, its answer lost", noWorkWhileOn, stageEvent,
			1, "a1 called Turns.Start for run run-1, agent release-investigator, under epoch 1, after on-row 1 committed, which a coordinator wrote",
			func(t *testing.T, w *world, a, _ *instance) {
				w.armFault(faultSwitchLost)
				w.do("a1 turns the kill switch on, its answer lost", func() { _, _ = w.setSwitch(a, true, killing(0)) })
				require.Equal(t, int64(1), w.latestRow())
				w.do("a1's runner is told to start a turn", func() { require.NoError(t, startTurn(a, epochOf(a))) })
			}},
		{"1 keeps: a turn started after an on-row the driver wrote, before the instance's view reached it", noWorkWhileOn, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				w.do("the driver writes an on-row out of band", func() { w.writeRow(true) })
				require.Zero(t, a.coord.KillSwitchSeen())
				w.do("a1's runner starts a turn", func() { require.NoError(t, startTurn(a, epochOf(a))) })
			}},
		{"1 keeps: a start called by an instance whose holding had ended, which the runner refused", noWorkWhileOn, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				w.do("a1 turns the kill switch on", func() {
					_, err := w.setSwitch(a, true, killing(0))
					require.NoError(t, err)
				})
				epoch := epochOf(a)
				w.do("host a's wall clock steps 1m, which ends a1's holding", func() { w.stepClock(0, time.Minute) })
				require.False(t, a.hold.Valid(epoch))
				w.do("a1's runner is told to start a turn", func() { require.ErrorIs(t, startTurn(a, epoch), app.ErrUnheld) })
			}},
		{"1: an instance whose view turned on keeps a session live", noWorkWhileOn, stageEvent,
			1, "a1's view is on, at on-row 1, and it keeps the sessions [id-5/release-investigator] live",
			func(t *testing.T, w *world, a, _ *instance) {
				run := w.runOf(w.opened(a))
				w.do("a1 turns the kill switch on, and a session of the run stays live", func() {
					_, err := w.setSwitch(a, true, killing(0))
					require.NoError(t, err)
					w.mu.Lock()
					a.live[sessionKey{run, "release-investigator"}] = &liveSession{epoch: epochOf(a), round: 1}
					w.mu.Unlock()
				})
			}},
		{"1: an instance whose view stays on through the event keeps a turn running", noWorkWhileOn, stageEvent,
			1, "a1's view is on, at on-row 1, and its runner still runs the turn of run-1 release-investigator",
			func(t *testing.T, w *world, a, _ *instance) {
				w.do("a1 turns the kill switch on", func() {
					_, err := w.setSwitch(a, true, killing(0))
					require.NoError(t, err)
				})
				w.do("a turn of a1's runner runs on", func() {
					w.mu.Lock()
					a.turns = append(a.turns, &runnerTurn{turn: app.Turn{ID: "turn-1", Run: "run-1", Agent: "release-investigator", Round: 1}, epoch: epochOf(a)})
					w.mu.Unlock()
				})
			}},
		{"1: an on's success answer that names no stop and nothing owed of a run that ran when it came", noWorkWhileOn, stageEvent,
			1, "a1's on answered row 1 naming neither run id-5 of item id-1, which ran when the on came",
			func(t *testing.T, w *world, a, _ *instance) {
				w.opened(a)
				w.do("a1 turns the kill switch on", func() {
					_, err := w.setSwitch(a, true, killing(0))
					require.NoError(t, err)
				})
				w.mu.Lock()
				answer := &w.answers[len(w.answers)-1]
				require.NotEmpty(t, answer.out.Stopped, "the on stopped the run")
				answer.out.Stopped, answer.out.Owed = nil, nil
				w.mu.Unlock()
			}},
		{"1 keeps: an on's success answer that names neither a run whose decided stop its halt recorded as quiet, nor its item among the owed", noWorkWhileOn, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				w.do("the round of the item's run ends deciding quiet, its stop not recorded", func() {
					w.appendDirect(id, item.Body{RoundEnded: &item.RoundEnded{
						Round: 1, EndedBy: item.EndedByTurns, Turns: map[string]item.TurnClass{}, Decision: item.Decision{Next: item.NextStop, Stop: item.StopQuiet},
					}})
				})
				w.do("a1 turns the kill switch on", func() {
					_, err := w.setSwitch(a, true, killing(0))
					require.NoError(t, err)
				})
				w.mu.Lock()
				answer := &w.answers[len(w.answers)-1]
				w.mu.Unlock()
				require.Empty(t, answer.out.Stopped, "the on killed no run")
				require.Empty(t, answer.out.Owed)
				require.Equal(t, item.StopQuiet, w.stateOf(id).Run.Stopped.Reason, "its halt recorded the decided stop")
			}},
		{"the harness: an on's success answer whose request the world never noted at the kill switch's store", noWorkWhileOn, stageEvent,
			0, "the harness: a1's on, sent as nobody, answered success, but the world noted no request of it",
			func(_ *testing.T, w *world, a, _ *instance) {
				w.noteAnswer(a, "nobody", &app.KillSwitchOutcome{On: true, Seq: 1})
			}},
		{"2: an item left owing the latest on-row after the first settling stage, its halt refused by a store that does not recover",
			everyRunAliveAtAnOnIsHalted, stageSettled, 2, "item id-1, investigating, still owes the kill switch's latest on-row 1 after the first settling stage",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				w.refuse = func(_ item.ID, drafts []item.Draft) error {
					for i := range drafts {
						if st := drafts[i].Body.RunStopped; st != nil && st.Reason == item.StopKilled {
							return errBlinked
						}
					}
					return nil
				}
				w.do("a1 turns the kill switch on", func() {
					_, err := w.setSwitch(a, true, killing(0))
					require.NoError(t, err)
				})
				w.settleKillSwitch()
				require.True(t, w.stateOf(id).Owes(1), "the item still owes the on-row")
			}},
		{"3: a snapshot that holds a waiting item below the limit, its view settled", noQueuedItemWaitsWhileASlotIsFree, stageEvent,
			3, "the leading coordinator's snapshot holds [x1] waiting while 1 of 2 investigate, its view settled",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit - 1, Waiting: []item.ID{"x1"}}
			}},
		{"3: records that hold a queued item, once quiet, with a slot free", noQueuedItemWaitsWhileASlotIsFree, stageQuiet,
			3, "once quiet, the records hold [x1] queued while 0 of 2 investigate",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
			}},
		{"3 keeps: a waiting item below the limit while the view is unsettled", noQueuedItemWaitsWhileASlotIsFree, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				w.armFault(faultSwitchLost)
				w.armRead(a, faultSwitchRead)
				w.do("a1 turns the kill switch on, its answer lost, its read again failed", func() {
					_, err := w.setSwitch(a, true, killing(0))
					require.ErrorIs(t, err, errAnswerLost)
				})
				id := w.opened(a)
				_, after := w.lifecycles()
				require.True(t, after.KillSwitchUnsettled)
				require.Equal(t, []item.ID{id}, after.Waiting)
				require.Less(t, after.Investigating, after.Limit)
			}},
		{"4: records of more items investigating than the limit and one, appended by the test", theLimitAndOne, stageEvent,
			4, "the records hold 4 items investigating, above the limit 2 and one",
			func(_ *testing.T, w *world, _, _ *instance) {
				for i := range worldLimit + 2 {
					w.appendDirect(item.ID(fmt.Sprintf("x%d", i)), item.Body{Opened: openedBody()}, item.Body{Started: startedBody()})
				}
			}},
		{"5: a record that does not fold", recordsAndEpochs, stageEvent,
			5, "the records of item x1 cannot be read or do not fold: record 1: item: invalid record: an item opens with its first record, once",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Started: startedBody()})
			}},
		{"5: a write without an epoch, which no guard checked", recordsAndEpochs, stageEvent,
			5, "a write to the records store carried epoch 0, while the lease's was 1",
			func(t *testing.T, w *world, a, _ *instance) {
				w.armFault(faultSkipGuard)
				w.do("a1's records take an opening that carries no epoch", func() {
					_, err := recordsPort{a}.Append(ctx, "x1", openingDraft("x1"))
					require.NoError(t, err)
				})
			}},
		{"5: a write under an epoch the lease moved past, which no guard checked", recordsAndEpochs, stageEvent,
			5, "a write to the records store carried epoch 1, while the lease's was 2",
			func(t *testing.T, w *world, a, b *instance) {
				epoch := epochOf(a)
				w.do("a1 crashes", func() { w.discard(a) })
				w.do("time passes the lease's term", func() { w.passTime(worldTTL) })
				require.Same(t, b, w.leading())
				w.armFault(faultSkipGuard)
				w.do("a1's records take an opening under its old epoch", func() {
					_, err := recordsPort{a}.Append(holding.WithEpoch(ctx, epoch), "x1", openingDraft("x1"))
					require.NoError(t, err)
				})
			}},
		{"6: one request's key on two records of an item", aKeyNamesOneActOfItsItem, stageEvent,
			6, "item x1 holds the key " + keyHash("k-1") + " on two records, 2 and 3",
			func(_ *testing.T, w *world, _, _ *instance) {
				defer w.flying("x1", actClose, "k-1")()
				defer w.flying("x1", actReopen, "k-1")()
				w.appendDirect("x1", item.Body{Opened: openedBody()},
					item.Body{Closed: &item.Closed{By: "admin", Request: &item.Request{Key: keyHash("k-1"), Digest: actDigest(actClose, "x1")}}},
					item.Body{Reopened: &item.Reopened{
						By: "admin", Request: item.Request{Key: keyHash("k-1"), Digest: actDigest(actReopen, "x1")},
						AwaitingClose: item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseClosed},
					}})
			}},
		{"6: a record that lands with the digest of another act than the one in flight under its key", aKeyNamesOneActOfItsItem, stageEvent,
			6, "item x1's record 2 holds the key " + keyHash("k-1") + " with the digest " + actDigest(actClose, "y1") +
				", not that of an act in flight under the key: [" + actDigest(actClose, "x1") + "]",
			func(_ *testing.T, w *world, _, _ *instance) {
				defer w.flying("x1", actClose, "k-1")()
				w.appendDirect("x1", item.Body{Opened: openedBody()},
					item.Body{Closed: &item.Closed{By: "admin", Request: &item.Request{Key: keyHash("k-1"), Digest: actDigest(actClose, "y1")}}})
			}},
		{"6: an opening that lands with the digest of another request than the one in flight under its key", aKeyNamesOneActOfItsItem, stageEvent,
			6, "item x1 opened with the digest sha256:other, not that of an opening in flight under its key: [sha256:open]",
			func(_ *testing.T, w *world, _, _ *instance) {
				defer w.openingFlying("x1", "sha256:open")()
				opened := openedBody()
				opened.Digest = "sha256:other"
				w.appendDirect("x1", item.Body{Opened: opened})
			}},
		{"6 keeps: one key on two items, each its own act", aKeyNamesOneActOfItsItem, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				x, y := w.opened(a), w.opened(a)
				w.do("a1 closes both under one key", func() {
					w.act(a, actClose, x, closing("k-1"))
					w.act(a, actClose, y, closing("k-1"))
				})
				assert.Equal(t, item.StatusClosed, w.stateOf(x).Status())
				assert.Equal(t, item.StatusClosed, w.stateOf(y).Status())
			}},
		{"7: a stopped run with a live session", aStoppedRunLeavesNothingRunning, stageQuiet,
			7, "item id-1's run id-5 stopped, and a1 keeps the session of release-investigator live",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				run := w.runOf(id)
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("k-1")) })
				require.NotNil(t, w.stateOf(id).Run.Stopped)
				w.do("a session of its run stays live", func() {
					w.mu.Lock()
					a.live[sessionKey{run, "release-investigator"}] = &liveSession{epoch: epochOf(a), round: 1}
					w.mu.Unlock()
				})
			}},
		{"7: a stopped run whose upstream sessions were last closed before its stop committed", aStoppedRunLeavesNothingRunning, stageQuiet,
			7, "item id-1's run id-5 stopped at commit 6, and its upstream sessions were last closed after commit 0",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				run := w.runOf(id)
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("k-1")) })
				require.NotNil(t, w.stateOf(id).Run.Stopped)
				w.mu.Lock()
				w.closedRuns[run] = 0
				w.mu.Unlock()
			}},
		{"8: an automatic close recorded while the latest row is an on the instance's view has reached", autoCloseKeepsItsPauseAndItsDeadline, stageEvent,
			8, "a1 recorded item id-1's automatic close once its view reached on-row 1, which the driver wrote out of band",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.unstaffedAtTheCap(a)
				w.do("the driver writes an on-row out of band", func() { w.writeRow(true) })
				w.do("time passes to the next renewal, whose check reads the row", func() { w.passTime(worldRenewEvery) })
				require.Equal(t, int64(1), a.coord.KillSwitchSeen())
				w.do("time passes a minute, auto-close paused", func() { w.passTime(time.Minute) })
				due := w.stateOf(id).AutoCloseAt(worldAutoClose, time.Time{})
				w.do("a1's records take an automatic close", func() {
					_, err := recordsPort{a}.Append(holding.WithEpoch(ctx, epochOf(a)), id, autoCloseDraft(id, due))
					require.NoError(t, err)
				})
			}},
		{"8: an automatic close stamped before its due", autoCloseKeepsItsPauseAndItsDeadline, stageEvent,
			8, "item id-1's automatic close is stamped 2026-11-02 09:00:59.000001 +0000 UTC, before its due 2026-11-02 09:01:00.000001 +0000 UTC",
			func(_ *testing.T, w *world, a, _ *instance) {
				id := w.unstaffedAtTheCap(a)
				s := w.stateOf(id)
				due := s.AutoCloseAt(worldAutoClose, time.Time{})
				w.mu.Lock()
				w.autoClosed = append(w.autoClosed, autoCloseNote{
					inst: a.name, before: s, rec: item.Record{Item: id, At: due.Add(-time.Second), Body: item.Body{Closed: &item.Closed{Auto: true, Due: due}}},
				})
				w.mu.Unlock()
			}},
		{"8: an automatic close due at another time than the deadline its state gives", autoCloseKeepsItsPauseAndItsDeadline, stageEvent,
			8, "a1 recorded item id-1's automatic close due at 2026-11-02 09:02:00.000001 +0000 UTC, not at 2026-11-02 09:01:00.000001 +0000 UTC, which its state before it gives",
			func(_ *testing.T, w *world, a, _ *instance) {
				id := w.unstaffedAtTheCap(a)
				s := w.stateOf(id)
				due := s.AutoCloseAt(worldAutoClose, time.Time{}).Add(time.Minute)
				w.mu.Lock()
				w.autoClosed = append(w.autoClosed, autoCloseNote{
					inst: a.name, before: s, rec: item.Record{Item: id, At: due, Body: item.Body{Closed: &item.Closed{Auto: true, Due: due}}},
				})
				w.mu.Unlock()
			}},
		{"9: an item admitted while an older one, by its opening's time, stays waiting", theQueuesOrder, stageEvent,
			9, "item y1, admitted by the walk, does not rank ahead of item x1, still waiting: their openings' times are 2026-11-02 09:00:01 +0000 UTC and 2026-11-02 09:00:00 +0000 UTC",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.do("y1 starts, and x1 waits", func() { w.appendDirect("y1", item.Body{Started: startedBody()}) })
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1", "y1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1"}}
			}},
		{"9 keeps: two items admitted in the order of their openings' times, which a step back of the database's clock put out of their commit order",
			theQueuesOrder, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				first, second := w.opened(a), w.opened(a)
				x := w.opened(a)
				w.do("the database's clock steps -1m", func() { w.stepClock(-1, -time.Minute) })
				y := w.opened(a)
				_, after := w.lifecycles()
				require.Equal(t, []item.ID{y, x}, after.Waiting, "y, opened after x, ranks first by its opening's time")
				w.do("a1 closes the first item", func() { w.act(a, actClose, first, closing("k-1")) })
				require.NoError(t, theQueuesOrder(w, stageEvent))
				_, after = w.lifecycles()
				require.Equal(t, []item.ID{x}, after.Waiting, "the walk admitted y")
				w.do("a1 closes the second item", func() { w.act(a, actClose, second, closing("k-2")) })
				_, after = w.lifecycles()
				require.Empty(t, after.Waiting, "the walk admitted x")
			}},
		{"9 keeps: an item that leaves the waiting list as the coordinator learns of its start, committed before the event with its answer lost",
			theQueuesOrder, stageEvent, 0, "",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.appendDirect("x1", item.Body{Started: startedBody()})
				w.do("the coordinator loads x1, and learns it started", func() {})
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"y1", "x1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"y1"}}
			}},
		{"9 keeps: an item opened with its answer lost and its lookup failed, which the coordinator does not hold yet, while an older item waits",
			theQueuesOrder, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				for range worldLimit + 1 {
					w.opened(a)
				}
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.armFault(faultRecordsLost)
				w.armRead(a, faultRecordsRead)
				w.do("a1 opens an item, its answer lost, its lookup failed", func() { w.open(a, openInput()) })
				_, after := w.lifecycles()
				require.Len(t, after.Waiting, 1, "the older item waits, and the coordinator does not hold the new one")
				require.Len(t, w.records(), worldLimit+2, "the new item's opening landed")
			}},
		{"9 keeps: younger items admitted before the coordinator holds an older one, whose opening's answer was lost and its lookup failed, which its retry takes in waiting",
			theQueuesOrder, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				w.armFault(faultRecordsLost)
				w.armRead(a, faultRecordsRead)
				w.do("a1 opens an item, its answer lost, its lookup failed", func() { w.open(a, openInput()) })
				require.Len(t, w.records(), 1, "its opening landed")
				require.Equal(t, app.Lifecycle{Limit: worldLimit}, a.coord.Lifecycle(), "the coordinator does not hold it")
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.do("a1 opens two items, which start, and time passes the retry wait", func() {
					w.open(a, openInput())
					w.open(a, openInput())
					w.passTime(worldRetry)
				})
				_, after := w.lifecycles()
				require.Len(t, after.Waiting, 1, "the retry took the older item in, waiting")
			}},
		{"9 keeps: the promotion of the queue's last item while older items wait", theQueuesOrder, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				for range worldLimit + 3 {
					w.opened(a)
				}
				_, after := w.lifecycles()
				require.Len(t, after.Waiting, 3)
				last := after.Waiting[2]
				w.do("a1 promotes the queue's last item", func() { w.act(a, actPromote, last, promoting("k-1")) })
				_, after = w.lifecycles()
				require.Len(t, after.Waiting, 2)
				require.NotContains(t, after.Waiting, last)
			}},
		{"10: a start the fake runner is told to make for a run whose item's close committed", closedItemsRunNothing, stageEvent,
			10, "a1 called turns.Start for run id-5, agent release-investigator, under epoch 1, after item id-1's close committed",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				run := w.runOf(id)
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("k-1")) })
				w.do("a1's runner is told to start a turn of its run", func() { require.NoError(t, startTurnOf(a, epochOf(a), run)) })
			}},
		{"10: a session moved into a round of a run whose item's close committed", closedItemsRunNothing, stageEvent,
			10, "a1 called sessions.Move for run id-5, agent release-investigator, under epoch 1, after item id-1's close committed",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				run := w.runOf(id)
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("k-1")) })
				w.do("a session of its run stays live, and moves into round 2", func() {
					w.mu.Lock()
					a.live[sessionKey{run, "release-investigator"}] = &liveSession{epoch: epochOf(a), round: 1}
					w.mu.Unlock()
					require.NoError(t, sessionsPort{a}.Move(run, "release-investigator", 2, w.dbNow().Add(time.Minute), 0))
				})
			}},
		{"10: a launch that completes into a running turn after the close, the instance still holding", closedItemsRunNothing, stageEvent,
			10, "item id-1's close committed, and a1's runner still runs the turn of id-5 release-investigator",
			func(t *testing.T, w *world, a, _ *instance) {
				w.pending = true
				id := w.opened(a)
				run := w.runOf(id)
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("k-1")) })
				rt := &runnerTurn{turn: app.Turn{ID: "turn-late", Run: run, Agent: "release-investigator", Round: 1}, epoch: epochOf(a), launching: true}
				w.mu.Lock()
				a.turns = append(a.turns, rt)
				w.mu.Unlock()
				w.do("a launch of its run, left pending, completes", func() { w.launch(a, rt, "") })
			}},
		{"10: a live session of the run at an event's end after the close", closedItemsRunNothing, stageEvent,
			10, "item id-1's close committed, and a1 keeps the session of id-5 release-investigator live",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				run := w.runOf(id)
				w.do("a1 closes it, and a session of its run stays live", func() {
					w.act(a, actClose, id, closing("k-1"))
					w.mu.Lock()
					a.live[sessionKey{run, "release-investigator"}] = &liveSession{epoch: epochOf(a), round: 1}
					w.mu.Unlock()
				})
			}},
		{"10 keeps: a start called after the close by an instance whose holding had ended, which the runner refused", closedItemsRunNothing, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				run := w.runOf(id)
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("k-1")) })
				epoch := epochOf(a)
				w.do("host a's wall clock steps 1m, which ends a1's holding", func() { w.stepClock(0, time.Minute) })
				require.False(t, a.hold.Valid(epoch))
				w.do("a1's runner is told to start a turn of its run", func() { require.ErrorIs(t, startTurnOf(a, epoch, run), app.ErrUnheld) })
			}},
		{"10 keeps: a turn started after a close and its reopen", closedItemsRunNothing, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				run := w.runOf(id)
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("k-1")) })
				w.do("a1 reopens it", func() { w.act(a, actReopen, id, reopening("k-2")) })
				require.NotEqual(t, item.StatusClosed, w.stateOf(id).Status())
				w.do("a1's runner is told to start a turn of its run", func() { require.NoError(t, startTurnOf(a, epochOf(a), run)) })
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, 1, sequential)
			w.pairing = pairNever
			c.run(t, w, w.inst(0), w.inst(1))
			require.NoError(t, w.failed())
			err := c.check(w, c.stage)
			if c.want == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			if c.broke > 0 {
				assert.Contains(t, err.Error(), fmt.Sprintf("invariant %d: ", c.broke))
			}
			assert.Contains(t, err.Error(), c.want)
			t.Log(err)
		})
	}
	t.Run("every check together keeps a world that broke none", func(t *testing.T) {
		w := newWorld(t, 1, sequential)
		a := w.inst(0)
		for range worldLimit + 1 {
			w.opened(a)
		}
		for _, stage := range []string{stageEvent, stageSettled, stageQuiet} {
			require.NoError(t, lifecycleInvariants(w, stage), stage)
		}
	})
}
