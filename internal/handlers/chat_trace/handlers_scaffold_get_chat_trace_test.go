// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
// TestGetChatTrace_Generated: rows for `tdd.RunRPCCases`. The scaffold row is SELF-DESTRUCTING: it matches only
// forge's stub sentinel (not a bare CodeUnimplemented), so it fails once you implement GetChatTrace —
// replace it then with Check / WantErr rows. Depth: `forge skill load testing/patterns`.
package chat_trace_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	chat_trace "github.com/reliant-labs/reliant/internal/handlers/chat_trace"
)

// TestGetChatTrace_Generated runs the canonical scaffold rows for GetChatTrace.
// Add more rows by appending tdd.RPCCase entries — never write a new
// Test<X> function for what is really a new row in this table.
func TestGetChatTrace_Generated(t *testing.T) {
	t.Parallel()

	// chat_trace.NewTestChatTrace (helpers_gen_test.go) returns the
	// chat_trace *Service wired with stubbed dependencies. Override one
	// with chat_trace.WithChatTraceDeps(...), or per-row via Case.Setup.
	svc := chat_trace.NewTestChatTrace(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetChatTraceRequest, pb.GetChatTraceResponse]{
		{
			Name:    "no_chat_id",
			Req:     connect.NewRequest(&pb.GetChatTraceRequest{}),
			Ctx:     chat_trace.AuthedContext(t), // authenticated test claims (UserID "test-user", role "admin")
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.GetChatTrace)
}
