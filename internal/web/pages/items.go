package pages

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// itemsPage is /items: the rows grouped by incident, or that the list
// could not be read; on a standby, that acts are taken on the leader; and,
// for an admin whose freshness lapsed and who is refused an act on a row,
// the link to authenticate again (R76).
type itemsPage struct {
	Unread  bool
	Groups  []group
	Standby bool
	Fresh   string
	// Open is the link to the opening form, for a person who may open.
	Open string
}

// group is an incident's rows. Each item is its own incident until Plan
// 7c-1's links (S6); a row whose records cannot be read has none.
type group struct {
	Incident string
	Rows     []rowView
}

// rowView is a row as /items shows it: every status in words (D10).
type rowView struct {
	ID          string
	Board       string
	Title       string
	Service     string
	Principal   string
	NoPrincipal bool
	Responders  []string
	State       string
	Notes       []string
	Later       bool
	Unreadable  bool
	Acts        []actLink
}

// actLink is a link to an act's form, which previews it.
type actLink struct {
	Label, Href string
}

// stateWords are the items' states in words (D10).
var stateWords = map[string]string{
	string(item.StatusQueued):        "Queued",
	string(item.StatusInvestigating): "Investigating",
	string(item.StatusAwaitingClose): "Awaiting close",
	string(item.StatusClosed):        "Closed",
}

// items serves /items (design D8): the rows of Views.List, grouped by
// incident in the list's order, each with the acts app.May allows the
// actor on it, and none on a standby.
func (s *site) items(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sg, actor, _ := s.a.Acting(r)
	l := s.frame(ctx, &sg, "Items")
	page := &itemsPage{Standby: l.Standby != nil}
	if _, err := app.May(actor, app.ActOpen, nil); err == nil && !page.Standby {
		page.Open = openPath
	}
	l.Body = page
	rows, err := s.p.Views.List(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "the web interface could not list the items", "err", err)
		page.Unread = true
		s.render(w, r, http.StatusServiceUnavailable, "items", &l)
		return
	}
	at := map[item.IncidentID]int{}
	refused := false
	for i := range rows {
		v, adminRefused := s.rowView(&rows[i], actor, page.Standby)
		refused = refused || adminRefused
		k, seen := at[rows[i].Incident]
		if !seen {
			k = len(page.Groups)
			at[rows[i].Incident] = k
			page.Groups = append(page.Groups, group{Incident: string(rows[i].Incident)})
		}
		page.Groups[k].Rows = append(page.Groups[k].Rows, v)
	}
	if refused && sg.Session.Has(auth.Admin) && !actor.Admin {
		page.Fresh = freshLink("/items")
	}
	s.render(w, r, http.StatusOK, "items", &l)
}

// rowView is row's view for actor, and whether app.May refused the actor an
// act the row's state offers, which an admin who is fresh again may take.
func (s *site) rowView(row *app.Row, actor app.Actor, standby bool) (rowView, bool) {
	v := rowView{ID: string(row.ID), Board: boardPath(row.ID), Later: row.HeldByLaterBuild, Unreadable: row.Unreadable}
	if v.Later || v.Unreadable {
		return v, false
	}
	v.Title, v.Service, v.Principal, v.Responders = row.Title, row.Service, row.Principal, row.Responders
	v.NoPrincipal = row.Principal == ""
	v.State = row.State
	if words, ok := stateWords[row.State]; ok {
		v.State = words
	}
	v.Notes = s.notes(row)
	if standby {
		return v, false
	}
	refused := false
	opened := &item.Opened{Principal: row.Principal, Responders: row.Responders}
	for _, kind := range s.offered(row) {
		if _, err := app.May(actor, kind, opened); err != nil {
			refused = true
			continue
		}
		v.Acts = append(v.Acts, actLink{Label: actWords[kind].verb, Href: actPath(row.ID, kind)})
	}
	return v, refused
}

// offered are the acts a row's state offers: a promotion of an item queued
// or awaiting its close, a close of an item not closed, and a reopen of a
// closed item within its window. Who may take them is app.May's alone.
func (s *site) offered(row *app.Row) []app.ActKind {
	switch item.Status(row.State) {
	case item.StatusQueued, item.StatusAwaitingClose:
		return []app.ActKind{app.ActPromote, app.ActClose}
	case item.StatusInvestigating:
		return []app.ActKind{app.ActClose}
	case item.StatusClosed:
		if s.now().Before(row.ReopenableUntil) {
			return []app.ActKind{app.ActReopen}
		}
	}
	return nil
}

// notes are a row's lines beside its state: its place in the queue, or
// that it is not known on a standby; when it closes by itself, or why that
// is not known; and a closed item's close and reopen window.
func (s *site) notes(row *app.Row) []string {
	var out []string
	if item.Status(row.State) == item.StatusQueued {
		switch {
		case !row.PositionKnown:
			out = append(out, "Queue position: not known on this instance")
		case row.QueuePosition > 0:
			out = append(out, "position "+strconv.Itoa(row.QueuePosition)+" in the queue")
		}
	}
	if line := autoClose(s, row.AutoCloseUnread, row.AutoClosePaused, row.ClosesAtUnread, row.ClosesAt); line != "" {
		out = append(out, line)
	}
	if !row.ClosedAt.IsZero() {
		out = append(out, "closed at "+s.at(row.ClosedAt)+" ("+s.ago(row.ClosedAt)+")")
		if s.now().Before(row.ReopenableUntil) {
			out = append(out, "may be reopened until "+s.at(row.ReopenableUntil))
		} else {
			out = append(out, "may no longer be reopened")
		}
	}
	return out
}

// actPath is the path of the act kind's form on item id.
func actPath(id item.ID, kind app.ActKind) string {
	return "/items/" + url.PathEscape(string(id)) + "/" + string(kind)
}

// freshLink is the link to fresh authentication that comes back to back,
// a page's GET, never a form's POST (I8).
func freshLink(back string) string {
	return auth.FreshPath + "?return=" + url.QueryEscape(back)
}
