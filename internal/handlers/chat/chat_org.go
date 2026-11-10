// Copyright (c) 2025 Reliant Labs

package chat

import (
	"context"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// newChatOrgs is what a new chat's org is decided from (launch.Spec.OrgID and
// DefaultOrgID): the org the request names, verified, and, when it names
// none, the caller's default. A default that cannot be resolved is "": the
// chat takes its project's org, or none, and is stamped later (orgs.ListScope),
// so starting a chat never fails over an org the request did not name.
func (s *Service) newChatOrgs(ctx context.Context, userID, requested string) (named, def string, err error) {
	if s.deps.Orgs == nil {
		return "", "", nil
	}
	if named, err = s.deps.Orgs.Named(ctx, userID, requested); err != nil || named != "" {
		return named, "", err
	}
	def, err = s.deps.Orgs.DefaultOrg(ctx, userID)
	if err != nil {
		logging.WarnContext(ctx, "[chat] could not resolve the default org; the chat takes its project's or none", "user_id", userID, "error", err)
		return "", "", nil
	}
	return "", def, nil
}

// listScope is the org filter of a chat list (orgs.ListScope): nil when the
// request names no org.
func (s *Service) listScope(ctx context.Context, userID, requested string) (*core.OrgScope, error) {
	if s.deps.Orgs == nil {
		return nil, nil
	}
	return s.deps.Orgs.ListScope(ctx, userID, requested, s.deps.Repo.AssignUnassignedOrg)
}

// readableChats keeps the rows of a chat collection (a list, a search, the
// archive) that authz lets userID read, in order. The store's query filters
// on the owner, and on the org only when the request names one, so a member
// removed from an org would otherwise still list that org's chats they
// created; GetChat refuses those, and a collection must not show them either.
// A chat with no org stays its owner's. A membership that cannot be checked
// drops the org's rows (fail closed, logged by authz).
func readableChats[T any](ctx context.Context, userID string, rows []T, chatOf func(T) *db.Chat) []T {
	kept := make([]T, 0, len(rows))
	for _, row := range rows {
		if authz.CanReadChat(ctx, userID, chatOf(row)) {
			kept = append(kept, row)
		}
	}
	return kept
}
