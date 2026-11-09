package app_test

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// sha256Of is the request key of a platform act, sha256: and the hex of s
// (R120).
func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// appends has the records store note the kinds of each append from now on,
// by item, and returns them.
func appends(h *harness) func(id item.ID) [][]item.Kind {
	var mu sync.Mutex
	seen := map[item.ID][][]item.Kind{}
	h.records.refuses(func(id item.ID, drafts []item.Draft) error {
		kinds := make([]item.Kind, 0, len(drafts))
		for i := range drafts {
			kinds = append(kinds, drafts[i].Body.Kind())
		}
		mu.Lock()
		defer mu.Unlock()
		seen[id] = append(seen[id], kinds)
		return nil
	})
	return func(id item.ID) [][]item.Kind {
		mu.Lock()
		defer mu.Unlock()
		return seen[id]
	}
}

// resolvedAndStopped opens an incident by a firing of instance i, dedup key
// d, resolves the instance, and plays the round, whose run stops
// trigger_resolved: the item awaits its close, its trigger resolved.
func resolvedAndStopped(h *harness) item.ID {
	h.t.Helper()
	t0 := h.clock.Now()
	x := ingest(h, trigger("f1", "i", "d", item.Firing, t0)).Item
	require.Equal(h.t, app.IngestAppended, ingest(h, trigger("r1", "i", "d", item.Resolved, t0.Add(time.Second))).Outcome)
	h.round()
	s := h.state(x)
	require.NotNil(h.t, s.Run.Stopped)
	require.Equal(h.t, item.StopTriggerResolved, s.Run.Stopped.Reason)
	require.Equal(h.t, item.StatusAwaitingClose, s.Status())
	return x
}

// lastTriggered returns the item_triggered of event id on item x.
func lastTriggered(h *harness, x item.ID, id string) item.Triggered {
	h.t.Helper()
	all := triggers(h, x)
	for i := range all {
		if all[i].EventID == id {
			return all[i]
		}
	}
	require.FailNow(h.t, "no item_triggered of the event", id)
	return item.Triggered{}
}

// A firing of a resolved instance while its item awaits its close resumes
// the run at once (design D7 step 5; R112, R119, R120): item_triggered with
// Resume resumed, then run_extended of zero amount, cause firing, by the
// source, with the kill switch's latest row and R120's request, in one
// append with its row; the item investigates, and its board holds a resume
// entry with cause firing, then round 2.
func TestAFiringWhileAwaitingTheCloseResumesTheRunAtOnce(t *testing.T) {
	h := newHarness(t)
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	_, err = h.c.SetKillSwitch(t.Context(), false, turningOff(1))
	require.NoError(t, err)
	seq := h.c.Lifecycle().KillSwitch.Seq
	require.NotZero(t, seq)
	x := resolvedAndStopped(h)
	h.clock.Advance(time.Minute)
	of := appends(h)

	out := ingest(h, trigger("f2", "i", "d", item.Firing, h.clock.Now()))

	assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: x}, out)
	assert.Equal(t, item.ResumeResumed, lastTriggered(h, x, "f2").Resume)
	require.NotEmpty(t, of(x))
	assert.Equal(t, []item.Kind{item.KindTriggered, item.KindExtended}, of(x)[0], "in one append")
	e := lastRecordOf(h, x, func(b *item.Body) bool { return b.Extended != nil }).Body.Extended
	key := sha256Of("firing:f2")
	assert.Equal(t, &item.Extended{
		Cause: item.ExtendFiring, KillSwitchSeen: seq, By: "source:alerting",
		Request: item.Request{Key: key, Digest: key},
	}, e)
	assert.Equal(t, app.InboundAppended, rowOf(h, "f2").Outcome)
	requireResumedInto(t, h, x, 2)
	run := h.state(x).Run.ID
	resumes := resumeEntries(h, run)
	require.Len(t, resumes, 1)
	assert.Equal(t, &board.Resume{Cause: "firing"}, resumes[0])
	resumed := entryAt(h, run, func(b *board.Body) bool { return b.Resume != nil })
	second := entryAt(h, run, func(b *board.Body) bool { return b.Round != nil && b.Round.Round == 2 })
	assert.Less(t, resumed, second, "the resume entry, then the next round")
}

// A firing resume that would not open a round is declined with its code
// (design D7 step 5; R112): its item_triggered says declined:<code>, nothing
// more is recorded, and the item stays awaiting its close, its trigger
// firing, with no auto-close armed.
func TestAFiringResumeIsDeclinedWithItsCode(t *testing.T) {
	for _, c := range []struct {
		code     string
		opts     []func(*app.Options)
		instance string
		item     func(h *harness) item.ID
	}{
		{code: "kill_switch_on", item: func(h *harness) item.ID {
			x := resolvedAndStopped(h)
			h.c.SetKillSwitchViewForTest(app.KillSwitch{On: true, Seq: 1, OnSeq: 1})
			return x
		}},
		{code: "unsettled", item: func(h *harness) item.ID {
			x := resolvedAndStopped(h)
			unknownOn(h.t, h, false)
			return x
		}},
		{code: "daily_cap_reached", item: func(h *harness) item.ID {
			x := resolvedAndStopped(h)
			h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })
			return x
		}},
		{code: "no_agents_left", instance: "i2", item: noAgentKilledInbound},
		{code: "would_stop_at_once", instance: "i2", item: func(h *harness) item.ID {
			busy(h)
			x := ingest(h, trigger("f1", "i", "d", item.Firing, h.clock.Now())).Item
			for range 3 {
				h.round()
			}
			s := h.state(x)
			require.NotNil(h.t, s.Run.Stopped)
			require.Equal(h.t, item.StopBudgetExhausted, s.Run.Stopped.Reason)
			return x
		}},
		{code: "limit_reached", opts: []func(*app.Options){limit(1)}, item: limitReached},
		{code: "no_run", instance: "i2", item: func(h *harness) item.ID {
			h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })
			x := ingest(h, trigger("f1", "i", "d", item.Firing, h.clock.Now())).Item
			h.spend.set(func(s *spend) { s.day = 0 })
			s := h.state(x)
			require.Nil(h.t, s.Run, "opened not staffed")
			require.Equal(h.t, item.StatusAwaitingClose, s.Status())
			return x
		}},
	} {
		t.Run(c.code, func(t *testing.T) {
			h := newHarness(t, c.opts...)
			x := c.item(h)
			instance := c.instance
			if instance == "" {
				instance = "i"
			}
			h.clock.Advance(time.Minute)
			kinds := h.kinds(x)

			out := ingest(h, trigger("f2", instance, "d", item.Firing, h.clock.Now()))

			assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: x}, out)
			assert.Equal(t, item.ResumeDeclined+c.code, lastTriggered(h, x, "f2").Resume)
			assert.Equal(t, append(kinds, item.KindTriggered), h.kinds(x), "nothing more is recorded")
			s := h.state(x)
			assert.Equal(t, item.StatusAwaitingClose, s.Status())
			assert.True(t, s.TriggerFiring())
			assert.True(t, s.FiringResumeDeclined())
			_, armed := s.AutoCloseAt(closesAfter, time.Time{})
			assert.False(t, armed, "no auto-close is armed")
		})
	}
}

// noAgentKilledInbound has an inbound change, which no agent subscribes to,
// get its run, whose no_agents notice the board refuses, so that the run
// runs with no agent; then an on kills it, and an off follows. The item
// awaits its close, its instance i firing.
func noAgentKilledInbound(h *harness) item.ID {
	h.t.Helper()
	h.store.refuses(func(_ board.RunID, drafts []board.Draft) error {
		for i := range drafts {
			if n := drafts[i].Body.Notice; n != nil && n.Code == board.NoticeNoAgents {
				return errRefused
			}
		}
		return nil
	})
	e := trigger("f1", "i", "d", item.Firing, h.clock.Now())
	e.Type = "change"
	x := ingest(h, e).Item
	_, err := h.c.SetKillSwitch(h.t.Context(), true, killing(0))
	require.NoError(h.t, err)
	_, err = h.c.SetKillSwitch(h.t.Context(), false, turningOff(1))
	require.NoError(h.t, err)
	s := h.state(x)
	require.NotNil(h.t, s.Run.Stopped)
	require.Equal(h.t, item.StopKilled, s.Run.Stopped.Reason)
	require.Empty(h.t, s.Run.Agents())
	require.Equal(h.t, item.StatusAwaitingClose, s.Status())
	return x
}

// limitReached has, with a limit of 1, an inbound item stopped
// trigger_resolved and two items investigating, the limit and one; it
// returns the inbound item.
func limitReached(h *harness) item.ID {
	h.t.Helper()
	x := resolvedAndStopped(h)
	openLater(h, "payments fail")
	q := openLater(h, "orders fail")
	_, err := h.c.Promote(h.t.Context(), q, promoting("promote-1"))
	require.NoError(h.t, err)
	require.Equal(h.t, 2, h.c.Lifecycle().Investigating, "the limit and one")
	return x
}

// A declined resume is retried by the next applied firing of the instance,
// a re-notification with a later time and a new id (re-review N1; A7):
// declined limit_reached; a slot frees; the re-notification resumes.
func TestAReNotificationRetriesADeclinedResume(t *testing.T) {
	h := newHarness(t, limit(1))
	x := limitReached(h)
	h.clock.Advance(time.Minute)
	ingest(h, trigger("f2", "i", "d", item.Firing, h.clock.Now()))
	require.Equal(t, item.ResumeDeclined+app.CodeLimitReached, lastTriggered(h, x, "f2").Resume)
	var busyOne item.ID
	for _, id := range active(h) {
		if id != x && h.state(id).Status() == item.StatusInvestigating {
			busyOne = id
			break
		}
	}
	require.NotEmpty(t, busyOne)
	stopQuiet(h, busyOne)
	require.Equal(t, 1, h.c.Lifecycle().Investigating)
	h.clock.Advance(time.Minute)

	out := ingest(h, trigger("f3", "i", "d", item.Firing, h.clock.Now()))

	assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: x}, out)
	assert.Equal(t, item.ResumeResumed, lastTriggered(h, x, "f3").Resume)
	requireResumedInto(t, h, x, 2)
}

// A re-notification of an instance firing since before the stop, with no
// decline since the stop, tries no resume (design D7 step 5): its Resume is
// empty, and the run stays stopped.
func TestAReNotificationWithNoDeclineTriesNoResume(t *testing.T) {
	h := newHarness(t)
	x := ingest(h, trigger("f1", "i", "d", item.Firing, h.clock.Now())).Item
	h.round()
	require.Equal(t, item.StatusAwaitingClose, h.state(x).Status())
	require.True(t, h.state(x).TriggerFiring())
	h.clock.Advance(time.Minute)
	kinds := h.kinds(x)

	out := ingest(h, trigger("f2", "i", "d", item.Firing, h.clock.Now()))

	assert.Equal(t, app.IngestResult{Outcome: app.IngestAppended, Item: x}, out)
	assert.Empty(t, lastTriggered(h, x, "f2").Resume)
	assert.Equal(t, append(kinds, item.KindTriggered), h.kinds(x))
	s := h.state(x)
	assert.NotNil(t, s.Run.Stopped, "the run stays stopped")
	assert.Equal(t, item.StatusAwaitingClose, s.Status())
}

// A firing resume whose append landed with its answer lost, its lookup
// failing too, answers the error, and counts its item among the items
// investigating until its next load (7b-2's carry; R133): the item is
// pending reload, and on its retry it investigates in round 2.
func TestAFiringResumeWhoseOutcomeIsUnknownCountsItsItem(t *testing.T) {
	h := newHarness(t)
	x := resolvedAndStopped(h)
	require.Equal(t, 0, h.c.Lifecycle().Investigating)
	h.clock.Advance(time.Minute)
	e := trigger("f2", "i", "d", item.Firing, h.clock.Now())
	h.records.losesAnswers(once(errStore))
	loadsOf(h, x, func() error { return errStore })

	_, err := h.c.Ingest(t.Context(), &app.IngestInput{Event: e, ID: inboundID(e)})

	require.ErrorIs(t, err, errStore)
	records, err := h.records.Records.Load(t.Context(), x)
	require.NoError(t, err)
	require.Equal(t, item.KindExtended, records[len(records)-1].Body.Kind(), "it landed")
	assert.Equal(t, 1, h.c.Lifecycle().Investigating, "the item counts")
	h.records.loads(nil)
	h.clock.Advance(h.opts.Retry)
	requireResumedInto(t, h, x, 2)
	assert.Equal(t, 1, h.c.Lifecycle().Investigating)
}

// closedFor opens an incident by a firing of instance i, dedup key d,
// closes it, and lets hours pass; it returns the closed item.
func closedFor(h *harness, hours time.Duration) item.ID {
	h.t.Helper()
	p := ingest(h, trigger("f1", "i", "d", item.Firing, h.clock.Now())).Item
	_, err := h.c.Close(h.t.Context(), p, closing("close-1"))
	require.NoError(h.t, err)
	require.Equal(h.t, item.StatusClosed, h.state(p).Status())
	h.clock.Advance(hours * time.Hour)
	return p
}

// requireFlapLink fails unless records 1 to 3 of item n are its opening, its
// firing and its link into p's incident, by the source, with no reason and
// R120's request of event id, and n is in p's incident with it.
func requireFlapLink(t *testing.T, h *harness, n, p item.ID, id string) {
	t.Helper()
	records, err := h.records.Load(t.Context(), n)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(records), 3)
	assert.Equal(t, item.KindOpened, records[0].Body.Kind())
	assert.Equal(t, item.KindTriggered, records[1].Body.Kind())
	l := records[2].Body.Linked
	require.NotNil(t, l, "record 3 is item_linked")
	key := sha256Of("flap:" + id)
	assert.Equal(t, p, l.With)
	assert.Equal(t, records[0].Incident, l.Left)
	assert.Equal(t, "source:alerting", l.By)
	assert.Nil(t, l.Reason)
	assert.Equal(t, item.Request{Key: key, Digest: key}, l.Request)
	incident := h.state(p).Incident
	assert.Equal(t, incident, records[2].Incident, "it joins p's incident")
	assert.Equal(t, incident, h.state(n).Incident)
	items, err := h.records.IncidentItems(t.Context(), incident)
	require.NoError(t, err)
	assert.ElementsMatch(t, []item.ID{p, n}, items)
}

// A firing of a closed item's dedup key opens a new item linked into the
// closed item's incident, in its opening's append (design D8; R126): P
// closed 100 hours ago, past any window, and N's records 1 to 3 are
// item_opened, item_triggered and item_linked.
func TestAFiringAfterTheCloseOpensANewItemLinkedInOneAppend(t *testing.T) {
	h := newHarness(t)
	p := closedFor(h, 100)
	of := appends(h)

	out := ingest(h, trigger("f2", "i", "d", item.Firing, h.clock.Now()))

	require.Equal(t, app.IngestOpened, out.Outcome)
	n := out.Item
	assert.NotEqual(t, p, n)
	require.NotEmpty(t, of(n))
	assert.Equal(t, []item.Kind{item.KindOpened, item.KindTriggered, item.KindLinked}, of(n)[0], "one append opens it")
	requireFlapLink(t, h, n, p, "f2")
	assert.Equal(t, n, rowOf(h, "f2").Item)
}

// A flap opened not staffed still holds its link (design D8; R126): with
// the kill switch on, N's records 1 to 4 are item_opened, item_triggered,
// item_linked and item_awaiting_close, in one append.
func TestAFlapOpenedNotStaffedStillHoldsItsLink(t *testing.T) {
	h := newHarness(t)
	p := closedFor(h, 1)
	_, err := h.c.SetKillSwitch(t.Context(), true, killing(0))
	require.NoError(t, err)
	of := appends(h)

	out := ingest(h, trigger("f2", "i", "d", item.Firing, h.clock.Now()))

	require.Equal(t, app.IngestOpened, out.Outcome)
	opening := []item.Kind{item.KindOpened, item.KindTriggered, item.KindLinked, item.KindAwaitingClose}
	assert.Equal(t, [][]item.Kind{opening}, of(out.Item), "one append")
	assert.Equal(t, opening, h.kinds(out.Item))
	requireFlapLink(t, h, out.Item, p, "f2")
}

// The last item of a dedup key that is not closed never gives a flap link
// (the controller's ruling on Task 5): an item a person opened, named by the
// key's last row though it holds no key, matches nothing, and the firing
// opens an item in its own incident, linked to nothing.
func TestALastItemNotClosedGivesNoFlapLink(t *testing.T) {
	h := newHarness(t)
	person := h.open("payments fail")
	_, err := h.records.Records.AppendInbound(t.Context(), person, func([]item.Record) ([]item.Draft, *app.InboundEvent, error) {
		return nil, &app.InboundEvent{Source: "alerting", EventID: "e0", InstanceID: "i0", DedupKey: "d", Item: person, Outcome: app.InboundAppended}, nil
	})
	require.NoError(t, err)
	id, ok, err := h.records.LastItem(t.Context(), "alerting", "d")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, person, id)

	out := ingest(h, trigger("f1", "i", "d", item.Firing, h.clock.Now()))

	require.Equal(t, app.IngestOpened, out.Outcome)
	assert.NotContains(t, h.kinds(out.Item), item.KindLinked)
	assert.NotEqual(t, h.state(person).Incident, h.state(out.Item).Incident)
}
