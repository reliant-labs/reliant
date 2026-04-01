// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/auth"
)

// DaemonAuthInterceptor authenticates tools-daemon connections using
// Personal Access Tokens (PATs). The interceptor validates the bearer token
// against the database and injects the user identity into context.
type DaemonAuthInterceptor struct {
	validator auth.PATValidator
}

// NewDaemonAuthInterceptor creates an interceptor that validates PATs.
func NewDaemonAuthInterceptor(validator auth.PATValidator) (*DaemonAuthInterceptor, error) {
	if validator == nil {
		return nil, fmt.Errorf("PAT validator is required")
	}
	return &DaemonAuthInterceptor{validator: validator}, nil
}

func (i *DaemonAuthInterceptor) authenticate(ctx context.Context, header func(string) string) (context.Context, error) {
	authHeader := strings.TrimSpace(header("Authorization"))
	if authHeader == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("missing authorization token"))
	}

	rawToken := strings.TrimPrefix(authHeader, "Bearer ")
	if rawToken == authHeader {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid authorization header format"))
	}
	rawToken = strings.TrimSpace(rawToken)

	userID, _, err := i.validator.ValidatePAT(ctx, rawToken)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid daemon auth token: %w", err))
	}

	// Always inject user ID into context — PAT-based auth always resolves to a user
	ctx = context.WithValue(ctx, auth.UserIDContextKey, userID)
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
