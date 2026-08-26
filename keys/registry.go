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

// Effect is what pressing a key can change. It exists because
// keyAllowedWhileBusy (app/app_update.go) states a correctness rule about every
// key in the registry — "nothing that mutates a session, opens an overlay, or
// drives tmux/git" — that nothing checked: a new key that should have been
// excluded simply was not, and a green suite said nothing (#521).
//
// The zero value is INVALID on purpose. That is the whole mechanism: forgetting
// to classify a new key is a test failure
// (TestEveryRegistryEntryDeclaresAnEffect), not a silent default-allow, so the
// guard is bidirectional rather than a list someone must remember to update —
// the same shape TestEveryScalarConfigFieldHasARow uses for Config fields.
//
// Three values rather than the observe/mutate pair the issue proposed, and the
// third one is the interesting one. Six keys the busy-gate deliberately admits
// (fold, list width, layout preset) write PERSISTED state: SetCollapsedRepos and
// SetLayout (config/state.go) both return an error because they save state.json.
// Under a literal "observing never touches disk" they are all mutations and the
// subset check fails on its first run over correct code. EffectView names them
// instead, which keeps "observe" honest and — unlike a carve-out inside
// EffectObserve — leaves the set enumerable, because --readonly (#522) rules that
// those keys stay live with the save path made a no-op and needs to know exactly
// which keys that seam is load-bearing for.
//
// The line between EffectView and EffectMutate is what the persisted datum
// DESCRIBES, not whether it is persisted: the arrangement of the view, or a
// session / repo / config / agent. Mute (M) also writes state.json and is
// EffectMutate, because what it writes is a session's own attribute.
//
// Two classification rules the entries below rely on:
//
//   - Approve (a) is EffectMutate even though it changes nothing in Atrium. It
//     taps Enter on the agent's live tool-permission dialog, so miss it and a
//     read-only TUI lets a bystander authorize an `rm -rf`.
//   - A key that opens a surface with keys of its own carries the worst effect
//     reachable THROUGH that surface, unless another gate on this same
//     classification already covers it. The command palette is the one that
//     shows this is not just "openers are Mutate": runPaletteAction (app/palette.go)
//     re-enters the keypress tail (the focus router, then dispatchAction), so
//     every row it can run is classified on its own.
type Effect int

const (
	// EffectUnset is the invalid zero value: an Entry that never says what it
	// does. Never write it deliberately — it exists so that omitting the field
	// fails a test instead of defaulting into "safe".
	EffectUnset Effect = iota
	// EffectObserve changes nothing ATRIUM OWNS.
	//
	// The line is ownership, not I/O and not liveness — both of the weaker tests
	// misfile the keys below. Observing keys read from disk (hint mode decodes an
	// image) and start processes that outlive the keypress by design: open-PR (w)
	// and hint mode's URL branch hand a target to xdg-open/open through
	// actions.OpenInBrowser, and on Linux the clipboard helpers persist in order to
	// own the selection. None of those are Atrium's to track or reap.
	//
	// What disqualifies a key is changing something Atrium is responsible for: a
	// session on its socket, a worktree, a branch, state.json or config.json, an
	// agent's conversation. That is why the terminal tab is not here — its shell is
	// a session in Atrium's own fleet, which Atrium must later reap.
	EffectObserve
	// EffectView changes the arrangement of the view and nothing else — fold
	// state, the list/preview split, the layout preset, list order. Persisted
	// (state.json), which is why it is not EffectObserve; never a session, repo,
	// config or agent, which is why it is not EffectMutate.
	EffectView
	// EffectMutate changes a session, a repo, the config, or an agent — including
	// authorizing an agent to change something on its own.
	EffectMutate
)

// String names the effect for test failures and help text. Unset renders as
// "unset" rather than panicking, so a guard can report the entry that forgot.
func (e Effect) String() string {
	switch e {
	case EffectObserve:
		return "observe"
	case EffectView:
		return "view"
	case EffectMutate:
		return "mutate"
	default:
		return "unset"
	}
}

// Entry is one row of the keymap registry: a logical action with the binding
// that carries its authoritative key strings (WithKeys) and hint-bar help
// text (WithHelp), plus the layer that honors it and the effect it can have.
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
	// Effect is what pressing this key can change. Mandatory: the zero value is
	// invalid and fails TestEveryRegistryEntryDeclaresAnEffect. See Effect.
	Effect  Effect
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
	{Name: KeyUp, Action: "up", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	)},
	{Name: KeyDown, Action: "down", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	)},
	{Name: KeyShiftUp, Action: "scroll_up", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("shift+up"),
		key.WithHelp("shift-↑", "scroll"),
	)},
	{Name: KeyShiftDown, Action: "scroll_down", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("shift+down"),
		key.WithHelp("shift-↓", "scroll"),
	)},
	{Name: KeyNextUnread, Action: "next_unread", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("u"),
		key.WithHelp("u", "next unread"),
	)},
	{Name: KeyNextNeedsInput, Action: "next_blocked", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("b"),
		key.WithHelp("b", "next blocked"),
	)},
	// Attach, and the reason it is not the read-only act its "open" label suggests:
	// it hands the raw keyboard to the agent, which can then be told anything. The
	// attach layer also honors ctrl+x as a raw byte independently of dispatch, so a
	// caller that wants a harmless attach needs tmux's own -r AND allowKill=false —
	// see Session.Attach (session/tmux) — not a softer classification here.
	{Name: KeyEnter, Action: "open", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("enter", "o"),
		key.WithHelp("↵/o", "open"),
	)},
	{Name: KeyNew, Action: "new", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "new"),
	)},
	{Name: KeySmartDispatch, Action: "smart_new", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("i"),
		key.WithHelp("i", "smart new"),
	)},
	{Name: KeyKill, Action: "kill", Layer: LayerBoth, Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("ctrl+x"),
		key.WithHelp("ctrl-x", "kill"),
	)},
	{Name: KeyRename, Action: "rename", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("R"),
		key.WithHelp("R", "rename"),
	)},
	{Name: KeyAutoName, Action: "auto_name", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("A"),
		key.WithHelp("A", "auto-name"),
	)},
	{Name: KeyMute, Action: "mute", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("M"),
		key.WithHelp("M", "mute notifications"),
	)},
	{Name: KeyQuickSend, Action: "send", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "send"),
	)},
	{Name: KeyDiffComment, Action: "diff_comment", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("C"),
		key.WithHelp("C", "comment on a diff line"),
	)},
	// The overlay this opens cancels queued prompts, so the key inherits that.
	{Name: KeyQueue, Action: "queue", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("Q"),
		key.WithHelp("Q", "manage queued prompts"),
	)},
	{Name: KeyCmdLog, Action: "command_log", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("L"),
		key.WithHelp("L", "command log"),
	)},
	// The timeline itself is read-only — restoring a checkpoint is claude's own
	// Esc-Esc, not ours — but the overlay arms three actions (checkpointOverlay.go:
	// enter, f, r) and two of them are why this is Mutate. AttachRequested reaches
	// the keyboard-to-the-agent hand-off above without going through KeyEnter.
	// ForkRequested reaches forkFromCheckpoint, which opens the create FORM seeded
	// from the conversation — the session appears only if the user submits it, so
	// this is KeyNew's effect arrived at sideways rather than a session made here.
	// RefreshRequested (r) reloads the timeline and is the read-only one.
	{Name: KeyCheckpoints, Action: "checkpoints", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("H"),
		key.WithHelp("H", "checkpoints"),
	)},
	{Name: KeyHelp, Action: "help", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	)},
	// Quitting persists state and can launch the autoyes daemon (main.go), so it is
	// a mutation — and it is nonetheless in keyAllowedWhileBusy on purpose, because
	// swallowing it would leave a wedged action with no way out but ctrl+c. That
	// tension is carried as a named exemption in the busy-gate's own guard, not by
	// softening this classification.
	{Name: KeyQuit, Action: "quit", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("q"),
		key.WithHelp("q", "quit"),
	)},
	{Name: KeySubmit, Action: "push_branch", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("P"),
		key.WithHelp("P", "push branch"),
	)},
	{Name: KeyCreate, Action: "create_pr", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "create PR"),
	)},
	{Name: KeyMerge, Action: "merge_pr", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("m"),
		key.WithHelp("m", "merge PR"),
	)},
	// Viewing, not touching: openPRForSelected reads the poll-maintained snapshot
	// and launches a browser. Nothing in the fleet, the repo or the PR changes.
	{Name: KeyOpenPR, Action: "open_pr", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("w"),
		key.WithHelp("w", "open PR"),
	)},
	{Name: KeyPrompt, Action: "new_pick_project", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("N"),
		key.WithHelp("N", "new (pick project)"),
	)},
	{Name: KeyPause, Action: "pause", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("p"),
		key.WithHelp("p", "pause"),
	)},
	{Name: KeyPauseAll, Action: "pause_all", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("ctrl+p"),
		key.WithHelp("ctrl-p", "pause all"),
	)},
	// Cycling reaches the terminal tab, so it inherits that tab's effect (see
	// KeyTabTerminal). Nothing else covers it: the tab is a rendering surface, not
	// a dispatched action, so no other Entry is in a position to classify the shell
	// it starts. Reclassify these two the day a gate on THIS classification covers
	// that surface — #701 gated it on an action being in flight, which is a
	// different question and leaves the idle case exactly as it was.
	{Name: KeyTab, Action: "next_tab", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch tab"),
	)},
	{Name: KeyShiftTab, Action: "prev_tab", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift-tab", "prev tab"),
	)},
	{Name: KeyResume, Action: "resume", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "resume"),
	)},
	{Name: KeyResumeAll, Action: "resume_all", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl-r", "resume all"),
	)},
	{Name: KeyUndoKill, Action: "undo_kill", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("U"),
		key.WithHelp("U", "undo the last kill"),
	)},
	// Entering the mode mutates nothing by itself, but the mode runs batch
	// kill/pause/resume over the marked set. Most of that table IS visible to a
	// registry walk: handleMultiSelectState (app/app_keys.go) matches only esc and
	// a literal "x", then resolves the rest through GlobalKeyStringsMap and
	// switches on KeyPause/KeyResume/KeyKill — all registered, all EffectMutate,
	// all override-aware. The unowned one is plain "x" (batch kill, the key the
	// mode's own bar advertises), and this key is the only registry-level gate
	// over it, so it carries that effect.
	{Name: KeyMultiSelect, Action: "multi_select", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("v"),
		key.WithHelp("v", "multi-select"),
	)},
	// Marking a row is bookkeeping in the list; the marked set does not act until
	// one of the lifecycle keys above does.
	{Name: KeyToggleMark, Action: "toggle_mark", Effect: EffectObserve, Binding: key.NewBinding(
		// "space", not " ". Bubble Tea v1 reported the space bar from String() as a
		// literal space; v2 names it. Dispatch is keyed on that string
		// (GlobalKeyStringsMap), so leaving " " here would compile, pass review, and
		// ship the mark key dead — nothing in the suite asserts that a registered key
		// reaches a dispatch case.
		key.WithKeys("space"),
		key.WithHelp("space", "mark/unmark"),
	)},
	// The reorder ladder and the fold/split/preset keys below are the whole of
	// EffectView: each one writes state.json (moveAndPersist saves the instance
	// array; SetAccountOrder, SetCollapsedRepos and SetLayout are the rest), and
	// what each one writes is the arrangement of the view — never a session, a
	// repo, the config or an agent. They are the six the busy-gate admits plus the
	// six reorder keys, and the set --readonly (#522) keeps live behind a save path
	// made a no-op.
	{Name: KeyMoveUp, Action: "move_up", Effect: EffectView, Binding: key.NewBinding(
		key.WithKeys("K"),
		key.WithHelp("K", "move up"),
	)},
	{Name: KeyMoveDown, Action: "move_down", Effect: EffectView, Binding: key.NewBinding(
		key.WithKeys("J"),
		key.WithHelp("J", "move down"),
	)},
	{Name: KeyMoveGroupUp, Action: "move_group_up", Effect: EffectView, Binding: key.NewBinding(
		key.WithKeys("{"),
		key.WithHelp("{", "move group up"),
	)},
	{Name: KeyMoveGroupDown, Action: "move_group_down", Effect: EffectView, Binding: key.NewBinding(
		key.WithKeys("}"),
		key.WithHelp("}", "move group down"),
	)},
	// The unit here is the account *cluster* (a repo whose sessions span
	// accounts still renders as one cluster) — #357 was this text saying
	// "account"; the ladder vocabulary is pinned by registry_test.go.
	{Name: KeyMoveAccountUp, Action: "move_account_up", Effect: EffectView, Binding: key.NewBinding(
		key.WithKeys("["),
		key.WithHelp("[", "move account cluster up"),
	)},
	{Name: KeyMoveAccountDown, Action: "move_account_down", Effect: EffectView, Binding: key.NewBinding(
		key.WithKeys("]"),
		key.WithHelp("]", "move account cluster down"),
	)},
	{Name: KeyCollapse, Action: "collapse_group", Effect: EffectView, Binding: key.NewBinding(
		key.WithKeys("left"),
		key.WithHelp("←", "collapse group"),
	)},
	{Name: KeyExpand, Action: "expand_group", Effect: EffectView, Binding: key.NewBinding(
		key.WithKeys("right"),
		key.WithHelp("→", "expand group"),
	)},
	{Name: KeyCollapseAll, Action: "collapse_all", Effect: EffectView, Binding: key.NewBinding(
		key.WithKeys("Z"),
		key.WithHelp("Z", "collapse/expand all"),
	)},
	// Observe, not View: the committed query lives on the list, and nothing
	// persists it — a relaunch comes up unfiltered.
	{Name: KeyFilter, Action: "filter", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "filter sessions"),
	)},
	{Name: KeyCopyBranch, Action: "copy_branch", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("y"),
		key.WithHelp("y", "copy branch name"),
	)},
	{Name: KeyCopyContent, Action: "copy_content", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("Y"),
		key.WithHelp("Y", "copy pane/diff"),
	)},
	{Name: KeyShrinkList, Action: "shrink_list", Effect: EffectView, Binding: key.NewBinding(
		key.WithKeys("<"),
		key.WithHelp("<", "shrink list"),
	)},
	{Name: KeyGrowList, Action: "grow_list", Effect: EffectView, Binding: key.NewBinding(
		key.WithKeys(">"),
		key.WithHelp(">", "grow list"),
	)},
	// Backslash: a free, unshifted key (a reviewer may prefer a mnemonic — see
	// the PR). The label reads like a leaning divider between the two panes it
	// re-proportions.
	{Name: KeyLayoutPreset, Action: "layout_preset", Effect: EffectView, Binding: key.NewBinding(
		key.WithKeys("\\"),
		key.WithHelp("\\", "cycle layout"),
	)},
	{Name: KeyTabPreview, Action: "tab_preview", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("1"),
		key.WithHelp("1", "preview tab"),
	)},
	{Name: KeyTabDiff, Action: "tab_diff", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("2"),
		key.WithHelp("2", "diff tab"),
	)},
	// Opening the terminal tab starts a shell. TerminalPane.EnsureSession
	// (ui/terminal.go) runs tmux new-session for <name>_term with the user's
	// $SHELL in the instance's working directory, lazily, the first time the tab
	// resolves a capture target — so this key's one job is to put an unrestricted
	// shell in the worktree, which is a fleet session that outlives the keypress.
	// A gate on this classification wants the TAB, not the key: block the surface
	// and the two cycling keys above can go back to observing.
	//
	// #701 built that surface gate (home.shellStartRefused, app/app_frames.go) but
	// wired only its own condition to it — an action in flight — so all three keys
	// stay EffectMutate: in an idle TUI the shell still starts. #522 is what makes
	// the reclassification true, by adding --readonly to the same predicate; the
	// exemption in the opener rule above is for a gate on THIS classification, and
	// a busy gate is not one.
	{Name: KeyTabTerminal, Action: "tab_terminal", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("3"),
		key.WithHelp("3", "terminal tab"),
	)},
	// Both panels write config.json, which is why neither is EffectView: what they
	// change is the configuration, not the arrangement of the view.
	{Name: KeySettings, Action: "settings", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys(","),
		key.WithHelp(",", "settings"),
	)},
	{Name: KeyAccounts, Action: "accounts", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("@"),
		key.WithHelp("@", "accounts"),
	)},
	// The one opener that does NOT inherit what its surface can do, because this
	// classification already covers it: runPaletteAction (app/palette.go) re-enters
	// the keypress tail (focus router, then dispatchAction), so every row the
	// palette can run is an Entry with an Effect of its own. Opening the picker
	// changes nothing.
	{Name: KeyCommandPalette, Action: "command_palette", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("ctrl+k"),
		key.WithHelp("ctrl-k", "command palette"),
	)},
	// The opposite case: the user's own verbs run shell in the worktree, they are
	// config rather than Registry entries (see keys.go on why this is a leader key),
	// so nothing downstream classifies them. This key is their only gate.
	{Name: KeyCustomCommands, Action: "custom_commands", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("!"),
		key.WithHelp("!", "custom commands"),
	)},
	{Name: KeyAttachToggle, Action: "attach_toggle", Layer: LayerBoth, Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("ctrl+q"),
		key.WithHelp("ctrl-q", "attach/detach"),
	)},
	// Hint mode copies a match to the clipboard, and its capital form also opens
	// what can be opened. The two branches of actHint (app/app_hints.go) differ in
	// WHICH opener, and only the image one refuses the OS: an IMAGE PATH renders in
	// Atrium's own overlay, because handing it to xdg-open over SSH would launch a
	// viewer on the wrong machine (#398). A URL does go to the OS opener, via
	// actions.OpenInBrowser — and OpenableURL accepts file://, so a local file
	// spelled that way goes with it. Reading a file and handing a target to the
	// OS are both fine here; neither changes anything Atrium owns.
	{Name: KeyHints, Action: "hints", Effect: EffectObserve, Binding: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "copy/open from screen"),
	)},
	// The classification most likely to be got wrong, and the most expensive to get
	// wrong. It changes NOTHING in Atrium: it taps Enter at the selected session's
	// visible prompt (approveSelected → Instance.ApprovePrompt), which authorizes
	// the AGENT to do whatever it was asking about. Read as "observing" because
	// Atrium's own state is untouched, a read-only mode would let a bystander
	// approve an `rm -rf`.
	{Name: KeyApprove, Action: "approve", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("a"),
		key.WithHelp("a", "approve"),
	)},
	// Starts (or stops) a real process in the session's worktree, on its own port.
	{Name: KeyRunCommand, Action: "run_command", Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("d"),
		key.WithHelp("d", "run/stop dev command"),
	)},

	// Documented-only keys: real keys the TUI's dispatch map never sees, kept
	// here so generated help can reference them (see keys.go for each one's
	// story).
	//
	// They carry an Effect like everything else. Nothing gates them today —
	// their keys are consumed before the dispatch lookup or by the attach layer,
	// which is what DocOnly means — but "exhaustive" is the property that makes
	// the zero value work, and an entry exempted from classification is an entry
	// whose reclassification nobody would review.
	// Mutate for the same reason KeyEnter is, and it must not disagree with it: a
	// cycle is not a view change but a real detach-and-attach (cycleTarget,
	// app/app_session.go, says so), so each hop hands the raw keyboard to a
	// DIFFERENT agent. A classification that refused the attach key and permitted
	// this one would be inconsistent with itself.
	{Name: KeySessionCycle, DocOnly: true, Layer: LayerAttached, Effect: EffectMutate, Binding: key.NewBinding(
		key.WithKeys("ctrl+pgup", "ctrl+pgdown"),
		key.WithHelp("ctrl-pgup/pgdn", "cycle sessions"),
	)},
	// EffectView, not Observe, and the help text is why it reads wrong: esc's
	// roles are the app package's escLadder, and the desc names only the first
	// and third rungs. The last rung backs out of the focus LAYOUT preset via
	// exitFocusLayout -> applyLayoutPreset -> appState.SetLayout, which saves
	// state.json — the same write that makes the layout and split keys EffectView.
	// Classify the handler, not the label: reachable as layout-key-into-focus, then
	// esc.
	{Name: KeyEscape, DocOnly: true, Effect: EffectView, Binding: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "exit scroll / clear filter"),
	)},
	{Name: KeyRedraw, DocOnly: true, Effect: EffectObserve, Binding: key.NewBinding(
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

// effects maps each registered action to its Effect, for EffectOf. Derived from
// Registry, and immutable for the same reason layers is: an override moves an
// action's keys, never what pressing it can change.
var effects = func() map[KeyName]Effect {
	m := make(map[KeyName]Effect, len(Registry))
	for _, e := range Registry {
		m[e.Name] = e.Effect
	}
	return m
}()

// EffectOf reports what the named action can change. Gates read it to decide
// whether to run an action at all — the busy-gate's allowlist is held to it by
// TestBusyAllowlistNeverAdmitsAMutation (app/), and --readonly (#522) will refuse
// on EffectMutate.
//
// A name no Entry owns — KeyScreensaver, which is deliberately absent from
// Registry — returns EffectUnset, not EffectObserve. That is what makes a caller
// fail closed: an unclassified name is never "safe by default", here any more
// than in the Entry.
func EffectOf(name KeyName) Effect {
	return effects[name]
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

// PaneFocusHints teaches the bar while the active pane is scroll-captured: the
// nav keys scroll the pane and esc leaves scroll mode. Unlike its siblings
// above this is not a MenuState's vocabulary — the app derives the capture
// from the pane state and pushes it into the menu at render time
// (Menu.SetPaneFocus), so the bar cannot outlive a scroll mode that exits
// inside the pane, and it never renders when the capture is absent (the app
// gates the push on ActivePaneInScrollMode, not on focus alone).
//
// The move label names the keys the focus router actually resolves
// (routeFocusKey, app package); esc is reserved, so it needs no lookup. Esc's
// description names the mechanism, not a destination: in the focus layout the
// list is hidden, so "back to list" would promise a frame esc does not
// produce, while leaving scroll mode is — by that push gate — always what the
// first esc does while this bar shows. Explicit focus without scroll capture
// (esc pops focus instead) keeps the default bar; its vocabulary is #806's.
func PaneFocusHints() []key.Binding {
	hints := make([]key.Binding, 0, 2)
	// With both nav actions unbound the entry drops out whole rather than
	// rendering a keyless " scroll" segment (VisualModeHints' rule): scroll
	// mode stays reachable by wheel and shift+↑, but the bar can only teach
	// keys that exist.
	if move := Label(append(keysOf(KeyUp), keysOf(KeyDown)...)); move != "" {
		hints = append(hints, key.NewBinding(key.WithHelp(move, "scroll")))
	}
	return append(hints, key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "exit scroll")))
}

// ModeHintTables enumerates every mode hint table above, keyed by the bar
// variant that renders it. The drift guards consume this one list — the bar
// scan in the ui package seeds its known keys from it, demands a scan arm per
// entry, and requires each table to render in its own arm's bar; the app
// package's click-synthesis sweep checks every key it carries. What forces an
// entry INTO this list differs by variant kind: a new MenuState trips the
// scan's menuStateCount sentinel, and its bar then fails the unknown-token
// direction until its table lands here; a variant behind a render-time bool
// (the pane-focus bar's shape) is rendered by no state the scan walks, so it
// must be added here by hand — the residual gap the scan's header names.
func ModeHintTables() map[string][]key.Binding {
	return map[string][]key.Binding{
		"filter":       FilterModeHints(),
		"hints":        HintModeHints(),
		"visual":       VisualModeHints(),
		"diff-comment": DiffCommentModeHints(),
		"pane-focus":   PaneFocusHints(),
	}
}

// keysOf is an action's bound key strings, empty when the user unbound it.
func keysOf(name KeyName) []string {
	return GlobalKeyBindings[name].Keys()
}
