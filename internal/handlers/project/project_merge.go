package project

import (
	"context"
	"errors"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Merging two of the caller's projects (MergeProjects, PreviewProjectMerge).
// The store does the merge (store.MergeProjects); here is who may ask for it.

// ReasonProjectMergeBlocked (x-forge-error-reason): work in the projects, or
// a machine both are checked out on, refuses the merge now. The preview
// lists each blocker.
const ReasonProjectMergeBlocked = "project_merge_blocked"

// mergeableProjects admits a merge of sourceID into targetID: two different
// projects, both the caller's own (owned by them, and writable by them:
// authz.CanWriteProject, which a project scheduled for deletion is not), in
// one organization. A project the caller does not own, cannot write or that
// is scheduled for deletion is NotFound, as for any project they may not
// write; an organization membership that cannot be read now is Unavailable.
func (s *Service) mergeableProjects(ctx context.Context, userID, sourceID, targetID string) (source, target *db.Project, err error) {
	if sourceID == "" || targetID == "" {
		return nil, nil, svcerr.InvalidArgument("source_project_id and target_project_id are required")
	}
	if sourceID == targetID {
		return nil, nil, core.ErrProjectMergeSameProject
	}
	if source, err = s.ownWritableProject(ctx, userID, sourceID); err != nil {
		return nil, nil, err
	}
	if target, err = s.ownWritableProject(ctx, userID, targetID); err != nil {
		return nil, nil, err
	}
	if orgKey(source.OrgID) != orgKey(target.OrgID) {
		return nil, nil, core.ErrProjectMergeOtherOrg
	}
	return source, target, nil
}

// ownWritableProject reads id and admits it only when userID owns it and may
// write it.
func (s *Service) ownWritableProject(ctx context.Context, userID, id string) (*db.Project, error) {
	p, err := s.deps.ProjectRows.GetProject(ctx, id)
	if err != nil {
		if svcerr.IsNotFound(err) {
			return nil, core.ErrProjectNotFound
		}
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load the project"))
	}
	if p.UserID != userID {
		return nil, core.ErrProjectNotFound
	}
	if err := authz.WriteProjectDenial(ctx, userID, p); err != nil {
		if errors.Is(err, authz.ErrMembershipUnavailable) {
			return nil, svcerr.WithCause(svcerr.Unavailable("could not verify organization membership; try again"), err)
		}
		return nil, svcerr.WithCause(core.ErrProjectNotFound, err)
	}
	return p, nil
}

// orgKey is a project's org as COALESCE(org_id, ”): a project with none is
// in one "organization" with every other project of its owner that has none.
func orgKey(org *string) string {
	if org == nil {
		return ""
	}
	return *org
}

// projectMergeError is the answer to a store refusal: blockers become one
// FailedPrecondition naming each; anything else is classified as svcerr
// declares it.
func projectMergeError(err error, blockers []core.ProjectMergeBlocker, fallback string) error {
	if errors.Is(err, core.ErrProjectMergeBlocked) && len(blockers) > 0 {
		msgs := make([]string, 0, len(blockers))
		for _, b := range blockers {
			msgs = append(msgs, b.Message)
		}
		return svcerr.WithReason(svcerr.WithCause(
			svcerr.FailedPrecondition("these projects cannot be merged now: "+strings.Join(msgs, "; ")), err),
			ReasonProjectMergeBlocked)
	}
	return svcerr.WrapOr(err, svcerr.Internal(fallback))
}

func (s *Service) projectMergeCountsProto(ctx context.Context, c core.ProjectMergeCounts) *pb.ProjectMergeCounts {
	out := &pb.ProjectMergeCounts{
		Chats:     int32(c.Chats),
		Worktrees: int32(c.Worktrees),
		Triggers:  int32(c.Triggers),
		Repos:     int32(c.Repos),
		OtherRows: int32(c.OtherRows),
		Checkouts: []*pb.ProjectCheckout{},
	}
	for _, pd := range c.Checkouts {
		out.Checkouts = append(out.Checkouts, &pb.ProjectCheckout{
			DaemonId:     pd.DaemonID,
			DaemonName:   s.machineName(ctx, pd.DaemonID),
			InstallState: installStateToProto(pd.InstallState),
		})
	}
	return out
}

func projectMergeBlockersProto(blockers []core.ProjectMergeBlocker) []*pb.ProjectMergeBlocker {
	out := make([]*pb.ProjectMergeBlocker, 0, len(blockers))
	for _, b := range blockers {
		pbb := &pb.ProjectMergeBlocker{Message: b.Message}
		switch b.Kind {
		case core.ProjectMergeRunningWork:
			pbb.Kind = pb.ProjectMergeBlockerKind_PROJECT_MERGE_BLOCKER_KIND_RUNNING_WORK
		case core.ProjectMergeSameMachine:
			pbb.Kind = pb.ProjectMergeBlockerKind_PROJECT_MERGE_BLOCKER_KIND_SAME_MACHINE
		}
		if b.DaemonID != "" {
			pbb.DaemonId = &b.DaemonID
		}
		if b.ChatID != "" {
			pbb.ChatId = &b.ChatID
		}
		out = append(out, pbb)
	}
	return out
}
