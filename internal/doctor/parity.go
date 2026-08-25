package doctor

// Reporting whether the members of a rotation pool are actually substitutes.
//
// A pool is a promise that its members are interchangeable, and rotation spends that
// promise on every unpinned session: SelectPoolMember takes the next available
// member and nothing in the choice consults what that member's config dir can do.
// The promise has never been checked. Three accounts sharing one pool were found
// holding nine claude.ai connector grants, zero, and zero — three genuinely distinct
// logins, so the identity section was satisfied and the pools section had nothing to
// say, while most of the user's connector traffic was being routed to dirs that
// silently lacked the integration.
//
// The three sections are one question asked three ways, and this is the third:
// identity catches one login wearing two names, pools catches two names on one dir,
// parity catches two real logins that are not substitutes.
//
// The section is silent when a pool is in parity, so it must never be silent when a
// member could not be read. An unreadable member gets its own warning rather than
// being dropped: "we compared these and they agree" and "we could not look" are the
// two conclusions a reader would otherwise be unable to tell apart.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ZviBaratz/atrium/config"
)

// ParityKind is the class of capability a warning is about.
//
// The zero value is ParityUnreadable on purpose: a warning carrying no capability
// class is one about a member nothing could be read from, which is the honest thing
// for an unset kind to mean here.
type ParityKind int

const (
	// ParityUnreadable means this member's config dir could not be read, so it was
	// left out of the comparison. Never evidence that it lacks anything.
	ParityUnreadable ParityKind = iota
	// ParityPlugin is a plugin enabled for some members and not others.
	ParityPlugin
	// ParityMarketplace is a plugin marketplace known to some members and not others.
	ParityMarketplace
	// ParityMCPServer is an MCP server configured for some members and not others.
	ParityMCPServer
	// ParityDeniedMCPServer is an MCP server some members deny and others allow.
	ParityDeniedMCPServer
	// ParityConnectors is claude.ai connectors being disabled for some members only.
	ParityConnectors
)

// ParityWarning is one way the members of a pool are not substitutes.
//
// Have and Lack are stated in terms of the CAPABILITY, not of the setting that
// produces it: Have lists the members where the thing is available and Lack the
// members where it is not. For ParityDeniedMCPServer that inverts the file — a
// member listing a server under deniedMcpServers is the one that Lacks it — because
// a reader is diagnosing missing capability, not auditing settings keys.
//
// For ParityUnreadable there is nothing to compare, so Have is empty and Lack holds
// the single member that could not be read, with Feature naming its config dir.
type ParityWarning struct {
	Pool    string
	Kind    ParityKind
	Feature string   // the plugin / marketplace / server; the config dir when unreadable
	Have    []string // members where it is available, in config order
	Lack    []string // members where it is not, in config order
}

// parityMember pairs a configured account name with what its dir turned out to hold.
type parityMember struct {
	name string
	caps config.DirCapabilities
}

// CheckParity reports the capability differences inside each rotation pool. Pure
// apart from the injected reader, so tests never touch a real config dir.
//
// A nil reader (or nil config) reports nothing, which is how a caller that has not
// wired a reader renders exactly as it did before this section existed.
//
// Only accounts with a non-empty Pool are considered, matching CheckPools: an
// account with no pool is a singleton, and a pool of one has nothing to be
// interchangeable with. Pools appear in the config order of their first member and
// members keep config order, so the section does not reorder between runs on an
// unchanged config.
func CheckParity(cfg *config.Config, read config.CapabilityReadFunc) []ParityWarning {
	if cfg == nil || read == nil {
		return nil
	}

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

	// One cache for the whole report: two accounts naming one directory cost a single
	// read and, more to the point, cannot be made to disagree with each other by a
	// file rewritten between two of them. That pair is not hypothetical — it is
	// exactly the configuration CheckPools flags.
	cached := cachedCapabilityRead(read)

	var warns []ParityWarning
	for _, pool := range order {
		accounts := byPool[pool]
		if len(accounts) < 2 {
			continue
		}
		var members []parityMember
		for _, a := range accounts {
			caps, ok := cached(a.NormalizedConfigDir())
			if !ok {
				warns = append(warns, ParityWarning{
					Pool: pool, Kind: ParityUnreadable,
					Feature: a.NormalizedConfigDir(), Lack: []string{a.Name},
				})
				continue
			}
			members = append(members, parityMember{name: a.Name, caps: caps})
		}
		if len(members) < 2 {
			continue // nothing readable left to compare against
		}
		warns = append(warns, diffPool(pool, members)...)
	}
	return warns
}

// diffPool emits every difference between two or more readable members of one pool,
// in a fixed section order (plugins, marketplaces, MCP servers, denials, connectors)
// and alphabetically within each, so the rendered block is stable.
func diffPool(pool string, members []parityMember) []ParityWarning {
	var warns []ParityWarning
	warns = append(warns, diffNames(pool, ParityPlugin, members,
		func(c config.DirCapabilities) []string { return c.EnabledPlugins },
		func(c config.DirCapabilities, n string) bool { return contains(c.EnabledPlugins, n) })...)
	warns = append(warns, diffNames(pool, ParityMarketplace, members,
		func(c config.DirCapabilities) []string { return c.Marketplaces },
		func(c config.DirCapabilities, n string) bool { return contains(c.Marketplaces, n) })...)
	warns = append(warns, diffNames(pool, ParityMCPServer, members,
		func(c config.DirCapabilities) []string { return c.MCPServers },
		func(c config.DirCapabilities, n string) bool { return contains(c.MCPServers, n) })...)
	// Inverted on purpose: the union is built from the denial lists, but a member is
	// counted as HAVING the server precisely when it does not deny it.
	warns = append(warns, diffNames(pool, ParityDeniedMCPServer, members,
		func(c config.DirCapabilities) []string { return c.DeniedMCPServers },
		func(c config.DirCapabilities, n string) bool { return !contains(c.DeniedMCPServers, n) })...)

	var on, off []string
	for _, m := range members {
		if m.caps.ConnectorsOff {
			off = append(off, m.name)
		} else {
			on = append(on, m.name)
		}
	}
	if len(on) > 0 && len(off) > 0 {
		warns = append(warns, ParityWarning{
			Pool: pool, Kind: ParityConnectors, Have: on, Lack: off,
		})
	}
	return warns
}

// diffNames emits one warning per name that some members have and others lack.
// list supplies the names a member contributes to the union; available answers, per
// member, whether the capability is actually there — the two are separate so a
// denial list can build a union out of the members that DENY a server while the
// warning still names the members that have it.
func diffNames(pool string, kind ParityKind, members []parityMember,
	list func(config.DirCapabilities) []string,
	available func(config.DirCapabilities, string) bool) []ParityWarning {

	var union []string
	for _, m := range members {
		union = append(union, list(m.caps)...)
	}
	var warns []ParityWarning
	for _, name := range sortedUnique(union) {
		var have, lack []string
		for _, m := range members {
			if available(m.caps, name) {
				have = append(have, m.name)
			} else {
				lack = append(lack, m.name)
			}
		}
		if len(have) == 0 || len(lack) == 0 {
			continue // every member agrees; not a parity problem
		}
		warns = append(warns, ParityWarning{
			Pool: pool, Kind: kind, Feature: name, Have: have, Lack: lack,
		})
	}
	return warns
}

// cachedCapabilityRead memoises read per directory for the life of one report.
func cachedCapabilityRead(read config.CapabilityReadFunc) config.CapabilityReadFunc {
	type result struct {
		caps config.DirCapabilities
		ok   bool
	}
	seen := map[string]result{}
	return func(dir string) (config.DirCapabilities, bool) {
		got, cached := seen[dir]
		if !cached {
			got.caps, got.ok = read(dir)
			seen[dir] = got
		}
		return got.caps, got.ok
	}
}

func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// sortedUnique sorts and deduplicates, so the union a diff walks is stable and each
// name is reported once however many members contributed it.
func sortedUnique(names []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// CheckParityInstalled reports on the real environment. Like
// CheckAccountIdentityInstalled it is the only function here that loads config —
// LoadConfig seeds config.json when absent, which is a write and must stay out of
// the pure CheckParity that tests call.
func CheckParityInstalled() []ParityWarning {
	return CheckParity(config.LoadConfig(), config.ReadDirCapabilities)
}

// noun is what a have/lack difference line leads with. Only the three kinds that
// reach that line have an arm: unreadable members, denials and connectors each get
// a sentence of their own in line() and never ask for a noun.
func (k ParityKind) noun() string {
	switch k {
	case ParityMarketplace:
		return "marketplace"
	case ParityMCPServer:
		return "MCP server"
	default: // ParityPlugin
		return "plugin"
	}
}

// line renders one warning without its pool prefix.
func (w ParityWarning) line() string {
	switch w.Kind {
	case ParityUnreadable:
		return fmt.Sprintf("capabilities unreadable for %s (%s) — it was left out of the comparison",
			quotedList(w.Lack), w.Feature)
	case ParityConnectors:
		return fmt.Sprintf("claude.ai connectors are on for %s but disabled for %s",
			quotedList(w.Have), quotedList(w.Lack))
	case ParityDeniedMCPServer:
		return fmt.Sprintf("MCP server %q is denied for %s but not for %s",
			w.Feature, quotedList(w.Lack), quotedList(w.Have))
	default:
		return fmt.Sprintf("%s %q: %s %s it, %s %s not",
			w.Kind.noun(), w.Feature,
			quotedList(w.Have), agrees(w.Have, "has", "have"),
			quotedList(w.Lack), agrees(w.Lack, "does", "do"))
	}
}

// agrees picks the verb form for a list of account names.
func agrees(names []string, singular, plural string) string {
	if len(names) == 1 {
		return singular
	}
	return plural
}

// allUnreadable reports whether nothing in warns is an actual comparison.
func allUnreadable(warns []ParityWarning) bool {
	for _, w := range warns {
		if w.Kind != ParityUnreadable {
			return false
		}
	}
	return true
}

// RenderParity formats the section for `atrium doctor` (empty string when no pool
// has anything to report).
//
// The trailing hint states the consequence rather than the fact, because "these
// dirs differ" is a curiosity until it is spelled out as rotation placing sessions
// on a member that cannot do the work. It promises nothing Atrium does not do:
// routing deliberately does not consult capability — expect_account's boundary is
// that verification decides whether a chosen account may launch, never which one is
// chosen — so the remedy offered is the user's, not Atrium's.
func RenderParity(warns []ParityWarning) string {
	if len(warns) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Account pool parity:\n")
	for _, w := range warns {
		fmt.Fprintf(&b, "  ⚠ pool %q: %s\n", w.Pool, w.line())
	}
	if allUnreadable(warns) {
		// Nothing was compared, so the generic remedy would be a non-sequitur: the
		// user's problem here is that the question went unanswered, not that two dirs
		// disagree.
		b.WriteString("    → these members were not measured, so their pools are unverified.\n")
		b.WriteString("      Check the dir exists and its settings.json / .claude.json parse.\n")
		return b.String()
	}
	b.WriteString("    → rotation picks whichever member is next; it does not consult capability,\n")
	b.WriteString("      so a session placed on a member that lacks one just quietly goes without.\n")
	b.WriteString("      Align the config dirs, or split the pool.\n")
	return b.String()
}
