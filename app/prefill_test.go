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
	}{
		{
			name:          "issue ref names the project exactly",
			line:          "Review box#123",
			candidates:    []string{"/x/box", "/y/atrium"},
			wantPath:      "/x/box",
			wantTitle:     "Review box 123",
			wantPrompt:    "Review box#123",
			wantConfident: true,
		},
		{
			name:          "prose containing the literal repo name routes confidently",
			line:          "The hub is failing with a migration error",
			candidates:    []string{"/y/hub", "/x/atrium"},
			wantPath:      "/y/hub",
			wantTitle:     "The hub is failing with a",
			wantPrompt:    "The hub is failing with a migration error",
			wantConfident: true,
		},
		{
			name:          "prose with no literal repo name does not route",
			line:          "fix the dashboard crash",
			candidates:    []string{"/x/box", "/y/hub"},
			wantPath:      "",
			wantTitle:     "fix the dashboard crash",
			wantPrompt:    "fix the dashboard crash",
			wantConfident: false,
		},
		{
			name:          "same basename in two repos prefills MRU-first but is not confident",
			line:          "review box#1",
			candidates:    []string{"/a/box", "/b/box"},
			wantPath:      "/a/box",
			wantTitle:     "review box 1",
			wantPrompt:    "review box#1",
			wantConfident: false,
		},
		{
			name:          "prefix match prefills but is not confident",
			line:          "atri needs a tweak",
			candidates:    []string{"/x/atrium"},
			wantPath:      "/x/atrium",
			wantTitle:     "atri needs a tweak",
			wantPrompt:    "atri needs a tweak",
			wantConfident: false,
		},
		{
			name:          "dash-number that is not a known repo is treated as a plain word",
			line:          "fix box-123 now",
			candidates:    []string{"/x/hub"},
			wantPath:      "",
			wantTitle:     "fix box-123 now",
			wantPrompt:    "fix box-123 now",
			wantConfident: false,
		},
		{
			name:          "two distinct repos named prefills the first, not confident",
			line:          "box and hub",
			candidates:    []string{"/x/box", "/y/hub"},
			wantPath:      "/x/box",
			wantTitle:     "box and hub",
			wantPrompt:    "box and hub",
			wantConfident: false,
		},
		{
			name:          "blank line is a no-op",
			line:          "   ",
			candidates:    []string{"/x/box"},
			wantPath:      "",
			wantTitle:     "",
			wantPrompt:    "",
			wantConfident: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParsePrefill(tt.line, tt.candidates)
			require.Equal(t, tt.wantPath, got.Path, "Path")
			require.Equal(t, tt.wantTitle, got.Title, "Title")
			require.Equal(t, tt.wantPrompt, got.Prompt, "Prompt")
			require.Equal(t, tt.wantConfident, got.Confident, "Confident")
		})
	}
}
