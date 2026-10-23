package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// THE SHIPPED-DEV-CONFIG BUG.
//
// A frontend's runtime config document (public/config.js) is rendered from an
// ENVIRONMENT's KCL, and the Dockerfile COPYs public/ straight out of the
// working tree. But only `forge env up` re-rendered it for the env being
// operated on; `forge build <env>` did not — and the document `forge generate`
// leaves on disk is rendered from the DEV env. So `forge build prod` baked
// DEV's runtime config into the production image.
//
// It hid for a long time because it is invisible whenever two envs happen to
// share a literal. control-plane's operator console had api_url =
// "http://localhost:8090" in BOTH dev and prod, so the wrong document was
// byte-identical to the right one. The day prod's value changed, the built
// image silently kept dev's, the deployed console called the wrong origin, and
// nothing in the build output named the cause.
//
// This test pins the property that closes it: rendering a frontend runtime doc
// for env X must produce X's values, and the refresh must actually overwrite a
// document left behind by a DIFFERENT env.
func TestRefreshFrontendRuntimeConfigs_OverwritesAnotherEnvsDocument(t *testing.T) {
	projectDir := identityProject(t)
	cfg := &config.ProjectConfig{
		Name:      "acme",
		Frontends: []config.FrontendConfig{config.FrontendConfig{Name: "web", Type: "vite"}.WithDir("frontends/web")},
	}

	// Stand up a SECOND env whose config differs from dev's in the one field
	// that matters. This is the shape of the real defect: two envs, one
	// document, and whichever was rendered last wins.
	kclDir := filepath.Join(projectDir, "deploy", "kcl")
	if err := os.MkdirAll(filepath.Join(kclDir, "prod"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFrontendCfgFile(t, filepath.Join(kclDir, "prod", "config.k"),
		"app_config = {\n    log_level = \"info\"\n}\n")
	fc := identityFrontendConfig()
	if _, err := codegen.EnsureFrontendConfigInstances(
		[]codegen.FrontendConfig{fc}, kclDir, "prod", "acme"); err != nil {
		t.Fatalf("EnsureFrontendConfigInstances(prod): %v", err)
	}
	// Give prod a client id dev does not have, standing in for any per-env
	// value (api_url in the real case).
	writeConvergedIdentityForEnv(t, projectDir, "prod", "prod-only-client-id")

	// The two documents the renderer produces for the two envs, from each
	// env's OWN KCL. `forge generate` leaves the dev one on disk; the prod one
	// is what a production image must ship.
	//
	// This asserts against loadFrontendRuntimeConfig — the layer that actually
	// reads an env's KCL — rather than refreshFrontendRuntimeConfigs, which
	// additionally requires a compiled proto/config descriptor that this
	// fixture deliberately does not have. The env-selection behaviour is the
	// part that was broken; the proto-parsing half is covered elsewhere.
	devValues, err := loadFrontendRuntimeConfig(projectDir, "dev", []codegen.FrontendConfig{fc})
	if err != nil {
		t.Fatalf("loadFrontendRuntimeConfig(dev): %v", err)
	}
	prodValues, err := loadFrontendRuntimeConfig(projectDir, "prod", []codegen.FrontendConfig{fc})
	if err != nil {
		t.Fatalf("loadFrontendRuntimeConfig(prod): %v", err)
	}

	devDoc := codegen.GenerateFrontendConfigJS(fc.Frontend, "dev", renderValues(t, fc, devValues))
	prodDoc := codegen.GenerateFrontendConfigJS(fc.Frontend, "prod", renderValues(t, fc, prodValues))

	if strings.Contains(devDoc, "prod-only-client-id") {
		t.Fatal("fixture is wrong: dev's document must not already carry prod's value")
	}
	// The load-bearing assertion. If these two are equal the test proves
	// nothing — that is precisely the condition (dev and prod sharing a
	// literal) under which the real bug stayed invisible for months.
	if devDoc == prodDoc {
		t.Fatal("fixture is wrong: the two envs' documents must differ, or this test cannot detect shipping the wrong one")
	}
	if !strings.Contains(prodDoc, "prod-only-client-id") {
		t.Errorf("prod's document does not carry prod's own value:\n%s", prodDoc)
	}

	// And the write half: a document rendered for prod must replace a
	// document left behind by dev, which is what `forge build prod` now does
	// before the frontend build reads public/config.js.
	writeConfigJS(t, projectDir, cfg, devDoc)
	changed, err := writeFrontendRuntimeDocs(cfg, projectDir, map[string]string{"web": prodDoc})
	if err != nil {
		t.Fatalf("writeFrontendRuntimeDocs: %v", err)
	}
	if changed != 1 {
		t.Errorf("rewrote %d document(s), want 1 — dev's document survived into the prod build", changed)
	}
	if got := readConfigJS(t, projectDir, cfg); !strings.Contains(got, "prod-only-client-id") {
		t.Errorf("config.js still carries dev's values after building for prod.\n"+
			"This is the bug: the image would ship another env's runtime config.\ngot:\n%s", got)
	}
}

// renderValues encodes a frontend's resolved runtime values the way the
// generator does, so a test can compare two envs' documents byte-for-byte.
func renderValues(t *testing.T, fc codegen.FrontendConfig, values map[string]map[string]any) string {
	t.Helper()
	encoded, err := json.MarshalIndent(frontendRuntimeValues(fc, values[fc.Frontend]), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
