package agent

// Published list prices, for turning a session's dedup'd token counts into a
// cost estimate (#392).
//
// What this is and is not. Anthropic meters a Pro/Max/Team subscription in
// Opus-weighted compute-hours against a rolling allowance, and does not expose
// that figure locally — that is why #298 is deferred. Nothing here recovers it.
// What this computes is the same arithmetic Claude Code's own /usage Session
// block does: local token counts priced at standard list rates. Claude Code's
// documentation is explicit that the result "isn't relevant for billing
// purposes" on a subscription and "may differ from your actual bill" on the API.
// Atrium renders it with a "~" for exactly that reason. Do not describe the
// output of this file as spend, as a bill, or as a total.
//
// Provenance. Every rate below is transcribed from Anthropic's published pricing
// page (platform.claude.com/docs/en/about-claude/pricing) as fetched on
// 2026-08-07. Unlike claudeContextWindows in window.go, the local transcript
// corpus can offer NO independent confirmation of a price — a transcript records
// tokens, never money, and costUSD was removed from the JSONL in Claude Code
// 1.0.9 (verified absent across all 4717 transcripts on the development
// machine). The catalog is therefore the sole source, and re-checking it is the
// only maintenance this table has.

import "time"

// Price is a model's list rate in USD per million tokens.
type Price struct {
	InputPerMTok  float64
	OutputPerMTok float64
}

// Multipliers Anthropic applies on top of a model's base input rate. These are
// contract-level and identical for every model, which is why they are constants
// here rather than four more columns per row: the pricing page states them once,
// as multipliers, and every per-model row it publishes is consistent with them.
//
// Deriving the cache rates rather than transcribing them is deliberate. It makes
// a model's row two numbers instead of five, so adding a model cannot get the
// cache columns subtly wrong, and it keeps the ratio that actually drives the
// bill in one place.
const (
	// CacheWrite5mMultiplier prices a 5-minute cache write at 1.25x base input.
	CacheWrite5mMultiplier = 1.25
	// CacheWrite1hMultiplier prices a 1-hour cache write at 2x base input. This is
	// the one that matters: Claude Code uses a 1-hour cache lifetime on a
	// subscription, and 90.2% of cache-write tokens in the development corpus were
	// 1h writes. A reader that prices every write at 1.25x — which is what a
	// single blended cache-write rate amounts to — is ~1.6x low on this term.
	CacheWrite1hMultiplier = 2.00
	// CacheReadMultiplier prices a cache hit at 0.1x base input. This is the rate
	// to get right above all others: in the development corpus cache-read tokens
	// outweigh every other category by roughly two orders of magnitude, so a
	// session's estimate is dominated by this one product.
	CacheReadMultiplier = 0.10
	// InferenceGeoUSMultiplier prices a request pinned to US-only inference at
	// 1.1x across every token category. Claude 4.6 and later only; earlier models
	// reject the parameter. Every entry in the development corpus recorded
	// "not_available", so this path is unexercised there — it is honored because
	// the field exists in the usage object, not because it has been seen.
	InferenceGeoUSMultiplier = 1.10
	// WebSearchUSDPerRequest is the server-side web search charge, $10 per 1,000
	// searches, billed on top of the tokens the results become. Web fetch has no
	// per-request charge.
	WebSearchUSDPerRequest = 0.01
)

// Base rates, named rather than repeated as digit soup. The families collapse
// to four price points across every current model.
var (
	priceFableTier  = Price{InputPerMTok: 10, OutputPerMTok: 50} // Fable 5, Mythos 5
	priceOpusTier   = Price{InputPerMTok: 5, OutputPerMTok: 25}  // Opus 4.6 through 5
	priceSonnetTier = Price{InputPerMTok: 3, OutputPerMTok: 15}  // Sonnet 4.6, Sonnet 5 from Sep 2026
	priceHaikuTier  = Price{InputPerMTok: 1, OutputPerMTok: 5}   // Haiku 4.5
	// priceSonnet5Intro is Sonnet 5's introductory rate, in effect through
	// 2026-08-31 and superseded by priceSonnetTier on 2026-09-01.
	priceSonnet5Intro = Price{InputPerMTok: 2, OutputPerMTok: 10}
	// priceOpusFast is the fast-mode rate for Opus 5 and Opus 4.8 — double the
	// standard rate, applied across the full context window. It REPLACES the base
	// rate rather than multiplying it, and the cache multipliers then apply on top
	// of the replacement.
	priceOpusFast = Price{InputPerMTok: 10, OutputPerMTok: 50}
)

// sonnet5Repricing is when Sonnet 5's introductory pricing lapses. Anthropic
// states the boundary as a calendar date ("through August 31, 2026") without a
// timezone; UTC is the assumption, and the exposure of getting it wrong is one
// day of one model's requests priced 33% low.
var sonnet5Repricing = time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)

// pricePeriod is one rate and the instant it takes effect. A model's periods are
// ordered oldest-first and the last one whose From has passed wins.
type pricePeriod struct {
	From  time.Time
	Price Price
}

// claudePrices maps an EXACT model id to its list-rate schedule.
//
// Exact match only. Never prefix-match — the same rule window.go states at
// length for context windows, and for the same reason: the safety property of
// the whole feature is that an unpriceable entry is VISIBLE. A session whose
// transcript contains a model this table does not know renders "≥"-style partial
// notation instead of "~", so a stale table under-reports in a way the user can
// see. A strings.HasPrefix(model, "claude-opus") rule would replace that with a
// confident wrong number the day a model reprices, which is precisely what
// happened to Opus between 4.1 ($15/$75) and 4.5 ($5/$25) — a 3x error that
// shares a prefix.
//
// The table is conservative about what it admits, again mirroring window.go.
// Retired models (Opus 4.1/4, Sonnet 4/4.5, Haiku 3.5) are absent: they are
// reachable only on Bedrock and Vertex, which Claude Code does not report
// through message.model in any transcript on the development machine, and an
// absent entry degrades the way the design intends.
//
// Coverage, measured rather than assumed: summing every Atrium session in the
// development corpus — 761 project directories, 157,359 deduplicated requests
// across main transcripts and their sub-agent trees — produced ZERO unpriced
// requests. The bare aliases that do appear elsewhere in the JSONL ("sonnet",
// "haiku", "opus", "fable") are configuration echoes, not message.model values,
// and are deliberately not given rows — "sonnet" does not name a price, it names
// a family whose price has changed twice.
//
// That is a measurement of one machine's history, not a guarantee about the
// future, which is the point of the unpriced path rather than an admission
// against it: the table is complete today and will stop being complete without
// warning, and the ">" the chip switches to is how that becomes visible.
//
// Model ids appear in both alias and dated forms. Among current models only
// Haiku 4.5 has a dated form in circulation, so it carries both keys; the rest
// are complete as written and must never be given a date suffix.
var claudePrices = map[string][]pricePeriod{
	"claude-fable-5":  {{Price: priceFableTier}},
	"claude-mythos-5": {{Price: priceFableTier}},
	"claude-opus-5":   {{Price: priceOpusTier}},
	"claude-opus-4-8": {{Price: priceOpusTier}},
	"claude-opus-4-7": {{Price: priceOpusTier}},
	"claude-opus-4-6": {{Price: priceOpusTier}},
	"claude-sonnet-5": {
		{Price: priceSonnet5Intro},
		{From: sonnet5Repricing, Price: priceSonnetTier},
	},
	"claude-sonnet-4-6":         {{Price: priceSonnetTier}},
	"claude-haiku-4-5":          {{Price: priceHaikuTier}},
	"claude-haiku-4-5-20251001": {{Price: priceHaikuTier}},
}

// claudeFastPrices maps an EXACT model id to its fast-mode rate. Fast mode is a
// research preview on the first-party API only, and only Opus 5 and Opus 4.8
// support it — Opus 4.7 errors on the request and Opus 4.6 silently runs at
// standard speed. A model absent from here that nonetheless reports speed:"fast"
// is UNPRICEABLE rather than priced at its standard rate: guessing would
// under-report by 2x, and this table going stale is exactly how that guess would
// start being wrong.
var claudeFastPrices = map[string]Price{
	"claude-opus-5":   priceOpusFast,
	"claude-opus-4-8": priceOpusFast,
}

// Tokens is one request's billable token counts, as recorded in a transcript
// entry's message.usage object, after deduplication.
//
// The cache-write split is two fields rather than one because the two tiers
// price differently (1.25x vs 2x base input) and Claude Code's subscription
// default is the expensive one. Collapsing them loses the larger term.
type Tokens struct {
	Input        int64
	Output       int64
	CacheRead    int64
	CacheWrite5m int64
	CacheWrite1h int64
	WebSearches  int64
}

// Request is everything about one API call that bears on its price: which model
// answered, when (so a repricing boundary lands on the right side), and the two
// usage-object modifiers that scale the result.
//
// Speed and InferenceGeo are the raw strings from message.usage. They are
// compared, never parsed: an unrecognized Speed is not fast mode, and an
// unrecognized InferenceGeo is not the US premium. Older Claude Code versions
// omit both fields entirely, which decodes to "" and takes the same path.
type Request struct {
	Model        string
	At           time.Time
	Speed        string
	InferenceGeo string
	Tokens       Tokens
}

// speedFast and geoUS are the only two values of their fields that change a
// price. Everything else — "standard", "not_available", "", an unknown future
// value — leaves the base rate alone.
const (
	speedFast = "fast"
	geoUS     = "us"
)

// ClaudePrice returns the list rate in effect for a model at instant at, and
// whether the model is known. Lookup is an exact map hit; see claudePrices for
// why that is a design guarantee and not an implementation detail.
//
// A zero at selects the earliest period, which is the right answer for a
// transcript entry whose timestamp failed to parse: every schedule here starts
// at the zero time, so an unstamped entry is priced at the oldest published rate
// rather than dropped.
func ClaudePrice(model string, at time.Time) (Price, bool) {
	periods, ok := claudePrices[model]
	if !ok || len(periods) == 0 {
		return Price{}, false
	}
	// Periods are ordered oldest-first and are never more than a handful, so a
	// linear scan for the last one that has taken effect is both clearest and
	// fastest. The first period always has a zero From, so this cannot miss.
	price := periods[0].Price
	for _, p := range periods[1:] {
		if at.Before(p.From) {
			break
		}
		price = p.Price
	}
	return price, true
}

// ClaudeFastPrice returns the fast-mode rate for a model and whether the model
// has one. See claudeFastPrices for why a miss must not fall back to the
// standard rate.
func ClaudeFastPrice(model string) (Price, bool) {
	price, ok := claudeFastPrices[model]
	return price, ok
}

// ClaudeCost estimates one request's cost in USD, and reports ok=false when the
// request cannot be priced at all — an unknown model id, or fast mode on a model
// with no published fast rate.
//
// ok=false is the useful, safe answer: the caller counts the request as unpriced
// and the rendered figure becomes a lower bound rather than an estimate, so the
// user can see that Atrium no longer knows every rate involved. Returning a
// best-guess number instead would make a stale table invisible.
func ClaudeCost(r Request) (usd float64, ok bool) {
	base, ok := ClaudePrice(r.Model, r.At)
	if !ok {
		return 0, false
	}
	if r.Speed == speedFast {
		if base, ok = ClaudeFastPrice(r.Model); !ok {
			return 0, false
		}
	}

	in, out := base.InputPerMTok, base.OutputPerMTok
	t := r.Tokens
	usd = float64(t.Input)*in +
		float64(t.Output)*out +
		float64(t.CacheWrite5m)*in*CacheWrite5mMultiplier +
		float64(t.CacheWrite1h)*in*CacheWrite1hMultiplier +
		float64(t.CacheRead)*in*CacheReadMultiplier
	usd /= 1_000_000

	if r.InferenceGeo == geoUS {
		// The premium applies to every token category, per the data-residency
		// section — but not to the per-search web charge, which is not a token
		// price. Hence the multiply here, before searches are added.
		usd *= InferenceGeoUSMultiplier
	}

	return usd + float64(t.WebSearches)*WebSearchUSDPerRequest, true
}
