package keys

// HelpRow is one cheatsheet line: a key column derived from the referenced
// bindings' Help().Key labels (never free text — a label that lies about its
// key can't be written), and the row's prose. Rows may merge several bindings
// ("↑/k ↓/j" is KeyUp + KeyDown) because the cheatsheet is a curated
// projection, not a mechanical dump.
type HelpRow struct {
	// Keys are the bindings this row's key column documents (≥1).
	Keys []KeyName
	// Mentions are bindings taught inside this row's Desc prose instead of a
	// key column of their own (space lives in the multi-select row). Each
	// mention's Help().Key must appear verbatim in Desc — pinned by
	// TestHelpGroups_MentionsAreRendered, so deleting the prose undocuments
	// the key loudly. Keep this rare.
	Mentions []KeyName
	// Desc is the cheatsheet prose. Rows whose keys are LayerAttached render
	// with a generated "in a session: " prefix — don't write it here.
	Desc string
	// Compact joins the key labels with " " instead of " / " (paired arrows).
	Compact bool
}

// HelpGroup is one titled cheatsheet section; order on screen is slice order.
type HelpGroup struct {
	Title string
	Rows  []HelpRow
}

// HelpGroups is the ? cheatsheet's layout: the one authored copy of the help
// text, projected to the screen by app/help.go. The drift guards in
// help_layout_test.go tie it to the registry in both directions.
//
// It is built per call rather than held in a package-level var because a few
// Desc strings name a key inside their prose, and those have to be read from
// the live binding: a var would freeze them at whatever the keymap was when the
// package initialised, which is exactly one edit away from being wrong.
//
// One desc still spells its keys by hand — the diff-comment row's "↑↓/j/k move,
// shift+↑↓/J/K extend". Those four keys do resolve through the registry
// (app/app_diffcomment.go), but the label compresses two bindings into one
// glyph pair, and that compression is not recoverable from the labels: "↑/k"
// and "↓/j" do not mechanically become "↑↓/j/k". It is the same case as
// KeySessionCycle's "ctrl-pgup/pgdn" (see label.go), and it is grouped with the
// mode-hint tables' compound labels in registry.go, which have the same shape
// and want the same answer.
func HelpGroups() []HelpGroup {
	return []HelpGroup{
		{Title: "Navigate", Rows: []HelpRow{
			{Keys: []KeyName{KeyUp, KeyDown}, Compact: true, Desc: "move selection"},
			{Keys: []KeyName{KeyNextUnread, KeyNextNeedsInput}, Desc: "jump to next unread / blocked"},
			{Keys: []KeyName{KeyTab, KeyShiftTab}, Desc: "next / prev pane"},
			// Compact: "1 / 2 / 3 / 4" is 13 cells, one over the cheatsheet's
			// key column; the space-joined form fits with room to spare.
			{Keys: []KeyName{KeyTabPreview, KeyTabDiff, KeyTabTerminal, KeyTabInspector}, Compact: true, Desc: "jump to preview / diff / terminal / inspector"},
			{Keys: []KeyName{KeyShiftUp, KeyShiftDown}, Compact: true, Desc: "scroll the active pane"},
			{Keys: []KeyName{KeyShrinkList, KeyGrowList}, Desc: "shrink / grow the session list (or drag the divider)"},
			{Keys: []KeyName{KeyLayoutPreset}, Desc: "cycle layout presets (monitor / default / review / focus)"},
			{Keys: []KeyName{KeyEscape}, Desc: "exit scroll mode / clear filter / leave focus"},
		}},
		{Title: "Manage", Rows: []HelpRow{
			{Keys: []KeyName{KeyNew}, Desc: "new session (form, name first)"},
			{Keys: []KeyName{KeyPrompt}, Desc: "new session (form, project first)"},
			{Keys: []KeyName{KeySmartDispatch}, Desc: "smart new (describe it; auto-routes to a project)"},
			{Keys: []KeyName{KeyRename}, Desc: "rename session (label only)"},
			{Keys: []KeyName{KeyAutoName}, Desc: "auto-name session (via its agent)"},
			{Keys: []KeyName{KeyMute}, Desc: "mute / unmute this session's notifications"},
			{Keys: []KeyName{KeyFilter}, Desc: "filter sessions"},
			{Keys: []KeyName{KeyMultiSelect}, Mentions: []KeyName{KeyToggleMark},
				Desc: "multi-select: " + LabelOf(KeyToggleMark) + " marks, " +
					LabelOf(KeyPause) + "/" + LabelOf(KeyResume) + "/x act on the marked set"},
		}},
		{Title: "Handoff", Rows: []HelpRow{
			{Keys: []KeyName{KeyEnter}, Desc: "attach to the selected session"},
			{Keys: []KeyName{KeyAttachToggle}, Desc: "toggle attach/detach (detach when in, attach from the list)"},
			{Keys: []KeyName{KeyKill}, Desc: "kill the selected/attached session (press it twice to confirm — every keyed confirmation takes its own key twice)"},
			{Keys: []KeyName{KeyUndoKill}, Desc: "undo the last kill: rebuild its branch, worktree and agent"},
			{Keys: []KeyName{KeySessionCycle}, Desc: "cycle to prev / next session in the repo group"},
			{Keys: []KeyName{KeyQuickSend}, Desc: "send a message (without attaching)"},
			{Keys: []KeyName{KeyDiffComment}, Desc: "diff tab: comment on a line or range → queue it to the agent (↑↓/j/k move, shift+↑↓/J/K extend, enter comment, esc exit)"},
			{Keys: []KeyName{KeyQueue}, Desc: "manage queued prompts (list / cancel)"},
			// "Esc Esc" is a literal for the same reason the row below spells "enter":
			// it is Claude's own rewind chord, pressed inside the agent's pane after
			// attaching, so it is not in Atrium's keymap and no override can move it.
			// Reading it from the registry would be reading the wrong keymap.
			{Keys: []KeyName{KeyCheckpoints}, Desc: "claude sessions: list the checkpoints it took before each prompt, then attach to rewind one (Esc Esc)"},
			// "enter" is a literal here on purpose, and is the one key in this table
			// that has to stay one: approve sends Enter into the agent's own pane
			// (ApprovePrompt), so the parenthetical describes the agent's UI rather
			// than Atrium's keymap. Reading it from KeyEnter would make rebinding
			// Atrium's open key rewrite advice about a key the agent never sees.
			{Keys: []KeyName{KeyApprove}, Desc: "approve the agent's prompt (enter picks its default); on idle claude, accept the suggested prompt"},
			{Keys: []KeyName{KeyRunCommand}, Desc: "start / stop the repo's run_command (dev server) on this session's port"},
			{Keys: []KeyName{KeyPause}, Desc: "pause: stop the agent, commit changes, free the worktree"},
			{Keys: []KeyName{KeyPauseAll}, Desc: "pause all active sessions in the current view"},
			{Keys: []KeyName{KeySubmit}, Desc: "commit & push branch"},
			{Keys: []KeyName{KeyCreate}, Desc: "create a PR for the pushed branch (gh)"},
			{Keys: []KeyName{KeyMerge}, Desc: "merge the session's PR (squash)"},
			{Keys: []KeyName{KeyOpenPR}, Desc: "open the session's PR in the browser"},
			{Keys: []KeyName{KeyResume}, Desc: "resume a paused session"},
			{Keys: []KeyName{KeyResumeAll}, Desc: "resume all paused sessions in the current view"},
			{Keys: []KeyName{KeyCopyBranch}, Desc: "copy branch name to clipboard"},
			{Keys: []KeyName{KeyCopyContent}, Desc: "copy the active tab's content, unstyled"},
			{Keys: []KeyName{KeyHints}, Desc: "copy/open URLs & paths from the preview"},
		}},
		{Title: "Groups", Rows: []HelpRow{
			{Keys: []KeyName{KeyMoveDown, KeyMoveUp}, Desc: "reorder within a repo group"},
			{Keys: []KeyName{KeyMoveGroupUp, KeyMoveGroupDown}, Desc: "move a whole group up / down"},
			{Keys: []KeyName{KeyMoveAccountUp, KeyMoveAccountDown}, Desc: "move an account cluster up / down"},
			{Keys: []KeyName{KeyCollapse, KeyExpand}, Desc: "collapse / expand group"},
			{Keys: []KeyName{KeyCollapseAll}, Desc: "collapse / expand all"},
		}},
		{Title: "Other", Rows: []HelpRow{
			{Keys: []KeyName{KeyHelp}, Desc: "toggle this cheatsheet"},
			{Keys: []KeyName{KeyCommandPalette}, Desc: "command palette: find any action by name and run it"},
			{Keys: []KeyName{KeyCustomCommands}, Desc: "custom commands: your own verbs over the selected session"},
			{Keys: []KeyName{KeySettings}, Desc: "settings"},
			{Keys: []KeyName{KeyAccounts}, Desc: "accounts (Claude / GitHub)"},
			{Keys: []KeyName{KeyCmdLog}, Desc: "command log: the tmux/git/gh commands Atrium ran (filter all / session / failures)"},
			{Keys: []KeyName{KeyRedraw}, Desc: "force a full redraw of the screen"},
			{Keys: []KeyName{KeyQuit}, Desc: "quit"},
		}},
	}
}
