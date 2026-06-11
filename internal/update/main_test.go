package update

import (
	"os"
	"testing"

	"github.com/ZviBaratz/atrium/log"
)

// TestMain sandboxes HOME so no test can ever read or write the user's real
// data dir (the check cache lives under config.GetConfigDir()). Individual
// cache tests still t.Setenv their own temp HOME for isolation from each other.
func TestMain(m *testing.M) {
	tmpHome, err := os.MkdirTemp("", "atrium-update-test-home-")
	if err == nil {
		_ = os.Setenv("HOME", tmpHome)
	}
	log.Initialize(false)
	code := m.Run()
	log.Close()
	if tmpHome != "" {
		_ = os.RemoveAll(tmpHome)
	}
	os.Exit(code)
}
