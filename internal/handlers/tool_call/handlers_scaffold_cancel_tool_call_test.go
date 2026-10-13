package tool_call_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	tool_call "github.com/reliant-labs/reliant/internal/handlers/tool_call"
)

// The cancel path (daemon delivery, spawn stop, durable status) runs against a
// real database in tool_call_test.go, tool_call_cancel_spawn_test.go and
// tool_call_durable_status_test.go; these rows pin the checks before it.
func TestCancelToolCall_Generated(t *testing.T) {
	t.Parallel()

	svc := tool_call.NewTestToolCall(t)
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CancelToolCallRequest, pb.CancelToolCallResponse]{
		{
			Name:    "unauthenticated caller is rejected",
			Req:     connect.NewRequest(&pb.CancelToolCallRequest{ToolCallId: "call-1"}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "tool_call_id is required",
			Req:     connect.NewRequest(&pb.CancelToolCallRequest{}),
			Ctx:     tool_call.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.CancelToolCall)

	missing := tool_call.NewTestToolCall(t, tool_call.WithToolCallDeps(tool_call.Deps{Repo: unknownToolCallRepo{}}))
	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CancelToolCallRequest, pb.CancelToolCallResponse]{
		{
			Name:    "unknown tool call is not found",
			Req:     connect.NewRequest(&pb.CancelToolCallRequest{ToolCallId: "call-1"}),
			Ctx:     tool_call.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, missing.CancelToolCall)
}
