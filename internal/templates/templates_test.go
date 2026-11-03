package templates

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

func TestRenderTemplate_StripsBuildIgnoreFromRenderedOutput(t *testing.T) {
	content, err := WebhookTemplates().Render("webhook_routes_gen.go.tmpl", map[string]any{
		"Package":  "tasks",
		"Webhooks": []map[string]any{{"Name": "github", "PascalName": "Github"}},
	})
	if err != nil {
		t.Fatalf("WebhookTemplates().Render() error = %v", err)
	}

	rendered := string(content)
	if strings.HasPrefix(rendered, "//go:build ignore") {
		t.Fatal("rendered template should not retain //go:build ignore header")
	}
	if !strings.Contains(rendered, "func (s *Service) RegisterWebhookRoutes") {
		t.Fatal("rendered template should include webhook route registration")
	}
}

// TestComponentTestHelpersTemplate_MinimalService verifies that
// component_test_helpers.go.tmpl renders valid Go for the leanest component
// shape: a service with no DB, no migrations, no auto-stubs, no func seams.
//
// This replaces TestBootstrapTestingTemplate_ZeroServices, which pinned the
// zero-COMPONENT state of the retired single-file harness (where every
// helper, and the `testing` import itself, had to be gated on "are there any
// components at all"). One file per component makes that state
// unrepresentable: a component's helper file exists only because the
// component does, so the file always has exactly one component to serve.
func TestComponentTestHelpersTemplate_MinimalService(t *testing.T) {
	data := struct {
		Module    string
		Package   string
		Name      string
		FieldName string
		IsService bool

		ConstructorName   string
		Fallible          bool
		HasDB             bool
		HasLogger         bool
		ConfigImport      string
		ConfigImportAlias string
		HasMigrationsFS   bool
		NeedsTime         bool
		NeedsULID         bool

		ConnectServiceGoName   string
		ProtoConnectImportPath string
		ProtoConnectPkg        string
		MountMethod            string

		// The template ranges over these; declare them so the struct-field
		// lookup succeeds. Element types only need the fields the template
		// reads.
		AutoStubs       []struct{ FieldName, StubType, InterfaceQualified string }
		AutoMocks       []struct{ FieldName, MockType, Interface string }
		UnresolvedStubs []struct{ FieldName, TypeExpr string }
		FuncDefaults    []struct {
			FieldName, Expr      string
			NeedsTime, NeedsULID bool
		}
		FuncTodos    []struct{ FieldName, TypeExpr string }
		UnfilledDeps []struct{ FieldName, TypeExpr string }
		ExtraImports []struct{ Alias, Path string }
	}{
		Module:                 "example.com/myproject",
		Package:                "order",
		Name:                   "order",
		FieldName:              "Order",
		IsService:              true,
		ConstructorName:        "New",
		ConnectServiceGoName:   "OrderService",
		ProtoConnectImportPath: "example.com/myproject/gen/services/order/v1/orderv1connect",
		ProtoConnectPkg:        "orderv1connect",
		MountMethod:            "Register",
	}

	content, err := ProjectTemplates().Render("component_test_helpers.go.tmpl", data)
	if err != nil {
		t.Fatalf("Render component_test_helpers.go.tmpl: %v", err)
	}
	rendered := string(content)

	// The package clause is the COMPONENT's own, not <pkg>_test: Go compiles
	// in-package _test.go files INTO the package, which is what lets both
	// internal and external test files in this dir use these helpers.
	if !strings.Contains(rendered, "package order\n") {
		t.Fatalf("helpers must be in `package order`, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "type TestOption func") {
		t.Fatal("missing TestOption type")
	}
	if !strings.Contains(rendered, "func NewTestOrder(") {
		t.Fatal("missing NewTestOrder factory")
	}
	// Unlike the retired shared file, `testing` is ALWAYS imported here —
	// this file only exists for a component that has a factory, and the file
	// is a _test.go so the import never reaches the production binary.
	if !strings.Contains(rendered, `"testing"`) {
		t.Fatal("a component helper file must import testing")
	}
	// Without a DB there must be no orm import and no db field.
	if strings.Contains(rendered, "orm.Context") {
		t.Fatalf("no-DB component must not reference orm.Context:\n%s", rendered)
	}

	// The slog gating bug (journey fr-994db53964): the body uses
	// *slog.Logger unconditionally, so the import must always be present.
	if strings.Contains(rendered, "slog.") && !strings.Contains(rendered, `"log/slog"`) {
		t.Fatalf("rendered helpers reference slog without importing log/slog:\n%s", rendered)
	}

	fset := token.NewFileSet()
	if _, parseErr := parser.ParseFile(fset, "helpers_gen_test.go", rendered, parser.AllErrors); parseErr != nil {
		t.Fatalf("rendered helpers do not parse as valid Go:\n%v\n\nSource:\n%s", parseErr, rendered)
	}

	// Belt-and-braces for the same class of bug on every qualifier the
	// template can emit: any package qualifier used in the body must
	// have a matching import.
	assertQualifiersImported(t, rendered)
}

// assertQualifiersImported parses a rendered Go file and asserts that
// every known package qualifier referenced in the body has a matching
// import path. The qualifier→path table covers the packages
// bootstrap_testing.go.tmpl can emit; extend it when the template grows
// a new import.
func assertQualifiersImported(t *testing.T, src string) {
	t.Helper()
	qualifierImports := map[string]string{
		"slog":     "log/slog",
		"testing":  "testing",
		"context":  "context",
		"http":     "net/http",
		"httptest": "net/http/httptest",
		"connect":  "connectrpc.com/connect",
		"orm":      "github.com/reliant-labs/forge/pkg/orm",
		"testkit":  "github.com/reliant-labs/forge/pkg/testkit",
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "testing.go", src, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	imported := map[string]bool{}
	for _, imp := range f.Imports {
		imported[strings.Trim(imp.Path.Value, `"`)] = true
	}
	// Match the qualifier as a whole identifier. A plain
	// strings.Contains(src, qual+".") reports `orderv1connect.NewFooClient`
	// as a reference to the `connect` package — a false positive that would
	// demand an import the file neither has nor needs.
	for qual, path := range qualifierImports {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(qual) + `\.`)
		for _, m := range re.FindAllStringIndex(src, -1) {
			// \b treats '1' as a word char, so `orderv1connect.` does not
			// match — but a leading identifier rune still has to be ruled
			// out for qualifiers that end at a non-word boundary.
			if m[0] > 0 {
				prev := rune(src[m[0]-1])
				if prev == '_' || prev == '.' {
					continue
				}
			}
			if !imported[path] {
				t.Errorf("rendered file references %s.* without importing %q", qual, path)
			}
			break
		}
	}
}

// TestDBReadme_HyphenatedPackageIsSnakeCased covers the same stripe-latent
// bug in the markdown README example, which is rendered into proto/db/README.md
// at scaffold time (so consumers may copy-paste it into a real proto file).
// TestDockerfile_NeverCopiesForgePkg pins the post-vendoring-removal
// invariant: the scaffolded Dockerfile NEVER carries a `COPY .forge-pkg`
// line. forge/pkg is a published module resolved by `go mod download` from
// the pinned require; there is no vendored directory to copy into the build
// context.
func TestDockerfile_NeverCopiesForgePkg(t *testing.T) {
	data := struct {
		Name                   string
		ProtoName              string
		Module                 string
		HasDatabase            bool
		DatabaseDriver         string
		OrmEnabled             bool
		ServiceName            string
		ServicePort            int
		ProjectName            string
		FrontendName           string
		FrontendPort           int
		GoVersion              string
		GoVersionMinor         string
		DockerBuilderGoVersion string
		VersionVar             string
		Binaries               []struct {
			Dir     string
			Primary bool
		}
	}{
		Name: "demo", ProtoName: "demo", Module: "github.com/example/demo",
		ServiceName: "api", ServicePort: 8080, ProjectName: "demo",
		GoVersion: "1.26", GoVersionMinor: "26", DockerBuilderGoVersion: "1.26",
		Binaries: []struct {
			Dir     string
			Primary bool
		}{{Dir: "demo", Primary: true}},
	}
	out, err := ProjectTemplates().Render("Dockerfile.tmpl", data)
	if err != nil {
		t.Fatalf("render Dockerfile.tmpl: %v", err)
	}
	rendered := string(out)
	if strings.Contains(rendered, ".forge-pkg") {
		t.Errorf("Dockerfile must never reference .forge-pkg, got:\n%s", rendered)
	}
	// Sanity: it still downloads modules the normal way.
	if !strings.Contains(rendered, "RUN go mod download") {
		t.Errorf("Dockerfile lost `RUN go mod download`, got:\n%s", rendered)
	}
}

// TestDockerfile_VersionVarLdflags verifies the Dockerfile's build-stage
// ldflags emit the extra `-X <path>=${FORGE_VERSION}` when build.version_var
// is set, and omit it (leaving the canonical main.version/commit/date
// stamping) when it is empty. Also pins that the in-container `git describe`
// is gone — replaced by the FORGE_VERSION build-arg.
func TestDockerfile_VersionVarLdflags(t *testing.T) {
	type tc struct {
		name        string
		versionVar  string
		wantContain string
		wantAbsent  string
	}
	cases := []tc{
		{
			name:       "unset omits extra -X target",
			versionVar: "",
			wantAbsent: "-X github.com/acme/app/internal/buildinfo.Version=${FORGE_VERSION}",
		},
		{
			name:        "set emits extra -X target with FORGE_VERSION value",
			versionVar:  "github.com/acme/app/internal/buildinfo.Version",
			wantContain: "-X github.com/acme/app/internal/buildinfo.Version=${FORGE_VERSION}",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			data := struct {
				Name                   string
				ProtoName              string
				Module                 string
				ServiceName            string
				ServicePort            int
				ProjectName            string
				FrontendName           string
				FrontendPort           int
				GoVersion              string
				GoVersionMinor         string
				DockerBuilderGoVersion string
				LocalForgePkgVendored  bool
				VersionVar             string
				Binaries               []struct {
					Dir     string
					Primary bool
				}
			}{
				Name: "demo", ProtoName: "demo", Module: "github.com/example/demo",
				ServiceName: "api", ServicePort: 8080, ProjectName: "demo",
				GoVersion: "1.26", GoVersionMinor: "26", DockerBuilderGoVersion: "1.26",
				VersionVar: c.versionVar,
				Binaries: []struct {
					Dir     string
					Primary bool
				}{{Dir: "demo", Primary: true}},
			}
			out, err := ProjectTemplates().Render("Dockerfile.tmpl", data)
			if err != nil {
				t.Fatalf("render Dockerfile.tmpl: %v", err)
			}
			rendered := string(out)
			// The canonical main.version/commit/date stamping is always present.
			for _, want := range []string{
				"-X main.version=${FORGE_VERSION}",
				"-X main.commit=${FORGE_COMMIT}",
				"-X main.date=${FORGE_DATE}",
				"ARG FORGE_VERSION=dev",
			} {
				if !strings.Contains(rendered, want) {
					t.Errorf("expected %q in rendered Dockerfile, got:\n%s", want, rendered)
				}
			}
			// The old in-container git-describe invocation in -ldflags must
			// be gone (the prose comment may still mention it).
			if strings.Contains(rendered, "$(git describe") {
				t.Errorf("expected in-container `$(git describe ...)` ldflags to be removed, got:\n%s", rendered)
			}
			if c.wantContain != "" && !strings.Contains(rendered, c.wantContain) {
				t.Errorf("expected %q in rendered Dockerfile, got:\n%s", c.wantContain, rendered)
			}
			if c.wantAbsent != "" && strings.Contains(rendered, c.wantAbsent) {
				t.Errorf("did not expect %q in rendered Dockerfile, got:\n%s", c.wantAbsent, rendered)
			}
		})
	}
}

// TestCmdServerTemplate_ComposesServer verifies the generated serve.go is the
// EXPLICIT-COMPOSITION SHARED SERVE PIPELINE: open the owned infra
// (app.OpenInfra), construct the explicit component graph (app.NewComponents —
// NO by-type injector), build the EXPLICIT interceptor chain via serverkit.NewChain,
// apply a TYPED mount FUNCTION (no string selection, no inventory lookup on the
// run path) onto a serverkit.Server{Mux,HandlerOpts}, and RequireMounted before
// serverkit.Run. serverkit OWNS OTel, so serve projects OTLPEndpoint +
// ServiceName onto skCfg and builds no otel-shutdown closure of its own.
func TestCmdServerTemplate_ComposesServer(t *testing.T) {
	data := struct {
		Module         string
		HasDatabase    bool
		DatabaseDriver string
		OrmEnabled     bool
		ConfigFields   map[string]bool
		RESTEnabled    bool
	}{
		Module:       "example.com/myproject",
		ConfigFields: map[string]bool{"OtlpEndpoint": true},
	}

	content, err := ProjectTemplates().Render("cmd-tree-serve.go.tmpl", data)
	if err != nil {
		t.Fatalf("render cmd-tree-serve.go.tmpl: %v", err)
	}
	rendered := string(content)

	// Explicit composition: OpenInfra → NewComponents over internal/app.
	for _, want := range []string{
		"app.OpenInfra(ctx, cfg, logger)",
		"app.NewComponents(infra)",
		"serverkit.NewChain(serverkit.ChainDeps{",
		"serverkit.Server{",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("cmd-tree-serve.go.tmpl must reference %q (explicit composition); rendered output:\n%s", want, rendered)
		}
	}
	// The completeness gate must still run on the all-services path, and it
	// must be GATED on the ServeSpec asking for it — a per-service subcommand
	// mounts a subset on purpose, so an unconditional gate would refuse to
	// boot it. Asserted structurally (a call guarded by spec.RequireComplete)
	// rather than by matching the gate's current spelling: the scoping half
	// moved into pkg/serverkit, so the old `srv.RequireMounted(projectFiles)`
	// text is gone while the property it stood for is not.
	assertCompletenessGateIsConditional(t, rendered)
	// The retired by-type injector + two-phase post-build + the old
	// DefaultMiddlewares chain AND the *Services god-struct are gone.
	for _, gone := range []string{
		"app.Build(", "app.PostBuild(", "*app.Services", "(*app.Services)",
		"app.BootstrapOnly(", "app.Bootstrap(", "serverkit.Hooks",
		"observe.DefaultMiddlewares(", "setupOTel(", "otelShutdown",
		// self-composition: the string-selection apparatus is retired.
		"ServeOptions", "SelectOperators(", "SelectWorkers(",
		"OperatorNames", "WorkerNames",
	} {
		if strings.Contains(rendered, gone) {
			t.Errorf("cmd-tree-serve.go.tmpl must NOT reference the retired %q; rendered output:\n%s", gone, rendered)
		}
	}
	// TYPED mount: serve takes a typed mount FUNCTION (method expression),
	// not a string. The function value is applied to the constructed graph.
	if !strings.Contains(rendered, "spec.Mount(components, srv.Mux, cfg, logger, opts...)") {
		t.Errorf("cmd-tree-serve.go.tmpl must apply the typed ServeSpec.Mount func to the constructed *Components; rendered output:\n%s", rendered)
	}
	// Self-composition: Serve takes a typed ServeSpec; each command names its
	// own mount + workers + operators via typed accessors — no string filter.
	for _, want := range []string{
		"spec ServeSpec", "Mount MountFunc",
		"spec.Operators(components)", "srv.AddOperator(op.Marker())",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("cmd-tree-serve.go.tmpl must self-compose via %q; rendered output:\n%s", want, rendered)
		}
	}
	// serverkit owns OTel — serve projects OTLPEndpoint + ServiceName.
	if !strings.Contains(rendered, "ServiceName: ServiceName") {
		t.Errorf("cmd-tree-serve.go.tmpl must project the app-identity ServiceName onto skCfg; rendered output:\n%s", rendered)
	}
	if !strings.Contains(rendered, "OTLPEndpoint: cfg.OtlpEndpoint") {
		t.Errorf("cmd-tree-serve.go.tmpl must project cfg.OtlpEndpoint onto skCfg; rendered output:\n%s", rendered)
	}
	// RunOperators wired over the constructed components.
	if !strings.Contains(rendered, "app.RunOperators(ctx, logger, healthProbeAddr, ops)") {
		t.Errorf("cmd-tree-serve.go.tmpl must derive RunOperators from the ServeSpec.Operators slice (ops); rendered output:\n%s", rendered)
	}

	// Verify it still parses as valid Go.
	fset := token.NewFileSet()
	if _, perr := parser.ParseFile(fset, "serve.go", rendered, parser.AllErrors); perr != nil {
		t.Fatalf("rendered serve.go does not parse:\n%v\n\nSource:\n%s", perr, rendered)
	}
}

// assertCompletenessGateIsConditional asserts the boot-time mount-completeness
// gate is (a) present and (b) reached only when the ServeSpec asks for it.
//
// Both halves are load-bearing and neither is visible to a substring match.
// Dropping the gate lets a half-wired server boot green — the failure the gate
// exists for. Running it UNCONDITIONALLY is the opposite failure and just as
// bad: every per-service subcommand mounts a deliberate subset, so an
// always-on gate makes `myapp <service>` refuse to start.
//
// It derives the gate from the guard, not from a name: any call inside an
// `if` whose condition reads spec.RequireComplete counts. That is exactly the
// contract — "this runs only when the spec opts in" — and it survives the gate
// being renamed or moved into a library, which is what happened here.
func assertCompletenessGateIsConditional(t *testing.T, rendered string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "serve.go", rendered, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("rendered serve.go does not parse: %v\n%s", err, rendered)
	}

	guarded, guards := 0, 0
	ast.Inspect(file, func(n ast.Node) bool {
		stmt, ok := n.(*ast.IfStmt)
		if !ok || !readsSelector(stmt.Cond, "spec", "RequireComplete") {
			return true
		}
		guards++
		ast.Inspect(stmt.Body, func(inner ast.Node) bool {
			if _, isCall := inner.(*ast.CallExpr); isCall {
				guarded++
			}
			return true
		})
		return true
	})

	if guards == 0 {
		t.Errorf("rendered serve.go has NO `if spec.RequireComplete` branch — either the boot-time "+
			"completeness gate is gone (a half-wired server boots green) or it now runs "+
			"unconditionally (every per-service subcommand, which mounts a subset on purpose, "+
			"refuses to start); rendered output:\n%s", rendered)
		return
	}
	if guarded == 0 {
		t.Errorf("rendered serve.go guards on spec.RequireComplete but CALLS NOTHING inside that "+
			"branch — the gate is a no-op and a server missing a service still boots; rendered "+
			"output:\n%s", rendered)
	}
}

// readsSelector reports whether expr contains a `recv.field` selector.
func readsSelector(expr ast.Expr, recv, field string) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != field {
			return true
		}
		if id, isIdent := sel.X.(*ast.Ident); isIdent && id.Name == recv {
			found = true
		}
		return true
	})
	return found
}

// TestDeadAppSubstrateTemplatesRemoved asserts the retired pkg/app DI
// substrate templates are gone. The LIVE runtime composition is
// internal/app (OpenInfra → NewComponents); setup.go / post_bootstrap.go /
// app_extras.go / app_gen.go described a DI lifecycle that no longer runs,
// so a fresh scaffold must not emit them.
func TestDeadAppSubstrateTemplatesRemoved(t *testing.T) {
	for _, name := range []string{
		"setup.go.tmpl",
		"post_bootstrap.go.tmpl",
		"app_extras.go.tmpl",
		"app_gen.go.tmpl",
	} {
		if _, err := ProjectTemplates().Get(name); err == nil {
			t.Errorf("dead substrate template %q must not exist; the live DI is internal/app (OpenInfra → NewComponents)", name)
		}
	}
}

// TestComponentTestHelpersTemplate_PackageContractName pins the helper's
// return type to what the component's constructor ACTUALLY returns.
//
// The template used to emit a literal `Service` for the package branch. A
// package whose contract interface is named for what it does — keywrap's
// `KeyWrapper` — therefore generated `func NewTestKeywrap(...) Service`,
// naming a type that does not exist. Because the file is forge-owned the
// author could not fix it, and `forge generate` failed its own validate step,
// so ONE package's naming broke codegen for the whole repository.
func TestComponentTestHelpersTemplate_PackageContractName(t *testing.T) {
	data := struct {
		Module            string
		Package           string
		Name              string
		FieldName         string
		IsService         bool
		ConstructorName   string
		ConstructorType   string
		Fallible          bool
		HasDB             bool
		HasLogger         bool
		ConfigImport      string
		ConfigImportAlias string
		HasMigrationsFS   bool
		NeedsTime         bool
		NeedsULID         bool

		ConnectServiceGoName   string
		ProtoConnectImportPath string
		ProtoConnectPkg        string
		MountMethod            string

		AutoStubs       []struct{ FieldName, StubType, InterfaceQualified string }
		AutoMocks       []struct{ FieldName, MockType, Interface string }
		UnresolvedStubs []struct{ FieldName, TypeExpr string }
		FuncDefaults    []struct {
			FieldName, Expr      string
			NeedsTime, NeedsULID bool
		}
		FuncTodos    []struct{ FieldName, TypeExpr string }
		UnfilledDeps []struct{ FieldName, TypeExpr string }
		ExtraImports []struct{ Alias, Path string }
	}{
		Module:          "example.com/myproject",
		Package:         "keywrap",
		Name:            "keywrap",
		FieldName:       "Keywrap",
		IsService:       false,
		ConstructorName: "New",
		ConstructorType: "KeyWrapper",
		Fallible:        true,
	}

	content, err := ProjectTemplates().Render("component_test_helpers.go.tmpl", data)
	if err != nil {
		t.Fatalf("Render component_test_helpers.go.tmpl: %v", err)
	}
	rendered := string(content)

	if !strings.Contains(rendered, "func NewTestKeywrap(t *testing.T, opts ...TestOption) KeyWrapper {") {
		t.Errorf("helper must return the constructor's own type KeyWrapper; got:\n%s", rendered)
	}
	if strings.Contains(rendered, "opts ...TestOption) Service {") {
		t.Error("helper returns Service, a type this package does not declare — the bug that broke forge generate repo-wide")
	}

	// An unset ConstructorType still falls back to Service, which is right
	// for a just-scaffolded package whose New cannot be parsed yet.
	fallback := data
	fallback.ConstructorType = ""
	content, err = ProjectTemplates().Render("component_test_helpers.go.tmpl", fallback)
	if err != nil {
		t.Fatalf("Render (fallback): %v", err)
	}
	if !strings.Contains(string(content), "opts ...TestOption) Service {") {
		t.Error("an unparseable constructor must fall back to Service")
	}
}
