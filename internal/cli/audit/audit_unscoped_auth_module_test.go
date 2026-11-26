package audit

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// reliant's handlers resolve the caller through auth.MustGetUserID(ctx), a
// wrapper in another package of the same module, and the audit — which
// followed calls only inside the handler package — reported all 368 of its
// authenticated rpcs as never resolving the caller.

// seamCall is forge's seam as a wrapper package calls it, whatever it is
// currently named.
var seamCall = codegen.CRUDAuthSeam() + "(ctx)"

func goFile(pkg string, imports []string, body string) string {
	var b strings.Builder
	b.WriteString("package " + pkg + "\n\nimport (\n\t\"context\"\n")
	for _, imp := range imports {
		b.WriteString("\t" + imp + "\n")
	}
	b.WriteString(")\n\n" + body)
	return b.String()
}

func handlerSource(imports []string, handlers ...string) string {
	all := append([]string{`"connectrpc.com/connect"`}, imports...)
	return goFile("shop", all, strings.Join(handlers, ""))
}

func writeSeamPackage(t *testing.T, dir string) {
	t.Helper()
	pkg, fn, _ := strings.Cut(codegen.CRUDAuthSeam(), ".")
	writeFile(t, filepath.Join(dir, "pkg", pkg, pkg+".go"), goFile(pkg, nil,
		"func "+fn+"(ctx context.Context) (any, error) { return nil, nil }\n\n"+
			"// RequireUser wraps the seam inside the seam's own package.\n"+
			"func RequireUser(ctx context.Context) any { c, _ := "+fn+"(ctx); return c }\n"))
}

func TestUnscopedAuth_FollowsWrappersInOtherPackagesOfTheModule(t *testing.T) {
	seamPkg, _, _ := strings.Cut(codegen.CRUDAuthSeam(), ".")
	seamImport := `"example.com/app/pkg/` + seamPkg + `"`

	src := handlerSource([]string{
		`"example.com/app/internal/whoami"`,
		`"example.com/app/internal/lookup"`,
		seamImport,
		`extwhoami "github.com/other/whoami"`,
	},
		handlerCalling("GetOrder", "whoami.Caller(ctx)"),          // one hop
		handlerCalling("GetInvoice", "whoami.Deep(ctx)"),          // two hops
		handlerCalling("GetSession", seamPkg+".RequireUser(ctx)"), // a wrapper in the seam package
		handlerCalling("ListOrders", "lookup.Owner(ctx)"),         // reaches store.GetUser, a row fetch
		handlerCalling("ListInvoices", "extwhoami.Caller(ctx)"),   // same name, outside the module
		delegationBody("GetCart"),                                 // no getter at all
	)
	dir := writeProject(t, "ShopService", map[string]bool{
		"GetOrder": true, "GetInvoice": true, "GetSession": true,
		"ListOrders": true, "ListInvoices": true, "GetCart": true,
	}, src)
	writeSeamPackage(t, dir)
	writeFile(t, filepath.Join(dir, "internal", "whoami", "whoami.go"), goFile("whoami",
		[]string{seamImport, `"example.com/app/internal/session"`},
		"func Caller(ctx context.Context) any { c, _ := "+seamCall+"; return c }\n\n"+
			"func Deep(ctx context.Context) any { return session.User(ctx) }\n"))
	writeFile(t, filepath.Join(dir, "internal", "session", "session.go"), goFile("session",
		[]string{seamImport},
		"func User(ctx context.Context) any { c, _ := "+seamCall+"; return c }\n"))
	// store.GetUser shares the seam's function name but fetches a row; it
	// must not clear a handler from inside a followed package.
	writeFile(t, filepath.Join(dir, "internal", "store", "store.go"), goFile("store", nil,
		"func GetUser(ctx context.Context, id string) any { return nil }\n"))
	writeFile(t, filepath.Join(dir, "internal", "lookup", "lookup.go"), goFile("lookup",
		[]string{`"example.com/app/internal/store"`},
		"func Owner(ctx context.Context) any { return store.GetUser(ctx, \"x\") }\n"))

	got := unscopedMethods(t, auditUnscopedAuth(nil, dir))
	slices.Sort(got)
	want := []string{"GetCart", "ListInvoices", "ListOrders"}
	if !slices.Equal(got, want) {
		t.Fatalf("unscoped = %v, want %v — a same-module wrapper of the seam clears its handler at any hop "+
			"within the bound; a same-named row fetch, a package outside the module, and a bare delegation do not", got, want)
	}
}

func TestUnscopedAuth_WrapperReferencesAreNotCalls(t *testing.T) {
	seamPkg, _, _ := strings.Cut(codegen.CRUDAuthSeam(), ".")
	seamImport := `"example.com/app/pkg/` + seamPkg + `"`
	src := handlerSource([]string{`"example.com/app/internal/whoami"`},
		handlerCalling("GetOrder", "whoami.Caller"),
		handlerCalling("GetInvoice", "consume(whoami.Caller)"),
		handlerCalling("GetCart", "whoami.Client.UserID"),
		handlerCalling("GetSession", "whoami.Indirect(ctx)"),
		handlerCalling("ListOrders", "whoami.Caller(ctx)"),
	)
	dir := writeProject(t, "ShopService", map[string]bool{
		"GetOrder": true, "GetInvoice": true, "GetCart": true, "GetSession": true, "ListOrders": true,
	}, src)
	writeSeamPackage(t, dir)
	writeFile(t, filepath.Join(dir, "internal", "whoami", "whoami.go"), goFile("whoami",
		[]string{seamImport, `"example.com/app/internal/session"`},
		"func Caller(ctx context.Context) any { c, _ := "+seamCall+"; return c }\n"+
			"type Client struct { UserID string }\n"+
			"type Other struct{}\n"+
			"func (Other) Client(ctx context.Context) any { return Caller(ctx) }\n"+
			"func Indirect(ctx context.Context) any { return session.Caller }\n"))
	writeFile(t, filepath.Join(dir, "internal", "session", "session.go"), goFile("session",
		[]string{seamImport}, "func Caller(ctx context.Context) any { c, _ := "+seamCall+"; return c }\n"))

	got := unscopedMethods(t, auditUnscopedAuth(nil, dir))
	slices.Sort(got)
	if want := []string{"GetCart", "GetInvoice", "GetOrder", "GetSession"}; !slices.Equal(got, want) {
		t.Fatalf("unscoped = %v, want %v — only an invoked wrapper resolves the caller", got, want)
	}
}

// A local name that shadows the import alias is not the imported package:
// `whoami.Caller(ctx)` on a local `whoami` value, in a handler or in a
// followed wrapper, never clears a handler.
func TestUnscopedAuth_ShadowedImportAliasIsNotTheWrapper(t *testing.T) {
	seamPkg, _, _ := strings.Cut(codegen.CRUDAuthSeam(), ".")
	seamImport := `"example.com/app/pkg/` + seamPkg + `"`
	shadowed := func(method, decl string) string {
		return "func (s *Service) " + method + "(ctx context.Context, req *connect.Request[pb." + method + "Request]) (*connect.Response[pb." + method + "Response], error) {\n" +
			"\t" + decl + "\n\t_ = whoami.Caller(ctx)\n\treturn nil, nil\n}\n"
	}
	src := handlerSource([]string{`"example.com/app/internal/whoami"`, `"example.com/app/internal/relay"`},
		shadowed("GetOrder", "whoami := s.lookup"),
		shadowed("GetInvoice", "var whoami fakeWhoami"),
		shadowed("GetCart", "for _, whoami := range s.lookups { _ = whoami }"),
		"func (s *Service) GetSession(ctx context.Context, req *connect.Request[pb.GetSessionRequest]) (*connect.Response[pb.GetSessionResponse], error) {\n"+
			"\tf := func(whoami fakeWhoami) any { return whoami.Caller(ctx) }\n\t_ = f\n\treturn nil, nil\n}\n",
		"func (s *Service) GetLine(ctx context.Context, req *connect.Request[pb.GetLineRequest]) (*connect.Response[pb.GetLineResponse], error) {\n"+
			"\tselect {\n\tcase whoami := <-s.lookups:\n\t\t_ = whoami.Caller(ctx)\n\t}\n\treturn nil, nil\n}\n",
		handlerCalling("ListInvoices", "relay.Shadowed(ctx, nil)"),
		handlerCalling("ListOrders", "whoami.Caller(ctx)"),
	)
	dir := writeProject(t, "ShopService", map[string]bool{
		"GetOrder": true, "GetInvoice": true, "GetCart": true, "GetSession": true, "GetLine": true, "ListInvoices": true, "ListOrders": true,
	}, src)
	writeSeamPackage(t, dir)
	writeFile(t, filepath.Join(dir, "internal", "whoami", "whoami.go"), goFile("whoami",
		[]string{seamImport}, "func Caller(ctx context.Context) any { c, _ := "+seamCall+"; return c }\n"))
	writeFile(t, filepath.Join(dir, "internal", "relay", "relay.go"), goFile("relay",
		[]string{`"example.com/app/internal/whoami"`},
		"func Shadowed(ctx context.Context, whoami fakeWhoami) any { return whoami.Caller(ctx) }\n"))

	got := unscopedMethods(t, auditUnscopedAuth(nil, dir))
	slices.Sort(got)
	if want := []string{"GetCart", "GetInvoice", "GetLine", "GetOrder", "GetSession", "ListInvoices"}; !slices.Equal(got, want) {
		t.Fatalf("unscoped = %v, want %v — only the imported whoami resolves the caller, never a local value of that name", got, want)
	}
}

// The walk is bounded: a chain longer than maxWrapperHops packages reports
// unscoped (mark the resolver instead), and an import cycle — which does not
// build, but can sit on disk — terminates.
func TestUnscopedAuth_WrapperWalkIsBoundedAndCycleSafe(t *testing.T) {
	seamPkg, _, _ := strings.Cut(codegen.CRUDAuthSeam(), ".")
	seamImport := `"example.com/app/pkg/` + seamPkg + `"`

	chain := func(dir, prefix string, n int) {
		for i := 1; i <= n; i++ {
			name := fmt.Sprintf("%s%d", prefix, i)
			var imports []string
			var call string
			if i == n {
				imports, call = []string{seamImport}, seamCall
			} else {
				next := fmt.Sprintf("%s%d", prefix, i+1)
				imports, call = []string{`"example.com/app/internal/` + next + `"`}, next+".F(ctx)"
			}
			writeFile(t, filepath.Join(dir, "internal", name, name+".go"), goFile(name, imports,
				"func F(ctx context.Context) any { return "+call+" }\n"))
		}
	}

	src := handlerSource([]string{
		`"example.com/app/internal/ok1"`,
		`"example.com/app/internal/far1"`,
		`"example.com/app/internal/cyca"`,
	},
		handlerCalling("GetOrder", "ok1.F(ctx)"),
		handlerCalling("GetInvoice", "far1.F(ctx)"),
		handlerCalling("GetCart", "cyca.F(ctx)"),
	)
	dir := writeProject(t, "ShopService", map[string]bool{"GetOrder": true, "GetInvoice": true, "GetCart": true}, src)
	writeSeamPackage(t, dir)
	chain(dir, "ok", maxWrapperHops)    // the last package of the bound calls the seam
	chain(dir, "far", maxWrapperHops+1) // one package past it
	writeFile(t, filepath.Join(dir, "internal", "cyca", "a.go"), goFile("cyca",
		[]string{`"example.com/app/internal/cycb"`}, "func F(ctx context.Context) any { return cycb.F(ctx) }\n"))
	writeFile(t, filepath.Join(dir, "internal", "cycb", "b.go"), goFile("cycb",
		[]string{`"example.com/app/internal/cyca"`}, "func F(ctx context.Context) any { return cyca.F(ctx) }\n"))

	got := unscopedMethods(t, auditUnscopedAuth(nil, dir))
	slices.Sort(got)
	if want := []string{"GetCart", "GetInvoice"}; !slices.Equal(got, want) {
		t.Fatalf("unscoped = %v, want %v", got, want)
	}
}

// lint.auth.caller_resolvers declares a resolver the //forge:resolves-caller
// marker cannot reach: one in a dependency, or a package left unannotated.
func TestUnscopedAuth_HonorsConfiguredCallerResolvers(t *testing.T) {
	src := handlerSource([]string{
		`"github.com/acme/authlib/v2"`,
		`"example.com/app/internal/ids"`,
		`"example.com/app/internal/me"`,
	},
		handlerCalling("GetOrder", "authlib.UserID(ctx)"),     // configured, outside the module
		handlerCalling("GetInvoice", "identity.Current(ctx)"), // configured, package clause differs from the dir
		handlerCalling("GetSession", "me.ID(ctx)"),            // a module wrapper of a configured resolver
		handlerCalling("ListOrders", "authlib.Other(ctx)"),    // not configured
		delegationBody("GetCart"),
	)
	dir := writeProject(t, "ShopService", map[string]bool{
		"GetOrder": true, "GetInvoice": true, "GetSession": true, "ListOrders": true, "GetCart": true,
	}, src)
	writeFile(t, filepath.Join(dir, "internal", "ids", "ids.go"), goFile("identity", nil,
		"func Current(ctx context.Context) string { return \"\" }\n"))
	writeFile(t, filepath.Join(dir, "internal", "me", "me.go"), goFile("me",
		[]string{`"github.com/acme/authlib/v2"`}, "func ID(ctx context.Context) string { return authlib.UserID(ctx) }\n"))

	cfg := &config.ProjectConfig{Lint: config.LintConfig{Auth: config.AuthLintConfig{CallerResolvers: []config.CallerResolverConfig{
		{Call: "github.com/acme/authlib/v2.UserID"},
		{Call: "example.com/app/internal/ids.Current"},
		{Call: "nodot"},
		{Call: "example.com/app/internal/ids.not-an-identifier"},
	}}}}
	cat := auditUnscopedAuth(cfg, dir)

	got := unscopedMethods(t, cat)
	slices.Sort(got)
	if want := []string{"GetCart", "ListOrders"}; !slices.Equal(got, want) {
		t.Fatalf("unscoped = %v, want %v — a configured resolver, and a wrapper of one, clear their handler", got, want)
	}
	resolvers, _ := cat.Details["caller_resolvers"].([]string)
	if want := []string{"example.com/app/internal/ids.Current", "github.com/acme/authlib/v2.UserID"}; !slices.Equal(sortedCopy(resolvers), want) {
		t.Errorf("caller_resolvers = %v, want the configured functions %v", resolvers, want)
	}
	invalid, _ := cat.Details["caller_resolvers_config_invalid"].([]invalidResolverConfig)
	if len(invalid) != 2 || invalid[0].Call != "nodot" || invalid[1].Call != "example.com/app/internal/ids.not-an-identifier" {
		t.Errorf("caller_resolvers_config_invalid = %+v, want the two malformed entries", cat.Details["caller_resolvers_config_invalid"])
	}
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// A generated mock in the handler package (mock_gen.go, a mock of the
// service's own dependency) can declare a method with an rpc's name. It sorts
// after the handler's file, and it used to replace the handler as the method
// judged — so a handler that resolved the caller was reported unscoped,
// pointing at mock_gen.go.
func TestUnscopedAuth_JudgesTheHandlerNotAMockSharingItsName(t *testing.T) {
	dir := writeProject(t, "ShopService", map[string]bool{"GetOrder": true}, handlerHeader+scopedBody("GetOrder"))
	writeFile(t, filepath.Join(dir, "internal", "handlers", "shop", "mock_gen.go"),
		"package shop\n\nimport \"context\"\n\ntype MockRepository struct{}\n\n"+
			"func (m *MockRepository) GetOrder(ctx context.Context, id string) error { return nil }\n")

	if got := unscopedMethods(t, auditUnscopedAuth(nil, dir)); len(got) != 0 {
		t.Fatalf("unscoped = %v, want none — the mock's GetOrder is not the handler", got)
	}
}
