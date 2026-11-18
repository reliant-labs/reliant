package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/authn"
)

// trackLines runs Track and returns every record it logged, in order.
func trackLines(t *testing.T, ctx context.Context, name string, attrs ...slog.Attr) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	ctx = WithLogger(ctx, slog.New(slog.NewJSONHandler(&buf, nil)))
	Track(ctx, name, attrs...)
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

// eventRecord returns the product event among recs, failing when there is none.
func eventRecord(t *testing.T, recs []map[string]any) map[string]any {
	t.Helper()
	for _, m := range recs {
		if name, _ := m["event.name"].(string); strings.HasPrefix(name, EventNamePrefix) {
			return m
		}
	}
	t.Fatalf("no product event in %v", recs)
	return nil
}

func TestTrackStampsOrgFromClaims(t *testing.T) {
	ctx := authn.ContextWithClaims(context.Background(), &authn.Claims{UserID: "u-1", OrgID: "org-1"})
	m := trackLine(t, ctx, "project_created")
	if m["user.id"] != "u-1" || m["org.id"] != "org-1" {
		t.Errorf("want user.id u-1 and org.id org-1, got %v", m)
	}
}

func TestWithActorFillsNoClaimsPaths(t *testing.T) {
	ctx := WithActor(context.Background(), Actor{OrgID: "org-2", UserID: "u-2", AnonymousID: "inst-1"})
	m := trackLine(t, ctx, "deploy_succeeded")
	for k, want := range map[string]string{"org.id": "org-2", "user.id": "u-2", "visitor.id": "inst-1"} {
		if m[k] != want {
			t.Errorf("%s = %v, want %s (line %v)", k, m[k], want, m)
		}
	}
}

func TestWithActorWinsOverClaimsAndMerges(t *testing.T) {
	ctx := authn.ContextWithClaims(context.Background(), &authn.Claims{UserID: "u-claims", OrgID: "org-claims"})
	ctx = WithActor(ctx, Actor{OrgID: "org-actor"})
	ctx = WithActor(ctx, Actor{AnonymousID: "inst-9"})
	a := ActorFrom(ctx)
	if a.OrgID != "org-actor" || a.UserID != "u-claims" || a.AnonymousID != "inst-9" {
		t.Errorf("ActorFrom = %+v, want org-actor / u-claims / inst-9", a)
	}
}

func TestActorRefusesNonOpaqueIDs(t *testing.T) {
	for _, id := range []string{"someone@example.com", "10.0.0.1", "2001:db8::1", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig", strings.Repeat("a", 129)} {
		if ValidActorID(id) {
			t.Errorf("ValidActorID(%q) = true, want false", id)
		}
	}
	for _, id := range []string{"", "user_2NNEqL2nrIRdJ194ndJqAHwEfxC", "auth0|65f0c1d2e3", "550e8400-e29b-41d4-a716-446655440000", strings.Repeat("a", 64)} {
		if !ValidActorID(id) {
			t.Errorf("ValidActorID(%q) = false, want true", id)
		}
	}
	ctx := WithActor(context.Background(), Actor{UserID: "someone@example.com", OrgID: "org-3"})
	m := eventRecord(t, trackLines(t, ctx, "project_created"))
	if _, ok := m["user.id"]; ok || m["org.id"] != "org-3" {
		t.Errorf("an email user id must be dropped and the event kept: %v", m)
	}
}

func TestTrackDropsPIIProps(t *testing.T) {
	recs := trackLines(t, context.Background(), "invite_sent",
		slog.String("role", "admin"),
		slog.String("invitee", "someone@example.com"),
		slog.String("client_ip", "203.0.113.7"),
		slog.String("auth", "Bearer abcdefghijklmnop"),
		slog.String("session", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln"),
		slog.String("key", "sk-ant-api03-abcdefghijkl"),
		slog.String("version", "1.4.0"),
		slog.String("id", "550e8400-e29b-41d4-a716-446655440000"),
	)
	m := eventRecord(t, recs)
	for _, k := range []string{"invitee", "client_ip", "auth", "session", "key"} {
		if v, ok := m["product."+k]; ok {
			t.Errorf("product.%s = %v survived the PII guard", k, v)
		}
	}
	for _, k := range []string{"role", "version", "id"} {
		if _, ok := m["product."+k]; !ok {
			t.Errorf("product.%s was dropped but is not PII: %v", k, m)
		}
	}
	for _, r := range recs {
		if r["level"] == "WARN" {
			if s, _ := json.Marshal(r); strings.Contains(string(s), "example.com") {
				t.Errorf("the PII warning must name the key, never the value: %s", s)
			}
		}
	}
}

func TestLooksLikePII(t *testing.T) {
	for _, v := range []string{"a.b@c.io", "192.168.1.1", "::1", "postgres://u:p@h/db", "password=hunter2", "ghp_abcdefghijklmnopqrst", strings.Repeat("Z", 40)} {
		if !LooksLikePII(v) {
			t.Errorf("LooksLikePII(%q) = false", v)
		}
	}
	for _, v := range []string{"", "claimable", "true", "42", "10-100ms", "linux", "arm64", "v1.4.0", "noun.verb", "first_for_org"} {
		if LooksLikePII(v) {
			t.Errorf("LooksLikePII(%q) = true", v)
		}
	}
}

func TestConsentSourceFailsClosed(t *testing.T) {
	t.Cleanup(func() { SetConsentSource(nil) })
	ctx := WithActor(context.Background(), Actor{UserID: "u-opted-out"})

	SetConsentSource(ConsentFunc(func(_ context.Context, a Actor) (bool, error) { return a.UserID != "u-opted-out", nil }))
	if recs := trackLines(t, ctx, "project_created"); len(recs) != 0 {
		t.Errorf("an opted-out subject's event was recorded: %v", recs)
	}
	if recs := trackLines(t, WithActor(context.Background(), Actor{UserID: "u-ok"}), "project_created"); len(recs) != 1 {
		t.Errorf("an allowed subject's event must be recorded once, got %v", recs)
	}

	SetConsentSource(ConsentFunc(func(context.Context, Actor) (bool, error) { return true, errors.New("store down") }))
	for _, r := range trackLines(t, ctx, "project_created") {
		if r["level"] != "WARN" {
			t.Errorf("a failed consent lookup must drop the event: %v", r)
		}
	}

	SetConsentSource(ConsentFunc(func(context.Context, Actor) (bool, error) { panic("boom") }))
	for _, r := range trackLines(t, ctx, "project_created") {
		if r["level"] != "WARN" {
			t.Errorf("a panicking consent lookup must drop the event: %v", r)
		}
	}
}

// The consent WARN names only the failure's class: the source's error text
// and panic value never reach the output, which is telemetry too.
func TestConsentFailureWarningLeaksNeitherErrorNorPanic(t *testing.T) {
	t.Cleanup(func() { SetConsentSource(nil); consentRefused.Store(false) })
	ctx := WithActor(context.Background(), Actor{UserID: "u-1"})
	for _, tc := range []struct {
		failure, secret string
		src             ConsentFunc
	}{
		{"error", "ada@example.com password=hunter2", func(context.Context, Actor) (bool, error) {
			return true, errors.New("lookup for ada@example.com password=hunter2 failed")
		}},
		{"panic", "ghp_abcdefghijklmnopqrst", func(context.Context, Actor) (bool, error) {
			panic("token ghp_abcdefghijklmnopqrst rejected")
		}},
	} {
		consentRefused.Store(false)
		SetConsentSource(tc.src)
		recs := trackLines(t, ctx, "project_created")
		if len(recs) != 1 || recs[0]["level"] != "WARN" || recs[0]["failure"] != tc.failure {
			t.Errorf("%s: want one WARN with failure=%s, got %v", tc.failure, tc.failure, recs)
		}
		for _, r := range recs {
			out, _ := json.Marshal(r)
			for _, part := range strings.Fields(tc.secret) {
				if strings.Contains(string(out), part) {
					t.Errorf("%s: the warning leaked %q: %s", tc.failure, part, out)
				}
			}
		}
	}
}

func TestTrackAuthCarriesActorOrg(t *testing.T) {
	var buf bytes.Buffer
	ctx := WithLogger(WithActor(context.Background(), Actor{OrgID: "org-7"}), slog.New(slog.NewJSONHandler(&buf, nil)))
	TrackAuth(ctx, AuthSignedUp, "u-7")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("bad line %q: %v", buf.String(), err)
	}
	if m["user.id"] != "u-7" || m["org.id"] != "org-7" {
		t.Errorf("TrackAuth must keep the explicit user and the actor's org: %v", m)
	}
}
