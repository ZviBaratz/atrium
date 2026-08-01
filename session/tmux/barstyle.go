package tmux

import (
	"context"
	"fmt"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/ui/theme"
)

// barstyle.go keeps the in-session status bar's BAND in step with a live theme
// change.
//
// The band's colours (status-style) are baked into the managed tmux config, and
// tmux reads -f only when a SERVER starts — so before this existed, a theme
// change restyled the bar's text (Atrium pushes @atrium_left with #[fg=...]
// markup on every metadata tick, see context.go) while leaving the band on the
// launch-time colours. Half the bar moved and half did not, which a dark->light
// flip turns from a subtle mismatch into an unreadable one.
//
// status-style is a server-global option, so one set-option -g restyles every
// session at once. That is why this is a package-level function taking an
// Executor rather than a Session method: there is nothing per-session about it,
// and a per-session push would be N subprocesses on the update thread (#380).

// barStyleColours resolves the band's bg and fg for the theme active right now,
// as tmux style values. It lives here because the two routes that set
// status-style — this file's live push and renderManagedConfig's file — must
// resolve it identically or the fleet's bars disagree, which is the whole subject
// of this file.
//
// Under NO_COLOR both become "default", the terminal's own colours. Not the empty
// string: `status-style "bg=,fg="` is rejected by tmux 3.6 as `invalid style`, and
// it fails through source-file too — which is how validateConfig sees it, and a
// parse error there disables the ENTIRE managed config. Trading a coloured bar for
// no scrollback bindings, no clipboard fix and no terminal title is not a trade
// anyone asked for. Omitting the option instead is no better: the fallback is
// tmux's built-in default, which is a real colour on tmux 3.3 and earlier.
func barStyleColours() (bg, fg string) {
	if theme.Mono() {
		return "default", "default"
	}
	th := theme.Current()
	return theme.Hex(th.Palette.BarBg), theme.Hex(th.Palette.Fg)
}

// ApplyBarStyle pushes the active theme's status-bar band colours to the live
// tmux server. One set-option for the whole fleet, then a best-effort repaint.
//
// It resolves the colours at call time, through barStyleColours, rather than
// taking them as arguments: the value being fixed too early is precisely the bug
// this exists to fix, and a colour passed in as an argument is a wire no test
// guards. That resolution runs off the bubbletea loop, which is why both globals it
// reaches are atomic loads — theme.Current() and theme.Mono() (see
// ui/theme/current.go and ui/theme/mono.go).
//
// No-ops when the user supplied their own tmux config: status-style is then theirs
// to set, and stomping it server-globally would contradict tmux_config_override,
// which RewriteManagedConfig already honours by leaving the file alone.
//
// A failure is returned but is not fatal to the caller, which is expected to log it
// rather than surface it. The common one is "no server running" — nothing to
// restyle, and the managed config (rewritten alongside this by the caller) already
// carries the new colours for whenever a server does start.
//
// The repaint is deliberately a SEPARATE, ignored command. Batched behind a `;` it
// sinks the whole call: `refresh-client -S` exits 1 with "no current client" when
// nothing is attached, and nothing is attached at the only moment this runs — the
// user is in the settings overlay, which is unreachable from inside a session pane.
// The set-option half still lands, so batching them would have reported failure on
// every single theme change and masked a genuine set-option error behind it.
// eb76f43 hit the same edge in SetContext's identical batch.
func ApplyBarStyle(ctx context.Context, cmdExec cmd.Executor) error {
	if overrideConfigPath() != "" {
		return nil
	}
	bg, fg := barStyleColours()
	style := fmt.Sprintf("bg=%s,fg=%s", bg, fg)

	opCtx, cancel := context.WithTimeout(ctx, tmuxOpTimeout)
	defer cancel()

	if err := cmdExec.Run(tmuxCommand(opCtx, "set-option", "-g", "status-style", style)); err != nil {
		return fmt.Errorf("failed to apply tmux status-bar style: %w", err)
	}
	// Attached clients repaint now; with none attached this is a no-op that errors,
	// and the band is already correct for whoever attaches next.
	_ = cmdExec.Run(tmuxCommand(opCtx, "refresh-client", "-S"))
	return nil
}

// RewriteManagedConfig re-materializes the managed tmux config from the CURRENT
// theme, so a server that starts after a live theme change opens with the new
// band rather than the one baked in at launch.
//
// It is the deferred half of ApplyBarStyle: that one fixes servers already
// running, this one fixes servers not yet started. Both are needed, or the fleet
// diverges depending on when each session was created.
//
// It delegates to Init deliberately, passing the stored override back in so one the
// user set at launch is preserved: Init returns early for an override, rewrites the
// managed file wholesale, and re-runs validateConfig — which is what we want, since a
// theme whose hex somehow broke the file should fall back to tmux defaults rather
// than lock the user out of the pane.
func RewriteManagedConfig(contextBar bool) error {
	return Init(overrideConfigPath(), contextBar)
}
