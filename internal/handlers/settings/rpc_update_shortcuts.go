package settings

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// UpdateShortcuts updates user keyboard shortcuts
func (s *Service) UpdateShortcuts(ctx context.Context, req *connect.Request[reliantv1.UpdateShortcutsRequest]) (*connect.Response[reliantv1.UpdateShortcutsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.upsertSetting(ctx, userID, nil, "shortcuts", req.Msg.Shortcuts); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to update shortcuts"))
	}

	return connect.NewResponse(&reliantv1.UpdateShortcutsResponse{
		Success: true,
		Message: "Shortcuts updated successfully",
	}), nil
}
