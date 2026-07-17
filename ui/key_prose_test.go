package ui

import (
	"testing"

	"github.com/ZviBaratz/atrium/keys"
	"github.com/stretchr/testify/require"
)

// A few surfaces name keys in prose rather than through a binding lookup: the
// splash and paused-fallback text in preview.go ('n' at the splash, 'r' and
// 'y' in the paused block) and the empty-list hint in list_render.go (n, ?).
// Free prose can't be generated, but its keys can be pinned: if one of these
// strings is ever remapped in the registry, the prose is lying and this fails,
// naming the site to fix. (The prose text itself is pinned verbatim by the
// preview and list tests; this ties the other end — the key — to the
// registry.)
func TestProseNamedKeys_ExistInRegistry(t *testing.T) {
	for _, pin := range []struct {
		key  string
		want keys.KeyName
		site string
	}{
		{"n", keys.KeyNew, "preview.go splash text; list_render.go empty-list hint"},
		{"r", keys.KeyResume, "preview.go paused-fallback text"},
		{"y", keys.KeyCopyBranch, "preview.go paused-fallback copy hint"},
		{"?", keys.KeyHelp, "list_render.go empty-list hint"},
	} {
		got, ok := keys.GlobalKeyStringsMap[pin.key]
		require.Truef(t, ok, "%q is named in prose (%s) but no longer dispatches", pin.key, pin.site)
		require.Equalf(t, pin.want, got, "%q is named in prose (%s) but now dispatches %v", pin.key, pin.site, got)
	}
}
