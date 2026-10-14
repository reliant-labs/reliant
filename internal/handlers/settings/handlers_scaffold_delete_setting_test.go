package settings_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
)

func TestDeleteSetting_Generated(t *testing.T) {
	t.Parallel()

	stored := memorySettings{
		"theme": {ID: "s-1", UserID: "test-user", Key: "theme", Value: "dark", ValueType: "string"},
	}
	svc := newSettingsService(t, stored.repo())

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DeleteSettingRequest, pb.DeleteSettingResponse]{
		{
			Name:    "key is required",
			Req:     connect.NewRequest(&pb.DeleteSettingRequest{}),
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an unset key is not found",
			Req:     connect.NewRequest(&pb.DeleteSettingRequest{Key: "unset"}),
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the stored key is deleted",
			Req:  connect.NewRequest(&pb.DeleteSettingRequest{Key: "theme"}),
			Ctx:  settings.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.DeleteSettingResponse]) {
				require.True(t, resp.Msg.GetSuccess())
				require.NotContains(t, stored, "theme")
			},
		},
	}, svc.DeleteSetting)
}
