package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Names of the state files inside the data dir.
const (
	StateFileName     = "state.json"
	InstancesFileName = "instances.json"
)

// InstanceStorage handles instance-related operations
type InstanceStorage interface {
	// SaveInstances saves the raw instance data
	SaveInstances(instancesJSON json.RawMessage) error
	// GetInstances returns the raw instance data
	GetInstances() json.RawMessage
	// DeleteAllInstances removes all stored instances
	DeleteAllInstances() error
}

// AppState handles application-level state
type AppState interface {
	// GetHelpScreensSeen returns the bitmask of seen help screens
	GetHelpScreensSeen() uint32
	// SetHelpScreensSeen updates the bitmask of seen help screens
	SetHelpScreensSeen(seen uint32) error
	// GetRecentPaths returns recently-used project directories, most-recent-first
	GetRecentPaths() []string
	// AddRecentPath records a project directory as most-recently-used
	AddRecentPath(path string) error
	// GetKnownProjects returns every project directory ever used for a session,
	// most-recent-first (the durable long tail behind GetRecentPaths)
	GetKnownProjects() []string
	// GetScannedRepos returns the cached repo-scan results and when they were produced
	GetScannedRepos() ([]string, time.Time)
	// SetScannedRepos stores a completed repo scan's results, stamped now
	SetScannedRepos(paths []string) error
	// GetCollapsedRepos returns the repo group keys that should render folded
	GetCollapsedRepos() []string
	// SetCollapsedRepos replaces the set of folded repo group keys
	SetCollapsedRepos(repos []string) error
	// GetAccountOrder returns the chosen order of account clusters in the session list
	GetAccountOrder() []string
	// SetAccountOrder replaces the chosen order of account clusters
	SetAccountOrder(accounts []string) error
	// GetListRatio returns the fraction of width given to the session list
	GetListRatio() float64
	// SetListRatio stores the list/preview split (clamped to a sane range)
	SetListRatio(ratio float64) error
	// GetLastNotesVersion returns the version whose release notes were last
	// shown after an update ("" if none ever were)
	GetLastNotesVersion() string
	// SetLastNotesVersion records the version whose release notes were just shown
	SetLastNotesVersion(version string) error
	// GetAckedDrift returns a copy of the agent-key → acknowledged-version map
	// (never nil); mutating it does not affect persisted state.
	GetAckedDrift() map[string]string
	// SetAckedDrift merges the given agent-key → installed-version acknowledgements
	// and persists once. A nil/empty map is a no-op.
	SetAckedDrift(acks map[string]string) error
	// GetDraft returns a copy of the stashed new-session draft, or nil when none is
	// stashed. Mutating the result does not affect persisted state.
	GetDraft() *SessionDraft
	// SetDraft stores the stashed new-session draft and persists it.
	SetDraft(draft *SessionDraft) error
	// ClearDraft drops any stashed new-session draft. A no-op (no write) when none
	// is stashed.
	ClearDraft() error
	// GetPromptHistory returns recently-submitted prompts, most-recent-first.
	GetPromptHistory() []PromptHistoryEntry
	// AddPromptHistory records a submitted prompt at the front of the history,
	// deduplicating a consecutive repeat and capping the list; persists once.
	AddPromptHistory(text string) error
	// ClearPromptHistory empties the prompt history. A no-op (no write) when empty.
	ClearPromptHistory() error
}

// maxPromptHistory caps how many recently-submitted prompts are retained for
// reuse. Prompts are cheap to store; the cap just keeps state.json bounded.
const maxPromptHistory = 50

// PromptHistoryEntry is one persisted, reusable prompt: the submitted text plus
// when it was sent (unix seconds, so omitempty works and old files read cleanly).
type PromptHistoryEntry struct {
	Text   string `json:"text"`
	AtUnix int64  `json:"at_unix,omitempty"`
}

// maxRecentPaths caps how many recently-used project directories are retained.
const maxRecentPaths = 10

// maxKnownProjects caps the durable known-projects list — the long tail behind
// RecentPaths' short head. Generous on purpose: this is what keeps a non-git
// (direct-session) directory fuzzy-searchable after it falls out of recents,
// since no repo scan will ever rediscover it.
const maxKnownProjects = 100

// List/preview split bounds. listRatio is the fraction of the terminal width
// given to the session list; the clamp keeps either pane from collapsing.
const (
	defaultListRatio = 0.30
	minListRatio     = 0.15
	maxListRatio     = 0.60
)

// clampListRatio bounds r to [minListRatio, maxListRatio].
func clampListRatio(r float64) float64 {
	if r < minListRatio {
		return minListRatio
	}
	if r > maxListRatio {
		return maxListRatio
	}
	return r
}

// ClampListRatio bounds r to the same [min,max] range SetListRatio enforces, for
// callers computing a live value that isn't persisted yet (e.g. a divider drag).
func ClampListRatio(r float64) float64 {
	return clampListRatio(r)
}

// StateManager combines instance storage and app state management
type StateManager interface {
	InstanceStorage
	AppState
}

// State represents the application state that persists between sessions
type State struct {
	// HelpScreensSeen is a bitmask tracking which help screens have been shown
	HelpScreensSeen uint32 `json:"help_screens_seen"`
	// Instances stores the serialized instance data as raw JSON
	InstancesData json.RawMessage `json:"instances"`
	// RecentPaths is the list of recently-used project directories, most-recent-first
	RecentPaths []string `json:"recent_paths"`
	// CollapsedRepos is the set of repo group keys the session list should render folded
	CollapsedRepos []string `json:"collapsed_repos"`
	// AccountOrder is the chosen order of the session list's account clusters,
	// most-preferred first (see ui.List.SetAccountOrder). An account missing from the
	// list falls back to first-appearance order and "" (no account) trails last, so an
	// absent key — an older state file — reproduces the pre-reordering behavior exactly.
	// Names with no live sessions are kept rather than pruned: they cost nothing in the
	// view and are what restores an account's slot when a session for it returns.
	AccountOrder []string `json:"account_order,omitempty"`
	// ListRatio is the fraction of the terminal width given to the session list.
	// Zero (an older state file with no such key) reads back as defaultListRatio.
	ListRatio float64 `json:"list_ratio,omitempty"`
	// KnownProjects is every project directory ever used for a session (git or
	// direct), most-recent-first, capped at maxKnownProjects. It is maintained
	// alongside RecentPaths by AddRecentPath and feeds the new-session picker's
	// fuzzy search with the durable long tail.
	KnownProjects []string `json:"known_projects,omitempty"`
	// ScannedRepos caches the last completed background repo scan so the first
	// form-open after launch is populated instantly. The order carries the
	// scanner's most-recently-active-first ranking.
	ScannedRepos []string `json:"scanned_repos,omitempty"`
	// LastRepoScanUnix is when ScannedRepos was produced, in unix seconds.
	// Zero (including older state files) means never scanned. An int64 rather
	// than time.Time so omitempty works and old files read back cleanly.
	LastRepoScanUnix int64 `json:"last_repo_scan_unix,omitempty"`
	// LastNotesVersion is the version whose post-update "what's new" notes were
	// last shown. Empty (an older state file, or a fresh install) means none
	// have been shown yet.
	LastNotesVersion string `json:"last_notes_version,omitempty"`
	// AckedDrift maps an agent key to the installed version the user dismissed the
	// heuristic-drift hint for. The hint stays quiet while installed == acked; a
	// later version bump re-arms it.
	AckedDrift map[string]string `json:"acked_drift,omitempty"`
	// Draft is the dirty new-session form stashed on Escape, persisted so a deliberate
	// non-destructive cancel survives a crash or quit (not just an in-run reopen). Nil
	// (an older state file, or no stash) means there is nothing to restore. It mirrors
	// the in-memory home.stashedDraft and is cleared on submit / restore / clear-form.
	Draft *SessionDraft `json:"draft,omitempty"`
	// PromptHistory is the most-recent-first ring of submitted prompts, capped at
	// maxPromptHistory, offered for reuse in the create form and quick-send. Absent
	// (an older state file) reads back as no history.
	PromptHistory []PromptHistoryEntry `json:"prompt_history,omitempty"`
}

// SessionDraft is the persisted, serializable projection of a stashed new-session
// form: only the free-text fields and the chosen project, which have stable overlay
// setters. The live overlay (cursors, async pickers, closures) is not serializable,
// so the form is rebuilt from these values on the next open.
type SessionDraft struct {
	Title  string `json:"title,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	Path   string `json:"path,omitempty"`
}

// DefaultState returns the default state
func DefaultState() *State {
	return &State{
		HelpScreensSeen: 0,
		InstancesData:   json.RawMessage("[]"),
		RecentPaths:     []string{},
		CollapsedRepos:  []string{},
		ListRatio:       defaultListRatio,
	}
}

// LoadState loads the state from disk. If it cannot be done, we return the default
// state. A torn or unparseable file is preserved for recovery before falling back,
// so persisted sessions are never silently discarded. See loadJSONFile.
func LoadState() *State {
	return loadJSONFile(StateFileName, "state", DefaultState, SaveState)
}

// SaveState saves the state to disk
func SaveState(state *State) error {
	configDir, err := GetConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	statePath := filepath.Join(configDir, StateFileName)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	return writeFileAtomic(statePath, data, 0644)
}

// InstanceStorage interface implementation

// SaveInstances saves the raw instance data
func (s *State) SaveInstances(instancesJSON json.RawMessage) error {
	s.InstancesData = instancesJSON
	return SaveState(s)
}

// GetInstances returns the raw instance data
func (s *State) GetInstances() json.RawMessage {
	return s.InstancesData
}

// DeleteAllInstances removes all stored instances
func (s *State) DeleteAllInstances() error {
	s.InstancesData = json.RawMessage("[]")
	return SaveState(s)
}

// AppState interface implementation

// GetHelpScreensSeen returns the bitmask of seen help screens
func (s *State) GetHelpScreensSeen() uint32 {
	return s.HelpScreensSeen
}

// SetHelpScreensSeen updates the bitmask of seen help screens
func (s *State) SetHelpScreensSeen(seen uint32) error {
	s.HelpScreensSeen = seen
	return SaveState(s)
}

// GetRecentPaths returns recently-used project directories, most-recent-first.
func (s *State) GetRecentPaths() []string {
	return s.RecentPaths
}

// AddRecentPath records a project directory as most-recently-used in both MRU
// lists — the short RecentPaths head shown first in the picker, and the durable
// KnownProjects tail that keeps the path fuzzy-searchable after it falls out of
// recents. One call, one save.
func (s *State) AddRecentPath(path string) error {
	if path == "" {
		return nil
	}
	s.RecentPaths = promoteFront(s.RecentPaths, path, maxRecentPaths)
	s.KnownProjects = promoteFront(s.KnownProjects, path, maxKnownProjects)
	return SaveState(s)
}

// promoteFront returns list with path moved (or inserted) at the front,
// deduplicated, and capped at limit entries.
func promoteFront(list []string, path string, limit int) []string {
	deduped := []string{path}
	for _, p := range list {
		if p == path {
			continue
		}
		deduped = append(deduped, p)
		if len(deduped) >= limit {
			break
		}
	}
	return deduped
}

// GetKnownProjects returns every project directory ever used for a session,
// most-recent-first.
func (s *State) GetKnownProjects() []string {
	return s.KnownProjects
}

// GetScannedRepos returns the cached repo-scan results and when they were
// produced; a zero time means no scan has ever completed.
func (s *State) GetScannedRepos() ([]string, time.Time) {
	if s.LastRepoScanUnix == 0 {
		return s.ScannedRepos, time.Time{}
	}
	return s.ScannedRepos, time.Unix(s.LastRepoScanUnix, 0)
}

// SetScannedRepos stores a completed repo scan's results (order preserved — it
// carries the scanner's ranking), stamps the time, and persists.
func (s *State) SetScannedRepos(paths []string) error {
	s.ScannedRepos = paths
	s.LastRepoScanUnix = time.Now().Unix()
	return SaveState(s)
}

// GetCollapsedRepos returns the repo group keys that should render folded.
func (s *State) GetCollapsedRepos() []string {
	return s.CollapsedRepos
}

// SetCollapsedRepos replaces the set of folded repo group keys and persists it.
func (s *State) SetCollapsedRepos(repos []string) error {
	s.CollapsedRepos = repos
	return SaveState(s)
}

// GetAccountOrder returns the chosen order of the session list's account clusters.
func (s *State) GetAccountOrder() []string {
	return s.AccountOrder
}

// SetAccountOrder replaces the chosen order of account clusters and persists it.
func (s *State) SetAccountOrder(accounts []string) error {
	s.AccountOrder = accounts
	return SaveState(s)
}

// GetListRatio returns the fraction of width given to the session list. A zero or
// out-of-range stored value (older state files, or a hand-edited state.json) is
// normalized: zero becomes the default, everything else clamps to the bounds.
func (s *State) GetListRatio() float64 {
	if s.ListRatio == 0 {
		return defaultListRatio
	}
	return clampListRatio(s.ListRatio)
}

// SetListRatio stores the list/preview split, clamped to a sane range, and persists it.
func (s *State) SetListRatio(ratio float64) error {
	s.ListRatio = clampListRatio(ratio)
	return SaveState(s)
}

// GetLastNotesVersion returns the version whose post-update notes were last
// shown, or "" if none ever were.
func (s *State) GetLastNotesVersion() string {
	return s.LastNotesVersion
}

// SetLastNotesVersion records the version whose post-update notes were just
// shown (or seeded on first run) and persists it.
func (s *State) SetLastNotesVersion(version string) error {
	s.LastNotesVersion = version
	return SaveState(s)
}

// GetAckedDrift returns a copy of the acknowledged-drift map, never nil. The
// copy keeps callers (including the startup probe goroutine) from aliasing the
// persisted map.
func (s *State) GetAckedDrift() map[string]string {
	if len(s.AckedDrift) == 0 {
		return map[string]string{}
	}
	return maps.Clone(s.AckedDrift)
}

// SetAckedDrift merges the given agent-key → installed-version acknowledgements
// and persists once. A nil/empty map is a no-op (no write).
func (s *State) SetAckedDrift(acks map[string]string) error {
	if len(acks) == 0 {
		return nil
	}
	if s.AckedDrift == nil {
		s.AckedDrift = map[string]string{}
	}
	for k, v := range acks {
		s.AckedDrift[k] = v
	}
	return SaveState(s)
}

// GetDraft returns a shallow copy of the stashed new-session draft (never the
// stored pointer), or nil when none is stashed, so a caller can read the values
// without aliasing persisted state.
func (s *State) GetDraft() *SessionDraft {
	if s.Draft == nil {
		return nil
	}
	d := *s.Draft
	return &d
}

// SetDraft stores the stashed new-session draft and persists it. The app only
// stashes a dirty form, so this is never called with an all-empty draft.
func (s *State) SetDraft(draft *SessionDraft) error {
	s.Draft = draft
	return SaveState(s)
}

// ClearDraft drops any stashed new-session draft and persists. A no-op (no write)
// when nothing is stashed, so the common clear-on-submit path stays cheap.
func (s *State) ClearDraft() error {
	if s.Draft == nil {
		return nil
	}
	s.Draft = nil
	return SaveState(s)
}

// GetPromptHistory returns the reusable prompt history, most-recent-first.
func (s *State) GetPromptHistory() []PromptHistoryEntry {
	return s.PromptHistory
}

// AddPromptHistory records a submitted prompt at the front of the history. It
// skips a blank prompt and a *consecutive* repeat of the current head (so
// re-sending the same thing twice in a row does not pile up), but keeps
// non-consecutive repeats — a prompt reused after others is genuinely recent
// again. The list is capped at maxPromptHistory and persisted once.
func (s *State) AddPromptHistory(text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if len(s.PromptHistory) > 0 && s.PromptHistory[0].Text == text {
		return nil
	}
	s.PromptHistory = append([]PromptHistoryEntry{{Text: text, AtUnix: time.Now().Unix()}}, s.PromptHistory...)
	if len(s.PromptHistory) > maxPromptHistory {
		s.PromptHistory = s.PromptHistory[:maxPromptHistory]
	}
	return SaveState(s)
}

// ClearPromptHistory empties the prompt history and persists. A no-op (no write)
// when it is already empty.
func (s *State) ClearPromptHistory() error {
	if len(s.PromptHistory) == 0 {
		return nil
	}
	s.PromptHistory = nil
	return SaveState(s)
}
