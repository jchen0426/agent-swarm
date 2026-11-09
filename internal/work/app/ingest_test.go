package app_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/inbound/mapping"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/dirsource"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// errStore is a store's failure.
var errStore = errors.New("the store did not answer")

// trigger is an event of the alerting source, as its mapping evaluated it: of
// instance and dedup key, with status at at, an incident routed
// service=checkout, with no correlation.
func trigger(id, instance, dedup string, status item.TriggerStatus, at time.Time) *mapping.Event {
	return &mapping.Event{
		Source: "alerting", Mapping: "alerts", Owner: "sre-leads", Type: "incident", EventID: id,
		InstanceID: instance, DedupKey: dedup, Status: status, Occurred: at, Clamped: at, Title: "checkout fails",
		Routing: map[string]string{"service": "checkout"}, Payload: `{"id":"` + id + `"}`,
	}
}

// correlated is trigger correlated by key within within.
func correlated(id, instance, dedup string, status item.TriggerStatus, at time.Time, key string, within time.Duration) *mapping.Event {
	e := trigger(id, instance, dedup, status, at)
	e.CorrelationKey, e.Within = key, within
	return e
}

// inboundID is the item an opening by e takes (R121).
func inboundID(e *mapping.Event) item.ID {
	return app.ItemID("tenant", "inbound:"+e.Source+":"+e.EventID)
}

// ingest has the coordinator take e, and requires it answered.
func ingest(h *harness, e *mapping.Event) app.IngestResult {
	h.t.Helper()
	out, err := h.c.Ingest(h.t.Context(), &app.IngestInput{Event: e, ID: inboundID(e)})
	require.NoError(h.t, err, "event %s", e.EventID)
	return out
}

// rowsOf returns the rows the store committed of source's event id.
func rowsOf(h *harness, source, id string) []app.InboundEvent {
	rows := h.records.inbound()
	var out []app.InboundEvent
	for i := range rows {
		if rows[i].Source == source && rows[i].EventID == id {
			out = append(out, rows[i])
		}
	}
	return out
}

// rowOf returns the one row of alerting's event id.
func rowOf(h *harness, id string) app.InboundEvent {
	h.t.Helper()
	rows := rowsOf(h, "alerting", id)
	require.Len(h.t, rows, 1, "one row of event %s", id)
	return rows[0]
}

// triggers returns the item_triggered records of item id, in order.
func triggers(h *harness, id item.ID) []item.Triggered {
	h.t.Helper()
	records, err := h.records.Load(h.t.Context(), id)
	require.NoError(h.t, err)
	var out []item.Triggered
	for i := range records {
		if t := records[i].Body.Triggered; t != nil {
			out = append(out, *t)
		}
	}
	return out
}

// active lists the items that are not closed.
func active(h *harness) []item.ID {
	h.t.Helper()
	ids, err := h.records.Active(h.t.Context())
	require.NoError(h.t, err)
	return ids
}

// once returns a fault that answers err to its first call alone.
func once(err error) func(int) error {
	var spent atomic.Bool
	return func(int) error {
		if spent.CompareAndSwap(false, true) {
			return err
		}
		return nil
	}
}

// An event taken twice is a repeat the second time: it writes nothing, so its
// item holds one item_triggered of it and the store one row.
func TestARepeatedEventWritesNothing(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.Now()
	opened := ingest(h, trigger("e0", "i1", "d1", item.Firing, t0))
	e := trigger("e1", "i1", "d1", item.Resolved, t0.Add(time.Minute))

	first := ingest(h, e)
	kinds, writes := h.kinds(opened.Item), len(h.records.writes.noted())
	second := ingest(h, e)

	assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: opened.Item}, first)
	assert.Equal(t, app.IngestResult{Outcome: app.IngestRepeat}, second)
	assert.Equal(t, kinds, h.kinds(opened.Item), "nothing more is recorded")
	assert.Len(t, h.records.writes.noted(), writes, "no write is tried")
	n := 0
	for _, tr := range triggers(h, opened.Item) {
		if tr.EventID == "e1" {
			n++
		}
	}
	assert.Equal(t, 1, n, "one record of the event")
	assert.Len(t, rowsOf(h, "alerting", "e1"), 1, "one row of the event")
}

// An event the evaluator found unroutable, or refused, is put with its
// outcome and reason, the columns it kept, and opens nothing (design D10).
func TestAnUnroutableOrRefusedEventIsRecordedAndOpensNothing(t *testing.T) {
	h := newHarness(t)
	at := h.clock.Now()
	unroutable := trigger("e1", "", "", item.Firing, at)
	unroutable.Unroutable, unroutable.Routing = "missing:instanceId", nil
	refused := trigger("e2", "i2", "d2", item.Firing, at)
	refused.Refused = "checks_unsupported"

	assert.Equal(t, app.IngestResult{Outcome: app.IngestUnroutable, Reason: "missing:instanceId"}, ingest(h, unroutable))
	assert.Equal(t, app.IngestResult{Outcome: app.IngestRefused, Reason: "checks_unsupported"}, ingest(h, refused))

	assert.Equal(t, app.InboundEvent{
		Source: "alerting", EventID: "e1", Status: item.Firing, OccurredAt: at, Outcome: app.InboundUnroutable, Reason: "missing:instanceId",
		Title: "checkout fails", Payload: `{"id":"e1"}`,
	}, rowOf(h, "e1"))
	assert.Equal(t, app.InboundEvent{
		Source: "alerting", EventID: "e2", InstanceID: "i2", DedupKey: "d2", Status: item.Firing, OccurredAt: at, Outcome: app.InboundRefused,
		Reason: "checks_unsupported", Title: "checkout fails", Payload: `{"id":"e2"}`,
	}, rowOf(h, "e2"))
	assert.Empty(t, active(h), "no item opens")
}

// An event not later than its instance's last status at its source is late
// (design D7 step 3; review C2): appended, not applied, to the item that
// holds the instance, or put late; at equal time a resolution of a firing
// alone is later.
func TestAnEventNotLaterThanItsInstancesLastStatusIsLate(t *testing.T) {
	for name, tc := range map[string]struct {
		before []*mapping.Event
		event  func(t0 time.Time) *mapping.Event
		want   app.IngestOutcome
		onItem bool
	}{
		"an older firing of an instance an item holds": {
			event: func(t0 time.Time) *mapping.Event { return trigger("e1", "i1", "d1", item.Firing, t0.Add(-time.Minute)) },
			want:  app.IngestLate, onItem: true,
		},
		"an older firing of an instance no item holds": {
			before: []*mapping.Event{trigger("r1", "i9", "d9", item.Resolved, time.Time{})},
			event:  func(t0 time.Time) *mapping.Event { return trigger("e1", "i9", "d9", item.Firing, t0.Add(-time.Minute)) },
			want:   app.IngestLate,
		},
		"a firing at the time of the last resolution": {
			before: []*mapping.Event{trigger("r1", "i9", "d9", item.Resolved, time.Time{})},
			event:  func(t0 time.Time) *mapping.Event { return trigger("e1", "i9", "d9", item.Firing, t0) },
			want:   app.IngestLate,
		},
		"a resolution at the time of the last firing": {
			event: func(t0 time.Time) *mapping.Event { return trigger("e1", "i1", "d1", item.Resolved, t0) },
			want:  app.IngestAppended, onItem: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			t0 := h.clock.Now()
			x := ingest(h, trigger("e0", "i1", "d1", item.Firing, t0)).Item
			for _, e := range tc.before {
				e.Clamped, e.Occurred = t0, t0
				require.Equal(t, app.IngestResolvedUnmatched, ingest(h, e).Outcome)
			}
			e := tc.event(t0)

			out := ingest(h, e)

			assert.Equal(t, tc.want, out.Outcome)
			row := rowOf(h, "e1")
			assert.Equal(t, tc.want == app.IngestAppended, row.Applied)
			assert.Equal(t, app.InboundOutcome(tc.want), row.Outcome)
			if !tc.onItem {
				assert.Empty(t, out.Item)
				assert.Empty(t, row.Item)
				assert.Equal(t, []item.ID{x}, active(h), "a late event opens nothing")
				return
			}
			assert.Equal(t, x, out.Item)
			assert.Equal(t, x, row.Item)
			tr := triggers(h, x)
			require.Len(t, tr, 2)
			assert.Equal(t, tc.want == app.IngestAppended, tr[1].Applied)
			assert.Equal(t, "e1", tr[1].EventID)
		})
	}
}

// A retried old firing opens no zombie item (review C2): once its
// instance's resolution was put unmatched at t2, its firing at t1 is late,
// and no item opens.
func TestARetriedOldFiringOpensNoZombieItem(t *testing.T) {
	h := newHarness(t)
	t1 := h.clock.Now()
	t2 := t1.Add(time.Minute)
	require.Equal(t, app.IngestResolvedUnmatched, ingest(h, trigger("r", "i", "d", item.Resolved, t2)).Outcome)

	out := ingest(h, trigger("f", "i", "d", item.Firing, t1))

	assert.Equal(t, app.IngestResult{Outcome: app.IngestLate}, out)
	assert.Empty(t, active(h), "no item")
}

// The match (design D7 step 4): an item holding the event's instance wins
// over an older one holding its dedup key; of two items holding the key, the
// older; the correlation key matches only within its window of the item's
// opening.
func TestAnEventMatchesByInstanceThenDedupKeyThenCorrelationKey(t *testing.T) {
	t.Run("the instance before an older item's dedup key", func(t *testing.T) {
		h := newHarness(t)
		t0 := h.clock.Now()
		older := ingest(h, trigger("a", "ia", "k1", item.Firing, t0)).Item
		newer := ingest(h, trigger("b", "ib", "k2", item.Firing, t0)).Item
		require.NotEqual(t, older, newer)

		out := ingest(h, trigger("e", "ib", "k1", item.Firing, t0.Add(time.Minute)))

		assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: newer}, out)
	})
	t.Run("of two items holding the dedup key, the older", func(t *testing.T) {
		h := newHarness(t)
		t0 := h.clock.Now()
		older := ingest(h, trigger("a", "ia", "k", item.Firing, t0)).Item
		newer := ingest(h, trigger("b", "ib", "k2", item.Firing, t0)).Item
		require.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: newer}, ingest(h, trigger("c", "ib", "k", item.Firing, t0.Add(time.Minute))),
			"the newer item takes the key by its instance")

		out := ingest(h, trigger("e", "ic", "k", item.Firing, t0))

		assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: older}, out)
	})
	t.Run("the correlation key within its window alone", func(t *testing.T) {
		h := newHarness(t)
		t0 := h.clock.Now()
		x := ingest(h, correlated("a", "ia", "ka", item.Firing, t0, "checkout", time.Minute)).Item

		within := ingest(h, correlated("b", "ib", "kb", item.Firing, t0, "checkout", time.Minute))
		h.clock.Advance(time.Minute + time.Second)
		past := ingest(h, correlated("c", "ic", "kc", item.Firing, h.clock.Now(), "checkout", time.Minute))

		assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: x}, within)
		assert.Equal(t, app.IngestOpened, past.Outcome, "past the window a new item opens")
		assert.NotEqual(t, x, past.Item)
	})
}

// A correlated alert's resolution still finds its item (review C1; R111): A
// opens X; B correlates into X ten minutes later; B's resolution, an hour
// after the opening, past the window, is appended to X by its instance,
// never put unmatched; and B's next firing, of another instance, matches X by
// its dedup key.
func TestACorrelatedAlertsResolutionStillFindsItsItem(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.Now()
	x := ingest(h, correlated("a", "ia", "ka", item.Firing, t0, "checkout", 30*time.Minute)).Item
	h.clock.Advance(10 * time.Minute)
	require.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: x},
		ingest(h, correlated("b1", "ib", "kb", item.Firing, h.clock.Now(), "checkout", 30*time.Minute)))
	h.clock.Advance(50 * time.Minute)

	resolved := ingest(h, correlated("b2", "ib", "kb", item.Resolved, h.clock.Now(), "checkout", 30*time.Minute))
	again := ingest(h, correlated("b3", "ib2", "kb", item.Firing, h.clock.Now(), "checkout", 30*time.Minute))

	assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: x}, resolved)
	assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: x}, again)
}

// A person's item holds no key (review M4): a firing routed as a person's
// item is opens a new item.
func TestAPersonsItemTakesNoInboundEvent(t *testing.T) {
	h := newHarness(t)
	person := h.open("checkout fails")

	out := ingest(h, trigger("e1", "i1", "d1", item.Firing, h.clock.Now()))

	assert.Equal(t, app.IngestOpened, out.Outcome)
	assert.NotEqual(t, person, out.Item)
	assert.Empty(t, triggers(h, person))
}

// A resolution that matches no item is put resolved_unmatched, applied, and
// opens nothing (design D7 step 6); a later-sent older firing of its instance
// is ordered against it, late.
func TestAnUnmatchedResolutionIsRecordedAndOpensNothing(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.Now()

	out := ingest(h, trigger("r", "i", "d", item.Resolved, t0))
	late := ingest(h, trigger("f", "i", "d", item.Firing, t0.Add(-time.Second)))

	assert.Equal(t, app.IngestResult{Outcome: app.IngestResolvedUnmatched}, out)
	assert.Equal(t, app.InboundEvent{
		Source: "alerting", EventID: "r", InstanceID: "i", DedupKey: "d", Status: item.Resolved, OccurredAt: t0, Applied: true,
		Outcome: app.InboundResolvedUnmatched, Title: "checkout fails", Payload: `{"id":"r"}`,
	}, rowOf(h, "r"))
	assert.Equal(t, app.IngestLate, late.Outcome)
	assert.Empty(t, active(h))
}

// A firing that matches no item opens one under the source's actor (design
// D8; R121, R126): its id is derived from the event, and its first two
// records, item_opened with the trigger, by source:alerting, with the
// mapping's routing, and item_triggered, come in one append with the opened
// row. While the kill switch is on, item_awaiting_close follows them in the
// same append. Past the limit the item waits queued.
func TestAnUnmatchedFiringOpensAnItemUnderTheSourceActor(t *testing.T) {
	h := newHarness(t)
	at := h.clock.Now()
	e := correlated("e1", "i1", "d1", item.Firing, at, "checkout", time.Minute)
	id := app.ItemID("tenant", "inbound:alerting:e1")
	appends := appendsOf(h, id)

	out := ingest(h, e)

	assert.Equal(t, app.IngestResult{Outcome: app.IngestOpened, Item: id}, out)
	require.NotEmpty(t, appends())
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindTriggered}, appends()[0], "one append")
	records, err := h.records.Load(t.Context(), id)
	require.NoError(t, err)
	o := records[0].Body.Opened
	assert.Equal(t, &item.Trigger{Source: "alerting", DedupKey: "d1", CorrelationKey: "checkout"}, o.Trigger)
	assert.Equal(t, "source:alerting", o.By)
	assert.Equal(t, "alerting", o.Source)
	assert.Equal(t, map[string]string{"service": "checkout"}, o.Routing)
	assert.Equal(t, app.OpeningDigest(&app.OpenInput{Type: "incident", Routing: map[string]string{"service": "checkout"}}), o.Digest)
	assert.Equal(t, &item.Triggered{
		Source: "alerting", EventID: "e1", InstanceID: "i1", DedupKey: "d1", CorrelationKey: "checkout", Status: item.Firing, OccurredAt: at, Applied: true,
	}, records[1].Body.Triggered)
	assert.Equal(t, app.InboundEvent{
		Source: "alerting", EventID: "e1", InstanceID: "i1", DedupKey: "d1", CorrelationKey: "checkout", Status: item.Firing, OccurredAt: at,
		Item: id, Applied: true, Outcome: app.InboundOpened, Title: "checkout fails", Payload: `{"id":"e1"}`,
	}, rowOf(h, "e1"))

	_, err = h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	killed := trigger("e2", "i2", "d2", item.Firing, at)
	unstaffed := appendsOf(h, inboundID(killed))
	require.Equal(t, app.IngestOpened, ingest(h, killed).Outcome)
	require.Len(t, unstaffed(), 1)
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindTriggered, item.KindAwaitingClose}, unstaffed()[0], "one append")

	full := newHarness(t)
	for range full.opts.Limit {
		full.open("checkout fails")
	}
	queued := ingest(full, trigger("e3", "i3", "d3", item.Firing, at))
	assert.Equal(t, app.IngestOpened, queued.Outcome)
	assert.Equal(t, item.StatusQueued, full.state(queued.Item).Status(), "it waits in the queue")
}

// Two events of one dedup key taken at once open one item (review M10): one
// is opened, the other appended to it.
func TestTwoEventsOfOneKeyOpenOneItem(t *testing.T) {
	h := newHarness(t)
	at := h.clock.Now()
	events := []*mapping.Event{trigger("e1", "i1", "d", item.Firing, at), trigger("e2", "i2", "d", item.Firing, at)}
	outs := make([]app.IngestResult, len(events))
	errs := make([]error, len(events))
	var wg sync.WaitGroup
	for i, e := range events {
		wg.Go(func() {
			outs[i], errs[i] = h.c.Ingest(t.Context(), &app.IngestInput{Event: e, ID: inboundID(e)})
		})
	}
	wg.Wait()

	require.NoError(t, errors.Join(errs...))
	got := []app.IngestOutcome{outs[0].Outcome, outs[1].Outcome}
	assert.ElementsMatch(t, []app.IngestOutcome{app.IngestOpened, app.IngestAppended}, got)
	assert.Equal(t, outs[0].Item, outs[1].Item, "one item")
	assert.Len(t, active(h), 1)
}

// An opening Open refuses refuses the event with its reason (review I4;
// R131), and the next event is taken: a type whose key reads at open is
// type_not_runnable, a routing value of 257 characters routing_refused.
func TestAnOpeningRefusedByOpenIsRecordedWithItsReason(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(e *mapping.Event)
		reason string
	}{
		"a type whose key reads at open": {func(e *mapping.Event) { e.Type = "catalogued" }, "type_not_runnable"},
		"a routing value of 257 characters": {func(e *mapping.Event) {
			e.Routing = map[string]string{"service": strings.Repeat("x", 257)}
		}, "routing_refused"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			e := trigger("e1", "i1", "d1", item.Firing, h.clock.Now())
			tc.change(e)

			out := ingest(h, e)
			next := ingest(h, trigger("e2", "i2", "d2", item.Firing, h.clock.Now()))

			assert.Equal(t, app.IngestResult{Outcome: app.IngestRefused, Reason: tc.reason}, out)
			row := rowOf(h, "e1")
			assert.Equal(t, app.InboundRefused, row.Outcome)
			assert.Equal(t, tc.reason, row.Reason)
			assert.Empty(t, row.Item)
			assert.Empty(t, h.kinds(inboundID(e)), "no item")
			assert.Equal(t, app.IngestOpened, next.Outcome, "the next event is taken")
		})
	}
}

// An item outside the account is not opened again (review M1): when the last
// item of a firing's dedup key is not closed and does not fold, or is held by
// a later build, by its records or its board, the firing is refused
// item_unreadable, and no item opens.
func TestAnItemOutsideTheAccountIsNotOpenedAgain(t *testing.T) {
	for name, unreadable := range map[string]func(h *harness, x item.ID){
		"records that do not fold": func(h *harness, x item.ID) {
			h.records.loads(func(records []item.Record) error {
				if len(records) > 0 && records[0].Item == x {
					return fmt.Errorf("%w: a body that does not decode", item.ErrInvalid)
				}
				return nil
			})
		},
		"held by a later build": func(h *harness, x item.ID) {
			appendRaw(h.t, h, x, item.Body{V: item.Version + 1})
		},
		"its board held by a later build": func(h *harness, x item.ID) {
			appendLaterEvent(h.t, h, h.state(x).Run.ID)
			h.crash()
			require.NoError(h.t, h.boot())
			require.Equal(h.t, []item.ID{x}, h.c.Skipped())
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			t0 := h.clock.Now()
			x := ingest(h, trigger("e1", "i1", "d", item.Firing, t0)).Item
			unreadable(h, x)
			h.c.AdvanceForTest(t.Context(), x)
			require.NotContains(t, h.c.Lifecycle().Waiting, x)
			e := trigger("e2", "i2", "d", item.Firing, t0)

			out := ingest(h, e)

			assert.Equal(t, app.IngestResult{Outcome: app.IngestRefused, Reason: "item_unreadable"}, out)
			assert.Empty(t, h.kinds(inboundID(e)), "no new item")
			assert.Equal(t, []item.ID{x}, active(h))
		})
	}
}

// An event whose append fails is tried again whole (review I1): one that
// fails before its commit writes no record and no row, and its retry is
// appended once; one whose answer is lost after its commit is a repeat when
// retried.
func TestAnEventWhoseAppendFailsIsTriedAgainWhole(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.Now()
	x := ingest(h, trigger("e0", "i1", "d1", item.Firing, t0)).Item
	in := func(e *mapping.Event) *app.IngestInput { return &app.IngestInput{Event: e, ID: inboundID(e)} }
	before := trigger("e1", "i2", "d1", item.Firing, t0)
	h.records.set(once(errStore))

	_, err := h.c.Ingest(t.Context(), in(before))

	require.ErrorIs(t, err, errStore)
	assert.Len(t, triggers(h, x), 1, "no record")
	assert.Empty(t, rowsOf(h, "alerting", "e1"), "no row")
	assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: x}, ingest(h, before))
	assert.Len(t, triggers(h, x), 2, "appended once")

	after := trigger("e2", "i3", "d1", item.Firing, t0)
	h.records.losesAnswers(once(errStore))

	_, err = h.c.Ingest(t.Context(), in(after))

	require.ErrorIs(t, err, errStore)
	assert.Equal(t, app.IngestResult{Outcome: app.IngestRepeat}, ingest(h, after))
	assert.Len(t, triggers(h, x), 3, "one record of the event")
	assert.Len(t, rowsOf(h, "alerting", "e2"), 1, "one row of the event")
}

// Two sources' instances of the same id are kept apart (Review Focus 3):
// neither's order, match or last status reads the other's.
func TestTwoSourcesWithTheSameInstanceIDAreKeptApart(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.Now()
	paging := func(e *mapping.Event) *mapping.Event { e.Source, e.Mapping = "paging", "pages"; return e }
	x := ingest(h, trigger("a1", "i", "d", item.Firing, t0.Add(2*time.Minute))).Item

	older := ingest(h, paging(trigger("p1", "i", "d", item.Firing, t0)))
	resolvedX := ingest(h, trigger("a2", "i", "d", item.Resolved, t0.Add(3*time.Minute)))
	resolvedY := ingest(h, paging(trigger("p2", "i", "d", item.Resolved, t0)))

	assert.Equal(t, app.IngestOpened, older.Outcome, "not late against alerting's status, nor matched to its item")
	assert.NotEqual(t, x, older.Item)
	assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: x}, resolvedX)
	assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: older.Item}, resolvedY, "at the time of its own firing")
	assert.Equal(t, "source:paging", h.state(older.Item).Opened.By)
}

// exampleAlert is one alert of the example alerting source's body, as its
// mapping reads it.
func exampleAlert(status, startsAt, endsAt string) []byte {
	return fmt.Appendf(nil, `{"alerts":[{"fingerprint":"f1","status":%q,"startsAt":%q,"endsAt":%q,"labels":{"service":"checkout","rule_id":"r1"},"annotations":{"summary":"checkout fails"}}]}`,
		status, startsAt, endsAt)
}

// exampleMapping is the example deployment's alerting mapping, without its
// checks, as Plan 7c-2 runs them.
func exampleMapping(t *testing.T) (*config.TriggerMapping, *config.Revision) {
	t.Helper()
	snap, err := dirsource.New("../../../examples/config").Read(t.Context(), "")
	require.NoError(t, err)
	conditions, err := celeval.New()
	require.NoError(t, err)
	rev, err := config.Build(snap.Files, snap.Commit, conditions)
	require.NoError(t, err)
	m := rev.TriggerMapping("alerting")
	require.NotNil(t, m)
	m.Spec.Checks = nil
	return m, rev
}

// evaluated is the one event of body, as the example mapping evaluates it on
// receipt at received.
func evaluated(t *testing.T, body []byte, received time.Time) *mapping.Event {
	t.Helper()
	m, rev := exampleMapping(t)
	events, err := mapping.Evaluate(m, body, received, mapping.Redactor(rev, m))
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Empty(t, events[0].Unroutable)
	return &events[0]
}

// A firing dated an hour ahead is clamped to its receipt plus the skew of 2
// minutes (Review Focus 2), as its record and row store it: a resolution
// dated 5 minutes after the receipt, clamped to the same time, applies,
// resolving the firing; one dated a minute after it is late.
func TestAFutureDatedFiringIsClampedAndItsResolutionStillApplies(t *testing.T) {
	for name, tc := range map[string]struct {
		endsAt time.Duration
		want   app.IngestOutcome
	}{
		"a resolution dated after the clamp":  {5 * time.Minute, app.IngestAppended},
		"a resolution dated before the clamp": {time.Minute, app.IngestLate},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			received := h.clock.Now()
			ahead := received.Add(time.Hour).Format(time.RFC3339)
			firing := evaluated(t, exampleAlert("firing", ahead, ahead), received)
			opened := ingest(h, firing)
			require.Equal(t, app.IngestOpened, opened.Outcome)
			clamped := received.Add(mapping.Skew)
			assert.True(t, clamped.Equal(triggers(h, opened.Item)[0].OccurredAt), "stored at the receipt plus the skew")
			assert.True(t, clamped.Equal(rowOf(h, firing.EventID).OccurredAt))

			resolved := evaluated(t, exampleAlert("resolved", ahead, received.Add(tc.endsAt).Format(time.RFC3339)), received)
			out := ingest(h, resolved)

			assert.Equal(t, tc.want, out.Outcome)
			assert.Equal(t, opened.Item, out.Item)
			assert.Equal(t, tc.want == app.IngestAppended, !h.state(opened.Item).TriggerFiring(), "resolved once applied")
		})
	}
}

// A trigger resolved after its run started stops the run at its round's end
// (design D12): the visible run's round ends trigger_resolved. A run whose
// trigger resolved before its run_created runs to its budget (ruling 10),
// and a person's extension of the stopped run runs a round that does not end
// trigger_resolved.
func TestATriggerResolvedAfterTheRunStartedStopsItAtTheRoundsEnd(t *testing.T) {
	t.Run("resolved during the run", func(t *testing.T) {
		h := newHarness(t)
		t0 := h.clock.Now()
		x := ingest(h, trigger("f", "i", "d", item.Firing, t0)).Item
		require.NotNil(t, h.state(x).Run)
		require.Equal(t, app.IngestAppended, ingest(h, trigger("r", "i", "d", item.Resolved, t0.Add(time.Second))).Outcome)

		h.round()

		s := h.state(x)
		require.NotNil(t, s.Run.Stopped)
		assert.Equal(t, item.StopTriggerResolved, s.Run.Stopped.Reason)
		assert.Equal(t, item.StopTriggerResolved, s.Run.Rounds[0].Ended.Decision.Stop)

		_, err := h.c.Extend(t.Context(), x, rounds(1), extending("extend-1"))
		require.NoError(t, err)
		require.Len(t, h.state(x).Run.Rounds, 2, "the extension resumed the run")
		h.round()

		ended := h.state(x).Run.Rounds[1].Ended
		require.NotNil(t, ended)
		assert.NotEqual(t, item.StopTriggerResolved, ended.Decision.Stop, "a run resumed since runs as asked")
	})
	t.Run("resolved before the run's creation", func(t *testing.T) {
		h := newHarness(t, func(o *app.Options) { o.Limit = 1 })
		busy(h)
		person := h.open("payments fail")
		t0 := h.clock.Now()
		x := ingest(h, trigger("f", "i", "d", item.Firing, t0)).Item
		require.Equal(t, item.StatusQueued, h.state(x).Status())
		require.Equal(t, app.IngestAppended, ingest(h, trigger("r", "i", "d", item.Resolved, t0.Add(time.Second))).Outcome)
		_, err := h.c.Close(t.Context(), person, closing("close-1"))
		require.NoError(t, err)
		require.NotNil(t, h.state(x).Run, "it started once the slot freed")

		for range 3 {
			h.round()
		}

		s := h.state(x)
		require.NotNil(t, s.Run.Stopped)
		assert.Equal(t, item.StopBudgetExhausted, s.Run.Stopped.Reason, "it runs to its budget")
		assert.Len(t, s.Run.Rounds, 3)
	})
}

// The daily cap stops a round before its trigger's resolution does (spec 5.6;
// design D12): at the cap the round ends cut.
func TestTheDailyCapStopsARoundBeforeATriggersResolution(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.Now()
	x := ingest(h, trigger("f", "i", "d", item.Firing, t0)).Item
	require.Equal(t, app.IngestAppended, ingest(h, trigger("r", "i", "d", item.Resolved, t0.Add(time.Second))).Outcome)
	h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })

	h.round()

	s := h.state(x)
	require.NotNil(t, s.Run.Stopped)
	assert.Equal(t, item.StopCut, s.Run.Stopped.Reason)
}

// Each matched event arms the item's auto-close again, or cancels it (design
// D5): an item awaiting its close, its trigger firing, has no timer; its
// resolution arms one at the resolution's record plus the delay; a new
// firing cancels it.
func TestEachMatchedEventRearmsTheAutoClose(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.Now()
	x := ingest(h, trigger("f1", "i", "d", item.Firing, t0)).Item
	h.round()
	require.Equal(t, item.StatusAwaitingClose, h.state(x).Status())
	require.True(t, h.state(x).TriggerFiring())
	h.clock.Advance(time.Minute)

	require.Equal(t, app.IngestAppended, ingest(h, trigger("r1", "i", "d", item.Resolved, h.clock.Now())).Outcome)
	due := lastRecord(h, x).At.Add(closesAfter)

	assert.Contains(t, h.clock.armed(), due, "armed at the resolution plus its delay")
	h.clock.Advance(time.Minute)
	require.Equal(t, app.IngestAppended, ingest(h, trigger("f2", "i", "d", item.Firing, h.clock.Now())).Outcome)
	assert.NotContains(t, h.clock.armed(), due, "a firing cancels it")
	h.clock.Advance(closesAfter)
	assert.Equal(t, item.StatusAwaitingClose, h.state(x).Status(), "it never closes while its trigger fires")
}

// A fenced coordinator answers ErrNotHeld, and writes nothing.
func TestIngestAnswersNotHeldOnAFencedCoordinator(t *testing.T) {
	h := newHarness(t)
	h.c.Fence()
	e := trigger("e1", "i1", "d1", item.Firing, h.clock.Now())

	_, err := h.c.Ingest(t.Context(), &app.IngestInput{Event: e, ID: inboundID(e)})

	require.ErrorIs(t, err, app.ErrNotHeld)
	assert.Empty(t, h.records.inbound())
	assert.Empty(t, active(h))
}

// loadsOf has the loads of item id fail with err while fail says so.
func loadsOf(h *harness, id item.ID, fail func() error) {
	h.records.loads(func(records []item.Record) error {
		if len(records) > 0 && records[0].Item == id {
			return fail()
		}
		return nil
	})
}

// An inbound opening whose answer is lost, and whose lookup failed too, is
// matched, never opened again (R133; I5a): the event answers the error, the
// item pending reload; a second event of its dedup key, while the reload
// fails, answers the error and writes nothing; once the item loads, at the
// event's start, or through LastItem's load once it left the account, the
// event is appended to it, and one item holds the key.
func TestAnOpeningWhoseLookupFailedIsMatchedNotOpenedAgain(t *testing.T) {
	for name, recover := range map[string]func(h *harness, x item.ID){
		"its reload at the next event": func(h *harness, x item.ID) { h.records.loads(nil) },
		"LastItem's load once it left the account": func(h *harness, x item.ID) {
			var n atomic.Int32
			loadsOf(h, x, func() error {
				if n.Add(1) == 1 {
					return fmt.Errorf("%w: a body that does not decode", item.ErrInvalid)
				}
				return nil
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			t0 := h.clock.Now()
			first := trigger("e1", "i1", "d", item.Firing, t0)
			x := inboundID(first)
			h.records.losesAnswers(once(errStore))
			loadsOf(h, x, func() error { return errStore })

			_, err := h.c.Ingest(t.Context(), &app.IngestInput{Event: first, ID: x})
			require.ErrorIs(t, err, errStore)
			require.Len(t, rowsOf(h, "alerting", "e1"), 1, "the opening landed")
			second := trigger("e2", "i2", "d", item.Firing, t0)
			_, err = h.c.Ingest(t.Context(), &app.IngestInput{Event: second, ID: inboundID(second)})
			require.ErrorIs(t, err, errStore, "the reload fails")
			assert.Empty(t, rowsOf(h, "alerting", "e2"), "nothing written")
			recover(h, x)

			out := ingest(h, second)

			assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: x}, out)
			assert.Equal(t, []item.ID{x}, active(h), "one item holds the key")
		})
	}
}

// An append whose answer was lost is reloaded before the next match (R133;
// I5b): a correlated event's append lands with its answer lost, its item
// pending; the instance's resolution, sent past the correlation window,
// finds the item by its instance once reloaded, and is appended, never put
// unmatched.
func TestAnAppendWhoseAnswerWasLostIsReloadedBeforeTheNextMatch(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.Now()
	x := ingest(h, correlated("a", "ia", "ka", item.Firing, t0, "checkout", time.Second)).Item
	h.records.losesAnswers(once(errStore))
	b := correlated("b1", "ib", "kb", item.Firing, t0, "checkout", time.Second)
	_, err := h.c.Ingest(t.Context(), &app.IngestInput{Event: b, ID: inboundID(b)})
	require.ErrorIs(t, err, errStore)
	h.clock.Advance(2 * time.Second)
	require.Less(t, 2*time.Second, h.opts.Retry, "the item's retry has not fired")

	out := ingest(h, correlated("b2", "ib", "kb", item.Resolved, h.clock.Now(), "checkout", time.Second))

	assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: x}, out)
}

// A pending item's retry clears its mark (the plan's): the retry's advance
// loads the item, and the next event of its source reads no store for it.
func TestAPendingItemsRetryClearsItsMark(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.Now()
	x := ingest(h, trigger("a", "ia", "ka", item.Firing, t0)).Item
	h.records.losesAnswers(once(errStore))
	b := trigger("b", "ia", "ka", item.Resolved, t0.Add(time.Second))
	_, err := h.c.Ingest(t.Context(), &app.IngestInput{Event: b, ID: inboundID(b)})
	require.ErrorIs(t, err, errStore)

	h.clock.Advance(h.opts.Retry)
	var loads atomic.Int32
	loadsOf(h, x, func() error { loads.Add(1); return nil })
	unroutable := trigger("c", "", "", item.Firing, h.clock.Now())
	unroutable.Unroutable = "missing:instanceId"
	require.Equal(t, app.IngestUnroutable, ingest(h, unroutable).Outcome)

	assert.Zero(t, loads.Load(), "no load of the item")
	assert.Equal(t, app.IngestRepeat, ingest(h, b).Outcome, "its append landed")
}

// An event whose row the store finds held only as it writes, as when an
// earlier try's commit landed after the event's Seen, is a repeat (design
// D7 step 1), and nothing more is written. (The plan's.)
func TestAnEventSeenOnlyAtItsWriteIsARepeat(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.Now()
	x := ingest(h, trigger("e0", "i1", "d1", item.Firing, t0))
	e := trigger("e1", "i1", "d1", item.Resolved, t0.Add(time.Second))
	kinds := h.kinds(x.Item)
	var landed atomic.Bool
	h.records.set(func(int) error {
		if landed.CompareAndSwap(false, true) {
			return h.records.Records.Put(t.Context(), &app.InboundEvent{Source: "alerting", EventID: "e1", Outcome: app.InboundRefused, Reason: "earlier"})
		}
		return nil
	})

	out := ingest(h, e)

	assert.Equal(t, app.IngestResult{Outcome: app.IngestRepeat}, out)
	assert.Equal(t, kinds, h.kinds(x.Item), "nothing more is recorded")
}

// An event whose request ends while its write is in flight is still taken
// (fix round 1, I-1): Ingest's store calls run apart from the request,
// bounded by the write timeout and ended by the holding, so that a commit
// never lands after an answer that says it failed. (The plan's.)
func TestAnEventWhoseRequestEndsDuringItsWriteIsStillTaken(t *testing.T) {
	h := newHarness(t)
	e := trigger("e1", "i1", "d1", item.Firing, h.clock.Now())
	letGo := h.records.hang()
	ctx, cancel := context.WithCancel(t.Context())
	type answer struct {
		out app.IngestResult
		err error
	}
	answered := make(chan answer, 1)
	go func() {
		out, err := h.c.Ingest(ctx, &app.IngestInput{Event: e, ID: inboundID(e)})
		answered <- answer{out, err}
	}()
	require.Eventually(t, func() bool { return h.records.waiting.Load() == 1 }, 5*time.Second, time.Millisecond)

	cancel()
	letGo()
	a := <-answered

	require.NoError(t, a.err)
	assert.Equal(t, app.IngestResult{Outcome: app.IngestOpened, Item: inboundID(e)}, a.out)
	assert.Len(t, rowsOf(h, "alerting", "e1"), 1)
}

// An inbound opening whose commit lands only after its lookup found no
// opening is pending reload, its retry armed (fix round 1, I-1): the
// instance's resolution finds it by its instance once reloaded, never put
// unmatched, and its retry starts it. (The plan's.)
func TestAnInboundOpeningThatLandsAfterItsLookupIsReloaded(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.Now()
	e := trigger("e1", "i1", "d1", item.Firing, t0)
	x := inboundID(e)
	h.records.landsLater(once(errStore))
	_, err := h.c.Ingest(t.Context(), &app.IngestInput{Event: e, ID: x})
	require.ErrorIs(t, err, errStore)
	h.records.landsLater(nil)
	require.NoError(t, h.records.land())
	require.Equal(t, item.KindOpened, h.kinds(x)[0], "the opening landed after its answer")

	out := ingest(h, trigger("r1", "i1", "d1", item.Resolved, t0.Add(time.Second)))
	assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: x}, out)

	h.clock.Advance(h.opts.Retry)
	assert.NotNil(t, h.state(x).Started, "its retry started it")
}

// An event behind an item's own fold, as when the store's rows of its
// instance are gone, is late (fix round 1, I-2): the order reads the later of
// the store's last status and the account items' fold, so the event is
// appended, not applied, rather than refused by the fold on every retry.
// (The plan's.)
func TestAnEventBehindAnItemsOwnFoldIsLate(t *testing.T) {
	h := newHarness(t)
	t0 := h.clock.Now()
	x := ingest(h, trigger("e0", "i1", "d1", item.Firing, t0)).Item
	appendRaw(t, h, x, item.Body{V: item.Version, Triggered: &item.Triggered{
		Source: "alerting", EventID: "e-gone", InstanceID: "i1", DedupKey: "d1", Status: item.Resolved, OccurredAt: t0.Add(2 * time.Minute), Applied: true,
	}})
	h.c.AdvanceForTest(t.Context(), x)

	out := ingest(h, trigger("e1", "i1", "d1", item.Firing, t0.Add(time.Minute)))

	assert.Equal(t, app.IngestResult{Outcome: app.IngestLate, Item: x}, out)
	tr := triggers(h, x)
	assert.False(t, tr[len(tr)-1].Applied)
}

// An opening whose derived id holds records that do not fold is refused
// item_unreadable (fix round 1, M-1), as LastItem's are, never answered 503
// on every retry. (The plan's.)
func TestAnOpeningOverAnUnreadableItemIsRefused(t *testing.T) {
	h := newHarness(t)
	e := trigger("e1", "i1", "d1", item.Firing, h.clock.Now())
	x := inboundID(e)
	_, err := h.c.Open(t.Context(), byKey(x, "sha256:admin", "checkout fails"))
	require.NoError(t, err)
	loadsOf(h, x, func() error { return fmt.Errorf("%w: a body that does not decode", item.ErrInvalid) })

	out := ingest(h, e)

	assert.Equal(t, app.IngestResult{Outcome: app.IngestRefused, Reason: "item_unreadable"}, out)
}

// An inbound opening whose append finds its row held is a repeat, and its
// opening looks nothing up and logs no failed step (fix round 1, M-3). (The
// plan's.)
func TestAnOpeningWhoseRowIsHeldLooksNothingUp(t *testing.T) {
	logs := &syncBuffer{}
	h := newHarness(t, logTo(logs))
	e := trigger("e1", "i1", "d1", item.Firing, h.clock.Now())
	var landed atomic.Bool
	h.records.set(func(int) error {
		if landed.CompareAndSwap(false, true) {
			return h.records.Records.Put(t.Context(), &app.InboundEvent{Source: "alerting", EventID: "e1", Outcome: app.InboundRefused, Reason: "earlier"})
		}
		return nil
	})

	out := ingest(h, e)

	assert.Equal(t, app.IngestResult{Outcome: app.IngestRepeat}, out)
	assert.Empty(t, logged(t, logs.String(), "a step of a work item failed"))
}
