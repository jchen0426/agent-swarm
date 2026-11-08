package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// duplicate is a link's preset reason, a code alone.
var duplicate = board.Reason{Code: "duplicate"}

// linking is a link or an unlink through the admin API, by its admin, under
// key, with a preset reason; its preview when key is empty.
func linking(key string) *app.Act {
	reason := duplicate
	return &app.Act{Actor: administrator, Key: key, Reason: &reason, Preview: key == ""}
}

// itemsOf returns the items of an incident, as the records store reads them.
func itemsOf(h *harness, incident item.IncidentID) []item.ID {
	h.t.Helper()
	items, err := h.records.IncidentItems(h.t.Context(), incident)
	require.NoError(h.t, err)
	return items
}

// recordsOf returns an item's records.
func recordsOf(h *harness, id item.ID) []item.Record {
	h.t.Helper()
	records, err := h.records.Load(h.t.Context(), id)
	require.NoError(h.t, err)
	return records
}

// A link moves one item into the incident the other has now (Decision 18): B
// linked to A records item_linked on B, whose column holds A's incident and
// whose body names the incident B left, the item linked to, who linked it,
// why and the act's request; B's incident is A's; A's records do not change.
// Fails first: a build failure.
func TestLinkingMovesTheItemIntoTheOtherItemsIncident(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	b := h.open("payments fail")
	ia, ib := h.state(a).Incident, h.state(b).Incident
	require.NotEqual(t, ia, ib)
	recordsOfA := recordsOf(h, a)

	out, err := h.c.Link(t.Context(), b, a, linking("link-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{
		Incident: &app.IncidentChange{From: ib, To: ia, Items: sortedIDs(a, b)},
	}}, out)
	r := lastRecord(h, b)
	require.NotNil(t, r.Body.Linked)
	assert.Equal(t, ia, r.Incident, "the column holds the incident it joins")
	assert.Equal(t, ib, r.Body.Linked.Left)
	assert.Equal(t, a, r.Body.Linked.With)
	assert.Equal(t, administrator.Subject, r.Body.Linked.By)
	assert.Equal(t, &duplicate, r.Body.Linked.Reason)
	assert.Equal(t, keyHash("link-1"), r.Body.Linked.Request.Key)
	assert.Empty(t, r.Run, "a record of the item")
	assert.Equal(t, ia, h.state(b).Incident)
	assert.Equal(t, recordsOfA, recordsOf(h, a), "A's records are unchanged")
}

// An item's incident is its latest record's column (Decision 18): once B is
// linked to A and records nothing more, it is among A's incident's items, and
// no longer among its old incident's.
func TestALinkedItemThatRecordsNothingMoreIsAmongItsNewIncidentsItems(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	b := h.open("payments fail")
	ia, ib := h.state(a).Incident, h.state(b).Incident

	_, err := h.c.Link(t.Context(), b, a, linking("link-1"))

	require.NoError(t, err)
	assert.Equal(t, sortedIDs(a, b), itemsOf(h, ia))
	assert.Empty(t, itemsOf(h, ib))
}

// An item is not linked to itself (R60): refused self, a preview too, and
// nothing is recorded.
func TestLinkingAnItemToItselfIsRefused(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	kinds := h.kinds(a)
	for _, key := range []string{"link-1", ""} {
		out, err := h.c.Link(t.Context(), a, a, linking(key))

		assert.Equal(t, app.CodeSelf, refusal(err), "key %q", key)
		assert.Equal(t, app.Outcome{}, out)
	}
	assert.Equal(t, kinds, h.kinds(a), "nothing recorded")
}

// A link to an item with no records answers ErrNoItem, which the admin API
// answers 404 no_such_item, and records nothing (R60).
func TestLinkingToAnUnknownItemAnswersNotFound(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	kinds := h.kinds(a)

	_, err := h.c.Link(t.Context(), a, "no-such-item", linking("link-1"))

	require.ErrorIs(t, err, app.ErrNoItem)
	assert.Equal(t, kinds, h.kinds(a), "nothing recorded")
}

// Linking an item into the incident it is in changes nothing (R60): changed
// false, the incident and its items as they are, and nothing is recorded,
// whichever of the two items is linked to the other.
func TestLinkingAnItemAlreadyInTheIncidentChangesNothing(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	b := h.open("payments fail")
	ia := h.state(a).Incident
	_, err := h.c.Link(t.Context(), b, a, linking("link-1"))
	require.NoError(t, err)
	kindsA, kindsB := h.kinds(a), h.kinds(b)

	for name, link := range map[string]func() (app.Outcome, error){
		"B to A again": func() (app.Outcome, error) { return h.c.Link(t.Context(), b, a, linking("link-2")) },
		"A to B":       func() (app.Outcome, error) { return h.c.Link(t.Context(), a, b, linking("link-3")) },
	} {
		out, err := link()

		require.NoError(t, err, name)
		assert.False(t, out.Changed, name)
		assert.Equal(t, &app.IncidentChange{From: ia, To: ia, Items: sortedIDs(a, b)}, out.Consequences.Incident, name)
	}
	assert.Equal(t, kindsA, h.kinds(a), "nothing recorded")
	assert.Equal(t, kindsB, h.kinds(b), "nothing recorded")
}

// An unlink gives the item a new incident of its own (R61): neither the one it
// left nor its first; the one it left keeps its other items.
func TestUnlinkingGivesTheItemAnIncidentOfItsOwn(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	b := h.open("payments fail")
	ia, ib := h.state(a).Incident, h.state(b).Incident
	_, err := h.c.Link(t.Context(), b, a, linking("link-1"))
	require.NoError(t, err)

	out, err := h.c.Unlink(t.Context(), b, linking("unlink-1"))

	require.NoError(t, err)
	now := h.state(b).Incident
	assert.NotEqual(t, ia, now)
	assert.NotEqual(t, ib, now)
	assert.NotEmpty(t, now)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{
		Incident: &app.IncidentChange{From: ia, To: now, Items: []item.ID{b}},
	}}, out)
	r := lastRecord(h, b)
	require.NotNil(t, r.Body.Unlinked)
	assert.Equal(t, now, r.Incident)
	assert.Equal(t, ia, r.Body.Unlinked.Left)
	assert.Equal(t, administrator.Subject, r.Body.Unlinked.By)
	assert.Equal(t, keyHash("unlink-1"), r.Body.Unlinked.Request.Key)
	assert.Equal(t, []item.ID{a}, itemsOf(h, ia), "A's items are A alone")
	assert.Equal(t, []item.ID{b}, itemsOf(h, now))
}

// Unlinking an item alone in its incident changes nothing (R60): changed
// false, and nothing is recorded.
func TestUnlinkingAnItemAloneInItsIncidentChangesNothing(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	ia := h.state(a).Incident
	kinds := h.kinds(a)

	out, err := h.c.Unlink(t.Context(), a, linking("unlink-1"))

	require.NoError(t, err)
	assert.False(t, out.Changed)
	assert.Equal(t, &app.IncidentChange{From: ia, To: ia, Items: []item.ID{a}}, out.Consequences.Incident)
	assert.Equal(t, kinds, h.kinds(a), "nothing recorded")
}

// incidentsBlink is the incidents' port whose every read fails.
type incidentsBlink struct{}

func (incidentsBlink) IncidentItems(context.Context, item.IncidentID) ([]item.ID, error) {
	return nil, errBlinked
}

// An unlink reads whether its item is alone in its incident under the
// coordinator's lock (R60): a read that fails answers its error, which the
// admin API answers 503 unavailable, and nothing is recorded; so does a
// link, which reads the items it would join.
func TestALinkOrUnlinkWhoseIncidentCannotBeReadRecordsNothing(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	b := h.open("payments fail")
	_, err := h.c.Link(t.Context(), b, a, linking("link-1"))
	require.NoError(t, err)
	ports := h.ports()
	ports.Incidents = incidentsBlink{}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
	kindsA, kindsB := h.kinds(a), h.kinds(b)

	_, err = h.c.Unlink(t.Context(), b, linking("unlink-1"))

	require.ErrorIs(t, err, errBlinked)
	assert.Empty(t, refusal(err))
	_, err = h.c.Link(t.Context(), a, h.open("orders fail"), linking("link-2"))
	require.ErrorIs(t, err, errBlinked)
	assert.Equal(t, kindsA, h.kinds(a), "nothing recorded")
	assert.Equal(t, kindsB, h.kinds(b), "nothing recorded")
}

// A link's or an unlink's preview answers its consequences with changed
// false, recording nothing (Decision 13): the incident the item leaves, the
// one it joins and its items after the act; an unlink's new incident is
// named only once recorded.
func TestALinksPreviewShowsTheIncidentItWouldJoinAndRecordsNothing(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	b := h.open("payments fail")
	ia, ib := h.state(a).Incident, h.state(b).Incident

	out, err := h.c.Link(t.Context(), b, a, linking(""))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Consequences: app.Consequences{
		Incident: &app.IncidentChange{From: ib, To: ia, Items: sortedIDs(a, b)},
	}}, out)
	assert.Equal(t, ib, h.state(b).Incident, "nothing recorded")
	_, err = h.c.Link(t.Context(), b, a, linking("link-1"))
	require.NoError(t, err)
	kinds := h.kinds(b)

	out, err = h.c.Unlink(t.Context(), b, linking(""))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Consequences: app.Consequences{
		Incident: &app.IncidentChange{From: ia, Items: []item.ID{b}},
	}}, out)
	assert.Equal(t, kinds, h.kinds(b), "nothing recorded")
}

// A link and an unlink take a preset reason (spec Section 4.2): without one,
// each is refused reason_required, a preview too, and records nothing; one
// with a code alone is taken.
func TestALinkNeedsAPresetReason(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	b := h.open("payments fail")
	kinds := h.kinds(b)
	for _, key := range []string{"act-1", ""} {
		_, err := h.c.Link(t.Context(), b, a, &app.Act{Actor: administrator, Key: key, Preview: key == ""})
		assert.Equal(t, app.CodeReasonRequired, refusal(err), "a link, key %q", key)
		_, err = h.c.Unlink(t.Context(), b, &app.Act{Actor: administrator, Key: key, Preview: key == ""})
		assert.Equal(t, app.CodeReasonRequired, refusal(err), "an unlink, key %q", key)
	}
	assert.Equal(t, kinds, h.kinds(b), "nothing recorded")

	out, err := h.c.Link(t.Context(), b, a, linking("link-1"))
	require.NoError(t, err, "a code alone")
	require.True(t, out.Changed)
	out, err = h.c.Unlink(t.Context(), b, linking("unlink-1"))
	require.NoError(t, err, "a code alone")
	assert.True(t, out.Changed)
}

// A closed item, either one, is linked or unlinked only within the window of
// late rulings of its close, Options.ClosedRulings, judged in the database's
// time (R60, R64): within it the link lands, whichever item is closed; past
// it, each is refused rulings_window_passed, a preview too, and records
// nothing.
func TestAClosedItemIsLinkedWithinTheRulingsWindowOnly(t *testing.T) {
	h := newHarness(t, func(o *app.Options) { o.ClosedRulings = 10 * time.Minute })
	a := closedAwaiting(h, "checkout fails")
	b := h.open("payments fail")
	c := h.open("orders fail")
	ic := h.state(c).Incident

	_, err := h.c.Link(t.Context(), b, a, linking("link-1"))
	require.NoError(t, err, "into a closed item's incident, within the window")
	_, err = h.c.Link(t.Context(), a, c, linking("link-2"))
	require.NoError(t, err, "a closed item, within the window")
	require.Equal(t, ic, h.state(a).Incident)

	h.clock.Advance(10*time.Minute + time.Second)
	d := h.open("search fails")
	kindsA, kindsD := h.kinds(a), h.kinds(d)
	for _, key := range []string{"late-1", ""} {
		_, err = h.c.Link(t.Context(), d, a, linking(key))
		assert.Equal(t, app.CodeRulingsWindowPassed, refusal(err), "into a closed item's incident, key %q", key)
		_, err = h.c.Link(t.Context(), a, d, linking(key))
		assert.Equal(t, app.CodeRulingsWindowPassed, refusal(err), "a closed item, key %q", key)
		_, err = h.c.Unlink(t.Context(), a, linking(key))
		assert.Equal(t, app.CodeRulingsWindowPassed, refusal(err), "an unlink of a closed item, key %q", key)
	}
	assert.Equal(t, kindsA, h.kinds(a), "nothing recorded")
	assert.Equal(t, kindsD, h.kinds(d), "nothing recorded")
}

// A link follows a chain (Review Focus 4): B linked to C, then A linked to B,
// takes C's incident, the one B has now, not B's first. A key is one act of
// its item (R98): a link under the key of an unlink of the same item, or
// under its own key with another item, is refused ErrKeyReused, never taken
// as a repeat; under its own key with the same item, it is a repeat.
func TestALinkTakesTheOtherItemsIncidentNowNotItsFirst(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	b := h.open("payments fail")
	c := h.open("orders fail")
	ib, ic := h.state(b).Incident, h.state(c).Incident
	_, err := h.c.Link(t.Context(), b, c, linking("link-b"))
	require.NoError(t, err)

	out, err := h.c.Link(t.Context(), a, b, linking("link-a"))

	require.NoError(t, err)
	assert.Equal(t, ic, h.state(a).Incident, "C's incident")
	assert.NotEqual(t, ib, h.state(a).Incident, "not B's first")
	assert.Equal(t, sortedIDs(a, b, c), out.Consequences.Incident.Items)

	again, err := h.c.Link(t.Context(), a, b, linking("link-a"))
	require.NoError(t, err)
	assert.Equal(t, out, again, "a repeat answers as the link did")
	_, err = h.c.Link(t.Context(), a, c, linking("link-a"))
	require.ErrorIs(t, err, app.ErrKeyReused, "another item under the key")
	_, err = h.c.Unlink(t.Context(), a, linking("unlink-a"))
	require.NoError(t, err)
	_, err = h.c.Link(t.Context(), a, b, linking("unlink-a"))
	require.ErrorIs(t, err, app.ErrKeyReused, "a link under an unlink's key")
	assert.Equal(t, 1, countOf(h, a, item.KindLinked))
	assert.Equal(t, 1, countOf(h, a, item.KindUnlinked))
}

// linkAnswerLost is the records store whose appends of an item_linked
// commit, then fail with errAnswerLost, as when the database's answer is lost
// on its way back. Every other append answers.
type linkAnswerLost struct{ *failing }

func (l linkAnswerLost) Append(ctx context.Context, id item.ID, decide func([]item.Record) ([]item.Draft, error)) ([]item.Record, error) {
	committed, err := l.failing.Append(ctx, id, decide)
	if err != nil {
		return nil, err
	}
	for i := range committed {
		if committed[i].Body.Linked != nil {
			return nil, errAnswerLost
		}
	}
	return committed, nil
}

// A link whose item_linked landed with its answer lost, and whose lookup
// failed too, answers the append's error (Decision 13); the retry under its
// key answers as a repeat, changed, into the incident it joined, recording
// nothing more. (The plan's.)
func TestALinkWhoseOutcomeIsUnknownIsFoundByItsKey(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	b := h.open("payments fail")
	ia, ib := h.state(a).Incident, h.state(b).Incident
	ports := h.ports()
	ports.Records = linkAnswerLost{h.records}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c
	blinked := false
	h.records.loads(func(records []item.Record) error {
		if !blinked && len(records) > 0 && records[0].Item == b && records[len(records)-1].Body.Kind() == item.KindLinked {
			blinked = true
			return errBlinked
		}
		return nil
	})

	_, err = h.c.Link(t.Context(), b, a, linking("link-1"))

	require.ErrorIs(t, err, errAnswerLost, "the outcome is not known yet")
	require.True(t, blinked, "the lookup failed")
	require.Equal(t, item.KindLinked, lastKind(h, b), "it landed")

	out, err := h.c.Link(t.Context(), b, a, linking("link-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{
		Incident: &app.IncidentChange{From: ib, To: ia, Items: sortedIDs(a, b)},
	}}, out, "a repeat")
	assert.Equal(t, 1, countOf(h, b, item.KindLinked), "nothing more")
}

// A link whose append's answer is lost and whose lookup finds it answers as
// the link did, once (Decision 13).
func TestALinkWhoseLostAnswerIsFoundAnswersAsItDid(t *testing.T) {
	h := newHarness(t)
	a := h.open("checkout fails")
	b := h.open("payments fail")
	ia, ib := h.state(a).Incident, h.state(b).Incident
	ports := h.ports()
	ports.Records = linkAnswerLost{h.records}
	c, err := app.NewCoordinator(ports, &h.opts)
	require.NoError(t, err)
	h.c.Fence()
	require.NoError(t, c.TakeOver(t.Context(), app.Term{Epoch: h.epoch, At: h.clock.Now(), Previous: h.clock.Now()}))
	h.c = c

	out, err := h.c.Link(t.Context(), b, a, linking("link-1"))

	require.NoError(t, err)
	assert.Equal(t, app.Outcome{Changed: true, Consequences: app.Consequences{
		Incident: &app.IncidentChange{From: ib, To: ia, Items: sortedIDs(a, b)},
	}}, out)
	assert.Equal(t, 1, countOf(h, b, item.KindLinked))
}

// A link moves an item into the incident of the item with, so its actor must
// be a member of both, or an admin (A4; D10): a responder of one item alone
// is refused not_item_responder, whichever item it is, and nothing is
// recorded; an admin links them. An unlink, which takes an item out, is
// checked on its own item alone. (The plan's.)
func TestALinkNeedsTheActorOnBothItems(t *testing.T) {
	h := newHarness(t)
	x := owned(h)
	y, err := h.c.Open(t.Context(), openingAs(administrator, "sso:frank"))
	require.NoError(t, err)
	erin := app.Actor{Subject: "sso:erin", Responder: true}
	kindsX, kindsY := h.kinds(x), h.kinds(y)
	reason := duplicate

	_, err = h.c.Link(t.Context(), x, y, &app.Act{Actor: erin, Key: "link-1", Reason: &reason})
	assert.Equal(t, app.CodeNotItemResponder, refusal(err), "into an item erin does not handle")
	_, err = h.c.Link(t.Context(), y, x, &app.Act{Actor: erin, Key: "link-2", Reason: &reason})
	assert.Equal(t, app.CodeNotItemResponder, refusal(err), "an item erin does not handle")
	assert.Equal(t, kindsX, h.kinds(x), "nothing recorded")
	assert.Equal(t, kindsY, h.kinds(y), "nothing recorded")

	out, err := h.c.Link(t.Context(), x, y, linking("link-3"))
	require.NoError(t, err, "an admin links them")
	require.True(t, out.Changed)

	out, err = h.c.Unlink(t.Context(), x, &app.Act{Actor: erin, Key: "unlink-1", Reason: &reason})
	require.NoError(t, err, "erin takes her item out")
	assert.True(t, out.Changed)
	assert.Equal(t, erin.Subject, lastRecord(h, x).Body.Unlinked.By)
}

// A coordinator needs the incidents' port, which Unlink reads, and a window
// of late rulings above zero, which a link of a closed item is judged by
// (R64; R102).
func TestACoordinatorNeedsTheIncidentsAndAClosedRulingsWindow(t *testing.T) {
	h := newHarness(t)
	ports := h.ports()
	ports.Incidents = nil
	_, err := app.NewCoordinator(ports, &h.opts)
	require.Error(t, err, "no incidents")
	for _, window := range []time.Duration{0, -time.Second} {
		opts := h.opts
		opts.ClosedRulings = window

		_, err := app.NewCoordinator(h.ports(), &opts)

		require.Error(t, err, "a window of %v", window)
	}
}
