package admin_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/admin"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// acts are the lifecycle's acts on an item, as their routes name them.
var acts = []string{"promote", "close", "reopen"}

// administrator is who acts through the admin API: the subject admin, an
// admin, since the admin token is an admin's.
var administrator = app.Actor{Subject: "admin", Admin: true}

// secretKey and secretText are a request's key and its reason's text, which
// no answer quotes, nor any line logged (Decision 9).
const (
	secretKey  = "key-4b1e-never-quoted"
	secretText = "a reason's text, never quoted"
)

// withReason is an act's body: a reason, whose text is secretText.
const withReason = `{"reason": {"code": "outage", "text": "` + secretText + `"}}`

// at is a time, which every answer gives in UTC, with its nanoseconds.
var at = time.Date(2026, 11, 3, 11, 30, 0, 5, time.FixedZone("CET", 3600))

// atJSON is at as the answers give it.
const atJSON = `"2026-11-03T10:30:00.000000005Z"`

// acting is a request to act on an item, POST /v1/items/{id}/{act}, under
// key, "" for none, with body.
func acting(act string, id item.ID, key, body string) *http.Request {
	r := request(http.MethodPost, "/v1/items/"+string(id)+"/"+act, body)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}

// switching is a request to the kill switch, POST /v1/kill-switch, with body.
func switching(body string) *http.Request {
	r := request(http.MethodPost, "/v1/kill-switch", body)
	r.Header.Set("Content-Type", "application/json")
	return r
}

// refusalOf returns the text and the code of a lifecycle route's refusal,
// which is JSON of those two alone (R15).
func refusalOf(t *testing.T, w *httptest.ResponseRecorder) (text, code string) {
	t.Helper()
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	require.Len(t, body, 2, w.Body.String())
	require.Contains(t, body, "error")
	require.Contains(t, body, "code")
	return body["error"], body["code"]
}

// padded is head, then x as often as it takes, then tail: a body of n bytes.
func padded(head, tail string, n int) string {
	return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
}

// lifecycleCase is a request to a lifecycle route, how the fixture's
// coordinator and leader answer it, and what the route answers: a 200's
// JSON, or a refusal's code and a part of its text; its Retry-After; and the
// level of the one line it logs, "" for none. Every case's request carries
// secretKey, and secretText in its reason, but where body says otherwise.
type lifecycleCase struct {
	name   string
	leader *leader
	body   string
	// unread has the item's view fail once the act is done, and preview says
	// the request asks a preview.
	unread     bool
	preview    bool
	outcome    app.Outcome
	switched   app.KillSwitchOutcome
	err        error
	status     int
	code       string
	text       string
	want       string
	retryAfter string
	logged     string
	// uncalled says the coordinator is not asked.
	uncalled bool
}

// Each lifecycle route answers as Contract C4 says, whatever the coordinator
// answers. An act on an item answers 200 with whether it changed the item,
// its consequences, each only where it applies, and the item's view after
// it, which an item that cannot be read then leaves out, logged; its preview,
// 200 with the consequences alone. The kill switch answers 200 with its
// state, its latest row, whether it changed, whether it was deferred, the
// runs it stopped, the items it left not staffed, the items that stayed so,
// and what is owed, every list empty rather than null, and an off whose list
// could not be read in full says so; its preview gives its latest row too;
// and its reading gives its latest row's time, actor and reason, if any,
// none before the first row. Every refusal is {"error", "code"} (R15): 400
// invalid or reason_required; 404 no_such_item; 409 for each code of a
// refusal, a key reused, a newer row, an item a later build holds, and an
// instance that does not hold the lease or drains; 413 too_large; 503
// taking_over, with Retry-After: 1, and unavailable, each logged with the
// item. No answer, nor any line logged, quotes the request.
func TestTheLifecycleRoutesAnswerAsTheContractSays(t *testing.T) {
	id, other := admin.ItemID(tenant, "k"), admin.ItemID(tenant, "other")
	view := app.View{ID: id, State: "investigating", Incident: "incident-1", Type: "incident", OpenedAt: at}
	viewJSON := fmt.Sprintf(`{"id": %q, "state": "investigating", "stop": "", "heldByLaterBuild": false, "rounds": [],
		"incident": "incident-1", "type": "incident", "openedAt": %s}`, id, atJSON)
	everything := app.Consequences{
		Stops: []app.RunStop{{Item: id, Run: "run-1", Round: 2, Reason: item.StopClosed}}, Starts: []item.ID{other},
		Closes: []item.ID{id}, Unstaffed: []item.ID{other}, ClosesAt: at, Investigating: 2, Limit: 3,
	}
	everythingJSON := fmt.Sprintf(`{"stops": [{"item": %q, "run": "run-1", "round": 2, "reason": "closed"}], "starts": [%q],
		"closes": [%q], "unstaffed": [%q], "closesAt": %s, "investigating": 2, "limit": 3}`, id, other, id, other, atJSON)
	draining := &leader{epoch: 3, held: true, draining: true}
	refusals := []lifecycleCase{
		{
			name: "an invalid act", status: http.StatusBadRequest, code: "invalid", text: "a reason's code is a lowercase letter",
			err: fmt.Errorf("%w: a reason's code is a lowercase letter, then at most 31 lowercase letters and underscores", app.ErrInvalid),
		},
		{name: "a reason required", err: &app.Refused{Code: app.CodeReasonRequired}, status: http.StatusBadRequest, code: "reason_required"},
		{name: "no such item", err: app.ErrNoItem, status: http.StatusNotFound, code: "no_such_item"},
		{name: "a newer row", err: fmt.Errorf("the kill switch's latest row is 7, not 5: %w", app.ErrStale), status: http.StatusConflict, code: "stale"},
		{name: "a key reused", err: app.ErrKeyReused, status: http.StatusConflict, code: "key_reused"},
		{name: "skipped", err: app.ErrSkipped, status: http.StatusConflict, code: "held_by_later_build"},
		{name: "skipped, of a later version", err: fmt.Errorf("%w: %w", app.ErrSkipped, item.ErrVersion), status: http.StatusConflict, code: "held_by_later_build"},
		{name: "a board event of a later version", err: board.ErrVersion, status: http.StatusConflict, code: "held_by_later_build"},
		{name: "a standby", leader: &leader{epoch: 3}, err: app.ErrNotHeld, status: http.StatusConflict, code: "not_held", text: "does not hold the lease"},
		{
			name: "a write that found the lease gone", err: fmt.Errorf("record item %s: %w", id, app.ErrLeaseLost),
			status: http.StatusConflict, code: "not_held", text: "does not hold the lease",
		},
		{name: "a draining leader", leader: draining, err: app.ErrNotHeld, status: http.StatusConflict, code: "draining", text: "draining"},
		{name: "a leader taking over", err: app.ErrNotHeld, status: http.StatusServiceUnavailable, code: "taking_over", retryAfter: "1", logged: "WARN"},
		{name: "a failure", err: errors.New("the database blinked"), status: http.StatusServiceUnavailable, code: "unavailable", logged: "ERROR"},
		{name: "an unsettled kill switch", err: app.ErrUnsettled, status: http.StatusServiceUnavailable, code: "unavailable", logged: "WARN"},
		{
			name: "a run stopping", err: app.ErrStopping, status: http.StatusServiceUnavailable, code: "stopping", retryAfter: "1", logged: "WARN",
			text: "the run is stopping; try again in a second",
		},
	}
	for _, code := range []string{
		app.CodeClosed, app.CodeReopenWindowPassed, app.CodeLimitReached, app.CodeKillSwitchOn, app.CodeDailyCapReached, app.CodeAdminOnly,
		app.CodeNotStarted,
	} {
		refusals = append(refusals, lifecycleCase{name: "refused " + code, err: &app.Refused{Code: code}, status: http.StatusConflict, code: code})
	}
	overTheCap := `{"reason": {"code": "outage", "text": "` + secretText + strings.Repeat("x", 64<<10) + `"}}`
	actCases := append([]lifecycleCase{
		{
			name: "an act that changed the item", outcome: app.Outcome{Changed: true, Consequences: everything}, status: http.StatusOK,
			want: `{"changed": true, "consequences": ` + everythingJSON + `, "item": ` + viewJSON + `}`,
		},
		{
			name: "an act whose effect held", outcome: app.Outcome{Consequences: app.Consequences{AutoClosePaused: true, Limit: 3}}, status: http.StatusOK,
			want: `{"changed": false, "consequences": {"autoClosePaused": true, "investigating": 0, "limit": 3}, "item": ` + viewJSON + `}`,
		},
		{
			name: "an act whose item cannot be read after it", unread: true, status: http.StatusOK, logged: "WARN",
			outcome: app.Outcome{Changed: true, Consequences: app.Consequences{Investigating: 1, Limit: 3}},
			want:    `{"changed": true, "consequences": {"investigating": 1, "limit": 3}}`,
		},
		{
			name: "a preview", body: `{"preview": true, "reason": {"code": "outage", "text": "` + secretText + `"}}`, preview: true,
			outcome: app.Outcome{Consequences: everything}, status: http.StatusOK, want: `{"preview": true, "consequences": ` + everythingJSON + `}`,
		},
		{name: "a body over the cap", body: overTheCap, status: http.StatusRequestEntityTooLarge, code: "too_large", uncalled: true},
	}, refusals...)
	for _, act := range acts {
		for _, tc := range actCases {
			name := act + ": " + tc.name
			f := newFixture(0)
			if tc.leader != nil {
				*f.leader = *tc.leader
			}
			f.coord.outcome, f.coord.actErr = tc.outcome, tc.err
			f.items.views[id] = view
			if tc.unread {
				f.items.err = errors.New("the database blinked")
			}
			body := withReason
			if tc.body != "" {
				body = tc.body
			}

			w := f.serve(acting(act, id, secretKey, body))

			answered(t, name, f, w, &tc)
			if tc.uncalled {
				assert.Empty(t, f.coord.acts(), name)
				continue
			}
			want := app.Act{Actor: administrator, Key: secretKey, Reason: &board.Reason{Code: "outage", Text: secretText}}
			if tc.preview {
				want.Key, want.Preview = "", true
			}
			assert.Equal(t, []actCall{{kind: act, id: id, act: want}}, f.coord.acts(), name)
			if tc.logged != "" {
				line := f.logs.lines(t)[0]
				assert.Equal(t, act, line["act"], name)
				assert.Equal(t, string(id), line["item"], name)
			}
		}
	}

	on := `{"on": true, "after": 4, "reason": {"code": "outage", "text": "` + secretText + `"}}`
	off := `{"on": false, "after": 5, "reason": {"code": "recovered", "text": "` + secretText + `"}}`
	stopped := []app.RunStop{{Item: id, Run: "run-1", Round: 2, Reason: item.StopKilled}}
	switchCases := []lifecycleCase{
		{
			name: "an on", body: on, status: http.StatusOK,
			switched: app.KillSwitchOutcome{On: true, Seq: 5, Changed: true, Stopped: stopped, Unstaffed: []item.ID{other}, Owed: []item.ID{id}},
			want: fmt.Sprintf(`{"on": true, "seq": 5, "changed": true, "deferred": false, "stopped": [{"item": %q, "run": "run-1", "round": 2}],
				"unstaffed": [%q], "stayedUnstaffed": [], "owed": [%q]}`, id, other, id),
		},
		{
			name: "an on whose state held", body: on, switched: app.KillSwitchOutcome{On: true, Seq: 5}, status: http.StatusOK,
			want: `{"on": true, "seq": 5, "changed": false, "deferred": false, "stopped": [], "unstaffed": [], "stayedUnstaffed": [], "owed": []}`,
		},
		{
			name: "an off", body: off, status: http.StatusOK, switched: app.KillSwitchOutcome{Seq: 6, Changed: true, StayedUnstaffed: []app.UnstaffedItem{
				{Item: id, State: item.StatusAwaitingClose}, {Item: other, State: item.StatusClosed},
			}},
			want: fmt.Sprintf(`{"on": false, "seq": 6, "changed": true, "deferred": false, "stopped": [], "unstaffed": [],
				"stayedUnstaffed": [{"item": %q, "state": "awaiting_close"}, {"item": %q, "state": "closed"}], "owed": []}`, id, other),
		},
		{
			name: "an off whose list could not be read in full", body: off, status: http.StatusOK, switched: app.KillSwitchOutcome{
				Seq: 6, Changed: true, StayedUnstaffed: []app.UnstaffedItem{{Item: id, State: item.StatusAwaitingClose}, {Item: other}},
				StayedUnstaffedUnread: true,
			},
			want: fmt.Sprintf(`{"on": false, "seq": 6, "changed": true, "deferred": false, "stopped": [], "unstaffed": [],
				"stayedUnstaffed": [{"item": %q, "state": "awaiting_close"}, {"item": %q, "state": ""}], "stayedUnstaffedUnread": true, "owed": []}`, id, other),
		},
		{
			name: "a preview", body: `{"on": true, "after": 4, "preview": true}`, status: http.StatusOK,
			switched: app.KillSwitchOutcome{Seq: 4, Preview: true, Consequences: app.Consequences{Stops: stopped, Unstaffed: []item.ID{other}}},
			want: fmt.Sprintf(`{"preview": true, "seq": 4, "consequences": {"stops": [{"item": %q, "run": "run-1", "round": 2, "reason": "killed"}],
				"unstaffed": [%q]}}`, id, other),
		},
		{
			name: "a preview before the first row", body: `{"on": true, "after": 0, "preview": true}`, status: http.StatusOK,
			switched: app.KillSwitchOutcome{Preview: true}, want: `{"preview": true, "seq": 0, "consequences": {}}`,
		},
		{
			name: "an invalid request", body: on, status: http.StatusBadRequest, code: "invalid", text: "after is the kill switch's latest row",
			err: fmt.Errorf("%w: after is the kill switch's latest row its sender saw, 0 before the first", app.ErrInvalid),
		},
		{name: "an on without a reason", body: `{"on": true, "after": 4}`, err: &app.Refused{Code: app.CodeReasonRequired}, status: http.StatusBadRequest, code: "reason_required"},
		{name: "a newer row", body: off, err: fmt.Errorf("the kill switch's latest row is 7, not 5: %w", app.ErrStale), status: http.StatusConflict, code: "stale"},
		{name: "an off by no admin", body: off, err: &app.Refused{Code: app.CodeAdminOnly}, status: http.StatusConflict, code: "admin_only"},
		{name: "an on on a standby", leader: &leader{epoch: 3}, body: on, err: app.ErrNotHeld, status: http.StatusConflict, code: "not_held"},
		{name: "an off on a standby", leader: &leader{epoch: 3}, body: off, err: app.ErrNotHeld, status: http.StatusConflict, code: "not_held"},
		{name: "an off on a draining leader", leader: draining, body: off, err: app.ErrNotHeld, status: http.StatusConflict, code: "draining"},
		{
			name: "an on on a draining leader that lost the lease", leader: &leader{epoch: 3, draining: true}, body: on, err: app.ErrNotHeld,
			status: http.StatusConflict, code: "not_held", text: "does not hold the lease",
		},
		{
			name: "a preview on a draining leader that lost the lease", leader: &leader{epoch: 3, draining: true}, body: `{"on": false, "after": 4, "preview": true}`,
			err: app.ErrNotHeld, status: http.StatusConflict, code: "not_held",
		},
		{
			name: "an off on a draining leader that lost the lease", leader: &leader{epoch: 3, draining: true}, body: off, err: app.ErrNotHeld,
			status: http.StatusConflict, code: "draining",
		},
		{
			name: "a body over the cap", status: http.StatusRequestEntityTooLarge, code: "too_large", uncalled: true,
			body: `{"on": true, "after": 4, "reason": {"code": "outage", "text": "` + secretText + strings.Repeat("x", 64<<10) + `"}}`,
		},
		{name: "a leader taking over", body: on, err: app.ErrNotHeld, status: http.StatusServiceUnavailable, code: "taking_over", retryAfter: "1", logged: "WARN"},
		{name: "a failure", body: on, err: errors.New("the database blinked"), status: http.StatusServiceUnavailable, code: "unavailable", logged: "ERROR"},
	}
	for _, tc := range switchCases {
		name := "the kill switch: " + tc.name
		f := newFixture(0)
		if tc.leader != nil {
			*f.leader = *tc.leader
		}
		f.coord.switched, f.coord.switchErr = tc.switched, tc.err

		w := f.serve(switching(tc.body))

		answered(t, name, f, w, &tc)
		if tc.uncalled {
			assert.Empty(t, f.coord.switchCalls(), name)
			continue
		}
		assert.Equal(t, []string{"SetKillSwitch"}, f.coord.switchCalls(), name)
	}

	readings := []lifecycleCase{
		{
			name: "a switch on", status: http.StatusOK,
			want: `{"on": true, "seq": 3, "since": ` + atJSON + `, "by": "admin", "reason": {"code": "outage", "text": "checkout is down"}}`,
		},
		{name: "a switch never set", status: http.StatusOK, want: `{"on": false, "seq": 0}`},
		{name: "a switch turned off with no reason", status: http.StatusOK, want: `{"on": false, "seq": 4, "since": ` + atJSON + `, "by": "admin"}`},
		{name: "a table that cannot be read", err: errors.New("connection refused"), status: http.StatusServiceUnavailable, code: "unavailable", logged: "ERROR"},
	}
	states := []app.KillSwitch{
		{On: true, Seq: 3, OnSeq: 3, By: "admin", Reason: board.Reason{Code: "outage", Text: "checkout is down"}, Since: at},
		{},
		{Seq: 4, OnSeq: 3, OffAt: at, By: "admin", Since: at},
		{},
	}
	for i, tc := range readings {
		name := "reading the kill switch: " + tc.name
		f := newFixture(0)
		f.items.killSwitch, f.items.switchErr = states[i], tc.err

		w := f.serve(request(http.MethodGet, "/v1/kill-switch", ""))

		answered(t, name, f, w, &tc)
		if tc.logged != "" {
			assert.Equal(t, "/v1/kill-switch", f.logs.lines(t)[0]["path"], name)
		}
	}
}

// answered checks what a lifecycle route answered w, as tc says: its status;
// a 200's JSON, or a refusal's code and text; its Retry-After; and the line
// it logged, if any, which names the error, at tc's level. Neither the
// answer nor the log quotes the request.
func answered(t *testing.T, name string, f *fixture, w *httptest.ResponseRecorder, tc *lifecycleCase) {
	t.Helper()
	assert.Equal(t, tc.status, w.Code, "%s: %s", name, w.Body.String())
	if tc.status == http.StatusOK {
		assert.JSONEq(t, tc.want, w.Body.String(), name)
	} else {
		text, code := refusalOf(t, w)
		assert.Equal(t, tc.code, code, name)
		assert.Contains(t, text, tc.text, name)
		for _, kept := range []string{"blinked", "refused", "app:"} {
			assert.NotContains(t, text, kept, "%s: the error stays in the log", name)
		}
	}
	for _, quoted := range []string{secretKey, secretText} {
		assert.NotContains(t, w.Body.String(), quoted, "%s: no answer quotes the request", name)
	}
	assert.Equal(t, tc.retryAfter, w.Header().Get("Retry-After"), name)
	lines := f.logs.lines(t)
	if tc.logged == "" {
		assert.Empty(t, lines, name)
		return
	}
	require.Len(t, lines, 1, name)
	assert.Equal(t, tc.logged, lines[0]["level"], name)
	if tc.err != nil {
		assert.Equal(t, tc.err.Error(), lines[0]["err"], name)
	}
	for _, quoted := range []string{secretKey, secretText} {
		assert.NotContains(t, fmt.Sprint(lines[0]), quoted, "%s: no line logged quotes the request", name)
	}
}

// An act on an item needs its key, of 1 to 128 printable ASCII characters,
// once (Contract C4): without one, with two, or with one too long or of other
// characters, it is refused 400, invalid, and the coordinator is not asked.
// A preview takes no key: one without is taken, its act keyless, and so is an
// act under a key of 128 characters.
func TestALifecycleActWithoutAKeyIsRefused(t *testing.T) {
	id := admin.ItemID(tenant, "k")
	for _, act := range acts {
		f := newFixture(0)
		for name, key := range map[string]string{
			"no key":                  "",
			"a key of 129 characters": strings.Repeat("k", 129),
			"a key with a control":    "retry\x7f1",
			"a key beyond ASCII":      "retry-é",
		} {
			w := f.serve(acting(act, id, key, ""))

			assert.Equal(t, http.StatusBadRequest, w.Code, "%s: %s", act, name)
			_, code := refusalOf(t, w)
			assert.Equal(t, "invalid", code, "%s: %s", act, name)
		}
		twice := acting(act, id, "a", "")
		twice.Header.Add("Idempotency-Key", "b")
		w := f.serve(twice)
		assert.Equal(t, http.StatusBadRequest, w.Code, "%s: two keys", act)
		_, code := refusalOf(t, w)
		assert.Equal(t, "invalid", code, "%s: two keys", act)
		assert.Empty(t, f.coord.acts(), "%s: the coordinator was not asked", act)

		previewed := f.serve(acting(act, id, "", `{"preview": true}`))
		longest := f.serve(acting(act, id, strings.Repeat("k", 128), ""))

		assert.Equal(t, http.StatusOK, previewed.Code, "%s: a preview without a key", act)
		assert.Equal(t, http.StatusOK, longest.Code, "%s: a key of 128 characters", act)
		assert.Equal(t, []actCall{
			{kind: act, id: id, act: app.Act{Actor: administrator, Preview: true}},
			{kind: act, id: id, act: app.Act{Actor: administrator, Key: strings.Repeat("k", 128)}},
		}, f.coord.acts(), act)
	}
}

// An act's body is optional, and decoded strictly when given (Contract C4):
// one JSON object, of a reason and preview alone. A field it does not have,
// in the act or in its reason, a reason or a preview of another kind, more
// than one value, or a value that is no object, is refused 400, invalid,
// without repeating the body, and the coordinator is not asked. An empty
// body, or one of whitespace alone, is an act with no reason.
func TestALifecycleActsBodyIsDecodedStrictly(t *testing.T) {
	id := admin.ItemID(tenant, "k")
	for _, act := range acts {
		f := newFixture(0)
		for name, body := range map[string]string{
			"a field it does not have":         `{"reasn": {"code": "outage"}}`,
			"a field its reason does not have": `{"reason": {"code": "outage", "txt": "checkout is down"}}`,
			"a reason of another kind":         `{"reason": "outage"}`,
			"a reason's code of another kind":  `{"reason": {"code": 7}}`,
			"a preview of another kind":        `{"preview": "yes"}`,
			"two objects":                      `{} {}`,
			"an object, then more":             `{"preview": true} x`,
			"not JSON":                         `reason=outage`,
			"an array":                         `[]`,
			"null":                             `null`,
		} {
			w := f.serve(acting(act, id, "k", body))

			assert.Equal(t, http.StatusBadRequest, w.Code, "%s: %s", act, name)
			text, code := refusalOf(t, w)
			assert.Equal(t, "invalid", code, "%s: %s", act, name)
			for _, word := range []string{"reasn", "txt", "checkout", "yes", "outage"} {
				assert.NotContains(t, text, word, "%s: %s: the body is not repeated", act, name)
			}
		}
		assert.Empty(t, f.coord.acts(), "%s: the coordinator was not asked", act)

		for name, body := range map[string]string{"no body": "", "whitespace alone": " \n\t "} {
			w := f.serve(acting(act, id, "k", body))

			assert.Equal(t, http.StatusOK, w.Code, "%s: %s: %s", act, name, w.Body.String())
		}
		noReason := actCall{kind: act, id: id, act: app.Act{Actor: administrator, Key: "k"}}
		assert.Equal(t, []actCall{noReason, noReason}, f.coord.acts(), "%s: an act with no reason", act)
	}
}

// A lifecycle request's body over the cap, 64 KiB, is refused 413,
// too_large, whether or not the request declared its length, and the
// coordinator is not asked (Decision 14); a body at the cap is read. So for
// every act on an item, and for the kill switch.
func TestALifecycleActsBodyOverTheCapAnswers413(t *testing.T) {
	const capacity = 64 << 10
	id := admin.ItemID(tenant, "k")
	type route struct {
		head string
		send func(body string) *http.Request
	}
	routes := map[string]route{"the kill switch": {head: `{"on": true, "after": 0, "reason": {"code": "outage", "text": "`, send: switching}}
	for _, act := range acts {
		routes[act] = route{head: `{"reason": {"code": "outage", "text": "`, send: func(body string) *http.Request { return acting(act, id, "k", body) }}
	}
	for name, rt := range routes {
		f := newFixture(0)
		atTheCap, over := padded(rt.head, `"}}`, capacity), padded(rt.head, `"}}`, capacity+1)
		require.Len(t, atTheCap, capacity)
		undeclared := rt.send(over)
		undeclared.ContentLength = -1
		for how, r := range map[string]*http.Request{"declared": rt.send(over), "undeclared": undeclared} {
			w := f.serve(r)

			assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code, "%s: %s", name, how)
			_, code := refusalOf(t, w)
			assert.Equal(t, "too_large", code, "%s: %s", name, how)
		}
		assert.Empty(t, f.coord.acts(), name)
		assert.Empty(t, f.coord.switchCalls(), name)

		w := f.serve(rt.send(atTheCap))

		assert.Equal(t, http.StatusOK, w.Code, "%s: a body at the cap: %s", name, w.Body.String())
	}
}

// The kill switch's body is required, and names the state asked, on, and
// after, the latest row its sender saw, 0 before the first (R8): a body
// without either, or no body, is refused 400, invalid, and the coordinator is
// not asked. The request takes no key: an Idempotency-Key is not read, so
// that one an act on an item would refuse refuses nothing here. After
// reaches the coordinator as Act.After, the reason as given, and the actor
// as admin, an admin.
func TestTheKillSwitchRouteTakesAfterAndNoKey(t *testing.T) {
	f := newFixture(0)
	for name, body := range map[string]string{
		"no after":                 `{"on": true, "reason": {"code": "outage"}}`,
		"no on":                    `{"after": 3, "reason": {"code": "outage"}}`,
		"an empty object":          `{}`,
		"no body":                  ``,
		"an after of another kind": `{"on": true, "after": "3", "reason": {"code": "outage"}}`,
		"a field it does not have": `{"on": true, "after": 3, "key": "k"}`,
		"two objects":              `{"on": true, "after": 3} {"on": false, "after": 3}`,
	} {
		w := f.serve(switching(body))

		assert.Equal(t, http.StatusBadRequest, w.Code, name)
		_, code := refusalOf(t, w)
		assert.Equal(t, "invalid", code, name)
	}
	assert.Empty(t, f.coord.switchCalls(), "the coordinator was not asked")

	keyed := switching(`{"on": true, "after": 3, "reason": {"code": "outage", "text": "checkout is down"}}`)
	keyed.Header.Set("Idempotency-Key", strings.Repeat("k", 129))
	keyed.Header.Add("Idempotency-Key", "another")
	on := f.serve(keyed)
	off := f.serve(switching(`{"on": false, "after": 0}`))

	assert.Equal(t, http.StatusOK, on.Code, on.Body.String())
	assert.Equal(t, http.StatusOK, off.Code, off.Body.String())
	assert.Equal(t, []switchCall{
		{on: true, act: app.Act{Actor: administrator, After: 3, Reason: &board.Reason{Code: "outage", Text: "checkout is down"}}},
		{on: false, act: app.Act{Actor: administrator}},
	}, f.coord.switchRequests())
}

// The kill switch's on during a drain goes to the coordinator first (Decision
// 5): SetKillSwitch answers ErrNotHeld, as the drain ended the coordinator's
// holding, and the admin API reads the leader's status, which holds the
// lease and drains; it then records the on through RecordKillSwitchOn, under
// the draining holding's epoch, with the request's act.
func TestTheKillSwitchOnDuringADrainTriesTheCoordinatorFirst(t *testing.T) {
	f := newFixture(0)
	*f.leader = leader{epoch: 7, held: true, draining: true}
	f.coord.switchErr = app.ErrNotHeld
	f.coord.recorded = app.KillSwitchOutcome{On: true, Seq: 5, Changed: true, Deferred: true}

	w := f.serve(switching(`{"on": true, "after": 4, "reason": {"code": "outage"}}`))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []string{"SetKillSwitch", "RecordKillSwitchOn"}, f.coord.switchCalls())
	act := app.Act{Actor: administrator, After: 4, Reason: &board.Reason{Code: "outage"}}
	assert.Equal(t, []switchCall{{on: true, act: act}}, f.coord.switchRequests())
	assert.Equal(t, []recordCall{{epoch: 7, act: act}}, f.coord.recordRequests())
}

// An on recorded during a drain answers 200, deferred, as the store answered
// it, with nothing stopped and no owed at all (Decision 5; Contract C4): the
// fenced coordinator holds no account, and the next leader's takeover acts on
// the row. An off during a drain is refused 409, draining, and so is any
// preview: neither is sent to RecordKillSwitchOn. An on whose recording the
// store's guard refused, as once the draining holding ended, answers 409,
// not_held, and is sent to neither method again; one whose recording failed
// otherwise answers 503, unavailable. Each failure of the recording is
// logged, with the draining holding's epoch.
func TestTheKillSwitchOnDuringADrainIsRecordedAndDeferred(t *testing.T) {
	draining := leader{epoch: 7, held: true, draining: true}
	const on = `{"on": true, "after": 4, "reason": {"code": "outage"}}`
	f := newFixture(0)
	*f.leader = draining
	f.coord.switchErr = app.ErrNotHeld
	f.coord.recorded = app.KillSwitchOutcome{On: true, Seq: 5, Changed: true, Deferred: true}

	w := f.serve(switching(on))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"on": true, "seq": 5, "changed": true, "deferred": true, "stopped": [], "unstaffed": [], "stayedUnstaffed": []}`,
		w.Body.String(), "no owed key")
	assert.Empty(t, f.logs.lines(t))

	for name, body := range map[string]string{
		"an off":           `{"on": false, "after": 5}`,
		"an on's preview":  `{"on": true, "after": 5, "preview": true}`,
		"an off's preview": `{"on": false, "after": 5, "preview": true}`,
	} {
		f := newFixture(0)
		*f.leader = draining
		f.coord.switchErr = app.ErrNotHeld

		w := f.serve(switching(body))

		assert.Equal(t, http.StatusConflict, w.Code, name)
		text, code := refusalOf(t, w)
		assert.Equal(t, "draining", code, name)
		assert.True(t, strings.HasPrefix(text, "this instance is draining; "), "%s: %s", name, text)
		assert.Equal(t, []string{"SetKillSwitch"}, f.coord.switchCalls(), name)
		assert.Empty(t, f.logs.lines(t), name)
	}

	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
		level  string
	}{
		"the guard's refusal": {err: app.ErrNotHeld, status: http.StatusConflict, code: "not_held", level: "WARN"},
		"a lost answer":       {err: errors.New("the database's answer was lost"), status: http.StatusServiceUnavailable, code: "unavailable", level: "ERROR"},
	} {
		f := newFixture(0)
		*f.leader = draining
		f.coord.switchErr, f.coord.recordErr = app.ErrNotHeld, tc.err

		w := f.serve(switching(on))

		assert.Equal(t, tc.status, w.Code, name)
		text, code := refusalOf(t, w)
		assert.Equal(t, tc.code, code, name)
		assert.NotContains(t, text, "lost", name)
		assert.Equal(t, []string{"SetKillSwitch", "RecordKillSwitchOn"}, f.coord.switchCalls(), "%s: sent to neither method again", name)
		lines := f.logs.lines(t)
		require.Len(t, lines, 1, name)
		assert.Equal(t, tc.level, lines[0]["level"], name)
		assert.InDelta(t, 7, lines[0]["epoch"], 0, name)
		assert.Equal(t, tc.err.Error(), lines[0]["err"], name)
	}
}

// A kill switch request that SetKillSwitch answers ErrNotHeld while the
// leader holds the lease and does not drain yet, as in the moment between a
// drain's end of the holding's context and its publishing, or during a
// takeover, answers 503, taking_over, with Retry-After: 1, logged, and is not
// recorded (Decision 5): the retry finds the drain, or the coordinator taken
// over. A preview answers so too.
func TestAKillSwitchOnBeforeTheDrainIsPublishedAnswersTryAgain(t *testing.T) {
	for name, body := range map[string]string{
		"an on":        `{"on": true, "after": 4, "reason": {"code": "outage"}}`,
		"its preview":  `{"on": true, "after": 4, "preview": true}`,
		"an off":       `{"on": false, "after": 4}`,
		"its preview ": `{"on": false, "after": 4, "preview": true}`,
	} {
		f := newFixture(0)
		*f.leader = leader{epoch: 7, held: true}
		f.coord.switchErr = app.ErrNotHeld

		w := f.serve(switching(body))

		assert.Equal(t, http.StatusServiceUnavailable, w.Code, name)
		_, code := refusalOf(t, w)
		assert.Equal(t, "taking_over", code, name)
		assert.Equal(t, "1", w.Header().Get("Retry-After"), name)
		assert.Equal(t, []string{"SetKillSwitch"}, f.coord.switchCalls(), "%s: not recorded", name)
		lines := f.logs.lines(t)
		require.Len(t, lines, 1, name)
		assert.Equal(t, "WARN", lines[0]["level"], name)
	}
}

// Each act on an item that the coordinator answers ErrNotHeld while the
// leader drains, as every act answers once the drain ended the holding's
// context, answers 409, draining, with the drain's text (Decision 12): it
// says how the instance stands, that the act is not confirmed, as one fenced
// after its commit answers ErrNotHeld too, and what to do on the instance that
// leads next. Its preview answers 409, draining, with a preview's text, which
// says that a preview records nothing, and where to send it (see
// TestARefusedPreviewIsToldWhatAPreviewCanDo). None is logged.
func TestALifecycleActOnADrainingInstanceAnswersDraining(t *testing.T) {
	texts := map[string]string{
		"the act":     "this instance is draining; the act is not confirmed: on the instance that leads next, read the item, or send the act again with the same key",
		"its preview": "this instance is draining; a preview records nothing: send it to the instance that leads next",
	}
	id := admin.ItemID(tenant, "k")
	for _, act := range acts {
		for name, body := range map[string]string{"the act": "", "its preview": `{"preview": true}`} {
			f := newFixture(0)
			*f.leader = leader{epoch: 7, held: true, draining: true}
			f.coord.actErr = app.ErrNotHeld

			w := f.serve(acting(act, id, "k", body))

			assert.Equal(t, http.StatusConflict, w.Code, "%s: %s", act, name)
			text, code := refusalOf(t, w)
			assert.Equal(t, "draining", code, "%s: %s", act, name)
			assert.Equal(t, texts[name], text, "%s: %s", act, name)
			assert.Empty(t, f.logs.lines(t), "%s: %s", act, name)
		}
	}
}

// A refused preview is told what a preview can do (Contract C4): it records
// nothing and takes no key, so its refusal never says that it is not
// confirmed, never asks for a key, and never asks for the on or the off to be
// sent again. The preview of each act on an item, and of the kill switch's on
// and off, refused as draining, as not holding the lease, or as unavailable,
// says what a preview is, and to send it again, there or here. Fails first:
// each was given its act's text.
func TestARefusedPreviewIsToldWhatAPreviewCanDo(t *testing.T) {
	id := admin.ItemID(tenant, "k")
	for _, c := range []struct {
		name   string
		leader leader
		err    error
		code   string
	}{
		{name: "on a draining leader", leader: leader{epoch: 7, held: true, draining: true}, err: app.ErrNotHeld, code: "draining"},
		{name: "on a standby", leader: leader{epoch: 7}, err: app.ErrNotHeld, code: "not_held"},
		{name: "that failed", leader: leader{epoch: 7, held: true}, err: errors.New("the database blinked"), code: "unavailable"},
	} {
		previews := map[string]func() *http.Request{
			"the on's preview":  func() *http.Request { return switching(`{"on": true, "after": 4, "preview": true}`) },
			"the off's preview": func() *http.Request { return switching(`{"on": false, "after": 4, "preview": true}`) },
		}
		for _, act := range acts {
			previews[act+"'s preview"] = func() *http.Request { return acting(act, id, "", `{"preview": true}`) }
		}
		for what, preview := range previews {
			name := what + " " + c.name
			f := newFixture(0)
			*f.leader = c.leader
			f.coord.actErr, f.coord.switchErr = c.err, c.err

			w := f.serve(preview())

			text, code := refusalOf(t, w)
			assert.Equal(t, c.code, code, name)
			assert.Contains(t, text, "preview", name)
			for _, wrong := range []string{"not confirmed", "key", "send the on again", "send the off again", "send the act again", "send the request again"} {
				assert.NotContains(t, text, wrong, name)
			}
		}
	}
}

// A lifecycle route refuses another method with 405, naming the methods it
// takes, with the refusal every route shares, {"error"} alone (R15), and the
// coordinator is not asked.
func TestALifecycleRouteRefusesAnotherMethod(t *testing.T) {
	f := newFixture(0)
	path := "/v1/items/" + string(admin.ItemID(tenant, "k"))
	for _, tc := range []struct{ method, path, allow string }{
		{http.MethodGet, path + "/promote", "POST"},
		{http.MethodGet, path + "/close", "POST"},
		{http.MethodDelete, path + "/reopen", "POST"},
		{http.MethodDelete, "/v1/kill-switch", "GET, POST"},
		{http.MethodPut, "/v1/kill-switch", "GET, POST"},
	} {
		w := f.serve(request(tc.method, tc.path, ""))

		assert.Equal(t, http.StatusMethodNotAllowed, w.Code, "%s %s", tc.method, tc.path)
		assert.Equal(t, tc.allow, w.Header().Get("Allow"), "%s %s", tc.method, tc.path)
		assert.NotEmpty(t, errorOf(t, w))
		assert.Empty(t, codeOf(t, w), "a refusal every route shares has no code")
	}
	assert.Empty(t, f.coord.acts())
	assert.Empty(t, f.coord.switchCalls())
}
