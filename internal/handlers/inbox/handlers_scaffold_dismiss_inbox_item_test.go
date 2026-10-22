package inbox_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	inbox "github.com/reliant-labs/reliant/internal/handlers/inbox"
)

// inbox_test.go covers dismissal against a real database; these rows need
// none.
func TestDismissInboxItem_Generated(t *testing.T) {
	t.Parallel()

	// The one write this rpc makes is programmed; every read refuses.
	svc := inbox.NewTestInbox(t, inbox.WithInboxDeps(inbox.Deps{Repo: &inbox.MockRepository{
		DismissInboxItemsFunc: func(context.Context, string, []string, time.Time) error { return nil },
	}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DismissInboxItemRequest, pb.DismissInboxItemResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     connect.NewRequest(&pb.DismissInboxItemRequest{ItemIds: []string{"approval:a1"}}),
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "item_ids is required",
			Req:     connect.NewRequest(&pb.DismissInboxItemRequest{}),
			Ctx:     inbox.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an id that names no item is rejected",
			Req:     connect.NewRequest(&pb.DismissInboxItemRequest{ItemIds: []string{"chat-1"}}),
			Ctx:     inbox.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "an item id is dismissed",
			Req:  connect.NewRequest(&pb.DismissInboxItemRequest{ItemIds: []string{"approval:a1"}}),
			Ctx:  inbox.AuthedContext(t),
		},
	}, svc.DismissInboxItem)
}
