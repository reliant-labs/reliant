package project

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// GetProjectPrompts retrieves project-scoped prompts
func (s *Service) GetProjectPrompts(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectPromptsRequest],
) (*connect.Response[reliantv1.GetProjectPromptsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	projectID := req.Msg.ProjectId
	setting, err := s.deps.Repo.GetSetting(ctx, userID, &projectID, "project.prompts")
	if err != nil {
		// Return empty prompts if not found
		return connect.NewResponse(&reliantv1.GetProjectPromptsResponse{
			Prompts: []*reliantv1.Prompt{},
		}), nil
	}

	var prompts []promptData
	if setting.Value != "" {
		if err := json.Unmarshal([]byte(setting.Value), &prompts); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to parse prompts"))
		}
	}

	protoPrompts := make([]*reliantv1.Prompt, len(prompts))
	for i, p := range prompts {
		protoPrompts[i] = &reliantv1.Prompt{
			Id:          p.ID,
			Name:        p.Name,
			Content:     p.Content,
			Description: p.Description,
			CreatedAt:   p.CreatedAt,
			UpdatedAt:   p.UpdatedAt,
		}
	}

	return connect.NewResponse(&reliantv1.GetProjectPromptsResponse{
		Prompts: protoPrompts,
	}), nil
}
