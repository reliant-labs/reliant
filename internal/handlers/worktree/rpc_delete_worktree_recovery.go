package worktree

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

func (s *Service) DeleteWorktreeRecovery(ctx context.Context, req *connect.Request[pb.DeleteWorktreeRecoveryRequest]) (*connect.Response[pb.DeleteWorktreeRecoveryResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if req.Msg.RecoveryId == "" || req.Msg.IdempotencyKey == "" {
		return nil, svcerr.InvalidArgument("recovery_id and idempotency_key required")
	}
	row, err := s.deps.Recoveries.GetWorktreeRecovery(ctx, userID, req.Msg.RecoveryId)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	captures, err := recoveryCaptures(row)
	if err != nil {
		return nil, err
	}
	if err := s.deps.Recoveries.BeginWorktreeRecoveryOperation(ctx, userID, row.ID, "deletion_requested", req.Msg.IdempotencyKey, ""); err != nil {
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
		if err := s.deps.Recoveries.CompleteWorktreeRecoveryCheckout(ctx, userID, row.ID, c.RepoID, phase, detail); err != nil {
			return nil, svcerr.Wrap(err)
		}
	}
	row, err = s.deps.Recoveries.GetWorktreeRecovery(ctx, userID, row.ID)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return connect.NewResponse(&pb.DeleteWorktreeRecoveryResponse{Recovery: recoveryToProto(row)}), nil
}
