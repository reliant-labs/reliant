package inbox

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

// Marked worktrees (worktreesweep mark.go): a live worktree quiet for a day
// whose machine placed a reclaim tombstone in it. The item says when it will
// be reclaimed; the person may keep it or reclaim it now. Nothing has to be
// done to keep a worktree that is used: any use cancels the mark.

// markedItemID is a marked worktree's item id: the worktree and its mark.
func markedItemID(worktreeID, markID string) string {
	return inboxWorktreeMarkedPrefix + worktreeID + ":" + markID
}

// markedItems lists the caller's marked live worktrees as items, nearest
// reclaim first.
func (s *Service) markedItems(ctx context.Context, userID string) ([]*pb.InboxItem, error) {
	if s.deps.Sweeper == nil {
		return nil, nil
	}
	rows, err := s.deps.Repo.ListMarkedWorktreesForUser(ctx, userID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	names := map[string]string{}
	if daemons, err := s.deps.Repo.ListDaemonsByUserID(ctx, userID); err == nil {
		for _, d := range daemons {
			if d.Hostname != nil {
				names[d.ID] = *d.Hostname
			}
		}
	}
	out := make([]*pb.InboxItem, 0, len(rows))
	for _, h := range rows {
		wt, m := h.Worktree, h.Worktree.CleanupMetadata
		if !m.Marked() || m.ReclaimMarkedAt == nil {
			continue
		}
		daemonID := ""
		if wt.DaemonID != nil {
			daemonID = *wt.DaemonID
		}
		payload := &pb.InboxWorktreeMarked{
			WorktreeId: wt.ID, Name: wt.Name, Path: wt.Path, Branch: wt.Branch,
			DaemonId: daemonID, DaemonName: names[daemonID],
			MarkId: m.ReclaimMarkID, MarkedAt: m.ReclaimMarkedAt.UTC().Format(time.RFC3339),
			Reason: m.ReclaimMarkReason, Approved: m.ReclaimApprovedAt != nil,
		}
		if at := s.deps.Sweeper.GraceFor(wt); !at.IsZero() {
			payload.ReclaimAt = at.UTC().Format(time.RFC3339)
		}
		out = append(out, &pb.InboxItem{
			Kind:         pb.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_MARKED,
			ItemId:       markedItemID(wt.ID, m.ReclaimMarkID),
			ProjectId:    wt.ProjectID,
			ProjectName:  h.ProjectName,
			WaitingSince: m.ReclaimMarkedAt.UTC().Format(time.RFC3339Nano),
			Payload:      &pb.InboxItem_WorktreeMarked{WorktreeMarked: payload},
		})
	}
	return out, nil
}

// KeepMarkedWorktree cancels the mark the person saw (Sweeper.KeepMarked).
func (s *Service) KeepMarkedWorktree(
	ctx context.Context,
	req *connect.Request[pb.KeepMarkedWorktreeRequest],
) (*connect.Response[pb.KeepMarkedWorktreeResponse], error) {
	userID, err := s.markDecision(ctx, req.Msg.WorktreeId, req.Msg.MarkId)
	if err != nil {
		return nil, err
	}
	if err := markDecisionError(s.deps.Sweeper.KeepMarked(ctx, s.deps.Repo, userID, req.Msg.WorktreeId, req.Msg.MarkId)); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.KeepMarkedWorktreeResponse{}), nil
}

// ReclaimMarkedWorktreeNow skips the wait of the mark the person saw
// (Sweeper.ReclaimMarkedNow).
func (s *Service) ReclaimMarkedWorktreeNow(
	ctx context.Context,
	req *connect.Request[pb.ReclaimMarkedWorktreeNowRequest],
) (*connect.Response[pb.ReclaimMarkedWorktreeNowResponse], error) {
	userID, err := s.markDecision(ctx, req.Msg.WorktreeId, req.Msg.MarkId)
	if err != nil {
		return nil, err
	}
	if err := markDecisionError(s.deps.Sweeper.ReclaimMarkedNow(ctx, s.deps.Repo, userID, req.Msg.WorktreeId, req.Msg.MarkId)); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.ReclaimMarkedWorktreeNowResponse{}), nil
}

// markDecision checks what both decisions need: a caller, the ids, a sweeper.
func (s *Service) markDecision(ctx context.Context, worktreeID, markID string) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", svcerr.Unauthenticated("user ID not found in context")
	}
	if worktreeID == "" || markID == "" {
		return "", svcerr.InvalidArgument("worktree_id and mark_id are required")
	}
	if s.deps.Sweeper == nil {
		return "", svcerr.Unavailable("worktree reclaim is not available")
	}
	return userID, nil
}

func markDecisionError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, worktreesweep.ErrNotMarked):
		return svcerr.NotFound("marked workspace")
	case errors.Is(err, worktreesweep.ErrMarkChanged):
		return svcerr.FailedPrecondition("the workspace's reclaim mark changed since it was shown; refresh and decide again")
	default:
		return svcerr.WrapOr(err, svcerr.Internal("could not record the decision"))
	}
}
