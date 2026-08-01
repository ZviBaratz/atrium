package app

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/colorprofile"
	"github.com/stretchr/testify/require"
)

// nocolour_test.go is the NO_COLOR oracle, and it has to be its own file because
// the existing colour fingerprint cannot see what it guards.
// TestFrameColourFingerprint reads View().Content — the string the model
// produces, BEFORE Bubble Tea's renderer down-samples it — so a fix that lives in
// the colour profile is completely invisible there. This test puts the frame
// through a writer pinned to the same profile instead.
//
// One honesty note about what that proves. Production does not use
// colorprofile.Writer: Bubble Tea hands the profile to ultraviolet, whose
// ConvertStyle nils a cell's Fg/Bg/UnderlineColor under Ascii (and returns a bare
// Style{} under NoTTY). Measured on both paths, same verdict — but this file
// models the production strip rather than exercising it, and the live drive in
// #394 Stage D Task 6 Step 7 is what covers the real renderer.

// sgrCarriesColour reports whether an SGR sequence sets a colour. It classifies
// what the package's existing sgrRE finds, rather than being a second matcher, so
// the two oracles cannot disagree about what an SGR sequence even is.
//
// It reads the parameter list instead of pattern-matching it, and that is not
// fastidiousness — a regex was tried first and matched ESC[38;2;65;72;104m by
// reading the 104 out of the RGB triple as a bright-background code. Right answer,
// wrong reason, and a wrong reason cannot be mutation-tested: deleting the
// truecolor branch left the test green because the accident covered for it.
//
// Returning at 38/48/58 is what keeps it honest. Everything after one of those is
// that colour's payload, so nothing downstream is ever read as a parameter in its
// own right.
//
// Attributes are deliberately NOT colour. Bold, italic and underline must SURVIVE
// the strip, and asserting their survival is what stops a later "fix" reaching for
// NoTTY, which takes them too and flattens the whole hierarchy. Nor is the bare
// ESC[m reset colour — that is precisely what the Ascii profile rewrites a
// pure-colour sequence INTO, so counting it would make the oracle fail on its own
// fix. 39 and 49 are likewise excluded: they remove colour rather than carry it.
func sgrCarriesColour(seq string) bool {
	body := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b["), "m")
	for _, param := range strings.Split(body, ";") {
		// Colon sub-parameters belong to the parameter they follow (ESC[4:3m,
		// ESC[38:2::r:g:bm), so only the leading number decides what this is.
		n, err := strconv.Atoi(strings.SplitN(param, ":", 2)[0])
		if err != nil {
			continue
		}
		switch {
		case n == 38, n == 48, n == 58: // extended fg / bg / underline colour
			return true
		case n >= 30 && n <= 37, n >= 40 && n <= 47: // 16-colour fg / bg
			return true
		case n >= 90 && n <= 97, n >= 100 && n <= 107: // bright fg / bg
			return true
		}
	}
	return false
}

// colourSGRsIn returns the colour-carrying SGR sequences in s.
func colourSGRsIn(s string) []string {
	var found []string
	for _, seq := range sgrRE.FindAllString(s, -1) {
		if sgrCarriesColour(seq) {
			found = append(found, seq)
		}
	}
	return found
}

// TestSGRCarriesColourClassifiesEveryForm pins the oracle's own instrument. Both
// frame tests below are only as good as this predicate: one missing case and
// TestNoColorFrameCarriesNoColour passes over real colour.
//
// The inputs are the forms measured through colorprofile.Writer at Ascii — the
// ones it rewrites to ESC[m or ESC[<attr>m are colour, the ones that pass through
// unchanged are not.
func TestSGRCarriesColourClassifiesEveryForm(t *testing.T) {
	for _, seq := range []string{
		"\x1b[31m",             // 16-colour fg, first position — the dead-branch case
		"\x1b[1;31m",           // 16-colour fg after an attribute
		"\x1b[41m",             // 16-colour bg
		"\x1b[91m",             // bright fg
		"\x1b[107m",            // bright bg
		"\x1b[38;2;255;0;0m",   // truecolor fg
		"\x1b[48;2;0;0;255m",   // truecolor bg
		"\x1b[38;5;33m",        // 256-colour fg
		"\x1b[58;5;33m",        // underline colour
		"\x1b[4;38;2;1;2;3m",   // underline + truecolor fg
		"\x1b[1;3;4;38;5;200m", // several attributes then a colour
		"\x1b[38:2::1:2:3m",    // colon-delimited truecolor, the ITU form
	} {
		require.Truef(t, sgrCarriesColour(seq),
			"%q carries colour and must be detected", strings.ReplaceAll(seq, "\x1b", "ESC"))
	}

	for _, seq := range []string{
		"\x1b[m",      // the reset Ascii rewrites a pure-colour sequence INTO
		"\x1b[0m",     // the explicit reset
		"\x1b[1m",     // bold — must survive
		"\x1b[3m",     // italic — must survive
		"\x1b[4m",     // underline — must survive
		"\x1b[1;3;4m", // all three at once
		"\x1b[24m",    // no-underline
		"\x1b[22m",    // normal intensity
		"\x1b[10m",    // primary font: two digits starting 1, not a colour
		"\x1b[39m",    // default foreground: removes colour, does not carry it
		"\x1b[49m",    // default background
		"\x1b[4:3m",   // curly underline: the 3 is a sub-parameter, not a colour
		"\x1b[59m",    // default underline colour
	} {
		require.Falsef(t, sgrCarriesColour(seq),
			"%q carries no colour and must not be detected", strings.ReplaceAll(seq, "\x1b", "ESC"))
	}
}

// asciiProfileFrame renders a state and pushes it through a writer pinned to the
// profile NO_COLOR selects, returning what would reach the terminal.
func asciiProfileFrame(t *testing.T, fs frameState, w, h int) string {
	t.Helper()
	var buf bytes.Buffer
	writer := &colorprofile.Writer{Forward: &buf, Profile: noColorProfile()}
	_, err := writer.WriteString(newParityHome(t, fs, w, h).View().Content)
	require.NoError(t, err)
	return buf.String()
}

// TestNoColorFrameCarriesNoColour is AC#3's mechanical half: under the Ascii
// profile no colour survives, in any state.
func TestNoColorFrameCarriesNoColour(t *testing.T) {
	const w, h = 120, 40
	for _, fs := range frameStates() {
		t.Run(fs.name, func(t *testing.T) {
			if found := colourSGRsIn(asciiProfileFrame(t, fs, w, h)); len(found) > 0 {
				t.Errorf("state %s emitted %d colour sequences under the Ascii profile; first: %q",
					fs.name, len(found), strings.ReplaceAll(found[0], "\x1b", "ESC"))
			}
		})
	}
}

// TestNoColorFramePreservesAttributes is the other half, and it is the one that
// makes the UI navigable rather than merely colourless: bold survives. Without
// this assertion, swapping Ascii for NoTTY would keep TestNoColorFrameCarriesNoColour
// green while destroying every non-colour distinction on screen.
func TestNoColorFramePreservesAttributes(t *testing.T) {
	const w, h = 120, 40
	// The default state's list renders a bold display name; the help overlay's
	// title is bold too. Two states, so the assertion does not rest on one.
	for _, want := range []string{"default", "help"} {
		var seen bool
		for _, fs := range frameStates() {
			if fs.name != want {
				continue
			}
			seen = true
			out := asciiProfileFrame(t, fs, w, h)
			require.Containsf(t, out, "\x1b[1m",
				"state %s lost bold under the Ascii profile: monochrome must keep the weight hierarchy", want)
		}
		require.Truef(t, seen, "state %q not present in frameStates()", want)
	}
}

// TestNoColorPicksTheAsciiProfile guards the WIRE, not the mechanism. The two
// tests above build their own writer, so both stay green if app.Run stops passing
// the profile at all — the same class as #391's "a derived value passed as an
// argument is a wire nothing guards".
func TestNoColorPicksTheAsciiProfile(t *testing.T) {
	require.Equal(t, colorprofile.Ascii, noColorProfile(),
		"NO_COLOR must select Ascii: it drops colour and keeps bold/italic/underline, where NoTTY strips those too")
}

// TestProgramOptionsPinsTheProfileOnlyUnderMono is the other half of that wire:
// that Run actually asks for the profile, and only when colour is suppressed.
//
// It counts options rather than identifying them, because tea.ProgramOption is an
// opaque func with nothing to inspect. So it catches the branch being deleted, not
// the option being swapped for a different one — TestNoColorPicksTheAsciiProfile
// is what catches the swap.
func TestProgramOptionsPinsTheProfileOnlyUnderMono(t *testing.T) {
	require.Len(t, programOptions(t.Context()), 1, "colour on: no profile override")

	defer theme.SetMono(true)()
	require.Len(t, programOptions(t.Context()), 2, "NO_COLOR must add the colour-profile option")
}
