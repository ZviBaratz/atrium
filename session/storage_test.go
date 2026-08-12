package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeleteInstanceDoesNotReconstructSiblings is the regression test for the
// zombie-session bug: a stored instance whose repo/worktree no longer exist on
// disk (e.g. after the user renamed their project directory) must not block
// deleting another session, and must not be silently corrupted in the process.
//
// DeleteInstance must operate on the serialized []InstanceData directly. The old
// implementation went through LoadInstances -> FromInstanceData, which reattaches
// to / restarts tmux and rewrites a dead session's Status (Running -> Paused) and
// UpdatedAt. This test pins that untouched siblings are preserved byte-for-byte.
func TestDeleteInstanceDoesNotReconstructSiblings(t *testing.T) {
	keeperUpdated := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	keeper := InstanceData{
		Title:     "keeper",
		Path:      "/nonexistent/repo",
		Branch:    "feature",
		Status:    Running, // 0 — would flip to Paused if reconstructed
		Program:   "claude",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: keeperUpdated,
		Worktree: GitWorktreeData{
			RepoPath:     "/nonexistent/repo",
			WorktreePath: "/nonexistent/worktree",
			SessionName:  "keeper",
			BranchName:   "feature",
		},
	}
	target := InstanceData{
		Title:   "target",
		Path:    "/nonexistent/repo2",
		Status:  Running,
		Program: "claude",
		Worktree: GitWorktreeData{
			RepoPath:     "/nonexistent/repo2",
			WorktreePath: "/nonexistent/worktree2",
			SessionName:  "target",
			BranchName:   "feature2",
		},
	}

	seeded, err := json.Marshal([]InstanceData{keeper, target})
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}

	state := config.DefaultState()
	state.InstancesData = seeded
	storage, err := NewStorage(state)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}

	if err := storage.DeleteInstance("target", "/nonexistent/repo2"); err != nil {
		t.Fatalf("DeleteInstance returned error: %v", err)
	}

	var got []InstanceData
	if err := json.Unmarshal(state.GetInstances(), &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly 1 remaining instance, got %d", len(got))
	}
	g := got[0]
	if g.Title != "keeper" {
		t.Fatalf("wrong instance kept: %q", g.Title)
	}
	if g.Status != Running {
		t.Errorf("keeper status corrupted: want Running(%d), got %d", Running, g.Status)
	}
	if !g.UpdatedAt.Equal(keeperUpdated) {
		t.Errorf("keeper UpdatedAt rewritten: want %s, got %s", keeperUpdated, g.UpdatedAt)
	}
	if g.Worktree.RepoPath != keeper.Worktree.RepoPath {
		t.Errorf("keeper repo_path changed: %q", g.Worktree.RepoPath)
	}
}

// TestDeleteInstanceNotFound documents that deleting a missing title is an error.
func TestDeleteInstanceNotFound(t *testing.T) {
	state := config.DefaultState() // InstancesData == "[]"
	storage, err := NewStorage(state)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	if err := storage.DeleteInstance("ghost", "/nowhere"); err == nil {
		t.Fatal("expected error deleting non-existent instance, got nil")
	}
}

// --- helpers for the tests below ---

// inMemoryStorage is a minimal in-memory config.InstanceStorage for unit tests.
type inMemoryStorage struct {
	data json.RawMessage
}

func (s *inMemoryStorage) SaveInstances(b json.RawMessage) error {
	s.data = append([]byte(nil), b...)
	return nil
}
func (s *inMemoryStorage) GetInstances() json.RawMessage {
	if s.data == nil {
		return []byte("[]")
	}
	return s.data
}
func (s *inMemoryStorage) DeleteAllInstances() error {
	s.data = []byte("[]")
	return nil
}

// newPausedInstance creates an Instance in Paused state without starting tmux
// or git — safe for storage-layer tests because FromInstanceData never opens a
// PTY for paused instances.
func newPausedInstance(t *testing.T, title string) *Instance {
	t.Helper()
	inst, err := NewInstance(InstanceOptions{Title: title, Path: ".", Program: "echo"})
	require.NoError(t, err)
	inst.status = Paused
	inst.started = true // mark started so ToInstanceData / SaveInstances includes it
	return inst
}

func newTestStorage(t *testing.T) *Storage {
	t.Helper()
	store, err := NewStorage(&inMemoryStorage{})
	require.NoError(t, err)
	return store
}

// TestStorageRoundTrip saves two paused instances and loads them back, asserting
// the in-memory store faithfully serialises and deserialises InstanceData.
func TestStorageRoundTrip(t *testing.T) {
	store := newTestStorage(t)

	a := newPausedInstance(t, "alpha")
	b := newPausedInstance(t, "beta")
	require.NoError(t, store.SaveInstances([]*Instance{a, b}))

	got, _, err := store.LoadInstances(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "alpha", got[0].Title)
	assert.Equal(t, "beta", got[1].Title)
	assert.Equal(t, Paused, got[0].status)
}

// TestStorageRoundTrip_Unread asserts the unread bit survives a save/load cycle
// (and that its absence deserializes as seen, the quiet default for old files).
func TestStorageRoundTrip_Unread(t *testing.T) {
	store := newTestStorage(t)

	a := newPausedInstance(t, "alpha")
	a.unread = true
	b := newPausedInstance(t, "beta")
	require.NoError(t, store.SaveInstances([]*Instance{a, b}))

	got, _, err := store.LoadInstances(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.True(t, got[0].Unread(), "a persisted unread bit must survive the round-trip")
	assert.False(t, got[1].Unread(), "an unflagged instance must load as seen")
}

// TestUpdateInstance_UpdatesField confirms that UpdateInstance persists a changed
// displayName and leaves other instances untouched.
func TestUpdateInstance_UpdatesField(t *testing.T) {
	store := newTestStorage(t)
	a := newPausedInstance(t, "alpha")
	b := newPausedInstance(t, "beta")
	require.NoError(t, store.SaveInstances([]*Instance{a, b}))

	a.SetDisplayName("Alpha New Label")
	require.NoError(t, store.UpdateInstance(a))

	got, _, err := store.LoadInstances(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 2)

	var updatedAlpha, unchangedBeta *Instance
	for _, inst := range got {
		if inst.Title == "alpha" {
			updatedAlpha = inst
		} else if inst.Title == "beta" {
			unchangedBeta = inst
		}
	}
	require.NotNil(t, updatedAlpha)
	require.NotNil(t, unchangedBeta)
	assert.Equal(t, "Alpha New Label", updatedAlpha.DisplayName())
	assert.Equal(t, "beta", unchangedBeta.Title)
}

// TestUpdateInstance_NotFoundReturnsError asserts that updating a non-existent
// instance returns an error rather than silently appending a new entry.
func TestUpdateInstance_NotFoundReturnsError(t *testing.T) {
	store := newTestStorage(t)
	require.NoError(t, store.SaveInstances([]*Instance{newPausedInstance(t, "alpha")}))

	ghost := newPausedInstance(t, "ghost")
	assert.ErrorContains(t, store.UpdateInstance(ghost), "not found")
}

// TestDeleteAllInstances_ClearsEverything confirms that DeleteAllInstances wipes
// all stored instances so a subsequent load returns an empty slice.
func TestDeleteAllInstances_ClearsEverything(t *testing.T) {
	store := newTestStorage(t)
	require.NoError(t, store.SaveInstances([]*Instance{newPausedInstance(t, "alpha"), newPausedInstance(t, "beta")}))

	require.NoError(t, store.DeleteAllInstances())

	got, _, err := store.LoadInstances(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestInstanceDataAccountRoundTrip(t *testing.T) {
	data := InstanceData{
		Title:                "t",
		Path:                 "/tmp/x",
		Program:              "claude",
		Direct:               true,
		ClaudeAccount:        "quantivly",
		ClaudeConfigDir:      "/home/tester/.claude-quantivly",
		ClaudeAccountDefault: false,
	}
	raw, err := json.Marshal(data)
	require.NoError(t, err)

	var back InstanceData
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, "quantivly", back.ClaudeAccount)
	require.Equal(t, "/home/tester/.claude-quantivly", back.ClaudeConfigDir)
	require.False(t, back.ClaudeAccountDefault)

	// Old state.json with no account keys -> empty fields (feature dormant).
	var legacy InstanceData
	require.NoError(t, json.Unmarshal([]byte(`{"title":"t","program":"claude","direct":true}`), &legacy))
	require.Equal(t, "", legacy.ClaudeAccount)
	require.Equal(t, "", legacy.ClaudeConfigDir)
}

func TestInstanceAccountGettersAndFromData(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "t", Path: ".", Program: "claude"})
	require.NoError(t, err)
	inst.SetClaudeAccount("quantivly", "/home/tester/.claude-quantivly", false)
	inst.SetGHConfigDir("/home/tester/.config/gh-quantivly")
	inst.SetGitHubTokenEnv([]string{"GITHUB_PERSONAL_ACCESS_TOKEN"})
	require.Equal(t, "quantivly", inst.ClaudeAccountName())
	require.Equal(t, "/home/tester/.claude-quantivly", inst.ClaudeConfigDir())
	require.Equal(t, "/home/tester/.config/gh-quantivly", inst.GHConfigDir())
	require.Equal(t, []string{"GITHUB_PERSONAL_ACCESS_TOKEN"}, inst.GitHubTokenEnv())
	require.False(t, inst.ClaudeAccountIsDefault())

	require.Equal(t, "quantivly", inst.ToInstanceData().ClaudeAccount)
	require.Equal(t, "/home/tester/.config/gh-quantivly", inst.ToInstanceData().GHConfigDir)
	// Only the token-env NAMES are persisted; the token value is never a field.
	require.Equal(t, []string{"GITHUB_PERSONAL_ACCESS_TOKEN"}, inst.ToInstanceData().GitHubTokenEnv)

	// FromInstanceData on a paused direct instance is hermetic (no live tmux:
	// the paused branch constructs a Session without shelling out).
	restored, err := FromInstanceData(context.Background(), InstanceData{
		Title:           "t",
		Path:            ".",
		Program:         "claude",
		Direct:          true,
		Status:          Paused,
		ClaudeAccount:   "quantivly",
		ClaudeConfigDir: "/home/tester/.claude-quantivly",
		GHConfigDir:     "/home/tester/.config/gh-quantivly",
		GitHubTokenEnv:  []string{"GITHUB_PERSONAL_ACCESS_TOKEN"},
	}, "session/")
	require.NoError(t, err)
	require.Equal(t, "quantivly", restored.ClaudeAccountName())
	require.Equal(t, "/home/tester/.claude-quantivly", restored.ClaudeConfigDir())
	require.Equal(t, "/home/tester/.config/gh-quantivly", restored.GHConfigDir())
	require.Equal(t, []string{"GITHUB_PERSONAL_ACCESS_TOKEN"}, restored.GitHubTokenEnv())

	// A state.json predating the feature (no github_token_env) decodes to nil.
	var legacy InstanceData
	require.NoError(t, json.Unmarshal([]byte(`{"title":"t","program":"claude","direct":true}`), &legacy))
	require.Nil(t, legacy.GitHubTokenEnv)
}

func TestInstanceClaudeAccountPoolRoundTrip(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "t", Path: ".", Program: "claude"})
	require.NoError(t, err)

	assert.Equal(t, "", inst.ClaudeAccountPool()) // dormant default

	inst.SetClaudeAccount("work-1", "/home/tester/.claude-work", false)
	inst.SetClaudeAccountPool("work")
	assert.Equal(t, "work", inst.ClaudeAccountPool())

	// Survives the InstanceData round-trip.
	data := inst.ToInstanceData()
	assert.Equal(t, "work", data.ClaudeAccountPool)

	restored, err := FromInstanceData(context.Background(),
		InstanceData{Title: "t", Path: ".", Branch: "b", Program: "claude", Direct: true,
			ClaudeAccount: "work-1", ClaudeAccountPool: "work"}, "session/")
	require.NoError(t, err)
	assert.Equal(t, "work", restored.ClaudeAccountPool())

	// Legacy data with no pool key decodes to empty (feature dormant).
	legacy, err := FromInstanceData(context.Background(),
		InstanceData{Title: "t", Path: ".", Branch: "b", Program: "claude", Direct: true}, "session/")
	require.NoError(t, err)
	assert.Equal(t, "", legacy.ClaudeAccountPool())
}

// TestPermissionModeRoundTrip asserts the live permission mode survives a
// save/restore (so a paused session keeps its chip) and that a pre-feature
// state.json — with no permission_mode key — restores to the flag fallback.
func TestPermissionModeRoundTrip(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "t", Path: ".", Program: "claude"})
	require.NoError(t, err)
	inst.SetModeMeta("auto")
	require.Equal(t, "auto", inst.ToInstanceData().PermissionMode)

	// Program has no --permission-mode flag, so PermissionModeInfo == the
	// restored runtimeMode: a clean read of what survived the round-trip.
	restored, err := FromInstanceData(context.Background(), InstanceData{
		Title: "t", Path: ".", Program: "claude", Direct: true, Status: Paused,
		PermissionMode: "auto",
	}, "session/")
	require.NoError(t, err)
	require.Equal(t, "auto", restored.PermissionModeInfo())

	// Old state.json (no key) -> empty -> falls back to the pinned flag.
	var legacy InstanceData
	require.NoError(t, json.Unmarshal([]byte(`{"title":"t","program":"claude --permission-mode plan","direct":true}`), &legacy))
	require.Equal(t, "", legacy.PermissionMode)
	pre, err := FromInstanceData(context.Background(), legacy, "session/")
	require.NoError(t, err)
	require.Equal(t, "plan", pre.PermissionModeInfo(), "pre-feature session falls back to the flag")
}

// TestEffortRoundTrip asserts the hook-reported effort survives a save/restore (so the chip
// is right on the first frame after a restart, rather than blank until the session's next
// tool-using turn) and that a pre-feature state.json — with no effort key — restores to the
// flag fallback.
func TestEffortRoundTrip(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "t", Path: ".", Program: "claude"})
	require.NoError(t, err)
	inst.SetEffortMeta("xhigh")
	require.Equal(t, "xhigh", inst.ToInstanceData().Effort)

	// Program has no --effort flag, so EffortInfo == the restored runtimeEffort: a clean
	// read of what survived the round-trip.
	restored, err := FromInstanceData(context.Background(), InstanceData{
		Title: "t", Path: ".", Program: "claude", Direct: true, Status: Paused,
		Effort: "xhigh",
	}, "session/")
	require.NoError(t, err)
	require.Equal(t, "xhigh", restored.EffortInfo())

	// Old state.json (no key) -> empty -> falls back to the pinned flag.
	var legacy InstanceData
	require.NoError(t, json.Unmarshal([]byte(`{"title":"t","program":"claude --effort low","direct":true}`), &legacy))
	require.Equal(t, "", legacy.Effort)
	pre, err := FromInstanceData(context.Background(), legacy, "session/")
	require.NoError(t, err)
	require.Equal(t, "low", pre.EffortInfo(), "pre-feature session falls back to the flag")
}

// TestPromptQueueRoundTrip pins that a multi-element queue is serialized and restored in
// order: the head re-arms its delivery clock at load time (the agent re-boots on resume),
// while the follow-up tail restores with strict idle-only (zero) clocks.
func TestPromptQueueRoundTrip(t *testing.T) {
	a := newPausedInstance(t, "queued")
	a.promptQueue = []queuedPrompt{
		{text: "first", queuedAt: time.Unix(1000, 0)},
		{text: "second", queuedAt: time.Unix(2000, 0)},
	}

	data := a.ToInstanceData()
	require.Len(t, data.PromptQueue, 2, "the whole queue must be persisted")
	require.Equal(t, "", data.Prompt, "the legacy single-prompt field is no longer written")

	restored, err := FromInstanceData(context.Background(), data, "session/")
	require.NoError(t, err)
	require.Equal(t, 2, restored.QueueLen())
	require.Equal(t, "first", restored.Prompt(), "the head restores first (FIFO order preserved)")
	require.False(t, restored.PromptQueuedAt().IsZero(), "the restored head re-arms its delivery clock")
	require.True(t, restored.PromptQueuedAt().After(time.Unix(1000, 0)),
		"the head clock restarts from reload, not the stale persisted time")

	restored.ClearPrompt("first")
	require.Equal(t, "second", restored.Prompt(), "the tail restores in order behind the head")
	require.True(t, restored.PromptQueuedAt().IsZero(),
		"a restored tail entry is a follow-up: strict idle-only (zero clock)")
}

// TestLegacyPromptFieldMigration pins that a pre-queue state.json (only the legacy `prompt`
// field) migrates into a one-element queue on load.
func TestLegacyPromptFieldMigration(t *testing.T) {
	var legacy InstanceData
	require.NoError(t, json.Unmarshal(
		[]byte(`{"title":"t","program":"echo","direct":true,"prompt":"finish it"}`), &legacy))
	require.Empty(t, legacy.PromptQueue, "a pre-queue file has no prompt_queue")
	require.Equal(t, "finish it", legacy.Prompt)

	restored, err := FromInstanceData(context.Background(), legacy, "session/")
	require.NoError(t, err)
	require.Equal(t, 1, restored.QueueLen(), "the legacy prompt migrates into a one-element queue")
	require.Equal(t, "finish it", restored.Prompt())
	require.False(t, restored.PromptQueuedAt().IsZero(), "the migrated head gets a live clock")
}

// TestPromptQueueWinsOverLegacyPrompt pins the strict precedence: when a transitional file
// carries BOTH prompt_queue and the legacy prompt, the queue is authoritative and the head
// is not duplicated.
func TestPromptQueueWinsOverLegacyPrompt(t *testing.T) {
	data := InstanceData{
		Title: "t", Program: "echo", Direct: true, Status: Paused,
		Prompt:      "legacy",
		PromptQueue: []QueuedPromptData{{Text: "queued"}},
	}
	restored, err := FromInstanceData(context.Background(), data, "session/")
	require.NoError(t, err)
	require.Equal(t, 1, restored.QueueLen(), "the legacy field must not be appended on top of the queue")
	require.Equal(t, "queued", restored.Prompt(), "prompt_queue is authoritative when both are present")
}

// TestLoadInstances_RationsRecoveryByTheConfiguredCap pins what the loader itself is
// responsible for, which is everything around bringOnline rather than the rationing
// inside it: it must hand over the WHOLE fleet, in the stored order the budget is spent
// in, under the cap resolved from config, and return the report untouched.
//
// bringOnline is stubbed here because the real one relaunches agents for sessions whose
// tmux server is gone — the production sessions FromInstanceData builds cannot be given
// a fake pty, so an unstubbed test at this level would be the one thing these tests may
// never do. The rationing itself is covered against injected deps in recovery_test.go.
func TestLoadInstances_RationsRecoveryByTheConfiguredCap(t *testing.T) {
	seed := func(t *testing.T) *Storage {
		t.Helper()
		store := newTestStorage(t)
		require.NoError(t, store.SaveInstances([]*Instance{
			newPausedInstance(t, "alpha"), newPausedInstance(t, "beta"), newPausedInstance(t, "gamma"),
		}))
		return store
	}

	t.Run("the whole fleet goes through the budget, in stored order", func(t *testing.T) {
		store := seed(t)
		var gotTitles []string
		var gotCap config.SessionCap
		calls := 0
		restore := stubBringOnline(t, func(insts []*Instance, sc config.SessionCap) DeferredRecovery {
			calls++
			gotCap = sc
			for _, inst := range insts {
				gotTitles = append(gotTitles, inst.Title)
			}
			return DeferredRecovery{Sessions: []ParkedSession{{Title: "gamma", Path: "/repo/web"}}, Limit: sc.Limit}
		})
		defer restore()

		got, deferred, err := store.LoadInstances(context.Background())
		require.NoError(t, err)

		require.Equal(t, 1, calls, "the fleet is rationed once, not per instance")
		require.Equal(t, []string{"alpha", "beta", "gamma"}, gotTitles,
			"every loaded session is offered, in the stored (user-arranged) order")
		require.Len(t, got, 3, "and every one is still returned to the caller")
		require.Equal(t, DeferredRecovery{Sessions: []ParkedSession{{Title: "gamma", Path: "/repo/web"}}, Limit: gotCap.Limit},
			deferred, "the report is passed through, not re-derived")
	})

	t.Run("with max_sessions unset the cap is the host-derived soft one", func(t *testing.T) {
		store := seed(t)
		var gotCap config.SessionCap
		restore := stubBringOnline(t, func(_ []*Instance, sc config.SessionCap) DeferredRecovery {
			gotCap = sc
			return DeferredRecovery{}
		})
		defer restore()

		_, _, err := store.LoadInstances(context.Background())
		require.NoError(t, err)
		require.Equal(t, config.SessionCap{Limit: config.DefaultSessionCap(), Soft: true}, gotCap,
			"an unset max_sessions must reach the budget as the soft host cap, which is the only shape that rations")
	})
}

// stubBringOnline swaps the loader's bring-online seam for the duration of a test,
// returning the restore. A helper rather than a t.Cleanup so a subtest cannot leave the
// stub installed for its siblings.
func stubBringOnline(t *testing.T, fn func([]*Instance, config.SessionCap) DeferredRecovery) func() {
	t.Helper()
	prev := bringInstancesOnline
	bringInstancesOnline = fn
	return func() { bringInstancesOnline = prev }
}

// The per-session link_paths opt-out (#481) is state-bearing: seeding re-runs on
// every worktree materialization, so a session that survives an app restart or a
// pause/resume must carry the choice with it.
//
// The last assertion is the one that matters, and it is not about JSON. Resume calls
// Setup — and therefore seedLocalPaths — on the Worktree that FromInstanceData
// rebuilt, NOT on the one Start built, so a flag restored onto the Instance but never
// pushed onto that Worktree would work exactly once and then silently start linking
// again.
func TestInstanceDataIsolateDepsRoundTrip(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{Title: "deps", Path: ".", Program: "claude", IsolateDeps: true})
	require.NoError(t, err)
	require.True(t, inst.IsolateDeps())

	data := inst.ToInstanceData()
	require.True(t, data.IsolateDeps)
	// Give the round-trip a worktree to restore onto; FromInstanceData only builds one
	// for a non-direct session.
	data.Worktree = GitWorktreeData{
		RepoPath:     "/tmp/repo",
		WorktreePath: "/tmp/wt",
		SessionName:  "deps",
		BranchName:   "zvi/deps",
	}

	raw, err := json.Marshal(data)
	require.NoError(t, err)
	var back InstanceData
	require.NoError(t, json.Unmarshal(raw, &back))
	require.True(t, back.IsolateDeps)

	restored, err := FromInstanceData(context.Background(), back, "zvi/")
	require.NoError(t, err)
	require.True(t, restored.IsolateDeps())
	wt := restored.worktree()
	require.NotNil(t, wt)
	require.True(t, wt.IsolateDeps(),
		"the restored worktree must be told: Resume seeds through THIS worktree, not the one Start built")
}

// The default is off, so a state.json written before the field existed must decode to
// a shared session — the pre-upgrade behavior — rather than silently isolating one.
func TestInstanceDataIsolateDepsDefaultsOffForLegacyState(t *testing.T) {
	var legacy InstanceData
	require.NoError(t, json.Unmarshal([]byte(`{"title":"t","program":"claude"}`), &legacy))
	require.False(t, legacy.IsolateDeps)

	// And an off session omits the key entirely, keeping old state files compact.
	raw, err := json.Marshal(InstanceData{Title: "t", Program: "claude"})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "isolate_deps")
}
