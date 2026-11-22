package cli

import (
	"errors"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// The inventory renders every env early in the run, before per-env deploy
// config writes the config_gen.k / config.k those envs import. On a fresh
// project that early render fails, and the observability step (which refuses
// to derive from a failed render) used to read the cached failure after its
// cause was gone: `forge project new` failed its initial generate (forge
// main-red after #2258). A step that writes an imported module drops a failed
// cache; a good one is kept, so a healthy project still renders once.
func TestForgetFailedRenders_DropsAFailedCacheKeepsAGoodOne(t *testing.T) {
	failed := &pipelineContext{
		renderedRead: true,
		rendered: renderedEnvironments{failed: []envRenderFailure{
			{Env: "dev", Err: errors.New("evaluate KCL: exit status 1")},
		}},
	}
	failed.forgetFailedRenders()
	if failed.renderedRead || len(failed.rendered.failed) != 0 {
		t.Errorf("a cache holding a failed render was kept (renderedRead=%v, failed=%d); the next step would read a failure whose cause is gone",
			failed.renderedRead, len(failed.rendered.failed))
	}

	good := &pipelineContext{
		renderedRead: true,
		rendered:     renderedEnvironments{workloads: []WorkloadEntity{{Name: "api"}}},
	}
	good.forgetFailedRenders()
	if !good.renderedRead || len(good.rendered.workloads) != 1 {
		t.Error("a cache with no failed render was dropped; a healthy project would render its envs twice")
	}
}

// The per-env deploy config step is the one that writes the modules a fresh
// project's envs import, so it is where a failed early render is forgotten.
func TestPerEnvDeployConfig_ForgetsAFailedRender(t *testing.T) {
	dir := t.TempDir()
	ctx := &pipelineContext{
		Cfg:          &config.ProjectConfig{Name: "demo"},
		ProjectDir:   dir,
		AbsPath:      dir,
		renderedRead: true,
		rendered: renderedEnvironments{failed: []envRenderFailure{
			{Env: "dev", Err: errors.New("CannotFindModule: config_gen")},
		}},
	}
	_ = stepPerEnvDeployConfig(ctx)
	if ctx.renderedRead {
		t.Error("per-env deploy config kept a failed render cached; the observability step would fail on it")
	}
}
