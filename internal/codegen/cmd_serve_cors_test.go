package codegen

// The generated serve scaffold's CORS wiring.
//
// serverkit enables the CORS layer when origins are named OR the runtime
// environment is development (serverkit.Config.CORSEnabled) — the dev arm
// exists because nothing defaults CORS_ORIGINS and a scaffolded frontend's
// dev port is not knowable to the server. That means the scaffold must supply
// a CORSMiddleware factory whenever EITHER config field is present: a project
// whose config carries `environment` but no `cors_origins` still reaches the
// layer, and serverkit fails closed on a missing factory.
//
// These guards read the PARSED scaffold rather than grepping it. The factory
// is found as a key in the serverkit.Server composite literal and the dev arm
// as a comparison against serverkit.EnvDevelopment — both are properties the
// compiled server actually has, where a substring is only evidence about
// today's formatting. That matters most for the negative cases: `strings.
// Contains(got, "DevCORSMiddleware")` is satisfied by the word appearing in a
// COMMENT, so a scaffold that documented the dev policy without wiring it
// would have passed.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// parseServeScaffold renders the cmd serve scaffold for exactly the named
// config fields and returns its AST.
func parseServeScaffold(t *testing.T, fieldNames ...string) *ast.File {
	t.Helper()
	fields := map[string]bool{}
	for _, n := range fieldNames {
		fields[n] = true
	}
	return parseServeFields(t, fields)
}

// parseServeFields renders and parses the scaffold for a config field set.
func parseServeFields(t *testing.T, fields map[string]bool) *ast.File {
	t.Helper()
	out, err := templates.ProjectTemplates().Render("cmd-tree-serve.go.tmpl", CmdServerTemplateData{
		Module:       "example.com/proj",
		ConfigFields: fields,
	})
	if err != nil {
		t.Fatalf("render serve scaffold %v: %v", fields, err)
	}
	file, perr := parser.ParseFile(token.NewFileSet(), "serve.go", out, parser.SkipObjectResolution)
	if perr != nil {
		t.Fatalf("rendered serve scaffold %v does not parse: %v\n%s", fields, perr, out)
	}
	return file
}

// corsFactory returns the CORSMiddleware function literal the scaffold
// installs on the serverkit.Server, or nil when it installs none.
//
// It looks for the field in the SERVER LITERAL specifically, because that is
// the only position where serverkit will ever read it — a CORSMiddleware
// assigned anywhere else is dead code that a name-based search would happily
// accept.
func corsFactory(t *testing.T, file *ast.File) *ast.FuncLit {
	t.Helper()
	var factory *ast.FuncLit
	var serverLiterals int
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isQualifiedType(lit.Type, "serverkit", "Server") {
			return true
		}
		serverLiterals++
		for _, elt := range lit.Elts {
			kv, isKV := elt.(*ast.KeyValueExpr)
			if !isKV {
				continue
			}
			key, isIdent := kv.Key.(*ast.Ident)
			if !isIdent || key.Name != "CORSMiddleware" {
				continue
			}
			if fn, isFn := kv.Value.(*ast.FuncLit); isFn {
				factory = fn
			} else {
				t.Errorf("serverkit.Server.CORSMiddleware is not a function literal (%T) — "+
					"this guard reads the factory's body to tell the dev arm from the strict one", kv.Value)
			}
		}
		return true
	})
	if serverLiterals == 0 {
		t.Fatal("rendered scaffold builds NO serverkit.Server literal — there is no edge " +
			"configuration to check, so every CORS assertion below would vacuously pass")
	}
	return factory
}

// devCORSArm reports whether fn takes a permissive branch gated on the
// runtime environment being development, and whether that gate compares
// against serverkit's own constant.
//
// usesConstant is only meaningful when hasArm is true.
func devCORSArm(fn *ast.FuncLit) (hasArm, usesConstant bool) {
	if fn == nil {
		return false, false
	}
	ast.Inspect(fn, func(n ast.Node) bool {
		bin, ok := n.(*ast.BinaryExpr)
		if !ok || bin.Op != token.EQL {
			return true
		}
		// The environment side must be a read of the typed config, so a
		// comparison of two unrelated values can't masquerade as the gate.
		if !isQualifiedType(bin.X, "cfg", "Environment") {
			return true
		}
		hasArm = true
		if isQualifiedType(bin.Y, "serverkit", "EnvDevelopment") {
			usesConstant = true
		}
		return true
	})
	return hasArm, usesConstant
}

// isQualifiedType reports whether expr is the selector `x.sel`.
func isQualifiedType(expr ast.Expr, x, sel string) bool {
	s, ok := expr.(*ast.SelectorExpr)
	if !ok || s.Sel.Name != sel {
		return false
	}
	id, isIdent := s.X.(*ast.Ident)
	return isIdent && id.Name == x
}

// TestCmdServe_CORSFactoryEmittedForEitherConfigField is the regression.
// Gating the factory on `cors_origins` alone left a project that has
// `environment` but no `cors_origins` handing serverkit a nil factory for a
// layer serverkit now enables — a boot-time fail-closed error.
func TestCmdServe_CORSFactoryEmittedForEitherConfigField(t *testing.T) {
	cases := []struct {
		name       string
		fields     []string
		wantFactor bool
		wantDev    bool
		why        string
	}{
		{
			name:       "environment only",
			fields:     []string{"Environment"},
			wantFactor: true,
			wantDev:    true,
			why:        "development enables CORS with no origins, so the factory must exist",
		},
		{
			name:       "cors_origins only",
			fields:     []string{"CorsOrigins"},
			wantFactor: true,
			wantDev:    false,
			why:        "named origins are enforced, but with no MODE field there is no dev branch to take",
		},
		{
			name:       "both",
			fields:     []string{"Environment", "CorsOrigins"},
			wantFactor: true,
			wantDev:    true,
			why:        "the default scaffold shape",
		},
		{
			name:       "neither",
			fields:     []string{"Port"},
			wantFactor: false,
			wantDev:    false,
			why:        "a config with no CORS and no MODE field can never enable the layer",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			factory := corsFactory(t, parseServeScaffold(t, c.fields...))

			if has := factory != nil; has != c.wantFactor {
				t.Errorf("serverkit.Server.CORSMiddleware factory installed = %v, want %v — %s",
					has, c.wantFactor, c.why)
			}
			if hasDev, _ := devCORSArm(factory); hasDev != c.wantDev {
				t.Errorf("dev CORS branch present = %v, want %v — %s", hasDev, c.wantDev, c.why)
			}
		})
	}
}

// TestCmdServe_DevCORSBranchUsesServerkitConstant: the scaffold must compare
// against serverkit.EnvDevelopment, not a re-typed "development" literal.
// serverkit owns the value that unlocks the permissive posture; a second copy
// of the string in generated code is a place for the two to drift, and the
// drift is silent — a scaffold comparing against "dev" simply never takes the
// branch, and the frontend's CORS failures look like a browser problem.
func TestCmdServe_DevCORSBranchUsesServerkitConstant(t *testing.T) {
	factory := corsFactory(t, parseServeScaffold(t, "Environment", "CorsOrigins"))
	hasDev, usesConstant := devCORSArm(factory)
	if !hasDev {
		t.Fatal("scaffold has no dev CORS branch to check — with an `environment` config field " +
			"the permissive dev arm must exist")
	}
	if !usesConstant {
		t.Error("the dev CORS branch does not compare cfg.Environment against " +
			"serverkit.EnvDevelopment — a re-typed literal is a second copy of the value that " +
			"unlocks the permissive posture, free to drift from serverkit's own")
	}
}

// TestCmdServe_DefaultScaffoldWiresDevCORS pins the shape a freshly forged
// project actually gets: the default config field set must produce both the
// factory and the constant-compared dev branch, so `--environment development`
// delivers the relaxed CORS its own flag help advertises.
func TestCmdServe_DefaultScaffoldWiresDevCORS(t *testing.T) {
	fields := DefaultConfigFieldNames()
	if !fields["Environment"] || !fields["CorsOrigins"] {
		t.Fatalf("the default config field set no longer carries both Environment and CorsOrigins "+
			"(%v) — this test would still pass while asserting nothing about the shape a fresh "+
			"project gets", fields)
	}

	factory := corsFactory(t, parseServeFields(t, fields))
	if factory == nil {
		t.Fatal("the default scaffold installs no CORSMiddleware factory — serverkit enables the " +
			"CORS layer for a development environment and fails closed on a nil factory")
	}
	hasDev, usesConstant := devCORSArm(factory)
	if !hasDev {
		t.Error("the default scaffold has no dev CORS branch")
	}
	if hasDev && !usesConstant {
		t.Error("the default scaffold's dev CORS branch does not use serverkit.EnvDevelopment")
	}
}

// The browser's traceparent must PARENT the API's server span. otelconnect
// treats inbound trace context as untrusted by default and only LINKS to it, so
// without WithTrustRemote a page and the API call it made are two traces and
// "this click, end to end" has no answer. Found live: ClickHouse held the
// browser's and the API's spans under different trace ids although the request
// carried a valid traceparent.
//
// serverkit.NewChain's default Trace is otelconnect with WithTrustRemote
// (pkg/serverkit's TestNewChain_DefaultTraceTrustsRemoteParent proves it), so
// the scaffold keeps it by leaving the slot unset — or, if it fills the slot
// itself, by building otelconnect with the option.
func TestServeScaffoldTrustsTheBrowsersTraceContext(t *testing.T) {
	deps := serveChainDeps(t, parseServeFields(t, nil))
	trace, set := deps["Trace"]
	if !set {
		return
	}
	if isSel(trace, "serverkit", "NoTrace") {
		t.Fatal("the serve scaffold drops the chain's trace span (serverkit.NoTrace)")
	}
	trusted := false
	ast.Inspect(trace, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && isSel(c.Fun, "otelconnect", "WithTrustRemote") {
			trusted = true
		}
		return true
	})
	if !trusted {
		t.Error("ChainDeps.Trace is built without otelconnect.WithTrustRemote: browser and API spans land in different traces")
	}
}

// Tracing must wrap auth: a call auth rejects is exactly the one you want a
// span for, and the logs written for it must carry the span's trace_id. The
// scaffold used to PREPEND otelconnect to Deps.Extras, which run after auth,
// so a 401 produced no span and its "rpc failed" line had no trace_id. The
// span belongs in the chain's outermost slot — ChainDeps.Trace, whose default
// is the trusted otelconnect interceptor — and never among the Extras.
func TestServeScaffoldOpensTheTraceSpanOutsideAuth(t *testing.T) {
	deps := serveChainDeps(t, parseServeFields(t, nil))
	if extras, ok := deps["Extras"]; ok {
		ast.Inspect(extras, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "otelconnect" {
				t.Error("an otelconnect interceptor is among ChainDeps.Extras, which run after auth: a rejected call has no span")
			}
			return true
		})
	}
}

// serveChainDeps returns the fields of the one serverkit.ChainDeps literal
// passed to serverkit.NewChain in the scaffolded serve.go.
func serveChainDeps(t *testing.T, file *ast.File) map[string]ast.Expr {
	t.Helper()
	var lits []*ast.CompositeLit
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isSel(call.Fun, "serverkit", "NewChain") || len(call.Args) != 1 {
			return true
		}
		if lit, ok := call.Args[0].(*ast.CompositeLit); ok && isSel(lit.Type, "serverkit", "ChainDeps") {
			lits = append(lits, lit)
		}
		return true
	})
	if len(lits) != 1 {
		t.Fatalf("the serve scaffold builds %d serverkit.NewChain(serverkit.ChainDeps{...}) chains, want exactly 1", len(lits))
	}
	fields := map[string]ast.Expr{}
	for _, elt := range lits[0].Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			if key, ok := kv.Key.(*ast.Ident); ok {
				fields[key.Name] = kv.Value
			}
		}
	}
	return fields
}

// isSel reports whether e is the selector pkg.name.
func isSel(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == pkg
}
