package background

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// processNotRunningMarker is the phrase the daemon's BackgroundManager.KillProcess
// writes for a process that already exited ("process <id> is not running
// (status: …)"). Daemon-command errors cross NATS as text, so the text is the
// wire contract between the two (as in daemonerr's markers).
const processNotRunningMarker = "is not running"

// processNotRunning reclassifies a kill of a process that had already exited:
// not a server fault (it answered Internal) but FailedPrecondition with the
// process_not_running reason, which the web treats as "already stopped" and
// refreshes. The message is unchanged — older clients match on it.
func processNotRunning(err error) error {
	text, ok := toolexec.DaemonReportedText(err)
	if !ok || !strings.Contains(text, processNotRunningMarker) {
		return err
	}
	return svcerr.WithReason(svcerr.WithCause(svcerr.FailedPrecondition(svcerr.ToConnect(err).Message()), err),
		errreason.ProcessNotRunning)
}

// KillProcess terminates a background process.
func (s *Service) KillProcess(
	ctx context.Context,
	req *connect.Request[reliantv1.KillProcessRequest],
) (*connect.Response[reliantv1.KillProcessResponse], error) {
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

	var cmdResp struct{}
	if err := s.sendCommand(ctx, userID, "exec.bg_kill", cmdReq, &cmdResp, 30000); err != nil {
		return nil, processNotRunning(err)
	}

	return connect.NewResponse(&reliantv1.KillProcessResponse{
		Message: "Process killed successfully",
	}), nil
}
