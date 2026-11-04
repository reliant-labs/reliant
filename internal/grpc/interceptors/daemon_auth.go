// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"
)

// DaemonAuthInterceptor authenticates daemon connections to the gateway.
//
// The credential is an `rlat_` access token carrying daemon:connect and acting
// as a user, resolved by the deployment's token authority — control-plane's
// Introspect when hosted, the local store when self-hosted. It runs ONCE PER
// CONNECT against an UNCACHED introspector: a connect is rare, and a daemon
// stream is the highest-value surface, so a revoked credential is refused on
// its very next connect. Anything not shaped like an `rlat_` is refused
// before a round trip.
//
// A token bound to a daemon (resource daemon:<id>) authenticates as that
// daemon only: the bound id is placed on the context, and the gateway uses it
// as the daemon's authoritative identity instead of guessing from hostname.
//
// The authority also names the account's email, when it knows one; it goes on
// the context (auth.GetUserEmailFromContext) so the gateway can tell the
// daemon which account it connected as.
type DaemonAuthInterceptor struct {
	tokens auth.AccessTokenAccountIntrospector
}

// NewDaemonAuthInterceptor creates an interceptor that validates daemon
// credentials through tokens.
func NewDaemonAuthInterceptor(tokens auth.AccessTokenAccountIntrospector) (*DaemonAuthInterceptor, error) {
	if tokens == nil {
		return nil, fmt.Errorf("daemon credential introspector is required")
	}
	return &DaemonAuthInterceptor{tokens: tokens}, nil
}

func (i *DaemonAuthInterceptor) authenticate(ctx context.Context, header func(string) string) (context.Context, error) {
	authHeader := strings.TrimSpace(header("Authorization"))
	if authHeader == "" {
		return nil, svcerr.ToConnect(svcerr.Unauthenticated("missing authorization token"))
	}
	rawToken := strings.TrimPrefix(authHeader, "Bearer ")
	if rawToken == authHeader {
		return nil, svcerr.ToConnect(svcerr.Unauthenticated("invalid authorization header format"))
	}
	rawToken = strings.TrimSpace(rawToken)
	if !auth.IsAccessTokenFormat(rawToken) {
		return nil, svcerr.ToConnect(svcerr.Unauthenticated("daemon credential must be an rlat_ access token; re-register the daemon"))
	}

	account, err := i.tokens.IntrospectAccount(ctx, rawToken)
	p := account.Principal
	if err != nil {
		if errors.Is(err, auth.ErrAccessTokenInactive) {
			return nil, svcerr.ToConnect(svcerr.Unauthenticated("invalid daemon auth token"))
		}
		// The authority is unreachable: our outage, not the daemon's bad
		// credential. Unavailable makes the daemon retry instead of
		// discarding a good token. The daemon reads only the fixed text;
		// the authority's own error stays server-side as the cause, which
		// the "rpc failed" record logs.
		return nil, svcerr.ToConnect(svcerr.WithCause(
			svcerr.Unavailable("daemon credential verification unavailable"), err))
	}
	if !p.Scopes.Permits(fat.ScopeDaemonConnect) || p.ActingUserID == "" {
		return nil, svcerr.ToConnect(svcerr.Unauthenticated("token does not grant daemon:connect"))
	}
	if p.Resource != nil && p.Resource.Kind != fat.ResourceDaemon {
		return nil, svcerr.ToConnect(svcerr.Unauthenticated("token is bound to a non-daemon resource"))
	}

	ctx = context.WithValue(ctx, auth.UserIDContextKey, p.ActingUserID)
	if account.Email != "" {
		ctx = context.WithValue(ctx, auth.UserEmailContextKey, account.Email)
	}
	if p.Resource != nil {
		ctx = context.WithValue(ctx, auth.DaemonIDContextKey, p.Resource.ID)
	}
	return ctx, nil
}

func (i *DaemonAuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		authenticatedCtx, err := i.authenticate(ctx, req.Header().Get)
		if err != nil {
			return nil, err
		}
		return next(authenticatedCtx, req)
	}
}

func (i *DaemonAuthInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *DaemonAuthInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		authenticatedCtx, err := i.authenticate(ctx, conn.RequestHeader().Get)
		if err != nil {
			return err
		}
		return next(authenticatedCtx, conn)
	}
}
