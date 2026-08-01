package theme

import (
	"strings"
	"sync/atomic"
)

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
//     pushes into each session's tmux user options; session/tmux/barstyle.go, for
//     status-style — barStyleColours is ONE reader serving BOTH routes to that
//     option, the managed config a server reads when it STARTS (rendered in
//     session/tmux/config.go, which calls the helper rather than reading Mono()
//     itself) and the live push to a server already running after a theme change,
//     because fixing either route alone leaves half the fleet's bars disagreeing,
//     which is the split barstyle.go exists to prevent; and ui/splash.go, for the
//     splash's colour-borne brightness channel, since with colour gone a channel
//     that spends brightness on colour spends it on nothing.
//
//     Keep that list in step with the tree rather than trusting it:
//     `git grep -n 'theme.Mono()' -- '*.go'`. It must be git grep: `-- '<glob>'` is
//     a git pathspec, and plain grep reads it as a literal filename and exits 2
//     having searched nothing. The hit that list does not name is app/app.go —
//     that one is (1)'s renderer pin, not an emitter opting out.
//
// Mono deliberately does NOT blank the palette. A monochrome palette would defeat
// (1) — the renderer's own strip is what handles the hand-written escapes — and it
// would lose the bold/italic/underline hierarchy along the way. Do not "simplify"
// this by making Current() return greys.

// mono is atomic for the same reason current is, and on the same rule: it is READ
// off the bubbletea loop. barStyleColours reaches it from the tea.Cmd that restyles
// the fleet after a theme change, and app_layout.go's barStyleApplier states the
// requirement outright — every global that Cmd reaches has to be safe off the
// update thread. curName/curGlyphSet get to stay plain vars because nothing reads
// them off the loop; mono does not qualify for that exemption.
//
// curScheme (current.go) is atomic without qualifying either. Nothing reaches it off
// the loop, so the curName exemption was available and was declined: its getter
// CurrentScheme() is exported directly beside Current(), and two neighbouring
// getters with opposite concurrency contracts is a footgun. The rule this file
// argues — that safety resting on an invariant of the CALL SITE is one feature away
// from being false — is why it was declined rather than a reason it had to be.
//
// Today a race is unreachable anyway: SetMono is called once, from main, before any
// goroutine that reads it exists, so the write happens-before every read. That is an
// invariant of the CALL SITE, not of this type, and it is one live-toggling colour
// from a settings panel away from being false — which is the exact transition
// current itself already went through. A single-word atomic on a path that already
// shells out to tmux costs nothing measurable; relying on the invariant costs a
// reader the whole argument above.
var mono atomic.Bool

// Mono reports whether colour output is suppressed. Read by the emitters Lip
// Gloss does not cover; see the file comment for why that is not everything.
// Safe to call from any goroutine.
func Mono() bool { return mono.Load() }

// SetMono suppresses or restores colour for the non-Lip-Gloss emitters, returning
// a function that restores the previous value — matching Set and SetGlyphSet in
// shape, so a test cannot leave the rest of the suite monochrome.
//
// Concurrent SetMono calls race each other's restore functions, exactly as Set and
// SetGlyphSet do. That is fine for the callers that exist: one startup call, and
// tests that set and restore on their own goroutine.
func SetMono(on bool) (restore func()) {
	prev := mono.Swap(on)
	return func() { mono.Store(prev) }
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
