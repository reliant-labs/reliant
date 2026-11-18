package deploytarget

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFirebaseAssembleWritesRuntimeConfig is the PROMOTE acceptance
// criterion for the frontend runtime-config system.
//
// The whole reason forge chose runtime injection over build-time inlining
// is that ONE built bundle should be deployable to several environments.
// That only holds if the deploy path actually writes the environment's
// config.js beside the bundle it ships. Before the fix nothing did: forge
// generated the KCL runtime projection (frontend_config_gen.k) but no
// deploy-time renderer ever turned it into an artifact, so a promoted
// bundle had no way to receive its environment's values.
//
// This test builds the bundle ONCE, then assembles it for two environments
// carrying different runtime documents, and asserts:
//
//  1. each staging tree carries its OWN config.js, and
//  2. the bundle bytes are IDENTICAL between the two — nothing was rebuilt.
func TestFirebaseAssembleWritesRuntimeConfig(t *testing.T) {
	projectDir := t.TempDir()

	// The built bundle — produced once, promoted twice. Its content is
	// deliberately environment-agnostic.
	writeFile(t, filepath.Join(projectDir, "web", "dist", "index.html"),
		`<html><head><script src="/config.js"></script></head><body>app</body></html>`)

	deployTo := func(t *testing.T, env, runtimeJS string) string {
		t.Helper()
		staging := filepath.Join(t.TempDir(), "public")
		prov := FirebaseProvider{ProjectDir: projectDir, Runner: &fakeRunner{}, StagingRoot: staging}
		fe := FirebaseFrontend{
			Name:      "web",
			Path:      "web",
			DevRunner: "npm",
			// RuntimeConfigJS is the environment's rendered config.js —
			// the artifact the KCL runtime projection produces.
			RuntimeConfigJS: runtimeJS,
			Spec: FirebaseHostingSpec{
				Project:   "demo-proj",
				Site:      "demo-" + env,
				PublicDir: "dist",
			},
		}
		if err := prov.Deploy(context.Background(), ServiceGroup{
			ProviderID: prov.Name(),
			Env:        env,
			Frontends:  []FirebaseFrontend{fe},
		}); err != nil {
			t.Fatalf("firebase deploy (%s): %v", env, err)
		}
		return staging
	}

	devJS := `window.__FORGE_CONFIG__ = {"NEXT_PUBLIC_ENVIRONMENT": "dev"};` + "\n"
	prodJS := `window.__FORGE_CONFIG__ = {"NEXT_PUBLIC_ENVIRONMENT": "production"};` + "\n"

	devStaging := deployTo(t, "dev", devJS)
	prodStaging := deployTo(t, "prod", prodJS)

	// 1) Each environment's assembled tree carries ITS OWN config.js,
	//    landing where the document head's <script src> resolves it.
	devCfg := readAssembled(t, devStaging, "config.js")
	prodCfg := readAssembled(t, prodStaging, "config.js")

	if !strings.Contains(devCfg, `"NEXT_PUBLIC_ENVIRONMENT": "dev"`) {
		t.Errorf("dev staging config.js must carry dev's values; got:\n%s", devCfg)
	}
	if !strings.Contains(prodCfg, `"NEXT_PUBLIC_ENVIRONMENT": "production"`) {
		t.Errorf("prod staging config.js must carry prod's values; got:\n%s", prodCfg)
	}
	if devCfg == prodCfg {
		t.Errorf("dev and prod must NOT receive the same config.js; both were:\n%s", devCfg)
	}

	// 2) The bundle itself is byte-identical across the two deploys —
	//    which is what "promote, don't rebuild" means.
	devIndex := readAssembled(t, devStaging, "index.html")
	prodIndex := readAssembled(t, prodStaging, "index.html")
	if devIndex != prodIndex {
		t.Errorf("the same bundle must reach both environments unchanged:\ndev:\n%s\nprod:\n%s", devIndex, prodIndex)
	}
}

// TestFirebaseAssembleRuntimeConfigHonorsBasePath pins WHERE the document
// lands for a frontend served under a base path. The generated document
// head references it as <basePath>/config.js, so it must be written inside
// the base-path subtree of the assembled site, not at the site root.
func TestFirebaseAssembleRuntimeConfigHonorsBasePath(t *testing.T) {
	projectDir := t.TempDir()
	writeFile(t, filepath.Join(projectDir, "admin", "out", "index.html"), "<admin/>")

	staging := filepath.Join(t.TempDir(), "public")
	prov := FirebaseProvider{ProjectDir: projectDir, Runner: &fakeRunner{}, StagingRoot: staging}
	fe := FirebaseFrontend{
		Name:            "admin",
		Path:            "admin",
		DevRunner:       "npm",
		RuntimeConfigJS: `window.__FORGE_CONFIG__ = {"NEXT_PUBLIC_ENVIRONMENT": "staging"};` + "\n",
		Spec: FirebaseHostingSpec{
			Project:   "demo-proj",
			Site:      "demo-staging",
			PublicDir: "out",
			BasePath:  "/admin",
		},
	}
	if err := prov.Deploy(context.Background(), ServiceGroup{
		ProviderID: prov.Name(), Env: "staging", Frontends: []FirebaseFrontend{fe},
	}); err != nil {
		t.Fatalf("firebase deploy: %v", err)
	}

	got := readAssembled(t, staging, filepath.Join("admin", "config.js"))
	if !strings.Contains(got, `"NEXT_PUBLIC_ENVIRONMENT": "staging"`) {
		t.Errorf("config.js must land under the base path; got:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(staging, "config.js")); err == nil {
		t.Error("config.js must NOT also land at the site root for a base-path frontend")
	}
}

// TestFirebaseAssembleWithoutRuntimeConfig confirms the pre-feature shape
// is untouched: a frontend carrying no runtime document (a project that
// annotates no frontend config) assembles exactly as before, with no
// config.js invented for it.
func TestFirebaseAssembleWithoutRuntimeConfig(t *testing.T) {
	projectDir := t.TempDir()
	writeFile(t, filepath.Join(projectDir, "web", "dist", "index.html"), "<spa/>")

	staging := filepath.Join(t.TempDir(), "public")
	prov := FirebaseProvider{ProjectDir: projectDir, Runner: &fakeRunner{}, StagingRoot: staging}
	fe := FirebaseFrontend{
		Name: "web", Path: "web", DevRunner: "npm",
		Spec: FirebaseHostingSpec{Project: "p", Site: "s", PublicDir: "dist"},
	}
	if err := prov.Deploy(context.Background(), ServiceGroup{
		ProviderID: prov.Name(), Frontends: []FirebaseFrontend{fe},
	}); err != nil {
		t.Fatalf("firebase deploy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staging, "config.js")); err == nil {
		t.Error("a frontend with no runtime config must not get an invented config.js")
	}
}

func readAssembled(t *testing.T, staging, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(staging, rel))
	if err != nil {
		t.Fatalf("read assembled %s: %v", rel, err)
	}
	return string(body)
}
