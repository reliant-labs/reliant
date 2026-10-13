package tool_call_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	tool_call "github.com/reliant-labs/reliant/internal/handlers/tool_call"
)

// The background path (daemon delivery, durable status) runs against a real
// database in tool_call_background_test.go and tool_call_durable_status_test.go;
// these rows pin the checks before it.
func TestConvertToBackground_Generated(t *testing.T) {
	t.Parallel()

	svc := tool_call.NewTestToolCall(t)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ConvertToBackgroundRequest, pb.ConvertToBackgroundResponse]{
		{
			Name:    "unauthenticated caller is rejected",
			Req:     connect.NewRequest(&pb.ConvertToBackgroundRequest{ToolCallId: "call-1"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "tool_call_id is required",
			Req:     connect.NewRequest(&pb.ConvertToBackgroundRequest{}),
			Ctx:     tool_call.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.ConvertToBackground)

	missing := tool_call.NewTestToolCall(t, tool_call.WithToolCallDeps(tool_call.Deps{Repo: unknownToolCallRepo{}}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ConvertToBackgroundRequest, pb.ConvertToBackgroundResponse]{
		{
			Name:    "unknown tool call is not found",
			Req:     connect.NewRequest(&pb.ConvertToBackgroundRequest{ToolCallId: "call-1"}),
			Ctx:     tool_call.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, missing.ConvertToBackground)
}
