package agent

import "testing"

func TestValidModelName(t *testing.T) {
	valid := []string{
		"opus", "fable", "haiku", "sonnet",
		"claude-opus-4-6", "claude-3-5-haiku-20241022",
		"us.anthropic.claude-x:0", "ollama_chat/gemma3", "Opus",
	}
	for _, s := range valid {
		if !ValidModelName(s) {
			t.Errorf("ValidModelName(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"", " ", "opus 4", "opus;rm -rf", "$(whoami)", `"opus"`, "'opus'",
		"--model", "-opus", ".opus", "opus\n",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 65 chars
	}
	for _, s := range invalid {
		if ValidModelName(s) {
			t.Errorf("ValidModelName(%q) = true, want false", s)
		}
	}
}

func TestWithModelFlag(t *testing.T) {
	cases := []struct {
		name, program, model, want string
	}{
		{"append to bare program", "claude", "opus", "claude --model opus"},
		{"append preserves existing flags",
			"claude --dangerously-skip-permissions", "haiku",
			"claude --dangerously-skip-permissions --model haiku"},
		{"replace separate-form flag",
			"claude --model sonnet", "opus", "claude --model opus"},
		{"replace combined-form flag",
			"claude --model=sonnet", "opus", "claude --model opus"},
		{"replace keeps trailing flags",
			"claude --model sonnet --dangerously-skip-permissions", "opus",
			"claude --dangerously-skip-permissions --model opus"},
		{"replace works on non-claude programs too",
			"aider --model ollama_chat/gemma3:1b", "ollama_chat/llama3",
			"aider --model ollama_chat/llama3"},
		// A flag that merely starts with "--model" is not a model pin: the
		// program must take the verbatim append path, not the replace path
		// whose Fields re-join would collapse the quoted run of spaces.
		{"flag lookalike takes the append path",
			"claude --models-dir 'a  b'", "opus",
			"claude --models-dir 'a  b' --model opus"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WithModelFlag(c.program, c.model); got != c.want {
				t.Errorf("WithModelFlag(%q, %q) = %q, want %q", c.program, c.model, got, c.want)
			}
		})
	}
}
