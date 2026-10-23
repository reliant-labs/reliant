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
// CI's sharded Go lane uses two more commands (shard.go):
//
//	go list ./... | go run ./tools/test-affected shard   # the backend matrix, as JSON
//	gh run view <id> --log | go run ./tools/test-affected weights > tools/test-affected/go-test-weights.tsv
//
// and its new-flake gate one (changedtests.go):
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
)

func main() {
	args := os.Args[1:]
	cmd := "plan"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	// shard and weights serve CI's sharded Go lane (shard.go), not the
	// affected-tests plan.
	switch cmd {
	case "shard", "weights", "changed-tests":
		run := func() error { return runShard(args, os.Stdin, os.Stdout) }
		switch cmd {
		case "weights":
			run = func() error { return runWeights(os.Stdin, os.Stdout) }
		case "changed-tests":
			// CI's new-flake gate (changedtests.go).
			run = func() error { return runChangedTests(args, os.Stdout) }
		}
		if err := run(); err != nil {
			fmt.Fprintln(os.Stderr, "test-affected "+cmd+":", err)
			os.Exit(1)
		}
		return
	case "plan", "run":
	default:
		fmt.Fprintf(os.Stderr, "test-affected: unknown command %q (want plan, run, shard, weights or changed-tests)\n", cmd)
		os.Exit(2)
	}
	fs := newFlagSet(cmd)
	root := fs.String("root", ".", "repository root")
	baseFlag := fs.String("base", "", "base commit-ish (default: the merge-base with origin/main, or $TEST_BASE)")
	asJSON := fs.Bool("json", false, "print the plan as JSON instead of the table")
	full := fs.Bool("full", false, "run everything (also TEST_FULL=1)")
	lanes := fs.String("lanes", "go,web,electron,forge-generate,docs-generate", "comma-separated lanes to consider")
	files := fs.String("files", "", "comma-separated paths (or @file, one per line) to treat as the change set instead of the git diff (what-if)")
	dry := fs.Bool("dry-run", false, "with run: print the commands instead of executing them")
	gotestP := fs.Int("p", 0, "go test -p (default: the machine's parallelism)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if *baseFlag == "" {
		*baseFlag = os.Getenv("TEST_BASE")
	}
	if os.Getenv("TEST_FULL") == "1" {
		*full = true
	}

	ctx := context.Background()
	pl, err := plan(ctx, *root, *baseFlag, *full, splitList(*files))
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
		r := &runner{root: mustAbs(*root), out: os.Stdout, errOut: os.Stderr, dry: *dry, goP: *gotestP,
			slot: strings.Fields(os.Getenv("TEST_AFFECTED_SLOT"))}
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
func plan(ctx context.Context, root, baseFlag string, forceFull bool, whatIf []string) (*Plan, error) {
	root = mustAbs(root)
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
	pl := Classify(changes, g)
	pl.Base, pl.Head = base, head
	return pl, nil
}

func failSafe(base, head, why string) *Plan {
	lf := LaneFull{Full: true, Reasons: []string{why}}
	return &Plan{Base: base, Head: head, Go: GoPlan{LaneFull: lf}, Web: WebPlan{LaneFull: lf}, Electron: ElectronPlan{LaneFull: lf}, ForgeGen: ForgeGenPlan{LaneFull: lf}, DocGen: DocGenPlan{LaneFull: lf}}
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
		{laneGo, pl.Go.LaneFull, fmt.Sprintf("%d package(s)", len(pl.Go.Packages)), pl.Go.Packages, pl.Go.Why},
		{laneWeb, pl.Web.LaneFull, fmt.Sprintf("%d vitest file(s)", len(pl.Web.Tests)), pl.Web.Tests, pl.Web.Why},
		{laneElectron, pl.Electron.LaneFull, "electron tests", nil, nil},
		{laneForgeGen, pl.ForgeGen.LaneFull, "forge generate", nil, nil},
		{laneDocGen, pl.DocGen.LaneFull, "docs generators", nil, nil},
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
		case len(lp.list) == 0 && (lp.l == laneElectron || lp.l == laneForgeGen || lp.l == laneDocGen):
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
			p.f("\n[npm-lockfile] %s: npm ci --dry-run (%s)\n", lc.Dir, lc.Why)
		}
	}
	return p.err
}

// ── runner ──────────────────────────────────────────────────────────────────

type runner struct {
	root   string
	out    io.Writer
	errOut io.Writer
	dry    bool
	goP    int
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

type step struct {
	name string
	dir  string
	env  []string
	argv []string
}

// lockfileArgv proves a project's lockfile still installs without touching
// its node_modules: `npm ci` validates package.json against the lockfile
// before it reifies anything, and --dry-run stops it there (~10s, offline for
// a lockfile in sync).
var lockfileArgv = []string{"npm", "ci", "--dry-run", "--ignore-scripts", "--no-audit", "--no-fund", "--loglevel=error"}

func (r *runner) steps(pl *Plan, enabled map[lane]bool) []step {
	var out []step
	// First: the cheapest step, and every test after it would otherwise pass
	// against a node_modules a fresh install can no longer produce.
	for _, lc := range pl.Lockfiles {
		if slices.ContainsFunc(lc.Lanes, func(l lane) bool { return enabled[l] }) {
			out = append(out, step{name: "npm lockfile in sync (" + lc.Dir + ")", dir: filepath.Join(r.root, lc.Dir), argv: lockfileArgv})
		}
	}
	// First, so the tests below run against what generate leaves behind. It
	// writes what it regenerates into the tree, naming every file it changed.
	if enabled[laneForgeGen] && pl.ForgeGen.Full {
		out = append(out, step{name: "forge generate is a no-op", dir: r.root, argv: []string{"task", "generate:check"}})
	}
	// Likewise: the docs generators write into the tree (one of them a Go
	// file). They compile most of the module, so the step takes a heavy slot.
	if enabled[laneDocGen] && pl.DocGen.Full {
		out = append(out, step{name: "docs generators are a no-op", dir: r.root, argv: r.heavy([]string{"task", "generate:docs:check"})})
	}
	if enabled[laneGo] {
		out = append(out, r.goSteps(pl)...)
	}
	if enabled[laneElectron] && pl.Electron.Full {
		out = append(out, step{name: "electron", dir: filepath.Join(r.root, "electron"), argv: []string{"npm", "test", "--silent"}})
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
	return []step{{name: "go", dir: r.root, argv: append(append([]string{}, base...), pl.Go.Packages...)}}
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
	fmt.Fprintln(r.out)
	failed := 0
	for _, s := range steps {
		fmt.Fprintf(r.out, "==> %s: %s\n", s.name, summarize(s.argv))
		if r.dry {
			continue
		}
		start := time.Now()
		c := exec.CommandContext(ctx, s.argv[0], s.argv[1:]...)
		c.Dir = s.dir
		c.Env = append(os.Environ(), s.env...)
		c.Stdout, c.Stderr = r.out, r.errOut
		err := c.Run()
		status := "ok"
		if err != nil {
			status = "FAILED: " + err.Error()
			failed++
		}
		fmt.Fprintf(r.out, "<== %s: %s (%s)\n", s.name, status, time.Since(start).Round(time.Second))
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func summarize(argv []string) string {
	const max = 12
	if len(argv) <= max {
		return strings.Join(argv, " ")
	}
	return strings.Join(argv[:max], " ") + fmt.Sprintf(" ... (+%d more)", len(argv)-max)
}
