package pages

import (
	"context"
	"net/http"
	"strconv"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// switchPath is the kill switch's control (design D7).
const switchPath = "/kill-switch"

// The kill switch's form's fields, beside the reason's and the CSRF
// token: which way it turns the switch, on or off, and After, the table's
// latest row the page read (R8).
const (
	switchField = "switch"
	afterField  = "after"
)

// switchReasons are the on's preset reasons, each a one-tap button, beside
// other with text (M8), so that stopping stays fast on a phone (spec 8.3).
var switchReasons = []reasonButton{
	{Code: "runaway_cost", Label: "Runaway cost"},
	{Code: "bad_output", Label: "Bad output"},
	{Code: "security", Label: "Security"},
}

// The kill switch's words (design D7), besides those its template holds:
// D7's for an on whose preview cannot count and for an on deferred during
// a drain, and the standby's.
const (
	// offAdminOnly is a person's view of the off who holds no admin role.
	offAdminOnly = "Only an admin turns the kill switch off."
	// onRefused is a person's view of the on who may not turn it on.
	onRefused = "Only a responder or an admin turns the kill switch on."
	// unreadSwitch is the control while the table cannot be read: a form
	// names the latest row, which is not known.
	unreadSwitch = "The kill switch's state could not be read, so it cannot be set from this page now. Open this page again in a moment."
)

// switchPage is /kill-switch: the switch's state as its table holds it;
// the control, disabled on a standby; the on's form, after its preview's
// consequences or D7's words; the off's form, after the items the latest
// on left not staffed, or the fresh link; a refusal; or an answer.
type switchPage struct {
	Read    bool
	On      bool
	Seq     int64
	Standby *standbyView
	Unread  string
	Refusal *refusalView
	Done    *switchDone

	// The control: the reason no form is offered, or the fresh link that
	// would offer the off; the on's preview, or that it cannot count; the
	// off's list; and the form.
	NoForm      string
	Fresh       string
	Blocked     *refusalView
	Preview     []string
	Uncounted   bool
	OffList     []string
	OffListNone bool
	OffUnread   bool
	Form        *switchForm
}

// switchForm is the kill switch's form: which way it turns the switch,
// After, the table's row read, the CSRF token, and, for the on, its
// reasons, with the text typed kept.
type switchForm struct {
	On                bool
	After             int64
	Token, TokenField string
	Reasons           []reasonButton
	Text              string
}

// switchDone is a kill switch's answer, what happened (I6): its state and
// row after it, whether it changed, whether the on was deferred during a
// drain; the runs it stopped, the items it left not staffed and the items
// its rule still acts on; at an off, each item the latest on left not
// staffed, with its state now, and whether that list was read in full.
type switchDone struct {
	On, Changed, Deferred bool
	Seq                   int64
	Stopped               []string
	StoppedCount          string
	Unstaffed             []string
	UnstaffedCount        string
	Owed                  []string
	OwedCount             string
	Stayed                []stayedView
	Unread                bool
}

// stayedView is an item the latest on left not staffed, as the off's
// answer shows it: its state now, in words, and the link to promote it.
type stayedView struct {
	ID, State, Promote string
}

// switchControl serves the control, GET /kill-switch (design D7): the
// switch's state from its table, and the form that turns it the other way,
// whose After is the table's row. The on is offered to anyone app.May lets
// turn it on, an admin whose freshness lapsed included (I1), after its
// preview; the off, to an admin while fresh, after the items the latest on
// left not staffed. On a standby nothing is offered and nothing asked.
func (s *site) switchControl(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sg, actor, _ := s.a.Acting(r)
	l := s.frame(ctx, &sg, "Kill switch")
	page := &switchPage{}
	l.Body = page
	s.control(ctx, page, &l, &sg, actor, "")
	s.render(w, r, http.StatusOK, "switch", &l)
}

// switchCommit turns the switch, POST /kill-switch, through switching.Set,
// which routes an on during a drain to RecordKillSwitchOn as the admin API
// does (R81). Its answer is what happened. A refusal is shown in words,
// after the table is read again, with the control as the switch stands now
// and a fresh form at the row read (R82), the text typed kept. A standby
// refuses before anything is sent. It never redirects (I8).
func (s *site) switchCommit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sg, actor, _ := s.a.Acting(r)
	l := s.frame(ctx, &sg, "Kill switch")
	page := &switchPage{}
	l.Body = page
	if l.Standby != nil {
		page.state(&l)
		page.Refusal = &refusalView{Code: switching.CodeNotHeld, Words: standbyWords, Standby: l.Standby}
		s.render(w, r, http.StatusConflict, "switch", &l)
		return
	}
	on, after, bad := switchOf(r.PostFormValue(switchField), r.PostFormValue(afterField))
	var reason *board.Reason
	text := ""
	if bad == "" && on {
		reason, text, bad = reasonOf(switchReasons, r.PostFormValue(reasonField), r.PostFormValue(textField))
	}
	if bad != "" {
		page.Refusal = &refusalView{Code: switching.CodeInvalid, Words: bad}
		s.control(ctx, page, &l, &sg, actor, text)
		s.render(w, r, http.StatusBadRequest, "switch", &l)
		return
	}
	res := switching.Set(ctx, s.p.Switching, on, &app.Act{Actor: actor, After: after, Reason: reason})
	// The table is read again, whatever the answer: the header then shows
	// the switch as it stands after it.
	l = s.frame(ctx, &sg, "Kill switch")
	l.Body = page
	if res.Switched != nil {
		page.state(&l)
		page.Done = doneOf(res.Switched, res.Deferred)
		s.render(w, r, http.StatusOK, "switch", &l)
		return
	}
	refusal := res.Refusal
	page.Refusal = s.refusal(refusal, &sg, switchPath)
	page.Refusal.Again = ""
	s.control(ctx, page, &l, &sg, actor, text)
	s.answer(w, refusal)
	s.render(w, r, refusal.Status, "switch", &l)
}

// switchOf is the way a form turns the switch and its After, or what is
// wrong with them, in words.
func switchOf(way, rawAfter string) (on bool, after int64, bad string) {
	switch way {
	case "on":
		on = true
	case "off":
	default:
		return false, 0, "This form does not say whether to turn the kill switch on or off. Use the form below."
	}
	after, err := strconv.ParseInt(rawAfter, 10, 64)
	if err != nil || after < 0 {
		return false, 0, "This form does not carry the kill switch's row it was read at. Use the form below."
	}
	return on, after, ""
}

// state is the switch's state on the page, as the frame read its table.
func (p *switchPage) state(l *layout) {
	p.Read, p.On, p.Seq = l.Switch.Read, l.Switch.On, l.Switch.Seq
}

// control fills page's control for actor, as the frame l read the table:
// disabled on a standby, and with no form while the table cannot be read;
// else the form that turns the switch the other way, its After the row
// read, with text, the reason typed, kept.
func (s *site) control(ctx context.Context, page *switchPage, l *layout, sg *auth.Signed, actor app.Actor, text string) {
	page.state(l)
	switch {
	case l.Standby != nil:
		page.Standby = l.Standby
		return
	case !l.Switch.Read:
		page.Unread = unreadSwitch
		return
	case l.Switch.On:
		s.offControl(ctx, page, l, sg, actor)
		return
	}
	if _, err := app.May(actor, app.ActSwitchOn, nil); err != nil {
		page.NoForm = onRefused
		return
	}
	form := &switchForm{On: true, After: l.Switch.Seq, Token: sg.Token, TokenField: auth.TokenField, Reasons: switchReasons, Text: text}
	res := switching.Set(ctx, s.p.Switching, true, &app.Act{Actor: actor, After: l.Switch.Seq, Preview: true})
	switch {
	case res.Switched != nil:
		page.Preview = switchConsequences(&res.Switched.Consequences)
	case uncounted(res.Refusal.Code):
		page.Uncounted = true
	default:
		page.Blocked = s.refusal(res.Refusal, sg, "")
		return
	}
	page.Form = form
}

// offControl fills the off's control: for an admin while fresh, the items
// the latest on left not staffed, from the records (M7), and the form; for
// a session that holds the admin role and is not fresh, the fresh link
// back to the control (R76); for anyone else, that only an admin turns it
// off.
func (s *site) offControl(ctx context.Context, page *switchPage, l *layout, sg *auth.Signed, actor app.Actor) {
	if _, err := app.May(actor, app.ActSwitchOff, nil); err != nil {
		if sg.Session.Has(auth.Admin) {
			page.Fresh = freshLink(switchPath)
			return
		}
		page.NoForm = offAdminOnly
		return
	}
	ids, err := s.p.Views.UnstaffedBy(ctx, l.Switch.OnSeq)
	switch {
	case err != nil:
		s.log.WarnContext(ctx, "the web interface could not read the items the kill switch left unstaffed", "onSeq", l.Switch.OnSeq, "err", err)
		page.OffUnread = true
	case len(ids) == 0:
		page.OffListNone = true
	}
	for _, id := range ids {
		page.OffList = append(page.OffList, string(id))
	}
	page.Form = &switchForm{After: l.Switch.Seq, Token: sg.Token, TokenField: auth.TokenField}
}

// switchConsequences are an on's preview in words: the runs it stops, on
// how many items, and the items it leaves not staffed.
func switchConsequences(c *app.Consequences) []string {
	items := map[item.ID]bool{}
	for _, stop := range c.Stops {
		items[stop.Item] = true
	}
	out := []string{"Stops no run"}
	if len(c.Stops) > 0 {
		out[0] = "Stops " + plural(len(c.Stops), "run") + " on " + plural(len(items), "item")
	}
	if n := len(c.Unstaffed); n > 0 {
		out = append(out, "Leaves "+plural(n, "item")+" not staffed")
	}
	return out
}

// doneOf is a kill switch's answer in words (I6).
func doneOf(out *app.KillSwitchOutcome, deferred bool) *switchDone {
	d := &switchDone{On: out.On, Changed: out.Changed, Deferred: deferred || out.Deferred, Seq: out.Seq}
	for _, stop := range out.Stopped {
		d.Stopped = append(d.Stopped, "Run "+string(stop.Run)+" of item "+string(stop.Item)+", at round "+strconv.Itoa(stop.Round))
	}
	d.StoppedCount = plural(len(out.Stopped), "run")
	for _, id := range out.Unstaffed {
		d.Unstaffed = append(d.Unstaffed, string(id))
	}
	d.UnstaffedCount = plural(len(out.Unstaffed), "item")
	for _, id := range out.Owed {
		d.Owed = append(d.Owed, string(id))
	}
	d.OwedCount = plural(len(out.Owed), "item")
	for _, u := range out.StayedUnstaffed {
		v := stayedView{ID: string(u.Item), State: stateWords[string(u.State)]}
		switch {
		case u.State == "":
			v.State = "its state could not be read"
		case v.State == "":
			v.State = string(u.State)
		}
		// A closed item, or one investigating, has nothing to promote; one
		// whose state could not be read is offered the link, whose form
		// previews the act first.
		if u.State != item.StatusClosed && u.State != item.StatusInvestigating {
			v.Promote = actPath(u.Item, app.ActPromote)
		}
		d.Stayed = append(d.Stayed, v)
	}
	d.Unread = out.StayedUnstaffedUnread
	return d
}
