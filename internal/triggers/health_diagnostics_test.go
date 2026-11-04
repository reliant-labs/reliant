package triggers

import (
	"testing"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func TestHealthUsesLaunchFailureDetailNotLaterRun(t *testing.T) {
	firing := &core.TriggerEventWithRun{Event: &core.TriggerEvent{Outcome: core.TriggerEventLaunched, RunStatus: core.TriggerRunFailed, OutcomeDetail: "Node build failed: command exited with code 2"}, Run: &core.TriggerEventRun{DisplayState: core.RunDisplayCompleted, Title: "later human reply"}}
	health := ComputeHealth([]*core.TriggerEventWithRun{firing, firing})
	require.Equal(t, pb.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING, health.Status)
	require.Equal(t, firing.Event.OutcomeDetail, health.LastFailureDetail)
}
