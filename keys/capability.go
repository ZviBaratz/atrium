package keys

import "sync/atomic"

// capability.go holds what Atrium has learned about the terminal's keyboard, as
// opposed to what Atrium asks of it.
//
// There is exactly one such fact today: whether the terminal disambiguates
// modified keys. Bubble Tea requests that unconditionally — cursed_renderer.go's
// keyboardEnhancementsFlags opens with `flags := 1 // always enable basic key
// disambiguation`, and the renderer writes the kitty enable, xterm's
// modifyOtherKeys, and the kitty query on its first flush — so Atrium never sends
// anything itself. What it does is listen: a terminal that supports the protocol
// answers the query, Bubble Tea turns the answer into a KeyboardEnhancementsMsg,
// and app's handler latches it here.
//
// A terminal without the protocol never answers, so ABSENCE IS THE ONLY "NO".
// That is why the zero value has to mean "not disambiguating": the state Atrium
// starts in is also the state it stays in for every terminal that will never tell
// it otherwise, and the surfaces that read this must degrade to the pre-protocol
// behaviour by default rather than by correction.
//
// The fact belongs in this package because this package already reasons about
// what a terminal can and cannot send: ParseKey rejects shift+<printable> because
// no terminal sends it, and wireAmbiguousCtrl (override.go) is the existing
// vocabulary for chords a terminal cannot tell apart. This is the other half of
// that same question.

// disambiguates is atomic rather than a plain var, and the distinction from its
// neighbours is the point.
//
// GlobalKeyBindings and GlobalKeyStringsMap are plain vars read from the render
// and update paths with no synchronisation, and Apply says why that is safe: it
// is called once, before tea.NewProgram, so the write happens-before the
// existence of any goroutine that reads them. This latch cannot inherit that
// argument — it is written from Update, while the program runs. What would be
// left is the weaker claim that every reader happens to sit on the Bubble Tea
// loop goroutine, which is exactly the call-site invariant ui/theme/mono.go
// declines to rest on: true today, and one live-reading Cmd away from being
// false. A single-word atomic costs nothing and the reader does not have to
// reconstruct the argument.
var disambiguates atomic.Bool

// TerminalDisambiguates reports whether the terminal confirmed that it
// distinguishes modified keys — shift+enter from enter, ctrl+m from enter, and
// the rest of the combinations a legacy terminal collapses onto one control code.
//
// False until the terminal says otherwise, which includes every terminal that
// never will. Read it to decide what to PROMISE a user, never to decide whether
// to handle a key: a key the terminal cannot send simply never arrives, so
// handling it unconditionally is free, while advertising it unconditionally is
// the defect this exists to fix (#396).
//
// Safe to call from any goroutine.
func TerminalDisambiguates() bool { return disambiguates.Load() }

// SetTerminalDisambiguates records the terminal's answer, returning a function
// that restores the previous value — matching theme.SetMono, theme.Set and
// theme.SetGlyphSet in shape, so a test cannot leave the rest of the suite
// believing in a capability the next test's terminal does not have.
func SetTerminalDisambiguates(on bool) (restore func()) {
	prev := disambiguates.Swap(on)
	return func() { disambiguates.Store(prev) }
}
