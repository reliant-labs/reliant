package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// Each site starter renders a complete, self-contained app: no unrendered
// template action, nothing of the app frontend's backend wiring (Connect,
// protobuf), with generated browser telemetry config and the dev-log receiver.
func TestWriteSiteStarter(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		files []string
	}{
		{"nextjs", []string{"package.json", "next.config.ts", "tsconfig.json", "postcss.config.mjs", ".gitignore",
			"src/app/layout.tsx", "src/app/page.tsx", "src/app/globals.css", "src/app/dev-logging.tsx", "src/app/%5F_forge/log/route.ts",
			"src/app/icon.svg"}},
		// The Vite dev-log receiver comes from the runtime at the current floor;
		// TestViteSiteStarterFollowsTheRuntimeFloor checks both receiver paths.
		{"vite-spa", []string{"package.json", "vite.config.ts", "index.html", "tsconfig.json", ".gitignore",
			"src/main.tsx", "src/App.tsx", "src/index.css", "src/vite-env.d.ts", "public/icon.svg"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			fe, ok := SiteStarterFrontend(tc.kind, "web")
			if !ok {
				t.Fatalf("no starter for %s", tc.kind)
			}
			dir := t.TempDir()
			if err := WriteSiteStarter(dir, "landing", fe); err != nil {
				t.Fatal(err)
			}
			// A fresh site answers /favicon.ico and /robots.txt instead of 404s.
			for _, rel := range append(tc.files, "src/lib/otel_gen.ts", "src/lib/apiurl_gen.ts", "public/config.js",
				"public/favicon.ico", "public/robots.txt") {
				if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
					t.Errorf("starter lacks %s: %v", rel, err)
				}
			}
			err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				if strings.HasSuffix(path, ".tmpl") {
					t.Errorf("%s kept its .tmpl suffix", path)
				}
				b, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				body := string(b)
				// package.json declares the runtime's peers; no source file
				// may use them.
				bads := []string{"{{", `from "./config_gen"`, "@/lib/connect"}
				if filepath.Base(path) != "package.json" {
					bads = append(bads, "@connectrpc", "@bufbuild")
				}
				for _, bad := range bads {
					if strings.Contains(body, bad) {
						t.Errorf("%s contains %q", path, bad)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			otel, _ := os.ReadFile(filepath.Join(dir, "src/lib/otel_gen.ts"))
			if !strings.Contains(string(otel), `SERVICE_NAME = 'landing-web'`) || !strings.Contains(string(otel), "cfg.OTEL_REPLAY") {
				t.Fatal("site lacks canonical runtime telemetry configuration")
			}
			entry, html, proxy := "src/main.tsx", "index.html", "vite.config.ts"
			proxyWant := "forgeOtelProxy()"
			if tc.kind == "nextjs" {
				entry, html, proxy = "src/app/dev-logging.tsx", "src/app/layout.tsx", "next.config.ts"
				proxyWant = "startOtelRelay"
			}
			checks := [][2]string{{entry, "initTelemetry();"}, {html, `src="/config.js"`}, {proxy, proxyWant}}
			if tc.kind == "vite-spa" {
				// Next.js links src/app/icon.svg itself; a Vite page must.
				checks = append(checks, [2]string{html, `<link rel="icon" type="image/svg+xml" href="%BASE_URL%icon.svg" />`})
			}
			for _, c := range checks {
				file, want := c[0], c[1]
				b, err := os.ReadFile(filepath.Join(dir, file))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(b), want) {
					t.Errorf("%s missing %s", file, want)
				}
			}
			pkg, _ := os.ReadFile(filepath.Join(dir, "package.json"))
			// The SDK's parts are the runtime's telemetry peers (below); the
			// retired @hyperdx/browser is not.
			if strings.Contains(string(pkg), `"@hyperdx/browser"`) {
				t.Fatal("site declares @hyperdx/browser, which the runtime no longer imports")
			}

			if !strings.Contains(string(pkg), `"@reliantlabs/forge-web-runtime": "`+webRuntimePublishedRange+`"`) {
				t.Errorf("package.json must declare the web runtime at forge's floor:\n%s", pkg)
			}
			for _, peer := range []string{"@connectrpc/connect", "@bufbuild/protobuf", "@opentelemetry/api", "@hyperdx/otel-web", "rrweb"} {
				spec, _ := webruntimepeers.PeerSpec(peer)
				if !strings.Contains(string(pkg), `"`+peer+`": "`+spec+`"`) {
					t.Errorf("package.json must declare the runtime's peer %s at %q:\n%s", peer, spec, pkg)
				}
			}
		})
	}
	if _, ok := SiteStarterFrontend("astro", "web"); ok {
		t.Error("an unknown kind must have no starter")
	}
}

// Every kind SiteStarterKindNames lists has a starter tree.
func TestSiteStarterKindNamesAreAllBacked(t *testing.T) {
	for _, kind := range SiteStarterKindNames() {
		fe, ok := SiteStarterFrontend(kind, "web")
		if !ok {
			t.Errorf("%s is listed but has no starter", kind)
			continue
		}
		if err := WriteSiteStarter(t.TempDir(), "landing", fe); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
}

// An adopted Vite app whose dev server ignores PORT gets its real port in
// dev, and only in dev; with no DevPort the resolved port stays.
func TestSiteScaffoldDevPort(t *testing.T) {
	for _, tc := range []struct {
		port int
		want string
	}{
		{4321, "    port = 4321\n"},
		{0, `port = plugin.resolve_port("landing-dev-web", 3000)`},
	} {
		dir := t.TempDir()
		fe := SiteFrontend{Name: "web", Type: "vite", PublicDir: "dist", DevPort: tc.port}
		if err := WriteSiteScaffold(dir, "landing", HarnessNone, fe); err != nil {
			t.Fatal(err)
		}
		dev, err := os.ReadFile(filepath.Join(dir, "deploy", "kcl", "dev", "main.k"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(dev), tc.want) {
			t.Errorf("DevPort %d: dev/main.k lacks %q:\n%s", tc.port, tc.want, dev)
		}
		prod, err := os.ReadFile(filepath.Join(dir, "deploy", "kcl", "prod", "main.k"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(prod), "port =") {
			t.Errorf("DevPort %d: a hosted env declares no dev port:\n%s", tc.port, prod)
		}
	}
}
