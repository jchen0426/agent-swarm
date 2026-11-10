package pages

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// visible says whether an item's board may be shown (design D9). Plan 8a
// replaces it with the item's arm; until then every board is visible. Every
// route that shows a board asks it before it shows anything of the board.
var visible = func(*app.View) bool { return true }

// boardPage is /items/{id} (Plan 9a-2, design D1, D3): the board's regions,
// each its own model, the layout holding the first two; the ETag over their
// versions, and, for a visible item, the order of what it shows, its latest
// record's seq and its run's latest entry's (R156); the path of the live
// bar's hidden "Sign in again" link, back to the board as it is shown, its
// ?feed=all kept (R155; Task 6's review, R1-S); and the path of its script,
// live.js. A read that failed is its refusal's words alone.
type boardPage struct {
	Refusal     string
	ETag, Order string
	SignIn      string
	Script      string
	Header      headerRegion
	Summary     summaryRegion
	Strip       stripRegion
	Hypotheses  hypothesesRegion
	Feed        feedRegion
}

// board serves an item's board, GET /items/{id} (design D1, D3; R162): its
// regions, as shown reads them, in the page, which loads live.js. The page
// works without the script, and says so in its live bar.
func (s *site) board(w http.ResponseWriter, r *http.Request) {
	l, page, ok := s.shown(w, r)
	if !ok {
		return
	}
	l.Body = page
	s.render(w, r, http.StatusOK, "board", l)
}

// shown reads item {id}'s board for r, the board page's and its polls'
// alike (design D1, D3; R162): the board watch's read, within boardWait
// (R141), framed by its facts, a failed read's included, which carry its
// tick's kill switch when it had one in hand (Task 5's review, decision 6);
// and the board's regions, each with the version of its content, never
// before visible, asked once, has said that the board may be shown (D13),
// the feed whole when r asks for it (see wholeFeed). A read that failed is
// answered here, as the board page answers it, and so is a region that
// could not be rendered, each with false.
func (s *site) shown(w http.ResponseWriter, r *http.Request) (*layout, *boardPage, bool) {
	ctx := r.Context()
	sg, actor, _ := s.a.Acting(r)
	id := item.ID(r.PathValue("id"))
	reading, cancel := context.WithTimeout(ctx, boardWait)
	defer cancel()
	read, err := s.p.Boards.Board(reading, id)
	l := s.boardFrame(ctx, &sg, "Item", &read.Facts)
	if err != nil {
		status, refusal := s.boardFailed(ctx, id, err)
		l.Body = &boardPage{Refusal: refusal}
		s.render(w, r, status, "board", &l)
		return nil, nil, false
	}
	page := s.boardOf(&read, actor, l.Standby != nil, wholeFeed(r))
	if err := versioned(templates["board"], &l, page); err != nil {
		s.unrendered(w, r, "board", err)
		return nil, nil, false
	}
	return &l, page, true
}

// wholeFeed says whether r asks for every line of the feed, ?feed=all, which
// the board page and its polls read alike, so that a page of the whole feed
// keeps it whole through its polls; any other value of feed is ignored
// (R167).
func wholeFeed(r *http.Request) bool { return r.URL.Query().Get("feed") == feedAll }

// The words of a board read that failed: records or a board that are
// invalid, a lasting condition, with no advice to retry (9a-1's); and any
// other failure, a store's or a read past its wait, which a reload may mend
// (R141). unavailableCopy is 9a-1's words of a read that failed, which a
// cited call's read keeps (Task 4).
const (
	unreadableCopy       = "This item's records cannot be read."
	unavailableCopy      = "The item could not be read just now. Reload the page to try again."
	boardUnavailableCopy = "The board could not be read just now. Reload the page to try again."
)

// failureLogEvery is how often at most a board read's failure of one item is
// logged (R159, R176).
const failureLogEvery = time.Minute

// boardFailed is how a route that shows a board answers err, a failed read
// of item id's board, and logs it, the board page and the raw outputs alike:
// an item with no record, 404, in words; records or a board that are
// invalid, a lasting condition, 500, logged once for the item; any other
// failure, a store's or a read's whose wait ran out, 503, logged at Error at
// most once a minute an item (R176), so that a database's fault does not log
// a line a poll for each viewer. A log line names the item and the failure's
// class, never the error's text, which on a corrupt row can quote a stored
// value (app.FailureClass).
func (s *site) boardFailed(ctx context.Context, id item.ID, err error) (status int, refusal string) {
	if errors.Is(err, app.ErrNoItem) {
		return http.StatusNotFound, words[switching.CodeNoSuchItem]
	}
	class := app.FailureClass(err)
	if class == app.FailureCorrupt {
		if _, again := s.warned.LoadOrStore(id, struct{}{}); !again {
			s.log.WarnContext(ctx, "an item's records are invalid: its board cannot be shown", "item", id, "class", class)
		}
		return http.StatusInternalServerError, unreadableCopy
	}
	if s.failureDue(id) {
		s.log.ErrorContext(ctx, "the web interface could not read an item", "item", id, "class", class)
	}
	return http.StatusServiceUnavailable, boardUnavailableCopy
}

// failureDue reports whether a board read's failure of item id is to be
// logged now, and if so notes it logged: the first always, then once in
// failureLogEvery at most, on the monotonic clock. The notes older than that
// are forgotten, so that the map holds the items of the last minute alone.
func (s *site) failureDue(id item.ID) bool {
	now := s.mono()
	s.mu.Lock()
	defer s.mu.Unlock()
	if at, ok := s.failed[id]; ok && now.Sub(at) < failureLogEvery && !now.Before(at) {
		return false
	}
	maps.DeleteFunc(s.failed, func(_ item.ID, at time.Time) bool { return now.Sub(at) >= failureLogEvery })
	s.failed[id] = now
	return true
}

// boardOf is the board page of read for actor, standby saying that this
// instance does not lead (R144, R170, D13): an item a later build holds, its
// refusal's words alone; an item visible, asked once, says may not be shown,
// its hidden copy and state word alone, with no time and no order; any other,
// every region's content, its feed's every line when all, its newest
// otherwise (R167), and the order of what it shows. Every region exists in
// each, empty when it has nothing. read.Board is the watch's, shared: it is
// only read.
func (s *site) boardOf(read *app.BoardRead, actor app.Actor, standby, all bool) *boardPage {
	bv := read.Board
	back := boardPath(bv.View.ID)
	if all {
		back += "?feed=" + feedAll
	}
	page := &boardPage{SignIn: auth.SignInPath + "?return=" + url.QueryEscape(back), Script: livePath}
	switch {
	case bv.View.HeldByLaterBuild:
		page.Header = headerRegion{Stale: staleWords, Refusal: words[switching.CodeHeldByLaterBuild]}
	case !visible(&bv.View):
		page.Header = headerRegion{Stale: staleWords, State: line(nil).say(hiddenState(bv.View.State)), Hidden: hiddenCopy}
	default:
		m := member(actor, &bv.Opened)
		page.Order = strconv.FormatInt(bv.Version.Record.Seq, 10) + "." + strconv.FormatInt(bv.Version.Board.Seq, 10)
		page.Header = s.headerOf(bv, &read.Facts, actor, standby)
		page.Summary = summaryOf(bv)
		page.Strip = stripOf(&read.Strip)
		page.Hypotheses = hypothesesOf(bv, m)
		page.Feed = s.feedOf(bv, m, all)
	}
	return page
}

// headerOf is the header of bv's board, a visible item's, with f, the facts
// of now (the words of the board): the kill switch's banner while the
// switch is on, as the tick read it, or the item was left not staffed by it
// (row 14); the item, its type and its title; its state and the state's rows
// (see stateLines); the conclusion; the budget used, for an item with a run,
// and its suggestions counted; its alert; the item a flap's opening
// followed; this round's notices; and when it was last updated, which the
// stale indicator names too.
func (s *site) headerOf(bv *app.BoardView, f *app.BoardFacts, actor app.Actor, standby bool) headerRegion {
	v, h := &bv.View, &bv.Header
	r := headerRegion{
		Stale: staleWords + ", last updated at " + s.at(h.Updated),
		Item:  itemLine(v.ID, v.Type, h.Title),
		State: line(nil).code(stateWords, v.State),
	}
	if (f.KillSwitchRead && f.KillSwitch.On) || v.NotStaffed == item.CauseKillSwitch {
		r.Banner = killSwitchCopy
	}
	r.Lines, r.Promote = s.stateLines(bv, f, actor, standby)
	r.Conclusion = s.conclusionOf(bv)
	if bv.Version.Run != "" {
		r.Budget, r.Suggestions = budgetWords(v.Budget), suggestionsWords(h.Suggestions)
	}
	r.Alert, r.Instances = s.alertOf(h.Alert)
	if h.FlapOf != "" {
		r.Flap = flapLine(h.FlapOf)
	}
	if h.Notices > 0 {
		r.Notices = plural(h.Notices, "notice") + " this round"
	}
	r.Updated = "Last updated at " + s.at(h.Updated) + " (" + s.ago(h.Updated) + ")"
	return r
}

// unconverged is the hypothesis a run that stopped without converging marks
// not converged (spec 10.2, row 5): the conclusion, which the assessment
// picks even then, or else the leading hypothesis; nil for a run that
// converged, that goes on, or that leads with none, and for an item with no
// run.
func unconverged(bv *app.BoardView) *app.HypothesisCard {
	if stop := bv.View.Stop; bv.Version.Run == "" || stop == "" || stop == string(item.StopConverged) {
		return nil
	}
	if c := bv.Header.Conclusion; c != nil {
		return c
	}
	for i := range bv.Hypotheses {
		if bv.Hypotheses[i].Status == board.StatusLeading {
			return &bv.Hypotheses[i]
		}
	}
	return nil
}

// conclusionOf is the header's conclusion (rows 3 to 5): the assessment's
// conclusion's key and standing (see standingOf), and its statement,
// model-written; nil with no conclusion.
func (s *site) conclusionOf(bv *app.BoardView) *conclusionView {
	c := bv.Header.Conclusion
	if c == nil {
		return nil
	}
	return &conclusionView{Line: line(nil).name(c.Key.String()).say(s.standingOf(bv, c)), Statement: line(nil).model(c.Statement)}
}

// standingOf is the words after the conclusion c's key: confirming until
// the time its wrap-up round's decision lands, rounded up (row 3; R146); not
// converged, when the run's stop marks it (row 5); else its status word, as
// converged (row 4).
func (s *site) standingOf(bv *app.BoardView, c *app.HypothesisCard) string {
	switch {
	case c.Status == board.StatusConfirming && !bv.Header.ConfirmingUntil.IsZero():
		return " · confirming until " + s.atLatest(bv.Header.ConfirmingUntil)
	case c.ID == unconvergedID(bv):
		return " · not converged"
	}
	return " · " + string(c.Status)
}

// unconvergedID is the id of the hypothesis unconverged gives; "" for none.
func unconvergedID(bv *app.BoardView) board.EntryID {
	if c := unconverged(bv); c != nil {
		return c.ID
	}
	return ""
}

// summaryOf is the round summary of bv's board (R177): the latest ended
// round and the current assessment's rank-0 hypothesis with its status word
// now; or, the run stopped, why; empty with no run, and before any round
// ended. Its data-round is "<n>.<k>", n the latest ended round, 0 for none,
// and k the run's stops so far, View.Stops, which the records' fold gives
// with the stop itself, so that it changes once at each round's end and once
// at each stop, in the very version that sets the stop's sentence, however
// late the board's stop entry lands, and not at a resume, after which the
// summary of the round before returns with the same value (Task 5's review,
// decision 10; re-review N1).
func summaryOf(bv *app.BoardView) summaryRegion {
	h := &bv.Header
	round := strconv.Itoa(h.Ended) + "." + strconv.Itoa(bv.View.Stops)
	switch {
	case bv.Version.Run == "":
		return summaryRegion{}
	case bv.View.Stop != "":
		return summaryRegion{Round: round, Line: line(nil).say("The run stopped: ").code(stopWords, bv.View.Stop)}
	case h.Ended == 0:
		return summaryRegion{}
	}
	ended := "Round " + strconv.Itoa(h.Ended) + " ended: "
	top := rankZero(bv)
	if top == nil {
		return summaryRegion{Round: round, Line: line(nil).say(ended + "no hypothesis yet")}
	}
	return summaryRegion{Round: round, Line: line(nil).say(ended + string(top.Status) + ", ").name(top.Key.String())}
}

// rankZero is the card of the current assessment's rank-0 hypothesis
// (R177), ruled out or not: the first of the assessment's ranking, which
// every round's report of the view holds whole (board.State.Report), the
// latest one's read; nil when it ranks none. A round ended, so the view has
// its rounds.
func rankZero(bv *app.BoardView) *app.HypothesisCard {
	n := len(bv.View.Rounds)
	if n == 0 {
		return nil
	}
	ranked := bv.View.Rounds[n-1].Report.Assessment.Hypotheses
	if len(ranked) == 0 {
		return nil
	}
	for _, cards := range [][]app.HypothesisCard{bv.Hypotheses, bv.RuledOut} {
		for i := range cards {
			if cards[i].ID == ranked[0].ID {
				return &cards[i]
			}
		}
	}
	return nil
}

// stripOf is the strip's region of st (design D7): that the incident's other
// items could not be listed; or each item's line (see stripLine), and how
// many more there are; empty for an incident of the item alone.
func stripOf(st *app.Strip) stripRegion {
	if st.Unread {
		return stripRegion{Unread: true}
	}
	var r stripRegion
	for i := range st.Items {
		r.Items = append(r.Items, stripLine(&st.Items[i]))
	}
	if st.More > 0 {
		r.More = "and " + strconv.Itoa(st.More) + " more"
	}
	return r
}

// hypothesesOf is the hypotheses' region of bv's board: the ranked ones'
// cards, then those ruled out, each by cardOf; member says that the viewer
// reads the raw outputs, which its quotes then link. Each card's id is
// h-<its entry's seq>, from the feed, which holds every hypothesis's entry,
// so that a reader's card on screen anchors the page across the region's
// replacement (Task 8's re-review NB3); none for an entry it does not hold.
func hypothesesOf(bv *app.BoardView, member bool) hypothesesRegion {
	marked := unconvergedID(bv)
	seqs := map[board.EntryID]int64{}
	for i := range bv.Feed {
		if e := bv.Feed[i].Event; e != nil {
			seqs[e.ID] = e.Seq
		}
	}
	card := func(c *app.HypothesisCard) cardView {
		cv := cardOf(bv.View.ID, c, member, marked)
		if seq, ok := seqs[c.ID]; ok {
			cv.ID = "h-" + strconv.FormatInt(seq, 10)
		}
		return cv
	}
	var r hypothesesRegion
	for i := range bv.Hypotheses {
		r.Ranked = append(r.Ranked, card(&bv.Hypotheses[i]))
	}
	for i := range bv.RuledOut {
		r.RuledOut = append(r.RuledOut, card(&bv.RuledOut[i]))
	}
	return r
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

// boardPath is the path of an item's board.
func boardPath(id item.ID) string { return "/items/" + url.PathEscape(string(id)) }
