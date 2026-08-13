package keys

import (
	"fmt"
	"maps"
	"testing"
)

// effectLabel names a KeyName for a failure message. KeyName is a bare int, so
// %v alone reports "entry 0" — the help desc is the identifier a reader can find
// in the registry, which is the trick TestRegistry_OmitsScreensaver uses too.
func effectLabel(name KeyName) string {
	for _, e := range Registry {
		if e.Name == name {
			return e.Binding.Help().Desc
		}
	}
	return fmt.Sprintf("unregistered KeyName(%d)", int(name))
}

// TestEveryRegistryEntryDeclaresAnEffect is the guard the invalid zero value
// exists for. keyAllowedWhileBusy states a rule about every key in the registry
// and nothing checked it, so the fix cannot be a list someone must remember to
// extend: a new Entry that says nothing about what it does has to FAIL, not
// default into the permissive answer.
//
// DocOnly entries included. Nothing gates them today, but exhaustiveness is the
// whole mechanism — an entry exempted from classification is one whose
// reclassification the golden below would never show.
func TestEveryRegistryEntryDeclaresAnEffect(t *testing.T) {
	for _, e := range Registry {
		if e.Effect == EffectUnset {
			t.Errorf("registry entry %q declares no Effect. Say what pressing it can "+
				"change: EffectObserve (reads only), EffectView (persists view state "+
				"only), or EffectMutate (a session, repo, config, or agent). See the "+
				"Effect doc comment — the zero value is invalid on purpose",
				e.Binding.Help().Desc)
		}
	}
}

// TestKeyEffects_Golden pins the whole classification, so flipping a key's effect
// is a reviewable one-line diff rather than a silent regression. Same reasoning as
// TestGlobalKeyStringsMap_GoldenInventory, and the same three-loop report, because
// the three ways this can drift want different messages: a key that lost its
// entry, one that gained one, and — the one that matters — one whose effect
// changed under a refactor nobody read as a permissions change.
//
// The classification rules behind the values are on the Effect type. Two worth
// finding here rather than deriving: "approve" is a mutation because it authorizes
// the agent, and "command palette" is the one surface-opener that does not inherit
// what its rows can do, because those rows are themselves classified here.
func TestKeyEffects_Golden(t *testing.T) {
	want := map[KeyName]Effect{
		// Observing: reads only. No disk, no fleet, no repo, no config, no agent.
		KeyUp:             EffectObserve,
		KeyDown:           EffectObserve,
		KeyShiftUp:        EffectObserve,
		KeyShiftDown:      EffectObserve,
		KeyNextUnread:     EffectObserve,
		KeyNextNeedsInput: EffectObserve,
		KeyTab:            EffectObserve,
		KeyShiftTab:       EffectObserve,
		KeyTabPreview:     EffectObserve,
		KeyTabDiff:        EffectObserve,
		KeyTabTerminal:    EffectObserve,
		KeyHelp:           EffectObserve,
		KeyFilter:         EffectObserve,
		KeyToggleMark:     EffectObserve,
		KeyCopyBranch:     EffectObserve,
		KeyCopyContent:    EffectObserve,
		KeyHints:          EffectObserve,
		KeyOpenPR:         EffectObserve,
		KeyCmdLog:         EffectObserve,
		KeyCommandPalette: EffectObserve,
		KeySessionCycle:   EffectObserve,
		KeyEscape:         EffectObserve,
		KeyRedraw:         EffectObserve,

		// View-only: writes state.json, and what it writes is the arrangement of
		// the view. The fold/split/preset half is what the busy-gate admits; the
		// reorder half is the rest of the same category.
		KeyCollapse:        EffectView,
		KeyExpand:          EffectView,
		KeyCollapseAll:     EffectView,
		KeyShrinkList:      EffectView,
		KeyGrowList:        EffectView,
		KeyLayoutPreset:    EffectView,
		KeyMoveUp:          EffectView,
		KeyMoveDown:        EffectView,
		KeyMoveGroupUp:     EffectView,
		KeyMoveGroupDown:   EffectView,
		KeyMoveAccountUp:   EffectView,
		KeyMoveAccountDown: EffectView,

		// Mutating: a session, a repo, the config, or an agent.
		KeyNew:            EffectMutate,
		KeyPrompt:         EffectMutate,
		KeySmartDispatch:  EffectMutate,
		KeyKill:           EffectMutate,
		KeyUndoKill:       EffectMutate,
		KeyPause:          EffectMutate,
		KeyPauseAll:       EffectMutate,
		KeyResume:         EffectMutate,
		KeyResumeAll:      EffectMutate,
		KeyRename:         EffectMutate,
		KeyAutoName:       EffectMutate,
		KeyMute:           EffectMutate,
		KeyQuickSend:      EffectMutate,
		KeyDiffComment:    EffectMutate,
		KeyQueue:          EffectMutate,
		KeySubmit:         EffectMutate,
		KeyCreate:         EffectMutate,
		KeyMerge:          EffectMutate,
		KeySettings:       EffectMutate,
		KeyAccounts:       EffectMutate,
		KeyCustomCommands: EffectMutate,
		KeyMultiSelect:    EffectMutate,
		KeyCheckpoints:    EffectMutate,
		KeyRunCommand:     EffectMutate,
		KeyApprove:        EffectMutate,
		KeyEnter:          EffectMutate,
		KeyAttachToggle:   EffectMutate,
		KeyQuit:           EffectMutate,
	}
	if maps.Equal(effects, want) {
		return
	}
	for name, effect := range want {
		got, ok := effects[name]
		switch {
		case !ok:
			t.Errorf("registry lost %s (was %s)", effectLabel(name), effect)
		case got != effect:
			t.Errorf("%s reclassified: got %s, want %s. If that is deliberate, say so "+
				"in the PR — this is a permissions change, and the busy-gate and "+
				"--readonly both read it", effectLabel(name), got, effect)
		}
	}
	for name, effect := range effects {
		if _, ok := want[name]; !ok {
			t.Errorf("registry gained %s → %s; add it to this golden", effectLabel(name), effect)
		}
	}
}

// EffectOf has to fail closed. KeyScreensaver is the live case: it dispatches
// without a Registry entry (its absence is what keeps the easter egg out of every
// help surface), so it is the one name a gate can be handed that no Entry
// classifies — and the answer must be the invalid value, never the permissive one.
func TestEffectOf_UnregisteredIsUnset(t *testing.T) {
	if got := EffectOf(KeyScreensaver); got != EffectUnset {
		t.Errorf("EffectOf(KeyScreensaver) = %s, want unset — a name no Entry owns must "+
			"not read back as observing", got)
	}
}

// The String forms appear in every failure message above and in the guards over in
// app/, so they are part of those messages' meaning rather than cosmetic.
func TestEffectStrings(t *testing.T) {
	for effect, want := range map[Effect]string{
		EffectUnset:   "unset",
		EffectObserve: "observe",
		EffectView:    "view",
		EffectMutate:  "mutate",
		Effect(99):    "unset",
	} {
		if got := effect.String(); got != want {
			t.Errorf("Effect(%d).String() = %q, want %q", int(effect), got, want)
		}
	}
}
