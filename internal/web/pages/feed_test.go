package pages_test

import (
	"bytes"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/jchen0426/agent-swarm/internal/web/auth"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// The feed's fixtures' authors.
var (
	agentA = board.Author{Kind: board.AuthorAgent, Name: "agent-a"}
	agentB = board.Author{Kind: board.AuthorAgent, Name: "agent-b"}
	bob    = board.Author{Kind: board.AuthorHuman, Name: "sso:bob"}
	rita   = board.Author{Kind: board.AuthorHuman, Name: "sso:rita"}
)

// checkout is the key of the feed's fixtures' hypothesis, as the page shows
// it, its canonical form (key.Key.String).
var checkout = key.Key{"service": "checkout"}

const checkoutKey = `service="checkout"`

// occurred is when the fixtures' trigger events say their status began, and
// shownOccurred the minute the feed shows it at, in the zone; deadline is
// the fixtures' rounds' deadline, and shownDeadline the minute the feed
// shows it at, rounded up (R146).
var (
	occurred = time.Date(2026, 11, 11, 14, 10, 30, 0, time.UTC)
	deadline = time.Date(2026, 11, 11, 14, 25, 30, 0, time.UTC)
)

const (
	shownOccurred = "2026-11-11 14:10 UTC"
	shownDeadline = "2026-11-11 14:26 UTC"
)

// clockOf is t as the feed shows a line's time, and the time of another
// line it names, to the second, in the zone.
func clockOf(t time.Time) string { return t.UTC().Format("15:04:05") }

// fire is an applied firing of the instance i-1 from alerting, event x1.
func fire() *item.Triggered {
	return &item.Triggered{Source: "alerting", EventID: "x1", InstanceID: "i-1", DedupKey: "d", Status: item.Firing, OccurredAt: occurred, Applied: true}
}

// triggered adds the line of t to f, the inbound store keeping its title
// title, or keeping none when title is empty, firing instances firing once
// it applied, and returns its anchor.
func triggered(f *feedFixture, t *item.Triggered, title string, firing int) string {
	anchor := f.record(&item.Body{Triggered: t})
	f.last().Title, f.last().Kept, f.last().Firing = title, title != "", firing
	return anchor
}

// platform is the platform's evidence of ref, from source, which claims
// claim.
func platform(claim, ref string, source board.Source) *board.Body {
	return &board.Body{Evidence: &board.Evidence{Claim: claim, Provenance: board.Provenance{Kind: board.FromPlatform, Ref: ref, Source: source}}}
}

// readEvidence is an agent's evidence from its read call, quoting quote
// from source, of stance on hypothesis hyp; no stance for "".
func readEvidence(claim, call, quote string, source board.Source, hyp board.EntryID, stance board.Stance) *board.Body {
	return &board.Body{Evidence: &board.Evidence{Claim: claim, Provenance: board.Provenance{Kind: board.FromReadCall, CallID: call, Quote: quote, Source: source},
		Hypothesis: hyp, Stance: stance}}
}

// feedRow is a line of the words of the board's tables (Plan 9a-2), as the
// feed shows it: its anchor, the words it shows, its time left out, its
// parts (see partsOf), and whether it divides the feed.
type feedRow struct {
	name, anchor, words string
	parts               []string
	divider             bool
}

// everyLine is item f1's feed of every line the words of the board's tables
// give: each record kind's, in each of its cases, R149's four left out;
// each entry kind's, in each of its cases, a turn end's and a firing
// resume's left out; each notice's, repeats counted; and each round's
// divider; with the rows its lines render.
func everyLine() (*feedFixture, []feedRow) {
	f := &feedFixture{id: "f1"}
	var rows []feedRow
	row := func(name, anchor, words string, parts ...string) {
		rows = append(rows, feedRow{name: name, anchor: anchor, words: words, parts: parts})
	}
	divider := func(name, anchor, words string, parts ...string) {
		rows = append(rows, feedRow{name: name, anchor: anchor, words: words, parts: parts, divider: true})
	}

	// The item's records.
	opening := f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Checkout fails",
		Routing: map[string]string{"service": "checkout", "region": "eu-1"}}})
	openingRecord := f.last().Record
	row("an opening by a person, its title and its routing", opening,
		"Opened by sso:carol. Title: Checkout fails. Routing: region = eu-1, service = checkout",
		"name:sso:carol", "data:Checkout fails", "name:region", "data:eu-1", "name:service", "data:checkout")
	row("an opening by a source", f.record(&item.Body{Opened: &item.Opened{Source: "alerting", By: "source:alerting", Title: "Disk full"}}),
		"Opened by the source alerting. Title: Disk full", "name:alerting", "data:Disk full")
	row("an opening through the admin API", f.record(&item.Body{Opened: &item.Opened{Source: "person", By: "admin", Title: "Disk full"}}),
		"Opened through the admin API. Title: Disk full", "data:Disk full")
	row("an opening of an earlier build, by no one recorded", f.record(&item.Body{Opened: &item.Opened{Source: "person", Title: "Disk full"}}),
		"Opened. Title: Disk full", "data:Disk full")
	row("a start", f.record(&item.Body{Started: &item.Started{Commit: "0123456789abcdef0123"}}),
		"Started under configuration 0123456789ab", "name:0123456789ab")
	row("a run's creation", f.record(&item.Body{RunCreated: &item.RunCreated{Agents: []string{"agent-a", "agent-b"},
		Budget: item.Budget{Rounds: 5, WallClock: 30 * time.Minute, Tokens: 50000}, RoundLimit: 150 * time.Second}}),
		"Run created: 2 agents invited (agent-a, agent-b); budget 5 rounds, 30m0s of wall clock, 50000 tokens; a round's time limit 2m30s",
		"name:agent-a", "name:agent-b")
	row("a promotion, its reason the person's words", f.record(&item.Body{Promoted: &item.Promoted{By: "sso:erin", Reason: &board.Reason{Code: "other", Text: "urgent"}}}),
		"Promoted by sso:erin, reason other: a person's words: urgent", "name:sso:erin", "name:other", "person:urgent")
	row("a promotion with no reason", f.record(&item.Body{Promoted: &item.Promoted{By: "admin"}}), "Promoted by admin", "name:admin")
	row("a close by itself", f.record(&item.Body{Closed: &item.Closed{Auto: true, Due: occurred}}), "Closed by itself")
	row("a close by a person, a preset reason's label", f.record(&item.Body{Closed: &item.Closed{By: "sso:rita", Reason: &board.Reason{Code: "resolved"}}}),
		"Closed by sso:rita, reason Resolved", "name:sso:rita")
	row("a reopen", f.record(&item.Body{Reopened: &item.Reopened{By: "sso:rita", Reason: &board.Reason{Code: "recurred"}}}),
		"Reopened by sso:rita, reason It came back", "name:sso:rita")
	row("a person's extension", f.record(&item.Body{Extended: &item.Extended{By: "sso:erin", Amount: item.Budget{Rounds: 2, WallClock: 10 * time.Minute},
		Reason: &board.Reason{Code: "other", Text: "needs more"}}}),
		"Extended by sso:erin: +2 rounds, +10m0s of wall clock, reason other: a person's words: needs more",
		"name:sso:erin", "name:other", "person:needs more")
	row("an extension of tokens", f.record(&item.Body{Extended: &item.Extended{By: "sso:erin", Amount: item.Budget{Tokens: 1}}}),
		"Extended by sso:erin: +1 token", "name:sso:erin")
	row("a link by a person", f.record(&item.Body{Linked: &item.Linked{By: "sso:rita", With: "item-b", Reason: &board.Reason{Code: "same_cause"}}}),
		"Linked by sso:rita with item item-b, reason same_cause", "name:sso:rita", "link:/items/item-b", "name:item-b", "name:same_cause")
	row("a link with an item whose id holds a path's marks, a path segment escaped", f.record(&item.Body{Linked: &item.Linked{By: "sso:rita",
		With: "item-b/x?y#z", Reason: &board.Reason{Code: "same_cause"}}}),
		"Linked by sso:rita with item item-b/x?y#z, reason same_cause", "name:sso:rita", "link:/items/item-b%2Fx%3Fy%23z", "name:item-b/x?y#z", "name:same_cause")
	row("a flap's link by its source", f.record(&item.Body{Linked: &item.Linked{By: "source:alerting", With: "item-p", Reason: &board.Reason{Code: "flap"}}}),
		"Joined the incident of item item-p, whose alert fired again after it closed (linked by the source alerting)",
		"link:/items/item-p", "name:item-p", "name:alerting")
	row("an unlink", f.record(&item.Body{Unlinked: &item.Unlinked{By: "sso:rita", Reason: &board.Reason{Code: "other", Text: "wrong incident"}}}),
		"Unlinked by sso:rita into an incident of its own, reason other: a person's words: wrong incident",
		"name:sso:rita", "name:other", "person:wrong incident")

	// The item's trigger events.
	firing := triggered(f, fire(), "Disk full", 1)
	firingRecord := f.last().Record
	row("an alert firing", firing, "Alert firing: instance i-1 from alerting at "+shownOccurred+". Title: Disk full",
		"data:i-1", "name:alerting", "data:Disk full")
	var stillFiring *item.Record
	for n, fires := range []string{"no instance fires now", "1 instance still fires", "2 instances still fire"} {
		resolved := fire()
		resolved.EventID, resolved.Status = "x2-"+strconv.Itoa(n), item.Resolved
		row("an alert resolved, "+fires, triggered(f, resolved, "Disk full", n),
			"Alert resolved: instance i-1 from alerting at "+shownOccurred+"; "+fires+". Title: Disk full",
			"data:i-1", "name:alerting", "data:Disk full")
		if n == 1 {
			stillFiring = f.last().Record
		}
	}
	for _, status := range []item.TriggerStatus{item.Firing, item.Resolved} {
		late := fire()
		late.EventID, late.Status, late.Applied = "x3-"+string(status), status, false
		row("a late event, "+string(status), triggered(f, late, "Disk full", 0),
			"Received late, not applied: alert "+string(status)+" for instance i-1 from alerting at "+shownOccurred+". Title: Disk full",
			"data:i-1", "name:alerting", "data:Disk full")
	}
	resumed := fire()
	resumed.EventID, resumed.Resume = "x4", item.ResumeResumed
	divider("a firing resume, the divider", triggered(f, resumed, "Disk full", 1),
		"Reopened after a flap: the alert fired again at "+shownOccurred+" (instance i-1 from alerting). Title: Disk full",
		"data:i-1", "name:alerting", "data:Disk full")
	for code, words := range map[string]string{
		app.CodeKillSwitchOn:    "the kill switch is on",
		"unsettled":             "the kill switch's state was not known",
		app.CodeDailyCapReached: "the day's spend has reached the daily cap",
		app.CodeNoAgentsLeft:    "no agent is left to take part",
		app.CodeWouldStopAtOnce: "the run has no budget left",
		app.CodeLimitReached:    "as many items are investigating as the limit allows",
		"no_run":                "the item has no run to resume",
	} {
		declined := fire()
		declined.EventID, declined.Resume = "x5-"+code, item.ResumeDeclined+code
		row("a firing resume declined, "+code, triggered(f, declined, "Disk full", 1),
			"The alert fired again; the agents did not resume: "+words+". Title: Disk full", "data:Disk full")
	}
	gone := fire()
	gone.EventID = "x6"
	row("an alert whose title the inbound store no longer keeps", triggered(f, gone, "", 1),
		"Alert firing: instance i-1 from alerting at "+shownOccurred+". Title: no longer kept (alert titles are kept 30 days)",
		"data:i-1", "name:alerting")

	// The board's rounds.
	round := func(n int, wrapUp board.EntryID, r *item.Round) string {
		anchor := f.entry(board.Coordinator, &board.Body{Round: &board.RoundStart{Round: n, WrapUp: wrapUp}})
		f.last().Round = r
		if wrapUp != "" {
			f.last().Key = checkout
		}
		return anchor
	}
	opened := func(n int) item.RoundOpened { return item.RoundOpened{Round: n, Deadline: deadline} }
	ended := func(n int, by item.EndedBy, quiet bool) *item.RoundEnded {
		return &item.RoundEnded{Round: n, EndedBy: by, Quiet: quiet}
	}
	divider("a round ended, quiet", round(1, "", &item.Round{Opened: opened(1), Ended: ended(1, item.EndedByTurns, true)}),
		"Round 1, ended: every agent ended its turn, with nothing new")

	// The board's evidence, hypotheses, retractions and rulings.
	logs := f.entry(agentA, readEvidence("the logs say so", "call-2", "errors at 10:02", "logs/search", "", ""))
	logsAt, logsLine := f.last().At, len(f.lines)-1
	hypothesis := f.entry(agentA, &board.Body{Hypothesis: &board.Hypothesis{Key: checkout, Statement: "A bad release", Evidence: []board.EntryID{"e2", "e4"}}})
	f.last().Key = checkout
	hypothesisID := f.last().Event.ID
	row("a hypothesis", hypothesis, `agent-a posted the hypothesis `+checkoutKey+`, citing 2 evidence entries. model-written: A bad release`,
		"name:agent-a", "name:"+checkoutKey, "model:A bad release")
	support := f.entry(agentA, readEvidence("the disk is full", "call-1", "disk full on db-1", "logs/search", hypothesisID, board.Supports))
	f.last().Key = checkout
	row("an agent's support, read by a member", support,
		`agent-a posted evidence (supporting `+checkoutKey+`). model-written: the disk is full. Quote: disk full on db-1. Source logs/search. Raw output`,
		"name:agent-a", "name:"+checkoutKey, "model:the disk is full", "data:disk full on db-1", "name:logs/search", "link:/items/f1/calls/call-1")
	refutation := f.entry(agentB, readEvidence("no deploy", "call-3", "no deploy since Monday", "deploys/list", hypothesisID, board.Refutes))
	f.last().Key = checkout
	refutationID, refutationAt := f.last().Event.ID, f.last().At
	row("an agent's refutation", refutation,
		`agent-b posted evidence (refuting `+checkoutKey+`). model-written: no deploy. Quote: no deploy since Monday. Source deploys/list. Raw output`,
		"name:agent-b", "name:"+checkoutKey, "model:no deploy", "data:no deploy since Monday", "name:deploys/list", "link:/items/f1/calls/call-3")
	other := f.entry(agentB, readEvidence("no change", "call-4", "no change since Monday", "deploys/list", hypothesisID, board.Refutes))
	f.last().Key = checkout
	otherID, otherAt := f.last().Event.ID, f.last().At
	row("another agent's refutation", other,
		`agent-b posted evidence (refuting `+checkoutKey+`). model-written: no change. Quote: no change since Monday. Source deploys/list. Raw output`,
		"name:agent-b", "name:"+checkoutKey, "model:no change", "data:no change since Monday", "name:deploys/list", "link:/items/f1/calls/call-4")
	lifting := f.entry(bob, &board.Body{Evidence: &board.Evidence{Claim: "I saw the release go out", Provenance: board.Provenance{Kind: board.FromHuman, Source: board.HumanObservation},
		Hypothesis: hypothesisID, Stance: board.Supports, Dismisses: []board.EntryID{refutationID, otherID}, Reason: &board.Reason{Code: "not_the_cause"}}})
	f.last().Key = checkout
	row("a person's support that lifts refutations", lifting,
		`sso:bob posted evidence (supporting `+checkoutKey+`). A person's words: I saw the release go out. It lifted 2 refutations (of `+
			clockOf(refutationAt)+`, `+clockOf(otherAt)+`), reason not_the_cause`,
		"name:sso:bob", "name:"+checkoutKey, "person:I saw the release go out", "link:#"+refutation, "link:#"+other, "name:not_the_cause")
	row("a person's evidence of no stance", f.entry(bob, &board.Body{Evidence: &board.Evidence{Claim: "it is back",
		Provenance: board.Provenance{Kind: board.FromHuman, Source: board.HumanObservation}}}),
		"sso:bob posted evidence. A person's words: it is back", "name:sso:bob", "person:it is back")
	row("a person's evidence of no stance, a reason's code alone", f.entry(bob, &board.Body{Evidence: &board.Evidence{Claim: "it is back again",
		Provenance: board.Provenance{Kind: board.FromHuman, Source: board.HumanObservation}, Reason: &board.Reason{Code: "not_the_cause"}}}),
		"sso:bob posted evidence. A person's words: it is back again, reason not_the_cause", "name:sso:bob", "person:it is back again", "name:not_the_cause")
	explained := readEvidence("no rollback", "call-5", "no rollback since Monday", "deploys/list", hypothesisID, board.Refutes)
	explained.Evidence.Reason = &board.Reason{Code: "other", Text: "checked twice"}
	row("an agent's refutation whose stored reason, which no agent gives, shows not", f.entry(agentB, explained),
		`agent-b posted evidence (refuting `+checkoutKey+`). model-written: no rollback. Quote: no rollback since Monday. Source deploys/list. Raw output`,
		"name:agent-b", "name:"+checkoutKey, "model:no rollback", "data:no rollback since Monday", "name:deploys/list", "link:/items/f1/calls/call-5")
	f.last().Key = checkout
	person := func(stance board.Stance, claim string) *board.Body {
		return &board.Body{Evidence: &board.Evidence{Claim: claim, Provenance: board.Provenance{Kind: board.FromHuman, Source: board.HumanObservation},
			Hypothesis: hypothesisID, Stance: stance, Reason: &board.Reason{Code: "other", Text: "checked twice"}}}
	}
	row("a person's support, with a reason and its text", f.entry(bob, person(board.Supports, "the release went out")),
		`sso:bob posted evidence (supporting `+checkoutKey+`). A person's words: the release went out, reason other: a person's words: checked twice`,
		"name:sso:bob", "name:"+checkoutKey, "person:the release went out", "name:other", "person:checked twice")
	f.last().Key = checkout
	row("a person's refutation, with a reason and its text", f.entry(bob, person(board.Refutes, "no release went out")),
		`sso:bob posted evidence (refuting `+checkoutKey+`). A person's words: no release went out, reason other: a person's words: checked twice`,
		"name:sso:bob", "name:"+checkoutKey, "person:no release went out", "name:other", "person:checked twice")
	f.last().Key = checkout
	ofOpening := f.entry(board.Coordinator, platform("the alert opened this item", "item:f1", "person"))
	f.last().Typed = openingRecord
	row("the platform's evidence of the opening", ofOpening,
		"The platform's evidence of the opening: Opened by sso:carol. Title: Checkout fails. Routing: region = eu-1, service = checkout (see the opening)",
		"name:sso:carol", "data:Checkout fails", "name:region", "data:eu-1", "name:service", "data:checkout", "link:#"+opening)
	ofUpdate := f.entry(board.Coordinator, platform("the alert fired", "trigger:x1", "alerting"))
	f.last().Typed, f.last().Title, f.last().Kept = firingRecord, "Disk full", true
	row("the platform's evidence of a trigger update", ofUpdate,
		"The platform's evidence of a trigger update: Alert firing: instance i-1 from alerting at "+shownOccurred+". Title: Disk full (see the update)",
		"data:i-1", "name:alerting", "data:Disk full", "link:#"+firing)
	ofResolution := f.entry(board.Coordinator, platform("the alert resolved", "trigger:x2-1", "alerting"))
	f.last().Typed, f.last().Title, f.last().Kept = stillFiring, "Disk full", true
	row("the platform's evidence of a resolution, the instances still firing its record's", ofResolution,
		"The platform's evidence of a trigger update: Alert resolved: instance i-1 from alerting at "+shownOccurred+
			"; 1 instance still fires. Title: Disk full (see the update)",
		"data:i-1", "name:alerting", "data:Disk full", "link:#rec-"+strconv.FormatInt(stillFiring.Seq, 10))
	row("the platform's evidence of a trigger event the records do not hold", f.entry(board.Coordinator, platform("the alert fired", "trigger:x9", "alerting")),
		"The platform's evidence of a trigger event this item's records do not hold")
	row("the platform's evidence of another ref", f.entry(board.Coordinator, platform("a probe read", "probe:x", "probes")),
		"The platform's evidence (probe:x)", "name:probe:x")
	reasonedPlatform := platform("a probe read", "probe:y", "probes")
	reasonedPlatform.Evidence.Reason = &board.Reason{Code: "other", Text: "a stored reason"}
	row("the platform's evidence with a stored reason, which the platform gives none, shows not", f.entry(board.Coordinator, reasonedPlatform),
		"The platform's evidence (probe:y)", "name:probe:y")
	alias := f.entry(agentB, &board.Body{Hypothesis: &board.Hypothesis{Key: checkout, Statement: "The release of checkout", Evidence: []board.EntryID{refutationID},
		AliasOf: hypothesisID}})
	f.last().Key = checkout
	row("a hypothesis of an earlier one's key, its alias", alias,
		`agent-b posted the hypothesis `+checkoutKey+`, citing 1 evidence entry. model-written: The release of checkout (the same key as an earlier hypothesis, for which it counts)`,
		"name:agent-b", "name:"+checkoutKey, "model:The release of checkout", "link:#"+hypothesis)
	retraction := f.entry(agentA, &board.Body{Retraction: &board.Retraction{Evidence: "e2"}})
	f.last().Of = agentA
	f.lines[logsLine].Retracted = f.last().At
	row("a retraction", retraction, "agent-a retracted its evidence of "+clockOf(logsAt), "name:agent-a", "link:#"+logs)
	row("an agent's evidence of no stance, retracted since", logs,
		"agent-a posted evidence. model-written: the logs say so. Quote: errors at 10:02. Source logs/search. Raw output (retracted at "+clockOf(f.last().At)+")",
		"name:agent-a", "model:the logs say so", "data:errors at 10:02", "name:logs/search", "link:/items/f1/calls/call-2")
	ruling := func(r *board.Ruling, of board.Author) string {
		anchor := f.entry(rita, &board.Body{Ruling: r})
		f.last().Key, f.last().Of = checkout, of
		return anchor
	}
	row("a rejection", ruling(&board.Ruling{Kind: board.RulingReject, Hypothesis: hypothesisID, Reason: &board.Reason{Code: "not_the_cause"}}, board.Author{}),
		"sso:rita rejected the hypothesis "+checkoutKey+", reason not_the_cause", "name:sso:rita", "name:"+checkoutKey, "name:not_the_cause")
	row("an un-rejection", ruling(&board.Ruling{Kind: board.RulingUnreject, Hypothesis: hypothesisID}, board.Author{}),
		"sso:rita un-rejected the hypothesis "+checkoutKey, "name:sso:rita", "name:"+checkoutKey)
	row("a dismissal of a refutation", ruling(&board.Ruling{Kind: board.RulingDismiss, Refutation: refutationID, Reason: &board.Reason{Code: "other", Text: "stale"}}, agentB),
		"sso:rita dismissed a refutation of "+checkoutKey+" by agent-b, reason other: a person's words: stale",
		"name:sso:rita", "link:#"+refutation, "name:"+checkoutKey, "name:agent-b", "name:other", "person:stale")

	// The board's tasks and proposals.
	task := f.entry(agentA, &board.Body{Task: &board.Task{Title: "check the deploy", Tags: []string{"deploys", "logs"}}})
	taskEvent := f.lines[len(f.lines)-1].Event
	row("a task", task, "agent-a posted a task (tags deploys, logs). model-written: check the deploy",
		"name:agent-a", "name:deploys", "name:logs", "model:check the deploy")
	row("a task with no tag", f.entry(agentA, &board.Body{Task: &board.Task{Title: "read the logs"}}),
		"agent-a posted a task. model-written: read the logs", "name:agent-a", "model:read the logs")
	claim := f.entry(agentB, &board.Body{Claim: &board.Claim{Task: taskEvent.ID}})
	f.last().Task = taskEvent
	row("a claim", claim, "agent-b claimed a task. model-written: check the deploy", "name:agent-b", "link:#"+task, "model:check the deploy")
	completion := f.entry(agentB, &board.Body{Completion: &board.Completion{Task: taskEvent.ID, Evidence: []board.EntryID{"e4"}}})
	f.last().Task = taskEvent
	row("a completion", completion, "agent-b completed a task, citing 1 evidence entry. model-written: check the deploy",
		"name:agent-b", "link:#"+task, "model:check the deploy")
	proposal := f.entry(board.Gateway, &board.Body{Proposal: &board.Proposal{Proposer: "agent-a", Tool: "deploy.rollback", Hypothesis: hypothesisID,
		Summary: []board.SummaryPart{{Text: "Roll back "}, {Text: "checkout", Value: true}, {Text: " to v41"}}}})
	f.last().Key = checkout
	row("a proposal, its summary's text parts each isolated", proposal, "A suggestion was recorded for agent-a, under "+checkoutKey+": Roll back checkout to v41",
		"name:agent-a", "name:"+checkoutKey, "name:Roll back ", "data:checkout", "name: to v41")

	// The board's notices (design D9).
	notice := func(n *board.Notice, repeats int) string {
		anchor := f.entry(board.Coordinator, &board.Body{Notice: n})
		f.last().Repeats = repeats
		return anchor
	}
	row("a refused call, repeated", notice(&board.Notice{Code: board.NoticeRefusedCall, Agent: "agent-a", Tool: "logs.search", Rule: "one_claim"}, 3),
		"A call by agent-a (to logs.search) was refused (by the board's rule one_claim). (3 times this round)",
		"name:agent-a", "name:logs.search", "name:one_claim")
	row("a refused call of no tool of the agent's list, by no board rule", notice(&board.Notice{Code: board.NoticeRefusedCall, Agent: "agent-a"}, 1),
		"A call by agent-a was refused.", "name:agent-a")
	row("a failed read", notice(&board.Notice{Code: board.NoticeFailedRead, Agent: "agent-a", Tool: "logs.search"}, 1),
		"A read by agent-a (of logs.search) failed.", "name:agent-a", "name:logs.search")
	row("a failed read of no tool of the agent's list", notice(&board.Notice{Code: board.NoticeFailedRead, Agent: "agent-a"}, 1),
		"A read by agent-a failed.", "name:agent-a")
	row("a model call refused", notice(&board.Notice{Code: board.NoticeModelRefused, Agent: "agent-a", CallID: "m-1"}, 1),
		"A model call of agent-a was refused by the LLM gateway.", "name:agent-a")
	row("a model call failed", notice(&board.Notice{Code: board.NoticeModelFailed, Agent: "agent-a", CallID: "m-2"}, 2),
		"A model call of agent-a failed at the model vendor. (2 times this round)", "name:agent-a")
	row("a model call of unknown outcome", notice(&board.Notice{Code: board.NoticeModelUnknown, Agent: "agent-a", CallID: "m-3"}, 1),
		"A model call of agent-a ended with its outcome unknown.", "name:agent-a")
	row("no agent left", notice(&board.Notice{Code: board.NoticeNoAgents}, 1), "No agent was left to invite.")
	for reason, words := range map[string]string{
		"engine": "the runner's container engine did not answer",
		"image":  "its runtime image could not be used",
		"launch": "its sandbox could not be launched",
	} {
		row("a turn unstarted, "+reason, notice(&board.Notice{Code: board.NoticeTurnUnstarted, Agent: "agent-a", Reason: reason}, 1),
			"The turn of agent-a could not start: "+words+".", "name:agent-a")
	}
	row("a turn unstarted for a reason of a later build", notice(&board.Notice{Code: board.NoticeTurnUnstarted, Agent: "agent-a", Reason: "quota"}, 1),
		"The turn of agent-a could not start: quota.", "name:agent-a", "name:quota")
	row("a skill yanked", notice(&board.Notice{Code: board.NoticeSkillYanked, Agent: "agent-a", Skill: "triage@1.2.0"}, 1),
		"agent-a pins the skill version triage@1.2.0, which the current configuration yanks; this item keeps the version it locked.",
		"name:agent-a", "name:triage@1.2.0")
	row("the kill switch", notice(&board.Notice{Code: board.NoticeKillSwitch}, 1), "The kill switch stopped the run.")
	row("a notice of a later build's code", notice(&board.Notice{Code: "melt_down", Agent: "agent-a"}, 1),
		"A notice of the code melt_down.", "name:melt_down")

	// The board's other rounds, and its stops and resumes.
	divider("a round open", round(2, "", &item.Round{Opened: opened(2)}), "Round 2, open until "+shownDeadline)
	divider("a wrap-up round open", round(3, hypothesisID, &item.Round{Opened: opened(3)}),
		"Round 3, the wrap-up round of "+checkoutKey+", open until "+shownDeadline, "name:"+checkoutKey)
	divider("a round past its time limit", round(4, "", &item.Round{Opened: opened(4), Ended: ended(4, item.EndedByDeadline, false)}),
		"Round 4, ended: its time limit passed")
	divider("a round of an outage", round(5, "", &item.Round{Opened: opened(5), Ended: ended(5, item.EndedByOutage, false)}),
		"Round 5, ended: no instance held the lease at its deadline")
	divider("a round the run's stop ended", round(6, "", &item.Round{Opened: opened(6), Ended: ended(6, item.EndedByStopped, false)}),
		"Round 6, ended: the run stopped during it")
	divider("a wrap-up round ended", round(7, hypothesisID, &item.Round{Opened: opened(7), Ended: ended(7, item.EndedByTurns, false)}),
		"Round 7, the wrap-up round of "+checkoutKey+", ended: every agent ended its turn", "name:"+checkoutKey)
	divider("a round the records do not hold", round(8, "", nil), "Round 8")
	divider("a round ended in a way of a later build's", round(9, "", &item.Round{Opened: opened(9), Ended: ended(9, "paused", false)}),
		"Round 9, ended: paused", "name:paused")
	stop := f.entry(board.Coordinator, &board.Body{Stop: &board.Stop{Reason: "converged", Conclusion: hypothesisID, Rounds: 3, Tokens: 4200, WallClock: 90 * time.Second}})
	f.last().Key = checkout
	row("a stop with its conclusion", stop,
		"The run stopped: converged. It used 3 rounds, 4200 tokens and 1m30s of wall clock. (Conclusion: "+checkoutKey+")", "name:"+checkoutKey)
	row("a stop with none", f.entry(board.Coordinator, &board.Body{Stop: &board.Stop{Reason: "quiet", Rounds: 1, WallClock: 2 * time.Minute}}),
		"The run stopped: a round passed with nothing new. It used 1 round, 0 tokens and 2m0s of wall clock.")
	row("a stop of a later build's reason", f.entry(board.Coordinator, &board.Body{Stop: &board.Stop{Reason: "melted", Rounds: 2, Tokens: 1, WallClock: time.Second}}),
		"The run stopped: melted. It used 2 rounds, 1 token and 1s of wall clock.", "name:melted")
	row("a resume of a person's extension", f.entry(board.Coordinator, &board.Body{Resume: &board.Resume{Cause: "extension", Rounds: 1, Tokens: 1000}}),
		"The run resumed with +1 round, +1000 tokens")
	row("a resume of another cause", f.entry(board.Coordinator, &board.Body{Resume: &board.Resume{Cause: "probe"}}),
		"The run resumed (probe)", "name:probe")
	return f, rows
}

// Every line of the feed renders its words (the words of the board's
// tables; R149, R171): each record kind's, each entry kind's, each notice's
// and each round's, in each case the tables give, its words the platform's
// and each stored value its part, a name, quoted data, model-written text or
// a person's words; a round's and a firing resume's line divides the feed.
func TestEveryFeedLineHasItsWords(t *testing.T) {
	w := newWorld(t)
	f, rows := everyLine()
	w.boards.set(f.id, f.read())

	p := w.browser(t, "carol", auth.Responder).Get(w.public + "/items/f1?feed=all")

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	anchors := map[string]bool{}
	for _, r := range rows {
		anchors[r.anchor] = true
		words, n := feedLineOf(t, p.Body, r.anchor)
		assert.Equal(t, r.words, words, r.name)
		assert.Equal(t, r.parts, partsOf(n), r.name)
		assert.Equal(t, r.divider, attr(n, "class") == "divider", "%s: a divider", r.name)
	}
	assert.Len(t, anchors, len(f.lines), "a row for each line")
	assert.Len(t, feedAnchors(t, p.Body), len(f.lines), "every line shown")
}

// Every notice code the board knows, its Notice* constants, and each code a
// declined firing resume gives, R112's seven, the coordinator's own two
// among them, have the platform's words (the plan's; design D9): no line of
// one names its code; and a code the page has no words for reads as itself,
// a name, inert in its <bdi>, never among the platform's words (Task 6's
// ruling): a notice of a later build's code, and a resume it declined.
func TestEveryNoticeAndDeclinedCodeHasWords(t *testing.T) {
	notices := constants(t, filepath.Join("..", "..", "work", "board"), "Notice")
	require.Contains(t, notices, string(board.NoticeRefusedCall))
	require.Contains(t, notices, string(board.NoticeKillSwitch))
	declined := []string{app.CodeKillSwitchOn, "unsettled", app.CodeDailyCapReached, app.CodeNoAgentsLeft, app.CodeWouldStopAtOnce, app.CodeLimitReached, "no_run"}
	own := constants(t, filepath.Join("..", "..", "work", "app"), "declined")
	require.Len(t, own, 2, "the coordinator's own codes, no_run and unsettled")
	for _, code := range own {
		require.Contains(t, declined, code)
	}
	const unknownNotice, unknownDeclined = "melt_down", "budget_gone"
	f := &feedFixture{id: "n1"}
	noticeOf, declinedOf := map[string]string{}, map[string]string{}
	for _, code := range append(slices.Clone(notices), unknownNotice) {
		noticeOf[code] = f.entry(board.Coordinator, &board.Body{Notice: &board.Notice{
			Code: board.NoticeCode(code), Agent: "agent-a", Tool: "logs.search", Rule: "one_claim", Reason: "engine", Skill: "triage@1.2.0", CallID: "m-1",
		}})
	}
	for _, code := range append(slices.Clone(declined), unknownDeclined) {
		ev := fire()
		ev.EventID, ev.Resume = "x-"+code, item.ResumeDeclined+code
		declinedOf[code] = triggered(f, ev, "Disk full", 1)
	}
	w := newWorld(t)
	w.boards.set(f.id, f.read())

	p := w.browser(t, "vic", auth.Viewer).Get(w.public + "/items/n1")

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	for code, anchor := range noticeOf {
		words, n := feedLineOf(t, p.Body, anchor)
		if code == unknownNotice {
			assert.Equal(t, "A notice of the code melt_down.", words)
			assert.Equal(t, []string{"name:melt_down"}, partsOf(n), "the code, a name")
			continue
		}
		assert.NotContains(t, words, "A notice of the code", code)
		assert.NotContains(t, words, code, "%s has its words", code)
	}
	for code, anchor := range declinedOf {
		words, n := feedLineOf(t, p.Body, anchor)
		if code == unknownDeclined {
			assert.Equal(t, "The alert fired again; the agents did not resume: budget_gone. Title: Disk full", words)
			assert.Equal(t, []string{"name:budget_gone", "data:Disk full"}, partsOf(n), "the code, a name")
			continue
		}
		assert.True(t, strings.HasPrefix(words, "The alert fired again; the agents did not resume: "), code)
		assert.NotContains(t, words, code, "%s has its words", code)
		assert.Equal(t, []string{"data:Disk full"}, partsOf(n), "%s: the platform's words alone, and the title", code)
	}
}

// linkTo is the path of body's link whose words are words; it must exist,
// once.
func linkTo(t *testing.T, body []byte, words string) string {
	t.Helper()
	doc, err := html.Parse(bytes.NewReader(body))
	require.NoError(t, err)
	var found []string
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "a" && shownWords(n) == words {
			found = append(found, attr(n, "href"))
		}
	}
	require.Len(t, found, 1, "one link %q", words)
	return found[0]
}

// The feed shows its newest 50 lines, oldest first, and "Show earlier" to
// ?feed=all, which shows every line (R167): a feed of 60 shows the newest
// 50, then 60; any other value of feed is ignored; a feed of 50 shows them
// all, with no link.
func TestTheFeedShowsTheNewestFiftyThenShowEarlier(t *testing.T) {
	w := newWorld(t)
	b := w.browser(t, "vic", auth.Viewer)
	f := &feedFixture{id: "p1"}
	var all []string
	for i := range 60 {
		all = append(all, f.entry(agentA, &board.Body{Task: &board.Task{Title: "task " + strconv.Itoa(i+1)}}))
	}
	w.boards.set(f.id, f.read())

	page := b.Get(w.public + "/items/p1")

	require.Equal(t, http.StatusOK, page.Resp.StatusCode)
	assert.Equal(t, all[10:], feedAnchors(t, page.Body), "the newest 50, oldest first")
	assert.Equal(t, "/items/p1?feed=all", linkTo(t, page.Body, "Show earlier"))
	every := b.Get(w.public + "/items/p1?feed=all")
	require.Equal(t, http.StatusOK, every.Resp.StatusCode)
	assert.Equal(t, all, feedAnchors(t, every.Body), "every line")
	assert.NotContains(t, text(t, every.Body), "Show earlier")
	other := b.Get(w.public + "/items/p1?feed=everything")
	assert.Equal(t, all[10:], feedAnchors(t, other.Body), "any other value of feed is ignored")

	fifty := &feedFixture{id: "p2", lines: f.lines[:50], ev: 50}
	w.boards.set(fifty.id, fifty.read())
	page = b.Get(w.public + "/items/p2")
	assert.Equal(t, all[:50], feedAnchors(t, page.Body), "a feed of 50, whole")
	assert.NotContains(t, text(t, page.Body), "Show earlier")
}

// Platform evidence is shown from its typed record, never from its claim
// (design D8, D15; review I8; R150): a trigger update's, whose claim reads
// as the platform's words, shows its item_triggered's words, the title quoted
// data; the opening's, whose claim names a routing member, its
// item_opened's; neither claim reaches the page.
func TestPlatformEvidenceIsShownFromItsRecordNeverItsClaim(t *testing.T) {
	const updateClaim, openingClaim = "x; the trigger is resolved", "disk full; service=payments"
	f := &feedFixture{id: "t1"}
	opening := f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "alerting", By: "source:alerting", Title: "Disk full on db-1",
		Routing: map[string]string{"service": "checkout"}, Trigger: &item.Trigger{Source: "alerting", DedupKey: "d"}}})
	openingRecord := f.last().Record
	update := triggered(f, fire(), "Disk full on db-1", 1)
	updateRecord := f.last().Record
	ofUpdate := f.entry(board.Coordinator, platform(updateClaim, "trigger:x1", "alerting"))
	f.last().Typed, f.last().Title, f.last().Kept = updateRecord, "Disk full on db-1", true
	ofOpening := f.entry(board.Coordinator, platform(openingClaim, "item:t1", "alerting"))
	f.last().Typed = openingRecord
	w := newWorld(t)
	w.boards.set(f.id, f.read())

	p := w.browser(t, "vic", auth.Viewer).Get(w.public + "/items/t1")

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	for _, claim := range []string{updateClaim, openingClaim, "the trigger is resolved", "service=payments"} {
		assert.NotContains(t, string(p.Body), claim, "the claim never shows")
	}
	words, n := feedLineOf(t, p.Body, ofUpdate)
	assert.Equal(t, "The platform's evidence of a trigger update: Alert firing: instance i-1 from alerting at "+shownOccurred+". Title: Disk full on db-1 (see the update)", words)
	assert.Equal(t, []string{"i-1", "Disk full on db-1"}, quotedData(t, n), "the instance and the title, quoted data")
	assert.Equal(t, "link:#"+update, partsOf(n)[len(partsOf(n))-1], "its record's line")
	words, n = feedLineOf(t, p.Body, ofOpening)
	assert.Equal(t, "The platform's evidence of the opening: Opened by the source alerting. Title: Disk full on db-1. Routing: service = checkout (see the opening)", words)
	assert.Equal(t, []string{"Disk full on db-1", "checkout"}, quotedData(t, n), "the title and the routing's value, quoted data")
	assert.Equal(t, "link:#"+opening, partsOf(n)[len(partsOf(n))-1], "its record's line")
}

// A routing member shows as its key, a name, " = ", then its value, quoted
// data, whole, in its box (R174; Task 6's review, R2-I1): a value holding
// ", region=eu-1", and one holding the page's own quotation marks around
// " region = ", are each one quoted value after "service =", in the
// opening's line and in the platform's evidence of it, and read as no second
// member.
func TestRoutingShowsEachMembersKeyAndQuotedValue(t *testing.T) {
	for i, value := range []string{"checkout, region=eu-1", "checkout\u201d, region = \u201ceu-1"} {
		id := item.ID("o" + strconv.Itoa(i))
		f := &feedFixture{id: id}
		opening := f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Checkout fails",
			Routing: map[string]string{"service": value}}})
		ofOpening := f.entry(board.Coordinator, platform("opened", "item:"+string(id), "person"))
		f.last().Typed = f.lines[0].Record
		w := newWorld(t)
		w.boards.set(f.id, f.read())

		p := w.browser(t, "vic", auth.Viewer).Get(w.public + "/items/" + string(id))

		require.Equal(t, http.StatusOK, p.Resp.StatusCode)
		for _, anchor := range []string{opening, ofOpening} {
			words, n := feedLineOf(t, p.Body, anchor)
			assert.Contains(t, words, "Routing: service = "+value, anchor)
			parts := partsOf(n)
			at := slices.Index(parts, "name:service")
			require.GreaterOrEqual(t, at, 0, "%s: the key, a name", anchor)
			require.Less(t, at+1, len(parts))
			assert.Equal(t, "data:"+value, parts[at+1], "%s: its value, one quoted value", anchor)
			assert.Len(t, parts, at+2+linksIn(parts[at+2:]), "%s: no second member after it", anchor)
			assert.NotContains(t, parts, "name:region", "%s: no second member", anchor)
			assert.Equal(t, []string{"Checkout fails", value}, quotedData(t, n), anchor)
		}
		assert.Contains(t, string(p.Body), `<bdi>service</bdi> = <q class="data"><bdi>`+html.EscapeString(value)+`</bdi></q>`)
	}
}

// linksIn counts the links among parts.
func linksIn(parts []string) int {
	n := 0
	for _, p := range parts {
		if strings.HasPrefix(p, "link:") {
			n++
		}
	}
	return n
}

// A trigger update's title the inbound store no longer keeps, past its 30
// days, says so (design D8; ruling 8), in its line and in the platform's
// evidence of it, with no title as data; one it keeps shows as quoted data.
func TestATriggerUpdatesTitleNoLongerKeptSaysSo(t *testing.T) {
	const notKept = "Title: no longer kept (alert titles are kept 30 days)"
	f := &feedFixture{id: "k1"}
	gone := triggered(f, fire(), "", 1)
	goneRecord := f.last().Record
	kept := fire()
	kept.EventID = "x2"
	keeps := triggered(f, kept, "Disk full", 1)
	ofGone := f.entry(board.Coordinator, platform("the alert fired", "trigger:x1", "alerting"))
	f.last().Typed = goneRecord
	w := newWorld(t)
	w.boards.set(f.id, f.read())

	p := w.browser(t, "vic", auth.Viewer).Get(w.public + "/items/k1")

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	words, n := feedLineOf(t, p.Body, gone)
	assert.True(t, strings.HasSuffix(words, ". "+notKept), words)
	assert.Equal(t, []string{"i-1"}, quotedData(t, n), "no title as data")
	words, n = feedLineOf(t, p.Body, ofGone)
	assert.Contains(t, words, ". "+notKept+" (see the update)")
	assert.Equal(t, []string{"i-1"}, quotedData(t, n), "no title as data")
	words, n = feedLineOf(t, p.Body, keeps)
	assert.True(t, strings.HasSuffix(words, ". Title: Disk full"), words)
	assert.Equal(t, []string{"i-1", "Disk full"}, quotedData(t, n))
}

// linksOf are the paths of the links of n.
func linksOf(n *html.Node) []string {
	var out []string
	for _, p := range partsOf(n) {
		if l, ok := strings.CutPrefix(p, "link:"); ok {
			out = append(out, l)
		}
	}
	return out
}

// A provenance link leads to its raw output or to its record's line (design
// D8, D10; R152): an agent's evidence links its call's raw output for the
// item's members alone, a viewer's line has no link, nor has a person's
// evidence; the platform's evidence links its typed record's line,
// #rec-<seq>, on the page, or the whole feed's, ?feed=all, when the page
// leaves that line out.
func TestAProvenanceLinkLeadsToItsRawOutputOrItsRecord(t *testing.T) {
	f := &feedFixture{id: "l1"}
	opening := f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Checkout fails"}})
	openingRecord := f.last().Record
	update := triggered(f, fire(), "Disk full", 1)
	updateRecord := f.last().Record
	agents := f.entry(agentA, readEvidence("the disk is full", "call-1", "disk full", "logs/search", "", ""))
	persons := f.entry(bob, &board.Body{Evidence: &board.Evidence{Claim: "it is full", Provenance: board.Provenance{Kind: board.FromHuman, Source: board.HumanObservation}}})
	ofOpening := f.entry(board.Coordinator, platform("opened", "item:l1", "person"))
	f.last().Typed = openingRecord
	ofUpdate := f.entry(board.Coordinator, platform("fired", "trigger:x1", "alerting"))
	f.last().Typed, f.last().Title, f.last().Kept = updateRecord, "Disk full", true
	w := newWorld(t)
	w.boards.set(f.id, f.read())

	member := w.browser(t, "carol", auth.Responder).Get(w.public + "/items/l1")
	viewer := w.browser(t, "vic", auth.Viewer).Get(w.public + "/items/l1")

	require.Equal(t, http.StatusOK, member.Resp.StatusCode)
	require.Equal(t, http.StatusOK, viewer.Resp.StatusCode)
	_, n := feedLineOf(t, member.Body, agents)
	assert.Equal(t, []string{"/items/l1/calls/call-1"}, linksOf(n), "a member's: its raw output")
	_, n = feedLineOf(t, viewer.Body, agents)
	assert.Empty(t, linksOf(n), "a viewer's: no link")
	for _, l := range links(t, viewer.Body) {
		assert.NotContains(t, l, "/calls/", "no raw output's link for a viewer")
	}
	_, n = feedLineOf(t, member.Body, persons)
	assert.Empty(t, linksOf(n), "a person's evidence")
	_, n = feedLineOf(t, viewer.Body, ofOpening)
	assert.Equal(t, []string{"#" + opening}, linksOf(n), "the opening's line")
	_, n = feedLineOf(t, viewer.Body, ofUpdate)
	assert.Equal(t, []string{"#" + update}, linksOf(n), "the update's line")

	far := &feedFixture{id: "l2"}
	farOpening := far.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Checkout fails"}})
	for i := range 50 {
		far.entry(agentA, &board.Body{Task: &board.Task{Title: "task " + strconv.Itoa(i+1)}})
	}
	farEvidence := far.entry(board.Coordinator, platform("opened", "item:l2", "person"))
	far.last().Typed = far.lines[0].Record
	w.boards.set(far.id, far.read())
	page := w.browser(t, "vic", auth.Viewer).Get(w.public + "/items/l2")
	assert.NotContains(t, feedAnchors(t, page.Body), farOpening, "the page leaves the opening's line out")
	_, n = feedLineOf(t, page.Body, farEvidence)
	assert.Equal(t, []string{"/items/l2?feed=all#" + farOpening}, linksOf(n), "the whole feed's line")
}

// A firing resume is a divider of the feed, the one line of its resume
// (design D8, spec 10.2 row 10; R149); a declined one says why in the
// platform's words, and a code the page has no words for as itself.
func TestAFiringResumeIsADividerAndADeclinedOneSaysWhy(t *testing.T) {
	f := &feedFixture{id: "r1"}
	resume := fire()
	resume.Resume = item.ResumeResumed
	resumed := triggered(f, resume, "Disk full", 1)
	decline := fire()
	decline.EventID, decline.Resume = "x2", item.ResumeDeclined+app.CodeNoAgentsLeft
	declined := triggered(f, decline, "Disk full", 1)
	later := fire()
	later.EventID, later.Resume = "x3", item.ResumeDeclined+"budget_gone"
	unknown := triggered(f, later, "Disk full", 1)
	w := newWorld(t)
	w.boards.set(f.id, f.read())

	p := w.browser(t, "vic", auth.Viewer).Get(w.public + "/items/r1")

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	words, n := feedLineOf(t, p.Body, resumed)
	assert.Equal(t, "Reopened after a flap: the alert fired again at "+shownOccurred+" (instance i-1 from alerting). Title: Disk full", words)
	assert.Equal(t, "divider", attr(n, "class"), "a divider")
	words, n = feedLineOf(t, p.Body, declined)
	assert.Equal(t, "The alert fired again; the agents did not resume: no agent is left to take part. Title: Disk full", words)
	assert.Empty(t, attr(n, "class"), "no divider")
	words, n = feedLineOf(t, p.Body, unknown)
	assert.Equal(t, "The alert fired again; the agents did not resume: budget_gone. Title: Disk full", words)
	assert.Equal(t, []string{"name:budget_gone", "data:Disk full"}, partsOf(n))
}

// The feed is the last of the board's regions, and open, so that a page
// with no script shows it whole on a phone, last (design D1, D12): its
// <details id="feed-list"> is open, its summary the feed's heading, an h2
// as every other region's.
func TestTheFeedIsLastAndOpen(t *testing.T) {
	r := richRead("z1", "")
	f := &feedFixture{id: "z1"}
	f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Checkout fails"}})
	r.Board.Feed = f.lines
	w := newWorld(t)
	w.boards.set("z1", r)

	p := w.browser(t, "vic", auth.Viewer).Get(w.public + "/items/z1")

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	doc, err := html.Parse(bytes.NewReader(p.Body))
	require.NoError(t, err)
	var order []string
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && slices.Contains(regionNames, attr(n, "id")) {
			order = append(order, attr(n, "id"))
		}
	}
	assert.Equal(t, regionNames, order, "the regions in the page's order")
	assert.Equal(t, "feed", order[len(order)-1], "the feed last")
	list := node(t, p.Body, "feed-list")
	require.NotNil(t, list, "the feed's list")
	assert.Equal(t, "details", list.Data)
	assert.True(t, hasAttr(list, "open"), "open")
	assert.Equal(t, "feed", attr(list.Parent, "id"), "inside the feed's region")
	summary := list.FirstChild
	for summary != nil && summary.Type != html.ElementNode {
		summary = summary.NextSibling
	}
	require.NotNil(t, summary)
	assert.Equal(t, "summary", summary.Data)
	assert.Equal(t, "Feed", shownWords(summary))
	heading := summary.FirstChild
	require.NotNil(t, heading, "the summary holds the feed's heading")
	assert.Equal(t, "h2", heading.Data, "the feed's heading, as every other region's (Task 6's review, R1-M3)")
	assert.Equal(t, "Feed", nodeText(heading))
}

// The board is whole without JavaScript (design D15): every region of the
// board holds its content as served, the switch's, the header's, the
// summary's, the strip's, the hypotheses' and the feed's, a leader's standby
// notice left empty; the page holds no script; and its live bar says "This
// page does not refresh itself: reload it to see more.", shown, its controls
// hidden.
func TestTheBoardIsCompleteWithoutJavaScript(t *testing.T) {
	r := richRead("j1", "")
	f := &feedFixture{id: "j1"}
	f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Checkout fails"}})
	f.entry(agentA, readEvidence("the disk is full", "call-1", "disk full on db-1", "logs/search", "", ""))
	r.Board.Feed = f.lines
	w := newWorld(t)
	w.boards.set("j1", r)

	p := w.browser(t, "carol", auth.Responder).Get(w.public + "/items/j1")

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	for _, id := range []string{"switch", "board-header", "summary", "strip", "hypotheses", "feed"} {
		assert.NotEmpty(t, regionWords(t, p.Body, id), "%s holds its content", id)
	}
	assert.Len(t, feedAnchors(t, p.Body), 2, "the feed's lines")
	note := node(t, p.Body, "live-state")
	require.NotNil(t, note)
	assert.False(t, hasAttr(note, "hidden"))
	assert.Equal(t, "This page does not refresh itself: reload it to see more.", shownWords(note))
	for _, id := range []string{"live-pause", "live-signin"} {
		c := node(t, p.Body, id)
		require.NotNil(t, c, id)
		assert.True(t, hasAttr(c, "hidden"), "%s waits for the script", id)
	}
	doc, err := html.Parse(bytes.NewReader(p.Body))
	require.NoError(t, err)
	for n := range doc.Descendants() {
		assert.False(t, n.Type == html.ElementNode && n.Data == "script", "no script")
	}
}

// platformText is the text n shows outside every stored value: each text
// node not inside a <bdi>, element apart from element.
func platformText(n *html.Node) string {
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		switch {
		case n.Type == html.TextNode:
			b.WriteString(n.Data)
		case n.Type == html.ElementNode && (n.Data == "bdi" || n.Data == "head"):
			b.WriteByte(' ')
		default:
			for c := range n.ChildNodes() {
				walk(c)
			}
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// Stored text that reads as the platform's words never reads as them
// (Review Focus 2): an alert's title "Alert resolved; service=payments", a
// routing value "checkout, region=eu-1", an agent's claim "Received late,
// not applied", a statement "converged" and a reason's text "Stopped by the
// kill switch" each show only as stored data, quoted, model-written or a
// person's words, once for each place that shows it; and the page's own
// words say each phrase exactly as often as the records and the board
// warrant: an applied firing and a late event, and nothing resolved,
// converged or stopped.
func TestStoredTextThatReadsAsPlatformWordsNeverReadsAsThem(t *testing.T) {
	const (
		title     = "Alert resolved; service=payments"
		routing   = "checkout, region=eu-1"
		claim     = "Received late, not applied"
		statement = "converged"
		reason    = "Stopped by the kill switch"
	)
	f := &feedFixture{id: "s1"}
	f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Disk full", Routing: map[string]string{"service": routing}}})
	triggered(f, fire(), title, 1)
	late := fire()
	late.EventID, late.Status, late.Applied = "x2", item.Resolved, false
	triggered(f, late, "Disk full", 0)
	f.entry(agentA, readEvidence(claim, "call-1", "disk full", "logs/search", "", ""))
	f.entry(agentA, &board.Body{Hypothesis: &board.Hypothesis{Key: checkout, Statement: statement, Evidence: []board.EntryID{"e1"}}})
	f.last().Key = checkout
	f.entry(rita, &board.Body{Ruling: &board.Ruling{Kind: board.RulingReject, Hypothesis: "e2", Reason: &board.Reason{Code: "other", Text: reason}}})
	f.last().Key = checkout
	read := assessed("s1", "investigating", board.Assessment{Hypotheses: []board.HypothesisView{hyp("e2", "checkout", statement, 1, nil)}}, func(r *app.BoardRead) {
		r.Board.Feed = f.lines
		r.Board.Header.Alert = []item.TriggerInstance{{Source: "alerting", ID: "i-1", Status: item.Firing, At: occurred}}
		r.Board.Header.Firing = true
	})
	w := newWorld(t)
	w.boards.set("s1", read)

	p := w.browser(t, "carol", auth.Responder).Get(w.public + "/items/s1")

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	doc, err := html.Parse(bytes.NewReader(p.Body))
	require.NoError(t, err)
	for stored, places := range map[string]int{title: 1, routing: 1, claim: 1, statement: 2, reason: 1} {
		shown := 0
		for n := range doc.Descendants() {
			if n.Type != html.TextNode || n.Parent == nil || n.Parent.Data != "bdi" || !strings.Contains(n.Data, stored) {
				continue
			}
			shown++
			assert.Contains(t, []string{"data", "model", "person"}, kindOf(n.Parent), "%q shows as stored data", stored)
		}
		assert.Equal(t, places, shown, "%q, once for each place that shows it", stored)
	}
	words := platformText(doc)
	for phrase, warranted := range map[string]int{
		"Alert firing":               1,
		"Received late, not applied": 1,
		"Alert resolved":             0,
		"service=payments":           0,
		"region=eu-1":                0,
		"converged":                  0,
		"Stopped by the kill switch": 0,
	} {
		assert.Equal(t, warranted, strings.Count(words, phrase), "the page's own words say %q as often as warranted", phrase)
	}
}

// A person's words that start a sentence say so with a capital, "A person's
// words:" (the words of the board, "The board's entries in the feed"; Task
// 6's review, D6), and stay lower case elsewhere, as after a comma; every
// person's value sits in its own <span class="person">, its prefix outside it
// (R2-I1).
func TestAPersonsWordsAtASentencesStartAreCapitalized(t *testing.T) {
	r := richRead("w1", "")
	f := &feedFixture{id: "w1"}
	evidence := f.entry(bob, &board.Body{Evidence: &board.Evidence{Claim: "the deploy went out",
		Provenance: board.Provenance{Kind: board.FromHuman, Source: board.HumanObservation}}})
	r.Board.Feed = f.lines
	w := newWorld(t)
	w.boards.set("w1", r)

	p := w.browser(t, "vic", auth.Viewer).Get(w.public + "/items/w1")

	require.Equal(t, http.StatusOK, p.Resp.StatusCode)
	words, n := feedLineOf(t, p.Body, evidence)
	assert.Equal(t, "sso:bob posted evidence. A person's words: the deploy went out", words, "a sentence's start")
	assert.Equal(t, []string{"name:sso:bob", "person:the deploy went out"}, partsOf(n))
	assert.Contains(t, regionWords(t, p.Body, "hypotheses"), "sso:bob wrote, a person's words: I saw the release go out", "after a comma")
	doc, err := html.Parse(bytes.NewReader(p.Body))
	require.NoError(t, err)
	people := 0
	for d := range doc.Descendants() {
		if d.Type == html.ElementNode && d.Data == "bdi" && strings.HasSuffix(textBefore(d), "person's words: ") {
			t.Errorf("a person's words outside their <span class=\"person\">: %q", nodeText(d))
		}
		if d.Type == html.ElementNode && d.Data == "span" && attr(d, "class") == "person" {
			people++
			assert.Equal(t, "person", kindOf(d.FirstChild), "its one child, the <bdi>, after its prefix")
		}
	}
	assert.Equal(t, 2, people, "the feed's line and the card's quote")
}

// textBefore is the text of the node just before n, "" when it is no text.
func textBefore(n *html.Node) string {
	if b := n.PrevSibling; b != nil && b.Type == html.TextNode {
		return b.Data
	}
	return ""
}

// "Sign in again", the live bar's link once a session ended, returns to the
// board as the page shows it: ?feed=all kept, which the sign-in's return
// check takes; any other value of feed, which the page ignores, left out
// (Task 6's review, R1-S).
func TestSignInAgainReturnsToTheBoardAsShown(t *testing.T) {
	f := &feedFixture{id: "g1"}
	f.record(&item.Body{Opened: &item.Opened{Type: "incident", Source: "person", By: "sso:carol", Title: "Checkout fails"}})
	w := newWorld(t)
	w.boards.set(f.id, f.read())
	b := w.browser(t, "vic", auth.Viewer)

	for query, back := range map[string]string{"": "/items/g1", "?feed=all": "/items/g1?feed=all", "?feed=other": "/items/g1"} {
		p := b.Get(w.public + "/items/g1" + query)

		require.Equal(t, http.StatusOK, p.Resp.StatusCode, query)
		link := node(t, p.Body, "live-signin")
		require.NotNil(t, link, query)
		assert.Equal(t, auth.SignInPath+"?return="+url.QueryEscape(back), attr(link, "href"), "%q returns to %s", query, back)
		safe, ok := auth.SafeReturn(back)
		assert.True(t, ok, "%s is a return the sign-in takes", back)
		assert.Equal(t, back, safe)
	}
}
