// Package cost prices model calls as the vendor charges for them, and holds
// the rules that let a call start: the run's token budget and the
// deployment's daily cap (spec Sections 5.6, 7 and 13.6; ADR-007). Budgets
// and the cap count cost-weighted tokens: spend at the price of one
// reference token, so that they bound money whichever model an agent uses.
package cost

import (
	"errors"
	"fmt"
	"time"
)

// MaxCount is the most tokens of one kind a call can count, a hundred times
// the longest context: an answer that reports more is not read, and no
// estimate counts more.
const MaxCount = 100_000_000

// MaxRate is the dearest rate a card may hold, in pico-USD a token: $1,000
// per million tokens. With MaxCount, no call's spend can overflow.
const MaxRate = 1_000_000_000

// framing is the most tokens the vendor adds to a request of its own, such
// as the system prompt that introduces tools, which is a few hundred.
const framing = 1024

// Usage counts one call's tokens as the vendor reports them.
type Usage struct {
	// Input is the input read neither from nor into the cache.
	Input int64
	// CacheWrite5m is input written to the five-minute cache, and
	// CacheWrite1h input written to the one-hour cache.
	CacheWrite5m, CacheWrite1h int64
	// CacheRead is input read from the cache.
	CacheRead int64
	Output    int64
}

// Prompt is the call's prompt in tokens: its input, cached or not.
func (u Usage) Prompt() int64 { return u.Input + u.CacheWrite5m + u.CacheWrite1h + u.CacheRead }

// Rates are what one token costs, in pico-USD (10^-12 USD): $3 per million
// tokens is 3,000,000.
type Rates struct {
	Input, Output, CacheWrite5m, CacheWrite1h, CacheRead int64
}

func (r Rates) valid() bool {
	for _, rate := range []int64{r.Input, r.Output, r.CacheWrite5m, r.CacheWrite1h, r.CacheRead} {
		if rate <= 0 || rate > MaxRate {
			return false
		}
	}
	return true
}

// writeCovers reports whether a five-minute cache write costs at least the
// input and the cache read it stands for, as Estimate's worst case needs.
func (r Rates) writeCovers() bool {
	return r.CacheWrite5m >= r.Input && r.CacheWrite5m >= r.CacheRead
}

// Card is a model's prices, as the deployment copies them from the
// vendor's list: Base, and, when Threshold is set, Long for a call whose
// prompt is longer than Threshold tokens.
type Card struct {
	Threshold  int64
	Base, Long Rates
}

// Check reports a card that cannot price a call as the vendor charges it: a
// rate missing or past the bounds; long rates, which a card with a threshold
// needs and one without may not have; and a five-minute cache write that
// costs less than the input or the cache read it stands for. Estimate counts
// a lost call's request as five-minute cache writes, its worst case, so a
// write cheaper than either would price that worst case below what the same
// tokens cost.
func (c *Card) Check() error {
	switch {
	case !c.Base.valid():
		return fmt.Errorf("cost: a card needs every rate above zero and at most %d pico-USD a token", MaxRate)
	case !c.Base.writeCovers():
		return errors.New("cost: a card's five-minute cache write costs less than its input or its cache read")
	case c.Threshold < 0:
		return errors.New("cost: a card's threshold cannot be below zero")
	case c.Threshold > 0 && !c.Long.valid():
		return errors.New("cost: a card with a threshold needs long rates")
	case c.Threshold > 0 && !c.Long.writeCovers():
		return errors.New("cost: a card's long five-minute cache write costs less than its long input or its long cache read")
	case c.Threshold == 0 && c.Long != (Rates{}):
		return errors.New("cost: a card without a threshold has no long rates")
	}
	return nil
}

// Spend returns what usage costs, in pico-USD.
func (c *Card) Spend(u Usage) int64 {
	r := c.Base
	if c.Threshold > 0 && u.Prompt() > c.Threshold {
		r = c.Long
	}
	return u.Input*r.Input + u.CacheWrite5m*r.CacheWrite5m + u.CacheWrite1h*r.CacheWrite1h + u.CacheRead*r.CacheRead + u.Output*r.Output
}

// Weighted returns spend in cost-weighted tokens: spend at the reference
// price of one token, in pico-USD, rounded up.
func Weighted(spend, reference int64) int64 { return (spend + reference - 1) / reference }

// Estimate returns the usage counted for a call whose cost is not known:
// one in flight, or one whose answer was lost or could not be read. It is
// the call's worst case, so that what the vendor may have charged is never
// counted as less: its request at one token a byte, with the most the
// vendor adds of its own, all written to the cache, and its whole output.
func Estimate(requestBytes, maxTokens int64) Usage {
	return Usage{CacheWrite5m: min(requestBytes+framing, MaxCount), Output: min(maxTokens, MaxCount)}
}

// Class says whether a run's calls are cut first as spend nears the daily
// cap.
type Class string

// Classes of runs (spec Section 7).
const (
	// Primary is a primary run of either arm: it is never cut before the
	// daily cap.
	Primary Class = "primary"
	// Shadow is a comparison or probation run: it is cut first.
	Shadow Class = "shadow"
)

// Limits are the deployment's limits on its daily spend.
type Limits struct {
	// DailyCap is the cost-weighted tokens the tenant's calls may spend in
	// one UTC day.
	DailyCap int64
	// ShadowCut is the share of the cap, in percent, from which shadow runs'
	// calls are refused [80].
	ShadowCut int
}

// Check reports limits that cannot hold: no cap, or a cut outside 1–100%.
func (l Limits) Check() error {
	if l.DailyCap <= 0 {
		return errors.New("cost: the daily token cap must be set")
	}
	if l.ShadowCut < 1 || l.ShadowCut > 100 {
		return fmt.Errorf("cost: the shadow cut is a share of the cap from 1 to 100%%, not %d%%", l.ShadowCut)
	}
	return nil
}

// Admit reports whether a call may start: its run has spent less than its
// budget, and the tenant has spent less today than the daily cap, or, for
// a shadow run, than the shadow cut. A call that starts below the budget
// may end above it.
func Admit(runSpent, budget, today int64, class Class, l Limits) error {
	switch {
	case class != Primary && class != Shadow:
		return fmt.Errorf("the session's class %q is neither primary nor shadow", class)
	case runSpent >= budget:
		return fmt.Errorf("the run's token budget of %d is spent", budget)
	case today >= l.DailyCap:
		return errors.New("the daily token cap is reached")
	case class == Shadow && today*100 >= l.DailyCap*int64(l.ShadowCut):
		return fmt.Errorf("comparison and probation runs are cut at %d%% of the daily token cap", l.ShadowCut)
	}
	return nil
}

// DayStart returns the start of t's day, UTC, from which the daily cap
// counts.
func DayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
