package settings_test

import (
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
)

func TestGetSetting_Generated(t *testing.T) {
	t.Parallel()

	svc := newSettingsService(t, memorySettings{
		"theme": {ID: "s-1", UserID: "test-user", Key: "theme", Value: "dark", ValueType: "string"},
	}.repo())

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.GetSettingRequest, pb.GetSettingResponse]{
		{
			Name:    "key is required",
			Req:     &pb.GetSettingRequest{},
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "an unset key answers an empty response, not an error",
			Req:  &pb.GetSettingRequest{Key: "unset"},
			Ctx:  settings.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.GetSettingResponse) {
				require.Nil(t, resp.GetSetting())
			},
		},
		{
			Name: "a stored key",
			Req:  &pb.GetSettingRequest{Key: "theme"},
			Ctx:  settings.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.GetSettingResponse) {
				require.Equal(t, "dark", resp.GetSetting().GetValue())
			},
		},
	}, svc.GetSetting)
}
