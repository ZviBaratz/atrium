// Package keys defines the logical key actions of the TUI and the global maps
// that translate terminal key strings into those actions and into displayable
// help bindings.
package keys

import (
	"github.com/charmbracelet/bubbles/key"
)

// KeyName identifies a logical key action in the TUI. The home model switches
// on KeyName rather than raw key strings, so a rebind only touches the maps in
// this package.
type KeyName int

// The logical key actions. Their bindings live in GlobalKeyStringsMap (string
// → action) and GlobalkeyBindings (action → help entry).
const (
	KeyUp KeyName = iota
	KeyDown
	KeyEnter
	KeyNew
	KeyKill
	KeyQuit
	KeyReview
	KeyPush
	KeySubmit
	KeyCreate // Open a pull request for the pushed branch via gh
	KeyMerge  // Squash-merge the selected session's pull request via gh
	KeyOpenPR // Open the selected session's pull request in the browser via gh

	KeyTab      // Tab is a special keybinding for switching between panes.
	KeyShiftTab // ShiftTab cycles between panes in reverse order.

	KeyPause // Commit changes and pause the session, freeing its worktree
	KeyResume
	KeyPrompt // Open the new-session form focused on the project picker
	KeyHelp   // Key for showing help screen

	// KeyShiftUp and KeyShiftDown scroll the diff/preview pane.
	KeyShiftUp
	KeyShiftDown

	// KeyMoveUp and KeyMoveDown reorder the selected session within its group.
	KeyMoveUp
	KeyMoveDown

	// KeyMoveGroupUp and KeyMoveGroupDown reorder a whole repo group.
	KeyMoveGroupUp
	KeyMoveGroupDown

	// KeyCollapse and KeyExpand fold/unfold the selected repo group (tree-view
	// style); KeyCollapseAll folds/unfolds every group at once.
	KeyCollapse
	KeyExpand
	KeyCollapseAll

	KeyRename // Rename the selected session's display label

	KeyQuickSend // Open a compose box to send a message to the selected session without attaching

	KeyAutoName // Auto-generate a display name for the selected session via claude

	KeyFilter // Enter incremental filter mode to narrow the session list

	KeyCopyBranch // Copy the selected session's branch name to the clipboard

	// KeyShrinkList and KeyGrowList resize the session list relative to the
	// preview pane.
	KeyShrinkList
	KeyGrowList

	// KeyTabPreview/KeyTabDiff/KeyTabTerminal jump straight to a tab by number,
	// complementing Tab/Shift+Tab cycling.
	KeyTabPreview
	KeyTabDiff
	KeyTabTerminal

	KeySettings // Open the settings panel to view and edit the configuration

	// KeyAttachToggle mirrors the in-session detach key: on the list it attaches
	// the selected session, making ctrl+q a symmetric attach/detach toggle.
	KeyAttachToggle

	// KeyHints enters hint (fingers) mode: overlay copy/open hint labels on
	// the preview pane's visible matches (URLs, paths, SHAs, …).
	KeyHints

	// KeyApprove taps Enter at the selected session's visible prompt (tool
	// permission, plan approval) without attaching.
	KeyApprove
)

// KillKey is the chord that triggers a kill from the session list. It mirrors the
// in-session kill byte (ctrlX, session/tmux/tmux.go) so the same key tears a session
// down whether you're on the list or attached to it.
const KillKey = "ctrl+x"

// GlobalKeyStringsMap is a global, immutable map string to keybinding.
var GlobalKeyStringsMap = map[string]KeyName{
	"up":         KeyUp,
	"k":          KeyUp,
	"down":       KeyDown,
	"j":          KeyDown,
	"shift+up":   KeyShiftUp,
	"shift+down": KeyShiftDown,
	"J":          KeyMoveDown,
	"K":          KeyMoveUp,
	"{":          KeyMoveGroupUp,
	"}":          KeyMoveGroupDown,
	"left":       KeyCollapse,
	"right":      KeyExpand,
	"Z":          KeyCollapseAll,
	"N":          KeyPrompt,
	"enter":      KeyEnter,
	"o":          KeyEnter,
	"n":          KeyNew,
	KillKey:      KeyKill,
	"R":          KeyRename,
	"A":          KeyAutoName,
	"s":          KeyQuickSend,
	"y":          KeyCopyBranch,
	"q":          KeyQuit,
	"tab":        KeyTab,
	"shift+tab":  KeyShiftTab,
	"p":          KeyPause,
	"r":          KeyResume,
	"P":          KeySubmit,
	"c":          KeyCreate,
	"m":          KeyMerge,
	"w":          KeyOpenPR,
	"?":          KeyHelp,
	"/":          KeyFilter,
	"<":          KeyShrinkList,
	">":          KeyGrowList,
	"1":          KeyTabPreview,
	"2":          KeyTabDiff,
	"3":          KeyTabTerminal,
	",":          KeySettings,
	"ctrl+q":     KeyAttachToggle,
	"f":          KeyHints,
	"a":          KeyApprove,
}

// GlobalkeyBindings is a global, immutable map of KeyName to keybinding.
var GlobalkeyBindings = map[KeyName]key.Binding{
	KeyUp: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	KeyDown: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	KeyShiftUp: key.NewBinding(
		key.WithKeys("shift+up"),
		key.WithHelp("shift+↑", "scroll"),
	),
	KeyShiftDown: key.NewBinding(
		key.WithKeys("shift+down"),
		key.WithHelp("shift+↓", "scroll"),
	),
	KeyEnter: key.NewBinding(
		key.WithKeys("enter", "o"),
		key.WithHelp("↵/o", "open"),
	),
	KeyNew: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "new"),
	),
	KeyKill: key.NewBinding(
		key.WithKeys(KillKey),
		key.WithHelp("ctrl-x", "kill"),
	),
	KeyRename: key.NewBinding(
		key.WithKeys("R"),
		key.WithHelp("R", "rename"),
	),
	KeyAutoName: key.NewBinding(
		key.WithKeys("A"),
		key.WithHelp("A", "auto-name"),
	),
	KeyQuickSend: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "send"),
	),
	KeyHelp: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	KeyQuit: key.NewBinding(
		key.WithKeys("q"),
		key.WithHelp("q", "quit"),
	),
	KeySubmit: key.NewBinding(
		key.WithKeys("P"),
		key.WithHelp("P", "push branch"),
	),
	KeyCreate: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "create PR"),
	),
	KeyMerge: key.NewBinding(
		key.WithKeys("m"),
		key.WithHelp("m", "merge PR"),
	),
	KeyOpenPR: key.NewBinding(
		key.WithKeys("w"),
		key.WithHelp("w", "open PR"),
	),
	KeyPrompt: key.NewBinding(
		key.WithKeys("N"),
		key.WithHelp("N", "new (pick project)"),
	),
	KeyPause: key.NewBinding(
		key.WithKeys("p"),
		key.WithHelp("p", "pause"),
	),
	KeyTab: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch tab"),
	),
	KeyShiftTab: key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift+tab", "prev tab"),
	),
	KeyResume: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "resume"),
	),

	KeyMoveUp: key.NewBinding(
		key.WithKeys("K"),
		key.WithHelp("K", "move up"),
	),
	KeyMoveDown: key.NewBinding(
		key.WithKeys("J"),
		key.WithHelp("J", "move down"),
	),

	KeyMoveGroupUp: key.NewBinding(
		key.WithKeys("{"),
		key.WithHelp("{", "move group up"),
	),
	KeyMoveGroupDown: key.NewBinding(
		key.WithKeys("}"),
		key.WithHelp("}", "move group down"),
	),
	KeyCollapse: key.NewBinding(
		key.WithKeys("left"),
		key.WithHelp("←", "collapse group"),
	),
	KeyExpand: key.NewBinding(
		key.WithKeys("right"),
		key.WithHelp("→", "expand group"),
	),
	KeyCollapseAll: key.NewBinding(
		key.WithKeys("Z"),
		key.WithHelp("Z", "collapse/expand all"),
	),
	KeyFilter: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "filter sessions"),
	),
	KeyCopyBranch: key.NewBinding(
		key.WithKeys("y"),
		key.WithHelp("y", "copy branch name"),
	),
	KeyShrinkList: key.NewBinding(
		key.WithKeys("<"),
		key.WithHelp("<", "shrink list"),
	),
	KeyGrowList: key.NewBinding(
		key.WithKeys(">"),
		key.WithHelp(">", "grow list"),
	),
	KeyTabPreview: key.NewBinding(
		key.WithKeys("1"),
		key.WithHelp("1", "preview tab"),
	),
	KeyTabDiff: key.NewBinding(
		key.WithKeys("2"),
		key.WithHelp("2", "diff tab"),
	),
	KeyTabTerminal: key.NewBinding(
		key.WithKeys("3"),
		key.WithHelp("3", "terminal tab"),
	),
	KeySettings: key.NewBinding(
		key.WithKeys(","),
		key.WithHelp(",", "settings"),
	),
	KeyAttachToggle: key.NewBinding(
		key.WithKeys("ctrl+q"),
		key.WithHelp("ctrl-q", "attach/detach"),
	),
	KeyHints: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "copy/open from screen"),
	),
	KeyApprove: key.NewBinding(
		key.WithKeys("a"),
		key.WithHelp("a", "approve"),
	),
}
