package cli

// Coverage-instrumented builds (kcl/schema.k Tests): which Go binaries an env
// builds with `-cover -covermode=atomic`, and the flags and GOCOVERDIR that
// carry it to every runtime forge compiles for.
//
// Like PGO, the unit is the BINARY: one binary has one answer, so workloads
// of an env that run the same binary must agree on it. Unlike PGO, the env's
// default is computed rather than declared — instrumented for local,
// ephemeral and preview envs of a project whose forge.yaml opts in
// (`tests.coverage`), never for a persistent env — and the opt-in lives in
// forge.yaml, which is why the default is resolved here and not in KCL.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/hostlaunch"
)

// TestsEntity is Bundle.tests as the render carries it: only what the env
// declared. The render omits it when the env declares nothing.
type TestsEntity struct {
	// Coverage is the env's declared answer; nil is the computed default
	// (CoverEnvDefault).
	Coverage *bool `json:"coverage,omitempty"`
	// CoveragePackages are explicit `-coverpkg` patterns; empty is "main",
	// the project's module minus its gen/ tree.
	CoveragePackages []string `json:"coverage_packages,omitempty"`
	// CoverageInPersistentEnv is why a persistent env is instrumented.
	CoverageInPersistentEnv string `json:"coverage_in_persistent_env,omitempty"`
}

// coverMode is the one mode forge builds: atomic counters are the only ones
// Go writes and clears while the process runs (serverkit's /debug/coverage),
// and hit counts merge across inputs only when every input counts.
const coverMode = "atomic"

// CoverBinary is one Go binary an env builds, with its resolved coverage.
type CoverBinary struct {
	// Package is the binary's main package, "./cmd/app" (normalizePGOPackage).
	Package string
	// Enabled builds it with -cover.
	Enabled bool
	// Workloads run (or name) it, sorted.
	Workloads []string
}

// Persistent reports whether e is a persistent env: no lifecycle and not a
// preview. forge treats such an env as production (kcl/schema.k Bundle).
func (e *KCLEntities) Persistent() bool {
	return e.Lifecycle == "" && e.Preview == nil
}

// CoverEnvDefault is the env's coverage answer for a binary that declares
// none: the env's declared `tests.coverage`, else on for a non-persistent env
// of a project that opts in (forge.yaml `tests.coverage`, see
// config.ReadTestsCoverage).
func (e *KCLEntities) CoverEnvDefault(optIn bool) bool {
	if e.Tests != nil && e.Tests.Coverage != nil {
		return *e.Tests.Coverage
	}
	return optIn && !e.Persistent()
}

// ResolveCoverBinaries returns every Go binary the env builds with its
// effective coverage, sorted by package. A GoBuild workload answers for
// GoBuild.cmd; a DockerBuild workload for each binary its `coverage` names
// and for the binary it runs (the PGO rule, dockerWorkloadBinary). A
// workload that does not name its binary takes the env's default.
//
// It refuses two workloads that disagree on one binary, and a binary
// instrumented in a persistent env that does not say why
// (`tests.coverage_in_persistent_env`), naming the workloads.
func ResolveCoverBinaries(e *KCLEntities, optIn bool) ([]CoverBinary, error) {
	if e == nil {
		return nil, nil
	}
	def := e.CoverEnvDefault(optIn)
	type claim struct {
		workload string
		on       bool
	}
	claims := map[string]claim{}
	runs := map[string][]string{}
	declare := func(pkg, workload string, declared *bool) error {
		on := def
		if declared != nil {
			on = *declared
		}
		prev, ok := claims[pkg]
		if !ok {
			claims[pkg] = claim{workload: workload, on: on}
			return nil
		}
		if prev.on != on {
			return fmt.Errorf("workloads %q and %q both run %s, one built with coverage and one without (coverage=%v vs %v): "+
				"one binary has one build, so every workload of an env that runs it declares the same `coverage` on its build "+
				"(or none, which follows the env's tests.coverage; kcl/schema.k Tests)",
				prev.workload, workload, pkg, prev.on, on)
		}
		return nil
	}
	for _, w := range e.Workloads {
		switch {
		case w.Build.Type == "go" && w.Build.Go != nil:
			pkg := normalizePGOPackage(w.Build.Go.Cmd)
			if pkg == "" {
				continue
			}
			if err := declare(pkg, w.Name, w.Build.Go.Coverage); err != nil {
				return nil, err
			}
			runs[pkg] = append(runs[pkg], w.Name)
		case w.Build.Type == "docker" && w.Build.Docker != nil:
			d := w.Build.Docker
			named := map[string]bool{}
			for _, k := range sortedKeys(d.Coverage) {
				on := d.Coverage[k]
				pkg := normalizePGOPackage(k)
				if err := declare(pkg, w.Name, &on); err != nil {
					return nil, err
				}
				named[pkg] = true
			}
			pkg := dockerWorkloadBinary(w, d, e.Project, named)
			if pkg == "" {
				continue
			}
			if !named[pkg] {
				if err := declare(pkg, w.Name, nil); err != nil {
					return nil, err
				}
			}
			runs[pkg] = append(runs[pkg], w.Name)
		}
	}
	out := make([]CoverBinary, 0, len(claims))
	for _, pkg := range sortedKeys(claims) {
		workloads := slices.Compact(slices.Sorted(slices.Values(runs[pkg])))
		if len(workloads) == 0 {
			workloads = []string{claims[pkg].workload}
		}
		out = append(out, CoverBinary{Package: pkg, Enabled: claims[pkg].on, Workloads: workloads})
	}
	if e.Persistent() && (e.Tests == nil || strings.TrimSpace(e.Tests.CoverageInPersistentEnv) == "") {
		for _, b := range out {
			if b.Enabled {
				return nil, fmt.Errorf("workloads %s build %s with coverage in env %q, a persistent env (no lifecycle, not a preview): "+
					"instrumented builds are for local, ephemeral and preview envs. If this env really runs e2e against cover builds, "+
					"say why: tests = forge.Tests {coverage_in_persistent_env = \"<why>\"} (kcl/schema.k Tests)",
					strings.Join(b.Workloads, ", "), b.Package, e.Env)
			}
		}
	}
	return out, nil
}

// applyCover records the resolved coverage on the entities: each GoBuild
// workload whose binary is instrumented is marked (WorkloadEntity.CoverBuild),
// which is what the host build and the host launch read.
func (e *KCLEntities) applyCover(bins []CoverBinary) {
	on := map[string]bool{}
	for _, b := range bins {
		if b.Enabled {
			on[b.Package] = true
		}
	}
	var pkgs []string
	if e.Tests != nil {
		pkgs = e.Tests.CoveragePackages
	}
	for i := range e.Workloads {
		if g := e.Workloads[i].GoBuild(); g != nil && on[normalizePGOPackage(g.Cmd)] {
			e.Workloads[i].cover = &workloadCover{packages: pkgs}
		}
	}
}

// workloadCover is a workload's instrumented build: the env's -coverpkg
// patterns, empty for "main".
type workloadCover struct{ packages []string }

// CoverBuild reports whether the env builds w's Go binary with -cover. Only
// an env that went through the render's resolution (RenderKCLWith) says yes.
func (w WorkloadEntity) CoverBuild() bool { return w.cover != nil }

// coverPackages are the -coverpkg patterns of w's cover build; empty is
// "main".
func (w WorkloadEntity) coverPackages() []string {
	if w.cover == nil {
		return nil
	}
	return w.cover.packages
}

// coverDir is where a host workload's instrumented binary writes its
// coverage (GOCOVERDIR): .forge/tests/covdata/<env>/<workload>/ under the
// project. Go writes counters there when the process exits and refuses a
// directory that does not exist, so the caller creates it.
func coverDir(projectDir, env, workload string) string {
	return filepath.Join(projectDir, ".forge", "tests", "covdata", env, workload)
}

// coverBuildFlags are the `go build` (and `go run`) flags of a cover build:
// -cover -covermode=atomic -coverpkg=<patterns>. Empty patterns are "main":
// the project's module minus its gen/ tree (mainModuleCoverPatterns).
func coverBuildFlags(ctx context.Context, projectDir string, patterns []string) ([]string, error) {
	pkgs := patterns
	if len(pkgs) == 0 {
		var err error
		if pkgs, err = mainModuleCoverPatterns(ctx, projectDir); err != nil {
			return nil, err
		}
	}
	flags := []string{"-cover", "-covermode=" + coverMode}
	if len(pkgs) > 0 {
		flags = append(flags, "-coverpkg="+strings.Join(pkgs, ","))
	}
	return flags, nil
}

var mainCoverPatterns sync.Map // projectDir → []string

// mainModuleCoverPatterns is "main" as -coverpkg patterns: one
// `<module>/<dir>/...` per top-level directory of the module that holds a Go
// package, plus `<module>` when the root does, leaving out gen/. Patterns
// rather than every package keep the flag short on any command line (a
// Windows one is 32K). Memoized per project for the life of the process.
func mainModuleCoverPatterns(ctx context.Context, projectDir string) ([]string, error) {
	if v, ok := mainCoverPatterns.Load(projectDir); ok {
		return v.([]string), nil
	}
	gomod, err := os.ReadFile(filepath.Join(projectDir, "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("coverage: the cover build instruments the project's module, and %s has no go.mod: %w", projectDir, err)
	}
	mod := modfile.ModulePath(gomod)
	if mod == "" {
		return nil, fmt.Errorf("coverage: %s/go.mod declares no module", projectDir)
	}
	cmd := exec.CommandContext(ctx, "go", "list", "-find", "-e", "-f", "{{.ImportPath}}", "./...")
	cmd.Dir = projectDir
	cmd.Env = goexec.Env()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("coverage: list the module's packages (go list ./...): %w", err)
	}
	patterns := mainCoverPatternsOf(mod, strings.Fields(string(out)))
	mainCoverPatterns.Store(projectDir, patterns)
	return patterns, nil
}

// mainCoverPatternsOf folds a module's package import paths into one
// pattern per top-level directory, leaving out gen/.
func mainCoverPatternsOf(mod string, importPaths []string) []string {
	seen := map[string]bool{}
	for _, ip := range importPaths {
		rel, ok := strings.CutPrefix(ip, mod)
		if !ok || (rel != "" && !strings.HasPrefix(rel, "/")) {
			continue // another module's package
		}
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			seen[mod] = true
			continue
		}
		top, _, _ := strings.Cut(rel, "/")
		if top == "gen" {
			continue
		}
		seen[mod+"/"+top+"/..."] = true
	}
	return slices.Sorted(func(yield func(string) bool) {
		for p := range seen {
			if !yield(p) {
				return
			}
		}
	})
}

// withHostCover makes spec launch w's instrumented binary when the env
// builds it with -cover: the cover flags for the runners that compile on
// launch (go-run, air), and GOCOVERDIR=.forge/tests/covdata/<env>/<w>/,
// created here because Go refuses a directory that does not exist. An air
// config that does not expand $FORGE_GO_BUILD_FLAGS builds uninstrumented,
// and is named so the missing coverage has a reason.
func withHostCover(ctx context.Context, spec *hostlaunch.RunnerSpec, w WorkloadEntity, env string) error {
	if !w.CoverBuild() || (len(spec.Command) > 0 && strings.TrimSpace(spec.Runner) != "air") {
		return nil
	}
	projectDir := projectDirForKCL()
	if r := strings.TrimSpace(spec.Runner); r == "" || r == "go-run" || r == "air" {
		var userFlags []string
		var userEnv map[string]string
		if g := w.GoBuild(); g != nil {
			userFlags, userEnv = g.Flags, g.Env
		}
		if owns, _, _ := userCoverFlags(userFlags, userEnv); !owns {
			flags, err := coverBuildFlags(ctx, projectDir, w.coverPackages())
			if err != nil {
				return fmt.Errorf("host workload %s: %w", w.Name, err)
			}
			spec.GoBuildFlags = mergeCoverFlags(flags, userFlags, userEnv)
		}
		if r == "air" && !spec.AirExpandsBuildFlags() {
			fmt.Printf("[up] host %s: not instrumented for coverage — its air config does not build with $%s (add it to its `go build`; a new scaffold's .air.toml does)\n",
				w.Name, hostlaunch.GoBuildFlagsEnv)
		}
	}
	dir := coverDir(projectDir, env, w.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("host workload %s: coverage dir: %w", w.Name, err)
	}
	spec.CoverDir = dir
	return nil
}

// userCoverFlags reads the cover flags the build already carries in
// GoBuild.flags or GOFLAGS of GoBuild.env. owns is an explicit -cover (or
// -cover=false): the user decides instrumentation and forge adds nothing.
// Otherwise forge adds -cover, and its own -covermode / -coverpkg only where
// the user set none (setsMode / setsPkg), so a value of theirs is kept.
func userCoverFlags(flags []string, env map[string]string) (owns, setsMode, setsPkg bool) {
	for _, f := range slices.Concat(flags, strings.Fields(env["GOFLAGS"])) {
		if !strings.HasPrefix(f, "-") {
			continue
		}
		switch name, _, _ := strings.Cut(strings.TrimLeft(f, "-"), "="); name {
		case "cover":
			owns = true
		case "covermode":
			setsMode = true
		case "coverpkg":
			setsPkg = true
		}
	}
	return owns, setsMode, setsPkg
}

// mergeCoverFlags is forge's cover flags for a build that carries flags and
// env of its own (userCoverFlags): nil when the user owns -cover, else forge's
// flags minus the -covermode / -coverpkg the user already set.
func mergeCoverFlags(forge, flags []string, env map[string]string) []string {
	owns, setsMode, setsPkg := userCoverFlags(flags, env)
	if owns {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(forge), func(f string) bool {
		return (setsMode && strings.HasPrefix(f, "-covermode=")) || (setsPkg && strings.HasPrefix(f, "-coverpkg="))
	})
}
