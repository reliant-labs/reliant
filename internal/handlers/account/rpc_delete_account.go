package account

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// DeleteAccount permanently removes every row reliant holds for the caller.
func (s *Service) DeleteAccount(
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
			return nil, svcerr.InvalidArgument("confirmation does not match your email address")
		}
	}

	logging.WarnContext(ctx, "[AccountService] deleting account", "user_id", userID)

	// CONTROL PLANE FIRST, then the local purge.
	//
	// If the control plane refuses (a paid subscription), nothing has been
	// destroyed and the user can clear the blocker and retry. A wallet balance
	// does not refuse: the control plane refunds the paid part to the card
	// and forfeits promotional credit as part of deleting the account.
	// If it succeeds and the purge below then fails, the user is locked out of
	// the platform but their data is still here and the operation is
	// retryable. The reverse order has an unacceptable failure: every chat and
	// project destroyed, and the billing still running.
	resp := &reliantv1.DeleteAccountResponse{}
	if s.deps.ControlPlane != nil {
		result, cpErr := s.deps.ControlPlane.DeleteCurrentUserAccount(ctx,
			auth.BearerToken(req.Header().Get("Authorization")))
		if cpErr != nil {
			// control-plane's error is a remote client's: it decides which
			// sentence this rpc says, and is the logged cause, never the
			// verdict itself (svcerr.Code reads the *connect.Error it is).
			if svcerr.Code(cpErr) == connect.CodeUnavailable {
				// A refund's outcome could not be confirmed with the payment
				// provider. Nothing irreversible happened and retrying replays
				// the same refund rather than issuing another.
				return nil, svcerr.WithCause(svcerr.Unavailable("we could not confirm your refund with our payment provider; nothing was deleted, please try again in a minute"), cpErr)
			}
			return nil, svcerr.WithCause(svcerr.Internal("could not delete your platform account; nothing was deleted"), cpErr)
		}
		if len(result.Blockers) > 0 {
			// FailedPrecondition with the blocker's own sentence: the user
			// gets "cancel your subscription first", not a generic error.
			return nil, svcerr.FailedPrecondition(result.Blockers[0].Detail)
		}
		resp.RefundedCents = result.RefundedCents
		resp.RefundDestinations = refundDestinationsToProto(result.RefundDestinations)
		resp.ForfeitedPromoCents = result.ForfeitedPromoCents
		resp.RefundPending = result.RefundPending
		resp.RefundOwedCents = result.RefundOwedCents
	}

	deleted, err := s.deps.Purger.Purge(ctx, userID)
	if err != nil {
		// The purge is transactional: a failure changed nothing, so the client
		// can safely retry.
		return nil, svcerr.WrapOr(err, svcerr.Internal("account deletion failed; nothing was deleted"))
	}

	logging.WarnContext(ctx, "[AccountService] account deleted", "user_id", userID, "rows", deleted,
		"refund_pending", resp.RefundPending)

	resp.DeletedRowCount = deleted
	resp.UserId = userID
	return connect.NewResponse(resp), nil
}
