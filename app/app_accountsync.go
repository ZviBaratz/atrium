package app

import (
	"fmt"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session"
)

// Re-deriving session account identity from live config (#470).
//
// A session stamps its account's name, catch-all flag and rotation pool at
// creation, and those strings are what the list clusters and badges by. Rename the
// account in config — or move it into a pool — and every existing session keeps
// clustering under a key config no longer has, while new sessions cluster under the
// new one: the same account rendered as two groups.
//
// The fix anchors on what a rename cannot change: the CLAUDE_CONFIG_DIR the
// session was actually born with (config.Config.FindClaudeAccount). The labels are
// re-derived from it; the dir itself is never rewritten, so the pass is idempotent
// and any later config fix re-heals from the same anchor. A session whose account
// is *gone* from config keeps its last-known stamp — a rename must heal, a deletion
// must not blank a badge.

// accountStampSync is what one re-stamp pass changed.
type accountStampSync struct {
	// restamped counts sessions whose labels moved — i.e. whether the healed
	// identities are worth writing back to state.json.
	restamped int
	// regrouped counts sessions whose CLUSTER KEY moved, the subset of restamped the
	// user actually sees rearrange. Renaming a pooled account, or an Antigravity
	// account, refreshes a label without moving any cluster.
	regrouped int
	// clusterMoves maps a cluster key that no longer has any session to the key its
	// sessions moved to ("work" -> "quantivly" after the account was renamed into a
	// pool). Only VANISHED keys are listed, so carrying the persisted cluster order
	// across them can never collide with a cluster still on screen.
	clusterMoves map[string]string
}

// changed reports whether the pass has anything to persist or announce.
func (s accountStampSync) changed() bool { return s.restamped > 0 || len(s.clusterMoves) > 0 }

// syncAccountStamps re-derives every instance's account labels from cfg, anchored
// on the config dir each session was born with, and reports what moved. It writes
// only labels: no session's config dir is touched, so re-running it is a no-op.
//
// Empty accounts config is dormant — nothing to re-derive from, so nothing changes
// (byte-for-byte behavior for a user who configures no accounts).
//
// Callers must run it on a single-threaded window: during load, before instances
// are published to the poll loop, or from Update (the only writer of these labels,
// and the only caller of ToInstanceData).
func syncAccountStamps(cfg *config.Config, instances []*session.Instance) accountStampSync {
	var out accountStampSync
	if cfg == nil || (len(cfg.ClaudeAccounts) == 0 && len(cfg.AgyAccounts) == 0) {
		return out
	}
	moves := map[string]string{}
	live := map[string]bool{}
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		before := inst.AccountClusterKey()
		if acct, ok := cfg.FindClaudeAccount(inst.ClaudeAccountName(), inst.ClaudeConfigDir()); ok {
			// Exactly what creation stamps (see app_session.go's account block), so the
			// pass and a fresh session can never disagree: the catch-all flag drives the
			// dim badge, and only a REAL declared pool becomes a cluster key.
			if inst.RestampClaudeAccount(acct.Name, acct.IsCatchAll(), acct.Pool) {
				out.restamped++
			}
		}
		if acct, ok := cfg.FindAgyAccount(inst.AgyAccountName(), inst.AgyConfigDir()); ok {
			if inst.RestampAgyAccount(acct.Name) {
				out.restamped++
			}
		}
		after := inst.AccountClusterKey()
		live[after] = true
		if before != after {
			out.regrouped++
			if _, seen := moves[before]; !seen {
				moves[before] = after
			}
		}
	}
	// A key some session still clusters under has not vanished, so its slot in the
	// persisted order is not free to carry (two sessions stamped "work", only one of
	// them healable).
	for old := range moves {
		if live[old] {
			delete(moves, old)
		}
	}
	if len(moves) > 0 {
		out.clusterMoves = moves
	}
	return out
}

// remapAccountOrder carries each renamed cluster to the slot the user chose for it
// with [ / ], instead of letting it fall to the bottom of the list as a key
// State.AccountOrder does not name (clusterByAccount appends unlisted keys last).
// Entries with no live sessions are preserved untouched — the order keeps them on
// purpose, to restore a slot when an account's sessions come back.
//
// First slot wins on collision and the duplicate is dropped: a duplicated key
// would make a [ / ] swap a silent no-op, since moveAccount indexes the first
// occurrence. Returns the (possibly unchanged) order and whether it moved.
func remapAccountOrder(order []string, moves map[string]string) ([]string, bool) {
	if len(order) == 0 || len(moves) == 0 {
		return order, false
	}
	next := make([]string, 0, len(order))
	seen := make(map[string]bool, len(order))
	changed := false
	for _, name := range order {
		if to, ok := moves[name]; ok {
			name, changed = to, true
		}
		if seen[name] {
			changed = true // a collapsed duplicate is itself a change
			continue
		}
		seen[name] = true
		next = append(next, name)
	}
	return next, changed
}

// flushAccountStamps lands the startup pass: the healed labels and the carried
// cluster order go to disk here because assembleHome performs no IO. It is what
// makes state.json self-heal on the first launch after a rename, so `atrium ls` and
// the daemon — which read the stored rows raw, from another process, and never
// re-derive — end up seeing the same identities the TUI does.
//
// One-shot and best-effort: a failed write is logged, not surfaced. The in-memory
// identities are already correct and the next ordinary save retries.
func (m *home) flushAccountStamps() {
	if !m.accountSync.changed() {
		return
	}
	if m.accountSync.restamped > 0 {
		if err := m.persistInstances(); err != nil {
			log.WarningLog.Printf("failed to persist healed account identities: %v", err)
		}
	}
	if order, moved := remapAccountOrder(m.appState.GetAccountOrder(), m.accountSync.clusterMoves); moved {
		if err := m.appState.SetAccountOrder(order); err != nil {
			log.WarningLog.Printf("failed to persist carried account order: %v", err)
		}
	}
	m.accountSync = accountStampSync{}
}

// resyncAccountStamps re-derives the labels of the sessions already on screen and
// re-clusters the list. The accounts panel is the one place config changes without
// a relaunch, so a rename there must land immediately rather than at next startup.
// Returns text describing what moved ("" when nothing did) for the caller to show
// once the panel stops covering the list — a view that visibly rearranges should
// explain itself.
func (m *home) resyncAccountStamps() string {
	sync := syncAccountStamps(m.appConfig, m.list.InstancesForPersist())
	if !sync.changed() {
		return ""
	}
	if sync.restamped > 0 {
		if err := m.persistInstances(); err != nil {
			log.WarningLog.Printf("failed to persist healed account identities: %v", err)
		}
	}
	order, moved := remapAccountOrder(m.appState.GetAccountOrder(), sync.clusterMoves)
	if moved {
		if err := m.appState.SetAccountOrder(order); err != nil {
			log.WarningLog.Printf("failed to persist carried account order: %v", err)
		}
		m.list.SetAccountOrder(order) // rebuilds the view itself
	} else {
		m.list.RegroupAccounts()
	}
	return accountSyncNotice(sync)
}

// accountSyncNotice describes a completed re-stamp, counting the sessions that
// actually moved on screen and naming the cluster they landed in — the part the
// list alone cannot explain. Several destinations stay generic rather than listing
// them; a rename that moved no cluster (a pooled account, or an Antigravity one)
// only refreshed badges, and says so.
func accountSyncNotice(sync accountStampSync) string {
	if sync.regrouped == 0 {
		return fmt.Sprintf("%d badge%s renamed to match the accounts config",
			sync.restamped, plural(sync.restamped))
	}
	if len(sync.clusterMoves) == 1 {
		for _, to := range sync.clusterMoves {
			return fmt.Sprintf("%d session%s regrouped under %q",
				sync.regrouped, plural(sync.regrouped), to)
		}
	}
	return fmt.Sprintf("%d session%s regrouped to match the renamed accounts",
		sync.regrouped, plural(sync.regrouped))
}
