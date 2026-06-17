package doctor

import (
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/session/agent"
)

func TestRender(t *testing.T) {
	out := Render([]Result{
		{Key: agent.KeyClaude, Name: "Claude Code", Installed: "2.1.179", Verified: "2.1.170", Status: StatusDrifted},
		{Key: agent.KeyGemini, Name: "Gemini CLI", Installed: "0.27.4", Verified: "0.27", Status: StatusOK},
		{Key: agent.KeyCodex, Name: "Codex", Status: StatusNotInstalled},
		{Key: agent.KeyAider, Name: "Aider", Installed: "0.64.1", Status: StatusUnknown},
	})

	for _, want := range []string{
		"Claude Code", "2.1.179", "2.1.170", "drifted",
		"Gemini CLI", "ok",
		"Codex", "not installed",
		"Aider", "unknown",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render() output missing %q\n--- got ---\n%s", want, out)
		}
	}
}
