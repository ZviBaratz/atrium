// Package customcmd validates and renders the user-defined verbs declared in
// config.json's custom_commands section (#375): a key, a description, a shell
// template, and where the template runs.
//
// It is the whole rule set for what a valid entry looks like, deliberately split
// from config (which owns only the wire shape) and from app (which owns the menu,
// the gating and the execution). That split is what lets every rule below be tested
// without a TUI, a tmux server, or a git repo.
//
// Two properties are load-bearing and easy to lose:
//
// A rejected entry is DROPPED, not bound. config.LoadConfig cannot fail — "a missing
// file is created with defaults, and any read/parse error logs a warning and falls
// back to DefaultConfig" — and it is called from a dozen-odd non-test sites, most of
// them outside the TUI, so a config problem must never be an error return that ripples
// out to them. Validate reports problems alongside the entries that survived, and the
// caller decides how loudly to say so.
//
// A template is validated by RENDERING it, not by parsing it. Parsing accepts
// {{.Session.Wortree}} happily; only execution against a populated context reports
// the typo. Doing that at load time is what turns a placeholder typo into a message
// the user can act on instead of an empty string handed to a shell. The limit of that
// technique is worth knowing: one render takes one path, so a placeholder inside a
// conditional the probe does not enter escapes to run time.
//
// Four surfaces consume this, and they must never disagree about what is valid because
// they all read the same pass: the `!` menu that runs these commands, the `?` cheatsheet
// that lists them (which has its own width bound and truncation rules — see
// helpCustomDescWidth), the startup modal that reports the refused ones, and
// `atrium doctor`.
package customcmd

import (
	"fmt"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"

	"github.com/ZviBaratz/atrium/config"
)

// Context selects the directory a command runs in.
type Context string

const (
	// ContextSession runs in the agent's working directory. It is the default, and the
	// app gates it on a started, unpaused session (customCommandInertReason), because
	// before Start an instance's working directory is still the user's origin checkout —
	// running there is the one outcome this context must never produce.
	ContextSession Context = "session"
	// ContextRepo runs in the repository root, which outlives a pause.
	ContextRepo Context = "repo"
)

// Output selects how a command's execution is presented.
type Output string

const (
	// OutputBackground runs the command detached, reporting its exit status when it
	// finishes.
	OutputBackground Output = "background"
	// OutputTerminal gives the command the terminal for its whole duration, the way an
	// attach does, and returns to the list when it exits. It is for a command the user
	// means to watch or interact with — lazygit, an editor, a pager — where a captured
	// buffer would be useless.
	//
	// The cost is real and is why `output` has no default: the Bubble Tea event loop is
	// suspended for the duration, so the app services the fleet from a keeper goroutine
	// rather than from its tick (see app.attachKeeper).
	OutputTerminal Output = "terminal"
)

// outputModes is the accepted `output` set and the blurb each mode is described by.
//
// It is the single source for three things that were separately spelled and drifted the
// moment a second mode landed: what parseOutput accepts, and both messages it refuses
// with. With no default, those messages are the ONLY place a user learns what the legal
// values are, and when they were hand-written enumerations naming `background` alone, a
// user adding `terminal` correctly was told by the error itself that the value did not
// exist. A new mode now joins the accepted set and both messages by adding one row here.
var outputModes = []struct {
	name  Output
	blurb string
}{
	{OutputBackground, "runs it detached and reports the exit status"},
	{OutputTerminal, "gives it the terminal until it exits"},
}

// Outputs returns every accepted output mode, in declaration order. Exported so a guard
// over "every mode" can be derived rather than restated — the drift this table exists to
// prevent is exactly what a hand-written test fixture reintroduces.
func Outputs() []Output {
	out := make([]Output, 0, len(outputModes))
	for _, m := range outputModes {
		out = append(out, m.name)
	}
	return out
}

// SessionCtx is the selected session's half of the template context.
type SessionCtx struct {
	Title    string
	Name     string
	Branch   string
	Worktree string
}

// RepoCtx is the selected session's repository half of the template context.
type RepoCtx struct {
	Path string
	Name string
}

// Ctx is what a command's template renders against.
//
// It is a struct of strings rather than a map on purpose: a struct makes an unknown
// placeholder an execution error naming the field, where a map would need
// missingkey=error to say anything at all and would still accept a misspelled
// top-level key as a nil lookup.
type Ctx struct {
	Session SessionCtx
	Repo    RepoCtx
}

// probe is a Ctx with every field populated, used to render each template once at
// validation time. Every value is non-empty so that a render failure means "this
// placeholder does not exist", never "this session happens to have no branch" —
// that second question is MissingFields', and it is asked per selection at run time.
var probe = Ctx{
	Session: SessionCtx{Title: "probe", Name: "probe", Branch: "probe", Worktree: "/probe"},
	Repo:    RepoCtx{Path: "/probe", Name: "probe"},
}

// Command is a validated custom command, ready to render and run.
type Command struct {
	Key         string
	Description string
	Context     Context
	Output      Output
	Confirm     bool

	tmpl   *template.Template
	source string
}

// Problem is one rejected entry: which one, and why. Index is the entry's position
// in the configured list, so a user with several commands can find it.
type Problem struct {
	Index int
	Key   string
	Msg   string
}

// Error renders the problem as the user sees it.
func (p Problem) Error() string {
	if p.Key == "" {
		return fmt.Sprintf("custom_commands[%d]: %s", p.Index, p.Msg)
	}
	return fmt.Sprintf("custom_commands[%d] (%q): %s", p.Index, p.Key, p.Msg)
}

// funcs are the helpers a command template may call.
//
// quote is the escape hatch for a value carrying a space or a shell metacharacter.
// It is opt-in rather than automatic because a template renders into a shell string
// and Go templates have no notion of the context a value lands in — the alternative
// to opting in is the $ATRIUM_* environment, which needs no escaping at all.
var funcs = Funcs()

// Funcs returns the helper set a template rendering against Ctx may call.
//
// Exported because repo_scripts renders the same context through its own templates
// (repocfg), and a value the user is told to wrap in `quote` there has to have the
// function to wrap it with. A fresh map per call, so a caller cannot reach in and add
// to the set this package validates against.
func Funcs() template.FuncMap {
	return template.FuncMap{"quote": shellQuote}
}

// shellQuote wraps s in single quotes, escaping any single quote it contains, which
// is the one form POSIX sh treats as fully literal.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// Validate turns configured entries into runnable commands, reporting the ones it
// refused. The two results are independent: a config with one bad entry still yields
// every good one.
func Validate(entries []config.CustomCommand) ([]Command, []Problem) {
	var out []Command
	var problems []Problem
	// claimed maps a key to the description of the entry that took it, so a
	// collision can name both parties rather than just the loser.
	claimed := map[string]string{}

	for i, e := range entries {
		reject := func(format string, args ...any) {
			problems = append(problems, Problem{Index: i, Key: e.Key, Msg: fmt.Sprintf(format, args...)})
		}

		if err := checkKey(e.Key); err != nil {
			reject("%s", err)
			continue
		}
		// Description comes before the collision check, not after: the collision
		// message's whole job is naming both parties, and an entry with no
		// description cannot be one. Reversed, two nameless duplicates report
		// `so "" is ignored`, which names nobody.
		if strings.TrimSpace(e.Description) == "" {
			reject("description is required — it is all the menu and the ? screen can show")
			continue
		}
		if strings.TrimSpace(e.Command) == "" {
			reject("command is required")
			continue
		}
		if owner, taken := claimed[e.Key]; taken {
			// Name both parties: the incumbent AND this entry. A message that
			// reports only the key leaves the user grepping their config for which
			// of two identical-looking rows lost.
			reject("key %q is already bound to %q, so %q is ignored", e.Key, owner, e.Description)
			continue
		}

		ctx, err := parseContext(e.Context)
		if err != nil {
			reject("%s", err)
			continue
		}
		output, err := parseOutput(e.Output)
		if err != nil {
			reject("%s", err)
			continue
		}

		// No missingkey option: it governs map indexing only, and Ctx is a struct of
		// structs (TestFieldAccessCoversEveryContextLeaf enforces that shape), so an
		// unknown field is already an execution error. Setting it here would read as
		// load-bearing while doing nothing.
		tmpl, err := template.New(e.Key).Funcs(funcs).Parse(e.Command)
		if err != nil {
			reject("template does not parse: %v", err)
			continue
		}
		// Render against the probe rather than trusting the parse: parsing accepts
		// any field name, and only execution reports a misspelled one.
		if _, err := render(tmpl, probe); err != nil {
			reject("template does not render: %v", err)
			continue
		}

		claimed[e.Key] = e.Description
		out = append(out, Command{
			Key:         e.Key,
			Description: e.Description,
			Context:     ctx,
			Output:      output,
			Confirm:     e.Confirm,
			tmpl:        tmpl,
			source:      e.Command,
		})
	}
	return out, problems
}

// checkKey enforces what the menu can actually dispatch: one printable, non-space
// rune. Space is called out separately because it is printable but arrives as the
// key string "space", so a " " entry would validate here and then never fire.
func checkKey(key string) error {
	if key == "" {
		return fmt.Errorf("key is required")
	}
	r, size := utf8.DecodeRuneInString(key)
	// U+FFFD is rejected however it got here, and the size is deliberately not
	// consulted. Undecodable bytes handed straight to this package decode to
	// RuneError with size 1 — which satisfies size == len(key), and RuneError is
	// printable, so they would pass every other rule below. But the path that
	// actually reaches a user is the other one: encoding/json silently replaces
	// invalid UTF-8 in a string with U+FFFD, so binary garbage in config.json
	// arrives here already valid, three bytes wide, and a size-based test waves it
	// through. Either way the result is a key bound to something no keypress can
	// produce — a command silently missing from the menu with nothing to explain it.
	// Nobody loses a key they wanted: U+FFFD is not typeable.
	//
	// This runs before the single-character test so the message names the real
	// problem; reversed, two bad bytes are reported as being too long, which sends
	// the user counting characters instead of fixing their encoding.
	if r == utf8.RuneError {
		return fmt.Errorf("key %q is not usable text — a keypress can never produce it", key)
	}
	if size != len(key) {
		return fmt.Errorf("key %q must be a single character", key)
	}
	if r == ' ' {
		return fmt.Errorf("key cannot be the space bar")
	}
	if !unicode.IsPrint(r) {
		return fmt.Errorf("key must be a printable character")
	}
	// A combining mark is printable and one rune wide, so it clears every rule above
	// — and lands in the same place U+FFFD does: bound to something no keypress can
	// produce. U+0301 (combining acute) and U+FE0F (variation selector) are both Mn.
	// Only the non-spacing and enclosing categories are refused: a spacing mark (Mc)
	// is a character an Indic keyboard really does emit on its own.
	if unicode.In(r, unicode.Mn, unicode.Me) {
		// The codepoint, not just the character: a combining mark has no glyph of its
		// own, so %q prints it onto the closing quote and the user sees an empty pair.
		// U+0301 is something they can search their config for.
		return fmt.Errorf("key U+%04X is a combining mark, not a character a keypress can produce", r)
	}
	return nil
}

// parseContext resolves the context, defaulting an omitted one to session.
func parseContext(s string) (Context, error) {
	switch Context(s) {
	case "", ContextSession:
		return ContextSession, nil
	case ContextRepo:
		return ContextRepo, nil
	}
	return "", fmt.Errorf("context %q is not one of %q, %q", s, ContextSession, ContextRepo)
}

// parseOutput resolves the output mode. Unlike context it has no default: the modes
// differ enough that an implicit one would make "it took over my terminal" a
// surprise.
//
// Both messages are BUILT from outputModes rather than spelling the set again, because
// with no default they are the only place a user learns what the values are — and a
// hand-written enumeration is what made them stale the moment `terminal` landed.
func parseOutput(s string) (Output, error) {
	for _, m := range outputModes {
		if Output(s) == m.name {
			return m.name, nil
		}
	}
	if s == "" {
		return "", fmt.Errorf("output is required — %s", strings.Join(outputBlurbs(), ", "))
	}
	return "", fmt.Errorf("output %q is not one of %s", s, strings.Join(outputNames(), ", "))
}

// outputBlurbs describes each mode, for the required-field message.
func outputBlurbs() []string {
	out := make([]string, 0, len(outputModes))
	for _, m := range outputModes {
		out = append(out, fmt.Sprintf("%q %s", m.name, m.blurb))
	}
	return out
}

// outputNames lists each mode's value, for the unknown-value message.
func outputNames() []string {
	out := make([]string, 0, len(outputModes))
	for _, m := range outputModes {
		out = append(out, fmt.Sprintf("%q", m.name))
	}
	return out
}

// Render resolves the command's template against ctx.
func (c Command) Render(ctx Ctx) (string, error) { return render(c.tmpl, ctx) }

func render(t *template.Template, ctx Ctx) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, ctx); err != nil {
		return "", err
	}
	return b.String(), nil
}

// MissingFields reports which empty context fields actually reach the rendered
// command, in "Session.Branch" form.
//
// Validation proves a placeholder EXISTS; only this proves it carries a value for
// the session in hand. A direct (non-git) session has no branch, so without this
// `gh pr create --head {{.Session.Branch}}` renders to a trailing space and acts on
// whatever happens to be checked out.
//
// It works by re-rendering with each empty field replaced by a unique sentinel and
// looking for those sentinels in the output — asking the template engine which
// fields reached the command, rather than reimplementing its name resolution by
// walking the parse tree. That distinction is the whole correctness argument: a
// tree walk has to know every syntax that can reach a field, and the two it is
// easiest to forget — `{{with .Session}}…{{.Branch}}` and `{{(.Session).Branch}}` —
// both reach one without ever spelling it as a path, so a walk silently reports
// nothing missing and the guard reads as passing.
//
// It is also more precise in the other direction: a field behind a conditional the
// render does not take never reaches the command, so it is correctly not reported.
//
// The technique has one known limit, and it errs the safe way. A sentinel is
// non-empty, so a template that tests the emptiness of the very field being probed —
// {{if .Session.Branch}}…{{end}} — takes the opposite branch while probing and reads
// as using it. That over-reports, dimming a row that would have run; a false dim is
// visible and carries its reason, where a false pass runs a command with a hole in it.
//
// What keeps the failure on that side is the shape of the sentinel, not the search:
// the value passes through whatever the template does to it before landing in the
// output, so a sentinel a function can re-encode goes undetected. See sentinelFor.
//
// A render error reports nothing missing. Only validated templates get here, so an
// error means the command is broken outright — which its own run-time render
// surfaces as an error, rather than as a dimmed row.
func (c Command) MissingFields(ctx Ctx) []string {
	probeCtx := ctx
	var empty []string
	for _, f := range fieldAccess {
		if f.get(ctx) != "" {
			continue
		}
		empty = append(empty, f.path)
		f.set(&probeCtx, sentinelFor(f.path))
	}
	if len(empty) == 0 {
		return nil
	}
	out, err := render(c.tmpl, probeCtx)
	if err != nil {
		return nil
	}
	var missing []string
	for _, path := range empty {
		if strings.Contains(out, sentinelFor(path)) {
			missing = append(missing, path)
		}
	}
	return missing
}

// sentinelFor is the stand-in substituted for an empty field.
//
// It is deliberately plain letters — no NUL delimiters, no punctuation. The obvious
// choice reads safer and is not: MissingFields finds a sentinel by substring, but the
// value reaches the output through whatever the template does to it first, and every
// escaping builtin re-encodes a NUL. `{{ printf "%q" .Session.Branch }}` — the way a
// user who never finds quote reaches for shell quoting — renders a NUL as the four
// characters backslash-x-0-0, so the search misses, MissingFields reports nothing,
// and the command runs with an empty --head. `js` (to a unicode escape) and
// `urlquery` (to %00) defeat it the same way. Letters survive all three untouched,
// which is what keeps the over-reporting case documented above the ONLY limit.
//
// The cost is that a template echoing this exact string literally would report a
// field it does not use. That over-reports — the safe direction — and the string is
// distinctive enough that it is a theoretical cost, not a practical one.
func sentinelFor(path string) string {
	return "atriumMissingField" + strings.ReplaceAll(path, ".", "")
}

// fieldAccess is the leaf-by-leaf view of Ctx that MissingFields substitutes
// through, and the single source of truth for the two ways a context field reaches a
// command: its template path and its environment variable.
//
// The env name lives here rather than in Env for one reason — the two consumers must
// never disagree about which variable carries which field. MissingEnv decides whether
// to refuse a command by looking for a name in its script; Env decides what that name
// is worth. Spelling them separately is how the emptiness gate came to cover the
// template form and not the environment form of the same value.
//
// It is a hand-written table because Ctx is a fixed, tiny shape and reflection here
// would buy nothing but indirection — TestFieldAccessCoversEveryContextLeaf is what
// keeps it honest, in both directions.
var fieldAccess = []struct {
	path string
	env  string
	get  func(Ctx) string
	set  func(*Ctx, string)
}{
	{"Session.Title", "ATRIUM_TITLE", func(c Ctx) string { return c.Session.Title }, func(c *Ctx, v string) { c.Session.Title = v }},
	{"Session.Name", "ATRIUM_SESSION", func(c Ctx) string { return c.Session.Name }, func(c *Ctx, v string) { c.Session.Name = v }},
	{"Session.Branch", "ATRIUM_BRANCH", func(c Ctx) string { return c.Session.Branch }, func(c *Ctx, v string) { c.Session.Branch = v }},
	{"Session.Worktree", "ATRIUM_WORKTREE", func(c Ctx) string { return c.Session.Worktree }, func(c *Ctx, v string) { c.Session.Worktree = v }},
	{"Repo.Path", "ATRIUM_REPO", func(c Ctx) string { return c.Repo.Path }, func(c *Ctx, v string) { c.Repo.Path = v }},
	{"Repo.Name", "ATRIUM_REPO_NAME", func(c Ctx) string { return c.Repo.Name }, func(c *Ctx, v string) { c.Repo.Name = v }},
}

// MissingEnv reports which empty context fields the command's script may read through
// the $ATRIUM_* environment, in the same "Session.Branch" form as MissingFields.
//
// It exists because MissingFields cannot see this. That function asks the template
// renderer which fields reached the output, which is exactly right for a placeholder
// and blind to a variable the SHELL expands — so the two forms this package documents
// as interchangeable were not: `rm -rf {{ quote .Session.Worktree }}/build` was refused
// on a session with no worktree, and `rm -rf "$ATRIUM_WORKTREE"/build` ran, as
// `rm -rf /build`. Worse, `cd "$ATRIUM_WORKTREE" && rm -rf *` — `cd ""` succeeds and
// stays put — runs in the command's working directory, which for repo context is the
// repository root.
//
// It is a scan of the unrendered script, not an expansion of it, so it is deliberately
// approximate in the same direction MissingFields is: a name inside single quotes, or
// in a comment, or in a here-doc the shell never reaches, still counts. That
// over-refuses, which is visible and carries its reason. The alternative — deciding
// which occurrences a shell would actually expand — means implementing shell quoting,
// and being wrong there runs the command.
func (c Command) MissingEnv(ctx Ctx) []string {
	var missing []string
	for _, f := range fieldAccess {
		if f.get(ctx) == "" && mentionsVar(c.source, f.env) {
			missing = append(missing, f.path)
		}
	}
	return missing
}

// mentionsVar reports whether script references the shell variable name: `$NAME` or
// `${NAME}`, and not a longer name that merely starts with it.
//
// That last clause is the whole reason this is not strings.Contains: ATRIUM_REPO is a
// prefix of ATRIUM_REPO_NAME, so a naive check would refuse every command using
// $ATRIUM_REPO_NAME whenever Repo.Path happened to be empty, and report the wrong field
// while doing it.
func mentionsVar(script, name string) bool {
	for i := 0; ; {
		idx := strings.Index(script[i:], "$")
		if idx < 0 {
			return false
		}
		rest := script[i+idx+1:]
		i += idx + 1
		rest = strings.TrimPrefix(rest, "{")
		if !strings.HasPrefix(rest, name) {
			continue
		}
		// A name ends where a character that cannot be part of one begins. End of
		// string counts, so a script ending in the bare variable matches.
		after := rest[len(name):]
		if after == "" || !isNameByte(after[0]) {
			return true
		}
	}
}

// isNameByte reports whether b can appear inside a shell variable name. Only ASCII
// matters: the names are ours, and they are all ATRIUM_[A-Z_]+.
func isNameByte(b byte) bool {
	return b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z')
}

// LogArgv is the argv recorded in the command log for this command.
//
// It is deliberately NOT the rendered script. cmdlog.Redact models one NAME=VALUE
// per argv token, and a whole shell script in a single token defeats it twice: a
// bearer token inside a -H flag has no leading NAME= and is stored verbatim, while a
// leading FOO=bar match returns everything before the first '=' and throws the rest
// of the command away. Naming the command by key and description keeps the log
// useful and keeps user-authored secrets out of it.
func (c Command) LogArgv() []string {
	return []string{"atrium", "custom-command", c.Key, c.Description}
}

// Source is the command's unrendered template, for surfaces that want to show what
// was configured rather than what was run.
func (c Command) Source() string { return c.source }

// Env is the $ATRIUM_* environment exported to every custom command.
//
// It exists alongside the template so a user need never interpolate a path into a
// shell string: `lazygit -p "$ATRIUM_WORKTREE"` cannot break argument parsing however
// odd the path is. ATRIUM_SESSION carries the display name, matching what it means to
// a notify command. Note it does NOT match the ATRIUM_SESSION seen from inside an
// agent's pane, which tmux sets to the sanitized session handle
// (atrium_<group>_<title>, derived from the immutable Title) — a script written
// against one surface cannot assume the other.
// Built from fieldAccess rather than spelled out, so the names MissingEnv looks for in
// a script and the names this exports cannot drift apart.
func Env(ctx Ctx) []string {
	out := make([]string, 0, len(fieldAccess))
	for _, f := range fieldAccess {
		out = append(out, f.env+"="+f.get(ctx))
	}
	return out
}
