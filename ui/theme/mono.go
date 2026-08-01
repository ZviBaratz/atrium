package theme

import "strings"

// mono.go is the NO_COLOR seam.
//
// Colour is removed in two different places, on purpose, because two different
// kinds of emitter produce it:
//
//  1. Everything Atrium renders through Lip Gloss is stripped at the RENDERER, by
//     handing Bubble Tea the colorprofile.Ascii profile (see app.programOptions).
//     That covers the frame wholesale, including escapes Atrium writes by hand —
//     the overlay fade's SGR rewrite is the case that matters — and it keeps bold,
//     italic and underline, which is what leaves a monochrome UI navigable.
//     colorprofile's NoTTY profile would strip those too and flatten the
//     hierarchy; measured, in both the writer and the cell renderer.
//
//  2. Everything Atrium emits that Lip Gloss never sees has to opt out itself, and
//     Mono() is how it asks. What reads it today, named rather than counted:
//     ui/contextbar.go, for the #[fg=...] markup in the session header Atrium
//     pushes into each session's tmux user options; session/tmux/config.go, for
//     status-style in the managed config that tmux reads when a SERVER starts;
//     session/tmux/barstyle.go, for the same status-style pushed to a server
//     already running after a live theme change — two routes to one option, and
//     fixing either alone leaves half the fleet's bars disagreeing, the split
//     barstyle.go exists to prevent; and ui/splash.go, for the splash's
//     colour-borne brightness channel, since with colour gone a channel that
//     spends brightness on colour spends it on nothing.
//
//     Keep that list in step with the tree rather than trusting it:
//     `grep -rn 'theme.Mono()' -- '*.go'`.
//
// Mono deliberately does NOT blank the palette. A monochrome palette would defeat
// (1) — the renderer's own strip is what handles the hand-written escapes — and it
// would lose the bold/italic/underline hierarchy along the way. Do not "simplify"
// this by making Current() return greys.

var mono bool

// Mono reports whether colour output is suppressed. Read by the emitters Lip
// Gloss does not cover; see the file comment for why that is not everything.
func Mono() bool { return mono }

// SetMono suppresses or restores colour for the non-Lip-Gloss emitters, returning
// a function that restores the previous value — matching Set and SetGlyphSet, so
// a test cannot leave the rest of the suite monochrome.
func SetMono(on bool) (restore func()) {
	prev := mono
	mono = on
	return func() { mono = prev }
}

// NoColorRequested reports whether environ asks for no colour, per
// https://no-color.org: the variable being PRESENT AND NON-EMPTY is the request,
// whatever its value.
//
// Atrium implements this itself rather than leaning on the dependency, because
// colorprofile parses NO_COLOR through strconv.ParseBool — so NO_COLOR=yes, =x,
// =0 and =2 leave colour fully on. Four spec violations, inherited for free by
// anyone who assumes the stack handles it. Re-measured on colorprofile v0.4.3:
// colorprofile.Env answers Ascii only for 1/true/TRUE.
//
// environ is a parameter rather than an os.Environ() call so the rule is testable
// as a pure function. That is not a style preference: a predicate whose input is
// read at package load cannot be reached by t.Setenv at all, which is how #394
// Stage C's first splash test came out unfalsifiable. Later entries win, matching
// os.Environ semantics for a duplicated name.
func NoColorRequested(environ []string) bool {
	requested := false
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name != "NO_COLOR" {
			continue
		}
		requested = value != ""
	}
	return requested
}
