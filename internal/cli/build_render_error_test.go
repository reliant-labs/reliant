package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderBuildKCL_SurfacesRenderFailure is the F1 regression. An env
// whose KCL FAILS a check used to be printed as "Note: skipping KCL filter"
// and built with no entity set, so `forge build <env> --target external`
// then blamed the user for not declaring build_cmd. The render error must
// be returned, verbatim.
func TestRenderBuildKCL_SurfacesRenderFailure(t *testing.T) {
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", "")
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"deploy/kcl/kcl.mod":     "[package]\nname = \"render_err_probe\"\n",
		"deploy/kcl/prod/main.k": "assert False, \"cluster_target.platform is required\"\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ents, err := renderBuildKCL(context.Background(), dir, "prod")
	if err == nil {
		t.Fatalf("a failing KCL render must fail the build, got entities %+v", ents)
	}
	if !strings.Contains(err.Error(), "cluster_target.platform is required") {
		t.Errorf("the KCL check message must reach the user verbatim; got: %v", err)
	}
}

// TestRenderBuildKCL_NoEnvDirStillBuildsUnfiltered keeps the one tolerated
// miss: a project with no deploy/kcl/<env>/ builds without an entity set.
func TestRenderBuildKCL_NoEnvDirStillBuildsUnfiltered(t *testing.T) {
	t.Setenv("FORGE_KCL_RENDER_FIXTURE", "")
	ents, err := renderBuildKCL(context.Background(), t.TempDir(), "prod")
	if err != nil || ents != nil {
		t.Fatalf("renderBuildKCL(no env dir) = (%v, %v); want (nil, nil)", ents, err)
	}
}
