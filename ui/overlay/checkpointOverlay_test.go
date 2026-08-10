package overlay

import (
	"fmt"
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

// Every intent is read-once, so the app acts on each press exactly once.
func TestCheckpointOverlay_IntentsAreReadOnce(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.SetRows(checkpointRows(3))

	if o.AttachRequested() || o.ForkRequested() || o.RefreshRequested() {
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

	if closed := o.HandleKeyPress(keyMsg("f")); closed {
		t.Error("f must not close the overlay — the app decides, and may have to refuse")
	}
	if !o.ForkRequested() {
		t.Error("f should arm a fork")
	}
	if o.ForkRequested() {
		t.Error("the fork flag must clear when read")
	}

	o.HandleKeyPress(keyMsg("r"))
	if !o.RefreshRequested() {
		t.Error("r should arm a reload")
	}
	if o.RefreshRequested() {
		t.Error("the reload flag must clear when read")
	}

	// The three are independent: arming one must not arm another. Without this a
	// single shared flag would satisfy every assertion above.
	o.HandleKeyPress(keyMsg("f"))
	if o.AttachRequested() || o.RefreshRequested() {
		t.Error("f armed an attach or a reload as well as a fork")
	}
	o.ForkRequested()
	o.HandleKeyPress(keyMsg("enter"))
	if o.ForkRequested() {
		t.Error("enter armed a fork as well as an attach")
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
			{"with a note", func(o *CheckpointOverlay) {
				o.SetRows(checkpointRows(60))
				o.SetNote("claude has swept this session's file backups")
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

// The footer is one line at every width — shortened or truncated, never wrapped,
// since a wrapped footer would silently claim a row the height budget already
// spent — and it always names the keys that leave the overlay.
//
// The second half is the part a fixed string got wrong: stateCheckpoints hides the
// hint bar, and the full hint line is 80 cells against an inner width of 50 on an
// 80-column terminal (0.7 × 80 = a 56-cell box), so truncation cut it at `enter
// attach (then Esc…` and left nothing on screen saying how to close or reload.
//
// The widths below are box widths, not terminal widths — the app derives one from
// the other — so 56 is the 80-column case and 80 is a 114-column terminal.
func TestCheckpointOverlay_FooterFitsAndKeepsTheExitKeys(t *testing.T) {
	for _, width := range []int{40, 56, 72, 76, 80, 120} {
		t.Run(fmt.Sprintf("w%d", width), func(t *testing.T) {
			o := NewCheckpointOverlay("alpha")
			o.SetSize(width, 12)
			o.SetRows(checkpointRows(3))
			out := o.Render()
			lines := strings.Split(stripANSI(out), "\n")

			var footers int
			for _, line := range lines {
				if strings.Contains(line, "esc close") {
					footers++
				}
			}
			if footers != 1 {
				t.Errorf("want exactly one footer line naming `esc close`, got %d:\n%s",
					footers, strings.Join(lines, "\n"))
			}
			if w := lipgloss.Width(out); w > width {
				t.Errorf("rendered %d cells wide, want <= %d", w, width)
			}
		})
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

// An overflowing list reports how much it is hiding wherever the window sits, so
// the timeline never implies it is complete — and it costs the same line at every
// scroll position, because a box that grows a row at the end is a box PlaceOverlay
// re-centres, i.e. one that visibly jumps as the cursor crosses into the last
// window.
func TestCheckpointOverlay_ReportsHiddenRowsAtEveryScrollPosition(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.SetSize(80, 14)
	o.SetRows(checkpointRows(40))

	top := stripANSI(o.Render())
	if !strings.Contains(top, "more") {
		t.Errorf("an overflowing list must say how many rows are hidden:\n%s", top)
	}
	topHeight := lipgloss.Height(o.Render())

	// Walk to the very last row: the window is now at the end of the list, with
	// everything hidden ABOVE it and nothing below.
	for i := 0; i < 60; i++ {
		o.HandleKeyPress(keyMsg("down"))
	}
	bottom := stripANSI(o.Render())
	if !strings.Contains(bottom, "above") {
		t.Errorf("scrolled to the end, the box must still report the rows hidden above it:\n%s", bottom)
	}
	if got := lipgloss.Height(o.Render()); got != topHeight {
		t.Errorf("box is %d lines at the end and %d at the top — a changing height makes the overlay jump", got, topHeight)
	}

	// And somewhere in the middle, both sides are named.
	for i := 0; i < 20; i++ {
		o.HandleKeyPress(keyMsg("up"))
	}
	mid := stripANSI(o.Render())
	if !strings.Contains(mid, "above") || !strings.Contains(mid, "below") {
		t.Errorf("mid-list, the box should name both sides:\n%s", mid)
	}
	if got := lipgloss.Height(o.Render()); got != topHeight {
		t.Errorf("box is %d lines mid-list and %d at the top", got, topHeight)
	}
}

func TestHiddenRowSummary(t *testing.T) {
	cases := []struct {
		above, below int
		want         string
	}{
		{0, 13, "… 13 more"},
		{7, 0, "… 7 above"},
		{7, 13, "… 7 above, 13 below"},
		{0, 0, "…"},
	}
	for _, tc := range cases {
		if got := hiddenRowSummary(tc.above, tc.below); got != tc.want {
			t.Errorf("hiddenRowSummary(%d, %d) = %q, want %q", tc.above, tc.below, got, tc.want)
		}
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

// In a box too short for the rows plus the note, the note gives way: it is a
// caveat about the rows, and a box with no rows in it has nothing to caveat. The
// alternative is an overflowing box, which is what the height accounting exists to
// prevent.
func TestCheckpointOverlay_TightBoxDropsTheNoteNotTheRows(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.SetSize(60, checkpointChrome+2)
	o.SetRows(checkpointRows(20))
	o.SetNote("claude has swept this session's file backups")

	lay := o.layout()
	if lay.showNote {
		t.Error("the note should give way in a box this short")
	}
	if lay.visible < 1 {
		t.Errorf("visible = %d, want at least one row", lay.visible)
	}
	out := stripANSI(o.Render())
	if strings.Contains(out, "swept") {
		t.Errorf("the dropped note must not be drawn:\n%s", out)
	}
	if lipgloss.Height(o.Render()) > checkpointChrome+2 {
		t.Errorf("the box overflowed the height it was given:\n%s", out)
	}

	// Given room for both, it keeps both.
	o.SetSize(60, 24)
	roomy := o.layout()
	if !roomy.showNote {
		t.Error("with room to spare the note must be drawn")
	}
}

// The note describes the rows, so it survives a reload that returns rows and is
// dropped by one that finds none — otherwise a caveat about backups would stand
// under a box that has just said there is no transcript to have backups for.
func TestCheckpointOverlay_NoteSurvivesRowsAndDiesWithThem(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.SetSize(80, 24)
	o.SetNote("claude has swept this session's file backups")
	o.SetRows(checkpointRows(2))

	out := stripANSI(o.Render())
	if !strings.Contains(out, "swept") {
		t.Errorf("the note should survive SetRows:\n%s", out)
	}

	o.SetUnavailable("no transcript for this session yet")
	gone := stripANSI(o.Render())
	if strings.Contains(gone, "swept") {
		t.Errorf("the note must not outlive the rows it describes:\n%s", gone)
	}
}

// The window follows the list down AND back up. Only the first half was guarded:
// clampScroll pushed the window forward for a cursor below it and reset to 0 for
// one outside the list, but never pulled it back when the list shrank under it or
// the box grew past it — and since the hidden-rows line is drawn only while the
// list overflows, the rows it stranded above the window were reported nowhere.
func TestCheckpointOverlay_ScrollFollowsTheListBackUp(t *testing.T) {
	t.Run("a shorter reload", func(t *testing.T) {
		o := NewCheckpointOverlay("alpha")
		o.SetSize(76, 18)
		o.SetRows(checkpointRows(40))
		for i := 0; i < 60; i++ {
			o.HandleKeyPress(keyMsg("down"))
		}
		o.Render() // the window only moves when the box is drawn
		o.SetRows(checkpointRows(5))

		out := stripANSI(o.Render())
		for _, want := range []string{"prompt number a", "prompt number e"} {
			if !strings.Contains(out, want) {
				t.Errorf("row %q is missing — the window stayed near the old bottom:\n%s", want, out)
			}
		}
	})

	t.Run("a taller box", func(t *testing.T) {
		o := NewCheckpointOverlay("alpha")
		o.SetSize(76, 18)
		o.SetRows(checkpointRows(20))
		for i := 0; i < 30; i++ {
			o.HandleKeyPress(keyMsg("down"))
		}
		o.Render()        // the window only moves when the box is drawn
		o.SetSize(76, 34) // the terminal was maximised: room for every row

		out := stripANSI(o.Render())
		if !strings.Contains(out, "prompt number a") {
			t.Errorf("the oldest row is hidden in a box with room for the whole list:\n%s", out)
		}
	})
}

// A reload holds the box at the height it already had. SetLoading swaps the row
// window for one line, and PlaceOverlay re-centres on every height change, so a
// full-height timeline would jump up and shrink on every `r` and jump back when
// the result landed.
//
// Swept over row counts either side of the window, because padding the loading
// branch to the *window* height fixes only the full case and makes the short one
// worse — a 3-row list in an 18-line box draws 3 rows, and padding to 10 grows the
// box on reload rather than holding it. Short lists are the common case: a session
// has far fewer checkpoints than a terminal has rows.
func TestCheckpointOverlay_ReloadDoesNotResizeTheBox(t *testing.T) {
	for _, rows := range []int{1, 3, 9, 10, 11, 40} {
		t.Run(fmt.Sprintf("rows%d", rows), func(t *testing.T) {
			o := NewCheckpointOverlay("alpha")
			o.SetSize(76, 18)
			o.SetRows(checkpointRows(rows))
			loaded := lipgloss.Height(o.Render())

			o.SetLoading()
			if got := lipgloss.Height(o.Render()); got != loaded {
				t.Errorf("box is %d lines while reloading and %d with %d rows — the overlay jumps",
					got, loaded, rows)
			}
		})
	}
}

// The footer sheds clauses in one direction only. A ladder is a promise that a
// narrower box shows a subset of what a wider one showed; a rung that hands back a
// clause a wider rung had already dropped breaks that, and it breaks it invisibly,
// because every rung still fits its width and the whole thing still renders on one
// line.
//
// That is exactly how the 46-cell rung `j/k move · enter attach · r reload · esc
// close` shipped: it sat below the 53-cell rung that had already shed `j/k move`,
// so a box between 46 and 52 cells inner showed `j/k move` again and lost the
// Esc-Esc reminder instead — and 50 is what an 80-column terminal gives, so the
// band it broke was the default one, which the frame golden renders.
func TestCheckpointFooter_ShedsMonotonically(t *testing.T) {
	clauses := []string{"j/k move", "Esc Esc", "r reload", "f fork", "esc close"}
	shedAt := map[string]int{}

	// Down to the narrowest budget the overlay can hand it, and no further: SetSize
	// floors the box at checkpointMinWidth, so Render's `inner` cannot go below this.
	// Sweeping past it would assert the ladder over widths no caller can produce, and
	// the ladder would have to grow a rung to satisfy it — dead copy, justified by
	// nothing but the test.
	const narrowest = checkpointMinWidth - 6

	for inner := 90; inner >= narrowest; inner-- {
		got := checkpointFooter(inner)
		if w := lipgloss.Width(got); w > inner {
			t.Fatalf("footer is %d cells at inner=%d: %q", w, inner, got)
		}
		for _, clause := range clauses {
			switch {
			case !strings.Contains(got, clause):
				if _, gone := shedAt[clause]; !gone {
					shedAt[clause] = inner
				}
			case shedAt[clause] != 0:
				t.Errorf("%q is back at inner=%d after being shed at inner=%d: %q",
					clause, inner, shedAt[clause], got)
			}
		}
	}

	// The order the ladder's doc comment states, asserted rather than described:
	// the reminder outlives both hints, and `esc close` outlives everything.
	if shedAt["esc close"] != 0 {
		t.Errorf("`esc close` was shed at inner=%d; it is the only key out of a state "+
			"that hides the hint bar", shedAt["esc close"])
	}
	// `f` is not in keys.Registry, so it gets no cheatsheet row, no palette entry
	// and no README line — this footer is the only place it is written down. A rung
	// that sheds it makes the whole fork feature unreachable by anyone who has not
	// read the source.
	if shedAt["f fork"] != 0 {
		t.Errorf("`f fork` was shed at inner=%d; nothing else in the app names that key", shedAt["f fork"])
	}
	for _, earlier := range []string{"j/k move", "r reload"} {
		if shedAt[earlier] <= shedAt["Esc Esc"] {
			t.Errorf("%q survives to inner=%d but the Esc-Esc reminder only to %d — the "+
				"reminder is the one thing here nothing else teaches",
				earlier, shedAt[earlier], shedAt["Esc Esc"])
		}
	}
}

// The rung widths the ladder's doc comment states, asserted rather than described.
// A comment carrying six numbers is six chances to be a lie after the next reword,
// and the 48 is the load-bearing one: an 80-column terminal gives exactly 50, so a
// rung that drifts three cells wider drops the whole default frame to the rung
// below it. Ordering is pinned separately, for every ladder at once, by
// TestHintLadders_OrderedWidestFirst.
func TestCheckpointFooterHints_RungWidths(t *testing.T) {
	want := []int{80, 73, 62, 59, 48, 33}
	if len(checkpointFooterHints) != len(want) {
		t.Fatalf("ladder has %d rungs, want %d — update the doc comment with it",
			len(checkpointFooterHints), len(want))
	}
	for i, rung := range checkpointFooterHints {
		if got := lipgloss.Width(rung); got != want[i] {
			t.Errorf("rung %d is %d cells, want %d: %q", i, got, want[i], rung)
		}
	}
}

// What survives the 80×24 floor, which is the width the ladder exists for and the
// one the frame goldens render. Both actions and the Esc-Esc reminder must be
// there; `r reload` must not, and asserting its absence is not pedantry but the
// record of a trade-off. Before #644 a rung fit all four clauses in exactly 50
// cells. Adding `f fork` made the tightest four-clause line 54, so one had to go at
// the default size, and `r reload` is the only one of the three with another route
// to the same place (esc, then a fresh `H`) — at the cost of a second
// whole-transcript scan. If a future reword frees up four cells, `r reload` is what
// should come back, and this test failing is how that gets noticed.
//
// The negative control is the point: at inner 50 a fixed truncated line, the
// 46-cell rung that once replaced it, and the 42-cell rung after that all produce a
// footer that fits on one line and names keys — so every other assertion in this
// file passes over a footer that has silently dropped an instruction.
func TestCheckpointFooter_KeepsBothActionsAndTheReminderAtTheFloor(t *testing.T) {
	const inner = 50 // 0.7 × 80 columns = a 56-cell box, less border and padding

	got := checkpointFooter(inner)

	for _, want := range []string{"enter attach", "f fork", "Esc Esc", "esc close"} {
		if !strings.Contains(got, want) {
			t.Errorf("the 80-column footer does not name %q: %q", want, got)
		}
	}
	if strings.Contains(got, "r reload") {
		t.Errorf("the 80-column footer names `r reload`, so a clause that should have "+
			"survived was shed instead — the shed order is `j/k move`, then `r reload`, "+
			"then the reminder: %q", got)
	}
	if w := lipgloss.Width(got); w > inner {
		t.Errorf("the 80-column footer is %d cells against an inner width of %d: %q", w, inner, got)
	}
}

// enter is dead while the read is in flight. A whole-transcript scan is not
// instantaneous, and attaching hands the terminal to tmux — so a press in the gap
// would throw the user into the agent instead of showing them the list they opened.
func TestCheckpointOverlay_EnterIsInertWhileLoading(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.HandleKeyPress(keyMsg("enter"))
	if o.AttachRequested() {
		t.Error("enter armed an attach while the transcript was still being read")
	}

	o.SetRows(checkpointRows(3))
	o.HandleKeyPress(keyMsg("enter"))
	if !o.AttachRequested() {
		t.Error("enter should arm an attach once the box has an answer")
	}
}

// f is dead while the read is in flight for the same reason enter is, one step
// removed: there are no rows yet, so the cursor names nothing, and a fork armed in
// the gap would spawn a whole session — worktree, branch, tmux — seeded from
// whichever checkpoint later happened to land at index 0.
func TestCheckpointOverlay_ForkIsInertWhileLoading(t *testing.T) {
	o := NewCheckpointOverlay("alpha")
	o.HandleKeyPress(keyMsg("f"))
	if o.ForkRequested() {
		t.Error("f armed a fork while the transcript was still being read")
	}

	o.SetRows(checkpointRows(3))
	o.HandleKeyPress(keyMsg("f"))
	if !o.ForkRequested() {
		t.Error("f should arm a fork once the box has an answer")
	}
}
