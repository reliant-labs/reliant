package templates_test

import (
	"os"
	"strings"
	"testing"
)

// The graph's generated owned-policy input is additive: zero keeps Build,
// positive timeout opts into the runtime primitive. Read the template in-process
// so this guard is fast/cache-sound and never rewrites generated app output.
func TestGraphRollbackRemainsOptIn(t *testing.T) {
	data, err := os.ReadFile("project/graph_gen.go.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ConstructionRollbackTimeout time.Duration",
		"if g.ConstructionRollbackTimeout != 0 {",
		"g.b.BuildWithRollback(g.Ctx, root, g.ConstructionRollbackTimeout, force)",
		"return g.b.Build(root, force)",
		"g.b.Rollback(g.ConstructionRollbackTimeout)",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing graph rollback projection %q", want)
		}
	}
}
