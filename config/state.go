package config

import (
	"encoding/json"
	"fmt"
	"github.com/ZviBaratz/atrium/log"
	"os"
	"path/filepath"
)

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
		log.ErrorLog.Printf("failed to parse state file: %v", err)
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

	return os.WriteFile(statePath, data, 0644)
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

// AddRecentPath records a project directory as most-recently-used: it is moved to
// the front, duplicates are removed, and the list is capped at maxRecentPaths.
func (s *State) AddRecentPath(path string) error {
	if path == "" {
		return nil
	}
	deduped := []string{path}
	for _, p := range s.RecentPaths {
		if p == path {
			continue
		}
		deduped = append(deduped, p)
		if len(deduped) >= maxRecentPaths {
			break
		}
	}
	s.RecentPaths = deduped
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
