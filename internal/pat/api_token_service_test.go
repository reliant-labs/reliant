// Copyright (c) 2025 Reliant Labs
package pat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// fakeStore is an in-memory Store for unit tests. Unlike the real store's
// GetDaemonPATByTokenHash (which filters revoked/expired in SQL), it returns
// rows regardless of state so the validator's own defense-in-depth checks are
// exercised.
type fakeStore struct {
	mu     sync.Mutex
	tokens map[string]*db.DaemonPAT // by ID

	lastUsedCalls []string
	failLookups   bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{tokens: map[string]*db.DaemonPAT{}}
}

func (f *fakeStore) CreateDaemonPAT(_ context.Context, p *db.DaemonPAT) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *p
	if cp.Kind == "" {
		cp.Kind = db.DaemonPATKindDaemon
	}
	f.tokens[p.ID] = &cp
	return nil
}

func (f *fakeStore) GetDaemonPATByTokenHash(_ context.Context, tokenHash string) (*db.DaemonPAT, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failLookups {
		return nil, errors.New("store down")
	}
	for _, t := range f.tokens {
		if t.TokenHash == tokenHash {
			cp := *t
			return &cp, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) ListDaemonPATsByUserIDAndKind(_ context.Context, userID, kind string) ([]*db.DaemonPAT, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*db.DaemonPAT
	for _, t := range f.tokens {
		if t.UserID == userID && t.Kind == kind {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeStore) RevokeDaemonPATByUserID(_ context.Context, userID, id, kind string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[id]
	if !ok || t.UserID != userID || t.Kind != kind || t.RevokedAt != nil {
		return false, nil
	}
	now := time.Now().UTC()
	t.RevokedAt = &now
	return true, nil
}

func (f *fakeStore) RevokeDaemonPATsByUserID(_ context.Context, userID string, ephemeralOnly bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	for _, t := range f.tokens {
		if t.UserID == userID && t.RevokedAt == nil && (!ephemeralOnly || t.Ephemeral) {
			t.RevokedAt = &now
		}
	}
	return nil
}

func (f *fakeStore) RevokeDaemonPATsByDaemonID(_ context.Context, daemonID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	n := 0
	for _, t := range f.tokens {
		if t.DaemonID == daemonID && t.RevokedAt == nil {
			t.RevokedAt = &now
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) UpdateDaemonPATLastUsed(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastUsedCalls = append(f.lastUsedCalls, id)
	if t, ok := f.tokens[id]; ok {
		now := time.Now().UTC()
		t.LastUsedAt = &now
	}
	return nil
}

func newTestService(store *fakeStore) *Service {
	svc := NewService(store)
	svc.syncLastUsed = true
	return svc
}

func TestCreateAndValidateRoundTrip(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	ctx := context.Background()

	raw, tok, err := svc.CreateAPIToken(ctx, "user-1", "u1@example.com", "ci-runner", 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if !strings.HasPrefix(raw, auth.PATPrefix) {
		t.Errorf("raw token %q missing %s prefix", raw, auth.PATPrefix)
	}
	if tok.TokenHash != auth.HashPAT(raw) {
		t.Errorf("stored hash does not match HashPAT(raw)")
	}
	if tok.Kind != db.DaemonPATKindAPI {
		t.Errorf("Kind = %q, want %q", tok.Kind, db.DaemonPATKindAPI)
	}
	if tok.ExpiresAt != nil {
		t.Errorf("ttl=0 should mean no expiry, got %v", tok.ExpiresAt)
	}

	claims, err := svc.ValidateAPIToken(ctx, raw)
	if err != nil {
		t.Fatalf("ValidateAPIToken: %v", err)
	}
	if claims.Sub != "user-1" {
		t.Errorf("claims.Sub = %q, want user-1", claims.Sub)
	}
	if claims.Email != "u1@example.com" {
		t.Errorf("claims.Email = %q, want u1@example.com", claims.Email)
	}
	if claims.Role != "authenticated" {
		t.Errorf("claims.Role = %q, want authenticated", claims.Role)
	}
}

// TestKindSeparation is the core unification invariant: ONE token format and
// ONE validation core, but a daemon-kind token never authenticates user APIs
// and an api-kind token never authenticates a daemon stream. Wrong-kind
// rejections are indistinguishable from unknown tokens.
func TestKindSeparation(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	ctx := context.Background()

	rawAPI, _, err := svc.CreateAPIToken(ctx, "user-1", "u1@example.com", "api-tok", 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	rawDaemon, _, err := svc.CreatePAT(ctx, "user-1", "daemon-tok", false, nil)
	if err != nil {
		t.Fatalf("CreatePAT: %v", err)
	}

	// Both kinds share the single rlnt_pat_ format.
	if !auth.IsPATFormat(rawAPI) || !auth.IsPATFormat(rawDaemon) {
		t.Fatal("both kinds must use the unified PAT format")
	}

	// Daemon token must NOT authenticate user APIs...
	if _, err := svc.ValidateAPIToken(ctx, rawDaemon); err == nil {
		t.Fatal("daemon-kind token accepted by ValidateAPIToken")
	} else if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("daemon-kind rejection must be indistinguishable from unknown token, got: %v", err)
	}
	// ...and an api token must NOT authenticate the daemon path.
	if _, err := svc.ValidateDaemonPAT(ctx, rawAPI); err == nil {
		t.Fatal("api-kind token accepted by ValidateDaemonPAT")
	} else if !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("api-kind rejection must be indistinguishable from unknown token, got: %v", err)
	}

	// Each kind still authenticates its own surface.
	if _, err := svc.ValidateAPIToken(ctx, rawAPI); err != nil {
		t.Fatalf("api-kind token rejected by its own surface: %v", err)
	}
	if p, err := svc.ValidateDaemonPAT(ctx, rawDaemon); err != nil {
		t.Fatalf("daemon-kind token rejected by its own surface: %v", err)
	} else if p.UserID != "user-1" {
		t.Errorf("ValidateDaemonPAT UserID = %q, want user-1", p.UserID)
	}
}

// TestRevokeKindScoping: the api and daemon management surfaces cannot reach
// each other's tokens even for the same owner.
func TestRevokeKindScoping(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	ctx := context.Background()

	_, apiTok, err := svc.CreateAPIToken(ctx, "user-1", "", "api-tok", 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	_, daemonTok, err := svc.CreatePAT(ctx, "user-1", "daemon-tok", false, nil)
	if err != nil {
		t.Fatalf("CreatePAT: %v", err)
	}

	if err := svc.RevokeAPIToken(ctx, "user-1", daemonTok.ID); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("api surface revoked a daemon token: %v", err)
	}
	if err := svc.RevokePAT(ctx, "user-1", apiTok.ID); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("daemon surface revoked an api token: %v", err)
	}
	if err := svc.RevokeAPIToken(ctx, "user-1", apiTok.ID); err != nil {
		t.Errorf("api surface failed to revoke its own token: %v", err)
	}
	if err := svc.RevokePAT(ctx, "user-1", daemonTok.ID); err != nil {
		t.Errorf("daemon surface failed to revoke its own token: %v", err)
	}
}

func TestCreateWithTTLSetsExpiry(t *testing.T) {
	svc := newTestService(newFakeStore())
	base := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return base }

	_, tok, err := svc.CreateAPIToken(context.Background(), "user-1", "", "short", 90*24*time.Hour)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if tok.ExpiresAt == nil {
		t.Fatal("expected expiry to be set")
	}
	if want := base.Add(90 * 24 * time.Hour); !tok.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want %v", tok.ExpiresAt, want)
	}
}

func TestCreateRejectsDuplicateActiveName(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	ctx := context.Background()

	if _, _, err := svc.CreateAPIToken(ctx, "user-1", "", "dup", 0); err != nil {
		t.Fatalf("first CreateAPIToken: %v", err)
	}
	if _, _, err := svc.CreateAPIToken(ctx, "user-1", "", "dup", 0); err == nil {
		t.Fatal("expected duplicate active name to be rejected")
	}
	// Another user may reuse the name.
	if _, _, err := svc.CreateAPIToken(ctx, "user-2", "", "dup", 0); err != nil {
		t.Fatalf("other user CreateAPIToken: %v", err)
	}
	// A daemon token with the same name does not block an api token.
	if _, _, err := svc.CreatePAT(ctx, "user-3", "dup", false, nil); err != nil {
		t.Fatalf("CreatePAT: %v", err)
	}
	if _, _, err := svc.CreateAPIToken(ctx, "user-3", "", "dup", 0); err != nil {
		t.Fatalf("api token blocked by same-named daemon token: %v", err)
	}
	// After revoking, the name is free again.
	toks, _ := store.ListDaemonPATsByUserIDAndKind(ctx, "user-1", db.DaemonPATKindAPI)
	if err := svc.RevokeAPIToken(ctx, "user-1", toks[0].ID); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, _, err := svc.CreateAPIToken(ctx, "user-1", "", "dup", 0); err != nil {
		t.Fatalf("CreateAPIToken after revoke: %v", err)
	}
}

func TestValidateRejectsUnknownToken(t *testing.T) {
	svc := newTestService(newFakeStore())
	if _, err := svc.ValidateAPIToken(context.Background(), auth.PATPrefix+strings.Repeat("x", 40)); err == nil {
		t.Fatal("expected unknown token to be rejected")
	}
}

func TestValidateRejectsBadFormat(t *testing.T) {
	svc := newTestService(newFakeStore())
	for _, tok := range []string{"", "not-a-token", "rlt_" + strings.Repeat("x", 40), "rlnt_pat_short"} {
		if _, err := svc.ValidateAPIToken(context.Background(), tok); err == nil {
			t.Errorf("expected format rejection for %q", tok)
		}
	}
}

func TestValidateRejectsRevokedToken(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	ctx := context.Background()

	raw, tok, err := svc.CreateAPIToken(ctx, "user-1", "", "to-revoke", 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if err := svc.RevokeAPIToken(ctx, "user-1", tok.ID); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}

	if _, err := svc.ValidateAPIToken(ctx, raw); err == nil {
		t.Fatal("expected revoked token to be rejected")
	} else if !strings.Contains(err.Error(), "revoked") {
		t.Errorf("error should mention revocation, got: %v", err)
	}
}

func TestValidateRejectsExpiredToken(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	ctx := context.Background()

	base := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return base }

	raw, _, err := svc.CreateAPIToken(ctx, "user-1", "", "short-lived", time.Hour)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	// Still valid one second before expiry.
	svc.now = func() time.Time { return base.Add(time.Hour - time.Second) }
	if _, err := svc.ValidateAPIToken(ctx, raw); err != nil {
		t.Fatalf("token should be valid before expiry: %v", err)
	}

	// Rejected at/after expiry.
	svc.now = func() time.Time { return base.Add(time.Hour) }
	if _, err := svc.ValidateAPIToken(ctx, raw); err == nil {
		t.Fatal("expected expired token to be rejected")
	} else if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error should mention expiry, got: %v", err)
	}
}

func TestValidateThrottlesLastUsed(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	ctx := context.Background()

	raw, _, err := svc.CreateAPIToken(ctx, "user-1", "", "throttle", 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	// First validate stamps last_used_at.
	if _, err := svc.ValidateAPIToken(ctx, raw); err != nil {
		t.Fatalf("first validate: %v", err)
	}
	if got := len(store.lastUsedCalls); got != 1 {
		t.Fatalf("last-used calls after first validate = %d, want 1", got)
	}

	// A validate right after must be throttled.
	if _, err := svc.ValidateAPIToken(ctx, raw); err != nil {
		t.Fatalf("second validate: %v", err)
	}
	if got := len(store.lastUsedCalls); got != 1 {
		t.Errorf("last-used calls after throttled validate = %d, want 1", got)
	}

	// Once the throttle window passes, it stamps again.
	svc.now = func() time.Time { return time.Now().Add(2 * lastUsedThrottle) }
	if _, err := svc.ValidateAPIToken(ctx, raw); err != nil {
		t.Fatalf("third validate: %v", err)
	}
	if got := len(store.lastUsedCalls); got != 2 {
		t.Errorf("last-used calls after throttle window = %d, want 2", got)
	}
}

func TestRevokeUnknownOrForeignToken(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	ctx := context.Background()

	_, tok, err := svc.CreateAPIToken(ctx, "user-1", "", "mine", 0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}

	if err := svc.RevokeAPIToken(ctx, "user-2", tok.ID); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("foreign revoke: got %v, want ErrTokenNotFound", err)
	}
	if err := svc.RevokeAPIToken(ctx, "user-1", "nope"); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("unknown revoke: got %v, want ErrTokenNotFound", err)
	}
	if err := svc.RevokeAPIToken(ctx, "user-1", tok.ID); err != nil {
		t.Fatalf("owner revoke: %v", err)
	}
	if err := svc.RevokeAPIToken(ctx, "user-1", tok.ID); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("double revoke: got %v, want ErrTokenNotFound", err)
	}
}

func TestListsAreKindScoped(t *testing.T) {
	svc := newTestService(newFakeStore())
	ctx := context.Background()

	if _, _, err := svc.CreateAPIToken(ctx, "user-1", "", "api-tok", 0); err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if _, _, err := svc.CreatePAT(ctx, "user-1", "daemon-tok", false, nil); err != nil {
		t.Fatalf("CreatePAT: %v", err)
	}

	apiToks, err := svc.ListAPITokens(ctx, "user-1")
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(apiToks) != 1 || apiToks[0].Name != "api-tok" {
		t.Errorf("ListAPITokens = %+v, want exactly the api token", apiToks)
	}

	daemonToks, err := svc.ListPATs(ctx, "user-1")
	if err != nil {
		t.Fatalf("ListPATs: %v", err)
	}
	if len(daemonToks) != 1 || daemonToks[0].Name != "daemon-tok" {
		t.Errorf("ListPATs = %+v, want exactly the daemon token", daemonToks)
	}
}
