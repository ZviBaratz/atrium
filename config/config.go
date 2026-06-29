// Package config persists Atrium's two data-dir artifacts — config.json
// (Config: program, profiles, auto-attach) and state.json (State: serialized
// instances plus UI state) — and resolves the runtime identity (data dir, tmux
// socket, session prefix) shared with legacy claude-squad installs. See
// RuntimeName and GetConfigDir for the prefer-new/fall-back-to-legacy rules.
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ZviBaratz/atrium/log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// ConfigFileName is the name of the config file inside the data dir.
	ConfigFileName = "config.json"
	defaultProgram = "claude"
	// shellProbeTimeout bounds the shell invocation GetClaudeCommand uses to
	// resolve the claude binary, so a hung profile script can't wedge startup.
	shellProbeTimeout = 10 * time.Second
)

// AutoUpdate modes (Config.AutoUpdate). See GetAutoUpdateMode for normalization.
const (
	// AutoUpdateNotify checks for a newer release at TUI startup and shows a
	// hint pointing at `atrium update`. The default.
	AutoUpdateNotify = "notify"
	// AutoUpdateAuto downloads, verifies, and stages the new binary in the
	// background; it takes effect on the next launch (the running TUI, daemon,
	// and sessions are never disturbed).
	AutoUpdateAuto = "auto"
	// AutoUpdateOff disables the startup check entirely.
	AutoUpdateOff = "off"
)

// SessionSort modes (Config.SessionSort). See GetSessionSort for normalization.
// The sort applies within each repo group only; group order stays manual ({ / }).
const (
	// SessionSortCreation keeps the existing manual/creation order — the order
	// sessions were added in, as adjusted by J/K. The default; no reordering.
	SessionSortCreation = "creation"
	// SessionSortStatus orders each repo group by action-priority: NeedsInput,
	// then unread Ready, then seen Ready, Running, Loading, Paused. Manual J/K
	// reordering is disabled while this mode is active (group order stays manual).
	SessionSortStatus = "status"
)

// GetConfigDir returns the path to the application's data/config directory.
//
// It prefers the new ~/.atrium layout, falls back to an existing legacy
// ~/.claude-squad directory without moving it, and otherwise defaults to
// ~/.atrium for fresh installs. The directory holds config.json, state.json, and
// the worktrees/ tree; the worktree and tmux paths recorded inside are absolute,
// so a legacy install must keep using its existing directory rather than be
// migrated. See RuntimeName for the matching tmux/socket identifiers.
func GetConfigDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get config home directory: %w", err)
	}
	newDir := filepath.Join(homeDir, configDirName)
	if DirExists(newDir) {
		return newDir, nil
	}
	if legacy := filepath.Join(homeDir, legacyConfigDirName); DirExists(legacy) {
		return legacy, nil
	}
	return newDir, nil
}

// WorktreesDir returns the directory that holds every session worktree:
// <config dir>/worktrees. It is the single source of truth for that path —
// session/git materializes worktrees under it, and the Claude workspace-trust
// shim trusts it — so it must always derive from GetConfigDir (never a
// hardcoded ~/.atrium or ~/.claude-squad).
func WorktreesDir() (string, error) {
	configDir, err := GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "worktrees"), nil
}

// Profile represents a named program configuration
type Profile struct {
	Name    string `json:"name"`
	Program string `json:"program"`
}

// ClaudeAccount maps a named Claude Code account to a CLAUDE_CONFIG_DIR and the
// route rules that auto-select it: git-remote substrings (RemoteMatches) and/or
// target-directory-path substrings (PathMatches, the routing signal for
// non-git/direct sessions, which have no origin remote). Rules are evaluated per
// account in config order (see ResolveClaudeAccount), not as a global remote-then-
// path pass. config_dir may use a leading ~ for the home directory. The first
// account with no route rules at all is the inferred catch-all default used when
// nothing else matches.
type ClaudeAccount struct {
	Name          string   `json:"name"`
	ConfigDir     string   `json:"config_dir"`
	RemoteMatches []string `json:"remote_matches,omitempty"`
	PathMatches   []string `json:"path_matches,omitempty"`
}

// ResolvedConfigDir expands a leading ~ in ConfigDir to the user's home directory.
func (a ClaudeAccount) ResolvedConfigDir() string {
	return expandHomePath(a.ConfigDir)
}

// IsCatchAll reports whether the account has no routing rules, making it the
// inferred default used when no remote or path route matches.
func (a ClaudeAccount) IsCatchAll() bool {
	return len(a.RemoteMatches) == 0 && len(a.PathMatches) == 0
}

// expandHomePath expands a leading "~" or "~/" in p to the user's home directory.
// On any failure resolving home, p is returned unchanged.
func expandHomePath(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

// Config represents the application configuration
type Config struct {
	// DefaultProgram is the default program to run in new instances
	DefaultProgram string `json:"default_program"`
	// AutoYes is a flag to automatically accept all prompts.
	AutoYes bool `json:"auto_yes"`
	// DaemonPollInterval is the interval (ms) at which the daemon polls sessions for autoyes mode.
	DaemonPollInterval int `json:"daemon_poll_interval"`
	// BranchPrefix is the prefix used for git branches created by the application.
	BranchPrefix string `json:"branch_prefix"`
	// Profiles is a list of named program profiles.
	Profiles []Profile `json:"profiles,omitempty"`
	// TmuxConfigOverride, when set to an existing file path, is used as the tmux
	// config for cs sessions instead of the bundled managed config. When empty,
	// cs materializes and uses its own config.
	TmuxConfigOverride string `json:"tmux_config_override,omitempty"`
	// AutoAttach, when true, automatically attaches to a new session as soon as it
	// starts (and has no initial prompt). nil means use the default (on), so the
	// feature stays enabled for config files written before it existed.
	AutoAttach *bool `json:"auto_attach,omitempty"`
	// ShowReleaseNotesAfterUpdate, when true, shows a dismissible "what's new"
	// overlay once after the app updates to a newer version. nil means use the
	// default (on), so configs written before it existed keep it.
	ShowReleaseNotesAfterUpdate *bool `json:"show_release_notes_after_update,omitempty"`
	// KillDoubleTapConfirm, when true, lets a second press of the kill key (Ctrl+X)
	// confirm the kill dialog, so Ctrl+X Ctrl+X tears a session down in one motion.
	// nil means use the default (on), so configs written before it existed keep it.
	KillDoubleTapConfirm *bool `json:"kill_double_tap_confirm,omitempty"`
	// Theme selects the UI color palette and border style by name (see ui/theme
	// registry: "tokyo-night", "catppuccin-mocha", "unicode"). Empty falls back
	// to the default. Glyphs are a separate axis — see NerdFont; the "unicode"
	// theme differs only by using square borders.
	Theme string `json:"theme,omitempty"`
	// NerdFont, when true, draws the branch / pull-request / dirty / auto markers
	// with vendor icons from a patched Nerd Font. nil/false (the default) uses
	// plain Unicode that renders on any font, so a bare terminal never shows tofu
	// boxes. Orthogonal to Theme: it applies on top of whichever color theme is
	// selected. Turn it on only if your terminal uses a patched Nerd Font.
	NerdFont *bool `json:"nerd_font,omitempty"`
	// SessionContextBar, when true, renders a thin tmux status line inside each
	// attached session (name · repo · branch · status + a strip of sibling
	// sessions in the same repo group). nil means use the default (on), so the
	// feature stays enabled for config files written before it existed. Setting
	// it false restores the chrome-free fullscreen pane (tmux status off).
	SessionContextBar *bool `json:"session_context_bar,omitempty"`
	// HintBar, when true, keeps a one-line key-hint bar at the bottom of the
	// screen during plain navigation. nil means use the default (on). Setting it
	// false restores the chrome-free interface, where the bar appears only for
	// inline interactions that need it (naming, filtering, progress).
	HintBar *bool `json:"hint_bar,omitempty"`
	// MaxSessions is an opt-in cap on how many sessions can exist at once;
	// creating one beyond it is rejected with an error in the UI. nil (or a
	// non-positive value) means unlimited — there is no cap by default.
	MaxSessions *int `json:"max_sessions,omitempty"`
	// TrustWorktreesRoot, when true, pre-accepts Claude Code's workspace-trust
	// dialog for the worktrees root in ~/.claude.json before sessions start.
	// Claude's trust check walks up parent directories, so trusting the root
	// covers every session worktree: project-scoped skills/hooks/MCP servers
	// load without the per-worktree dialog. Opt-in (nil/false = off) because it
	// writes outside Atrium's data dir and bypasses a deliberate Claude Code
	// confirmation — enable only if you trust the repos you open with Atrium.
	TrustWorktreesRoot *bool `json:"trust_worktrees_root,omitempty"`
	// CarryFiles lists repo-relative, gitignored files to copy from the origin
	// checkout into each newly materialized session worktree (worktrees carry
	// only tracked files, so local config like .claude/settings.local.json
	// would otherwise never reach a session). nil means use the default list;
	// an explicitly empty list opts out. Deliberately NOT omitempty: an
	// explicit [] must survive a save/load cycle instead of being dropped and
	// reverting to the default.
	CarryFiles []string `json:"carry_files"`
	// PRCreateDraft selects whether a PR opened with the create key (c) starts as
	// a draft. nil means use the default (draft), so configs predating this key
	// open drafts. Note: a draft PR cannot be merged with m until it is marked
	// ready for review (on GitHub); set this false for the one-key push→PR→merge
	// loop entirely in-app.
	PRCreateDraft *bool `json:"pr_create_draft,omitempty"`
	// UpdateBaseOnCreate selects whether a new session fetches its base branch and
	// branches off the freshest remote tip (origin/<base> when local is behind),
	// rather than a possibly-stale local branch. nil means use the default (on), so
	// configs predating this key freshen by default. Non-invasive: it never moves a
	// local branch — see FastForwardLocalBase for that.
	UpdateBaseOnCreate *bool `json:"update_base_on_create,omitempty"`
	// FastForwardLocalBase, when UpdateBaseOnCreate is also on, additionally
	// fast-forwards the local base branch to origin during session creation (a pure
	// ref move when the branch is not checked out, or git merge --ff-only when it is
	// and the tree is clean). Opt-in: nil/false means Atrium makes no local changes.
	FastForwardLocalBase *bool `json:"fast_forward_local_base,omitempty"`
	// ClaudeAccounts routes sessions to a per-session CLAUDE_CONFIG_DIR (which
	// Claude Code account a session runs under) by matching the worktree's git
	// origin remote or, for a non-git/direct session with no remote, its
	// directory path. Empty (the default) disables the feature entirely: no env
	// is injected and no account badge is shown, so configs predating this key
	// behave exactly as before.
	ClaudeAccounts []ClaudeAccount `json:"claude_accounts,omitempty"`
	// AutoUpdate selects the update behavior at TUI startup: "notify" (default
	// — check for a newer release and hint at `atrium update`), "auto"
	// (download + verify + stage in the background; applied on next launch), or
	// "off". Empty or unrecognized values behave as "notify". The explicit
	// `atrium update` command works regardless of this setting.
	AutoUpdate string `json:"auto_update,omitempty"`
	// ProjectSearchRoots lists the directories the background repo scan walks
	// to populate the new-session project picker with git repos the user has
	// never opened in Atrium. A leading "~" expands to the home directory.
	// nil or empty (configs predating this key) defaults to ["~"].
	ProjectSearchRoots []string `json:"project_search_roots,omitempty"`
	// ProjectSearchDepth bounds how many directory levels below each search
	// root the repo scan descends (a root's children are level 1). nil
	// defaults to 3; zero or negative disables the scan entirely; large
	// values are clamped so a typo can't walk the world.
	ProjectSearchDepth *int `json:"project_search_depth,omitempty"`
	// ModelIndicator controls the per-session model chip in the list: "on"
	// shows it on any session whose model is known (a --model flag before the
	// first turn, transcript truth after), "off" hides it. Everything else —
	// empty, unknown, and the retired "pinned"/"always" modes — normalizes to
	// "on" (GetModelIndicator).
	ModelIndicator string `json:"model_indicator,omitempty"`
	// PermissionIndicator controls the per-session permission-mode chip in the
	// list: "on" shows it for any pinned non-default mode (e.g. plan,
	// acceptEdits, auto), "off" hides it. Everything else normalizes to "on"
	// (GetPermissionIndicator).
	PermissionIndicator string `json:"permission_indicator,omitempty"`
	// SessionSort selects how sessions are ordered within each repo group:
	// "creation" (default — manual/creation order, reorderable with J/K) or
	// "status" (action-priority: NeedsInput, unread Ready, Ready, Running,
	// Loading, Paused). Empty or unrecognized values normalize to "creation"
	// (GetSessionSort). The group order itself stays manual ({ / }) in all modes.
	SessionSort string `json:"session_sort,omitempty"`
	// SmartDispatchAuto, when true, lets a confident deterministic project match from the
	// smart-dispatch input (the `i` key) create the session immediately, skipping the
	// confirmation form. Off (nil) by default: the pre-filled form always opens first.
	// Never applies to an LLM-routed guess — only an exact, unambiguous local match.
	// Auto-created sessions use the agent's default permission mode (skipping the form
	// forgoes the Permissions chip), so enable this only if that default suits you.
	SmartDispatchAuto *bool `json:"smart_dispatch_auto,omitempty"`
}

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

// GetSessionContextBar reports whether attached sessions should render the
// in-session context status line. A nil SessionContextBar (e.g. an older config
// file with no such key) defaults to on, mirroring GetAutoAttach.
func (c *Config) GetSessionContextBar() bool {
	return c.SessionContextBar == nil || *c.SessionContextBar
}

// GetNerdFont reports whether vendor Nerd-Font icons should be used. A nil
// NerdFont (an older config, or a fresh install) — or a nil Config — defaults to
// off, so the UI never renders tofu boxes without an explicit opt-in.
func (c *Config) GetNerdFont() bool {
	return c != nil && c.NerdFont != nil && *c.NerdFont
}

// GetHintBar reports whether the always-on bottom hint bar is enabled. A nil
// HintBar (e.g. an older config file with no such key) — or a nil Config —
// defaults to on.
func (c *Config) GetHintBar() bool {
	return c == nil || c.HintBar == nil || *c.HintBar
}

// GetAutoAttach reports whether new sessions should auto-attach on creation.
// A nil AutoAttach (e.g. an older config file with no such key) defaults to on.
func (c *Config) GetAutoAttach() bool {
	return c.AutoAttach == nil || *c.AutoAttach
}

// GetShowReleaseNotesAfterUpdate reports whether the post-update "what's new"
// overlay should be shown. A nil field (an older config file with no such key)
// — or a nil Config — defaults to on.
func (c *Config) GetShowReleaseNotesAfterUpdate() bool {
	return c == nil || c.ShowReleaseNotesAfterUpdate == nil || *c.ShowReleaseNotesAfterUpdate
}

// GetPRCreateDraft reports whether PRs opened with the create key (c) start as
// drafts. A nil PRCreateDraft (e.g. an older config file with no such key) — or a
// nil Config — defaults to draft.
func (c *Config) GetPRCreateDraft() bool {
	return c == nil || c.PRCreateDraft == nil || *c.PRCreateDraft
}

// GetUpdateBaseOnCreate reports whether new sessions branch off the freshest
// remote tip of their base. A nil field (an older config file with no such key)
// — or a nil Config — defaults to on.
func (c *Config) GetUpdateBaseOnCreate() bool {
	return c == nil || c.UpdateBaseOnCreate == nil || *c.UpdateBaseOnCreate
}

// GetFastForwardLocalBase reports whether session creation also fast-forwards the
// local base branch to origin. Defaults to off (opt-in): a nil field or nil
// Config makes no local changes.
func (c *Config) GetFastForwardLocalBase() bool {
	return c != nil && c.FastForwardLocalBase != nil && *c.FastForwardLocalBase
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
// such key) defaults to on.
func (c *Config) GetKillDoubleTapConfirm() bool {
	return c.KillDoubleTapConfirm == nil || *c.KillDoubleTapConfirm
}

// GetSmartDispatchAuto reports whether a confident deterministic smart-dispatch match
// may create a session without the confirmation form. Off by default (nil → false).
func (c *Config) GetSmartDispatchAuto() bool {
	return c.SmartDispatchAuto != nil && *c.SmartDispatchAuto
}

// GetTrustWorktreesRoot reports whether Atrium should pre-accept Claude Code's
// workspace trust for the worktrees root. Defaults OFF for a nil field: this
// writes to another tool's config file, so it is strictly opt-in.
func (c *Config) GetTrustWorktreesRoot() bool {
	return c.TrustWorktreesRoot != nil && *c.TrustWorktreesRoot
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

// ResolveClaudeAccount picks the Claude Code account for a target whose origin
// remote is remoteURL and whose directory is targetPath. It returns the account
// name (for the TUI badge), the resolved CLAUDE_CONFIG_DIR to inject (empty =
// inherit the current env), and whether the choice is the default/fallback
// account (drives dim styling). Matching is case-insensitive substring, evaluated
// per account in config order: the remote is tested against remote_matches, then
// the path against path_matches (the only signal for non-git/direct sessions,
// which have no remote); the first account that hits either wins. When nothing
// matches, the first account with no route rules is the inferred default; absent
// that, ("default", "", true) means inherit env.
func (c *Config) ResolveClaudeAccount(remoteURL, targetPath string) (name, configDir string, isDefault bool) {
	if len(c.ClaudeAccounts) == 0 {
		return "", "", false
	}
	lowerRemote := strings.ToLower(remoteURL)
	lowerPath := strings.ToLower(targetPath)
	defaultIdx := -1
	for i := range c.ClaudeAccounts {
		a := &c.ClaudeAccounts[i]
		if a.IsCatchAll() && defaultIdx == -1 {
			defaultIdx = i // first account with no route rules is the fallback
		}
		// Per account, in config order: try the origin remote first, then the
		// target directory path (the only signal for non-git/direct sessions).
		if lowerRemote != "" && containsAny(lowerRemote, a.RemoteMatches) {
			return a.Name, a.ResolvedConfigDir(), false
		}
		if lowerPath != "" && containsAny(lowerPath, a.PathMatches) {
			return a.Name, a.ResolvedConfigDir(), false
		}
	}
	if defaultIdx >= 0 {
		d := c.ClaudeAccounts[defaultIdx]
		return d.Name, d.ResolvedConfigDir(), true
	}
	return "default", "", true
}

// containsAny reports whether lower (already lowercased) contains any non-empty
// substring in subs (each lowercased on the fly), the shared rule used for both
// remote and path route matching.
func containsAny(lower string, subs []string) bool {
	for _, s := range subs {
		if s != "" && strings.Contains(lower, strings.ToLower(s)) {
			return true
		}
	}
	return false
}

// DefaultConfig returns the built-in defaults without probing the machine: no
// profiles, and the bare "claude" literal as the program. It is what tests
// construct directly (hermetic — no PATH lookups or shell spawns), while every
// LoadConfig fallback goes through seededDefaultConfig so a real user always
// gets detection.
func DefaultConfig() *Config {
	autoAttach := true
	killDoubleTap := true
	sessionContextBar := true
	hintBar := true
	showReleaseNotes := true
	updateBaseOnCreate := true
	return &Config{
		DefaultProgram:     defaultProgram,
		AutoYes:            false,
		DaemonPollInterval: 1000,
		Theme:              "tokyo-night",
		SessionContextBar:  &sessionContextBar,
		HintBar:            &hintBar,
		BranchPrefix: func() string {
			user, err := user.Current()
			if err != nil || user == nil || user.Username == "" {
				log.ErrorLog.Printf("failed to get current user: %v", err)
				return "session/"
			}
			return fmt.Sprintf("%s/", strings.ToLower(user.Username))
		}(),
		AutoAttach:                  &autoAttach,
		KillDoubleTapConfirm:        &killDoubleTap,
		ShowReleaseNotesAfterUpdate: &showReleaseNotes,
		UpdateBaseOnCreate:          &updateBaseOnCreate,
		CarryFiles:                  append([]string(nil), defaultCarryFiles...),
	}
}

// seededDefaultConfig is DefaultConfig with profiles seeded from the installed
// agent CLIs so the create-form picker works out of the box; claude leads when
// present (it is first in knownAgentBins) and becomes the default program,
// falling back to the bare "claude" literal when nothing is detected (the
// historical behavior for a machine with no agents yet). It is the config a
// user actually receives from every LoadConfig fallback — kept separate from
// DefaultConfig so tests constructing defaults never probe the machine.
func seededDefaultConfig() *Config {
	cfg := DefaultConfig()
	cfg.Profiles = DetectAgentProfiles()
	if len(cfg.Profiles) > 0 {
		cfg.DefaultProgram = cfg.Profiles[0].Name
	} else {
		log.WarningLog.Printf("no agent CLIs detected; defaulting program to %q", defaultProgram)
	}
	return cfg
}

// GetClaudeCommand attempts to find the "claude" command in the user's shell
// It checks in the following order:
// 1. Shell alias resolution: using "which" command
// 2. PATH lookup
//
// If both fail, it returns an error.
func GetClaudeCommand() (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash" // Default to bash if SHELL is not set
	}

	// Force the shell to load the user's profile and then run the command
	// For zsh, source .zshrc; for bash, source .bashrc
	var shellCmd string
	if strings.Contains(shell, "zsh") {
		shellCmd = "source ~/.zshrc &>/dev/null || true; which claude"
	} else if strings.Contains(shell, "bash") {
		shellCmd = "source ~/.bashrc &>/dev/null || true; which claude"
	} else {
		shellCmd = "which claude"
	}

	// One-shot startup probe with no ctx-bearing caller (config load runs before
	// any lifecycle context exists); Background capped at the probe timeout is
	// deliberate.
	ctx, cancel := context.WithTimeout(context.Background(), shellProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-c", shellCmd)
	output, err := cmd.Output()
	if err == nil {
		if program, ok := resolveClaudeCandidate(string(output)); ok {
			return program, nil
		}
	}

	// Otherwise, try to find in PATH directly
	claudePath, err := exec.LookPath("claude")
	if err == nil {
		return claudePath, nil
	}

	return "", fmt.Errorf("claude command not found in aliases or PATH")
}

// resolveClaudeCandidate interprets the output of `which claude` and returns a
// usable program path. The output may be a plain path, an alias definition
// (e.g. "claude: aliased to /usr/local/bin/claude"), or — when `claude` is a
// shell function — the full multi-line function body. We extract the alias
// target when present, then require the result to resolve to a real executable
// via exec.LookPath. If it does not (as happens with a function body, where the
// alias regex can capture a non-path token such as "$?"), we report no match so
// the caller falls back to a direct PATH lookup instead of persisting an
// unrunnable program as default_program — which otherwise causes new sessions to
// fail with "timed out waiting for tmux session ... (cleanup error: ...)".
func resolveClaudeCandidate(whichOutput string) (string, bool) {
	path := strings.TrimSpace(whichOutput)
	if path == "" {
		return "", false
	}

	// A shell function prints its entire multi-line body through `which`; that is
	// never a usable program path, and running the alias regex over it can capture
	// a stray token that happens to resolve (e.g. a binary name from an inline
	// "VAR=cmd" prefix, or "$?" from "local ret=$?"). Anything spanning multiple
	// lines is not a path, so reject it here and let the caller fall back to the
	// direct PATH lookup.
	if strings.ContainsAny(path, "\n\r") {
		return "", false
	}

	// Extract the target if the output is an alias definition.
	// Handle formats like "claude: aliased to /path/to/claude" or other shell-specific formats.
	aliasRegex := regexp.MustCompile(`(?:aliased to|->|=)\s*([^\s]+)`)
	if matches := aliasRegex.FindStringSubmatch(path); len(matches) > 1 {
		path = matches[1]
	}

	// Only trust the candidate if it actually resolves to an executable.
	if resolved, lookErr := exec.LookPath(path); lookErr == nil {
		return resolved, true
	}
	return "", false
}

// LoadConfig reads config.json from the data dir. It never fails: a missing
// file is created with defaults, and any read/parse error logs a warning and
// falls back to DefaultConfig.
func LoadConfig() *Config {
	configDir, err := GetConfigDir()
	if err != nil {
		log.ErrorLog.Printf("failed to get config directory: %v", err)
		return seededDefaultConfig()
	}

	configPath := filepath.Join(configDir, ConfigFileName)
	// Clear any temp files orphaned by a crash mid-write (see writeFileAtomic).
	sweepStaleTempFiles(configPath)

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Create and save default config if file doesn't exist
			defaultCfg := seededDefaultConfig()
			if saveErr := saveConfig(defaultCfg); saveErr != nil {
				log.WarningLog.Printf("failed to save default config: %v", saveErr)
			}
			return defaultCfg
		}

		log.WarningLog.Printf("failed to get config file: %v", err)
		return seededDefaultConfig()
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		// Preserve an unparseable config for recovery rather than silently
		// replacing it with defaults the next save would overwrite it with.
		if len(data) > 0 {
			if dst, qerr := quarantineCorruptFile(configPath); qerr != nil {
				log.ErrorLog.Printf("failed to parse config file and could not preserve it: parse=%v rename=%v", err, qerr)
			} else {
				log.ErrorLog.Printf("failed to parse config file; preserved corrupt copy at %s: %v", dst, err)
			}
		} else {
			log.ErrorLog.Printf("failed to parse config file: %v", err)
		}
		return seededDefaultConfig()
	}

	return &config
}

// saveConfig saves the configuration to disk
func saveConfig(config *Config) error {
	configDir, err := GetConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	configPath := filepath.Join(configDir, ConfigFileName)
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	return writeFileAtomic(configPath, data, 0644)
}

// SaveConfig exports the saveConfig function for use by other packages
func SaveConfig(config *Config) error {
	return saveConfig(config)
}
