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

	// size applies one overlay field's resize policy, replacing one of
	// updateHandleWindowSizeEvent's nil-guarded blocks. Owned by the overlay
	// FIELD, not the state: exactly one entry carries each field's block
	// (stateInfo's textOverlay is sized by stateHelp's entry), each closure
	// nil-checks its own pointer, and the resize walk runs every entry's size —
	// preserving the old semantics, where a still-armed overlay is resized
	// whatever the current state is. Until #802's data-shaped SizeSpec replaces
	// these closures, the one-block-per-field rule is held by prose alone — no
	// guard counts a field's owners (#856).
	size sizeSpec

	// paste handles a paste landing in this state. nil: the paste is inert.
	// The non-nil entries are the enumeration of where pasted text can land;
	// anywhere else there is nothing for text to mean (handlePaste's doc says
	// why paste never rides the key dispatch). Rename, settings and accounts
	// share pasteOverlay; prompt, filter and the palette are bespoke (follow-up
	// cmd, list mutation, string payload).
	paste func(m *home, msg tea.PasteMsg) (tea.Model, tea.Cmd)
}

// sizeSpec is one overlay's resize policy. A named type so the layout-budget
// work (#802) can evolve sizing into data without re-shaping the table.
type sizeSpec func(m *home, msg tea.WindowSizeMsg)

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
			size: func(m *home, msg tea.WindowSizeMsg) {
				if m.textInputOverlay != nil {
					// Pass the full terminal height: the create form sizes its own
					// sections to fit (and the plain prompt overlay applies its own
					// fraction), so it needs to know the real height rather than a
					// pre-scaled slice of it.
					m.textInputOverlay.SetSize(int(float32(msg.Width)*0.6), msg.Height)
				}
			},
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
			size: func(m *home, msg tea.WindowSizeMsg) {
				if m.textOverlay != nil {
					// Pass the full terminal size: the overlay hugs its content width
					// and windows its lines to fit short terminals.
					m.textOverlay.SetSize(msg.Width, msg.Height)
				}
			},
		},
		stateConfirm: {
			st: stateConfirm, fixture: "confirm",
			render: renderOverlay("confirmation overlay",
				func(m *home) *overlay.ConfirmationOverlay { return m.confirmationOverlay }),
			keys: (*home).handleConfirmState,
			size: func(m *home, msg tea.WindowSizeMsg) {
				if m.confirmationOverlay != nil {
					// The dialog keeps its classic width on normal terminals and
					// shrinks with narrow ones; it was the one overlay excluded from
					// resize handling.
					m.confirmationOverlay.SetWidth(confirmWidth(msg.Width))
				}
			},
		},
		stateRename: {
			st: stateRename, fixture: "rename",
			render: renderOverlay("rename overlay",
				func(m *home) *overlay.RenameOverlay { return m.renameOverlay }),
			// Rename runs before the global q/ctrl+c quit handling so those keys
			// edit (or cancel) the label instead of quitting the app.
			keys: (*home).handleRenameState,
			// No size: the rename box is sized once, at open — deliberately
			// outside the resize walk.
			paste: pasteOverlay(func(m *home) *overlay.RenameOverlay { return m.renameOverlay }),
		},
		stateQueue: {
			st: stateQueue, fixture: "queue",
			render: renderOverlay("queue overlay",
				func(m *home) *overlay.QueueOverlay { return m.queueOverlay }),
			// q is swallowed by the queue overlay rather than quitting; esc is
			// what closes it.
			keys: (*home).handleQueueState,
			size: func(m *home, msg tea.WindowSizeMsg) {
				if m.queueOverlay != nil {
					// The queue box shares the history picker's responsive width,
					// and historyOverlayWidth owns the one expression of it — the
					// opener sends tea.RequestWindowSize, so this closure is the
					// queue's only sizing site.
					m.queueOverlay.SetWidth(historyOverlayWidth(msg.Width))
				}
			},
		},
		stateCmdLog: {
			st: stateCmdLog, fixture: "cmdlog",
			render: renderOverlay("command-log overlay",
				func(m *home) *overlay.CmdLogOverlay { return m.cmdLogOverlay }),
			keys: (*home).handleCmdLogState,
			size: func(m *home, msg tea.WindowSizeMsg) {
				if m.cmdLogOverlay != nil {
					// The command log benefits from width (argv) and height (many
					// rows), so it takes a larger share than the queue overlay, capped
					// for very wide terminals.
					w := int(float32(msg.Width) * 0.85)
					if w > 120 {
						w = 120
					}
					h := int(float32(msg.Height) * 0.85)
					if h > 44 {
						h = 44
					}
					m.cmdLogOverlay.SetSize(w, h)
				}
			},
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
			// size block lives on stateHelp's entry (a second one here would
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
			size: func(m *home, msg tea.WindowSizeMsg) {
				if m.settingsOverlay != nil {
					// Pass the full terminal size: the panel caps its own width and
					// windows its rows to fit short terminals.
					m.settingsOverlay.SetSize(msg.Width, msg.Height)
				}
			},
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
			size: func(m *home, msg tea.WindowSizeMsg) {
				if m.welcomeOverlay != nil {
					// Same idiom as the confirmation dialog: keep the authored width
					// on normal terminals, shrink so the box never spills off a
					// narrow one.
					m.welcomeOverlay.SetWidth(welcomeWidth(msg.Width))
				}
			},
		},
		stateAccounts: {
			st: stateAccounts, fixture: "accounts",
			render: renderOverlay("accounts overlay",
				func(m *home) *overlay.AccountsOverlay { return m.accountsOverlay }),
			// Accounts, like the other overlay states, runs before the global quit
			// handling so q/esc and printable keys reach the panel.
			keys: (*home).handleAccountsState,
			size: func(m *home, msg tea.WindowSizeMsg) {
				if m.accountsOverlay != nil {
					// Pass the full terminal size: the panel caps its own width and
					// windows its rows to fit short terminals.
					m.accountsOverlay.SetSize(msg.Width, msg.Height)
				}
			},
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
			size: func(m *home, msg tea.WindowSizeMsg) {
				if m.promptHistoryOverlay != nil {
					// The same width the picker opened with: historyOverlayWidth
					// owns the expression, so open and resize cannot drift apart.
					m.promptHistoryOverlay.SetWidth(historyOverlayWidth(msg.Width))
				}
			},
		},
		stateCommandPalette: {
			st: stateCommandPalette, fixture: "commandPalette",
			render: renderOverlay("command palette overlay",
				func(m *home) *overlay.CommandPaletteOverlay { return m.commandPaletteOverlay }),
			// The palette runs before the global quit handling so that q and
			// every other printable key narrows the filter instead of quitting
			// the app mid-query.
			keys: (*home).handleCommandPaletteState,
			size: func(m *home, msg tea.WindowSizeMsg) {
				if m.commandPaletteOverlay != nil {
					// Three columns wide (key, verb, prose) and as many rows as it
					// can get: the palette's whole value is seeing a lot of the
					// keymap at once. Capped like the command log so a very wide
					// terminal doesn't stretch the prose column past comfortable
					// reading.
					w := int(float32(msg.Width) * 0.85)
					if w > 100 {
						w = 100
					}
					// The share is of the *box*, border and padding included, so it
					// is the room the palette may occupy rather than the room it
					// fills and then overruns. The +3 keeps the rendered size where
					// it was before that was true.
					h := int(float32(msg.Height)*0.85) + 3
					if h > 43 {
						h = 43
					}
					if h > msg.Height {
						h = msg.Height
					}
					m.commandPaletteOverlay.SetSize(w, h)
				}
			},
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
			size: func(m *home, msg tea.WindowSizeMsg) {
				if m.customCommandsOverlay != nil {
					// Narrower than the palette: two columns (key, description) of
					// user-authored prose, where the palette has three of generated
					// text. Capped for the same reason — a very wide terminal should
					// not stretch a one-line description across the screen. The
					// share is of the box, border and padding included.
					w := int(float32(msg.Width) * 0.7)
					if w > 80 {
						w = 80
					}
					// No `h > msg.Height` clamp, unlike the palette: a 0.7 share
					// capped at 30 cannot exceed the height it was taken from. That
					// guard is necessary there because of the palette's `+3`, and
					// copying it here would read as load-bearing while doing
					// nothing.
					h := int(float32(msg.Height) * 0.7)
					if h > 30 {
						h = 30
					}
					m.customCommandsOverlay.SetSize(w, h)
				}
			},
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
			size: func(m *home, msg tea.WindowSizeMsg) {
				if m.checkpointOverlay != nil {
					// One row per checkpoint: a time column, the prompt line, and
					// what the checkpoint covers. Wants height more than width — a
					// long session has dozens of checkpoints — so it takes the
					// command log's height share and a narrower width, capped so the
					// prompt column stays readable.
					w := int(float32(msg.Width) * 0.7)
					if w > 96 {
						w = 96
					}
					h := int(float32(msg.Height) * 0.85)
					if h > 40 {
						h = 40
					}
					m.checkpointOverlay.SetSize(w, h)
				}
			},
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
			size: func(m *home, msg tea.WindowSizeMsg) {
				if m.imageOverlay != nil {
					// The most generous share here, because resolution is the whole
					// point: every cell the box gives up is two pixels of the
					// picture. The caps are the same ones ImageOverlay enforces on
					// the picture itself, plus its chrome — asking for more would
					// only pad the box around a picture that cannot grow. The share
					// is of the box, border and padding included.
					w := int(float32(msg.Width) * 0.85)
					if w > imagePreviewMaxWidth {
						w = imagePreviewMaxWidth
					}
					h := int(float32(msg.Height) * 0.85)
					if h > imagePreviewMaxHeight {
						h = imagePreviewMaxHeight
					}
					m.imageOverlay.SetSize(w, h)
				}
			},
		},
	}
}
