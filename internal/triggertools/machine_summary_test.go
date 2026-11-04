package triggertools

import (
	"testing"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestSummarizeProjectsMachineAndNodeFailure(t *testing.T) {
	s := summarize(&pb.Trigger{Id: "t", DaemonId: "machine", DaemonName: "buildbox", MachineState: "connected", Health: &pb.TriggerHealth{Status: pb.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING, LastFailureDetail: "Node gate failed: build error"}})
	require.Equal(t, "machine", s.DaemonID)
	require.Equal(t, "buildbox", s.DaemonName)
	require.Equal(t, "connected", s.MachineState)
	require.Equal(t, "Node gate failed: build error", s.HealthDetail)
}
