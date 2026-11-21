package background

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// GetProcessOutput returns the stdout/stderr output of a process.
func (s *Service) GetProcessOutput(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProcessOutputRequest],
) (*connect.Response[reliantv1.GetProcessOutputResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	if req.Msg.ProcessId == "" {
		return nil, svcerr.InvalidArgument("process_id is required")
	}

	cmdReq := map[string]any{
		"process_id": req.Msg.ProcessId,
	}

	var cmdResp struct {
		Output     string `json:"output"`
		HasMore    bool   `json:"has_more"`
		NextOffset int    `json:"next_offset"`
		TotalBytes int    `json:"total_bytes"`
	}
	if err := s.sendCommand(ctx, userID, "exec.bg_output", cmdReq, &cmdResp, 30000); err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.GetProcessOutputResponse{
		Stdout: cmdResp.Output,
	}), nil
}
