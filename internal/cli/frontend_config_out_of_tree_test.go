package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// A frontend whose code lives in ANOTHER repository — a sibling checkout in
// dev (`path: "../reliant/web"`) — used to get no runtime config document at
// all: writeFrontendRuntimeDocs skipped every frontend without a directory in
// this tree, so the dev SPA 404'd config.js and silently ran on its
// config_gen.ts defaults. The document now goes to this project's .forge/,
// the sibling tree is never written, and the dev server is pointed at it.
func TestWriteFrontendRuntimeDocs_OutOfTreeFrontendGoesToDotForge(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	projectDir := filepath.Join(root, "control-plane")
	sibling := filepath.Join(root, "reliant", "web")
	for _, d := range []string{projectDir, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.ProjectConfig{
		Name:      "control-plane",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "reliant-web", Type: "vite"}.WithDir("../reliant/web")},
	}
	doc := "window.__FORGE_CONFIG__ = {\"API_URL\":\"http://api.test\"};\n"

	changed, err := writeFrontendRuntimeDocs(cfg, projectDir, map[string]string{"reliant-web": doc})
	if err != nil || changed != 1 {
		t.Fatalf("writeFrontendRuntimeDocs = (%d, %v), want (1, nil)", changed, err)
	}
	got, err := os.ReadFile(filepath.Join(projectDir, ".forge", "runtime-config", "reliant-web", "config.js"))
	if err != nil || string(got) != doc {
		t.Fatalf(".forge/runtime-config/reliant-web/config.js = %q, %v; want the rendered document", got, err)
	}

	// The other repository's tree is untouched: no public/, nothing at all.
	entries, err := os.ReadDir(sibling)
	if err != nil || len(entries) != 0 {
		t.Fatalf("wrote into the sibling repo's tree: %v (%v)", entries, err)
	}

	// Unchanged is a no-op, so a watching dev server is not told to reload.
	if changed, err := writeFrontendRuntimeDocs(cfg, projectDir, map[string]string{"reliant-web": doc}); err != nil || changed != 0 {
		t.Fatalf("second write = (%d, %v), want (0, nil)", changed, err)
	}

	// And the dev server is handed the absolute path.
	want := filepath.Join(projectDir, ".forge", "runtime-config", "reliant-web", "config.js")
	if p := outOfTreeRuntimeConfigFile(projectDir, "reliant-web"); p != want {
		t.Fatalf("outOfTreeRuntimeConfigFile = %q, want %q", p, want)
	}
}

// A frontend that moves INTO the tree must stop being shadowed by the copy
// rendered while it was outside.
func TestWriteFrontendRuntimeDocs_InTreeFrontendDropsAStaleOutOfTreeCopy(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	stale := filepath.Join(projectDir, outOfTreeRuntimeConfigRel("web"))
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.ProjectConfig{
		Name:      "acme",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "vite"}.WithDir("frontends/web")},
	}
	if _, err := writeFrontendRuntimeDocs(cfg, projectDir, map[string]string{"web": "window.__FORGE_CONFIG__ = {};\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale out-of-tree copy survived (stat err %v)", err)
	}
	if p := outOfTreeRuntimeConfigFile(projectDir, "web"); p != "" {
		t.Fatalf("outOfTreeRuntimeConfigFile = %q for an in-tree frontend, want \"\"", p)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "frontends", "web", "public", "config.js")); err != nil {
		t.Fatalf("in-tree document not written: %v", err)
	}
}

// The command upFrontends runs for an out-of-tree frontend names the
// document; one for an in-tree frontend does not, and a value the user
// exported wins.
func TestFrontendDevCmdPointsAnOutOfTreeFrontendAtItsDocument(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()
	doc := filepath.Join(projectDir, outOfTreeRuntimeConfigRel("reliant-web"))
	if err := os.MkdirAll(filepath.Dir(doc), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, []byte("window.__FORGE_CONFIG__ = {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fl := frontendLaunch{entities: &KCLEntities{}, env: "dev", projectDir: projectDir}
	out := FrontendEntity{Name: "reliant-web", Type: "vite", EnvFile: "/does/not/exist"}
	in := FrontendEntity{Name: "web", Type: "vite", EnvFile: "/does/not/exist"}

	env := strings.Join(buildFrontendDevCmd(context.Background(), fl, out, nil).Env, "\n")
	if !strings.Contains(env, frontendRuntimeConfigFileEnv+"="+doc) {
		t.Errorf("out-of-tree frontend's dev env lacks %s=%s:\n%s", frontendRuntimeConfigFileEnv, doc, env)
	}
	env = strings.Join(buildFrontendDevCmd(context.Background(), fl, in, nil).Env, "\n")
	if strings.Contains(env, frontendRuntimeConfigFileEnv+"=") {
		t.Errorf("in-tree frontend's dev env names an out-of-tree document:\n%s", env)
	}
	mine := frontendRuntimeConfigFileEnv + "=/mine/config.js"
	env = strings.Join(buildFrontendDevCmd(context.Background(), fl, out, []string{mine}).Env, "\n")
	if !strings.Contains(env, mine) || strings.Contains(env, frontendRuntimeConfigFileEnv+"="+doc) {
		t.Errorf("the user's %s did not win:\n%s", frontendRuntimeConfigFileEnv, env)
	}
}
