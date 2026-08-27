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

	// StatusChangedAt is when Status last actually changed. It is the only
	// timestamp here that dates the SESSION rather than the snapshot: CreatedAt
	// is the worktree's age and UpdatedAt is stamped at serialization, so
	// neither can answer "how long has this been waiting on me". Persisting it
	// is what lets the answer survive a restart — without it the restored
	// instance re-stamps from now on its first observed status, and the whole
	// fleet reads as having just changed.
	//
	// No omitempty, deliberately: encoding/json only omits empty values for
	// basic types, so the tag is silently inert on a time.Time (see
	// PromptQueuedAt below, which carries it and emits anyway). Absence in a
	// state file written before this field decodes to the zero time, which
	// recordStatusChange re-stamps on first observation and cli_ls publishes as
	// null rather than as the year 1.
	StatusChangedAt time.Time `json:"status_changed_at"`

	AutoYes bool `json:"auto_yes"`

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

	// Muted marks a session the user has silenced (M): it never notifies. omitempty
	// keeps old state files compact; absence deserializes to false (= not muted).
	Muted bool `json:"muted,omitempty"`

	// Direct marks a direct (non-git) session: no worktree or diff is serialized, and on
	// load the instance is rehydrated with a nil worktree.
	Direct bool `json:"direct,omitempty"`

	// IsolateDeps marks a dependency-isolating session: link_paths are not symlinked
	// into its worktree, so an `npm install` it runs cannot reach the origin checkout
	// (#481). It must persist because seeding re-runs on every worktree materialization
	// — a resume of a session stored without it would start linking again.
	//
	// A plain bool with omitempty, not a *bool: the default is OFF, so absent, false and
	// "written by a build predating the field" all mean the same thing, which is exactly
	// the pre-upgrade behavior. (Compare DiffStatsData.Unpushed, where they differ.)
	IsolateDeps bool `json:"isolate_deps,omitempty"`

	// CreateRequest is the `atrium new` spool record this session was built for, or
	// "" for one created from the form, a fork or smart auto-dispatch (#716).
	//
	// It is a provenance stamp with exactly one reader: the startup reconcile, which
	// uses it to tell "the session this claim asked for exists and is recorded" from
	// "a session of that title exists and is somebody else's". A (Title, Path) match
	// cannot make that distinction — it is the same match the drain's conflict gate
	// already ran and passed before the crash — and getting it wrong reports a
	// stranger's session to a caller's --wait as its own.
	//
	// Never cleared once the request settles. A record path carries nanoseconds plus
	// a random nonce, so a stale stamp cannot collide with a later claim, and clearing
	// it would buy a whole state.json rewrite per create for nothing.
	CreateRequest string `json:"create_request,omitempty"`

	Program string `json:"program"`

	// ClaudeAccount / ClaudeConfigDir / ClaudeAccountDefault pin the Claude Code
	// account resolved at creation: the display name, the CLAUDE_CONFIG_DIR
	// actually injected into the tmux session, and whether it is the
	// default/fallback account (dim badge). All omitempty: a state.json predating
	// the feature decodes to empty -> no badge, no injection.
	//
	// ClaudeConfigDir is the durable identity — it is what the session actually
	// runs under, and no config edit can change it after launch. The other two, and
	// ClaudeAccountPool below, are a CACHE of config re-derived from it at startup
	// and whenever the accounts panel commits, so renaming an account or moving it
	// into a pool adopts its existing sessions instead of stranding them under a
	// name config no longer has (#470).
	ClaudeAccount        string `json:"claude_account,omitempty"`
	ClaudeConfigDir      string `json:"claude_config_dir,omitempty"`
	ClaudeAccountDefault bool   `json:"claude_account_is_default,omitempty"`
	// ClaudeAccountPool is the rotation pool this session clusters under (the
	// badge still shows the concrete member). omitempty: a state.json predating
	// the feature decodes to empty -> singleton/none.
	ClaudeAccountPool string `json:"claude_account_pool,omitempty"`
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
	// AgyAccount and AgyConfigDir mirror the same route-forwarding mechanism for Antigravity CLI.
	AgyAccount   string `json:"agy_account,omitempty"`
	AgyConfigDir string `json:"agy_config_dir,omitempty"`

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

	// Effort is the reasoning-effort level claude's hooks last reported for a
	// resolved turn (e.g. "max"), persisted so the chip is right on the first frame
	// after a restart rather than blank until the session's next tool-using turn.
	// omitempty: a state.json predating the feature decodes to "" -> the UI falls
	// back to the Program's --effort flag, refreshed on the next poll.
	Effort string `json:"effort,omitempty"`

	// TmuxName is the session's tmux session name. It is persisted state, not a
	// derivation: new sessions mint a repo-qualified name (so identical titles
	// in different repo groups coexist on the shared socket). omitempty: a
	// state.json predating the field decodes to "" -> the instance falls back
	// to the legacy title-derived name its live session still has.
	TmuxName string `json:"tmux_name,omitempty"`

	// HookName is the session name the agent's status-hook artifacts are keyed by:
	// the tmux name as of the session's LAST launch, which a later deep rename does not
	// change. The running agent was exec'd with an absolute --state-file baked in, so its
	// write path outlives every rename; persisting the name is what lets a restarted TUI
	// keep reading it, since reattach restores the pane without re-running the bake (#492).
	// Usually equal to TmuxName — they diverge only between a rename and the next relaunch.
	// omitempty: a state.json predating the field, or a session Atrium never launched,
	// decodes to "". Rehydration then pins the session's current name (a downgrade-safe
	// no-op when the two agree) rather than re-resolving it on every read, so a later
	// rename cannot move the read path off a pre-upgrade agent's directory.
	HookName string `json:"hook_name,omitempty"`

	// Port is the session's managed dev-server port (#389), or absent when its repo
	// declares no port_range. Persisted because a running session's server is bound to
	// this number: a restart that re-allocated would renumber the row while the server
	// stayed where it was, and would hand the same port to the next session created.
	// omitempty: 0 is not a port, so absence and "no port" are the same thing and a
	// state.json predating the field decodes to exactly that.
	Port int `json:"port,omitempty"`

	// RunStarted records that the user had this session's run_command running (#389).
	// Persisted rather than re-derived because the `_run` tmux session outlives Atrium:
	// it is what tells a restarted TUI to probe for the server at all — a session that
	// never started one never pays for a has-session — and what tells Resume that the
	// pause it is undoing had stopped a server it should bring back.
	//
	// A plain bool, not a pointer: false and absent mean the same thing here, so
	// omitempty loses nothing and a state.json predating the field decodes to exactly
	// the right answer. (Compare DiffStatsData.Unpushed, where the two genuinely differ.)
	RunStarted bool `json:"run_command_started,omitempty"`

	// RunSession is the tmux name of the sibling session hosting that run command, as
	// this session MINTED it — not as it would be derived today.
	//
	// Persisting the name rather than re-deriving it is what survives a deep rename: the
	// rename moves the agent's tmux session and leaves the `_run` sibling where it is, so
	// a derived name stops resolving and the running dev server is orphaned — still bound
	// to a port Atrium then hands to the next session. An empty value also carries real
	// meaning, and the safety-critical half of it: this session has never hosted a run
	// command, so nothing sitting on the name it WOULD mint is ours to attach to or kill.
	RunSession string `json:"run_session,omitempty"`

	// TermSession is the tmux name of the sibling session hosting the terminal tab's
	// shell, as this session MINTED it — not as it would be derived today.
	//
	// Persisted for the half of RunSession's reason that applies to a shell: a deep rename
	// moves the agent's tmux session and leaves the `_term` sibling where it is, so a
	// derived name stops resolving and the user's shell is orphaned — still running
	// whatever they left in it, and reachable by no reap (#708). Nothing sweeps shells at
	// exit; they are MEANT to outlive Atrium and be adopted by the next run
	// (ui/terminal.go's EnsureSession), which is exactly why the name has to survive the
	// process that minted it. Without this field a restart after a rename would mint a
	// second shell beside the first and strand it for good.
	//
	// Unlike RunSession, an empty value here is not safety-critical: the pane adopts a
	// live `<name>_term` it did not start by design. omitempty, and a state.json predating
	// the field decodes to "" — which is right, since a pre-upgrade shell is on the name
	// the mint would produce anyway.
	TermSession string `json:"term_session,omitempty"`

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
	// Unpushed is a pointer so that "absent" and "0" stay distinguishable, which the
	// rest of these fields have no need for. A state.json written before this field
	// existed omits it, and the poll never recomputes stats for a paused session — so
	// decoding the gap as a literal 0 would tell a paused-with-WIP session that
	// nothing is at risk and let a kill discard it. nil means unknown, and
	// FromInstanceData resolves unknown conservatively. omitempty drops only nil,
	// never 0, so every save written from here on carries the key.
	Unpushed *int `json:"unpushed,omitempty"`
	Dirty    bool `json:"dirty"`
	// BranchStatsMeasured is a pointer for Unpushed's reason, and it guards a worse
	// gap than Unpushed does. It separates "measured, and nothing is at risk" from
	// "the git commands never ran", which is the one thing a zero DiffStats cannot say
	// for itself — git.RepoStats swallows a subprocess failure into a zero value. The
	// retire gate refuses a session it cannot establish, so leaving this out of the
	// serialized shape made every rehydrated instance read as unmeasured: a kill an
	// agent spooled while no TUI was up was refused by the next TUI's first tick, with
	// a receipt saying its tree state could not be established.
	//
	// A state.json predating this field omits it, and nil resolves to false — not
	// measured — which is the conservative direction: it withholds a teardown rather
	// than clearing one on numbers nobody took. Active sessions self-correct on the
	// next poll, and a paused one is refused by retire.Admits before any of this is
	// read. omitempty drops only nil, never false, so every save written from here on
	// carries the key.
	BranchStatsMeasured *bool `json:"branch_stats_measured,omitempty"`
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

// bringInstancesOnline is the seam LoadInstances brings its rehydrated instances
// online through. A package var — matching the package's own test-seam idiom
// (repoGroupKey, execSetup) — so a test can pin what the loader is itself
// responsible for: handing over the whole fleet, in stored order, under the cap it
// resolved from config, and returning the report unchanged. Production always uses
// bringOnline, whose rationing is tested directly against injected tmux deps.
var bringInstancesOnline = bringOnline

// LoadInstances loads the list of instances from disk and brings each one online.
// ctx is the lifecycle context reconstructed instances derive their subprocess
// contexts from.
//
// Bringing a session online is not always free: one whose tmux session survived is
// reattached and adds no load, but one whose session is gone is *relaunched* (see
// recovery.go). So the load is rationed by the host session budget, and returns the
// sessions it left parked for want of it — the report a caller may surface, and the
// zero value when nothing was refused. Survivors are counted before any relaunch is
// granted; see recoveryBudget for why that ordering is load-bearing.
func (s *Storage) LoadInstances(ctx context.Context) ([]*Instance, DeferredRecovery, error) {
	instancesData, err := s.loadInstanceData()
	if err != nil {
		return nil, DeferredRecovery{}, err
	}

	// Load config once for the whole batch: FromInstanceData needs BranchPrefix, and
	// the recovery budget needs the session cap.
	cfg := config.LoadConfig()
	instances := make([]*Instance, len(instancesData))
	for i, data := range instancesData {
		instance, err := FromInstanceData(ctx, data, cfg.BranchPrefix)
		if err != nil {
			return nil, DeferredRecovery{}, fmt.Errorf("failed to create instance %s: %w", data.Title, err)
		}
		instances[i] = instance
	}

	// FromInstanceData is now pure rehydration; bringing the fleet online (reattach,
	// or recover in place where the budget allows) is the separate IO step. Safe to
	// call here: no instance is published until the returned slice reaches the poll
	// loop, satisfying reattach's pre-publication precondition. Done for the whole
	// fleet at once rather than per instance above, because the budget has to count
	// every surviving session before it grants the first relaunch.
	return instances, bringInstancesOnline(instances, cfg.SessionCap()), nil
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
