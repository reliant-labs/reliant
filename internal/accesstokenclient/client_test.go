package accesstokenclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
)

var liveToken = fat.Prefix + strings.Repeat("A1b2", 8)

// fakeCP stands in for control-plane's AccessTokenInternalService, emitting
// the protojson shapes control-plane's wire test pins
// (control-plane internal/handlers/access_token_internal/wire_json_test.go).
func fakeCP(t *testing.T, handle func(method string, body map[string]any) (int, string)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer signed-internal" {
			t.Errorf("missing internal-service bearer: %q", r.Header.Get("Authorization"))
		}
		if !strings.HasPrefix(r.URL.Path, ServicePath) {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		status, out := handle(strings.TrimPrefix(r.URL.Path, ServicePath), body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(out))
	}))
}

func sign() (string, error) { return "signed-internal", nil }

func TestIntrospect_DecodesAnActivePrincipal(t *testing.T) {
	srv := fakeCP(t, func(method string, body map[string]any) (int, string) {
		if method != "Introspect" || body["token"] != liveToken {
			t.Fatalf("got %s %v", method, body)
		}
		return 200, `{"active":true,"tokenId":"tok-1","orgId":"org-1","actingUserId":"user-1",
			"scopes":["daemon:connect"],"resource":{"kind":"daemon","id":"d-1"},"ephemeral":true,
			"expiresAt":"2030-01-01T00:00:00Z"}`
	})
	defer srv.Close()

	p, err := New(Deps{BaseURL: srv.URL, Sign: sign}).Introspect(context.Background(), liveToken)
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}
	if p.TokenID != "tok-1" || p.OrgID != "org-1" || p.ActingUserID != "user-1" || !p.Ephemeral ||
		!p.Scopes.Has(fat.ScopeDaemonConnect) || !p.BoundTo(fat.ResourceDaemon, "d-1") || p.ExpiresAt == nil {
		t.Fatalf("principal lost a field: %+v", p)
	}
}

func TestIntrospect_InactiveIsErrInactive(t *testing.T) {
	srv := fakeCP(t, func(string, map[string]any) (int, string) { return 200, `{}` })
	defer srv.Close()
	if _, err := New(Deps{BaseURL: srv.URL, Sign: sign}).Introspect(context.Background(), liveToken); !errors.Is(err, ErrInactive) {
		t.Fatalf("err = %v, want ErrInactive", err)
	}
}

// TestIntrospect_RefusesEveryOtherFamilyWithoutARoundTrip is reliant's
// cross-family recognizer test: every retired family — including the ones
// reliant itself used to mint — is refused on shape, locally.
func TestIntrospect_RefusesEveryOtherFamilyWithoutARoundTrip(t *testing.T) {
	srv := fakeCP(t, func(string, map[string]any) (int, string) {
		t.Fatal("a non-rlat_ token reached control-plane")
		return 500, ""
	})
	defer srv.Close()
	c := New(Deps{BaseURL: srv.URL, Sign: sign})
	for name, tok := range map[string]string{
		"daemon/api PAT":       "rlnt_pat_" + strings.Repeat("A1b2C3", 5),
		"connector credential": "rlnt_conn_" + strings.Repeat("Z9y8X7", 5),
		"rlnt_ LLM key":        "rlnt_" + strings.Repeat("aB-_", 10) + "xyz",
		"rly_ key":             "rly_" + strings.Repeat("0a1b2c3d", 8),
		"dpat_ share link":     "dpat_123e4567-e89b-12d3-a456-426614174000",
		"Supabase JWT":         "eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJ1In0.c2ln",
	} {
		if _, err := c.Introspect(context.Background(), tok); !errors.Is(err, ErrInactive) {
			t.Errorf("%s: err = %v, want ErrInactive", name, err)
		}
	}
}

// roundTripFunc lets a test observe the transport the adapter was given.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestNew_UsesTheInjectedHTTPClient pins the Deps.HTTPClient seam: a caller
// that hands the adapter an instrumented or proxied client must have every
// call go through it, not through a private default.
func TestNew_UsesTheInjectedHTTPClient(t *testing.T) {
	srv := fakeCP(t, func(string, map[string]any) (int, string) {
		return 200, `{"active":false}`
	})
	defer srv.Close()
	var calls int
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return http.DefaultTransport.RoundTrip(r)
	})
	c := New(Deps{BaseURL: srv.URL, Sign: sign, HTTPClient: &http.Client{Transport: transport}})
	if _, err := c.Introspect(context.Background(), liveToken); !errors.Is(err, ErrInactive) {
		t.Fatalf("Introspect: %v", err)
	}
	if calls != 1 {
		t.Fatalf("injected HTTPClient saw %d calls, want 1", calls)
	}
}

func TestIntrospect_TransportErrorIsNotInactive(t *testing.T) {
	srv := fakeCP(t, func(string, map[string]any) (int, string) {
		return 500, `{"code":"internal","message":"db down"}`
	})
	defer srv.Close()
	_, err := New(Deps{BaseURL: srv.URL, Sign: sign}).Introspect(context.Background(), liveToken)
	if err == nil || errors.Is(err, ErrInactive) {
		t.Fatalf("a control-plane outage read as an inactive token (err=%v)", err)
	}
}

func TestMintForUser_SendsTheGrantAndDecodes(t *testing.T) {
	srv := fakeCP(t, func(method string, body map[string]any) (int, string) {
		if method != "MintForUser" || body["userId"] != "u-1" || body["name"] != "laptop" || body["rotate"] != true {
			t.Fatalf("got %s %v", method, body)
		}
		res, _ := body["resource"].(map[string]any)
		if res["kind"] != "daemon" || res["id"] != "d-1" {
			t.Fatalf("resource %v", body["resource"])
		}
		return 200, `{"tokenId":"tok-9","secret":"` + liveToken + `","displayPrefix":"rlat_A1b2A1b2","rotated":true}`
	})
	defer srv.Close()
	m, err := New(Deps{BaseURL: srv.URL, Sign: sign}).MintForUser(context.Background(), MintRequest{
		UserID: "u-1", Name: "laptop", Scopes: []fat.Scope{fat.ScopeDaemonConnect},
		Resource: &fat.Resource{Kind: fat.ResourceDaemon, ID: "d-1"}, Rotate: true,
	})
	if err != nil {
		t.Fatalf("MintForUser: %v", err)
	}
	if m.TokenID != "tok-9" || m.Plaintext != liveToken || !m.Rotated {
		t.Fatalf("minted %+v", m)
	}
}

func TestRevokeResource_DecodesInt64AsString(t *testing.T) {
	srv := fakeCP(t, func(string, map[string]any) (int, string) { return 200, `{"revokedCount":"3"}` })
	defer srv.Close()
	n, err := New(Deps{BaseURL: srv.URL, Sign: sign}).RevokeResource(context.Background(), fat.Resource{Kind: fat.ResourceDaemon, ID: "d"})
	if err != nil || n != 3 {
		t.Fatalf("RevokeResource = %d, %v; want 3", n, err)
	}
}

// ── the API interceptor's cache ──────────────────────────────────────────

// TestCacheTTL_IsBounded pins the documented revocation window. Raising it
// lengthens how long a revoked token keeps working at reliant's API; that is
// a security decision, so it must be made here, deliberately, not drift.
func TestCacheTTL_IsBounded(t *testing.T) {
	if CacheTTL <= 0 || CacheTTL > 5*time.Second {
		t.Fatalf("CacheTTL = %v; the documented revocation window is at most 5s", CacheTTL)
	}
}

type countingIntrospector struct {
	calls int
	p     *fat.Principal
	err   error
}

func (c *countingIntrospector) Introspect(context.Context, string) (*fat.Principal, error) {
	c.calls++
	return c.p, c.err
}

func TestCachedIntrospector_ServesWithinTTLAndRefreshesAfter(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	inner := &countingIntrospector{p: &fat.Principal{TokenID: "t"}}
	c := NewCachedIntrospector(inner)
	c.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if _, err := c.Introspect(context.Background(), liveToken); err != nil {
			t.Fatal(err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("inner called %d times within TTL, want 1", inner.calls)
	}
	// The token is revoked upstream; once the TTL lapses the revocation lands.
	inner.p, inner.err = nil, ErrInactive
	now = now.Add(CacheTTL)
	if _, err := c.Introspect(context.Background(), liveToken); !errors.Is(err, ErrInactive) {
		t.Fatalf("revocation did not land after CacheTTL (err=%v)", err)
	}
}

func TestCachedIntrospector_DoesNotCacheOutages(t *testing.T) {
	inner := &countingIntrospector{err: errors.New("control-plane unreachable")}
	c := NewCachedIntrospector(inner)
	_, _ = c.Introspect(context.Background(), liveToken)
	inner.err, inner.p = nil, &fat.Principal{TokenID: "t"}
	if _, err := c.Introspect(context.Background(), liveToken); err != nil {
		t.Fatalf("an outage was cached and pinned a live token as failed: %v", err)
	}
}

func TestListForUser_DecodesMetadata(t *testing.T) {
	srv := fakeCP(t, func(method string, body map[string]any) (int, string) {
		if method != "ListForUser" || body["userId"] != "u-1" || body["scope"] != "daemon:connect" {
			t.Fatalf("got %s %v", method, body)
		}
		return 200, `{"tokens":[{"id":"t1","name":"laptop","displayPrefix":"rlat_abcd1234","scopes":["daemon:connect"],
			"resource":{"kind":"daemon","id":"d-1"},"createdAt":"2026-01-01T00:00:00Z","lastUsedAt":"2026-01-02T00:00:00Z"}]}`
	})
	defer srv.Close()
	infos, err := New(Deps{BaseURL: srv.URL, Sign: sign}).ListForUser(context.Background(), "u-1", fat.ScopeDaemonConnect)
	if err != nil || len(infos) != 1 {
		t.Fatalf("ListForUser = %v, %v", infos, err)
	}
	if infos[0].ID != "t1" || infos[0].LastUsedAt == nil || infos[0].Resource == nil || infos[0].CreatedAt.IsZero() {
		t.Fatalf("lost a field: %+v", infos[0])
	}
}

func TestRevokeForUser_NotFoundIsRecognisable(t *testing.T) {
	srv := fakeCP(t, func(string, map[string]any) (int, string) {
		return 404, `{"code":"not_found","message":"token not found"}`
	})
	defer srv.Close()
	err := New(Deps{BaseURL: srv.URL, Sign: sign}).RevokeForUser(context.Background(), "u", "t")
	if !IsNotFound(err) {
		t.Fatalf("err = %v, want a recognisable not_found", err)
	}
}
