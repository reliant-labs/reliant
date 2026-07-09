// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// TerminalProxyService implements TerminalServiceHandler by forwarding
// requests to the user's daemon via DaemonCommand (request/response).
type TerminalProxyService struct {
	reliantv1connect.UnimplementedTerminalServiceHandler
	router toolexec.DaemonRouter
}

// NewTerminalProxyService creates a new TerminalProxyService.
func NewTerminalProxyService(router toolexec.DaemonRouter) *TerminalProxyService {
	return &TerminalProxyService{router: router}
}

func (s *TerminalProxyService) getUserID(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("user ID not found in context")
	}
	return userID, nil
}

// ListSessions returns all active terminal sessions.
func (s *TerminalProxyService) ListSessions(
	ctx context.Context,
	req *connect.Request[reliantv1.ListTerminalSessionsRequest],
) (*connect.Response[reliantv1.ListTerminalSessionsResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	payload, err := json.Marshal(struct{}{})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))
	}

	respBytes, err := s.router.SendDaemonCommand(ctx, userID, "terminal.list", payload, 30000)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var cmdResp struct {
		Sessions []struct {
			ID         string `json:"id"`
			WorkingDir string `json:"working_dir"`
			CreatedAt  string `json:"created_at"`
			LastActive string `json:"last_active"`
			PID        int    `json:"pid"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(respBytes, &cmdResp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))
	}

	sessions := make([]*reliantv1.TerminalSession, len(cmdResp.Sessions))
	for i, s := range cmdResp.Sessions {
		session := &reliantv1.TerminalSession{
			Id:         s.ID,
			WorkingDir: s.WorkingDir,
			CreatedAt:  s.CreatedAt,
			LastActive: s.LastActive,
		}
		if s.PID > 0 {
			pid := int32(s.PID)
			session.Pid = &pid
		}
		sessions[i] = session
	}

	return connect.NewResponse(&reliantv1.ListTerminalSessionsResponse{
		Sessions: sessions,
		Total:    int32(len(sessions)),
	}), nil
}

// CloseSession closes a specific terminal session.
func (s *TerminalProxyService) CloseSession(
	ctx context.Context,
	req *connect.Request[reliantv1.CloseTerminalSessionRequest],
) (*connect.Response[reliantv1.CloseTerminalSessionResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	if req.Msg.SessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("session_id is required"))
	}

	cmdReq := map[string]any{
		"session_id": req.Msg.SessionId,
	}
	payload, err := json.Marshal(cmdReq)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("marshal request: %w", err))
	}

	respBytes, err := s.router.SendDaemonCommand(ctx, userID, "terminal.close", payload, 30000)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	var cmdResp struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(respBytes, &cmdResp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal response: %w", err))
	}

	msg := "Session closed successfully"
	if !cmdResp.Success {
		msg = "Failed to close session"
	}

	return connect.NewResponse(&reliantv1.CloseTerminalSessionResponse{
		Success: cmdResp.Success,
		Message: msg,
	}), nil
}

// StreamTerminal implements bidi streaming terminal I/O by forwarding data
// between the browser stream and the daemon's terminal session via the router.
func (s *TerminalProxyService) StreamTerminal(
	ctx context.Context,
	stream *connect.BidiStream[reliantv1.TerminalStreamInput, reliantv1.TerminalStreamOutput],
) error {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnauthenticated, err)
	}

	// Wait for the first message — must be a Create request.
	firstMsg, err := stream.Receive()
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("failed to receive initial message: %w", err))
	}

	createReq := firstMsg.GetCreate()
	if createReq == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("first message must be a Create request"))
	}

	// Create the terminal session via DaemonCommand.
	cmdReq := map[string]any{
		"working_dir": createReq.GetWorkingDir(),
	}
	payload, err := json.Marshal(cmdReq)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("marshal create request: %w", err))
	}

	respBytes, err := s.router.SendDaemonCommand(ctx, userID, "terminal.create", payload, 30000)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("create terminal session: %w", err))
	}

	var createResp struct {
		SessionID string `json:"session_id"`
		PID       int32  `json:"pid"`
	}
	if err := json.Unmarshal(respBytes, &createResp); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("unmarshal create response: %w", err))
	}

	sessionID := createResp.SessionID

	// Send the Created response to the client.
	if err := stream.Send(&reliantv1.TerminalStreamOutput{
		Output: &reliantv1.TerminalStreamOutput_Created{
			Created: &reliantv1.TerminalSessionCreated{
				SessionId: sessionID,
				Pid:       createResp.PID,
			},
		},
	}); err != nil {
		return err
	}

	// Subscribe to terminal output from the daemon.
	outputCh, unsub, err := s.router.SubscribeTerminalOutput(ctx, userID, sessionID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("subscribe terminal output: %w", err))
	}
	defer unsub()

	// Use a cancellable context so either pump can stop the other.
	pumpCtx, pumpCancel := context.WithCancel(ctx)
	defer pumpCancel()

	var wg sync.WaitGroup
	wg.Add(2)

	// Shared error from first pump to fail.
	var pumpErr error
	var errOnce sync.Once

	setPumpErr := func(e error) {
		errOnce.Do(func() {
			pumpErr = e
			pumpCancel()
		})
	}

	// Output pump: daemon output -> browser stream.
	go func() {
		defer wg.Done()
		for {
			select {
			case <-pumpCtx.Done():
				return
			case evt, ok := <-outputCh:
				if !ok {
					// Channel closed — subscription ended.
					setPumpErr(nil)
					return
				}
				if evt.Error != "" {
					_ = stream.Send(&reliantv1.TerminalStreamOutput{
						Output: &reliantv1.TerminalStreamOutput_Error{
							Error: &reliantv1.TerminalError{
								SessionId: sessionID,
								Message:   evt.Error,
							},
						},
					})
					setPumpErr(nil)
					return
				}
				if evt.Closed {
					_ = stream.Send(&reliantv1.TerminalStreamOutput{
						Output: &reliantv1.TerminalStreamOutput_Closed{
							Closed: &reliantv1.TerminalSessionClosed{
								SessionId: sessionID,
								ExitCode:  evt.ExitCode,
							},
						},
					})
					setPumpErr(nil)
					return
				}
				if len(evt.Data) > 0 {
					if err := stream.Send(&reliantv1.TerminalStreamOutput{
						Output: &reliantv1.TerminalStreamOutput_Data{
							Data: &reliantv1.TerminalDataOutput{
								SessionId: sessionID,
								Data:      evt.Data,
							},
						},
					}); err != nil {
						setPumpErr(err)
						return
					}
				}
			}
		}
	}()

	// Input pump: browser stream -> daemon.
	go func() {
		defer wg.Done()
		for {
			msg, err := stream.Receive()
			if err != nil {
				if errors.Is(err, io.EOF) || pumpCtx.Err() != nil {
					setPumpErr(nil)
				} else {
					setPumpErr(err)
				}
				return
			}

			switch input := msg.GetInput().(type) {
			case *reliantv1.TerminalStreamInput_Data:
				if err := s.router.SendTerminalInput(pumpCtx, userID, sessionID, input.Data.GetData()); err != nil {
					setPumpErr(fmt.Errorf("send terminal input: %w", err))
					return
				}
			case *reliantv1.TerminalStreamInput_Resize:
				if err := s.router.SendTerminalResize(pumpCtx, userID, sessionID, input.Resize.GetCols(), input.Resize.GetRows()); err != nil {
					setPumpErr(fmt.Errorf("send terminal resize: %w", err))
					return
				}
			case *reliantv1.TerminalStreamInput_CloseSession:
				// Send close command to daemon and finish.
				closeReq := map[string]any{
					"session_id": sessionID,
				}
				closePayload, err := json.Marshal(closeReq)
				if err == nil {
					_, _ = s.router.SendDaemonCommand(pumpCtx, userID, "terminal.close", closePayload, 30000)
				}
				setPumpErr(nil)
				return
			}
		}
	}()

	wg.Wait()
	return pumpErr
}
