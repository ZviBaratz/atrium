package agent

// Declared context windows, for turning a session's raw token count into a
// percentage (#596).
//
// The denominator is not in the transcript — every file was probed for
// context_window, max_tokens, exceeds_200k and any compaction threshold, and
// none exists. A survey of 839 transcripts across all five account config roots
// established that the model's *declared* window is the right denominator and
// that sessions genuinely run to it: the peak reading observed was 999,323
// tokens on claude-opus-4-8, or 99.93% of 1M, on a single entry whose
// cache_read_input_tokens alone was 994,891.
//
// The compaction threshold is deliberately NOT used. 79 compaction boundaries
// in that corpus put claude-opus-4-8 anywhere from 81.7k to 735.5k, median
// 323.5k — no consistent fraction of the window, consistent with compaction
// being mostly user-driven (/compact). A "% until auto-compact" figure is not
// derivable locally, and is not what this claims.
//
// max_input_tokens from the Models API (GET /v1/models) is the authoritative
// source and would retire this table entirely, but it needs an API key Atrium
// does not have — Claude Code is OAuth. Worth revisiting if that changes.

// Context-window sizes, named rather than repeated as digit soup.
const (
	contextWindow1M   = 1_000_000
	contextWindow200K = 200_000
)

// claudeContextWindows maps an EXACT model id to its declared context window.
//
// Exact match only. Never prefix-match — a rule worth stating as loudly as this
// because it is the single most likely way this feature ships wrong, and
// because the wrong version looks reasonable:
// strings.HasPrefix(model, "claude-opus") → 1M reads like sensible
// future-proofing.
//
// It is the opposite. The safety property of the whole design is that an
// UNKNOWN model falls back to a raw count, so a table that has gone stale
// degrades VISIBLY — the chip flips from "28%" to "283k" and the user can see
// that Atrium no longer knows the ceiling. A prefix rule destroys that: the next
// model ships silently mis-measured, quietly reporting 50% when the truth is
// 25%, with nothing on screen to say so. A missing entry is a visible gap; a
// guessed entry is an invisible lie.
//
// For the same reason the table is conservative about what it admits. Older
// models (Opus 4.5/4.1, Sonnet 4.5/4.0) are absent because their windows were
// not verified against an authoritative source, and an absent entry degrades
// exactly the way the design intends.
//
// Provenance, since "verified" is the load-bearing word. Every value here comes
// from Anthropic's published model catalog, which states 1M as both the default
// AND the maximum for the Opus 4.6+/Sonnet 4.6+/Fable/Mythos generation — not a
// tier-gated or beta-header variant, which is what it was for the Sonnet 4.x
// generation those rows are deliberately absent for.
//
// A subset is independently confirmed by Atrium's own corpus (857 transcripts,
// ~212k assistant entries), by the only evidence a transcript can give: the peak
// reading observed, which is a floor on the true window.
//
//	claude-opus-4-8    peak   999,323  → 99.93% of 1M; confirms 1M outright
//	claude-opus-5      peak   822,147  → rules out anything at or below 822K
//	claude-fable-5     peak   786,952  → rules out anything at or below 787K
//	claude-sonnet-5    peak   129,097  → catalog only (199 entries, none large)
//	claude-sonnet-4-6      no entries  → catalog only
//	claude-mythos-5        no entries  → catalog only
//	claude-haiku-4-5    peak 37,932    → catalog only
//
// The catalog-only rows are kept rather than dropped: the catalog is the
// authoritative source this table's standard names, it is unambiguous for them,
// and where the corpus can speak it agrees with the catalog every time. Dropping
// a correct entry is not free either — it costs every Sonnet user the percentage
// permanently to hedge against a discrepancy nothing has shown. The failure that
// would matter is a row whose window is too LARGE, which under-reports (a 1M
// entry on a 200K model reads 5× low), so re-check these against the catalog
// before adding a model rather than after.
//
// Model ids appear in both alias and dated forms. Among current models only
// Haiku 4.5 has a dated form in circulation, so it carries both keys; the rest
// are complete as written and must never be given a date suffix.
var claudeContextWindows = map[string]int{
	"claude-fable-5":            contextWindow1M,
	"claude-mythos-5":           contextWindow1M,
	"claude-opus-5":             contextWindow1M,
	"claude-opus-4-8":           contextWindow1M,
	"claude-opus-4-7":           contextWindow1M,
	"claude-opus-4-6":           contextWindow1M,
	"claude-sonnet-5":           contextWindow1M,
	"claude-sonnet-4-6":         contextWindow1M,
	"claude-haiku-4-5":          contextWindow200K,
	"claude-haiku-4-5-20251001": contextWindow200K,
}

// ClaudeContextWindow returns the declared context window for a model id, and
// whether the id is known. Lookup is an exact map hit and nothing else — see
// claudeContextWindows for why that is a design guarantee rather than an
// implementation detail.
//
// ok == false is the useful, safe answer: the caller renders a raw token count
// instead of a percentage, so an unrecognized model reports something true and
// obviously ceiling-less rather than a confident wrong fraction.
func ClaudeContextWindow(model string) (tokens int, ok bool) {
	tokens, ok = claudeContextWindows[model]
	return tokens, ok
}
