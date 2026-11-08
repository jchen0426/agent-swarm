package item

import (
	"fmt"
	"time"
)

// Budget returns the run's budget: its creation's plus every extension's,
// resumes included (Decision 17).
func (r *Run) Budget() Budget {
	b := r.Created.Budget
	for i := range r.Extensions {
		a := &r.Extensions[i].Extended.Amount
		b.Rounds += a.Rounds
		b.WallClock += a.WallClock
		b.Tokens += a.Tokens
	}
	return b
}

// SessionBudget returns the tokens the sessions of the run's round may spend
// (R5): the round's recorded budget plus the tokens extended since it opened.
// An extension that landed before the round opened is in its recorded budget
// already. It returns 0 for a round the run has not opened.
func (r *Run) SessionBudget(round int) int64 {
	if round < 1 || round > len(r.Rounds) {
		return 0
	}
	b := r.Rounds[round-1].Opened.Budget
	for i := range r.Extensions {
		if r.Extensions[i].At >= round {
			b += r.Extensions[i].Extended.Amount.Tokens
		}
	}
	return b
}

// KillSwitchSeen is the latest kill switch row the coordinator knew for the
// run (Decision 16): the largest of its creation's and every resume's. A
// later on-row is a kill the run owes.
func (r *Run) KillSwitchSeen() int64 {
	seen := r.Created.KillSwitchSeen
	for i := range r.Extensions {
		if e := &r.Extensions[i]; e.Resume {
			seen = max(seen, e.Extended.KillSwitchSeen)
		}
	}
	return seen
}

// Resumed reports whether the run was resumed and has opened no round since
// (R67): it has no stop, it stopped before, and its rounds are those it had
// at its latest resume. It holds for a run stopped between rounds, or before
// its first round, as for one whose round decided its stop.
func (r *Run) Resumed() bool {
	return r.Stopped == nil && len(r.Stops) > 0 && len(r.Rounds) == r.resumedAt
}

// decided returns what the run's latest round decided, which its next record
// follows: the latest round's decision once it ended; a normal round before
// the first round, while a round is open, and while the run is resumed, when
// the decision before the resume no longer holds (R67). Run.open, Run.Open,
// mayStop, Halt and Stop read the latest decision through it.
func (r *Run) decided() Decision {
	l := r.Latest()
	if r.Resumed() || l == nil || l.Ended == nil {
		return Decision{Next: NextRound}
	}
	return l.Ended.Decision
}

// Concludes reports whether the run's latest round decided its stop, which
// is not recorded yet (R74): the run has not stopped, and it is not resumed.
func (r *Run) Concludes() bool {
	return r.Stopped == nil && r.decided().Next == NextStop
}

// WouldOpen reports whether the run, under the budget after an extension,
// would open a round that its budget does not stop at once (R56): its latest
// ended round's usage, under after, is not exhausted by exhausted's rule. A
// run with no ended round, as one killed before its first, would.
func (r *Run) WouldOpen(after Budget) bool {
	for i := len(r.Rounds) - 1; i >= 0; i-- {
		if e := r.Rounds[i].Ended; e != nil {
			return !r.exhaustedUnder(after, e)
		}
	}
	return true
}

// LatestResume returns the amount the run's latest resume brought, which the
// board's resume entry posts (R58), and false when the run was never resumed.
func (r *Run) LatestResume() (Budget, bool) {
	for i := len(r.Extensions) - 1; i >= 0; i-- {
		if r.Extensions[i].Resume {
			return r.Extensions[i].Extended.Amount, true
		}
	}
	return Budget{}, false
}

// ExtensionAmount resolves the amount asked of an extension of a run created
// with the budget created (Decision 17, R55): a dimension named is taken as
// named, zero included (R70); one left out gets the rounds asked's share of
// created, its rounds' part of it by integer division, and nothing when no
// round is asked. The amount is invalid when a dimension asked is negative,
// when it resolves to nothing, or when it is above created in any dimension.
func ExtensionAmount(created Budget, asked Amount) (Budget, error) {
	var out Budget
	if asked.Rounds != nil {
		out.Rounds = *asked.Rounds
	}
	switch {
	case out.Rounds < 0 || asked.WallClock != nil && *asked.WallClock < 0 || asked.Tokens != nil && *asked.Tokens < 0:
		return Budget{}, fmt.Errorf("%w: an extension asks at least zero of each dimension", ErrInvalid)
	case out.Rounds > created.Rounds:
		return Budget{}, fmt.Errorf("%w: an extension of %d rounds is above the run's %d", ErrInvalid, out.Rounds, created.Rounds)
	}
	share := func(whole int64) int64 {
		if created.Rounds == 0 {
			return 0
		}
		return whole / int64(created.Rounds) * int64(out.Rounds)
	}
	out.WallClock = time.Duration(share(int64(created.WallClock)))
	if asked.WallClock != nil {
		out.WallClock = *asked.WallClock
	}
	out.Tokens = share(created.Tokens)
	if asked.Tokens != nil {
		out.Tokens = *asked.Tokens
	}
	switch {
	case out == (Budget{}):
		return Budget{}, fmt.Errorf("%w: an extension adds more of one dimension", ErrInvalid)
	case out.WallClock > created.WallClock || out.Tokens > created.Tokens:
		return Budget{}, fmt.Errorf("%w: an extension is above the run's budget", ErrInvalid)
	}
	return out, nil
}
