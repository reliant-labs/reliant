package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// UserInfo fills [Claims.Email] from the issuer's OIDC userinfo endpoint
// when a validated token carries no email of its own.
//
// Zitadel, the engine behind a forge managed identity, issues access tokens
// with a subject, an audience and roles but no `email`: the address is in the
// ID token and at the userinfo endpoint, not in the token a request presents.
// An app that binds a first sign-in to an invitation, or seeds a row for a
// known person, needs the address in its enrich seam, and the only thing it
// has there is the subject. Install a UserInfo with
// [Authenticator.UseUserInfo] and Claims.Email arrives filled instead.
//
// Only a VERIFIED address is filled. Userinfo reports `email_verified`, and an
// address the identity provider has not verified is a string the user typed:
// binding an invitation to it would hand the invitation to whoever registered
// that string first. A token that carries an `email` claim itself is left as
// it is.
//
// A UserInfo answers for ONE issuer. A subject is unique only within its
// issuer, and an Authenticator may validate tokens from several (see
// [NewMultiValidator]): a token whose `iss` claim is not that issuer — or
// carries none — is never looked up and never read from the cache, so issuer
// B's subject "123" cannot receive issuer A's subject "123"'s address.
//
// The bearer token is sent only over HTTPS. A plain-HTTP userinfo URL is
// refused before any request is made unless its host is loopback (a local
// development issuer), and a redirect to an insecure URL is refused the same
// way, whatever client was supplied.
//
// The answer is cached per subject for [UserInfoTTL], so a signed-in user
// costs one userinfo request per TTL, not one per request. A failed or
// unanswerable lookup never fails authentication — the token was valid —
// and is retried after [userInfoRetry]. The endpoint is resolved lazily, on
// the first lookup, so a server whose issuer is unreachable at boot still
// starts; it is retried the same way until it resolves.
type UserInfo struct {
	issuer   string
	endpoint func(ctx context.Context) (string, error)
	client   *http.Client
	now      func() time.Time

	mu         sync.Mutex
	resolved   string
	retryAfter time.Time // no endpoint resolution before this
	cache      map[string]userInfoEntry
}

type userInfoEntry struct {
	email   string // "" when the account has no verified address
	expires time.Time
}

const (
	// UserInfoTTL is how long a subject's userinfo answer is reused.
	UserInfoTTL = 5 * time.Minute
	// userInfoRetry is how long a failed lookup or endpoint resolution is
	// remembered before the next attempt, so an outage costs one request per
	// subject per interval rather than one per request.
	userInfoRetry = 30 * time.Second
	// userInfoTimeout bounds one userinfo request.
	userInfoTimeout = 5 * time.Second
	// userInfoMaxEntries bounds the cache; past it the cache starts over.
	userInfoMaxEntries = 10000
)

// NewUserInfo returns a UserInfo for tokens issued by issuer (compared with
// the token's `iss` claim exactly), whose endpoint is the URL endpoint
// returns — typically the `userinfo_endpoint` of issuer's OIDC discovery
// document. An empty issuer matches no token. client nil uses
// http.DefaultClient; either way, redirects to an insecure URL are refused.
func NewUserInfo(issuer string, endpoint func(ctx context.Context) (string, error), client *http.Client) *UserInfo {
	if client == nil {
		client = http.DefaultClient
	}
	secured := *client
	next := client.CheckRedirect
	secured.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := secureEndpoint(req.URL); err != nil {
			return fmt.Errorf("userinfo: refusing redirect: %w", err)
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("userinfo: stopped after 10 redirects")
		}
		return nil
	}
	return &UserInfo{issuer: issuer, endpoint: endpoint, client: &secured, now: time.Now, cache: map[string]userInfoEntry{}}
}

// secureEndpoint reports whether a bearer token may be sent to u: HTTPS, or
// plain HTTP to a loopback host only.
func secureEndpoint(u *url.URL) error {
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("userinfo URL %q is plain HTTP to a non-loopback host", u.Redacted())
	}
	return fmt.Errorf("userinfo URL %q is not HTTPS", u.Redacted())
}

// issuerOf returns the token's `iss` claim, "" when it carries none.
func issuerOf(claims *Claims) string {
	iss, _ := claims.Raw["iss"].(string)
	return iss
}

// fill sets claims.Email from the subject's userinfo answer when the token
// carried none. token is the access token the request presented; userinfo
// is asked with it, so the answer is the token holder's own.
func (u *UserInfo) fill(ctx context.Context, token string, claims *Claims) {
	if claims == nil || claims.Email != "" || claims.UserID == "" {
		return
	}
	// The endpoint and the cache answer for u.issuer's subjects only.
	if u.issuer == "" || issuerOf(claims) != u.issuer {
		return
	}
	now := u.now()
	u.mu.Lock()
	if e, ok := u.cache[claims.UserID]; ok && now.Before(e.expires) {
		u.mu.Unlock()
		claims.Email = e.email
		return
	}
	u.mu.Unlock()

	email, err := u.lookup(ctx, token, claims.UserID)
	ttl := UserInfoTTL
	if err != nil {
		ttl = userInfoRetry
	}
	u.mu.Lock()
	if len(u.cache) >= userInfoMaxEntries {
		u.cache = map[string]userInfoEntry{}
	}
	u.cache[claims.UserID] = userInfoEntry{email: email, expires: now.Add(ttl)}
	u.mu.Unlock()
	claims.Email = email
}

// lookup asks the userinfo endpoint for subject's verified address.
func (u *UserInfo) lookup(ctx context.Context, token, subject string) (string, error) {
	endpoint, err := u.resolve(ctx)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, userInfoTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo: %s", resp.Status)
	}
	var info struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return "", fmt.Errorf("userinfo: %w", err)
	}
	// OIDC Core §5.3.2: the sub in the response MUST be checked against the
	// token's. A mismatch is a response about someone else.
	if info.Sub != subject {
		return "", errors.New("userinfo: the response is for a different subject")
	}
	if !verified(info.EmailVerified) {
		return "", nil
	}
	return info.Email, nil
}

// verified reads email_verified, which some providers send as a string.
func verified(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "true"
	}
	return false
}

// resolve returns the endpoint, resolving it on first use and remembering a
// failure for userInfoRetry.
func (u *UserInfo) resolve(ctx context.Context) (string, error) {
	u.mu.Lock()
	if u.resolved != "" {
		defer u.mu.Unlock()
		return u.resolved, nil
	}
	if u.now().Before(u.retryAfter) {
		u.mu.Unlock()
		return "", errors.New("userinfo: endpoint not resolved yet")
	}
	u.mu.Unlock()

	endpoint, err := u.endpoint(ctx)
	if err == nil && endpoint == "" {
		err = errors.New("userinfo: the issuer publishes no userinfo_endpoint")
	}
	if err == nil {
		var parsed *url.URL
		if parsed, err = url.Parse(endpoint); err == nil {
			err = secureEndpoint(parsed)
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if err != nil {
		u.retryAfter = u.now().Add(userInfoRetry)
		return "", err
	}
	u.resolved = endpoint
	return endpoint, nil
}
