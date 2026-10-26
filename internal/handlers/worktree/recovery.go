package worktree

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// GitCredentialProvider resolves a CURRENT owner token for one operation.
type GitCredentialProvider interface {
	UserAccessToken(context.Context, string, string) (gitcredentialclient.Token, error)
}

func recoveryToProto(row *core.WorktreeRecovery) *pb.WorktreeRecovery {
	out := &pb.WorktreeRecovery{Id: row.ID, SourceWorktreeId: row.SourceWorktreeID, ProjectId: row.ProjectID, DaemonId: row.DaemonID, CaptureId: row.CaptureID, Phase: row.Phase, LastError: row.LastError, CreatedAt: row.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339Nano), Operation: row.Operation, OperationKey: row.OperationKey, TargetDaemonId: row.TargetDaemonID}
	for _, c := range row.Checkouts {
		out.Checkouts = append(out.Checkouts, &pb.WorktreeRecoveryCheckout{RepoId: c.RepoID, Rel: c.Rel, RemoteUrl: c.RemoteURL, RecoveryRef: c.RecoveryRef, CommitOid: c.CommitOID, ManifestDigest: c.ManifestDigest, Phase: c.Phase, LastError: c.LastError})
	}
	return out
}

func (s *Service) ListWorktreeRecoveries(ctx context.Context, req *connect.Request[pb.ListWorktreeRecoveriesRequest]) (*connect.Response[pb.ListWorktreeRecoveriesResponse], error) {
	rows, err := s.deps.Repo.ListWorktreeRecoveries(ctx, auth.MustGetUserID(ctx), req.Msg.GetProjectId())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	out := &pb.ListWorktreeRecoveriesResponse{}
	for _, row := range rows {
		if req.Msg.GetSourceWorktreeId() == "" || row.SourceWorktreeID == req.Msg.GetSourceWorktreeId() {
			out.Recoveries = append(out.Recoveries, recoveryToProto(row))
		}
	}
	return connect.NewResponse(out), nil
}

// recoveryInstallationPath never treats a project's path on one machine as an
// installation on another. The daemon additionally validates the actual clone.
func recoveryInstallationPath(rows []*core.ProjectDaemon, daemonID string) string {
	for _, row := range rows {
		if row.DaemonID == daemonID && row.InstallState == core.ProjectInstallInstalled && filepath.IsAbs(row.Path) && filepath.Clean(row.Path) == row.Path {
			return row.Path
		}
	}
	return ""
}

func recoveryCaptures(row *core.WorktreeRecovery) ([]worktreereclaim.RecoveryCapture, error) {
	var captures []worktreereclaim.RecoveryCapture
	for _, c := range row.Checkouts {
		var capture worktreereclaim.RecoveryCapture
		if json.Unmarshal([]byte(c.DescriptorJSON), &capture) != nil || capture.RepoID != c.RepoID || capture.CommitOID != c.CommitOID || capture.RecoveryRef != c.RecoveryRef || capture.RemoteURL != c.RemoteURL || capture.ManifestDigest != c.ManifestDigest || capture.Rel != c.Rel || capture.CaptureID != row.CaptureID {
			return nil, svcerr.FailedPrecondition("recovery descriptor does not match durable catalog")
		}
		captures = append(captures, capture)
	}
	if len(captures) == 0 {
		return nil, svcerr.FailedPrecondition("recovery has no captured repositories")
	}
	return captures, nil
}

func (s *Service) restoreRecovery(ctx context.Context, req *pb.RestoreWorktreeRecoveryRequest) (*pb.RestoreWorktreeRecoveryResponse, error) {
	userID := auth.MustGetUserID(ctx)
	if req.RecoveryId == "" || req.TargetDaemonId == "" || req.IdempotencyKey == "" {
		return nil, svcerr.InvalidArgument("recovery_id, target_daemon_id and idempotency_key are required")
	}
	row, err := s.deps.Repo.GetWorktreeRecovery(ctx, userID, req.RecoveryId)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	target, err := s.deps.Repo.GetDaemon(ctx, req.TargetDaemonId)
	if err != nil || target == nil || target.UserID != userID {
		return nil, svcerr.NotFound("destination machine")
	}
	project, err := s.deps.Repo.GetProjectWithUserCheck(ctx, row.ProjectID, userID)
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
	repos, err := s.deps.Repo.ListReposByProject(ctx, project.ID)
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
	if err := s.deps.Repo.BeginWorktreeRecoveryOperation(ctx, userID, row.ID, "restoring", req.IdempotencyKey, req.TargetDaemonId); err != nil {
		return nil, svcerr.Wrap(err)
	}
	// A stable key binds destination daemon too: retries never migrate a partially
	// restored operation to another machine, row, branch or path.
	key := sha256.Sum256([]byte(row.ID + "\x00" + req.IdempotencyKey))
	suffix := hex.EncodeToString(key[:16])
	branch := "reliant-restored/" + suffix
	idem := "recovery:" + row.ID + ":" + req.IdempotencyKey
	wt, err := s.deps.Repo.GetWorktreeByIdempotencyKey(ctx, project.ID, idem)
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
			existing, loadErr := s.deps.Repo.GetWorktreeByIdempotencyKey(ctx, project.ID, idem)
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
			if err := s.deps.Repo.CompleteWorktreeRecoveryCheckout(ctx, userID, row.ID, c.RepoID, "held", detail); err != nil {
				return nil, svcerr.Wrap(err)
			}
		}
		row, err = s.deps.Repo.GetWorktreeRecovery(ctx, userID, row.ID)
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
		if err := s.deps.Repo.CompleteWorktreeRecoveryCheckout(ctx, userID, row.ID, c.RepoID, "restored", ""); err != nil {
			return nil, svcerr.Wrap(err)
		}
	}
	row, err = s.deps.Repo.GetWorktreeRecovery(ctx, userID, row.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return &pb.RestoreWorktreeRecoveryResponse{Recovery: recoveryToProto(row), WorktreeId: wt.ID}, nil
}

func (s *Service) DeleteWorktreeRecovery(ctx context.Context, req *connect.Request[pb.DeleteWorktreeRecoveryRequest]) (*connect.Response[pb.DeleteWorktreeRecoveryResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if req.Msg.RecoveryId == "" || req.Msg.IdempotencyKey == "" {
		return nil, svcerr.InvalidArgument("recovery_id and idempotency_key required")
	}
	row, err := s.deps.Repo.GetWorktreeRecovery(ctx, userID, req.Msg.RecoveryId)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	captures, err := recoveryCaptures(row)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Repo.BeginWorktreeRecoveryOperation(ctx, userID, row.ID, "deletion_requested", req.Msg.IdempotencyKey, ""); err != nil {
		return nil, svcerr.Wrap(err)
	}
	// No source filesystem is needed. Any CURRENT owner daemon can perform the
	// exact authenticated Git operation; a deleted source machine is irrelevant.
	if s.deps.Router == nil {
		return nil, svcerr.Unavailable("Connect a machine to delete the exact recovery branches")
	}
	daemonID, err := s.deps.Router.ResolveDaemonID(ctx, userID)
	if err != nil {
		return nil, svcerr.Unavailable("Connect a machine to delete the exact recovery branches")
	}
	token, err := s.deps.GitCredentials.UserAccessToken(ctx, userID, "github")
	if err != nil {
		return nil, svcerr.FailedPrecondition("Reconnect GitHub to delete recovery branches")
	}
	for i, c := range captures {
		if row.Checkouts[i].Phase == "refs_deleted" {
			continue
		}
		var response struct {
			Held    bool   `json:"held"`
			Deleted bool   `json:"deleted"`
			Detail  string `json:"detail"`
		}
		err = token.Use(func(secret string) error {
			return s.sendWorktreeDaemonCommand(ctx, userID, daemonID, "git.recovery_delete", struct {
				Capture worktreereclaim.RecoveryCapture `json:"capture"`
				Token   string                          `json:"token"`
			}{c, secret}, &response)
		})
		phase, detail := "refs_deleted", ""
		if err != nil || response.Held || !response.Deleted {
			phase = "held"
			detail = "Exact recovery branch deletion held; check current repository access, policy and ref identity"
		}
		if err := s.deps.Repo.CompleteWorktreeRecoveryCheckout(ctx, userID, row.ID, c.RepoID, phase, detail); err != nil {
			return nil, svcerr.Wrap(err)
		}
	}
	row, err = s.deps.Repo.GetWorktreeRecovery(ctx, userID, row.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return connect.NewResponse(&pb.DeleteWorktreeRecoveryResponse{Recovery: recoveryToProto(row)}), nil
}

func (s *Service) RestoreWorktreeRecovery(ctx context.Context, req *connect.Request[pb.RestoreWorktreeRecoveryRequest]) (*connect.Response[pb.RestoreWorktreeRecoveryResponse], error) {
	out, err := s.restoreRecovery(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(out), nil
}
