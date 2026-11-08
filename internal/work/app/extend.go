package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// extendKind is a person's extension, as its request's digest names the act.
const extendKind = "extend"

// Extend extends the run of item id by the amount asked, for a person
// (Decision 17), and answers what it did, or, previewed, what it would do.
// The amount is resolved against the run's creation's budget (see
// item.ExtensionAmount): a dimension left out gets the rounds asked's share,
// and an amount that resolves to nothing, or is above the creation's budget
// in any dimension, is refused ErrInvalid. A reason is optional.
//
// On a run that runs, Extend takes one append, run_extended, with the
// amount, who extended the run, why, and the act's request, which keeps the
// key's hash, never the key (Decision 13). The run's budget rises at once in
// the records: rounds and wall clock take effect at the round's end, which
// reads them; tokens at once, as the item, advanced once the append commits,
// moves its open round's sessions again with Run.SessionBudget, its deadline
// unchanged (R5, R54; see raise). The answer is changed, with the budget
// before and after the extension and what the run used (R57), and gives no
// items investigating: no limit applies, as the item stays investigating.
// An extension of a stopped run, its item awaiting its close, which resumes
// it (Decision 16), is refused ErrInvalid by this build.
//
// The checks come in this order, after the act's form, the item, its actor
// as May decides it (D9), a stranger refused CodeNotItemResponder, and its
// reason (D23): the act's key, as findKey looks for it, a repeat answering as
// the extension did and advancing the item (see actFound); then a closed
// item, CodeClosed; an item with no run, CodeNotStarted (R62); the kill
// switch on, CodeKillSwitchOn, and, but for a preview, the view unsettled,
// ErrUnsettled (R52); a run that is stopping, ErrStopping (R69); then the
// amount. A preview takes no key and skips the lookup: it runs the same
// checks on the same state, under the same lock, and answers the
// extension's consequences, with Changed false, recording nothing and
// moving nothing (Decision 13).
//
// An extension whose append fails is checked as carryOn says: one that
// landed, its answer lost, answers as a repeat; one whose outcome is unknown
// changes nothing in memory, and its item's retry raises the sessions once
// it finds the record (Decision 17).
//
// Extend takes the coordinator's lock, and reads and writes under the
// coordinator's own context, which carries its holding's epoch, whatever ctx
// says. It answers ErrNotHeld when the coordinator is not held, or its
// context ended, before it reads anything, and when it no longer holds once
// the extension is done (see heldThrough); ErrInvalid; ErrSkipped; ErrNoItem;
// ErrKeyReused; ErrUnsettled; ErrStopping; a *Refused; or a failed read's
// error, or a failed append's.
func (c *Coordinator) Extend(_ context.Context, id item.ID, asked item.Amount, a *Act) (answer Outcome, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.holds() {
		return Outcome{}, ErrNotHeld
	}
	defer func() { answer, err = heldThrough(c, answer, err) }()
	if err := checkAct(a); err != nil {
		return Outcome{}, err
	}
	s, err := c.target(c.ctx, id)
	if err != nil {
		return Outcome{}, err
	}
	if _, err := May(a.Actor, ActExtend, s.Opened); err != nil {
		return Outcome{}, err
	}
	reason, err := c.checkReason(s, a.Reason)
	if err != nil {
		return Outcome{}, err
	}
	req := requestOf(extendKind, id, a, amountKeys(asked))
	if !a.Preview {
		switch found, err := c.findKey(s, req); {
		case err != nil:
			return Outcome{}, err
		case found:
			c.actFound(s)
			return extendedOutcome(s, req), nil
		}
	}
	if err := c.mayExtend(s, a.Preview); err != nil {
		return Outcome{}, err
	}
	amount, err := item.ExtensionAmount(s.Run.Created.Budget, asked)
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: %s", ErrInvalid, strings.TrimPrefix(err.Error(), item.ErrInvalid.Error()+": "))
	}
	if a.Preview {
		after := s.Run.Budget()
		after.Rounds += amount.Rounds
		after.WallClock += amount.WallClock
		after.Tokens += amount.Tokens
		return Outcome{Consequences: Consequences{Budget: &BudgetChange{Before: s.Run.Budget(), After: after, Used: usedBy(s.Run)}}}, nil
	}
	out, _, err := c.extendNow(s, &item.Extended{Amount: amount, By: a.Actor.Subject, Reason: reason, Request: req})
	return out, err
}

// mayExtend checks the state of s for an extension (Decision 17), in R60's
// order: a closed item; an item with no run (R62); the kill switch on, and,
// but for a preview, the view unsettled (R52); a run that is stopping (R69);
// and, until the resume is built, a stopped run, whose extension resumes it.
func (c *Coordinator) mayExtend(s *item.State, preview bool) error {
	switch {
	case s.Status() == item.StatusClosed:
		return &Refused{Code: CodeClosed}
	case s.Run == nil:
		return &Refused{Code: CodeNotStarted}
	case c.killSwitch.On:
		return &Refused{Code: CodeKillSwitchOn}
	case c.unsettled && !preview:
		return ErrUnsettled
	case s.Run.Concludes(), s.Run.Stopped != nil && s.Status() != item.StatusAwaitingClose:
		return ErrStopping
	case s.Run.Stopped != nil:
		return fmt.Errorf("%w: the run stopped, and this build does not resume a run", ErrInvalid)
	}
	return nil
}

// extendNow records e, run_extended for s's run, its reason redacted and its
// kill switch row set for a resume (Decision 16), and returns the act's
// outcome; repeat reports a found key. Once the append commits, the item
// advances, and its step raises the open round's sessions (see raise). An
// append that fails is checked as carryOn says: an extension that landed,
// its answer lost, answers as a repeat, its item advanced and loaded again
// for its answer; one whose outcome is unknown changes nothing in memory.
func (c *Coordinator) extendNow(s *item.State, e *item.Extended) (Outcome, bool, error) {
	after, err := c.record(c.ctx, s, rec{run: s.Run.ID, body: item.Body{Extended: e}})
	if err != nil {
		// carryOn answers no error only once it found the record: a repeat.
		if _, _, err = c.carryOn(c.ctx, s.ID, e.Request, err); err != nil {
			return Outcome{}, false, err
		}
		if now, err := c.load(c.ctx, s.ID); err == nil {
			return extendedOutcome(now, e.Request), true, nil
		}
		return Outcome{Changed: true}, true, nil
	}
	c.advance(c.ctx, s.ID)
	return Outcome{Changed: true, Consequences: Consequences{Budget: &BudgetChange{
		Before: s.Run.Budget(), After: after.Run.Budget(), Used: usedBy(after.Run),
	}}}, false, nil
}

// extendedOutcome is the answer of an extension found by its request, req,
// in s, a repeat (R14): changed, with the run's budget before and after the
// extension found, as it answered, and what the run used now. A run with no
// extension of req, as when s cannot show it, answers changed alone.
func extendedOutcome(s *item.State, req item.Request) Outcome {
	out := Outcome{Changed: true}
	if s.Run == nil {
		return out
	}
	before := s.Run.Created.Budget
	for i := range s.Run.Extensions {
		e := &s.Run.Extensions[i].Extended
		after := before
		after.Rounds += e.Amount.Rounds
		after.WallClock += e.Amount.WallClock
		after.Tokens += e.Amount.Tokens
		if e.Request.Key == req.Key {
			out.Consequences.Budget = &BudgetChange{Before: before, After: after, Used: usedBy(s.Run)}
			return out
		}
		before = after
	}
	return out
}

// usedBy is the budget the run used, as its latest ended round says: its
// rounds, its wall clock and its spend; nothing before its first round ends.
func usedBy(r *item.Run) item.Budget {
	for i := len(r.Rounds) - 1; i >= 0; i-- {
		if e := r.Rounds[i].Ended; e != nil {
			return item.Budget{Rounds: e.Rounds, WallClock: e.WallClock, Tokens: e.Spent}
		}
	}
	return item.Budget{}
}

// amountKeys are the parameters of an extension asked, as its request's
// digest takes them (R98; I1): each dimension the person named, zero
// included, as a decimal string, the wall clock in nanoseconds, so that 5m
// and 300s agree; a dimension left out is no key.
func amountKeys(asked item.Amount) map[string]string {
	keys := map[string]string{}
	if asked.Rounds != nil {
		keys["rounds"] = strconv.Itoa(*asked.Rounds)
	}
	if asked.WallClock != nil {
		keys["wallClock"] = strconv.FormatInt(int64(*asked.WallClock), 10)
	}
	if asked.Tokens != nil {
		keys["tokens"] = strconv.FormatInt(*asked.Tokens, 10)
	}
	return keys
}

// raise moves the open round's sessions of s's run again, its deadline
// unchanged, with Run.SessionBudget when it exceeds the budget r moved them
// with (R54): an extension of tokens since the round opened. Each agent of
// the round that this instance issued a session to is moved. It moves
// nothing for a round r does not hold, or once the coordinator is fenced.
func (c *Coordinator) raise(s *item.State, r *running) error {
	l := s.Run.Latest()
	if r == nil || !c.held || l == nil || l.Ended != nil || r.round != l.Opened.Round {
		return nil
	}
	budget := s.Run.SessionBudget(r.round)
	if budget <= r.budget {
		return nil
	}
	o := &l.Opened
	for _, agent := range o.Agents {
		if _, ok := r.tokens[agent]; !ok {
			continue
		}
		if err := c.p.Sessions.Move(s.Run.ID, agent, o.Round, c.p.Clock.Local(o.Deadline), budget); err != nil {
			return err
		}
	}
	r.budget = budget
	return nil
}
