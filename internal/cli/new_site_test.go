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
	root := viteApp(t, "export default { build: { outDir: 'public-build' } }\n")
	if err := runNewSite(t.Context(), newSiteInput{targetPath: root, projectName: "landing", inPlace: true, harness: "reliant"}); err != nil {
		t.Fatalf("runNewSite: %v", err)
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
		{"vite-spa", "vite", "dist", []string{"package.json", "vite.config.ts", "index.html", "tsconfig.json",
			"src/main.tsx", "src/App.tsx", "src/lib/devlog-plugin.ts"}},
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

// The next steps after a site name the skill, the dev loop and the deploy,
// and every command in them resolves in the command tree.
func TestSiteNextStepsArePasteable(t *testing.T) {
	lines := siteNextSteps("landing")
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
