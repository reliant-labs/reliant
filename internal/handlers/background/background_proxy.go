// Copyright (c) 2025 Reliant Labs
package background

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/processproto"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// bgListTimeoutMs bounds the daemon round trip for a process LIST.
//
// Listing reads the daemon's in-memory process table plus one batched port
// scan; its duration does not depend on the user's commands, so it does not
// need start/kill's 30s budget. A list that can pin an API request — and a
// database-free but pool-bound handler — for 30 seconds is a liveness problem,
// and the caller is a UI list that will simply ask again.
const bgListTimeoutMs int32 = 5_000

// Every rpc forwards its request to the caller's daemon through Deps.Router:
// a DaemonCommand (request/response), or for StreamProcessOutput the
// daemon's output subscription.

func (s *Service) getUserID(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return "", svcerr.Unauthenticated("user ID not found in context")
	}
	return userID, nil
}

func (s *Service) sendCommand(ctx context.Context, userID, commandType string, req any, resp any, timeoutMs int32) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("marshal request"))
	}

	respBytes, err := s.deps.Router.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	if err != nil {
		return daemonFailure("daemon command "+commandType, err)
	}

	if err := json.Unmarshal(respBytes, resp); err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("unmarshal response"))
	}
	return nil
}

// daemonFailure is a failed daemon round trip as the svcerr kind the rpc
// returns. A machine that is starting, asleep or absent is not a server
// fault: Unavailable while it is on its way, FailedPrecondition when the user
// has none (daemonerr.MachineState; ListProcesses used to return the second as
// Internal, ELECTRON-A6). A daemon older than this server is FailedPrecondition
// in the daemon's own words (daemonerr.Outdated). Anything else is Internal,
// saying what was being done: the daemon's own words reach the user, the
// router's and the transport's stay on the error as its logged cause.
func daemonFailure(doing string, err error) error {
	if daemonerr.IsOutdated(err) {
		return daemonerr.Outdated(err)
	}
	if serr, ok := daemonerr.MachineState("", err); ok {
		return serr
	}
	msg := doing + " failed"
	if text, ok := toolexec.DaemonReportedText(err); ok {
		msg += ": " + text
	}
	return svcerr.WithCause(svcerr.Internal(msg), err)
}

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

// daemonProcessInfo mirrors daemon.ProcessInfo for JSON deserialization.
type daemonProcessInfo struct {
	ID        string     `json:"id"`
	Command   string     `json:"command"`
	Status    string     `json:"status"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	StartTime time.Time  `json:"start_time"`
	EndTime   *time.Time `json:"end_time,omitempty"`
}

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

// StreamProcessOutput streams real-time process output by subscribing to the
// daemon's output channel via the router.
func (s *Service) StreamProcessOutput(
	ctx context.Context,
	req *connect.Request[reliantv1.StreamProcessOutputRequest],
	stream *connect.ServerStream[reliantv1.ProcessOutputEvent],
) error {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return err
	}

	if req.Msg.ProcessId == "" {
		return svcerr.InvalidArgument("process_id is required")
	}

	// A process lives on one machine. The router resolves a named one among
	// the caller's own machines only, so another user's id is not found.
	if daemonID := req.Msg.GetDaemonId(); daemonID != "" {
		ctx = toolexec.WithDaemonSelector(ctx, &toolexec.DaemonSelector{ID: daemonID})
	}
	outputCh, unsub, err := s.deps.Router.SubscribeProcessOutput(ctx, userID, req.Msg.ProcessId, req.Msg.NewOnly)
	if err != nil {
		return daemonFailure("subscribe process output", err)
	}
	defer unsub()

	for {
		select {
		case <-ctx.Done():
			return nil
		case evt, ok := <-outputCh:
			if !ok {
				// Channel closed — subscription ended.
				return nil
			}

			if evt.IsComplete {
				exitCode := evt.ExitCode
				if err := stream.Send(&reliantv1.ProcessOutputEvent{
					Event: &reliantv1.ProcessOutputEvent_Complete{
						Complete: &reliantv1.ProcessOutputComplete{
							ExitCode: &exitCode,
						},
					},
				}); err != nil {
					return err
				}
				return nil
			}

			streamType := reliantv1.OutputStreamType_OUTPUT_STREAM_TYPE_STDOUT
			if evt.Stream == "stderr" {
				streamType = reliantv1.OutputStreamType_OUTPUT_STREAM_TYPE_STDERR
			}

			if err := stream.Send(&reliantv1.ProcessOutputEvent{
				Event: &reliantv1.ProcessOutputEvent_Line{
					Line: &reliantv1.ProcessOutputLine{
						Type:     streamType,
						Text:     evt.Data,
						Sequence: int64(evt.Sequence),
					},
				},
			}); err != nil {
				return err
			}
		}
	}
}
