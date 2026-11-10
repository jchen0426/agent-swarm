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
	"github.com/jchen0426/agent-swarm/internal/work/app"
)

// layout is what every page's frame shows: its title; the person signed
// in, and the CSRF token of the sign-out form; the kill switch's fixed
// header; and, on a standby, the standby's notice. The header and the notice
// are the board's first two regions, each its own named template (R156), and
// StandbyV the notice's version (see versioned). Body is the page's own.
type layout struct {
	Title      string
	Subject    string
	Token      string
	TokenField string
	Switch     switchHeader
	Standby    *standbyView
	StandbyV   string
	Body       any
}

// StandbyRegion is the standby region's model: its version and the notice,
// nil on a leader.
func (l *layout) StandbyRegion() standbyRegion {
	return standbyRegion{V: l.StandbyV, View: l.Standby}
}

// switchHeader is the kill switch as its table holds it (M6): read, on or
// off, since when, and the row read; and, on, its line of parts (see
// switchOf), which says by whom and for what reason. V is the region's
// version (see versioned).
type switchHeader struct {
	V      string
	Read   bool
	On     bool
	Line   line
	Since  string
	Ago    string
	Seq    int64
	OnSeq  int64
	Exists bool
}

// frame is the frame of a page 9a-1 shipped outside the board, titled title,
// for the signed-in sg: the kill switch read from its table alone,
// Views.KillSwitch, on the main pool (R143), and never a preview, which
// takes the coordinator's lock (M6); and the standby's notice when this
// instance does not hold the lease.
func (s *site) frame(ctx context.Context, sg *auth.Signed, title string) layout {
	l := s.framed(sg, title)
	ks, err := s.p.Views.KillSwitch(ctx)
	if err != nil {
		s.log.WarnContext(ctx, "the web interface could not read the kill switch", "err", err)
	} else {
		l.Switch = s.switchOf(&ks)
	}
	if s.standby() {
		l.Standby = s.leader(ctx)
	}
	return l
}

// boardFrame is the frame of a board page, titled title, for the signed-in
// sg: the kill switch as the board watch's tick read it, from f, the read's
// facts, a failed read's included, which carry its tick's switch when it had
// one in hand (app.BoardWatch.Board), never a read of its own (R139, R143),
// so that no board page reads the table on the main pool; not read when the
// read had no tick, or the tick could not read it. On a standby, the
// standby's notice, from the board pages' read of the lease's holder, kept a
// second (see boardLeader).
func (s *site) boardFrame(ctx context.Context, sg *auth.Signed, title string, f *app.BoardFacts) layout {
	l := s.framed(sg, title)
	if f.KillSwitchRead {
		l.Switch = s.switchOf(&f.KillSwitch)
	}
	if s.standby() {
		l.Standby = s.boardLeader(ctx)
	}
	return l
}

// framed is the frame of a page titled title for the signed-in sg, its kill
// switch not read, and no standby's notice.
func (s *site) framed(sg *auth.Signed, title string) layout {
	return layout{Title: title, Subject: auth.SubjectPrefix + sg.Session.Subject, Token: sg.Token, TokenField: auth.TokenField}
}

// switchOf is the header of the kill switch as its table holds it, ks; on,
// a line of parts (R171; re-review RR-3): since when, by whom, a name, its
// reason in words.md's general form, its code by the on form's preset
// label or as itself, then its text, a person's words in their box, so that
// a reason can never read as the header's own words; and the row read.
func (s *site) switchOf(ks *app.KillSwitch) switchHeader {
	h := switchHeader{Read: true, On: ks.On, Seq: ks.Seq, OnSeq: ks.OnSeq, Exists: ks.Seq > 0, Since: s.at(ks.Since), Ago: s.ago(ks.Since)}
	if ks.On {
		on := line(nil).say("Kill switch: on since " + h.Since + " (" + h.Ago + ") by ").name(ks.By)
		h.Line = reasonedOf(on, &ks.Reason, switchReasonWords).say("; row " + strconv.FormatInt(ks.Seq, 10))
	}
	return h
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

// atLatest is at of t rounded up to the minute, so that the minute shown is
// never before t (R146): a time a person waits on, "by T at the latest" or
// "confirming until T".
func (s *site) atLatest(t time.Time) string {
	if up := t.Truncate(time.Minute); !up.Equal(t) {
		t = up.Add(time.Minute)
	}
	return s.at(t)
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
