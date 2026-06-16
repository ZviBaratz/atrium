package transcript

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/ZviBaratz/atrium/ui/theme"
)

// truncationHeader announces a tail-capped transcript instead of silently
// dropping history.
const truncationHeader = "— transcript truncated —"

// renderEntries renders parsed entries to match what the attached Claude
// session showed: user prompts ("❯ ") and assistant prose ("● ") with markdown,
// a run of tool calls collapsed into one dim aggregate line ("Ran 3 shell
// commands, recalled 1 memory…"), errored tool results surfaced, thinking and
// successful tool output omitted. Sections are separated by a blank line.
func renderEntries(entries []entry, truncated bool, width int) string {
	dim := theme.Current().DimStyle()
	st := mdStyleSet()
	var sections []string
	if truncated {
		sections = append(sections, dim.Render(truncationHeader))
	}

	// A run of tool_use blocks — possibly spanning several assistant entries with
	// interleaved successful tool_results — collapses into one aggregate line,
	// flushed when the next prose, error, image, or the end of the stream breaks
	// the run.
	var group toolCounts
	imgN := 0
	flush := func() {
		if line := group.line(width, dim); line != "" {
			sections = append(sections, line)
		}
		group = toolCounts{}
	}

	for _, e := range entries {
		for _, b := range e.Blocks {
			switch b.Kind {
			case "text":
				if e.Role == "user" {
					display, skip := cleanUserText(b.Text)
					if skip {
						continue
					}
					flush()
					sections = append(sections, renderProse(display, "❯ ", width, st))
				} else {
					flush()
					sections = append(sections, renderProse(b.Text, "● ", width, st))
				}
			case "tool_use":
				group.add(b.ToolName, b.ToolInput)
			case "tool_result":
				// Successful results keep the run intact (so consecutive tools
				// collapse); an error breaks it and surfaces its first line.
				if b.IsError {
					flush()
					sections = append(sections, dim.Render(oneLine("  ⎿ error: "+firstLine(b.Text), width)))
				}
			case "image":
				flush()
				imgN++
				sections = append(sections, dim.Render(fmt.Sprintf("  [Image #%d]", imgN)))
			}
			// "thinking" is deliberately omitted: it routinely outweighs the
			// answer and isn't what a scrollback reviewer is after.
		}
	}
	flush()
	return strings.Join(sections, "\n\n")
}

// mdStyleSet builds the markdown style set from the active theme. Italic and
// strikethrough have no dedicated theme accessor (they are attributes, not
// colors), so they are composed inline on the default foreground.
func mdStyleSet() mdStyles {
	t := theme.Current()
	return mdStyles{
		Bold:    t.BoldStyle(),
		Italic:  lipgloss.NewStyle().Italic(true),
		Strike:  lipgloss.NewStyle().Strikethrough(true),
		Code:    t.CodeStyle(),
		Link:    t.LinkStyle(),
		Heading: t.BoldStyle(),
		Quote:   t.DimStyle(),
		Fence:   t.FaintStyle(),
	}
}

// renderProse renders a markdown text body to wrapped lines under a block lead.
// firstLead leads the very first visual row ("● " for assistant prose, "❯ " for
// a user prompt); every later row hangs at the same width. Each markdown line's
// own marker (list bullet, quote bar) rides the base indent and its content
// wraps with an aligned hang.
func renderProse(text, firstLead string, width int, st mdStyles) string {
	base := strings.Repeat(" ", lipgloss.Width(firstLead))
	var rows []string
	first := true
	for _, ml := range renderMarkdown(text, st) {
		if ml.Text == "" && ml.Marker == "" {
			rows = append(rows, "")
			continue
		}
		lead := base
		if first {
			lead = firstLead
			first = false
		}
		prefix := lead + ml.Marker
		if ml.NoWrap {
			rows = append(rows, oneLine(prefix+ml.Text, width))
			continue
		}
		cont := base + strings.Repeat(" ", lipgloss.Width(ml.Marker))
		rows = append(rows, wrapStyled(ml.Text, prefix, cont, width))
	}
	// Trim trailing blank rows so a body ending in newlines doesn't inflate the
	// blank-line separation between entries.
	for len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}
	return strings.Join(rows, "\n")
}

// toolCat buckets a tool call into the category whose wording Claude Code uses
// when it collapses a turn's tools into one status line.
type toolCat int

const (
	catShell toolCat = iota
	catRead
	catEdit
	catSearch
	catAgent
	catWeb
	catTodo
	catMemRead
	catMemWrite
	catMCP
	catGeneric
	numCats
)

// catWording is the (verb, singular-noun, plural-noun) for each category, plus a
// countless flag for categories rendered without a number ("updated the todo
// list"). The clause order in an aggregate line follows the toolCat iota order,
// so a line reads "Ran 3 shell commands, recalled 1 memory, wrote 3 memories".
var catWording = [numCats]struct {
	verb, one, many string
	countless       bool
}{
	catShell:    {verb: "ran", one: "shell command", many: "shell commands"},
	catRead:     {verb: "read", one: "file", many: "files"},
	catEdit:     {verb: "made", one: "edit", many: "edits"},
	catSearch:   {verb: "ran", one: "search", many: "searches"},
	catAgent:    {verb: "ran", one: "agent", many: "agents"},
	catWeb:      {verb: "made", one: "web request", many: "web requests"},
	catTodo:     {verb: "updated", one: "the todo list", countless: true},
	catMemRead:  {verb: "recalled", one: "memory", many: "memories"},
	catMemWrite: {verb: "wrote", one: "memory", many: "memories"},
	catMCP:      {verb: "called", one: "tool", many: "tools"},
	catGeneric:  {verb: "used", one: "tool", many: "tools"},
}

// categorize maps a tool name (and, for file tools, its target path) to a
// category. File reads/writes whose path is in a memory directory become the
// memory categories — that is how Claude Code's "recalled/wrote N memories"
// wording is reconstructed, since memory ops are plain Read/Write calls.
func categorize(name, input string) toolCat {
	switch name {
	case "Bash", "BashOutput", "KillShell":
		return catShell
	case "Read", "NotebookRead":
		if isMemoryPath(input) {
			return catMemRead
		}
		return catRead
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		if isMemoryPath(input) {
			return catMemWrite
		}
		return catEdit
	case "Grep", "Glob", "LS":
		return catSearch
	case "Task", "Agent":
		return catAgent
	case "WebFetch", "WebSearch":
		return catWeb
	case "TodoWrite":
		return catTodo
	}
	if strings.HasPrefix(name, "mcp__") {
		return catMCP
	}
	return catGeneric
}

// isMemoryPath reports whether a file tool's input targets a memory store: a
// path under a "memory/" directory or a MEMORY.md index.
func isMemoryPath(input string) bool {
	p := toolPath(input)
	if p == "" {
		return false
	}
	if path.Base(p) == "MEMORY.md" {
		return true
	}
	return strings.Contains(p, "/memory/")
}

// toolPath extracts the file_path/path scalar from a tool input, or "".
func toolPath(input string) string {
	var m map[string]any
	if json.Unmarshal([]byte(input), &m) != nil {
		return ""
	}
	for _, k := range []string{"file_path", "path"} {
		if v, ok := m[k].(string); ok {
			return v
		}
	}
	return ""
}

// toolCounts accumulates a run of tool calls by category so the run renders as
// one aggregate line.
type toolCounts [numCats]int

func (tc *toolCounts) add(name, input string) { tc[categorize(name, input)]++ }

// line renders the accumulated run as a dim, two-space-indented aggregate, or
// "" when no tools were seen. Clauses follow category order; the first is
// capitalized ("Ran 3 shell commands, recalled 1 memory").
func (tc *toolCounts) line(width int, dim lipgloss.Style) string {
	var clauses []string
	for cat := toolCat(0); cat < numCats; cat++ {
		if tc[cat] == 0 {
			continue
		}
		clauses = append(clauses, catClause(cat, tc[cat]))
	}
	if len(clauses) == 0 {
		return ""
	}
	return dim.Render(oneLine("  "+capitalizeFirst(strings.Join(clauses, ", ")), width))
}

// catClause renders one category's count, e.g. "ran 3 shell commands" or the
// count-suppressed "updated the todo list".
func catClause(cat toolCat, n int) string {
	w := catWording[cat]
	if w.countless {
		return w.verb + " " + w.one
	}
	noun := w.many
	if n == 1 {
		noun = w.one
	}
	return fmt.Sprintf("%s %d %s", w.verb, n, noun)
}

// capitalizeFirst upper-cases the first rune of s.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// cleanUserText prepares a user text block for display. A slash command stored
// as <command-name>/x</command-name> renders as the bare "/x"; machine-plumbing
// blocks (command stdout/args, local-command caveats) are skipped entirely so
// they never appear as stray prompts.
func cleanUserText(s string) (display string, skip bool) {
	t := strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(t, "<command-name>"); ok {
		if i := strings.Index(rest, "</command-name>"); i >= 0 {
			if name := strings.TrimSpace(rest[:i]); name != "" {
				return name, false
			}
		}
		return "", true
	}
	for _, tag := range []string{"<local-command-stdout>", "<command-message>", "<command-args>", "<bash-stdout>", "<bash-stderr>"} {
		if strings.HasPrefix(t, tag) {
			return "", true
		}
	}
	// Claude Code's local-command caveat boilerplate, not real prose. Match the
	// full opening clause so a genuine user message that merely starts with
	// "Caveat:" is preserved.
	if strings.HasPrefix(t, "Caveat: The messages below were generated by the user") {
		return "", true
	}
	return s, false
}

// firstLine returns the trimmed first line of s.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// wrapStyled word-wraps already-styled text to width with a single ANSI-aware
// pass (ansi.Wrap preserves SGR sequences and only breaks a word that genuinely
// overflows), then hangs every wrapped row under the first: prefix leads row 0,
// cont leads rows 1..n. Wrapping before applying the lead is deliberate — the
// old wordwrap+wrap double pass re-wrapped an already-wrapped string and could
// split a word mid-token ("fuz\nzy") when a hanging indent shifted the two
// passes out of phase. width <= 0 leaves the body unwrapped behind the prefix.
func wrapStyled(styled, prefix, cont string, width int) string {
	if width <= 0 {
		return prefix + styled
	}
	// Deduct the widest lead so no row can overflow regardless of which lead it
	// carries; prefix and cont are equal-width in practice (marker+space vs two
	// spaces), so this is exact, not conservative.
	inner := width - max(lipgloss.Width(prefix), lipgloss.Width(cont))
	if inner < 1 {
		inner = 1
	}
	rows := strings.Split(ansi.Wrap(styled, inner, ""), "\n")
	for i := range rows {
		if i == 0 {
			rows[i] = prefix + rows[i]
		} else {
			rows[i] = cont + rows[i]
		}
	}
	return strings.Join(rows, "\n")
}

// oneLine truncates s to a single line of at most width cells. ansi.Truncate is
// escape-safe, so it is correct whether s is plain or already styled.
func oneLine(s string, width int) string {
	if width <= 0 {
		return s
	}
	return ansi.Truncate(s, width, "…")
}
