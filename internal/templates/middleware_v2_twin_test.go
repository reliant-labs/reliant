package templates

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// A connect-go v2 project's auth-policy pair is its own pair of files
// (middleware_v2.go.tmpl, middleware_test_v2.go), not a branch inside the v1
// ones: both stay plain Go before rendering, which forge's own checks read.
// What keeps the twins from drifting is this test — the v2 middleware.go is
// the v1 one with exactly the connect-go v2 substitutions below, so a policy
// change made to one and not the other fails here.
func TestMiddlewareV2TwinIsTheV1FileOnV2(t *testing.T) {
	v1, err := ProjectTemplates().Get("middleware.go.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := ProjectTemplates().Get("middleware_v2.go.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	back := string(v2)
	for _, s := range []struct{ v2, v1 string }{
		{"// connect-go v2: NewAuthInterceptorV2 returns a connect.ServerInterceptor of connectrpc.com/connect/v2.\n", ""},
		{"\tconnect \"connectrpc.com/connect/v2\"\n", "\t\"connectrpc.com/connect\"\n"},
		{"// into NewAuthInterceptorV2, so", "// into NewAuthInterceptor, so"},
		{"// NewAuthInterceptorV2 resolves this file's policy into the forge authn\n// interceptor (connect-go v2) from", "// NewAuthInterceptor resolves this file's policy into the forge authn\n// interceptor from"},
		{"//\n// It stashes the claims the forge way (authn.ContextWithClaims), so the\n// server chain reads the caller back with ClaimsFromContext\n// (serverkit.AuthWithCallerV2).\nfunc NewAuthInterceptorV2(deps AuthDeps) (connect.ServerInterceptor, error) {", "func NewAuthInterceptor(deps AuthDeps) (connect.Interceptor, error) {"},
		{"authn.NewInterceptorV2(authn.Policy{", "authn.NewInterceptor(authn.Policy{"},
	} {
		if strings.Count(back, s.v2) != 1 {
			t.Fatalf("middleware_v2.go.tmpl no longer holds exactly one %q", s.v2)
		}
		back = strings.Replace(back, s.v2, s.v1, 1)
	}
	if back != string(v1) {
		t.Error("middleware_v2.go.tmpl and middleware.go.tmpl differ beyond the connect-go v2 substitutions; make the same policy change in both")
	}

	// Both twins of both files parse as Go before rendering, and declare
	// the package's GetUser.
	for _, name := range []string{"middleware.go.tmpl", "middleware_v2.go.tmpl", "middleware_test.go", "middleware_test_v2.go"} {
		src, err := ProjectTemplates().Get(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
		if err != nil {
			t.Errorf("%s does not parse as Go: %v", name, err)
			continue
		}
		if f.Name.Name != "middleware" {
			t.Errorf("%s is package %s", name, f.Name.Name)
		}
		if strings.HasSuffix(name, ".tmpl") && !strings.Contains(string(src), "\nfunc GetUser(ctx context.Context) (*Claims, error)") {
			t.Errorf("%s does not declare GetUser", name)
		}
	}
}
