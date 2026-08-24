package app

import (
	"context"
	"os"

	"github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/customcmd"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/notify"
	"github.com/ZviBaratz/atrium/repocfg"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/spinner"
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
		hostCap:      config.DefaultSessionCap(),
		program:      program,
		autoYes:      autoYes,
		version:      version,
		binName:      binName,
		state:        stateDefault,
		appState:     appState,
		listRatio:    appState.GetListRatio(),
	}
	// Restore the persisted layout preset (see app_presets.go). When the split is
	// not a custom override, the preset owns the ratio, so seed listRatio from it —
	// this keeps a restored monitor/review at its preset width even if the stored
	// ratio drifted, while a custom override or focus (ratio 0) keeps the stored
	// value. layoutPrev seeds to the default so an Esc out of a restored focus lands
	// somewhere sane rather than back in focus.
	h.layoutIndex = presetIndexByName(appState.GetLayoutPreset())
	h.layoutPrev = defaultPresetIndex
	h.layoutCustom = appState.GetLayoutCustom()
	if !h.layoutCustom {
		if p := layoutPresets[h.layoutIndex]; p.ratio > 0 {
			h.listRatio = p.ratio
		}
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
	// Seed the session chip (off/count/percent/bar/cost; see
	// config.GetContextIndicator).
	h.list.SetContextIndicator(appConfig.GetContextIndicator())
	// Seed the context chip's colour bands (#799). The accessors clamp and hold
	// warn ≤ danger, so the renderer receives a coherent pair.
	h.list.SetContextThresholds(appConfig.GetContextWarnPercent(), appConfig.GetContextDangerPercent())
	// Install the user's Pending watchdog cap on the session package, which owns the
	// reconciliation. Package-level because the cap is fleet-wide, not per-instance;
	// the daemon does the same at its own startup, since it polls without a TUI.
	//
	// PendingWatchdogOverride, not the accessor: an unset field must install 0, or the
	// adapter rung of session's ladder never runs (see that method's comment).
	session.SetPendingWatchdog(appConfig.PendingWatchdogOverride())
	// Seed the hint bar's chrome-free flag: with hint_bar off the menu row stays
	// reserved but renders blank, so notices ride it without a shift (#438).
	h.menu.SetQuiet(!appConfig.GetHintBar())
	// Seed the splash: whether it animates at all, and the pattern (a pinned
	// name, or a fresh random pick; see config.GetSplash and config.SplashOff).
	applySplashConfig(appConfig)

	// Re-derive each restored session's account labels from live config before the
	// first cluster build, so a rename made since the last run adopts its sessions
	// instead of stranding them under a name config no longer has (#470). Pure
	// in-memory: flushAccountStamps (called from newHome) does the IO half.
	h.accountSync = syncAccountStamps(appConfig, instances)

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
	// A cluster whose key was just renamed carries its slot over, rather than falling to
	// the bottom of the list as a key the stored order does not name (#470).
	restoredOrder, _ := remapAccountOrder(appState.GetAccountOrder(), h.accountSync.clusterMoves)
	h.list.SetAccountOrder(restoredOrder)
	// Apply the persisted top-level grouping after the full list is in place, so its
	// canonical-order snapshot is the real creation order.
	h.list.SetGroupMode(appConfig.GetGroupMode())

	// Validate the user's own verbs once, here, rather than on every open of the menu
	// (#375). config.LoadConfig cannot fail by design, so a malformed entry is
	// reported rather than returned: the good entries bind and the refused ones are
	// buffered for the startup modal, which the preview tick flushes once no overlay
	// owns the screen. `atrium doctor` runs the same pass, so the two cannot disagree.
	h.customCommands, h.pendingCustomCommandProblems = customcmd.Validate(appConfig.CustomCommands)

	// The same pass for repo_scripts (#389). Only the problems are kept: a session
	// re-reads the config and revalidates its own entry when it runs, deliberately, so
	// that an edit reaches a session resumed later in the same run. What cannot wait is
	// the report — a refused entry is dropped, and its whole symptom otherwise is a
	// setup script that never runs and says nothing about why.
	_, h.pendingRepoScriptProblems = repocfg.Validate(appConfig.RepoScripts)

	// And the keymap (#376). Here rather than in main.go for three reasons: the
	// daemon loads the same config and must not install a keymap it never renders;
	// the problems have to reach the buffer above to be shown; and this runs before
	// tea.NewProgram, which is the contract keys.Apply's readers depend on.
	//
	// The restore func is dropped deliberately. The keymap is process-wide and
	// lasts as long as the program does — there is nothing to restore it for
	// outside a test, and holding it would only invite someone to call it.
	h.pendingKeybindingProblems, _ = keys.Apply(appConfig.KeybindingOverrides())
	installAttachChords()

	return h
}
