//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// The scenarios of Plan 7c-1b, inbound events: a source posts signed groups
// of alerts to serve's inbound listener, which opens an item for the first
// and takes the rest by instance, dedup key or correlation, in order, as late
// or as repeats; the resolution of every alert ends the run at its round's
// end, and the item closes by itself; a firing resumes a stopped run, and a
// firing after the close opens an item linked to the closed one; and a
// request unsigned, stale, of an unknown source or of a bearer source is
// refused before anything is recorded. They read what a person reads: the
// listener's answers, the admin API's views (C4), the records and the boards
// (C5), and serve's log (C3).

const (
	// alertSource is the source of the alerts mapping, and alertSecret its
	// secret.
	alertSource = "alerting"
	alertSecret = "harness-alerting-secret"
	// pagerToken is the bearer source pager's token.
	pagerToken = "harness-pager-token"
	// inboundAutoClose is the delay after which an item of these scenarios
	// closes by itself once it awaits its close.
	inboundAutoClose = 5 * time.Second
)

// alerting is the configuration of these scenarios: the team of the others,
// its type's autoClose 5 s, and two trigger mappings: alerts, of the signed
// source alerting, and pager, of a source that authenticates by a bearer
// token, which the listener refuses until the onboarding build. Each reads
// the logs server's search as its identity source.
func alerting() harness.ConfigSpec {
	cfg := team(0)
	cfg.WorkType.AutoClose = inboundAutoClose
	cfg.Mappings = []harness.MappingSpec{
		{Name: "alerts", Source: alertSource, Secret: alertSecret, Identity: "logs.search"},
		{Name: "pager", Source: "pager", Bearer: true, Secret: pagerToken, Identity: "logs.search"},
	}
	return cfg
}

// gate holds the vendor's answers while it is shut, so that a round runs
// until the scenario has sent what it must send during it, and answers each
// turn's request with end_turn once it is open, or the test has ended.
type gate struct {
	ctx  context.Context
	mu   sync.Mutex
	open chan struct{}
	shut bool
}

// shutGate returns a gate that is shut.
func shutGate(t *testing.T) *gate {
	t.Helper()
	return &gate{ctx: t.Context(), open: make(chan struct{}), shut: true}
}

// hold shuts the gate, if it is open.
func (g *gate) hold() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.shut {
		g.open, g.shut = make(chan struct{}), true
	}
}

// release opens the gate, if it is shut: every request it holds is answered.
func (g *gate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.shut {
		close(g.open)
		g.shut = false
	}
}

func (g *gate) script(*harness.VendorRequest) harness.VendorReply {
	g.mu.Lock()
	open := g.open
	g.mu.Unlock()
	select {
	case <-open:
	case <-g.ctx.Done():
	}
	return harness.EndTurn()
}

// alertServing starts a serve with the inbound listener on the alerting
// configuration, its vendor behind a gate that is shut.
func alertServing(t *testing.T) (*harness.Env, *harness.Instance, *harness.Inbound, *gate) {
	t.Helper()
	d := quickRetry
	d.Inbound = true
	env, a := serving(t, alerting(), d)
	g := shutGate(t)
	env.Vendor().Script(g.script)
	in := a.Inbound()
	require.NotNil(t, in, "serve binds the inbound listener and writes its address")
	return env, a, in, g
}

// alert is one alert of the alerts mapping's shape (harness.MappingSpec).
type alert struct {
	Fingerprint string            `json:"fingerprint,omitempty"`
	Status      string            `json:"status"`
	StartsAt    string            `json:"startsAt"`
	EndsAt      string            `json:"endsAt,omitempty"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

// firing is an alert of the cart service that fires from at under a rule,
// its summary a marker of its own, which no log line may hold.
func firing(fingerprint, rule string, at time.Time) alert {
	return alert{
		Fingerprint: fingerprint, Status: "firing", StartsAt: at.UTC().Format(time.RFC3339),
		Labels:      map[string]string{"service": "cart", "rule_id": rule},
		Annotations: map[string]string{"summary": marker(fingerprint, rule, at)},
	}
}

// marker is the summary of an alert, unique to it.
func marker(fingerprint, rule string, at time.Time) string {
	return fmt.Sprintf("body-marker-%s-%s-%d", fingerprint, rule, at.Unix())
}

// resolved is the alert resolved at at.
func (a *alert) resolved(at time.Time) alert {
	r := *a
	r.Status, r.EndsAt = "resolved", at.UTC().Format(time.RFC3339)
	return r
}

// id is the alert's event id, as the mapping's eventId renders it.
func (a *alert) id() string { return a.Fingerprint + ":" + a.Status + ":" + a.StartsAt }

// alerts is a request's body: the alerts, in order.
func alerts(t *testing.T, as ...alert) []byte {
	t.Helper()
	body, err := json.Marshal(map[string][]alert{"alerts": as})
	require.NoError(t, err)
	return body
}

// send posts the alerts to the alerts mapping's source, signed now, and
// asserts that the listener answers 200 with the counts want.
func send(t *testing.T, in *harness.Inbound, want harness.InboundAnswer, as ...alert) {
	t.Helper()
	code, answer := in.Send(t, alertSource, alerts(t, as...), alertSecret, time.Now())
	require.Equal(t, http.StatusOK, code, answer.Error)
	assert.Equal(t, want, answer, "the outcomes of the request's events")
}

// triggered is an item_triggered record's body as inspect prints it (C5).
type triggered struct {
	EventID string `json:"eventId"`
	Status  string `json:"status"`
	Applied bool   `json:"applied"`
	Resume  string `json:"resume"`
}

// triggers returns an item's item_triggered records, in order.
func triggers(t *testing.T, a *harness.Instance, item string) []triggered {
	t.Helper()
	var out []triggered
	for _, line := range a.Inspect(t, "item", item) {
		if line["kind"] != "item_triggered" {
			continue
		}
		var body struct {
			Triggered *triggered `json:"triggered"`
		}
		bodyOf(t, line, &body)
		require.NotNil(t, body.Triggered, "an item_triggered record holds its event: %v", line)
		out = append(out, *body.Triggered)
	}
	return out
}

// bodyOf decodes a line's body, as inspect prints it (C5), into v.
func bodyOf(t *testing.T, line map[string]any, v any) {
	t.Helper()
	raw, err := json.Marshal(line["body"])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, v))
}

// absent asserts that no item exists under an id.
func absent(t *testing.T, a *harness.Instance, id, why string) {
	t.Helper()
	code, v := a.Admin().Item(t, id)
	assert.Equal(t, http.StatusNotFound, code, "%s: item %s: %+v", why, id, v)
}

// unheard asserts that nothing serve wrote holds any of the markers.
func unheard(t *testing.T, a *harness.Instance, markers ...string) {
	t.Helper()
	output := a.Output(t)
	for _, m := range markers {
		assert.NotContains(t, output, m, "serve's output holds no request's body")
	}
}

// grouped is what scenario 29's first two steps leave: alert A's item,
// investigating, which took B by correlation; the alert with no fingerprint;
// and A's firing dated ten minutes before its own.
type grouped struct {
	env       *harness.Env
	a         *harness.Instance
	in        *harness.Inbound
	gate      *gate
	id        string
	t0        time.Time
	first     alert
	second    alert
	bare, old alert
}

// signedGroup runs scenario 29's first two steps, the vendor held: A opens
// an item, which investigates; then one request holds B, of A's service
// under another rule, which correlates into A's item; an alert with no
// fingerprint, unroutable; A again, a repeat; and A's firing with a start
// ten minutes earlier, late.
func signedGroup(t *testing.T) *grouped {
	t.Helper()
	env, a, in, g := alertServing(t)
	// Every time lies in the past, so that the instance clamps none.
	t0 := time.Now().Add(-5 * time.Minute).Truncate(time.Second)
	first := firing("fp-a", "r1", t0)
	send(t, in, harness.InboundAnswer{Opened: 1}, first)
	id := harness.InboundItemID(env.Tenant(), alertSource, first.id())
	a.Admin().WaitItem(t, id, investigatingNow, runWait)

	second := firing("fp-b", "r2", t0.Add(time.Minute))
	bare := firing("", "r3", t0)
	old := firing("fp-a", "r1", t0.Add(-10*time.Minute))
	send(t, in, harness.InboundAnswer{Appended: 1, Unroutable: 1, Repeat: 1, Late: 1}, second, bare, first, old)
	return &grouped{env: env, a: a, in: in, gate: g, id: id, t0: t0, first: first, second: second, bare: bare, old: old}
}

// Scenario 29: a signed alert opens one item, and a group sent while it
// investigates takes each event once, in order: a correlated alert applied,
// one with no fingerprint unroutable, a repeat, and a firing older than its
// instance's status late. The item records every event it took, the late one
// not applied, and no other item opens; the run's board holds the correlated
// alert as platform evidence; serve's log names the unroutable event's
// mapping, owner and reason in one Warn, and holds no body.
func TestASignedGroupOpensOneItemAndTakesItsCorrelatedRepeatedLateAndPoisonEvents(t *testing.T) {
	g := signedGroup(t)
	a := g.a

	assert.Equal(t, []triggered{
		{EventID: g.first.id(), Status: "firing", Applied: true},
		{EventID: g.second.id(), Status: "firing", Applied: true},
		{EventID: g.old.id(), Status: "firing", Applied: false},
	}, triggers(t, a, g.id), "A applied, B applied, and the older A late")
	for _, other := range []alert{g.second, g.old} {
		absent(t, a, harness.InboundItemID(g.env.Tenant(), alertSource, other.id()), "no event but A's first opens an item")
	}
	assert.Equal(t, "1", g.env.Psql(t, "SELECT count(DISTINCT item_id) FROM work_records"), "one item, A's")

	run := runOf(t, g.env, g.id)
	var refs []string
	for _, event := range a.Inspect(t, "board", run) {
		var body struct {
			Evidence *struct {
				Provenance struct {
					Kind string `json:"kind"`
					Ref  string `json:"ref"`
				} `json:"provenance"`
			} `json:"evidence"`
		}
		bodyOf(t, event, &body)
		if body.Evidence != nil && body.Evidence.Provenance.Kind == "platform" {
			refs = append(refs, body.Evidence.Provenance.Ref)
		}
	}
	assert.Contains(t, refs, "trigger:"+g.second.id(), "the board holds B's trigger update as platform evidence (A6 (c))")

	var warned []map[string]any
	for _, line := range a.Log().Lines() {
		if line["level"] == "WARN" && line["reason"] == "missing:instanceId" {
			warned = append(warned, line)
		}
	}
	if assert.Len(t, warned, 1, "one Warn for the unroutable event") {
		assert.Equal(t, "alerts", warned[0]["mapping"], "it names the mapping")
		assert.Equal(t, "harness-team", warned[0]["owner"], "and its owner")
		assert.Equal(t, "unroutable", warned[0]["outcome"])
	}
	unheard(t, a, g.first.Annotations["summary"], g.second.Annotations["summary"], g.bare.Annotations["summary"], g.old.Annotations["summary"])
}

// Scenario 30: once every alert of the item resolves, the run stops
// trigger_resolved at its round's end, and the item awaits its close. An
// extension resumes the run, and its round, which began after the
// resolution, ends for another reason; the item then closes by itself, its
// autoClose after it awaits its close again.
func TestTheResolutionOfEveryAlertEndsTheRunAndTheItemClosesByItself(t *testing.T) {
	g := signedGroup(t)
	a := g.a
	resolvedAt := g.t0.Add(2 * time.Minute)
	send(t, g.in, harness.InboundAnswer{Appended: 2}, g.first.resolved(resolvedAt), g.second.resolved(resolvedAt))
	g.gate.release()

	v := a.Admin().WaitItem(t, g.id, stopped, runWait)
	assert.Equal(t, "trigger_resolved", v.Stop, "the resolution recorded during the round stops the run at its end")
	requireRounds(t, &v, 1)

	one := 1
	code, extended := a.Admin().Extend(t, g.id, "extend", harness.ExtendBody{Rounds: &one})
	require.Equal(t, http.StatusOK, code, extended.Error)
	assert.True(t, extended.Consequences.Resumes, "the extension resumes the run")
	v = a.Admin().WaitItem(t, g.id, func(v *harness.ItemView) bool { return stopped(v) && len(v.Rounds) == 2 }, runWait)
	assert.NotEqual(t, "trigger_resolved", v.Stop, "a resolution before the resume does not stop the resumed run")

	awaited := recordedAt(t, a, g.id, "item_awaiting_close")
	v = a.Admin().WaitItem(t, g.id, func(v *harness.ItemView) bool { return v.State == "closed" }, inboundAutoClose+10*time.Second)
	require.NotNil(t, v.Closed)
	assert.True(t, v.Closed.Auto, "the item closed by itself")
	assert.False(t, v.Closed.At.Before(awaited.Add(inboundAutoClose)), "not before its autoClose: awaiting at %v, closed at %v", awaited, v.Closed.At)
	assert.LessOrEqual(t, v.Closed.At.Sub(awaited), inboundAutoClose+time.Second, "about %v after it awaits its close", inboundAutoClose)
}

// Scenario 31: a firing of an alert whose resolution stopped the run resumes
// the run, recorded as an extension caused by the firing and posted on the
// board as a resume caused by it; once the item is closed, a firing opens a
// new item, linked into the closed item's incident by the source.
func TestAFiringResumesTheRunAndAFiringAfterTheCloseOpensALinkedItem(t *testing.T) {
	env, a, in, g := alertServing(t)
	t0 := time.Now().Add(-5 * time.Minute).Truncate(time.Second)
	first := firing("fp-a", "r1", t0)
	send(t, in, harness.InboundAnswer{Opened: 1}, first)
	id := harness.InboundItemID(env.Tenant(), alertSource, first.id())
	a.Admin().WaitItem(t, id, investigatingNow, runWait)
	// The run is read before its stop, so that the auto-close's window
	// after the stop is spent on the firing alone.
	run := runOf(t, env, id)
	send(t, in, harness.InboundAnswer{Appended: 1}, first.resolved(t0.Add(time.Minute)))
	g.release()
	v := a.Admin().WaitItem(t, id, stopped, runWait)
	require.Equal(t, "trigger_resolved", v.Stop)

	g.hold()
	again := firing("fp-a", "r1", t0.Add(2*time.Minute))
	send(t, in, harness.InboundAnswer{Appended: 1}, again)
	a.Admin().WaitItem(t, id, investigatingNow, runWait)
	var causes []string
	for _, line := range a.Inspect(t, "item", id) {
		if line["kind"] == "run_extended" {
			var body struct {
				Extended *struct {
					Cause string `json:"cause"`
				} `json:"extended"`
			}
			bodyOf(t, line, &body)
			require.NotNil(t, body.Extended, "a run_extended record holds its extension: %v", line)
			causes = append(causes, body.Extended.Cause)
		}
	}
	assert.Equal(t, []string{"firing"}, causes, "one run_extended, caused by the firing")
	var resumes []string
	for _, event := range a.Inspect(t, "board", run) {
		var body struct {
			Resume *struct {
				Cause string `json:"cause"`
			} `json:"resume"`
		}
		bodyOf(t, event, &body)
		if body.Resume != nil {
			resumes = append(resumes, body.Resume.Cause)
		}
	}
	assert.Equal(t, []string{"firing"}, resumes, "the board's resume entry, caused by the firing")
	ts := triggers(t, a, id)
	if assert.Len(t, ts, 3) {
		assert.Equal(t, "resumed", ts[2].Resume, "the firing records that it resumed the run")
	}

	send(t, in, harness.InboundAnswer{Appended: 1}, again.resolved(t0.Add(3*time.Minute)))
	g.release()
	v = a.Admin().WaitItem(t, id, func(v *harness.ItemView) bool { return stopped(v) && len(v.Rounds) == 2 }, runWait)
	assert.Equal(t, "trigger_resolved", v.Stop, "the resolution after the resume stops the resumed run")
	code, closed := a.Admin().Close(t, id, "close")
	require.Equal(t, http.StatusOK, code, closed.Error)
	require.NotNil(t, closed.Item)
	require.Equal(t, "closed", closed.Item.State)

	flap := firing("fp-a", "r1", t0.Add(4*time.Minute))
	send(t, in, harness.InboundAnswer{Opened: 1}, flap)
	linked := harness.InboundItemID(env.Tenant(), alertSource, flap.id())
	code, nv := a.Admin().Item(t, linked)
	require.Equal(t, http.StatusOK, code, nv.Error)
	assert.Equal(t, v.Incident, nv.Incident, "the new item joins the closed item's incident")
	lines := a.Inspect(t, "item", linked)
	require.GreaterOrEqual(t, len(lines), 3, "the new item's records: %v", lines)
	var kinds3 []string
	for _, line := range lines[:3] {
		kinds3 = append(kinds3, fmt.Sprint(line["kind"]))
	}
	assert.Equal(t, []string{"item_opened", "item_triggered", "item_linked"}, kinds3, "the opening, its trigger, then its link")
	var body struct {
		Linked *struct {
			With string `json:"with"`
			By   string `json:"by"`
		} `json:"linked"`
	}
	bodyOf(t, lines[2], &body)
	if assert.NotNil(t, body.Linked) {
		assert.Equal(t, "source:"+alertSource, body.Linked.By, "the source links it")
		assert.Equal(t, id, body.Linked.With, "to the closed item")
	}
}

// Scenario 32: a request whose signature no secret makes, one signed ten
// minutes ago, one to a source no mapping maps, and one to a source that
// authenticates by a bearer token are refused, each with its code, and leave
// nothing: no item for any of their events, no inbound row, and no body in
// serve's output.
func TestUnsignedStaleUnknownAndBearerRequestsAreRefused(t *testing.T) {
	env, a, in, _ := alertServing(t)
	now := time.Now().Truncate(time.Second)
	unsigned := firing("fp-unsigned", "r1", now.Add(-time.Minute))
	stale := firing("fp-stale", "r1", now.Add(-time.Minute))
	unknown := firing("fp-unknown", "r1", now.Add(-time.Minute))
	bearer := firing("fp-bearer", "r1", now.Add(-time.Minute))

	code, answer := in.Send(t, alertSource, alerts(t, unsigned), "not-the-secret", time.Now())
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, "bad_signature", answer.Error, "a signature no secret of the source makes")
	code, answer = in.Send(t, alertSource, alerts(t, stale), alertSecret, time.Now().Add(-10*time.Minute))
	assert.Equal(t, http.StatusUnauthorized, code)
	assert.Equal(t, "stale", answer.Error, "a request signed ten minutes ago, past its maxAge of 5 minutes")
	code, answer = in.Send(t, "nosuch", alerts(t, unknown), alertSecret, time.Now())
	assert.Equal(t, http.StatusNotFound, code)
	assert.Equal(t, "unknown_source", answer.Error, "a source no mapping maps")
	code, answer = in.SendRaw(t, "pager", alerts(t, bearer), http.Header{"Authorization": {"Bearer " + pagerToken}})
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Equal(t, "scheme_unsupported", answer.Error, "a bearer source, refused until the onboarding build")

	for _, c := range []struct {
		source string
		alert  alert
	}{{alertSource, unsigned}, {alertSource, stale}, {"nosuch", unknown}, {"pager", bearer}} {
		absent(t, a, harness.InboundItemID(env.Tenant(), c.source, c.alert.id()), "a refused request opens no item")
	}
	assert.Equal(t, "0", env.Psql(t, "SELECT count(*) FROM inbound_events"), "a refused request records no inbound row")
	assert.Equal(t, "0", env.Psql(t, "SELECT count(*) FROM work_records"), "nor any record")
	unheard(t, a, unsigned.Annotations["summary"], stale.Annotations["summary"], unknown.Annotations["summary"], bearer.Annotations["summary"])
	assert.NotContains(t, a.Output(t), pagerToken, "nor the bearer token")
}
