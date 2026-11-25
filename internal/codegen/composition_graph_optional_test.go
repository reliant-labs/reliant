package codegen

import (
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// optionalFixture: a ChangeController provider that is optional, consumed by
// a Tools handler whose Deps.Changes takes it.
func optionalComponentFixture(t *testing.T, fieldOptional bool) *graphModel {
	t.Helper()
	pkg := types.NewPackage("example.com/x/internal/tools", "tools")
	iface := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "ChangeController", nil), types.NewInterfaceType(nil, nil), nil)
	cc := &graphNode{Name: "ChangeController", LogName: "change-controller", Kind: nodeProvider, Type: "tools.ChangeController", File: "internal/app/change_controller.go", Line: 7, Optional: true}
	tools := &graphNode{Name: "Tools", LogName: "tools", Kind: nodeComponent, Comp: &BuildComponent{ImportPath: "internal/handlers/tools"},
		Deps: []DepsField{{Name: "Changes", Type: "tools.ChangeController", Optional: fieldOptional}, {Name: "Name", Type: "string"}}}
	m := &graphModel{
		byName: map[string]*graphNode{"ChangeController": cc, "Tools": tools},
		nodes:  []*graphNode{cc, tools},
		u: &graphUniverse{
			produced: map[string]types.Type{"ChangeController": iface},
			deps:     map[string]map[string]types.Type{"Tools": {"Changes": iface, "Name": types.Typ[types.String]}},
		},
		edges: map[string][]string{"Tools": {"ChangeController"}},
	}
	return m
}

func TestOptionalComponentConsumerMustBeOptionalDep(t *testing.T) {
	fs := optionalComponentFixture(t, false).optionalComponentFindings()
	if len(fs) != 1 {
		t.Fatalf("findings = %+v, want one", fs)
	}
	f := fs[0]
	if f.Rule != GraphRuleOptionalComponent || !f.Error || f.File != "internal/handlers/tools" ||
		!strings.Contains(f.Message, "Deps.Changes") || !strings.Contains(f.Fix, "forge:optional-dep") {
		t.Fatalf("finding = %+v", f)
	}
	if fs := optionalComponentFixture(t, true).optionalComponentFindings(); len(fs) != 0 {
		t.Fatalf("an optional-dep consumer is clean, got %+v", fs)
	}
}

func TestOptionalComponentWithoutMarkerIsUnaffected(t *testing.T) {
	m := optionalComponentFixture(t, false)
	m.byName["ChangeController"].Optional = false
	if fs := m.optionalComponentFindings(); len(fs) != 0 {
		t.Fatalf("a non-optional producer yields no finding, got %+v", fs)
	}
}

func TestOptionalComponentMustBeNilable(t *testing.T) {
	m := optionalComponentFixture(t, true)
	if err := m.checkOptionalNilable(); err != nil {
		t.Fatal(err)
	}
	if !m.byName["ChangeController"].NilGuard {
		t.Fatal("an optional node's lifecycle literal must be nil-checked")
	}
	pkg := types.NewPackage("example.com/x/internal/app", "app")
	m.u.produced["ChangeController"] = types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Value", nil), types.NewStruct(nil, nil), nil)
	if err := m.checkOptionalNilable(); err == nil || !strings.Contains(err.Error(), "cannot be nil") {
		t.Fatalf("a struct-valued optional node must be refused, got %v", err)
	}
}

func TestOptionalWrappedComponentReturnsNilUnwrapped(t *testing.T) {
	n := &graphNode{Name: "OrgMove", Type: "orgmove.Service", Ctor: "orgmove.New", DepsType: "orgmove.Deps", Doc: "orgmove",
		Wrapped: true, MiddlewareCtor: "NewObserved", Optional: true, Fallible: true, Comp: &BuildComponent{Alias: "orgmove"}}
	got := renderGraphMethod(n, nil)
	if !strings.Contains(got, "if err != nil || svc == nil {\n\t\treturn nil, err\n\t}\n\treturn orgmove.NewObserved(svc), nil") {
		t.Fatalf("fallible optional wrapped:\n%s", got)
	}
	n.Fallible = false
	got = renderGraphMethod(n, nil)
	if !strings.Contains(got, "\tsvc := orgmove.New(") || !strings.Contains(got, "if svc == nil {\n\t\treturn nil, nil\n\t}\n\treturn orgmove.NewObserved(svc), nil") {
		t.Fatalf("infallible optional wrapped:\n%s", got)
	}
	n.Optional = false
	if got := renderGraphMethod(n, nil); strings.Contains(got, "svc == nil") {
		t.Fatalf("a non-optional component is rendered as before:\n%s", got)
	}
}

func TestOptionalComponentDirectiveIsRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "doc.go"), []byte("// Package orgmove moves orgs.\n//\n//forge:optional-component: nil without a control plane\npackage orgmove\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, ok := FindOptionalComponentMarker(dir)
	if !ok || m.Reason != "nil without a control plane" {
		t.Fatalf("marker = %+v, %v", m, ok)
	}
}

// writeProject writes files (project-relative path → content) under a temp dir.
func writeOptionalProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const optionalProviderFile = `package app

type OrgMoveDeps struct{}

// NewOrgMove is a provider of the same name as the component.
//
%s
func NewOrgMove(d OrgMoveDeps) (*int, error) { return nil, nil }

// NewOther is provider-shaped, and its marker opts in only node Other.
//
//forge:optional-component: other is absent self-hosted
func NewOther(d OtherDeps) (*int, error) { return nil, nil }

type OtherDeps struct{}
`

// [B1] A component's optionality is its package marker; a same-name
// provider marked differently never overrides it, in either direction.
func TestOptionalComponentPackageMarkerIsNotOverriddenByProvider(t *testing.T) {
	for _, tc := range []struct {
		name, pkgDoc, providerDoc string
		want                      bool
	}{
		{"package marked, provider unmarked", "//forge:optional-component: nil without a control plane\n", "// plain doc", true},
		{"package unmarked, provider marked", "", "//forge:optional-component: provider says so", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeOptionalProject(t, map[string]string{
				"internal/orgmove/doc.go":   tc.pkgDoc + "package orgmove\n",
				"internal/app/providers.go": fmt.Sprintf(optionalProviderFile, tc.providerDoc),
			})
			c := &BuildComponent{FieldName: "OrgMove", compRoleRoot: "internal", compImportLeaf: "orgmove"}
			if got := componentOptional(dir, c); got != tc.want {
				t.Fatalf("component optional = %v, want %v", got, tc.want)
			}
			providers, _, _, err := discoverAppDeclsAndGroups(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range providers {
				if p.Name == "OrgMove" && p.Optional != !tc.want {
					t.Fatalf("provider OrgMove optional = %v; its own doc decides it", p.Optional)
				}
			}
		})
	}
}

// [B1] A provider-only marker opts in exactly the node its New<X> builds,
// needs a reason, and a bare one is an error naming the func.
func TestOptionalComponentProviderOnlyMarker(t *testing.T) {
	dir := writeOptionalProject(t, map[string]string{
		"internal/app/providers.go": fmt.Sprintf(optionalProviderFile, "// plain doc"),
	})
	providers, _, _, err := discoverAppDeclsAndGroups(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range providers {
		got[p.Name] = p.Optional
	}
	if !got["Other"] || got["OrgMove"] {
		t.Fatalf("optional providers = %v, want Other only", got)
	}
	dir = writeOptionalProject(t, map[string]string{
		"internal/app/providers.go": fmt.Sprintf(optionalProviderFile, "//forge:optional-component"),
	})
	if _, _, _, err := discoverAppDeclsAndGroups(dir); err == nil || !strings.Contains(err.Error(), "NewOrgMove") || !strings.Contains(err.Error(), "<why>") {
		t.Fatalf("a bare provider marker must be refused naming the func, got %v", err)
	}
}

// [e5e9b7da] An optional variant wrapper whose consumer field takes the
// inner value through the wrapper's accessor (compose.go writes
// `g.CallerControlPlane().Client()`, directly or through a local) is a field
// the optional node fills, though the wrapper's type is not the field's.
func TestOptionalComponentFollowsVariantAccessorToConsumerField(t *testing.T) {
	pkg := types.NewPackage("example.com/x/internal/app", "app")
	wrapper := types.NewPointer(types.NewNamed(types.NewTypeName(token.NoPos, pkg, "CallerControlPlane", nil), types.NewStruct(nil, nil), nil))
	inner := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Client", nil), types.NewInterfaceType([]*types.Func{
		types.NewFunc(token.NoPos, pkg, "Call", types.NewSignatureType(nil, nil, nil, nil, nil, false)),
	}, nil).Complete(), nil)
	if types.AssignableTo(wrapper, inner) {
		t.Fatal("fixture: the wrapper must not satisfy the inner type, or the type check alone finds the field")
	}
	for _, tc := range []struct{ name, body string }{
		{"accessor chain", "return tools.New(tools.Deps{CP: g.CallerControlPlane().Client(), Name: \"x\"})"},
		{"nested in a call argument", "return tools.New(tools.Deps{CP: wrap(g.CallerControlPlane().Client().Sub()), Name: \"x\"})"},
		{"through a local", "cp := g.CallerControlPlane()\n\treturn tools.New(tools.Deps{CP: cp.Client(), Name: \"x\"})"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := func(fieldOptional bool) *graphModel {
				v := &graphNode{Name: "CallerControlPlane", LogName: "caller-control-plane", Kind: nodeProvider, File: "internal/app/variant_caller_controlplane.go", Optional: true}
				tools := &graphNode{Name: "Tools", LogName: "tools", Kind: nodeComponent, Comp: &BuildComponent{ImportPath: "internal/handlers/tools"},
					Deps: []DepsField{{Name: "CP", Optional: fieldOptional}, {Name: "Name"}}}
				return &graphModel{
					byName: map[string]*graphNode{"CallerControlPlane": v, "Tools": tools},
					nodes:  []*graphNode{v, tools},
					u: &graphUniverse{
						produced: map[string]types.Type{"CallerControlPlane": wrapper},
						deps:     map[string]map[string]types.Type{"Tools": {"CP": inner, "Name": types.Typ[types.String]}},
					},
					edges:   map[string][]string{"Tools": {"CallerControlPlane"}},
					compose: "package app\n\nfunc (g *Graph) newTools() tools.Service {\n\t" + tc.body + "\n}\n",
				}
			}
			fs := build(false).optionalComponentFindings()
			if len(fs) != 1 || !strings.Contains(fs[0].Message, "Deps.CP") {
				t.Fatalf("findings = %+v, want one naming Deps.CP", fs)
			}
			if fs := build(true).optionalComponentFindings(); len(fs) != 0 {
				t.Fatalf("an optional-dep consumer is clean, got %+v", fs)
			}
		})
	}
}
