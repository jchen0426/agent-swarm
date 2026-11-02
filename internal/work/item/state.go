package item

import (
	"fmt"
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
}

// Run is one of an item's runs, folded from its records.
type Run struct {
	ID      board.RunID
	Created RunCreated
	Rounds  []Round
	Stopped *RunStopped
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
		s.ID, s.Incident, s.Opened, s.OpenedAt = r.Item, r.Incident, b.Opened, r.At
	case KindStarted:
		if s.Started != nil {
			return invalid("the item started already")
		}
		s.Started = b.Started
	case KindRunCreated:
		return s.create(r.Run, b.RunCreated)
	case KindAwaitingClose:
		return s.awaitClose(b.AwaitingClose, r.At)
	case KindPromoted:
		return s.promote(b.Promoted)
	case KindClosed:
		return s.closeItem(b.Closed, r.At)
	case KindReopened:
		s.Closed, s.Closing, s.AwaitingSince = nil, &b.Reopened.AwaitingClose, r.At
	default:
		if s.Run == nil || r.Run != s.Run.ID {
			return invalid("record %s names a run the item does not have", r.ID)
		}
		return s.Run.apply(b)
	}
	return nil
}

// create adds the item's run, once, after the item started, under an id that
// holds no colon: a record's scope in the audit chain is its run, or
// incident:<id> for the records of no run.
func (s *State) create(run board.RunID, rc *RunCreated) error {
	if s.Started == nil || s.Run != nil || run == "" {
		return invalid("a run is created once, after the item started")
	}
	if strings.Contains(string(run), ":") {
		return invalid("run id %q holds a colon", run)
	}
	s.Run = &Run{ID: run, Created: *rc}
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
// 6), or by itself, only while it awaits its close (Decision 8).
func (s *State) closeItem(c *Closed, at time.Time) error {
	switch {
	case s.Running():
		return invalid("an item closes once no run of it runs")
	case c.Auto && s.Status() != StatusAwaitingClose:
		return invalid("an item closes by itself only while it awaits its close")
	}
	s.Closed, s.ClosedAt = c, at
	return nil
}

// admitted holds the kinds of record each state admits (Decision 2), which
// each kind's own rules narrow: a promotion while the item has no run, a
// close while no run of it runs, and a run's records in their order.
var admitted = map[Status][]Kind{
	StatusQueued:        {KindPromoted, KindStarted, KindAwaitingClose, KindClosed},
	StatusInvestigating: {KindRunCreated, KindRoundOpened, KindRoundEnded, KindRunStopped, KindAwaitingClose, KindClosed},
	StatusAwaitingClose: {KindClosed, KindPromoted},
	StatusClosed:        {KindReopened},
}

// admits checks what every record needs: one kind, the item's opening first,
// once, and a kind the item's state admits.
func (s *State) admits(kind Kind) error {
	switch {
	case kind == "":
		return invalid("a record holds exactly one body")
	case (s.Opened == nil) != (kind == KindOpened):
		return invalid("an item opens with its first record, once")
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
		if !mayStop(latest, st.Reason) {
			return invalid("a run stops as its last round decided, not staffed before its first, or killed or closed between rounds")
		}
		if st.Reason == StopConverged && st.Conclusion != latest.Opened.WrapUp {
			return invalid("a converged run concludes with its wrap-up round's hypothesis")
		}
		r.Stopped = st
	}
	return nil
}

// mayStop reports whether a run whose latest round is latest, nil before its
// first, may stop for reason: as that round decided; not staffed before the
// first round; or, killed or closed, before the first round or after a round
// that decided no stop (Decision 3). A round open stops no run: a halt ends
// it first, stopped, deciding the halt's stop.
func mayStop(latest *Round, reason StopReason) bool {
	switch {
	case latest == nil:
		return reason == StopNotStaffed || halted(reason)
	case latest.Ended == nil:
		return false
	case latest.Ended.Decision.Next != NextStop:
		return halted(reason)
	}
	return latest.Ended.Decision == Decision{Next: NextStop, Stop: reason}
}

// halted reports whether a run stops for a halt's reason, killed or closed,
// rather than as a round decided (Decision 3).
func halted(reason StopReason) bool {
	return reason == StopKilled || reason == StopClosed
}

func (r *Run) open(latest *Round, o *RoundOpened) error {
	if o.Round != len(r.Rounds)+1 {
		return invalid("round %d cannot follow round %d", o.Round, len(r.Rounds))
	}
	want := Decision{Next: NextRound}
	if latest != nil {
		if latest.Ended == nil {
			return invalid("round %d is still open", latest.Opened.Round)
		}
		want = latest.Ended.Decision
	}
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
