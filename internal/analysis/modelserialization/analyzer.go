// Package modelserialization checks statically identifiable serialization of
// Reliant's step/scenario storage models. It consumes the complete type info
// supplied by go vet; it does not load packages, create stubs, or ignore errors.
//
//forge:exclude-contract: pure go/analysis rule, not a runtime component
package modelserialization

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/types/typeutil"
)

// Analyzer rejects model payloads passed to non-JSON serializers. It is not a
// dataflow analysis: dynamic interface contents, reflection, serializer function
// variables and application wrapper calls are outside its bounded guarantee.
// RunDespiteErrors remains false: vet must reject broken/partially typed input.
var Analyzer = &analysis.Analyzer{
	Name: "modelserialization",
	Doc:  "reject statically identifiable step/scenario models at non-JSON serialization calls",
	Run:  run,
}

const corePath = "github.com/reliant-labs/reliant/internal/db/core"

func serializer(pkg string) bool {
	// JSON deliberately stays allowed: core/legacy_json.go preserves its public
	// sql.Null* representation, including when a model sits behind an interface.
	switch pkg {
	case "encoding/gob", "encoding/xml", "gopkg.in/yaml.v2", "gopkg.in/yaml.v3",
		"go.yaml.in/yaml/v2", "go.yaml.in/yaml/v3", "go.yaml.in/yaml/v4",
		"github.com/goccy/go-yaml", "sigs.k8s.io/yaml",
		"google.golang.org/protobuf/encoding/protojson":
		return true
	}
	return false
}

func payloadMethod(name string) bool {
	switch name {
	case "Marshal", "MarshalIndent", "MarshalAppend", "MarshalWithOptions",
		"Unmarshal", "UnmarshalWithOptions", "UnmarshalStrict",
		"Encode", "EncodeElement", "Decode", "DecodeElement", "DecodeValue", "EncodeValue":
		return true
	}
	return false
}

func containsModel(t types.Type, seen map[types.Type]bool) bool {
	t = types.Unalias(t)
	if seen[t] {
		return false
	}
	seen[t] = true
	switch t := t.(type) {
	case *types.Named:
		o := t.Obj()
		if o.Pkg() != nil && o.Pkg().Path() == corePath {
			switch o.Name() {
			case "StepExecution", "ChatStepExecution", "WorkflowScenario":
				return true
			}
		}
		return containsModel(t.Underlying(), seen)
	case *types.Pointer:
		return containsModel(t.Elem(), seen)
	case *types.Slice:
		return containsModel(t.Elem(), seen)
	case *types.Array:
		return containsModel(t.Elem(), seen)
	case *types.Map:
		return containsModel(t.Key(), seen) || containsModel(t.Elem(), seen)
	case *types.Struct:
		for i := 0; i < t.NumFields(); i++ {
			// Conservative across serializer tag dialects: json:"-" does not
			// hide a field from XML/YAML. Inspect every exported/embedded field.
			f := t.Field(i)
			if (f.Exported() || f.Embedded()) && containsModel(f.Type(), seen) {
				return true
			}
		}
	}
	return false
}

func identifiable(pass *analysis.Pass, e ast.Expr) bool {
	if t := pass.TypesInfo.TypeOf(e); t != nil && containsModel(t, map[types.Type]bool{}) {
		return true
	}
	switch e := e.(type) {
	case *ast.ParenExpr:
		return identifiable(pass, e.X)
	case *ast.UnaryExpr:
		return identifiable(pass, e.X)
	case *ast.CompositeLit:
		for _, v := range e.Elts {
			if kv, ok := v.(*ast.KeyValueExpr); ok {
				if identifiable(pass, kv.Key) {
					return true
				}
				v = kv.Value
			}
			if identifiable(pass, v) {
				return true
			}
		}
	case *ast.CallExpr:
		// Explicit interface conversions at the boundary, not arbitrary calls.
		if tv, ok := pass.TypesInfo.Types[e.Fun]; ok && tv.IsType() {
			for _, a := range e.Args {
				if identifiable(pass, a) {
					return true
				}
			}
		}
	}
	return false
}

func run(pass *analysis.Pass) (any, error) {
	for _, f := range pass.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn := typeutil.StaticCallee(pass.TypesInfo, call)
			if fn == nil || fn.Pkg() == nil || !serializer(fn.Pkg().Path()) || !payloadMethod(fn.Name()) {
				return true
			}
			for _, a := range call.Args {
				if identifiable(pass, a) {
					pass.Reportf(call.Pos(), "step/scenario model at %s.%s: project into a transport type; only encoding/json preserves the legacy model shape", strings.TrimPrefix(fn.Pkg().Path(), "encoding/"), fn.Name())
					break
				}
			}
			return true
		})
	}
	return nil, nil
}
