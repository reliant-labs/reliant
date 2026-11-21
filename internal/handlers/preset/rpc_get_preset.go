package preset

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// GetPreset returns a specific preset by name.
// Priority: user (database) > project (stored config) > builtin
func (s *Service) GetPreset(
	ctx context.Context,
	req *connect.Request[reliantv1.GetPresetRequest],
) (*connect.Response[reliantv1.GetPresetResponse], error) {
	if req.Msg.ProjectId == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if req.Msg.Name == "" {
		return nil, svcerr.InvalidArgument("preset name is required")
	}

	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	// Generate slug from name for database lookup
	slug := strings.ToLower(strings.ReplaceAll(req.Msg.Name, " ", "-"))

	// 1. First check database for user preset (highest priority)
	dbPreset, err := s.deps.Repo.GetPresetBySlug(ctx, userID, slug)
	if err == nil && dbPreset != nil {
		return connect.NewResponse(&reliantv1.GetPresetResponse{
			Preset: dbPresetToProto(dbPreset),
		}), nil
	}

	// 2. Fall back to stored project presets / builtins
	p, err := s.loadPresetByNameFromDB(ctx, req.Msg.ProjectId, req.Msg.Name)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}

	protoPreset, err := presetToProto(p)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to convert preset"))
	}

	return connect.NewResponse(&reliantv1.GetPresetResponse{
		Preset: protoPreset,
	}), nil
}
