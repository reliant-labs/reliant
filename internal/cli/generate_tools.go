package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/assets"
	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/goexec"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// devWorkspaceBridgesExternalModule reports whether the project's go.work
// bridges a module to a LOCAL directory OUTSIDE the project tree — the
// signature of a cross-repo dev bridge, which `forge project new --link-forge`
// writes (`use <local-forge>`, see writeDevForgeGoWork).
//
// It matters for the go-mod-tidy steps: when the workspace deliberately
// overrides a published `require` with an UNPUBLISHED local checkout, a plain
// `go mod tidy` cannot succeed — tidy ignores go.work and resolves from the
// module proxy, so it 404s on the not-yet-published API and (fatally) aborts
// codegen. `go build`/`go vet` DO honor the workspace, so once tidy is
// softened the pipeline's final `go build (validate)` still guards
// correctness. This is a strict no-op for ordinary projects: the scaffold's
// starter go.work only `use`s `.` and `gen` (both inside the project), and
// released/CI builds never write an external `use` at all.
func devWorkspaceBridgesExternalModule(projectDir string) bool {
	data, err := os.ReadFile(filepath.Join(projectDir, "go.work"))
	if err != nil {
		return false
	}
	wf, err := modfile.ParseWork("go.work", data, nil)
	if err != nil {
		return false
	}
	isExternal := func(p string) bool {
		if p == "" {
			return false
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(projectDir, p)
		}
		rel, err := filepath.Rel(projectDir, filepath.Clean(abs))
		if err != nil {
			return true // unrelated volume/root → outside the project
		}
		return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	for _, u := range wf.Use {
		if isExternal(u.Path) {
			return true
		}
	}
	for _, r := range wf.Replace {
		// Only filesystem replaces (empty New.Version) point at a local dir.
		if r.New.Version == "" && isExternal(r.New.Path) {
			return true
		}
	}
	return false
}

// tidyBridgedModule is `go mod tidy` for ONE module of a project whose go.work
// bridges forge (or another module) to a local checkout.
//
// It used to run `go work sync` instead, and that was wrong in a way that
// looked like forge helpfully tidying: sync writes the WORKSPACE's build list
// back into every member go.mod, and the workspace includes the bridged
// checkout — so forge's own requirements (gomega, protovalidate, go-sqlite3,
// …) were copied into the project's go.mod as "upgrades" on every generate. A
// project that pinned a forge version had its dependency graph silently moved
// by a tool that was only supposed to regenerate code.
//
// The bridge decides what COMPILES; it must never decide what the project
// DECLARES. So tidy runs with GOWORK=off — against the module's own
// requirements, exactly the graph `forge env up`'s `go mod tidy -diff`
// preflight checks — and only when that graph is actually stale:
//
//   - clean: nothing is written (the common case — zero churn);
//   - stale and tidy succeeds: go.mod/go.sum converge the way a plain
//     `go mod tidy` would;
//   - tidy cannot resolve (the project calls forge API that is not published
//     yet, which is what the bridge is for): warn and leave go.mod alone. The
//     validate build honors go.work, so it remains the correctness gate.
func tidyBridgedModule(moduleDir, label string) error {
	return tidyBridgedModuleWith(runGoOffWorkspace, moduleDir, label)
}

// goOffWorkspaceRunner runs `go <args...>` in dir with GOWORK=off. A seam so
// the tidy decision is testable without a module proxy.
type goOffWorkspaceRunner func(dir string, args ...string) ([]byte, error)

func runGoOffWorkspace(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = goexec.Env("GOWORK=off")
	return cmd.CombinedOutput()
}

func tidyBridgedModuleWith(run goOffWorkspaceRunner, moduleDir, label string) error {
	if _, err := run(moduleDir, "mod", "tidy", "-diff"); err == nil {
		return nil // already tidy against its own requirements — write nothing
	}
	fmt.Printf("🔗 %s: go.work bridges a local module — tidying against this module's own requirements (GOWORK=off), so the bridged checkout's deps never leak into go.mod.\n", label)
	out, err := run(moduleDir, "mod", "tidy")
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠️  %s: `GOWORK=off go mod tidy` could not resolve (continuing — go.mod left unchanged; the go.work-aware `go build (validate)` still gates correctness). Usually the code calls forge API that is not published yet; it tidies once that forge is released and pinned.\n%s", label, strings.TrimSpace(string(out)))
		fmt.Fprintln(os.Stderr)
	}
	return nil
}

// ensureGenGoMod bootstraps `gen/go.mod` when it's missing.
//
// `forge project new` renders gen-go.mod.tmpl as part of the initial scaffold, but a
// git worktree carved from a checkout that has never run `forge generate`
// won't have `gen/go.mod` on disk — the file is typically gitignored or has
// simply not been committed. Every subsequent `go build` / `go test` /
// `go list` then fails with:
//
//	go: cannot load module gen listed in go.work file:
//	open gen/go.mod: no such file or directory
//
// because the project's go.work declares `use gen`. The pipeline runs `buf
// generate` and `go list ./...` before the post-codegen `go mod tidy (gen/)`
// step, so we have to render the file before any of those steps fire.
//
// Best-effort: anything that prevents us from synthesizing the file (no
// go.mod in the project root to derive the module path from, template
// render error) returns nil with a warning — the downstream step that
// actually needed the module will surface a clearer error.
func ensureGenGoMod(projectDir string) error {
	genDir := filepath.Join(projectDir, "gen")
	goMod := filepath.Join(genDir, "go.mod")
	if _, err := os.Stat(goMod); err == nil {
		return nil // already present
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat gen/go.mod: %w", err)
	}

	// Only bootstrap when go.work declares `use gen` — if there's no
	// gen workspace member, the missing file is by design: a project whose
	// root module owns gen/ (an in-place adoption; see
	// generator.ProjectGenerator.GenInRootModule) has none, and writing one
	// would split gen/'s packages out of the module that imports them.
	if !goWorkUsesGen(projectDir) {
		return nil
	}

	modulePath, err := codegen.GetModulePath(projectDir)
	if err != nil {
		// Intentional soft warning: this is a best-effort bootstrap for
		// fresh-checkout worktrees. If we can't read the module path the
		// project is unusable anyway — the downstream `go list ./...`
		// step (which the pipeline runs before any codegen) will surface
		// the canonical "module path missing" error with full context.
		// Promoting here would only produce a noisier, less actionable
		// failure for the same underlying cause.
		fmt.Fprintf(os.Stderr, "Warning: bootstrap gen/go.mod skipped (cannot read project module path): %v\n", err)
		return nil
	}

	goVersion := goVersionFromProject(projectDir)
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		return fmt.Errorf("create gen/: %w", err)
	}
	data := struct {
		Module    string
		GoVersion string
		// ForgeVersion mirrors the root module's forge pin so both of the
		// project's modules resolve the SAME published forge from the proxy.
		// Empty when the root has no forge require (the template then omits
		// the line and `go mod tidy` resolves it if the generated code
		// imports it).
		ForgeVersion string
	}{
		Module:       modulePath,
		GoVersion:    goVersion,
		ForgeVersion: rootForgeVersion(projectDir),
	}
	if err := assets.WriteTemplateWithData("gen-go.mod.tmpl", goMod, data); err != nil {
		return fmt.Errorf("render gen/go.mod: %w", err)
	}
	fmt.Println("🔧 Bootstrapped missing gen/go.mod (fresh worktree).")
	return nil
}

// goWorkUsesGen reports whether the project's go.work has a `use` directive
// naming the project's gen/ directory. It parses the file: matching the bytes
// "gen" anywhere in it (what this used to do) also fired on a `use` path that
// merely contains the letters, such as a bridged checkout at ../forge-gen-fix,
// and bootstrapped a gen/go.mod the project never had.
func goWorkUsesGen(projectDir string) bool {
	path := filepath.Join(projectDir, "go.work")
	data, err := os.ReadFile(path)
	if err != nil {
		return false // no go.work → no workspace → no gen member
	}
	wf, err := modfile.ParseWork(path, data, nil)
	if err != nil {
		return false // malformed go.work — `go` itself reports it
	}
	genDir := filepath.Join(projectDir, "gen")
	for _, u := range wf.Use {
		p := u.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(projectDir, p)
		}
		if filepath.Clean(p) == genDir {
			return true
		}
	}
	return false
}

// retiredForgePkgModule is the pre-collapse companion module. Importing
// forge/pkg/* with no forge requirement makes `go mod tidy` (which ignores
// go.work) pick it as the longest module path that provides the import.
const retiredForgePkgModule = "github.com/reliant-labs/forge/pkg"

// ensureGenForgeRequire makes gen/go.mod require the single forge module and
// drops the retired forge/pkg module, reporting whether it rewrote the file.
//
// gen/ is forge's own module: every *.pb.go in it blank-imports
// forge/pkg/forgepb, so it must always carry the require. The scaffold's
// gen-go.mod.tmpl writes one, but the scaffold's own `go mod tidy` runs while
// gen/ still holds no Go files and removes it as unused. The tidy after
// codegen then finds forge/pkg/forgepb with no forge requirement and resolves
// it to the retired module instead — which generate refuses. Re-asserting the
// require before codegen keeps tidy from ever choosing it, and migrates a
// project whose gen/go.mod already holds the retired require.
//
// The version mirrors the root go.mod's pin (this binary's own when the root
// has none), so both of the project's modules resolve the same forge.
func ensureGenForgeRequire(projectDir string) (bool, error) {
	path := filepath.Join(projectDir, "gen", "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, nil // no gen module: nothing to maintain
	}
	f, err := modfile.Parse(path, data, nil)
	if err != nil {
		return false, nil // not ours to repair; tidy will say what is wrong
	}
	hasForge, hasRetired := false, false
	for _, r := range f.Require {
		switch r.Mod.Path {
		case forgeModulePath:
			hasForge = true
		case retiredForgePkgModule:
			hasRetired = true
		}
	}
	if hasForge && !hasRetired {
		return false, nil
	}
	if hasRetired {
		if err := f.DropRequire(retiredForgePkgModule); err != nil {
			return false, err
		}
	}
	if !hasForge {
		version := rootForgeVersion(projectDir)
		if version == "" {
			version = buildinfo.InstallableVersion()
		}
		if version == "" {
			version = buildinfo.PublishedFloor()
		}
		if version == "" {
			return false, nil
		}
		if err := f.AddRequire(forgeModulePath, version); err != nil {
			return false, err
		}
	}
	f.Cleanup()
	out, err := f.Format()
	if err != nil {
		return false, err
	}
	if err := atomicfile.WriteFile(path, out, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// forgeRequireRE matches the forge `require` line in a go.mod — either the
// block form (`\tgithub.com/reliant-labs/forge vX.Y.Z`) or the single-line
// form (`require github.com/... vX.Y.Z`). It does NOT match a `replace` line
// (the module path is not the first token there), nor a `forge/pkg` line from
// a project still on the pre-collapse two-module pin: the trailing `[\t ]+`
// requires whitespace straight after the module path, which `/pkg` breaks.
// Such a project is stale by definition and handled by the compatibility
// check in generate_pkg_compat.go, not silently mirrored into gen/.
var forgeRequireRE = regexp.MustCompile(
	`(?m)^[\t ]*(?:require[\t ]+)?github\.com/reliant-labs/forge[\t ]+(v[^\s]+)[\t ]*$`)

// rootForgeVersion returns the forge version pinned in the project's root
// go.mod, or "" when the root has no forge require (or no go.mod). Used to
// mirror the root pin into a freshly bootstrapped gen/go.mod so both of the
// project's modules resolve forge to the same published version.
func rootForgeVersion(projectDir string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, "go.mod"))
	if err != nil {
		return ""
	}
	m := forgeRequireRE.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// goVersionFromProject reads the `go <version>` directive out of the
// project's root go.mod and returns it, falling back to a conservative
// default that matches the rest of the scaffolder. Kept local so the cli
// package doesn't have to import the generator's GoVersion helpers (which
// are oriented around the ProjectGenerator struct).
func goVersionFromProject(projectDir string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, "go.mod"))
	if err != nil {
		return "1.27.0"
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "go ") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "go "))
		if v != "" {
			return v
		}
	}
	return "1.27.0"
}

// runGoModTidyGen runs `go mod tidy` inside the gen/ directory to keep deps fresh.
func runGoModTidyGen(projectDir string) error {
	genDir := filepath.Join(projectDir, "gen")
	goMod := filepath.Join(genDir, "go.mod")
	if _, err := os.Stat(goMod); os.IsNotExist(err) {
		// No go.mod in gen/, nothing to tidy
		return nil
	}

	// A dev-forge go.work bridge deliberately overrides a published require
	// with an unpublished local checkout — see tidyBridgedModule.
	if devWorkspaceBridgesExternalModule(projectDir) {
		return tidyBridgedModule(genDir, "gen/ tidy")
	}

	fmt.Println("🔨 Running go mod tidy in gen/...")
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = genDir
	cmd.Env = goexec.Env()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go mod tidy in gen/ failed: %w", err)
	}

	fmt.Println("  ✅ gen/go.mod tidied")
	return nil
}

func runGoModTidyRoot(projectDir string) error {
	goMod := filepath.Join(projectDir, "go.mod")
	if _, err := os.Stat(goMod); os.IsNotExist(err) {
		return nil
	}

	// See runGoModTidyGen / tidyBridgedModule: under a dev-forge go.work
	// bridge, tidy against the module's own requirements, never the
	// workspace's.
	if devWorkspaceBridgesExternalModule(projectDir) {
		return tidyBridgedModule(projectDir, "root tidy")
	}

	fmt.Println("🔨 Running go mod tidy in project root...")
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = projectDir
	cmd.Env = goexec.Env()
	cmd.Stdout = os.Stdout
	// Streamed as before, and kept, so a failure can name its cause.
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)

	if err := cmd.Run(); err != nil {
		modulePath, _ := codegen.GetModulePath(projectDir)
		if cause := tidyFailureCause(stderr.String(), modulePath); cause != "" {
			return &tidyImportError{cause: cause, err: err}
		}
		return fmt.Errorf("%w (fix go.mod or go.sum; go's output is above)", err)
	}

	fmt.Println("  ✅ go.mod tidied")
	return nil
}

// runGoimportsOnGenerated runs goimports over the Go files forge WROTE this
// run, fixing import grouping in its own output.
//
// Scoped to the written set, NOT to the cmd/pkg/gen/internal trees. Passing
// the directories made `forge generate` rewrite every hand-owned Go file in
// the project — forge editing what it does not own, which is the one thing the
// tier model exists to prevent. It is not merely cosmetic either: goimports
// RESOLVES unknown identifiers by guessing a package, so a file forge has no
// business touching can silently acquire an import nobody asked for. That is
// not hypothetical — it happened to a linter fixture in this repo, which
// gained an `html/template` import purely because `forge generate` ran.
//
// checksums.WrittenThisRun is the right scope because every forge write goes
// through the checksums chokepoint, including the in-place reconcilers that
// splice into compose.go / lifecycle.go.
func runGoimportsOnGenerated(projectDir, modulePath string) error {
	pinned, err := ensureCodegenTools(context.Background(), projectDir)
	if err != nil {
		return err
	}
	goimportsPath, ok := pinned.Path("goimports")
	if !ok {
		goimportsPath, err = exec.LookPath("goimports")
	}
	if err != nil {
		fmt.Println("  ⚠️  goimports not found — skipping import formatting")
		fmt.Println("     Install with: go install golang.org/x/tools/cmd/goimports@latest")
		return nil
	}

	targets := writtenGoFiles(projectDir)
	if len(targets) == 0 {
		return nil
	}

	fmt.Println("🔨 Running goimports on generated code...")
	// Batch the argv: a large project can write hundreds of files in one run
	// and the whole set at once risks E2BIG.
	const batchSize = 200
	for start := 0; start < len(targets); start += batchSize {
		end := start + batchSize
		if end > len(targets) {
			end = len(targets)
		}
		args := append([]string{"-local", modulePath, "-w"}, targets[start:end]...)
		cmd := exec.Command(goimportsPath, args...)
		cmd.Dir = projectDir
		// goimports resolves imports by running `go list` itself, so it
		// gets the same scrubbed GOFLAGS as forge's own go calls.
		cmd.Env = goexec.Env()
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("goimports failed: %w", err)
		}
	}

	fmt.Println("  ✅ Imports formatted")
	return nil
}

// writtenGoFiles returns the project-relative .go paths forge wrote this run,
// sorted so the goimports invocation is deterministic. Paths that no longer
// exist are dropped: a file can be written and then removed by the cleanup
// sweep within the same run.
func writtenGoFiles(projectDir string) []string {
	var out []string
	for relPath := range checksums.WrittenPaths() {
		if filepath.Ext(relPath) != ".go" {
			continue
		}
		if _, err := os.Stat(filepath.Join(projectDir, relPath)); err != nil {
			continue
		}
		out = append(out, relPath)
	}
	sort.Strings(out)
	return out
}
