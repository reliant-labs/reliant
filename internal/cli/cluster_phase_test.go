package cli

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const sampleClustersJSON = `{
  "clusters": [
    {"name": "cp", "context": "k3d-cp", "image": "rancher/k3s:v1.36.3-k3s1", "registry_inherit": false, "servers": 1, "agents": 0, "api_port": 6443},
    {"name": "workload", "context": "k3d-workload", "image": "rancher/k3s:v1.36.3-k3s1", "network": "k3d-cp", "registry_inherit": true, "servers": 1, "agents": 2, "api_port": 6444},
    {"name": "configured", "context": "k3d-configured", "config": "deploy/k3d.workload.yaml", "servers": 1, "agents": 0}
  ]
}`

// TestParseKCLEntities_Clusters pins that the declared clusters block
// parses into ClusterEntity in order, with the derived-ownership fields
// (context / network / registry_inherit) and api_port preserved.
func TestParseKCLEntities_Clusters(t *testing.T) {
	entities, err := parseKCLEntities([]byte(sampleClustersJSON))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	if len(entities.Clusters) != 3 {
		t.Fatalf("clusters: got %d want 3", len(entities.Clusters))
	}

	owner := entities.Clusters[0]
	if owner.Name != "cp" {
		t.Errorf("clusters[0].Name = %q want cp", owner.Name)
	}
	if owner.Context != "k3d-cp" {
		t.Errorf("clusters[0].Context = %q want k3d-cp", owner.Context)
	}
	if owner.Image != "rancher/k3s:v1.36.3-k3s1" {
		t.Errorf("clusters[0].Image = %q want pinned k3s image", owner.Image)
	}
	if owner.Network != "" || owner.RegistryInherit {
		t.Errorf("owner cluster must derive no network/registry_inherit; got network=%q inherit=%v",
			owner.Network, owner.RegistryInherit)
	}
	if owner.APIPort != 6443 {
		t.Errorf("clusters[0].APIPort = %d want 6443", owner.APIPort)
	}

	sec := entities.Clusters[1]
	if sec.Network != "k3d-cp" {
		t.Errorf("secondary.Network = %q want k3d-cp (derived from owner)", sec.Network)
	}
	if !sec.RegistryInherit {
		t.Errorf("secondary.RegistryInherit = %v want true", sec.RegistryInherit)
	}
	if sec.Agents != 2 {
		t.Errorf("secondary.Agents = %d want 2", sec.Agents)
	}

	cfg := entities.Clusters[2]
	if cfg.Config != "deploy/k3d.workload.yaml" {
		t.Errorf("clusters[2].Config = %q want deploy/k3d.workload.yaml", cfg.Config)
	}
}

// TestParseKCLEntities_NoClusters confirms an env that declares no
// clusters parses to an empty list (no-op reconcile) — preserving the
// no-ensure behavior for single-cluster / cluster-less envs.
func TestParseKCLEntities_NoClusters(t *testing.T) {
	entities, _ := loadContract(t, "compose")
	if len(entities.Clusters) != 0 {
		t.Errorf("clusters: got %d want 0", len(entities.Clusters))
	}
}

// TestReconcileDeclaredClusters_EmptyIsNoop guards the fast path: a nil
// cluster list never shells out to k3d, so it's safe to call
// unconditionally at the head of every `forge env up` / `forge env deploy`.
func TestReconcileDeclaredClusters_EmptyIsNoop(t *testing.T) {
	if err := reconcileDeclaredClusters(t.Context(), nil, "", ""); err != nil {
		t.Errorf("empty reconcile should be a no-op, got %v", err)
	}
	if err := reconcileDeclaredClusters(t.Context(), []ClusterEntity{}, "", ""); err != nil {
		t.Errorf("empty-slice reconcile should be a no-op, got %v", err)
	}
}

// withFreshLoadBalancers stubs the k3d load-balancer freshness check, which
// shells out to `docker inspect` on every existing cluster before its health
// probe. Reconcile tests that stub the k3d/kubectl seams must stub this one
// too, or they pass only where a Docker daemon happens to answer.
func withFreshLoadBalancers(t *testing.T) {
	t.Helper()
	orig := ensureClusterLBFreshFn
	t.Cleanup(func() { ensureClusterLBFreshFn = orig })
	ensureClusterLBFreshFn = func(context.Context, string) error { return nil }
}

// TestReconcileDeclaredClusters_NoImperativeIngress guards the
// declarative-ingress invariant: reconcile NEVER installs the Gateway API
// stack imperatively, even for a cluster that still carries the legacy
// `Ingress` flag. Ingress is a DECLARED forge.HelmChart applied by the
// deploy phase (`forge env deploy <env> --target=envoy-gateway`), so the
// per-cluster ingress-install seam is gone entirely. A warm reconcile of an
// already-existing cluster is a pure no-op beyond the secondary-node setup.
// The cluster-state seam is stubbed so the test never touches k3d/kubectl;
// the absence of any ingress shell-out is the assertion (the test would not
// compile if an installClusterIngressFn seam were still referenced).
func TestReconcileDeclaredClusters_NoImperativeIngress(t *testing.T) {
	withReachableDocker(t)
	withFreshLoadBalancers(t)
	origState := clusterRuntimeStateFn
	origHealth := ensureRunningClusterHealthyFn
	origHostDNS := ensureClusterHostGatewayDNSFn
	origSetup := setupSecondaryClusterNodeFn
	t.Cleanup(func() {
		clusterRuntimeStateFn = origState
		ensureRunningClusterHealthyFn = origHealth
		ensureClusterHostGatewayDNSFn = origHostDNS
		setupSecondaryClusterNodeFn = origSetup
	})

	clusterRuntimeStateFn = func(_ context.Context, _ string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true, Running: true}, nil
	}
	ensureRunningClusterHealthyFn = func(_ context.Context, _ ClusterEntity) error { return nil }
	ensureClusterHostGatewayDNSFn = func(_ context.Context, _ string) error { return nil }
	// A nested-secondary setup seam is the ONLY warm-path side effect that
	// remains; an owner cluster (no derived network/inherit) triggers none.
	setupSecondaryClusterNodeFn = func(_ context.Context, _ ClusterEntity) error { return nil }

	// `Ingress: true` is deliberately set to prove it is now inert — no
	// install is triggered. A warm reconcile must succeed as a no-op.
	clusters := []ClusterEntity{
		{Name: "control-plane", Ingress: true, HostPorts: true},
		{Name: "cp-daemon", Ingress: false},
	}
	if err := reconcileDeclaredClusters(t.Context(), clusters, "proj", "e2e"); err != nil {
		t.Fatalf("reconcileDeclaredClusters: %v", err)
	}
}

// TestIsNestedSecondary pins the gate that identifies a secondary cluster
// nested on an owner's docker network: an `owner` reference, which the
// render layer projects as RegistryInherit=true + a derived Network. An
// owner cluster derives neither; a malformed half-projection (only one of
// the two) is not treated as nested.
func TestIsNestedSecondary(t *testing.T) {
	cases := []struct {
		name string
		c    ClusterEntity
		want bool
	}{
		{"owner (neither)", ClusterEntity{Name: "cp"}, false},
		{"nested secondary", ClusterEntity{Name: "wl", Network: "k3d-cp", RegistryInherit: true}, true},
		{"network only", ClusterEntity{Name: "wl", Network: "k3d-cp"}, false},
		{"inherit only", ClusterEntity{Name: "wl", RegistryInherit: true}, false},
	}
	for _, tc := range cases {
		if got := isNestedSecondary(tc.c); got != tc.want {
			t.Errorf("%s: isNestedSecondary = %v want %v", tc.name, got, tc.want)
		}
	}
}

// TestReconcileDeclaredClusters_SecondarySetup asserts the secondary-node
// setup is invoked for EXACTLY the nested-secondary clusters (derived
// network + registry_inherit) and skipped for standalone owners. All
// shell-out seams are stubbed so the test never touches k3d/docker/kubectl;
// clusters report as already existing so the create branch is never
// reached (the warm path also gates the secondary setup on the same
// predicate, so this exercises that path).
func TestReconcileDeclaredClusters_SecondarySetup(t *testing.T) {
	withReachableDocker(t)
	withFreshLoadBalancers(t)
	origState := clusterRuntimeStateFn
	origHealth := ensureRunningClusterHealthyFn
	origHostDNS := ensureClusterHostGatewayDNSFn
	origSetup := setupSecondaryClusterNodeFn
	t.Cleanup(func() {
		clusterRuntimeStateFn = origState
		ensureRunningClusterHealthyFn = origHealth
		ensureClusterHostGatewayDNSFn = origHostDNS
		setupSecondaryClusterNodeFn = origSetup
	})

	clusterRuntimeStateFn = func(_ context.Context, _ string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true, Running: true}, nil
	}

	var events []string
	ensureRunningClusterHealthyFn = func(_ context.Context, c ClusterEntity) error {
		events = append(events, "health:"+c.Name)
		return nil
	}
	ensureClusterHostGatewayDNSFn = func(_ context.Context, name string) error {
		events = append(events, "host-dns:"+name)
		return nil
	}
	setupSecondaryClusterNodeFn = func(_ context.Context, c ClusterEntity) error {
		events = append(events, "setup:"+c.Name)
		return nil
	}

	clusters := []ClusterEntity{
		{Name: "control-plane"}, // standalone owner — no setup
		{Name: "cp-daemon", Network: "k3d-control-plane", RegistryInherit: true}, // nested — setup
	}
	if err := reconcileDeclaredClusters(t.Context(), clusters, "", ""); err != nil {
		t.Fatalf("reconcileDeclaredClusters: %v", err)
	}

	want := []string{
		"health:control-plane", "host-dns:control-plane",
		"health:cp-daemon", "host-dns:cp-daemon", "setup:cp-daemon",
	}
	if len(events) != len(want) {
		t.Fatalf("events = %v; want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v; want %v", events, want)
		}
	}
}

// TestReconcileDeclaredClusters_StartsStoppedInDeclarationOrder pins the
// stopped-cluster recovery path that `forge env up` needs after `k3d cluster
// stop`: start and wait for the owner before touching the nested secondary,
// then start and wait for the secondary before its DNS/MSS setup.
func TestReconcileDeclaredClusters_StartsStoppedInDeclarationOrder(t *testing.T) {
	withReachableDocker(t)
	withFreshLoadBalancers(t)
	origState := clusterRuntimeStateFn
	origStart := startDeclaredClusterFn
	origWait := waitDeclaredClusterReadyFn
	origHostDNS := ensureClusterHostGatewayDNSFn
	origSetup := setupSecondaryClusterNodeFn
	t.Cleanup(func() {
		clusterRuntimeStateFn = origState
		startDeclaredClusterFn = origStart
		waitDeclaredClusterReadyFn = origWait
		ensureClusterHostGatewayDNSFn = origHostDNS
		setupSecondaryClusterNodeFn = origSetup
	})

	clusterRuntimeStateFn = func(_ context.Context, _ string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true, Running: false}, nil
	}

	var events []string
	startDeclaredClusterFn = func(_ context.Context, name string) error {
		events = append(events, "start:"+name)
		return nil
	}
	waitDeclaredClusterReadyFn = func(_ context.Context, name string) error {
		events = append(events, "wait:"+name)
		return nil
	}
	ensureClusterHostGatewayDNSFn = func(_ context.Context, name string) error {
		events = append(events, "host-dns:"+name)
		return nil
	}
	setupSecondaryClusterNodeFn = func(_ context.Context, c ClusterEntity) error {
		events = append(events, "setup:"+c.Name)
		return nil
	}

	clusters := []ClusterEntity{
		{Name: "control-plane"},
		{Name: "cp-daemon", Network: "k3d-control-plane", RegistryInherit: true},
	}
	if err := reconcileDeclaredClusters(t.Context(), clusters, "", "dev"); err != nil {
		t.Fatalf("reconcileDeclaredClusters: %v", err)
	}

	want := []string{
		"start:control-plane",
		"wait:control-plane",
		"host-dns:control-plane",
		"start:cp-daemon",
		"wait:cp-daemon",
		"host-dns:cp-daemon",
		"setup:cp-daemon",
	}
	if len(events) != len(want) {
		t.Fatalf("events = %v; want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v; want %v", events, want)
		}
	}
}

func TestReconcileDeclaredClusters_StoppedClusterStartFailureStopsReconcile(t *testing.T) {
	withReachableDocker(t)
	origState := clusterRuntimeStateFn
	origStart := startDeclaredClusterFn
	origWait := waitDeclaredClusterReadyFn
	origHostDNS := ensureClusterHostGatewayDNSFn
	origSetup := setupSecondaryClusterNodeFn
	t.Cleanup(func() {
		clusterRuntimeStateFn = origState
		startDeclaredClusterFn = origStart
		waitDeclaredClusterReadyFn = origWait
		ensureClusterHostGatewayDNSFn = origHostDNS
		setupSecondaryClusterNodeFn = origSetup
	})

	clusterRuntimeStateFn = func(_ context.Context, _ string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true, Running: false}, nil
	}
	wantErr := errors.New("start failed")
	startDeclaredClusterFn = func(_ context.Context, _ string) error { return wantErr }
	waitCalled := false
	hostDNSCalled := false
	setupCalled := false
	waitDeclaredClusterReadyFn = func(_ context.Context, _ string) error {
		waitCalled = true
		return nil
	}
	ensureClusterHostGatewayDNSFn = func(_ context.Context, _ string) error {
		hostDNSCalled = true
		return nil
	}
	setupSecondaryClusterNodeFn = func(_ context.Context, _ ClusterEntity) error {
		setupCalled = true
		return nil
	}

	err := reconcileDeclaredClusters(t.Context(), []ClusterEntity{{
		Name: "cp-daemon", Network: "k3d-control-plane", RegistryInherit: true,
	}}, "", "dev")
	if !errors.Is(err, wantErr) {
		t.Fatalf("reconcileDeclaredClusters error = %v; want wrapped %v", err, wantErr)
	}
	if waitCalled || hostDNSCalled || setupCalled {
		t.Fatalf("start failure continued reconciliation: wait=%v hostDNS=%v setup=%v", waitCalled, hostDNSCalled, setupCalled)
	}
}

func TestEnsureDeclaredCluster_PassesPinnedK3sImage(t *testing.T) {
	origState := clusterRuntimeStateFn
	origCreate := createDeclaredClusterFn
	origHostDNS := ensureClusterHostGatewayDNSFn
	t.Cleanup(func() {
		clusterRuntimeStateFn = origState
		createDeclaredClusterFn = origCreate
		ensureClusterHostGatewayDNSFn = origHostDNS
	})

	clusterRuntimeStateFn = func(_ context.Context, _ string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{}, nil
	}
	var gotArgs []string
	createDeclaredClusterFn = func(_ context.Context, name string, args []string) error {
		if name != "cp-daemon" {
			t.Fatalf("create name = %q; want cp-daemon", name)
		}
		gotArgs = append([]string(nil), args...)
		return nil
	}
	ensureClusterHostGatewayDNSFn = func(_ context.Context, name string) error {
		if name != "cp-daemon" {
			t.Fatalf("host DNS cluster = %q; want cp-daemon", name)
		}
		return nil
	}

	if err := ensureDeclaredCluster(t.Context(), ClusterEntity{
		Name: "cp-daemon", Image: "rancher/k3s:v1.36.3-k3s1", Servers: 1,
	}, nil, "", "dev"); err != nil {
		t.Fatalf("ensureDeclaredCluster: %v", err)
	}
	want := []string{
		"cluster", "create", "cp-daemon", "--image", "rancher/k3s:v1.36.3-k3s1", "--servers", "1",
	}
	want = append(want, k3sIssuerFlags()...)
	if len(gotArgs) != len(want) {
		t.Fatalf("create args = %v; want %v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("create args = %v; want %v", gotArgs, want)
		}
	}
}

func TestK3dClusterListEntryFullyRunning(t *testing.T) {
	cases := []struct {
		name  string
		entry k3dClusterListEntry
		want  bool
	}{
		{"stopped", k3dClusterListEntry{ServersCount: 1}, false},
		{"all nodes running", k3dClusterListEntry{ServersRunning: 1, ServersCount: 1, AgentsRunning: 2, AgentsCount: 2}, true},
		{"agent stopped", k3dClusterListEntry{ServersRunning: 1, ServersCount: 1, AgentsRunning: 1, AgentsCount: 2}, false},
		{"legacy k3d JSON without counts", k3dClusterListEntry{ServersRunning: 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.entry.fullyRunning(); got != tc.want {
				t.Errorf("fullyRunning() = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestStartK3dCluster_CommandFailureButClusterRunningContinues(t *testing.T) {
	origRun := runK3dClusterStartCommandFn
	origLookup := lookupK3dStateAfterStartFn
	origCleanup := cleanupK3dStartToolsFn
	t.Cleanup(func() {
		runK3dClusterStartCommandFn = origRun
		lookupK3dStateAfterStartFn = origLookup
		cleanupK3dStartToolsFn = origCleanup
	})

	startErr := errors.New("host-alias injection hung")
	runK3dClusterStartCommandFn = func(_ context.Context, _ string) error { return startErr }
	lookupK3dStateAfterStartFn = func(_ context.Context, _ string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true, Running: true}, nil
	}
	cleanupK3dStartToolsFn = func(_ context.Context, _ string) error { return nil }

	if err := startK3dCluster(t.Context(), "control-plane"); err != nil {
		t.Fatalf("startK3dCluster should accept authoritative running state after command failure: %v", err)
	}
}

func TestStartK3dCluster_CommandFailureAndClusterStoppedFails(t *testing.T) {
	origRun := runK3dClusterStartCommandFn
	origLookup := lookupK3dStateAfterStartFn
	origCleanup := cleanupK3dStartToolsFn
	t.Cleanup(func() {
		runK3dClusterStartCommandFn = origRun
		lookupK3dStateAfterStartFn = origLookup
		cleanupK3dStartToolsFn = origCleanup
	})

	startErr := errors.New("start failed")
	runK3dClusterStartCommandFn = func(_ context.Context, _ string) error { return startErr }
	lookupK3dStateAfterStartFn = func(_ context.Context, _ string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true, Running: false}, nil
	}
	cleanupK3dStartToolsFn = func(_ context.Context, _ string) error { return nil }

	err := startK3dCluster(t.Context(), "control-plane")
	if !errors.Is(err, startErr) {
		t.Fatalf("startK3dCluster error = %v; want wrapped %v", err, startErr)
	}
}

func TestStartK3dCluster_CancelsHungClientAfterClusterIsRunning(t *testing.T) {
	origRun := runK3dClusterStartCommandFn
	origLookup := lookupK3dStateAfterStartFn
	origCleanup := cleanupK3dStartToolsFn
	origPoll := k3dClusterStartPollInterval
	origGrace := k3dClusterStartHealthyGrace
	t.Cleanup(func() {
		runK3dClusterStartCommandFn = origRun
		lookupK3dStateAfterStartFn = origLookup
		cleanupK3dStartToolsFn = origCleanup
		k3dClusterStartPollInterval = origPoll
		k3dClusterStartHealthyGrace = origGrace
	})

	runK3dClusterStartCommandFn = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	lookupK3dStateAfterStartFn = func(_ context.Context, _ string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true, Running: true}, nil
	}
	cleaned := false
	cleanupK3dStartToolsFn = func(_ context.Context, name string) error {
		if name != "cp-daemon" {
			t.Fatalf("cleanup name = %q; want cp-daemon", name)
		}
		cleaned = true
		return nil
	}
	k3dClusterStartPollInterval = time.Millisecond
	k3dClusterStartHealthyGrace = 2 * time.Millisecond

	if err := startK3dCluster(t.Context(), "cp-daemon"); err != nil {
		t.Fatalf("startK3dCluster: %v", err)
	}
	if !cleaned {
		t.Fatal("hung-client recovery did not clean the temporary tools container")
	}
}

func TestCreateK3dCluster_CancelsHungClientAfterClusterIsRunning(t *testing.T) {
	origRun := runK3dClusterCreateCommandFn
	origLookup := lookupK3dStateAfterCreateFn
	origCleanup := cleanupK3dCreateToolsFn
	origMerge := mergeK3dKubeconfigFn
	origPoll := k3dClusterStartPollInterval
	origGrace := k3dClusterStartHealthyGrace
	t.Cleanup(func() {
		runK3dClusterCreateCommandFn = origRun
		lookupK3dStateAfterCreateFn = origLookup
		cleanupK3dCreateToolsFn = origCleanup
		mergeK3dKubeconfigFn = origMerge
		k3dClusterStartPollInterval = origPoll
		k3dClusterStartHealthyGrace = origGrace
	})

	runK3dClusterCreateCommandFn = func(ctx context.Context, args []string) error {
		if len(args) < 3 || args[0] != "cluster" || args[1] != "create" {
			t.Fatalf("create args = %v", args)
		}
		// The context is merged under the kubeconfig lock afterwards; the
		// create itself must not write the kubeconfig (kubeconfig_lock.go).
		if !slices.Contains(args, "--kubeconfig-update-default=false") || !slices.Contains(args, "--kubeconfig-switch-context=false") {
			t.Errorf("create writes the kubeconfig unlocked: args = %v", args)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	lookupK3dStateAfterCreateFn = func(_ context.Context, _ string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true, Running: true}, nil
	}
	cleaned := false
	cleanupK3dCreateToolsFn = func(_ context.Context, name string) error {
		if name != "control-plane" {
			t.Fatalf("cleanup name = %q; want control-plane", name)
		}
		cleaned = true
		return nil
	}
	merged := false
	mergeK3dKubeconfigFn = func(_ context.Context, name string) error {
		if name != "control-plane" {
			t.Fatalf("merge name = %q; want control-plane", name)
		}
		merged = true
		return nil
	}
	k3dClusterStartPollInterval = time.Millisecond
	k3dClusterStartHealthyGrace = 2 * time.Millisecond

	if err := createK3dCluster(t.Context(), "control-plane",
		[]string{"cluster", "create", "control-plane"}); err != nil {
		t.Fatalf("createK3dCluster: %v", err)
	}
	if !cleaned {
		t.Fatal("hung-client recovery did not clean the temporary tools container")
	}
	if !merged {
		t.Fatal("hung-client recovery did not repair the default kubeconfig")
	}
}

// TestNodeHostsLineFor checks the host-gateway alias is matched on a
// whole whitespace field (not a substring), and returns the full line.
func TestNodeHostsLineFor(t *testing.T) {
	const hosts = "10.0.0.1 k3d-cp-server-0\n192.168.65.254 host.k3d.internal\n"
	if got := nodeHostsLineFor(hosts, "host.k3d.internal"); got != "192.168.65.254 host.k3d.internal" {
		t.Errorf("nodeHostsLineFor = %q want the host.k3d.internal line", got)
	}
	if got := nodeHostsLineFor(hosts, "missing.alias"); got != "" {
		t.Errorf("nodeHostsLineFor(missing) = %q want empty", got)
	}
	// A longer hostname that merely contains the alias as a substring must
	// not false-match.
	if got := nodeHostsLineFor("10.0.0.2 host.k3d.internal.evil\n", "host.k3d.internal"); got != "" {
		t.Errorf("nodeHostsLineFor(substring) = %q want empty (no substring match)", got)
	}
}

func TestUpsertNodeHostsAlias(t *testing.T) {
	const before = "172.27.0.2 k3d-control-plane-server-0\n" +
		"192.168.1.10 host.k3d.internal retained-name\n" +
		"10.0.0.8 another-name\n"
	const want = "172.27.0.2 k3d-control-plane-server-0\n" +
		"192.168.1.10 retained-name\n" +
		"10.0.0.8 another-name\n" +
		"192.168.65.254 host.k3d.internal\n"
	got := upsertNodeHostsAlias(before, k3dHostGatewayAlias, "192.168.65.254")
	if got != want {
		t.Fatalf("upsertNodeHostsAlias:\n%s\nwant:\n%s", got, want)
	}
	if gotIP := nodeHostsIPFor(got, k3dHostGatewayAlias); gotIP != "192.168.65.254" {
		t.Fatalf("nodeHostsIPFor = %q; want 192.168.65.254", gotIP)
	}
}

// TestEffectiveServers defaults a zero/negative Servers to 1 (the schema
// default; belt-and-suspenders for a hand-built entity).
func TestEffectiveServers(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 1},
		{-3, 1},
		{1, 1},
		{3, 3},
	}
	for _, tc := range cases {
		if got := effectiveServers(ClusterEntity{Servers: tc.in}); got != tc.want {
			t.Errorf("effectiveServers(%d) = %d want %d", tc.in, got, tc.want)
		}
	}
}

// TestWaitNodeReadyReportsProgressAndTheLastReason pins that the wait is not
// silent. Ninety seconds of no output is indistinguishable from a hang — the
// exact misreading that sent an operator hunting a forge bug when the cluster
// was merely churning — so every failed attempt reports elapsed time and why,
// and the final error carries the last reason instead of a bare timeout.
func TestWaitNodeReadyReportsProgressAndTheLastReason(t *testing.T) {
	origAttempt := nodeReadyAttemptFn
	origBudget := nodeReadyWaitBudget
	origAttemptBudget := nodeReadyAttemptBudget
	origInterval := nodeReadyRetryInterval
	origReport := nodeReadyReportInterval
	t.Cleanup(func() {
		nodeReadyAttemptFn = origAttempt
		nodeReadyWaitBudget = origBudget
		nodeReadyAttemptBudget = origAttemptBudget
		nodeReadyRetryInterval = origInterval
		nodeReadyReportInterval = origReport
	})

	nodeReadyWaitBudget = 60 * time.Millisecond
	nodeReadyAttemptBudget = 10 * time.Millisecond
	nodeReadyRetryInterval = time.Millisecond
	nodeReadyReportInterval = time.Hour // only the first failure reports
	attempts := 0
	nodeReadyAttemptFn = func(_ context.Context, kctx string, budget time.Duration) (string, error) {
		attempts++
		if kctx != "k3d-control-plane" {
			t.Fatalf("attempt targeted context %q", kctx)
		}
		if budget > nodeReadyAttemptBudget {
			t.Fatalf("attempt budget %s exceeded the per-attempt cap", budget)
		}
		return "error: timed out waiting for the condition on nodes/k3d-control-plane-server-0", errTestNodeNotReady
	}

	var err error
	out := captureStdout(t, func() { err = waitNodeReady(t.Context(), "control-plane") })
	if err == nil {
		t.Fatal("waitNodeReady returned success after every attempt failed")
	}
	if !strings.Contains(err.Error(), "timed out waiting for the condition") {
		t.Fatalf("final error dropped the last reason: %v", err)
	}
	if attempts < 2 {
		t.Fatalf("attempts = %d; want the wait to retry within its budget", attempts)
	}
	if !strings.Contains(out, "still retrying") || !strings.Contains(out, "k3d-control-plane") {
		t.Fatalf("wait was silent; got:\n%s", out)
	}
	// Paced, not per-attempt: a blip the loop absorbs must not scroll a wall.
	if n := strings.Count(out, "still retrying"); n != 1 {
		t.Fatalf("progress reported %d times for a sub-interval wait; want 1:\n%s", n, out)
	}
}

// TestWaitNodeReadyReturnsOnFirstSuccess keeps the warm path quiet: a cluster
// that is already Ready must not print a progress line at all.
func TestWaitNodeReadyReturnsOnFirstSuccess(t *testing.T) {
	origAttempt := nodeReadyAttemptFn
	t.Cleanup(func() { nodeReadyAttemptFn = origAttempt })

	attempts := 0
	nodeReadyAttemptFn = func(context.Context, string, time.Duration) (string, error) {
		attempts++
		return "", nil
	}
	out := captureStdout(t, func() {
		if err := waitNodeReady(t.Context(), "control-plane"); err != nil {
			t.Fatalf("waitNodeReady: %v", err)
		}
	})
	if attempts != 1 {
		t.Fatalf("attempts = %d; want 1", attempts)
	}
	if out != "" {
		t.Fatalf("warm path printed progress noise:\n%s", out)
	}
}

var errTestNodeNotReady = errors.New("exit status 1")

// TestRecreateClusterCommandNamesDependentSecondaries pins that a remediation
// which recreates an OWNER cluster names the nested clusters that live on its
// network. `k3d cluster delete control-plane` alone cannot remove a network
// cp-daemon is still attached to, and a cp-daemon left behind points at a
// registry container that no longer exists — forge derives the ownership edge
// already, so it must not hand the operator a command that half-works.
func TestRecreateClusterCommandNamesDependentSecondaries(t *testing.T) {
	owner := ClusterEntity{Name: "control-plane"}
	declared := []ClusterEntity{
		owner,
		{Name: "cp-daemon", Network: "k3d-control-plane", RegistryInherit: true},
	}
	got := recreateClusterCommand(owner, declared, "dev")
	want := "k3d cluster delete cp-daemon control-plane && forge env up dev"
	if got != want {
		t.Fatalf("remediation = %q; want %q", got, want)
	}
}

// TestRecreateClusterCommandLeavesStandaloneClustersAlone keeps the common
// case unchanged: a cluster nothing is nested inside deletes on its own.
func TestRecreateClusterCommandLeavesStandaloneClustersAlone(t *testing.T) {
	solo := ClusterEntity{Name: "control-plane"}
	got := recreateClusterCommand(solo, []ClusterEntity{solo}, "dev")
	want := "k3d cluster delete control-plane && forge env up dev"
	if got != want {
		t.Fatalf("remediation = %q; want %q", got, want)
	}
}

// TestDependentSecondariesIgnoresUnrelatedNesting guards the edge derivation:
// a secondary nested inside a DIFFERENT owner is not a dependent, and a
// cluster is never its own dependent.
func TestDependentSecondariesIgnoresUnrelatedNesting(t *testing.T) {
	declared := []ClusterEntity{
		{Name: "control-plane"},
		{Name: "other"},
		{Name: "nested-elsewhere", Network: "k3d-other", RegistryInherit: true},
		{Name: "not-a-secondary", Network: "k3d-control-plane"},
	}
	if got := dependentSecondaries(declared[0], declared); len(got) != 0 {
		t.Fatalf("dependents = %v; want none", got)
	}
	got := dependentSecondaries(declared[1], declared)
	if len(got) != 1 || got[0] != "nested-elsewhere" {
		t.Fatalf("dependents = %v; want [nested-elsewhere]", got)
	}
}

// TestLoadBalancerNeedsRefresh pins the start-ORDER signal behind the stale
// load-balancer heal. k3d's serverlb resolves the server node's name once at
// nginx startup and has no `resolver` to re-resolve it, so a node that came up
// AFTER the load balancer may hold an address the LB never saw.
func TestLoadBalancerNeedsRefresh(t *testing.T) {
	lb := time.Date(2026, 8, 28, 11, 27, 1, 207751754, time.UTC)
	nodeAfter := time.Date(2026, 8, 28, 11, 27, 8, 734552341, time.UTC)
	nodeBefore := lb.Add(-2 * time.Second)

	if !loadBalancerNeedsRefresh(lb, nodeAfter) {
		t.Fatal("a node that started after the load balancer was not treated as stale")
	}
	if loadBalancerNeedsRefresh(lb, nodeBefore) {
		t.Fatal("a node that started before the load balancer was treated as stale")
	}
	if loadBalancerNeedsRefresh(lb, lb) {
		t.Fatal("identical start times were treated as stale")
	}
	// Absent containers: nothing cached, nothing to heal.
	if loadBalancerNeedsRefresh(time.Time{}, nodeAfter) {
		t.Fatal("a cluster with no load balancer asked for a refresh")
	}
	if loadBalancerNeedsRefresh(lb, time.Time{}) {
		t.Fatal("a cluster with no server-0 asked for a refresh")
	}
}

// TestReconcileExistingClusterHealsLoadBalancerBeforeProbing pins the ORDER.
// Healing after the probe would be useless: the stale load balancer is one of
// the reasons the probe fails, so it has to be repaired first or forge spends
// its whole readiness budget waiting on a route it could have fixed.
func TestReconcileExistingClusterHealsLoadBalancerBeforeProbing(t *testing.T) {
	origLB := ensureClusterLBFreshFn
	origHealthy := ensureRunningClusterHealthyFn
	origDNS := ensureClusterHostGatewayDNSFn
	t.Cleanup(func() {
		ensureClusterLBFreshFn = origLB
		ensureRunningClusterHealthyFn = origHealthy
		ensureClusterHostGatewayDNSFn = origDNS
	})

	var order []string
	ensureClusterLBFreshFn = func(_ context.Context, name string) error {
		if name != "control-plane" {
			t.Fatalf("LB refresh cluster = %q", name)
		}
		order = append(order, "lb")
		return nil
	}
	ensureRunningClusterHealthyFn = func(context.Context, ClusterEntity) error {
		order = append(order, "probe")
		return nil
	}
	ensureClusterHostGatewayDNSFn = func(context.Context, string) error {
		order = append(order, "dns")
		return nil
	}

	err := reconcileExistingCluster(t.Context(),
		ClusterEntity{Name: "control-plane"},
		k3dClusterRuntimeState{Exists: true, Running: true}, nil, "", "dev")
	if err != nil {
		t.Fatalf("reconcileExistingCluster: %v", err)
	}
	if len(order) != 3 || order[0] != "lb" || order[1] != "probe" || order[2] != "dns" {
		t.Fatalf("reconcile order = %v; want [lb probe dns]", order)
	}
}

// TestInheritRegistryMirrorRepairsNodeIPDriftAfterRestart pins that the node
// restart inside the registry-mirror step goes through the health-AND-REPAIR
// path. That `docker restart` is the most likely trigger of Docker node-IP
// drift in forge — the node can come back on a different address, k3s exits
// with "failed to find interface with specified node ip", and the node never
// reports Ready. A bare wait turns a failure forge already knows how to repair
// into an opaque 90-second timeout, on the CREATE path a new user hits first.
func TestInheritRegistryMirrorRepairsNodeIPDriftAfterRestart(t *testing.T) {
	origHealthy := ensureRunningClusterHealthyFn
	origRead := readNodeFileFn
	origWrite := writeNodeFileFn
	origRestart := dockerRestartFn
	origRefresh := refreshLoadBalancerFn
	t.Cleanup(func() {
		ensureRunningClusterHealthyFn = origHealthy
		readNodeFileFn = origRead
		writeNodeFileFn = origWrite
		dockerRestartFn = origRestart
		refreshLoadBalancerFn = origRefresh
	})

	readNodeFileFn = func(context.Context, string, string) ([]byte, error) {
		return []byte("mirrors: {}\n"), nil
	}
	writeNodeFileFn = func(context.Context, string, string, []byte) error { return nil }
	dockerRestartFn = func(context.Context, string) error { return nil }
	refreshLoadBalancerFn = func(context.Context, string) error { return nil }

	healed := false
	ensureRunningClusterHealthyFn = func(_ context.Context, c ClusterEntity) error {
		healed = true
		if c.Name != "cp-daemon" {
			t.Fatalf("health/repair ran for cluster %q; want cp-daemon", c.Name)
		}
		return nil
	}

	err := inheritRegistryMirror(t.Context(), ClusterEntity{
		Name: "cp-daemon", Network: "k3d-control-plane", RegistryInherit: true, Servers: 1,
	})
	if err != nil {
		t.Fatalf("inheritRegistryMirror: %v", err)
	}
	if !healed {
		t.Fatal("the post-restart wait bypassed the node-IP drift repair path")
	}
}

// stubPortDrift fakes the live serverlb ports and the in-place edit. The edit
// adds its ports to the live set when applies is true, so the post-edit
// verification sees what a real `k3d cluster edit --port-add` would publish.
func stubPortDrift(t *testing.T, live map[int]bool, editErr error, applies bool) *[][]int {
	t.Helper()
	origRun, origAdd := runningClusterHostPortsFn, addClusterHostPortsFn
	t.Cleanup(func() { runningClusterHostPortsFn, addClusterHostPortsFn = origRun, origAdd })
	var calls [][]int
	runningClusterHostPortsFn = func(context.Context, string) (map[int]bool, error) { return live, nil }
	addClusterHostPortsFn = func(_ context.Context, _ string, ports []int) error {
		calls = append(calls, ports)
		if editErr == nil && applies {
			for _, p := range ports {
				live[p] = true
			}
		}
		return editErr
	}
	return &calls
}

// Drift is repaired in place: one edit carrying every missing port, no error,
// so `forge env up` proceeds on the live cluster instead of demanding a recreate.
func TestReconcileClusterPortDrift_AddsMissingPortsInPlace(t *testing.T) {
	c := ClusterEntity{Name: "control-plane"}
	calls := stubPortDrift(t, map[int]bool{28080: true}, nil, true)
	err := reconcileClusterPortDrift(context.Background(), c, []ClusterEntity{c}, "dev",
		map[int]bool{28080: true, 38086: true, 28090: true})
	if err != nil {
		t.Fatalf("drift should be repaired in place, got %v", err)
	}
	if len(*calls) != 1 || len((*calls)[0]) != 2 || (*calls)[0][0] != 28090 || (*calls)[0][1] != 38086 {
		t.Fatalf("want one edit adding [28090 38086], got %v", *calls)
	}
}

func TestReconcileClusterPortDrift_NoDriftDoesNotEdit(t *testing.T) {
	c := ClusterEntity{Name: "control-plane"}
	calls := stubPortDrift(t, map[int]bool{28080: true}, nil, true)
	if err := reconcileClusterPortDrift(context.Background(), c, []ClusterEntity{c}, "dev", map[int]bool{28080: true}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatalf("no drift must not touch the serverlb, got %v", *calls)
	}
}

// A failed edit falls back to the recreate guidance, which must still name the
// exact command and warn that it deletes workloads.
func TestReconcileClusterPortDrift_EditFailureFallsBackToRecreate(t *testing.T) {
	c := ClusterEntity{Name: "control-plane"}
	stubPortDrift(t, map[int]bool{28080: true}, errors.New("boom"), false)
	err := reconcileClusterPortDrift(context.Background(), c, []ClusterEntity{c}, "dev", map[int]bool{28080: true, 38086: true})
	if err == nil {
		t.Fatal("want an error when the in-place edit fails")
	}
	for _, want := range []string{"38086", "boom", "k3d cluster delete control-plane", "DELETES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

// An edit that exits 0 but doesn't publish the port is not a repair.
func TestReconcileClusterPortDrift_UnverifiedEditIsAnError(t *testing.T) {
	c := ClusterEntity{Name: "control-plane"}
	stubPortDrift(t, map[int]bool{28080: true}, nil, false)
	err := reconcileClusterPortDrift(context.Background(), c, []ClusterEntity{c}, "dev", map[int]bool{28080: true, 38086: true})
	if err == nil || !strings.Contains(err.Error(), "still unmapped") {
		t.Fatalf("want a still-unmapped error, got %v", err)
	}
}

// createSeams stubs every side effect of createK3dCluster and records them.
type createSeams struct {
	mu      sync.Mutex
	runs    [][]string
	deletes []string
	merges  int
}

// record notes one create run and returns how many there have been. The
// create runs on the lifecycle's goroutine while the boot-crash watch reads
// the count on its own, hence the lock.
func (s *createSeams) record(args []string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs = append(s.runs, args)
	return len(s.runs)
}

func (s *createSeams) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}

func stubCreateSeams(t *testing.T, exists bool, runErrs []error, crashes []string) *createSeams {
	t.Helper()
	origRun, origLookup, origCleanup := runK3dClusterCreateCommandFn, lookupK3dStateAfterCreateFn, cleanupK3dCreateToolsFn
	origMerge, origCrash, origDelete, origStorage := mergeK3dKubeconfigFn, k3dServerBootCrashFn, deleteK3dClusterFn, addClusterStorageArgsFn
	t.Cleanup(func() {
		runK3dClusterCreateCommandFn, lookupK3dStateAfterCreateFn, cleanupK3dCreateToolsFn = origRun, origLookup, origCleanup
		mergeK3dKubeconfigFn, k3dServerBootCrashFn, deleteK3dClusterFn, addClusterStorageArgsFn = origMerge, origCrash, origDelete, origStorage
	})
	s := &createSeams{}
	addClusterStorageArgsFn = func(args []string) ([]string, error) { return args, nil }
	runK3dClusterCreateCommandFn = func(_ context.Context, args []string) error {
		if i := s.record(args) - 1; i < len(runErrs) {
			return runErrs[i]
		}
		return nil
	}
	lookupK3dStateAfterCreateFn = func(context.Context, string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: exists}, nil
	}
	cleanupK3dCreateToolsFn = func(context.Context, string) error { return nil }
	mergeK3dKubeconfigFn = func(context.Context, string) error { s.merges++; return nil }
	k3dServerBootCrashFn = func(context.Context, string) string {
		if i := s.count() - 1; i >= 0 && i < len(crashes) {
			return crashes[i]
		}
		return ""
	}
	deleteK3dClusterFn = func(_ context.Context, name string) error { s.deletes = append(s.deletes, name); return nil }
	return s
}

var errK3dReady = errors.New("k3d: node k3d-x-server-0 failed to get ready: stopped returning log lines: node is running=true in status=restarting")

// A server that crashed while booting is a k3s/k3d startup failure forge
// cannot prevent and the next create does not repeat: the half-made cluster
// is deleted and created once more.
func TestCreateK3dCluster_RecreatesOnceAfterAK3sBootCrash(t *testing.T) {
	s := stubCreateSeams(t, false, []error{errK3dReady}, []string{"k3d-x-server-0: restarts=1 running exit=1"})
	if err := createK3dCluster(t.Context(), "x", []string{"cluster", "create", "x"}); err != nil {
		t.Fatalf("createK3dCluster: %v", err)
	}
	if len(s.runs) != 2 || len(s.deletes) != 1 || s.merges != 1 {
		t.Fatalf("runs=%d deletes=%v merges=%d, want 2 creates, the failed one deleted, one merge", len(s.runs), s.deletes, s.merges)
	}
	if !slices.Contains(s.runs[0], "--no-rollback") {
		t.Errorf("the create must keep a failed server for its logs: args=%v", s.runs[0])
	}
}

// Any other failure is not retried: forge rolls back what it made (k3d's own
// rollback is off) and returns the error.
func TestCreateK3dCluster_OtherFailureRollsBackOnceAndReturns(t *testing.T) {
	boom := errors.New("k3d: port 6443 is already allocated")
	s := stubCreateSeams(t, false, []error{boom}, nil)
	if err := createK3dCluster(t.Context(), "x", []string{"cluster", "create", "x"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the create's own error", err)
	}
	if len(s.runs) != 1 || len(s.deletes) != 1 || s.merges != 0 {
		t.Fatalf("runs=%d deletes=%v merges=%d, want one create, rolled back, nothing merged", len(s.runs), s.deletes, s.merges)
	}
}

// A crash on the retry too is not an infrastructure blip: fail, with both
// servers' reasons.
func TestCreateK3dCluster_SecondBootCrashFailsWithBothReasons(t *testing.T) {
	s := stubCreateSeams(t, false, []error{errK3dReady, errK3dReady}, []string{"first: exit=1", "second: exit=2"})
	err := createK3dCluster(t.Context(), "x", []string{"cluster", "create", "x"})
	if err == nil || !strings.Contains(err.Error(), "first: exit=1") || !strings.Contains(err.Error(), "second: exit=2") {
		t.Fatalf("err = %v, want both attempts' reasons", err)
	}
	if len(s.runs) != 2 || len(s.deletes) != 2 {
		t.Fatalf("runs=%d deletes=%v, want exactly one retry, both rolled back", len(s.runs), s.deletes)
	}
}

// A name that is already taken is someone's cluster: k3d refuses the create,
// and forge must neither keep the nodes for itself nor delete anything.
func TestCreateK3dCluster_NeverRollsBackAClusterThatExisted(t *testing.T) {
	s := stubCreateSeams(t, true, []error{errors.New("cluster x already exists")}, []string{"k3d-x-server-0: restarts=3"})
	if err := createK3dCluster(t.Context(), "x", []string{"cluster", "create", "x"}); err == nil {
		t.Fatal("want the create's error")
	}
	if len(s.deletes) != 0 || slices.Contains(s.runs[0], "--no-rollback") {
		t.Fatalf("deletes=%v args=%v: an existing cluster was put at risk", s.deletes, s.runs[0])
	}
}

// Some k3d/Docker combinations do not fail a create whose server restarted:
// the readiness wait keeps following a log stream that has ended until its
// own timeout (seen on CI's k3d 5.8.3). The create watches the server
// containers instead, cancels the wedged client the moment one restarted, and
// recreates — it does not sit out the timeout.
func TestCreateK3dCluster_WatchCatchesARestartK3dDoesNotReport(t *testing.T) {
	s := stubCreateSeams(t, false, nil, nil)
	origPoll := k3dBootCrashPoll
	t.Cleanup(func() { k3dBootCrashPoll = origPoll })
	k3dBootCrashPoll = 5 * time.Millisecond
	runK3dClusterCreateCommandFn = func(ctx context.Context, args []string) error {
		if s.record(args) == 1 {
			<-ctx.Done() // k3d waiting on a log stream that will never say "up"
			return ctx.Err()
		}
		return nil
	}
	k3dServerBootCrashFn = func(context.Context, string) string {
		if s.count() == 1 {
			return "k3d-x-server-0: restarts=1 running exit=0"
		}
		return ""
	}
	start := time.Now()
	if err := createK3dCluster(t.Context(), "x", []string{"cluster", "create", "x"}); err != nil {
		t.Fatalf("createK3dCluster: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("took %v: the restart was not caught while k3d waited", took)
	}
	if len(s.runs) != 2 || len(s.deletes) != 1 || s.merges != 1 {
		t.Fatalf("runs=%d deletes=%v merges=%d, want the wedged create cancelled, deleted, and created again", len(s.runs), s.deletes, s.merges)
	}
}
