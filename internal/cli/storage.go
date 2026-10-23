package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/gitsource"
	"github.com/reliant-labs/forge/internal/storage"
	"github.com/reliant-labs/forge/pkg/release"
)

// maintenanceRunner builds the Runner for a pass that RECLAIMS, as opposed to
// one that only reports. Both absolute roots the reclaiming layers walk —
// $TMPDIR and the cross-repo source cache — are named here rather than
// defaulted inside the layer, because each layer REFUSES an unset root under
// `go test`: a test that forgets to scope one gets a skip and a message, not a
// sweep of the developer's real caches. Production is the only caller that
// supplies them, and it supplies them in one place so a new reclaiming layer
// cannot be wired up at three call sites and missed at the fourth.
//
// A source cache that cannot be located is left empty, which the layer reads as
// "use the real machine-local default" in production and as "skip" under test.
// os.UserCacheDir only fails on a host with no HOME, where there is also
// nothing to reclaim.
//
// policyPath is where the policy was loaded from; maintenance state that must
// survive an interrupted pass (a registry stopped for GC) is kept beside it.
func maintenanceRunner(p storage.Policy, policyPath string, out io.Writer) storage.Runner {
	sourceRoot, err := gitsource.DefaultCacheRoot()
	if err != nil {
		sourceRoot = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var gocache, gomodcache, golangci, goimports string
	if !testing.Testing() { // a test must never be handed the real Go caches
		gocache, gomodcache, golangci, goimports = storage.ProductionGoCacheRoots(ctx, storage.Exec)
	}
	return storage.Runner{Policy: p, Out: out, TempRoot: os.TempDir(), SourceCacheRoot: sourceRoot, PolicyPath: policyPath,
		GoCacheRoot: gocache, GoModCacheRoot: gomodcache, GolangciCacheRoot: golangci, GoimportsRoot: goimports}
}

// fullGCFn is the full maintenance pass `storage gc` runs, seamed so the
// recording around it is testable without Docker.
var fullGCFn = func(ctx context.Context, r storage.Runner, apply bool) error { return r.GC(ctx, apply) }

// runFullGC runs the full pass and, when it applied, records the outcome —
// success, or which layers failed — beside the policy. That record is the only
// evidence registry retention is running, so it is written for a failure too:
// `forge doctor` and `forge env up` read it to say so. A preview changes
// nothing and records nothing. The caller holds the maintenance lock.
//
// explicit is true only for a `forge storage gc` a person or agent ran: it is
// the one pass that may remove worktrees (under the worktree_reap policy) and
// anonymous volumes. The installed schedule and `forge storage daemon` pass
// false.
func runFullGC(ctx context.Context, p storage.Policy, path string, out io.Writer, apply, explicit bool) error {
	r := maintenanceRunner(p, path, out)
	r.Explicit = explicit
	err := fullGCFn(ctx, r, apply)
	if !apply {
		return storage.RealFailure(err)
	}
	if recErr := storage.RecordFullGC(path, storage.NewGCResult(time.Now(), err)); recErr != nil && err == nil {
		return fmt.Errorf("record storage GC result: %w", recErr)
	}
	return storage.RealFailure(err)
}

func newStorageCmd() *cobra.Command {
	var path string
	group := &cobra.Command{Use: "storage", Short: "Inspect and bound local caches; preserve persistent application data"}
	group.PersistentFlags().StringVar(&path, "policy", "", "machine storage policy (default: user config directory/forge/storage.json)")
	load := func() (storage.Policy, string, error) {
		p := path
		if p == "" {
			var err error
			p, err = storage.DefaultPath()
			if err != nil {
				return storage.Policy{}, "", err
			}
		}
		cfg, err := storage.Load(p)
		return cfg, p, err
	}
	group.AddCommand(&cobra.Command{Use: "check [path...]", Args: cobra.ArbitraryArgs, Short: "Refuse a build below the physical host free-space reserve", RunE: func(cmd *cobra.Command, paths []string) error {
		p, _, err := load()
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			paths = []string{"."}
		}
		_, err = storage.CheckBuildSpace(p, paths...)
		return err
	}})
	group.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, Short: "Report physical host capacity, Docker usage and node cleanup settings", RunE: func(cmd *cobra.Command, _ []string) error {
		p, _, err := load()
		if err != nil {
			return err
		}
		runner := storage.Runner{Policy: p, Out: cmd.OutOrStdout()}
		statusErr := runner.Status(cmd.Context())
		// Held worktrees are reported even when Docker is unreachable.
		_ = runner.PrintHeldWorktrees(cmd.Context())
		return statusErr
	}})
	group.AddCommand(&cobra.Command{Use: "policy", Args: cobra.NoArgs, Short: "Print the effective storage policy", RunE: func(cmd *cobra.Command, _ []string) error {
		p, _, err := load()
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(p)
	}})
	var apply, dryRun, scheduled bool
	gc := &cobra.Command{Use: "gc", Args: cobra.NoArgs, Short: "Preview cleanup; --apply deletes only eligible cache and registry versions", RunE: func(cmd *cobra.Command, _ []string) error {
		p, path, err := load()
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return storage.WithLock(path, func() error {
			return runFullGC(ctx, p, path, cmd.OutOrStdout(), apply, !scheduled)
		})
	}}
	gc.Flags().BoolVar(&apply, "apply", false, "execute the cleanup plan")
	gc.Flags().BoolVar(&dryRun, "dry-run", false, "preview only (the default)")
	gc.Flags().BoolVar(&scheduled, "scheduled", false, "run as the installed maintenance schedule: never removes worktrees")
	_ = gc.Flags().MarkHidden("scheduled")
	gc.MarkFlagsMutuallyExclusive("apply", "dry-run")
	group.AddCommand(gc)
	var interval time.Duration
	daemon := &cobra.Command{Use: "daemon", Args: cobra.NoArgs, Short: "Run maintenance periodically, reloading policy for every pass", RunE: func(cmd *cobra.Command, _ []string) error {
		if interval < time.Hour {
			return fmt.Errorf("maintenance interval must be at least 1h")
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			p, path, err := load()
			if err == nil {
				// A periodic pass is not an explicit one: it never removes
				// worktrees.
				err = storage.WithLock(path, func() error {
					return runFullGC(ctx, p, path, cmd.OutOrStdout(), true, false)
				})
			}
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "storage maintenance: %v\n", err)
			}
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
	}}
	daemon.Flags().DurationVar(&interval, "interval", 24*time.Hour, "time between maintenance passes")
	group.AddCommand(daemon)
	group.AddCommand(&cobra.Command{Use: "install", Args: cobra.NoArgs, Short: "Install daily maintenance (launchd, systemd timer, or Windows Task Scheduler) using a stable copy of this executable", RunE: func(cmd *cobra.Command, _ []string) error {
		p, path, err := load()
		if err != nil {
			return err
		}
		return storage.WithLock(path, func() error { return installStorageSchedule(cmd.Context(), cmd.OutOrStdout(), path, p) })
	}})
	var contexts, repos, pins, builders []string
	var ledgerDir string
	register := &cobra.Command{Use: "register", Args: cobra.NoArgs, Short: "Register local cluster contexts, declared repositories or release pins", RunE: func(cmd *cobra.Command, _ []string) error {
		_, path, err := load()
		if err != nil {
			return err
		}
		if err := storage.RegisterProject(path, filepath.Dir(filepath.Dir(ledgerDir))); err != nil {
			return err
		}
		historical, err := storage.LedgerPins(ledgerDir)
		if err != nil {
			return err
		}
		for _, builder := range builders {
			if err := (storage.Runner{}).RegisterBuilder(cmd.Context(), path, builder); err != nil {
				return err
			}
		}
		if len(contexts) == 0 && len(repos) == 0 && len(pins) == 0 && len(historical) == 0 {
			return nil
		}
		return storage.Register(cmd.Context(), path, contexts, repos, append(pins, historical...))
	}}
	register.Flags().StringSliceVar(&builders, "builder", nil, "local Docker builders whose cache to maintain")
	register.Flags().StringVar(&ledgerDir, "ledger", ".forge/releases", "local release ledger directory to import before enabling cleanup")
	register.Flags().StringSliceVar(&contexts, "context", nil, "local k3d contexts that can use these images")
	register.Flags().StringSliceVar(&repos, "repository", nil, "declared local image repositories, including registry host")
	register.Flags().StringSliceVar(&pins, "pin", nil, "image references that must remain available")
	group.AddCommand(register)
	var restart bool
	nodes := &cobra.Command{Use: "configure-nodes", Args: cobra.NoArgs, Short: "Preview kubelet GC migration for existing registered nodes; --apply restarts them", RunE: func(cmd *cobra.Command, _ []string) error {
		p, path, err := load()
		if err != nil {
			return err
		}
		return storage.WithLock(path, func() error {
			return (storage.Runner{Policy: p, Out: cmd.OutOrStdout()}).ConfigureNodes(cmd.Context(), path, restart)
		})
	}}
	nodes.Flags().BoolVar(&restart, "apply", false, "install configuration and restart local k3d nodes sequentially")
	group.AddCommand(nodes)
	group.AddCommand(newStorageWorktreesCmd())
	group.AddCommand(newStorageAutoGCCmd(&path))
	group.AddCommand(newStorageGoCacheCmd(&path))
	return cmdutil.StrictGroup(group)
}

// newStorageWorktreesCmd is `forge storage worktrees`. It reads no storage
// policy — worktree cleanup is scoped by its own flags — so it is built apart
// from the policy-loading subcommands above.
func newStorageWorktreesCmd() *cobra.Command {
	var repo, base string
	var worktreeAge time.Duration
	var remove, reliantManaged bool
	trees := &cobra.Command{Use: "worktrees", Args: cobra.NoArgs, Short: "Preview idle, clean, pushed worktrees of a repo; --apply removes them", RunE: func(cmd *cobra.Command, _ []string) error {
		return (storage.Runner{Out: cmd.OutOrStdout()}).Worktrees(cmd.Context(), repo, base, worktreeAge, remove, reliantManaged)
	}}
	trees.Flags().StringVar(&repo, "repo", ".", "repository whose worktrees to inspect")
	trees.Flags().StringVar(&base, "base", "", "integration branch for the pushed test (default: origin/HEAD, else origin/main)")
	trees.Flags().DurationVar(&worktreeAge, "idle", 24*time.Hour, "minimum time since any activity in the worktree (at least 24h)")
	trees.Flags().BoolVar(&remove, "apply", false, "remove removable worktrees without forcing or deleting branches")
	trees.Flags().BoolVar(&reliantManaged, "reliant-managed", false, "also consider worktrees under Reliant's worktree root (~/.reliant/worktrees), which Reliant otherwise reclaims itself")
	return trees
}

var checkBuildStorageFn = func(project string) error {
	return checkBuildStorage(project, os.Stderr)
}

// enforceReserveEnv turns the host reserve back into a refusal on a CI
// runner. Any non-empty value enforces it.
const enforceReserveEnv = "FORGE_STORAGE_ENFORCE_RESERVE"

// checkBuildStorage is the admission check every build lane runs: refuse a
// build that would start below the machine policy's host reserve.
//
// ON A CI RUNNER A SHORTFALL IS A WARNING, NOT A REFUSAL. The reserve exists
// because a PERSISTENT machine accumulates caches until it fills — the
// developer Mac that went from 70 GiB free to a failed build. Its remedy,
// `forge storage gc`, reclaims exactly those caches. An ephemeral runner has
// neither: its disk is discarded with the job, and a stock GitHub runner
// starts with ~14 GiB free — under the 20 GiB default before a single step
// runs. Refusing there turned every `forge env build` on a stock runner into
// a failed job with a remedy that reclaims nothing — control-plane's test,
// image-build and release workflows all build on stock runners — and could
// not be satisfied by any action the job could take short of guessing which
// toolchains to delete.
//
// Only a MEASURED shortfall (*storage.ReserveError) is relaxed. A policy
// that will not load, or a disk that cannot be read, still fails: that is an
// unknown, and being on a runner says nothing about it. The shortfall is
// still printed, so a build that then dies of ENOSPC has its cause on the
// line above. A persistent self-hosted runner IS a machine that fills, so
// FORGE_STORAGE_ENFORCE_RESERVE=1 restores the refusal there.
func checkBuildStorage(project string, warn io.Writer) error {
	path, err := storage.DefaultPath()
	if err != nil {
		return err
	}
	p, err := storage.Load(path)
	if err != nil {
		return err
	}
	_, err = storage.CheckBuildSpace(p, project)
	var short *storage.ReserveError
	if !errors.As(err, &short) || !buildinfo.IsCI() || os.Getenv(enforceReserveEnv) != "" {
		return err
	}
	fmt.Fprintf(warn, "warning: %v\n  building anyway: this is an ephemeral CI runner, whose disk is discarded with the job; set %s=1 to refuse instead (persistent self-hosted runners)\n",
		short, enforceReserveEnv)
	return nil
}

// addClusterStorageArgsFn is the seam every k3d creation path routes its argv
// through, so a test can assert the kubelet mount is present without a policy
// file on disk — and so a new creation path cannot quietly skip it.
var addClusterStorageArgsFn = addClusterStorageArgs

// Only actual k3d creation gains this mount. Existing nodes are never restarted
// as a side effect of a build; storage status exposes their config for migration.
func addClusterStorageArgs(args []string) ([]string, error) {
	path, err := storage.DefaultPath()
	if err != nil {
		return nil, err
	}
	p, err := storage.Load(path)
	if err != nil {
		return nil, err
	}
	file, err := storage.NodeConfigPath(path, p)
	if errors.Is(err, storage.ErrMachinePolicyUnderTest) {
		return args, nil
	}
	if err != nil {
		return nil, err
	}
	return append(args, "--volume", filepath.Clean(file)+":/var/lib/rancher/k3s/agent/etc/kubelet.conf.d/90-forge-storage.conf:ro@all"), nil
}

// registerBuildStorage converges what the BUILD knows: the project whose
// rotated logs forge may expire, the repositories this build pushes to a local
// registry, the declared k3d contexts, and the project's release pins.
//
// The repositories are the half the cluster phase cannot supply, and they are
// what completes a registry entry (see storage.Converge's completeness gate) —
// so for a project that builds and pushes locally, activation finishes here
// with no command anyone has to remember to run.
//
// ctx is unused now that this goes through Converge rather than Register's
// live docker discovery; it is kept so the call site reads the same as the
// other storage touch points and so a future fact source that needs a context
// is not a signature change at every caller.
func registerBuildStorage(_ context.Context, project string, entities *KCLEntities, plan pushPlan) {
	facts := storage.Facts{Project: project, Repos: projectRepos(project)}
	if pins, err := storage.LedgerPins(filepath.Join(project, ".forge", "releases")); err == nil {
		facts.Pins = pins
	}
	if entities != nil {
		facts.Contexts = localClusterContexts(entities.Clusters)
		for _, d := range plan.destinations {
			if isLocalRegistryHost(d.host()) {
				facts.Repositories = append(facts.Repositories, d.repository)
				// The destination host IS a verified alias of whichever local
				// registry serves it: forge just pushed this reference there.
				// The container name comes from the cluster phase's reading of
				// the k3d config; a build with no cluster declaration supplies
				// the repositories and leaves the entry incomplete until one
				// does, which is the gate working as intended.
				facts.Aliases = append(facts.Aliases, d.host())
			}
		}
		facts.Registry = declaredRegistryContainer(entities.Clusters)
	}
	convergeStorageFn(facts)
}

// declaredRegistryContainer reads the registry container name out of the first
// declared cluster whose k3d config references one via `registries.use`. An
// unreadable or registry-less config yields "", which converges the other facts
// and records no registry.
func declaredRegistryContainer(clusters []ClusterEntity) string {
	for _, c := range clusters {
		if c.Config == "" {
			continue
		}
		data, err := os.ReadFile(c.Config)
		if err != nil {
			continue
		}
		if container, _, err := registryFactsFromConfig(data); err == nil && container != "" {
			return container
		}
	}
	return ""
}

// releasePinLockWait bounds how long a release cut waits for a running
// maintenance pass. Pinning is milliseconds of work, so any real wait is a pass
// that is mid-flight; waiting keeps the guarantee (pins land BEFORE maintenance
// can delete) without failing a cut that already paid for its build.
var releasePinLockWait = 5 * time.Minute

// Pin before publishing the ledger entry. If maintenance owns the lock, the
// cut WAITS (bounded) rather than racing deletion or failing instantly. Failed
// cuts can leave safe pins.
func pinStorageRelease(rel release.Release) error {
	path, err := storage.DefaultPath()
	if err != nil {
		return err
	}
	err = storage.WithLockWait(path, releasePinLockWait, func() error {
		p, err := storage.Load(path)
		if err != nil {
			return err
		}
		if len(p.Registries) == 0 {
			return nil
		}
		for _, a := range rel.Artifacts {
			for _, digest := range a.Digests {
				p.Pins = append(p.Pins, digest)
			}
		}
		seen := map[string]bool{}
		var pins []string
		for _, pin := range p.Pins {
			if !seen[pin] {
				pins = append(pins, pin)
				seen[pin] = true
			}
		}
		p.Pins = pins
		return storage.Save(path, p)
	})
	// A release cut by a test pins nothing real, and the machine policy is
	// unwritable under `go test`; refusing the cut over it would fail every
	// test that exercises `--release`.
	if errors.Is(err, storage.ErrMachinePolicyUnderTest) {
		return nil
	}
	return err
}

// docker build uses the default builder unless BUILDX_BUILDER explicitly
// overrides it; buildx use alone does not change docker build's selection.
func prepareDockerBuildStorage(ctx context.Context, project string) error {
	if err := checkBuildStorageFn(project); err != nil {
		return err
	}
	builder := os.Getenv("BUILDX_BUILDER")
	if builder == "" {
		builder = "default"
	}
	registerDockerBuilderStorage(ctx, builder)
	return nil
}

func registerDockerBuilderStorage(ctx context.Context, builder string) {
	path, err := storage.DefaultPath()
	if err == nil {
		err = (storage.Runner{}).RegisterBuilder(ctx, path, builder)
	}
	if err != nil && !errors.Is(err, storage.ErrMachinePolicyUnderTest) {
		fmt.Fprintf(os.Stderr, "storage builder registration: %v\n", err)
	}
}
