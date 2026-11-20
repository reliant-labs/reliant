package settings

import (
	"context"
	"encoding/json"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	"github.com/reliant-labs/reliant/internal/db"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/logging"
)

// SavePrompts saves global user prompts
func (s *Service) SavePrompts(ctx context.Context, req *reliantv1.SavePromptsRequest) (*reliantv1.SavePromptsResponse, error) {
	userID := auth.MustGetUserID(ctx)

	// Convert proto prompts to JSON-serializable struct
	prompts := make([]map[string]interface{}, len(req.Prompts))
	for i, p := range req.Prompts {
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

	return &reliantv1.SavePromptsResponse{
		Success: true,
		Message: "Prompts saved successfully",
		Prompts: req.Prompts,
	}, nil
}
