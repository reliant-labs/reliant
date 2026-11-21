package tools_daemon

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
)

// WhoAmI names the account the presented daemon credential acts as. The daemon
// auth interceptor has already resolved the credential; this reads the result
// and touches nothing else — no daemon row, no connection, no NATS.
//
// `reliant daemon start --token` asks before deriving the daemon's instance
// directory, so each account on a machine keeps its own saved daemon id.
func (s *Service) WhoAmI(
	ctx context.Context,
	_ *connect.Request[reliantv1.WhoAmIRequest],
) (*connect.Response[reliantv1.WhoAmIResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || strings.TrimSpace(userID) == "" {
		return nil, svcerr.Unauthenticated("a daemon credential is required")
	}
	return connect.NewResponse(&reliantv1.WhoAmIResponse{UserId: userID, AccountEmail: accountEmail(ctx)}), nil
}
