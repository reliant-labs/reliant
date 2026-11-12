package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/authn"
)

// authLines records what TrackAuth logs, one map per line.
func authLines(t *testing.T, ctx context.Context, ev AuthEvent, userID string, opts ...AuthOption) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	ctx = WithLogger(ctx, slog.New(slog.NewJSONHandler(&buf, nil)))
	TrackAuth(ctx, ev, userID, opts...)
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func authLine(t *testing.T, ev AuthEvent, userID string, opts ...AuthOption) map[string]any {
	t.Helper()
	lines := authLines(t, context.Background(), ev, userID, opts...)
	if len(lines) != 1 {
		t.Fatalf("want one record, got %d: %v", len(lines), lines)
	}
	return lines[0]
}

func productProps(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		if strings.HasPrefix(k, AttrPropPrefix) {
			out[strings.TrimPrefix(k, AttrPropPrefix)] = v
		}
	}
	return out
}

func TestTrackAuthShape(t *testing.T) {
	SetAppVersion("1.2.3")
	t.Cleanup(func() { SetAppVersion("") })

	m := authLine(t, AuthSignedIn, "auth0|abc", WithMethod(AuthMethodPassword), WithProvider("auth0"))
	want := map[string]any{
		"level": "INFO", "msg": "product.auth_signed_in", "event.name": "product.auth_signed_in",
		"user.id": "auth0|abc", "app.client": "backend", "app.version": "1.2.3",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v (line %v)", k, m[k], v, m)
		}
	}
	wantProps := map[string]any{"kind": "credential", "method": "password", "provider": "auth0"}
	if got := productProps(m); !equalMaps(got, wantProps) {
		t.Errorf("props = %v, want %v", got, wantProps)
	}
}

func TestTrackAuthUsesTheGivenIDNotTheCaller(t *testing.T) {
	// At sign-in the request is anonymous; a signed-in admin creating a user
	// must not record the event under the admin.
	ctx := authn.ContextWithClaims(context.Background(), &authn.Claims{UserID: "admin-1"})
	lines := authLines(t, ctx, AuthSignedUp, "user-2")
	if len(lines) != 1 || lines[0]["user.id"] != "user-2" {
		t.Fatalf("user.id = %v, want user-2", lines)
	}
}

func TestTrackAuthPropsPerEvent(t *testing.T) {
	all := []AuthOption{
		WithKind(AuthKindResumed), WithMethod(AuthMethodSocial), WithSocialProvider("GitHub"),
		WithProvider(AuthProviderForge), WithVerified(true),
	}
	cases := []struct {
		ev   AuthEvent
		want map[string]any
	}{
		{AuthSignedUp, map[string]any{"method": "social", "social_provider": "github", "provider": "forge", "verified": "true"}},
		{AuthSignedIn, map[string]any{"kind": "resumed", "method": "social", "social_provider": "github", "provider": "forge"}},
		{AuthSignedOut, map[string]any{"provider": "forge"}},
	}
	for _, tc := range cases {
		if got := productProps(authLine(t, tc.ev, "u-1", all...)); !equalMaps(got, tc.want) {
			t.Errorf("%s props = %v, want %v", tc.ev, got, tc.want)
		}
	}
}

func TestTrackAuthBoundsVocabularies(t *testing.T) {
	m := productProps(authLine(t, AuthSignedUp, "u-1",
		WithMethod("carrier_pigeon"), WithProvider("Not A Provider"), WithVerified(false)))
	want := map[string]any{"method": "other", "provider": "other", "verified": "false"}
	if !equalMaps(m, want) {
		t.Errorf("props = %v, want %v", m, want)
	}

	m = productProps(authLine(t, AuthSignedIn, "u-1", WithMethod(AuthMethodSocial), WithSocialProvider("myspace")))
	if m["social_provider"] != "other" {
		t.Errorf("social_provider = %v, want other", m["social_provider"])
	}
	m = productProps(authLine(t, AuthSignedIn, "u-1", WithKind("sideways"), WithProvider("my_idp")))
	if m["kind"] != "credential" || m["provider"] != "my_idp" {
		t.Errorf("props = %v, want kind=credential provider=my_idp", m)
	}
	// A social provider on a non-social method is not recorded.
	m = productProps(authLine(t, AuthSignedIn, "u-1", WithMethod(AuthMethodPasskey), WithSocialProvider("google")))
	if _, ok := m["social_provider"]; ok {
		t.Errorf("social_provider recorded for a passkey sign-in: %v", m)
	}
}

func TestTrackAuthRefusesIDsThatAreNotOpaqueSubjects(t *testing.T) {
	authIDRefused.Store(false)
	t.Cleanup(func() { authIDRefused.Store(false) })

	for i, id := range []string{"", "ada@example.com", strings.Repeat("x", MaxAuthUserIDLen+1)} {
		lines := authLines(t, context.Background(), AuthSignedIn, id)
		for _, m := range lines {
			if _, ok := m["event.name"]; ok {
				t.Errorf("id %q was recorded: %v", id, m)
			}
			if (id != "" && strings.Contains(m["msg"].(string), id)) || m["user.id"] != nil {
				t.Errorf("the refusal leaked the id: %v", m)
			}
		}
		// One WARN per process, not one per sign-in.
		if want := map[bool]int{true: 1, false: 0}[i == 0]; len(lines) != want {
			t.Errorf("id %q: %d lines, want %d: %v", id, len(lines), want, lines)
		}
	}
	if !ValidAuthUserID(strings.Repeat("x", MaxAuthUserIDLen)) {
		t.Error("a 128-character id was refused")
	}
}

func TestTrackAuthRefusesUnknownEvents(t *testing.T) {
	lines := authLines(t, context.Background(), AuthEvent("auth_exploded"), "u-1")
	if len(lines) != 1 || lines[0]["level"] != "WARN" || lines[0]["event.name"] != nil {
		t.Errorf("unknown event: %v", lines)
	}
}

func equalMaps(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
