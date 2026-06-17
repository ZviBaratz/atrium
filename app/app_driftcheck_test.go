package app

import (
	"context"
	"testing"

	"github.com/ZviBaratz/atrium/config"
	"github.com/ZviBaratz/atrium/internal/doctor"
	"github.com/ZviBaratz/atrium/session/agent"
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
