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

// ApplyBarStyle pushes the active theme's status-bar band colours to the live
// tmux server and repaints, in one batched command.
//
// It reads theme.Current() at call time rather than taking colours as arguments:
// the value being fixed too early is precisely the bug this exists to fix, and a
// colour passed in as an argument is a wire no test guards.
//
// A failure is returned but is not fatal to the caller, which is expected to log
// it rather than surface it. The common one is "no server running" — not a problem
// to report, since there are no bars to restyle, and the managed config (rewritten
// alongside this by the caller) already carries the new colours for whenever a
// server does start.
func ApplyBarStyle(ctx context.Context, cmdExec cmd.Executor) error {
	th := theme.Current()
	style := fmt.Sprintf("bg=%s,fg=%s", theme.Hex(th.Palette.BarBg), theme.Hex(th.Palette.Fg))

	opCtx, cancel := context.WithTimeout(ctx, tmuxOpTimeout)
	defer cancel()

	c := tmuxCommand(opCtx,
		"set-option", "-g", "status-style", style, ";",
		"refresh-client", "-S",
	)
	if err := cmdExec.Run(c); err != nil {
		return fmt.Errorf("failed to apply tmux status-bar style: %w", err)
	}
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
// It delegates to Init deliberately, passing the stored configOverridePath back in
// so an override the user set at launch is preserved: Init returns early for an
// override, rewrites the managed file wholesale, and re-runs validateConfig — which
// is what we want, since a theme whose hex somehow broke the file should fall back
// to tmux defaults rather than lock the user out of the pane.
func RewriteManagedConfig(contextBar bool) error {
	return Init(configOverridePath, contextBar)
}
