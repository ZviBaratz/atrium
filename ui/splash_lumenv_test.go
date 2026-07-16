package ui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLumRangeEnvReachesRender pins the inverted env plumbing end to end. The
// splash engine reads no environment, so ui must resolve ATRIUM_SPLASH_LUMRANGE
// (init → splashLumRangeOverride) and thread it through splash.Render's
// Options.LumRange in splashScene. init reads the variable once at package load,
// which is why this drives it in a subprocess rather than in-process: the same
// variant rendered with the override set must differ from one rendered without
// it, or the knob was dropped at the ui→engine boundary.
//
// The rendered variant is whatever TestMain pins (tunnel); the override is a
// separate variable TestMain leaves alone, so the child reads the value this
// parent hands it.
func TestLumRangeEnvReachesRender(t *testing.T) {
	if os.Getenv("ATRIUM_LUM_CHILD") == "1" {
		sum := sha256.Sum256([]byte(SplashScreensaver(120, 40, 7)))
		fmt.Printf("RENDERHASH:%s\n", hex.EncodeToString(sum[:]))
		os.Exit(0)
	}

	child := func(lum string) string {
		env := make([]string, 0, len(os.Environ())+2)
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, "ATRIUM_SPLASH_LUMRANGE=") {
				env = append(env, e)
			}
		}
		env = append(env, "ATRIUM_LUM_CHILD=1")
		if lum != "" {
			env = append(env, "ATRIUM_SPLASH_LUMRANGE="+lum)
		}
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestLumRangeEnvReachesRender$")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "child failed: %s", out)
		for _, line := range strings.Split(string(out), "\n") {
			if h, ok := strings.CutPrefix(line, "RENDERHASH:"); ok {
				return h
			}
		}
		t.Fatalf("child emitted no RENDERHASH: %s", out)
		return ""
	}

	base := child("")
	overridden := child("0")
	require.NotEqual(t, base, overridden,
		"ATRIUM_SPLASH_LUMRANGE must change the render — the engine reads no env, so "+
			"ui must thread it through splash.Render's Options.LumRange (see splashScene)")
}
