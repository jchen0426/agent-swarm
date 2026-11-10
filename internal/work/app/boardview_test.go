package app_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/holding"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
	"github.com/jchen0426/agent-swarm/internal/work/key"
)

// waitsOf are the waits of a round's end under h's options, as serve gives
// them to the views.
func waitsOf(h *harness) item.Waits {
	return item.Waits{Grace: h.opts.Grace, Settle: h.opts.SettleWait, Poll: h.opts.SettlePoll, Retry: h.opts.Retry}
}

// boardViews are viewsOf's views, with the waits of h's options and the
// inbound store's titles.
func boardViews(h *harness) *app.Views {
	return viewsWith(h, func(p *app.ViewPorts) { p.Waits, p.Titles = waitsOf(h), h.records })
}

// boardOf reads an item's board at the latest version.
func boardOf(t *testing.T, views *app.Views, id item.ID) *app.BoardView {
	t.Helper()
	bv, err := views.Board(t.Context(), id, app.BoardVersion{})
	require.NoError(t, err)
	return bv
}

// acting is a context of the harness's holding, as every write carries one.
func acting(h *harness) context.Context { return holding.WithEpoch(h.t.Context(), h.epoch) }

// readCall records agent's read call of source, server/tool, in run, which
// answered output, as the MCP gateway records one, and returns its id.
func readCall(h *harness, run board.RunID, agent string, source board.Source, output string) string {
	h.t.Helper()
	server, tool, ok := strings.Cut(string(source), "/")
	require.True(h.t, ok, "a source is server/tool")
	id := h.ids.NewID()
	require.NoError(h.t, h.audit.Begin(h.t.Context(), &actionsapp.CallRecord{ID: id, Run: run, Agent: agent, Server: server, Tool: tool, Args: json.RawMessage(`{}`)}))
	require.NoError(h.t, h.audit.End(h.t.Context(), &actionsapp.Outcome{ID: id, Status: actionsapp.StatusOK, Output: output}))
	return id
}

// postEvidence posts an agent's evidence in the run's current round, quoting
// a read call of source that answered quote, with a stance on hyp when
// stance is set, and returns it.
func postEvidence(h *harness, run board.RunID, agent board.Author, source board.Source, quote string, hyp board.EntryID, stance board.Stance) board.Event {
	h.t.Helper()
	call := readCall(h, run, agent.Name, source, quote)
	posted, err := h.boards.PostEvidence(acting(h), run, 0, agent, &board.EvidenceInput{
		Claim: "the read shows " + quote, CallID: call, Quote: quote, Hypothesis: hyp, Stance: stance,
	})
	require.NoError(h.t, err)
	return posted.Event
}

// postHypothesis posts an agent's hypothesis of k, citing evidence, and
// returns its id.
func postHypothesis(h *harness, run board.RunID, agent board.Author, k key.Key, evidence ...board.EntryID) board.EntryID {
	h.t.Helper()
	posted, err := h.boards.PostHypothesis(acting(h), run, 0, agent, &board.HypothesisInput{Key: k, Statement: "it is " + k.String(), Evidence: evidence})
	require.NoError(h.t, err)
	return posted.Event.ID
}

// Hypothesis keys of the harness's incident type.
var (
	checkoutRelease = key.Key{"service": "checkout", "failureMode": "bad-release"}
	checkoutConfig  = key.Key{"service": "checkout", "failureMode": "config-change"}
	paymentsConfig  = key.Key{"service": "payments", "failureMode": "config-change"}
)

// on is a target or a scope of one argument, service.
func on(service string) []board.TargetValue {
	return []board.TargetValue{{Arg: "service", Value: service}}
}

// propose posts the gateway's proposal of proposer's call of tool, citing
// hyp, with the target and scope given, and returns its id.
func propose(h *harness, run board.RunID, proposer board.Author, hyp board.EntryID, tool string, target []board.TargetValue, scope map[string]string) board.EntryID {
	h.t.Helper()
	call := h.ids.NewID()
	posted, err := h.boards.Propose(acting(h), run, 0, &board.Proposal{
		Proposer: proposer.Name, Tool: tool, SchemaHash: "sha256:" + strings.Repeat("c", 64), ArgsHash: sha256Of(call), Hypothesis: hyp,
		Target: target, Scope: scope, Summary: []board.SummaryPart{{Text: "Act on "}, {Text: "checkout", Value: true}}, CallID: call,
	})
	require.NoError(h.t, err)
	return posted.Event.ID
}

// notice posts a platform notice in the run's current round.
func notice(h *harness, run board.RunID, n *board.Notice) {
	h.t.Helper()
	_, err := h.boards.Notice(acting(h), run, board.Coordinator, n)
	require.NoError(h.t, err)
}

// reject has alice reject hyp on the run's board.
func reject(h *harness, run board.RunID, hyp board.EntryID) {
	h.t.Helper()
	_, err := h.boards.Rule(acting(h), run, alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: hyp, Reason: &board.Reason{Code: "not_the_cause"}})
	require.NoError(h.t, err)
}

// cardOf returns the card of hypothesis id among a board's, ruled out or
// not.
func cardOf(t *testing.T, bv *app.BoardView, id board.EntryID) *app.HypothesisCard {
	t.Helper()
	for _, cards := range [][]app.HypothesisCard{bv.Hypotheses, bv.RuledOut} {
		for i := range cards {
			if cards[i].ID == id {
				return &cards[i]
			}
		}
	}
	require.FailNow(t, "no card of the hypothesis", id)
	return nil
}

// lineOf returns the feed's first line that is holds true of.
func lineOf(t *testing.T, bv *app.BoardView, is func(l *app.FeedLine) bool) *app.FeedLine {
	t.Helper()
	for i := range bv.Feed {
		if is(&bv.Feed[i]) {
			return &bv.Feed[i]
		}
	}
	require.FailNow(t, "no such line in the feed")
	return nil
}

// recordLine reports whether a line is a record's of kind.
func recordLine(kind item.Kind) func(*app.FeedLine) bool {
	return func(l *app.FeedLine) bool { return l.Record != nil && l.Record.Body.Kind() == kind }
}

// entryLine reports whether a line is the entry's of id.
func entryLine(id board.EntryID) func(*app.FeedLine) bool {
	return func(l *app.FeedLine) bool { return l.Event != nil && l.Event.ID == id }
}

// triggeredLine reports whether a line is the item_triggered record's of
// event id.
func triggeredLine(id string) func(*app.FeedLine) bool {
	return func(l *app.FeedLine) bool {
		return l.Record != nil && l.Record.Body.Triggered != nil && l.Record.Body.Triggered.EventID == id
	}
}

// A board's header holds what the records and the board determine (plan
// review I1): the item's title; the assessment's conclusion, with its status
// now, confirming while its wrap-up round is open; the budget; the number of
// suggestions; each instance of the alert with its last status, and whether
// it fires; the later of the latest record's and entry's times; the closed
// item a flap's opening linked to; and the notices of the latest round. Here
// a flap of a closed item converges in round 1, and in its wrap-up round a
// proposal and two notices are posted, a second instance fires and the first
// resolves. The lifecycle's facts that the records determine are its view's
// (R169): an item not staffed by its routing, one left not staffed by an
// on-row, an item closed by a person and one closed by itself, each closed
// item with its windows, and one awaiting its close while its alert fires,
// which stays open; nothing of the kill switch or of the queue is.
func TestABoardViewHoldsItsHeadersFacts(t *testing.T) {
	h := newHarness(t)
	p := closedFor(h, 100)
	n := ingest(h, trigger("f2", "i", "d", item.Firing, h.clock.Now())).Item
	run := h.state(n).Run.ID
	notice(h, run, &board.Notice{Code: board.NoticeModelUnknown, Agent: runtime.Name, CallID: "call-0"})
	h.scripts[release.Name], h.scripts[runtime.Name] = hypothesis, hypothesis
	h.round()
	require.Equal(t, 2, h.state(n).Run.Latest().Opened.Round)
	wrapUp := h.state(n).Run.Latest().Opened
	require.NotEmpty(t, wrapUp.WrapUp, "round 2 is the wrap-up round")
	propose(h, run, release, wrapUp.WrapUp, "deploys.rollback", on("checkout"), nil)
	notice(h, run, &board.Notice{Code: board.NoticeModelFailed, Agent: release.Name, CallID: "call-1"})
	h.clock.Advance(time.Second)
	fired := h.clock.Now()
	require.Equal(t, app.IngestAppended, ingest(h, trigger("f3", "i2", "d", item.Firing, fired)).Outcome)
	h.clock.Advance(time.Second)
	resolved := h.clock.Now()
	require.Equal(t, app.IngestAppended, ingest(h, trigger("r1", "i", "d", item.Resolved, resolved)).Outcome)
	h.clock.Advance(time.Second)
	notice(h, run, &board.Notice{Code: board.NoticeModelFailed, Agent: release.Name, CallID: "call-2"})

	bv := boardOf(t, boardViews(h), n)

	s, records, events := h.state(n), recordsOf(h, n), h.events(run)
	head := bv.Header
	assert.Equal(t, "checkout fails", head.Title)
	require.NotNil(t, head.Conclusion, "the assessment concludes")
	assert.Equal(t, wrapUp.WrapUp, head.Conclusion.ID)
	assert.Equal(t, board.StatusConfirming, head.Conclusion.Status, "its wrap-up round is open")
	assert.Equal(t, &app.BudgetView{Budget: s.Run.Budget(), Used: usedBy(s.Run)}, bv.View.Budget)
	assert.Equal(t, 1, head.Suggestions)
	assert.Equal(t, []item.TriggerInstance{
		{Source: "alerting", ID: "i", Status: item.Resolved, At: resolved},
		{Source: "alerting", ID: "i2", Status: item.Firing, At: fired},
	}, head.Alert)
	assert.True(t, head.Firing)
	assert.False(t, head.StaysOpen, "it investigates")
	last := records[len(records)-1].At
	require.True(t, events[len(events)-1].At.After(last), "the latest entry is later than the latest record")
	assert.Equal(t, events[len(events)-1].At, head.Updated)
	assert.Equal(t, p, head.FlapOf)
	assert.Equal(t, 2, head.Notices, "round 2's, each counted")
	assert.Equal(t, []int{2, 1}, []int{head.Round, head.Ended})
	assert.Equal(t, s.Run.StopBy(last, waitsOf(h)), head.StopBy)
	assert.False(t, head.StopBy.IsZero())
	assert.Equal(t, wrapUp.Deadline.Add(h.opts.Grace+h.opts.SettleWait+h.opts.SettlePoll), head.ConfirmingUntil)
	assert.Equal(t, app.BoardVersion{
		Record: app.Mark{ID: records[len(records)-1].ID, Seq: records[len(records)-1].Seq},
		Run:    run,
		Board:  app.Mark{ID: string(events[len(events)-1].ID), Seq: events[len(events)-1].Seq},
	}, bv.Version)
	assert.Equal(t, *s.Opened, bv.Opened)

	for _, tc := range []struct {
		name string
		// item makes the row's item and returns it, with the view its board
		// gives of the item's place in the lifecycle.
		item func(h *harness) (item.ID, app.View)
		// staysOpen is the header's.
		staysOpen bool
	}{
		{"not staffed by its routing", func(h *harness) (item.ID, app.View) {
			h.revs.next(h.revision(strings.Repeat("b", 40), func(files map[string][]byte) {
				files["redaction-rules/services.yaml"] = []byte("kind: RedactionRule\nmetadata: {name: services, owner: security@example.com}\n" +
					"spec:\n  always: true\n  patterns: ['^checkout$']\n")
			}))
			id := h.open("checkout fails")
			return id, app.View{State: "awaiting_close", Stop: "not_staffed", NotStaffed: item.CauseRouting}
		}, false},
		{"left not staffed by an on-row", func(h *harness) (item.ID, app.View) {
			id := h.open("checkout fails")
			_, err := h.c.SetKillSwitch(h.t.Context(), true, killing(0))
			require.NoError(h.t, err)
			requireKilled(h.t, h, id)
			return id, app.View{State: "awaiting_close", Stop: "killed", NotStaffed: item.CauseKillSwitch}
		}, false},
		{"closed by a person", func(h *harness) (item.ID, app.View) {
			id := awaitingItsClose(h, "checkout fails")
			_, err := h.c.Close(h.t.Context(), id, closing("close-1"))
			require.NoError(h.t, err)
			at := h.state(id).ClosedAt
			return id, app.View{State: "closed", Stop: "quiet", Closed: &app.ClosedView{
				At: at, By: "admin", ReopenableUntil: at.Add(h.opts.ReopenWindow), LateRulingsUntil: at.Add(closedRulings),
			}}
		}, false},
		{"closed by itself", func(h *harness) (item.ID, app.View) {
			id := awaitingItsClose(h, "checkout fails")
			advanceTo(h, h.state(id).AwaitingSince.Add(closesAfter))
			at := h.state(id).ClosedAt
			require.Equal(h.t, item.StatusClosed, h.state(id).Status())
			return id, app.View{State: "closed", Stop: "quiet", Closed: &app.ClosedView{
				At: at, Auto: true, ReopenableUntil: at.Add(h.opts.ReopenWindow), LateRulingsUntil: at.Add(closedRulings),
			}}
		}, false},
		{"awaiting its close while its alert fires", func(h *harness) (item.ID, app.View) {
			id := ingest(h, trigger("f1", "i", "d", item.Firing, h.clock.Now())).Item
			h.round()
			return id, app.View{State: "awaiting_close", Stop: "quiet"}
		}, true},
		{"awaiting its close, its alert resolved", func(h *harness) (item.ID, app.View) {
			return resolvedAndStopped(h), app.View{State: "awaiting_close", Stop: "trigger_resolved"}
		}, false},
		{"investigating, its run stopped and its finish not ended (the plan's)", func(h *harness) (item.ID, app.View) {
			h.store.refuses(func(_ board.RunID, drafts []board.Draft) error {
				if stopEntryOf(drafts) {
					return errRefused
				}
				return nil
			})
			id := h.open("checkout fails")
			h.round()
			require.NotNil(h.t, h.state(id).Run.Stopped)
			return id, app.View{State: "investigating", Stop: "quiet"}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			id, want := tc.item(h)

			bv := boardOf(t, boardViews(h), id)

			s := h.state(id)
			got := bv.View
			assert.Equal(t, []any{want.State, want.Stop, want.NotStaffed, want.Closed}, []any{got.State, got.Stop, got.NotStaffed, got.Closed})
			assert.Equal(t, tc.staysOpen, bv.Header.StaysOpen)
			assert.Equal(t, []any{0, false, false, time.Time{}, false},
				[]any{got.QueuePosition, got.AutoClosePaused, got.AutoCloseUnread, got.ClosesAt, got.ClosesAtUnread},
				"no fact of the kill switch, a revision or the queue")
			assert.Nil(t, got.IncidentItems, "the strip's")
			assert.Zero(t, bv.Header.StopBy, "no time but while its run runs")
			assert.Equal(t, []any{id, s.Incident, "incident", s.OpenedAt}, []any{got.ID, got.Incident, got.Type, got.OpenedAt})
		})
	}
	t.Run("a person's link of a queued inbound item is no flap's (the plan's)", func(t *testing.T) {
		h := newHarness(t, limit(1))
		other := h.open("payments fail")
		x := ingest(h, trigger("f1", "i", "d", item.Firing, h.clock.Now())).Item
		require.Equal(t, []item.Kind{item.KindOpened, item.KindTriggered}, h.kinds(x), "it waits queued")
		_, err := h.c.Link(t.Context(), x, other, linking("link-1"))
		require.NoError(t, err)
		require.Equal(t, item.KindLinked, h.kinds(x)[2], "its third record")

		assert.Empty(t, boardOf(t, boardViews(h), x).Header.FlapOf)
	})
}

// A board's projection is its version's (R140): read at a version older
// than the latest, it holds nothing committed after the version's seqs, and
// is what the board was at that version; a version whose record, entry or
// run is not the item's at its seqs has moved, as after a restore; a zero
// version reads everything committed, and the view's version says what it
// read. An id with no records is no item.
func TestABoardViewIsTheProjectionOfItsVersion(t *testing.T) {
	h := newHarness(t)
	views := boardViews(h)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.scripts[release.Name] = hypothesis
	h.round()

	old := boardOf(t, views, id)

	records, events := recordsOf(h, id), h.events(run)
	assert.Equal(t, app.BoardVersion{
		Record: app.Mark{ID: records[len(records)-1].ID, Seq: records[len(records)-1].Seq},
		Run:    run,
		Board:  app.Mark{ID: string(events[len(events)-1].ID), Seq: events[len(events)-1].Seq},
	}, old.Version, "the latest record and entry")
	h.round()
	h.round()
	latest := boardOf(t, views, id)
	require.Greater(t, latest.Version.Record.Seq, old.Version.Record.Seq)
	require.Greater(t, latest.Version.Board.Seq, old.Version.Board.Seq)

	again, err := views.Board(t.Context(), id, old.Version)

	require.NoError(t, err)
	assert.Equal(t, old, again, "nothing after its seqs")
	for name, moved := range map[string]func(v *app.BoardVersion){
		"another record at its seq": func(v *app.BoardVersion) { v.Record.ID = "restored" },
		"another entry at its seq":  func(v *app.BoardVersion) { v.Board.ID = "restored" },
		"an entry the item has not": func(v *app.BoardVersion) { v.Board.Seq = latest.Version.Board.Seq + 1 },
		"a record the item has not": func(v *app.BoardVersion) { v.Record.Seq = latest.Version.Record.Seq + 1 },
		"another run":               func(v *app.BoardVersion) { v.Run = "another-run" },
		"the latest record at a seq it is not at": func(v *app.BoardVersion) {
			*v = latest.Version
			v.Record.Seq++
		},
		"the latest entry at a seq it is not at": func(v *app.BoardVersion) {
			*v = latest.Version
			v.Board.Seq++
		},
	} {
		v := old.Version
		moved(&v)
		_, err := views.Board(t.Context(), id, v)
		require.ErrorIs(t, err, app.ErrMoved, name)
	}
	_, err = views.Board(t.Context(), "no-such-item", app.BoardVersion{})
	require.ErrorIs(t, err, app.ErrNoItem)
}

// An item that holds a record of a later version, or whose run's board holds
// an event of one, is a board view with HeldByLaterBuild alone (Decision 19),
// as its item view is (the plan's).
func TestABoardViewOfALaterVersionIsHeldAlone(t *testing.T) {
	for name, later := range map[string]func(t *testing.T, h *harness, id item.ID){
		"a record of version 2":      func(t *testing.T, h *harness, id item.ID) { appendRaw(t, h, id, item.Body{V: item.Version + 1}) },
		"a board event of version 2": func(t *testing.T, h *harness, id item.ID) { appendLaterEvent(t, h, h.state(id).Run.ID) },
	} {
		h := newHarness(t)
		id := h.open("checkout fails")
		later(t, h, id)

		bv := boardOf(t, boardViews(h), id)

		assert.Equal(t, &app.BoardView{View: app.View{ID: id, HeldByLaterBuild: true}}, bv, name)
	}
}

// line names a feed line by its record's kind and seq, or its entry's kind
// and seq.
type line struct {
	kind string
	seq  int64
}

// linesOf names a feed's lines in order.
func linesOf(feed []app.FeedLine) []line {
	out := make([]line, 0, len(feed))
	for i := range feed {
		if r := feed[i].Record; r != nil {
			out = append(out, line{string(r.Body.Kind()), r.Seq})
		} else {
			out = append(out, line{"entry:" + string(feed[i].Event.Body.Kind()), feed[i].Event.Seq})
		}
	}
	return out
}

// The feed merges the item's records and its run's entries by their
// database times; at equal time the records come first, then each store's
// own seq (design D8). Here the item opens, and its records and entries
// interleave as the two stores stamped them; then both stores read every row
// at one time, as Postgres's greatest(clock_timestamp(), previous) may stamp
// them.
func TestTheFeedMergesRecordsAndEntriesByDatabaseTime(t *testing.T) {
	h := newHarness(t)
	views := boardViews(h)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.clock.Advance(time.Second)
	e := postEvidence(h, run, release, "logs/search", "checkout v42 deployed at 10:02", "", "")

	feed := boardOf(t, views, id).Feed

	records, events := recordsOf(h, id), h.events(run)
	require.Equal(t, []item.Kind{item.KindOpened, item.KindStarted, item.KindRunCreated, item.KindRoundOpened}, h.kinds(id))
	require.Equal(t, records[0].At, events[0].At, "the opening and its evidence share a time")
	require.True(t, records[1].At.Equal(events[1].At), "so do the start and the round's entry")
	assert.Equal(t, []line{
		{"item_opened", 1}, {"entry:evidence", 1}, {"item_started", 2}, {"entry:round", 2}, {"run_created", 3}, {"entry:evidence", e.Seq},
	}, linesOf(feed), "by time, the record first at equal time")

	at := h.clock.Now()
	h.records.loads(func(records []item.Record) error {
		for i := range records {
			records[i].At = at
		}
		return nil
	})
	h.store.loads(func(events []board.Event) error {
		for i := range events {
			events[i].At = at
		}
		return nil
	})
	defer h.records.loads(nil)
	defer h.store.loads(nil)

	feed = boardOf(t, views, id).Feed

	assert.Equal(t, []line{
		{"item_opened", 1}, {"item_started", 2}, {"run_created", 3}, {"entry:evidence", 1}, {"entry:round", 2}, {"entry:evidence", e.Seq},
	}, linesOf(feed), "at one time, the records by seq, then the entries by seq")
	for i := range feed {
		assert.Equal(t, at, feed[i].At)
	}
}

// Rounds and stops are folded into the board's entries (R149): no line for
// a turn_end entry, nor for round_opened, round_ended, run_stopped or
// item_awaiting_close; the round's divider, its round entry, holds its round
// as the records hold it, and the stop entry is the stop's line.
func TestTheFeedFoldsRoundsAndStopsIntoTheBoardsEntries(t *testing.T) {
	h := newHarness(t)
	id := awaitingItsClose(h, "checkout fails")
	s := h.state(id)

	bv := boardOf(t, boardViews(h), id)

	assert.Equal(t, []line{
		{"item_opened", 1}, {"entry:evidence", 1}, {"item_started", 2}, {"entry:round", 2}, {"run_created", 3}, {"entry:stop", 5},
	}, linesOf(bv.Feed))
	assert.Equal(t, []board.Kind{board.KindEvidence, board.KindRound, board.KindTurnEnd, board.KindTurnEnd, board.KindStop}, kindsOf(h.events(s.Run.ID)))
	divider := lineOf(t, bv, func(l *app.FeedLine) bool { return l.Event != nil && l.Event.Body.Round != nil })
	require.NotNil(t, divider.Round)
	assert.Equal(t, s.Run.Rounds[0], *divider.Round, "its round, as the records hold it")
	for i := range bv.Feed {
		assert.Equal(t, 1, bv.Feed[i].Repeats, "a line stands for itself")
		if bv.Feed[i].Event == nil || bv.Feed[i].Event.Body.Round == nil {
			assert.Nil(t, bv.Feed[i].Round, "only a divider holds a round")
		}
	}
}

// kindsOf lists a board's entries by kind.
func kindsOf(events []board.Event) []board.Kind {
	out := make([]board.Kind, 0, len(events))
	for i := range events {
		out = append(out, events[i].Body.Kind())
	}
	return out
}

// A firing resume is one line, its item_triggered's (R149): its
// run_extended, cause firing, and the board's resume entry of cause firing
// make none. A declined one is its item_triggered's line, which keeps its
// code. A person's extension is a line, and so is its resume entry.
func TestAFiringResumeIsOneLineAndADeclinedOneKeepsItsCode(t *testing.T) {
	t.Run("resumed", func(t *testing.T) {
		h := newHarness(t)
		x := resolvedAndStopped(h)
		h.clock.Advance(time.Minute)
		ingest(h, trigger("f2", "i", "d", item.Firing, h.clock.Now()))
		requireResumedInto(t, h, x, 2)

		bv := boardOf(t, boardViews(h), x)

		resumed := lineOf(t, bv, triggeredLine("f2"))
		assert.Equal(t, item.ResumeResumed, resumed.Record.Body.Triggered.Resume)
		require.Len(t, resumeEntries(h, h.state(x).Run.ID), 1)
		for i := range bv.Feed {
			l := &bv.Feed[i]
			assert.False(t, l.Record != nil && l.Record.Body.Extended != nil, "no line of its run_extended")
			assert.False(t, l.Event != nil && l.Event.Body.Resume != nil, "no line of its resume entry")
		}
	})
	t.Run("declined", func(t *testing.T) {
		h := newHarness(t)
		x := resolvedAndStopped(h)
		h.spend.set(func(s *spend) { s.day = h.opts.DailyCap })
		h.clock.Advance(time.Minute)
		ingest(h, trigger("f2", "i", "d", item.Firing, h.clock.Now()))

		bv := boardOf(t, boardViews(h), x)

		declined := lineOf(t, bv, triggeredLine("f2"))
		assert.Equal(t, item.ResumeDeclined+app.CodeDailyCapReached, declined.Record.Body.Triggered.Resume, "it keeps its code")
	})
	t.Run("a person's extension", func(t *testing.T) {
		h := newHarness(t)
		id := awaitingItsClose(h, "checkout fails")
		_, err := h.c.Extend(t.Context(), id, rounds(1), extending("extend-1"))
		require.NoError(t, err)
		requireResumedInto(t, h, id, 2)

		bv := boardOf(t, boardViews(h), id)

		lineOf(t, bv, recordLine(item.KindExtended))
		resume := lineOf(t, bv, func(l *app.FeedLine) bool { return l.Event != nil && l.Event.Body.Resume != nil })
		assert.Equal(t, "extension", resume.Event.Body.Resume.Cause)
	})
}

// Platform evidence names its typed record (R150): the opening's, of
// item:<id>, its item_opened; a trigger update's, of trigger:<event id>, the
// item_triggered of its source and that event id. The same event id of
// another source matches nothing, nor does an entry that is no platform
// evidence.
func TestPlatformEvidenceNamesItsTypedRecord(t *testing.T) {
	h := newHarness(t)
	x := ingest(h, trigger("f1", "i", "d", item.Firing, h.clock.Now())).Item
	run := h.state(x).Run.ID
	h.clock.Advance(time.Second)
	require.Equal(t, app.IngestAppended, ingest(h, trigger("f2", "i2", "d", item.Firing, h.clock.Now())).Outcome)
	appendRaw(t, h, x, item.Body{V: item.Version, Triggered: &item.Triggered{
		Source: "pager", EventID: "f9", InstanceID: "p1", DedupKey: "dp", Status: item.Firing, OccurredAt: h.clock.Now(), Applied: true,
	}})
	elsewhere, err := h.boards.PostPlatformEvidence(acting(h), run, &board.PlatformEvidenceInput{Claim: "the alert fired", Ref: "trigger:f9", Source: "alerting"})
	require.NoError(t, err)
	agents := postEvidence(h, run, release, "logs/search", "checkout v42 deployed at 10:02", "", "")

	bv := boardOf(t, boardViews(h), x)

	records := recordsOf(h, x)
	evidenceOf := func(ref string) *app.FeedLine {
		return lineOf(t, bv, func(l *app.FeedLine) bool {
			return l.Event != nil && l.Event.Body.Evidence != nil && l.Event.Body.Evidence.Provenance.Ref == ref
		})
	}
	assert.Equal(t, &records[0], evidenceOf("item:"+string(x)).Typed, "the opening")
	update := evidenceOf("trigger:f2").Typed
	require.NotNil(t, update)
	assert.Equal(t, "f2", update.Body.Triggered.EventID)
	assert.Equal(t, "alerting", update.Body.Triggered.Source)
	assert.Nil(t, lineOf(t, bv, entryLine(elsewhere.Event.ID)).Typed, "pager's f9 is not alerting's")
	assert.Nil(t, lineOf(t, bv, entryLine(agents.ID)).Typed, "an agent's evidence")
}

// A feed line holds what its words need that its entry does not (the
// plan's; design D8): the key it names, a stance's, a hypothesis's, an
// alias's its hypothesis's, a ruling's, a dismissal's its refutation's
// hypothesis's, a proposal's, a wrap-up round's and a stop's conclusion's;
// when an evidence's entry was retracted; a claim's or a completion's task;
// a dismissed refutation's author, and a retraction's evidence's; and, of an
// applied item_triggered, the instances firing once it applied, as the
// records give them.
func TestAFeedLineHoldsWhatItsWordsNeed(t *testing.T) {
	t.Run("the entries of a round", func(t *testing.T) {
		h := newHarness(t)
		id := h.open("checkout fails")
		run := h.state(id).Run.ID
		ctx := acting(h)
		e1 := postEvidence(h, run, release, "logs/search", "checkout v42 deployed at 10:02", "", "")
		a := postHypothesis(h, run, release, checkoutRelease, e1.ID)
		alias := postHypothesis(h, run, runtime, checkoutRelease, postEvidence(h, run, runtime, "logs/search", "errors rose at 10:03", "", "").ID)
		e3 := postEvidence(h, run, runtime, "metrics/query", "error rate 12%", a, board.Supports)
		proposal := propose(h, run, runtime, alias, "deploys.rollback", on("checkout"), nil)
		e4 := postEvidence(h, run, runtime, "metrics/query", "errors started before 10:02", a, board.Refutes)
		retraction, err := h.boards.Retract(ctx, run, 0, release, e1.ID)
		require.NoError(t, err)
		dismissal, err := h.boards.Rule(ctx, run, alice, &board.RulingInput{Kind: board.RulingDismiss, Refutation: e4.ID, Reason: &board.Reason{Code: "not_the_cause"}})
		require.NoError(t, err)
		rejection, err := h.boards.Rule(ctx, run, alice, &board.RulingInput{Kind: board.RulingReject, Hypothesis: alias, Reason: &board.Reason{Code: "not_the_cause"}})
		require.NoError(t, err)
		task, err := h.boards.PostTask(ctx, run, 0, release, &board.TaskInput{Title: "check the deploy"})
		require.NoError(t, err)
		claim, err := h.boards.ClaimTask(ctx, run, 0, runtime, task.Event.ID)
		require.NoError(t, err)
		completion, err := h.boards.CompleteTask(ctx, run, 0, runtime, &board.CompletionInput{Task: task.Event.ID, Evidence: []board.EntryID{e3.ID}})
		require.NoError(t, err)

		bv := boardOf(t, boardViews(h), id)

		of := func(id board.EntryID) *app.FeedLine { return lineOf(t, bv, entryLine(id)) }
		for name, l := range map[string]*app.FeedLine{
			"a support": of(e3.ID), "a refutation": of(e4.ID), "a hypothesis": of(a), "its alias": of(alias), "a dismissal": of(dismissal.ID),
			"a rejection of the alias": of(rejection.ID), "a proposal citing the alias": of(proposal),
		} {
			assert.Equal(t, checkoutRelease, l.Key, name)
		}
		assert.Nil(t, of(e1.ID).Key, "evidence of no stance")
		assert.Equal(t, retraction.Event.At, of(e1.ID).Retracted)
		assert.Zero(t, of(e3.ID).Retracted, "it stands")
		assert.Equal(t, release, of(retraction.Event.ID).Of, "the retracted evidence's author")
		assert.Equal(t, runtime, of(dismissal.ID).Of, "the dismissed refutation's author")
		for _, l := range []*app.FeedLine{of(claim.Event.ID), of(completion.Event.ID)} {
			require.NotNil(t, l.Task)
			assert.Equal(t, task.Event.ID, l.Task.ID)
		}
		assert.Nil(t, of(task.Event.ID).Task)
	})
	t.Run("a wrap-up round and a stop", func(t *testing.T) {
		h := newHarness(t)
		h.scripts[release.Name], h.scripts[runtime.Name] = hypothesis, hypothesis
		id := h.open("checkout fails")
		h.round()
		h.scripts[release.Name], h.scripts[runtime.Name] = endTurn, endTurn
		h.round()
		s := h.state(id)
		require.Equal(t, item.StopConverged, s.Run.Stopped.Reason)

		bv := boardOf(t, boardViews(h), id)

		divider := func(n int) *app.FeedLine {
			return lineOf(t, bv, func(l *app.FeedLine) bool {
				return l.Event != nil && l.Event.Body.Round != nil && l.Event.Body.Round.Round == n
			})
		}
		assert.Nil(t, divider(1).Key, "a normal round names no key")
		assert.Equal(t, checkoutRelease, divider(2).Key, "the wrap-up round's hypothesis's")
		stop := lineOf(t, bv, func(l *app.FeedLine) bool { return l.Event != nil && l.Event.Body.Stop != nil })
		assert.Equal(t, checkoutRelease, stop.Key, "its conclusion's")
	})
	t.Run("the instances firing", func(t *testing.T) {
		h := newHarness(t)
		x := ingest(h, trigger("f1", "i", "d", item.Firing, h.clock.Now())).Item
		h.clock.Advance(time.Second)
		require.Equal(t, app.IngestAppended, ingest(h, trigger("f2", "i2", "d", item.Firing, h.clock.Now())).Outcome)
		h.clock.Advance(time.Second)
		require.Equal(t, app.IngestAppended, ingest(h, trigger("r1", "i", "d", item.Resolved, h.clock.Now())).Outcome)
		require.Equal(t, app.IngestLate, ingest(h, trigger("f0", "i", "d", item.Firing, h.clock.Now().Add(-time.Hour))).Outcome)

		bv := boardOf(t, boardViews(h), x)

		firing := map[string]int{}
		for _, id := range []string{"f1", "f2", "r1", "f0"} {
			firing[id] = lineOf(t, bv, triggeredLine(id)).Firing
		}
		assert.Equal(t, map[string]int{"f1": 1, "f2": 2, "r1": 1, "f0": 0}, firing, "a late event applies nothing")
	})
}

// titleStore is the inbound store's titles, as Views reads them: it notes
// each call, and keeps no title of the event ids gone, as after the store's
// retention.
type titleStore struct {
	app.InboundTitles
	gone  map[string]bool
	mu    sync.Mutex
	calls []titleCall
}

// titleCall is one call of Titles.
type titleCall struct {
	source string
	ids    []string
}

func (s *titleStore) Titles(ctx context.Context, source string, ids []string) (map[string]string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, titleCall{source: source, ids: slices.Clone(ids)})
	s.mu.Unlock()
	titles, err := s.InboundTitles.Titles(ctx, source, ids)
	for id := range s.gone {
		delete(titles, id)
	}
	return titles, err
}

// A trigger event's title is read from the inbound store (design D8), one
// call per source over every item_triggered of the feed, on the event's line
// and on the line of the trigger update that names it; one the store no
// longer keeps reads not kept.
func TestATriggersTitleComesFromTheInboundStoreOrIsNoLongerKept(t *testing.T) {
	h := newHarness(t)
	x := ingest(h, trigger("f1", "i", "d", item.Firing, h.clock.Now())).Item
	h.clock.Advance(time.Second)
	disk := trigger("f2", "i2", "d", item.Firing, h.clock.Now())
	disk.Title = "disk full"
	require.Equal(t, app.IngestAppended, ingest(h, disk).Outcome)
	titles := &titleStore{InboundTitles: h.records, gone: map[string]bool{"f1": true}}

	bv := boardOf(t, viewsWith(h, func(p *app.ViewPorts) { p.Titles = titles }), x)

	opening, update := lineOf(t, bv, triggeredLine("f1")), lineOf(t, bv, triggeredLine("f2"))
	assert.Equal(t, []any{"", false}, []any{opening.Title, opening.Kept}, "no longer kept")
	assert.Equal(t, []any{"disk full", true}, []any{update.Title, update.Kept})
	evidence := lineOf(t, bv, func(l *app.FeedLine) bool {
		return l.Event != nil && l.Event.Body.Evidence != nil && l.Event.Body.Evidence.Provenance.Ref == "trigger:f2"
	})
	assert.Equal(t, []any{"disk full", true}, []any{evidence.Title, evidence.Kept}, "the update that names it")
	require.Len(t, titles.calls, 1, "one call per source")
	assert.Equal(t, "alerting", titles.calls[0].source)
	assert.ElementsMatch(t, []string{"f1", "f2"}, titles.calls[0].ids)
}

// Notices repeated in a round, of one code, agent, tool, rule and skill,
// are one line at the first, with their count (design D9): two model_failed
// notices of one agent in round 2 are one line of 2; two skill_yanked
// notices of two versions are two lines; another round's is another line.
// The header counts the latest round's notices, each counted.
func TestNoticesRepeatedInARoundShowOnceWithTheirCount(t *testing.T) {
	h := newHarness(t)
	busy(h)
	views := boardViews(h)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	h.round()
	notice(h, run, &board.Notice{Code: board.NoticeModelFailed, Agent: release.Name, CallID: "call-1"})
	notice(h, run, &board.Notice{Code: board.NoticeModelFailed, Agent: release.Name, CallID: "call-2"})
	notice(h, run, &board.Notice{Code: board.NoticeSkillYanked, Agent: release.Name, Skill: "triage@1.0.0"})
	notice(h, run, &board.Notice{Code: board.NoticeSkillYanked, Agent: release.Name, Skill: "triage@1.1.0"})
	assert.Equal(t, 4, boardOf(t, views, id).Header.Notices, "round 2's, each counted")
	h.round()
	notice(h, run, &board.Notice{Code: board.NoticeModelFailed, Agent: release.Name, CallID: "call-3"})

	bv := boardOf(t, views, id)

	type shown struct {
		round   int
		code    board.NoticeCode
		call    string
		skill   string
		repeats int
	}
	var notices []shown
	for i := range bv.Feed {
		if l := &bv.Feed[i]; l.Event != nil && l.Event.Body.Notice != nil {
			n := l.Event.Body.Notice
			notices = append(notices, shown{l.Event.Round, n.Code, n.CallID, n.Skill, l.Repeats})
		}
	}
	assert.Equal(t, []shown{
		{2, board.NoticeModelFailed, "call-1", "", 2},
		{2, board.NoticeSkillYanked, "", "triage@1.0.0", 1},
		{2, board.NoticeSkillYanked, "", "triage@1.1.0", 1},
		{3, board.NoticeModelFailed, "call-3", "", 1},
	}, notices, "at the first, with their count")
	assert.Equal(t, 1, bv.Header.Notices, "round 3's")
	assert.Equal(t, []int{3, 2}, []int{bv.Header.Round, bv.Header.Ended}, "the latest round, and the latest ended")
}

// idsOf lists entries' ids.
func idsOf(events []board.Event) []board.EntryID {
	out := make([]board.EntryID, 0, len(events))
	for i := range events {
		out = append(out, events[i].ID)
	}
	return out
}

// Suggestions sit under the hypothesis each cites, an alias's under its
// hypothesis, grouped by their target in the order tool policy lists it, the
// groups by their first proposal and each group's in commit order (design
// D6); the header counts every suggestion.
func TestSuggestionsSitUnderTheirHypothesisGroupedByTarget(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	a := postHypothesis(h, run, release, checkoutRelease, postEvidence(h, run, release, "logs/search", "checkout v42 deployed at 10:02", "", "").ID)
	b := postHypothesis(h, run, runtime, paymentsConfig, postEvidence(h, run, runtime, "metrics/query", "pool at 98%", "", "").ID)
	p1 := propose(h, run, release, a, "deploys.rollback", on("checkout"), nil)
	p2 := propose(h, run, release, a, "deploys.rollback", on("payments"), nil)
	p3 := propose(h, run, release, a, "deploys.restart", on("checkout"), nil)
	p4 := propose(h, run, runtime, b, "deploys.restart", on("payments"), nil)

	bv := boardOf(t, boardViews(h), id)

	groups := func(card *app.HypothesisCard) map[string][]board.EntryID {
		out := map[string][]board.EntryID{}
		for _, g := range card.Suggestions {
			var ids []board.EntryID
			for i := range g.Suggestions {
				ids = append(ids, g.Suggestions[i].Event.ID)
				assert.Equal(t, card.Status, g.Suggestions[i].Status, "its hypothesis's status")
			}
			out[g.Target[0].Value] = ids
		}
		return out
	}
	ca, cb := cardOf(t, bv, a), cardOf(t, bv, b)
	assert.Equal(t, map[string][]board.EntryID{"checkout": {p1, p3}, "payments": {p2}}, groups(ca))
	require.Len(t, ca.Suggestions, 2)
	assert.Equal(t, []board.TargetValue{{Arg: "service", Value: "checkout"}}, ca.Suggestions[0].Target, "by its first proposal")
	assert.Equal(t, map[string][]board.EntryID{"payments": {p4}}, groups(cb))
	assert.Equal(t, 4, bv.Header.Suggestions)
}

// A suggestion is superseded when its hypothesis is rejected now, or is not
// converged now and converged at some round's end (D5): here converged at
// round 1's end and refuted in its wrap-up round; rejected; and never
// converged, which is not.
func TestASuggestionIsSupersededOnceItsHypothesisLostConvergence(t *testing.T) {
	suggestionOn := func(t *testing.T, bv *app.BoardView, hyp board.EntryID) app.Suggestion {
		t.Helper()
		card := cardOf(t, bv, hyp)
		require.Len(t, card.Suggestions, 1)
		require.Len(t, card.Suggestions[0].Suggestions, 1)
		return card.Suggestions[0].Suggestions[0]
	}
	t.Run("converged at round 1's end, not now", func(t *testing.T) {
		h := newHarness(t)
		h.scripts[release.Name], h.scripts[runtime.Name] = hypothesis, hypothesis
		id := h.open("checkout fails")
		run := h.state(id).Run.ID
		h.round()
		hyp := h.state(id).Run.Latest().Opened.WrapUp
		require.NotEmpty(t, hyp, "converged at round 1's end")
		propose(h, run, release, hyp, "deploys.rollback", on("checkout"), nil)
		views := boardViews(h)
		converged := suggestionOn(t, boardOf(t, views, id), hyp)
		assert.False(t, converged.Superseded, "converged now")
		assert.Equal(t, board.StatusConfirming, converged.Status)

		postEvidence(h, run, runtime, "logs/search", "errors rose at 10:03", hyp, board.Refutes)

		lost := suggestionOn(t, boardOf(t, views, id), hyp)
		assert.True(t, lost.Superseded)
		assert.Equal(t, board.StatusRefuted, lost.Status)
	})
	for name, c := range map[string]struct {
		rejected, superseded bool
	}{
		"rejected":        {rejected: true, superseded: true},
		"never converged": {superseded: false},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			id := h.open("checkout fails")
			run := h.state(id).Run.ID
			hyp := postHypothesis(h, run, release, checkoutRelease, postEvidence(h, run, release, "logs/search", "checkout v42 deployed at 10:02", "", "").ID)
			propose(h, run, release, hyp, "deploys.rollback", on("checkout"), nil)
			if c.rejected {
				reject(h, run, hyp)
			}

			assert.Equal(t, c.superseded, suggestionOn(t, boardOf(t, boardViews(h), id), hyp).Superseded)
		})
	}
}

// deploysCommit is the revision of deploysRevision.
var deploysCommit = strings.Repeat("d", 40)

// deploysRevision makes current a revision of the harness's configuration
// with a deploys server, whose rollback binds its service argument to the
// cited hypothesis's service, padded, and whose restart binds it to the
// item's.
func deploysRevision(h *harness) {
	h.t.Helper()
	read := `{effect: read, schemaHash: "sha256:` + strings.Repeat("e", 64) + `"}`
	write := func(tool, binding string) string {
		return "    " + tool + ":\n      schemaHash: \"sha256:" + strings.Repeat("f", 64) + "\"\n      effect: write\n      target: [service]\n" +
			"      scope: {service: \"" + binding + "\"}\n      candidates:\n        service: {tool: list_services, field: \"$[*].name\"}\n" +
			"      summary: Act on {args.service}\n"
	}
	h.revs.set(h.revision(deploysCommit, func(files map[string][]byte) {
		files["servers/deploys.yaml"] = []byte("kind: McpServer\nmetadata: {name: deploys, owner: platform}\nspec:\n  endpoint: https://deploys.internal/mcp\n" +
			"  image: \"sha256:" + strings.Repeat("a", 64) + "\"\n  tools:\n    list_services: " + read + "\n" +
			write("rollback", " hypothesis.service ") + write("restart", "item.service"))
	}))
}

// The service mark compares, for each scoped argument whose binding, trimmed,
// is hypothesis.service, the value the proposal recorded with the item's
// routing service, under the tool policy of the revision the item locked
// (review M3): inside when they agree, outside when they differ; a binding of
// another expression, an item with no routing service, a revision that cannot
// be read and a tool it does not hold compare nothing.
func TestTheServiceMarkComparesTheHypothesisServiceBinding(t *testing.T) {
	for _, tc := range []struct {
		name    string
		routing map[string]string
		tool    string
		scope   string
		lose    bool
		want    app.ServiceMark
	}{
		{"inside", map[string]string{"service": "checkout"}, "deploys.rollback", "checkout", false, app.ServiceInside},
		{"outside", map[string]string{"service": "checkout"}, "deploys.rollback", "payments", false, app.ServiceOutside},
		{"a binding of another expression", map[string]string{"service": "checkout"}, "deploys.restart", "payments", false, app.ServiceUncompared},
		{"no routing service", map[string]string{"region": "eu-1"}, "deploys.rollback", "payments", false, app.ServiceUncompared},
		{"the revision unreadable", map[string]string{"service": "checkout"}, "deploys.rollback", "payments", true, app.ServiceUncompared},
		{"a tool the revision does not hold", map[string]string{"service": "checkout"}, "deploys.scale", "payments", false, app.ServiceUncompared},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			deploysRevision(h)
			id, err := h.c.Open(t.Context(), &app.OpenInput{Actor: administrator, Type: "incident", Title: "checkout fails", Source: "person", Routing: tc.routing})
			require.NoError(t, err)
			require.Equal(t, deploysCommit, h.state(id).Started.Commit)
			run := h.state(id).Run.ID
			hyp := postHypothesis(h, run, release, checkoutRelease, postEvidence(h, run, release, "logs/search", "checkout v42 deployed at 10:02", "", "").ID)
			propose(h, run, release, hyp, tc.tool, on(tc.scope), map[string]string{"service": tc.scope})
			if tc.lose {
				loseCommit(h, deploysCommit)
			}

			card := cardOf(t, boardOf(t, boardViews(h), id), hyp)

			require.Len(t, card.Suggestions, 1)
			assert.Equal(t, tc.want, card.Suggestions[0].Suggestions[0].Service)
		})
	}
}

// A proposer's support is its own counted evidence on the hypothesis, the
// distinct entries by source, as the assessment counts them (design D6).
func TestAProposersSupportIsCountedBySource(t *testing.T) {
	h := newHarness(t)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	hyp := postHypothesis(h, run, release, checkoutRelease, postEvidence(h, run, release, "logs/search", "checkout v42 deployed at 10:02", "", "").ID)
	postEvidence(h, run, release, "metrics/query", "error rate 12%", hyp, board.Supports)
	postEvidence(h, run, release, "metrics/query", "error rate 14%", hyp, board.Supports)
	postEvidence(h, run, runtime, "logs/search", "errors rose at 10:03", hyp, board.Supports)
	byRelease := propose(h, run, release, hyp, "deploys.rollback", on("checkout"), nil)
	byRuntime := propose(h, run, runtime, hyp, "deploys.rollback", on("payments"), nil)

	card := cardOf(t, boardOf(t, boardViews(h), id), hyp)

	support := map[board.EntryID]map[board.Source]int{}
	for _, g := range card.Suggestions {
		for i := range g.Suggestions {
			support[g.Suggestions[i].Event.ID] = g.Suggestions[i].Support
		}
	}
	assert.Equal(t, map[board.EntryID]map[board.Source]int{
		byRelease: {"logs/search": 1, "metrics/query": 2},
		byRuntime: {"logs/search": 1},
	}, support)
}

// The hypotheses are ranked, each with its status and its quotes, the
// spec's 2 a side, and those rejected, or that a refutation stands against,
// are apart; the read calls that evidence cites are held with the quotes
// citing them (the plan's). While the wrap-up round of the converged one is
// open, it is confirming.
func TestTheHypothesesAreRankedWithTheirQuotesAndTheRuledOutApart(t *testing.T) {
	h := newHarness(t)
	views := boardViews(h)
	id := h.open("checkout fails")
	run := h.state(id).Run.ID
	e1 := postEvidence(h, run, release, "logs/search", "checkout v42 deployed at 10:02", "", "")
	a := postHypothesis(h, run, release, checkoutRelease, e1.ID)
	e2 := postEvidence(h, run, runtime, "metrics/query", "error rate 12%", a, board.Supports)
	b := postHypothesis(h, run, runtime, paymentsConfig, postEvidence(h, run, runtime, "metrics/query", "pool at 98%", "", "").ID)
	reject(h, run, b)
	c := postHypothesis(h, run, release, checkoutConfig, postEvidence(h, run, release, "logs/search", "config pushed at 10:01", "", "").ID)
	e5 := postEvidence(h, run, runtime, "logs/search", "no config change", c, board.Refutes)
	_, err := h.boards.PostEvidence(acting(h), run, 0, runtime, &board.EvidenceInput{
		Claim: "the same read again", CallID: e5.Body.Evidence.Provenance.CallID, Quote: "no config change",
	})
	require.NoError(t, err)

	bv := boardOf(t, views, id)

	cardIDs := func(cards []app.HypothesisCard) []board.EntryID {
		var out []board.EntryID
		for i := range cards {
			out = append(out, cards[i].ID)
		}
		return out
	}
	assert.Equal(t, []board.EntryID{a}, cardIDs(bv.Hypotheses))
	assert.Equal(t, []board.EntryID{b, c}, cardIDs(bv.RuledOut), "rejected, and refuted, by rank")
	ca := cardOf(t, bv, a)
	assert.Equal(t, board.StatusConverged, ca.Status)
	assert.Equal(t, []board.EntryID{e2.ID, e1.ID}, idsOf(ca.Quotes.Supports), "newest first, one of each source")
	assert.Equal(t, []any{board.StatusRejected, board.StatusRefuted}, []any{cardOf(t, bv, b).Status, cardOf(t, bv, c).Status})
	assert.Equal(t, []board.EntryID{e5.ID}, idsOf(cardOf(t, bv, c).Quotes.Refutes))
	assert.Equal(t, a, bv.Header.Conclusion.ID)
	assert.Len(t, bv.Cited, 5, "each read call evidence cites")
	assert.Equal(t, []string{"checkout v42 deployed at 10:02"}, bv.Cited[e1.Body.Evidence.Provenance.CallID])
	assert.Equal(t, []string{"no config change"}, bv.Cited[e5.Body.Evidence.Provenance.CallID], "a quote cited twice, once")

	h.round()
	require.Equal(t, a, h.state(id).Run.Latest().Opened.WrapUp)

	confirming := boardOf(t, views, id)

	assert.Equal(t, board.StatusConfirming, cardOf(t, confirming, a).Status)
	assert.Equal(t, board.StatusConfirming, confirming.Header.Conclusion.Status)
}
