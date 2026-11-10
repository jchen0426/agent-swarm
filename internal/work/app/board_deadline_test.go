package app_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	actionsapp "github.com/jchen0426/agent-swarm/internal/actions/app"
	"github.com/jchen0426/agent-swarm/internal/work/app"
	"github.com/jchen0426/agent-swarm/internal/work/board"
	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// The counterexample's work type (review I6): six rounds of at most 150 s,
// and fifteen minutes of wall clock, six limits.
const (
	counterRounds = 6
	counterLimit  = 150 * time.Second
)

// counterexample makes current a revision of the harness's configuration
// whose incident type has the counterexample's budget.
func counterexample(h *harness) {
	h.t.Helper()
	h.revs.set(h.revision(strings.Repeat("9", 40), func(files map[string][]byte) {
		s := string(files["work-item-types/incident.yaml"])
		s = strings.Replace(s, "budget: {wallClock: 10m, tokens: 90000, rounds: 3}", "budget: {wallClock: 15m, tokens: 90000, rounds: 6}", 1)
		s = strings.Replace(s, "round: {timeLimit: 2m}", "round: {timeLimit: 150s}", 1)
		files["work-item-types/incident.yaml"] = []byte(s)
	}))
}

// pinnedHarness is a harness whose round ends are pinned at zero slack (plan
// review M1): Grace 5 s, a settling wait of 30 s and 1 ms, polled each second,
// and a retry wait of 5 s, under the counterexample's work type; every round
// end's first append is refused, and tried again after the retry wait.
func pinnedHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, func(o *app.Options) { o.SettleWait = 30*time.Second + time.Millisecond })
	counterexample(h)
	var mu sync.Mutex
	refused := map[roundOf]bool{}
	h.records.refuses(func(id item.ID, drafts []item.Draft) error {
		mu.Lock()
		defer mu.Unlock()
		for i := range drafts {
			if re := drafts[i].Body.RoundEnded; re != nil && !refused[roundOf{string(id), re.Round}] {
				refused[roundOf{string(id), re.Round}] = true
				return errRefused
			}
		}
		return nil
	})
	return h
}

// holdsOpen keeps the agent's turn open to the round's deadline, posting
// nothing.
func holdsOpen(*harness, *app.Turn) item.TurnClass { return "" }

// convergesHeldOpen posts as hypothesis does, and keeps the turn open to
// the round's deadline: with an agent that runs hypothesis, the hypothesis
// converges.
func convergesHeldOpen(h *harness, t *app.Turn) item.TurnClass {
	read := h.call(t, "logs__search", `{"q":"checkout"}`)
	require.Equal(h.t, actionsapp.StatusOK, read.Status, read.Text)
	ev := h.call(t, "post_evidence", fmt.Sprintf(`{"claim":"v42 went out a minute before the errors","call_id":%q,"quote":"checkout v42 deployed at 10:02"}`, read.CallID))
	require.Equal(h.t, actionsapp.StatusOK, ev.Status, ev.Text)
	r := h.call(t, "post_hypothesis", fmt.Sprintf(`{"key":{"service":"checkout","failureMode":"bad-release"},"statement":"v42 broke checkout","evidence_ids":[%q]}`,
		strings.TrimPrefix(ev.Text, "posted evidence ")))
	require.Equal(h.t, actionsapp.StatusOK, r.Status, r.Text)
	return ""
}

// stance reads the logs and posts evidence of stance on the hypothesis hyp
// gives, then ends the turn.
func stance(s board.Stance, hyp func(t *app.Turn) board.EntryID) script {
	return func(h *harness, t *app.Turn) item.TurnClass {
		read := h.call(t, "logs__search", `{"q":"checkout"}`)
		require.Equal(h.t, actionsapp.StatusOK, read.Status, read.Text)
		ev := h.call(t, "post_evidence", fmt.Sprintf(`{"claim":"the errors and v42","call_id":%q,"quote":"errors rose at 10:03","hypothesis_id":%q,"stance":%q}`,
			read.CallID, hyp(t), s))
		require.Equal(h.t, actionsapp.StatusOK, ev.Status, ev.Text)
		return endTurn(h, t)
	}
}

// plan is what each agent does in each round: plan[agent][round], and
// holdsOpen in any other.
type plan map[string]map[int]script

// play has the harness's agents play p.
func (p plan) play(h *harness) {
	for _, agent := range []string{release.Name, runtime.Name} {
		rounds := p[agent]
		h.scripts[agent] = func(h *harness, t *app.Turn) item.TurnClass {
			if s := rounds[t.Round]; s != nil {
				return s(h, t)
			}
			return holdsOpen(h, t)
		}
	}
}

// converging is a plan whose agents make the hypothesis converge in round
// 1 and hold every round open.
func converging() plan {
	return plan{release.Name: {1: convergesHeldOpen}, runtime.Name: {1: hypothesis}}
}

// read is the latest time a board showed, as of the round it was read in.
type read struct {
	round int
	by    time.Time
}

// deadlineRun drives an item's real rounds on a pinned harness, its clock
// stepped half a second at a time, its timers firing (design D5's test): each
// round's model call is in flight at the settle's last poll before Grace and
// SettleWait, so that each decision lands at the next poll, and is still in
// flight then unless the round is among settled. While the item
// investigates, its board is read after every step, or once a round unless
// every, through views and each of lacking (TestEachWaitTermIsNeeded).
type deadlineRun struct {
	h       *harness
	views   *app.Views
	lacking map[string]*app.Views
	every   bool
	settled map[int]bool
	id      item.ID
	run     board.RunID
	// round is the latest round seen open; onRound runs when one is.
	round   int
	onRound func(n int)
	// reads are the times read since the run's start or latest resume, and
	// lackingReads those of lacking, by name.
	reads        []read
	lackingReads map[string][]read
	stops        int
}

// newDeadlineRun is a run on h, whose agents play p.
func newDeadlineRun(h *harness, p plan, settled ...int) *deadlineRun {
	p.play(h)
	d := &deadlineRun{h: h, views: boardViews(h), every: true, settled: map[int]bool{}, lackingReads: map[string][]read{}}
	for _, n := range settled {
		d.settled[n] = true
	}
	return d
}

// started takes up the item opened, its round 1 open.
func (d *deadlineRun) started(id item.ID) {
	d.h.t.Helper()
	d.id, d.run = id, d.h.state(id).Run.ID
}

// act does f, then plays the turns it started, and looks at the item.
func (d *deadlineRun) act(f func()) {
	d.h.t.Helper()
	f()
	d.h.play()
	d.look()
}

// look acts on the item as the latest step or act left it: a round seen
// open for the first time has its model call in flight, and its onRound
// run; a settled round's call ends half a second after the settle's last
// poll before Grace and SettleWait; the board is read while the item
// investigates.
func (d *deadlineRun) look() {
	h := d.h
	h.t.Helper()
	s := h.state(d.id)
	l := s.Run.Latest()
	first := l != nil && l.Opened.Round != d.round
	if first {
		d.round = l.Opened.Round
		h.spend.set(func(sp *spend) { sp.inFlight[roundOf{string(d.run), d.round}] = 1 })
	}
	if l != nil && l.Ended == nil && d.settled[l.Opened.Round] &&
		!h.clock.Now().Before(l.Opened.Deadline.Add(h.opts.Grace+h.opts.SettleWait.Truncate(time.Second)+h.opts.SettlePoll/2)) {
		h.spend.set(func(sp *spend) { delete(sp.inFlight, roundOf{string(d.run), l.Opened.Round}) })
	}
	if s.Status() == item.StatusInvestigating && (d.every || first) {
		d.reads = append(d.reads, d.read(d.views, s))
		for name, v := range d.lacking {
			d.lackingReads[name] = append(d.lackingReads[name], d.read(v, s))
		}
	}
	if first && d.onRound != nil {
		d.onRound(d.round)
	}
}

// read reads the latest time the board shows through views, which is set
// while the run runs.
func (d *deadlineRun) read(views *app.Views, s *item.State) read {
	d.h.t.Helper()
	bv := boardOf(d.h.t, views, d.id)
	if s.Running() {
		require.False(d.h.t, bv.Header.StopBy.IsZero(), "the time is shown while the run runs, round %d", d.round)
	}
	return read{round: d.round, by: bv.Header.StopBy}
}

// untilStopped steps the run until it stops once more, and returns the stop's
// record, the round that stopped it, and the times read since the run's
// start or latest resume.
func (d *deadlineRun) untilStopped() (stop item.Record, last int, reads []read) {
	h := d.h
	h.t.Helper()
	for range 20_000 {
		h.clock.Advance(500 * time.Millisecond)
		h.play()
		d.look()
		s := h.state(d.id)
		stops := len(s.Run.Stops)
		if s.Run.Stopped != nil {
			stops++
		}
		if stops > d.stops {
			d.stops = stops
			reads, d.reads = d.reads, nil
			return lastRecordOf(h, d.id, func(b *item.Body) bool { return b.RunStopped != nil }), d.round, reads
		}
	}
	require.FailNow(h.t, "the run never stopped")
	return item.Record{}, 0, nil
}

// noLaterThanShown requires that the stop came no later than any time read
// since the run's start or latest resume (design D5). Pinned at zero slack,
// each round end took W less 1 ms, so that each time is the stop to within
// 1 ms for each round end from its read's to the last.
func noLaterThanShown(t *testing.T, stop *item.Record, last int, reads []read, pinned bool) {
	t.Helper()
	require.NotEmpty(t, reads)
	for _, r := range reads {
		assert.False(t, stop.At.After(r.by), "the stop at %s, after the time %s read in round %d", stop.At, r.by, r.round)
		if pinned {
			assert.LessOrEqual(t, r.by.Sub(stop.At), time.Duration(last-r.round+1)*time.Millisecond,
				"the time read in round %d is the stop to within 1 ms a round end", r.round)
		}
	}
}

// budgetUsed is subtest 1's run: its agents converge in round 1, both
// wrap-up rounds end unsettled, so they conclude nothing and count, and the
// run uses its whole budget, six normal rounds, and stops budget_exhausted.
func budgetUsed(t *testing.T, d *deadlineRun) {
	t.Helper()
	d.act(func() { d.started(d.h.open("checkout fails")) })

	stop, last, reads := d.untilStopped()

	noLaterThanShown(t, &stop, last, reads, true)
	s := d.h.state(d.id)
	assert.Equal(t, item.StopBudgetExhausted, s.Run.Stopped.Reason)
	require.Len(t, s.Run.Rounds, counterRounds+item.MaxWrapUps)
	for i, wrapUp := range []int{1, 2} {
		r := s.Run.Rounds[wrapUp]
		assert.NotEmpty(t, r.Opened.WrapUp, "round %d is a wrap-up round", wrapUp+1)
		assert.False(t, r.Ended.Settled, "wrap-up %d ended unsettled", i+1)
	}
	assert.Equal(t, counterRounds, s.Run.Stopped.Rounds)
	assert.Equal(t, counterRounds*counterLimit, s.Run.Stopped.WallClock, "every normal round ran to its deadline")
}

// revokedPlan is subtest 2's plan: its agents converge in round 5; in the
// first wrap-up round, round 6, one refutes the hypothesis; in round 7, the
// budget's sixth, it supports it again, and they converge again; in the
// second wrap-up round one agent ends its turn and the other holds the round
// to its deadline, its last call ending before the decision's poll.
func revokedPlan() plan {
	var hyp board.EntryID
	refute := stance(board.Refutes, func(t *app.Turn) board.EntryID { hyp = t.WrapUp; return hyp })
	support := stance(board.Supports, func(*app.Turn) board.EntryID { return hyp })
	return plan{release.Name: {5: convergesHeldOpen}, runtime.Name: {5: hypothesis, 6: refute, 7: support, 8: endTurn}}
}

// revoked is subtest 2's run (review I6's counterexample A): its stop, the
// second wrap-up round's, settled and taken part in, is converged, and comes
// after design v1's time, the start plus B plus 2L.
func revoked(t *testing.T, d *deadlineRun) {
	t.Helper()
	d.act(func() { d.started(d.h.open("checkout fails")) })

	stop, last, reads := d.untilStopped()

	noLaterThanShown(t, &stop, last, reads, true)
	s := d.h.state(d.id)
	assert.Equal(t, item.StopConverged, s.Run.Stopped.Reason)
	require.Len(t, s.Run.Rounds, 8)
	assert.Equal(t, []bool{true, false, true}, []bool{s.Run.Rounds[5].Opened.WrapUp != "", s.Run.Rounds[6].Opened.WrapUp != "", s.Run.Rounds[7].Opened.WrapUp != ""})
	assert.True(t, s.Run.Rounds[7].Ended.Settled, "the second wrap-up round settled")
	created := lastRecordOf(d.h, d.id, func(b *item.Body) bool { return b.RunCreated != nil }).At
	assert.True(t, stop.At.After(created.Add(counterRounds*counterLimit+2*counterLimit)), "after design v1's time, so the test bites")
}

// The run stops no later than the time its board shows (design D5's test;
// review I6; plan review M1): on the harness's real rounds, its clock stepped
// half a second at a time, its timers firing, the board is read after every
// step while the item investigates, and each run_stopped record's time is
// not after any time read since the run's start or latest resume. Every
// subtest is pinned at zero slack: each round runs to its deadline, an agent
// holding its turn open; a model call of it is in flight at the settle's
// last poll before Grace and SettleWait; and every round end's first append
// is refused and tried again after the retry wait. Each end then takes W
// less 1 ms, so that the time read is the stop to within 1 ms a round end.
// Fails first: no StopBy.
func TestTheRunStopsNoLaterThanTheTimeItsBoardShows(t *testing.T) {
	t.Run("the budget used", func(t *testing.T) {
		h := pinnedHarness(t)
		budgetUsed(t, newDeadlineRun(h, converging()))
	})
	t.Run("converged, revoked, converged again", func(t *testing.T) {
		h := pinnedHarness(t)
		revoked(t, newDeadlineRun(h, revokedPlan(), 8))
	})
	t.Run("a person's extension", func(t *testing.T) {
		h := pinnedHarness(t)
		d := newDeadlineRun(h, converging())
		budgetUsed(t, d)
		require.Equal(t, item.StatusAwaitingClose, h.state(d.id).Status())

		d.act(func() {
			_, err := h.c.Extend(t.Context(), d.id, item.Amount{Rounds: new(2), WallClock: new(2 * counterLimit)}, extending("extend-1"))
			require.NoError(t, err)
		})
		stop, last, reads := d.untilStopped()

		noLaterThanShown(t, &stop, last, reads, true)
		s := h.state(d.id)
		assert.Equal(t, item.StopBudgetExhausted, s.Run.Stopped.Reason, "at the resumed rounds' end")
		assert.Len(t, s.Run.Rounds, counterRounds+item.MaxWrapUps+2, "the resumed rounds")
		assert.Equal(t, []int{counterRounds + item.MaxWrapUps + 1, counterRounds + item.MaxWrapUps + 2}, roundsOf(reads),
			"read from the resume on: the first resumed round with a normal round still to open, n 1, and the extension's wall clock left")
	})
	t.Run("a firing resume", func(t *testing.T) {
		h := pinnedHarness(t)
		d := newDeadlineRun(h, converging())
		d.onRound = func(n int) {
			if n == 3 {
				require.Equal(t, app.IngestAppended, ingest(h, trigger("r1", "i", "d", item.Resolved, h.clock.Now())).Outcome)
			}
		}
		d.act(func() { d.started(ingest(h, trigger("f1", "i", "d", item.Firing, h.clock.Now())).Item) })

		stop, last, reads := d.untilStopped()

		noLaterThanShown(t, &stop, last, reads, false)
		s := h.state(d.id)
		require.Equal(t, item.StopTriggerResolved, s.Run.Stopped.Reason, "its alert resolved in round 3")
		require.Len(t, s.Run.Rounds, 3)
		require.Equal(t, item.StatusAwaitingClose, s.Status())
		budget := s.Run.Budget()

		d.act(func() { ingest(h, trigger("f2", "i", "d", item.Firing, h.clock.Now())) })
		requireResumedInto(t, h, d.id, 4)
		stop, last, reads = d.untilStopped()

		noLaterThanShown(t, &stop, last, reads, true)
		s = h.state(d.id)
		assert.Equal(t, item.StopBudgetExhausted, s.Run.Stopped.Reason)
		assert.Equal(t, budget, s.Run.Budget(), "a firing resume adds no budget")
		assert.Len(t, s.Run.Rounds, 3+counterRounds-1, "the resumed rounds use the budget left")
	})
}

// roundsOf lists the distinct rounds reads were read in, in order.
func roundsOf(reads []read) []int {
	var out []int
	for _, r := range reads {
		if len(out) == 0 || out[len(out)-1] != r.round {
			out = append(out, r.round)
		}
	}
	return out
}

// Each wait term is needed (the plan's; plan review M1, its mutation row): in
// subtests 1 and 2's runs, views beside the real ones whose waits lack 1 s of
// Grace, of Settle, of Poll or of Retry each read, during the run's last
// round, a time before its stop, which the real views' time is not.
func TestEachWaitTermIsNeeded(t *testing.T) {
	for name, run := range map[string]struct {
		plan    func() plan
		settled []int
		drive   func(t *testing.T, d *deadlineRun)
	}{
		"the budget used":                     {converging, nil, budgetUsed},
		"converged, revoked, converged again": {revokedPlan, []int{8}, revoked},
	} {
		t.Run(name, func(t *testing.T) {
			h := pinnedHarness(t)
			d := newDeadlineRun(h, run.plan(), run.settled...)
			d.every = false
			lack := map[string]func(w *item.Waits){
				"Grace":  func(w *item.Waits) { w.Grace -= time.Second },
				"Settle": func(w *item.Waits) { w.Settle -= time.Second },
				"Poll":   func(w *item.Waits) { w.Poll -= time.Second },
				"Retry":  func(w *item.Waits) { w.Retry -= time.Second },
			}
			d.lacking = map[string]*app.Views{}
			for term, less := range lack {
				d.lacking[term] = viewsWith(h, func(p *app.ViewPorts) {
					p.Waits, p.Titles = waitsOf(h), h.records
					less(&p.Waits)
				})
			}

			run.drive(t, d)

			s := h.state(d.id)
			last := len(s.Run.Rounds)
			stop := lastRecordOf(h, d.id, func(b *item.Body) bool { return b.RunStopped != nil })
			for term := range lack {
				reads := d.lackingReads[term]
				require.NotEmpty(t, reads)
				r := reads[len(reads)-1]
				require.Equal(t, last, r.round, "read in the last round")
				assert.True(t, r.by.Before(stop.At), "lacking 1 s of %s, the time read in the last round is before the stop", term)
			}
		})
	}
}
