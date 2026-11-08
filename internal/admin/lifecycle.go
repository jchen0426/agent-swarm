package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// codeTooLarge is the code of a lifecycle request whose body is over the cap
// (Contract C4; R15). The codes of the other refusals are the shared
// routing's (see switching), and the coordinator's, which it answers as
// given.
const codeTooLarge = "too_large"

// adminActor is who acts through the admin API (Decision 13): the subject
// admin, an admin, since the admin token is an admin's.
var adminActor = app.Actor{Subject: "admin", Admin: true}

// What the lifecycle routes' answers say a person may do next. An answer
// that says how the instance stands, draining or not holding the lease, says
// too that the request is not confirmed: an act fenced, or drained, after its
// commit is answered so too (Decision 13). Each says what to do on the
// instance that leads next, where what landed is found: an act on an item by
// its key, and the kill switch by its state. A preview's answers say what a
// preview is: it records nothing, and takes no key, so it is never told that
// it is not confirmed, nor to send a key, an on or an off again.
const (
	actUnconfirmed     = "the act is not confirmed: on the instance that leads next, read the item, or send the act again with the same key"
	onUnconfirmed      = "the on is not confirmed: on the instance that leads next, read the kill switch, and send the on again with the seq read as after"
	offUnconfirmed     = "the off is not confirmed: on the instance that leads next, read the kill switch, and send the off again with the seq read as after; the items the kill switch left not staffed say so in their views, under notStaffed"
	actUnavailable     = "the act failed, or its outcome is not known yet; send it again with the same key"
	switchUnavailable  = "the request failed, or its outcome is not known yet; read the kill switch, and send the request again with the seq read as after"
	previewUnheld      = "a preview records nothing: send it to the instance that leads next"
	previewUnavailable = "the preview failed; send it again"
	unsettled          = "the kill switch's state is not known yet, and it may be on; send the act again in a moment"
)

// asked is a lifecycle request as its answers speak of it, and as the lines
// logged of it name it.
type asked struct {
	// what names the request in a refusal's text: the act, the on or the off.
	what string
	// unconfirmed is what a refusal as draining or not holding the lease says
	// after how the instance stands, and unavailable what a 503, unavailable,
	// says.
	unconfirmed, unavailable string
	// attrs name the request in a line logged: the act and its item, or the
	// state the kill switch is asked.
	attrs []any
}

// actAsked is the act on the item id that kind names, promote, close or
// reopen, or, as preview says, its preview.
func actAsked(kind string, id item.ID, preview bool) *asked {
	return previewed(&asked{what: "the act", unconfirmed: actUnconfirmed, unavailable: actUnavailable, attrs: []any{"act", kind, "item", id}}, preview)
}

// switchAsked is a request to turn the kill switch on, or off, or, as preview
// says, its preview.
func switchAsked(on, preview bool) *asked {
	q := &asked{what: "the on", unconfirmed: onUnconfirmed, unavailable: switchUnavailable, attrs: []any{"on", on}}
	if !on {
		q.what, q.unconfirmed = "the off", offUnconfirmed
	}
	return previewed(q, preview)
}

// previewed gives q a preview's texts when preview says it is one: a preview
// records nothing and takes no key (Contract C4).
func previewed(q *asked, preview bool) *asked {
	if preview {
		q.unconfirmed, q.unavailable = previewUnheld, previewUnavailable
	}
	return q
}

// text is the text of the refusal r of the request (Contract C4), whose
// code the shared routing gave: one that says how the instance stands,
// draining or not holding the lease, says so, then that the request is not
// confirmed, and what to do on the instance that leads next; taking over, to
// try again; and every other its own, never quoting the request. An invalid
// request's gives the coordinator's reason, and an unavailable one says
// whether the kill switch's state is not known yet (R37).
func (q *asked) text(r *switching.Refusal) string {
	switch r.Code {
	case switching.CodeDraining:
		return "this instance is draining; " + q.unconfirmed
	case switching.CodeTakingOver:
		return "this instance is taking over; try again"
	case switching.CodeNotHeld:
		return "this instance does not hold the lease; " + q.unconfirmed
	case switching.CodeHeldByLaterBuild:
		return switching.HeldByLaterBuild
	case switching.CodeKeyReused:
		return "the Idempotency-Key named another act on the item"
	case switching.CodeStale:
		return "the kill switch has a row newer than after; read it, and send the request again with its seq as after"
	case switching.CodeNoSuchItem:
		return "no such item"
	case switching.CodeInvalid:
		return switching.Invalid(q.what, r.Err)
	case switching.CodeUnavailable:
		if errors.Is(r.Err, app.ErrUnsettled) {
			return unsettled
		}
		return q.unavailable
	case app.CodeReasonRequired:
		return q.what + " needs a reason"
	}
	if text, ok := switching.RefusedText(r.Code); ok {
		return text
	}
	return q.what + " is not allowed now"
}

// routing is the shared routing's ports, whose log names the request by
// attrs.
func (a *api) routing(attrs []any) switching.Ports {
	return switching.Ports{Coordinator: a.p.Coordinator, Leader: a.p.Leader, Log: a.log.With(attrs...)}
}

// lifecycleRefused answers a lifecycle request q refused as r gives it
// (Contract C4): {"error", "code"}, whose text never quotes the request (see
// text), and a Retry-After, in whole seconds, when r gives one.
func lifecycleRefused(w http.ResponseWriter, q *asked, r *switching.Refusal) {
	if r.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(max(int(r.RetryAfter/time.Second), 1)))
	}
	refuseCode(w, r.Status, r.Code, q.text(r))
}

// actBody is an act's body (Contract C4): its reason, and whether it asks
// the act's consequences alone. Each is optional, and so is the body.
type actBody struct {
	Reason  *board.Reason `json:"reason"`
	Preview bool          `json:"preview"`
}

// switchBody is a request to the kill switch (Contract C4): the state it
// asks, on; after, the latest row its sender saw, 0 before the first (R8),
// both required; its reason, which an on needs, but its preview; and whether
// it asks the request's consequences alone.
type switchBody struct {
	On      *bool         `json:"on"`
	After   *int64        `json:"after"`
	Reason  *board.Reason `json:"reason"`
	Preview bool          `json:"preview"`
}

// The forms of the lifecycle's bodies, as a refusal of one says them.
const (
	actForm    = "an act's body, if any, is one JSON object of reason and preview, each optional"
	switchForm = "the kill switch's body is one JSON object of on, the state asked, and after, the latest row its sender saw, 0 before the first, with reason and preview if any"
)

// act serves the act on an item that kind names, which do does: promote,
// close or reopen (Contract C4). Its body, if any, is decoded strictly (see
// readStrictly); an act, but its preview, takes its one Idempotency-Key, of 1
// to 128 printable ASCII characters, and a preview reads none. The act is
// the admin's (see adminActor). It answers 200 with whether the act changed
// the item, its consequences, and the item's view once it is done, which a
// view that cannot be read leaves out, logged; a preview, 200 with its
// consequences alone. A refusal is answered as lifecycleRefused says.
func (a *api) act(kind string, do func(context.Context, item.ID, *app.Act) (app.Outcome, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := item.ID(r.PathValue("id"))
		in, ok := readStrictly[actBody](a, w, r, actForm)
		if !ok {
			return
		}
		act := &app.Act{Actor: adminActor}
		if in != nil {
			act.Reason, act.Preview = in.Reason, in.Preview
		}
		if !act.Preview {
			key, err := idempotencyKey(r.Header, "an act on an item")
			if err != nil {
				refuseCode(w, http.StatusBadRequest, switching.CodeInvalid, err.Error())
				return
			}
			act.Key = key
		}
		q := actAsked(kind, id, act.Preview)
		out, err := do(r.Context(), id, act)
		switch {
		case err != nil:
			lifecycleRefused(w, q, switching.Act(r.Context(), a.routing(q.attrs), err))
		case act.Preview:
			answer(w, http.StatusOK, previewView{Preview: true, Consequences: consequencesOf(&out.Consequences)})
		default:
			answer(w, http.StatusOK, actedView{Changed: out.Changed, Consequences: consequencesOf(&out.Consequences), Item: a.itemAfter(r, q, id)})
		}
	}
}

// itemAfter is the item's view once an act on it is done, as C4 shows it, or
// nil when it cannot be read: the act is done all the same, the read is
// logged, and a read of the item shows it.
func (a *api) itemAfter(r *http.Request, q *asked, id item.ID) any {
	v, err := a.p.Items.Item(r.Context(), id)
	if err != nil {
		a.log.WarnContext(r.Context(), "an item acted on could not be read", slices.Concat(q.attrs, []any{"err", err})...)
		return nil
	}
	return a.shownItem(r, id, &v)
}

// showKillSwitch shows the kill switch as its table holds it, on any
// instance, a standby included (Contract C4). A table that cannot be read
// answers 503, unavailable, logged.
func (a *api) showKillSwitch(w http.ResponseWriter, r *http.Request) {
	ks, err := a.p.Items.KillSwitch(r.Context())
	if err != nil {
		a.switchUnread(r, err)
		refuseCode(w, http.StatusServiceUnavailable, switching.CodeUnavailable, "the kill switch could not be read; try again")
		return
	}
	answer(w, http.StatusOK, switchStateOf(&ks))
}

// setKillSwitch turns the kill switch on or off, or previews it (Decision 5;
// Contract C4). Its body is required, and decoded strictly (see
// readStrictly): the state asked and after are required. It takes no key,
// and reads none: its retry is safe by the switch's state and after. The
// request is the admin's (see adminActor), and routed as the web's is (see
// switching.Set): an on during a drain is recorded, deferred. It answers 200
// with what the coordinator did; a preview, 200 with the latest row the
// table holds and the consequences. A refusal is answered as
// lifecycleRefused says.
func (a *api) setKillSwitch(w http.ResponseWriter, r *http.Request) {
	in, ok := readStrictly[switchBody](a, w, r, switchForm)
	if !ok {
		return
	}
	if in == nil || in.On == nil || in.After == nil {
		refuseCode(w, http.StatusBadRequest, switching.CodeInvalid, switchForm)
		return
	}
	on, q := *in.On, switchAsked(*in.On, in.Preview)
	act := &app.Act{Actor: adminActor, After: *in.After, Reason: in.Reason, Preview: in.Preview}
	got := switching.Set(r.Context(), a.routing(q.attrs), on, act)
	switch {
	case got.Refusal != nil:
		lifecycleRefused(w, q, got.Refusal)
	case act.Preview:
		answer(w, http.StatusOK, previewView{Preview: true, Seq: &got.Switched.Seq, Consequences: consequencesOf(&got.Switched.Consequences)})
	default:
		answer(w, http.StatusOK, switchedOf(got.Switched))
	}
}

// readStrictly reads a lifecycle request's body within the cap, and decodes
// it strictly: one JSON object of T's fields alone, and nothing after it. An
// empty body, or one of whitespace alone, decodes as nil. A body it does not
// take is answered, {"error", "code"}, and it reports whether it took the
// body: one over the cap 413, too_large; any other 400, invalid, with form,
// the body's form, which never repeats the body.
func readStrictly[T any](a *api, w http.ResponseWriter, r *http.Request, form string) (*T, bool) {
	body, err := a.body(w, r)
	switch {
	case errors.Is(err, errTooLarge):
		refuseCode(w, http.StatusRequestEntityTooLarge, codeTooLarge, fmt.Sprintf("a body holds at most %d bytes", a.o.MaxBody))
		return nil, false
	case err != nil:
		refuseCode(w, http.StatusBadRequest, switching.CodeInvalid, "the body could not be read")
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var v *T
	switch err := dec.Decode(&v); {
	case errors.Is(err, io.EOF):
		return nil, true
	case err != nil, v == nil:
		refuseCode(w, http.StatusBadRequest, switching.CodeInvalid, form)
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		refuseCode(w, http.StatusBadRequest, switching.CodeInvalid, "the body holds more than one value; "+form)
		return nil, false
	}
	return v, true
}

// consequencesView is an act's consequences as C4 shows them, each only
// where it applies: the runs that stop; the items that start, close or are
// not staffed; when the item closes by itself, or that its auto-close is
// paused; and the items investigating and the limit, which the kill switch's
// consequences do not give.
type consequencesView struct {
	Stops           []stopView `json:"stops,omitempty"`
	Starts          []item.ID  `json:"starts,omitempty"`
	Closes          []item.ID  `json:"closes,omitempty"`
	Unstaffed       []item.ID  `json:"unstaffed,omitempty"`
	ClosesAt        time.Time  `json:"closesAt,omitzero"`
	AutoClosePaused bool       `json:"autoClosePaused,omitempty"`
	Investigating   *int       `json:"investigating,omitempty"`
	Limit           *int       `json:"limit,omitempty"`
}

// stopView is a run an act stops, or would stop: its item, its latest round,
// 0 before its first, and the reason it stops for.
type stopView struct {
	Item   item.ID         `json:"item"`
	Run    board.RunID     `json:"run"`
	Round  int             `json:"round"`
	Reason item.StopReason `json:"reason"`
}

// consequencesOf is c as C4 shows it. The items investigating and the limit
// apply when the limit is given, as every act on an item gives it.
func consequencesOf(c *app.Consequences) consequencesView {
	out := consequencesView{Starts: c.Starts, Closes: c.Closes, Unstaffed: c.Unstaffed, ClosesAt: c.ClosesAt.UTC(), AutoClosePaused: c.AutoClosePaused}
	for _, s := range c.Stops {
		out.Stops = append(out.Stops, stopView{Item: s.Item, Run: s.Run, Round: s.Round, Reason: s.Reason})
	}
	if c.Limit > 0 {
		investigating, limit := c.Investigating, c.Limit
		out.Investigating, out.Limit = &investigating, &limit
	}
	return out
}

// actedView is what an act on an item did (Contract C4): whether it changed
// the item, its consequences, and the item's view once it is done, left out
// when it could not be read.
type actedView struct {
	Changed      bool             `json:"changed"`
	Consequences consequencesView `json:"consequences"`
	Item         any              `json:"item,omitempty"`
}

// previewView is what a lifecycle request would do (Contract C4): its
// consequences, and, the kill switch's, the latest row its table holds, which
// the request then names as after.
type previewView struct {
	Preview      bool             `json:"preview"`
	Seq          *int64           `json:"seq,omitempty"`
	Consequences consequencesView `json:"consequences"`
}

// switchedView is what a request to the kill switch did (Contract C4): the
// switch's state and its latest row after it; whether it appended a row;
// whether it was deferred, recorded during a drain for the next leader to act
// on; the runs it stopped, each at its latest round; the items it left not
// staffed; at an off, the items the latest on left not staffed, with their
// states now, and whether that list could not be read in full, said only when
// it could not; and what the kill switch's rule still acts on, but for a
// deferred on, whose coordinator holds no account. Every list is empty,
// never null.
type switchedView struct {
	On                    bool            `json:"on"`
	Seq                   int64           `json:"seq"`
	Changed               bool            `json:"changed"`
	Deferred              bool            `json:"deferred"`
	Stopped               []stoppedView   `json:"stopped"`
	Unstaffed             []item.ID       `json:"unstaffed"`
	StayedUnstaffed       []unstaffedView `json:"stayedUnstaffed"`
	StayedUnstaffedUnread bool            `json:"stayedUnstaffedUnread,omitempty"`
	Owed                  []item.ID       `json:"owed,omitzero"`
}

// stoppedView is a run the kill switch stopped: its item, and its latest
// round, 0 before its first.
type stoppedView struct {
	Item  item.ID     `json:"item"`
	Run   board.RunID `json:"run"`
	Round int         `json:"round"`
}

// unstaffedView is an item the latest on left not staffed, and its state
// now, empty when it could not be read.
type unstaffedView struct {
	Item  item.ID     `json:"item"`
	State item.Status `json:"state"`
}

// switchedOf is out as C4 shows it.
func switchedOf(out *app.KillSwitchOutcome) switchedView {
	v := switchedView{
		On: out.On, Seq: out.Seq, Changed: out.Changed, Deferred: out.Deferred, Stopped: make([]stoppedView, 0, len(out.Stopped)),
		Unstaffed: append([]item.ID{}, out.Unstaffed...), StayedUnstaffed: make([]unstaffedView, 0, len(out.StayedUnstaffed)),
		StayedUnstaffedUnread: out.StayedUnstaffedUnread,
	}
	for _, s := range out.Stopped {
		v.Stopped = append(v.Stopped, stoppedView{Item: s.Item, Run: s.Run, Round: s.Round})
	}
	for _, u := range out.StayedUnstaffed {
		v.StayedUnstaffed = append(v.StayedUnstaffed, unstaffedView{Item: u.Item, State: u.State})
	}
	if !out.Deferred {
		v.Owed = append([]item.ID{}, out.Owed...)
	}
	return v
}

// switchState is the kill switch as its table holds it (Contract C4): its
// state and its latest row, 0 before the first; and, from its first row on,
// the latest row's time, in UTC, and who wrote it, and its reason, if it has
// one, its text redacted as the table keeps it.
type switchState struct {
	On     bool          `json:"on"`
	Seq    int64         `json:"seq"`
	Since  time.Time     `json:"since,omitzero"`
	By     string        `json:"by,omitempty"`
	Reason *board.Reason `json:"reason,omitempty"`
}

// switchStateOf is ks as C4 shows it.
func switchStateOf(ks *app.KillSwitch) switchState {
	out := switchState{On: ks.On, Seq: ks.Seq}
	if ks.Seq > 0 {
		out.Since, out.By = ks.Since.UTC(), ks.By
	}
	if ks.Reason.Code != "" {
		reason := ks.Reason
		out.Reason = &reason
	}
	return out
}
