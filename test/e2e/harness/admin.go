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
// refused.
type Opened struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Error string `json:"error"`
}

// ItemView is an item as GET /v1/items/{id} shows it.
type ItemView struct {
	ID               string      `json:"id"`
	State            string      `json:"state"`
	Stop             string      `json:"stop"`
	HeldByLaterBuild bool        `json:"heldByLaterBuild"`
	Rounds           []RoundView `json:"rounds"`
	Error            string      `json:"error"`
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
	Leading  bool   `json:"leading"`
	Epoch    int64  `json:"epoch"`
	Schema   string `json:"schema"`
	Draining bool   `json:"draining"`
	Skipped  int    `json:"skipped"`
}

// Do sends a request to the admin API, with the admin token unless header
// sets Authorization, and returns the answer's status and body. The test fails
// when no answer comes.
func (a *Admin) Do(t *testing.T, method, path string, header http.Header, body []byte) (status int, answer []byte) {
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
	return resp.StatusCode, answer
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
// status and body.
//
//nolint:gocritic // hugeParam: an opening is a value a scenario builds.
func (a *Admin) Open(t *testing.T, key string, o Opening) (int, Opened) {
	t.Helper()
	body, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	code, answer := a.Do(t, http.MethodPost, "/v1/items", http.Header{"Idempotency-Key": {key}, "Content-Type": {"application/json"}}, body)
	var out Opened
	decode(t, answer, &out)
	return code, out
}

// notHeld is the admin API's answer to an opening while the instance does not
// hold the lease (C4).
const notHeld = "this instance does not hold the lease"

// OpenItem opens an item under key, asking again while the instance cannot
// open it yet: it does not hold the lease yet (409), or cannot answer yet
// (503), as before a configuration is current. It returns the item's id once
// the opening is answered 201, and fails the test on any other answer, or when
// none is 201 within timeout.
//
//nolint:gocritic // hugeParam: an opening is a value a scenario builds.
func (a *Admin) OpenItem(t *testing.T, key string, o Opening, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		code, out := a.Open(t, key, o)
		notYet := code == http.StatusConflict && out.Error == notHeld || code == http.StatusServiceUnavailable
		switch {
		case code == http.StatusCreated:
			return out.ID
		case !notYet:
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

// decode decodes an answer's JSON body, failing the test when it is not JSON.
func decode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("harness: the admin API's answer is not JSON: %v\n%s", err, body)
	}
}
