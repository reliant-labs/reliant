package terminal

import (
	"context"
	"encoding/json"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// terminalSessionNotFound is how the daemon's terminal manager says a session
// id names no live session (terminal.Manager.CloseSession). The daemon
// transport carries a command's error as its message only
// (daemon_router_nats.go), so the text is what reaches this server; daemons
// already in the field send exactly this.
const terminalSessionNotFound = "session not found"

// isTerminalSessionNotFound reports whether a terminal.close failure is the
// daemon having no such session.
func isTerminalSessionNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), terminalSessionNotFound)
}

// CloseSession closes a specific terminal session.
func (s *Service) CloseSession(
	ctx context.Context,
	req *connect.Request[reliantv1.CloseTerminalSessionRequest],
) (*connect.Response[reliantv1.CloseTerminalSessionResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	if req.Msg.SessionId == "" {
		return nil, svcerr.InvalidArgument("session_id is required")
	}

	cmdReq := map[string]any{
		"session_id": req.Msg.SessionId,
	}
	payload, err := json.Marshal(cmdReq)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("marshal request"))
	}

	respBytes, err := sendToMachine(ctx, s.deps.Router, userID, req.Msg.GetDaemonId(), "terminal.close", payload, 30000)
	if err != nil {
		// Closing a session that is already gone has achieved what close
		// asks for. The race is built in: the terminal connection's deferred
		// close (closeDaemonTerminalSession) and the browser's explicit
		// CloseSession both close the same session when a tab is closed,
		// and whichever lands second used to fail as INTERNAL (ELECTRON-97).
		if isTerminalSessionNotFound(err) {
			return connect.NewResponse(&reliantv1.CloseTerminalSessionResponse{
				Success: true,
				Message: "Session already closed",
			}), nil
		}
		return nil, daemonFailure("", "daemon command terminal.close", err)
	}

	var cmdResp struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(respBytes, &cmdResp); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("unmarshal response"))
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
