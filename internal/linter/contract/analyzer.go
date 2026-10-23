package contract

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"

	"github.com/reliant-labs/forge/internal/codegen"
)

// Analyzer is the go/analysis analyzer registered by cmd/contractlint.
// It checks that types implementing contract interfaces (declared in
// a sibling contract.go) expose no exported methods outside that
// interface — the "single seam" rule.
//
// An interface contract.go declares only as the type of a Deps field is a
// requirement, not an offer: the package says what it NEEDS from a
// collaborator, in the narrow form the deps-are-interfaces rule asks for. A
// type that satisfies nothing but such seams is not the package's contract
// implementation, so it is not bound. Without that, declaring the seam made
// an existing type's API illegal: reliant's connauth could not take its own
// *Brokers registry (Get and Register) through a `Get`-only Deps interface,
// and turned the lookup into a func field instead. The package's provided
// contract (Service, or an interface marked //forge:service /
// //forge:contract) binds even when a Deps field also names it, as a
// decorator's `Next Service` does.
var Analyzer = &analysis.Analyzer{
	Name:     "contract",
	Doc:      "checks that types implementing contract interfaces (defined in contract.go) have no exported methods outside the interface",
	Run:      run,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

func init() {
	registerExcludeFlag(&Analyzer.Flags)
}

func run(pass *analysis.Pass) (interface{}, error) {
	// Honor forge.yaml's contracts.exclude AND the per-package
	// //forge:exclude-contract header — packages explicitly excluded by the
	// user (via either opt-out source) must be skipped before any analysis runs.
	if IsExcludedPass(pass) {
		return nil, nil
	}
	// Step 1: Find contract.go and extract interfaces from it.
	contractInterfaces := extractContractInterfaces(pass)
	if len(contractInterfaces) == 0 {
		return nil, nil
	}
	// binds reports whether a contract interface binds its implementers:
	// every one does but a Deps seam that is not also the provided contract.
	provided := providedContractNames(pass)
	seams := depsSeamNames(pass, contractInterfaces)
	binds := func(ifaceName string) bool { return provided[ifaceName] || !seams[ifaceName] }

	// Step 2: For each named type in the package, check if it implements any
	// contract interface. If so, collect the allowed method names.
	//
	// A type T implements interface I if either T or *T satisfies I.
	// We gather all implementing types along with which interface(s) they satisfy.
	//
	// A type is BOUND only when one of those interfaces binds (see binds
	// above); a type satisfying nothing but Deps seams is skipped. A bound
	// type's allowed methods are still the union of every contract interface
	// it satisfies, seams included, so this never rejects a method that was
	// allowed before.
	type implInfo struct {
		ifaceName   string
		ifaceType   *types.Interface
		methodNames map[string]bool
	}

	typeConstraints := make(map[*types.Named][]implInfo)
	bound := make(map[*types.Named]bool)

	for _, obj := range pass.TypesInfo.Defs {
		tn, ok := obj.(*types.TypeName)
		if !ok || tn.IsAlias() {
			continue
		}
		named, ok := tn.Type().(*types.Named)
		if !ok {
			continue
		}

		for ifaceName, ifaceType := range contractInterfaces {
			// Check if *T or T implements the interface.
			ptrType := types.NewPointer(named)
			if !types.Implements(named, ifaceType) && !types.Implements(ptrType, ifaceType) {
				continue
			}
			// Collect allowed method names from the interface (including embedded).
			allowed := make(map[string]bool)
			for i := 0; i < ifaceType.NumMethods(); i++ {
				allowed[ifaceType.Method(i).Name()] = true
			}
			typeConstraints[named] = append(typeConstraints[named], implInfo{
				ifaceName:   ifaceName,
				ifaceType:   ifaceType,
				methodNames: allowed,
			})
			if binds(ifaceName) {
				bound[named] = true
			}
		}
	}
	for named := range typeConstraints {
		if !bound[named] {
			delete(typeConstraints, named)
		}
	}

	if len(typeConstraints) == 0 {
		return nil, nil
	}

	// Step 3: Walk all method declarations and report violations.
	//
	// Enforcement is per-method: an exported method on an impl type is
	// allowed if it appears in AT LEAST ONE of the contract interfaces the
	// type implements. This handles three cases uniformly:
	//
	//   1. Single interface — method must be in that interface.
	//   2. Embedded interfaces (Service embeds Base, type implements both) —
	//      methods on Service automatically satisfy the union.
	//   3. Disjoint interfaces (one struct satisfies Reader and Writer with
	//      no method overlap) — Read() lives in Reader's set, Write() lives
	//      in Writer's set, both pass.
	//
	// The previous "in ALL interfaces" rule (with embedding-only pruning)
	// incorrectly rejected case 3, forcing users to split into one impl
	// struct per contract interface as a workaround.
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	nodeFilter := []ast.Node{(*ast.FuncDecl)(nil)}

	// Methods declared in generated files are exempt (see generated.go):
	// codegen output (mock methods, observability wrappers) is forge's
	// responsibility — the user cannot durably remove a method the next
	// `forge generate` re-emits. So are methods declared in test files
	// (see testfiles.go): a test fake that satisfies a contract interface
	// is not the package's API. Hand-written methods on the same types
	// are still enforced.
	exempt := exemptFilenames(pass)

	insp.Preorder(nodeFilter, func(n ast.Node) {
		funcDecl := n.(*ast.FuncDecl)

		// Only care about methods (has receiver).
		if funcDecl.Recv == nil || len(funcDecl.Recv.List) == 0 {
			return
		}

		if exempt[pass.Fset.Position(funcDecl.Pos()).Filename] {
			return
		}

		// Only care about exported methods.
		if !funcDecl.Name.IsExported() {
			return
		}

		methodName := funcDecl.Name.Name

		// Resolve the receiver's named type.
		recvType := pass.TypesInfo.TypeOf(funcDecl.Recv.List[0].Type)
		if recvType == nil {
			return
		}
		// Dereference pointer receiver.
		if ptr, ok := recvType.(*types.Pointer); ok {
			recvType = ptr.Elem()
		}
		named, ok := recvType.(*types.Named)
		if !ok {
			return
		}

		constraints, ok := typeConstraints[named]
		if !ok {
			return
		}

		// Pass if the method is declared in any of the implemented interfaces.
		var ifaceNames []string
		for _, c := range constraints {
			if c.methodNames[methodName] {
				return
			}
			ifaceNames = append(ifaceNames, c.ifaceName)
		}
		// Method is in none of the interfaces — report against the union.
		// Sort for deterministic diagnostic output (constraints is built by
		// iterating maps, so the order would otherwise be non-deterministic).
		sort.Strings(ifaceNames)
		joined := strings.Join(ifaceNames, ", ")
		if len(constraints) == 1 {
			pass.Reportf(funcDecl.Name.Pos(),
				"exported method %s on type %s is not declared in the %s interface (contract.go)",
				methodName, named.Obj().Name(), joined)
		} else {
			pass.Reportf(funcDecl.Name.Pos(),
				"exported method %s on type %s is not declared in any of the implemented contract interfaces [%s] (contract.go)",
				methodName, named.Obj().Name(), joined)
		}
	})

	return nil, nil
}

// providedContractNames returns the contract.go interfaces that are the
// package's PROVIDED contract: one named Service, or one carrying the
// //forge:service / //forge:contract marker (codegen.HasServiceContractMarker,
// the same reading codegen and the contract-name lint use).
func providedContractNames(pass *analysis.Pass) map[string]bool {
	out := map[string]bool{}
	for _, file := range pass.Files {
		if filepath.Base(pass.Fset.Position(file.Pos()).Filename) != "contract.go" {
			continue
		}
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if _, isIface := ts.Type.(*ast.InterfaceType); !isIface {
					continue
				}
				if ts.Name.Name == "Service" || codegen.HasServiceContractMarker(genDecl.Doc, ts.Doc, ts.Comment) {
					out[ts.Name.Name] = true
				}
			}
		}
	}
	return out
}

// depsSeamNames returns the contract interfaces the package's Deps struct
// holds: a field's type, or the element of a pointer, slice, array, map or
// channel field. These are what the package consumes. A func-typed field's
// signature is not followed — its parameters are values the package hands
// OUT, which is no evidence of a requirement.
func depsSeamNames(pass *analysis.Pass, contractInterfaces map[string]*types.Interface) map[string]bool {
	out := map[string]bool{}
	var walk func(t types.Type)
	walk = func(t types.Type) {
		switch u := t.(type) {
		case *types.Alias:
			walk(types.Unalias(u))
		case *types.Named:
			obj := u.Obj()
			if obj.Pkg() == pass.Pkg && contractInterfaces[obj.Name()] != nil {
				out[obj.Name()] = true
			}
		case *types.Pointer:
			walk(u.Elem())
		case *types.Slice:
			walk(u.Elem())
		case *types.Array:
			walk(u.Elem())
		case *types.Map:
			walk(u.Key())
			walk(u.Elem())
		case *types.Chan:
			walk(u.Elem())
		}
	}
	for _, file := range pass.Files {
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name.Name != "Deps" {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range st.Fields.List {
					if t := pass.TypesInfo.TypeOf(field.Type); t != nil {
						walk(t)
					}
				}
			}
		}
	}
	return out
}

// extractContractInterfaces scans the package for a file named "contract.go"
// and returns all interface types declared in it, keyed by name.
func extractContractInterfaces(pass *analysis.Pass) map[string]*types.Interface {
	result := make(map[string]*types.Interface)

	for _, file := range pass.Files {
		filename := pass.Fset.Position(file.Pos()).Filename
		if filepath.Base(filename) != "contract.go" {
			continue
		}

		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.TYPE {
				continue
			}
			for _, spec := range genDecl.Specs {
				typeSpec := spec.(*ast.TypeSpec)
				if _, ok := typeSpec.Type.(*ast.InterfaceType); !ok {
					continue
				}
				// Look up the types.Interface from the type checker.
				obj := pass.TypesInfo.Defs[typeSpec.Name]
				if obj == nil {
					continue
				}
				tn, ok := obj.(*types.TypeName)
				if !ok {
					continue
				}
				iface, ok := tn.Type().Underlying().(*types.Interface)
				if !ok {
					continue
				}
				// Only consider exported interfaces — contract interfaces are
				// part of the public API of the package.
				if !strings.HasPrefix(typeSpec.Name.Name, strings.ToUpper(typeSpec.Name.Name[:1])) {
					continue
				}
				result[typeSpec.Name.Name] = iface
			}
		}
	}

	return result
}
