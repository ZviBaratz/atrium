package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ZviBaratz/atrium/internal/repotrust"
)

// RepoTrustEntry is one recorded grant held up against the repo as it is now.
type RepoTrustEntry struct {
	Key     string
	State   string
	Granted string
	Hash    string
}

// CheckRepoTrust reads the per-repo trust ledger (#814) and compares every
// grant with its repo's committed config at the ref a new session would start
// from (updateBase is the user's update_base_on_create, which decides whether
// that is origin's tip). The returned error is a ledger that could not be read
// (corrupt, or written by a newer atrium) — worth a section of its own,
// because every repo reads as untrusted while it stands and nothing in the TUI
// says so persistently.
//
// The load never writes (repotrust.Load's contract), so doctor stays a pure
// reader of a data dir a live TUI may own. The per-record comparison forks git
// through calls that self-bound (session/git's local timeout), so a wedged
// repo costs timeouts, not a hang — bounded by how many repos are trusted.
func CheckRepoTrust(ctx context.Context, updateBase bool) ([]RepoTrustEntry, error) {
	ledger, err := repotrust.Load()
	keys := make([]string, 0, len(ledger.Repos))
	for k := range ledger.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entries := make([]RepoTrustEntry, 0, len(keys))
	for _, key := range keys {
		rec := ledger.Repos[key]
		hash := rec.Hash
		if len(hash) > 12 {
			hash = hash[:12]
		}
		entries = append(entries, RepoTrustEntry{
			Key:     key,
			State:   repotrust.LiveState(ctx, key, rec, updateBase),
			Granted: rec.GrantedAt.Format("2006-01-02"),
			Hash:    hash,
		})
	}
	return entries, err
}

// RenderRepoTrust formats the trust section for `atrium doctor` (empty when
// there are no grants and no ledger problem — the steady state for anyone not
// using repo-local config), in the section shape RenderPools established.
func RenderRepoTrust(entries []RepoTrustEntry, ledgerErr error) string {
	if len(entries) == 0 && ledgerErr == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("Repo trust (repo-local .atrium.json):\n")
	if ledgerErr != nil {
		fmt.Fprintf(&b, "  ⚠ %v — every repo reads as untrusted until this is fixed\n", ledgerErr)
	}
	// Column widths fit the widest value each column actually carries: the
	// 12-char short hash, and "changed (re-allow to use)" (25) for the state —
	// only an "absent at <ref>" with a long branch name overflows, trading
	// alignment for naming the ref.
	for _, e := range entries {
		fmt.Fprintf(&b, "  %-12s %-25s granted %s  %s\n", e.Hash, e.State, e.Granted, e.Key)
	}
	return b.String()
}
