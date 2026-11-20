// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/logging"
)

// DomainWhitelistInterceptor rejects authenticated requests whose email domain
// is not in the allowed list. If the allowed list is empty, all domains pass through.
// Unauthenticated requests (no email in context) are passed through unchanged.
type DomainWhitelistInterceptor struct {
	allowedDomains map[string]bool
}

// NewDomainWhitelistInterceptor creates a new domain whitelist interceptor.
// If allowedDomains is empty, the interceptor is a no-op (all domains allowed).
func NewDomainWhitelistInterceptor(allowedDomains []string) *DomainWhitelistInterceptor {
	m := make(map[string]bool, len(allowedDomains))
	for _, d := range allowedDomains {
		d = strings.TrimSpace(strings.ToLower(d))
		if d != "" {
			m[d] = true
		}
	}
	return &DomainWhitelistInterceptor{allowedDomains: m}
}

// checkDomain validates the email domain from context against the allowed list.
// Returns nil if the request should proceed, or an error if the domain is not allowed.
func (i *DomainWhitelistInterceptor) checkDomain(ctx context.Context) error {
	if len(i.allowedDomains) == 0 {
		return nil
	}

	email, ok := auth.GetUserEmailFromContext(ctx)
	if !ok || email == "" {
		// No email in context (unauthenticated or daemon PAT) — pass through
		return nil
	}

	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return nil
	}
	domain := strings.ToLower(parts[1])

	if !i.allowedDomains[domain] {
		logging.WarnContext(ctx, "[Domain Whitelist] Rejected email domain",
			"domain", domain,
		)
		return svcerr.ToConnectV2(svcerr.PermissionDenied(fmt.Sprintf("email domain %q is not allowed in this environment", domain)))
	}

	return nil
}

// Intercept is the whitelist as a connect.ServerInterceptor, for unary and
// streaming RPCs alike. A refusal goes out through svcerr.RejectV2.
func (i *DomainWhitelistInterceptor) Intercept(next connect.ServerFunc) connect.ServerFunc {
	return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
		if err := i.checkDomain(ctx); err != nil {
			return svcerr.RejectV2(ctx, err)
		}
		return next(ctx, spec, stream)
	}
}
