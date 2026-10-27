package settings

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	"github.com/reliant-labs/reliant/internal/db"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/logging"
)

// ============================================================================
// Sub-phase 6c: Prompts & Active Settings
// ============================================================================

// GetPrompts retrieves global user prompts
func (s *Service) GetPrompts(ctx context.Context, req *connect.Request[reliantv1.GetPromptsRequest]) (*connect.Response[reliantv1.GetPromptsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	setting, err := s.deps.Repo.GetSetting(ctx, userID, nil, "global.prompts")
	if err != nil {
		// Return empty prompts if not found
		return connect.NewResponse(&reliantv1.GetPromptsResponse{
			Prompts: []*reliantv1.UserPrompt{},
		}), nil
	}

	var prompts []struct {
		ID       string  `json:"id"`
		Title    string  `json:"title"`
		Content  string  `json:"content"`
		Default  *bool   `json:"default,omitempty"`
		Hotkey   *string `json:"hotkey,omitempty"`
		Category *string `json:"category,omitempty"`
	}

	if setting.Value != "" {
		if err := json.Unmarshal([]byte(setting.Value), &prompts); err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to parse prompts"))
		}
	}

	protoPrompts := make([]*reliantv1.UserPrompt, len(prompts))
	for i, p := range prompts {
		protoPrompts[i] = &reliantv1.UserPrompt{
			Id:       p.ID,
			Title:    p.Title,
			Content:  p.Content,
			Default:  p.Default,
			Hotkey:   p.Hotkey,
			Category: p.Category,
		}
	}

	return connect.NewResponse(&reliantv1.GetPromptsResponse{
		Prompts: protoPrompts,
	}), nil
}

// SavePrompts saves global user prompts
func (s *Service) SavePrompts(ctx context.Context, req *connect.Request[reliantv1.SavePromptsRequest]) (*connect.Response[reliantv1.SavePromptsResponse], error) {
	userID := auth.MustGetUserID(ctx)

	// Convert proto prompts to JSON-serializable struct
	prompts := make([]map[string]interface{}, len(req.Msg.Prompts))
	for i, p := range req.Msg.Prompts {
		prompt := map[string]interface{}{
			"id":      p.Id,
			"title":   p.Title,
			"content": p.Content,
		}
		if p.Default != nil {
			prompt["default"] = *p.Default
		}
		if p.Hotkey != nil {
			prompt["hotkey"] = *p.Hotkey
		}
		if p.Category != nil {
			prompt["category"] = *p.Category
		}
		prompts[i] = prompt
	}

	promptsJSON, err := json.Marshal(prompts)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to serialize prompts"))
	}

	if err := s.upsertSetting(ctx, userID, nil, "global.prompts", string(promptsJSON)); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to save prompts"))
	}

	if err := s.deps.Repo.EmitUserRefetch(ctx, userID, db.RefetchConfigHealth, db.RefetchOpts{}); err != nil {
		logging.WarnContext(ctx, "Failed to emit config_health refetch after SavePrompts", "error", err)
	}

	return connect.NewResponse(&reliantv1.SavePromptsResponse{
		Success: true,
		Message: "Prompts saved successfully",
		Prompts: req.Msg.Prompts,
	}), nil
}
