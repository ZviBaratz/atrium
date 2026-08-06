package overlay

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

func checkpointRows(n int) []CheckpointRow {
	base := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	rows := make([]CheckpointRow, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, CheckpointRow{
			When:  base.Add(time.Duration(i) * time.Minute),
			Label: "prompt number " + string(rune('a'+i%26)),
			Files: i,
		})
	}
	return rows
}

// The overlay starts in its loading state, because the enumeration is a file
// read the app performs off the UI thread — there is never data at open time.
func TestCheckpointOverlay_StartsLoading(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	out := stripANSI(o.Render())
	if !strings.Contains(out, "reading transcript") {
		t.Errorf("a fresh overlay should show its loading state:\n%s", out)
	}
	if !strings.Contains(out, `Checkpoints for "alpha"`) {
		t.Errorf("title missing:\n%s", out)
	}
}

// Both intents are read-once, so the app acts on each press exactly once.
func TestCheckpointOverlay_IntentsAreReadOnce(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.SetRows(checkpointRows(3))

	if o.AttachRequested() || o.RefreshRequested() {
		t.Fatal("no intent should be armed before a keypress")
	}
	if closed := o.HandleKeyPress(keyMsg("enter")); closed {
		t.Error("enter must not close the overlay — the app decides, since attach suspends the loop")
	}
	if !o.AttachRequested() {
		t.Error("enter should arm an attach")
	}
	if o.AttachRequested() {
		t.Error("the attach flag must clear when read")
	}

	o.HandleKeyPress(keyMsg("r"))
	if !o.RefreshRequested() {
		t.Error("r should arm a reload")
	}
	if o.RefreshRequested() {
		t.Error("the reload flag must clear when read")
	}
}

func TestCheckpointOverlay_EscCloses(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	if !o.HandleKeyPress(keyMsg("esc")) {
		t.Error("esc should close")
	}
	if !o.HandleKeyPress(keyMsg("ctrl+c")) {
		t.Error("ctrl+c should close")
	}
}

// The cursor moves within the list and never leaves it, including after a reload
// returns a shorter list than the one the cursor was placed in.
func TestCheckpointOverlay_CursorClamping(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.SetSize(80, 24)
	o.SetRows(checkpointRows(5))

	for i := 0; i < 10; i++ {
		o.HandleKeyPress(keyMsg("down"))
	}
	if got := o.SelectedIndex(); got != 4 {
		t.Errorf("cursor = %d after running past the end, want 4", got)
	}
	for i := 0; i < 10; i++ {
		o.HandleKeyPress(keyMsg("up"))
	}
	if got := o.SelectedIndex(); got != 0 {
		t.Errorf("cursor = %d after running past the start, want 0", got)
	}

	o.HandleKeyPress(keyMsg("down"))
	o.HandleKeyPress(keyMsg("down"))
	o.SetRows(checkpointRows(2))
	if got := o.SelectedIndex(); got != 1 {
		t.Errorf("cursor = %d after a shorter reload, want 1", got)
	}
	o.SetRows(nil)
	if got := o.SelectedIndex(); got != 0 {
		t.Errorf("cursor = %d for an empty list, want 0", got)
	}
}

// Every state must render inside the box it was given, at a roomy size and a
// cramped one. The Go suite is otherwise blind to this.
func TestCheckpointOverlay_FitsItsBox(t *testing.T) {
	for _, size := range []struct{ w, h int }{{80, 24}, {40, 12}, {40, checkpointChrome + 2}} {
		for _, state := range []struct {
			name  string
			apply func(*CheckpointOverlay)
		}{
			{"loading", func(o *CheckpointOverlay) { o.SetLoading() }},
			{"unavailable", func(o *CheckpointOverlay) { o.SetUnavailable("no checkpoints for this session") }},
			{"few rows", func(o *CheckpointOverlay) { o.SetRows(checkpointRows(2)) }},
			{"overflowing", func(o *CheckpointOverlay) { o.SetRows(checkpointRows(60)) }},
			{"with note and message", func(o *CheckpointOverlay) {
				o.SetRows(checkpointRows(60))
				o.SetNote("claude has swept this session's file backups")
				o.SetMessage("this session is still starting")
			}},
			{"long label", func(o *CheckpointOverlay) {
				o.SetRows([]CheckpointRow{{
					When:    time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
					Label:   strings.Repeat("an extremely long prompt line ", 20),
					Files:   1200,
					Outside: 340,
				}})
			}},
		} {
			t.Run(state.name, func(t *testing.T) {
				o := NewCheckpointOverlay("a-session-with-a-fairly-long-name")
				o.SetSize(size.w, size.h)
				state.apply(o)
				out := o.Render()
				if h := lipgloss.Height(out); h > size.h {
					t.Errorf("%dx%d: rendered %d lines, want <= %d", size.w, size.h, h, size.h)
				}
				if w := lipgloss.Width(out); w > size.w {
					t.Errorf("%dx%d: rendered %d cells wide, want <= %d", size.w, size.h, w, size.w)
				}
			})
		}
	}
}

// The footer is truncated, never wrapped: a wrapped footer would silently claim a
// row the height budget already spent.
func TestCheckpointOverlay_FooterTruncates(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.SetSize(40, 12)
	o.SetRows(checkpointRows(3))
	lines := strings.Split(stripANSI(o.Render()), "\n")

	var footers int
	for _, line := range lines {
		if strings.Contains(line, "j/k move") {
			footers++
		}
		if strings.Contains(line, "esc close") {
			t.Errorf("the footer wrapped instead of truncating at width 40:\n%s", strings.Join(lines, "\n"))
		}
	}
	if footers != 1 {
		t.Errorf("expected exactly one footer line, got %d", footers)
	}
}

// The coverage summary is flush right and the rows are the same width, which is
// what makes the column comparable down the list. Asserted on cells rather than
// bytes, because a label is arbitrary user text.
func TestCheckpointOverlay_RowsAlignTheSummaryColumn(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.SetSize(80, 24)
	base := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	o.SetRows([]CheckpointRow{
		{When: base, Label: "short", Files: 1},
		{When: base.Add(time.Minute), Label: "a considerably longer prompt line here", Files: 1200, Outside: 7},
		{When: base.Add(2 * time.Minute), Label: "wide runes: 日本語のプロンプト", Files: 0},
	})

	var rows []string
	for _, line := range strings.Split(stripANSI(o.Render()), "\n") {
		if strings.Contains(line, "Aug  5 10:") {
			rows = append(rows, line)
		}
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rendered rows, got %d:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	want := lipgloss.Width(rows[0])
	for i, row := range rows {
		if got := lipgloss.Width(row); got != want {
			t.Errorf("row %d is %d cells, row 0 is %d — the summary column cannot align:\n%s",
				i, got, want, strings.Join(rows, "\n"))
		}
	}
	// Every row's last non-space content is its summary, so they end at one column.
	for i, row := range rows {
		if strings.HasSuffix(strings.TrimRight(row, " "), "…") {
			t.Errorf("row %d ends in an ellipsis — the summary was truncated away:\n%s", i, row)
		}
	}
}

// An overflowing list reports how much it is hiding, so the timeline never
// implies it is complete.
func TestCheckpointOverlay_ReportsHiddenRows(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.SetSize(80, 14)
	o.SetRows(checkpointRows(40))
	out := stripANSI(o.Render())
	if !strings.Contains(out, "more") {
		t.Errorf("an overflowing list must say how many rows are hidden:\n%s", out)
	}
}

// The file summary is the honest part of a row: it distinguishes a checkpoint
// that restores nothing, and names how much of the coverage lies outside the
// worktree.
func TestCheckpointFileSummary(t *testing.T) {
	cases := []struct {
		name string
		row  CheckpointRow
		want string
	}{
		{"empty snapshot", CheckpointRow{}, "no files"},
		{"single file", CheckpointRow{Files: 1}, "1 file"},
		{"several", CheckpointRow{Files: 7}, "7 files"},
		{"some outside the worktree", CheckpointRow{Files: 7, Outside: 3}, "7 files, 3 outside"},
		{"all outside the worktree", CheckpointRow{Files: 2, Outside: 2}, "2 files, 2 outside"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkpointFileSummary(tc.row); got != tc.want {
				t.Errorf("checkpointFileSummary(%+v) = %q, want %q", tc.row, got, tc.want)
			}
		})
	}
}

// A row with no extractable prompt text still renders as a row — roughly a fifth
// of real checkpoints are anchored to a turn with nothing to label.
func TestCheckpointOverlay_RowWithoutLabel(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.SetSize(80, 24)
	o.SetRows([]CheckpointRow{{When: time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC), Files: 3}})
	out := stripANSI(o.Render())
	if !strings.Contains(out, "(no prompt text)") {
		t.Errorf("a label-less checkpoint should still be identifiable:\n%s", out)
	}
	if !strings.Contains(out, "Aug  5 10:00") {
		t.Errorf("the time column should carry the checkpoint's timestamp:\n%s", out)
	}
}

// A checkpoint whose timestamps were all unreadable is shown as such rather than
// as the zero time.
func TestCheckpointOverlay_UnknownTime(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.SetSize(80, 24)
	o.SetRows([]CheckpointRow{{Label: "no timestamp anywhere", Files: 1}})
	out := stripANSI(o.Render())
	if !strings.Contains(out, "unknown") {
		t.Errorf("a checkpoint with no readable time should say so:\n%s", out)
	}
	if strings.Contains(out, "Jan  1 00:00") {
		t.Errorf("the zero time must not be rendered as a date:\n%s", out)
	}
}

// In a box too short for rows plus both optional lines, the standing note gives
// way — the rows and the transient message are what the user is looking at. The
// alternative is an overflowing box, which is what the height accounting exists
// to prevent.
func TestCheckpointOverlay_TightBoxDropsTheNoteNotTheRows(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.SetSize(60, checkpointChrome+4)
	o.SetRows(checkpointRows(20))
	o.SetNote("claude has swept this session's file backups")
	o.SetMessage("this session is still starting")

	lay := o.layout()
	if lay.showNote {
		t.Error("the note should give way in a box this short")
	}
	if !lay.showMessage {
		t.Error("the transient message should be kept")
	}
	if lay.visible < 1 {
		t.Errorf("visible = %d, want at least one row", lay.visible)
	}
	out := stripANSI(o.Render())
	if strings.Contains(out, "swept") {
		t.Errorf("the dropped note must not be drawn:\n%s", out)
	}
	if !strings.Contains(out, "still starting") {
		t.Errorf("the message must be drawn:\n%s", out)
	}

	// At the very floor neither optional line fits, and the rows still win.
	o.SetSize(60, checkpointChrome+2)
	floor := o.layout()
	if floor.showNote || floor.showMessage {
		t.Error("at the height floor both optional lines must give way to the rows")
	}
	if floor.visible < 1 {
		t.Errorf("visible = %d at the height floor, want at least one row", floor.visible)
	}
}

// The note is a property of the data and survives a reload; the message is
// transient and does not.
func TestCheckpointOverlay_NoteSurvivesReloadMessageDoesNot(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.SetSize(80, 24)
	o.SetNote("claude has swept this session's file backups")
	o.SetMessage("transient")
	o.SetRows(checkpointRows(2))

	out := stripANSI(o.Render())
	if !strings.Contains(out, "swept") {
		t.Errorf("the note should survive SetRows:\n%s", out)
	}
	if strings.Contains(out, "transient") {
		t.Errorf("the message should be cleared by SetRows:\n%s", out)
	}
}
