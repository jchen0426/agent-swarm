package item_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

const alerts = "alerts"

// event is an applied trigger event of the alerts source on instance inst,
// keyed dedup, reporting status at occurred.
func event(inst, dedup string, status item.TriggerStatus, occurred time.Time) *item.Triggered {
	return &item.Triggered{
		Source: alerts, EventID: "ev-" + inst + "-" + string(status) + "-" + occurred.Format(time.RFC3339),
		InstanceID: inst, DedupKey: dedup, Status: status, OccurredAt: occurred, Applied: true,
	}
}

// late is event, not applied: evidence that came after its instance's later
// status.
func late(inst, dedup string, status item.TriggerStatus, occurred time.Time) *item.Triggered {
	e := event(inst, dedup, status, occurred)
	e.Applied = false
	return e
}

// trigger is the alerts source's trigger of an inbound opening, keyed d1.
func trigger() *item.Trigger {
	return &item.Trigger{Source: alerts, DedupKey: "d1", CorrelationKey: "c"}
}

// inboundOpened is the opening of an item by the alerts source, its trigger
// set.
func inboundOpened() *item.Opened {
	return &item.Opened{Type: "incident", Title: "checkout fails", Source: alerts, Arm: item.ArmVisible, How: item.NoWindow, Trigger: trigger()}
}

// newInbound is a life whose item the alerts source opened at t0, followed
// by its trigger's applied firing, instance i1, keyed d1, correlated c: it
// waits in the queue.
func newInbound(t *testing.T) *life {
	t.Helper()
	l := &life{t: t, s: &item.State{}, now: t0}
	l.write("", item.Body{Opened: inboundOpened()})
	first := event("i1", "d1", item.Firing, t0)
	first.CorrelationKey = "c"
	l.write("", item.Body{Triggered: first})
	return l
}

// trig records e on the item at d after t0.
func (l *life) trig(d time.Duration, e *item.Triggered) {
	l.t.Helper()
	l.now = t0.Add(d)
	l.write("", item.Body{Triggered: e})
}

// An item's instances are listed with their last applied status and when it
// began, by source, then by id; a late event applies nothing, and its
// instance is not listed. An item with no trigger lists none.
func TestAnItemsInstancesAreListedWithTheirLastStatus(t *testing.T) {
	l := newInbound(t)
	paged := event("p1", "d9", item.Firing, t0.Add(time.Minute))
	paged.Source = "pager"
	l.trig(time.Minute, paged)
	l.trig(2*time.Minute, event("i3", "d1", item.Firing, t0.Add(2*time.Minute)))
	l.trig(3*time.Minute, event("i1", "d1", item.Resolved, t0.Add(3*time.Minute)))
	l.trig(4*time.Minute, late("i2", "d1", item.Firing, t0.Add(-time.Hour)))

	assert.Equal(t, []item.TriggerInstance{
		{Source: alerts, ID: "i1", Status: item.Resolved, At: t0.Add(3 * time.Minute)},
		{Source: alerts, ID: "i3", Status: item.Firing, At: t0.Add(2 * time.Minute)},
		{Source: "pager", ID: "p1", Status: item.Firing, At: t0.Add(time.Minute)},
	}, l.s.Instances())
	assert.Empty(t, newOpened(t).s.Instances(), "a person's item has no trigger")
}

// An item_triggered is admitted while the item is not closed (design D5):
// queued, investigating and awaiting its close; a closed item refuses it.
func TestAnItemTriggeredIsAdmittedWhileTheItemIsNotClosed(t *testing.T) {
	queued := newOpened(t)
	investigating := newLife(t, created())
	awaiting := newOpened(t)
	awaiting.write("", item.Body{AwaitingClose: notStaffed(item.CauseDailyCap)})
	closed := newOpened(t)
	closed.write("", item.Body{Closed: closedBy()})

	for _, c := range []struct {
		name  string
		l     *life
		admit bool
	}{
		{"queued", queued, true}, {"investigating", investigating, true}, {"awaiting its close", awaiting, true}, {"closed", closed, false},
	} {
		err := c.l.try("", item.Body{Triggered: event("i1", "d1", item.Firing, t0)})
		if c.admit {
			require.NoError(t, err, c.name)
		} else {
			require.ErrorIs(t, err, item.ErrInvalid, c.name)
		}
	}
}

// An item_triggered's body names its source, its event, its instance, its
// dedup key and a known status; its Resume is empty, resumed, or declined
// with a code, and only on an applied firing while the item awaits its close.
func TestAnItemTriggeredsBodyIsWhole(t *testing.T) {
	ok := event("i1", "d1", item.Firing, t0)
	for name, change := range map[string]func(*item.Triggered){
		"no source":           func(e *item.Triggered) { e.Source = "" },
		"no event id":         func(e *item.Triggered) { e.EventID = "" },
		"no instance":         func(e *item.Triggered) { e.InstanceID = "" },
		"no dedup key":        func(e *item.Triggered) { e.DedupKey = "" },
		"an unknown status":   func(e *item.Triggered) { e.Status = "pending" },
		"an unknown resume":   func(e *item.Triggered) { e.Resume = "maybe" },
		"a resume while open": func(e *item.Triggered) { e.Resume = "resumed" },
	} {
		e := *ok
		change(&e)
		require.ErrorIs(t, newOpened(t).try("", item.Body{Triggered: &e}), item.ErrInvalid, name)
	}

	awaiting := func() *life {
		l := newOpened(t)
		l.write("", item.Body{AwaitingClose: notStaffed(item.CauseDailyCap)})
		return l
	}
	for resume, admit := range map[string]bool{
		"resumed": true, "declined:limit_reached": true, "declined:": false, "declined:Limit": false, "declined:9a": false,
		"declined:" + strings.Repeat("a", 32): true, "declined:" + strings.Repeat("a", 33): false,
	} {
		e := *ok
		e.Resume = resume
		err := awaiting().try("", item.Body{Triggered: &e})
		if admit {
			require.NoError(t, err, resume)
		} else {
			require.ErrorIs(t, err, item.ErrInvalid, resume)
		}
	}
	for name, e := range map[string]*item.Triggered{
		"late":     late("i1", "d1", item.Firing, t0),
		"resolved": event("i1", "d1", item.Resolved, t0),
	} {
		e.Resume = "resumed"
		require.ErrorIs(t, awaiting().try("", item.Body{Triggered: e}), item.ErrInvalid, name)
	}
}

// An inbound opening is followed, as its second record, by its trigger's
// applied firing (R126): an opening with a Trigger and then anything else is
// refused, and so is an item_triggered of another source or dedup key, or
// late, or resolved, as is a trigger that names no dedup key; item_opened,
// item_triggered, item_linked and item_awaiting_close fold whole.
func TestAnInboundOpeningIsFollowedByItsTrigger(t *testing.T) {
	opened := func() *life {
		l := &life{t: t, s: &item.State{}, now: t0}
		l.write("", item.Body{Opened: inboundOpened()})
		return l
	}
	require.ErrorIs(t, opened().try("", item.Body{AwaitingClose: notStaffed(item.CauseDailyCap)}), item.ErrInvalid, "the trigger first")
	bare := inboundOpened()
	bare.Trigger = &item.Trigger{Source: alerts}
	require.ErrorIs(t, (&life{t: t, s: &item.State{}, now: t0}).try("", item.Body{Opened: bare}), item.ErrInvalid, "a trigger with no dedup key")

	// fires is the opening's own firing: its trigger's source, dedup key and
	// correlation key.
	fires := func(dedup string) *item.Triggered {
		e := event("i1", dedup, item.Firing, t0)
		e.CorrelationKey = "c"
		return e
	}
	other := fires("d1")
	other.Source = "pager"
	uncorrelated := fires("d1")
	uncorrelated.CorrelationKey = ""
	correlatedElsewhere := fires("d1")
	correlatedElsewhere.CorrelationKey = "c2"
	lateOne := fires("d1")
	lateOne.Applied = false
	resolved := fires("d1")
	resolved.Status = item.Resolved
	for name, e := range map[string]*item.Triggered{
		"another source":          other,
		"another dedup":           fires("d2"),
		"no correlation key":      uncorrelated,
		"another correlation key": correlatedElsewhere,
		"late":                    lateOne,
		"resolved":                resolved,
	} {
		require.ErrorIs(t, opened().try("", item.Body{Triggered: e}), item.ErrInvalid, name)
	}

	l := opened()
	l.write("", item.Body{Triggered: fires("d1")})
	link := item.Record{Seq: 3, ID: "rec-3", Item: "item-1", Incident: "inc-2", At: l.now, Body: item.Body{
		V: item.Version, Linked: &item.Linked{Left: "inc-1", With: "item-0", By: "source:alerts", Request: item.Request{Key: "sha256:0a", Digest: "sha256:0a"}},
	}}
	require.NoError(t, l.s.Apply(&link))
	l.records = append(l.records, link)
	awaiting := item.Record{Seq: 4, ID: "rec-4", Item: "item-1", Incident: "inc-2", At: l.now, Body: item.Body{V: item.Version, AwaitingClose: notStaffed(item.CauseDailyCap)}}
	require.NoError(t, l.s.Apply(&awaiting))
	l.records = append(l.records, awaiting)

	s, err := item.Fold(l.records)
	require.NoError(t, err)
	assert.Equal(t, item.StatusAwaitingClose, s.Status())
	assert.Equal(t, item.IncidentID("inc-2"), s.Incident)
	assert.True(t, s.TriggerFiring())
}

// An inbound opening's trigger is of the opening's own source: a source opens
// items for itself alone.
func TestAnInboundOpeningsTriggerIsOfItsOwnSource(t *testing.T) {
	o := inboundOpened()
	o.Trigger.Source = "pager"
	require.ErrorIs(t, (&life{t: t, s: &item.State{}, now: t0}).try("", item.Body{Opened: o}), item.ErrInvalid)
}

// The fold keeps every dedup key, instance and correlation key an item took,
// applied or late, each under its source alone (design D5, R111).
func TestTheFoldKeepsEveryKeyAndInstanceAnItemTook(t *testing.T) {
	l := newInbound(t)
	l.trig(time.Minute, late("i2", "d2", item.Firing, t0.Add(-time.Hour)))

	assert.True(t, l.s.HoldsDedupKey(alerts, "d1"))
	assert.True(t, l.s.HoldsDedupKey(alerts, "d2"))
	assert.True(t, l.s.HoldsInstance(alerts, "i1"))
	assert.True(t, l.s.HoldsInstance(alerts, "i2"))
	assert.True(t, l.s.HoldsCorrelationKey(alerts, "c"))
	assert.False(t, l.s.HoldsDedupKey("pager", "d1"), "another source")
	assert.False(t, l.s.HoldsInstance("pager", "i1"), "another source")
	assert.False(t, l.s.HoldsCorrelationKey("pager", "c"), "another source")
	assert.False(t, l.s.HoldsDedupKey(alerts, "i1"), "an instance is no dedup key")
	assert.False(t, l.s.HoldsCorrelationKey(alerts, "d1"), "a dedup key is no correlation key")
	st, at, ok := l.s.Instance(alerts, "i1")
	assert.True(t, ok)
	assert.Equal(t, item.Firing, st)
	assert.Equal(t, t0, at)
	_, _, ok = l.s.Instance(alerts, "i2")
	assert.False(t, ok, "a late event applies nothing")
}

// An applied trigger event follows its instance's order on the item (R125,
// design D7 step 3): not later than its instance's last applied status, it is
// refused, at equal time a resolution being later than a firing; a late event
// is evidence and folds whatever its time.
func TestAnAppliedTriggerFollowsItsInstancesOrderOnTheItem(t *testing.T) {
	t1 := t0.Add(time.Minute)
	l := newInbound(t)
	l.trig(time.Minute, event("i1", "d1", item.Firing, t1))

	require.ErrorIs(t, l.try("", item.Body{Triggered: event("i1", "d1", item.Firing, t0)}), item.ErrInvalid, "a firing at t0 after one at t1")
	require.ErrorIs(t, l.try("", item.Body{Triggered: event("i1", "d1", item.Firing, t1)}), item.ErrInvalid, "a firing at t1 after one at t1")
	require.NoError(t, l.try("", item.Body{Triggered: event("i1", "d1", item.Resolved, t1)}), "a resolved at t1 after a firing at t1")
	require.ErrorIs(t, l.try("", item.Body{Triggered: event("i1", "d1", item.Firing, t1)}), item.ErrInvalid, "a firing at t1 after a resolved at t1")
	require.NoError(t, l.try("", item.Body{Triggered: late("i1", "d1", item.Firing, t0)}), "a late event at t0")
	st, at, _ := l.s.Instance(alerts, "i1")
	assert.Equal(t, item.Resolved, st)
	assert.Equal(t, t1, at)
}

// The trigger fires while any instance's last applied status is a firing.
func TestTheTriggerFiresWhileAnyInstanceFires(t *testing.T) {
	l := newInbound(t)
	l.trig(time.Minute, event("i2", "d1", item.Firing, t0))
	l.trig(2*time.Minute, event("i1", "d1", item.Resolved, t0.Add(time.Minute)))
	assert.True(t, l.s.TriggerFiring(), "i2 fires")
	l.trig(3*time.Minute, event("i2", "d1", item.Resolved, t0.Add(time.Minute)))
	assert.False(t, l.s.TriggerFiring(), "both resolved")
}

// ResolvedAt is when the last firing instance resolved, by its record's time
// (design D5, R110): an item a person opened was resolved at its opening;
// while any instance fires, never; then the record time of the resolution of
// the last that fired, until an instance fires again.
func TestResolvedAtIsWhenTheLastFiringInstanceResolved(t *testing.T) {
	person := newOpened(t)
	at, ok := person.s.ResolvedAt()
	assert.True(t, ok)
	assert.Equal(t, person.s.OpenedAt, at)

	l := newInbound(t)
	at, ok = l.s.ResolvedAt()
	assert.False(t, ok, "firing")
	assert.Zero(t, at)
	l.trig(time.Minute, event("i2", "d1", item.Firing, t0))
	l.trig(2*time.Minute, event("i1", "d1", item.Resolved, t0.Add(time.Hour)))
	_, ok = l.s.ResolvedAt()
	assert.False(t, ok, "i2 fires")
	l.trig(3*time.Minute, event("i2", "d1", item.Resolved, t0))
	at, ok = l.s.ResolvedAt()
	assert.True(t, ok)
	assert.Equal(t, t0.Add(3*time.Minute), at, "r2, the record's time, not the event's")

	l.trig(4*time.Minute, event("i1", "d1", item.Firing, t0.Add(2*time.Hour)))
	_, ok = l.s.ResolvedAt()
	assert.False(t, ok, "fires again")
	l.trig(5*time.Minute, event("i1", "d1", item.Resolved, t0.Add(3*time.Hour)))
	at, ok = l.s.ResolvedAt()
	assert.True(t, ok)
	assert.Equal(t, t0.Add(5*time.Minute), at, "r3")
}

// An item whose trigger fires never closes by itself (review M6); a person
// closes it.
func TestAnItemWhoseTriggerFiresNeverClosesByItself(t *testing.T) {
	l := newInbound(t)
	l.write("", item.Body{AwaitingClose: notStaffed(item.CauseDailyCap)})

	require.ErrorIs(t, l.try("", item.Body{Closed: closedAuto(t0)}), item.ErrInvalid)
	require.NoError(t, l.try("", item.Body{Closed: closedBy()}))
}

// AutoCloseAt is never while the trigger fires (R110); once it resolved at r,
// it is the delay after the latest of r and the item's entry into
// awaiting_close.
func TestAutoCloseAtIsNeverWhileTheTriggerFires(t *testing.T) {
	const delay = 30 * time.Minute
	l := newInbound(t)
	l.now = t0.Add(time.Minute)
	l.write("", item.Body{AwaitingClose: notStaffed(item.CauseDailyCap)})
	at, ok := l.s.AutoCloseAt(delay, time.Time{})
	assert.False(t, ok)
	assert.Zero(t, at)

	l.trig(time.Hour, event("i1", "d1", item.Resolved, t0.Add(time.Hour)))
	at, ok = l.s.AutoCloseAt(delay, time.Time{})
	assert.True(t, ok)
	assert.Equal(t, t0.Add(time.Hour+delay), at, "after r, later than the entry")
	require.NoError(t, l.try("", item.Body{Closed: closedAuto(at)}), "closes by itself once resolved")

	early := newInbound(t)
	early.trig(time.Minute, event("i1", "d1", item.Resolved, t0.Add(time.Minute)))
	early.now = t0.Add(time.Hour)
	early.write("", item.Body{AwaitingClose: notStaffed(item.CauseDailyCap)})
	at, ok = early.s.AutoCloseAt(delay, time.Time{})
	assert.True(t, ok)
	assert.Equal(t, t0.Add(time.Hour+delay), at, "after the entry, later than r")
}

// firing is a firing resume of the run, as the coordinator records it: a
// zero amount, by the source, keyed by the event.
func firing() *item.Extended {
	return &item.Extended{Cause: item.ExtendFiring, By: "source:alerts", Request: item.Request{Key: "sha256:0f", Digest: "sha256:0f"}}
}

// A firing resume admits a zero amount on a stopped run whose item awaits its
// close, and resumes it; on a running run it is refused, as is one with an
// amount, an unknown cause, and a zero amount without a cause.
func TestAFiringResumeAdmitsAZeroAmountOnAStoppedRunOnly(t *testing.T) {
	stopped := func() *life {
		l := newLife(t, created())
		l.open()
		l.end(time.Minute, quiet())
		l.stopAndAwait()
		return l
	}
	l := stopped()
	require.NoError(t, l.try("run-1", item.Body{Extended: firing()}))
	assert.Equal(t, item.StatusInvestigating, l.s.Status())
	assert.Nil(t, l.s.Run.Stopped)
	assert.True(t, l.s.Run.Resumed())
	assert.Equal(t, created().Budget, l.s.Run.Budget(), "a firing resume adds nothing")

	running := newLife(t, created())
	withAmount := firing()
	withAmount.Amount = oneRound
	unknown := firing()
	unknown.Cause, unknown.Amount = "whim", oneRound
	noCause := firing()
	noCause.Cause = ""
	for name, c := range map[string]struct {
		l *life
		e *item.Extended
	}{
		"on a running run":       {running, firing()},
		"with an amount":         {stopped(), withAmount},
		"of an unknown cause":    {stopped(), unknown},
		"zero without its cause": {stopped(), noCause},
	} {
		require.ErrorIs(t, c.l.try("run-1", item.Body{Extended: c.e}), item.ErrInvalid, name)
	}
}

// A trigger resolves a run only when its resolution is later, by record
// sequence, than the run's latest start or resume (design D12, R127).
func TestTheTriggerResolvesARunOnlyAfterItsStartOrResume(t *testing.T) {
	person := newLife(t, created())
	assert.False(t, person.s.TriggerResolvedSince(), "a person's item")

	l := newInbound(t)
	l.trig(time.Minute, event("i1", "d1", item.Resolved, t0.Add(time.Minute)))
	l.start()
	l.write("run-1", item.Body{RunCreated: created()})
	assert.False(t, l.s.TriggerResolvedSince(), "resolved before the run's creation")

	l.trig(2*time.Minute, event("i1", "d1", item.Firing, t0.Add(2*time.Minute)))
	l.trig(3*time.Minute, event("i1", "d1", item.Resolved, t0.Add(3*time.Minute)))
	assert.True(t, l.s.TriggerResolvedSince(), "resolved after it")

	l.open()
	l.end(time.Minute, quiet())
	l.stopAndAwait()
	l.extend(oneRound, 0)
	assert.False(t, l.s.TriggerResolvedSince(), "after a later resume")
	l.trig(time.Hour, event("i1", "d1", item.Firing, t0.Add(time.Hour)))
	l.trig(2*time.Hour, event("i1", "d1", item.Resolved, t0.Add(2*time.Hour)))
	assert.True(t, l.s.TriggerResolvedSince(), "a new resolution")
}

// A firing resume declined since the run's stop is remembered (re-review
// N1), until the run stops again or a resume lands.
func TestAFiringResumeDeclinedSinceTheRunsStopIsRemembered(t *testing.T) {
	l := newInbound(t)
	l.start()
	l.write("run-1", item.Body{RunCreated: created()})
	l.open()
	l.end(time.Minute, quiet())
	l.stopAndAwait()
	assert.False(t, l.s.FiringResumeDeclined())

	declined := event("i1", "d1", item.Firing, t0.Add(time.Hour))
	declined.Resume = "declined:limit_reached"
	l.trig(time.Hour, declined)
	assert.True(t, l.s.FiringResumeDeclined())

	resumed := event("i1", "d1", item.Firing, t0.Add(2*time.Hour))
	resumed.Resume = "resumed"
	l.trig(2*time.Hour, resumed)
	assert.False(t, l.s.FiringResumeDeclined(), "a resume after it")
	l.write("run-1", item.Body{Extended: firing()})

	l.now = t0.Add(3 * time.Hour)
	l.halt(item.StopKilled, busy())
	l.await()
	again := event("i1", "d1", item.Firing, t0.Add(3*time.Hour))
	again.Resume = "declined:kill_switch_on"
	l.trig(3*time.Hour, again)
	require.True(t, l.s.FiringResumeDeclined(), "declined since the run's second stop")
	l.write("", item.Body{Closed: closedBy()})
	l.write("", item.Body{Reopened: reopened(&item.AwaitingClose{Reason: item.StopNotStaffed})})
	assert.True(t, l.s.FiringResumeDeclined(), "a close and a reopen stop no run")

	stops := newInbound(t)
	stops.start()
	stops.write("run-1", item.Body{RunCreated: created()})
	stops.open()
	stops.end(time.Minute, quiet())
	stops.stopAndAwait()
	d := event("i1", "d1", item.Firing, t0.Add(time.Hour))
	d.Resume = "declined:daily_cap_reached"
	stops.trig(time.Hour, d)
	stops.extend(oneRound, 0)
	require.True(t, stops.s.FiringResumeDeclined(), "the run runs, its stop's decline not cleared until it stops")
	stops.open()
	stops.end(time.Minute, quiet())
	st := stops.s.Run.Stop("")
	stops.write("run-1", item.Body{RunStopped: &st})
	assert.False(t, stops.s.FiringResumeDeclined(), "a later run_stopped clears it")
}

// A firing that would have tried a firing resume, landing after the run's
// stop with none tried, as before its item awaits its close, is remembered
// (fix round 1, M-1), until a resume is tried or the run stops again; a
// re-notification of an instance firing since before the stop is not.
func TestAFiringResumeMissedSinceTheRunsStopIsRemembered(t *testing.T) {
	l := newInbound(t)
	l.trig(time.Minute, event("i1", "d1", item.Firing, t0.Add(time.Minute)))
	l.start()
	l.write("run-1", item.Body{RunCreated: created()})
	l.open()
	l.end(time.Minute, quiet())
	st := l.s.Run.Stop("")
	l.write("run-1", item.Body{RunStopped: &st})
	l.trig(time.Hour, event("i1", "d1", item.Firing, t0.Add(time.Hour)))
	assert.False(t, l.s.FiringResumeMissed(), "an instance firing since before the stop")
	l.trig(2*time.Hour, event("i2", "d1", item.Firing, t0.Add(2*time.Hour)))
	assert.True(t, l.s.FiringResumeMissed(), "a new instance, before the item awaits its close")
	l.await()
	assert.True(t, l.s.FiringResumeMissed(), "still, once it awaits its close")

	declined := event("i1", "d1", item.Firing, t0.Add(3*time.Hour))
	declined.Resume = "declined:limit_reached"
	l.trig(3*time.Hour, declined)
	assert.False(t, l.s.FiringResumeMissed(), "a resume tried since")

	stops := newInbound(t)
	stops.start()
	stops.write("run-1", item.Body{RunCreated: created()})
	stops.open()
	stops.end(time.Minute, quiet())
	st = stops.s.Run.Stop("")
	stops.write("run-1", item.Body{RunStopped: &st})
	stops.trig(time.Hour, event("i9", "d1", item.Firing, t0.Add(time.Hour)))
	require.True(t, stops.s.FiringResumeMissed())
	stops.await()
	stops.extend(oneRound, 0)
	stops.open()
	stops.end(time.Minute, quiet())
	st = stops.s.Run.Stop("")
	stops.write("run-1", item.Body{RunStopped: &st})
	assert.False(t, stops.s.FiringResumeMissed(), "a later run_stopped clears it")
}

// A firing that would have tried a firing resume, landing after the run's
// last round decided its stop and before run_stopped, is remembered by the
// stop: the run neither saw it nor was stopped for it to resume. One that
// landed before the round ended, or a re-notification of an instance firing
// since before, is not.
func TestAFiringBetweenTheStopDecisionAndTheStopIsRemembered(t *testing.T) {
	window := newInbound(t)
	window.start()
	window.write("run-1", item.Body{RunCreated: created()})
	window.open()
	window.end(time.Minute, quiet())
	window.trig(time.Hour, event("i2", "d1", item.Firing, t0.Add(time.Hour)))
	require.False(t, window.s.FiringResumeMissed(), "the run has not stopped")
	st := window.s.Run.Stop("")
	window.write("run-1", item.Body{RunStopped: &st})
	assert.True(t, window.s.FiringResumeMissed(), "a new instance since the round's end")

	during := newInbound(t)
	during.start()
	during.write("run-1", item.Body{RunCreated: created()})
	during.open()
	during.trig(time.Minute, event("i2", "d1", item.Firing, t0.Add(time.Minute)))
	during.end(2*time.Minute, quiet())
	st = during.s.Run.Stop("")
	during.write("run-1", item.Body{RunStopped: &st})
	assert.False(t, during.s.FiringResumeMissed(), "a firing before the round's end")

	held := newInbound(t)
	held.start()
	held.write("run-1", item.Body{RunCreated: created()})
	held.open()
	held.end(time.Minute, quiet())
	held.trig(time.Hour, event("i1", "d1", item.Firing, t0.Add(time.Hour)))
	st = held.s.Run.Stop("")
	held.write("run-1", item.Body{RunStopped: &st})
	assert.False(t, held.s.FiringResumeMissed(), "an instance firing since before")
}

// The trigger's records round-trip at version 1 under their JSON names.
func TestTheTriggerRecordsRoundTripAtVersionOne(t *testing.T) {
	trig := item.Body{V: item.Version, Triggered: &item.Triggered{
		Source: alerts, EventID: "e1", InstanceID: "i1", DedupKey: "d1", CorrelationKey: "c", Status: item.Firing,
		OccurredAt: t0, Applied: true, Resume: "declined:limit_reached",
	}}
	raw, err := json.Marshal(trig)
	require.NoError(t, err)
	assert.JSONEq(t, `{"v":1,"triggered":{"source":"alerts","eventId":"e1","instanceId":"i1","dedupKey":"d1","correlationKey":"c",
		"status":"firing","occurredAt":"2026-10-18T09:00:00Z","applied":true,"resume":"declined:limit_reached"}}`, string(raw))
	assert.Equal(t, item.KindTriggered, trig.Kind())
	var back item.Body
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, trig, back)

	opened := item.Body{V: item.Version, Opened: &item.Opened{Type: "incident", Title: "t", Source: alerts, Arm: item.ArmVisible, How: item.NoWindow, Trigger: trigger()}}
	raw, err = json.Marshal(opened)
	require.NoError(t, err)
	assert.JSONEq(t, `{"v":1,"opened":{"type":"incident","title":"t","source":"alerts","arm":"visible","how":"no_window",
		"trigger":{"source":"alerts","dedupKey":"d1","correlationKey":"c"}}}`, string(raw))
	back = item.Body{}
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, opened, back)

	ext := item.Body{V: item.Version, Extended: firing()}
	raw, err = json.Marshal(ext)
	require.NoError(t, err)
	assert.JSONEq(t, `{"v":1,"extended":{"amount":{"wallClock":0,"tokens":0,"rounds":0},"cause":"firing","by":"source:alerts",
		"request":{"key":"sha256:0f","digest":"sha256:0f"}}}`, string(raw))
	back = item.Body{}
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, ext, back)
}
