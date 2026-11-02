package app

import (
	"context"
	"fmt"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// promoteKind is a person's promotion, as its request's digest names the act.
const promoteKind = "promote"

// Promote promotes an item with no run for a person (spec Section 4.1;
// Decision 4), and answers what it did, or, previewed, what it would do. It
// applies to a queued item, which then starts whatever the queue, even past
// the limit by one; and to an item awaiting its close not staffed without a
// run, as one opened at the daily cap, or one that started and whose run was
// never created. Its promotion lifts the awaiting_close: an item that never
// started then starts, and one that started gets its run. It takes one
// append, item_promoted, with who promoted the item, why, and the act's
// request, which keeps the key's hash, never the key (Decision 13); then the
// item advances, and its step starts it, whatever the queue, or creates its
// run (see step). A promoted item counts among the items investigating from
// its promotion, as an admitted one does, so that a start that fails keeps
// its slot. A promotion is allowed as often as the item has no run (R6): a
// start that leaves the item not staffed again, as for its routing or its
// type, may be followed by another promotion once its configuration is
// fixed. The answer names the item among the starts once it started, or among
// the items not staffed once its start left it so (see promotedOutcome), and
// gives the items investigating, as the limit counts them, and the limit,
// once the promotion is done (see counted).
//
// Its checks come in R23's order. A closed item is refused with a *Refused,
// CodeClosed: reopen it first. A promotion whose effect holds already changes
// nothing, and nothing is recorded (R10), whatever the limit: of an item that
// has a run; of an item that investigates without one, whose step creates
// its run; and of a queued item admitted or promoted already, whose start is
// due and counted (see promotionHolds). Then a promotion while a kill switch
// write's outcome is unknown, the view off, answers ErrUnsettled and records
// nothing: the switch may be on, and the client sends it again (R37). Then a
// promotion while the kill switch is on is refused, CodeKillSwitchOn; one
// once the day's spend reached the daily cap, CodeDailyCapReached; and one
// that would take the items investigating past the limit and one,
// CodeLimitReached (see mayPromote). The promotion records the latest kill
// switch row the coordinator knew (Decision 5).
//
// Before it decides, Promote looks for the act's key among the item's records
// (see findKey): a promotion it finds is a repeat, which records nothing,
// answers changed, as the promotion did (R14), and advances the item, whose
// step decides from its state now (see actFound); a key that named another
// act of the item is refused with ErrKeyReused. A promotion whose append
// fails is checked as carryOn says: one that landed, its answer lost, answers
// as a repeat, its item advanced, and one that did not changed nothing. A
// preview takes no key and skips the lookup: it runs the same checks on the
// same state, under the same lock, and answers the promotion's consequences
// with Changed false, recording nothing and acting on nothing (Decision 13;
// see wouldPromote). The reason's text is checked and redacted before it is
// recorded (see checkReason).
//
// Promote takes the coordinator's lock, and reads and writes under the
// coordinator's own context, which carries its holding's epoch, whatever ctx
// says. It answers ErrNotHeld when the coordinator is not held, before it
// reads anything; ErrInvalid for an act of another form (see checkAct and
// checkReason); ErrSkipped for an item held by a later build; ErrNoItem for an
// id with no records; ErrKeyReused for a key that named another act of the
// item; ErrUnsettled while a kill switch write's outcome is unknown; a
// *Refused for a promotion the item's state or the lifecycle forbids; and a
// failed read's error, the day's spend's included, or a failed append's.
func (c *Coordinator) Promote(_ context.Context, id item.ID, a *Act) (Outcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.held {
		return Outcome{}, ErrNotHeld
	}
	if err := checkAct(a); err != nil {
		return Outcome{}, err
	}
	s, err := c.target(c.ctx, id)
	if err != nil {
		return Outcome{}, err
	}
	reason, err := c.checkReason(s, a.Reason)
	if err != nil {
		return Outcome{}, err
	}
	req := requestOf(promoteKind, id, a)
	if !a.Preview {
		switch found, err := c.findKey(s, req); {
		case err != nil:
			return Outcome{}, err
		case found:
			c.actFound(s)
			return c.counted(&Outcome{Changed: true}), nil
		}
	}
	switch {
	case s.Status() == item.StatusClosed:
		return Outcome{}, &Refused{Code: CodeClosed}
	case c.promotionHolds(s):
		return c.counted(&Outcome{}), nil
	}
	if err := c.mayPromote(); err != nil {
		return Outcome{}, err
	}
	if a.Preview {
		return c.wouldPromote(s), nil
	}
	out, err := c.promoteNow(s, &item.Promoted{By: a.Actor.Subject, KillSwitchSeen: c.killSwitch.Seq, Reason: reason, Request: req})
	if err != nil {
		return Outcome{}, err
	}
	return c.counted(&out), nil
}

// promotionHolds reports whether the effect of a promotion of s, an item not
// closed, holds already (Decision 4): s has a run; or it investigates, and
// its step creates its run; or it is queued, admitted or promoted already,
// and its start is due and counted.
func (c *Coordinator) promotionHolds(s *item.State) bool {
	switch s.Status() {
	case item.StatusInvestigating:
		return true
	case item.StatusQueued:
		return c.startDue(s)
	}
	return s.Run != nil
}

// mayPromote refuses a promotion that the lifecycle forbids now, in R23's
// order, whose checks of a closed item and of the promotion's effect come
// before: ErrUnsettled while a kill switch write's outcome is unknown and the
// view is off (R37); then, with a *Refused, CodeKillSwitchOn while the kill
// switch is on (Decision 5); then CodeDailyCapReached once the day's spend
// reached the daily cap; then CodeLimitReached when one more item
// investigating would take the items investigating past the limit and one
// (see overLimit). A read of the day's spend that fails is returned.
func (c *Coordinator) mayPromote() error {
	switch {
	case c.unsettled && !c.killSwitch.On:
		return ErrUnsettled
	case c.killSwitch.On:
		return &Refused{Code: CodeKillSwitchOn}
	}
	day, err := c.p.Spend.SpentToday(c.ctx)
	switch {
	case err != nil:
		return fmt.Errorf("read the day's spend: %w", err)
	case day >= c.opts.DailyCap:
		return &Refused{Code: CodeDailyCapReached}
	case c.overLimit():
		return &Refused{Code: CodeLimitReached}
	}
	return nil
}

// wouldPromote answers what a promotion of s, which the checks allow, would
// do (Decision 13), with Changed false: an item that never started is among
// the starts, and the item counts among the items investigating from then on.
// It predicts no start that fails, or that leaves the item not staffed, as for
// its routing or its type, which only the start itself checks: the promotion
// then answers otherwise (see promotedOutcome).
func (c *Coordinator) wouldPromote(s *item.State) Outcome {
	out := Outcome{Consequences: Consequences{Investigating: c.investigating() + 1, Limit: c.opts.Limit}}
	if s.Started == nil {
		out.Consequences.Starts = []item.ID{s.ID}
	}
	return out
}

// promoteNow records promoted, the promotion of s, then advances the item,
// whose step starts it or creates its run, and answers what the promotion did
// (see promotedOutcome). An append that fails is checked as carryOn says: a
// promotion that landed, its answer lost, answers as a repeat (R14).
func (c *Coordinator) promoteNow(s *item.State, promoted *item.Promoted) (Outcome, error) {
	if _, err := c.record(c.ctx, s, rec{body: item.Body{Promoted: promoted}}); err != nil {
		if _, err = c.carryOn(c.ctx, s.ID, promoted.Request, err); err != nil {
			return Outcome{}, err
		}
		return Outcome{Changed: true}, nil
	}
	c.advance(c.ctx, s.ID)
	return c.promotedOutcome(s), nil
}

// promotedOutcome is what the promotion of s did, once its advance is done, as
// the account of the items holds the item then (Contract C4): an item that had
// not started is among the starts once it investigates; and an item awaiting
// its close again, as one whose start left it not staffed, is among the items
// not staffed. An item whose start is still due, as when it failed, is in
// neither; nor is one that started before and gets its run, which no
// consequence names.
func (c *Coordinator) promotedOutcome(s *item.State) Outcome {
	out := Outcome{Changed: true}
	switch c.items[s.ID].status {
	case item.StatusInvestigating:
		if s.Started == nil {
			out.Consequences.Starts = []item.ID{s.ID}
		}
	case item.StatusAwaitingClose:
		out.Consequences.Unstaffed = []item.ID{s.ID}
	}
	return out
}
