package tokenauthority

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/store"
)

// SelfHostedOrg is the one org a self-hosted reliant's org automation tokens
// belong to. Self-hosted reliant has no organizations; personal tokens use the
// acting user's id as their org, which keeps one user's tokens from ever being
// listed or revoked under another's.
const SelfHostedOrg = "self-hosted"

// maxLiveUserTokens mirrors control-plane's per-user cap on rotated keys.
const maxLiveUserTokens = 50

// LocalStore is the SELF-HOSTED Authority: the same token system as
// control-plane's — every rule comes from forge/pkg/accesstoken — over
// reliant's own access_tokens table (migration 20260925000000). A token minted
// here behaves identically to one minted by control-plane. Its statements are
// internal/store's (access_token_repo_ext.go).
type LocalStore struct {
	db  orm.Context
	now func() time.Time
}

// NewLocalStore constructs the self-hosted store over reliant's database.
func NewLocalStore(db orm.Context) *LocalStore { return &LocalStore{db: db, now: time.Now} }

// Introspect resolves a presented token. Revocation and expiry are in the
// WHERE clause and evaluated on the database clock; the hash is re-compared in
// constant time.
func (s *LocalStore) Introspect(ctx context.Context, token string) (*fat.Principal, error) {
	if !fat.HasFormat(token) {
		return nil, accesstokenclient.ErrInactive
	}
	hash := fat.Hash(token)
	rec, found, err := store.FindLiveAccessToken(ctx, s.db, hash)
	if err != nil {
		return nil, fmt.Errorf("tokenauthority: resolving token: %w", err)
	}
	if !found || !fat.EqualHash(hash, rec.TokenHash) {
		return nil, accesstokenclient.ErrInactive
	}
	p := fat.Principal{TokenID: rec.ID, OrgID: rec.OrgID, ActingUserID: rec.ActingUserID, Ephemeral: rec.Ephemeral, ExpiresAt: rec.ExpiresAt}
	if p.Scopes, err = fat.NewSet(rec.Scopes); err != nil {
		return nil, fmt.Errorf("tokenauthority: token %s: %w", p.TokenID, err)
	}
	if rec.ResourceKind != "" {
		p.Resource = &fat.Resource{Kind: fat.ResourceKind(rec.ResourceKind), ID: rec.ResourceID}
	}
	// Best-effort, throttled to once a minute per token.
	_ = store.TouchAccessToken(ctx, s.db, p.TokenID)
	return &p, nil
}

// MintForUser mints a personal token acting as req.UserID. With Rotate, it
// atomically revokes the user's live tokens of the same name and scope set.
func (s *LocalStore) MintForUser(ctx context.Context, req MintRequest) (Minted, error) {
	scopes := fat.SetOf(req.Scopes...)
	grant := fat.Grant{
		OrgID:        req.UserID,
		Name:         strings.TrimSpace(req.Name),
		Scopes:       scopes,
		ActingUserID: req.UserID,
		Resource:     req.Resource,
		Ephemeral:    req.Ephemeral,
		ExpiresAt:    req.ExpiresAt,
	}
	if strings.TrimSpace(req.UserID) == "" {
		return Minted{}, fmt.Errorf("%w: user id is required", fat.ErrInvalidGrant)
	}
	if req.OrgID != "" && req.OrgID != req.UserID {
		return Minted{}, fmt.Errorf("%w: a self-hosted server has no organizations to mint in", fat.ErrInvalidGrant)
	}
	if err := grant.Validate(s.now()); err != nil {
		return Minted{}, err
	}
	if !req.Rotate {
		return s.insert(ctx, grant)
	}

	// One READ COMMITTED transaction (as database/sql's default was), never
	// retried: the advisory lock, not snapshot isolation, serializes rotations.
	var m Minted
	err := s.db.RunTx(ctx, orm.TxOptions{Isolation: orm.ReadCommitted, MaxAttempts: 1}, func(ctx context.Context) error {
		if err := store.LockUserAccessTokens(ctx, s.db, req.UserID); err != nil {
			return err
		}
		scopeList := scopes.Strings()
		replaced, err := store.RevokeUserAccessTokensNamed(ctx, s.db, req.UserID, grant.Name, scopeList)
		if err != nil {
			return err
		}
		live, err := store.CountLiveUserAccessTokens(ctx, s.db, req.UserID, scopeList)
		if err != nil {
			return err
		}
		if live >= maxLiveUserTokens {
			return fmt.Errorf("tokenauthority: too many live tokens (%d); revoke unused ones first", maxLiveUserTokens)
		}
		if m, err = s.insert(ctx, grant); err != nil {
			return err
		}
		m.Rotated = replaced > 0
		return nil
	})
	if err != nil {
		return Minted{}, err
	}
	return m, nil
}

func (s *LocalStore) insert(ctx context.Context, g fat.Grant) (Minted, error) {
	minted, err := fat.Mint()
	if err != nil {
		return Minted{}, err
	}
	t := store.NewAccessToken{
		ID: uuid.NewString(), OrgID: g.OrgID, Name: g.Name, TokenHash: minted.Hash, TokenPrefix: minted.DisplayPrefix,
		Scopes: g.Scopes.Strings(), ActingUserID: g.ActingUserID, Ephemeral: g.Ephemeral, ExpiresAt: g.ExpiresAt,
	}
	if g.Resource != nil {
		t.ResourceKind, t.ResourceID = string(g.Resource.Kind), g.Resource.ID
	}
	if err := store.InsertAccessToken(ctx, s.db, t); err != nil {
		return Minted{}, fmt.Errorf("tokenauthority: storing token: %w", err)
	}
	return Minted{TokenID: t.ID, Plaintext: minted.Plaintext, DisplayPrefix: minted.DisplayPrefix,
		ExpiresAt: g.ExpiresAt, Scopes: g.Scopes.Strings()}, nil
}

func tokenInfo(r store.AccessTokenRecord) TokenInfo {
	t := TokenInfo{
		ID: r.ID, Name: r.Name, DisplayPrefix: r.TokenPrefix, Scopes: r.Scopes, Ephemeral: r.Ephemeral,
		CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, LastUsedAt: r.LastUsedAt,
	}
	if r.ResourceKind != "" {
		t.Resource = &fat.Resource{Kind: fat.ResourceKind(r.ResourceKind), ID: r.ResourceID}
	}
	return t
}

// ListForUser lists the live tokens acting as userID, optionally one scope.
func (s *LocalStore) ListForUser(ctx context.Context, userID string, scope fat.Scope) ([]TokenInfo, error) {
	recs, err := store.ListUserAccessTokens(ctx, s.db, userID, string(scope))
	if err != nil {
		return nil, fmt.Errorf("tokenauthority: listing tokens: %w", err)
	}
	var out []TokenInfo
	for _, r := range recs {
		out = append(out, tokenInfo(r))
	}
	return out, nil
}

// ErrNotFound reports a token that is not the user's (or does not exist).
var ErrNotFound = errors.New("tokenauthority: token not found")

// UpdateForUser changes one of userID's tokens' name and/or scopes. The user is
// in the WHERE clause, so one user can never edit another's token.
//
// token_hash is deliberately untouched: the credential keeps working with its
// new authority, which is what makes editing a REMOTE daemon's permissions
// possible at all.
func (s *LocalStore) UpdateForUser(
	ctx context.Context, userID, tokenID string, name *string, scopes fat.Set,
) (TokenInfo, error) {
	if strings.TrimSpace(tokenID) == "" {
		return TokenInfo{}, fmt.Errorf("%w: token id is required", fat.ErrInvalidGrant)
	}
	if name == nil && scopes == nil {
		return TokenInfo{}, fmt.Errorf("%w: an update must change the name or the scopes", fat.ErrInvalidGrant)
	}
	if scopes != nil && len(scopes) == 0 {
		// Mirrors the mint path: a token that authenticates and can do
		// nothing reads as an outage rather than as a revocation.
		return TokenInfo{}, fmt.Errorf("%w: a token must keep at least one scope; revoke it instead", fat.ErrInvalidGrant)
	}
	var newName *string
	if name != nil {
		if strings.TrimSpace(*name) == "" {
			return TokenInfo{}, fmt.Errorf("%w: a token's name cannot be blank", fat.ErrInvalidGrant)
		}
		trimmed := strings.TrimSpace(*name)
		newName = &trimmed
	}
	var newScopes []string
	if scopes != nil {
		newScopes = scopes.Strings()
	}
	rec, found, err := store.UpdateUserAccessToken(ctx, s.db, userID, tokenID, newName, newScopes)
	if err != nil {
		return TokenInfo{}, fmt.Errorf("tokenauthority: updating token: %w", err)
	}
	if !found {
		// No-such-token, not-yours and already-revoked collapse into one
		// answer, so a caller cannot probe for other people's token ids.
		return TokenInfo{}, ErrNotFound
	}
	return tokenInfo(rec), nil
}

// RevokeForUser revokes one of userID's tokens; the user is in the WHERE
// clause. Idempotent on an already-revoked token.
func (s *LocalStore) RevokeForUser(ctx context.Context, userID, tokenID string) error {
	n, err := store.RevokeUserAccessToken(ctx, s.db, userID, tokenID)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	exists, err := store.UserAccessTokenExists(ctx, s.db, userID, tokenID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// RevokeResource revokes every live token bound to resource.
func (s *LocalStore) RevokeResource(ctx context.Context, resource fat.Resource) (int64, error) {
	return store.RevokeResourceAccessTokens(ctx, s.db, string(resource.Kind), resource.ID)
}

// RevokeEphemeral revokes userID's ephemeral tokens.
func (s *LocalStore) RevokeEphemeral(ctx context.Context, userID string) (int64, error) {
	return store.RevokeEphemeralAccessTokens(ctx, s.db, userID)
}
