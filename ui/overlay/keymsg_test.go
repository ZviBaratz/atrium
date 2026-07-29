package overlay

import (
	"github.com/ZviBaratz/atrium/internal/testutil"

	tea "charm.land/bubbletea/v2"
)

// keyMsg and textMsg are this package's spelling of testutil.Key / testutil.Runes.
//
// They are thin on purpose. The suite builds key literals in hundreds of places,
// and Bubble Tea v2 rewrites the shape of every one of them (#393); routing them
// through a single definition turns that port into one function body instead of a
// diff nobody can review. Wrapping rather than importing testutil at each call
// site keeps the churn to this file — the call sites read `keyMsg("ctrl+s")` with
// no import of their own.
//
// The spec vocabulary is the one msg.String() emits, which is also the one
// keys.GlobalKeyStringsMap is keyed by, so a test now presses the same string the
// dispatch matches. See internal/testutil/keys.go for the full contract.
func keyMsg(spec string) tea.KeyPressMsg { return testutil.Key(spec) }

// textMsg builds typed text, with no keystroke-name interpretation — textMsg("enter")
// types six letters where keyMsg("enter") presses return.
func textMsg(s string) tea.KeyPressMsg { return testutil.Runes(s) }
