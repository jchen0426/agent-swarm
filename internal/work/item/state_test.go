package item_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// An item's records fold into its state, and a record that breaks the
// lifecycle's order is refused: each kind once where it is once, rounds in
// order, each opened as the one before decided and ended once as itself, a
// run stopped as its last round decided, a round's records only of the item's
// run, and nothing after the run stops but the item's close, or after that.
// Each state takes what Decision 2's table admits, each row failing alone when
// its rule is taken out: a promotion while the item has no run and does not
// investigate; a person's close while no run of it runs, and a close by itself
// only while the item awaits its close; a reopen of a closed item alone; a
// round stopped only to stop killed or closed, and such a stop before the
// run's first round, after a round that decided no stop, or after a round
// stopped to it; not staffed while the item has no run, though it started;
// and an extension of the item's run, of some amount, while it runs, or, a
// resume, once it stopped and the item awaits its close; and a link or an
// unlink of the item, in any state, from the incident it is in into another,
// a link naming its other item, never of a run.
func TestRecordsFoldInTheLifecyclesOrderOnly(t *testing.T) {
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, quiet())
	stop := l.s.Run.Stop("")
	l.write("run-1", item.Body{RunStopped: &stop})
	l.write("", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopQuiet}})

	s, err := item.Fold(l.records)

	require.NoError(t, err)
	assert.Equal(t, item.StatusAwaitingClose, s.Status())
	assert.Equal(t, item.StopQuiet, s.Closing.Reason)
	assert.Len(t, s.Run.Rounds, 1)
	// The records the lifecycle never writes are copies of its own, each with
	// a body of its own: a body holds pointers. The life w decided a wrap-up
	// of hyp in its first round.
	second := l.records[3]
	amend(&second.Body.RoundOpened, func(o *item.RoundOpened) { o.Round = 2 })
	endsSecond := l.records[4]
	amend(&endsSecond.Body.RoundEnded, func(e *item.RoundEnded) { e.Round = 2 })
	stopsOverBudget := l.records[5]
	amend(&stopsOverBudget.Body.RunStopped, func(r *item.RunStopped) { r.Reason = item.StopBudgetExhausted })
	ofRunTwo := l.records[3]
	ofRunTwo.Run = "run-2"
	closesUnstaffed := l.records[6]
	amend(&closesUnstaffed.Body.AwaitingClose, func(c *item.AwaitingClose) { c.Reason = item.StopNotStaffed })
	w := newLife(t, created())
	w.open()
	w.end(time.Minute, converging(hyp))
	wrapsUpOther := w.records[3]
	amend(&wrapsUpOther.Body.RoundOpened, func(o *item.RoundOpened) { o.Round, o.WrapUp = 2, other })
	// 7b-1's records of the item, made as the coordinator writes them, not
	// through the rules a row tests. The round's end and the run's stops a
	// halt writes are copies of l's.
	ofItem := func(id string, body item.Body) item.Record {
		body.V = item.Version
		return item.Record{ID: id, Item: "item-1", Incident: "inc-1", Body: body}
	}
	promotes := ofItem("promotion", item.Body{Promoted: promoted()})
	closes := ofItem("close", item.Body{Closed: closedBy()})
	reopens := ofItem("reopen", item.Body{Reopened: reopened(notStaffed(item.CauseClosed))})
	closesByItself := ofItem("close-by-itself", item.Body{Closed: closedAuto(t0)})
	// 7c-1a's extension of the item's run, and one of nothing.
	ofRun := func(id string, body item.Body) item.Record {
		r := ofItem(id, body)
		r.Run = "run-1"
		return r
	}
	extends := ofRun("extension", item.Body{Extended: extension(oneRound, 0)})
	extendsByNothing := ofRun("extension-of-nothing", item.Body{Extended: extension(item.Budget{}, 0)})
	// 7c-1a's links and unlinks of the item: the column holds the incident it
	// joins, and the body the one it left.
	ofIncident := func(id string, incident item.IncidentID, body item.Body) item.Record {
		r := ofItem(id, body)
		r.Incident = incident
		return r
	}
	linkedFrom := func(left item.IncidentID, with item.ID) *item.Linked {
		return &item.Linked{Left: left, With: with, By: "admin", Reason: &board.Reason{Code: "duplicate"}, Request: item.Request{Key: "sha256:09", Digest: "sha256:0a"}}
	}
	unlinkedFrom := func(left item.IncidentID) *item.Unlinked {
		return &item.Unlinked{Left: left, By: "admin", Reason: &board.Reason{Code: "not_related"}, Request: item.Request{Key: "sha256:0b", Digest: "sha256:0c"}}
	}
	links := ofIncident("link", "inc-2", item.Body{Linked: linkedFrom("inc-1", "item-2")})
	linksBack := ofIncident("link-back", "inc-1", item.Body{Linked: linkedFrom("inc-2", "item-3")})
	unlinks := ofIncident("unlink", "inc-3", item.Body{Unlinked: unlinkedFrom("inc-1")})
	linksFromElsewhere := ofIncident("link-elsewhere", "inc-2", item.Body{Linked: linkedFrom("inc-9", "item-2")})
	linksIntoItsOwn := ofIncident("link-own", "inc-1", item.Body{Linked: linkedFrom("inc-1", "item-2")})
	linksToNoItem := ofIncident("link-no-item", "inc-2", item.Body{Linked: linkedFrom("inc-1", "")})
	unlinksIntoItsOwn := ofIncident("unlink-own", "inc-1", item.Body{Unlinked: unlinkedFrom("inc-1")})
	linksIntoNone := ofIncident("link-none", "", item.Body{Linked: linkedFrom("inc-1", "item-2")})
	linksOfARun := links
	linksOfARun.Run = "run-1"
	stoppedAs := func(d item.Decision) item.Record {
		r := l.records[4]
		amend(&r.Body.RoundEnded, func(e *item.RoundEnded) { e.EndedBy, e.Decision = item.EndedByStopped, d })
		return r
	}
	stoppedToKill := stoppedAs(item.Decision{Next: item.NextStop, Stop: item.StopKilled})
	stopsAs := func(reason item.StopReason) item.Record {
		r := l.records[5]
		amend(&r.Body.RunStopped, func(st *item.RunStopped) { st.Reason = reason })
		return r
	}
	stopsKilled, stopsClosed := stopsAs(item.StopKilled), stopsAs(item.StopClosed)
	// The states the rows start from: l's item queued, started with no run,
	// with its run created, in its first round, its run stopped, and awaiting
	// its close; one awaiting its close not staffed, one closed while queued,
	// and b's item, whose first round decided another.
	b := newLife(t, created())
	b.open()
	b.end(time.Minute, busy())
	queued, started, running, roundOpen := l.records[:1], l.records[:2], l.records[:3], l.records[:4]
	runStopped, awaiting := l.records[:6], l.records[:7]
	awaitingWithNoRun, closed := []item.Record{l.records[0], closesUnstaffed}, []item.Record{l.records[0], closes}
	wentOn := b.records[:5]
	then := func(before []item.Record, more ...item.Record) []item.Record {
		return append(slices.Clone(before), more...)
	}
	for name, records := range map[string][]item.Record{
		"a promotion while queued":                    then(queued, promotes),
		"a promotion awaiting its close with no run":  then(awaitingWithNoRun, promotes),
		"a close while queued":                        then(queued, closes),
		"a close awaiting its close":                  then(awaiting, closes),
		"a close by itself awaiting its close":        then(awaiting, closesByItself),
		"a close investigating with no run":           then(started, closes),
		"a close once its run's stop is recorded":     then(runStopped, closes),
		"a reopen of a closed item":                   then(closed, reopens),
		"a round stopped to stop killed":              then(roundOpen, stoppedToKill),
		"a round stopped to stop closed":              then(roundOpen, stoppedAs(item.Decision{Next: item.NextStop, Stop: item.StopClosed})),
		"a killed stop before the first round":        then(running, stopsKilled),
		"a closed stop after a round that went on":    then(wentOn, stopsClosed),
		"a killed stop after a round stopped to kill": then(roundOpen, stoppedToKill, stopsKilled),
		"not staffed once started with no run":        then(started, closesUnstaffed),
		"an extension investigating":                  then(running, extends),
		"an extension while a round is open":          then(roundOpen, extends),
		"a resume awaiting its close":                 then(awaiting, extends),
		"a link while queued":                         then(queued, links),
		"a link investigating":                        then(running, links),
		"a link while a round is open":                then(roundOpen, links),
		"a link awaiting its close":                   then(awaiting, links),
		"a link of a closed item":                     then(closed, links),
		"a link back after a link":                    then(running, links, linksBack),
		"an unlink while queued":                      then(queued, unlinks),
		"an unlink of a closed item":                  then(closed, unlinks),
		"an extension after a link":                   then(running, links, extends),
	} {
		_, err := item.Fold(renumbered(records))

		require.NoError(t, err, name)
	}
	for name, records := range map[string][]item.Record{
		"a promotion awaiting its close after its run": then(awaiting, promotes),
		"a promotion investigating":                    then(started, promotes),
		"a promotion of a closed item":                 then(closed, promotes),
		"a close while its run runs":                   then(running, closes),
		"a close of a closed item":                     then(closed, closes),
		"a close by itself not awaiting its close":     then(runStopped, closesByItself),
		"a reopen while queued":                        then(queued, reopens),
		"a reopen investigating":                       then(started, reopens),
		"a reopen awaiting its close":                  then(awaitingWithNoRun, reopens),
		"a round stopped to go on":                     then(roundOpen, stoppedAs(item.Decision{Next: item.NextRound})),
		"a round stopped to wrap up":                   then(roundOpen, stoppedAs(item.Decision{Next: item.NextWrapUp, WrapUp: hyp})),
		"a round stopped to stop quiet":                then(roundOpen, stoppedAs(item.Decision{Next: item.NextStop, Stop: item.StopQuiet})),
		"a killed stop while a round is open":          then(roundOpen, stopsKilled),
		"a closed stop after a round stopped to kill":  then(roundOpen, stoppedToKill, stopsClosed),
		"an extension while queued":                    then(queued, extends),
		"an extension of nothing":                      then(running, extendsByNothing),
		"an extension of a closed item":                then(awaiting, closes, extends),
		"a resume before the item awaits its close":    then(runStopped, extends),
		"an extension awaiting its close with no run":  then(awaitingWithNoRun, extends),
		"a link from another incident":                 then(running, linksFromElsewhere),
		"a link into its own incident":                 then(running, linksIntoItsOwn),
		"a link naming no other item":                  then(running, linksToNoItem),
		"a link into no incident":                      then(running, linksIntoNone),
		"a link of a run":                              then(running, linksOfARun),
		"an unlink into its own incident":              then(running, unlinksIntoItsOwn),
		"an unlink from another incident":              then(running, links, unlinks),
		"no opening":                                   l.records[1:],
		"started twice":                                {l.records[0], l.records[1], l.records[1]},
		"a run before start":                           {l.records[0], l.records[2]},
		"a round twice":                                {l.records[0], l.records[1], l.records[2], l.records[3], l.records[3]},
		"an end before open":                           {l.records[0], l.records[1], l.records[2], l.records[4]},
		"ended twice":                                  {l.records[0], l.records[1], l.records[2], l.records[3], l.records[4], l.records[4]},
		"a stop before end":                            {l.records[0], l.records[1], l.records[2], l.records[3], l.records[5]},
		"closed while open":                            {l.records[0], l.records[1], l.records[2], l.records[6]},
		"anything after stop":                          {l.records[0], l.records[1], l.records[2], l.records[3], l.records[4], l.records[5], l.records[3]},
		"a round after a stop decision":                {l.records[0], l.records[1], l.records[2], l.records[3], l.records[4], second},
		"a wrap-up of another hypothesis":              {w.records[0], w.records[1], w.records[2], w.records[3], w.records[4], wrapsUpOther},
		"a round while one is open":                    {l.records[0], l.records[1], l.records[2], l.records[3], second},
		"an end of another round":                      {l.records[0], l.records[1], l.records[2], l.records[3], endsSecond},
		"a stop for another reason":                    {l.records[0], l.records[1], l.records[2], l.records[3], l.records[4], stopsOverBudget},
		"a start after close":                          {l.records[0], closesUnstaffed, l.records[1]},
		"a round of another run":                       {l.records[0], l.records[1], l.records[2], ofRunTwo},
	} {
		_, err := item.Fold(renumbered(records))

		require.ErrorIs(t, err, item.ErrInvalid, name)
	}
}

// renumbered gives records the sequence numbers a store would.
func renumbered(records []item.Record) []item.Record {
	out := slices.Clone(records)
	for i := range out {
		out[i].Seq = int64(i + 1)
	}
	return out
}

// amend gives the pointer a changed copy of what it pointed at: a copy of a
// record shares its body's pointers with the record, and a case must not
// change what another case reads.
func amend[T any](p **T, change func(*T)) {
	c := **p
	change(&c)
	*p = &c
}

// A record of another version is refused: an older binary never acts on a
// record it cannot read.
func TestARecordOfAnotherVersionIsRefused(t *testing.T) {
	l := newLife(t, nil)
	r := l.records[0]
	r.Body.V = item.Version + 1

	_, err := item.Fold([]item.Record{r})

	require.ErrorIs(t, err, item.ErrVersion)
}

// A body of another version decodes to its version alone, though the rest
// of it no longer decodes, so that the fold refuses it with ErrVersion; a
// body of this version that does not decode is an error.
func TestABodyOfAnotherVersionDecodesToItsVersion(t *testing.T) {
	var later item.Body
	require.NoError(t, json.Unmarshal([]byte(`{"v": 2, "opened": "a later form"}`), &later))
	assert.Equal(t, item.Body{V: 2}, later)
	_, err := item.Fold([]item.Record{{Seq: 1, ID: "rec-1", Item: "item-1", Incident: "inc-1", Body: later}})
	require.ErrorIs(t, err, item.ErrVersion)

	var broken item.Body
	require.Error(t, json.Unmarshal([]byte(`{"v": 1, "opened": "not an opening"}`), &broken))
}

// Records fold in commit order: a record out of order, or of another item,
// is refused.
func TestRecordsOfOneItemFoldInCommitOrder(t *testing.T) {
	l := newLife(t, created())
	stray := l.records[1]
	stray.Item = "item-2"
	late := l.records[1]
	late.Seq = 0

	_, err := item.Fold([]item.Record{l.records[0], stray})
	require.ErrorIs(t, err, item.ErrInvalid)
	_, err = item.Fold([]item.Record{l.records[0], late})
	require.ErrorIs(t, err, item.ErrInvalid)
}

// A stored body with no version is corrupt, not a later version: this build
// writes version 1 and a later one writes more, so a body whose version is
// missing, null or below 1 does not decode, whatever else it holds.
func TestABodyWithNoVersionDoesNotDecode(t *testing.T) {
	for name, raw := range map[string]string{
		"an empty object":       `{}`,
		"a null version":        `{"v": null}`,
		"version zero":          `{"v": 0}`,
		"a negative version":    `{"v": -1}`,
		"a kind and no version": `{"opened": {"type": "incident", "title": "checkout fails", "source": "person"}}`,
		"a null body":           `null`,
	} {
		var body item.Body

		err := json.Unmarshal([]byte(raw), &body)

		require.Error(t, err, name)
	}
}

// A body of this version decodes whole, as it was written: version 1, the
// first, is the lowest a body can hold.
func TestABodyOfThisVersionDecodesWhole(t *testing.T) {
	body := item.Body{V: item.Version, AwaitingClose: &item.AwaitingClose{Reason: item.StopQuiet}}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	var decoded item.Body
	err = json.Unmarshal(raw, &decoded)

	require.NoError(t, err)
	assert.Equal(t, body, decoded)
}

// An item opened while the day's spend has reached the cap waits for a
// person to close it, not staffed, without a run.
func TestAnItemNotStaffedAwaitsCloseWithoutARun(t *testing.T) {
	l := newLife(t, nil)
	l.records = l.records[:1]
	l.s, _ = item.Fold(l.records)

	l.write("", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed}})

	assert.Equal(t, item.StatusAwaitingClose, l.s.Status())
	assert.Nil(t, l.s.Run)
}

// An item awaits its close with its reason: its run's, as ItemReason gives
// it, or not staffed when it has no run.
func TestAnItemAwaitsItsCloseWithItsReason(t *testing.T) {
	closing := func(reason item.StopReason) item.Body {
		return item.Body{AwaitingClose: &item.AwaitingClose{Reason: reason}}
	}
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, quiet())
	stop := l.s.Run.Stop("")
	l.write("run-1", item.Body{RunStopped: &stop})
	require.ErrorIs(t, l.try("", closing(item.StopConverged)), item.ErrInvalid)
	require.NoError(t, l.try("", closing(item.StopQuiet)))

	cut := newLife(t, created())
	cut.open()
	e := busy()
	e.DaySpent, e.DailyCap = 10, 10
	cut.end(time.Minute, e)
	stop = cut.s.Run.Stop("")
	cut.write("run-1", item.Body{RunStopped: &stop})
	require.ErrorIs(t, cut.try("", closing(item.StopCut)), item.ErrInvalid)
	require.NoError(t, cut.try("", closing(item.StopNotStaffed)))

	unstaffed := newLife(t, nil)
	unstaffed.records = unstaffed.records[:1]
	unstaffed.s, _ = item.Fold(unstaffed.records)
	require.ErrorIs(t, unstaffed.try("", closing(item.StopQuiet)), item.ErrInvalid)
	require.NoError(t, unstaffed.try("", closing(item.StopNotStaffed)))
}

// A run's id holds no colon: a record's scope in the audit chain is its run,
// or incident:<id> for the records of no run, and a run id could pass for
// an incident's scope.
func TestARunsIDHoldsNoColon(t *testing.T) {
	l := newLife(t, nil)

	require.ErrorIs(t, l.try("incident:inc-1", item.Body{RunCreated: created()}), item.ErrInvalid)
	require.NoError(t, l.try("run-1", item.Body{RunCreated: created()}))
}

// A run with no agent to invite stops not staffed, with no round.
func TestARunWithNoAgentsStopsWithoutARound(t *testing.T) {
	rc := created()
	rc.Agents = []string{}
	l := newLife(t, rc)

	stop := item.RunStopped{Reason: item.StopNotStaffed}
	l.write("run-1", item.Body{RunStopped: &stop})

	assert.Equal(t, item.StopNotStaffed, l.s.Run.Stopped.Reason)
	assert.Equal(t, item.StatusInvestigating, l.s.Status())
}

// A run cut at the daily cap leaves its item not staffed.
func TestACutRunLeavesItsItemNotStaffed(t *testing.T) {
	assert.Equal(t, item.StopNotStaffed, item.ItemReason(item.StopCut))
	assert.Equal(t, item.StopQuiet, item.ItemReason(item.StopQuiet))
}

// A record's body names its kind once, and its JSON keeps its version.
func TestARecordsBodyHoldsOneKindAndItsVersion(t *testing.T) {
	body := item.Body{V: item.Version, AwaitingClose: &item.AwaitingClose{Reason: item.StopQuiet}}

	raw, err := json.Marshal(body)

	require.NoError(t, err)
	assert.JSONEq(t, `{"v": 1, "awaitingClose": {"reason": "quiet"}}`, string(raw))
	assert.Equal(t, item.KindAwaitingClose, body.Kind())
	assert.Equal(t, item.Kind(""), (&item.Body{}).Kind())
	two := body
	two.Opened = &item.Opened{}
	assert.Equal(t, item.Kind(""), two.Kind())
}

// An item's status follows its records (Decision 2): queued from its
// opening, investigating once it started, awaiting its close once its run
// stopped and the item said so, closed by a close, and awaiting its close
// again after a reopen. An item not staffed and promoted is queued again when
// it never started, and investigating, to get its run, when it did.
func TestAnItemsStatusFollowsItsRecords(t *testing.T) {
	l := newOpened(t)
	assert.Equal(t, item.StatusQueued, l.s.Status(), "opened")
	l.start()
	assert.Equal(t, item.StatusInvestigating, l.s.Status(), "started")
	l.write("run-1", item.Body{RunCreated: created()})
	l.open()
	l.end(time.Minute, quiet())
	stop := l.s.Run.Stop("")
	l.write("run-1", item.Body{RunStopped: &stop})
	assert.Equal(t, item.StatusInvestigating, l.s.Status(), "its run stopped")
	l.write("", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopQuiet}})
	assert.Equal(t, item.StatusAwaitingClose, l.s.Status(), "awaiting its close")
	l.write("", item.Body{Closed: closedBy()})
	assert.Equal(t, item.StatusClosed, l.s.Status(), "closed")
	l.write("", item.Body{Reopened: reopened(&item.AwaitingClose{Reason: item.StopQuiet})})
	assert.Equal(t, item.StatusAwaitingClose, l.s.Status(), "reopened")

	never := newOpened(t)
	never.write("", item.Body{AwaitingClose: notStaffed(item.CauseDailyCap)})
	assert.Equal(t, item.StatusAwaitingClose, never.s.Status(), "not staffed")
	never.write("", item.Body{Promoted: promoted()})
	assert.Equal(t, item.StatusQueued, never.s.Status(), "promoted before it started")

	runless := newLife(t, nil)
	runless.write("", item.Body{AwaitingClose: notStaffed(item.CauseKillSwitch)})
	runless.write("", item.Body{Promoted: promoted()})
	assert.Equal(t, item.StatusInvestigating, runless.s.Status(), "promoted once it started")
}

// A closed item takes a reopen and nothing else, a run's records included,
// and the reopen returns it to awaiting its close with the reason it carries.
func TestAClosedItemTakesOnlyAReopen(t *testing.T) {
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, quiet())
	stop := l.s.Run.Stop("")
	l.write("run-1", item.Body{RunStopped: &stop})
	l.write("", item.Body{Closed: closedBy()})
	next := item.RoundOpened{Round: 2, OpenedAt: l.now, Deadline: l.now.Add(time.Minute), Agents: []string{release, runtime}}

	for kind, r := range map[item.Kind]struct {
		run  board.RunID
		body item.Body
	}{
		item.KindOpened:        {"", l.records[0].Body},
		item.KindStarted:       {"", l.records[1].Body},
		item.KindRunCreated:    {"run-2", item.Body{RunCreated: created()}},
		item.KindRoundOpened:   {"run-1", item.Body{RoundOpened: &next}},
		item.KindRoundEnded:    {"run-1", l.records[4].Body},
		item.KindRunStopped:    {"run-1", l.records[5].Body},
		item.KindAwaitingClose: {"", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopQuiet}}},
		item.KindPromoted:      {"", item.Body{Promoted: promoted()}},
		item.KindClosed:        {"", item.Body{Closed: closedBy()}},
	} {
		require.ErrorIs(t, l.try(r.run, r.body), item.ErrInvalid, kind)
	}
	require.NoError(t, l.try("", item.Body{Reopened: reopened(&item.AwaitingClose{Reason: item.StopQuiet, AutoClose: 30 * time.Minute})}))

	assert.Equal(t, item.StatusAwaitingClose, l.s.Status())
	assert.Equal(t, &item.AwaitingClose{Reason: item.StopQuiet, AutoClose: 30 * time.Minute}, l.s.Closing)
}

// A round that decided its stop decides the run's (Decision 3): a stop
// killed or closed after it is refused, and the decided stop is taken.
func TestAKilledOrClosedStopIsRefusedAfterADecidedStop(t *testing.T) {
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, quiet())

	for _, reason := range []item.StopReason{item.StopKilled, item.StopClosed} {
		require.ErrorIs(t, l.try("run-1", item.Body{RunStopped: &item.RunStopped{Reason: reason}}), item.ErrInvalid, reason)
	}
	stop := l.s.Run.Stop("")
	require.NoError(t, l.try("run-1", item.Body{RunStopped: &stop}))
	assert.Equal(t, item.StopQuiet, l.s.Run.Stopped.Reason)
}

// A started item whose run was never created awaits its close not staffed,
// as the kill switch leaves it (Decision 5), and with no other reason.
func TestAStartedItemWithoutARunAwaitsItsCloseNotStaffed(t *testing.T) {
	l := newLife(t, nil)

	require.ErrorIs(t, l.try("", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopQuiet}}), item.ErrInvalid)
	require.NoError(t, l.try("", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.StopNotStaffed, Cause: item.CauseKillSwitch, UnstaffedBy: 1}}))

	assert.Equal(t, item.StatusAwaitingClose, l.s.Status())
	assert.Equal(t, item.CauseKillSwitch, l.s.Closing.Cause)
	assert.Nil(t, l.s.Run)
}

// The fold keeps, from the committed records' times, when the item opened,
// when it last entered awaiting_close, a reopen included, and when it last
// closed; a reopen ends the close it follows.
func TestTheFoldKnowsWhenAnItemOpenedAwaitedItsCloseAndClosed(t *testing.T) {
	l := newOpened(t)
	at := func(d time.Duration) time.Time {
		l.now = t0.Add(d)
		return l.now
	}
	awaited := at(time.Hour)
	l.write("", item.Body{AwaitingClose: notStaffed(item.CauseDailyCap)})
	firstClose := at(2 * time.Hour)
	l.write("", item.Body{Closed: closedBy()})

	assert.Equal(t, t0, l.s.OpenedAt)
	assert.Equal(t, awaited, l.s.AwaitingSince)
	assert.Equal(t, firstClose, l.s.ClosedAt)
	require.NotNil(t, l.s.Closed)

	reopen := at(3 * time.Hour)
	l.write("", item.Body{Reopened: reopened(notStaffed(item.CauseDailyCap))})
	assert.Equal(t, reopen, l.s.AwaitingSince, "a reopen enters awaiting_close")
	assert.Nil(t, l.s.Closed, "a reopen ends the close")
	assert.Equal(t, firstClose, l.s.ClosedAt, "the latest close's")

	secondClose := at(4 * time.Hour)
	l.write("", item.Body{Closed: closedAuto(secondClose)})
	assert.Equal(t, secondClose, l.s.ClosedAt, "the latest close's")
	assert.Equal(t, reopen, l.s.AwaitingSince)
	assert.Equal(t, t0, l.s.OpenedAt)
}

// A link gives the item the incident of its record's column, and an unlink
// the new one its column holds; every later record keeps it (Decision 18).
func TestALinkGivesTheItemTheIncidentOfItsColumn(t *testing.T) {
	l := newLife(t, created())
	require.Equal(t, item.IncidentID("inc-1"), l.s.Incident)
	link := item.Record{Seq: int64(len(l.records) + 1), ID: "link", Item: "item-1", Incident: "inc-2", At: l.now, Body: item.Body{
		V: item.Version, Linked: &item.Linked{Left: "inc-1", With: "item-2", By: "admin", Reason: &board.Reason{Code: "duplicate"}},
	}}

	require.NoError(t, l.s.Apply(&link))

	assert.Equal(t, item.IncidentID("inc-2"), l.s.Incident)
	assert.Equal(t, item.KindLinked, link.Body.Kind())
	unlink := item.Record{Seq: link.Seq + 1, ID: "unlink", Item: "item-1", Incident: "inc-3", At: l.now, Body: item.Body{
		V: item.Version, Unlinked: &item.Unlinked{Left: "inc-2", By: "admin", Reason: &board.Reason{Code: "not_related"}},
	}}

	require.NoError(t, l.s.Apply(&unlink))

	assert.Equal(t, item.IncidentID("inc-3"), l.s.Incident)
	assert.Equal(t, item.KindUnlinked, unlink.Body.Kind())
	assert.Equal(t, item.StatusInvestigating, l.s.Status(), "a link changes no state")
}
