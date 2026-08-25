package app

// The surface registry (#801): one table for the per-state readers that used to
// be five hand-written enumerations of the state enum. viewContent (render),
// handleKeyPress (keys), menuVisible (barVisible), updateHandleWindowSizeEvent
// (size) and handlePaste (paste) each look the current state up here instead of
// switching over the enum, so adding a state means writing one entry instead of
// five arms.
//
// Selection plumbing is all that merged. Every field stays the state's own
// judgment expressed as data — a bare surface is nil/zero fields, never a
// forced modal template — the same move the repo made with dispatchExempt and
// keys.Effect: per-site judgment becomes per-site data with a completeness
// guard (TestEverySurfaceSpecIsComplete).

import (
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/ui/overlay"

	tea "charm.land/bubbletea/v2"
)

// surfaceSpec describes one UI state's surface: how it renders over the frame,
// who handles its keys and pastes, whether the hint bar keeps its row, how its
// overlay is sized on resize, and which golden fixture renders it. The zero
// value is invalid as a table entry on purpose: st and fixture make a forgotten
// slot fail TestEverySurfaceSpecIsComplete rather than default into a
// bar-hidden, paste-inert surface.
type surfaceSpec struct {
	// st is the state this entry describes and must equal the entry's index in
	// surfaceSpecs. Redundant with the keyed literal below on purpose:
	// self-identification is how the completeness guard tells a forgotten
	// (zero-valued) slot from a deliberately bare one.
	st state

	// fixture is the golden's basename under testdata/frames — unique across
	// the table, and the name half of what frameStates derives from here.
	fixture string

	// render returns the overlay content viewContent places over the composed
	// frame. nil: the state renders in the frame itself (the list, panes and
	// bar own every pixel, and viewContent returns the frame unadorned).
	// Non-nil entries are built by renderOverlay, which keeps each old ladder
	// arm's shape: log a nil overlay, then render it anyway.
	render func(m *home) string

	// keys handles a key press in this state, replacing one handleKeyPress
	// prelude guard. nil: keys fall through to the globals (the esc ladder,
	// ctrl+c, registry dispatch). Every non-nil handler runs before those
	// globals — the ordering each old guard demanded in its own comment, now
	// structural; what q or esc means inside a surface is per-entry rationale
	// below.
	keys func(m *home, msg tea.KeyPressMsg) (tea.Model, tea.Cmd)

	// barVisible is menuVisible's bit: whether the hint bar keeps its row in
	// this state. Modal overlays render their own instructions, so the bar
	// behind them would be a redundant strip; inline interactions and plain
	// navigation keep it (menuVisible's doc carries the #438 reservation
	// contract).
	barVisible bool

	// size is one overlay field's resize policy, as data: the geometry is the
	// overlay's own SizeSpec (declared beside the overlay, in outer cells) and
	// target returns the field's live pointer for the walk to size. Owned by
	// the overlay FIELD, not the state: exactly one entry carries each field's
	// target (stateInfo's textOverlay is sized by stateHelp's entry), and the
	// resize walk runs every entry — preserving the old semantics, where a
	// still-armed overlay is resized whatever the current state is.
	// TestEverySizedOverlayFieldHasOneOwner counts each field's owners (#856).
	size sizeSpec

	// paste handles a paste landing in this state. nil: the paste is inert.
	// The non-nil entries are the enumeration of where pasted text can land;
	// anywhere else there is nothing for text to mean (handlePaste's doc says
	// why paste never rides the key dispatch). Rename, settings and accounts
	// share pasteOverlay; prompt, filter and the palette are bespoke (follow-up
	// cmd, list mutation, string payload).
	paste func(m *home, msg tea.PasteMsg) (tea.Model, tea.Cmd)
}

// sizeSpec is one overlay field's resize policy: spec is the geometry, target
// the field it applies to. The walk calls target(m).SetSize(spec.Fit(...))
// whenever the pointer is armed, so the target is the only mechanism by which
// a field gets sized — which is what makes the one-owner-per-field rule
// checkable by pointer identity rather than held by prose (#856).
type sizeSpec struct {
	spec   overlay.SizeSpec
	target func(m *home) resizer
}

// resizer is the one call the resize walk makes on an armed overlay.
type resizer interface {
	SetSize(width, height int)
}

// sizeTarget adapts one overlay field accessor into the nil-safe accessor a
// sizeSpec carries. The typed pointer parameter matters for the same reason
// as renderOverlay's: boxed straight into the interface, a nil field would
// pass the walk's nil check and panic inside SetSize, so the nil is caught
// here while the pointer is still typed.
func sizeTarget[T any, P interface {
	*T
	SetSize(width, height int)
}](field func(m *home) P) func(m *home) resizer {
	return func(m *home) resizer {
		if o := field(m); o != nil {
			return o
		}
		return nil
	}
}

// renderOverlay builds one overlay field's render closure: log a nil pointer,
// then render it anyway — a state and its overlay are set together, so the log
// is a loud breadcrumb for a bug, not a recovery path. The getter returns the
// field's concrete pointer type on purpose: an interface-typed accessor would
// box a nil pointer into a non-nil interface value and skip the breadcrumb.
func renderOverlay[T any, P interface {
	*T
	Render() string
}](name string, field func(m *home) P) func(m *home) string {
	return func(m *home) string {
		o := field(m)
		if o == nil {
			log.ErrorLog.Printf("%s is nil", name)
		}
		return o.Render()
	}
}

// renderTextOverlay is the shared render for stateHelp and stateInfo, which
// display different content through the same textOverlay field — the enum's one
// many-to-one.
var renderTextOverlay = renderOverlay("text overlay",
	func(m *home) *overlay.TextOverlay { return m.textOverlay })

// pasteOverlay builds one overlay field's paste closure: hand the paste to the
// overlay when it is armed, stay put either way. Unlike renderOverlay's arms, a
// nil field here really bails — the old switch dropped a paste with no overlay
// rather than logging one (TestPasteOnAMissingOverlayIsDropped pins the drop).
// The typed pointer matters for the same reason as renderOverlay's: boxed
// through an interface, a nil field would pass the nil check and panic inside
// HandlePaste.
func pasteOverlay[T any, P interface {
	*T
	HandlePaste(tea.PasteMsg)
}](field func(m *home) P) func(m *home, msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	return func(m *home, msg tea.PasteMsg) (tea.Model, tea.Cmd) {
		if o := field(m); o != nil {
			o.HandlePaste(msg)
		}
		return m, nil
	}
}

// surfaceSpecs is the registry, indexed by state. Declared empty and filled in
// init because a package-level initializer that references the key handlers
// cannot compile: Go's initialization-dependency analysis is transitive through
// function bodies, and the handlers reach recomputeLayout, whose
// updateHandleWindowSizeEvent reads this very table (as does menuVisible) — an
// initialization cycle. Assigning in init breaks that analysis edge, at a
// price the compiler no longer polices: the zero table — every handler nil,
// every bar hidden — is readable without a panic to say so, by an init in a
// sibling file that sorts before this one and equally by any package-level var
// initializer in the package, since var initializers all run before every init
// and this declaration carries no initializer expression for the dependency
// analysis to order against. So neither may reach the readers; nothing
// enforces that rule (#856 tracks the guard). The keyed literal makes a whole
// entry moved under the wrong state a duplicate-index compile error (every
// index is claimed); what compiles — two entries' bodies swapped between their
// literal indexes, an st mistyped — is what TestEverySurfaceSpecIsComplete
// catches.
var surfaceSpecs [numStates]surfaceSpec

func init() {
	surfaceSpecs = [numStates]surfaceSpec{
		stateDefault: {
			st: stateDefault, fixture: "default",
			// The bottom row is always reserved during plain navigation, so a
			// transient notice never resizes the frame (#438). generatingName /
			// actionInFlight are subsumed here — they still drive the menu's
			// StateGeneratingName / StateBusy content. With hint_bar off the row
			// renders blank (Menu.quiet, seeded from the setting), giving a still
			// chrome-free frame a notice can ride without a shift.
			barVisible: true,
		},
		statePrompt: {
			st: statePrompt, fixture: "prompt",
			render: renderOverlay("text input overlay",
				func(m *home) *overlay.TextInputOverlay { return m.textInputOverlay }),
			keys: (*home).handlePromptState,
			size: sizeSpec{spec: overlay.TextInputSize,
				target: sizeTarget(func(m *home) *overlay.TextInputOverlay { return m.textInputOverlay })},
			paste: func(m *home, msg tea.PasteMsg) (tea.Model, tea.Cmd) {
				if m.textInputOverlay == nil {
					return m, nil
				}
				return m.handlePromptPaste(msg)
			},
		},
		stateHelp: {
			st: stateHelp, fixture: "help",
			render: renderTextOverlay,
			keys:   (*home).handleHelpState,
			size: sizeSpec{spec: overlay.Fullscreen,
				target: sizeTarget(func(m *home) *overlay.TextOverlay { return m.textOverlay })},
		},
		stateConfirm: {
			st: stateConfirm, fixture: "confirm",
			render: renderOverlay("confirmation overlay",
				func(m *home) *overlay.ConfirmationOverlay { return m.confirmationOverlay }),
			keys: (*home).handleConfirmState,
			size: sizeSpec{spec: overlay.ConfirmSize,
				target: sizeTarget(func(m *home) *overlay.ConfirmationOverlay { return m.confirmationOverlay })},
		},
		stateRename: {
			st: stateRename, fixture: "rename",
			render: renderOverlay("rename overlay",
				func(m *home) *overlay.RenameOverlay { return m.renameOverlay }),
			// Rename runs before the global q/ctrl+c quit handling so those keys
			// edit (or cancel) the label instead of quitting the app.
			keys: (*home).handleRenameState,
			// No size target: the rename box is sized once, at open —
			// deliberately outside the resize walk.
			paste: pasteOverlay(func(m *home) *overlay.RenameOverlay { return m.renameOverlay }),
		},
		stateQueue: {
			st: stateQueue, fixture: "queue",
			render: renderOverlay("queue overlay",
				func(m *home) *overlay.QueueOverlay { return m.queueOverlay }),
			// q is swallowed by the queue overlay rather than quitting; esc is
			// what closes it.
			keys: (*home).handleQueueState,
			// The queue box shares HistoryPickerSize with the picker; the opener
			// sends tea.RequestWindowSize, so this entry is the queue's only
			// sizing site.
			size: sizeSpec{spec: overlay.HistoryPickerSize,
				target: sizeTarget(func(m *home) *overlay.QueueOverlay { return m.queueOverlay })},
		},
		stateCmdLog: {
			st: stateCmdLog, fixture: "cmdlog",
			render: renderOverlay("command-log overlay",
				func(m *home) *overlay.CmdLogOverlay { return m.cmdLogOverlay }),
			keys: (*home).handleCmdLogState,
			size: sizeSpec{spec: overlay.CmdLogSize,
				target: sizeTarget(func(m *home) *overlay.CmdLogOverlay { return m.cmdLogOverlay })},
		},
		stateFilter: {
			st: stateFilter, fixture: "filter",
			// Inline interaction: the bar teaches its gestures (the accept/clear
			// cue), so it stays even when the always-on hint bar is turned off.
			barVisible: true,
			// Filter runs before the global quit handling so that printable keys
			// and esc update the filter instead of quitting.
			keys: (*home).handleFilterState,
			paste: func(m *home, msg tea.PasteMsg) (tea.Model, tea.Cmd) {
				// The list owns the query; a paste extends it exactly as typing does.
				m.list.SetFilter(m.list.FilterQuery() + msg.Content)
				return m, m.instanceChanged()
			},
		},
		stateInfo: {
			st: stateInfo, fixture: "info",
			// Shares textOverlay with stateHelp: same render, and the field's one
			// size entry lives on stateHelp's (a second target here would
			// double-apply the same SetSize on every resize).
			render: renderTextOverlay,
			keys:   (*home).handleInfoState,
		},
		stateSettings: {
			st: stateSettings, fixture: "settings",
			render: renderOverlay("settings overlay",
				func(m *home) *overlay.SettingsOverlay { return m.settingsOverlay }),
			// Settings, like the other overlay states, runs before the global quit
			// handling so q/esc and printable keys reach the panel.
			keys: (*home).handleSettingsState,
			size: sizeSpec{spec: overlay.Fullscreen,
				target: sizeTarget(func(m *home) *overlay.SettingsOverlay { return m.settingsOverlay })},
			paste: pasteOverlay(func(m *home) *overlay.SettingsOverlay { return m.settingsOverlay }),
		},
		stateHints: {
			st: stateHints, fixture: "hints",
			// Inline interaction — the bar teaches its gestures (see stateFilter).
			// The old menuVisible switch reached the same answer through its
			// default arm without naming this state; the entry makes it explicit.
			barVisible: true,
			// Hint (fingers) mode: every key is either a hint character or an
			// exit. Runs before the global esc/quit handling so hint letters like
			// q never quit the app.
			keys: (*home).handleHintsState,
		},
		stateVisual: {
			st: stateVisual, fixture: "visual",
			// Inline interaction — the bar teaches its gestures (see stateFilter).
			barVisible: true,
			// Multi-select (visual) mode: space marks, lifecycle keys act on the
			// marked set. Runs before the global esc/quit handling so esc clears
			// the marks (not the filter) and q never quits.
			keys: (*home).handleMultiSelectState,
		},
		stateDiffComment: {
			st: stateDiffComment, fixture: "diffComment",
			// Inline interaction — the bar teaches its gestures (see stateFilter).
			barVisible: true,
			// Diff-comment mode: the line cursor moves with j/k, enter opens the
			// composer. Runs before the global esc/quit handling so esc leaves
			// comment mode (not the app) and q never quits.
			keys: (*home).handleDiffCommentState,
		},
		stateWelcome: {
			st: stateWelcome, fixture: "welcome",
			render: renderOverlay("welcome overlay",
				func(m *home) *overlay.WelcomeOverlay { return m.welcomeOverlay }),
			keys: (*home).handleWelcomeState,
			size: sizeSpec{spec: overlay.WelcomeSize,
				target: sizeTarget(func(m *home) *overlay.WelcomeOverlay { return m.welcomeOverlay })},
		},
		stateAccounts: {
			st: stateAccounts, fixture: "accounts",
			render: renderOverlay("accounts overlay",
				func(m *home) *overlay.AccountsOverlay { return m.accountsOverlay }),
			// Accounts, like the other overlay states, runs before the global quit
			// handling so q/esc and printable keys reach the panel.
			keys: (*home).handleAccountsState,
			size: sizeSpec{spec: overlay.Fullscreen,
				target: sizeTarget(func(m *home) *overlay.AccountsOverlay { return m.accountsOverlay })},
			paste: pasteOverlay(func(m *home) *overlay.AccountsOverlay { return m.accountsOverlay }),
		},
		stateScreensaver: {
			st: stateScreensaver, fixture: "screensaver",
			// Bare on purpose, not forgotten: the splash replaces the whole frame
			// before viewContent consults render, and handleKeyPress consumes
			// every key but ctrl+l (a repaint must not tear the splash down)
			// before the table lookup, dismissing — see dismissScreensaver for
			// the full exit inventory. barVisible is true
			// because the old menuVisible switch let this state fall through to
			// its default arm; the reserved row is simply never composed while the
			// splash is up.
			barVisible: true,
		},
		stateHistory: {
			st: stateHistory, fixture: "history",
			render: renderOverlay("prompt history overlay",
				func(m *home) *overlay.PromptHistoryOverlay { return m.promptHistoryOverlay }),
			keys: (*home).handleHistoryState,
			// The same spec the picker opened with (HistoryPickerSize), so open
			// and resize cannot drift apart.
			size: sizeSpec{spec: overlay.HistoryPickerSize,
				target: sizeTarget(func(m *home) *overlay.PromptHistoryOverlay { return m.promptHistoryOverlay })},
		},
		stateCommandPalette: {
			st: stateCommandPalette, fixture: "commandPalette",
			render: renderOverlay("command palette overlay",
				func(m *home) *overlay.CommandPaletteOverlay { return m.commandPaletteOverlay }),
			// The palette runs before the global quit handling so that q and
			// every other printable key narrows the filter instead of quitting
			// the app mid-query.
			keys: (*home).handleCommandPaletteState,
			size: sizeSpec{spec: overlay.CommandPaletteSize,
				target: sizeTarget(func(m *home) *overlay.CommandPaletteOverlay { return m.commandPaletteOverlay })},
			paste: func(m *home, msg tea.PasteMsg) (tea.Model, tea.Cmd) {
				if m.commandPaletteOverlay != nil {
					m.commandPaletteOverlay.HandlePaste(msg.Content)
				}
				return m, nil
			},
		},
		stateCustomCommands: {
			st: stateCustomCommands, fixture: "customCommands",
			render: renderOverlay("custom commands overlay",
				func(m *home) *overlay.CustomCommandsOverlay { return m.customCommandsOverlay }),
			// The custom-commands menu runs before the global quit handling for
			// the same reason as the palette, and more sharply: its rows are keyed
			// by whatever the user configured, so q really can be a command key
			// here.
			keys: (*home).handleCustomCommandsState,
			size: sizeSpec{spec: overlay.CustomCommandsSize,
				target: sizeTarget(func(m *home) *overlay.CustomCommandsOverlay { return m.customCommandsOverlay })},
		},
		stateCheckpoints: {
			st: stateCheckpoints, fixture: "checkpoints",
			render: renderOverlay("checkpoint overlay",
				func(m *home) *overlay.CheckpointOverlay { return m.checkpointOverlay }),
			// The checkpoint timeline runs before the global quit handling too: r
			// reloads it here rather than resuming a paused session, and q must be
			// swallowed rather than quit the app out from under an open box (as in
			// the queue overlay, esc is what closes).
			keys: (*home).handleCheckpointsState,
			size: sizeSpec{spec: overlay.CheckpointSize,
				target: sizeTarget(func(m *home) *overlay.CheckpointOverlay { return m.checkpointOverlay })},
		},
		stateImagePreview: {
			st: stateImagePreview, fixture: "imagePreview",
			render: renderOverlay("image overlay",
				func(m *home) *overlay.ImageOverlay { return m.imageOverlay }),
			// The image preview, like the other overlay states, runs before the
			// global quit handling: it is a read-only box with one gesture, so
			// every other key — q included — is swallowed rather than acted on
			// behind it.
			keys: (*home).handleImagePreviewState,
			size: sizeSpec{spec: overlay.ImageSize,
				target: sizeTarget(func(m *home) *overlay.ImageOverlay { return m.imageOverlay })},
		},
	}
}
