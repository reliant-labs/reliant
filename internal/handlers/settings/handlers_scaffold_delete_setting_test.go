package settings_test

import (
	"testing"

	"connectrpc.com/connect/v2"
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

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.DeleteSettingRequest, pb.DeleteSettingResponse]{
		{
			Name:    "key is required",
			Req:     &pb.DeleteSettingRequest{},
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an unset key is not found",
			Req:     &pb.DeleteSettingRequest{Key: "unset"},
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name: "the stored key is deleted",
			Req:  &pb.DeleteSettingRequest{Key: "theme"},
			Ctx:  settings.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.DeleteSettingResponse) {
				require.True(t, resp.GetSuccess())
				require.NotContains(t, stored, "theme")
			},
		},
	}, svc.DeleteSetting)
}
