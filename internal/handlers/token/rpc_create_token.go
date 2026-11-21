package token

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
)

// CreateToken mints a token acting as the caller. Interactive session only: a
// machine credential never mints a credential.
func (s *Service) CreateToken(
	ctx context.Context, req *connect.Request[reliantv1.CreateTokenRequest],
) (*connect.Response[reliantv1.CreateTokenResponse], error) {
	userID, isMachine, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}
	if isMachine {
		return nil, svcerr.PermissionDenied("token management requires an interactive session; a token cannot mint a token")
	}
	scope, err := scopeForKind(req.Msg.GetKind())
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" || len(name) > maxTokenNameLen {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("name is required (at most %d characters)", maxTokenNameLen))
	}
	if req.Msg.GetTtlSeconds() < 0 {
		return nil, svcerr.InvalidArgument("ttl_seconds must not be negative")
	}
	var expiresAt *time.Time
	if ttl := req.Msg.GetTtlSeconds(); ttl > 0 {
		at := time.Now().Add(time.Duration(ttl) * time.Second)
		expiresAt = &at
	}

	// The kind's one scope, and nothing else. A DAEMON credential minted by
	// the control plane holds EVERY scope its person holds from that alone,
	// resolved at each use (control-plane accesstoken/wildcard.go), so no
	// list named here can go stale when forge adds a scope; a self-hosted
	// store mints exactly daemon:connect.
	minted, err := s.deps.Authority.MintForUser(ctx, tokenauthority.MintRequest{
		UserID: userID, Name: name, Scopes: []fat.Scope{scope}, ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, authorityError("create", err)
	}
	scopes := minted.Scopes
	logging.InfoContext(ctx, "access token created", "user_id", userID, "token_id", minted.TokenID, "scopes", strings.Join(scopes, " "))
	return connect.NewResponse(&reliantv1.CreateTokenResponse{
		Info: &reliantv1.TokenInfo{
			Id:          minted.TokenID,
			Name:        name,
			TokenPrefix: minted.DisplayPrefix,
			CreatedAt:   time.Now().UTC().Format(time.RFC3339),
			ExpiresAt:   formatTime(minted.ExpiresAt),
			Kind:        req.Msg.GetKind(),
			// What the token carries, as the authority reports it — for a
			// daemon credential, its resolved wildcard. Built by hand rather than through
			// tokenInfoProto because a mint returns Minted, not TokenInfo —
			// which is exactly why this field was missing while the list
			// reported it.
			Scopes: scopes,
		},
		Token: minted.Plaintext,
	}), nil
}
