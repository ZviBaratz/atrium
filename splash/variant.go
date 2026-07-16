package splash

// The splash variant vocabulary and per-variant Pass-2 policy: which field
// generators exist, what users call them, and how each shades. Selection (which
// variant is active) and the dev-only ATRIUM_SPLASH_* env overrides live in
// package ui, which drives this package through Render. Kept apart from field.go
// so that file is only the noise core and the renderer — this is the part that
// changes whenever a variant is added, retired, or renamed.

// Variant selects the field generator + glyph technique. Package ui pins one
// (or keeps a random per-launch rotation) and passes it in through Options; the
// dev-only ATRIUM_SPLASH_VARIANT override, also resolved in ui, can trump that.
type Variant int

const (
	// Rain ("f") is Matrix-style digital rain: per-column streams with bright
	// heads and fading tails, layered at three depths. The roster's motion entry
	// — and the only variant that shades by luminance alone, with no hue of its
	// own to spend (see buildRainRamp).
	//
	// It is also the fallback: a variant with no case in splashFieldAt or ops
	// renders as rain, and package ui resolves an unrecognized override name to
	// it too.
	Rain Variant = iota
	// Tunnel ("g") is a textured wall flying past a vanishing point that sits on
	// the wordmark: screen position maps to (depth, angle), so a plain noise
	// lookup becomes an infinite corridor. The roster's depth entry — z-fog
	// carries distance in luminance, and hue bands by depth into coloured rings
	// receding down the wall.
	Tunnel
	// Ripple ("h") is drops falling on a dark pool: each one flashes where it
	// lands and expands into a ring that shifts hue as it ages, and the rings
	// interfere where they cross. The roster's event entry — the only field with
	// a birth and a death in it rather than a steady state.
	Ripple

	// variantCount is the enum's cardinality, not a variant — it must stay last.
	// It exists so the tests can prove they cover every variant: the contract
	// loop and the benchmark both walk a hand-maintained map, and a variant
	// missing from it escapes both without failing anything. Safe to key off the
	// iota here because nothing persists these ordinals — config stores the
	// pattern's name, not its number.
	variantCount
)

// variantNames maps each pinnable pattern name onto its variant. It is the
// vocabulary shared with config (config.SplashVariants, cycled in the settings
// panel): the two lists are hand-maintained in packages that cannot import each
// other, and app asserts they agree. It backs both ParseVariant and String.
var variantNames = map[string]Variant{
	"rain":   Rain,
	"ripple": Ripple,
	"tunnel": Tunnel,
}

// Variants lists the shipped variants — the pool package ui's random mode draws
// from. The order is the rotation order; the returned slice is a fresh copy, so
// callers cannot mutate the pool.
func Variants() []Variant {
	return []Variant{Rain, Tunnel, Ripple}
}

// String returns the variant's pinnable pattern name, or "unknown" for a value
// outside the shipped set.
func (v Variant) String() string {
	for name, vv := range variantNames {
		if vv == v {
			return name
		}
	}
	return "unknown"
}

// ParseVariant resolves a pinnable pattern name to its variant. It knows only
// the user-facing names, not the dev letters (f/g/h) — those are an Atrium
// affordance package ui layers on top. The bool is false for an unknown name.
func ParseVariant(s string) (Variant, bool) {
	v, ok := variantNames[s]
	return v, ok
}

// splashOps is a variant's Pass-2 policy: how its raw field is turned into
// glyphs and colour.
//
// It is deliberately small. It carried five more fields — a contrast window, a
// dither, a radial dim, a breathing swell — that existed for the organic fields
// retired in V5, and not one of them was a knob a surviving variant turned. A new
// variant that wants one re-adds it with its own justification, which is healthier
// than inheriting an unowned one: this package has already shipped that bug, with
// dimToRim declared and never read, so rain silently took a 42% dim it had opted
// out of. git show 0734403 is the archive.
type splashOps struct {
	// stars draws the fixed twinkling starfield over the field.
	stars bool
	// lumRange is the share of a cell's brightness that rides its colour's
	// luminance rather than its glyph's density.
	//
	// 0 is the shading every field had before there was a choice: density carries
	// all of it, so a dim cell is necessarily a *small* one and a fading field
	// degenerates into a scatter of dots. 1 is rain's: a constant-weight glyph with
	// all brightness in the colour, which is the only thing that works when the
	// glyph vocabulary has no light end to fade into. Between them the two channels
	// split it, so density can carry texture while luminance carries brightness.
	//
	// Both endpoints are reproduced exactly and for free (see splashShade). No
	// shipped variant sits at 0 any more — the fields that wanted their stipple were
	// the organic ones — so that endpoint survives for the dev override and for a
	// future variant rather than for the roster.
	lumRange float64
}

// ops returns a variant's Pass-2 policy: the shipped per-variant literal, before
// the dev-only lumRange override package ui applies on top (see Options.LumRange
// and Render).
//
// Every variant states both fields as a literal, and the roster table
// (TestShippedVariantsOps) pins them per variant, so a new one has to make an
// explicit choice rather than inherit whatever the zero value happens to be. Rain
// and the tunnel currently agree on both and are still written out separately on
// purpose: merging them would silently move one when the other is retuned.
func (v Variant) ops() splashOps {
	switch v {
	case Tunnel:
		return splashOps{
			// Screen-fixed stars punching through a moving wall destroy vection —
			// the reflex that makes a receding texture read as self-motion rather
			// than as a pattern. This is the variant's whole premise, so it is not
			// a preference.
			stars: false,
			// The fog is a gradient with no stipple to spend, so it is exactly what
			// the luminance channel was built for. Density cannot carry depth here:
			// "dim" would mean "small", and a far wall would read as a scatter of
			// dots rather than as distance. Chosen from a rendered sweep of
			// {0, 0.5, 0.75, 1}, not from the arithmetic — at 1 the glyph is a
			// constant '@' and every bit of the corridor is drawn in colour.
			lumRange: 1,
		}
	case Ripple:
		return splashOps{
			// The one variant that keeps the starfield, because for once fixed
			// points are *right*: this field is a dark pool, and a still pool
			// reflects a still sky. Rain and the tunnel had to drop the stars because
			// the eye tracks their motion and a fixed point in a moving field reads
			// as a stuck pixel — but nothing here travels except the rings, and the
			// pool between them is empty enough to want the company.
			stars: true,
			// Ripple's ring decay is a gradient with no stipple to spend, so the
			// luminance channel is exactly what it was built for — but this one stops
			// short of rain's and the tunnel's 1, and the sweep is what decided it.
			//
			// At 1 every lit cell is a solid '@' and the rings read as a bitmap of
			// bubbles: correct, and less like something a terminal drew. At 0.75 the
			// crests get the density ramp back (a band steps o → O → 0 → @ across its
			// width) while the tail keeps enough of its brightness in the colour to
			// stay dim rather than breaking into the scatter of dots that is this
			// palette's whole defect. Below that the ramp reaches into the packet's
			// faint halo and the halo is most of the field's area — at 0.5 it renders
			// as confetti around every ring.
			lumRange: 0.75,
		}
	default:
		// Rain, and the fallback for a variant with no case here.
		return splashOps{
			// Stars are fixed points; rain is moving ones. Together the fixed ones
			// read as stuck pixels, and rain has its own highlight anyway.
			stars: false,
			// All brightness rides the colour; the glyph stays a constant mark.
			// This is the whole difference between a stream and a column of dots —
			// see buildRainRamp.
			lumRange: 1,
		}
	}
}
