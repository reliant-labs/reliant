package serverkit_test

// Development is the second way CORS turns on.
//
// The regression these pin: serverkit gated the CORS layer solely on a
// non-empty Config.CORSOrigins. A freshly scaffolded project has no
// CORS_ORIGINS value — nothing defaults it — so the generated factory's
// `if development { DevCORSMiddleware }` branch was unreachable, and the
// scaffolded browser frontend could not call its own backend until someone
// hand-curated an allow-list. Meanwhile the ENVIRONMENT flag's own help text
// advertised "relaxed CORS" in development.
//
// The pair that matters: dev with no origins ENFORCES CORS (so the caller's
// dev policy runs), and every deployed environment with no origins stays
// exactly as closed as it was.

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// TestConfig_CORSEnabled is the truth table for the gate. The near-miss
// rows are the point: only the exact "development" string unlocks the
// permissive posture, so a typo or an unset variable fails closed.
func TestConfig_CORSEnabled(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		env     string
		origins []string
		want    bool
		why     string
	}{
		{"development, no origins", "development", nil, true,
			"the fresh-scaffold case: dev must enforce CORS so the caller's dev policy runs"},
		{"development, origins named", "development", []string{"https://example.com"}, true, ""},
		{"production, origins named", "production", []string{"https://example.com"}, true,
			"an operator naming origins wants them enforced in every environment"},
		{"production, no origins", "production", nil, false,
			"DEV-ONLY: a deployed environment with no origins gets no CORS layer"},
		{"staging, no origins", "staging", nil, false,
			"DEV-ONLY: staging must not inherit the dev relaxation"},
		{"environment unset, no origins", "", nil, false,
			"forgetting to set ENVIRONMENT must not open anything"},
		{"dev abbreviation, no origins", "dev", nil, false,
			"near-miss: only the exact value unlocks the permissive posture"},
		{"capitalized Development, no origins", "Development", nil, false,
			"near-miss: the comparison is exact, not case-folded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			cfg := serverkit.Config{Environment: c.env, CORSOrigins: c.origins}
			if got := cfg.CORSEnabled(); got != c.want {
				t.Errorf("CORSEnabled() = %v, want %v — %s", got, c.want, c.why)
			}
			if got := cfg.IsDevelopment(); got != (c.env == serverkit.EnvDevelopment) {
				t.Errorf("IsDevelopment() = %v for Environment=%q", got, c.env)
			}
		})
	}
}

// markerCORSFactory returns a CORS middleware factory that stamps a header
// on everything it wraps, plus the counter recording how many times the
// factory itself was invoked. Counting the FACTORY (not the requests) is
// what proves the deployed-environment case never reaches the CORS layer
// at all, rather than reaching it and matching nothing.
func markerCORSFactory(header string) (func([]string, bool) func(http.Handler) http.Handler, *atomic.Int32) {
	var calls atomic.Int32
	factory := func(_ []string, _ bool) func(http.Handler) http.Handler {
		calls.Add(1)
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(header, "1")
				next.ServeHTTP(w, r)
			})
		}
	}
	return factory, &calls
}

// TestRun_DevelopmentEnablesCORSWithoutOrigins is the headline regression:
// Environment=development and an EMPTY CORSOrigins must still wrap the
// handler in the CORS layer. Before the fix the factory was never invoked
// and the response carried no CORS header, which is exactly what blocked a
// scaffolded frontend from calling its own backend in a browser.
func TestRun_DevelopmentEnablesCORSWithoutOrigins(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — shutdownAndWait sends SIGTERM to the test process.
	addr := freeAddr(t)

	const marker = "X-Cors-Applied"
	factory, calls := markerCORSFactory(marker)

	inner := http.NewServeMux()
	inner.HandleFunc("/app", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	cfg := baseConfig(addr)
	cfg.Environment = serverkit.EnvDevelopment
	// CORSOrigins deliberately left EMPTY — the fresh-scaffold state.
	srv := serverkit.Server{Handler: inner, CORSMiddleware: factory}
	errCh, _ := runInBackground(t, cfg, srv)
	waitReady(t, addr, 2*time.Second)

	resp, err := http.Get("http://" + addr + "/app")
	if err != nil {
		t.Fatalf("GET /app: %v", err)
	}
	_ = resp.Body.Close()

	if calls.Load() == 0 {
		t.Error("CORSMiddleware factory was never invoked in development with an empty CORSOrigins — " +
			"the generated dev-CORS branch is unreachable and a scaffolded frontend cannot call its own backend")
	}
	if resp.Header.Get(marker) == "" {
		t.Errorf("/app response carries no %s header — the CORS layer did not wrap the handler", marker)
	}

	if err := shutdownAndWait(t, errCh, 5*time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

// TestRun_DeployedEnvWithoutOriginsHasNoCORS is the other half of the
// contract, and the one that keeps the fix honest: the relaxation is DEV
// ONLY. A staging/production Config with no origins must not reach the
// CORS layer at all.
func TestRun_DeployedEnvWithoutOriginsHasNoCORS(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — shutdownAndWait sends SIGTERM to the test process.
	addr := freeAddr(t)

	const marker = "X-Cors-Applied"
	factory, calls := markerCORSFactory(marker)

	inner := http.NewServeMux()
	inner.HandleFunc("/app", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	cfg := baseConfig(addr)
	cfg.Environment = "production"
	srv := serverkit.Server{Handler: inner, CORSMiddleware: factory}
	errCh, _ := runInBackground(t, cfg, srv)
	waitReady(t, addr, 2*time.Second)

	resp, err := http.Get("http://" + addr + "/app")
	if err != nil {
		t.Fatalf("GET /app: %v", err)
	}
	_ = resp.Body.Close()

	if calls.Load() != 0 {
		t.Error("CORSMiddleware factory was invoked in production with an empty CORSOrigins — " +
			"the development relaxation leaked into a deployed environment")
	}
	if got := resp.Header.Get(marker); got != "" {
		t.Errorf("/app response carries %s=%q in production — CORS must stay closed by default", marker, got)
	}

	if err := shutdownAndWait(t, errCh, 5*time.Second); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

// TestRun_DevelopmentCORSWithoutFactoryIsBootError extends the existing
// fail-closed contract to the new arm of the gate. Enabling a security
// layer the composition root forgot to wire must be a loud boot error, not
// a silent unenforced serve — and that has to hold for the arm that turns
// on WITHOUT the operator setting anything.
func TestRun_DevelopmentCORSWithoutFactoryIsBootError(t *testing.T) {
	t.Parallel()
	cfg := baseConfig(":0")
	cfg.Environment = serverkit.EnvDevelopment
	// CORSOrigins empty AND CORSMiddleware nil.
	//
	// The context is bounded on purpose. A fail-closed check must reject
	// BEFORE the listener binds, so the correct behaviour returns
	// immediately and never observes the deadline. If the check regresses,
	// Run boots and serves — and an unbounded context would wedge the
	// suite instead of reporting a failure.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := serverkit.Run(ctx, cfg, serverkit.Server{Handler: emptyHandler()})
	if err == nil || !contains(err.Error(), "CORSMiddleware is nil") {
		t.Fatalf("expected fail-closed CORS boot error in development, got %v — "+
			"development enables CORS with no origins, so an unwired factory must be rejected", err)
	}
}
