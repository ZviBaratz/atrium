package keys

import (
	"github.com/charmbracelet/bubbles/key"
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
	{Name: KeyUp, Binding: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	)},
	{Name: KeyDown, Binding: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	)},
	{Name: KeyShiftUp, Binding: key.NewBinding(
		key.WithKeys("shift+up"),
		key.WithHelp("shift-↑", "scroll"),
	)},
	{Name: KeyShiftDown, Binding: key.NewBinding(
		key.WithKeys("shift+down"),
		key.WithHelp("shift-↓", "scroll"),
	)},
	{Name: KeyNextUnread, Binding: key.NewBinding(
		key.WithKeys("u"),
		key.WithHelp("u", "next unread"),
	)},
	{Name: KeyNextNeedsInput, Binding: key.NewBinding(
		key.WithKeys("b"),
		key.WithHelp("b", "next blocked"),
	)},
	{Name: KeyEnter, Binding: key.NewBinding(
		key.WithKeys("enter", "o"),
		key.WithHelp("↵/o", "open"),
	)},
	{Name: KeyNew, Binding: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "new"),
	)},
	{Name: KeySmartDispatch, Binding: key.NewBinding(
		key.WithKeys("i"),
		key.WithHelp("i", "smart new"),
	)},
	{Name: KeyKill, Layer: LayerBoth, Binding: key.NewBinding(
		key.WithKeys(KillKey),
		key.WithHelp("ctrl-x", "kill"),
	)},
	{Name: KeyRename, Binding: key.NewBinding(
		key.WithKeys("R"),
		key.WithHelp("R", "rename"),
	)},
	{Name: KeyAutoName, Binding: key.NewBinding(
		key.WithKeys("A"),
		key.WithHelp("A", "auto-name"),
	)},
	{Name: KeyQuickSend, Binding: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "send"),
	)},
	{Name: KeyDiffComment, Binding: key.NewBinding(
		key.WithKeys("C"),
		key.WithHelp("C", "comment on a diff line"),
	)},
	{Name: KeyQueue, Binding: key.NewBinding(
		key.WithKeys("Q"),
		key.WithHelp("Q", "manage queued prompts"),
	)},
	{Name: KeyCmdLog, Binding: key.NewBinding(
		key.WithKeys("L"),
		key.WithHelp("L", "command log"),
	)},
	{Name: KeyHelp, Binding: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	)},
	{Name: KeyQuit, Binding: key.NewBinding(
		key.WithKeys("q"),
		key.WithHelp("q", "quit"),
	)},
	{Name: KeySubmit, Binding: key.NewBinding(
		key.WithKeys("P"),
		key.WithHelp("P", "push branch"),
	)},
	{Name: KeyCreate, Binding: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "create PR"),
	)},
	{Name: KeyMerge, Binding: key.NewBinding(
		key.WithKeys("m"),
		key.WithHelp("m", "merge PR"),
	)},
	{Name: KeyOpenPR, Binding: key.NewBinding(
		key.WithKeys("w"),
		key.WithHelp("w", "open PR"),
	)},
	{Name: KeyPrompt, Binding: key.NewBinding(
		key.WithKeys("N"),
		key.WithHelp("N", "new (pick project)"),
	)},
	{Name: KeyPause, Binding: key.NewBinding(
		key.WithKeys("p"),
		key.WithHelp("p", "pause"),
	)},
	{Name: KeyPauseAll, Binding: key.NewBinding(
		key.WithKeys("ctrl+p"),
		key.WithHelp("ctrl-p", "pause all"),
	)},
	{Name: KeyTab, Binding: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch tab"),
	)},
	{Name: KeyShiftTab, Binding: key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift-tab", "prev tab"),
	)},
	{Name: KeyResume, Binding: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "resume"),
	)},
	{Name: KeyResumeAll, Binding: key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl-r", "resume all"),
	)},
	{Name: KeyMultiSelect, Binding: key.NewBinding(
		key.WithKeys("v"),
		key.WithHelp("v", "multi-select"),
	)},
	{Name: KeyToggleMark, Binding: key.NewBinding(
		key.WithKeys(" "),
		key.WithHelp("space", "mark/unmark"),
	)},
	{Name: KeyMoveUp, Binding: key.NewBinding(
		key.WithKeys("K"),
		key.WithHelp("K", "move up"),
	)},
	{Name: KeyMoveDown, Binding: key.NewBinding(
		key.WithKeys("J"),
		key.WithHelp("J", "move down"),
	)},
	{Name: KeyMoveGroupUp, Binding: key.NewBinding(
		key.WithKeys("{"),
		key.WithHelp("{", "move group up"),
	)},
	{Name: KeyMoveGroupDown, Binding: key.NewBinding(
		key.WithKeys("}"),
		key.WithHelp("}", "move group down"),
	)},
	// The unit here is the account *cluster* (a repo whose sessions span
	// accounts still renders as one cluster) — #357 was this text saying
	// "account"; the ladder vocabulary is pinned by registry_test.go.
	{Name: KeyMoveAccountUp, Binding: key.NewBinding(
		key.WithKeys("["),
		key.WithHelp("[", "move account cluster up"),
	)},
	{Name: KeyMoveAccountDown, Binding: key.NewBinding(
		key.WithKeys("]"),
		key.WithHelp("]", "move account cluster down"),
	)},
	{Name: KeyCollapse, Binding: key.NewBinding(
		key.WithKeys("left"),
		key.WithHelp("←", "collapse group"),
	)},
	{Name: KeyExpand, Binding: key.NewBinding(
		key.WithKeys("right"),
		key.WithHelp("→", "expand group"),
	)},
	{Name: KeyCollapseAll, Binding: key.NewBinding(
		key.WithKeys("Z"),
		key.WithHelp("Z", "collapse/expand all"),
	)},
	{Name: KeyFilter, Binding: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "filter sessions"),
	)},
	{Name: KeyCopyBranch, Binding: key.NewBinding(
		key.WithKeys("y"),
		key.WithHelp("y", "copy branch name"),
	)},
	{Name: KeyShrinkList, Binding: key.NewBinding(
		key.WithKeys("<"),
		key.WithHelp("<", "shrink list"),
	)},
	{Name: KeyGrowList, Binding: key.NewBinding(
		key.WithKeys(">"),
		key.WithHelp(">", "grow list"),
	)},
	// Backslash: a free, unshifted key (a reviewer may prefer a mnemonic — see
	// the PR). The label reads like a leaning divider between the two panes it
	// re-proportions.
	{Name: KeyLayoutPreset, Binding: key.NewBinding(
		key.WithKeys("\\"),
		key.WithHelp("\\", "cycle layout"),
	)},
	{Name: KeyTabPreview, Binding: key.NewBinding(
		key.WithKeys("1"),
		key.WithHelp("1", "preview tab"),
	)},
	{Name: KeyTabDiff, Binding: key.NewBinding(
		key.WithKeys("2"),
		key.WithHelp("2", "diff tab"),
	)},
	{Name: KeyTabTerminal, Binding: key.NewBinding(
		key.WithKeys("3"),
		key.WithHelp("3", "terminal tab"),
	)},
	{Name: KeySettings, Binding: key.NewBinding(
		key.WithKeys(","),
		key.WithHelp(",", "settings"),
	)},
	{Name: KeyAccounts, Binding: key.NewBinding(
		key.WithKeys("@"),
		key.WithHelp("@", "accounts"),
	)},
	{Name: KeyAttachToggle, Layer: LayerBoth, Binding: key.NewBinding(
		key.WithKeys("ctrl+q"),
		key.WithHelp("ctrl-q", "attach/detach"),
	)},
	{Name: KeyHints, Binding: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "copy/open from screen"),
	)},
	{Name: KeyApprove, Binding: key.NewBinding(
		key.WithKeys("a"),
		key.WithHelp("a", "approve"),
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
// of the hint bar's and the cheatsheet's key labels and help text. Derived
// from Registry; immutable after init.
var GlobalKeyBindings = func() map[KeyName]key.Binding {
	m := make(map[KeyName]key.Binding, len(Registry))
	for _, e := range Registry {
		m[e.Name] = e.Binding
	}
	return m
}()

// layers maps each registered action to its Layer, for LayerOf. Derived from
// Registry; immutable after init.
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
// loop's dispatch. Derived from the Registry entries' WithKeys (documented-
// only entries excluded); immutable after init.
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

// The mode hint tables are the modal gesture vocabularies the bar teaches
// while a mode owns the keyboard (filter / hint / multi-select). They are part
// of the registry — the bar's reverse drift guard walks them — but never enter
// dispatch: each mode's handler routes its own keys, and a label here may be a
// range ("a–z") or a compound ("p/r/x") that no single dispatch string could
// carry. Order within each table is deliberate: actions first, so a narrow
// terminal's truncation drops the tail cue, never the verbs.
var (
	// FilterModeHints teaches the incremental-filter bar (StateFilter).
	FilterModeHints = []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "accept")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear")),
	}
	// HintModeHints teaches hint (fingers) mode's three gestures (StateHints).
	HintModeHints = []key.Binding{
		key.NewBinding(key.WithHelp("a–z", "copy")),
		key.NewBinding(key.WithHelp("A–Z", "copy + open")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}
	// VisualModeHints teaches multi-select mode's mark/act/exit gestures
	// (StateVisual).
	VisualModeHints = []key.Binding{
		key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "mark")),
		key.NewBinding(key.WithKeys("p", "r", "x"), key.WithHelp("p/r/x", "pause/resume/kill marked")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "exit")),
	}
	// DiffCommentModeHints teaches diff-comment mode's move/comment/exit gestures
	// (StateDiffComment): the line cursor steps code lines, enter composes the
	// comment, esc leaves.
	DiffCommentModeHints = []key.Binding{
		key.NewBinding(key.WithHelp("↑↓/jk", "move")),
		key.NewBinding(key.WithHelp("shift+↑↓/JK", "extend")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "comment")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "exit")),
	}
)
