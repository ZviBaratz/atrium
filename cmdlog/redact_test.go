package cmdlog

import (
	"strings"
	"testing"
)

// Redact scrubs the value of secret-bearing NAME=VALUE argv tokens while leaving
// the command, its flags, and the safe env Atrium injects untouched. This is the
// guard behind AC "secret-bearing arguments/environment are redacted" — the
// asserts below fail if the token value ever survives into the log text.
func TestRedact_ScrubsSecretsKeepsSafe(t *testing.T) {
	argv := []string{
		"tmux", "-L", "sock", "new-session", "-d",
		"-e", "GITHUB_PERSONAL_ACCESS_TOKEN=ghp_supersecretvalue",
		"-e", "GH_TOKEN=anothersecret",
		"-e", "ATRIUM_SESSION=mysession",
		"-e", "CLAUDE_CONFIG_DIR=/home/u/.claude",
		"-e", "GH_CONFIG_DIR=/home/u/.config/gh",
		"claude",
	}
	got := Redact(argv)

	// Secrets must be gone; their names remain (so the log is still legible).
	for _, secret := range []string{"ghp_supersecretvalue", "anothersecret"} {
		if strings.Contains(got, secret) {
			t.Errorf("redacted output leaked secret %q:\n%s", secret, got)
		}
	}
	for _, want := range []string{
		"GITHUB_PERSONAL_ACCESS_TOKEN=***",
		"GH_TOKEN=***",
		"ATRIUM_SESSION=mysession", // safe env passes through
		"CLAUDE_CONFIG_DIR=/home/u/.claude",
		"GH_CONFIG_DIR=/home/u/.config/gh",
		"tmux -L sock new-session -d", // command + flags untouched
		"claude",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("redacted output missing %q:\n%s", want, got)
		}
	}
}

// Assorted sensitive name shapes are all caught; plain args are never touched.
func TestRedact_NameShapes(t *testing.T) {
	cases := map[string]bool{ // input token -> should be redacted
		"MY_SECRET=x":        true,
		"DB_PASSWORD=hunter": true,
		"api_key=abc":        true, // case-insensitive
		"SERVICE_PAT=z":      true,
		"AWS_CREDENTIAL=q":   true,
		"GH_CONFIG_DIR=/p":   false,
		"ATRIUM_SESSION=s":   false,
		"-C":                 false, // no '=', untouched
		"origin":             false,
	}
	for in, wantRedacted := range cases {
		got := Redact([]string{in})
		isRedacted := strings.HasSuffix(got, "=***")
		if isRedacted != wantRedacted {
			t.Errorf("Redact(%q) = %q; redacted=%v want %v", in, got, isRedacted, wantRedacted)
		}
	}
}
