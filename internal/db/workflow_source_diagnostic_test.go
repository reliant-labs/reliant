package db

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkflowSnapshotDiagnostic(t *testing.T) {
	for _, state := range []string{"offline", "removed", "online"} {
		t.Run(state, func(t *testing.T) {
			msg := workflowSnapshotDiagnostic("cicd", "daemon-id", state, "/projects/labs", "")
			require.Contains(t, msg, `machine "cicd" (daemon-id, `+state+`)`)
			require.Contains(t, msg, "/projects/labs/.reliant/workflows")
			require.Contains(t, msg, "nested repository")
			require.Contains(t, msg, "original path is not retained")
			require.Contains(t, msg, "save the committed workflow again")
			require.NotContains(t, msg, "merge-steward.yaml", "a name is not a filename")
		})
	}
	msg := workflowSnapshotDiagnostic("default", "d", "offline", "/projects/labs", "control-plane/.reliant/workflows/steward.yaml")
	require.Contains(t, msg, "/projects/labs/control-plane/.reliant/workflows/steward.yaml")
}
