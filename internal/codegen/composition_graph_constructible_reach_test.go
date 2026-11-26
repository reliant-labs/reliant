package codegen

import (
	"strings"
	"testing"
)

// An internal contract package with no Deps/New and no marker is refused even
// when no declared root reaches it: the test harness would construct it
// anyway, in a helpers_gen_test.go that does not compile.
func TestCheckConstructorReach_RefusesUnreachedInternalPackage(t *testing.T) {
	comp := &BuildComponent{compRoleRoot: "internal", compImportLeaf: "requirements"}
	n := &graphNode{Name: "Requirements", Kind: nodeComponent, Comp: comp}
	m := &graphModel{
		u:     loadGraphUniverse("/nonexistent", nil),
		nodes: []*graphNode{n}, byName: map[string]*graphNode{n.Name: n},
		constructorProblems: []unconstructibleComponent{{Dir: "internal/requirements", RoleRoot: "internal", Problems: []string{"no `type Deps struct`"}}},
	}
	root := []graphRoot{{Name: "api", TypeName: "APIRoot"}}
	err := m.checkConstructorReach(root, "package app\n")
	if err == nil || !strings.Contains(err.Error(), "internal/requirements") || !strings.Contains(err.Error(), "forge:not-a-component") {
		t.Fatalf("want a refusal naming internal/requirements and the markers, got %v", err)
	}
	// An unreached worker gets no harness, so it is still left out.
	comp.compRoleRoot = "internal/workers"
	m.constructorProblems[0].RoleRoot = "internal/workers"
	if err := m.checkConstructorReach(root, "package app\n"); err != nil {
		t.Fatalf("unreached worker must be left out, got %v", err)
	}
}
