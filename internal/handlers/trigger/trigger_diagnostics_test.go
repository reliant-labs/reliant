package trigger

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func TestTriggerProjectsMachineAndHistoricalLaunchNodeFailure(t *testing.T) {
	repo := &MockRepository{ListRunNodeExecutionsFunc: func(_ context.Context, id string, limit int) ([]*db.StepExecution, error) {
		require.Equal(t, "launched-chat", id, "never use the chat's later root workflow")
		return []*db.StepExecution{{StepID: "gate", ExitCode: sql.NullInt64{Int64: 2, Valid: true}, ErrorMessage: sql.NullString{String: "build error api_key=secret", Valid: true}}}, nil
	}}
	svc := &Service{deps: Deps{Repo: repo}}
	chatID := "launched-chat"
	out := svc.renderFirings(context.Background(), &core.Trigger{ID: "trigger", Kind: core.TriggerKindSchedule, Config: json.RawMessage(`{"interval":"1h"}`), DaemonID: "d", DaemonName: "buildbox", MachineState: "suspended"}, []*core.TriggerEventWithRun{
		{Event: &core.TriggerEvent{Outcome: core.TriggerEventLaunched, RunStatus: core.TriggerRunFailed, ChatID: &chatID}, Run: &core.TriggerEventRun{DisplayState: core.RunDisplayCompleted}},
	}, nil)
	require.Equal(t, "buildbox", out.DaemonName)
	require.Equal(t, "d", out.DaemonId)
	require.Equal(t, "suspended", out.MachineState)
	require.Equal(t, pb.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_DEGRADED, out.Health.Status)
	require.Equal(t, "Node gate failed: [REDACTED: credential-bearing diagnostic omitted]", out.Health.LastFailureDetail)
}
