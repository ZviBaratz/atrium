package keys

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
)

// The keybindings override layer.
//
// Atrium's keys compete for the same keyboard as the user's terminal, their
// multiplexer and their shell, and two of them (detach and kill) are raw control
// bytes read while the user is inside an agent pane. That is a collision the
// author cannot resolve on the user's behalf, which is what this layer is for:
// config.json names an action and the key(s) it should answer to, and Apply
// rewrites the derived maps before the program starts.
//
// Every surface is already a projection of those maps — the hint bar, the ?
// cheatsheet, the command palette, and (since the prose pass) every sentence
// that names a key — so an override reaches all of them without any of them
// knowing this file exists.
//
// A bad override is reported and dropped, never fatal. config.LoadConfig cannot
// fail (customcmd/customcmd.go says why: a dozen-odd non-TUI callers, including
// the daemon and the worktree hooks), and a typo in a keybinding must not be a
// reason a user cannot reach a fleet of running sessions. Dropping one override
// leaves that action on its default; the rest still apply, and the problem is
// reported at startup and by `atrium doctor`.

// Disabled is the spec value that unbinds an action: it answers to no key, and
// disappears from the hint bar. The cheatsheet keeps its row and blanks the key
// column instead (app/help.go, pinned by TestHelpScreen_OmitsAnUnboundActionsKey)
// — dropping the row too would take away the one place a user can see the action
// still exists. It stays reachable from the command palette, which is the point:
// the palette is what makes unbinding a key safe rather than a way to lose an
// action.
const Disabled = "disabled"

// maxKeysPerAction caps how many keys one action may answer to. The default
// keymap's widest entry has two ("up"/"k"); three leaves room without letting a
// generated label outgrow the cheatsheet's key column.
const maxKeysPerAction = 3

// Spec is what config names for one action: the keys it should answer to, or
// nothing at all when the user asked for it to be unbound.
type Spec struct {
	Keys     []string
	Disabled bool
	// Malformed carries the reason the config value could not be read as a spec at
	// all. It travels as data rather than as an unmarshalling error because
	// config.LoadConfig cannot fail; validation turns it back into a Problem here,
	// so a value Atrium could not even parse is reported in the same list and the
	// same voice as one it parsed and refused.
	Malformed string
}

// Problem is one rejected override: which action, and why. The action keeps its
// default binding; every other override still applies.
type Problem struct {
	Action string
	Msg    string
	// Warning marks a problem the override survived: it was applied, with a
	// caveat worth saying out loud. A refusal and a caveat read identically in a
	// list, and reporting the second as the first tells the user their key did not
	// take effect when it did — so the surfaces split on this.
	Warning bool
}

// Error renders the problem as the user sees it.
func (p Problem) Error() string {
	if p.Action == "" {
		return fmt.Sprintf("keybindings: %s", p.Msg)
	}
	return fmt.Sprintf("keybindings[%q]: %s", p.Action, p.Msg)
}

// reserved names the keys an override may not take, and why. Each one is
// consumed before the dispatch map is ever consulted, so a binding on it would
// be dead — and the first three are the ways out of a keymap that has gone
// wrong, which is exactly when they must still work.
var reserved = map[string]string{
	"ctrl+c": "quit is matched before the keymap, so it still works when a rebind does not",
	// Short on purpose: the keybindings modal clips a report line at
	// app/custom_commands.go's reportLineBudget, and an enumeration of esc's
	// rungs (see escLadder, app package) does not survive the clip.
	"esc":    "esc backs out of scroll, focus, filters and overlays",
	"ctrl+l": "ctrl+l is the manual repaint, and has to work while the screen is corrupted",
	"`":      "the backtick is the screensaver's, and is dispatched outside the registry",
	// ctrl+[ IS esc on a terminal without key disambiguation, and 0x1b is the
	// byte every escape sequence opens with — so binding it would both shadow esc
	// and make the attach layer detach on a stray arrow key.
	"ctrl+[": "ctrl+[ is esc on most terminals, and is the lead byte of every escape sequence",
	// The attach layer matches these as escape sequences while you are inside a
	// pane (session/tmux/detach.go), so the TUI would own them only on the list —
	// a key that works in one half of the app and not the other.
	"ctrl+pgup":   "the attach layer cycles to the previous session on it",
	"ctrl+pgdown": "the attach layer cycles to the next session on it",
}

// shadowed names the keys a mode handler consumes before falling through to the
// dispatch map. An override onto one of these is honored everywhere except
// inside that mode, which is worth saying out loud rather than leaving the user
// to discover — but it is a warning, not a rejection: the key works fine in the
// default state, which is where most actions live.
//
// Kept honest by TestShadowTableMatchesTheModeHandlers, which reads the case
// labels out of the handlers rather than trusting this list.
var shadowed = map[string]string{
	"x":     "multi-select mode",
	"enter": "diff-comment mode",
}

// attachedLayerActions are the actions the attach layer honors as raw control
// bytes while the user is inside an agent pane (session/tmux/attach.go). Their
// keys have to survive being encoded as a single byte, which only ctrl+<letter>
// does, so they are validated more narrowly than the rest.
var attachedLayerActions = map[string]KeyName{
	"kill":          KeyKill,
	"attach_toggle": KeyAttachToggle,
}

// resolved is one action's binding after the override layer has been applied.
type resolved struct {
	name KeyName
	// docOnly entries reach GlobalKeyBindings (the cheatsheet documents them) but
	// never the dispatch map, exactly as in the default derivation.
	docOnly bool
	keys    []string
	// label is the display spelling; empty when the action was unbound.
	label string
	desc  string
}

// Validate resolves overrides against the default keymap without installing
// anything, and reports every override it had to drop. It is the pure half, for
// `atrium doctor` and for tests; Apply is Validate plus the install.
func Validate(overrides map[string]Spec) []Problem {
	problems, _ := resolve(overrides)
	return problems
}

// resolve is the whole of the override layer: it walks the requested overrides
// against the default keymap and returns the resulting bindings alongside the
// overrides it had to drop.
//
// The result is order-independent. A map has no iteration order, so resolving in
// map order would let a collision between two overrides be decided differently
// on each run — and the losing action would be the one silently unreachable.
func resolve(overrides map[string]Spec) ([]Problem, map[KeyName]resolved) {
	byAction := map[string]*Entry{}
	for i := range Registry {
		if Registry[i].Action != "" {
			byAction[Registry[i].Action] = &Registry[i]
		}
	}

	// Start from the defaults, then apply the surviving overrides onto them.
	//
	// out carries every entry, DocOnly included, because it becomes
	// GlobalKeyBindings and the cheatsheet renders a label for the documented-only
	// keys too. claimed carries only the dispatched ones, because that is what the
	// dispatch map holds — the DocOnly keys are unclaimable for a different reason,
	// which is that they are all in the reserved table.
	out := make(map[KeyName]resolved, len(Registry))
	claimed := map[string]string{} // key string → the action holding it
	for _, e := range Registry {
		out[e.Name] = resolved{
			name:    e.Name,
			docOnly: e.DocOnly,
			keys:    slices.Clone(e.Binding.Keys()),
			label:   e.Binding.Help().Key,
			desc:    e.Binding.Help().Desc,
		}
		if e.DocOnly {
			continue
		}
		for _, k := range e.Binding.Keys() {
			claimed[k] = e.Action
		}
	}

	var problems []Problem
	reject := func(action, format string, args ...any) {
		problems = append(problems, Problem{Action: action, Msg: fmt.Sprintf(format, args...)})
	}
	warn := func(action, format string, args ...any) {
		problems = append(problems, Problem{Action: action, Warning: true, Msg: fmt.Sprintf(format, args...)})
	}

	actions := make([]string, 0, len(overrides))
	for action := range overrides {
		actions = append(actions, action)
	}
	sort.Strings(actions)

	// Resolution runs to a fixpoint rather than in one pass, because rejecting an
	// override puts its default keys back — and those may be the very keys another
	// override was accepted for. Settling that by restoring into a half-built claim
	// map is order-dependent, and order-dependent here means a coin flip: with
	// {"down": "k", "up": "y"} (y being copy_branch's), one pass left both "up" and
	// "down" claiming "k", and 200 runs dispatched it to each of them almost exactly
	// half the time. A user would get a different keymap on each launch.
	//
	// So instead: assume every well-formed override applies, rebuild the claim map
	// from scratch, and drop the first one (in sorted order) that wants a key
	// something else holds. Repeat until nothing is dropped. Rejections only ever
	// grow, so it terminates, and the sorted walk makes the outcome independent of
	// the config file's map order.
	type accepted struct {
		action string
		entry  *Entry
		keys   []string
	}
	var accepts []accepted
	disabled := map[KeyName]bool{}
	for _, action := range actions {
		spec := overrides[action]
		entry, known := byAction[action]
		if !known {
			reject(action, "no such action — %s", nearestActionHint(action, byAction))
			continue
		}
		if spec.Malformed != "" {
			reject(action, "%s", spec.Malformed)
			continue
		}
		if spec.Disabled {
			if action == "attach_toggle" {
				reject(action, "cannot be unbound — it is the only way out of an attached "+
					"session other than tmux's own prefix, so unbinding it would strand you "+
					"inside a pane. Rebind it instead")
				continue
			}
			disabled[entry.Name] = true
			continue
		}
		keys, err := checkKeys(action, spec.Keys)
		if err != nil {
			reject(action, "%s", err)
			continue
		}
		accepts = append(accepts, accepted{action: action, entry: entry, keys: keys})
	}

	for {
		// Rebuild from the defaults each round: an action whose override was dropped
		// on the previous round is back to holding its own keys, and that has to be
		// visible to everyone still standing.
		claimed = map[string]string{}
		overridden := map[KeyName]bool{}
		for _, a := range accepts {
			overridden[a.entry.Name] = true
		}
		for _, e := range Registry {
			if e.DocOnly || overridden[e.Name] || disabled[e.Name] {
				continue
			}
			for _, k := range e.Binding.Keys() {
				claimed[k] = e.Action
			}
		}

		dropped := -1
		for i, a := range accepts {
			conflict := ""
			for _, k := range a.keys {
				if owner, taken := claimed[k]; taken && owner != a.action {
					reject(a.action, "key %q is held by %q — rebind %q too, or %q is ignored",
						k, owner, owner, a.action)
					conflict = k
					break
				}
			}
			if conflict != "" {
				dropped = i
				break
			}
			for _, k := range a.keys {
				claimed[k] = a.action
			}
		}
		if dropped < 0 {
			break
		}
		accepts = append(accepts[:dropped], accepts[dropped+1:]...)
	}

	for name := range disabled {
		out[name] = resolved{name: name, desc: out[name].desc}
	}
	for _, a := range accepts {
		for _, k := range a.keys {
			if mode, shadow := shadowed[k]; shadow {
				warn(a.action, "key %q is consumed by %s before dispatch, so %q will not "+
					"fire there — it still works everywhere else", k, mode, a.action)
			}
			// A wire-ambiguous chord is refused outright for the attach-layer actions
			// (checkKeys, via ControlByte) because the pane cannot tell it from the
			// ordinary key. Everywhere else it is a warning rather than a rejection: the
			// TUI *can* separate them, but only on a terminal speaking the kitty
			// keyboard protocol or modifyOtherKeys. On the common terminal the chord is
			// simply never delivered — the action is silently dead while the cheatsheet
			// goes on printing the key — so the config that asked for it says so.
			if other, ambiguous := needsDisambiguation(k); ambiguous {
				warn(a.action, "key %q reaches Atrium as %s on a terminal that does not "+
					"disambiguate modified keys, so %q will not fire there — see the README "+
					"on remapping keys", k, other, a.action)
			}
		}
		out[a.entry.Name] = resolved{
			name:  a.entry.Name,
			keys:  a.keys,
			label: Label(a.keys),
			desc:  out[a.entry.Name].desc,
		}
	}

	return problems, out
}

// checkKeys validates one override's key list: every key legal and canonically
// spelled, none reserved, no repeats, and — for the two actions the attach layer
// mirrors as raw bytes — exactly one key, encodable as a single control byte.
func checkKeys(action string, list []string) ([]string, error) {
	if len(list) == 0 {
		return nil, fmt.Errorf("no key given — set a key, or %q to unbind it", Disabled)
	}
	if len(list) > maxKeysPerAction {
		return nil, fmt.Errorf("%d keys is more than the %d an action may answer to",
			len(list), maxKeysPerAction)
	}
	// One key only for the two the attach layer mirrors. SetAttachChords takes a
	// single byte per action and installAttachChords derives it from the primary
	// key, so a second alias would be live on the list and dead inside a pane —
	// where it is not inert but forwarded, typing its control byte into the agent.
	// Validating every key in the list while installing only the first is what made
	// that reachable; refusing the list keeps the two layers describing one key.
	if _, attached := attachedLayerActions[action]; attached && len(list) > 1 {
		return nil, fmt.Errorf("%q answers to one key only — inside a session Atrium reads "+
			"it as a single raw byte, so a second key would work on the list and type "+
			"into the agent's pane", action)
	}
	seen := map[string]bool{}
	for _, k := range list {
		if _, err := ParseKey(k); err != nil {
			return nil, err
		}
		if why, isReserved := reserved[k]; isReserved {
			return nil, fmt.Errorf("key %q is reserved — %s", k, why)
		}
		if seen[k] {
			return nil, fmt.Errorf("key %q is given twice", k)
		}
		seen[k] = true
		if _, attached := attachedLayerActions[action]; attached {
			// Returned as-is: ControlByte's messages already explain that this action
			// is honored inside a session and why that constrains the chord, so
			// wrapping them repeated the same sentence twice in one line.
			if _, err := ControlByte(k); err != nil {
				return nil, err
			}
		}
	}
	return slices.Clone(list), nil
}

// wireAmbiguousCtrl are the ctrl chords whose control code is also what a
// terminal sends for an ordinary key, and what that key is.
//
// The TUI can tell them apart on a terminal that speaks the kitty keyboard
// protocol (see the README), but the attach layer cannot: it reads raw bytes off
// a pty, where ctrl+m and enter are both 0x0d and nothing distinguishes them. So
// binding detach to ctrl+m would detach the user every time they pressed Enter
// inside an agent pane — an unusable session, from a config line that looks
// perfectly reasonable.
var wireAmbiguousCtrl = map[byte]string{
	'm': "enter",
	'i': "tab",
	'j': "a newline",
	'h': "backspace",
}

// wireAmbiguousCtrlChord reports whether a key string is one of the ctrl chords
// wireAmbiguousCtrl lists, and what an ordinary keypress sends the same code for.
func wireAmbiguousCtrlChord(chord string) (string, bool) {
	base, ok := strings.CutPrefix(chord, "ctrl+")
	if !ok || len(base) != 1 {
		return "", false
	}
	other, ambiguous := wireAmbiguousCtrl[base[0]]
	return other, ambiguous
}

// modifiedEnterChord reports whether a key string is Enter under a modifier that
// no legacy terminal encodes — the same collapse wireAmbiguousCtrl describes,
// approached from the other side.
//
// Ctrl and Shift over Enter both arrive as a bare CR, so on a terminal without
// disambiguation "shift+enter" IS "enter"; that is exactly why #396's newline had
// to wait for the protocol. Alt is the exception and must not be listed: alt+enter
// has a real legacy encoding, ESC CR, which is what has made it the portable
// stand-in all along (ultraviolet key_table.go).
//
// Deliberately scoped to Enter rather than generalised over every special key.
// The obvious generalisation is wrong in both directions — shift+tab has a legacy
// encoding (CSI Z) while ctrl+tab does not — and a rule that is wrong about which
// keys survive is worse than a short list that is right. See needsDisambiguation
// on what that costs.
// The modifier list is the whole decision — "alt+" is absent rather than
// excluded, so there is exactly one place to read the answer off.
func modifiedEnterChord(chord string) (string, bool) {
	base, ok := strings.CutSuffix(chord, "enter")
	if !ok {
		return "", false
	}
	switch base {
	case "ctrl+", "shift+", "ctrl+shift+":
		return "enter", true
	}
	return "", false
}

// needsDisambiguation reports whether a keystroke reaches Atrium only on a
// terminal that disambiguates modified keys, and what an ordinary keypress sends
// in its place everywhere else.
//
// This is a FLOOR over the chords this package knows about, not a decision
// procedure. Proving the general case would mean owning a model of every legacy
// encoding — the thing terminfo exists for and still gets wrong — so a chord that
// is not listed here is "not known to be ambiguous", never "known to be safe".
// Treat a false answer as the absence of a warning, not as a guarantee.
func needsDisambiguation(chord string) (string, bool) {
	if other, ambiguous := wireAmbiguousCtrlChord(chord); ambiguous {
		return other, true
	}
	return modifiedEnterChord(chord)
}

// ControlByte is the byte a terminal sends for a ctrl+<letter> chord, which is
// how the attach layer recognises the detach and kill keys while it is pumping
// raw stdin (session/tmux/attach.go).
//
// It refuses everything it cannot encode unambiguously rather than computing it.
// The obvious derivation is quietly wrong twice over: the mask is over the
// chord's last byte, so "ctrl+space" would encode as ctrl+E, "ctrl+up" as ctrl+P
// and "ctrl+pgdown" as ctrl+N; and four of the letters it does encode correctly
// land on a byte an ordinary keypress also produces.
func ControlByte(chord string) (byte, error) {
	base, ok := strings.CutPrefix(chord, "ctrl+")
	if !ok || len(base) != 1 || base[0] < 'a' || base[0] > 'z' {
		return 0, fmt.Errorf("key %q cannot be read as a single control byte — inside a "+
			"session Atrium reads raw stdin, so this action needs ctrl+ and a single letter", chord)
	}
	if other, ambiguous := wireAmbiguousCtrl[base[0]]; ambiguous {
		return 0, fmt.Errorf("key %q is indistinguishable from %s inside a session, where "+
			"Atrium reads raw stdin — pick another letter", chord, other)
	}
	return base[0] & 0x1f, nil
}

// Apply installs overrides over the default keymap and returns the problems it
// dropped, plus a func that puts the defaults back.
//
// Call it once, before tea.NewProgram: the maps it rewrites are read from the
// render and update path with no synchronisation, which is safe only because
// nothing writes them once the program is running. The restore func is for
// tests, so an override in one does not leak into the next.
func Apply(overrides map[string]Spec) ([]Problem, func()) {
	prevBindings, prevStrings := GlobalKeyBindings, GlobalKeyStringsMap

	problems, res := resolve(overrides)
	bindings := make(map[KeyName]key.Binding, len(res))
	dispatch := make(map[string]KeyName, len(res))
	for name, r := range res {
		// An unbound action keeps its description and loses its keys and its
		// label, so the surfaces that render a key have nothing to print. Building
		// it this way rather than through SetEnabled is deliberate: nothing in the
		// tree consults Binding.Enabled (only key.Matches does, which Atrium never
		// calls), so a disabled-but-populated binding would go on rendering the old
		// key for an action that no longer answers to it.
		bindings[name] = key.NewBinding(key.WithKeys(r.keys...), key.WithHelp(r.label, r.desc))
		if r.docOnly {
			continue
		}
		for _, k := range r.keys {
			dispatch[k] = name
		}
	}
	// The screensaver dispatches without a Registry entry, so its line is
	// re-appended here for the same reason it is appended in the derivation.
	dispatch["`"] = KeyScreensaver

	GlobalKeyBindings, GlobalKeyStringsMap = bindings, dispatch
	return problems, func() {
		GlobalKeyBindings, GlobalKeyStringsMap = prevBindings, prevStrings
	}
}

// nearestActionHint helps a user who typed an action name that does not exist,
// by naming the closest one when there is an obvious candidate and pointing at
// the documentation when there is not.
//
// Closest is the longest shared prefix, with ties broken by whichever candidate
// is nearest in length. The tie-break is what makes it useful rather than merely
// present: "pauze_all" shares exactly three characters with both "pause" and
// "pause_all", and suggesting the short one to somebody who plainly meant the
// long one is a worse answer than none.
func nearestActionHint(action string, byAction map[string]*Entry) string {
	names := make([]string, 0, len(byAction))
	for name := range byAction {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic pick among equally close names

	best, bestShared, bestGap := "", 2, 0
	for _, name := range names {
		shared := 0
		for shared < len(name) && shared < len(action) && name[shared] == action[shared] {
			shared++
		}
		gap := len(name) - len(action)
		if gap < 0 {
			gap = -gap
		}
		if shared > bestShared || (shared == bestShared && best != "" && gap < bestGap) {
			best, bestShared, bestGap = name, shared, gap
		}
	}
	if best != "" {
		return fmt.Sprintf("did you mean %q?", best)
	}
	return "see the keybindings table in the README for the names"
}

// ConsumedBeforeDispatch reports whether a key is claimed by something upstream
// of the dispatch map — a reserved key the prelude handles, or a key a mode
// handler routes itself — and why.
//
// Exported for the guard in app/ that reads the mode handlers' case labels and
// holds them against these two tables. The tables are a claim about code in
// another package, which is the kind of claim that rots quietly.
func ConsumedBeforeDispatch(k string) (string, bool) {
	if why, ok := reserved[k]; ok {
		return why, true
	}
	if mode, ok := shadowed[k]; ok {
		return "consumed by " + mode + " before dispatch", true
	}
	return "", false
}
