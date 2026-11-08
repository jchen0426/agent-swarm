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
//
// On a stopped run whose item awaits its close, the extension resumes the run
// (Decision 16): its run_extended also records the latest kill switch row the
// coordinator knew, 0 when no row was ever written, which the run's owed kill
// reads from then on. The fold lifts the run's stop and the item's
// awaiting_close, and the item, advanced, opens its next round on its board,
// a normal round, after the board's resume entry (see open). The answer says
// the run resumes, with the items investigating and the limit once the
// advance is done (R57). A resume that would not open a round is refused
// (see mayResume), and a resume whose outcome is unknown counts its item
// among the items investigating until its next load (R53; see
// countAsStarting).
//
// The checks come in this order, after the act's form, the item, its actor
// as May decides it (D9), a stranger refused CodeNotItemResponder, and its
// reason (D23): the act's key, as findKey looks for it, a repeat answering as
// the extension did and advancing the item (see actFound); then a closed
// item, CodeClosed; an item with no run, CodeNotStarted (R62); the kill
// switch on, CodeKillSwitchOn, and, but for a preview, the view unsettled,
// ErrUnsettled (R52); a run that is stopping, ErrStopping (R69); then the
// amount; then, for a resume, the checks of mayResume (R60). A preview takes
// no key and skips the lookup: it runs the same checks on the same state,
// under the same lock, and answers the extension's consequences, with
// Changed false, recording nothing and moving nothing (Decision 13).
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
			return c.extendedOutcome(s, req), nil
		}
	}
	if err := c.mayExtend(s, a.Preview); err != nil {
		return Outcome{}, err
	}
	amount, err := item.ExtensionAmount(s.Run.Created.Budget, asked)
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: %s", ErrInvalid, strings.TrimPrefix(err.Error(), item.ErrInvalid.Error()+": "))
	}
	after := s.Run.Budget()
	after.Rounds += amount.Rounds
	after.WallClock += amount.WallClock
	after.Tokens += amount.Tokens
	if err := c.mayResume(s, after); err != nil {
		return Outcome{}, err
	}
	if a.Preview {
		return c.wouldExtend(s, after), nil
	}
	e := &item.Extended{Amount: amount, By: a.Actor.Subject, Reason: reason, Request: req}
	if s.Run.Stopped != nil {
		e.KillSwitchSeen = c.killSwitch.Seq
	}
	out, _, err := c.extendNow(s, e)
	return out, err
}

// wouldExtend answers what an extension of s's run to the budget after,
// which the checks allow, would do (Decision 13), with Changed false: the
// budget before and after it, and what the run used; and, for a stopped run,
// that it resumes, with the items investigating once it counts among them,
// and the limit (R57).
func (c *Coordinator) wouldExtend(s *item.State, after item.Budget) Outcome {
	out := Outcome{Consequences: Consequences{Budget: &BudgetChange{Before: s.Run.Budget(), After: after, Used: usedBy(s.Run)}}}
	if s.Run.Stopped != nil {
		out.Consequences.Resumes = true
		out.Consequences.Investigating, out.Consequences.Limit = c.investigating()+1, c.opts.Limit
	}
	return out
}

// mayExtend checks the state of s for an extension (Decision 17), in R60's
// order: a closed item; an item with no run (R62); the kill switch on, and,
// but for a preview, the view unsettled (R52); and a run that is stopping
// (R69): its round decided a stop not recorded yet, or its stop is recorded
// and its finish has not ended. A stopped run whose item awaits its close
// passes, and its extension resumes it once mayResume allows it.
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
	}
	return nil
}

// mayResume refuses the resume of s's stopped run, its item awaiting its
// close, under the budget after the extension, when it would not open a
// round (Decision 16), and passes the extension of a run that runs. Its
// checks come in R60's order after mayExtend's: the day's spend at the daily
// cap, CodeDailyCapReached; no agent left to invite, CodeNoAgentsLeft; a
// budget the run's usage would still exhaust, so that its round would stop
// at once, CodeWouldStopAtOnce (R56); and one more item investigating past
// the limit and one, CodeLimitReached (see overLimit). A read of the day's
// spend that fails is returned.
func (c *Coordinator) mayResume(s *item.State, after item.Budget) error {
	if s.Run.Stopped == nil {
		return nil
	}
	day, err := c.p.Spend.SpentToday(c.ctx)
	switch {
	case err != nil:
		return fmt.Errorf("read the day's spend: %w", err)
	case day >= c.opts.DailyCap:
		return &Refused{Code: CodeDailyCapReached}
	case len(s.Run.Agents()) == 0:
		return &Refused{Code: CodeNoAgentsLeft}
	case !s.Run.WouldOpen(after):
		return &Refused{Code: CodeWouldStopAtOnce}
	case c.overLimit():
		return &Refused{Code: CodeLimitReached}
	}
	return nil
}

// extendNow records e, run_extended for s's run, its reason redacted and its
// kill switch row set for a resume (Decision 16), and returns the act's
// outcome; repeat reports a found key. Once the append commits, the item
// advances: a running run's step raises the open round's sessions (see
// raise), and a resumed run's opens its next round on its board (see open).
// A resume's answer says so, with the items investigating and the limit once
// the advance is done (R57; see counted). An append that fails is checked as
// carryOn says: an extension that landed, its answer lost, answers as a
// repeat, its item advanced and loaded again for its answer; one whose
// outcome is unknown changes nothing in memory, but a resume's, whose item
// the account counts among the items investigating until its next load, as
// its resume may have landed (R53; see countAsStarting).
func (c *Coordinator) extendNow(s *item.State, e *item.Extended) (Outcome, bool, error) {
	resumes := s.Run.Stopped != nil
	after, err := c.record(c.ctx, s, rec{run: s.Run.ID, body: item.Body{Extended: e}})
	if err != nil {
		// carryOn answers no error only once it found the record: a repeat.
		var unknown bool
		if _, unknown, err = c.carryOn(c.ctx, s.ID, e.Request, err); unknown && resumes {
			c.countAsStarting(s.ID)
		}
		if err != nil {
			return Outcome{}, false, err
		}
		if now, err := c.load(c.ctx, s.ID); err == nil {
			return c.extendedOutcome(now, e.Request), true, nil
		}
		return Outcome{Changed: true, Consequences: Consequences{Resumes: resumes}}, true, nil
	}
	c.advance(c.ctx, s.ID)
	out := Outcome{Changed: true, Consequences: Consequences{Budget: &BudgetChange{
		Before: s.Run.Budget(), After: after.Run.Budget(), Used: usedBy(after.Run),
	}, Resumes: resumes}}
	if resumes {
		return c.counted(&out), false, nil
	}
	return out, false, nil
}

// extendedOutcome is the answer of an extension found by its request, req,
// in s, a repeat (R14): changed, with the run's budget before and after the
// extension found, as it answered, and what the run used now; a resume's
// says so, with the items investigating and the limit now (R57; see
// counted). A run with no extension of req, as when s cannot show it,
// answers changed alone.
func (c *Coordinator) extendedOutcome(s *item.State, req item.Request) Outcome {
	out := extendedBudget(s, req)
	if out.Consequences.Resumes {
		return c.counted(&out)
	}
	return out
}

// extendedBudget is extendedOutcome's answer before the items investigating:
// changed, with the budget of the extension of req found in s, and whether it
// resumed the run.
func extendedBudget(s *item.State, req item.Request) Outcome {
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
			out.Consequences.Resumes = s.Run.Extensions[i].Resume
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
