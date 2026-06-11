package session

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ZviBaratz/atrium/config"
	"time"
)

// InstanceData represents the serializable data of an Instance
type InstanceData struct {
	Title       string    `json:"title"`
	DisplayName string    `json:"display_name,omitempty"`
	Path        string    `json:"path"`
	Branch      string    `json:"branch"`
	Status      Status    `json:"status"`
	Height      int       `json:"height"`
	Width       int       `json:"width"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	AutoYes     bool      `json:"auto_yes"`

	// Unread marks a Ready session the user has not visited since the agent last
	// finished a turn. omitempty keeps old state files (and seen sessions) compact;
	// absence deserializes to false (= seen), so upgrades stay quiet.
	Unread bool `json:"unread,omitempty"`

	// Direct marks a direct (non-git) session: no worktree or diff is serialized, and on
	// load the instance is rehydrated with a nil worktree.
	Direct bool `json:"direct,omitempty"`

	Program string `json:"program"`

	// ClaudeAccount / ClaudeConfigDir / ClaudeAccountDefault pin the Claude Code
	// account resolved at creation: the display name, the CLAUDE_CONFIG_DIR
	// actually injected into the tmux session, and whether it is the
	// default/fallback account (dim badge). All omitempty: a state.json predating
	// the feature decodes to empty -> no badge, no injection.
	ClaudeAccount        string `json:"claude_account,omitempty"`
	ClaudeConfigDir      string `json:"claude_config_dir,omitempty"`
	ClaudeAccountDefault bool   `json:"claude_account_is_default,omitempty"`

	// Model is the session's transcript-derived model id (e.g.
	// "claude-opus-4-7"), persisted so paused sessions keep their model chip.
	// omitempty: a state.json predating the feature decodes to "" -> the UI
	// falls back to the Program's --model flag.
	Model string `json:"model,omitempty"`

	Worktree  GitWorktreeData `json:"worktree"`
	DiffStats DiffStatsData   `json:"diff_stats"`
}

// GitWorktreeData represents the serializable data of a Worktree
type GitWorktreeData struct {
	RepoPath         string `json:"repo_path"`
	WorktreePath     string `json:"worktree_path"`
	SessionName      string `json:"session_name"`
	BranchName       string `json:"branch_name"`
	BaseCommitSHA    string `json:"base_commit_sha"`
	BaseRef          string `json:"base_ref"`
	IsExistingBranch bool   `json:"is_existing_branch"`
}

// DiffStatsData represents the serializable data of a DiffStats
type DiffStatsData struct {
	Added        int    `json:"added"`
	Removed      int    `json:"removed"`
	Content      string `json:"content"`
	FilesChanged int    `json:"files_changed"`
	Commits      int    `json:"commits"`
	Behind       int    `json:"behind"`
	Dirty        bool   `json:"dirty"`
}

// Storage handles saving and loading instances using the state interface
type Storage struct {
	state config.InstanceStorage
}

// NewStorage creates a new storage instance
func NewStorage(state config.InstanceStorage) (*Storage, error) {
	return &Storage{
		state: state,
	}, nil
}

// SaveInstances saves the list of instances to disk
func (s *Storage) SaveInstances(instances []*Instance) error {
	// Convert instances to InstanceData
	data := make([]InstanceData, 0)
	for _, instance := range instances {
		if instance.Started() {
			data = append(data, instance.ToInstanceData())
		}
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal instances: %w", err)
	}

	return s.state.SaveInstances(jsonData)
}

// LoadInstances loads the list of instances from disk. ctx is the lifecycle
// context reconstructed instances derive their subprocess contexts from.
func (s *Storage) LoadInstances(ctx context.Context) ([]*Instance, error) {
	instancesData, err := s.loadInstanceData()
	if err != nil {
		return nil, err
	}

	// Load config once for the whole batch; FromInstanceData only needs BranchPrefix.
	cfg := config.LoadConfig()
	instances := make([]*Instance, len(instancesData))
	for i, data := range instancesData {
		instance, err := FromInstanceData(ctx, data, cfg.BranchPrefix)
		if err != nil {
			return nil, fmt.Errorf("failed to create instance %s: %w", data.Title, err)
		}
		instances[i] = instance
	}

	return instances, nil
}

// loadInstanceData reads the persisted instances as raw serialized data, without
// reconstructing live Instance objects. Mutating storage (delete/update) goes
// through this path so a stored entry whose repo/worktree no longer exists on
// disk cannot block the operation or be corrupted by a reconstruct round-trip.
func (s *Storage) loadInstanceData() ([]InstanceData, error) {
	jsonData := s.state.GetInstances()

	var instancesData []InstanceData
	if err := json.Unmarshal(jsonData, &instancesData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal instances: %w", err)
	}
	return instancesData, nil
}

// saveInstanceData persists raw serialized instance data as-is. Unlike
// SaveInstances it does not filter on Started(): callers operate on data already
// read from disk, so every entry was persisted before and must round-trip intact.
func (s *Storage) saveInstanceData(data []InstanceData) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal instances: %w", err)
	}
	return s.state.SaveInstances(jsonData)
}

// DeleteInstance removes an instance from storage. It operates on the serialized
// data directly so an orphaned entry (repo/worktree gone) can always be removed
// and unrelated siblings are left byte-for-byte intact.
func (s *Storage) DeleteInstance(title string) error {
	instances, err := s.loadInstanceData()
	if err != nil {
		return fmt.Errorf("failed to load instances: %w", err)
	}

	found := false
	newInstances := make([]InstanceData, 0, len(instances))
	for _, data := range instances {
		if data.Title == title {
			found = true
			continue
		}
		newInstances = append(newInstances, data)
	}

	if !found {
		return fmt.Errorf("instance not found: %s", title)
	}

	return s.saveInstanceData(newInstances)
}

// UpdateInstance updates an existing instance in storage. Only the target entry
// is replaced; other entries are preserved exactly as stored (no reconstruct).
func (s *Storage) UpdateInstance(instance *Instance) error {
	instances, err := s.loadInstanceData()
	if err != nil {
		return fmt.Errorf("failed to load instances: %w", err)
	}

	data := instance.ToInstanceData()
	found := false
	for i, existing := range instances {
		if existing.Title == data.Title {
			instances[i] = data
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("instance not found: %s", data.Title)
	}

	return s.saveInstanceData(instances)
}

// DeleteAllInstances removes all stored instances
func (s *Storage) DeleteAllInstances() error {
	return s.state.DeleteAllInstances()
}
