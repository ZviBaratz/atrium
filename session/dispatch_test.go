package session

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/cmd/cmd_test"

	"github.com/stretchr/testify/require"
)

func TestGenerateDispatch(t *testing.T) {
	okExec := func(stdout string) cmd_test.MockCmdExec {
		return cmd_test.MockCmdExec{
			OutputFunc: func(*exec.Cmd) ([]byte, error) { return []byte(stdout), nil },
		}
	}

	t.Run("returns the chosen project and sanitized title", func(t *testing.T) {
		out := `{"is_error":false,"result":"{\"project\":\"hub\",\"title\":\"Migration error\"}"}`
		project, title, err := generateDispatch(context.Background(), okExec(out), "claude", t.TempDir(),
			"the hub is failing", []string{"hub", "box"})
		require.NoError(t, err)
		require.Equal(t, "hub", project)
		require.Equal(t, "Migration error", title)
	})

	t.Run("drops a hallucinated project not in the candidate list", func(t *testing.T) {
		out := `{"is_error":false,"result":"{\"project\":\"nonsense\",\"title\":\"Some task\"}"}`
		project, title, err := generateDispatch(context.Background(), okExec(out), "claude", t.TempDir(),
			"do a thing", []string{"hub", "box"})
		require.NoError(t, err)
		require.Empty(t, project, "a project outside the candidate set is rejected")
		require.Equal(t, "Some task", title)
	})

	t.Run("maps is_error to a failure", func(t *testing.T) {
		out := `{"is_error":true,"result":"Not logged in"}`
		_, _, err := generateDispatch(context.Background(), okExec(out), "claude", t.TempDir(),
			"x", []string{"hub"})
		require.Error(t, err)
	})

	t.Run("errors on an unparseable inner reply", func(t *testing.T) {
		out := `{"is_error":false,"result":"not json at all"}`
		_, _, err := generateDispatch(context.Background(), okExec(out), "claude", t.TempDir(),
			"x", []string{"hub"})
		require.Error(t, err)
	})

	t.Run("refuses a blank line without calling claude", func(t *testing.T) {
		called := false
		probe := cmd_test.MockCmdExec{
			OutputFunc: func(*exec.Cmd) ([]byte, error) { called = true; return nil, nil },
		}
		_, _, err := generateDispatch(context.Background(), probe, "claude", t.TempDir(),
			"   ", []string{"hub"})
		require.Error(t, err)
		require.False(t, called)
	})

	t.Run("builds the expected headless invocation", func(t *testing.T) {
		var gotArgs []string
		var gotStdin string
		inspect := cmd_test.MockCmdExec{
			OutputFunc: func(c *exec.Cmd) ([]byte, error) {
				gotArgs = c.Args
				if c.Stdin != nil {
					b, _ := io.ReadAll(c.Stdin)
					gotStdin = string(b)
				}
				return []byte(`{"is_error":false,"result":"{\"project\":\"\",\"title\":\"T\"}"}`), nil
			},
		}
		_, _, err := generateDispatch(context.Background(), inspect, "/usr/bin/claude", t.TempDir(),
			"review the hub", []string{"hub", "box"})
		require.NoError(t, err)
		joined := strings.Join(gotArgs, " ")
		require.Contains(t, joined, "--output-format json")
		require.Contains(t, joined, "--model haiku")
		require.Contains(t, joined, "hub", "the candidate basenames are offered to the model")
		require.Contains(t, gotStdin, "review the hub", "the line is piped on stdin")
	})
}

func TestDispatchBasenames(t *testing.T) {
	t.Run("dedupes by basename preserving first-seen order", func(t *testing.T) {
		got := dispatchBasenames([]string{"/a/box", "/b/box", "/c/hub"}, 40)
		require.Equal(t, []string{"box", "hub"}, got)
	})

	t.Run("caps the list", func(t *testing.T) {
		got := dispatchBasenames([]string{"/a/one", "/b/two", "/c/three"}, 2)
		require.Equal(t, []string{"one", "two"}, got)
	})
}

func TestParseDispatchReply(t *testing.T) {
	t.Run("parses a bare JSON object", func(t *testing.T) {
		project, title, err := parseDispatchReply(`{"project":"hub","title":"Fix it"}`, []string{"hub"})
		require.NoError(t, err)
		require.Equal(t, "hub", project)
		require.Equal(t, "Fix it", title)
	})

	t.Run("tolerates markdown fences and surrounding prose (gemini)", func(t *testing.T) {
		raw := "Sure, here is the routing:\n```json\n{\"project\":\"box\",\"title\":\"Login bug\"}\n```\n"
		project, title, err := parseDispatchReply(raw, []string{"box", "hub"})
		require.NoError(t, err)
		require.Equal(t, "box", project)
		require.Equal(t, "Login bug", title)
	})

	t.Run("errors when no JSON object is present", func(t *testing.T) {
		_, _, err := parseDispatchReply("no json here", []string{"hub"})
		require.Error(t, err)
	})
}
