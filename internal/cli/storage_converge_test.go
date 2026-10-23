package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/storage"
	"github.com/reliant-labs/forge/pkg/release"
)

// stubConvergeStorage captures the facts a touch point derived instead of
// writing the machine policy.
func stubConvergeStorage(t *testing.T) *[]storage.Facts {
	t.Helper()
	var got []storage.Facts
	orig := convergeStorageFn
	convergeStorageFn = func(f storage.Facts) { got = append(got, f) }
	t.Cleanup(func() { convergeStorageFn = orig })
	return &got
}

// TestRegistryFactsFromConfig pins alias extraction: EVERY `<host>:<port>` key
// in the inline containerd mirror block is an alias of the registry, plus the
// in-network `<container>:5000` that pods pull by. Retention attributes a
// repository to a registry by matching this set, so a name missing here makes
// images pushed under it look unowned and eligible for deletion.
func TestRegistryFactsFromConfig(t *testing.T) {
	container, aliases, err := registryFactsFromConfig([]byte(k3dUseConfig))
	if err != nil {
		t.Fatalf("registryFactsFromConfig: %v", err)
	}
	if container != "k3d-control-plane-registry" {
		t.Errorf("container = %q; want k3d-control-plane-registry", container)
	}
	for _, want := range []string{
		"localhost:5051",                  // the host push name
		"registry.localhost:5000",         // the in-cluster mirror alias
		"k3d-control-plane-registry:5000", // the in-network container name
	} {
		if !slices.Contains(aliases, want) {
			t.Errorf("aliases %v missing %q", aliases, want)
		}
	}
	// Deterministic across runs: Converge's idempotency depends on the alias
	// order not coming from Go's map iteration.
	for i := 0; i < 5; i++ {
		_, again, err := registryFactsFromConfig([]byte(k3dUseConfig))
		if err != nil {
			t.Fatalf("repeat registryFactsFromConfig: %v", err)
		}
		if !slices.Equal(again, aliases) {
			t.Fatalf("alias order is not stable: %v then %v", aliases, again)
		}
	}
}

// TestRegistryFactsFromConfig_CreateIsNoop confirms a cluster-OWNED registry
// (`registries.create`) yields no facts. forge converges retention only for a
// standalone registry whose lifecycle it owns.
func TestRegistryFactsFromConfig_CreateIsNoop(t *testing.T) {
	container, aliases, err := registryFactsFromConfig([]byte(k3dCreateConfig))
	if err != nil {
		t.Fatalf("registryFactsFromConfig: %v", err)
	}
	if container != "" || len(aliases) != 0 {
		t.Fatalf("got container=%q aliases=%v; want nothing for a cluster-owned registry", container, aliases)
	}
}

// TestEnsureDeclaredCluster_ConvergesStorageFacts is the activation claim at
// the cluster touch point: the contexts the env declares and the registry its
// config names reach the policy as a side effect of the cluster phase, with no
// `forge storage register` anywhere.
func TestEnsureDeclaredCluster_ConvergesStorageFacts(t *testing.T) {
	origState := clusterRuntimeStateFn
	origCreate := createDeclaredClusterFn
	origHostDNS := ensureClusterHostGatewayDNSFn
	origRegExists := registryExistsFn
	origRegCreate := registryCreateFn
	t.Cleanup(func() {
		clusterRuntimeStateFn = origState
		createDeclaredClusterFn = origCreate
		ensureClusterHostGatewayDNSFn = origHostDNS
		registryExistsFn = origRegExists
		registryCreateFn = origRegCreate
	})
	clusterRuntimeStateFn = func(context.Context, string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{}, nil
	}
	createDeclaredClusterFn = func(context.Context, string, []string) error { return nil }
	ensureClusterHostGatewayDNSFn = func(context.Context, string) error { return nil }
	registryExistsFn = func(context.Context, string) (bool, error) { return true, nil }
	registryCreateFn = func(context.Context, k3dRegistryRef) error { return nil }

	project := t.TempDir()
	configPath := filepath.Join(project, "k3d.yaml")
	if err := os.WriteFile(configPath, []byte(k3dUseConfig), 0o644); err != nil {
		t.Fatalf("write k3d.yaml: %v", err)
	}

	got := stubConvergeStorage(t)
	declared := []ClusterEntity{
		{Name: "control-plane", Context: "k3d-control-plane", Config: configPath},
		{Name: "cp-daemon", Context: "k3d-cp-daemon"},
	}
	if err := ensureDeclaredCluster(t.Context(), declared[0], declared, project, "dev"); err != nil {
		t.Fatalf("ensureDeclaredCluster: %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("converged %d time(s); want exactly 1", len(*got))
	}
	f := (*got)[0]
	if f.Project != project {
		t.Errorf("Project = %q; want %q", f.Project, project)
	}
	if f.Registry != "k3d-control-plane-registry" {
		t.Errorf("Registry = %q; want the container from registries.use", f.Registry)
	}
	if !slices.Contains(f.Aliases, "localhost:5051") {
		t.Errorf("Aliases = %v; want the mirror's host push name", f.Aliases)
	}
	// BOTH declared contexts, not just the one being ensured: retention
	// protects an image when ANY registered cluster references it.
	for _, want := range []string{"k3d-control-plane", "k3d-cp-daemon"} {
		if !slices.Contains(f.Contexts, want) {
			t.Errorf("Contexts = %v; missing %q", f.Contexts, want)
		}
	}
}

// TestEnsureDeclaredCluster_ConvergesOnWarmCluster pins that a cluster which
// already exists converges too. Activation that fired only on a cold create
// would never run on the long-lived clusters that actually have the disk
// problem.
func TestEnsureDeclaredCluster_ConvergesOnWarmCluster(t *testing.T) {
	origState := clusterRuntimeStateFn
	origLB := ensureClusterLBFreshFn
	origHealthy := ensureRunningClusterHealthyFn
	origHostDNS := ensureClusterHostGatewayDNSFn
	t.Cleanup(func() {
		clusterRuntimeStateFn = origState
		ensureClusterLBFreshFn = origLB
		ensureRunningClusterHealthyFn = origHealthy
		ensureClusterHostGatewayDNSFn = origHostDNS
	})
	clusterRuntimeStateFn = func(context.Context, string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true, Running: true}, nil
	}
	ensureClusterLBFreshFn = func(context.Context, string) error { return nil }
	ensureRunningClusterHealthyFn = func(context.Context, ClusterEntity) error { return nil }
	ensureClusterHostGatewayDNSFn = func(context.Context, string) error { return nil }

	got := stubConvergeStorage(t)
	c := ClusterEntity{Name: "control-plane", Context: "k3d-control-plane"}
	if err := ensureDeclaredCluster(t.Context(), c, []ClusterEntity{c}, t.TempDir(), "dev"); err != nil {
		t.Fatalf("ensureDeclaredCluster: %v", err)
	}
	if len(*got) != 1 || !slices.Contains((*got)[0].Contexts, "k3d-control-plane") {
		t.Fatalf("warm cluster converged %+v; want the declared context", *got)
	}
}

// TestRegisterBuildStorage_ConvergesLocalRepositories pins the build touch
// point: local push destinations become repositories + aliases, a REMOTE
// registry never does, and the registry container is read from the cluster's
// k3d config.
func TestRegisterBuildStorage_ConvergesLocalRepositories(t *testing.T) {
	project := t.TempDir()
	configPath := filepath.Join(project, "k3d.yaml")
	if err := os.WriteFile(configPath, []byte(k3dUseConfig), 0o644); err != nil {
		t.Fatalf("write k3d.yaml: %v", err)
	}
	got := stubConvergeStorage(t)
	entities := &KCLEntities{Clusters: []ClusterEntity{
		{Name: "control-plane", Context: "k3d-control-plane", Config: configPath},
	}}
	plan := pushPlan{push: true, destinations: []imageDestination{
		{repository: "localhost:5051/admin-server", workload: "admin-server"},
		{repository: "ghcr.io/acme/api", workload: "api"},
	}}
	registerBuildStorage(t.Context(), project, entities, plan)

	if len(*got) != 1 {
		t.Fatalf("converged %d time(s); want 1", len(*got))
	}
	f := (*got)[0]
	if !slices.Equal(f.Repositories, []string{"localhost:5051/admin-server"}) {
		t.Errorf("Repositories = %v; want only the local destination (never a remote registry)", f.Repositories)
	}
	if !slices.Contains(f.Aliases, "localhost:5051") {
		t.Errorf("Aliases = %v; want the host forge just pushed to", f.Aliases)
	}
	if f.Registry != "k3d-control-plane-registry" {
		t.Errorf("Registry = %q; want the container from the cluster's k3d config", f.Registry)
	}
	if f.Project != project {
		t.Errorf("Project = %q; want %q", f.Project, project)
	}
}

// TestFallbackDevClusterCreateGetsKubeletStorageArgs is the G4 gap. The
// `forge env deploy` bootstrap built its own argv and so was the one
// forge-created-cluster path shipping a node with unbounded containerd image
// growth. BOTH its branches must carry the kubelet drop-in mount.
func TestFallbackDevClusterCreateGetsKubeletStorageArgs(t *testing.T) {
	orig := addClusterStorageArgsFn
	t.Cleanup(func() { addClusterStorageArgsFn = orig })
	const mount = "/forge/kubelet-storage.conf:/var/lib/rancher/k3s/agent/etc/kubelet.conf.d/90-forge-storage.conf:ro@all"
	addClusterStorageArgsFn = func(args []string) ([]string, error) {
		return append(args, "--volume", mount), nil
	}

	for name, withConfig := range map[string]bool{
		"with deploy/k3d.yaml":    true,
		"without deploy/k3d.yaml": false,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			if withConfig {
				if err := os.MkdirAll(filepath.Join(dir, "deploy"), 0o755); err != nil {
					t.Fatalf("mkdir deploy: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, "deploy", "k3d.yaml"),
					[]byte(k3dUseConfig), 0o644); err != nil {
					t.Fatalf("write deploy/k3d.yaml: %v", err)
				}
			}
			args, cleanup, err := devClusterCreateArgs()
			if err != nil {
				t.Fatalf("devClusterCreateArgs: %v", err)
			}
			defer cleanup()
			if !slices.Contains(args, mount) {
				t.Fatalf("argv %v is missing the kubelet storage mount — this cluster's "+
					"containerd image store would grow without bound", args)
			}
			if args[0] != "cluster" || args[1] != "create" {
				t.Fatalf("argv %v does not start with `cluster create`", args)
			}
		})
	}
}

// TestK3dRegistryCreateArgs_DeleteEnabled pins the G4 registry flag.
// distribution refuses DELETE by default, so without --delete-enabled
// retention cannot reclaim a manifest through the API and has to restart the
// container behind a delete-enabled config — a write outage during every pass.
func TestK3dRegistryCreateArgs_DeleteEnabled(t *testing.T) {
	args := k3dRegistryCreateArgs(k3dRegistryRef{Name: "k3d-cp-registry", HostPort: 5051})
	if !slices.Contains(args, "--delete-enabled") {
		t.Fatalf("argv %v is missing --delete-enabled; registry GC would need a container restart", args)
	}
	// The pre-existing contract still holds: k3d adds its own `k3d-` prefix.
	if !slices.Contains(args, "cp-registry") || slices.Contains(args, "k3d-cp-registry") {
		t.Errorf("argv %v should pass the UNPREFIXED name (k3d adds `k3d-`)", args)
	}
	if !slices.Contains(args, "0.0.0.0:5051") {
		t.Errorf("argv %v lost the host port binding", args)
	}
	// No host port declared: k3d picks one, so --port is omitted entirely.
	noPort := k3dRegistryCreateArgs(k3dRegistryRef{Name: "k3d-cp-registry"})
	if slices.Contains(noPort, "--port") {
		t.Errorf("argv %v passed --port with no declared host port", noPort)
	}
	if !slices.Contains(noPort, "--delete-enabled") {
		t.Errorf("argv %v is missing --delete-enabled", noPort)
	}
}

// storagePolicyForOpportunisticGC points FORGE_STORAGE_POLICY at a temp policy
// with one registered registry, stubs the pass itself (the real one shells out
// to docker), and returns the policy path.
func storagePolicyForOpportunisticGC(t *testing.T) string {
	t.Helper()
	origGC := nonDisruptiveGCFn
	nonDisruptiveGCFn = func(context.Context, storage.Runner) error { return nil }
	t.Cleanup(func() { nonDisruptiveGCFn = origGC })
	// The background launch runs the pass in-process here, so the gate and
	// the pass body are exercised together without spawning anything.
	origStart := startAutoGCFn
	startAutoGCFn = func(path string) (string, error) {
		_ = runAutoGC(context.Background(), path, io.Discard)
		return "/fake/auto-gc.log", nil
	}
	t.Cleanup(func() { startAutoGCFn = origStart })
	path := filepath.Join(t.TempDir(), "storage.json")
	policy := storage.DefaultPolicy()
	policy.Registries = []storage.Registry{{
		Container: "k3d-cp-registry", Repositories: []string{"admin-server"},
		Aliases: []string{"localhost:5051"}, Contexts: []string{"k3d-control-plane"},
	}}
	if err := storage.Save(path, policy); err != nil {
		t.Fatalf("save policy: %v", err)
	}
	// Registry retention is healthy unless a test says otherwise, so these
	// tests see only the opportunistic gate they are about.
	if err := storage.RecordFullGC(path, storage.GCResult{At: time.Now(), OK: true}); err != nil {
		t.Fatalf("record full GC: %v", err)
	}
	t.Setenv("FORGE_STORAGE_POLICY", path)
	return path
}

// TestOpportunisticGC_SkipsWhenRecent is the gating claim: a machine whose
// maintenance ran within the interval does no work at the end of `forge env
// up`. Without the gate, every `up` would pay a builder prune.
func TestOpportunisticGC_SkipsWhenRecent(t *testing.T) {
	path := storagePolicyForOpportunisticGC(t)
	origSchedule := storageScheduleInstalledFn
	storageScheduleInstalledFn = func() bool { return true }
	t.Cleanup(func() { storageScheduleInstalledFn = origSchedule })

	if err := storage.RecordAutoGC(path, storage.GCResult{At: time.Now().Add(-time.Hour), OK: true}); err != nil {
		t.Fatalf("RecordAutoGC: %v", err)
	}
	var out bytes.Buffer
	maybeOpportunisticGC(t.Context(), &out)
	if out.Len() != 0 {
		t.Fatalf("a recent pass still did work:\n%s", out.String())
	}
}

// TestOpportunisticGC_RunsWhenStale pins the other side of the gate, and that
// the pass is non-disruptive: it announces the prune, it does NOT touch the
// registry, and it records a fresh completion timestamp so the next `up`
// skips.
func TestOpportunisticGC_RunsWhenStale(t *testing.T) {
	path := storagePolicyForOpportunisticGC(t)
	origSchedule := storageScheduleInstalledFn
	storageScheduleInstalledFn = func() bool { return true }
	t.Cleanup(func() { storageScheduleInstalledFn = origSchedule })

	stale := time.Now().Add(-48 * time.Hour)
	if err := storage.RecordAutoGC(path, storage.GCResult{At: stale, OK: true}); err != nil {
		t.Fatalf("RecordAutoGC: %v", err)
	}
	var out bytes.Buffer
	maybeOpportunisticGC(t.Context(), &out)
	text := out.String()
	if !bytes.Contains(out.Bytes(), []byte("in the background")) {
		t.Fatalf("a stale pass did not start:\n%s", text)
	}
	// The pass must never mention registry work: that is the layer that takes
	// the registry offline, and it is explicitly excluded.
	if bytes.Contains(out.Bytes(), []byte("k3d-cp-registry")) {
		t.Fatalf("the opportunistic pass touched the registry:\n%s", text)
	}
	// The stamp advances whether or not docker was reachable in this
	// environment; a pass that could not advance it would retry on every up.
	if got, _ := storage.LastAutoGC(path); !got.At.After(stale) {
		t.Fatalf("last attempt = %v; want it advanced past %v", got.At, stale)
	}
}

// TestOpportunisticGC_NoticesMissingSchedule pins the one-line notice. The
// registry layer reclaims the most and is the one the opportunistic pass will
// never run, so a machine with registered registries genuinely needs the
// LaunchAgent — and nothing else tells the user that.
func TestOpportunisticGC_NoticesMissingSchedule(t *testing.T) {
	path := storagePolicyForOpportunisticGC(t)
	origSchedule := storageScheduleInstalledFn
	storageScheduleInstalledFn = func() bool { return false }
	t.Cleanup(func() { storageScheduleInstalledFn = origSchedule })
	if err := storage.RecordAutoGC(path, storage.GCResult{At: time.Now(), OK: true}); err != nil {
		t.Fatalf("RecordAutoGC: %v", err)
	}

	var out bytes.Buffer
	maybeOpportunisticGC(t.Context(), &out)
	if !bytes.Contains(out.Bytes(), []byte("forge storage install")) {
		t.Fatalf("no notice naming the install command:\n%s", out.String())
	}

	// Installed -> silent.
	storageScheduleInstalledFn = func() bool { return true }
	out.Reset()
	maybeOpportunisticGC(t.Context(), &out)
	if out.Len() != 0 {
		t.Fatalf("notice printed with a schedule installed:\n%s", out.String())
	}
}

// TestOpportunisticGC_AdvancesStampOnFailure pins the rate limit. A machine
// where the pass reliably fails (no Docker daemon, a vanished builder) must not
// re-attempt and re-print on EVERY `forge env up` — the interval limits
// ATTEMPTS, not successes.
func TestOpportunisticGC_AdvancesStampOnFailure(t *testing.T) {
	path := storagePolicyForOpportunisticGC(t)
	origSchedule := storageScheduleInstalledFn
	storageScheduleInstalledFn = func() bool { return true }
	t.Cleanup(func() { storageScheduleInstalledFn = origSchedule })
	nonDisruptiveGCFn = func(context.Context, storage.Runner) error {
		return errors.New("docker daemon is not reachable")
	}

	stale := time.Now().Add(-48 * time.Hour)
	if err := storage.RecordAutoGC(path, storage.GCResult{At: stale, OK: true}); err != nil {
		t.Fatalf("RecordAutoGC: %v", err)
	}
	var out bytes.Buffer
	maybeOpportunisticGC(t.Context(), &out)
	if attempt, _ := storage.LastAutoGC(path); attempt.OK || !strings.Contains(attempt.Error, "docker daemon is not reachable") {
		t.Fatalf("the failing pass was not recorded as a failure: %+v", attempt)
	}
	if got, _ := storage.LastAutoGC(path); !got.At.After(stale) {
		t.Fatalf("last attempt = %v; a failed pass must still rate-limit the next attempt", got.At)
	}
	// Immediately after, the gate is closed again.
	out.Reset()
	maybeOpportunisticGC(t.Context(), &out)
	if out.Len() != 0 {
		t.Fatalf("the failing pass re-attempted on the next run:\n%s", out.String())
	}
}

// TestOpportunisticGC_SilentWithoutRegistries pins that a project with no
// registered registry gets no notice: there is nothing for the schedule to
// reclaim, so naming the command would be noise.
func TestOpportunisticGC_SilentWithoutRegistries(t *testing.T) {
	origGC := nonDisruptiveGCFn
	nonDisruptiveGCFn = func(context.Context, storage.Runner) error { return nil }
	t.Cleanup(func() { nonDisruptiveGCFn = origGC })
	path := filepath.Join(t.TempDir(), "storage.json")
	if err := storage.Save(path, storage.DefaultPolicy()); err != nil {
		t.Fatalf("save policy: %v", err)
	}
	t.Setenv("FORGE_STORAGE_POLICY", path)
	origSchedule := storageScheduleInstalledFn
	storageScheduleInstalledFn = func() bool { return false }
	t.Cleanup(func() { storageScheduleInstalledFn = origSchedule })
	if err := storage.RecordAutoGC(path, storage.GCResult{At: time.Now(), OK: true}); err != nil {
		t.Fatalf("RecordAutoGC: %v", err)
	}

	var out bytes.Buffer
	maybeOpportunisticGC(t.Context(), &out)
	if out.Len() != 0 {
		t.Fatalf("notice printed with no registries registered:\n%s", out.String())
	}
}

// A release cut must not fail because a maintenance pass holds the policy lock
// for a moment. This failed a real prod cut AFTER a six-minute build
// ("storage maintenance is already running"). The cut waits, bounded, and the
// pin still lands before the ledger entry is published.
func TestPinStorageRelease_WaitsForARunningMaintenancePass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage.json")
	p := storage.DefaultPolicy()
	p.Registries = []storage.Registry{{Container: "k3d-registry", Repositories: []string{"app"}, Aliases: []string{"k3d-registry:5000"}, Contexts: []string{"k3d-demo"}}}
	if err := storage.Save(path, p); err != nil {
		t.Fatalf("save policy: %v", err)
	}
	t.Setenv("FORGE_STORAGE_POLICY", path)

	prevPoll, prevWait := storage.LockWaitPoll, releasePinLockWait
	storage.LockWaitPoll, releasePinLockWait = 10*time.Millisecond, 5*time.Second
	t.Cleanup(func() { storage.LockWaitPoll, releasePinLockWait = prevPoll, prevWait })

	held, free := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- storage.WithLock(path, func() error { close(held); <-free; return nil })
	}()
	<-held
	go func() { time.Sleep(100 * time.Millisecond); close(free) }()

	digest := "sha256:" + strings.Repeat("a", 64)
	err := pinStorageRelease(release.Release{Version: "v1", Artifacts: map[string]release.Artifact{
		"app": {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: digest}},
	}})
	if err != nil {
		t.Fatalf("the cut was refused because maintenance held the lock briefly: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := storage.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	pinned := false
	for _, pin := range got.Pins {
		pinned = pinned || pin == digest
	}
	if !pinned {
		t.Errorf("the release's digest was not pinned after waiting: pins = %v", got.Pins)
	}
}
