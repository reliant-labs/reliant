// yours: scaffolded once, never touched again — forge will not overwrite this file.
// ONE RPC PER FILE, no package-level identifiers, so two RPCs' owners never collide; CRUD RPCs are
// tested in handlers_crud_test.go.
// TestDetachProjectFromChat_Generated: rows for `tdd.RunRPCCases`. The scaffold row is SELF-DESTRUCTING: it matches only
// forge's stub sentinel (not a bare CodeUnimplemented), so it fails once you implement DetachProjectFromChat —
// replace it then with Check / WantErr rows. Depth: `forge skill load testing/patterns`.
package chat_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	chat "github.com/reliant-labs/reliant/internal/handlers/chat"
)

// TestDetachProjectFromChat_Generated runs the canonical scaffold rows for DetachProjectFromChat.
// Add more rows by appending tdd.RPCCase entries — never write a new
// Test<X> function for what is really a new row in this table.
func TestDetachProjectFromChat_Generated(t *testing.T) {
	t.Parallel()

	// chat.NewTestChat (helpers_gen_test.go) returns the
	// chat *Service wired with stubbed dependencies. Override one
	// with chat.WithChatDeps(...), or per-row via Case.Setup.
	svc := chat.NewTestChat(t)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DetachProjectFromChatRequest, pb.DetachProjectFromChatResponse]{
		{
			Name:    "missing_chat_id",
			Req:     connect.NewRequest(&pb.DetachProjectFromChatRequest{}),
			Ctx:     chat.AuthedContext(t), // authenticated test claims (UserID "test-user", role "admin")
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.DetachProjectFromChat)
}
