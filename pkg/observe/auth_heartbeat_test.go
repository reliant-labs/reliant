package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/authn"
)

// heartbeatHarness calls a handler through a heartbeat whose clock and
// memory the test owns, and returns the auth events it logged.
type heartbeatHarness struct {
	t   *testing.T
	buf bytes.Buffer
	now time.Time
	hb  authHeartbeat
}

func newHeartbeatHarness(t *testing.T, provider string) *heartbeatHarness {
	h := &heartbeatHarness{t: t, now: time.Date(2026, 11, 10, 23, 59, 0, 0, time.UTC)}
	hb := AuthHeartbeatInterceptor(nil, provider).(authHeartbeat)
	hb.seen = &resumedSet{}
	hb.now = func() time.Time { return h.now }
	h.hb = hb
	return h
}

func (h *heartbeatHarness) call(claims *authn.Claims) {
	h.t.Helper()
	ctx := WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&h.buf, nil)))
	if claims != nil {
		ctx = authn.ContextWithClaims(ctx, claims)
	}
	handler := func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) { return nil, nil }
	if _, err := h.hb.WrapUnary(handler)(ctx, connect.NewRequest(&struct{}{})); err != nil {
		h.t.Fatal(err)
	}
}

func (h *heartbeatHarness) events() []map[string]any {
	h.t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(h.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			h.t.Fatalf("bad line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestAuthHeartbeatRecordsOncePerUserPerUTCDay(t *testing.T) {
	h := newHeartbeatHarness(t, "auth0")
	h.call(&authn.Claims{UserID: "auth0|a"})
	h.call(&authn.Claims{UserID: "auth0|a"})
	h.call(&authn.Claims{UserID: "auth0|b"})
	h.now = h.now.Add(2 * time.Minute) // past midnight UTC
	h.call(&authn.Claims{UserID: "auth0|a"})

	ev := h.events()
	var users []string
	for _, m := range ev {
		if m["event.name"] != "product.auth_signed_in" || m["product.kind"] != "resumed" || m["product.provider"] != "auth0" {
			t.Errorf("unexpected record %v", m)
		}
		users = append(users, m["user.id"].(string))
	}
	if got := strings.Join(users, ","); got != "auth0|a,auth0|b,auth0|a" {
		t.Errorf("recorded for %s, want auth0|a,auth0|b then auth0|a again on the next day", got)
	}
}

func TestAuthHeartbeatSkipsAnonymousAndAPIKeyCallers(t *testing.T) {
	h := newHeartbeatHarness(t, "")
	h.call(nil)
	h.call(&authn.Claims{})
	h.call(&authn.Claims{UserID: "svc-1", Credential: auth.APIKeyCredential("fk_abc")})
	if ev := h.events(); len(ev) != 0 {
		t.Errorf("recorded %v for callers that are not signed-in users", ev)
	}
}

func TestAuthHeartbeatProviderDefault(t *testing.T) {
	t.Setenv(EnvIDPBrokerToken, "")
	t.Setenv(EnvIDPBrokerToken+"_FILE", "")
	if p := AuthHeartbeatInterceptor(nil, "").(authHeartbeat).provider; p != AuthProviderJWT {
		t.Errorf("provider = %q, want jwt for an unnamed issuer", p)
	}
	t.Setenv(EnvIDPBrokerToken+"_FILE", "/run/secrets/broker")
	if p := AuthHeartbeatInterceptor(nil, "").(authHeartbeat).provider; p != AuthProviderForge {
		t.Errorf("provider = %q, want forge with the managed broker's credential", p)
	}
	if p := AuthHeartbeatInterceptor(nil, "clerk").(authHeartbeat).provider; p != "clerk" {
		t.Errorf("provider = %q, want the named one", p)
	}
}

func TestResumedSetIsBounded(t *testing.T) {
	s := &resumedSet{}
	for i := 0; i < MaxResumedUsersPerDay; i++ {
		s.first(strconv.Itoa(i), "d")
	}
	if len(s.seen) > MaxResumedUsersPerDay {
		t.Fatalf("remembered %d users, cap %d", len(s.seen), MaxResumedUsersPerDay)
	}
	s.first("one-more", "d")
	if len(s.seen) != 1 {
		t.Errorf("a full memory was not restarted: %d entries", len(s.seen))
	}
}

func TestChainRunsTheHeartbeatAfterAuth(t *testing.T) {
	chain := Chain(Deps{Auth: connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc { return next })})
	n := 0
	for _, ic := range chain {
		if _, ok := ic.(authHeartbeat); ok {
			n++
		}
	}
	if n != 1 {
		t.Errorf("Chain with Auth installs %d heartbeats, want 1", n)
	}
	for _, ic := range Chain(Deps{}) {
		if _, ok := ic.(authHeartbeat); ok {
			t.Error("Chain without Auth installs a heartbeat")
		}
	}
}

func TestAuthHeartbeatRecordsTheNamedProvider(t *testing.T) {
	h := newHeartbeatHarness(t, "supabase")
	h.call(&authn.Claims{UserID: "u-named"})
	ev := h.events()
	if len(ev) != 1 || ev[0]["product.provider"] != "supabase" {
		t.Fatalf("events = %v, want one auth_signed_in with provider supabase", ev)
	}
}
