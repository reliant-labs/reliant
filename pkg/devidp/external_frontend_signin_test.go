package devidp_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/reliant-labs/forge/pkg/devidp"
	"github.com/reliant-labs/forge/pkg/middleware"
)

// The frontend-elsewhere recipe (skill frontend/runtime-config), end to end at
// the API's edge: a frontend at https://app.example.com signs in through an API
// at https://api.example.com, composed as a deployed API is (credentialed CORS
// from CORS_ORIGINS + CORS_ALLOW_CREDENTIALS around the sign-in routes, which
// sit behind the origin guard trusting idp_login_uri's origin), with a fake
// broker that issues a session cookie and a session read that requires it.
func TestExternalFrontendSignsInThroughTheRecipe(t *testing.T) {
	const app, api = "https://app.example.com", "https://api.example.com"
	edge := func(trustedLogin string) http.Handler {
		mux := http.NewServeMux()
		g := devidp.GuardMux(mux, trustedLogin) // idp_login_uri
		g.Handle("POST /auth/login", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "s1", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
			w.WriteHeader(http.StatusOK)
		}))
		mux.Handle("GET /auth/session", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie("session"); err != nil || c.Value != "s1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		return middleware.CORSMiddleware([]string{app}, true)(mux) // CORS_ORIGINS, CORS_ALLOW_CREDENTIALS
	}
	do := func(h http.Handler, method, path, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, api+path, nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Sec-Fetch-Site", "same-site") // app. and api.example.com share a site
		if method == http.MethodOptions {
			req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	credentialed := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Header().Get("Access-Control-Allow-Origin") != app || rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
			t.Fatalf("not a credentialed CORS answer for %s: %v", app, rec.Header())
		}
	}

	h := edge(app + "/auth/sign-in")
	pre := do(h, http.MethodOptions, "/auth/login", app, nil)
	credentialed(t, pre)
	login := do(h, http.MethodPost, "/auth/login", app, nil)
	if login.Code != http.StatusOK {
		t.Fatalf("sign-in from the declared frontend = %d, want 200", login.Code)
	}
	credentialed(t, login)
	cookies := login.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("sign-in set %d cookies, want the session", len(cookies))
	}
	if s := do(h, http.MethodGet, "/auth/session", app, cookies[0]); s.Code != http.StatusOK {
		t.Fatalf("the session the sign-in issued is not usable from the frontend: %d", s.Code)
	} else {
		credentialed(t, s)
	}

	// Skip the recipe's sign-in-origin step (idp_login_uri still names the
	// API): the browser's sign-in POST is refused, however CORS is set.
	if got := do(edge(api+"/auth/sign-in"), http.MethodPost, "/auth/login", app, nil).Code; got != http.StatusForbidden {
		t.Fatalf("sign-in without the trusted login origin = %d, want 403", got)
	}
	// An undeclared sibling origin is refused by the guard and gets no CORS grant.
	evil := do(h, http.MethodPost, "/auth/login", "https://evil.example.com", nil)
	if evil.Code != http.StatusForbidden || evil.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("an undeclared origin = %d %v, want 403 with no CORS grant", evil.Code, evil.Header())
	}
}
