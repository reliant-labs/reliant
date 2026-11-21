package middleware

import (
	"context"
	"encoding/json"
	"math"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/auth"
)

// testPrincipals are the callers the principal interceptors below stash,
// named by the X-Test-Principal header. "billing-read" and "billing-manage"
// are one user in one org with identical fixed fields and credential: only
// the permissions their tokens carry in Raw differ.
var testPrincipals = map[string]*auth.Claims{
	"billing-read":   jwtPrincipal(map[string]any{"org_permissions": []any{"org:billing:read"}, "iat": float64(1700000000), "exp": float64(1700003600), "jti": "t-1"}),
	"billing-manage": jwtPrincipal(map[string]any{"org_permissions": []any{"org:billing:read", "org:billing:manage"}, "iat": float64(1700000000), "exp": float64(1700003600), "jti": "t-1"}),
	// The same authority as billing-read, re-issued: only the volatile
	// registered claims differ, in the types decoders hand them over as.
	"billing-read-refreshed": jwtPrincipal(map[string]any{"org_permissions": []any{"org:billing:read"}, "iat": json.Number("1700009000"), "exp": int64(1700012600), "nbf": float64(1700009000), "jti": "t-2"}),
	"unencodable":            jwtPrincipal(map[string]any{"org_permissions": []any{"org:billing:read"}, "score": math.NaN()}),
	"bob":                    {UserID: "bob", OrgID: "acme", Credential: auth.TokenCredential()},
}

func jwtPrincipal(raw map[string]any) *auth.Claims {
	return &auth.Claims{
		UserID: "alice", OrgID: "acme", Email: "alice@acme.test", EmailVerified: true,
		Role: "member", Roles: []string{"member", "billing"},
		Raw: raw, Credential: auth.TokenCredential(),
	}
}

func principalContext(ctx context.Context, header func(string) string) context.Context {
	if c, ok := testPrincipals[header("X-Test-Principal")]; ok {
		ctx = rlContextWithClaims(ctx, c)
	}
	return ctx
}

func principalsV2(next connectv2.ServerFunc) connectv2.ServerFunc {
	return func(ctx context.Context, spec connectv2.Spec, stream connectv2.ServerStream) error {
		if info, ok := connectv2.CallInfoForServerContext(ctx); ok {
			ctx = principalContext(ctx, info.RequestHeader().Get)
		}
		return next(ctx, spec, stream)
	}
}

var principalsV1 = connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(principalContext(ctx, req.Header().Get), req)
	}
})

// callerScopedMajors serves a counting handler behind each major's
// caller-scoped idempotency interceptor and returns, per major, a function
// calling it as a principal and the handler's run count.
func callerScopedMajors(opts IdempotencyOptions, callerOpts ...IdempotencyCallerOption) map[string]func(t *testing.T) (call func(principal string), runs *atomic.Int32) {
	return map[string]func(t *testing.T) (func(string), *atomic.Int32){
		"v2": func(t *testing.T) (func(string), *atomic.Int32) {
			runs := new(atomic.Int32)
			h := func(context.Context) (string, error) { runs.Add(1); return "ok", nil }
			c := serveV2(t, h, principalsV2, IdempotencyInterceptorV2(opts, rlClaimsFromContext, callerOpts...))
			return func(p string) {
				if r := callV2(t, c, false, map[string]string{IdempotencyKeyHeader: "shared-key", "X-Test-Principal": p}); r.err != nil {
					t.Fatalf("call as %s: %v", p, r.err)
				}
			}, runs
		},
		"v1": func(t *testing.T) (func(string), *atomic.Int32) {
			runs := new(atomic.Int32)
			h := func(context.Context) (string, error) { runs.Add(1); return "ok", nil }
			call := serveV1(t, h, principalsV1, IdempotencyInterceptorWithCaller(opts, rlClaimsFromContext, callerOpts...))
			return func(p string) {
				if err := call(map[string]string{IdempotencyKeyHeader: "shared-key", "X-Test-Principal": p}); err != nil {
					t.Fatalf("call as %s: %v", p, err)
				}
			}, runs
		},
	}
}

// Review blocker B1: two tokens with identical fixed fields and credential
// but different permissions in Raw must not share an answer — the
// restricted token's call runs the handler (and its permission check)
// instead of replaying the privileged token's response.
func TestIdempotencyCallerScoped_RawPermissionsPartitionTheCache(t *testing.T) {
	for major, serve := range callerScopedMajors(IdempotencyOptions{CacheSize: 10}) {
		t.Run(major, func(t *testing.T) {
			call, runs := serve(t)
			call("billing-manage")
			call("billing-read")
			if got := runs.Load(); got != 2 {
				t.Fatalf("handler ran %d times after the manage then read tokens, want 2: the read token replayed the manage token's answer", got)
			}
			call("billing-manage")
			call("billing-read")
			if got := runs.Load(); got != 2 {
				t.Errorf("handler ran %d times after each token's repeat, want 2 (each replays its own)", got)
			}
		})
	}
}

// A refreshed token of the same authority — differing only in iat, exp,
// nbf and jti — replays the original's answer.
func TestIdempotencyCallerScoped_VolatileClaimsShareTheEntry(t *testing.T) {
	for major, serve := range callerScopedMajors(IdempotencyOptions{CacheSize: 10}) {
		t.Run(major, func(t *testing.T) {
			call, runs := serve(t)
			call("billing-read")
			call("billing-read-refreshed")
			if got := runs.Load(); got != 1 {
				t.Errorf("handler ran %d times, want 1: a refreshed token of the same authority should replay", got)
			}
		})
	}
}

// Raw that json cannot encode has no canonical partition, so the call
// bypasses the cache: the handler runs every time and nothing is replayed.
func TestIdempotencyCallerScoped_UnencodableRawBypasses(t *testing.T) {
	for major, serve := range callerScopedMajors(IdempotencyOptions{CacheSize: 10}) {
		t.Run(major, func(t *testing.T) {
			call, runs := serve(t)
			for range 3 {
				call("unencodable")
			}
			if got := runs.Load(); got != 3 {
				t.Errorf("handler ran %d times, want 3 (bypass, never cached)", got)
			}
		})
	}
}

// A custom partition replaces the default: here one partition per org, so
// two users of one org share answers; a partition the func declines
// bypasses the cache.
func TestIdempotencyCallerScoped_CustomPartition(t *testing.T) {
	partition := WithIdempotencyPartition(func(_ context.Context, c *auth.Claims) (string, bool) {
		if c.UserID == "bob" {
			return "", false
		}
		return c.OrgID, true
	})
	for major, serve := range callerScopedMajors(IdempotencyOptions{CacheSize: 10}, partition) {
		t.Run(major, func(t *testing.T) {
			call, runs := serve(t)
			call("billing-manage")
			call("billing-read") // same org: the custom partition shares it
			if got := runs.Load(); got != 1 {
				t.Fatalf("handler ran %d times, want 1: the custom partition was not used", got)
			}
			call("bob")
			call("bob")
			if got := runs.Load(); got != 3 {
				t.Errorf("handler ran %d times, want 3: a declined partition must bypass the cache", got)
			}
		})
	}
}

// The plain v1 interceptor is unchanged: keyed by procedure and client key
// alone, it replays across callers.
func TestIdempotencyInterceptor_StaysUnscoped(t *testing.T) {
	var runs atomic.Int32
	h := func(context.Context) (string, error) { runs.Add(1); return "ok", nil }
	call := serveV1(t, h, principalsV1, IdempotencyInterceptor(IdempotencyOptions{CacheSize: 10}))
	for _, p := range []string{"billing-manage", "bob", ""} {
		if err := call(map[string]string{IdempotencyKeyHeader: "shared-key", "X-Test-Principal": p}); err != nil {
			t.Fatal(err)
		}
	}
	if got := runs.Load(); got != 1 {
		t.Errorf("handler ran %d times, want 1", got)
	}
}

func TestIdempotencyInterceptorWithCaller_Disabled(t *testing.T) {
	if IdempotencyInterceptorWithCaller(IdempotencyOptions{CacheSize: 0}, rlClaimsFromContext) != nil {
		t.Error("expected nil when CacheSize <= 0")
	}
	if IdempotencyInterceptorWithCaller(IdempotencyOptions{CacheSize: 10}, nil) != nil {
		t.Error("expected nil when claimsFrom is nil")
	}
}

// The default partition is canonical: map insertion order and role order
// do not change it, while every authority-carrying field does.
func TestClaimsPartition_CanonicalAndComplete(t *testing.T) {
	base := func() *auth.Claims {
		return &auth.Claims{
			UserID: "alice", OrgID: "acme", Email: "a@acme.test", EmailVerified: true,
			Role: "member", Roles: []string{"a", "b"},
			Raw:        map[string]any{"meta": map[string]any{"x": 1.5, "y": []any{"p", true, nil}}, "tier": "gold"},
			Credential: auth.TokenCredential(),
		}
	}
	want, ok := claimsPartition(base())
	if !ok {
		t.Fatal("base claims have no partition")
	}

	reordered := base()
	reordered.Roles = []string{"b", "a"}
	reordered.Raw = map[string]any{"tier": "gold", "meta": map[string]any{"y": []any{"p", true, nil}, "x": 1.5}, "iat": 1.0, "exp": 2.0}
	if got, _ := claimsPartition(reordered); got != want {
		t.Error("reordered roles, map keys or added volatile claims changed the partition")
	}

	for name, mutate := range map[string]func(c *auth.Claims){
		"user":           func(c *auth.Claims) { c.UserID = "bob" },
		"org":            func(c *auth.Claims) { c.OrgID = "globex" },
		"email":          func(c *auth.Claims) { c.Email = "b@acme.test" },
		"email_verified": func(c *auth.Claims) { c.EmailVerified = false },
		"role":           func(c *auth.Claims) { c.Role = "admin" },
		"roles":          func(c *auth.Claims) { c.Roles = []string{"a"} },
		"roles_split":    func(c *auth.Claims) { c.Roles = []string{"a\x00b"} },
		"credential":     func(c *auth.Claims) { c.Credential = auth.APIKeyCredential("fk_live_abc") },
		"raw_nested":     func(c *auth.Claims) { c.Raw["meta"].(map[string]any)["x"] = 2.5 },
		"raw_auth_time":  func(c *auth.Claims) { c.Raw["auth_time"] = 1.0 },
	} {
		c := base()
		mutate(c)
		if got, ok := claimsPartition(c); !ok || got == want {
			t.Errorf("%s: changing it left the partition unchanged (ok=%v)", name, ok)
		}
	}

	unencodable := base()
	unencodable.Raw["f"] = func() {}
	if _, ok := claimsPartition(unencodable); ok {
		t.Error("Raw holding a func reported a partition, want none")
	}
}
