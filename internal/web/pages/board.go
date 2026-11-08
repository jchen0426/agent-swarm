package pages

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// visible says whether an item's board may be shown (design D9). Plan 8a
// replaces it with the item's arm; until then every board is visible. Every
// route that shows a board asks it before it shows anything of the board.
var visible = func(*app.View) bool { return true }

// The fixed texts of spec Section 10.2, the board states.
const (
	hiddenCopy     = "Agents are working on this alert for measurement. Their output stays hidden until it is rated. Handle it with your usual process."
	readingCopy    = "Agents are reading. A message comes when the run stops."
	notStaffedCopy = "Not staffed: handle with your usual process"
	killSwitchCopy = "Stopped by the kill switch"
)

// boardPage is /items/{id}: the item's state in words; its banner; the run's
// stop; the conclusion and the hypotheses, whose statements are marked
// model-written; and when the page was last updated. It holds no quote, no
// raw output, no feed, and no live refresh (D9).
type boardPage struct {
	ID         string
	Type       string
	State      string
	Hidden     string
	Refusal    string
	Banner     string
	Lines      []string
	Promote    string
	Stop       string
	StopLine   string
	Conclusion *hypView
	Hypotheses []hypView
	Updated    string
}

// hypView is a hypothesis as the board shows it: its key, score and counts
// are platform facts; its statement is the model's.
type hypView struct {
	Status       string
	Key          string
	Score        int
	Supporting   int
	Refuting     int
	Sources      int
	Statement    string
	NotConverged bool
}

// board serves an item's board, GET /items/{id} (design D9): the item's
// state with spec 10.2's fixed texts, never before visible has said the
// board may be shown.
func (s *site) board(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sg, _, _ := s.a.Acting(r)
	id := item.ID(r.PathValue("id"))
	page := &boardPage{ID: string(id)}
	l := s.frame(ctx, &sg, "Item")
	l.Body = page
	view, err := s.p.Views.Item(ctx, id)
	switch {
	case errors.Is(err, app.ErrNoItem):
		page.Refusal = words[switching.CodeNoSuchItem]
		s.render(w, r, http.StatusNotFound, "board", &l)
		return
	case err != nil:
		s.log.ErrorContext(ctx, "the web interface could not read an item", "item", id, "err", err)
		page.Refusal = "The item could not be read just now. Reload the page to try again."
		s.render(w, r, http.StatusServiceUnavailable, "board", &l)
		return
	case view.HeldByLaterBuild:
		page.Refusal = words[switching.CodeHeldByLaterBuild]
		s.render(w, r, http.StatusOK, "board", &l)
		return
	}
	page.Type = view.Type
	page.Updated = s.at(s.now()) + " (" + s.ago(s.now()) + ")"
	if !visible(&view) {
		page.State = hiddenState(view.State)
		page.Hidden = hiddenCopy
		s.render(w, r, http.StatusOK, "board", &l)
		return
	}
	page.State = view.State
	if words, ok := stateWords[view.State]; ok {
		page.State = words
	}
	if l.Switch.On || view.NotStaffed == item.CauseKillSwitch {
		page.Banner = killSwitchCopy
	}
	s.fillBoard(page, &view, l.Standby != nil)
	s.render(w, r, http.StatusOK, "board", &l)
}

// hiddenState is the state an item of a hidden run shows: queued, open, or
// closed (spec 10.2).
func hiddenState(state string) string {
	switch item.Status(state) {
	case item.StatusQueued:
		return "Queued"
	case item.StatusClosed:
		return "Closed"
	}
	return "Open"
}

// fillBoard fills the page with the state's text, the stop, the conclusion
// and the hypotheses of the view.
func (s *site) fillBoard(page *boardPage, view *app.View, standby bool) {
	var a board.Assessment
	if n := len(view.Rounds); n > 0 {
		a = view.Rounds[n-1].Report.Assessment
	}
	for i := range a.Hypotheses {
		h := &a.Hypotheses[i]
		hv := hypView{
			Status: string(board.StatusOf(&a, i)), Key: h.Key.String(), Score: h.Score,
			Supporting: len(h.Supporters), Refuting: len(h.Refuters), Sources: len(h.Sources), Statement: h.Statement,
		}
		if h.ID == a.Conclusion && a.Conclusion != "" {
			c := hv
			page.Conclusion = &c
		}
		page.Hypotheses = append(page.Hypotheses, hv)
	}
	stopped := view.Stop != ""
	if stopped && page.Conclusion == nil && len(page.Hypotheses) > 0 {
		page.Hypotheses[0].NotConverged = true
	}
	page.Lines, page.Promote = s.stateLines(view, standby, len(page.Hypotheses) > 0)
	if stopped {
		page.Stop = "Run stopped: " + view.Stop
	}
	if e := view.StopEntry; e != nil {
		page.StopLine = plural(e.Rounds, "round") + ", " + plural(int(e.Tokens), "token") + ", " + e.WallClock.String() + " of wall clock"
	}
}

// stateLines are the lines of the view's state, spec 10.2's fixed texts, and,
// for a queued item, the path of its promote form.
func (s *site) stateLines(view *app.View, standby, hypotheses bool) (lines []string, promote string) {
	switch item.Status(view.State) {
	case item.StatusQueued:
		switch {
		case standby:
			lines = append(lines, "Queue position: not known on this instance")
		case view.QueuePosition > 0:
			lines = append(lines, "position "+strconv.Itoa(view.QueuePosition)+" in the queue")
		}
		lines = append(lines, "Promoting it starts its run now, if the limit allows; the promote form shows what it does.")
		promote = actPath(view.ID, app.ActPromote)
	case item.StatusInvestigating:
		if !hypotheses {
			lines = append(lines, readingCopy)
		}
	case item.StatusAwaitingClose:
		if view.NotStaffed != "" {
			lines = append(lines, notStaffedCopy, "Cause: "+string(view.NotStaffed))
		}
		if line := autoClose(s, view.AutoCloseUnread, view.AutoClosePaused, view.ClosesAtUnread, view.ClosesAt); line != "" {
			lines = append(lines, line)
		}
	case item.StatusClosed:
		if c := view.Closed; c != nil {
			by := "by itself"
			if !c.Auto && c.By != "" {
				by = "by " + c.By
			}
			lines = append(lines, "Closed at "+s.at(c.At)+" ("+s.ago(c.At)+") "+by)
			if s.now().Before(c.ReopenableUntil) {
				lines = append(lines, "may be reopened until "+s.at(c.ReopenableUntil))
			} else {
				lines = append(lines, "may no longer be reopened")
			}
		}
	}
	return lines, promote
}

// autoClose is the line of an item's auto-close: when it closes by itself,
// or why that is not known; empty for an item that does not await its
// close.
func autoClose(s *site, unread, paused, timeUnread bool, at time.Time) string {
	switch {
	case unread:
		return "auto-close not known: the kill switch could not be read"
	case paused:
		return "auto-close paused while the kill switch is on"
	case timeUnread:
		return "auto-close time not known"
	case !at.IsZero():
		return "closes by itself at " + s.at(at) + " (" + s.ago(at) + ")"
	}
	return ""
}

// boardPath is the path of an item's board.
func boardPath(id item.ID) string { return "/items/" + url.PathEscape(string(id)) }
