package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func TestCompositionBuildTargetsExcludeUnselectedCodegenFrontends(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "p", Frontends: []config.FrontendConfig{{Name: "unselected", Type: "vite"}}}
	set, err := resolveBuildTargetSet(cfg, &KCLEntities{}, buildOptions{buildTarget: "all", composition: &compositionRun{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.frontends) != 0 {
		t.Fatalf("unselected frontend leaked into composition build: %+v", set.frontends)
	}
}
