package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/session/tmux"
	"github.com/ZviBaratz/atrium/ui/theme"

	xansi "github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHomeWithKeybindings builds a home from a config.json holding the given
// keybindings section, through the real load path — the point being that this
// exercises the wire from the file to the keymap rather than assuming it.
func newHomeWithKeybindings(t *testing.T, section map[string]config.KeySpec) *home {
	t.Helper()
	// newHome mutates process-global state: the theme, the keymap and the attach
	// layer's chords. Put all three back.
	t.Cleanup(theme.Set(config.DefaultConfig().Theme))
	t.Cleanup(func() {
		_, restore := keys.Apply(nil)
		restore()
		tmux.SetAttachChords(17, 24)
	})

	t.Setenv("HOME", t.TempDir())
	cfgDir, err := config.GetConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(cfgDir, 0o755))

	cfg := config.DefaultConfig()
	cfg.Keybindings = section
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfgDir, config.ConfigFileName), raw, 0o644))

	h, err := newHome(context.Background(), "claude", false, "v", "atr")
	require.NoError(t, err)
	return h
}

// The end-to-end claim: a keybindings section in config.json reaches the keymap.
// Everything either side of this is tested on its own — the wire format in
// config, the resolution rules in keys — and this is the wire between them.
func TestConstruct_AppliesTheConfiguredKeybindings(t *testing.T) {
	h := newHomeWithKeybindings(t, map[string]config.KeySpec{
		"new":       {Keys: []string{"ctrl+n"}},
		"pauze_all": {Keys: []string{"ctrl+j"}}, // not an action
	})

	assert.Equal(t, keys.KeyNew, keys.GlobalKeyStringsMap["ctrl+n"],
		"the good override must reach the dispatch map")
	assert.NotContains(t, keys.GlobalKeyStringsMap, "n",
		"and must replace the default rather than adding to it")
	assert.Equal(t, "ctrl-n", keys.LabelOf(keys.KeyNew),
		"and the label every surface renders must move with it")

	require.Len(t, h.pendingKeybindingProblems, 1,
		"the bad one must be buffered for the startup modal, not dropped silently")
	assert.Contains(t, h.pendingKeybindingProblems[0].Error(), "no such action")
}

// A rebound detach has to reach the attach layer too, or #376 is fixed on the
// list and still broken inside the pane the user is actually stuck in.
func TestConstruct_InstallsTheAttachChords(t *testing.T) {
	h := newHomeWithKeybindings(t, map[string]config.KeySpec{
		"attach_toggle": {Keys: []string{"ctrl+g"}},
	})
	require.Empty(t, h.pendingKeybindingProblems)

	detach, kill := tmux.AttachChords()
	assert.Equal(t, byte('g'&0x1f), detach, "the attach layer must detach on the rebound chord")
	assert.Equal(t, byte('x'&0x1f), kill, "and still kill on the unchanged one")
}

// No section is the case almost every user is in, and it must leave the keymap
// exactly as shipped.
func TestConstruct_NoKeybindingsSectionLeavesTheDefaults(t *testing.T) {
	h := newHomeWithKeybindings(t, nil)

	assert.Empty(t, h.pendingKeybindingProblems)
	assert.Equal(t, keys.KeyNew, keys.GlobalKeyStringsMap["n"])
	assert.Equal(t, "n", keys.LabelOf(keys.KeyNew))
	detach, kill := tmux.AttachChords()
	assert.Equal(t, byte(17), detach)
	assert.Equal(t, byte(24), kill)
}

// The report's shape, and the consequence line that distinguishes it from the
// other two: a dropped override looks exactly like a config that was not read.
func TestKeybindingProblemsReport(t *testing.T) {
	report := keybindingProblemsReport([]keys.Problem{{Action: "new", Msg: "no such action"}})

	assert.Contains(t, report, "1 keybinding in config.json was not applied:")
	assert.Contains(t, report, `keybindings["new"]: no such action`)
	assert.Contains(t, report, "keep their default keys")
	assert.NotContains(t, report, "… and")
}

// Mirrors flushCustomCommandProblems: a modal opened while an overlay owns the
// screen would clobber it, and a buffer that is only read reopens the modal on
// every 100ms tick.
func TestKeybindingProblemsFlushWaitsForTheScreen(t *testing.T) {
	h, _ := newUnreadHome(t)
	h.pendingKeybindingProblems = []keys.Problem{{Action: "new", Msg: "no such action"}}

	h.state = stateHelp
	assert.Nil(t, h.flushKeybindingProblems(), "it must wait while an overlay owns the screen")
	assert.NotEmpty(t, h.pendingKeybindingProblems, "and stay buffered")

	h.state = stateDefault
	h.flushKeybindingProblems()
	assert.Equal(t, stateInfo, h.state, "then open the persistent modal")
	assert.Contains(t, xansi.Strip(h.textOverlay.Render()), "no such action")
	assert.Empty(t, h.pendingKeybindingProblems,
		"and clear the buffer, or the preview tick reopens it forever")

	h.state = stateDefault
	assert.Nil(t, h.flushKeybindingProblems(), "a second tick must find nothing to do")
}
