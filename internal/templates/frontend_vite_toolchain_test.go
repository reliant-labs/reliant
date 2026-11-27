package templates

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// viteToolchain is the reviewed vite / vitest / @vitejs/plugin-react triple
// every web frontend kind scaffolds, by major.
//
// The three move together. A frontend's vitest.config.ts (vite.config.ts in
// a Vite SPA) takes defineConfig from vitest's copy of vite and react() from
// the plugin's, so both must resolve ONE vite, or `tsc --noEmit` fails the
// config with TS2769. That is how forge main went red: in a dev build's
// bridged workspace, web-runtime's vitest ^4.1 put vite 8 at the root, the
// scaffold's @vitejs/plugin-react 4 resolved it there, and vitest 3 kept a
// vite 7 of its own: "Property 'rolldownVersion' is missing".
//
// Bumping one of these is deliberate. Before changing a major here, check
// against a fresh scaffold (`forge project new --frontend web`, and a
// vite-spa frontend) that:
//
//   - `npm install` succeeds on the npm that ships with Node 22 (10.x). It
//     crashes ("Cannot read properties of null (reading 'edgesOut')") on any
//     tree holding vitest 4.1, which is why the scaffold skipped from 3 to 5.
//   - `npm ls vite` lists one version, in a standalone install AND in a dev
//     build's bridged workspace.
//   - `npm run typecheck`, `npm test` and `npm run build` pass.
//   - engines.node is the highest floor vite and vitest declare.
var viteToolchain = map[string]int{
	"vite":                 8,
	"vitest":               5,
	"@vitejs/plugin-react": 6,
}

// viteToolchainNodeEngine is the floor vite 8 (^20.19 || >=22.12) and
// vitest 5 (^22.12 || ^24 || >=26) share on the Node 22 line.
const viteToolchainNodeEngine = ">=22.12"

func renderedPackageJSON(t *testing.T, kind string) map[string]any {
	t.Helper()
	content, err := FrontendTemplates().Render(
		filepath.Join(kind, "package.json.tmpl"),
		FrontendTemplateData{FrontendName: "web", ProjectName: "testproject", Platform: kind},
	)
	if err != nil {
		t.Fatalf("render %s/package.json.tmpl: %v", kind, err)
	}
	var pkg map[string]any
	if err := json.Unmarshal(content, &pkg); err != nil {
		t.Fatalf("%s package.json is not valid JSON: %v\n%s", kind, err, content)
	}
	return pkg
}

// TestFrontendViteToolchain_OneViteAcrossKinds pins that every web kind
// declares vite itself (not only through vitest's peer, which npm may place
// where the plugin cannot see it), names exactly one major for each tool,
// and agrees with the other kinds, so a project with a Next.js and a Vite
// frontend hoists a single vite.
func TestFrontendViteToolchain_OneViteAcrossKinds(t *testing.T) {
	kinds := []string{"nextjs", "vite-spa"}
	ranges := map[string]map[string]string{} // dep -> kind -> range
	for _, kind := range kinds {
		pkg := renderedPackageJSON(t, kind)
		deps, _ := pkg["devDependencies"].(map[string]any)
		for dep, wantMajor := range viteToolchain {
			rng, ok := deps[dep].(string)
			if !ok {
				t.Errorf("%s does not declare %s in devDependencies; the config's types then resolve whatever vite npm happens to hoist", kind, dep)
				continue
			}
			if strings.Contains(rng, "||") {
				t.Errorf("%s declares %s %q: a range spanning two majors lets npm pick a different one per consumer, which is the split this pin prevents", kind, dep, rng)
			}
			if major, _ := semverFloor(t, dep, rng); major != wantMajor {
				t.Errorf("%s declares %s %q, want major %d — bump vite, vitest and @vitejs/plugin-react together and re-check the list on viteToolchain", kind, dep, rng, wantMajor)
			}
			if ranges[dep] == nil {
				ranges[dep] = map[string]string{}
			}
			ranges[dep][kind] = rng
		}
		engines, _ := pkg["engines"].(map[string]any)
		if got, _ := engines["node"].(string); got != viteToolchainNodeEngine {
			t.Errorf("%s engines.node = %q, want %q (the floor vite and vitest declare)", kind, got, viteToolchainNodeEngine)
		}
	}
	for dep, byKind := range ranges {
		if byKind["nextjs"] != byKind["vite-spa"] {
			t.Errorf("%s differs across kinds (%v): a project with both hoists one and nests the other", dep, byKind)
		}
	}
}
