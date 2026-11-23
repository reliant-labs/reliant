package worktree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

func (s *Service) restoreRecovery(ctx context.Context, req *pb.RestoreWorktreeRecoveryRequest) (*pb.RestoreWorktreeRecoveryResponse, error) {
	userID := auth.MustGetUserID(ctx)
	if req.RecoveryId == "" || req.TargetDaemonId == "" || req.IdempotencyKey == "" {
		return nil, svcerr.InvalidArgument("recovery_id, target_daemon_id and idempotency_key are required")
	}
	row, err := s.deps.Recoveries.GetWorktreeRecovery(ctx, userID, req.RecoveryId)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	target, err := s.getDaemon(ctx, req.TargetDaemonId)
	if err != nil || !authz.CanWriteMachine(ctx, userID, target) {
		return nil, svcerr.NotFound("destination machine")
	}
	project, err := s.deps.Projects.GetProjectWithUserCheck(ctx, row.ProjectID, userID)
	if err != nil || project == nil {
		return nil, svcerr.FailedPrecondition("Install the source project on the destination machine before restoring this retained recovery")
	}
	installations, err := s.deps.Repo.ListProjectDaemonsForProject(ctx, project.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	destinationRoot := recoveryInstallationPath(installations, req.TargetDaemonId)
	if destinationRoot == "" {
		return nil, svcerr.FailedPrecondition("Install the source project on the destination machine before restoring this retained recovery")
	}
	captures, err := recoveryCaptures(row)
	if err != nil {
		return nil, err
	}
	repos, err := s.listRepos(ctx, project.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	registered := map[string]string{}
	for _, repo := range repos {
		for _, c := range captures {
			if repo.ID == c.RepoID && repo.RemoteURL != nil && *repo.RemoteURL == c.RemoteURL && filepath.ToSlash(filepath.Clean(repo.RelativePath)) == filepath.ToSlash(filepath.Clean(c.Rel)) {
				registered[repo.ID] = filepath.Join(destinationRoot, repo.RelativePath)
			}
		}
	}
	if len(registered) != len(captures) {
		return nil, svcerr.FailedPrecondition("Registered destination repository identities do not match the recovery")
	}
	if err := s.deps.Recoveries.BeginWorktreeRecoveryOperation(ctx, userID, row.ID, "restoring", req.IdempotencyKey, req.TargetDaemonId); err != nil {
		return nil, svcerr.Wrap(err)
	}
	// A stable key binds destination daemon too: retries never migrate a partially
	// restored operation to another machine, row, branch or path.
	key := sha256.Sum256([]byte(row.ID + "\x00" + req.IdempotencyKey))
	suffix := hex.EncodeToString(key[:16])
	branch := "reliant-restored/" + suffix
	idem := "recovery:" + row.ID + ":" + req.IdempotencyKey
	wt, err := s.deps.Rows.GetWorktreeByIdempotencyKey(ctx, project.ID, idem)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if wt != nil && (wt.ProjectID != project.ID || wt.Branch != branch || wt.DaemonID == nil || *wt.DaemonID != req.TargetDaemonId) {
		return nil, svcerr.FailedPrecondition("This restore operation is already bound to a different destination machine")
	}
	if row.Phase == "restored" && row.Operation == "restoring" && row.OperationKey == req.IdempotencyKey && row.TargetDaemonID == req.TargetDaemonId {
		// A completed operation is immutable. The user may already be working
		// in the result: a lost-response retry must not revalidate their edits,
		// downgrade the active worktree or replay reconstruction.
		if wt == nil || wt.Status != int32(pb.WorktreeStatus_WORKTREE_STATUS_ACTIVE) || wt.Path == "" {
			return nil, svcerr.FailedPrecondition("The completed recovery workspace is no longer available; retained recovery branches were not changed")
		}
		return &pb.RestoreWorktreeRecoveryResponse{Recovery: recoveryToProto(row), WorktreeId: wt.ID}, nil
	}
	if wt == nil {
		now := time.Now()
		wt = &db.Worktree{ID: uuid.NewString(), Name: "Recovered " + suffix[:8], Branch: branch, ProjectID: project.ID, DaemonID: &req.TargetDaemonId, IdempotencyKey: &idem, Status: int32(pb.WorktreeStatus_WORKTREE_STATUS_CREATING), CreatedAt: now, UpdatedAt: now, LastActive: now}
		if createErr := s.deps.Repo.CreateWorktree(ctx, wt); createErr != nil {
			// Another same-key request may have inserted first. Only accept that
			// exact operation's existing row; never update an unrelated workspace.
			existing, loadErr := s.deps.Rows.GetWorktreeByIdempotencyKey(ctx, project.ID, idem)
			if loadErr != nil || existing == nil || existing.ProjectID != project.ID || existing.Branch != branch || existing.DaemonID == nil || *existing.DaemonID != req.TargetDaemonId {
				return nil, svcerr.Wrap(createErr)
			}
			wt = existing
		}
	}
	token, err := s.deps.GitCredentials.UserAccessToken(ctx, userID, "github")
	if err != nil {
		return nil, svcerr.FailedPrecondition("Reconnect GitHub to read this recovery")
	}
	var response worktreereclaim.RecoveryRestoreResponse
	err = token.Use(func(secret string) error {
		return s.sendWorktreeDaemonCommandTimeout(ctx, userID, req.TargetDaemonId, "worktree.recovery_restore", struct {
			worktreereclaim.RecoveryRestoreRequest
			Repositories map[string]string `json:"repositories"`
			Token        string            `json:"token"`
		}{worktreereclaim.RecoveryRestoreRequest{RecoveryID: row.ID, IdempotencyKey: req.IdempotencyKey, Branch: branch, Captures: captures}, registered, secret}, &response, 600000)
	})
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	if response.Outcome != worktreereclaim.OutcomeRestored {
		detail := response.Detail
		if detail == "" {
			detail = "Recovery restore held; originals and recovery branches retained"
		}
		wt.Status = int32(pb.WorktreeStatus_WORKTREE_STATUS_FAILED)
		wt.FailureReason = detail
		if err := s.deps.Repo.UpdateWorktree(ctx, wt); err != nil {
			return nil, svcerr.Wrap(err)
		}
		for _, c := range captures {
			if err := s.deps.Recoveries.CompleteWorktreeRecoveryCheckout(ctx, userID, row.ID, c.RepoID, "held", detail); err != nil {
				return nil, svcerr.Wrap(err)
			}
		}
		row, err = s.deps.Recoveries.GetWorktreeRecovery(ctx, userID, row.ID)
		if err != nil {
			return nil, svcerr.Wrap(err)
		}
		return &pb.RestoreWorktreeRecoveryResponse{Recovery: recoveryToProto(row)}, nil
	}
	if response.Path == "" || response.Branch != branch || len(response.RestoredRepoIDs) != len(captures) {
		return nil, svcerr.FailedPrecondition("Destination did not confirm the complete recovered workspace")
	}
	confirmed := map[string]bool{}
	for _, id := range response.RestoredRepoIDs {
		if confirmed[id] {
			return nil, svcerr.FailedPrecondition("Duplicate restored checkout confirmation")
		}
		confirmed[id] = true
	}
	for _, c := range captures {
		if !confirmed[c.RepoID] {
			return nil, svcerr.FailedPrecondition("Missing restored checkout confirmation")
		}
	}
	wt.Path = response.Path
	wt.Status = int32(pb.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	wt.FailureReason = ""
	if err := s.deps.Repo.UpdateWorktree(ctx, wt); err != nil {
		return nil, svcerr.Wrap(err)
	}
	for _, c := range captures {
		if err := s.deps.Recoveries.CompleteWorktreeRecoveryCheckout(ctx, userID, row.ID, c.RepoID, "restored", ""); err != nil {
			return nil, svcerr.Wrap(err)
		}
	}
	row, err = s.deps.Recoveries.GetWorktreeRecovery(ctx, userID, row.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return &pb.RestoreWorktreeRecoveryResponse{Recovery: recoveryToProto(row), WorktreeId: wt.ID}, nil
}

func (s *Service) RestoreWorktreeRecovery(ctx context.Context, req *connect.Request[pb.RestoreWorktreeRecoveryRequest]) (*connect.Response[pb.RestoreWorktreeRecoveryResponse], error) {
	out, err := s.restoreRecovery(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(out), nil
}
