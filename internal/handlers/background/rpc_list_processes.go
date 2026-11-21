package background

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/processproto"
)

// bgListTimeoutMs bounds the daemon round trip for a process LIST.
//
// Listing reads the daemon's in-memory process table plus one batched port
// scan; its duration does not depend on the user's commands, so it does not
// need start/kill's 30s budget. A list that can pin an API request — and a
// database-free but pool-bound handler — for 30 seconds is a liveness problem,
// and the caller is a UI list that will simply ask again.
const bgListTimeoutMs int32 = 5_000

// ListProcesses returns all background processes.
func (s *Service) ListProcesses(
	ctx context.Context,
	req *connect.Request[reliantv1.ListBackgroundProcessesRequest],
) (*connect.Response[reliantv1.ListBackgroundProcessesResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	var cmdResp []daemonProcessInfo
	if err := s.sendCommand(ctx, userID, "exec.bg_list", struct{}{}, &cmdResp, bgListTimeoutMs); err != nil {
		return nil, err
	}

	processes := make([]*reliantv1.BackgroundProcess, len(cmdResp))
	for i, p := range cmdResp {
		proc := &reliantv1.BackgroundProcess{
			Id:        p.ID,
			Command:   p.Command,
			Status:    processproto.Status(p.Status),
			StartTime: p.StartTime.Format(time.RFC3339),
		}
		if p.ExitCode != nil {
			proc.ExitCode = proto.Int32(int32(*p.ExitCode))
		}
		if p.EndTime != nil {
			endTime := p.EndTime.Format(time.RFC3339)
			proc.EndTime = &endTime
		}
		processes[i] = proc
	}

	return connect.NewResponse(&reliantv1.ListBackgroundProcessesResponse{
		Processes: processes,
	}), nil
}
