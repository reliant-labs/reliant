// Copyright (c) 2025 Reliant Labs
package plan

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
)

// getChatIDForPlan looks up the chat (conversation) ID for a plan by resolving its thread.
func (s *Service) getChatIDForPlan(ctx context.Context, plan *db.Plan) (string, error) {
	thread, err := s.deps.Repo.GetThread(ctx, plan.ThreadID)
	if err != nil {
		return "", fmt.Errorf("failed to get thread for plan: %w", err)
	}
	return thread.ChatID, nil
}

// planBelongsToUser checks if a plan belongs to a user via the thread's chat
func (s *Service) planBelongsToUser(ctx context.Context, planID string, userID string) error {
	if planID == "" {
		return svcerr.InvalidArgument("plan_id is required")
	}
	plan, err := s.deps.Repo.GetPlan(ctx, planID)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to get plan"))
	}

	chatID, err := s.getChatIDForPlan(ctx, plan)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	chat, err := s.deps.Repo.GetChatRow(ctx, chatID)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	if !authz.CanWriteChat(ctx, userID, chat) {
		return svcerr.PermissionDenied("access denied")
	}

	return nil
}
