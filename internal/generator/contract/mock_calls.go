package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Typed per-method call records on every generated mock.
//
// contractkit.Recorder keeps each call as a Call{Args []any}, so asserting
// "PublishAssignment ran under epoch 9" meant `calls[0].Args[1].(store.Lease)`:
// a positional type assertion that still compiles, and silently asserts the
// wrong argument, after a parameter is added in front of it. The mock knows
// every parameter's name and type, so it also emits, per method:
//
//	type MockStorePublishAssignmentCall struct {
//		Ctx        context.Context
//		Lease      store.Lease
//		Assignment *sharedv1.Assignment
//		Version    int
//	}
//
//	func (m *MockStore) PublishAssignmentCalls() []MockStorePublishAssignmentCall
//
// A parameter added to the contract becomes a new field, and a renamed or
// retyped one breaks the test at compile time, where it belongs.
//
// The record holds EVERY parameter, context included. A record that drops
// one is lossy (a test asserting the caller's deadline or the principal a
// context carries has nothing to read), it would disagree with Call.Args
// positionally, and deciding what "is a context" by its spelling misses an
// alias. So Ctx is a field like any other, and costs a test nothing to ignore.
//
// Recorder.Calls and Call.Args are untouched: the accessor reads them.

// mockInterface is an interface as the mock template renders it: the
// contract's InterfaceDef plus each method's call record.
type mockInterface struct {
	InterfaceDef
	Methods []mockMethod
}

// mockMethod is one method as the mock template renders it. MethodDef is
// the body-scoped copy (withBodyScopedNames), whose parameter names are
// safe inside a method body; Calls is named from the contract's own names.
type mockMethod struct {
	MethodDef
	Calls callRecord
}

// callRecord is everything the template spells for one method's typed
// call record.
type callRecord struct {
	// Accessor is the mock method that returns the records:
	// <Method>Calls, unless the mock already has a member of that name.
	Accessor string
	// Type is the record's package-level type name:
	// <MockName><Method>Call, unless the package already declares it.
	Type string
	// Fields holds one field per contract parameter, in order.
	Fields []callField
	// Out, Call and Rec are the accessor body's locals, chosen so that no
	// package qualifier a field type spells is shadowed by one.
	Out, Call, Rec string
}

// callField is one parameter of a call record.
type callField struct {
	Name string // exported field name
	Type string // the parameter's type; a variadic ...T is the []T it was recorded as
}

// recorderMembers are the names contractkit.Recorder promotes onto every
// mock. An accessor of one of these names would be legal Go — it shadows the
// promoted member — but would silently change what `m.Calls("X")` means.
var recorderMembers = []string{"Recorder", "Record", "Calls", "CallCount", "Reset"}

// buildMockInterfaces pairs each interface's body-scoped methods with its
// call records. original holds the contract's interfaces with their own
// parameter names (field names come from those); scoped holds the copies
// whose names withBodyScopedNames made safe for a method body.
// packageNames is every identifier the package declares outside
// mock_gen.go, which a record type must not redeclare.
func buildMockInterfaces(original, scoped []InterfaceDef, packageNames map[string]bool) []mockInterface {
	// Type names are package-scoped, so they are claimed across every mock
	// at once: the package's own declarations and every mock type first,
	// then each record type in render order.
	takenTypes := make(map[string]bool, len(packageNames)+len(original))
	for name := range packageNames {
		takenTypes[name] = true
	}
	for _, iface := range original {
		takenTypes[iface.MockName()] = true
	}

	out := make([]mockInterface, len(original))
	for i, iface := range original {
		// Accessor names are scoped to one mock: its methods, its Func
		// fields and the Recorder's promoted members are claimed before
		// any accessor, so a contract method literally named FooCalls
		// keeps its name and Foo's accessor moves aside.
		takenMembers := make(map[string]bool, 2*len(iface.Methods)+len(recorderMembers))
		for _, name := range recorderMembers {
			takenMembers[name] = true
		}
		for _, m := range iface.Methods {
			takenMembers[m.Name] = true
			takenMembers[m.Name+"Func"] = true
		}

		out[i] = mockInterface{InterfaceDef: scoped[i], Methods: make([]mockMethod, len(iface.Methods))}
		for j, m := range iface.Methods {
			rec := callRecord{
				Accessor: claimName(m.Name+"Calls", takenMembers),
				Type:     claimName(iface.MockName()+m.Name+"Call", takenTypes),
				Fields:   callFields(m.Params),
			}
			// The locals must not shadow a qualifier the field types
			// spell (an import aliased `out` is legal), nor the receiver.
			reserved := map[string]bool{mockReceiver: true}
			for _, p := range m.Params {
				for _, id := range typeExprIdents(p.TypeExpr) {
					reserved[id] = true
				}
			}
			rec.Out = claimName("out", reserved)
			rec.Call = claimName("c", reserved)
			rec.Rec = claimName("r", reserved)
			out[i].Methods[j] = mockMethod{MethodDef: scoped[i].Methods[j], Calls: rec}
		}
	}
	return out
}

// callFields names one exported field per parameter. A named parameter's
// field is its name with the first letter upper-cased (jobID → JobID,
// ctx → Ctx; a leading initialism whole, id → ID). An unnamed or blank parameter, or one whose name cannot be
// exported (_x), takes P<index> — the field twin of the p<index> the mock
// method's signature already gives it. Named fields claim their names
// first, so a positional name can never displace a contract name; a
// remaining clash (x and X) moves the later field aside with a trailing _,
// as withBodyScopedNames does for parameters.
func callFields(params []ParamDef) []callField {
	fields := make([]callField, len(params))
	taken := map[string]bool{}
	positional := make([]bool, len(params))
	for i, p := range params {
		fields[i].Type = p.TypeExpr
		if p.Variadic {
			fields[i].Type = "[]" + strings.TrimPrefix(p.TypeExpr, "...")
		}
		name, ok := exportedName(p.Name)
		if !ok {
			positional[i] = true
			continue
		}
		fields[i].Name = claimName(name, taken)
	}
	for i := range params {
		if positional[i] {
			fields[i].Name = claimName(fmt.Sprintf("P%d", i), taken)
		}
	}
	return fields
}

// exportedName upper-cases name's first letter, reporting false when the
// result is not an exported identifier (empty, blank, or a leading _). A
// leading lower-case word that is a Go initialism is upper-cased whole, the
// way Go spells it in an exported name: id → ID, urlPath → URLPath.
func exportedName(name string) (string, bool) {
	if name == "" || name == "_" {
		return "", false
	}
	word := name
	if i := strings.IndexFunc(name, func(r rune) bool { return !unicode.IsLower(r) }); i >= 0 {
		word = name[:i]
	}
	if goInitialisms[word] {
		return strings.ToUpper(word) + name[len(word):], true
	}
	r, size := utf8.DecodeRuneInString(name)
	if !unicode.IsLetter(r) {
		return "", false
	}
	exported := string(unicode.ToUpper(r)) + name[size:]
	if !ast.IsExported(exported) {
		return "", false
	}
	return exported, true
}

// goInitialisms are the lower-case words Go style writes in capitals
// (staticcheck ST1003's list, less the ones no parameter name starts with).
var goInitialisms = map[string]bool{
	"acl": true, "api": true, "ascii": true, "cpu": true, "css": true, "dns": true,
	"eof": true, "guid": true, "html": true, "http": true, "https": true, "id": true,
	"ip": true, "json": true, "qps": true, "ram": true, "rpc": true, "sla": true,
	"smtp": true, "sql": true, "ssh": true, "tcp": true, "tls": true, "ttl": true,
	"udp": true, "ui": true, "uid": true, "uuid": true, "uri": true, "url": true,
	"vm": true, "xml": true, "xmpp": true, "xsrf": true, "xss": true,
}

// claimName returns name, or name with trailing underscores until it is free
// in taken, and marks the result taken.
func claimName(name string, taken map[string]bool) string {
	for taken[name] {
		name += "_"
	}
	taken[name] = true
	return name
}

// collectPackageScopeNames returns every package-level identifier the
// package in dir declares in any file except mock_gen.go: types, funcs,
// vars and consts, including those of its in-package _test.go files, which
// share the scope when the tests build. mock_gen.go is excluded because it
// is the file being rendered — reading its own record types back would move
// every one of them aside on the next run. Read or parse failures abort the
// scan: an incomplete set cannot guarantee collision-free declarations.
func collectPackageScopeNames(dir, pkg string) (map[string]bool, error) {
	names := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("scan package scope in %s: %w", dir, err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || entry.Name() == mockFileName {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("scan package scope in %s: %w", path, err)
		}
		if f.Name.Name != pkg {
			continue // an external foo_test package
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					names[d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						names[s.Name.Name] = true
					case *ast.ValueSpec:
						for _, n := range s.Names {
							names[n.Name] = true
						}
					}
				}
			}
		}
	}
	return names, nil
}
