package package_commands

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
)

// KillProcess stops a package-command process on its machine. One that has
// already stopped is success: that is what was asked for.
func (s *Service) KillProcess(
	ctx context.Context,
	req *connect.Request[reliantv1.KillPackageProcessRequest],
) (*connect.Response[reliantv1.KillPackageProcessResponse], error) {
	if req.Msg.ProcessId == "" {
		return nil, svcerr.InvalidArgument("process_id is required")
	}
	userID, err := daemoncmd.UserID(ctx)
	if err != nil {
		return nil, err
	}
	daemonID, err := s.requestedMachine(ctx, userID, req.Msg.DaemonId)
	if err != nil {
		return nil, err
	}
	var ignored struct{}
	if err := s.daemon(daemonID).Dispatch(ctx, userID, "exec.bg_kill", struct {
		ProcessID string `json:"process_id"`
	}{ProcessID: req.Msg.ProcessId}, &ignored, pkgProcessTimeoutMs); err != nil {
		if strings.Contains(err.Error(), "is not running") {
			return connect.NewResponse(&reliantv1.KillPackageProcessResponse{Message: "Process was already stopped"}), nil
		}
		return nil, err
	}
	return connect.NewResponse(&reliantv1.KillPackageProcessResponse{Message: "Process killed successfully"}), nil
}
