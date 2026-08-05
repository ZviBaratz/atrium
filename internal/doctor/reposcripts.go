package doctor

import (
	"fmt"
	"strings"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/repocfg"
)

// CheckRepoScripts reports the repo_scripts entries validation refuses to apply
// (#389).
//
// A rejected entry is dropped rather than applied — the correct runtime behaviour,
// since one typo must not cost the user their other repos' scripts — which leaves "why
// did my setup script not run?" with no answer anywhere. This is that answer, and it
// reads the same validation pass the session does, so the two can never disagree
// about what is valid.
func CheckRepoScripts(cfg *config.Config) []repocfg.Problem {
	if cfg == nil {
		return nil
	}
	_, problems := repocfg.Validate(cfg.RepoScripts)
	return problems
}

// RenderRepoScripts formats refused entries for `atrium doctor` (empty string when
// none), in the section shape RenderPools established.
func RenderRepoScripts(problems []repocfg.Problem) string {
	if len(problems) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Repo scripts:\n")
	for _, p := range problems {
		fmt.Fprintf(&b, "  ⚠ %s\n", p.Error())
	}
	return b.String()
}
