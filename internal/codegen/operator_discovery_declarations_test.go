package codegen

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGraphConstructorValidationScopesHelpersWithoutChangingOperatorCatalogue(t *testing.T) {
	dir := t.TempDir()
	write := func(name, file, source string) {
		t.Helper()
		p := filepath.Join(dir, "internal/operators", name)
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, file), []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("fleet", "operator.go", `package fleet
 type Deps struct{}
 type Reconciler struct{}
 func New(Deps) *Reconciler { return nil }
 `)
	// Sibling reconcilers are collaborators of fleet, not standalone forge
	// operators. Their constructors take owner-specific resources, not Deps.
	write("volume", "controller.go", `package volume
 type Reconciler struct{}
 func (r *Reconciler) SetupWithManager(any) error { return nil }
 `)
	write("shared", "quota.go", `package shared
 type Quota struct{}
 func Check(Quota) bool { return true }
 `)
	write("resources", "types.go", `package resources
 type VolumeSpec struct{ Size string }
 `)
	// A declared operator whose constructor was broken must remain visible;
	// constructibility, not discovery, reports the missing New.
	write("broken", "operator.go", `package broken
 type Controller struct{}
 type Deps struct{}
 `)
	write("custom", "operator.go", `package custom
 type Deps struct{}
 type Runner struct{}
 // forge:constructor
 func Build(Deps) *Runner { return nil }
 `)
	got := DiscoverOperatorSpecs(dir)
	var names []string
	for _, spec := range got {
		names = append(names, spec.Name)
	}
	if want := []string{"broken", "custom", "fleet", "resources", "shared", "volume"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("legacy catalogue changed: got %v want %v", names, want)
	}
	ops, err := OperatorDataFromSpecs(got, dir)
	if err != nil {
		t.Fatal(err)
	}
	comps, err := assembleBuildComponents(InjectGenInput{GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/test"}, Operators: ops})
	if err != nil {
		t.Fatal(err)
	}
	bad := unconstructibleComponents(dir, comps)
	if len(bad) != 4 {
		t.Fatalf("legacy constructor validation must still catch every broken catalogue entry, got %+v", bad)
	}
	model := &graphModel{u: loadGraphUniverse("/nonexistent", nil), constructorProblems: bad, byName: map[string]*graphNode{}}
	for i := range comps {
		node := componentNode("example.com/test", &comps[i])
		model.nodes = append(model.nodes, node)
		model.byName[node.Name] = node
	}
	root := []graphRoot{{Name: "fleet", TypeName: "FleetRoot", Fields: []graphRootField{{Name: "Operator", Type: "*fleet.Reconciler"}}}}
	if err := model.checkConstructorReach(root, "package app\n"); err != nil {
		t.Fatalf("unreached helpers made an explicit real operator root unconstructible: %v", err)
	}
	// Run the same real root/reconciliation scope, rather than checking only
	// the predicate: helper candidates must not get constructor expressions.
	model.compNode = map[string]*graphNode{}
	resolver := &graphResolver{module: "example.com/test", nodes: model.nodes, byName: model.byName, u: model.u}
	if err := model.wire(GraphGenInput{GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/test"}}, resolver, root); err != nil {
		t.Fatalf("wire explicit real operator root: %v", err)
	}
	for _, name := range []string{"Broken", "Resources", "Shared", "Volume"} {
		if _, out := model.unbuilt[name]; !out {
			t.Errorf("unconstructible helper %s must be left out, got %v", name, model.unbuilt)
		}
	}
	if err := model.validateReachedConstructors(root); err != nil {
		t.Fatalf("final validation of the wired fleet root: %v", err)
	}
	root[0].Fields[0].Type = "*broken.Controller"
	if err := model.checkConstructorReach(root, "package app\n"); err == nil {
		t.Fatal("reached malformed operator must fail")
	}
	// The final check measures reach again over the final compose.go, not the
	// unbuilt set an earlier draft decided: Broken is still marked left out,
	// but a root now reaches it.
	if _, out := model.unbuilt["Broken"]; !out {
		t.Fatalf("fixture: Broken must still be marked unbuilt, got %v", model.unbuilt)
	}
	if err := model.validateReachedConstructors(root); err == nil {
		t.Fatal("a malformed component left out by an earlier draft but reached in the final graph must fail")
	}
	if err := model.checkConstructorReach(nil, "package app\n"); err == nil {
		t.Fatal("default root must keep validating every catalogue component")
	}

}
