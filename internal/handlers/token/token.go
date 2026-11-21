// Copyright (c) 2025 Reliant Labs
package token

import (
	"context"
	"errors"
	"fmt"
	"time"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// maxTokenNameLen bounds a token label.
const maxTokenNameLen = 128

// scopeForKind maps the wire kind onto its one scope.
func scopeForKind(kind reliantv1.TokenKind) (fat.Scope, error) {
	switch kind {
	case reliantv1.TokenKind_TOKEN_KIND_DAEMON:
		return fat.ScopeDaemonConnect, nil
	case reliantv1.TokenKind_TOKEN_KIND_API:
		return fat.ScopeReliantAPI, nil
	default:
		return "", svcerr.InvalidArgument("kind is required (DAEMON or API)")
	}
}

// kindForScopes names a token by the session authority it carries. A daemon
// credential also carries reliant:api, so daemon:connect decides wherever it
// sits in the list — the order a store returns scopes in is not a contract.
func kindForScopes(scopes []string) reliantv1.TokenKind {
	kind := reliantv1.TokenKind_TOKEN_KIND_UNSPECIFIED
	for _, s := range scopes {
		switch fat.Scope(s) {
		case fat.ScopeDaemonConnect:
			return reliantv1.TokenKind_TOKEN_KIND_DAEMON
		case fat.ScopeReliantAPI:
			kind = reliantv1.TokenKind_TOKEN_KIND_API
		}
	}
	return kind
}

// callerUser returns the authenticated user and whether the caller is a
// machine credential rather than an interactive session.
func callerUser(ctx context.Context) (string, bool, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", false, svcerr.Unauthenticated("unauthenticated")
	}
	_, isMachine := auth.MachineTokenFromContext(ctx)
	return userID, isMachine, nil
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func tokenInfoProto(t tokenauthority.TokenInfo) *reliantv1.TokenInfo {
	info := &reliantv1.TokenInfo{
		Id:          t.ID,
		Name:        t.Name,
		TokenPrefix: t.DisplayPrefix,
		CreatedAt:   t.CreatedAt.UTC().Format(time.RFC3339),
		LastUsedAt:  formatTime(t.LastUsedAt),
		ExpiresAt:   formatTime(t.ExpiresAt),
		Kind:        kindForScopes(t.Scopes),
		Ephemeral:   t.Ephemeral,
		// A permanent credential has no expiry to bound it, so its
		// PERMISSIONS are the only thing left to audit. Surfacing them is
		// what lets the Settings list answer "what can this token do".
		Scopes: t.Scopes,
	}
	if t.Resource != nil && t.Resource.Kind == fat.ResourceDaemon {
		info.DaemonId = t.Resource.ID
	}
	return info
}

// authorityError is a failed token-authority call as the svcerr kind the rpc
// returns. The authority may be control-plane's (a remote client), so its error
// is never the verdict itself: it is the logged cause, and the client reads
// what this rpc says — except a grant the caller asked for and may not have,
// which is theirs to correct and is reported in the authority's own words.
func authorityError(op string, err error) error {
	switch {
	case tokenauthority.IsNotFound(err):
		return svcerr.WithCause(svcerr.NotFound("token"), err)
	case tokenauthority.IsInvalidGrant(err):
		msg := err.Error()
		var refused *accesstokenclient.RPCError
		if errors.As(err, &refused) {
			msg = refused.Message
		}
		return svcerr.WithCause(svcerr.InvalidArgument(msg), err)
	}
	return svcerr.WithCause(svcerr.Unavailable("token service unavailable"), fmt.Errorf("token authority %s: %w", op, err))
}
