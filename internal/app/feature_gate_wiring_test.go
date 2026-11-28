// Copyright (c) 2026 Reliant Labs
package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// TestWorkerCallLLMGetsTheFeatureGate holds the worker's call_llm activity to
// the process's feature gate, link by link:
//
//	compose.go          newTemporalWorker: TemporalWorkerDeps{Gate: g.FeatureGate()}
//	providers_worker.go NewTemporalWorker: workersetup.Config{FeatureGate: d.Gate}
//	workersetup         NewWorker:         activityDeps.FeatureGate = cfg.FeatureGate
//	activities          RegisterAll:       handlers.NewCallLLMActivity(…)….WithFeatureGate(deps.FeatureGate)
//
// Every link is optional (nil entitles nothing), so dropping any one of them
// compiles and every other test passes: the GTM tools would silently vanish
// for everyone. The registered activity is a wrapped function the registry
// cannot hand back, so the links are checked in the source, as
// chat_access_wiring_test.go checks Chats.
func TestWorkerCallLLMGetsTheFeatureGate(t *testing.T) {
	t.Run("compose.go: newTemporalWorker passes g.FeatureGate()", func(t *testing.T) {
		f := parseGo(t, "compose.go")
		found := false
		eachFunc(f, "newTemporalWorker", func(body *ast.BlockStmt) {
			ast.Inspect(body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if ok && isIdent(lit.Type, "TemporalWorkerDeps") {
					found = found || hasField(lit, "Gate", func(v ast.Expr) bool { return isCallOf(v, "g", "FeatureGate") })
				}
				return true
			})
		})
		if !found {
			t.Error("newTemporalWorker does not set TemporalWorkerDeps.Gate to g.FeatureGate()")
		}
	})

	t.Run("providers_worker.go: NewTemporalWorker passes d.Gate to workersetup", func(t *testing.T) {
		f := parseGo(t, "providers_worker.go")
		found := false
		eachFunc(f, "NewTemporalWorker", func(body *ast.BlockStmt) {
			ast.Inspect(body, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if ok && isSelector(lit.Type, "workersetup", "Config") {
					found = found || hasField(lit, "FeatureGate", func(v ast.Expr) bool { return isSelector(v, "d", "Gate") })
				}
				return true
			})
		})
		if !found {
			t.Error("NewTemporalWorker does not set workersetup.Config.FeatureGate to d.Gate")
		}
	})

	t.Run("workersetup: NewWorker copies cfg.FeatureGate to the activities", func(t *testing.T) {
		f := parseGo(t, filepath.Join("..", "workersetup", "setup.go"))
		found := false
		eachFunc(f, "NewWorker", func(body *ast.BlockStmt) {
			ast.Inspect(body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 &&
					isSelector(as.Lhs[0], "activityDeps", "FeatureGate") && isSelector(as.Rhs[0], "cfg", "FeatureGate") {
					found = true
				}
				return true
			})
		})
		if !found {
			t.Error("workersetup.NewWorker does not set activityDeps.FeatureGate = cfg.FeatureGate")
		}
	})

	t.Run("activities: RegisterAll gives call_llm deps.FeatureGate", func(t *testing.T) {
		f := parseGo(t, filepath.Join("..", "workflow", "runtime", "activities", "register.go"))
		found := false
		eachFunc(f, "RegisterAll", func(body *ast.BlockStmt) {
			ast.Inspect(body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "WithFeatureGate" || len(call.Args) != 1 || !isSelector(call.Args[0], "deps", "FeatureGate") {
					return true
				}
				// The receiver chain must start at handlers.NewCallLLMActivity.
				for x := sel.X; x != nil; {
					c, ok := x.(*ast.CallExpr)
					if !ok {
						break
					}
					if isSelector(c.Fun, "handlers", "NewCallLLMActivity") {
						found = true
						break
					}
					s, ok := c.Fun.(*ast.SelectorExpr)
					if !ok {
						break
					}
					x = s.X
				}
				return true
			})
		})
		if !found {
			t.Error("RegisterAll does not call handlers.NewCallLLMActivity(…)….WithFeatureGate(deps.FeatureGate)")
		}
	})
}

// TestFeatureGateReachesEveryConsumer holds every other consumer of the
// process's feature gate to it, the same way: each Deps literal must set its
// gate field to g.FeatureGate() in compose.go, or to d.Gate where a provider
// passes its own Deps.Gate on. Each field is optional and a nil gate entitles
// nothing, so dropping any one of these fails closed and compiles: GTM would
// silently disappear from that surface (the catalog, the API server's and the
// worker's trigger services, or the API server's and the worker's GTM domain).
func TestFeatureGateReachesEveryConsumer(t *testing.T) {
	fromGraph := func(v ast.Expr) bool { return isCallOf(v, "g", "FeatureGate") }
	fromDeps := func(v ast.Expr) bool { return isSelector(v, "d", "Gate") }
	cases := []struct {
		file, fn  string
		pkg, typ  string // the Deps literal's type: pkg.typ, or typ when pkg is ""
		field     string
		value     func(ast.Expr) bool
		valueName string
	}{
		{"compose.go", "newCatalog", "catalog", "Deps", "Gate", fromGraph, "g.FeatureGate()"},
		{"compose.go", "newTrigger", "trigger", "Deps", "Gate", fromGraph, "g.FeatureGate()"},
		{"compose.go", "newWorkerTrigger", "", "WorkerTriggerDeps", "Gate", fromGraph, "g.FeatureGate()"},
		{"providers_worker.go", "NewWorkerTrigger", "trigger", "Deps", "Gate", fromDeps, "d.Gate"},
		{"compose.go", "newGtmDomain", "", "GtmDomainDeps", "Gate", fromGraph, "g.FeatureGate()"},
		{"providers_gtm.go", "NewGtmDomain", "gtmsvc", "Deps", "Gate", fromDeps, "d.Gate"},
		{"compose.go", "newWorkerGtmDomain", "", "WorkerGtmDomainDeps", "Gate", fromGraph, "g.FeatureGate()"},
		{"providers_gtm.go", "NewWorkerGtmDomain", "gtmsvc", "Deps", "Gate", fromDeps, "d.Gate"},
	}
	for _, c := range cases {
		typ := c.typ
		if c.pkg != "" {
			typ = c.pkg + "." + c.typ
		}
		t.Run(c.file+": "+c.fn+" sets "+typ+"."+c.field, func(t *testing.T) {
			f := parseGo(t, c.file)
			found := false
			eachFunc(f, c.fn, func(body *ast.BlockStmt) {
				ast.Inspect(body, func(n ast.Node) bool {
					lit, ok := n.(*ast.CompositeLit)
					if !ok {
						return true
					}
					if (c.pkg == "" && isIdent(lit.Type, c.typ)) || (c.pkg != "" && isSelector(lit.Type, c.pkg, c.typ)) {
						found = found || hasField(lit, c.field, c.value)
					}
					return true
				})
			})
			if !found {
				t.Errorf("%s does not set %s.%s to %s", c.fn, typ, c.field, c.valueName)
			}
		})
	}
}

func parseGo(t *testing.T, path string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return f
}

// eachFunc calls fn with the body of every function or method named name.
func eachFunc(f *ast.File, name string, fn func(*ast.BlockStmt)) {
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name && fd.Body != nil {
			fn(fd.Body)
		}
	}
}

func hasField(lit *ast.CompositeLit, key string, value func(ast.Expr) bool) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if ok && isIdent(kv.Key, key) && value(kv.Value) {
			return true
		}
	}
	return false
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func isSelector(e ast.Expr, x, sel string) bool {
	s, ok := e.(*ast.SelectorExpr)
	return ok && isIdent(s.X, x) && s.Sel.Name == sel
}

func isCallOf(e ast.Expr, x, sel string) bool {
	c, ok := e.(*ast.CallExpr)
	return ok && len(c.Args) == 0 && isSelector(c.Fun, x, sel)
}
