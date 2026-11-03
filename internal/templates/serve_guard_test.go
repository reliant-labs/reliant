package templates

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// serve_guard_test.go — dataflow guards on the rendered composition root
// (cmd/<bin>/cmd/serve.go).
//
// serve.go reads serverkit.Config fields BEFORE serverkit.Run is entered:
// the Connect payload caps become handler options, and the auto-migrate
// branch opens a sql.DB by driver name. serverkit.Run normalizes the Config
// at ITS entry, which is far too late for those reads — and every one of
// them fails SILENTLY on a zero value. A zero ReadMaxBytes/SendMaxBytes is
// documented by connect-go as UNLIMITED, so the server happily buffers and
// decodes a 300 MiB anonymous request (measured: 41 MB RSS -> 1402 MB, against
// a 1 GiB pod limit) with no error anywhere.
//
// A string-match test cannot see that: the emitted text
// `connect.WithReadMaxBytes(skCfg.ReadMaxBytes)` looks correct whether or not
// the value behind it is zero. So these guards parse the rendered file and
// assert the DATAFLOW property instead — the Config feeding the caps is
// normalized before anything reads it.
//
// They locate the participants STRUCTURALLY — the Config producer by its
// return type, the Server by what is handed to serverkit.Run — rather than by
// the names those things happen to have. Names here are internal spellings a
// refactor may legitimately change, and a guard pinned to one fails on the
// rename instead of on the property, which trains the next reader to edit the
// expectation rather than investigate. The dataflow is what the running
// server depends on; that is what these assert.

// serveRenderCases are the two shapes that matter: a project whose config
// proto declares the cap fields (the default) and one that does not. The
// guarantee must hold for both — an absent config field means the cap comes
// entirely from Normalize.
func serveRenderCases() map[string]map[string]bool {
	return map[string]map[string]bool{
		"caps declared in config proto": {
			"OtlpEndpoint": true,
			"DatabaseUrl":  true,
			"AutoMigrate":  true,
			"ReadMaxBytes": true,
			"SendMaxBytes": true,
		},
		"caps absent from config proto": {
			"OtlpEndpoint": true,
			"DatabaseUrl":  true,
			"AutoMigrate":  true,
		},
	}
}

func renderServe(t *testing.T, configFields map[string]bool) *ast.File {
	t.Helper()
	data := struct {
		Module         string
		HasDatabase    bool
		DatabaseDriver string
		OrmEnabled     bool
		ConfigFields   map[string]bool
		RESTEnabled    bool
	}{
		Module:       "example.com/myproject",
		HasDatabase:  true,
		ConfigFields: configFields,
	}
	content, err := ProjectTemplates().Render("cmd-tree-serve.go.tmpl", data)
	if err != nil {
		t.Fatalf("render cmd-tree-serve.go.tmpl: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "serve.go", content, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("rendered serve.go does not parse: %v\n%s", err, content)
	}
	return file
}

// configProducer finds the function that PRODUCES the serverkit.Config this
// file serves from, and the name of the variable it returns.
//
// It is found by SIGNATURE — "the top-level func whose first result is
// serverkit.Config" — not by name. The name is an internal spelling that a
// legitimate refactor is free to change (it did: the function was once
// called projectServerkitConfig), and a guard keyed to it fails for the
// wrong reason when that happens, which teaches the next reader to rename
// the constant and move on. The signature is the thing the rest of the file
// actually depends on: whatever builds the Config is what must normalize it.
//
// Fails the test when there is no such function, or more than one — either
// means the "single place the Config is built and normalized" property this
// whole file rests on is not there to check.
func configProducer(t *testing.T, file *ast.File) (*ast.FuncDecl, string) {
	t.Helper()
	var found []*ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Type.Results == nil || len(fn.Type.Results.List) == 0 {
			continue
		}
		if isSelector(fn.Type.Results.List[0].Type, "serverkit", "Config") {
			found = append(found, fn)
		}
	}
	if len(found) == 0 {
		t.Fatal("rendered serve.go has NO function returning a serverkit.Config — nothing in this " +
			"file builds the Config the payload caps are read from, so there is no single place " +
			"that can normalize it before those reads happen")
	}
	if len(found) > 1 {
		names := make([]string, 0, len(found))
		for _, fn := range found {
			names = append(names, fn.Name.Name)
		}
		t.Fatalf("rendered serve.go has %d functions returning a serverkit.Config (%v) — the caps "+
			"guard cannot tell which one feeds connect, and two producers means one of them can "+
			"skip Normalize unnoticed", len(found), names)
	}
	fn := found[0]
	returned := returnedIdent(fn)
	if returned == "" {
		t.Fatalf("%s does not return a NAMED serverkit.Config variable, so nothing in it can be "+
			"shown to have been normalized before it is returned", fn.Name.Name)
	}
	return fn, returned
}

// isSelector reports whether expr is the qualified type `pkg.name`.
func isSelector(expr ast.Expr, pkg, name string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

// TestServeTemplate_PayloadCapsAreNormalizedBeforeUse asserts the whole chain:
// every connect.With{Read,Send}MaxBytes argument is a field read off a
// serverkit.Config variable, that variable comes from the file's Config
// producer, and that producer normalizes the value it returns. Break any link
// and the caps silently become unlimited.
func TestServeTemplate_PayloadCapsAreNormalizedBeforeUse(t *testing.T) {
	for name, fields := range serveRenderCases() {
		t.Run(name, func(t *testing.T) {
			file := renderServe(t, fields)

			// 1. The producer must normalize the Config it returns.
			fn, returned := configProducer(t, file)
			if !callsMethodOn(fn, returned, "Normalize") {
				t.Errorf("%s returns %q without calling %s.Normalize() — "+
					"every field it leaves zero (payload caps, DB driver) reaches its caller as a "+
					"zero value, and serverkit.Run's own normalization happens far too late to help",
					fn.Name.Name, returned, returned)
			}

			// 2. The caps reach the chain as fields of the serverkit.ChainDeps
			//    serverkit.NewChain builds the handler options from, read off a
			//    variable produced by that same producer — never a literal,
			//    never an unrelated value. (NewChain maps a zero cap to 4 MiB,
			//    so a cap that read zero would still be bounded; this pins that
			//    the configured knobs are the ones that reach connect.)
			fields := chainDepsFields(t, file)
			for field, kind := range chainCapFields {
				v, ok := fields[field]
				if !ok {
					t.Errorf("serverkit.ChainDeps sets no %s — the configured %s cap never reaches connect", field, kind)
					continue
				}
				argSel, isSel := v.(*ast.SelectorExpr)
				if !isSel {
					t.Errorf("ChainDeps.%s must be a field of the normalized serverkit.Config, got a bare expression", field)
					continue
				}
				recv, isIdent := argSel.X.(*ast.Ident)
				if !isIdent {
					t.Errorf("ChainDeps.%s is not a simple <config>.<Field> selector", field)
					continue
				}
				if !assignedFromCall(file, recv.Name, fn.Name.Name) {
					t.Errorf("ChainDeps.%s reads %s.%s, but %s is not the value returned by %s — "+
						"it is therefore not normalized", field, recv.Name, argSel.Sel.Name, recv.Name, fn.Name.Name)
				}
			}
		})
	}
}

// TestServeTemplate_ChainDeadlineIsTheProtoProjection pins that the
// scaffolded chain hands its Deadline slot the budgets the protos declare —
// serverkit.Deadline{Procedures: middleware.ProcedureDeadlines}. Without it
// `(forge.v1.method).timeout` is generated into procedures_gen.go and read
// by nothing, which is what it was before the slot existed.
func TestServeTemplate_ChainDeadlineIsTheProtoProjection(t *testing.T) {
	for name, fields := range serveRenderCases() {
		t.Run(name, func(t *testing.T) {
			v, ok := chainDepsFields(t, renderServe(t, fields))["Deadline"]
			if !ok {
				t.Fatal("serverkit.ChainDeps sets no Deadline — the protos' declared timeouts never reach the chain")
			}
			lit, ok := v.(*ast.CompositeLit)
			if !ok || !isSelector(lit.Type, "serverkit", "Deadline") {
				t.Fatalf("ChainDeps.Deadline is not a serverkit.Deadline{...} literal")
			}
			for _, elt := range lit.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Procedures" && isSelector(kv.Value, "middleware", "ProcedureDeadlines") {
						return
					}
				}
			}
			t.Error("serverkit.Deadline does not set Procedures: middleware.ProcedureDeadlines")
		})
	}
}

// chainCapFields are the ChainDeps payload caps serve.go must feed from the
// normalized Config.
var chainCapFields = map[string]string{
	"ReadMaxBytes": "inbound request",
	"SendMaxBytes": "outbound response",
}

// chainDepsFields returns the fields of the serverkit.ChainDeps literal passed
// to serverkit.NewChain — the one chain the handler options come from.
func chainDepsFields(t *testing.T, file *ast.File) map[string]ast.Expr {
	t.Helper()
	var lits []*ast.CompositeLit
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isSelector(call.Fun, "serverkit", "NewChain") || len(call.Args) != 1 {
			return true
		}
		if lit, ok := call.Args[0].(*ast.CompositeLit); ok && isSelector(lit.Type, "serverkit", "ChainDeps") {
			lits = append(lits, lit)
		}
		return true
	})
	if len(lits) != 1 {
		t.Fatalf("rendered serve.go builds %d serverkit.NewChain(serverkit.ChainDeps{...}) chains, want exactly 1", len(lits))
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

// TestServeTemplate_ConfigCapFieldsAreProjected asserts the operator-facing
// half: when the project's config proto declares the cap fields, serve.go must
// actually project them onto the serverkit.Config. Without this the env vars
// exist and do nothing.
func TestServeTemplate_ConfigCapFieldsAreProjected(t *testing.T) {
	file := renderServe(t, serveRenderCases()["caps declared in config proto"])
	fn, _ := configProducer(t, file)

	for _, field := range []string{"ReadMaxBytes", "SendMaxBytes"} {
		if !assignsField(fn, field) {
			t.Errorf("%s never assigns %s — the READ_MAX_BYTES/SEND_MAX_BYTES "+
				"config knobs would be silently ignored", fn.Name.Name, field)
		}
	}
}

// TestServeTemplate_ReadinessRegistersTheServingPool asserts that a rendered
// composition root registers the database it ACTUALLY SERVES FROM as a /readyz
// dependency, and registers it in time to count.
//
// pkg/serverkit already answers /readyz from its registered checks, and
// pkg/serverkit/readiness_test.go proves the endpoint returns 503 when one
// fails. None of that runs if the composition root never registers anything:
// the endpoint keeps answering the listener-bound question alone, so a rollout
// carrying a bad DB secret reports Ready on every replica and reaches 100%
// behind green probes. That defect lived entirely in this template, and until
// now nothing asserted the line was there.
//
// The two properties a string match cannot see:
//
//   - ORDER. serverkit.Run takes its Server BY VALUE and hands
//     srv.ReadyChecks to the /readyz handler at that moment. A registration
//     that happens after the Run call is a silent no-op — it compiles, it
//     reads correctly, and /readyz is unconditional again.
//   - IDENTITY. The check must ping the pool the handlers use (the one
//     app.OpenInfra returned), not a fresh connection opened for the probe. A
//     probe against a second pool answers a question nobody asked: it stays
//     green while the serving pool is exhausted.
func TestServeTemplate_ReadinessRegistersTheServingPool(t *testing.T) {
	for name, fields := range serveRenderCases() {
		t.Run(name, func(t *testing.T) {
			file := renderServe(t, fields)
			fn := findFunc(file, "Serve")
			if fn == nil {
				t.Fatal("rendered serve.go has no Serve function")
			}

			srvName, runPos := serverkitRunTarget(fn)
			if srvName == "" {
				t.Fatal("rendered serve.go never calls serverkit.Run with a named Server value")
			}

			var registered []*ast.CallExpr
			late, foreign := 0, 0
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "AddReadyCheck" {
					return true
				}
				recv, isIdent := sel.X.(*ast.Ident)
				if !isIdent || recv.Name != srvName {
					foreign++
					return true
				}
				if call.Pos() > runPos {
					late++
					return true
				}
				registered = append(registered, call)
				return true
			})

			if late > 0 {
				t.Errorf("%d AddReadyCheck call(s) happen AFTER serverkit.Run — Run takes the Server by "+
					"value, so /readyz was built from an empty check list and answers 200 with the "+
					"database down", late)
			}
			if foreign > 0 {
				t.Errorf("%d AddReadyCheck call(s) target a Server other than %q, the one passed to "+
					"serverkit.Run — those checks never reach /readyz", foreign, srvName)
			}
			if len(registered) == 0 {
				t.Fatalf("rendered serve.go never calls %s.AddReadyCheck — /readyz reports Ready on the "+
					"strength of a bound listener alone, so a rollout with an unreachable database "+
					"rolls to 100%% behind green probes", srvName)
			}

			// The registered check must be built from the SERVING pool.
			probesServingPool := false
			for _, call := range registered {
				if len(call.Args) != 1 {
					continue
				}
				inner, isCall := call.Args[0].(*ast.CallExpr)
				if !isCall || calleeName(inner.Fun) != "DBReadyCheck" || len(inner.Args) != 2 {
					continue
				}
				poolSel, isSel := inner.Args[1].(*ast.SelectorExpr)
				if !isSel {
					continue
				}
				owner, isIdent := poolSel.X.(*ast.Ident)
				if !isIdent {
					continue
				}
				if assignedFromCall(file, owner.Name, "OpenInfra") {
					probesServingPool = true
				}
			}
			if !probesServingPool {
				t.Error("no AddReadyCheck registers serverkit.DBReadyCheck over a pool field of the value " +
					"app.OpenInfra returned — /readyz must probe the pool the handlers serve from, or it " +
					"reports green while the serving pool is unreachable")
			}
		})
	}
}

// serverkitRunTarget returns the name of the Server variable handed to
// serverkit.Run, and the position of that call. Both halves are the point:
// checks registered on another variable, or after this position, never reach
// the /readyz handler.
func serverkitRunTarget(fn *ast.FuncDecl) (string, token.Pos) {
	var name string
	var pos token.Pos
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || name != "" {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Run" {
			return true
		}
		pkg, isIdent := sel.X.(*ast.Ident)
		if !isIdent || pkg.Name != "serverkit" || len(call.Args) == 0 {
			return true
		}
		if id, isArgIdent := call.Args[len(call.Args)-1].(*ast.Ident); isArgIdent {
			name, pos = id.Name, call.Pos()
		}
		return true
	})
	return name, pos
}

// findFunc returns the named top-level function declaration, or nil.
func findFunc(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// returnedIdent returns the name of the first identifier fn returns, or "".
func returnedIdent(fn *ast.FuncDecl) string {
	var name string
	ast.Inspect(fn, func(n ast.Node) bool {
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) == 0 || name != "" {
			return true
		}
		if id, isIdent := ret.Results[0].(*ast.Ident); isIdent {
			name = id.Name
		}
		return true
	})
	return name
}

// callsMethodOn reports whether fn contains a `recv.method(...)` call.
func callsMethodOn(fn *ast.FuncDecl, recv, method string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != method {
			return true
		}
		if id, isIdent := sel.X.(*ast.Ident); isIdent && id.Name == recv {
			found = true
		}
		return true
	})
	return found
}

// assignsField reports whether fn assigns the named struct field, either as a
// composite-literal key or as a `x.Field = …` selector assignment.
func assignsField(fn *ast.FuncDecl, field string) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.KeyValueExpr:
			if key, ok := node.Key.(*ast.Ident); ok && key.Name == field {
				found = true
			}
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == field {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// calleeName is the trailing identifier of a call's function expression:
// "f" for f(), "OpenInfra" for app.OpenInfra(). Package qualification is
// deliberately ignored — these guards care which FUNCTION produced a value,
// and the import alias is not part of that question.
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// assignedFromCall reports whether `name` is assigned anywhere in file from a
// call to the named function (bare or package-qualified).
func assignedFromCall(file *ast.File, name, callee string) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		lhsMatches := false
		for _, lhs := range assign.Lhs {
			if id, isIdent := lhs.(*ast.Ident); isIdent && id.Name == name {
				lhsMatches = true
			}
		}
		if !lhsMatches {
			return true
		}
		for _, rhs := range assign.Rhs {
			call, isCall := rhs.(*ast.CallExpr)
			if !isCall {
				continue
			}
			if calleeName(call.Fun) == callee {
				found = true
			}
		}
		return true
	})
	return found
}

// TestServeTemplate_StreamingRPCsAreBoundToTheDrain pins the #773 fix into
// every scaffolded server: a server-streaming or bidi RPC never finishes on
// its own, so a chain without the stream-drain interceptor leaves each open
// stream to hold the shutdown for the whole SHUTDOWN_TIMEOUT and then be cut
// without a status. serverkit.NewChain installs it by default
// (pkg/serverkit's TestNewChain_DefaultsAndOmits and
// TestListener_DefaultChainEndsStreamsOnDrain prove that); this pins that the
// scaffold builds its handler options from that chain and does not drop it.
func TestServeTemplate_StreamingRPCsAreBoundToTheDrain(t *testing.T) {
	for name, fields := range serveRenderCases() {
		t.Run(name, func(t *testing.T) {
			file := renderServe(t, fields)
			deps := chainDepsFields(t, file)
			if v, ok := deps["StreamDrain"]; ok && isSelector(v, "serverkit", "NoStreamDrain") {
				t.Error("rendered serve.go drops the chain's stream drain (serverkit.NoStreamDrain) — " +
					"every open server stream would hold the shutdown for the whole budget and be cut without a status")
			}
			if !assignedFromCall(file, "opts", "HandlerOptions") {
				t.Error("rendered serve.go does not build its handler options from the chain (opts := chain.HandlerOptions())")
			}
		})
	}
}
