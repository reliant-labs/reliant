package audit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
)

// A project with its own auth package — reliant, whose interceptor predates
// forge — resolves the caller through its own accessor, not forge's
// middleware.GetUser. Before //forge:resolves-caller the audit could not be
// told so, and reported every one of its authenticated rpcs as never
// resolving the caller (P3b friction F18).

const authPkg = `package auth

import "context"

type key struct{}

// GetUserIDFromContext returns the signed-in user's id.
//
//forge:resolves-caller
func GetUserIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(key{}).(string)
	return id, ok
}

// MustGetUserID is NOT declared: wrapping a resolver does not make one.
func MustGetUserID(ctx context.Context) string {
	id, _ := GetUserIDFromContext(ctx)
	return id
}

// Session is a method: a call site cannot name its receiver's type.
type Session struct{}

// forge:resolves-caller
func (Session) UserID(ctx context.Context) string { return "" }
`

// A same-named function nobody declared. Matching by name alone would clear
// a handler calling it.
const otherPkg = `package other

import "context"

func GetUserIDFromContext(ctx context.Context) (string, bool) { return "", false }
`

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func handlerCalling(method, call string) string {
	return "func (s *Service) " + method + "(ctx context.Context, req *connect.Request[pb." + method + "Request]) (*connect.Response[pb." + method + "Response], error) {\n" +
		"\t_ = " + call + "\n\treturn nil, nil\n}\n"
}

func TestUnscopedAuth_RecognisesADeclaredCallerResolver(t *testing.T) {
	src := "package shop\n\nimport (\n\t\"context\"\n\t\"connectrpc.com/connect\"\n" +
		"\t\"example.com/app/internal/auth\"\n" +
		"\taliased \"example.com/app/internal/auth\"\n" +
		"\t\"example.com/app/internal/other\"\n)\n\n" +
		handlerCalling("GetOrder", "auth.GetUserIDFromContext") +
		handlerCalling("GetInvoice", "aliased.GetUserIDFromContext") +
		handlerCalling("ListOrders", "auth.MustGetUserID(ctx)") +
		handlerCalling("ListInvoices", "other.GetUserIDFromContext") +
		handlerCalling("GetSession", "auth.Session{}.UserID(ctx)")
	dir := writeProject(t, "ShopService", map[string]bool{
		"GetOrder": true, "GetInvoice": true, "ListOrders": true, "ListInvoices": true, "GetSession": true,
	}, src)
	writeFile(t, filepath.Join(dir, "internal", "auth", "context.go"), authPkg)
	writeFile(t, filepath.Join(dir, "internal", "other", "other.go"), otherPkg)

	cat := auditUnscopedAuth(nil, dir)
	got := unscopedMethods(t, cat)
	slices.Sort(got)
	want := []string{"GetSession", "ListInvoices", "ListOrders"}
	if !slices.Equal(got, want) {
		t.Fatalf("unscoped = %v, want %v — a declared resolver (plain or aliased import) clears its handler; "+
			"an undeclared wrapper, a same-named function in another package, and a method do not", got, want)
	}

	resolvers, _ := cat.Details["caller_resolvers"].([]string)
	if !slices.Equal(resolvers, []string{"example.com/app/internal/auth.GetUserIDFromContext"}) {
		t.Errorf("caller_resolvers = %v, want the one declared function", cat.Details["caller_resolvers"])
	}
	if hint, _ := cat.Details["hint"].(string); !strings.Contains(hint, "auth.GetUserIDFromContext(ctx)") {
		t.Errorf("the hint must name the project's own resolver, not forge's seam: %q", hint)
	}
	ignored, _ := cat.Details["caller_resolver_markers_ignored"].([]codegen.IgnoredResolverMarker)
	if len(ignored) != 1 || ignored[0].File != "internal/auth/context.go" {
		t.Errorf("the marker on a method must be reported as ignored, got %+v", cat.Details["caller_resolver_markers_ignored"])
	}
}

// A resolver declared inside the handler package is called by its bare
// name, and seeds the package's fixed point like a call to the seam.
func TestUnscopedAuth_RecognisesAResolverDeclaredInTheHandlerPackage(t *testing.T) {
	src := handlerHeader +
		"//forge:resolves-caller\nfunc callerID(ctx context.Context) string { return \"\" }\n\n" +
		"func scoped(ctx context.Context) string { return callerID(ctx) }\n\n" +
		handlerCalling("GetOrder", "callerID(ctx)") +
		handlerCalling("ListOrders", "scoped(ctx)") +
		delegationBody("GetInvoice")
	dir := writeProject(t, "ShopService", map[string]bool{"GetOrder": true, "ListOrders": true, "GetInvoice": true}, src)

	got := unscopedMethods(t, auditUnscopedAuth(nil, dir))
	if !slices.Equal(got, []string{"GetInvoice"}) {
		t.Fatalf("unscoped = %v, want only GetInvoice", got)
	}
}
