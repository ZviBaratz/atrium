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
	b.WriteString("Account pools:\n")
	for _, w := range warns {
		fmt.Fprintf(&b, "  ⚠ pool %q: %s\n", w.Pool, w.Detail)
	}
	return b.String()
}
