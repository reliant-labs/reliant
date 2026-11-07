package cigate

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/frontenddeps"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/internal/toolpin"
)

// Forge owns the step list below. The scaffolded ci.yml renders the same
// toggles (templates.CIWorkflowData) and the same tool pins
// (templates.*Version), and a test holds the workflow's pins to these, so the
// gate and the workflow cannot describe two different CIs. A project adds
// checks (forge.yaml ci.gate.extra_checks) and skips checks (CI_SKIP); it does
// not copy this list into a script of its own.

// Pins. Version strings come from templates so the workflow and the gate read
// one constant.
var (
	golangciLint = Tool{
		Binary:      "golangci-lint",
		Module:      "github.com/golangci/golangci-lint/v2/cmd/golangci-lint",
		BuildModule: "github.com/golangci/golangci-lint/v2",
		Version:     templates.GolangciLintVersion,
		MatchGo:     true,
		VersionArgs: []string{"--version"},
		VersionRE:   regexp.MustCompile(`has version v?(\d+\.\d+\.\d+)`),
	}
	// buf is forge's pin; a project that declares its own in go.mod runs
	// that one instead (BufTool).
	buf = Tool{
		Binary:      "buf",
		Module:      "github.com/bufbuild/buf/cmd/buf",
		BuildModule: "github.com/bufbuild/buf",
		Version:     "v" + templates.BufVersion,
		VersionArgs: []string{"--version"},
		VersionRE:   regexp.MustCompile(`^v?(\d+\.\d+\.\d+)`),
	}
	govulncheck = Tool{
		Binary:      "govulncheck",
		Module:      "golang.org/x/vuln/cmd/govulncheck",
		BuildModule: "golang.org/x/vuln",
		Version:     templates.GovulncheckVersion,
	}
	goLicenses = Tool{
		Binary:  "go-licenses",
		Module:  "github.com/google/go-licenses",
		Version: templates.GoLicensesVersion,
	}
)

// BufTool is the buf the project at root pins (internal/toolpin): its go.mod's
// github.com/bufbuild/buf when it declares one, forge's pin otherwise. The
// gate holds PATH to it for verify-generated and buf-lint, so the gate, `forge
// generate` and the scaffolded ci.yml run one buf.
func BufTool(root string) Tool {
	t := buf
	t.Version = toolpin.BufFor(root).Version
	return t
}

// PlanInput is everything the plan is derived from.
type PlanInput struct {
	// Root is the project root.
	Root string
	// CI is the data the scaffolded ci.yml renders with: which lints, scanners
	// and jobs the project has. The gate reads the same struct so a toggle
	// means the same thing in both.
	CI templates.CIWorkflowData
	// Cfg supplies the project's extra checks and its derived features.
	Cfg *config.ProjectConfig
	// Forge is the argv that re-enters this forge binary.
	Forge []string
}

// Groups forge's own checks carry, for CI_ONLY (`CI_ONLY=kcl`). A project's
// extra checks default to GroupExtras.
const (
	GroupSetup       = "setup"
	GroupStatic      = "static"
	GroupKCL         = "kcl"
	GroupTest        = "test"
	GroupIntegration = "integration"
	GroupExtras      = "extras"
)

// Plan returns every check of every lane, in the order the table prints them.
// The order is not the execution order: the scheduler (Run) starts each check
// as soon as its dependencies, the tree and a host slot allow.
func Plan(in PlanInput) ([]Check, error) {
	p := planner{PlanInput: in, tree: &treeSnapshot{root: in.Root}}
	lanes := [][]Check{p.checksLane(), p.testLane(), p.integrationLane()}
	var owned []Check
	for _, l := range lanes {
		owned = append(owned, l...)
	}
	replaced, extras, err := p.extras(owned)
	if err != nil {
		return nil, err
	}
	// A replacement takes the replaced check's row; extras follow their
	// lane's own checks, and lanes of the project's own naming come last.
	byLane := map[Lane][]Check{}
	var custom []Lane
	for _, e := range extras {
		if !isBuiltinLane(e.Lane) && len(byLane[e.Lane]) == 0 {
			custom = append(custom, e.Lane)
		}
		byLane[e.Lane] = append(byLane[e.Lane], e)
	}
	var out []Check
	for _, l := range lanes {
		for _, c := range l {
			if r, ok := replaced[c.Name]; ok {
				c = r
			}
			out = append(out, c)
		}
		if len(l) > 0 {
			out = append(out, byLane[l[0].Lane]...)
			delete(byLane, l[0].Lane)
		}
	}
	for _, l := range []Lane{LaneChecks, LaneTest, LaneIntegration} {
		out = append(out, byLane[l]...) // a built-in lane forge has no checks in
	}
	for _, l := range custom {
		out = append(out, byLane[l]...)
	}
	if err := validateGraph(out); err != nil {
		return nil, err
	}
	return out, nil
}

func isBuiltinLane(l Lane) bool {
	return l == LaneChecks || l == LaneTest || l == LaneIntegration
}

// Lanes lists every lane the plan has, built-in ones first.
func Lanes(all []Check) []Lane {
	seen := map[Lane]bool{}
	out := []Lane{LaneChecks, LaneTest, LaneIntegration}
	for _, l := range out {
		seen[l] = true
	}
	for _, c := range all {
		if !seen[c.Lane] {
			seen[c.Lane] = true
			out = append(out, c.Lane)
		}
	}
	return out
}

// validateGraph rejects an `after` that names no check, and a dependency
// cycle — either would otherwise surface as a gate that waits forever.
func validateGraph(all []Check) error {
	idx := map[string]int{}
	for i, c := range all {
		if _, ok := idx[c.Name]; !ok {
			idx[c.Name] = i
		}
	}
	for _, c := range all {
		for _, a := range c.After {
			if _, ok := idx[a]; !ok {
				return fmt.Errorf("check %q: after names %q, which is not a check (forge's own and yours: `forge ci gate --list`)", c.Name, a)
			}
		}
	}
	const (
		unseen = iota
		visiting
		visited
	)
	state := make([]int, len(all))
	var path []string
	var visit func(i int) error
	visit = func(i int) error {
		switch state[i] {
		case visiting:
			return fmt.Errorf("checks depend on each other in a cycle: %s -> %s", strings.Join(path, " -> "), all[i].Name)
		case visited:
			return nil
		}
		state[i] = visiting
		path = append(path, all[i].Name)
		for _, a := range all[i].After {
			if err := visit(idx[a]); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		state[i] = visited
		return nil
	}
	for i := range all {
		if err := visit(i); err != nil {
			return err
		}
	}
	return nil
}

// Select keeps the checks of the named lanes, in plan order, dropping a check
// whose name an earlier selected lane already ran (the frontend-deps row sits
// in both the checks and test lanes, so `task ci` installs once).
func Select(all []Check, lanes []Lane) []Check {
	want := map[Lane]bool{}
	for _, l := range lanes {
		want[l] = true
	}
	seen := map[string]bool{}
	var out []Check
	for _, c := range all {
		if !want[c.Lane] || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		out = append(out, c)
	}
	return out
}

type planner struct {
	PlanInput
	tree *treeSnapshot
}

func (p planner) forge(args ...string) []string {
	return append(append([]string(nil), p.Forge...), args...)
}

func (p planner) frontendDeps(lane Lane) []Check {
	if !p.CI.HasFrontends {
		return nil
	}
	var out []Check
	for _, fe := range p.CI.Frontends {
		fe := fe
		inst := frontenddeps.InstallArgv(p.path(fe.Path), "", true)
		depsArgv := frontenddeps.ExecArgv(p.path(fe.Path), inst[0], inst[1:]...)
		out = append(out, Check{
			Name:  depsName(fe),
			Lane:  lane,
			Group: GroupSetup,
			// Whether node_modules is there is not a fact about the tree, so
			// a cached verdict could not stand in for this row.
			NoCache: true,
			// A frozen install replaces node_modules, so the gate installs only what is
			// absent. CI installs first, in the shared setup, so this row is
			// instant there.
			Done: func() string {
				if depsCurrent(p.path(fe.Path)) {
					return "already installed"
				}
				return ""
			},
			Requires: []string{depsArgv[0]},
			// The frontend's own package manager, frozen to its lockfile:
			// `npm ci` (or `npm install` when a workspace root — forge's dev
			// web-runtime bridge — adopts the frontend without a lockfile of
			// its own, where `npm ci` cannot run at all), or pnpm/yarn with
			// --frozen-lockfile.
			Argv: depsArgv,
			Dir:  fe.Path,
		})
	}
	return out
}

func depsName(fe templates.FrontendCIConfig) string { return "deps-" + fe.Name }

// depsCurrent reports whether dir's node_modules is installed from its
// current lockfile: the package manager rewrites its own stamp on every
// install (npm node_modules/.package-lock.json, pnpm node_modules/.modules.yaml),
// so one older than the lockfile means the lockfile moved on (a rebase, a
// dependency bump) and the install is stale.
func depsCurrent(dir string) bool {
	if !dirExists(filepath.Join(dir, "node_modules")) {
		return false
	}
	runner := frontenddeps.PackageManager(dir, "")
	lock, err := os.Stat(filepath.Join(dir, frontenddeps.LockfileName(runner)))
	if err != nil {
		return true // no lockfile to be stale against
	}
	stamp, err := os.Stat(frontenddeps.InstallStamp(dir, runner))
	return err == nil && !stamp.ModTime().Before(lock.ModTime())
}

// feRun runs a package.json script in the frontend with its own package
// manager.
func (p planner) feRun(fe templates.FrontendCIConfig, args ...string) []string {
	dir := p.path(fe.Path)
	return frontenddeps.ExecArgv(dir, frontenddeps.PackageManager(dir, ""), append([]string{"run"}, args...)...)
}

// allDeps names every frontend's deps row: what verify-generated (whose
// regenerate runs protoc-gen-es out of node_modules) and the unit lane wait
// for.
func (p planner) allDeps() []string {
	if !p.CI.HasFrontends {
		return nil
	}
	var out []string
	for _, fe := range p.CI.Frontends {
		out = append(out, depsName(fe))
	}
	return out
}

func (p planner) path(parts ...string) string {
	return filepath.Join(append([]string{p.Root}, parts...)...)
}

func (p planner) checksLane() []Check {
	ci := p.CI
	var out []Check

	// 1. Frontend dependencies, so every npm row below has what it needs.
	out = append(out, p.frontendDeps(LaneChecks)...)

	// 2. verify-generated regenerates a scratch copy of the checkout and
	// never writes the tree, so it runs beside the checks that read it. It
	// still waits for the dependency installs: the TypeScript stubs are
	// regenerated with the protoc-gen-es a frontend's node_modules holds.
	if ci.VerifyGenerated {
		vg := Check{
			Name:  "verify-generated",
			Lane:  LaneChecks,
			Group: GroupStatic,
			Heavy: true,
			After: p.allDeps(),
			Argv:  p.forge("ci", "verify-generated"),
		}
		if ci.RunsBufGenerate {
			vg.Tools = []Tool{BufTool(p.Root)}
		}
		out = append(out, vg)
	}

	// 3. Lint.
	if ci.LintGolangci {
		out = append(out, Check{
			Name:  "golangci-lint",
			Lane:  LaneChecks,
			Group: GroupStatic,
			Heavy: true,
			Tools: []Tool{golangciLint},
			// No --timeout: the flag overrides the project's own
			// .golangci.yml, whose scaffolded `timeout: 20m` is deliberate —
			// a cold cache on a loaded machine crosses 5m and exits 4
			// ("Timeout exceeded") with 0 issues, a red gate over clean code.
			Argv: []string{"golangci-lint", "run", "--allow-serial-runners"},
			// In its own heavy slot the lint drops golangci-lint's
			// machine-global lock: the slot bounds the memory the lock
			// protected, and keeping both ran every golangci-lint on the
			// machine one at a time while each waiter held a slot.
			SlotArgv: []string{"golangci-lint", "run", "--allow-parallel-runners"},
		})
	}
	if ci.LintBuf && ci.HasServices {
		out = append(out, Check{
			Name:  "buf-lint",
			Lane:  LaneChecks,
			Group: GroupStatic,
			Tools: []Tool{BufTool(p.Root)},
			Argv:  []string{"buf", "lint"},
		})
	}
	if ci.LintMigrationSafety {
		c := Check{
			Name:  "migration-safety",
			Lane:  LaneChecks,
			Group: GroupStatic,
			Argv:  p.forge("ci", "migration-safety"),
		}
		if !dirExists(p.path("db", "migrations")) {
			c.NotApplicable = "no db/migrations/ directory"
		}
		out = append(out, c)
	}
	if ci.LintFrontend && ci.HasFrontends {
		for _, fe := range ci.Frontends {
			fe := fe
			needDeps, after := p.needsDeps(fe), []string{depsName(fe)}
			out = append(out,
				Check{Name: "lint-" + fe.Name, Lane: LaneChecks, Group: GroupStatic, Heavy: true, After: after, Prereq: needDeps, Argv: p.feRun(fe, "lint"), Dir: fe.Path},
				// tsc --noEmit is not covered by eslint; without a dedicated
				// row, type errors only surface at `next build` in a deploy.
				Check{Name: "typecheck-" + fe.Name, Lane: LaneChecks, Group: GroupStatic, Heavy: true, After: after, Prereq: needDeps, Argv: p.feRun(fe, "typecheck"), Dir: fe.Path},
			)
			if ci.LintFrontendStyles {
				css := Check{Name: "css-health-" + fe.Name, Lane: LaneChecks, Group: GroupStatic, After: after, Prereq: needDeps, Argv: p.feRun(fe, "lint:styles"), Dir: fe.Path}
				// Yarn (classic and Berry) does not accept --if-present.
				// Determine applicability from the manifest instead of passing
				// one manager's optional-script flag to every manager.
				data, err := os.ReadFile(p.path(fe.Path, "package.json"))
				var manifest struct {
					Scripts map[string]string `json:"scripts"`
				}
				if err == nil {
					err = json.Unmarshal(data, &manifest)
				}
				if err != nil {
					css.Prereq = func() string { return "cannot determine optional lint:styles script: " + err.Error() }
				} else if manifest.Scripts["lint:styles"] == "" {
					css.NotApplicable = "package.json declares no lint:styles script"
				}
				out = append(out, css)
			}
		}
	}

	// 4. Build. -trimpath makes builds reproducible across machines;
	// -buildvcs=true stamps the commit into the binary. Both must match the
	// Dockerfile and `task build` so every binary hashes identically from the
	// same source.
	build := Check{
		Name:  "go-build",
		Lane:  LaneChecks,
		Group: GroupStatic,
		Heavy: true,
		Argv:  []string{"go", "build", "-trimpath", "-buildvcs=true", "./cmd/..."},
	}
	// `go build` discards what it builds only for SEVERAL packages: with
	// one main package under cmd/ (the scaffold's shape) it writes the
	// binary into the project root, dirtying the tree the gate judges.
	if mainPackages(p.path("cmd")) == 1 {
		build.Argv = []string{"go", "build", "-trimpath", "-buildvcs=true", "-o", os.DevNull, "./cmd/..."}
	}
	if !dirExists(p.path("cmd")) {
		build.NotApplicable = "no cmd/ directory"
	}
	out = append(out, build)
	if ci.HasFrontends {
		for _, fe := range ci.Frontends {
			fe := fe
			out = append(out, Check{
				Name:   "build-" + fe.Name,
				Lane:   LaneChecks,
				Group:  GroupStatic,
				Heavy:  true,
				After:  []string{depsName(fe)},
				Prereq: p.needsDeps(fe),
				Argv:   p.feRun(fe, "build"),
				Dir:    fe.Path,
				Env:    []string{"NODE_ENV=production"},
			})
		}
	}

	// 5. Vulnerabilities and licenses.
	if ci.VulnGo {
		out = append(out, Check{
			Name:  "vuln-go",
			Lane:  LaneChecks,
			Group: GroupStatic,
			Heavy: true,
			// The advisory database changes daily: yesterday's clean scan of
			// this tree says nothing about today's.
			NoCache: true,
			Tools:   []Tool{govulncheck},
			Argv:    p.forge("ci", "vuln-scan", "--go"),
		})
	}
	if ci.VulnNPM && ci.HasFrontends {
		out = append(out, Check{
			Name:     "vuln-npm",
			Lane:     LaneChecks,
			Group:    GroupStatic,
			NoCache:  true,
			Requires: []string{"npm"},
			Argv:     p.forge("ci", "vuln-scan", "--npm"),
		})
	}
	if ci.LicenseCheck {
		out = append(out, Check{
			Name:  "license-check",
			Lane:  LaneChecks,
			Group: GroupStatic,
			Heavy: true,
			Tools: []Tool{goLicenses},
			Argv:  licenseCheckArgv(p.Root),
		})
	}

	// 6. Deployability, last: it is the one that renders every env's KCL.
	if ci.HasKCL {
		out = append(out,
			// Asserts the render is something `kubectl apply` accepts, not
			// merely that the KCL evaluates.
			Check{Name: "validate-kcl", Lane: LaneChecks, Group: GroupKCL, Heavy: true, Argv: p.forge("ci", "validate-kcl")},
			// Would an SRE let this reach production: probes, resource
			// requests/limits, credentials from Secrets, bound ServiceAccounts.
			Check{Name: "deployability", Lane: LaneChecks, Group: GroupKCL, Heavy: true, Argv: p.forge("doctor", "--signal", "deploy")},
		)
	}
	return out
}

func (p planner) testLane() []Check {
	out := p.frontendDeps(LaneTest)
	return append(out, Check{
		Name:  UnitTests,
		Lane:  LaneTest,
		Group: GroupTest,
		Heavy: true,
		// `task test` runs every frontend's suite too.
		After: p.allDeps(),
		// Task is the single definition of the suite: this row, the CI `test`
		// job and `task test:all` all run the same commands out of
		// Taskfile.yml.
		Requires: []string{"task"},
		Argv:     p.task("test"),
	})
}

func (p planner) integrationLane() []Check {
	c := Check{
		Name:     "integration-tests",
		Lane:     LaneIntegration,
		Group:    GroupIntegration,
		Heavy:    true,
		Requires: []string{"task"},
		Argv:     p.task("test:integration"),
	}
	if !HasIntegrationTests(p.Root) {
		c.NotApplicable = "no //go:build integration test files"
	}
	return []Check{c}
}

// task builds a `task <name>` argv, carrying forge.yaml's ci.test.race through
// to the Taskfile the way the workflow does.
func (p planner) task(name string) []string {
	argv := []string{"task", name}
	if !p.CI.TestRace {
		argv = append(argv, "GOTESTRACE=")
	}
	return argv
}

func (p planner) needsDeps(fe templates.FrontendCIConfig) func() string {
	return func() string {
		if dirExists(p.path(fe.Path, "node_modules")) {
			return ""
		}
		return fmt.Sprintf("%s/node_modules is missing — run %#q in %s (the deps-%s row does)", fe.Path, frontenddeps.InstallCommand(p.path(fe.Path), "", true), fe.Path, fe.Name)
	}
}

// UnitTests is the name of the unit-test row, which affected-package mode
// (affected.go) narrows on a branch.
const UnitTests = "unit-tests"

var laneRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// extras turns forge.yaml's ci.gate.extra_checks into checks: replacements
// for forge's own rows, keyed by the name they replace, and additions.
func (p planner) extras(owned []Check) (map[string]Check, []Check, error) {
	if p.Cfg == nil {
		return nil, nil, nil
	}
	ownedBy := map[string]Check{}
	for _, c := range owned {
		if _, ok := ownedBy[c.Name]; !ok {
			ownedBy[c.Name] = c
		}
	}
	replaced := map[string]Check{}
	taken := map[string]bool{}
	for name := range ownedBy {
		taken[name] = true
	}
	var added []Check
	for i, e := range p.Cfg.CI.Gate.ExtraChecks {
		where := fmt.Sprintf("ci.gate.extra_checks[%d]", i)
		c, err := p.extraCheck(where, e)
		if err != nil {
			return nil, nil, err
		}
		if e.Replaces != "" {
			orig, ok := ownedBy[e.Replaces]
			switch {
			case !ok:
				return nil, nil, fmt.Errorf("%s (%s): replaces %q, which is not one of forge's checks (`forge ci gate --list`)", where, e.Name, e.Replaces)
			case replaced[e.Replaces].Name != "":
				return nil, nil, fmt.Errorf("%s (%s): %q is already replaced by %q", where, e.Name, e.Replaces, replaced[e.Replaces].Name)
			}
			// The replacement takes the row: its lane, and its group unless it
			// names one, so CI_ONLY=<group> still reaches it.
			if e.Lane == "" {
				c.Lane = orig.Lane
			}
			if e.Group == "" {
				c.Group = orig.Group
			}
			delete(taken, e.Replaces)
			if taken[c.Name] {
				return nil, nil, fmt.Errorf("%s: %q is already a check name — pick another", where, c.Name)
			}
			taken[c.Name] = true
			replaced[e.Replaces] = c
			continue
		}
		if taken[c.Name] {
			return nil, nil, fmt.Errorf("%s: %q is already a check name — pick another, or set `replaces: %s` to take over forge's row (forge's own checks: `forge ci gate --list`)", where, c.Name, c.Name)
		}
		taken[c.Name] = true
		added = append(added, c)
	}
	return replaced, added, nil
}

// extraCheck validates one extra_checks entry and turns it into a Check.
func (p planner) extraCheck(where string, e config.CIGateCheck) (Check, error) {
	switch {
	case strings.TrimSpace(e.Name) == "":
		return Check{}, fmt.Errorf("%s: name is required (it is the row in the summary and what CI_SKIP matches)", where)
	case len(e.Command) == 0:
		return Check{}, fmt.Errorf("%s (%s): command is required", where, e.Name)
	case e.Lane != "" && (!laneRE.MatchString(e.Lane) || e.Lane == "all"):
		return Check{}, fmt.Errorf("%s (%s): lane %q — want checks, test, integration, or a lowercase name of your own (not \"all\")", where, e.Name, e.Lane)
	case e.Probe != nil && len(e.Probe.Command) == 0:
		return Check{}, fmt.Errorf("%s (%s): probe.command is required", where, e.Name)
	case e.SkipExitCode < 0 || e.SkipExitCode > 255:
		return Check{}, fmt.Errorf("%s (%s): skip_exit_code %d is not an exit status (1-255)", where, e.Name, e.SkipExitCode)
	}
	c := Check{
		Name:         e.Name,
		Lane:         LaneChecks,
		Group:        GroupExtras,
		After:        append([]string(nil), e.After...),
		Heavy:        e.Heavy,
		MutatesTree:  e.MutatesTree,
		NoCache:      e.Cache != nil && !*e.Cache,
		Requires:     append([]string(nil), e.Requires...),
		Argv:         append([]string(nil), e.Command...),
		Dir:          e.Dir,
		Env:          envList(e.Env),
		SkipExitCode: e.SkipExitCode,
	}
	if e.Lane != "" {
		c.Lane = Lane(e.Lane)
	}
	if e.Group != "" {
		c.Group = e.Group
	}
	if e.Probe != nil {
		c.Probe, c.ProbeReason = append([]string(nil), e.Probe.Command...), e.Probe.Reason
	}
	if e.MutatesTree {
		c.OnFail = p.resetIfClean(e.Name)
	}
	return c, nil
}

// envList renders an env map as sorted KEY=VALUE entries.
func envList(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}

// GateEnv is ci.gate.env as KEY=VALUE entries, for the base environment of
// every check.
func GateEnv(cfg *config.ProjectConfig) []string {
	if cfg == nil {
		return nil
	}
	return envList(cfg.CI.Gate.Env)
}

// GatePath is ci.gate.path made absolute against root.
func GatePath(root string, cfg *config.ProjectConfig) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	for _, d := range cfg.CI.Gate.Path {
		out = append(out, joinDir(root, d))
	}
	return out
}

var packageMain = regexp.MustCompile(`(?m)^package\s+main\b`)

// mainPackages counts the directories under dir holding a non-test .go file
// in package main.
func mainPackages(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if name := d.Name(); path != dir && (name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
			return filepath.SkipDir
		}
		entries, _ := os.ReadDir(path)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			if b, err := os.ReadFile(filepath.Join(path, e.Name())); err == nil && packageMain.Match(b) {
				n++
				break
			}
		}
		return nil
	})
	return n
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

var integrationTag = regexp.MustCompile(`(?m)^//go:build\s.*\bintegration\b`)

// HasIntegrationTests reports whether any Go test file under root carries the
// `integration` build tag. The scaffolded workflow asks the same question in
// its `changes` job (git grep for the same constraint) to decide whether the
// integration-test job runs at all.
func HasIntegrationTests(root string) bool {
	found := false
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return filepath.SkipDir
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", ".next", "vendor", ".forge":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// The constraint must precede the package clause, so the head of the
		// file is enough.
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		buf := make([]byte, 2048)
		n, _ := f.Read(buf)
		if integrationTag.Match(buf[:n]) {
			found = true
		}
		return nil
	})
	return found
}

// resetIfClean is a project tree-mutating check's OnFail: the same reset, but
// only when the tree was clean when the gate started — otherwise the
// "committed state" would throw away the developer's uncommitted work.
func (p planner) resetIfClean(name string) func(io.Writer) {
	clean := p.tree.wasClean() // now, at plan time: before anything has run
	return func(out io.Writer) {
		if !clean {
			fmt.Fprintf(out, "not resetting the tree after %s: it had uncommitted changes before the gate ran\n", name)
			return
		}
		resetTo(p.Root, name+" failing", out)
	}
}

func resetTo(root, why string, out io.Writer) {
	for _, argv := range [][]string{{"git", "checkout", "--", "."}, {"git", "clean", "-fd"}} {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = root
		if b, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(out, "could not reset the tree after %s (%s): %v\n%s", why, strings.Join(argv, " "), err, b)
			return
		}
	}
	fmt.Fprintf(out, "reset the working tree to the committed state after %s\n", why)
}

// treeSnapshot remembers whether the work tree was clean when the gate was
// planned.
type treeSnapshot struct {
	root  string
	once  sync.Once
	clean bool
}

func (t *treeSnapshot) wasClean() bool {
	t.once.Do(func() {
		cmd := exec.Command("git", "status", "--porcelain")
		cmd.Dir = t.root
		out, err := cmd.Output()
		t.clean = err == nil && strings.TrimSpace(string(out)) == ""
	})
	return t.clean
}
