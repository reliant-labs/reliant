package project

import (
	"context"
	"encoding/json"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// SaveProjectPrompts saves project-scoped prompts
func (s *Service) SaveProjectPrompts(
	ctx context.Context,
	req *connect.Request[reliantv1.SaveProjectPromptsRequest],
) (*connect.Response[reliantv1.SaveProjectPromptsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	// Convert proto prompts to data structure
	prompts := make([]promptData, len(req.Msg.Prompts))
	for i, p := range req.Msg.Prompts {
		prompts[i] = promptData{
			ID:          p.Id,
			Name:        p.Name,
			Content:     p.Content,
			Description: p.Description,
			CreatedAt:   p.CreatedAt,
			UpdatedAt:   p.UpdatedAt,
		}
	}

	// Marshal prompts to JSON
	promptsJSON, err := json.Marshal(prompts)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to serialize prompts"))
	}

	projectID := req.Msg.ProjectId

	// Use upsert pattern: try to get existing, then create or update
	setting, err := s.deps.Repo.GetSetting(ctx, userID, &projectID, "project.prompts")
	if err != nil {
		// Create new setting
		newSetting := &db.Setting{
			ID:        uuid.New().String(),
			UserID:    userID,
			ProjectID: &projectID,
			Key:       "project.prompts",
			Value:     string(promptsJSON),
			ValueType: "string",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if err := s.deps.Repo.CreateSetting(ctx, newSetting); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save prompts"))
		}
	} else {
		// Update existing setting
		setting.Value = string(promptsJSON)
		setting.UpdatedAt = time.Now().UTC()
		if err := s.deps.Repo.UpdateSetting(ctx, setting); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save prompts"))
		}
	}

	return connect.NewResponse(&reliantv1.SaveProjectPromptsResponse{
		Message: "Prompts saved successfully",
		Prompts: req.Msg.Prompts,
	}), nil
}
