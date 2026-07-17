package keys

import (
	"maps"
	"testing"
)

// The dispatch table is derived from Registry, and a derived map has failure
// modes a hand-written literal cannot: a duplicate WithKeys string silently
// overwrites instead of failing to compile, and a Dispatch flag flipped on a
// documented-only entry silently grows the map. This golden pin freezes the
// full derived inventory so both mutations fail loudly. Two classes matter
// most: a Dispatch flip on esc/ctrl+l would route those keys into the
// busy-gate (app/app_update.go), swallowing them with a notice while an
// action is in flight; and a dropped or respelled dispatch string has no
// behavior test for most actions. #376 (remappable keys) retires this pin
// deliberately when the inventory stops being static.
func TestGlobalKeyStringsMap_GoldenInventory(t *testing.T) {
	want := map[string]KeyName{
		"up":         KeyUp,
		"k":          KeyUp,
		"down":       KeyDown,
		"j":          KeyDown,
		"shift+up":   KeyShiftUp,
		"shift+down": KeyShiftDown,
		"u":          KeyNextUnread,
		"b":          KeyNextNeedsInput,
		"J":          KeyMoveDown,
		"K":          KeyMoveUp,
		"{":          KeyMoveGroupUp,
		"}":          KeyMoveGroupDown,
		"[":          KeyMoveAccountUp,
		"]":          KeyMoveAccountDown,
		"left":       KeyCollapse,
		"right":      KeyExpand,
		"Z":          KeyCollapseAll,
		"N":          KeyPrompt,
		"enter":      KeyEnter,
		"o":          KeyEnter,
		"n":          KeyNew,
		"i":          KeySmartDispatch,
		"ctrl+x":     KeyKill,
		"R":          KeyRename,
		"A":          KeyAutoName,
		"s":          KeyQuickSend,
		"Q":          KeyQueue,
		"L":          KeyCmdLog,
		"y":          KeyCopyBranch,
		"q":          KeyQuit,
		"tab":        KeyTab,
		"shift+tab":  KeyShiftTab,
		"p":          KeyPause,
		"ctrl+p":     KeyPauseAll,
		"r":          KeyResume,
		"ctrl+r":     KeyResumeAll,
		"P":          KeySubmit,
		"c":          KeyCreate,
		"m":          KeyMerge,
		"w":          KeyOpenPR,
		"?":          KeyHelp,
		"/":          KeyFilter,
		"<":          KeyShrinkList,
		">":          KeyGrowList,
		"\\":         KeyLayoutPreset,
		"1":          KeyTabPreview,
		"2":          KeyTabDiff,
		"3":          KeyTabTerminal,
		",":          KeySettings,
		"@":          KeyAccounts,
		"ctrl+q":     KeyAttachToggle,
		"f":          KeyHints,
		"a":          KeyApprove,
		"v":          KeyMultiSelect,
		" ":          KeyToggleMark,
		"`":          KeyScreensaver,
	}
	if !maps.Equal(GlobalKeyStringsMap, want) {
		for s, name := range want {
			if got, ok := GlobalKeyStringsMap[s]; !ok {
				t.Errorf("dispatch map lost %q (want %v)", s, name)
			} else if got != name {
				t.Errorf("dispatch map remapped %q: got %v, want %v", s, got, name)
			}
		}
		for s, name := range GlobalKeyStringsMap {
			if _, ok := want[s]; !ok {
				t.Errorf("dispatch map gained %q → %v", s, name)
			}
		}
	}
}

// A hand-written map literal rejects a duplicate key at compile time; the
// derived dispatch map would just let the later entry win. This restores that
// check — across ALL of Registry, documented-only entries included, so a
// Dispatch:false entry cannot quietly claim a string some action dispatches.
func TestRegistry_NoDuplicateKeyStrings(t *testing.T) {
	claimed := map[string]KeyName{}
	for _, e := range Registry {
		for _, s := range e.Binding.Keys() {
			if prev, ok := claimed[s]; ok {
				t.Errorf("key string %q claimed by both %v and %v", s, prev, e.Name)
			}
			claimed[s] = e.Name
		}
	}
}

// The screensaver backtick is a deliberate easter egg: its absence from
// Registry is what keeps it out of GlobalKeyBindings and therefore out of
// every generated help surface (the cheatsheet coverage guard and the hint
// bar both walk that map). Its dispatch line is appended by hand in
// registry.go instead. app/help_coverage_test.go guards the rendered half.
func TestRegistry_OmitsScreensaver(t *testing.T) {
	for _, e := range Registry {
		if e.Name == KeyScreensaver {
			t.Fatal("KeyScreensaver must not appear in Registry — the easter egg's " +
				"exclusion from help is structural, not a flag")
		}
	}
	if _, ok := GlobalKeyBindings[KeyScreensaver]; ok {
		t.Error("KeyScreensaver must have no GlobalKeyBindings entry")
	}
}

// The reorder ladder is session → repo group → account cluster, and #357 was
// exactly this text drifting: "move account up" where the unit moved is a
// cluster. Exact matches on purpose — a Contains("account") check would have
// passed the original bug.
func TestRegistry_ReorderLadderVocabulary(t *testing.T) {
	want := map[KeyName]string{
		KeyMoveUp:          "move up",
		KeyMoveDown:        "move down",
		KeyMoveGroupUp:     "move group up",
		KeyMoveGroupDown:   "move group down",
		KeyMoveAccountUp:   "move account cluster up",
		KeyMoveAccountDown: "move account cluster down",
	}
	for name, desc := range want {
		if got := GlobalKeyBindings[name].Help().Desc; got != desc {
			t.Errorf("%v help desc: got %q, want %q", name, got, desc)
		}
	}
}

// Layer tags are what let generated help document layer-crossing keys
// truthfully: ctrl+q and ctrl+x are TUI actions the attach layer mirrors as
// raw bytes (session/tmux/attach.go), and ctrl+pgup/pgdn exists only in the
// attach layer (session/tmux/detach.go). Everything else is plain TUI
// dispatch. This test pins the layer tag on each entry; the matching prose
// rule — LayerBoth descs must state the attached side, since those rows get no
// "in a session: " prefix — is pinned by TestHelpGroups_LayerBothStateAttachedSide.
func TestRegistry_LayerTags(t *testing.T) {
	wantLayer := map[KeyName]Layer{
		KeyAttachToggle: LayerBoth,
		KeyKill:         LayerBoth,
		KeySessionCycle: LayerAttached,
	}
	for _, e := range Registry {
		want, special := wantLayer[e.Name]
		if !special {
			want = LayerTUI
		}
		if e.Layer != want {
			t.Errorf("%v layer: got %v, want %v", e.Name, e.Layer, want)
		}
		delete(wantLayer, e.Name)
	}
	for name := range wantLayer {
		t.Errorf("registry has no entry for %v", name)
	}
}

// The three documented-only entries exist so the cheatsheet can reference the
// keys it documents (esc, ctrl+l, ctrl+pgup/pgdn were previously hand-written
// prose that no map knew about). They must never enter dispatch: esc and
// ctrl+l are handled before the GlobalKeyStringsMap lookup, and routing them
// through it would put them behind the busy-gate.
func TestRegistry_DocumentedOnlyEntries(t *testing.T) {
	wantDocOnly := map[KeyName]bool{
		KeySessionCycle: true,
		KeyEscape:       true,
		KeyRedraw:       true,
	}
	for _, e := range Registry {
		if wantDocOnly[e.Name] {
			if !e.DocOnly {
				t.Errorf("%v must be DocOnly", e.Name)
			}
			delete(wantDocOnly, e.Name)
			continue
		}
		if e.DocOnly {
			t.Errorf("%v is DocOnly but not a known documented-only key", e.Name)
		}
	}
	for name := range wantDocOnly {
		t.Errorf("registry has no entry for documented-only %v", name)
	}
}
