package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ZviBaratz/atrium/cmdlog"
	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/customcmd"
	"github.com/ZviBaratz/atrium/session"
	"github.com/ZviBaratz/atrium/ui/overlay"
	"github.com/ZviBaratz/atrium/ui/theme"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/ansi"
	"github.com/stretchr/testify/require"
)

// This file is the frame-parity oracle for the Bubble Tea v2 / Lip Gloss v2
// migration (#393). Its job is to answer one question the rest of the suite
// cannot: did swapping the renderer change what Atrium draws?
//
// The oracle is deliberately COLOUR-FREE, and that is what let it survive the
// cut. Every other colour-stable test in this repo pinned its bytes by forcing
// Lip Gloss v1's global colour profile — a global v2 deletes — so a golden
// baselined that way could not have been re-run afterwards, which is precisely
// when it was needed. Stripping ANSI with x/ansi.Strip is renderer-independent by
// construction: these goldens were generated on v1, and they pass unchanged on
// v2.
//
// Colour is separated rather than abandoned: TestFrameColourFingerprint snapshots
// the set of SGR sequences a frame emits. That half was expected to move at the
// cut and was re-baselined once, deliberately, with the diff read.
//
// Why plain-text parity was a fair bar: Lip Gloss v1.1.0 and v2.0.5 both measure
// through charmbracelet/x/ansi, and go.mod already pinned the version v2 wanted.

// frameState is one UI state plus the wiring production keeps for it. A state
// rendered without its overlay is a half-constructed shape whose frame proves
// nothing, so every entry that owns an overlay field arms it.
type frameState struct {
	name string
	st   state
	wire func(h *home, inst *session.Instance)
}

// frameStates enumerates every UI state. It is the single source of truth shared
// with TestStateMachine_BackgroundMessagesNeverPanic: two hand-maintained tables
// would drift, and the one that drifts silently is the one that stops covering a
// new state. TestFrameStatesCoverEveryState pins it to the enum.
func frameStates() []frameState {
	return []frameState{
		{"default", stateDefault, nil},
		{"prompt", statePrompt, func(h *home, _ *session.Instance) {
			h.textInputOverlay = overlay.NewSessionCreateOverlay(
				h.appConfig.GetProfiles(), h.appConfig.ClaudeAccounts, nil, h.program)
		}},
		{"help", stateHelp, func(h *home, _ *session.Instance) {
			h.textOverlay = overlay.NewTextOverlay("help")
		}},
		{"info", stateInfo, func(h *home, _ *session.Instance) {
			h.textOverlay = overlay.NewTextOverlay("info")
		}},
		{"confirm", stateConfirm, func(h *home, _ *session.Instance) {
			h.confirmationOverlay = overlay.NewConfirmationOverlay("sure?")
		}},
		{"rename", stateRename, func(h *home, inst *session.Instance) {
			h.renameTarget = inst
			h.renameOverlay = overlay.NewRenameOverlay("label", "", false)
		}},
		{"settings", stateSettings, func(h *home, _ *session.Instance) {
			h.settingsOverlay = overlay.NewSettingsOverlay(h.appConfig)
		}},
		{"accounts", stateAccounts, func(h *home, _ *session.Instance) {
			h.accountsOverlay = overlay.NewAccountsOverlay(h.appConfig, h.appState)
		}},
		{"filter", stateFilter, nil},
		{"hints", stateHints, nil},
		{"visual", stateVisual, nil},
		{"diffComment", stateDiffComment, func(h *home, _ *session.Instance) {
			const diff = "diff --git a/foo.go b/foo.go\n@@ -1,2 +1,2 @@\n ctx\n+add\n"
			h.tabbedWindow.SetDiffContent(diff)
			h.tabbedWindow.EnterDiffComment()
		}},
		{"queue", stateQueue, func(h *home, inst *session.Instance) {
			h.queueOverlay = overlay.NewQueueOverlay(inst.DisplayName())
		}},
		{"cmdlog", stateCmdLog, func(h *home, _ *session.Instance) {
			seedCmdLog()
			h.cmdLogOverlay = overlay.NewCmdLogOverlay("parity-session")
		}},
		{"welcome", stateWelcome, func(h *home, _ *session.Instance) {
			h.welcomeOverlay = overlay.NewWelcomeOverlay()
		}},
		{"history", stateHistory, func(h *home, _ *session.Instance) {
			h.promptHistoryOverlay = overlay.NewPromptHistoryOverlay([]string{"remembered"})
		}},
		{"commandPalette", stateCommandPalette, func(h *home, _ *session.Instance) {
			// Through the opener, not by hand: the overlay and paletteRows must stay
			// in step, and a half-armed palette renders a frame nothing produces.
			h.openCommandPalette()
		}},
		{"screensaver", stateScreensaver, nil},
		// Appended last on purpose: the two colour fingerprints iterate this slice in
		// order and write one block per state, so a new entry at the end appends a
		// trailing block where one inserted mid-slice rewrites every block after it.
		{"customCommands", stateCustomCommands, func(h *home, _ *session.Instance) {
			h.customCommands = parityCustomCommands()
			// Through the opener, like the palette: the overlay and customCommandRows
			// must stay in step, and the per-row gating only exists on that path.
			h.openCustomCommands()
		}},
	}
}

// parityCustomCommands is a POPULATED command list, and that is the whole point of
// its existence. Left empty, every size sweep and both colour fingerprints would
// render the menu's one-line empty state — so the width guards would hold nothing,
// the golden would pin a frame no user with custom commands ever sees, and a row
// wide enough to break the box would sail through.
//
// Built through customcmd.Validate rather than by hand: Command's template and source
// are unexported, so a composite literal is a Command whose Render and MissingFields
// nil-panic on first use.
func parityCustomCommands() []customcmd.Command {
	cmds, problems := customcmd.Validate([]config.CustomCommand{
		// `terminal`, matching the README's own worked example — and so the mode's row
		// marker is covered by every size sweep and both colour fingerprints below. Left
		// all-`background`, the fixture rendered a frame no user of the mode ever sees.
		{Key: "g", Description: "lazygit in this worktree", Command: "lazygit -p {{ quote .Session.Worktree }}", Output: "terminal"},
		{Key: "c", Description: "just ci", Command: "just ci", Output: "background"},
		{Key: "f", Description: "git fetch --all", Context: "repo", Command: "git fetch --all", Output: "background"},
		{Key: "b", Description: "gh pr checks on this branch", Command: "gh pr checks {{ .Session.Branch }}", Output: "background"},
	})
	if len(problems) > 0 || len(cmds) != 4 {
		panic(fmt.Sprintf("parity fixture must validate cleanly: %v", problems))
	}
	return cmds
}

// seedCmdLog replaces the process-global command ring with two fixed records.
//
// The overlay reads the ring live and renders each record's age as
// relTime(time.Since(r.Start)), so both the *contents* and the *ages* are
// ambient: without this the frame carries however many subprocesses the rest of
// the suite happened to run before it ("40 commands", "41 commands", ...), which
// is the one state the volatility probe caught. The ages are set two and three
// hours back because relTime buckets anything past an hour to whole hours, so
// they cannot tick over mid-run the way a seconds-old record would.
func seedCmdLog() {
	cmdlog.Reset()
	cmdlog.Add(cmdlog.Record{
		Argv:    "git status --porcelain",
		Session: "parity-session",
		Start:   time.Now().Add(-2 * time.Hour),
		Dur:     12 * time.Millisecond,
		CPU:     8 * time.Millisecond,
	})
	cmdlog.Add(cmdlog.Record{
		Argv:    "tmux has-session -t atrium-parity",
		Session: "parity-session",
		Start:   time.Now().Add(-3 * time.Hour),
		Dur:     4 * time.Millisecond,
		// A non-zero CPU on a fixture record so the golden pins the summary's
		// per-verb attribution, not just its counter. Deliberately smaller than the
		// wall duration: a subprocess that waits does not burn CPU while it waits,
		// and the two columns exist to be told apart.
		CPU:  1 * time.Millisecond,
		Exit: 1,
		Err:  true,
	})
}

// newParityHome builds a home parked in fs, sized to w×h, whose frame is
// reproducible.
//
// Two things are pinned that production leaves ambient. The instance path gets a
// fixed basename: t.TempDir() numbers its directories (001, 002, ...) and the
// list groups sessions by repo path, so the counter would otherwise land in the
// frame and every golden would rot on the next test added above it. The theme and
// glyph set are pinned to the shipped defaults, mirroring app.Run's
// theme.Set/SetGlyphSet wiring — theme.Current() is a package global that other
// tests in this package mutate, so under -shuffle the frame would otherwise
// inherit whichever theme ran last.
//
// Pinning to config.DefaultConfig() rather than to a fixed literal is deliberate:
// the goldens then show what a default install actually draws, and changing the
// shipped default surfaces here as a diff the author has to acknowledge.
//
// The window is sized, then the overlay is armed, then it is sized again. That is
// the real sequence — the app is already running at some size when a modal opens —
// and it is what gives the overlay its dimensions. Arming after the only resize
// leaves overlays unsized and renders a frame production never shows.
func newParityHome(t *testing.T, fs frameState, w, h int) *home {
	t.Helper()

	cfg := config.DefaultConfig()
	t.Cleanup(theme.Set(cfg.GetTheme()))
	t.Cleanup(theme.SetGlyphSet(cfg.GetGlyphSet()))
	// The scheme axis. Since Stage F this line SELECTS the palette every golden is
	// rendered in, rather than merely guarding it: the shipped default is `auto`, so
	// cfg.GetTheme() above resolves through the scheme instead of around it. Dark is
	// what a terminal that does not answer gets, and what these goldens are baselined
	// at. Set it to SchemeLight and both colours.txt and frames/screensaver.txt move —
	// the latter because the splash encodes luminance as ASCII density, so for that
	// one state the palette is layout.
	//
	// What it does NOT do today is catch a leak, and the distinction is worth keeping
	// straight because the line reads like a -shuffle guard. Deleting it entirely was
	// measured: nothing fails, even with -shuffle over the whole package, because every
	// SetScheme in app is paired with its restore. So this is defence-in-depth against
	// a dirty global that no test currently creates — do not upgrade that to a claim
	// the suite would disprove.
	t.Cleanup(theme.SetScheme(theme.SchemeDark))

	m := newCreateFormHome(t)
	m.spinner = spinner.New()
	m.lostStrikes = map[*session.Instance]int{}

	// Pin the session cap, because an unset max_sessions is HOST-DERIVED: the
	// settings panel renders config.DefaultSessionCap(), which is
	// max(2, hardware_threads/2) — "auto (4)" on an 8-thread dev box and
	// "auto (2)" on a 2-thread CI runner. A golden carrying that number is
	// machine-specific, which is how this first reached CI red while green
	// locally. Reproduce with `taskset -c 0,1 go test ./app/ -run TestFrameParity`.
	//
	// Pinning the config rather than the thread count is deliberate: config's
	// cpuCount seam is package-private and reaching it would mean widening config's
	// public API for a test in another package. The cost is that this golden shows
	// an explicit cap where a default install shows "auto (N)" — a difference the
	// renderer cannot see (both are just a string in a row), and the "auto (N)"
	// display shape has its own coverage in ui/overlay's settings tests.
	sessionCap := 4
	m.appConfig.MaxSessions = &sessionCap

	dir := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "parity", Path: dir, Program: "echo",
	})
	require.NoError(t, err)
	m.list.AddInstance(inst)()
	m.list.SetSelectedInstance(0)

	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.state = fs.st
	if fs.wire != nil {
		fs.wire(m, inst)
	}
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// parityFrame renders the composed frame with all styling removed. View() already
// runs zone.Scan, so bubblezone's markers are gone before this sees the string.
func parityFrame(t *testing.T, fs frameState, w, h int) string {
	t.Helper()
	return xansi.Strip(newParityHome(t, fs, w, h).View().Content)
}

// paritySizes are the two shapes the plain-text goldens capture: the 80×24 floor
// where every degradation path is live, and a roomy default. Width invariants are
// swept far wider by TestViewFitsTerminalBoundsEveryState, which needs no golden
// per size and so can afford six.
var paritySizes = [][2]int{{80, 24}, {120, 40}}

// TestFrameStatesCoverEveryState is the drift guard on the table above: add a
// state to the enum without adding it here and this fails, rather than the new
// state quietly having no frame oracle.
func TestFrameStatesCoverEveryState(t *testing.T) {
	require.Len(t, frameStates(), int(numStates),
		"frameStates() must list every state in the enum (numStates=%d)", numStates)

	seen := map[state]string{}
	for _, fs := range frameStates() {
		if prev, dup := seen[fs.st]; dup {
			t.Errorf("state %d listed twice: %q and %q", fs.st, prev, fs.name)
		}
		seen[fs.st] = fs.name
	}
}

// TestFrameParity snapshots the ANSI-stripped frame for every state at every
// parity size. Regenerate with:
//
//	CS_UPDATE_GOLDEN=1 go test ./app/ -run TestFrameParity
func TestFrameParity(t *testing.T) {
	for _, fs := range frameStates() {
		t.Run(fs.name, func(t *testing.T) {
			var b strings.Builder
			for _, dim := range paritySizes {
				w, h := dim[0], dim[1]
				fmt.Fprintf(&b, "### %dx%d\n", w, h)
				b.WriteString(parityFrame(t, fs, w, h))
				b.WriteString("\n")
			}
			compareGolden(t, filepath.Join("testdata", "frames", fs.name+".txt"), b.String())
		})
	}
}

// sgrRE matches the SGR (colour/attribute) sequences a frame emits. Deliberately
// only "...m" sequences: those are what the renderer swap changes.
var sgrRE = regexp.MustCompile(`\x1b\[[0-9;:]*m`)

// TestFrameColourFingerprint snapshots the distinct SGR sequences each state
// emits, with counts. This is the half of the oracle that IS expected to move at
// the v2 cut — v2's Render() emits full-fidelity colour and downsamples at the
// writer, where v1 downsampled inside Render — so it is kept apart from the
// layout goldens rather than mixed in with them. Re-baseline it once, at the cut,
// having read the diff.
//
// The forced profile is load-bearing and is the one v1-only mechanism in this
// file. Under `go test` there is no TTY, so Lip Gloss v1's global renderer
// resolves to termenv.Ascii and strips every escape: without this the fingerprint
// records "0 distinct" for all eighteen states and silently guards nothing.
// SetColorProfile is exactly what v2 removes, so this test — unlike the layout
// goldens above, which deliberately depend on no such mechanism — has to be
// rewritten at the cut against colorprofile/lipgloss.Writer. That is the expected
// cost of the one test whose subject is colour.
//
// Regenerate with:
//
//	CS_UPDATE_GOLDEN=1 go test ./app/ -run TestFrameColourFingerprint
func TestFrameColourFingerprint(t *testing.T) {
	// No colour profile to pin: Lip Gloss v2 styles always emit full fidelity,
	// and Bubble Tea down-samples at the writer. v1 needed the global forced to
	// TrueColor here or the assertions below saw plain text.

	const w, h = 120, 40
	var b strings.Builder
	for _, fs := range frameStates() {
		counts := map[string]int{}
		for _, seq := range sgrRE.FindAllString(newParityHome(t, fs, w, h).View().Content, -1) {
			counts[seq]++
		}
		seqs := make([]string, 0, len(counts))
		for seq := range counts {
			seqs = append(seqs, seq)
		}
		sort.Strings(seqs)

		fmt.Fprintf(&b, "## %s (%d distinct)\n", fs.name, len(seqs))
		for _, seq := range seqs {
			fmt.Fprintf(&b, "  %-24s %d\n", strings.ReplaceAll(seq, "\x1b", "ESC"), counts[seq])
		}
	}
	compareGolden(t, filepath.Join("testdata", "colours.txt"), b.String())
}

// newParityHomeThemed is newParityHome pinned to a named theme instead of the
// config default. Split out rather than parameterizing every call site, because
// the default-theme path is what eighteen goldens depend on and it should not grow
// an argument that could be passed wrong.
//
// The ORDERING is the whole helper. newParityHome runs t.Cleanup(theme.Set(...))
// itself, and theme.Set applies immediately — it is the returned restore that is
// deferred — so a pin registered by the caller BEFORE newParityHome would be
// overwritten by it and the frame would render in the default theme while the test
// name claimed otherwise. Setting the theme here, after, is what makes the pin
// take. Styles read theme.Current() lazily at render time, so restyling the model
// after it is built is enough; the caller's View() sees the named theme.
func newParityHomeThemed(t *testing.T, fs frameState, w, h int, themeName string) *home {
	t.Helper()
	m := newParityHome(t, fs, w, h)
	t.Cleanup(theme.Set(themeName))
	return m
}

// TestLightFrameColourFingerprint is TestFrameColourFingerprint under the light
// palette. It exists because colours.txt is generated at the DEFAULT theme and must
// never move (that immovability is what proves `theme: auto` with no detection is a
// no-op), which would otherwise leave the light palette with no rendering guard at
// all — only its hex values checked, never what the frame actually emits.
//
// Regenerate with:
//
//	CS_UPDATE_GOLDEN=1 go test ./app/ -run TestLightFrameColourFingerprint
func TestLightFrameColourFingerprint(t *testing.T) {
	const w, h = 120, 40
	var b strings.Builder
	for _, fs := range frameStates() {
		counts := map[string]int{}
		m := newParityHomeThemed(t, fs, w, h, "tokyo-night-day")
		for _, seq := range sgrRE.FindAllString(m.View().Content, -1) {
			counts[seq]++
		}
		seqs := make([]string, 0, len(counts))
		for seq := range counts {
			seqs = append(seqs, seq)
		}
		sort.Strings(seqs)

		fmt.Fprintf(&b, "## %s (%d distinct)\n", fs.name, len(seqs))
		for _, seq := range seqs {
			fmt.Fprintf(&b, "  %-24s %d\n", strings.ReplaceAll(seq, "\x1b", "ESC"), counts[seq])
		}
	}
	compareGolden(t, filepath.Join("testdata", "colours-light.txt"), b.String())
}

// TestViewFitsTerminalBoundsEveryState sweeps the box contract — no more rows
// than the terminal has, every row exactly its width — across every state and a
// wide spread of sizes.
//
// It complements TestViewFitsTerminalBounds rather than replacing it: that one
// carries fixture-specific arms (notably the 30-account rotation pool that
// reproduces #478) which this generic table has no way to express. This one
// carries the breadth — every state, not four.
//
// The measurement is muesli/ansi.PrintableRuneWidth, deliberately not
// lipgloss.Width: measuring Lip Gloss's output with Lip Gloss's own measurer is a
// tautology that stays green when the emitter and the measurer move together,
// which is exactly the failure a renderer migration can produce.
func TestViewFitsTerminalBoundsEveryState(t *testing.T) {
	sizes := [][2]int{{200, 50}, {210, 48}, {120, 30}, {160, 40}, {235, 55}, {80, 24}}

	for _, fs := range frameStates() {
		t.Run(fs.name, func(t *testing.T) {
			for _, dim := range sizes {
				w, h := dim[0], dim[1]
				lines := strings.Split(newParityHome(t, fs, w, h).View().Content, "\n")

				if len(lines) > h {
					t.Errorf("size=%dx%d: View() emitted %d lines, exceeds height %d",
						w, h, len(lines), h)
				}
				for i, l := range lines {
					if pw := ansi.PrintableRuneWidth(l); pw != w {
						t.Errorf("size=%dx%d: line %d width=%d, expected %d",
							w, h, i, pw, w)
						break
					}
				}
			}
		})
	}
}

// compareGolden diffs got against the file at path, or rewrites it when
// CS_UPDATE_GOLDEN is set — the convention TestListGolden already uses.
func compareGolden(t *testing.T, path, got string) {
	t.Helper()

	if os.Getenv("CS_UPDATE_GOLDEN") != "" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
		t.Logf("golden updated: %s", path)
		return
	}

	want, err := os.ReadFile(path)
	require.NoErrorf(t, err, "missing golden %s — regenerate with CS_UPDATE_GOLDEN=1", path)
	if string(want) == got {
		return
	}

	// Report the first differing line rather than dumping two full frames: a
	// 40-row diff of 120-column rows is unreadable in test output, and the first
	// divergence is nearly always the whole story.
	wl, gl := strings.Split(string(want), "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var wline, gline string
		if i < len(wl) {
			wline = wl[i]
		}
		if i < len(gl) {
			gline = gl[i]
		}
		if wline != gline {
			t.Fatalf("%s differs at line %d\n  want: %q\n  got:  %q\n(regenerate with CS_UPDATE_GOLDEN=1 if intended)",
				path, i+1, wline, gline)
		}
	}
	t.Fatalf("%s differs in length: want %d lines, got %d", path, len(wl), len(gl))
}
