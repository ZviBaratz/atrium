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
//
// Membership and the bucket key both match CheckParity, deliberately. Membership
// comes from config.PoolMembers, so an account with no pool of its own whose NAME is
// another account's pool counts as the member rotation treats it as; scanning for a
// matching Pool field skipped it, and `{name: work}` plus `{name: work-alt, pool:
// work}` on one dir — a rotation that is entirely a no-op — was reported by neither
// section. The key is NormalizedConfigDir for the reason that method's own doc gives:
// config_dir is hand-written, so left raw "/h/x" and "/h/x/" bucket apart and the one
// shape this check exists to catch hid behind a trailing slash.
func CheckPools(cfg *config.Config) []PoolWarning {
	if cfg == nil {
		return nil
	}

	// Pool names in the config order of their first mention, so the section does not
	// reorder between runs on an unchanged config.
	var pools []string
	seenPool := map[string]bool{}
	for _, a := range cfg.ClaudeAccounts {
		if a.Pool == "" || seenPool[a.Pool] {
			continue
		}
		seenPool[a.Pool] = true
		pools = append(pools, a.Pool)
	}

	var warns []PoolWarning
	for _, p := range pools {
		byDir := map[string][]string{}
		var dirOrder []string
		for _, a := range cfg.PoolMembers(p) {
			dir := a.NormalizedConfigDir()
			if _, seen := byDir[dir]; !seen {
				dirOrder = append(dirOrder, dir)
			}
			byDir[dir] = append(byDir[dir], a.Name)
		}
		// Walked in the order the dirs were first mentioned rather than over the map,
		// so a pool with two separate collisions renders the same way twice.
		for _, dir := range dirOrder {
			names := byDir[dir]
			if len(names) < 2 {
				continue
			}
			warns = append(warns, PoolWarning{Pool: p, Detail: poolCollision(names, dir)})
		}
	}
	return warns
}

// poolCollision describes one set of pool members that share a config dir. An empty
// dir is not a path they share but the absence of one — every member of that set
// inherits the ambient CLAUDE_CONFIG_DIR, which makes them the same login for the
// same reason — and naming it as a directory printed the sentence with a hole in it.
func poolCollision(names []string, dir string) string {
	joined := strings.Join(names, " and ")
	if dir == "" {
		return fmt.Sprintf("%s inherit the ambient CLAUDE_CONFIG_DIR — same login, rotation is a no-op", joined)
	}
	return fmt.Sprintf("%s share %s — same login, rotation is a no-op", joined, dir)
}

// RenderPools formats pool warnings for `atrium doctor` (empty string when none).
func RenderPools(warns []PoolWarning) string {
	if len(warns) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Account pools:\n")
	for _, w := range warns {
		fmt.Fprintf(&b, "  ⚠ pool %q: %s\n", w.Pool, w.Detail)
	}
	return b.String()
}
