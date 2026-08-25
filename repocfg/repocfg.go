// Package repocfg validates and renders the per-repository environment declared in
// config.json's repo_scripts section (#389): the script that runs once a session's
// worktree exists, and the environment exported into it.
//
// It is the whole rule set for what a valid entry looks like, deliberately split from
// config (which owns only the wire shape) and from session/app (which own execution
// and the surfaces that report it). That split is what lets every rule below be tested
// without a TUI, a tmux server, or a git repo — the same division customcmd draws, and
// for the same reason.
//
// Two properties are load-bearing and easy to lose, both inherited from customcmd:
//
// A rejected entry is DROPPED, not applied. config.LoadConfig cannot fail — a read or
// parse error logs a warning and falls back to DefaultConfig — and it is called from a
// dozen-odd non-test sites, most of them outside the TUI, so a config problem must
// never be an error return that ripples out to them. Validate reports problems
// alongside the entries that survived, and the caller decides how loudly to say so.
//
// A template is validated by RENDERING it, not by parsing it. Parsing accepts
// {{.Session.Wortree}} happily; only execution against a populated context reports the
// typo. Doing that at load time turns a placeholder typo into a message the user can
// act on, instead of an empty string handed to a shell that then runs `rm -rf /build`.
// The limit of the technique is the same one it has there: one render takes one path,
// so a placeholder inside a conditional the probe does not enter escapes to run time.
//
// The template context is customcmd's, by alias rather than by copy. A user writing
// {{.Session.Worktree}} should not have to learn which of two vocabularies a given
// config section speaks, and a second declaration of the same six fields is a drift
// site with nothing guarding it.
package repocfg

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/customcmd"
)

// The template context, shared verbatim with custom commands. Aliases, not new types:
// a value of one is a value of the other, so a caller that has built a context for one
// feature can hand it to the other without conversion.
type (
	// Ctx is what a repo script's templates render against.
	Ctx = customcmd.Ctx
	// SessionCtx is the session's half of that context.
	SessionCtx = customcmd.SessionCtx
	// RepoCtx is the repository's half.
	RepoCtx = customcmd.RepoCtx
)

// probe is a Ctx with every field populated, used to render each template once at
// validation time. Every value is non-empty so that a render failure means "this
// placeholder does not exist", never "this session happens to have no branch".
// TestProbePopulatesEveryContextLeaf keeps that true as the context grows.
var probe = Ctx{
	Session: SessionCtx{Title: "probe", Name: "probe", Branch: "probe", Worktree: "/probe", Port: "3000"},
	Repo:    RepoCtx{Path: "/probe", Name: "probe"},
}

// Script is a validated repo_scripts entry, ready to render.
type Script struct {
	// Name is the entry's optional label, carried so a message about a script can
	// name which configured entry it came from.
	Name string

	setup *template.Template
	run   *template.Template
	env   []envEntry
	ports PortRange
}

// PortRange is the inclusive span a session's managed port is drawn from.
//
// A zero value means the entry declares no range, which is not the same as a range
// that happens to be exhausted: the first is "this repo has no managed port" and the
// second is a condition to report. Ports() is what tells them apart.
type PortRange struct {
	Lo int
	Hi int
}

// Count is how many ports the range holds, which is the ceiling on how many sessions
// of this repo can hold one at once.
func (r PortRange) Count() int {
	// The zero value counts zero, not one: Lo == Hi == 0 is "no range declared", and a
	// range of one is spelled with a real port on both ends.
	if r.Lo <= 0 || r.Hi < r.Lo {
		return 0
	}
	return r.Hi - r.Lo + 1
}

// String renders the range the way it is spelled in config.json.
func (r PortRange) String() string { return fmt.Sprintf("%d-%d", r.Lo, r.Hi) }

// envEntry is one session_env pair, kept as a slice rather than a map so the rendered
// environment has a stable order.
type envEntry struct {
	name string
	tmpl *template.Template
}

// Problem is one rejected entry: which one, and why. Index is the entry's position in
// the configured list, so a user with several entries can find it even when none of
// them is named.
type Problem struct {
	// Section is the config key the entry sits under. The zero value means
	// repo_scripts, which is every Problem the global config produces — a default
	// rather than a required field so the dozen existing construction sites keep
	// naming the section they always named.
	Section string
	Index   int
	Name    string
	Msg     string
}

// where names the entry the problem is about: `<section>[N]`, plus the entry's own
// name when it has one. Split out from Error because the repo-local parser reports
// some refusals as a whole-file error rather than a Problem and must still say
// which entry it choked on, in one spelling.
func (p Problem) where() string {
	section := p.Section
	if section == "" {
		section = "repo_scripts"
	}
	if p.Name == "" {
		return fmt.Sprintf("%s[%d]", section, p.Index)
	}
	return fmt.Sprintf("%s[%d] (%q)", section, p.Index, p.Name)
}

// Error renders the problem as the user sees it.
func (p Problem) Error() string {
	return fmt.Sprintf("%s: %s", p.where(), p.Msg)
}

// Validate turns configured entries into renderable scripts, reporting the ones it
// refused. The two results are independent: a config with one bad entry still yields
// every good one.
func Validate(entries []config.RepoScript) ([]Script, []Problem) {
	var out []Script
	var problems []Problem

	for i, e := range entries {
		script, problem := ValidateOne(i, e)
		if problem != nil {
			problems = append(problems, *problem)
			continue
		}
		out = append(out, script)
	}
	return out, problems
}

// ValidateOne validates the single entry sitting at position index of the configured
// list, which is what the session does: routing has already picked one entry, and a
// broken sibling must not stop it.
//
// index is passed rather than assumed because it is the only thing a reported problem
// can be found by — an entry need not be named, and Problem.Error() prints
// `repo_scripts[N]`. Validating a one-element slice instead would report every problem
// as entry 0 and point the user at whichever entry happens to be first.
func ValidateOne(index int, e config.RepoScript) (Script, *Problem) {
	script, err := compile(e)
	if err != nil {
		return Script{}, &Problem{Index: index, Name: e.Name, Msg: err.Error()}
	}
	return script, nil
}

// DeclaredSurfaces names the execution-adjacent surfaces entry e configures, in
// a fixed order, under the same emptiness rules compile enforces — an empty
// list IS compile's "configures nothing". One definition on purpose: the
// create-time trust prompt and `atrium trust allow` describe an entry by this
// list, and both compile here and the repo-local parse refuse an entry it is
// empty for, so a private copy in any of those places could describe an entry
// the others refuse (or grant one they never run).
func DeclaredSurfaces(e config.RepoScript) []string {
	var out []string
	if strings.TrimSpace(e.SetupScript) != "" {
		out = append(out, "setup script")
	}
	if strings.TrimSpace(e.RunCommand) != "" {
		out = append(out, "run command")
	}
	if len(e.SessionEnv) > 0 {
		out = append(out, "session env")
	}
	if strings.TrimSpace(e.PortRange) != "" {
		out = append(out, "port range")
	}
	return out
}

// configuresNothingMsg is shared by compile's refusal and ParseRepoLocal's, so
// the same defect reads the same whichever list it was found in.
const configuresNothingMsg = "entry configures nothing — set setup_script, run_command, session_env or port_range"

// compile applies every rule to one entry, returning the first failure.
func compile(e config.RepoScript) (Script, error) {
	// An entry that configures nothing is refused rather than kept as a harmless
	// no-op. Its route rules still MATCH, so keeping it would shadow a later entry
	// that does configure something — a silent "my setup script never ran" with
	// nothing to point at.
	if len(DeclaredSurfaces(e)) == 0 {
		return Script{}, errors.New(configuresNothingMsg)
	}

	script := Script{Name: e.Name}
	if raw := strings.TrimSpace(e.PortRange); raw != "" {
		rng, err := parsePortRange(raw)
		if err != nil {
			return Script{}, err
		}
		script.ports = rng
	}
	if strings.TrimSpace(e.SetupScript) != "" {
		tmpl, err := compileTemplate("setup_script", e.SetupScript)
		if err != nil {
			return Script{}, err
		}
		script.setup = tmpl
	}
	if strings.TrimSpace(e.RunCommand) != "" {
		tmpl, err := compileTemplate("run_command", e.RunCommand)
		if err != nil {
			return Script{}, err
		}
		script.run = tmpl
	}

	names := make([]string, 0, len(e.SessionEnv))
	for name := range e.SessionEnv {
		if !validEnvName(name) {
			return Script{}, fmt.Errorf("session_env: %q is not a usable environment variable name — letters, digits and underscores only, and not starting with a digit", name)
		}
		if reservedEnvName(name) {
			return Script{}, fmt.Errorf("session_env: %q is reserved — Atrium injects it itself", name)
		}
		names = append(names, name)
	}
	// Sorted here rather than at render time: the order is a property of the compiled
	// script, so one config always yields the same environment and the same
	// `new-session -e` argv, however Go happened to iterate the map. (Nothing records
	// either: cmdlog stores argv, and the setup script's argv is synthetic while the
	// tmux launch goes through the pty factory rather than the recording executor.)
	sort.Strings(names)
	for _, name := range names {
		tmpl, err := compileTemplate("session_env["+name+"]", e.SessionEnv[name])
		if err != nil {
			return Script{}, err
		}
		script.env = append(script.env, envEntry{name: name, tmpl: tmpl})
	}
	return script, nil
}

// compileTemplate parses one template and proves it renders, naming the field it came
// from so a problem with three templates in it says which one broke.
//
// customcmd's helper set, not a set of its own. A repo script renders into a shell
// string exactly as a custom command does, and the README points a user with a value
// that might contain a space at the same `quote` function — which, without this, parses
// as `function "quote" not defined` and silently drops the entry. It is also the only
// escaping there is: {{.Session.Name}} is the freely-mutable display name, so a session
// renamed to `x; rm -rf ~` is a shell injection into the user's own setup script
// wherever it is interpolated bare.
func compileTemplate(field, text string) (*template.Template, error) {
	tmpl, err := template.New(field).Funcs(customcmd.Funcs()).Parse(text)
	if err != nil {
		return nil, fmt.Errorf("%s: template does not parse: %w", field, err)
	}
	if _, err := render(tmpl, probe); err != nil {
		return nil, fmt.Errorf("%s: template does not render: %w", field, err)
	}
	return tmpl, nil
}

// minManagedPort is the bottom of the range a port_range may draw from. Below 1024 a
// listener needs root on every platform Atrium runs on, so a dev server started from
// an agent's pane could not bind one — refusing at load time says so where the message
// can be read, rather than handing out a port every allocation then fails to prove free.
const minManagedPort = 1024

// maxPort is the top of the TCP port space.
const maxPort = 65535

// parsePortRange resolves the "lo-hi" spelling, refusing every way it can be wrong.
//
// One syntax, deliberately: a bare "3000" is refused rather than read as a range of
// one, because the two spellings would differ in what happens when a second session
// asks for a port — and a user who wrote the short form is likelier to have meant "the
// dev server port" than "exactly one session may run".
func parsePortRange(raw string) (PortRange, error) {
	lo, hi, ok := strings.Cut(raw, "-")
	if !ok {
		return PortRange{}, fmt.Errorf("port_range %q is not a range — spell it lo-hi, e.g. 3000-3099", raw)
	}
	loN, loErr := strconv.Atoi(strings.TrimSpace(lo))
	hiN, hiErr := strconv.Atoi(strings.TrimSpace(hi))
	if loErr != nil || hiErr != nil {
		return PortRange{}, fmt.Errorf("port_range %q is not a range of two numbers — spell it lo-hi, e.g. 3000-3099", raw)
	}
	if loN < minManagedPort || hiN < minManagedPort {
		return PortRange{}, fmt.Errorf("port_range %q reaches below %d, where binding needs root", raw, minManagedPort)
	}
	if loN > maxPort || hiN > maxPort {
		return PortRange{}, fmt.Errorf("port_range %q reaches above %d, the highest TCP port", raw, maxPort)
	}
	if hiN < loN {
		return PortRange{}, fmt.Errorf("port_range %q ends below where it starts — the low end comes first", raw)
	}
	return PortRange{Lo: loN, Hi: hiN}, nil
}

// Ports reports the configured range, and false when the entry declares none.
func (s Script) Ports() (PortRange, bool) {
	if s.ports.Count() == 0 {
		return PortRange{}, false
	}
	return s.ports, true
}

// reservedEnvName reports whether Atrium injects name itself.
//
// These are refused rather than allowed to win or lose. Every one of them is set on
// the same `tmux new-session -e` argv the session's own values ride, and which of two
// assignments of one name survives is tmux's business, not something this package can
// state — so a user who sets ATRIUM_SESSION would get a session whose hook plumbing
// works or does not depending on a detail nothing here controls. Refusing says so at
// load time, where the message can be read.
//
// The whole ATRIUM_ prefix is reserved, not just the names in use today — which are
// ATRIUM_SESSION, the marker, and ATRIUM_PORT. The set grows, and a rule that listed its
// members one by one would silently start colliding with a config that was valid when it
// was written.
func reservedEnvName(name string) bool {
	if strings.HasPrefix(name, "ATRIUM_") || name == "ATRIUM" {
		return true
	}
	switch name {
	case "CLAUDE_CONFIG_DIR", "GH_CONFIG_DIR":
		return true
	}
	return false
}

// validEnvName reports whether name is one a shell can export: the POSIX name
// production, which is also what tmux's `-e NAME=VALUE` accepts.
func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		switch {
		case b == '_':
		case b >= 'a' && b <= 'z':
		case b >= 'A' && b <= 'Z':
		case b >= '0' && b <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// HasSetupScript reports whether the entry declares a script to run.
func (s Script) HasSetupScript() bool { return s.setup != nil }

// RenderSetup resolves the setup script against ctx. It returns "" when the entry
// declares no script, which callers must treat as "nothing to run" rather than as an
// empty command.
func (s Script) RenderSetup(ctx Ctx) (string, error) {
	if s.setup == nil {
		return "", nil
	}
	return render(s.setup, ctx)
}

// HasRunCommand reports whether the entry declares a long-running command to host.
func (s Script) HasRunCommand() bool { return s.run != nil }

// RenderRun resolves the run command against ctx. It returns "" when the entry declares
// none, which callers must treat as "there is nothing to run" rather than as an empty
// command handed to a shell.
func (s Script) RenderRun(ctx Ctx) (string, error) {
	if s.run == nil {
		return "", nil
	}
	return render(s.run, ctx)
}

// RenderEnv resolves session_env against ctx, as NAME=VALUE in name order.
//
// It is only the user's own variables: the $ATRIUM_* set every Atrium-run command
// gets is customcmd.Env's, and composing them is the caller's job so that the two
// cannot silently disagree about which one wins.
func (s Script) RenderEnv(ctx Ctx) ([]string, error) {
	if len(s.env) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(s.env))
	for _, e := range s.env {
		value, err := render(e.tmpl, ctx)
		if err != nil {
			return nil, fmt.Errorf("session_env[%s]: %w", e.name, err)
		}
		out = append(out, e.name+"="+value)
	}
	return out, nil
}

func render(t *template.Template, ctx Ctx) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, ctx); err != nil {
		return "", err
	}
	return b.String(), nil
}
