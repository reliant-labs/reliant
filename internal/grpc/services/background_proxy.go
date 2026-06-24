// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/reliant/internal/auth"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// BackgroundProxyService implements BackgroundServiceHandler by forwarding
// requests to the user's daemon via DaemonCommand (request/response).
type BackgroundProxyService struct {
	reliantv1connect.UnimplementedBackgroundServiceHandler
	router toolexec.DaemonRouter
}

// NewBackgroundProxyService creates a new BackgroundProxyService.
func NewBackgroundProxyService(router toolexec.DaemonRouter) *BackgroundProxyService {
	return &BackgroundProxyService{router: router}
}

func (s *BackgroundProxyService) getUserID(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("user ID not found in context")
	}
	return userID, nil
}

func (s *BackgroundProxyService) sendCommand(ctx context.Context, userID, commandType string, req any, resp any, timeoutMs int32) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))
	}

	respBytes, err := s.router.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}

	if err := json.Unmarshal(respBytes, resp); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))
	}
	return nil
}

// ListProcesses returns all background processes.
func (s *BackgroundProxyService) ListProcesses(
	ctx context.Context,
	req *connect.Request[reliantv1.ListBackgroundProcessesRequest],
) (*connect.Response[reliantv1.ListBackgroundProcessesResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	var cmdResp []daemonProcessInfo
	if err := s.sendCommand(ctx, userID, "exec.bg_list", struct{}{}, &cmdResp, 30000); err != nil {
		return nil, err
	}

	processes := make([]*reliantv1.BackgroundProcess, len(cmdResp))
	for i, p := range cmdResp {
		proc := &reliantv1.BackgroundProcess{
			Id:        p.ID,
			Command:   p.Command,
			Status:    backgroundProcessStatusFromString(p.Status),
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
func (s *BackgroundProxyService) GetProcessOutput(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProcessOutputRequest],
) (*connect.Response[reliantv1.GetProcessOutputResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	if req.Msg.ProcessId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("process_id is required"))
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
func (s *BackgroundProxyService) KillProcess(
	ctx context.Context,
	req *connect.Request[reliantv1.KillProcessRequest],
) (*connect.Response[reliantv1.KillProcessResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	if req.Msg.ProcessId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("process_id is required"))
	}

	cmdReq := map[string]any{
		"process_id": req.Msg.ProcessId,
	}

	var cmdResp struct{}
	if err := s.sendCommand(ctx, userID, "exec.bg_kill", cmdReq, &cmdResp, 30000); err != nil {
		return nil, err
	}

	return connect.NewResponse(&reliantv1.KillProcessResponse{
		Message: "Process killed successfully",
	}), nil
}

// StreamProcessOutput streams real-time process output by subscribing to the
// daemon's output channel via the router.
func (s *BackgroundProxyService) StreamProcessOutput(
	ctx context.Context,
	req *connect.Request[reliantv1.StreamProcessOutputRequest],
	stream *connect.ServerStream[reliantv1.ProcessOutputEvent],
) error {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, err)
	}

	if req.Msg.ProcessId == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("process_id is required"))
	}

	outputCh, unsub, err := s.router.SubscribeProcessOutput(ctx, userID, req.Msg.ProcessId, req.Msg.NewOnly)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("subscribe process output: %w", err))
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
