package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/apikey"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// fakeKeyStore is a KeyValidator over an in-memory "table" keyed by lookup
// prefix, storing only the hash — the shape the auth/api-keys skill tells
// projects to build over their database.
type fakeKeyStore struct {
	rows  map[string]string // lookup prefix -> apikey.Hash(key)
	calls int
	err   error // returned instead of a lookup, to model a store failure
	ctx   context.Context
}

func (s *fakeKeyStore) ValidateKey(ctx context.Context, key string) (*Claims, error) {
	s.calls++
	s.ctx = ctx
	if s.err != nil {
		return nil, s.err
	}
	prefix, err := apikey.LookupPrefix(key)
	if err != nil {
		return nil, err
	}
	hash, ok := s.rows[prefix]
	if !ok || !apikey.Verify(key, hash) {
		return nil, errors.New("no such key")
	}
	return &Claims{UserID: "key-owner"}, nil
}

func newKey(t *testing.T) (string, *fakeKeyStore) {
	t.Helper()
	key, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	prefix, err := apikey.LookupPrefix(key)
	if err != nil {
		t.Fatalf("lookup prefix: %v", err)
	}
	return key, &fakeKeyStore{rows: map[string]string{prefix: apikey.Hash(key)}}
}

// jwtOnly stands in for a JWT validator's Validate: it accepts exactly one
// token and records what it was asked about.
type jwtOnly struct{ seen []string }

func (j *jwtOnly) validate(token string) (*Claims, error) {
	j.seen = append(j.seen, token)
	if token == "good.jwt.token" {
		return &Claims{UserID: "jwt-user"}, nil
	}
	return nil, errors.New("signature is invalid")
}

// The credential's SHAPE picks the validator. A key goes to the key
// validator WITH the request context (it is a database read); anything
// else goes to the token validator; neither validator ever sees the other's
// credential.
func TestAuthenticator_RoutesByCredentialShape(t *testing.T) {
	t.Parallel()
	key, store := newKey(t)
	tokens := &jwtOnly{}
	a := NewAuthenticator(tokens.validate)
	a.UseKeyValidator(store)

	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "request")

	c, err := a.Authenticate(ctx, key)
	if err != nil || c == nil || c.UserID != "key-owner" {
		t.Fatalf("a valid API key must authenticate via the key validator, got %+v, %v", c, err)
	}
	if store.ctx == nil || store.ctx.Value(ctxKey{}) != "request" {
		t.Fatal("the key validator must receive the request's context")
	}

	c, err = a.Authenticate(ctx, "good.jwt.token")
	if err != nil || c == nil || c.UserID != "jwt-user" {
		t.Fatalf("a JWT must authenticate via the token validator, got %+v, %v", c, err)
	}

	if store.calls != 1 {
		t.Errorf("the key validator ran %d times, want 1 — it must never see a JWT", store.calls)
	}
	for _, seen := range tokens.seen {
		if strings.HasPrefix(seen, apikey.KeyPrefix) {
			t.Errorf("the token validator was handed an API key %q", seen)
		}
	}
}

// With no key validator installed — the scaffold's default — API keys are
// DISABLED, not unchecked: every fk_ credential is rejected and bearer
// tokens are unaffected.
func TestAuthenticator_KeysDisabledUntilInstalled(t *testing.T) {
	t.Parallel()
	key, _ := newKey(t)
	tokens := &jwtOnly{}
	a := NewAuthenticator(tokens.validate)

	if c, err := a.Authenticate(context.Background(), key); !errors.Is(err, ErrAPIKeysDisabled) || c != nil {
		t.Fatalf("an API key with no key validator must fail with ErrAPIKeysDisabled, got %+v, %v", c, err)
	}
	if len(tokens.seen) != 0 {
		t.Fatalf("a rejected API key must not fall through to the token validator, it saw %v", tokens.seen)
	}
	if _, err := a.Authenticate(context.Background(), "good.jwt.token"); err != nil {
		t.Fatalf("bearer tokens must keep working with keys disabled: %v", err)
	}

	// UseKeyValidator(nil) is how a project turns keys back off.
	_, store := newKey(t)
	a.UseKeyValidator(store)
	a.UseKeyValidator(nil)
	if _, err := a.Authenticate(context.Background(), key); !errors.Is(err, ErrAPIKeysDisabled) {
		t.Fatalf("UseKeyValidator(nil) must disable keys again, got %v", err)
	}
}

// A key too short to carry a lookup prefix is rejected before the key
// validator — and the store behind it — is consulted.
func TestAuthenticator_MalformedKeyNeverReachesTheStore(t *testing.T) {
	t.Parallel()
	_, store := newKey(t)
	a := NewAuthenticator((&jwtOnly{}).validate)
	a.UseKeyValidator(store)

	for _, bad := range []string{apikey.KeyPrefix, apikey.KeyPrefix + "abc"} {
		if _, err := a.Authenticate(context.Background(), bad); !errors.Is(err, ErrMalformedAPIKey) {
			t.Errorf("Authenticate(%q) = %v, want ErrMalformedAPIKey", bad, err)
		}
	}
	if store.calls != 0 {
		t.Fatalf("a malformed key reached the key validator %d times", store.calls)
	}
}

// A wrong key and a store failure both reach the caller as the same
// "invalid API key" — the text of a store error (a host, a DSN, a table
// name) is a diagnostic for the operator, never a message for an
// unauthenticated caller. The operator still gets it: svcerr.Cause is what
// the logging interceptor writes as the failure's cause.
func TestAuthenticator_KeyValidatorErrorsAreRedacted(t *testing.T) {
	t.Parallel()
	key, store := newKey(t)
	a := NewAuthenticator((&jwtOnly{}).validate)
	a.UseKeyValidator(store)

	wrong := key[:len(key)-1] + "!"
	_, err := a.Authenticate(context.Background(), wrong)
	if !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("a wrong key must fail with ErrInvalidAPIKey, got %v", err)
	}

	leak := errors.New(`dial tcp 10.0.0.7:5432: connect: connection refused (dsn=postgres://app:s3cr3t@db.internal/prod)`)
	store.err = leak
	_, err = a.Authenticate(context.Background(), key)
	if !errors.Is(err, ErrInvalidAPIKey) {
		t.Fatalf("a store failure must fail with ErrInvalidAPIKey, got %v", err)
	}
	if strings.Contains(err.Error(), "10.0.0.7") || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("a store error's text reached the caller-visible message: %q", err.Error())
	}
	if cause := svcerr.Cause(err); !errors.Is(cause, leak) {
		t.Fatalf("the store error must stay reachable for the operator's log via svcerr.Cause, got %v", cause)
	}
}

// A validator that reports success with no principal is a bug, and the
// Authenticator does not turn it into one: no claims is a failure.
func TestAuthenticator_NoClaimsIsAFailure(t *testing.T) {
	t.Parallel()
	key, _ := newKey(t)
	a := NewAuthenticator(func(string) (*Claims, error) { return nil, nil })
	a.UseKeyValidator(KeyValidatorFunc(func(context.Context, string) (*Claims, error) { return nil, nil }))

	if c, err := a.Authenticate(context.Background(), key); err == nil || c != nil {
		t.Fatalf("a key validator returning (nil, nil) must fail, got %+v, %v", c, err)
	}
	if c, err := a.Authenticate(context.Background(), "some.jwt.token"); err == nil || c != nil {
		t.Fatalf("a token validator returning (nil, nil) must fail, got %+v, %v", c, err)
	}
}

// A key-only service builds the Authenticator with no token validator: a
// non-key credential is then rejected rather than dereferencing nil.
func TestAuthenticator_NilTokenValidatorRejectsTokens(t *testing.T) {
	t.Parallel()
	key, store := newKey(t)
	a := NewAuthenticator(nil)
	a.UseKeyValidator(store)

	if _, err := a.Authenticate(context.Background(), "good.jwt.token"); !errors.Is(err, ErrBearerTokensDisabled) {
		t.Fatalf("with no token validator a bearer token must fail with ErrBearerTokensDisabled, got %v", err)
	}
	if _, err := a.Authenticate(context.Background(), key); err != nil {
		t.Fatalf("keys must still authenticate on a key-only Authenticator: %v", err)
	}
}

// UseKeyValidator runs once, after infra opens — but nothing stops it racing
// a request, so it must be safe to call while Authenticate is running.
// `go test -race` is the assertion.
func TestAuthenticator_UseKeyValidatorIsConcurrencySafe(t *testing.T) {
	t.Parallel()
	key, _ := newKey(t)
	a := NewAuthenticator((&jwtOnly{}).validate)
	accept := KeyValidatorFunc(func(context.Context, string) (*Claims, error) { return &Claims{UserID: "k"}, nil })

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			a.UseKeyValidator(accept)
		}()
		go func() {
			defer wg.Done()
			_, _ = a.Authenticate(context.Background(), key)
		}()
	}
	wg.Wait()
	if _, err := a.Authenticate(context.Background(), key); err != nil {
		t.Fatalf("after installation a key must authenticate: %v", err)
	}
}
