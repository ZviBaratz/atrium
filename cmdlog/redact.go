package cmdlog

import "strings"

// sensitiveEnvNames are argv tokens of the form NAME=VALUE whose VALUE must never
// reach the log. The gh token is injected into the tmux client argv as
// `-e GITHUB_PERSONAL_ACCESS_TOKEN=<token>` (session/tmux/tmux.go), and any future
// secret-bearing env would follow the same shape. Matching is by substring on the
// NAME (upper-cased), so GH_TOKEN, GITHUB_TOKEN, GITHUB_PERSONAL_ACCESS_TOKEN,
// *_SECRET, *_PASSWORD, *_PAT, *_CREDENTIAL, *APIKEY, *API_KEY are all scrubbed —
// while the safe env Atrium injects (ATRIUM_SESSION, CLAUDE_CONFIG_DIR,
// GH_CONFIG_DIR) passes through untouched.
var sensitiveEnvNames = []string{
	"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "APIKEY", "API_KEY", "PAT",
}

// Redact renders argv as a single space-joined string with the value of any
// secret-bearing NAME=VALUE token replaced by "***". It is the ONLY function that
// turns an argv into log text, so a secret can never be recorded verbatim.
func Redact(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = redactArg(a)
	}
	return strings.Join(out, " ")
}

func redactArg(a string) string {
	eq := strings.IndexByte(a, '=')
	if eq <= 0 {
		return a
	}
	name := strings.ToUpper(a[:eq])
	if isSensitiveName(name) {
		return a[:eq] + "=***"
	}
	return a
}

func isSensitiveName(name string) bool {
	for _, s := range sensitiveEnvNames {
		if strings.Contains(name, s) {
			return true
		}
	}
	return false
}
