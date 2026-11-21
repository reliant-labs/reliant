package settings

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ============================================================================
// Sub-phase 6b: UI Settings (Shortcuts/Preferences)
// ============================================================================

// GetShortcuts retrieves user keyboard shortcuts
func (s *Service) GetShortcuts(ctx context.Context, req *connect.Request[reliantv1.GetShortcutsRequest]) (*connect.Response[reliantv1.GetShortcutsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	setting, err := s.deps.Repo.GetSetting(ctx, userID, nil, "shortcuts")
	if err != nil {
		// Return empty shortcuts if not found (frontend will use defaults)
		return connect.NewResponse(&reliantv1.GetShortcutsResponse{
			Shortcuts: "",
		}), nil
	}

	return connect.NewResponse(&reliantv1.GetShortcutsResponse{
		Shortcuts: setting.Value,
	}), nil
}
