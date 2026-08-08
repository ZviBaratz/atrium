package app

// image_paths.go turns a hint match into an image file on disk.
//
// The hints package is deliberately pure — no filesystem, no session import
// (hints/assign.go) — and Match carries the raw on-screen substring, nothing
// more. So the question "is this text a picture I can show?" is answered here,
// where the selected *session.Instance, and therefore the worktree to resolve
// relative names against, is in hand.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// imageExts are the extensions this can decode, which is exactly what the
// standard library registers: image/png, image/jpeg and image/gif. webp and bmp
// would each need golang.org/x/image, and are left out until someone asks.
var imageExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
}

// lineSuffix matches the :line or :line:column a compiler or a grep appends to a
// filename. The image-path pattern in hints already stops before one, but the
// path pattern can still win a line (a diff header, a git-status capture), so
// this is retried rather than assumed away.
var lineSuffix = regexp.MustCompile(`:\d+(:\d+)?$`)

// resolveImagePath reports the absolute path of the image text names, resolving
// relative names against root.
//
// root is the selected session's worktree. An empty root means there is nothing
// to resolve against — a direct or unstarted session — and a relative name is
// then refused rather than silently resolved against the process's working
// directory, which is Atrium's own and never what the agent meant.
//
// Refusal is the normal outcome: most path matches are source files. The caller
// falls back to plain copy, so this must not report an error, only a no.
func resolveImagePath(text, root string) (string, bool) {
	for _, candidate := range []string{text, lineSuffix.ReplaceAllString(text, "")} {
		if p, ok := resolveOne(candidate, root); ok {
			return p, true
		}
	}
	return "", false
}

func resolveOne(text, root string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || !imageExts[strings.ToLower(filepath.Ext(text))] {
		return "", false
	}

	p := expandTilde(text)
	if !filepath.IsAbs(p) {
		if root == "" {
			return "", false
		}
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)

	// Stat, not Lstat: a symlink to a real image is a real image. What this
	// rejects is a target that is not a regular file — a directory that happens
	// to end in .png, and the device nodes and FIFOs that would otherwise be
	// handed to a decoder.
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	return p, true
}

// expandTilde resolves a leading ~ against the user's home directory.
func expandTilde(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}
