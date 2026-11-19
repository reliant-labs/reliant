package settings

import (
	"context"
	"encoding/json"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
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
