// Command test-affected runs only the tests a diff can reach.
//
// It diffs the working tree against the merge-base with origin/main (committed,
// staged, unstaged and untracked), maps every changed path to the tests that
// can observe it, prints why each runs, and executes them:
//
//	go run ./tools/test-affected            # print the plan, run nothing
//	go run ./tools/test-affected run        # print the plan, then run it
//	go run ./tools/test-affected run -lanes go,web
//	go run ./tools/test-affected -base origin/main~3 -json
//
// The new-flake check (scripts/test-flake.sh, `task test:flake`) uses one
// more (changedtests.go):
//
//	go run ./tools/test-affected changed-tests   # ./pkg<TAB>^(TestA|TestB)$
//
// `task test:affected` is the front door. The decision is Classify (plan.go)
// over rules.go, the Go package graph (graph.go) and the web import graph
// (webgraph.go); this file is the I/O. A path no rule and no Go package claims,
// a dependency or config pin, or a failure to compute the diff makes the
// answer "run everything" for the lanes it touches.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func main() {
	args := os.Args[1:]
	cmd := "plan"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "changed-tests":
		// The new-flake check (changedtests.go).
		if err := runChangedTests(args, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "test-affected "+cmd+":", err)
			os.Exit(1)
		}
		return
	case "migration-order":
		os.Exit(runMigrationOrder(args, os.Stdout, os.Stderr))
	case "gate-verify":
		os.Exit(runGateVerify(args, os.Stdout, os.Stderr))
	case "gate-report":
		os.Exit(runGateReport(args, os.Stdout, os.Stderr))
	case "plan", "run":
	default:
		fmt.Fprintf(os.Stderr, "test-affected: unknown command %q (want plan, run, migration-order or changed-tests)\n", cmd)
		os.Exit(2)
	}
	fs := newFlagSet(cmd)
	root := fs.String("root", ".", "repository root")
	baseFlag := fs.String("base", "", "base commit-ish (default: the merge-base with origin/main, or $TEST_BASE)")
	asJSON := fs.Bool("json", false, "print the plan as JSON instead of the table")
	full := fs.Bool("full", false, "run everything (also TEST_FULL=1)")
	lanes := fs.String("lanes", defaultLanes, "comma-separated lanes to consider")
	files := fs.String("files", "", "comma-separated paths (or @file, one per line) to treat as the change set instead of the git diff (what-if)")
	dry := fs.Bool("dry-run", false, "with run: print the commands instead of executing them")
	gotestP := fs.Int("p", 0, "go test -p (default: the machine's parallelism)")
	jobs := fs.Int("jobs", 2, "maximum concurrent test steps (1 serializes; prerequisites always run first)")
	planFile := fs.String("plan", "", "with run: run this plan (the -json output of an earlier plan) instead of computing one; the merge gate's plan phase already wrote it")
	budgetFlag := fs.String("test-budget", "", "with run: fail a Go test this change adds or edits that runs longer than this (a duration; \"auto\" is 10s scaled by the load per core; empty or 0 is off), and report every other test over it")
	reachFlag := fs.String("reach", "", "Go and web lane fan-out: all (every package whose test binary, every vitest file whose import closure, contains a change; the default, also $TEST_REACH) or direct (changed packages + their direct importers; the vitest files that import a changed module or a module importing it: the merge tier)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *jobs < 1 {
		fmt.Fprintln(os.Stderr, "test-affected: -jobs must be positive")
		os.Exit(2)
	}
	if *baseFlag == "" {
		*baseFlag = os.Getenv("TEST_BASE")
	}
	if os.Getenv("TEST_FULL") == "1" {
		*full = true
	}
	if *reachFlag == "" {
		*reachFlag = os.Getenv("TEST_REACH")
	}
	switch *reachFlag {
	case "", ReachAll:
		*reachFlag = ReachAll
	case ReachDirect:
	default:
		fmt.Fprintf(os.Stderr, "test-affected: -reach must be %s or %s, not %q\n", ReachAll, ReachDirect, *reachFlag)
		os.Exit(2)
	}

	ctx := context.Background()
	var pl *Plan
	var err error
	if *planFile != "" {
		pl, err = readPlan(ctx, *root, *baseFlag, *planFile)
	} else {
		pl, err = plan(ctx, *root, *baseFlag, *full, splitList(*files), *reachFlag)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "test-affected:", err)
		os.Exit(1)
	}
	enabled := laneSet(*lanes)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(pl)
	} else if err := printTable(os.Stdout, pl, enabled); err != nil {
		fmt.Fprintln(os.Stderr, "test-affected:", err)
		os.Exit(1)
	}
	if cmd == "run" {
		r := &runner{root: mustAbs(*root), out: os.Stdout, errOut: os.Stderr, dry: *dry, goP: *gotestP, jobs: *jobs,
			slot: strings.Fields(os.Getenv("TEST_AFFECTED_SLOT"))}
		if err := r.setBudget(ctx, *budgetFlag, pl.Base); err != nil {
			fmt.Fprintln(os.Stderr, "test-affected:", err)
			os.Exit(2)
		}
		os.Exit(r.run(ctx, pl, enabled))
	}
}

func newFlagSet(cmd string) *flag.FlagSet {
	return flag.NewFlagSet(cmd, flag.ExitOnError)
}

func splitList(s string) []string {
	var out []string
	if file, ok := strings.CutPrefix(s, "@"); ok {
		b, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintln(os.Stderr, "test-affected:", err)
			os.Exit(2)
		}
		s = strings.ReplaceAll(string(b), "\n", ",")
	}
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

func laneSet(s string) map[lane]bool {
	out := map[lane]bool{}
	for _, l := range strings.Split(s, ",") {
		if l = strings.TrimSpace(l); l != "" {
			out[lane(l)] = true
		}
	}
	return out
}

// plan computes the decision for the tree at root.
func plan(ctx context.Context, root, baseFlag string, forceFull bool, whatIf []string, reachMode string) (result *Plan, resultErr error) {
	root = mustAbs(root)
	before, err := checkoutDigest(ctx, root)
	if err != nil {
		return nil, err
	}
	defer func() {
		// What-if decisions are not executable evidence of this checkout.
		if resultErr == nil && result != nil && len(whatIf) == 0 {
			result.Schema = planSchema
			result.InputDigest, resultErr = checkoutDigest(ctx, root)
			if resultErr == nil && result.InputDigest != before {
				resultErr = errors.New("checkout changed while planning; replan")
			}
		}
	}()
	module, err := moduleOf(root)
	if err != nil {
		return nil, err
	}
	changes, base, head, derr := diff(ctx, root, baseFlag)
	if len(whatIf) > 0 {
		changes, derr = nil, nil
		for _, f := range whatIf {
			changes = append(changes, Change{Path: f})
		}
	}
	switch {
	case forceFull:
		return failSafe(base, head, "TEST_FULL=1 / -full"), nil
	case derr != nil:
		return failSafe(base, head, "cannot compute the diff ("+derr.Error()+")"), nil
	}
	tracked, untracked, terr := repoFiles(ctx, root, base)
	if terr != nil {
		return failSafe(base, head, "cannot list repo files ("+terr.Error()+")"), nil
	}
	g, gerr := LoadGraph(ctx, root, module, tracked)
	if gerr != nil {
		return failSafe(base, head, "cannot load the Go package graph ("+firstLine(gerr.Error())+")"), nil
	}
	g.Web = BuildWebGraph(append(append([]string{}, tracked...), untracked...), diskReader(root))
	g.ElectronText = electronTexts(root)
	pl := ClassifyReach(changes, g, reachMode)
	pl.Base, pl.Head = base, head
	if pl.ForgeGen.HooksOnly {
		cmds, err := hookCommands(root, pl.ForgeGen.Hooks)
		if err != nil {
			// Without the commands nobody can run hooks alone: the whole
			// forge generate it is, as before.
			pl.ForgeGen.HooksOnly, pl.ForgeGen.Hooks = false, nil
		} else {
			pl.ForgeGen.HookCommands = cmds
		}
	}
	return pl, nil
}

// readPlan reads a plan `plan -json` wrote. Planning loads the whole module's
// package graph (`go list -test -json ./...`: 17-141s in the merge queue on a
// loaded host), so a gate that has planned once runs that plan.
func readPlan(ctx context.Context, root, base, file string) (*Plan, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	// plan's output may follow lines a wrapper printed: start at the JSON.
	if i := bytes.IndexByte(b, '{'); i > 0 {
		b = b[i:]
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	for _, key := range []string{"schema", "input_digest", "base", "head", "files", "go", "web", "electron", "forge_generate", "docs_generate", "migration_order"} {
		value, ok := fields[key]
		if !ok || (key != "files" && bytes.Equal(value, []byte("null"))) {
			return nil, fmt.Errorf("%s: missing plan field %s", file, key)
		}
	}
	for _, key := range []string{"go", "web", "electron", "forge_generate", "docs_generate", "migration_order"} {
		var laneFields map[string]json.RawMessage
		if err := json.Unmarshal(fields[key], &laneFields); err != nil {
			return nil, err
		}
		var full bool
		value, ok := laneFields["full"]
		if !ok || bytes.Equal(value, []byte("null")) {
			return nil, fmt.Errorf("%s: missing %s.full", file, key)
		}
		if err := json.Unmarshal(value, &full); err != nil {
			return nil, err
		}
	}
	var pl Plan
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pl); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("%s: trailing plan data", file)
	}
	if err := validatePlanIdentity(ctx, root, base, &pl); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return &pl, nil
}

// hookCommands reads each named hook's command from forge.yaml's
// generate.hooks.
func hookCommands(root string, names []string) (map[string][]string, error) {
	b, err := os.ReadFile(filepath.Join(root, "forge.yaml"))
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Generate struct {
			Hooks []struct {
				Name    string   `yaml:"name"`
				Command []string `yaml:"command"`
			} `yaml:"hooks"`
		} `yaml:"generate"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, h := range cfg.Generate.Hooks {
		if slices.Contains(names, h.Name) && len(h.Command) > 0 {
			out[h.Name] = h.Command
		}
	}
	for _, n := range names {
		if _, ok := out[n]; !ok {
			return nil, fmt.Errorf("forge.yaml has no generate hook %q", n)
		}
	}
	return out, nil
}

func failSafe(base, head, why string) *Plan {
	lf := LaneFull{Full: true, Reasons: []string{why}}
	return &Plan{Base: base, Head: head, Go: GoPlan{LaneFull: lf}, Web: WebPlan{LaneFull: lf}, Electron: ElectronPlan{LaneFull: lf}, ForgeGen: ForgeGenPlan{LaneFull: lf}, DocGen: DocGenPlan{LaneFull: lf}, MigOrder: MigOrderPlan{LaneFull: lf}}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func moduleOf(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if m, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(m), nil
		}
	}
	return "", errors.New("go.mod declares no module")
}

// electronTexts reads electron/test/*.test.js: the tests that may name files
// outside electron/ (a workflow, a Go source) a change elsewhere can break.
func electronTexts(root string) map[string]string {
	out := map[string]string{}
	matches, _ := filepath.Glob(filepath.Join(root, "electron", "test", "*.test.js"))
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(root, m)
		if err != nil {
			continue
		}
		out[filepath.ToSlash(rel)] = string(b)
	}
	return out
}

func git(ctx context.Context, root string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = root
	b, err := c.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(b), nil
}

// diff returns what changed between the base and the working tree (committed,
// staged, unstaged and untracked); a rename is its delete plus its add.
func diff(ctx context.Context, root, baseFlag string) (changes []Change, base, head string, err error) {
	headOut, herr := git(ctx, root, "rev-parse", "HEAD")
	if herr != nil {
		return nil, "", "", herr
	}
	head = strings.TrimSpace(headOut)
	base = strings.TrimSpace(baseFlag)
	if base == "" {
		mb, merr := git(ctx, root, "merge-base", "HEAD", "origin/main")
		if merr != nil {
			return nil, "", head, errors.New("no origin/main to diff against; set TEST_BASE or run git fetch")
		}
		base = strings.TrimSpace(mb)
	} else if rev, rerr := git(ctx, root, "rev-parse", "--verify", base+"^{commit}"); rerr == nil {
		base = strings.TrimSpace(rev)
	} else {
		return nil, base, head, rerr
	}
	names, err := git(ctx, root, "diff", "--name-status", "--no-renames", "-z", base)
	if err != nil {
		return nil, base, head, err
	}
	changes = parseNameStatus(names)
	untracked, err := git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, base, head, err
	}
	seen := map[string]bool{}
	for _, c := range changes {
		seen[c.Path] = true
	}
	for _, p := range strings.Split(untracked, "\x00") {
		// A trailing slash is a nested repository: another checkout living in
		// this one, not part of this tree.
		if p == "" || strings.HasSuffix(p, "/") || seen[p] {
			continue
		}
		changes = append(changes, Change{Path: p})
	}
	return changes, base, head, nil
}

func parseNameStatus(z string) []Change {
	parts := strings.Split(z, "\x00")
	var out []Change
	for i := 0; i+1 < len(parts); i += 2 {
		status, p := parts[i], parts[i+1]
		if status == "" || p == "" {
			continue
		}
		out = append(out, Change{Path: p, Deleted: strings.HasPrefix(status, "D")})
	}
	return out
}

// repoFiles returns every path in HEAD and in the base (a literal a test reads
// may name a file this diff deletes) and the untracked ones.
func repoFiles(ctx context.Context, root, base string) (tracked, untracked []string, err error) {
	cur, err := git(ctx, root, "ls-files", "-z")
	if err != nil {
		return nil, nil, err
	}
	old, err := git(ctx, root, "ls-tree", "-r", "-z", "--name-only", base)
	if err != nil {
		return nil, nil, err
	}
	un, err := git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, blob := range []string{cur, old} {
		for _, p := range strings.Split(blob, "\x00") {
			if p != "" && !seen[p] {
				seen[p] = true
				tracked = append(tracked, p)
			}
		}
	}
	for _, p := range strings.Split(un, "\x00") {
		if p != "" && !strings.HasSuffix(p, "/") && !seen[p] {
			seen[p] = true
			untracked = append(untracked, p)
		}
	}
	return tracked, untracked, nil
}

// ── output ──────────────────────────────────────────────────────────────────

type printer struct {
	w   io.Writer
	err error
}

func (p *printer) f(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// goLine heads the Go lane: a -reach direct plan says how many transitive
// dependents it left to the full lane.
func goLine(gp GoPlan) string {
	if gp.Reach == ReachDirect {
		return fmt.Sprintf("%d package(s), reach direct (%d transitive dependent(s) left to the full lane)", len(gp.Packages), len(gp.Deferred))
	}
	return fmt.Sprintf("%d package(s)", len(gp.Packages))
}

// webLine heads the web lane, like goLine.
func webLine(wp WebPlan) string {
	if wp.Reach == ReachDirect {
		return fmt.Sprintf("%d vitest file(s), reach direct, %d import edge(s) (%d further importer test(s) left to the full lane)", len(wp.Tests), wp.Edges, len(wp.Deferred))
	}
	return fmt.Sprintf("%d vitest file(s)", len(wp.Tests))
}

func printTable(w io.Writer, pl *Plan, enabled map[lane]bool) error {
	p := &printer{w: w}
	p.f("== test-affected: base %s -> %s (%d changed path(s))\n", short(pl.Base), short(pl.Head), len(pl.Files))
	for i, f := range pl.Files {
		if i == 30 {
			p.f("  ... and %d more\n", len(pl.Files)-30)
			break
		}
		del := ""
		if f.Deleted {
			del = " (deleted)"
		}
		rules := "-"
		if len(f.Rules) > 0 {
			rules = strings.Join(f.Rules, ",")
		}
		p.f("  %s%s  [%s]  %s\n", f.Path, del, rules, f.Note)
	}
	lanePlans := []struct {
		l    lane
		lf   LaneFull
		line string
		list []string
		why  map[string]string
	}{
		{laneGo, pl.Go.LaneFull, goLine(pl.Go), pl.Go.Packages, pl.Go.Why},
		{laneWeb, pl.Web.LaneFull, webLine(pl.Web), pl.Web.Tests, pl.Web.Why},
		{laneElectron, pl.Electron.LaneFull, "electron tests", nil, nil},
		{laneForgeGen, pl.ForgeGen.LaneFull, "forge generate", nil, nil},
		{laneDocGen, pl.DocGen.LaneFull, "docs generators", nil, nil},
		{laneMigOrder, pl.MigOrder.LaneFull, "migration order", nil, nil},
	}
	for _, lp := range lanePlans {
		if !enabled[lp.l] {
			continue
		}
		switch {
		case lp.lf.Full:
			p.f("\n[%s] FULL. Why:\n", lp.l)
			for i, r := range lp.lf.Reasons {
				if i == 5 {
					p.f("  - ... and %d more\n", len(lp.lf.Reasons)-5)
					break
				}
				p.f("  - %s\n", r)
			}
		case len(lp.list) == 0 && (lp.l == laneElectron || lp.l == laneForgeGen || lp.l == laneDocGen || lp.l == laneMigOrder):
			p.f("\n[%s] skip: nothing it reads changed\n", lp.l)
		case len(lp.list) == 0:
			p.f("\n[%s] skip: no test reaches a changed file\n", lp.l)
		default:
			p.f("\n[%s] %s:\n", lp.l, lp.line)
			for i, t := range lp.list {
				if i == 25 {
					p.f("  ... and %d more (-json lists them all)\n", len(lp.list)-25)
					break
				}
				p.f("  %s  <- %s\n", t, lp.why[t])
			}
		}
	}
	for _, lc := range pl.Lockfiles {
		if slices.ContainsFunc(lc.Lanes, func(l lane) bool { return enabled[l] }) {
			p.f("\n[npm-lockfile] %s: %s (%s)\n", lc.Dir, strings.Join(lockfileArgv(lc.Lockfile), " "), lc.Why)
		}
	}
	return p.err
}

// ── runner ──────────────────────────────────────────────────────────────────

type runner struct {
	root    string
	out     io.Writer
	errOut  io.Writer
	dry     bool
	goP     int
	jobs    int
	execute func(context.Context, step, io.Writer, io.Writer) error
	// budget is the per-test time budget of the Go step (budget.go); 0 is
	// none. owned are the tests the diff adds or edits, which it fails.
	budget time.Duration
	owned  map[string]bool
	// slot prefixes a FULL lane's command (Taskfile.yml passes
	// TEST_AFFECTED_SLOT='forge slot run --class heavy --'): a whole-module
	// run is as heavy as any gate check, and outside a host slot it only steals
	// CPU from the jobs queued in one. A scoped run stays out of the queue: it
	// is the inner loop. Empty runs everything directly.
	slot []string
}

// heavy is argv run inside a host heavy slot when there is one.
func (r *runner) heavy(argv []string) []string {
	return append(append([]string{}, r.slot...), argv...)
}

// setBudget reads -test-budget: a duration, "auto" (defaultTestBudget), or
// empty / 0 for none.
func (r *runner) setBudget(ctx context.Context, spec, base string) error {
	switch spec {
	case "", "0":
		return nil
	case "auto":
		r.budget = defaultTestBudget()
	default:
		d, err := time.ParseDuration(spec)
		if err != nil || d < 0 {
			return fmt.Errorf("-test-budget %q: want a duration, auto, or 0", spec)
		}
		r.budget = d
	}
	module, err := moduleOf(r.root)
	if err != nil {
		return err
	}
	pkgs, err := changedTests(ctx, r.root, base)
	if err != nil {
		// Without the list every over-budget test is reported, none failed.
		fmt.Fprintln(r.errOut, "test-affected: cannot list the tests this change edits ("+firstLine(err.Error())+"); over-budget tests are reported, not failed")
	}
	r.owned = ownedTests(module, pkgs)
	return nil
}

type step struct {
	name         string
	dir          string
	env          []string
	argv         []string
	prerequisite bool
	// exclusive tests temporarily rewrite repository inputs used by other lanes.
	exclusive bool
	// budget, when set, times the step's `go test -json` output (budget.go).
	budget *budgetFilter
}

// lockfileArgv proves a project's lockfile still installs without touching
// its node_modules: `npm ci` validates package.json against the lockfile
// before it reifies anything, and --dry-run stops it there (~10s, offline for
// a lockfile in sync). pnpm's --frozen-lockfile --lockfile-only asks the same
// question and writes nothing (~3s).
func lockfileArgv(lockfile string) []string {
	if lockfile == pnpmLock {
		return []string{"pnpm", "install", "--frozen-lockfile", "--lockfile-only", "--ignore-scripts"}
	}
	return []string{"npm", "ci", "--dry-run", "--ignore-scripts", "--no-audit", "--no-fund", "--loglevel=error"}
}

func (r *runner) steps(pl *Plan, enabled map[lane]bool) []step {
	var out []step
	// First: the cheapest step, and every test after it would otherwise pass
	// against a node_modules a fresh install can no longer produce.
	for _, lc := range pl.Lockfiles {
		if slices.ContainsFunc(lc.Lanes, func(l lane) bool { return enabled[l] }) {
			out = append(out, step{name: "npm lockfile in sync (" + lc.Dir + ")", dir: filepath.Join(r.root, lc.Dir), argv: lockfileArgv(lc.Lockfile), prerequisite: true})
		}
	}
	// Cheapest of all (a fetch and a directory listing) and the one that
	// matters most before a merge: a migration below main's newest is never
	// applied, and no test notices.
	if enabled[laneMigOrder] && pl.MigOrder.Full {
		out = append(out, step{name: "migration order is above origin/main's newest", dir: r.root, argv: []string{"go", "run", "./tools/test-affected", "migration-order"}, prerequisite: true})
	}
	// First, so the tests below run against what generate leaves behind. It
	// writes what it regenerates into the tree, naming every file it changed.
	if enabled[laneForgeGen] && pl.ForgeGen.Full {
		out = append(out, step{name: "forge generate is a no-op", dir: r.root, argv: []string{"task", "generate:check"}, prerequisite: true})
	}
	// Likewise: the docs generators write into the tree (one of them a Go
	// file). They compile most of the module, so the step takes a heavy slot.
	if enabled[laneDocGen] && pl.DocGen.Full {
		out = append(out, step{name: "docs generators are a no-op", dir: r.root, argv: r.heavy([]string{"task", "generate:docs:check"}), prerequisite: true})
	}
	if enabled[laneGo] {
		out = append(out, r.goSteps(pl)...)
	}
	if enabled[laneElectron] && pl.Electron.Full {
		out = append(out, step{name: "electron", dir: filepath.Join(r.root, "electron"), argv: []string{"npm", "test", "--silent"}, exclusive: true})
	}
	if enabled[laneWeb] {
		out = append(out, r.webSteps(pl)...)
	}
	return out
}

func (r *runner) goSteps(pl *Plan) []step {
	// -timeout is per package binary and matches task test:short's (5m): the
	// DB-backed packages run under -short too (sized in Taskfile.yml).
	base := []string{"go", "test", "-buildvcs=false", "-short", "-timeout", "5m"}
	if r.goP > 0 {
		base = append(base, "-p", fmt.Sprint(r.goP))
	}
	switch {
	case pl.Go.Full:
		return []step{{name: "go (full)", dir: r.root, argv: r.heavy(append(append([]string{}, base...), "./..."))}}
	case len(pl.Go.Packages) == 0:
		return nil
	}
	s := step{name: "go", dir: r.root, argv: append(append([]string{}, base...), pl.Go.Packages...)}
	if r.budget > 0 {
		// -json right after "test": the filter turns it back into plain
		// go test output and times each test.
		s.argv = append([]string{"go", "test", "-json"}, s.argv[2:]...)
		s.budget = newBudgetFilter(r.budget, r.owned, nil)
	}
	return []step{s}
}

func (r *runner) webSteps(pl *Plan) []step {
	web := filepath.Join(r.root, "frontends", "web")
	switch {
	case pl.Web.Full:
		return []step{{name: "web (full)", dir: web, argv: r.heavy([]string{"npx", "vitest", "run"})}}
	case len(pl.Web.Tests) == 0:
		return nil
	}
	return []step{{name: "web", dir: web, argv: append([]string{"npx", "vitest", "run"}, pl.Web.Tests...)}}
}

func (r *runner) run(ctx context.Context, pl *Plan, enabled map[lane]bool) int {
	steps := r.steps(pl, enabled)
	if len(steps) == 0 {
		fmt.Fprintln(r.out, "\ntest-affected: nothing to run.")
		return 0
	}
	return r.runSteps(ctx, steps)
}

func summarize(argv []string) string {
	const max = 12
	if len(argv) <= max {
		return strings.Join(argv, " ")
	}
	return strings.Join(argv[:max], " ") + fmt.Sprintf(" ... (+%d more)", len(argv)-max)
}
