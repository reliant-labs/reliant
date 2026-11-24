// Copyright (c) 2025 Reliant Labs
package account

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/logging"
)

// callerIdentity extracts the caller's user id and email, and enforces the
// session-only rule.
//
// An access-token bearer is refused: an automation credential must not be
// able to perform the one irreversible action on its owner's data. This
// mirrors TokenService.CreateToken, for the same reason.
func (s *Service) callerIdentity(ctx context.Context, authHeader string) (userID, email string, err error) {
	if _, isMachine := auth.MachineTokenFromContext(ctx); isMachine || auth.IsAccessTokenFormat(auth.BearerToken(authHeader)) {
		return "", "", svcerr.Unauthenticated("account deletion requires an interactive session")
	}
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", "", svcerr.Unauthenticated("no authenticated user")
	}
	email, _ = auth.GetUserEmailFromContext(ctx)
	return userID, email, nil
}

// controlPlaneAccountDeletedReason is the svcerr reason control-plane sets
// (svcerr.ReasonHeader) when it refuses a request because the caller's
// platform account is already deleted.
const controlPlaneAccountDeletedReason = "account_deleted"

// controlPlaneAccountDeletedMessage is the message control-plane's
// deleted-account interceptor refuses with. Matched as well as the reason,
// because control-plane releases before the reason only send the message.
const controlPlaneAccountDeletedMessage = "this account has been deleted"

// controlPlaneAccountAlreadyDeleted reports whether the control plane refused
// because the caller's platform account is ALREADY deleted.
//
// That is what a retry sees after a deletion whose control-plane half
// succeeded and whose local purge then failed: the platform account is gone,
// so the control plane refuses every later call with the same, still-valid
// JWT. Treating that refusal as a failure strands the user's reliant data
// forever, so DeleteAccount resumes the local purge on it instead. Trusting it
// is sound: the control plane answers for exactly the bearer the caller
// presented, and the purge removes only that caller's own rows.
func controlPlaneAccountAlreadyDeleted(err error) bool {
	if svcerr.MetaOf(err).Get(svcerr.ReasonHeader) == controlPlaneAccountDeletedReason {
		return true
	}
	var ce *connect.Error
	return errors.As(err, &ce) && ce.Code() == connect.CodePermissionDenied &&
		strings.Contains(ce.Message(), controlPlaneAccountDeletedMessage)
}

// purgeAttempts bounds how often DeleteAccount runs the local purge when a
// pooled connection dies under it (driver.ErrBadConn, e.g. a PgBouncer
// rollout). The purge is one transaction and idempotent, so a retry is safe;
// failing instead strands an account whose platform half is already deleted.
const purgeAttempts = 3

// purgeWithRetry runs the Purger, retrying only a dead connection.
func (s *Service) purgeWithRetry(ctx context.Context, userID string) (int64, error) {
	var (
		deleted int64
		err     error
	)
	for attempt := 1; attempt <= purgeAttempts; attempt++ {
		deleted, err = s.deps.Purger.Purge(ctx, userID)
		if err == nil || !errors.Is(err, driver.ErrBadConn) || ctx.Err() != nil {
			return deleted, err
		}
		logging.WarnContext(ctx, "[AccountService] account purge lost its database connection; retrying",
			"user_id", userID, "attempt", attempt, "error", err)
	}
	return deleted, err
}

func refundDestinationsToProto(in []controlplane.RefundDestination) []*reliantv1.RefundDestination {
	out := make([]*reliantv1.RefundDestination, 0, len(in))
	for _, d := range in {
		out = append(out, &reliantv1.RefundDestination{
			CardBrand:   d.CardBrand,
			CardLast4:   d.CardLast4,
			AmountCents: d.AmountCents,
		})
	}
	return out
}
