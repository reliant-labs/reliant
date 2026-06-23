// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/auth"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/terminal"
)

// TerminalService implements the TerminalService RPC handlers
// Note: The interactive terminal WebSocket (/terminal/ws) remains as a WebSocket endpoint
// because it requires true bidirectional streaming (user input + PTY output).
// This service handles only the session management operations.
type TerminalService struct {
	reliantv1connect.UnimplementedTerminalServiceHandler
	manager *terminal.Manager
}

// NewTerminalService creates a new TerminalService
func NewTerminalService(manager *terminal.Manager) *TerminalService {
	return &TerminalService{
		manager: manager,
	}
}

// ListSessions returns active terminal sessions for the authenticated user.
func (s *TerminalService) ListSessions(
	ctx context.Context,
	req *connect.Request[reliantv1.ListTerminalSessionsRequest],
) (*connect.Response[reliantv1.ListTerminalSessionsResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}
	sessions := s.manager.ListSessionsForUser(userID)

	protoSessions := make([]*reliantv1.TerminalSession, 0, len(sessions))
	for _, session := range sessions {
		protoSession := &reliantv1.TerminalSession{
			Id:         session.ID,
			WorkingDir: session.WorkingDir,
			CreatedAt:  session.CreatedAt.Format(time.RFC3339),
			LastActive: session.LastActive.Format(time.RFC3339),
		}

		// Add PID if available
		if session.CMD != nil && session.CMD.Process != nil {
			pid := int32(session.CMD.Process.Pid)
			protoSession.Pid = &pid
		}

		protoSessions = append(protoSessions, protoSession)
	}

	return connect.NewResponse(&reliantv1.ListTerminalSessionsResponse{
		Sessions: protoSessions,
		Total:    int32(len(protoSessions)),
	}), nil
}

// CloseSession closes a specific terminal session if owned by the authenticated user.
func (s *TerminalService) CloseSession(
	ctx context.Context,
	req *connect.Request[reliantv1.CloseTerminalSessionRequest],
) (*connect.Response[reliantv1.CloseTerminalSessionResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, nil)
	}

	sessionID := req.Msg.SessionId

	if sessionID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}

	err := s.manager.CloseSessionForUser(sessionID, userID)
	if err != nil {
		return connect.NewResponse(&reliantv1.CloseTerminalSessionResponse{
			Success: false,
			Message: err.Error(),
		}), nil
	}

	return connect.NewResponse(&reliantv1.CloseTerminalSessionResponse{
		Success: true,
		Message: "Session closed successfully",
	}), nil
}
