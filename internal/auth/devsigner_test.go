package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/reliant-labs/reliant/internal/runenv"
)

// devSecret is control-plane's dev literal (internal/devjwt.DefaultSecret
// there): what its `auth mint-dev-token` signs with.
const devSecret = "dev-local-hmac-shared-secret-do-not-use-in-prod"

// signHS256 mints a token the way control-plane's devjwt.Mint does.
func signHS256(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// refTime is the fixed "now" every token here is minted and checked against:
// no assertion depends on the wall clock.
var refTime = time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)

// farFuture is an exp no wall clock reaches, for tokens checked by the session
// validator, whose clock is the real one.
var farFuture = time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC)

// atRefTime pins v's dev signer clock to refTime.
func atRefTime(t *testing.T, v TokenValidator) TokenValidator {
	t.Helper()
	d, ok := v.(*devSignerDispatch)
	if !ok {
		t.Fatalf("validator is %T, want the dev signer dispatch", v)
	}
	d.dev.now = func() time.Time { return refTime }
	return v
}

func devClaims(exp time.Time) jwt.MapClaims {
	return jwt.MapClaims{
		"sub":            "dev-smoke-user",
		"email":          "dev-smoke-user@dev.test",
		"email_verified": true,
		"role":           "authenticated",
		"aud":            "authenticated",
		"iss":            "https://umoxnokojetwkrmttfqj.supabase.co/auth/v1",
		"iat":            refTime.Unix(),
		"exp":            exp.Unix(),
	}
}

// devAndSession is the session validator (a PEM key) with the dev signer
// beside it, in the dev tier, plus a signer for real session tokens.
func devAndSession(t *testing.T) (TokenValidator, func(map[string]any) string) {
	t.Helper()
	runenv.ConfigureForTest(t, "dev")
	key, pubPEM := newTestKeyAndPEM(t)
	session, err := NewJWTValidator(pubPEM)
	if err != nil {
		t.Fatal(err)
	}
	v, err := WithDevSigner(session, Settings{DevJWTSecret: devSecret})
	if err != nil {
		t.Fatalf("WithDevSigner in the dev tier: %v", err)
	}
	return atRefTime(t, v), func(claims map[string]any) string { return signES256(t, key, claims) }
}

func TestDevSignerAcceptsMintedTokenAsSession(t *testing.T) {
	v, _ := devAndSession(t)
	exp := refTime.Add(time.Hour)
	claims, err := v.ValidateToken(signHS256(t, devSecret, devClaims(exp)))
	if err != nil {
		t.Fatalf("a token minted with the dev secret was refused: %v", err)
	}
	if claims.Sub != "dev-smoke-user" || claims.Email != "dev-smoke-user@dev.test" || claims.Role != "authenticated" {
		t.Errorf("claims = %+v, want the minted sub, email and role", claims)
	}
	if claims.Exp != exp.Unix() {
		t.Errorf("Exp = %d, want %d", claims.Exp, exp.Unix())
	}
	if claims.Credential != CredentialSession {
		t.Errorf("Credential = %v, want CredentialSession: a dev sign-in stands in for a browser session", claims.Credential)
	}
}

// The dev signer sits beside the session key: a real ES256 session token
// still validates through the same validator.
func TestDevSignerKeepsSessionTokensWorking(t *testing.T) {
	v, signSession := devAndSession(t)
	claims, err := v.ValidateToken(signSession(map[string]any{
		"sub": "real-user", "email": "real@example.com", "exp": farFuture.Unix(),
	}))
	if err != nil {
		t.Fatalf("an ES256 session token was refused once the dev signer was added: %v", err)
	}
	if claims.Sub != "real-user" {
		t.Errorf("Sub = %q, want real-user", claims.Sub)
	}
}

func TestDevSignerRejections(t *testing.T) {
	v, _ := devAndSession(t)
	noExp := devClaims(refTime)
	delete(noExp, "exp")
	noSub := devClaims(refTime.Add(time.Hour))
	delete(noSub, "sub")
	for _, tc := range []struct {
		name  string
		token string
		want  error
	}{
		{"wrong secret", signHS256(t, "some-other-secret", devClaims(refTime.Add(time.Hour))), ErrInvalidSignature},
		{"expired", signHS256(t, devSecret, devClaims(refTime.Add(-time.Minute))), ErrExpiredToken},
		{"no exp", signHS256(t, devSecret, noExp), ErrInvalidToken},
		{"no sub", signHS256(t, devSecret, noSub), ErrInvalidToken},
		// Another HMAC alg is not the dev signer's: it goes to the session
		// validator, which accepts ES256 alone.
		{"HS512", func() string {
			s, err := jwt.NewWithClaims(jwt.SigningMethodHS512, devClaims(refTime.Add(time.Hour))).SignedString([]byte(devSecret))
			if err != nil {
				t.Fatal(err)
			}
			return s
		}(), ErrInvalidToken},
		{"not a JWT", "not-a-jwt", ErrInvalidToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.ValidateToken(tc.token)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !IsCredentialRejection(err) {
				t.Errorf("err = %v is not a credential rejection: the caller would report an outage instead of a bad token", err)
			}
		})
	}
}

// Without AUTH_DEV_JWT_SECRET nothing changes: the session validator comes
// back as it went in, and an HS256 token is refused by it.
func TestNoDevSignerLeavesSessionValidatorAlone(t *testing.T) {
	runenv.ConfigureForTest(t, "dev")
	_, pubPEM := newTestKeyAndPEM(t)
	session, err := NewJWTValidator(pubPEM)
	if err != nil {
		t.Fatal(err)
	}
	v, err := WithDevSigner(session, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if v != TokenValidator(session) {
		t.Fatalf("WithDevSigner without a secret returned %T, want the session validator itself", v)
	}
	if _, err := v.ValidateToken(signHS256(t, devSecret, devClaims(refTime.Add(time.Hour)))); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("an HS256 token with no dev signer configured: err = %v, want ErrInvalidToken", err)
	}
}

// Fail closed: a dev signer outside the dev tier is a configuration error, so
// no environment variable alone turns it on in prod. Every tier but dev
// refuses it, an unset or unrecognised one included (runenv.Parse is prod).
func TestDevSignerIsRefusedOutsideTheDevTier(t *testing.T) {
	_, pubPEM := newTestKeyAndPEM(t)
	session, err := NewJWTValidator(pubPEM)
	if err != nil {
		t.Fatal(err)
	}
	settings := Settings{DevJWTSecret: devSecret}
	for _, env := range []string{"", "prod", "production", "staging", "test", "nonprod"} {
		t.Run("RELIANT_ENV="+env, func(t *testing.T) {
			runenv.ConfigureForTest(t, env)
			if err := CheckDevSigner(settings); !errors.Is(err, ErrDevSignerOutsideDev) {
				t.Errorf("CheckDevSigner: err = %v, want ErrDevSignerOutsideDev", err)
			}
			v, err := WithDevSigner(session, settings)
			if !errors.Is(err, ErrDevSignerOutsideDev) || v != nil {
				t.Errorf("WithDevSigner = (%v, %v), want (nil, ErrDevSignerOutsideDev)", v, err)
			}
			if _, err := NewMiddleware(Settings{PublicKeyPEM: pubPEM, DevJWTSecret: devSecret}); !errors.Is(err, ErrDevSignerOutsideDev) {
				t.Errorf("NewMiddleware: err = %v, want ErrDevSignerOutsideDev", err)
			}
		})
	}
	// Unset is fine in every tier: the check costs nothing to a process
	// that never asked for the dev signer.
	runenv.ConfigureForTest(t, "prod")
	if err := CheckDevSigner(Settings{}); err != nil {
		t.Errorf("CheckDevSigner with no secret in prod: %v", err)
	}
}
