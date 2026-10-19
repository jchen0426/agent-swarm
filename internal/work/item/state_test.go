package item_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// An item's records fold into its state, and a record that breaks the
// lifecycle's order is refused: each kind once where it is once, rounds in
// order, each opened as the one before decided and ended once as itself, a
// run stopped as its last round decided, a round's records only of the item's
// run, and nothing after the run stops but the item's close, or after that.
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
	for name, records := range map[string][]item.Record{
		"no opening":                      l.records[1:],
		"started twice":                   {l.records[0], l.records[1], l.records[1]},
		"a run before start":              {l.records[0], l.records[2]},
		"a round twice":                   {l.records[0], l.records[1], l.records[2], l.records[3], l.records[3]},
		"an end before open":              {l.records[0], l.records[1], l.records[2], l.records[4]},
		"ended twice":                     {l.records[0], l.records[1], l.records[2], l.records[3], l.records[4], l.records[4]},
		"a stop before end":               {l.records[0], l.records[1], l.records[2], l.records[3], l.records[5]},
		"closed while open":               {l.records[0], l.records[1], l.records[2], l.records[6]},
		"anything after stop":             {l.records[0], l.records[1], l.records[2], l.records[3], l.records[4], l.records[5], l.records[3]},
		"a round after a stop decision":   {l.records[0], l.records[1], l.records[2], l.records[3], l.records[4], second},
		"a wrap-up of another hypothesis": {w.records[0], w.records[1], w.records[2], w.records[3], w.records[4], wrapsUpOther},
		"a round while one is open":       {l.records[0], l.records[1], l.records[2], l.records[3], second},
		"an end of another round":         {l.records[0], l.records[1], l.records[2], l.records[3], endsSecond},
		"a stop for another reason":       {l.records[0], l.records[1], l.records[2], l.records[3], l.records[4], stopsOverBudget},
		"a start after close":             {l.records[0], closesUnstaffed, l.records[1]},
		"a round of another run":          {l.records[0], l.records[1], l.records[2], ofRunTwo},
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
