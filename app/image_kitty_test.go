package app

import (
	"math"
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
	h.kittyEnviron = []string{"TERM=xterm-kitty"}
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
			assert.Equal(t, tc.want, kittyEligible(tc.environ, tc.pref, tc.mono))
		})
	}
}

// An empty TMUX is not something tmux sets, so it must not trip the veto.
func TestKittyEligible_EmptyTmuxIsNotTmux(t *testing.T) {
	assert.True(t, kittyEligible(
		[]string{"TERM=xterm-kitty", "TMUX="}, config.ImagePreviewAuto, false))
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
	h2.kittyEnviron = []string{"TERM=xterm-256color"}
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
	// upgrading anyway would dereference an overlay that is gone.
	t.Run("the overlay already closed", func(t *testing.T) {
		h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
		openKittyPreview(t, h)
		number := h.kittyNumber
		h.closeImagePreview()

		_, cmd := h.handleKittyGraphics(kittyGraphicsConfirmed{number: number, id: 7})
		assert.Zero(t, h.kittyID)
		assert.Nil(t, cmd)
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
	h.kittyEnviron = []string{"TERM=xterm-kitty"}

	restore := theme.SetMono(true)
	defer restore()
	cmd := h.openImagePreview(overlay.Image{Path: "/fixture/shot.png", Pixels: parityImage()})
	assert.Nil(t, cmd, "NO_COLOR strips the foreground the image ID rides in")
}
