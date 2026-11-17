package config

import (
	"os"
	"path/filepath"
	"testing"
)

// mkFrontendDir creates an in-repo frontend directory so the containment
// test has something to stat, and returns the project root.
func mkFrontendDir(t *testing.T, rel string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	return root
}

func TestOwnsFrontendCode(t *testing.T) {
	root := mkFrontendDir(t, filepath.Join("frontends", "internal-console"))

	tests := []struct {
		name string
		fe   KCLFrontend
		want bool
	}{
		{
			name: "in-repo path is owned",
			fe:   KCLFrontend{Name: "internal-console", Path: "frontends/internal-console"},
			want: true,
		},
		{
			name: "empty path falls back to the frontends/<name> convention",
			fe:   KCLFrontend{Name: "internal-console"},
			want: true,
		},
		{
			// The case that makes this a containment test rather than a
			// name list: control-plane's reliant-web lives in a sibling
			// repository, and generating into it would put one repo's
			// generated TypeScript in another repo's working tree.
			name: "sibling-repo path escapes the project root",
			fe:   KCLFrontend{Name: "reliant-web", Path: "../reliant/web"},
			want: false,
		},
		{
			name: "cross-repo source pin has no directory here by design",
			fe:   KCLFrontend{Name: "reliant-web", Path: "frontends/internal-console", HasSource: true},
			want: false,
		},
		{
			name: "in-repo path that does not exist yet is not a codegen target",
			fe:   KCLFrontend{Name: "not-scaffolded", Path: "frontends/not-scaffolded"},
			want: false,
		},
		{
			name: "unnamed declaration",
			fe:   KCLFrontend{Path: "frontends/internal-console"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.fe.OwnsFrontendCode(root); got != tt.want {
				t.Errorf("OwnsFrontendCode(%+v) = %v, want %v", tt.fe, got, tt.want)
			}
		})
	}
}

// TestDeriveFrontendsFromKCLKeepsInRepoOnly is the defect this inventory was
// built for: control-plane's shape, where the deploy graph declares one in-repo
// frontend beside one sibling-repo frontend. Only the in-repo one is this
// project's to generate into.
func TestDeriveFrontendsFromKCLKeepsInRepoOnly(t *testing.T) {
	root := mkFrontendDir(t, filepath.Join("frontends", "internal-console"))

	got := DeriveFrontendsFromKCL(root, []KCLFrontend{
		{Name: "reliant-web", Type: "vite", Path: "../reliant/web"},
		{Name: "internal-console", Type: "nextjs", Path: "frontends/internal-console"},
	})

	if len(got) != 1 {
		t.Fatalf("want exactly the in-repo frontend, got %+v", got)
	}
	if got[0].Name != "internal-console" || got[0].Type != "nextjs" {
		t.Errorf("derived entry = %+v, want internal-console/nextjs", got[0])
	}
	if got[0].DeclaredDir() != "frontends/internal-console" {
		t.Errorf("derived path = %q, want a project-relative path", got[0].DeclaredDir())
	}
}

// TestMergeFrontendInventory pins the overlay that replaced the forge.yaml
// `frontends:` block: KCL is authoritative for every field it declares, and the
// disk supplies what the declaration cannot (the Next.js output shape, and any
// dev_runner / base_path the declaration left out).
func TestMergeFrontendInventory(t *testing.T) {
	disk := []FrontendConfig{
		FrontendConfig{Name: "web", Type: "nextjs", Output: "static", DevRunner: "pnpm", BasePath: "/from-disk"}.WithDir("frontends/web"),
		FrontendConfig{Name: "scratch-only", Type: "vite-spa"}.WithDir("frontends/scratch-only"),
	}
	kcl := []FrontendConfig{
		FrontendConfig{Name: "web", Type: "nextjs", Port: 4100, Routes: []string{"users"}, BasePath: "/admin"}.WithDir("frontends/web"),
		FrontendConfig{Name: "kcl-only", Type: "nextjs", Port: 4200}.WithDir("frontends/kcl-only"),
	}

	got := MergeFrontendInventory(disk, kcl)
	byName := map[string]FrontendConfig{}
	for _, fe := range got {
		byName[fe.Name] = fe
	}
	if len(got) != 3 {
		t.Fatalf("want web + kcl-only + scratch-only, got %+v", got)
	}
	web := byName["web"]
	if web.Port != 4100 || len(web.Routes) != 1 || web.Routes[0] != "users" {
		t.Errorf("KCL port/routes must win, got %+v", web)
	}
	if web.BasePath != "/admin" {
		t.Errorf("KCL base_path must beat the next.config.ts fallback, got %q", web.BasePath)
	}
	if web.Output != "static" {
		t.Errorf("output comes from next.config.ts (KCL has no such field), got %q", web.Output)
	}
	if web.DevRunner != "pnpm" {
		t.Errorf("dev_runner the KCL left out must come from the lockfile, got %q", web.DevRunner)
	}
	if got[0].Name != "kcl-only" || got[1].Name != "scratch-only" || got[2].Name != "web" {
		t.Errorf("want name-sorted order, got %s, %s, %s", got[0].Name, got[1].Name, got[2].Name)
	}
}

// TestDeriveFrontendsFromKCLDedupesAndSorts: several environments normally
// declare the SAME frontend, so without dedupe+sort the codegen inventory
// would vary with which environment was read first — and generate's output
// would depend on directory order.
func TestDeriveFrontendsFromKCLDedupesAndSorts(t *testing.T) {
	root := mkFrontendDir(t, filepath.Join("frontends", "admin"))
	if err := os.MkdirAll(filepath.Join(root, "frontends", "console"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := DeriveFrontendsFromKCL(root, []KCLFrontend{
		{Name: "console", Type: "nextjs", Path: "frontends/console"},
		{Name: "admin", Type: "nextjs", Path: "frontends/admin"},
		{Name: "console", Type: "nextjs", Path: "frontends/console"},
	})

	if len(got) != 2 {
		t.Fatalf("want 2 deduped frontends, got %+v", got)
	}
	if got[0].Name != "admin" || got[1].Name != "console" {
		t.Errorf("want name-sorted order, got %s then %s", got[0].Name, got[1].Name)
	}
}

// A KCL module that does not compile renders nothing, and "no frontends" then
// has effects indistinguishable from the original bug — emitters walk an empty
// list and already-generated files read as stale. The load seam therefore never
// depends on the deploy graph COMPILING: an in-repo frontend is found from the
// tree itself.
func TestResolveInventoryAtLoadFindsFrontendsOnDisk(t *testing.T) {
	root := mkFrontendDir(t, filepath.Join("frontends", "internal-console"))
	writeMarker(t, root, "frontends/internal-console/next.config.ts")

	cfg := &ProjectConfig{}
	ResolveInventoryAtLoad(cfg, root)
	if len(cfg.Frontends) != 1 || cfg.Frontends[0].Name != "internal-console" {
		t.Fatalf("want the on-disk frontend, got %+v", cfg.Frontends)
	}
	if cfg.Frontends[0].Type != "nextjs" {
		t.Errorf("Type = %q, want nextjs from next.config.ts", cfg.Frontends[0].Type)
	}
}

// Each per-frontend field that is not in KCL is read off the frontend's own
// files. Pinned here so the audit table in the PR stays true.
func TestDiscoverInRepoFrontendsDetectsOutputBasePathAndRunner(t *testing.T) {
	root := t.TempDir()
	writeMarker(t, root, "frontends/site/next.config.ts")
	writeFile(t, root, "frontends/site/next.config.ts", `const basePath = process.env.NEXT_PUBLIC_BASE_PATH ?? "/docs";
  // output: "standalone" is only mentioned in this comment
  ...(process.env.NODE_ENV === "production" ? { output: "export" } : {}),
`)
	writeFile(t, root, "frontends/site/pnpm-lock.yaml", "lockfileVersion: 9\n")
	writeMarker(t, root, "frontends/plain/next.config.ts")
	writeFile(t, root, "frontends/plain/next.config.ts", "export default { output: \"standalone\" };\n")

	got := map[string]FrontendConfig{}
	for _, fe := range DiscoverInRepoFrontends(root) {
		got[fe.Name] = fe
	}
	site := got["site"]
	if site.Output != "static" {
		t.Errorf("output: export => static, got %q (a commented-out standalone must not win)", site.Output)
	}
	if site.BasePath != "/docs" {
		t.Errorf("BasePath = %q, want /docs from next.config.ts", site.BasePath)
	}
	if site.DevRunner != DevRunnerPNPM {
		t.Errorf("DevRunner = %q, want pnpm from pnpm-lock.yaml", site.DevRunner)
	}
	if got["plain"].Output != "standalone" {
		t.Errorf("Output = %q, want standalone", got["plain"].Output)
	}
	if got["plain"].DevRunner != "" {
		t.Errorf("no lockfile => unset (npm default), got %q", got["plain"].DevRunner)
	}
}

// A directory with no framework marker is not a frontend. Without this,
// a scratch copy or a half-deleted tree under frontends/ would be
// generated into.
func TestDiscoverInRepoFrontendsRequiresAMarker(t *testing.T) {
	root := mkFrontendDir(t, filepath.Join("frontends", "leftovers"))

	if got := DiscoverInRepoFrontends(root); len(got) != 0 {
		t.Fatalf("a directory with no framework config is not a frontend, got %+v", got)
	}
}

func TestDiscoverInRepoFrontendsIdentifiesKinds(t *testing.T) {
	root := t.TempDir()
	writeMarker(t, root, "frontends/web/vite.config.ts")
	writeMarker(t, root, "frontends/mobile/app.json")

	got := DiscoverInRepoFrontends(root)
	if len(got) != 2 {
		t.Fatalf("want 2 frontends, got %+v", got)
	}
	// Sorted by name: mobile before web.
	if got[0].Type != "react-native" || got[1].Type != "vite-spa" {
		t.Errorf("kinds = %q/%q, want react-native/vite-spa", got[0].Type, got[1].Type)
	}
}

// writeMarker creates a framework config file (and its parent frontend
// directory) so discovery has something to identify.
func writeMarker(t *testing.T, root, rel string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte("export default {}\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A scaffolded KCL stanza omits `type`; the framework marker on disk is the
// only thing that knows a Vite app is not a Next.js one. Losing it made
// `forge generate` write Next.js nav/dashboard files into a Vite frontend.
func TestMergeFrontendInventory_KCLWithoutTypeKeepsTheDiscoveredType(t *testing.T) {
	disk := []FrontendConfig{
		FrontendConfig{Name: "spa", Type: "vite-spa"}.WithDir("frontends/spa"),
	}
	kcl := []FrontendConfig{
		FrontendConfig{Name: "spa", Port: 4100}.WithDir("frontends/spa"),
	}
	got := MergeFrontendInventory(disk, kcl)
	if len(got) != 1 || got[0].Type != "vite-spa" || got[0].Port != 4100 {
		t.Errorf("want the on-disk type with the KCL port, got %+v", got)
	}
}

func TestDeriveFrontendsFromKCL_NormalizesKCLTypeSpellings(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"spa", "mobile", "web"} {
		if err := os.MkdirAll(filepath.Join(dir, "frontends", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := DeriveFrontendsFromKCL(dir, []KCLFrontend{
		{Name: "spa", Type: "vite", Path: "frontends/spa"},
		{Name: "mobile", Type: "rn", Path: "frontends/mobile"},
		{Name: "web", Type: "nextjs", Path: "frontends/web"},
	})
	types := map[string]string{}
	for _, fe := range got {
		types[fe.Name] = fe.Type
	}
	want := map[string]string{"spa": "vite-spa", "mobile": "react-native", "web": "nextjs"}
	for n, w := range want {
		if types[n] != w {
			t.Errorf("%s: type = %q, want %q", n, types[n], w)
		}
	}
}
