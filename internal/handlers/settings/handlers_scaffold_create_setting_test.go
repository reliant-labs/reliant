package settings_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
)

func TestCreateSetting_Generated(t *testing.T) {
	t.Parallel()

	stored := memorySettings{}
	svc := newSettingsService(t, stored.repo())

	failing := memorySettings{}.repo()
	failing.CreateSettingFunc = func(context.Context, *db.Setting) error { return errors.New("db down") }
	failingSvc := newSettingsService(t, failing)

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.CreateSettingRequest, pb.CreateSettingResponse]{
		{
			Name:    "key is required",
			Req:     &pb.CreateSettingRequest{Value: "dark"},
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "value is required",
			Req:     &pb.CreateSettingRequest{Key: "theme"},
			Ctx:     settings.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name: "the setting is the caller's, typed string by default",
			Req:  &pb.CreateSettingRequest{Key: "theme", Value: "dark"},
			Ctx:  settings.AuthedContext(t),
			Check: func(t *testing.T, resp *pb.CreateSettingResponse) {
				require.Equal(t, "theme", resp.GetSetting().GetKey())
				require.Equal(t, "string", resp.GetSetting().GetValueType())
				require.Equal(t, "test-user", stored["theme"].UserID)
				require.Equal(t, "dark", stored["theme"].Value)
			},
		},
	}, svc.CreateSetting)

	_, err := failingSvc.CreateSetting(settings.AuthedContext(t), &pb.CreateSettingRequest{Key: "theme", Value: "dark"})
	require.Equal(t, connect.CodeInternal, svcerr.CodeV2(err), "a failed write is internal")
}
