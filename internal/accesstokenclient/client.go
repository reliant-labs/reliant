package accesstokenclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	"github.com/reliant-labs/reliant/internal/auth"
)

// CacheTTL bounds how long CachedIntrospector trusts a positive OR negative
// introspection. It is the API interceptor's revocation window: a token
// revoked at control-plane stops working at reliant's API within CacheTTL.
const CacheTTL = 5 * time.Second

// ServicePath is the Connect path prefix of control-plane's internal service.
const ServicePath = "/controlplane.v1.AccessTokenInternalService/"

// ErrInactive reports a token control-plane says is not live (unknown,
// revoked, expired, malformed). Distinct from a transport error, which is our
// outage and must not be reported to the caller as "your token is bad".
var ErrInactive = fmt.Errorf("accesstokenclient: %w", auth.ErrAccessTokenInactive)

// Client calls control-plane's AccessTokenInternalService.
type Client struct {
	baseURL string
	http    *http.Client
	sign    Signer
}

// New returns a client for the control-plane at deps.BaseURL.
//
// forge:constructor
func New(deps Deps) *Client {
	httpClient := deps.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(deps.BaseURL), "/"),
		http:    httpClient,
		sign:    deps.Sign,
	}
}

// resourceJSON is InternalResourceBinding on the wire (protojson names).
type resourceJSON struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id,omitempty"`
}

type introspectResponse struct {
	Active       bool          `json:"active"`
	TokenID      string        `json:"tokenId"`
	OrgID        string        `json:"orgId"`
	ActingUserID string        `json:"actingUserId"`
	Scopes       []string      `json:"scopes"`
	Resource     *resourceJSON `json:"resource"`
	Ephemeral    bool          `json:"ephemeral"`
	ExpiresAt    *time.Time    `json:"expiresAt"`
}

// Introspect resolves a presented token, uncached. A token that is not even
// shaped like an `rlat_` is refused locally without a round trip.
func (c *Client) Introspect(ctx context.Context, token string) (*fat.Principal, error) {
	if !fat.HasFormat(token) {
		return nil, ErrInactive
	}
	var out introspectResponse
	if err := c.call(ctx, "Introspect", map[string]any{"token": token}, &out); err != nil {
		return nil, err
	}
	if !out.Active {
		return nil, ErrInactive
	}
	scopes, err := fat.NewSet(out.Scopes)
	if err != nil {
		// A scope this binary cannot evaluate refuses, rather than
		// authenticating with the understood subset.
		return nil, fmt.Errorf("accesstokenclient: token %s: %w", out.TokenID, err)
	}
	p := &fat.Principal{
		TokenID:      out.TokenID,
		OrgID:        out.OrgID,
		ActingUserID: out.ActingUserID,
		Scopes:       scopes,
		Ephemeral:    out.Ephemeral,
		ExpiresAt:    out.ExpiresAt,
	}
	if out.Resource != nil && out.Resource.Kind != "" {
		p.Resource = &fat.Resource{Kind: fat.ResourceKind(out.Resource.Kind), ID: out.Resource.ID}
	}
	return p, nil
}

// MintRequest is a mint on a user's behalf.
type MintRequest struct {
	UserID    string
	Name      string
	Scopes    []fat.Scope
	Resource  *fat.Resource
	Ephemeral bool
	ExpiresAt *time.Time
	// Rotate atomically replaces the user's live token(s) with this name and
	// scope set.
	Rotate bool
}

// Minted is a freshly minted token. Plaintext is returned exactly once.
type Minted struct {
	TokenID       string
	Plaintext     string
	DisplayPrefix string
	ExpiresAt     *time.Time
	Rotated       bool
}

// MintForUser mints a token acting as req.UserID in their primary org.
func (c *Client) MintForUser(ctx context.Context, req MintRequest) (Minted, error) {
	body := map[string]any{
		"userId":    req.UserID,
		"name":      req.Name,
		"scopes":    scopeStrings(req.Scopes),
		"ephemeral": req.Ephemeral,
		"rotate":    req.Rotate,
	}
	if req.Resource != nil {
		body["resource"] = resourceJSON{Kind: string(req.Resource.Kind), ID: req.Resource.ID}
	}
	if req.ExpiresAt != nil {
		body["expiresAt"] = req.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	var out struct {
		TokenID       string     `json:"tokenId"`
		Secret        string     `json:"secret"`
		DisplayPrefix string     `json:"displayPrefix"`
		ExpiresAt     *time.Time `json:"expiresAt"`
		Rotated       bool       `json:"rotated"`
	}
	if err := c.call(ctx, "MintForUser", body, &out); err != nil {
		return Minted{}, err
	}
	if !fat.HasFormat(out.Secret) {
		return Minted{}, fmt.Errorf("accesstokenclient: MintForUser returned a malformed token")
	}
	return Minted{TokenID: out.TokenID, Plaintext: out.Secret, DisplayPrefix: out.DisplayPrefix,
		ExpiresAt: out.ExpiresAt, Rotated: out.Rotated}, nil
}

// RevokeResource revokes every live token bound to resource.
func (c *Client) RevokeResource(ctx context.Context, resource fat.Resource) (int64, error) {
	var out struct {
		RevokedCount json.Number `json:"revokedCount"`
	}
	err := c.call(ctx, "RevokeResource",
		map[string]any{"resource": resourceJSON{Kind: string(resource.Kind), ID: resource.ID}}, &out)
	if err != nil {
		return 0, err
	}
	return numberOrZero(out.RevokedCount), nil
}

// RevokeEphemeral revokes a user's ephemeral tokens (session end).
func (c *Client) RevokeEphemeral(ctx context.Context, userID string) (int64, error) {
	var out struct {
		RevokedCount json.Number `json:"revokedCount"`
	}
	if err := c.call(ctx, "RevokeEphemeral", map[string]any{"userId": userID}, &out); err != nil {
		return 0, err
	}
	return numberOrZero(out.RevokedCount), nil
}

// TokenInfo is a token's metadata. It never carries the secret.
type TokenInfo struct {
	ID            string
	Name          string
	DisplayPrefix string
	Scopes        []string
	Resource      *fat.Resource
	Ephemeral     bool
	CreatedAt     time.Time
	ExpiresAt     *time.Time
	LastUsedAt    *time.Time
}

// ListForUser lists a user's live tokens, optionally narrowed to one scope.
func (c *Client) ListForUser(ctx context.Context, userID string, scope fat.Scope) ([]TokenInfo, error) {
	var out struct {
		Tokens []struct {
			ID            string        `json:"id"`
			Name          string        `json:"name"`
			DisplayPrefix string        `json:"displayPrefix"`
			Scopes        []string      `json:"scopes"`
			Resource      *resourceJSON `json:"resource"`
			Ephemeral     bool          `json:"ephemeral"`
			CreatedAt     time.Time     `json:"createdAt"`
			ExpiresAt     *time.Time    `json:"expiresAt"`
			LastUsedAt    *time.Time    `json:"lastUsedAt"`
		} `json:"tokens"`
	}
	body := map[string]any{"userId": userID}
	if scope != "" {
		body["scope"] = string(scope)
	}
	if err := c.call(ctx, "ListForUser", body, &out); err != nil {
		return nil, err
	}
	infos := make([]TokenInfo, 0, len(out.Tokens))
	for _, t := range out.Tokens {
		info := TokenInfo{ID: t.ID, Name: t.Name, DisplayPrefix: t.DisplayPrefix, Scopes: t.Scopes,
			Ephemeral: t.Ephemeral, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt}
		if t.Resource != nil && t.Resource.Kind != "" {
			info.Resource = &fat.Resource{Kind: fat.ResourceKind(t.Resource.Kind), ID: t.Resource.ID}
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// RevokeForUser revokes one of a user's tokens. A token belonging to someone
// else reports a not_found RPCError.
func (c *Client) RevokeForUser(ctx context.Context, userID, tokenID string) error {
	var out struct{}
	return c.call(ctx, "RevokeForUser", map[string]any{"userId": userID, "tokenId": tokenID}, &out)
}

// IsNotFound reports whether err is control-plane's not_found.
func IsNotFound(err error) bool {
	var rpc *RPCError
	return errors.As(err, &rpc) && rpc.Code == "not_found"
}

// RPCError is a Connect error returned by control-plane.
type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return "accesstokenclient: control-plane " + e.Code + ": " + e.Message
}

func (c *Client) call(ctx context.Context, method string, in, out any) error {
	if c.baseURL == "" {
		return errors.New("accesstokenclient: control-plane URL is not configured")
	}
	bearer, err := c.sign()
	if err != nil {
		return fmt.Errorf("accesstokenclient: signing internal-service token: %w", err)
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+ServicePath+method, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("accesstokenclient: %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("accesstokenclient: %s: reading response: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		rpcErr := &RPCError{Code: http.StatusText(resp.StatusCode)}
		_ = json.Unmarshal(body, rpcErr)
		return rpcErr
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("accesstokenclient: %s: decoding response: %w", method, err)
	}
	return nil
}

func scopeStrings(scopes []fat.Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, string(s))
	}
	return out
}

// numberOrZero parses a protojson int64 (encoded as a string or a number).
func numberOrZero(n json.Number) int64 {
	v, err := n.Int64()
	if err != nil {
		return 0
	}
	return v
}

// CachedIntrospector caches Introspect answers — positive AND negative — for
// CacheTTL, keyed by the token's SHA-256 (never the plaintext). Transport
// errors are NOT cached: our outage must not pin a live token as dead.
type CachedIntrospector struct {
	inner Introspector
	now   func() time.Time
	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	principal *fat.Principal
	inactive  bool
	expires   time.Time
}

// NewCachedIntrospector wraps inner with the CacheTTL cache.
func NewCachedIntrospector(inner Introspector) *CachedIntrospector {
	return &CachedIntrospector{inner: inner, now: time.Now, cache: map[string]cacheEntry{}}
}

// Introspect answers from the cache when fresh, else asks inner.
func (c *CachedIntrospector) Introspect(ctx context.Context, token string) (*fat.Principal, error) {
	key := fat.Hash(token)
	now := c.now()

	c.mu.Lock()
	if e, ok := c.cache[key]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		if e.inactive {
			return nil, ErrInactive
		}
		return e.principal, nil
	}
	c.mu.Unlock()

	p, err := c.inner.Introspect(ctx, token)
	switch {
	case errors.Is(err, ErrInactive):
		c.store(key, cacheEntry{inactive: true, expires: now.Add(CacheTTL)})
	case err == nil:
		c.store(key, cacheEntry{principal: p, expires: now.Add(CacheTTL)})
	}
	return p, err
}

func (c *CachedIntrospector) store(key string, e cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Opportunistic sweep so the map stays bounded by live traffic.
	if len(c.cache) > 10000 {
		now := c.now()
		for k, v := range c.cache {
			if !now.Before(v.expires) {
				delete(c.cache, k)
			}
		}
	}
	c.cache[key] = e
}
