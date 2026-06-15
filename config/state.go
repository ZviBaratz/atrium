package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ZviBaratz/atrium/log"
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
	// GetListRatio returns the fraction of width given to the session list
	GetListRatio() float64
	// SetListRatio stores the list/preview split (clamped to a sane range)
	SetListRatio(ratio float64) error
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

// LoadState loads the state from disk. If it cannot be done, we return the default state.
func LoadState() *State {
	configDir, err := GetConfigDir()
	if err != nil {
		log.ErrorLog.Printf("failed to get config directory: %v", err)
		return DefaultState()
	}

	statePath := filepath.Join(configDir, StateFileName)
	// Clear any temp files orphaned by a crash mid-write (see writeFileAtomic).
	sweepStaleTempFiles(statePath)

	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Create and save default state if file doesn't exist
			defaultState := DefaultState()
			if saveErr := SaveState(defaultState); saveErr != nil {
				log.WarningLog.Printf("failed to save default state: %v", saveErr)
			}
			return defaultState
		}

		log.WarningLog.Printf("failed to get state file: %v", err)
		return DefaultState()
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		// A torn or unparseable file would otherwise be silently discarded along
		// with every persisted session. Preserve it for recovery before falling
		// back to defaults, and log loudly. Empty files are not worth archiving.
		if len(data) > 0 {
			if dst, qerr := quarantineCorruptFile(statePath); qerr != nil {
				log.ErrorLog.Printf("failed to parse state file and could not preserve it: parse=%v rename=%v", err, qerr)
			} else {
				log.ErrorLog.Printf("failed to parse state file; preserved corrupt copy at %s: %v", dst, err)
			}
		} else {
			log.ErrorLog.Printf("failed to parse state file: %v", err)
		}
		return DefaultState()
	}

	return &state
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
