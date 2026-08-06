package keys

import (
	"strings"
	"testing"
)

func spec(k ...string) Spec { return Spec{Keys: k} }

// The baseline the whole feature rests on: a user with no keybindings section
// gets today's keymap, bit for bit.
func TestApply_NoOverridesLeavesTheDefaults(t *testing.T) {
	wantBindings, wantStrings := GlobalKeyBindings, GlobalKeyStringsMap

	problems, restore := Apply(nil)
	defer restore()

	if len(problems) != 0 {
		t.Fatalf("Apply(nil) reported %v", problems)
	}
	if len(GlobalKeyStringsMap) != len(wantStrings) {
		t.Fatalf("dispatch map has %d keys, want %d", len(GlobalKeyStringsMap), len(wantStrings))
	}
	for k, want := range wantStrings {
		if got, ok := GlobalKeyStringsMap[k]; !ok || got != want {
			t.Errorf("dispatch[%q] = %v (present %v), want %v", k, got, ok, want)
		}
	}
	for name, want := range wantBindings {
		got := GlobalKeyBindings[name]
		if got.Help().Key != want.Help().Key || got.Help().Desc != want.Help().Desc {
			t.Errorf("%q help drifted: %v / %v", want.Help().Desc, got.Help(), want.Help())
		}
	}
}

func TestApply_RebindsTheKeyAndItsLabel(t *testing.T) {
	problems, restore := Apply(map[string]Spec{"new": spec("ctrl+n")})
	defer restore()

	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if got, ok := GlobalKeyStringsMap["ctrl+n"]; !ok || got != KeyNew {
		t.Errorf("ctrl+n dispatches %v (present %v), want KeyNew", got, ok)
	}
	if _, ok := GlobalKeyStringsMap["n"]; ok {
		t.Error("n still dispatches — an override replaces the action's keys, it does not add to them")
	}
	// The surfaces read the label, so a rebind that moved the key but not the
	// label would leave every hint bar and cheatsheet row lying.
	if got := LabelOf(KeyNew); got != "ctrl-n" {
		t.Errorf("LabelOf(new) = %q, want the regenerated %q", got, "ctrl-n")
	}
}

// Unbinding has to leave the action with nothing to render, or the bar prints a
// label for a key that no longer answers.
func TestApply_DisabledLeavesNoKeyAndNoLabel(t *testing.T) {
	problems, restore := Apply(map[string]Spec{"pause_all": {Disabled: true}})
	defer restore()

	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if _, ok := GlobalKeyStringsMap["ctrl+p"]; ok {
		t.Error("ctrl+p still dispatches after pause_all was unbound")
	}
	if got := LabelOf(KeyPauseAll); got != "" {
		t.Errorf("LabelOf(pause_all) = %q, want empty so no surface can render it", got)
	}
	if got := GlobalKeyBindings[KeyPauseAll].Help().Desc; got == "" {
		t.Error("an unbound action must keep its description — the palette still lists it")
	}
}

// The rule that makes the outcome independent of map order: an override never
// takes a key a default is still holding.
func TestValidate_OverrideNeverEvictsADefault(t *testing.T) {
	problems := Validate(map[string]Spec{"new": spec("p")}) // p is pause's

	if len(problems) != 1 {
		t.Fatalf("want exactly one problem, got %v", problems)
	}
	msg := problems[0].Error()
	for _, want := range []string{`"new"`, `"pause"`, `"p"`} {
		if !strings.Contains(msg, want) {
			t.Errorf("problem %q must name %s — a collision message that names only the loser "+
				"cannot be acted on", msg, want)
		}
	}
}

// Swapping two keys is the case the eviction rule has to leave possible: it is
// legal precisely because both actions are overridden.
func TestValidate_SwappingTwoKeysIsAllowed(t *testing.T) {
	problems, restore := Apply(map[string]Spec{
		"pause":  spec("r"),
		"resume": spec("p"),
	})
	defer restore()

	if len(problems) != 0 {
		t.Fatalf("swapping two keys must be legal, got %v", problems)
	}
	if got := GlobalKeyStringsMap["r"]; got != KeyPause {
		t.Errorf("r dispatches %v, want KeyPause", got)
	}
	if got := GlobalKeyStringsMap["p"]; got != KeyResume {
		t.Errorf("p dispatches %v, want KeyResume", got)
	}
}

// A map has no iteration order. If the resolver walked it directly, this pair
// would be decided differently between runs and the losing action would be
// silently unreachable — so run it enough times that a coin flip would show.
func TestValidate_IsOrderIndependent(t *testing.T) {
	overrides := map[string]Spec{
		"new":              spec("z"),
		"send":             spec("z"),
		"rename":           spec("z"),
		"queue":            spec("z"),
		"filter":           spec("z"),
		"copy_branch":      spec("z"),
		"move_account_up":  spec("z"),
		"new_pick_project": spec("z"),
	}
	first := renderProblems(Validate(overrides))
	for i := 0; i < 100; i++ {
		if got := renderProblems(Validate(overrides)); got != first {
			t.Fatalf("run %d disagreed with the first:\n got: %s\nwant: %s", i, got, first)
		}
	}
	// And the winner is the first by name, not by luck.
	_, restore := Apply(overrides)
	defer restore()
	if got := GlobalKeyStringsMap["z"]; got != KeyCopyBranch {
		t.Errorf("z went to %v; the lowest-sorting action (copy_branch) must win", got)
	}
}

func TestValidate_RejectsWithAnActionableMessage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		overrides map[string]Spec
		want      []string
	}{
		{"unknown action", map[string]Spec{"pauze": spec("x")}, []string{"no such action", `"pause"`}},
		{"misspelled key", map[string]Spec{"new": spec("ctrl-n")}, []string{`"ctrl+n"`}},
		{"reserved key", map[string]Spec{"new": spec("esc")}, []string{"reserved", "overlay"}},
		{"reserved ctrl+[", map[string]Spec{"new": spec("ctrl+[")}, []string{"reserved", "escape sequence"}},
		{"screensaver key", map[string]Spec{"new": spec("`")}, []string{"reserved", "screensaver"}},
		{"no key", map[string]Spec{"new": {}}, []string{"no key given", `"disabled"`}},
		{"too many keys", map[string]Spec{"new": spec("z", "Z", "ctrl+z", "alt+z")}, []string{"more than the 3"}},
		{"repeated key", map[string]Spec{"new": spec("z", "z")}, []string{"given twice"}},
		{"detach unbound", map[string]Spec{"attach_toggle": {Disabled: true}}, []string{"strand", "Rebind it instead"}},
		{"detach not a control chord", map[string]Spec{"attach_toggle": spec("z")}, []string{"control byte", "inside a session"}},
		{"kill not a control chord", map[string]Spec{"kill": spec("ctrl+up")}, []string{"control byte"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := Validate(tc.overrides)
			if len(problems) != 1 {
				t.Fatalf("want exactly one problem, got %v", problems)
			}
			msg := problems[0].Error()
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Errorf("problem %q does not mention %q", msg, want)
				}
			}
		})
	}
}

// A shadowed key is honored, and said out loud. Rejecting it would be wrong —
// the action works everywhere except inside one mode — but leaving the user to
// discover the exception by pressing it would be worse.
func TestValidate_ShadowedKeyIsWarnedNotRejected(t *testing.T) {
	problems, restore := Apply(map[string]Spec{"new": spec("x")})
	defer restore()

	if len(problems) != 1 {
		t.Fatalf("want one warning, got %v", problems)
	}
	if msg := problems[0].Error(); !strings.Contains(msg, "multi-select mode") {
		t.Errorf("warning %q must name the mode that swallows the key", msg)
	}
	if got := GlobalKeyStringsMap["x"]; got != KeyNew {
		t.Errorf("x dispatches %v — a shadow warning must not drop the override", got)
	}
}

// Apply mutates package state, so the escape hatch has to actually work or one
// test's override becomes the next test's mystery failure.
func TestApply_RestorePutsTheDefaultsBack(t *testing.T) {
	before := LabelOf(KeyNew)
	_, restore := Apply(map[string]Spec{"new": spec("ctrl+n")})
	restore()

	if got := LabelOf(KeyNew); got != before {
		t.Errorf("LabelOf(new) = %q after restore, want %q", got, before)
	}
	if got, ok := GlobalKeyStringsMap["n"]; !ok || got != KeyNew {
		t.Error("restore did not put the default dispatch entry back")
	}
}

// The screensaver's dispatch line is hand-appended in two places now (the
// derivation and Apply), so assert the second one — losing it would kill the
// easter egg for every user who sets a single override.
func TestApply_KeepsTheScreensaverDispatchLine(t *testing.T) {
	_, restore := Apply(map[string]Spec{"new": spec("ctrl+n")})
	defer restore()

	if got, ok := GlobalKeyStringsMap["`"]; !ok || got != KeyScreensaver {
		t.Error("the screensaver key must survive an override")
	}
}

func TestControlByte_RejectsWhatTheMaskWouldMisEncode(t *testing.T) {
	if got, err := ControlByte("ctrl+x"); err != nil || got != 24 {
		t.Errorf("ControlByte(ctrl+x) = %d, %v; want 24", got, err)
	}
	if got, err := ControlByte("ctrl+q"); err != nil || got != 17 {
		t.Errorf("ControlByte(ctrl+q) = %d, %v; want 17", got, err)
	}
	// Each of these has a last byte that masks to a plausible-looking control
	// code, so computing instead of refusing would bind a chord the user never
	// asked for: ctrl+space→ctrl+E, ctrl+up→ctrl+P, ctrl+pgdown→ctrl+N.
	for _, chord := range []string{"ctrl+space", "ctrl+up", "ctrl+pgdown", "ctrl+[", "ctrl+]", "x", "", "ctrl+X"} {
		if _, err := ControlByte(chord); err == nil {
			t.Errorf("ControlByte(%q) succeeded; it must refuse anything but ctrl+<letter>", chord)
		}
	}
}

func renderProblems(ps []Problem) string {
	var b strings.Builder
	for _, p := range ps {
		b.WriteString(p.Error())
		b.WriteString("\n")
	}
	return b.String()
}
