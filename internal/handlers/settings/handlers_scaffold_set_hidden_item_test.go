package settings_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
)

func TestSetHiddenItem_Generated(t *testing.T) {
	t.Parallel()

	type override struct {
		userID    string
		itemType  int32
		slug      string
		isVisible bool
	}
	var got []override
	repo := memorySettings{}.repo()
	repo.SetVisibilityOverrideFunc = func(_ context.Context, userID string, itemType int32, slug string, isVisible bool) error {
		got = append(got, override{userID, itemType, slug, isVisible})
		return nil
	}
	svc := newSettingsService(t, repo)

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.SetHiddenItemRequest, pb.SetHiddenItemResponse]{
		{
			Name:    "item_type must be workflow or preset",
			Req:     connect.NewRequest(&pb.SetHiddenItemRequest{Slug: "agent", Hidden: true}),
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "slug is required",
			Req:     connect.NewRequest(&pb.SetHiddenItemRequest{ItemType: pb.HiddenItemType_HIDDEN_ITEM_TYPE_WORKFLOW, Hidden: true}),
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "hiding stores an is_visible=false override for the caller",
			Req:  connect.NewRequest(&pb.SetHiddenItemRequest{ItemType: pb.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET, Slug: "fast", Hidden: true}),
			Ctx:  settings.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.SetHiddenItemResponse]) {
				require.True(t, resp.Msg.GetSuccess())
				require.Equal(t, []override{{"test-user", int32(pb.HiddenItemType_HIDDEN_ITEM_TYPE_PRESET), "fast", false}}, got)
			},
		},
	}, svc.SetHiddenItem)
}
