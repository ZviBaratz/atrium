package app

import (
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/doctor"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi/kitty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ack, refusal and placementRefusal are the three answers a terminal gives, and
// every handler test builds its input through one of them.
//
// A struct literal per call site is what let twenty tests assert against a
// message no terminal sends: when the payload check landed, every one of them
// kept passing an implicit "this was an error" and none of them noticed. One
// constructor per reply shape means the next field can only be forgotten once —
// and TestKittyReply_DecodedFromRealTerminalBytes pins these against bytes
// captured off a real kitty and a real Ghostty, so "what a terminal sends" is
// measured rather than assumed. It has already caught one wrong assumption of
// mine; see the correction in that test.
func ack(number int, id uint32) kittyGraphicsConfirmed {
	return kittyGraphicsConfirmed{number: number, id: id, ok: true}
}

// refusal is a transmission the terminal REJECTED. The id is non-zero on
// purpose: kitty allocates one before it decodes, so `\e_Gi=2,I=22;EBADPNG` is
// what a bad payload really comes back as, and an id is therefore no evidence at
// all that an image exists.
func refusal(number int, id uint32) kittyGraphicsConfirmed {
	return kittyGraphicsConfirmed{number: number, id: id}
}

// placementRefusal is the answer to a rejected a=p. It carries p= and no I=,
// which is what makes it a hazard: on number alone it is indistinguishable from
// an untagged transmission reply, which the handler accepts.
func placementRefusal(id uint32) kittyGraphicsConfirmed {
	return kittyGraphicsConfirmed{id: id, placement: kittyPlacementID}
}

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

// doctor and the TUI must reach the same verdict for the same environment.
//
// They are two implementations of one rule, in two packages, and that is the
// defect this guards: the first cut of doctor replicated only the NO_COLOR veto,
// so it reported "eligible" to a user on kitty under an east-asian width rule or
// a downsampling profile — the exact person the section exists for, told the
// opposite of the truth about the exact surprise it exists to explain.
//
// The sweep is the whole cross-product rather than a few examples, because a
// hand-picked list is how three vetoes came to be one: what is missing from the
// implementation is missing from the examples too.
func TestDoctorAgreesWithTheTUI(t *testing.T) {
	environs := [][]string{
		{"TERM=xterm-kitty"},
		{"TERM=xterm-kitty", "TMUX=/tmp/s,1,0"},
		{"TERM=xterm-256color", "TERM_PROGRAM=ghostty", "COLORTERM=truecolor"},
		{"TERM=xterm-256color", "TERM_PROGRAM=ghostty"}, // no COLORTERM: not truecolor
		{"TERM=xterm-256color"},
		{"TERM=xterm-kitty", "NO_COLOR=1"},
		{"GHOSTTY_BIN_DIR=/usr/bin"},
		nil,
	}
	prefs := []string{
		config.ImagePreviewAuto, config.ImagePreviewKitty,
		config.ImagePreviewGlyph, config.ImagePreviewOff,
	}

	for _, env := range environs {
		for _, pref := range prefs {
			for _, oneCell := range []bool{true, false} {
				name := fmt.Sprintf("%v/%s/oneCell=%v", env, pref, oneCell)
				t.Run(name, func(t *testing.T) {
					// mono and trueColor are resolved from the same environment the
					// section reads, which is what production does — theme.Mono is set
					// from NO_COLOR at startup, and the profile is read off the env.
					tui := kittyEligible(env, pref,
						theme.NoColorRequested(env),
						colorprofile.Env(env) == colorprofile.TrueColor,
						oneCell)
					got := doctor.CheckImagePreview(env, pref, oneCell).Eligible

					assert.Equalf(t, tui, got,
						"doctor says %v where the TUI does %v", got, tui)
				})
			}
		}
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

	_, cmd := h.handleKittyGraphics(ack(h.kittyNumber, 0xAABBCC))
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

	h.handleKittyGraphics(ack(h.kittyNumber, 0xAABBCC))

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
		h.handleKittyGraphics(ack(h.kittyNumber, 42))

		// A second, unnumbered reply now belongs to some other program: there is
		// nothing outstanding for it to be an answer to.
		_, cmd := h.handleKittyGraphics(ack(0, 99))
		assert.Equal(t, uint32(42), h.kittyID, "an untagged reply must not retarget a settled image")
		assert.Nil(t, cmd)
	})

	t.Run("another program's number", func(t *testing.T) {
		h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
		openKittyPreview(t, h)

		_, cmd := h.handleKittyGraphics(ack(99, 7))
		assert.Zero(t, h.kittyID)
		assert.Nil(t, cmd)
		assert.NotContains(t, h.imageOverlay.Render(), string(kitty.Placeholder))
	})

	// An error reply after a success must not undo it. Driving it from the
	// un-upgraded state would prove nothing — kittyID is zero whether the reply
	// was refused or accepted as zero — so this runs AFTER a success, where the
	// two answers differ: refusing keeps the picture, accepting resets it.
	//
	// Both id shapes are exercised, and the second is the one that matters. An
	// earlier version of this test asserted only `id: 0` on the belief that "an
	// error reply carries no ID"; kitty 0.45 answers a bad payload with
	// `\e_Gi=2,I=22;EBADPNG`, so the id is real and names nothing. Under the
	// id-only rule that reply read as a confirmation.
	t.Run("an error reply after a success must not undo it", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			bad  kittyGraphicsConfirmed
		}{
			{"no id at all", refusal(2, 0)},
			{"an id kitty allocated and then rejected", refusal(2, 99)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
				openKittyPreview(t, h)
				h.handleKittyGraphics(ack(h.kittyNumber, 42))
				require.Equal(t, uint32(42), h.kittyID)

				// A second transmission, so the refusal has something to answer.
				h.transmitImageCmd(parityImage())
				_, cmd := h.handleKittyGraphics(tc.bad)

				assert.Equal(t, uint32(42), h.kittyID, "a refusal must not replace a confirmed image")
				assert.Nil(t, cmd, "and must not free an image the terminal never stored")
				assert.Contains(t, h.imageOverlay.Render(), string(kitty.Placeholder),
					"the picture must survive an error reply that arrives late")
			})
		}
	})

	// A reply that lands after the user pressed esc has nothing to upgrade — and
	// upgrading anyway would dereference an overlay that is gone. It is not simply
	// dropped, though: see TestHandleKittyGraphics_LateReplyStillFreesTheImage.
	t.Run("the overlay already closed", func(t *testing.T) {
		h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
		openKittyPreview(t, h)
		number := h.kittyNumber
		h.closeImagePreview()

		_, cmd := h.handleKittyGraphics(ack(number, 7))
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
	h.handleKittyGraphics(ack(h.kittyNumber, 42))

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
	h.handleKittyGraphics(ack(h.kittyNumber, 42))

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
		Payload: []byte("OK"),
	})
	assert.Equal(t, ack(3, 987654), got)

	// The largest ID the protocol allows must still survive; a bound that
	// rejected it would refuse a legitimate reply.
	got = kittyGraphicsEventFrom(uv.KittyGraphicsEvent{
		Options: kitty.Options{ID: math.MaxUint32, Number: 1},
		Payload: []byte("OK"),
	})
	assert.Equal(t, uint32(math.MaxUint32), got.id)
}

// The payload is the ONLY field that says whether the request succeeded, so the
// converter's reading of it is worth its own guard. Every failure the protocol
// defines is a named code, and none of them may pass for an acknowledgement.
func TestKittyGraphicsEventFrom_ReadsTheOutcomeFromThePayload(t *testing.T) {
	outcome := func(payload string) bool {
		return kittyGraphicsEventFrom(uv.KittyGraphicsEvent{
			Options: kitty.Options{ID: 1, Number: 1},
			Payload: []byte(payload),
		}).ok
	}

	assert.True(t, outcome("OK"))
	assert.True(t, outcome("OK\r\n"), "a terminal may terminate the line")

	for _, bad := range []string{
		"EBADPNG:Not a PNG file",
		"ENOENT:Put command refers to non-existent image with id: 987654 and number: 0",
		"EINVAL:Image too large, width or height greater than 10000",
		"ENOMEM:Out of memory",
		"", // no payload at all is not a confirmation either
	} {
		assert.Falsef(t, outcome(bad), "%q is a refusal, not an acknowledgement", bad)
	}
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

	h.Update(uv.KittyGraphicsEvent{Options: kitty.Options{ID: 5, Number: h.kittyNumber}, Payload: []byte("OK")})
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

// The handler must accept a reply whether or not it echoes the image number.
//
// Both measured terminals DO echo it (see the correction in
// TestKittyReply_DecodedFromRealTerminalBytes, which is where that was got
// wrong). The untagged row stays because the echo is a courtesy no client can
// require, and refusing one would cost such a terminal the rung in silence.
//
// The large ID is Ghostty's real one, and it is over 24 bits — the only case
// that exercises the third diacritic in production, which is why it is the real
// value rather than a round number.
func TestHandleKittyGraphics_AcceptsEitherReplyShape(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply func(number int) kittyGraphicsConfirmed
		want  uint32
	}{
		{
			name:  "kitty echoes the image number",
			reply: func(n int) kittyGraphicsConfirmed { return ack(n, 1) },
			want:  1,
		},
		{
			// Not a shape either terminal was measured sending; accepted so a
			// terminal that does not echo I= keeps the rung.
			name:  "a terminal that echoes no number",
			reply: func(int) kittyGraphicsConfirmed { return ack(0, 2147483647) },
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

	_, cmd := h.handleKittyGraphics(ack(0, 7))
	assert.Zero(t, h.kittyID, "a reply to a transmission we never sent is not ours")
	assert.Nil(t, cmd)
}

// A rejected transmission must not become a picture.
//
// This is the defect an id-only rule shipped: kitty answers a bad payload with
// `\e_Gi=2,I=22;EBADPNG`, so the reply names an image the terminal does not
// hold. Upgrading on it swaps a correct glyph picture for cells addressing
// nothing, and because SetKittyImage has no failure path the box stays blank for
// the rest of its life — strictly worse than not having tried.
func TestHandleKittyGraphics_ARefusedTransmissionKeepsTheGlyphs(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)
	glyphs := h.imageOverlay.Render()

	_, cmd := h.handleKittyGraphics(refusal(h.kittyNumber, 2))

	assert.Zero(t, h.kittyID, "an id on a refusal names no image the terminal holds")
	assert.Nil(t, cmd, "and freeing it would delete whatever else now answers to 2")
	assert.NotContains(t, h.imageOverlay.Render(), string(kitty.Placeholder))
	assert.Equal(t, glyphs, h.imageOverlay.Render(), "the picture is untouched")
}

// A refusal still ANSWERS the transmission it names, so the outstanding count
// has to come down with it. Leaving it up strands the count above zero forever,
// which permanently disqualifies every untagged reply after it, and a terminal
// that does not echo the number would lose the rung for the rest of the session.
func TestHandleKittyGraphics_ARefusalStillClosesItsTransmission(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)
	require.Equal(t, 1, h.kittyOutstanding)

	h.handleKittyGraphics(refusal(h.kittyNumber, 2))
	assert.Zero(t, h.kittyOutstanding, "the transmission was answered, unsuccessfully")

	// And the next picture is reachable: an untagged reply is accepted again.
	h.transmitImageCmd(parityImage())
	h.handleKittyGraphics(ack(0, 55))
	assert.Equal(t, uint32(55), h.kittyID)
}

// A refused PLACEMENT drops the picture back to glyphs.
//
// q=1 suppresses the OK and leaves the error, so a placement that works says
// nothing and one that fails answers — the opposite of the shape the rest of
// this file deals in. Without the fallback the cells go on addressing a
// placement that does not exist and the terminal draws nothing for them, from
// the one code path that has a working picture in hand.
func TestHandleKittyPlacement_RefusalFallsBackToGlyphs(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)
	h.handleKittyGraphics(ack(h.kittyNumber, 42))
	require.Contains(t, h.imageOverlay.Render(), string(kitty.Placeholder))

	_, cmd := h.handleKittyGraphics(placementRefusal(42))

	assert.NotContains(t, h.imageOverlay.Render(), string(kitty.Placeholder),
		"cells addressing a placement the terminal refused draw nothing at all")
	assert.Nil(t, cmd)
	assert.Equal(t, uint32(42), h.kittyID,
		"the image is still stored terminal-side; only closing frees it")
	assert.Contains(t, rawString(t, h.closeImagePreview()), "i=42",
		"and closing must still free it")
}

// The hazard that makes p= worth reading at all: a placement error carries no
// I=, so on number alone it IS the untagged shape. Counted as one, it
// closes the window on the transmission actually in flight and retargets the
// open overlay onto an image from the picture before it.
func TestHandleKittyPlacement_RefusalIsNotATransmissionReply(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)
	h.handleKittyGraphics(ack(h.kittyNumber, 42))

	// The user moves on. A new picture is in flight when the old placement's
	// refusal finally arrives.
	h.closeImagePreview()
	h2 := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h2)
	h2.kittyNumber, h2.kittyOutstanding = 2, 1

	_, cmd := h2.handleKittyGraphics(placementRefusal(42))

	assert.Equal(t, 1, h2.kittyOutstanding, "a placement's answer closes no transmission")
	assert.Zero(t, h2.kittyID, "and cannot hand this overlay the previous picture's image")
	assert.Nil(t, cmd)

	// The real reply is still accepted afterwards.
	h2.handleKittyGraphics(ack(2, 77))
	assert.Equal(t, uint32(77), h2.kittyID)
}

// A placement refusal naming somebody else's image must not touch our picture.
func TestHandleKittyPlacement_RefusalForAForeignImageIsIgnored(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)
	h.handleKittyGraphics(ack(h.kittyNumber, 42))

	h.handleKittyGraphics(placementRefusal(43))

	assert.Contains(t, h.imageOverlay.Render(), string(kitty.Placeholder),
		"another program's placement failing is not our picture's problem")
}

// An encode that fails must not advance the transmission number.
//
// The number and the outstanding count define the unanswered window
// [number-outstanding+1, number]. Advancing one without the other slides that
// window off the reply still in flight: it reads as another program's, so it is
// neither counted nor freed — leaking the image — and the count never returns to
// zero, which disqualifies every untagged reply for the rest of the session.
func TestTransmitImageCmd_AFailedEncodeCostsNoNumber(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)
	require.Equal(t, 1, h.kittyNumber)
	require.Equal(t, 1, h.kittyOutstanding)

	// A zero-sized image is one TransmitPNG refuses.
	assert.Nil(t, h.transmitImageCmd(image.NewRGBA(image.Rect(0, 0, 0, 0))))
	assert.Equal(t, 1, h.kittyNumber, "a transmission that never went out consumed no number")
	assert.Equal(t, 1, h.kittyOutstanding)

	// The reply to the transmission that DID go out is still inside the window.
	_, cmd := h.handleKittyGraphics(ack(1, 7))
	assert.Equal(t, uint32(7), h.kittyID)
	assert.NotNil(t, cmd)
}

// The bytes each terminal really sends, decoded by the parser that really
// decodes them.
//
// Everything else in this file builds the message through ack/refusal/
// placementRefusal, which tests Atrium's half of the contract and assumes the
// constructors describe a real terminal. That assumption is exactly where the
// Ghostty defect lived, and where the id-means-success defect lived after it —
// so this is the test that pins the constructors to captured bytes. Every string
// below came off a real terminal, by sending it Atrium's own escapes and reading
// the tty; the failures were provoked with a deliberately corrupt payload and a
// placement of an image that was never transmitted.
//
// The two refusals are the load-bearing rows. Both carry a NON-ZERO i=, which is
// why "the reply names an image" cannot mean "the terminal has one".
func TestKittyReply_DecodedFromRealTerminalBytes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes string
		want  kittyGraphicsConfirmed
	}{
		{"kitty 0.45.0 accepts", "\x1b_Gi=1,I=11;OK\x1b\\", ack(11, 1)},
		{"ghostty 1.3.0 accepts", "\x1b_Gi=2147483647,I=11;OK\x1b\\", ack(11, 2147483647)},
		{
			"kitty 0.45.0 rejects a bad payload — with an id that names nothing",
			"\x1b_Gi=2,I=22;EBADPNG:Not a PNG file\x1b\\",
			refusal(22, 2),
		},
		{
			// Ghostty reports the same failure with NO i= at all, which is why the
			// id==0 test cannot be the only refusal check and the payload has to be.
			"ghostty 1.3.0 rejects a bad payload, with no id",
			"\x1b_GI=22;EINVAL: invalid data\x1b\\",
			refusal(22, 0),
		},
		{
			"kitty 0.45.0 rejects a placement, despite q=1",
			"\x1b_Gi=987654,p=1;ENOENT:Put command refers to non-existent image with id: 987654 and number: 0\x1b\\",
			placementRefusal(987654),
		},
		{
			"ghostty 1.3.0 rejects a placement too",
			"\x1b_Gi=987654,p=1;ENOENT: image not found\x1b\\",
			placementRefusal(987654),
		},
		{
			// Not a shape either terminal was seen to send; accepted on purpose so a
			// terminal that does not echo I= keeps the rung. See TestUpdate_Accepts-
			// AnUntaggedReply.
			"a terminal that echoes no number",
			"\x1b_Gi=2147483647;OK\x1b\\",
			ack(0, 2147483647),
		},
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

// An untagged reply must survive the whole Update path, not just the converter.
//
// No terminal measured sends this shape (see TestKittyReply_DecodedFromReal-
// TerminalBytes for the correction), but the handler accepts it deliberately: a
// terminal that does not echo I= would otherwise lose the rung silently, and
// this is the half of the path where that would happen without a converter test
// noticing.
func TestUpdate_AcceptsAnUntaggedReply(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h)

	var dec uv.EventDecoder
	_, ev := dec.Decode([]byte("\x1b_Gi=2147483647;OK\x1b\\"))
	h.Update(ev)

	assert.Equal(t, uint32(2147483647), h.kittyID,
		"requiring the echo would cost a non-echoing terminal the rung, silently")
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
// It only bites for a reply carrying no image number — with one, the mismatch
// rejects it outright. That is why this drives the untagged shape.
func TestHandleKittyGraphics_StaleReplyCannotRetargetTheNextImage(t *testing.T) {
	h := newHintsHome(t, newBranchInstance(t, "a", "b1"))
	openKittyPreview(t, h) // image A
	h.closeImagePreview()  // esc before the terminal answers
	openKittyPreview(t, h) // image B, still awaiting its own reply

	// A's answer, arriving late and untagged.
	_, cmd := h.handleKittyGraphics(ack(0, 111))

	assert.Zero(t, h.kittyID, "a reply for the previous image must not name this one")
	assert.NotContains(t, h.imageOverlay.Render(), string(kitty.Placeholder))
	// The abandoned image is still sitting in the terminal, so it is freed rather
	// than forgotten.
	assert.Contains(t, rawString(t, cmd), "i=111")

	// And B's own reply still works, so the guard rejects the stale one without
	// closing the door on the real one.
	h.handleKittyGraphics(ack(0, 222))
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

	_, cmd := h.handleKittyGraphics(ack(number, 77))

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

	_, cmd := h.handleKittyGraphics(ack(99, 7))

	assert.Nil(t, cmd, "another program's image must not be freed by us")
	assert.Equal(t, before, h.kittyOutstanding, "and it does not answer our transmission")

	// Our own reply still lands afterwards, so the guard did not consume it.
	h.handleKittyGraphics(ack(h.kittyNumber, 5))
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

	_, cmd := h.handleKittyGraphics(ack(1, 111))
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
