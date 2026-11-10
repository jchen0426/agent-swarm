package pages

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/registry/config"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The opening form's fields, beside the key and its MAC.
const (
	typeField      = "type"
	titleField     = "title"
	serviceField   = "service"
	principalField = "principal"
)

const (
	// openPath is the opening form's path.
	openPath = "/items/new"
	// openAct is both the act and the item of an opening's MAC (R85).
	openAct = "open"
	// webKeyPrefix keeps the web's opening keys apart from the admin API's
	// (R85; n2).
	webKeyPrefix = "web:"
	// openSource is the source of an item a person opens.
	openSource = "person"
	// serviceKey is the routing key of an item's service (R91).
	serviceKey = "service"
)

// openPage is /items/new: the form, after its refusal, or rendered again
// because the sign-in changed (R157); or the item opened.
type openPage struct {
	Path          string
	SignInChanged bool
	Refusal       *refusalView
	Form          *openForm
	Done          *openDone
}

// openForm is the opening's form: its key and MAC, minted once per
// rendering and kept by a retry after an unknown outcome (R85; I7); the
// types and services offered; the person's values; and, for an admin who is
// fresh, the field that names another principal. Fixed is a retry, which
// sends its values again as they were, since the key is bound to them.
type openForm struct {
	Key, MAC          string
	Token, TokenField string
	Types             []string
	Services          []string
	Type, Title       string
	Service           string
	Principal         string
	NamesPrincipal    bool
	Fixed             bool
	// Fresh is the link to authenticate again, for an admin whose freshness
	// lapsed, who may not name another principal (R76).
	Fresh string
}

// openDone is the item opened.
type openDone struct {
	ID, Path string
}

// openForm serves the opening form, GET /items/new (design D9): a type
// from the current revision's work item types, a title, a service with the
// services of the items open now as suggestions (R91), and the principal,
// which is the person unless they are a fresh admin (R78). A person who may
// not open is refused in words; on a standby nothing is asked.
func (s *site) openForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sg, actor, _ := s.a.Acting(r)
	page := &openPage{Path: openPath}
	l := s.frame(ctx, &sg, "Open an item")
	l.Body = page
	if l.Standby != nil {
		page.Refusal = &refusalView{Code: switching.CodeNotHeld, Words: standbyWords, Standby: l.Standby}
		s.render(w, r, http.StatusConflict, "open", &l)
		return
	}
	if _, err := app.May(actor, app.ActOpen, nil); err != nil {
		refusal := switching.Opening(ctx, s.p.Switching, err)
		page.Refusal = s.refusal(refusal, &sg, openPath)
		s.render(w, r, refusal.Status, "open", &l)
		return
	}
	form, ok := s.newOpenForm(r, &sg, actor)
	if !ok {
		page.Refusal = &refusalView{Code: switching.CodeUnavailable, Words: "No configuration is loaded, so no item can be opened just now. Try again in a moment.", Again: openPath}
		w.Header().Set("Retry-After", "5")
		s.render(w, r, http.StatusServiceUnavailable, "open", &l)
		return
	}
	page.Form = form
	s.render(w, r, http.StatusOK, "open", &l)
}

// newOpenForm is the form with a key minted now, 16 random bytes in
// base64url, and its MAC under the forms key (R85), its types and services
// offered; false while no revision is current.
func (s *site) newOpenForm(r *http.Request, sg *auth.Signed, actor app.Actor) (*openForm, bool) {
	key := mintKey()
	f := s.openFormFor(sg, actor, key, sg.Keys.FormMAC(actor.Subject, openAct, openAct, key))
	return f, s.offer(r, f)
}

// postedForm is the form carrying key and mac with the values r posted,
// trimmed: the principal is the person's own unless one is named.
func (s *site) postedForm(r *http.Request, sg *auth.Signed, actor app.Actor, key, mac string) *openForm {
	form := s.openFormFor(sg, actor, key, mac)
	form.Type = strings.TrimSpace(r.PostFormValue(typeField))
	form.Title = strings.TrimSpace(r.PostFormValue(titleField))
	form.Service = strings.TrimSpace(r.PostFormValue(serviceField))
	if p := strings.TrimSpace(r.PostFormValue(principalField)); p != "" {
		form.Principal = p
	}
	return form
}

// openFormFor is the form carrying key and mac, with the principal field. It
// needs no revision and reads nothing, so that a retry keeps its key whatever
// is current.
func (s *site) openFormFor(sg *auth.Signed, actor app.Actor, key, mac string) *openForm {
	f := &openForm{
		Key: key, MAC: mac, Token: sg.Token, TokenField: auth.TokenField,
		Principal: actor.Subject, NamesPrincipal: actor.Admin,
	}
	if sg.Session.Has(auth.Admin) && !actor.Admin {
		f.Fresh = freshLink(openPath)
	}
	return f
}

// offer fills the form's types, from the current revision, which must be
// one, and the services of the items open now, which are left out when the
// list cannot be read (R91).
func (s *site) offer(r *http.Request, f *openForm) bool {
	if s.p.Revisions == nil {
		return false
	}
	rev := s.p.Revisions.Current()
	if rev == nil {
		return false
	}
	f.Types = workTypes(rev)
	rows, err := s.p.Views.List(r.Context())
	if err != nil {
		s.log.WarnContext(r.Context(), "the web interface could not list the services open now", "err", err)
		return true
	}
	for i := range rows {
		if rows[i].Service != "" && rows[i].State != string(item.StatusClosed) && !rows[i].Unreadable && !rows[i].HeldByLaterBuild {
			f.Services = append(f.Services, rows[i].Service)
		}
	}
	slices.Sort(f.Services)
	f.Services = slices.Compact(f.Services)
	return true
}

// workTypes are the names of the revision's work item types, sorted.
func workTypes(rev *config.Revision) []string {
	var out []string
	for _, o := range rev.Objects() {
		if o.Kind == config.KindWorkItemType {
			out = append(out, o.Name)
		}
	}
	return out
}

// invalidOpening fills page for an opening refused invalid with err: its
// reason in words, the coordinator's, or ownSubjectWords for the person's
// own subject (I1); and, a final answer, a new form, a new key, with the
// person's values in form kept, so that they mend it rather than type it
// again; none for a person whose own subject the rules change and who can
// name no other principal, since it would be refused again.
func (s *site) invalidOpening(r *http.Request, page *openPage, sg *auth.Signed, actor app.Actor, form *openForm, err error) {
	own := errors.Is(err, app.ErrOwnSubjectRedacted)
	switch {
	case own:
		page.Refusal.Detail = ownSubjectWords
	case err != nil:
		page.Refusal.Detail = switching.Invalid("This opening", err) + "."
	}
	page.Refusal.Again = openPath
	next, ok := s.newOpenForm(r, sg, actor)
	if !ok {
		return
	}
	page.Refusal.Again = ""
	if own && !next.NamesPrincipal {
		return
	}
	next.Type, next.Title, next.Service = form.Type, form.Title, form.Service
	if next.NamesPrincipal {
		next.Principal = form.Principal
	}
	if next.Fresh != "" && form.Principal != actor.Subject {
		page.Refusal.Fresh = next.Fresh
	}
	page.Form = next
}

// ownSubjectWords say why an opening whose principal or responder is the
// person's own subject, which the deployment's rules that apply always
// change, is refused (I1), without quoting it.
const ownSubjectWords = "You cannot be an item's principal or responder: the subject you signed in with holds text " +
	"the redaction rules of this deployment remove, so it cannot be recorded on an item. " +
	"An admin must set the subject claim (web.oidc.subjectClaim) to one the rules leave alone, such as sub or an employee id."

// openCommit opens the item, POST /items/new (R78, R80, R85, R91): its key
// is the form's, refused invalid unless its MAC is the forms key's of the
// person's subject, the opening and the key. The item's id comes from the
// key, so that a form sent twice opens one item. The principal is the
// person, unless a fresh admin named another; whatever else is named is the
// coordinator's to refuse. An answer whose outcome is not known yet,
// unavailable, or taking_over, renders the form again with the same key,
// MAC and values, so that sending it again is the same opening; a refusal
// the person can mend renders a new form with their values kept. It never
// redirects (I8).
func (s *site) openCommit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sg, actor, _ := s.a.Acting(r)
	page := &openPage{Path: openPath}
	l := s.frame(ctx, &sg, "Open an item")
	l.Body = page
	if l.Standby != nil {
		page.Refusal = &refusalView{Code: switching.CodeNotHeld, Words: standbyWords, Standby: l.Standby}
		s.render(w, r, http.StatusConflict, "open", &l)
		return
	}
	key, mac := r.PostFormValue(keyField), r.PostFormValue(macField)
	if key == "" || !sg.Keys.ValidForm(actor.Subject, openAct, openAct, key, mac) {
		page.Refusal = &refusalView{
			Code: switching.CodeInvalid, Words: words[switching.CodeInvalid],
			Detail: "This form is not one this site gave you for this opening, or it was changed. Open the form again.", Again: openPath,
		}
		s.render(w, r, http.StatusBadRequest, "open", &l)
		return
	}
	form := s.postedForm(r, &sg, actor, key, mac)
	in := &app.OpenInput{
		ID: app.ItemID(s.p.Tenant, webKeyPrefix+key), Type: form.Type, Title: form.Title, Source: openSource,
		Principal: form.Principal, Actor: actor,
	}
	if form.Service != "" {
		in.Routing = map[string]string{serviceKey: form.Service}
	}
	in.Digest = app.OpeningDigest(in)
	id, err := s.p.Coordinator.Open(ctx, in)
	if err == nil {
		page.Done = &openDone{ID: string(id), Path: boardPath(id)}
		s.render(w, r, http.StatusOK, "open", &l)
		return
	}
	refusal := switching.Opening(ctx, s.p.Switching, err)
	page.Refusal = s.refusal(refusal, &sg, openPath)
	switch refusal.Code {
	case switching.CodeUnavailable, switching.CodeTakingOver:
		// The same opening again: the same key, with the same values, fixed.
		form.Fixed = true
		page.Form, page.Refusal.Again = form, ""
	case switching.CodeInvalid:
		s.invalidOpening(r, page, &sg, actor, form, refusal.Err)
	}
	s.answer(w, refusal)
	s.render(w, r, refusal.Status, "open", &l)
}

// openAgain is the opening's form again, for a POST /items/new whose token is
// present but not its session's, as a new sign-in leaves an open form's
// token behind (R157; design D14). When the form's key holds its MAC for the
// actor's subject, the form is rendered again with that key and MAC under
// the new session's token, saying that the sign-in changed, its values fixed
// as they were sent and shown inert for the person to check, so that sending
// it again is the same opening, which opens one item. Any other form,
// another person's among them, is refused as the CSRF gate refuses it. It
// asks the coordinator nothing.
func (s *site) openAgain(w http.ResponseWriter, r *http.Request) {
	sg, actor, _ := s.a.Acting(r)
	key, mac := r.PostFormValue(keyField), r.PostFormValue(macField)
	if key == "" || !sg.Keys.ValidForm(actor.Subject, openAct, openAct, key, mac) {
		auth.RefuseToken(w)
		return
	}
	form := s.postedForm(r, &sg, actor, key, mac)
	form.Fixed = true
	page := &openPage{Path: openPath, SignInChanged: true, Form: form}
	l := s.frame(r.Context(), &sg, "Open an item")
	l.Body = page
	s.render(w, r, http.StatusOK, "open", &l)
}
