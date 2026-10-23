// Package cli — declarative cluster reconcile for `forge env up`.
//
// This file generalizes the dev-only, single-cluster ensureDevCluster
// bootstrap into a declarative LIST: an env declares
// `Bundle.clusters = [forge.Cluster {...}, ...]` and forge ensures each
// exists at the head of `forge env up` (create-if-absent, no-op if present).
//
// This file IS the k3d ClusterProvider's implementation (see
// cluster_provider.go for the registry that dispatches to it per-entity by
// Cluster.provider). Everything below — ensureDeclaredCluster, the
// create/start/heal seams, the node-level docker shell-outs — is k3d-
// specific; a "vcluster" or "gke" provider is a SEPARATE ClusterProvider
// implementation with its own file, not a branch added here.
//
// Multi-cluster ownership is a REFERENCE. There is no "primary" cluster:
// a secondary cluster names its `owner` Cluster, and the KCL render layer
// DERIVES the joined docker network (Cluster.Network = `k3d-<owner.name>`)
// and the registry-inherit flag (Cluster.RegistryInherit = true) from
// that one edge. The owner cluster projects neither — k3d creates its own
// network/registry. There is no most-X heuristic. `owner` is meaningful
// for k3d only — see the Cluster schema doc (kcl/schema.k) for why a
// vcluster's host relationship is a distinct `host?` edge instead.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// reconcileDeclaredClusters ensures every cluster the env declares exists
// and is running, in declaration order. Idempotent: a cluster that already
// exists and is running is a no-op (warm-run fast path); a preserved but
// stopped cluster is started before reconciliation continues. The owner
// cluster must be declared BEFORE any secondary that inherits its network/registry —
// declaration order is the contract (a secondary references the owner's
// network by name, which only exists once the owner is created).
//
// Each declared cluster is dispatched through clusterProviderRegistry by
// its own Cluster.provider ("k3d" by default) — see cluster_provider.go.
// Looked up per-entity, not once for the whole call, so a mixed-provider
// env never collides two clusters on shared dispatch state.
//
// A nil/empty list is a no-op: an env that declares no clusters keeps
// today's behavior (`forge env up e2e` ensures nothing; the legacy
// single dev cluster is bootstrapped by ensureDevCluster on the deploy
// path).
// projectDir + env are retained for caller signature stability (the deploy
// phase that follows reconcile reads the same render). Ingress is NO LONGER
// installed imperatively per cluster: the Gateway API controller (Envoy
// Gateway) + CRDs + the `eg` GatewayClass come from the env's DECLARED
// `helm_charts` (a forge.HelmChart), rendered and applied by the deploy phase
// (`forge env deploy <env> --target=envoy-gateway`) EXACTLY like the cloud envs —
// one declarative model everywhere. This phase only ensures the bare k3d
// clusters exist (create-if-absent) with their host-port mappings and any
// nested-secondary node setup.
func reconcileDeclaredClusters(ctx context.Context, clusters []ClusterEntity, projectDir, env string) error {
	if len(clusters) == 0 {
		return nil
	}
	fmt.Printf("\n[up] cluster phase — ensuring %d declared cluster(s)\n", len(clusters))
	// k3d nodes, the k3d registry and the cluster network are all docker
	// objects, so a reachable daemon is a precondition for everything below.
	// Checking it once here turns N cascading per-command failures into one
	// true cause. See docker_preflight.go.
	if err := ensureDockerDaemon(ctx); err != nil {
		return err
	}
	for i := range clusters {
		// Resolved PER-ENTITY (not once for the whole call), so a mixed
		// env dispatches each declared cluster to its own provider rather
		// than colliding on shared registry state. See lookupClusterProvider.
		provider, err := lookupClusterProvider(clusters[i].Provider)
		if err != nil {
			return fmt.Errorf("cluster %q: %w", clusters[i].Name, err)
		}
		if err := provider.Ensure(ctx, clusters[i], clusters, projectDir, env); err != nil {
			return fmt.Errorf("ensure cluster %q: %w", clusters[i].Name, err)
		}
	}
	return nil
}

// ensureDeclaredCluster creates one declared k3d cluster if it's absent,
// applying the declared flags; starts it when present but stopped; and is a
// no-op when it already exists and is running. After a
// fresh create of a cluster with RegistryInherit (an `owner` reference),
// it mirrors the owner cluster's registries.yaml onto the new node and
// restarts it to reload.
// The indirection seams keep reconcile decisions unit-testable without
// shelling out to k3d / kubectl / docker. Production wires them to the real
// implementations; tests pin start/wait/setup ordering.
var (
	clusterRuntimeStateFn         = lookupK3dClusterRuntimeState
	startDeclaredClusterFn        = startK3dCluster
	createDeclaredClusterFn       = createK3dCluster
	waitDeclaredClusterReadyFn    = waitNodeReady
	ensureRunningClusterHealthyFn = ensureRunningK3dClusterHealthy
	ensureClusterLBFreshFn        = ensureClusterLoadBalancerFresh
	ensureClusterHostGatewayDNSFn = ensureClusterHostGatewayDNS
	setupSecondaryClusterNodeFn   = setupSecondaryClusterNode

	// Node-level docker shell-outs, seamed so inheritRegistryMirror's
	// read → write → restart → refresh → heal ordering is pinnable without a
	// Docker daemon. That ordering is load-bearing: each step undoes state the
	// previous one established.
	readNodeFileFn        = readNodeFile
	writeNodeFileFn       = writeNodeFile
	dockerRestartFn       = dockerRestart
	refreshLoadBalancerFn = refreshK3dLoadBalancer
)

// isNestedSecondary reports whether a cluster is a SECONDARY joined to an
// owner cluster's docker network — i.e. it declared an `owner`, which the
// render layer projects as RegistryInherit=true + a derived Network
// (`k3d-<owner.name>`). RegistryInherit keys directly off `owner != None`,
// so it's the load-bearing signal; Network carries the owner the node
// setup needs. Such a cluster needs the extra node setup
// setupSecondaryClusterNode performs.
func isNestedSecondary(c ClusterEntity) bool {
	return c.RegistryInherit && c.Network != ""
}

// clusterCreateFlags builds the `k3d cluster create` flags for a cluster
// declared WITHOUT a config file, projecting each declared field onto its
// flag. A cluster that names a config file takes none of these: the config is
// authoritative for servers/agents/ports/network.
func clusterCreateFlags(c ClusterEntity) []string {
	var flags []string
	if c.Image != "" {
		flags = append(flags, "--image", c.Image)
	}
	flags = append(flags, "--servers", strconv.Itoa(effectiveServers(c)))
	if c.Agents > 0 {
		flags = append(flags, "--agents", strconv.Itoa(c.Agents))
	}
	if c.Network != "" {
		flags = append(flags, "--network", c.Network)
	}
	if c.APIPort > 0 {
		// Bind on 0.0.0.0 so a sibling cluster on the same docker
		// network can reach this API server by host IP if needed.
		flags = append(flags, "--api-port", fmt.Sprintf("0.0.0.0:%d", c.APIPort))
	}
	flags = append(flags, k3sCIDRArgs(c)...)
	flags = append(flags, k3sIssuerFlags()...)
	return flags
}

const (
	// k3sPublicIssuer is the service-account issuer forge gives every k3d
	// cluster it creates. Stock k3s signs with
	// https://kubernetes.default.svc.cluster.local, a `.local` host the control
	// plane refuses when `forge cluster connect` registers the issuer.
	k3sPublicIssuer = "https://kubernetes.default.svc"
	// k3sLegacyIssuer is k3s's own default issuer. It stays accepted so tokens
	// minted by components that still name it keep validating.
	k3sLegacyIssuer = k3sPublicIssuer + ".cluster.local"

	k3sIssuerArgKey = "--kube-apiserver-arg=service-account-issuer="
)

// k3sIssuerArgs returns the k3s server args that make a new cluster sign with
// k3sPublicIssuer. kube-apiserver signs with the FIRST service-account-issuer
// and accepts every one listed, so the legacy issuer follows it. api-audiences
// must be restated because k3s sets it from its default issuer (plus "k3s");
// the legacy audience and "k3s" are kept so in-cluster tokens still validate.
func k3sIssuerArgs() []string {
	return []string{
		k3sIssuerArgKey + k3sPublicIssuer,
		k3sIssuerArgKey + k3sLegacyIssuer,
		"--kube-apiserver-arg=api-audiences=" + k3sPublicIssuer + "," + k3sLegacyIssuer + ",k3s",
	}
}

// k3sIssuerFlags is k3sIssuerArgs as `k3d cluster create --k3s-arg` flags, on
// every server node (see k3sCIDRArgs for why `server:*`).
func k3sIssuerFlags() []string {
	var flags []string
	for _, a := range k3sIssuerArgs() {
		flags = append(flags, "--k3s-arg", a+"@server:*")
	}
	return flags
}

// k3sCIDRArgs projects the declared pod / Service CIDRs onto the `k3d cluster
// create --k3s-arg` flags that carry them.
//
// k3d has no first-class flag for either: they are k3s SERVER args, passed
// through with the `<arg>@<nodefilter>` syntax k3d's own docs use
// (`--k3s-arg "--disable=traefik@server:*"`). The `server:*` filter — rather
// than `server:0` — applies the setting to every control-plane node, which
// matters for a multi-server cluster where a CIDR set on only the first server
// is a split-brain allocator rather than a partial configuration.
//
// Returns nil when neither is declared, so a cluster that does not use this
// capability produces the exact flag list it produced before.
func k3sCIDRArgs(c ClusterEntity) []string {
	var args []string
	if c.ClusterCIDR != "" {
		args = append(args, "--k3s-arg", "--cluster-cidr="+c.ClusterCIDR+"@server:*")
	}
	if c.ServiceCIDR != "" {
		args = append(args, "--k3s-arg", "--service-cidr="+c.ServiceCIDR+"@server:*")
	}
	return args
}

// reconcileExistingCluster brings an ALREADY-CREATED cluster up to what the
// current render declares: start it if it is stopped, fail loudly on host-port
// drift, and re-apply the secondary-node setup.
//
// Every step is idempotent, so this runs on warm and restarted clusters alike
// — a cluster whose host-gateway DNS alias or MSS clamp was lost (a manual
// node restart clears the iptables rule) heals on the next `forge env up`.
func reconcileExistingCluster(ctx context.Context, c ClusterEntity, state k3dClusterRuntimeState, declared []ClusterEntity, projectDir, env string) error {
	if state.Running {
		fmt.Printf("  cluster %q already exists and its containers are running — verifying Kubernetes API...\n", c.Name)
		// BEFORE the API probe, not after: a load balancer pinned to a stale
		// upstream is one of the things that makes the probe fail, and healing
		// it first turns an unexplainable intermittent failure into a no-op.
		if err := ensureClusterLBFreshFn(ctx, c.Name); err != nil {
			return fmt.Errorf("refresh load balancer for cluster %q: %w", c.Name, err)
		}
		if err := ensureRunningClusterHealthyFn(ctx, c); err != nil {
			return fmt.Errorf("verify running cluster %q: %w", c.Name, err)
		}
		fmt.Printf("  cluster %q Kubernetes API and nodes are Ready — no-op\n", c.Name)
	} else {
		fmt.Printf("  cluster %q already exists but is stopped — starting...\n", c.Name)
		if err := startDeclaredClusterFn(ctx, c.Name); err != nil {
			return err
		}
		// `k3d cluster start` brings the containers back in its own order, so
		// the node can once again come up after the load balancer.
		if err := ensureClusterLBFreshFn(ctx, c.Name); err != nil {
			return fmt.Errorf("refresh load balancer for cluster %q: %w", c.Name, err)
		}
		fmt.Printf("  waiting for cluster %q nodes to report Ready...\n", c.Name)
		if err := waitDeclaredClusterReadyFn(ctx, c.Name); err != nil {
			return fmt.Errorf("wait for restarted cluster %q: %w", c.Name, err)
		}
	}
	if err := ensureClusterHostGatewayDNSFn(ctx, c.Name); err != nil {
		return fmt.Errorf("ensure host-gateway DNS for cluster %q: %w", c.Name, err)
	}

	// HOST-PORT DRIFT GUARD. k3d fixes a cluster's host-port maps at
	// create time — a Gateway listener whose host port renders AFTER
	// the cluster was created (e.g. a listener added to the env's KCL)
	// is NOT mapped on the live cluster, so its route is silently
	// unreachable (connection refused on the host port). The host-port
	// set is DERIVED from the SAME render `forge env up` deploys
	// (deploy/k3d.yaml + the generated deploy/k3d-ports.yaml), so we
	// can detect this: compare the declared set to what the live
	// serverlb publishes and FAIL CLEARLY telling the user to recreate,
	// rather than leave the route dead. Only meaningful for a
	// config-driven cluster that maps Gateway host ports (HostPorts).
	if c.Config != "" && c.HostPorts {
		if err := checkClusterPortDrift(ctx, c, declared, projectDir, env); err != nil {
			return err
		}
	}

	// CIDR DRIFT GUARD, the same shape one level down the stack. k3s fixes
	// the pod and Service address blocks at create time too, so a
	// `cluster_cidr` / `service_cidr` declared against an already-running
	// cluster is completely inert. Unguarded that is worse than the port
	// case: the cluster comes up green, and the operator debugs the
	// cross-cluster routing their declaration was meant to fix rather than
	// the declaration never having taken effect. See cluster_cidr_drift.go.
	if err := checkClusterCIDRDrift(ctx, c, declared, env); err != nil {
		return err
	}
	if isNestedSecondary(c) {
		if err := setupSecondaryClusterNodeFn(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

func ensureDeclaredCluster(ctx context.Context, c ClusterEntity, declared []ClusterEntity, projectDir, env string) error {
	state, err := clusterRuntimeStateFn(ctx, c.Name)
	if err != nil {
		return err
	}
	// Converge the machine storage policy with what this cluster's declaration
	// says about local caches — the kubectl contexts whose workloads protect
	// images, and the registry container + host aliases its k3d config
	// references. BEFORE the create/reconcile branch and on BOTH of them: a
	// warm cluster's facts are just as true as a fresh one's, and activation
	// that only happened on a cold create would never fire on the machines
	// that have been running the same cluster for weeks — exactly the ones
	// with the disk problem. Idempotent and never fatal; see
	// storage_converge.go.
	convergeClusterStorage(c, declared, projectDir)
	if state.Exists {
		return reconcileExistingCluster(ctx, c, state, declared, projectDir, env)
	}

	args := []string{"cluster", "create", c.Name}
	if c.Config != "" {
		// A config file is authoritative: k3d reads servers/agents/ports/
		// network/registries from it. We still pass the cluster NAME
		// positionally (k3d merges it with metadata.name) so clusterExists
		// can find it by the declared name afterward.
		//
		// When this cluster hosts a Gateway — declared as a forge.HelmChart
		// platform dep with `host_ports = True` — merge the generated
		// deploy/k3d-ports.yaml listener host-port fragment into the config the
		// SAME way the dev `forge cluster up` path does. The
		// Gateway's listeners (e.g. grpc :29190 for daemon dial-out) need host
		// ports mapped through the k3d loadbalancer at CREATE time, or
		// in-cluster→host→cluster traffic (host.k3d.internal:<port>) finds
		// nothing listening. k3d.yaml deliberately carries no `ports:` block —
		// the ports live in the generated fragment, merged here.
		// Ensure any standalone registry the config references via
		// `registries.use` exists BEFORE create — a `use` reference fails the
		// create if the registry container isn't already up. A standalone
		// registry is owned by no cluster, so it (and its pushed images)
		// survives a later `cluster delete`; the next cold create re-references
		// the SAME registry and image pushes are cache hits. Idempotent: a
		// present registry is a `k3d registry list` no-op. Reads the un-merged
		// user config (the ports merge below doesn't touch `registries`).
		if err := ensureConfigRegistries(ctx, c.Config); err != nil {
			return fmt.Errorf("ensure standalone registry for cluster %q: %w", c.Name, err)
		}

		cfgPath, cleanup, err := mergeK3dConfig(c.Config, k3dConfigOverlay{
			HostPorts:    c.HostPorts,
			APIPort:      c.APIPort,
			ClusterCIDR:  c.ClusterCIDR,
			ServiceCIDR:  c.ServiceCIDR,
			PublicIssuer: true,
		})
		if err != nil {
			return fmt.Errorf("merge k3d config for cluster %q: %w", c.Name, err)
		}
		defer cleanup()
		if cfgPath.temporary {
			fmt.Printf("  (merging deploy/k3d-ports.yaml — Gateway listener host ports)\n")
		}
		args = append(args, "--config", cfgPath.path)
	} else {
		args = append(args, clusterCreateFlags(c)...)
	}

	fmt.Printf("  creating k3d cluster %q (%s)...\n", c.Name, strings.Join(args[2:], " "))
	if err := createDeclaredClusterFn(ctx, c.Name, args); err != nil {
		return err
	}

	if c.RegistryInherit {
		if c.Network == "" {
			return fmt.Errorf(
				"cluster %q: registry_inherit is set but the derived network is empty — an `owner` Cluster "+
					"reference is required so forge can resolve the owner whose registry to mirror", c.Name)
		}
		if err := inheritRegistryMirror(ctx, c); err != nil {
			return fmt.Errorf("inherit registry mirror: %w", err)
		}
	}
	if err := ensureClusterHostGatewayDNSFn(ctx, c.Name); err != nil {
		return fmt.Errorf("ensure host-gateway DNS for new cluster %q: %w", c.Name, err)
	}

	// A secondary cluster nested on an owner's docker network needs node
	// setup beyond the registry mirror: the owner's host-gateway DNS alias
	// (so its pods reach host services) and a TCP MSS clamp (so the nested
	// path-MTU doesn't shred large outbound TLS handshakes). Run it AFTER
	// inheritRegistryMirror, which restarts the node — the MSS clamp is
	// applied to the node's live iptables and must outlive that restart.
	if isNestedSecondary(c) {
		if err := setupSecondaryClusterNodeFn(ctx, c); err != nil {
			return err
		}
	}

	// The Gateway API stack (controller + CRDs + `eg` GatewayClass) is NOT
	// installed here. A fresh `k3d cluster create` ships none of it, but the
	// env declares Envoy Gateway as a forge.HelmChart platform dependency —
	// the deploy phase that follows reconcile renders + applies it
	// (`forge env deploy <env> --target=envoy-gateway`, CRD-first) BEFORE the
	// project's Gateway/HTTPRoute/GRPCRoute resources, EXACTLY like the cloud
	// envs. One declarative model everywhere.
	return nil
}

// checkClusterPortDrift reconciles the LIVE cluster's host ports with the
// ones the env's RENDERED Gateway listeners now require. The required set is
// DERIVED from the same render `forge env up` deploys (renderedGatewayHostPorts
// → the env's Gateway listeners, transform-added ones included), so it is
// the single source of truth — no static port list to keep in sync.
//
// k3d can't add a port map at the CLUSTER level after create, but the
// mappings live on the serverlb container and `k3d cluster edit --port-add`
// replaces just that container — nodes, PVCs, Secrets and every workload
// survive. So drift is repaired in place; a recreate is only the fallback
// when the edit itself fails. No drift (or an unreadable serverlb / render)
// is a silent no-op.
func checkClusterPortDrift(ctx context.Context, c ClusterEntity, declared []ClusterEntity, projectDir, env string) error {
	required, err := renderedGatewayHostPorts(ctx, projectDir, env)
	if err != nil {
		fmt.Printf("  warning: could not render Gateway listeners to verify host-port mappings for %q: %v\n", c.Name, err)
		return nil
	}
	return reconcileClusterPortDrift(ctx, c, declared, env, required)
}

// addClusterHostPortsFn adds host-port → serverlb mappings to a live cluster.
// A var so tests don't shell out to k3d.
var addClusterHostPortsFn = addClusterHostPorts

// addClusterHostPorts maps each port host:port → the serverlb via one
// `k3d cluster edit` (one serverlb replacement, not one per port). Ports are
// published 1:1, matching how the cluster's create-time mappings are declared.
func addClusterHostPorts(ctx context.Context, clusterName string, ports []int) error {
	args := []string{"cluster", "edit", clusterName}
	for _, p := range ports {
		args = append(args, "--port-add", fmt.Sprintf("%d:%d@loadbalancer", p, p))
	}
	out, err := exec.CommandContext(ctx, "k3d", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("k3d %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func reconcileClusterPortDrift(ctx context.Context, c ClusterEntity, declared []ClusterEntity, env string, required map[int]bool) error {
	missing, err := clusterPortDrift(ctx, c.Name, required)
	if err != nil {
		// Best-effort: a drift-read failure must not block the warm-run
		// fast path. Warn and continue.
		fmt.Printf("  warning: could not verify host-port mappings for %q: %v\n", c.Name, err)
		return nil
	}
	if len(missing) == 0 {
		return nil
	}
	ports := make([]string, len(missing))
	for i, p := range missing {
		ports[i] = strconv.Itoa(p)
	}
	fmt.Printf("  cluster %q is missing host-port mapping(s) %s required by env %q's Gateway listeners — adding them to its load balancer in place (nodes and workloads are untouched)\n",
		c.Name, strings.Join(ports, ", "), env)
	editErr := addClusterHostPortsFn(ctx, c.Name, missing)
	if editErr == nil {
		// Verify rather than trust the exit code: an edit that "succeeds"
		// without publishing the port is the failure this check exists for.
		still, verr := clusterPortDrift(ctx, c.Name, required)
		if verr != nil || len(still) == 0 {
			return nil
		}
		editErr = fmt.Errorf("ports %v still unmapped after the edit", still)
	}
	return fmt.Errorf(
		"cluster %q is missing host-port mapping(s) %s that env %q's rendered Gateway listeners now require, "+
			"and adding them in place failed: %v\n"+
			"Recreating the cluster picks up the mappings (this DELETES its workloads and Secrets):\n"+
			"    %s\n"+
			"(the required ports are DERIVED from the same render forge deploys — no manual port list to maintain)",
		c.Name, strings.Join(ports, ", "), env, editErr, recreateClusterCommand(c, declared, env))
}

// dependentSecondaries names the declared clusters that are NESTED inside c —
// they joined c's auto-created docker network and inherit its registry mirror
// (see isNestedSecondary). Deleting an owner out from under them leaves them
// attached to a network whose registry container is gone, so any remediation
// that recreates the owner has to account for them.
func dependentSecondaries(c ClusterEntity, declared []ClusterEntity) []string {
	var names []string
	for _, other := range declared {
		if other.Name != c.Name && isNestedSecondary(other) && strings.TrimPrefix(other.Network, "k3d-") == c.Name {
			names = append(names, other.Name)
		}
	}
	return names
}

// recreateClusterCommand builds the remediation command line for a cluster
// that must be recreated. A cluster that OWNS nested secondaries cannot just
// be deleted: k3d cannot remove a docker network the secondaries are still
// attached to, and a secondary left behind keeps pointing at a registry
// container that no longer exists. forge already knows the ownership edge (it
// derives it from the secondary's `owner`), so the command it prints names
// the dependents instead of leaving the operator to discover them by breaking
// them. `forge env up` recreates every declared cluster in order afterwards.
func recreateClusterCommand(c ClusterEntity, declared []ClusterEntity, env string) string {
	targets := append(dependentSecondaries(c, declared), c.Name)
	return fmt.Sprintf("k3d cluster delete %s && forge env up %s", strings.Join(targets, " "), env)
}

// effectiveServers defaults a zero Servers to 1 (the schema default;
// belt-and-suspenders for a hand-constructed entity that skipped it).
func effectiveServers(c ClusterEntity) int {
	if c.Servers <= 0 {
		return 1
	}
	return c.Servers
}

// inheritRegistryMirror ports scripts/bootstrap-dev-daemon.sh's
// registries.yaml load-order fix to Go: copy the OWNER cluster's
// containerd registries.yaml onto THIS cluster's server node and restart
// it so containerd reloads with the mirror. The owner is the cluster
// whose auto-created network this one joined — k3d names that network
// `k3d-<owner>`, so the owner name is Network with the `k3d-` prefix
// stripped. forge owns the owner cluster's registry container, so the
// mapping is deterministic.
//
// Without this, in-cluster pulls of `localhost:<port>/<image>` from the
// secondary cluster's node fail (the registry endpoint is only resolvable
// via the shared mirror config). Copying the EXACT same registries.yaml
// the owner uses keeps the two clusters' pull behavior identical.
func inheritRegistryMirror(ctx context.Context, c ClusterEntity) error {
	owner := strings.TrimPrefix(c.Network, "k3d-")
	if owner == "" {
		return fmt.Errorf("cluster %q: cannot derive owner cluster from network %q", c.Name, c.Network)
	}
	ownerNode := "k3d-" + owner + "-server-0"
	newNode := "k3d-" + c.Name + "-server-0"

	// Read the owner node's registries.yaml. The owner is forge-created
	// (it carries the canonical mirror config), so this file exists.
	regsYAML, err := readNodeFileFn(ctx, ownerNode, "/etc/rancher/k3s/registries.yaml")
	if err != nil {
		// Fall back to forge's canonical fallback mirror config: an owner
		// created from a deploy/k3d.yaml carries the mirrors inline in
		// containerd's config rather than a standalone registries.yaml, so
		// a read miss isn't fatal — write the known-good fallback the
		// owner's inline config mirrors.
		regsYAML = []byte(fallbackRegistriesYAML)
	}
	if len(bytes.TrimSpace(regsYAML)) == 0 {
		regsYAML = []byte(fallbackRegistriesYAML)
	}

	fmt.Printf("  mirroring %s registries.yaml onto %s and reloading...\n", ownerNode, newNode)
	if err := writeNodeFileFn(ctx, newNode, "/etc/rancher/k3s/registries.yaml", regsYAML); err != nil {
		return fmt.Errorf("write registries.yaml onto %s: %w", newNode, err)
	}
	if err := dockerRestartFn(ctx, newNode); err != nil {
		return fmt.Errorf("restart %s: %w", newNode, err)
	}
	// k3d's load balancer resolves the server node name when nginx starts.
	// If the disposable tools node was removed during hung-client recovery,
	// restarting the server can reuse that freed IP. Refresh the load balancer
	// before the readiness probe or it keeps proxying the API port to the old
	// address until some later manual restart.
	if err := refreshLoadBalancerFn(ctx, c.Name); err != nil {
		return fmt.Errorf("refresh %s load balancer after node restart: %w", c.Name, err)
	}
	// Health-and-REPAIR, not a bare wait. The `docker restart` above is the
	// single most likely trigger of Docker node-IP drift in all of forge: the
	// node gives up its address and, on a shared k3d network with sibling
	// containers, frequently comes back on a different one — at which point
	// k3s exits with "failed to find interface with specified node ip" and the
	// node never reports Ready again. Waiting alone turns a failure forge
	// already knows how to repair into an opaque timeout on the CREATE path,
	// which is the first thing a new user runs.
	if err := ensureRunningClusterHealthyFn(ctx, c); err != nil {
		return fmt.Errorf("wait %s ready after reload: %w", c.Name, err)
	}
	return nil
}

// containerStartedAt reads a container's current start time. A missing
// container is reported as a zero time with no error: callers treat "not
// there" as nothing to reconcile, matching refreshK3dLoadBalancer.
func containerStartedAt(ctx context.Context, container string) (time.Time, error) {
	out, err := exec.CommandContext(ctx, "docker", "container", "inspect", container,
		"--format", "{{.State.StartedAt}}").CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "No such object") || strings.Contains(string(out), "No such container") {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("inspect %s: %w: %s", container, err, strings.TrimSpace(string(out)))
	}
	started, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(out)))
	if err != nil {
		return time.Time{}, fmt.Errorf("parse start time for %s: %w", container, err)
	}
	return started, nil
}

// loadBalancerNeedsRefresh is the decision ensureClusterLoadBalancerFresh
// acts on, split out so it can be pinned without a Docker daemon. A zero
// time means the container is absent: a cluster with no load balancer (or no
// server-0 by that name) has no cached upstream that can go stale.
func loadBalancerNeedsRefresh(lbStarted, nodeStarted time.Time) bool {
	if lbStarted.IsZero() || nodeStarted.IsZero() {
		return false
	}
	return nodeStarted.After(lbStarted)
}

// ensureClusterLoadBalancerFresh repairs a k3d serverlb that may be proxying
// to a stale address, and is the Kubernetes-path counterpart to the node
// health check: k3s can be perfectly healthy while the only route to its API
// is broken.
//
// k3d's serverlb is nginx, configured by confd at container start with the
// server node's name as the upstream. That config carries NO `resolver`
// directive, so nginx's stream module resolves each upstream hostname ONCE at
// startup and caches the answer for the life of the process. If the node then
// gets a different Docker IP — which happens whenever containers on a shared
// k3d network are recreated in a different order — nginx keeps proxying to the
// address it cached. Every upstream is `max_fails=1 fail_timeout=10s`, so the
// symptom is not a clean outage: the mapped host ports (the Kubernetes API
// among them) go to "connection refused" in ~10-second windows and recover on
// their own, which reads to the user as a flaky cluster or a hung forge.
//
// The signal is container start ORDER. nginx cached whatever the node name
// resolved to when the LOAD BALANCER started, so a server node that started
// AFTER it may well have an address the LB never saw. That is a heuristic, not
// a proof — the node can restart and reclaim the same IP, in which case the
// cached answer is still right and this restart was unnecessary. The trade is
// deliberate: the false positive costs one container restart during a phase
// that is already reconciling the cluster, while the false negative costs an
// intermittent, essentially undiagnosable failure that outlives every retry.
//
// Self-limiting, so warm runs stay quiet: refreshing makes the LB the newer
// container, and the check does not fire again until the node next restarts.
func ensureClusterLoadBalancerFresh(ctx context.Context, clusterName string) error {
	lb := "k3d-" + clusterName + "-serverlb"
	node := "k3d-" + clusterName + "-server-0"

	lbStarted, err := containerStartedAt(ctx, lb)
	if err != nil {
		return err
	}
	nodeStarted, err := containerStartedAt(ctx, node)
	if err != nil {
		return err
	}
	if !loadBalancerNeedsRefresh(lbStarted, nodeStarted) {
		return nil
	}

	fmt.Printf("  %s started %s after %s, so its cached upstream address may be stale — refreshing...\n",
		node, nodeStarted.Sub(lbStarted).Round(time.Millisecond), lb)
	return refreshK3dLoadBalancer(ctx, clusterName)
}

func refreshK3dLoadBalancer(ctx context.Context, clusterName string) error {
	lb := "k3d-" + clusterName + "-serverlb"
	out, err := exec.CommandContext(ctx, "docker", "container", "inspect", lb).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "No such object") || strings.Contains(string(out), "No such container") {
			return nil
		}
		return fmt.Errorf("inspect %s: %w", lb, err)
	}
	// Callers state WHY they are refreshing; this says only what is always
	// true of the act itself — nginx re-resolves the server node on start.
	fmt.Printf("  restarting %s so it re-resolves the server node...\n", lb)
	return dockerRestart(ctx, lb)
}

// setupSecondaryClusterNode performs the node setup a SECONDARY cluster
// (nested on an owner's docker network) needs but that `k3d cluster
// create` does not do — beyond the registry mirror inheritRegistryMirror
// already handles. Two steps, both idempotent (a guard precedes each
// mutation so warm re-runs no-op):
//
//  1. HOST-GATEWAY DNS. The owner cluster reaches host services (a
//     host-mode Postgres/NATS, etc.) via `host.k3d.internal`, which k3d
//     seeds in the OWNER node's /etc/hosts and CoreDNS NodeHosts. The
//     secondary, joined to the owner's network, gets no such alias — and
//     the registry-mirror node restart regenerates CoreDNS from
//     /etc/hosts, dropping anything k3d injected. Without it the
//     secondary's pods fail "lookup host.k3d.internal: no such host". We
//     copy the alias FROM the owner cluster's CoreDNS NodeHosts (rather
//     than hardcoding a host-gateway IP) so it tracks whatever gateway IP
//     this docker host actually uses, then write it into the secondary's
//     node /etc/hosts + CoreDNS NodeHosts and bounce CoreDNS.
//
//  2. TCP MSS CLAMP. A secondary nested on the owner's docker network has
//     a lower effective egress path-MTU (pod → secondary node → shared
//     bridge → host NAT) than the pod's interface MTU. Large TLS
//     handshake packets are dropped with DF set, so outbound TLS (e.g. a
//     git clone over HTTPS) dies "gnutls_handshake() failed: The TLS
//     connection was non-properly terminated." Clamp TCP MSS on the
//     node's FORWARD chain so sessions negotiate a fitting segment.
//     Applied AFTER the registry-mirror node restart — iptables state
//     does not survive it. Idempotent via an iptables -C check.
func setupSecondaryClusterNode(ctx context.Context, c ClusterEntity) error {
	owner := strings.TrimPrefix(c.Network, "k3d-")
	if owner == "" {
		return fmt.Errorf("cluster %q: cannot derive owner cluster from network %q", c.Name, c.Network)
	}
	node := "k3d-" + c.Name + "-server-0"

	if err := ensureHostGatewayDNS(ctx, c, owner, node); err != nil {
		return fmt.Errorf("ensure host-gateway DNS on %s: %w", node, err)
	}
	if err := ensureMSSClamp(ctx, node); err != nil {
		return fmt.Errorf("clamp TCP MSS on %s: %w", node, err)
	}
	return nil
}

const k3dHostGatewayAlias = "host.k3d.internal"

// ensureClusterHostGatewayDNS repairs the host alias k3d injects at cluster
// creation but can lose when Docker or a k3d node restarts. The host-gateway
// address is resolved through Docker's own host-gateway token using the
// already-local k3s node image, so this works without hardcoding a Docker
// Desktop- or Linux-specific address. Both the node's /etc/hosts and CoreDNS
// are converged so a later k3s process restart does not immediately lose the
// repaired record again.
func ensureClusterHostGatewayDNS(ctx context.Context, clusterName string) error {
	node := "k3d-" + clusterName + "-server-0"
	kctx := "k3d-" + clusterName

	currentIP, err := resolveDockerHostGatewayIP(ctx, node)
	if err != nil {
		return err
	}

	nodeHosts, err := readNodeFile(ctx, node, "/etc/hosts")
	if err != nil {
		return fmt.Errorf("read %s /etc/hosts: %w", node, err)
	}
	if nodeHostsIPFor(string(nodeHosts), k3dHostGatewayAlias) != currentIP {
		// currentIP is validated by net.ParseIP before it reaches this
		// fixed shell fragment; the alias is a compile-time constant.
		upsert := fmt.Sprintf(
			"tmp=/tmp/forge-hosts.$$; { grep -v '[[:space:]]host[.]k3d[.]internal\\([[:space:]]\\|$\\)' /etc/hosts || true; printf '%%s\\n' '%s %s'; } > \"$tmp\" && cat \"$tmp\" > /etc/hosts; rc=$?; rm -f \"$tmp\"; exit $rc",
			currentIP, k3dHostGatewayAlias)
		if err := execNodeShell(ctx, node, upsert); err != nil {
			return fmt.Errorf("upsert %s in %s /etc/hosts: %w", k3dHostGatewayAlias, node, err)
		}
	}

	coreDNSHosts, err := readCoreDNSNodeHosts(ctx, kctx)
	if err != nil {
		return err
	}
	if nodeHostsIPFor(coreDNSHosts, k3dHostGatewayAlias) == currentIP {
		fmt.Printf("  %s resolves to Docker host gateway %s in cluster %q — no-op\n",
			k3dHostGatewayAlias, currentIP, clusterName)
		return nil
	}

	newHosts := upsertNodeHostsAlias(coreDNSHosts, k3dHostGatewayAlias, currentIP)
	fmt.Printf("  restoring %s=%s in cluster %q CoreDNS and bouncing CoreDNS...\n",
		k3dHostGatewayAlias, currentIP, clusterName)
	if err := patchCoreDNSNodeHosts(ctx, kctx, newHosts); err != nil {
		return err
	}
	return rolloutRestartCoreDNS(ctx, kctx)
}

// resolveDockerHostGatewayIP asks the Docker daemon to expand its special
// host-gateway token. Reusing the node's already-present image keeps this
// offline and avoids depending on a utility image being installed.
func resolveDockerHostGatewayIP(ctx context.Context, node string) (string, error) {
	imageOut, err := exec.CommandContext(ctx, "docker", "container", "inspect", node,
		"--format", "{{.Config.Image}}").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("inspect image for %s: %w: %s", node, err, strings.TrimSpace(string(imageOut)))
	}
	image := strings.TrimSpace(string(imageOut))
	if image == "" {
		return "", fmt.Errorf("inspect image for %s returned an empty image", node)
	}

	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--pull=never", "--network", "none",
		"--add-host", k3dHostGatewayAlias+":host-gateway", "--entrypoint", "grep",
		image, k3dHostGatewayAlias, "/etc/hosts").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("resolve Docker host-gateway using %s: %w: %s", image, err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[1] != k3dHostGatewayAlias || net.ParseIP(fields[0]) == nil {
		return "", fmt.Errorf("docker returned invalid host-gateway record %q", strings.TrimSpace(string(out)))
	}
	return fields[0], nil
}

// ensureHostGatewayDNS copies the `host.k3d.internal` alias from the OWNER
// cluster's CoreDNS NodeHosts onto the secondary cluster's node /etc/hosts
// and CoreDNS NodeHosts, then bounces CoreDNS. Idempotent: a no-op when
// the secondary already resolves the alias. Reading the IP from the owner
// (instead of hardcoding the Docker Desktop gateway) keeps it portable
// across docker hosts.
func ensureHostGatewayDNS(ctx context.Context, c ClusterEntity, owner, node string) error {
	const alias = "host.k3d.internal"
	ownerKctx := "k3d-" + owner
	secKctx := "k3d-" + c.Name

	// Resolve the host-gateway line from the owner's CoreDNS NodeHosts —
	// e.g. "192.168.65.254 host.k3d.internal". This is the source of truth
	// for whatever gateway IP this docker host assigned.
	ownerHosts, err := readCoreDNSNodeHosts(ctx, ownerKctx)
	if err != nil {
		return fmt.Errorf("read owner %s CoreDNS NodeHosts: %w", ownerKctx, err)
	}
	gwLine := nodeHostsLineFor(ownerHosts, alias)
	if gwLine == "" {
		// The owner doesn't advertise the alias (e.g. a docker host where
		// k3d didn't seed it). Nothing to copy; skip rather than guess an IP.
		fmt.Printf("  owner cluster %q has no %s alias — skipping host-gateway DNS for %q\n", owner, alias, c.Name)
		return nil
	}

	// Node /etc/hosts: append the line if absent.
	addHosts := fmt.Sprintf("grep -q %q /etc/hosts || echo %q >> /etc/hosts", alias, gwLine)
	if err := execNodeShell(ctx, node, addHosts); err != nil {
		return fmt.Errorf("append %s to %s /etc/hosts: %w", alias, node, err)
	}

	// CoreDNS NodeHosts: append the line + bounce CoreDNS, only if absent.
	secHosts, err := readCoreDNSNodeHosts(ctx, secKctx)
	if err != nil {
		return fmt.Errorf("read %s CoreDNS NodeHosts: %w", secKctx, err)
	}
	if nodeHostsLineFor(secHosts, alias) != "" {
		fmt.Printf("  %s already resolves %s in %q — no-op\n", alias, alias, c.Name)
		return nil
	}
	newHosts := strings.TrimRight(secHosts, "\n") + "\n" + gwLine
	fmt.Printf("  adding %q to cluster %q CoreDNS NodeHosts (copied from owner %q) and bouncing CoreDNS...\n", gwLine, c.Name, owner)
	if err := patchCoreDNSNodeHosts(ctx, secKctx, newHosts); err != nil {
		return err
	}
	return rolloutRestartCoreDNS(ctx, secKctx)
}

// ensureMSSClamp adds a TCP MSS-clamp rule to the node's mangle/FORWARD
// chain, idempotently via an iptables -C check (append only when the
// rule isn't already present).
func ensureMSSClamp(ctx context.Context, node string) error {
	const rule = "-p tcp --tcp-flags SYN,RST SYN -j TCPMSS --set-mss 1360"
	// `iptables -C` (check) exits non-zero when the rule is absent; only
	// then do we append. Run as one shell so the check+append is atomic
	// from the host's perspective.
	clamp := fmt.Sprintf(
		"iptables -t mangle -C FORWARD %s 2>/dev/null || iptables -t mangle -A FORWARD %s",
		rule, rule)
	fmt.Printf("  clamping TCP MSS on %s (nested-network MTU fix for outbound TLS)...\n", node)
	return execNodeShell(ctx, node, clamp)
}

// readCoreDNSNodeHosts returns the kube-system/coredns ConfigMap's
// NodeHosts data for the given kubectl context (empty string when unset).
func readCoreDNSNodeHosts(ctx context.Context, kctx string) (string, error) {
	out, err := exec.CommandContext(ctx, "kubectl", "--context", kctx,
		"get", "configmap", "coredns", "-n", "kube-system",
		"-o", "jsonpath={.data.NodeHosts}").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return "", fmt.Errorf("kubectl read %s kube-system/coredns: %w: %s", kctx, err, detail)
		}
		return "", fmt.Errorf("kubectl read %s kube-system/coredns: %w", kctx, err)
	}
	return string(out), nil
}

// nodeHostsLineFor returns the (trimmed) NodeHosts line whose host column
// contains the alias, or "" when absent. NodeHosts is `<ip> <name>...`
// lines; we match on a whitespace-delimited field so a substring like
// `host.k3d.internal` doesn't false-match a longer hostname.
func nodeHostsLineFor(nodeHosts, alias string) string {
	for _, line := range strings.Split(nodeHosts, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// fields[0] is the IP; the rest are hostnames for that IP.
		for _, host := range fields[1:] {
			if host == alias {
				return strings.TrimSpace(line)
			}
		}
	}
	return ""
}

func nodeHostsIPFor(nodeHosts, alias string) string {
	line := nodeHostsLineFor(nodeHosts, alias)
	fields := strings.Fields(line)
	if len(fields) < 2 || net.ParseIP(fields[0]) == nil {
		return ""
	}
	return fields[0]
}

// upsertNodeHostsAlias replaces any old occurrence of alias while preserving
// unrelated names and records, then appends one canonical current record.
func upsertNodeHostsAlias(nodeHosts, alias, ip string) string {
	lines := make([]string, 0, strings.Count(nodeHosts, "\n")+1)
	for _, raw := range strings.Split(nodeHosts, "\n") {
		fields := strings.Fields(raw)
		if len(fields) < 2 {
			continue
		}
		kept := []string{fields[0]}
		for _, host := range fields[1:] {
			if host != alias {
				kept = append(kept, host)
			}
		}
		if len(kept) > 1 {
			lines = append(lines, strings.Join(kept, " "))
		}
	}
	lines = append(lines, ip+" "+alias)
	return strings.Join(lines, "\n") + "\n"
}

// patchCoreDNSNodeHosts writes newHosts as the coredns ConfigMap's
// NodeHosts via a strategic-merge patch (avoids a get-mutate-apply race on
// the rest of the ConfigMap).
func patchCoreDNSNodeHosts(ctx context.Context, kctx, newHosts string) error {
	patch := map[string]any{"data": map[string]string{"NodeHosts": newHosts}}
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "kubectl", "--context", kctx,
		"patch", "configmap", "coredns", "-n", "kube-system",
		"--type", "merge", "-p", string(body))
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// rolloutRestartCoreDNS bounces the coredns deployment so it reloads the
// patched NodeHosts.
func rolloutRestartCoreDNS(ctx context.Context, kctx string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "--context", kctx,
		"rollout", "restart", "deployment/coredns", "-n", "kube-system")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "kubectl", "--context", kctx,
		"rollout", "status", "deployment/coredns", "-n", "kube-system",
		"--timeout=60s").CombinedOutput()
	if err != nil {
		return fmt.Errorf("wait for %s CoreDNS rollout: %w: %s", kctx, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// execNodeShell runs a /bin/sh -c command inside a k3d node container.
func execNodeShell(ctx context.Context, node, script string) error {
	cmd := exec.CommandContext(ctx, "docker", "exec", node, "sh", "-c", script)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// readNodeFile cats a file from inside a k3d node container.
func readNodeFile(ctx context.Context, node, path string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "docker", "exec", node, "cat", path).Output()
	if err != nil {
		return nil, fmt.Errorf("docker exec %s cat %s: %w", node, path, err)
	}
	return out, nil
}

// writeNodeFile writes content to a path inside a k3d node container by
// piping it to `sh -c 'cat > path'` (the node image has no `tee`/`cp`
// from host). Matches the bootstrap script's heredoc approach.
func writeNodeFile(ctx context.Context, node, path string, content []byte) error {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", node, "sh", "-c", "cat > "+path)
	cmd.Stdin = bytes.NewReader(content)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// dockerRestart restarts a container (the k3d node) so containerd
// reloads its registries.yaml.
func dockerRestart(ctx context.Context, node string) error {
	cmd := exec.CommandContext(ctx, "docker", "restart", node)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// nodeReadyWaitBudget caps waitNodeReady, nodeReadyAttemptBudget caps one
// attempt inside it, and nodeReadyRetryInterval spaces them. Vars, not
// consts, so tests can shorten them; nothing in production reassigns them.
var (
	nodeReadyWaitBudget    = 90 * time.Second
	nodeReadyAttemptBudget = 15 * time.Second
	nodeReadyRetryInterval = 2 * time.Second

	// nodeReadyReportInterval paces the PROGRESS OUTPUT, which is not the same
	// question as how fast to retry. A refused connection fails instantly, so
	// retrying every 2s is right — it returns the moment the API comes back —
	// but REPORTING every 2s turns a blip the loop is designed to absorb into a
	// wall of alarming text, and an operator reasonably reads that as a hard
	// failure and kills a run that was seconds from succeeding. Report the
	// first failure at once (the reason is what makes the wait legible), then
	// settle into a calm cadence.
	nodeReadyReportInterval = 15 * time.Second
)

// kubectlWaitNodesReady is one bounded attempt: the seam production wires to
// kubectl and tests replace, so the retry/report loop is exercised without a
// cluster. It returns kubectl's own output as the reason on failure.
var nodeReadyAttemptFn = kubectlWaitNodesReady

func kubectlWaitNodesReady(ctx context.Context, kctx string, budget time.Duration) (string, error) {
	attemptCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	out, err := exec.CommandContext(attemptCtx, "kubectl", "--context", kctx,
		"wait", "--for=condition=Ready", "nodes", "--all",
		"--timeout="+budget.String()).CombinedOutput()
	if err != nil {
		return string(out), err
	}
	// Every caller waits right after a node (re)start, which is exactly when
	// a Ready condition left over from the previous boot is still on record.
	if err := verifyNodesReadyThisBoot(attemptCtx, kctx); err != nil {
		return err.Error(), err
	}
	return string(out), nil
}

// waitNodeReady blocks (bounded) until the cluster's nodes report Ready
// after a node restart — containerd needs a beat to come back. Uses the
// k3d context convention (k3d-<name>) so the wait targets the right
// cluster regardless of the active kubectl context. Best-effort with a
// hard cap; a slow-to-Ready node surfaces downstream as a deploy
// rollout failure with its own diagnostics rather than hanging here.
//
// It REPORTS while it waits. A wait that prints nothing for a minute and a
// half is indistinguishable from a hang, which is the one thing an operator
// watching `forge env up` most needs to tell apart — and the reason each
// attempt failed is the evidence that says which of the two it is. Each
// attempt is sized from the REMAINING budget, so the cap is the true
// wall-clock cap rather than the cap plus one attempt.
func waitNodeReady(ctx context.Context, clusterName string) error {
	return waitNodeReadyWhile(ctx, clusterName, nil)
}

// waitNodeReadyWhile is waitNodeReady for a caller that can tell whether the
// thing it is waiting on is still alive. After each failed attempt it asks
// alive; a definite "no" ends the wait with errK3sExitedDuringWait instead of
// spending the rest of the budget on a process that will never answer. A nil
// alive, or one that errors, never shortens the wait.
func waitNodeReadyWhile(ctx context.Context, clusterName string, alive func(context.Context) (bool, error)) error {
	kctx := "k3d-" + clusterName
	start := time.Now()
	deadline := start.Add(nodeReadyWaitBudget)
	var lastOut string
	var lastReport time.Time
	for {
		budget := min(time.Until(deadline), nodeReadyAttemptBudget)
		if budget <= 0 {
			return fmt.Errorf("nodes not Ready within %s (context %s)%s",
				nodeReadyWaitBudget, kctx, formatCommandStderr(lastOut))
		}
		out, err := nodeReadyAttemptFn(ctx, kctx, budget.Round(time.Second))
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		lastOut = out
		if lastOut == "" {
			lastOut = err.Error()
		}
		if alive != nil {
			if running, aliveErr := alive(ctx); aliveErr == nil && !running {
				return fmt.Errorf("%w (context %s)%s", errK3sExitedDuringWait, kctx, formatCommandStderr(lastOut))
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
		}
		if now := time.Now(); lastReport.IsZero() || now.Sub(lastReport) >= nodeReadyReportInterval {
			lastReport = now
			fmt.Printf("    %s: not answering yet, still retrying (%s of %s)%s\n",
				kctx, time.Since(start).Round(time.Second), nodeReadyWaitBudget,
				formatCommandStderr(lastOut))
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(nodeReadyRetryInterval):
		}
	}
}
