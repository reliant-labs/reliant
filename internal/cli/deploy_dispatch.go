package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/deploy"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// buildDeployGroups walks the rendered entities and produces the deploy
// groups the dispatcher will dispatch in turn. Placement is PER WORKLOAD
// (ADR 0002): each workload's own runtime decides its group, so one env may
// produce a cluster group, a compose group, host-infra and a hosted group
// side by side.
//
//   - runtime cluster → a k8s-cluster group per (cluster, namespace,
//     registry). The namespace defaults to fallbackNamespace when KCL leaves
//     it blank.
//   - runtime compose → a compose group per compose file.
//   - runtime hosted  → the ONE hosted group, together with hosted
//     ManagedDatabases and OnHosted frontends. It requires the
//     Bundle's control_plane; its target (endpoint, release, digests) is
//     filled in by the caller, which owns the credential and the ledger read.
//   - runtime host / build-only → no group (forge env up / forge build).
//   - Bundle.infra → the host-infra group.
//
// buildDeployGroupsWithOpts is the dry-run-aware variant.
func buildDeployGroupsWithOpts(envName string, entities *KCLEntities, fallbackNamespace string, dryRun bool) ([]deploytarget.ServiceGroup, error) {
	groups, err := buildDeployGroups(envName, entities, fallbackNamespace)
	if err != nil {
		return nil, err
	}
	if dryRun {
		for i := range groups {
			groups[i].DryRun = true
		}
	}
	return groups, nil
}

func buildDeployGroups(envName string, entities *KCLEntities, fallbackNamespace string) ([]deploytarget.ServiceGroup, error) {
	if entities == nil {
		return nil, nil
	}
	// Resolve the bundle's secret provider once. For a dotenv provider,
	// All() returns the resolved key→value map we inline into the runtime
	// env of compose workloads (env_file overrides on conflict — see
	// compose.go deployOne). External/none providers return nil (those
	// resolve secrets out-of-band). Cluster workloads deliberately do NOT
	// receive this map: they get rendered Secret objects + secretKeyRef.
	prov, err := secretProviderFromEntities(entities, projectDirForKCL())
	if err != nil {
		return nil, fmt.Errorf("secret provider: %w", err)
	}
	secretEnv := prov.All()

	var raw []deploytarget.RawService
	for _, w := range entities.Workloads {
		switch w.Runtime.Type {
		case RuntimeCluster:
			c := w.Runtime.Cluster
			namespace := c.Namespace
			if namespace == "" {
				namespace = fallbackNamespace
			}
			var ports []int
			for _, p := range w.Spec.Ports {
				ports = append(ports, int(p.Port))
			}
			replicas := int(w.Spec.Replicas)
			if replicas == 0 {
				replicas = int(v1alpha1.DefaultReplicas)
			}
			raw = append(raw, deploytarget.RawService{
				Name: w.Name,
				K8sCluster: &deploytarget.RawK8sCluster{
					Cluster:   c.Cluster,
					Namespace: namespace,
					Registry:  c.Registry,
					Domain:    c.Domain,
					Spec: &deploytarget.K8sClusterSpec{
						Replicas: replicas,
						Platform: c.Platform,
						Ports:    ports,
						// The PVC this workload's render CREATES, named for
						// the observer: an unbound claim shows up on the
						// Deployment only as "0/1 ready", which names the
						// symptom and not the cause.
						OwnedClaims: workloadOwnedClaims(w),
					},
				},
			})
		case RuntimeCompose:
			cm := w.Runtime.Compose
			spec := composeSpecFor(projectDirForKCL(), envName, cm)
			withComposeFileEnv(spec, cm, entities)
			svcSecrets := secretEnv
			if composeServiceName(w) == clickstack.ForwarderService {
				// The forwarder reads its destination headers from one fixed
				// variable (the store key is the author's) and gets nothing
				// else from the store.
				svcSecrets = forwarderHeadersEnv(entities, secretEnv)
			}
			raw = append(raw, deploytarget.RawService{
				Name:    w.Name,
				Compose: spec,
				Secrets: svcSecrets,
			})
		default:
			// host, build-only — no deploy group; hosted — below.
		}
	}
	for _, hi := range entities.Infra {
		// No Secrets layer: a host-infra instance's credentials are the
		// ones the declaration states, and the app's DSN is composed from
		// the SAME declaration. Injecting a secret store's values here
		// would give forge a second, independent opinion about the
		// password — which is how the two ever disagree.
		raw = append(raw, deploytarget.RawService{
			Name: hi.Name,
			HostInfra: &deploytarget.HostInfraSpec{
				Engine:          hi.Engine,
				Port:            hi.Port,
				Database:        hi.Database,
				User:            hi.User,
				Password:        hi.Password,
				DataDir:         hi.DataDir,
				Version:         hi.Version,
				IDPDatabase:     hi.IDPDatabase,
				IDPDatabasePort: hi.IDPDatabasePort,
				IDPMasterKey:    hi.IDPMasterKey,
				IDPStepsFile:    hi.IDPStepsFile,
				IDPPATPath:      hi.IDPPATPath,

				NATSMonitorPort:    hi.NATSMonitorPort,
				TemporalNamespaces: hi.TemporalNamespaces,
				TemporalUIPort:     hi.TemporalUIPort,
			},
		})
	}
	groups, err := deploytarget.GroupServices(envName, raw)
	if err != nil {
		return nil, err
	}
	groups = joinManifestClusterGroups(envName, groups, entities, fallbackNamespace)
	hosted, err := buildHostedGroup(envName, entities)
	if err != nil {
		return nil, err
	}
	if hosted != nil {
		groups = append(groups, *hosted)
	}
	return groups, nil
}

// joinManifestClusterGroups makes every kubectl context the rendered stream
// stamps an object onto (forge.dev/cluster) a deploy destination. A context
// that already has a k8s group (a workload runs there) is joined as-is; one
// with no workload — a forge.Manifests group, a cluster database or a
// secondary Namespace on an otherwise empty cluster — gets a k8s group of its
// own with no services. Without it the router has no group for that cluster,
// the render attributes its objects to another cluster, and the deploy never
// applies them anywhere.
//
// The group needs only a context and a namespace: registry and domain belong
// to images and routes, which a manifests-only cluster has none of. The
// namespace is the one its objects name, else the primary target's, else the
// deploy's fallback.
func joinManifestClusterGroups(envName string, groups []deploytarget.ServiceGroup, entities *KCLEntities, fallbackNamespace string) []deploytarget.ServiceGroup {
	if entities == nil || len(entities.ManifestClusters) == 0 {
		return groups
	}
	covered := map[string]bool{}
	for _, g := range groups {
		if g.ProviderID == "k8s-cluster" && g.Cluster != "" {
			covered[g.Cluster] = true
		}
	}
	added := false
	for _, mc := range entities.ManifestClusters {
		if covered[mc.Cluster] {
			continue
		}
		covered[mc.Cluster] = true
		namespace := mc.Namespace
		if namespace == "" {
			namespace = entities.ClusterTarget.field("namespace")
		}
		if namespace == "" {
			namespace = fallbackNamespace
		}
		groups = append(groups, deploytarget.ServiceGroup{
			Env:        envName,
			ProviderID: "k8s-cluster",
			Cluster:    mc.Cluster,
			Namespace:  namespace,
		})
		added = true
	}
	if added {
		// Same order GroupServices produces (provider|cluster|…), so a
		// manifests-only cluster sorts among the others deterministically.
		sort.SliceStable(groups, func(i, j int) bool {
			return groupSortKey(groups[i]) < groupSortKey(groups[j])
		})
	}
	return groups
}

// groupSortKey is the key deploytarget.GroupServices orders k8s groups by;
// other providers keep their relative order behind it.
func groupSortKey(g deploytarget.ServiceGroup) string {
	if g.ProviderID == "k8s-cluster" {
		return "k8s-cluster|" + g.Cluster + "|" + g.Namespace + "|" + g.Registry
	}
	return g.ProviderID
}

// workloadOwnedClaims names the PersistentVolumeClaims forge emits for one
// cluster workload: one, the renderer's own PVC name, when the workload
// declares storage, and none otherwise. The name comes from
// pkg/deploy.PVCName, the SAME function RenderWorkloads names the claim with,
// so the observer and the renderer cannot disagree about it.
func workloadOwnedClaims(w WorkloadEntity) []string {
	if w.Spec.StorageGiB <= 0 {
		return nil
	}
	return []string{deploy.PVCName(w.Name)}
}

// splitHostedGroups separates the hosted group (published to the control
// plane) from the groups applied from this machine.
func splitHostedGroups(groups []deploytarget.ServiceGroup) (local, hosted []deploytarget.ServiceGroup) {
	for _, g := range groups {
		if g.ProviderID == deploytarget.HostedProviderID {
			hosted = append(hosted, g)
			continue
		}
		local = append(local, g)
	}
	return local, hosted
}

// rolloutApplier is a provider whose deploy is an apply followed by a wait
// for the target to converge, and which can do the apply alone. The k8s
// cluster provider is one: its wait is the rollout wait, and splitting it off
// is what lets a deploy apply every cluster before it waits on any.
type rolloutApplier interface {
	ApplyNoWait(ctx context.Context, group deploytarget.ServiceGroup) (*cluster.PendingRollout, error)
}

// dispatchDeployGroups runs every group through its provider. Per-
// group failures abort the loop (deploy.go's pre-v2 behavior was
// fail-fast on the single apply) but each failure is wrapped to
// include the provider id + group target so users can tell at a
// glance which group failed.
//
// A run of consecutive cluster groups is APPLIED in turn and then awaited
// TOGETHER, with one cluster.WaitRollouts. One env may span several clusters
// with readiness dependencies between them — a workload on the first cluster
// that cannot become ready until one on the second is running — and awaiting
// each cluster before applying the next deadlocks that env on every fresh
// deploy: the first wait expires on a dependency that was never sent, and the
// second cluster is never applied. Kubernetes converges concurrently, so the
// deploy applies everything first and then reports every cluster's rollout in
// one verdict.
//
// Any other provider is a barrier: the pending cluster rollouts are awaited
// before it runs, so a group still starts only once every group ahead of it
// has finished, exactly as before.
//
// A failed group is NOT reverted: there is no rollback. The failure is
// returned as-is and recovery is roll forward — fix, then deploy again.
func dispatchDeployGroups(ctx context.Context, registry *deploytarget.Registry, groups []deploytarget.ServiceGroup) error {
	return dispatchDeployGroupsBeforeClusters(ctx, registry, groups, nil)
}

// dispatchDeployGroupsBeforeClusters is dispatchDeployGroups with one hook:
// beforeClusters runs exactly once, immediately before the FIRST cluster
// group is applied — after every infrastructure group ahead of it (compose,
// host-infra sort first) has deployed, and before any cluster workload
// exists. A nil hook, or a deploy with no cluster group, never runs it.
//
// It exists for preparation the cluster workloads depend on but that needs
// the infrastructure to already be up: creating the dev databases an
// in-cluster workload dials (ensureDevDatabaseHook). A hook error stops the
// deploy before any workload is applied, the same fail-closed rule as the
// pre-rollout Job gate.
func dispatchDeployGroupsBeforeClusters(ctx context.Context, registry *deploytarget.Registry, groups []deploytarget.ServiceGroup, beforeClusters func(context.Context) error) error {
	if registry == nil {
		return errors.New("deploy dispatch: nil provider registry")
	}
	var (
		pending    []*cluster.PendingRollout
		pendingIDs []string
		// reachedCluster is set once any group has been sent to a target. A
		// later group's "nothing was applied" refusal is then only true of
		// that group, so it must not mark the whole dispatch as untouched.
		reachedCluster bool
	)
	partial := func(err error) error {
		if reachedCluster && errors.Is(err, cluster.ErrNothingApplied) {
			return errors.New(err.Error())
		}
		return err
	}
	// awaitPending waits on every cluster applied since the last barrier.
	awaitPending := func() error {
		if len(pending) == 0 {
			return nil
		}
		err := cluster.WaitRollouts(ctx, pending...)
		ids := strings.Join(pendingIDs, "+")
		pending, pendingIDs = nil, nil
		if err != nil {
			return fmt.Errorf("deploy %s: %w", ids, err)
		}
		return nil
	}
	for _, group := range groups {
		p := registry.Lookup(group.ProviderID)
		if p == nil {
			return fmt.Errorf("deploy dispatch: no provider for %q (group: %s)", group.ProviderID, deploytarget.FormatGroupSummary(group))
		}
		if applier, ok := p.(rolloutApplier); ok {
			if beforeClusters != nil {
				if err := beforeClusters(ctx); err != nil {
					return err
				}
				beforeClusters = nil
			}
			fmt.Printf("\n%s\n", deploytarget.FormatGroupSummary(group))
			rollout, err := applier.ApplyNoWait(ctx, group)
			if err != nil {
				// A failed APPLY stops the deploy here, as it always has:
				// no later group is applied. The clusters already applied
				// keep converging (there is no rollback) but are not
				// awaited — their rollout may well depend on the cluster
				// that just failed, and waiting out its timeout would only
				// delay the error that already decides this deploy.
				return fmt.Errorf("deploy %s: %w", group.ProviderID, partial(err))
			}
			reachedCluster = true
			pending = append(pending, rollout)
			if !slices.Contains(pendingIDs, group.ProviderID) {
				pendingIDs = append(pendingIDs, group.ProviderID)
			}
			continue
		}
		if err := awaitPending(); err != nil {
			return err
		}
		fmt.Printf("\n%s\n", deploytarget.FormatGroupSummary(group))
		reachedCluster = true
		if err := p.Deploy(ctx, group); err != nil {
			return fmt.Errorf("deploy %s: %w", group.ProviderID, err)
		}
	}
	return awaitPending()
}

// applyOptsContext carries the deploy-wide envelope that
// applyOptsBuilderFromContext captures once and threads into every
// per-group cluster.ApplyOpts it emits. Grouping these into one struct
// keeps the builder's signature to a single declarative parameter.
type applyOptsContext struct {
	MainK             string
	ImageTag          string
	FallbackNamespace string
	Env               string
	EnvCfgKV          map[string]string
	DryRun            bool
	Prune             bool
	// Project names the env's owner with Env (cluster.CRDOwner), which is
	// what makes the CRD prune provable. Empty disables it.
	Project  string
	Targets  []string
	Groups   []deploytarget.ServiceGroup
	Entities *KCLEntities
	// Topology / TopologyEntities are the WHOLE env's groups and entities —
	// what the multi-cluster scope is built from. A targeted deploy
	// dispatches a subset of the env (Groups) but must route each object by
	// the env's full ownership map, or a target on one cluster turns scoping
	// off entirely. Nil falls back to Groups / Entities.
	Topology         []deploytarget.ServiceGroup
	TopologyEntities *KCLEntities
	ImageDigests     map[string]string
	HelmCharts       []cluster.HelmChartSpec
	Rollout          cluster.RolloutPolicy
	// SkipUnits are the delivery units the hub converges (Bundle.hub_converges):
	// this apply leaves their objects alone. See cluster.ApplyOpts.SkipUnits.
	SkipUnits []string
	// OnStream and OnRollout are the optional observation callbacks the
	// --json report installs (nil in text mode). Threaded through the builder
	// so a multi-group dispatch reports every group's stream and every
	// group's rollout outcomes into ONE document.
	OnStream  func(string)
	OnRollout func(cluster.RolloutObservation)
}

// applyOptsBuilderFromContext returns an ApplyOptsBuilder closure
// that captures the deploy-wide opts (mainK, image tag, env config,
// dry-run, prune, kube-context) and emits a per-group cluster.ApplyOpts.
//
// A group's Namespace is where that group's rollout is awaited (and what a
// namespace-less Job or prune is resolved against). It is never a render
// input: the env is rendered once, with the env namespace `forge env render`
// uses, and each group applies the cluster-scoped partition of that one
// stream. Rendering the env again per group with the group's namespace bound
// to KCL's `namespace` option changed what the env DECLARED — a group whose
// namespace equalled one the env declares on another cluster made two
// objects answer to one override key, and the apply failed after the
// promotion was recorded.
//
// Context is purely DECLARATIVE: every K8sCluster group already carries
// its target cluster in group.Cluster (populated from the KCL
// `forge.K8sCluster.cluster`, which IS the kubectl context name), so the
// per-group kubectl context is derived from group.Cluster. This is the
// "can't deploy the wrong env to the wrong cluster" property — the binding
// lives in the env's KCL, not in whatever context happens to be active,
// and there is no CLI override. A multi-cluster env (rare) therefore
// applies each group to ITS OWN declared cluster context. When a group
// declares no cluster (host-only / compose), Context stays empty — and the
// cluster.KubectlApply chokepoint refuses an empty context rather than
// falling back to the active one.
func applyOptsBuilderFromContext(p applyOptsContext) func(deploytarget.ServiceGroup) cluster.ApplyOpts {
	topology, topologyEntities := p.Groups, p.Entities
	if len(p.Topology) > 0 {
		topology, topologyEntities = p.Topology, p.TopologyEntities
	}
	scopeFor := clusterScopeForGroups(topology, topologyEntities)
	// The env is rendered ONCE, here, for every group this builder emits: with
	// the env namespace (FallbackNamespace is the deploy's resolved one), like
	// `forge env render`. A group never gets a render of its own.
	render := sharedEnvRender(p.MainK, p.ImageTag, p.FallbackNamespace, p.Env, p.EnvCfgKV, p.ImageDigests)
	// Platform deps (helm-as-a-RENDERER) are env-level, not per-group. To
	// apply each selected chart EXACTLY ONCE across a multi-group dispatch,
	// attach the chart specs only to the FIRST k8s group processed —
	// identified by its context. The charts carry their own target
	// namespace (helm template -n), so a single apply against any one of the
	// env's group contexts is correct for the cloud single-cluster case;
	// the once-only guard avoids re-applying cert-manager per service group.
	//
	// A chart that re-targets a SECOND cluster (HelmChartSpec.Cluster) still
	// rides this one group, and still installs into its own cluster: the
	// context is resolved PER CHART inside applyRenderedCharts, not taken from
	// the group. Keeping the attachment here group-agnostic is deliberate — a
	// re-targeted chart must be applied once whether or not the env happens to
	// declare a service group on that cluster, and an operator cluster
	// frequently has no forge-deployed service at all.
	//
	// The primary context is the env's own (declaredEnvContext: the declared
	// cluster_target, else the first group). Group order alone would pick
	// whichever context sorts lowest, which put prod's cert-manager on its
	// daemon cluster.
	primaryHelmContext := ""
	if len(p.HelmCharts) > 0 {
		primaryHelmContext = helmPrimaryContext(p.Entities, p.Groups)
	}
	return func(group deploytarget.ServiceGroup) cluster.ApplyOpts {
		ns := group.Namespace
		if ns == "" {
			ns = p.FallbackNamespace
		}
		// Emit the platform-dep specs only on the primary context's group so
		// each chart applies once. Other groups get no charts.
		var charts []cluster.HelmChartSpec
		if len(p.HelmCharts) > 0 && resolveGroupContext(group) == primaryHelmContext {
			charts = p.HelmCharts
		}
		return cluster.ApplyOpts{
			MainK:        p.MainK,
			ImageTag:     p.ImageTag,
			ImageDigests: p.ImageDigests,
			Namespace:    ns,
			Env:          p.Env,
			Context:      resolveGroupContext(group),
			EnvConfigKV:  p.EnvCfgKV,
			DryRun:       p.DryRun,
			DryRunFramed: true,
			Prune:        p.Prune,
			Project:      p.Project,
			// Always on: a CRD this env stopped rendering is otherwise
			// served forever. Apply confines it to a full, non-dry-run
			// apply, and to CRDs this (project, env) stamped.
			PruneCRDs:    true,
			Targets:      p.Targets,
			ClusterScope: scopeFor(group),
			Render:       render,
			HelmCharts:   charts,
			Rollout:      p.Rollout,
			SkipUnits:    p.SkipUnits,
			OnStream:     p.OnStream,
			OnRollout:    p.OnRollout,
		}
	}
}

// clusterScopeForGroups returns a closure that maps one k8s-cluster group
// to the cluster.GroupScope that filters the env's rendered manifest
// stream to THAT group's cluster — the load-bearing half of declared-
// cluster-only multi-cluster routing.
//
// KCL renders the whole env as a single manifest stream (every service's
// workloads, namespace, gateways, CRDs, infra). Each k8s group applies it
// to its OWN declared `--context`, so without scoping a two-cluster env
// dumps the entire bundle onto BOTH clusters: the secondary receives the
// other cluster's whole stack (and hard-fails on CRDs it doesn't have).
// The GroupScope partitions the stream so each cluster gets only the
// manifests whose OWNING service targets it — every manifest carries its
// owner's `app.kubernetes.io/name` label (workloads AND per-service owned
// manifests, including the env-level resources an image-less infra service
// pins to a cluster). There is NO primary cluster: a manifest lands on the
// cluster its owner declares, nowhere else (see
// cluster.ScopeManifestsToGroup for the per-document ownership rule).
//
// Every workload carries its OWN cluster (its OnCluster runtime), so there is
// no attribution by fallback: an operator or cron is a group member exactly
// like a service.
//
// SINGLE-CLUSTER no-op: when every k8s group declares the SAME cluster
// (the common dev-k8s / staging / prod case), there is no second cluster to
// isolate, so the closure returns nil and the apply path is byte-identical
// to the pre-scoping behaviour. Scoping engages ONLY when >1 distinct
// cluster is declared across the k8s groups.
func clusterScopeForGroups(groups []deploytarget.ServiceGroup, _ *KCLEntities) func(deploytarget.ServiceGroup) *cluster.GroupScope {
	// Distinct clusters across the k8s groups, and the apps each owns. The
	// app set per cluster includes image-less infra services (they ARE
	// cluster groups in buildDeployGroups), so an infra service's owned
	// manifests route to its declared cluster via its app label.
	clusters := map[string]struct{}{}
	appsByCluster := map[string][]string{}
	for _, g := range groups {
		if g.ProviderID != "k8s-cluster" || g.Cluster == "" {
			continue
		}
		clusters[g.Cluster] = struct{}{}
		for _, s := range g.Services {
			appsByCluster[g.Cluster] = append(appsByCluster[g.Cluster], s.Name)
		}
	}
	// Single-cluster (or no-cluster) env: nothing to isolate — no-op.
	if len(clusters) < 2 {
		return func(deploytarget.ServiceGroup) *cluster.GroupScope { return nil }
	}

	return func(group deploytarget.ServiceGroup) *cluster.GroupScope {
		if group.ProviderID != "k8s-cluster" || group.Cluster == "" {
			return nil
		}
		own := map[string]struct{}{}
		for _, name := range appsByCluster[group.Cluster] {
			own[name] = struct{}{}
		}
		other := map[string]struct{}{}
		for c, apps := range appsByCluster {
			if c == group.Cluster {
				continue
			}
			for _, name := range apps {
				other[name] = struct{}{}
			}
		}
		return &cluster.GroupScope{
			Cluster:   group.Cluster,
			OwnApps:   own,
			OtherApps: other,
		}
	}
}

// mainClusterForEntities resolves the env's MAIN cluster — the context its
// env-wide support resources (Namespace, ConfigMaps, gateways) deploy to.
// This is the Bundle's declared `cluster_target.cluster`; a contract with no
// cluster_target falls back to the first Cluster-bound workload's cluster,
// then to the first k8s group's, and "" when the env declares no cluster at
// all (host-only / compose / hosted — nothing to attribute).
func mainClusterForEntities(entities *KCLEntities, groups []deploytarget.ServiceGroup) string {
	if entities != nil {
		if c := entities.ClusterTarget.field("cluster"); c != "" {
			return c
		}
		for _, w := range entities.Workloads {
			if w.OnRuntime(RuntimeCluster) && w.Runtime.Cluster.Cluster != "" {
				return w.Runtime.Cluster.Cluster
			}
		}
	}
	for _, g := range groups {
		if g.ProviderID == "k8s-cluster" && g.Cluster != "" {
			return g.Cluster
		}
	}
	return ""
}

// resolveGroupContext picks the kubectl context for a single deploy
// group. The kubectl context is purely DECLARATIVE: the declared cluster
// (group.Cluster, from KCL `forge.K8sCluster.cluster`) IS the kubectl
// context name, and it is the ONLY source — there is no CLI override. An
// empty result means the group declares no cluster (host-only / compose);
// the cluster.KubectlApply chokepoint refuses an empty context on a write
// rather than falling back to kubectl's current context.
func resolveGroupContext(group deploytarget.ServiceGroup) string {
	return group.Cluster
}

// helmPrimaryContext is the context a chart WITHOUT its own `cluster`
// installs into: the env's declared context, provided some group in this
// dispatch targets it. The charts must ride SOME group or they are silently
// never applied, so when none does (a --target that selects only a secondary
// cluster's app) it falls back to the first group with a context.
//
// Shared by the deploy dispatch and `forge env render`, so the render
// attributes each chart to the cluster the deploy actually applies it to.
func helmPrimaryContext(entities *KCLEntities, groups []deploytarget.ServiceGroup) string {
	primary := declaredEnvContext(entities, groups)
	for _, g := range groups {
		if primary != "" && resolveGroupContext(g) == primary {
			return primary
		}
	}
	for _, g := range groups {
		if c := resolveGroupContext(g); c != "" {
			return c
		}
	}
	return ""
}

// declaredEnvContext returns the env-wide kubectl context for the
// consumers that don't iterate groups per-target: the secrets pre-apply,
// the empty-groups direct cluster.Apply, and the
// deploy preflight. It is the Bundle's declared `cluster_target.cluster`,
// falling back (for a contract that declares none) to the first declared
// K8sCluster cluster (group.Cluster, from KCL `forge.K8sCluster.cluster`) —
// there is no CLI override. Empty when no
// cluster is declared (host-only / compose); the apply chokepoint refuses
// an empty context on a write rather than using kubectl's current one.
//
// A multi-cluster env's per-group dispatch still routes each group to its
// own declared cluster via resolveGroupContext; this single value covers
// only the env-wide single-cluster paths, which already assume one
// namespace per env.
func declaredEnvContext(entities *KCLEntities, groups []deploytarget.ServiceGroup) string {
	// The Bundle's declared env target wins. Group order cannot stand in for
	// it: groups are sorted by `k8s-cluster|<context>|…`, so the "first"
	// group is whichever context sorts lowest, and a zonal GKE context
	// (`…_us-central1-a_…`) sorts ahead of a regional one (`…_us-central1_…`).
	// That once aimed prod's deploy preflight at its secondary daemon cluster.
	if entities != nil {
		if c := entities.ClusterTarget.field("cluster"); c != "" {
			return c
		}
	}
	for _, g := range groups {
		if g.ProviderID == "k8s-cluster" && g.Cluster != "" {
			return g.Cluster
		}
	}
	return ""
}

// buildHostedGroup collects everything this env publishes to the control
// plane into ONE hosted group: every workload bound to OnHosted (of any
// Restricted kind — a JOB is published as a Workload CR like any other, not
// dropped), every hosted ManagedDatabase, and every OnHosted frontend.
// nil when nothing in the env is hosted.
//
// Hosting is a property of each item, not of the env: the same env's
// cluster-bound workloads are applied from this machine in the same deploy.
// What the env must still declare is WHERE the control plane is, so a hosted
// item with no Bundle.control_plane is refused here with the fix, rather than
// published to nowhere.
func buildHostedGroup(envName string, entities *KCLEntities) (*deploytarget.ServiceGroup, error) {
	var (
		services []deploytarget.ResolvedService
		names    []string
	)
	for _, w := range entities.Workloads {
		if !w.OnRuntime(RuntimeHosted) {
			continue
		}
		spec := w.Spec
		if w.Image != "" {
			// A workload this project builds is published by its artifact
			// name: WHERE the bytes were pushed is a release fact, and the
			// plan pins `<recorded registry>/<artifact>@<digest>` from it.
			// Whatever tag the render resolved is not what a hosted
			// deploy ships.
			spec.Image = w.Image
		}
		services = append(services, deploytarget.ResolvedService{
			Name: w.Name,
			Hosted: &deploytarget.HostedWorkload{
				Tier: deploytarget.HostedTierWorkload, Workload: &spec, Artifact: hostedArtifactKey(entities, w),
			},
		})
		names = append(names, w.Name)
	}
	for _, f := range entities.Frontends {
		if !frontendIsHosted(f) {
			continue
		}
		services = append(services, deploytarget.ResolvedService{
			Name: f.Name,
			Hosted: &deploytarget.HostedWorkload{Tier: deploytarget.HostedTierStatic, Static: hostedStaticSpec(f),
				// The ledger keys a site by the repository it was pushed to,
				// so the deploy must look it up under the same key — through
				// the one rule the build pushed by, which resolves a bare
				// hosted image against the platform's base.
				Artifact: hostedStaticDestinationForDecl(entities, imageRepository(f.Image))},
		})
		names = append(names, f.Name)
	}
	for _, db := range entities.Databases {
		if !db.Hosted() {
			continue
		}
		spec := db.Spec
		services = append(services, deploytarget.ResolvedService{
			Name:   db.Name,
			Hosted: &deploytarget.HostedWorkload{Tier: deploytarget.HostedTierDatabase, Database: &spec},
		})
		names = append(names, db.Name)
	}
	if len(services) == 0 {
		return nil, nil
	}
	if entities.ControlPlane == nil {
		return nil, fmt.Errorf("env %q binds %s to the control plane (forge.OnHosted workloads / frontends, or a hosted database), "+
			"but its Bundle declares no control_plane, so there is nowhere to publish them.\n"+
			"  fix: declare `control_plane = forge.ControlPlane {...}` on the Bundle, or bind them to another runtime",
			envName, strings.Join(names, ", "))
	}
	return &deploytarget.ServiceGroup{
		Env:        envName,
		ProviderID: deploytarget.HostedProviderID,
		Services:   services,
	}, nil
}

// hostedStaticSpec projects a frontend on forge.OnHosted onto the DEPLOYED
// half of forge's StaticSiteSpec. Build inputs (public_dir, bundle) are
// consumed by `forge build` and are not part of the deployed spec, and
// OnHosted has no bucket or cdn to state: the platform owns both. The
// liveDigest is pinned later, from the bound release.
//
// keepReleases is written EXPLICITLY as forge's default. OnHosted carries no
// retention knob (it is the same runtime a workload binds), and the CR has
// always stated its retention rather than leaving the platform to infer it,
// so the published spec is byte-identical to what a hosted frontend
// published before the runtime split.
//
// runtimeConfig is the frontend's declared runtime_config with every
// forge.WorkloadURL KEPT as a reference: the control plane knows the URLs
// (it allocates them), resolves each against the named workload in the same
// environment, and writes config.js after every sync. That document is what
// the release artifact deliberately does NOT carry — see
// buildHostedStaticSites.
// The spec carries NO domains: a hosted site's custom hostname is an
// org-scoped control-plane resource bound to this environment
// (`forge domain add` + `forge domain bind`), never a field forge publishes.
//
// assetDir is the frontend's content-hashed asset directory as the render
// resolved it (declared, else the type's convention). It is what lets the
// platform cache those files forever and keep previous releases' copies
// serveable for tabs opened before a deploy — the same two guarantees
// OnFirebase renders into firebase.json.
func hostedStaticSpec(f FrontendEntity) *v1alpha1.StaticSiteSpec {
	keep := int32(v1alpha1.DefaultKeepReleases)
	spec := &v1alpha1.StaticSiteSpec{BasePath: f.BasePath, AssetDir: f.AssetDir, KeepReleases: &keep}
	if len(f.RuntimeConfigSpec) > 0 {
		spec.RuntimeConfig = f.RuntimeConfigSpec
	}
	return spec
}

// hostedArtifactKey is the ONE rule for which release artifact a hosted
// workload's digest is bound under, shared by `forge env build --release` (which
// records it) and the hosted deploy (which pins by it):
//
//   - the workload's own artifact name (`image`), when forge builds it —
//     that is the key forge's build state records a pushed image under,
//     RESOLVED against the platform's push base so a bare hosted image is
//     keyed by the address it was actually pushed to (ADR-0003 F1);
//   - otherwise the spec image's last path segment
//     (deploytarget.HostedArtifactName), for an image built elsewhere.
func hostedArtifactKey(e *KCLEntities, w WorkloadEntity) string {
	if w.Image != "" {
		// The declared REPOSITORY, host included: the release ledger's key.
		return hostedImageForDecl(e, imageRepository(w.Image))
	}
	return deploytarget.HostedArtifactName(w.Spec.Image)
}

// sharedEnvRender renders the env once, on first use, and hands the same
// stream to every caller — the groups of one deploy. It binds the env
// namespace, exactly as `forge env render` does, so the stream a group applies
// is the stream the render printed.
//
// Lazy on purpose: the first group's apply is the first render of the apply
// phase, so it still follows the secret projection and minting that precede
// it. The result (an error included) is kept: a second render of the same
// inputs could only disagree with the first.
func sharedEnvRender(mainK, imageTag, namespace, env string, envCfgKV, imageDigests map[string]string) func(context.Context) (string, error) {
	return sharedEnvRenderWith(func(ctx context.Context) (string, error) {
		return cluster.RenderManifests(ctx, mainK, imageTag, namespace, env, envCfgKV, imageDigests)
	})
}

// sharedEnvRenderWith is sharedEnvRender over any render function.
func sharedEnvRenderWith(render func(context.Context) (string, error)) func(context.Context) (string, error) {
	var (
		once sync.Once
		out  string
		err  error
	)
	return func(ctx context.Context) (string, error) {
		once.Do(func() { out, err = render(ctx) })
		return out, err
	}
}
