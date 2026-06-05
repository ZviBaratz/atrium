package transcript

import (
	"encoding/json"
	"strings"

	"github.com/mattn/go-runewidth"
	"github.com/muesli/reflow/wordwrap"
	"github.com/muesli/reflow/wrap"

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
	var sections []string
	if truncated {
		sections = append(sections, dim.Render(truncationHeader))
	}
	for _, e := range entries {
		var lines []string
		for _, b := range e.Blocks {
			switch b.Kind {
			case "text":
				text := b.Text
				if e.Role == "user" {
					text = "❯ " + text
				}
				lines = append(lines, wrapTo(text, width))
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

// wrapTo word-wraps s to width, then hard-wraps so an unbroken token (a long
// path, a URL) can never overflow the pane. width <= 0 leaves s untouched.
func wrapTo(s string, width int) string {
	if width <= 0 {
		return s
	}
	return wrap.String(wordwrap.String(s, width), width)
}

// oneLine truncates s to a single line of at most width cells. It runs on
// plain text before any styling, so cell-width truncation is safe.
func oneLine(s string, width int) string {
	if width <= 0 {
		return s
	}
	return runewidth.Truncate(s, width, "…")
}
