package settings

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/logging"
)

// DeleteSetting deletes a setting by key
func (s *Service) DeleteSetting(ctx context.Context, req *connect.Request[reliantv1.DeleteSettingRequest]) (*connect.Response[reliantv1.DeleteSettingResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if req.Msg.Key == "" {
		return nil, svcerr.InvalidArgument("key is required")
	}
	if err := guardSettingWrite(ctx, req.Msg.Key); err != nil {
		return nil, err
	}

	var projectID *string
	if req.Msg.ProjectId != nil {
		projectID = req.Msg.ProjectId
	}

	setting, err := s.deps.Repo.GetSetting(ctx, userID, projectID, req.Msg.Key)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get setting"))
	}

	if err := s.deps.Repo.DeleteSetting(ctx, setting.ID); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to delete setting"))
	}

	if err := s.deps.Refetch.EmitUserRefetch(ctx, userID, core.RefetchConfigHealth, core.RefetchOpts{ProjectID: projectID}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after DeleteSetting", "error", err)
	}

	return connect.NewResponse(&reliantv1.DeleteSettingResponse{
		Success: true,
		Message: "Setting deleted successfully",
	}), nil
}
