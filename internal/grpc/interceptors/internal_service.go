// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// InternalServiceInterceptor authenticates a narrow allow-list of procedures
// using an internal-service HS256 token (see auth.InternalServiceVerifier),
// NOT a Supabase user JWT.
//
// It exists for RPCs the control-plane OPERATOR calls with no end-user request
// in scope — currently the managed-daemon-token mint/revoke surface. The
// operator presents a short-lived token signed with the shared
// INTERNAL_SERVICE_SECRET; this interceptor verifies it and, on success,
// injects the internal-service identity into context.
//
// Procedures NOT in gatedProcedures pass through untouched: this interceptor is
// composed alongside the regular AuthInterceptor, which still owns user-JWT
// auth for every other RPC. Conversely, a gated procedure is authenticated
// ONLY by this interceptor — a user JWT will not satisfy it (the verifier
// requires sub=internal-service / role=admin), and the regular AuthInterceptor
// must list the gated procedures as public so it does not also demand a user
// JWT for them.
type InternalServiceInterceptor struct {
	verifier        *auth.InternalServiceVerifier
	gatedProcedures map[string]bool
}

// NewInternalServiceInterceptor builds an interceptor that enforces
// internal-service auth on exactly the supplied procedures (fully-qualified,
// e.g. "/reliant.v1.DaemonTokenService/MintManagedDaemonToken").
func NewInternalServiceInterceptor(verifier *auth.InternalServiceVerifier, gatedProcedures []string) (*InternalServiceInterceptor, error) {
	if verifier == nil {
		return nil, fmt.Errorf("internal-service verifier is required")
	}
	set := make(map[string]bool, len(gatedProcedures))
	for _, p := range gatedProcedures {
		set[p] = true
	}
	if !verifier.Enabled() {
		// Fail-closed is still enforced per-request, but warn loudly at setup so
		// a missing secret in a managed environment is obvious in logs.
		logging.Warn("[Internal Service Auth] INTERNAL_SERVICE_SECRET is not set; managed-daemon-token RPCs will reject all callers")
	}
	return &InternalServiceInterceptor{verifier: verifier, gatedProcedures: set}, nil
}

func (i *InternalServiceInterceptor) authenticate(ctx context.Context, procedure string, header func(string) string) (context.Context, error) {
	if !i.gatedProcedures[procedure] {
		// Not an internal-service RPC — leave auth to the regular chain.
		return ctx, nil
	}

	authHeader := strings.TrimSpace(header("Authorization"))
	if authHeader == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("missing internal-service authorization token"))
	}
	rawToken := strings.TrimPrefix(authHeader, "Bearer ")
	if rawToken == authHeader {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid authorization header format"))
	}
	rawToken = strings.TrimSpace(rawToken)

	claims, err := i.verifier.ValidateToken(rawToken)
	if err != nil {
		logging.Warn("[Internal Service Auth] Rejected token", "procedure", procedure, "error", err)
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid internal-service token"))
	}

	// Inject the internal-service identity so downstream handlers/audit can
	// distinguish operator-driven calls from human-driven ones.
	ctx = context.WithValue(ctx, auth.UserIDContextKey, claims.Sub)
	ctx = context.WithValue(ctx, auth.UserRoleContextKey, claims.Role)
	return ctx, nil
}

func (i *InternalServiceInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := i.authenticate(ctx, req.Spec().Procedure, req.Header().Get)
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (i *InternalServiceInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *InternalServiceInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := i.authenticate(ctx, conn.Spec().Procedure, conn.RequestHeader().Get)
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}
