package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParsePrefill(t *testing.T) {
	tests := []struct {
		name          string
		line          string
		candidates    []string
		wantPath      string
		wantTitle     string
		wantPrompt    string
		wantConfident bool
		wantRough     bool
	}{
		{
			name:          "issue ref names the project exactly, dropped from the title",
			line:          "Review box#123",
			candidates:    []string{"/x/box", "/y/atrium"},
			wantPath:      "/x/box",
			wantTitle:     "Review #123", // the project name is redundant with the group; '#' kept
			wantPrompt:    "Review box#123",
			wantConfident: true,
			wantRough:     false,
		},
		{
			name:          "trailing period on the issue ref still matches exactly and strips",
			line:          "Review nanoclaw#247.",
			candidates:    []string{"/x/nanoclaw", "/y/atrium"},
			wantPath:      "/x/nanoclaw",
			wantTitle:     "Review #247",
			wantPrompt:    "Review nanoclaw#247.",
			wantConfident: true,
			wantRough:     false,
		},
		{
			name:          "prose containing the literal repo name strips it and reads as rough",
			line:          "The hub is failing with a migration error",
			candidates:    []string{"/y/hub", "/x/atrium"},
			wantPath:      "/y/hub",
			wantTitle:     "The is failing with a migration", // 'hub' dropped, then 32-char bound
			wantPrompt:    "The hub is failing with a migration error",
			wantConfident: true,
			wantRough:     true,
		},
		{
			name:          "prose with no literal repo name does not route and is not stripped",
			line:          "fix the dashboard crash",
			candidates:    []string{"/x/box", "/y/hub"},
			wantPath:      "",
			wantTitle:     "fix the dashboard crash",
			wantPrompt:    "fix the dashboard crash",
			wantConfident: false,
			wantRough:     false, // 4 words, not > 4
		},
		{
			name:          "same basename in two repos prefills MRU-first, strips the matched name",
			line:          "review box#1",
			candidates:    []string{"/a/box", "/b/box"},
			wantPath:      "/a/box",
			wantTitle:     "review #1",
			wantPrompt:    "review box#1",
			wantConfident: false,
			wantRough:     false,
		},
		{
			name:          "prefix match prefills but does not strip (not a clean mention)",
			line:          "atri needs a tweak",
			candidates:    []string{"/x/atrium"},
			wantPath:      "/x/atrium",
			wantTitle:     "atri needs a tweak",
			wantPrompt:    "atri needs a tweak",
			wantConfident: false,
			wantRough:     false,
		},
		{
			name:          "dash-number that is not a known repo is treated as a plain word",
			line:          "fix box-123 now",
			candidates:    []string{"/x/hub"},
			wantPath:      "",
			wantTitle:     "fix box-123 now",
			wantPrompt:    "fix box-123 now",
			wantConfident: false,
			wantRough:     false,
		},
		{
			name:          "two distinct repos named: only the matched one is stripped",
			line:          "box and hub",
			candidates:    []string{"/x/box", "/y/hub"},
			wantPath:      "/x/box",
			wantTitle:     "and hub",
			wantPrompt:    "box and hub",
			wantConfident: false,
			wantRough:     false,
		},
		{
			name:          "literal repo name mid-prose is dropped from the title",
			line:          "fix the box login bug",
			candidates:    []string{"/x/box"},
			wantPath:      "/x/box",
			wantTitle:     "fix the login bug",
			wantPrompt:    "fix the box login bug",
			wantConfident: true,
			wantRough:     false, // 4 words after the strip
		},
		{
			name:          "five words after the strip reads as rough",
			line:          "box keeps crashing on startup unexpectedly",
			candidates:    []string{"/x/box"},
			wantPath:      "/x/box",
			wantTitle:     "keeps crashing on startup",
			wantPrompt:    "box keeps crashing on startup unexpectedly",
			wantConfident: true,
			wantRough:     true,
		},
		{
			name:          "line that is only the project name falls back to a non-blank title",
			line:          "nanoclaw",
			candidates:    []string{"/x/nanoclaw"},
			wantPath:      "/x/nanoclaw",
			wantTitle:     "nanoclaw",
			wantPrompt:    "nanoclaw",
			wantConfident: true,
			wantRough:     false,
		},
		{
			name:          "blank line is a no-op",
			line:          "   ",
			candidates:    []string{"/x/box"},
			wantPath:      "",
			wantTitle:     "",
			wantPrompt:    "",
			wantConfident: false,
			wantRough:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParsePrefill(tt.line, tt.candidates)
			require.Equal(t, tt.wantPath, got.Path, "Path")
			require.Equal(t, tt.wantTitle, got.Title, "Title")
			require.Equal(t, tt.wantPrompt, got.Prompt, "Prompt")
			require.Equal(t, tt.wantConfident, got.Confident, "Confident")
			require.Equal(t, tt.wantRough, got.TitleIsRough, "TitleIsRough")
		})
	}
}
