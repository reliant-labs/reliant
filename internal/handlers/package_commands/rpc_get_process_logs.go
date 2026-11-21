package package_commands

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemoncmd"
)

// GetProcessLogs returns a package-command process's output, line by line
// with the stream each line came from.
func (s *Service) GetProcessLogs(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPackageProcessLogsRequest],
) (*connect.Response[reliantv1.GetPackageProcessLogsResponse], error) {
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
	var output daemon.ProcessOutput
	if err := s.daemon(daemonID).Dispatch(ctx, userID, "exec.bg_output", struct {
		ProcessID string             `json:"process_id"`
		Opts      *daemon.OutputOpts `json:"opts"`
	}{ProcessID: req.Msg.ProcessId, Opts: &daemon.OutputOpts{Lines: true}}, &output, pkgProcessTimeoutMs); err != nil {
		return nil, err
	}
	return connect.NewResponse(processLogs(&output)), nil
}
