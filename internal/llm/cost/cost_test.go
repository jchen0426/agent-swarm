package cost_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/llm/cost"
)

// card prices a model as the vendor lists it, in pico-USD per token: $0.10
// and $0.50 per million input and output tokens, five times that for a
// prompt over 100,000 tokens.
var card = cost.Card{
	Threshold: 100_000,
	Base:      cost.Rates{Input: 100_000, Output: 500_000, CacheWrite5m: 125_000, CacheWrite1h: 200_000, CacheRead: 10_000},
	Long:      cost.Rates{Input: 500_000, Output: 2_500_000, CacheWrite5m: 625_000, CacheWrite1h: 1_000_000, CacheRead: 50_000},
}

// A call costs each kind of token at the model's rate for it, and the long
// rates once its prompt, cached or not, passes the threshold.
func TestACallCostsWhatTheVendorCharges(t *testing.T) {
	short := cost.Usage{Input: 1000, CacheWrite5m: 400, CacheWrite1h: 100, CacheRead: 20000, Output: 300}
	long := cost.Usage{Input: 1000, CacheRead: 99_001, Output: 10}

	assert.Equal(t, int64(1000*100_000+400*125_000+100*200_000+20000*10_000+300*500_000), card.Spend(short))
	assert.Equal(t, int64(1000*500_000+99_001*50_000+10*2_500_000), card.Spend(long), "100,001 prompt tokens")
	assert.Equal(t, int64(21_500), short.Prompt())
}

// A card without a threshold has one set of rates.
func TestACardWithoutAThresholdHasOneRate(t *testing.T) {
	flat := &cost.Card{Base: card.Base}

	assert.Equal(t, int64(1_000_000*100_000), flat.Spend(cost.Usage{Input: 1_000_000}))
}

// Cost-weighted tokens are spend at the reference price of one token,
// rounded up, so that budgets and the daily cap bound money whichever model
// an agent uses.
func TestWeightedTokensAreSpendAtTheReferencePrice(t *testing.T) {
	assert.Equal(t, int64(3), cost.Weighted(250_001, 100_000))
	assert.Equal(t, int64(2), cost.Weighted(200_000, 100_000))
	assert.Equal(t, int64(0), cost.Weighted(0, 100_000))
}

// A call whose cost is not known counts what it could have cost: its
// request at one token a byte, with the most the vendor adds of its own,
// all written to the cache, and its whole output.
func TestALostAnswerCountsItsWorstCase(t *testing.T) {
	assert.Equal(t, cost.Usage{CacheWrite5m: 1001 + 1024, Output: 4096}, cost.Estimate(1001, 4096))
	assert.Equal(t, cost.Usage{CacheWrite5m: cost.MaxCount, Output: cost.MaxCount}, cost.Estimate(1<<62, 1<<62), "never more than a card prices")
}

// Cards that cannot price are refused: every rate is above zero, and none
// is dearer than the dearest a card may hold.
func TestCardsMustPrice(t *testing.T) {
	require.NoError(t, card.Check())
	require.NoError(t, (&cost.Card{Base: card.Base}).Check())
	require.Error(t, (&cost.Card{Threshold: 100_000, Base: card.Base}).Check(), "no long rates")
	require.Error(t, (&cost.Card{Base: cost.Rates{Input: -1}}).Check())
	require.Error(t, (&cost.Card{}).Check(), "no rates at all")
	free := card.Base
	free.CacheRead = 0
	require.Error(t, (&cost.Card{Base: free}).Check(), "a rate of nothing")
	dear := card.Base
	dear.Output = cost.MaxRate + 1
	require.Error(t, (&cost.Card{Base: dear}).Check(), "a rate past the dearest")
}

// The dearest call a card can price fits in its spend: no count and no
// rate can overflow it.
func TestTheDearestCallFits(t *testing.T) {
	top := cost.Rates{Input: cost.MaxRate, Output: cost.MaxRate, CacheWrite5m: cost.MaxRate, CacheWrite1h: cost.MaxRate, CacheRead: cost.MaxRate}
	most := cost.Usage{Input: cost.MaxCount, CacheWrite5m: cost.MaxCount, CacheWrite1h: cost.MaxCount, CacheRead: cost.MaxCount, Output: cost.MaxCount}
	c := &cost.Card{Base: top}

	require.NoError(t, c.Check())
	assert.Equal(t, int64(5*cost.MaxCount*cost.MaxRate), c.Spend(most))
	assert.Positive(t, c.Spend(most))
}

var limits = cost.Limits{DailyCap: 1_000_000, ShadowCut: 80}

// A call starts while its run has budget left and the day's spend is under
// the cap; a comparison or probation run's call only while it is under
// 80% of the cap (spec Section 7).
func TestACallStartsWhileBudgetAndCapAllowIt(t *testing.T) {
	cases := []struct {
		name               string
		run, budget, today int64
		class              cost.Class
		refused            string
	}{
		{name: "fresh", run: 0, budget: 2000, today: 0, class: cost.Primary},
		{name: "budget nearly spent", run: 1999, budget: 2000, today: 0, class: cost.Primary},
		{name: "budget spent", run: 2000, budget: 2000, today: 0, class: cost.Primary, refused: "budget"},
		{name: "budget overspent", run: 2600, budget: 2000, today: 0, class: cost.Primary, refused: "budget"},
		{name: "no budget", run: 0, budget: 0, today: 0, class: cost.Primary, refused: "budget"},
		{name: "primary past the cut", run: 0, budget: 2000, today: 900_000, class: cost.Primary},
		{name: "shadow under the cut", run: 0, budget: 2000, today: 799_999, class: cost.Shadow},
		{name: "shadow at the cut", run: 0, budget: 2000, today: 800_000, class: cost.Shadow, refused: "80%"},
		{name: "primary at the cap", run: 0, budget: 2000, today: 1_000_000, class: cost.Primary, refused: "daily token cap"},
		{name: "unknown class", run: 0, budget: 2000, today: 0, class: "other", refused: "class"},
	}
	for _, c := range cases {
		err := cost.Admit(c.run, c.budget, c.today, c.class, limits)

		if c.refused == "" {
			require.NoError(t, err, c.name)
			continue
		}
		require.ErrorContains(t, err, c.refused, c.name)
	}
}

// The day turns at midnight UTC, whatever the clock's zone.
func TestTheDayTurnsAtMidnightUTC(t *testing.T) {
	sydney := time.FixedZone("AEDT", 11*3600)

	assert.Equal(t, time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), cost.DayStart(time.Date(2026, 10, 17, 9, 30, 0, 0, sydney)))
	assert.Equal(t, time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), cost.DayStart(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)))
}

// Limits a deployment cannot mean are refused.
func TestLimitsMustBeSet(t *testing.T) {
	require.NoError(t, limits.Check())
	require.Error(t, cost.Limits{DailyCap: 0, ShadowCut: 80}.Check())
	require.Error(t, cost.Limits{DailyCap: 1, ShadowCut: 0}.Check())
	require.Error(t, cost.Limits{DailyCap: 1, ShadowCut: 101}.Check())
}
