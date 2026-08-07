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
	// The two generated surfaces read Help().Key directly and drop the entry when
	// it is empty; prose goes through LabelOf, which cannot drop anything and so
	// says there is no key rather than leaving a hole in the sentence.
	if got := GlobalKeyBindings[KeyPauseAll].Help().Key; got != "" {
		t.Errorf("an unbound action's help label = %q, want empty so the bar and the "+
			"cheatsheet drop its entry", got)
	}
	if got := LabelOf(KeyPauseAll); got != unboundLabel {
		t.Errorf("LabelOf(pause_all) = %q, want %q — prose must not render an empty key",
			got, unboundLabel)
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
		{"detach on a wire-ambiguous chord", map[string]Spec{"attach_toggle": spec("ctrl+m")}, []string{"indistinguishable from enter"}},
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

// The review case, and the reason resolution runs to a fixpoint. "down" wants
// "k", which "up" holds; "up" wants "y", which copy_branch holds. One pass
// rejected "up" and handed its defaults back — including the "k" it had just
// given to "down" — leaving both claiming it. Two hundred runs then dispatched
// "k" to each of them almost exactly half the time, so the user's keymap changed
// between launches.
func TestValidate_ARejectedOverrideCannotLeaveADuplicateClaim(t *testing.T) {
	overrides := map[string]Spec{
		"down": spec("k"),
		"up":   spec("y"), // y is copy_branch's
	}
	seen := map[KeyName]int{}
	for i := 0; i < 200; i++ {
		_, restore := Apply(overrides)
		seen[GlobalKeyStringsMap["k"]]++
		for name, b := range GlobalKeyBindings {
			for _, k := range b.Keys() {
				if k != "k" {
					continue
				}
				if got := GlobalKeyStringsMap[k]; got != name {
					t.Fatalf("%v still lists key %q, but dispatch sends it to %v — "+
						"two actions claiming one key is a coin flip at every launch", name, k, got)
				}
			}
		}
		restore()
	}
	if len(seen) != 1 {
		t.Fatalf("key %q dispatched to more than one action across 200 runs: %v", "k", seen)
	}
}

// The rejections a cascade produces must be as stable as the acceptances.
func TestValidate_CascadingRejectionsAreDeterministic(t *testing.T) {
	overrides := map[string]Spec{
		"down": spec("k"),
		"up":   spec("y"),
		"new":  spec("j"),
	}
	first := renderProblems(Validate(overrides))
	for i := 0; i < 100; i++ {
		if got := renderProblems(Validate(overrides)); got != first {
			t.Fatalf("run %d disagreed:\n got: %s\nwant: %s", i, got, first)
		}
	}
}

// Four ctrl chords encode to a byte an ordinary keypress also sends. The TUI can
// tell them apart where the kitty protocol is available; the attach layer, which
// reads raw bytes off a pty, never can — so binding detach to ctrl+m would
// detach the user on every Enter pressed inside an agent pane.
func TestControlByte_RejectsTheWireAmbiguousChords(t *testing.T) {
	for chord, other := range map[string]string{
		"ctrl+m": "enter", "ctrl+i": "tab", "ctrl+j": "a newline", "ctrl+h": "backspace",
	} {
		if _, err := ControlByte(chord); err == nil {
			t.Errorf("ControlByte(%q) succeeded; it is %s on the wire", chord, other)
			continue
		}
		for _, action := range []string{"attach_toggle", "kill"} {
			problems := Validate(map[string]Spec{action: spec(chord)})
			if len(problems) != 1 {
				t.Errorf("%s = %q must be refused, got %v", action, chord, problems)
				continue
			}
			if !strings.Contains(problems[0].Error(), other) {
				t.Errorf("refusing %s = %q must say it collides with %s: %v",
					action, chord, other, problems[0])
			}
		}
	}
}

// Unbinding kill is legitimate, and has to reach the attach layer: otherwise the
// user who removed the kill key can still tear a session down from inside its
// own pane with the key they just removed.
func TestValidate_KillMayBeUnbound(t *testing.T) {
	problems, restore := Apply(map[string]Spec{"kill": {Disabled: true}})
	defer restore()

	if len(problems) != 0 {
		t.Fatalf("unbinding kill must be allowed: %v", problems)
	}
	if got := KillKey(); got != "" {
		t.Errorf("KillKey() = %q after being unbound, want empty", got)
	}
}

// A warning is not a refusal, and the surfaces split on the flag rather than on
// the wording, so the flag has to be set.
func TestValidate_ShadowWarningsAreFlagged(t *testing.T) {
	problems := Validate(map[string]Spec{"new": spec("x")})
	if len(problems) != 1 {
		t.Fatalf("want one problem, got %v", problems)
	}
	if !problems[0].Warning {
		t.Error("a shadowed key is applied, so its problem must be marked a warning")
	}

	refusals := Validate(map[string]Spec{"new": spec("esc")})
	if len(refusals) != 1 || refusals[0].Warning {
		t.Errorf("a reserved key is refused, so it must not be marked a warning: %v", refusals)
	}
}

// The mode bars resolve their keys through the registry, so their labels have to
// follow a rebind — multi-select advertised the literal "p/r/x" while its
// handler looked pause and resume up, so rebinding pause left the bar teaching a
// key that did nothing.
func TestModeHints_FollowARebind(t *testing.T) {
	before := VisualModeHints()[1].Help().Key
	if before != "p/r/x" {
		t.Fatalf("multi-select's marked-set label = %q, want the default %q", before, "p/r/x")
	}

	_, restore := Apply(map[string]Spec{"pause": spec("z")})
	defer restore()

	if got := VisualModeHints()[1].Help().Key; got != "z/r/x" {
		t.Errorf("after rebinding pause to z the bar teaches %q, want %q", got, "z/r/x")
	}
}

// The multi-select bar's label is a DISPLAY spelling, so it has to be generated
// the way every other one is: through Label. Concatenating PrimaryKey printed the
// dispatch spelling instead, so a chord came out "alt+p/r/x" on the bar while the
// cheatsheet's own multi-select row said "alt-p/r/x" for the same three keys.
func TestModeHints_UseTheDisplaySpelling(t *testing.T) {
	_, restore := Apply(map[string]Spec{"pause": spec("alt+p")})
	defer restore()

	if got := VisualModeHints()[1].Help().Key; got != "alt-p/r/x" {
		t.Errorf("multi-select's marked-set label = %q, want the hyphenated %q", got, "alt-p/r/x")
	}
}

// An unbound action has no key to contribute, and a label that joins it anyway
// renders a leading slash where a key should be ("/r/x") — plus a binding whose
// key list holds an empty string, which dispatches on nothing.
func TestModeHints_UnboundActionLeavesNoEmptySegment(t *testing.T) {
	_, restore := Apply(map[string]Spec{"pause": {Disabled: true}})
	defer restore()

	marked := VisualModeHints()[1]
	if got := marked.Help().Key; got != "r/x" {
		t.Errorf("with pause unbound the bar teaches %q, want %q", got, "r/x")
	}
	for _, k := range marked.Keys() {
		if k == "" {
			t.Errorf("the marked-set binding kept an empty key: %q", marked.Keys())
		}
	}
}

// checkKeys validated every key in an attach-layer action's list, but
// installAttachChords installs only the primary one — so a second alias was live
// on the list and, inside a pane, forwarded to the agent as its raw control byte
// instead of doing what the user bound it to. One key, or the two layers describe
// different keys.
func TestValidate_AttachLayerActionTakesOneKeyOnly(t *testing.T) {
	for _, action := range []string{"kill", "attach_toggle"} {
		problems := Validate(map[string]Spec{action: {Keys: []string{"ctrl+g", "ctrl+y"}}})
		if len(problems) != 1 || problems[0].Warning {
			t.Errorf("%s with two keys must be refused, got %v", action, problems)
			continue
		}
		if msg := problems[0].Error(); !strings.Contains(msg, "one key only") {
			t.Errorf("refusing %s must say why one key is the limit: %q", action, msg)
		}
		// The single-key form of the same chord still applies.
		if got := Validate(map[string]Spec{action: spec("ctrl+g")}); len(got) != 0 {
			t.Errorf("%s = ctrl+g must still be accepted, got %v", action, got)
		}
	}
}

// The wire-ambiguous chords are refused outright for the two actions the attach
// layer reads as raw bytes. Everywhere else they were accepted in silence: the
// TUI can separate ctrl+m from enter only on a terminal that disambiguates
// modified keys, so on the common one the action was simply dead while the
// cheatsheet went on printing the key. Applied, but said out loud.
func TestValidate_WireAmbiguousChordWarnsOnAnOrdinaryAction(t *testing.T) {
	problems, restore := Apply(map[string]Spec{"settings": spec("ctrl+m")})
	defer restore()

	if len(problems) != 1 {
		t.Fatalf("want one warning, got %v", problems)
	}
	if !problems[0].Warning {
		t.Error("the override is applied, so its problem must be marked a warning")
	}
	if msg := problems[0].Error(); !strings.Contains(msg, "enter") {
		t.Errorf("the warning must name the key it collides with: %q", msg)
	}
	if got := GlobalKeyStringsMap["ctrl+m"]; got != KeySettings {
		t.Errorf("ctrl+m dispatches %v — a warning must not drop the override", got)
	}
}
