package doctor

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/customcmd"
)

// CheckCustomCommands reports the custom_commands entries validation refuses to
// bind (#375).
//
// A rejected entry is silently absent from the menu — that is the correct runtime
// behaviour, since one typo must not cost the user their other commands — which
// leaves "why is my command not there?" with no answer at the terminal. This is that
// answer, and it is deliberately the same list the TUI surfaces at startup: one
// validation pass, two places to read it.
func CheckCustomCommands(cfg *config.Config) []customcmd.Problem {
	if cfg == nil {
		return nil
	}
	_, problems := customcmd.Validate(cfg.CustomCommands)
	return problems
}

// RenderCustomCommands formats rejected entries for `atrium doctor` (empty string
// when none), in the section shape RenderPools established.
func RenderCustomCommands(problems []customcmd.Problem) string {
	if len(problems) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Custom commands:\n")
	for _, p := range problems {
		fmt.Fprintf(&b, "  ⚠ %s\n", p.Error())
	}
	return b.String()
}
