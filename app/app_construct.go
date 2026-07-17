package app

import (
	"context"
	"os"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/notify"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/theme"

	"github.com/charmbracelet/bubbles/spinner"
)

// assembleHome builds the root model from already-loaded inputs. It performs no
// IO and never exits: config/state/storage loading (and any failure handling)
// lives in newHome, so this constructor is a pure function unit tests can drive
// directly. theme.Set must have run before this — the spinner reads
// theme.Current().Glyphs.
func assembleHome(
	ctx context.Context,
	program string,
	autoYes bool,
	version, binName string,
	appConfig *config.Config,
	appState config.AppState,
	storage *session.Storage,
	instances []*session.Instance,
) *home {
	h := &home{
		ctx: ctx,
		spinner: spinner.New(spinner.WithSpinner(spinner.Spinner{
			Frames: theme.Current().Glyphs.SpinnerFrames,
			FPS:    theme.Current().Glyphs.SpinnerFPS,
		})),
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane(ctx)),
		errBox:       ui.NewErrBox(),
		storage:      storage,
		lostStrikes:  make(map[*session.Instance]int),
		notifier:     notify.New(os.Stdout, cmd.MakeExecutor()),
		notifySeen:   make(map[*session.Instance]*notifyState),
		appConfig:    appConfig,
		program:      program,
		autoYes:      autoYes,
		version:      version,
		binName:      binName,
		state:        stateDefault,
		appState:     appState,
		listRatio:    appState.GetListRatio(),
	}
	// Seed the picker's scanned-repo candidates from the persisted cache so the
	// first form-open after launch is populated before the startup scan lands.
	// Gated on the feature being enabled: with project_search_depth ≤ 0, a
	// cache written before the user disabled the scan must not keep surfacing.
	if appConfig.GetProjectSearchDepth() > 0 {
		h.scannedRepos, h.lastScanAt = appState.GetScannedRepos()
	}
	h.list = ui.NewList(&h.spinner)
	// Hide the redundant branch namespace (e.g. "zvi/") from each row's branch
	// label — it repeats on every session and only crowds the diff off the line.
	h.list.SetBranchPrefix(appConfig.GetBranchPrefix())
	// Seed the model-chip mode (on/off; see config.GetModelIndicator).
	h.list.SetModelIndicator(appConfig.GetModelIndicator())
	// Seed the reasoning-effort chip (on/off; see config.GetEffortIndicator).
	h.list.SetEffortIndicator(appConfig.GetEffortIndicator())
	// Seed the permission-mode chip (on/off; see config.GetPermissionIndicator).
	h.list.SetPermissionIndicator(appConfig.GetPermissionIndicator())
	// Seed the splash pattern (a pinned name, or a fresh random pick; see
	// config.GetSplash). ui takes the normalized name, no config import needed.
	ui.SetSplashVariant(appConfig.GetSplash())

	// Add loaded instances to the list
	for _, instance := range instances {
		// Call the finalizer immediately.
		h.list.AddInstance(instance)()
		if autoYes {
			instance.AutoYes = true
		}
	}
	// Restore folded groups only after every instance is loaded — AddInstance auto-expands the
	// group it inserts into, so applying persisted folds earlier would be undone by the loop.
	h.list.SetCollapsedRepos(appState.GetCollapsedRepos())
	// Apply the persisted within-group sort mode once the full (creation-order) list
	// is in place, so its canonical-order snapshot is the real creation order.
	h.list.SetSortMode(appConfig.GetSessionSort())
	// Restore the chosen account-cluster order. Applying it before the grouping means
	// the first cluster build already honors it; SetAccountOrder rebuilds an active view
	// itself, so the reverse order would also land correctly, just via a second build.
	h.list.SetAccountOrder(appState.GetAccountOrder())
	// Apply the persisted top-level grouping after the full list is in place, so its
	// canonical-order snapshot is the real creation order.
	h.list.SetGroupMode(appConfig.GetGroupMode())

	return h
}
