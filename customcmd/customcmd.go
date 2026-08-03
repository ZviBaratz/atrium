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
// back to DefaultConfig" — and it is called from a dozen non-TUI sites, so a config
// problem must never be an error return that ripples out to them. Validate reports
// problems alongside the entries that survived, and the caller decides how loudly to
// say so.
//
// A template is validated by RENDERING it, not by parsing it. Parsing accepts
// {{.Session.Wortree}} happily; only execution against a populated context reports
// the typo. Doing that at load time is what turns a placeholder typo into a message
// the user can act on instead of an empty string handed to a shell.
package customcmd

import (
	"fmt"
	"strings"
	"text/template"
	"text/template/parse"
	"unicode"
	"unicode/utf8"

	"github.com/ZviBaratz/atrium/config"
)

// Context selects the directory a command runs in.
type Context string

const (
	// ContextSession runs in the agent's working directory. It is the default, and
	// the app gates it on a started, unpaused session — before Start, an instance's
	// working directory is still the user's origin checkout.
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
)

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
	// fields lists the context fields this template references, in "Session.Branch"
	// form, deduplicated and in first-appearance order. Collected at validation time
	// so MissingFields costs nothing per keypress.
	fields []string
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
var funcs = template.FuncMap{"quote": shellQuote}

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
		if owner, taken := claimed[e.Key]; taken {
			// Name both parties: the incumbent AND this entry. A message that
			// reports only the key leaves the user grepping their config for which
			// of two identical-looking rows lost.
			reject("key %q is already bound to %q, so %q is ignored", e.Key, owner, e.Description)
			continue
		}
		if strings.TrimSpace(e.Description) == "" {
			reject("description is required — it is all the menu and the ? screen can show")
			continue
		}
		if strings.TrimSpace(e.Command) == "" {
			reject("command is required")
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

		tmpl, err := template.New(e.Key).Funcs(funcs).Option("missingkey=error").Parse(e.Command)
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
			fields:      referencedFields(tmpl),
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
	if size != len(key) {
		return fmt.Errorf("key %q must be a single character", key)
	}
	if r == ' ' {
		return fmt.Errorf("key cannot be the space bar")
	}
	if !unicode.IsPrint(r) {
		return fmt.Errorf("key must be a printable character")
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
func parseOutput(s string) (Output, error) {
	if s == "" {
		return "", fmt.Errorf("output is required — %q runs it detached and reports the exit status", OutputBackground)
	}
	if Output(s) == OutputBackground {
		return OutputBackground, nil
	}
	return "", fmt.Errorf("output %q is not %q", s, OutputBackground)
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

// MissingFields reports the context fields this command's template references that
// are empty for ctx, in "Session.Branch" form.
//
// Validation proves a placeholder EXISTS; only this proves it has a value for the
// session in hand. A direct (non-git) session has no branch, so without this
// `gh pr create --head {{.Session.Branch}}` renders to a trailing space and acts on
// whatever happens to be checked out.
func (c Command) MissingFields(ctx Ctx) []string {
	var missing []string
	for _, f := range c.fields {
		if fieldValue(ctx, f) == "" {
			missing = append(missing, f)
		}
	}
	return missing
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
// odd the path is. ATRIUM_SESSION carries the display name, matching what it already
// means to a notify command and inside an agent's tmux session.
func Env(ctx Ctx) []string {
	return []string{
		"ATRIUM_TITLE=" + ctx.Session.Title,
		"ATRIUM_SESSION=" + ctx.Session.Name,
		"ATRIUM_BRANCH=" + ctx.Session.Branch,
		"ATRIUM_WORKTREE=" + ctx.Session.Worktree,
		"ATRIUM_REPO=" + ctx.Repo.Path,
		"ATRIUM_REPO_NAME=" + ctx.Repo.Name,
	}
}

// fieldValue reads a "Session.Branch"-form path out of ctx. An unknown path reports
// a non-empty sentinel rather than "": only validated templates reach here, so an
// unrecognised path means this function has fallen behind Ctx, and reporting it as
// missing would dim a row for a field that is actually fine.
func fieldValue(ctx Ctx, path string) string {
	switch path {
	case "Session.Title":
		return ctx.Session.Title
	case "Session.Name":
		return ctx.Session.Name
	case "Session.Branch":
		return ctx.Session.Branch
	case "Session.Worktree":
		return ctx.Session.Worktree
	case "Repo.Path":
		return ctx.Repo.Path
	case "Repo.Name":
		return ctx.Repo.Name
	}
	return "unknown"
}

// referencedFields walks the parsed template for field references, returning them in
// "Session.Branch" form, deduplicated and in first-appearance order.
func referencedFields(t *template.Template) []string {
	seen := map[string]bool{}
	var out []string
	for _, tree := range t.Templates() {
		// tree.Tree is the embedded *parse.Tree and must be nil-checked by its own
		// name; Root then reads through the embedding.
		if tree.Tree == nil || tree.Root == nil {
			continue
		}
		walkFields(tree.Root, func(path string) {
			if seen[path] {
				return
			}
			seen[path] = true
			out = append(out, path)
		})
	}
	return out
}

// walkFields visits every parse.FieldNode under n. A FieldNode's Ident is already
// the dotted path split into segments ({{.Session.Branch}} -> ["Session","Branch"]),
// which is why this needs no name resolution of its own.
func walkFields(n parse.Node, visit func(string)) {
	switch node := n.(type) {
	case nil:
		return
	case *parse.FieldNode:
		visit(strings.Join(node.Ident, "."))
	case *parse.ListNode:
		if node == nil {
			return
		}
		for _, child := range node.Nodes {
			walkFields(child, visit)
		}
	case *parse.ActionNode:
		walkFields(node.Pipe, visit)
	case *parse.PipeNode:
		if node == nil {
			return
		}
		for _, cmd := range node.Cmds {
			walkFields(cmd, visit)
		}
	case *parse.CommandNode:
		for _, arg := range node.Args {
			walkFields(arg, visit)
		}
	case *parse.IfNode:
		walkBranch(node.BranchNode, visit)
	case *parse.RangeNode:
		walkBranch(node.BranchNode, visit)
	case *parse.WithNode:
		walkBranch(node.BranchNode, visit)
	}
}

func walkBranch(b parse.BranchNode, visit func(string)) {
	walkFields(b.Pipe, visit)
	if b.List != nil {
		walkFields(b.List, visit)
	}
	if b.ElseList != nil {
		walkFields(b.ElseList, visit)
	}
}
