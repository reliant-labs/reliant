// Copyright (c) 2025 Reliant Labs
package account

import (
	"context"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/controlplane"
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
