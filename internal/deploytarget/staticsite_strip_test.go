package deploytarget

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStaticSiteStripRuntimeConfig: a HOSTED release must be
// environment-agnostic, so the assembled tree carries NO config.js — not even
// the dev copy `forge generate` keeps in the frontend's public/, which the
// frontend build copies into public_dir. Everything else ships unchanged.
func TestStaticSiteStripRuntimeConfig(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "app")
	writeTree(t, filepath.Join(projectDir, "admin-web", "out"), map[string]string{
		"index.html": "<html>admin</html>",
		"config.js":  "window.__FORGE_CONFIG__={env:'dev'}", // travelled in the build
	})
	writeTree(t, filepath.Join(root, "reliant-web", "dist"), map[string]string{"index.html": "<html>spa</html>"})

	fe := fakeStaticSiteFrontend()
	fe.StripRuntimeConfig = true
	plan, err := StaticSiteProvider{ProjectDir: projectDir, Runner: &fakeRunner{}}.buildPlan(fe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(plan.Stage.StagingDir) })
	if err := assembleStaging(plan.Stage); err != nil {
		t.Fatal(err)
	}
	files := readTree(t, plan.Stage.StagingDir)
	if _, ok := files["admin/config.js"]; ok {
		t.Errorf("hosted artifact ships a config.js; its runtime config is spec, written per env by the control plane: %v", keysOf(files))
	}
	if files["admin/index.html"] != "<html>admin</html>" {
		t.Errorf("stripping the runtime config dropped other files: %v", keysOf(files))
	}

	// Writing and stripping at once has no correct reading.
	fe.RuntimeConfigJS = "window.__FORGE_CONFIG__={}"
	if _, err := (StaticSiteProvider{ProjectDir: projectDir, Runner: &fakeRunner{}}).buildPlan(fe); err == nil {
		t.Error("a plan that both writes and strips config.js was accepted")
	}
}
