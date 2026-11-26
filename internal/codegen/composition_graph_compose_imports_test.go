package codegen

import (
	"strings"
	"testing"
)

// Deleting several newX methods but leaving their imports must prune every
// now-unused known import, not panic: astutil.DeleteNamedImport shrinks
// file.Imports, so the prune loop must not range over the live slice.
func TestFixGraphComposeImports_PrunesSeveralUnusedImports(t *testing.T) {
	src := `package app

import (
	"example.com/p/internal/alpha"
	"example.com/p/internal/beta"
	"example.com/p/internal/gamma"
	"example.com/p/internal/keep"
)

func (g *Graph) newKeep() *keep.Service { return keep.New(keep.Deps{}) }
`
	node := &graphNode{Name: "X", Imports: map[string]string{
		"alpha": "example.com/p/internal/alpha",
		"beta":  "example.com/p/internal/beta",
		"gamma": "example.com/p/internal/gamma",
		"keep":  "example.com/p/internal/keep",
	}}
	r := &graphResolver{module: "example.com/p", nodes: []*graphNode{node}}
	out, err := fixGraphComposeImports(src, nil, r)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"alpha", "beta", "gamma"} {
		if strings.Contains(out, "internal/"+gone+`"`) {
			t.Errorf("unused import %s kept:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, `"example.com/p/internal/keep"`) {
		t.Errorf("used import dropped:\n%s", out)
	}
}

// A stale new<X> whose package's node was renamed (a nested package's node is
// <Parent><Leaf>) names the method compose.go should have instead.
func TestStaleGraphMethods_NamesTheRenamedNode(t *testing.T) {
	compose := "package app\n\nfunc (g *Graph) newAutomationtokens() int { return 0 }\n"
	n := &graphNode{Name: "CredentialsAutomationtokens", Kind: nodeComponent,
		Comp: &BuildComponent{ImportPath: "internal/credentials/automationtokens"}}
	got := staleGraphMethods(compose, map[string]*graphNode{n.Name: n})
	if len(got) != 1 || !strings.Contains(got[0], "rename the method to newCredentialsAutomationtokens") {
		t.Fatalf("got %q", got)
	}
	// No candidate: the old advice stands.
	got = staleGraphMethods(compose, map[string]*graphNode{})
	if len(got) != 1 || !strings.HasSuffix(got[0], "delete the method") {
		t.Fatalf("got %q", got)
	}
}
