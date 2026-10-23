//go:build e2e

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// The two frontend tests in this file drive the REAL JavaScript toolchain —
// `npm install`, `next build`, `docker build` — and so live in the e2e lane,
// not in the unit lane's `go test -race ./...`.
//
// They began in the unit lane (internal/generator, internal/templates) and
// that was wrong on both of the counts forge's testing tiers name. They were
// slow: the next build test scaffolded a whole frontend and installed its
// full dependency tree, OpenTelemetry web instrumentation included. And they
// were heavy: `next build` ran on the unit job's 2-core/7GB runner alongside
// every other package's -race compile and was OOM-killed (`signal: killed`
// after 461-1029s), failing the Test job on unrelated PRs. The e2e shards run
// on their own runners, and the coverage ledger reports any of these that
// skips instead of running.
//
// The fixtures are also MINIMAL, which is the other half of the fix: each
// installs only the packages its claim needs (~33 for the pin test, not the
// scaffold's hundreds), so a build is seconds and well under a gigabyte.
// nodeBuildHeapMB caps the V8 heap regardless, so a regression back toward
// the heavy fixture fails with a heap error that names itself rather than by
// the runner silently killing the job.

// nodeBuildHeapMB bounds each node process these tests start. The minimal
// fixtures peak well below it (measured: ~750MB RSS for the whole next build).
const nodeBuildHeapMB = 2048

// e2eNodeEnv is the environment every npm/next invocation here runs with.
//
// CI=1 on every run, not only on a CI runner: outside CI, `next build` that
// finds a TypeScript dependency missing INSTALLS it into the fixture and
// carries on, while under CI it refuses and fails. A fixture missing
// @types/node therefore passed on a laptop and failed on the e2e shard — the
// same bytes, two verdicts. Pinning CI makes the build as strict everywhere
// as it is where it counts, and forbids it from editing the fixture it is
// testing.
func e2eNodeEnv() []string {
	return append(os.Environ(),
		"CI=1",
		fmt.Sprintf("NODE_OPTIONS=--max-old-space-size=%d", nodeBuildHeapMB),
		"NEXT_TELEMETRY_DISABLED=1",
	)
}

// outputTimeout runs a command and returns its combined output and error, so
// a test can assert on HOW a command failed — which runCmdTimeout, which
// fails the test on any non-zero exit, cannot express.
func outputTimeout(dir string, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = e2eNodeEnv()
	out, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("timed out after %s: %w", timeout, err)
	}
	return string(out), err
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// pinProbeManifest installs exactly what the bundle-split claim needs: Next,
// React, the runtime, and the runtime's REQUIRED peers the probe imports.
// The runtime's OpenTelemetry peers are optional and nothing here imports
// them, so they are not installed — the reconcile leaves their pins as the
// inert directory pins the template wrote.
const pinProbeManifest = `{
  "name": "pin-probe",
  "private": true,
  "scripts": { "build": "NODE_ENV=production next build --webpack" },
  "dependencies": {
    "@bufbuild/protobuf": "^2.5.0",
    "@connectrpc/connect": "^2.0.0",
    "@reliantlabs/forge-web-runtime": "^0.3.1",
    "@tanstack/react-query": "^5.59.0",
    "next": "^16.3.5",
    "react": "^19.1.0",
    "react-dom": "^19.1.0"
  },
  "devDependencies": {
    "@types/node": "^22.14.0",
    "@types/react": "^19.1.0",
    "typescript": "^5.8.0"
  }
}
`

// The app mounts the QueryClientProvider (app code: resolved through the
// tsconfig pins); the probe page's hook is built INSIDE the runtime
// (createMutationHook calls useQueryClient from node_modules, which the pins
// never touch). They share one React context only if both resolve
// @tanstack/react-query to one module instance.
var pinProbeSources = map[string]string{
	"next.config.mjs": `export default { transpilePackages: ["@reliantlabs/forge-web-runtime"] };
`,
	"app/providers.tsx": `"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";

export function Providers({ children }: { children: ReactNode }) {
  const [client] = useState(() => new QueryClient());
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
`,
	"app/layout.tsx": `import type { ReactNode } from "react";

import { Providers } from "./providers";

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body>
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
`,
	"app/pin-probe/page.tsx": `"use client";

import { createMutationHook } from "@reliantlabs/forge-web-runtime/service-hooks";

const useProbe = createMutationHook(["pin-probe"], async () => "ok");

export default function PinProbePage() {
  const probe = useProbe();
  return <p>pin probe {probe.status}</p>;
}
`,
}

// pinProbeTsconfig carries forge's peer pins exactly as the scaffold templates
// render them before any install exists: every webruntimepeers.TypePins()
// entry, aimed at the package DIRECTORY via the same TypePinPath the
// templates' typePinPath helper calls.
func pinProbeTsconfig() string {
	var pins strings.Builder
	for _, pkg := range webruntimepeers.TypePins() {
		fmt.Fprintf(&pins, "      %q: [%q],\n", pkg, webruntimepeers.TypePinPath(pkg, false, ""))
	}
	return `{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["dom", "dom.iterable", "esnext"],
    "allowJs": true,
    "skipLibCheck": true,
    "strict": true,
    "noEmit": true,
    "esModuleInterop": true,
    "module": "esnext",
    "moduleResolution": "bundler",
    "resolveJsonModule": true,
    "isolatedModules": true,
    "jsx": "preserve",
    "paths": {
` + pins.String() + `      "@/*": ["./*"]
    }
  },
  "include": ["**/*.ts", "**/*.tsx"],
  "exclude": ["node_modules", ".next"]
}
`
}

// TestE2ENextBuildPrerendersWithInstalledPeerPins is the end-to-end proof
// that forge's tsconfig peer pins do not split packages in the bundle.
//
// Next's webpack resolver applies tsconfig `paths` to app code, and a pin that
// names a package DIRECTORY is a directory request that skips the package's
// `exports`. Wherever such a pin resolves, app code bundled react-query's
// build/legacy while the runtime bundled build/modern — two module
// instances, two React contexts:
//
//	Error occurred prerendering page "/pin-probe".
//	Error: No QueryClient set, use QueryClientProvider to set one
//
// The fix aims each pin at the package's DECLARATION FILE, which that resolver
// skips. The test proves both halves in one run, through forge's own code:
//
//  1. the pins as the template writes them (directory pins) must FAIL with
//     exactly that error — so the probe cannot pass by never being exercised;
//  2. forge's post-install reconcile (generator.ReconcileFrontendTsconfigPeers,
//     what `forge project new` / `forge scaffold frontend` run after their
//     `npm install`) must rewrite them to declaration files, after which the
//     build succeeds, prerenders the probe page, and `tsc --noEmit` passes —
//     type dedupe, the only reason the pins exist, still compiles.
func TestE2ENextBuildPrerendersWithInstalledPeerPins(t *testing.T) {
	t.Parallel()
	requireTool(t, "node", "npm")

	root := t.TempDir()
	feDir := filepath.Join(root, "frontends", "web")
	writeFileE2E(t, filepath.Join(feDir, "package.json"), pinProbeManifest)
	for rel, body := range pinProbeSources {
		writeFileE2E(t, filepath.Join(feDir, filepath.FromSlash(rel)), body)
	}
	tsconfig := filepath.Join(feDir, "tsconfig.json")
	writeFileE2E(t, tsconfig, pinProbeTsconfig())

	if out, err := outputTimeout(feDir, 5*time.Minute, "npm", "install", "--no-audit", "--no-fund", "--prefer-offline"); err != nil {
		t.Fatalf("npm install: %v\n%s", err, lastLines(out, 40))
	}
	for _, pkg := range []string{"@tanstack/react-query", "@reliantlabs/forge-web-runtime"} {
		if _, err := os.Stat(filepath.Join(feDir, "node_modules", filepath.FromSlash(pkg), "package.json")); err != nil {
			t.Fatalf("fixture assumption broken: %s is not installed under the frontend, so the "+
				"directory pins do not resolve and the before-half proves nothing: %v", pkg, err)
		}
	}

	// ── Before: the pins as the template renders them. ──
	out, err := outputTimeout(feDir, 5*time.Minute, "npm", "run", "build")
	if err == nil {
		t.Fatalf("next build SUCCEEDED with directory pins; the probe no longer reproduces the bundle "+
			"split, so the after-half proves nothing. Output tail:\n%s", lastLines(out, 40))
	}
	if !strings.Contains(out, "No QueryClient set") {
		t.Fatalf("next build with directory pins failed for a reason other than the bundle split: %v\n%s",
			err, lastLines(out, 60))
	}

	// ── After: forge's post-install reconcile. ──
	generator.ReconcileFrontendTsconfigPeers(root)
	healed := readFileE2E(t, tsconfig)
	for _, want := range []string{
		`"./node_modules/@tanstack/react-query/build/modern/index.d.ts"`,
		`"./node_modules/@types/react/index.d.ts"`,
	} {
		if !strings.Contains(healed, want) {
			t.Fatalf("the reconcile did not aim the pins at declaration files (missing %s):\n%s", want, healed)
		}
	}
	if err := os.RemoveAll(filepath.Join(feDir, ".next")); err != nil {
		t.Fatal(err)
	}
	if out, err := outputTimeout(feDir, 5*time.Minute, "npm", "run", "build"); err != nil {
		t.Fatalf("next build failed with declaration-file pins: %v\n%s", err, lastLines(out, 60))
	}
	html := readFileE2E(t, filepath.Join(feDir, ".next", "server", "app", "pin-probe.html"))
	if !strings.Contains(html, "pin probe") {
		t.Errorf("the probe page was not prerendered with its content:\n%s", lastLines(html, 5))
	}
	if out, err := outputTimeout(feDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit"); err != nil {
		t.Fatalf("tsc --noEmit failed with declaration-file pins: %v\n%s", err, lastLines(out, 40))
	}
}

// TestE2ENextjsDockerfileStaticImageBuilds builds a static Next.js frontend
// with the Dockerfile forge renders for `output: static`, using the frontend
// directory as the build context — exactly CI's `Build <frontend> image` step
// — and serves it.
//
// Before the Dockerfile followed the output mode it copied
// .next-prod/standalone, which an export never writes, and the build failed:
//
//	"/app/.next-prod/standalone": not found
//
// The frontend is a minimal Next app rather than a full scaffold: the claim
// is the Dockerfile's contract with an `output: export` build (npm ci,
// `npm run build` writing out/, the nginx runner serving it), and a minimal
// app exercises all of it. The Dockerfile's render per mode is covered by the
// unit tests in internal/templates/nextjs_dockerfile_test.go.
func TestE2ENextjsDockerfileStaticImageBuilds(t *testing.T) {
	t.Parallel()
	requireTool(t, "docker", "npm")

	feDir := t.TempDir()
	render := func(name string) string {
		t.Helper()
		out, err := templates.FrontendTemplates().Render(name,
			templates.FrontendTemplateData{FrontendName: "web", Output: "static"})
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		return string(out)
	}
	writeFileE2E(t, filepath.Join(feDir, "Dockerfile"), render("nextjs/Dockerfile.tmpl"))
	writeFileE2E(t, filepath.Join(feDir, ".dockerignore"), render("nextjs/.dockerignore"))
	writeFileE2E(t, filepath.Join(feDir, "package.json"), `{"name":"web","private":true,
  "scripts":{"build":"NODE_ENV=production next build --webpack"},
  "dependencies":{"next":"^16.3.5","react":"^19.1.0","react-dom":"^19.1.0"}}
`)
	// The same production gate the scaffold's static next.config.ts uses.
	writeFileE2E(t, filepath.Join(feDir, "next.config.mjs"),
		"export default process.env.NODE_ENV === \"production\" ? { output: \"export\" } : {};\n")
	writeFileE2E(t, filepath.Join(feDir, "app", "layout.jsx"),
		"export default function RootLayout({ children }) {\n  return <html lang=\"en\"><body>{children}</body></html>;\n}\n")
	writeFileE2E(t, filepath.Join(feDir, "app", "page.jsx"),
		"export default function Page() {\n  return <p>static image ok</p>;\n}\n")
	writeFileE2E(t, filepath.Join(feDir, "app", "about", "page.jsx"),
		"export default function About() {\n  return <p>about page</p>;\n}\n")
	writeFileE2E(t, filepath.Join(feDir, "public", "robots.txt"), "User-agent: *\n")
	// A host node_modules the .dockerignore must keep out of the context.
	writeFileE2E(t, filepath.Join(feDir, "node_modules", ".poison"), "a host install must not reach the image\n")

	if out, err := outputTimeout(feDir, 4*time.Minute, "npm", "install", "--package-lock-only", "--no-audit", "--no-fund"); err != nil {
		t.Fatalf("npm install --package-lock-only: %v\n%s", err, lastLines(out, 40))
	}

	id := strings.ToLower(strings.ReplaceAll(filepath.Base(filepath.Dir(feDir))+"-"+filepath.Base(feDir), "_", "-"))
	tag := "forge-e2e-nextjs-static:" + id
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })
	if out, err := outputTimeout(feDir, 10*time.Minute, "docker", "build", "-t", tag, "-f", filepath.Join(feDir, "Dockerfile"), feDir); err != nil {
		t.Fatalf("docker build of a static Next.js frontend failed: %v\n%s", err, lastLines(out, 60))
	}

	name := "forge-e2e-nextjs-static-" + id
	if out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "-p", "127.0.0.1::8080", tag).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	port, err := exec.Command("docker", "port", name, "8080/tcp").Output()
	if err != nil {
		t.Fatalf("docker port: %v", err)
	}
	addr := strings.TrimSpace(strings.Split(string(port), "\n")[0])
	for _, probe := range []struct{ path, want string }{{"/", "static image ok"}, {"/about", "about page"}} {
		body := getWithRetry(t, "http://"+addr+probe.path)
		if !strings.Contains(body, probe.want) {
			t.Errorf("GET %s did not serve the export (want %q):\n%s", probe.path, probe.want, lastLines(body, 5))
		}
	}
	if uid, err := exec.Command("docker", "exec", name, "id", "-u").Output(); err != nil || strings.TrimSpace(string(uid)) == "0" {
		t.Errorf("the static runner runs as root (uid=%q, err=%v)", strings.TrimSpace(string(uid)), err)
	}
	if out, err := exec.Command("docker", "exec", name, "ls", "/usr/share/nginx/html/robots.txt").CombinedOutput(); err != nil {
		t.Errorf("public/ assets are missing from the export: %v\n%s", err, out)
	}
}

// getWithRetry GETs url until it answers 200, for a container still starting.
func getWithRetry(t *testing.T, url string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(url) //nolint:gosec,noctx // a test fetching its own container on loopback
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK {
				return string(body)
			}
			err = readErr
			if err == nil {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s never answered 200: %v", url, err)
		}
		time.Sleep(time.Second)
	}
}
