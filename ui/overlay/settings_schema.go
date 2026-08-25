package overlay

import (
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/keys"
	"github.com/ZviBaratz/atrium/ui/theme"
)

// settingCategory identifies which section (PR A) and rail entry (PR B) a settings
// row belongs to. It is a closed vocabulary rendered from allCategories(), so adding
// a category is one deliberate edit — unlike the free-string `section` it replaces,
// where a typo silently created an eleventh section of one row.
type settingCategory int

const (
	catSessions settingCategory = iota
	catWorktrees
	catAppearance
	catSessionList
	catNotifications
	catAutomation
	catInput
	catProjects
	catUpdates
	catAdvanced
)

// allCategories returns every category in rail/section display order. It is the
// single ordered source: the renderer walks it rather than deriving order from the
// row declarations, so a row's position in newSettingRows cannot reorder sections.
func allCategories() []settingCategory {
	return []settingCategory{
		catSessions,
		catWorktrees,
		catAppearance,
		catSessionList,
		catNotifications,
		catAutomation,
		catInput,
		catProjects,
		catUpdates,
		catAdvanced,
	}
}

// label returns the category's section/rail label.
func (c settingCategory) label() string {
	switch c {
	case catSessions:
		return "Sessions"
	case catWorktrees:
		return "Worktrees & git"
	case catAppearance:
		return "Appearance"
	case catSessionList:
		return "Session list"
	case catNotifications:
		return "Notifications"
	case catAutomation:
		return "Automation"
	case catInput:
		return "Input"
	case catProjects:
		return "Projects"
	case catUpdates:
		return "Updates"
	case catAdvanced:
		return "Advanced"
	}
	return ""
}

// applyTiming says when a change to a setting takes effect. It is a closed enum with
// two projections, so the two renderers cannot drift: footerNote() is the prose the
// single-column footer appends after "·" (empty for live, which is most rows — saying
// "live" 25 times would be noise), and badge() is the right-aligned per-row chip the
// two-pane renderer adds in PR B.
//
// It deliberately has no "modifies your local branch" member. That was one of the old
// free-text applyNote's four values, and it is a caution rather than a timing, so it
// moved to the settingRow.caution field the footer renders alongside this note —
// keeping this enum about scheduling without dropping the warning (spec §5).
type applyTiming int

const (
	timingLive applyTiming = iota
	timingNewSessions
	timingRestart
)

// footerNote returns the prose the single-column footer appends, or "" for a change
// that applies immediately.
func (t applyTiming) footerNote() string {
	switch t {
	case timingNewSessions:
		return "affects new sessions"
	case timingRestart:
		return "applies on restart"
	}
	return ""
}

// badge returns the short right-aligned chip text for the two-pane renderer.
func (t applyTiming) badge() string {
	switch t {
	case timingNewSessions:
		return "new sessions"
	case timingRestart:
		return "restart"
	}
	return "live"
}

// settingScope says where a row's value can come from. It was the seam left for a
// per-repo override layer (#477) and per-session settings (#454); #815 made the
// first half real, so it now has two members and the renderer reads it.
//
// The rule the seam was left under still holds: nothing may special-case
// scopeGlobal (spec §5). A row is described by its scope, and the provenance a
// scoped row shows comes from a fact the panel is TOLD (SetRepoLayer) rather than
// one it derives — the render path may not touch the filesystem, and the panel has
// no repo of its own to look at.
type settingScope int

const (
	// scopeGlobal: config.json is the only source. Every row but the seed lists.
	scopeGlobal settingScope = iota
	// scopeRepoLayered: a repository's own trusted .atrium.json can add to this
	// key for sessions in that repo (#815). The row still edits the global value —
	// the layer is a union, not a replacement, so the value here remains the value
	// for every repo — and it renders which repo is adding to it. Exactly the keys
	// repocfg.RepoLocalLayerKeys names, held to that by
	// TestRepoLayeredRowsMatchTheRepoLocalSchema.
	scopeRepoLayered
)

// allScopes is the ordered, complete member list, so a new scope cannot be added
// without a label (TestEveryScopeHasALabel), the same completeness discipline
// allCategories carries.
func allScopes() []settingScope {
	return []settingScope{scopeGlobal, scopeRepoLayered}
}

// label names the scope in help text.
func (s settingScope) label() string {
	switch s {
	case scopeRepoLayered:
		return "global, plus what a trusted repo adds"
	default:
		return "global"
	}
}

// settingRow declares one editable config field. The panel is driven entirely by
// this schema, so exposing a new Config field is a matter of appending a row — the
// navigation, editing, and rendering are generic.
//
// Rows are presentational + value plumbing only: set mutates the Config (with
// validation), but persisting to disk and live-applying side effects (theme repaint,
// tmux conf re-render) are the home model's job, keyed off the row's key (see
// app.applySettingChange).
type settingRow struct {
	key      string          // stable identifier home switches on for live-apply
	category settingCategory // section (PR A) / rail entry (PR B)
	label    string
	kind     settingKind
	scope    settingScope // where the value can come from; see settingScope

	// summary is the one-line help shown whenever the row is selected. It is capped
	// at 74 cells so it never wraps at the 80-column floor (TestSummaryFitsOneLine).
	summary string
	// detail is the optional long-form help: the value grammar, cautions, and
	// cross-references that used to be crammed into one description. It is rendered
	// behind `?` (expandedHelpContent), and its first sentence is the help pane's
	// fallback for a row whose current value has no gloss.
	detail string
	timing applyTiming // when a change takes effect
	// caution is a short warning the footer appends after "·", for a setting whose
	// effect reaches somewhere the user would not expect. It is deliberately separate
	// from both timing (a caution is not a schedule) and detail (which is only visible
	// behind `?`, so a caution parked there would wait to be asked for). Keep it to a
	// clause.
	caution string

	get func(c *config.Config) string // display value
	// editGet returns the raw value to prefill the inline editor with; nil
	// means use get. Needed where display and raw differ (e.g. "unlimited").
	editGet func(c *config.Config) string
	set     func(c *config.Config, v string) error
	options func(c *config.Config) []string // enum rows only
	// gloss explains each enum option in one line, keyed by option value. It is what
	// dissolves the 300-443-char run-on descriptions: the option semantics move out
	// of the prose and onto the options themselves. Enum rows only, and only where the
	// vocabulary is fixed and non-obvious (see glossExemptRows).
	gloss map[string]string

	// defaultDisplay returns the display string of the built-in default, for the
	// "changed from default" marker. The renderer calls it per row per frame, so it
	// MUST stay pure — no exec, no filesystem — and in particular must never call
	// config.DefaultConfig(), which resolves the OS user to derive branch_prefix.
	//
	// A nil defaultDisplay means the row has no fixed default to diverge from and is
	// never marked modified. Exactly two rows are nil by design: default_program
	// (defaults to the first *detected* agent profile — see config.seededDefaultConfig,
	// which is what every LoadConfig fallback actually returns) and branch_prefix
	// (defaults to the OS username). A marker on either would be a lie — do not
	// "fix" this.
	defaultDisplay func() string
	// reset restores the built-in default. nil for kindReadOnly and for the two rows
	// with no fixed default.
	reset func(c *config.Config)
	// activeWhen reports whether changing the row currently has any effect. nil means
	// always active. An inert row is dimmed and carries a reason chip while staying
	// fully editable — a user may configure ahead of enabling the parent (spec §5).
	activeWhen func(c *config.Config) bool
	// modifiedWhen gates the "changed from default" marker for a row whose displayed
	// value a SIBLING field can move. nil means the plain value comparison decides.
	//
	// The comparison alone answers "does the effective value differ from the default",
	// which is the wrong question wherever the user did not cause the difference:
	// context_warn_percent collapses onto a lower context_danger_percent, so an untouched
	// warn row would show the marker and then not clear it, because its reset — which
	// clears a field that is already clear — genuinely has nothing to do.
	modifiedWhen func(c *config.Config) bool
	// resetChanges reports whether pressing r would actually change stored state. nil
	// means resetRow's own before/after comparison of get decides.
	//
	// It exists because that comparison reads the EFFECTIVE value, which for a cadence
	// row is the default whenever the stored value equals it — so clearing a field
	// storing exactly the default looked like a no-op, resetRow reported "" and the
	// save never ran. The marker cleared on screen while config.json kept the value,
	// and the next launch brought both back.
	resetChanges func(c *config.Config) bool
}

// withModifiedWhen gates a row's "changed from default" marker on the user having set the
// field, for a row whose value a sibling can move. See settingRow.modifiedWhen.
func withModifiedWhen(r settingRow, modifiedWhen func(c *config.Config) bool) settingRow {
	r.modifiedWhen = modifiedWhen
	return r
}

// withResetHook runs after a row's own reset, for a row whose default can invalidate a
// SIBLING field's stored value. Reset is the one mutation path that does not go through
// settingRow.set, so a cross-field rule enforced only by a validator is not enforced here
// — which is exactly how a reset can re-create the state its validator refuses.
func withResetHook(r settingRow, after func(c *config.Config)) settingRow {
	inner := r.reset
	r.reset = func(c *config.Config) {
		inner(c)
		after(c)
	}
	return r
}

// footerText composes the row's single-column footer help: the summary, then any
// caution, then any timing note, each after a "·". It is the one place that ordering
// lives, so the width guards in settings_test.go measure what the renderer actually
// emits rather than a second copy of this composition that could drift from it.
func (r settingRow) footerText() string {
	desc := r.summary
	// The caution precedes the timing note: it qualifies what the setting does, which
	// reads ahead of when the change lands.
	for _, note := range []string{r.caution, r.timing.footerNote()} {
		if note != "" {
			desc += " · " + note
		}
	}
	return desc
}

// boolRow builds a kindBool row over a getter and a setter; get displays
// "on"/"off" and set accepts the same strings (the toggle handler flips them).
// Its defaultDisplay is derived from the accessor's own default, which callers pass
// as defaultOn, so a bool row cannot disagree with its accessor about the default.
func boolRow(key string, category settingCategory, label, summary, detail string, timing applyTiming, defaultOn bool, get func(c *config.Config) bool, set func(c *config.Config, v bool)) settingRow {
	display := func(on bool) string {
		if on {
			return "on"
		}
		return "off"
	}
	return settingRow{
		key: key, category: category, label: label, kind: kindBool,
		summary: summary, detail: detail, timing: timing, scope: scopeGlobal,
		get: func(c *config.Config) string { return display(get(c)) },
		set: func(c *config.Config, v string) error {
			set(c, v == "on")
			return nil
		},
		defaultDisplay: func() string { return display(defaultOn) },
		reset:          func(c *config.Config) { set(c, defaultOn) },
	}
}

// cadenceNote is the one-clause reminder, repeated across the four exposed cadence
// knobs (#799), that the boundary around them is deliberate. Verbatim in each of their
// details rather than paraphrased five ways, so a reader who opens two of them learns
// the rule once and a grep finds every site.
const cadenceNote = "One of four exposed cadences; the rest are tuned for correctness, not taste."

// cadenceRow builds a kindInt row over a clamping config accessor: get reports the
// EFFECTIVE value (so the panel shows what is in force, never a stored value the
// accessor would rewrite), set stores a pointer, and reset clears it back to nil.
//
// field is one closure rather than a set/isSet pair because reading and writing the
// pointer must never disagree about which field the row owns: resetChanges asks whether
// anything is stored, and answering that from a different closure than the one reset
// clears is a bug nothing here would catch.
//
// lo/hi are the accessor's own bounds, and the row REFUSES a value outside them rather
// than echoing back a number the accessor would silently clamp — the same contract
// MaxProjectSearchDepth exists for. validate is an optional extra predicate for a row
// whose bound depends on another field; nil where the range is the whole rule. Note that
// validate guards set alone: a cross-field rule that reset can also break needs a
// withResetHook as well.
func cadenceRow(key string, category settingCategory, label, summary, detail string,
	def, lo, hi int,
	get func(c *config.Config) int,
	field func(c *config.Config) **int,
	validate func(c *config.Config, n int) error,
) settingRow {
	value := func(c *config.Config) string { return strconv.Itoa(get(c)) }
	return settingRow{
		key: key, category: category, label: label, kind: kindInt,
		scope: scopeGlobal, timing: timingLive,
		summary: summary, detail: detail,
		get:            value,
		defaultDisplay: func() string { return strconv.Itoa(def) },
		reset:          func(c *config.Config) { *field(c) = nil },
		resetChanges:   func(c *config.Config) bool { return *field(c) != nil },
		set: func(c *config.Config, v string) error {
			v = strings.TrimSpace(v)
			if v == "" {
				// Emptying the box clears the field, matching max_sessions and
				// project_search_depth. Unlike those two there is no editGet: they render
				// unset as "" and so must edit as "", while a cadence row shows the number
				// in force, and prefilling the editor with it is worth more than making
				// blank the round trip.
				*field(c) = nil
				return nil
			}
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("%s must be a whole number (empty restores the default)", label)
			}
			if n < lo || n > hi {
				return fmt.Errorf("%s must be between %d and %d", label, lo, hi)
			}
			if validate != nil {
				if err := validate(c, n); err != nil {
					return err
				}
			}
			*field(c) = &n
			return nil
		},
	}
}

// Placeholder value displays, shared between a row's get and its defaultDisplay so
// the two cannot disagree about what an unset field looks like. The panel shows these
// in the value column where the stored value is empty but a default is in force.
const (
	displayNone     = "(none)"     // an empty list
	displayBuiltIn  = "(built-in)" // notify_command falls back to a per-OS notifier
	displayManaged  = "(managed)"  // tmux_config_override falls back to Atrium's conf
	displayUnresolv = "(unresolved)"
)

// displayList renders a list-valued row's value: the joined entries, or displayNone
// when the list is empty. Both get and defaultDisplay route through it so an empty
// default renders identically to an emptied value.
func displayList(items []string) string {
	if len(items) == 0 {
		return displayNone
	}
	return strings.Join(items, ", ")
}

// groupModeOnOff projects the stored group_mode vocabulary (repo/account) onto the
// plain on/off the row presents. Both get and defaultDisplay route through it, so the
// row's value and its advertised default cannot disagree about which mode is "off".
func groupModeOnOff(c *config.Config) string {
	if c.GetGroupMode() == config.GroupModeAccount {
		return "on"
	}
	return "off"
}

// withActiveWhen attaches an inert predicate to an already-built row. boolRow's
// signature is already long, and only two of the fifteen bool rows have a parent to
// follow, so those two are decorated here rather than every call site growing a nil.
func withActiveWhen(r settingRow, activeWhen func(c *config.Config) bool) settingRow {
	r.activeWhen = activeWhen
	return r
}

// withCaution attaches a footer warning to an already-built row, for the same reason
// withActiveWhen exists: only one row needs it, so boolRow's signature does not grow.
func withCaution(r settingRow, caution string) settingRow {
	r.caution = caution
	return r
}

// configFilePath returns the resolved config.json path shown by the read-only Config
// file row, degrading to a legible placeholder rather than an empty cell when the home
// directory cannot be determined.
//
// It resolves once (GetConfigDir stats the filesystem, and the render path should not
// do that per frame) but resolves *lazily*, on first render. That distinction is
// load-bearing for tests, not just style: a package-level var initializer runs before
// TestMain, so it would capture the developer's real HOME no matter what the suite
// sandboxes, and no TestMain could fix it (CLAUDE.md: "Tests must never read or write
// the user's real data dir"). Pinned by TestConfigFilePathHonoursSandboxedHome.
var configFilePath = sync.OnceValue(func() string {
	dir, err := config.GetConfigDir()
	if err != nil {
		return displayUnresolv
	}
	return filepath.Join(dir, "config.json")
})

// newSettingRows declares the panel contents in display order, grouped by category in
// allCategories() order. Section headers are derived at render time from consecutive
// rows sharing a category.
func newSettingRows(cfg *config.Config) []settingRow {
	// Captured at panel open: a hand-edited config may hold a raw command in
	// default_program rather than a profile name (GetProgram passes it through
	// as-is). The enum's options must keep offering that original value even
	// after cycling overwrites it in the live config — otherwise the first
	// ←/→/enter press would persist a profile name over it and the raw command
	// would be irrecoverable.
	rawDefaultProgram := cfg.DefaultProgram

	// The same capture for the theme row, and for the same reason with a different cause.
	// A configured user theme stops being registered whenever its file is absent —
	// mid-edit, on a machine the dotfiles have not reached, or newly refused because an
	// upgrade retuned the base palette it extends — and theme.SelectableNames() then does
	// not offer the name. Without this the first ←/→ press would select `auto` (opts[0]),
	// applySettingChange persists before anything else, and `esc` moves focus to the rail
	// rather than cancelling: the name is gone and has to be retyped from memory.
	rawTheme := cfg.GetTheme()

	return []settingRow{
		// ── Sessions ──────────────────────────────────────────────────────────
		{
			key: "default_program", category: catSessions, label: "Default program", kind: kindEnum,
			scope:   scopeGlobal,
			timing:  timingNewSessions,
			summary: "Agent command new sessions launch. A profile name, or a raw command.",
			detail: "A name matching a profile launches that profile's command; anything else " +
				"is passed to the shell as written. Edit the profile list under Profiles.",
			get: func(c *config.Config) string { return c.DefaultProgram },
			set: func(c *config.Config, v string) error {
				c.DefaultProgram = v
				return nil
			},
			// Walk the declared profile order, not GetProfiles(): that helper
			// reorders the default first, which would make cycling ping-pong
			// between the first two profiles and never reach the rest.
			options: func(c *config.Config) []string {
				if len(c.Profiles) == 0 {
					return []string{c.DefaultProgram}
				}
				names := make([]string, len(c.Profiles))
				for i, p := range c.Profiles {
					names[i] = p.Name
				}
				// Keep the captured raw value (see newSettingRows) as a cycle
				// option so touching the row can never silently destroy it —
				// cycling must always be able to return.
				if !slices.Contains(names, rawDefaultProgram) {
					names = append([]string{rawDefaultProgram}, names...)
				}
				return names
			},
		},
		{
			key: "max_sessions", category: catSessions, label: "Session limit", kind: kindInt,
			scope:          scopeGlobal,
			timing:         timingLive,
			defaultDisplay: func() string { return fmt.Sprintf("auto (%d)", config.DefaultSessionCap()) },
			reset:          func(c *config.Config) { c.MaxSessions = nil },
			summary:        "How many sessions Atrium will hold. Empty auto-derives from this host.",
			detail: "Empty is a soft cap of half your CPU threads (minimum 2), counting only " +
				"live sessions — a create or resume past it asks for confirmation rather than " +
				"refusing, and a startup that would relaunch past it leaves the overflow paused " +
				"for you to resume. A number is a hard cap on every session, paused ones " +
				"included, and a create past it is refused. 0 means unlimited, with no " +
				"confirmation. `atrium doctor` reports the same host capacity.",
			get: func(c *config.Config) string {
				switch {
				case c.MaxSessions == nil:
					return fmt.Sprintf("auto (%d)", config.DefaultSessionCap())
				case *c.MaxSessions < 1:
					return "unlimited"
				default:
					return strconv.Itoa(*c.MaxSessions)
				}
			},
			editGet: func(c *config.Config) string {
				switch {
				case c.MaxSessions == nil:
					return "" // empty selects the host-derived auto default
				case *c.MaxSessions < 1:
					return "0" // explicit unlimited edits as 0
				default:
					return strconv.Itoa(*c.MaxSessions)
				}
			},
			set: func(c *config.Config, v string) error {
				v = strings.TrimSpace(v)
				if v == "" {
					c.MaxSessions = nil // auto (host-derived)
					return nil
				}
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return fmt.Errorf("max sessions must be a non-negative number (0 = unlimited, empty = auto)")
				}
				c.MaxSessions = &n // 0 = explicit unlimited; positive = hard cap
				return nil
			},
		},
		boolRow("auto_attach", catSessions, "Attach on create",
			"Drop straight into a new session's pane as soon as it starts.",
			"",
			timingLive, true,
			(*config.Config).GetAutoAttach,
			func(c *config.Config, v bool) { c.AutoAttach = &v }),
		boolRow("session_context_bar", catSessions, "In-session status bar",
			"Thin tmux status line inside attached sessions.",
			"Sessions already running keep the status line they started with; tmux only "+
				"reads its config when a server starts.",
			timingNewSessions, true,
			(*config.Config).GetSessionContextBar,
			func(c *config.Config, v bool) { c.SessionContextBar = &v }),

		// ── Worktrees & git ───────────────────────────────────────────────────
		{
			key: "branch_prefix", category: catWorktrees, label: "Branch prefix", kind: kindText,
			scope:   scopeGlobal,
			timing:  timingNewSessions,
			summary: "Prefix for branches Atrium creates, e.g. zvi/ makes zvi/my-feature.",
			get:     func(c *config.Config) string { return c.BranchPrefix },
			set: func(c *config.Config, v string) error {
				c.BranchPrefix = strings.TrimSpace(v)
				return nil
			},
		},
		boolRow("update_base_on_create", catWorktrees, "Update base on create",
			"Branch new sessions off the freshest remote tip, not a stale local copy.",
			"",
			timingNewSessions, true,
			(*config.Config).GetUpdateBaseOnCreate,
			func(c *config.Config, v bool) { c.UpdateBaseOnCreate = &v }),
		withCaution(
			withActiveWhen(boolRow("fast_forward_local_base", catWorktrees, "Fast-forward local base",
				"Also advance your own local base branch to origin during create.",
				"This is the one setting here that writes outside a session worktree — it moves "+
					"your local branch. Clean fast-forward only: a diverged local base is left alone.",
				timingNewSessions, false,
				(*config.Config).GetFastForwardLocalBase,
				func(c *config.Config, v bool) { c.FastForwardLocalBase = &v }),
				// nothing to fast-forward if the base is not refreshed in the first place
				(*config.Config).GetUpdateBaseOnCreate),
			// The literal #168 shipped in applyNote. This is the one row whose effect
			// escapes the session worktree, so the warning has to be on the surface PR A
			// renders — detail carries the fuller version for PR B.
			"modifies your local branch"),
		{
			key: "carry_files", category: catWorktrees, label: "Carry files", kind: kindText,
			scope:          scopeRepoLayered,
			timing:         timingNewSessions,
			defaultDisplay: func() string { return displayList((&config.Config{}).GetCarryFiles()) },
			reset:          func(c *config.Config) { c.CarryFiles = nil },
			summary:        "Gitignored files copied into each new worktree.",
			detail: "Comma-separated repo-relative paths. Copies, so later edits in a worktree " +
				"do not travel back. An empty list is an explicit opt-out, not a fall back to " +
				"the default `.claude/settings.local.json`. A repository you have trusted can " +
				"ADD to this list for its own sessions by committing a `.atrium.json`; your " +
				"entries are never replaced, and `atrium trust revoke` stops the repo's from being seeded into new worktrees.",
			get: func(c *config.Config) string { return displayList(c.GetCarryFiles()) },
			editGet: func(c *config.Config) string {
				return strings.Join(c.GetCarryFiles(), ", ")
			},
			set: func(c *config.Config, v string) error {
				// Split on commas, trim each entry, drop blanks. Empty or
				// all-blank input collapses to a non-nil empty slice — the
				// explicit opt-out per GetCarryFiles's nil-vs-empty contract.
				parts := strings.Split(v, ",")
				files := make([]string, 0, len(parts))
				for _, p := range parts {
					if t := strings.TrimSpace(p); t != "" {
						files = append(files, t)
					}
				}
				c.CarryFiles = files
				return nil
			},
		},
		{
			key: "link_paths", category: catWorktrees, label: "Link paths", kind: kindText,
			scope:          scopeRepoLayered,
			timing:         timingNewSessions,
			defaultDisplay: func() string { return displayList((&config.Config{}).GetLinkPaths()) },
			reset:          func(c *config.Config) { c.LinkPaths = nil },
			summary:        "Gitignored paths symlinked into each new worktree, e.g. node_modules.",
			detail: "Comma-separated repo-relative paths. A symlink, not a copy, so every " +
				"session shares one directory — unless the session was created with " +
				"Dependencies set to isolated, which gives it none of them. Ignore the path " +
				"with a pattern that has no trailing slash — with one, git does not treat the " +
				"symlink as ignored and it lands in pause commits. A repository you have " +
				"trusted can ADD to this list for its own sessions by committing a " +
				"`.atrium.json`; your entries are never replaced, and `atrium trust revoke` " +
				"stops the repo's from being seeded into new worktrees.",
			get: func(c *config.Config) string { return displayList(c.GetLinkPaths()) },
			editGet: func(c *config.Config) string {
				return strings.Join(c.GetLinkPaths(), ", ")
			},
			set: func(c *config.Config, v string) error {
				// Same split/trim/drop-blanks shape as carry_files, but empty input
				// clears the key to nil rather than storing an explicit empty list:
				// GetLinkPaths has no default, so nil and empty both mean off and nil
				// is the honest way to say "not configured".
				parts := strings.Split(v, ",")
				paths := make([]string, 0, len(parts))
				for _, p := range parts {
					if t := strings.TrimSpace(p); t != "" {
						paths = append(paths, t)
					}
				}
				if len(paths) == 0 {
					c.LinkPaths = nil
					return nil
				}
				c.LinkPaths = paths
				return nil
			},
		},
		boolRow("pr_create_draft", catWorktrees, "Create PRs as draft",
			"Open PRs as drafts. Turn off to merge them with "+keys.LabelOf(keys.KeyMerge)+" in-app.",
			"",
			timingLive, true,
			(*config.Config).GetPRCreateDraft,
			func(c *config.Config, v bool) { c.PRCreateDraft = &v }),

		// ── Appearance ────────────────────────────────────────────────────────
		{
			key: "theme", category: catAppearance, label: "Theme", kind: kindEnum,
			scope:          scopeGlobal,
			timing:         timingLive,
			defaultDisplay: func() string { return (&config.Config{}).GetTheme() },
			reset:          func(c *config.Config) { c.Theme = "" },
			summary:        "Colour palette and border style. `auto` follows the terminal background.",
			get:            func(c *config.Config) string { return c.GetTheme() },
			set: func(c *config.Config, v string) error {
				c.Theme = v
				return nil
			},
			// auto first, then the registry sorted — SelectableNames owns that order
			// so the picker's vocabulary and the theme package's cannot drift. Plus the
			// captured value if the registry has lost it, so cycling can always return;
			// appended at the END rather than the front, because `auto` leading this list
			// is what a reader of the summary above expects to find there.
			options: func(c *config.Config) []string {
				names := theme.SelectableNames()
				if !slices.Contains(names, rawTheme) {
					names = append(names, rawTheme)
				}
				return names
			},
		},
		{
			key: "splash", category: catAppearance, label: "Splash", kind: kindEnum,
			scope:          scopeGlobal,
			timing:         timingLive,
			defaultDisplay: func() string { return (&config.Config{}).GetSplash() },
			reset:          func(c *config.Config) { c.Splash = "" },
			summary:        "Animation behind the empty session list.",
			// The first sentence is the context line for every option this row leaves
			// unglossed — the five pattern names — so it has to hold for all of them
			// rather than describe the off rung alone
			// (TestDetailFallbackIsTrueOfEveryUnglossedOption).
			detail: "Patterns animate behind the idle session list; off leaves the plain wordmark. " +
				"Off also stops the repaint entirely. " +
				"The full-window screensaver is a separate, explicit keypress and still animates.",
			gloss: map[string]string{
				config.SplashRandom: "a different pattern each launch",
				config.SplashOff:    "no animation; the plain wordmark",
			},
			get: func(c *config.Config) string { return c.GetSplash() },
			set: func(c *config.Config, v string) error {
				c.Splash = v
				return nil
			},
			// Off sits last so random keeps the home position and the list reads
			// as patterns-then-none. It is not a member of SplashVariants: that
			// list is pinned against the engine's own generators (app's
			// TestSplashVocabularyAgrees), so a mode has to be appended here.
			options: func(c *config.Config) []string {
				return append(append([]string{config.SplashRandom}, config.SplashVariants()...), config.SplashOff)
			},
		},
		{
			key: "glyph_set", category: catAppearance, label: "Glyph set", kind: kindEnum,
			scope:          scopeGlobal,
			timing:         timingLive,
			defaultDisplay: func() string { return (&config.Config{}).GetGlyphSet() },
			reset:          func(c *config.Config) { c.GlyphSet = "" },
			summary:        "Icon fidelity. Drop a rung if you see boxes instead of icons.",
			gloss: map[string]string{
				config.GlyphSetNerd:  "vendor Nerd-Font icons; needs a patched font",
				config.GlyphSetPlain: "Unicode that renders on any font (the default)",
				config.GlyphSetASCII: "a 7-bit floor for terminals that show boxes even on plain",
			},
			get: func(c *config.Config) string { return c.GetGlyphSet() },
			set: func(c *config.Config, v string) error {
				c.GlyphSet = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.GlyphSetNerd, config.GlyphSetPlain, config.GlyphSetASCII}
			},
		},
		{
			key: "image_preview", category: catAppearance, label: "Image preview", kind: kindEnum,
			scope: scopeGlobal,
			// Live in the sense that matters: the rung is resolved when the box
			// opens, so the next image obeys a change made now. Nothing has to be
			// restyled or relaunched, which is why applySettingChange has no arm.
			timing:         timingLive,
			defaultDisplay: func() string { return (&config.Config{}).GetImagePreview() },
			reset:          func(c *config.Config) { c.ImagePreview = "" },
			summary:        "How a hinted image opens. Pixels need kitty or Ghostty.",
			// What `kitty` risks, which is the one thing about this setting a user
			// cannot find out by trying it: the failure is silent and permanent.
			// Placeholder support has no query — the protocol's own answers for
			// graphics as a whole — so a terminal that stores the image and cannot
			// draw the cells looks exactly like one that works, right up until the
			// picture is blank.
			//
			// The tmux limitation is deliberately NOT here. It costs a third line,
			// which puts `Current value` below the fold at 80x24
			// (TestExpandedHelpFitsTheFloor), and `atrium doctor` is the better home
			// for it anyway: it has its own tmux arm and, unlike this panel, it
			// knows whether the user is actually in one.
			detail: "kitty tries pixels on an untested terminal: blank or boxes means " +
				"no Unicode placeholder support — undetectable, so choose glyph.",
			gloss: map[string]string{
				config.ImagePreviewAuto:  "pixels where the terminal is known to show them, glyphs elsewhere",
				config.ImagePreviewKitty: "try pixels anyway, for a terminal Atrium does not recognise",
				config.ImagePreviewGlyph: "block glyphs always; works everywhere, including over SSH",
				config.ImagePreviewOff:   "no overlay at all; hinting an image path just copies it",
			},
			get: func(c *config.Config) string { return c.GetImagePreview() },
			set: func(c *config.Config, v string) error {
				c.ImagePreview = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{
					config.ImagePreviewAuto, config.ImagePreviewKitty,
					config.ImagePreviewGlyph, config.ImagePreviewOff,
				}
			},
		},
		boolRow("hint_bar", catAppearance, "Hint bar",
			"Show key hints on the bottom row. Off leaves the row blank.",
			"The row is reserved either way, so turning hints off does not resize the panes.",
			timingLive, true,
			(*config.Config).GetHintBar,
			func(c *config.Config, v bool) { c.HintBar = &v }),
		boolRow("os_chrome", catAppearance, "OS chrome",
			"Put fleet state in the window title and taskbar progress.",
			"Sends OSC 9;4. Turn it off if your shell owns the terminal title.",
			timingLive, true,
			(*config.Config).GetOSChrome,
			func(c *config.Config, v bool) { c.OSChrome = &v }),

		// ── Session list ──────────────────────────────────────────────────────
		{
			key: "session_sort", category: catSessionList, label: "Sort within group", kind: kindEnum,
			scope:          scopeGlobal,
			timing:         timingLive,
			defaultDisplay: func() string { return (&config.Config{}).GetSessionSort() },
			reset:          func(c *config.Config) { c.SessionSort = "" },
			summary:        "Row order inside each repo group.",
			detail: "Group order stays manual either way (`" + keys.LabelOf(keys.KeyMoveGroupUp) +
				"` / `" + keys.LabelOf(keys.KeyMoveGroupDown) + "`).",
			gloss: map[string]string{
				config.SessionSortCreation: "the manual order you set with " +
					keys.LabelOf(keys.KeyMoveDown) + "/" + keys.LabelOf(keys.KeyMoveUp),
				config.SessionSortStatus: "floats blocked and unread sessions to the top",
			},
			get: func(c *config.Config) string { return c.GetSessionSort() },
			set: func(c *config.Config, v string) error {
				c.SessionSort = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.SessionSortCreation, config.SessionSortStatus}
			},
		},
		{
			key: "group_mode", category: catSessionList, label: "Account clustering", kind: kindEnum,
			scope:          scopeGlobal,
			timing:         timingLive,
			defaultDisplay: func() string { return groupModeOnOff(&config.Config{}) },
			reset:          func(c *config.Config) { c.GroupMode = "" },
			summary:        "Add a top-level cluster per Claude account above the repo groups.",
			detail: "A divider and tinted headers per account. Manual reordering stays " +
				"available: J/K within a repo group, `{` / `}` for groups inside one cluster " +
				"(a move across an account boundary is refused), `[` / `]` for whole clusters.",
			// An on/off pair still needs glosses: contextLine shows the selected option's
			// gloss and, failing that, firstSentence(detail) — which is the same sentence
			// under BOTH options, so an unglossed row here described the clustered list
			// while clustering was off (#511). Each gloss says what the list looks like in
			// its own state.
			gloss: map[string]string{
				"off": "repo groups only, every session under its repo",
				"on":  "a divider and tinted header per account",
			},
			// Display value is off/on; the stored config value stays repo/account, so
			// config.json and a future third grouping axis keep their vocabulary.
			get: groupModeOnOff,
			set: func(c *config.Config, v string) error {
				if v == "on" {
					c.GroupMode = config.GroupModeAccount
				} else {
					c.GroupMode = config.GroupModeRepo
				}
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{"off", "on"}
			},
		},
		{
			key: "model_indicator", category: catSessionList, label: "Model chip", kind: kindEnum,
			scope:          scopeGlobal,
			timing:         timingLive,
			defaultDisplay: func() string { return (&config.Config{}).GetModelIndicator() },
			reset:          func(c *config.Config) { c.ModelIndicator = "" },
			summary:        "Per-session model chip, shown whenever the model is known.",
			get: func(c *config.Config) string {
				return c.GetModelIndicator()
			},
			set: func(c *config.Config, v string) error {
				c.ModelIndicator = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.ModelIndicatorOn, config.ModelIndicatorOff}
			},
		},
		{
			key: "effort_indicator", category: catSessionList, label: "Effort chip", kind: kindEnum,
			scope:          scopeGlobal,
			timing:         timingLive,
			defaultDisplay: func() string { return (&config.Config{}).GetEffortIndicator() },
			reset:          func(c *config.Config) { c.EffortIndicator = "" },
			summary:        "Per-session reasoning-effort chip; claude only.",
			get: func(c *config.Config) string {
				return c.GetEffortIndicator()
			},
			set: func(c *config.Config, v string) error {
				c.EffortIndicator = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.EffortIndicatorOn, config.EffortIndicatorOff}
			},
		},
		{
			key: "permission_indicator", category: catSessionList, label: "Permission chip", kind: kindEnum,
			scope:          scopeGlobal,
			timing:         timingLive,
			defaultDisplay: func() string { return (&config.Config{}).GetPermissionIndicator() },
			reset:          func(c *config.Config) { c.PermissionIndicator = "" },
			summary:        "Per-session permission-mode chip: plan, accept-edits, auto.",
			get: func(c *config.Config) string {
				return c.GetPermissionIndicator()
			},
			set: func(c *config.Config, v string) error {
				c.PermissionIndicator = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.PermissionIndicatorOn, config.PermissionIndicatorOff}
			},
		},
		{
			key: "context_indicator", category: catSessionList, label: "Session chip", kind: kindEnum,
			scope:          scopeGlobal,
			timing:         timingLive,
			defaultDisplay: func() string { return (&config.Config{}).GetContextIndicator() },
			reset:          func(c *config.Config) { c.ContextIndicator = "" },
			summary:        "What the per-session transcript chip shows; claude only.",
			// ONE line, and the fifth option is why. The ? view's budget is 17 lines
			// and this row sat exactly on it with four options and a two-line detail,
			// so "cost" costs two: its own option row, and the detail line that had
			// to go with it.
			//
			// What survived is the fact no gloss can carry — a chip that is missing
			// entirely, which reads as a bug rather than as a rule. The two
			// per-mode surprises moved into the glosses beside the modes they
			// belong to: that occupancy degrades to a count, and that a dollar
			// figure is an estimate rather than a bill. The reasoning behind all
			// three lives in ui/context.go, session/agent/window.go and
			// session/agent/pricing.go.
			detail: "Hidden when two sessions read one transcript directory.",
			gloss: map[string]string{
				config.ContextIndicatorOff:     "no chip",
				config.ContextIndicatorCount:   "tokens used, e.g. 283k",
				config.ContextIndicatorPercent: "window share, e.g. 28%; a count if unknown",
				config.ContextIndicatorBar:     "a one-cell meter; a count if unknown",
				config.ContextIndicatorCost:    "spend at list rates, e.g. ~$4.1 — not a bill",
			},
			get: func(c *config.Config) string {
				return c.GetContextIndicator()
			},
			set: func(c *config.Config, v string) error {
				c.ContextIndicator = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{
					config.ContextIndicatorPercent,
					config.ContextIndicatorCount,
					config.ContextIndicatorBar,
					config.ContextIndicatorCost,
					config.ContextIndicatorOff,
				}
			},
		},
		withModifiedWhen(cadenceRow("context_warn_percent", catSessionList, "Context warn at",
			"How full the context window must be before the chip turns amber.",
			"A percentage of the model's window, so it only applies where the window is "+
				"known. Held at or below Context danger at, which outranks it. "+cadenceNote,
			config.DefaultContextWarnPercent(), 1, 100,
			(*config.Config).GetContextWarnPercent,
			func(c *config.Config) **int { return &c.ContextWarnPercent },
			func(c *config.Config, n int) error {
				if n > c.GetContextDangerPercent() {
					return fmt.Errorf("warn must not exceed the danger threshold (%d)", c.GetContextDangerPercent())
				}
				return nil
			}),
			func(c *config.Config) bool { return c.ContextWarnPercent != nil }),
		withResetHook(cadenceRow("context_danger_percent", catSessionList, "Context danger at",
			"How full the context window must be before the chip turns red.",
			"A percentage of the model's window, so it only applies where the window is "+
				"known. Refused below a set Context warn at; while that row is unset, its "+
				"default band follows this one down. "+cadenceNote,
			config.DefaultContextDangerPercent(), 1, 100,
			(*config.Config).GetContextDangerPercent,
			func(c *config.Config) **int { return &c.ContextDangerPercent },
			func(c *config.Config, n int) error {
				// The mirror of the warn row's refusal, and it reads the STORED warn, not
				// GetContextWarnPercent: the accessor already collapses an inverted pair onto
				// danger, so comparing against it would refuse nothing. Without this the stored
				// warn becomes invisible — the warn row renders the collapsed value, so the
				// number the user set is neither displayed nor reachable, and pressing r on it
				// clears a field without changing a thing on screen. Refusing here is also what
				// makes GetContextWarnPercent's "the settings row refuses the inversion up
				// front" true from both directions; the collapse remains for a hand-edited
				// config.json, which has no row to refuse at.
				if c != nil && c.ContextWarnPercent != nil && n < *c.ContextWarnPercent {
					return fmt.Errorf("danger must not fall below the warn threshold (%d)", *c.ContextWarnPercent)
				}
				return nil
			}),
			func(c *config.Config) {
				// Reset does not go through set, so the validator above cannot see it: r on
				// this row drops danger to the built-in 90, and a warn stored at 95 — which
				// was legal while danger was 95 — is then inverted and invisible, the exact
				// state the validator refuses. Clamp the sibling down instead of clearing it,
				// so the value stays visible on its own row and reachable by its own reset.
				if c == nil || c.ContextWarnPercent == nil {
					return
				}
				if capped := c.GetContextDangerPercent(); *c.ContextWarnPercent > capped {
					c.ContextWarnPercent = &capped
				}
			}),
		cadenceRow("diff_refresh_seconds", catSessionList, "Diff chip refresh",
			"How stale a background session's +/- chip may get, in seconds.",
			"Backstops the writers no agent status can see: the terminal tab's shell, a "+
				"commit in the agent's own pane, an editor, a session sharing a linked path. "+
				"Lower costs a git walk per background session per sweep. "+cadenceNote,
			config.DefaultDiffRefreshSeconds(), 1, config.MaxDiffRefreshSeconds(),
			(*config.Config).GetDiffRefreshSeconds,
			func(c *config.Config) **int { return &c.DiffRefreshSeconds }, nil),

		// ── Notifications ─────────────────────────────────────────────────────
		{
			key: "notifications", category: catNotifications, label: "Notifications", kind: kindEnum,
			scope:          scopeGlobal,
			timing:         timingLive,
			defaultDisplay: func() string { return (&config.Config{}).GetNotifications() },
			reset:          func(c *config.Config) { c.Notifications = "" },
			summary:        "How Atrium signals a background session that finishes or blocks.",
			detail: "The selected, attached and muted sessions always stay silent, and so " +
				"does a focused terminal unless Notify when focused is on.",
			gloss: map[string]string{
				config.NotificationsOff:     "no signal",
				config.NotificationsBell:    "rings the terminal",
				config.NotificationsDesktop: "runs a notifier",
				config.NotificationsOSC:     "an OSC 9 escape that reaches you over SSH with no local binary",
			},
			get: func(c *config.Config) string { return c.GetNotifications() },
			set: func(c *config.Config, v string) error {
				c.Notifications = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.NotificationsOff, config.NotificationsBell, config.NotificationsDesktop, config.NotificationsOSC}
			},
		},
		{
			key: "notifications_finished", category: catNotifications, label: "Finished turns", kind: kindEnum,
			scope:  scopeGlobal,
			timing: timingLive,
			activeWhen: func(c *config.Config) bool {
				return c.GetNotifications() != config.NotificationsOff
			},
			defaultDisplay: func() string { return (&config.Config{}).GetNotificationsFinished() },
			reset:          func(c *config.Config) { c.NotificationsFinished = "" },
			summary:        "A quieter signal for a finished turn that did not stop to ask you.",
			detail: "Applies to a plain finished turn only: a blocked session, or one that " +
				"ended by asking you a question, stays on Notifications. Only quieter rungs " +
				"are offered, so a finished turn can never out-shout either.",
			gloss: map[string]string{
				config.NotificationsSame: "use the Notifications mode for all three",
				config.NotificationsOff:  "leave it to the list's unread marker",
				config.NotificationsBell: "ring the terminal",
			},
			get: func(c *config.Config) string { return c.GetNotificationsFinished() },
			set: func(c *config.Config, v string) error {
				c.NotificationsFinished = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.NotificationsSame, config.NotificationsOff, config.NotificationsBell}
			},
		},
		{
			key: "notify_command", category: catNotifications, label: "Notify command", kind: kindText,
			scope:  scopeGlobal,
			timing: timingLive,
			activeWhen: func(c *config.Config) bool {
				// desktop is the only mode that runs a command
				return c.GetNotifications() == config.NotificationsDesktop
			},
			defaultDisplay: func() string { return displayBuiltIn },
			reset:          func(c *config.Config) { c.NotifyCommand = "" },
			summary:        "Shell command run for each desktop notification.",
			detail: "`$ATRIUM_SESSION`, `$ATRIUM_STATUS` and `$ATRIUM_EVENT` are in its " +
				"environment. Empty uses a built-in per-OS notifier (notify-send, " +
				"terminal-notifier, or osascript).",
			get: func(c *config.Config) string {
				if c.NotifyCommand == "" {
					return displayBuiltIn
				}
				return c.NotifyCommand
			},
			editGet: func(c *config.Config) string { return c.NotifyCommand },
			set: func(c *config.Config, v string) error {
				c.NotifyCommand = strings.TrimSpace(v)
				return nil
			},
		},
		withActiveWhen(boolRow("notify_when_focused", catNotifications, "Notify when focused",
			"Keep notifying while Atrium's own terminal is focused.",
			"Off stays silent while you are watching the fleet and notifies once you switch "+
				"away. A terminal that never reports focus always notifies.",
			timingLive, false,
			(*config.Config).GetNotifyWhenFocused,
			func(c *config.Config, v bool) { c.NotifyWhenFocused = v }),
			func(c *config.Config) bool {
				return c.GetNotifications() != config.NotificationsOff
			}),
		withActiveWhen(cadenceRow("notify_throttle_seconds", catNotifications, "Notify throttle",
			"Minimum gap between two of the same signal for one session, in seconds.",
			"Each event keeps its own budget, so a question is never swallowed by a "+
				"finish moments earlier. 0 signals every edge, which is only noisy for an "+
				"agent whose state Atrium has to guess at. "+cadenceNote,
			config.DefaultNotifyThrottleSeconds(), 0, config.MaxNotifyThrottleSeconds(),
			(*config.Config).GetNotifyThrottleSeconds,
			func(c *config.Config) **int { return &c.NotifyThrottleSeconds }, nil),
			func(c *config.Config) bool {
				return c.GetNotifications() != config.NotificationsOff
			}),

		// ── Automation ────────────────────────────────────────────────────────
		boolRow("auto_yes", catAutomation, "Auto-yes",
			"Auto-accept agent prompts. A daemon keeps doing it after you quit.",
			"",
			timingLive, false,
			func(c *config.Config) bool { return c.AutoYes },
			func(c *config.Config, v bool) { c.AutoYes = v }),
		{
			key: "daemon_poll_interval", category: catAutomation, label: "Auto-yes poll interval", kind: kindInt,
			scope:  scopeGlobal,
			timing: timingRestart,
			activeWhen: func(c *config.Config) bool {
				// the daemon only runs while auto-yes is on
				return c.AutoYes
			},
			defaultDisplay: func() string { return strconv.Itoa(config.DefaultDaemonPollIntervalMs) },
			reset:          func(c *config.Config) { c.DaemonPollInterval = config.DefaultDaemonPollIntervalMs },
			summary:        "How often the auto-yes daemon checks for prompts, in milliseconds.",
			detail: fmt.Sprintf("At least %dms — below that the daemon hammers tmux in a hot "+
				"loop. Applies the next time the daemon starts.", minPollIntervalMs),
			get: func(c *config.Config) string { return strconv.Itoa(c.DaemonPollInterval) },
			set: func(c *config.Config, v string) error {
				n, err := strconv.Atoi(strings.TrimSpace(v))
				if err != nil {
					return fmt.Errorf("poll interval must be a number of milliseconds")
				}
				if n < minPollIntervalMs {
					return fmt.Errorf("poll interval must be at least %dms", minPollIntervalMs)
				}
				c.DaemonPollInterval = n
				return nil
			},
		},
		boolRow("smart_dispatch_auto", catAutomation, "Smart dispatch auto-create",
			"Let a confident i match create the session without opening the form.",
			"",
			timingLive, false,
			(*config.Config).GetSmartDispatchAuto,
			func(c *config.Config, v bool) { c.SmartDispatchAuto = &v }),
		boolRow("trust_worktrees_root", catAutomation, "Trust worktrees root",
			"Pre-accept Claude's workspace-trust dialog for every session worktree.",
			"",
			timingRestart, false,
			(*config.Config).GetTrustWorktreesRoot,
			func(c *config.Config, v bool) { c.TrustWorktreesRoot = &v }),

		// ── Input ─────────────────────────────────────────────────────────────
		boolRow("mouse", catInput, "Mouse",
			"Clickable rows, tabs and hint bar, wheel scroll, draggable divider.",
			"Off hands the mouse back to the terminal so native select-to-copy works. "+
				"While on, Shift+drag is the per-gesture escape.",
			timingLive, true,
			(*config.Config).GetMouse,
			func(c *config.Config, v bool) { c.Mouse = &v }),
		boolRow("double_tap_confirm", catInput, "Double-tap to confirm",
			// The description names the pattern rather than any one key, because the row
			// gates every keyed confirmation: spelling one of them would teach a rule
			// narrower than the switch. Where a key IS named — the examples below — it is
			// read from the registry, not spelled: the double-tap follows a rebind
			// (armDoubleTap is handed a registry-read key), so a literal "Ctrl+X" would
			// teach the one key the dialog
			// stops answering to, in front of the user who rebound kill precisely because
			// ctrl+x is their shell's editing key.
			"Let a second press of the key that opened a confirmation confirm it.",
			"So "+keys.LabelOf(keys.KeyKill)+" twice kills and "+keys.LabelOf(keys.KeySubmit)+
				" twice pushes, in one motion. Off still confirms with y — the dialog and "+
				"its warning are on screen either way.",
			timingLive, true,
			(*config.Config).GetDoubleTapConfirm,
			func(c *config.Config, v bool) { c.DoubleTapConfirm = &v }),
		boolRow("record_prompt_history", catInput, "Record prompt history",
			"Remember submitted prompts so ↑ in an empty prompt can reuse them.",
			"",
			timingLive, true,
			(*config.Config).GetRecordPromptHistory,
			func(c *config.Config, v bool) { c.RecordPromptHistory = &v }),

		// ── Projects ──────────────────────────────────────────────────────────
		{
			key: "project_search_roots", category: catProjects, label: "Project scan roots", kind: kindText,
			scope:          scopeGlobal,
			timing:         timingLive,
			defaultDisplay: func() string { return strings.Join((&config.Config{}).GetProjectSearchRoots(), ", ") },
			reset:          func(c *config.Config) { c.ProjectSearchRoots = nil },
			summary:        "Directories the background scan walks to stock the project picker.",
			detail: "Comma-separated; `~` is allowed. A changed scope re-scans the next time " +
				"the create form opens.",
			get: func(c *config.Config) string {
				return strings.Join(c.GetProjectSearchRoots(), ", ")
			},
			editGet: func(c *config.Config) string {
				return strings.Join(c.GetProjectSearchRoots(), ", ")
			},
			set: func(c *config.Config, v string) error {
				// Same split/trim/drop-blanks shape as carry_files, but empty input
				// clears the key to nil rather than storing an explicit empty list:
				// GetProjectSearchRoots treats nil and empty alike (both fall back to
				// ~), so nil is the honest way to say "no override".
				parts := strings.Split(v, ",")
				roots := make([]string, 0, len(parts))
				for _, p := range parts {
					if t := strings.TrimSpace(p); t != "" {
						roots = append(roots, t)
					}
				}
				if len(roots) == 0 {
					c.ProjectSearchRoots = nil
					return nil
				}
				c.ProjectSearchRoots = roots
				return nil
			},
		},
		{
			key: "project_search_depth", category: catProjects, label: "Project scan depth", kind: kindInt,
			scope:          scopeGlobal,
			timing:         timingLive,
			defaultDisplay: func() string { return fmt.Sprintf("default (%d)", config.DefaultProjectSearchDepth()) },
			reset:          func(c *config.Config) { c.ProjectSearchDepth = nil },
			summary:        "How many levels below each root the scan descends. 0 turns it off.",
			detail: fmt.Sprintf("Empty uses the default of %d; the maximum is %d.",
				config.DefaultProjectSearchDepth(), config.MaxProjectSearchDepth()),
			get: func(c *config.Config) string {
				switch {
				case c.ProjectSearchDepth == nil:
					return fmt.Sprintf("default (%d)", config.DefaultProjectSearchDepth())
				case *c.ProjectSearchDepth < 1:
					return "off"
				default:
					return strconv.Itoa(*c.ProjectSearchDepth)
				}
			},
			editGet: func(c *config.Config) string {
				switch {
				case c.ProjectSearchDepth == nil:
					return "" // empty selects the built-in default depth
				case *c.ProjectSearchDepth < 1:
					return "0" // explicit disabled edits as 0
				default:
					return strconv.Itoa(*c.ProjectSearchDepth)
				}
			},
			set: func(c *config.Config, v string) error {
				v = strings.TrimSpace(v)
				if v == "" {
					c.ProjectSearchDepth = nil // built-in default
					return nil
				}
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return fmt.Errorf("scan depth must be a non-negative number (0 = off, empty = default)")
				}
				// Store what the user typed and let GetProjectSearchDepth clamp, so the
				// accessor stays the single source of the bound; but refuse a value it
				// would silently rewrite, rather than showing back a number we ignore.
				if n > config.MaxProjectSearchDepth() {
					return fmt.Errorf("scan depth must be at most %d", config.MaxProjectSearchDepth())
				}
				c.ProjectSearchDepth = &n
				return nil
			},
		},

		// ── Updates ───────────────────────────────────────────────────────────
		{
			key: "auto_update", category: catUpdates, label: "Auto-update", kind: kindEnum,
			scope:          scopeGlobal,
			timing:         timingRestart,
			defaultDisplay: func() string { return (&config.Config{}).GetAutoUpdateMode() },
			reset:          func(c *config.Config) { c.AutoUpdate = "" },
			summary:        "What the startup update check does when a new version exists.",
			gloss: map[string]string{
				config.AutoUpdateNotify: "show a hint",
				config.AutoUpdateAuto:   "install in the background",
				config.AutoUpdateOff:    "no check",
			},
			get: func(c *config.Config) string { return c.GetAutoUpdateMode() },
			set: func(c *config.Config, v string) error {
				c.AutoUpdate = v
				return nil
			},
			options: func(c *config.Config) []string {
				return []string{config.AutoUpdateNotify, config.AutoUpdateAuto, config.AutoUpdateOff}
			},
		},
		boolRow("show_release_notes_after_update", catUpdates, "Release notes after update",
			"Show a what's-new overlay once after updating.",
			"",
			timingLive, true,
			(*config.Config).GetShowReleaseNotesAfterUpdate,
			func(c *config.Config, v bool) { c.ShowReleaseNotesAfterUpdate = &v }),

		// ── Advanced ──────────────────────────────────────────────────────────
		{
			key: "tmux_config_override", category: catAdvanced, label: "Tmux config override", kind: kindText,
			scope:          scopeGlobal,
			timing:         timingNewSessions,
			defaultDisplay: func() string { return displayManaged },
			reset:          func(c *config.Config) { c.TmuxConfigOverride = "" },
			summary:        "Path to your own tmux config for session panes.",
			detail: "Empty uses Atrium's managed conf. Sessions already running keep the " +
				"config their server started with.",
			get: func(c *config.Config) string {
				if c.TmuxConfigOverride == "" {
					return displayManaged
				}
				return c.TmuxConfigOverride
			},
			editGet: func(c *config.Config) string { return c.TmuxConfigOverride },
			set: func(c *config.Config, v string) error {
				c.TmuxConfigOverride = strings.TrimSpace(v)
				return nil
			},
		},
		{
			key: "agent_oom_margin", category: catAdvanced, label: "Agent OOM margin", kind: kindInt,
			scope:  scopeGlobal,
			timing: timingNewSessions,
			activeWhen: func(c *config.Config) bool {
				// the kernel knob is Linux-only
				return runtime.GOOS == "linux"
			},
			defaultDisplay: func() string { return fmt.Sprintf("on (%d)", config.DefaultOOMMargin()) },
			reset:          func(c *config.Config) { c.AgentOOMMargin = nil },
			summary:        "Raise each agent above the tmux server in the kernel's OOM ranking.",
			detail: fmt.Sprintf("Linux only. A kernel OOM kill then sheds one recoverable "+
				"session instead of the shared server and every session with it. Empty is on at "+
				"the default margin of %d, 0 is off, a number sets the margin. The kernel fixes "+
				"`oom_score_adj` at exec, so an agent already running keeps its launched value "+
				"until the session is relaunched.", config.DefaultOOMMargin()),
			get: func(c *config.Config) string {
				switch {
				case c.AgentOOMMargin == nil:
					return fmt.Sprintf("on (%d)", config.DefaultOOMMargin())
				case *c.AgentOOMMargin < 1:
					return "off"
				default:
					return strconv.Itoa(*c.AgentOOMMargin)
				}
			},
			editGet: func(c *config.Config) string {
				switch {
				case c.AgentOOMMargin == nil:
					return "" // empty selects the default margin (on)
				case *c.AgentOOMMargin < 1:
					return "0" // explicit disabled edits as 0
				default:
					return strconv.Itoa(*c.AgentOOMMargin)
				}
			},
			set: func(c *config.Config, v string) error {
				v = strings.TrimSpace(v)
				if v == "" {
					c.AgentOOMMargin = nil // default margin (on)
					return nil
				}
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return fmt.Errorf("agent OOM margin must be a non-negative number (0 = off, empty = on)")
				}
				c.AgentOOMMargin = &n // 0 = explicit off; positive = margin
				return nil
			},
		},
		cadenceRow("pending_watchdog_minutes", catAdvanced, "Pending watchdog",
			"How long a session may wait on background work before Atrium gives up.",
			"Past the cap Atrium stops believing the agent's in-flight record and marks "+
				"the session finished. A dead pane is already caught in seconds, so this "+
				"only backstops an alive-but-stuck one, and it outranks any per-agent "+
				"default. "+cadenceNote,
			config.DefaultPendingWatchdogMinutes(), 1, config.MaxPendingWatchdogMinutes(),
			(*config.Config).GetPendingWatchdogMinutes,
			func(c *config.Config) **int { return &c.PendingWatchdogMinutes }, nil),
		// The resolved config.json path, so the file the panel writes is discoverable
		// from inside the panel. Memoized rather than re-resolved per render, since
		// GetConfigDir stats the filesystem; see configFilePath for why the memo has to
		// be lazy rather than a package var.
		{
			key: "config_file", category: catAdvanced, label: "Config file", kind: kindReadOnly,
			scope:   scopeGlobal,
			timing:  timingLive,
			summary: "Where Atrium keeps the settings on this page.",
			detail: "Atrium reads this file at launch and rewrites it whenever you change a " +
				"setting here, so an edit made by hand while the TUI is running will be " +
				"overwritten.",
			get: func(c *config.Config) string { return configFilePath() },
		},
	}
}
