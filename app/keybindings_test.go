package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	//
	// Apply(nil) IS the reset — it resolves an empty override set, which is the
	// default keymap — so its restore func is deliberately discarded. Calling it
	// would undo the reset and reinstall this test's override for every test that
	// ran afterwards. That is not a hypothetical tidiness point: it shipped, and
	// because the package's tests run in file order locally it was invisible until
	// CI's `-shuffle=on` put a draft test after this one, where pressing the
	// now-unbound "n" never opened the create form and the nil overlay panicked.
	t.Cleanup(theme.Set(config.DefaultConfig().Theme))
	t.Cleanup(func() {
		keys.Apply(nil)
		tmux.SetAttachChords(17, 24, true)
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

	detach, kill, killBound := tmux.AttachChords()
	assert.Equal(t, byte('g'&0x1f), detach, "the attach layer must detach on the rebound chord")
	assert.Equal(t, byte('x'&0x1f), kill, "and still kill on the unchanged one")
	assert.True(t, killBound)
}

// No section is the case almost every user is in, and it must leave the keymap
// exactly as shipped.
func TestConstruct_NoKeybindingsSectionLeavesTheDefaults(t *testing.T) {
	h := newHomeWithKeybindings(t, nil)

	assert.Empty(t, h.pendingKeybindingProblems)
	assert.Equal(t, keys.KeyNew, keys.GlobalKeyStringsMap["n"])
	assert.Equal(t, "n", keys.LabelOf(keys.KeyNew))
	detach, kill, killBound := tmux.AttachChords()
	assert.Equal(t, byte(17), detach)
	assert.Equal(t, byte(24), kill)
	assert.True(t, killBound)
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

// A refusal and a caveat are opposite outcomes, so the report cannot list them
// together: an applied-with-a-caveat override under a heading saying "not
// applied … those actions keep their default keys" tells the user their key is
// dead when it is live.
func TestKeybindingProblemsReport_SeparatesWarningsFromRefusals(t *testing.T) {
	report := keybindingProblemsReport([]keys.Problem{
		{Action: "pauze", Msg: "no such action"},
		{Action: "new", Msg: `key "x" is consumed by multi-select mode`, Warning: true},
	})

	assert.Contains(t, report, "1 keybinding in config.json was not applied:")
	assert.Contains(t, report, "1 keybinding was applied with a caveat:")

	refusedAt := strings.Index(report, "not applied")
	caveatAt := strings.Index(report, "applied with a caveat")
	defaultsAt := strings.Index(report, "keep their default keys")
	assert.Less(t, refusedAt, defaultsAt)
	assert.Less(t, defaultsAt, caveatAt,
		`"those actions keep their default keys" must belong to the refusals, not trail the caveats`)

	only := keybindingProblemsReport([]keys.Problem{
		{Action: "new", Msg: "shadowed", Warning: true},
	})
	assert.NotContains(t, only, "not applied",
		"a report with nothing refused must not claim anything was")
	assert.NotContains(t, only, "keep their default keys")
}

// An unbound kill has to reach the attach layer, or the user who removed the
// kill key can still destroy a session with it from inside the pane.
func TestConstruct_UnbindingKillDisarmsTheAttachLayer(t *testing.T) {
	h := newHomeWithKeybindings(t, map[string]config.KeySpec{
		"kill":          {Disabled: true},
		"attach_toggle": {Keys: []string{"ctrl+g"}},
	})
	require.Empty(t, h.pendingKeybindingProblems)

	detach, _, killBound := tmux.AttachChords()
	assert.False(t, killBound, "with kill unbound the attach layer must have no kill byte")
	assert.Equal(t, byte('g'&0x1f), detach,
		"and the detach rebind must still be installed — bailing on the kill lookup used to "+
			"skip it, leaving the pane detaching on ctrl+q while the list used ctrl+g")
}

// The reserved-esc refusal must survive clipReportLine whole: the modal keeps
// the HEAD of a report line, so an over-long reason silently drops its own
// tail (the "…and overlays" clause) while the keys package's substring test on
// the raw Error() keeps passing — the reason's length is only checkable here,
// beside the budget that clips it.
func TestReservedEscReasonFitsTheReportLine(t *testing.T) {
	problems := keys.Validate(map[string]keys.Spec{"new": {Keys: []string{"esc"}}})
	require.Len(t, problems, 1)
	line := problems[0].Error()
	require.Equal(t, line, clipReportLine(line),
		"the reserved-esc reason must render un-clipped in the keybindings modal")
	require.Contains(t, line, "overlay")
}
