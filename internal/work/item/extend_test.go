package item_test

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/item"
)

// extension is a person's extension of the item's run by amount, as its
// record holds it; seen is a resume's kill switch row, 0 for none.
func extension(amount item.Budget, seen int64) *item.Extended {
	return &item.Extended{Amount: amount, KillSwitchSeen: seen, By: "admin", Request: item.Request{Key: "sha256:07", Digest: "sha256:08"}}
}

// oneRound is an extension of one round and five minutes of wall clock.
var oneRound = item.Budget{Rounds: 1, WallClock: 5 * time.Minute}

// plus is the budget b with the amount a added.
func plus(b, a item.Budget) item.Budget {
	return item.Budget{Rounds: b.Rounds + a.Rounds, WallClock: b.WallClock + a.WallClock, Tokens: b.Tokens + a.Tokens}
}

// stopAndAwait records the run's stop as its latest round decided, and the
// item's awaiting its close with the reason ItemReason gives.
func (l *life) stopAndAwait() item.RunStopped {
	l.t.Helper()
	st := l.s.Run.Stop("")
	l.write("run-1", item.Body{RunStopped: &st})
	l.await()
	return st
}

// await records the item's awaiting its close, its run stopped.
func (l *life) await() {
	l.t.Helper()
	l.write("", item.Body{AwaitingClose: &item.AwaitingClose{Reason: item.ItemReason(l.s.Run.Stopped.Reason)}})
}

// halt records the halt of the run for reason, as Run.Halt gives it.
func (l *life) halt(reason item.StopReason, e *item.Ending) {
	l.t.Helper()
	if e.EndedAt.IsZero() {
		e.EndedAt = l.now
	}
	for _, b := range l.s.Run.Halt(reason, e) {
		l.write("run-1", b)
	}
}

// extend records an extension of the run by amount, seen as extension says.
func (l *life) extend(amount item.Budget, seen int64) {
	l.t.Helper()
	l.write("run-1", item.Body{Extended: extension(amount, seen)})
}

// A run whose budget is used stops, and its item awaits its close; an
// extension then resumes it (Decision 16): the item investigates again, the
// run has no stop, its earlier stop is kept in its history, and the item no
// longer awaits its close.
func TestAnExtensionResumesAStoppedRun(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 1
	l := newLife(t, rc)
	l.open()
	require.Equal(t, item.StopBudgetExhausted, l.end(time.Minute, busy()).Decision.Stop)
	st := l.stopAndAwait()
	require.Equal(t, item.StatusAwaitingClose, l.s.Status())

	l.extend(oneRound, 3)

	assert.Equal(t, item.StatusInvestigating, l.s.Status())
	assert.Nil(t, l.s.Run.Stopped)
	assert.Equal(t, []item.RunStopped{st}, l.s.Run.Stops)
	assert.Nil(t, l.s.Closing)
	assert.True(t, l.s.Run.Resumed())
}

// A run whose wrap-up round converged stops so; resumed, it opens its next
// round as a normal one, outside the wrap-up, with a normal round's deadline
// and the budget its extension raised, and the fold takes that round.
func TestAResumedRunOpensANormalRoundAfterItsDecidedStop(t *testing.T) {
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, converging(hyp))
	l.open()
	require.Equal(t, item.StopConverged, l.end(time.Minute, converging(hyp)).Decision.Stop)
	l.stopAndAwait()
	l.extend(item.Budget{Rounds: 1, WallClock: 5 * time.Minute, Tokens: 30_000}, 0)

	next := l.s.Run.Open(l.now)

	assert.Equal(t, 3, next.Round)
	assert.Empty(t, next.WrapUp, "a normal round")
	assert.Equal(t, l.now.Add(2*time.Minute), next.Deadline, "the round's limit, the wall clock left being more")
	assert.Equal(t, int64(120_000), next.Budget, "the run's tokens, its extension's included")
	l.open()
	assert.False(t, l.s.Run.Resumed())
	assert.Len(t, l.s.Run.Rounds, 3)
}

// A run created knowing row 2, killed by row 3 before its first round, and
// resumed knowing row 4 owes the kill of a later on-row only: the owed-kill
// rule reads the largest of its creation's row and its resumes' (Decision 16).
func TestAResumedRunOwesAKillOnlyForAnOnAfterItsResume(t *testing.T) {
	rc := created()
	rc.KillSwitchSeen = 2
	l := newLife(t, rc)
	require.True(t, l.s.Owes(3))
	l.halt(item.StopKilled, &item.Ending{KilledBy: 3})
	l.await()
	require.False(t, l.s.Owes(5), "a stopped run owes nothing")

	l.extend(oneRound, 4)

	assert.False(t, l.s.Owes(3), "the on-row that killed it")
	assert.False(t, l.s.Owes(4), "the row its resume knew")
	assert.True(t, l.s.Owes(5), "an on after its resume")
	assert.Equal(t, int64(4), l.s.Run.KillSwitchSeen())
	assert.Equal(t, int64(4), l.s.KillSwitchSeen())
}

// An extension of a run that runs records no kill switch row: the fold
// refuses one, and the run's row stays its creation's. A resume records the
// row the coordinator knew, 0 included, where no row was ever written, and is
// a resume all the same (the plan review's I3).
func TestAnExtensionOfARunningRunRecordsNoKillSwitchSeen(t *testing.T) {
	rc := created()
	rc.KillSwitchSeen = 2
	l := newLife(t, rc)
	l.open()

	require.ErrorIs(t, l.try("run-1", item.Body{Extended: extension(oneRound, 5)}), item.ErrInvalid)
	require.NoError(t, l.try("run-1", item.Body{Extended: extension(oneRound, 0)}))
	assert.Equal(t, int64(2), l.s.Run.KillSwitchSeen())
	assert.Empty(t, l.s.Run.Stops)
	assert.False(t, l.s.Run.Resumed())

	r := newLife(t, created())
	r.open()
	r.end(time.Minute, quiet())
	r.stopAndAwait()
	r.extend(oneRound, 0)
	assert.Equal(t, item.StatusInvestigating, r.s.Status())
	assert.True(t, r.s.Run.Resumed())
	assert.Len(t, r.s.Run.Stops, 1)
	assert.Equal(t, int64(0), r.s.Run.KillSwitchSeen())
}

// An extension raises a running run's budget at once (Decision 17): a round
// that used the creation's rounds no longer decides the run's stop.
func TestAnExtensionRaisesARunningRunsBudget(t *testing.T) {
	build := func(extended bool) *life {
		rc := created()
		rc.Budget.Rounds = 1
		l := newLife(t, rc)
		l.open()
		if extended {
			l.extend(item.Budget{Rounds: 2, Tokens: 10_000}, 0)
		}
		return l
	}
	plain, extended := build(false), build(true)

	assert.Equal(t, item.Budget{WallClock: 10 * time.Minute, Tokens: 100_000, Rounds: 3}, extended.s.Run.Budget())
	assert.Equal(t, created().Budget.Tokens, plain.s.Run.Budget().Tokens)
	assert.Equal(t, item.StopBudgetExhausted, plain.end(time.Minute, busy()).Decision.Stop, "the creation's rounds alone")
	assert.Equal(t, item.Decision{Next: item.NextRound}, extended.end(time.Minute, busy()).Decision, "the rounds extended")
}

// An extension of N rounds brings, for each dimension it leaves out, N
// rounds' share of the run's creation's budget, by integer division (R55); a
// dimension named, zero included, is taken as named (R70). An amount above
// the creation's budget in any dimension, or of nothing, or negative, is
// invalid.
func TestAnExtensionOfRoundsBringsTheirShareOfTokensAndWallClock(t *testing.T) {
	createdWith := item.Budget{Rounds: 6, WallClock: 30 * time.Minute, Tokens: 6_000_000}
	n := func(v int) *int { return &v }
	d := func(v time.Duration) *time.Duration { return &v }
	tok := func(v int64) *int64 { return &v }

	for name, c := range map[string]struct {
		asked item.Amount
		want  item.Budget
	}{
		"one round":               {item.Amount{Rounds: n(1)}, item.Budget{Rounds: 1, WallClock: 5 * time.Minute, Tokens: 1_000_000}},
		"one round, no tokens":    {item.Amount{Rounds: n(1), Tokens: tok(0)}, item.Budget{Rounds: 1, WallClock: 5 * time.Minute}},
		"wall clock alone":        {item.Amount{WallClock: d(time.Minute)}, item.Budget{WallClock: time.Minute}},
		"every dimension named":   {item.Amount{Rounds: n(2), WallClock: d(time.Minute), Tokens: tok(7)}, item.Budget{Rounds: 2, WallClock: time.Minute, Tokens: 7}},
		"the creation's, exactly": {item.Amount{Rounds: n(6)}, createdWith},
	} {
		got, err := item.ExtensionAmount(createdWith, c.asked)

		require.NoError(t, err, name)
		assert.Equal(t, c.want, got, name)
	}
	for name, asked := range map[string]item.Amount{
		"rounds above the creation's":     {Rounds: n(7)},
		"wall clock above the creation's": {WallClock: d(31 * time.Minute)},
		"tokens above the creation's":     {Tokens: tok(6_000_001)},
		"nothing":                         {},
		"all zero":                        {Rounds: n(0), WallClock: d(0), Tokens: tok(0)},
		"negative rounds":                 {Rounds: n(-1), Tokens: tok(5)},
		"negative wall clock":             {Rounds: n(1), WallClock: d(-time.Second)},
		"negative tokens":                 {Rounds: n(1), Tokens: tok(-1)},
	} {
		_, err := item.ExtensionAmount(createdWith, asked)

		require.ErrorIs(t, err, item.ErrInvalid, name)
	}
}

// With no round lost to an outage or a takeover, a run stops within its
// creation's rounds, its extensions' rounds and the wrap-up rounds a
// convergence may add, however it is extended while it runs, halted, and
// resumed where an extension would open a round (R56): every normal round
// counts toward the budget, its extensions' included, and every wrap-up round
// uses up a wrap-up of the run's, across its resumes. The runs are drawn from
// fixed seeds, as Plan 7a's are.
func TestARunStopsWithinItsBudgetAndItsExtensions(t *testing.T) {
	for seed := range uint64(500) {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 3))
			rc := created()
			rc.Budget.Rounds = 1 + rng.IntN(4)
			l := newLife(t, rc)
			var spent, row int64

			for resumes := 0; ; resumes++ {
				for stopped := false; !stopped; {
					require.Less(t, len(l.s.Run.Rounds), 50, "the run went on")
					o := l.open()
					if rng.IntN(4) == 0 {
						l.extend(item.Budget{Rounds: rng.IntN(3), Tokens: 1 + rng.Int64N(9_999)}, 0)
					}
					spent += 1 + rng.Int64N(9_999)
					limit := int(o.Deadline.Sub(o.OpenedAt) / time.Second)
					d := time.Duration(1+rng.IntN(limit)) * time.Second
					if rng.IntN(8) == 0 {
						l.now = l.now.Add(d)
						row++
						l.halt(item.StopKilled, &item.Ending{Spent: spent, KilledBy: row})
						break
					}
					stopped = l.end(d, drawnEnding(rng, o.Agents, spent)).Decision.Next == item.NextStop
				}
				if l.s.Run.Stopped == nil {
					l.stopAndAwait()
				} else {
					l.await()
				}
				amount := item.Budget{Rounds: rng.IntN(3), WallClock: time.Duration(rng.IntN(6)) * time.Minute, Tokens: rng.Int64N(10_000)}
				if amount == (item.Budget{}) || resumes == 3 || rng.IntN(3) == 0 || !l.s.Run.WouldOpen(plus(l.s.Run.Budget(), amount)) {
					break
				}
				l.extend(amount, row)
			}

			normal, wrapUps := 0, 0
			for _, r := range l.s.Run.Rounds {
				if r.Opened.WrapUp != "" {
					wrapUps++
				} else {
					normal++
				}
			}
			assert.LessOrEqual(t, normal, l.s.Run.Budget().Rounds, "normal rounds past the budget and its extensions")
			assert.LessOrEqual(t, wrapUps, item.MaxWrapUps, "wrap-up rounds past the run's wrap-ups")
			assert.NotNil(t, l.s.Run.Stopped, "the run stopped")
		})
	}
}

// R5: a round's sessions may spend the round's recorded budget and the tokens
// extended since it opened; an extension before the round opened is in its
// recorded budget already, and counted once.
func TestASessionBudgetCountsTheTokensExtendedSinceTheRoundOpened(t *testing.T) {
	rc := created()
	rc.Budget.Tokens = 1_000
	l := newLife(t, rc)
	first := l.open()
	require.Equal(t, int64(1_000), first.Budget)

	l.extend(item.Budget{Tokens: 500}, 0)

	assert.Equal(t, int64(1_500), l.s.Run.SessionBudget(1))
	l.end(time.Minute, busy())
	second := l.open()
	assert.Equal(t, int64(1_500), second.Budget, "the extension is in the next round's recorded budget")
	assert.Equal(t, int64(1_500), l.s.Run.SessionBudget(2), "and counted once")
	assert.Equal(t, int64(1_500), l.s.Run.SessionBudget(1), "the first round's sessions keep theirs")
}

// A run killed between rounds, or before its first, and then resumed is told
// resumed until its next round opens (R67): that round opens as a normal one,
// numbered after the last, and the fold takes it.
func TestAResumedRunKilledBetweenRoundsOpensItsNextRound(t *testing.T) {
	l := newLife(t, created())
	l.open()
	l.end(time.Minute, busy())
	l.open()
	require.Equal(t, item.Decision{Next: item.NextRound}, l.end(time.Minute, busy()).Decision)
	l.halt(item.StopKilled, &item.Ending{KilledBy: 1})
	require.Equal(t, item.StopKilled, l.s.Run.Stopped.Reason)
	l.await()
	assert.False(t, l.s.Run.Resumed(), "stopped, not resumed")

	l.extend(oneRound, 1)

	assert.True(t, l.s.Run.Resumed())
	next := l.s.Run.Open(l.now)
	assert.Equal(t, 3, next.Round)
	assert.Empty(t, next.WrapUp)
	l.open()
	assert.False(t, l.s.Run.Resumed(), "a round opened since")

	before := newLife(t, created())
	before.halt(item.StopKilled, &item.Ending{KilledBy: 1})
	before.await()
	before.extend(oneRound, 1)
	assert.True(t, before.s.Run.Resumed())
	assert.Equal(t, 1, before.open().Round)
	assert.False(t, before.s.Run.Resumed())
}

// A run whose round decided budget_exhausted, stopped and resumed, halted
// before its next round, stops for the halt's own reason: the old decision
// is not read again (R67), and the fold takes the stop. Run.Stop gives no
// old decided reason either.
func TestAHaltOfAResumedRunBeforeItsRoundRecordsItsOwnReason(t *testing.T) {
	build := func() *life {
		rc := created()
		rc.Budget.Rounds = 2
		l := newLife(t, rc)
		l.open()
		l.end(time.Minute, busy())
		l.open()
		require.Equal(t, item.StopBudgetExhausted, l.end(time.Minute, busy()).Decision.Stop)
		l.stopAndAwait()
		l.extend(oneRound, 0)
		return l
	}

	for _, reason := range []item.StopReason{item.StopKilled, item.StopClosed} {
		l := build()
		bodies := l.s.Run.Halt(reason, &item.Ending{EndedAt: l.now, KilledBy: 5})
		require.Len(t, bodies, 1, reason)
		require.NotNil(t, bodies[0].RunStopped, reason)
		assert.Equal(t, reason, bodies[0].RunStopped.Reason)
		assert.Equal(t, 2, bodies[0].RunStopped.Rounds, reason)
		require.NoError(t, l.try("run-1", bodies[0]), reason)
		assert.Equal(t, reason, l.s.Run.Stopped.Reason)
	}
	assert.NotEqual(t, item.StopBudgetExhausted, build().s.Run.Stop("").Reason)
}

// R56: an extension would open a round only if the latest ended round's
// usage, under the budget after it, is not exhausted: wall clock alone on a
// run whose rounds are spent would not; one round would. A run with no
// ended round would open one.
func TestAnExtensionWouldOpenARoundOnlyIfItsBudgetAllowsOne(t *testing.T) {
	rc := created()
	rc.Budget.Rounds = 1
	l := newLife(t, rc)
	assert.True(t, l.s.Run.WouldOpen(l.s.Run.Budget()), "no ended round")
	l.open()
	l.end(time.Minute, busy())

	assert.False(t, l.s.Run.WouldOpen(plus(l.s.Run.Budget(), item.Budget{WallClock: 5 * time.Minute})))
	assert.True(t, l.s.Run.WouldOpen(plus(l.s.Run.Budget(), item.Budget{Rounds: 1})))
}

// A run's latest resume is the amount it brought, for the board's resume
// entry (R58); a running run's extension after it is not; and a run that
// concludes is one whose latest round decided a stop not recorded, not
// resumed (R74).
func TestALatestResumeAndAConclusion(t *testing.T) {
	l := newLife(t, created())
	_, ok := l.s.Run.LatestResume()
	assert.False(t, ok)
	l.open()
	l.end(time.Minute, quiet())
	assert.True(t, l.s.Run.Concludes())
	l.stopAndAwait()
	assert.False(t, l.s.Run.Concludes(), "stopped")
	l.extend(oneRound, 0)
	assert.False(t, l.s.Run.Concludes(), "resumed")
	l.open()
	l.extend(item.Budget{Tokens: 9}, 0)

	got, ok := l.s.Run.LatestResume()

	assert.True(t, ok)
	assert.Equal(t, oneRound, got)
	assert.Equal(t, plus(plus(created().Budget, oneRound), item.Budget{Tokens: 9}), l.s.Run.Budget())
}

// A run's latest resume knows its cause, which the board's resume entry
// posts (R119): a person's extension's is empty, a firing's is firing; a run
// never resumed has none. (The plan's.)
func TestALatestResumeKnowsItsCause(t *testing.T) {
	l := newLife(t, created())
	assert.Equal(t, item.ExtendCause(""), l.s.Run.LatestResumeCause(), "never resumed")
	l.open()
	l.end(time.Minute, quiet())
	l.stopAndAwait()
	l.extend(oneRound, 0)
	assert.Equal(t, item.ExtendCause(""), l.s.Run.LatestResumeCause(), "an extension's resume")
	l.open()
	l.end(time.Minute, quiet())
	l.stopAndAwait()
	l.write("run-1", item.Body{Extended: firing()})
	assert.Equal(t, item.ExtendFiring, l.s.Run.LatestResumeCause(), "a firing's")
	l.extend(item.Budget{Tokens: 9}, 0)
	assert.Equal(t, item.ExtendFiring, l.s.Run.LatestResumeCause(), "an extension of the running run resumes nothing")
}
