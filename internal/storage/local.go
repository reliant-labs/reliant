package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/openfiles"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Runner executes maintenance using injectable process operations.
type Runner struct {
	Policy  Policy
	Out     io.Writer
	Command func(context.Context, string, ...string) ([]byte, error)
	// SourceCacheRoot overrides the cross-repo source cache the Sources
	// layer reclaims from. Empty means the real machine-local cache in
	// production, and is REFUSED under `go test` — see sources.go.
	SourceCacheRoot string
	// TempRoot overrides the system temp directory the TempSweep layer
	// reclaims from. Empty means os.TempDir() in production, and is
	// REFUSED under `go test` — see tempsweep.go.
	TempRoot string
	// PolicyPath is the policy file this pass was loaded from. Maintenance
	// state that must outlive the process — the marker recording that a
	// registry was stopped for GC — is kept beside it, so a later pass can
	// find and repair what an interrupted one left behind.
	PolicyPath string
	// Ctx bounds the host-filesystem layers (logs, temp sweep, source cache),
	// which take no context of their own. GC and NonDisruptiveGC set it from
	// theirs, so a pass's deadline bounds the whole pass — its lsof snapshot
	// and its walk over entries, not only its docker calls. Nil means
	// unbounded.
	Ctx context.Context

	// Explicit marks the one pass a person or agent invoked: `forge storage
	// gc` without --scheduled. Only it may remove what no observation can
	// prove abandoned — worktrees (and then only under the policy's
	// worktree_reap) and anonymous Docker volumes. Every automatic pass leaves
	// it false: the background auto-gc, the installed schedule and `forge
	// storage daemon`. An agent between two commands holds no process in its
	// worktree, and an orphaned volume may be the only copy of a database, so
	// to a pass nobody invoked either looks exactly like garbage.
	Explicit bool

	// afterQuarantine is a test seam run between the quarantine move and the
	// final classification, where a concurrent writer would land.
	afterQuarantine func(original, quarantined string)

	// removeFn and sleepFn are test seams for the deletion governor
	// (deletes.go): the unlink itself, and the pacing pause. Nil means
	// os.Remove and a real timer.
	removeFn func(string) error
	sleepFn  func(context.Context, time.Duration) error

	// Go-cache layer roots (gocache.go). Empty means the real machine
	// location in production and is REFUSED under `go test`.
	GoCacheRoot       string // shared GOCACHE
	GoModCacheRoot    string // shared GOMODCACHE
	GolangciCacheRoot string
	GoimportsRoot     string
	// ProcessEnv returns the text of every live process's environment; an error
	// makes the orphan layer skip. OpenPaths is the lsof snapshot. Both are
	// REFUSED under test when unset.
	ProcessEnv func(context.Context) (string, error)
	OpenPaths  func(context.Context) (openfiles.Snapshot, error)
	// OnGoCacheTrim, when set, receives what each Go-format cache trim
	// measured and did (gocache.go). The CLI collects them into the pass's
	// record. It may be called from the layer's goroutine; it is never called
	// concurrently.
	OnGoCacheTrim func(GoCacheTrim)

	// Per-pass state, set by beginPass (passctx.go).
	passTotal  time.Duration
	openShared *sharedOpen
	governor   deleter
}

// hostCtx is the context the host layers run under.
func (r Runner) hostCtx() context.Context {
	if r.Ctx != nil {
		return r.Ctx
	}
	return context.Background()
}

// Exec runs a command with a bounded lifetime and returns its STDOUT.
//
// Stderr is captured separately and only ever reported, in the error of a
// command that failed. Callers parse what Exec returns — kubectl and docker
// JSON — and a tool that succeeds while warning on stderr (kubectl's
// "Warning: v1 ComponentStatus is deprecated") would otherwise put the
// warning in front of the JSON. That made every protected-set scan fail to
// parse, so registry GC refused on every pass.
func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return nil, fmt.Errorf("%s %v: %w: %s", name, args, err, detail)
	}
	return stdout.Bytes(), nil
}

func (r Runner) command(ctx context.Context, name string, args ...string) ([]byte, error) {
	if r.Command != nil {
		return r.Command(ctx, name, args...)
	}
	return Exec(ctx, name, args...)
}
func (r Runner) docker(ctx context.Context, args ...string) ([]byte, error) {
	if r.Policy.DockerContext != "" {
		args = append([]string{"--context", r.Policy.DockerContext}, args...)
	}
	return r.command(ctx, "docker", args...)
}
func (r Runner) print(format string, args ...any) {
	if r.Out != nil {
		fmt.Fprintf(r.Out, format, args...)
	}
}

// Local refuses remote Docker endpoints.
func (r Runner) Local(ctx context.Context) error {
	// DOCKER_HOST overrides the selected context unless --context or
	// DOCKER_CONTEXT is explicit. context inspect alone would miss it.
	if r.Policy.DockerContext == "" && os.Getenv("DOCKER_CONTEXT") == "" {
		if host := os.Getenv("DOCKER_HOST"); host != "" && !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "npipe://") {
			return fmt.Errorf("storage maintenance refuses nonlocal DOCKER_HOST %q", host)
		}
	}
	b, err := r.docker(ctx, "context", "inspect")
	if err != nil {
		return err
	}
	var contexts []struct {
		Endpoints map[string]struct{ Host string }
	}
	if err := json.Unmarshal(b, &contexts); err != nil {
		return err
	}
	if len(contexts) != 1 {
		return fmt.Errorf("cannot establish Docker endpoint")
	}
	host := contexts[0].Endpoints["docker"].Host
	if !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "npipe://") {
		return fmt.Errorf("storage maintenance refuses nonlocal Docker endpoint %q", host)
	}
	return nil
}

// Status reports host, Docker, builder and kubelet storage usage.
func (r Runner) Status(ctx context.Context) error {
	disks, spaceErr := CheckSpace(r.Policy)
	for _, d := range disks {
		r.print("host %s: %.1f GiB available / %.1f GiB capacity\n", d.Path, float64(d.Available)/float64(GiB), float64(d.Capacity)/float64(GiB))
	}
	if spaceErr != nil {
		r.print("pressure: %v\n", spaceErr)
	}
	if err := r.Local(ctx); err != nil {
		return err
	}
	b, err := r.docker(ctx, "system", "df")
	if err != nil {
		return err
	}
	r.print("%s\n", b)
	r.goCacheStatus(ctx)
	for _, builder := range r.Policy.Builders {
		b, err = r.builderUsage(ctx, builder)
		if err != nil {
			return err
		}
		r.print("builder %s (budget %d GiB):\n%s\n", builder, r.Policy.BuildCacheGiB, b)
	}
	// One unreachable cluster is reported and skipped, not fatal: this is a
	// read-only report, and Policy.Clusters only accumulates (a cluster that
	// was deleted and recreated under a new name stays listed), so aborting
	// here hid every healthy cluster after a stale one. Nothing destructive
	// reads this list; ConfigureNodes, which restarts nodes, still aborts.
	for _, cluster := range r.Policy.Clusters {
		if err := r.clusterStatus(ctx, cluster); err != nil {
			r.print("cluster %s: unavailable (%v)\n", cluster, err)
		}
	}
	return nil
}

func (r Runner) clusterStatus(ctx context.Context, cluster string) error {
	b, err := r.command(ctx, "kubectl", "--context", cluster, "--request-timeout=15s", "get", "nodes", "-o", "json")
	if err != nil {
		return err
	}
	var nodes struct {
		Items []struct{ Metadata struct{ Name string } }
	}
	if err := json.Unmarshal(b, &nodes); err != nil {
		return err
	}
	for _, node := range nodes.Items {
		b, err = r.command(ctx, "kubectl", "--context", cluster, "--request-timeout=15s", "get", "--raw", "/api/v1/nodes/"+node.Metadata.Name+"/proxy/configz")
		if err != nil {
			return err
		}
		r.print("node %s GC configuration: %s\n", node.Metadata.Name, b)
	}
	return nil
}

// LockWaitPoll is how often WithLockWait retries a held lock.
var LockWaitPoll = 200 * time.Millisecond

// WithLock serializes policy updates and maintenance on this machine. It does
// not wait: a held lock is an immediate error, which is right for a pass that
// would only duplicate the holder's work.
func WithLock(policyPath string, fn func() error) error {
	return withLock(policyPath, 0, fn)
}

// WithLockWait is WithLock for a caller that must not be turned away by
// maintenance. It retries a held lock until wait elapses, then fails with the
// same "already running" error. The bound is the point: an unbounded wait would
// let a wedged maintenance pass hang a release cut forever, and no wait at all
// failed a cut — after a six-minute build — because a background pass happened
// to hold a lock for the few milliseconds the cut needs.
func WithLockWait(policyPath string, wait time.Duration, fn func() error) error {
	return withLock(policyPath, wait, fn)
}

func withLock(policyPath string, wait time.Duration, fn func() error) error {
	// Refused before the mkdir: the lock file would otherwise be created
	// beside the real machine policy even when every write inside is refused.
	if err := guardMachinePolicy(policyPath); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(policyPath), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(policyPath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	deadline := time.Now().Add(wait)
	for {
		err := lock(f)
		if err == nil {
			break
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("storage maintenance is already running: %w", err)
		}
		time.Sleep(LockWaitPoll)
	}
	return fn()
}

// GC previews or applies the registered cache and registry policy.
//
// Every layer runs even when an earlier one failed: a layer's error is
// recorded and the combined error is returned at the end. One unreadable
// registered project used to end the pass at the first layer, so the temp
// sweep, source eviction, BuildKit and registry retention silently never ran.
// Two checks still stop everything, because nothing after them is safe
// without them: an invalid policy, and a Docker endpoint that is not local
// (which ends the Docker layers, not the host ones already done).
func (r Runner) GC(ctx context.Context, apply bool) error {
	if err := r.Policy.Validate(); err != nil {
		return err
	}
	var failures []error
	// Stale clusters and projects are pruned first: a registered cluster
	// that no longer exists makes the registry layer's protected-set scan
	// fail on every pass (see prune.go). A preview prunes in memory only,
	// so it shows what an apply would do without writing anything.
	if next, changed := r.pruned(ctx); changed {
		r.Policy = next
		if apply && r.PolicyPath != "" {
			if err := Save(r.PolicyPath, next); err != nil {
				failures = append(failures, layerErr("policy prune", err))
			}
		}
	}
	r = r.beginPass(ctx)
	defer r.endPass()
	failures = append(failures, r.hostLayers(apply)...)
	if err := r.backedOffErr(); err != nil {
		r.print("docker layers: skipped, the pass backed off\n")
		return errors.Join(append(failures, layerErr("docker", err))...)
	}
	if err := r.Local(ctx); err != nil {
		return errors.Join(append(failures, layerErr("docker", err))...)
	}
	if err := r.LocalImages(ctx, apply); err != nil {
		failures = append(failures, layerErr("local-registry images", err))
	}
	// These two hand removals to the daemon and can observe its distress.
	// After a back-off nothing more is asked of it: every later layer forks
	// docker, and the builder and registry layers make it delete.
	for _, layer := range []struct {
		name string
		run  func() error
	}{
		{"dangling images", func() error { return r.DanglingImages(ctx, apply) }},
		{"anonymous volumes", func() error { return r.AnonymousVolumes(ctx, apply) }},
	} {
		if err := layer.run(); err != nil {
			failures = append(failures, layerErr(layer.name, err))
		}
		if err := r.backedOffErr(); err != nil {
			r.print("remaining docker layers: skipped, the pass backed off\n")
			return errors.Join(failures...)
		}
	}
	for _, builder := range r.Policy.Builders {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, layerErr("builder "+builder, err))...)
		}
		if err := r.builderGC(ctx, builder, apply); err != nil {
			failures = append(failures, layerErr("builder "+builder, err))
		}
	}
	for _, registry := range r.Policy.Registries {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, layerErr("registry "+registry.Container, err))...)
		}
		if err := r.RegistryGC(ctx, registry, apply); err != nil {
			failures = append(failures, layerErr("registry "+registry.Container, err))
		}
	}
	r.print("named volumes, volumes any container uses, running containers and application data are retained; anonymous volumes are removed only by an explicit `forge storage gc --apply`, worktrees only by one under the worktree_reap policy\n")
	return errors.Join(failures...)
}

// hostLayers runs the layers that reclaim from the host filesystem — rotated
// logs, the Go caches, the temp sweep, the source cache, worktrees — each
// independently, returning one error per failed layer. They share the pass's
// delete budget in this order, the Go caches taking at most their share.
func (r Runner) hostLayers(apply bool) []error {
	var failures []error
	add := func(err error) {
		if err != nil {
			failures = append(failures, err)
		}
	}
	add(r.runLayer("logs", shareLogs, func(s Runner) error { return s.Logs(apply) }))
	add(r.goCacheLayer(apply))
	add(r.runLayer("temp sweep", shareTemp, func(s Runner) error { return s.TempSweep(apply) }))
	add(r.runLayer("source cache", shareSources, func(s Runner) error { return s.Sources(apply) }))
	add(r.runLayer("worktrees", shareLast, func(s Runner) error { return s.worktreeLayer(apply) }))
	return failures
}

// goCacheLayer runs the Go caches in their slice. GoCaches reports each of its
// own steps as a layer, so its result is returned as is, not wrapped again.
func (r Runner) goCacheLayer(apply bool) error {
	if err := r.hostCtx().Err(); err != nil {
		return layerErr("go caches", &CutOffError{Err: err})
	}
	if err := r.backedOffErr(); err != nil {
		r.print("go caches: skipped, the pass backed off\n")
		return layerErr("go caches", err)
	}
	sub, done := r.slice(shareGoCaches)
	defer done()
	sub.governor = sub.deletes().share(shareGoCacheDeletes)
	return sub.GoCaches(apply)
}

// NodeConfigPath is stable across command exits; k3d bind mounts must never
// refer to a temporary file removed at the end of cluster creation.
func NodeConfigPath(policyPath string, p Policy) (string, error) {
	if err := guardMachinePolicy(policyPath); err != nil {
		return "", err
	}
	path := filepath.Join(filepath.Dir(policyPath), "kubelet-storage.conf")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	return path, atomicfile.WriteFile(path, p.KubeletConfig(), 0644)
}
