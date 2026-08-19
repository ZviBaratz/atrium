package tmux

import (
	"bytes"
	"context"
	"fmt"
	cmd2 "github.com/ZviBaratz/atrium/cmd"
	"github.com/ZviBaratz/atrium/log"
	"github.com/ZviBaratz/atrium/session/agent"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"
	"github.com/ZviBaratz/atrium/internal/testutil"

	"github.com/stretchr/testify/require"
)

type MockPtyFactory struct {
	t *testing.T

	// Array of commands and the corresponding file handles representing PTYs.
	cmds  []*exec.Cmd
	files []*os.File

	// StartErr, when non-nil, makes Start fail without allocating a pty. Tests use
	// it to simulate a Restore/pty failure (e.g. the Detach degraded path).
	StartErr error
}

func (pt *MockPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	if pt.StartErr != nil {
		return nil, pt.StartErr
	}
	filePath := filepath.Join(pt.t.TempDir(), fmt.Sprintf("pty-%s-%d", pt.t.Name(), rand.Int31()))
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0644)
	if err == nil {
		pt.cmds = append(pt.cmds, cmd)
		pt.files = append(pt.files, f)
	}
	return f, err
}

func (pt *MockPtyFactory) Close() {}

func NewMockPtyFactory(t *testing.T) *MockPtyFactory {
	return &MockPtyFactory{
		t: t,
	}
}

func TestSanitizeName(t *testing.T) {
	session := NewSession(context.Background(), "asdf", "program")
	require.Equal(t, Prefix()+"asdf", session.sanitizedName)

	session = NewSession(context.Background(), "a sd f . . asdf", "program")
	require.Equal(t, Prefix()+"asdf__asdf", session.sanitizedName)
}

func TestIsReadyForPrompt(t *testing.T) {
	cases := []struct {
		name    string
		program string
		content string
		want    bool
	}{
		{
			name:    "claude trust screen is not ready",
			program: "claude",
			content: "Do you trust the files in this folder?\n  Yes  No",
			want:    false,
		},
		{
			name:    "claude new MCP server screen is not ready",
			program: "claude",
			content: "new MCP server detected. Approve?",
			want:    false,
		},
		{
			name:    "empty pane is not ready",
			program: "claude",
			content: "   \n\t\n",
			want:    false,
		},
		{
			name:    "claude idle input box is ready",
			program: "claude",
			content: "╭───╮\n│ > │  ? for shortcuts\n╰───╯",
			want:    true,
		},
		{
			name:    "non-claude doc-url gate is not ready",
			program: "aider",
			content: "Open documentation url for more info? (Y)es/(N)o",
			want:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ptyFactory := NewMockPtyFactory(t)
			cmdExec := cmd_test.MockCmdExec{
				RunFunc: func(cmd *exec.Cmd) error { return nil }, // session exists
				OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
					return []byte(tc.content), nil
				},
			}
			session := NewSessionWithDeps(context.Background(), "ready-test", tc.program, ptyFactory, cmdExec)
			require.Equal(t, tc.want, session.IsReadyForPrompt())
		})
	}
}

// Regression: the per-agent startup gates (adapter data, consumed by Poll's PaneGate
// classification and by IsReadyForPrompt/AwaitingInput) must still recognize each gate
// string — and only for their own agent, so one agent's screen never gates another's.
func TestStartupGates(t *testing.T) {
	cases := []struct {
		name    string
		program string
		content string
		want    bool
	}{
		{"claude trust folder", "claude", "Do you trust the files in this folder?", true},
		{"claude new MCP server (lowercase)", "claude", "new MCP server found in this project", true},
		{"claude new MCP server (capital-N)", "claude", "New MCP server found in this project: nanoclaw", true},
		{"aider doc url", "aider", "Open documentation url for more info", true},
		{"claude idle box has no gate", "claude", "│ > │  ? for shortcuts", false},
		{"claude ignores aider gate string", "claude", "Open documentation url for more info", false},
		{"aider ignores claude gate string", "aider", "Do you trust the files in this folder?", false},
		// gemini's gate needs the accept row INSIDE a box whose bottom border ends the pane
		// (#713) — the box is what separates a live dialog from the same words in the
		// transcript, so a bare row is deliberately not enough here.
		{"gemini folder-trust option rows", "gemini",
			" ╭────────────────────────────╮\n │ ● 1. Trust folder (repo)   │\n │   3. Don't trust           │\n ╰────────────────────────────╯", true},
		{"gemini option row outside a box is transcript", "gemini", "● 1. Trust folder (repo)\n  3. Don't trust", false},
		// 0.55.1 gave gemini the same headline claude uses, and gemini deliberately does NOT
		// key on it — it is unreachable once a narrow pane wraps it across the dialog's box
		// border (#713). So the shared string must gate claude above and not gemini here.
		{"gemini ignores the shared headline", "gemini", "Do you trust the files in this folder?", false},
		// Pre-adapter, every non-claude program matched aider's documentation gate and
		// received its stray 'D' keystroke; an unknown agent must match nothing.
		{"unknown agent has no gates", "someagent", "Open documentation url for more info", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSessionWithDeps(context.Background(), "gate-test", tc.program, NewMockPtyFactory(t), cmd_test.MockCmdExec{})
			_, ok := s.adapter.GateUp(tc.content)
			require.Equal(t, tc.want, ok)
		})
	}
}

// geminiTrustGateProdCapture is gemini 0.55.1's folder-trust dialog in PRODUCTION capture
// form: `tmux capture-pane -p -e -J`, the form tmux.go actually reads, escapes and all.
// Driven 2026-08-16 by scripts/drive-agent.sh at 40x40 on an isolated socket, verbatim but
// for trailing all-blank rows; written as one quoted line per row because a Go raw string
// cannot hold an ESC byte.
//
// It exists because every other gemini fixture in this repo is `capture-pane -p`, which
// tmux strips the escapes out of, and the gate's anchor is far more sensitive to them than
// the literal window it replaced (#713 round 4 review). isHorizontalRule rejects a line
// holding any character outside the box set, so a single escape surviving cleanForDetection
// ON THE BORDER would take the whole gate down rather than cost it a line — and the two
// load-bearing rows here, the bottom border and the accept row, both carry SGR mid-line.
//
// This is the FITS-THE-PANE shape, where the border is the last non-empty row. That is the
// shape the pre-#713 anchor already accepted, so on its own it is a control for a case that
// was never at risk; geminiTrustGateOverflowProdCapture below is the shape the trailing
// allowance exists for, and the pair is what covers the anchor.
//
// Measured on this capture: 152 escape sequences, every one CSI SGR, and zero OSC. OSC is
// the class ansiRegex does not match, so the hazard is real in principle and absent in fact
// at 0.55.1. Those three numbers are RECOMPUTED by the test below, which an earlier draft of
// this comment claimed of an assertion that did no such thing — it checked only that the raw
// pane does not gate and the cleaned one does, which stays true at any escape count and with
// an OSC sequence added anywhere off the two load-bearing rows.
//
// The gating assertions are still there and check BOTH directions: uncleaned the pane must NOT
// gate, or the fixture has quietly lost its escapes and proves nothing.
var geminiTrustGateProdCapture = strings.Join([]string{
	"",
	" \x1b[38;2;71;150;228m▝\x1b[38;2;102;136;217m▜\x1b[38;2;132;122;206m▄\x1b[38;2;164;113;167m \x1b[38;2;195;103;127m ",
	"\x1b[39m \x1b[38;2;71;150;228m \x1b[38;2;102;136;217m \x1b[38;2;132;122;206m▝\x1b[38;2;164;113;167m▜\x1b[38;2;195;103;127m▄",
	"\x1b[39m \x1b[38;2;71;150;228m \x1b[38;2;102;136;217m▗\x1b[38;2;132;122;206m▟\x1b[38;2;164;113;167m▀\x1b[38;2;195;103;127m ",
	"\x1b[39m \x1b[38;2;71;150;228m▝\x1b[38;2;102;136;217m▀\x1b[38;2;132;122;206m \x1b[38;2;153;116;180m \x1b[38;2;174;109;153m \x1b[38;2;195;103;127m ",
	"",
	"\x1b[39m \x1b[1m\x1b[38;2;255;255;255mGemini CLI\x1b[0m\x1b[38;2;175;175;175m v0.55.1",
	"",
	"",
	"",
	"\x1b[38;2;255;255;175mℹ Skipping project agents due to",
	"\x1b[39m  \x1b[38;2;255;255;175muntrusted folder. To enable, ensure",
	"\x1b[39m  \x1b[38;2;255;255;175mthat the project root is trusted.",
	"",
	"\x1b[39m \x1b[38;2;255;255;175m╭────────────────────────────────────╮",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m                                    \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[1m\x1b[38;2;255;255;255mDo you trust the files in this\x1b[0m     \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[1m\x1b[38;2;255;255;255mfolder?\x1b[0m                            \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m                                    \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mTrusting a folder allows Gemini\x1b[39m    \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mCLI to load its local\x1b[39m              \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mconfigurations, including custom\x1b[39m   \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mcommands, hooks, MCP servers,\x1b[39m      \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255magent skills, and settings. These\x1b[39m  \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mconfigurations could execute code\x1b[39m  \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mon your behalf or change the\x1b[39m       \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mbehavior of the CLI.\x1b[39m               \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m                                    \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m                                    \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;215;255;215m\x1b[48;2;0;95;0m●\x1b[39m \x1b[38;2;215;255;215m1.\x1b[39m \x1b[38;2;215;255;215mTrust folder (repo)\x1b[39m          \x1b[49m \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255m \x1b[39m \x1b[38;2;255;255;255m2.\x1b[39m \x1b[38;2;255;255;255mTrust parent folder (gemini7…\x1b[39m \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255m \x1b[39m \x1b[38;2;255;255;255m3.\x1b[39m \x1b[38;2;255;255;255mDon't trust\x1b[39m                   \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m                                    \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m╰────────────────────────────────────╯",
}, "\n")

// The capture-form seam, which neither package can test alone: the fixtures live in
// session/agent and cleanForDetection lives here, so this is the only place the pane
// production reads meets the matcher production runs.
func TestGeminiTrustGateSurvivesProductionCaptureForm(t *testing.T) {
	// The escape census, recomputed rather than asserted in prose. ansiRegex is production's
	// own CSI matcher (poll.go); anything it does not match is what survives cleanForDetection
	// and can land on the border.
	csi := ansiRegex.FindAllString(geminiTrustGateProdCapture, -1)
	require.Len(t, csi, 152,
		"the fixture is a frozen capture; a different escape count means it was edited, and the "+
			"claim that every escape here is one ansiRegex strips no longer rests on anything")
	for _, seq := range csi {
		require.True(t, strings.HasSuffix(seq, "m"),
			"%q is a CSI sequence that is not SGR; the comment above claims all 152 are SGR", seq)
	}
	require.Equal(t, 152, strings.Count(geminiTrustGateProdCapture, "\x1b"),
		"every ESC in the capture must be one ansiRegex matched — a leftover is by definition "+
			"an escape production does not strip, and OSC is the class that reaches the border")
	require.NotContains(t, geminiTrustGateProdCapture, "\x1b]",
		"no OSC at 0.55.1: that is the measurement, and it is what makes the hazard theoretical")

	s := NewSessionWithDeps(context.Background(), "gate-test", "gemini", NewMockPtyFactory(t), cmd_test.MockCmdExec{})

	_, raw := s.adapter.GateUp(geminiTrustGateProdCapture)
	require.False(t, raw,
		"the raw -e capture must NOT gate: its border carries SGR, so if this passes the "+
			"fixture has lost the escapes that make the cleaned assertion below mean anything")

	_, cleaned := s.adapter.GateUp(cleanForDetection(geminiTrustGateProdCapture))
	require.True(t, cleaned,
		"gemini's trust gate must fire on the pane PRODUCTION captures once it is cleaned the "+
			"way production cleans it; the fixtures in session/agent are the -p form, which "+
			"never carried an escape for the anchor to trip over")
}

// geminiTrustGateOverflowProdCapture is the OTHER shape, in the same production form:
// gemini 0.55.1's folder-trust dialog OVERFLOWING its pane, so a "Press Ctrl+O to s…" hint
// renders BELOW the box's bottom border. Driven 2026-08-17 by scripts/drive-agent.sh at 24x24
// — `up gemini 24 24` then `sample`, never a resize, because a resized rung of this dialog is
// not equal to a native one (#713).
//
// It exists because the capture above cannot stand for this one. That one ends AT the border,
// which is the shape the pre-#713 anchor already accepted, so as a seam test it was a control
// for a case that was never at risk. The seven-of-twelve geometries that missed all look like
// THIS, and the two rows the trailing allowance turns on — the border, and the hint that now
// sits under it — each carry their own SGR run, in different colours. If either lost an escape
// ansiRegex does not strip, the border stops being a horizontal rule and the whole gate goes
// down rather than costing a line.
//
// Its `capture-pane -p` twin is committed in session/agent as geminiTrustGateOverflowPane24,
// and the two were byte-identical once the escapes came out — driven a day apart, in run
// directories with different names, which is also an independent reproduction of the render.
// That comparison was made by hand at capture time and NOTHING HOLDS IT: the two fixtures are
// unexported consts in packages that cannot see each other's test files, so if one is edited
// the pair drifts silently. It is disclosed rather than asserted because a copy of either
// fixture on this side of the boundary would be a third artifact to keep in step, not a guard.
var geminiTrustGateOverflowProdCapture = strings.Join([]string{
	" \x1b[38;2;255;255;175m│\x1b[39m \x1b[1m\x1b[38;2;255;255;255mfiles in this\x1b[0m      \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[1m\x1b[38;2;255;255;255mfolder?\x1b[0m            \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m                    \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mTrusting a folder\x1b[39m  \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mallows Gemini CLI\x1b[39m  \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mto load its local\x1b[39m  \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mconfigurations,\x1b[39m    \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mincluding custom\x1b[39m   \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mcommands, hooks,\x1b[39m   \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mMCP servers, agent\x1b[39m \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mskills, and\x1b[39m        \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255msettings. These\x1b[39m    \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mconfigurations\x1b[39m     \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255mcould execute code\x1b[39m \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;175;175;175m... last 5 lines …\x1b[39m \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m                    \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;215;255;215m\x1b[48;2;0;95;0m●\x1b[39m \x1b[38;2;215;255;215m1.\x1b[39m \x1b[38;2;215;255;215mTrust folder…\x1b[39m\x1b[49m \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255m \x1b[39m \x1b[38;2;255;255;255m2.\x1b[39m \x1b[38;2;255;255;255mTrust parent…\x1b[39m \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m \x1b[38;2;255;255;255m \x1b[39m \x1b[38;2;255;255;255m3.\x1b[39m \x1b[38;2;255;255;255mDon't trust\x1b[39m   \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m│\x1b[39m                    \x1b[38;2;255;255;175m│",
	"\x1b[39m \x1b[38;2;255;255;175m╰────────────────────╯",
	"\x1b[39m   \x1b[38;2;215;175;255mPress Ctrl+O to s…",
}, "\n")

// The seam for the overflow render, which is the one the trailing allowance exists for.
func TestGeminiTrustGateSurvivesProductionCaptureFormWhenOverflowing(t *testing.T) {
	csi := ansiRegex.FindAllString(geminiTrustGateOverflowProdCapture, -1)
	require.Len(t, csi, 133,
		"the fixture is a frozen capture; a different escape count means it was edited")
	for _, seq := range csi {
		require.True(t, strings.HasSuffix(seq, "m"), "%q is a CSI sequence that is not SGR", seq)
	}
	require.Equal(t, 133, strings.Count(geminiTrustGateOverflowProdCapture, "\x1b"),
		"every ESC must be one ansiRegex matched — a leftover is an escape production does "+
			"not strip, and this render puts two load-bearing rows in its path")
	require.NotContains(t, geminiTrustGateOverflowProdCapture, "\x1b]", "no OSC at 0.55.1")

	// The premise that makes this fixture different from the one above: the pane does NOT end
	// at the border. If a future capture did, this would silently become a second copy of it.
	rows := strings.Split(strings.TrimRight(geminiTrustGateOverflowProdCapture, "\n"), "\n")
	require.Contains(t, rows[len(rows)-1], "Press Ctrl+O",
		"the last row must be the overflow hint — that is the whole difference between this "+
			"capture and geminiTrustGateProdCapture")

	s := NewSessionWithDeps(context.Background(), "gate-test", "gemini", NewMockPtyFactory(t), cmd_test.MockCmdExec{})

	_, raw := s.adapter.GateUp(geminiTrustGateOverflowProdCapture)
	require.False(t, raw,
		"the raw -e capture must NOT gate, or the fixture has lost the escapes that make the "+
			"cleaned assertion below mean anything")

	_, cleaned := s.adapter.GateUp(cleanForDetection(geminiTrustGateOverflowProdCapture))
	require.True(t, cleaned,
		"an OVERFLOWING dialog must still gate once production cleans the pane — this is #713 "+
			"on the height axis, at the one shape the other production capture cannot cover")
}

// A pane sitting on a startup/trust gate must classify as PaneGate, not PaneIdle.
// Regression for #266: gates carry no busy marker and match no prompt matcher, so
// without the gate branch they fall through to idle and the row lies as Ready while
// the session is actually blocked (and its queued first prompt is held indefinitely).
func TestPollClassifiesStartupGateAsGate(t *testing.T) {
	cases := []struct{ name, program, content string }{
		{"claude folder-trust", "claude", "Quick safety check…\n ❯ 1. Yes, I trust this folder\n Enter to confirm · Esc to cancel"},
		{"claude new MCP server", "claude", "New MCP server found in this project: nanoclaw\n [Enter] to approve"},
		{"codex folder-trust", "codex", "Do you trust the contents of this directory?\n› 1. Yes, continue"},
		// Keyed on the option row inside a live box, not the headline: gemini's headline is
		// unreachable once a narrow pane wraps it across the box border, and the box is what
		// proves the dialog is on screen rather than in scrollback (#713,
		// session/agent/gemini_pane_test.go).
		{"gemini folder-trust", "gemini",
			" ╭──────────────────────────────────────╮\n │ Do you trust the files in this folder│\n │ ● 1. Trust folder (repo)             │\n │   3. Don't trust                     │\n ╰──────────────────────────────────────╯"},
		{"aider first-run docs", "aider", "Open documentation url for more info? (Y)es/(N)o/(D)on't ask again [Yes]:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.content
			s := pollSession(t, tc.program, &c, nil)
			require.Equal(t, PaneGate, s.Poll(), "a startup gate must classify as PaneGate")
		})
	}
}

// When the gate clears, the next poll must move off PaneGate cleanly: the gate branch
// sets lastReported=PaneGate, which must not wedge the marker-absent working grace.
func TestPollGateClearsToIdle(t *testing.T) {
	c := "Quick safety check…\n ❯ 1. Yes, I trust this folder\n Enter to confirm · Esc to cancel"
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneGate, s.Poll())

	// Composer on screen, no gate, no busy marker: a cleared claude gate settles to idle
	// immediately (the grace only holds a prior PaneWorking, never a prior PaneGate).
	c = strings.Repeat("─", 40) + "\n❯ \n" + strings.Repeat("─", 40) + "\n  ? for shortcuts"
	require.Equal(t, PaneIdle, s.Poll(), "a cleared gate settles to idle, not wedged in the working grace")
}

// A gate literal quoted in the transcript body (a claude session editing this registry, or
// discussing a "New MCP server") must NOT gate the whole pane: gate detection is confined to
// the live dialog region, so a pane whose bottom chrome is a normal composer settles to idle.
// Without the confinement the row would flip to a bogus "waiting on setup screen" while the
// session is genuinely idle/working (#266 follow-up).
func TestPollGateLiteralInBodyIsNotGate(t *testing.T) {
	var b strings.Builder
	b.WriteString("New MCP server found in this project: nanoclaw\n")
	b.WriteString("Do you trust the files in this folder?\n")
	for i := 0; i < agent.WindowPrompt+5; i++ {
		b.WriteString("plain transcript line\n")
	}
	// Bottom chrome is an ordinary idle composer: no gate, no busy marker.
	b.WriteString(strings.Repeat("─", 40) + "\n❯ \n" + strings.Repeat("─", 40) + "\n  ? for shortcuts")
	c := b.String()
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneIdle, s.Poll(), "a gate literal in the transcript body must not classify the pane as PaneGate")
}

// The reported shape, which the distance-based test above cannot express: the quote is the
// agent's LAST message, sitting directly above the composer, on a pane that is provably
// working (the below-box busy marker is right there). GateUp is checked before the marker, so
// a false gate beat positive proof of work — the atrium log recorded this pane flapping
// between "marker → working" and "gate → needs-input" as its own output scrolled the literal
// in and out of the old bottom-15 window.
func TestPollQuotedGateLiteralAboveComposerStaysWorking(t *testing.T) {
	c := strings.Join([]string{
		"● The dialog titles are \"New MCP server found in this project: nanoclaw\" and",
		"  \"3 new MCP servers found in this project\", and I trust this folder is the other one.",
		"",
		"✽ Sautéing… (2m 52s · ↓ 5.7k tokens)",
		"",
		strings.Repeat("─", 40) + " my-branch ──",
		"❯ ",
		strings.Repeat("─", 52),
		"  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents",
	}, "\n")
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneWorking, s.Poll(), "a working pane quoting the gate's titles must read as working, not gated")
}

// A dead/missing tmux session must not be probed: the pollers should short-circuit
// without ever running capture-pane, so a single dead session can't flood the log
// and error box with "error capturing pane content: exit status 1" every tick.
func TestPollersSkipCaptureWhenSessionDead(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	captured := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			// has-session fails => the session no longer exists.
			return fmt.Errorf("can't find session")
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			captured = true
			return nil, fmt.Errorf("error capturing pane content: exit status 1")
		},
	}
	session := NewSessionWithDeps(context.Background(), "dead", "claude", ptyFactory, cmdExec)

	updated, hasPrompt := session.HasUpdated()
	require.False(t, updated)
	require.False(t, hasPrompt)
	require.False(t, session.IsReadyForPrompt())
	require.False(t, captured, "capture-pane must not run when the tmux session is dead")
}

// A dead/missing session must classify as PaneDead (distinct from the PaneUnknown a
// transient capture failure yields), so the metadata loop can flag it lost from this one
// has-session check instead of forking its own. Neither poller may run capture-pane.
//
// Driven from alreadyGoneMessages, the same table close_test.go holds the kill path to, so
// a message added to sessionAlreadyGone is proven on BOTH sides of that shared predicate
// rather than only on the one whose test the author happened to open. The diagnostic goes to
// stderr and the error carries only the exit status, which is the shape real tmux has: the
// error-string fallback that a test fake would otherwise hit proves nothing about production.
func TestPollersReturnDeadWhenSessionDead(t *testing.T) {
	for _, msg := range alreadyGoneMessages {
		t.Run(msg, func(t *testing.T) {
			captured := false
			cmdExec := cmd_test.MockCmdExec{
				RunFunc: func(cmd *exec.Cmd) error {
					// has-session fails => the session no longer exists.
					if cmd.Stderr != nil {
						_, _ = fmt.Fprintln(cmd.Stderr, msg)
					}
					return fmt.Errorf("exit status 1")
				},
				OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
					captured = true
					return nil, fmt.Errorf("error capturing pane content: exit status 1")
				},
			}
			s := NewSessionWithDeps(context.Background(), "dead", "claude", NewMockPtyFactory(t), cmdExec)

			require.Equal(t, PaneDead, s.Poll(), "a dead session must classify as PaneDead")
			require.Equal(t, PaneDead, s.PollNow(), "a dead session must classify as PaneDead")
			require.False(t, captured, "capture-pane must not run when the tmux session is dead")
		})
	}
}

// An inconclusive has-session probe must NOT read as a dead session: a deadline-kill of a
// slow-but-alive server, a cancellation on the way out of the app, a fork/exec failure
// under full-sweep fan-out, or a socket tmux could not open. Each classifies as PaneUnknown
// so the metadata loop keeps the prior status and the lost-session strike counter never
// advances on a transient infrastructure hiccup — the mass-pause bug in #270.
//
// Three of these pass an *exec.ExitError — "cancelled context", "cancellation reported by
// the executor", and every "unreachable socket" row — which liveness otherwise reads as a
// definitive "no" on the grounds that has-session only fails when the session is absent.
// Each is a counterexample to that premise, and each is guarded by a case ahead of the
// ExitError branch. The rows that are NOT an ExitError ("deadline exceeded", "wrapped
// deadline", "exec failure", "wrapped cancellation") never reach that branch at all: they
// fall to the default, which is already indeterminate. They pin the guard's reach, not its
// precedence.
func TestPollersReturnUnknownOnIndeterminateProbe(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	type indeterminateCase struct {
		name string
		err  error
		// stderr is where real tmux puts its diagnostic; the classification reads it.
		stderr string
		// sessionCtx is the session's base context, nil for Background. A cancelled one
		// is how app shutdown reaches an in-flight probe.
		sessionCtx context.Context
	}
	cases := []indeterminateCase{
		// A timeout kill: exec.CommandContext SIGKILLs the process and Run surfaces
		// the wait error, but ctx.Err()/the error chain carries the deadline.
		{name: "deadline exceeded", err: context.DeadlineExceeded},
		{name: "wrapped deadline", err: fmt.Errorf("signal: killed: %w", context.DeadlineExceeded)},
		// A fork/exec failure never reaches the server (EMFILE/ENOMEM): not an
		// ExitError, not a recognized "gone" message.
		{name: "exec failure", err: fmt.Errorf("fork/exec /usr/bin/tmux: too many open files")},
		// CANCELLED, not expired. The kill looks identical from Run's side — an
		// ExitError carrying "signal: killed" — and ctx.Err() reads Canceled, so a
		// guard written against DeadlineExceeded alone lets this reach the ExitError
		// branch and reports a live session dead on the way out of the app.
		{name: "cancelled context", err: &exec.ExitError{}, sessionCtx: cancelled},
		{name: "wrapped cancellation", err: fmt.Errorf("signal: killed: %w", context.Canceled)},
		// The guard checks the error chain as well as ctx.Err(), and this row is the only
		// thing that can tell the difference: an executor that reports BOTH the exit and
		// the cause, against a context of its own that this session's ctx knows nothing
		// about (ctx.Err() is nil here, so the chain check is load-bearing). Drop
		// `errors.Is(err, context.Canceled)` from liveness's guard and this row alone goes
		// red — the plain "wrapped cancellation" row above cannot, because it is not an
		// ExitError and reaches the already-indeterminate default either way.
		{
			name: "cancellation reported by the executor",
			err:  fmt.Errorf("%w: %w", &exec.ExitError{}, context.Canceled),
		},
	}
	// tmux ran, exited non-zero, and never reached a server: connect() failed on a socket
	// that exists and may still be serving the session (#730). "has-session only fails when
	// the session is absent" does not hold for any of these. Driven from the shared table so
	// the poll side covers every message the Close side does — covering only the first let a
	// socketUnreachable narrowed to "permission denied" keep the suite green.
	for _, msg := range unreachableSocketMessages {
		cases = append(cases, indeterminateCase{
			name:   "unreachable socket: " + msg,
			err:    &exec.ExitError{},
			stderr: msg,
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captured := false
			cmdExec := cmd_test.MockCmdExec{
				RunFunc: func(cmd *exec.Cmd) error {
					if cmd.Stderr != nil && tc.stderr != "" {
						_, _ = fmt.Fprintln(cmd.Stderr, tc.stderr)
					}
					return tc.err
				},
				OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
					captured = true
					return nil, fmt.Errorf("should not be called")
				},
			}
			baseCtx := tc.sessionCtx
			if baseCtx == nil {
				baseCtx = context.Background()
			}
			s := NewSessionWithDeps(baseCtx, "blip", "claude", NewMockPtyFactory(t), cmdExec)

			require.Equal(t, PaneUnknown, s.Poll(), "an indeterminate probe must not classify as dead")
			require.Equal(t, PaneUnknown, s.PollNow(), "an indeterminate probe must not classify as dead")
			require.False(t, captured, "capture-pane must not run on an indeterminate probe")
		})
	}
}

// An unreachable socket is the one indeterminate case that can persist forever, and the
// classification it gets is silent by construction: the status is left untouched and the
// lost-session strike count is cleared, so nothing in the UI changes and no recovery runs.
// The log line is the only evidence the user or a bug report can reach, which is why it is
// asserted rather than left as a comment — and why it must carry tmux's own diagnostic: the
// session name alone does not say whether the socket is missing, misowned, or not a socket.
//
// Throttled to one line per window, so the assertion is on the first probe only. That the
// second is silent is the point of the throttle (a 500ms poll loop would otherwise write a
// line per session per tick), so it is asserted too.
func TestUnreachableSocketIsLogged(t *testing.T) {
	var buf bytes.Buffer
	prev := log.ErrorLog.Writer()
	log.ErrorLog.SetOutput(&buf)
	t.Cleanup(func() { log.ErrorLog.SetOutput(prev) })

	const diagnostic = "error connecting to /tmp/sock (Permission denied)"
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if cmd.Stderr != nil {
				_, _ = fmt.Fprintln(cmd.Stderr, diagnostic)
			}
			return &exec.ExitError{}
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return nil, fmt.Errorf("should not be called") },
	}
	s := NewSessionWithDeps(context.Background(), "frozen", "claude", NewMockPtyFactory(t), cmdExec)

	require.Equal(t, PaneUnknown, s.Poll())
	logged := buf.String()
	require.Contains(t, logged, "unreachable",
		"a socket that cannot be opened must leave a trace: the classification changes nothing visible")
	require.Contains(t, logged, diagnostic,
		"tmux's own diagnostic must be folded in — which errno it is, is the whole diagnosis")
	require.Contains(t, logged, "frozen", "the log line must name the session it froze")

	buf.Reset()
	require.Equal(t, PaneUnknown, s.Poll())
	require.Empty(t, buf.String(), "the second probe in the same window must be throttled, not repeated")
}

// The happy path must keep working: an alive session still captures. For a program with
// no busy marker, freshly seen content classifies as working (the content-change path),
// which the HasUpdated shim reports as updated.
func TestHasUpdatedCapturesWhenSessionAlive(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	cmdExec := cmd_test.MockCmdExec{
		RunFunc:    func(cmd *exec.Cmd) error { return nil }, // session exists
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("hello"), nil },
	}
	session := NewSessionWithDeps(context.Background(), "alive", "aider", ptyFactory, cmdExec)

	updated, _ := session.HasUpdated()
	require.True(t, updated, "first capture of new content should report updated")
}

// TestSessionDeathStopsProbing drives a REAL tmux session (not mocks) to reproduce the
// production flood: once a started session's pane is killed out from under cs, the
// pollers must report "not alive" and stop capturing, instead of running capture-pane
// and getting "exit status 1" on every tick.
func TestSessionDeathStopsProbing(t *testing.T) {
	// Intentionally a plain skip, NOT testutil.RequireTmux: this test is
	// -skip'd by name in CI (local-only/flaky even with tmux; see build.yml),
	// so it must never hard-fail under ATRIUM_CI_REQUIRE_TMUX=1. Don't convert.
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	// The skip policy is about *tmux being present*; isolation is a separate gate and
	// this test does not get to opt out of it. It starts a real session on
	// `-L socketName()` and then kills a pane out from under it — unsandboxed, that is
	// the developer's live fleet (#581).
	testutil.RequireSandboxedTmux(t)

	name := fmt.Sprintf("death-%s-%d", t.Name(), rand.Int31())
	session := NewSession(context.Background(), name, "sleep 300")
	require.NoError(t, session.Start(t.TempDir()))
	t.Cleanup(func() { _ = session.Close() })

	// While alive: detectable, and a probe runs without panicking.
	require.True(t, session.DoesSessionExist())
	_, _ = session.HasUpdated()

	// Kill the session out from under cs (simulates a crash / external kill).
	// Must target the same dedicated socket the session was created on — bare
	// `tmux kill-session` hits tmux's default socket, where this session never
	// existed, so it would fail with "exit status 1".
	require.NoError(t, tmuxCommand(context.Background(), "kill-session", "-t", session.sanitizedName).Run())

	// The pollers must now short-circuit cleanly rather than erroring every tick.
	require.False(t, session.DoesSessionExist())
	updated, hasPrompt := session.HasUpdated()
	require.False(t, updated)
	require.False(t, hasPrompt)
	require.False(t, session.IsReadyForPrompt())
}

// pollSession builds a Session whose CapturePaneContent returns *content (or an
// error when *fail is true), so a test can drive Poll across ticks by mutating them.
// RunFunc reports the session as alive so Poll's liveness guard does not short-circuit.
func pollSession(t *testing.T, program string, content *string, fail *bool) *Session {
	t.Helper()
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error { return nil }, // session exists
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			if fail != nil && *fail {
				return nil, fmt.Errorf("capture failed")
			}
			return []byte(*content), nil
		},
	}
	return NewSessionWithDeps(context.Background(), "poll-test", program, NewMockPtyFactory(t), cmdExec)
}

func TestCleanForDetection(t *testing.T) {
	require.Equal(t, "hello", cleanForDetection("\x1b[31mhel\x1b[0mlo"))
	require.Equal(t, "a\nb", cleanForDetection("a  \t\nb   "))
}

// A Claude pane showing the busy marker is PaneWorking, and stays PaneWorking even as the
// marker line's elapsed-time counter ticks — proving the counter no longer flips state.
func TestPollClaudeBusyMarkerIsStable(t *testing.T) {
	content := "✻ Cogitating… (5s · esc to interrupt)"
	c := content
	s := pollSession(t, "claude", &c, nil)

	require.Equal(t, PaneWorking, s.Poll())
	c = "✻ Cogitating… (6s · esc to interrupt)" // counter advanced, marker still present
	require.Equal(t, PaneWorking, s.Poll())
	c = "✻ Cogitating… (7s · esc to interrupt)"
	require.Equal(t, PaneWorking, s.Poll())
}

// Poll feeds the live permission mode from the footer into the session, end to
// end: real capture → cleanForDetection (ANSI strip) → adapter detection. The
// box-rule + below-the-box footer mirror a live claude pane (see Step-0 capture).
func TestPollDetectsPermissionMode(t *testing.T) {
	box := func(footer string) string {
		rule := strings.Repeat("─", 40)
		return rule + "\n❯ \n" + rule + "\n" + footer
	}
	cases := []struct{ name, footer, want string }{
		{"plan", "  ⏸ plan mode on (shift+tab to cycle) · ← for agents", "plan"},
		{"acceptEdits", "  ⏵⏵ accept edits on (shift+tab to cycle) · ← for agents", "acceptEdits"},
		{"auto", "  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents", "auto"},
		{"bypass", "  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents", "bypassPermissions"},
		{"default", "  ? for shortcuts · ← for agents", "default"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content := box(c.footer)
			s := pollSession(t, "claude", &content, nil)
			s.Poll()
			require.Equal(t, c.want, s.RuntimePermissionMode())
		})
	}

	// A real capture carries ANSI; cleanForDetection must strip it so footerRegion
	// and the detector still fire (the rule line would fail isHorizontalRule raw).
	ansi := "\x1b[2m" + strings.Repeat("─", 40) + "\x1b[0m\n❯ \n" +
		strings.Repeat("─", 40) + "\n\x1b[39m  ⏵⏵ auto mode on (shift+tab to cycle)\x1b[39m"
	s := pollSession(t, "claude", &ansi, nil)
	s.Poll()
	require.Equal(t, "auto", s.RuntimePermissionMode(), "ANSI-wrapped footer must still detect")

	// Sticky: an indeterminate (busy, no indicator) footer leaves the last mode in
	// place rather than blanking it, so the chip doesn't flicker mid-turn.
	c := box("  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents")
	s = pollSession(t, "claude", &c, nil)
	s.Poll()
	require.Equal(t, "auto", s.RuntimePermissionMode())
	c = "✻ Cogitating… (6s · esc to interrupt)" // no box, no mode indicator
	s.Poll()
	require.Equal(t, "auto", s.RuntimePermissionMode(), "indeterminate footer must keep the last mode")
}

func TestPollClaudeIdleAndPrompt(t *testing.T) {
	idle := "╭───╮\n│ > │  ? for shortcuts\n╰───╯"
	c := idle
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneIdle, s.Poll(), "idle input box with no marker is idle immediately")

	c = claudeFetchPane
	require.Equal(t, PanePrompt, s.Poll(), "a tool-permission y/n prompt takes precedence")
}

// An interactive selection prompt (AskUserQuestion) blocks on the user just like the
// permission dialog, even though it shows no permission text. Its footer is the signal
// — and the real idle/working footers must not trip it. Selections classify as
// PanePromptManual (#271): they are judgment prompts (AskUserQuestion renders even in
// bypass/auto permission modes), so autoyes must surface them instead of tapping Enter.
func TestPollClaudeSelectionPrompt(t *testing.T) {
	selection := "How do you want to be notified?\n  1. Telegram\n  2. Email\n" +
		"Enter to select · ↑/↓ to navigate · Esc to cancel"
	c := selection
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePromptManual, s.Poll(), "a selection prompt is a manual needs-input state")

	_, hasPrompt := s.HasUpdated()
	require.False(t, hasPrompt, "the HasUpdated shim must not report a manual prompt as tappable")

	// The live AskUserQuestion footer carries extra hints ("n to add notes") between the
	// navigate and cancel tokens; it must still classify as a prompt.
	c = "Server restart?\n  1. Relaunch\n❯ 2. Restart now\n  3. Nav only\n" +
		"Enter to select · ↑/↓ to navigate · n to add notes · Esc to cancel"
	s = pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePromptManual, s.Poll(), "selection footer with extra hints is a manual prompt")

	// Footers captured from live idle/working panes must classify as idle/working,
	// never as a prompt.
	for _, footer := range []string{
		"❯ \n⏵⏵ auto mode on · 1 shell · ctrl+t to hide tasks · ← for agents · ↓ to manage",
		"❯ \n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
	} {
		c = footer
		s := pollSession(t, "claude", &c, nil)
		require.Equal(t, PaneIdle, s.Poll(), "idle footer must not be read as a prompt: %q", footer)
	}
}

// The plan-approval dialog classifies as PanePromptManual — its auto-answer is
// destructive (Enter accepts the plan AND enables auto mode), so autoyes paths
// must surface it instead of tapping. The HasUpdated shim's hasPrompt must stay
// false for it: the daemon's legacy tap path keyed on that bit, so excluding the
// manual state there is the fail-safe for any caller not yet switched to Poll.
// Pane content mirrors a live 2.1.170 capture (see agent.TestClaudePlanPrompt).
func TestPollClaudePlanPrompt(t *testing.T) {
	plan := strings.Join([]string{
		"   Claude has written up a plan and is ready to execute. Would you like to proceed?",
		"",
		"   ❯ 1. Yes, and use auto mode",
		"     2. Yes, manually approve edits",
		"     3. No, refine with Ultraplan on Claude Code on the web",
		"     4. Tell Claude what to change",
		"        shift+tab to approve with this feedback",
		"",
		"   ctrl+g to edit in  VS Code  · ~/.claude/plans/make-a-plan.md",
	}, "\n")
	c := plan
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePromptManual, s.Poll(), "plan approval is a manual-only prompt")

	_, hasPrompt := s.HasUpdated()
	require.False(t, hasPrompt, "the HasUpdated shim must not report a manual prompt as tappable")
}

// A session launched with a bad --model stays alive showing claude's error and
// an idle input box; Poll must surface it as a manual prompt (needs-input),
// never auto-tap — there is nothing for autoyes to answer. Pane content mirrors
// a live 2.1.170 capture (see agent.TestClaudeModelErrorPrompt).
func TestPollClaudeModelError(t *testing.T) {
	pane := strings.Join([]string{
		"❯ say hi",
		"",
		"● There's an issue with the selected model (atrium-bogus-model-check). It may not exist or you may",
		"  not have access to it. Run /model to pick a different model.",
		"",
		"✻ Cogitated for 0s",
		"",
		strings.Repeat("─", 100),
		"❯ ",
		strings.Repeat("─", 100),
		"  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
	}, "\n")
	c := pane
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePromptManual, s.Poll(), "a bad-model launch must surface as needs-input")

	_, hasPrompt := s.HasUpdated()
	require.False(t, hasPrompt, "the HasUpdated shim must not report a manual prompt as tappable")
}

// A custom Claude Code statusLine renders below the selection-prompt footer (captured live:
// the overlay draws a horizontal rule, then "6. Chat about this", the key-hint footer, blank
// padding, and finally the user's multi-line statusLine). The footer is then several non-empty
// lines above the pane bottom, so a fixed bottom-N window misses it. The rule-delimited
// segment scan (selectionFooterVisible) keeps it visible regardless of the statusLine's
// height.
func TestPollClaudeSelectionPromptBelowStatusLine(t *testing.T) {
	rule := strings.Repeat("─", 80)
	pane := strings.Join([]string{
		"  4. IMP-1573: midday API exhaustion gate",
		"  5. Type something.",
		rule,
		"  6. Chat about this",
		"",
		"Enter to select · ↑/↓ to navigate · Esc to cancel",
		"", "", "", "", "", "", "", "", "", "",
		"  2 tasks (0 done, 2 open)",
		"  ◻ Session ID: c706f0e8-d7a3-413e-85bf-9b74bd725e0b",
		"  ◻ Worktree mode: inplace",
	}, "\n")
	c := pane
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePromptManual, s.Poll(),
		"a selection prompt whose footer sits above a multi-line statusLine is still a prompt")
}

// Regression (review): a custom statusLine may draw its own horizontal divider — a pure-─
// separator is a common powerline/boxed statusLine idiom. Anchoring the footer match to
// "below the last rule" would re-anchor past the footer onto the statusLine's divider and
// miss the prompt: the very displacement bug the statusLine fix addresses, reintroduced by
// fancier statusLines. Detection must survive any number of rules below the footer.
func TestPollClaudeSelectionPromptAboveStatusLineDivider(t *testing.T) {
	rule := strings.Repeat("─", 80)
	for _, tc := range []struct {
		name       string
		statusLine []string
	}{
		{"divider", []string{"────────────", "  main · opus · 12% ctx"}},
		{"boxed", []string{"──────────", "  main · opus · 12% ctx", "──────────"}},
		{"tall sectioned", []string{
			"────────────",
			"  main · opus · 12% ctx",
			"  2 tasks (0 done, 2 open)",
			"────────────",
			"  ◻ Session ID: c706f0e8-d7a3-413e-85bf-9b74bd725e0b",
			"  ◻ Worktree mode: inplace",
		}},
	} {
		pane := strings.Join(append([]string{
			"  5. Type something.",
			rule,
			"  6. Chat about this",
			"",
			"Enter to select · ↑/↓ to navigate · Esc to cancel",
			"", "",
		}, tc.statusLine...), "\n")
		c := pane
		s := pollSession(t, "claude", &c, nil)
		require.Equal(t, PanePromptManual, s.Poll(),
			"a selection prompt above a %s statusLine is still a prompt", tc.name)
	}
}

// FP-safety: the footer's co-occurring tokens must appear within one rule-delimited segment.
// Hint text spread across different segments — Claude's own hint line plus an unrelated
// statusLine line below a divider — must not combine into a false footer.
func TestPollClaudeHintTokensAcrossSegmentsStayIdle(t *testing.T) {
	pane := strings.Join([]string{
		"╭────────────────────────────────────────╮",
		"│ >                                        │",
		"╰────────────────────────────────────────╯",
		"  ⏵⏵ auto mode on · ↑/↓ to navigate history · ← for agents",
		"────────────",
		"  press Esc to cancel the current task",
	}, "\n")
	c := pane
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneIdle, s.Poll(),
		"footer tokens split across rule-delimited segments must not be read as a live prompt")
}

// FP-safety: a transcript quote of the footer stays excluded even when the statusLine draws
// its own divider below the input box — the upward scan stops at the box interior and never
// reaches the quote.
func TestPollClaudeFooterQuoteAboveBoxWithDividerStatusLine(t *testing.T) {
	pane := strings.Join([]string{
		"  The selection footer looks like:",
		"  Enter to select · ↑/↓ to navigate · Esc to cancel",
		"╭────────────────────────────────────────╮",
		"│ ❯                                        │",
		"╰────────────────────────────────────────╯",
		"  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
		"────────────",
		"  main · opus · 12% ctx",
	}, "\n")
	c := pane
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneIdle, s.Poll(),
		"a quoted footer above the input box must stay idle regardless of statusLine rules")
}

// FP-safety: an idle pane whose scrolled-back transcript quotes the full footer line must
// stay idle. The quote sits above the input box, so the upward segment scan stops at the
// box interior before reaching it — where a merely-wider bottom-N window would re-admit it
// and flip the session to a spurious needs-input.
func TestPollClaudeFooterQuoteInScrollbackStaysIdle(t *testing.T) {
	rule := strings.Repeat("─", 80)
	pane := strings.Join([]string{
		"  The selection footer looks like:",
		"  Enter to select · ↑/↓ to navigate · Esc to cancel",
		"  (that is what we match on).",
		"",
		rule,
		"❯ ",
		rule,
		"  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
	}, "\n")
	c := pane
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneIdle, s.Poll(),
		"a footer quoted in the transcript above the input box must not be read as a live prompt")
}

// At a narrow pane width Claude hard-wraps its chrome, splitting a prompt's footer (and the
// permission dialog's decline option) across physical lines. Detection must survive the wrap:
// the navigate/select token and "Esc to cancel" can land on separate lines, and the decline
// sentence can break mid-phrase. The bottom-chrome confinement still holds, so a wrapped footer
// is recognized while scrolled-back prose is not.
func TestPollClaudePromptWrapTolerant(t *testing.T) {
	// Selection footer wrapped so "Esc to cancel" is on a different line than the nav/select
	// tokens — the case the old same-line check missed.
	wrappedFooter := "Server restart?\n  1. Relaunch\n❯ 2. Restart now\n" +
		"Enter to select · ↑/↓ to navigate\n· n to add notes · Esc to cancel"
	c := wrappedFooter
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePromptManual, s.Poll(), "a wrapped selection footer is still a prompt")

	// Fetch dialog whose title wraps mid-sentence across three physical lines. ABRIDGED
	// from the live width-28 capture, not a transcription of one — the body and option 2 are
	// dropped, keeping only the wrap this test is about. The verbatim pane is session/agent's
	// claudeFetchNarrowPane, and that is where the matcher is pinned; this asserts the poll
	// boundary consumes the match. The matcher keys on the title, so flattening the anchored
	// region is what reconstructs it across the wrap.
	wrappedDialog := strings.Join([]string{
		"● Fetch(https://example.org)",
		strings.Repeat("─", 28),
		" Fetch",
		" Do you want to allow",
		" Claude to fetch this",
		" content?",
		" ❯ 1. Yes",
		"   3.No, and tell Claude",
		"     what to do differently",
		"     (esc)",
	}, "\n")
	c = wrappedDialog
	s = pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePrompt, s.Poll(), "a wrapped permission dialog is still a prompt")

	// Footer wrapped across three physical lines, with a filler line between the nav/select
	// token and "Esc to cancel". This pane has no horizontal rule, so it pins the no-rule
	// fallback window (workChromeLines) at its current width: any window narrower than 3
	// would drop the nav/select token and silently misclassify the prompt.
	threeLineFooter := "Server restart?\n❯ 2. Restart now\n" +
		"Enter to select · ↑/↓ to navigate\n· n to add notes\n· Esc to cancel"
	c = threeLineFooter
	s = pollSession(t, "claude", &c, nil)
	require.Equal(t, PanePromptManual, s.Poll(), "a footer wrapped across the full footer window is still a prompt")
}

// Regression: capture-pane includes the scrolled-back transcript, so the marker strings
// can appear in the agent's own words. Detection must look only at the live chrome, and
// the selection footer must require its structural tokens — a bare "Esc to cancel"
// sentence in prose must not trigger the prompt state.
//
// The quote sits DIRECTLY above the composer, which is where an agent's own last message
// lands. It used to sit 20 filler lines up, which only ever proved that a distant quote is
// out of the bottom-N window — the easy half, and not the shape #343 reported: the flat
// window's failure was a quote INSIDE it, one an idle pane never scrolls away. The box is
// real here (rule / ❯ / rule / footer, as claude renders it) so the exclusion is structural.
func TestPollIgnoresMarkersInScrollback(t *testing.T) {
	body := "I added the \"esc to interrupt\" marker and matched the\n" +
		"\"Esc to cancel\" footer, plus the literal option text\n" +
		"\"No, and tell Claude what to do differently\".\n"
	rule := strings.Repeat("─", 60)
	idleBox := rule + "\n❯ \n" + rule + "\n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents"
	c := body + idleBox
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneIdle, s.Poll(),
		"markers and a bare \"Esc to cancel\" in the scrolled-back body must be ignored")
}

// Fix 2 (agent-team layout): the busy marker lives in the footer below the input box's
// bottom border, and the variable-height team selector (one line per teammate) renders below
// the marker — pushing it outside a fixed bottom-N window. markerWorking must anchor to the
// box border so it still finds the marker no matter how many teammates the selector lists.
func TestMarkerWorkingAnchorsBelowInputBox(t *testing.T) {
	c := ""
	s := pollSession(t, "claude", &c, nil)

	working := strings.Join([]string{
		"⏺ Running the build…",
		"╭────────────────────────────────────────╮",
		"│ >                                        │",
		"╰────────────────────────────────────────╯",
		"  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents",
		"  Running 2 agents…",
		"  ● main",
		"  ◯ general-purpose",
	}, "\n")
	require.True(t, s.markerWorking(working),
		"the footer marker is found even when a team selector renders below it")

	// Regression: the same marker text sitting in the scrolled-back transcript (above the
	// last box border) must not count — only the live footer below the border does.
	scrollback := strings.Join([]string{
		"  I will add the \"esc to interrupt\" marker check now.",
		"╭────────────────────────────────────────╮",
		"│ >                                        │",
		"╰────────────────────────────────────────╯",
		"  ⏵⏵ auto mode on (shift+tab to cycle) · ← for agents",
		"  ● main",
	}, "\n")
	require.False(t, s.markerWorking(scrollback),
		"a marker above the input box (in the transcript) is ignored")
}

// #354: the chord in claude's interrupt hint is the display text of whatever the user
// bound chat:cancel to, so a rebind used to make a working session read Ready. Driven at
// 2.1.220 — the same live pane rendered "esc to interrupt" and then "ctrl+q to interrupt"
// under a hot-reloaded keybindings.json (session/agent/registry_test.go carries both
// captures). This is the behavioral half: the table test proves HasBusyMarker matches,
// this proves the poller reaches PaneWorking through it.
func TestPollWorkingSurvivesRebindOfChatCancel(t *testing.T) {
	rule := strings.Repeat("─", 60)
	for name, footer := range map[string]string{
		"default binding":     "  ⏸ manual mode on · esc to interrupt · ← for agents",
		"rebound chat:cancel": "  ⏸ manual mode on · ctrl+q to interrupt · ← for agents",
	} {
		c := "⏺ Writing the poem…\n" + rule + "\n❯ \n" + rule + "\n" + footer
		s := pollSession(t, "claude", &c, nil)
		require.Equal(t, PaneWorking, s.Poll(), "%s: a busy pane must not read idle", name)
	}
}

// Codex renders its status row ("Working (12s • esc to interrupt)") *above* the
// composer, outside claude's below-the-box footer anchor; the adapter's bottom-window
// confinement must still find it, hold across counter ticks, and read its approval
// overlay as a prompt.
func TestPollCodex(t *testing.T) {
	working := "• Fixing the failing test.\n\n▌ Working (12s • esc to interrupt)\n\n› \n\n  ? for shortcuts"
	c := working
	s := pollSession(t, "codex", &c, nil)
	require.Equal(t, PaneWorking, s.Poll())
	c = "• Fixing the failing test.\n\n▌ Working (13s • esc to interrupt)\n\n› \n\n  ? for shortcuts"
	require.Equal(t, PaneWorking, s.Poll(), "counter ticking does not flip the state")

	c = "Would you like to run the following command?\n\n  rm -rf build/\n\n" +
		"› 1. Yes, proceed\n  3. No, and tell Codex what to do differently"
	require.Equal(t, PanePromptManual, s.Poll(),
		"an approval overlay is a needs-input state — manual, since the matcher reads a flat "+
			"window and Enter here approves a shell command (#347)")

	c = "• Done. The tests pass.\n\n› \n\n  ? for shortcuts"
	require.Equal(t, PaneIdle, s.Poll(), "marker gone after a prompt commits idle at face value")
}

// Gemini's loading row ("(esc to cancel, 12s)") also renders above its input box; it is
// now a marker-bearing program, and its tool confirmation must classify as a prompt on
// the current upstream strings (the pre-adapter "Yes, allow once" no longer exists).
func TestPollGemini(t *testing.T) {
	working := "✦ Refactoring the parser.\n\n⠏ Thinking... (esc to cancel, 12s)\n\n" +
		"╭───╮\n│ > │\n╰───╯\n~/project   no sandbox   gemini-2.5-pro"
	c := working
	s := pollSession(t, "gemini", &c, nil)
	require.Equal(t, PaneWorking, s.Poll())

	// Abridged from the verbatim width-40 capture in session/agent's
	// gemini_confirm_pane_test.go — the same arrangement wrappedDialog above uses, since the
	// full pane belongs with the ladder that measures it. It has to be the real SHAPE and not
	// the four bare lines this used to be: since #736 the matcher anchors on a box whose
	// bottom border ends the pane, so an unboxed transcript of the option rows is exactly what
	// must NOT classify as a prompt.
	c = "✦ I will run a command to delete the README.md file.\n\n" +
		"╭──────────────────────────────────────╮\n" +
		"│ ? Shell  rm -f README.md             │\n" +
		"│ Allow execution of [Shell]?          │\n" +
		"│                                      │\n" +
		"│ ● 1. Allow once                      │\n" +
		"│   2. Allow for this session          │\n" +
		"│   3. No, suggest changes (esc)       │\n" +
		"╰──────────────────────────────────────╯"
	require.Equal(t, PanePromptManual, s.Poll(),
		"a tool confirmation is a needs-input state — manual, because Enter here runs the "+
			"shell command and the highlighted default is \"Allow once\" (#347)")

	// The same rows with no box around them: a transcript quoting the dialog, which the flat
	// matcher read as live until #736.
	c = "Apply this change?\n  1. Allow once\n  2. Allow always\n  3. No, suggest changes (esc)"
	require.Equal(t, PaneIdle, s.Poll(),
		"unboxed option rows are a quote, not a live dialog (#736)")

	// PollNow (the post-detach face-value refresh): gemini is marker-bearing, so —
	// unlike aider's PaneUnknown — an absent marker with no hook file reads as idle,
	// and a present marker as working.
	c = "✦ Done.\n\n╭───╮\n│ > │\n╰───╯\n~/project   no sandbox   gemini-2.5-pro"
	require.Equal(t, PaneIdle, s.PollNow(), "no marker at face value is idle")
	c = working
	require.Equal(t, PaneWorking, s.PollNow(), "a live marker at face value is working")
}

// agy's panes, driven end to end through Poll. Before #512 the adapter carried no
// heuristics at all, so a pane parked on a trust gate or a tool confirmation is exactly
// the case that used to fall through to the content-change fallback and settle PaneIdle —
// the row reading Ready while the agent was blocked. The fixtures live in
// session/agent/registry_test.go (a live agy 1.1.11, 2026-08-09); these are the same
// shapes, kept short here because Poll consumes only the match result.
func TestPollAgy(t *testing.T) {
	working := "> do the thing\n⣻  Generating...\n────\n>\n────\n" +
		"esc to cancel                          Gemini 3.1 Pro · high"
	c := working
	s := pollSession(t, "agy", &c, nil)
	require.Equal(t, PaneWorking, s.Poll())

	// The verb rotates within a single turn; the footer marker is what holds the state.
	c = "> do the thing\n⣽  Running...\n────\n>\n────\n" +
		"esc to cancel                          Gemini 3.1 Pro · high"
	require.Equal(t, PaneWorking, s.Poll(), "the spinner's verb changing does not flip the state")

	c = "Do you want to proceed?\n> 1. Yes\n  4. No\n\n" +
		"  ↑/↓ Navigate · tab Amend · ctrl+g edit/expand command\nesc to cancel"
	require.Equal(t, PanePromptManual, s.Poll(),
		"a tool confirmation is a needs-input state — manual, because the matcher reads a "+
			"flat window and Enter here approves a shell command (#347)")

	gate := "Do you trust the contents of this project?\n\n> Yes, I trust this folder\n" +
		"  No, exit\n\n  ↑/↓ Navigate · enter Confirm"
	c = gate
	require.Equal(t, PaneGate, s.Poll(), "the startup trust screen is a gate, not a prompt")

	c = "────\n>\n────\n? for shortcuts                          Gemini 3.1 Pro · high"
	require.Equal(t, PaneIdle, s.Poll(), "marker gone after a prompt commits idle at face value")

	// PollNow (the post-detach face-value refresh): agy is marker-bearing now, so — unlike
	// aider's PaneUnknown — an absent marker reads as idle and a present one as working.
	require.Equal(t, PaneIdle, s.PollNow(), "no marker at face value is idle")
	c = working
	require.Equal(t, PaneWorking, s.PollNow(), "a live marker at face value is working")
}

// A live aider confirm pane classifies PanePrompt — auto-tappable, NOT manual:
// aider's confirms are permission-type prompts autoyes should answer, unlike
// claude's judgment selections (#271). Aider has no busy marker, so without the
// prompt match the quiet pane would commit PaneIdle via the content-change
// fallback — a blocked session showing Ready. One representative pane (captured
// from a live aider 0.86.2 in tmux, 2026-07-04) proves the wiring: Poll consumes
// only the match result, so the full confirm_ask shape catalog stays pinned at
// the agent layer (agent.TestAiderConfirmShapes), like the codex/gemini poll
// tests above.
func TestPollAiderConfirmPrompt(t *testing.T) {
	pane := strings.Join([]string{
		"> please look at qux.py",
		"",
		"qux.py",
		"Add file to the chat? (Y)es/(N)o/(D)on't ask again [Yes]:",
	}, "\n")
	c := pane
	s := pollSession(t, "aider", &c, nil)
	require.Equal(t, PanePrompt, s.Poll(), "an aider confirm is an auto-tappable prompt")
}

// Hysteresis (content-change fallback, e.g. aider): a content change reads as working;
// once the pane goes quiet the indicator is held until it has been unchanged for
// idleSettleTicks, then commits idle. This path is only for programs without a busy marker;
// the marker-driven Claude path is covered by the hook/marker tests.
func TestPollHysteresis(t *testing.T) {
	busy := "running… building target"
	idle := "$ done"
	c := busy
	s := pollSession(t, "aider", &c, nil)

	require.Equal(t, PaneWorking, s.Poll(), "first content read → working")
	// The content changing to idle is itself a change (working), then the pane must stay
	// quiet for idleSettleTicks observations before idle commits.
	c = idle
	for i := 0; i < idleSettleTicks; i++ {
		require.Equal(t, PaneWorking, s.Poll(), "held while the pane settles (observation %d)", i)
	}
	require.Equal(t, PaneIdle, s.Poll(), "commits once the pane has been quiet for idleSettleTicks")
	c = busy
	require.Equal(t, PaneWorking, s.Poll(), "a content change resets to working")
}

// Regression for the status oscillation: at an auto-accept turn boundary Claude briefly
// drops the "esc to interrupt" marker (model spin-up) while the pane keeps repainting
// (spinner elapsed ticking, output rendering). A *moving* pane must never settle to idle —
// it holds "working" until the marker returns, so there is no Ready→Running flicker.
func TestPollBridgesAutoAcceptTurnBoundary(t *testing.T) {
	working := "⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ctrl+t to hide tasks"
	// Same footer minus the marker, plus a spinner whose elapsed counter advances each tick.
	gap := func(i int) string {
		return fmt.Sprintf("✻ Cogitating… (%ds)\n⏵⏵ auto mode on (shift+tab to cycle) · ← for agents", i)
	}
	c := working
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneWorking, s.Poll())

	// A churning gap (well past idleSettleTicks) is held the whole time, then the marker
	// returns: the indicator never flipped to idle.
	for i := 1; i < idleConfirmTicks; i++ {
		c = gap(i)
		require.Equal(t, PaneWorking, s.Poll(), "churning turn-boundary gap held (observation %d)", i)
	}
	c = working
	require.Equal(t, PaneWorking, s.Poll(), "marker returning resumes working without a blip")
}

// Safety cap: if the marker stays absent while the pane keeps changing (an agent UI we
// don't model, or a missed marker), the idleConfirmTicks cap eventually commits idle rather
// than holding "working" forever.
func TestPollChurnHitsSafetyCap(t *testing.T) {
	c := "esc to interrupt"
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, PaneWorking, s.Poll())

	for i := 1; i < idleConfirmTicks; i++ {
		c = fmt.Sprintf("repainting %d, no marker", i) // changes every tick → never settles
		require.Equal(t, PaneWorking, s.Poll(), "held under churn before the cap (observation %d)", i)
	}
	c = "repainting final, no marker"
	require.Equal(t, PaneIdle, s.Poll(), "commits at the idleConfirmTicks safety cap")
}

// idleConfirmCap returns the adapter override when set (> 0), else the package default.
func TestIdleConfirmCap(t *testing.T) {
	c := "x"
	s := pollSession(t, "claude", &c, nil)
	require.Equal(t, idleConfirmTicks, s.idleConfirmCap(), "claude sets no override → package default")

	s.adapter = &agent.Adapter{IdleConfirmTicks: 3}
	require.Equal(t, 3, s.idleConfirmCap(), "a positive adapter override is honored")

	s.adapter = nil
	require.Equal(t, idleConfirmTicks, s.idleConfirmCap(), "nil adapter falls back to the default")
}

// An adapter that raises IdleConfirmTicks holds working past the package default, and a
// lowered one commits idle earlier — proving Poll reads the per-adapter cap, not the const.
func TestPollHonorsAdapterIdleConfirmCap(t *testing.T) {
	c := "esc to interrupt"
	s := pollSession(t, "claude", &c, nil)
	// Clone the resolved adapter by value and change only the cap. Mutating the shared
	// registry adapter in place would leak the lowered cap into the default-cap tests.
	ad := *s.adapter
	ad.IdleConfirmTicks = 3
	s.adapter = &ad

	require.Equal(t, PaneWorking, s.Poll())
	// idleStreak climbs 1,2 under churn (held), then 3 trips the lowered cap — well before
	// the default idleConfirmTicks (6) would have.
	for i := 1; i < 3; i++ {
		c = fmt.Sprintf("repainting %d, no marker", i)
		require.Equal(t, PaneWorking, s.Poll(), "held under churn before the per-adapter cap (observation %d)", i)
	}
	c = "repainting final, no marker"
	require.Equal(t, PaneIdle, s.Poll(), "commits at the per-adapter cap (3), earlier than the default 6")
}

// PollNow classifies at face value with no hysteresis — for the one-shot refresh on detach,
// where the stalled stream left the smoothing state stale. An idle pane reads idle at once
// even though the monitor last reported working; a marker pane reads working; a markerless
// program can't be classified from a single snapshot and yields PaneUnknown.
func TestPollNow(t *testing.T) {
	idle := "│ > │  ? for shortcuts"
	c := "working… esc to interrupt"
	s := pollSession(t, "claude", &c, nil)

	// Leave the monitor mid working→idle hold, the way a stalled stream would.
	require.Equal(t, PaneWorking, s.Poll())
	c = idle
	require.Equal(t, PaneWorking, s.Poll(), "normal Poll holds working via hysteresis")

	require.Equal(t, PaneIdle, s.PollNow(), "PollNow ignores the hold and commits idle at once")

	c = "thinking… esc to interrupt"
	require.Equal(t, PaneWorking, s.PollNow(), "a marker pane is working")

	// A markerless program has no level signal, so a single snapshot is inconclusive.
	c = "some output"
	a := pollSession(t, "aider", &c, nil)
	require.Equal(t, PaneUnknown, a.PollNow(), "no marker → unknown, left to the tick loop")
}

// Poll is driven by the metadata tick and, off-cadence, by the UI when the selection
// changes or a session is detached. monitorMu must make concurrent calls on one session
// race-free; run under -race to exercise it.
func TestPollConcurrentIsRaceFree(t *testing.T) {
	c := "✻ Cogitating… (5s · esc to interrupt)"
	s := pollSession(t, "claude", &c, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.Poll()
			}
		}()
	}
	wg.Wait()
}

// Programs without a known marker use content-change detection on ANSI-stripped text, so
// color/cursor churn does not register as working.
func TestPollFallbackNormalization(t *testing.T) {
	c := "\x1b[32mthinking\x1b[0m"
	s := pollSession(t, "aider", &c, nil)

	require.Equal(t, PaneWorking, s.Poll(), "first observation is treated as active")
	// Same visible text, different ANSI only → not a change, so the pane reads as quiet and
	// settles to idle after idleSettleTicks. Vary the ANSI each tick to prove a raw byte
	// comparison would (wrongly) see motion.
	for i := 1; i < idleSettleTicks; i++ {
		c = fmt.Sprintf("\x1b[%dmthinking\x1b[0m", 31+i)
		require.Equal(t, PaneWorking, s.Poll(), "ANSI-only churn held (observation %d)", i)
	}
	c = fmt.Sprintf("\x1b[%dmthinking\x1b[0m", 31+idleSettleTicks)
	require.Equal(t, PaneIdle, s.Poll(), "ANSI-only churn settles to idle")
	// A real text change flips back to working.
	c = "\x1b[31mthinking more\x1b[0m"
	require.Equal(t, PaneWorking, s.Poll())
}

func TestPollCaptureErrorIsUnknown(t *testing.T) {
	// Poll logs on capture error; ErrorLog is otherwise nil in tests.
	t.Cleanup(log.Initialize(t.TempDir(), false))
	c := "anything"
	fail := false
	s := pollSession(t, "claude", &c, &fail)
	require.Equal(t, PaneIdle, s.Poll())
	fail = true
	require.Equal(t, PaneUnknown, s.Poll(), "capture failure yields PaneUnknown")
}

func TestHasUpdatedShim(t *testing.T) {
	busy := "esc to interrupt"
	c := busy
	s := pollSession(t, "claude", &c, nil)
	u, p := s.HasUpdated()
	require.True(t, u)
	require.False(t, p)

	c = claudeFetchPane
	u, p = s.HasUpdated()
	require.False(t, u)
	require.True(t, p)
}

func TestTmuxCommandInjectsIsolationFlags(t *testing.T) {
	cmd := tmuxCommand(context.Background(), "has-session", "-t=foo")
	// Args[0] is "tmux"; the socket flag must immediately follow and precede the
	// subcommand (tmux requires -L/-f before the command).
	require.Equal(t, "tmux", cmd.Args[0])
	require.Equal(t, "-L", cmd.Args[1])
	require.Equal(t, socketName(), cmd.Args[2])
	// The subcommand and its args must still be present and last.
	require.Contains(t, cmd.Args, "has-session")
	require.Equal(t, "-t=foo", cmd.Args[len(cmd.Args)-1])
}

func TestStartSession(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)

	created := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") && !created {
				created = true
				return fmt.Errorf("session already exists")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte("output"), nil
		},
	}

	workdir := t.TempDir()
	session := NewSessionWithDeps(context.Background(), "test-session", "claude", ptyFactory, cmdExec)

	err := session.Start(workdir)
	require.NoError(t, err)
	require.Equal(t, 2, len(ptyFactory.cmds))

	// Atrium runs on a dedicated socket with a bundled config, so every command is
	// prefixed with `-L <socket> -f <conf>`. The conf path is absolute and
	// machine-dependent, and the socket/prefix follow the active brand, so assert
	// the load-bearing parts via the same helpers rather than a literal string.
	newSession := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSession, "-L "+socketName())
	require.Contains(t, newSession, "new-session -d -s "+Prefix()+"test-session")
	require.Contains(t, newSession, "-c "+workdir)
	require.Contains(t, newSession, "-n test-session")
	require.Contains(t, newSession, "claude")

	attach := cmd2.ToString(ptyFactory.cmds[1])
	require.Contains(t, attach, "-L "+socketName())
	require.Contains(t, attach, "attach-session -t "+Prefix()+"test-session")

	require.Equal(t, 2, len(ptyFactory.files))

	// File should be closed.
	_, err = ptyFactory.files[0].Stat()
	require.Error(t, err)
	// File should be open
	_, err = ptyFactory.files[1].Stat()
	require.NoError(t, err)
}

// Regression: a session title with a shell metacharacter (e.g. "Surya's comment") flows
// into the hook settings path, which is appended to the launch command — a string tmux
// hands to `sh -c`. Unquoted, the apostrophe opened an unterminated quote, the window's
// shell died instantly, and the start poll timed out ("timed out waiting for tmux
// session claudesquad_Surya'scomment"). The path must be single-quoted so the launch
// command stays valid shell for any session name.
func TestStartQuotesHookSettingsPath(t *testing.T) {
	forceSettingsFlag(t, true)
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "Surya's comment", "claude", ptyFactory, startMockExec())

	require.NoError(t, session.Start(t.TempDir()))

	// The launch command is the final argument of the new-session invocation; tmux runs
	// it via the shell, so it must parse cleanly (sh -n parses without executing).
	launchArgs := ptyFactory.cmds[0].Args
	program := launchArgs[len(launchArgs)-1]
	require.Contains(t, program, "--settings")
	parseOnly := exec.CommandContext(context.Background(), "sh", "-n", "-c", program)
	require.NoError(t, parseOnly.Run(), "launch command must be valid shell syntax: %q", program)

	// The settings path (which embeds the apostrophe-bearing session name) is quoted.
	dir, err := hookSessionDir(session.sanitizedName)
	require.NoError(t, err)
	settingsPath := filepath.Join(dir, "settings.json")
	require.Contains(t, program, " --settings "+shellSingleQuote(settingsPath))
}

// Regression: the start poll's timeout branch wrapped a nil error with %w, rendering as
// the unreadable "timed out waiting for tmux session X: %!w(<nil>) (cleanup error: …)".
// The timeout must produce a clean message whether or not the cleanup also fails.
func TestStartTimeoutErrorOmitsNilWrap(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	// has-session never succeeds (the session died at launch) and kill-session fails too
	// (nothing to kill) — the exact shape of the real failure.
	cmdExec := cmd_test.MockCmdExec{
		RunFunc:    func(cmd *exec.Cmd) error { return fmt.Errorf("no such session") },
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}
	session := NewSessionWithDeps(context.Background(), "timeout-test", "prog", ptyFactory, cmdExec)

	err := session.Start(t.TempDir())
	require.Error(t, err)
	require.Contains(t, err.Error(), "timed out waiting for tmux session")
	require.Contains(t, err.Error(), "cleanup error", "the failed kill is still reported")
	require.NotContains(t, err.Error(), "%!w", "a nil error must never be wrapped")
}

// forceHelpProbe installs canned --help outputs so the capability probes never exec a
// real binary in tests. The override is set and cleared under helpProbeMu, since
// binHelpContains reads it under the same lock from production goroutines.
func forceHelpProbe(t *testing.T, outputs map[string]string) {
	t.Helper()
	helpProbeMu.Lock()
	helpProbeOverride = outputs
	helpProbeMu.Unlock()
	t.Cleanup(func() {
		helpProbeMu.Lock()
		helpProbeOverride = nil
		helpProbeMu.Unlock()
	})
}

func TestResumeCommand(t *testing.T) {
	forceHelpProbe(t, map[string]string{
		"gemini": "-r, --resume   Resume a previous session",
		"codex":  "Commands:\n  resume  Resume a previous interactive session",
		"agy":    "  -c                Short alias for --continue\n  --continue        Continue the most recent conversation",
		// The canonical binary at an absolute path is probed at that path (it may
		// not be on PATH at all); keyed by path so a bare-name probe would miss.
		"/opt/agents/gemini": "-r, --resume   Resume a previous session",
	})

	cases := []struct {
		name    string
		program string
		want    string
	}{
		{"bare claude gets --continue", "claude", "claude --continue"},
		{"absolute claude path gets --continue", "/usr/local/bin/claude", "/usr/local/bin/claude --continue"},
		{"aider unchanged", "aider --model x", "aider --model x"},
		// Resume parity: gemini and codex now relaunch into their prior conversation.
		{"gemini gets --resume latest", "gemini", "gemini --resume latest"},
		{"codex gets resume --last", "codex", "codex resume --last"},
		// The codex subcommand cannot be spliced into an argv with flags; relaunch blank.
		{"codex with flags unchanged", "codex --model o3", "codex --model o3"},
		// agy appends unconditionally, so a flag-bearing program still resumes.
		{"agy gets --continue", "agy", "agy --continue"},
		{"agy with flags gets --continue", "agy --model x", "agy --model x --continue"},
		// An off-PATH absolute install still resumes: the probe targets the
		// program's own path because its basename is the canonical binary.
		{"absolute gemini path probes itself", "/opt/agents/gemini", "/opt/agents/gemini --resume latest"},
		// Detection is on the binary basename containing "claude", so a launcher wrapper that
		// exec's claude (the default_program many setups use) and a flag-bearing claude are
		// both recognized — the wrapper forwards the appended flag through to claude.
		{"claude launcher wrapper gets --continue", "/home/u/.claude-squad/launch-claude.sh", "/home/u/.claude-squad/launch-claude.sh --continue"},
		{"claude-wrapper gets --continue", "claude-wrapper", "claude-wrapper --continue"},
		{"claude with trailing flags gets --continue", "claude --model opus", "claude --model opus --continue"},
		// A non-claude binary under a claude-containing directory is NOT matched (basename wins).
		{"non-claude binary in claude dir unchanged", "/home/u/.claude-squad/bin/aider", "/home/u/.claude-squad/bin/aider"},
		{"unknown agent unchanged", "someagent", "someagent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSessionWithDeps(context.Background(), "resume-test", tc.program, NewMockPtyFactory(t), cmd_test.MockCmdExec{})
			require.Equal(t, tc.want, s.resumeCommand())
		})
	}
}

// An installed binary that predates its resume flag (probe finds no support) must
// relaunch blank rather than fail on an unknown flag. Codex's needle additionally pins
// the subcommand *listing* — help text that merely mentions resuming must not pass.
func TestResumeCommandProbeGate(t *testing.T) {
	forceHelpProbe(t, map[string]string{
		"gemini": "old gemini help with no such flag",
		"codex":  "old codex; sessions resume automatically on restart",
		"agy":    "old agy help; nothing to continue with",
	})

	for _, program := range []string{"gemini", "codex", "agy"} {
		s := NewSessionWithDeps(context.Background(), "resume-test", program, NewMockPtyFactory(t), cmd_test.MockCmdExec{})
		require.Equal(t, program, s.resumeCommand(), "probe must fail closed for %s", program)
	}
}

// probeTarget picks which binary's --help the resume probe runs: the program's own first
// token when it is the canonical binary (wherever it lives), the canonical name otherwise —
// a wrapper's side effects must never run on a probe.
func TestProbeTarget(t *testing.T) {
	cases := []struct {
		program string
		key     agent.Key
		want    string
	}{
		{"gemini", agent.KeyGemini, "gemini"},
		{"/opt/agents/gemini", agent.KeyGemini, "/opt/agents/gemini"},
		{"/opt/agents/gemini --yolo", agent.KeyGemini, "/opt/agents/gemini"},
		{"launch-gemini.sh", agent.KeyGemini, "gemini"},
		{"/usr/local/bin/codex", agent.KeyCodex, "/usr/local/bin/codex"},
		{"codex-nightly", agent.KeyCodex, "codex"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, probeTarget(tc.program, tc.key), "program %q", tc.program)
	}
}

// startMockExec mirrors TestStartSession's executor: the first has-session check
// reports "not found" so start's entry guard passes, and every later check succeeds so
// the poll loop sees the session and breaks.
func startMockExec() cmd_test.MockCmdExec {
	created := false
	return cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") && !created {
				created = true
				return fmt.Errorf("session already exists")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte("output"), nil
		},
	}
}

func TestStartContinueAppendsContinueForClaude(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "cont-test", "claude", ptyFactory, startMockExec())

	resuming, err := session.StartContinue(t.TempDir())
	require.NoError(t, err)
	require.True(t, resuming, "a launch that appended --continue must report that it resumed")

	// cmds[0] is the new-session launch; cmds[1] is the trailing attach from Restore.
	newSession := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSession, "claude --continue")
	// The session name is keyed off the session, not the program, so it is unchanged.
	require.Contains(t, newSession, "new-session -d -s "+Prefix()+"cont-test")
}

func TestStartContinueLeavesNonClaudeUnchanged(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "cont-test", "aider --model x", ptyFactory, startMockExec())

	resuming, err := session.StartContinue(t.TempDir())
	require.NoError(t, err)
	require.False(t, resuming,
		"an agent with no resume support launched the plain program, and a caller repairing that launch must not retry the same command")

	newSession := cmd2.ToString(ptyFactory.cmds[0])
	require.NotContains(t, newSession, "--continue")
	require.Contains(t, newSession, "aider --model x")
}

// Gone answers a different question from DoesSessionExist, and this is the case where
// they must disagree: a socket tmux cannot open for a reason that is not its absence.
// Nothing was asked of any server, so a session may well be alive behind it — which is
// why the relaunch-over-it caller (Instance.RepairResumingLaunch) gates on this one.
// Reading the pair the other way round would relaunch an agent over a live one.
func TestGoneIsNotTheNegationOfDoesSessionExist(t *testing.T) {
	sealed := cmd_test.MockCmdExec{
		RunFunc: func(*exec.Cmd) error {
			return fmt.Errorf("error connecting to /tmp/sock (Permission denied)")
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	session := NewSessionWithDeps(context.Background(), "sealed", "claude", NewMockPtyFactory(t), sealed)

	require.False(t, session.DoesSessionExist(), "an inconclusive probe is not proof of life")
	require.False(t, session.Gone(), "nor is it proof of death, which is the whole difference")
}

// And the case where they must agree: tmux answered, and the answer was no such session.
func TestGoneIsTrueWhenTmuxSaysTheSessionIsNotThere(t *testing.T) {
	dead := cmd_test.MockCmdExec{
		RunFunc:    func(*exec.Cmd) error { return fmt.Errorf("can't find session: sealed") },
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	session := NewSessionWithDeps(context.Background(), "sealed", "claude", NewMockPtyFactory(t), dead)

	require.True(t, session.Gone())
	require.False(t, session.DoesSessionExist())
}

// Gone is narrower than `liveness() == sessionGone`, and this is the whole of that gap:
// the poll parks on all three of sessionGone's producers, while Gone accepts only the one
// a server answered.
//
// Driven from the split halves of the same table close_test.go holds the kill path to, so
// a message added to sessionAlreadyGone cannot land on the wrong side unnoticed. Both
// directions are asserted because they fail differently. A confirmed message Gone refuses
// makes the repair decline forever — a lost feature, and a silent one. A socket-missing
// message Gone accepts is the DOUBLE AGENT: unlink a live server's socket and every
// command aimed at that path reports ENOENT, so the kill is forgiven, the existence check
// agrees, and `new-session` builds a second server at the same path with a second agent in
// the same worktree while the first keeps running.
//
// PaneDead is asserted alongside so the two verdicts are pinned as DIFFERENT rather than
// as one predicate read twice: every row here is a death for the poll, and only some are
// a death Gone will act on.
func TestGoneAcceptsOnlyAServersOwnAnswer(t *testing.T) {
	goneCase := func(msg string, wantGone bool) {
		t.Run(msg, func(t *testing.T) {
			cmdExec := cmd_test.MockCmdExec{
				RunFunc: func(cmd *exec.Cmd) error {
					// Real tmux puts the diagnostic on stderr and exits non-zero; the
					// error-string fallback a fake would otherwise hit proves nothing
					// about production.
					if cmd.Stderr != nil {
						_, _ = fmt.Fprintln(cmd.Stderr, msg)
					}
					return fmt.Errorf("exit status 1")
				},
				OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
			}
			s := NewSessionWithDeps(context.Background(), "dead", "claude", NewMockPtyFactory(t), cmdExec)

			require.Equal(t, wantGone, s.Gone())
			require.Equal(t, PaneDead, s.Poll(), "every row here is still a death for the poll")
		})
	}
	for _, msg := range confirmedGoneMessages {
		goneCase(msg, true)
	}
	for _, msg := range socketMissingGoneMessages {
		goneCase(msg, false)
	}
}

// The other producer Gone must refuse: liveness's trailing fallthrough, which reads every
// diagnostic this package has not been taught as a death (#734). `unknown command:
// has-session` is the concrete one — a tmux below the version floor — and it is a server
// that is up, answering, and quite possibly running the session.
func TestGoneRefusesAnUnrecognizedTmuxFailure(t *testing.T) {
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if cmd.Stderr != nil {
				_, _ = fmt.Fprintln(cmd.Stderr, "unknown command: has-session")
			}
			// A real *exec.ExitError, because that is what the fallthrough keys on:
			// an error carrying only the text would fall to the already-indeterminate
			// default and prove nothing about this branch.
			return &exec.ExitError{}
		},
		OutputFunc: func(*exec.Cmd) ([]byte, error) { return nil, nil },
	}
	s := NewSessionWithDeps(context.Background(), "dead", "claude", NewMockPtyFactory(t), cmdExec)

	require.False(t, s.Gone(),
		"an unrecognized failure is not a server saying the session is absent, and relaunching over one risks a second agent")
	require.Equal(t, PaneDead, s.Poll(), "the poll still parks on it, which is the gap this pair pins")
}

// Plain Start must never append --continue, even for claude — that is the first-time and
// PTY-reattach path, where there is nothing to continue.
func TestStartDoesNotAppendContinue(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "cont-test", "claude", ptyFactory, startMockExec())

	require.NoError(t, session.Start(t.TempDir()))
	require.NotContains(t, cmd2.ToString(ptyFactory.cmds[0]), "--continue")
}

func TestStartSessionInjectsClaudeConfigDir(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	created := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") && !created {
				created = true
				return fmt.Errorf("session already exists")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}

	session := NewSessionWithDeps(context.Background(), "acct-session", "claude", ptyFactory, cmdExec)
	session.SetClaudeConfigDir("/home/tester/.claude-quantivly")
	require.NoError(t, session.Start(t.TempDir()))

	newSessionCmd := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSessionCmd, "-e CLAUDE_CONFIG_DIR=/home/tester/.claude-quantivly")
	// The -e flag must precede the program word.
	require.Less(t, strings.Index(newSessionCmd, "CLAUDE_CONFIG_DIR"),
		strings.LastIndex(newSessionCmd, "claude"))
}

func TestStartSessionNoConfigDirNoEnvFlag(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	created := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") && !created {
				created = true
				return fmt.Errorf("session already exists")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}

	session := NewSessionWithDeps(context.Background(), "plain-session", "claude", ptyFactory, cmdExec)
	require.NoError(t, session.Start(t.TempDir()))

	require.NotContains(t, cmd2.ToString(ptyFactory.cmds[0]), "CLAUDE_CONFIG_DIR")
}

// TestStartSessionConfigDirReachesPane drives a real tmux server on Atrium's
// dedicated socket and asserts the injected CLAUDE_CONFIG_DIR is actually present
// in the session environment — the end-to-end proxy for the acceptance criterion
// (`tmux show-environment` shows the var). Self-skips when tmux is unavailable.
func TestStartSessionConfigDirReachesPane(t *testing.T) {
	testutil.RequireTmux(t)

	name := fmt.Sprintf("acctenv-%d", rand.Int31())
	dir := t.TempDir()
	session := NewSession(context.Background(), name, "sleep 300")
	session.SetClaudeConfigDir(dir)
	require.NoError(t, session.Start(t.TempDir()))
	t.Cleanup(func() { _ = session.Close() })

	out, err := tmuxCommand(context.Background(), "show-environment", "-t", session.sanitizedName).Output()
	require.NoError(t, err)
	require.Contains(t, string(out), "CLAUDE_CONFIG_DIR="+dir)
}

func TestStartSessionInjectsGHConfigDir(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	created := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") && !created {
				created = true
				return fmt.Errorf("session already exists")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}

	session := NewSessionWithDeps(context.Background(), "gh-session", "claude", ptyFactory, cmdExec)
	session.SetGHConfigDir("/home/tester/.config/gh-quantivly")
	require.NoError(t, session.Start(t.TempDir()))

	newSessionCmd := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSessionCmd, "-e GH_CONFIG_DIR=/home/tester/.config/gh-quantivly")
	// The -e flag must precede the program word.
	require.Less(t, strings.Index(newSessionCmd, "GH_CONFIG_DIR"),
		strings.LastIndex(newSessionCmd, "claude"))
}

func TestStartSessionNoGHConfigDirNoEnvFlag(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	created := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") && !created {
				created = true
				return fmt.Errorf("session already exists")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}

	session := NewSessionWithDeps(context.Background(), "plain-gh-session", "claude", ptyFactory, cmdExec)
	require.NoError(t, session.Start(t.TempDir()))

	require.NotContains(t, cmd2.ToString(ptyFactory.cmds[0]), "GH_CONFIG_DIR")
}

// TestStartSessionInjectsBothConfigDirs asserts CLAUDE_CONFIG_DIR and
// GH_CONFIG_DIR coexist as independent -e flags, both ahead of the program word.
func TestStartSessionInjectsBothConfigDirs(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	created := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") && !created {
				created = true
				return fmt.Errorf("session already exists")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) { return []byte("output"), nil },
	}

	session := NewSessionWithDeps(context.Background(), "both-session", "claude", ptyFactory, cmdExec)
	session.SetClaudeConfigDir("/home/tester/.claude-quantivly")
	session.SetGHConfigDir("/home/tester/.config/gh-quantivly")
	require.NoError(t, session.Start(t.TempDir()))

	newSessionCmd := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSessionCmd, "-e CLAUDE_CONFIG_DIR=/home/tester/.claude-quantivly")
	require.Contains(t, newSessionCmd, "-e GH_CONFIG_DIR=/home/tester/.config/gh-quantivly")
	programIdx := strings.LastIndex(newSessionCmd, "claude")
	require.Less(t, strings.Index(newSessionCmd, "CLAUDE_CONFIG_DIR"), programIdx)
	require.Less(t, strings.Index(newSessionCmd, "GH_CONFIG_DIR"), programIdx)
}

// The ATRIUM marker is injected into every session (no setter, no config) so
// external shell hooks can detect an Atrium session and defer to the injected env.
func TestStartSessionInjectsAtriumMarker(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "marker-session", "claude", ptyFactory, startMockExec())
	require.NoError(t, session.Start(t.TempDir()))

	newSessionCmd := cmd2.ToString(ptyFactory.cmds[0])
	require.Contains(t, newSessionCmd, "-e ATRIUM=1")
	require.Contains(t, newSessionCmd, "-e ATRIUM_SESSION="+session.sanitizedName)
	// Marker env must precede the program word.
	require.Less(t, strings.Index(newSessionCmd, "ATRIUM=1"),
		strings.LastIndex(newSessionCmd, "claude"))
}

// stubGitHubToken swaps the package-level gh-token resolver for a test double and
// restores it after the test, keeping token tests off the real gh/keyring (the
// same stubbable-package-var convention as session/git's checkGHCLI).
func stubGitHubToken(t *testing.T, fn func(ctx context.Context, ghConfigDir string) (string, error)) {
	t.Helper()
	orig := resolveGitHubToken
	resolveGitHubToken = fn
	t.Cleanup(func() { resolveGitHubToken = orig })
}

// rawArgv joins a command's argv without cmd.ToString's redaction, for the two
// tests that assert an injected token's VALUE. ToString scrubs secret-bearing
// NAME=VALUE tokens because it feeds logs and error messages, which is exactly
// what those assertions need to see through. Every other test here inspects
// non-secret content and keeps using ToString.
func rawArgv(c *exec.Cmd) string { return strings.Join(c.Args, " ") }

func TestStartSessionInjectsGitHubToken(t *testing.T) {
	stubGitHubToken(t, func(_ context.Context, ghConfigDir string) (string, error) {
		require.Equal(t, "/home/tester/.config/gh-quantivly", ghConfigDir)
		return "gho_testtoken", nil
	})
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "tok-session", "claude", ptyFactory, startMockExec())
	session.SetGHConfigDir("/home/tester/.config/gh-quantivly")
	session.SetGitHubTokenEnv([]string{"GITHUB_PERSONAL_ACCESS_TOKEN"})
	require.NoError(t, session.Start(t.TempDir()))

	newSessionCmd := rawArgv(ptyFactory.cmds[0])
	require.Contains(t, newSessionCmd, "-e GITHUB_PERSONAL_ACCESS_TOKEN=gho_testtoken")
	// The token -e flag must precede the program word.
	require.Less(t, strings.Index(newSessionCmd, "GITHUB_PERSONAL_ACCESS_TOKEN"),
		strings.LastIndex(newSessionCmd, "claude"))
}

// One resolved token, injected under every configured name.
func TestStartSessionMultipleTokenEnv(t *testing.T) {
	stubGitHubToken(t, func(_ context.Context, _ string) (string, error) { return "tok123", nil })
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "multi-tok", "claude", ptyFactory, startMockExec())
	session.SetGitHubTokenEnv([]string{"GITHUB_PERSONAL_ACCESS_TOKEN", "GH_TOKEN"})
	require.NoError(t, session.Start(t.TempDir()))

	newSessionCmd := rawArgv(ptyFactory.cmds[0])
	require.Contains(t, newSessionCmd, "-e GITHUB_PERSONAL_ACCESS_TOKEN=tok123")
	require.Contains(t, newSessionCmd, "-e GH_TOKEN=tok123")
}

// A failed token resolution must never block the launch — inject nothing, start anyway.
func TestStartSessionTokenResolutionFailureStillStarts(t *testing.T) {
	stubGitHubToken(t, func(_ context.Context, _ string) (string, error) {
		return "", fmt.Errorf("gh not authenticated")
	})
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "tok-fail", "claude", ptyFactory, startMockExec())
	session.SetGitHubTokenEnv([]string{"GITHUB_PERSONAL_ACCESS_TOKEN"})
	require.NoError(t, session.Start(t.TempDir()))

	require.NotContains(t, cmd2.ToString(ptyFactory.cmds[0]), "GITHUB_PERSONAL_ACCESS_TOKEN")
}

// Guards the opt-in default: with no TokenEnv, the resolver is never invoked (no
// gh subprocess) and no token env is injected.
func TestStartSessionNoTokenEnvSkipsResolution(t *testing.T) {
	called := false
	stubGitHubToken(t, func(_ context.Context, _ string) (string, error) {
		called = true
		return "tok", nil
	})
	ptyFactory := NewMockPtyFactory(t)
	session := NewSessionWithDeps(context.Background(), "no-tok", "claude", ptyFactory, startMockExec())
	session.SetGHConfigDir("/home/tester/.config/gh")
	require.NoError(t, session.Start(t.TempDir()))

	require.False(t, called, "resolveGitHubToken must not run when TokenEnv is empty")
	require.NotContains(t, cmd2.ToString(ptyFactory.cmds[0]), "GH_TOKEN")
}

// TestStartSessionAtriumMarkerReachesPane drives a real tmux server and asserts
// ATRIUM=1 is actually present in the session environment. Self-skips without tmux.
func TestStartSessionAtriumMarkerReachesPane(t *testing.T) {
	testutil.RequireTmux(t)
	name := fmt.Sprintf("markerenv-%d", rand.Int31())
	session := NewSession(context.Background(), name, "sleep 300")
	require.NoError(t, session.Start(t.TempDir()))
	t.Cleanup(func() { _ = session.Close() })

	out, err := tmuxCommand(context.Background(), "show-environment", "-t", session.sanitizedName).Output()
	require.NoError(t, err)
	require.Contains(t, string(out), "ATRIUM=1")
}
