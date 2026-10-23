package templates

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
)

// The dev-loop API proxy contract: in the dev loop a web frontend's browser
// talks to the API only through the dev server's own origin (`/_api`), so a
// page opened through a Reliant workspace preview URL, a tunnel or a LAN IP
// never dials "localhost:<api port>" — the viewer's own machine. Every build
// keeps reading the runtime config's API_URL. Next.js and Vite both; React
// Native has no page origin and is out of scope.

// TestDevAPIProxy_NextConfig pins, for every output shape, the rewrite, its
// upstream variable, the opt-out, the dev-only gate the static-export lint
// reads, and the `env` hand-off that tells src/lib/api-base.ts the answer.
func TestDevAPIProxy_NextConfig(t *testing.T) {
	for _, output := range []string{"static", "standalone", "server"} {
		t.Run(output, func(t *testing.T) {
			s := renderTelemetryTmpl(t, "nextjs/next.config.ts.tmpl", telemetryData(output))

			for _, want := range []string{
				`const apiUpstream = (process.env.NEXT_PUBLIC_API_URL || "").replace(/\/+$/, "");`,
				`process.env.FORGE_DEV_API_PROXY`,
				`/^(off|false|0|no)$/i`,
				`env: { FORGE_DEV_API_PROXY: devApiProxy },`,
				"{ source: `${devApiProxy}/:path*`, destination: `${apiUpstream}/:path*` }",
			} {
				if !strings.Contains(s, want) {
					t.Errorf("next.config.ts (%s) missing %q", output, want)
				}
			}
			// The proxy decision itself is dev-only, so a production build
			// publishes "" and api-base.ts reads config.js.
			if !regexp.MustCompile(`const devApiProxy =\s*process\.env\.NODE_ENV === "development" &&`).MatchString(s) {
				t.Errorf("next.config.ts (%s): devApiProxy must be gated to NODE_ENV === \"development\"", output)
			}
			// Without allowedDevOrigins `next dev` refuses the HMR socket and
			// dev resources to any non-localhost page, which then renders but
			// never hydrates — the proxy would be unreachable in practice.
			for _, want := range []string{
				`import os from "node:os";`,
				"process.env.RELIANT_PREVIEW_URL_TEMPLATE",
				"process.env.FORGE_DEV_ALLOWED_ORIGINS",
				`...(process.env.NODE_ENV === "development" ? { allowedDevOrigins: devOrigins() } : {}),`,
			} {
				if !strings.Contains(s, want) {
					t.Errorf("next.config.ts (%s) missing %q", output, want)
				}
			}
			// The /_api rewrite sits in the same dev-gated rewrites() as
			// /_otel — the shape `forge lint --static-export` recognises.
			gated := regexp.MustCompile(`process\.env\.NODE_ENV === "development"\s*\?\s*\{\s*async rewrites\(\)\s*\{\s*return \[\s*\{ source: "/_otel/:path\*"[^\n]*\n\s*\.\.\.\(devApiProxy\s*\?\s*\[\{ source: ` + "`" + `\$\{devApiProxy\}/:path\*` + "`")
			if !gated.MatchString(s) {
				t.Errorf("next.config.ts (%s): the /_api rewrite must live in the dev-gated rewrites() beside /_otel", output)
			}
		})
	}
}

// TestDevAPIProxy_ViteConfig pins the Vite twin: same path, VITE_API_URL as
// the upstream, the opt-out, and a `define` only the dev server gets.
func TestDevAPIProxy_ViteConfig(t *testing.T) {
	s := renderTelemetryTmpl(t, "vite-spa/vite.config.ts.tmpl", telemetryData("static"))
	for _, want := range []string{
		`const API_UPSTREAM = (process.env.VITE_API_URL || "").replace(/\/+$/, "");`,
		`process.env.FORGE_DEV_API_PROXY`,
		`/^(off|false|0|no)$/i`,
		`export default defineConfig(({ command, mode }) => {`,
		`const devApiProxy = command === "serve" && mode !== "test" ? DEV_API_PROXY : "";`,
		`{ "import.meta.env.FORGE_DEV_API_PROXY": JSON.stringify(devApiProxy) }`,
		`[devApiProxy]: {`,
		`target: API_UPSTREAM,`,
		`rewrite: (p: string) => p.slice(devApiProxy.length),`,
		// Vite refuses an unrecognised Host header outright.
		"process.env.RELIANT_PREVIEW_URL_TEMPLATE",
		"process.env.FORGE_DEV_ALLOWED_ORIGINS",
		`allowedHosts: devAllowedHosts(5173),`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("vite.config.ts missing %q", want)
		}
	}
}

// TestDevAPIProxy_TransportsAskAPIBase pins that both browser paths to the
// API — the Connect transport and native sign-in — route their configured
// URL through apiBase(), in the typed-config and env-var variants alike. A
// transport that bypassed it would leave sign-in and RPCs pointed at
// different hosts: the cookie set on one and sent to the other.
func TestDevAPIProxy_TransportsAskAPIBase(t *testing.T) {
	for _, typed := range []bool{true, false} {
		data := telemetryData("static")
		data.HasTypedConfig = typed
		data.HasMockAPI = typed

		for _, tc := range []struct{ tmpl, imp, call string }{
			{"nextjs/src/lib/connect.ts.tmpl", `import { apiBase } from "./api-base";`, "return apiBase("},
			{"vite-spa/src/lib/connect.ts.tmpl", `import { apiBase } from "./api-base";`, "return apiBase("},
			{"shared-web/src/lib/auth/native-login.ts.tmpl", `import { apiBase } from "@/lib/api-base";`, "return apiBase(String("},
		} {
			s := renderTelemetryTmpl(t, tc.tmpl, data)
			if got := strings.Count(s, tc.imp); got != 1 {
				t.Errorf("%s (typed=%v): want the import %q exactly once, got %d", tc.tmpl, typed, tc.imp, got)
			}
			if !strings.Contains(s, tc.call) {
				t.Errorf("%s (typed=%v): the configured API URL must go through %q", tc.tmpl, typed, tc.call)
			}
		}
	}
}

// TestDevAPIProxy_ShippedToWebTrees pins that both web kinds scaffold the
// decision module and its test, and React Native — no page origin to proxy
// through — does not.
func TestDevAPIProxy_ShippedToWebTrees(t *testing.T) {
	for kind, want := range map[string]bool{"nextjs": true, "vite-spa": true, "react-native": false} {
		files, err := ListFrontendTree(kind)
		if err != nil {
			t.Fatalf("list %s: %v", kind, err)
		}
		have := map[string]bool{}
		for _, f := range files {
			have[f.Rel] = true
		}
		for _, rel := range []string{"src/lib/api-base.ts", "src/lib/api-base.test.ts"} {
			if have[rel] != want {
				t.Errorf("%s: has %s = %v, want %v", kind, rel, have[rel], want)
			}
		}
	}
}

// TestDevAPIProxy_DevSameOriginProdConfigured is the behavioural gate. It
// is not a string check: it EVALUATES next.config.ts's proxy decision under
// node for each environment, inlines the answer into src/lib/api-base.ts
// the way Next's `env` block does (an esbuild define standing in for
// webpack's DefinePlugin), runs the module, and asserts where a page at a
// workspace preview URL would send its API traffic.
func TestDevAPIProxy_DevSameOriginProdConfigured(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates next.config.ts and api-base.ts under node; runs in task test")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH — skipping dev API proxy evaluation")
	}

	config := renderTelemetryTmpl(t, "nextjs/next.config.ts.tmpl", telemetryData("static"))
	decision := constStatement(t, config, "apiUpstream") + "\n" + constStatement(t, config, "devApiProxy")
	module, err := FrontendTemplates().Get("nextjs/src/lib/api-base.ts")
	if err != nil {
		t.Fatalf("read api-base.ts: %v", err)
	}

	const preview = "https://3000-abc123.workspaces.reliantapi.com"
	for _, tc := range []struct {
		name       string
		env        map[string]string // the dev server's / build's environment
		basePath   string
		configured string // what config.js (or its fallback) names
		want       string
	}{
		{
			name:       "next dev under forge env up: the page's own origin",
			env:        map[string]string{"NODE_ENV": "development", "NEXT_PUBLIC_API_URL": "http://localhost:8085"},
			configured: "http://localhost:8085",
			want:       preview + "/_api",
		},
		{
			name:       "next dev with a base path keeps it in front",
			env:        map[string]string{"NODE_ENV": "development", "NEXT_PUBLIC_API_URL": "http://localhost:8085/"},
			basePath:   "/admin",
			configured: "http://localhost:8085",
			want:       preview + "/admin/_api",
		},
		{
			name:       "production build: config.js's API_URL, untouched",
			env:        map[string]string{"NODE_ENV": "production", "NEXT_PUBLIC_API_URL": "http://localhost:8085"},
			configured: "https://api.example.com",
			want:       "https://api.example.com",
		},
		{
			name:       "opted out: the configured URL, direct",
			env:        map[string]string{"NODE_ENV": "development", "NEXT_PUBLIC_API_URL": "http://localhost:8085", "FORGE_DEV_API_PROXY": "off"},
			configured: "http://localhost:8085",
			want:       "http://localhost:8085",
		},
		{
			name:       "no upstream to proxy to: the configured URL",
			env:        map[string]string{"NODE_ENV": "development"},
			configured: "http://localhost:8085",
			want:       "http://localhost:8085",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := evalNextProxyDecision(t, node, decision, tc.env)
			got := runAPIBase(t, node, string(module), tc.env["NODE_ENV"], proxy, tc.basePath, preview, tc.configured)
			if got != tc.want {
				t.Errorf("api base = %q, want %q (next.config published FORGE_DEV_API_PROXY=%q)", got, tc.want, proxy)
			}
		})
	}

	// The module's own NODE_ENV gate: even a production bundle that somehow
	// carried the proxy path must not use it.
	if got := runAPIBase(t, node, string(module), "production", "/_api", "", preview, "https://api.example.com"); got != "https://api.example.com" {
		t.Errorf("a production bundle must read the configured URL even with a leaked proxy path; got %q", got)
	}

	// The proxy is only reachable if `next dev` lets the page hydrate: the
	// preview host for the port the dev server binds must be an allowed dev
	// origin, plus anything named in FORGE_DEV_ALLOWED_ORIGINS.
	origins := evalNextDevOrigins(t, node, config, map[string]string{
		"RELIANT_PREVIEW_URL_TEMPLATE": "https://{port}-abc123.workspaces.reliantapi.com",
		"PORT":                         "3000",
		"FORGE_DEV_ALLOWED_ORIGINS":    "dev.example.test, *.tunnel.example",
	})
	for _, want := range []string{"3000-abc123.workspaces.reliantapi.com", "dev.example.test", "*.tunnel.example"} {
		if !contains(origins, want) {
			t.Errorf("allowedDevOrigins %v is missing %q", origins, want)
		}
	}
}

// evalNextDevOrigins runs next.config.ts's devOrigins() under node with the
// given environment.
func evalNextDevOrigins(t *testing.T, node, config string, env map[string]string) []string {
	t.Helper()
	start := strings.Index(config, "const devOrigins = ")
	end := strings.Index(config[start:], "\n};\n")
	if start < 0 || end < 0 {
		t.Fatalf("no devOrigins in next.config.ts:\n%s", config)
	}
	res := api.Transform(config[start:start+end+4], api.TransformOptions{Loader: api.LoaderTS})
	if len(res.Errors) > 0 {
		t.Fatalf("compile devOrigins: %v", res.Errors)
	}
	cmd := exec.Command(node, "-e", `const os = require("node:os");`+"\n"+string(res.Code)+
		"process.stdout.write(JSON.stringify(devOrigins()));")
	cmd.Env = proxyTestEnv(env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("evaluating devOrigins: %v\n%s", err, out)
	}
	var origins []string
	if err := json.Unmarshal(out, &origins); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return origins
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// constStatement lifts `const <name> = …;` out of a rendered config. The
// statements it is used on hold no `;` before their end.
func constStatement(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "const "+name+" =")
	if start < 0 {
		t.Fatalf("no `const %s =` in:\n%s", name, src)
	}
	end := strings.Index(src[start:], ";\n")
	if end < 0 {
		t.Fatalf("unterminated `const %s`", name)
	}
	return src[start : start+end+1]
}

// evalNextProxyDecision runs next.config.ts's decision statements under
// node with the given environment and returns the devApiProxy it computes.
func evalNextProxyDecision(t *testing.T, node, decision string, env map[string]string) string {
	t.Helper()
	cmd := exec.Command(node, "-e", decision+"\nprocess.stdout.write(JSON.stringify(devApiProxy));")
	cmd.Env = proxyTestEnv(env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("evaluating next.config.ts's proxy decision: %v\n%s\n%s", err, decision, out)
	}
	var proxy string
	if err := json.Unmarshal(out, &proxy); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	return proxy
}

// runAPIBase compiles api-base.ts the way Next's bundler sees it — NODE_ENV
// and the `env` block's FORGE_DEV_API_PROXY inlined as literals — and
// returns apiBase(configured) for a page at origin.
func runAPIBase(t *testing.T, node, module, nodeEnv, proxy, basePath, origin, configured string) string {
	t.Helper()
	res := api.Transform(module, api.TransformOptions{
		Loader: api.LoaderTS,
		Format: api.FormatCommonJS,
		Define: map[string]string{
			"process.env.NODE_ENV":            jsString(nodeEnv),
			"process.env.FORGE_DEV_API_PROXY": jsString(proxy),
		},
	})
	if len(res.Errors) > 0 {
		t.Fatalf("compile api-base.ts: %v", res.Errors)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "api-base.js"), string(res.Code))
	writeFile(t, filepath.Join(dir, "basepath_gen.js"), "exports.BASE_PATH = "+jsString(basePath)+";\n")

	script := "globalThis.window = { location: { origin: " + jsString(origin) + " } };\n" +
		"const { apiBase } = require(" + jsString(filepath.Join(dir, "api-base.js")) + ");\n" +
		"process.stdout.write(apiBase(" + jsString(configured) + "));"
	cmd := exec.Command(node, "-e", script)
	cmd.Env = proxyTestEnv(nil)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running api-base.ts: %v\n%s", err, out)
	}
	return string(out)
}

// proxyTestEnv is the parent environment minus anything that would leak
// into the decision, plus env.
func proxyTestEnv(env map[string]string) []string {
	var out []string
	for _, kv := range os.Environ() {
		switch strings.SplitN(kv, "=", 2)[0] {
		case "NODE_ENV", "NEXT_PUBLIC_API_URL", "FORGE_DEV_API_PROXY",
			"RELIANT_PREVIEW_URL_TEMPLATE", "PORT", "FORGE_DEV_ALLOWED_ORIGINS":
			continue
		}
		out = append(out, kv)
	}
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
