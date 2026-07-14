package config

// This file holds the normalizing Config getters: each tolerates a nil receiver
// and a nil/absent field (an older config.json predating the key) and returns the
// documented default, so callers never branch on config presence.

import "slices"

// ModelIndicator modes (see Config.ModelIndicator).
const (
	ModelIndicatorOn  = "on"
	ModelIndicatorOff = "off"
)

// GetModelIndicator returns the normalized model-chip mode: "off" only when
// set explicitly, "on" for everything else (including a nil Config and the
// retired "pinned"/"always" values from the chip's pinned/observed era).
func (c *Config) GetModelIndicator() string {
	if c != nil && c.ModelIndicator == ModelIndicatorOff {
		return ModelIndicatorOff
	}
	return ModelIndicatorOn
}

// PermissionIndicator modes (see Config.PermissionIndicator).
const (
	PermissionIndicatorOn  = "on"
	PermissionIndicatorOff = "off"
)

// GetPermissionIndicator returns the normalized permission-chip mode: "off"
// only when set explicitly, "on" for everything else.
func (c *Config) GetPermissionIndicator() string {
	if c != nil && c.PermissionIndicator == PermissionIndicatorOff {
		return PermissionIndicatorOff
	}
	return PermissionIndicatorOn
}

// EffortIndicator modes (see Config.EffortIndicator).
const (
	EffortIndicatorOn  = "on"
	EffortIndicatorOff = "off"
)

// GetEffortIndicator returns the normalized effort-chip mode: "off" only when
// set explicitly, "on" for everything else.
func (c *Config) GetEffortIndicator() string {
	if c != nil && c.EffortIndicator == EffortIndicatorOff {
		return EffortIndicatorOff
	}
	return EffortIndicatorOn
}

// SplashRandom is the Splash mode that picks a fresh pattern each launch —
// the default (see Config.Splash).
const SplashRandom = "random"

// SplashVariants lists the pinnable splash pattern names in settings-panel
// display order. The mapping onto generators lives in ui (splash_field.go),
// which takes the name as a plain string so it needs no config import.
func SplashVariants() []string {
	return []string{"nebula", "braille", "contours", "julia", "mandala", "plasma"}
}

// GetSplash returns the normalized splash mode: a known variant name when set
// to one, else SplashRandom (including a nil Config, the empty default, and
// unknown values from a hand-edited config).
func (c *Config) GetSplash() string {
	if c != nil && slices.Contains(SplashVariants(), c.Splash) {
		return c.Splash
	}
	return SplashRandom
}

// defaultCarryFiles is the carry list applied when a config predates the
// carry_files key (nil field). Claude Code's gitignored local project config
// is the one file every fresh worktree loses by default.
var defaultCarryFiles = []string{".claude/settings.local.json"}

// GetCarryFiles returns the repo-relative gitignored files to copy into each
// new session worktree. A nil CarryFiles (e.g. an older config file with no
// such key) defaults to defaultCarryFiles; an explicitly empty list opts out.
// The default is returned as a copy so callers can never mutate the shared
// seed.
func (c *Config) GetCarryFiles() []string {
	if c.CarryFiles == nil {
		return append([]string(nil), defaultCarryFiles...)
	}
	return c.CarryFiles
}

// GetMaxSessions returns the configured session cap, or 0 (no cap) for a nil
// or non-positive value. Callers must treat 0 as unlimited.
func (c *Config) GetMaxSessions() int {
	if c.MaxSessions == nil || *c.MaxSessions < 1 {
		return 0
	}
	return *c.MaxSessions
}

// Project-scan depth bounds (see Config.ProjectSearchDepth).
const (
	defaultProjectSearchDepth = 3
	maxProjectSearchDepth     = 8
)

// defaultProjectSearchRoots is the scan scope applied when a config predates
// the project_search_roots key (nil or empty field).
var defaultProjectSearchRoots = []string{"~"}

// GetProjectSearchRoots returns the directories the repo scan walks. A nil or
// empty ProjectSearchRoots — or a nil Config — defaults to the home directory.
// The result is always a fresh copy so callers can never mutate the shared
// default seed nor the Config's stored slice.
func (c *Config) GetProjectSearchRoots() []string {
	if c == nil || len(c.ProjectSearchRoots) == 0 {
		return append([]string(nil), defaultProjectSearchRoots...)
	}
	return append([]string(nil), c.ProjectSearchRoots...)
}

// GetProjectSearchDepth returns the scan's depth bound: nil (an older config
// with no such key) defaults to defaultProjectSearchDepth, zero or negative
// disables the scan (returns 0), and values beyond maxProjectSearchDepth clamp.
func (c *Config) GetProjectSearchDepth() int {
	if c == nil || c.ProjectSearchDepth == nil {
		return defaultProjectSearchDepth
	}
	d := *c.ProjectSearchDepth
	if d <= 0 {
		return 0
	}
	if d > maxProjectSearchDepth {
		return maxProjectSearchDepth
	}
	return d
}

// boolOr returns the value *p points to, or def when p is nil — the "optional
// bool absent from an older config file" fallback every Get* bool accessor below
// shares. Each accessor pairs it with a nil-receiver guard so a nil *Config also
// resolves to the documented default instead of panicking; several accessors
// previously omitted that guard and would panic on a nil receiver.
func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// GetSessionContextBar reports whether attached sessions should render the
// in-session context status line. A nil SessionContextBar (e.g. an older config
// file with no such key) — or a nil Config — defaults to on, mirroring GetAutoAttach.
func (c *Config) GetSessionContextBar() bool {
	if c == nil {
		return true
	}
	return boolOr(c.SessionContextBar, true)
}

// GetNerdFont reports whether vendor Nerd-Font icons should be used. A nil
// NerdFont (an older config, or a fresh install) — or a nil Config — defaults to
// off, so the UI never renders tofu boxes without an explicit opt-in.
func (c *Config) GetNerdFont() bool {
	if c == nil {
		return false
	}
	return boolOr(c.NerdFont, false)
}

// GetHintBar reports whether the always-on bottom hint bar is enabled. A nil
// HintBar (e.g. an older config file with no such key) — or a nil Config —
// defaults to on.
func (c *Config) GetHintBar() bool {
	if c == nil {
		return true
	}
	return boolOr(c.HintBar, true)
}

// GetAutoAttach reports whether new sessions should auto-attach on creation.
// A nil AutoAttach (e.g. an older config file with no such key) — or a nil
// Config — defaults to on.
func (c *Config) GetAutoAttach() bool {
	if c == nil {
		return true
	}
	return boolOr(c.AutoAttach, true)
}

// GetShowReleaseNotesAfterUpdate reports whether the post-update "what's new"
// overlay should be shown. A nil field (an older config file with no such key)
// — or a nil Config — defaults to on.
func (c *Config) GetShowReleaseNotesAfterUpdate() bool {
	if c == nil {
		return true
	}
	return boolOr(c.ShowReleaseNotesAfterUpdate, true)
}

// GetPRCreateDraft reports whether PRs opened with the create key (c) start as
// drafts. A nil PRCreateDraft (e.g. an older config file with no such key) — or a
// nil Config — defaults to draft.
func (c *Config) GetPRCreateDraft() bool {
	if c == nil {
		return true
	}
	return boolOr(c.PRCreateDraft, true)
}

// GetUpdateBaseOnCreate reports whether new sessions branch off the freshest
// remote tip of their base. A nil field (an older config file with no such key)
// — or a nil Config — defaults to on.
func (c *Config) GetUpdateBaseOnCreate() bool {
	if c == nil {
		return true
	}
	return boolOr(c.UpdateBaseOnCreate, true)
}

// GetFastForwardLocalBase reports whether session creation also fast-forwards the
// local base branch to origin. Defaults to off (opt-in): a nil field or nil
// Config makes no local changes.
func (c *Config) GetFastForwardLocalBase() bool {
	if c == nil {
		return false
	}
	return boolOr(c.FastForwardLocalBase, false)
}

// GetAutoUpdateMode returns the normalized auto-update mode: AutoUpdateAuto,
// AutoUpdateOff, or AutoUpdateNotify for a nil Config, an empty value, or
// anything unrecognized — a typo must never silently disable update hints nor
// enable unattended binary swaps.
func (c *Config) GetAutoUpdateMode() string {
	if c == nil {
		return AutoUpdateNotify
	}
	switch c.AutoUpdate {
	case AutoUpdateAuto, AutoUpdateOff:
		return c.AutoUpdate
	default:
		return AutoUpdateNotify
	}
}

// GetSessionSort returns the normalized within-group session sort mode:
// SessionSortStatus, or SessionSortCreation for a nil Config, an empty value, or
// anything unrecognized — a typo must never silently rearrange the list.
func (c *Config) GetSessionSort() string {
	if c == nil {
		return SessionSortCreation
	}
	switch c.SessionSort {
	case SessionSortStatus:
		return c.SessionSort
	default:
		return SessionSortCreation
	}
}

// GetGroupMode returns the normalized top-level grouping mode: GroupModeAccount,
// or GroupModeRepo for a nil Config, an empty value, or anything unrecognized — a
// typo must never silently regroup the list.
func (c *Config) GetGroupMode() string {
	if c == nil {
		return GroupModeRepo
	}
	switch c.GroupMode {
	case GroupModeAccount:
		return c.GroupMode
	default:
		return GroupModeRepo
	}
}

// GetNotifications returns the normalized notification mode: NotificationsBell,
// NotificationsDesktop, or NotificationsOff for a nil Config, an empty value, or
// anything unrecognized — a typo must never silently start ringing bells or firing
// desktop popups.
func (c *Config) GetNotifications() string {
	if c == nil {
		return NotificationsOff
	}
	switch c.Notifications {
	case NotificationsBell, NotificationsDesktop:
		return c.Notifications
	default:
		return NotificationsOff
	}
}

// GetNotifyCommand returns the configured desktop-notification command, or "" for a
// nil Config or an unset value (the notifier then falls back to a per-OS default).
func (c *Config) GetNotifyCommand() string {
	if c == nil {
		return ""
	}
	return c.NotifyCommand
}

// GetBranchPrefix returns the configured git-branch prefix (e.g. "zvi/"), or ""
// for a nil Config. The list view strips this prefix from each session's branch
// when rendering, since it repeats identically on every row and carries no
// distinguishing information.
func (c *Config) GetBranchPrefix() string {
	if c == nil {
		return ""
	}
	return c.BranchPrefix
}

// GetKillDoubleTapConfirm reports whether a second press of the kill key confirms
// the kill dialog. A nil KillDoubleTapConfirm (e.g. an older config file with no
// such key) — or a nil Config — defaults to on.
func (c *Config) GetKillDoubleTapConfirm() bool {
	if c == nil {
		return true
	}
	return boolOr(c.KillDoubleTapConfirm, true)
}

// GetSmartDispatchAuto reports whether a confident deterministic smart-dispatch match
// may create a session without the confirmation form. Off by default (a nil field
// or a nil Config → false).
func (c *Config) GetSmartDispatchAuto() bool {
	if c == nil {
		return false
	}
	return boolOr(c.SmartDispatchAuto, false)
}

// GetTrustWorktreesRoot reports whether Atrium should pre-accept Claude Code's
// workspace trust for the worktrees root. Defaults OFF for a nil field or a nil
// Config: this writes to another tool's config file, so it is strictly opt-in.
func (c *Config) GetTrustWorktreesRoot() bool {
	if c == nil {
		return false
	}
	return boolOr(c.TrustWorktreesRoot, false)
}

// GetProgram returns the program to run. If Profiles is non-empty and
// DefaultProgram matches a profile name, that profile's Program is returned.
// Otherwise DefaultProgram is returned as-is.
func (c *Config) GetProgram() string {
	for _, p := range c.Profiles {
		if p.Name == c.DefaultProgram {
			return p.Program
		}
	}
	return c.DefaultProgram
}

// GetProfiles returns a unified list of profiles. If Profiles is defined,
// those are returned with the default profile first. Otherwise, a single
// profile is synthesized from DefaultProgram.
func (c *Config) GetProfiles() []Profile {
	if len(c.Profiles) == 0 {
		return []Profile{{Name: c.DefaultProgram, Program: c.DefaultProgram}}
	}
	// Reorder so the default profile comes first.
	profiles := make([]Profile, 0, len(c.Profiles))
	for _, p := range c.Profiles {
		if p.Name == c.DefaultProgram {
			profiles = append(profiles, p)
			break
		}
	}
	for _, p := range c.Profiles {
		if p.Name != c.DefaultProgram {
			profiles = append(profiles, p)
		}
	}
	return profiles
}
