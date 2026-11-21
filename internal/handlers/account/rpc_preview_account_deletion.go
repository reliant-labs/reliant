package account

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accountpurge"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
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
	"If you have a paid subscription, deletion will stop and ask you to cancel it first.",
	"Your sign-in identity is removed, so you will be signed out everywhere and this email can be used to sign up again.",
}

// PreviewAccountDeletion reports what DeleteAccount would destroy.
func (s *Service) PreviewAccountDeletion(
	ctx context.Context,
	req *connect.Request[reliantv1.PreviewAccountDeletionRequest],
) (*connect.Response[reliantv1.PreviewAccountDeletionResponse], error) {
	userID, email, err := s.callerIdentity(ctx, req.Header().Get("Authorization"))
	if err != nil {
		return nil, err
	}

	counts, err := accountpurge.Preview(ctx, s.deps.SQL, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("could not read account contents"))
	}

	// The wallet quote comes from the control plane, which owns the wallet.
	// A preview that cannot get it FAILS rather than omitting it: the dialog
	// must state the refund and the forfeiture in dollars before the user
	// confirms, and a dialog silently missing that line reads as "nothing
	// happens to your money".
	var wallet *reliantv1.AccountDeletionWalletQuote
	if s.deps.ControlPlane != nil {
		quote, cpErr := s.deps.ControlPlane.PreviewAccountDeletionWallet(ctx,
			auth.BearerToken(req.Header().Get("Authorization")))
		if cpErr != nil {
			// control-plane's error is a remote client's: the logged cause,
			// never this rpc's verdict.
			return nil, svcerr.WithCause(svcerr.Unavailable("could not load your wallet balance; please try again"), cpErr)
		}
		wallet = walletQuoteToProto(quote)
	}

	return connect.NewResponse(&reliantv1.PreviewAccountDeletionResponse{
		ProjectCount:           counts.Projects,
		ChatCount:              counts.Chats,
		WorktreeCount:          counts.Worktrees,
		MessageCount:           counts.Messages,
		HasProviderCredentials: counts.HasProviderCredentials,
		ConfirmEmail:           email,
		RetainedElsewhere:      retainedElsewhere,
		Wallet:                 wallet,
	}), nil
}

func walletQuoteToProto(q *controlplane.AccountDeletionWalletQuote) *reliantv1.AccountDeletionWalletQuote {
	if q == nil {
		return nil
	}
	return &reliantv1.AccountDeletionWalletQuote{
		RefundCents:         q.RefundCents,
		Destinations:        refundDestinationsToProto(q.Destinations),
		UnrefundableCents:   q.UnrefundableCents,
		ForfeitedPromoCents: q.ForfeitedPromoCents,
	}
}
