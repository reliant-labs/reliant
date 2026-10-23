package auth

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// A controllable fake issuer: serves whatever key set it was last given, or a
// 503 while "down". Not parallel-safe with other tests that shrink the retry
// variables, which is why none of these call t.Parallel.
type fakeIssuer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	keys []map[string]string
	down bool
	hits atomic.Int64
}

func newFakeIssuer(t *testing.T, keys ...map[string]string) *fakeIssuer {
	t.Helper()
	f := &fakeIssuer{keys: keys}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		down, keys := f.down, f.keys
		f.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		doc := make([]any, len(keys))
		for i, k := range keys {
			doc[i] = k
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": doc})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIssuer) url() string { return f.srv.URL + "/keys" }

func (f *fakeIssuer) set(down bool, keys ...map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
	if keys != nil {
		f.keys = keys
	}
}

// deadURL is a JWKS URL nothing is listening on.
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL + "/keys"
	srv.Close()
	return u
}

func fastRetries(t *testing.T) {
	t.Helper()
	i, m, b, r := jwksRetryInitial, jwksRetryMax, jwksBootAttempt, snapshotFileRecheck
	jwksRetryInitial, jwksRetryMax, jwksBootAttempt, snapshotFileRecheck = 20*time.Millisecond, 40*time.Millisecond, 2*time.Second, 0
	t.Cleanup(func() { jwksRetryInitial, jwksRetryMax, jwksBootAttempt, snapshotFileRecheck = i, m, b, r })
}

const snapIssuer = "https://idp.example"

func snapshotJSON(t *testing.T, delivered time.Time, issuer string, keys ...map[string]string) string {
	t.Helper()
	doc := map[string]any{"keys": keys}
	if !delivered.IsZero() {
		doc["delivered_at"] = delivered.UTC().Format(time.RFC3339Nano)
	}
	if issuer != "" {
		doc["issuer"] = issuer
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeSnapshot(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "jwks.json")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func snapToken(t *testing.T, key *rsa.PrivateKey, kid string, mod func(jwt.MapClaims)) string {
	t.Helper()
	c := jwt.MapClaims{"sub": "u1", "iss": snapIssuer, "aud": "api", "exp": time.Now().Add(time.Hour).Unix()}
	if mod != nil {
		mod(c)
	}
	return signRS256WithKID(t, key, kid, c)
}

func newSnapValidator(t *testing.T, jwt JWTConfig) (*Validator, error) {
	t.Helper()
	jwt.SigningMethod, jwt.Issuer, jwt.Audience = "RS256", snapIssuer, "api"
	v, err := NewValidator(Config{Provider: ProviderJWT, JWT: jwt})
	if err == nil {
		t.Cleanup(func() { _ = v.Close() })
	}
	return v, err
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ── the matrix: issuer up/down × snapshot fresh/stale/absent ───────────

func TestJWKSSnapshot_StartupMatrix(t *testing.T) {
	fastRetries(t)
	key := rsaKeyFor(t)
	const kid = "k1"
	now := time.Now()

	fresh := snapshotJSON(t, now.Add(-time.Hour), snapIssuer, rsaJWK(t, key, kid))
	stale := snapshotJSON(t, now.Add(-8*24*time.Hour), snapIssuer, rsaJWK(t, key, kid))

	tests := []struct {
		name     string
		issuerUp bool
		snapshot JWKSSnapshot
		wantErr  string // substring; "" means must start
	}{
		{"up + fresh snapshot: starts (live)", true, JWKSSnapshot{Inline: fresh}, ""},
		{"up + stale snapshot: starts (live), snapshot irrelevant", true, JWKSSnapshot{Inline: stale}, ""},
		{"up + no snapshot: starts (today's behaviour)", true, JWKSSnapshot{}, ""},
		{"up + unreadable snapshot file: starts (live)", true, JWKSSnapshot{File: "/nonexistent/jwks.json"}, ""},

		{"down + fresh inline snapshot: starts on the snapshot", false, JWKSSnapshot{Inline: fresh}, ""},
		{"down + fresh snapshot file: starts on the snapshot", false, JWKSSnapshot{File: writeSnapshot(t, fresh)}, ""},
		{"down + stale snapshot: refuses to start", false, JWKSSnapshot{Inline: stale}, "stale"},
		{"down + absent snapshot file: refuses to start", false, JWKSSnapshot{File: "/nonexistent/jwks.json"}, "no usable snapshot"},
		{"down + no snapshot configured: refuses to start (today's behaviour)", false, JWKSSnapshot{}, "fetch JWKS"},
		{"down + stale but MaxAge raised: starts", false, JWKSSnapshot{Inline: stale, MaxAge: 30 * 24 * time.Hour}, ""},
		{"down + garbage snapshot: refuses to start", false, JWKSSnapshot{Inline: "not json"}, "not valid JSON"},
		{"down + snapshot with no keys: refuses to start", false, JWKSSnapshot{Inline: snapshotJSON(t, now, snapIssuer)}, "no keys"},
		{"down + snapshot for another issuer: refuses to start", false,
			JWKSSnapshot{Inline: snapshotJSON(t, now, "https://other.example", rsaJWK(t, key, kid))}, "published for issuer"},
		{"down + inline snapshot without delivered_at: refuses to start", false,
			JWKSSnapshot{Inline: snapshotJSON(t, time.Time{}, snapIssuer, rsaJWK(t, key, kid))}, "no delivered_at"},
		{"down + snapshot dated in the future: refuses to start", false,
			JWKSSnapshot{Inline: snapshotJSON(t, now.Add(48*time.Hour), snapIssuer, rsaJWK(t, key, kid))}, "in the future"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issuer := newFakeIssuer(t, rsaJWK(t, key, kid))
			url := issuer.url()
			if !tc.issuerUp {
				url = deadURL(t)
			}
			v, err := newSnapValidator(t, JWTConfig{JWKSURL: url, JWKSSnapshot: tc.snapshot})
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected a boot failure containing %q, got none", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("boot error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected to start, got: %v", err)
			}
			if _, err := v.Validate(snapToken(t, key, kid, nil)); err != nil {
				t.Fatalf("a token signed by the published key was rejected: %v", err)
			}
		})
	}
}

// A snapshot is key material, not a bypass: everything else is still checked.
func TestJWKSSnapshot_TokensAreStillFullyVerified(t *testing.T) {
	fastRetries(t)
	key, other := rsaKeyFor(t), rsaKeyFor(t)
	snap := snapshotJSON(t, time.Now(), snapIssuer, rsaJWK(t, key, "k1"))
	v, err := newSnapValidator(t, JWTConfig{JWKSURL: deadURL(t), JWKSSnapshot: JWKSSnapshot{Inline: snap}})
	if err != nil {
		t.Fatal(err)
	}
	for name, tok := range map[string]string{
		"expired":                       snapToken(t, key, "k1", func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() }),
		"wrong audience":                snapToken(t, key, "k1", func(c jwt.MapClaims) { c["aud"] = "someone-else" }),
		"wrong issuer":                  snapToken(t, key, "k1", func(c jwt.MapClaims) { c["iss"] = "https://evil.example" }),
		"unknown kid":                   snapToken(t, key, "k9", nil),
		"forged (right kid, wrong key)": snapToken(t, other, "k1", nil),
	} {
		if _, err := v.Validate(tok); err == nil {
			t.Errorf("%s token was accepted on the snapshot", name)
		}
	}
	if _, err := v.Validate(snapToken(t, key, "k1", nil)); err != nil {
		t.Errorf("the good token was rejected: %v", err)
	}
}

// Once the issuer is reachable, live wins: a key the snapshot holds under a kid
// the live set holds differently stops being trusted.
func TestJWKSSnapshot_LiveWinsAfterRecovery(t *testing.T) {
	fastRetries(t)
	snapKey, liveKey := rsaKeyFor(t), rsaKeyFor(t)
	const kid = "shared-kid"

	issuer := newFakeIssuer(t, rsaJWK(t, liveKey, kid))
	issuer.set(true) // down at boot
	snap := snapshotJSON(t, time.Now(), snapIssuer, rsaJWK(t, snapKey, kid))
	v, err := newSnapValidator(t, JWTConfig{JWKSURL: issuer.url(), JWKSSnapshot: JWKSSnapshot{Inline: snap}})
	if err != nil {
		t.Fatalf("should start on the snapshot: %v", err)
	}
	if _, err := v.Validate(snapToken(t, snapKey, kid, nil)); err != nil {
		t.Fatalf("snapshot key rejected while the issuer is down: %v", err)
	}
	if _, err := v.Validate(snapToken(t, liveKey, kid, nil)); err == nil {
		t.Fatal("a key the snapshot does not hold was accepted while the issuer is down")
	}

	issuer.set(false) // issuer comes back
	eventually(t, "the validator to switch to live keys", func() bool {
		_, err := v.Validate(snapToken(t, liveKey, kid, nil))
		return err == nil
	})
	if _, err := v.Validate(snapToken(t, snapKey, kid, nil)); err == nil {
		t.Fatal("the snapshot's key was still accepted for a kid the live set holds differently")
	}
}

// A key the issuer dropped is not kept alive by an older snapshot.
func TestJWKSSnapshot_RevokedKeyNotResurrectedBySnapshot(t *testing.T) {
	fastRetries(t)
	oldKey, newKey := rsaKeyFor(t), rsaKeyFor(t)

	issuer := newFakeIssuer(t, rsaJWK(t, newKey, "new")) // live no longer has "old"
	snap := snapshotJSON(t, time.Now().Add(-time.Hour), snapIssuer, rsaJWK(t, oldKey, "old"), rsaJWK(t, newKey, "new"))
	v, err := newSnapValidator(t, JWTConfig{JWKSURL: issuer.url(), JWKSSnapshot: JWKSSnapshot{Inline: snap}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate(snapToken(t, newKey, "new", nil)); err != nil {
		t.Fatalf("live key rejected: %v", err)
	}
	if _, err := v.Validate(snapToken(t, oldKey, "old", nil)); err == nil {
		t.Fatal("a key absent from the live set was accepted from an OLDER snapshot")
	}
}

// Rotation overlap: the publisher saw a rotation this process has not refreshed
// for, so a snapshot delivered after the last live fetch may add the new key.
func TestJWKSSnapshot_RotationOverlapUnion(t *testing.T) {
	fastRetries(t)
	oldKey, newKey := rsaKeyFor(t), rsaKeyFor(t)

	issuer := newFakeIssuer(t, rsaJWK(t, oldKey, "old"))
	path := writeSnapshot(t, snapshotJSON(t, time.Now().Add(-time.Hour), snapIssuer, rsaJWK(t, oldKey, "old")))
	v, err := newSnapValidator(t, JWTConfig{JWKSURL: issuer.url(), JWKSSnapshot: JWKSSnapshot{File: path}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate(snapToken(t, oldKey, "old", nil)); err != nil {
		t.Fatalf("live key rejected: %v", err)
	}
	// "new" is unknown to both the live set (cached) and the snapshot.
	if _, err := v.Validate(snapToken(t, newKey, "new", nil)); err == nil {
		t.Fatal("a key neither source holds was accepted")
	}

	// The control plane re-delivers a snapshot that includes the rotated key,
	// after this process's last live fetch.
	// Dated a second ahead so it is strictly after the live fetch even on a
	// clock too coarse to tell the two instants apart (Windows).
	if err := os.WriteFile(path, []byte(snapshotJSON(t, time.Now().Add(time.Second), snapIssuer,
		rsaJWK(t, oldKey, "old"), rsaJWK(t, newKey, "new"))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate(snapToken(t, newKey, "new", nil)); err != nil {
		t.Fatalf("rotation overlap: a key only the newer snapshot holds should be accepted: %v", err)
	}
	// For a kid live holds, live still wins.
	if _, err := v.Validate(snapToken(t, newKey, "old", nil)); err == nil {
		t.Fatal("snapshot overrode a kid the live set holds")
	}
}

func TestJWKSSnapshot_SnapshotAgesOutWhileIssuerStaysDown(t *testing.T) {
	fastRetries(t)
	key := rsaKeyFor(t)
	snap := snapshotJSON(t, time.Now(), snapIssuer, rsaJWK(t, key, "k1"))
	v, err := newSnapValidator(t, JWTConfig{
		JWKSURL: deadURL(t), JWKSSnapshot: JWKSSnapshot{Inline: snap, MaxAge: 400 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate(snapToken(t, key, "k1", nil)); err != nil {
		t.Fatalf("fresh snapshot rejected: %v", err)
	}
	time.Sleep(600 * time.Millisecond)
	if _, err := v.Validate(snapToken(t, key, "k1", nil)); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("an aged-out snapshot must fail closed with a clear reason, got: %v", err)
	}
}

func TestJWKSSnapshot_FileIsReReadAndDatedByMtime(t *testing.T) {
	fastRetries(t)
	k1, k2 := rsaKeyFor(t), rsaKeyFor(t)

	// No delivered_at: the file's mtime dates it.
	path := writeSnapshot(t, snapshotJSON(t, time.Time{}, snapIssuer, rsaJWK(t, k1, "k1")))
	v, err := newSnapValidator(t, JWTConfig{JWKSURL: deadURL(t), JWKSSnapshot: JWKSSnapshot{File: path}})
	if err != nil {
		t.Fatalf("a file with no delivered_at should be dated by its mtime: %v", err)
	}
	if _, err := v.Validate(snapToken(t, k1, "k1", nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate(snapToken(t, k2, "k2", nil)); err == nil {
		t.Fatal("k2 should be unknown before re-delivery")
	}

	// Re-delivery with a rotated key set: picked up without a restart.
	if err := os.WriteFile(path, []byte(snapshotJSON(t, time.Now(), snapIssuer, rsaJWK(t, k2, "k2"))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate(snapToken(t, k2, "k2", nil)); err != nil {
		t.Fatalf("re-delivered snapshot not picked up: %v", err)
	}
	if _, err := v.Validate(snapToken(t, k1, "k1", nil)); err == nil {
		t.Fatal("a key removed by the re-delivered snapshot is still accepted")
	}

	// An old mtime with no delivered_at is stale.
	old := writeSnapshot(t, snapshotJSON(t, time.Time{}, snapIssuer, rsaJWK(t, k1, "k1")))
	if err := os.Chtimes(old, time.Now().Add(-10*24*time.Hour), time.Now().Add(-10*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := newSnapValidator(t, JWTConfig{JWKSURL: deadURL(t), JWKSSnapshot: JWKSSnapshot{File: old}}); err == nil {
		t.Fatal("a 10-day-old file must be stale under the 7-day default")
	}
}

func TestJWKSSnapshot_NewerOfFileAndInlineWins(t *testing.T) {
	fastRetries(t)
	k1, k2 := rsaKeyFor(t), rsaKeyFor(t)
	file := writeSnapshot(t, snapshotJSON(t, time.Now().Add(-2*time.Hour), snapIssuer, rsaJWK(t, k1, "k1")))
	inline := snapshotJSON(t, time.Now().Add(-time.Hour), snapIssuer, rsaJWK(t, k2, "k2"))
	v, err := newSnapValidator(t, JWTConfig{JWKSURL: deadURL(t), JWKSSnapshot: JWKSSnapshot{File: file, Inline: inline}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate(snapToken(t, k2, "k2", nil)); err != nil {
		t.Fatalf("the newer (inline) snapshot should win: %v", err)
	}
	if _, err := v.Validate(snapToken(t, k1, "k1", nil)); err == nil {
		t.Fatal("snapshots must not be merged: the older one's key was accepted")
	}
}

// Discovery counts as part of the live attempt: an issuer that is down for
// discovery too still lets a snapshot-backed server start.
func TestJWKSSnapshot_DiscoveryDownThenUp(t *testing.T) {
	fastRetries(t)
	key := rsaKeyFor(t)
	issuer := newFakeIssuer(t, rsaJWK(t, key, "k1"))
	var discoverable atomic.Bool
	discover := func(context.Context) (string, error) {
		if !discoverable.Load() {
			return "", errors.New("discovery: connection refused")
		}
		return issuer.url(), nil
	}
	snap := snapshotJSON(t, time.Now(), snapIssuer, rsaJWK(t, key, "k1"))
	v, err := newSnapValidator(t, JWTConfig{JWKSDiscover: discover, JWKSSnapshot: JWKSSnapshot{Inline: snap}})
	if err != nil {
		t.Fatalf("should start on the snapshot while discovery fails: %v", err)
	}
	if _, err := v.Validate(snapToken(t, key, "k1", nil)); err != nil {
		t.Fatal(err)
	}
	discoverable.Store(true)
	eventually(t, "a live fetch after discovery recovers", func() bool { return issuer.hits.Load() > 0 })

	// Without a snapshot, a failing discovery still refuses to start.
	if _, err := newSnapValidator(t, JWTConfig{JWKSDiscover: func(context.Context) (string, error) {
		return "", errors.New("discovery: connection refused")
	}}); err == nil {
		t.Fatal("no snapshot + failing discovery must refuse to start")
	}
}

func TestJWKSSnapshot_CloseStopsRetrying(t *testing.T) {
	fastRetries(t)
	key := rsaKeyFor(t)
	issuer := newFakeIssuer(t, rsaJWK(t, key, "k1"))
	issuer.set(true)
	snap := snapshotJSON(t, time.Now(), snapIssuer, rsaJWK(t, key, "k1"))
	v, err := newSnapValidator(t, JWTConfig{JWKSURL: issuer.url(), JWKSSnapshot: JWKSSnapshot{Inline: snap}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "background retries to hit the issuer", func() bool { return issuer.hits.Load() > 2 })
	_ = v.Close()
	time.Sleep(100 * time.Millisecond)
	before := issuer.hits.Load()
	time.Sleep(200 * time.Millisecond)
	if after := issuer.hits.Load(); after != before {
		t.Fatalf("retries continued after Close: %d -> %d", before, after)
	}
}

func TestJWKSSnapshot_WithoutLiveSourceIsAConfigError(t *testing.T) {
	_, err := NewValidator(Config{Provider: ProviderJWT, JWT: JWTConfig{
		SigningMethod: "RS256", JWKSSnapshot: JWKSSnapshot{Inline: "{}"},
	}})
	if err == nil || !strings.Contains(err.Error(), "JWKSSnapshot") {
		t.Fatalf("a snapshot with no live source must be rejected, got: %v", err)
	}
}

func TestJWKSKeyfuncWithSnapshot(t *testing.T) {
	fastRetries(t)
	key := rsaKeyFor(t)
	snap := snapshotJSON(t, time.Now(), "", rsaJWK(t, key, "k1"))
	kf, stop, err := JWKSKeyfuncWithSnapshot(deadURL(t), JWKSSnapshot{Inline: snap})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	jv, err := NewJWKSValidator(JWKSValidatorConfig{KeyFunc: kf, Issuer: snapIssuer, Audience: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jv.ValidateToken(snapToken(t, key, "k1", nil)); err != nil {
		t.Fatalf("composed validator rejected a snapshot-signed token: %v", err)
	}
}
