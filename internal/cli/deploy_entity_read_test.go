package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderAndScopeEntities_UnreadableRenderRefusesTheDeploy is the
// 2026-10-09 regression. A control-plane prod deploy's entity read failed and
// the deploy carried on with no entities: no deploy groups, so no per-cluster
// scope, and the env-wide direct apply wrote every object declared for prod's
// daemon cluster onto the main cluster. The entities are what route each
// object, so a deploy that cannot read them must stop, saying so, before
// anything is applied.
func TestRenderAndScopeEntities_UnreadableRenderRefusesTheDeploy(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"the render fails":              func(t *testing.T) string { return filepath.Join(t.TempDir(), "does-not-exist.json") },
		"the render does not decode":    func(t *testing.T) string { return writeKCLFixture(t, "{not json") },
		"the render is the wrong shape": func(t *testing.T) string { return writeKCLFixture(t, `{"workloads": "not-a-list"}`) },
	}
	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("FORGE_KCL_RENDER_FIXTURE", fixture(t))
			scoped, full, err := renderAndScopeEntities(context.Background(), t.TempDir(), "prod", nil, false, nil)
			if err == nil {
				t.Fatalf("renderAndScopeEntities = (%v, %v, nil), want a refusal: a deploy with no entities has nothing to route by", scoped, full)
			}
			for _, want := range []string{`env "prod"`, "nothing was deployed"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal does not say %q: %v", want, err)
				}
			}
		})
	}
}
