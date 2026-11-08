//go:build e2e

package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

const (
	// adminTimeout bounds one request to the admin API.
	adminTimeout = 30 * time.Second
	// adminPoll is how often OpenItem and WaitItem ask again.
	adminPoll = 100 * time.Millisecond
)

// Admin is serve's admin API (Contract C4), on loopback, called with the
// instance's admin token as a bearer token.
type Admin struct {
	base   string
	token  string
	client *http.Client
}

func newAdmin(addr, token string) *Admin {
	return &Admin{base: "http://" + addr, token: token, client: &http.Client{Timeout: adminTimeout}}
}

// Opening is the body of an opening, POST /v1/items.
type Opening struct {
	Type       string            `json:"type"`
	Title      string            `json:"title"`
	Routing    map[string]string `json:"routing"`
	Principal  string            `json:"principal"`
	Responders []string          `json:"responders"`
}

// Opened is the answer to an opening: the item's id and state, or why it was
// refused, and, for a refusal to be tried again in a moment, its Retry-After
// header (C4).
type Opened struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	Error      string `json:"error"`
	RetryAfter string `json:"-"`
}

// ItemView is an item as GET /v1/items/{id} shows it (Contract C4). Its
// incident, type and opening time are every item's; Queue a waiting item's,
// nil for any other; NotStaffed an item's that awaits its close not staffed;
// AutoClosePaused every item's that awaits its close, nil for any other, and
// nil too when the kill switch's table could not be read (R28); ClosesAt such
// an item's while its auto-close is not paused and its delay could be read,
// zero otherwise; Closed a closed item's, nil for any other; IncidentItems,
// every item's, the items of its incident, sorted; and Budget and
// ExtensionOffered an item's with a run, nil for any other: its run's budget,
// its creation's and every extension's, and what it used, and the extension
// the interface offers, lifecycle.extension clamped to the run's creation's
// budget, which gives no tokens. A view of an item a later build holds says
// that alone.
type ItemView struct {
	ID               string      `json:"id"`
	State            string      `json:"state"`
	Stop             string      `json:"stop"`
	HeldByLaterBuild bool        `json:"heldByLaterBuild"`
	Rounds           []RoundView `json:"rounds"`
	Incident         string      `json:"incident"`
	Type             string      `json:"type"`
	OpenedAt         time.Time   `json:"openedAt"`
	Queue            *QueueView  `json:"queue"`
	NotStaffed       string      `json:"notStaffed"`
	ClosesAt         time.Time   `json:"closesAt"`
	AutoClosePaused  *bool       `json:"autoClosePaused"`
	Closed           *ClosedView `json:"closed"`
	IncidentItems    []string    `json:"incidentItems"`
	Budget           *BudgetView `json:"budget"`
	ExtensionOffered *AmountView `json:"extensionOffered"`
	Error            string      `json:"error"`
}

// AmountView is a budget, or an amount of one, as the admin API shows it
// (Contract C4): rounds, a wall clock as a duration, such as 5m0s, and
// cost-weighted tokens.
type AmountView struct {
	Rounds    int    `json:"rounds"`
	WallClock string `json:"wallClock"`
	Tokens    int64  `json:"tokens"`
}

// BudgetView is a run's budget as an item's view shows it (Contract C4): its
// creation's plus every extension's, and what the run used, as its latest
// ended round says.
type BudgetView struct {
	AmountView
	Used AmountView `json:"used"`
}

// QueueView is a waiting item's place in the queue, from 1, among the queued
// items neither admitted nor promoted (R17).
type QueueView struct {
	Position int `json:"position"`
}

// ClosedView is a closed item's close: when, by whom, "" for a close by
// itself, which Auto says; and until when a person may reopen the item, and
// make the rulings a closed item takes.
type ClosedView struct {
	At               time.Time `json:"at"`
	By               string    `json:"by"`
	Auto             bool      `json:"auto"`
	ReopenableUntil  time.Time `json:"reopenableUntil"`
	LateRulingsUntil time.Time `json:"lateRulingsUntil"`
}

// RoundView is a round of an item's view: how it ended, the evidence its
// end read, and the board's report of it, as the board marshals it.
type RoundView struct {
	Round    int             `json:"round"`
	WrapUp   bool            `json:"wrapUp"`
	Ended    string          `json:"ended"`
	Quiet    bool            `json:"quiet"`
	Evidence Evidence        `json:"evidence"`
	Report   json.RawMessage `json:"report"`
}

// Evidence is what a round's end read (Decision 9).
type Evidence struct {
	TurnEnds         []string `json:"turnEnds"`
	FailedModelCalls int      `json:"failedModelCalls"`
	FailedToolCalls  int      `json:"failedToolCalls"`
	Settled          bool     `json:"settled"`
}

// InstanceStatus is the instance's status, GET /v1/status.
type InstanceStatus struct {
	Leading    bool             `json:"leading"`
	Epoch      int64            `json:"epoch"`
	Schema     string           `json:"schema"`
	Draining   bool             `json:"draining"`
	Skipped    int              `json:"skipped"`
	Runner     RunnerStatus     `json:"runner"`
	KillSwitch KillSwitchStatus `json:"killSwitch"`
	Queue      QueueStatus      `json:"queue"`
}

// KillSwitchStatus is the kill switch's part of the instance's status
// (Contract C4): its state and its latest row, 0 before the first, as its
// table holds them.
type KillSwitchStatus struct {
	On  bool  `json:"on"`
	Seq int64 `json:"seq"`
}

// QueueStatus is the queue's part of the instance's status (Contract C4), as
// the coordinator's snapshot holds it: the limit of items investigating at
// once; the items investigating as the limit counts them, admitted and
// promoted items that have not started included; and the queued items that
// are neither. A standby's is all zeros.
type QueueStatus struct {
	Limit         int `json:"limit"`
	Investigating int `json:"investigating"`
	Queued        int `json:"queued"`
}

// RunnerStatus is the runner's part of the instance's status (Contract C4):
// its kind, whether its turns run sandboxed, and under Docker whether the
// engine answers and which of the deployment's images its last check found
// missing. Under the process runner the wire leaves the last two out, so
// Engine is "" and MissingImages nil; under Docker MissingImages is never
// null on the wire, and decodes as an empty list when none is missing. It is
// the harness's own copy of C4's wire form, not the admin API's type, which
// the depguard rule e2e forbids importing: a black-box harness that decodes
// the wire with its own type checks C4 from outside.
type RunnerStatus struct {
	Kind          string   `json:"kind"`
	Sandboxed     bool     `json:"sandboxed"`
	Engine        string   `json:"engine"`
	MissingImages []string `json:"missingImages"`
}

// Token returns the instance's admin token, which the deployment's secrets
// hold, so that a test can check that it reaches no one.
func (a *Admin) Token() string { return a.token }

// Do sends a request to the admin API, with the admin token unless header
// sets Authorization, and returns the answer's status and body. The test fails
// when no answer comes.
func (a *Admin) Do(t *testing.T, method, path string, header http.Header, body []byte) (status int, answer []byte) {
	t.Helper()
	status, _, answer = a.do(t, method, path, header, body)
	return status, answer
}

// do is Do, which also returns the answer's header.
func (a *Admin) do(t *testing.T, method, path string, header http.Header, body []byte) (status int, answered http.Header, answer []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), adminTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	for name, values := range header {
		req.Header[name] = values
	}
	if req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := a.client.Do(req) //nolint:gosec // G704: the instance's own admin API, on loopback.
	if err != nil {
		t.Fatalf("harness: %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("harness: read the answer to %s %s: %v", method, path, err)
	}
	return resp.StatusCode, resp.Header, answer
}

// Healthz answers GET /healthz, which needs no token: its status and body.
func (a *Admin) Healthz(t *testing.T) (status int, body string) {
	t.Helper()
	status, raw := a.Do(t, http.MethodGet, "/healthz", nil, nil)
	return status, string(raw)
}

// Readyz answers GET /readyz, which needs no token: its status and body.
func (a *Admin) Readyz(t *testing.T) (status int, body string) {
	t.Helper()
	status, raw := a.Do(t, http.MethodGet, "/readyz", nil, nil)
	return status, string(raw)
}

// Status answers GET /v1/status: its status, and the instance's status when
// it is 200.
func (a *Admin) Status(t *testing.T) (int, InstanceStatus) {
	t.Helper()
	code, body := a.Do(t, http.MethodGet, "/v1/status", nil, nil)
	var s InstanceStatus
	if code == http.StatusOK {
		decode(t, body, &s)
	}
	return code, s
}

// WaitStatus reads the instance's status until until accepts it, and returns
// that status. The test fails when none does within timeout.
func (a *Admin) WaitStatus(t *testing.T, until func(*InstanceStatus) bool, timeout time.Duration) InstanceStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		code, s := a.Status(t)
		if code == http.StatusOK && until(&s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("harness: the instance's status did not come to the one the test waits for within %v; the last (%d): %+v", timeout, code, s)
		}
		time.Sleep(adminPoll)
	}
}

// Open opens an item under key, POST /v1/items, and returns the answer's
// status and body, with its Retry-After header.
//
//nolint:gocritic // hugeParam: an opening is a value a scenario builds.
func (a *Admin) Open(t *testing.T, key string, o Opening) (int, Opened) {
	t.Helper()
	body, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	code, header, answer := a.do(t, http.MethodPost, "/v1/items", http.Header{"Idempotency-Key": {key}, "Content-Type": {"application/json"}}, body)
	var out Opened
	decode(t, answer, &out)
	out.RetryAfter = header.Get("Retry-After")
	return code, out
}

// OpenItem opens an item under key, asking again while the instance cannot
// open it yet, as its answer, 503, says: it is taking the lease over, or no
// configuration is current yet. It returns the item's id once the opening is
// answered 201, and fails the test on any other answer, or when none is 201
// within timeout.
//
//nolint:gocritic // hugeParam: an opening is a value a scenario builds.
func (a *Admin) OpenItem(t *testing.T, key string, o Opening, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		code, out := a.Open(t, key, o)
		switch {
		case code == http.StatusCreated:
			return out.ID
		case code != http.StatusServiceUnavailable:
			t.Fatalf("harness: the opening was answered %d: %s", code, out.Error)
		case time.Now().After(deadline):
			t.Fatalf("harness: the opening was not answered 201 within %v; the last answer was %d: %s", timeout, code, out.Error)
		}
		time.Sleep(adminPoll)
	}
}

// Item answers GET /v1/items/{id}: its status, and the item's view when it is
// 200, or the error's text.
func (a *Admin) Item(t *testing.T, id string) (int, ItemView) {
	t.Helper()
	code, body := a.Do(t, http.MethodGet, "/v1/items/"+id, nil, nil)
	var v ItemView
	decode(t, body, &v)
	return code, v
}

// WaitItem reads an item's view until until accepts it, and returns that
// view. The test fails when none does within timeout.
func (a *Admin) WaitItem(t *testing.T, id string, until func(*ItemView) bool, timeout time.Duration) ItemView {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		code, v := a.Item(t, id)
		if code == http.StatusOK && until(&v) {
			return v
		}
		if time.Now().After(deadline) {
			last, _ := json.Marshal(v)
			t.Fatalf("harness: item %s did not come to the state the test waits for within %v; its last view (%d): %s", id, timeout, code, last)
		}
		time.Sleep(adminPoll)
	}
}

// Reason is the reason a person gives an act on an item, or the kill switch's
// on (Contract C4): its code, and its text, if any, which the platform
// redacts. GET /v1/kill-switch shows the latest row's so too.
type Reason struct {
	Code string `json:"code"`
	Text string `json:"text,omitempty"`
}

// ActBody is the body of an act on an item (Contract C4): its reason, nil for
// none, and whether it asks the act's consequences alone. Its zero value
// sends no body.
type ActBody struct {
	Reason  *Reason `json:"reason,omitempty"`
	Preview bool    `json:"preview,omitempty"`
}

// Consequences are what an act did, or what its preview says it would do
// (Contract C4), each only where it applies: the runs that stop; the items
// that start, close or are not staffed; when the item closes by itself, or
// that its auto-close is paused; and the items investigating, as the limit
// counts them, and the limit, which every act on an item gives, and the kill
// switch's preview leaves out, nil; an extension's effect on its run's budget,
// and whether it resumes the run; and a link's or an unlink's effect on its
// item's incident.
type Consequences struct {
	Stops           []StoppedRun        `json:"stops"`
	Starts          []string            `json:"starts"`
	Closes          []string            `json:"closes"`
	Unstaffed       []string            `json:"unstaffed"`
	ClosesAt        time.Time           `json:"closesAt"`
	AutoClosePaused bool                `json:"autoClosePaused"`
	Investigating   *int                `json:"investigating"`
	Limit           *int                `json:"limit"`
	Budget          *BudgetChangeView   `json:"budget"`
	Resumes         bool                `json:"resumes"`
	Incident        *IncidentChangeView `json:"incident"`
}

// BudgetChangeView is an extension's effect on its run's budget (Contract
// C4): the budget before and after it, and what the run used.
type BudgetChangeView struct {
	Before AmountView `json:"before"`
	After  AmountView `json:"after"`
	Used   AmountView `json:"used"`
}

// IncidentChangeView is a link's or an unlink's effect on its item's
// incident (Contract C4): the incident it leaves; the one it joins, which an
// unlink's preview leaves out, ""; and that one's items after the act,
// sorted.
type IncidentChangeView struct {
	From  string   `json:"from"`
	To    string   `json:"to"`
	Items []string `json:"items"`
}

// StoppedRun is a run an act stops, or would stop (Contract C4): its item,
// its latest round, 0 before its first, and the reason it stops for. A kill
// switch's answer names the runs it stopped so too, without a reason, which
// is then "".
type StoppedRun struct {
	Item   string `json:"item"`
	Run    string `json:"run"`
	Round  int    `json:"round"`
	Reason string `json:"reason"`
}

// Acted is the answer to an act on an item (Contract C4): whether the act
// changed the item, its consequences, and the item's view once the act is
// done, nil when the instance could not read it; a preview's says it is one,
// and gives its consequences alone. A refusal gives its Error and Code
// instead, and one to be tried again in a moment its Retry-After header.
type Acted struct {
	Changed      bool         `json:"changed"`
	Preview      bool         `json:"preview"`
	Consequences Consequences `json:"consequences"`
	Item         *ItemView    `json:"item"`
	Error        string       `json:"error"`
	Code         string       `json:"code"`
	RetryAfter   string       `json:"-"`
}

// KillSwitchState is the kill switch as its table holds it, GET
// /v1/kill-switch (Contract C4): its state and its latest row, 0 before the
// first; from the first row on, the latest row's time and who wrote it; and
// the latest row's reason, nil before the first row and for a row without
// one. A refusal gives its Error and Code instead.
type KillSwitchState struct {
	On     bool      `json:"on"`
	Seq    int64     `json:"seq"`
	Since  time.Time `json:"since"`
	By     string    `json:"by"`
	Reason *Reason   `json:"reason"`
	Error  string    `json:"error"`
	Code   string    `json:"code"`
}

// KillSwitchAnswer is the answer to a request to turn the kill switch on or
// off, POST /v1/kill-switch (Contract C4): the switch's state and its latest
// row after the request; whether the request appended a row; whether it was
// deferred, recorded during a drain for the next leader to act on; the runs
// it stopped; the items it left not staffed; at an off, the items the latest
// on left not staffed, with their states now, and whether that list could not
// be read in full; and the items the kill switch's rule still acts on, nil
// when the answer leaves them out, as a deferred one does. A refusal gives
// its Error and Code instead, and one to be tried again in a moment its
// Retry-After header.
type KillSwitchAnswer struct {
	On                    bool            `json:"on"`
	Seq                   int64           `json:"seq"`
	Changed               bool            `json:"changed"`
	Deferred              bool            `json:"deferred"`
	Stopped               []StoppedRun    `json:"stopped"`
	Unstaffed             []string        `json:"unstaffed"`
	StayedUnstaffed       []UnstaffedItem `json:"stayedUnstaffed"`
	StayedUnstaffedUnread bool            `json:"stayedUnstaffedUnread"`
	Owed                  []string        `json:"owed"`
	Error                 string          `json:"error"`
	Code                  string          `json:"code"`
	RetryAfter            string          `json:"-"`
}

// UnstaffedItem is an item the latest on left not staffed, as an off's
// answer names it (Contract C4): its id, and its state now, "" when it could
// not be read.
type UnstaffedItem struct {
	Item  string `json:"item"`
	State string `json:"state"`
}

// Act sends POST /v1/items/{id}/{act}, an act on an item whose body holds a
// reason and a preview alone (Contract C4): promote, close, reopen or unlink,
// under the Idempotency-Key key, "" sending none, and with body, whose zero
// value sends none. A preview takes no key, so Act sends none with one,
// whatever key says. It returns the answer's status and body, with its
// Retry-After header.
func (a *Admin) Act(t *testing.T, id, act, key string, body ActBody) (int, Acted) {
	t.Helper()
	var sent any
	if body != (ActBody{}) {
		sent = body
	}
	return a.act(t, id, act, key, sent, body.Preview)
}

// act sends the act on an item that act names, under key, with body, nil
// sending none, a preview as preview says, which takes no key (see Act).
func (a *Admin) act(t *testing.T, id, act, key string, body any, preview bool) (int, Acted) {
	t.Helper()
	header := http.Header{}
	if key != "" && !preview {
		header.Set("Idempotency-Key", key)
	}
	var raw []byte
	if body != nil {
		raw = encode(t, body)
		header.Set("Content-Type", "application/json")
	}
	code, answered, answer := a.do(t, http.MethodPost, "/v1/items/"+id+"/"+act, header, raw)
	var out Acted
	decode(t, answer, &out)
	out.RetryAfter = answered.Get("Retry-After")
	return code, out
}

// ExtendBody is the body of an extension (Contract C4): its amount, each
// dimension nil, or "", to leave it out: rounds, a wall clock as a duration
// with its unit, such as 5m, and cost-weighted tokens; its reason, nil for
// none; and whether it asks the extension's consequences alone. The admin
// API refuses one that asks nothing above zero.
type ExtendBody struct {
	Rounds    *int    `json:"rounds,omitempty"`
	WallClock string  `json:"wallClock,omitempty"`
	Tokens    *int64  `json:"tokens,omitempty"`
	Reason    *Reason `json:"reason,omitempty"`
	Preview   bool    `json:"preview,omitempty"`
}

// Extend extends an item's run under key by the amount b asks, POST
// /v1/items/{id}/extend (see Act): a stopped run's extension resumes it.
func (a *Admin) Extend(t *testing.T, id, key string, b ExtendBody) (int, Acted) {
	t.Helper()
	return a.act(t, id, "extend", key, b, b.Preview)
}

// Link links an item under key into the incident of the item with, POST
// /v1/items/{id}/link, with reason, nil for none (see Act).
func (a *Admin) Link(t *testing.T, id, key, with string, reason *Reason) (int, Acted) {
	t.Helper()
	return a.act(t, id, "link", key, struct {
		With   string  `json:"with"`
		Reason *Reason `json:"reason,omitempty"`
	}{With: with, Reason: reason}, false)
}

// Unlink gives an item under key an incident of its own, POST
// /v1/items/{id}/unlink, with reason, nil sending no body (see Act).
func (a *Admin) Unlink(t *testing.T, id, key string, reason *Reason) (int, Acted) {
	t.Helper()
	return a.Act(t, id, "unlink", key, ActBody{Reason: reason})
}

// Promote promotes an item under key, an act with no body (see Act).
func (a *Admin) Promote(t *testing.T, id, key string) (int, Acted) {
	t.Helper()
	return a.Act(t, id, "promote", key, ActBody{})
}

// Close closes an item under key, an act with no body (see Act).
func (a *Admin) Close(t *testing.T, id, key string) (int, Acted) {
	t.Helper()
	return a.Act(t, id, "close", key, ActBody{})
}

// Reopen reopens an item under key, an act with no body (see Act).
func (a *Admin) Reopen(t *testing.T, id, key string) (int, Acted) {
	t.Helper()
	return a.Act(t, id, "reopen", key, ActBody{})
}

// KillSwitch sends GET /v1/kill-switch, and returns the answer's status and
// body: the kill switch as its table holds it, or the refusal.
func (a *Admin) KillSwitch(t *testing.T) (int, KillSwitchState) {
	t.Helper()
	code, body := a.Do(t, http.MethodGet, "/v1/kill-switch", nil, nil)
	var out KillSwitchState
	decode(t, body, &out)
	return code, out
}

// SetKillSwitch sends POST /v1/kill-switch (Contract C4): the state asked,
// on, and after, the latest row the sender saw, 0 before the first, with the
// reason, which nil leaves out. It returns the answer's status and body, with
// its Retry-After header.
func (a *Admin) SetKillSwitch(t *testing.T, on bool, after int64, reason *Reason) (int, KillSwitchAnswer) {
	t.Helper()
	raw := encode(t, struct {
		On     bool    `json:"on"`
		After  int64   `json:"after"`
		Reason *Reason `json:"reason,omitempty"`
	}{On: on, After: after, Reason: reason})
	code, answered, answer := a.do(t, http.MethodPost, "/v1/kill-switch", http.Header{"Content-Type": {"application/json"}}, raw)
	var out KillSwitchAnswer
	decode(t, answer, &out)
	out.RetryAfter = answered.Get("Retry-After")
	return code, out
}

// encode encodes a request's JSON body, failing the test when it cannot.
func encode(t *testing.T, v any) []byte {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return body
}

// decode decodes an answer's JSON body, failing the test when it is not JSON.
func decode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("harness: the admin API's answer is not JSON: %v\n%s", err, body)
	}
}
