// Copyright (c) 2025 Reliant Labs
package providerusage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
)

var fixedNow = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func win(t *testing.T, s ratelimit.Snapshot, name string) ratelimit.Window {
	t.Helper()
	w, ok := s.Window(name)
	if !ok {
		names := []string{}
		for _, x := range s.Windows {
			names = append(names, x.Name)
		}
		t.Fatalf("window %q missing; have %v", name, names)
	}
	return w
}

func wantUsed(t *testing.T, w ratelimit.Window, want float64) {
	t.Helper()
	got, ok := w.Used()
	if !ok || got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("window %s used = %v (ok=%v), want %v", w.Name, got, ok, want)
	}
}

// rewriteDoer sends every request to srv regardless of the URL's host, so tests
// can assert the exact path and headers the real endpoint would receive.
type rewriteDoer struct{ srv *httptest.Server }

func (d rewriteDoer) Do(r *http.Request) (*http.Response, error) {
	u := *r.URL
	r2 := r.Clone(r.Context())
	r2.URL.Scheme = "http"
	r2.URL.Host = strings.TrimPrefix(d.srv.URL, "http://")
	r2.Host = u.Host
	return d.srv.Client().Do(r2)
}

func TestParseClaudeUsage_LimitsArrayAndWindows(t *testing.T) {
	s, err := ParseClaudeUsage(fixture(t, "claude_usage.json"), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if s.Provider != ProviderClaude || !s.ObservedAt.Equal(fixedNow) {
		t.Fatalf("provider/observed: %+v", s)
	}
	h5 := win(t, s, "5h")
	wantUsed(t, h5, 0.42)
	if h5.Length != 5*time.Hour {
		t.Fatalf("5h length = %v", h5.Length)
	}
	if want := time.Date(2026, 9, 2, 22, 0, 0, 123456000, time.UTC); !h5.ResetsAt.Equal(want) {
		t.Fatalf("5h reset = %v, want %v", h5.ResetsAt, want)
	}
	d7 := win(t, s, "7d")
	wantUsed(t, d7, 0.88)
	if d7.Length != 7*24*time.Hour {
		t.Fatalf("7d length = %v", d7.Length)
	}
	wantUsed(t, win(t, s, "7d.fable"), 1)
	wantUsed(t, win(t, s, "7d.sonnet_5"), 0.125)
	if _, ok := s.Window("7d.opus"); ok {
		t.Fatal("null seven_day_opus must not produce a window")
	}
	if len(s.Windows) != 4 {
		t.Fatalf("want 4 windows (5h, 7d, fable, sonnet_5), got %d: %+v", len(s.Windows), s.Windows)
	}
	if s.Status != ratelimit.StatusRejected {
		t.Fatalf("a 100%% scoped row must make the snapshot rejected, got %s", s.Status)
	}
}

func TestParseClaudeUsage_LegacyModelKeys(t *testing.T) {
	s, err := ParseClaudeUsage(fixture(t, "claude_usage_legacy_opus.json"), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	wantUsed(t, win(t, s, "7d.opus"), 0.64)
	if _, ok := s.Window("7d.sonnet"); ok {
		t.Fatal("null seven_day_sonnet must not produce a window")
	}
	if s.Status != ratelimit.StatusAllowed {
		t.Fatalf("status = %s", s.Status)
	}
}

func TestParseClaudeUsage_LimitsRowWinsOverLegacyKey(t *testing.T) {
	body := `{"seven_day":{"utilization":10},"seven_day_opus":{"utilization":20},
	  "limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Opus"}},"percent":55}]}`
	s, err := ParseClaudeUsage([]byte(body), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	wantUsed(t, win(t, s, "7d.opus"), 0.55)
}

func TestParseClaudeUsage_Tolerance(t *testing.T) {
	// Utilization as a numeric string, an over-100 clamp, an epoch reset, and a
	// window whose utilization is junk.
	body := `{"five_hour":{"utilization":"33.5","resets_at":1790000000},
	  "seven_day":{"utilization":140},"seven_day_opus":{"utilization":"n/a"}}`
	s, err := ParseClaudeUsage([]byte(body), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	h5 := win(t, s, "5h")
	wantUsed(t, h5, 0.335)
	if !h5.ResetsAt.Equal(time.Unix(1790000000, 0).UTC()) {
		t.Fatalf("epoch reset = %v", h5.ResetsAt)
	}
	wantUsed(t, win(t, s, "7d"), 1)
	if _, ok := s.Window("7d.opus"); ok {
		t.Fatal("junk utilization must drop the window")
	}
}

func TestParseClaudeUsage_Errors(t *testing.T) {
	for name, body := range map[string]string{
		"not json":   `<html>`,
		"array":      `[]`,
		"null":       `null`,
		"no windows": `{"error":{"type":"x"}}`,
	} {
		if _, err := ParseClaudeUsage([]byte(body), fixedNow); !errors.Is(err, ErrUnexpectedResponse) {
			t.Errorf("%s: err = %v, want ErrUnexpectedResponse", name, err)
		}
	}
	// Recognised keys but every window null: valid, empty, status unknown.
	s, err := ParseClaudeUsage([]byte(`{"five_hour":null,"seven_day":null}`), fixedNow)
	if err != nil || len(s.Windows) != 0 || s.Status != ratelimit.StatusUnknown {
		t.Fatalf("all-null: %+v, %v", s, err)
	}
}

func TestParseClaudeProfile(t *testing.T) {
	p, err := ParseClaudeProfile(fixture(t, "claude_profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	if p.AccountUUID != "00000000-aaaa-4000-8000-000000000001" || p.OrganizationUUID != "00000000-bbbb-4000-8000-000000000002" {
		t.Fatalf("ids: %+v", p)
	}
	if p.Email != "test.user@example.invalid" || p.OrganizationType != "claude_max" || p.RateLimitTier != "default_claude_max_20x" {
		t.Fatalf("fields: %+v", p)
	}
	if p.Plan != "max_20x" {
		t.Fatalf("plan = %q", p.Plan)
	}
}

func TestParseClaudeProfile_FallbacksAndErrors(t *testing.T) {
	// email_address spelling, plan from the has_claude_* flag when org is silent.
	p, err := ParseClaudeProfile([]byte(`{"account":{"uuid":"A","email_address":"x@y.z","has_claude_pro":true}}`))
	if err != nil || p.Email != "x@y.z" || p.Plan != "pro" {
		t.Fatalf("%+v, %v", p, err)
	}
	for _, body := range []string{`nope`, `{}`, `{"application":{"name":"x"}}`} {
		if _, err := ParseClaudeProfile([]byte(body)); !errors.Is(err, ErrUnexpectedResponse) {
			t.Errorf("%q: err = %v", body, err)
		}
	}
}

func TestParseCodexUsage(t *testing.T) {
	s, err := ParseCodexUsage(fixture(t, "codex_usage.json"), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if s.Provider != ProviderCodex || s.PlanType != "plus" || s.LimitID != "codex" || s.Status != ratelimit.StatusAllowed {
		t.Fatalf("snapshot: %+v", s)
	}
	pr := win(t, s, "primary")
	wantUsed(t, pr, 0.25)
	if pr.Length != 5*time.Hour || !pr.ResetsAt.Equal(time.Unix(1790000900, 0).UTC()) {
		t.Fatalf("primary: %+v", pr)
	}
	sec := win(t, s, "secondary")
	wantUsed(t, sec, 0.61)
	if sec.Length != 7*24*time.Hour {
		t.Fatalf("secondary length = %v", sec.Length)
	}
	spark := win(t, s, "codex_bengalfox.primary")
	wantUsed(t, spark, 0.03)
	if _, ok := s.Window("codex_bengalfox.secondary"); ok {
		t.Fatal("null secondary must not produce a window")
	}
	if len(s.Windows) != 3 {
		t.Fatalf("windows: %+v", s.Windows)
	}
}

func TestParseCodexUsage_WrappedAndRelativeReset(t *testing.T) {
	s, err := ParseCodexUsage(fixture(t, "codex_usage_wrapped.json"), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if s.PlanType != "pro" {
		t.Fatalf("plan = %q", s.PlanType)
	}
	pr := win(t, s, "primary")
	wantUsed(t, pr, 0.10)
	// No reset_at: derived from reset_after_seconds against the injected clock.
	if want := fixedNow.Add(600 * time.Second); !pr.ResetsAt.Equal(want) {
		t.Fatalf("relative reset = %v, want %v", pr.ResetsAt, want)
	}
	// A string epoch reset_at is accepted.
	if sec := win(t, s, "secondary"); !sec.ResetsAt.Equal(time.Unix(1790086400, 0).UTC()) {
		t.Fatalf("string epoch reset = %v", sec.ResetsAt)
	}
}

func TestParseCodexUsage_Limited(t *testing.T) {
	s, err := ParseCodexUsage(fixture(t, "codex_usage_limited.json"), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != ratelimit.StatusRejected {
		t.Fatalf("status = %s", s.Status)
	}
	wantUsed(t, win(t, s, "primary"), 1)
	// Exhausted purely by window math, with limit_reached unset.
	s2, err := ParseCodexUsage([]byte(`{"plan_type":"plus","rate_limit":{"allowed":true,"primary_window":{"used_percent":100,"reset_at":1790003000}}}`), fixedNow)
	if err != nil || s2.Status != ratelimit.StatusRejected {
		t.Fatalf("window math: %+v, %v", s2, err)
	}
}

func TestParseCodexUsage_AdditionalLimitDoesNotRejectSnapshot(t *testing.T) {
	body := `{"plan_type":"plus","rate_limit":{"allowed":true,"limit_reached":false,
	  "primary_window":{"used_percent":5,"limit_window_seconds":18000,"reset_at":1790003000}},
	  "additional_rate_limits":[{"limit_name":"Spark","metered_feature":"spark","rate_limit":
	  {"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100,"reset_at":1790003000}}}]}`
	s, err := ParseCodexUsage([]byte(body), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != ratelimit.StatusAllowed {
		t.Fatalf("a model-specific cap must not reject the subscription, got %s", s.Status)
	}
	wantUsed(t, win(t, s, "spark.primary"), 1)
}

func TestParseCodexUsage_Errors(t *testing.T) {
	for name, body := range map[string]string{
		"not json": `oops`, "array": `[1]`, "empty object": `{}`, "error body": `{"detail":"nope"}`,
	} {
		if _, err := ParseCodexUsage([]byte(body), fixedNow); !errors.Is(err, ErrUnexpectedResponse) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestClaudeUsage_ExactRequest(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		_, _ = w.Write(fixture(t, "claude_usage.json"))
	}))
	defer srv.Close()
	s, err := ClaudeUsage(context.Background(), rewriteDoer{srv}, " tok-123 ")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Windows) == 0 {
		t.Fatal("no windows parsed")
	}
	if got.Method != http.MethodGet || got.URL.Path != "/api/oauth/usage" || got.Host != "api.anthropic.com" {
		t.Fatalf("request: %s %s host=%s", got.Method, got.URL.Path, got.Host)
	}
	for k, want := range map[string]string{
		"Authorization":  "Bearer tok-123",
		"Anthropic-Beta": "oauth-2025-04-20",
		"User-Agent":     "claude-cli/2.1.204 (external, cli)",
		"Accept":         "application/json",
	} {
		if v := got.Header.Get(k); v != want {
			t.Errorf("header %s = %q, want %q", k, v, want)
		}
	}
}

func TestClaudeProfile_ExactRequest(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		_, _ = w.Write(fixture(t, "claude_profile.json"))
	}))
	defer srv.Close()
	p, err := ClaudeProfile(context.Background(), rewriteDoer{srv}, "tok")
	if err != nil || p.Plan != "max_20x" {
		t.Fatalf("%+v, %v", p, err)
	}
	if got.URL.Path != "/api/oauth/profile" || got.Host != "api.anthropic.com" || got.Header.Get("Anthropic-Beta") != "oauth-2025-04-20" {
		t.Fatalf("request: %s host=%s beta=%q", got.URL.Path, got.Host, got.Header.Get("Anthropic-Beta"))
	}
}

func TestCodexUsage_ExactRequest(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		_, _ = w.Write(fixture(t, "codex_usage.json"))
	}))
	defer srv.Close()
	if _, err := CodexUsage(context.Background(), rewriteDoer{srv}, "tok-abc", "acct-9"); err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != "/backend-api/wham/usage" || got.Host != "chatgpt.com" {
		t.Fatalf("request: %s host=%s", got.URL.Path, got.Host)
	}
	if got.Header.Get("Authorization") != "Bearer tok-abc" || got.Header.Get("ChatGPT-Account-Id") != "acct-9" {
		t.Fatalf("auth headers: %v", got.Header)
	}
	if ua := got.Header.Get("User-Agent"); !strings.HasPrefix(ua, "codex-tui/") {
		t.Fatalf("user agent = %q", ua)
	}

	// No account id: the header is omitted, not sent empty.
	if _, err := CodexUsage(context.Background(), rewriteDoer{srv}, "tok-abc", ""); err != nil {
		t.Fatal(err)
	}
	if _, present := got.Header["Chatgpt-Account-Id"]; present {
		t.Fatalf("empty account id must omit the header: %v", got.Header)
	}
}

func TestThrottled429CarriesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error"}}`))
	}))
	defer srv.Close()
	for name, call := range map[string]func() error{
		"claude usage":   func() error { _, err := ClaudeUsage(context.Background(), rewriteDoer{srv}, "t"); return err },
		"claude profile": func() error { _, err := ClaudeProfile(context.Background(), rewriteDoer{srv}, "t"); return err },
		"codex usage":    func() error { _, err := CodexUsage(context.Background(), rewriteDoer{srv}, "t", "a"); return err },
	} {
		err := call()
		if !errors.Is(err, ErrThrottled) {
			t.Fatalf("%s: err = %v, want ErrThrottled", name, err)
		}
		var te *ThrottledError
		if !errors.As(err, &te) || te.RetryAfter != 120*time.Second {
			t.Fatalf("%s: throttled = %+v", name, te)
		}
		if errors.Is(err, ErrUnauthorized) {
			t.Fatalf("%s: a 429 is not an auth failure", name)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":        0,
		"0":       0, // Claude sends 0 while still refusing: unknown
		"30":      30 * time.Second,
		"1.5":     1500 * time.Millisecond,
		"garbage": 0,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second,
		now.Add(-time.Minute).Format(http.TimeFormat):     0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestStatusErrors(t *testing.T) {
	for code, auth := range map[int]bool{401: true, 403: true, 404: false, 500: false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(strings.Repeat("x", 500)))
		}))
		_, err := ClaudeUsage(context.Background(), rewriteDoer{srv}, "super-secret-token")
		srv.Close()
		var se *StatusError
		if !errors.As(err, &se) || se.StatusCode != code {
			t.Fatalf("%d: err = %v", code, err)
		}
		if errors.Is(err, ErrUnauthorized) != auth {
			t.Fatalf("%d: ErrUnauthorized = %v, want %v", code, !auth, auth)
		}
		if len(se.Body) != errBodySnippet {
			t.Fatalf("%d: body excerpt len = %d", code, len(se.Body))
		}
		if strings.Contains(err.Error(), "super-secret-token") {
			t.Fatalf("error leaks the token: %v", err)
		}
	}
}

func TestEmptyTokenNeverCallsOut(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	d := rewriteDoer{srv}
	if _, err := ClaudeUsage(context.Background(), d, "  "); err == nil {
		t.Fatal("empty token must fail")
	}
	if _, err := ClaudeProfile(context.Background(), d, ""); err == nil {
		t.Fatal("empty token must fail")
	}
	if _, err := CodexUsage(context.Background(), d, "", "a"); err == nil {
		t.Fatal("empty token must fail")
	}
	if called {
		t.Fatal("no request may be made without a token")
	}
}

func TestUsageAPIEnabled(t *testing.T) {
	if !UsageAPIEnabled(ProviderClaude) || !UsageAPIEnabled(ProviderCodex) {
		t.Fatal("claude and codex usage APIs are enabled on public evidence")
	}
	if UsageAPIEnabled("openai") || UsageAPIEnabled("") {
		t.Fatal("providers without a usage API are disabled")
	}
}

func jwt(t *testing.T, claims map[string]any) string {
	t.Helper()
	b, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(b) + ".sig"
}

func TestCodexIdentity(t *testing.T) {
	auth := map[string]any{
		"chatgpt_account_id":      "acct-1",
		"chatgpt_account_user_id": "user-acct-1__u9",
		"chatgpt_user_id":         "user-9",
		"chatgpt_plan_type":       "plus",
	}
	t.Run("top-level email", func(t *testing.T) {
		id, err := CodexIdentity(jwt(t, map[string]any{
			"email":                       "Top@Example.invalid",
			"https://api.openai.com/auth": auth,
		}), "")
		if err != nil {
			t.Fatal(err)
		}
		if id.Email != "Top@Example.invalid" || id.AccountID != "acct-1" || id.UserID != "user-acct-1__u9" || id.Plan != "plus" || id.PlanRaw != "plus" {
			t.Fatalf("%+v", id)
		}
		if id.IdentityKey() != "acct:acct-1/user:user-acct-1__u9" {
			t.Fatalf("key = %q", id.IdentityKey())
		}
	})
	t.Run("profile email", func(t *testing.T) {
		id, err := CodexIdentity(jwt(t, map[string]any{
			"https://api.openai.com/profile": map[string]any{"email": "prof@example.invalid"},
			"https://api.openai.com/auth":    auth,
		}), "")
		if err != nil || id.Email != "prof@example.invalid" {
			t.Fatalf("%+v, %v", id, err)
		}
	})
	t.Run("top-level wins over profile", func(t *testing.T) {
		id, _ := CodexIdentity(jwt(t, map[string]any{
			"email":                          "a@example.invalid",
			"https://api.openai.com/profile": map[string]any{"email": "b@example.invalid"},
		}), "")
		if id.Email != "a@example.invalid" {
			t.Fatalf("%+v", id)
		}
	})
	t.Run("user id fallbacks", func(t *testing.T) {
		id, _ := CodexIdentity(jwt(t, map[string]any{"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "a", "chatgpt_user_id": "u1", "user_id": "u2"}}), "")
		if id.UserID != "u1" {
			t.Fatalf("chatgpt_user_id before user_id: %+v", id)
		}
		id, _ = CodexIdentity(jwt(t, map[string]any{"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "a", "user_id": "u2"}}), "")
		if id.UserID != "u2" {
			t.Fatalf("user_id last: %+v", id)
		}
	})
	t.Run("access token fills what the id token lacks", func(t *testing.T) {
		id, err := CodexIdentity(
			jwt(t, map[string]any{"email": "e@example.invalid"}),
			jwt(t, map[string]any{"https://api.openai.com/auth": auth}))
		if err != nil || id.Email != "e@example.invalid" || id.AccountID != "acct-1" || id.Plan != "plus" {
			t.Fatalf("%+v, %v", id, err)
		}
	})
	t.Run("incomplete identity has no key", func(t *testing.T) {
		id, _ := CodexIdentity(jwt(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "a"}}), "")
		if id.IdentityKey() != "" {
			t.Fatalf("key = %q", id.IdentityKey())
		}
	})
	t.Run("not a jwt", func(t *testing.T) {
		if _, err := CodexIdentity("opaque-token", ""); err == nil {
			t.Fatal("want error")
		}
		if _, err := CodexIdentity("a.!!!.c", ""); err == nil {
			t.Fatal("want error for bad base64")
		}
	})
	t.Run("both empty", func(t *testing.T) {
		id, err := CodexIdentity("", " ")
		if err != nil || id != (CodexIdentityInfo{}) {
			t.Fatalf("%+v, %v", id, err)
		}
	})
}

func TestNormalizePlan(t *testing.T) {
	cases := []struct {
		provider, a, b, want string
	}{
		{ProviderClaude, "claude_max", "default_claude_max_20x", "max_20x"},
		{ProviderClaude, "claude_max", "default_claude_max_5x", "max_5x"},
		{ProviderClaude, "claude_max", "", "max"},
		{ProviderClaude, "max", "default_claude_max_20x", "max_20x"},
		{ProviderClaude, "", "default_claude_max_5x", "max_5x"},
		{ProviderClaude, "claude_pro", "default_claude_ai", "pro"},
		{ProviderClaude, "claude_pro", "", "pro"},
		{ProviderClaude, "pro", "", "pro"},
		{ProviderClaude, "claude_team", "default_claude_max_5x", "team"},
		{ProviderClaude, "claude_enterprise", "", "enterprise"},
		{ProviderClaude, "claude_free", "", "free"},
		{ProviderClaude, "", "", ""},
		{ProviderClaude, "claude_weird", "", "weird"},
		{ProviderCodex, "plus", "", "plus"},
		{ProviderCodex, "Pro", "", "pro"},
		{ProviderCodex, "prolite", "", "pro_lite"},
		{ProviderCodex, "promax", "", "pro_max"},
		{ProviderCodex, "team", "", "team"},
		{ProviderCodex, "self_serve_business_usage_based", "", "business"},
		{ProviderCodex, "business", "", "business"},
		{ProviderCodex, "enterprise_cbp_usage_based", "", "enterprise"},
		{ProviderCodex, "ent26", "", "enterprise"},
		{ProviderCodex, "edu_pro", "", "edu"},
		{ProviderCodex, "free_workspace", "", "free"},
		{ProviderCodex, "guest", "", "free"},
		{ProviderCodex, "", "", ""},
		{ProviderCodex, "FutureTier", "", "futuretier"},
		{"openai", "plus", "", ""},
		{"", "plus", "", ""},
	}
	for _, c := range cases {
		if got := NormalizePlan(c.provider, c.a, c.b); got != c.want {
			t.Errorf("NormalizePlan(%q, %q, %q) = %q, want %q", c.provider, c.a, c.b, got, c.want)
		}
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Fable": "fable", "Sonnet 5": "sonnet_5", "Fable 5.1": "fable_5_1", "  --x--  ": "x", "": "", "codex_bengalfox": "codex_bengalfox",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}
