package inbox_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect/v2"

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

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.DismissInboxItemRequest, pb.DismissInboxItemResponse]{
		{
			Name:    "a caller without a user is unauthenticated",
			Req:     &pb.DismissInboxItemRequest{ItemIds: []string{"approval:a1"}},
			Ctx:     context.Background(),
			WantErr: connect.CodeUnauthenticated,
		},
		{
			Name:    "item_ids is required",
			Req:     &pb.DismissInboxItemRequest{},
			Ctx:     inbox.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an id that names no item is rejected",
			Req:     &pb.DismissInboxItemRequest{ItemIds: []string{"chat-1"}},
			Ctx:     inbox.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "an item id is dismissed",
			Req:  &pb.DismissInboxItemRequest{ItemIds: []string{"approval:a1"}},
			Ctx:  inbox.AuthedContext(t),
		},
	}, svc.DismissInboxItem)
}
