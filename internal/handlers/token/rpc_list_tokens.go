package token

import (
	"context"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// ListTokens lists the caller's live tokens, optionally of one kind.
func (s *Service) ListTokens(
	ctx context.Context, req *connect.Request[reliantv1.ListTokensRequest],
) (*connect.Response[reliantv1.ListTokensResponse], error) {
	userID, _, err := callerUser(ctx)
	if err != nil {
		return nil, err
	}
	var scope fat.Scope
	if req.Msg.GetKind() != reliantv1.TokenKind_TOKEN_KIND_UNSPECIFIED {
		if scope, err = scopeForKind(req.Msg.GetKind()); err != nil {
			return nil, err
		}
	}
	tokens, err := s.deps.Authority.ListForUser(ctx, userID, scope)
	if err != nil {
		return nil, authorityError("list", err)
	}
	out := make([]*reliantv1.TokenInfo, 0, len(tokens))
	for _, t := range tokens {
		// This surface manages reliant's own kinds; LLM keys and connector
		// credentials have their own surfaces.
		if kindForScopes(t.Scopes) == reliantv1.TokenKind_TOKEN_KIND_UNSPECIFIED {
			continue
		}
		out = append(out, tokenInfoProto(t))
	}
	return connect.NewResponse(&reliantv1.ListTokensResponse{Tokens: out}), nil
}
