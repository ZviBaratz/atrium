package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// gemini_confirm_pane_test.go — the tool-confirmation dialog's ladder (#736), and the busy
// marker's (#666), both driven in the same sessions.
//
// Driven natively at gemini-cli 0.55.1 on 2026-08-17 with `drive-agent.sh fresh <width>
// [height]`, one fresh session per rung. NOT a resize ladder, and for this dialog that is
// measured rather than inherited: a resized width-40 rung taken from the same live dialog
// diverged from the native one, carrying torn fragments of the wider frame exactly as #713
// found for the folder-trust dialog. Two gemini dialogs now, so treat resize-equals-native as
// false for this CLI unless a specific dialog has been shown otherwise.
//
// ISOLATION, and why these are the first gemini panes past the trust dialog. Every earlier
// gemini capture in this package is a startup screen, because reaching a confirmation needs an
// authenticated session and a real turn, and drive-agent.sh drove against the real ~/.gemini.
// These were driven with ATR_CAP_ENV (#736) pointing GEMINI_CLI_HOME and
// GEMINI_FORCE_FILE_STORAGE at a throwaway home, so the run could authenticate without being
// able to touch the developer's config. The isolation was checked for render-neutrality rather
// than assumed: the trust gate driven at width 80 under the same ATR_CAP_ENV came back
// byte-identical to geminiTrustGatePane80, which was driven without it.
//
// That check is PROVENANCE, like the date and the auth type above it, not an invariant the
// suite holds — the isolated capture was a duplicate of a fixture already in the tree, and
// committing it would have bought an equality between two literals that only a human edit can
// break. What a later drive should redo is the check, not the assertion.
//
// AUTH TYPE IS PART OF THE PROVENANCE. These were driven under `gemini-api-key`, not
// `oauth-personal`, and not by preference: at 0.55.1 oauth-personal returns
// IneligibleTierError ("This client is no longer supported for Gemini Code Assist for
// individuals"), so an individual Google account cannot reach ANY of these screens. The splash
// row "Authenticated with gemini-api-key /auth" is visible in the width-120 rung and is the
// only place the two differ that these captures show.
//
// The workspace is a scratch git repo under /tmp holding one README.md, and the command the
// model was asked to run is `rm -f README.md` — chosen because the policy engine auto-allows
// anything it judges safe (an `echo` ran with no dialog at all, costing a turn to learn), and
// because an approved one would have been inert. It was never approved: every rung was
// dismissed with Escape, which the bundle maps to ToolConfirmationOutcome.Cancel — the same
// outcome as the "No, suggest changes (esc)" row.

const geminiConfirmPane120 = `  ▗▟▀    Authenticated with gemini-api-key /auth
 ▝▀


Tips for getting started:
1. Create GEMINI.md files to customize your interactions
2. /help for more information
3. Ask coding questions, edit code or run commands
4. Be specific for the best results
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 > Use the run_shell_command tool to run exactly: echo atrium-736. Do not explain anything.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀

╭──────────────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│ ✓  Shell echo atrium-736                                                                                         │
│                                                                                                                  │
│ atrium-736                                                                                                       │
│                                                                                                                  │
╰──────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯

✦ atrium-736
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 > Use the run_shell_command tool to run exactly: rm -f README.md. Do not explain anything.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
✦ I will run a command to delete the README.md file from the workspace directory. This will modify the filesystem by
  removing the file.

╭──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│ ? Shell  rm -f README.md                                                                                             │
│ ╭──────────────────────────────────────────────────────────────────────────────────────────────────────────────────╮ │
│ │ rm -f README.md                                                                                                  │ │
│ ╰──────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯ │
│ Allow execution of [Shell]?                                                                                          │
│                                                                                                                      │
│ ● 1. Allow once                                                                                                      │
│   2. Allow for this session                                                                                          │
│   3. No, suggest changes (esc)                                                                                       │
╰──────────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯
`

const geminiConfirmPane45x19 = `   anything.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
✦ This command will permanently delete the
  README.md file from the workspace root
  directory.

╭───────────────────────────────────────────╮
│ ? Shell  rm -f README.md                  │
│ ╭───────────────────────────────────────╮ │
│ │ rm -f README.md                       │ │
│ ╰───────────────────────────────────────╯ │
│ Allow execution of [Shell]?               │
│                                           │
│ ● 1. Allow once                           │
│   2. Allow for this session               │
│   3. No, suggest changes (esc)            │
╰───────────────────────────────────────────╯
`

const geminiConfirmPane40 = `
 ▝▜▄
   ▝▜▄
  ▗▟▀
 ▝▀

 Gemini CLI v0.55.1

 Authenticated with gemini-api-key /auth


Tips for getting started:
1. Create GEMINI.md files to customize
your interactions
2. /help for more information
3. Ask coding questions, edit code or
run commands
4. Be specific for the best results
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 > Use the run_shell_command tool to
   run exactly: rm -f README.md. Do not
   explain anything.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
✦ I will execute the command to remove
  the README.md file from the workspace.

╭──────────────────────────────────────╮
│ ? Shell  rm -f README.md             │
│ ╭──────────────────────────────────╮ │
│ │ rm -f README.md                  │ │
│ ╰──────────────────────────────────╯ │
│ Allow execution of [Shell]?          │
│                                      │
│ ● 1. Allow once                      │
│   2. Allow for this session          │
│   3. No, suggest changes (esc)       │
╰──────────────────────────────────────╯
`

const geminiConfirmPane34 = `
 Gemini CLI v0.55.1

 Authenticated with gemini-api-key
 /auth


Tips for getting started:
1. Create GEMINI.md files to custo
mize
your interactions
2. /help for more information
3. Ask coding questions, edit code
 or
run commands
4. Be specific for the best result
s

▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 > Use the run_shell_command tool
   to run exactly: rm -f
   README.md. Do not explain
   anything.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
✦ I will delete the README.md file
  from the workspace root.

╭────────────────────────────────╮
│ ? Shell  rm -f README.md       │
│ ╭────────────────────────────╮ │
│ │ rm -f README.md            │ │
│ ╰────────────────────────────╯ │
│ Allow execution of [Shell]?    │
│                                │
│ ● 1. Allow once                │
│   2. Allow for this session    │
│   3. No, suggest changes (esc) │
╰────────────────────────────────╯
`

const geminiConfirmPane33 = `
 Authenticated with gemini-api-ke
 /auth



Tips for getting started:
1. Create GEMINI.md files to cust
omize
your interactions
2. /help for more information
3. Ask coding questions, edit cod
e or
run commands
4. Be specific for the best resul
ts

▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 > Use the run_shell_command
   tool to run exactly: rm -f
   README.md. Do not explain
   anything.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
✦ This command removes the
  README.md file from the
  workspace.

╭───────────────────────────────╮
│ ? Shell  rm -f README.md      │
│ ╭───────────────────────────╮ │
│ │ rm -f README.md           │ │
│ ╰───────────────────────────╯ │
│ Allow execution of [Shell]?   │
│                               │
│ ● 1. Allow once               │
│   2. Allow for this session   │
│   3. No, suggest changes (es… │
╰───────────────────────────────╯
`

const geminiConfirmPane24 = `✦ I will remove the
  README.md file from
  the workspace.

╭──────────────────────╮
│ ? Shell rm -f READM… │
│ ╭──────────────────╮ │
│ │ rm -f            │ │
│ │ README.md        │ │
│ ╰──────────────────╯ │
│ Allow execution of   │
│ [Shell]?             │
│                      │
│ ● 1. Allow once      │
│   2. Allow for this… │
│   3. No, suggest ch… │
╰──────────────────────╯
`

const geminiConfirmPane24x19 = `  permanently delete the
  README.md file from
  the workspace.

╭──────────────────────╮
│ ? Shell rm -f READM… │
│ ╭──────────────────╮ │
│ │ rm -f            │ │
│ │ README.md        │ │
│ ╰──────────────────╯ │
│ Allow execution of   │
│ [Shell]?             │
│                      │
│ ● 1. Allow once      │
│   2. Allow for this… │
│   3. No, suggest ch… │
╰──────────────────────╯
`

const geminiConfirmPane20 = `  README.md file
  from the

╭──────────────────╮
│ ?     rm -f REA… │
│ Shell            │
│ ╭──────────────╮ │
│ │ rm -f        │ │
│ │ README.md    │ │
│ ╰──────────────╯ │
│ Allow execution  │
│ of [Shell]?      │
│                  │
│ ● 1. Allow once  │
│   2. Allow for … │
│   3. No, sugges… │
╰──────────────────╯
`

// geminiConfirmDismissedPane is the one capture in this file the header's "one fresh session
// per rung, NOT a resize ladder" does not describe, and the exception is in its own bytes: it
// is a 120-column pane whose scrollback holds 40-, 28- and 24-wide dialog frames and a torn
// 20-column fragment. gemini's dialog box is width: mainAreaWidth, so at 120 columns it draws
// the ~116-wide frame near the bottom; the narrow ones can only be residue from the widths the
// terminal held earlier. It is the same session one keystroke later, and that session had been
// resized across the drive.
//
// Kept, and kept HERE rather than re-driven, because the residue cannot flatter the assertion
// it backs. An earlier draft said it made the control HARDER; measured, it does neither. The
// frames are unreachable — this pane's last two non-empty lines are the footer, neither is a
// box bottom border, and trailingBelowBoxCap is 1, so bottomBoxBlock returns false before
// reading anything above them — and they carry neither label to raise a false positive with.
// Inert, not adversarial. What makes this a real negative control is the two facts below it:
// no bottom-most box, and no option row anywhere on the pane.
//
// What a re-drive must not do is go looking for the fresh 120-column session that produced
// this. There wasn't one.
const geminiConfirmDismissedPane = `│                                                                                                                      │

╭──────────────────────────────────────╮
│ ? Shell  rm -f README.md             │
│ ╭──────────────────────────────────╮ │
│ │ rm -f README.md                  │ │
│ ╰──────────────────────────────────╯ │

╭──────────────────────────╮
│ ? Shell  rm -f README.md │
│ ╭──────────────────────╮ │
│ │ rm -f README.md      │ │
│ ╰──────────────────────╯ │
│ Allow execution of

╭──────────────────────╮
│ ? Shell rm -f READM… │
│ ╭──────────────────╮ │
│ │ rm -f            │ │
│ │ README.md        │ │
│ ╰──────────────────╯ │

╭──────────────────────────────────────────────────────────────────────────────────────────────────────────────────╮
│ -  Shell rm -f README.md                                                                                         │
│                                                                                                                  │
╰──────────────────────────────────────────────────────────────────────────────────────────────────────────────────╯


ℹ Request cancelled.


                                                                                                        ? for shortcuts
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
 Shift+Tab to accept edits
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message or @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace (/directory)                               branch                     sandbox                         /model
 /tmp/atrium-capture/gemini/repo                      main                       no sandbox                        Auto
`

// The pane #736 is ABOUT, driven rather than composed: a user typing a message that quotes the
// decline label, captured under gemini 0.55.1 with NOENTER=1 so the text sits in the composer
// unsubmitted. It is the only capture in this file taken that way, and the only one that
// justifies NOENTER existing at all — see drive-agent.sh's help. Its box rules measure 45
// columns; the row count is not recoverable from the bytes, so it is not claimed here.
//
// Provenance is stated here because it is the fixture the whole issue turns on and it was the
// one const in this file without a header. The old flat matcher fires on it; nothing may.
const geminiComposerQuotingTheLiteralPane = `


─────────────────────────────────────────────
 Shift+Tab to accept edits

▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 > the reviewer asked about Trust folder
   and whether No, suggest changes (esc)
   would be quoted here, and this
   sentence keeps going so that it wraps
   well past the height of a nineteen row
   preview pane, which is what the issue
   is about, and it must keep going
   further still to be certain of
   overflowing the composer box entirely
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace               branch   /model
 /tmp/.../fresh45        main     Auto     …
`

const geminiIdlePane055 = `
 ▝▜▄     Gemini CLI v0.55.1
   ▝▜▄
  ▗▟▀    Authenticated with gemini-api-key /auth
 ▝▀


Tips for getting started:
1. Create GEMINI.md files to customize your interactions
2. /help for more information
3. Ask coding questions, edit code or run commands
4. Be specific for the best results


                                                                                                        ? for shortcuts
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
 Shift+Tab to accept edits
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message or @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace (/directory)                               branch                     sandbox                         /model
 /tmp/atrium-capture/gemini/repo                      main                       no sandbox                        Auto
`

const geminiBusyPane120 = `
 ▝▜▄     Gemini CLI v0.55.1
   ▝▜▄
  ▗▟▀    Authenticated with gemini-api-key /auth
 ▝▀


Tips for getting started:
1. Create GEMINI.md files to customize your interactions
2. /help for more information
3. Ask coding questions, edit code or run commands
4. Be specific for the best results
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 > Use the run_shell_command tool to run exactly: echo atrium-736. Do not explain anything.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀


 ⠸ Thinking... (esc to cancel, 3s)                                                                      ? for shortcuts
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
 Shift+Tab to accept edits
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message or @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace (/directory)                               branch                     sandbox                         /model
 /tmp/atrium-capture/gemini/repo                      main                       no sandbox                        Auto
`

const geminiBusyPane45x19 = ` > Use the run_shell_command tool to run
   exactly: rm -f README.md. Do not explain
   anything.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
✦ This command will permanently delete the
  README.md file from the workspace root
  directory.


 ⠴ Thinking... (esc to cancel, 8s)
─────────────────────────────────────────────
 Shift+Tab to accept edits

▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message or @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace               branch   /model
 /tmp/.../fresh45x19     main     Auto     …
`

const geminiBusyPane40 = `
 ▝▜▄
   ▝▜▄
  ▗▟▀
 ▝▀

 Gemini CLI v0.55.1

 Authenticated with gemini-api-key /auth


Tips for getting started:
1. Create GEMINI.md files to customize
your interactions
2. /help for more information
3. Ask coding questions, edit code or
run commands
4. Be specific for the best results
▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 > Use the run_shell_command tool to
   run exactly: rm -f README.md. Do not
   explain anything.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀


 ⠧ Thinking... (esc to cancel, 2s)
────────────────────────────────────────
 Shift+Tab to accept edits

▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message or @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace (/directory)     branch
 /tmp/.../gemini/fresh40    main      …
`

const geminiBusyPane34 = `   ▝▜▄
  ▗▟▀
 ▝▀

 Gemini CLI v0.55.1

 Authenticated with gemini-api-key
 /auth


Tips for getting started:
1. Create GEMINI.md files to custo
mize
your interactions
2. /help for more information
3. Ask coding questions, edit code
 or
run commands
4. Be specific for the best result
s

▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 > Use the run_shell_command tool
   to run exactly: rm -f
   README.md. Do not explain
   anything.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀


 ⠏ Thinking... (esc to cancel, 2s)
──────────────────────────────────
 Shift+Tab to accept edits

▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message or
   @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace             branch
 /tmp/.../fresh34      main     …
`

const geminiBusyPane33 = `  ▗▟▀
 ▝▀

 Gemini CLI v0.55.1

 Authenticated with gemini-api-ke
 /auth



Tips for getting started:
1. Create GEMINI.md files to cust
omize
your interactions
2. /help for more information
3. Ask coding questions, edit cod
e or
run commands
4. Be specific for the best resul
ts

▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 > Use the run_shell_command
   tool to run exactly: rm -f
   README.md. Do not explain
   anything.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀


 ⠹ Thinking... (esc to cancel, 3s
─────────────────────────────────
 Shift+Tab to accept edits

▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message or
   @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace            branch
 /tmp/.../fresh33     main     …
`

const geminiBusyPane24 = `   tool to run exactly:
   rm -f README.md. Do
   not explain
   anything.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀


 ⠋ Thinking... (esc to c
────────────────────────
 Shift+Tab to accept
 edits

▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your message
   or @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 workspace
 /tmp/.../fresh24     …
`

const geminiBusyPane20 = `   README.md. Do
   not explain
   anything.
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀


 ⠇ Thinking... (esc
────────────────────
 Shift+Tab to accept
 edits

▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄
 >   Type your
   message or
   @path/to/file
▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀
 branch   /model
 main     Auto     …
`

// The driven confirmation ladder, feeding paneCoverage["gemini/prompt/confirmation"] directly.
//
// Every rung fires, which is what paneCoverage requires — but the interesting number is where
// the SHIPPED literal stops being on screen. "No, suggest changes (esc)" is 25 cells and the
// option label renders wrap:"truncate" behind a 5-column row prefix inside a box costing 4
// columns, so it needs a pane 34 wide. 34 and 33 are both here, driven, for exactly that
// reason: they are the last width it survives and the first it does not.
var geminiConfirmLadder = []paneCapture{
	{name: "geminiConfirmPane20", width: 20, note: "cancel row elided to \"No, sugges…\"", pane: geminiConfirmPane20},
	{name: "geminiConfirmPane24", width: 24, note: "cancel row elided", pane: geminiConfirmPane24},
	{name: "geminiConfirmPane24x19", width: 24, note: "narrow and 19 rows; box still ends the pane", pane: geminiConfirmPane24x19},
	{name: "geminiConfirmPane33", width: 33, note: "first width the shipped literal loses \"c)\"", pane: geminiConfirmPane33},
	{name: "geminiConfirmPane34", width: 34, note: "last width the shipped literal survives", pane: geminiConfirmPane34},
	{name: "geminiConfirmPane40", width: 40, note: "literal intact", pane: geminiConfirmPane40},
	{name: "geminiConfirmPane45x19", width: 45, note: "the agent pane a plain 70x24 terminal produces", pane: geminiConfirmPane45x19},
	{name: "geminiConfirmPane120", width: 120, note: "a completed tool box above the dialog", pane: geminiConfirmPane120},
}

// The busy ladder is SHORTER than the confirmation one, and the gap is the finding. Four of
// the seven driven rungs missed when they were captured, for TWO different reasons, and
// keeping them apart is what let one half be fixed: the marker sat one row past MarkerWindow
// at 34 and 33, so the window moved 8 -> 9 and those two rungs are covered here. The other
// half is below, and no window reaches it.
var geminiBusyLadder = []paneCapture{
	{name: "geminiBusyPane33", width: 33, note: "composer placeholder splits; marker 9 non-empty lines up", pane: geminiBusyPane33},
	{name: "geminiBusyPane34", width: 34, note: "composer placeholder splits; marker 9 non-empty lines up", pane: geminiBusyPane34},
	{name: "geminiBusyPane40", width: 40, note: "hint and placeholder both one row; marker 8 up", pane: geminiBusyPane40},
	{name: "geminiBusyPane45x19", width: 45, note: "19 rows, marker inside the window", pane: geminiBusyPane45x19},
	{name: "geminiBusyPane120", width: 120, note: "the wide LoadingIndicator layout", pane: geminiBusyPane120},
}

// Missed because the marker is NOT ON SCREEN AT ALL. The loading row truncates rather than
// wraps, so at these widths "esc to cancel" is cut mid-phrase and no window of any size
// reaches it — a content loss, exactly as the trust gate's option rows suffer at width 20.
var geminiBusyTruncatedRungs = []paneCapture{
	{name: "geminiBusyPane24", width: 24, note: "loading row cut at \"(esc to c\"", pane: geminiBusyPane24},
	{name: "geminiBusyPane20", width: 20, note: "loading row cut at \"(esc\"", pane: geminiBusyPane20},
}

// geminiConfirmationVisible's contract, stated as behaviour on driven bytes: it fires on every
// rung of the ladder and on nothing else this file holds.
func TestGeminiConfirmationFiresOnEveryDrivenRung(t *testing.T) {
	for _, c := range geminiConfirmLadder {
		t.Run(c.label(), func(t *testing.T) {
			m, ok := gemini.DetectPrompt(c.pane)
			require.True(t, ok, "the dialog is on screen and must classify as a prompt")
			require.Equal(t, "confirmation", m.Name)
			require.True(t, m.NoAutoTap, "Enter here runs the command; it must never be auto-tapped")
		})
	}
}

// The half the issue is about, on a DRIVEN pane rather than a composed one: a user typing a
// message that happens to quote the decline label. The old flat matcher fires on this, so the
// session reports needs-input, AwaitingInput goes false and the queued first prompt is
// withheld — #342's direction, triggered by the user's own keystrokes.
//
// The premise is asserted alongside the verdict: if InputBoxVisible were false this pane would
// not be a composer at all and the test would prove nothing about the case it names.
func TestGeminiConfirmationIgnoresAComposerQuotingTheLiteral(t *testing.T) {
	pane := geminiComposerQuotingTheLiteralPane
	require.Contains(t, pane, "No, suggest changes (esc)", "the premise: the literal is on screen")
	require.True(t, gemini.InputBoxVisible(pane), "the premise: this is a composer")

	_, ok := gemini.DetectPrompt(pane)
	require.False(t, ok, "a quoted literal in the composer is not a live confirmation")
}

// The other control, and the one that separates an OPEN dialog from an answered one. Escape
// dismissed the dialog, the composer came back, and nothing here may still read as a prompt —
// a dialog left matching forever is what keeps a queued first prompt undelivered. Read
// geminiConfirmDismissedPane's own note on where those bytes came from before re-driving it.
func TestGeminiConfirmationDropsOnceDismissed(t *testing.T) {
	_, ok := gemini.DetectPrompt(geminiConfirmDismissedPane)
	require.False(t, ok, "the dialog is gone; nothing may still classify as a prompt")
	_, gated := gemini.GateUp(geminiConfirmDismissedPane)
	require.False(t, gated)

	// WHY it drops, stated as two separate facts, because the verdict above holds for a
	// structural reason while the interesting one is about content.
	//
	// The second is what refuted #746's first disclosure. That draft argued no predicate could
	// tell a live dialog from an answered one because an answered dialog lingers in scrollback
	// looking the same. It does not: gemini replaces the entire dialog with a two-row tool
	// RESULT box, and neither option label is anywhere on the pane. Any predicate keyed on the
	// pair therefore separates live from answered on its own.
	_, boxed := bottomBoxBlock(geminiConfirmDismissedPane)
	require.False(t, boxed, "the pane ends in the composer's footer, so there is no bottom-most box")
	require.NotContains(t, geminiConfirmDismissedPane, geminiAllowRow,
		"an answered dialog keeps no option row: the allow label is gone")
	require.NotContains(t, geminiConfirmDismissedPane, geminiCancelRow,
		"and so is the cancel label")
}

// shippedCancelLabel is the full cancel row as gemini 0.55.1 draws it — the literal #736
// proposed keying on. It lives here, above both tests that need it, so the label and the
// prefix the matcher actually reads are compared against ONE spelling rather than two.
const shippedCancelLabel = "No, suggest changes (esc)"

// The measurement that overrides the issue. #736 proposes keeping "No, suggest changes (esc)"
// as the in-box content; driven, that literal is absent below width 34 — above every width
// Atrium's preview pane routinely produces. Keying on it would have re-shipped #713's mistake
// a third time, from a wide capture.
//
// Both halves are asserted. Without the second, the test would pass while the matcher keyed on
// something no narrow pane renders.
func TestGeminiConfirmationCancelRowTruncatesBelowWidth34(t *testing.T) {
	const shipped = shippedCancelLabel
	for _, c := range geminiConfirmLadder {
		t.Run(c.label(), func(t *testing.T) {
			if c.width >= 34 {
				require.Contains(t, c.pane, shipped, "at %d the shipped literal is still on screen", c.width)
			} else {
				require.NotContains(t, c.pane, shipped, "at %d it is truncated away", c.width)
			}
			// What the matcher actually keys on must be present at EVERY rung, or the
			// assertion above is measuring a literal nothing reads.
			require.Contains(t, c.pane, geminiAllowRow, "the first option row survives every driven width")
			require.Contains(t, c.pane, geminiCancelRow, "the cancel prefix survives every driven width")
		})
	}
}

// The matcher's width floor, which is set by the row the doc used NOT to reason about. A
// conjunction's floor is its widest term: the label column is paneWidth-9, "Allow once" is 10
// cells and the cancel prefix 7, so "Allow once" binds and the floor is a 19-column pane. The
// narrowest rung driven is 20, one column above it — and pane_width_test.go's header records
// that the preview width is not clamped to any minimum, so a narrower pane is not hypothetical.
//
// Measured on the driven bytes rather than on the arithmetic, because the arithmetic is the
// claim being checked: at width 20 the allow row still carries its whole label while the
// cancel row is ALREADY elided, which is what "one column of headroom, on the other literal"
// looks like from outside the box model.
func TestGeminiConfirmationFloorIsSetByTheAllowRow(t *testing.T) {
	labelCol := func(pane string, want string) string {
		for _, l := range strings.Split(pane, "\n") {
			if strings.Contains(l, want) {
				return l
			}
		}
		return ""
	}

	allow := labelCol(geminiConfirmPane20, "Allow once")
	require.NotEmpty(t, allow, "the premise: the narrowest driven rung still renders the allow row")
	require.NotContains(t, allow, "…", "the binding literal is NOT elided at width 20 — it fits, barely")

	cancel := labelCol(geminiConfirmPane20, geminiCancelRow)
	require.Contains(t, cancel, "…",
		"the other literal IS elided here, which is why 7 cells of headroom on it says nothing "+
			"about the matcher's floor")

	// The label column, MEASURED off the driven bytes rather than asserted from the box
	// arithmetic that predicts it. The cancel row is elided, so its label field is full: its
	// rune count IS the column. Everything the doc says about the floor rests on this number.
	const width = 20
	label := strings.TrimSuffix(strings.TrimPrefix(strings.Trim(cancel, "│"), "   3. "), " ")
	require.Equal(t, width-9, len([]rune(label)),
		"the label column is paneWidth-9; measured %q", label)
	// Against the matcher's own symbol, not against a second spelling of it. `width-9-1 ==
	// len("Allow once")` is 10 == 10 with nothing from the package on either side: shortening
	// the binding literal moves the floor from 19 to 18 and that assertion still passes.
	require.Equal(t, width-9-1, len([]rune(geminiAllowRow)),
		"the binding literal fills all but ONE cell of the label column at the narrowest driven "+
			"rung — which is what makes 19 the floor, and 20 the last rung that proves anything")
	require.Contains(t, allow, geminiAllowRow,
		"and it is THIS literal the driven row carries, so the two cannot drift apart")

	// The cancel term gets the one guard it can have, which is NOT a length. Nothing measurable
	// here bounds its specificity: shortening it to "No," keeps every assertion above green —
	// it still finds the row, still fits the elided field, and cannot move a floor it does not
	// bind. What a measurement DOES catch is drift to a string gemini never renders, so that is
	// what is asserted. Read the const's own comment before shortening it; the cost is an
	// over-broad conjunction, not a red test.
	require.True(t, strings.HasPrefix(shippedCancelLabel, geminiCancelRow),
		"the cancel term must be a prefix of the row gemini actually draws, got %q", geminiCancelRow)
}

// The residual of #736's own class, pinned rather than left to be rediscovered. The box clause
// says "last box on the pane", not "dialog" — so gemini's own tool-RESULT box fires if it
// carries both rows, and in THIS repo it can: both literals are verbatim in registry.go and in
// this file. Narrower than the flat matcher, which fired on a bare transcript line with no box
// at all, and not closed.
func TestGeminiConfirmationStillFiresOnAQuotedDialogEndingThePane(t *testing.T) {
	const quoted = `✦ Here is what registry.go says the matcher keys on:

╭────────────────────────────────────────────╮
│ ✓  ReadFile registry.go                    │
│                                            │
│ ● 1. Allow once                            │
│   3. No, suggest changes (esc)             │
╰────────────────────────────────────────────╯
`
	require.True(t, geminiConfirmationVisible(quoted),
		"disclosed, not fixed: a bottom-most box quoting both rows is indistinguishable from "+
			"the dialog without a per-branch header literal, and four branches are undriven")
}

// What the veto's removal did NOT cost, which is why this guard outlived the clause it was
// written for. It used to be the veto's premise — geminiConfirmationVisible rejected any block
// line reading as a composer, meaningful only while no rung contained one. That clause is gone
// (registry.go's THERE IS NO COMPOSER VETO HERE), so nothing here can take the matcher down
// any more.
//
// It still measures something the matcher depends on, in the other direction: a driven rung
// whose box holds a "> " row is a rung where InputBoxVisible answers TRUE on a live dialog,
// which is Session.AwaitingInput's third term. No rung does today. When one does, this reddens
// and TestGeminiConfirmationFiresOnADialogRowThatLooksLikeAComposer is the pane to read next.
func TestGeminiConfirmCapturesRenderNoComposerGlyphInsideTheDialog(t *testing.T) {
	for _, c := range geminiConfirmLadder {
		t.Run(c.label(), func(t *testing.T) {
			block, ok := bottomBoxBlock(c.pane)
			require.True(t, ok, "every rung must anchor, or the matcher is measuring nothing")
			for _, line := range block {
				require.False(t, isInputBoxLine(line, defaultPrompts),
					"a driven rung whose box reads as a composer makes InputBoxVisible true on a "+
						"live dialog; read the note above before re-pinning this: %q", line)
			}
		})
	}
}

// The structural claim the whole anchor rests on, asserted per rung rather than described: the
// dialog's bottom border is the last non-empty line. gemini's Composer returns null while a
// tool confirmation is pending (collapseDrawerDuringApproval, default true) and the Footer
// renders inside it, so nothing is drawn below the box.
func TestGeminiConfirmCapturesEndAtTheDialogBorder(t *testing.T) {
	for _, c := range geminiConfirmLadder {
		t.Run(c.label(), func(t *testing.T) {
			lines := strings.Split(strings.TrimRight(c.pane, "\n"), "\n")
			last := lines[len(lines)-1]
			require.True(t, isBoxBottomBorder(last),
				"expected the dialog's bottom border to end the pane, got %q", last)
		})
	}
}

// gemini's busy marker is a TRUNCATING row, so its coverage has a floor, and the floor is
// inside the range Atrium's preview pane produces. Below it a streaming session reads
// not-busy: no gate, no prompt, no marker → PaneIdle → Ready → the completion ding, on a
// session that is actively working. That is #713's failure class on a second surface.
//
// The boundary itself is bracketed, not pinned: 33 hits and 24 misses, and nothing between
// them was driven. Saying which is which is the point — an interpolated cliff would be a claim
// no capture supports.
func TestGeminiBusyMarkerMissesAtNarrowWidths(t *testing.T) {
	const marker = "esc to cancel" // gemini's only BusyMarkers entry

	for _, c := range geminiBusyLadder {
		t.Run("hit/"+c.label(), func(t *testing.T) {
			require.True(t, gemini.HasBusyMarker(c.pane),
				"the marker must be reachable at every covered width")
		})
	}

	// The cause, asserted rather than implied. Without the Contains half this would say only
	// "it misses", which is also true of the two rungs a wider window DID fix — and those two
	// are now in the ladder above, so the distinction is only legible if it is written down.
	for _, c := range geminiBusyTruncatedRungs {
		t.Run("truncated/"+c.label(), func(t *testing.T) {
			require.Contains(t, c.pane, "Thinking...",
				"the premise: the session IS streaming in this capture")
			require.NotContains(t, c.pane, marker,
				"the marker is cut mid-phrase, so no window reaches it")
			require.False(t, gemini.HasBusyMarker(c.pane))
		})
	}
}

// The window budget, as numbers rather than a sentence, and the measurement MarkerWindow is
// fitted to. gemini's loading row sits directly above the composer, so what decides whether
// the window reaches it is how many non-empty rows the composer and footer occupy.
//
// It is the COMPOSER that grows, not the footer. An earlier draft of this comment said "the
// footer wraps", in five places across three files; the driven bytes say the footer is two
// rows at 120, 45, 40, 34, 33, 24 and 20 alike and drops columns with an ellipsis, while the
// composer's " >   Type your message or @path/to/file" placeholder splits in two at 34 and
// below. Two rows becoming three is why 8 was one short there.
//
// All seven driven rungs are held, not the five the window covers, because "9 is the maximum"
// was the other thing that draft got wrong: 24 and 20 are DEEPER, at 10 and 11, and are out of
// reach for a different reason — gemini truncates the phrase itself off the row, so no window
// value reaches them. 9 is the deepest rung whose marker text survives. A rung deeper than
// that which still renders the phrase reddens this.
func TestGeminiBusyMarkerSitsAtTheEdgeOfItsWindow(t *testing.T) {
	depth := func(pane string) int {
		var nonEmpty []string
		for _, l := range strings.Split(strings.TrimRight(pane, "\n"), "\n") {
			if strings.TrimSpace(l) != "" {
				nonEmpty = append(nonEmpty, l)
			}
		}
		for i := len(nonEmpty) - 1; i >= 0; i-- {
			if strings.Contains(nonEmpty[i], "esc to cancel") {
				return len(nonEmpty) - i
			}
		}
		return -1
	}
	// The loading ROW's depth, located by the spinner's surviving prefix rather than by the
	// marker phrase: at 24 and 20 the phrase is cut mid-word and only "(esc" is left, so the
	// closure above returns -1 for both and cannot say how deep the row actually sits.
	rowDepth := func(pane string) int {
		var nonEmpty []string
		for _, l := range strings.Split(strings.TrimRight(pane, "\n"), "\n") {
			if strings.TrimSpace(l) != "" {
				nonEmpty = append(nonEmpty, l)
			}
		}
		for i := len(nonEmpty) - 1; i >= 0; i-- {
			if strings.Contains(nonEmpty[i], "(esc") {
				return len(nonEmpty) - i
			}
		}
		return -1
	}
	depth24, depth20 := rowDepth(geminiBusyPane24), rowDepth(geminiBusyPane20)
	require.Equal(t, -1, depth(geminiBusyPane24), "the premise: the phrase itself is gone at 24")
	require.Equal(t, -1, depth(geminiBusyPane20), "and at 20")

	require.Equal(t, 8, depth(geminiBusyPane120), "composer placeholder on one row")
	require.Equal(t, 8, depth(geminiBusyPane45x19), "same, at the preview pane's real width")
	require.Equal(t, 8, depth(geminiBusyPane40), "same — 40 is the last width it fits on one row")
	require.Equal(t, 9, depth(geminiBusyPane34), "one row deeper — the placeholder split in two")
	require.Equal(t, 9, depth(geminiBusyPane33), "same split, same depth")

	// Deeper than the window, and NOT what the window is short for. Both render the loading row
	// on screen; both have the phrase cut out of it, which is why they are in
	// geminiBusyTruncatedRungs rather than here.
	require.Equal(t, 10, depth24, "24 renders the row deeper still, at 10")
	require.Equal(t, 11, depth20, "and 20 at 11")
	require.Greater(t, depth24, gemini.MarkerWindow,
		"so widening the window to reach 34/33 could never have reached these two")

	require.Equal(t, 9, gemini.MarkerWindow,
		"the constant IS the deepest rung above: it buys zero margin, and moving either "+
			"without the other silently changes which widths detect as busy")
}

// The conjunction, one literal at a time. No driven rung can test this — a real dialog always
// renders both rows — so these are composed boxes, and they are the only thing standing
// between the matcher and an alternation. #715 round 1 shipped exactly that mistake on the
// sibling gate, where Contains IS an alternation and "Don't trust" turned out to be ordinary
// English; going through Match is what makes AND expressible here, and this is what proves it
// was actually written.
func TestGeminiConfirmationNeedsBothLiterals(t *testing.T) {
	const inner = 34
	box := func(rows ...string) string {
		rule := strings.Repeat("─", inner)
		out := "╭" + rule + "╮\n"
		for _, r := range rows {
			pad := inner - 1 - len([]rune(r))
			require.GreaterOrEqual(t, pad, 0, "row %q does not fit the composed box", r)
			out += "│ " + r + strings.Repeat(" ", pad) + "│\n"
		}
		return out + "╰" + rule + "╯"
	}

	both := box("Allow execution of [Shell]?", "", "● 1. Allow once", "  3. No, suggest changes (esc)")
	require.True(t, geminiConfirmationVisible(both), "the premise: both rows in a live box match")

	allowOnly := box("Allow execution of [Shell]?", "", "● 1. Allow once", "  2. Allow for this session")
	require.False(t, geminiConfirmationVisible(allowOnly),
		"\"Allow once\" alone is ordinary English and must not carry the match")

	cancelOnly := box("Cancel this?", "", "  3. No, suggest changes (esc)")
	require.False(t, geminiConfirmationVisible(cancelOnly),
		"the decline prefix alone must not carry the match either")
}

// The cost of having NO composer veto, asserted rather than described. The loop used to reject
// any block line that read as an input box; at 0.27 the composer was a walled box, so a
// composer holding both labels was the shape that clause existed for. Without it that pane
// matches.
//
// This is the trade being made, in the direction the package prefers: an over-fire is
// NoAutoTap -> PanePromptManual -> NeedsInput with the queued prompt withheld (#342), while
// the miss the clause bought in exchange was Session.AwaitingInput going true on a live dialog
// (the test below). Pinned so that restoring the veto has to argue with a measurement.
func TestGeminiConfirmationOverFiresOnA027BoxedComposer(t *testing.T) {
	boxedComposer := strings.Join([]string{
		"✦ The matcher keys on Allow once and No, suggest changes (esc).",
		"",
		"╭────────────────────────────────────────────────────╮",
		"│ > Allow once and No, suggest changes (esc)          │",
		"╰────────────────────────────────────────────────────╯",
	}, "\n")

	block, ok := bottomBoxBlock(boxedComposer)
	require.True(t, ok, "the premise: this IS a live box")
	joined := strings.Join(block, "\n")
	require.Contains(t, joined, geminiAllowRow, "the premise: the allow label is inside that box")
	require.Contains(t, joined, geminiCancelRow, "the premise: and so is the cancel label")

	require.True(t, geminiConfirmationVisible(boxedComposer),
		"a 0.27-shaped boxed composer quoting both labels now matches — the over-fire the "+
			"veto's removal buys, and the safe direction to fail in")
}

// Why the veto had to go, measured on the pane it used to reject. isInputBoxLine with
// defaultPrompts is the same predicate InputBoxVisible anchors on and gemini declares no
// InputBoxPrompts of its own, so the single-walled "> " row that vetoed the match also
// answered InputBoxVisible TRUE. What Atrium then types reaches a pane holding an unanswered
// approval; what gemini DOES with those keystrokes is registry.go's to not state, and it
// does not state it.
//
// ORDER IS THE ASSERTION HERE, and an earlier draft got it backwards. The verdict is the
// AwaitingInput conjunction, so it runs FIRST: with require.True(prompted) above it, !prompted
// is false by construction, Go short-circuits before InputBoxVisible is called, and the
// conjunction can never be true — a guard that cannot fail. It is not two independent halves
// either, which is what that draft claimed: given !gated and InputBoxVisible, "AwaitingInput
// is false" and "prompted is true" are the same fact. The decomposition below it exists to say
// WHICH term moved when the verdict fails, not to add coverage.
func TestGeminiConfirmationFiresOnADialogRowThatLooksLikeAComposer(t *testing.T) {
	withQuote := strings.Join([]string{
		"╭────────────────────────────────────────╮",
		"│ ? Shell  cat notes.md                  │",
		"│ > a quoted line from the file          │",
		"│ Allow execution of [Shell]?            │",
		"│ ● 1. Allow once                        │",
		"│   3. No, suggest changes (esc)         │",
		"╰────────────────────────────────────────╯",
	}, "\n")

	// The premise, and the reason this pane is the interesting one: that middle row IS an
	// input-box line to the predicate InputBoxVisible uses.
	require.True(t, isInputBoxLine("│ > a quoted line from the file          │", defaultPrompts),
		"the premise: one dialog row survives the single-\"│\" trim and reads as a composer")
	require.True(t, gemini.InputBoxVisible(withQuote),
		"the premise: so the adapter sees an input box on a pane holding a LIVE dialog")

	// Session.AwaitingInput, verbatim, and evaluated before anything can FailNow. False here
	// is the auto-approval being closed: with the drawer collapsed — the default — Enter would
	// land on the RadioButtonSelect whose highlighted row is "Allow once" by default — or,
	// under security.autoAddToPolicyByDefault, the row that persists the approval to
	// ~/.gemini/policies/auto-saved.toml. Either way a tap approves something.
	_, gated := gemini.GateUp(withQuote)
	_, prompted := gemini.DetectPrompt(withQuote)
	require.False(t, !gated && !prompted && gemini.InputBoxVisible(withQuote),
		"AwaitingInput must be false on a live approval dialog, or Atrium types the queued "+
			"first prompt into it and submits")

	// Which term carried it, so a future failure names itself instead of reporting a false
	// conjunction. Neither adds coverage over the assertion above.
	require.False(t, gated, "the dialog is not a Gate; it must be a prompt that closes this")
	require.True(t, prompted, "and it is the confirmation matcher that has to do it")
}

// The configuration this matcher cannot see, pinned as a MISS rather than left to be
// rediscovered (#746). gemini's ui.collapseDrawerDuringApproval defaults true and every rung
// above was driven that way; set it false and the composer renders below a LIVE dialog.
//
// Two things are asserted, and the second is why this test exists. The miss alone would be a
// documented limit. The conjunction below it is Session.AwaitingInput's exact expression, and
// it going TRUE on a pane holding an unanswered approval is Atrium typing the queued first
// prompt into a RadioButtonSelect whose highlighted row approves — "Allow once" by default,
// and a persistent policy write under security.autoAddToPolicyByDefault.
//
// The composer tail is COMPOSED, not lifted. Only the " >   Type your message or
// @path/to/file" row is verbatim from a driven capture; the block-glyph runs are cut to 40
// columns and the two footer rows of the real 120-column tail are re-spaced into one. Saying
// it was "taken from geminiConfirmDismissedPane" was the sentence that licenses a
// hand-composed fixture as evidence, so it is worth being exact: the three verdicts below were
// re-measured against that capture's real eight-row tail spliced in unaltered and came out
// identical, which is what the shape claim actually rests on.
func TestGeminiConfirmationMissesWhenTheDrawerStaysOpen(t *testing.T) {
	composer := strings.Join([]string{
		"",
		"▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄",
		" >   Type your message or @path/to/file",
		"▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀▀",
		" workspace (/directory)          branch",
	}, "\n")
	pane := strings.TrimRight(geminiConfirmPane40, "\n") + "\n" + composer + "\n"

	require.Contains(t, pane, "Allow once", "the premise: the dialog is still on screen")
	// Not "the literal is somewhere in the pane" — the matcher this replaced read the
	// flattened bottom WindowPrompt lines, so the regression claim is only true if the row
	// lands inside that window. It does, which is what makes this a regression rather than a
	// gap both matchers shared.
	require.Contains(t, flattenChrome(pane, WindowPrompt), "No, suggest changes (esc)",
		"the premise: the flat matcher #736 replaced DID fire on this pane")

	_, prompted := gemini.DetectPrompt(pane)
	require.False(t, prompted, "#746: the drawer is open, so the dialog's border no longer ends the pane")

	// Session.AwaitingInput, verbatim. True here means the queued prompt is typed AT the dialog.
	_, gated := gemini.GateUp(pane)
	require.True(t, !gated && !prompted && gemini.InputBoxVisible(pane),
		"#746 is a live regression, not a theoretical one: if this goes false, the bug is fixed and the disclosure in geminiConfirmationVisible must go with it")
	require.False(t, gemini.HasBusyMarker(pane),
		"and nothing else catches it — showLoadingIndicator requires !hasPendingActionRequired")
}

// The first DRIVEN gemini idle pane this repo has held, and a negative control for all three
// gemini surfaces at once: an authenticated session sitting at its composer with nothing
// pending must be neither gated, nor prompting, nor busy.
//
// It also records a shape change the composed geminiIdlePane cannot. At 0.27 the composer was
// a rounded box around the "> " row; at 0.55.1 it is bounded by "▄▄▄"/"▀▀▀" block rules with
// NO side walls. That is why isBoxWallLine can never reach a 0.55.1 composer, and therefore
// why bottomBoxBlock cannot mistake one for a dialog — asserted here rather than assumed,
// because the whole anchor rests on it.
func TestGeminiDrivenIdlePaneIsQuietOnEverySurface(t *testing.T) {
	pane := geminiIdlePane055

	require.True(t, gemini.InputBoxVisible(pane), "the premise: this is a live composer")
	require.Contains(t, pane, "Authenticated with gemini-api-key",
		"the premise: the session is authenticated, so this is the real idle state")

	_, prompted := gemini.DetectPrompt(pane)
	require.False(t, prompted, "no dialog is up")
	_, gated := gemini.GateUp(pane)
	require.False(t, gated, "no startup gate is up")
	require.False(t, gemini.HasBusyMarker(pane), "nothing is streaming")

	// The shape claim, measured: no line of the 0.55.1 composer carries both side walls, so
	// the composer cannot form a bottomBoxBlock at all.
	_, boxed := bottomBoxBlock(pane)
	require.False(t, boxed, "the 0.55.1 composer draws no walled box for the anchor to find")
	for _, line := range strings.Split(pane, "\n") {
		require.False(t, isBoxWallLine(line),
			"no composer row may read as box interior: %q", line)
	}
}

// The mechanism behind MarkerWindow, COMPUTED rather than described — because the sentence in
// registry.go that describes it has now been wrong twice. The first draft credited the extra
// row to a footer wrap; its replacement credited it entirely to the composer placeholder,
// which is false at 20, where the placeholder takes three rows.
//
// Two things below the loading row grow, at different widths: the " Shift+Tab to accept edits"
// hint and the " >   Type your message or @path/to/file" placeholder. The workspace/branch
// footer does not — it drops columns with an ellipsis. So the marker's depth is the base plus
// one row for each extra row those two take, and that is an arithmetic identity a new rung
// cannot be added in violation of.
//
// Both lists are walked. Splitting them is what let one half be fixed while the other stayed
// uncovered, so a guard over the mechanism has to see the uncovered half too.
func TestGeminiBusyDepthIsTheSumOfItsTwoGrowthSites(t *testing.T) {
	nonEmpty := func(pane string) []string {
		var out []string
		for _, l := range strings.Split(strings.TrimRight(pane, "\n"), "\n") {
			if strings.TrimSpace(l) != "" {
				out = append(out, l)
			}
		}
		return out
	}
	// rows counts the block starting at the line containing want and running to the next
	// block-glyph rule, which is how gemini bounds the composer at 0.55.1.
	rows := func(lines []string, want string) int {
		for i, l := range lines {
			if strings.Contains(l, want) {
				n := 1
				for i+n < len(lines) && !strings.HasPrefix(lines[i+n], "▄") && !strings.HasPrefix(lines[i+n], "▀") {
					n++
				}
				return n
			}
		}
		return 0
	}
	depth := func(lines []string) int {
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.Contains(lines[i], "(esc") {
				return len(lines) - i
			}
		}
		return 0
	}

	const baseDepth = 8
	for _, c := range append(append([]paneCapture{}, geminiBusyLadder...), geminiBusyTruncatedRungs...) {
		t.Run(c.label(), func(t *testing.T) {
			lines := nonEmpty(c.pane)
			hint := rows(lines, "Shift+Tab")
			placeholder := rows(lines, "Type your")
			require.NotZero(t, hint, "the premise: every driven rung renders the accept-edits hint")
			require.NotZero(t, placeholder, "the premise: and the composer placeholder")

			require.Equal(t, baseDepth+(hint-1)+(placeholder-1), depth(lines),
				"marker depth is the base plus one row per EXTRA row the two growth sites take; "+
					"hint=%d placeholder=%d at width %d", hint, placeholder, c.width)
		})
	}
}
