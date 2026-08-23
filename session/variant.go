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
	// Both creation surfaces enforce it before anything is created: the create form's
	// submit gate and `atrium new`'s spec parser. The drain does not enforce it as a
	// ceiling on a batch — a hand-written batch wider than this is charged and refused
	// like any other, and the session cap is its real bound — but it is not blind to the
	// size either: outbox.Request.BatchSize records what the invocation committed to the
	// spool, and app.createBatchRefusalBudget is this constant read as a per-tick bound
	// on how many members one refusal may answer.
	MaxVariantBatch = 20
	// VariantTitleScan bounds how far past the requested count a suffix search probes
	// for free <stem>-N names, so a repo dense with orphan <stem>-N branches cannot
	// loop unboundedly. Generous — the common case finds names immediately.
	//
	// One number for every search rather than one each: the create form scans the live
	// instance list, `atrium new` scans the stored one plus the repo's local branches,
	// and the fork path scans for a free `<title>-fork` — and a repo one of them calls
	// too dense should not be one another keeps digging through. How far past the count
	// each goes is the caller's own bound, not this one: two of them scan total+this and
	// the fork scans exactly this.
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
// One of the three callers named above checks. main.planVariantTitles terminates its scan
// on the first over-length candidate and refuses the fan-out. app.planVariantTitles does
// not check at all, so the create form makes sessions whose titles its own rename field
// cannot re-enter (atrium#784); app.firstFreeTitle does not either, and reaches the same
// place by a different route, since forkTitleSuffix is itself five runes onto a stem that
// may already be at the cap (atrium#785). Both are gaps rather than a division of labour,
// and together they are why this function must not read as though the rule were enforced
// somewhere on its behalf.
func VariantTitle(stem string, n int) string {
	return fmt.Sprintf("%s-%d", stem, n)
}
