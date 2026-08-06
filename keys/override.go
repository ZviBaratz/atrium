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
// disappears from the hint bar and the cheatsheet. It stays reachable from the
// command palette, which is the point — the palette is what makes unbinding a
// key safe rather than a way to lose an action.
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
}

// Problem is one rejected override: which action, and why. The action keeps its
// default binding; every other override still applies.
type Problem struct {
	Action string
	Msg    string
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
	"esc":    "esc backs out of scroll mode, a filter and every overlay, ahead of dispatch",
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

	actions := make([]string, 0, len(overrides))
	for action := range overrides {
		actions = append(actions, action)
	}
	sort.Strings(actions)

	// Two passes, because the keys an override wants may be the keys another
	// override is giving up. Pass one accepts the well-formed overrides and
	// releases the defaults they replace; pass two claims what they asked for.
	// Collapsing the two would reject the commonest thing a user actually wants —
	// swapping a pair of keys — since the second half of the swap would still see
	// the first half's default in the way.
	type accepted struct {
		action string
		entry  *Entry
		keys   []string
	}
	var accepts []accepted
	for _, action := range actions {
		spec := overrides[action]
		entry, known := byAction[action]
		if !known {
			reject(action, "no such action — %s", nearestActionHint(action, byAction))
			continue
		}

		if spec.Disabled {
			if action == "attach_toggle" {
				reject(action, "cannot be unbound — it is the only way out of an attached "+
					"session other than tmux's own prefix, so unbinding it would strand you "+
					"inside a pane. Rebind it instead")
				continue
			}
			for _, k := range out[entry.Name].keys {
				delete(claimed, k)
			}
			out[entry.Name] = resolved{name: entry.Name, desc: out[entry.Name].desc}
			continue
		}

		keys, err := checkKeys(action, spec.Keys)
		if err != nil {
			reject(action, "%s", err)
			continue
		}
		for _, k := range out[entry.Name].keys {
			delete(claimed, k)
		}
		accepts = append(accepts, accepted{action: action, entry: entry, keys: keys})
	}

	for _, a := range accepts {
		// An override must never evict a key still held by an action the user did
		// not rebind. Whichever action lost would be silently unreachable while
		// every surface went on advertising its key, and over a map the loser would
		// be whichever entry happened to be visited last — so the override loses,
		// deterministically, and the message says how to get what was wanted.
		conflict := false
		for _, k := range a.keys {
			if owner, taken := claimed[k]; taken && owner != a.action {
				reject(a.action, "key %q is held by %q — rebind %q too, or %q is ignored",
					k, owner, owner, a.action)
				conflict = true
				break
			}
		}
		if conflict {
			// It gave up its defaults in pass one; put them back, since the override
			// it was giving them up for is not happening.
			for _, k := range out[a.entry.Name].keys {
				claimed[k] = a.action
			}
			continue
		}

		for _, k := range a.keys {
			claimed[k] = a.action
			if mode, shadow := shadowed[k]; shadow {
				reject(a.action, "key %q is consumed by %s before dispatch, so %q will not "+
					"fire there — it still works everywhere else", k, mode, a.action)
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
// mirrors as raw bytes — encodable as a single control byte.
func checkKeys(action string, list []string) ([]string, error) {
	if len(list) == 0 {
		return nil, fmt.Errorf("no key given — set a key, or %q to unbind it", Disabled)
	}
	if len(list) > maxKeysPerAction {
		return nil, fmt.Errorf("%d keys is more than the %d an action may answer to",
			len(list), maxKeysPerAction)
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
			if _, err := ControlByte(k); err != nil {
				return nil, fmt.Errorf("%w — %q is also honored inside a session, where "+
					"Atrium reads it as a single control byte", err, action)
			}
		}
	}
	return slices.Clone(list), nil
}

// ControlByte is the byte a terminal sends for a ctrl+<letter> chord, which is
// how the attach layer recognises the detach and kill keys while it is pumping
// raw stdin (session/tmux/attach.go).
//
// It refuses everything else rather than computing it, because the obvious
// derivation is quietly wrong for the rest: the mask is over the chord's last
// byte, so "ctrl+space" would encode as ctrl+E, "ctrl+up" as ctrl+P and
// "ctrl+pgdown" as ctrl+N — three chords that would silently detach on a key the
// user never bound.
func ControlByte(chord string) (byte, error) {
	base, ok := strings.CutPrefix(chord, "ctrl+")
	if !ok || len(base) != 1 || base[0] < 'a' || base[0] > 'z' {
		return 0, fmt.Errorf("key %q cannot be a control byte — use ctrl+ and a single letter", chord)
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
func nearestActionHint(action string, byAction map[string]*Entry) string {
	best, bestShared := "", 2
	names := make([]string, 0, len(byAction))
	for name := range byAction {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic pick among equally close names
	for _, name := range names {
		shared := 0
		for shared < len(name) && shared < len(action) && name[shared] == action[shared] {
			shared++
		}
		if shared > bestShared {
			best, bestShared = name, shared
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
