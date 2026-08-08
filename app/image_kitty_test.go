package app

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawString runs a command the runtime would and returns the string tea.Raw
// queued, or "" when the command was nil.
//
// It asserts the RAW MESSAGE's payload rather than "a command was returned",
// because tea.Raw takes an `any` and runs it through fmt.Sprint: a []byte would
// come back as "[27 95 71 …]" and print that to the terminal, and a test that
// stopped at non-nil would pass on it.
func rawString(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		return ""
	}
	msg := cmd()
	raw, ok := msg.(tea.RawMsg)
	require.Truef(t, ok, "expected a tea.RawMsg, got %T", msg)
	s, ok := raw.Msg.(string)
	require.Truef(t, ok, "tea.Raw must be handed a string, got %T", raw.Msg)
	return s
}

// openKittyPreview opens the overlay in an environment where the pixel rung is
// eligible, and returns the transmission it queued.
func openKittyPreview(t *testing.T, h *home) string {
	t.Helper()
	h.windowWidth, h.windowHeight = 100, 30
	h.kittyEnviron = []string{"TERM=xterm-kitty", "COLORTERM=truecolor"}
	cmd := h.openImagePreview(overlay.Image{
		Path: "/fixture/shot.png", Pixels: parityImage(), Width: 30, Height: 20,
	})
	require.Equal(t, stateImagePreview, h.state)
	return rawString(t, cmd)
}

func TestKittyEligible(t *testing.T) {
	kittyTerm := []string{"TERM=xterm-kitty"}
	ghostty := []string{"TERM=xterm-256color", "TERM_PROGRAM=ghostty"}
	plain := []string{"TERM=xterm-256color"}
	// TERM_PROGRAM is INHERITED into tmux, so this environment is exactly the
	// false positive the veto exists for: the sniff says ghostty and the payload
	// would go to tmux, which neither implements the protocol nor forwards it.
	ghosttyInTmux := []string{"TERM_PROGRAM=ghostty", "TMUX=/tmp/s,1,0"}

	for _, tc := range []struct {
		name    string
		environ []string
		pref    string
		mono    bool
		want    bool
	}{
		{"auto on kitty", kittyTerm, config.ImagePreviewAuto, false, true},
		{"auto on ghostty", ghostty, config.ImagePreviewAuto, false, true},
		{"auto on an unrecognised terminal", plain, config.ImagePreviewAuto, false, false},
		{"auto inside tmux", ghosttyInTmux, config.ImagePreviewAuto, false, false},
		{"kitty overrides the tmux veto", ghosttyInTmux, config.ImagePreviewKitty, false, true},
		{"kitty overrides the terminal sniff", plain, config.ImagePreviewKitty, false, true},
		{"glyph refuses on kitty", kittyTerm, config.ImagePreviewGlyph, false, false},
		{"off refuses on kitty", kittyTerm, config.ImagePreviewOff, false, false},
		// NO_COLOR pins the profile to Ascii, which strips the foreground — and
		// the foreground IS the image ID, so the cells would address nothing.
		{"mono vetoes even the explicit opt-in", kittyTerm, config.ImagePreviewKitty, true, false},
		{"mono vetoes auto on kitty", kittyTerm, config.ImagePreviewAuto, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, kittyEligible(tc.environ, tc.pref, tc.mono, true, true))
		})
	}
}

// An empty TMUX is not something tmux sets, so it must not trip the veto.
func TestKittyEligible_EmptyTmuxIsNotTmux(t *testing.T) {
	assert.True(t, kittyEligible(
		[]string{"TERM=xterm-kitty", "TMUX="}, config.ImagePreviewAuto, false, true, true))
}

// Opening transmits only where the rung is eligible, and the payload is the
// image — not a placement, and not a description of a byte slice.
func TestOpenImagePreview_TransmitsOnlyWhenEligible(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	got := openKittyPreview(t, h)

	require.True(t, strings.HasPrefix(got, "\x1b_G"), "an APC transmission, got %q", got)
	assert.Contains(t, got, "f=100", "a PNG, not eight megabytes of raw pixels")
	assert.Contains(t, got, "I=1", "tagged so the reply can be matched")

	// The same open on an unrecognised terminal must queue nothing at all.
	h2 := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	h2.windowWidth, h2.windowHeight = 100, 30
	h2.kittyEnviron = []string{"TERM=xterm-256color", "COLORTERM=truecolor"}
	cmd := h2.openImagePreview(overlay.Image{Path: "/fixture/shot.png", Pixels: parityImage()})
	assert.Nil(t, cmd, "an unrecognised terminal must not be written to")
	assert.Equal(t, stateImagePreview, h2.state, "but the glyph rung still opens")
}

// The overlay must be drawable the instant it opens, before any reply. This is
// the property that makes the whole rung safe: there is no loading state to get
// wrong and no timeout to tune, because the lower rung is already on screen.
func TestOpenImagePreview_DrawsGlyphsBeforeAnyReply(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)

	before := h.imageOverlay.Render()
	require.NotEmpty(t, before)
	assert.NotContains(t, before, string(kitty.Placeholder),
		"no placeholder may exist before the terminal has named the image")
	assert.Zero(t, h.kittyID)
}

// The confirmation upgrades the open overlay, and the placement it emits must
// describe the same grid the cells were built for.
func TestHandleKittyGraphics_UpgradesTheOverlayAndPlacesIt(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)

	_, cmd := h.handleKittyGraphics(kittyGraphicsConfirmed{number: h.kittyNumber, id: 0xAABBCC})
	require.Equal(t, uint32(0xAABBCC), h.kittyID)

	after := h.imageOverlay.Render()
	assert.Contains(t, after, string(kitty.Placeholder), "the picture is placeholders now")
	assert.Contains(t, after, "\x1b[38;2;170;187;204m", "whose foreground is the image ID")

	place := rawString(t, cmd)
	cols, rows, ok := h.imageOverlay.KittyCells()
	require.True(t, ok)
	assert.Contains(t, place, "U=1", "a virtual placement, not one at the cursor")
	assert.Contains(t, place, "i=11189196", "for the ID the terminal assigned")
	assert.Contains(t, place, "c="+strconv.Itoa(cols), "the placement must declare the cells' own grid")
	assert.Contains(t, place, "r="+strconv.Itoa(rows), "the placement must declare the cells' own grid")
}

// The upgrade must survive the picture cache, and this is the sequence
// production actually runs: the overlay draws glyphs for however many frames it
// takes the terminal to answer, and only then does the reply land.
//
// Rendering first is the whole test. ImageOverlay caches the rendered picture
// under a key of everything it depends on, and its own doc comment says a field
// added to the render and not to the key serves a stale picture — so a test that
// renders only after the upgrade finds a cold cache and passes either way.
func TestHandleKittyGraphics_UpgradeBeatsTheCachedGlyphs(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)

	first := h.imageOverlay.Render()
	require.NotContains(t, first, string(kitty.Placeholder), "glyphs while we wait")

	h.handleKittyGraphics(kittyGraphicsConfirmed{number: h.kittyNumber, id: 0xAABBCC})

	assert.Contains(t, h.imageOverlay.Render(), string(kitty.Placeholder),
		"the cached glyph picture must not outlive the image ID that replaced it")
}

// Every reply that is not ours must be dropped, and each of these is a distinct
// way for one to arrive: another program sharing the terminal mid-transmission,
// an error reply that carries no ID, and a reply for the picture before this one.
func TestHandleKittyGraphics_DropsForeignAndStaleReplies(t *testing.T) {
	t.Run("an unnumbered reply outside the window", func(t *testing.T) {
		h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
		openKittyPreview(t, h)
		// Answer the outstanding transmission, closing the window.
		h.handleKittyGraphics(kittyGraphicsConfirmed{number: h.kittyNumber, id: 42})

		// A second, unnumbered reply now belongs to some other program: there is
		// nothing outstanding for it to be an answer to.
		_, cmd := h.handleKittyGraphics(kittyGraphicsConfirmed{id: 99})
		assert.Equal(t, uint32(42), h.kittyID, "an untagged reply must not retarget a settled image")
		assert.Nil(t, cmd)
	})

	t.Run("another program's number", func(t *testing.T) {
		h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
		openKittyPreview(t, h)

		_, cmd := h.handleKittyGraphics(kittyGraphicsConfirmed{number: 99, id: 7})
		assert.Zero(t, h.kittyID)
		assert.Nil(t, cmd)
		assert.NotContains(t, h.imageOverlay.Render(), string(kitty.Placeholder))
	})

	// An error reply carries no ID. Asserting that from the un-upgraded state
	// proves nothing — kittyID is zero whether the reply was refused or accepted
	// as zero — so this drives it AFTER a success, where the two answers differ:
	// refusing keeps the picture, accepting resets it to glyphs.
	t.Run("an error reply after a success must not undo it", func(t *testing.T) {
		h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
		openKittyPreview(t, h)
		h.handleKittyGraphics(kittyGraphicsConfirmed{number: h.kittyNumber, id: 42})
		require.Equal(t, uint32(42), h.kittyID)

		_, cmd := h.handleKittyGraphics(kittyGraphicsConfirmed{number: h.kittyNumber, id: 0})
		assert.Equal(t, uint32(42), h.kittyID, "a reply with no id must not clear a confirmed one")
		assert.Nil(t, cmd)
		assert.Contains(t, h.imageOverlay.Render(), string(kitty.Placeholder),
			"the picture must survive an error reply that arrives late")
	})

	// A reply that lands after the user pressed esc has nothing to upgrade — and
	// upgrading anyway would dereference an overlay that is gone. It is not simply
	// dropped, though: see TestHandleKittyGraphics_LateReplyStillFreesTheImage.
	t.Run("the overlay already closed", func(t *testing.T) {
		h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
		openKittyPreview(t, h)
		number := h.kittyNumber
		h.closeImagePreview()

		_, cmd := h.handleKittyGraphics(kittyGraphicsConfirmed{number: number, id: 7})
		assert.Zero(t, h.kittyID, "nothing is on screen to upgrade")
		assert.Contains(t, rawString(t, cmd), "d=I", "but the stored image is freed")
	})
}

// Closing frees the terminal's copy of the pixels — but only when there is one.
// d=I is what frees the data; d=i would drop the placements and leak it.
func TestCloseImagePreview_ReleasesOnlyAConfirmedImage(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)
	assert.Empty(t, rawString(t, h.closeImagePreview()),
		"nothing was ever placed, so there is nothing to free")

	h = newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)
	h.handleKittyGraphics(kittyGraphicsConfirmed{number: h.kittyNumber, id: 42})

	got := rawString(t, h.closeImagePreview())
	assert.Contains(t, got, "d=I", "the uppercase form is what frees the image data")
	assert.Contains(t, got, "i=42")
	assert.Zero(t, h.kittyID, "and the app must forget it, or the next close re-deletes it")
}

// A resize re-places, and only when the grid actually changed. Re-emitting an
// identical placement every window event would put an escape on the hot path for
// nothing; never re-emitting would leave the cells addressing the old grid.
func TestPlaceKittyImage_SkipsARedundantResize(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)
	h.handleKittyGraphics(kittyGraphicsConfirmed{number: h.kittyNumber, id: 42})

	assert.Nil(t, h.placeKittyImageCmd(), "the grid has not changed")

	h.imageOverlay.SetSize(60, 20)
	assert.NotEmpty(t, rawString(t, h.placeKittyImageCmd()), "a smaller box is a new grid")
	assert.Nil(t, h.placeKittyImageCmd(), "and then it has not changed again")
}

// Nothing is placed while the overlay is on the glyph rung, so a window resize
// on an ordinary terminal writes no bytes at all.
func TestPlaceKittyImage_SilentWithoutPixels(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	h.windowWidth, h.windowHeight = 100, 30
	h.openImagePreview(overlay.Image{Path: "/fixture/shot.png", Pixels: parityImage()})
	assert.Nil(t, h.placeKittyImageCmd())
}

// Bubble Tea does not translate this event, so it reaches Update as
// ultraviolet's own type. The conversion is where a field-name change in an
// untagged dependency would land, so it is exercised rather than bypassed.
func TestKittyGraphicsEventFrom_ReadsTheReply(t *testing.T) {
	got := kittyGraphicsEventFrom(uv.KittyGraphicsEvent{
		Options: kitty.Options{ID: 987654, Number: 3},
	})
	assert.Equal(t, kittyGraphicsConfirmed{number: 3, id: 987654}, got)

	// The largest ID the protocol allows must still survive; a bound that
	// rejected it would refuse a legitimate reply.
	got = kittyGraphicsEventFrom(uv.KittyGraphicsEvent{
		Options: kitty.Options{ID: math.MaxUint32, Number: 1},
	})
	assert.Equal(t, uint32(math.MaxUint32), got.id)
}

// Options.ID is an int parsed from bytes the terminal sent, and ultraviolet
// discards the unmarshal error — so an out-of-range value is a reply Atrium can
// actually receive, not a hypothetical. A negative one is the dangerous shape:
// converted without a check it wraps to a large uint32 and would address an
// image belonging to another program.
func TestKittyGraphicsEventFrom_RefusesAnImpossibleID(t *testing.T) {
	for _, id := range []int{-1, 0, math.MaxUint32 + 1} {
		got := kittyGraphicsEventFrom(uv.KittyGraphicsEvent{Options: kitty.Options{ID: id, Number: 1}})
		assert.Zerof(t, got.id, "id %d must not become an image address", id)
	}
}

// The whole path, driven the way the runtime drives it: the uv event goes
// through Update rather than into the handler by hand.
func TestUpdate_RoutesTheGraphicsReply(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)

	h.Update(uv.KittyGraphicsEvent{Options: kitty.Options{ID: 5, Number: h.kittyNumber}})
	assert.Equal(t, uint32(5), h.kittyID, "Update must have a case for the untranslated event")
}

// image_preview: off restores what this gesture did for every path before #398.
// It is checked before the decode, because the read-and-decode is the cost the
// setting exists to refuse.
//
// Driven through the real gesture rather than by calling actHint with a
// hand-built Match, so the setting is proven to reach the key the user presses.
func TestActHint_OffSkipsTheImageOverlay(t *testing.T) {
	path := writeFixtureImage(t, filepath.Join(t.TempDir(), "screenshot.png"))

	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	fc := withFakeClipboard(t, nil)
	h.appConfig.ImagePreview = config.ImagePreviewOff
	_, _ = h.startHints(h.list.GetSelectedInstance(), "Saved to "+path+" ok\n")
	runCmdInto(t, h, pressRunes(h, "A"))

	assert.True(t, fc.called, "the copy half still happens; only the overlay is off")
	assert.Equal(t, stateDefault, h.state, "off must not open the overlay")
	assert.Nil(t, h.imageOverlay)

	// The negative control: the same path and the same gesture on the default
	// value. Without it, a branch that never opened anything would pass above.
	h2 := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	withFakeClipboard(t, nil)
	h2.appConfig.ImagePreview = ""
	_, _ = h2.startHints(h2.list.GetSelectedInstance(), "Saved to "+path+" ok\n")
	runCmdInto(t, h2, pressRunes(h2, "A"))
	assert.Equal(t, stateImagePreview, h2.state, "auto still opens it")
}

// theme.Mono is process-global, so this asserts the veto through the real
// accessor rather than the pure function, which is where a caller that forgot to
// pass it would show up.
func TestOpenImagePreview_MonoNeverTransmits(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	h.windowWidth, h.windowHeight = 100, 30
	h.kittyEnviron = []string{"TERM=xterm-kitty", "COLORTERM=truecolor"}

	restore := theme.SetMono(true)
	defer restore()
	cmd := h.openImagePreview(overlay.Image{Path: "/fixture/shot.png", Pixels: parityImage()})
	assert.Nil(t, cmd, "NO_COLOR strips the foreground the image ID rides in")
}

// Two terminals, two reply shapes, and the difference is not academic — it was
// measured, and getting it wrong costs one of them the whole rung.
//
// The protocol says a transmission tagged I=<number> is answered
// `i=<id>,I=<number>;OK`. kitty 0.45.0 does that. Ghostty 1.3.0 answers
// `\x1b_Gi=2147483647;OK` — the assigned ID and no number at all. Every unit
// test here synthesises its own replies, so nothing in this file could have
// caught that; it came from sending Atrium's own bytes to both terminals and
// reading what came back.
//
// Ghostty's ID is also over 24 bits, which is the only case that exercises the
// third diacritic in production. That is why the assertion uses the real value
// rather than a round number.
func TestHandleKittyGraphics_AcceptsBothTerminalsReplyShapes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply func(number int) kittyGraphicsConfirmed
		want  uint32
	}{
		{
			name:  "kitty echoes the image number",
			reply: func(n int) kittyGraphicsConfirmed { return kittyGraphicsConfirmed{number: n, id: 1} },
			want:  1,
		},
		{
			// Measured on Ghostty 1.3.0: \x1b_Gi=2147483647;OK
			name:  "ghostty omits it",
			reply: func(int) kittyGraphicsConfirmed { return kittyGraphicsConfirmed{id: 2147483647} },
			want:  2147483647,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
			openKittyPreview(t, h)

			_, cmd := h.handleKittyGraphics(tc.reply(h.kittyNumber))
			assert.Equal(t, tc.want, h.kittyID)
			assert.NotEmpty(t, rawString(t, cmd), "a confirmed image must be placed")
			assert.Contains(t, h.imageOverlay.Render(), string(kitty.Placeholder))
		})
	}
}

// Nothing is awaited before a transmission, so a reply that arrives out of the
// blue — another program's, or one for a preview that was never eligible — must
// not upgrade anything.
func TestHandleKittyGraphics_IgnoresRepliesWhenNothingWasSent(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	h.windowWidth, h.windowHeight = 100, 30
	h.kittyEnviron = []string{"TERM=xterm-256color", "COLORTERM=truecolor"} // not eligible: nothing is transmitted
	h.openImagePreview(overlay.Image{Path: "/fixture/shot.png", Pixels: parityImage()})
	require.Zero(t, h.kittyOutstanding)

	_, cmd := h.handleKittyGraphics(kittyGraphicsConfirmed{id: 7})
	assert.Zero(t, h.kittyID, "a reply to a transmission we never sent is not ours")
	assert.Nil(t, cmd)
}

// The bytes each terminal really sends, decoded by the parser that really
// decodes them.
//
// Everything else in this file synthesises a uv.KittyGraphicsEvent, which tests
// Atrium's half of the contract and assumes ultraviolet's. That assumption is
// exactly where the Ghostty defect lived: the reply shape differs between
// terminals, and no hand-written fixture would have shown it. These two strings
// were captured from kitty 0.45.0 and Ghostty 1.3.0 by sending them Atrium's own
// transmission and reading the tty.
func TestKittyReply_DecodedFromRealTerminalBytes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes string
		want  kittyGraphicsConfirmed
	}{
		{"kitty 0.45.0", "\x1b_Gi=1,I=7;OK\x1b\\", kittyGraphicsConfirmed{number: 7, id: 1}},
		{"ghostty 1.3.0", "\x1b_Gi=2147483647;OK\x1b\\", kittyGraphicsConfirmed{number: 0, id: 2147483647}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dec uv.EventDecoder
			n, ev := dec.Decode([]byte(tc.bytes))
			require.Equal(t, len(tc.bytes), n, "the whole sequence must be consumed")
			gfx, ok := ev.(uv.KittyGraphicsEvent)
			require.Truef(t, ok, "ultraviolet produced %T, not a KittyGraphicsEvent", ev)

			assert.Equal(t, tc.want, kittyGraphicsEventFrom(gfx))
		})
	}
}

// And the reply must survive the whole Update path, not just the converter —
// which is the half that would have stayed green while Ghostty got nothing.
func TestUpdate_AcceptsGhosttysUnnumberedReply(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)

	var dec uv.EventDecoder
	_, ev := dec.Decode([]byte("\x1b_Gi=2147483647;OK\x1b\\"))
	h.Update(ev)

	assert.Equal(t, uint32(2147483647), h.kittyID,
		"Ghostty omits the image number; requiring it costs that terminal the rung")
	assert.Contains(t, h.imageOverlay.Render(), string(kitty.Placeholder))
}

// The two drawability vetoes the first cut of this gate simply did not have.
//
// Both are silent failures: the transmission is confirmed, the overlay switches
// to placeholders, and there is no way back — Placeholders succeeds, so the
// overlay never reverts to glyphs. What the user gets is a wrapped, ghosting box
// (a placeholder measuring two columns) or an empty one (a foreground the profile
// rewrote, so the cells name an image that does not exist).
//
// The explicit opt-in must NOT override either: image_preview: kitty says "this
// terminal is not on my list", not "draw it wrong".
func TestKittyEligible_RefusesWhenTheCellsCannotBeDrawn(t *testing.T) {
	env := []string{"TERM=xterm-kitty"}
	for _, pref := range []string{config.ImagePreviewAuto, config.ImagePreviewKitty} {
		t.Run(pref+"/placeholder measures two cells", func(t *testing.T) {
			assert.False(t, kittyEligible(env, pref, false, true, false))
		})
		t.Run(pref+"/profile is not truecolor", func(t *testing.T) {
			assert.False(t, kittyEligible(env, pref, false, false, true))
		})
		// The positive control: with both satisfied the same call is eligible, so
		// the assertions above are about these axes and not about the preference.
		t.Run(pref+"/both satisfied", func(t *testing.T) {
			assert.True(t, kittyEligible(env, pref, false, true, true))
		})
	}
}

// The gate must read the LIVE width and profile, not assume them. This drives
// openImagePreview rather than the pure rule, because the defect it guards was a
// caller that never passed the inputs at all — the rule was right and unreachable.
func TestOpenImagePreview_RefusesANonTrueColorEnvironment(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	h.windowWidth, h.windowHeight = 100, 30
	// kitty by name, but an environment that resolves to 256 colours.
	h.kittyEnviron = []string{"TERM=xterm-256color", "KITTY_WINDOW_ID=1"}

	cmd := h.openImagePreview(overlay.Image{Path: "/fixture/shot.png", Pixels: parityImage()})
	assert.Nil(t, cmd, "a downsampling profile rewrites the ID the cells are addressed by")
	assert.Equal(t, stateImagePreview, h.state, "the glyph rung still opens")
}

// The sequence that made removing one line in closeImagePreview a real defect:
// open, close before the reply, open again. A's reply must not land on B.
//
// It only bites because Ghostty's replies carry no image number — with a number
// the mismatch would reject it. That is why this drives the unnumbered shape.
func TestHandleKittyGraphics_StaleReplyCannotRetargetTheNextImage(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h) // image A
	h.closeImagePreview()  // esc before the terminal answers
	openKittyPreview(t, h) // image B, still awaiting its own reply

	// A's answer, arriving late and untagged.
	_, cmd := h.handleKittyGraphics(kittyGraphicsConfirmed{id: 111})

	assert.Zero(t, h.kittyID, "a reply for the previous image must not name this one")
	assert.NotContains(t, h.imageOverlay.Render(), string(kitty.Placeholder))
	// The abandoned image is still sitting in the terminal, so it is freed rather
	// than forgotten.
	assert.Contains(t, rawString(t, cmd), "i=111")

	// And B's own reply still works, so the guard rejects the stale one without
	// closing the door on the real one.
	h.handleKittyGraphics(kittyGraphicsConfirmed{id: 222})
	assert.Equal(t, uint32(222), h.kittyID)
}

// A confirmation that lands after the box closed still has to free the image.
//
// The terminal has decoded and stored the PNG by then, and only d=I releases it.
// Dropping the reply — which the nil-overlay check does — leaks a megapixel per
// open/esc cycle for the life of the terminal. This is the one case "closing the
// overlay deletes the image by construction" does not cover, because the cells
// never existed to be removed.
func TestHandleKittyGraphics_LateReplyStillFreesTheImage(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)
	number := h.kittyNumber
	h.closeImagePreview()

	_, cmd := h.handleKittyGraphics(kittyGraphicsConfirmed{number: number, id: 77})

	got := rawString(t, cmd)
	assert.Contains(t, got, "d=I", "the image the terminal stored must be freed")
	assert.Contains(t, got, "i=77")
	assert.Zero(t, h.kittyID, "nothing is on screen, so nothing is placed")
}

// Duplicated environment names must resolve the same way here as in doctor, or
// the two disagree about the same environment — and doctor's whole job in this
// section is to explain a surprise.
func TestKittyTerminalEnv_LastWinsLikeDoctor(t *testing.T) {
	// A kitty TERM later overridden by a plain one is NOT kitty.
	assert.False(t, kittyTerminalEnv([]string{"TERM=xterm-kitty", "TERM=xterm-256color"}))
	// And the other way round.
	assert.True(t, kittyTerminalEnv([]string{"TERM=xterm-256color", "TERM=xterm-kitty"}))
}

// A reply tagged with a number that is not ours must be left completely alone —
// not counted, and above all not DELETED.
//
// Image numbers are client-chosen, so another graphics program sharing this
// terminal can pick any of them. An earlier version of the disposal path deleted
// the image named by any reply it decided was "stale", which for a foreign number
// means freeing somebody else's picture out from under them.
func TestHandleKittyGraphics_NeverDeletesAForeignImage(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)
	before := h.kittyOutstanding

	_, cmd := h.handleKittyGraphics(kittyGraphicsConfirmed{number: 99, id: 7})

	assert.Nil(t, cmd, "another program's image must not be freed by us")
	assert.Equal(t, before, h.kittyOutstanding, "and it does not answer our transmission")

	// Our own reply still lands afterwards, so the guard did not consume it.
	h.handleKittyGraphics(kittyGraphicsConfirmed{number: h.kittyNumber, id: 5})
	assert.Equal(t, uint32(5), h.kittyID)
}

// An abandoned transmission of OURS is freed, and the distinction from the test
// above is the number: one of ours that is no longer the newest.
func TestHandleKittyGraphics_FreesOurOwnSupersededImage(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h) // number 1
	h.closeImagePreview()
	openKittyPreview(t, h) // number 2
	require.Equal(t, 2, h.kittyOutstanding)

	_, cmd := h.handleKittyGraphics(kittyGraphicsConfirmed{number: 1, id: 111})
	assert.Contains(t, rawString(t, cmd), "i=111", "our own superseded image is freed")
	assert.Zero(t, h.kittyID)
}

// kittyWidthProbeVar marks the re-executed child of the probe below.
const kittyWidthProbeVar = "ATRIUM_TEST_KITTY_WIDTH_PROBE"

// The gate must MEASURE the placeholder's width, not assume it — and this is the
// only test that can tell the difference.
//
// Every other test here runs where placeholders already measure one cell, so a
// caller that hardcoded `true` would satisfy all of them; the mutation that does
// exactly that survived the whole suite. The rule being right is not the
// question — it was right and unreachable, which is how the pixel rung came to
// ship ungated on the predicate written for it.
//
// A subprocess, because go-runewidth and x/ansi both decide at package init:
// t.Setenv cannot reach a value read before the test started, and the go test
// cache cannot see the variable at all.
func TestOpenImagePreview_RefusesWhenPlaceholdersMeasureTwo(t *testing.T) {
	if os.Getenv(kittyWidthProbeVar) == "1" {
		h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
		h.windowWidth, h.windowHeight = 100, 30
		h.kittyEnviron = []string{"TERM=xterm-kitty", "COLORTERM=truecolor"}
		cmd := h.openImagePreview(overlay.Image{Path: "/fixture/shot.png", Pixels: parityImage()})
		// The picture still opens; only the pixel rung is refused.
		fmt.Printf("PROBE state=%v transmitted=%v\n", h.state == stateImagePreview, cmd != nil)
		return
	}
	child := exec.CommandContext(t.Context(), os.Args[0],
		"-test.run=^TestOpenImagePreview_RefusesWhenPlaceholdersMeasureTwo$")
	// Later entries win, per exec.Cmd.Env. RUNEWIDTH_EASTASIAN=1 is the one
	// environment both width libraries agree makes a placeholder two cells wide.
	child.Env = append(append(os.Environ(), kittyWidthProbeVar+"=1"),
		"RUNEWIDTH_EASTASIAN=1", "LC_ALL=C", "LC_CTYPE=C", "LANG=C")

	out, err := child.CombinedOutput()
	require.NoError(t, err, "probe failed: %s", out)
	assert.Contains(t, string(out), "PROBE state=true transmitted=false",
		"a placeholder that measures two cells must cost the pixel rung, not the picture")
}
