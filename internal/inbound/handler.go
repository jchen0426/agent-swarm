package inbound

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/jchen0426/agent-swarm/internal/inbound/mapping"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// MaxBody caps a request's body, in bytes; a longer one is refused unread.
const MaxBody = 4 << 20

// Coordinator takes one evaluated event (app.Coordinator).
type Coordinator interface {
	Ingest(ctx context.Context, in *app.IngestInput) (app.IngestResult, error)
}

// Leader is the leader's holding: whether it holds the lease and whether it
// drains (app.Leader).
type Leader interface {
	Status() (epoch int64, held, draining bool)
}

// Revisions give the current configuration revision, or nil before the
// first.
type Revisions interface {
	Current() *config.Revision
}

// SecretReader reads a secret by its reference, at each request (spec 9.3)
// (files.Store). It is not named Secrets, which parses a secret file.
type SecretReader interface {
	Secret(ctx context.Context, ref string) (string, error)
}

// Ports are what the handler reads and acts through. Every one is required
// but Log.
type Ports struct {
	Coordinator Coordinator
	Leader      Leader
	Revisions   Revisions
	Secrets     SecretReader
	// Clock is the database's time as the instance estimates it (clock.DB):
	// the signature's window, and an event's receipt, are judged by it.
	Clock func() time.Time
	// Tenant is the deployment's tenant, of an opening's item id (R121).
	Tenant string
	Log    *slog.Logger
}

// sourceForm is the form Open takes a source in (R132).
var sourceForm = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// The refusals' codes, as the answers give them.
const (
	codeNoSuchRoute       = "no_such_route"
	codeMethodNotAllowed  = "method_not_allowed"
	codeNoRevision        = "no_revision"
	codeUnknownSource     = "unknown_source"
	codeSchemeUnsupported = "scheme_unsupported"
	codeTooLarge          = "too_large"
	codeSecretUnavailable = "secret_unavailable"
	codeBadSignature      = "bad_signature"
	codeStale             = "stale"
	codeDraining          = "draining"
	codeNotHeld           = "not_held"
	codeBadBody           = "bad_body"
	codeTooManyEvents     = "too_many_events"
	codeNoEvents          = "no_events"
	codeUnavailable       = "unavailable"
)

// The reasons of a request's refused row (R115, N6).
const (
	reasonBodyUnparsed  = "body_unparsed"
	reasonTooManyEvents = "too_many_events"
	reasonNoEvents      = "no_events"
)

// The reasons by which a mapping's redaction rules make an event unroutable
// (N3).
var redacted = map[string]bool{"id_redacted": true, "routing_refused": true}

type handler struct {
	p   Ports
	log *slog.Logger

	// mu guards the lines logged once per mapping and commit: commit is the
	// revision's they were logged under, and logged their keys.
	mu     sync.Mutex
	commit string
	logged map[string]bool
}

// NewHandler returns the inbound listener's handler (spec Section 3.6):
// GET /healthz, and POST /inbound/{source}. No answer or log line holds a
// header's value, a signature, a secret or the body. Serve wraps it in its
// bounded server (R113).
func NewHandler(p *Ports) http.Handler {
	h := &handler{p: *p, log: p.Log, logged: map[string]bool{}}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", http.MethodGet)
		refuse(w, http.StatusMethodNotAllowed, codeMethodNotAllowed)
	})
	mux.HandleFunc("POST /inbound/{source}", h.take)
	mux.HandleFunc("/inbound/{source}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", http.MethodPost)
		refuse(w, http.StatusMethodNotAllowed, codeMethodNotAllowed)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { refuse(w, http.StatusNotFound, codeNoSuchRoute) })
	return mux
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	h.log.DebugContext(r.Context(), "the inbound listener's health was asked")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

// take serves a source's request, in the order its refusals are checked:
// the revision, the source and its mapping, the scheme, the body's size, the
// secret, the signature and its window, the holding; then the body's events,
// each ingested in order, and answered 200 only once every one has an
// outcome.
func (h *handler) take(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rev := h.p.Revisions.Current()
	if rev == nil {
		h.log.WarnContext(ctx, "an inbound request found no configuration revision")
		unavailable(w, codeNoRevision)
		return
	}
	source := r.PathValue("source")
	if !sourceForm.MatchString(source) {
		h.log.WarnContext(ctx, "an inbound request names a source out of form")
		refuse(w, http.StatusNotFound, codeUnknownSource)
		return
	}
	m := rev.TriggerMapping(source)
	if m == nil {
		h.log.WarnContext(ctx, "an inbound request names a source no mapping maps", "source", source)
		refuse(w, http.StatusNotFound, codeUnknownSource)
		return
	}
	auth := &m.Spec.Authentication
	if auth.HMACSecretRef == "" {
		h.log.WarnContext(ctx, "an inbound request's mapping authenticates by a scheme not supported", "source", source)
		refuse(w, http.StatusBadRequest, codeSchemeUnsupported)
		return
	}
	body, ok := h.read(w, r, source)
	if !ok {
		return
	}
	// The window and the events' receipt are judged by one reading of the
	// database's time.
	now := h.p.Clock()
	if !h.verify(w, r, source, auth, body, now) {
		return
	}
	h.unapplied(ctx, rev, m)
	if code := h.unheld(); code != "" {
		unavailable(w, code)
		return
	}
	events, err := mapping.Evaluate(m, body, now, mapping.Redactor(rev, m))
	if err != nil {
		h.unparsed(w, r, m, body, err)
		return
	}
	if len(events) == 0 {
		h.empty(w, r, rev, m, body)
		return
	}
	h.allRedacted(ctx, rev, m, events)
	h.ingest(w, r, m, events)
}

// read reads the body, at most MaxBody bytes, and refuses a longer one 413,
// unread when its length says so.
func (h *handler) read(w http.ResponseWriter, r *http.Request, source string) ([]byte, bool) {
	tooLarge := func() {
		h.log.WarnContext(r.Context(), "an inbound request's body is too large", "source", source, "limit", MaxBody)
		refuse(w, http.StatusRequestEntityTooLarge, codeTooLarge)
	}
	if r.ContentLength > MaxBody {
		tooLarge()
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if tooBig := new(http.MaxBytesError); errors.As(err, &tooBig) {
		tooLarge()
		return nil, false
	}
	if err != nil {
		h.log.WarnContext(r.Context(), "an inbound request's body could not be read", "source", source, "err", err)
		refuse(w, http.StatusBadRequest, codeBadBody)
		return nil, false
	}
	return body, true
}

// verify reads the mapping's secret file, at each request, and checks the
// request's signature, then its window, by the database's time.
func (h *handler) verify(w http.ResponseWriter, r *http.Request, source string, auth *config.Authentication, body []byte, now time.Time) bool {
	ctx := r.Context()
	text, err := h.p.Secrets.Secret(ctx, auth.HMACSecretRef)
	if err == nil {
		var secrets []string
		if secrets, err = Secrets(text); err == nil {
			err = Verify(secrets, r.Header, body, now, auth.MaxAgeOrDefault())
			switch {
			case errors.Is(err, ErrStale):
				h.log.WarnContext(ctx, "an inbound request is outside its mapping's maximum age", "source", source)
				refuse(w, http.StatusUnauthorized, codeStale)
				return false
			case err != nil:
				h.log.WarnContext(ctx, "an inbound request is not signed by a current secret", "source", source)
				refuse(w, http.StatusUnauthorized, codeBadSignature)
				return false
			}
			return true
		}
	}
	// The store's errors and ErrSecretFile hold the reference at most,
	// never the secret.
	h.log.WarnContext(ctx, "an inbound source's secret could not be read", "source", source, "ref", auth.HMACSecretRef, "err", err)
	unavailable(w, codeSecretUnavailable)
	return false
}

// unheld returns the code of a 503 while this instance drains or does not
// hold the lease, or "".
func (h *handler) unheld() string {
	switch _, held, draining := h.p.Leader.Status(); {
	case draining:
		return codeDraining
	case !held:
		return codeNotHeld
	}
	return ""
}

// unparsed records an authenticated body the mapping could not split as one
// refused row, keyed by the body's hash: body_unparsed (R115), answered 400,
// or too_many_events (N6), answered 413.
func (h *handler) unparsed(w http.ResponseWriter, r *http.Request, m *config.TriggerMapping, body []byte, err error) {
	ctx := r.Context()
	reason, status, code := reasonBodyUnparsed, http.StatusBadRequest, codeBadBody
	if tooMany := new(mapping.TooManyEventsError); errors.As(err, &tooMany) {
		reason, status, code = reasonTooManyEvents, http.StatusRequestEntityTooLarge, codeTooManyEvents
		h.log.WarnContext(ctx, "an inbound request's body splits into too many events", "source", m.Spec.Source,
			"mapping", m.Metadata.Name, "owner", m.Metadata.Owner, "count", tooMany.Count, "limit", mapping.MaxEvents)
	} else {
		h.log.WarnContext(ctx, "an inbound request's body is not JSON", "source", m.Spec.Source,
			"mapping", m.Metadata.Name, "owner", m.Metadata.Owner)
	}
	h.refuseBody(w, r, m, body, reason, status, code)
}

// empty records an authenticated body whose split selects no event as one
// refused row, keyed by the body's hash, no_events, answered 400; the owner
// is warned once per mapping and commit, never of a value.
func (h *handler) empty(w http.ResponseWriter, r *http.Request, rev *config.Revision, m *config.TriggerMapping, body []byte) {
	if h.first(rev.Commit(), "empty:"+m.Metadata.Name) {
		h.log.WarnContext(r.Context(), "an inbound request's body selects no event by its mapping's split", "source", m.Spec.Source,
			"mapping", m.Metadata.Name, "owner", m.Metadata.Owner, "commit", rev.Commit())
	}
	h.refuseBody(w, r, m, body, reasonNoEvents, http.StatusBadRequest, codeNoEvents)
}

// refuseBody records body as one refused row, keyed by its hash, for
// reason, and answers status with code once it is recorded.
func (h *handler) refuseBody(w http.ResponseWriter, r *http.Request, m *config.TriggerMapping, body []byte, reason string, status int, code string) {
	ctx := r.Context()
	sum := sha256.Sum256(body)
	e := mapping.Event{
		Source: m.Spec.Source, Mapping: m.Metadata.Name, Owner: m.Metadata.Owner, Type: m.Spec.WorkItemType,
		EventID: "sha256:" + hex.EncodeToString(sum[:]), Refused: reason,
	}
	if _, err := h.p.Coordinator.Ingest(ctx, h.input(&e)); err != nil {
		h.failed(ctx, w, m, err)
		return
	}
	refuse(w, status, code)
}

// ingest takes each event in order, and answers 200 with the outcomes'
// counts once every one has one; an event whose Ingest fails ends the
// request with 503, the events before it taken, the source to retry.
func (h *handler) ingest(w http.ResponseWriter, r *http.Request, m *config.TriggerMapping, events []mapping.Event) {
	ctx := r.Context()
	var n counts
	for i := range events {
		e := &events[i]
		out, err := h.p.Coordinator.Ingest(ctx, h.input(e))
		if err != nil {
			h.failed(ctx, w, m, err)
			return
		}
		n.add(out.Outcome)
		if out.Outcome == app.IngestUnroutable || out.Outcome == app.IngestRefused {
			h.log.WarnContext(ctx, "an inbound event was not taken", "source", e.Source, "mapping", e.Mapping, "owner", e.Owner,
				"event", e.EventID, "outcome", string(out.Outcome), "reason", out.Reason)
		}
	}
	h.log.InfoContext(ctx, "an inbound request was taken", "source", m.Spec.Source, "mapping", m.Metadata.Name, "events", len(events),
		"opened", n.Opened, "appended", n.Appended, "late", n.Late, "repeat", n.Repeat, "resolved_unmatched", n.ResolvedUnmatched,
		"unroutable", n.Unroutable, "refused", n.Refused)
	answer(w, http.StatusOK, n)
}

// input is e's IngestInput, with the item an opening by it takes (R121).
func (h *handler) input(e *mapping.Event) *app.IngestInput {
	return &app.IngestInput{Event: e, ID: app.ItemID(h.p.Tenant, "inbound:"+e.Source+":"+e.EventID)}
}

// failed answers an Ingest that failed 503: draining or not_held when the
// coordinator does not hold, as the leader then stands, and unavailable,
// logged, for any other failure.
func (h *handler) failed(ctx context.Context, w http.ResponseWriter, m *config.TriggerMapping, err error) {
	if errors.Is(err, app.ErrNotHeld) {
		if _, _, draining := h.p.Leader.Status(); draining {
			unavailable(w, codeDraining)
			return
		}
		unavailable(w, codeNotHeld)
		return
	}
	h.log.WarnContext(ctx, "an inbound event could not be taken", "source", m.Spec.Source, "mapping", m.Metadata.Name, "err", err)
	unavailable(w, codeUnavailable)
}

// unapplied logs, once per mapping and commit, that the mapping's principal,
// responders and fallbackPrincipal are not applied until Plan 7c-2 (R117).
func (h *handler) unapplied(ctx context.Context, rev *config.Revision, m *config.TriggerMapping) {
	sp := &m.Spec
	if sp.Principal == nil && sp.Responders == nil && sp.FallbackPrincipal == nil {
		return
	}
	if h.first(rev.Commit(), "unapplied:"+m.Metadata.Name) {
		h.log.WarnContext(ctx, "a trigger mapping's principal, responders and fallbackPrincipal are not applied until Plan 7c-2",
			"mapping", m.Metadata.Name, "owner", m.Metadata.Owner, "commit", rev.Commit())
	}
}

// allRedacted logs, once per mapping and commit, that the mapping's
// redaction rules made every event of a request unroutable (N3), naming the
// mapping and never a value.
func (h *handler) allRedacted(ctx context.Context, rev *config.Revision, m *config.TriggerMapping, events []mapping.Event) {
	if len(events) == 0 {
		return
	}
	for i := range events {
		if !redacted[events[i].Unroutable] {
			return
		}
	}
	if h.first(rev.Commit(), "redacted:"+m.Metadata.Name) {
		h.log.WarnContext(ctx, "a trigger mapping's redaction rules made every event of a request unroutable",
			"mapping", m.Metadata.Name, "owner", m.Metadata.Owner, "commit", rev.Commit(), "events", len(events))
	}
}

// first reports whether key is new under commit: the keys are forgotten
// when the commit changes.
func (h *handler) first(commit, key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if commit != h.commit {
		h.commit, h.logged = commit, map[string]bool{}
	}
	if h.logged[key] {
		return false
	}
	h.logged[key] = true
	return true
}

// counts are a request's outcomes, as its 200 answers them.
type counts struct {
	Opened            int `json:"opened"`
	Appended          int `json:"appended"`
	Late              int `json:"late"`
	Repeat            int `json:"repeat"`
	ResolvedUnmatched int `json:"resolved_unmatched"`
	Unroutable        int `json:"unroutable"`
	Refused           int `json:"refused"`
}

func (n *counts) add(o app.IngestOutcome) {
	switch o {
	case app.IngestOpened:
		n.Opened++
	case app.IngestAppended:
		n.Appended++
	case app.IngestLate:
		n.Late++
	case app.IngestRepeat:
		n.Repeat++
	case app.IngestResolvedUnmatched:
		n.ResolvedUnmatched++
	case app.IngestUnroutable:
		n.Unroutable++
	case app.IngestRefused:
		n.Refused++
	}
}

// refuse answers status with {"error": code}.
func refuse(w http.ResponseWriter, status int, code string) {
	answer(w, status, struct {
		Error string `json:"error"`
	}{code})
}

// unavailable answers 503 with code, for the source to retry in a second.
func unavailable(w http.ResponseWriter, code string) {
	w.Header().Set("Retry-After", "1")
	refuse(w, http.StatusServiceUnavailable, code)
}

func answer(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
