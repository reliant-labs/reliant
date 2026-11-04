// yours: scaffolded once, never touched again — forge will not overwrite this file.
//
// TestGetToolResult_Generated covers GetToolResult's request validation, which
// fails before any repository read. The behavior over a real database (the
// owner's read, not-found, a reused call id, a branch's inherited result) is
// TestGetToolResult in rpc_get_tool_result_test.go.
//
// Library reference: github.com/reliant-labs/forge/pkg/tdd
package chat_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

func TestGetToolResult_Generated(t *testing.T) {
	t.Parallel()

	svc := chat.NewTestChat(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetToolResultRequest, pb.GetToolResultResponse]{
		{
			Name:    "missing_chat_id",
			Req:     connect.NewRequest(&pb.GetToolResultRequest{ToolCallId: "call_1"}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "missing_tool_call_id",
			Req:     connect.NewRequest(&pb.GetToolResultRequest{ChatId: "chat-1"}),
			Ctx:     chat.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.GetToolResult)
}
