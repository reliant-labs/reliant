package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/apikey"
	"github.com/reliant-labs/forge/pkg/session"
)

// formatKeyStore is fakeKeyStore over a Format: the store a project that
// names its own key prefix builds, holding keys of both prefixes.
type formatKeyStore struct {
	format apikey.Format
	rows   map[string]string // lookup prefix -> apikey.Hash(key)
	calls  int
}

func (s *formatKeyStore) issue(t *testing.T, gen func() (string, error)) string {
	t.Helper()
	key, err := gen()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	prefix, err := s.format.LookupPrefix(key)
	if err != nil {
		t.Fatalf("lookup prefix of %q: %v", key, err)
	}
	s.rows[prefix] = apikey.Hash(key)
	return key
}

func (s *formatKeyStore) ValidateKey(_ context.Context, key string) (*Claims, error) {
	s.calls++
	prefix, err := s.format.LookupPrefix(key)
	if err != nil {
		return nil, err
	}
	hash, ok := s.rows[prefix]
	if !ok || !apikey.Verify(key, hash) {
		return nil, errors.New("no such key")
	}
	return &Claims{UserID: "key-owner"}, nil
}

// A project that names its own key prefix installs that Format on the
// authenticator: its keys route to the key validator, the "fk_" keys it
// issued before the rename still do, and JWTs and session ids still reach
// the validators for their kind.
func TestAuthenticator_UseKeyFormatRoutesCustomAndDefaultKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	format := apikey.MustFormat("acme_")
	store := &formatKeyStore{format: format, rows: map[string]string{}}
	legacy := store.issue(t, apikey.Generate)
	custom := store.issue(t, format.Generate)

	m, err := session.NewManager(session.Config{Store: session.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := m.Create(ctx, session.Tokens{AccessToken: "good.jwt.token", Expiry: time.Now().Add(time.Hour)}, session.Meta{Subject: "user-1"})
	if err != nil {
		t.Fatal(err)
	}

	tokens := &jwtOnly{}
	a := NewAuthenticator(tokens.validate)
	a.UseKeyValidator(store)
	a.UseSessions(m)

	// Before the Format is installed, the custom key is not a key to this
	// server: it is a bearer token, and the token validator refuses it.
	if _, err := a.Authenticate(ctx, custom); err == nil {
		t.Fatal("a custom-prefix key authenticated with no key format installed")
	}
	if store.calls != 0 {
		t.Fatalf("the key validator saw a credential with an unknown prefix")
	}
	tokens.seen = nil

	a.UseKeyFormat(format)
	for name, key := range map[string]string{"custom": custom, "fk_ issued before the rename": legacy} {
		c, err := a.Authenticate(ctx, key)
		if err != nil || c == nil || c.UserID != "key-owner" {
			t.Fatalf("%s key %q: got %+v, %v", name, key, c, err)
		}
		want, _ := format.LookupPrefix(key)
		if !c.IsAPIKey() || c.Credential.KeyPrefix() != want {
			t.Errorf("%s key: credential = %v/%q, want an API key with lookup prefix %q", name, c.Credential.Kind(), c.Credential.KeyPrefix(), want)
		}
	}
	if store.calls != 2 {
		t.Errorf("the key validator ran %d times, want 2", store.calls)
	}

	if c, err := a.Authenticate(ctx, "good.jwt.token"); err != nil || c.Credential.Kind() != CredentialToken {
		t.Fatalf("a JWT must still reach the token validator: %+v, %v", c, err)
	}
	if c, err := a.Authenticate(ctx, sess.ID); err != nil || !c.IsSession() {
		t.Fatalf("a session id must still resolve through the session resolver: %+v, %v", c, err)
	}
	if store.calls != 2 {
		t.Errorf("the key validator saw a JWT or session id (%d calls)", store.calls)
	}
	for _, seen := range tokens.seen {
		if format.IsKey(seen) {
			t.Errorf("the token validator was handed an API key %q", seen)
		}
	}
}

// A custom-prefix credential too short to carry a lookup prefix fails closed
// as a malformed key, before the store — as an "fk_" one does.
func TestAuthenticator_MalformedCustomKeyNeverReachesTheStore(t *testing.T) {
	t.Parallel()
	format := apikey.MustFormat("acme_")
	store := &formatKeyStore{format: format, rows: map[string]string{}}
	tokens := &jwtOnly{}
	a := NewAuthenticator(tokens.validate)
	a.UseKeyValidator(store)
	a.UseKeyFormat(format)

	for _, bad := range []string{"acme_", "acme_abc", apikey.KeyPrefix + "abc"} {
		if _, err := a.Authenticate(context.Background(), bad); !errors.Is(err, ErrMalformedAPIKey) {
			t.Errorf("Authenticate(%q) = %v, want ErrMalformedAPIKey", bad, err)
		}
	}
	if store.calls != 0 || len(tokens.seen) != 0 {
		t.Fatalf("a malformed key reached a validator: store %d calls, tokens saw %v", store.calls, tokens.seen)
	}
}

func TestAuthenticator_UseKeyFormatIsConcurrencySafe(t *testing.T) {
	t.Parallel()
	format := apikey.MustFormat("acme_")
	store := &formatKeyStore{format: format, rows: map[string]string{}}
	key := store.issue(t, format.Generate)
	a := NewAuthenticator(nil)
	a.UseKeyValidator(KeyValidatorFunc(func(context.Context, string) (*Claims, error) {
		return &Claims{UserID: "key-owner"}, nil
	}))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			a.UseKeyFormat(format)
		}()
		go func() {
			defer wg.Done()
			_, _ = a.Authenticate(context.Background(), key)
		}()
	}
	wg.Wait()
	if c, err := a.Authenticate(context.Background(), key); err != nil || !c.IsAPIKey() {
		t.Fatalf("after UseKeyFormat the key must authenticate as a key: %+v, %v", c, err)
	}
}
