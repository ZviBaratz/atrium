package ui

import (
	"strings"
	"testing"

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
