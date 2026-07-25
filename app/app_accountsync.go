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
	// restampedSessions is the set of sessions whose labels moved — what decides
	// whether the healed identities are worth writing back to state.json, and the
	// count the badge-only notice reports. A set for the same reason as
	// regroupedSessions below, and because one session can be re-stamped twice in a
	// single pass when both its Claude and its Antigravity account were renamed.
	restampedSessions map[*session.Instance]bool
	// regroupedSessions is the set of sessions whose CLUSTER KEY moved — the subset
	// of restampedSessions the user actually sees rearrange (renaming a pooled account, or an
	// Antigravity one, refreshes a label without moving any cluster). A SET, not a
	// counter, because merging a visit's passes must count a session that moved twice
	// once: two renames of the same account move the same sessions again, and summing
	// would report more sessions rearranged than exist. Pointer identity is safe to
	// hold here — the accounts panel is modal, so nothing can be killed between the
	// passes it accumulates, and the set is only ever counted, never dereferenced.
	regroupedSessions map[*session.Instance]bool
	// clusterMoves maps a cluster key that no longer has any session to the key its
	// sessions moved to ("work" -> "quantivly" after the account was renamed into a
	// pool). Only VANISHED keys are listed, so carrying the persisted cluster order
	// across them can never collide with a cluster still on screen.
	clusterMoves map[string]string
	// destinations is the set of cluster keys regrouped sessions actually landed in,
	// and the only honest answer to "how many clusters was that". clusterMoves is
	// keyed by the OLD key and so cannot answer it: a fan-out collapses to a single
	// entry, and a move off a key some session still holds is dropped from it
	// entirely.
	destinations map[string]bool
}

// changed reports whether the pass has anything to persist or announce.
func (s accountStampSync) changed() bool {
	return len(s.restampedSessions) > 0 || len(s.clusterMoves) > 0
}

// restamped is how many sessions had a label re-derived, and regrouped how many of
// them visibly changed cluster.
func (s accountStampSync) restamped() int { return len(s.restampedSessions) }
func (s accountStampSync) regrouped() int { return len(s.regroupedSessions) }

// merge folds a later pass into this one. The accounts panel commits one pass per
// edit, each re-derived from the already-healed list, so only the accumulated view
// knows about every rename of a visit — holding one pass would let the last edit
// silently replace the rest.
func (s *accountStampSync) merge(other accountStampSync) {
	for inst := range other.restampedSessions {
		if s.restampedSessions == nil {
			s.restampedSessions = map[*session.Instance]bool{}
		}
		s.restampedSessions[inst] = true
	}
	for inst := range other.regroupedSessions {
		if s.regroupedSessions == nil {
			s.regroupedSessions = map[*session.Instance]bool{}
		}
		s.regroupedSessions[inst] = true
	}
	for from, to := range other.clusterMoves {
		if s.clusterMoves == nil {
			s.clusterMoves = map[string]string{}
		}
		if _, seen := s.clusterMoves[from]; !seen {
			s.clusterMoves[from] = to
		}
	}
	for to := range other.destinations {
		if s.destinations == nil {
			s.destinations = map[string]bool{}
		}
		s.destinations[to] = true
	}
}

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
	dests := map[string]bool{}
	live := map[string]bool{}
	moved := map[*session.Instance]bool{}
	touched := map[*session.Instance]bool{}
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
				touched[inst] = true
			}
		}
		if acct, ok := cfg.FindAgyAccount(inst.AgyAccountName(), inst.AgyConfigDir()); ok {
			if inst.RestampAgyAccount(acct.Name) {
				touched[inst] = true
			}
		}
		after := inst.AccountClusterKey()
		live[after] = true
		if before != after {
			moved[inst] = true
			dests[after] = true
			// First destination wins: one persisted slot cannot be split, so when a
			// stale key fans out — two sessions sharing a pool whose accounts were
			// later split apart — the first session's cluster keeps the slot and the
			// rest append. Which is why the notice counts destinations, not these.
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
	if len(touched) > 0 {
		out.restampedSessions = touched
	}
	if len(moved) > 0 {
		out.regroupedSessions = moved
	}
	if len(moves) > 0 {
		out.clusterMoves = moves
	}
	if len(dests) > 0 {
		out.destinations = dests
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

// persistAccountSync lands one pass to disk: the carried cluster order first, then
// the healed labels.
//
// The order goes first because it is the half with no second chance. The labels are
// already correct in memory, so any later ordinary save rewrites them; but
// clusterMoves is a DIFF between the stamp on disk and live config, and once healed
// labels land nothing disagrees any more, so no later pass can recompute it. Writing
// labels first would let a failed order write strand the user's [ / ] slot behind
// them permanently. In this order a failure leaves both halves stale, so the next
// launch recomputes the move and retries the pair — which is why the label write is
// skipped once the order write has failed. That retry is best-effort rather than
// guaranteed: the healed labels are live in memory, so an ordinary save later in the
// same run persists them anyway and the carry is lost. It costs nothing to order the
// writes this way, and both go to the same state.json — a failure that hits one
// almost certainly hits the other — so this narrows the window rather than closing
// it.
//
// Best-effort: failures are logged, not surfaced. Returns the carried order and
// whether it moved, so the caller can rebuild the view it belongs to.
func (m *home) persistAccountSync(sync accountStampSync) ([]string, bool) {
	order, moved := remapAccountOrder(m.appState.GetAccountOrder(), sync.clusterMoves)
	if moved {
		if err := m.appState.SetAccountOrder(order); err != nil {
			log.WarningLog.Printf("failed to persist carried account order: %v", err)
			return order, moved
		}
	}
	if sync.restamped() > 0 {
		if err := m.persistInstances(); err != nil {
			log.WarningLog.Printf("failed to persist healed account identities: %v", err)
		}
	}
	return order, moved
}

// flushAccountStamps lands the startup pass, which assembleHome could not write
// itself because it performs no IO. It is what makes state.json self-heal on the
// first launch after a rename, so `atrium ls` and the daemon — which read the stored
// rows raw, from another process, and never re-derive — end up seeing the same
// identities the TUI does.
func (m *home) flushAccountStamps() {
	if !m.accountSync.changed() {
		return
	}
	m.persistAccountSync(m.accountSync)
	m.accountSync = accountStampSync{}
}

// resyncAccountStamps re-derives the labels of the sessions already on screen and
// re-clusters the list. The accounts panel is the one place config changes without
// a relaunch, so a rename there must land immediately rather than at next startup.
// Returns what the pass moved, for the caller to accumulate and announce once the
// panel stops covering the list — a view that visibly rearranges should explain
// itself, and a toast fired behind a modal expires unseen.
func (m *home) resyncAccountStamps() accountStampSync {
	sync := syncAccountStamps(m.appConfig, m.list.InstancesForPersist())
	if !sync.changed() {
		return sync
	}
	if order, moved := m.persistAccountSync(sync); moved {
		m.list.SetAccountOrder(order) // rebuilds the view itself
	} else {
		m.list.RegroupAccounts()
	}
	return sync
}

// accountSyncNotice describes a completed re-stamp, counting the sessions that
// actually moved on screen and naming the cluster they landed in — the part the
// list alone cannot explain. Several destinations stay generic rather than listing
// them; a rename that moved no cluster (a pooled account, or an Antigravity one)
// only refreshed badges, and says so.
func accountSyncNotice(sync accountStampSync) string {
	regrouped := sync.regrouped()
	if regrouped == 0 {
		restamped := sync.restamped()
		return fmt.Sprintf("%d badge%s renamed to match the accounts config",
			restamped, plural(restamped))
	}
	if len(sync.destinations) == 1 {
		for to := range sync.destinations {
			return fmt.Sprintf("%d session%s regrouped under %q", regrouped, plural(regrouped), to)
		}
	}
	return fmt.Sprintf("%d session%s regrouped to match the renamed accounts",
		regrouped, plural(regrouped))
}
