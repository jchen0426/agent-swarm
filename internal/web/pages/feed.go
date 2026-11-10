package pages

import (
	"strconv"
	"strings"
	"time"

	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The feed's paging (R159, R167): the region shows the newest feedShown of
// its lines, and the query feed=all every line.
const (
	feedShown = 50
	feedAll   = "all"
)

// triggerRef begins a trigger update's platform evidence's ref,
// trigger:<event id>, as the projection reads it (R150).
const triggerRef = "trigger:"

// feedLine is a line of the feed as its region shows it: its anchor,
// rec-<seq> for a record's line and ev-<seq> for an entry's, from the seqs
// alone; its time, to the second, as words, and as RFC 3339 for its <time>;
// whether it divides the feed, as a round's and a firing resume's do; and
// its words, a line of parts (R171).
type feedLine struct {
	ID, At, When string
	Divider      bool
	Line         line
}

// feedOf is the feed region of bv's board (design D8; R167): its lines,
// oldest first, as the projection merged them: the newest feedShown, with
// the path of the whole feed when it holds more; or, all, every line. member
// says that the viewer reads the raw outputs its evidence cites (R152). bv
// is the watch's, shared: it is only read.
func (s *site) feedOf(bv *app.BoardView, member, all bool) feedRegion {
	f := &feedWords{s: s, id: bv.View.ID, member: member, feed: bv.Feed, records: map[int64]int{}, entries: map[board.EntryID]int{}}
	for i := range bv.Feed {
		if l := &bv.Feed[i]; l.Record != nil {
			f.records[l.Record.Seq] = i
		} else {
			f.entries[l.Event.ID] = i
		}
	}
	var r feedRegion
	if n := len(bv.Feed); !all && n > feedShown {
		f.first, r.Earlier = n-feedShown, boardPath(f.id)+"?feed="+feedAll
	}
	for i := f.first; i < len(bv.Feed); i++ {
		l := &bv.Feed[i]
		words, divider := f.words(l)
		if words == nil {
			continue
		}
		r.Lines = append(r.Lines, feedLine{ID: anchorOf(l), At: s.atSecond(l.At), When: l.At.UTC().Format(time.RFC3339), Divider: divider, Line: words})
	}
	return r
}

// atSecond is t in the deployment's zone, to the second, as a feed line
// shows its time and another line's (the words of the board).
func (s *site) atSecond(t time.Time) string { return t.In(s.zone).Format("15:04:05") }

// anchorOf is l's anchor: rec-<seq> for a record's line, ev-<seq> for an
// entry's.
func anchorOf(l *app.FeedLine) string {
	if l.Record != nil {
		return "rec-" + strconv.FormatInt(l.Record.Seq, 10)
	}
	return "ev-" + strconv.FormatInt(l.Event.Seq, 10)
}

// feedWords are what a feed's lines are read against: the site, for its
// zone; the board's item, whose raw outputs a member reads; the feed, each
// record's line by its seq and each entry's by its id; and the first line
// the region shows, those before it left out.
type feedWords struct {
	s       *site
	id      item.ID
	member  bool
	feed    []app.FeedLine
	records map[int64]int
	entries map[board.EntryID]int
	first   int
}

// href is the link to the feed's line i: its anchor on this page; or, when
// the region leaves it out, its anchor on the whole feed's.
func (f *feedWords) href(i int) string {
	a := anchorOf(&f.feed[i])
	if i >= f.first {
		return "#" + a
	}
	return boardPath(f.id) + "?feed=" + feedAll + "#" + a
}

// words is l's line, and whether it divides the feed (see recordLine and
// entryLine): nil for a line of no kind this build has words for, which
// the projection never gives, and which the region leaves out.
func (f *feedWords) words(l *app.FeedLine) (line, bool) {
	if l.Record != nil {
		return f.recordLine(l)
	}
	return f.entryLine(l)
}

// recordLine is the line of l, a record's (the words of the board, "The
// item's records in the feed"; R149), and whether it divides the feed, as
// a firing resume's does: the opening, the start, the run's creation, a
// promotion, a close, a reopen, a person's extension, a link, an unlink, and
// each trigger event. The four kinds whose effect shows in a round's
// divider, the stop entry and the header make no line of their own, nor
// does a firing resume's run_extended: the projection gives none.
func (f *feedWords) recordLine(l *app.FeedLine) (line, bool) {
	b := &l.Record.Body
	switch {
	case b.Opened != nil:
		return openedLine(nil, b.Opened), false
	case b.Started != nil:
		return line(nil).say("Started under configuration ").name(firstRunes(b.Started.Commit, 12)), false
	case b.RunCreated != nil:
		return runCreatedLine(b.RunCreated), false
	case b.Promoted != nil:
		return reasoned(line(nil).say("Promoted by ").name(b.Promoted.By), b.Promoted.Reason), false
	case b.Closed != nil:
		return closedRecordLine(b.Closed), false
	case b.Reopened != nil:
		return reasoned(line(nil).say("Reopened by ").name(b.Reopened.By), b.Reopened.Reason), false
	case b.Extended != nil:
		return extendedLine(b.Extended), false
	case b.Linked != nil:
		return linkedLine(b.Linked), false
	case b.Unlinked != nil:
		return reasoned(line(nil).say("Unlinked by ").name(b.Unlinked.By).say(" into an incident of its own"), b.Unlinked.Reason), false
	case b.Triggered != nil:
		return f.triggeredLine(nil, l), b.Triggered.Resume == item.ResumeResumed
	}
	return nil, false
}

// triggeredLine is w, then the words of l, an item_triggered's line (the
// words of the board; design D8; spec 10.2 rows 10 and 11): a firing
// resume's divider; a declined one's, its code in the platform's words or as
// itself; a late event's, not applied; an applied resolution's, with how
// many instances still fire once it applied; or an applied firing's; each
// instance quoted data and each source a name; then its title, from the
// inbound store (see titled).
func (f *feedWords) triggeredLine(w line, l *app.FeedLine) line {
	t := l.Record.Body.Triggered
	at := f.s.at(t.OccurredAt)
	code, declined := strings.CutPrefix(t.Resume, item.ResumeDeclined)
	switch {
	case t.Resume == item.ResumeResumed:
		w = w.say("Reopened after a flap: the alert fired again at " + at + " (instance ").value(t.InstanceID).say(" from ").name(t.Source).say(")")
	case declined:
		w = w.say("The alert fired again; the agents did not resume: ").code(declinedWords, code)
	case !t.Applied:
		w = w.say("Received late, not applied: alert ").code(triggerStatusWords, string(t.Status)).say(" for instance ").value(t.InstanceID).
			say(" from ").name(t.Source).say(" at " + at)
	case t.Status == item.Resolved:
		w = w.say("Alert resolved: instance ").value(t.InstanceID).say(" from ").name(t.Source).say(" at " + at + "; " + stillFiring(l.Firing))
	default:
		w = w.say("Alert firing: instance ").value(t.InstanceID).say(" from ").name(t.Source).say(" at " + at)
	}
	return titled(w, l)
}

// entryLine is the line of l, a board entry's (the words of the board,
// "The board's entries in the feed"; design D8, D9), and whether it divides
// the feed, as a round's does. A turn end makes no line, its effect shown
// in its round's divider, nor does a firing resume's resume entry: the
// projection gives none.
func (f *feedWords) entryLine(l *app.FeedLine) (line, bool) {
	e := l.Event
	b := &e.Body
	switch {
	case b.Evidence != nil:
		return f.evidenceLine(l), false
	case b.Hypothesis != nil:
		return f.hypothesisLine(l), false
	case b.Retraction != nil:
		return f.retractionLine(l), false
	case b.Ruling != nil:
		return f.rulingLine(l), false
	case b.Task != nil:
		return taskLine(e), false
	case b.Claim != nil:
		return f.tasked(line(nil).name(e.Author.Name).say(" claimed "), l, ""), false
	case b.Completion != nil:
		return f.tasked(line(nil).name(e.Author.Name).say(" completed "), l, ", citing "+entriesCited(len(b.Completion.Evidence))), false
	case b.Proposal != nil:
		return proposalLine(l), false
	case b.Notice != nil:
		return noticeLine(b.Notice, l.Repeats), false
	case b.Round != nil:
		return f.roundLine(l), true
	case b.Stop != nil:
		return stopLine(l), false
	case b.Resume != nil:
		return resumeLine(b.Resume), false
	}
	return nil, false
}

// evidenceLine is the line of l, an evidence entry's (the words of the
// board; design D8, D10): the platform's, from its typed record, never its
// stored claim (see platformLine); a person's, its author's subject, its
// stance, their words, and the refutations it lifted, each linked to its
// line (see lifted), with its reason; an agent's, its name, its stance, its
// claim, model-written, its quote, quoted data, its source, a name, and, for
// a member alone, the link to its raw output (R152); and when it was
// retracted.
func (f *feedWords) evidenceLine(l *app.FeedLine) line {
	e, ev := l.Event, l.Event.Body.Evidence
	var w line
	switch e.Author.Kind {
	case board.AuthorPlatform:
		w = f.platformLine(l)
	case board.AuthorHuman:
		w = stanced(line(nil).name(e.Author.Name).say(" posted evidence"), ev, l.Key).say(". ").person(ev.Claim)
		if len(ev.Dismisses) > 0 {
			w = reasoned(f.lifted(w, ev.Dismisses), ev.Reason)
		}
	default:
		p := &ev.Provenance
		w = stanced(line(nil).name(e.Author.Name).say(" posted evidence"), ev, l.Key).say(". ").model(ev.Claim)
		if p.Quote != "" {
			w = w.say(". Quote: ").quote(p.Quote)
		}
		if p.Source != "" {
			w = w.say(". Source ").name(string(p.Source))
		}
		if f.member && p.CallID != "" {
			w = w.say(". ").link("Raw output", callPath(f.id, p.CallID))
		}
	}
	if !l.Retracted.IsZero() {
		w = w.say(" (retracted at " + f.s.atSecond(l.Retracted) + ")")
	}
	return w
}

// lifted is w, then the refutations a person's support lifted, ids, from
// the entry itself, since its line names no refutation's author (Task 2's
// FeedLine.Of): how many, then each linked to its line by its time, or named
// by its id when the feed holds no line of it.
func (f *feedWords) lifted(w line, ids []board.EntryID) line {
	w = w.say(". It lifted " + plural(len(ids), "refutation") + " (of ")
	for i, id := range ids {
		if i > 0 {
			w = w.say(", ")
		}
		if j, ok := f.entries[id]; ok {
			w = w.link(f.s.atSecond(f.feed[j].At), f.href(j))
		} else {
			w = w.name(string(id))
		}
	}
	return w.say(")")
}

// platformLine is the words of l, the platform's evidence, from its typed
// record, never its stored claim, which a title can make read as the
// platform's words (design D8, D15; review I8; R150): the opening's,
// item:<id>, its item_opened's words; a trigger update's, trigger:<event
// id>, its item_triggered's, the title the inbound store keeps among them;
// each linked to its record's line; one of a trigger event the item's
// records do not hold, so said; and one of another ref, by the ref, a name.
func (f *feedWords) platformLine(l *app.FeedLine) line {
	typed, ref := l.Typed, l.Event.Body.Evidence.Provenance.Ref
	switch {
	case typed != nil && typed.Body.Opened != nil:
		w := openedLine(line(nil).say("The platform's evidence of the opening: "), typed.Body.Opened)
		return f.recordLink(w, "see the opening", typed.Seq)
	case typed != nil && typed.Body.Triggered != nil:
		w := f.triggeredLine(line(nil).say("The platform's evidence of a trigger update: "), f.typedLine(l))
		return f.recordLink(w, "see the update", typed.Seq)
	case strings.HasPrefix(ref, triggerRef):
		return line(nil).say("The platform's evidence of a trigger event this item's records do not hold")
	}
	w := line(nil).say("The platform's evidence")
	if ref != "" {
		w = w.say(" (").name(ref).say(")")
	}
	return w
}

// typedLine is the feed's line of l's typed record, whose words the
// platform's evidence of it repeats: the instances firing once it applied,
// and its title, which that line holds. The projection gives every
// item_triggered its line; were one missing, the evidence's own facts stand
// in, its title among them.
func (f *feedWords) typedLine(l *app.FeedLine) *app.FeedLine {
	if i, ok := f.records[l.Typed.Seq]; ok {
		return &f.feed[i]
	}
	return &app.FeedLine{Record: l.Typed, Title: l.Title, Kept: l.Kept}
}

// recordLink is w, then words in parentheses, linked to the line of the
// item's record of seq; w alone when the feed holds no line of it.
func (f *feedWords) recordLink(w line, words string, seq int64) line {
	i, ok := f.records[seq]
	if !ok {
		return w
	}
	return w.say(" (").link(words, f.href(i)).say(")")
}

// hypothesisLine is the line of l, a hypothesis (the words of the board):
// its author, a name; its key, its hypothesis's for an alias (see keyed);
// how much evidence it cites; its statement, model-written; and, for an
// alias, that it counts for the earlier hypothesis of its key, linked to
// that one's line.
func (f *feedWords) hypothesisLine(l *app.FeedLine) line {
	e, h := l.Event, l.Event.Body.Hypothesis
	k := l.Key
	if len(k) == 0 {
		k = h.Key
	}
	w := keyed(line(nil).name(e.Author.Name).say(" posted the hypothesis "), k, e.ID).
		say(", citing " + entriesCited(len(h.Evidence)) + ". ").model(h.Statement)
	if h.AliasOf == "" {
		return w
	}
	w = w.say(" (the same key as ")
	if i, ok := f.entries[h.AliasOf]; ok {
		w = w.link("an earlier hypothesis", f.href(i))
	} else {
		w = w.say("an earlier hypothesis")
	}
	return w.say(", for which it counts)")
}

// retractionLine is the line of l, a retraction (the words of the board):
// its author, a name, and its evidence, linked to its line by its time, or
// named by its id when the feed holds no line of it.
func (f *feedWords) retractionLine(l *app.FeedLine) line {
	e := l.Event
	w := line(nil).name(e.Author.Name).say(" retracted its evidence")
	id := e.Body.Retraction.Evidence
	if i, ok := f.entries[id]; ok {
		return w.say(" of ").link(f.s.atSecond(f.feed[i].At), f.href(i))
	}
	return w.say(" ").name(string(id))
}

// rulingLine is the line of l, a person's ruling (the words of the board):
// a rejection or an un-rejection of a hypothesis, by its key; or a
// dismissal of a refutation, linked to its line, of its hypothesis's key
// and by its author; each with its reason. A ruling of a kind this build
// has no words for, which a replay refuses, makes no line.
func (f *feedWords) rulingLine(l *app.FeedLine) line {
	e, r := l.Event, l.Event.Body.Ruling
	w := line(nil).name(e.Author.Name)
	switch r.Kind {
	case board.RulingReject:
		w = keyed(w.say(" rejected the hypothesis "), l.Key, r.Hypothesis)
	case board.RulingUnreject:
		w = keyed(w.say(" un-rejected the hypothesis "), l.Key, r.Hypothesis)
	case board.RulingDismiss:
		w = w.say(" dismissed ")
		if i, ok := f.entries[r.Refutation]; ok {
			w = w.link("a refutation", f.href(i))
		} else {
			w = w.say("a refutation")
		}
		if len(l.Key) > 0 {
			w = w.say(" of ").name(l.Key.String())
		}
		if l.Of.Name != "" {
			w = w.say(" by ").name(l.Of.Name)
		}
	default:
		return nil
	}
	return reasoned(w, r.Reason)
}

// tasked is w, then "a task", linked to the task's line, then citing, then
// the task's title, model-written, when the feed holds the task: a claim's
// line, or a completion's.
func (f *feedWords) tasked(w line, l *app.FeedLine, citing string) line {
	t := l.Task
	if i, ok := f.taskLineOf(t); ok {
		w = w.link("a task", f.href(i))
	} else {
		w = w.say("a task")
	}
	w = w.say(citing)
	if t != nil && t.Body.Task != nil {
		w = w.say(". ").model(t.Body.Task.Title)
	}
	return w
}

// taskLineOf is the feed's line of the task t, and whether it has one.
func (f *feedWords) taskLineOf(t *board.Event) (int, bool) {
	if t == nil {
		return 0, false
	}
	i, ok := f.entries[t.ID]
	return i, ok
}

// roundLine is the line of l, a round's divider (the words of the board;
// R149): its number; for a wrap-up round, its hypothesis's key; then, from
// the round's records, how it ended, quiet or not, or, while it is open, its
// deadline, rounded up to the minute (R146); its number alone when the
// records hold no such round.
func (f *feedWords) roundLine(l *app.FeedLine) line {
	rs := l.Event.Body.Round
	w := line(nil).say("Round " + strconv.Itoa(rs.Round))
	if rs.WrapUp != "" {
		w = keyed(w.say(", the wrap-up round of "), l.Key, rs.WrapUp)
	}
	switch r := l.Round; {
	case r == nil:
	case r.Ended != nil:
		w = w.say(", ended: ").code(endedWords, string(r.Ended.EndedBy))
		if r.Ended.Quiet {
			w = w.say(", with nothing new")
		}
	default:
		w = w.say(", open until " + f.s.atLatest(r.Opened.Deadline))
	}
	return w
}
