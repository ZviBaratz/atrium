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
	{name: "geminiBusyPane33", width: 33, note: "footer wraps; marker 9 non-empty lines up", pane: geminiBusyPane33},
	{name: "geminiBusyPane34", width: 34, note: "footer wraps; marker 9 non-empty lines up", pane: geminiBusyPane34},
	{name: "geminiBusyPane40", width: 40, note: "footer on one row; marker 8 up", pane: geminiBusyPane40},
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

// The other control, and the one that separates an OPEN dialog from an answered one: the same
// session one keystroke later. Escape dismissed the dialog, the composer came back, and
// nothing here may still read as a prompt — a dialog left matching forever is what keeps a
// queued first prompt undelivered.
func TestGeminiConfirmationDropsOnceDismissed(t *testing.T) {
	_, ok := gemini.DetectPrompt(geminiConfirmDismissedPane)
	require.False(t, ok, "the dialog is gone; nothing may still classify as a prompt")
	_, gated := gemini.GateUp(geminiConfirmDismissedPane)
	require.False(t, gated)
}

// The measurement that overrides the issue. #736 proposes keeping "No, suggest changes (esc)"
// as the in-box content; driven, that literal is absent below width 34 — above every width
// Atrium's preview pane routinely produces. Keying on it would have re-shipped #713's mistake
// a third time, from a wide capture.
//
// Both halves are asserted. Without the second, the test would pass while the matcher keyed on
// something no narrow pane renders.
func TestGeminiConfirmationCancelRowTruncatesBelowWidth34(t *testing.T) {
	const shipped = "No, suggest changes (esc)"
	for _, c := range geminiConfirmLadder {
		t.Run(c.label(), func(t *testing.T) {
			if c.width >= 34 {
				require.Contains(t, c.pane, shipped, "at %d the shipped literal is still on screen", c.width)
			} else {
				require.NotContains(t, c.pane, shipped, "at %d it is truncated away", c.width)
			}
			// What the matcher actually keys on must be present at EVERY rung, or the
			// assertion above is measuring a literal nothing reads.
			require.Contains(t, c.pane, "Allow once", "the first option row survives every driven width")
			require.Contains(t, c.pane, "No, sug", "the cancel prefix survives every driven width")
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

	cancel := labelCol(geminiConfirmPane20, "No, sug")
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
	require.Equal(t, width-9-1, len([]rune("Allow once")),
		"and the binding literal fills all but ONE cell of it at the narrowest driven rung — "+
			"which is what makes 19 the floor, and 20 the last rung that proves anything")
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

// The composer veto's PREMISE. geminiConfirmationVisible returns false on any block line that
// reads as a composer, which is only meaningful while no confirmation rung contains one.
func TestGeminiConfirmCapturesRenderNoComposerGlyphInsideTheDialog(t *testing.T) {
	for _, c := range geminiConfirmLadder {
		t.Run(c.label(), func(t *testing.T) {
			block, ok := bottomBoxBlock(c.pane)
			require.True(t, ok, "every rung must anchor, or the matcher is measuring nothing")
			for _, line := range block {
				require.False(t, isInputBoxLine(line, defaultPrompts),
					"a composer glyph inside the dialog would take the matcher down: %q", line)
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

// The window budget, as a number rather than a sentence, and the measurement MarkerWindow is
// fitted to. gemini's loading row sits directly above the composer, so what decides whether
// the window reaches it is how many non-empty rows the composer and footer occupy — and that
// grows by one when the footer wraps. 8 is exactly enough at 40 and one short at 34, which is
// why the constant is 9 and why 9 buys no margin at all: it is the deepest rung driven, not a
// bound anything derives. A rung whose footer wrapped twice would need 10 and is not covered.
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
	require.Equal(t, 8, depth(geminiBusyPane40), "footer on one row")
	require.Equal(t, 9, depth(geminiBusyPane34), "one row deeper — the footer wrapped")
	require.Equal(t, 9, depth(geminiBusyPane33), "same wrap, same depth")
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

// The composer veto, which no driven pane can exercise at 0.55.1: the composer is bounded by
// "▄▄▄"/"▀▀▀" block rules with no side walls, so it can never appear inside a bottomBoxBlock.
// The clause is kept anyway because gemini HAS shipped a walled composer — 0.27 drew a rounded
// box around the "> " row, which is what geminiIdlePane still records — and this is that
// shape, carrying both literals so that nothing but the veto can reject it.
//
// Without this test the veto is unexercised, and an unexercised defensive clause is how #717
// shipped a predicate that was false on every pane it was written for.
func TestGeminiConfirmationVetoesABoxedComposer(t *testing.T) {
	boxedComposer := strings.Join([]string{
		"✦ The matcher keys on Allow once and No, suggest changes (esc).",
		"",
		"╭────────────────────────────────────────────────────╮",
		"│ > Allow once and No, suggest changes (esc)          │",
		"╰────────────────────────────────────────────────────╯",
	}, "\n")

	block, ok := bottomBoxBlock(boxedComposer)
	require.True(t, ok, "the premise: this IS a live box, so only the veto can reject it")
	require.Contains(t, strings.Join(block, "\n"), "Allow once",
		"the premise: both literals are inside that box")

	require.False(t, geminiConfirmationVisible(boxedComposer),
		"a composer glyph inside the block means the user is typing, not approving")
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
