package preset_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	preset "github.com/reliant-labs/reliant/internal/handlers/preset"
)

// Setting, reading back and clearing a default, and the project ownership
// check, run against a real database in preset_batch_test.go
// (TestPresetService_SetDefaultPreset_RoundTrip); these rows pin the request
// checks that run before it.
func TestSetDefaultPreset_Generated(t *testing.T) {
	t.Parallel()

	svc := preset.NewTestPreset(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.SetDefaultPresetRequest, pb.SetDefaultPresetResponse]{
		{
			Name:    "project_id is required",
			Req:     connect.NewRequest(&pb.SetDefaultPresetRequest{WorkflowName: "builtin://agent"}),
			Ctx:     preset.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "workflow_name is required",
			Req:     connect.NewRequest(&pb.SetDefaultPresetRequest{ProjectId: "p-1"}),
			Ctx:     preset.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.SetDefaultPreset)
}
