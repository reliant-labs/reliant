package settings_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
	"github.com/reliant-labs/reliant/internal/ptr"
)

func TestUpdateSetting_Generated(t *testing.T) {
	t.Parallel()

	stored := memorySettings{
		"theme": {ID: "s-1", UserID: "test-user", Key: "theme", Value: "dark", ValueType: "string"},
	}
	svc := newSettingsService(t, stored.repo())

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.UpdateSettingRequest, pb.UpdateSettingResponse]{
		{
			Name:    "key is required",
			Req:     connect.NewRequest(&pb.UpdateSettingRequest{Value: ptr.Of("light")}),
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an unset key is not found",
			Req:     connect.NewRequest(&pb.UpdateSettingRequest{Key: "unset", Value: ptr.Of("light")}),
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "only the fields set change",
			Req:  connect.NewRequest(&pb.UpdateSettingRequest{Key: "theme", Value: ptr.Of("light")}),
			Ctx:  settings.AuthedContext(t),
			Check: func(t *testing.T, resp *connect.Response[pb.UpdateSettingResponse]) {
				require.Equal(t, "light", resp.Msg.GetSetting().GetValue())
				require.Equal(t, "string", stored["theme"].ValueType)
				require.Equal(t, "light", stored["theme"].Value)
			},
		},
	}, svc.UpdateSetting)
}
