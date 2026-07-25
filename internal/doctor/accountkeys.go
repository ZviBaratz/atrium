package doctor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ZviBaratz/atrium/config"
)

// AccountKeyState is the slice of state.json this check reads: the three maps and
// lists that key persisted UI/rotation state by an account or pool NAME. A caller
// decodes them straight from the file rather than through config.LoadState, which
// mutates the data dir and so must not run beside a live TUI.
type AccountKeyState struct {
	Order        []string
	Availability []string
	Rotation     []string
}

// AccountKeyWarning flags one state.json entry naming an account or pool that
// config no longer declares.
type AccountKeyWarning struct {
	Key    string
	Detail string
}

// CheckAccountKeys reports state entries whose key config no longer has. Renaming an
// account or a pool leaves them behind: a `[`/`]` cluster slot for a name that can
// never render, a rate-limit flag for a login that no longer exists, a rotation
// cursor for a pool nobody is in (#470). None of them breaks anything — the whole
// point of keeping unknown names is that an account's slot survives its sessions
// going away — so these are warnings a user can act on, not errors.
//
// Dormant when no Claude accounts are configured: without a roster to compare
// against, every key would look orphaned.
func CheckAccountKeys(cfg *config.Config, st AccountKeyState) []AccountKeyWarning {
	if cfg == nil || len(cfg.ClaudeAccounts) == 0 {
		return nil
	}
	// A cluster key is a pool name or an account name; availability is keyed by
	// account name only; a rotation cursor by pool name only.
	accounts := map[string]bool{}
	pools := map[string]bool{}
	for _, a := range cfg.ClaudeAccounts {
		accounts[a.Name] = true
		if a.Pool != "" {
			pools[a.Pool] = true
		}
	}

	var warns []AccountKeyWarning
	for _, name := range st.Order {
		if name == "" || accounts[name] || pools[name] {
			continue
		}
		warns = append(warns, AccountKeyWarning{Key: name,
			Detail: "cluster order names no configured account or pool — its slot can never render"})
	}
	for _, name := range sorted(st.Availability) {
		if accounts[name] {
			continue
		}
		warns = append(warns, AccountKeyWarning{Key: name,
			Detail: "rate-limit flag on no configured account — it applies to nothing"})
	}
	for _, name := range sorted(st.Rotation) {
		if pools[name] || accounts[name] {
			continue
		}
		warns = append(warns, AccountKeyWarning{Key: name,
			Detail: "rotation cursor for no configured pool"})
	}
	return warns
}

// sorted returns names in a stable order, so map-keyed sections don't reorder the
// report between runs.
func sorted(names []string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}

// RenderAccountKeys formats orphaned-key warnings for `atrium doctor` (empty string
// when none), naming the fix: nothing is removed automatically, because a name with
// no live sessions is kept on purpose.
func RenderAccountKeys(warns []AccountKeyWarning) string {
	if len(warns) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Account state keys:\n")
	for _, w := range warns {
		fmt.Fprintf(&b, "  ⚠ %q: %s\n", w.Key, w.Detail)
	}
	b.WriteString("  → harmless; they clear themselves if you restore the name, or edit state.json to drop them\n")
	return b.String()
}
