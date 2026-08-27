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
// half, on four axes: a plugin, a marketplace or an MCP server one member has and
// another does not, one they both have that points somewhere different, an MCP server
// one member can run while another cannot — whether because it is not configured there
// or because that member's deniedMcpServers blocks it — and a member whose own
// settings.json switches claude.ai connectors off while its siblings leave them on,
// which is the shape the incident above actually took.
//
// The section is silent when a pool is in parity, so it must never be silent when a
// member could not be read. Every unanswered question gets a line of its own —
// a whole dir that would not read, a single dimension whose file was absent, an
// account with no dir at all — because "we compared these and they agree" and "we
// could not look" are the two conclusions a reader would otherwise be unable to tell
// apart.

import (
	"fmt"
	"maps"
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
	// took no part in the comparison — and with only two members, there was no
	// comparison left to take part in, which is what Compared distinguishes. Never
	// evidence that the member lacks anything.
	ParityUnreadable
	// ParityUnmeasured means one dimension could not be compared for some members, so
	// it is unverified for the pool. Two things produce it, both on the MCP axis:
	// .claude.json is absent, so nothing records what the dir configures; or the dir
	// denies a server by command or URL, which claude enforces and this build cannot
	// restate as a name. Neither is the member reporting nothing — the second reports
	// a full server list and cannot say which of it survives.
	ParityUnmeasured
	// ParityMissing means a capability some members have and others lack.
	ParityMissing
	// ParityDivergent means every compared member has the capability under the same
	// name, but configured to point at different things.
	ParityDivergent
	// ParityConnectors means claude.ai connectors are enabled for some members only.
	ParityConnectors
	// ParityConnectorsUnknown means no connector state was recorded for a member at
	// all. ReadDirCapabilities does not produce it: a disableClaudeAiConnectors value
	// claude rejects is fatal to the dir's whole settings.json and comes back as
	// ParityUnreadable instead. It stays because ConnectorsUnknown is the zero value,
	// and a DirCapabilities nobody filled in must render as an obvious fault rather
	// than as "connectors on".
	ParityConnectorsUnknown
)

// parityKindLast is the highest real kind, and it is what the exhaustiveness test
// ranges to instead of a hand-written list, so a new kind is covered without anyone
// remembering to add it.
//
// Declared OUTSIDE the block above for the reason config's dimensionLast is: inside
// it, a const appended after this line with no value of its own would repeat this
// expression rather than continue the iota, and come out equal to its neighbour.
//
// TestEveryParityKindRendersALineAndAHint is what it buys. RenderParity chooses a
// remedy from a switch with no default, so a kind added with a line() case and no
// hint arm printed a warning with no fix under it and the suite stayed green.
const parityKindLast = ParityConnectorsUnknown

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
	// Compared lists the members that WERE compared on this axis, and is set for the
	// kinds that report a member being left out: ParityUnmeasured and ParityUnreadable.
	// It is what separates "this member was left out of a comparison that still
	// happened" from "nothing was compared at all" — one sentence claimed the latter in
	// both cases, and with three or more members it printed "nothing was compared"
	// directly above the comparison it denied.
	Compared []string
	// Groups is set only for ParityDivergent: the compared members partitioned into
	// the sets that agree with each other, each set in config order, the sets in the
	// order their first member appears. Two groups is the ordinary case and names the
	// odd member out, which one flat list of every compared member could not — a name
	// two of three members configure identically rendered as all three differing.
	Groups [][]string
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
// Membership comes from config.PoolMembers, and NOT from a local scan for accounts
// whose Pool field matches this name. The difference is load-bearing: PoolMembers also
// counts an account with no pool of its own whose NAME is another account's pool, so
// `{name: work}` plus `{name: work-alt, pool: work}` is a two-member rotation. A local
// scan skipped the first of those for having an empty Pool, left one member, and went
// silent on a shape where rotation was live and drifted.
//
// Rotation reaches that pair through PoolMembers on some routes and not others:
// ResolveClaudePool calls it only when the MATCHED account carries a pool, and
// returns a singleton when the matched account is the one whose bare name the pool is
// named after. Reporting on the pair regardless is deliberate — this section is asked
// whether the members are substitutes, not which route happens to be live.
func CheckParity(cfg *config.Config, read config.CapabilityReadFunc) []ParityWarning {
	if cfg == nil || read == nil {
		return nil
	}

	pools := poolNames(cfg)

	// One cache for the whole report: two accounts naming one directory cost a single
	// read and, more to the point, cannot be made to disagree with each other by a
	// file rewritten between two of them. That pair is not hypothetical — it is the
	// configuration CheckPools flags, which resolves membership and cleans the dir the
	// same way this does.
	cached := cachedByDir(read)

	var warns []ParityWarning
	for _, pool := range pools {
		accounts := cfg.PoolMembers(pool)
		if len(accounts) < 2 {
			continue
		}
		labels := memberLabels(accounts)
		var members []parityMember
		var leftOut []int
		poolWarns := []ParityWarning{}
		for i, a := range accounts {
			dir := a.NormalizedConfigDir()
			if dir == "" {
				// An inherit-env account has no dir to be wrong about, which is why
				// the identity section skips it outright. Here it still needs saying:
				// a pool one of whose members rides the ambient env is a pool whose
				// interchangeability cannot be established at all.
				poolWarns = append(poolWarns, ParityWarning{
					Pool: pool, Kind: ParityNoConfigDir, Lack: []string{labels[i]},
				})
				continue
			}
			caps, ok := cached(dir)
			if !ok {
				poolWarns = append(poolWarns, ParityWarning{
					Pool: pool, Kind: ParityUnreadable, Dir: dir, Lack: []string{labels[i]},
				})
				leftOut = append(leftOut, len(poolWarns)-1)
				continue
			}
			members = append(members, parityMember{label: labels[i], caps: caps})
		}
		// Whether a comparison happened is only known once every member has been
		// tried, and the lines saying a member was left out are written before that. So
		// they are filled in on a second pass rather than asserting a comparison this
		// loop cannot yet have seen: with two members and one unreadable, diffPool
		// returns nothing and "it was left out of the comparison" named a comparison
		// that never ran.
		if compared := labelsOf(members); len(compared) >= 2 {
			for _, i := range leftOut {
				poolWarns[i].Compared = compared
			}
		}
		warns = append(warns, poolWarns...)
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
//
// The dir does not always settle it. Two entries can share a name AND a dir — which
// is precisely the shape CheckPools flags, so this section is expected to meet it —
// and two blank-named members riding the ambient env collide the same way. Qualifying
// by dir alone reproduced the defect it was added to fix, one step further in:
// `"work at /d" and "work at /d" have it`. Whatever is left identical after that is
// separated by its position in the pool, which is also what the user needs to find
// the entry in config.json.
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
	return positionallyUnique(labels)
}

// positionallyUnique appends a pool position to any label that is still not unique,
// so no rendered list can name one member twice and mean two.
func positionallyUnique(labels []string) []string {
	count := map[string]int{}
	for _, l := range labels {
		count[l]++
	}
	for i, l := range labels {
		if count[l] > 1 {
			labels[i] = fmt.Sprintf("%s, pool entry %d", l, i+1)
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
// per-dimension line for a single member would repeat it once per dimension.
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
			Pool: pool, Kind: ParityUnmeasured, Dimension: dim,
			Lack: unmeasured, Compared: labelsOf(measured),
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
	for _, name := range slices.Sorted(maps.Keys(names)) {
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
		}
		// Both can be true of one name, which is why this is not an else. Two dirs
		// can fail to be substitutes without either of them lacking the name: the
		// same marketplace name pointing at a different repo, or the same MCP server
		// name pointing at a different URL or command, is a member that cannot do the
		// work while looking identical by name. Skipping this whenever a THIRD member
		// merely lacked the name deleted that difference from the report — and the
		// ParityMissing line that survived listed the two divergent members side by
		// side as the ones that have it, which reads as agreement. A member that
		// lacks the name has no target, so targetGroups leaves it out on its own.
		if groups := targetGroups(dim, name, measured); len(groups) > 1 {
			warns = append(warns, ParityWarning{
				Pool: pool, Kind: ParityDivergent, Dimension: dim,
				Feature: name, Have: have, Groups: groups,
			})
		}
	}
	return warns
}

// targetGroups partitions the members that have a comparable target for name into the
// sets that agree with each other. One group (or none) means nothing diverges; more
// than one IS the divergence, and naming the groups is what tells the reader which
// member is the odd one out.
//
// A member carrying no comparable target is left OUT of the partition rather than
// answering for the pool: enabledPlugins maps a name to a bool, so a bare true has
// nothing to point at, and a value that could not be canonicalised is not evidence of
// a difference either. It is left out rather than vetoing the comparison. Returning
// "no divergence" on the first such member kept the answer order-independent, but did
// it by letting one permissive member DELETE a difference the others really had: two
// dirs pinning a plugin to different versions went silent as soon as a third enabled
// it with `true`, in the one section whose contract is that silence means agreement.
func targetGroups(dim config.Dimension, name string, members []parityMember) [][]string {
	var order []string
	byTarget := map[string][]string{}
	for _, m := range members {
		target := m.caps.State(dim).Target(name)
		if target == "" {
			continue
		}
		if _, seen := byTarget[target]; !seen {
			order = append(order, target)
		}
		byTarget[target] = append(byTarget[target], m.label)
	}
	groups := make([][]string, 0, len(order))
	for _, target := range order {
		groups = append(groups, byTarget[target])
	}
	return groups
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

// groupList renders a divergence as `"a" and "b" vs "c"`: the members that agree with
// each other, and then the ones that do not. One undifferentiated list of every
// compared member named three dirs when only one was wrong, and the remedy under it
// then pointed at all three.
func groupList(groups [][]string) string {
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = quotedList(g)
	}
	return strings.Join(out, " vs ")
}

// labelsOf is the members' labels in config order.
func labelsOf(members []parityMember) []string {
	out := make([]string, len(members))
	for i, m := range members {
		out[i] = m.label
	}
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
		if len(w.Compared) >= 2 {
			return fmt.Sprintf("capabilities unreadable for %s (%s) — it was left out, and %s were compared without it",
				quotedList(w.Lack), w.Dir, quotedList(w.Compared))
		}
		return fmt.Sprintf("capabilities unreadable for %s (%s), so nothing was compared",
			quotedList(w.Lack), w.Dir)
	case ParityUnmeasured:
		// "could not be compared on it" rather than "does not report one": a member
		// whose denial this build cannot name reports its full server list and simply
		// cannot say which of it survives, and telling that reader the dir reports
		// nothing sent them looking for a file that is present and parsing.
		if len(w.Compared) >= 2 {
			return fmt.Sprintf("%s parity is unverified: %s could not be compared on it, and %s %s compared without %s",
				w.Dimension.Noun(), quotedList(w.Lack), quotedList(w.Compared),
				plural(len(w.Compared), "was", "were"), plural(len(w.Lack), "it", "them"))
		}
		return fmt.Sprintf("%s parity is unverified: %s could not be compared on it, so nothing was compared",
			w.Dimension.Noun(), quotedList(w.Lack))
	case ParityMissing:
		return fmt.Sprintf("%s %q: %s %s it, %s %s not",
			w.Dimension.Noun(), w.Feature,
			quotedList(w.Have), plural(len(w.Have), "has", "have"),
			quotedList(w.Lack), plural(len(w.Lack), "does", "do"))
	case ParityDivergent:
		return fmt.Sprintf("%s %q is configured differently — same name, different target: %s",
			w.Dimension.Noun(), w.Feature, groupList(w.Groups))
	case ParityConnectors:
		// Stated as what the dirs DECLARE, not as the state in force.
		// disableClaudeAiConnectors is any-source-true — "a project can opt out, but a
		// project-level false cannot override a user-level true" — so a project or
		// managed source can switch connectors off for every member here, and only
		// the dir's own setting is knowable from a config dir.
		return fmt.Sprintf("%s %s claude.ai connectors in %s own settings.json and %s %s not",
			quotedList(w.Lack), plural(len(w.Lack), "disables", "disable"),
			plural(len(w.Lack), "its", "their"),
			quotedList(w.Have), plural(len(w.Have), "does", "do"))
	case ParityConnectorsUnknown:
		return fmt.Sprintf("no claude.ai connector state was recorded for %s", quotedList(w.Lack))
	case parityKindUnset:
		return "internal error: parity warning with no kind"
	default:
		return fmt.Sprintf("internal error: parity warning with unknown kind %d", int(w.Kind))
	}
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
// The two unanswered causes are split for the same reason. A dir that would not open
// and an axis this build could not compare are different problems, and one hint
// telling the reader to check that the files "are present and parse" was the wrong
// diagnosis for the second.
//
// The second hint names both of ITS causes, which the kind's own doc lists and an
// earlier wording did not: an axis goes unmeasured when .claude.json is absent, as it
// is in any dir configured but never logged into, and ALSO when the dir carries an MCP
// denial keyed on a command or a URL. Telling that reader to "check the value of the
// named setting" named a setting that was not there, in a line that names no setting
// either; telling them a value was "held in a shape this build does not compare" was
// the wrong diagnosis for the denial case, whose settings.json parses and whose value
// claude and this build both read fine.
//
// ParityConnectorsUnknown gets a remedy of its own rather than sharing that one,
// because it no longer shares a cause with it: a rejected connector value costs the
// dir its whole settings.json and arrives as ParityUnreadable, so the kind now means
// only that a DirCapabilities was never filled in. Under the shared hint it sent the
// reader to look for an absent file for a dir that has one.
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
	var drifted, unreadable, unrecognised, connectorsUnknown, ambient bool
	for _, w := range warns {
		fmt.Fprintf(&b, "  ⚠ pool %q: %s\n", w.Pool, w.line())
		switch w.Kind {
		case ParityMissing, ParityDivergent, ParityConnectors:
			drifted = true
		case ParityUnreadable:
			unreadable = true
		case ParityUnmeasured:
			unrecognised = true
		case ParityConnectorsUnknown:
			connectorsUnknown = true
		case ParityNoConfigDir:
			ambient = true
		}
	}
	if drifted {
		b.WriteString("    → rotation picks whichever member is next; it does not consult capability,\n")
		b.WriteString("      so a session placed on a member that lacks one just quietly goes without.\n")
		b.WriteString("      Align the config dirs, or split the pool.\n")
	}
	if unreadable {
		b.WriteString("    → what could not be read is not evidence of parity. Check the dir exists,\n")
		b.WriteString("      is an absolute path, and that its settings.json and .claude.json parse.\n")
		b.WriteString("      A dir claude was never run in has neither file: set one up with\n")
		b.WriteString("      `CLAUDE_CONFIG_DIR=<dir> claude`, then /login inside it.\n")
		b.WriteString("      A settings.json claude itself rejects lands here too, and claude is\n")
		b.WriteString("      throwing the whole file away: `CLAUDE_CONFIG_DIR=<dir> claude doctor`\n")
		b.WriteString("      names the offending key under \"Invalid settings\".\n")
	}
	if unrecognised {
		b.WriteString("    → what could not be compared is not evidence of parity. Either the file\n")
		b.WriteString("      that records that axis is absent — .claude.json is, in a dir configured\n")
		b.WriteString("      but never logged into — or that dir denies an MCP server by command or\n")
		b.WriteString("      URL, which claude enforces and this cannot restate as a server name.\n")
	}
	if connectorsUnknown {
		b.WriteString("    → what could not be read is not evidence of parity. No connector state at\n")
		b.WriteString("      all was recorded for that member, which a normal read does not produce:\n")
		b.WriteString("      a setting claude rejects is reported as an unreadable dir instead.\n")
		b.WriteString("      Re-run, and report it if it persists.\n")
	}
	if ambient {
		b.WriteString("    → give that member its own config_dir, or rotation cannot be told what it\n")
		b.WriteString("      is rotating between.\n")
	}
	return b.String()
}
