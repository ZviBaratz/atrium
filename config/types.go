package config

import (
	"os"
	"path/filepath"
	"strings"
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

// GHAccount maps a GitHub CLI config dir (injected as GH_CONFIG_DIR) to the same
// kind of route rules as ClaudeAccount. It is a separate section from
// ClaudeAccounts so gh-account routing can differ from Claude-login routing, but
// it reuses the identical matching machinery (see ResolveGHConfigDir): per-account
// in config order, remote substrings first then path substrings; the first
// rule-less account is the catch-all. config_dir may use a leading ~.
type GHAccount struct {
	Name          string   `json:"name"`
	ConfigDir     string   `json:"config_dir"`
	RemoteMatches []string `json:"remote_matches,omitempty"`
	PathMatches   []string `json:"path_matches,omitempty"`
	// TokenEnv lists env var names to inject this account's gh token under at
	// session launch (e.g. ["GITHUB_PERSONAL_ACCESS_TOKEN"], which the github MCP
	// reads as its Bearer token). Empty (the default) injects no token, preserving
	// the pre-feature behavior. The token itself is resolved fresh at session start
	// via `gh auth token` under ConfigDir and is never persisted; only these names
	// are stored. ConfigDir should hold a single account so the token is
	// unambiguous. GH_CONFIG_DIR already routes the agent's own `gh` CLI to this
	// account, so TokenEnv is mainly for tools that read the token straight from the
	// env (like the github MCP), not the CLI. Adding "GH_TOKEN"/"GITHUB_TOKEN"
	// additionally pins the CLI to this account's token, overriding gh's own
	// selection — handy when the OS keyring's shared default would otherwise shadow
	// it (see resolveGitHubToken), but otherwise leave them out.
	TokenEnv []string `json:"token_env,omitempty"`
}

// ResolvedConfigDir expands a leading ~ in ConfigDir to the user's home directory.
func (a GHAccount) ResolvedConfigDir() string {
	return expandHomePath(a.ConfigDir)
}

// IsCatchAll reports whether the account has no routing rules, making it the
// inferred default used when no remote or path route matches.
func (a GHAccount) IsCatchAll() bool {
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
	// GHAccounts routes sessions to a per-session GH_CONFIG_DIR (which GitHub CLI
	// account `gh` runs under) by the same remote/path matching as ClaudeAccounts,
	// in an independent section so gh routing can differ from Claude-login routing.
	// The dir is injected into the agent's tmux session (so the agent's own `gh`
	// and any https credential-helper calls pick the right account) and into
	// Atrium's own `gh` subprocesses (PR create/merge/view). Empty (the default)
	// disables the feature: no env is injected and gh inherits the ambient
	// (global-active) account, exactly as before this key existed.
	GHAccounts []GHAccount `json:"gh_accounts,omitempty"`
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
