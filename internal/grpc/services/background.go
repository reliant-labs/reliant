// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// BackgroundService implements the BackgroundService RPC handlers
type BackgroundService struct {
	reliantv1connect.UnimplementedBackgroundServiceHandler
	provider BackgroundProcessProvider
}

// NewBackgroundService creates a new BackgroundService with the given provider
func NewBackgroundService(provider BackgroundProcessProvider) *BackgroundService {
	return &BackgroundService{provider: provider}
}

// ============================================================================
// Helper Methods
// ============================================================================

// convertProcessInfoToProto converts a BackgroundProcessInfo to proto format
func convertProcessInfoToProto(p *BackgroundProcessInfo) *reliantv1.BackgroundProcess {
	result := &reliantv1.BackgroundProcess{
		Id:         p.ID,
		Command:    p.Command,
		Status:     backgroundProcessStatusFromString(p.Status),
		StartTime:  p.StartTime.Format("2006-01-02T15:04:05Z07:00"),
		WorkingDir: p.WorkingDir,
		SessionId:  p.SessionID,
	}

	if p.WorktreeID != "" {
		result.WorktreeId = proto.String(p.WorktreeID)
	}

	if p.ChatID != "" {
		result.ChatId = proto.String(p.ChatID)
	}

	if p.EndTime != nil {
		endTime := p.EndTime.Format("2006-01-02T15:04:05Z07:00")
		result.EndTime = proto.String(endTime)
	}

	if p.ExitCode != nil {
		result.ExitCode = proto.Int32(int32(*p.ExitCode))
	}

	// Convert port information
	if len(p.Ports) > 0 {
		result.Ports = make([]*reliantv1.PortInfo, len(p.Ports))
		for i, port := range p.Ports {
			result.Ports[i] = &reliantv1.PortInfo{
				Port:     int32(port.Port),
				Protocol: port.Protocol,
				State:    port.State,
				Address:  port.Address,
			}
		}
	}

	return result
}

// ============================================================================
// RPC Handlers
// ============================================================================

// ListProcesses returns all background processes, optionally filtered
func (s *BackgroundService) ListProcesses(
	ctx context.Context,
	req *connect.Request[reliantv1.ListBackgroundProcessesRequest],
) (*connect.Response[reliantv1.ListBackgroundProcessesResponse], error) {
	worktreeID := ""
	if req.Msg.WorktreeId != nil {
		worktreeID = *req.Msg.WorktreeId
	}

	sessionID := ""
	if req.Msg.SessionId != nil {
		sessionID = *req.Msg.SessionId
	}

	chatID := ""
	if req.Msg.ChatId != nil {
		chatID = *req.Msg.ChatId
	}

	processes, err := s.provider.ListProcesses(ctx, worktreeID, sessionID, chatID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list processes: %w", err))
	}

	// Convert to proto format
	protoProcesses := make([]*reliantv1.BackgroundProcess, len(processes))
	for i := range processes {
		protoProcesses[i] = convertProcessInfoToProto(&processes[i])
	}

	return connect.NewResponse(&reliantv1.ListBackgroundProcessesResponse{
		Processes: protoProcesses,
	}), nil
}

// GetProcessOutput returns the stdout/stderr output of a process
func (s *BackgroundService) GetProcessOutput(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProcessOutputRequest],
) (*connect.Response[reliantv1.GetProcessOutputResponse], error) {
	processID := req.Msg.ProcessId
	if processID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	output, err := s.provider.GetOutput(ctx, processID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	return connect.NewResponse(&reliantv1.GetProcessOutputResponse{
		Stdout: output.Stdout,
		Stderr: output.Stderr,
	}), nil
}

// KillProcess terminates a background process
func (s *BackgroundService) KillProcess(
	ctx context.Context,
	req *connect.Request[reliantv1.KillProcessRequest],
) (*connect.Response[reliantv1.KillProcessResponse], error) {
	processID := req.Msg.ProcessId
	if processID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	err := s.provider.KillProcess(ctx, processID)
	if err != nil {
		// If process is already dead, treat as success - the desired state is achieved
		if strings.Contains(err.Error(), "is not running") {
			return connect.NewResponse(&reliantv1.KillProcessResponse{
				Message: "Process was already stopped",
			}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&reliantv1.KillProcessResponse{
		Message: "Process killed successfully",
	}), nil
}

// StreamProcessOutput streams real-time process output to the client.
// On connect, it sends existing output as a snapshot, then streams new lines.
func (s *BackgroundService) StreamProcessOutput(
	ctx context.Context,
	req *connect.Request[reliantv1.StreamProcessOutputRequest],
	stream *connect.ServerStream[reliantv1.ProcessOutputEvent],
) error {
	processID := req.Msg.ProcessId
	if processID == "" {
		return connect.NewError(connect.CodeInvalidArgument, nil)
	}

	// Get current process status
	status, isComplete, exitCode, err := s.provider.GetProcessStatus(ctx, processID)
	if err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}

	// Send initial snapshot unless client only wants new output
	if !req.Msg.NewOnly {
		lines, latestSeq, err := s.provider.GetCombinedOutputWithSeq(ctx, processID, 0)
		if err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}

		// Convert lines to proto format
		protoLines := make([]*reliantv1.ProcessOutputLine, len(lines))
		for i, line := range lines {
			protoLines[i] = &reliantv1.ProcessOutputLine{
				Type:     outputStreamTypeFromString(line.Type),
				Text:     line.Text,
				Sequence: line.Sequence,
			}
		}

		// Build snapshot message
		snapshot := &reliantv1.ProcessOutputSnapshot{
			Lines:          protoLines,
			LatestSequence: latestSeq,
			IsComplete:     isComplete,
		}
		if isComplete {
			snapshot.Status = backgroundProcessStatusPtr(status)
			if exitCode != nil {
				snapshot.ExitCode = proto.Int32(int32(*exitCode))
			}
		}

		if err := stream.Send(&reliantv1.ProcessOutputEvent{
			Event: &reliantv1.ProcessOutputEvent_Snapshot{Snapshot: snapshot},
		}); err != nil {
			return err
		}
	}

	// If process is already complete, send completion and exit
	if isComplete {
		return s.sendCompletionEvent(ctx, stream, processID, status, exitCode)
	}

	// If provider doesn't support streaming, send completion immediately
	if !s.provider.SupportsStreaming() {
		return s.sendCompletionEvent(ctx, stream, processID, status, exitCode)
	}

	// Subscribe to new output
	sub, err := s.provider.SubscribeToOutput(ctx, processID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	defer s.provider.UnsubscribeFromOutput(sub)

	// Stream new lines until process completes or client disconnects
	for {
		select {
		case <-ctx.Done():
			// Client disconnected
			return nil

		case line, ok := <-sub.Sub.Ch:
			if !ok {
				// Channel closed, subscription ended
				return nil
			}

			// Send the new line
			if err := stream.Send(&reliantv1.ProcessOutputEvent{
				Event: &reliantv1.ProcessOutputEvent_Line{
					Line: &reliantv1.ProcessOutputLine{
						Type:     outputStreamTypeFromString(line.Type),
						Text:     line.Text,
						Sequence: line.Sequence,
					},
				},
			}); err != nil {
				return err
			}

		case <-sub.Sub.Done:
			// Process completed - get final status and send completion event
			status, _, exitCode, err := s.provider.GetProcessStatus(ctx, processID)
			if err != nil {
				return connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get process status: %w", err))
			}
			return s.sendCompletionEvent(ctx, stream, processID, status, exitCode)
		}
	}
}

// sendCompletionEvent sends a ProcessOutputComplete event on the stream.
func (s *BackgroundService) sendCompletionEvent(
	ctx context.Context,
	stream *connect.ServerStream[reliantv1.ProcessOutputEvent],
	processID, status string,
	exitCode *int,
) error {
	proc, err := s.provider.GetProcess(ctx, processID)
	endTime := ""
	if err == nil && proc != nil && proc.EndTime != nil {
		endTime = proc.EndTime.Format("2006-01-02T15:04:05Z07:00")
	}

	complete := &reliantv1.ProcessOutputComplete{
		Status:  backgroundProcessStatusFromString(status),
		EndTime: endTime,
	}
	if exitCode != nil {
		complete.ExitCode = proto.Int32(int32(*exitCode))
	}

	return stream.Send(&reliantv1.ProcessOutputEvent{
		Event: &reliantv1.ProcessOutputEvent_Complete{Complete: complete},
	})
}
