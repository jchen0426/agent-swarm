package pages

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The act form's fields: the key and its MAC (R85), and the reason.
const (
	keyField    = "k"
	macField    = "mac"
	reasonField = "reason"
	textField   = "text"
)

// otherReason is the reason whose words the person writes.
const otherReason = "other"

// keyBytes is a form key's length, in random bytes (R85).
const keyBytes = 16

// actWord is an act's words: its button's verb, its page's title, and what
// its answer says it did.
type actWord struct {
	verb, title, done string
	// reasons are its preset reasons, each a one-tap button, beside other;
	// none for an act that takes no reason on the web.
	reasons []reasonButton
}

// reasonButton is a preset reason's code and its words.
type reasonButton struct {
	Code, Label string
}

// actWords are the item acts' words, by kind.
var actWords = map[app.ActKind]actWord{
	app.ActPromote: {verb: "Promote", title: "Promote the item", done: "Done: the item was promoted."},
	app.ActClose: {verb: "Close", title: "Close the item", done: "Done: the item was closed.", reasons: []reasonButton{
		{Code: "resolved", Label: "Resolved"}, {Code: "false_alarm", Label: "False alarm"}, {Code: "duplicate", Label: "Duplicate"},
	}},
	app.ActReopen: {verb: "Reopen", title: "Reopen the item", done: "Done: the item was reopened.", reasons: []reasonButton{
		{Code: "not_resolved", Label: "Not resolved"}, {Code: "recurred", Label: "It came back"},
	}},
}

// actPage is an act's page: its form, after its preview's consequences, or
// labeled that they cannot be counted (R84); its refusal; or its answer.
type actPage struct {
	Item      string
	Title     string
	Verb      string
	Path      string
	Preview   []string
	Uncounted bool
	Refusal   *refusalView
	Form      *formView
	Done      *doneView
}

// formView is an act's form: its key and MAC, minted once per rendering,
// and kept by a retry after an unknown outcome (R85; I7); the CSRF token;
// and the reasons.
type formView struct {
	Key, MAC          string
	Token, TokenField string
	Reasons           []reasonButton
	TakesReason       bool
	Text              string
	// Fixed is the reason a retry sends again, with its key: shown, not
	// chosen again, so that the key is never sent with another reason
	// (review M2).
	Fixed *fixedReason
}

// fixedReason is a retry's reason: its code, its words, and its text.
type fixedReason struct {
	Code, Label, Text string
}

// doneView is an act's answer: what it did, and its consequences.
type doneView struct {
	Words        string
	Consequences []string
}

// refusalView is a refusal in the web's words (m4): its code, its words, a
// reason the coordinator gave for an invalid act, the leader's name on a
// standby, and, for an admin whose freshness lapsed, the link to
// authenticate again (R76).
type refusalView struct {
	Code    string
	Words   string
	Detail  string
	Standby *standbyView
	Fresh   string
	Again   string
}

// actOf is the act a request's path names, and its page.
func (s *site) actOf(r *http.Request) (app.ActKind, item.ID, *actPage, bool) {
	kind := app.ActKind(r.PathValue("act"))
	words, ok := actWords[kind]
	if !ok {
		return "", "", nil, false
	}
	id := item.ID(r.PathValue("id"))
	return kind, id, &actPage{Item: string(id), Title: words.title, Verb: words.verb, Path: actPath(id, kind)}, true
}

// actForm serves an act's form, GET /items/{id}/{act} (design D8): the
// act's preview, then the form that commits it, with a key minted now. A
// close whose preview cannot answer, as during a drain, while the instance
// takes over, or with its outcome unknown, is still offered, labeled so
// (R84). A refusal is shown in words; on a standby nothing is asked.
func (s *site) actForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sg, actor, _ := s.a.Acting(r)
	kind, id, page, ok := s.actOf(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	l := s.frame(ctx, &sg, page.Title)
	l.Body = page
	if l.Standby != nil {
		page.Refusal = &refusalView{Code: switching.CodeNotHeld, Words: standbyWords, Standby: l.Standby}
		s.render(w, r, http.StatusConflict, "act", &l)
		return
	}
	out, err := s.act(ctx, kind, id, &app.Act{Actor: actor, Preview: true})
	if err == nil {
		page.Preview = s.consequences(&out.Consequences)
		page.Form = s.newForm(&sg, actor, kind, id)
		s.render(w, r, http.StatusOK, "act", &l)
		return
	}
	refusal := switching.Act(ctx, s.p.Switching, err)
	if kind == app.ActClose && uncounted(refusal.Code) {
		page.Uncounted = true
		page.Form = s.newForm(&sg, actor, kind, id)
		s.render(w, r, http.StatusOK, "act", &l)
		return
	}
	page.Refusal = s.refusal(refusal, &sg, page.Path)
	if uncounted(refusal.Code) {
		// A preview is never an act: nothing was tried, whatever the
		// commit's words for the code say (review M1).
		page.Refusal.Words, page.Refusal.Detail = notPreviewed, ""
	}
	s.answer(w, refusal)
	s.render(w, r, refusal.Status, "act", &l)
}

// notPreviewed is the words of a preview that could not answer, whose act
// is not offered.
const notPreviewed = "The act could not be previewed just now, and nothing was tried. Open it again in a moment."

// actCommit commits an act, POST /items/{id}/{act} (R83, R85): its key is
// the form's, refused invalid unless its MAC is the forms key's of the
// actor's subject, the act, the item and the key. An answer whose outcome
// is not known yet, unavailable, or taking_over, renders the form again
// with the same key and MAC, so that sending it again is the same act; any
// other answer is final, and a new act takes a new form. It never
// redirects (I8).
func (s *site) actCommit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sg, actor, _ := s.a.Acting(r)
	kind, id, page, ok := s.actOf(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	l := s.frame(ctx, &sg, page.Title)
	l.Body = page
	if l.Standby != nil {
		page.Refusal = &refusalView{Code: switching.CodeNotHeld, Words: standbyWords, Standby: l.Standby}
		s.render(w, r, http.StatusConflict, "act", &l)
		return
	}
	key, mac := r.PostFormValue(keyField), r.PostFormValue(macField)
	if key == "" || !sg.Keys.ValidForm(actor.Subject, string(kind), string(id), key, mac) {
		page.Refusal = &refusalView{
			Code: switching.CodeInvalid, Words: words[switching.CodeInvalid],
			Detail: "This form is not one this site gave you for this act, or it was changed. Open the act again.", Again: page.Path,
		}
		s.render(w, r, http.StatusBadRequest, "act", &l)
		return
	}
	form := s.form(&sg, kind, key, mac)
	reason, text, missing := reasonOf(actWords[kind].reasons, r.PostFormValue(reasonField), r.PostFormValue(textField))
	if missing != "" {
		form.Text = text
		page.Form, page.Refusal = form, &refusalView{Code: switching.CodeInvalid, Words: missing}
		s.render(w, r, http.StatusBadRequest, "act", &l)
		return
	}
	out, err := s.act(ctx, kind, id, &app.Act{Actor: actor, Key: key, Reason: reason})
	if err == nil {
		page.Done = s.done(kind, &out)
		s.render(w, r, http.StatusOK, "act", &l)
		return
	}
	refusal := switching.Act(ctx, s.p.Switching, err)
	page.Refusal = s.refusal(refusal, &sg, page.Path)
	switch refusal.Code {
	case switching.CodeUnavailable, switching.CodeTakingOver:
		// The same act again: the same key, with the same reason, fixed.
		form.Fixed = fixed(kind, reason)
		page.Form, page.Refusal.Again = form, ""
	case switching.CodeInvalid:
		// A final answer: a new form, a new key, with the person's text
		// kept, so that they mend it rather than type it again (review M4).
		next := s.newForm(&sg, actor, kind, id)
		next.Text = text
		page.Form, page.Refusal.Again = next, ""
	}
	s.answer(w, refusal)
	s.render(w, r, refusal.Status, "act", &l)
}

// fixed is the retry's view of reason, the reason a commit sent for kind;
// nil for none.
func fixed(kind app.ActKind, reason *board.Reason) *fixedReason {
	if reason == nil {
		return nil
	}
	f := &fixedReason{Code: reason.Code, Label: "Another reason", Text: reason.Text}
	for _, p := range actWords[kind].reasons {
		if p.Code == reason.Code {
			f.Label = p.Label
		}
	}
	return f
}

// act asks the coordinator for the act kind on id.
func (s *site) act(ctx context.Context, kind app.ActKind, id item.ID, a *app.Act) (app.Outcome, error) {
	switch kind {
	case app.ActPromote:
		return s.p.Coordinator.Promote(ctx, id, a)
	case app.ActClose:
		return s.p.Coordinator.Close(ctx, id, a)
	case app.ActReopen:
		return s.p.Coordinator.Reopen(ctx, id, a)
	}
	return app.Outcome{}, fmt.Errorf("%w: no act of the kind %q", app.ErrInvalid, kind)
}

// uncounted says whether a preview's refusal of code says only that the
// consequences cannot be counted now, not that the act is refused (R84).
func uncounted(code string) bool {
	return code == switching.CodeDraining || code == switching.CodeTakingOver || code == switching.CodeUnavailable
}

// answer sets a refusal's Retry-After, for one to try again shortly.
func (s *site) answer(w http.ResponseWriter, refusal *switching.Refusal) {
	if refusal.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(refusal.RetryAfter.Seconds()))))
	}
}

// refusal is refusal in the web's words (m4). An admin's act on an item they
// are not on, refused admin_only or not_item_responder for a session that
// holds the admin role, renders "Re-authenticate to act as an admin" with
// the fresh link, back to the act's form, a GET, never the POST (R76; I8).
func (s *site) refusal(refusal *switching.Refusal, sg *auth.Signed, back string) *refusalView {
	v := &refusalView{Code: refusal.Code, Words: words[refusal.Code], Again: back}
	if v.Words == "" {
		v.Words = words[switching.CodeUnavailable]
	}
	if refusal.Code == switching.CodeInvalid && refusal.Err != nil {
		v.Detail = switching.Invalid("This act", refusal.Err) + "."
	}
	if (refusal.Code == app.CodeAdminOnly || refusal.Code == app.CodeNotItemResponder) && sg.Session.Has(auth.Admin) {
		v.Fresh = freshLink(back)
	}
	return v
}

// newForm is the act's form with a key minted now, 16 random bytes in
// base64url, and its MAC under the forms key (R85).
func (s *site) newForm(sg *auth.Signed, actor app.Actor, kind app.ActKind, id item.ID) *formView {
	key := mintKey()
	return s.form(sg, kind, key, sg.Keys.FormMAC(actor.Subject, string(kind), string(id), key))
}

// mintKey is a form key: 16 random bytes in base64url (R85).
func mintKey() string {
	b := make([]byte, keyBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go 1.24).
	return base64.RawURLEncoding.EncodeToString(b)
}

// form is the act's form carrying key and mac.
func (s *site) form(sg *auth.Signed, kind app.ActKind, key, mac string) *formView {
	reasons := actWords[kind].reasons
	return &formView{Key: key, MAC: mac, Token: sg.Token, TokenField: auth.TokenField, Reasons: reasons, TakesReason: len(reasons) > 0}
}

// reasonOf is the reason a form posted, of the presets its act offers: none
// for an act that offers none, a promotion; else a preset code alone, the
// box's words ignored (review M3), or other, with its text, which the
// coordinator checks and redacts.
// The text is trimmed and its line endings made LF, as a browser submits
// CRLF, and is at most board.MaxReasonRunes characters, counted as the
// coordinator counts them (review M4). What is missing or too long is said
// in words instead; text is the box's words, made so, for the form to keep.
func reasonOf(presets []reasonButton, code, raw string) (reason *board.Reason, text, missing string) {
	text = strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	if len(presets) == 0 {
		return nil, text, ""
	}
	if code == otherReason {
		switch n := utf8.RuneCountInString(text); {
		case n == 0:
			return nil, text, "Write the reason in the box, or choose one of the reasons."
		case n > board.MaxReasonRunes:
			return nil, text, fmt.Sprintf("A reason holds at most %d characters; this one holds %d. Shorten it, then send it again.", board.MaxReasonRunes, n)
		}
		return &board.Reason{Code: otherReason, Text: text}, text, ""
	}
	for _, p := range presets {
		if p.Code == code {
			return &board.Reason{Code: code}, text, ""
		}
	}
	return nil, text, "Choose one of the reasons."
}

// done is an act's answer in words.
func (s *site) done(kind app.ActKind, out *app.Outcome) *doneView {
	d := &doneView{Words: actWords[kind].done, Consequences: s.consequences(&out.Consequences)}
	if !out.Changed {
		d.Words = "Nothing changed: what you asked for already held."
	}
	return d
}

// consequences are an act's consequences in words (Contract C4).
func (s *site) consequences(c *app.Consequences) []string {
	var out []string
	if n := len(c.Stops); n > 0 {
		out = append(out, "Stops "+plural(n, "run"))
	}
	for _, id := range c.Starts {
		out = append(out, "Starts item "+string(id))
	}
	for _, id := range c.Closes {
		out = append(out, "Closes item "+string(id))
	}
	for _, id := range c.Unstaffed {
		out = append(out, "Leaves item "+string(id)+" not staffed")
	}
	switch {
	case c.AutoClosePaused:
		out = append(out, "Its auto-close is paused while the kill switch is on")
	case !c.ClosesAt.IsZero():
		out = append(out, "Closes by itself at "+s.at(c.ClosesAt)+" ("+s.ago(c.ClosesAt)+")")
	}
	if c.Limit > 0 {
		out = append(out, plural(c.Investigating, "item")+" investigating, of a limit of "+strconv.Itoa(c.Limit))
	}
	if len(out) == 0 {
		out = append(out, "Nothing else changes.")
	}
	return out
}

// plural is n and noun, plural unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
