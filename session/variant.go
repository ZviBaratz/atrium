package session

// variant.go — the naming rule a fan-out batch is derived by (#387, #761).
//
// It lives here, beside MaxTitleLen and for that constant's stated reason: this is a
// rule about titles rather than about any one surface, and both creation surfaces
// enforce it now. The create form derives a batch through app.planVariantTitles; the
// headless `atrium new --variants` derives one before it spools, because the record it
// writes carries a final title and an atrium that never heard of a batch has to be able
// to create it. Two derivations, one scheme — which is only true while both call this.

import "fmt"

const (
	// MaxVariantBatch caps how many sessions one fan-out may request — a fixed sanity
	// bound on a single batch, independent of max_sessions. The effective session cap
	// (config.SessionCap: the host-derived soft default or an explicit value) is
	// enforced separately and is usually the tighter limit.
	//
	// Both surfaces enforce it before anything is created: the create form's submit
	// gate and `atrium new`'s spec parser. The create drain deliberately does not — a
	// spool record carries no original batch size, only the id its members share, so
	// the drain's real bound on a batch is the session cap it charges the batch for.
	MaxVariantBatch = 20
	// VariantTitleScan bounds how far past the requested count a suffix search probes
	// for free <stem>-N names, so a repo dense with orphan <stem>-N branches cannot
	// loop unboundedly. Generous — the common case finds names immediately.
	//
	// One number for both searches rather than two: the create form scans the live
	// instance list and `atrium new` scans the stored one plus the repo's local
	// branches, and a repo the form calls too dense should not be one the CLI keeps
	// digging through.
	VariantTitleScan = 100
)

// VariantTitle is the name of the n-th variant derived from stem, and the one spelling
// of the <stem>-N scheme. n is 1-based.
//
// Whether a batch of one keeps the bare stem is the caller's decision and not this
// function's: app.planVariantTitles keeps it (the pre-#387 contract, so an ordinary
// create is unchanged), app.firstFreeTitle treats the bare stem as the 1 and starts
// suffixing at 2, and main's fan-out never reaches here for a total of one.
//
// It deliberately does not bound the result against MaxTitleLen: a numbered suffix can
// push a derived title over the cap, and how soon depends on the stem.
// TestVariantTitleCanOutgrowMaxTitleLen owns that fact, and owning it there rather than
// here is the point — a stem at the cap overflows at the very FIRST variant, which is not
// what an eye reading the scheme expects and not something a sentence should be trusted
// to keep saying correctly.
//
// One caller checks. main.planVariantTitles terminates its scan on the first over-length
// candidate and refuses the fan-out; app.planVariantTitles does not check at all, so the
// create form makes sessions whose titles its own rename field cannot re-enter. That is
// atrium#784 — a gap, not a division of labour, and the reason this function must not
// read as though the rule were enforced somewhere on its behalf.
func VariantTitle(stem string, n int) string {
	return fmt.Sprintf("%s-%d", stem, n)
}
