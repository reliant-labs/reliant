package cli

// Profile-guided optimization: which Go binaries an env builds, the PGO each
// is built with, and the workloads that run it (kcl/schema.k PGO).
//
// Go applies PGO per main package: `go build ./cmd/<bin>` reads
// cmd/<bin>/default.pgo when it exists (`-pgo=auto`, the default). So the
// unit of configuration is the BINARY, not the workload, and one binary has
// one profile. A modular-monolith binary that runs as the server, a worker
// and a cron gets ONE profile merging every workload that runs it, and the
// workloads (OTEL service names = workload names, kcl/render.k) that run a
// binary come from the rendered env, never from a naming convention.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/profilequery"
)

// The kcl/schema.k PGO defaults and bounds. The render omits a default PGO,
// and a hand-written env document may name only what it changes, so the
// decoder applies these to whatever is absent.
const (
	pgoDefaultSince     = 168 * time.Hour
	pgoDefaultMinWeight = 10 * time.Second
	// pgoMaxSince is how long forge keeps the profile edges pull reads.
	pgoMaxSince = 720 * time.Hour
)

// PGOConfig is a PGO as an env document carries it (kcl/schema.k PGO). Every
// field is optional: absent is the schema default.
type PGOConfig struct {
	// Enabled false builds the binary with -pgo=off; absent is true.
	Enabled *bool `json:"enabled,omitempty"`
	// FromEnv is the env whose profiles `forge pgo pull` merges; empty is
	// the env that declares the build.
	FromEnv string `json:"from_env,omitempty"`
	// Since is how far back pull reads, a Go duration of at most 720h;
	// empty is 168h.
	Since string `json:"since,omitempty"`
	// MinWeight is the least edge weight the window must hold (sampled CPU
	// summed over call edges, not unique CPU time), a Go duration; empty is 10s.
	MinWeight string `json:"min_weight,omitempty"`
	// Services are the OTEL service names whose profiles are merged; empty
	// is every workload that runs the binary.
	Services []string `json:"services,omitempty"`
}

// PGOSettings is a binary's effective PGO in one env: the declaration with
// every default applied.
type PGOSettings struct {
	// Enabled false: builds pass -pgo=off and `forge pgo pull` skips it.
	Enabled bool
	// FromEnv is the env whose profiles pull merges; the declaring env when
	// the declaration left it empty.
	FromEnv   string
	Since     time.Duration
	MinWeight time.Duration
	// Services are the declared OTEL service names, sorted; empty means
	// every workload that runs the binary (PGOBinary.Services).
	Services []string
	// Out is where `forge pgo pull` writes the profile, relative to the
	// project root with forward slashes (or absolute, for a build whose cwd
	// is); empty is the binary's own <package>/default.pgo. Only a
	// ShellBuild sets it (shellPGOBinary).
	Out string
	// Namespace is the OTEL service.namespace whose profiles are merged;
	// empty is the env's project. Only a ShellBuild sets it.
	Namespace string
}

// PGOBinary is one Go binary an env builds.
type PGOBinary struct {
	// Package is the binary's main package as a project-relative path,
	// "./cmd/app".
	Package string
	PGO     PGOSettings
	// Workloads are the workloads of the resolved env that run the binary,
	// sorted. Empty for a binary a DockerBuild names in its `pgo` that no
	// workload of this env runs.
	Workloads []string
	// Services are the OTEL service names whose profiles make the binary's
	// profile: PGO.Services when declared, else Workloads. When PGO.FromEnv
	// is another env, Workloads are still THIS env's: a caller reading that
	// env's profiles resolves that env's render for its own workloads.
	Services []string
}

// Dir is the binary's cmd/<dir> leaf, "app" for ./cmd/app: the Dockerfile's
// /app/<dir> and the PGO build arg's name (templates.PGOBuildArg).
func (b PGOBinary) Dir() string { return path.Base(b.Package) }

// ProfilePath is where the binary's committed profile lives, relative to
// the project root with forward slashes: "cmd/app/default.pgo". `go build`
// reads exactly this file under -pgo=auto. A ShellBuild may name another
// (PGOSettings.Out), such as a sibling checkout's
// "../reliant/cmd/reliant/default.pgo".
func (b PGOBinary) ProfilePath() string {
	if b.PGO.Out != "" {
		return b.PGO.Out
	}
	return path.Join(strings.TrimPrefix(b.Package, "./"), "default.pgo")
}

// ResolvePGOBinaries returns every Go binary the env builds, sorted by
// package, with its effective PGO and the workloads that run it:
//
//   - a GoBuild workload runs GoBuild.cmd and declares GoBuild.pgo for it;
//   - a DockerBuild workload declares DockerBuild.pgo for each binary it
//     names, and runs the binary its command selects inside the image —
//     /app/<bin> (or /app/bin/<bin>) is ./cmd/<bin>, and with no command the
//     project Dockerfile's ENTRYPOINT runs the primary binary, ./cmd/<project>.
//     A workload on another Dockerfile runs a binary only when its pgo names
//     it (the command's, or its only key); forge cannot see inside the image.
//   - a ShellBuild workload declares ShellBuild.pgo for each binary it names
//     (keyed by package relative to its cwd), and runs the one it names, or
//     the one its command's executable is named after (shellWorkloadBinary).
//     forge cannot see what a shell command builds, so a binary it does not
//     name is not one of the env's.
//
// Workloads of one env that run, or name, the same binary must agree on its
// PGO — a workload that runs it without naming it declares the defaults — or
// the env is refused, naming both workloads.
func ResolvePGOBinaries(e *KCLEntities) ([]PGOBinary, error) {
	if e == nil {
		return nil, nil
	}
	type claim struct {
		workload string
		pgo      PGOSettings
	}
	claims := map[string]claim{}
	runs := map[string][]string{}
	claimSettings := func(pkg, workload string, s PGOSettings) error {
		prev, ok := claims[pkg]
		if !ok {
			claims[pkg] = claim{workload: workload, pgo: s}
			return nil
		}
		if !prev.pgo.equal(s) {
			return fmt.Errorf("workloads %q and %q both build %s with different PGO (%s vs %s): one binary has one profile, "+
				"so every workload of an env that runs it declares the same `pgo` on its build (kcl/schema.k PGO)",
				prev.workload, workload, pkg, prev.pgo, s)
		}
		return nil
	}
	declare := func(pkg, workload string, cfg *PGOConfig) error {
		s, err := cfg.settings(e.Env)
		if err != nil {
			return fmt.Errorf("workload %q: binary %s: %w", workload, pkg, err)
		}
		return claimSettings(pkg, workload, s)
	}
	for _, w := range e.Workloads {
		switch {
		case w.Build.Type == "go" && w.Build.Go != nil:
			pkg := normalizePGOPackage(w.Build.Go.Cmd)
			if pkg == "" {
				continue
			}
			if err := declare(pkg, w.Name, w.Build.Go.PGO); err != nil {
				return nil, err
			}
			runs[pkg] = append(runs[pkg], w.Name)
		case w.Build.Type == "docker" && w.Build.Docker != nil:
			d := w.Build.Docker
			named := map[string]bool{}
			for _, k := range sortedKeys(d.PGO) {
				pkg := normalizePGOPackage(k)
				cfg := d.PGO[k]
				if err := declare(pkg, w.Name, &cfg); err != nil {
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
		case w.Build.Type == "shell" && w.Build.Shell != nil:
			sh := w.Build.Shell
			named := map[string]string{} // key -> package
			for _, k := range sortedKeys(sh.PGO) {
				pkg, s, err := shellPGOBinary(sh, k, e.Env)
				if err != nil {
					return nil, fmt.Errorf("workload %q: ShellBuild.pgo[%q]: %w", w.Name, k, err)
				}
				if err := claimSettings(pkg, w.Name, s); err != nil {
					return nil, err
				}
				named[k] = pkg
			}
			if pkg := shellWorkloadBinary(w, named); pkg != "" {
				runs[pkg] = append(runs[pkg], w.Name)
			}
		}
	}
	out := make([]PGOBinary, 0, len(claims))
	for _, pkg := range sortedKeys(claims) {
		workloads := slices.Compact(slices.Sorted(slices.Values(runs[pkg])))
		services := claims[pkg].pgo.Services
		if len(services) == 0 {
			services = workloads
		}
		out = append(out, PGOBinary{Package: pkg, PGO: claims[pkg].pgo, Workloads: workloads, Services: services})
	}
	return out, nil
}

// pgoImageCollision refuses a binary an image build turns PGO off for when
// another binary built in the SAME image shares its Dockerfile PGO arg
// ("./cmd/my-app" and "./cmd/my_app" are both FORGE_PGO_MY_APP): the
// `--build-arg` would turn PGO off for that one too. It is checked only at
// the image build that passes the arg, never at render: two binaries whose
// args collide are fine until one of them is disabled in an image that
// builds the other, so a project that declares no PGO never meets it. off
// is the disabled binaries; built is every binary the image builds.
func pgoImageCollision(off map[string]bool, built []string) error {
	built = slices.Compact(slices.Sorted(slices.Values(built)))
	for _, pkg := range sortedKeys(off) {
		if !off[pkg] {
			continue
		}
		name := templates.PGOBuildArg(path.Base(pkg))
		for _, other := range built {
			if other == pkg || off[other] || templates.PGOBuildArg(path.Base(other)) != name {
				continue
			}
			return fmt.Errorf("binaries %s and %s are built in one image and share the Dockerfile PGO build arg %s, "+
				"so turning PGO off for %s would turn it off for %s too: rename one binary's directory",
				min(pkg, other), max(pkg, other), name, pkg, other)
		}
	}
	return nil
}

// projectImagePGOCollision is pgoImageCollision for the project image,
// which builds every cmd/<dir> with a main.go — including dirs no workload
// names.
func projectImagePGOCollision(projectDir string, off map[string]bool) error {
	if len(off) == 0 {
		return nil
	}
	return pgoImageCollision(off, projectCmdBinaries(projectDir))
}

// serviceImagePGOCollision is pgoImageCollision for a DockerBuild workload's
// image: it builds the binaries its pgo names and, on the project
// Dockerfile, every cmd/<dir> as well. A binary whose arg the build's own
// build_args sets is the author's to set, so forge passes nothing for it.
func serviceImagePGOCollision(projectDir string, d *DockerBuild) error {
	if d == nil {
		return nil
	}
	off := dockerPGODisabled(d)
	for pkg := range off {
		if _, own := d.BuildArgs[templates.PGOBuildArg(path.Base(pkg))]; own {
			delete(off, pkg)
		}
	}
	if len(off) == 0 {
		return nil
	}
	var built []string
	for k := range d.PGO {
		built = append(built, normalizePGOPackage(k))
	}
	if isProjectDockerfile(d) {
		built = append(built, projectCmdBinaries(projectDir)...)
	}
	return pgoImageCollision(off, built)
}

// projectCmdBinaries is every ./cmd/<dir> of the project with a main.go:
// what the scaffolded Dockerfile builds.
func projectCmdBinaries(projectDir string) []string {
	var out []string
	entries, _ := os.ReadDir(filepath.Join(projectDir, "cmd"))
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(projectDir, "cmd", e.Name(), "main.go")); e.IsDir() && err == nil {
			out = append(out, "./cmd/"+e.Name())
		}
	}
	return out
}

// pgoDisabledPackages is the set of binaries the env builds with -pgo=off.
// An env whose PGO does not resolve was refused at render
// (RenderKCLWith), so an error here only means an entity set built without
// one: it disables nothing.
func pgoDisabledPackages(e *KCLEntities) map[string]bool {
	bins, err := ResolvePGOBinaries(e)
	if err != nil {
		return nil
	}
	off := map[string]bool{}
	for _, b := range bins {
		if !b.PGO.Enabled {
			off[b.Package] = true
		}
	}
	return off
}

// dockerPGODisabled is the set of binaries d's pgo disables.
func dockerPGODisabled(d *DockerBuild) map[string]bool {
	off := map[string]bool{}
	for k, c := range d.PGO {
		if c.Enabled != nil && !*c.Enabled {
			off[normalizePGOPackage(k)] = true
		}
	}
	return off
}

// dockerWorkloadBinary is the binary a DockerBuild workload runs, or "".
// named is the set of binaries its pgo names.
//
// kcl/render.k _pgo_docker_binary is the same rule, and the two must agree:
// it decides which workloads get FORGE_PROFILES_PGO=off, this which binaries
// the build disables. kcl/tests/positive_build_pgo_docker_binary.k is
// rendered by both sides (TestPGODockerBinaryAgreesWithKCL).
func dockerWorkloadBinary(w WorkloadEntity, d *DockerBuild, primary string, named map[string]bool) string {
	fromCommand := ""
	if len(w.Spec.Command) > 0 {
		if bin := commandBinary(w.Spec.Command[0]); bin != "" {
			fromCommand = "./cmd/" + bin
		}
	}
	if isProjectDockerfile(d) {
		switch {
		case fromCommand != "":
			return fromCommand
		case len(w.Spec.Command) > 0:
			return "" // a command of its own, not one of the image's binaries
		case primary != "":
			return "./cmd/" + primary
		}
		return ""
	}
	if fromCommand != "" && named[fromCommand] {
		return fromCommand
	}
	if len(named) == 1 {
		for pkg := range named {
			return pkg
		}
	}
	return ""
}

// commandBinary is the image binary a workload command names, or "": the
// scaffolded Dockerfile puts cmd/<bin> at /app/<bin> (its builder at
// /app/bin/<bin>), and its WORKDIR is /app, so `/app/<bin>`,
// `/app/bin/<bin>` and the relative `bin/<bin>` all name <bin>. Anything
// else, or a <bin> with a `/`, is not one of the image's binaries.
func commandBinary(c0 string) string {
	var bin string
	switch {
	case strings.HasPrefix(c0, "/app/bin/"):
		bin = c0[len("/app/bin/"):]
	case strings.HasPrefix(c0, "/app/"):
		bin = c0[len("/app/"):]
	case strings.HasPrefix(c0, "bin/"):
		bin = c0[len("bin/"):]
	}
	if strings.Contains(bin, "/") {
		return ""
	}
	return bin
}

// isProjectDockerfile reports whether d builds the project's own Dockerfile
// from the project root: the scaffolded one, which builds every cmd/<bin>
// into /app/<bin> and runs the primary binary by default.
func isProjectDockerfile(d *DockerBuild) bool {
	if d.DockerfileSource != nil || d.ContextSource != nil {
		return false
	}
	dockerfile := pgoProjectPath(d.Dockerfile)
	return (dockerfile == "" || dockerfile == "Dockerfile") && pgoProjectPath(d.Context) == ""
}

// pgoProjectPath spells a project-relative path the one way the PGO rules
// compare it: surrounding space trimmed, and empty and `.` segments dropped,
// so "./", "." and "" are all the root and "./Dockerfile" is "Dockerfile".
// An absolute path is kept as it is. kcl/render.k _pgo_project_path is the
// same function; it has no path.Clean, so neither side resolves "..".
func pgoProjectPath(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "/") {
		return p
	}
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			segs = append(segs, s)
		}
	}
	return strings.Join(segs, "/")
}

// normalizePGOPackage spells a build target the one way PGO keys it:
// "cmd/app", "./cmd/app/" and "./cmd/app" are all "./cmd/app". An import
// path ("example.com/x/cmd/app") is kept as it is.
func normalizePGOPackage(p string) string {
	p = strings.TrimSpace(filepath.ToSlash(p))
	if p == "" {
		return ""
	}
	c := path.Clean(p)
	if c == "." || strings.HasPrefix(c, "/") || strings.HasPrefix(c, "../") {
		return c
	}
	if first, _, _ := strings.Cut(c, "/"); strings.Contains(first, ".") && !strings.HasPrefix(p, ".") {
		return c // an import path: its first element is a domain
	}
	return "./" + c
}

// settings applies the schema defaults to c (nil is every default) and
// checks what a hand-written env document could get wrong; a KCL render
// already passed the same checks.
func (c *PGOConfig) settings(env string) (PGOSettings, error) {
	s := PGOSettings{Enabled: true, FromEnv: env, Since: pgoDefaultSince, MinWeight: pgoDefaultMinWeight}
	if c == nil {
		return s, nil
	}
	if c.Enabled != nil {
		s.Enabled = *c.Enabled
	}
	if c.FromEnv != "" {
		s.FromEnv = c.FromEnv
	}
	if c.Since != "" {
		d, err := time.ParseDuration(c.Since)
		if err != nil || d <= 0 {
			return PGOSettings{}, fmt.Errorf("PGO.since %q is not a positive duration (e.g. \"168h\")", c.Since)
		}
		if d > pgoMaxSince {
			return PGOSettings{}, fmt.Errorf("PGO.since %q is longer than %s, how long forge keeps profile edges", c.Since, pgoMaxSince)
		}
		s.Since = d
	}
	if c.MinWeight != "" {
		d, err := time.ParseDuration(c.MinWeight)
		if err != nil || d < 0 {
			return PGOSettings{}, fmt.Errorf("PGO.min_weight %q is not a duration (e.g. \"10s\")", c.MinWeight)
		}
		s.MinWeight = d
	}
	for _, svc := range c.Services {
		if svc = strings.TrimSpace(svc); svc != "" {
			s.Services = append(s.Services, svc)
		}
	}
	s.Services = slices.Compact(slices.Sorted(slices.Values(s.Services)))
	return s, nil
}

// shellPGOBinary resolves the binary a ShellBuild's pgo names by key (its
// package path relative to the build's cwd): the binary's package, as the
// project-relative path PGO keys every binary by ("../reliant/cmd/reliant"
// for cwd "../reliant"), and its effective PGO, with the profile's path in
// Out unless it is the package's own default.pgo. A build with a `source`
// runs in a checkout forge resolves per invocation, so its binary is keyed
// by the repository's path and must name `out` (kcl/schema.k ShellPGO).
func shellPGOBinary(sh *ShellBuild, key, env string) (string, PGOSettings, error) {
	cfg := sh.PGO[key]
	s, err := cfg.settings(env)
	if err != nil {
		return "", PGOSettings{}, err
	}
	out := strings.TrimSpace(filepath.ToSlash(cfg.Out))
	if path.IsAbs(out) || strings.HasSuffix(out, "/") {
		return "", PGOSettings{}, fmt.Errorf("ShellPGO.out %q is the profile file's path relative to the project root, e.g. \"../reliant/cmd/reliant/default.pgo\"", cfg.Out)
	}
	rel := strings.TrimPrefix(path.Clean(filepath.ToSlash(key)), "./")
	var pkg string
	if sh.Source != nil {
		if out == "" {
			return "", PGOSettings{}, fmt.Errorf("the build has a `source`, so ShellPGO.out must say where the profile goes")
		}
		pkg = path.Join(gitSourcePath(sh.Source.Repo), sh.Source.Subdir, rel)
	} else {
		pkg = normalizePGOPackage(path.Join(filepath.ToSlash(sh.Cwd), rel))
	}
	own := path.Join(strings.TrimPrefix(pkg, "./"), "default.pgo")
	if out == "" {
		out = own
	}
	if out = path.Clean(out); out != own || !strings.HasPrefix(pkg, "./") {
		// Only a binary of this project has a default.pgo the package
		// path alone names; anything else carries its path.
		s.Out = out
	}
	s.Namespace = strings.TrimSpace(cfg.Namespace)
	return pkg, s, nil
}

// gitSourcePath spells a GitSource repo as a path: "github.com/acme/api" for
// it and for "https://github.com/acme/api.git" or "git@github.com:acme/api".
func gitSourcePath(repo string) string {
	r := strings.TrimSpace(repo)
	if _, rest, ok := strings.Cut(r, "://"); ok {
		r = rest
	}
	if at := strings.Index(r, "@"); at >= 0 && at < strings.IndexAny(r+"/", ":/") {
		r = strings.Replace(r[at+1:], ":", "/", 1)
	}
	return strings.TrimSuffix(strings.TrimSuffix(r, "/"), ".git")
}

// shellWorkloadBinary is the binary a ShellBuild workload runs among those
// its pgo names (key -> package): the only one, or the one whose directory
// the command's executable is named after. "" when forge cannot tell, so the
// binary's profiles are read only from the services its pgo names.
func shellWorkloadBinary(w WorkloadEntity, named map[string]string) string {
	if len(named) == 1 {
		for _, pkg := range named {
			return pkg
		}
	}
	if len(w.Spec.Command) == 0 {
		return ""
	}
	exe, match := path.Base(w.Spec.Command[0]), ""
	for _, k := range sortedKeys(named) {
		if path.Base(named[k]) == exe {
			if match != "" {
				return ""
			}
			match = named[k]
		}
	}
	return match
}

func (s PGOSettings) equal(o PGOSettings) bool {
	return s.Enabled == o.Enabled && s.FromEnv == o.FromEnv && s.Since == o.Since &&
		s.MinWeight == o.MinWeight && slices.Equal(s.Services, o.Services) &&
		s.Out == o.Out && s.Namespace == o.Namespace
}

// String is the settings as an error message names them.
func (s PGOSettings) String() string {
	if !s.Enabled {
		return "enabled=False"
	}
	out := fmt.Sprintf("from_env=%q since=%s min_weight=%s", s.FromEnv, s.Since, s.MinWeight)
	if len(s.Services) > 0 {
		out += " services=" + strings.Join(s.Services, ",")
	}
	if s.Out != "" {
		out += " out=" + s.Out
	}
	if s.Namespace != "" {
		out += " namespace=" + s.Namespace
	}
	return out
}

// pgoOffFlag is what a disabled binary's host `go build` adds: Go's default
// -pgo=auto would otherwise read a committed default.pgo.
const pgoOffFlag = "-pgo=off"

// userSetsPGO reports whether the build already chooses its profile: a
// `-pgo` among GoBuild.flags, or in GOFLAGS of GoBuild.env. The user's own
// choice always wins over the KCL PGO.
func userSetsPGO(flags []string, env map[string]string) bool {
	all := slices.Concat(flags, strings.Fields(env["GOFLAGS"]))
	for _, f := range all {
		name, _, _ := strings.Cut(strings.TrimLeft(f, "-"), "=")
		if strings.HasPrefix(f, "-") && name == "pgo" {
			return true
		}
	}
	return false
}

// pgoOffBuildArgs are the `--build-arg FORGE_PGO_<BIN>=off` pairs for the
// disabled binaries among pkgs, sorted and deduplicated, skipping any the
// build's own build_args already set (a declared build arg wins). Only a
// ./cmd/<bin> binary has an ARG in the scaffolded Dockerfile; a Dockerfile
// without the ARG ignores it.
func pgoOffBuildArgs(off map[string]bool, own map[string]string) []string {
	var args []string
	seen := map[string]bool{}
	for _, pkg := range sortedKeys(off) {
		if !off[pkg] {
			continue
		}
		name := templates.PGOBuildArg(path.Base(pkg))
		if _, declared := own[name]; declared || seen[name] {
			continue
		}
		seen[name] = true
		args = append(args, "--build-arg", name+"=off")
	}
	return args
}

// printPGOPlan prints one informational line per Go binary the env builds:
// the committed profile its build reads and how old it is, that it has
// none, or that PGO is off. It never fails a build.
func printPGOPlan(w io.Writer, projectDir string, e *KCLEntities, now time.Time) {
	bins, err := ResolvePGOBinaries(e)
	if err != nil {
		return
	}
	for _, b := range bins {
		fmt.Fprintf(w, "[build]   pgo:      %s\n", pgoProfileNote(projectDir, b, now))
	}
}

// pgoProfileNote describes the profile b's build reads (describePGOFile).
func pgoProfileNote(projectDir string, b PGOBinary, now time.Time) string {
	if !b.PGO.Enabled {
		return "off for " + strings.TrimPrefix(b.Package, "./")
	}
	rel := b.ProfilePath()
	desc := describePGOFile(filepath.Join(projectDir, filepath.FromSlash(rel)), now)
	if desc == "" {
		return "none for " + strings.TrimPrefix(b.Package, "./") + " (forge pgo pull writes one)"
	}
	return rel + " (" + desc + ")"
}

// describePGOFile says where the default.pgo at path came from, read from
// the provenance `forge pgo pull` writes into it (profilequery.ReadPGO), never
// from the file's mtime, which a clone resets: "from prod, data up to 6d old",
// "not written by forge", or "unreadable: <why>". "" when there is no file.
// It never fails: the build and `forge pgo status` only report it.
func describePGOFile(path string, now time.Time) string {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		return "unreadable: " + err.Error()
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err == nil && info.IsDir() {
		return ""
	}
	p, err := profilequery.ReadPGO(f)
	switch {
	case errors.Is(err, profilequery.ErrNotForgePGO):
		return "not written by forge"
	case err != nil:
		return "unreadable: " + err.Error()
	}
	from := "from " + p.Env
	if p.Env == "" {
		from = "from every env"
	}
	if p.Newest.IsZero() {
		return from + ", no profile data"
	}
	return from + ", data up to " + pgoAge(now.Sub(p.Newest)) + " old"
}

// pgoAge is an age at a glance: "6d", "3h", "5m", "<1m".
func pgoAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return "<1m"
}
