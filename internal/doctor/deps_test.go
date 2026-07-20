package doctor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeDepRunner returns canned version output (or error) per binary.
type fakeDepRunner struct {
	out map[string]string
	err map[string]error
}

func (f fakeDepRunner) probe(_ context.Context, bin, _ string) (string, error) {
	if e, ok := f.err[bin]; ok {
		// Canned output rides along with the error for the same reason as
		// fakeRunner.version: an empty-output error is indistinguishable from
		// unparseable output, since checkDeps maps both to DepPresentUnknown.
		return f.out[bin], e
	}
	if o, ok := f.out[bin]; ok {
		return o, nil
	}
	return "", fmt.Errorf("%w: %s", ErrNotInstalled, bin)
}

func stateFor(results []DepResult, name string) DepResult {
	for _, r := range results {
		if r.Name == name {
			return r
		}
	}
	return DepResult{}
}

func TestCheckDepsClassifies(t *testing.T) {
	okAuth := authChecker(func(context.Context) error { return nil })
	failAuth := authChecker(func(context.Context) error { return errors.New("not logged in") })

	t.Run("all present and parseable", func(t *testing.T) {
		r := fakeDepRunner{out: map[string]string{
			"tmux": "tmux 3.6\n",
			"git":  "git version 2.53.0\n",
			"gh":   "gh version 2.46.0 (2025-12-13)\n",
		}}
		got := checkDeps(context.Background(), coreDeps, r, "linux", okAuth)
		for _, name := range []string{"tmux", "git", "gh"} {
			d := stateFor(got, name)
			if d.State != DepOK {
				t.Errorf("%s: State = %v, want DepOK", name, d.State)
			}
			if d.Hint != "" {
				t.Errorf("%s: Hint = %q, want empty for DepOK", name, d.Hint)
			}
		}
		if v := stateFor(got, "tmux").Version; v != "3.6" {
			t.Errorf("tmux version = %q, want 3.6", v)
		}
	})

	t.Run("tmux missing", func(t *testing.T) {
		r := fakeDepRunner{
			out: map[string]string{"git": "git version 2.53.0\n", "gh": "gh version 2.46.0\n"},
			err: map[string]error{"tmux": ErrNotInstalled},
		}
		got := checkDeps(context.Background(), coreDeps, r, "darwin", okAuth)
		d := stateFor(got, "tmux")
		if d.State != DepMissing {
			t.Fatalf("tmux State = %v, want DepMissing", d.State)
		}
		if !strings.Contains(d.Hint, "brew install tmux") {
			t.Errorf("tmux Hint = %q, want a brew hint", d.Hint)
		}
		if !MissingRequired(got) {
			t.Error("MissingRequired = false, want true when tmux missing")
		}
	})

	t.Run("gh present but unauthenticated", func(t *testing.T) {
		r := fakeDepRunner{out: map[string]string{
			"tmux": "tmux 3.6\n", "git": "git version 2.53.0\n", "gh": "gh version 2.46.0\n",
		}}
		got := checkDeps(context.Background(), coreDeps, r, "linux", failAuth)
		d := stateFor(got, "gh")
		if d.State != DepPresentUnauth {
			t.Fatalf("gh State = %v, want DepPresentUnauth", d.State)
		}
		if !strings.Contains(d.Hint, "gh auth login") {
			t.Errorf("gh Hint = %q, want an auth hint", d.Hint)
		}
		// gh is already installed here — the hint must not tell the user to reinstall it.
		if strings.Contains(d.Hint, "install:") {
			t.Errorf("gh Hint = %q, must not advise a reinstall when gh is present", d.Hint)
		}
		// gh is optional, so an unauthenticated gh must not fail doctor.
		if MissingRequired(got) {
			t.Error("MissingRequired = true, want false when only gh is unauthenticated")
		}
	})

	t.Run("tmux present but unparseable version", func(t *testing.T) {
		r := fakeDepRunner{out: map[string]string{
			"tmux": "tmux next\n", "git": "git version 2.53.0\n", "gh": "gh version 2.46.0\n",
		}}
		got := checkDeps(context.Background(), coreDeps, r, "linux", okAuth)
		d := stateFor(got, "tmux")
		if d.State != DepPresentUnknown {
			t.Fatalf("tmux State = %v, want DepPresentUnknown", d.State)
		}
		// Present-but-unknown is not missing, so it must not fail doctor.
		if MissingRequired(got) {
			t.Error("MissingRequired = true, want false when tmux is present with an odd version")
		}
	})

	t.Run("nil auth leaves present gh at ok", func(t *testing.T) {
		r := fakeDepRunner{out: map[string]string{
			"tmux": "tmux 3.6\n", "git": "git version 2.53.0\n", "gh": "gh version 2.46.0\n",
		}}
		got := checkDeps(context.Background(), coreDeps, r, "linux", nil)
		if d := stateFor(got, "gh"); d.State != DepOK {
			t.Fatalf("gh State = %v, want DepOK when auth checker is nil", d.State)
		}
	})
}

func TestInstallHint(t *testing.T) {
	tmux := depSpec{name: "tmux", bin: "tmux"}
	gh := depSpec{name: "gh", bin: "gh"}
	cases := []struct {
		goos, want string
		spec       depSpec
	}{
		{"darwin", "brew install tmux", tmux},
		{"linux", "sudo apt install tmux", tmux},
		{"windows", "install docs", tmux},
		{"darwin", "brew install gh", gh},
		{"linux", "github.com/cli/cli", gh},
	}
	for _, c := range cases {
		// A missing binary is the case that warrants an install command.
		if got := installHint(c.goos, c.spec, DepMissing); !strings.Contains(got, c.want) {
			t.Errorf("installHint(%q, %q) = %q, want substring %q", c.goos, c.spec.bin, got, c.want)
		}
	}
}

// installHint must not tell a user to reinstall a binary that is already present:
// an unauthenticated gh only needs `gh auth login`, and a present-but-unparseable
// binary has nothing to install.
func TestInstallHint_PresentStatesDoNotAdviseReinstall(t *testing.T) {
	gh := depSpec{name: "gh", bin: "gh"}
	tmux := depSpec{name: "tmux", bin: "tmux"}

	unauth := installHint("darwin", gh, DepPresentUnauth)
	if strings.Contains(unauth, "install:") {
		t.Errorf("unauthenticated gh hint advises a reinstall: %q", unauth)
	}
	if !strings.Contains(unauth, "gh auth login") {
		t.Errorf("unauthenticated gh hint = %q, want it to point at gh auth login", unauth)
	}

	if unknown := installHint("linux", tmux, DepPresentUnknown); strings.Contains(unknown, "install:") {
		t.Errorf("present-but-unknown tmux hint advises a reinstall: %q", unknown)
	}
}

func TestRenderDeps(t *testing.T) {
	out := RenderDeps([]DepResult{
		{Name: "tmux", Bin: "tmux", Kind: DepRequired, State: DepMissing, Hint: "install: brew install tmux"},
		{Name: "git", Bin: "git", Kind: DepRequired, State: DepOK, Version: "2.53.0"},
		{Name: "gh", Bin: "gh", Kind: DepOptional, State: DepPresentUnauth, Hint: "run: gh auth login"},
	})

	for _, want := range []string{
		"Core dependencies:",
		"tmux", "not installed", "brew install tmux",
		"git", "2.53.0", "ok",
		"gh", "not authenticated", "gh auth login",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderDeps() missing %q\n--- got ---\n%s", want, out)
		}
	}
	// An OK dep must not emit a hint line.
	if strings.Contains(out, "→ install: brew install git") {
		t.Errorf("RenderDeps() emitted a hint for an OK dep\n%s", out)
	}
}

// A missing dependency's row must not contradict itself by claiming the binary is
// "installed" while its status reads "not installed".
func TestRenderDeps_MissingRowNotContradictory(t *testing.T) {
	out := RenderDeps([]DepResult{
		{Name: "tmux", Bin: "tmux", Kind: DepRequired, State: DepMissing, Hint: "install: brew install tmux"},
	})

	var row string
	for _, ln := range strings.Split(out, "\n") {
		if strings.Contains(ln, "tmux") {
			row = ln
			break
		}
	}
	if row == "" {
		t.Fatalf("no tmux row found in output:\n%s", out)
	}
	// "not installed" is the only place "installed" may legitimately appear; a
	// leftover bare "installed" token means the row asserts the opposite too.
	if strings.Contains(strings.Replace(row, "not installed", "", 1), "installed") {
		t.Errorf("missing-dep row contradictorily claims the binary is installed: %q", row)
	}
}

// TestCheckDeps_ProbeErrorIsDepPresentUnknown guards the case where the binary is
// found on PATH but the version command exits nonzero (e.g. killed by a signal).
// That path is distinct from ErrNotInstalled (binary absent) and from an unparseable
// version output (successful exit, garbage output): the probe itself errors, but the
// binary IS present — so the result is DepPresentUnknown, never DepMissing, and
// MissingRequired stays false.
//
// tmux's canned output is parseable and paired with the error on purpose: an
// erroring probe that returns "" also lands on DepPresentUnknown via the default
// branch's failed parseVersion, so a test using "" would still pass with the
// err != nil branch deleted. With parseable output, deleting it yields DepOK — and
// a version string the caller was told not to trust — so this test fails.
func TestCheckDeps_ProbeErrorIsDepPresentUnknown(t *testing.T) {
	r := fakeDepRunner{
		err: map[string]error{
			"tmux": errors.New("exec: signal: killed"),
		},
		out: map[string]string{
			"tmux": "tmux 3.4\n",
			"git":  "git version 2.53.0\n",
			"gh":   "gh version 2.46.0\n",
		},
	}
	got := checkDeps(context.Background(), coreDeps, r, "linux", nil)
	d := stateFor(got, "tmux")
	if d.State != DepPresentUnknown {
		t.Fatalf("tmux State = %v, want DepPresentUnknown when probe errors (not ErrNotInstalled)", d.State)
	}
	if d.Version != "" {
		t.Errorf("tmux Version = %q, want empty: output from a failed probe must not be reported", d.Version)
	}
	if MissingRequired(got) {
		t.Error("MissingRequired = true, want false when tmux probe errors (binary present, just unreportable)")
	}
	if strings.Contains(d.Hint, "install:") {
		t.Errorf("tmux Hint = %q, must not advise reinstall for a present binary", d.Hint)
	}
}

// TestRenderDeps_PresentUnknownVersion pins the DepState.label() default case
// ("⚠ unknown version"), reached when a binary is on PATH but its version
// is unreadable. The label is the user-visible signal that the tool exists
// but its version could not be parsed.
func TestRenderDeps_PresentUnknownVersion(t *testing.T) {
	out := RenderDeps([]DepResult{
		{Name: "tmux", Kind: DepRequired, State: DepPresentUnknown},
	})
	if !strings.Contains(out, "unknown version") {
		t.Errorf("RenderDeps() with DepPresentUnknown missing 'unknown version'\n%s", out)
	}
}
