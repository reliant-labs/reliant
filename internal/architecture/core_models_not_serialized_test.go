package architecture

import (
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"golang.org/x/tools/go/packages"
)

// ─── step and scenario models are never serialized directly ────────────────
//
// core.StepExecution, core.ChatStepExecution and core.WorkflowScenario hold
// their nullable columns as pointers. That is a different JSON shape from the
// sql.Null* structs they replaced ({"String":..,"Valid":..} became the bare
// value or null), so one of them reaching an encoder — directly, in a slice or
// map, or as a field of another struct — would change a persisted or public
// payload. Every consumer projects them by hand (proto messages, the dev
// tools' own report structs); this test keeps it that way by type-checking the
// whole module, tests included, and failing on any call to a YAML, XML, gob
// or protojson encoder or decoder whose argument can contain one of the three.
// It follows aliases (db.StepExecution), pointers, slices, maps and exported
// struct fields not tagged json:"-".
//
// encoding/json is not on the list: the three models implement
// json.Marshaler/Unmarshaler in the pre-pointer shape
// (internal/db/core/legacy_json.go, pinned by legacy_json_test.go), so a value
// that reaches encoding/json by any path — directly, behind an interface,
// inside a wrapper — renders the old bytes. This guard keeps the other
// encoders, which those methods do not cover, away from them.

var serializedModels = map[string]bool{"StepExecution": true, "ChatStepExecution": true, "WorkflowScenario": true}

var serializerPackages = []string{
	"encoding/gob", "encoding/xml",
	"gopkg.in/yaml", "go.yaml.in/yaml", "github.com/goccy/go-yaml", "sigs.k8s.io/yaml",
	"google.golang.org/protobuf/encoding/protojson",
}

func isSerializer(path string) bool {
	for _, p := range serializerPackages {
		if path == p || strings.HasPrefix(path, p+"/") || strings.HasPrefix(path, p+".") || strings.HasPrefix(path, p+"@") || strings.HasPrefix(path, p) && strings.Contains(path[len(p):], "yaml") {
			return true
		}
	}
	return false
}

func carriesSerializedModel(ty types.Type, seen map[types.Type]bool) bool {
	ty = types.Unalias(ty)
	if seen[ty] {
		return false
	}
	seen[ty] = true
	switch x := ty.(type) {
	case *types.Pointer:
		return carriesSerializedModel(x.Elem(), seen)
	case *types.Slice:
		return carriesSerializedModel(x.Elem(), seen)
	case *types.Array:
		return carriesSerializedModel(x.Elem(), seen)
	case *types.Map:
		return carriesSerializedModel(x.Elem(), seen)
	case *types.Named:
		if o := x.Obj(); o.Pkg() != nil && strings.HasSuffix(o.Pkg().Path(), "/internal/db/core") && serializedModels[o.Name()] {
			return true
		}
		return carriesSerializedModel(x.Underlying(), seen)
	case *types.Struct:
		for i := 0; i < x.NumFields(); i++ {
			f := x.Field(i)
			if !f.Exported() && !f.Embedded() || strings.Contains(x.Tag(i), `json:"-"`) {
				continue
			}
			if carriesSerializedModel(f.Type(), seen) {
				return true
			}
		}
	}
	return false
}

func TestStepAndScenarioModelsAreNeverSerialized(t *testing.T) {
	testkit.Slow(t, "type-checks the whole module, tests included")

	pkgs, err := packages.Load(&packages.Config{
		Mode:  packages.NeedName | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedFiles,
		Tests: true,
		Dir:   "../..",
	}, "./...")
	if err != nil {
		t.Fatal(err)
	}
	// packages.Load reports a package that failed to parse or type-check in
	// its Errors, not in err; its calls would go unchecked.
	var loadErrs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, p.ID+": "+e.Error())
		}
	})
	if len(loadErrs) > 0 {
		t.Fatalf("packages failed to load, so their serializer calls were not checked:\n%s", strings.Join(loadErrs, "\n"))
	}

	found := map[string]bool{}
	checked := 0
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var callee types.Object
				switch fn := call.Fun.(type) {
				case *ast.SelectorExpr:
					callee = p.TypesInfo.Uses[fn.Sel]
				case *ast.Ident:
					callee = p.TypesInfo.Uses[fn]
				}
				if callee == nil || callee.Pkg() == nil || !isSerializer(callee.Pkg().Path()) {
					return true
				}
				checked++
				for _, arg := range call.Args {
					if at := p.TypesInfo.TypeOf(arg); at != nil && carriesSerializedModel(at, map[types.Type]bool{}) {
						found[p.Fset.Position(call.Pos()).String()+": "+callee.Pkg().Path()+"."+callee.Name()] = true
					}
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("found no serializer calls at all: the package load is not seeing the module")
	}
	if len(found) > 0 {
		var out []string
		for k := range found {
			out = append(out, k)
		}
		sort.Strings(out)
		t.Fatalf("core step/scenario models reach a serializer; only encoding/json keeps their pre-pointer sql.Null* shape:\n%s", strings.Join(out, "\n"))
	}
}
