package inbox_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	inbox "github.com/reliant-labs/reliant/internal/handlers/inbox"
)

// inbox_test.go covers restoring against a real database; these rows need
// none.
func TestRestoreInboxItem_Generated(t *testing.T) {
	t.Parallel()

	// The one write this rpc makes is programmed; every read refuses.
	svc := inbox.NewTestInbox(t, inbox.WithInboxDeps(inbox.Deps{Repo: &inbox.StubRepo{
		RestoreInboxItemsFunc: func(context.Context, string, []string) error { return nil },
	}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.RestoreInboxItemRequest, pb.RestoreInboxItemResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     connect.NewRequest(&pb.RestoreInboxItemRequest{ItemIds: []string{"question:q1"}}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "item_ids is required",
			Req:     connect.NewRequest(&pb.RestoreInboxItemRequest{}),
			Ctx:     inbox.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an id that names no item is rejected",
			Req:     connect.NewRequest(&pb.RestoreInboxItemRequest{ItemIds: []string{"question:"}}),
			Ctx:     inbox.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "an item id is restored",
			Req:  connect.NewRequest(&pb.RestoreInboxItemRequest{ItemIds: []string{"question:q1"}}),
			Ctx:  inbox.AuthedContext(t),
		},
	}, svc.RestoreInboxItem)
}
