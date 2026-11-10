package pages

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
)

// layout is what every page's frame shows: its title; the person signed
// in, and the CSRF token of the sign-out form; the kill switch's fixed
// header; and, on a standby, the standby's notice. Body is the page's own.
type layout struct {
	Title      string
	Subject    string
	Token      string
	TokenField string
	Switch     switchHeader
	Standby    *standbyView
	Body       any
}

// switchHeader is the kill switch as its table holds it (M6): read, on or
// off, since when, by whom, for what reason, and the row read.
type switchHeader struct {
	Read   bool
	On     bool
	Since  string
	Ago    string
	By     string
	Code   string
	Text   string
	Seq    int64
	OnSeq  int64
	Exists bool
}

// frame is the frame of a page titled title for the signed-in sg: the
// kill switch read from its table alone, Views.KillSwitch, and never a
// preview, which takes the coordinator's lock (M6); and the standby's
// notice when this instance does not hold the lease.
func (s *site) frame(ctx context.Context, sg *auth.Signed, title string) layout {
	l := layout{Title: title, Subject: auth.SubjectPrefix + sg.Session.Subject, Token: sg.Token, TokenField: auth.TokenField}
	ks, err := s.p.Views.KillSwitch(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "the web interface could not read the kill switch", "err", err)
	} else {
		l.Switch = switchHeader{
			Read: true, On: ks.On, By: ks.By, Code: ks.Reason.Code, Text: ks.Reason.Text, Seq: ks.Seq, OnSeq: ks.OnSeq, Exists: ks.Seq > 0,
			Since: s.at(ks.Since), Ago: s.ago(ks.Since),
		}
	}
	if s.standby() {
		l.Standby = s.leader(ctx)
	}
	return l
}

// standby says whether this instance does not hold the lease (R-b).
func (s *site) standby() bool {
	_, held, _ := s.p.Leader.Status()
	return !held
}

// at is t in the deployment's zone, to the minute (M9); empty for the
// zero time.
func (s *site) at(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(s.zone).Format("2006-01-02 15:04 MST")
}

// ago is t relative to now, in words, beside the time itself, since a time
// in a zone not the reader's is easily misread at night (M9).
func (s *site) ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := s.now().Sub(t)
	suffix := "ago"
	if d < 0 {
		d, suffix = -d, "from now"
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min %s", int(d/time.Minute), suffix)
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h %s", int(d/time.Hour), suffix)
	}
	return fmt.Sprintf("%d days %s", int(d/(24*time.Hour)), suffix)
}

// templates are the pages' templates, each with the layout.
var templates = map[string]*template.Template{
	"items":  parse("items.html"),
	"act":    parse("act.html"),
	"open":   parse("open.html"),
	"board":  parse("board.html"),
	"switch": parse("killswitch.html"),
	"output": parse("output.html"),
}

// funcs are the templates' functions: the two display functions for stored
// text, each a plain string that html/template still escapes, which a
// template prints inside <bdi> (design D11; R154).
var funcs = template.FuncMap{"inertText": inertText, "inertLine": inertLine}

// parse is the layout with the page's template, name, and the functions.
func parse(name string) *template.Template {
	return template.Must(template.New("layout.html").Funcs(funcs).ParseFS(files, "templates/layout.html", "templates/"+name))
}

// render answers status with the page name, framed by l, never cached. A
// page that cannot be rendered answers 500 with words, never half a page.
func (s *site) render(w http.ResponseWriter, r *http.Request, status int, name string, l *layout) {
	var b bytes.Buffer
	if err := templates[name].Execute(&b, l); err != nil {
		s.log.ErrorContext(r.Context(), "a page could not be rendered", "page", name, "err", err)
		http.Error(w, "the page could not be rendered", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(b.Len()))
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes()) //nolint:gosec // G705: rendered by html/template, which escapes every value
}
