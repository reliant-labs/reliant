package message_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	message "github.com/reliant-labs/reliant/internal/handlers/message"
)

// TestGetMessage_PopulatesDurableToolCallStatus (internal/handlers/chat, which
// also builds the chat service) reads a real message back; these rows pin the
// request and ownership checks that run before it.
func TestGetMessage_Generated(t *testing.T) {
	t.Parallel()

	svc := message.NewTestMessage(t, message.WithMessageDeps(message.Deps{Repo: foreignMessageRepo{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetMessageRequest, pb.GetMessageResponse]{
		{
			Name:    "message_id is required",
			Req:     connect.NewRequest(&pb.GetMessageRequest{}),
			Ctx:     message.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown message is not found",
			Req:     connect.NewRequest(&pb.GetMessageRequest{MessageId: "msg-unknown"}),
			Ctx:     message.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's message is not found",
			Req:     connect.NewRequest(&pb.GetMessageRequest{MessageId: foreignMessageID}),
			Ctx:     message.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetMessage)
}
