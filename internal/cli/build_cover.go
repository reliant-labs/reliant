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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/pkg/release"

	"github.com/reliant-labs/forge/internal/buildlog"
	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/hostlaunch"
	"github.com/reliant-labs/forge/internal/kclrender"
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
// which is what the host build and the host launch read. An image whose
// binary is NOT instrumented here, but WOULD be in a preview of this env
// (previewCoverBinaries: the same per-binary resolution, so a binary any
// workload or the env turned off is never one), is marked for a cover
// VARIANT: a release build of it pushes the cover stage beside the shared
// image, for a preview of the release to run (buildReleaseCoverVariant).
func (e *KCLEntities) applyCover(bins []CoverBinary, optIn bool) {
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
	inPreview := previewCoverBinaries(e, optIn)
	for i := range e.Workloads {
		w := &e.Workloads[i]
		if g := w.GoBuild(); g != nil && on[normalizePGOPackage(g.Cmd)] {
			w.cover = &workloadCover{packages: pkgs}
		}
		// An image is built instrumented when the binary its workload runs
		// is: the same binary ResolveCoverBinaries resolved for it.
		if d := w.Build.Docker; w.Build.Type == "docker" && d != nil {
			named := map[string]bool{}
			for k := range d.Coverage {
				named[normalizePGOPackage(k)] = true
			}
			pkg := dockerWorkloadBinary(*w, d, e.Project, named)
			switch {
			case pkg != "" && on[pkg]:
				d.cover = &workloadCover{packages: pkgs}
				w.cover = d.cover
			case pkg != "" && inPreview[pkg]:
				d.coverVariant = &workloadCover{packages: pkgs}
			}
		}
	}
}

// previewCoverBinaries is the set of binaries a preview of e instruments:
// ResolveCoverBinaries over e as a preview (not persistent), so the variant
// follows exactly the per-binary policy a preview's own build would — the
// env's declared tests.coverage, else the project's opt-in, and a binary's
// declared `coverage` on any workload that runs or names it. Binaries whose
// workloads disagree resolve to none (a preview of e would refuse to render).
func previewCoverBinaries(e *KCLEntities, optIn bool) map[string]bool {
	if e == nil {
		return nil
	}
	preview := *e
	if preview.Preview == nil {
		preview.Preview = &release.ShapePreview{Of: e.Env}
	}
	bins, err := ResolveCoverBinaries(&preview, optIn)
	if err != nil {
		return nil
	}
	on := map[string]bool{}
	for _, b := range bins {
		if b.Enabled {
			on[b.Package] = true
		}
	}
	return on
}

// buildReleaseCoverVariant builds and pushes the cover variant of a release
// image (applyCover marked it) BESIDE the shared one: the same `docker build`
// with `--target cover`, every tag suffixed `-cover`. It returns the pushed
// digest, which the cut records as the image's cover variant artifact.
//
// It never fails the release: coverage decides no exit code (design §5.3).
// A Dockerfile without the cover stage, a build that names its own target, a
// release that pushes nowhere, or a failed cover build is a line on stdout
// and no variant, and a preview then runs the shared image.
//
// The stage is looked for in the Dockerfile the build uses (coverDockerfile),
// a GitSource pin's included.
func buildReleaseCoverVariant(ctx context.Context, svcName string, d *DockerBuild, dockerArgs, pushTags []string, alreadyCover bool) (string, error) {
	if alreadyCover || d == nil || d.coverVariant == nil || len(pushTags) == 0 {
		return "", nil
	}
	dockerfile, derr := coverDockerfile(d)
	if derr != nil {
		fmt.Printf("[build] %s: no cover variant for this release — %v\n", svcName, derr)
		return "", nil
	}
	if d.Target != "" || !dockerfileHasCoverStage(dockerfile) {
		fmt.Printf("[build] %s: no cover variant for this release — %s\n", svcName, coverSkipReason(dockerfile, d))
		return "", nil
	}
	args, err := coverVariantArgs(ctx, svcName, d, dockerArgs)
	if errors.Is(err, errCoverVariantUntagged) {
		fmt.Printf("[build] %s: no cover variant for this release — %v\n", svcName, err)
		return "", nil
	}
	if err != nil {
		return "", err
	}
	fmt.Printf("[build] %s: building the release's cover variant (docker build --target %s)\n", svcName, coverImageStage)
	cmd := exec.CommandContext(ctx, "docker", args...)
	var endStep func(error)
	cmd.Stdout, cmd.Stderr, endStep = buildlog.Step("docker build " + svcName + " (cover)")
	err = cmd.Run()
	endStep(err)
	if err != nil {
		fmt.Printf("[build]   Warning: %s: the cover variant did not build (%v); previews of this release run the shared image\n", svcName, err)
		return "", nil
	}
	var last string
	for _, t := range pushTags {
		last = coverTag(t)
		if err := dockerPush(ctx, last); err != nil {
			fmt.Printf("[build]   Warning: %s: push %s: %v; previews of this release run the shared image\n", svcName, last, err)
			return "", nil
		}
	}
	digest, _, err := imageRepoDigest(ctx, last)
	if err != nil {
		fmt.Printf("[build]   Warning: %s: could not capture the cover variant's digest (%v); previews of this release run the shared image\n", svcName, err)
		return "", nil
	}
	fmt.Printf("[build] %s: cover variant %s\n", svcName, digest)
	return digest, nil
}

// coverSkipReason says why an image gets no cover build.
func coverSkipReason(dockerfile string, d *DockerBuild) string {
	if d.Target != "" {
		return fmt.Sprintf("the build names its own target %q", d.Target)
	}
	return fmt.Sprintf("%s has no %#q stage (a new scaffold's Dockerfile does)", dockerfile, coverImageStage)
}

// errCoverVariantUntagged is coverVariantArgs refusing an argv that tags
// nothing: the variant would carry the shared image's tags.
var errCoverVariantUntagged = errors.New("its docker build names no tag to suffix -" + release.CoverVariant + ", so the variant would push over the shared image")

// coverVariantArgs is the shared image's `docker build` argv turned into its
// cover variant's: `--target cover` and FORGE_COVERPKG (coverImageArgs's
// rule), and every tag suffixed -cover (coverTag), in each spelling docker
// accepts: `-t ref`, `--tag ref`, `-t=ref`, `--tag=ref` and `-tref`. An argv
// that tags nothing is refused: the variant would carry the shared image's
// tags, and pushing them would overwrite it.
func coverVariantArgs(ctx context.Context, svcName string, d *DockerBuild, dockerArgs []string) ([]string, error) {
	variant := *d
	variant.cover, variant.coverVariant = d.coverVariant, nil
	extra, err := coverImageArgs(ctx, svcName, &variant)
	if err != nil {
		return nil, err
	}
	out := append([]string{dockerArgs[0]}, extra...)
	tagged := false
	for i := 1; i < len(dockerArgs); i++ {
		a := dockerArgs[i]
		switch {
		case (a == "-t" || a == "--tag") && i+1 < len(dockerArgs):
			i++
			out = append(out, a, coverTag(dockerArgs[i]))
			tagged = true
		case strings.HasPrefix(a, "--tag="), strings.HasPrefix(a, "-t="):
			flag, ref, _ := strings.Cut(a, "=")
			out = append(out, flag+"="+coverTag(ref))
			tagged = true
		case strings.HasPrefix(a, "-t") && len(a) > 2 && !strings.HasPrefix(a, "--"):
			out = append(out, "-t"+coverTag(a[2:]))
			tagged = true
		default:
			out = append(out, a)
		}
	}
	if !tagged {
		return nil, errCoverVariantUntagged
	}
	return out, nil
}

// envRunsVariant is the build variant env runs from a release: a preview
// instance (forge env preview create marks its directory) runs the cover
// variant, every other env none — a persistent env runs the shared digests,
// the bytes staging ran.
func envRunsVariant(projectDir, env string) string {
	if _, ok, _ := kclrender.ReadPreviewMarker(filepath.Join(projectDir, "deploy", "kcl", env)); ok {
		return release.CoverVariant
	}
	return ""
}

// releasePinsForEnv is the image pin set env resolves from rel
// (release.PinsFor), checked so that no variant digest lands on an env that
// does not run it (release.CheckPins). A release that merely CARRIES a cover
// variant promotes to a persistent env on its shared digests; only a cover
// digest in a persistent env's pins is refused.
func releasePinsForEnv(projectDir, env string, rel release.Release) (map[string]string, error) {
	runs := envRunsVariant(projectDir, env)
	pins := rel.PinsFor(runs)
	if err := rel.CheckPins(runs, pins); err != nil {
		return nil, fmt.Errorf("env %s: %w", env, err)
	}
	return pins, nil
}

// promoteTargetPins is the pin set a promotion of env to target is written
// with: resolveReleaseDigests's refusal of a release that pins nothing, then
// the env's pins (releasePinsForEnv) — a preview runs the release's cover
// variant where it has one, every other env its shared digests, and a pin set
// that would put a cover digest on a persistent env is refused before the
// promotion is written.
func promoteTargetPins(projectDir, env string, target release.Release) (map[string]string, error) {
	if _, err := resolveReleaseDigests(target); err != nil {
		return nil, err
	}
	return releasePinsForEnv(projectDir, env, target)
}

// coverTag is ref's cover variant tag: `repo:v1.4.0` → `repo:v1.4.0-cover`;
// an untagged ref gets the tag `cover`.
func coverTag(ref string) string {
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref + "-" + release.CoverVariant
	}
	return ref + ":" + release.CoverVariant
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

// coverImageStage is the Dockerfile stage a cover image is built from: the
// scaffolded Dockerfile's `cover` stage (Dockerfile.tmpl), the production
// image with every binary built -cover -covermode=atomic.
const coverImageStage = "cover"

// coverStageRE finds a `FROM <base> AS cover` line (Dockerfile keywords are
// case-insensitive).
var coverStageRE = regexp.MustCompile(`(?im)^\s*FROM\s+\S+(\s+--\S+)*\s+AS\s+` + coverImageStage + `\s*$`)

// dockerfileHasCoverStage reports whether the Dockerfile at path declares the
// cover stage. A Dockerfile scaffolded before it existed does not, and its
// image is built as before.
func dockerfileHasCoverStage(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && coverStageRE.Match(data)
}

// coverDockerfile is the Dockerfile d builds with, as a path the stage
// detection can read: the file a GitSource pin resolved to (DockerfilePath,
// set by resolveDockerBuildSources) — which under a GitSource context must
// lie inside that resolved checkout — else the declared path, relative to
// the project root. It never falls back to a local Dockerfile for a pinned
// build: reading the deploy repo's own file would answer for the wrong image.
func coverDockerfile(d *DockerBuild) (string, error) {
	if d.DockerfilePath != "" {
		if d.ContextDir != "" && d.DockerfileSource == nil {
			rel, err := filepath.Rel(d.ContextDir, d.DockerfilePath)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				return "", fmt.Errorf("the Dockerfile %s is outside its resolved GitSource context %s", d.DockerfilePath, d.ContextDir)
			}
		}
		return d.DockerfilePath, nil
	}
	if d.HasGitSource() {
		return "", fmt.Errorf("its GitSource Dockerfile was not resolved")
	}
	p := serviceDockerfile(d)
	if !filepath.IsAbs(p) {
		p = filepath.Join(projectDirForKCL(), p)
	}
	return p, nil
}

// coverImageArgs are the extra `docker build` args of a DockerBuild workload
// the env instruments: `--target cover`, and FORGE_COVERPKG (the packages the
// stage instruments) when the image builds this project's module. nil, with a
// line saying why, when the image cannot be a cover build: the Dockerfile has
// no cover stage, or the build names a target of its own, which wins. The
// Dockerfile read is the one the build actually uses (coverDockerfile),
// including a GitSource pin's.
func coverImageArgs(ctx context.Context, svcName string, d *DockerBuild) ([]string, error) {
	if d == nil || d.cover == nil {
		return nil, nil
	}
	dockerfile, err := coverDockerfile(d)
	if err != nil {
		return nil, fmt.Errorf("service %q: coverage: %w", svcName, err)
	}
	if d.Target != "" {
		fmt.Printf("[build] %s: not instrumented for coverage — the build names its own target %q (the cover image is the `%s` stage)\n", svcName, d.Target, coverImageStage)
		return nil, nil
	}
	if !dockerfileHasCoverStage(dockerfile) {
		fmt.Printf("[build] %s: not instrumented for coverage — %s has no `%s` stage (a new scaffold's Dockerfile does)\n", svcName, dockerfile, coverImageStage)
		return nil, nil
	}
	args := []string{"--target", coverImageStage}
	// FORGE_COVERPKG names this project's packages, so it is passed only when
	// the image builds this project from its root; another context builds
	// another module, and Go's default (its main module) is the right one.
	if _, own := d.BuildArgs["FORGE_COVERPKG"]; !own && !d.HasGitSource() && serviceDockerContext(d) == "." {
		pkgs, err := coverPackages(ctx, projectDirForKCL(), d.cover.packages)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", svcName, err)
		}
		args = append(args, "--build-arg", "FORGE_COVERPKG="+strings.Join(pkgs, ","))
	}
	fmt.Printf("[build] %s: instrumented for coverage (docker build --target %s)\n", svcName, coverImageStage)
	return args, nil
}

// coverBuildFlags are the `go build` (and `go run`) flags of a cover build:
// -cover -covermode=atomic -coverpkg=<patterns>. Empty patterns are "main":
// the project's module minus its gen/ tree (mainModuleCoverPatterns).
func coverBuildFlags(ctx context.Context, projectDir string, patterns []string) ([]string, error) {
	pkgs, err := coverPackages(ctx, projectDir, patterns)
	if err != nil {
		return nil, err
	}
	flags := []string{"-cover", "-covermode=" + coverMode}
	if len(pkgs) > 0 {
		flags = append(flags, "-coverpkg="+strings.Join(pkgs, ","))
	}
	return flags, nil
}

// coverPackages are the -coverpkg patterns: patterns as declared, or "main"
// (mainModuleCoverPatterns) when there are none.
func coverPackages(ctx context.Context, projectDir string, patterns []string) ([]string, error) {
	if len(patterns) > 0 {
		return patterns, nil
	}
	return mainModuleCoverPatterns(ctx, projectDir)
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
