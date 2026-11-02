// Package cli — `forge cluster` k3d lifecycle subcommands.
//
// This file consolidates the k3d cluster lifecycle that every k8s-targeting
// forge project would otherwise hand-write in bash (~30-50 lines of
// idempotent create / delete / wait-for-rollout / context-pin logic).
//
// k3d itself is the source of truth for cluster state — we shell out to
// `k3d cluster create/delete/list` rather than reinvent. The value forge
// adds is:
//
//   - read deploy/k3d.yaml as the canonical config (no hand-written
//     --servers/--no-lb/--registry-create flags scattered across scripts)
//   - idempotent up/down semantics (`up` no-ops if the cluster exists,
//     `down` no-ops if it doesn't)
//   - kubectl context pinning so `forge cluster reload` can't
//     accidentally apply to staging or prod
//   - one-command reload that re-renders KCL + applies + waits for rollout
//     (the inner loop during local dev)
package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/config"
)

// defaultK3dConfigPath is the canonical location of the project's k3d
// Simple-config YAML. Override via --config.
const defaultK3dConfigPath = "deploy/k3d.yaml"

// The k3d lifecycle subcommands below (up/down/reset/reload) are
// registered flat under `forge cluster` by newClusterCmd in dev.go.
// `status` is served by the dev_status.go implementation (a superset of
// the old nested cluster status), so there is no newDevClusterStatusCmd.

func newDevClusterUpCmd() *cobra.Command {
	var (
		configPath string
		wait       bool
	)
	cmd := &cobra.Command{
		Use:   "up [environment]",
		Args:  cobra.MaximumNArgs(1),
		Short: "Create the k3d cluster from deploy/k3d.yaml",
		Long: `Create the k3d cluster from deploy/k3d.yaml.

If the cluster already exists, this is a no-op success. With --wait,
blocks until the cluster's nodes report ready.

Given an environment, the clusters come from that environment's KCL instead
of a k3d config file: every forge.Cluster the env's bundle declares is ensured exactly
as ` + "`forge env up <env>`" + ` ensures it — the declared pod/Service CIDRs, API port,
owner network and registry-inherit included, none of which a k3d YAML carries.
Use it whenever the env declares its clusters: a cluster created from the bare
config file lacks those fields, and the env's own deploy then refuses it.

Examples:
  forge cluster up
  forge cluster up --wait
  forge cluster up --config deploy/k3d.custom.yaml
  forge cluster up dev-k8s --wait`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if cmd.Flags().Changed("config") {
					return fmt.Errorf("an environment and --config are mutually exclusive: the environment's KCL names its clusters (and their config files)")
				}
				return runEnvClustersUp(cmd.Context(), args[0], wait)
			}
			return runDevClusterUp(cmd.Context(), configPath, wait)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", defaultK3dConfigPath, "k3d config file (ignored when an environment is given)")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait until cluster nodes are ready")
	return cmd
}

func newDevClusterDownCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "down [environment]",
		Args:  cobra.MaximumNArgs(1),
		Short: "Delete the k3d cluster",
		Long: `Delete the k3d cluster named by --config's metadata.name, and FIRST every
cluster nested on its docker network (declared with ` + "`owner`" + `, so it has no
config file of its own and cannot outlive its owner's network).

Given an environment, delete every k3d cluster its KCL declares instead —
including secondaries declared with ` + "`owner`" + ` and no config file, which
--config cannot name. Secondaries are deleted before their owner: k3d cannot
remove a docker network a secondary is still attached to.

This deletes whole clusters, and with them every namespace on them — including
other environments' and other worktrees' stacks sharing the cluster.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				if cmd.Flags().Changed("config") {
					return fmt.Errorf("an environment and --config are mutually exclusive: the environment's KCL names its clusters")
				}
				return runEnvClustersDown(cmd.Context(), args[0])
			}
			// An EXPLICIT --config that does not exist is an error, never a
			// fallback. resolveClusterName otherwise falls back to the
			// forge.yaml project name — for a project named like its
			// cluster, a typo'd path deletes the wrong cluster.
			if cmd.Flags().Changed("config") {
				if _, err := os.Stat(configPath); err != nil {
					return fmt.Errorf("--config %s: %w (refusing to guess which cluster to delete)", configPath, err)
				}
			}
			return runDevClusterDown(cmd.Context(), configPath)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", defaultK3dConfigPath, "k3d config file (ignored when an environment is given)")
	return cmd
}

func newDevClusterResetCmd() *cobra.Command {
	var (
		configPath string
		wait       bool
	)
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Delete then recreate the cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runDevClusterDown(cmd.Context(), configPath); err != nil {
				return err
			}
			return runDevClusterUp(cmd.Context(), configPath, wait)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", defaultK3dConfigPath, "k3d config file")
	cmd.Flags().BoolVar(&wait, "wait", true, "Wait until cluster nodes are ready after recreate")
	return cmd
}

func newDevClusterReloadCmd() *cobra.Command {
	var (
		configPath string
		imageTag   string
		namespace  string
		dryRun     bool
	)
	cmd := &cobra.Command{
		Use:   "reload",
		Short: "Re-render deploy/kcl/dev + kubectl apply + wait rollout",
		Long: `Re-render the dev KCL manifests, apply, and wait for rollout.

This is the inner loop during local development: after editing code or
KCL, run this to push the change into the cluster without rebuilding the
docker image (the same code path forge env deploy dev uses, but skips the
cluster bootstrap).

Examples:
  forge cluster reload
  forge cluster reload --image-tag dev-2026-06-01
  forge cluster reload --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDevClusterReload(cmd.Context(), configPath, imageTag, namespace, dryRun)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", defaultK3dConfigPath, "k3d config file")
	cmd.Flags().StringVar(&imageTag, "image-tag", "", "Image tag (default: git short SHA)")
	cmd.Flags().StringVar(&namespace, "namespace", "", "Override namespace from environment config")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print manifests without applying")
	return cmd
}

// k3dSimpleConfig is the subset of the k3d "simple config" YAML we
// inspect. The full schema is large; we only need the cluster name to
// resolve `k3d-<name>` for the kubectl context.
type k3dSimpleConfig struct {
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
}

// k3dClusterListEntry mirrors the `k3d cluster list -o json` element
// shape — only the fields we read.
type k3dClusterListEntry struct {
	Name           string `json:"name"`
	ServersRunning int    `json:"serversRunning"`
	ServersCount   int    `json:"serversCount"`
	AgentsRunning  int    `json:"agentsRunning"`
	AgentsCount    int    `json:"agentsCount"`
}

// k3dClusterRuntimeState distinguishes a cluster that merely exists from
// one whose declared server/agent containers are all running. k3d keeps
// stopped clusters in `cluster list`, so presence alone is not sufficient
// for any subsequent kubectl or docker-exec reconciliation.
type k3dClusterRuntimeState struct {
	Exists  bool
	Running bool
}

// readK3dClusterName parses the k3d config file and returns the cluster
// name. Falls back to "" when the file is missing (callers pass the
// fallback name explicitly).
func readK3dClusterName(configPath string) (string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", configPath, err)
	}
	var cfg k3dSimpleConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parse %s: %w", configPath, err)
	}
	return cfg.Metadata.Name, nil
}

// resolveClusterName returns the cluster name to operate on. Priority:
// 1) name in deploy/k3d.yaml metadata.name
// 2) project name from forge.yaml
// 3) "dev" (last-resort default matching forge's previous behavior)
func resolveClusterName(configPath string) (string, error) {
	if name, err := readK3dClusterName(configPath); err == nil && name != "" {
		return name, nil
	}
	if store, err := loadProjectStore(); err == nil {
		return store.Meta().Name, nil
	}
	return "dev", nil
}

// listK3dClusters shells out to `k3d cluster list -o json` and returns
// the parsed list. An empty array (no clusters) returns a nil slice with
// no error.
//
// stdout carries the JSON, so stderr is captured SEPARATELY and folded into
// the error. k3d reports its real diagnosis there (e.g. "runtime failed to
// list nodes: docker failed to get containers ... 500 Internal Server
// Error"); discarding it and printing a blanket "install k3d" hint sent
// callers chasing a missing binary that was installed all along. The
// install hint is now emitted ONLY when the binary is genuinely absent.
func listK3dClusters(ctx context.Context) ([]k3dClusterListEntry, error) {
	cmd := exec.CommandContext(ctx, "k3d", "cluster", "list", "-o", "json")
	scrubSubprocessLogEnv(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("k3d cluster list: %w (install k3d: https://k3d.io)", err)
		}
		return nil, fmt.Errorf("k3d cluster list: %w%s", err, formatCommandStderr(stderr.String()))
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "[]" {
		return nil, nil
	}
	var entries []k3dClusterListEntry
	if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
		// Output that does not begin with '[' is almost always diagnostics
		// interleaved ahead of the JSON by a log-verbosity env var forge did
		// not scrub. Say so: the raw error names an escape byte
		// ("invalid character '\x1b'"), which is unactionable on its own.
		if !strings.HasPrefix(trimmed, "[") {
			return nil, fmt.Errorf(
				"parse k3d cluster list output: %w\n"+
					"k3d wrote non-JSON on stdout — usually a log-verbosity env var it inherited.\n"+
					"first line: %s\n"+
					"fix: unset the variable (LOG_LEVEL / K3D_LOG_LEVEL / DEBUG) for this shell,\n"+
					"     or report it so forge scrubs that name too (internal/cli/subprocess_env.go)",
				err, firstLine(trimmed))
		}
		return nil, fmt.Errorf("parse k3d cluster list output: %w", err)
	}
	return entries, nil
}

// The two host-state shell-outs every `forge cluster` path funnels through,
// seamed so no test reads the machine's real k3d clusters or rewrites the
// developer's kubeconfig. That kubeconfig is shared with every other process
// on the box: a unit test that ran a real `kubectl config use-context`
// switched it out from under them. main_test.go installs hermetic defaults;
// a test that wants a cluster list or observes the pin sets its own.
var (
	listK3dClustersFn   = listK3dClusters
	pinKubectlContextFn = pinKubectlContext
)

// fullyRunning reports whether every k3d server and agent is running. Older
// k3d versions may omit the total-count fields; in that case, at least one
// running server is the strongest available signal and preserves backwards
// compatibility with their JSON shape.
func (e k3dClusterListEntry) fullyRunning() bool {
	if e.ServersRunning == 0 {
		return false
	}
	if e.ServersCount > 0 && e.ServersRunning != e.ServersCount {
		return false
	}
	if e.AgentsCount > 0 && e.AgentsRunning != e.AgentsCount {
		return false
	}
	return true
}

// lookupK3dClusterRuntimeState returns both presence and liveness from one
// `k3d cluster list` snapshot. A stopped cluster exists but is not running.
func lookupK3dClusterRuntimeState(ctx context.Context, name string) (k3dClusterRuntimeState, error) {
	entries, err := listK3dClustersFn(ctx)
	if err != nil {
		return k3dClusterRuntimeState{}, err
	}
	for _, e := range entries {
		if e.Name == name {
			return k3dClusterRuntimeState{Exists: true, Running: e.fullyRunning()}, nil
		}
	}
	return k3dClusterRuntimeState{}, nil
}

// clusterExists returns true when a cluster of the given name is listed
// by k3d.
func clusterExists(ctx context.Context, name string) (bool, error) {
	state, err := lookupK3dClusterRuntimeState(ctx, name)
	if err != nil {
		return false, err
	}
	return state.Exists, nil
}

const k3dClusterStartTimeout = 90 * time.Second

var (
	runK3dClusterStartCommandFn  = runK3dClusterStartCommand
	lookupK3dStateAfterStartFn   = lookupK3dClusterRuntimeState
	cleanupK3dStartToolsFn       = cleanupK3dStartTools
	runK3dClusterCreateCommandFn = runK3dClusterCreateCommand
	lookupK3dStateAfterCreateFn  = lookupK3dClusterRuntimeState
	cleanupK3dCreateToolsFn      = cleanupK3dStartTools
	mergeK3dKubeconfigFn         = mergeK3dKubeconfig
	// lookupClusterStateForUpFn is the `forge cluster up` existence probe.
	// Seamed like its siblings above so the up path is testable on a host
	// that happens to have a real cluster of the same name running.
	lookupClusterStateForUpFn   = lookupK3dClusterRuntimeState
	k3dClusterStartPollInterval = time.Second
	k3dClusterStartHealthyGrace = 10 * time.Second
)

// runK3dClusterStartCommand is split from startK3dCluster so the latter's
// hung-client recovery is unit-testable without launching Docker.
func runK3dClusterStartCommand(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, "k3d", "cluster", "start", name,
		"--wait", "--timeout", k3dClusterStartTimeout.String())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runK3dClusterCreateCommand is split from createK3dCluster for the same
// reason as the start command: k3d can wedge in its post-start CoreDNS alias
// injection even though every cluster container is already running.
func runK3dClusterCreateCommand(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, "k3d", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// mergeK3dKubeconfig writes the cluster's k3d-<name> context into the default
// kubeconfig, under withKubeconfigLock. It is THE merge after every create —
// createK3dCluster tells `k3d cluster create` not to write the kubeconfig,
// because that write is unlocked — and it also repairs a wedged create client
// cancelled after the cluster was already running. Merge is an idempotent
// update and deliberately does not switch the caller's active context.
func mergeK3dKubeconfig(ctx context.Context, name string) error {
	return withKubeconfigLock(func() error {
		cmd := exec.CommandContext(ctx, "k3d", "kubeconfig", "merge", name,
			"--kubeconfig-merge-default", "--kubeconfig-switch-context=false")
		cmd.Stdout = nil
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("merge kubeconfig for k3d cluster %q: %w", name, err)
		}
		return nil
	})
}

// cleanupK3dStartTools removes the disposable helper k3d leaves behind when
// its start client is cancelled during host-alias injection. Cluster nodes,
// load balancers, registries, volumes, and networks are untouched.
func cleanupK3dStartTools(ctx context.Context, name string) error {
	container := "k3d-" + name + "-tools"
	out, err := exec.CommandContext(ctx, "docker", "rm", "-f", container).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such container") {
		return fmt.Errorf("remove temporary container %s: %w", container, err)
	}
	return nil
}

func finishK3dClusterLifecycle(ctx, commandCtx context.Context, name, operation string, commandErr error,
	lookup func(context.Context, string) (k3dClusterRuntimeState, error),
	cleanup func(context.Context, string) error,
) error {
	if commandErr == nil {
		if cleanupErr := cleanup(ctx, name); cleanupErr != nil {
			fmt.Fprintf(os.Stderr, "  warning: %v\n", cleanupErr)
		}
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	state, stateErr := lookup(ctx, name)
	if cleanupErr := cleanup(ctx, name); cleanupErr != nil {
		fmt.Fprintf(os.Stderr, "  warning: %v\n", cleanupErr)
	}
	if stateErr == nil && state.Exists && state.Running {
		fmt.Fprintf(os.Stderr,
			"  warning: k3d cluster %s %q returned after the cluster became running (%v) — continuing with readiness checks\n",
			operation, name, commandErr)
		return nil
	}
	if commandCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("k3d cluster %s %q timed out after %s", operation, name, k3dClusterStartTimeout)
	}
	if stateErr != nil {
		return fmt.Errorf("k3d cluster %s %q: %w (state recheck also failed: %v)", operation, name, commandErr, stateErr)
	}
	return fmt.Errorf("k3d cluster %s %q: %w", operation, name, commandErr)
}

func runK3dClusterLifecycle(ctx context.Context, name, operation string,
	run func(context.Context) error,
	lookup func(context.Context, string) (k3dClusterRuntimeState, error),
	cleanup func(context.Context, string) error,
) error {
	commandCtx, cancel := context.WithTimeout(ctx, k3dClusterStartTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- run(commandCtx)
	}()

	ticker := time.NewTicker(k3dClusterStartPollInterval)
	defer ticker.Stop()
	var runningSince time.Time

	for {
		select {
		case commandErr := <-done:
			return finishK3dClusterLifecycle(ctx, commandCtx, name, operation, commandErr, lookup, cleanup)
		case <-ticker.C:
			state, err := lookup(ctx, name)
			if err != nil || !state.Exists || !state.Running {
				runningSince = time.Time{}
				continue
			}
			if runningSince.IsZero() {
				runningSince = time.Now()
				continue
			}
			if time.Since(runningSince) < k3dClusterStartHealthyGrace {
				continue
			}

			fmt.Fprintf(os.Stderr,
				"  warning: k3d cluster %q is fully running but its %s client did not return within %s — cancelling the wedged client\n",
				name, operation, k3dClusterStartHealthyGrace)
			cancel()
			commandErr := <-done
			if cleanupErr := cleanup(ctx, name); cleanupErr != nil {
				fmt.Fprintf(os.Stderr, "  warning: %v\n", cleanupErr)
			}
			if commandErr != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			return nil
		case <-commandCtx.Done():
			commandErr := <-done
			return finishK3dClusterLifecycle(ctx, commandCtx, name, operation, commandErr, lookup, cleanup)
		case <-ctx.Done():
			cancel()
			<-done
			if cleanupErr := cleanup(context.Background(), name); cleanupErr != nil {
				fmt.Fprintf(os.Stderr, "  warning: %v\n", cleanupErr)
			}
			return ctx.Err()
		}
	}
}

// startK3dCluster starts a preserved-but-stopped cluster and waits for k3d's
// servers and load balancer to become ready. k3d v5.8.1 and v5.9.0 can hang
// after the cluster is already running while injecting host aliases into
// CoreDNS. Forge polls authoritative state while k3d runs: once every
// server/agent is up, k3d
// gets a short grace period to finish normally; a still-wedged client is then
// cancelled and its disposable tools helper removed. Callers subsequently
// wait for Kubernetes Nodes and Forge's own DNS/MSS reconciliation.
func startK3dCluster(ctx context.Context, name string) error {
	return runK3dClusterLifecycle(ctx, name, "start",
		func(commandCtx context.Context) error {
			return runK3dClusterStartCommandFn(commandCtx, name)
		},
		lookupK3dStateAfterStartFn,
		cleanupK3dStartToolsFn,
	)
}

// createK3dCluster gives fresh creates the same hung-client recovery as
// starts. k3d 5.9.0 can reach a fully running cluster and then wedge while
// injecting CoreDNS host aliases; authoritative running state plus Forge's
// subsequent readiness/DNS reconciliation is sufficient to continue safely.
//
// It also rides out one k3s crash during boot. k3d waits for a server's
// "k3s is up and running" log line and gives up the moment the container
// restarts ("stopped returning log lines: … status=restarting"), although
// Docker's restart policy would have brought k3s back. That failure happens
// entirely inside `k3d cluster create`, before forge has sent the cluster a
// single request, and the same arguments succeed on the next try: CI saw it
// once, two seconds into a server's boot, in a run whose other creates were
// identical and passed. So a create that fails WITH a restarted server is
// deleted and made again, once, and the server's exit and last log lines
// are printed as the reason. Any other failure is returned as before.
func createK3dCluster(ctx context.Context, name string, args []string) error {
	var storageErr error
	args, storageErr = addClusterStorageArgsFn(args)
	if storageErr != nil {
		return storageErr
	}
	if args, storageErr = addK3dKmsgArgsFn(args); storageErr != nil {
		return storageErr
	}
	// The context is merged below, under the kubeconfig lock; see
	// kubeconfig_lock.go for the lost-update this prevents.
	args = withoutKubeconfigWrites(args)
	// Keep a failed cluster's nodes, so a crashed server's logs can be read,
	// and roll back ourselves. Only for a cluster that does not exist yet: a
	// create that fails because the name is taken must never delete what was
	// already there.
	ownRollback := false
	if pre, err := lookupK3dStateAfterCreateFn(ctx, name); err == nil && !pre.Exists && !slices.Contains(args, "--no-rollback") {
		args = append(slices.Clone(args), "--no-rollback")
		ownRollback = true
	}
	var firstCrash string
	for attempt := 1; ; attempt++ {
		var seen bootCrashWatch
		err := runK3dClusterLifecycle(ctx, name, "create",
			func(commandCtx context.Context) error {
				if !ownRollback {
					return runK3dClusterCreateCommandFn(commandCtx, args)
				}
				return seen.run(commandCtx, name, func(c context.Context) error {
					return runK3dClusterCreateCommandFn(c, args)
				})
			},
			lookupK3dStateAfterCreateFn,
			cleanupK3dCreateToolsFn,
		)
		crash := seen.reason()
		if err == nil && crash == "" {
			return mergeK3dKubeconfigFn(ctx, name)
		}
		if !ownRollback || ctx.Err() != nil {
			if err == nil {
				err = fmt.Errorf("k3d cluster create %q: a server restarted during boot", name)
			}
			return err
		}
		if crash == "" {
			crash = k3dServerBootCrashFn(ctx, name)
		}
		if err == nil {
			err = fmt.Errorf("k3d cluster create %q: a server restarted during boot", name)
		}
		if derr := deleteK3dClusterFn(ctx, name); derr != nil {
			fmt.Fprintf(os.Stderr, "  warning: roll back the failed k3d cluster %q: %v\n", name, derr)
		}
		switch {
		case crash == "":
			return err
		case attempt == 1:
			firstCrash = crash
			fmt.Fprintf(os.Stderr, "  warning: k3s exited while k3d cluster %q was booting, and k3d does not wait out a server restart — "+
				"deleted it, creating it once more. What the server said:\n%s\n", name, indentLines(crash, "    "))
			continue
		default:
			return fmt.Errorf("%w\nk3s exited during boot on both attempts.\nfirst attempt:\n%s\nsecond attempt:\n%s",
				err, indentLines(firstCrash, "    "), indentLines(crash, "    "))
		}
	}
}

// k3dServerBootCrashFn is the seam for k3dServerBootCrash.
var k3dServerBootCrashFn = k3dServerBootCrash

// k3dBootCrashPoll is how often a create checks its servers for a restart.
var k3dBootCrashPoll = time.Second

// bootCrashWatch watches a create's servers for a restart while the create
// runs, and cancels the k3d client when one happens. Not every k3d reports a
// restarted server: depending on version and Docker, its readiness wait
// either fails ("stopped returning log lines: … status=restarting") or keeps
// following a log stream that has ended until its own timeout. Watching the
// containers decides the crash the same way in both cases, at once.
type bootCrashWatch struct {
	mu    sync.Mutex
	crash string
}

func (w *bootCrashWatch) run(ctx context.Context, name string, create func(context.Context) error) error {
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(k3dBootCrashPoll)
		defer t.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-t.C:
			}
			if r := k3dServerBootCrashFn(watchCtx, name); r != "" {
				w.mu.Lock()
				w.crash = r
				w.mu.Unlock()
				cancel()
				return
			}
		}
	}()
	err := create(watchCtx)
	cancel()
	<-done
	return err
}

func (w *bootCrashWatch) reason() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.crash
}

// k3dServerBootCrash reports why a failed create's server crashed during
// boot, or "" when no server container restarted (then the failure is not
// the k3s boot crash createK3dCluster rides out). Read before the rollback
// deletes the containers: their state and last log lines are the reason.
func k3dServerBootCrash(ctx context.Context, name string) string {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label=k3d.cluster="+name, "--filter", "label=k3d.role=server",
		"--format", "{{.Names}}").Output()
	if err != nil {
		return ""
	}
	var reasons []string
	for _, node := range strings.Fields(string(out)) {
		state, err := exec.CommandContext(ctx, "docker", "inspect", "--format",
			"{{.RestartCount}} {{.State.Status}} exit={{.State.ExitCode}} oom={{.State.OOMKilled}} {{.State.Error}}", node).Output()
		if err != nil {
			continue
		}
		restarts, rest, _ := strings.Cut(strings.TrimSpace(string(state)), " ")
		if restarts == "0" && !strings.HasPrefix(rest, "restarting") {
			continue
		}
		logs, _ := exec.CommandContext(ctx, "docker", "logs", "--tail", "25", node).CombinedOutput()
		reason := fmt.Sprintf("%s: restarts=%s %s\n%s", node, restarts, strings.TrimSpace(rest), strings.TrimSpace(string(logs)))
		if ep := k3dEntrypointLog(ctx, node); ep != "" {
			reason += "\nk3d entrypoint log:\n" + ep
		}
		reasons = append(reasons, reason)
	}
	return strings.Join(reasons, "\n")
}

// k3dEntrypointLog returns the tail of a k3d node's newest entrypoint log
// (/var/log/k3d-entrypoints_*.log), or "". k3d's entrypoint runs its fix-up
// scripts (cgroup v2, DNS, mounts) before k3s, with their output in that file
// rather than on stdout, and exits 1 if one fails — so a server that dies
// before k3s logs a line has its reason only there. `docker cp` reads it
// from a stopped or restarting container, where `docker exec` cannot.
func k3dEntrypointLog(ctx context.Context, node string) string {
	out, err := exec.CommandContext(ctx, "docker", "cp", node+":/var/log/.", "-").Output()
	if err != nil {
		return ""
	}
	var newest, body string
	tr := tar.NewReader(bytes.NewReader(out))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		base := path.Base(h.Name)
		if !strings.HasPrefix(base, "k3d-entrypoints_") || !strings.HasSuffix(base, ".log") || base <= newest {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, 64<<10))
		if err != nil {
			continue
		}
		newest, body = base, string(b)
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) > 15 {
		lines = lines[len(lines)-15:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// pinKubectlContext sets the current kubectl context to k3d-<name>.
// k3d names its kubeconfig contexts as `k3d-<cluster-name>` by
// convention, so this is the one-liner guard rail that prevents the
// rest of `forge cluster` from leaking commands into a non-dev context.
func pinKubectlContext(ctx context.Context, clusterName string) error {
	target := "k3d-" + clusterName
	return withKubeconfigLock(func() error {
		cmd := exec.CommandContext(ctx, "kubectl", "config", "use-context", target)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("kubectl config use-context %s: %w", target, err)
		}
		return nil
	})
}

// currentKubectlContext returns the current kubectl context name, or ""
// on error. Used for non-fatal display in `status` / `info`.
func currentKubectlContext(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "kubectl", "config", "current-context").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// resumeExistingDevCluster handles `forge cluster up` against a cluster that
// already exists: start it if stopped, then pin the kubectl context (so the
// kubectl calls that follow cannot land on a stale one) and optionally wait
// for nodes to report Ready. Creation is the caller's job.
func resumeExistingDevCluster(ctx context.Context, clusterName string, state k3dClusterRuntimeState, wait bool) error {
	if state.Running {
		fmt.Printf("k3d cluster %q already exists and is running — no-op\n", clusterName)
	} else {
		fmt.Printf("k3d cluster %q already exists but is stopped — starting...\n", clusterName)
		if err := startK3dCluster(ctx, clusterName); err != nil {
			return err
		}
	}
	if err := pinKubectlContextFn(ctx, clusterName); err != nil {
		return err
	}
	if wait {
		fmt.Println("Waiting for cluster nodes to report Ready...")
		if err := waitNodeReady(ctx, clusterName); err != nil {
			return fmt.Errorf("wait for cluster nodes: %w", err)
		}
	}
	return nil
}

func runDevClusterUp(ctx context.Context, configPath string, wait bool) error {
	// Deploy-feature gate: `forge cluster up` boots a k3d cluster
	// whose only purpose is hosting the project's deploy. A library /
	// CLI project that's opted out of deploy has no reason to spin
	// one up. Mirrors the deploy gate in runDeploy / runDevClusterReload.
	if store, err := loadProjectStore(); err == nil && !store.Features().DeployEnabled() {
		return config.DisabledFeatureError(config.FeatureDeploy)
	}
	clusterName, err := resolveClusterName(configPath)
	if err != nil {
		return err
	}

	state, err := lookupClusterStateForUpFn(ctx, clusterName)
	if err != nil {
		return err
	}
	if state.Exists {
		return resumeExistingDevCluster(ctx, clusterName, state, wait)
	}

	// Resolve the effective k3d config. When features.ingress is on
	// and deploy/k3d-ports.yaml exists, merge the listener-port
	// fragment into the user's deploy/k3d.yaml (in memory) and pass
	// k3d a tempfile holding the merged YAML. See dev_cluster_ingress.go
	// for the merge policy.
	ingressOn := false
	if store, err := loadProjectStore(); err == nil {
		ingressOn = store.Features().IngressEnabled()
	}
	// `forge cluster up` reads deploy/k3d.yaml directly and has no rendered
	// Cluster entity in hand, so it projects only the ports fragment. A
	// project that declares CIDRs declares them on a forge.Cluster, which is
	// the declarative `forge env up` path (cluster_phase.go).
	effective, cleanupCfg, err := mergeK3dConfig(configPath, k3dConfigOverlay{HostPorts: ingressOn, PublicIssuer: true})
	if err != nil {
		return err
	}
	defer cleanupCfg()

	// Cluster doesn't exist — create from config if present, else use
	// the same fallback path forge env deploy dev has used historically.
	if _, statErr := os.Stat(effective.path); statErr == nil {
		fmt.Printf("Creating k3d cluster from %s...\n", configPath)
		if effective.temporary {
			fmt.Printf("  (merging deploy/k3d-ports.yaml from project's ingress KCL)\n")
		}
		// A `registries.use` reference names a STANDALONE registry that k3d
		// does NOT create: `cluster create` fails Cluster Preparation with
		// "Didn't find container for node '<name>'" and then rolls back. The
		// declarative env path already ensures these (cluster_phase.go:
		// ensureDeclaredCluster), so a project whose deploy/k3d.yaml uses a
		// standalone registry worked under `forge env up` but not under
		// `forge cluster up` — the same config, two different outcomes.
		// Read the UN-MERGED user config: the ports merge above rewrites
		// `ports`, never `registries`.
		if err := ensureConfigRegistries(ctx, configPath); err != nil {
			return fmt.Errorf("ensure standalone registry for cluster %q: %w", clusterName, err)
		}
		if err := createK3dCluster(ctx, clusterName,
			[]string{"cluster", "create", "--config", effective.path}); err != nil {
			return err
		}
	} else {
		// Reuse the existing ensureDevCluster path so the fallback
		// registries.yaml mirror config stays in one place.
		fmt.Printf("No %s found — falling back to forge default cluster shape...\n", configPath)
		if err := ensureDevCluster(ctx); err != nil {
			return err
		}
	}

	if err := pinKubectlContextFn(ctx, clusterName); err != nil {
		return err
	}

	if wait {
		fmt.Println("Waiting for cluster nodes to report Ready...")
		waitCmd := exec.CommandContext(ctx, "kubectl", "wait", "--for=condition=Ready",
			"nodes", "--all", "--timeout=120s")
		waitCmd.Stdout = os.Stdout
		waitCmd.Stderr = os.Stderr
		if err := waitCmd.Run(); err != nil {
			return fmt.Errorf("wait for cluster nodes: %w", err)
		}
	}

	// The Gateway API stack (Gateway API CRDs + Envoy Gateway controller +
	// the `eg` GatewayClass) is NOT installed imperatively here. It is a
	// DECLARED platform dependency — a forge.HelmChart in the env Bundle's
	// `helm_charts` — rendered + applied by the deploy phase
	// (`forge env deploy <env> --target=envoy-gateway`, CRD-first) EXACTLY like
	// the cloud envs. `forge cluster up` only creates the bare k3d cluster
	// with the host-port mapping the merged k3d config carries; the
	// controller arrives with the deploy. One declarative model everywhere.
	//
	// Still provision mkcert TLS Secrets for any dev Gateway that opted in
	// via tls.mode == "mkcert": those are project-owned Secrets the declared
	// Gateway references, independent of the controller install. No-op when
	// no mkcert gateways are declared (or no dev KCL).
	if ingressOn {
		projectDir, _ := os.Getwd()
		if err := provisionMkcertSecrets(ctx, projectDir); err != nil {
			return fmt.Errorf("provision mkcert TLS: %w", err)
		}
	}

	fmt.Printf("k3d cluster %q is up.\n", clusterName)
	return nil
}

func runDevClusterDown(ctx context.Context, configPath string) error {
	// Same gate as runDevClusterUp — tearing down a cluster that
	// `cluster up` won't create is at worst a no-op, but we keep the
	// error symmetric so a `forge cluster up && forge cluster
	// down` cycle fails uniformly when deploy is off.
	if store, err := loadProjectStore(); err == nil && !store.Features().DeployEnabled() {
		return config.DisabledFeatureError(config.FeatureDeploy)
	}
	clusterName, err := resolveClusterName(configPath)
	if err != nil {
		return err
	}

	// Through the same seams as the env-scoped path, so no unit test of this
	// command can ever reach a real `k3d cluster delete`.
	state, err := lookupClusterStateForUpFn(ctx, clusterName)
	if err != nil {
		return err
	}
	if !state.Exists {
		fmt.Printf("k3d cluster %q not found — no-op\n", clusterName)
		return nil
	}

	// A cluster declared with `owner` joins its owner's docker network and
	// inherits its registry mirror, and it has no config file of its own —
	// so --config can never name it, and deleting the owner alone strands it
	// on a network whose owner is gone. Discover those secondaries from the
	// live docker labels and delete them FIRST, the same order
	// `forge cluster down <env>` uses.
	dependents, err := nestedSecondariesOfFn(ctx, clusterName)
	if err != nil {
		return fmt.Errorf("find clusters nested on %q's network: %w", clusterName, err)
	}
	for _, dep := range dependents {
		fmt.Printf("Deleting k3d cluster %q (nested on %q's network — it cannot outlive it)...\n", dep, clusterName)
		if err := deleteK3dClusterFn(ctx, dep); err != nil {
			return err
		}
	}
	fmt.Printf("Deleting k3d cluster %q...\n", clusterName)
	return deleteK3dClusterFn(ctx, clusterName)
}

// nestedSecondariesOfFn names the k3d clusters whose nodes sit on owner's
// docker network (k3d-<owner>) but belong to a different cluster — the
// secondaries forge created with an `owner` reference. Seamed for tests.
var nestedSecondariesOfFn = nestedSecondariesOf

func nestedSecondariesOf(ctx context.Context, owner string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label=app=k3d",
		"--filter", "label=k3d.cluster.network=k3d-"+owner,
		"--format", `{{.Label "k3d.cluster"}}`)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w%s", err, formatCommandStderr(stderr.String()))
	}
	return parseNestedSecondaries(string(out), owner), nil
}

// parseNestedSecondaries dedupes the per-node cluster labels `docker ps`
// prints, dropping the owner itself and the empty label a standalone registry
// container carries (it is on the network but belongs to no cluster).
func parseNestedSecondaries(out, owner string) []string {
	seen := map[string]bool{}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || name == owner || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// renderEnvClustersFn renders an env's declared clusters. Seamed so the
// env-scoped lifecycle paths are unit-testable without a KCL toolchain.
var renderEnvClustersFn = func(ctx context.Context, env string) ([]ClusterEntity, string, error) {
	projectDir := projectDirForKCL()
	_, restore := activateDevStack(ctx, projectDir, env, renderDeclaration, claimNewBlocks)
	entities, err := RenderKCL(ctx, projectDir, env)
	restore() // a cluster lifecycle render must not drift resolve_port state
	if err != nil {
		return nil, "", fmt.Errorf("render env %q: %w", env, err)
	}
	if entities == nil || len(entities.Clusters) == 0 {
		return nil, "", fmt.Errorf("env %q declares no clusters (Bundle.clusters is empty) — nothing to manage; use --config for a bare k3d config file", env)
	}
	return entities.Clusters, projectDir, nil
}

// reconcileEnvClustersFn / deleteK3dClusterFn are the seams for the
// env-scoped paths' side effects.
var (
	reconcileEnvClustersFn = reconcileDeclaredClusters
	deleteK3dClusterFn     = func(ctx context.Context, name string) error {
		// `k3d cluster delete` removes the context from the default
		// kubeconfig — a whole-file rewrite like any other, so it takes the
		// kubeconfig lock (kubeconfig_lock.go).
		return withKubeconfigLock(func() error {
			del := exec.CommandContext(ctx, "k3d", "cluster", "delete", name)
			del.Stdout = os.Stdout
			del.Stderr = os.Stderr
			if err := del.Run(); err != nil {
				return fmt.Errorf("k3d cluster delete %s: %w", name, err)
			}
			return nil
		})
	}
)

// runEnvClustersUp is `forge cluster up <env>`: ensure every cluster the
// env's KCL declares through the SAME reconcile `forge env up` runs, so the
// declared CIDRs / API port / owner edge are applied at create time.
//
// This exists because `forge cluster up --config <file>` cannot express them:
// they live on the forge.Cluster, not in the k3d YAML, and k3s fixes CIDRs at
// create. A cluster created from the bare file therefore comes up on k3s's
// default blocks, and the env's next `forge env deploy` correctly refuses it on
// the CIDR drift guard — the create step and the deploy step disagreeing about
// a cluster both claim to own. CI creates clusters ahead of the deploy (so the
// kubectl steps in between have a target), which is exactly that split.
func runEnvClustersUp(ctx context.Context, env string, wait bool) error {
	if store, err := loadProjectStore(); err == nil && !store.Features().DeployEnabled() {
		return config.DisabledFeatureError(config.FeatureDeploy)
	}
	clusters, projectDir, err := renderEnvClustersFn(ctx, env)
	if err != nil {
		return err
	}
	if err := reconcileEnvClustersFn(ctx, clusters, projectDir, env); err != nil {
		return err
	}
	if wait {
		for _, c := range clusters {
			fmt.Printf("Waiting for cluster %q nodes to report Ready...\n", c.Name)
			if err := waitDeclaredClusterReadyFn(ctx, c.Name); err != nil {
				return fmt.Errorf("wait for cluster %q nodes: %w", c.Name, err)
			}
		}
	}
	// The env's DECLARED bootstrap charts (Envoy Gateway, cert-manager, CNPG,
	// and a Flux an env declares for its own reasons) land after the clusters
	// exist and, when asked for, after the node-Ready wait. Installing a chart
	// needs the apiserver reachable, which `--wait` is what guarantees;
	// without it, the chart apply's own kubectl is the thing that discovers a
	// node still coming up, and it reports that in its own terms. See
	// platform_charts.go.
	return ensureEnvPlatformChartsInstalledFn(ctx, env, clusters)
}

// ensureEnvPlatformChartsInstalledFn is the bootstrap-chart install, seamed so
// the cluster lifecycle paths stay unit-testable without helm or a cluster. It
// re-renders the env because runEnvClustersUp holds only the cluster list, and
// the install decision needs the env's declared charts too; the render is
// cached, so this is not a second evaluation.
var ensureEnvPlatformChartsInstalledFn = func(ctx context.Context, env string, clusters []ClusterEntity) error {
	entities, err := RenderKCL(ctx, projectDirForKCL(), env)
	if err != nil {
		// Soft: the clusters are up, which is what the verb promised.
		// A render that failed HERE cannot be a fact about the env —
		// renderEnvClustersFn just succeeded on the same render — so
		// refusing would fail a cluster-lifecycle command over a
		// transient.
		return nil
	}
	project := ""
	if store, serr := loadProjectStore(); serr == nil {
		project = store.Meta().Name
	}
	return ensureEnvPlatformChartsInstalled(ctx, env, project, entities, clusters)
}

// envClusterDeleteOrder orders an env's declared clusters for deletion:
// nested secondaries first, owners last. k3d cannot remove a docker network a
// secondary is still attached to, so deleting an owner first fails (or, worse,
// half-succeeds and strands the secondary on a network whose registry is gone).
func envClusterDeleteOrder(clusters []ClusterEntity) []string {
	var secondaries, owners []string
	for _, c := range clusters {
		if isNestedSecondary(c) {
			secondaries = append(secondaries, c.Name)
		} else {
			owners = append(owners, c.Name)
		}
	}
	return append(secondaries, owners...)
}

// runEnvClustersDown is `forge cluster down <env>`: delete every k3d
// cluster the env declares, secondaries first. A cluster declared with `owner`
// and no config file has no other way to be named through forge.
func runEnvClustersDown(ctx context.Context, env string) error {
	if store, err := loadProjectStore(); err == nil && !store.Features().DeployEnabled() {
		return config.DisabledFeatureError(config.FeatureDeploy)
	}
	clusters, _, err := renderEnvClustersFn(ctx, env)
	if err != nil {
		return err
	}
	for _, c := range clusters {
		if c.Provider != "" && c.Provider != "k3d" {
			return fmt.Errorf("env %q declares cluster %q with provider=%q; forge cluster down only deletes k3d clusters", env, c.Name, c.Provider)
		}
	}
	for _, name := range envClusterDeleteOrder(clusters) {
		state, err := lookupClusterStateForUpFn(ctx, name)
		if err != nil {
			return err
		}
		if !state.Exists {
			fmt.Printf("k3d cluster %q not found — no-op\n", name)
			continue
		}
		fmt.Printf("Deleting k3d cluster %q...\n", name)
		if err := deleteK3dClusterFn(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

// clusterStatusSummary is the data shape rendered by `cluster status`.
// Used for both human and --json output.
type clusterStatusSummary struct {
	Name        string `json:"name"`
	Exists      bool   `json:"exists"`
	Context     string `json:"kubectl_context"`
	Registry    string `json:"registry,omitempty"`
	APIPort     string `json:"api_port,omitempty"`
	ConfigPath  string `json:"config_path"`
	ConfigFound bool   `json:"config_found"`
}

func boolUpDown(b bool) string {
	if b {
		return "up"
	}
	return "down"
}

func foundOrMissing(b bool) string {
	if b {
		return "found"
	}
	return "missing"
}

// runDevClusterReload invokes the same KCL render + kubectl apply +
// wait-rollout code path forge env deploy dev uses, with cluster bootstrap
// and docker build/push skipped. Pins kubectl context first so a stale
// non-dev context can't leak the apply somewhere unintended.
func runDevClusterReload(ctx context.Context, configPath, imageTag, namespace string, dryRun bool) error {
	clusterName, err := resolveClusterName(configPath)
	if err != nil {
		return err
	}

	if !dryRun {
		if err := pinKubectlContextFn(ctx, clusterName); err != nil {
			return err
		}
	}

	store, err := loadProjectStore()
	if err != nil {
		return err
	}
	if !store.Features().DeployEnabled() {
		return config.DisabledFeatureError(config.FeatureDeploy)
	}

	kclDir := store.K8s().KCLDir
	if kclDir == "" {
		kclDir = "deploy/kcl"
	}
	envDir := filepath.Join(kclDir, "dev")
	mainK := filepath.Join(envDir, "main.k")
	if _, err := os.Stat(mainK); os.IsNotExist(err) {
		return fmt.Errorf("dev KCL not found: %s does not exist (run forge generate first?)", mainK)
	}

	if imageTag == "" {
		// Reload assumes the image is already in the cluster's
		// registry; default to the most recent SHA we have.
		tag, err := gitShortSHA(ctx)
		if err != nil {
			return fmt.Errorf("git rev-parse --short HEAD: %w (use --image-tag)", err)
		}
		imageTag = tag
	}

	if namespace == "" {
		if ns := k8sClusterNamespaceForEnv(ctx, "dev"); ns != "" {
			namespace = ns
		} else {
			namespace = store.Meta().Name + "-dev"
		}
	}

	fmt.Printf("Reloading dev manifests for cluster %q (namespace=%s, tag=%s)...\n",
		clusterName, namespace, imageTag)

	// Reload is a DIRECT cluster apply, so it carries the same notice the
	// deploy path does for an env that declares no lifecycle. A render
	// failure is not fatal here: the notice is informational, and reload
	// deliberately tolerates a partial read (see the skipped extras
	// below). The scaffolded dev env declares `lifecycle = "local"`, so
	// this is silent in the normal inner loop.
	if entities, rerr := RenderKCL(ctx, projectDirForKCL(), "dev"); rerr == nil && !DirectApplyAllowed(entities) {
		fmt.Println(lifecycleNoticeLine)
	}

	// Reload deliberately skips the deploy-time extras: no per-env
	// config projection (rebuilds defeat the inner-loop purpose), no
	// prune, no host-skip filter, no one-shot Job wait. Quiet=true
	// suppresses the section-header banners and matches the shorter
	// error wraps the pre-extraction reload used. The dry-run output
	// is unframed (raw manifests).
	return cluster.Apply(ctx, cluster.ApplyOpts{
		MainK:     mainK,
		ImageTag:  imageTag,
		Namespace: namespace,
		Env:       "dev", // dev cluster reload is dev-only (matches the envDir above)
		DryRun:    dryRun,
		Quiet:     true,
		// Reload is the INNER LOOP: a half-rolled cluster is something you
		// look at and iterate on, not a build result, and the previous
		// Deployment is still serving while the new one settles. So warn
		// rather than fail — the deploy/CI paths take the failing default.
		Rollout: cluster.RolloutPolicy{Mode: cluster.RolloutWarn},
	})
}
