package app_test

import (
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

// The nine invariants of the interleaving test (The interleaving test; R36 to
// R42): each is a check of a world at a stage, after each event, or each batch
// of the concurrent mode's, and at the end of each settling stage, where its
// line says. The ports note what invariants 1 and 8 judge, at each call that
// starts work and at each automatic close: the table's latest row, the calling
// instance's view and its holding, as each stood at the call; the checks judge
// the notes after the event. Each check judges times only by comparing two of
// the database's, never against the order the events ran in, so that every
// invariant holds under clock steps both ways (R39).

// outOfBand is who the driver writes the kill switch's rows as, out of band,
// as a psql session, a restore or a writer that crashed leaves them: a row a
// coordinator wrote names the person whose act wrote it.
const outOfBand = "psql"

// observer is what the checks keep of a world, under w.mu: the stores'
// commits, a record or a kill switch row each, numbered in the order they
// committed, and each record's number and its append's; the records the event
// under way committed, or the concurrent mode's batch; what the ports noted
// at each call that starts work and at each automatic close (invariants 1 and
// 8); the kill switch's ons the driver sent and their success answers, and
// the commit each one's request came after; the commit after which each run's
// upstream sessions were last closed (invariant 7); the acts and openings the
// driver sent, by item and key (invariant 6); each live instance's snapshot
// around the event; how far the checks read each kind of note; and, for the
// violation table alone, a store's refusal of what an append would commit.
// folded, each item's records and their fold as the checks last read them, is
// the checks' own, on the driver's goroutine.
type observer struct {
	order, appends int64
	recordOrder    map[string]int64
	events         []eventRecord
	works          []workNote
	autoClosed     []autoCloseNote
	answers        []onAnswer
	cameAt         map[string]int64
	closedRuns     map[board.RunID]int64
	sentActs       map[sentKey][]string
	sentOpenings   map[item.ID][]string
	views          []instView

	checkedWorks, checkedCloses, checkedAnswers int

	refuse func(id item.ID, drafts []item.Draft) error
	folded map[item.ID]*foldedItem
}

func newObserver() observer {
	return observer{
		recordOrder: map[string]int64{}, cameAt: map[string]int64{}, closedRuns: map[board.RunID]int64{},
		sentActs: map[sentKey][]string{}, sentOpenings: map[item.ID][]string{}, folded: map[item.ID]*foldedItem{},
	}
}

// eventRecord is a record the event under way committed, and the number of
// the append that committed it.
type eventRecord struct {
	rec    item.Record
	append int64
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

// sentKey names the acts the driver sent to an item under a key, as records
// keep the key: its hash.
type sentKey struct {
	id  item.ID
	key string
}

// instView is a live instance's coordinator's snapshot before an event, empty
// for one the event booted, and after it.
type instView struct {
	in            *instance
	before, after app.Lifecycle
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

// noteWork notes a call of in's that starts work, call, for run and agent,
// under epoch (invariant 1): the table's latest row, then in's view, then
// whether in's holding is valid for epoch, which it returns, for the runner to
// judge the call by. It reads the row first and the holding last, as the
// concurrent mode needs: a row another instance writes comes after that
// instance took the lease, by when in's holding is no longer valid, as the
// lease's premise has it; and a row in's own coordinator writes, like any
// move of its view, comes under the lock it calls under. Its caller holds no
// lock of the world's.
func (w *world) noteWork(in *instance, call string, run board.RunID, agent string, epoch int64) (valid bool) {
	latest, _ := w.switchStore.KillSwitch(context.Background())
	n := workNote{inst: in.name, call: call, run: run, agent: agent, epoch: epoch, latest: latest, seen: in.coord.KillSwitchSeen()}
	n.valid = in.validFor(epoch)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.works = append(w.works, n)
	return n.valid
}

// committedRecords notes the records an append of in's committed for item id,
// after history: their numbers in the order of the stores' commits, the
// append's, the item to read again, and, for each automatic close, what
// invariant 8 judges at it, with the table's latest row and the view of in's
// coordinator, which holds its lock as it records, read now. in is nil for the
// violation table's own appends. Its caller holds the lease's row lock
// shared, as the commit did, and no lock of the world's.
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
		w.events = append(w.events, eventRecord{rec: committed[i], append: w.appends})
	}
	w.dirty[id] = true
	if !w.itemSeen[id] {
		w.itemSeen[id] = true
		w.itemIDs = append(w.itemIDs, id)
	}
	w.autoClosed = append(w.autoClosed, closes...)
}

// came notes that a kill switch's on, sent as subject, came to the kill
// switch's store, after the commits so far: the runs then running are those
// its success answer names (invariant 1).
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

// noteSent notes an act the driver sends to item id under key: a record of it
// would keep the key's hash and the act's digest (invariant 6).
func (w *world) noteSent(id item.ID, act, key string) {
	k, d := sentKey{id, keyHash(key)}, actDigest(act, id)
	w.mu.Lock()
	defer w.mu.Unlock()
	if !slices.Contains(w.sentActs[k], d) {
		w.sentActs[k] = append(w.sentActs[k], d)
	}
}

// noteOpening notes an opening the driver sends under its item's key, with
// its request's digest (invariant 6).
func (w *world) noteOpening(id item.ID, digest string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !slices.Contains(w.sentOpenings[id], digest) {
		w.sentOpenings[id] = append(w.sentOpenings[id], digest)
	}
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
// creation and of its run's stop in the order of the stores' commits, 0 for
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

// lifecycleInvariants checks the nine invariants, in their order, each where
// its line applies, and returns the first that broke.
func lifecycleInvariants(w *world, stage string) error {
	for _, check := range []func(*world, string) error{
		noWorkWhileOn, everyRunAliveAtAnOnIsHalted, noQueuedItemWaitsWhileASlotIsFree, theLimitAndOne, recordsAndEpochs,
		aKeyNamesOneActOfItsItem, aStoppedRunLeavesNothingRunning, autoCloseKeepsItsPauseAndItsDeadline, theQueuesOrder,
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
// the driver wrote it out of band. A call counts only while the calling
// instance's holding is valid for its epoch: one made after its holding ended
// is refused downstream, by the runner and by the gateways. After each event
// in which an instance's view turned on, the instance has no live session and
// its runner runs no turn; and an on's success answer names every run that
// ran when the on came in its Stopped or its Owed. A drain's on, answered
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
		if err := w.stillAfterOn(&views[i]); err != nil {
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

// whileOn judges a call that starts work by the table's latest row and the
// caller's view, as they stood at the call.
func (n *workNote) whileOn() error {
	switch {
	case !n.valid || !n.latest.On:
		return nil
	case n.latest.By != outOfBand:
		return broke(1, "%s called %s for run %s, agent %s, under epoch %d, after on-row %d committed, which a coordinator wrote",
			n.inst, n.call, n.run, n.agent, n.epoch, n.latest.Seq)
	case n.seen >= n.latest.Seq:
		return broke(1, "%s called %s for run %s, agent %s, under epoch %d, once its view reached on-row %d, which the driver wrote out of band",
			n.inst, n.call, n.run, n.agent, n.epoch, n.latest.Seq)
	}
	return nil
}

// stillAfterOn checks, of an instance whose view turned on in the event, to a
// latest on-row it did not hold before, that it keeps no session live and
// that its runner runs no turn: the on's steps, or the fence before a
// takeover, stopped every turn and ended every session, and nothing starts
// while the view is on.
func (w *world) stillAfterOn(v *instView) error {
	before, after := v.before.KillSwitch, v.after.KillSwitch
	if !after.On || before.On && before.OnSeq == after.OnSeq {
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
		return broke(1, "%s's view turned on, to on-row %d, and it keeps the sessions %v live", v.in.name, after.OnSeq, live)
	}
	for _, rt := range v.in.turns {
		if !rt.stopped {
			return broke(1, "%s's view turned on, to on-row %d, and its runner still runs the turn of %s %s", v.in.name, after.OnSeq, rt.turn.Run, rt.turn.Agent)
		}
	}
	return nil
}

// namesEveryRun checks that an on's success answer names, in its Stopped or
// its Owed, every run that ran when the on came: one whose creation committed
// before the on's request came to the kill switch's store, and whose stop, if
// any, committed after.
func (w *world) namesEveryRun(a *onAnswer) error {
	w.mu.Lock()
	came, ok := w.cameAt[a.subject]
	w.mu.Unlock()
	if !ok {
		return nil
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
// records of an item hold one request's key, and each key an item's records
// hold carries the digest of an act the driver sent to that item under it,
// so, the key held once, of the one act under it that landed: an act refused
// before its record, or whose append did not land, leaves the key free for
// the next. An opening by key carries the digest of an opening the driver
// sent under it. The same key on two items names two acts, one on each.
func aKeyNamesOneActOfItsItem(w *world, _ string) error {
	w.observe()
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, id := range w.observed() {
		keys := map[string]int64{}
		records := w.folded[id].records
		for i := range records {
			r := &records[i]
			if o := r.Body.Opened; o != nil && o.Digest != "" && !slices.Contains(w.sentOpenings[id], o.Digest) {
				return broke(6, "item %s opened with the digest %s, of no opening the driver sent under its key", id, o.Digest)
			}
			req := requestHeld(&r.Body)
			if req == nil {
				continue
			}
			if seq, ok := keys[req.Key]; ok {
				return broke(6, "item %s holds the key %s on two records, %d and %d", id, req.Key, seq, r.Seq)
			}
			keys[req.Key] = r.Seq
			if !slices.Contains(w.sentActs[sentKey{id, req.Key}], req.Digest) {
				return broke(6, "item %s's record %d holds the key %s with the digest %s, of no act the driver sent the item under the key",
					id, r.Seq, req.Key, req.Digest)
			}
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
// the recording instance's view reached it, as in invariant 1; its due is the
// deadline its item's state before it gives (R19), with the off time of the
// recording instance's view, not the table's latest off-row's, which a row
// written out of band and not yet seen can move; and the time its record is
// stamped with, the database's, is no earlier (R41). An item whose state
// before it does not fold is invariant 5's.
func (n *autoCloseNote) keeps() error {
	switch {
	case n.latest.On && n.latest.By != outOfBand:
		return broke(8, "%s recorded item %s's automatic close after on-row %d committed, which a coordinator wrote", n.inst, n.rec.Item, n.latest.Seq)
	case n.latest.On && n.seen >= n.latest.Seq:
		return broke(8, "%s recorded item %s's automatic close once its view reached on-row %d, which the driver wrote out of band", n.inst, n.rec.Item, n.latest.Seq)
	case n.before == nil || n.before.Closing == nil:
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
// list in the event with no item_promoted committed for it in the event, no
// close, and no not staffed for the kill switch: it started, or was not
// staffed at its start for the daily cap, its routing or its type, or waits
// for its start's retry, admitted. A promotion past older waiting items is
// correct, and no admission. Against the order in which the openings
// committed, the rank tolerates only two items whose openings' times are out
// of their commit order, which a step back of the database's clock between
// them makes, R9's residual, and, with their times equal, the order of their
// ids, as R9 gives it. The snapshot before is the last one a leading
// coordinator published, and an event after which none leads is not judged.
// An item absent from the snapshots, whose records the coordinator could not
// read, holds up no other. In the concurrent mode an item opened in the batch
// may have come after an admission of the batch's, so the items still waiting
// are those that waited before the batch.
func theQueuesOrder(w *world, _ string) error {
	before, after := w.lifecycles()
	if after.Limit == 0 {
		return nil
	}
	w.observe()
	w.mu.Lock()
	events := slices.Clone(w.events)
	w.mu.Unlock()
	opened, left := queuedAndLeft(events)
	waiting, waited := setOf(after.Waiting), setOf(before.Waiting)
	var admitted []item.ID
	for _, id := range slices.Concat(before.Waiting, opened) {
		if !waiting[id] && !left[id] && !slices.Contains(admitted, id) {
			admitted = append(admitted, id)
		}
	}
	for _, still := range after.Waiting {
		if w.mode == concurrent && !waited[still] {
			continue
		}
		for _, a := range admitted {
			if !w.ranksAhead(a, still) {
				return broke(9, "item %s, admitted by the walk, does not rank ahead of item %s, still waiting: their openings' times are %s and %s",
					a, still, w.openedAt(a), w.openedAt(still))
			}
		}
	}
	return nil
}

// queuedAndLeft returns, from the records an event committed, the items it
// opened, and the items that left the queue in it for another cause than the
// walk: a promotion, a close, not staffed for the kill switch, or not staffed
// at its opening, in the opening's own append, which never waited.
func queuedAndLeft(events []eventRecord) (opened []item.ID, left map[item.ID]bool) {
	left = map[item.ID]bool{}
	openedIn := map[item.ID]int64{}
	for i := range events {
		e := &events[i]
		id, b := e.rec.Item, &e.rec.Body
		switch {
		case b.Opened != nil:
			openedIn[id] = e.append
			opened = append(opened, id)
		case b.Promoted != nil, b.Closed != nil:
			left[id] = true
		case b.AwaitingClose != nil:
			if a, ok := openedIn[id]; b.AwaitingClose.Cause == item.CauseKillSwitch || ok && a == e.append {
				left[id] = true
			}
		}
	}
	return opened, left
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

// The violation table's fixtures.

// appendDirect appends records of bodies to item id straight to the records
// store, past every port and guard, as the violation table's fixtures do, and
// notes them as the stores' commits are noted, but for any write: no guarded
// store committed them.
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
			drafts = append(drafts, item.Draft{ID: worldIDs{w}.NewID(), Item: id, Incident: incident, Body: b})
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
// naming the invariant; and each check passes the worlds its invariant
// allows, so that no check is stricter than its invariant (The interleaving
// test). A check that cannot fail would let every run pass for nothing.
// Fails first: a build failure, no checks.
func TestEachInterleavingInvariantCatchesItsViolation(t *testing.T) {
	ctx := context.Background()
	epochOf := func(in *instance) int64 {
		epoch, _, _ := in.leader.Status()
		return epoch
	}
	startTurn := func(in *instance, epoch int64) error {
		_, err := runnerPort{in}.Start(holding.WithEpoch(ctx, epoch), &app.Turn{ID: "turn-1", Run: "run-1", Agent: "release-investigator", Round: 1})
		return err
	}
	for _, c := range []struct {
		name  string
		check func(w *world, stage string) error
		stage string
		// broke is the invariant the row breaks, 0 for a row that keeps it.
		broke int
		run   func(t *testing.T, w *world, a, b *instance)
	}{
		{"1: a start the fake runner is told to make after an on-row a coordinator wrote committed, its answer lost", noWorkWhileOn, stageEvent, 1,
			func(t *testing.T, w *world, a, _ *instance) {
				w.armFault(faultSwitchLost)
				w.do("a1 turns the kill switch on, its answer lost", func() { _, _ = w.setSwitch(a, true, killing(0)) })
				require.Equal(t, int64(1), w.latestRow())
				w.do("a1's runner is told to start a turn", func() { require.NoError(t, startTurn(a, epochOf(a))) })
			}},
		{"1 keeps: a turn started after an on-row the driver wrote, before the instance's view reached it", noWorkWhileOn, stageEvent, 0,
			func(t *testing.T, w *world, a, _ *instance) {
				w.do("the driver writes an on-row out of band", func() { w.writeRow(true) })
				require.Zero(t, a.coord.KillSwitchSeen())
				w.do("a1's runner starts a turn", func() { require.NoError(t, startTurn(a, epochOf(a))) })
			}},
		{"1 keeps: a start called by an instance whose holding had ended, which the runner refused", noWorkWhileOn, stageEvent, 0,
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
		{"1: an instance whose view turned on keeps a session live", noWorkWhileOn, stageEvent, 1,
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
		{"1: an on's success answer that names no stop and nothing owed of a run that ran when it came", noWorkWhileOn, stageEvent, 1,
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
		{"2: an item left owing the latest on-row after the first settling stage, its halt refused by a store that does not recover",
			everyRunAliveAtAnOnIsHalted, stageSettled, 2,
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
		{"3: a snapshot that holds a waiting item below the limit, its view settled", noQueuedItemWaitsWhileASlotIsFree, stageEvent, 3,
			func(_ *testing.T, w *world, _, _ *instance) {
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit - 1, Waiting: []item.ID{"x1"}}
			}},
		{"3: records that hold a queued item, once quiet, with a slot free", noQueuedItemWaitsWhileASlotIsFree, stageQuiet, 3,
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
			}},
		{"3 keeps: a waiting item below the limit while the view is unsettled", noQueuedItemWaitsWhileASlotIsFree, stageEvent, 0,
			func(t *testing.T, w *world, a, _ *instance) {
				w.armFault(faultSwitchLost)
				w.armFault(faultSwitchRead)
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
		{"4: records of more items investigating than the limit and one, appended by the test", theLimitAndOne, stageEvent, 4,
			func(_ *testing.T, w *world, _, _ *instance) {
				for i := range worldLimit + 2 {
					w.appendDirect(item.ID(fmt.Sprintf("x%d", i)), item.Body{Opened: openedBody()}, item.Body{Started: startedBody()})
				}
			}},
		{"5: a record that does not fold", recordsAndEpochs, stageEvent, 5,
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Started: startedBody()})
			}},
		{"5: a write without an epoch, which no guard checked", recordsAndEpochs, stageEvent, 5,
			func(t *testing.T, w *world, a, _ *instance) {
				w.armFault(faultSkipGuard)
				w.do("a1's records take an opening that carries no epoch", func() {
					_, err := recordsPort{a}.Append(ctx, "x1", openingDraft("x1"))
					require.NoError(t, err)
				})
			}},
		{"5: a write under an epoch the lease moved past, which no guard checked", recordsAndEpochs, stageEvent, 5,
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
		{"6: one request's key on two records of an item", aKeyNamesOneActOfItsItem, stageEvent, 6,
			func(_ *testing.T, w *world, _, _ *instance) {
				w.noteSent("x1", actClose, "k-1")
				w.noteSent("x1", actReopen, "k-1")
				w.appendDirect("x1", item.Body{Opened: openedBody()},
					item.Body{Closed: &item.Closed{By: "admin", Request: &item.Request{Key: keyHash("k-1"), Digest: actDigest(actClose, "x1")}}},
					item.Body{Reopened: &item.Reopened{
						By: "admin", Request: item.Request{Key: keyHash("k-1"), Digest: actDigest(actReopen, "x1")},
						AwaitingClose: item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseClosed},
					}})
			}},
		{"6: a key held with the digest of an act the driver never sent the item under it", aKeyNamesOneActOfItsItem, stageEvent, 6,
			func(_ *testing.T, w *world, _, _ *instance) {
				w.noteSent("x1", actClose, "k-1")
				w.appendDirect("x1", item.Body{Opened: openedBody()},
					item.Body{Closed: &item.Closed{By: "admin", Request: &item.Request{Key: keyHash("k-1"), Digest: actDigest(actClose, "y1")}}})
			}},
		{"6 keeps: one key on two items, each its own act", aKeyNamesOneActOfItsItem, stageEvent, 0,
			func(t *testing.T, w *world, a, _ *instance) {
				x, y := w.opened(a), w.opened(a)
				w.do("a1 closes both under one key", func() {
					w.act(a, actClose, x, closing("k-1"))
					w.act(a, actClose, y, closing("k-1"))
				})
				assert.Equal(t, item.StatusClosed, w.stateOf(x).Status())
				assert.Equal(t, item.StatusClosed, w.stateOf(y).Status())
			}},
		{"7: a stopped run with a live session", aStoppedRunLeavesNothingRunning, stageQuiet, 7,
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
		{"7: a stopped run whose upstream sessions were last closed before its stop committed", aStoppedRunLeavesNothingRunning, stageQuiet, 7,
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				run := w.runOf(id)
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("k-1")) })
				require.NotNil(t, w.stateOf(id).Run.Stopped)
				w.mu.Lock()
				w.closedRuns[run] = 0
				w.mu.Unlock()
			}},
		{"8: an automatic close recorded while the latest row is an on the instance's view has reached", autoCloseKeepsItsPauseAndItsDeadline, stageEvent, 8,
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
		{"8: an automatic close stamped before its due", autoCloseKeepsItsPauseAndItsDeadline, stageEvent, 8,
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
		{"8: an automatic close due at another time than the deadline its state gives", autoCloseKeepsItsPauseAndItsDeadline, stageEvent, 8,
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
		{"9: an item admitted while an older one, by its opening's time, stays waiting", theQueuesOrder, stageEvent, 9,
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.do("y1 starts, and x1 waits", func() { w.appendDirect("y1", item.Body{Started: startedBody()}) })
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1", "y1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1"}}
			}},
		{"9 keeps: two items admitted in the order of their openings' times, which a step back of the database's clock put out of their commit order",
			theQueuesOrder, stageEvent, 0,
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
		{"9 keeps: the promotion of the queue's last item while older items wait", theQueuesOrder, stageEvent, 0,
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
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, 1, sequential)
			c.run(t, w, w.inst(0), w.inst(1))
			require.NoError(t, w.failed())
			err := c.check(w, c.stage)
			if c.broke == 0 {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), fmt.Sprintf("invariant %d:", c.broke))
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
