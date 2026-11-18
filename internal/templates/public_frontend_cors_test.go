package templates

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/middleware"
)

// TestPublicFrontendTransportNeedsCredentialedCORS pins the browser side of a
// frontend with no sign-in (`forge project new --auth-mode none`) calling its
// API on another origin — a deployed API_URL, or dev with
// FORGE_DEV_API_PROXY=off.
//
// Both scaffolded transports fetch with credentials "include" whether or not
// anyone signs in. For such a request the browser accepts a cross-origin
// response only if Access-Control-Allow-Origin echoes the page's origin
// exactly AND Access-Control-Allow-Credentials is "true" (Fetch standard,
// CORS check): an auth_required: false rpc is refused like any other when
// the API does not allow credentials. So the dev env renders
// CORS_ALLOW_CREDENTIALS for every frontend, users or not, and this test
// replays the browser's check against forge's CORS middleware with the
// policy that env sets, for the preflight and the rpc itself.
func TestPublicFrontendTransportNeedsCredentialedCORS(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"frontend/nextjs/src/lib/connect.ts.tmpl",
		"frontend/vite-spa/src/lib/connect.ts.tmpl",
	} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// If a transport stops sending credentials when no one signs in,
		// this test's premise is gone: revisit the dev env's gate instead
		// of deleting the assertion.
		if !strings.Contains(string(src), `credentials: "include"`) {
			t.Fatalf("%s no longer fetches with credentials \"include\"; the dev env's CORS_ALLOW_CREDENTIALS gate (kcl/dev/main.k.tmpl) assumes it does", path)
		}
	}

	// The dev env the public frontend renders allows credentials.
	out, err := DeployTemplates().Render("kcl/dev/main.k.tmpl", EnvTemplateData{
		ProjectName: "shop", EnvName: "dev", Roots: true,
		HasFrontend: true, FrontendName: "web", NoIdentity: true,
		Bindings: "        _on_host(wl.api)",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	allowCredentials := strings.Contains(string(out), `CORS_ALLOW_CREDENTIALS = "true"`)
	if !allowCredentials {
		t.Error("the dev env of a frontend with no users does not allow credentialed CORS")
	}

	const site = "http://localhost:3000"
	rpc := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})
	browserAccepts := func(policy bool, req *http.Request) bool {
		rec := httptest.NewRecorder()
		middleware.CORSMiddleware([]string{site}, policy)(rpc).ServeHTTP(rec, req)
		h := rec.Result().Header
		return h.Get("Access-Control-Allow-Origin") == site && h.Get("Access-Control-Allow-Credentials") == "true"
	}
	call := func() []*http.Request {
		preflight := httptest.NewRequest(http.MethodOptions, "http://localhost:8080/shop.v1.Public/Get", nil)
		preflight.Header.Set("Origin", site)
		preflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
		preflight.Header.Set("Access-Control-Request-Headers", "content-type,connect-protocol-version")
		post := httptest.NewRequest(http.MethodPost, "http://localhost:8080/shop.v1.Public/Get", strings.NewReader(`{}`))
		post.Header.Set("Origin", site)
		post.Header.Set("Content-Type", "application/json")
		return []*http.Request{preflight, post}
	}
	for _, req := range call() {
		if !browserAccepts(allowCredentials, req) {
			t.Errorf("%s %s: a browser refuses the public rpc's credentialed cross-origin response under the dev env's CORS policy", req.Method, req.URL.Path)
		}
	}
	// The check above can fail: without credentials allowed (the regression
	// this pins) the browser refuses the same calls.
	for _, req := range call() {
		if browserAccepts(false, req) {
			t.Fatalf("%s: the browser's credentialed CORS check passed without Access-Control-Allow-Credentials; the test checks nothing", req.Method)
		}
	}
}
