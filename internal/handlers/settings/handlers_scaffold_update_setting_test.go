package settings_test

import (
	"testing"

	"connectrpc.com/connect/v2"
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

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.UpdateSettingRequest, pb.UpdateSettingResponse]{
		{
			Name:    "key is required",
			Req:     &pb.UpdateSettingRequest{Value: ptr.Of("light")},
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an unset key is not found",
			Req:     &pb.UpdateSettingRequest{Key: "unset", Value: ptr.Of("light")},
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "only the fields set change",
			Req:  &pb.UpdateSettingRequest{Key: "theme", Value: ptr.Of("light")},
			Ctx:  settings.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.UpdateSettingResponse) {
				require.Equal(t, "light", resp.GetSetting().GetValue())
				require.Equal(t, "string", stored["theme"].ValueType)
				require.Equal(t, "light", stored["theme"].Value)
			},
		},
	}, svc.UpdateSetting)
}
