package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// anchor is an instant comfortably inside Sonnet 5's introductory window, used
// wherever a test needs a timestamp but is not about the repricing boundary.
var anchor = time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)

// TestClaudePrice_KnownModels pins every row against Anthropic's published
// catalog. A wrong rate here is not a cosmetic defect: it is the multiplier on
// every token the session ever spent, and nothing downstream can detect it.
func TestClaudePrice_KnownModels(t *testing.T) {
	for model, want := range map[string]Price{
		"claude-fable-5":            {InputPerMTok: 10, OutputPerMTok: 50},
		"claude-mythos-5":           {InputPerMTok: 10, OutputPerMTok: 50},
		"claude-opus-5":             {InputPerMTok: 5, OutputPerMTok: 25},
		"claude-opus-4-8":           {InputPerMTok: 5, OutputPerMTok: 25},
		"claude-opus-4-7":           {InputPerMTok: 5, OutputPerMTok: 25},
		"claude-opus-4-6":           {InputPerMTok: 5, OutputPerMTok: 25},
		"claude-sonnet-5":           {InputPerMTok: 2, OutputPerMTok: 10}, // introductory; see the boundary test
		"claude-sonnet-4-6":         {InputPerMTok: 3, OutputPerMTok: 15},
		"claude-haiku-4-5":          {InputPerMTok: 1, OutputPerMTok: 5},
		"claude-haiku-4-5-20251001": {InputPerMTok: 1, OutputPerMTok: 5},
	} {
		got, ok := ClaudePrice(model, anchor)
		if !assert.Truef(t, ok, "ClaudePrice(%q): not found", model) {
			continue
		}
		assert.Equalf(t, want, got, "ClaudePrice(%q)", model)
	}
}

// TestClaudePrice_IsExactMatchNotPrefixMatch is the guard the whole design
// rests on, and it is the sibling of window_test.go's identical assertion.
//
// A prefix rule reads like sensible future-proofing and is the opposite. The
// Opus family alone has repriced 3x within one prefix — claude-opus-4-1 was
// $15/$75 and claude-opus-4-5 is $5/$25 — so "claude-opus-*" → $5/$25 is not a
// safe default, it is a 3x error waiting for the next release. Exact matching
// means an unknown model is reported as unpriced, and the rendered figure
// becomes a visible lower bound instead of a confident wrong number.
func TestClaudePrice_IsExactMatchNotPrefixMatch(t *testing.T) {
	for _, model := range []string{
		"claude-opus-99",            // a future model
		"claude-opus-4-9",           // a future point release
		"claude-opus",               // the bare family prefix
		"claude-opus-5-20260401",    // a dated form the table does not carry
		"claude-sonnet-6",           // a future Sonnet
		"claude-haiku-5",            // a future Haiku
		"claude-haiku-4-5-20991231", // a different dated Haiku build
		"claude-opus-4-1",           // a RETIRED model that really did cost 3x
		"claude-sonnet-4-5",         // a retired model, deliberately absent
		"sonnet",                    // a config alias, not a transcript id
		"haiku",                     // ditto
		"opus",                      // ditto
		"<synthetic>",               // Claude Code's API-error placeholder
		"",                          // no model at all
	} {
		if price, ok := ClaudePrice(model, anchor); ok {
			t.Errorf("ClaudePrice(%q) matched (%v) — the lookup must be exact, so an "+
				"unknown model is reported unpriced rather than given a wrong rate", model, price)
		}
	}
}

// TestClaudePrice_Sonnet5RepricesOnSeptember1 drives the only reason the table
// is a schedule rather than a flat map.
//
// Sonnet 5 ran at an introductory $2/$10 through 2026-08-31 and reverts to
// $3/$15 on 2026-09-01. A flat table is 33% wrong on one side of that boundary
// whichever rate it carries, and Sonnet 5 is the third most-used model in the
// development corpus. The boundary is asserted at the last instant before and
// the first instant at, not merely "some day in August" and "some day in
// September", so an off-by-one in the comparison fails here.
func TestClaudePrice_Sonnet5RepricesOnSeptember1(t *testing.T) {
	intro := Price{InputPerMTok: 2, OutputPerMTok: 10}
	standard := Price{InputPerMTok: 3, OutputPerMTok: 15}

	for _, tc := range []struct {
		name string
		at   time.Time
		want Price
	}{
		{"the zero time", time.Time{}, intro},
		{"long before", time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC), intro},
		{"the last nanosecond of August 31", sonnet5Repricing.Add(-time.Nanosecond), intro},
		{"the instant of the change", sonnet5Repricing, standard},
		{"the first nanosecond after", sonnet5Repricing.Add(time.Nanosecond), standard},
		{"long after", time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC), standard},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ClaudePrice("claude-sonnet-5", tc.at)
			require.True(t, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestClaudePrice_FlatModelsIgnoreTheClock guards the other half of the
// schedule: a model with one period must return that period at every instant,
// including the zero time a failed timestamp parse produces.
func TestClaudePrice_FlatModelsIgnoreTheClock(t *testing.T) {
	want := Price{InputPerMTok: 5, OutputPerMTok: 25}
	for _, at := range []time.Time{
		{},
		time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC),
		anchor,
		time.Date(2099, time.December, 31, 0, 0, 0, 0, time.UTC),
	} {
		got, ok := ClaudePrice("claude-opus-5", at)
		require.True(t, ok)
		assert.Equalf(t, want, got, "ClaudePrice(claude-opus-5, %v)", at)
	}
}

// TestCacheMultipliersDeriveThePublishedPerModelRates is the test that earns the
// two-numbers-per-row table shape.
//
// The pricing page publishes five columns per model (base input, 5m write, 1h
// write, cache hit, output); this file stores two and derives the middle three
// from constants. That is only sound if the derivation reproduces the published
// column exactly for every tier — so the published numbers are transcribed HERE,
// independently, and checked against the arithmetic. If Anthropic ever prices a
// cache tier off-ratio for one model, this fails rather than the estimate
// quietly drifting.
func TestCacheMultipliersDeriveThePublishedPerModelRates(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		base                   float64
		write5m, write1h, read float64
	}{
		{"Fable 5 / Mythos 5", 10, 12.50, 20, 1.00},
		{"Opus 4.6 through 5", 5, 6.25, 10, 0.50},
		{"Sonnet 4.6 / Sonnet 5 from Sep", 3, 3.75, 6, 0.30},
		{"Sonnet 5 introductory", 2, 2.50, 4, 0.20},
		{"Haiku 4.5", 1, 1.25, 2, 0.10},
		{"Opus fast mode", 10, 12.50, 20, 1.00},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.write5m, tc.base*CacheWrite5mMultiplier, 1e-9, "5m cache write")
			assert.InDelta(t, tc.write1h, tc.base*CacheWrite1hMultiplier, 1e-9, "1h cache write")
			assert.InDelta(t, tc.read, tc.base*CacheReadMultiplier, 1e-9, "cache hit")
		})
	}
}

// TestClaudeCost_PricesEachCategoryIndependently checks the arithmetic one term
// at a time, so a transposed multiplier fails in the row that names it rather
// than being absorbed by a total.
//
// Every expectation is hand-computed from Opus 5's published $5/$25: a round
// million tokens in one category at a time makes the published per-MTok rate the
// answer, which is why the fixture uses 1,000,000 rather than a realistic count.
func TestClaudeCost_PricesEachCategoryIndependently(t *testing.T) {
	const million = 1_000_000

	for _, tc := range []struct {
		name   string
		tokens Tokens
		want   float64
	}{
		{"input", Tokens{Input: million}, 5.00},
		{"output", Tokens{Output: million}, 25.00},
		{"5m cache write", Tokens{CacheWrite5m: million}, 6.25},
		{"1h cache write", Tokens{CacheWrite1h: million}, 10.00},
		{"cache read", Tokens{CacheRead: million}, 0.50},
		{"nothing at all", Tokens{}, 0},
		{
			"every category at once",
			Tokens{Input: million, Output: million, CacheWrite5m: million, CacheWrite1h: million, CacheRead: million},
			5.00 + 25.00 + 6.25 + 10.00 + 0.50,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usd, ok := ClaudeCost(Request{Model: "claude-opus-5", At: anchor, Tokens: tc.tokens})
			require.True(t, ok)
			assert.InDelta(t, tc.want, usd, 1e-9)
		})
	}
}

// TestClaudeCost_WebSearchIsAFlatPerRequestCharge pins the one term that is not
// a token price: $10 per 1,000 searches, added after the token total and
// deliberately outside the data-residency multiplier.
func TestClaudeCost_WebSearchIsAFlatPerRequestCharge(t *testing.T) {
	usd, ok := ClaudeCost(Request{
		Model:  "claude-opus-5",
		At:     anchor,
		Tokens: Tokens{WebSearches: 1000},
	})
	require.True(t, ok)
	assert.InDelta(t, 10.00, usd, 1e-9)

	// A search charge rides alongside the tokens the results become, not instead
	// of them.
	usd, ok = ClaudeCost(Request{
		Model:  "claude-opus-5",
		At:     anchor,
		Tokens: Tokens{Output: 1_000_000, WebSearches: 1000},
	})
	require.True(t, ok)
	assert.InDelta(t, 35.00, usd, 1e-9)
}

// TestClaudeCost_InferenceGeoUSScalesTokensButNotSearches drives the 1.1x
// data-residency premium, and pins the boundary the code draws around it: the
// pricing page applies the multiplier to "all token pricing categories", which
// a per-search dollar charge is not.
func TestClaudeCost_InferenceGeoUSScalesTokensButNotSearches(t *testing.T) {
	base := Request{Model: "claude-opus-5", At: anchor, Tokens: Tokens{Output: 1_000_000, WebSearches: 1000}}

	standard, ok := ClaudeCost(base)
	require.True(t, ok)
	assert.InDelta(t, 25.00+10.00, standard, 1e-9)

	base.InferenceGeo = "us"
	us, ok := ClaudeCost(base)
	require.True(t, ok)
	assert.InDelta(t, 25.00*1.1+10.00, us, 1e-9,
		"the premium must scale the tokens and leave the per-search charge alone")

	// Everything that is not exactly "us" is standard pricing, including the
	// value every entry in the development corpus actually carries.
	for _, geo := range []string{"", "not_available", "global", "US", "eu"} {
		base.InferenceGeo = geo
		got, ok := ClaudeCost(base)
		require.True(t, ok)
		assert.InDeltaf(t, standard, got, 1e-9, "inference_geo=%q must not be the US premium", geo)
	}
}

// TestClaudeCost_FastModeReplacesTheBaseRate proves fast mode is a substitution
// rather than another multiplier — the cache tiers then derive from the REPLACED
// rate, which is what "prompt caching multipliers apply on top of fast mode
// pricing" means.
func TestClaudeCost_FastModeReplacesTheBaseRate(t *testing.T) {
	tokens := Tokens{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000}

	standard, ok := ClaudeCost(Request{Model: "claude-opus-5", At: anchor, Tokens: tokens})
	require.True(t, ok)
	assert.InDelta(t, 5.00+25.00+0.50, standard, 1e-9)

	fast, ok := ClaudeCost(Request{Model: "claude-opus-5", At: anchor, Speed: "fast", Tokens: tokens})
	require.True(t, ok)
	assert.InDelta(t, 10.00+50.00+1.00, fast, 1e-9,
		"the cache read must be 0.1x of the FAST input rate, not of the standard one")

	// Only "fast" switches rates. "standard" is what every entry in the corpus
	// carries, and an absent field decodes to "".
	for _, speed := range []string{"", "standard", "Fast", "fastest"} {
		got, ok := ClaudeCost(Request{Model: "claude-opus-5", At: anchor, Speed: speed, Tokens: tokens})
		require.True(t, ok)
		assert.InDeltaf(t, standard, got, 1e-9, "speed=%q must not select fast-mode rates", speed)
	}
}

// TestClaudeCost_UnpriceableRequests enumerates every way ClaudeCost must
// decline, because declining is what makes a stale table visible. A request it
// priced by guessing would be indistinguishable from one it priced correctly.
func TestClaudeCost_UnpriceableRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  Request
	}{
		{
			"a model the table does not carry",
			Request{Model: "claude-opus-99", At: anchor, Tokens: Tokens{Output: 1000}},
		},
		{
			"no model at all",
			Request{At: anchor, Tokens: Tokens{Output: 1000}},
		},
		{
			// The important one. Opus 4.7 rejects speed:"fast" outright and Opus 4.6
			// silently runs at standard speed, so neither has a fast rate — but both
			// have a standard one, and falling back to it would under-report by 2x.
			"fast mode on a model with no published fast rate",
			Request{Model: "claude-opus-4-7", At: anchor, Speed: "fast", Tokens: Tokens{Output: 1_000_000}},
		},
		{
			"fast mode on a Haiku",
			Request{Model: "claude-haiku-4-5", At: anchor, Speed: "fast", Tokens: Tokens{Output: 1_000_000}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usd, ok := ClaudeCost(tc.req)
			assert.False(t, ok, "must report unpriced")
			assert.Zero(t, usd, "an unpriced request must contribute nothing")
		})
	}
}

// TestClaudeCost_FastModeIsAvailableOnlyWhereAnthropicSaysItIs pins the fast
// table's membership in both directions, so adding a model to claudePrices does
// not silently imply it has a fast rate and removing one does not silently
// withdraw it.
func TestClaudeCost_FastModeIsAvailableOnlyWhereAnthropicSaysItIs(t *testing.T) {
	fast := map[string]bool{"claude-opus-5": true, "claude-opus-4-8": true}
	for model := range claudePrices {
		_, ok := ClaudeFastPrice(model)
		assert.Equalf(t, fast[model], ok, "ClaudeFastPrice(%q) availability", model)
	}
	assert.Len(t, claudeFastPrices, len(fast), "no fast entry may exist for a model without a standard rate")
}

// TestPriceAndWindowTablesCoverTheSameModels is a drift guard between the two
// exact-match tables in this package.
//
// Both are transcribed from the same published catalog page, and a model that
// has one and not the other is almost always an omission rather than a decision:
// a session would show a context percentage it cannot cost, or a cost it cannot
// place in a window. Neither failure is visible at a glance, which is why it is
// asserted here rather than left to review.
//
// If a genuine asymmetry ever arises, this test is the right place to record it
// — with the reason — rather than the place to delete.
func TestPriceAndWindowTablesCoverTheSameModels(t *testing.T) {
	for model := range claudePrices {
		_, ok := ClaudeContextWindow(model)
		assert.Truef(t, ok, "%q has a price but no context window (window.go)", model)
	}
	for model := range claudeContextWindows {
		_, ok := ClaudePrice(model, anchor)
		assert.Truef(t, ok, "%q has a context window but no price (pricing.go)", model)
	}
}
