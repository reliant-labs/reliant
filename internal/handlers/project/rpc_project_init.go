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
	"github.com/reliant-labs/reliant/internal/logging"
)

// GetProjectInitStatus returns the initialization status of a project
func (s *Service) GetProjectInitStatus(
	ctx context.Context,
	req *connect.Request[reliantv1.GetProjectInitStatusRequest],
) (*connect.Response[reliantv1.GetProjectInitStatusResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	// Check initialization status via daemon (filesystem checks)
	var initStatus struct {
		Initialized bool   `json:"initialized"`
		Message     string `json:"message"`
	}
	initPayload := struct {
		Path      string `json:"path"`
		IsGitRepo bool   `json:"is_git_repo"`
	}{Path: project.Path, IsGitRepo: project.IsGitRepo}
	if err := s.sendProjectDaemonCommand(ctx, userID, "project.check_init_status", initPayload, &initStatus); err != nil {
		logging.ErrorContext(ctx, "Failed to check init status via daemon", "error", err, "projectID", req.Msg.ProjectId)
		// Fallback: assume initialized since it exists in the database
		initStatus.Initialized = true
		initStatus.Message = "Project is initialized"
	}

	return connect.NewResponse(&reliantv1.GetProjectInitStatusResponse{
		Initialized: initStatus.Initialized,
		ProjectId:   req.Msg.ProjectId,
		Message:     initStatus.Message,
	}), nil
}

// InitializeProject initializes a project (no-op in V2)
func (s *Service) InitializeProject(
	ctx context.Context,
	req *connect.Request[reliantv1.InitializeProjectRequest],
) (*connect.Response[reliantv1.InitializeProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)

	if err := s.projectBelongsToUser(ctx, req.Msg.ProjectId, userID); err != nil {
		return nil, err
	}

	project, err := s.deps.Repo.GetProject(ctx, req.Msg.ProjectId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get project"))
	}

	// Project initialization is a no-op in V2 (projects are initialized on creation)
	return connect.NewResponse(&reliantv1.InitializeProjectResponse{
		ProjectId:   project.ID,
		Status:      "initialized",
		Message:     "Project already initialized",
		Initialized: true,
	}), nil
}

// Prompt type for JSON marshaling
type promptData struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Content     string `json:"content"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

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
