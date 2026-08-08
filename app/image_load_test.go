package app

// The fixtures under testdata/images are committed rather than encoded here, and
// that is the whole point of them.
//
// Go registers an image decoder per BINARY, not per file. A test that built its
// own fixtures with image/png would register those decoders for every test in
// the package — so loadImage would keep decoding after someone deleted the blank
// import it depends on, and the format coverage below would prove nothing.
// Measured: with the encoders imported here, deleting the production
// `_ "image/gif"` left the suite green.
//
// Only gif is provable even so, and it is worth knowing why. Two of the three
// decoders are already registered by things that have nothing to do with this
// feature — `image/png` by github.com/charmbracelet/x/ansi/kitty, `image/jpeg`
// by golang.org/x/crypto/openpgp/packet (via go-selfupdate) — so deleting their
// blank imports changes nothing today. They stay because that is an accident of
// two unrelated dependency trees, not a property of this package: the day either
// dependency drops its import, the explicit ones are what keeps decoding working.
//
// Regenerate with a throwaway program if a format is ever added; they are solid
// red and a few hundred bytes each.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture copies testdata/images/name into a temp dir, so a test can name a
// path that is not inside the repo.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "images", name))
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, b, 0o644))
	return p
}

func TestLoadImage_DecodesAndShrinks(t *testing.T) {
	got, w, h, err := loadImage(fixture(t, "wide.png")) // 2048×1024
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, imageIntermediateMax, got.Bounds().Dx(), "shrunk to the intermediate bound")
	assert.Equal(t, imageIntermediateMax/2, got.Bounds().Dy(), "aspect preserved")
	// The reported size is the FILE's, and it deliberately differs from the
	// intermediate's here — that difference is the whole point of returning it.
	assert.Equal(t, 2048, w, "reported width must be the file's, not the intermediate's")
	assert.Equal(t, 1024, h)
}

// A GIF is captioned by its LOGICAL SCREEN size, not by frame 0's rectangle.
//
// The two disagree only for GIF, and only when the first frame is a
// subrectangle — which is why the ordinary tiny.gif fixture cannot see this:
// its frame fills its screen, so both readings give the same number and the
// assertion would pass either way. offset-frame.gif is a 100×80 screen whose
// first frame is (10,5)-(50,35); reading the decoded bounds captioned it 40×30.
func TestLoadImage_GIFReportsItsLogicalScreenSize(t *testing.T) {
	_, w, h, err := loadImage(fixture(t, "offset-frame.gif"))
	require.NoError(t, err)
	assert.Equal(t, 100, w, "a GIF's size is its logical screen, not frame 0's rect")
	assert.Equal(t, 80, h)
}

// Every extension in imageExts must actually decode. The allowlist and the blank
// imports in image_load.go are two separate lists and only one of them makes
// decoding work; nothing but this test connects them.
func TestLoadImage_DecodesEveryAllowedFormat(t *testing.T) {
	byExt := map[string]string{".png": "tiny.png", ".jpg": "tiny.jpg", ".jpeg": "tiny.jpg", ".gif": "tiny.gif"}
	for ext := range imageExts {
		name, ok := byExt[ext]
		require.True(t, ok, "imageExts gained %q with no fixture — add one", ext)
		got, _, _, err := loadImage(fixture(t, name))
		assert.NoError(t, err, "decoding %s", name)
		assert.NotNil(t, got, "decoding %s", name)
	}
}

// A file named like an image but holding something else must be reported, not
// panicked on and not shown as an empty box.
func TestLoadImage_RejectsNonImageContent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fake.png")
	require.NoError(t, os.WriteFile(p, []byte("this is not a png"), 0o644))

	_, _, _, err := loadImage(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fake.png")
}

func TestLoadImage_MissingFile(t *testing.T) {
	_, _, _, err := loadImage(filepath.Join(t.TempDir(), "gone.png"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gone.png")
}

// The command wraps the load rather than performing it, so the decode never runs
// on the Bubble Tea goroutine. Its message carries the path back either way, so
// a late result can be matched to what was asked for.
func TestLoadImageCmd_CarriesPathAndResult(t *testing.T) {
	p := fixture(t, "tiny.png")

	msg, ok := loadImageCmd(p)().(imageLoadedMsg)
	require.True(t, ok)
	assert.Equal(t, p, msg.path)
	assert.NoError(t, msg.err)
	assert.NotNil(t, msg.img)

	missing := filepath.Join(t.TempDir(), "nope.png")
	msg, ok = loadImageCmd(missing)().(imageLoadedMsg)
	require.True(t, ok)
	assert.Equal(t, missing, msg.path)
	assert.Error(t, msg.err)
	assert.Nil(t, msg.img)
}

// The budget is asserted directly because the inputs that trip it cannot be
// built as fixtures: a 50-megapixel image is 200 MB of RGBA, and a 24 MB file
// takes longer to write than the rest of this package takes to test. Going
// through a real file could therefore only ever cover the cases that pass.
func TestCheckImageBudget(t *testing.T) {
	cases := []struct {
		name    string
		size    int64
		w, h    int
		wantErr string
	}{
		{"ordinary screenshot", 2 << 20, 1920, 1080, ""},
		{"exactly at the byte cap", maxImageFileBytes, 100, 100, ""},
		{"one byte over the byte cap", maxImageFileBytes + 1, 100, 100, "too big to preview"},
		{"exactly at the pixel cap", 1024, maxImagePixels / 1000, 1000, ""},
		{"over the pixel cap", 1024, maxImagePixels/1000 + 1, 1000, "too big to preview"},
		{"zero width", 1024, 0, 100, "no pixels"},
		{"zero height", 1024, 100, 0, "no pixels"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkImageBudget("shot.png", tc.size, tc.w, tc.h)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), "shot.png", "the message must name the file")
		})
	}
}

// The refusal has to say what the user can act on. A message that only says
// "too big" leaves them unable to tell whether it was the file or the canvas,
// and the two have different fixes.
func TestCheckImageBudget_NamesTheNumber(t *testing.T) {
	err := checkImageBudget("shot.png", 40<<20, 100, 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "40 MB")

	err = checkImageBudget("shot.png", 1024, 90000, 90000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "90000×90000")
}
