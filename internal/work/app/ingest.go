package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/inbound/mapping"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// IngestOutcome is what Ingest made of an inbound event (design D7).
type IngestOutcome string

// Ingest's outcomes. All but repeat are the outcome of the event's row.
const (
	IngestRepeat            IngestOutcome = "repeat"
	IngestOpened            IngestOutcome = "opened"
	IngestAppended          IngestOutcome = "appended"
	IngestLate              IngestOutcome = "late"
	IngestResolvedUnmatched IngestOutcome = "resolved_unmatched"
	IngestUnroutable        IngestOutcome = "unroutable"
	IngestRefused           IngestOutcome = "refused"
)

// The reasons of an event refused by the coordinator (design D10; R131).
const (
	reasonItemUnreadable  = "item_unreadable"
	reasonKeyReused       = "key_reused"
	reasonSkipped         = "skipped"
	reasonTypeNotRunnable = "type_not_runnable"
	reasonRoutingRefused  = "routing_refused"
	reasonInvalid         = "invalid"
)

// IngestInput is one inbound event, as the evaluator made it.
type IngestInput struct {
	Event *mapping.Event
	// ID is the item an opening by this event takes:
	// ItemID(tenant, "inbound:"+source+":"+eventID) (R121).
	ID item.ID
}

// IngestResult is what came of an event: its outcome, the reason of an
// unroutable or refused one, and the item it touched, if any.
type IngestResult struct {
	Outcome IngestOutcome
	Reason  string
	Item    item.ID
}

// Ingest takes one event under the coordinator's lock, held through it. It
// answers ErrNotHeld when not held, or any store's error, an unknown
// outcome's included, with nothing more written: the caller answers 503 and
// the source retries (D9).
//
// In design D7's order: the items pending reload of the event's source are
// loaded first (R133); an event seen already is a repeat, and writes nothing;
// one the evaluator found unroutable or refused is put with its reason; one
// not later than its instance's last status at the source is late, appended
// to the oldest item that holds the instance, not applied, or put, and opens
// nothing (review C2); otherwise it is appended to the item it matches (see
// match), put as resolved_unmatched when it resolves and matches none, or, a
// firing that matches none, opens an item under the source's actor (see
// openFor). Every append to an item advances it, which arms or cancels its
// auto-close (design D5). A write that fails with ErrSeen is a repeat.
func (c *Coordinator) Ingest(ctx context.Context, in *IngestInput) (out IngestResult, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.holds() {
		return IngestResult{}, ErrNotHeld
	}
	defer func() { out, err = heldThrough(c, out, err) }()
	if in == nil || in.Event == nil {
		return IngestResult{}, fmt.Errorf("%w: no event", ErrInvalid)
	}
	// The event's store calls run apart from its request, bounded by the write
	// timeout and ended by the holding, as the drain's on is written: a
	// request that ends while a commit is in flight would answer its failure
	// while the commit lands, and the reads that follow would not see it
	// (fix round 1, I-1). The bound bounds the event's own reads and writes;
	// the item's advance after a commit runs under the coordinator's own
	// context, not under it.
	ctx, cancel := context.WithTimeout(holding.WithEpoch(context.WithoutCancel(ctx), c.term.Epoch), c.opts.WriteTimeout)
	defer cancel()
	defer context.AfterFunc(c.ctx, cancel)()
	out, err = c.ingest(ctx, in)
	if errors.Is(err, ErrSeen) {
		return IngestResult{Outcome: IngestRepeat}, nil
	}
	return out, err
}

// ingest is Ingest under the lock, with the holding checked and ctx bound to
// its epoch.
func (c *Coordinator) ingest(ctx context.Context, in *IngestInput) (IngestResult, error) {
	e := in.Event
	if err := c.reloadPending(ctx); err != nil {
		return IngestResult{}, err
	}
	switch seen, err := c.p.Inbound.Seen(ctx, e.Source, e.EventID); {
	case err != nil:
		return IngestResult{}, fmt.Errorf("read whether the event was seen: %w", err)
	case seen:
		return IngestResult{Outcome: IngestRepeat}, nil
	}
	switch {
	case e.Unroutable != "":
		return c.untaken(ctx, e, InboundUnroutable, e.Unroutable)
	case e.Refused != "":
		return c.untaken(ctx, e, InboundRefused, e.Refused)
	}
	late, err := c.late(ctx, e)
	switch {
	case err != nil:
		return IngestResult{}, err
	case late:
		if s := c.oldest(func(s *item.State) bool { return s.HoldsInstance(e.Source, e.InstanceID) }); s != nil {
			return c.appendTo(ctx, s, e, false)
		}
		return c.put(ctx, rowOf(e, InboundLate, "", false))
	}
	if s := c.match(e); s != nil {
		return c.appendTo(ctx, s, e, true)
	}
	if e.Status == item.Resolved {
		return c.put(ctx, rowOf(e, InboundResolvedUnmatched, "", true))
	}
	return c.firing(ctx, in)
}

// reloadPending loads every item pending reload (R133; fix round 2): an
// append to it, an act's, a step's or an earlier event's, may have landed,
// its answer lost, so the account's entry cannot say what it holds, or does
// not hold it at all, as an item reopened so. A load that succeeds clears the
// item's mark (see refresh). One that finds the item unreadable clears it
// too: the item leaves the account (see unreadable), which then holds no
// stale entry of it, and LastItem's load answers for it (see lastItem). Any
// other failure is returned: no event is taken until the item loads.
func (c *Coordinator) reloadPending(ctx context.Context) error {
	for _, id := range slices.Sorted(maps.Keys(c.pending)) {
		if _, err := c.load(ctx, id); err != nil {
			if !corrupt(err) && !ofLaterVersion(err) {
				return err
			}
			delete(c.pending, id)
		}
	}
	return nil
}

// late reports whether e is not later than its instance's last applied status
// (design D7 step 3): it began before it, or at the same time, unless it
// resolves a firing. The last status is the later of the source's, as the
// store's rows give it, and of each item of the account that holds the
// instance, as its fold gives it: rows gone from the store, as after the
// retention, leave an item's fold ahead of them, and an event between the two
// would be refused by the fold on every retry (R125).
func (c *Coordinator) late(ctx context.Context, e *mapping.Event) (bool, error) {
	status, at, ok, err := c.p.Inbound.LastStatus(ctx, e.Source, e.InstanceID)
	if err != nil {
		return false, fmt.Errorf("read the instance's last status: %w", err)
	}
	for _, en := range c.items {
		if en.state == nil {
			continue
		}
		if st, t, held := en.state.Instance(e.Source, e.InstanceID); held && (!ok || after(st, t, status, at)) {
			status, at, ok = st, t, true
		}
	}
	return ok && !after(e.Status, e.Clamped, status, at), nil
}

// after reports whether status at at is later than last at lastAt, by design
// D7 step 3's rule: it began after it, or at the same time as a firing it
// resolves.
func after(status item.TriggerStatus, at time.Time, last item.TriggerStatus, lastAt time.Time) bool {
	if at.Equal(lastAt) {
		return status == item.Resolved && last == item.Firing
	}
	return at.After(lastAt)
}

// match returns the item of the account that e matches, or nil (design D7
// step 4): the first rule that matches any item, of an item that holds e's
// instance, then its dedup key, then its correlation key, opened within the
// correlation's window, when e has one; ties go to the oldest. A person's
// item holds no key until an event is appended to it (review M4).
func (c *Coordinator) match(e *mapping.Event) *item.State {
	rules := []func(*item.State) bool{
		func(s *item.State) bool { return s.HoldsInstance(e.Source, e.InstanceID) },
		func(s *item.State) bool { return s.HoldsDedupKey(e.Source, e.DedupKey) },
	}
	if e.Within > 0 && e.CorrelationKey != "" {
		since := c.p.Clock.Now().Add(-e.Within)
		rules = append(rules, func(s *item.State) bool {
			return s.HoldsCorrelationKey(e.Source, e.CorrelationKey) && !s.OpenedAt.Before(since)
		})
	}
	for _, holds := range rules {
		if s := c.oldest(holds); s != nil {
			return s
		}
	}
	return nil
}

// oldest returns the state of the oldest item of the account for which holds
// is true, by its opening's time, then its id, or nil.
func (c *Coordinator) oldest(holds func(*item.State) bool) *item.State {
	var best item.ID
	for id, e := range c.items {
		if e.state == nil || !holds(e.state) {
			continue
		}
		if best == "" || cmp.Or(e.opened.Compare(c.items[best].opened), cmp.Compare(id, best)) < 0 {
			best = id
		}
	}
	if best == "" {
		return nil
	}
	return c.items[best].state
}

// appendTo appends e to the item of s as item_triggered, applied or late,
// with its row, in one append, then advances the item, which arms or cancels
// its auto-close (design D5). An applied firing that tries a firing resume
// (see triesResume) records what came of it in its item_triggered: resumed,
// followed in the same append by run_extended of zero amount, cause firing,
// by the source, with the latest kill switch row the coordinator knew and
// R120's request, so that the advance posts the board's resume entry and
// opens the next round (R119); or declined with its code (see
// mayFiringResume, R112), the item left awaiting its close. No resume waits
// for a slot (O1). A decision that cannot be made, as when the day's spend
// cannot be read, is returned, with nothing written. An append that fails is
// returned (see appendFailed).
func (c *Coordinator) appendTo(ctx context.Context, s *item.State, e *mapping.Event, applied bool) (IngestResult, error) {
	outcome := InboundAppended
	if !applied {
		outcome = InboundLate
	}
	t := triggeredOf(e, applied)
	recs := []rec{{body: item.Body{Triggered: t}}}
	resumes := false
	if applied && triesResume(s, e) {
		code, err := c.mayFiringResume(ctx, s)
		switch {
		case err != nil:
			return IngestResult{}, err
		case code != "":
			t.Resume = item.ResumeDeclined + code
		default:
			t.Resume, resumes = item.ResumeResumed, true
			h := hashOf([]byte("firing:" + e.EventID))
			recs = append(recs, rec{run: s.Run.ID, body: item.Body{Extended: &item.Extended{
				Cause: item.ExtendFiring, KillSwitchSeen: c.killSwitch.Seq, By: SourceSubjectPrefix + e.Source,
				Request: item.Request{Key: h, Digest: h},
			}}})
		}
	}
	after, err := c.recordInbound(ctx, s, rowOf(e, outcome, "", applied), recs...)
	if err != nil {
		return IngestResult{}, c.appendFailed(ctx, s.ID, err, resumes)
	}
	c.advance(c.ctx, s.ID)
	c.postTrigger(ctx, after, e, t)
	return IngestResult{Outcome: IngestOutcome(outcome), Item: s.ID}, nil
}

// postTrigger posts the update t made, with e, to the board of the item of
// after, its state at the commit, as platform evidence (design D11, as the
// author chose A6 (c)): agents see it as they see the opening's claim. It
// posts to a running, primary, visible run alone, and after the advance, which
// posts a firing resume's board entry and starts its round, since a stopped
// board takes nothing but rulings. The board repeats a post of the same Ref,
// so a retry posts once. A post that fails holds nothing: the event and its
// group stand, and the failure is a Warn line (re-review N2).
func (c *Coordinator) postTrigger(ctx context.Context, after *item.State, e *mapping.Event, t *item.Triggered) {
	s := after
	if en, ok := c.items[after.ID]; ok && en.state != nil {
		s = en.state
	}
	if !s.Running() || s.Run.Created.Class != item.ClassPrimary || s.Run.Created.Mode != item.ModeVisible || s.Started == nil {
		return
	}
	rev, err := c.p.Revisions.At(ctx, s.Started.Commit)
	if err == nil {
		_, resolved := s.ResolvedAt()
		in := &board.PlatformEvidenceInput{Claim: triggerUpdateClaim(rev, t, e.Title, resolved), Ref: "trigger:" + e.EventID, Source: board.Source(e.Source)}
		_, err = c.p.Boards.PostPlatformEvidence(ctx, s.Run.ID, in)
	}
	if err != nil {
		c.log.WarnContext(ctx, "a trigger update could not be posted to the item's board", "item", s.ID, "event", e.EventID, "err", err)
	}
}

// triesResume reports whether e, an applied event, tries a firing resume of
// s's item (design D7 step 5): e fires while the item awaits its close, its
// run absent, as for an item opened not staffed (review I3), or stopped; and
// e's instance is resolved on the item, or not held by it, or the latest
// firing resume tried since the run's stop was declined, so that a
// re-notification retries it (re-review N1), or a firing that would have
// tried one landed since the stop with none tried, as before the item
// awaited its close (fix round 1, M-1).
func triesResume(s *item.State, e *mapping.Event) bool {
	if e.Status != item.Firing || s.Status() != item.StatusAwaitingClose || s.Run != nil && s.Run.Stopped == nil {
		return false
	}
	status, _, held := s.Instance(e.Source, e.InstanceID)
	return !held || status == item.Resolved || s.FiringResumeDeclined() || s.FiringResumeMissed()
}

// appendFailed answers an append of an event to an item that failed. ErrSeen
// is a repeat. Otherwise the failure is logged as a step's, which fences on a
// lost lease; and an append whose outcome is unknown, one that the guard did
// not refuse and whose decide did not, may have landed with its answer lost:
// its append marked the item pending reload (see recordInbound), and its
// retry is armed, which loads it (R133). A firing resume's, resumes, also has the
// account count its item among the items investigating until its next load,
// as its resume may have landed (7b-2's carry; see countAsStarting). One that
// the lifecycle refused, as decided from a stale account, arms the item's
// retry, which loads it, so that the account catches up (fix round 1, M-3).
func (c *Coordinator) appendFailed(ctx context.Context, id item.ID, err error, resumes bool) error {
	if errors.Is(err, ErrSeen) {
		return err
	}
	c.failed(ctx, id, err)
	if c.held && mayHaveLanded(err) {
		c.retry(id)
		if resumes {
			c.countAsStarting(id)
		}
	} else if c.held && errors.Is(err, item.ErrInvalid) {
		c.retry(id)
	}
	return err
}

// mayHaveLanded reports whether an append that failed with err may have
// committed: any failure but the guard's refusal, a key held, and the
// lifecycle's refusal of what decide checked.
func mayHaveLanded(err error) bool {
	return !errors.Is(err, ErrLeaseLost) && !errors.Is(err, holding.ErrNoEpoch) && !errors.Is(err, ErrSeen) &&
		!errors.Is(err, item.ErrInvalid) && !ofLaterVersion(err)
}

// firing takes a firing that matched no item (design D7 steps 7 and 8, D8).
// The last item its dedup key took, when there is one and it is not closed,
// matches it, once loaded (see lastItem). Otherwise it opens an item (see
// openFor): when that last item is closed, a flap, linked into its incident.
func (c *Coordinator) firing(ctx context.Context, in *IngestInput) (IngestResult, error) {
	e := in.Event
	id, ok, err := c.p.Inbound.LastItem(ctx, e.Source, e.DedupKey)
	if err != nil {
		return IngestResult{}, fmt.Errorf("read the last item of the dedup key: %w", err)
	}
	var flap *Flap
	if ok {
		out, closed, done, err := c.lastItem(ctx, e, id)
		if done || err != nil {
			return out, err
		}
		if closed != nil {
			flap = &Flap{Item: closed.ID, Incident: closed.Incident, EventID: e.EventID}
		}
	}
	return c.openFor(ctx, in, flap)
}

// lastItem answers e, a firing that matched no item, by id, the last item its
// dedup key took, and reports whether it did. An item held by a later build,
// or whose records do not fold, refuses e, item_unreadable, so that no second
// item opens beside it (review M1). One that is not closed, outside the
// account, as an opening whose answer and lookup were lost, enters it as it
// loads, and e is matched again (R133). A closed one answers nothing but its
// state, as it loaded it, which the flap's opening links to (design D8): the
// flap's item and incident are this fold's, a closed item's, never another's
// (the controller's ruling on Task 5). One with no opening, or not closed and
// matching nothing, answers nothing: e opens an item, linked to none. A load
// that fails otherwise is returned.
func (c *Coordinator) lastItem(ctx context.Context, e *mapping.Event, id item.ID) (_ IngestResult, closed *item.State, done bool, _ error) {
	if c.isSkipped(id) {
		out, done, err := c.unreadableItem(ctx, e, id, ErrSkipped)
		return out, nil, done, err
	}
	s, err := c.load(ctx, id)
	switch {
	case corrupt(err) || ofLaterVersion(err):
		out, done, err := c.unreadableItem(ctx, e, id, err)
		return out, nil, done, err
	case err != nil:
		return IngestResult{}, nil, true, err
	case s.Opened == nil:
		return IngestResult{}, nil, false, nil
	case s.Status() == item.StatusClosed:
		return IngestResult{}, s, false, nil
	}
	if m := c.match(e); m != nil {
		out, err := c.appendTo(ctx, m, e, true)
		return out, nil, true, err
	}
	return IngestResult{}, nil, false, nil
}

// unreadableItem refuses e, item_unreadable, as the last item its dedup key
// took, id, cannot be read, as err says, and logs the item and the error at
// Warn, for the operator: the handler, which sees neither, logs the owner's
// line (design D10).
func (c *Coordinator) unreadableItem(ctx context.Context, e *mapping.Event, id item.ID, err error) (IngestResult, bool, error) {
	c.log.WarnContext(ctx, "the last item of an inbound event's dedup key cannot be read",
		"source", e.Source, "event", e.EventID, "item", id, "err", err)
	out, err := c.put(ctx, rowOf(e, InboundRefused, reasonItemUnreadable, false))
	return out, true, err
}

// openFor opens the item of e, a firing that matched no item (design D8), as
// Open does, under the source's actor, with in.ID, the event's trigger and
// its applied firing, the link of a flap, into the closed item's incident,
// when flap is set, and the opened row, in one append (R126); a flap's link
// needs no May, no reason and no window, whatever the closed item's close
// time (ruling 5). A refusal of the
// opening refuses the event with its reason (see refusal), and the next event
// is taken (review I4). ErrNoRevision and a store's error are returned.
func (c *Coordinator) openFor(ctx context.Context, in *IngestInput, flap *Flap) (IngestResult, error) {
	e := in.Event
	open := &OpenInput{
		ID: in.ID, Type: e.Type, Title: e.Title, Source: e.Source, Routing: e.Routing,
		Actor:     Actor{Subject: SourceSubjectPrefix + e.Source, Source: true},
		Trigger:   &item.Trigger{Source: e.Source, DedupKey: e.DedupKey, CorrelationKey: e.CorrelationKey},
		Triggered: triggeredOf(e, true),
		FlapOf:    flap,
		Row:       rowOf(e, InboundOpened, "", true),
	}
	open.Digest = OpeningDigest(open)
	id, err := c.openLocked(ctx, open)
	if err == nil {
		return IngestResult{Outcome: IngestOpened, Item: id}, nil
	}
	if corrupt(err) || ofLaterVersion(err) {
		// The item under the opening's id cannot be read (fix round 1, M-1).
		out, _, err := c.unreadableItem(ctx, e, in.ID, err)
		return out, err
	}
	if reason := refusal(err); reason != "" {
		return c.untaken(ctx, e, InboundRefused, reason)
	}
	return IngestResult{}, err
}

// refusal is the reason an event is refused for, when its opening was refused
// with err, or "" when err is no refusal (R131).
func refusal(err error) string {
	var refused *Refused
	switch {
	case errors.Is(err, ErrSeen):
		return ""
	case errors.Is(err, ErrKeyReused):
		return reasonKeyReused
	case errors.Is(err, ErrSkipped):
		return reasonSkipped
	case errors.Is(err, ErrTypeNotRunnable):
		return reasonTypeNotRunnable
	case errors.Is(err, ErrRoutingRefused):
		return reasonRoutingRefused
	case errors.As(err, &refused), errors.Is(err, ErrInvalid):
		return reasonInvalid
	}
	return ""
}

// untaken puts e as unroutable or refused, for reason. It logs nothing: the
// inbound handler logs the one line naming the mapping's owner (design D10)
// for every event Ingest answers unroutable or refused, from its outcome and
// reason.
func (c *Coordinator) untaken(ctx context.Context, e *mapping.Event, outcome InboundOutcome, reason string) (IngestResult, error) {
	return c.put(ctx, rowOf(e, outcome, reason, false))
}

// put writes row, which touches no item. A write that fails is logged as a
// step's, which fences on a lost lease, and returned.
func (c *Coordinator) put(ctx context.Context, row *InboundEvent) (IngestResult, error) {
	if err := c.p.Inbound.Put(ctx, row); err != nil {
		if !errors.Is(err, ErrSeen) {
			c.failed(ctx, "", fmt.Errorf("put the inbound event: %w", err))
		}
		return IngestResult{}, err
	}
	return IngestResult{Outcome: IngestOutcome(row.Outcome), Reason: row.Reason}, nil
}

// rowOf is e's row, with the columns the evaluator kept, for outcome and
// reason.
func rowOf(e *mapping.Event, outcome InboundOutcome, reason string, applied bool) *InboundEvent {
	return &InboundEvent{
		Source: e.Source, EventID: e.EventID, InstanceID: e.InstanceID, DedupKey: e.DedupKey, CorrelationKey: e.CorrelationKey,
		Status: e.Status, OccurredAt: e.Clamped, Applied: applied, Outcome: outcome, Reason: reason, Title: e.Title, Payload: e.Payload,
	}
}

// triggeredOf is e as item_triggered, applied or late.
func triggeredOf(e *mapping.Event, applied bool) *item.Triggered {
	return &item.Triggered{
		Source: e.Source, EventID: e.EventID, InstanceID: e.InstanceID, DedupKey: e.DedupKey, CorrelationKey: e.CorrelationKey,
		Status: e.Status, OccurredAt: e.Clamped, Applied: applied,
	}
}
