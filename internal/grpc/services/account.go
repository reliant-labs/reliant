// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/accountpurge"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/logging"
)

// retainedElsewhere is what DeleteAccount does NOT remove, phrased for a user
// rather than for us, and returned by PreviewAccountDeletion so the dialog can
// render it verbatim.
//
// It exists because the honest scope of this RPC is narrower than its name.
// Reliant owns the caller's content; the control plane owns their billing,
// wallet, cloud daemons and Supabase identity, and removing those needs the
// cross-repo work described in ACCOUNT_DELETION_DESIGN.md. Saying so in the
// confirmation dialog is the difference between a user who knows to also
// cancel their subscription and one who finds out on their next statement.
//
// The list shrinks as the cross-repo phases land, and the UI needs no change
// because it renders whatever the server sends. The sign-in-identity line is
// gone as of phase 3: control-plane now deletes the GoTrue user itself, so the
// address CAN be reused for a new account. Saying otherwise would be worse
// than saying nothing — it would send users to support for a problem that no
// longer exists.
var retainedElsewhere = []string{
	"Your invoices and billing history are kept as financial records, with your personal details removed.",
	"If you have a paid subscription or unspent credit, deletion will stop and ask you to resolve that first.",
	"Your sign-in identity is removed, so you will be signed out everywhere and this email can be used to sign up again.",
}

// AccountService lets a user delete everything reliant stores about them.
//
// It is a thin Connect surface over internal/accountpurge, which owns the
// ordered delete sequence and the transaction. This layer does auth, the
// confirm-email check, and proto translation — nothing else.
type AccountService struct {
	reliantv1connect.UnimplementedAccountServiceHandler
	db *sql.DB
	// controlPlane deletes the caller's platform account (billing identity,
	// daemons, PII) before the local purge. Optional: a deployment with no
	// control plane configured leaves it nil and deletes only local data.
	controlPlane controlplane.Client
}

// NewAccountService constructs the account-deletion service. It takes the raw
// *sql.DB because the purge is an ordered multi-statement transaction across
// 23 tables, which is not expressible through the per-entity Repository
// surface and should not be smeared across it.
//
// The control-plane client is wired only when this deployment HAS a control
// plane (RELIANT_CONTROL_PLANE_URL and friends). Self-hosted reliant has no
// platform account to delete, and defaulting to localhost would make every
// such deployment fail deletion against a control plane that isn't there.
func NewAccountService(sqlDB *sql.DB) *AccountService {
	svc := &AccountService{db: sqlDB}
	if baseURL := controlplane.BaseURLFromEnv(); baseURL != "" {
		svc.controlPlane = controlplane.NewClient(baseURL)
	}
	return svc
}

// WithControlPlaneClient overrides the control-plane client, for tests.
func (s *AccountService) WithControlPlaneClient(client controlplane.Client) *AccountService {
	s.controlPlane = client
	return s
}

// bearerToken strips the "Bearer " prefix from an Authorization header,
// yielding the raw JWT to forward to the control plane. callerIdentity has
// already established that this is a session JWT and not a PAT.
func bearerToken(authHeader string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authHeader), "Bearer "))
}

// callerIdentity extracts the caller's user id and email, and enforces the
// session-only rule.
//
// A PAT bearer is rejected: an automation credential must not be able to
// perform the one irreversible action on its owner's data. This mirrors
// TokenService.CreateToken's requireInteractiveSession, for the same reason.
func (s *AccountService) callerIdentity(ctx context.Context, authHeader string) (userID, email string, err error) {
	if rErr := requireInteractiveSession(authHeader); rErr != nil {
		return "", "", rErr
	}
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", "", connect.NewError(connect.CodeUnauthenticated,
			fmt.Errorf("no authenticated user"))
	}
	email, _ = auth.GetUserEmailFromContext(ctx)
	return userID, email, nil
}

// PreviewAccountDeletion reports what DeleteAccount would destroy.
func (s *AccountService) PreviewAccountDeletion(
	ctx context.Context,
	req *connect.Request[reliantv1.PreviewAccountDeletionRequest],
) (*connect.Response[reliantv1.PreviewAccountDeletionResponse], error) {
	userID, email, err := s.callerIdentity(ctx, req.Header().Get("Authorization"))
	if err != nil {
		return nil, err
	}

	counts, err := accountpurge.Preview(ctx, s.db, userID)
	if err != nil {
		logging.Error("[AccountService] preview failed", "user_id", userID, "error", err)
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("could not read account contents"))
	}

	return connect.NewResponse(&reliantv1.PreviewAccountDeletionResponse{
		ProjectCount:           counts.Projects,
		ChatCount:              counts.Chats,
		WorktreeCount:          counts.Worktrees,
		MessageCount:           counts.Messages,
		HasProviderCredentials: counts.HasProviderCredentials,
		ConfirmEmail:           email,
		RetainedElsewhere:      retainedElsewhere,
	}), nil
}

// DeleteAccount permanently removes every row reliant holds for the caller.
func (s *AccountService) DeleteAccount(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteAccountRequest],
) (*connect.Response[reliantv1.DeleteAccountResponse], error) {
	userID, email, err := s.callerIdentity(ctx, req.Header().Get("Authorization"))
	if err != nil {
		return nil, err
	}

	// The confirmation is verified here, not only in the dialog: a client that
	// skips its own UI still cannot delete an account by accident.
	//
	// An anonymous session has no email claim. Requiring a typed confirmation
	// it cannot know would strand exactly the half-upgraded users this feature
	// exists to rescue, so for them the confirmation is waived — they have no
	// email to type, and the JWT already proves the session is theirs.
	if email != "" {
		got := strings.ToLower(strings.TrimSpace(req.Msg.GetConfirmEmail()))
		want := strings.ToLower(strings.TrimSpace(email))
		if got != want {
			return nil, connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("confirmation does not match your email address"))
		}
	}

	logging.Warn("[AccountService] deleting account", "user_id", userID)

	// CONTROL PLANE FIRST, then the local purge.
	//
	// If the control plane refuses (a paid subscription, prepaid credit),
	// nothing has been destroyed and the user can clear the blocker and retry.
	// If it succeeds and the purge below then fails, the user is locked out of
	// the platform but their data is still here and the operation is
	// retryable. The reverse order has an unacceptable failure: every chat and
	// project destroyed, and the billing still running.
	if s.controlPlane != nil {
		blockers, cpErr := s.controlPlane.DeleteCurrentUserAccount(ctx,
			bearerToken(req.Header().Get("Authorization")))
		if cpErr != nil {
			logging.Error("[AccountService] control-plane deletion failed", "user_id", userID, "error", cpErr)
			return nil, connect.NewError(connect.CodeInternal,
				fmt.Errorf("could not delete your platform account; nothing was deleted"))
		}
		if len(blockers) > 0 {
			// FailedPrecondition with the blocker's own sentence: the user
			// gets "cancel your subscription first", not a generic error.
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New(blockers[0].Detail))
		}
	}

	deleted, err := accountpurge.Purge(ctx, s.db, userID)
	if err != nil {
		logging.Error("[AccountService] purge failed", "user_id", userID, "error", err)
		// The purge is transactional: a failure changed nothing, so the client
		// can safely retry.
		return nil, connect.NewError(connect.CodeInternal,
			fmt.Errorf("account deletion failed; nothing was deleted"))
	}

	logging.Warn("[AccountService] account deleted", "user_id", userID, "rows", deleted)

	return connect.NewResponse(&reliantv1.DeleteAccountResponse{
		DeletedRowCount: deleted,
	}), nil
}
