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
// protobuf, generated modules), and the reused dev-log receiver.
func TestWriteSiteStarter(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		files []string
	}{
		{"nextjs", []string{"package.json", "next.config.ts", "tsconfig.json", "postcss.config.mjs", ".gitignore",
			"src/app/layout.tsx", "src/app/page.tsx", "src/app/globals.css", "src/app/dev-logging.tsx", "src/app/%5F_forge/log/route.ts"}},
		{"vite-spa", []string{"package.json", "vite.config.ts", "index.html", "tsconfig.json", ".gitignore",
			"src/main.tsx", "src/App.tsx", "src/index.css", "src/vite-env.d.ts", "src/lib/devlog-plugin.ts"}},
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
			for _, rel := range tc.files {
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
				bads := []string{"{{", "_gen", "config_gen", "@/lib/connect"}
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
			pkg, _ := os.ReadFile(filepath.Join(dir, "package.json"))
			if !strings.Contains(string(pkg), `"@reliantlabs/forge-web-runtime": "`+webRuntimePublishedRange+`"`) {
				t.Errorf("package.json must declare the web runtime at forge's floor:\n%s", pkg)
			}
			for _, peer := range []string{"@connectrpc/connect", "@bufbuild/protobuf", "@opentelemetry/api"} {
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
