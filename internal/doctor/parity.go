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
// That original incident is also the limit of what this section can see. Grant state
// is not in any file (ReadDirCapabilities says why it is not fetched), so a pool
// whose members are configured identically and differ only in what claude.ai granted
// them reads as being in parity here. What this section catches is the configuration
// half: a plugin, a marketplace or an MCP server one member has and another does
// not, or one they both have that points somewhere different.
//
// The section is silent when a pool is in parity, so it must never be silent when a
// member could not be read. Every unanswered question gets a line of its own —
// a whole dir that would not read, a single dimension whose file was absent, an
// account with no dir at all — because "we compared these and they agree" and "we
// could not look" are the two conclusions a reader would otherwise be unable to tell
// apart.

import (
	"fmt"
	"slices"
	"strings"

	"github.com/ZviBaratz/atrium/config"
)

// ParityKind is what one warning is telling the reader.
type ParityKind int

const (
	// parityKindUnset is the zero value and is never a real warning. It exists so a
	// ParityWarning built without a Kind renders as an obvious internal fault
	// instead of quietly impersonating whichever kind happened to be iota 0.
	parityKindUnset ParityKind = iota
	// ParityNoConfigDir means the member injects no CLAUDE_CONFIG_DIR, so it has no
	// dir of its own to compare. Not a dir that failed to read: there is no dir.
	ParityNoConfigDir
	// ParityUnreadable means the member's config dir could not be read at all, so it
	// was left out of the comparison. Never evidence that it lacks anything.
	ParityUnreadable
	// ParityUnmeasured means one dimension could not be read for some members — the
	// file that records it was absent, or held a shape this build does not
	// recognise — so that dimension is unverified for the pool.
	ParityUnmeasured
	// ParityMissing means a capability some members have and others lack.
	ParityMissing
	// ParityDivergent means every compared member has the capability under the same
	// name, but configured to point at different things.
	ParityDivergent
	// ParityConnectors means claude.ai connectors are enabled for some members only.
	ParityConnectors
	// ParityConnectorsUnknown means the connector setting could not be read for some
	// members, so the pool is unverified on that axis.
	ParityConnectorsUnknown
)

// ParityWarning is one way the members of a pool are not known to be substitutes.
//
// Have and Lack are stated in terms of the CAPABILITY rather than of the setting
// that produces it: Have lists the members where the thing is available and Lack the
// members where it is not, both in config order. For the kinds that report an
// unanswered question, Have is empty and Lack holds the members nothing could be
// established for.
type ParityWarning struct {
	// Pool is the pool the members share.
	Pool string
	// Kind is what this warning says.
	Kind ParityKind
	// Dimension is the axis a ParityMissing, ParityDivergent or ParityUnmeasured
	// warning is about, and DimensionUnspecified for the rest.
	Dimension config.Dimension
	// Feature is the plugin, marketplace or server name. Empty for the kinds that
	// are about a member rather than a capability.
	Feature string
	// Dir is the config dir a ParityUnreadable warning could not read, and empty
	// otherwise. A separate field from Feature on purpose: one field carrying either
	// a directory or a capability name meant a warning missing its Kind rendered a
	// capability name as a path.
	Dir string
	// Have lists members where the capability is available; Lack lists the rest, or
	// the members an unanswered question is about.
	Have []string
	Lack []string
}

// parityMember pairs a member's display label with what its dir turned out to hold.
type parityMember struct {
	label string
	caps  config.DirCapabilities
}

// CheckParity reports the capability differences inside each rotation pool. Pure
// apart from the injected reader, so tests never touch a real config dir.
//
// A nil reader (or nil config) reports nothing, which is how a caller that has not
// wired a reader renders exactly as it did before this section existed.
//
// Membership comes from config.PoolMembers, which is the function rotation itself
// resolves through, and NOT from a local scan for accounts whose Pool matches. The
// difference is load-bearing: PoolMembers also counts an account with no pool of its
// own whose NAME is used as another account's pool, so `{name: work}` plus
// `{name: work-alt, pool: work}` is a real two-member rotation. A local scan skipped
// the first of those for having an empty Pool, left one member, and went silent on
// the one shape where rotation was live and drifted.
func CheckParity(cfg *config.Config, read config.CapabilityReadFunc) []ParityWarning {
	if cfg == nil || read == nil {
		return nil
	}

	// Pool names in the config order of their first mention, so the section does not
	// reorder between runs on an unchanged config.
	var pools []string
	seen := map[string]bool{}
	for _, a := range cfg.ClaudeAccounts {
		if a.Pool == "" || seen[a.Pool] {
			continue
		}
		seen[a.Pool] = true
		pools = append(pools, a.Pool)
	}

	// One cache for the whole report: two accounts naming one directory cost a single
	// read and, more to the point, cannot be made to disagree with each other by a
	// file rewritten between two of them. That pair is not hypothetical — it is
	// roughly the configuration CheckPools flags, though that section buckets the
	// raw config_dir while this one keys on the cleaned path.
	cached := cachedCapabilityRead(read)

	var warns []ParityWarning
	for _, pool := range pools {
		accounts := cfg.PoolMembers(pool)
		if len(accounts) < 2 {
			continue
		}
		labels := memberLabels(accounts)
		var members []parityMember
		for i, a := range accounts {
			dir := a.NormalizedConfigDir()
			if dir == "" {
				// An inherit-env account has no dir to be wrong about, which is why
				// the identity section skips it outright. Here it still needs saying:
				// a pool one of whose members rides the ambient env is a pool whose
				// interchangeability cannot be established at all.
				warns = append(warns, ParityWarning{
					Pool: pool, Kind: ParityNoConfigDir, Lack: []string{labels[i]},
				})
				continue
			}
			caps, ok := cached(dir)
			if !ok {
				warns = append(warns, ParityWarning{
					Pool: pool, Kind: ParityUnreadable, Dir: dir, Lack: []string{labels[i]},
				})
				continue
			}
			members = append(members, parityMember{label: labels[i], caps: caps})
		}
		warns = append(warns, diffPool(pool, members)...)
	}
	return warns
}

// memberLabels is how each member is named in the output. A name is used as written
// when it is unique within the pool and not blank; otherwise it is qualified by the
// dir, because config.json is hand-editable and the only uniqueness guard runs in
// the accounts overlay rather than on load. Unqualified, two members both named
// "work" rendered as `"work" has it, "work" does not` — the section that exists to
// make a bad pool diagnosable being the one place a duplicate name made it
// undiagnosable.
func memberLabels(accounts []config.ClaudeAccount) []string {
	count := map[string]int{}
	for _, a := range accounts {
		count[a.Name]++
	}
	labels := make([]string, len(accounts))
	for i, a := range accounts {
		where := a.NormalizedConfigDir()
		if where == "" {
			where = "ambient env"
		}
		switch {
		case strings.TrimSpace(a.Name) == "":
			labels[i] = fmt.Sprintf("unnamed account at %s", where)
		case count[a.Name] > 1:
			labels[i] = fmt.Sprintf("%s at %s", a.Name, where)
		default:
			labels[i] = a.Name
		}
	}
	return labels
}

// diffPool emits every difference between the readable members of one pool, walking
// config.Dimensions() in order and then the connector setting, so the rendered block
// is stable.
//
// Fewer than two readable members means there is nothing to compare against, and in
// particular no dimension to call unverified: the ParityUnreadable and
// ParityNoConfigDir lines already say why the pool went unchecked, and adding a
// per-dimension line for a single member would repeat it three times.
func diffPool(pool string, members []parityMember) []ParityWarning {
	if len(members) < 2 {
		return nil
	}
	var warns []ParityWarning
	for _, dim := range config.Dimensions() {
		warns = append(warns, diffDimension(pool, dim, members)...)
	}
	return append(warns, diffConnectors(pool, members)...)
}

// diffDimension emits the unmeasured members of one dimension, then one warning per
// name the measured members disagree about — either because some lack it, or because
// they all have it pointing somewhere different.
func diffDimension(pool string, dim config.Dimension, members []parityMember) []ParityWarning {
	var measured []parityMember
	var unmeasured []string
	for _, m := range members {
		if m.caps.State(dim).Measured {
			measured = append(measured, m)
		} else {
			unmeasured = append(unmeasured, m.label)
		}
	}

	var warns []ParityWarning
	if len(unmeasured) > 0 {
		warns = append(warns, ParityWarning{
			Pool: pool, Kind: ParityUnmeasured, Dimension: dim, Lack: unmeasured,
		})
	}
	if len(measured) < 2 {
		return warns
	}

	names := map[string]bool{}
	for _, m := range measured {
		for _, n := range m.caps.State(dim).Names() {
			names[n] = true
		}
	}
	for _, name := range sortedKeys(names) {
		var have, lack []string
		for _, m := range measured {
			if m.caps.State(dim).Has(name) {
				have = append(have, m.label)
			} else {
				lack = append(lack, m.label)
			}
		}
		if len(have) > 0 && len(lack) > 0 {
			warns = append(warns, ParityWarning{
				Pool: pool, Kind: ParityMissing, Dimension: dim,
				Feature: name, Have: have, Lack: lack,
			})
			continue
		}
		// Every measured member has the name. Two dirs can still fail to be
		// substitutes here: the same marketplace name pointing at a different repo,
		// or the same MCP server name pointing at a different URL or command, is a
		// member that cannot do the work while looking identical by name.
		if diverges(dim, name, measured) {
			warns = append(warns, ParityWarning{
				Pool: pool, Kind: ParityDivergent, Dimension: dim,
				Feature: name, Have: labelsOf(measured),
			})
		}
	}
	return warns
}

// diverges reports whether the members configure name to point at different things.
// A member whose target could not be canonicalised makes the answer no rather than
// yes: two values nothing could compare are not evidence of a difference.
func diverges(dim config.Dimension, name string, members []parityMember) bool {
	first := ""
	for i, m := range members {
		target := m.caps.State(dim).Target(name)
		if target == "" {
			return false
		}
		if i == 0 {
			first = target
			continue
		}
		if target != first {
			return true
		}
	}
	return false
}

// diffConnectors splits the members by whether claude.ai connectors are available,
// reporting members whose setting could not be read separately from a genuine split.
func diffConnectors(pool string, members []parityMember) []ParityWarning {
	var on, off, unknown []string
	for _, m := range members {
		switch m.caps.Connectors {
		case config.ConnectorsOn:
			on = append(on, m.label)
		case config.ConnectorsOff:
			off = append(off, m.label)
		default:
			unknown = append(unknown, m.label)
		}
	}
	var warns []ParityWarning
	if len(unknown) > 0 {
		warns = append(warns, ParityWarning{
			Pool: pool, Kind: ParityConnectorsUnknown, Lack: unknown,
		})
	}
	if len(on) > 0 && len(off) > 0 {
		warns = append(warns, ParityWarning{
			Pool: pool, Kind: ParityConnectors, Have: on, Lack: off,
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

// labelsOf is the members' labels in config order.
func labelsOf(members []parityMember) []string {
	out := make([]string, len(members))
	for i, m := range members {
		out[i] = m.label
	}
	return out
}

// sortedKeys is the set's members in sorted order, so a union walked out of a map
// does not reorder between runs.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// CheckParityInstalled reports on the real environment. Like
// CheckAccountIdentityInstalled it is the only function here that loads config —
// LoadConfig seeds config.json when absent, which is a write and must stay out of
// the pure CheckParity that tests call.
func CheckParityInstalled() []ParityWarning {
	return CheckParity(config.LoadConfig(), config.ReadDirCapabilities)
}

// line renders one warning without its pool prefix.
func (w ParityWarning) line() string {
	switch w.Kind {
	case ParityNoConfigDir:
		return fmt.Sprintf("%s injects no CLAUDE_CONFIG_DIR, so what it brings to a session cannot be compared",
			quotedList(w.Lack))
	case ParityUnreadable:
		return fmt.Sprintf("capabilities unreadable for %s (%s) — it was left out of the comparison",
			quotedList(w.Lack), w.Dir)
	case ParityUnmeasured:
		return fmt.Sprintf("%s parity is unverified: %s %s not report one, so nothing was compared",
			w.Dimension.Noun(), quotedList(w.Lack), agrees(w.Lack, "does", "do"))
	case ParityMissing:
		return fmt.Sprintf("%s %q: %s %s it, %s %s not",
			w.Dimension.Noun(), w.Feature,
			quotedList(w.Have), agrees(w.Have, "has", "have"),
			quotedList(w.Lack), agrees(w.Lack, "does", "do"))
	case ParityDivergent:
		return fmt.Sprintf("%s %q is configured differently across %s — same name, different target",
			w.Dimension.Noun(), w.Feature, quotedList(w.Have))
	case ParityConnectors:
		return fmt.Sprintf("claude.ai connectors are on for %s but disabled for %s",
			quotedList(w.Have), quotedList(w.Lack))
	case ParityConnectorsUnknown:
		return fmt.Sprintf("the claude.ai connector setting could not be read for %s", quotedList(w.Lack))
	case parityKindUnset:
		return "internal error: parity warning with no kind"
	default:
		return fmt.Sprintf("internal error: parity warning with unknown kind %d", int(w.Kind))
	}
}

// agrees picks the verb form for a list of member labels.
func agrees(labels []string, one, many string) string {
	if len(labels) == 1 {
		return one
	}
	return many
}

// RenderParity formats the section for `atrium doctor` (empty string when no pool
// has anything to report).
//
// The hints are chosen by what the warnings actually contain rather than by one
// all-or-nothing test, because a report can hold both a real difference and an
// unanswered question and each needs its own remedy: "align the dirs" is a
// non-sequitur for a member nothing could be read from, and "check the file parses"
// is a non-sequitur for two dirs that were read and disagree.
//
// The drift hint states the consequence rather than the fact, because "these dirs
// differ" is a curiosity until it is spelled out as rotation placing sessions on a
// member that cannot do the work. It promises nothing Atrium does not do: routing
// deliberately does not consult capability — expect_account's boundary is that
// verification decides whether a chosen account may launch, never which one is
// chosen — so the remedy offered is the user's, not Atrium's.
func RenderParity(warns []ParityWarning) string {
	if len(warns) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Account pool parity:\n")
	var drifted, unanswered, ambient bool
	for _, w := range warns {
		fmt.Fprintf(&b, "  ⚠ pool %q: %s\n", w.Pool, w.line())
		switch w.Kind {
		case ParityMissing, ParityDivergent, ParityConnectors:
			drifted = true
		case ParityUnreadable, ParityUnmeasured, ParityConnectorsUnknown:
			unanswered = true
		case ParityNoConfigDir:
			ambient = true
		}
	}
	if drifted {
		b.WriteString("    → rotation picks whichever member is next; it does not consult capability,\n")
		b.WriteString("      so a session placed on a member that lacks one just quietly goes without.\n")
		b.WriteString("      Align the config dirs, or split the pool.\n")
	}
	if unanswered {
		b.WriteString("    → what could not be read is not evidence of parity. Check the dir exists\n")
		b.WriteString("      and that its settings.json and .claude.json are present and parse.\n")
	}
	if ambient {
		b.WriteString("    → give that member its own config_dir, or rotation cannot be told what it\n")
		b.WriteString("      is rotating between.\n")
	}
	return b.String()
}
