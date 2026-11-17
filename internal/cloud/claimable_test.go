package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"
)

func TestSolvePoW_MeetsTheServersRule(t *testing.T) {
	t.Parallel()
	ch := ClaimableChallenge{Challenge: "signed.opaque.challenge", DifficultyBits: 14}
	nonce, err := SolvePoW(context.Background(), ch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strconv.ParseInt(nonce, 10, 64); err != nil {
		t.Fatalf("nonce %q is not a decimal string", nonce)
	}
	sum := sha256.Sum256([]byte(ch.Challenge + ":" + nonce))
	if got := leadingZeroBits(sum[:]); got < 14 {
		t.Fatalf("SHA-256(challenge:nonce) has %d leading zero bits, want >= 14", got)
	}
}

func TestSolvePoW_RefusesAnAbsurdDifficulty(t *testing.T) {
	t.Parallel()
	_, err := SolvePoW(context.Background(), ClaimableChallenge{Challenge: "x", DifficultyBits: 60})
	if err == nil || !strings.Contains(err.Error(), "forge login") {
		t.Fatalf("a 60-bit puzzle must be refused with the sign-up remedy; got %v", err)
	}
}

func TestSolvePoW_StopsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SolvePoW(ctx, ClaimableChallenge{Challenge: "x", DifficultyBits: 27}); err == nil {
		t.Fatal("a cancelled solve must return an error")
	}
}

func TestLeadingZeroBits(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   []byte
		want int
	}{{[]byte{0xff}, 0}, {[]byte{0x0f}, 4}, {[]byte{0, 0x80}, 8}, {[]byte{0, 0, 0x01}, 23}, {[]byte{0, 0}, 16}} {
		if got := leadingZeroBits(c.in); got != c.want {
			t.Errorf("leadingZeroBits(%x) = %d, want %d", c.in, got, c.want)
		}
	}
}

// A claimable token is stored under its own client id, so ResolveCredential —
// which looks up only ClientID — can never return it.
func TestClaimableCredentialNeverResolvesAsALogin(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "")
	ep := endpointFor(t, "prod", "https://cp.example.com", "")
	now := time.Now()
	if _, err := StoreClaimable(ep, "acme", "prod", ClaimableDeploy{
		AccessToken: "rlat_anonymous0123456789", OrgID: "org-1", ClaimURL: "https://app.reliant.dev/claim#rcl_x",
		ExpiresAt: now.Add(72 * time.Hour), PurgeAfter: now.Add(10 * 24 * time.Hour),
	}, now); err != nil {
		t.Fatal(err)
	}
	if ClaimableClientIDFor("acme", "prod") == ClientID {
		t.Fatal("the claimable client id must differ from the login client id")
	}
	if _, err := ResolveCredential("", ep); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("ResolveCredential returned a stored claimable token as a login: %v", err)
	}
	got, err := LoadClaimable(ep, "acme", "prod", now)
	if err != nil || got.Token != "rlat_anonymous0123456789" || got.Claimable == nil || got.Claimable.ClaimURL == "" {
		t.Fatalf("LoadClaimable = %+v, %v", got, err)
	}
	// Another env of the same project is another deploy.
	if _, err := LoadClaimable(ep, "acme", "staging", now); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("a different env must not see this deploy's entry: %v", err)
	}
	// An expired entry is a deleted deploy.
	if _, err := LoadClaimable(ep, "acme", "prod", now.Add(11*24*time.Hour)); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("an entry past purge_after must read as absent: %v", err)
	}
	// forge logout / a login entry are untouched by forgetting it.
	if err := ForgetClaimable(ep, "acme", "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadClaimable(ep, "acme", "prod", now); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("ForgetClaimable left the entry: %v", err)
	}
}

func TestUseClaimableCredential_IsScopedToTheEndpointAndRestores(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "")
	ep := endpointFor(t, "prod", "https://cp.example.com", "")
	other := endpointFor(t, "prod", "https://other.example.com", "")
	restore := UseClaimableCredential(ep, "rlat_anon", nil)
	got, err := ResolveCredential("", ep)
	if err != nil || got.Token != "rlat_anon" || got.Source != SourceClaimable {
		t.Fatalf("override not applied: %+v, %v", got, err)
	}
	if _, err := ResolveCredential("", other); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("the override leaked to another endpoint: %v", err)
	}
	restore()
	if _, err := ResolveCredential("", ep); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("the override outlived restore: %v", err)
	}
}

func TestNoCredentialMessageMentionsTheClaimableFlag(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "")
	_, err := ResolveCredential("", endpointFor(t, "prod", "https://cp.example.com", ""))
	if err == nil || !strings.Contains(err.Error(), "forge env deploy <env> --claimable") ||
		!strings.Contains(err.Error(), "72h") {
		t.Fatalf("the no-credential error must name --claimable and the 72h window:\n%v", err)
	}
}

func TestClaimableDeviceID_IsStable(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	a, err := ClaimableDeviceID()
	if err != nil || len(a) < 32 {
		t.Fatalf("device id = %q, %v", a, err)
	}
	b, _ := ClaimableDeviceID()
	if a != b {
		t.Fatalf("device id changed between calls: %q vs %q", a, b)
	}
}

func TestClaimableClient_SendsNoCredentialAndDecodesTheDeploy(t *testing.T) {
	t.Parallel()
	var sawAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = append(sawAuth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/" + ProcGetClaimableChallenge:
			_, _ = w.Write([]byte(`{"challenge":"c1","difficultyBits":10,"expiresAt":"2026-10-26T15:00:00Z"}`))
		case "/" + ProcStartClaimableDeploy:
			var req ClaimableStartRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Project != "acme" || req.PoWNonce != "42" {
				http.Error(w, `{"code":"invalid_argument","message":"bad request"}`, http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"accessToken":"rlat_x","orgId":"org-1","claimUrl":"https://app.reliant.dev/claim#rcl_x",` +
				`"expiresAt":"2026-10-29T14:02:00Z","purgeAfter":"2026-11-05T14:02:00Z","reused":true}`))
		}
	}))
	defer srv.Close()
	c := NewClaimableClient(endpointFor(t, "prod", srv.URL, ""))
	ch, err := c.Challenge(context.Background())
	if err != nil || ch.Challenge != "c1" || ch.DifficultyBits != 10 {
		t.Fatalf("challenge = %+v, %v", ch, err)
	}
	d, err := c.Start(context.Background(), ClaimableStartRequest{Project: "acme", Env: "prod", DeviceID: "dev", PoWChallenge: "c1", PoWNonce: "42"})
	if err != nil || d.AccessToken != "rlat_x" || !d.Reused || d.ClaimURL == "" || d.ExpiresAt.IsZero() {
		t.Fatalf("start = %+v, %v", d, err)
	}
	for _, a := range sawAuth {
		if a != "" {
			t.Fatalf("an unauthenticated rpc sent Authorization %q", a)
		}
	}
}

func TestClaimableThrottleOf_ReadsDetailThenMetadata(t *testing.T) {
	t.Parallel()
	fromDetail := &Error{Code: CodeResourceExhausted, Reason: ReasonClaimableThrottled, Details: []ErrorDetail{{
		Type: "controlplane.v1.ClaimableThrottle", Debug: json.RawMessage(`{"retryAfterSeconds":"840","scope":"platform"}`),
	}}}
	th, ok := ClaimableThrottleOf(fromDetail)
	if !ok || th.RetryAfter != 14*time.Minute || th.Scope != "platform" {
		t.Fatalf("detail: %+v ok=%v", th, ok)
	}
	h := http.Header{}
	h.Set("x-forge-error-retry-after-seconds", "90")
	h.Set("x-forge-error-throttle-scope", "device")
	th, ok = ClaimableThrottleOf(&Error{Code: CodeResourceExhausted, Reason: ReasonClaimableThrottled, Header: h})
	if !ok || th.RetryAfter != 90*time.Second || th.Scope != "device" {
		t.Fatalf("metadata: %+v ok=%v", th, ok)
	}
	if _, ok := ClaimableThrottleOf(&Error{Code: CodeNotFound}); ok {
		t.Fatal("a not_found is not a throttle")
	}
	if _, ok := ClaimableThrottleOf(errors.New("plain")); ok {
		t.Fatal("a plain error is not a throttle")
	}
}
