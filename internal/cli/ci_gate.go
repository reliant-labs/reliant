package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cigate"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/hostslot"
)

// newCIGateCmd builds `forge ci gate`: the local CI gate. A scaffolded
// workflow is a thin wrapper that calls `task ci:checks`, `task test` and
// `task test:integration`; `task ci` runs all three on a laptop. The step list
// and the tool pins live here, in forge, so every project gets the same gate
// and a fix to it reaches every project on the next forge bump — instead of
// being a script each project copied once and owns forever. A project adds
// checks under forge.yaml `ci.gate.extra_checks`. See package cigate for the
// four rules every run obeys and how checks are scheduled.
func newCIGateCmd() *cobra.Command {
	var flags gateFlags
	cmd := &cobra.Command{
		Use:   "gate [checks|test|integration|all|<lane>]",
		Short: "Run the local CI gate: every check, one summary table, non-zero if any failed or was skipped",
		Long: "Runs the project's CI checks locally, exactly as the scaffolded workflow does.\n\n" +
			"Lanes (default: all):\n" +
			"  checks       verify-generated, lint, build, vuln-scan, license-check, KCL validation,\n" +
			"               deployability, and any ci.gate.extra_checks from forge.yaml   (`task ci:checks`)\n" +
			"  test         the unit suite, via `task test`\n" +
			"  integration  the integration suite, via `task test:integration`\n" +
			"  all          the three lanes, in one table                                   (`task ci`)\n" +
			"  <lane>       a lane extra checks of your own declare (`lane: docker`); never part of all\n\n" +
			"Four rules:\n" +
			"  1. Every check runs even if another failed; a summary table follows and the exit\n" +
			"     status is non-zero if any failed.\n" +
			"  2. A check that could not run (missing tool or prerequisite) is SKIPPED, not PASS,\n" +
			"     and fails the gate unless CI_SKIP names it: CI_SKIP=golangci-lint,license-check.\n" +
			"     The summary still lists it. N/A rows (nothing for the check to look at) are fine.\n" +
			"  3. Pinned tools (golangci-lint, buf, govulncheck, go-licenses) run at the versions the\n" +
			"     scaffolded workflow installs, from forge's tool cache, never from a different\n" +
			"     version that happens to be on PATH.\n" +
			"  4. Under GitHub Actions the table is appended to $GITHUB_STEP_SUMMARY.\n\n" +
			"Checks run concurrently. A check waits for the checks it names in `after`; a check that\n" +
			"rewrites the tree (an extra with `mutates_tree`) runs alone, before the checks that read\n" +
			"it (verify-generated regenerates a scratch copy, so it is not one); a heavy check holds\n" +
			"one of the machine's heavy slots (`forge slot status`), and at most --jobs light checks\n" +
			"run at once. Each check's output goes to\n" +
			"tmp/ci/<lane>/<check>.log; the console shows start and finish lines and a failure's tail.\n\n" +
			"On a committed tree, a check that PASSED on this exact tree before (any worktree of the\n" +
			"repository, same forge and pinned tools) is not run again: its row reads\n" +
			"`PASS (cached <sha>)`. CI_NO_CACHE=1 or --no-cache runs everything.\n\n" +
			"On a branch, unit-tests runs only the Go packages the branch's changes reach (affected\n" +
			"mode), and falls back to the whole suite when anything but Go source changed, on the\n" +
			"default branch, or with CI_FULL=1 / --full. The mode and why are printed.\n\n" +
			"Environment: CI_SKIP, CI_ONLY (checks or groups to run, with what they depend on),\n" +
			"CI_FULL, CI_BASE, CI_NO_CACHE, CI_REPORT (also write the Markdown table to a file),\n" +
			"CI_LOG_DIR, FORGE_CI_CACHE_DIR.",
		Example: "  forge ci gate                       # the whole gate\n" +
			"  forge ci gate checks                # static + build lane only\n" +
			"  forge ci gate --list                # every check, its lane, group and scheduling\n" +
			"  CI_ONLY=kcl forge ci gate checks    # one group (and whatever it depends on)\n" +
			"  CI_SKIP=vuln-go forge ci gate       # offline: allow one skip, still listed\n" +
			"  CI_FULL=1 forge ci gate test        # the whole unit suite on a branch",
		Args:         cobra.MaximumNArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCIGate(cmd, args, flags)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&flags.list, "list", false, "Print the checks of the selected lanes (lane, group, name, scheduling) and exit")
	f.StringSliceVar(&flags.skip, "skip", nil, "Check names to skip, in addition to CI_SKIP (still listed, as SKIPPED)")
	f.StringSliceVar(&flags.only, "only", nil, "Checks or groups to run, in addition to CI_ONLY (plus what they depend on)")
	f.BoolVar(&flags.groups, "groups", false, "Print each check's output in a GitHub Actions ::group:: (automatic under GITHUB_ACTIONS)")
	f.IntVar(&flags.jobs, "jobs", 0, "Light checks to run at once (default: half the CPUs, 2-8); heavy checks are bounded by host slots")
	f.BoolVar(&flags.noCache, "no-cache", false, "Run every check even when this tree already passed it (also CI_NO_CACHE=1)")
	f.BoolVar(&flags.full, "full", false, "Run the whole unit suite on a branch instead of the affected packages (also CI_FULL=1)")
	f.StringVar(&flags.report, "report", "", "Also write the Markdown summary to this file (also CI_REPORT)")
	f.StringVar(&flags.base, "base", "", "Ref a branch's changes are measured from for affected unit tests (default origin/<default branch>; a stacked branch names its parent; also CI_BASE)")
	return cmd
}

type gateFlags struct {
	list, groups, noCache, full bool
	skip, only                  []string
	jobs                        int
	report, base                string
}

func runCIGate(cmd *cobra.Command, args []string, flags gateFlags) error {
	out, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	root, err := projectRoot()
	if err != nil {
		return err
	}
	store, err := loadProjectStore()
	if err != nil {
		return err
	}
	cfg := store.Config()
	forge, err := forgeExecCommand()
	if err != nil {
		return fmt.Errorf("resolve forge binary: %w", err)
	}
	ciData := generator.CIWorkflowDataFor(root, cfg, generator.CIInputs{Frontends: ciFrontends(root, cfg)})
	all, err := cigate.Plan(cigate.PlanInput{Root: root, CI: ciData, Cfg: cfg, Forge: forge})
	if err != nil {
		return cliutil.UserErr("forge ci gate", err.Error(), "forge.yaml (ci.gate)", "fix the extra check and re-run")
	}
	lanes, label, err := gateLanes(args, cigate.Lanes(all))
	if err != nil {
		return err
	}
	selected := cigate.Select(all, lanes)

	only := append(cigate.ParseSkips(os.Getenv(cigate.OnlyEnv)), flags.only...)
	if bad := cigate.UnknownOnly(only, selected); len(bad) > 0 {
		return cliutil.UserErr("forge ci gate",
			fmt.Sprintf("%s names no check or group of lane %q: %s — a typo would otherwise run nothing and look clean", cigate.OnlyEnv, label, strings.Join(bad, ", ")),
			"", "checks and groups: `forge ci gate "+label+" --list`")
	}
	selected = cigate.Only(selected, only)

	if flags.list {
		writeGateList(out, selected)
		return nil
	}

	skips := append(cigate.ParseSkips(os.Getenv(cigate.SkipEnv)), flags.skip...)
	if bad := cigate.UnknownSkips(skips, all); len(bad) > 0 {
		names := make([]string, 0, len(all))
		for _, c := range all {
			names = append(names, c.Name)
		}
		return cliutil.UserErr("forge ci gate",
			fmt.Sprintf("%s names no check: %s — a misspelled skip would silently skip nothing", cigate.SkipEnv, strings.Join(bad, ", ")),
			"", "valid names: "+strings.Join(names, ", "))
	}

	// CI runs with go's default -mod=readonly; a caller's GOFLAGS=-mod=mod
	// would let the gate's `go build` rewrite go.mod and go.sum and report a
	// verdict CI would not give. ci.gate.env applies to every check. Every
	// check also builds with -trimpath, so a fresh worktree reuses the build
	// and test cache its siblings filled instead of compiling the module again
	// while it holds a heavy slot (goexec.WithTrimpath).
	env := goexec.CheckEnv(cigate.GateEnv(cfg)...)

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var verdicts cigate.Verdicts
	var cache *cigate.VerdictCache
	switch {
	case flags.noCache || truthy(os.Getenv(cigate.NoCacheEnv)):
		fmt.Fprintln(out, "verdict cache: off ("+map[bool]string{true: "--no-cache", false: cigate.NoCacheEnv + "=1"}[flags.noCache]+")")
	default:
		var why string
		cache, why = cigate.OpenVerdictCache(ctx, cigate.CacheInput{
			Root: root, Dir: os.Getenv(cigate.CacheDirEnv), Forge: gateJudgeIdentity(), Frontends: frontendDirs(ciData), Env: env,
		})
		if cache != nil {
			verdicts = cache
			fmt.Fprintf(out, "verdict cache: on — a check that passed on this tree (%s) before is not run again; %s=1 runs everything\n", cache.Commit(), cigate.NoCacheEnv)
		} else {
			fmt.Fprintln(out, "verdict cache: off — "+why)
		}
	}

	base := flags.base
	if base == "" {
		base = os.Getenv(cigate.BaseEnv)
	}
	selected = applyAffectedMode(ctx, out, selected, affectedArgs{
		root: root, base: base, ignore: cfg.CI.Gate.AffectedIgnore, env: env, skips: skips, cache: cache, full: flags.full,
	})

	opt := cigate.Options{
		Out:      out,
		Skip:     skips,
		Root:     root,
		Groups:   flags.groups || os.Getenv("GITHUB_ACTIONS") == "true",
		Resolver: cigate.NewResolver(root, stderr),
		Env:      env,
		Path:     cigate.GatePath(root, cfg),
		Jobs:     flags.jobs,
		Verdicts: verdicts,
		LogDir:   gateLogDir(root, label),
	}
	opt.Slot = gateSlots(out, stderr)
	// How long each check took last time, so the longest path starts first.
	history := cigate.OpenDurationHistory(ctx, root, os.Getenv(cigate.CacheDirEnv))
	if opt.Expected = history.Expected(); len(opt.Expected) > 0 {
		fmt.Fprintf(out, "scheduling: longest path first, from %d check duration(s) recorded by earlier runs\n", len(opt.Expected))
	}

	rep := cigate.Run(ctx, label, selected, opt)
	rep.Head, rep.Dirty = gitHead(root)
	rep.RenderText(out)
	history.Observe(rep)
	if err := history.Save(); err != nil {
		fmt.Fprintf(stderr, "warning: could not record check durations: %v\n", err)
	}

	if cache != nil {
		if n, why := cache.Flush(context.WithoutCancel(ctx)); why != "" {
			fmt.Fprintln(out, "verdict cache: nothing stored — "+why)
		} else if n > 0 {
			fmt.Fprintf(out, "verdict cache: stored %d new PASS verdict(s) for %s\n", n, cache.Commit())
		}
	}
	report := flags.report
	if report == "" {
		report = os.Getenv("CI_REPORT")
	}
	if report != "" {
		if err := writeReport(report, rep, false); err != nil {
			fmt.Fprintf(stderr, "warning: could not write %s: %v\n", report, err)
		}
	}
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		if err := writeReport(path, rep, true); err != nil {
			fmt.Fprintf(stderr, "warning: could not write the job summary: %v\n", err)
		}
	}
	if rep.Failed() {
		return gateFailure{}
	}
	return nil
}

// applyAffectedMode narrows the unit-tests row to the packages a branch
// reaches (cigate.ComputeAffected), printing the mode and why. A PASS of the
// whole suite cached for this tree answers for any narrower run, so then the
// row stays whole and the cache answers it.
type affectedArgs struct {
	root, base         string
	ignore, env, skips []string
	cache              *cigate.VerdictCache
	full               bool
}

func applyAffectedMode(ctx context.Context, out io.Writer, checks []cigate.Check, in affectedArgs) []cigate.Check {
	var unit *cigate.Check
	for i := range checks {
		if checks[i].Name == cigate.UnitTests {
			unit = &checks[i]
		}
	}
	if unit == nil || unit.NotApplicable != "" {
		return checks
	}
	for _, s := range in.skips {
		if s == cigate.UnitTests {
			return checks
		}
	}
	if in.cache != nil && !unit.NoCache {
		if commit, ok := in.cache.Lookup(*unit); ok {
			fmt.Fprintf(out, "unit tests: full suite — it already passed on this tree (%s)\n", commit)
			return checks
		}
	}
	force := ""
	switch {
	case in.full:
		force = "--full"
	case truthy(os.Getenv(cigate.FullEnv)):
		force = cigate.FullEnv + "=1"
	}
	a := cigate.ComputeAffected(ctx, cigate.AffectedInput{Root: in.root, BaseRef: in.base, Env: in.env, Force: force, Ignore: in.ignore})
	fmt.Fprintln(out, a.Line())
	return cigate.ApplyAffected(checks, a)
}

// gateSlots is how the gate bounds its heavy checks: one host heavy slot each.
// It returns nil — heavy checks then share the light checks' job cap — when
// the limit is off, or when the gate itself already runs inside a slot (under
// `forge slot run`, or as a check of another gate): every slot it asked for
// would be granted at once, so slots could not bound anything.
func gateSlots(out, stderr io.Writer) cigate.SlotFunc {
	env := os.Environ()
	capy, err := hostslot.CapacityFor(hostslot.Heavy, env)
	switch {
	case err != nil:
		fmt.Fprintf(stderr, "⚠️  heavy slots: %v — heavy checks share the job cap\n", err)
		return nil
	case capy.Unlimited:
		fmt.Fprintf(out, "heavy slots: off (%s) — heavy checks share the job cap\n", capy.Source)
		return nil
	}
	if why, inside := hostslot.Inside(hostslot.Heavy, hostslot.Options{Env: env}); inside {
		fmt.Fprintf(stderr, "⚠️  heavy slots: this gate already runs inside a heavy slot (%s), so its heavy checks cannot queue for their own and share the job cap instead. Run the gate outside `forge slot run`.\n", why)
		return nil
	}
	fmt.Fprintf(out, "heavy slots: %s — `forge slot status` shows who holds them\n", capy)
	return func(ctx context.Context) (cigate.HeldSlot, error) {
		return hostslot.Acquire(ctx, hostslot.Heavy, hostslot.Options{Out: stderr, Label: "forge ci gate", Env: env})
	}
}

// gateLogDir is where each check's log goes: CI_LOG_DIR, else tmp/ci/<lane>
// when git ignores it (the scaffolded .gitignore does), else .forge/ci/<lane>.
// A log directory git does not ignore would dirty the tree the gate is
// judging — verify-generated would refuse it, and no verdict could be cached.
func gateLogDir(root, label string) string {
	if d := os.Getenv("CI_LOG_DIR"); d != "" {
		if !filepath.IsAbs(d) {
			d = filepath.Join(root, d)
		}
		return d
	}
	for _, rel := range []string{filepath.Join("tmp", "ci", label), filepath.Join(".forge", "ci", label)} {
		c := exec.Command("git", "check-ignore", "-q", filepath.ToSlash(filepath.Join(rel, "check.log")))
		c.Dir = root
		if c.Run() == nil {
			return filepath.Join(root, rel)
		}
	}
	return filepath.Join(root, ".forge", "ci", label)
}

func writeGateList(w io.Writer, checks []cigate.Check) {
	for _, c := range checks {
		var notes []string
		if c.Heavy {
			notes = append(notes, "heavy")
		}
		if c.MutatesTree {
			notes = append(notes, "mutates-tree")
		}
		if c.NoCache {
			notes = append(notes, "no-cache")
		}
		if len(c.After) > 0 {
			notes = append(notes, "after "+strings.Join(c.After, ","))
		}
		if c.NotApplicable != "" {
			notes = append(notes, "n/a: "+c.NotApplicable)
		}
		fmt.Fprintf(w, "%-12s %-12s %-28s %s\n", c.Lane, c.Group, c.Name, strings.Join(notes, "; "))
	}
}

// gateFailure is the gate's verdict as an error. The summary table above
// already said everything, so its message adds nothing and main prints one
// short line.
type gateFailure struct{}

func (gateFailure) Error() string { return "the CI gate failed (see the summary above)" }

// gateLanes resolves the lane argument. "all" is the three built-in lanes; a
// lane of the project's own naming runs only when asked for by name.
func gateLanes(args []string, known []cigate.Lane) ([]cigate.Lane, string, error) {
	which := "all"
	if len(args) == 1 {
		which = args[0]
	}
	if which == "all" {
		return []cigate.Lane{cigate.LaneChecks, cigate.LaneTest, cigate.LaneIntegration}, "all", nil
	}
	names := []string{"all"}
	for _, l := range known {
		if string(l) == which {
			return []cigate.Lane{l}, which, nil
		}
		names = append(names, string(l))
	}
	return nil, "", cliutil.UserErr("forge ci gate", fmt.Sprintf("unknown lane %q", which), "", "use one of: "+strings.Join(names, ", "))
}

func writeReport(path string, rep cigate.Report, appendTo bool) error {
	mode := os.O_TRUNC | os.O_WRONLY | os.O_CREATE
	if appendTo {
		mode = os.O_APPEND | os.O_WRONLY | os.O_CREATE
	}
	f, err := os.OpenFile(path, mode, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	rep.RenderMarkdown(f)
	return nil
}

// gitHead is the short HEAD commit and whether the tree has uncommitted
// changes, for the summary's `head:` line.
func gitHead(root string) (string, bool) {
	c := exec.Command("git", "rev-parse", "--short", "HEAD")
	c.Dir = root
	head, err := c.Output()
	if err != nil {
		return "", false
	}
	c = exec.Command("git", "status", "--porcelain")
	c.Dir = root
	st, _ := c.Output()
	return strings.TrimSpace(string(head)), len(strings.TrimSpace(string(st))) > 0
}

// gateJudgeIdentity names the judging forge for the verdict cache: its version
// and commit, plus the executable's size and mtime, so two dev builds of one
// commit (one with an uncommitted fix) never share verdicts.
func gateJudgeIdentity() string {
	id := buildinfo.Identity()
	if exe, err := os.Executable(); err == nil {
		if fi, err := os.Stat(exe); err == nil {
			id += fmt.Sprintf(" exe=%d@%d", fi.Size(), fi.ModTime().UnixNano())
		}
	}
	return id
}

func frontendDirs(ci templates.CIWorkflowData) []string {
	var paths []string
	for _, fe := range ci.Frontends {
		paths = append(paths, fe.Path)
	}
	sort.Strings(paths)
	return paths
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
