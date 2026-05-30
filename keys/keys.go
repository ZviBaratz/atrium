package keys

import (
	"github.com/charmbracelet/bubbles/key"
)

type KeyName int

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

	KeyTab        // Tab is a special keybinding for switching between panes.
	KeyShiftTab   // ShiftTab cycles between panes in reverse order.
	KeySubmitName // SubmitName is a special keybinding for submitting the name of a new instance.

	KeyCheckout
	KeyResume
	KeyPrompt // New key for entering a prompt
	KeyHelp   // Key for showing help screen

	// Diff keybindings
	KeyShiftUp
	KeyShiftDown

	// Reorder keybindings
	KeyMoveUp
	KeyMoveDown

	// Whole-group reorder keybindings
	KeyMoveGroupUp
	KeyMoveGroupDown

	// Group collapse keybindings
	KeyCollapseToggle
	KeyCollapseAll

	KeyRename // Rename the selected session's display label

	KeyQuickSend // Open a compose box to send a message to the selected session without attaching

	KeyAutoName // Auto-generate a display name for the selected session via claude

	KeyFilter // Enter incremental filter mode to narrow the session list

	KeyCopyBranch // Copy the selected session's branch name to the clipboard

	// Pane resize keybindings: grow/shrink the session list relative to the preview.
	KeyShrinkList
	KeyGrowList
)

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
	" ":          KeyCollapseToggle,
	"Z":          KeyCollapseAll,
	"N":          KeyPrompt,
	"enter":      KeyEnter,
	"o":          KeyEnter,
	"n":          KeyNew,
	"D":          KeyKill,
	"R":          KeyRename,
	"A":          KeyAutoName,
	"right":      KeyQuickSend,
	"y":          KeyCopyBranch,
	"q":          KeyQuit,
	"tab":        KeyTab,
	"shift+tab":  KeyShiftTab,
	"c":          KeyCheckout,
	"r":          KeyResume,
	"p":          KeySubmit,
	"?":          KeyHelp,
	"/":          KeyFilter,
	"<":          KeyShrinkList,
	">":          KeyGrowList,
}

// GlobalkeyBindings is a global, immutable map of KeyName tot keybinding.
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
		key.WithKeys("D"),
		key.WithHelp("D", "kill"),
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
		key.WithKeys("right"),
		key.WithHelp("→", "send"),
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
		key.WithKeys("p"),
		key.WithHelp("p", "push branch"),
	),
	KeyPrompt: key.NewBinding(
		key.WithKeys("N"),
		key.WithHelp("N", "new with prompt"),
	),
	KeyCheckout: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "checkout"),
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
	KeyCollapseToggle: key.NewBinding(
		key.WithKeys(" "),
		key.WithHelp("space", "collapse/expand group"),
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

	// -- Special keybindings --

	KeySubmitName: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "submit name"),
	),
}
