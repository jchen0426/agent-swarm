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
	// (fix round 1, I-1). The bound also bounds every store call made under
	// the lock.
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
	if err := c.reloadPending(ctx, e.Source); err != nil {
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

// reloadPending loads every item pending reload of source (R133): an append
// of an earlier event to it may have landed, its answer lost, so the
// account's entry cannot say which keys it took. A load that succeeds clears
// the item's mark (see refresh). One that finds the item unreadable clears it
// too: the item leaves the account (see unreadable), which then holds no
// stale entry of it, and LastItem's load answers for it (see lastItem). Any
// other failure is returned: no event of the source is taken until the item
// loads.
func (c *Coordinator) reloadPending(ctx context.Context, source string) error {
	for _, id := range slices.Sorted(maps.Keys(c.pending)) {
		if c.pending[id] != source {
			continue
		}
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
// its auto-close (design D5). An append that fails is returned (see
// appendFailed).
func (c *Coordinator) appendTo(ctx context.Context, s *item.State, e *mapping.Event, applied bool) (IngestResult, error) {
	outcome := InboundAppended
	if !applied {
		outcome = InboundLate
	}
	if _, err := c.recordInbound(ctx, s, rowOf(e, outcome, "", applied), rec{body: item.Body{Triggered: triggeredOf(e, applied)}}); err != nil {
		return IngestResult{}, c.appendFailed(ctx, s.ID, e.Source, err)
	}
	c.advance(c.ctx, s.ID)
	return IngestResult{Outcome: IngestOutcome(outcome), Item: s.ID}, nil
}

// appendFailed answers an append of an event to an item that failed. ErrSeen
// is a repeat. Otherwise the failure is logged as a step's, which fences on a
// lost lease; and an append whose outcome is unknown, one that the guard did
// not refuse and whose decide did not, may have landed with its answer lost:
// the item is marked pending reload by the event's source, and its retry is
// armed, which loads it (R133).
func (c *Coordinator) appendFailed(ctx context.Context, id item.ID, source string, err error) error {
	if errors.Is(err, ErrSeen) {
		return err
	}
	c.failed(ctx, id, err)
	if c.held && mayHaveLanded(err) {
		c.pending[id] = source
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
// openFor).
func (c *Coordinator) firing(ctx context.Context, in *IngestInput) (IngestResult, error) {
	e := in.Event
	id, ok, err := c.p.Inbound.LastItem(ctx, e.Source, e.DedupKey)
	if err != nil {
		return IngestResult{}, fmt.Errorf("read the last item of the dedup key: %w", err)
	}
	if ok {
		if out, done, err := c.lastItem(ctx, e, id); done || err != nil {
			return out, err
		}
	}
	return c.openFor(ctx, in)
}

// lastItem answers e, a firing that matched no item, by id, the last item its
// dedup key took, and reports whether it did. An item held by a later build,
// or whose records do not fold, refuses e, item_unreadable, so that no second
// item opens beside it (review M1). One that is not closed, outside the
// account, as an opening whose answer and lookup were lost, enters it as it
// loads, and e is matched again (R133). A closed one, or one with no
// opening, answers nothing: e opens an item. A load that fails otherwise is
// returned.
func (c *Coordinator) lastItem(ctx context.Context, e *mapping.Event, id item.ID) (IngestResult, bool, error) {
	if c.isSkipped(id) {
		return c.unreadableItem(ctx, e, id, ErrSkipped)
	}
	s, err := c.load(ctx, id)
	switch {
	case corrupt(err) || ofLaterVersion(err):
		return c.unreadableItem(ctx, e, id, err)
	case err != nil:
		return IngestResult{}, true, err
	case s.Opened == nil || s.Status() == item.StatusClosed:
		return IngestResult{}, false, nil
	}
	if m := c.match(e); m != nil {
		out, err := c.appendTo(ctx, m, e, true)
		return out, true, err
	}
	return IngestResult{}, false, nil
}

// unreadableItem refuses e, item_unreadable, as the last item its dedup key
// took, id, cannot be read, as err says, and logs it at Warn.
func (c *Coordinator) unreadableItem(ctx context.Context, e *mapping.Event, id item.ID, err error) (IngestResult, bool, error) {
	c.log.WarnContext(ctx, "an inbound event is refused: the last item of its dedup key cannot be read",
		"source", e.Source, "mapping", e.Mapping, "owner", e.Owner, "event", e.EventID, "item", id, "err", err)
	out, err := c.put(ctx, rowOf(e, InboundRefused, reasonItemUnreadable, false))
	return out, true, err
}

// openFor opens the item of e, a firing that matched no item (design D8), as
// Open does, under the source's actor, with in.ID, the event's trigger and
// its applied firing, and the opened row, in one append. A refusal of the
// opening refuses the event with its reason (see refusal), and the next event
// is taken (review I4). ErrNoRevision and a store's error are returned.
func (c *Coordinator) openFor(ctx context.Context, in *IngestInput) (IngestResult, error) {
	e := in.Event
	open := &OpenInput{
		ID: in.ID, Type: e.Type, Title: e.Title, Source: e.Source, Routing: e.Routing,
		Actor:     Actor{Subject: SourceSubjectPrefix + e.Source, Source: true},
		Trigger:   &item.Trigger{Source: e.Source, DedupKey: e.DedupKey, CorrelationKey: e.CorrelationKey},
		Triggered: triggeredOf(e, true),
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

// untaken puts e as unroutable or refused, for reason, and logs it at Warn,
// naming its mapping and the mapping's owner (design D10).
func (c *Coordinator) untaken(ctx context.Context, e *mapping.Event, outcome InboundOutcome, reason string) (IngestResult, error) {
	c.log.WarnContext(ctx, "an inbound event is not taken", "source", e.Source, "mapping", e.Mapping, "owner", e.Owner,
		"event", e.EventID, "outcome", string(outcome), "reason", reason)
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
