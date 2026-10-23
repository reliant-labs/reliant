package deploytarget

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testMapJSON = `{"version":3,"sources":["../src/app.ts"],"sourcesContent":["export {}"],"names":[],"mappings":"AAAA"}`

// buildFixture lays out a frontend as a Vite build leaves it: bundles with
// trailers, their maps beside them, a CSS file with a /*# */ trailer, a
// non-map `.map` file, and an index.html.
func buildFixture(t *testing.T) (projectDir string, files map[string]string) {
	t.Helper()
	projectDir = t.TempDir()
	files = map[string]string{
		"index.html":               "<html></html>",
		"assets/index-abc.js":      "console.log(1);\n//# sourceMappingURL=index-abc.js.map\n",
		"assets/index-abc.js.map":  testMapJSON,
		"assets/chunk-def.mjs":     "export const x=1;\n//# sourceMappingURL=chunk-def.mjs.map",
		"assets/chunk-def.mjs.map": testMapJSON,
		"assets/style-123.css":     "a{color:red}\n/*# sourceMappingURL=style-123.css.map */\n",
		"assets/style-123.css.map": testMapJSON,
		"assets/level.map":         `{"tiles":[1,2,3]}`,
		"src/app.ts":               "export {}",
	}
	writeTree(t, filepath.Join(projectDir, "web", "dist"), files)
	return projectDir, files
}

func fixturePlan(t *testing.T, projectDir, mode string) StagePlan {
	t.Helper()
	plan, err := buildStagePlan(StageInput{
		Name:          "web",
		Path:          "web",
		PublicDir:     "dist",
		BasePath:      "/app",
		ProjectDir:    projectDir,
		StagingRoot:   filepath.Join(t.TempDir(), "stage"),
		Project:       "acme",
		VersionEnvVar: "VITE_APP_VERSION",
		Sourcemaps:    mode,
		BuildEnv:      map[string]string{"VITE_API_URL": "https://api.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestRunStagePlanExtractsMapsIntoSidecar(t *testing.T) {
	projectDir, files := buildFixture(t)
	plan := fixturePlan(t, projectDir, "")
	runner := &fakeRunner{}

	res, err := runStagePlan(context.Background(), runner, plan)
	if err != nil {
		t.Fatal(err)
	}

	served := readTree(t, plan.StagingDir)
	for rel := range served {
		if strings.HasSuffix(rel, ".map") && rel != "app/assets/level.map" {
			t.Errorf("served tree still holds source map %q", rel)
		}
	}
	// A `.map` that is not a source map is data, and stays.
	if got := served["app/assets/level.map"]; got != files["assets/level.map"] {
		t.Errorf("non-source-map .map was touched: %q", got)
	}

	for rel, want := range map[string]string{
		"app/assets/index-abc.js":  "console.log(1);\n",
		"app/assets/chunk-def.mjs": "export const x=1;\n",
		"app/assets/style-123.css": "a{color:red}\n",
	} {
		if got := served[rel]; got != want {
			t.Errorf("%s after trailer strip = %q, want %q", rel, got, want)
		}
	}
	if got := served["app/index.html"]; got != files["index.html"] {
		t.Errorf("index.html changed: %q", got)
	}

	sidecar := SourcemapsDir(plan.StagingDir)
	if res.SourcemapsDir != sidecar || res.Sourcemaps != 3 {
		t.Errorf("result = %+v, want 3 maps in %s", res, sidecar)
	}
	side := readTree(t, sidecar)
	for _, rel := range []string{"app/assets/index-abc.js.map", "app/assets/chunk-def.mjs.map", "app/assets/style-123.css.map"} {
		if side[rel] != testMapJSON {
			t.Errorf("sidecar %s = %q, want the original map moved intact", rel, side[rel])
		}
	}

	var m SourcemapManifest
	if err := json.Unmarshal([]byte(side[SourcemapManifestName]), &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.Schema != SourcemapManifestSchema || m.Project != "acme" || m.Frontend != "web" || m.BasePath != "/app" {
		t.Errorf("manifest header = %+v", m)
	}
	if m.BuildID == "" || m.BuildID != res.BuildID {
		t.Errorf("manifest build id %q, result %q", m.BuildID, res.BuildID)
	}
	if len(m.Files) != 3 {
		t.Fatalf("manifest files = %+v", m.Files)
	}
	first := m.Files[0]
	if first.Path != "app/assets/chunk-def.mjs" || first.Map != "app/assets/chunk-def.mjs.map" || len(first.SHA256) != 64 {
		t.Errorf("manifest entry = %+v", first)
	}
}

func TestRunStagePlanInjectsBuildIDAndMapSwitch(t *testing.T) {
	projectDir, _ := buildFixture(t)
	plan := fixturePlan(t, projectDir, "")
	runner := &fakeRunner{}

	res, err := runStagePlan(context.Background(), runner, plan)
	if err != nil {
		t.Fatal(err)
	}

	var buildEnv map[string]string
	for i, c := range runner.calls {
		if strings.HasPrefix(c, "npm run build") {
			buildEnv = runner.envCalls[i]
		}
	}
	if buildEnv == nil {
		t.Fatalf("no build call in %v", runner.calls)
	}
	if buildEnv["VITE_APP_VERSION"] != res.BuildID || len(res.BuildID) != 12 {
		t.Errorf("build env version = %q, build id = %q", buildEnv["VITE_APP_VERSION"], res.BuildID)
	}
	if buildEnv[SourcemapsEnvVar] != "1" {
		t.Errorf("%s = %q, want 1", SourcemapsEnvVar, buildEnv[SourcemapsEnvVar])
	}
	if buildEnv["VITE_API_URL"] != "https://api.example.com" || buildEnv["NODE_ENV"] != "production" {
		t.Errorf("existing build env lost: %v", buildEnv)
	}
}

func TestComputeBuildID(t *testing.T) {
	write := func(files map[string]string) string {
		dir := t.TempDir()
		writeTree(t, dir, files)
		return dir
	}
	env := map[string]string{"VITE_API_URL": "https://a"}
	id := func(dir string, e map[string]string) string {
		t.Helper()
		got, err := computeBuildID(dir, "VITE_APP_VERSION", e)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	base := write(map[string]string{"src/a.ts": "1", "package.json": "{}"})
	same := write(map[string]string{"package.json": "{}", "src/a.ts": "1"})
	if a, b := id(base, env), id(same, env); a != b || len(a) != 12 {
		t.Errorf("same tree and env: %q vs %q", a, b)
	}

	changedSrc := write(map[string]string{"src/a.ts": "2", "package.json": "{}"})
	if id(base, env) == id(changedSrc, env) {
		t.Error("a changed source file must change the id")
	}
	if id(base, env) == id(base, map[string]string{"VITE_API_URL": "https://b"}) {
		t.Error("a changed build env value must change the id")
	}
	// Dependencies and build output are not the code.
	noisy := write(map[string]string{"src/a.ts": "1", "package.json": "{}", "node_modules/x/i.js": "x", "dist/o.js": "y", ".git/HEAD": "z"})
	if id(base, env) != id(noisy, env) {
		t.Error("node_modules, dist and .git must not feed the id")
	}
	// A project-set version is used verbatim, and is not itself an input.
	set := map[string]string{"VITE_APP_VERSION": "1.4.2-rc1"}
	if got := id(base, set); got != "1.4.2-rc1" {
		t.Errorf("project-set version = %q", got)
	}
}

func TestStagePlanKeepsProjectSetVersion(t *testing.T) {
	projectDir, _ := buildFixture(t)
	plan := fixturePlan(t, projectDir, "")
	plan.BuildEnv = map[string]string{"VITE_APP_VERSION": "2.0.0"}

	res, err := runStagePlan(context.Background(), &fakeRunner{}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if res.BuildID != "2.0.0" {
		t.Errorf("build id = %q, want the project's own version", res.BuildID)
	}
}

func TestStagingDigestExcludesMaps(t *testing.T) {
	projectDir, _ := buildFixture(t)
	plan := fixturePlan(t, projectDir, "")
	if _, err := runStagePlan(context.Background(), &fakeRunner{}, plan); err != nil {
		t.Fatal(err)
	}
	before, err := stagingDigest(plan.StagingDir)
	if err != nil {
		t.Fatal(err)
	}

	// A different map for the same bundle must not change the release.
	sidecarMap := filepath.Join(SourcemapsDir(plan.StagingDir), "app", "assets", "index-abc.js.map")
	if err := os.WriteFile(sidecarMap, []byte(`{"version":3,"mappings":"BBBB","sources":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := stagingDigest(plan.StagingDir)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("digest moved with a map change: %s -> %s", before, after)
	}

	// And two builds that differ only in their maps share a digest.
	other := fixturePlan(t, projectDir, "")
	other.StagingDir = filepath.Join(t.TempDir(), "stage")
	if err := os.WriteFile(filepath.Join(projectDir, "web", "dist", "assets", "index-abc.js.map"), []byte(`{"version":3,"mappings":"ZZZZ","sources":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runStagePlan(context.Background(), &fakeRunner{}, other); err != nil {
		t.Fatal(err)
	}
	if d, _ := stagingDigest(other.StagingDir); d != before {
		t.Errorf("digest depends on map bytes: %s vs %s", d, before)
	}
}

func TestGuardRefusesStagedMap(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"assets/a.js": "x", "assets/a.js.map": testMapJSON})

	err := guardNoSourceMaps(dir, "")
	if err == nil || !strings.Contains(err.Error(), "assets/a.js.map") || !strings.Contains(err.Error(), `sourcemaps = "public"`) {
		t.Fatalf("guard error = %v", err)
	}
	if err := guardNoSourceMaps(dir, SourcemapsPublic); err != nil {
		t.Errorf("public opt-out still refused: %v", err)
	}
	// Data that merely ends in .map is not refused.
	data := t.TempDir()
	writeTree(t, data, map[string]string{"level.map": `{"tiles":[]}`})
	if err := guardNoSourceMaps(data, ""); err != nil {
		t.Errorf("non-source-map refused: %v", err)
	}
}

func TestRunStagePlanGuardsAMapAddedLate(t *testing.T) {
	projectDir, _ := buildFixture(t)
	plan := fixturePlan(t, projectDir, "")
	if _, err := runStagePlan(context.Background(), &fakeRunner{}, plan); err != nil {
		t.Fatal(err)
	}
	// What Firebase asset retention does after the stage: add a file.
	writeTree(t, plan.StagingDir, map[string]string{"app/assets/old.js.map": testMapJSON})
	if err := guardNoSourceMaps(plan.StagingDir, plan.Sourcemaps); err == nil {
		t.Fatal("a map added after the stage must be refused")
	}
}

func TestPublicModeServesMapsAndKeepsTrailers(t *testing.T) {
	projectDir, files := buildFixture(t)
	plan := fixturePlan(t, projectDir, SourcemapsPublic)

	res, err := runStagePlan(context.Background(), &fakeRunner{}, plan)
	if err != nil {
		t.Fatal(err)
	}
	served := readTree(t, plan.StagingDir)
	if served["app/assets/index-abc.js.map"] != testMapJSON || served["app/assets/index-abc.js"] != files["assets/index-abc.js"] {
		t.Error("public mode must leave maps and trailers alone")
	}
	if res.SourcemapsDir != "" {
		t.Errorf("public mode made a sidecar: %q", res.SourcemapsDir)
	}
	if _, err := os.Stat(SourcemapsDir(plan.StagingDir)); !os.IsNotExist(err) {
		t.Error("public mode created the sidecar directory")
	}
}

func TestOffModeDropsMapsEverywhere(t *testing.T) {
	projectDir, _ := buildFixture(t)
	plan := fixturePlan(t, projectDir, SourcemapsOff)
	runner := &fakeRunner{}

	res, err := runStagePlan(context.Background(), runner, plan)
	if err != nil {
		t.Fatal(err)
	}
	if res.SourcemapsDir != "" {
		t.Errorf("off mode kept a sidecar: %q", res.SourcemapsDir)
	}
	if _, err := os.Stat(SourcemapsDir(plan.StagingDir)); !os.IsNotExist(err) {
		t.Error("off mode left the sidecar on disk")
	}
	for rel, body := range readTree(t, plan.StagingDir) {
		if strings.HasSuffix(rel, ".map") && rel != "app/assets/level.map" {
			t.Errorf("off mode served %s", rel)
		}
		if strings.Contains(body, "sourceMappingURL") {
			t.Errorf("off mode left a trailer in %s", rel)
		}
	}
	for i, c := range runner.calls {
		if strings.HasPrefix(c, "npm run build") && runner.envCalls[i][SourcemapsEnvVar] != "0" {
			t.Errorf("off mode build env %s = %q, want 0", SourcemapsEnvVar, runner.envCalls[i][SourcemapsEnvVar])
		}
	}
}

func TestInlineMapIsExtractedNotServed(t *testing.T) {
	projectDir := t.TempDir()
	inline := "data:application/json;base64," + base64.StdEncoding.EncodeToString([]byte(testMapJSON))
	writeTree(t, filepath.Join(projectDir, "web", "dist"), map[string]string{
		"assets/a.js": "var a=1;\n//# sourceMappingURL=" + inline + "\n",
	})
	plan := fixturePlan(t, projectDir, "")

	res, err := runStagePlan(context.Background(), &fakeRunner{}, plan)
	if err != nil {
		t.Fatal(err)
	}
	served := readTree(t, plan.StagingDir)
	if served["app/assets/a.js"] != "var a=1;\n" {
		t.Errorf("inline map still in the bundle: %q", served["app/assets/a.js"])
	}
	if got := readTree(t, SourcemapsDir(plan.StagingDir))["app/assets/a.js.map"]; got != testMapJSON || res.Sourcemaps != 1 {
		t.Errorf("inline map not extracted: %q (count %d)", got, res.Sourcemaps)
	}
}

func TestSplitSourceMapTrailer(t *testing.T) {
	cases := []struct {
		name, in, body, ref string
		ok                  bool
	}{
		{"line", "x;\n//# sourceMappingURL=a.js.map\n", "x;\n", "a.js.map", true},
		{"no newline", "x;\n//# sourceMappingURL=a.js.map", "x;\n", "a.js.map", true},
		{"legacy @", "x;\n//@ sourceMappingURL=a.js.map\n", "x;\n", "a.js.map", true},
		{"css", "a{}\n/*# sourceMappingURL=a.css.map */\n", "a{}\n", "a.css.map", true},
		{"trailing blank lines", "x;\n//# sourceMappingURL=a.js.map\n\n\n", "x;\n", "a.js.map", true},
		{"only line", "//# sourceMappingURL=a.js.map", "", "a.js.map", true},
		{"none", "x;\n", "x;\n", "", false},
		{"mentioned in code", "var s='//# sourceMappingURL=x';\nmore();\n", "var s='//# sourceMappingURL=x';\nmore();\n", "", false},
		{"followed by code", "//# sourceMappingURL=a.map\nmore();\n", "//# sourceMappingURL=a.map\nmore();\n", "", false},
		{"in a string on the last line", "y(\"//# sourceMappingURL=a.map\")", "y(\"//# sourceMappingURL=a.map\")", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, ref, ok := splitSourceMapTrailer(c.in)
			if body != c.body || ref != c.ref || ok != c.ok {
				t.Errorf("split(%q) = (%q, %q, %v), want (%q, %q, %v)", c.in, body, ref, ok, c.body, c.ref, c.ok)
			}
		})
	}
}

func TestSourcemapsModeValidation(t *testing.T) {
	if _, err := buildStagePlan(StageInput{Name: "web", Path: "web", PublicDir: "dist", Sourcemaps: "sometimes"}); err == nil {
		t.Fatal("an unknown sourcemaps mode must be refused")
	}
	for _, mode := range []string{"", "publish", "public", "off"} {
		if _, err := buildStagePlan(StageInput{Name: "web", Path: "web", PublicDir: "dist", Sourcemaps: mode}); err != nil {
			t.Errorf("mode %q: %v", mode, err)
		}
	}
}

func TestPlanAssetCarryNeverCarriesMaps(t *testing.T) {
	sum := strings.Repeat("a", 64)
	prev := &assetManifest{Release: 3, Assets: map[string]assetRecord{
		"assets/old.js":     {SHA256: sum, Release: 2},
		"assets/old.js.map": {SHA256: sum, Release: 2},
	}}
	_, carry := planAssetCarry(prev, map[string]string{}, "assets", 10)
	if _, ok := carry["assets/old.js"]; !ok {
		t.Error("a hashed asset must still be carried")
	}
	if _, ok := carry["assets/old.js.map"]; ok {
		t.Error("a source map must not be carried forward")
	}
}
