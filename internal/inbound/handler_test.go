package inbound_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/inbound"
	"github.com/jchen0426/agent-swarm/internal/inbound/mapping"
	"github.com/jchen0426/agent-swarm/internal/registry/celeval"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/registry/memfs"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

const (
	tenant  = "tenant"
	current = "s3cret"
	commitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	alerts  = "trigger-mappings/alerts.yaml"
	hmacKey = "authentication: {hmacSecretRef: secret://inbound/alerting, maxAge: 5m}"
)

var errStore = errors.New("the store did not answer")

// exampleFiles are the example configuration's files, its alerting mapping
// without its checks, as Plan 7c-2 runs them.
func exampleFiles(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	root := os.DirFS("../../examples/config")
	require.NoError(t, fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files[path], err = fs.ReadFile(root, path)
		return err
	}))
	var kept []string
	for line := range strings.SplitSeq(string(files[alerts]), "\n") {
		if !strings.HasPrefix(line, "  checks:") && !strings.Contains(line, "refuses its events") {
			kept = append(kept, line)
		}
	}
	files[alerts] = []byte(strings.Join(kept, "\n"))
	return files
}

// revision is the example configuration under commit, as edit changes the
// alerting mapping's text.
func revision(t *testing.T, commit string, edit func(string) string) *config.Revision {
	t.Helper()
	files := exampleFiles(t)
	if edit != nil {
		files[alerts] = []byte(edit(string(files[alerts])))
	}
	fsys, err := memfs.New(files)
	require.NoError(t, err)
	cc, err := celeval.New()
	require.NoError(t, err)
	rev, err := config.Build(fsys, commit, cc)
	require.NoError(t, err)
	return rev
}

// alert is one alert of the example alerting source's body; an empty
// fingerprint or service is left out.
func alert(fingerprint, service, summary string, startsAt time.Time) string {
	labels := map[string]any{"rule_id": "r1"}
	if service != "" {
		labels["service"] = service
	}
	a := map[string]any{
		"status": "firing", "startsAt": startsAt.UTC().Format(time.RFC3339), "endsAt": "", "labels": labels,
		"annotations": map[string]any{"summary": summary},
	}
	if fingerprint != "" {
		a["fingerprint"] = fingerprint
	}
	b, err := json.Marshal(a)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func bodyOf(items ...string) []byte {
	return []byte(`{"alerts":[` + strings.Join(items, ",") + `]}`)
}

// ingester is the coordinator as the handler ingests through it: it notes
// each call, fails the nth as fail says, counting from 1, and otherwise
// answers unroutable or refused for such an event, and opened for any other.
type ingester struct {
	mu   sync.Mutex
	ins  []app.IngestInput
	fail func(n int) error
}

func (c *ingester) Ingest(_ context.Context, in *app.IngestInput) (app.IngestResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ins = append(c.ins, app.IngestInput{Event: new(*in.Event), ID: in.ID})
	if c.fail != nil {
		if err := c.fail(len(c.ins)); err != nil {
			return app.IngestResult{}, err
		}
	}
	switch e := in.Event; {
	case e.Unroutable != "":
		return app.IngestResult{Outcome: app.IngestUnroutable, Reason: e.Unroutable}, nil
	case e.Refused != "":
		return app.IngestResult{Outcome: app.IngestRefused, Reason: e.Refused}, nil
	}
	return app.IngestResult{Outcome: app.IngestOpened, Item: in.ID}, nil
}

func (c *ingester) calls() []app.IngestInput {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]app.IngestInput(nil), c.ins...)
}

type leader struct {
	mu             sync.Mutex
	held, draining bool
}

func (l *leader) Status() (epoch int64, held, draining bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return 4, l.held, l.draining
}

func (l *leader) set(held, draining bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held, l.draining = held, draining
}

type revisions struct {
	rev atomic.Pointer[config.Revision]
}

func (r *revisions) Current() *config.Revision { return r.rev.Load() }

// secretFile is the secret store: the text of the file, or the error a read
// answers; it counts the reads and notes the references read.
type secretFile struct {
	mu    sync.Mutex
	text  string
	err   error
	reads []string
}

func (s *secretFile) Secret(_ context.Context, ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, ref)
	return s.text, s.err
}

func (s *secretFile) set(text string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.text, s.err = text, err
}

func (s *secretFile) read() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reads)
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// lines are the log's lines, each decoded.
func (l *lockedBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(l.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m), line)
		out = append(out, m)
	}
	return out
}

// matching are the log's lines whose message holds msg.
func (l *lockedBuffer) matching(t *testing.T, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range l.lines(t) {
		if s, _ := line["msg"].(string); strings.Contains(s, msg) {
			out = append(out, line)
		}
	}
	return out
}

// fixture is a handler over fake ports: the example revision, a leader that
// holds, the secret file holding current, and the database's time at now.
type fixture struct {
	t       *testing.T
	coord   *ingester
	leader  *leader
	revs    *revisions
	secrets *secretFile
	logs    *lockedBuffer
	now     atomic.Pointer[time.Time]
	h       http.Handler
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, coord: &ingester{}, leader: &leader{held: true}, revs: &revisions{}, secrets: &secretFile{text: current}, logs: &lockedBuffer{}}
	f.revs.rev.Store(revision(t, commitA, nil))
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	f.now.Store(&at)
	f.h = inbound.NewHandler(&inbound.Ports{
		Coordinator: f.coord, Leader: f.leader, Revisions: f.revs, Secrets: f.secrets,
		Clock: func() time.Time { return *f.now.Load() }, Tenant: tenant,
		Log: slog.New(slog.NewJSONHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	return f
}

func (f *fixture) clock() time.Time { return *f.now.Load() }

// signed is a request to source of body, stamped at, and signed with secret.
func signed(source string, body []byte, at time.Time, secret string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/inbound/"+source, bytes.NewReader(body))
	ts := stamp(at)
	r.Header.Set(inbound.HeaderTimestamp, ts)
	r.Header.Set(inbound.HeaderSignature, "v1="+sign(secret, ts, body))
	return r
}

// post sends body to the alerting source, signed with the current secret at
// the database's time.
func (f *fixture) post(body []byte) *httptest.ResponseRecorder {
	return f.send(signed("alerting", body, f.clock(), current))
}

func (f *fixture) send(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

// codeOf is a refusal's code.
func codeOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var answer struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &answer), w.Body.String())
	return answer.Error
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// The routes answer in the order the table checks them; a 503 carries
// Retry-After: 1; nothing is asked of the coordinator unless the row says so.
func TestTheInboundRoutesAnswerAsTheTableSays(t *testing.T) {
	at := time.Date(2026, 11, 9, 11, 59, 0, 0, time.UTC)
	one := bodyOf(alert("f1", "checkout", "checkout fails", at))
	two := bodyOf(alert("f1", "checkout", "checkout fails", at), alert("f2", "checkout", "checkout fails", at))
	notJSON := []byte(`{"alerts":[`)
	for _, tc := range []struct {
		name    string
		setup   func(f *fixture)
		request func(f *fixture) *http.Request
		status  int
		code    string
		// asked are the reasons of the events asked of the coordinator, ""
		// for an event that is neither unroutable nor refused.
		asked []string
	}{
		{name: "another path", request: func(*fixture) *http.Request { return httptest.NewRequest(http.MethodGet, "/v1/items", http.NoBody) }, status: 404, code: "no_such_route"},
		{name: "a path below a source", request: func(f *fixture) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/inbound/alerting/more", bytes.NewReader(one))
		}, status: 404, code: "no_such_route"},
		{name: "another method", request: func(*fixture) *http.Request {
			return httptest.NewRequest(http.MethodGet, "/inbound/alerting", http.NoBody)
		}, status: 405, code: "method_not_allowed"},
		{name: "no current revision", setup: func(f *fixture) { f.revs.rev.Store(nil) }, request: func(f *fixture) *http.Request { return signed("alerting", one, f.clock(), current) }, status: 503, code: "no_revision"},
		{name: "a source out of form", request: func(f *fixture) *http.Request { return signed("9am", one, f.clock(), current) }, status: 404, code: "unknown_source"},
		{name: "a source with no mapping", request: func(f *fixture) *http.Request { return signed("pager", one, f.clock(), current) }, status: 404, code: "unknown_source"},
		{name: "a bearer mapping", setup: func(f *fixture) {
			f.revs.rev.Store(revision(f.t, commitA, func(s string) string {
				return strings.Replace(s, hmacKey, "authentication: {bearerSecretRef: secret://inbound/alerting}", 1)
			}))
		}, request: func(f *fixture) *http.Request { return signed("alerting", one, f.clock(), current) }, status: 400, code: "scheme_unsupported"},
		{name: "a body over the limit", request: func(f *fixture) *http.Request {
			return signed("alerting", make([]byte, inbound.MaxBody+1), f.clock(), current)
		}, status: 413, code: "too_large"},
		{name: "a secret that cannot be read", setup: func(f *fixture) { f.secrets.set("", errors.New("files: read secret://inbound/alerting")) },
			request: func(f *fixture) *http.Request { return signed("alerting", one, f.clock(), current) }, status: 503, code: "secret_unavailable"},
		{name: "a secret file of three lines", setup: func(f *fixture) { f.secrets.set("a\nb\nc", nil) },
			request: func(f *fixture) *http.Request { return signed("alerting", one, f.clock(), current) }, status: 503, code: "secret_unavailable"},
		{name: "no valid signature", request: func(f *fixture) *http.Request { return signed("alerting", one, f.clock(), "another") }, status: 401, code: "bad_signature"},
		{name: "unsigned", request: func(f *fixture) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/inbound/alerting", bytes.NewReader(one))
		}, status: 401, code: "bad_signature"},
		{name: "outside maxAge", request: func(f *fixture) *http.Request {
			return signed("alerting", one, f.clock().Add(-6*time.Minute), current)
		}, status: 401, code: "stale"},
		{name: "the leader drains", setup: func(f *fixture) { f.leader.set(true, true) },
			request: func(f *fixture) *http.Request { return signed("alerting", one, f.clock(), current) }, status: 503, code: "draining"},
		{name: "this instance does not hold", setup: func(f *fixture) { f.leader.set(false, false) },
			request: func(f *fixture) *http.Request { return signed("alerting", one, f.clock(), current) }, status: 503, code: "not_held"},
		{name: "a body that is not JSON", request: func(f *fixture) *http.Request { return signed("alerting", notJSON, f.clock(), current) },
			status: 400, code: "bad_body", asked: []string{"body_unparsed"}},
		{name: "a body whose split selects nothing", request: func(f *fixture) *http.Request { return signed("alerting", bodyOf(), f.clock(), current) },
			status: 400, code: "no_events", asked: []string{"no_events"}},
		{name: "an event's Ingest not held while draining", setup: func(f *fixture) {
			f.coord.fail = func(int) error { f.leader.set(true, true); return app.ErrNotHeld }
		}, request: func(f *fixture) *http.Request { return signed("alerting", two, f.clock(), current) }, status: 503, code: "draining", asked: []string{""}},
		{name: "an event's Ingest not held", setup: func(f *fixture) {
			f.coord.fail = func(int) error { f.leader.set(false, false); return fmt.Errorf("fenced: %w", app.ErrNotHeld) }
		}, request: func(f *fixture) *http.Request { return signed("alerting", two, f.clock(), current) }, status: 503, code: "not_held", asked: []string{""}},
		{name: "an event's Ingest fails", setup: func(f *fixture) {
			f.coord.fail = func(n int) error {
				if n == 2 {
					return errStore
				}
				return nil
			}
		}, request: func(f *fixture) *http.Request { return signed("alerting", two, f.clock(), current) }, status: 503, code: "unavailable", asked: []string{"", ""}},
		{name: "every event has an outcome", request: func(f *fixture) *http.Request { return signed("alerting", two, f.clock(), current) }, status: 200, asked: []string{"", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.setup != nil {
				tc.setup(f)
			}

			w := f.send(tc.request(f))

			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == http.StatusServiceUnavailable {
				assert.Equal(t, "1", w.Header().Get("Retry-After"))
			} else {
				assert.Empty(t, w.Header().Get("Retry-After"))
			}
			if tc.code != "" {
				assert.Equal(t, tc.code, codeOf(t, w))
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
			}
			if tc.status == http.StatusMethodNotAllowed {
				assert.Equal(t, http.MethodPost, w.Header().Get("Allow"))
			}
			var asked []string
			for _, in := range f.coord.calls() {
				asked = append(asked, in.Event.Unroutable+in.Event.Refused)
			}
			assert.Equal(t, tc.asked, asked)
		})
	}
}

// The health route answers ok, as text, whatever the revision or the
// leader.
func TestTheHealthRouteAnswersOK(t *testing.T) {
	f := newFixture(t)
	f.revs.rev.Store(nil)
	f.leader.set(false, false)

	w := f.send(httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ok", strings.TrimSpace(w.Body.String()))
	assert.Empty(t, f.coord.calls())
}

// An authenticated body that is not JSON records one refused row keyed by
// the body's hash, body_unparsed (R115), under the derived item id.
func TestABodyThatIsNotJSONLeavesOneRefusedRow(t *testing.T) {
	f := newFixture(t)
	body := []byte("not json")

	w := f.post(body)

	require.Equal(t, http.StatusBadRequest, w.Code)
	calls := f.coord.calls()
	require.Len(t, calls, 1)
	e := calls[0].Event
	assert.Equal(t, mapping.Event{Source: "alerting", Mapping: "alerts", Owner: "sre-leads", Type: "incident", EventID: hashOf(body), Refused: "body_unparsed"}, *e)
	assert.Equal(t, app.ItemID(tenant, "inbound:alerting:"+hashOf(body)), calls[0].ID)
	lines := f.logs.matching(t, "not JSON")
	require.Len(t, lines, 1)
	assert.Equal(t, "WARN", lines[0]["level"])
	assert.Equal(t, "alerts", lines[0]["mapping"])
	assert.Equal(t, "sre-leads", lines[0]["owner"])
	assert.NotContains(t, f.logs.String(), "not json")
}

// An authenticated body whose split selects nothing records one refused row
// keyed by the body's hash, no_events, under the derived item id, and is
// answered 400; the owner is warned once per mapping and commit, naming the
// mapping and its owner and never a value.
func TestABodyWhoseSplitSelectsNothingLeavesOneRefusedRow(t *testing.T) {
	f := newFixture(t)
	body := []byte(`{"alerts":[],"note":"ORD-12345678"}`)

	first := f.post(body)
	second := f.post(bodyOf())

	require.Equal(t, http.StatusBadRequest, first.Code, first.Body.String())
	assert.Equal(t, "no_events", codeOf(t, first))
	require.Equal(t, http.StatusBadRequest, second.Code, second.Body.String())
	calls := f.coord.calls()
	require.Len(t, calls, 2)
	assert.Equal(t, mapping.Event{Source: "alerting", Mapping: "alerts", Owner: "sre-leads", Type: "incident", EventID: hashOf(body), Refused: "no_events"}, *calls[0].Event)
	assert.Equal(t, app.ItemID(tenant, "inbound:alerting:"+hashOf(body)), calls[0].ID)
	assert.Equal(t, hashOf(bodyOf()), calls[1].Event.EventID)
	lines := f.logs.matching(t, "selects no event")
	require.Len(t, lines, 1, "once per mapping and commit")
	assert.Equal(t, "WARN", lines[0]["level"])
	assert.Equal(t, "alerts", lines[0]["mapping"])
	assert.Equal(t, "sre-leads", lines[0]["owner"])
	assert.Equal(t, commitA, lines[0]["commit"])
	assert.NotContains(t, f.logs.String(), "ORD-")
	assert.Empty(t, f.logs.matching(t, "an inbound request was taken"))

	f.revs.rev.Store(revision(t, commitB, nil))
	f.post(body)
	assert.Len(t, f.logs.matching(t, "selects no event"), 2, "a new commit warns again")
}

// Every event is ingested in order under its derived item id, and the answer
// counts the outcomes.
func TestEveryEventIsIngestedInOrderAndCounted(t *testing.T) {
	f := newFixture(t)
	at := f.clock().Add(-time.Minute)
	f.coord.fail = nil

	w := f.post(bodyOf(alert("f1", "checkout", "s", at), alert("", "checkout", "s", at), alert("f3", "checkout", "s", at)))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.JSONEq(t, `{"opened":2,"appended":0,"late":0,"repeat":0,"resolved_unmatched":0,"unroutable":1,"refused":0}`, w.Body.String())
	calls := f.coord.calls()
	require.Len(t, calls, 3)
	stamp := at.UTC().Format(time.RFC3339)
	assert.Equal(t, "f1:firing:"+stamp, calls[0].Event.EventID)
	assert.Equal(t, "f3:firing:"+stamp, calls[2].Event.EventID)
	for _, in := range calls {
		assert.Equal(t, app.ItemID(tenant, "inbound:alerting:"+in.Event.EventID), in.ID)
	}
	info := f.logs.matching(t, "an inbound request was taken")
	require.Len(t, info, 1)
	assert.Equal(t, "INFO", info[0]["level"])
	assert.InDelta(t, 2, info[0]["opened"], 0)
	assert.InDelta(t, 1, info[0]["unroutable"], 0)
}

// A request is answered 200 only once every event has an outcome: when the
// second event's Ingest fails, the answer is 503 and the third is never
// ingested.
func TestARequestIsAcknowledgedOnlyOnceEveryEventHasAnOutcome(t *testing.T) {
	f := newFixture(t)
	at := f.clock().Add(-time.Minute)
	f.coord.fail = func(n int) error {
		if n == 2 {
			return errStore
		}
		return nil
	}

	w := f.post(bodyOf(alert("f1", "checkout", "s", at), alert("f2", "checkout", "s", at), alert("f3", "checkout", "s", at)))

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "unavailable", codeOf(t, w))
	calls := f.coord.calls()
	require.Len(t, calls, 2, "the third is never ingested")
	assert.True(t, strings.HasPrefix(calls[1].Event.EventID, "f2:"))
}

// countingBody counts the bytes read of a body of n bytes.
type countingBody struct {
	r    io.Reader
	read atomic.Int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read.Add(int64(n))
	return n, err
}

// A body over MaxBody is refused 413 before the secret is read and nothing
// is ingested: one that says its length is never read, and one that does not
// is read no further than the limit.
func TestAnOverLongBodyIsRefusedUnreadAndUnrecorded(t *testing.T) {
	for name, length := range map[string]int64{"its length given": inbound.MaxBody + 1, "chunked": -1} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			body := &countingBody{r: io.LimitReader(zeros{}, 2*inbound.MaxBody)}
			r := httptest.NewRequest(http.MethodPost, "/inbound/alerting", body)
			r.ContentLength = length
			ts := stamp(f.clock())
			r.Header.Set(inbound.HeaderTimestamp, ts)
			r.Header.Set(inbound.HeaderSignature, "v1="+strings.Repeat("0", 64))

			w := f.send(r)

			assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
			assert.Equal(t, "too_large", codeOf(t, w))
			assert.Empty(t, f.coord.calls())
			assert.Zero(t, f.secrets.read(), "no secret read")
			assert.LessOrEqual(t, body.read.Load(), int64(inbound.MaxBody+1), "never read whole")
			if length > 0 {
				assert.Zero(t, body.read.Load(), "a length over the limit is refused unread")
			}
			lines := f.logs.matching(t, "too large")
			require.Len(t, lines, 1)
			assert.Equal(t, "alerting", lines[0]["source"])
			assert.InDelta(t, inbound.MaxBody, lines[0]["limit"], 0)
		})
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// A body of more than 500 events leaves one refused row, too_many_events,
// keyed by the body's hash (N6), and is answered 413.
func TestTooManyEventsLeaveOneRefusedRow(t *testing.T) {
	f := newFixture(t)
	at := f.clock().Add(-time.Minute)
	many := make([]string, mapping.MaxEvents+1)
	for i := range many {
		many[i] = alert(fmt.Sprintf("f%d", i), "checkout", "s", at)
	}
	body := bodyOf(many...)

	w := f.post(body)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.Equal(t, "too_many_events", codeOf(t, w))
	calls := f.coord.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, mapping.Event{Source: "alerting", Mapping: "alerts", Owner: "sre-leads", Type: "incident", EventID: hashOf(body), Refused: "too_many_events"}, *calls[0].Event)
	assert.Equal(t, app.ItemID(tenant, "inbound:alerting:"+hashOf(body)), calls[0].ID)
	lines := f.logs.matching(t, "too many events")
	require.Len(t, lines, 1)
	assert.Equal(t, "alerting", lines[0]["source"])
	assert.Equal(t, "alerts", lines[0]["mapping"])
	assert.Equal(t, "sre-leads", lines[0]["owner"])
	assert.InDelta(t, mapping.MaxEvents+1, lines[0]["count"], 0)
}

// Neither an answer nor a log line holds the body, a signature or the
// secret: not for a request with a bad signature, nor for one signed whose
// event is unroutable.
func TestNoAnswerOrLogLineEchoesTheBody(t *testing.T) {
	f := newFixture(t)
	at := f.clock().Add(-time.Minute)
	body := bodyOf(alert("f1", "", "password=hunter2", at))
	bad := signed("alerting", body, f.clock(), "wrong-key")
	good := signed("alerting", body, f.clock(), current)

	answers := []string{f.send(bad).Body.String()}
	w := f.send(good)
	answers = append(answers, w.Body.String())

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, f.coord.calls(), 1)
	require.NotEmpty(t, f.coord.calls()[0].Event.Unroutable)
	for _, text := range append(answers, f.logs.String()) {
		for _, secret := range []string{"hunter2", current, "wrong-key", bad.Header.Get(inbound.HeaderSignature)[3:], good.Header.Get(inbound.HeaderSignature)[3:]} {
			assert.NotContains(t, text, secret)
		}
	}
}

// The secret file is read at each request (spec 9.3): a request signed with
// the old secret passes while the file holds new and old, and is refused once
// the file holds new alone, with no restart.
func TestASecretRolledOverBetweenRequestsIsReadAtEachRequest(t *testing.T) {
	f := newFixture(t)
	at := f.clock().Add(-time.Minute)
	f.secrets.set("new\nold\n", nil)

	first := f.send(signed("alerting", bodyOf(alert("f1", "checkout", "s", at)), f.clock(), "old"))
	f.secrets.set("new\n", nil)
	second := f.send(signed("alerting", bodyOf(alert("f2", "checkout", "s", at)), f.clock(), "old"))
	third := f.send(signed("alerting", bodyOf(alert("f3", "checkout", "s", at)), f.clock(), "new"))

	assert.Equal(t, http.StatusOK, first.Code, first.Body.String())
	assert.Equal(t, http.StatusUnauthorized, second.Code)
	assert.Equal(t, "bad_signature", codeOf(t, second))
	assert.Equal(t, http.StatusOK, third.Code, third.Body.String())
	assert.Equal(t, 3, f.secrets.read())
	assert.Equal(t, []string{"secret://inbound/alerting", "secret://inbound/alerting", "secret://inbound/alerting"}, f.secrets.reads)
}

// The window is judged by the database's time as the instance estimates it,
// not by the host's clock: with the host an hour ahead, a request stamped at
// the database's time passes and one stamped at the host's is stale.
func TestTheWindowIsJudgedByTheDatabasesClock(t *testing.T) {
	f := newFixture(t)
	db := time.Now().Add(-time.Hour).Truncate(time.Second)
	f.now.Store(&db)
	body := bodyOf(alert("f1", "checkout", "s", db.Add(-time.Minute)))

	byDB := f.send(signed("alerting", body, db, current))
	byHost := f.send(signed("alerting", body, time.Now(), current))

	assert.Equal(t, http.StatusOK, byDB.Code, byDB.Body.String())
	assert.Equal(t, http.StatusUnauthorized, byHost.Code)
	assert.Equal(t, "stale", codeOf(t, byHost))
}

// The owner of a mapping is told of an unroutable event in one Warn line
// naming the mapping, its owner, the event's id and the reason (D10).
func TestTheOwnerIsToldOfAnUnroutableEventInALogLine(t *testing.T) {
	f := newFixture(t)
	at := f.clock().Add(-time.Minute)

	w := f.post(bodyOf(alert("", "checkout", "s", at)))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	calls := f.coord.calls()
	require.Len(t, calls, 1)
	require.Equal(t, "missing:instanceId", calls[0].Event.Unroutable)
	var lines []map[string]any
	for _, line := range f.logs.lines(t) {
		if line["level"] == "WARN" && line["reason"] == "missing:instanceId" {
			lines = append(lines, line)
		}
	}
	require.Len(t, lines, 1)
	assert.Equal(t, "alerts", lines[0]["mapping"])
	assert.Equal(t, "sre-leads", lines[0]["owner"])
	assert.Equal(t, calls[0].Event.EventID, lines[0]["event"])
	assert.Equal(t, "unroutable", lines[0]["outcome"])
}

// The Warn that a mapping's principal, responders and fallbackPrincipal are
// not applied until Plan 7c-2 (R117) is logged once per mapping and commit:
// two requests under one commit log it once, and a new commit logs it again.
func TestTheUnappliedReadsAreWarnedOncePerMappingAndRevision(t *testing.T) {
	f := newFixture(t)
	at := f.clock().Add(-time.Minute)

	f.post(bodyOf(alert("f1", "checkout", "s", at)))
	f.post(bodyOf(alert("f2", "checkout", "s", at)))
	lines := f.logs.matching(t, "not applied")
	require.Len(t, lines, 1)
	assert.Equal(t, "WARN", lines[0]["level"])
	assert.Equal(t, "alerts", lines[0]["mapping"])
	assert.Equal(t, commitA, lines[0]["commit"])

	f.revs.rev.Store(revision(t, commitB, nil))
	f.post(bodyOf(alert("f3", "checkout", "s", at)))
	f.post(bodyOf(alert("f4", "checkout", "s", at)))

	lines = f.logs.matching(t, "not applied")
	require.Len(t, lines, 2)
	assert.Equal(t, commitB, lines[1]["commit"])
}

// A request whose every event the mapping's redaction rules make unroutable,
// id_redacted or routing_refused, is warned once per mapping and commit
// (M10, N3), naming the mapping and never a value; a request with one
// routable event logs no such line.
func TestARequestWhoseEventsTheRulesAllRefuseIsWarnedOnce(t *testing.T) {
	f := newFixture(t)
	at := f.clock().Add(-time.Minute)
	refused := bodyOf(alert("ORD-12345678", "checkout", "s", at), alert("f2", "ORD-87654321", "s", at))

	f.post(refused)
	f.post(refused)

	var reasons []string
	for _, in := range f.coord.calls()[:2] {
		reasons = append(reasons, in.Event.Unroutable)
	}
	require.Equal(t, []string{"id_redacted", "routing_refused"}, reasons)
	lines := f.logs.matching(t, "redaction rules")
	require.Len(t, lines, 1)
	assert.Equal(t, "WARN", lines[0]["level"])
	assert.Equal(t, "alerts", lines[0]["mapping"])
	assert.Equal(t, "sre-leads", lines[0]["owner"])
	assert.NotContains(t, f.logs.String(), "ORD-")

	mixed := newFixture(t)
	mixed.post(bodyOf(alert("ORD-12345678", "checkout", "s", at), alert("f2", "checkout", "s", at)))
	assert.Empty(t, mixed.logs.matching(t, "redaction rules"))
}
