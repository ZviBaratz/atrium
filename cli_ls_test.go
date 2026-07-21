package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lsJSON(t *testing.T) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, runLs(&buf, true))

	var got []map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got), "output was: %s", buf.String())
	return got
}

// TestLsJSONEmptyIsArrayNotNull is acceptance criterion 1: no sessions exits 0
// with "[]". A nil slice marshals to "null", which breaks every consumer that
// iterates the result, so the empty case is pinned on the bytes.
func TestLsJSONEmptyIsArrayNotNull(t *testing.T) {
	sandboxDataDir(t)

	var buf bytes.Buffer
	require.NoError(t, runLs(&buf, true))
	assert.Equal(t, "[]", string(bytes.TrimSpace(buf.Bytes())))
}

// TestLsJSONEmitsStatusNames guards the boundary that makes this a public
// contract: session.Status is an int on disk with no MarshalJSON, so re-emitting
// the stored struct would publish "3" where a script expects "paused".
func TestLsJSONEmitsStatusNames(t *testing.T) {
	sandboxDataDir(t)
	for _, tc := range []struct {
		status session.Status
		want   string
	}{
		{session.Running, "running"},
		{session.Ready, "ready"},
		{session.Loading, "loading"},
		{session.Paused, "paused"},
		{session.NeedsInput, "needs-input"},
		{session.Pending, "pending"},
	} {
		d := inst("s", "/repo/web")
		d.Status = tc.status
		seedInstances(t, d)

		got := lsJSON(t)
		require.Len(t, got, 1)
		assert.Equal(t, tc.want, got[0]["status"])
	}
}

// TestLsJSONOmitsInternalFields keeps the stored struct's internals out of the
// published contract: pane geometry is meaningless to a script, and the per-
// session config directories are none of its business.
func TestLsJSONOmitsInternalFields(t *testing.T) {
	sandboxDataDir(t)
	d := inst("s", "/repo/web")
	d.Height, d.Width = 40, 120
	d.ClaudeConfigDir = "/home/u/.claude-work"
	d.GHConfigDir = "/home/u/.config/gh-work"
	d.GitHubTokenEnv = []string{"GH_TOKEN=secret"}
	seedInstances(t, d)

	got := lsJSON(t)
	require.Len(t, got, 1)
	for _, key := range []string{"height", "width", "claude_config_dir", "gh_config_dir", "github_token_env", "prompt", "prompt_queue"} {
		assert.NotContains(t, got[0], key, "internal field %q must not be published", key)
	}
}

// TestLsJSONFields covers the documented payload end to end.
func TestLsJSONFields(t *testing.T) {
	sandboxDataDir(t)
	created := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 7, 20, 9, 30, 0, 0, time.UTC)
	unpushed := 2

	d := session.InstanceData{
		Title:       "fix-auth",
		DisplayName: "the auth fix",
		Note:        "waiting on review",
		Path:        "/repo/web",
		Branch:      "zvi/fix-auth",
		Status:      session.Running,
		Program:     "claude",
		TmuxName:    "atrium_web_fix-auth",
		Model:       "opus",
		Effort:      "high",
		AutoYes:     true,
		Unread:      true,
		CreatedAt:   created,
		UpdatedAt:   updated,
		PromptQueue: []session.QueuedPromptData{{Text: "one"}, {Text: "two"}},
		Worktree:    session.GitWorktreeData{WorktreePath: "/data/worktrees/fix-auth"},
		DiffStats: session.DiffStatsData{
			Added: 12, Removed: 3, FilesChanged: 2, Commits: 1, Behind: 4, Unpushed: &unpushed, Dirty: true,
		},
	}
	seedInstances(t, d)

	got := lsJSON(t)
	require.Len(t, got, 1)
	s := got[0]

	assert.Equal(t, "fix-auth", s["title"])
	assert.Equal(t, "the auth fix", s["display_name"])
	assert.Equal(t, "waiting on review", s["note"])
	assert.Equal(t, "/repo/web", s["path"])
	assert.Equal(t, "/data/worktrees/fix-auth", s["worktree"])
	assert.Equal(t, "zvi/fix-auth", s["branch"])
	assert.Equal(t, "running", s["status"])
	assert.Equal(t, "claude", s["program"])
	assert.Equal(t, "atrium_web_fix-auth", s["tmux_name"])
	assert.Equal(t, "opus", s["model"])
	assert.Equal(t, "high", s["effort"])
	assert.Equal(t, true, s["auto_yes"])
	assert.Equal(t, true, s["unread"])
	assert.Equal(t, float64(2), s["queued_prompts"])
	assert.Equal(t, created.Format(time.RFC3339), s["created_at"])
	assert.Equal(t, updated.Format(time.RFC3339), s["updated_at"])

	diff, ok := s["diff"].(map[string]any)
	require.True(t, ok, "diff should be a nested object")
	assert.Equal(t, float64(12), diff["added"])
	assert.Equal(t, float64(3), diff["removed"])
	assert.Equal(t, float64(2), diff["files_changed"])
	assert.Equal(t, float64(1), diff["commits"])
	assert.Equal(t, float64(4), diff["behind"])
	assert.Equal(t, float64(2), diff["unpushed"])
	assert.Equal(t, true, diff["dirty"])
}

// TestLsJSONDisplayNameFallsBackToTitle mirrors Instance.DisplayName, so a
// script rendering display_name never gets an empty label.
func TestLsJSONDisplayNameFallsBackToTitle(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("fix-auth", "/repo/web"))

	got := lsJSON(t)
	require.Len(t, got, 1)
	assert.Equal(t, "fix-auth", got[0]["display_name"])
}

// TestLsJSONUnpushedIsNullWhenUnknown preserves the distinction the stored *int
// exists to carry: "no unpushed commits" and "not computed yet" are different,
// and flattening the second to 0 is what made the kill dialog lie in #322.
func TestLsJSONUnpushedIsNullWhenUnknown(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("s", "/repo/web"))

	got := lsJSON(t)
	require.Len(t, got, 1)
	diff := got[0]["diff"].(map[string]any)
	require.Contains(t, diff, "unpushed", "the key must be present so consumers can tell null from absent")
	assert.Nil(t, diff["unpushed"])
}

// TestLsJSONZeroTimesAreNull: an instance stored before the timestamps existed
// should report an absent time, not the year 1.
func TestLsJSONZeroTimesAreNull(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("s", "/repo/web"))

	got := lsJSON(t)
	require.Len(t, got, 1)
	assert.Nil(t, got[0]["created_at"])
	assert.Nil(t, got[0]["updated_at"])
}

// TestLsJSONPreservesStoredOrder: the list order is the user's manual ordering,
// which a script fanning out over sessions should see unchanged.
func TestLsJSONPreservesStoredOrder(t *testing.T) {
	sandboxDataDir(t)
	seedInstances(t, inst("first", "/repo/a"), inst("second", "/repo/b"), inst("third", "/repo/c"))

	got := lsJSON(t)
	require.Len(t, got, 3)
	assert.Equal(t, "first", got[0]["title"])
	assert.Equal(t, "second", got[1]["title"])
	assert.Equal(t, "third", got[2]["title"])
}

// TestLsHumanTable covers the default (non---json) rendering.
func TestLsHumanTable(t *testing.T) {
	sandboxDataDir(t)
	d := inst("fix-auth", "/repo/web")
	d.Branch = "zvi/fix-auth"
	d.Status = session.Running
	d.DiffStats = session.DiffStatsData{Added: 12, Removed: 3}
	d.PromptQueue = []session.QueuedPromptData{{Text: "one"}}
	seedInstances(t, d)

	var buf bytes.Buffer
	require.NoError(t, runLs(&buf, false))
	out := buf.String()

	assert.Contains(t, out, "TITLE")
	assert.Contains(t, out, "fix-auth")
	assert.Contains(t, out, "running")
	assert.Contains(t, out, "zvi/fix-auth")
	assert.Contains(t, out, "+12/-3")
	assert.Contains(t, out, "web", "the repo column disambiguates same-titled sessions")
}

// TestLsHumanEmpty tells a human what happened instead of printing a bare header.
func TestLsHumanEmpty(t *testing.T) {
	sandboxDataDir(t)

	var buf bytes.Buffer
	require.NoError(t, runLs(&buf, false))
	assert.Contains(t, buf.String(), "No sessions")
}

// TestShortAgo pins the relative-time column's rounding.
func TestShortAgo(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		at   time.Time
		want string
	}{
		"zero time":   {time.Time{}, "-"},
		"seconds":     {now.Add(-30 * time.Second), "30s"},
		"minutes":     {now.Add(-5 * time.Minute), "5m"},
		"hours":       {now.Add(-3 * time.Hour), "3h"},
		"days":        {now.Add(-50 * time.Hour), "2d"},
		"future":      {now.Add(time.Hour), "0s"},
		"just now":    {now, "0s"},
		"minute cusp": {now.Add(-119 * time.Second), "1m"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, shortAgo(tc.at, now))
		})
	}
}
