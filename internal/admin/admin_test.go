package admin_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/admin"
	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

const (
	tenant   = "example"
	tokenRef = "secret://admin/token"
	token    = "admin-token-4f1c9a2e7b"
)

// incident is an opening of C4's form.
const incident = `{"type": "incident", "title": "checkout fails", "routing": {"service": "checkout", "region": "eu"},
 "principal": "alice@example.com", "responders": ["bob@example.com"]}`

// coordinator opens items as the coordinator does for the admin API: once
// per id, answering an opening repeated under an id by the digest the first
// recorded (Decision 14). refuse, when set, refuses every opening.
type coordinator struct {
	mu      sync.Mutex
	opened  map[item.ID]string
	inputs  []app.OpenInput
	refuse  error
	skipped []item.ID
}

func (c *coordinator) Open(_ context.Context, in *app.OpenInput) (item.ID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputs = append(c.inputs, *in)
	switch digest, ok := c.opened[in.ID]; {
	case c.refuse != nil:
		return "", c.refuse
	case ok && digest != in.Digest:
		return "", app.ErrKeyReused
	case !ok:
		c.opened[in.ID] = in.Digest
	}
	return in.ID, nil
}

func (c *coordinator) Skipped() []item.ID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skipped
}

func (c *coordinator) opens() []app.OpenInput {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]app.OpenInput(nil), c.inputs...)
}

type leader struct {
	epoch          int64
	held, draining bool
}

func (l *leader) Status() (epoch int64, held, draining bool) { return l.epoch, l.held, l.draining }

type schema struct {
	state db.State
	err   error
}

func (s *schema) State(context.Context) (db.State, error) { return s.state, s.err }

// items are the items' views: an item the coordinator opened is
// investigating, unless views holds its view; another is no item.
type items struct {
	coord *coordinator
	views map[item.ID]app.View
	err   error
}

func (i *items) Item(_ context.Context, id item.ID) (app.View, error) {
	if i.err != nil {
		return app.View{}, i.err
	}
	if v, ok := i.views[id]; ok {
		return v, nil
	}
	i.coord.mu.Lock()
	defer i.coord.mu.Unlock()
	if _, ok := i.coord.opened[id]; ok {
		return app.View{ID: id, State: "investigating"}, nil
	}
	return app.View{}, app.ErrNoItem
}

// secrets are the secrets' store, read on each use; reads counts the reads.
type secrets struct {
	mu     sync.Mutex
	values map[string]string
	reads  int
}

func (s *secrets) Secret(_ context.Context, ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if v, ok := s.values[ref]; ok {
		return v, nil
	}
	return "", fmt.Errorf("files: no such secret: %s", ref)
}

func (s *secrets) set(ref, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value == "" {
		delete(s.values, ref)
		return
	}
	s.values[ref] = value
}

func (s *secrets) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// logLines holds what a JSON log writes, from any goroutine.
type logLines struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logLines) lines(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(l.buf.Bytes()))
	for sc.Scan() {
		var line map[string]any
		require.NoError(t, json.Unmarshal(sc.Bytes(), &line))
		out = append(out, line)
	}
	return out
}

// fixture is the admin API over fakes of its ports.
type fixture struct {
	coord   *coordinator
	leader  *leader
	schema  *schema
	items   *items
	secrets *secrets
	logs    *logLines
	api     http.Handler
}

func newFixture(maxBody int64) *fixture {
	f := &fixture{
		coord:   &coordinator{opened: map[item.ID]string{}},
		leader:  &leader{epoch: 3, held: true},
		schema:  &schema{state: db.Compatible},
		secrets: &secrets{values: map[string]string{tokenRef: token}},
		logs:    &logLines{},
	}
	f.items = &items{coord: f.coord, views: map[item.ID]app.View{}}
	f.api = admin.New(admin.Ports{Coordinator: f.coord, Leader: f.leader, Schema: f.schema, Items: f.items, Secrets: f.secrets}, admin.Options{
		Tenant: tenant, TokenRef: tokenRef, MaxBody: maxBody, Log: slog.New(slog.NewJSONHandler(f.logs, nil)),
	})
	return f
}

func (f *fixture) serve(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	return w
}

// request is a request from a loopback peer, with the admin token.
func request(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50123"
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// opening is a request to open an item under key, with body.
func opening(key, body string) *http.Request {
	r := request(http.MethodPost, "/v1/items", body)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}

// errorOf returns the text of an error answer, which is JSON.
func errorOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	require.Len(t, body, 1, w.Body.String())
	require.Contains(t, body, "error")
	return body["error"]
}

// The process's liveness, and whether it is ready, need no token: /healthz
// answers while the process is up, and /readyz while the schema is
// compatible and serve does not drain, a standby included. Otherwise
// /readyz answers 503 with the reason.
func TestHealthzAndReadyzNeedNoToken(t *testing.T) {
	f := newFixture(0)
	for path, want := range map[string]string{"/healthz": "ok", "/readyz": "ready"} {
		r := request(http.MethodGet, path, "")
		r.Header.Del("Authorization")

		w := f.serve(r)

		assert.Equal(t, http.StatusOK, w.Code, path)
		assert.Equal(t, want, w.Body.String(), path)
	}
	f.leader.held = false
	r := request(http.MethodGet, "/readyz", "")
	r.Header.Del("Authorization")
	assert.Equal(t, http.StatusOK, f.serve(r).Code, "a standby is ready")

	for name, tc := range map[string]struct {
		schema   schema
		draining bool
		reason   string
	}{
		"the schema behind this build":       {schema: schema{state: db.Behind}, reason: "behind"},
		"the schema incompatible with it":    {schema: schema{state: db.Incompatible}, reason: "incompatible"},
		"serve draining":                     {schema: schema{state: db.Compatible}, draining: true, reason: "draining"},
		"a schema that could not be checked": {schema: schema{err: errors.New("connection refused")}, reason: "could not be read"},
	} {
		f := newFixture(0)
		*f.schema, f.leader.draining = tc.schema, tc.draining
		r := request(http.MethodGet, "/readyz", "")
		r.Header.Del("Authorization")

		w := f.serve(r)

		assert.Equal(t, http.StatusServiceUnavailable, w.Code, name)
		assert.Contains(t, w.Body.String(), tc.reason, name)
		assert.NotContains(t, w.Body.String(), "connection refused", "%s: the error stays in the log", name)
	}
}

// Every /v1/ route, an unknown one included, needs the admin token as a
// bearer token: a request without it, with another token, or with another
// scheme is refused with 401, before anything is read or opened. The
// scheme's name is read in any case.
func TestEveryV1RouteNeedsTheToken(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/items", incident},
		{http.MethodGet, "/v1/items/" + string(admin.ItemID(tenant, "k")), ""},
		{http.MethodGet, "/v1/status", ""},
		{http.MethodGet, "/v1/nothing", ""},
	}
	f := newFixture(0)
	for _, route := range routes {
		for name, authorization := range map[string]string{
			"no token":        "",
			"another token":   "Bearer not-the-admin-token",
			"another scheme":  "Basic " + token,
			"a token cut off": "Bearer " + token[:len(token)-1],
			"no token at all": "Bearer ",
		} {
			r := request(route.method, route.path, route.body)
			r.Header.Set("Idempotency-Key", "k")
			r.Header.Set("Authorization", authorization)

			w := f.serve(r)

			assert.Equal(t, http.StatusUnauthorized, w.Code, "%s %s: %s", route.method, route.path, name)
			assert.NotEmpty(t, errorOf(t, w))
			assert.Contains(t, w.Header().Get("WWW-Authenticate"), "Bearer")
		}
	}
	assert.Empty(t, f.coord.opens(), "no item was opened")

	for _, route := range routes {
		r := request(route.method, route.path, route.body)
		r.Header.Set("Idempotency-Key", "k")
		r.Header.Set("Authorization", "bearer "+token)

		w := f.serve(r)

		assert.NotEqual(t, http.StatusUnauthorized, w.Code, "%s %s", route.method, route.path)
	}
	assert.Equal(t, http.StatusNotFound, f.serve(request(http.MethodGet, "/v1/nothing", "")).Code)
}

// The admin token is read from its secret on each request: once it changes,
// the old token is refused and the new one accepted. A token that cannot be
// read answers 503, and the reason stays in the log.
func TestTheTokenIsResolvedOnEachRequest(t *testing.T) {
	f := newFixture(0)
	require.Equal(t, http.StatusOK, f.serve(request(http.MethodGet, "/v1/status", "")).Code)

	f.secrets.set(tokenRef, "rotated-token")

	assert.Equal(t, http.StatusUnauthorized, f.serve(request(http.MethodGet, "/v1/status", "")).Code)
	r := request(http.MethodGet, "/v1/status", "")
	r.Header.Set("Authorization", "Bearer rotated-token")
	assert.Equal(t, http.StatusOK, f.serve(r).Code)

	f.secrets.set(tokenRef, "")
	w := f.serve(request(http.MethodGet, "/v1/status", ""))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.NotContains(t, errorOf(t, w), tokenRef)
	assert.Contains(t, fmt.Sprint(f.logs.lines(t)), "no such secret")
}

// digestOf is the digest an opening records: SHA-256 over the JSON of its
// type, routing, principal and responders, every object's keys sorted.
func digestOf(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// An opening names its item by its key (R15), and records the digest of its
// request but the title: the same key and request open one item, however
// often it is sent, as after a timeout (Review Focus 5); the same key with
// another request is refused; a retry that changes the title alone, which
// redaction may change, returns the item.
func TestOpeningAnItemByKey(t *testing.T) {
	f := newFixture(0)
	id := admin.ItemID(tenant, "retry-1")

	first := f.serve(opening("retry-1", incident))

	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	assert.JSONEq(t, fmt.Sprintf(`{"id": %q, "state": "investigating"}`, id), first.Body.String())
	opened := f.coord.opens()
	require.Len(t, opened, 1)
	assert.Equal(t, app.OpenInput{
		ID: id, Digest: digestOf(`{"principal":"alice@example.com","responders":["bob@example.com"],"routing":{"region":"eu","service":"checkout"},"type":"incident"}`),
		Type: "incident", Title: "checkout fails", Source: "person", Routing: map[string]string{"service": "checkout", "region": "eu"},
		Principal: "alice@example.com", Responders: []string{"bob@example.com"},
	}, opened[0])

	for name, body := range map[string]string{
		"the same request":             incident,
		"its routing in another order": strings.Replace(incident, `"service": "checkout", "region": "eu"`, `"region": "eu", "service": "checkout"`, 1),
		"another title alone":          strings.Replace(incident, "checkout fails", "checkout fails for everyone", 1),
		"its fields in another order":  `{"responders": ["bob@example.com"], "principal": "alice@example.com", "routing": {"region": "eu", "service": "checkout"}, "title": "x", "type": "incident"}`,
	} {
		w := f.serve(opening("retry-1", body))

		assert.Equal(t, http.StatusCreated, w.Code, "%s: %s", name, w.Body.String())
		assert.JSONEq(t, fmt.Sprintf(`{"id": %q, "state": "investigating"}`, id), w.Body.String(), name)
	}
	for name, body := range map[string]string{
		"another type":      strings.Replace(incident, `"incident"`, `"change"`, 1),
		"another routing":   strings.Replace(incident, `"checkout"`, `"payments"`, 1),
		"more routing":      strings.Replace(incident, `"region": "eu"`, `"region": "eu", "zone": "a"`, 1),
		"another principal": strings.Replace(incident, "alice@", "carol@", 1),
		"other responders":  strings.Replace(incident, `["bob@example.com"]`, `["bob@example.com", "dan@example.com"]`, 1),
	} {
		w := f.serve(opening("retry-1", body))

		assert.Equal(t, http.StatusConflict, w.Code, name)
		assert.NotEmpty(t, errorOf(t, w), name)
	}
	other := f.serve(opening("retry-2", incident))
	require.Equal(t, http.StatusCreated, other.Code)
	assert.JSONEq(t, fmt.Sprintf(`{"id": %q, "state": "investigating"}`, admin.ItemID(tenant, "retry-2")), other.Body.String(), "another key, another item")
}

// An opening with no routing and no responders is digested as one with
// them empty: leaving a field out and giving it empty are the same request.
func TestAnOpeningsEmptyFieldsAreItsMissingOnes(t *testing.T) {
	f := newFixture(0)

	require.Equal(t, http.StatusCreated, f.serve(opening("k", `{"type": "incident", "title": "t"}`)).Code)
	require.Equal(t, http.StatusCreated, f.serve(opening("k", `{"type": "incident", "title": "t", "routing": {}, "responders": [], "principal": ""}`)).Code)

	opened := f.coord.opens()
	require.Len(t, opened, 2)
	assert.Equal(t, digestOf(`{"principal":"","responders":[],"routing":{},"type":"incident"}`), opened[0].Digest)
	assert.Equal(t, opened[0].Digest, opened[1].Digest)
}

// An opening needs its key, of 1 to 128 printable ASCII characters, once:
// without one it is refused with 400, and nothing is opened.
func TestAnOpeningWithoutAKeyIsRefused(t *testing.T) {
	f := newFixture(0)
	for name, key := range map[string]string{
		"no key":               "",
		"a key too long":       strings.Repeat("k", 129),
		"a key with a control": "retry\x7f1",
		"a key beyond ASCII":   "retry-é",
	} {
		w := f.serve(opening(key, incident))

		assert.Equal(t, http.StatusBadRequest, w.Code, name)
		assert.NotEmpty(t, errorOf(t, w), name)
	}
	twice := opening("a", incident)
	twice.Header.Add("Idempotency-Key", "b")
	assert.Equal(t, http.StatusBadRequest, f.serve(twice).Code, "two keys")
	assert.Empty(t, f.coord.opens())

	w := f.serve(opening(strings.Repeat("k", 128), incident))
	assert.Equal(t, http.StatusCreated, w.Code, "a key of 128 characters")
	w = f.serve(opening("a key, with spaces: and ~punctuation!", incident))
	assert.Equal(t, http.StatusCreated, w.Code, "printable ASCII")
}

// A body that does not decode as an opening is refused with 400, and its
// text is not repeated: not JSON, a field the opening does not have, a field
// of another kind, or more than one value.
func TestAnOpeningWhoseBodyDoesNotDecodeIsRefused(t *testing.T) {
	f := newFixture(0)
	for name, body := range map[string]string{
		"not JSON":                 `type=incident`,
		"a field it does not have": `{"type": "incident", "titel": "checkout fails"}`,
		"a field of another kind":  `{"type": "incident", "routing": ["checkout"]}`,
		"more than one value":      `{"type": "incident"} {"type": "change"}`,
		"nothing":                  ``,
	} {
		w := f.serve(opening("k", body))

		assert.Equal(t, http.StatusBadRequest, w.Code, name)
		assert.NotContains(t, errorOf(t, w), "titel", name)
	}
	assert.Empty(t, f.coord.opens())
}

// An opening the coordinator refuses is answered by its refusal (C4): an
// instance that does not hold the lease, or lost it, 409; an item held by a
// later build, 409 "held by a later build", however the refusal reached the
// API; a key reused for another request, 409; an invalid opening, 400, with
// the coordinator's reason; no current revision, 503. An item whose opening
// is still being recorded answers 503, to be tried again, as does a failure
// of the stores, whose error stays in the log beside the item's id.
func TestOpeningAnswersByTheCoordinatorsRefusal(t *testing.T) {
	id := admin.ItemID(tenant, "k")
	for _, tc := range []struct {
		name   string
		err    error
		status int
		text   string
		logged bool
	}{
		{name: "not held", err: app.ErrNotHeld, status: http.StatusConflict},
		{name: "the lease lost", err: fmt.Errorf("record item %s: %w", id, app.ErrLeaseLost), status: http.StatusConflict},
		{name: "skipped", err: app.ErrSkipped, status: http.StatusConflict, text: "held by a later build"},
		{name: "skipped, of a later version", err: fmt.Errorf("%w: %w", app.ErrSkipped, item.ErrVersion), status: http.StatusConflict, text: "held by a later build"},
		{name: "a record of a later version", err: fmt.Errorf("record item %s: %w", id, item.ErrVersion), status: http.StatusConflict, text: "held by a later build"},
		{name: "a board event of a later version", err: board.ErrVersion, status: http.StatusConflict, text: "held by a later build"},
		{name: "the key reused", err: app.ErrKeyReused, status: http.StatusConflict},
		{
			name: "invalid", err: fmt.Errorf("%w: the configuration has no such work item type", app.ErrInvalid), status: http.StatusBadRequest,
			text: "the configuration has no such work item type",
		},
		{name: "no revision", err: app.ErrNoRevision, status: http.StatusServiceUnavailable},
		{name: "still being recorded", err: fmt.Errorf("record item %s: %w: an item opens with its first record, once", id, item.ErrInvalid),
			status: http.StatusServiceUnavailable, text: "try again", logged: true},
		{name: "the stores failed", err: errors.New("the database blinked"), status: http.StatusServiceUnavailable, logged: true},
	} {
		f := newFixture(0)
		f.coord.refuse = tc.err

		w := f.serve(opening("k", incident))

		assert.Equal(t, tc.status, w.Code, tc.name)
		text := errorOf(t, w)
		if tc.text != "" {
			assert.Contains(t, text, tc.text, tc.name)
		}
		if tc.status == http.StatusConflict && tc.text == "" {
			assert.NotContains(t, text, "later build", tc.name)
		}
		assert.NotContains(t, text, "database", "%s: the stores' errors stay in the log", tc.name)
		lines := f.logs.lines(t)
		if !tc.logged {
			assert.Empty(t, lines, tc.name)
			continue
		}
		require.Len(t, lines, 1, tc.name)
		assert.Equal(t, string(id), lines[0]["item"], tc.name)
		assert.Equal(t, tc.err.Error(), lines[0]["err"], tc.name)
		assert.Equal(t, "ERROR", lines[0]["level"], tc.name)
	}
}

// A body over the cap is refused with 413, whether or not the request
// declared its length, and nothing is opened; a body at the cap is read.
func TestABodyOverTheCapIsRefused(t *testing.T) {
	const capacity = 64 << 10
	f := newFixture(capacity)
	head, tail := `{"type": "incident", "title": "`, `"}`
	at := head + strings.Repeat("x", capacity-len(head)-len(tail)) + tail
	require.Len(t, at, capacity)
	over := head + strings.Repeat("x", capacity+1-len(head)-len(tail)) + tail

	declared := opening("k", over)
	undeclared := opening("k", over)
	undeclared.ContentLength = -1
	for name, r := range map[string]*http.Request{"declared": declared, "undeclared": undeclared} {
		w := f.serve(r)

		assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code, name)
		assert.NotEmpty(t, errorOf(t, w), name)
	}
	assert.Empty(t, f.coord.opens())

	assert.Equal(t, http.StatusCreated, f.serve(opening("k", at)).Code, "a body at the cap")
}

// The cap of an opening's body is 64 KiB unless Options say otherwise.
func TestTheBodysCapIs64KiBByDefault(t *testing.T) {
	f := newFixture(0)
	head, tail := `{"type": "incident", "title": "`, `"}`

	w := f.serve(opening("k", head+strings.Repeat("x", 64<<10)+tail))

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

// An item's view is C4's JSON: its state and its run's stop, and each round
// with its evidence and the board's report of it, as the board marshals it;
// a round still open shows no turn end. An unknown id answers 404, and a
// view the stores cannot give, 503, with the error in the log beside the
// item's id.
func TestAnItemsViewShowsItsRoundsEvidenceAndReports(t *testing.T) {
	f := newFixture(0)
	id := admin.ItemID(tenant, "k")
	reports := []board.Report{
		{
			Assessment: board.Assessment{Hypotheses: []board.HypothesisView{}},
			Facts:      board.RoundFacts{Ended: []string{"alpha", "beta"}, Entries: 3, FailedCalls: 1},
		},
		{Assessment: board.Assessment{Conclusion: "h-1"}, Facts: board.RoundFacts{Ended: []string{"alpha", "beta"}}, Stopped: true},
		{Facts: board.RoundFacts{Ended: []string{}}},
	}
	f.items.views[id] = app.View{ID: id, State: "investigating", Rounds: []app.RoundView{
		{
			Round: 1, Ended: "turns", TurnEnds: []string{"alpha", "beta"}, FailedCalls: 1, FailedToolCalls: 2, Report: reports[0],
		},
		{Round: 2, WrapUp: true, Ended: "deadline", Quiet: true, TurnEnds: []string{"alpha"}, Settled: true, Report: reports[1]},
		{Round: 3, Report: reports[2]},
	}}
	marshalled := make([]string, len(reports))
	for i := range reports {
		raw, err := json.Marshal(reports[i])
		require.NoError(t, err)
		marshalled[i] = string(raw)
	}

	w := f.serve(request(http.MethodGet, "/v1/items/"+string(id), ""))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.JSONEq(t, fmt.Sprintf(`{"id": %q, "state": "investigating", "stop": "", "heldByLaterBuild": false, "rounds": [
		{"round": 1, "wrapUp": false, "ended": "turns", "quiet": false,
		 "evidence": {"turnEnds": ["alpha", "beta"], "failedModelCalls": 1, "failedToolCalls": 2, "settled": false}, "report": %s},
		{"round": 2, "wrapUp": true, "ended": "deadline", "quiet": true,
		 "evidence": {"turnEnds": ["alpha"], "failedModelCalls": 0, "failedToolCalls": 0, "settled": true}, "report": %s},
		{"round": 3, "wrapUp": false, "ended": "", "quiet": false,
		 "evidence": {"turnEnds": [], "failedModelCalls": 0, "failedToolCalls": 0, "settled": false}, "report": %s}]}`,
		id, marshalled[0], marshalled[1], marshalled[2]), w.Body.String())

	f.items.views[id] = app.View{ID: id, State: "awaiting_close", Stop: "not_staffed"}
	w = f.serve(request(http.MethodGet, "/v1/items/"+string(id), ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, fmt.Sprintf(`{"id": %q, "state": "awaiting_close", "stop": "not_staffed", "heldByLaterBuild": false, "rounds": []}`, id),
		w.Body.String(), "an item without rounds shows an empty list")

	w = f.serve(request(http.MethodGet, "/v1/items/"+string(admin.ItemID(tenant, "unknown")), ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.NotEmpty(t, errorOf(t, w))

	f.items.err = errors.New("the database blinked")
	w = f.serve(request(http.MethodGet, "/v1/items/"+string(id), ""))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.NotContains(t, errorOf(t, w), "database")
	lines := f.logs.lines(t)
	require.Len(t, lines, 1)
	assert.Equal(t, string(id), lines[0]["item"])
	assert.Equal(t, "the database blinked", lines[0]["err"])
}

// An item held by a later build answers that, and nothing else (C4).
func TestASkippedItemsViewSaysHeldByALaterBuild(t *testing.T) {
	f := newFixture(0)
	id := admin.ItemID(tenant, "k")
	f.items.views[id] = app.View{ID: id, HeldByLaterBuild: true}

	w := f.serve(request(http.MethodGet, "/v1/items/"+string(id), ""))

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, fmt.Sprintf(`{"id": %q, "heldByLaterBuild": true}`, id), w.Body.String())

	w = f.serve(request(http.MethodGet, "/v1/items/"+string(admin.ItemID(tenant, "unknown")), ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// The status says whether this instance leads, its epoch, the schema's
// state, whether it drains, and how many items it skips.
func TestStatusShowsLeadingEpochSchemaDrainingAndSkipped(t *testing.T) {
	for name, tc := range map[string]struct {
		leader  leader
		schema  db.State
		skipped []item.ID
		want    string
	}{
		"a leader": {
			leader: leader{epoch: 3, held: true}, schema: db.Compatible, skipped: []item.ID{"a", "b"},
			want: `{"leading": true, "epoch": 3, "schema": "compatible", "draining": false, "skipped": 2}`,
		},
		"a standby": {
			leader: leader{epoch: 2}, schema: db.Behind,
			want: `{"leading": false, "epoch": 2, "schema": "behind", "draining": false, "skipped": 0}`,
		},
		"a leader that drains": {
			leader: leader{epoch: 4, held: true, draining: true}, schema: db.Incompatible, skipped: []item.ID{"a"},
			want: `{"leading": true, "epoch": 4, "schema": "incompatible", "draining": true, "skipped": 1}`,
		},
	} {
		f := newFixture(0)
		*f.leader, f.schema.state, f.coord.skipped = tc.leader, tc.schema, tc.skipped

		w := f.serve(request(http.MethodGet, "/v1/status", ""))

		require.Equal(t, http.StatusOK, w.Code, name)
		assert.JSONEq(t, tc.want, w.Body.String(), name)
	}

	f := newFixture(0)
	f.schema.err = errors.New("connection refused")
	w := f.serve(request(http.MethodGet, "/v1/status", ""))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, "a schema that cannot be read")
	assert.NotContains(t, errorOf(t, w), "connection refused")
}

// The admin API answers loopback peers only (Decision 14), beside serve's
// refusal of a listener's address that is not on loopback: any other peer
// is refused with 403, before its token is read.
func TestARequestFromAPeerNotOnLoopbackIsRefused(t *testing.T) {
	f := newFixture(0)
	for _, peer := range []string{"192.0.2.1:1234", "[2001:db8::1]:1234", "10.0.0.1:80", "[::ffff:192.0.2.1]:1234", "not an address"} {
		for _, path := range []string{"/healthz", "/v1/status"} {
			r := request(http.MethodGet, path, "")
			r.RemoteAddr = peer

			w := f.serve(r)

			assert.Equal(t, http.StatusForbidden, w.Code, "%s %s", peer, path)
			assert.NotEmpty(t, errorOf(t, w))
		}
	}
	assert.Zero(t, f.secrets.count(), "no token was read for them")

	for _, peer := range []string{"127.0.0.1:1234", "127.0.0.9:1234", "[::1]:1234", "[::ffff:127.0.0.1]:1234"} {
		r := request(http.MethodGet, "/v1/status", "")
		r.RemoteAddr = peer

		assert.Equal(t, http.StatusOK, f.serve(r).Code, peer)
	}
}

// An item's id from its key (R15) is the same for the same tenant and key,
// and another for another of either: a UUID of version 8 and the RFC 4122
// variant, in canonical form, whose other bits are the first 128 of SHA-256
// over the tenant, a zero byte, and the key.
func TestItemIDsAreUUIDsOfVersion8(t *testing.T) {
	id := admin.ItemID("example", "retry-1")

	assert.Equal(t, id, admin.ItemID("example", "retry-1"))
	assert.NotEqual(t, id, admin.ItemID("example", "retry-2"))
	assert.NotEqual(t, id, admin.ItemID("other", "retry-1"))
	assert.NotEqual(t, admin.ItemID("ab", "c"), admin.ItemID("a", "bc"), "the zero byte parts the tenant from the key")
	u, err := uuid.Parse(string(id))
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(8), u.Version())
	assert.Equal(t, uuid.RFC4122, u.Variant())
	assert.Equal(t, u.String(), string(id), "the canonical form")
	sum := sha256.Sum256([]byte("example\x00retry-1"))
	want := sum[:16]
	want[6] = want[6]&0x0f | 0x80
	want[8] = want[8]&0x3f | 0x80
	assert.Equal(t, want, u[:])
}
