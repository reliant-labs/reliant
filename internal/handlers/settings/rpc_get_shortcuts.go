package settings

import (
	"context"

	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ============================================================================
// Sub-phase 6b: UI Settings (Shortcuts/Preferences)
// ============================================================================

// GetShortcuts retrieves user keyboard shortcuts
func (s *Service) GetShortcuts(ctx context.Context, req *reliantv1.GetShortcutsRequest) (*reliantv1.GetShortcutsResponse, error) {
	userID := auth.MustGetUserID(ctx)

	setting, err := s.deps.Repo.GetSetting(ctx, userID, nil, "shortcuts")
	if err != nil {
		// Return empty shortcuts if not found (frontend will use defaults)
		return &reliantv1.GetShortcutsResponse{
			Shortcuts: "",
		}, nil
	}

	return &reliantv1.GetShortcutsResponse{
		Shortcuts: setting.Value,
	}, nil
}
