package pages

import (
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// words are the web's own words of each refusal's code (m4; R81), the
// coordinator's and the routing's. The admin API keeps its own texts, which
// carry the API's instructions; a person on a page reads these.
// standbyWords answer an act asked of a standby, which the web refuses before
// it reaches the coordinator: nothing was tried, so no outcome is in doubt,
// unlike a not_held answer that comes back after an act was sent.
const standbyWords = "This instance does not lead now, so it did not try this. Nothing was done here. Acts are taken on the leader."

// The raw output's words (Plan 9a-2's words of the board, "Raw output"): its
// refusal, whatever its cause, which says nothing of the cause; and a cited
// call whose stored output is past its retention.
const (
	noOutputWords = "There is no output here."
	notKeptWords  = "This call's stored output is no longer kept: outputs are kept 30 days."
)

var words = map[string]string{
	switching.CodeInvalid:          "The request is not one that can be acted on.",
	switching.CodeNoSuchItem:       "There is no such item.",
	switching.CodeKeyReused:        "This form was already used for another act. Open the act again for a new form.",
	switching.CodeStale:            "The kill switch changed since this page was loaded. The page now shows its state; decide again.",
	switching.CodeHeldByLaterBuild: "This item is held by a later build of the platform, which this instance cannot act on.",
	switching.CodeNotHeld:          "This instance does not lead now, so the outcome of an act just sent is not known here: check the items list to see whether it took effect. Acts are taken on the leader.",
	switching.CodeDraining:         "This instance is shutting down, so the outcome of an act just sent is not known here. In a minute, when the next leader has taken over, check the items list to see whether it took effect.",
	switching.CodeTakingOver:       "This instance is taking over as the leader. Please try again shortly.",
	switching.CodeUnavailable:      "This could not be done just now, and it may or may not have happened. Send the same form again: it is the same act, and is never done twice.",
	switching.CodeStopping:         "The run is stopping right now. Send the same form again in a moment.",

	app.CodeClosed:              "The item is closed. Reopen it first.",
	app.CodeReopenWindowPassed:  "The item closed too long ago to be reopened. Open a new item instead.",
	app.CodeLimitReached:        "As many items are investigating as the limit allows. Close or wait for one first.",
	app.CodeKillSwitchOn:        "The kill switch is on, so no item starts. Turn it off first.",
	app.CodeDailyCapReached:     "The day's spend has reached the daily cap, so no item starts today.",
	app.CodeAdminOnly:           "Only an admin may do this.",
	app.CodeReasonRequired:      "This needs a reason. Choose one.",
	app.CodeNotItemResponder:    "Only the item's principal, its responders, or an admin may act on this item.",
	app.CodeResponderOnly:       "Only a responder or an admin may do this.",
	app.CodeNotStarted:          "This item has no run yet, so there is nothing to extend. Promote it first.",
	app.CodeNoAgentsLeft:        "The run has no agent left to take part, so it cannot resume.",
	app.CodeWouldStopAtOnce:     "The run has used the budget this extension would give it, so it would stop at once. Extend it by more.",
	app.CodeSelf:                "An item cannot be linked to itself. Choose another item.",
	app.CodeRulingsWindowPassed: "The item closed too long ago to be linked or unlinked now.",
}

// Words are the web's words of a refusal's code, and whether it has any.
func Words(code string) (string, bool) {
	w, ok := words[code]
	return w, ok
}

// The board's fixed texts (spec 10.2; Plan 9a-2's words of the board): the
// hidden copy; 9a-1's reading copy, before a run's creation; row 7's; row
// 14's banner; row 1's promotion and its position not known on a standby;
// the investigating rows' word on the time they show; the stale indicator's
// words, all a hidden item's (R155); and a suggestion's superseded mark.
const (
	hiddenCopy     = "Agents are working on this alert for measurement. Their output stays hidden until it is rated. Handle it with your usual process."
	readingCopy    = "Agents are reading. A message comes when the run stops."
	notStaffedCopy = "Not staffed: handle with your usual process."
	killSwitchCopy = "Stopped by the kill switch"
	promotingCopy  = "Promoting it starts its run now, if the limit allows; the promote form shows what it does."
	positionCopy   = "Queue position: not known on this instance"
	laterCopy      = "An outage, a takeover or a resume moves that time later."
	staleWords     = "reconnecting"
	supersededCopy = "superseded: its hypothesis was rejected or lost convergence"
)

// stopWords are a run's stop reasons in words (spec Section 4.1).
var stopWords = map[string]string{
	string(item.StopConverged):       "converged",
	string(item.StopQuiet):           "a round passed with nothing new",
	string(item.StopBudgetExhausted): "the budget was used up",
	string(item.StopTriggerResolved): "the alert resolved",
	string(item.StopCut):             "the day's spend reached the daily cap",
	string(item.StopNotStaffed):      "no agent was left to invite",
	string(item.StopKilled):          "the kill switch stopped it",
	string(item.StopClosed):          "a person closed the item",
}

// causeWords are the causes of an item not staffed, in words.
var causeWords = map[string]string{
	string(item.CauseKillSwitch): "the kill switch was on",
	string(item.CauseDailyCap):   "the day's spend reached the daily cap",
	string(item.CauseRouting):    "its routing failed its check",
	string(item.CauseType):       "the configuration cannot run its type",
	string(item.CauseClosed):     "it was reopened with no run to return to",
}

// unavailableWords are why a suggestion's fact is unavailable, in words, by
// the code the gateway recorded (the words of the board, "Suggestion").
var unavailableWords = map[string]string{
	"no_time":    "no time was left to read it",
	"nothing":    "the read found nothing",
	"unreadable": "it could not be read",
	"not_plain":  "its value could not be shown whole",
}

// serviceWords are a suggestion's service marks in words (design D6): one
// inside the item's service says nothing of it.
var serviceWords = map[app.ServiceMark]string{
	app.ServiceOutside:    "outside this item's service",
	app.ServiceUncompared: "service not compared",
}

// word is code in words, or the code itself for one this build has no words
// for.
func word(m map[string]string, code string) string {
	if w, ok := m[code]; ok {
		return w
	}
	return code
}

// itemLine is the header's item (the words of the board, "Item"; R168): its
// id and type, names, and its title, quoted data, each left out when empty.
func itemLine(id item.ID, typ, title string) line {
	l := line(nil).say("Item ").name(string(id))
	if typ != "" {
		l = l.say(" · type ").name(typ)
	}
	if title != "" {
		l = l.say(". Title: ").quote(title)
	}
	return l
}

// stateLines are the rows of bv's state, a visible item's (spec 10.2, rows
// 1 to 9; the words of the board), with f, the facts of now, and, for a
// queued item, the path of its promote form, for an actor who may open an
// item on the leader (9a-1's rule; the coordinator's May decides the
// promotion itself): a queued item's position and what a promotion does; an
// investigating item's reading copy and the latest time its run stops; an
// item not staffed, and why; the run's stop (see stopLines); then an
// awaiting item's close (see autoCloseWords), or a closed item's (see
// closedLine).
func (s *site) stateLines(bv *app.BoardView, f *app.BoardFacts, actor app.Actor, standby bool) (lines []line, promote string) {
	v := &bv.View
	switch item.Status(v.State) {
	case item.StatusQueued:
		lines = queuedLines(f, standby)
		if _, err := app.May(actor, app.ActOpen, nil); err == nil && !standby {
			promote = actPath(v.ID, app.ActPromote)
		}
	case item.StatusInvestigating:
		lines = s.investigatingLines(bv)
	case item.StatusAwaitingClose:
		if v.NotStaffed != "" {
			lines = append(lines, line(nil).say(notStaffedCopy), line(nil).say("Cause: "+word(causeWords, string(v.NotStaffed))))
		}
	}
	lines = append(lines, stopLines(bv)...)
	switch item.Status(v.State) {
	case item.StatusAwaitingClose:
		if w := s.autoCloseWords(&bv.Header, f); w != "" {
			lines = append(lines, line(nil).say(w))
		}
	case item.StatusClosed:
		if c := v.Closed; c != nil {
			lines = append(lines, s.closedLine(c))
		}
	}
	return lines, promote
}

// queuedLines are a queued item's rows (row 1): its place in the queue, or
// that a standby does not know it; and what its promotion does.
func queuedLines(f *app.BoardFacts, standby bool) []line {
	var lines []line
	switch {
	case standby:
		lines = append(lines, line(nil).say(positionCopy))
	case f.QueuePosition > 0:
		lines = append(lines, line(nil).say("Queue position "+strconv.Itoa(f.QueuePosition)))
	}
	return append(lines, line(nil).say(promotingCopy))
}

// investigatingLines are an investigating item's rows (row 2, and the row
// with hypotheses): while its run runs, the latest time it stops, rounded up
// (R146); before its run's creation, 9a-1's reading copy; nothing once its
// run stopped, which its stop says.
func (s *site) investigatingLines(bv *app.BoardView) []line {
	h := &bv.Header
	hypotheses := len(bv.Hypotheses)+len(bv.RuledOut) > 0
	switch {
	case !h.StopBy.IsZero() && hypotheses:
		return []line{line(nil).say("The run stops by " + s.atLatest(h.StopBy) + " at the latest. " + laterCopy)}
	case !h.StopBy.IsZero():
		return []line{line(nil).say("Agents are reading. A message comes when the run stops, by " + s.atLatest(h.StopBy) + " at the latest. " + laterCopy)}
	case !hypotheses && bv.View.Stop == "":
		return []line{line(nil).say(readingCopy)}
	}
	return nil
}

// stopLines are the rows of a run that stopped (rows 4 to 6 and 8): the
// alert's resolution; the stop in words; the leading hypothesis, not
// converged, when no conclusion is (row 5); and the stop entry's line, 9a-1's
// stop line. None for an item with no run, or whose run goes on.
func stopLines(bv *app.BoardView) []line {
	v := &bv.View
	if bv.Version.Run == "" || v.Stop == "" {
		return nil
	}
	var lines []line
	if v.Stop == string(item.StopTriggerResolved) {
		lines = append(lines, line(nil).say("Alert resolved."))
	}
	lines = append(lines, line(nil).say("Run stopped: "+word(stopWords, v.Stop)+"."))
	if c := unconverged(bv); c != nil && bv.Header.Conclusion == nil {
		lines = append(lines, line(nil).name(c.Key.String()).say(" · not converged"))
	}
	if e := v.StopEntry; e != nil {
		lines = append(lines, line(nil).say(plural(e.Rounds, "round")+", "+plural(int(e.Tokens), "token")+", "+span(e.WallClock)+" of wall clock"))
	}
	return lines
}

// span is d in words, to the second.
func span(d time.Duration) string { return d.Round(time.Second).String() }

// autoCloseWords are an awaiting item's close (row 8), with f, the facts of
// now: that it stays open while its alert fires; that the kill switch could
// not be read, or pauses it; that its time could not be read; or when it
// closes by itself. Empty when none applies.
func (s *site) autoCloseWords(h *app.BoardHeader, f *app.BoardFacts) string {
	switch {
	case h.StaysOpen:
		return "Stays open while its alert fires"
	case f.AutoCloseUnread:
		return "auto-close not known: the kill switch could not be read"
	case f.AutoClosePaused:
		return "auto-close paused while the kill switch is on"
	case f.ClosesAtUnread:
		return "auto-close time not known"
	case !f.ClosesAt.IsZero():
		return "Closes by itself at " + s.at(f.ClosesAt) + " (" + s.ago(f.ClosesAt) + ")"
	}
	return ""
}

// closedLine is a closed item's close and its reopen window (row 9): when,
// by whom, its By a name, or by itself; and until when it may be reopened,
// or that it may no longer be (R171: 9a-1's closedLines, as parts).
func (s *site) closedLine(c *app.ClosedView) line {
	l := line(nil).say("Closed at " + s.at(c.At) + " (" + s.ago(c.At) + ") by ")
	if c.Auto || c.By == "" {
		l = l.say("itself")
	} else {
		l = l.name(c.By)
	}
	if s.now().Before(c.ReopenableUntil) {
		return l.say("; may be reopened until " + s.at(c.ReopenableUntil) + ".")
	}
	return l.say("; may no longer be reopened.")
}

// budgetWords are the budget a run used, of its budget with every extension
// (the words of the board, "Budget"); empty with none.
func budgetWords(b *app.BudgetView) string {
	if b == nil {
		return ""
	}
	return "Used " + strconv.Itoa(b.Used.Rounds) + " of " + strconv.Itoa(b.Budget.Rounds) + " rounds, " +
		span(b.Used.WallClock) + " of " + span(b.Budget.WallClock) + " wall clock, " +
		strconv.FormatInt(b.Used.Tokens, 10) + " of " + strconv.FormatInt(b.Budget.Tokens, 10) + " tokens"
}

// suggestionsWords are the suggestions counted (spec 10.2), every one.
func suggestionsWords(n int) string {
	if n == 0 {
		return "No suggestion yet"
	}
	return plural(n, "suggestion")
}

// alertOf is the header's alert (the words of the board, "Alert"): how many
// instances and how many fire, then each instance's line, its id quoted
// data, its source a name, and since when it fires or when it resolved; none
// for an item with no alert.
func (s *site) alertOf(instances []item.TriggerInstance) (alert string, lines []line) {
	if len(instances) == 0 {
		return "", nil
	}
	firing := 0
	for i := range instances {
		in := &instances[i]
		l := line(nil).value(in.ID).say(" from ").name(in.Source)
		if in.Status == item.Firing {
			firing++
			l = l.say(": firing since " + s.at(in.At))
		} else {
			l = l.say(": resolved at " + s.at(in.At))
		}
		lines = append(lines, l)
	}
	return "Alert: " + plural(len(instances), "instance") + ", " + strconv.Itoa(firing) + " firing", lines
}

// flapLine is a flap item's origin (the words of the board, "Flap origin"):
// the closed item whose alert fired again, linked to its board.
func flapLine(of item.ID) line {
	return line(nil).say("This item opened when the alert of item ").nameLink(string(of), boardPath(of)).say(" fired again after that item closed.")
}

// stripLine is one of the incident's other items in the strip (the words of
// the board, "Strip"): its id, linked to its board; then that it could not
// be read; or, hidden, its state alone, Queued, Open or Closed (D13); or its
// state in words, its stopped run's conclusion's key and whether it
// converged, and whether it disagrees (R148).
func stripLine(it *app.StripItem) line {
	l := line(nil).nameLink(string(it.ID), boardPath(it.ID))
	switch {
	case it.Unreadable:
		return l.say(" · this item could not be read")
	case it.Hidden:
		return l.say(" · " + hiddenState(string(it.State)))
	}
	l = l.say(" · " + word(stateWords, string(it.State)))
	if it.Conclusion != nil {
		l = l.say(" · ").name(it.Conclusion.String())
		if it.Converged {
			l = l.say(" · converged")
		} else {
			l = l.say(" · not converged")
		}
	}
	if it.Disagrees {
		l = l.say(" · disagrees")
	}
	return l
}

// cardOf is the card of c, a hypothesis of item id's board (the words of the
// board, "Hypothesis", "Quotes", "Suggestion"): its status word; its key, a
// name, score, counts and sources, and not converged when it is marked, the
// hypothesis of id marked; its statement, model-written; its quotes on each
// side, the projection's pick, and how many more; and its suggestions by
// target. member says that the viewer reads the raw outputs its quotes cite.
func cardOf(id item.ID, c *app.HypothesisCard, member bool, marked board.EntryID) cardView {
	cv := cardView{
		Status: string(c.Status),
		Line: line(nil).say(" · ").name(c.Key.String()).say(" · score " + strconv.Itoa(c.Score) + " · " + strconv.Itoa(len(c.Supporters)) +
			" supporting · " + strconv.Itoa(len(c.Refuters)) + " refuting · " + plural(len(c.Sources), "source")),
		Statement: line(nil).model(c.Statement),
	}
	if c.ID == marked {
		cv.Line = cv.Line.say(" · not converged")
	}
	for i := range c.Quotes.Supports {
		cv.Supports = append(cv.Supports, quoteLine(id, &c.Quotes.Supports[i], member))
	}
	for i := range c.Quotes.Refutes {
		cv.Refutes = append(cv.Refutes, quoteLine(id, &c.Quotes.Refutes[i], member))
	}
	cv.MoreSupports, cv.MoreRefutes = moreWords(c.Quotes.MoreSupports), moreWords(c.Quotes.MoreRefutes)
	for i := range c.Suggestions {
		cv.Groups = append(cv.Groups, groupOf(c, &c.Suggestions[i]))
	}
	return cv
}

// moreWords are how many more there are: "and <n> more", empty for none.
func moreWords(n int) string {
	if n == 0 {
		return ""
	}
	return "and " + strconv.Itoa(n) + " more"
}

// quoteLine is a quote of item id's board (design D4.4; the words of the
// board, "Quotes"): a person's evidence, their name and their words; an
// agent's, its quote, quoted data, from its source, and, for a member, its
// raw output's link (D10); and, which no quote is (R145), the platform's,
// by its ref, never its claim (D8).
func quoteLine(id item.ID, e *board.Event, member bool) line {
	ev := e.Body.Evidence
	if ev == nil {
		return nil
	}
	p := &ev.Provenance
	switch e.Author.Kind {
	case board.AuthorHuman:
		return line(nil).name(e.Author.Name).say(" wrote, ").person(ev.Claim)
	case board.AuthorAgent:
		l := line(nil).quote(p.Quote).say(", from ").name(string(p.Source))
		if member && p.CallID != "" {
			l = l.say(" (").link("raw output", callPath(id, p.CallID)).say(")")
		}
		return l
	}
	l := line(nil).say("The platform's evidence")
	if p.Ref != "" {
		l = l.say(" (").name(p.Ref).say(")")
	}
	return l
}

// groupOf is c's suggestions of one target, g (design D6): the target's
// arguments, names, and their values, quoted data, then each suggestion.
func groupOf(c *app.HypothesisCard, g *app.SuggestionGroup) groupView {
	gv := groupView{Target: line(nil).say("Target: ")}
	for i := range g.Target {
		if i > 0 {
			gv.Target = gv.Target.say(", ")
		}
		gv.Target = gv.Target.name(g.Target[i].Arg).say(" ").value(g.Target[i].Value)
	}
	for i := range g.Suggestions {
		gv.Suggestions = append(gv.Suggestions, suggestionOf(c, &g.Suggestions[i]))
	}
	return gv
}

// suggestionOf is c's suggestion sg (design D6; the words of the board,
// "Suggestion"): its summary, the policy's text parts as the platform's
// words and its value parts as quoted data; its facts; c's key and its
// status now; its proposer and the proposer's own evidence; and its
// superseded and service marks.
func suggestionOf(c *app.HypothesisCard, sg *app.Suggestion) suggestionView {
	p := sg.Event.Body.Proposal
	if p == nil {
		return suggestionView{}
	}
	var v suggestionView
	for _, sp := range p.Summary {
		if sp.Value {
			v.Summary = v.Summary.value(sp.Text)
		} else {
			v.Summary = v.Summary.say(sp.Text)
		}
	}
	v.Facts = factsLine(p.Facts)
	v.Status = line(nil).name(c.Key.String()).say(" is " + string(sg.Status))
	v.Proposed = proposedLine(p.Proposer, sg.Support)
	if sg.Superseded {
		v.Superseded = supersededCopy
	}
	v.Service = serviceWords[sg.Service]
	return v
}

// factsLine is a suggestion's facts, by name: each name's values, quoted
// data, and how many more; or why it is unavailable, in words, or as its
// code. None for a suggestion with no fact.
func factsLine(facts map[string]board.Fact) line {
	if len(facts) == 0 {
		return nil
	}
	l := line(nil).say("Facts: ")
	for i, n := range slices.Sorted(maps.Keys(facts)) {
		f := facts[n]
		if i > 0 {
			l = l.say("; ")
		}
		l = l.name(n).say(": ")
		if f.Unavailable != "" {
			l = l.say("unavailable: " + word(unavailableWords, f.Unavailable))
			continue
		}
		for j, value := range f.Values {
			if j > 0 {
				l = l.say(", ")
			}
			l = l.value(value)
		}
		if f.More > 0 {
			l = l.say(" " + moreWords(f.More))
		}
	}
	return l
}

// proposedLine is a suggestion's proposer, a name, and how its support was
// obtained: its own counted evidence on the hypothesis, by source, each
// source a name with its count; the proposer alone when it has none.
func proposedLine(proposer string, support map[board.Source]int) line {
	l := line(nil).say("Proposed by ").name(proposer)
	if len(support) == 0 {
		return l
	}
	l = l.say(", which supports it with its own evidence from ")
	for i, src := range slices.Sorted(maps.Keys(support)) {
		if i > 0 {
			l = l.say(", ")
		}
		l = l.name(string(src)).say(" (" + strconv.Itoa(support[src]) + ")")
	}
	return l
}
