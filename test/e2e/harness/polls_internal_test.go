//go:build e2e

package harness

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The poll reader's tests, over events made by hand: they need no browser,
// and always run.

// tabEvents builds a tab's events as the harness records them, each at its
// offset from the run's start, numbered in order.
type tabEvents struct {
	t      *testing.T
	events []Event
}

var pollEpoch = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

func (te *tabEvents) add(at time.Duration, kind string, detail any) {
	te.t.Helper()
	raw, err := json.Marshal(detail)
	require.NoError(te.t, err)
	te.events = append(te.events, Event{Seq: len(te.events) + 1, At: pollEpoch.Add(at), Kind: kind, Detail: raw})
}

// record is one of the recorder's records (R183).
func (te *tabEvents) record(at time.Duration, kind string, detail any) {
	te.t.Helper()
	te.add(at, "record", map[string]any{"kind": kind, "t": at.Milliseconds(), "path": "/items/a", "detail": detail})
}

// start is a document's start.
func (te *tabEvents) start(at time.Duration) {
	te.t.Helper()
	te.record(at, "start", map[string]any{"order": "1.1", "etag": `"e1"`, "bar": "", "visibility": "visible"})
}

// poll is a poll's request, an XHR GET of the board.
func (te *tabEvents) poll(at time.Duration, id string) {
	te.t.Helper()
	te.add(at, "request", map[string]any{"requestId": id, "url": "https://127.0.0.1:8443/items/a/board", "method": "GET", "type": "XHR", "poll": true})
}

// answer is a poll's answer; a 200's body follows it.
func (te *tabEvents) answer(at time.Duration, id string, status int, replayed bool) {
	te.t.Helper()
	te.add(at, "response", map[string]any{"requestId": id, "status": status, "type": "XHR", "poll": true, "replayed": replayed})
}

func (te *tabEvents) body(at time.Duration, id, order, round string, replayed bool) {
	te.t.Helper()
	te.add(at, "body", map[string]any{"requestId": id, "order": order, "round": round, "replayed": replayed})
}

// fail is a poll that got no answer.
func (te *tabEvents) fail(at time.Duration, id, errorText string) {
	te.t.Helper()
	te.add(at, "response", map[string]any{"requestId": id, "type": "XHR", "failed": errorText, "poll": true})
}

// pollWant is what a test asks of one poll; a gap below 0 is none.
type pollWant struct {
	id        string
	first     bool
	gap       float64
	status    int
	failed    string
	replayed  bool
	order     string
	changed   []string
	announced int
}

// noGap is a poll's want of no gap.
const noGap = -1

// pollRows reads, from a tab's events, each poll's time, its gap since the
// last answer, its answer and what the recorder saw it change. The cases are
// the shapes the gate's walk reads polls in: live.js's backoff after failed
// answers, a hidden tab, a replayed answer beside a 304, a redirect, a new
// document, and two writes to #announce in one MutationObserver callback.
func TestPollRowsReadEachPollsAnswerAndWhatItChanged(t *testing.T) {
	s := time.Second
	ms := time.Millisecond
	cases := []struct {
		name   string
		events func(te *tabEvents)
		want   []pollWant
	}{{
		// Row 13: after each failed answer in a row, live.js waits 10, 20,
		// 40, then 60 s, from the answer, a 503 or a failure alike.
		name: "the gaps after failed answers, a 503 or no answer at all",
		events: func(te *tabEvents) {
			te.start(0)
			te.poll(5*s, "r1")
			te.answer(5*s+100*ms, "r1", 200, false)
			te.body(5*s+150*ms, "r1", "2.2", "1", false)
			te.poll(10*s+100*ms, "r2")
			te.answer(10*s+200*ms, "r2", 503, false)
			te.poll(20*s+200*ms, "r3")
			te.answer(20*s+300*ms, "r3", 503, false)
			te.poll(40*s+300*ms, "r4")
			te.fail(40*s+400*ms, "r4", "net::ERR_CONNECTION_REFUSED")
			te.poll(80*s+400*ms, "r5")
			te.answer(80*s+500*ms, "r5", 503, false)
			te.poll(140*s+500*ms, "r6")
			te.answer(140*s+600*ms, "r6", 200, false)
			te.body(140*s+650*ms, "r6", "3.3", "1", false)
		},
		want: []pollWant{
			{id: "r1", first: true, gap: noGap, status: 200, order: "2.2"},
			{id: "r2", gap: 5, status: 503},
			{id: "r3", gap: 10, status: 503},
			{id: "r4", gap: 20, failed: "net::ERR_CONNECTION_REFUSED"},
			{id: "r5", gap: 40, status: 503},
			{id: "r6", gap: 60, status: 200, order: "3.3"},
		},
	}, {
		// Row 8: no poll while the tab is hidden, and the first once it is
		// visible again spans the minutes hidden.
		name: "a hidden tab's minutes",
		events: func(te *tabEvents) {
			te.start(0)
			te.poll(5*s, "r1")
			te.answer(5*s+100*ms, "r1", 304, false)
			te.record(7*s, "visibility", map[string]string{"state": "hidden"})
			te.record(127*s, "visibility", map[string]string{"state": "visible"})
			te.poll(127*s+300*ms, "r2")
			te.answer(127*s+400*ms, "r2", 304, false)
		},
		want: []pollWant{
			{id: "r1", first: true, gap: noGap, status: 304},
			{id: "r2", gap: 122.2, status: 304},
		},
	}, {
		// Row 10: a replayed 200 is told from a 304, and from the real 200
		// after it, which changes what the replay did not.
		name: "a replayed answer beside a 304",
		events: func(te *tabEvents) {
			te.start(0)
			te.poll(5*s, "r1")
			te.answer(5*s+100*ms, "r1", 200, false)
			te.body(5*s+150*ms, "r1", "3.3", "2", false)
			te.record(5*s+200*ms, "region", map[string]string{"region": "feed", "v": "f3", "how": "replaced"})
			te.poll(10*s+100*ms, "r2")
			te.answer(10*s+200*ms, "r2", 200, true)
			te.body(10*s+250*ms, "r2", "2.2", "1", true)
			te.poll(15*s+200*ms, "r3")
			te.answer(15*s+300*ms, "r3", 304, false)
			te.poll(20*s+300*ms, "r4")
			te.answer(20*s+400*ms, "r4", 200, false)
			te.body(20*s+450*ms, "r4", "4.4", "2", false)
			te.record(20*s+500*ms, "region", map[string]string{"region": "summary", "v": "m4", "how": "replaced"})
		},
		want: []pollWant{
			{id: "r1", first: true, gap: noGap, status: 200, order: "3.3", changed: []string{"feed"}},
			{id: "r2", gap: 5, status: 200, replayed: true, order: "2.2"},
			{id: "r3", gap: 5, status: 304},
			{id: "r4", gap: 5, status: 200, order: "4.4", changed: []string{"summary"}},
		},
	}, {
		// A redirect is neither an answer nor its time: the answer it led to
		// is.
		name: "a redirect skipped",
		events: func(te *tabEvents) {
			te.start(0)
			te.poll(5*s, "r1")
			te.add(5*s+100*ms, "response", map[string]any{"requestId": "r1", "status": 302, "type": "XHR", "redirect": true})
			te.answer(6*s, "r1", 200, false)
			te.body(6*s+50*ms, "r1", "2.2", "1", false)
			te.poll(11*s, "r2")
			te.answer(11*s+100*ms, "r2", 304, false)
		},
		want: []pollWant{
			{id: "r1", first: true, gap: noGap, status: 200, order: "2.2"},
			{id: "r2", gap: 5, status: 304},
		},
	}, {
		// A new document's first poll is first again, has no gap from the
		// last document's answer, and takes nothing the recorder saw before
		// it; nor does the last document's poll take what the new one shows.
		name: "a new document",
		events: func(te *tabEvents) {
			te.start(0)
			te.poll(5*s, "r1")
			te.answer(5*s+100*ms, "r1", 200, false)
			te.body(5*s+150*ms, "r1", "2.2", "1", false)
			te.record(5*s+200*ms, "region", map[string]string{"region": "feed", "v": "f2", "how": "replaced"})
			te.start(110 * s)
			te.record(112*s, "region", map[string]string{"region": "summary", "v": "m9", "how": "in place"})
			te.poll(115*s, "r2")
			te.answer(115*s+100*ms, "r2", 304, false)
		},
		want: []pollWant{
			{id: "r1", first: true, gap: noGap, status: 200, order: "2.2", changed: []string{"feed"}},
			{id: "r2", first: true, gap: noGap, status: 304},
		},
	}, {
		// Row 6: the recorder's writes count each document's writes to
		// #announce, one MutationObserver callback a record; two writes in
		// one task are one record of writes 2, which are two writes.
		name: "two writes to #announce in one callback",
		events: func(te *tabEvents) {
			te.start(0)
			te.poll(5*s, "r1")
			te.answer(5*s+100*ms, "r1", 200, false)
			te.body(5*s+150*ms, "r1", "2.2", "2", false)
			te.record(5*s+200*ms, "announce", map[string]any{"writes": 2, "text": "two"})
			te.poll(10*s+100*ms, "r2")
			te.answer(10*s+200*ms, "r2", 200, false)
			te.body(10*s+250*ms, "r2", "3.3", "3", false)
			te.record(10*s+300*ms, "announce", map[string]any{"writes": 3, "text": "three"})
			te.start(60 * s)
			te.poll(65*s, "r3")
			te.answer(65*s+100*ms, "r3", 200, false)
			te.body(65*s+150*ms, "r3", "4.4", "4", false)
			te.record(65*s+200*ms, "announce", map[string]any{"writes": 1, "text": "four"})
		},
		want: []pollWant{
			{id: "r1", first: true, gap: noGap, status: 200, order: "2.2", announced: 2},
			{id: "r2", gap: 5, status: 200, order: "3.3", announced: 1},
			{id: "r3", first: true, gap: noGap, status: 200, order: "4.4", announced: 1},
		},
	}, {
		// Rows 6 and 9: a new document's recorder counts its writes from
		// none, so that its first poll's one write, the count the last
		// document ended at, is one write, not none: the write rows 6 and 9
		// look for at the first poll after a load.
		name: "a new document's first write, at the count the last ended at",
		events: func(te *tabEvents) {
			te.start(0)
			te.poll(5*s, "r1")
			te.answer(5*s+100*ms, "r1", 200, false)
			te.body(5*s+150*ms, "r1", "2.2", "2", false)
			te.record(5*s+200*ms, "announce", map[string]any{"writes": 1, "text": "two"})
			te.start(60 * s)
			te.poll(65*s, "r2")
			te.answer(65*s+100*ms, "r2", 200, false)
			te.body(65*s+150*ms, "r2", "3.3", "3", false)
			te.record(65*s+200*ms, "announce", map[string]any{"writes": 1, "text": "three"})
		},
		want: []pollWant{
			{id: "r1", first: true, gap: noGap, status: 200, order: "2.2", announced: 1},
			{id: "r2", first: true, gap: noGap, status: 200, order: "3.3", announced: 1},
		},
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			te := &tabEvents{t: t}
			c.events(te)
			rows := pollRows(te.events)
			require.Len(t, rows, len(c.want))
			for i, w := range c.want {
				p := rows[i]
				assert.Equal(t, w.id, p.RequestID, "poll %d", i)
				assert.Equal(t, w.first, p.First, "poll %s: first", w.id)
				if w.gap < 0 {
					assert.Nil(t, p.Gap, "poll %s: no gap", w.id)
				} else if assert.NotNil(t, p.Gap, "poll %s: a gap", w.id) {
					assert.InDelta(t, w.gap, *p.Gap, 0.001, "poll %s: gap", w.id)
				}
				assert.Equal(t, w.status, p.Status, "poll %s: status", w.id)
				assert.Equal(t, w.failed, p.Failed, "poll %s: failed", w.id)
				assert.Equal(t, w.replayed, p.Replayed, "poll %s: replayed", w.id)
				assert.Equal(t, w.order, p.Order, "poll %s: order", w.id)
				assert.ElementsMatch(t, w.changed, p.Changed, "poll %s: changed", w.id)
				assert.Equal(t, w.announced, p.Announced, "poll %s: announced", w.id)
				assert.NotNil(t, p.Answered, "poll %s: answered", w.id)
			}
		})
	}
}
