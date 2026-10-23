package config

import (
	"os"
	"path/filepath"
	"testing"
)

// THE HALF-PREFIXED APP.
//
// BasePath feeds two generated artifacts that must agree:
//
//	next.config.ts           basePath = env ?? "<prefix>"   (scaffold-once, user-owned)
//	src/lib/basepath_gen.ts  createBasePath(env ?? "<prefix>")  (Tier-1, forge-owned)
//
// A frontend that forge DISCOVERS rather than reads from forge.yaml used to
// supply no BasePath at all, so forge rendered the second one with an empty
// fallback while the first kept the real prefix. That does not fail — it
// half-prefixes the app: Next serves routes and assets under the prefix from
// its own config, while forge's joinBasePath() becomes a no-op and every
// hand-built URL loses it.
//
// The casualty is the runtime config document. A scaffolded layout emits
// <script src={joinBasePath("/config.js")}>, which rendered "/config.js" and
// 404'd against a file at "/internal/config.js". window.__FORGE_CONFIG__ then
// never existed, every runtime value fell back to its schema default, and the
// deployed app called whatever API origin that default named — which on
// control-plane's operator console meant dialing the DEV origin from
// production and dying on CORS.

// nextConfigWithBasePath writes the frontend marker file exactly as forge's
// own next.config.ts template emits it.
func nextConfigWithBasePath(t *testing.T, dir, basePath string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? \"" + basePath + "\";\n" +
		"const nextConfig = { ...(basePath ? { basePath, assetPrefix: basePath } : {}) };\n" +
		"export default nextConfig;\n"
	if err := os.WriteFile(filepath.Join(dir, "next.config.ts"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDiscoverInRepoFrontends_RecoversBasePath is the reproduction: a
// discovered frontend must carry the prefix its own next.config.ts declares,
// because that value is what forge writes into basepath_gen.ts.
func TestDiscoverInRepoFrontends_RecoversBasePath(t *testing.T) {
	projectDir := t.TempDir()
	nextConfigWithBasePath(t, filepath.Join(projectDir, "frontends", "console"), "/internal")

	got := DiscoverInRepoFrontends(projectDir)
	if len(got) != 1 {
		t.Fatalf("discovered %d frontends, want 1", len(got))
	}
	if got[0].BasePath != "/internal" {
		t.Errorf("BasePath = %q, want %q — forge would generate basepath_gen.ts with an empty "+
			"fallback while next.config.ts serves under the prefix, so joinBasePath() becomes a "+
			"no-op and config.js 404s", got[0].BasePath, "/internal")
	}
}

// TestDeriveFrontendsFromKCL_RecoversBasePath covers the path a project that
// declares its frontends in KCL actually takes. This is the one control-plane
// uses, and the one where the outage happened.
func TestDeriveFrontendsFromKCL_RecoversBasePath(t *testing.T) {
	projectDir := t.TempDir()
	nextConfigWithBasePath(t, filepath.Join(projectDir, "frontends", "console"), "/internal")

	got := DeriveFrontendsFromKCL(projectDir, []KCLFrontend{
		{Name: "console", Type: "nextjs", Path: "frontends/console"},
	})
	if len(got) != 1 {
		t.Fatalf("derived %d frontends, want 1", len(got))
	}
	if got[0].BasePath != "/internal" {
		t.Errorf("BasePath = %q, want %q", got[0].BasePath, "/internal")
	}
}

// TestDiscoverInRepoFrontends_RootServedStaysEmpty keeps the recovery from
// inventing a prefix. A frontend served from the host root declares an empty
// fallback, and empty must survive as empty — a guessed prefix would break an
// app that was working.
func TestDiscoverInRepoFrontends_RootServedStaysEmpty(t *testing.T) {
	projectDir := t.TempDir()
	nextConfigWithBasePath(t, filepath.Join(projectDir, "frontends", "web"), "")

	got := DiscoverInRepoFrontends(projectDir)
	if len(got) != 1 {
		t.Fatalf("discovered %d frontends, want 1", len(got))
	}
	if got[0].BasePath != "" {
		t.Errorf("BasePath = %q, want empty for a root-served frontend", got[0].BasePath)
	}
}

// TestBasePathFromNextConfig_RejectsMalformed pins that a prefix failing the
// shape rules is DROPPED rather than propagated. This value is written into
// generated TypeScript, so it is held to the same rules forge.yaml's
// base_path is validated against.
func TestBasePathFromNextConfig_RejectsMalformed(t *testing.T) {
	for _, bad := range []string{"internal", "/internal/", "/in ternal"} {
		t.Run(bad, func(t *testing.T) {
			dir := t.TempDir()
			nextConfigWithBasePath(t, dir, bad)
			if got := basePathFromNextConfig(dir); got != "" {
				t.Errorf("basePathFromNextConfig = %q, want empty for malformed %q", got, bad)
			}
		})
	}
}

// TestBasePathFromNextConfig_NoConfigIsEmpty covers the frontend forge cannot
// read: no next.config.ts at all. It must degrade to the previous behaviour
// rather than erroring, so a frontend this cannot parse is no worse off.
func TestBasePathFromNextConfig_NoConfigIsEmpty(t *testing.T) {
	if got := basePathFromNextConfig(t.TempDir()); got != "" {
		t.Errorf("basePathFromNextConfig = %q, want empty when there is no next.config.ts", got)
	}
}

// TestMergeFrontendInventory_KCLBasePathWins is the precedence guard. A
// base_path the KCL forge.Frontend declares is authoritative; the next.config.ts
// recovery is strictly a fallback for when the declaration says nothing, and must
// never overwrite a stated value with one parsed out of a file.
func TestMergeFrontendInventory_KCLBasePathWins(t *testing.T) {
	projectDir := t.TempDir()
	nextConfigWithBasePath(t, filepath.Join(projectDir, "frontends", "console"), "/from-next-config")

	disk := DiscoverInRepoFrontends(projectDir)
	if len(disk) != 1 || disk[0].BasePath != "/from-next-config" {
		t.Fatalf("disk discovery should recover the prefix, got %+v", disk)
	}
	declared := FrontendConfig{Name: "console", Type: "nextjs", BasePath: "/declared"}.WithDir("frontends/console")

	got := MergeFrontendInventory(disk, []FrontendConfig{declared})
	if got[0].BasePath != "/declared" {
		t.Errorf("BasePath = %q, want %q — the KCL declaration must win over next.config.ts", got[0].BasePath, "/declared")
	}
}
