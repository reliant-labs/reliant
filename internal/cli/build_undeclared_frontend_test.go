package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// `forge env build <env>` builds the frontends the env declares, not every
// frontends/<name> on disk (bug 194a8475). reliant's `release` env declares
// no frontend, yet a bare `forge env build release` ran a full `npm run
// build` of frontends/web: filterFrontendsForBuild kept every inventory
// frontend the render did not mention, so the env had to pass --target to
// avoid a build nothing in it consumed.
func TestResolveBuildTargetSet_SkipsFrontendTheEnvDoesNotDeclare(t *testing.T) {
	cfg := &config.ProjectConfig{
		Name:      "reliant",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "vite"}.WithDir("frontends/web")},
	}

	t.Run("an env that declares no frontend builds none", func(t *testing.T) {
		set, err := resolveBuildTargetSet(cfg, &KCLEntities{}, buildOptions{buildTarget: "all", env: "release"})
		if err != nil {
			t.Fatalf("resolveBuildTargetSet: %v", err)
		}
		if len(set.frontends) != 0 {
			t.Errorf("frontends = %+v, want none: env %q declares no frontend", set.frontends, "release")
		}
	})

	t.Run("an env that declares the frontend still builds it", func(t *testing.T) {
		entities := &KCLEntities{Frontends: []FrontendEntity{{
			Name: "web", Type: "vite", Path: "frontends/web",
			Runtime: FrontendRuntime{Type: FrontendRuntimeBuildOnly},
		}}}
		set, err := resolveBuildTargetSet(cfg, entities, buildOptions{buildTarget: "all", env: "prod"})
		if err != nil {
			t.Fatalf("resolveBuildTargetSet: %v", err)
		}
		if len(set.frontends) != 1 || set.frontends[0].Name != "web" {
			t.Errorf("frontends = %+v, want [web]", set.frontends)
		}
	})

	t.Run("--target naming the frontend builds it anyway", func(t *testing.T) {
		set, err := resolveBuildTargetSet(cfg, &KCLEntities{}, buildOptions{buildTarget: "web", env: "release"})
		if err != nil {
			t.Fatalf("resolveBuildTargetSet: %v", err)
		}
		if len(set.frontends) != 1 || set.frontends[0].Name != "web" {
			t.Errorf("frontends = %+v, want [web]: an explicit --target is honoured", set.frontends)
		}
	})

	t.Run("no env renders nothing to filter on", func(t *testing.T) {
		set, err := resolveBuildTargetSet(cfg, nil, buildOptions{buildTarget: "all"})
		if err != nil {
			t.Fatalf("resolveBuildTargetSet: %v", err)
		}
		if len(set.frontends) != 1 {
			t.Errorf("frontends = %+v, want [web]: `forge build` with no env builds the inventory", set.frontends)
		}
	})
}
