package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeImage creates an empty file at root/rel and returns its absolute path.
// Resolution never opens the file, so its contents do not matter here — decoding
// is the load command's job and is tested separately.
func writeImage(t *testing.T, root, rel string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
	return p
}

func TestResolveImagePath(t *testing.T) {
	root := t.TempDir()
	rel := writeImage(t, root, "shots/a.png")
	nested := writeImage(t, root, "b.JPG")
	// Created on purpose: a refusal for a file that does not exist would pass
	// the extension case for the wrong reason.
	notImage := writeImage(t, root, "notes.md")
	require.FileExists(t, notImage)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "dir.png"), 0o755))

	// Somewhere an agent legitimately writes to that is not under the worktree.
	outside := t.TempDir()
	away := writeImage(t, outside, "screenshot.png")

	cases := []struct {
		name string
		text string
		want string // "" means refused
	}{
		{"relative under the worktree", "shots/a.png", rel},
		{"absolute under the worktree", rel, rel},
		{"uppercase extension", "b.JPG", nested},
		{"line suffix is stripped", "shots/a.png:12", rel},
		{"line and column suffix are stripped", "shots/a.png:12:4", rel},
		{"dot-slash prefix", "./shots/a.png", rel},
		// Accepted on purpose: /tmp is a normal place for an agent to leave a
		// screenshot, so there is no containment check to the worktree.
		{"absolute outside the worktree", away, away},
		{"non-image extension", "notes.md", ""},
		{"no extension at all", "shots/a", ""},
		{"missing file", "shots/gone.png", ""},
		{"a directory named like an image", "dir.png", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveImagePath(tc.text, root)
			if tc.want == "" {
				assert.False(t, ok, "expected refusal, got %q", got)
				return
			}
			require.True(t, ok, "expected %q to resolve", tc.text)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A tilde path resolves against the sandbox HOME, never against the worktree —
// joining "~/x.png" onto a worktree root would name a directory literally called
// "~" that nothing ever creates.
func TestResolveImagePath_ExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	want := writeImage(t, home, "pics/fig.png")

	got, ok := resolveImagePath("~/pics/fig.png", t.TempDir())
	require.True(t, ok)
	assert.Equal(t, want, got)
}

// With no worktree to resolve against — a direct or unstarted session — a
// relative name has no meaning and must be refused rather than resolved against
// whatever the process's working directory happens to be.
//
// The chdir is what gives this test teeth: the name has to be one that WOULD
// resolve from the cwd, or the refusal proves only that the file is missing.
func TestResolveImagePath_RelativeNeedsARoot(t *testing.T) {
	cwd := t.TempDir()
	writeImage(t, cwd, "shots/a.png")
	t.Chdir(cwd)

	_, ok := resolveImagePath("shots/a.png", "")
	assert.False(t, ok, "a relative name must not be resolved against Atrium's own cwd")

	// Control: the same name does resolve once there is a root to hang it on.
	_, ok = resolveImagePath("shots/a.png", cwd)
	assert.True(t, ok)
}

// A device node or FIFO named like an image is not something to decode. The
// regular-file check is what excludes them, and it is separate from existence.
func TestResolveImagePath_RefusesIrregularFiles(t *testing.T) {
	if _, err := os.Stat("/dev/null"); err != nil {
		t.Skip("no /dev/null")
	}
	root := t.TempDir()
	link := filepath.Join(root, "dev.png")
	require.NoError(t, os.Symlink("/dev/null", link))

	_, ok := resolveImagePath("dev.png", root)
	assert.False(t, ok, "a symlink to a device node is not a decodable image")
}
