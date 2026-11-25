package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// viteApp lays down an existing Vite SPA at the root of a fresh directory —
// the shape of a landing page being adopted.
func viteApp(t *testing.T, viteConfig string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"package.json":   `{"name":"landing","devDependencies":{"vite":"^4"}}`,
		"vite.config.ts": viteConfig,
		"index.html":     "<!doctype html>\n",
		".gitignore":     "node_modules/\ndist/\n",
	} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// --kind site writes forge.yaml and the deploy envs, declaring the root app,
// and nothing else: no Go, and not a byte of the app's own files.
func TestRunNewSiteAdoptsTheRootApp(t *testing.T) {
	testkit.Slow(t, "execs git")
	root := viteApp(t, "export default { build: { outDir: 'public-build' } }\n")
	before := readFile(t, filepath.Join(root, "vite.config.ts"))
	if err := runNewSite(t.Context(), newSiteInput{targetPath: root, projectName: "landing", inPlace: true, harness: "reliant"}); err != nil {
		t.Fatalf("runNewSite: %v", err)
	}

	if after := readFile(t, filepath.Join(root, "vite.config.ts")); after != before {
		t.Fatal("adoption rewrote existing application code")
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		main := readFile(t, filepath.Join(root, "deploy/kcl", env, "main.k"))
		if !strings.Contains(main, "_session_replay = False") {
			t.Fatalf("adopted %s silently opted into recording", env)
		}
	}
	for _, absent := range []string{"go.mod", "cmd", "proto", "Dockerfile", "internal", "Taskfile.yml"} {
		if _, err := os.Stat(filepath.Join(root, absent)); err == nil {
			t.Errorf("a site must not get %s", absent)
		}
	}
	yaml := readFile(t, filepath.Join(root, "forge.yaml"))
	if !strings.Contains(yaml, "name: landing") || strings.Contains(yaml, "\nmodule_path:") {
		t.Errorf("forge.yaml must name the site and carry no module_path:\n%s", yaml)
	}
	for _, env := range []string{"dev", "staging", "prod"} {
		main := readFile(t, filepath.Join(root, "deploy", "kcl", env, "main.k"))
		for _, want := range []string{`name = "web"`, `type = "vite"`, `path = "."`, `public_dir = "public-build"`} {
			if !strings.Contains(main, want) {
				t.Errorf("%s/main.k lacks %s:\n%s", env, want, main)
			}
		}
		binder := "_hosted_frontend(_web_frontend)"
		if env == "dev" {
			binder = "_on_host_frontend(_web_frontend)"
		}
		if !strings.Contains(main, binder) {
			t.Errorf("%s/main.k must bind the frontend with %s:\n%s", env, binder, main)
		}
	}
	gi := readFile(t, filepath.Join(root, ".gitignore"))
	if !strings.HasPrefix(gi, "node_modules/\ndist/\n") || !strings.Contains(gi, ".forge/*") {
		t.Errorf(".gitignore must keep the app's entries and gain forge's:\n%s", gi)
	}
	if got := readFile(t, filepath.Join(root, "vite.config.ts")); !strings.Contains(got, "public-build") {
		t.Errorf("the app's vite.config.ts was touched: %q", got)
	}
}

func TestRunNewSiteRefusals(t *testing.T) {
	t.Run("in place, not empty, no frontend at the root", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<!doctype html>\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := runNewSite(t.Context(), newSiteInput{targetPath: root, projectName: "landing", inPlace: true, harness: "reliant"})
		if err == nil || !strings.Contains(err.Error(), "no frontend at its root") || !strings.Contains(err.Error(), "index.html") {
			t.Fatalf("want a refusal naming what is in the way, got %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(root, "forge.yaml")); statErr == nil {
			t.Error("the refusal must come before anything is written")
		}
	})
	t.Run("in place, only an agent's instruction files: not a refusal", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"CLAUDE.md", "AGENTS.md", "reliant.md"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte("# mine\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if empty, blocking := emptyForSite(root); !empty {
			t.Fatalf("a folder holding only agent instruction files must count as empty, blocked by %v", blocking)
		}
	})
	t.Run("a Go module", func(t *testing.T) {
		root := viteApp(t, "export default {}\n")
		if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := runNewSite(t.Context(), newSiteInput{targetPath: root, projectName: "landing", inPlace: true, harness: "reliant"})
		if err == nil || !strings.Contains(err.Error(), "Go module") {
			t.Fatalf("want a Go-module refusal, got %v", err)
		}
	})
	t.Run("--frontend-kind over an existing app", func(t *testing.T) {
		root := viteApp(t, "export default {}\n")
		err := runNewSite(t.Context(), newSiteInput{targetPath: root, projectName: "landing", inPlace: true, harness: "reliant", frontendKind: "nextjs"})
		if err == nil || !strings.Contains(err.Error(), "--frontend-kind") {
			t.Fatalf("want a refusal naming --frontend-kind, got %v", err)
		}
	})
	t.Run("an unknown --frontend-kind", func(t *testing.T) {
		err := runNewSite(t.Context(), newSiteInput{targetPath: filepath.Join(t.TempDir(), "landing"), projectName: "landing", harness: "reliant", frontendKind: "astro"})
		if err == nil || !strings.Contains(err.Error(), "nextjs, vite-spa") {
			t.Fatalf("want a refusal listing the kinds, got %v", err)
		}
	})
}

// The flag combinations `project new` refuses for a site, before writing.
func TestProjectNewSiteFlagRefusals(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"landing", "--kind", "site", "--output", "standalone"}, "--output and --routes"},
		{[]string{"landing", "--kind", "site", "--frontend", "web", "--routes", "none"}, "--output and --routes"},
		{[]string{"landing", "--frontend-kind", "vite-spa"}, "needs --kind site"},
		{[]string{"landing", "--kind", "site", "--frontend", "web,docs"}, "one frontend"},
		{[]string{"landing", "--kind", "site", "--service", "waitlist"}, "--service"},
	} {
		root := t.TempDir()
		cmd := newNewCmd()
		cmd.SetArgs(append(tc.args, "--path", root))
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.ExecuteContext(t.Context())
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: want an error containing %q, got %v", tc.args, tc.want, err)
		}
		if entries, _ := os.ReadDir(root); len(entries) != 0 {
			t.Errorf("%v: wrote %d entries before refusing", tc.args, len(entries))
		}
	}
}

// siteGoArtifacts is everything the service scaffold writes that a site must
// not have: a site is its frontend and its deploy envs.
var siteGoArtifacts = []string{
	"go.mod", "go.work", "cmd", "internal", "pkg", "proto", "gen", "buf.yaml",
	"Dockerfile", "docker-compose.yml", "Taskfile.yml", "frontends", ".githooks",
}

// A greenfield site is a starter app at the root plus exactly what adopting
// that app would write — one project shape for both entry points — and
// nothing of the Go project.
func TestGreenfieldSiteIsFrontendOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the whole `project new --kind site`, which execs git (init + initial commit); runs in task test")
	}
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "1")
	for _, tc := range []struct {
		kind, feType, publicDir string
		present                 []string
	}{
		{"", "nextjs", "out", []string{"package.json", "next.config.ts", "tsconfig.json", "postcss.config.mjs",
			"src/app/layout.tsx", "src/app/page.tsx", "src/app/dev-logging.tsx", "src/app/%5F_forge/log/route.ts"}},
		// The Vite dev-log receiver comes from the runtime at the current floor
		// (no inlined src/lib/devlog-plugin.ts); the generator's
		// TestViteSiteStarterFollowsTheRuntimeFloor checks both receiver paths.
		{"vite-spa", "vite", "dist", []string{"package.json", "vite.config.ts", "index.html", "tsconfig.json",
			"src/main.tsx", "src/App.tsx"}},
	} {
		t.Run(tc.feType, func(t *testing.T) {
			parent := t.TempDir()
			args := []string{"landing", "--kind", "site", "--path", parent}
			if tc.kind != "" {
				args = append(args, "--frontend-kind", tc.kind)
			}
			cmd := newNewCmd()
			cmd.SetArgs(args)
			if err := cmd.ExecuteContext(t.Context()); err != nil {
				t.Fatalf("project new %v: %v", args, err)
			}
			root := filepath.Join(parent, "landing")
			for _, rel := range append(tc.present, "forge.yaml", ".gitignore", "deploy/kcl/kcl.mod") {
				if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
					t.Errorf("a %s site must have %s: %v", tc.feType, rel, err)
				}
			}
			for _, absent := range siteGoArtifacts {
				if _, err := os.Stat(filepath.Join(root, absent)); err == nil {
					t.Errorf("a site must not get %s", absent)
				}
			}
			if _, err := os.Stat(filepath.Join(root, ".forge", ".scaffold-in-progress")); err == nil {
				t.Error("the scaffold marker survived a finished scaffold")
			}
			pkg := readFile(t, filepath.Join(root, "package.json"))
			if !strings.Contains(pkg, `"name": "landing"`) || !strings.Contains(pkg, "@reliantlabs/forge-web-runtime") {
				t.Errorf("package.json must name the site and declare the web runtime (installDevLogging):\n%s", pkg)
			}
			// The runtime's barrel imports these at module top level and
			// marks them optional peers, so the site must declare them.
			for _, want := range []string{"@connectrpc/connect", "@bufbuild/protobuf", "@opentelemetry/api"} {
				if !strings.Contains(pkg, `"`+want+`"`) {
					t.Errorf("a site's package.json must declare %s:\n%s", want, pkg)
				}
			}
			if strings.Contains(pkg, "{{") {
				t.Errorf("a site's package.json has an unrendered action:\n%s", pkg)
			}
			for _, env := range []string{"dev", "staging", "prod"} {
				main := readFile(t, filepath.Join(root, "deploy", "kcl", env, "main.k"))
				for _, want := range []string{`name = "web"`, `type = "` + tc.feType + `"`, `path = "."`, `public_dir = "` + tc.publicDir + `"`} {
					if !strings.Contains(main, want) {
						t.Errorf("%s/main.k lacks %s:\n%s", env, want, main)
					}
				}
			}
			gi := readFile(t, filepath.Join(root, ".gitignore"))
			if !strings.Contains(gi, "node_modules/") || !strings.Contains(gi, ".forge/*") {
				t.Errorf(".gitignore must carry the app's entries and forge's:\n%s", gi)
			}
			yaml := readFile(t, filepath.Join(root, "forge.yaml"))
			if strings.Contains(yaml, "\nmodule_path:") {
				t.Errorf("a site's forge.yaml has no module_path:\n%s", yaml)
			}
			// The project's frontend inventory sees the root app, like an
			// adopted one.
			fe, ok := config.DiscoverRootFrontend(root)
			if !ok || fe.Name != config.RootFrontendName {
				t.Errorf("the root app is not discovered as the site's frontend: %+v %v", fe, ok)
			}
		})
	}
}

// `--in-place` in an empty directory is a greenfield site there; the
// directory's README and dotfiles stay.
func TestGreenfieldSiteInPlaceInAnEmptyDir(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the whole `project new --kind site`, which execs git (init + initial commit); runs in task test")
	}
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "1")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runNewSite(t.Context(), newSiteInput{targetPath: root, projectName: "landing", inPlace: true, harness: "reliant", frontendKind: "vite-spa", frontendName: "site"})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "README.md")); got != "# mine\n" {
		t.Errorf("README.md was touched: %q", got)
	}
	main := readFile(t, filepath.Join(root, "deploy", "kcl", "prod", "main.k"))
	if !strings.Contains(main, `name = "site"`) || !strings.Contains(main, `type = "vite"`) {
		t.Errorf("--frontend names the frontend in every env:\n%s", main)
	}
	if _, err := os.Stat(filepath.Join(root, "vite.config.ts")); err != nil {
		t.Errorf("no starter app written: %v", err)
	}
}

// A Vite site's next steps never offer `lint --static-export`, which checks
// only a Next.js export; its build is the check.
func TestSiteNextStepsForViteNameTheBuild(t *testing.T) {
	text := strings.Join(siteNextSteps("", "vite"), "\n")
	if strings.Contains(text, " lint --static-export\n") || !strings.Contains(text, "npm run build") {
		t.Errorf("a Vite site's next steps must name npm run build, not lint --static-export:\n%s", text)
	}
}

// The next steps after a site name the skill, the dev loop and the deploy,
// and every command in them resolves in the command tree.
func TestSiteNextStepsArePasteable(t *testing.T) {
	lines := siteNextSteps("landing", "nextjs")
	want := map[string]bool{"skill load": false, "env up": false, "env deploy": false, "env build": false, "lint": false, "domain add": false}
	for _, line := range lines {
		for _, args := range pasteableInvocations(line, Name()) {
			path := resolvePasteable(t, args)
			if _, ok := want[path]; ok {
				want[path] = true
			}
		}
	}
	for cmd, seen := range want {
		if !seen {
			t.Errorf("site next steps never name `%s`:\n%s", cmd, strings.Join(lines, "\n"))
		}
	}
	joined := strings.Join(lines, "\n")
	for _, s := range []string{"deploy/static-site", "env up dev", "env deploy prod", "cd landing"} {
		if !strings.Contains(joined, s) {
			t.Errorf("site next steps lack %q:\n%s", s, joined)
		}
	}
}

// A harness that reads a memory file gets the SITE memory, not the Go one.
func TestSiteWritesTheSiteMemoryForAFileHarness(t *testing.T) {
	testkit.Slow(t, "execs git")
	root := viteApp(t, "export default {}\n")
	if err := runNewSite(t.Context(), newSiteInput{targetPath: root, projectName: "landing", inPlace: true, harness: "claude"}); err != nil {
		t.Fatal(err)
	}
	// AGENTS.md holds the memory; CLAUDE.md is the pointer that imports it.
	memory := readFile(t, filepath.Join(root, "AGENTS.md"))
	if !strings.Contains(memory, "static site") || strings.Contains(memory, "This is a Go project") {
		t.Errorf("AGENTS.md is not the site memory:\n%s", memory)
	}
	if pointer := readFile(t, filepath.Join(root, "CLAUDE.md")); !strings.Contains(pointer, "AGENTS.md") {
		t.Errorf("CLAUDE.md does not import AGENTS.md:\n%s", pointer)
	}
}

// The service scaffold over a root frontend used to write a Go server beside
// the app and never declare it. With no --kind it now stops and names
// --kind site, before writing anything.
func TestProjectNewInPlaceOverARootFrontendPointsAtKindSite(t *testing.T) {
	root := viteApp(t, "export default {}\n")
	err := runNew(t.Context(), "landing", root, "", "", nil, nil, "", true, false, nil, "reliant", true, "local", "per-service", false, false, false, false, newFrontendOptions{})
	if err == nil || !strings.Contains(err.Error(), "--kind site") {
		t.Fatalf("want a refusal naming --kind site, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "forge.yaml")); statErr == nil {
		t.Error("the refusal must come before anything is written")
	}
}

// A site's generate skips every step that needs Go (the tidies, goimports,
// the compile check, the contract emitter, workloads.k, the CI workflows
// that install forge from go.mod) instead of failing on `pattern ./...`.
func TestGenerateSkipsTheGoStepsForASite(t *testing.T) {
	testkit.Slow(t, "execs git")
	root := viteApp(t, "export default {}\n")
	if err := runNewSite(t.Context(), newSiteInput{targetPath: root, projectName: "landing", inPlace: true, harness: "reliant"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectConfigFrom(filepath.Join(root, "forge.yaml"))
	if err != nil {
		t.Fatalf("load the site's forge.yaml: %v", err)
	}
	ctx, err := newPipelineContextWithFlags(root, pipelineFlags{})
	if err != nil {
		t.Fatal(err)
	}
	ctx.Cfg = cfg
	goSteps := map[string]bool{
		"ensure gen/go.mod": true, "go mod tidy (gen/)": true, "go mod tidy (root)": true,
		"goimports on generated Go": true, "go build (validate generated code)": true,
		"internal package contracts": true, "workloads.k (scaffold once)": true, "CI workflows": true,
	}
	for _, step := range generateSteps() {
		if goSteps[step.Name] {
			if step.Gate(ctx) {
				t.Errorf("step %q runs on a project with no go.mod", step.Name)
			}
			delete(goSteps, step.Name)
		}
	}
	for name := range goSteps {
		t.Errorf("step %q is no longer in the plan; update this test", name)
	}
}

// A greenfield site's production build must work from a clean install of what
// the starter declares: the runtime's barrel imports @connectrpc/connect,
// @bufbuild/protobuf and @opentelemetry/api, which it marks optional peers, so
// a starter that omits them builds nowhere but a machine that has them hoisted.
// Resolves the runtime from the registry (the starter's published range).
func TestGreenfieldSiteProductionBuilds(t *testing.T) {
	testkit.Slow(t, "scaffolds each site kind, runs npm install and its production build (network)")
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not on PATH")
	}
	for _, kind := range []string{"nextjs", "vite-spa"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			cmd := newNewCmd()
			cmd.SetArgs([]string{"landing", "--kind", "site", "--path", parent, "--frontend-kind", kind})
			if err := cmd.ExecuteContext(t.Context()); err != nil {
				t.Fatalf("project new: %v", err)
			}
			root := filepath.Join(parent, "landing")
			if _, err := os.Stat(filepath.Join(root, "node_modules")); err != nil {
				t.Fatalf("project new did not install dependencies: %v", err)
			}
			build := exec.CommandContext(t.Context(), "npm", "run", "build")
			build.Dir = root
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("npm run build failed for a fresh %s site: %v\n%s", kind, err, out)
			}
		})
	}
}

// A stock Vite app never reads PORT, so the dev env declares the port it
// really listens on; a config that reads PORT keeps the resolved port.
func TestViteFixedDevPort(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		want         int
	}{
		{"stock create-vite config", "import { defineConfig } from 'vite'\nexport default defineConfig({ plugins: [react()] })\n", 5173},
		{"literal server.port", "export default { server: { host: true, port: 4321 } }\n", 4321},
		{"port outside the server block is not the dev port", "export default { preview: { port: 4173 }, server: { open: true } }\n", 5173},
		{"reads PORT, like forge's starter", "const port = Number(process.env.PORT) || 5173;\nexport default { server: { port } }\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := viteFixedDevPort([]byte(tc.config)); got != tc.want {
				t.Errorf("viteFixedDevPort = %d, want %d", got, tc.want)
			}
		})
	}
	t.Run("no vite.config at all is Vite's default", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"devDependencies":{"vite":"^7"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		fe, ok := siteFrontendAt(root)
		if !ok || fe.DevPort != viteDefaultDevPort {
			t.Fatalf("siteFrontendAt = %+v, %v; want DevPort %d", fe, ok, viteDefaultDevPort)
		}
	})
}

// nextApp lays down a create-next-app@latest (Next 16) app at the root.
func nextApp(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"package.json":   `{"name":"shop","dependencies":{"next":"16.0.0","react":"19.0.0"}}`,
		"next.config.ts": "import type { NextConfig } from \"next\";\n\nconst nextConfig: NextConfig = {\n  cacheComponents: true,\n};\n\nexport default nextConfig;\n",
		"app/page.tsx":   "export default function Page() { return <h1>hi</h1>; }\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Adopting a Next.js app names what the hosted static build would refuse
// (no `output: "export"`, cacheComponents), with file:line and a fix that
// fits a site — not `forge project upgrade`, which has no static next.config
// to offer a project with no frontend output in forge.yaml.
func TestAdoptedNextSiteNamesTheExportFindings(t *testing.T) {
	root := nextApp(t)
	fe, ok := siteFrontendAt(root)
	if !ok {
		t.Fatal("no Next.js app detected")
	}
	out := captureStdout(t, func() { printAdoptedSiteNotes(root, fe) })
	for _, want := range []string{
		"next.config.ts:3 [static-export-not-exported]",
		"next.config.ts:4 [static-export-partial-prerender]",
		"add `output: \"export\"`",
		"@reliantlabs/forge-web-runtime/devlog",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("adoption notes lack %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "project upgrade") {
		t.Errorf("a root site's fix must not send the user to `forge project upgrade`:\n%s", out)
	}
}

// --dry-run previews a site adoption: every forge file with its content,
// what it keeps, and nothing written.
func TestRunNewSiteDryRunWritesNothing(t *testing.T) {
	testkit.Slow(t, "execs git")
	root := viteApp(t, "export default { server: { port: 4321 } }\n")
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = runNewSite(t.Context(), newSiteInput{targetPath: root, projectName: "landing", inPlace: true, harness: "none", dryRun: true})
	})
	if runErr != nil {
		t.Fatalf("runNewSite --dry-run: %v", runErr)
	}
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("--dry-run wrote into the app: %d entries before, %d after", len(before), len(after))
	}
	if got := readFile(t, filepath.Join(root, ".gitignore")); got != "node_modules/\ndist/\n" {
		t.Errorf("--dry-run touched .gitignore: %q", got)
	}
	for _, want := range []string{
		"Nothing was written",
		"----- forge.yaml -----",
		"----- deploy/kcl/dev/main.k -----",
		"    port = 4321\n",
		".gitignore (yours, with",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output lacks %q:\n%s", want, out)
		}
	}
}

// --dry-run is accepted for --kind site, and still refused for kinds it
// cannot preview.
func TestProjectNewDryRunKinds(t *testing.T) {
	root := t.TempDir()
	cmd := newNewCmd()
	cmd.SetArgs([]string{"landing", "--kind", "cli", "--dry-run", "--path", root})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "--kind app or --kind site") {
		t.Errorf("--kind cli --dry-run: want a refusal naming app and site, got %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("wrote %d entries before refusing", len(entries))
	}
}
