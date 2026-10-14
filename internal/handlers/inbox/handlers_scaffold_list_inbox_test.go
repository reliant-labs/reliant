package inbox_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	inbox "github.com/reliant-labs/reliant/internal/handlers/inbox"
)

// inbox_test.go and inbox_storage_test.go cover listing against a real
// database; these rows pin the checks that run before any read.
func TestListInbox_Generated(t *testing.T) {
	t.Parallel()

	// The harness's auto-stubbed Repo refuses every call, so no row may
	// reach the database.
	svc := inbox.NewTestInbox(t)
	negative := int32(-1)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.ListInboxRequest, pb.ListInboxResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     connect.NewRequest(&pb.ListInboxRequest{}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "a negative limit is rejected",
			Req:     connect.NewRequest(&pb.ListInboxRequest{Limit: &negative}),
			Ctx:     inbox.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
	}, svc.ListInbox)
}
