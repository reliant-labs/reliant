//go:build e2e

package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2EScaffoldFullSpecProject is the authoritative regression test for
// `forge project new`'s scaffold output. It exercises the exact invocation promised
// in the README:
//
//	forge project new demo --mod github.com/example/demo --service api --frontend web
//
// and then verifies — in order of blast radius —
//  1. the generated tree compiles, vets, and tests cleanly
//  2. `go mod tidy` is a no-op (guards against drift in go.mod/go.sum)
//  3. the linters (golangci-lint, buf) — required, not optional: skipped by
//     name on a laptop that lacks them, hard failure in CI
//  4. every file listed in the spec actually exists
//  5. specific byte-level content guards that protect against known
//     past regressions (see inline comments on each guard).
//
// Each guard traces back to a bug we've already shipped to users once.
// Don't soften them without replacing with an equivalent check.
func TestE2EScaffoldFullSpecProject(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	// Exact invocation from the spec. Any deviation here reduces the value
	// of this test as a user-facing regression guard.
	runCmd(t, dir, forgeBin,
		"project", "new", "demo",
		"--mod", "github.com/example/demo",
		"--service", "api",
		"--frontend", "web",
	)

	projectDir := filepath.Join(dir, "demo")
	addCorpusForgePkgReplace(t, projectDir)

	// Let `forge generate` fill in the derived sources (gen/, bootstrap,
	// handler stubs). Without this step `go build` fails because the proto
	// stubs don't exist yet.
	runCmd(t, projectDir, forgeBin, "generate")

	// `go mod tidy` across both module boundaries (project + gen). The
	// post-generate state is what users see after cloning a scaffold.
	runCmd(t, projectDir, "go", "mod", "tidy")
	runCmd(t, filepath.Join(projectDir, "gen"), "go", "mod", "tidy")

	// Capture go.mod/go.sum and ensure a second tidy is a no-op. Drift here
	// means `forge project new` emits a go.mod that's not self-consistent, which
	// surfaces as "your go.mod is out of sync" on fresh clones.
	modBefore := readFileE2E(t, filepath.Join(projectDir, "go.mod"))
	runCmd(t, projectDir, "go", "mod", "tidy")
	modAfter := readFileE2E(t, filepath.Join(projectDir, "go.mod"))
	if modBefore != modAfter {
		t.Fatalf("go mod tidy is not idempotent on the scaffold output\nbefore:\n%s\nafter:\n%s",
			modBefore, modAfter)
	}

	// Core toolchain checks.
	runCmd(t, projectDir, "go", "build", "./...")
	runCmd(t, projectDir, "go", "vet", "./...")
	runCmd(t, projectDir, "go", "test", "./...")

	// The golangci-lint / buf lint gates used to sit here, each behind an
	// `if toolAvailable(...)`. On a box without the tool that is not a
	// weaker test, it is NO test — the assertion vanished and the function
	// still reported PASS. They now live at the END of this function behind
	// requireTool, which fails in CI and skips (loudly, by name) on a
	// laptop. Moving them there is what makes the laptop skip cheap: every
	// structural guard below has already run by the time it can fire.

	// ── File-existence guards ──────────────────────────────────────────
	// Every path here corresponds to an item in the spec. Keep this list
	// in sync with the scaffold; it's the checklist a user would run
	// after `forge project new` to make sure nothing's missing.
	mustExist := []string{
		// cmd/ — a dir-nested cobra tree under cmd/<bin>/, one file per
		// top-level concern. OTel is no longer its own file: serverkit
		// owns tracer setup from the serve config, so the wiring lives
		// in serve.go beside the rest of the pipeline it configures.
		"cmd/demo/main.go",
		"cmd/demo/cmd/root.go",
		"cmd/demo/cmd/serve.go",
		// One generated command per root of the composition graph;
		// `server` is the default root's.
		"cmd/demo/cmd/roots_gen.go",
		"cmd/demo/cmd/version.go",
		"cmd/demo/cmd/db.go",

		// pkg/middleware — the ONE thin user-owned auth-policy file (plus
		// its policy test). The security-critical mechanisms (auth modes,
		// auth interceptor, CORS, recovery, audit, rate limiting, …) live
		// in forge/pkg/{authn,middleware,observe} and are NOT
		// photocopied into the scaffold anymore.
		"pkg/middleware/middleware.go",
		"pkg/middleware/middleware_test.go",

		// Service + proto — the scaffold's raison d'être.
		"internal/handlers/api/service.go",
		"proto/services/api/v1/api.proto",

		// Frontend — go.mod boundary marker plus real frontend files.
		"frontends/web/package.json",
		"frontends/web/go.mod",
		"frontends/web/buf.gen.yaml",
		// admin-url helper — basePath-aware URL builder for Stripe success
		// URLs, OAuth callbacks, etc. (see frontend skill for canonical usage).
		"frontends/web/src/lib/admin-url.ts",

		// Core UI components — installed during scaffold.
		"frontends/web/src/components/ui/sidebar_layout.tsx",
		"frontends/web/src/components/ui/page_header.tsx",
		"frontends/web/src/components/ui/badge.tsx",
		"frontends/web/src/components/ui/modal.tsx",
		"frontends/web/src/components/ui/skeleton_loader.tsx",
		"frontends/web/src/components/ui/pagination.tsx",
		"frontends/web/src/components/ui/search_input.tsx",
		"frontends/web/src/components/ui/alert_banner.tsx",
		"frontends/web/src/components/ui/toast_notification.tsx",
		"frontends/web/src/components/ui/key_value_list.tsx",

		// CI workflows. The scaffold's staging and prod are hosted (#518),
		// so the deploy path is release.yml driving the vendored
		// forge-deploy action — see the deploy.yml guard below.
		".github/workflows/ci.yml",
		".github/workflows/build-images.yml",
		".github/workflows/release.yml",
		".github/actions/forge-deploy/action.yml",
		".github/workflows/e2e.yml",

		// KCL deploy manifests. Per-env main.k files only — the
		// shared schemas (Service / Operator / Frontend / CronJob,
		// deploy union, render layer) live in the upstream `forge`
		// KCL module, which the binary supplies at render time;
		// deploy/kcl/kcl.mod is the KCL package root.
		"deploy/kcl/kcl.mod",
		"deploy/kcl/dev/main.k",
		"deploy/kcl/staging/main.k",
		"deploy/kcl/prod/main.k",

		// Top-level project documentation.
		"README.md",
		"CONTRIBUTING.md",
		"CHANGELOG.md",
	}
	for _, rel := range mustExist {
		assertPathExistsE2E(t, filepath.Join(projectDir, rel))
	}
	// Past bug (#518): the first generate of a fresh project took its hosted
	// envs for cluster envs and wrote a deploy.yml beside release.yml. A
	// project whose every env is hosted has no env for deploy.yml to deploy,
	// and a deploy.yml with no env renders no trigger and no job — a workflow
	// GitHub rejects as invalid on every push.
	assertPathNotExistsE2E(t, filepath.Join(projectDir, ".github", "workflows", "deploy.yml"))

	// The handler package is generated for the scaffolded service. There is
	// deliberately no handlers_gen.go: the pb-through collapse made every
	// RPC an OWNED file forge writes once (one per RPC, so two authors never
	// collide) rather than a single regenerated bag of stubs. A bare service
	// declares no RPCs, so what must exist here is the package and its
	// generated test harness — the per-RPC files appear as RPCs are
	// declared, which the pb-through e2e covers.
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "api", "service.go"))
	assertPathExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "api", "helpers_gen_test.go"))
	assertPathNotExistsE2E(t, filepath.Join(projectDir, "internal", "handlers", "api", "handlers_gen.go"))

	// ── Byte-level anti-regression content guards ─────────────────────
	// Each guard below is paired with the bug that produced it. If you
	// need to remove a guard, first remove the corresponding bug risk.

	// The serve PIPELINE (listener lifecycle, driver registration) lives in
	// serve.go's runRoot; roots_gen.go holds the thin cobra command per root
	// over it. Both guards below are about the pipeline, so they read
	// serve.go.
	serveGo := readFileE2E(t, filepath.Join(projectDir, "cmd", "demo", "cmd", "serve.go"))
	// Past bug: server used a raw error compare on srv.Serve's return,
	// which misclassified wrapped errors. The listener lifecycle (and
	// its errors.Is(err, http.ErrServerClosed) handling — see
	// pkg/serverkit) now lives in serverkit; the policy must hand the
	// lifecycle off rather than half-reimplementing it. A new project
	// composes with the graph: serve.go's runRoot runs the root a command
	// names (root.Run, a Run<Root> that supervises every node), and the HTTP
	// listener is the APIListener provider, a serverkit.Listener.
	if !strings.Contains(serveGo, "return root.Run(g, ctx, lifecyclekit.Policy{") {
		t.Errorf("cmd/demo/cmd/serve.go must hand the serve lifecycle to the root's supervisor (root.Run); got:\n%s",
			excerpt(serveGo, "runRoot", 400))
	}
	if strings.Contains(serveGo, ".ListenAndServe(") || strings.Contains(serveGo, "srv.Serve(") {
		t.Errorf("cmd/demo/cmd/serve.go reimplements the listener lifecycle serverkit owns; got:\n%s",
			excerpt(serveGo, "Serve", 400))
	}
	providersGo := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "providers.go"))
	if !strings.Contains(providersGo, "serverkit.NewListener(") {
		t.Errorf("internal/app/providers.go must build the API listener with serverkit.NewListener; got:\n%s",
			excerpt(providersGo, "APIListener", 400))
	}
	// Past bug: server used the `postgres://` URL directly with
	// database/sql, which has no registered driver named "postgres" in a
	// bare binary. pgx/v5/stdlib registers a "pgx" driver that accepts
	// postgres URLs; blank-importing it is the fix.
	if !strings.Contains(serveGo, `_ "github.com/jackc/pgx/v5/stdlib"`) {
		t.Errorf("cmd/demo/cmd/serve.go must blank-import github.com/jackc/pgx/v5/stdlib; got:\n%s",
			excerpt(serveGo, "import", 400))
	}

	authGo := readFileE2E(t, filepath.Join(projectDir, "pkg", "middleware", "middleware.go"))
	// Past bug: the unauthenticated allow-list was implemented as
	// `strings.Contains(procedure, "Health")`, which matches any RPC with
	// "Health" anywhere in its name — e.g. a user-defined `HealthReport`
	// silently bypassed auth. The thin policy file must declare an
	// exact-match map (the gate itself lives in forge/pkg/authn).
	healthContains := regexp.MustCompile(`strings\.Contains\([^)]*Health`)
	if healthContains.MatchString(authGo) {
		t.Errorf("pkg/middleware/middleware.go must not use strings.Contains(...Health...) for unauthenticated allow-list; use exact procedure matching instead. Got:\n%s",
			authGo)
	}
	// The allow-list is DERIVED from the protos' auth_required declarations
	// and lives in the generated procedures_gen.go; the policy file consumes
	// it. A hand-written copy here would be a second declaration surface for
	// one fact, able to disagree with the annotation that `forge project
	// graph` reports.
	if !strings.Contains(authGo, "Unauthenticated:     UnauthenticatedProcedures") {
		t.Errorf("pkg/middleware/middleware.go must wire the generated UnauthenticatedProcedures into the interceptor; got:\n%s", authGo)
	}
	proceduresGo := readFileE2E(t, filepath.Join(projectDir, "pkg", "middleware", "procedures_gen.go"))
	for _, probe := range []string{"/grpc.health.v1.Health/Check", "/grpc.health.v1.Health/Watch"} {
		if !strings.Contains(proceduresGo, probe) {
			t.Errorf("pkg/middleware/procedures_gen.go must allow %s (probes run before anything can authenticate); got:\n%s", probe, proceduresGo)
		}
	}

	// Past bug: PORT was parsed with `strconv.Atoi` (int) which accepts
	// values outside the port range and then silently truncates when
	// assigned; a width-checked ParseUint rejects at parse time instead.
	//
	// There is no longer a generated per-field loader to grep for that
	// call: pkg/config/config_gen.go is a thin shim over the generic,
	// descriptor-driven loader in forge/pkg/config, which does the
	// width-checked parse once for every numeric field (see typedValue in
	// pkg/config/loader.go, covered by that package's own tests). What this
	// gate can still prove is that the scaffold routes config through that
	// library rather than growing a hand-rolled parser again.
	configGo := readFileE2E(t, filepath.Join(projectDir, "pkg", "config", "config_gen.go"))
	if !strings.Contains(configGo, `forgeconfig "github.com/reliant-labs/forge/pkg/config"`) {
		t.Errorf("pkg/config/config_gen.go must delegate loading to forge/pkg/config (which width-checks numeric parses); got:\n%s",
			excerpt(configGo, "import", 400))
	}
	if strings.Contains(configGo, "strconv.Atoi(") {
		t.Errorf("pkg/config/config_gen.go must not hand-roll an unchecked strconv.Atoi port parse; got:\n%s",
			excerpt(configGo, "Atoi", 400))
	}

	frontendBufGen := readFileE2E(t, filepath.Join(projectDir, "frontends", "web", "buf.gen.yaml"))
	// Past bug: `include_imports: true` was nested under `opt:` in buf
	// v2, which bufbuild/es rejects as an unknown option. It must be a
	// sibling of `out:` / `opt:`, not an element of the opt list.
	if !strings.Contains(frontendBufGen, "include_imports: true") {
		t.Errorf("frontends/web/buf.gen.yaml must set include_imports: true; got:\n%s",
			frontendBufGen)
	}
	assertIncludeImportsPlacement(t, frontendBufGen)

	frontendLayout := readFileE2E(t, filepath.Join(projectDir, "frontends", "web", "src", "app", "layout.tsx"))
	// Component library integration: the layout must wire the scaffold's
	// shared chrome. That chrome is AppShell, which renders <Nav /> and
	// <MobileNav /> itself — so the layout composes AppShell and does NOT
	// name Nav directly.
	//
	// This assertion used to look for the literal "Nav" in layout.tsx, which
	// silently became a test of the old pre-AppShell structure: it passed
	// only because the layout happened to import Nav directly, and it failed
	// the moment the chrome was factored into one component without anything
	// actually regressing. Assert the composition that carries the chrome,
	// and let app_shell's own template own what goes inside it.
	if !strings.Contains(frontendLayout, "AppShell") {
		t.Errorf("frontends/web/src/app/layout.tsx must compose the AppShell chrome; got:\n%s",
			excerpt(frontendLayout, "import", 400))
	}
	appShell := readFileE2E(t, filepath.Join(projectDir, "frontends", "web", "src", "components", "app_shell.tsx"))
	if !strings.Contains(appShell, "Nav") {
		t.Errorf("frontends/web/src/components/app_shell.tsx must render the Nav component; got:\n%s",
			excerpt(appShell, "import", 400))
	}

	gitignore := readFileE2E(t, filepath.Join(projectDir, ".gitignore"))
	// Past bug: `.gitignore` ignored `cmd/*.go` because those were
	// regenerated on every `forge generate`. After the "scaffold-once,
	// user-owns-it" refactor they must be tracked by git; re-ignoring
	// them breaks fresh clones.
	if hasGitignoreRule(gitignore, "cmd/*.go") {
		t.Errorf(".gitignore must not ignore cmd/*.go (user-owned after scaffold); got:\n%s",
			gitignore)
	}

	// Past bug: generated error strings were ALL-CAPS ("HANDLER FOR %s
	// NOT YET IMPLEMENTED") which violates Go's error-string convention
	// (lowercase, no trailing punctuation) and lint-fails on any
	// user-configured staticcheck.
	//
	// The stub's own message now comes from svcerr.ScaffoldStub rather than
	// being formatted in the emitted file, and handlers_gen.go no longer
	// exists at all (one owned file per RPC). So the guard sweeps every Go
	// file forge writes into the handler package instead of one filename —
	// which is strictly wider than what it replaced.
	handlerDir := filepath.Join(projectDir, "internal", "handlers", "api")
	handlerFiles, err := filepath.Glob(filepath.Join(handlerDir, "*.go"))
	if err != nil || len(handlerFiles) == 0 {
		t.Fatalf("no Go files in %s (glob err %v) — the uppercase-error-string guard would be vacuous", handlerDir, err)
	}
	for _, f := range handlerFiles {
		if content := readFileE2E(t, f); hasUpperCaseErrorString(content) {
			t.Errorf("%s must use lowercase error strings (Go convention); got:\n%s",
				filepath.Base(f), content)
		}
	}

	// ── Lint gates (spec item 3) ───────────────────────────────────────
	// Last, deliberately: requireTool skips the whole test when the tool is
	// absent, and by here every guard above has already asserted.
	requireTool(t, "golangci-lint", "buf")
	runGolangciLintE2E(t, projectDir, "./...")
	runCmd(t, projectDir, "buf", "lint")
}

// assertIncludeImportsPlacement fails the test if `include_imports` appears
// as a list item under `opt:` instead of as a top-level plugin field.
//
// Layout expected (correct):
//
//	plugins:
//	  - local: ./frontends/web/node_modules/.bin/protoc-gen-es
//	    out: ...
//	    include_imports: true
//	    opt:
//	      - target=ts
//
// Layout rejected (past bug):
//
//	plugins:
//	  - local: ./frontends/web/node_modules/.bin/protoc-gen-es
//	    out: ...
//	    opt:
//	      - target=ts
//	      - include_imports=true
func assertIncludeImportsPlacement(t *testing.T, content string) {
	t.Helper()
	// If include_imports appears inside an `opt:` list entry (prefix `- `
	// after indentation) we've regressed.
	optListBug := regexp.MustCompile(`(?m)^\s*-\s*include_imports`)
	if optListBug.MatchString(content) {
		t.Errorf("frontends/web/buf.gen.yaml has include_imports under opt: as a list item; it must be a plugin-level field. Got:\n%s",
			content)
	}
}

// hasGitignoreRule reports whether pattern appears as a non-comment,
// non-blank rule in the .gitignore content. Commented-out patterns and
// patterns that are substrings of other patterns don't count.
func hasGitignoreRule(content, pattern string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == pattern {
			return true
		}
	}
	return false
}

// hasUpperCaseErrorString reports whether the generated handlers file
// contains an obviously ALL-CAPS error string in an errors.New /
// fmt.Errorf call. Matches strings of the form
//
//	fmt.Errorf("HANDLER FOR %s NOT YET IMPLEMENTED", ...)
//
// which was the exact shape of the past regression.
func hasUpperCaseErrorString(content string) bool {
	// Match a double-quoted string literal immediately following
	// fmt.Errorf( or errors.New( that is at least 10 chars long and
	// contains no lowercase letters. The 10-char floor keeps us from
	// flagging single-word identifiers.
	re := regexp.MustCompile(`(?:fmt\.Errorf|errors\.New)\("[^a-z"]{10,}"`)
	return re.Match([]byte(content))
}

// excerpt returns the line containing needle plus surrounding context
// (up to maxBytes). Used to make error messages pinpoint the offending
// region without dumping entire files.
func excerpt(content, needle string, maxBytes int) string {
	idx := strings.Index(content, needle)
	if idx < 0 {
		if len(content) > maxBytes {
			return content[:maxBytes] + "…(truncated)"
		}
		return content
	}
	start := idx - maxBytes/2
	if start < 0 {
		start = 0
	}
	end := idx + maxBytes/2
	if end > len(content) {
		end = len(content)
	}
	return content[start:end]
}

// Guard against build breakage caused by "unused" imports when we later
// whittle down this test file; keep the package list tight.
var (
	_ = bytes.NewBuffer
	_ = os.Getenv
	_ = exec.Command
)
