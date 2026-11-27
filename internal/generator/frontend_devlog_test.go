package generator

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The Vite scaffolds pick their dev-log receiver from the runtime floor at
// render time. Everything here is offline and deterministic: the floor is
// faked, never read from npm.

func TestViteDevLogFromRuntimeFollowsTheFloor(t *testing.T) {
	for floor, want := range map[string]bool{
		"^0.13.19":                     false,
		"0.13.19":                      false,
		"^0.13.20":                     false, // the newest release without forgeDevLog
		"^0.13.21":                     true,  // webRuntimeForgeDevLogSince
		"~0.13.21":                     true,
		"^0.14.0":                      true, // a later line keeps the export
		"^1.2.0":                       true,
		"file:../../forge/web-runtime": false, // not comparable: stay inlined
		"":                             false,
	} {
		if got := viteDevLogFromRuntime(floor); got != want {
			t.Errorf("viteDevLogFromRuntime(%q) = %v, want %v", floor, got, want)
		}
	}
	if _, ok := minimumVersion(webRuntimeForgeDevLogSince); !ok {
		t.Fatalf("webRuntimeForgeDevLogSince %q is not a version", webRuntimeForgeDevLogSince)
	}
}

// The runtime branch imports a name the runtime really exports. Read from the
// in-tree source, so a rename of forgeDevLog fails here, not in a new project.
func TestForgeDevLogIsExportedFromTheRuntime(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web-runtime", "src", "vite.ts"))
	if err != nil {
		t.Fatalf("web-runtime/src/vite.ts not readable: %v", err)
	}
	if !regexp.MustCompile(`(?m)^export function forgeDevLog\(`).Match(src) {
		t.Fatal("web-runtime/src/vite.ts no longer exports forgeDevLog, which the floor-gated Vite scaffolds import")
	}
}

const (
	runtimeDevLogImport = "import {\n  forgeDevLog,\n  forgeOtelProxy,\n  forgeRuntimeConfig,\n} from \"@reliantlabs/forge-web-runtime/vite\";"
	inlineDevLogImport  = `import { devLogPlugin } from "./src/lib/devlog-plugin";`
)

// checkViteConfig asserts one rendered vite.config.ts wires exactly one
// receiver, imports what it calls, and leaves no template syntax behind.
func checkViteConfig(t *testing.T, where, body string, fromRuntime bool) {
	t.Helper()
	if strings.Contains(body, "{{") || strings.Contains(body, "}}") {
		t.Errorf("%s: template syntax left in the output", where)
	}
	if fromRuntime {
		for _, want := range []string{runtimeDevLogImport, "forgeDevLog(),"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: runtime branch lacks %q", where, want)
			}
		}
		for _, bad := range []string{"devLogPlugin", "devlog-plugin"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s: runtime branch still mentions %q", where, bad)
			}
		}
		return
	}
	for _, want := range []string{inlineDevLogImport, "devLogPlugin(),",
		`import { forgeOtelProxy, forgeRuntimeConfig } from "@reliantlabs/forge-web-runtime/vite";`} {
		if !strings.Contains(body, want) {
			t.Errorf("%s: inlined branch lacks %q", where, want)
		}
	}
	if strings.Contains(body, "forgeDevLog") {
		t.Errorf("%s: inlined branch mentions forgeDevLog, which the floor's runtime does not export", where)
	}
}

func TestViteSPAScaffoldFollowsTheRuntimeFloor(t *testing.T) {
	for _, tc := range []struct {
		floor       string
		fromRuntime bool
	}{{"^0.13.20", false}, {"^0.13.21", true}} {
		t.Run(tc.floor, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.25\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := generateFrontendFiles(root, "example.com/app", "app", "web", 8080, "vite-spa", FrontendGenOptions{}, tc.floor); err != nil {
				t.Fatal(err)
			}
			fe := filepath.Join(root, "frontends", "web")
			body, err := os.ReadFile(filepath.Join(fe, "vite.config.ts"))
			if err != nil {
				t.Fatal(err)
			}
			checkViteConfig(t, "vite-spa vite.config.ts", string(body), tc.fromRuntime)
			_, err = os.Stat(filepath.Join(fe, filepath.FromSlash(inlinedDevLogPlugin)))
			if tc.fromRuntime != os.IsNotExist(err) {
				t.Errorf("floor %s: %s exists = %v, want %v", tc.floor, inlinedDevLogPlugin, err == nil, !tc.fromRuntime)
			}
		})
	}
}

func TestViteSiteStarterFollowsTheRuntimeFloor(t *testing.T) {
	fe, ok := SiteStarterFrontend("vite-spa", "web")
	if !ok {
		t.Fatal("no vite-spa site starter")
	}
	for _, tc := range []struct {
		floor       string
		fromRuntime bool
	}{{"^0.13.20", false}, {"^0.13.21", true}} {
		t.Run(tc.floor, func(t *testing.T) {
			dir := t.TempDir()
			if err := writeSiteStarter(dir, "landing", fe, tc.floor); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(dir, "vite.config.ts"))
			if err != nil {
				t.Fatal(err)
			}
			checkViteConfig(t, "site vite.config.ts", string(body), tc.fromRuntime)
			_, err = os.Stat(filepath.Join(dir, filepath.FromSlash(inlinedDevLogPlugin)))
			if tc.fromRuntime != os.IsNotExist(err) {
				t.Errorf("floor %s: %s exists = %v, want %v", tc.floor, inlinedDevLogPlugin, err == nil, !tc.fromRuntime)
			}
		})
	}
}
