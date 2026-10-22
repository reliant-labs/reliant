package settings_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	settings "github.com/reliant-labs/reliant/internal/handlers/settings"
)

func TestPollCopilotDeviceAuth_Generated(t *testing.T) {
	t.Parallel()

	svc := newSettingsService(t, memorySettings{}.repo())

	t.Run("device_code is required", func(t *testing.T) {
		_, err := svc.PollCopilotDeviceAuth(settings.AuthedContext(t), connect.NewRequest(&pb.PollCopilotDeviceAuthRequest{DeviceCode: " "}))
		require.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	})

	// A poll that fails for any reason but GitHub's own pending, expired and
	// denied answers is a STATUS_ERROR response, not an rpc error: the
	// client keeps the dialog open and shows the message. A cancelled
	// context fails the poll before it leaves the process.
	t.Run("a failed poll answers STATUS_ERROR", func(t *testing.T) {
		ctx, cancel := context.WithCancel(settings.AuthedContext(t))
		cancel()
		resp, err := svc.PollCopilotDeviceAuth(ctx, connect.NewRequest(&pb.PollCopilotDeviceAuthRequest{DeviceCode: "dc-1"}))
		require.NoError(t, err)
		require.Equal(t, pb.PollCopilotDeviceAuthResponse_STATUS_ERROR, resp.Msg.GetStatus())
		require.NotEmpty(t, resp.Msg.GetErrorMessage())
	})
}
