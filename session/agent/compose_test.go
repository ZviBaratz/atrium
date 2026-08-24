package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ComposeProgramFlags is the submit-time backstop the create form's field gating
// makes otherwise unreachable there — the model field reverts charset-invalid
// input and the mode chips are a closed valid set, so from the form these
// rejections only fire on UI/enum drift. From `atrium new`'s pin flags the same
// rejections are the ordinary answer to a typo. Test them directly, plus the
// compose and pass-through cases.
func TestComposeProgramFlags(t *testing.T) {
	t.Run("invalid model name is rejected", func(t *testing.T) {
		_, err := ComposeProgramFlags("claude", "bad model!", "", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid model name")
	})
	t.Run("invalid permission mode is rejected", func(t *testing.T) {
		_, err := ComposeProgramFlags("claude", "", "bogusmode", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid permission mode")
	})
	t.Run("invalid effort level is rejected", func(t *testing.T) {
		_, err := ComposeProgramFlags("claude", "", "", "ultracode")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid effort level")
	})
	t.Run("valid model, mode, and effort compose onto a claude program", func(t *testing.T) {
		got, err := ComposeProgramFlags("claude", "opus", "plan", "xhigh")
		require.NoError(t, err)
		assert.Equal(t, "claude --model opus --permission-mode plan --effort xhigh", got)
	})
	t.Run("effort alone composes", func(t *testing.T) {
		got, err := ComposeProgramFlags("claude", "", "", "high")
		require.NoError(t, err)
		assert.Equal(t, "claude --effort high", got)
	})
	t.Run("a non-claude program is left untouched", func(t *testing.T) {
		got, err := ComposeProgramFlags("echo", "opus", "plan", "xhigh")
		require.NoError(t, err)
		assert.Equal(t, "echo", got)
	})
	t.Run("empty overrides leave the program untouched", func(t *testing.T) {
		got, err := ComposeProgramFlags("claude", "", "", "")
		require.NoError(t, err)
		assert.Equal(t, "claude", got)
	})
}
