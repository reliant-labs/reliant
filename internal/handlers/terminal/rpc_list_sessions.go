package terminal

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListSessions returns all active terminal sessions.
func (s *Service) ListSessions(
	ctx context.Context,
	req *connect.Request[reliantv1.ListTerminalSessionsRequest],
) (*connect.Response[reliantv1.ListTerminalSessionsResponse], error) {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return nil, err
	}

	payload, err := json.Marshal(struct{}{})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("marshal request"))
	}

	respBytes, err := s.deps.Router.SendDaemonCommand(ctx, userID, "terminal.list", payload, 30000)
	if err != nil {
		return nil, daemonFailure("", "daemon command terminal.list", err)
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
		return nil, svcerr.WrapOr(err, svcerr.Internal("unmarshal response"))
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
