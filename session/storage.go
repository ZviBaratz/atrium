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
	Title       string `json:"title"`
	DisplayName string `json:"display_name,omitempty"`
	// Note is an optional freeform annotation shown on the session's row (e.g.
	// "blocked on review"). omitempty keeps old state files compact; absence
	// decodes to "" (no note).
	Note      string    `json:"note,omitempty"`
	Path      string    `json:"path"`
	Branch    string    `json:"branch"`
	Status    Status    `json:"status"`
	Height    int       `json:"height"`
	Width     int       `json:"width"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	AutoYes   bool      `json:"auto_yes"`

	// PromptQueue is the FIFO of prompts queued but not yet delivered to the agent.
	// Persisting it lets pending prompts survive a restart before delivery and be
	// re-delivered in order; delivered prompts have been popped, so this is empty for all
	// but freshly-created or busy sessions. omitempty keeps prompt-less sessions
	// byte-identical to before the field existed.
	PromptQueue []QueuedPromptData `json:"prompt_queue,omitempty"`

	// Prompt / PromptQueuedAt are the legacy single-prompt fields, no longer written
	// (superseded by PromptQueue). They remain decode-only so a state.json that predates
	// the queue migrates on load: FromInstanceData reads Prompt into a one-element queue
	// when PromptQueue is absent.
	Prompt         string    `json:"prompt,omitempty"`
	PromptQueuedAt time.Time `json:"prompt_queued_at,omitempty"`

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
	// GHConfigDir is the GH_CONFIG_DIR resolved at creation and injected into the
	// tmux session + Atrium's gh subprocesses. omitempty: a state.json predating
	// the feature decodes to empty -> no injection (inherit ambient gh account).
	GHConfigDir string `json:"gh_config_dir,omitempty"`
	// GitHubTokenEnv are the env var names the routed account's gh token is
	// injected under at launch (config.GHAccount.TokenEnv). Only the NAMES are
	// persisted — the token VALUE is resolved fresh at session start and never
	// serialized, so state.json never holds a credential. omitempty: legacy
	// state.json decodes to nil -> no token injection.
	GitHubTokenEnv []string `json:"github_token_env,omitempty"`

	// Model is the session's transcript-derived model id (e.g.
	// "claude-opus-4-7"), persisted so paused sessions keep their model chip.
	// omitempty: a state.json predating the feature decodes to "" -> the UI
	// falls back to the Program's --model flag.
	Model string `json:"model,omitempty"`

	// PermissionMode is the live permission mode last detected from the footer
	// (e.g. "auto"), persisted so paused sessions keep the chip. omitempty: a
	// state.json predating the feature decodes to "" -> the UI falls back to the
	// Program's --permission-mode flag, refreshed on the next poll after resume.
	PermissionMode string `json:"permission_mode,omitempty"`

	// TmuxName is the session's tmux session name. It is persisted state, not a
	// derivation: new sessions mint a repo-qualified name (so identical titles
	// in different repo groups coexist on the shared socket). omitempty: a
	// state.json predating the field decodes to "" -> the instance falls back
	// to the legacy title-derived name its live session still has.
	TmuxName string `json:"tmux_name,omitempty"`

	Worktree  GitWorktreeData `json:"worktree"`
	DiffStats DiffStatsData   `json:"diff_stats"`
}

// QueuedPromptData is the serializable form of one queued prompt. QueuedAt is
// persisted for diagnostics but is not load-bearing: FromInstanceData resets the
// restored head's clock to load time and treats the rest as strict idle-only, so a
// zero (omitted) QueuedAt round-trips safely.
type QueuedPromptData struct {
	Text     string    `json:"text"`
	QueuedAt time.Time `json:"queued_at,omitempty"`
}

// toQueuedPromptData converts an Instance's internal queue snapshot to its
// serializable form.
func toQueuedPromptData(queue []queuedPrompt) []QueuedPromptData {
	if len(queue) == 0 {
		return nil
	}
	out := make([]QueuedPromptData, len(queue))
	for idx, qp := range queue {
		out[idx] = QueuedPromptData{Text: qp.text, QueuedAt: qp.queuedAt}
	}
	return out
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
	// Convert instances to InstanceData (pre-sized: at most one entry per instance).
	data := make([]InstanceData, 0, len(instances))
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
		// FromInstanceData is now pure rehydration; reattach to the live tmux session
		// (or recover in place) as the separate IO step. Safe to call here: the
		// instance is not published until the returned slice reaches the poll loop,
		// satisfying reattach's pre-publication precondition.
		instance.reattach()
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
// and unrelated siblings are left byte-for-byte intact. Matching is composite
// (Title, Path): titles are only unique per repo group, so a same-titled session
// in another repo must never be the one removed.
func (s *Storage) DeleteInstance(title, path string) error {
	instances, err := s.loadInstanceData()
	if err != nil {
		return fmt.Errorf("failed to load instances: %w", err)
	}

	found := false
	newInstances := make([]InstanceData, 0, len(instances))
	for _, data := range instances {
		if data.Title == title && data.Path == path {
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
// Matching is composite (Title, Path) — see DeleteInstance.
func (s *Storage) UpdateInstance(instance *Instance) error {
	instances, err := s.loadInstanceData()
	if err != nil {
		return fmt.Errorf("failed to load instances: %w", err)
	}

	data := instance.ToInstanceData()
	found := false
	for i, existing := range instances {
		if existing.Title == data.Title && existing.Path == data.Path {
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

// RepoPaths returns the repository path of every stored instance, read from the
// raw serialized data. Like DeleteInstance it deliberately bypasses
// LoadInstances: rehydrating would reattach — or recover, i.e. relaunch — live
// sessions, which a caller like `reset` is about to destroy, and a raw read
// cannot fail on an orphaned entry. Direct sessions have no repo and yield ""
// (matching Instance.GetRepoPath); the empties are harmless — the consumer,
// git.CleanupWorktrees, drops them itself.
func (s *Storage) RepoPaths() ([]string, error) {
	instances, err := s.loadInstanceData()
	if err != nil {
		return nil, fmt.Errorf("failed to load instances: %w", err)
	}
	paths := make([]string, 0, len(instances))
	for _, data := range instances {
		paths = append(paths, data.Worktree.RepoPath)
	}
	return paths, nil
}
