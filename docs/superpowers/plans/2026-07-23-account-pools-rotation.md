# Account Pools with Availability-Aware Rotation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a new session rotate across a named *pool* of Claude accounts, skipping any the user has flagged rate-limited, so parallel work no longer piles onto one maxed-out account.

**Architecture:** A new optional `pool` field on `ClaudeAccount` groups accounts. Routing stays pure in `config` (`ResolveClaudePool` returns the matched account's whole pool); rotation and availability — which need `State` — live in `app`. Selection becomes `route → pool → next available member (round-robin)`, with a manual pin still available in the create-form picker and a manual availability toggle in the `@` accounts view. Fully dormant when no `pool` is configured.

**Tech Stack:** Go 1.26 (module `github.com/ZviBaratz/atrium`), Bubble Tea TUI, Cobra CLI, `testify` for tests, `just` task runner.

**Source spec:** `docs/superpowers/specs/2026-07-23-account-pools-rotation-design.md` (Approved, v1 scope). This plan implements v1 only; v2 (auto-detect) and v3 (utilization-% proxy) are explicitly out of scope.

## Global Constraints

- **Backward compatibility is a contract.** Every new JSON field is `omitempty`. With no `pool` configured anywhere, behavior is byte-for-byte identical to today: every account is its own singleton pool, `accountKey` returns the account name, the picker shows accounts as today, rotation is a no-op. No task may break the "no pools ⇒ zero change" property.
- **Terminology:** the feature is a **pool** (never "group" — `group` already names repo groups and the `group_mode=account` list clustering). Members are **accounts**. The rotation state is a per-pool **cursor**.
- **Scope: Claude accounts only.** Do not touch `gh_accounts`, Antigravity, or the usage-meter work.
- **Availability is manual in v1.** A per-account `limited` flag lives in `state.json`, keyed by account **name**. No pane-scraping, no proxy.
- **Tests stay hermetic.** Anything that resolves the config dir, saves state, or touches config/state MUST sandbox `HOME`: `t.Setenv("HOME", t.TempDir())` for disk round-trips; the fixed `t.Setenv("HOME", "/home/tester")` string is fine for pure `~`-expansion tests (mirror the existing convention in each file).
- **The gate is `just ci`** (build + vet + fmt-check + lint + test + cover). `golangci-lint` is installed at `~/go/bin` but is **not on PATH**, so the gate must be run as **`PATH="$HOME/go/bin:$PATH" just ci`** or it dies at `lint` with exit 127. The inner test loop is `go test ./<pkg>/...` (`go` is on PATH via mise). Lint (`unused`, `revive`'s `exported` / `redefines-builtin-id`) is the part the other checks cannot substitute for; every exported symbol needs a doc comment; never name anything `max`/`min`/`len`.
- **Commits:** Conventional Commits, lowercase (`feat:`/`test:`/`docs:`). End every commit message with `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.

## File Structure

New files:
- `ui/overlay/accountSelection.go` — the `AccountSelection` handoff type (picker → spawner).
- `internal/doctor/pools.go` + `internal/doctor/pools_test.go` — the same-`config_dir`-in-pool doctor check.

Modified files (with the responsibility each gains):
- `config/types.go` — `ClaudeAccount.Pool` field.
- `config/accounts.go` — `ResolveClaudePool`, `PoolMembers`.
- `config/state.go` — `AccountAvailability` type; `State.AccountAvailability`/`AccountRotation`; `AppState` methods + self-persisting `*State` impls; exported `AccountAvailable` helper.
- `session/instance.go` — `claudeAccountPool` field + round-trip in `ToInstanceData`/`FromInstanceData`.
- `session/account.go` — `SetClaudeAccountPool` / `ClaudeAccountPool`.
- `session/storage.go` — `InstanceData.ClaudeAccountPool`.
- `session/filter.go` — `accountTerm` matches pool or member.
- `app/session_cap.go` — `spawnPlan.account` type change; `proceedExhaustedMsg`.
- `app/app_session.go` — availability-aware rotation in `startNewSession`; `confirmAllExhausted`; batch gate; `AccountSelection` wiring.
- `app/app.go` — `pendingExhausted` field.
- `app/app_update.go` — `proceedExhaustedMsg` handler; `NewAccountsOverlay` construction.
- `ui/list.go` — `accountKey()` returns pool-else-name.
- `ui/overlay/accountPicker.go`, `ui/overlay/textInput_create.go` — pool + member entries; `AccountSelection` result; pool preselect.
- `ui/overlay/accounts.go`, `ui/overlay/accountForm.go` — pool column; availability toggle (`l`); `StateManager`; `pool` edit field.
- `main.go` — wire the pools doctor check.
- `README.md` — pools / rotation / availability docs.

---

## Task 1: `pool` field on `ClaudeAccount`

**Files:**
- Modify: `config/types.go:85-90`
- Test: `config/config_test.go` (append; this is where `TestResolveClaudeAccount` and `TestConfig_AccountsRoundTrip` already live)

**Interfaces:**
- Consumes: nothing.
- Produces: `ClaudeAccount.Pool string` (json `pool,omitempty`) — read by Tasks 3, 5, 6, 8, 9.

- [ ] **Step 1: Write the failing test**

Append to `config/config_test.go`:

```go
func TestClaudeAccountPoolRoundTrip(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	// A pooled account and an ungrouped one marshal/unmarshal through JSON, and a
	// legacy account with no "pool" key decodes to the empty pool (feature dormant).
	in := Config{ClaudeAccounts: []ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "personal", ConfigDir: "~/.claude-personal"},
	}}
	data, err := json.Marshal(in)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"pool":"work"`)
	assert.NotContains(t, string(data), `"name":"personal","config_dir":"~/.claude-personal","pool"`) // omitempty: no empty pool key

	var out Config
	require.NoError(t, json.Unmarshal(data, &out))
	assert.Equal(t, "work", out.ClaudeAccounts[0].Pool)
	assert.Equal(t, "", out.ClaudeAccounts[1].Pool)

	var legacy Config
	require.NoError(t, json.Unmarshal([]byte(`{"claude_accounts":[{"name":"solo","config_dir":"~/.c"}]}`), &legacy))
	assert.Equal(t, "", legacy.ClaudeAccounts[0].Pool)
}
```

If `encoding/json` isn't already imported in `config_test.go`, add it.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./config/ -run TestClaudeAccountPoolRoundTrip -v`
Expected: FAIL — `out.ClaudeAccounts[0].Pool` undefined (`Pool` field does not exist).

- [ ] **Step 3: Add the field**

In `config/types.go`, inside `ClaudeAccount` (lines 85-90), add `Pool` after `PathMatches`:

```go
type ClaudeAccount struct {
	Name          string   `json:"name"`
	ConfigDir     string   `json:"config_dir"`
	RemoteMatches []string `json:"remote_matches,omitempty"`
	PathMatches   []string `json:"path_matches,omitempty"`
	Pool          string   `json:"pool,omitempty"` // rotation-pool membership; empty = singleton pool (own name)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./config/ -run TestClaudeAccountPoolRoundTrip -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add config/types.go config/config_test.go
git commit -m "$(cat <<'EOF'
feat(config): add optional pool field to ClaudeAccount

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Runtime state — availability flag + rotation cursor

**Files:**
- Modify: `config/state.go` (State struct ~174; `AppState` interface ~51-53; `*State` impls ~368-377)
- Test: `config/state_test.go` (append; mirror `TestState_AccountOrderRoundTrip` at 124-136)

**Interfaces:**
- Consumes: `SaveState`, `DefaultState`, `LoadState`, `maps` (already imported), `time` (already imported at state.go:10).
- Produces (read by Tasks 5, 8):
  - `type AccountAvailability struct { Limited bool; Until string }`
  - `AppState.GetAccountAvailability() map[string]AccountAvailability`
  - `AppState.SetAccountLimited(name, untilRFC3339 string) error`
  - `AppState.ClearAccountLimited(name string) error`
  - `AppState.GetAccountRotation(pool string) int`
  - `AppState.SetAccountRotation(pool string, idx int) error`
  - `func AccountAvailable(av AccountAvailability, now time.Time) bool`

- [ ] **Step 1: Write the failing tests**

Append to `config/state_test.go` (add `"time"` to its imports if absent):

```go
func TestState_AccountAvailabilityRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	assert.Empty(t, DefaultState().GetAccountAvailability())

	s := DefaultState()
	require.NoError(t, s.SetAccountLimited("work-1", "2026-07-23T16:32:00Z"))

	got := LoadState().GetAccountAvailability()
	assert.Equal(t, AccountAvailability{Limited: true, Until: "2026-07-23T16:32:00Z"}, got["work-1"])

	require.NoError(t, LoadState().ClearAccountLimited("work-1"))
	assert.Empty(t, LoadState().GetAccountAvailability())
}

func TestState_AccountRotationRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	assert.Zero(t, DefaultState().GetAccountRotation("work")) // unset pool reads 0

	s := DefaultState()
	require.NoError(t, s.SetAccountRotation("work", 2))
	assert.Equal(t, 2, LoadState().GetAccountRotation("work"))
}

func TestAccountAvailable(t *testing.T) {
	now := time.Date(2026, 7, 23, 16, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		av   AccountAvailability
		want bool
	}{
		{"not limited", AccountAvailability{}, true},
		{"indefinite", AccountAvailability{Limited: true}, false},
		{"future until", AccountAvailability{Limited: true, Until: "2026-07-23T17:00:00Z"}, false},
		{"past until", AccountAvailability{Limited: true, Until: "2026-07-23T15:00:00Z"}, true},
		{"malformed until", AccountAvailability{Limited: true, Until: "not-a-time"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, AccountAvailable(tc.av, now))
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./config/ -run 'TestState_AccountAvailabilityRoundTrip|TestState_AccountRotationRoundTrip|TestAccountAvailable' -v`
Expected: FAIL — `AccountAvailability`, the accessors, and `AccountAvailable` are undefined.

- [ ] **Step 3: Add the type and the `State` fields**

In `config/state.go`, add the type just above the `State` struct (before line 159):

```go
// AccountAvailability is the per-account runtime rate-limit flag. It lives in
// state.json (not config.json) because it auto-expires and must survive a
// restart — a real 5-hour limit does. Keyed by account name.
type AccountAvailability struct {
	Limited bool   `json:"limited"`
	Until   string `json:"until,omitempty"` // RFC3339; "" while Limited means indefinite
}
```

Inside `State` (add after `AccountOrder` at line 174):

```go
	// AccountAvailability marks Claude accounts the user flagged rate-limited,
	// keyed by account name. Pool rotation skips a limited member until its
	// window elapses. Empty = every account available (feature dormant).
	AccountAvailability map[string]AccountAvailability `json:"account_availability,omitempty"`
	// AccountRotation is the per-pool round-robin cursor (pool name -> next
	// member index), advanced at each session creation.
	AccountRotation map[string]int `json:"account_rotation,omitempty"`
```

- [ ] **Step 4: Add the interface methods**

In the `AppState` interface (after `SetAccountOrder` at line 53), add:

```go
	// GetAccountAvailability returns a copy of the per-account rate-limit flags.
	GetAccountAvailability() map[string]AccountAvailability
	// SetAccountLimited flags an account rate-limited until untilRFC3339 ("" = indefinite).
	SetAccountLimited(name, untilRFC3339 string) error
	// ClearAccountLimited removes an account's rate-limit flag.
	ClearAccountLimited(name string) error
	// GetAccountRotation returns the round-robin cursor for a pool (0 if unset).
	GetAccountRotation(pool string) int
	// SetAccountRotation stores the round-robin cursor for a pool.
	SetAccountRotation(pool string, idx int) error
```

- [ ] **Step 5: Add the `*State` implementations and the helper**

Add near the `AccountOrder` accessors (after line 377). Note the nil-guard on map writes mirrors `SetAckedDrift` (state.go:442-453); reads of a nil map are safe in Go:

```go
// GetAccountAvailability returns a copy of the rate-limit flags (empty when unset).
func (s *State) GetAccountAvailability() map[string]AccountAvailability {
	if len(s.AccountAvailability) == 0 {
		return map[string]AccountAvailability{}
	}
	return maps.Clone(s.AccountAvailability)
}

// SetAccountLimited flags name rate-limited until untilRFC3339 ("" = indefinite).
func (s *State) SetAccountLimited(name, untilRFC3339 string) error {
	if s.AccountAvailability == nil {
		s.AccountAvailability = map[string]AccountAvailability{}
	}
	s.AccountAvailability[name] = AccountAvailability{Limited: true, Until: untilRFC3339}
	return SaveState(s)
}

// ClearAccountLimited removes name's rate-limit flag.
func (s *State) ClearAccountLimited(name string) error {
	if s.AccountAvailability == nil {
		return nil
	}
	delete(s.AccountAvailability, name)
	return SaveState(s)
}

// GetAccountRotation returns the round-robin cursor for pool (0 when unset).
func (s *State) GetAccountRotation(pool string) int {
	return s.AccountRotation[pool] // nil-map read yields 0
}

// SetAccountRotation stores the round-robin cursor for pool.
func (s *State) SetAccountRotation(pool string, idx int) error {
	if s.AccountRotation == nil {
		s.AccountRotation = map[string]int{}
	}
	s.AccountRotation[pool] = idx
	return SaveState(s)
}

// AccountAvailable reports whether an account may take a new session now. A
// limited account with an elapsed reset window counts as available again; a
// malformed or empty Until while Limited counts as unavailable.
func AccountAvailable(av AccountAvailability, now time.Time) bool {
	if !av.Limited {
		return true
	}
	if av.Until == "" {
		return false // indefinite
	}
	t, err := time.Parse(time.RFC3339, av.Until)
	if err != nil {
		return false // malformed -> treat as still limited
	}
	return now.After(t)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./config/ -run 'TestState_AccountAvailabilityRoundTrip|TestState_AccountRotationRoundTrip|TestAccountAvailable' -v`
Expected: PASS. Also run the whole package (the `State` struct changed): `go test ./config/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add config/state.go config/state_test.go
git commit -m "$(cat <<'EOF'
feat(config): add account availability + pool rotation state

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Pure pool routing — `ResolveClaudePool` + `PoolMembers`

**Files:**
- Modify: `config/accounts.go` (after `ResolveClaudeAccount`, line 27)
- Test: `config/config_test.go` (append; mirror `TestResolveClaudeAccount` at 707)

**Interfaces:**
- Consumes: `matchRouteIndex` (accounts.go:86), `ClaudeAccount.ResolvedConfigDir`/`IsCatchAll`, `ClaudeAccount.Pool` (Task 1).
- Produces (read by Task 5):
  - `func (c *Config) ResolveClaudePool(remoteURL, targetPath string) (pool string, members []ClaudeAccount, isDefault bool)`
  - `func (c *Config) PoolMembers(pool string) []ClaudeAccount`
- Does **not** remove or change `ResolveClaudeAccount` (still used by the picker preview / other callers).

- [ ] **Step 1: Write the failing tests**

Append to `config/config_test.go`:

```go
func TestResolveClaudePool(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	work1 := ClaudeAccount{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work",
		RemoteMatches: []string{"quantivly"}, PathMatches: []string{"/quantivly/"}}
	work2 := ClaudeAccount{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"}
	personal := ClaudeAccount{Name: "personal", ConfigDir: "~/.claude-personal"} // no rules -> catch-all
	cfg := &Config{ClaudeAccounts: []ClaudeAccount{work1, work2, personal}}

	// A remote hit on work-1 returns the whole work pool, members in config order.
	pool, members, isDefault := cfg.ResolveClaudePool("github.com/quantivly/app", "/x")
	assert.Equal(t, "work", pool)
	assert.False(t, isDefault)
	require.Len(t, members, 2)
	assert.Equal(t, "work-1", members[0].Name)
	assert.Equal(t, "work-2", members[1].Name)

	// No route hit -> catch-all personal as a singleton pool named for the account.
	pool, members, isDefault = cfg.ResolveClaudePool("github.com/other/repo", "/y")
	assert.Equal(t, "personal", pool)
	assert.True(t, isDefault)
	require.Len(t, members, 1)
	assert.Equal(t, "personal", members[0].Name)

	// Empty config -> dormant.
	pool, members, isDefault = (&Config{}).ResolveClaudePool("x", "y")
	assert.Equal(t, "", pool)
	assert.Nil(t, members)
	assert.False(t, isDefault)
}

func TestPoolMembers(t *testing.T) {
	cfg := &Config{ClaudeAccounts: []ClaudeAccount{
		{Name: "work-1", Pool: "work"},
		{Name: "personal"},
		{Name: "work-2", Pool: "work"},
	}}
	work := cfg.PoolMembers("work")
	require.Len(t, work, 2)
	assert.Equal(t, "work-1", work[0].Name)
	assert.Equal(t, "work-2", work[1].Name)

	// An ungrouped account is addressable as a singleton pool by its own name.
	solo := cfg.PoolMembers("personal")
	require.Len(t, solo, 1)
	assert.Equal(t, "personal", solo[0].Name)

	assert.Empty(t, cfg.PoolMembers("nope"))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./config/ -run 'TestResolveClaudePool|TestPoolMembers' -v`
Expected: FAIL — `ResolveClaudePool` / `PoolMembers` undefined.

- [ ] **Step 3: Implement**

In `config/accounts.go`, after `ResolveClaudeAccount` (line 27), add:

```go
// ResolveClaudePool finds the account routing selects (first-match, identical to
// ResolveClaudeAccount) and returns the whole pool that account belongs to: the
// pool name and its members in config order. An account with no Pool is a
// singleton pool whose name is the account name. isDefault mirrors
// ResolveClaudeAccount (the matched account was the rule-less catch-all). Empty
// claude_accounts returns ("", nil, false) — the feature is dormant.
func (c *Config) ResolveClaudePool(remoteURL, targetPath string) (pool string, members []ClaudeAccount, isDefault bool) {
	if len(c.ClaudeAccounts) == 0 {
		return "", nil, false
	}
	idx, isDefault := matchRouteIndex(len(c.ClaudeAccounts), strings.ToLower(remoteURL), strings.ToLower(targetPath),
		func(i int) []string { return c.ClaudeAccounts[i].RemoteMatches },
		func(i int) []string { return c.ClaudeAccounts[i].PathMatches })
	if idx < 0 {
		// No match and no catch-all: mirror ResolveClaudeAccount's synthetic default.
		return "default", []ClaudeAccount{{Name: "default"}}, true
	}
	matched := c.ClaudeAccounts[idx]
	if matched.Pool == "" {
		return matched.Name, []ClaudeAccount{matched}, isDefault
	}
	return matched.Pool, c.PoolMembers(matched.Pool), isDefault
}

// PoolMembers returns the accounts in the named pool, in config order. A name
// matching a single ungrouped account (Pool == "") returns that account as a
// singleton pool, so any pool name — grouped or singleton — resolves uniformly.
func (c *Config) PoolMembers(pool string) []ClaudeAccount {
	var members []ClaudeAccount
	for _, a := range c.ClaudeAccounts {
		if a.Pool == pool || (a.Pool == "" && a.Name == pool) {
			members = append(members, a)
		}
	}
	return members
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./config/ -run 'TestResolveClaudePool|TestPoolMembers' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add config/accounts.go config/config_test.go
git commit -m "$(cat <<'EOF'
feat(config): resolve an account's whole rotation pool

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Instance pinning — carry the pool

**Files:**
- Modify: `session/instance.go` (field ~206-208; `ToInstanceData` copy ~366-368; `FromInstanceData` copy ~443-445)
- Modify: `session/account.go` (after `SetClaudeAccount`, line 15)
- Modify: `session/storage.go` (`InstanceData`, after line 60)
- Test: `session/storage_test.go` (append; mirror `TestInstanceAccountGettersAndFromData` at 256)

**Interfaces:**
- Consumes: nothing new.
- Produces (read by Tasks 5, 7):
  - `func (i *Instance) SetClaudeAccountPool(pool string)`
  - `func (i *Instance) ClaudeAccountPool() string`
  - `InstanceData.ClaudeAccountPool string` (json `claude_account_pool,omitempty`)
- Read without the lock (creation-fixed), exactly like `claudeAccount` (instance.go:204-205 doc).

- [ ] **Step 1: Write the failing test**

Append to `session/storage_test.go`:

```go
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
```

(`context` is already imported in `storage_test.go`; if not, add it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./session/ -run TestInstanceClaudeAccountPoolRoundTrip -v`
Expected: FAIL — `SetClaudeAccountPool` / `ClaudeAccountPool` / `InstanceData.ClaudeAccountPool` undefined.

- [ ] **Step 3: Add the Instance field**

In `session/instance.go`, add after `claudeAccountDefault` (line 208):

```go
	claudeAccountPool    string // rotation pool this session was pinned under (cluster key); "" = singleton/none
```

- [ ] **Step 4: Add the setter/getter**

In `session/account.go`, after `SetClaudeAccount` (line 15):

```go
// SetClaudeAccountPool pins the rotation pool this session belongs to (the list
// cluster key; the badge still shows the concrete member). Empty = singleton/none.
func (i *Instance) SetClaudeAccountPool(pool string) { i.claudeAccountPool = pool }
```

Next to the other getters (e.g. after `ClaudeAccountIsDefault` at line 49):

```go
// ClaudeAccountPool returns the pinned rotation pool ("" when none).
func (i *Instance) ClaudeAccountPool() string { return i.claudeAccountPool }
```

- [ ] **Step 5: Add the `InstanceData` field and both round-trip copies**

In `session/storage.go`, after `ClaudeAccountDefault` (line 60):

```go
	ClaudeAccountPool string `json:"claude_account_pool,omitempty"`
```

In `session/instance.go`, in `ToInstanceData` after line 368 (`ClaudeAccountDefault: i.claudeAccountDefault,`):

```go
		ClaudeAccountPool:    i.claudeAccountPool,
```

In `session/instance.go`, in `FromInstanceData` after line 445 (`claudeAccountDefault: data.ClaudeAccountDefault,`):

```go
		claudeAccountPool:    data.ClaudeAccountPool,
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./session/ -run TestInstanceClaudeAccountPoolRoundTrip -v`
Expected: PASS. Then the package: `go test ./session/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add session/instance.go session/account.go session/storage.go session/storage_test.go
git commit -m "$(cat <<'EOF'
feat(session): pin the rotation pool on an instance

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Availability-aware rotation in the create flow

This is the create-flow core: the `AccountSelection` handoff type, the rotation decision, the all-exhausted confirm, and the `spawnPlan`/`startNewSession` signature change. The picker (Task 6) still returns `(config.ClaudeAccount, bool)` at the *picker* level; this task adapts it at the `TextInputOverlay` wrapper so the whole tree keeps building green.

**Files:**
- Create: `ui/overlay/accountSelection.go`
- Modify: `ui/overlay/textInput_create.go` (`GetSelectedAccount`, 356-361)
- Modify: `app/session_cap.go` (`spawnPlan.account` 89; add `proceedExhaustedMsg`)
- Modify: `app/app.go` (add `pendingExhausted`)
- Modify: `app/app_session.go` (`startNewSession` 1245/1257-1272; `createSessionFromForm` 1207-1237; add `confirmAllExhausted`, `gateAllExhausted`, `resolveSpawnPool`, `soonestResetMember`)
- Modify: `app/app_update.go` (`proceedExhaustedMsg` handler near 132-140)
- Test: `app/rotation_test.go` (new)

**Interfaces:**
- Consumes: `config.ResolveClaudePool`, `config.PoolMembers`, `config.AccountAvailable`, `AppState.GetAccountAvailability`/`GetAccountRotation`/`SetAccountRotation` (Tasks 2-3); `Instance.SetClaudeAccountPool` (Task 4); `confirmAction` (app_session.go:1595); the `proceedOverCapMsg` pattern (session_cap.go:94, app_update.go:132, app.go:418).
- Produces (consumed by Task 6):
  - `type AccountSelection struct { Pool string; Member *config.ClaudeAccount }` (package `overlay`)
  - `func (t *TextInputOverlay) GetSelectedAccount() *AccountSelection` (signature CHANGED from `(config.ClaudeAccount, bool)`)
  - `spawnPlan.account *overlay.AccountSelection`
  - `startNewSession(..., sel *overlay.AccountSelection, fromBatch bool)`

- [ ] **Step 1: Write the failing tests**

Create `app/rotation_test.go`:

```go
package app

import (
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// poolHome builds a create-form home routed to a two-member "work" pool where
// work-1 is the catch-all (no rules), so ResolveClaudePool always lands on it.
func poolHome(t *testing.T) *home {
	t.Helper()
	h := newCreateFormHome(t)
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}
	return h
}

func startDirect(t *testing.T, h *home, sel *overlay.AccountSelection) *session.Instance {
	t.Helper()
	before := h.list.NumInstances()
	_, err := h.startNewSession("s", t.TempDir(), true, "echo", "", "", sel, false)
	require.NoError(t, err)
	require.Equal(t, before+1, h.list.NumInstances())
	return h.list.GetInstances()[h.list.NumInstances()-1]
}

func TestStartNewSession_RotatesAndAdvances(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := poolHome(t)

	first := startDirect(t, h, nil)
	assert.Equal(t, "work-1", first.ClaudeAccountName())
	assert.Equal(t, "work", first.ClaudeAccountPool())

	second := startDirect(t, h, nil)
	assert.Equal(t, "work-2", second.ClaudeAccountName(), "cursor advanced to the sibling")
	assert.Equal(t, "work", second.ClaudeAccountPool())
}

func TestStartNewSession_SkipsLimited(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := poolHome(t)
	require.NoError(t, h.appState.SetAccountLimited("work-1", "")) // indefinite

	inst := startDirect(t, h, nil)
	assert.Equal(t, "work-2", inst.ClaudeAccountName(), "limited work-1 skipped")
}

func TestStartNewSession_PinnedBypassesAvailability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := poolHome(t)
	require.NoError(t, h.appState.SetAccountLimited("work-1", "")) // even limited

	pin := &overlay.AccountSelection{Pool: "work", Member: &config.ClaudeAccount{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"}}
	inst := startDirect(t, h, pin)
	assert.Equal(t, "work-1", inst.ClaudeAccountName(), "a deliberate pin runs even on a limited account")
	assert.Equal(t, "work", inst.ClaudeAccountPool())
}

func TestSoonestResetMember(t *testing.T) {
	now := time.Date(2026, 7, 23, 16, 0, 0, 0, time.UTC)
	members := []config.ClaudeAccount{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	avail := map[string]config.AccountAvailability{
		"a": {Limited: true, Until: "2026-07-23T18:00:00Z"},
		"b": {Limited: true, Until: "2026-07-23T17:00:00Z"},
		"c": {Limited: true}, // indefinite sorts last
	}
	assert.Equal(t, 1, soonestResetMember(members, avail, now), "b resets soonest")

	allIndef := map[string]config.AccountAvailability{"a": {Limited: true}, "b": {Limited: true}, "c": {Limited: true}}
	assert.Equal(t, 0, soonestResetMember(members, avail_or(allIndef), now), "all indefinite -> fallback 0")
}

func avail_or(m map[string]config.AccountAvailability) map[string]config.AccountAvailability { return m }
```

Add a create-flow test to the same file, driving the form the way `host_cap_test.go` does (via `typeString`/`ctrlS`):

```go
func TestCreateForm_AllExhaustedAsksConfirm(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := newFanOutHome(t, gitInitRepo(t))
	h.appConfig.ClaudeAccounts = []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}
	require.NoError(t, h.appState.SetAccountLimited("work-1", ""))
	require.NoError(t, h.appState.SetAccountLimited("work-2", ""))
	before := h.list.NumInstances()

	typeString(h, "doomed")
	ctrlS(h)

	assert.Equal(t, stateConfirm, h.state, "a fully-limited pool asks before spawning")
	require.NotNil(t, h.pendingExhausted, "the plan is staged behind the confirm")
	assert.Equal(t, before, h.list.NumInstances(), "nothing spawned yet")
	assert.Nil(t, h.textInputOverlay, "form dismissed (stashed as restorable draft)")
}
```

Add `"github.com/ZviBaratz/atrium/session"` to the imports if the helper needs it.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./app/ -run 'TestStartNewSession_|TestSoonestResetMember|TestCreateForm_AllExhausted' -v`
Expected: FAIL to compile — `overlay.AccountSelection`, `soonestResetMember`, `h.pendingExhausted`, and the new `startNewSession` signature don't exist yet.

- [ ] **Step 3: Create the `AccountSelection` type**

Create `ui/overlay/accountSelection.go`:

```go
package overlay

import "github.com/ZviBaratz/atrium/config"

// AccountSelection is the create-form's account choice handed to the session
// spawner. A non-nil Member pins that exact account, bypassing rotation and
// availability (a deliberate pin is the escape hatch even for a limited
// account). Otherwise Pool names the pool to rotate within. Pool is also the
// list cluster key and is set even for a member pin, so a pinned session still
// groups under its pool.
type AccountSelection struct {
	Pool   string
	Member *config.ClaudeAccount
}
```

- [ ] **Step 4: Adapt the overlay-level `GetSelectedAccount`**

Replace `ui/overlay/textInput_create.go:356-361` with (the picker's own `GetSelectedAccount` is untouched; Task 6 enriches it):

```go
// GetSelectedAccount returns the deliberate account choice, or nil when the user
// never touched the picker (an untouched preselection must not override routing).
func (t *TextInputOverlay) GetSelectedAccount() *AccountSelection {
	if t.accountPicker == nil || !t.accountPicker.Touched() {
		return nil
	}
	acct := t.accountPicker.GetSelectedAccount()
	if acct.Name == "" {
		return nil
	}
	// Pool is carried so the session clusters correctly; Member set = a pin.
	return &AccountSelection{Pool: acct.Pool, Member: &acct}
}
```

- [ ] **Step 5: Change `spawnPlan.account` and add `proceedExhaustedMsg`**

In `app/session_cap.go`, change line 89:

```go
	account  *overlay.AccountSelection
```

Add the message type next to `proceedOverCapMsg` (line 94):

```go
// proceedExhaustedMsg is emitted when the user accepts creating a session even
// though every member of the routed pool is rate-limited (see confirmAllExhausted).
type proceedExhaustedMsg struct{}
```

Ensure `session_cap.go` imports `"github.com/ZviBaratz/atrium/ui/overlay"` (the `app` package already imports it elsewhere; add to this file's import block if missing).

- [ ] **Step 6: Add the `pendingExhausted` home field**

In `app/app.go`, after `pendingOverCap` (line 418):

```go
	// pendingExhausted holds a creation staged behind an all-members-rate-limited
	// confirmation (see confirmAllExhausted). Its account is already pinned to the
	// soonest-to-reset member. Nil when no such confirm is pending.
	pendingExhausted *spawnPlan
```

- [ ] **Step 7: Rewrite the account block in `startNewSession`**

Change the signature (app_session.go:1245): rename the param `accountOverride *config.ClaudeAccount` to `sel *overlay.AccountSelection`. Replace the resolution+pin block (lines 1257-1272) with:

```go
	// Resolve the pool this worktree routes to, then pick the account: a picker
	// member-pin wins outright; otherwise rotate to the next available member and
	// advance the per-pool cursor (so an unpinned fan-out batch spreads across the
	// pool). Empty claude_accounts leaves all fields empty (feature dormant).
	remoteURL := ""
	if !direct {
		remoteURL = git.GetRemoteURL(m.ctx, path)
	}
	poolName, members, _ := m.appConfig.ResolveClaudePool(remoteURL, path)
	if sel != nil && sel.Pool != "" {
		poolName, members = sel.Pool, m.appConfig.PoolMembers(sel.Pool)
	}

	var accName, accDir string
	var accIsDefault bool
	switch {
	case sel != nil && sel.Member != nil:
		accName, accDir, accIsDefault = sel.Member.Name, sel.Member.ResolvedConfigDir(), sel.Member.IsCatchAll()
		if poolName == "" {
			poolName = sel.Member.Name // an ungrouped pin clusters under its own name
		}
	case len(members) == 0:
		// dormant: leave everything empty
	default:
		avail := m.appState.GetAccountAvailability()
		now := time.Now()
		start := ((m.appState.GetAccountRotation(poolName) % len(members)) + len(members)) % len(members)
		chosen := start // defensive fallback: the batch gate should have caught all-exhausted
		for k := 0; k < len(members); k++ {
			idx := (start + k) % len(members)
			if config.AccountAvailable(avail[members[idx].Name], now) {
				chosen = idx
				break
			}
		}
		if err := m.appState.SetAccountRotation(poolName, chosen+1); err != nil {
			log.WarningLog.Printf("failed to persist rotation cursor: %v", err)
		}
		accName, accDir, accIsDefault = members[chosen].Name, members[chosen].ResolvedConfigDir(), members[chosen].IsCatchAll()
	}
	instance.SetClaudeAccount(accName, accDir, accIsDefault)
	instance.SetClaudeAccountPool(poolName)
```

Confirm `time` and `log` are already imported in `app_session.go` (they are — `log` via `github.com/ZviBaratz/atrium/log`, used elsewhere in the file; add `time` if the file doesn't already import it).

- [ ] **Step 8: Add rotation helpers + the batch gate + the confirm**

Add to `app/app_session.go` (near `confirmOverCap`/`confirmAction`, e.g. after line 1604):

```go
// soonestResetMember returns the index of the member whose limit resets soonest.
// Members with a parseable Until sort by that time; indefinite or unparseable
// sort last; all-indefinite returns 0 (the cursor's natural start).
func soonestResetMember(members []config.ClaudeAccount, avail map[string]config.AccountAvailability, now time.Time) int {
	best, bestSet := 0, false
	var bestT time.Time
	for i, mem := range members {
		av := avail[mem.Name]
		if av.Until == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, av.Until)
		if err != nil {
			continue
		}
		if !bestSet || t.Before(bestT) {
			best, bestT, bestSet = i, t, true
		}
	}
	return best
}

// resolveSpawnPool returns the pool a plan rotates within: the picker's chosen
// pool if set, else the route-resolved pool.
func (m *home) resolveSpawnPool(plan spawnPlan) (string, []config.ClaudeAccount) {
	if plan.account != nil && plan.account.Pool != "" {
		return plan.account.Pool, m.appConfig.PoolMembers(plan.account.Pool)
	}
	remoteURL := ""
	if !plan.direct {
		remoteURL = git.GetRemoteURL(m.ctx, plan.path)
	}
	poolName, members, _ := m.appConfig.ResolveClaudePool(remoteURL, plan.path)
	return poolName, members
}

// gateAllExhausted returns (cmd, true) and stages a confirm when an unpinned
// multi-member pool has no currently-available member; (nil, false) to proceed.
// Evaluated once per batch, mirroring the soft-cap gate.
func (m *home) gateAllExhausted(plan spawnPlan) (tea.Cmd, bool) {
	if plan.account != nil && plan.account.Member != nil {
		return nil, false // a deliberate member pin bypasses availability
	}
	poolName, members := m.resolveSpawnPool(plan)
	if len(members) < 2 {
		return nil, false // singleton/empty pool: nothing to skip
	}
	avail := m.appState.GetAccountAvailability()
	now := time.Now()
	for _, mem := range members {
		if config.AccountAvailable(avail[mem.Name], now) {
			return nil, false
		}
	}
	return m.confirmAllExhausted(plan, poolName, members), true
}

// confirmAllExhausted stages a confirm for a fully-rate-limited pool. On accept
// it pins the soonest-to-reset member for the whole batch and spawns; on decline
// nothing is created (the dismissed form is stashed as a restorable draft, the
// same rollback contract as confirmOverCap).
func (m *home) confirmAllExhausted(plan spawnPlan, pool string, members []config.ClaudeAccount) tea.Cmd {
	avail := m.appState.GetAccountAvailability()
	soonest := soonestResetMember(members, avail, time.Now())
	pinned := members[soonest]
	plan.account = &overlay.AccountSelection{Pool: pool, Member: &pinned}
	m.pendingExhausted = &plan
	m.stashDirtyCreateForm()
	m.textInputOverlay = nil
	m.menu.SetState(ui.StateDefault)
	m.resetTitleCheck()
	return m.confirmAction(
		allExhaustedMessage(pool, members, avail, pinned.Name),
		func() tea.Msg { return proceedExhaustedMsg{} })
}

// allExhaustedMessage renders the confirm body: the pool, each member's reset
// time (or "indefinitely"), and which member the batch will use.
func allExhaustedMessage(pool string, members []config.ClaudeAccount, avail map[string]config.AccountAvailability, pinned string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⚠ all %s accounts are rate-limited\n", pool)
	for _, mem := range members {
		when := "indefinitely"
		if u := avail[mem.Name].Until; u != "" {
			if t, err := time.Parse(time.RFC3339, u); err == nil {
				when = "resets " + t.Local().Format("15:04")
			}
		}
		fmt.Fprintf(&b, "  %s %s\n", mem.Name, when)
	}
	fmt.Fprintf(&b, "create anyway on %s?", pinned)
	return b.String()
}
```

Confirm `fmt` and `strings` are imported in `app_session.go` (add if missing).

- [ ] **Step 9: Insert the gate and rewire the plan in `createSessionFromForm`**

Change the account-override derivation (app_session.go:1207-1210) to:

```go
	sel := ov.GetSelectedAccount() // *overlay.AccountSelection, nil when untouched
```

Change the plan build (app_session.go:1218-1221) to use `sel`:

```go
	plan := spawnPlan{
		titles: titles, path: path, direct: direct, programs: programs,
		branch: branch, prompt: prompt, account: sel,
	}
```

Immediately before the existing soft-cap check (the `m.confirmOverCap`/`m.spawnVariants` decision at 1222-1237), insert the availability gate:

```go
	// All-members-rate-limited gate (once per batch), before the soft-cap gate.
	if cmd, gated := m.gateAllExhausted(plan); gated {
		return m, cmd
	}
```

- [ ] **Step 10: Handle `proceedExhaustedMsg` in Update**

In `app/app_update.go`, next to the `proceedOverCapMsg` case (132-140), add:

```go
	case proceedExhaustedMsg:
		// The user accepted spawning on a fully-rate-limited pool: spawn the staged
		// plan (already pinned to the soonest-to-reset member) on the UI thread.
		if m.pendingExhausted == nil {
			return m, nil
		}
		plan := *m.pendingExhausted
		m.pendingExhausted = nil
		return m, m.spawnVariants(plan)
```

- [ ] **Step 11: Update the picker consumer's other call sites (compile fixes)**

`GetSelectedAccount` changed return type. Search for stale callers and fix:

Run: `grep -rn "GetSelectedAccount" app/ ui/`
`app/app_session.go` is handled above. `SelectedAccountName()` (textInput_create.go:368) is a *separate* method and is untouched. If any other caller destructures `(acct, ok)`, convert it to the `*AccountSelection` shape. Then verify the tree builds:

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 12: Run tests to verify they pass**

Run: `go test ./app/ -run 'TestStartNewSession_|TestSoonestResetMember|TestCreateForm_AllExhausted' -v`
Expected: PASS. Then the package: `go test ./app/`
Expected: PASS.

- [ ] **Step 13: Commit**

```bash
git add ui/overlay/accountSelection.go ui/overlay/textInput_create.go app/session_cap.go app/app.go app/app_session.go app/app_update.go app/rotation_test.go
git commit -m "$(cat <<'EOF'
feat(app): availability-aware pool rotation on session creation

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Create-form picker — pool ⇄ and member entries

**Files:**
- Modify: `ui/overlay/accountPicker.go` (entry model, builder, `SelectByName`, selection)
- Modify: `ui/overlay/textInput_create.go` (`GetSelectedAccount` maps the entry; `SelectedAccountName`; pool preselect)
- Modify: `app/app_session.go` / `app/app_msgs.go` (preselect the routed **pool**, not the account)
- Test: `ui/overlay/accountPicker_test.go` (append; mirror `TestAccountPicker_SelectionAndPreselect`)

**Interfaces:**
- Consumes: `config.ClaudeAccount.Pool` (Task 1); `AccountSelection` (Task 5); `config.Config.ResolveClaudePool` (Task 3, for preselect).
- Produces: a picker that yields `AccountSelection{Pool}` for a `⇄` entry and `AccountSelection{Pool, Member}` for a member entry; unchanged behavior when no pools exist.

- [ ] **Step 1: Write the failing tests**

Append to `ui/overlay/accountPicker_test.go`:

```go
func TestAccountPicker_PoolEntries(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
		{Name: "personal", ConfigDir: "~/.claude-personal"},
	}
	ap := NewAccountPicker(accounts)

	// A pool contributes one rotating entry then one entry per member; ungrouped
	// accounts contribute one entry each.
	labels := ap.entryLabels()
	require.Equal(t, []string{"work ⇄", "  work-1", "  work-2", "personal"}, labels)

	// The ⇄ entry rotates the pool (no Member); a member entry pins with its pool.
	ap.selectIndex(0)
	e := ap.Selected()
	assert.Equal(t, "work", e.pool)
	assert.Nil(t, e.member)

	ap.selectIndex(1)
	e = ap.Selected()
	assert.Equal(t, "work", e.pool)
	require.NotNil(t, e.member)
	assert.Equal(t, "work-1", e.member.Name)

	ap.selectIndex(3)
	e = ap.Selected()
	assert.Equal(t, "", e.pool)
	require.NotNil(t, e.member)
	assert.Equal(t, "personal", e.member.Name)
}

func TestAccountPicker_NoPoolsUnchanged(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "personal", ConfigDir: "~/.claude"},
		{Name: "quantivly", ConfigDir: "~/.claude-quantivly", RemoteMatches: []string{"quantivly/"}},
	}
	ap := NewAccountPicker(accounts)
	assert.Equal(t, []string{"personal", "quantivly"}, ap.entryLabels(), "no pools: one entry per account, config order")
}

func TestAccountPicker_PreselectPool(t *testing.T) {
	accounts := []config.ClaudeAccount{
		{Name: "work-1", Pool: "work"}, {Name: "work-2", Pool: "work"}, {Name: "personal"},
	}
	ap := NewAccountPicker(accounts)
	ap.SelectByName("work") // preselect the pool ⇄ entry
	e := ap.Selected()
	assert.Equal(t, "work", e.pool)
	assert.Nil(t, e.member, "preselecting a pool lands on its rotating entry")
}
```

The tests use small test-only helpers `entryLabels()` and `selectIndex(i)` — add them to a `_test.go` accessor or expose minimal methods; `Selected()` is production (Step 3). Prefer test helpers in the test file:

```go
func (ap *AccountPicker) entryLabels() []string {
	out := make([]string, len(ap.entries))
	for i, e := range ap.entries {
		out[i] = e.label
	}
	return out
}
func (ap *AccountPicker) selectIndex(i int) { ap.cursor = i; ap.touched = true }
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ui/overlay/ -run TestAccountPicker_ -v`
Expected: FAIL — `ap.entries`, `ap.Selected`, `accountEntry` undefined.

- [ ] **Step 3: Introduce the entry model + builder + selection**

In `ui/overlay/accountPicker.go`, add the entry type and builder above `NewAccountPicker`:

```go
// accountEntry is one selectable line in the picker: a pool "⇄" entry (member
// nil) that rotates the pool, or a concrete account (member set) that pins.
// pool is the cluster key and is set on both an entry's ⇄ line and its members.
type accountEntry struct {
	label  string
	pool   string
	member *config.ClaudeAccount
}

// buildAccountEntries groups accounts by pool (first-appearance order): each
// multi-member pool contributes a "<pool> ⇄" rotating entry followed by its
// members; ungrouped accounts (Pool == "") contribute one entry each, in config
// order. With no pools this is one entry per account, identical to before.
func buildAccountEntries(accounts []config.ClaudeAccount) []accountEntry {
	var order []string
	byPool := map[string][]int{}
	for i, a := range accounts {
		if a.Pool == "" {
			continue
		}
		if _, seen := byPool[a.Pool]; !seen {
			order = append(order, a.Pool)
		}
		byPool[a.Pool] = append(byPool[a.Pool], i)
	}
	var entries []accountEntry
	for _, p := range order {
		entries = append(entries, accountEntry{label: p + " ⇄", pool: p})
		for _, idx := range byPool[p] {
			a := accounts[idx]
			entries = append(entries, accountEntry{label: "  " + a.Name, pool: p, member: &a})
		}
	}
	for i := range accounts {
		if accounts[i].Pool == "" {
			a := accounts[i]
			entries = append(entries, accountEntry{label: a.Name, pool: "", member: &a})
		}
	}
	return entries
}
```

Change the struct (13-23): replace `accounts []config.ClaudeAccount` with `entries []accountEntry`. Update `NewAccountPicker` (26-28):

```go
func NewAccountPicker(accounts []config.ClaudeAccount) *AccountPicker {
	return &AccountPicker{entries: buildAccountEntries(accounts)}
}
```

Add `Selected()` and update `HasMultiple`, `HandleKeyPress` bounds, and `GetSelectedAccount` to index `entries`:

```go
// Selected returns the cursored entry (zero value when empty).
func (ap *AccountPicker) Selected() accountEntry {
	if len(ap.entries) == 0 {
		return accountEntry{}
	}
	if ap.cursor < 0 || ap.cursor >= len(ap.entries) {
		return ap.entries[0]
	}
	return ap.entries[ap.cursor]
}

// GetSelectedAccount returns the cursored account for display: the member if the
// entry pins one, else a synthetic account named for the pool ⇄ entry.
func (ap *AccountPicker) GetSelectedAccount() config.ClaudeAccount {
	e := ap.Selected()
	if e.member != nil {
		return *e.member
	}
	return config.ClaudeAccount{Name: e.pool, Pool: e.pool}
}
```

`HasMultiple` becomes `len(ap.entries) > 1`; `HandleKeyPress` nav bounds use `len(ap.entries)`; `Render` iterates `ap.entries` using `e.label`.

Update `SelectByName` (33-43) to match a pool ⇄ entry first, then a member:

```go
func (ap *AccountPicker) SelectByName(name string) {
	if ap.touched {
		return
	}
	for i, e := range ap.entries { // prefer the pool ⇄ entry
		if e.member == nil && e.pool == name {
			ap.cursor = i
			return
		}
	}
	for i, e := range ap.entries { // else a concrete member
		if e.member != nil && e.member.Name == name {
			ap.cursor = i
			return
		}
	}
}
```

- [ ] **Step 4: Map the entry to `AccountSelection` in the overlay wrapper**

Replace the `GetSelectedAccount` from Task 5 (textInput_create.go) with the entry-aware version:

```go
// GetSelectedAccount returns the deliberate account choice, or nil when the user
// never touched the picker. A pool ⇄ entry rotates (Member nil); a member entry
// pins (Member set); Pool is the cluster key in both cases.
func (t *TextInputOverlay) GetSelectedAccount() *AccountSelection {
	if t.accountPicker == nil || !t.accountPicker.Touched() {
		return nil
	}
	e := t.accountPicker.Selected()
	if e.member == nil && e.pool == "" {
		return nil
	}
	return &AccountSelection{Pool: e.pool, Member: e.member}
}
```

`SelectedAccountName()` (368-373) still uses `t.accountPicker.GetSelectedAccount().Name` — now returns the pool name for a ⇄ entry, which is the right label.

- [ ] **Step 5: Preselect the routed pool from `app`**

Where `app` currently preselects by account name (`app/app_session.go:827` and `app/app_msgs.go:450`), preselect the routed **pool** so the ⇄ entry is highlighted. At app_session.go:827, replace the resolved account name with:

```go
	poolName, _, _ := m.appConfig.ResolveClaudePool(remoteURL, path)
	ov.PreselectAccount(poolName)
```

(Use whatever `remoteURL`/`path` are already in scope at that call site; if only an account name is available, `PreselectAccount(name)` still resolves via the member fallback in `SelectByName`, so this is a preference, not a correctness requirement.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./ui/overlay/ -run TestAccountPicker_ -v` then `go test ./ui/overlay/ ./app/`
Expected: PASS. Also `go build ./...` → PASS.

- [ ] **Step 7: Commit**

```bash
git add ui/overlay/accountPicker.go ui/overlay/textInput_create.go ui/overlay/accountPicker_test.go app/app_session.go app/app_msgs.go
git commit -m "$(cat <<'EOF'
feat(ui): pool and member entries in the account picker

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: List clustering + filter by pool

**Files:**
- Modify: `ui/list.go:82-84` (`accountKey`)
- Modify: `session/filter.go:189-197` (`accountTerm`)
- Test: `ui/list_group_account_test.go` (append) and `session/filter_test.go` (append)

**Interfaces:**
- Consumes: `Instance.ClaudeAccountPool` (Task 4), `Instance.ClaudeAccountName`.
- Produces: `accountKey` returns pool-else-name; `account:` filter matches pool or member.

- [ ] **Step 1: Write the failing tests**

Append to `session/filter_test.go`:

```go
func TestFilter_AccountPool(t *testing.T) {
	work1 := newFilterInstance(t, "a", "b")
	work1.SetClaudeAccount("work-1", "", false)
	work1.SetClaudeAccountPool("work")

	assert.True(t, ParseFilter("account:work").Matches(work1), "pool name matches a member")
	assert.True(t, ParseFilter("account:work-1").Matches(work1), "member name still matches")
	assert.True(t, ParseFilter("account:wo").Matches(work1), "prefix matches")

	none := newFilterInstance(t, "c", "d")
	assert.True(t, ParseFilter("account:none").Matches(none), "no account and no pool")
	assert.False(t, ParseFilter("account:none").Matches(work1))
}
```

Append to `ui/list_group_account_test.go` (use the file's existing instance/list helpers; adapt names to match that file):

```go
func TestAccountKey_PoolElseName(t *testing.T) {
	pooled := newInstanceForTest(t, "a")
	pooled.SetClaudeAccount("work-2", "", false)
	pooled.SetClaudeAccountPool("work")
	assert.Equal(t, "work", accountKey(pooled), "pinned pool is the cluster key")

	solo := newInstanceForTest(t, "b")
	solo.SetClaudeAccount("personal", "", false)
	assert.Equal(t, "personal", accountKey(solo), "no pool falls back to the account name")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./session/ -run TestFilter_AccountPool -v` and `go test ./ui/ -run TestAccountKey_PoolElseName -v`
Expected: FAIL — current `accountKey` returns only the name; `accountTerm` ignores the pool.

- [ ] **Step 3: Implement `accountKey`**

Replace `ui/list.go:82-84`:

```go
func accountKey(i *session.Instance) string {
	if p := i.ClaudeAccountPool(); p != "" {
		return p
	}
	return i.ClaudeAccountName()
}
```

- [ ] **Step 4: Implement `accountTerm`**

Replace `session/filter.go:189-197`:

```go
func accountTerm(value string) term {
	return func(i *Instance) bool {
		name := strings.ToLower(i.ClaudeAccountName())
		pool := strings.ToLower(i.ClaudeAccountPool())
		if value == "none" {
			return name == "" && pool == ""
		}
		return strings.HasPrefix(name, value) || (pool != "" && strings.HasPrefix(pool, value))
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./session/ -run TestFilter_AccountPool -v` and `go test ./ui/ -run TestAccountKey_PoolElseName -v`, then `go test ./session/ ./ui/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add ui/list.go session/filter.go ui/list_group_account_test.go session/filter_test.go
git commit -m "$(cat <<'EOF'
feat(ui): cluster and filter sessions by rotation pool

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: `@` accounts view — pool column + availability toggle

**Files:**
- Modify: `ui/overlay/accounts.go` (constructor, struct, `handleListKey`, row render, hint)
- Modify: `ui/overlay/accountForm.go` (add the `pool` input, Claude-only)
- Modify: `app/app_update.go:787` (pass `m.appState` into `NewAccountsOverlay`)
- Test: `ui/overlay/accounts_test.go` (append)

**Interfaces:**
- Consumes: `config.StateManager` (or a minimal availability interface); `AppState.GetAccountAvailability`/`SetAccountLimited`/`ClearAccountLimited`; `config.AccountAvailable` (Task 2); `config.ClaudeAccount.Pool` (Task 1).
- Produces: `NewAccountsOverlay(cfg *config.Config, state config.StateManager)`; the `l` key toggles the cursored Claude account's availability; rows show `pool:<name>` + `● available`/`⛔ limited`; the edit form has a `Pool` field.

- [ ] **Step 1: Write the failing tests**

Append to `ui/overlay/accounts_test.go` (extend `twoTabCfg` or add a pooled config helper):

```go
func TestAccountsOverlay_ToggleAvailability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
	}}
	st := config.DefaultState()
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)

	// Cursor on work-1; 'l' flags it limited.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	assert.True(t, st.GetAccountAvailability()["work-1"].Limited, "l flags the cursored account limited")

	// 'l' again clears it.
	o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	assert.Empty(t, st.GetAccountAvailability(), "l again clears the flag")
}

func TestAccountsOverlay_RendersPoolAndAvailability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"}}}
	st := config.DefaultState()
	require.NoError(t, st.SetAccountLimited("work-1", ""))
	o := NewAccountsOverlay(cfg, st)
	o.SetSize(80, 24)

	view := o.Render()
	assert.Contains(t, view, "pool:work")
	assert.Contains(t, view, "limited")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ui/overlay/ -run TestAccountsOverlay_ -v`
Expected: FAIL — `NewAccountsOverlay` takes one arg; no `l` handling; no pool/availability in the view.

- [ ] **Step 3: Add `StateManager` to the overlay**

In `ui/overlay/accounts.go`, add `state config.StateManager` to the struct (33-48) and change the constructor (52-54):

```go
func NewAccountsOverlay(cfg *config.Config, state config.StateManager) *AccountsOverlay {
	return &AccountsOverlay{cfg: cfg, state: state, width: 80, height: 24}
}
```

- [ ] **Step 4: Add the `l` toggle to `handleListKey`**

In `handleListKey` (114-151), add a case (only meaningful on the Claude tab). The `State` setters self-persist, so no `dirty` flag is needed:

```go
	case "l":
		if o.tab == tabClaude && o.activeLen() > 0 {
			name := o.cfg.ClaudeAccounts[o.cursor].Name
			if o.state.GetAccountAvailability()[name].Limited {
				_ = o.state.ClearAccountLimited(name)
			} else {
				_ = o.state.SetAccountLimited(name, "") // indefinite; reset-time entry is a future polish
			}
		}
```

- [ ] **Step 5: Render pool + availability on Claude rows**

In the Claude row render (accounts.go:434-451), after the dir column, append the pool and availability for the Claude tab. Insert before the `b.WriteString(...)` at line 450:

```go
			extra := ""
			if o.tab == tabClaude {
				acct := o.cfg.ClaudeAccounts[i]
				if acct.Pool != "" {
					extra += "  " + t.DimStyle().Render("pool:"+acct.Pool)
				}
				if config.AccountAvailable(o.state.GetAccountAvailability()[acct.Name], time.Now()) {
					extra += "  " + t.DimStyle().Render("● available")
				} else {
					extra += "  " + t.DangerStyle().Render("⛔ limited")
				}
			}
			b.WriteString(marker + padRight(name, 12) + " " + padRight(dir, 28) + " " + o.badge(r.catchAll, &seenCatchAll) + extra + "\n")
```

Add `"time"` and `config` imports to `accounts.go` if missing. Update the hint line (461-462) to mention the toggle, e.g. append `· l limited`.

- [ ] **Step 6: Add the `pool` field to the edit form**

In `ui/overlay/accountForm.go`: add `fldPool` to the index block (14-20); build a "Pool (optional)" input in `newAccountForm` (51-70), shown Claude-only (the mirror of the GH-only token field); add a `Pool()` accessor next to the others (160-170); add `"Pool"` to the render labels (195). In `accounts.go` `commit()` (209-232), set `acct.Pool = o.form.Pool()` on the Claude tab.

- [ ] **Step 7: Update the construction call site**

In `app/app_update.go:787`, change to:

```go
	m.accountsOverlay = overlay.NewAccountsOverlay(m.appConfig, m.appState)
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./ui/overlay/ -run TestAccountsOverlay_ -v`, then `go test ./ui/overlay/ ./app/` and `go build ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add ui/overlay/accounts.go ui/overlay/accountForm.go ui/overlay/accounts_test.go app/app_update.go
git commit -m "$(cat <<'EOF'
feat(ui): pool column and availability toggle in the @ accounts view

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Doctor warning + README

**Files:**
- Create: `internal/doctor/pools.go`, `internal/doctor/pools_test.go`
- Modify: `main.go` (wire the check into `doctor`, after the capacity block ~327, before `MissingRequired` ~329)
- Modify: `README.md` (Claude-accounts section)

**Interfaces:**
- Consumes: `config.Config.ClaudeAccounts`, `ClaudeAccount.Pool`, `ClaudeAccount.ResolvedConfigDir` (Task 1); the `CheckGatesInstalled`/`installedGateDirs` config-loading pattern (gates.go:166-219).
- Produces: `func CheckPools(cfg *config.Config) []PoolWarning`; `func RenderPools([]PoolWarning) string`.

- [ ] **Step 1: Write the failing test**

Create `internal/doctor/pools_test.go`:

```go
package doctor

import (
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckPools_SameConfigDir(t *testing.T) {
	// Two members of one pool pointing at the same config_dir are the same login:
	// rotation is a silent no-op. Flag it.
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work", Pool: "work"},
	}}
	warns := CheckPools(cfg)
	require.Len(t, warns, 1)
	assert.Equal(t, "work", warns[0].Pool)
	assert.Contains(t, warns[0].Detail, "work-1")
	assert.Contains(t, warns[0].Detail, "work-2")
	assert.Contains(t, RenderPools(warns), "work")
}

func TestCheckPools_DistinctDirsClean(t *testing.T) {
	cfg := &config.Config{ClaudeAccounts: []config.ClaudeAccount{
		{Name: "work-1", ConfigDir: "~/.claude-work", Pool: "work"},
		{Name: "work-2", ConfigDir: "~/.claude-work2", Pool: "work"},
		{Name: "personal", ConfigDir: "~/.claude-personal"},
	}}
	assert.Empty(t, CheckPools(cfg))
	assert.Equal(t, "", RenderPools(nil))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/doctor/ -run TestCheckPools -v`
Expected: FAIL — `CheckPools`/`RenderPools`/`PoolWarning` undefined.

- [ ] **Step 3: Implement**

Create `internal/doctor/pools.go`:

```go
package doctor

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/config"
)

// PoolWarning flags a rotation pool whose members share a config_dir — the same
// Claude login, so rotation across them is a silent no-op.
type PoolWarning struct {
	Pool   string
	Detail string
}

// CheckPools reports pools whose members resolve to the same config_dir.
func CheckPools(cfg *config.Config) []PoolWarning {
	byPool := map[string][]config.ClaudeAccount{}
	var order []string
	for _, a := range cfg.ClaudeAccounts {
		if a.Pool == "" {
			continue
		}
		if _, seen := byPool[a.Pool]; !seen {
			order = append(order, a.Pool)
		}
		byPool[a.Pool] = append(byPool[a.Pool], a)
	}
	var warns []PoolWarning
	for _, p := range order {
		seen := map[string][]string{}
		for _, a := range byPool[p] {
			dir := a.ResolvedConfigDir()
			seen[dir] = append(seen[dir], a.Name)
		}
		for dir, names := range seen {
			if len(names) > 1 {
				warns = append(warns, PoolWarning{
					Pool:   p,
					Detail: fmt.Sprintf("%s share %s — same login, rotation is a no-op", strings.Join(names, " and "), dir),
				})
			}
		}
	}
	return warns
}

// RenderPools formats pool warnings for `atrium doctor` (empty string when none).
func RenderPools(warns []PoolWarning) string {
	if len(warns) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Account pools\n")
	for _, w := range warns {
		fmt.Fprintf(&b, "  ⚠ pool %q: %s\n", w.Pool, w.Detail)
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/doctor/ -run TestCheckPools -v`
Expected: PASS.

- [ ] **Step 5: Wire into `atrium doctor`**

In `main.go`, after the capacity block (line 327) and before the `MissingRequired` gate (329), add:

```go
			if pools := doctor.RenderPools(doctor.CheckPools(config.LoadConfig())); pools != "" {
				fmt.Println()
				fmt.Print(pools)
			}
```

(`config` is already imported in `main.go`. `config.LoadConfig()` matches the doctor pattern that reads accounts — see gates.go:166.)

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 6: Document in README**

In `README.md`, in the Claude-accounts section, add a subsection covering: the `pool` field (group two accounts under one selection); rotation (`route → pool → next available member`, round-robin, per-session — a busy and an idle session weigh the same); the manual availability flag and the `@` view's `l` toggle; the all-exhausted confirm; the one-time setup (`CLAUDE_CONFIG_DIR=~/.claude-work2 claude` then `/login`); and that two members sharing a `config_dir` are the same login (rotation no-op) — the doctor check flags it.

- [ ] **Step 7: Commit**

```bash
git add internal/doctor/pools.go internal/doctor/pools_test.go main.go README.md
git commit -m "$(cat <<'EOF'
feat(doctor): warn on pool members sharing a config_dir; document pools

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

## Final verification (after all tasks)

- [ ] **Run the full gate:**

```bash
PATH="$HOME/go/bin:$PATH" just ci
```
Expected: build + vet + fmt-check + lint + test + cover all green. If lint dies at exit 127, `golangci-lint` isn't resolving — confirm `~/go/bin` is on PATH. If lint flags files outside this worktree, that's stale global cache: `golangci-lint cache clean` and re-run.

- [ ] **Smoke check (manual, `just build` then `./bin/atrium`) with a two-member pool configured:**
  1. Session badges alternate across pool members as you create sessions in a routed repo (work-1, work-2, work-1, …), and the list clusters them under one `work` block.
  2. Flag one member limited with `l` in the `@` view → new sessions route to the sibling only.
  3. Flag **all** members limited → creating a session shows the "all accounts rate-limited — create anyway?" confirm; declining spawns nothing; accepting pins the soonest-to-reset member.
  4. With **no** `pool` configured, everything behaves exactly as before (dormant).

## Self-Review notes (author checklist — done during authoring)

- **Spec coverage:** §1 pool field → T1; §2 state → T2; §3 ResolveClaudePool → T3; §4 rotation → T5; §5 all-exhausted confirm → T5; §6 instance pinning → T4; §7 picker → T6; §8 clustering/badge/filter → T7 (badge keeps rendering the member unchanged — no code needed; §8's ⛔-on-badge is the spec's explicit *optional* deferral); §9 `@` view → T8; §10 fan-out → T5 (per-variant rotation inside `startNewSession` spreads a batch; the all-exhausted gate is evaluated once at batch level in `createSessionFromForm`); §11 doctor → T9.
- **Type consistency:** `AccountSelection{Pool, Member}` defined in T5 (`ui/overlay`), consumed by T5 (app) and produced by T6 (picker); `spawnPlan.account *overlay.AccountSelection`; `startNewSession(..., sel *overlay.AccountSelection, ...)`; state accessors named identically in the interface (T2) and impls (T2) and call sites (T5, T8).
- **Deviation from the original (lost) plan's structure:** the original was described as having a deliberately-red build across Tasks 5–7. This reconstruction keeps every task's build green by adapting the picker output at the `TextInputOverlay` wrapper (T5) before enriching the picker itself (T6) — strictly better for TDD/subagent execution, same end state. Flagged for the executor.
