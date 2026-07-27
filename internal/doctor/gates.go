package doctor

// Remote feature-gate reporting. `atrium doctor`'s version check answers "which
// build is installed"; this answers "which BRANCH of that build renders". Claude
// ships two footer implementations in one binary and picks between them on a
// server-resolved gate, so the pane can change with no version change at all —
// drift the version check structurally cannot see (#337).
//
// The values are readable because claude persists every gate it resolves to
// <config dir>/.claude.json under cachedGrowthBookFeatures, rewriting the map from
// its in-memory payload on every fetch. That makes the file a faithful,
// eventually-consistent mirror of what the CLI actually resolved.
//
// One edge is documented rather than handled: with DISABLE_GROWTHBOOK set, claude
// short-circuits gate resolution and every gate takes its LOCAL default, ignoring
// the file. For tengu_copper_thistle that default is false — the value we pin — so
// the edge can only make this read pessimistic (a spurious "flipped"), never
// falsely reassuring. That is the safe direction for a report, and special-casing
// an env var that doctor sees but a session may not would be guessing.
//
// Like VerifiedVersion, this is a RECORD, not a tripwire that acts: nothing polls
// it and nothing in the TUI consumes it. See the "Why the TUI is untouched" note
// below CheckGatesInstalled.

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/session/agent"
)

// GateState is the outcome of comparing one pinned gate against the value the
// agent CLI last resolved. Every failure resolves to GateUnknown: a gate check is
// a report, never an alarm, and a false alarm is worse than silence.
type GateState int

const (
	// GateMatchesPin means the resolved value equals the adapter's pin — the
	// heuristics were verified against the branch this account renders.
	GateMatchesPin GateState = iota
	// GateFlipped means the resolved value differs from the pin: the heuristics
	// were verified on the other branch of this gate.
	GateFlipped
	// GateUnknown means no comparable value could be read (see fileGateReader).
	GateUnknown
)

// GateResult is the report for one (agent, config dir, gate) triple.
type GateResult struct {
	Key     agent.Key
	Name    string // adapter display name
	Account string // config-dir label: an account name, or defaultAccount
	Gate    string
	Pinned  bool
	Actual  bool // meaningful only when State == GateMatchesPin or GateFlipped
	State   GateState
}

// defaultAccount labels the ambient config dir — the one a session with no
// CLAUDE_CONFIG_DIR injected inherits.
const defaultAccount = "default"

// gateDir is one claude config dir to report on, with the label it renders under.
type gateDir struct {
	Account string
	Dir     string
}

// gateReader reads the feature gates a claude CLI resolved into one config dir.
// The method is unexported so only in-package fakes implement it, mirroring
// Runner in check.go. ok=false means "no comparable map here" — every read
// failure collapses into it, because none of them are distinguishable to a user
// and none of them justify an alarm.
type gateReader interface {
	gates(configDir string) (m map[string]any, ok bool)
}

// fileGateReader is the production gateReader: it reads <configDir>/.claude.json.
//
// Deliberately conservative, in the spirit of session/tmux/trust.go (the other
// reader of this file): a missing file just means claude is not onboarded in that
// dir, and a malformed one is claude's file and claude's problem. It is far
// simpler than trust.go because it only reads — none of that function's
// symlink-following, stat-guarding or atomic-rename machinery has anything to
// guard here, and neither does its UseNumber decoding: we compare bools and
// discard every other value rather than re-encoding the file.
type fileGateReader struct{}

func (fileGateReader) gates(configDir string) (map[string]any, bool) {
	// Refuse a relative (or empty) dir rather than let filepath.Join resolve it
	// against doctor's working directory: "" would silently read ./.claude.json,
	// reporting on a file no session has any relationship to — and a stray true in
	// it would render as a flip, the one outcome this whole file exists to avoid.
	// An unresolvable home or a relative CLAUDE_CONFIG_DIR is genuinely unknown; we
	// cannot know the cwd claude would have resolved it against.
	if !filepath.IsAbs(configDir) {
		return nil, false
	}
	data, err := os.ReadFile(filepath.Join(configDir, ".claude.json"))
	if err != nil {
		return nil, false
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, false
	}
	m, ok := root["cachedGrowthBookFeatures"].(map[string]any)
	if !ok {
		return nil, false // absent, null, or reshaped by a future claude
	}
	return m, true
}

// CheckGates compares every adapter's VerifiedGates against the value resolved in
// each dir. Pure: no config load, no home resolution, no IO of its own — that all
// lives in the caller and the reader, which is what keeps this testable without
// touching a real config dir. It never errors, and adapters pinning no gates
// contribute no rows.
func CheckGates(adapters []*agent.Adapter, dirs []gateDir, r gateReader) []GateResult {
	var out []GateResult
	for _, a := range adapters {
		if len(a.VerifiedGates) == 0 {
			continue
		}
		for _, d := range dirs {
			resolved, ok := r.gates(d.Dir)
			for _, g := range a.VerifiedGates {
				res := GateResult{
					Key: a.Key, Name: a.DisplayName, Account: d.Account,
					Gate: g.Name, Pinned: g.Value, State: GateUnknown,
				}
				if ok {
					// A gate absent from the map was never resolved here, and a
					// non-bool (the map also holds numbers and objects) is not
					// comparable to a bool pin. Both stay unknown rather than
					// guess at a value.
					if v, isBool := resolved[g.Name].(bool); isBool {
						res.Actual = v
						res.State = GateMatchesPin
						if v != g.Value {
							res.State = GateFlipped
						}
					}
				}
				out = append(out, res)
			}
		}
	}
	return out
}

// CheckGatesInstalled reports on the real environment: the ambient config dir plus
// every configured ClaudeAccount. It is the only function here that touches
// config, which is the point — config.LoadConfig seeds config.json when it is
// missing (a write), so it must stay out of the pure CheckGates that tests call.
//
// Why nothing but `atrium doctor` calls this: the startup drift hint suppresses
// via an ack keyed on the *installed version* (app/app_driftcheck.go,
// config.State.AckedDrift), and a gate flip has no version. Wiring it in would
// either be swallowed for anyone who had already acked their current version, or
// become an un-ackable permanent nag. Doctor is the "tell me the truth about my
// heuristics" command, and #337's ask was precisely that a flip is invisible to
// it.
func CheckGatesInstalled() []GateResult {
	return CheckGates(agent.Adapters(), installedGateDirs(config.LoadConfig()), fileGateReader{})
}

// installedGateDirs enumerates the config dirs whose gate state matters, ambient
// first. The rule is one row per config dir the user named: every ClaudeAccount
// with a config_dir of its own keeps its row, so a reader finds the state under
// the name they routed sessions to rather than having to know which accounts
// share a dir. Two accounts on one dir therefore report it twice (same dir, same
// value, two names) — an absurd config, but the honest rendering of it, and one
// extra read of one small file.
//
// The lone dedupe is against defaultAccount, which is the one label here the user
// did NOT configure: it is a stand-in for "whatever dir an unrouted session
// inherits", so the moment a real account names that dir the stand-in has nothing
// left to say and yields its row. Only the first such account claims it; a second
// gets its own row like any other.
//
// The rule says "named a config dir" and not "configured" because an inherit-env
// account (config_dir "") names none: it injects nothing, so it reads whatever an
// unrouted session inherits and has no dir of its own to report. It rides the
// ambient row and is skipped here — which means it is reported under whatever name
// that row ends up carrying, "default" or the first account to claim it, never its
// own. Only a label is ever wrong: the dir read, and so the value, is the one that
// account's sessions actually see either way.
//
// Comparison is by CLEANED path, because the two spellings meeting here come from
// different places — doctor's own env and a hand-written config_dir — and a
// trailing slash is not a different dir. Cleaning happens here rather than in
// config.AmbientClaudeConfigDir so that function's ""-means-unresolvable contract
// survives: filepath.Clean("") is ".", which would turn "no ambient dir" into a
// cwd-relative one.
//
// The ambient dir comes from config.AmbientClaudeConfigDir, shared with the
// worktrees-root trust in app — the two resolved it separately until #359, and
// disagreed. Note it reads DOCTOR's env, which is a session's ambient env only
// when the two match (a session inherits the tmux server's env, captured at server
// start). Running `atrium doctor` from inside a Claude Code session, which exports
// CLAUDE_CONFIG_DIR, therefore points this row at the caller's own account dir.
// That mislabels rather than misreports: the dedupe below relabels the row to the
// account name whenever that dir is a configured account, and the value read is a
// real resolved value either way. Its "" (unresolvable) drops the row entirely,
// rather than reporting on a dir this had to guess at.
func installedGateDirs(cfg *config.Config) []gateDir {
	var dirs []gateDir
	ambient := -1 // index of the still-unclaimed defaultAccount row, if any
	if dir := config.AmbientClaudeConfigDir(); dir != "" {
		dirs = append(dirs, gateDir{Account: defaultAccount, Dir: filepath.Clean(dir)})
		ambient = 0
	}
	for _, a := range cfg.ClaudeAccounts {
		dir := a.ResolvedConfigDir()
		if dir == "" {
			continue // inherit-env account: no dir of its own; rides the ambient row
		}
		dir = filepath.Clean(dir)
		if ambient >= 0 && dirs[ambient].Dir == dir {
			dirs[ambient].Account = a.Name
			ambient = -1 // claimed: a further account on this dir gets its own row
			continue
		}
		dirs = append(dirs, gateDir{Account: a.Name, Dir: dir})
	}
	return dirs
}

// GatesFlipped reports whether any result is a confirmed flip — the only state
// that warrants more than an informational row.
func GatesFlipped(results []GateResult) bool {
	for _, r := range results {
		if r.State == GateFlipped {
			return true
		}
	}
	return false
}
