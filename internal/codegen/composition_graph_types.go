// File: internal/codegen/composition_graph_types.go
//
// The go/types half of the composition graph: what each node's value IS. One
// packages.Load over internal/app and every component package (a single type
// universe — go/types identity is pointer identity) gives
//
//   - the type each node produces, so a Deps field resolves to the node whose
//     value is assignable to it (identity is the Go type, never a name);
//   - each Deps field's declared type;
//   - the lifecycle methods each node's value has — Run, Drain, Close, Ready
//     with the lifecyclekit signature, or a serverkit.Worker's Start/Stop —
//     which the generated accessor binds in a lifecyclekit.Node literal. The
//     detection is here, at generate time; nothing switches on a type at run
//     time.
//
// Every lookup degrades instead of failing: a project mid-edit loads with
// type errors, and a field or node whose type did not check is simply not
// matched by type (resolution falls back to the string-keyed paths).

package codegen

import (
	"go/types"
	"path"
	"path/filepath"

	"golang.org/x/tools/go/packages"

	"github.com/reliant-labs/forge/internal/goexec"
)

// graphUniverse is the loaded type universe for one generate.
type graphUniverse struct {
	app      *packages.Package
	lookup   func(dir string) *packages.Package
	compDir  func(c BuildComponent) string
	produced map[string]types.Type // node Name → produced type
	deps     map[string]map[string]types.Type
}

// loadGraphUniverse loads internal/app plus every component package. It
// returns an empty universe (every lookup misses) when the load fails.
func loadGraphUniverse(projectDir string, comps []BuildComponent) *graphUniverse {
	u := &graphUniverse{
		lookup:   func(string) *packages.Package { return nil },
		compDir:  func(BuildComponent) string { return "" },
		produced: map[string]types.Type{},
		deps:     map[string]map[string]types.Type{},
	}
	absProject, err := filepath.Abs(projectDir)
	if err != nil {
		return u
	}
	patterns := []string{"./" + path.Join("internal", "app")}
	for _, c := range comps {
		patterns = append(patterns, "./"+path.Join(filepath.ToSlash(c.compRoleRoot), filepath.ToSlash(c.compImportLeaf)))
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedTypes |
			packages.NeedDeps | packages.NeedImports,
		Dir: absProject,
		Env: goexec.Env(),
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil || len(pkgs) == 0 {
		return u
	}
	byDir := map[string]*packages.Package{}
	for _, p := range pkgs {
		if p.Types != nil {
			byDir[filepath.Clean(packageDir(p))] = p
		}
	}
	u.lookup = func(dir string) *packages.Package {
		if p, ok := byDir[filepath.Clean(dir)]; ok {
			return p
		}
		for d, p := range byDir {
			if sameDir(d, dir) {
				return p
			}
		}
		return nil
	}
	u.compDir = func(c BuildComponent) string {
		return filepath.Join(absProject, filepath.FromSlash(c.compRoleRoot), filepath.FromSlash(c.compImportLeaf))
	}
	u.app = u.lookup(filepath.Join(absProject, "internal", "app"))
	return u
}

// typeNodes fills the produced type, Deps field types and lifecycle of every
// component and provider node it can see.
func (u *graphUniverse) typeNodes(nodes []*graphNode) {
	for _, n := range nodes {
		switch n.Kind {
		case nodeComponent:
			p := u.lookup(u.compDir(*n.Comp))
			if p == nil {
				continue
			}
			if t := constructedType(p.Types, n.Comp.compConstructor); t != nil {
				u.produced[n.Name] = t
			}
			if fields, _ := collectValidStructFieldTypes(p, "Deps"); len(fields) > 0 {
				u.deps[n.Name] = fields
			}
		case nodeProvider:
			if u.app == nil {
				continue
			}
			if t := constructedType(u.app.Types, "New"+n.Name); t != nil {
				u.produced[n.Name] = t
			}
			if fields, _ := collectValidStructFieldTypes(u.app, n.Name+"Deps"); len(fields) > 0 {
				u.deps[n.Name] = fields
			}
		}
		if t, ok := u.produced[n.Name]; ok {
			n.Lifecycle = lifecycleOf(t)
			n.NilGuard = nilable(t)
		}
	}
}

// rootFieldType is the declared type of a root struct's field, when it checked.
func (u *graphUniverse) rootFieldType(root, field string) types.Type {
	if u.app == nil {
		return nil
	}
	fields, _ := collectValidStructFieldTypes(u.app, root)
	return fields[field]
}

// lifecycleOf reads the lifecycle methods off t's method set: Run, Drain,
// Close and Ready shaped func(context.Context) error, and — for a value that
// implements serverkit.Worker (Name() string, Start and Stop) — RunContext or
// Start as its Run and Stop as its Close, which is what the worker contract
// already promises: Start blocks until its context ends, Stop runs after.
func lifecycleOf(t types.Type) lifecycle {
	ms := types.NewMethodSet(t)
	has := func(name string) bool {
		sel := ms.Lookup(nil, name)
		if sel == nil {
			// Unexported-package lookups need the method's package; the four
			// names are exported, so a nil-package lookup is exact.
			return false
		}
		sig, ok := sel.Type().(*types.Signature)
		return ok && isCtxErrSignature(sig)
	}
	var l lifecycle
	worker := hasNameString(ms) && has("Start") && has("Stop")
	switch {
	case has("Run"):
		l.Run = "Run"
	case worker && has("RunContext"):
		l.Run = "RunContext"
	case worker:
		l.Run = "Start"
	}
	if has("Drain") {
		l.Drain = "Drain"
	}
	switch {
	case has("Close"):
		l.Close = "Close"
	case worker:
		l.Close = "Stop"
	}
	if has("Ready") {
		l.Ready = "Ready"
	}
	return l
}

// isCtxErrSignature reports func(context.Context) error, ignoring the
// receiver.
func isCtxErrSignature(sig *types.Signature) bool {
	if sig.Params().Len() != 1 || sig.Results().Len() != 1 || sig.Variadic() {
		return false
	}
	return types.TypeString(sig.Params().At(0).Type(), nil) == "context.Context" &&
		types.TypeString(sig.Results().At(0).Type(), nil) == "error"
}

func hasNameString(ms *types.MethodSet) bool {
	sel := ms.Lookup(nil, "Name")
	if sel == nil {
		return false
	}
	sig, ok := sel.Type().(*types.Signature)
	return ok && sig.Params().Len() == 0 && sig.Results().Len() == 1 &&
		types.TypeString(sig.Results().At(0).Type(), nil) == "string"
}

// nilable reports whether a value of t can be nil and is compared with nil
// in Go source: a pointer or an interface.
func nilable(t types.Type) bool {
	switch t.Underlying().(type) {
	case *types.Pointer, *types.Interface:
		return true
	}
	return false
}

// producersOf returns the nodes (other than self) whose value is assignable
// to want — the identical ones alone when any is identical. An empty
// interface proves nothing and matches no producer.
func (u *graphUniverse) producersOf(want types.Type, self string, nodes []*graphNode) []*graphNode {
	if want == nil || want == types.Typ[types.Invalid] {
		return nil
	}
	if iface, ok := want.Underlying().(*types.Interface); ok && iface.Empty() {
		return nil
	}
	var identical, assignable []*graphNode
	for _, n := range nodes {
		if n.Name == self {
			continue
		}
		t, ok := u.produced[n.Name]
		if !ok {
			continue
		}
		switch {
		case types.Identical(t, want):
			identical = append(identical, n)
		case types.AssignableTo(t, want) && len(n.RootOnly) == 0:
			// A //forge:root-only node fills only a field of its very
			// type: an interface it happens to satisfy is no request
			// for that root's variant, and could hand it to a node
			// another root reaches.
			assignable = append(assignable, n)
		}
	}
	if len(identical) > 0 {
		return identical
	}
	return assignable
}
