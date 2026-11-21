package settings

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers/copilot"
)

// StartCopilotDeviceAuth begins the GitHub Copilot device-authorization flow and
// returns the user code + verification URI for the client to present.
func (s *Service) StartCopilotDeviceAuth(ctx context.Context, req *connect.Request[reliantv1.StartCopilotDeviceAuthRequest]) (*connect.Response[reliantv1.StartCopilotDeviceAuthResponse], error) {
	// userID is not required to start the flow, but require an authenticated
	// caller for parity with the other provider handlers.
	_ = auth.MustGetUserID(ctx)

	deviceAuth, err := copilot.StartDeviceAuth(ctx)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to start GitHub Copilot login"))
	}

	return connect.NewResponse(&reliantv1.StartCopilotDeviceAuthResponse{
		DeviceCode:       deviceAuth.DeviceCode,
		UserCode:         deviceAuth.UserCode,
		VerificationUri:  deviceAuth.VerificationURI,
		IntervalSeconds:  int32(deviceAuth.Interval),
		ExpiresInSeconds: int32(deviceAuth.ExpiresIn),
	}), nil
}
