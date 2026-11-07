// Package admin is the admin API (Plan 7d-1, Decision 14; Plan 7b-1, Decision
// 14; Contract C4): it opens work items by their key, acts on their lifecycle,
// promoting, closing and reopening them, turns the kill switch on and off,
// shows an item as the stores hold it, and says how the instance stands, for
// people and for the fault-injection harness. It answers loopback peers only,
// until Plan 9's sign-in, beside serve's refusal of a listener's address that
// is not on loopback. Every /v1/ route needs the admin token as a bearer
// token, read from its secret on each request; /healthz and /readyz need
// none.
package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// defaultMaxBody caps a request's body when Options set no cap [64 KiB].
const defaultMaxBody = 64 << 10

// maxKey caps an opening's key, and an act's, in characters, each printable
// ASCII.
const maxKey = 128

// person is who opens an item through the admin API.
const person = "person"

// heldByLaterBuild answers an opening of an item a later build holds (C4).
const heldByLaterBuild = "held by a later build"

// Ports are what the admin API reads and acts through.
type Ports struct {
	// Coordinator is the coordinator (app.Coordinator): it opens items, acts
	// on their lifecycle, sets the kill switch, records its on during a drain,
	// and gives its snapshot of the queue.
	Coordinator interface {
		Open(ctx context.Context, in *app.OpenInput) (item.ID, error)
		Skipped() []item.ID
		Promote(ctx context.Context, id item.ID, a *app.Act) (app.Outcome, error)
		Close(ctx context.Context, id item.ID, a *app.Act) (app.Outcome, error)
		Reopen(ctx context.Context, id item.ID, a *app.Act) (app.Outcome, error)
		SetKillSwitch(ctx context.Context, on bool, a *app.Act) (app.KillSwitchOutcome, error)
		RecordKillSwitchOn(ctx context.Context, epoch int64, a *app.Act) (app.KillSwitchOutcome, error)
		Lifecycle() app.Lifecycle
	}
	// Leader is the leader: its holding's epoch, whether it holds the lease,
	// and whether it drains, by which a request the coordinator refused as not
	// held is answered, and the kill switch's on during a drain recorded.
	Leader interface {
		Status() (epoch int64, held, draining bool)
	}
	Schema interface {
		State(ctx context.Context) (db.State, error)
	}
	// Items are the views (app.Views): an item's, and the kill switch as its
	// table holds it.
	Items interface {
		Item(ctx context.Context, id item.ID) (app.View, error)
		KillSwitch(ctx context.Context) (app.KillSwitch, error)
	}
	Secrets interface {
		Secret(ctx context.Context, ref string) (string, error)
	}
	// Runner is the runner, as the status shows it.
	Runner interface {
		Status() RunnerStatus
	}
}

// RunnerStatus is the runner as /v1/status shows it (Plan 7d-2, Decision 7;
// Contract C4): its kind, and whether its turns run sandboxed. The Docker
// runner adds Engine, ok or unreachable as its last call to the engine found
// it, and MissingImages, the images of runner.images its last check found
// missing, sorted: a list, empty, never nil, while none is. The process
// runner gives neither, and the status leaves both out.
type RunnerStatus struct {
	Kind          string   `json:"kind"`
	Sandboxed     bool     `json:"sandboxed"`
	Engine        string   `json:"engine,omitzero"`
	MissingImages []string `json:"missingImages,omitzero"` // nil under process; never nil under docker
}

// Options are the admin API's settings. Tenant and TokenRef are required.
type Options struct {
	// Tenant is the tenant whose items the API opens: an item's id derives
	// from it and the opening's key (R15).
	Tenant string
	// TokenRef is the admin token's secret reference.
	TokenRef string
	// MaxBody caps a request's body, an opening's, an act's or the kill
	// switch's, in bytes [64 KiB].
	MaxBody int64
	// Log receives what the API answered 5xx for, beside the item's id, or
	// the act or the kill switch's request; a token, a schema or a kill
	// switch that could not be read, beside the request's path; an item that
	// could not be read once an act on it was done; and a kill switch's on
	// that could not be recorded during a drain. Nil logs nothing.
	Log *slog.Logger
}

// api serves the admin API.
type api struct {
	p   Ports
	o   Options
	log *slog.Logger
}

// New returns the admin API's handler (Contract C4). Every port is required.
//
//nolint:gocritic // hugeParam: the contract passes the ports by value.
func New(p Ports, o Options) http.Handler {
	if o.MaxBody <= 0 {
		o.MaxBody = defaultMaxBody
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	a := &api{p: p, o: o, log: log}
	v1 := http.NewServeMux()
	// route serves pattern by its methods, each by its handler, and refuses any
	// other method with 405, naming those it takes.
	route := func(pattern string, handlers map[string]http.HandlerFunc) {
		methods := slices.Sorted(maps.Keys(handlers))
		for _, method := range methods {
			v1.HandleFunc(method+" "+pattern, handlers[method])
		}
		v1.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Allow", strings.Join(methods, ", "))
			refuse(w, http.StatusMethodNotAllowed, "the route takes "+strings.Join(methods, " or "))
		})
	}
	route("/v1/items", map[string]http.HandlerFunc{http.MethodPost: a.openItem})
	route("/v1/items/{id}", map[string]http.HandlerFunc{http.MethodGet: a.showItem})
	route("/v1/items/{id}/promote", map[string]http.HandlerFunc{http.MethodPost: a.act("promote", p.Coordinator.Promote)})
	route("/v1/items/{id}/close", map[string]http.HandlerFunc{http.MethodPost: a.act("close", p.Coordinator.Close)})
	route("/v1/items/{id}/reopen", map[string]http.HandlerFunc{http.MethodPost: a.act("reopen", p.Coordinator.Reopen)})
	route("/v1/kill-switch", map[string]http.HandlerFunc{http.MethodGet: a.showKillSwitch, http.MethodPost: a.setKillSwitch})
	route("/v1/status", map[string]http.HandlerFunc{http.MethodGet: a.showStatus})
	v1.HandleFunc("/v1/", func(w http.ResponseWriter, _ *http.Request) { refuse(w, http.StatusNotFound, "no such route") })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { text(w, http.StatusOK, "ok") })
	mux.HandleFunc("GET /readyz", a.ready)
	mux.Handle("/v1/", a.authorized(v1))
	return loopback(mux)
}

// loopback refuses, with 403, a request whose peer is not on loopback.
func loopback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !onLoopback(r.RemoteAddr) {
			refuse(w, http.StatusForbidden, "the admin API answers loopback peers only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// onLoopback reports whether a peer's address, host:port, is on loopback:
// 127.0.0.0/8 or ::1, an IPv4 address in IPv6's form included.
func onLoopback(peer string) bool {
	host, _, err := net.SplitHostPort(peer)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// authorized refuses, with 401, a request that does not carry the admin
// token as a bearer token. The token is read from its secret on each
// request, so that a rotated token takes effect at once (spec Section 9.3);
// a token that cannot be read answers 503.
func (a *api) authorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := bearer(r.Header.Get("Authorization"))
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			refuse(w, http.StatusUnauthorized, "the admin API needs the admin token as a bearer token")
			return
		}
		want, err := a.p.Secrets.Secret(r.Context(), a.o.TokenRef)
		if err != nil {
			a.log.ErrorContext(r.Context(), "the admin token could not be read", "err", err)
			refuse(w, http.StatusServiceUnavailable, "the admin token could not be read")
			return
		}
		if !same(got, want) {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			refuse(w, http.StatusUnauthorized, "the bearer token is not the admin token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearer returns the token of an Authorization header of the Bearer scheme,
// whose name is read in any case.
func bearer(header string) (string, bool) {
	scheme, token, _ := strings.Cut(header, " ")
	token = strings.TrimSpace(token)
	return token, strings.EqualFold(scheme, "Bearer") && token != ""
}

// same reports whether two tokens are equal, in a time that depends neither
// on where they differ nor on their lengths.
func same(got, want string) bool {
	x, y := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

// ready answers whether the instance is ready: the process is up, the
// schema compatible, and serve does not drain. A standby is ready.
func (a *api) ready(w http.ResponseWriter, r *http.Request) {
	if _, _, draining := a.p.Leader.Status(); draining {
		text(w, http.StatusServiceUnavailable, "draining")
		return
	}
	switch state, err := a.p.Schema.State(r.Context()); {
	case err != nil:
		a.unread(r, err)
		text(w, http.StatusServiceUnavailable, "the schema could not be read")
	case state != db.Compatible:
		text(w, http.StatusServiceUnavailable, "the schema is "+string(state))
	default:
		text(w, http.StatusOK, "ready")
	}
}

// opening is an opening's body (C4).
type opening struct {
	Type       string            `json:"type"`
	Title      string            `json:"title"`
	Routing    map[string]string `json:"routing"`
	Principal  string            `json:"principal"`
	Responders []string          `json:"responders"`
}

// opened answers an opening.
type opened struct {
	ID    item.ID `json:"id"`
	State string  `json:"state"`
}

// openItem opens an item by its key (Decision 14). The key names the item
// (R15) for its whole life, and the request's digest tells a retry from
// another request under the key: a retry gets the item, which is answered
// as at first, 201, with its state now. The coordinator does not say which
// it was.
func (a *api) openItem(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r.Header, "an opening")
	if err != nil {
		refuse(w, http.StatusBadRequest, err.Error())
		return
	}
	in, status, err := a.read(w, r)
	if err != nil {
		refuse(w, status, err.Error())
		return
	}
	id := ItemID(a.o.Tenant, key)
	got, err := a.p.Coordinator.Open(r.Context(), &app.OpenInput{
		ID: id, Digest: digest(in), Type: in.Type, Title: in.Title, Source: person,
		Routing: in.Routing, Principal: in.Principal, Responders: in.Responders, Actor: adminActor,
	})
	if err != nil {
		a.refused(w, r, id, err)
		return
	}
	// The item is open: a state that cannot be read leaves the answer
	// without one, which a retry gives.
	state := ""
	if v, err := a.p.Items.Item(r.Context(), got); err != nil {
		a.log.WarnContext(r.Context(), "an item opened could not be read", "item", got, "err", err)
	} else {
		state = v.State
	}
	answer(w, http.StatusCreated, opened{ID: got, State: state})
}

// idempotencyKey returns the key of what, an opening or an act on an item:
// its one Idempotency-Key, of 1 to 128 printable ASCII characters.
func idempotencyKey(h http.Header, what string) (string, error) {
	keys := h.Values("Idempotency-Key")
	switch {
	case len(keys) == 0 || keys[0] == "":
		return "", errors.New(what + " needs an Idempotency-Key")
	case len(keys) > 1:
		return "", errors.New(what + " has one Idempotency-Key")
	case len(keys[0]) > maxKey || strings.ContainsFunc(keys[0], func(r rune) bool { return r < ' ' || r > '~' }):
		return "", fmt.Errorf("an Idempotency-Key holds 1 to %d printable ASCII characters", maxKey)
	}
	return keys[0], nil
}

// errTooLarge marks a request's body over the cap.
var errTooLarge = errors.New("the body is over the cap")

// body reads a request's body within the cap: one over it, whether or not the
// request declared its length, fails with errTooLarge.
func (a *api) body(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.ContentLength > a.o.MaxBody {
		return nil, errTooLarge
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, a.o.MaxBody))
	var over *http.MaxBytesError
	if errors.As(err, &over) {
		return nil, errTooLarge
	}
	return body, err
}

// read reads an opening's body, within the cap, and decodes it strictly: one
// JSON object, of an opening's fields alone. On failure it returns the
// status to answer, and a text that never repeats the body.
func (a *api) read(w http.ResponseWriter, r *http.Request) (*opening, int, error) {
	body, err := a.body(w, r)
	switch {
	case errors.Is(err, errTooLarge):
		return nil, http.StatusRequestEntityTooLarge, fmt.Errorf("an opening's body holds at most %d bytes", a.o.MaxBody)
	case err != nil:
		return nil, http.StatusBadRequest, errors.New("the body could not be read")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var in opening
	if err := dec.Decode(&in); err != nil {
		return nil, http.StatusBadRequest, errors.New("the body is not an opening: a JSON object of type, title, routing, principal and responders")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, http.StatusBadRequest, errors.New("the body holds more than one opening")
	}
	return &in, 0, nil
}

// digest is an opening's digest, which its item's opening records: SHA-256
// over the JSON of its type, routing, principal and responders, every
// object's keys sorted, routing and responders left out read as empty. The
// title is left out: redaction may change it rather than refuse it, and a
// digest over it would keep, unsalted, a hash of a secret the redaction
// removed. A retry that changes only the title gets the item (Decision 14).
func digest(in *opening) string {
	routing, responders := in.Routing, in.Responders
	if routing == nil {
		routing = map[string]string{}
	}
	if responders == nil {
		responders = []string{}
	}
	// Strings alone, which always encode.
	canonical, _ := json.Marshal(map[string]any{"type": in.Type, "routing": routing, "principal": in.Principal, "responders": responders})
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// refused answers an opening the coordinator refused (C4). Each refusal's
// text is fixed, and never repeats the request; an invalid opening's gives
// the coordinator's reason, which never quotes the request either. A 5xx is
// logged with the item's id, which the coordinator's error may not name.
func (a *api) refused(w http.ResponseWriter, r *http.Request, id item.ID, err error) {
	switch {
	case errors.Is(err, app.ErrNotHeld), errors.Is(err, app.ErrLeaseLost):
		a.notHeld(w, err)
	case errors.Is(err, app.ErrSkipped), errors.Is(err, item.ErrVersion), errors.Is(err, board.ErrVersion):
		// An opening that races another of a later version can meet the
		// later record unwrapped by ErrSkipped (Task 8).
		refuse(w, http.StatusConflict, heldByLaterBuild)
	case errors.Is(err, app.ErrKeyReused):
		refuse(w, http.StatusConflict, "the Idempotency-Key opened an item for another request")
	case errors.Is(err, app.ErrInvalid):
		refuse(w, http.StatusBadRequest, invalid("the opening", err))
	case errors.Is(err, app.ErrNoRevision):
		refuse(w, http.StatusServiceUnavailable, "no configuration is current yet; try again")
	case errors.Is(err, item.ErrInvalid):
		// An opening whose first write landed after its caller saw an error
		// can make a quick retry meet it as it records: the next retry gets
		// the item (Task 8). A corrupt item answers so too, and is logged.
		a.log.ErrorContext(r.Context(), "an opening could not be answered", "item", id, "err", err)
		refuse(w, http.StatusServiceUnavailable, "the opening could not be answered yet; try again")
	default:
		a.log.ErrorContext(r.Context(), "an opening failed", "item", id, "err", err)
		refuse(w, http.StatusServiceUnavailable, "the opening failed; try again")
	}
}

// notHeld answers an opening the coordinator refused because it does not act
// under the lease, by how the instance stands (C4). A draining instance still
// holds the lease, and takes no new work: 409, and the item opens on the
// instance that leads next. An instance whose leader holds the lease, and
// does not drain, is taking over, and its status says it leads, so its
// coordinator acts in a moment: 503, with Retry-After, to be tried again,
// since a client tries no 409 again. Otherwise, as on a standby, or after a
// write found the lease gone, the instance does not hold the lease: 409.
func (a *api) notHeld(w http.ResponseWriter, err error) {
	_, held, draining := a.p.Leader.Status()
	switch standing(held, draining, err) {
	case codeDraining:
		refuse(w, http.StatusConflict, "this instance is draining; open the item on the instance that leads next")
	case codeTakingOver:
		w.Header().Set("Retry-After", "1")
		refuse(w, http.StatusServiceUnavailable, "this instance is taking over; try again")
	default:
		refuse(w, http.StatusConflict, "this instance does not hold the lease")
	}
}

// standing is how the instance stands for a request the coordinator refused
// as not held with err, by its leader's status, held and draining (C4):
// draining, while the leader drains, and takes no new work; taking over,
// while it holds the lease, does not drain, and err is ErrNotHeld, as its
// coordinator then acts in a moment; and not holding the lease otherwise, as
// on a standby, or once a write found the lease gone.
func standing(held, draining bool, err error) string {
	switch {
	case draining:
		return codeDraining
	case held && errors.Is(err, app.ErrNotHeld):
		return codeTakingOver
	}
	return codeNotHeld
}

// invalid is the answer to what, an opening or a lifecycle request, that the
// coordinator refused as invalid, with the coordinator's reason: what follows
// its sentinel's words, which never quote the request.
func invalid(what string, err error) string {
	if _, reason, ok := strings.Cut(err.Error(), app.ErrInvalid.Error()+": "); ok {
		return what + " is invalid: " + reason
	}
	return what + " is invalid"
}

// itemView is an item as C4 shows it, and heldView one a later build holds.
// Its incident, type and opening time are every item's; the queue a waiting
// item's (R17); notStaffed an item's that awaits its close not staffed (R18);
// autoClosePaused every item's that awaits its close, unless the kill switch's
// table could not be read (R28), and closesAt such an item's while not paused
// and its delay could be read; closed a closed item's. Times are in UTC.
type itemView struct {
	ID               item.ID         `json:"id"`
	State            string          `json:"state"`
	Stop             string          `json:"stop"`
	HeldByLaterBuild bool            `json:"heldByLaterBuild"`
	Rounds           []roundView     `json:"rounds"`
	Incident         item.IncidentID `json:"incident"`
	Type             string          `json:"type"`
	OpenedAt         time.Time       `json:"openedAt"`
	Queue            *queueView      `json:"queue,omitempty"`
	NotStaffed       item.Cause      `json:"notStaffed,omitempty"`
	ClosesAt         time.Time       `json:"closesAt,omitzero"`
	AutoClosePaused  *bool           `json:"autoClosePaused,omitempty"`
	Closed           *closedView     `json:"closed,omitempty"`
}

// queueView is a waiting item's place in the queue, from 1.
type queueView struct {
	Position int `json:"position"`
}

// closedView is a closed item's close: when, by whom, empty for a close by
// itself, which auto says; and until when a person may reopen it, and may
// make the rulings a closed item takes.
type closedView struct {
	At               time.Time `json:"at"`
	By               string    `json:"by"`
	Auto             bool      `json:"auto"`
	ReopenableUntil  time.Time `json:"reopenableUntil"`
	LateRulingsUntil time.Time `json:"lateRulingsUntil"`
}

type heldView struct {
	ID               item.ID `json:"id"`
	HeldByLaterBuild bool    `json:"heldByLaterBuild"`
}

type roundView struct {
	Round    int          `json:"round"`
	WrapUp   bool         `json:"wrapUp"`
	Ended    string       `json:"ended"`
	Quiet    bool         `json:"quiet"`
	Evidence evidence     `json:"evidence"`
	Report   board.Report `json:"report"`
}

type evidence struct {
	TurnEnds         []string `json:"turnEnds"`
	FailedModelCalls int      `json:"failedModelCalls"`
	FailedToolCalls  int      `json:"failedToolCalls"`
	Settled          bool     `json:"settled"`
}

// showItem shows an item, read from the stores, off the coordinator's lock.
func (a *api) showItem(w http.ResponseWriter, r *http.Request) {
	id := item.ID(r.PathValue("id"))
	v, err := a.p.Items.Item(r.Context(), id)
	switch {
	case errors.Is(err, app.ErrNoItem):
		refuse(w, http.StatusNotFound, "no such item")
	case err != nil:
		a.log.ErrorContext(r.Context(), "an item could not be read", "item", id, "err", err)
		refuse(w, http.StatusServiceUnavailable, "the item could not be read; try again")
	default:
		answer(w, http.StatusOK, a.shownItem(r, id, &v))
	}
}

// shownItem is the item's view as shown gives it, for r. A view that could
// not read the kill switch's table, which then leaves out when the item
// closes by itself and whether its auto-close is paused (R28), is logged at
// Warn, with the item, so that the table's failure is seen.
func (a *api) shownItem(r *http.Request, id item.ID, v *app.View) any {
	if v.AutoCloseUnread {
		a.log.WarnContext(r.Context(), "an item's view could not read the kill switch", "item", id)
	}
	return shown(id, v)
}

// shown is an item's view as C4 shows it: one a later build holds says that
// alone (see viewOf).
func shown(id item.ID, v *app.View) any {
	if v.HeldByLaterBuild {
		return heldView{ID: id, HeldByLaterBuild: true}
	}
	return viewOf(id, v)
}

// viewOf is C4's view of an item: lists empty, never null; the lifecycle's
// fields each only where it applies, its times in UTC.
func viewOf(id item.ID, v *app.View) itemView {
	out := itemView{
		ID: id, State: v.State, Stop: v.Stop, Rounds: make([]roundView, 0, len(v.Rounds)),
		Incident: v.Incident, Type: v.Type, OpenedAt: v.OpenedAt.UTC(), NotStaffed: v.NotStaffed, ClosesAt: v.ClosesAt.UTC(),
	}
	if v.QueuePosition > 0 {
		out.Queue = &queueView{Position: v.QueuePosition}
	}
	if v.State == string(item.StatusAwaitingClose) && !v.AutoCloseUnread {
		paused := v.AutoClosePaused
		out.AutoClosePaused = &paused
	}
	if c := v.Closed; c != nil {
		out.Closed = &closedView{
			At: c.At.UTC(), By: c.By, Auto: c.Auto, ReopenableUntil: c.ReopenableUntil.UTC(), LateRulingsUntil: c.LateRulingsUntil.UTC(),
		}
	}
	for i := range v.Rounds {
		r := &v.Rounds[i]
		ends := r.TurnEnds
		if ends == nil {
			ends = []string{}
		}
		out.Rounds = append(out.Rounds, roundView{
			Round: r.Round, WrapUp: r.WrapUp, Ended: r.Ended, Quiet: r.Quiet, Report: r.Report,
			Evidence: evidence{TurnEnds: ends, FailedModelCalls: r.FailedCalls, FailedToolCalls: r.FailedToolCalls, Settled: r.Settled},
		})
	}
	return out
}

// statusView is the instance's status (C4).
type statusView struct {
	Leading    bool         `json:"leading"`
	Epoch      int64        `json:"epoch"`
	Schema     db.State     `json:"schema"`
	Draining   bool         `json:"draining"`
	Skipped    int          `json:"skipped"`
	Runner     RunnerStatus `json:"runner"`
	KillSwitch switchStatus `json:"killSwitch"`
	Queue      queueStatus  `json:"queue"`
}

// switchStatus is the kill switch in the status: its state and its latest
// row, 0 before the first, as its table holds them.
type switchStatus struct {
	On  bool  `json:"on"`
	Seq int64 `json:"seq"`
}

// queueStatus is the queue in the status, as the coordinator's snapshot holds
// it: the limit; the items investigating as the limit counts them, admitted
// and promoted items that have not started included; and the queued items
// that are neither. A standby's is all zeros, its snapshot empty.
type queueStatus struct {
	Limit         int `json:"limit"`
	Investigating int `json:"investigating"`
	Queued        int `json:"queued"`
}

// showStatus shows whether the instance leads, its epoch, the schema's
// state, whether it drains, how many items it skips, its runner, the kill
// switch, read from its table, and the queue, from the coordinator's
// snapshot. A standby skips none, and queues none: a fenced coordinator holds
// nothing. It shows the kill switch as any instance reads it. A schema, or a
// kill switch, that could not be read answers 503, logged (R28).
func (a *api) showStatus(w http.ResponseWriter, r *http.Request) {
	epoch, held, draining := a.p.Leader.Status()
	state, err := a.p.Schema.State(r.Context())
	if err != nil {
		a.unread(r, err)
		refuse(w, http.StatusServiceUnavailable, "the schema could not be read")
		return
	}
	ks, err := a.p.Items.KillSwitch(r.Context())
	if err != nil {
		a.switchUnread(r, err)
		refuse(w, http.StatusServiceUnavailable, "the kill switch could not be read")
		return
	}
	lc := a.p.Coordinator.Lifecycle()
	answer(w, http.StatusOK, statusView{
		Leading: held, Epoch: epoch, Schema: state, Draining: draining, Skipped: len(a.p.Coordinator.Skipped()), Runner: a.p.Runner.Status(),
		KillSwitch: switchStatus{On: ks.On, Seq: ks.Seq}, Queue: queueStatus{Limit: lc.Limit, Investigating: lc.Investigating, Queued: len(lc.Waiting)},
	})
}

// unread logs a schema that could not be read, which a request's 503 leaves
// out, with the request's path.
func (a *api) unread(r *http.Request, err error) {
	a.log.ErrorContext(r.Context(), "the schema could not be read", "path", r.URL.Path, "err", err)
}

// switchUnread logs a kill switch that could not be read, which a request's
// 503 leaves out, with the request's path.
func (a *api) switchUnread(r *http.Request, err error) {
	a.log.ErrorContext(r.Context(), "the kill switch could not be read", "path", r.URL.Path, "err", err)
}

// answer answers v as JSON.
func answer(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		status, body = http.StatusInternalServerError, []byte(`{"error":"the answer could not be written"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body) //nolint:gosec // G705: JSON to an API client, served as application/json with nosniff, never a page
}

// refuse answers an error: {"error": text}.
func refuse(w http.ResponseWriter, status int, text string) {
	answer(w, status, map[string]string{"error": text})
}

// refuseCode answers an error of the lifecycle's routes: {"error": text,
// "code": code} (R15).
func refuseCode(w http.ResponseWriter, status int, code, text string) {
	answer(w, status, map[string]string{"error": text, "code": code})
}

// text answers a plain text.
func text(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body) //nolint:gosec // G705: the API's own words, served as text/plain with nosniff, never a page
}

// ItemID derives an item's id from its opening's key (R15): a UUID of
// version 8 whose bits are the first 128 of SHA-256 over the tenant, a zero
// byte, and the key, with the version and variant bits set. It has the form
// of the ids the platform makes, which are of version 7, and cannot be one
// of them.
func ItemID(tenant, key string) item.ID {
	sum := sha256.Sum256([]byte(tenant + "\x00" + key))
	var u uuid.UUID
	copy(u[:], sum[:16])
	u[6] = u[6]&0x0f | 0x80 // version 8
	u[8] = u[8]&0x3f | 0x80 // the variant of RFC 9562, RFC 4122's
	return item.ID(u.String())
}
