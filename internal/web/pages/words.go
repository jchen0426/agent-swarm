package pages

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/admin/switching"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/key"
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
			lines = append(lines, line(nil).say(notStaffedCopy), line(nil).say("Cause: ").code(causeWords, string(v.NotStaffed)))
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
	lines = append(lines, line(nil).say("Run stopped: ").code(stopWords, v.Stop).say("."))
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
	l = l.say(" · ").code(stateWords, string(it.State))
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
	v.Summary = summarized(nil, p.Summary)
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
			l = l.say("unavailable: ").code(unavailableWords, f.Unavailable)
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

// summarized is w, then a proposal's summary (design D6): the tool policy's
// text parts, the operator's words, each isolated and inert, unquoted, and
// the values filled in as quoted data, never as the platform's words. A
// text part reaches the page from the board's entry, stored as the gateway
// copied it, so it is a stored value too (Task 6's review, R2-I2, which
// replaces Task 5's decision 18).
func summarized(w line, parts []board.SummaryPart) line {
	for _, sp := range parts {
		if sp.Value {
			w = w.value(sp.Text)
		} else {
			w = w.policy(sp.Text)
		}
	}
	return w
}

// The feed's fixed texts (Plan 9a-2's words of the board, "The item's
// records in the feed"): a trigger event's title the inbound store no
// longer keeps, past its retention; the subject of the admin API's acts,
// by which an opening through it reads so (the admin API's actor,
// internal/admin/lifecycle.go, which the web does not import); and the
// cause of a person's extension's resume entry (board's resume causes).
const (
	titleNotKept    = "no longer kept (alert titles are kept 30 days)"
	adminSubject    = "admin"
	resumeExtension = "extension"
)

// declinedWords are why a firing resume was declined, in words, by R112's
// seven codes, the coordinator's (app.mayFiringResume): its refusals' codes
// and its own two, no_run and unsettled.
var declinedWords = map[string]string{
	app.CodeKillSwitchOn:    "the kill switch is on",
	"unsettled":             "the kill switch's state was not known",
	app.CodeDailyCapReached: "the day's spend has reached the daily cap",
	app.CodeNoAgentsLeft:    "no agent is left to take part",
	app.CodeWouldStopAtOnce: "the run has no budget left",
	app.CodeLimitReached:    "as many items are investigating as the limit allows",
	"no_run":                "the item has no run to resume",
}

// endedWords are how a round ended, in words, by item.EndedBy.
var endedWords = map[string]string{
	string(item.EndedByTurns):    "every agent ended its turn",
	string(item.EndedByDeadline): "its time limit passed",
	string(item.EndedByOutage):   "no instance held the lease at its deadline",
	string(item.EndedByStopped):  "the run stopped during it",
}

// unstartedWords are why a turn could not start, in words, by the reason a
// turn_unstarted notice gives (board's unstartedReasons).
var unstartedWords = map[string]string{
	"engine": "the runner's container engine did not answer",
	"image":  "its runtime image could not be used",
	"launch": "its sandbox could not be launched",
}

// triggerStatusWords are a trigger event's status, in words.
var triggerStatusWords = map[string]string{
	string(item.Firing):   "firing",
	string(item.Resolved): "resolved",
}

// reasonWords are the labels of the preset reasons the act forms offer
// (actWords), by their codes: a reason of another code shows as its code.
// switchReasonWords are the kill switch's on form's (switchReasons): its
// other reason shows as its code.
var (
	reasonWords       = presetLabels()
	switchReasonWords = labelsOf(switchReasons)
)

// presetLabels are the labels of actWords' preset reasons, by code.
func presetLabels() map[string]string {
	out := map[string]string{}
	for _, w := range actWords {
		maps.Copy(out, labelsOf(w.reasons))
	}
	return out
}

// labelsOf are reasons' labels, by code.
func labelsOf(reasons []reasonButton) map[string]string {
	out := make(map[string]string, len(reasons))
	for _, r := range reasons {
		out[r.Code] = r.Label
	}
	return out
}

// reasoned is w, then the reason r of an item's act or entry, by the act
// forms' labels (see reasonedOf).
func reasoned(w line, r *board.Reason) line { return reasonedOf(w, r, reasonWords) }

// reasonedOf is w, then the reason r (the words of the board): ", reason",
// its label in labels or its code as itself (see line.code), then, for one
// with text, ": " and the person's words; w alone when there is none.
func reasonedOf(w line, r *board.Reason, labels map[string]string) line {
	if r == nil || r.Code == "" && r.Text == "" {
		return w
	}
	w = w.say(", reason")
	if r.Code != "" {
		w = w.say(" ").code(labels, r.Code)
	}
	if r.Text != "" {
		w = w.say(": ").person(r.Text)
	}
	return w
}

// amountWords are an extension's amount (the words of the board):
// +<n> rounds, +<d> of wall clock, +<t> tokens, each only when not zero.
func amountWords(b item.Budget) string {
	var parts []string
	if b.Rounds != 0 {
		parts = append(parts, "+"+plural(b.Rounds, "round"))
	}
	if b.WallClock != 0 {
		parts = append(parts, "+"+span(b.WallClock)+" of wall clock")
	}
	if b.Tokens != 0 {
		parts = append(parts, "+"+plural(int(b.Tokens), "token"))
	}
	return strings.Join(parts, ", ")
}

// stillFiring is how many of an item's instances fire once a resolution
// applied: none, or how many still do.
func stillFiring(n int) string {
	switch n {
	case 0:
		return "no instance fires now"
	case 1:
		return "1 instance still fires"
	}
	return strconv.Itoa(n) + " instances still fire"
}

// entriesCited is how many evidence entries an entry cites.
func entriesCited(n int) string {
	if n == 1 {
		return "1 evidence entry"
	}
	return strconv.Itoa(n) + " evidence entries"
}

// firstRunes is the first n runes of s, s itself when it holds fewer.
func firstRunes(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

// keyed is w, then a hypothesis's key, k, a name in its canonical form; or,
// when the projection gives none, the hypothesis's id, a name.
func keyed(w line, k key.Key, id board.EntryID) line {
	if len(k) > 0 {
		return w.name(k.String())
	}
	return w.name(string(id))
}

// stanced is w, then ev's stance on its hypothesis, by the hypothesis's key
// k (see keyed): " (supporting <key>)" or " (refuting <key>)"; w alone for
// evidence of no stance.
func stanced(w line, ev *board.Evidence, k key.Key) line {
	switch ev.Stance {
	case board.Supports:
		return keyed(w.say(" (supporting "), k, ev.Hypothesis).say(")")
	case board.Refutes:
		return keyed(w.say(" (refuting "), k, ev.Hypothesis).say(")")
	}
	return w
}

// openedLine is w, then an opening's words, from its item_opened record
// (the words of the board; design D8): by the source <s>, through the admin
// API, by <By>, or by no one an earlier build recorded; its title, quoted
// data; and its routing, each member its key, a name, " = ", and its value,
// quoted data, whole (R174), by key.
func openedLine(w line, o *item.Opened) line {
	source, bySource := strings.CutPrefix(o.By, app.SourceSubjectPrefix)
	switch {
	case bySource:
		w = w.say("Opened by the source ").name(source)
	case o.By == adminSubject:
		w = w.say("Opened through the admin API")
	case o.By != "":
		w = w.say("Opened by ").name(o.By)
	default:
		w = w.say("Opened")
	}
	if o.Title != "" {
		w = w.say(". Title: ").quote(o.Title)
	}
	for i, k := range slices.Sorted(maps.Keys(o.Routing)) {
		if i == 0 {
			w = w.say(". Routing: ")
		} else {
			w = w.say(", ")
		}
		w = w.name(k).say(" = ").value(o.Routing[k])
	}
	return w
}

// runCreatedLine is a run's creation (the words of the board): the agents
// it invited, each a name; its budget; and a round's time limit.
func runCreatedLine(rc *item.RunCreated) line {
	w := line(nil).say("Run created: " + plural(len(rc.Agents), "agent") + " invited")
	for i, a := range rc.Agents {
		if i == 0 {
			w = w.say(" (")
		} else {
			w = w.say(", ")
		}
		w = w.name(a)
	}
	if len(rc.Agents) > 0 {
		w = w.say(")")
	}
	b := &rc.Budget
	return w.say("; budget " + plural(b.Rounds, "round") + ", " + span(b.WallClock) + " of wall clock, " + plural(int(b.Tokens), "token") +
		"; a round's time limit " + span(rc.RoundLimit))
}

// closedRecordLine is an item's close: by itself, or by its By, a name,
// with its reason.
func closedRecordLine(c *item.Closed) line {
	if c.Auto || c.By == "" {
		return line(nil).say("Closed by itself")
	}
	return reasoned(line(nil).say("Closed by ").name(c.By), c.Reason)
}

// extendedLine is a person's extension: by its By, a name, its amount, and
// its reason. A firing resume's run_extended, the one other cause the fold
// takes, makes no line of its own (R149).
func extendedLine(e *item.Extended) line {
	w := line(nil).say("Extended by ").name(e.By)
	if a := amountWords(e.Amount); a != "" {
		w = w.say(": " + a)
	}
	return reasoned(w, e.Reason)
}

// linkedLine is a link: a flap's, by its source, which joined the incident
// of the closed item whose alert fired again; or a person's, by its By, with
// its reason; the other item linked to its board.
func linkedLine(k *item.Linked) line {
	if source, ok := strings.CutPrefix(k.By, app.SourceSubjectPrefix); ok {
		return line(nil).say("Joined the incident of item ").nameLink(string(k.With), boardPath(k.With)).
			say(", whose alert fired again after it closed (linked by the source ").name(source).say(")")
	}
	return reasoned(line(nil).say("Linked by ").name(k.By).say(" with item ").nameLink(string(k.With), boardPath(k.With)), k.Reason)
}

// titled is w, then the title of l's trigger event, from the inbound store
// (design D8): quoted data; or, past the store's retention, that it is no
// longer kept. A title the store keeps empty is left out.
func titled(w line, l *app.FeedLine) line {
	switch {
	case !l.Kept:
		return w.say(". Title: " + titleNotKept)
	case l.Title != "":
		return w.say(". Title: ").quote(l.Title)
	}
	return w
}

// taskLine is a task: its author, a name, its tags, each a name, and its
// title, model-written.
func taskLine(e *board.Event) line {
	t := e.Body.Task
	w := line(nil).name(e.Author.Name).say(" posted a task")
	for i, tag := range t.Tags {
		if i == 0 {
			w = w.say(" (tags ")
		} else {
			w = w.say(", ")
		}
		w = w.name(tag)
	}
	if len(t.Tags) > 0 {
		w = w.say(")")
	}
	return w.say(". ").model(t.Title)
}

// proposalLine is l's proposal, by its summary (design D6, D8): for its
// proposer, a name, under its hypothesis's key, then the summary's parts.
func proposalLine(l *app.FeedLine) line {
	p := l.Event.Body.Proposal
	w := line(nil).say("A suggestion was recorded for ").name(p.Proposer)
	if len(l.Key) > 0 {
		w = w.say(", under ").name(l.Key.String())
	}
	return summarized(w.say(": "), p.Summary)
}

// stopLine is l's stop entry: the stop in words, a reason this build has no
// words for as itself; what the run used; and its conclusion's key.
func stopLine(l *app.FeedLine) line {
	st := l.Event.Body.Stop
	w := line(nil).say("The run stopped: ").code(stopWords, st.Reason).say(". It used " + plural(st.Rounds, "round") + ", " +
		plural(int(st.Tokens), "token") + " and " + span(st.WallClock) + " of wall clock.")
	if st.Conclusion != "" {
		w = keyed(w.say(" (Conclusion: "), l.Key, st.Conclusion).say(")")
	}
	return w
}

// resumeLine is a resume entry: a person's extension's, with its amount; a
// firing's makes no line, its item_triggered's divider being its line
// (R149); another cause shows as itself.
func resumeLine(r *board.Resume) line {
	switch r.Cause {
	case resumeExtension:
		w := line(nil).say("The run resumed")
		if a := amountWords(item.Budget{Rounds: r.Rounds, WallClock: r.WallClock, Tokens: r.Tokens}); a != "" {
			w = w.say(" with " + a)
		}
		return w
	case string(item.ExtendFiring):
		return nil
	}
	return line(nil).say("The run resumed (").name(r.Cause).say(")")
}

// noticeLines are each notice's words, by its code (design D9; the words of
// the board, "Notices"), the names it holds each a name: a notice holds
// names, ids and codes alone (spec 4.2).
var noticeLines = map[board.NoticeCode]func(n *board.Notice) line{
	board.NoticeRefusedCall: func(n *board.Notice) line {
		w := line(nil).say("A call by ").name(n.Agent)
		if n.Tool != "" {
			w = w.say(" (to ").name(n.Tool).say(")")
		}
		w = w.say(" was refused")
		if n.Rule != "" {
			w = w.say(" (by the board's rule ").name(n.Rule).say(")")
		}
		return w.say(".")
	},
	board.NoticeFailedRead: func(n *board.Notice) line {
		w := line(nil).say("A read by ").name(n.Agent)
		if n.Tool != "" {
			w = w.say(" (of ").name(n.Tool).say(")")
		}
		return w.say(" failed.")
	},
	board.NoticeModelRefused: func(n *board.Notice) line {
		return line(nil).say("A model call of ").name(n.Agent).say(" was refused by the LLM gateway.")
	},
	board.NoticeModelFailed: func(n *board.Notice) line {
		return line(nil).say("A model call of ").name(n.Agent).say(" failed at the model vendor.")
	},
	board.NoticeModelUnknown: func(n *board.Notice) line {
		return line(nil).say("A model call of ").name(n.Agent).say(" ended with its outcome unknown.")
	},
	board.NoticeNoAgents: func(*board.Notice) line { return line(nil).say("No agent was left to invite.") },
	board.NoticeTurnUnstarted: func(n *board.Notice) line {
		w := line(nil).say("The turn of ").name(n.Agent).say(" could not start")
		if n.Reason != "" {
			w = w.say(": ").code(unstartedWords, n.Reason)
		}
		return w.say(".")
	},
	board.NoticeSkillYanked: func(n *board.Notice) line {
		return line(nil).name(n.Agent).say(" pins the skill version ").name(n.Skill).
			say(", which the current configuration yanks; this item keeps the version it locked.")
	},
	board.NoticeKillSwitch: func(*board.Notice) line { return line(nil).say("The kill switch stopped the run.") },
}

// noticeLine is n's line (design D9): its code's words (noticeLines); or,
// for a code this build has no words for, as a later build may write one
// (Decision 7), the code itself, a name, inert in its <bdi>, inside the
// platform's "A notice of the code" (line.code's rule: never through say);
// then, for a line that stands for repeats of one round, how many there were.
func noticeLine(n *board.Notice, repeats int) line {
	var w line
	if words, ok := noticeLines[n.Code]; ok {
		w = words(n)
	} else {
		w = line(nil).say("A notice of the code ").name(string(n.Code)).say(".")
	}
	if repeats > 1 {
		w = w.say(" (" + strconv.Itoa(repeats) + " times this round)")
	}
	return w
}
