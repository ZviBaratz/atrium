package transcript

import (
	"encoding/json"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/ZviBaratz/atrium/ui/theme"
)

// truncationHeader announces a tail-capped transcript instead of silently
// dropping history.
const truncationHeader = "— transcript truncated —"

// renderEntries renders parsed entries at Lean fidelity: user prompts and
// assistant prose in full, tool calls as dim one-liners, errored tool results
// surfaced, thinking and successful tool output omitted. Entries are separated
// by a blank line; everything is wrapped (or one-liners truncated) to width.
func renderEntries(entries []entry, truncated bool, width int) string {
	dim := theme.Current().DimStyle()
	st := mdStyleSet()
	var sections []string
	if truncated {
		sections = append(sections, dim.Render(truncationHeader))
	}
	for _, e := range entries {
		var lines []string
		for _, b := range e.Blocks {
			switch b.Kind {
			case "text":
				lead := "● "
				if e.Role == "user" {
					lead = "❯ "
				}
				lines = append(lines, renderProse(b.Text, lead, width, st))
			case "tool_use":
				lines = append(lines, dim.Render(oneLine(toolLine(b), width)))
			case "tool_result":
				if b.IsError {
					lines = append(lines, dim.Render(oneLine("  ⎿ error: "+firstLine(b.Text), width)))
				}
			case "image":
				lines = append(lines, dim.Render("  [image]"))
			}
			// "thinking" is deliberately omitted: it routinely outweighs the
			// answer and isn't what a scrollback reviewer is after.
		}
		if len(lines) > 0 {
			sections = append(sections, strings.Join(lines, "\n"))
		}
	}
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

// toolLine compresses a tool_use block to "⏺ Name: summary" (or "⏺ Name" when
// no summary is recognizable).
func toolLine(b block) string {
	if summary := toolSummary(b.ToolInput); summary != "" {
		return "⏺ " + b.ToolName + ": " + summary
	}
	return "⏺ " + b.ToolName
}

// toolSummary extracts the most human-readable scalar from a tool input. The
// key preference is ordered (not map iteration) so output is deterministic:
// a Bash call prefers its description over the command, file tools surface
// their path.
func toolSummary(rawInput string) string {
	var m map[string]any
	if json.Unmarshal([]byte(rawInput), &m) != nil {
		return ""
	}
	for _, k := range []string{"description", "file_path", "path", "command", "pattern", "skill", "query", "prompt", "url"} {
		if v, ok := m[k].(string); ok && v != "" {
			return firstLine(v)
		}
	}
	return ""
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
