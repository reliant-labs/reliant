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

// A successful start reaches GitHub's device-code endpoint. A cancelled
// context fails the request before it leaves the process, which pins how the
// handler reports a start that failed.
func TestStartCopilotDeviceAuth_Generated(t *testing.T) {
	t.Parallel()

	svc := newSettingsService(t, memorySettings{}.repo())
	ctx, cancel := context.WithCancel(settings.AuthedContext(t))
	cancel()

	_, err := svc.StartCopilotDeviceAuth(ctx, connect.NewRequest(&pb.StartCopilotDeviceAuthRequest{}))
	require.Equal(t, connect.CodeInternal, svcerr.Code(err))
	require.ErrorContains(t, err, "failed to start GitHub Copilot login")
}
