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
	"fmt"
	"sort"
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
	Session: SessionCtx{Title: "probe", Name: "probe", Branch: "probe", Worktree: "/probe"},
	Repo:    RepoCtx{Path: "/probe", Name: "probe"},
}

// Script is a validated repo_scripts entry, ready to render.
type Script struct {
	// Name is the entry's optional label, carried so a message about a script can
	// name which configured entry it came from.
	Name string

	setup *template.Template
	env   []envEntry
}

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
	Index int
	Name  string
	Msg   string
}

// Error renders the problem as the user sees it.
func (p Problem) Error() string {
	if p.Name == "" {
		return fmt.Sprintf("repo_scripts[%d]: %s", p.Index, p.Msg)
	}
	return fmt.Sprintf("repo_scripts[%d] (%q): %s", p.Index, p.Name, p.Msg)
}

// Validate turns configured entries into renderable scripts, reporting the ones it
// refused. The two results are independent: a config with one bad entry still yields
// every good one.
func Validate(entries []config.RepoScript) ([]Script, []Problem) {
	var out []Script
	var problems []Problem

	for i, e := range entries {
		script, err := compile(e)
		if err != nil {
			problems = append(problems, Problem{Index: i, Name: e.Name, Msg: err.Error()})
			continue
		}
		out = append(out, script)
	}
	return out, problems
}

// compile applies every rule to one entry, returning the first failure.
func compile(e config.RepoScript) (Script, error) {
	// An entry that configures nothing is refused rather than kept as a harmless
	// no-op. Its route rules still MATCH, so keeping it would shadow a later entry
	// that does configure something — a silent "my setup script never ran" with
	// nothing to point at.
	if strings.TrimSpace(e.SetupScript) == "" && len(e.SessionEnv) == 0 {
		return Script{}, fmt.Errorf("entry configures nothing — set setup_script or session_env")
	}

	script := Script{Name: e.Name}
	if strings.TrimSpace(e.SetupScript) != "" {
		tmpl, err := compileTemplate("setup_script", e.SetupScript)
		if err != nil {
			return Script{}, err
		}
		script.setup = tmpl
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
func compileTemplate(field, text string) (*template.Template, error) {
	tmpl, err := template.New(field).Parse(text)
	if err != nil {
		return nil, fmt.Errorf("%s: template does not parse: %w", field, err)
	}
	if _, err := render(tmpl, probe); err != nil {
		return nil, fmt.Errorf("%s: template does not render: %w", field, err)
	}
	return tmpl, nil
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
// The whole ATRIUM_ prefix is reserved, not just the names in use today: the set grows
// (a managed port is next), and a rule that listed them one by one would silently
// start colliding with a config that was valid when it was written.
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
