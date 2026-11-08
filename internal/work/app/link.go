package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// linkKind and unlinkKind are a person's link and unlink, as their requests'
// digests name the acts.
const (
	linkKind   = "link"
	unlinkKind = "unlink"
)

// Link moves item id into the incident that item with has now, for a person
// (Decision 18), and answers what it did, or, previewed, what it would do. It
// takes one append on id, item_linked, whose column holds with's incident, so
// that the record is chained under its destination, and whose body names the
// incident id left, the item with, who linked it, why, and the act's request,
// which keeps the key's hash, never the key (Decision 13). with's records do
// not change, and a link has no effect on runs or boards. The answer is
// changed, with the incident id leaves, the one it joins and that one's
// items after the link, sorted (Contract C16).
//
// The checks come in this order (Link's order, M6; D23): the act's form; id
// (see target); id's actor as May decides it, a stranger refused
// CodeNotItemResponder; id and with the same item, CodeSelf; with, ErrNoItem
// for an id with no records, ErrSkipped for one a later build holds; with's
// actor as May decides it (A4), so that an actor joins only items they handle
// both of, or an admin; a reason, which a link requires, a code alone taken,
// CodeReasonRequired without one; the act's key, as findKey looks for it,
// with as one of its request's keys (R98), a repeat answering as the link did
// and a key that named another act of id, another link among them, refused
// ErrKeyReused; either item closed longer ago than Options.ClosedRulings, by
// the database's time, CodeRulingsWindowPassed (R64); then id already in
// with's incident, which changes nothing and answers changed false (R60).
// The items of with's incident are read under the lock (see incidentItems):
// a read that fails is returned, and nothing is recorded. A preview takes no
// key and skips the lookup: it runs the same checks on the same state, under
// the same lock, and answers the link's consequences with changed false,
// recording nothing.
//
// A link whose append fails is checked as carryOn says: one that landed, its
// answer lost, answers as the link did; one whose outcome is unknown answers
// the append's error, and a retry under its key finds it.
//
// Link takes the coordinator's lock, and reads and writes under the
// coordinator's own context, which carries its holding's epoch, whatever ctx
// says. It answers ErrNotHeld when the coordinator is not held, or its
// context ended, before it reads anything, and when it no longer holds once
// the link is done (see heldThrough); ErrInvalid; ErrSkipped; ErrNoItem;
// ErrKeyReused; a *Refused; or a failed read's error, or a failed append's.
func (c *Coordinator) Link(_ context.Context, id, with item.ID, a *Act) (answer Outcome, err error) {
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
	if _, err := May(a.Actor, ActLink, s.Opened); err != nil {
		return Outcome{}, err
	}
	o, err := c.linkedTo(id, with, a)
	if err != nil {
		return Outcome{}, err
	}
	reason, err := c.presetReason(s, a.Reason)
	if err != nil {
		return Outcome{}, err
	}
	req := requestOf(linkKind, id, a, map[string]string{"with": string(with)})
	if !a.Preview {
		switch r, err := c.findRecord(s, req); {
		case err != nil:
			return Outcome{}, err
		case r != nil:
			return c.movedOutcome(r)
		}
	}
	if err := c.mayRuleOn(s); err != nil {
		return Outcome{}, err
	}
	if err := c.mayRuleOn(o); err != nil {
		return Outcome{}, err
	}
	items, err := c.incidentItems(o.Incident)
	if err != nil {
		return Outcome{}, err
	}
	if s.Incident == o.Incident {
		return Outcome{Consequences: Consequences{Incident: &IncidentChange{From: s.Incident, To: s.Incident, Items: items}}}, nil
	}
	change := &IncidentChange{From: s.Incident, To: o.Incident, Items: joined(items, id)}
	if a.Preview {
		return Outcome{Consequences: Consequences{Incident: change}}, nil
	}
	linked := &item.Linked{Left: s.Incident, With: with, By: a.Actor.Subject, Reason: reason, Request: req}
	return c.moveNow(s, &rec{incident: o.Incident, body: item.Body{Linked: linked}}, req, change)
}

// Unlink gives item id an incident of its own, a new id from IDs (Decision
// 18; R61), for a person, and answers what it did, or, previewed, what it
// would do. It takes one append on id, item_unlinked, whose column holds the
// new incident and whose body names the incident id left, who unlinked it,
// why, and the act's request. The answer is changed, with the incident id
// leaves, the new one and its items, id alone (Contract C16).
//
// The checks come in this order (D10; D23): the act's form; id (see target);
// id's actor as May decides it, a stranger refused CodeNotItemResponder, and
// id's alone (A4), since an unlink takes an item out of an incident, never
// into another's; a reason, which an unlink requires, CodeReasonRequired
// without one; the act's key, a repeat answering as the unlink did and a key
// that named another act of id refused ErrKeyReused; id closed longer ago
// than Options.ClosedRulings, CodeRulingsWindowPassed (R64); then id alone in
// its incident, which changes nothing and answers changed false (R60). Alone
// is read from the incident's items under the lock (see incidentItems): a
// read that fails is returned, and nothing is recorded. A preview takes no key
// and skips the lookup: it runs the same checks and answers the unlink's
// consequences with changed false, recording nothing; its new incident, named
// only once recorded, is empty.
//
// An unlink whose append fails is checked as carryOn says, as a link's is.
// Unlink takes the coordinator's lock, and answers as Link does.
func (c *Coordinator) Unlink(_ context.Context, id item.ID, a *Act) (answer Outcome, err error) {
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
	if _, err := May(a.Actor, ActUnlink, s.Opened); err != nil {
		return Outcome{}, err
	}
	reason, err := c.presetReason(s, a.Reason)
	if err != nil {
		return Outcome{}, err
	}
	req := requestOf(unlinkKind, id, a, nil)
	if !a.Preview {
		switch r, err := c.findRecord(s, req); {
		case err != nil:
			return Outcome{}, err
		case r != nil:
			return c.movedOutcome(r)
		}
	}
	if err := c.mayRuleOn(s); err != nil {
		return Outcome{}, err
	}
	items, err := c.incidentItems(s.Incident)
	if err != nil {
		return Outcome{}, err
	}
	if !slices.ContainsFunc(items, func(other item.ID) bool { return other != id }) {
		return Outcome{Consequences: Consequences{Incident: &IncidentChange{From: s.Incident, To: s.Incident, Items: items}}}, nil
	}
	if a.Preview {
		return Outcome{Consequences: Consequences{Incident: &IncidentChange{From: s.Incident, Items: []item.ID{id}}}}, nil
	}
	to := item.IncidentID(c.p.IDs.NewID())
	change := &IncidentChange{From: s.Incident, To: to, Items: []item.ID{id}}
	unlinked := &item.Unlinked{Left: s.Incident, By: a.Actor.Subject, Reason: reason, Request: req}
	return c.moveNow(s, &rec{incident: to, body: item.Body{Unlinked: unlinked}}, req, change)
}

// linkedTo loads with, the item a link of id joins the incident of, in
// Link's order (M6): the same item as id, CodeSelf; with as target loads it,
// ErrNoItem or ErrSkipped; and its actor as May decides it on with (A4).
func (c *Coordinator) linkedTo(id, with item.ID, a *Act) (*item.State, error) {
	if with == id {
		return nil, &Refused{Code: CodeSelf}
	}
	o, err := c.target(c.ctx, with)
	if err != nil {
		return nil, err
	}
	if _, err := May(a.Actor, ActLink, o.Opened); err != nil {
		return nil, err
	}
	return o, nil
}

// presetReason checks the reason of a link or an unlink, which takes one
// (spec Section 4.2): none is refused CodeReasonRequired; one is checked as
// checkReason checks it, a code alone taken.
func (c *Coordinator) presetReason(s *item.State, r *board.Reason) (*board.Reason, error) {
	if r == nil {
		return nil, &Refused{Code: CodeReasonRequired}
	}
	return c.checkReason(s, r)
}

// mayRuleOn refuses a link or an unlink of s, closed longer ago than the
// window of late rulings, Options.ClosedRulings, as the database's time
// (Clock.Now) judges it, CodeRulingsWindowPassed (R60, R64). An item not
// closed passes.
func (c *Coordinator) mayRuleOn(s *item.State) error {
	if s.Status() == item.StatusClosed && c.p.Clock.Now().After(s.ClosedAt.Add(c.opts.ClosedRulings)) {
		return &Refused{Code: CodeRulingsWindowPassed}
	}
	return nil
}

// incidentItems reads the items of incident, sorted (see Incidents).
func (c *Coordinator) incidentItems(incident item.IncidentID) ([]item.ID, error) {
	items, err := c.p.Incidents.IncidentItems(c.ctx, incident)
	if err != nil {
		return nil, fmt.Errorf("read the items of incident %s: %w", incident, err)
	}
	return items, nil
}

// joined is items with id among them, sorted.
func joined(items []item.ID, id item.ID) []item.ID {
	out := slices.Clone(items)
	if !slices.Contains(out, id) {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// moveNow records r, s's link or unlink, under req, and answers it changed,
// with change, the incident it leaves and joins and that one's items. An
// append that fails is checked as carryOn says: a move that landed, its
// answer lost, answers as it did; one that did not, or whose outcome is
// unknown, answers the append's error. A move has no effect to carry on.
func (c *Coordinator) moveNow(s *item.State, r *rec, req item.Request, change *IncidentChange) (Outcome, error) {
	if _, err := c.record(c.ctx, s, *r); err != nil {
		// carryOn answers no error only once it found the record: a repeat.
		if _, _, err := c.carryOn(c.ctx, s.ID, req, err); err != nil {
			return Outcome{}, err
		}
	}
	return Outcome{Changed: true, Consequences: Consequences{Incident: change}}, nil
}

// movedOutcome is the answer of a link or an unlink found by its key, r, a
// repeat (R14): changed, with the incident it left and the one it joined, as
// r keeps them, and that one's items now. A read of them that fails is
// returned, and a retry under the key answers again.
func (c *Coordinator) movedOutcome(r *item.Record) (Outcome, error) {
	var left item.IncidentID
	switch {
	case r.Body.Linked != nil:
		left = r.Body.Linked.Left
	case r.Body.Unlinked != nil:
		left = r.Body.Unlinked.Left
	}
	items, err := c.incidentItems(r.Incident)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Changed: true, Consequences: Consequences{Incident: &IncidentChange{From: left, To: r.Incident, Items: items}}}, nil
}
