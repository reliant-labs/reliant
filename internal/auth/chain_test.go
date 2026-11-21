// Copyright (c) 2025 Reliant Labs
package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	forgeauth "github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/authn"
	"github.com/reliant-labs/forge/pkg/session"

	"github.com/reliant-labs/reliant/internal/runenv"
)

// The two issuers of these tests: a Supabase project and a managed identity
// (Zitadel) instance.
const (
	testSupabaseIssuer = "https://project.supabase.co/auth/v1"
	testIdentityIssuer = "https://reliant.reliantidentity.com"
	testIdentityAud    = "312309114829078531" // a Zitadel project id
	testDevSecret      = "dev-signer-secret-at-least-32-bytes!"
)

// testIdentity is a managed identity's issuer: an RSA key behind a JWKS.
type testIdentity struct {
	key     *rsa.PrivateKey
	jwksURL string
}

func newTestIdentity(t *testing.T) *testIdentity {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": "identity-1", "alg": "RS256", "use": "sig",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
	}))
	t.Cleanup(srv.Close)
	return &testIdentity{key: key, jwksURL: srv.URL}
}

// sign mints an RS256 token with the identity's key, over claims merged onto
// a valid identity token's.
func (ti *testIdentity) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	return signRS256(t, ti.key, "identity-1", identityClaimsFor(claims))
}

func identityClaimsFor(over map[string]any) jwt.MapClaims {
	c := jwt.MapClaims{
		"iss":            testIdentityIssuer,
		"aud":            []string{testIdentityAud},
		"sub":            "user-1",
		"email":          "person@example.com",
		"email_verified": true,
		"client_id":      "312309114829078532@reliant", // Zitadel stamps the app on every token
		"exp":            time.Now().Add(time.Hour).Unix(),
		"iat":            time.Now().Unix(),
	}
	for k, v := range over {
		if v == nil {
			delete(c, k)
			continue
		}
		c[k] = v
	}
	return c
}

func signRS256(t *testing.T, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func supabaseClaims(over map[string]any) map[string]any {
	c := map[string]any{
		"iss":   testSupabaseIssuer,
		"aud":   "authenticated",
		"sub":   "user-1",
		"email": "person@example.com",
		"role":  "authenticated",
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
	for k, v := range over {
		if v == nil {
			delete(c, k)
			continue
		}
		c[k] = v
	}
	return c
}

// newChainFixture is a deployment that accepts both issuers, as the
// migration window runs: Supabase (with its iss and aud checked) and the
// managed identity. It returns the settings, a signer of Supabase tokens
// over supabaseClaims, the identity, and the Supabase private key.
func newChainFixture(t *testing.T) (Settings, func(map[string]any) string, *testIdentity, *ecdsa.PrivateKey) {
	t.Helper()
	supaKey, supaPEM := newTestKeyAndPEM(t)
	ti := newTestIdentity(t)
	s := Settings{
		PublicKeyPEM:     supaPEM,
		SupabaseIssuer:   testSupabaseIssuer,
		SupabaseAudience: "authenticated",
		Identity: IdentitySettings{
			Issuer:   testIdentityIssuer,
			Audience: testIdentityAud,
			JWKSURL:  ti.jwksURL,
		},
	}
	supabase := func(claims map[string]any) string { return signES256(t, supaKey, supabaseClaims(claims)) }
	return s, supabase, ti, supaKey
}

func mustAuthenticator(t *testing.T, s Settings) *Authenticator {
	t.Helper()
	a, err := NewAuthenticator(context.Background(), s)
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	return a
}

// TestTokenChainNeverConfusesItsIssuers is the cross-chain token-confusion
// matrix: with both issuers configured, a token is accepted only by the one
// validator its iss names, with that validator's own key, alg and audience,
// and its Credential follows the validator that proved it.
func TestTokenChainNeverConfusesItsIssuers(t *testing.T) {
	s, supabase, ti, supaKey := newChainFixture(t)
	a := mustAuthenticator(t, s)
	otherRSA, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	otherEC, _ := newTestKeyAndPEM(t)
	pubDER, err := x509.MarshalPKIXPublicKey(&ti.key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	identityPubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	for _, tc := range []struct {
		name   string
		token  string
		cred   Credential // CredentialUnknown: refused
		source Source
	}{
		{name: "a Supabase session", token: supabase(nil), cred: CredentialSession, source: SourceSupabase},
		{name: "a Supabase OAuth client token", token: supabase(map[string]any{"client_id": "mcp-client"}), cred: CredentialOAuthClient, source: SourceSupabase},
		{name: "a managed identity token is software's", token: ti.sign(t, nil), cred: CredentialIdentityToken, source: SourceIdentity},
		{name: "an identity token naming an OAuth client is still an identity token", token: ti.sign(t, map[string]any{"client_id": "mcp-client"}), cred: CredentialIdentityToken, source: SourceIdentity},

		// Supabase's own checks, now that its issuer is known.
		{name: "Supabase key, another issuer", token: supabase(map[string]any{"iss": "https://other.supabase.co/auth/v1"})},
		{name: "Supabase key, no issuer", token: supabase(map[string]any{"iss": nil})},
		{name: "Supabase key, another audience", token: supabase(map[string]any{"aud": "anon"})},
		{name: "Supabase key, expired", token: supabase(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})},

		// The identity's own checks.
		{name: "identity issuer, another project's audience", token: ti.sign(t, map[string]any{"aud": []string{"another-project"}})},
		{name: "identity issuer, no audience", token: ti.sign(t, map[string]any{"aud": nil})},
		{name: "identity issuer, expired", token: ti.sign(t, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})},
		{name: "identity issuer, a key the issuer never published", token: signRS256(t, otherRSA, "identity-1", identityClaimsFor(nil))},
		{name: "identity issuer, no subject", token: ti.sign(t, map[string]any{"sub": nil})},

		// Confusion: a token never reaches the validator it was not issued by.
		{name: "identity issuer, signed with the Supabase key", token: signES256(t, supaKey, map[string]any(identityClaimsFor(nil)))},
		{name: "Supabase issuer, signed with the identity's key", token: signRS256(t, ti.key, "identity-1", jwt.MapClaims(supabaseClaims(nil)))},
		{name: "another identity instance's issuer, the identity's key", token: ti.sign(t, map[string]any{"iss": "https://other.reliantidentity.com"})},
		{name: "identity issuer, another EC key", token: signES256(t, otherEC, map[string]any(identityClaimsFor(nil)))},
		{name: "identity issuer, HS256 keyed with the identity's public key", token: signHS256(t, string(identityPubPEM), identityClaimsFor(nil))},
		{name: "Supabase issuer, HS256 with no dev signer configured", token: signHS256(t, testDevSecret, jwt.MapClaims(supabaseClaims(nil)))},
		{name: "identity issuer, alg none", token: unsignedToken(t, identityClaimsFor(nil))},
		{name: "Supabase issuer, alg none", token: unsignedToken(t, jwt.MapClaims(supabaseClaims(nil)))},
		{name: "an rlat_ access token", token: "rlat_" + strings.Repeat("a", 40)},
		{name: "not a token", token: "hello"},
		{name: "a session id with no session store", token: mustSessionID(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := a.Verify(context.Background(), tc.token, authn.ChannelAuthorization)
			if tc.cred == CredentialUnknown {
				if err == nil {
					t.Fatalf("Verify accepted it as %s from %s; want it refused", v.Claims.Credential, v.Source)
				}
				if !IsCredentialRejection(err) {
					t.Fatalf("Verify = %v; want a credential rejection, not an outage", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if v.Claims.Credential != tc.cred || v.Source != tc.source {
				t.Fatalf("Verify = %s from %s; want %s from %s", v.Claims.Credential, v.Source, tc.cred, tc.source)
			}
			if v.Claims.Sub != "user-1" {
				t.Fatalf("sub = %q, want user-1", v.Claims.Sub)
			}
			if v.Method.Channel != authn.ChannelAuthorization || v.Method.Credential != forgeauth.CredentialToken {
				t.Fatalf("method = %+v; want a token on the Authorization header", v.Method)
			}
			if v.Method.IsBrowserSession() {
				t.Fatal("a bearer token passed as a browser session")
			}
		})
	}
}

func unsignedToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustSessionID(t *testing.T) string {
	t.Helper()
	id, err := session.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestTokenChainVerdictCannotBeForged: the chain's verdict rides the claims
// under a key, as a typed value; a token's own claim of that name is a
// string, and decides nothing.
func TestTokenChainVerdictCannotBeForged(t *testing.T) {
	s, _, ti, _ := newChainFixture(t)
	a := mustAuthenticator(t, s)
	forged := ti.sign(t, map[string]any{verdictKey: map[string]any{"source": "supabase"}, "reliant.verdict": "supabase"})
	v, err := a.Verify(context.Background(), forged, authn.ChannelAuthorization)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Source != SourceIdentity || v.Claims.Credential != CredentialIdentityToken {
		t.Fatalf("Verify = %s from %s; a claim named like the verdict changed it", v.Claims.Credential, v.Source)
	}
	if _, ok := verdictOf(&forgeauth.Claims{Raw: map[string]any{verdictKey: "supabase"}}); ok {
		t.Fatal("verdictOf read a string as a verdict")
	}
}

// TestTokenChainIsInertWithoutAnIdentity: with no jwt_issuer, the chain is
// the Supabase validator it replaced: a managed identity's token is refused,
// and a Supabase session is what it always was.
func TestTokenChainIsInertWithoutAnIdentity(t *testing.T) {
	s, supabase, ti, _ := newChainFixture(t)
	s.Identity = IdentitySettings{}
	s.SupabaseIssuer, s.SupabaseAudience = "", ""
	a := mustAuthenticator(t, s)
	if v, err := a.Verify(context.Background(), ti.sign(t, nil), authn.ChannelAuthorization); err == nil {
		t.Fatalf("an identity token was accepted as %s with no identity configured", v.Claims.Credential)
	}
	// iss and aud are checked only when configured: unset, a token naming
	// any is accepted, as before.
	v, err := a.Verify(context.Background(), supabase(map[string]any{"iss": "anything", "aud": "anything"}), authn.ChannelAuthorization)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Claims.Credential != CredentialSession || v.Source != SourceSupabase {
		t.Fatalf("Verify = %s from %s; want a Supabase session", v.Claims.Credential, v.Source)
	}
}

// TestTokenChainRefusesAnIdentityWithoutAudience: an issuer alone would
// accept every project's tokens of the same instance.
func TestTokenChainRefusesAnIdentityWithoutAudience(t *testing.T) {
	s, _, _, _ := newChainFixture(t)
	s.Identity.Audience = ""
	if _, err := NewAuthenticator(context.Background(), s); err == nil {
		t.Fatal("NewAuthenticator accepted JWT_ISSUER without JWT_AUDIENCE")
	}
	s.Mode, s.APIKey = ModeAPIKey, "shared-key"
	s.Identity.Audience = testIdentityAud
	if _, err := NewAuthenticator(context.Background(), s); err == nil {
		t.Fatal("NewAuthenticator accepted a managed identity in AUTH_MODE=apikey")
	}
}

// TestTokenChainAcceptsTheIdentityAlone: a deployment may accept the managed
// identity with no Supabase key at all; then every other token is refused.
func TestTokenChainAcceptsTheIdentityAlone(t *testing.T) {
	s, supabase, ti, _ := newChainFixture(t)
	s.PublicKeyPEM = ""
	a := mustAuthenticator(t, s)
	if _, err := a.Verify(context.Background(), ti.sign(t, nil), authn.ChannelAuthorization); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if _, err := a.Verify(context.Background(), supabase(nil), authn.ChannelAuthorization); err == nil {
		t.Fatal("a Supabase token was accepted with no Supabase key configured")
	}
}

// TestDevSignerStaysBesideTheChain: the dev tier's HS256 tokens are a
// session from the dev signer, and only with the secret configured.
func TestDevSignerStaysBesideTheChain(t *testing.T) {
	runenv.ConfigureForTest(t, "dev")
	s, _, _, _ := newChainFixture(t)
	s.DevJWTSecret = testDevSecret
	a := mustAuthenticator(t, s)
	dev := signHS256(t, testDevSecret, jwt.MapClaims(supabaseClaims(nil)))
	v, err := a.Verify(context.Background(), dev, authn.ChannelAuthorization)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Source != SourceDevSigner || v.Claims.Credential != CredentialSession {
		t.Fatalf("Verify = %s from %s; want a session from the dev signer", v.Claims.Credential, v.Source)
	}
	// The dev secret never verifies a token claiming the identity's issuer.
	if _, err := a.Verify(context.Background(), signHS256(t, testDevSecret, identityClaimsFor(nil)), authn.ChannelAuthorization); err == nil {
		t.Fatal("an HS256 token claiming the identity's issuer was accepted")
	}
}

// fakeSessions is a SessionResolver over a fixed set of sessions, and an
// unavailable store.
type fakeSessions struct {
	tokens      map[string]string
	unavailable bool
}

func (f fakeSessions) AccessToken(_ context.Context, id string) (string, error) {
	if f.unavailable {
		return "", session.ErrUnavailable
	}
	tok, ok := f.tokens[id]
	if !ok {
		return "", session.ErrNoSession
	}
	return tok, nil
}

// TestSessionIDOnTheAuthorizationHeaderIsSoftware: a server-side session id
// is resolved through the session store, and its token checked by the chain;
// presented as a bearer, it is a program holding the user's session, never
// the person's browser.
func TestSessionIDOnTheAuthorizationHeaderIsSoftware(t *testing.T) {
	s, supabase, ti, _ := newChainFixture(t)
	live, ended, supa := mustSessionID(t), mustSessionID(t), mustSessionID(t)
	s.Sessions = fakeSessions{tokens: map[string]string{live: ti.sign(t, nil), supa: supabase(nil)}}
	a := mustAuthenticator(t, s)

	v, err := a.Verify(context.Background(), live, authn.ChannelAuthorization)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Method.Credential != forgeauth.CredentialSession || v.Method.IsBrowserSession() {
		t.Fatalf("method = %+v; want a session that is not a browser session", v.Method)
	}
	if v.Claims.Credential != CredentialIdentityToken {
		t.Fatalf("credential = %s; a session id on the Authorization header must be software's", v.Claims.Credential)
	}

	if _, err := a.Verify(context.Background(), ended, authn.ChannelAuthorization); err == nil || !IsCredentialRejection(err) {
		t.Fatalf("an ended session: Verify = %v; want a credential rejection", err)
	}
	// A session holding a Supabase token is not one the broker writes.
	if v, err := a.Verify(context.Background(), supa, authn.ChannelAuthorization); err == nil {
		t.Fatalf("a session holding a Supabase token was accepted as %s", v.Claims.Credential)
	}

	s.Sessions = fakeSessions{unavailable: true}
	down := mustAuthenticator(t, s)
	_, err = down.Verify(context.Background(), live, authn.ChannelAuthorization)
	if !errors.Is(err, ErrSessionUnavailable) || IsCredentialRejection(err) {
		t.Fatalf("an unreachable session store: Verify = %v; want ErrSessionUnavailable, not a rejection", err)
	}
}

// TestTokenChainRoutesByIssuer: a token claiming the managed identity's
// issuer is judged by the identity's validator alone, even where Supabase's
// validator would accept it — as it would today, with no SUPABASE_JWT_ISSUER
// set. Tried in turn (forge's MultiValidator), a token signed with Supabase's
// key under the identity's issuer would pass as a Supabase session.
func TestTokenChainRoutesByIssuer(t *testing.T) {
	s, _, _, supaKey := newChainFixture(t)
	s.SupabaseIssuer, s.SupabaseAudience = "", ""
	a := mustAuthenticator(t, s)
	tok := signES256(t, supaKey, map[string]any(identityClaimsFor(nil)))
	if v, err := a.Verify(context.Background(), tok, authn.ChannelAuthorization); err == nil {
		t.Fatalf("a Supabase-signed token claiming the identity's issuer was accepted as %s from %s", v.Claims.Credential, v.Source)
	}
}
