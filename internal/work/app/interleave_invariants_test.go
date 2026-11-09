package app_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/inbound/mapping"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The nineteen invariants of the interleaving test (The interleaving test; R36 to
// R42; R48): each is a check of a world at a stage, after each event, or each batch
// of the concurrent mode's, and at the end of each settling stage, where its
// line says. The ports note what invariants 1, 8 and 10 judge, at each call
// that starts work and at each automatic close: the table's latest row, the calling
// instance's view and its holding, as each stood at the call; the checks judge
// the notes after the event. Each check judges times only by comparing two of
// the database's, never against the order the events ran in, so that every
// invariant holds under clock steps both ways (R39). Invariants 11 to 19 are
// the inbound events' (design D14): the rows and the item_triggered records
// the ports note, each instance's account of each item, and the Ingests the
// driver sends.

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
// each such item first, and the items whose promotion by a person, since the
// snapshot the checks judge against, has an unknown outcome (invariant 9);
// each run's item, the position in the world's log of each item's
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
	unsure           map[item.ID]bool
	closedFaults     []error

	// steps numbers, in one order, the records' commits (recordStep), the
	// reads that took an item whose opening's answer was lost into the
	// account (heldAt), and the appends of an item's records made while the
	// caller's coordinator did not hold the item waiting (tries), so that
	// invariant 9 tells when the walk admitted an item from when its start
	// committed (see heldBefore). recordBy names the holding, an instance and
	// its epoch, whose guarded append committed a record, and each try the
	// holding that made it: only a try of the holding that committed the
	// item's exit, or of the leading one, dates its admission.
	steps      int64
	recordStep map[string]int64
	recordBy   map[string]tryHolder
	tries      map[item.ID][]try

	checkedWorks, checkedCloses, checkedAnswers, checkedKeyFaults, checkedClosedFaults int

	refuse func(id item.ID, drafts []item.Draft) error
	folded map[item.ID]*foldedItem
	// refolded holds the items observe read again since invariant 5 last
	// checked their incidents (see incidentsFold); movesChecked the records
	// of links and unlinks it checked against their windows and the incident
	// left (see movedRightly); killsChecked the run stops killed invariant 2
	// checked against the rows their runs had seen (see killsOwed).
	refolded     map[item.ID]bool
	movesChecked map[string]bool
	killsChecked map[string]bool
	// seenFaults are resumes that recorded a kill switch row below the one
	// their coordinator knew (invariant 2; see judgeResumeSeen); moves are the
	// sessions' moves and their budgets (invariant 5; see movedWithin); lost
	// holds the records whose append lost its answer.
	seenFaults []error
	moves      []moveNote
	lost       map[string]bool
	// ruledAt holds, for each link or unlink the driver's act committed in
	// the sequential mode, the database's time as its coordinator's clock
	// read it as the act came, which judges its window (see movedRightly);
	// direct holds the records the violation table appended past every port.
	ruledAt                         map[string]time.Time
	direct                          map[string]bool
	checkedSeenFaults, checkedMoves int

	inbound inboundObserver
}

// inboundObserver is what the checks keep of the inbound events (invariants
// 11 to 19), under w.mu but for what the checks alone read: the rows the
// guarded stores committed, in insert order (see noteRow); each instance's
// account of each item, as fresh as its latest load and as stale as its
// latest append that failed, numbered in one order (see staleLocked); and
// the un-faulted Ingests that answered an error. The checks' own, on the
// driver's goroutine: how far each read the rows and the faults; each
// instance's last applied row (invariants 11, 12); the rows' keys and the
// item_triggered records' (invariant 18); the items read again since the
// items' check last judged them, and what it keeps of each (see
// theItemsTriggers).
type inboundObserver struct {
	rows                []inboundNote
	accounts            int64
	loadedAt, failedAt  map[string]map[item.ID]int64
	ingestFaults        []error
	checkedRows         int
	checkedIngestFaults int
	lastApplied         map[instKey]app.InboundEvent
	rowKeys             map[string]bool
	triggeredBy         map[string]string
	dirty               map[item.ID]bool
	keys                map[item.ID]*itemKeys
}

// inboundNote is an inbound row a guarded store committed: the row, with its
// item, and the number of the last record committed at its commit, its own
// append's among them.
type inboundNote struct {
	row   app.InboundEvent
	order int64
}

// instKey is an instance under its source.
type instKey struct{ source, instance string }

// itemKeys is what the items' check keeps of an item (invariant 17): whether
// it is closed, the number of the first record of each dedup key it holds,
// under its source, and of each of its reopens.
type itemKeys struct {
	closed  bool
	first   map[instKey]int64
	reopens []int64
}

// moveNote is a move of a session a port noted, made while the calling
// instance's holding was valid for the session's epoch: the instance, the
// session's run and agent, the round, the budget, and the number of the last
// record committed at the call.
type moveNote struct {
	inst, agent string
	run         board.RunID
	round       int
	budget      int64
	order       int64
}

func newObserver() observer {
	return observer{
		recordOrder: map[string]int64{}, recordAppend: map[string]int64{}, cameAt: map[string]int64{}, closedRuns: map[board.RunID]int64{},
		inFlight: map[sentKey][]string{}, openingsInFlight: map[item.ID][]string{}, folded: map[item.ID]*foldedItem{}, refolded: map[item.ID]bool{},
		movesChecked: map[string]bool{}, killsChecked: map[string]bool{}, lost: map[string]bool{},
		ruledAt: map[string]time.Time{}, direct: map[string]bool{},
		runItems: map[board.RunID]item.ID{}, closedPos: map[item.ID]int{}, unheld: map[item.ID]bool{}, heldAt: map[item.ID]int64{}, unsure: map[item.ID]bool{},
		recordStep: map[string]int64{}, recordBy: map[string]tryHolder{}, tries: map[item.ID][]try{},
		inbound: inboundObserver{
			loadedAt: map[string]map[item.ID]int64{}, failedAt: map[string]map[item.ID]int64{},
			lastApplied: map[instKey]app.InboundEvent{}, rowKeys: map[string]bool{}, triggeredBy: map[string]string{},
			dirty: map[item.ID]bool{}, keys: map[item.ID]*itemKeys{},
		},
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
	// answered is the number of the last record committed when the answer
	// came back to the driver.
	answered int64
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
		if in == nil {
			w.direct[committed[i].ID] = true
		}
		w.order++
		w.steps++
		w.recordOrder[committed[i].ID] = w.order
		w.recordStep[committed[i].ID] = w.steps
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
// of an opening the driver sends under it now. An inbound opening, whose
// trigger is set, is no person's keyed opening: invariants 12 and 18 judge
// it. In the sequential mode one act
// is in flight; in the concurrent mode, a batch's few. With one record a key
// (see aKeyNamesOneActOfItsItem), the record is the first act that landed
// under its key.
func (w *world) inFlightFault(r *item.Record) error {
	if o := r.Body.Opened; o != nil && o.Digest != "" && o.Trigger == nil && !slices.Contains(w.openingsInFlight[r.Item], o.Digest) {
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

// judgeResumeSeen judges an extension the driver sent to item id under key
// that answered a resume, changed (invariant 2; C1): its run_extended, if it
// committed after the record numbered before, records a kill switch row no
// lower than seen, the row its coordinator's view held as the act came, so
// that an on-row its coordinator knew is never a kill the resumed run owes.
func (w *world) judgeResumeSeen(id item.ID, key string, seen, before int64) {
	records, err := w.recordStore.Load(context.Background(), id)
	if err != nil {
		return
	}
	hash := keyHash(key)
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range records {
		e := records[i].Body.Extended
		if e == nil || e.Request.Key != hash || w.recordOrder[records[i].ID] <= before {
			continue
		}
		if e.KillSwitchSeen < seen {
			w.seenFaults = append(w.seenFaults, broke(2, "item %s's resume under %s recorded the kill switch row %d, below row %d, which its coordinator knew", id, key, e.KillSwitchSeen, seen))
		}
		return
	}
}

// noteRuledAt notes, for the link or unlink of item id under key that
// committed after the record numbered before, the database's time its
// coordinator's clock read as the act came (see movedRightly).
func (w *world) noteRuledAt(id item.ID, key string, now time.Time, before int64) {
	records, err := w.recordStore.Load(context.Background(), id)
	if err != nil {
		return
	}
	hash := keyHash(key)
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range records {
		if req := requestHeld(&records[i].Body); req != nil && req.Key == hash && w.recordOrder[records[i].ID] > before &&
			(records[i].Body.Linked != nil || records[i].Body.Unlinked != nil) {
			w.ruledAt[records[i].ID] = now
		}
	}
}

// noteMove notes a move of a session, made while the calling instance's
// holding was valid (invariant 5; see movedWithin).
func (w *world) noteMove(in *instance, run board.RunID, agent string, round int, budget int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.moves = append(w.moves, moveNote{inst: in.name, agent: agent, run: run, round: round, budget: budget, order: w.order})
}

// noteLost notes the records an append committed whose answer was lost.
func (w *world) noteLost(committed []item.Record) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range committed {
		w.lost[committed[i].ID] = true
	}
}

// noteUnheld notes, of an append of the records of item id, whose answer was
// lost if lost says so, whether a coordinator has read the item since: one
// whose opening landed with its answer lost holds it only once it reads the
// item, which a load that succeeds notes, with lost false and no opening, and
// its step (invariant 9; see heldBefore).
func (w *world) noteUnheld(id item.ID, committed []item.Record, lost bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !lost {
		if w.unheld[id] {
			delete(w.unheld, id)
			w.steps++
			w.heldAt[id] = w.steps
		}
		return
	}
	for i := range committed {
		if committed[i].Body.Opened != nil {
			w.unheld[id] = true
		}
	}
}

// noteTry notes, at its step, an append of item id's records that in's
// coordinator makes while its snapshot does not hold the item waiting, as
// the start, or the not staffed at its start, of an item the walk admitted is
// made: the walk marks the item, which leaves the waiting list, before its
// advance appends (invariant 9; see heldBefore). A person's promotion or
// close of a waiting item is made while the item still waits, and is no try.
// The try is noted with the holding whose epoch ctx carries, which its guard
// checks: a try of a fenced instance, or of a holding the lease moved past,
// dates no admission of another holding's (R40).
func (w *world) noteTry(ctx context.Context, in *instance, id item.ID) {
	if slices.Contains(in.coord.Lifecycle().Waiting, id) {
		return
	}
	epoch, err := holding.EpochFrom(ctx)
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.steps++
	w.tries[id] = append(w.tries[id], try{step: w.steps, by: tryHolder{in.name, epoch}})
}

// tryHolder is a holding of the lease: an instance and its epoch.
type tryHolder struct {
	inst  string
	epoch int64
}

// try is an append of an item's records that a holding's coordinator made
// while not holding the item waiting, at its step (see noteTry).
type try struct {
	step int64
	by   tryHolder
}

// noteUnsure notes a promotion of item id, or an extension, which may be a
// resume (D21), sent to in, that answered an error, as one whose outcome
// stays unknown when, among the log's entries from from on, in appended to
// the item's records and a read of the item by in failed after it: the act's
// append failed, before its commit or with its answer lost, and its lookup
// failed too, so the account counts the item as promoted, or as resumed, and
// waits it in no queue, until its next load (invariant 9; R53). For a
// resumed item the note has no reader: admittedByTheWalk reads it only for an
// item whose opening is its only record, and a resumed item never waited; the
// driver reads it to aim its next event (see pick), and invariant 4 checks the
// account's count of such an item.
func (w *world) noteUnsure(in *instance, id item.ID, from int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	appended := false
	for _, e := range w.entries[from:] {
		switch c := e.call; {
		case c == nil || c.Inst != in.name || c.Port != storeRecords || c.Item != id:
		case c.Method == "Append":
			appended = true
		case c.Method == "fails" && appended:
			w.unsure[id] = true
			return
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
	w.answers = append(w.answers, onAnswer{inst: in.name, subject: subject, out: *out, answered: w.order})
}

// actKinds are the kinds of the driver's acts as their requests' digests name
// them (Decision 13).
var actKinds = map[string]string{
	actClose: "close", actPromote: "promote", actReopen: "reopen", actExtend: "extend", actLink: "link", actUnlink: "unlink",
}

// actDigest is the digest a record keeps of the driver's act on item id, with
// its parameters args, as Decision 13 and R98 make it: the SHA-256 of the
// canonical JSON of its kind, its item, its reason's code, duplicate for a
// link and an unlink, which require one, and none for the others, and the
// act's parameters as further keys: an extension's dimensions named, each as
// a decimal string, the wall clock in nanoseconds, and a link's other item.
func actDigest(act string, id item.ID, args actArgs) string {
	fields := map[string]string{"kind": actKinds[act], "item": string(id), "reason": ""}
	switch act {
	case actLink:
		fields["reason"], fields["with"] = duplicate.Code, string(args.with)
	case actUnlink:
		fields["reason"] = duplicate.Code
	case actExtend:
		if r := args.amount.Rounds; r != nil {
			fields["rounds"] = strconv.Itoa(*r)
		}
		if d := args.amount.WallClock; d != nil {
			fields["wallClock"] = strconv.FormatInt(int64(*d), 10)
		}
		if n := args.amount.Tokens; n != nil {
			fields["tokens"] = strconv.FormatInt(*n, 10)
		}
	}
	canonical, err := json.Marshal(fields)
	if err != nil {
		panic(err) // strings always encode
	}
	return keyHash(string(canonical))
}

// flying notes an act the driver sends to item id under key, with its
// parameters args, as in flight, until landed, which its caller calls once
// the act answered: a record of it keeps the key's hash and the act's digest
// (invariant 6).
func (w *world) flying(id item.ID, act, key string, args actArgs) (landed func()) {
	k, d := sentKey{id, keyHash(key)}, actDigest(act, id, args)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inFlight[k] = append(w.inFlight[k], d)
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.inFlight[k] = removeOne(w.inFlight[k], d)
	}
}

// keyAnswered judges an act the driver sent to item id under key, with its
// parameters args, that answered success (invariant 6; R98, M10): a record of
// the item committed before the act came, numbered before or at before, that
// holds the key with another digest, as an extension of another amount or a
// link with another item under the key, names another act, which the act
// must refuse ErrKeyReused, never answer as its repeat. A record of the act
// that committed meanwhile, under a concurrent batch's other act, is not
// judged, nor one the store holds that its append has not noted yet: it
// committed after before was read, as a record committed earlier was noted
// before its append returned, and the noting numbers it past before.
func (w *world) keyAnswered(id item.ID, act, key string, args actArgs, before int64) {
	records, err := w.recordStore.Load(context.Background(), id)
	if err != nil {
		return
	}
	hash, digest := keyHash(key), actDigest(act, id, args)
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range records {
		req := requestHeld(&records[i].Body)
		order, noted := w.recordOrder[records[i].ID]
		if req == nil || req.Key != hash || req.Digest == digest || !noted || order > before {
			continue
		}
		w.keyFaults = append(w.keyFaults, broke(6, "item %s's act %s under %s answered success, while its record %d held the key with the digest %s, of another act, before it came",
			id, act, key, records[i].Seq, req.Digest))
		return
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
		w.refolded[id] = true
		w.inbound.dirty[id] = true
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

// runSpan is an interval of a run's running (the plan review's M5): the
// numbers, in the order of the records' commits, of its start, the run's
// creation or a resume, and of its stop, 0 while it runs; and of the latest
// round_ended within it that decided the run's stop, not ended by a halt, 0
// for none, and whether that round was missed: unseen, ended by its deadline
// or an outage. A round decided before a resume belongs to the span before
// it, so that it never exempts the resumed run (D21).
type runSpan struct {
	start, stop, decided int64
	missed               bool
}

// runSpans returns, from an item's records, its run's intervals of running,
// in their order: from its creation, and from each resume, a run_extended of
// a stopped run, to the stop that ends each; none for an item with no run.
func (w *world) runSpans(records []item.Record) []runSpan {
	w.mu.Lock()
	defer w.mu.Unlock()
	var spans []runSpan
	for i := range records {
		order := w.recordOrder[records[i].ID]
		switch b := &records[i].Body; {
		case b.RunCreated != nil:
			spans = append(spans, runSpan{start: order})
		case b.Extended != nil && len(spans) > 0 && spans[len(spans)-1].stop != 0:
			spans = append(spans, runSpan{start: order})
		case len(spans) == 0:
		case b.RoundEnded != nil:
			last := &spans[len(spans)-1]
			last.decided, last.missed = 0, false
			if re := b.RoundEnded; re.Decision.Next == item.NextStop && re.EndedBy != item.EndedByStopped {
				last.decided = order
				last.missed = re.Unseen && (re.EndedBy == item.EndedByDeadline || re.EndedBy == item.EndedByOutage)
			}
		case b.RunStopped != nil:
			spans[len(spans)-1].stop = order
		}
	}
	return spans
}

// spanAt returns the interval of spans in which the run ran when the record
// numbered at had committed: started by then, and stopped after, if at all.
func spanAt(spans []runSpan, at int64) (runSpan, bool) {
	for _, s := range spans {
		if s.start <= at && (s.stop == 0 || s.stop > at) {
			return s, true
		}
	}
	return runSpan{}, false
}

// The checks.

// lifecycleInvariants checks the nineteen invariants, in their order, each where
// its line applies, and returns the first that broke.
func lifecycleInvariants(w *world, stage string) error {
	for _, check := range []func(*world, string) error{
		noWorkWhileOn, everyRunAliveAtAnOnIsHalted, noQueuedItemWaitsWhileASlotIsFree, theLimitAndOne, recordsAndEpochs,
		aKeyNamesOneActOfItsItem, aStoppedRunLeavesNothingRunning, autoCloseKeepsItsPauseAndItsDeadline, theQueuesOrder,
		closedItemsRunNothing, theInboundRows, theItemsTriggers, anUnfaultedIngestAnswers,
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
// its Owed, every run that ran when the on came: one with an interval of
// running, from its creation or a resume, that started before the on's
// request came to the kill switch's store, and whose stop, if any, committed
// after (see runSpans). A run whose round within that interval had decided
// its stop when the on came, its round_ended committed by then and not ended
// by a halt, is halted for that stop, and the answer does not name it; a
// round decided before a resume exempts nothing (D21). Nor does it name a run
// whose missed round, unseen, ended by its deadline or an outage, the on's
// own advance ended deciding its stop, before the answer came back: the
// on's advance checks a missed round before the kill it owes (Decision 12's
// order, runStep), and the run stops for that round's decision (the scoped
// re-review's N1). One stopped closed, or for a round decided after the on
// came otherwise, as its halt failed, it names. Every on that answers success
// reached the store, so an answer whose request the world never noted there
// is a fault of the harness's, which fails the schedule.
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
		span, ran := spanAt(w.runSpans(f.records), came)
		if !ran {
			continue
		}
		if span.stop != 0 && span.decided != 0 && (span.decided <= came || span.missed && span.decided <= a.answered) {
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
// and no item with no run that owes it has started. After every event, its
// other half: no run is killed by an on-row it did not owe (see killsOwed),
// and no resume records a row below its coordinator's view (see
// judgeResumeSeen), so that a resumed run is never killed by a row older
// than its resume (Task 11's fix round, C1).
func everyRunAliveAtAnOnIsHalted(w *world, stage string) error {
	w.mu.Lock()
	faults := slices.Clone(w.seenFaults[w.checkedSeenFaults:])
	w.checkedSeenFaults = len(w.seenFaults)
	w.mu.Unlock()
	if len(faults) > 0 {
		return faults[0]
	}
	w.observe()
	if err := w.killsOwed(); err != nil {
		return err
	}
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

// killsOwed checks, from the records alone, never through
// Run.KillSwitchSeen, that each run_stopped killed names an on-row, Killed,
// above every row the run had seen before that stop: its creation's
// KillSwitchSeen and every resume's, a run_extended of the run while it was
// stopped (invariant 2). Each stop is checked once.
func (w *world) killsOwed() error {
	for _, id := range w.observed() {
		var seen int64
		stopped := false
		records := w.folded[id].records
		for i := range records {
			switch b := &records[i].Body; {
			case b.RunCreated != nil:
				seen, stopped = b.RunCreated.KillSwitchSeen, false
			case b.Extended != nil && stopped:
				seen, stopped = max(seen, b.Extended.KillSwitchSeen), false
			case b.RunStopped != nil:
				stopped = true
				if b.RunStopped.Reason != item.StopKilled || w.killsChecked[records[i].ID] {
					continue
				}
				w.killsChecked[records[i].ID] = true
				if b.RunStopped.Killed <= seen {
					return broke(2, "item %s's run %s stopped killed by on-row %d, which its creation or a resume before the stop had seen, at row %d", id, records[i].Run, b.RunStopped.Killed, seen)
				}
			}
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
// snapshot counts them, are at most the limit and one. And the snapshot
// counts no fewer than the records hold, less the openings that landed with
// their answers lost that it does not hold yet (see noteUnheld): an act whose
// outcome is unknown, a resume's among them, keeps its slot until the item's
// next load (R53; Task 11's fix round, I2).
func theLimitAndOne(w *world, _ string) error {
	_, after := w.lifecycles()
	if after.Investigating > after.Limit+1 {
		return broke(4, "the leading coordinator's snapshot counts %d items investigating, above the limit %d and one", after.Investigating, after.Limit)
	}
	w.observe()
	n, _ := w.investigating()
	if n > worldLimit+1 {
		return broke(4, "the records hold %d items investigating, above the limit %d and one", n, worldLimit)
	}
	if after.Limit == 0 {
		return nil
	}
	w.mu.Lock()
	unheld := 0
	for id := range w.unheld {
		if f := w.folded[id]; f != nil && f.state != nil && (f.state.Status() == item.StatusInvestigating || f.state.Promotion != nil && f.state.Status() == item.StatusQueued) {
			unheld++
		}
	}
	w.mu.Unlock()
	if after.Investigating < n-unheld {
		return broke(4, "the leading coordinator's snapshot counts %d items investigating, while the records hold %d, %d of them an opening it does not hold", after.Investigating, n, unheld)
	}
	return nil
}

// recordsAndEpochs checks invariant 5 after every event: every item's records
// fold, and every write the guarded stores committed carried an epoch, the
// lease's at its commit. The world reads the lease's epoch at the commit,
// apart from the guard's check (see wrote), so a write that no guard checked,
// or a lease that moved between a write's check and its commit, breaks it.
// It checks the writes committed since its last check, and the items read
// again since its last check. The incidents fold too (Decision 18; Task 11): each record's incident column is its item's
// incident after it, and a link's the incident its other item had at the
// link's commit (see incidentsFold); once quiet, the records' IncidentItems
// lists each incident's items as the folds hold them.
func recordsAndEpochs(w *world, stage string) error {
	w.observe()
	for _, id := range w.observed() {
		if err := w.folded[id].err; err != nil {
			return broke(5, "the records of item %s cannot be read or do not fold: %v", id, err)
		}
	}
	refolded := slices.Sorted(maps.Keys(w.refolded))
	clear(w.refolded)
	for _, id := range refolded {
		if err := w.incidentsFold(id); err != nil {
			return err
		}
		if err := w.budgetsFold(id); err != nil {
			return err
		}
	}
	if err := w.movedWithin(); err != nil {
		return err
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
	if stage == stageQuiet {
		return w.incidentItemsAgree()
	}
	return nil
}

// incidentsFold checks item id's records, which fold, against their incident
// columns (invariant 5): each record's column is the item's incident after
// it, as the fold of the records up to it gives it; and a link's is the
// incident its other item had at the link's commit, as the fold of that
// item's records committed before it gives it.
func (w *world) incidentsFold(id item.ID) error {
	records := w.folded[id].records
	s := &item.State{}
	for i := range records {
		r := &records[i]
		if err := s.Apply(r); err != nil {
			return broke(5, "the records of item %s do not fold: %v", id, err)
		}
		if r.Incident != s.Incident {
			return broke(5, "item %s's record %d holds incident %s, and the item is in %s after it", id, r.Seq, r.Incident, s.Incident)
		}
		if l := r.Body.Linked; l != nil {
			if had := w.incidentAt(l.With, r.ID); r.Incident != had {
				return broke(5, "item %s's link, record %d, holds incident %s, not %s, which item %s had at its commit", id, r.Seq, r.Incident, had, l.With)
			}
		}
		if (r.Body.Linked != nil || r.Body.Unlinked != nil) && !w.movesChecked[r.ID] {
			w.movesChecked[r.ID] = true
			if err := w.movedRightly(id, r, records[:i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// movedRightly checks a link or an unlink, r, of item id, after its records
// before (invariant 5; Task 11's fix round, C2 and M-a): it came within the
// window of late rulings of its own item, if closed before it, and, for a
// link, of its other item, if closed at its commit (R64); and an unlink left
// an incident that held another item at its commit, as the folds of every
// item's records committed before it give them (R60). The window is judged
// at the database's time as the act's coordinator read it, by its clock.DB,
// as the act came, which the driver notes in the sequential mode: the
// coordinator judges the window so, and its reading lags a step of its host's
// wall clock until its next sample, R39's residual, which no record's time
// shows. A record the violation table appended is judged at its own time; a
// concurrent batch's act, whose clock other events of the batch may step, is
// not judged against the window.
func (w *world) movedRightly(id item.ID, r *item.Record, before []item.Record) error {
	what := "link"
	if r.Body.Unlinked != nil {
		what = "unlink"
	}
	w.mu.Lock()
	at, judged := w.ruledAt[r.ID]
	if w.direct[r.ID] {
		at, judged = r.At, true
	}
	w.mu.Unlock()
	if s, err := item.Fold(before); judged && err == nil && s.Status() == item.StatusClosed && at.After(s.ClosedAt.Add(worldClosedRulings)) {
		return broke(5, "item %s's %s, record %d, came at %s, past its window of late rulings, which its close at %s opened for %s",
			id, what, r.Seq, at, s.ClosedAt, worldClosedRulings)
	}
	if l := r.Body.Linked; l != nil && judged {
		if o := w.stateAt(l.With, r.ID); o != nil && o.Status() == item.StatusClosed && at.After(o.ClosedAt.Add(worldClosedRulings)) {
			return broke(5, "item %s's link, record %d, came at %s, past item %s's window of late rulings, which its close at %s opened for %s",
				id, r.Seq, at, l.With, o.ClosedAt, worldClosedRulings)
		}
	}
	if u := r.Body.Unlinked; u != nil {
		var in []item.ID
		for _, other := range w.observed() {
			if o := w.stateAt(other, r.ID); o != nil && o.Incident == u.Left {
				in = append(in, other)
			}
		}
		if len(in) < 2 {
			return broke(5, "item %s's unlink, record %d, left incident %s, which held %v alone at its commit", id, r.Seq, u.Left, in)
		}
	}
	return nil
}

// budgetsFold checks item id's records against the budget the test adds up
// (invariant 5; Task 11's fix round, I1): a run_stopped budget_exhausted
// follows a round_ended that its run's creation's budget plus the amounts of
// every run_extended before that round's end exhausts, by the rule of
// exhaustion (spec Section 5.6): its rounds reach the budget's, its wall
// clock leaves less than a third of a round's limit, or its spend reaches the
// budget's tokens.
func (w *world) budgetsFold(id item.ID) error {
	var b item.Budget
	var limit time.Duration
	var ended *item.RoundEnded
	var at item.Budget
	for i := range w.folded[id].records {
		r := &w.folded[id].records[i]
		switch body := &r.Body; {
		case body.RunCreated != nil:
			b, limit = body.RunCreated.Budget, body.RunCreated.RoundLimit
		case body.Extended != nil:
			b.Rounds += body.Extended.Amount.Rounds
			b.WallClock += body.Extended.Amount.WallClock
			b.Tokens += body.Extended.Amount.Tokens
		case body.RoundEnded != nil:
			ended, at = body.RoundEnded, b
		case body.RunStopped != nil && body.RunStopped.Reason == item.StopBudgetExhausted && ended != nil:
			left := at.WallClock - ended.WallClock
			if ended.Rounds < at.Rounds && left >= limit/3 && left > 0 && ended.Spent < at.Tokens {
				return broke(5, "item %s's run %s stopped budget_exhausted after round %d, which its budget and its extensions, %+v, do not exhaust", id, r.Run, ended.Round, at)
			}
		}
	}
	return nil
}

// movedWithin checks each session's move noted since its last check
// (invariant 5; Task 11's fix round, I1): the budget it moved the session
// with is at most its round's recorded budget plus the tokens of every
// run_extended of the run committed after the round's round_opened and
// before the move, by the order of the records' commits, never Extension.At;
// and at least that, less the extensions whose append lost its answer, which
// the coordinator raises the sessions for once it reads them. A move of a
// round whose opening is not committed is not judged.
func (w *world) movedWithin() error {
	w.mu.Lock()
	moves := slices.Clone(w.moves[w.checkedMoves:])
	w.checkedMoves = len(w.moves)
	w.mu.Unlock()
	for _, m := range moves {
		w.mu.Lock()
		id, ok := w.runItems[m.run]
		w.mu.Unlock()
		f := w.folded[id]
		if !ok || f == nil {
			continue
		}
		var opened int64
		var low, high int64
		w.mu.Lock()
		for i := range f.records {
			r := &f.records[i]
			order := w.recordOrder[r.ID]
			switch {
			case order > m.order:
			case r.Body.RoundOpened != nil && r.Body.RoundOpened.Round == m.round:
				opened, low, high = order, r.Body.RoundOpened.Budget, r.Body.RoundOpened.Budget
			case r.Body.Extended != nil && opened != 0:
				high += r.Body.Extended.Amount.Tokens
				if !w.lost[r.ID] {
					low += r.Body.Extended.Amount.Tokens
				}
			}
		}
		w.mu.Unlock()
		if opened != 0 && (m.budget < low || m.budget > high) {
			return broke(5, "%s moved the session of %s in run %s into round %d with %d tokens, outside the %d to %d its round's budget and the extensions since it give",
				m.inst, m.agent, m.run, m.round, m.budget, low, high)
		}
	}
	return nil
}

// incidentAt is the incident item id was in when the record whose id is at
// committed (see stateAt); empty for an item with none.
func (w *world) incidentAt(id item.ID, at string) item.IncidentID {
	if s := w.stateAt(id, at); s != nil {
		return s.Incident
	}
	return ""
}

// stateAt is item id's state when the record whose id is at committed: the
// fold of its records committed before it, by the order of the records'
// commits; nil for an item with none, or whose records do not fold.
func (w *world) stateAt(id item.ID, at string) *item.State {
	f := w.folded[id]
	if f == nil {
		return nil
	}
	w.mu.Lock()
	order := w.recordOrder[at]
	var before []item.Record
	for i := range f.records {
		if w.recordOrder[f.records[i].ID] < order {
			before = append(before, f.records[i])
		}
	}
	w.mu.Unlock()
	if len(before) == 0 {
		return nil
	}
	s, err := item.Fold(before)
	if err != nil {
		return nil
	}
	return s
}

// incidentItemsAgree checks, once quiet, that the records' IncidentItems
// lists each incident's items as the folds hold them: every incident a
// record's column names, those the items left by a link or an unlink among
// them (invariant 5).
func (w *world) incidentItemsAgree() error {
	folds := map[item.IncidentID][]item.ID{}
	for _, id := range w.observed() {
		f := w.folded[id]
		if f.state == nil {
			continue
		}
		for i := range f.records {
			if _, ok := folds[f.records[i].Incident]; !ok {
				folds[f.records[i].Incident] = nil
			}
		}
		folds[f.state.Incident] = append(folds[f.state.Incident], id)
	}
	for _, incident := range slices.Sorted(maps.Keys(folds)) {
		want := slices.Sorted(slices.Values(folds[incident]))
		if want == nil {
			want = []item.ID{}
		}
		got, err := w.recordStore.IncidentItems(context.Background(), incident)
		if err != nil {
			return broke(5, "once quiet, the items of incident %s cannot be read: %v", incident, err)
		}
		if !slices.Equal(got, want) {
			return broke(5, "once quiet, incident %s holds %v by the records' IncidentItems, and %v by the folds", incident, got, want)
		}
	}
	return nil
}

// requestHeld is the request a person's act on an item keeps in its record,
// b: a promotion's, an extension's, a link's, an unlink's, a reopen's, or a
// person's close's; nil for any other record (Contract C19; R98), and, as
// requestIn has it (R120), for a firing resume's run_extended and a flap's
// item_linked, platform acts whose keys no person sends.
func requestHeld(b *item.Body) *item.Request {
	switch {
	case b.Promoted != nil:
		return &b.Promoted.Request
	case b.Extended != nil && b.Extended.Cause == item.ExtendFiring:
		return nil
	case b.Extended != nil:
		return &b.Extended.Request
	case b.Linked != nil && strings.HasPrefix(b.Linked.By, app.SourceSubjectPrefix):
		return nil
	case b.Linked != nil:
		return &b.Linked.Request
	case b.Unlinked != nil:
		return &b.Unlinked.Request
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
// sessions were closed once its latest stop committed, a resumed run's
// second stop among them (see runSpans). And a run stopped after its board's
// resume entry has a stop entry of its own after it (Task 11; C18; see
// stoppedOnItsBoard).
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
			if err := w.stoppedOnItsBoard(id, s.Run.ID, f.records); err != nil {
				return err
			}
		}
	}
	return nil
}

// stoppedOnItsBoard checks, of item id's run, which stopped, that its board
// holds a stop entry after its latest resume entry, if it has one: each stop
// after a resume entry has its own stop entry (C18; v3's N5). A run whose
// latest stop is closed, or whose item closed after it, is not judged: a
// closed item's board stop is posted once and best effort (R73).
func (w *world) stoppedOnItsBoard(id item.ID, run board.RunID, records []item.Record) error {
	last := -1
	for i := range records {
		switch b := &records[i].Body; {
		case b.RunStopped != nil && b.RunStopped.Reason != item.StopClosed:
			last = i
		case b.RunStopped != nil, b.Closed != nil:
			last = -1
		}
	}
	if last < 0 {
		return nil
	}
	resumed, stopped := false, false
	events := w.boardOf(run)
	for i := range events {
		switch {
		case events[i].Body.Resume != nil:
			resumed, stopped = true, false
		case events[i].Body.Stop != nil:
			stopped = true
		}
	}
	if resumed && !stopped {
		return broke(7, "item %s's run %s stopped after its board's resume entry, and its board holds no stop entry after it", id, run)
	}
	return nil
}

// leftNothingRunning checks item id's run, which stopped, against the live
// instances and the upstream pools.
func (w *world) leftNothingRunning(id item.ID, run board.RunID, records []item.Record) error {
	var stopped int64
	if spans := w.runSpans(records); len(spans) > 0 {
		stopped = spans[len(spans)-1].stop
	}
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
	due, ok := n.before.AutoCloseAt(delay, n.offAt)
	if !ok {
		return broke(8, "%s recorded item %s's automatic close due at %s while its trigger fires, which never closes it by itself",
			n.inst, n.rec.Item, c.Due)
	}
	if !c.Due.Equal(due) {
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
// event or an earlier one (see heldBefore). Nor does an item that the walk
// which admitted another had admitted first, whose start failed, and which a
// takeover's account holds waiting again (see admittedFirst).
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
			if !w.ranksAhead(a, still) && w.heldBefore(still, a) && !w.admittedFirst(still, a) {
				return broke(9, "item %s, admitted by the walk, does not rank ahead of item %s, still waiting: their openings' times are %s and %s",
					a, still, w.openedAt(a), w.openedAt(still))
			}
		}
	}
	return nil
}

// admittedByTheWalk reports whether item id, which left the waiting list in
// the event, left it by the walk's admission there, as the record that took
// it out of the queue says, the first after its opening's append (see
// exitOf): none, the item admitted and its start due; or one the event
// committed, its start, or its not staffed at its start for the daily cap, its
// routing or its type. A
// promotion, a close, or a not staffed for the kill switch is no admission,
// nor is a not staffed in the opening's own append, as the item never waited.
// Nor is an opening that landed with its answer lost, while no coordinator
// has read the item since: the opening's lookup failed, the coordinator does
// not hold the item, and the item's retry, not the walk, takes it into the
// account (see noteUnheld). Nor is a person's promotion whose outcome stays
// unknown, which the account counts until the item's next load, whether it
// landed or not (see noteUnsure), while no record took the item out of the
// queue: a promotion that landed is no admission by its own record, and a
// start the walk wrote once the item's load found no promotion is one. A
// resume's note is never read here, as a resumed item never waited.
// Nor is a record that committed before the event: the snapshot before it
// held the item waiting all the same, as the coordinator learns of a record
// whose answer was lost only at the item's next load, and that load, not the
// walk, took the item out of the waiting list.
func (w *world) admittedByTheWalk(id item.ID, inEvent map[string]bool) bool {
	f := w.folded[id]
	w.mu.Lock()
	unheld, unsure := w.unheld[id], w.unsure[id]
	w.mu.Unlock()
	if f == nil || len(f.records) == 0 || unheld {
		return false
	}
	at, exited, waited := w.exitOf(f)
	switch {
	case !waited:
		return false
	case !exited:
		return !unsure
	}
	exit := &f.records[at]
	switch {
	case !inEvent[exit.ID]:
		return false
	case exit.Body.Started != nil:
		return true
	case exit.Body.AwaitingClose != nil:
		return exit.Body.AwaitingClose.Cause != item.CauseKillSwitch
	}
	return false
}

// exitOf returns the index of the record that took the item of f, its records
// folded, out of the queue, the first after its opening's append, and whether
// it has one; and whether the item waited at all: an item not staffed in its
// opening's own append never did. An inbound opening's item_triggered, and a
// flap's item_linked, land in that append (R126), and are no exit.
func (w *world) exitOf(f *foldedItem) (at int, exited, waited bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	opening := w.recordAppend[f.records[0].ID]
	for i := range f.records {
		if w.recordAppend[f.records[i].ID] == opening && f.records[i].Body.AwaitingClose != nil {
			return 0, false, false
		}
	}
	at, exited = w.exitAtLocked(f)
	return at, exited, true
}

// exitAtLocked is exitOf's exit, under w.mu: the first record after the
// opening's append that is no item_triggered, item_linked or item_unlinked,
// which a queued item takes while it waits (state.go:500), and which take it
// out of no queue.
func (w *world) exitAtLocked(f *foldedItem) (int, bool) {
	opening := w.recordAppend[f.records[0].ID]
	for i := 1; i < len(f.records); i++ {
		b := &f.records[i].Body
		if w.recordAppend[f.records[i].ID] != opening && b.Triggered == nil && b.Linked == nil && b.Unlinked == nil {
			return i, true
		}
	}
	return 0, false
}

// heldBefore reports whether a coordinator held item still when the walk
// admitted item a: unless still is an item whose opening landed with its
// answer lost, and a was admitted before a coordinator first read still,
// which took it into the account. a was admitted no later than its record
// that took it out of the queue committed, and no later than the first
// append of its records after its opening that the holding which committed
// that record made while not holding it waiting (see noteTry): a start that
// failed before its commit keeps the item's admission, and its retry commits
// the start after the read that took still in, in the event or a later one.
// An item a whose opening is its only record, its start not committed yet,
// was admitted at the first such append of the leading holding's, and is
// judged held before it only when there is none. A try of another holding,
// as one a takeover fenced, dates no admission of this one's (R40).
func (w *world) heldBefore(still, a item.ID) bool {
	f := w.folded[a]
	w.mu.Lock()
	_, ok := w.heldAt[still]
	w.mu.Unlock()
	if !ok || f == nil || len(f.records) == 0 {
		return true
	}
	admitted, _ := w.admission(f, a)
	w.mu.Lock()
	defer w.mu.Unlock()
	return admitted > w.heldAt[still]
}

// admission returns when the walk admitted item a, whose folded records are
// f, and the holding whose walk did (see heldBefore): its exit's commit, or
// the first try after its opening, before it, of the holding that committed
// the exit; with no exit yet, the leading holding's first try, or never.
func (w *world) admission(f *foldedItem, a item.ID) (int64, tryHolder) {
	var leading tryHolder
	if in := w.leading(); in != nil {
		epoch, _, _ := in.leader.Status()
		leading = tryHolder{in.name, epoch}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	opened, admitted, by := w.recordStep[f.records[0].ID], int64(math.MaxInt64), leading
	if at, exited := w.exitAtLocked(f); exited {
		admitted, by = w.recordStep[f.records[at].ID], w.recordBy[f.records[at].ID]
	}
	for _, t := range w.tries[a] {
		if t.by == by && t.step > opened && t.step < admitted {
			return t.step, by
		}
	}
	return admitted, by
}

// admittedFirst reports whether the walk that admitted item a had admitted
// item still before it: the same holding tried still, after still's opening,
// before a's admission. still waits again only because a takeover rebuilt the
// account from the records, in which still's start, failed before its commit,
// left no mark, so the walk's order was the queue's (R40; spec 4.1: a start
// that fails keeps its slot, until the holding ends). The holding that leads
// now never has it: under it still keeps its slot, and waits again only if
// the coordinator dropped it.
func (w *world) admittedFirst(still, a item.ID) bool {
	fa, fs := w.folded[a], w.folded[still]
	if fa == nil || len(fa.records) == 0 || fs == nil || len(fs.records) == 0 {
		return false
	}
	admitted, by := w.admission(fa, a)
	if in := w.leading(); in != nil {
		if epoch, _, _ := in.leader.Status(); by == (tryHolder{in.name, epoch}) {
			return false
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	opened := w.recordStep[fs.records[0].ID]
	for _, t := range w.tries[still] {
		if t.by == by && t.step > opened && t.step < admitted {
			return true
		}
	}
	return false
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

// The inbound events' invariants (design D14).

// noteRow notes an inbound row a guarded store committed, of item id when it
// commits with the item's records, after the records committed so far. Its
// caller holds the lease's row lock shared, as the commit did, and no lock of
// the world's.
func (w *world) noteRow(row *app.InboundEvent, id item.ID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r := *row
	if id != "" {
		r.Item = id
	}
	w.inbound.rows = append(w.inbound.rows, inboundNote{row: r, order: w.order})
}

// noteLoaded notes a load of item id's records by in that succeeded: in's
// account of the item is fresh from here on, until an append of it fails.
func (w *world) noteLoaded(in *instance, id item.ID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inbound.accounts++
	if w.inbound.loadedAt[in.name] == nil {
		w.inbound.loadedAt[in.name] = map[item.ID]int64{}
	}
	w.inbound.loadedAt[in.name][id] = w.inbound.accounts
}

// noteFailedAppend notes an append of item id's records by in that failed
// with err, or whose answer was lost, err nil: in's account of the item may be
// stale from here on, until its next load. An append the lifecycle refused
// (item.ErrInvalid), or the guard (a lost lease), or that found its row seen,
// committed nothing, and leaves the account as it was.
func (w *world) noteFailedAppend(in *instance, id item.ID, err error) {
	if errors.Is(err, item.ErrInvalid) || errors.Is(err, holding.ErrLeaseLost) || errors.Is(err, holding.ErrNoEpoch) || errors.Is(err, app.ErrSeen) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inbound.accounts++
	if w.inbound.failedAt[in.name] == nil {
		w.inbound.failedAt[in.name] = map[item.ID]int64{}
	}
	w.inbound.failedAt[in.name][id] = w.inbound.accounts
}

// staleLocked reports whether in's account of item id may be stale: an
// append of it failed, or lost its answer, after in's latest load of it,
// under w.mu. A coordinator decides from such an account, rightly, as the
// design accepts (fix round 1, M-1 and M-3): its decision is refused, or a
// firing tries no resume, which the next re-notification tries.
func (w *world) staleLocked(in *instance, id item.ID) bool {
	return w.inbound.failedAt[in.name][id] > w.inbound.loadedAt[in.name][id]
}

// anyStale reports whether in's account of any item that is not closed, as
// the store holds it now, may be stale: a closed item leaves the account,
// and no event reads it there.
func (w *world) anyStale(in *instance) bool {
	w.mu.Lock()
	var stale []item.ID
	for id := range w.inbound.failedAt[in.name] {
		if w.staleLocked(in, id) {
			stale = append(stale, id)
		}
	}
	w.mu.Unlock()
	for _, id := range stale {
		records, err := w.recordStore.Load(context.Background(), id)
		if err != nil {
			return true
		}
		if s, err := item.Fold(records); err != nil || s.Opened == nil || s.Status() != item.StatusClosed {
			return true
		}
	}
	return false
}

// laterThan reports whether row r is later than last, of its instance, by
// design D7 step 3's rule: it began after it, or at the same time, a
// resolution of a firing.
func laterThan(r, last *app.InboundEvent) bool {
	if r.OccurredAt.Equal(last.OccurredAt) {
		return r.Status == item.Resolved && last.Status == item.Firing
	}
	return r.OccurredAt.After(last.OccurredAt)
}

// rowText names a row in a check's error.
func rowText(r *app.InboundEvent) string {
	return fmt.Sprintf("%s's event %s, %s %s at %s", r.Source, r.EventID, r.InstanceID, r.Status, r.OccurredAt.UTC().Format(time.RFC3339Nano))
}

// theInboundRows checks, after every event, the inbound rows committed since
// its last check, in insert order: invariant 11, an instance's last applied
// time never goes back at the source, each applied row later than the one
// before it of its instance, equal only for a resolution of a firing; 12, no
// item is opened by an event not later than its instance's last status at
// its insert, which 11's order makes the last applied row before it; 14, no
// resolved_unmatched row while an item not closed holds its instance firing
// (see heldFiring); 15, every flap's opening append links it into the item
// LastItem named (see flapLinked); and 18's half of rows, one row a key.
func theInboundRows(w *world, _ string) error {
	w.observe()
	w.mu.Lock()
	rows := slices.Clone(w.inbound.rows)
	from := w.inbound.checkedRows
	w.inbound.checkedRows = len(rows)
	w.mu.Unlock()
	for i := from; i < len(rows); i++ {
		r := &rows[i].row
		key := r.Source + "\x00" + r.EventID
		if w.inbound.rowKeys[key] {
			return broke(18, "%s's event %s has two rows", r.Source, r.EventID)
		}
		w.inbound.rowKeys[key] = true
		if r.Applied {
			k := instKey{r.Source, r.InstanceID}
			if last, ok := w.inbound.lastApplied[k]; ok && !laterThan(r, &last) {
				if r.Outcome == app.InboundOpened {
					return broke(12, "item %s opened by %s, not later than its instance's last status, %s", r.Item, rowText(r), rowText(&last))
				}
				return broke(11, "%s applied, %s, after %s: the instance's last applied time went back", rowText(r), r.Outcome, rowText(&last))
			}
			w.inbound.lastApplied[k] = *r
		}
		switch r.Outcome {
		case app.InboundResolvedUnmatched:
			if err := w.heldFiring(&rows[i]); err != nil {
				return err
			}
		case app.InboundOpened:
			if err := w.flapLinked(rows[:i], r); err != nil {
				return err
			}
		}
	}
	return nil
}

// prefixAt returns the records of f committed by the record numbered order,
// in the order of the records' commits.
func (w *world) prefixAt(f *foldedItem, order int64) []item.Record {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for n < len(f.records) && w.recordOrder[f.records[n].ID] <= order {
		n++
	}
	return f.records[:n]
}

// heldFiring checks invariant 14 at a resolved_unmatched row, n: no item that
// is not closed holds its instance firing as the world's committed records
// stood at its commit, whether or not the item is in the writing instance's
// account (R133).
func (w *world) heldFiring(n *inboundNote) error {
	r := &n.row
	for _, id := range w.observed() {
		f := w.folded[id]
		if !slices.ContainsFunc(f.records, func(rec item.Record) bool { return rec.Body.Triggered != nil }) {
			continue
		}
		s, err := item.Fold(w.prefixAt(f, n.order))
		if err != nil || s.Opened == nil || s.Status() == item.StatusClosed {
			continue
		}
		if st, _, held := s.Instance(r.Source, r.InstanceID); held && st == item.Firing {
			return broke(14, "%s was put resolved_unmatched while item %s, %s, holds its instance firing", rowText(r), id, s.Status())
		}
	}
	return nil
}

// flapLinked checks invariant 15 at an opened row, r, after the rows before
// it: when the last item its dedup key took, as LastItem names it from those
// rows, was closed as the item's opening came, the opening's append holds an
// item_linked with that item, into its incident. One that was not closed
// opens nothing, as the event matches it, which invariant 17 judges.
func (w *world) flapLinked(before []inboundNote, r *app.InboundEvent) error {
	var last item.ID
	for i := len(before) - 1; i >= 0; i-- {
		if b := &before[i].row; b.Source == r.Source && b.DedupKey == r.DedupKey && b.Item != "" {
			last = b.Item
			break
		}
	}
	opened, prior := w.folded[r.Item], w.folded[last]
	if last == "" || opened == nil || len(opened.records) == 0 || prior == nil {
		return nil
	}
	w.mu.Lock()
	start, openingAppend := w.recordOrder[opened.records[0].ID], w.recordAppend[opened.records[0].ID]
	w.mu.Unlock()
	s, err := item.Fold(w.prefixAt(prior, start-1))
	if err != nil || s.Status() != item.StatusClosed {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range opened.records {
		rec := &opened.records[i]
		if l := rec.Body.Linked; l != nil && w.recordAppend[rec.ID] == openingAppend && l.With == last && rec.Incident == s.Incident {
			return nil
		}
	}
	return broke(15, "item %s, opened by %s, a flap of item %s, closed, holds no item_linked into its incident %s in its opening's append", r.Item, rowText(r), last, s.Incident)
}

// theItemsTriggers checks, after every event, the records of each item read
// again since its last check: invariant 13, no item closes by itself while
// its trigger fires, judged from its item_triggered records, each instance's
// last applied status, never from the fold, whose own refusal it judges; 16,
// a firing resume is resumed or declined, never pending (see resumeDecided);
// 18's half of records, one item_triggered an event, across every item; and
// then 17 over every item (see sharedKeys).
func theItemsTriggers(w *world, _ string) error {
	w.observe()
	dirty := slices.Sorted(maps.Keys(w.inbound.dirty))
	clear(w.inbound.dirty)
	for _, id := range dirty {
		records := w.folded[id].records
		if !slices.ContainsFunc(records, func(rec item.Record) bool { return rec.Body.Triggered != nil }) {
			delete(w.inbound.keys, id)
			continue
		}
		if err := w.noAutoCloseWhileFiring(id, records); err != nil {
			return err
		}
		if err := w.firingResumesDecided(id, records); err != nil {
			return err
		}
	}
	return w.sharedKeys()
}

// noAutoCloseWhileFiring checks invariant 13 and 18's half of records on item
// id's records, and keeps what invariant 17 reads of them.
func (w *world) noAutoCloseWhileFiring(id item.ID, records []item.Record) error {
	last := map[instKey]item.TriggerStatus{}
	keys := &itemKeys{first: map[instKey]int64{}}
	w.mu.Lock()
	defer w.mu.Unlock()
	for i := range records {
		r := &records[i]
		switch b := &r.Body; {
		case b.Triggered != nil:
			t := b.Triggered
			key := t.Source + "\x00" + t.EventID
			if by, ok := w.inbound.triggeredBy[key]; ok && by != r.ID {
				return broke(18, "%s's event %s is on two item_triggered records, %s and %s, the latter item %s's record %d", t.Source, t.EventID, by, r.ID, id, r.Seq)
			}
			w.inbound.triggeredBy[key] = r.ID
			if _, ok := keys.first[instKey{t.Source, t.DedupKey}]; !ok {
				keys.first[instKey{t.Source, t.DedupKey}] = w.recordOrder[r.ID]
			}
			if t.Applied {
				last[instKey{t.Source, t.InstanceID}] = t.Status
			}
		case b.Closed != nil:
			keys.closed = true
			if !b.Closed.Auto {
				continue
			}
			for _, k := range slices.SortedFunc(maps.Keys(last), func(a, b instKey) int {
				return cmp.Or(cmp.Compare(a.source, b.source), cmp.Compare(a.instance, b.instance))
			}) {
				if last[k] == item.Firing {
					return broke(13, "item %s closed by itself, record %d, while its instance %s/%s fires", id, r.Seq, k.source, k.instance)
				}
			}
		case b.Reopened != nil:
			keys.closed = false
			keys.reopens = append(keys.reopens, w.recordOrder[r.ID])
		}
	}
	w.inbound.keys[id] = keys
	return nil
}

// firingResumesDecided checks invariant 16 on item id's records, folded one
// by one: every applied firing that tries a firing resume, as design D7 step
// 5 has it, carries a Resume (see resumeDecided). A record that does not fold
// ends the check, as invariant 5's.
func (w *world) firingResumesDecided(id item.ID, records []item.Record) error {
	s := &item.State{}
	for i := range records {
		if t := records[i].Body.Triggered; t != nil && t.Applied && t.Status == item.Firing {
			if err := w.resumeDecided(id, s, records, i); err != nil {
				return err
			}
		}
		if s.Apply(&records[i]) != nil {
			return nil
		}
	}
	return nil
}

// resumeDecided judges records[i], an applied firing of item id, after s,
// the item's state before it (invariant 16): a firing that comes while the
// item awaits its close, its run absent (review I3) or stopped, whose
// instance is resolved on the item or new to it, or whose latest firing
// resume since the stop was declined, or missed (fix round 1, M-1), tries a
// firing resume: it carries a Resume; with no run, declined:no_run;
// and resumed, followed in its append by run_extended of cause firing.
func (w *world) resumeDecided(id item.ID, s *item.State, records []item.Record, i int) error {
	r, t := &records[i], records[i].Body.Triggered
	st, _, held := s.Instance(t.Source, t.InstanceID)
	awaits := s.Opened != nil && s.Status() == item.StatusAwaitingClose && (s.Run == nil || s.Run.Stopped != nil)
	if !awaits || held && st == item.Firing && !s.FiringResumeDeclined() && !s.FiringResumeMissed() {
		return nil
	}
	w.mu.Lock()
	ownAppend := w.recordAppend[r.ID]
	w.mu.Unlock()
	switch {
	case t.Resume == "":
		return broke(16, "item %s's applied firing %s of %s/%s, record %d, came while the item awaits its close and tried no firing resume", id, t.EventID, t.Source, t.InstanceID, r.Seq)
	case s.Run == nil && t.Resume != "" && t.Resume != item.ResumeDeclined+"no_run":
		return broke(16, "item %s's applied firing %s, record %d, came while the item has no run, and records %s, not declined:no_run", id, t.EventID, r.Seq, t.Resume)
	case t.Resume != item.ResumeResumed:
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for j := i + 1; j < len(records) && w.recordAppend[records[j].ID] == ownAppend; j++ {
		if e := records[j].Body.Extended; e != nil && e.Cause == item.ExtendFiring {
			return nil
		}
	}
	return broke(16, "item %s's applied firing %s, record %d, records resumed, and its append holds no run_extended of cause firing", id, t.EventID, r.Seq)
}

// sharedKeys checks invariant 17 after every event: two items that are not
// closed share a dedup key only when a person's reopen made it so (review
// M4): one of them, closed as the other took the key, was reopened after.
func (w *world) sharedKeys() error {
	ids := slices.Sorted(maps.Keys(w.inbound.keys))
	for i, x := range ids {
		kx := w.inbound.keys[x]
		if kx.closed {
			continue
		}
		for _, y := range ids[i+1:] {
			ky := w.inbound.keys[y]
			if ky.closed {
				continue
			}
			for k, fx := range kx.first {
				fy, ok := ky.first[k]
				if !ok || slices.ContainsFunc(kx.reopens, func(o int64) bool { return o > fy }) || slices.ContainsFunc(ky.reopens, func(o int64) bool { return o > fx }) {
					continue
				}
				return broke(17, "items %s and %s, neither closed, both hold %s's dedup key %s, and no reopen made it so", x, y, k.source, k.instance)
			}
		}
	}
	return nil
}

// anUnfaultedIngestAnswers checks invariant 19 after every event: an Ingest
// the driver sent to the leading instance in the sequential mode while no
// fault was armed, no lease loss pending, no drain under way, and the
// instance's account of every item fresh (see quietFor) answered an outcome,
// never an error (see ingestOn).
func anUnfaultedIngestAnswers(w *world, _ string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	faults := w.inbound.ingestFaults[w.inbound.checkedIngestFaults:]
	w.inbound.checkedIngestFaults = len(w.inbound.ingestFaults)
	if len(faults) > 0 {
		return faults[0]
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

// startingDraft decides the start of item id, as the walk's advance does.
func startingDraft(id item.ID) func([]item.Record) ([]item.Draft, error) {
	return func(h []item.Record) ([]item.Draft, error) {
		return []item.Draft{{ID: "start-" + string(id), Item: id, Incident: h[0].Incident, Body: item.Body{V: item.Version, Started: startedBody()}}}, nil
	}
}

// autoCloseDraft decides item id's automatic close, due at due, as the
// coordinator's auto-close timer does.
func autoCloseDraft(id item.ID, due time.Time) func([]item.Record) ([]item.Draft, error) {
	return func(h []item.Record) ([]item.Draft, error) {
		return []item.Draft{{ID: "auto-close-" + string(id), Item: id, Incident: h[0].Incident, Body: item.Body{V: item.Version, Closed: &item.Closed{Auto: true, Due: due}}}}, nil
	}
}

// missedCut appends the end of item id's open round as missed ends it, by an
// outage, unseen, deciding cut, and the run's stop for it, as an on's own
// advance writes them for a round this instance did not hold, its deadline
// past (runStep checks a missed round before the kill it owes).
func missedCut(w *world, id item.ID) {
	w.appendDirect(id, item.Body{RoundEnded: &item.RoundEnded{
		Round: 1, EndedBy: item.EndedByOutage, Unseen: true, Turns: map[string]item.TurnClass{}, Decision: item.Decision{Next: item.NextStop, Stop: item.StopCut},
	}})
	st := w.stateOf(id).Run.Stop("")
	w.appendDirect(id, item.Body{RunStopped: &st})
}

// resumedItem opens an item on in, whose run the kill switch's on-row 1
// kills, and once off-row 2 is written, resumes it by an extension of one
// round, under the key resume-1: the resume records row 2, and the run opens
// its next round.
func (w *world) resumedItem(in *instance) item.ID {
	id := w.opened(in)
	w.do(in.name+" turns the kill switch on", func() {
		_, err := w.setSwitch(in, true, killing(0))
		require.NoError(w.t, err)
	})
	w.do(in.name+" turns the kill switch off", func() {
		_, err := w.setSwitch(in, false, turningOff(1))
		require.NoError(w.t, err)
	})
	w.do(in.name+" extends the item's run by a round, which resumes it", func() { w.actOn(in, actExtend, id, extending("resume-1"), actArgs{amount: rounds(1)}) })
	s := w.stateOf(id)
	require.Equal(w.t, item.StatusInvestigating, s.Status())
	require.Len(w.t, s.Run.Stops, 1)
	require.Equal(w.t, int64(2), s.Run.KillSwitchSeen())
	return id
}

// exhaustedItem opens an item on in, and passes time until its run's third
// round ends by its deadline deciding the run's stop, budget_exhausted, which
// leaves the item awaiting its close, a minute before it closes by itself.
func (w *world) exhaustedItem(in *instance) item.ID {
	id := w.opened(in)
	for range 40 {
		if w.stateOf(id).Status() == item.StatusAwaitingClose {
			break
		}
		w.do("time passes 15s", func() { w.passTime(15 * time.Second) })
	}
	s := w.stateOf(id)
	require.Equal(w.t, item.StatusAwaitingClose, s.Status())
	require.Equal(w.t, item.StopBudgetExhausted, s.Run.Stopped.Reason)
	return id
}

// refuseKills has the records' store refuse every run's stop killed, as a
// store that does not recover would.
func (w *world) refuseKills() {
	w.refuse = func(_ item.ID, drafts []item.Draft) error {
		for i := range drafts {
			if st := drafts[i].Body.RunStopped; st != nil && st.Reason == item.StopKilled {
				return errBlinked
			}
		}
		return nil
	}
}

// appendInto appends a record of body to item id straight to the records
// store, as appendDirect does, under the incident column given.
func (w *world) appendInto(id item.ID, incident item.IncidentID, body *item.Body) {
	var history []item.Record
	committed, err := w.recordStore.Append(context.Background(), id, func(h []item.Record) ([]item.Draft, error) {
		history = h
		b := *body
		b.V = item.Version
		return []item.Draft{{ID: worldIDs{w}.NewID(), Item: id, Incident: incident, Body: b}}, nil
	})
	require.NoError(w.t, err)
	w.committedRecords(nil, id, history, committed)
}

// linkedBody is a link's body: item x1, from its own incident, into y1's, as
// the admin links it, for duplicate, under the key k-1, with the digest of a
// link with with.
func linkedBody(with item.ID) *item.Body {
	return &item.Body{Linked: &item.Linked{
		Left: "incident-x1", With: "y1", By: "admin", Reason: &duplicate,
		Request: item.Request{Key: keyHash("k-1"), Digest: actDigest(actLink, "x1", actArgs{with: with})},
	}}
}

// The violation table's inbound fixtures.

// took has in's coordinator take e, by the driver's Ingest, and requires an
// outcome.
func (w *world) took(in *instance, e *mapping.Event) app.IngestResult {
	var out app.IngestResult
	var err error
	w.do(fmt.Sprintf("%s takes %s", in.name, eventText(e)), func() { out, err = w.ingestOn(in, e) })
	require.NoError(w.t, err)
	return out
}

// alertOf is alerting's event id of instance, with status at the world's
// start and d.
func alertOf(instance, id string, status item.TriggerStatus, d time.Duration) *mapping.Event {
	return inboundEvent("alerting", instance, id, status, worldStart.Add(d))
}

// inboundRow is e's row, as Ingest writes it with outcome.
func inboundRow(e *mapping.Event, outcome app.InboundOutcome, applied bool) *app.InboundEvent {
	return &app.InboundEvent{
		Source: e.Source, EventID: e.EventID, InstanceID: e.InstanceID, DedupKey: e.DedupKey, CorrelationKey: e.CorrelationKey,
		Status: e.Status, OccurredAt: e.Clamped, Applied: applied, Outcome: outcome,
	}
}

// putRow puts row straight to the inbound store, past every port and guard,
// and notes it as the guarded stores' rows are noted.
func (w *world) putRow(row *app.InboundEvent) {
	require.NoError(w.t, w.recordStore.Put(context.Background(), row))
	w.noteRow(row, "")
}

// triggeredBody is e as item_triggered, applied or not, with resume.
func triggeredBody(e *mapping.Event, applied bool, resume string) item.Body {
	return item.Body{Triggered: &item.Triggered{
		Source: e.Source, EventID: e.EventID, InstanceID: e.InstanceID, DedupKey: e.DedupKey, CorrelationKey: e.CorrelationKey,
		Status: e.Status, OccurredAt: e.Clamped, Applied: applied, Resume: resume,
	}}
}

// inboundOpening appends straight to item id the records of an inbound
// opening by e, in one append, as Ingest writes them (R126): item_opened with
// its trigger, e's applied firing, and more; and notes its row, opened.
func (w *world) inboundOpening(id item.ID, e *mapping.Event, more ...item.Body) {
	o := openedBody()
	o.Source, o.Trigger = e.Source, &item.Trigger{Source: e.Source, DedupKey: e.DedupKey, CorrelationKey: e.CorrelationKey}
	w.appendDirect(id, slices.Concat([]item.Body{{Opened: o}, triggeredBody(e, true, "")}, more)...)
	w.noteRow(inboundRow(e, app.InboundOpened, true), id)
}

// notStaffed is the item_awaiting_close of an item opened at the daily cap.
func notStaffed() item.Body {
	return item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseDailyCap}}
}

// lastTriggered returns item id's latest item_triggered.
func (w *world) lastTriggered(id item.ID) *item.Triggered {
	records := w.records()[id]
	for i := len(records) - 1; i >= 0; i-- {
		if t := records[i].Body.Triggered; t != nil {
			return t
		}
	}
	w.t.Fatalf("item %s has no item_triggered", id)
	return nil
}

// awaitsItsClose passes time until item id's run stops, its budget
// exhausted, and the item awaits its close.
func (w *world) awaitsItsClose(id item.ID) {
	for range 40 {
		if w.stateOf(id).Status() == item.StatusAwaitingClose {
			return
		}
		w.do("time passes 15s", func() { w.passTime(15 * time.Second) })
	}
	require.Equal(w.t, item.StatusAwaitingClose, w.stateOf(id).Status())
}

// flapped has in's coordinator take alerting's firing e1 of i1, which opens
// an item, then a person closes it, and a later firing e2 of i1 opens its
// flap, linked into its incident; it returns both items.
func (w *world) flapped(in *instance) (closed, flap item.ID) {
	closed = w.took(in, alertOf("i1", "e1", item.Firing, 0)).Item
	w.do(in.name+" closes it", func() { w.act(in, actClose, closed, closing("close-1")) })
	require.Equal(w.t, item.StatusClosed, w.stateOf(closed).Status())
	out := w.took(in, inboundEvent("alerting", "i1", "e2", item.Firing, w.dbNow().Add(time.Second)))
	require.Equal(w.t, app.IngestOpened, out.Outcome)
	require.Contains(w.t, w.kinds(out.Item), item.KindLinked)
	return closed, out.Item
}

// firingResumed has in's coordinator take alerting's firing e1 of i1, which
// opens an item whose run the kill switch's on then kills; once off, the
// instance's resolution and a later firing resume the run.
func (w *world) firingResumed(in *instance) item.ID {
	id := w.took(in, alertOf("i1", "e1", item.Firing, 0)).Item
	w.do(in.name+" turns the kill switch on", func() {
		_, err := w.setSwitch(in, true, killing(0))
		require.NoError(w.t, err)
	})
	w.do(in.name+" turns the kill switch off", func() {
		_, err := w.setSwitch(in, false, turningOff(1))
		require.NoError(w.t, err)
	})
	require.Equal(w.t, item.StatusAwaitingClose, w.stateOf(id).Status())
	w.took(in, alertOf("i1", "e2", item.Resolved, time.Second))
	w.took(in, alertOf("i1", "e3", item.Firing, 2*time.Second))
	require.Equal(w.t, item.ResumeResumed, w.lastTriggered(id).Resume)
	return id
}

// unstaffedInbound has in's coordinator take alerting's firing e1 of i1 at
// the daily cap, which opens an item not staffed, awaiting its close with no
// run; then a new day begins.
func (w *world) unstaffedInbound(in *instance) item.ID {
	w.do("the day's spend reaches the daily cap", func() { w.setCapped(true) })
	id := w.took(in, alertOf("i1", "e1", item.Firing, 0)).Item
	w.do("a new day begins", func() { w.setCapped(false) })
	require.Equal(w.t, item.StatusAwaitingClose, w.stateOf(id).Status())
	return id
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
		{"1: an on's success answer that names neither a run its halt failed to stop, which a close then stopped as closed, nor its item among the owed", noWorkWhileOn, stageEvent,
			1, "a1's on answered row 1 naming neither run id-5 of item id-1, which ran when the on came",
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
				w.do("a1 turns the kill switch on, its halt of the run refused", func() {
					_, err := w.setSwitch(a, true, killing(0))
					require.NoError(t, err)
				})
				w.do("a1 closes the item", func() { w.act(a, actClose, id, closing("k-1")) })
				require.Equal(t, item.StopClosed, w.stateOf(id).Run.Stopped.Reason, "the close stopped the run")
				w.mu.Lock()
				answer := &w.answers[len(w.answers)-1]
				require.Equal(t, []item.ID{id}, answer.out.Owed, "the on owed the item")
				answer.out.Stopped, answer.out.Owed = nil, nil
				w.mu.Unlock()
			}},
		{"1: an on's success answer that names neither a run its halt failed to stop, whose round then ended deciding its stop, nor its item among the owed", noWorkWhileOn, stageEvent,
			1, "a1's on answered row 1 naming neither run id-5 of item id-1, which ran when the on came",
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
				w.do("a1 turns the kill switch on, its halt of the run refused", func() {
					_, err := w.setSwitch(a, true, killing(0))
					require.NoError(t, err)
				})
				w.do("the round of the item's run ends deciding quiet, and its run stops so", func() {
					w.appendDirect(id, item.Body{RoundEnded: &item.RoundEnded{
						Round: 1, EndedBy: item.EndedByTurns, Turns: map[string]item.TurnClass{}, Decision: item.Decision{Next: item.NextStop, Stop: item.StopQuiet},
					}})
					st := w.stateOf(id).Run.Stop("")
					w.appendDirect(id, item.Body{RunStopped: &st})
				})
				w.mu.Lock()
				answer := &w.answers[len(w.answers)-1]
				require.Equal(t, []item.ID{id}, answer.out.Owed, "the on owed the item")
				answer.out.Stopped, answer.out.Owed = nil, nil
				w.mu.Unlock()
			}},
		{"1 keeps: an on's success answer that names neither a run whose missed round its own advance ended deciding the run's stop, nor its item among the owed", noWorkWhileOn, stageEvent, 0, "",
			func(_ *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				w.came("on-missed")
				w.do("the on's advance ends the run's missed round, which decides cut, and stops the run so", func() { missedCut(w, id) })
				w.noteAnswer(a, "on-missed", &app.KillSwitchOutcome{On: true, Changed: true, Seq: 1})
			}},
		{"1: an on's success answer that names neither a run whose missed round ended deciding its stop after the on answered, nor its item among the owed", noWorkWhileOn, stageEvent,
			1, "a1's on answered row 1 naming neither run id-5 of item id-1, which ran when the on came",
			func(_ *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				w.came("on-missed")
				w.noteAnswer(a, "on-missed", &app.KillSwitchOutcome{On: true, Changed: true, Seq: 1})
				w.do("the run's missed round ends deciding cut after the on answered, and stops the run so", func() { missedCut(w, id) })
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
				defer w.flying("x1", actClose, "k-1", actArgs{})()
				defer w.flying("x1", actReopen, "k-1", actArgs{})()
				w.appendDirect("x1", item.Body{Opened: openedBody()},
					item.Body{Closed: &item.Closed{By: "admin", Request: &item.Request{Key: keyHash("k-1"), Digest: actDigest(actClose, "x1", actArgs{})}}},
					item.Body{Reopened: &item.Reopened{
						By: "admin", Request: item.Request{Key: keyHash("k-1"), Digest: actDigest(actReopen, "x1", actArgs{})},
						AwaitingClose: item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseClosed},
					}})
			}},
		{"6: a record that lands with the digest of another act than the one in flight under its key", aKeyNamesOneActOfItsItem, stageEvent,
			6, "item x1's record 2 holds the key " + keyHash("k-1") + " with the digest " + actDigest(actClose, "y1", actArgs{}) +
				", not that of an act in flight under the key: [" + actDigest(actClose, "x1", actArgs{}) + "]",
			func(_ *testing.T, w *world, _, _ *instance) {
				defer w.flying("x1", actClose, "k-1", actArgs{})()
				w.appendDirect("x1", item.Body{Opened: openedBody()},
					item.Body{Closed: &item.Closed{By: "admin", Request: &item.Request{Key: keyHash("k-1"), Digest: actDigest(actClose, "y1", actArgs{})}}})
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
				due, ok := w.stateOf(id).AutoCloseAt(worldAutoClose, time.Time{})
				require.True(t, ok, "an item a person opened has no trigger to fire")
				w.do("a1's records take an automatic close", func() {
					_, err := recordsPort{a}.Append(holding.WithEpoch(ctx, epochOf(a)), id, autoCloseDraft(id, due))
					require.NoError(t, err)
				})
			}},
		{"8: an automatic close stamped before its due", autoCloseKeepsItsPauseAndItsDeadline, stageEvent,
			8, "item id-1's automatic close is stamped 2026-11-02 09:00:59.000001 +0000 UTC, before its due 2026-11-02 09:01:00.000001 +0000 UTC",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.unstaffedAtTheCap(a)
				s := w.stateOf(id)
				due, ok := s.AutoCloseAt(worldAutoClose, time.Time{})
				require.True(t, ok, "an item a person opened has no trigger to fire")
				w.mu.Lock()
				w.autoClosed = append(w.autoClosed, autoCloseNote{
					inst: a.name, before: s, rec: item.Record{Item: id, At: due.Add(-time.Second), Body: item.Body{Closed: &item.Closed{Auto: true, Due: due}}},
				})
				w.mu.Unlock()
			}},
		{"8: an automatic close due at another time than the deadline its state gives", autoCloseKeepsItsPauseAndItsDeadline, stageEvent,
			8, "a1 recorded item id-1's automatic close due at 2026-11-02 09:02:00.000001 +0000 UTC, not at 2026-11-02 09:01:00.000001 +0000 UTC, which its state before it gives",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.unstaffedAtTheCap(a)
				s := w.stateOf(id)
				due, ok := s.AutoCloseAt(worldAutoClose, time.Time{})
				require.True(t, ok, "an item a person opened has no trigger to fire")
				due = due.Add(time.Minute)
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
		{"9: an item admitted by the walk out of order once its promotion's outcome was unknown, its start committed in the event", theQueuesOrder, stageEvent,
			9, "item y1, admitted by the walk, does not rank ahead of item x1, still waiting",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.do("y1 starts, and x1 waits", func() { w.appendDirect("y1", item.Body{Started: startedBody()}) })
				w.mu.Lock()
				w.unsure["y1"] = true
				w.mu.Unlock()
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1", "y1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1"}}
			}},
		{"9: an item admitted out of order while another item's promotion has an unknown outcome", theQueuesOrder, stageEvent,
			9, "item y1, admitted by the walk, does not rank ahead of item x1, still waiting",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.appendDirect("z1", item.Body{Opened: openedBody()})
				w.do("y1 starts, and x1 waits", func() { w.appendDirect("y1", item.Body{Started: startedBody()}) })
				w.mu.Lock()
				w.unsure["z1"] = true
				w.mu.Unlock()
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1", "y1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1"}}
			}},
		{"9: a younger item admitted after the coordinator read in an older one, whose opening's answer was lost, while the older one waits", theQueuesOrder, stageEvent,
			9, "item y1, admitted by the walk, does not rank ahead of item x1, still waiting",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.mu.Lock()
				w.steps++
				w.heldAt["x1"] = w.steps
				w.mu.Unlock()
				w.do("y1 starts after the coordinator read x1, and x1 waits", func() { w.appendDirect("y1", item.Body{Started: startedBody()}) })
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1", "y1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1"}}
			}},
		{"9: a younger item a takeover's leader admits after it read in an older one, while the older one waits, with a fenced leader's try of it before the read", theQueuesOrder, stageEvent,
			9, "item y1, admitted by the walk, does not rank ahead of item x1, still waiting",
			func(t *testing.T, w *world, a, b *instance) {
				epoch := epochOf(a)
				w.do("a1 crashes", func() { w.discard(a) })
				w.do("time passes the lease's term", func() { w.passTime(worldTTL) })
				require.Same(t, b, w.leading())
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.armFault(faultRecordsFail)
				w.do("a1's records, under its old epoch, fail y1's start", func() {
					_, err := recordsPort{a}.Append(holding.WithEpoch(ctx, epoch), "y1", startingDraft("y1"))
					require.ErrorIs(t, err, errBlinked)
				})
				w.mu.Lock()
				require.Len(t, w.tries["y1"], 1, "a1's try is noted")
				w.steps++
				w.heldAt["x1"] = w.steps
				w.mu.Unlock()
				w.do("b1 starts y1 after it read x1, and x1 waits", func() {
					_, err := recordsPort{b}.Append(holding.WithEpoch(ctx, epochOf(b)), "y1", startingDraft("y1"))
					require.NoError(t, err)
				})
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1", "y1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1"}}
			}},
		{"9 keeps: an older item the walk admitted first, its start failing, waiting again after a takeover, while the younger one it admitted next started",
			theQueuesOrder, stageEvent, 0, "",
			func(t *testing.T, w *world, a, b *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.armFault(faultRecordsFail)
				w.do("a1's walk admits x1, whose start fails, then y1, which starts; a1 crashes, and b1 takes over once the lease's term passes", func() {
					_, err := recordsPort{a}.Append(holding.WithEpoch(ctx, epochOf(a)), "x1", startingDraft("x1"))
					require.ErrorIs(t, err, errBlinked)
					_, err = recordsPort{a}.Append(holding.WithEpoch(ctx, epochOf(a)), "y1", startingDraft("y1"))
					require.NoError(t, err)
					w.discard(a)
					w.passTime(worldTTL)
				})
				require.Same(t, b, w.leading())
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1", "y1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1"}}
			}},
		{"9: an older item whose start failed, put back in the waiting list by the holding that still leads, which then admits a younger one", theQueuesOrder, stageEvent,
			9, "item y1, admitted by the walk, does not rank ahead of item x1, still waiting",
			func(t *testing.T, w *world, a, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.armFault(faultRecordsFail)
				w.do("a1's walk admits x1, whose start fails", func() {
					_, err := recordsPort{a}.Append(holding.WithEpoch(ctx, epochOf(a)), "x1", startingDraft("x1"))
					require.ErrorIs(t, err, errBlinked)
				})
				w.do("time passes 10s", func() { w.passTime(10 * time.Second) })
				require.Same(t, a, w.leading())
				w.do("a1 starts y1, x1 back in the waiting list", func() {
					_, err := recordsPort{a}.Append(holding.WithEpoch(ctx, epochOf(a)), "y1", startingDraft("y1"))
					require.NoError(t, err)
				})
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
		{"9 keeps: a younger item admitted before the coordinator holds an older one, whose opening's answer was lost and its lookup failed, the younger one's start failing and committing on its retry after the older one's retry took it in waiting",
			theQueuesOrder, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				for range worldLimit - 1 {
					w.opened(a)
				}
				w.armFault(faultSwitchLost)
				w.armRead(a, faultSwitchRead)
				w.do("a1 turns the kill switch off, its answer lost, its read failed", func() { _, _ = w.setSwitch(a, false, turningOff(0)) })
				require.True(t, a.coord.Lifecycle().KillSwitchUnsettled, "the walk admits none while the view is unsettled")
				w.armFault(faultRecordsLost)
				w.armRead(a, faultRecordsRead)
				w.do("a1 opens an item, its answer lost, its lookup failed", func() { w.open(a, openInput()) })
				w.mu.Lock()
				older := w.itemIDs[len(w.itemIDs)-1]
				w.mu.Unlock()
				require.Len(t, w.records(), worldLimit, "the older item's opening landed")
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				younger := w.opened(a)
				require.Equal(t, []item.ID{younger}, a.coord.Lifecycle().Waiting, "the coordinator does not hold the older item")
				w.armFault(faultRecordsFail)
				w.do("time passes two retry waits: the view settles and the walk admits the younger item, whose start fails; the older item's retry takes it in, waiting; the younger item's retry starts it", func() {
					w.passTime(2 * worldRetry)
				})
				require.Equal(t, item.StatusInvestigating, w.stateOf(younger).Status())
				_, after := w.lifecycles()
				require.Equal(t, []item.ID{older}, after.Waiting, "the older item waits, the limit reached")
			}},
		{"9 keeps: a younger item admitted before the coordinator holds an older one, whose opening's answer was lost and its lookup failed, the younger one's start failing and committing on its retry in a later event than the older one's retry, which took it in waiting",
			theQueuesOrder, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				for range worldLimit - 1 {
					w.opened(a)
				}
				w.armFault(faultSwitchLost)
				w.armRead(a, faultSwitchRead)
				w.do("a1 turns the kill switch off, its answer lost, its read failed", func() { _, _ = w.setSwitch(a, false, turningOff(0)) })
				require.True(t, a.coord.Lifecycle().KillSwitchUnsettled, "the walk admits none while the view is unsettled")
				w.armFault(faultRecordsLost)
				w.armRead(a, faultRecordsRead)
				w.do("a1 opens an item, its answer lost, its lookup failed", func() { w.open(a, openInput()) })
				w.mu.Lock()
				older := w.itemIDs[len(w.itemIDs)-1]
				w.mu.Unlock()
				require.Len(t, w.records(), worldLimit, "the older item's opening landed")
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				younger := w.opened(a)
				require.Equal(t, []item.ID{younger}, a.coord.Lifecycle().Waiting, "the coordinator does not hold the older item")
				w.armFault(faultRecordsFail)
				for range 2 * worldRetry / (250 * time.Millisecond) {
					w.do("time passes 250ms", func() { w.passTime(250 * time.Millisecond) })
					require.NoError(t, theQueuesOrder(w, stageEvent), "after each event")
				}
				require.Equal(t, item.StatusInvestigating, w.stateOf(younger).Status())
				_, after := w.lifecycles()
				require.Equal(t, []item.ID{older}, after.Waiting, "the older item waits, the limit reached")
			}},
		{"9 keeps: a promotion whose append failed and whose lookup failed, which the account counts, while an older item waits",
			theQueuesOrder, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				for range worldLimit + 1 {
					w.opened(a)
				}
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				y := w.opened(a)
				_, after := w.lifecycles()
				require.Len(t, after.Waiting, 2)
				w.pairing = pairAlways
				w.armFault(faultRecordsFail)
				w.do("a1 promotes the younger item, its append failed, its lookup failed", func() { w.act(a, actPromote, y, promoting("k-1")) })
				_, after = w.lifecycles()
				require.Len(t, after.Waiting, 1, "the account counts the younger item, whose promotion's outcome is unknown")
				require.NotContains(t, after.Waiting, y)
				require.Nil(t, w.stateOf(y).Promotion, "its promotion did not land")
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
		{"1: an on's success answer that names neither a resumed run that runs on, its halt refused, nor its item among the owed", noWorkWhileOn, stageEvent,
			1, "a1's on answered row 3 naming neither run id-5 of item id-1, which ran when the on came",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.resumedItem(a)
				w.refuseKills()
				w.do("a1 turns the kill switch on, its halt of the run refused", func() {
					_, err := w.setSwitch(a, true, killing(2))
					require.NoError(t, err)
				})
				require.Nil(t, w.stateOf(id).Run.Stopped, "the resumed run runs on")
				w.mu.Lock()
				answer := &w.answers[len(w.answers)-1]
				require.Equal(t, []item.ID{id}, answer.out.Owed, "the on owed the item")
				answer.out.Stopped, answer.out.Owed = nil, nil
				w.mu.Unlock()
			}},
		{"1: an on's success answer that names neither a resumed run whose round before the resume decided its stop, its halt refused and a close then stopping it, nor its item among the owed", noWorkWhileOn, stageEvent,
			1, "a1's on answered row 1 naming neither run id-5 of item id-1, which ran when the on came",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.exhaustedItem(a)
				w.armFault(faultEventsFail)
				w.do("a1 extends the item's run by a round, which resumes it, and its board's post fails", func() {
					w.actOn(a, actExtend, id, extending("resume-1"), actArgs{amount: rounds(1)})
				})
				require.True(t, w.stateOf(id).Run.Resumed(), "resumed, its next round not opened")
				w.refuseKills()
				w.do("a1 turns the kill switch on, its halt of the run refused", func() {
					_, err := w.setSwitch(a, true, killing(0))
					require.NoError(t, err)
				})
				w.do("a1 closes the item", func() { w.act(a, actClose, id, closing("k-1")) })
				require.Equal(t, item.StopClosed, w.stateOf(id).Run.Stopped.Reason, "the close stopped the run")
				w.mu.Lock()
				answer := &w.answers[len(w.answers)-1]
				require.Equal(t, []item.ID{id}, answer.out.Owed, "the on owed the item")
				answer.out.Stopped, answer.out.Owed = nil, nil
				w.mu.Unlock()
			}},
		{"1 keeps: a turn of a resumed run started after an on-row older than its resume", noWorkWhileOn, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				run := w.runOf(w.resumedItem(a))
				w.do("a1's runner starts a turn of the resumed run", func() { require.NoError(t, startTurnOf(a, epochOf(a), run)) })
			}},
		{"1: a turn of a resumed run started after an on-row newer than its resume", noWorkWhileOn, stageEvent,
			1, "a1 called Turns.Start for run id-5, agent release-investigator, under epoch 1, after on-row 3 committed, which a coordinator wrote",
			func(t *testing.T, w *world, a, _ *instance) {
				run := w.runOf(w.resumedItem(a))
				w.refuseKills()
				w.do("a1 turns the kill switch on, its halt of the run refused", func() {
					_, err := w.setSwitch(a, true, killing(2))
					require.NoError(t, err)
				})
				w.do("a1's runner is told to start a turn of the resumed run", func() { require.NoError(t, startTurnOf(a, epochOf(a), run)) })
			}},
		{"2: a resumed run left owing the latest on-row after the first settling stage, its halt refused by a store that does not recover",
			everyRunAliveAtAnOnIsHalted, stageSettled, 2, "item id-1, investigating, still owes the kill switch's latest on-row 3 after the first settling stage",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.resumedItem(a)
				w.refuseKills()
				w.do("a1 turns the kill switch on", func() {
					_, err := w.setSwitch(a, true, killing(2))
					require.NoError(t, err)
				})
				w.settleKillSwitch()
				require.True(t, w.stateOf(id).Owes(3), "the item still owes the on-row")
			}},
		{"2 keeps: a resumed run that does not owe an on-row older than its resume", everyRunAliveAtAnOnIsHalted, stageSettled, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				s := w.stateOf(w.resumedItem(a))
				table, err := w.switchStore.KillSwitch(ctx)
				require.NoError(t, err)
				require.Equal(t, int64(1), table.OnSeq, "the latest on-row, older than the resume")
				require.Less(t, s.Run.Created.KillSwitchSeen, table.OnSeq, "the run's creation saw no row")
				require.False(t, s.Owes(table.OnSeq))
			}},
		{"5: a link whose incident column is not the incident its other item had at its commit", recordsAndEpochs, stageEvent,
			5, "item x1's link, record 2, holds incident incident-z, not incident-y1, which item y1 had at its commit",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.appendInto("x1", "incident-z", linkedBody("y1"))
			}},
		{"5: a record whose incident column is not its item's incident after it", recordsAndEpochs, stageEvent,
			5, "item x1's record 2 holds incident incident-z, and the item is in incident-x1 after it",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.appendInto("x1", "incident-z", &item.Body{Started: startedBody()})
			}},
		{"5: once quiet, an incident whose items the records' IncidentItems lists otherwise than the folds", recordsAndEpochs, stageQuiet,
			5, "once quiet, incident incident-x1 holds [x1] by the records' IncidentItems, and [] by the folds",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.observe()
				w.folded["x1"].state.Incident = "incident-y1"
			}},
		{"5 keeps: a link and an unlink through the coordinator, once quiet", recordsAndEpochs, stageQuiet, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				x, y := w.opened(a), w.opened(a)
				w.do("a1 links x with y", func() { w.actOn(a, actLink, x, linking("k-1"), actArgs{with: y}) })
				require.Equal(t, w.stateOf(y).Incident, w.stateOf(x).Incident)
				w.do("a1 unlinks y", func() { w.actOn(a, actUnlink, y, linking("k-2"), actArgs{}) })
				require.NotEqual(t, w.stateOf(y).Incident, w.stateOf(x).Incident)
				w.settleQuiet()
			}},
		{"6: a link's record that holds its key with the digest of a link with another item than the one in flight", aKeyNamesOneActOfItsItem, stageEvent,
			6, "item x1's record 2 holds the key " + keyHash("k-1") + " with the digest " + actDigest(actLink, "x1", actArgs{with: "z1"}) +
				", not that of an act in flight under the key: [" + actDigest(actLink, "x1", actArgs{with: "y1"}) + "]",
			func(_ *testing.T, w *world, _, _ *instance) {
				defer w.flying("x1", actLink, "k-1", actArgs{with: "y1"})()
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.appendInto("x1", "incident-y1", linkedBody("z1"))
			}},
		{"6: one request's key on a link's record and an unlink's of an item", aKeyNamesOneActOfItsItem, stageEvent,
			6, "item x1 holds the key " + keyHash("k-1") + " on two records, 2 and 3",
			func(_ *testing.T, w *world, _, _ *instance) {
				defer w.flying("x1", actLink, "k-1", actArgs{with: "y1"})()
				defer w.flying("x1", actUnlink, "k-1", actArgs{})()
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.appendInto("x1", "incident-y1", linkedBody("y1"))
				w.appendInto("x1", "incident-x2", &item.Body{Unlinked: &item.Unlinked{
					Left: "incident-y1", By: "admin", Reason: &duplicate, Request: item.Request{Key: keyHash("k-1"), Digest: actDigest(actUnlink, "x1", actArgs{})},
				}})
			}},
		{"6: a link that answered success under a key its item held for a link with another item before it came", aKeyNamesOneActOfItsItem, stageEvent,
			6, "item x1's act links under k-1 answered success, while its record 2 held the key with the digest " + actDigest(actLink, "x1", actArgs{with: "y1"}),
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				func() {
					defer w.flying("x1", actLink, "k-1", actArgs{with: "y1"})()
					w.appendInto("x1", "incident-y1", linkedBody("y1"))
				}()
				w.keyAnswered("x1", actLink, "k-1", actArgs{with: "z1"}, w.order)
			}},
		{"6: a promotion that answered success under a key its item held for a close before it came", aKeyNamesOneActOfItsItem, stageEvent,
			6, "item x1's act promotes under k-1 answered success, while its record 2 held the key with the digest " + actDigest(actClose, "x1", actArgs{}),
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				func() {
					defer w.flying("x1", actClose, "k-1", actArgs{})()
					w.appendDirect("x1", item.Body{Closed: &item.Closed{By: "admin", Request: &item.Request{Key: keyHash("k-1"), Digest: actDigest(actClose, "x1", actArgs{})}}})
				}()
				w.keyAnswered("x1", actPromote, "k-1", actArgs{}, w.order)
			}},
		{"6 keeps: a promotion answered before a concurrent close under its key, whose record the store holds before its append noted it", aKeyNamesOneActOfItsItem, stageEvent, 0, "",
			func(t *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				before := w.order
				_, err := w.recordStore.Append(ctx, "x1", func(h []item.Record) ([]item.Draft, error) {
					return []item.Draft{{ID: "close-x1", Item: "x1", Incident: h[0].Incident, Body: item.Body{V: item.Version, Closed: &item.Closed{
						By: "admin", Request: &item.Request{Key: keyHash("k-1"), Digest: actDigest(actClose, "x1", actArgs{})},
					}}}}, nil
				})
				require.NoError(t, err)
				w.keyAnswered("x1", actPromote, "k-1", actArgs{}, before)
			}},
		{"6 keeps: an extension and a link retried under their keys, each with another amount or item refused key_reused", aKeyNamesOneActOfItsItem, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				x, y, z := w.opened(a), w.opened(a), w.opened(a)
				w.do("a1 extends x's run by a round, then by two under the same key, then by a round again", func() {
					w.actOn(a, actExtend, x, extending("k-1"), actArgs{amount: rounds(1)})
					_, err := a.coord.Extend(ctx, x, rounds(2), extending("k-1"))
					require.ErrorIs(t, err, app.ErrKeyReused)
					w.actOn(a, actExtend, x, extending("k-1"), actArgs{amount: rounds(1)})
				})
				w.do("a1 links x with y, then with z under the same key, then with y again", func() {
					w.actOn(a, actLink, x, linking("k-2"), actArgs{with: y})
					_, err := a.coord.Link(ctx, x, z, linking("k-2"))
					require.ErrorIs(t, err, app.ErrKeyReused)
					w.actOn(a, actLink, x, linking("k-2"), actArgs{with: y})
				})
				require.Len(t, w.stateOf(x).Run.Extensions, 1)
				require.Equal(t, w.stateOf(y).Incident, w.stateOf(x).Incident)
			}},
		{"7: a resumed run's second stop with a live session", aStoppedRunLeavesNothingRunning, stageQuiet,
			7, "item id-1's run id-5 stopped, and a1 keeps the session of release-investigator live",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.resumedItem(a)
				run := w.runOf(id)
				w.do("a1 turns the kill switch on, which stops the resumed run again", func() {
					_, err := w.setSwitch(a, true, killing(2))
					require.NoError(t, err)
				})
				require.Len(t, w.stateOf(id).Run.Stops, 1)
				require.NotNil(t, w.stateOf(id).Run.Stopped)
				w.do("a session of its run stays live", func() {
					w.mu.Lock()
					a.live[sessionKey{run, "release-investigator"}] = &liveSession{epoch: epochOf(a), round: 2}
					w.mu.Unlock()
				})
			}},
		{"7: a resumed run stopped again whose board holds no stop entry after its resume entry", aStoppedRunLeavesNothingRunning, stageQuiet,
			7, "item id-1's run id-5 stopped after its board's resume entry, and its board holds no stop entry after it",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.resumedItem(a)
				run := w.runOf(id)
				w.do("the resumed run's round ends deciding quiet, and its run stops so, its board told nothing", func() {
					w.appendDirect(id, item.Body{RoundEnded: &item.RoundEnded{
						Round: 2, EndedBy: item.EndedByTurns, Turns: map[string]item.TurnClass{}, Decision: item.Decision{Next: item.NextStop, Stop: item.StopQuiet},
					}})
					st := w.stateOf(id).Run.Stop("")
					w.appendDirect(id, item.Body{RunStopped: &st})
					w.mu.Lock()
					clear(a.live)
					for _, rt := range a.turns {
						rt.stopped = true
					}
					w.closedRuns[run] = w.order
					w.mu.Unlock()
				})
			}},
		{"7 keeps: a resumed run stopped again by the kill switch, its board's stop entry after its resume entry", aStoppedRunLeavesNothingRunning, stageQuiet, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.resumedItem(a)
				w.do("a1 turns the kill switch on, which stops the resumed run again", func() {
					_, err := w.setSwitch(a, true, killing(2))
					require.NoError(t, err)
				})
				w.settleQuiet()
				require.NotNil(t, w.stateOf(id).Run.Stopped)
			}},
		{"9 keeps: a resume whose append failed and whose lookup failed, which the account counts, while an older item waits", lifecycleInvariants, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.exhaustedItem(a)
				w.opened(a)
				w.opened(a)
				waiting := w.opened(a)
				_, after := w.lifecycles()
				require.Equal(t, []item.ID{waiting}, after.Waiting)
				w.pairing = pairAlways
				w.armFault(faultRecordsFail)
				w.do("a1 extends the item's run by a round, its append failed, its lookup failed", func() {
					w.actOn(a, actExtend, id, extending("resume-1"), actArgs{amount: rounds(1)})
				})
				_, after = w.lifecycles()
				require.Equal(t, worldLimit+1, after.Investigating, "the account counts the item, whose resume's outcome is unknown")
				require.Equal(t, []item.ID{waiting}, after.Waiting)
				require.Equal(t, item.StatusAwaitingClose, w.stateOf(id).Status(), "its resume did not land")
				w.mu.Lock()
				require.True(t, w.unsure[id], "noted unsure")
				w.mu.Unlock()
			}},
		{"2: a resumed run stopped killed by an on-row it had seen at its resume", everyRunAliveAtAnOnIsHalted, stageEvent,
			2, "item id-1's run id-5 stopped killed by on-row 1, which its creation or a resume before the stop had seen, at row 2",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.resumedItem(a)
				w.do("the resumed run is halted, killed by on-row 1", func() {
					w.appendDirect(id, w.stateOf(id).Run.Halt(item.StopKilled, &item.Ending{EndedAt: w.dbNow(), KilledBy: 1})...)
				})
			}},
		{"2: a resume that recorded a kill switch row below the one its coordinator knew", everyRunAliveAtAnOnIsHalted, stageEvent,
			2, "item id-1's resume under resume-1 recorded the kill switch row 2, below row 3, which its coordinator knew",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.resumedItem(a)
				w.judgeResumeSeen(id, "resume-1", 3, 0)
			}},
		{"2 keeps: a resumed run killed by an on-row newer than its resume", everyRunAliveAtAnOnIsHalted, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.resumedItem(a)
				w.do("a1 turns the kill switch on", func() {
					_, err := w.setSwitch(a, true, killing(2))
					require.NoError(t, err)
				})
				require.Equal(t, int64(3), w.stateOf(id).Run.Stopped.Killed)
			}},
		{"4: a leading snapshot that counts fewer items investigating than the records hold", theLimitAndOne, stageEvent,
			4, "the leading coordinator's snapshot counts 0 items investigating, while the records hold 1, 0 of them an opening it does not hold",
			func(_ *testing.T, w *world, a, _ *instance) {
				w.opened(a)
				w.after = app.Lifecycle{Limit: worldLimit}
			}},
		{"5: a link committed 11 minutes after its other item's close, past the window of late rulings", recordsAndEpochs, stageEvent,
			5, "item x1's link, record 2, came at 2026-11-02 09:11:00.000001 +0000 UTC, past item y1's window of late rulings, which its close at 2026-11-02 09:00:00.000001 +0000 UTC opened for 10m0s",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("y1", item.Body{Opened: openedBody()}, item.Body{Closed: &item.Closed{By: "admin"}})
				w.do("time passes 11m", func() { w.passTime(11 * time.Minute) })
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.appendInto("x1", "incident-y1", linkedBody("y1"))
			}},
		{"5: an unlink committed 11 minutes after its item's close, past the window of late rulings", recordsAndEpochs, stageEvent,
			5, "item x1's unlink, record 4, came at 2026-11-02 09:11:00 +0000 UTC, past its window of late rulings, which its close at 2026-11-02 09:00:00.000002 +0000 UTC opened for 10m0s",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.appendInto("x1", "incident-y1", linkedBody("y1"))
				w.appendInto("x1", "incident-y1", &item.Body{Closed: &item.Closed{By: "admin"}})
				w.do("time passes 11m", func() { w.passTime(11 * time.Minute) })
				w.appendInto("x1", "incident-x2", &item.Body{Unlinked: &item.Unlinked{
					Left: "incident-y1", By: "admin", Reason: &duplicate, Request: item.Request{Key: keyHash("k-2"), Digest: actDigest(actUnlink, "x1", actArgs{})},
				}})
			}},
		{"5 keeps: a link committed 9 minutes after its other item's close", recordsAndEpochs, stageEvent, 0, "",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("y1", item.Body{Opened: openedBody()}, item.Body{Closed: &item.Closed{By: "admin"}})
				w.do("time passes 9m", func() { w.passTime(9 * time.Minute) })
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.appendInto("x1", "incident-y1", linkedBody("y1"))
			}},
		{"5: an unlink of an item alone in the incident it left", recordsAndEpochs, stageEvent,
			5, "item x1's unlink, record 2, left incident incident-x1, which held [x1] alone at its commit",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.appendInto("x1", "incident-z", &item.Body{Unlinked: &item.Unlinked{
					Left: "incident-x1", By: "admin", Reason: &duplicate, Request: item.Request{Key: keyHash("k-1"), Digest: actDigest(actUnlink, "x1", actArgs{})},
				}})
			}},
		{"5: a session moved with a budget above its round's and every extension's since", recordsAndEpochs, stageEvent,
			5, "a1 moved the session of release-investigator in run id-5 into round 1 with 200000 tokens, outside the 90000 to 90000 its round's budget and the extensions since it give",
			func(t *testing.T, w *world, a, _ *instance) {
				run := w.runOf(w.opened(a))
				w.do("a1 moves a session of the run with the whole budget", func() {
					require.NoError(t, sessionsPort{a}.Move(run, "release-investigator", 1, w.dbNow().Add(time.Minute), 200_000))
				})
			}},
		{"5 keeps: the sessions of a running run raised by an extension of tokens", recordsAndEpochs, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				w.do("a1 extends the run by 30000 tokens", func() { w.actOn(a, actExtend, id, extending("k-1"), actArgs{amount: tokens(30_000)}) })
				w.mu.Lock()
				defer w.mu.Unlock()
				require.NotEmpty(t, w.moves, "the extension moved the sessions")
				require.Equal(t, int64(120_000), w.moves[len(w.moves)-1].budget, "the round's 90000 and the extension's 30000")
			}},
		{"5: a run stopped budget_exhausted that its budget and its extensions do not exhaust", recordsAndEpochs, stageEvent,
			5, "item id-1's run id-5 stopped budget_exhausted after round 1, which its budget and its extensions, {WallClock:16m40s Tokens:150000 Rounds:5}, do not exhaust",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				w.do("a1 extends the run by two rounds", func() { w.actOn(a, actExtend, id, extending("k-1"), actArgs{amount: rounds(2)}) })
				w.do("the run's first round ends deciding budget_exhausted, and its run stops so", func() {
					w.appendDirect(id, item.Body{RoundEnded: &item.RoundEnded{
						Round: 1, EndedBy: item.EndedByDeadline, Turns: map[string]item.TurnClass{}, Rounds: 1, WallClock: 2 * time.Minute,
						Decision: item.Decision{Next: item.NextStop, Stop: item.StopBudgetExhausted},
					}})
					st := w.stateOf(id).Run.Stop("")
					w.appendDirect(id, item.Body{RunStopped: &st})
				})
			}},
		{"5 keeps: a run stopped budget_exhausted once its third round ends", recordsAndEpochs, stageEvent, 0, "",
			func(_ *testing.T, w *world, a, _ *instance) { w.exhaustedItem(a) }},
		{"9 keeps: the slot an unknown resume held is freed once the item loads, and the walk admits the item waiting", lifecycleInvariants, stageQuiet, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.exhaustedItem(a)
				first := w.opened(a)
				w.opened(a)
				waiting := w.opened(a)
				w.pairing = pairAlways
				w.armFault(faultRecordsFail)
				w.do("a1 extends the item's run by a round, its append failed, its lookup failed", func() {
					w.actOn(a, actExtend, id, extending("resume-1"), actArgs{amount: rounds(1)})
				})
				w.pairing = pairNever
				w.do("a1 closes an item investigating", func() { w.act(a, actClose, first, closing("k-1")) })
				_, after := w.lifecycles()
				require.Equal(t, []item.ID{waiting}, after.Waiting, "the unknown resume still holds a slot")
				w.do("time passes the retry wait, and the item loads", func() { w.passTime(worldRetry) })
				_, after = w.lifecycles()
				require.Empty(t, after.Waiting, "the walk admitted the item waiting")
				require.Equal(t, item.StatusAwaitingClose, w.stateOf(id).Status())
				w.settleQuiet()
			}},
		{"10 keeps: an extension refused closed after the item's close", closedItemsRunNothing, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("k-1")) })
				w.do("a1 extends its run by a round", func() {
					_, err := a.coord.Extend(ctx, id, rounds(1), extending("k-2"))
					var refused *app.Refused
					require.ErrorAs(t, err, &refused)
					require.Equal(t, app.CodeClosed, refused.Code)
				})
				require.Equal(t, item.StatusClosed, w.stateOf(id).Status())
			}},
		{"10 keeps: a late close retry after a reopen and a resume, which leaves the resumed run going", closedItemsRunNothing, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.opened(a)
				run := w.runOf(id)
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("close-k")) })
				w.do("a1 reopens it", func() { w.act(a, actReopen, id, reopening("reopen-1")) })
				w.do("a1 extends its run by a round, which resumes it", func() {
					w.actOn(a, actExtend, id, extending("extend-1"), actArgs{amount: rounds(1)})
				})
				kinds := w.kinds(id)
				w.do("a1's close is retried late under its key", func() { w.act(a, actClose, id, closing("close-k")) })
				require.Equal(t, kinds, w.kinds(id), "nothing is recorded")
				require.Nil(t, w.stateOf(id).Run.Stopped, "the resumed run goes on")
				sessions, _ := w.liveRuns(a)
				require.Contains(t, sessions, run, "its sessions are live")
			}},
		{"5: an inbound row put under an epoch the lease moved past, which no guard checked", recordsAndEpochs, stageEvent,
			5, "a write to the inbound store carried epoch 1, while the lease's was 2",
			func(t *testing.T, w *world, a, b *instance) {
				epoch := epochOf(a)
				w.do("a1 crashes", func() { w.discard(a) })
				w.do("time passes the lease's term", func() { w.passTime(worldTTL) })
				require.Same(t, b, w.leading())
				w.armFault(faultSkipGuard)
				w.do("a1's records put a row under its old epoch", func() {
					require.NoError(t, recordsPort{a}.Put(holding.WithEpoch(ctx, epoch), inboundRow(alertOf("i1", "e1", item.Resolved, 0), app.InboundResolvedUnmatched, true)))
				})
			}},
		{"6 keeps: a firing resume's run_extended and a flap's item_linked, their keys in flight under no act", aKeyNamesOneActOfItsItem, stageEvent, 0, "",
			func(_ *testing.T, w *world, _, _ *instance) {
				h := keyHash("firing:e1")
				w.appendDirect("x1", item.Body{Opened: openedBody()},
					item.Body{Extended: &item.Extended{Cause: item.ExtendFiring, By: "source:alerting", Request: item.Request{Key: h, Digest: h}}},
					item.Body{Linked: &item.Linked{Left: "incident-x1", With: "y1", By: "source:alerting", Request: item.Request{Key: keyHash("flap:e1"), Digest: keyHash("flap:e1")}}})
			}},
		{"6 keeps: a firing resume through the coordinator", aKeyNamesOneActOfItsItem, stageEvent, 0, "",
			func(_ *testing.T, w *world, a, _ *instance) { w.firingResumed(a) }},
		{"6 keeps: an inbound opening and its flap through the coordinator, no opening in flight under their keys", aKeyNamesOneActOfItsItem, stageEvent, 0, "",
			func(_ *testing.T, w *world, a, _ *instance) { w.flapped(a) }},
		{"6: a person's extension under a key in flight under no act", aKeyNamesOneActOfItsItem, stageEvent,
			6, "item x1's record 2 holds the key " + keyHash("k-1") + " with the digest " + actDigest(actExtend, "x1", actArgs{}) + ", not that of an act in flight under the key: []",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()},
					item.Body{Extended: &item.Extended{By: "admin", Request: item.Request{Key: keyHash("k-1"), Digest: actDigest(actExtend, "x1", actArgs{})}}})
			}},
		{"6: a person's link under a key in flight under no act", aKeyNamesOneActOfItsItem, stageEvent,
			6, "item x1's record 2 holds the key " + keyHash("k-1"),
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()}, *linkedBody("y1"))
			}},
		{"6: a person's keyed opening with another digest, beside an inbound opening's", aKeyNamesOneActOfItsItem, stageEvent,
			6, "item x1 opened with the digest sha256:other, not that of an opening in flight under its key: []",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.inboundOpening("y1", alertOf("i1", "e1", item.Firing, 0))
				opened := openedBody()
				opened.Digest = "sha256:other"
				w.appendDirect("x1", item.Body{Opened: opened})
			}},
		{"8 keeps: an inbound item's automatic close, due once its trigger resolved", autoCloseKeepsItsPauseAndItsDeadline, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.unstaffedInbound(a)
				w.took(a, alertOf("i1", "e2", item.Resolved, time.Second))
				w.do("time passes 2m", func() { w.passTime(2 * time.Minute) })
				require.Equal(t, item.StatusClosed, w.stateOf(id).Status())
			}},
		{"8: an automatic close recorded while the item's trigger fires", autoCloseKeepsItsPauseAndItsDeadline, stageEvent,
			8, "a1 recorded item " + string(app.ItemID(inboundTenant, "inbound:alerting:e1")) + "'s automatic close due at 2026-11-02 09:01:00 +0000 UTC while its trigger fires",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.unstaffedInbound(a)
				s := w.stateOf(id)
				due := worldStart.Add(time.Minute)
				w.mu.Lock()
				w.autoClosed = append(w.autoClosed, autoCloseNote{
					inst: a.name, before: s, rec: item.Record{Item: id, At: due, Body: item.Body{Closed: &item.Closed{Auto: true, Due: due}}},
				})
				w.mu.Unlock()
			}},
		{"9 keeps: an inbound item admitted by the walk in its order, its item_triggered in its opening's append", theQueuesOrder, stageEvent, 0, "",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.inboundOpening("x1", alertOf("i1", "e1", item.Firing, 0))
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.do("x1 starts, and y1 waits", func() { w.appendDirect("x1", item.Body{Started: startedBody()}) })
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1", "y1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"y1"}}
			}},
		{"9: an inbound item started ahead of an older waiting item", theQueuesOrder, stageEvent,
			9, "item y1, admitted by the walk, does not rank ahead of item x1, still waiting",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.inboundOpening("y1", alertOf("i1", "e1", item.Firing, time.Second))
				w.do("y1 starts, and x1 waits", func() { w.appendDirect("y1", item.Body{Started: startedBody()}) })
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1", "y1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1"}}
			}},
		{"9: a flap started ahead of an older waiting item, its item_linked in its opening's append", theQueuesOrder, stageEvent,
			9, "item y1, admitted by the walk, does not rank ahead of item x1, still waiting",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.inboundOpening("y1", alertOf("i1", "e1", item.Firing, time.Second), item.Body{Linked: &item.Linked{
					Left: "incident-y1", With: "z1", By: "source:alerting", Request: item.Request{Key: keyHash("flap:e1"), Digest: keyHash("flap:e1")},
				}})
				w.do("y1 starts, and x1 waits", func() { w.appendDirect("y1", item.Body{Started: startedBody()}) })
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1", "y1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1"}}
			}},
		{"9 keeps: an inbound item that took an event while queued, then admitted by the walk in its order", theQueuesOrder, stageEvent, 0, "",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.inboundOpening("x1", alertOf("i1", "e1", item.Firing, 0))
				w.appendDirect("x1", triggeredBody(alertOf("i1", "e2", item.Resolved, time.Second), true, ""))
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.do("x1 starts, and y1 waits", func() { w.appendDirect("x1", item.Body{Started: startedBody()}) })
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1", "y1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"y1"}}
			}},
		{"9: an inbound item that took an event while queued, then started ahead of an older waiting item", theQueuesOrder, stageEvent,
			9, "item y1, admitted by the walk, does not rank ahead of item x1, still waiting",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.inboundOpening("y1", alertOf("i1", "e1", item.Firing, time.Second))
				w.appendDirect("y1", triggeredBody(alertOf("i1", "e2", item.Resolved, 2*time.Second), true, ""))
				w.do("y1 starts, and x1 waits", func() { w.appendDirect("y1", item.Body{Started: startedBody()}) })
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1", "y1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1"}}
			}},
		{"9: a person's item linked while queued, then started ahead of an older waiting item", theQueuesOrder, stageEvent,
			9, "item y1, admitted by the walk, does not rank ahead of item x1, still waiting",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.appendDirect("x1", item.Body{Opened: openedBody()})
				w.do("time passes 1s", func() { w.passTime(time.Second) })
				w.appendDirect("y1", item.Body{Opened: openedBody()})
				w.appendDirect("y1", item.Body{Linked: &item.Linked{Left: "incident-y1", With: "z1", By: "admin", Reason: &duplicate}})
				w.do("y1 starts, and x1 waits", func() { w.appendDirect("y1", item.Body{Started: startedBody()}) })
				w.before = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1", "y1"}}
				w.after = app.Lifecycle{Limit: worldLimit, Investigating: worldLimit, Waiting: []item.ID{"x1"}}
			}},
		{"11: an applied row of an instance older than the one before it", theInboundRows, stageEvent,
			11, "alerting's event e2, i1 resolved at 2026-11-02T09:00:05Z applied, resolved_unmatched, after alerting's event e1, i1 resolved at 2026-11-02T09:00:10Z",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.putRow(inboundRow(alertOf("i1", "e1", item.Resolved, 10*time.Second), app.InboundResolvedUnmatched, true))
				w.putRow(inboundRow(alertOf("i1", "e2", item.Resolved, 5*time.Second), app.InboundResolvedUnmatched, true))
			}},
		{"11: a firing applied at the time of the resolution before it", theInboundRows, stageEvent,
			11, "alerting's event e2, i1 firing at 2026-11-02T09:00:00Z applied, appended, after alerting's event e1, i1 resolved at 2026-11-02T09:00:00Z",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.putRow(inboundRow(alertOf("i1", "e1", item.Resolved, 0), app.InboundResolvedUnmatched, true))
				w.noteRow(inboundRow(alertOf("i1", "e2", item.Firing, 0), app.InboundAppended, true), "x1")
			}},
		{"11 and 12 keep: a resolution at its firing's time, a late firing, an equal-time firing, and a flap later", theInboundRows, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.took(a, alertOf("i1", "e1", item.Firing, 0)).Item
				assert.Equal(t, app.IngestAppended, w.took(a, alertOf("i1", "e2", item.Resolved, 0)).Outcome)
				assert.Equal(t, app.IngestLate, w.took(a, alertOf("i1", "e3", item.Firing, -time.Minute)).Outcome)
				assert.Equal(t, app.IngestLate, w.took(a, alertOf("i1", "e4", item.Firing, 0)).Outcome)
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("close-1")) })
				assert.Equal(t, app.IngestOpened, w.took(a, alertOf("i1", "e5", item.Firing, time.Second)).Outcome)
			}},
		{"12: an item opened by an event not later than its instance's last status", theInboundRows, stageEvent,
			12, "item x1 opened by alerting's event e2, i1 firing at 2026-11-02T09:00:05Z, not later than its instance's last status, alerting's event e1, i1 resolved at 2026-11-02T09:00:10Z",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.putRow(inboundRow(alertOf("i1", "e1", item.Resolved, 10*time.Second), app.InboundResolvedUnmatched, true))
				w.inboundOpening("x1", alertOf("i1", "e2", item.Firing, 5*time.Second))
			}},
		{"13: an item closed by itself while its trigger fires", theItemsTriggers, stageEvent,
			13, "item x1 closed by itself, record 4, while its instance alerting/i1 fires",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.inboundOpening("x1", alertOf("i1", "e1", item.Firing, 0), notStaffed())
				w.appendDirect("x1", item.Body{Closed: &item.Closed{Auto: true, Due: worldStart}})
			}},
		{"13: an item closed by itself while one of its two instances fires", theItemsTriggers, stageEvent,
			13, "item x1 closed by itself, record 6, while its instance alerting/i2 fires",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.inboundOpening("x1", alertOf("i1", "e1", item.Firing, 0), notStaffed())
				w.appendDirect("x1", triggeredBody(alertOf("i2", "e2", item.Firing, time.Second), true, "declined:no_run"),
					triggeredBody(alertOf("i1", "e3", item.Resolved, 2*time.Second), true, ""))
				w.appendDirect("x1", item.Body{Closed: &item.Closed{Auto: true, Due: worldStart}})
			}},
		{"13 keeps: an inbound item closed by itself once its trigger resolved", theItemsTriggers, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.unstaffedInbound(a)
				w.took(a, alertOf("i1", "e2", item.Resolved, time.Second))
				w.do("time passes 2m", func() { w.passTime(2 * time.Minute) })
				require.Equal(t, item.StatusClosed, w.stateOf(id).Status())
			}},
		{"14: a resolved_unmatched row while an item not closed holds its instance firing", theInboundRows, stageEvent,
			14, "alerting's event e2, i1 resolved at 2026-11-02T09:00:01Z was put resolved_unmatched while item x1, queued, holds its instance firing",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.inboundOpening("x1", alertOf("i1", "e1", item.Firing, 0))
				w.putRow(inboundRow(alertOf("i1", "e2", item.Resolved, time.Second), app.InboundResolvedUnmatched, true))
			}},
		{"14 keeps: a resolution that matches no item, the item that holds its instance firing closed", theInboundRows, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.took(a, alertOf("i1", "e1", item.Firing, 0)).Item
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("close-1")) })
				assert.Equal(t, app.IngestResolvedUnmatched, w.took(a, alertOf("i1", "e2", item.Resolved, time.Second)).Outcome)
			}},
		{"14 keeps: a resolution put before the item that holds its instance opened", theInboundRows, stageEvent, 0, "",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.putRow(inboundRow(alertOf("i1", "e1", item.Resolved, 0), app.InboundResolvedUnmatched, true))
				w.inboundOpening("x1", alertOf("i1", "e2", item.Firing, time.Second))
			}},
		{"15: a flap opened with no link into the closed item its dedup key last took", theInboundRows, stageEvent,
			15, "a flap of item " + string(app.ItemID(inboundTenant, "inbound:alerting:e1")) + ", closed, holds no item_linked into its incident",
			func(_ *testing.T, w *world, a, _ *instance) {
				id := w.took(a, alertOf("i1", "e1", item.Firing, 0)).Item
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("close-1")) })
				w.inboundOpening("y1", alertOf("i1", "e2", item.Firing, time.Second))
			}},
		{"15: a flap linked into another item than the one its dedup key last took", theInboundRows, stageEvent,
			15, "a flap of item " + string(app.ItemID(inboundTenant, "inbound:alerting:e1")) + ", closed, holds no item_linked",
			func(_ *testing.T, w *world, a, _ *instance) {
				id := w.took(a, alertOf("i1", "e1", item.Firing, 0)).Item
				w.do("a1 closes it", func() { w.act(a, actClose, id, closing("close-1")) })
				w.inboundOpening("y1", alertOf("i1", "e2", item.Firing, time.Second), item.Body{Linked: &item.Linked{
					Left: "incident-y1", With: "z1", By: "source:alerting", Request: item.Request{Key: keyHash("flap:e2"), Digest: keyHash("flap:e2")},
				}})
			}},
		{"15 keeps: a flap through the coordinator, linked into the closed item's incident", theInboundRows, stageEvent, 0, "",
			func(_ *testing.T, w *world, a, _ *instance) { w.flapped(a) }},
		{"15 keeps: an opening whose dedup key's last item is not closed is not judged a flap", theInboundRows, stageEvent, 0, "",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.inboundOpening("x1", alertOf("i1", "e1", item.Firing, 0))
				w.inboundOpening("y1", alertOf("i2", "e2", item.Firing, time.Second))
			}},
		{"16: an applied firing on an item awaiting its close with no run, which tries no resume", theItemsTriggers, stageEvent,
			16, "item x1's applied firing e2 of alerting/i2, record 4, came while the item awaits its close and tried no firing resume",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.inboundOpening("x1", alertOf("i1", "e1", item.Firing, 0), notStaffed())
				w.appendDirect("x1", triggeredBody(alertOf("i2", "e2", item.Firing, time.Second), true, ""))
			}},
		{"16: a re-notification after a declined resume, which tries no resume", theItemsTriggers, stageEvent,
			16, "item x1's applied firing e3 of alerting/i2, record 5, came while the item awaits its close and tried no firing resume",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.inboundOpening("x1", alertOf("i1", "e1", item.Firing, 0), notStaffed())
				w.appendDirect("x1", triggeredBody(alertOf("i2", "e2", item.Firing, time.Second), true, "declined:no_run"))
				w.appendDirect("x1", triggeredBody(alertOf("i2", "e3", item.Firing, 2*time.Second), true, ""))
			}},
		{"16: a firing resume of an item with no run declined otherwise than no_run", theItemsTriggers, stageEvent,
			16, "item x1's applied firing e2, record 4, came while the item has no run, and records declined:kill_switch_on, not declined:no_run",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.inboundOpening("x1", alertOf("i1", "e1", item.Firing, 0), notStaffed())
				w.appendDirect("x1", triggeredBody(alertOf("i2", "e2", item.Firing, time.Second), true, "declined:kill_switch_on"))
			}},
		{"16: a firing resume resumed with no run_extended in its append", theItemsTriggers, stageEvent,
			16, "records resumed, and its append holds no run_extended of cause firing",
			func(_ *testing.T, w *world, a, _ *instance) {
				id := w.took(a, alertOf("i1", "e1", item.Firing, 0)).Item
				w.awaitsItsClose(id)
				w.appendDirect(id, triggeredBody(inboundEvent("alerting", "i2", "e2", item.Firing, w.dbNow()), true, item.ResumeResumed))
			}},
		{"16 keeps: firing resumes declined no_run, and a re-notification after the decline declined again", theItemsTriggers, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.unstaffedInbound(a)
				assert.Equal(t, app.IngestAppended, w.took(a, alertOf("i2", "e2", item.Firing, time.Second)).Outcome)
				assert.Equal(t, "declined:no_run", w.lastTriggered(id).Resume)
				assert.Equal(t, app.IngestAppended, w.took(a, alertOf("i2", "e3", item.Firing, 2*time.Second)).Outcome)
				assert.Equal(t, "declined:no_run", w.lastTriggered(id).Resume)
			}},
		{"16 keeps: a firing resume declined for a run whose budget is spent", theItemsTriggers, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.took(a, alertOf("i1", "e1", item.Firing, 0)).Item
				w.awaitsItsClose(id)
				w.took(a, inboundEvent("alerting", "i1", "e2", item.Resolved, w.dbNow()))
				w.took(a, inboundEvent("alerting", "i1", "e3", item.Firing, w.dbNow().Add(time.Second)))
				assert.True(t, strings.HasPrefix(w.lastTriggered(id).Resume, item.ResumeDeclined), w.lastTriggered(id).Resume)
			}},
		{"16 keeps: a firing resume resumed, its run_extended of cause firing in its append", theItemsTriggers, stageEvent, 0, "",
			func(_ *testing.T, w *world, a, _ *instance) { w.firingResumed(a) }},
		{"17: two items not closed hold one dedup key, which no reopen made so", theItemsTriggers, stageEvent,
			17, "items x1 and y1, neither closed, both hold alerting's dedup key fp-i1, and no reopen made it so",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.inboundOpening("x1", alertOf("i1", "e1", item.Firing, 0))
				w.inboundOpening("y1", alertOf("i1", "e2", item.Firing, time.Second))
			}},
		{"17: an item that took an open item's dedup key after the other's reopen", theItemsTriggers, stageEvent,
			17, "both hold alerting's dedup key fp-i1, and no reopen made it so",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.inboundOpening("x1", alertOf("i1", "e1", item.Firing, 0))
				w.appendDirect("x1", item.Body{Closed: &item.Closed{By: "admin"}},
					item.Body{Reopened: &item.Reopened{By: "admin", AwaitingClose: item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseClosed}}})
				w.inboundOpening("y1", alertOf("i1", "e2", item.Firing, time.Second))
			}},
		{"17 keeps: a closed item reopened by a person, its flap holding its dedup key", theItemsTriggers, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				closed, _ := w.flapped(a)
				w.do("a1 reopens the closed item", func() { w.act(a, actReopen, closed, reopening("reopen-1")) })
				require.NotEqual(t, item.StatusClosed, w.stateOf(closed).Status())
			}},
		{"18: one event on item_triggered records of two items", theItemsTriggers, stageEvent,
			18, "alerting's event e1 is on two item_triggered records",
			func(_ *testing.T, w *world, _, _ *instance) {
				w.inboundOpening("x1", alertOf("i1", "e1", item.Firing, 0))
				w.appendDirect("y1", item.Body{Opened: openedBody()}, triggeredBody(alertOf("i1", "e1", item.Firing, 0), false, ""))
			}},
		{"18: one event's two rows", theInboundRows, stageEvent,
			18, "alerting's event e1 has two rows",
			func(_ *testing.T, w *world, _, _ *instance) {
				row := inboundRow(alertOf("i1", "e1", item.Resolved, 0), app.InboundResolvedUnmatched, true)
				w.putRow(row)
				w.noteRow(row, "")
			}},
		{"18 keeps: an event sent again, whose answer was lost, a repeat", lifecycleInvariants, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				e := alertOf("i1", "e1", item.Resolved, 0)
				w.armFault(faultInboundLost)
				var err error
				w.do("a1 takes e1, its answer lost", func() { _, err = w.ingestOn(a, e) })
				require.ErrorIs(t, err, errAnswerLost)
				assert.Equal(t, app.IngestRepeat, w.took(a, e).Outcome)
			}},
		{"19: an un-faulted Ingest that answers an error", anUnfaultedIngestAnswers, stageEvent,
			19, "a1's Ingest of alerting's event e3, i1 firing at +5s answered",
			func(t *testing.T, w *world, a, _ *instance) {
				id := w.took(a, alertOf("i1", "e1", item.Firing, 0)).Item
				w.appendDirect(id, triggeredBody(alertOf("i1", "e2", item.Resolved, 10*time.Second), true, ""))
				var err error
				w.do("a1 takes e3", func() { _, err = w.ingestOn(a, alertOf("i1", "e3", item.Firing, 5*time.Second)) })
				require.Error(t, err)
			}},
		{"19 keeps: an Ingest whose row's put fails, its fault armed", anUnfaultedIngestAnswers, stageEvent, 0, "",
			func(t *testing.T, w *world, a, _ *instance) {
				w.armFault(faultInboundFail)
				var err error
				w.do("a1 takes e1, its put failed", func() { _, err = w.ingestOn(a, alertOf("i1", "e1", item.Resolved, 0)) })
				require.ErrorIs(t, err, errBlinked)
			}},
		{"19 keeps: an Ingest sent to an instance that does not lead", anUnfaultedIngestAnswers, stageEvent, 0, "",
			func(t *testing.T, w *world, _, b *instance) {
				var err error
				w.do("b1 takes e1", func() { _, err = w.ingestOn(b, alertOf("i1", "e1", item.Firing, 0)) })
				require.ErrorIs(t, err, app.ErrNotHeld)
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
