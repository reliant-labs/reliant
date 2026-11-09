package templates

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTelemetryBuildVariableFallbackBehaviour runs the rendered otel_gen.ts
// (both web scaffolds) under node and checks the three paths of a
// per-environment value:
//
//   - runtime config wins: config.js sets the key, so a build variable set
//     beside it is ignored and nothing is warned;
//   - deprecated build fallback: config.js does not set it, the build
//     variable does, so the build value is used and ONE console.warn per page
//     load names the variable, the KCL field and the next-major removal;
//   - neither: the defaults (dev's same-origin /_otel, empty in a build,
//     replay off, no environment), and nothing is warned.
//
// The bundler's part is reproduced, not skipped: Vite's
// `import.meta.env.X` is defined from the case's build variables, as `vite
// build` inlines it, and Next's `process.env.X` is the real environment.
func TestTelemetryBuildVariableFallbackBehaviour(t *testing.T) {
	if testing.Short() {
		t.Skip("execs node over the rendered otel_gen.ts; runs in task test")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}

	type result struct {
		Options map[string]any `json:"options"`
		Warns   []string       `json:"warns"`
	}
	type tcase struct {
		name    string
		runtime map[string]string
		build   map[string]string // keyed without the framework prefix
		dev     bool
		check   func(t *testing.T, prefix string, r result)
	}
	cases := []tcase{
		{
			name:    "runtime config wins",
			runtime: map[string]string{"OTEL_ENDPOINT": "/rt", "ENVIRONMENT": "staging", "OTEL_REPLAY": "true"},
			build:   map[string]string{"OTEL_ENDPOINT": "/build", "ENVIRONMENT": "frozen", "OTEL_REPLAY": "false"},
			check: func(t *testing.T, _ string, r result) {
				want := map[string]any{"endpoint": "/rt", "environment": "staging", "replay": true}
				for k, v := range want {
					if r.Options[k] != v {
						t.Errorf("%s = %v, want the runtime value %v", k, r.Options[k], v)
					}
				}
				if len(r.Warns) != 0 {
					t.Errorf("warned with the runtime config set: %q", r.Warns)
				}
			},
		},
		{
			name:  "deprecated build fallback warns once",
			build: map[string]string{"OTEL_ENDPOINT": "/build", "ENVIRONMENT": "frozen", "OTEL_REPLAY": "true"},
			check: func(t *testing.T, prefix string, r result) {
				want := map[string]any{"endpoint": "/build", "environment": "frozen", "replay": true}
				for k, v := range want {
					if r.Options[k] != v {
						t.Errorf("%s = %v, want the build value %v", k, r.Options[k], v)
					}
				}
				if len(r.Warns) != 1 {
					t.Fatalf("want exactly one warning per page load (initTelemetry ran twice), got %d: %q", len(r.Warns), r.Warns)
				}
				w := r.Warns[0]
				for _, part := range []string{
					prefix + "OTEL_ENDPOINT (declare it as config.otel_endpoint)",
					prefix + "ENVIRONMENT (declare it as config.environment)",
					prefix + `OTEL_REPLAY (declare it as runtime_config.OTEL_REPLAY = "true")`,
					"runtime config",
					"deprecated",
					"next major version of forge",
				} {
					if !strings.Contains(w, part) {
						t.Errorf("warning does not say %q:\n%s", part, w)
					}
				}
			},
		},
		{
			name:    "a partial runtime config warns only for the key it lacks",
			runtime: map[string]string{"OTEL_ENDPOINT": "/rt", "ENVIRONMENT": "staging"},
			build:   map[string]string{"OTEL_ENDPOINT": "/build", "OTEL_REPLAY": "true"},
			check: func(t *testing.T, prefix string, r result) {
				if r.Options["endpoint"] != "/rt" || r.Options["replay"] != true {
					t.Errorf("options = %v, want endpoint from runtime, replay from the build", r.Options)
				}
				if len(r.Warns) != 1 || !strings.Contains(r.Warns[0], prefix+"OTEL_REPLAY") ||
					strings.Contains(r.Warns[0], prefix+"OTEL_ENDPOINT") {
					t.Errorf("want one warning naming %sOTEL_REPLAY alone, got %q", prefix, r.Warns)
				}
			},
		},
		{
			name: "neither set: build defaults, no warning",
			check: func(t *testing.T, _ string, r result) {
				if r.Options["endpoint"] != "" || r.Options["replay"] != false {
					t.Errorf("options = %v, want no endpoint and replay off in a build", r.Options)
				}
				if _, ok := r.Options["environment"]; ok {
					t.Errorf("environment = %v, want unset", r.Options["environment"])
				}
				if len(r.Warns) != 0 {
					t.Errorf("warned with nothing set: %q", r.Warns)
				}
			},
		},
		{
			name: "neither set: dev defaults to the same-origin ingest",
			dev:  true,
			check: func(t *testing.T, _ string, r result) {
				if e, _ := r.Options["endpoint"].(string); !strings.HasSuffix(e, "/_otel") {
					t.Errorf("endpoint = %v, want the dev /_otel default", r.Options["endpoint"])
				}
				if len(r.Warns) != 0 {
					t.Errorf("warned with nothing set: %q", r.Warns)
				}
			},
		},
	}

	for _, fw := range []struct{ name, tmpl, prefix string }{
		{"vite-spa", "vite-spa/src/lib/otel_gen.ts.tmpl", "VITE_"},
		{"nextjs", "nextjs/src/lib/otel_gen.ts.tmpl", "NEXT_PUBLIC_"},
	} {
		src := renderTelemetryTmpl(t, fw.tmpl, telemetryData("static"))
		dir := writeTelemetryHarness(t, fw.name, src)
		for _, tc := range cases {
			t.Run(fw.name+"/"+tc.name, func(t *testing.T) {
				build := map[string]string{}
				for k, v := range tc.build {
					build[fw.prefix+k] = v
				}
				runtime, _ := json.Marshal(tc.runtime)
				buildJSON, _ := json.Marshal(build)
				cmd := exec.Command(node, "--experimental-strip-types", "--no-warnings", "main.mjs")
				cmd.Dir = dir
				env := []string{"PATH=" + os.Getenv("PATH"), "FORGE_TEST_RUNTIME=" + string(runtime), "FORGE_TEST_BUILD=" + string(buildJSON)}
				if fw.name == "nextjs" {
					// Next inlines process.env at build: the real env is that.
					for k, v := range build {
						env = append(env, k+"="+v)
					}
					if tc.dev {
						env = append(env, "NODE_ENV=development")
					} else {
						env = append(env, "NODE_ENV=production")
					}
				} else if tc.dev {
					env = append(env, "FORGE_TEST_DEV=1")
				}
				cmd.Env = env
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("node: %v\n%s", err, out)
				}
				var r result
				if err := json.Unmarshal(out[strings.LastIndexByte(strings.TrimSpace(string(out)), '\n')+1:], &r); err != nil {
					t.Fatalf("parse harness output: %v\n%s", err, out)
				}
				tc.check(t, fw.prefix, r)
			})
		}
	}
}

// writeTelemetryHarness lays out a throwaway ESM package that imports the
// rendered module: the runtime's telemetry entry is a stub recording the
// options initTelemetry passes, and Vite's import.meta.env is defined from
// the case's build variables (what `vite build` does to the source).
func writeTelemetryHarness(t *testing.T, platform, src string) string {
	t.Helper()
	dir := t.TempDir()
	src = strings.ReplaceAll(src, `"@reliantlabs/forge-web-runtime/telemetry"`, `"./telemetry_stub.mjs"`)
	src = strings.ReplaceAll(src, `'@reliantlabs/forge-web-runtime/telemetry'`, `"./telemetry_stub.mjs"`)
	src = strings.ReplaceAll(src, `"./apiurl_gen"`, `"./apiurl_gen.ts"`)
	src = strings.ReplaceAll(src, `"./basepath_gen"`, `"./basepath_gen.ts"`)
	if platform == "vite-spa" {
		src = strings.ReplaceAll(src, "import.meta.env.", "globalThis.__viteEnv.")
	}
	files := map[string]string{
		"package.json":    `{"type":"module"}`,
		"otel_gen.ts":     src,
		"apiurl_gen.ts":   `export const DEV_API_URL = "http://localhost:8080";` + "\n",
		"basepath_gen.ts": `export const BASE_PATH = "";` + "\n",
		"telemetry_stub.mjs": `export async function initBrowserTelemetry(options) { globalThis.__captured = options; }
export const context = {}, propagation = {}, trace = {};
export function getTracer() { return {}; }
`,
		"main.mjs": `const build = JSON.parse(process.env.FORGE_TEST_BUILD || "{}");
globalThis.__viteEnv = { ...build, DEV: process.env.FORGE_TEST_DEV === "1" };
globalThis.window = { __FORGE_CONFIG__: JSON.parse(process.env.FORGE_TEST_RUNTIME || "null") || {} };
Object.defineProperty(globalThis, "navigator", {value: JSON.parse(process.env.FORGE_TEST_PRIVACY || "{}"), configurable: true});
const warns = [];
console.warn = (...args) => warns.push(args.join(" "));
const mod = await import("./otel_gen.ts");
mod.initTelemetry();
mod.initTelemetry(); // StrictMode / a second caller: still one warning
process.stdout.write("\n" + JSON.stringify({ options: globalThis.__captured, warns }));
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
