package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// TestStepFrontendBufTS_MissingPluginFailsTheRun pins that TypeScript
// generation is a REQUIRED step of `forge generate`, not a best-effort one.
//
// The step used to run through ctx.warnOrFail, which prints a warning and
// returns nil unless --strict was passed. So a checkout whose frontend had no
// node_modules produced this: one warning, zero TypeScript stubs written, and
// `forge generate` exiting 0. A run that wrote nothing was indistinguishable
// from a run with nothing to write.
//
// That is not survivable, because the stubs are not optional output. The hooks
// and mocks steps downstream emit TypeScript importing
// `@/gen/services/<svc>/v1/<svc>_pb` for every service, and this step is what
// writes those modules. Skipping it leaves whatever stale stubs were already
// on disk, and the diagnostic surfaces later and elsewhere — it broke
// control-plane's build and lint, attributed to the most recently added
// service rather than to the missing node_modules that actually caused it.
//
// The install is suppressed here (FORGE_SKIP_NPM_INSTALL) so the test pins the
// FAILURE contract rather than spending a real npm install: when forge cannot
// produce the stubs, the run must fail with an actionable error. The happy
// path, where forge installs the deps itself, is covered by
// TestEnsureTSPluginInstalled_NoManifestIsNotAnError plus the e2e frontend
// fixtures that run a real install.
func TestStepFrontendBufTS_MissingPluginFailsTheRun(t *testing.T) {
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "1")

	dir := t.TempDir()
	feDir := filepath.Join(dir, "frontends", "web")
	if err := os.MkdirAll(feDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A committed buf.gen.yaml naming the local plugin, and a package.json
	// that declares it — but no node_modules/.bin/protoc-gen-es anywhere.
	bufGen := "version: v2\nplugins:\n  - local: ./frontends/web/node_modules/.bin/protoc-gen-es\n    out: frontends/web/src/gen\n"
	if err := os.WriteFile(filepath.Join(feDir, "buf.gen.yaml"), []byte(bufGen), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(feDir, "package.json"),
		[]byte(`{"name":"web","devDependencies":{"@bufbuild/protoc-gen-es":"^2.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := &pipelineContext{
		ProjectDir: dir,
		// Strict is FALSE on purpose: the point of the test is that this
		// step fails without opting into --strict. Under warnOrFail the
		// same context produced a nil error and a green run.
		Strict: false,
		Cfg: &config.ProjectConfig{
			Name:      "app",
			Frontends: []config.FrontendConfig{{Name: "web", Type: "nextjs"}},
		},
	}

	err := stepFrontendBufTS(ctx)
	if err == nil {
		t.Fatal("stepFrontendBufTS returned nil with the TypeScript plugin missing — " +
			"`forge generate` exits 0 having written no stubs, and the later hooks and mocks " +
			"steps emit TypeScript importing _pb modules that do not exist")
	}
	if !strings.Contains(err.Error(), "web") {
		t.Errorf("error should name the frontend, got: %v", err)
	}
	if !strings.Contains(err.Error(), "npm") {
		t.Errorf("error should name the command that fixes it, got: %v", err)
	}
}

// TestEnsureTSPluginInstalled_NoManifestIsNotAnError pins the one case where
// the installer declines to act: a frontend directory with no package.json is
// not a node project, so there is nothing to install.
//
// It must not error, because erroring HERE would attribute the failure to the
// install rather than to the missing plugin. The caller re-resolves the plugin
// afterwards and produces the actionable error, so declining never converts a
// failure into a success.
func TestEnsureTSPluginInstalled_NoManifestIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	feDir := filepath.Join("frontends", "web")
	if err := os.MkdirAll(filepath.Join(dir, feDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureTSPluginInstalled("web", dir, feDir); err != nil {
		t.Fatalf("a directory with no package.json is not a node project and must not "+
			"produce an install error: %v", err)
	}
}
