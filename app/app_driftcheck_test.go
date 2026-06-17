package app

import (
	"context"
	"strings"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/doctor"
	"github.com/ZviBaratz/atrium/session/agent"
	"github.com/ZviBaratz/atrium/ui"

	"github.com/charmbracelet/bubbles/spinner"
)

func TestDriftCheckCmdEmitsUnackedDrift(t *testing.T) {
	orig := checkDrift
	t.Cleanup(func() { checkDrift = orig })
	checkDrift = func(context.Context) []doctor.Result {
		return []doctor.Result{
			{Key: agent.KeyClaude, Name: "Claude Code", Installed: "2.1.179", Status: doctor.StatusDrifted},
			{Key: agent.KeyGemini, Name: "Gemini CLI", Installed: "0.45.1", Status: doctor.StatusOK},
		}
	}

	m := &home{ctx: context.Background(), appState: config.DefaultState()}
	msg := m.driftCheckCmd()()
	df, ok := msg.(driftFoundMsg)
	if !ok {
		t.Fatalf("driftCheckCmd returned %T, want driftFoundMsg", msg)
	}
	if len(df.agents) != 1 || df.agents[0].Key != agent.KeyClaude {
		t.Fatalf("driftFoundMsg.agents = %+v, want only claude", df.agents)
	}
}

func TestDriftCheckCmdSuppressesAcked(t *testing.T) {
	orig := checkDrift
	t.Cleanup(func() { checkDrift = orig })
	checkDrift = func(context.Context) []doctor.Result {
		return []doctor.Result{
			{Key: agent.KeyClaude, Name: "Claude Code", Installed: "2.1.179", Status: doctor.StatusDrifted},
		}
	}

	st := config.DefaultState()
	st.AckedDrift = map[string]string{"claude": "2.1.179"} // already acked at this version
	m := &home{ctx: context.Background(), appState: st}
	if msg := m.driftCheckCmd()(); msg != nil {
		t.Fatalf("driftCheckCmd returned %T, want nil (acked)", msg)
	}
}

// TestDriftFoundMsg_NoAckWhenHintDropped verifies that when the hint bar cannot
// render (menu is nil), driftFoundMsg records no ack so the hint re-arms on a
// later startup instead of being silently consumed.
func TestDriftFoundMsg_NoAckWhenHintDropped(t *testing.T) {
	st := config.DefaultState()
	s := spinner.New()
	m := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		list:      ui.NewList(&s),
		appConfig: config.DefaultConfig(), // hint_bar: true
		appState:  st,
		// menu is intentionally nil: handleInfoNotice returns nil when menu == nil,
		// so the notice is dropped and no ack should be recorded.
	}

	agents := []doctor.Result{
		{Key: agent.KeyClaude, Name: "Claude Code", Installed: "2.1.179", Status: doctor.StatusDrifted},
	}
	m.Update(driftFoundMsg{agents: agents})

	if got := m.appState.GetAckedDrift(); len(got) != 0 {
		t.Fatalf("ack was recorded despite hint being dropped: %v", got)
	}

	m.list.SetSize(80, 24)
	if out := m.list.String(); !strings.Contains(out, "stale") {
		t.Errorf("drift badge not shown after hint was dropped; panel:\n%s", out)
	}
}

// TestDriftFoundMsg_AckRecordedWhenHintShown verifies that when the hint bar is
// available, driftFoundMsg records the ack at the agent's current installed version.
func TestDriftFoundMsg_AckRecordedWhenHintShown(t *testing.T) {
	st := config.DefaultState()
	s := spinner.New()
	m := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		list:      ui.NewList(&s),
		menu:      ui.NewMenu(),
		appConfig: config.DefaultConfig(), // hint_bar: true
		appState:  st,
	}

	agents := []doctor.Result{
		{Key: agent.KeyClaude, Name: "Claude Code", Installed: "2.1.179", Status: doctor.StatusDrifted},
	}
	m.Update(driftFoundMsg{agents: agents})

	got := m.appState.GetAckedDrift()
	if got["claude"] != "2.1.179" {
		t.Fatalf("ack not recorded after hint shown: GetAckedDrift() = %v", got)
	}

	m.list.SetSize(80, 24)
	if out := m.list.String(); strings.Contains(out, "stale") {
		t.Errorf("drift badge should NOT be set when the hint was shown; panel:\n%s", out)
	}
}
