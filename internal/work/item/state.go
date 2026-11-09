package item

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/board"
)

// Status is an item's state (spec Section 4.1).
type Status string

// Item states (spec Section 4.1). An item is queued from its opening until it
// starts, and again once a promotion lifts its awaiting_close before it
// started; until Plan 7b queues items, it starts at once. It investigates from
// its start until it awaits its close, and is closed from a close until a
// reopen returns it to awaiting its close.
const (
	StatusQueued        Status = "queued"
	StatusInvestigating Status = "investigating"
	StatusAwaitingClose Status = "awaiting_close"
	StatusClosed        Status = "closed"
)

// State is a work item, folded from its records in commit order.
type State struct {
	ID       ID
	Incident IncidentID
	Opened   *Opened
	Started  *Started
	// Run is the item's primary run, once created. An item has no run until
	// then: while it is queued, once it started and before its run's
	// creation, and when it awaits its close not staffed without one.
	Run *Run
	// Closing is how the item awaits its close: set when it enters
	// awaiting_close, by its item_awaiting_close or a reopen, and cleared when
	// a promotion lifts it. A close leaves it as it was.
	Closing *AwaitingClose
	// Promotion is the item's latest promotion.
	Promotion *Promoted
	// Closed is the item's latest close, while no reopen follows it.
	Closed *Closed
	// OpenedAt, AwaitingSince and ClosedAt are when the item opened, when it
	// last entered awaiting_close, a reopen included, and when it last
	// closed, from the committed records' times: a record checked before its
	// commit has no time yet, and no rule of order reads one.
	OpenedAt, AwaitingSince, ClosedAt time.Time
	last                              int64
	// trigger is the item's trigger, as its item_triggered records hold it.
	trigger trigger
}

// trigger is what the fold keeps of an item's trigger (design D5): every
// dedup key, instance and correlation key the item took, applied or late
// (R111); each instance's last applied status; and the trigger's resolution.
type trigger struct {
	// awaiting is an inbound opening's trigger until the item_triggered that
	// must follow it (R126).
	awaiting                        *Trigger
	dedups, instances, correlations map[keyOf]bool
	last                            map[keyOf]applied
	// resolved says no instance fires, and resolvedAt and resolvedSeq are
	// the time and sequence of the record after which none did: the opening
	// until an instance first fires (R110, R127).
	resolved    bool
	resolvedAt  time.Time
	resolvedSeq int64
	// resume is the latest Resume of an applied firing since the run's
	// latest stop (re-review N1).
	resume string
	// missed says an applied firing that would have tried a firing resume
	// landed since the run's latest stop with none tried, as one that came
	// before its item awaited its close (fix round 1, M-1).
	missed bool
}

// keyOf is a key, an instance or a correlation key under its source.
type keyOf struct{ source, value string }

// applied is an instance's last applied status, and when it began.
type applied struct {
	at     time.Time
	status TriggerStatus
}

// Run is one of an item's runs, folded from its records.
type Run struct {
	ID      board.RunID
	Created RunCreated
	Rounds  []Round
	Stopped *RunStopped
	// Stops are the run's earlier stops, each lifted by a resume, in their
	// order (Decision 16).
	Stops []RunStopped
	// Extensions are the run's extensions, resumes included, in their
	// order, each where it came among the run's rounds (R67).
	Extensions []Extension
	// resumedAt is the number of rounds the run had at its latest resume.
	resumedAt int
	// startedSeq is the sequence of the run's run_created or its latest
	// resume, against which its trigger's resolution is judged (R127).
	startedSeq int64
}

// Extension is one of a run's extensions: its record, the number of rounds
// the run had when it landed, and whether it resumed the run (R67, the
// re-review's N4).
type Extension struct {
	Extended Extended
	At       int
	Resume   bool
}

// Round is one of a run's rounds: how it opened, and how it ended once it
// has.
type Round struct {
	Opened RoundOpened
	Ended  *RoundEnded
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}

// Fold folds an item's records, in commit order, into its state.
func Fold(records []Record) (*State, error) {
	s := &State{}
	for i := range records {
		if err := s.Apply(&records[i]); err != nil {
			return nil, fmt.Errorf("record %d: %w", records[i].Seq, err)
		}
	}
	return s, nil
}

// Apply adds one committed record to the item, after checking its version
// and that it follows the lifecycle's order.
func (s *State) Apply(r *Record) error {
	if r.Body.V != Version {
		return fmt.Errorf("%w: record %s is of version %d", ErrVersion, r.ID, r.Body.V)
	}
	if s.Opened != nil && (r.Item != s.ID || r.Seq <= s.last) {
		return invalid("record %s is not the item's next", r.ID)
	}
	if err := s.apply(r); err != nil {
		return err
	}
	s.last = r.Seq
	return nil
}

func (s *State) apply(r *Record) error {
	b := &r.Body
	kind := b.Kind()
	if err := s.admits(kind); err != nil {
		return err
	}
	switch kind {
	case KindOpened:
		return s.open(r)
	case KindTriggered:
		return s.triggered(r)
	case KindStarted:
		if s.Started != nil {
			return invalid("the item started already")
		}
		s.Started = b.Started
	case KindRunCreated:
		return s.create(r)
	case KindAwaitingClose:
		return s.awaitClose(b.AwaitingClose, r.At)
	case KindPromoted:
		return s.promote(b.Promoted)
	case KindClosed:
		return s.closeItem(b.Closed, r.At)
	case KindReopened:
		s.Closed, s.Closing, s.AwaitingSince = nil, &b.Reopened.AwaitingClose, r.At
	case KindExtended:
		return s.extend(r, b.Extended)
	case KindLinked, KindUnlinked:
		return s.move(r)
	default:
		return s.applyToRun(r)
	}
	return nil
}

// open opens the item. An inbound opening's trigger names its source, the
// opening's own, and its dedup key, and its item_triggered must follow it
// (R126). The trigger reads as resolved at the opening until an instance
// fires (R110).
func (s *State) open(r *Record) error {
	o := r.Body.Opened
	switch tr := o.Trigger; {
	case tr == nil:
	case tr.Source == "" || tr.DedupKey == "":
		return invalid("an opening's trigger names its source and its dedup key")
	case tr.Source != o.Source:
		return invalid("an opening's trigger is of the opening's own source")
	}
	s.ID, s.Incident, s.Opened, s.OpenedAt = r.Item, r.Incident, o, r.At
	s.trigger = trigger{awaiting: o.Trigger, resolved: true, resolvedAt: r.At, resolvedSeq: r.Seq}
	return nil
}

// applyToRun adds a record of the item's run, which it names. Its stop ends
// the firing resumes tried since the stop before (re-review N1).
func (s *State) applyToRun(r *Record) error {
	if s.Run == nil || r.Run != s.Run.ID {
		return invalid("record %s names a run the item does not have", r.ID)
	}
	if err := s.Run.apply(&r.Body); err != nil {
		return err
	}
	if r.Body.RunStopped != nil {
		s.trigger.resume, s.trigger.missed = "", false
	}
	return nil
}

// missesResume reports whether t, about to be folded, is an applied firing
// that would try a firing resume but tries none (fix round 1, M-1): its
// Resume is empty; the item's run is stopped, or the item awaits its close;
// and t's instance is not held by the item, or resolved on it. Such a firing
// lands between the run's stop and its item's awaiting_close, as when the
// finish failed, or from a stale account; the next re-notification then
// tries the resume (see FiringResumeMissed).
func (s *State) missesResume(t *Triggered) bool {
	if t.Resume != "" || !t.Applied || t.Status != Firing {
		return false
	}
	if (s.Run == nil || s.Run.Stopped == nil) && s.Status() != StatusAwaitingClose {
		return false
	}
	last, held := s.trigger.last[keyOf{t.Source, t.InstanceID}]
	return !held || last.status == Resolved
}

// declined is the form of a declined firing resume's code.
var declined = regexp.MustCompile(`^[a-z][a-z_]{0,31}$`)

// triggered adds an inbound event to the item (design D5), once checkTriggered
// admits it. Every event adds its keys and its instance to the item's (R111);
// an applied one sets its instance's status, and the trigger's resolution
// with it: the record after which no instance fires, cleared by a firing.
func (s *State) triggered(r *Record) error {
	t := r.Body.Triggered
	if err := s.checkTriggered(t); err != nil {
		return err
	}
	tr := &s.trigger
	missed := s.missesResume(t)
	tr.awaiting = nil
	tr.dedups = with(tr.dedups, keyOf{t.Source, t.DedupKey})
	tr.instances = with(tr.instances, keyOf{t.Source, t.InstanceID})
	if t.CorrelationKey != "" {
		tr.correlations = with(tr.correlations, keyOf{t.Source, t.CorrelationKey})
	}
	if t.Resume != "" {
		tr.resume, tr.missed = t.Resume, false
	}
	if missed {
		tr.missed = true
	}
	if !t.Applied {
		return nil
	}
	was := s.TriggerFiring()
	if tr.last == nil {
		tr.last = map[keyOf]applied{}
	}
	tr.last[keyOf{t.Source, t.InstanceID}] = applied{at: t.OccurredAt, status: t.Status}
	switch firing := s.TriggerFiring(); {
	case firing:
		tr.resolved, tr.resolvedAt, tr.resolvedSeq = false, time.Time{}, 0
	case was:
		tr.resolved, tr.resolvedAt, tr.resolvedSeq = true, r.At, r.Seq
	}
	return nil
}

// checkTriggered checks an inbound event: it names its source, its event, its
// instance, its dedup key and a known status; an inbound opening's second
// record is its trigger's applied firing (R126); an applied event is later
// than its instance's last applied status on the item, by design D7 step 3's
// rule (R125); and its Resume is as checkResume says.
func (s *State) checkTriggered(t *Triggered) error {
	last, seen := s.trigger.last[keyOf{t.Source, t.InstanceID}]
	switch {
	case t.Source == "" || t.EventID == "" || t.InstanceID == "" || t.DedupKey == "":
		return invalid("an item_triggered names its source, its event, its instance and its dedup key")
	case t.Status != Firing && t.Status != Resolved:
		return invalid("an item_triggered's status is firing or resolved, not %q", t.Status)
	case !s.opensWith(t):
		return invalid("an inbound opening is followed by its trigger's applied firing")
	case t.Applied && seen && !later(t, last):
		return invalid("an applied event of instance %s is later than its last applied status", t.InstanceID)
	}
	return s.checkResume(t)
}

// checkResume checks an inbound event's Resume: "", resumed, or declined and
// a code (design D7), and set only on an applied firing while the item awaits
// its close.
func (s *State) checkResume(t *Triggered) error {
	code, isDeclined := strings.CutPrefix(t.Resume, ResumeDeclined)
	switch {
	case t.Resume == "":
	case t.Resume != ResumeResumed && (!isDeclined || !declined.MatchString(code)):
		return invalid("an item_triggered's resume is resumed or declined with a code, not %q", t.Resume)
	case !t.Applied || t.Status != Firing || s.Status() != StatusAwaitingClose:
		return invalid("a firing resume is tried by an applied firing while the item awaits its close")
	}
	return nil
}

// opensWith reports whether t may follow the item's records as to its
// opening: anything may, but after an inbound opening, whose next record is
// its trigger's applied firing, of its source, dedup key and correlation key
// (R126).
func (s *State) opensWith(t *Triggered) bool {
	a := s.trigger.awaiting
	return a == nil || t.Applied && t.Status == Firing && t.Source == a.Source && t.DedupKey == a.DedupKey && t.CorrelationKey == a.CorrelationKey
}

// later reports whether t is later than its instance's last applied status
// (design D7 step 3): it began after it, or at the same time as a firing it
// resolves.
func later(t *Triggered, last applied) bool {
	if t.OccurredAt.Equal(last.at) {
		return t.Status == Resolved && last.status == Firing
	}
	return t.OccurredAt.After(last.at)
}

// with is set with k added, made when it is nil.
func with(set map[keyOf]bool, k keyOf) map[keyOf]bool {
	if set == nil {
		set = map[keyOf]bool{}
	}
	set[k] = true
	return set
}

// create adds the item's run, once, after the item started, under an id that
// holds no colon: a record's scope in the audit chain is its run, or
// incident:<id> for the records of no run.
func (s *State) create(r *Record) error {
	run, rc := r.Run, r.Body.RunCreated
	if s.Started == nil || s.Run != nil || run == "" {
		return invalid("a run is created once, after the item started")
	}
	if strings.Contains(string(run), ":") {
		return invalid("run id %q holds a colon", run)
	}
	s.Run = &Run{ID: run, Created: *rc, startedSeq: r.Seq}
	return nil
}

// awaitClose has the item await its close, from at, once its run stopped,
// with its run's reason as ItemReason gives it, or not staffed while it has
// no run: queued, or started without a run, as the kill switch leaves it
// (Decision 5).
func (s *State) awaitClose(c *AwaitingClose, at time.Time) error {
	if s.Running() {
		return invalid("an item awaits its close once its run stopped, or not staffed while it has no run")
	}
	want := StopNotStaffed
	if s.Run != nil {
		want = ItemReason(s.Run.Stopped.Reason)
	}
	if c.Reason != want {
		return invalid("the item awaits its close %s, not %s", want, c.Reason)
	}
	s.Closing, s.AwaitingSince = c, at
	return nil
}

// promote lifts the awaiting_close of an item with no run (Decision 4): one
// that never started is queued again, and one that started investigates, to
// get its run.
func (s *State) promote(p *Promoted) error {
	if s.Run != nil {
		return invalid("an item is promoted while it has no run")
	}
	s.Promotion, s.Closing = p, nil
	return nil
}

// closeItem has the item closed from at: by a person while no run of it runs,
// which a halt's stop before the close in the same append makes so (Decision
// 6), or by itself, only while it awaits its close (Decision 8) and its
// trigger does not fire (review M6).
func (s *State) closeItem(c *Closed, at time.Time) error {
	switch {
	case s.Running():
		return invalid("an item closes once no run of it runs")
	case c.Auto && s.Status() != StatusAwaitingClose:
		return invalid("an item closes by itself only while it awaits its close")
	case c.Auto && s.TriggerFiring():
		return invalid("an item closes by itself only while its trigger does not fire")
	}
	s.Closed, s.ClosedAt = c, at
	return nil
}

// extend extends the item's run (Decision 17): a run that runs, while the
// item investigates, with no kill switch row; or, a resume, a stopped run
// while the item awaits its close, with any row, 0 included (Decision 16; the
// plan review's I3). A resume is told by the run's state alone. Each
// dimension of the amount is at least zero, and one above it. A firing resume,
// Cause firing, is a platform act (design D5, D7): a resume of a stopped run
// while the item awaits its close, with an amount of zero, the run's remaining
// budget.
func (s *State) extend(r *Record, e *Extended) error {
	a := e.Amount
	switch {
	case s.Run == nil || r.Run != s.Run.ID:
		return invalid("record %s names a run the item does not have", r.ID)
	case e.Cause != "":
		return s.resumeFiring(r, e)
	case a.Rounds < 0 || a.WallClock < 0 || a.Tokens < 0 || a == (Budget{}):
		return invalid("an extension adds at least zero of each dimension, and more of one")
	case s.Run.Stopped == nil && e.KillSwitchSeen != 0:
		return invalid("an extension of a run that runs records no kill switch row")
	case s.Run.Stopped == nil:
		s.Run.Extensions = append(s.Run.Extensions, Extension{Extended: *e, At: len(s.Run.Rounds)})
		return nil
	case s.Status() != StatusAwaitingClose:
		return invalid("a stopped run resumes once its item awaits its close")
	}
	s.Run.resume(e, r.Seq)
	s.Closing = nil
	return nil
}

// resumeFiring resumes the item's run for its trigger's firing (design D5,
// D7): the extension's cause is firing, its amount zero, the run stopped, and
// the item awaiting its close.
func (s *State) resumeFiring(r *Record, e *Extended) error {
	switch {
	case e.Cause != ExtendFiring:
		return invalid("an extension's cause is a person's or a firing, not %q", e.Cause)
	case e.Amount != (Budget{}):
		return invalid("a firing resume resumes the run with its remaining budget, an amount of zero")
	case s.Run.Stopped == nil || s.Status() != StatusAwaitingClose:
		return invalid("a firing resume resumes a stopped run while its item awaits its close")
	}
	s.Run.resume(e, r.Seq)
	s.Closing = nil
	return nil
}

// move gives the item the incident of r's column, a link's or an unlink's
// (Decision 18; R59), once it checks that the record is of the item, not of
// a run, so that it is chained under its incident; that a link names the
// item whose incident it joins; that it left the incident the item is in; and
// that it moves the item into another.
func (s *State) move(r *Record) error {
	var left IncidentID
	if l := r.Body.Linked; l != nil {
		if l.With == "" {
			return invalid("a link names the item whose incident it joins")
		}
		left = l.Left
	} else {
		left = r.Body.Unlinked.Left
	}
	switch {
	case r.Run != "":
		return invalid("record %s moves the item, and names no run", r.ID)
	case left != s.Incident:
		return invalid("record %s leaves incident %s, and the item is in %s", r.ID, left, s.Incident)
	case r.Incident == "" || r.Incident == left:
		return invalid("record %s moves the item into another incident", r.ID)
	}
	s.Incident = r.Incident
	return nil
}

// resume lifts the run's stop, which moves to its history, and keeps the
// extension that resumed it where it came (Decision 16, R67).
func (r *Run) resume(e *Extended, seq int64) {
	r.Stops = append(r.Stops, *r.Stopped)
	r.Stopped = nil
	r.resumedAt, r.startedSeq = len(r.Rounds), seq
	r.Extensions = append(r.Extensions, Extension{Extended: *e, At: len(r.Rounds), Resume: true})
}

// admitted holds the kinds of record each state admits (Decision 2), which
// each kind's own rules narrow: a promotion while the item has no run, a
// close while no run of it runs, an extension of the item's run, a resume
// only while the item awaits its close, a run's records in their order, and
// a link or an unlink in every state, from the item's incident into another
// (Decision 18); and an inbound event in every state but closed (design D5).
var admitted = map[Status][]Kind{
	StatusQueued:        {KindPromoted, KindStarted, KindAwaitingClose, KindClosed, KindLinked, KindUnlinked, KindTriggered},
	StatusInvestigating: {KindRunCreated, KindRoundOpened, KindRoundEnded, KindRunStopped, KindAwaitingClose, KindClosed, KindExtended, KindLinked, KindUnlinked, KindTriggered},
	StatusAwaitingClose: {KindClosed, KindPromoted, KindExtended, KindLinked, KindUnlinked, KindTriggered},
	StatusClosed:        {KindReopened, KindLinked, KindUnlinked},
}

// admits checks what every record needs: one kind, the item's opening first,
// once, an inbound opening's trigger second (R126), and a kind the item's
// state admits.
func (s *State) admits(kind Kind) error {
	switch {
	case kind == "":
		return invalid("a record holds exactly one body")
	case (s.Opened == nil) != (kind == KindOpened):
		return invalid("an item opens with its first record, once")
	case s.trigger.awaiting != nil && kind != KindTriggered:
		return invalid("an inbound opening is followed by its trigger's applied firing")
	case s.Opened != nil && !slices.Contains(admitted[s.Status()], kind):
		return invalid("an item %s takes no %s", s.Status(), kind)
	}
	return nil
}

func (r *Run) apply(b *Body) error {
	if r.Stopped != nil {
		return invalid("run %s stopped", r.ID)
	}
	latest := r.Latest()
	switch {
	case b.RoundOpened != nil:
		return r.open(latest, b.RoundOpened)
	case b.RoundEnded != nil:
		re := b.RoundEnded
		switch {
		case latest == nil || latest.Ended != nil || re.Round != latest.Opened.Round:
			return invalid("a round ends once, after it opened")
		case re.EndedBy == EndedByStopped && (re.Decision.Next != NextStop || !halted(re.Decision.Stop)):
			return invalid("a round stopped decides to stop killed or closed")
		}
		latest.Ended = re
	default:
		st := b.RunStopped
		if !r.mayStop(st.Reason) {
			return invalid("a run stops as its last round decided, not staffed before its first, or killed or closed between rounds")
		}
		if st.Reason == StopConverged && st.Conclusion != latest.Opened.WrapUp {
			return invalid("a converged run concludes with its wrap-up round's hypothesis")
		}
		r.Stopped = st
	}
	return nil
}

// mayStop reports whether the run may stop for reason: as its latest round
// decided (see decided); not staffed before its first round; or, killed or
// closed, before its first round or after a round that decided no stop, as a
// resumed run's reads before its next round (Decision 3, R67). A round open
// stops no run: a halt ends it first, stopped, deciding the halt's stop.
func (r *Run) mayStop(reason StopReason) bool {
	latest := r.Latest()
	d := r.decided()
	switch {
	case latest == nil:
		return reason == StopNotStaffed || halted(reason)
	case latest.Ended == nil:
		return false
	case d.Next != NextStop:
		return halted(reason)
	}
	return d == Decision{Next: NextStop, Stop: reason}
}

// halted reports whether a run stops for a halt's reason, killed or closed,
// rather than as a round decided (Decision 3).
func halted(reason StopReason) bool {
	return reason == StopKilled || reason == StopClosed
}

// open adds the run's next round, numbered after its last, once the latest
// ended, as it decided (see decided): a resumed run's as a normal round.
func (r *Run) open(latest *Round, o *RoundOpened) error {
	if o.Round != len(r.Rounds)+1 {
		return invalid("round %d cannot follow round %d", o.Round, len(r.Rounds))
	}
	if latest != nil && latest.Ended == nil {
		return invalid("round %d is still open", latest.Opened.Round)
	}
	want := r.decided()
	if want.Next == NextStop || want.WrapUp != o.WrapUp {
		return invalid("round %d opens as round %d decided", o.Round, len(r.Rounds))
	}
	r.Rounds = append(r.Rounds, Round{Opened: *o})
	return nil
}

// Status returns the item's state: closed while no reopen follows its latest
// close; otherwise awaiting its close while it does, investigating once it
// started, and queued before that.
func (s *State) Status() Status {
	switch {
	case s.Closed != nil:
		return StatusClosed
	case s.Closing != nil:
		return StatusAwaitingClose
	case s.Started != nil:
		return StatusInvestigating
	}
	return StatusQueued
}

// Latest returns the run's latest round, or nil before its first.
func (r *Run) Latest() *Round {
	if len(r.Rounds) == 0 {
		return nil
	}
	return &r.Rounds[len(r.Rounds)-1]
}

// Agents returns the agents the run's next round invites: its latest round's,
// but those removed after it, or the run's invited agents before its first
// round.
func (r *Run) Agents() []string {
	l := r.Latest()
	if l == nil {
		return append(make([]string, 0, len(r.Created.Agents)), r.Created.Agents...)
	}
	agents := make([]string, 0, len(l.Opened.Agents))
	for _, a := range l.Opened.Agents {
		if l.Ended == nil || !slices.Contains(l.Ended.Removed, a) {
			agents = append(agents, a)
		}
	}
	return agents
}

// usage returns the budget the run used, as its latest ended round says.
func (r *Run) usage() (rounds int, wallClock time.Duration) {
	for i := len(r.Rounds) - 1; i >= 0; i-- {
		if e := r.Rounds[i].Ended; e != nil {
			return e.Rounds, e.WallClock
		}
	}
	return 0, 0
}

// wrapUps counts the run's wrap-up rounds, the open one ending as re says,
// but those that ended by an outage or that the instance ending them did not
// see: they use up no wrap-up. A wrap-up round no agent took part in, as when
// no agent ended its turn on the board, or that left a failure, concludes
// nothing, but uses one up, so that the wrap-ups run out and the budget ends a
// run whose runner, model vendor or platform stays out of reach; so does a
// wrap-up round stopped (see Halt).
func (r *Run) wrapUps(re *RoundEnded) int {
	n := 0
	for i := range r.Rounds {
		ended := r.Rounds[i].Ended
		if ended == nil {
			ended = re
		}
		if r.Rounds[i].Opened.WrapUp != "" && ended.EndedBy != EndedByOutage && !ended.Unseen {
			n++
		}
	}
	return n
}

// wrapUpRounds counts the wrap-up rounds the run opened, however they ended.
func (r *Run) wrapUpRounds() int {
	n := 0
	for i := range r.Rounds {
		if r.Rounds[i].Opened.WrapUp != "" {
			n++
		}
	}
	return n
}

// crashes counts an agent's crashed turns in a row in the run's rounds before
// round i, rounds that ended by an outage, and turns that never started,
// aside.
func (r *Run) crashes(agent string, i int) int {
	n := 0
	for i--; i >= 0; i-- {
		e := r.Rounds[i].Ended
		if e == nil || e.EndedBy == EndedByOutage || e.Turns[agent] == TurnUnstarted {
			continue
		}
		if e.Turns[agent] != TurnCrashed {
			break
		}
		n++
	}
	return n
}
