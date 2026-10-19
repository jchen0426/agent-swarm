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

// Item states. An item is queued from its opening until it starts; until
// Plan 7b queues items, it starts at once.
const (
	StatusQueued        Status = "queued"
	StatusInvestigating Status = "investigating"
	StatusAwaitingClose Status = "awaiting_close"
)

// State is a work item, folded from its records in commit order.
type State struct {
	ID       ID
	Incident IncidentID
	Opened   *Opened
	Started  *Started
	// Run is the item's primary run, once created.
	Run *Run
	// Closing is set once the item awaits its close.
	Closing *AwaitingClose
	last    int64
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
		s.ID, s.Incident, s.Opened = r.Item, r.Incident, b.Opened
	case KindStarted:
		if s.Started != nil {
			return invalid("the item started already")
		}
		s.Started = b.Started
	case KindRunCreated:
		return s.create(r.Run, b.RunCreated)
	case KindAwaitingClose:
		return s.close(b.AwaitingClose)
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

// close has the item await its close once its run stopped, with its run's
// reason as ItemReason gives it, or not staffed when it has no run.
func (s *State) close(c *AwaitingClose) error {
	if (s.Run == nil) != (s.Started == nil) || s.Run != nil && s.Run.Stopped == nil {
		return invalid("an item awaits its close once its run stopped, or when it is not staffed")
	}
	want := StopNotStaffed
	if s.Run != nil {
		want = ItemReason(s.Run.Stopped.Reason)
	}
	if c.Reason != want {
		return invalid("the item awaits its close %s, not %s", want, c.Reason)
	}
	s.Closing = c
	return nil
}

// admits checks what every record needs: one kind, an item that does not
// await its close, and the item's opening first, once.
func (s *State) admits(kind Kind) error {
	switch {
	case kind == "":
		return invalid("a record holds exactly one body")
	case s.Closing != nil:
		return invalid("the item awaits its close")
	case (s.Opened == nil) != (kind == KindOpened):
		return invalid("an item opens with its first record, once")
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
		if latest == nil || latest.Ended != nil || b.RoundEnded.Round != latest.Opened.Round {
			return invalid("a round ends once, after it opened")
		}
		latest.Ended = b.RoundEnded
	default:
		st := b.RunStopped
		decided := latest != nil && latest.Ended != nil && latest.Ended.Decision == Decision{Next: NextStop, Stop: st.Reason}
		if !decided && (latest != nil || st.Reason != StopNotStaffed) {
			return invalid("a run stops as its last round decided, or not staffed before its first")
		}
		if st.Reason == StopConverged && st.Conclusion != latest.Opened.WrapUp {
			return invalid("a converged run concludes with its wrap-up round's hypothesis")
		}
		r.Stopped = st
	}
	return nil
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

// Status returns the item's state.
func (s *State) Status() Status {
	switch {
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
// but those no agent took part in, as the coordinator saw them: they revoked
// nothing.
func (r *Run) wrapUps(re *RoundEnded) int {
	n := 0
	for i := range r.Rounds {
		ended := r.Rounds[i].Ended
		if ended == nil {
			ended = re
		}
		if r.Rounds[i].Opened.WrapUp != "" && ended.tookPart() {
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
