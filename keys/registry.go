package keys

import (
	"charm.land/bubbles/v2/key"
)

// Layer says which input layer honors a key. Most keys are dispatched by the
// TUI's Update loop; a few are honored (also or only) by the attach layer's
// raw-byte scanner while inside a session (session/tmux/attach.go and
// detach.go). Generated help uses the tag to document layer-crossing keys
// truthfully: LayerAttached rows render with an "in a session: " prefix, and
// LayerBoth descs must state the attached side in prose (pinned by
// TestHelpGroups_LayerBothStateAttachedSide).
type Layer int

const (
	// LayerTUI keys are dispatched from GlobalKeyStringsMap by the home model.
	LayerTUI Layer = iota
	// LayerAttached keys are honored only while attached, by the attach layer.
	LayerAttached
	// LayerBoth keys are TUI actions the attach layer mirrors as raw bytes
	// (ctrl+q detaches, ctrl+x kills).
	LayerBoth
)

// Entry is one row of the keymap registry: a logical action with the binding
// that carries its authoritative key strings (WithKeys) and hint-bar help
// text (WithHelp), plus the layer that honors it.
type Entry struct {
	Name KeyName
	// Action is the stable, user-facing name of this action — the vocabulary
	// config.json's keybindings section is written in. Remappable is exactly
	// Action != "": the DocOnly entries below carry none (their keys are
	// handled outside the dispatch map, so an override could not reach them),
	// and the screensaver has no Entry at all, so both are excluded from the
	// remap namespace structurally rather than by a denylist.
	//
	// Once shipped a name is a compatibility surface — a user's config names
	// it — so it can be added to but never renamed. TestActionVocabulary_Golden
	// pins the whole set for that reason.
	Action string
	// DocOnly marks a documented-only key: it appears in generated help but
	// never enters GlobalKeyStringsMap (its keys are handled outside the
	// dispatch map — before it, or in the attach layer).
	DocOnly bool
	Layer   Layer
	Binding key.Binding
}

// Registry is the single source of truth for the keymap. The dispatch map
// (GlobalKeyStringsMap) and the help map (GlobalKeyBindings) are derived from
// it below; the cheatsheet layout (help_layout.go) and the hint bar render
// from those. Adding a key here without documenting it — or documenting a key
// that doesn't exist here — fails the drift guards in registry_test.go and
// help_layout_test.go.
//
// KeyScreensaver is deliberately absent: the easter egg's exclusion from every
// help surface is structural (see keys.go), and its dispatch line is appended
// by hand in the derivation below.
var Registry = []Entry{
	{Name: KeyUp, Action: "up", Binding: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	)},
	{Name: KeyDown, Action: "down", Binding: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	)},
	{Name: KeyShiftUp, Action: "scroll_up", Binding: key.NewBinding(
		key.WithKeys("shift+up"),
		key.WithHelp("shift-↑", "scroll"),
	)},
	{Name: KeyShiftDown, Action: "scroll_down", Binding: key.NewBinding(
		key.WithKeys("shift+down"),
		key.WithHelp("shift-↓", "scroll"),
	)},
	{Name: KeyNextUnread, Action: "next_unread", Binding: key.NewBinding(
		key.WithKeys("u"),
		key.WithHelp("u", "next unread"),
	)},
	{Name: KeyNextNeedsInput, Action: "next_blocked", Binding: key.NewBinding(
		key.WithKeys("b"),
		key.WithHelp("b", "next blocked"),
	)},
	{Name: KeyEnter, Action: "open", Binding: key.NewBinding(
		key.WithKeys("enter", "o"),
		key.WithHelp("↵/o", "open"),
	)},
	{Name: KeyNew, Action: "new", Binding: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "new"),
	)},
	{Name: KeySmartDispatch, Action: "smart_new", Binding: key.NewBinding(
		key.WithKeys("i"),
		key.WithHelp("i", "smart new"),
	)},
	{Name: KeyKill, Action: "kill", Layer: LayerBoth, Binding: key.NewBinding(
		key.WithKeys("ctrl+x"),
		key.WithHelp("ctrl-x", "kill"),
	)},
	{Name: KeyRename, Action: "rename", Binding: key.NewBinding(
		key.WithKeys("R"),
		key.WithHelp("R", "rename"),
	)},
	{Name: KeyAutoName, Action: "auto_name", Binding: key.NewBinding(
		key.WithKeys("A"),
		key.WithHelp("A", "auto-name"),
	)},
	{Name: KeyMute, Action: "mute", Binding: key.NewBinding(
		key.WithKeys("M"),
		key.WithHelp("M", "mute notifications"),
	)},
	{Name: KeyQuickSend, Action: "send", Binding: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "send"),
	)},
	{Name: KeyDiffComment, Action: "diff_comment", Binding: key.NewBinding(
		key.WithKeys("C"),
		key.WithHelp("C", "comment on a diff line"),
	)},
	{Name: KeyQueue, Action: "queue", Binding: key.NewBinding(
		key.WithKeys("Q"),
		key.WithHelp("Q", "manage queued prompts"),
	)},
	{Name: KeyCmdLog, Action: "command_log", Binding: key.NewBinding(
		key.WithKeys("L"),
		key.WithHelp("L", "command log"),
	)},
	{Name: KeyHelp, Action: "help", Binding: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	)},
	{Name: KeyQuit, Action: "quit", Binding: key.NewBinding(
		key.WithKeys("q"),
		key.WithHelp("q", "quit"),
	)},
	{Name: KeySubmit, Action: "push_branch", Binding: key.NewBinding(
		key.WithKeys("P"),
		key.WithHelp("P", "push branch"),
	)},
	{Name: KeyCreate, Action: "create_pr", Binding: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "create PR"),
	)},
	{Name: KeyMerge, Action: "merge_pr", Binding: key.NewBinding(
		key.WithKeys("m"),
		key.WithHelp("m", "merge PR"),
	)},
	{Name: KeyOpenPR, Action: "open_pr", Binding: key.NewBinding(
		key.WithKeys("w"),
		key.WithHelp("w", "open PR"),
	)},
	{Name: KeyPrompt, Action: "new_pick_project", Binding: key.NewBinding(
		key.WithKeys("N"),
		key.WithHelp("N", "new (pick project)"),
	)},
	{Name: KeyPause, Action: "pause", Binding: key.NewBinding(
		key.WithKeys("p"),
		key.WithHelp("p", "pause"),
	)},
	{Name: KeyPauseAll, Action: "pause_all", Binding: key.NewBinding(
		key.WithKeys("ctrl+p"),
		key.WithHelp("ctrl-p", "pause all"),
	)},
	{Name: KeyTab, Action: "next_tab", Binding: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch tab"),
	)},
	{Name: KeyShiftTab, Action: "prev_tab", Binding: key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift-tab", "prev tab"),
	)},
	{Name: KeyResume, Action: "resume", Binding: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "resume"),
	)},
	{Name: KeyResumeAll, Action: "resume_all", Binding: key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl-r", "resume all"),
	)},
	{Name: KeyUndoKill, Action: "undo_kill", Binding: key.NewBinding(
		key.WithKeys("U"),
		key.WithHelp("U", "undo the last kill"),
	)},
	{Name: KeyMultiSelect, Action: "multi_select", Binding: key.NewBinding(
		key.WithKeys("v"),
		key.WithHelp("v", "multi-select"),
	)},
	{Name: KeyToggleMark, Action: "toggle_mark", Binding: key.NewBinding(
		// "space", not " ". Bubble Tea v1 reported the space bar from String() as a
		// literal space; v2 names it. Dispatch is keyed on that string
		// (GlobalKeyStringsMap), so leaving " " here would compile, pass review, and
		// ship the mark key dead — nothing in the suite asserts that a registered key
		// reaches a dispatch case.
		key.WithKeys("space"),
		key.WithHelp("space", "mark/unmark"),
	)},
	{Name: KeyMoveUp, Action: "move_up", Binding: key.NewBinding(
		key.WithKeys("K"),
		key.WithHelp("K", "move up"),
	)},
	{Name: KeyMoveDown, Action: "move_down", Binding: key.NewBinding(
		key.WithKeys("J"),
		key.WithHelp("J", "move down"),
	)},
	{Name: KeyMoveGroupUp, Action: "move_group_up", Binding: key.NewBinding(
		key.WithKeys("{"),
		key.WithHelp("{", "move group up"),
	)},
	{Name: KeyMoveGroupDown, Action: "move_group_down", Binding: key.NewBinding(
		key.WithKeys("}"),
		key.WithHelp("}", "move group down"),
	)},
	// The unit here is the account *cluster* (a repo whose sessions span
	// accounts still renders as one cluster) — #357 was this text saying
	// "account"; the ladder vocabulary is pinned by registry_test.go.
	{Name: KeyMoveAccountUp, Action: "move_account_up", Binding: key.NewBinding(
		key.WithKeys("["),
		key.WithHelp("[", "move account cluster up"),
	)},
	{Name: KeyMoveAccountDown, Action: "move_account_down", Binding: key.NewBinding(
		key.WithKeys("]"),
		key.WithHelp("]", "move account cluster down"),
	)},
	{Name: KeyCollapse, Action: "collapse_group", Binding: key.NewBinding(
		key.WithKeys("left"),
		key.WithHelp("←", "collapse group"),
	)},
	{Name: KeyExpand, Action: "expand_group", Binding: key.NewBinding(
		key.WithKeys("right"),
		key.WithHelp("→", "expand group"),
	)},
	{Name: KeyCollapseAll, Action: "collapse_all", Binding: key.NewBinding(
		key.WithKeys("Z"),
		key.WithHelp("Z", "collapse/expand all"),
	)},
	{Name: KeyFilter, Action: "filter", Binding: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "filter sessions"),
	)},
	{Name: KeyCopyBranch, Action: "copy_branch", Binding: key.NewBinding(
		key.WithKeys("y"),
		key.WithHelp("y", "copy branch name"),
	)},
	{Name: KeyCopyContent, Action: "copy_content", Binding: key.NewBinding(
		key.WithKeys("Y"),
		key.WithHelp("Y", "copy pane/diff"),
	)},
	{Name: KeyShrinkList, Action: "shrink_list", Binding: key.NewBinding(
		key.WithKeys("<"),
		key.WithHelp("<", "shrink list"),
	)},
	{Name: KeyGrowList, Action: "grow_list", Binding: key.NewBinding(
		key.WithKeys(">"),
		key.WithHelp(">", "grow list"),
	)},
	// Backslash: a free, unshifted key (a reviewer may prefer a mnemonic — see
	// the PR). The label reads like a leaning divider between the two panes it
	// re-proportions.
	{Name: KeyLayoutPreset, Action: "layout_preset", Binding: key.NewBinding(
		key.WithKeys("\\"),
		key.WithHelp("\\", "cycle layout"),
	)},
	{Name: KeyTabPreview, Action: "tab_preview", Binding: key.NewBinding(
		key.WithKeys("1"),
		key.WithHelp("1", "preview tab"),
	)},
	{Name: KeyTabDiff, Action: "tab_diff", Binding: key.NewBinding(
		key.WithKeys("2"),
		key.WithHelp("2", "diff tab"),
	)},
	{Name: KeyTabTerminal, Action: "tab_terminal", Binding: key.NewBinding(
		key.WithKeys("3"),
		key.WithHelp("3", "terminal tab"),
	)},
	{Name: KeySettings, Action: "settings", Binding: key.NewBinding(
		key.WithKeys(","),
		key.WithHelp(",", "settings"),
	)},
	{Name: KeyAccounts, Action: "accounts", Binding: key.NewBinding(
		key.WithKeys("@"),
		key.WithHelp("@", "accounts"),
	)},
	{Name: KeyCommandPalette, Action: "command_palette", Binding: key.NewBinding(
		key.WithKeys("ctrl+k"),
		key.WithHelp("ctrl-k", "command palette"),
	)},
	{Name: KeyCustomCommands, Action: "custom_commands", Binding: key.NewBinding(
		key.WithKeys("!"),
		key.WithHelp("!", "custom commands"),
	)},
	{Name: KeyAttachToggle, Action: "attach_toggle", Layer: LayerBoth, Binding: key.NewBinding(
		key.WithKeys("ctrl+q"),
		key.WithHelp("ctrl-q", "attach/detach"),
	)},
	{Name: KeyHints, Action: "hints", Binding: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "copy/open from screen"),
	)},
	{Name: KeyApprove, Action: "approve", Binding: key.NewBinding(
		key.WithKeys("a"),
		key.WithHelp("a", "approve"),
	)},
	{Name: KeyRunCommand, Action: "run_command", Binding: key.NewBinding(
		key.WithKeys("d"),
		key.WithHelp("d", "run/stop dev command"),
	)},

	// Documented-only keys: real keys the TUI's dispatch map never sees, kept
	// here so generated help can reference them (see keys.go for each one's
	// story).
	{Name: KeySessionCycle, DocOnly: true, Layer: LayerAttached, Binding: key.NewBinding(
		key.WithKeys("ctrl+pgup", "ctrl+pgdown"),
		key.WithHelp("ctrl-pgup/pgdn", "cycle sessions"),
	)},
	{Name: KeyEscape, DocOnly: true, Binding: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "exit scroll / clear filter"),
	)},
	{Name: KeyRedraw, DocOnly: true, Binding: key.NewBinding(
		key.WithKeys("ctrl+l"),
		key.WithHelp("ctrl-l", "redraw"),
	)},
}

// GlobalKeyBindings maps every registered action to its binding — the source
// of the hint bar's and the cheatsheet's key labels and help text. Derived from
// Registry.
//
// Written once by Apply (override.go), before tea.NewProgram; read-only
// thereafter. That is the contract the readers depend on: every one of them is
// on the render or update path and takes no lock, which is safe only because
// nothing writes this map while the program is running.
var GlobalKeyBindings = func() map[KeyName]key.Binding {
	m := make(map[KeyName]key.Binding, len(Registry))
	for _, e := range Registry {
		m[e.Name] = e.Binding
	}
	return m
}()

// layers maps each registered action to its Layer, for LayerOf. Derived from
// Registry, and genuinely immutable: an override moves an action's keys, never
// which input layer honors it.
var layers = func() map[KeyName]Layer {
	m := make(map[KeyName]Layer, len(Registry))
	for _, e := range Registry {
		m[e.Name] = e.Layer
	}
	return m
}()

// LayerOf reports which input layer honors the named action's key. Help
// generators use it to annotate attached-layer keys truthfully.
func LayerOf(name KeyName) Layer {
	return layers[name]
}

// GlobalKeyStringsMap maps terminal key strings to actions for the Update
// loop's dispatch. Derived from the Registry entries' WithKeys (documented-only
// entries excluded).
//
// Written once by Apply (override.go), before tea.NewProgram; read-only
// thereafter — see GlobalKeyBindings for why that matters.
var GlobalKeyStringsMap = func() map[string]KeyName {
	m := make(map[string]KeyName, len(Registry))
	for _, e := range Registry {
		if e.DocOnly {
			continue
		}
		for _, s := range e.Binding.Keys() {
			m[s] = e.Name
		}
	}
	// The screensaver easter egg dispatches without a Registry entry: its
	// absence from the registry (not a flag) is what keeps it out of every
	// generated help surface, so its dispatch line lives here by hand.
	m["`"] = KeyScreensaver
	return m
}()

// The mode hint tables are the modal gesture vocabularies the bar teaches while
// a mode owns the keyboard (filter / hint / multi-select / diff-comment). They
// are part of the registry — the bar's reverse drift guard walks them — but never
// enter dispatch: each mode's handler routes its own keys, and a label here may
// be a range ("a–z") that no single dispatch string could carry.
//
// Functions rather than vars, for the reason HelpGroups is: the modes resolve
// their lifecycle and movement keys through GlobalKeyStringsMap, so a rebind
// moves what the mode does — and a table frozen at package-init time would go on
// teaching the old letters. That is not hypothetical: multi-select's label was
// the literal "p/r/x" while its handler looked pause and resume up in the
// registry, so rebinding pause left the bar advertising a key that did nothing.
//
// Order within each table is deliberate: actions first, so a narrow terminal's
// truncation drops the tail cue, never the verbs.

// FilterModeHints teaches the incremental-filter bar (StateFilter). Its two keys
// are reserved, so they need no lookup.
func FilterModeHints() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "accept")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear")),
	}
}

// HintModeHints teaches hint (fingers) mode's three gestures (StateHints). The
// two ranges are hints/assign.go's alphabet, not keymap actions.
func HintModeHints() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithHelp("a–z", "copy")),
		key.NewBinding(key.WithHelp("A–Z", "copy + open")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}
}

// VisualModeHints teaches multi-select mode's mark/act/exit gestures
// (StateVisual).
//
// x is a literal because it is the mode's own key: handleMultiSelectState
// answers it before the dispatch lookup, so no Registry entry owns it and no
// override moves it. pause and resume are looked up, because they are.
//
// The label goes through Label, not through the dispatch spellings: WithHelp is
// the display slot (see label.go), so concatenating PrimaryKey printed "alt+p/r/x"
// on the bar while the cheatsheet's own multi-select row printed "alt-p/r/x" for
// the same keys. An unbound action drops out of both slots rather than joining as
// an empty segment, which rendered a leading slash where a key should be.
func VisualModeHints() []key.Binding {
	marked := make([]string, 0, 3)
	for _, name := range []KeyName{KeyPause, KeyResume} {
		if k := PrimaryKey(name); k != "" {
			marked = append(marked, k)
		}
	}
	marked = append(marked, "x")
	return []key.Binding{
		key.NewBinding(key.WithKeys(PrimaryKey(KeyToggleMark)),
			key.WithHelp(LabelOf(KeyToggleMark), "mark")),
		key.NewBinding(key.WithKeys(marked...),
			key.WithHelp(Label(marked), "pause/resume/kill marked")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "exit")),
	}
}

// DiffCommentModeHints teaches diff-comment mode's move/comment/exit gestures
// (StateDiffComment): the line cursor steps code lines, enter composes the
// comment, esc leaves.
//
// The move and extend labels name the keys the handler actually resolves
// (app/app_diffcomment.go). Extend also accepts the shift arrows as aliases; the
// bar names the letter pair only, because a bar that lists every alias is a bar
// that truncates on an 80-column terminal.
func DiffCommentModeHints() []key.Binding {
	move := Label(append(keysOf(KeyUp), keysOf(KeyDown)...))
	extend := Label(append(keysOf(KeyMoveUp), keysOf(KeyMoveDown)...))
	return []key.Binding{
		key.NewBinding(key.WithHelp(move, "move")),
		key.NewBinding(key.WithHelp(extend, "extend")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "comment")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "exit")),
	}
}

// keysOf is an action's bound key strings, empty when the user unbound it.
func keysOf(name KeyName) []string {
	return GlobalKeyBindings[name].Keys()
}
