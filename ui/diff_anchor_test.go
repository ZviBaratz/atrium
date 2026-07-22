package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

// TestParseDiffRows pins the row model: rows are emitted in paint order (a rule
// before each file header), files attribute to the nearest "diff --git" b/<path>,
// and code lines carry the right file line — new-file line for adds/context,
// old-file line for deletions — tracked from the "@@" hunk headers.
func TestParseDiffRows(t *testing.T) {
	content := strings.Join([]string{
		"diff --git a/foo.go b/foo.go",
		"index 111..222 100644",
		"--- a/foo.go",
		"+++ b/foo.go",
		"@@ -1,3 +1,4 @@",
		" package foo",
		"+// added",
		" func A() {}",
		"-old",
		"diff --git a/bar.go b/bar.go",
		"@@ -10,2 +10,2 @@",
		"-removed",
		"+inserted",
	}, "\n")

	rows := parseDiffRows(content)

	kinds := make([]diffRowKind, len(rows))
	for i, r := range rows {
		kinds[i] = r.kind
	}
	require.Equal(t, []diffRowKind{
		rowRule, rowFileHeader, rowMeta, rowMeta, rowMeta, rowHunk,
		rowContext, rowAdd, rowContext, rowDel,
		rowRule, rowFileHeader, rowHunk, rowDel, rowAdd,
	}, kinds)

	// File header carries its post-change path.
	require.Equal(t, "foo.go", rows[1].file)
	require.Equal(t, "bar.go", rows[11].file)

	// Code lines: file + line number (new-file for add/context, old-file for del).
	require.Equal(t, diffRow{kind: rowContext, text: " package foo", file: "foo.go", lineNo: 1}, rows[6])
	require.Equal(t, diffRow{kind: rowAdd, text: "+// added", file: "foo.go", lineNo: 2}, rows[7])
	require.Equal(t, diffRow{kind: rowContext, text: " func A() {}", file: "foo.go", lineNo: 3}, rows[8])
	require.Equal(t, diffRow{kind: rowDel, text: "-old", file: "foo.go", lineNo: 3}, rows[9])

	// Second file's hunk starts at 10 for both sides.
	require.Equal(t, diffRow{kind: rowDel, text: "-removed", file: "bar.go", lineNo: 10}, rows[13])
	require.Equal(t, diffRow{kind: rowAdd, text: "+inserted", file: "bar.go", lineNo: 10}, rows[14])

	// Only code rows are annotatable.
	require.True(t, rows[7].annotatable(), "an added line is annotatable")
	require.False(t, rows[0].annotatable(), "a rule row is not annotatable")
	require.False(t, rows[5].annotatable(), "a hunk header is not annotatable")
}

// TestParseDiffRows_Empty returns no rows for an empty diff.
func TestParseDiffRows_Empty(t *testing.T) {
	require.Empty(t, parseDiffRows(""))
}

const cursorDiff = "diff --git a/foo.go b/foo.go\n" +
	"@@ -1,2 +1,3 @@\n" +
	" ctx1\n" +
	"+add1\n" +
	" ctx2\n"

// TestDiffCommentCursor pins comment-mode navigation: entering lands on the first
// code line, the cursor steps over annotatable rows only (skipping chrome), clamps
// at both ends, and exposes the row it sits on as the anchor.
func TestDiffCommentCursor(t *testing.T) {
	d := NewDiffPane()
	d.SetSize(80, 20)
	d.rows = parseDiffRows(cursorDiff)

	require.True(t, d.EnterComment(), "a diff with code lines can be commented")
	require.True(t, d.IsCommenting())

	// Lands on the first annotatable row: the context line " ctx1".
	a, ok := d.CommentAnchor()
	require.True(t, ok)
	require.Equal(t, diffRow{kind: rowContext, text: " ctx1", file: "foo.go", lineNo: 1}, a)

	// Down steps to the added line, then the second context line, then clamps.
	d.CursorDown()
	a, _ = d.CommentAnchor()
	require.Equal(t, "+add1", a.text)
	d.CursorDown()
	a, _ = d.CommentAnchor()
	require.Equal(t, " ctx2", a.text)
	d.CursorDown() // clamp at the last annotatable row
	a, _ = d.CommentAnchor()
	require.Equal(t, " ctx2", a.text)

	// Up walks back and clamps at the first.
	d.CursorUp()
	a, _ = d.CommentAnchor()
	require.Equal(t, "+add1", a.text)
	d.CursorUp()
	d.CursorUp() // clamp
	a, _ = d.CommentAnchor()
	require.Equal(t, " ctx1", a.text)

	d.ExitComment()
	require.False(t, d.IsCommenting())
}

// TestComposeDiffComment pins the queued-prompt text a single-line diff comment
// becomes: a file:line reference, the exact diff line quoted (marker kept, so the
// agent sees added vs removed), then the note — trimmed of surrounding whitespace.
func TestComposeDiffComment(t *testing.T) {
	rows := []diffRow{{kind: rowAdd, text: "+\tif cents <= 0 {", file: "payment.go", lineNo: 42}}
	got := composeDiffComment(rows, "  handle the zero case too\n")
	want := "Re: payment.go:42\n\n    +\tif cents <= 0 {\n\nhandle the zero case too"
	require.Equal(t, want, got)
}

// TestComposeDiffComment_Range pins a multi-line comment: a file:start-end reference
// and the whole selected block quoted in order, markers kept.
func TestComposeDiffComment_Range(t *testing.T) {
	rows := []diffRow{
		{kind: rowContext, text: " a", file: "x.go", lineNo: 10},
		{kind: rowDel, text: "-b", file: "x.go", lineNo: 11},
		{kind: rowAdd, text: "+c", file: "x.go", lineNo: 11},
	}
	got := composeDiffComment(rows, "look at this block")
	want := "Re: x.go:10-11\n\n     a\n    -b\n    +c\n\nlook at this block"
	require.Equal(t, want, got)
}

// TestComposeDiffComment_Deletion references the removed line's old-file position.
func TestComposeDiffComment_Deletion(t *testing.T) {
	rows := []diffRow{{kind: rowDel, text: "-\treturn nil", file: "a/b.go", lineNo: 7}}
	got := composeDiffComment(rows, "why drop the error?")
	require.Contains(t, got, "Re: a/b.go:7")
	require.Contains(t, got, "-\treturn nil")
	require.Contains(t, got, "why drop the error?")
}

// TestEnterCommentNoCodeLines declines the mode when the diff is all chrome.
func TestEnterCommentNoCodeLines(t *testing.T) {
	d := NewDiffPane()
	d.SetSize(80, 20)
	d.rows = parseDiffRows("diff --git a/x b/x\nindex 1..2\n")
	require.False(t, d.EnterComment(), "no code lines means nothing to comment")
	require.False(t, d.IsCommenting())
}

// TestDiffCommentRange pins J/K range selection: it starts single-line, grows a
// contiguous code block downward, clamps at hunk boundaries (a range can't span the
// @@ header the way j/k navigation can), and a plain move collapses it again.
func TestDiffCommentRange(t *testing.T) {
	d := NewDiffPane()
	d.SetSize(80, 20)
	d.rows = parseDiffRows(cursorDiff) // foo.go code lines " ctx1", "+add1", " ctx2"

	require.True(t, d.EnterComment())
	require.Len(t, d.selectedRows(), 1, "starts as a single-line selection")

	// Extend up at the first code line clamps — the hunk header sits above it.
	d.ExtendUp()
	require.Len(t, d.selectedRows(), 1, "extend clamps at the hunk boundary")

	// Extend down grows a contiguous range, in order.
	d.ExtendDown()
	rows := d.selectedRows()
	require.Len(t, rows, 2)
	require.Equal(t, " ctx1", rows[0].text)
	require.Equal(t, "+add1", rows[1].text)

	d.ExtendDown()
	require.Len(t, d.selectedRows(), 3, "spans the whole hunk's code lines")
	d.ExtendDown()
	require.Len(t, d.selectedRows(), 3, "extend clamps at the end of the hunk")

	// A range location reads file:start-end.
	loc, ok := d.CommentLocation()
	require.True(t, ok)
	require.Equal(t, "foo.go:1-3", loc)

	// A plain move collapses the selection back to a single line.
	d.CursorUp()
	rows = d.selectedRows()
	require.Len(t, rows, 1, "j/k collapses the selection")
	require.Equal(t, "+add1", rows[0].text)
}

// TestParseDiffRows_DeletedFile checks that del-row line numbers are correct when the
// hunk header has a zero new-file start (@@ -1,N +0,0 @@), which occurs for completely
// deleted files. Before the fix, parseDiffRows required both sides of the header to be
// > 0 before advancing either counter, so the zero new-file start suppressed the update
// entirely, leaving oldLine at 0 and the first del row numbered 0 (later rows carried
// 1, 2, … since oldLine++ still ran — so the whole hunk was off by one).
func TestParseDiffRows_DeletedFile(t *testing.T) {
	diff := "diff --git a/gone.go b/gone.go\n" +
		"@@ -1,3 +0,0 @@\n" +
		"-package gone\n" +
		"-func A() {}\n" +
		"-func B() {}\n"
	rows := parseDiffRows(diff)
	var delRows []diffRow
	for _, r := range rows {
		if r.kind == rowDel {
			delRows = append(delRows, r)
		}
	}
	require.Len(t, delRows, 3)
	require.Equal(t, 1, delRows[0].lineNo, "first del row must carry old-file line 1, not 0")
	require.Equal(t, 2, delRows[1].lineNo)
	require.Equal(t, 3, delRows[2].lineNo)
}

// TestParseDiffRows_NewFile is the mirror for @@ -0,0 +1,N @@ (new file): add rows
// must carry the correct new-file line numbers even when oldStart == 0.
func TestParseDiffRows_NewFile(t *testing.T) {
	diff := "diff --git a/new.go b/new.go\n" +
		"@@ -0,0 +1,2 @@\n" +
		"+package newpkg\n" +
		"+func C() {}\n"
	rows := parseDiffRows(diff)
	var addRows []diffRow
	for _, r := range rows {
		if r.kind == rowAdd {
			addRows = append(addRows, r)
		}
	}
	require.Len(t, addRows, 2)
	require.Equal(t, 1, addRows[0].lineNo, "first add row must carry new-file line 1, not 0")
	require.Equal(t, 2, addRows[1].lineNo)
}

// wideCursorDiff mirrors cursorDiff but carries a context line longer than 40 columns,
// so re-truncation at a narrowed width is observable and not just re-padding.
const wideCursorDiff = "diff --git a/foo.go b/foo.go\n" +
	"@@ -1,2 +1,3 @@\n" +
	" ctx1\n" +
	"+add1\n" +
	" this context line is definitely longer than forty columns wide\n"

// TestDiffCommentSetSize_RefreshesOnResize is the regression guard for the
// resize-in-comment-mode bug: when the terminal is resized while the diff pane is
// frozen in comment mode, SetSize must re-render the snapshot at the new width.
// Before the fix, d.diff kept its old-width render, so the cursor highlight bar was
// padded/truncated to the wrong column count after a resize.
//
// It asserts the rendered column count rather than merely that the snapshot changed:
// renderDiffRows paints the selected row via diffCursorStyle().Width(width), so the
// cursor row's width IS the width the snapshot was rendered at. A "did it change"
// assertion alone passes against a re-render at the wrong width, and against one that
// drops the highlight entirely — both of which are the bug, not the fix.
func TestDiffCommentSetSize_RefreshesOnResize(t *testing.T) {
	d := NewDiffPane()
	d.SetSize(80, 20)
	d.rows = parseDiffRows(wideCursorDiff)
	require.True(t, d.EnterComment())

	line := func(i int) string { return strings.Split(d.diff, "\n")[i] }
	cursorRow := func() string { return line(d.cursor) }

	require.Equal(t, 80, lipgloss.Width(cursorRow()), "cursor bar spans the pane at width 80")

	d.SetSize(40, 20)

	require.Equal(t, 40, lipgloss.Width(cursorRow()),
		"SetSize must re-render the frozen comment snapshot at the new width")
	require.Contains(t, cursorRow(), "ctx1", "the re-rendered bar still carries its own row's text")
	// An unselected row is not padded, so the width above came from the highlight bar
	// rather than from blanket padding of every line.
	require.Equal(t, 5, lipgloss.Width(line(d.cursor+1)), "unselected rows are not padded")
	// Over-long rows re-truncate to the new width: fit() truncates to width-1 and
	// appends a one-column ellipsis, so a truncated row lands at width-1.
	require.Equal(t, 39, lipgloss.Width(line(d.cursor+2)), "long rows truncate to the new width")
	require.True(t, d.IsCommenting(), "comment mode must survive a resize")
}

// noNewlineDiff is verbatim `git diff` output for a repo where one file with no
// trailing newline is deleted and another is added. It is the case that makes the
// whole-file delete/add headers interact: git emits "\ No newline at end of file"
// *inside* the zero-side hunks, and both files appear in one diff.
const noNewlineDiff = "diff --git a/brand.txt b/brand.txt\n" +
	"new file mode 100644\nindex 0000000..5fe505c\n--- /dev/null\n+++ b/brand.txt\n" +
	"@@ -0,0 +1,2 @@\n+n1\n+n2\n\\ No newline at end of file\n" +
	"diff --git a/gone.txt b/gone.txt\n" +
	"deleted file mode 100644\nindex 1b32298..0000000\n--- a/gone.txt\n+++ /dev/null\n" +
	"@@ -1,2 +0,0 @@\n-x\n-y\n\\ No newline at end of file\n"

// TestParseDiffRows_NoNewlineMarkerAcrossFiles pins two things the single-file
// delete/add tests cannot see, both of which produced a wrong "Re: file:N" reference.
//
// First, git's "\ No newline at end of file" is a marker, not content: it must not be
// annotatable (a comment cannot anchor to it) and must not advance either counter —
// when it did, every row after it was numbered one too high.
//
// Second, a zero side must not inherit the previous file's counter. gone.txt is deleted
// whole, so its new side has no lines; carrying brand.txt's count across the file
// boundary made the marker row report gone.txt:4 — a confident, entirely wrong line
// number for a two-line file. Only real code rows may be annotatable, each numbered
// against its own file.
func TestParseDiffRows_NoNewlineMarkerAcrossFiles(t *testing.T) {
	type ref struct {
		file   string
		lineNo int
		text   string
	}
	var got []ref
	for _, r := range parseDiffRows(noNewlineDiff) {
		if r.annotatable() {
			got = append(got, ref{r.file, r.lineNo, r.text})
		}
	}
	require.Equal(t, []ref{
		{"brand.txt", 1, "+n1"},
		{"brand.txt", 2, "+n2"},
		{"gone.txt", 1, "-x"},
		{"gone.txt", 2, "-y"},
	}, got, "only real code lines anchor comments, each numbered against its own file")
}

// TestParseDiffRows_DashDashContent is the regression guard for issue #435: a diff
// hunk that deletes a line starting with "--" (so the diff line is "---divider")
// and adds a line starting with "++" (so the diff line is "+++banner") must be
// classified as rowDel / rowAdd — not rowMeta — inside the hunk. The literals are
// deliberately three characters wide, exactly as long as the "--- a/x" file-header
// prefix they collide with: the old guard `line[1] != '-'` excluded "---divider"
// from the deletion case and isDiffMeta then matched the bare "---" prefix,
// misclassifying the row and corrupting every subsequent line number in the hunk.
func TestParseDiffRows_DashDashContent(t *testing.T) {
	diff := "diff --git a/x.go b/x.go\n" +
		"@@ -1,3 +1,3 @@\n" +
		" ctx\n" +
		"---divider\n" + // delete a line that started with "--"
		"+++banner\n" + // add a line that started with "++"
		" after\n"
	rows := parseDiffRows(diff)

	var codeRows []diffRow
	for _, r := range rows {
		if r.annotatable() {
			codeRows = append(codeRows, r)
		}
	}
	require.Len(t, codeRows, 4)

	// "---divider" is a deletion.
	require.Equal(t, rowDel, codeRows[1].kind, "---divider must be rowDel, not rowMeta")
	require.Equal(t, 2, codeRows[1].lineNo, "del row carries the old-file line number")

	// "+++banner" is an addition.
	require.Equal(t, rowAdd, codeRows[2].kind, "+++banner must be rowAdd, not rowMeta")
	require.Equal(t, 2, codeRows[2].lineNo, "add row carries the new-file line number")

	// The context row after the mis-classified row must not have its line number
	// corrupted by a skipped counter increment.
	require.Equal(t, rowContext, codeRows[3].kind)
	require.Equal(t, 3, codeRows[3].lineNo, "context row after the fix must be line 3, not 2")
}

// TestParseHunkHeader exercises parseHunkHeader directly for edge cases that
// TestParseDiffRows only covers indirectly via the full parser: the count-omitted
// form git uses for single-line hunks, trailing context text after the closing "@@",
// the whole-file delete/add headers whose zero side this fix hinges on, and malformed
// lines. Every case asserts both returned values — a header that parses to 0 must be
// pinned to 0 just as tightly as one that parses to a real line, since 0 is what tells
// parseDiffRows to leave that side's counter alone.
func TestParseHunkHeader(t *testing.T) {
	tests := []struct {
		line     string
		oldStart int
		newStart int
	}{
		{"@@ -1,3 +1,4 @@", 1, 1},
		{"@@ -10,2 +10,3 @@", 10, 10},
		{"@@ -10 +10 @@", 10, 10},              // count omitted (single-line hunk)
		{"@@ -1,3 +1,4 @@ func Foo() {", 1, 1}, // trailing context text
		{"@@ -5,0 +6,3 @@", 5, 6},              // zero-count old side
		{"@@ -1,3 +0,0 @@", 1, 0},              // whole file deleted: new side has no lines
		{"@@ -0,0 +1,3 @@", 0, 1},              // new file: old side has no lines
		{"not a hunk header", 0, 0},
		{"@@ missing fields", 0, 0},
		// Trailing context text may itself start with '-' or '+'. Only the first field
		// per side counts, so a zero side stays 0 instead of being claimed by that text.
		{"@@ -0,0 +1,3 @@ -7 items", 0, 1},
		{"@@ -1,3 +0,0 @@ +5 more", 1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			oldStart, newStart := parseHunkHeader(tc.line)
			require.Equal(t, tc.oldStart, oldStart, "oldStart mismatch")
			require.Equal(t, tc.newStart, newStart, "newStart mismatch")
		})
	}
}
